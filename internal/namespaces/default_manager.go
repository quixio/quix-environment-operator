package namespaces

import (
	"context"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/client-go/tools/record"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/log"

	quixiov1 "github.com/quix-analytics/quix-environment-operator/api/v1"
	"github.com/quix-analytics/quix-environment-operator/internal/status"
	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
)

// DefaultNamespaceManager is the standard implementation of NamespaceManager
type DefaultNamespaceManager struct {
	Client        client.Client
	Recorder      record.EventRecorder
	StatusUpdater status.StatusUpdater
}

// NewDefaultNamespaceManager creates a new default namespace manager
func NewDefaultNamespaceManager(client client.Client, recorder record.EventRecorder, statusUpdater status.StatusUpdater) *DefaultNamespaceManager {
	return &DefaultNamespaceManager{
		Client:        client,
		Recorder:      recorder,
		StatusUpdater: statusUpdater,
	}
}

// CreateNamespace creates a new namespace with proper metadata
func (m *DefaultNamespaceManager) CreateNamespace(ctx context.Context, env *quixiov1.Environment, name string) error {
	logger := log.FromContext(ctx).WithValues("namespace", name)
	logger.Info("Attempting to create namespace")

	namespace := &corev1.Namespace{
		ObjectMeta: metav1.ObjectMeta{
			Name: name,
		},
	}

	m.ApplyMetadata(env, namespace)

	if err := m.Client.Create(ctx, namespace); err != nil {
		if errors.IsAlreadyExists(err) {
			logger.Info("Namespace already exists")

			existingNs := &corev1.Namespace{}
			if getErr := m.Client.Get(ctx, types.NamespacedName{Name: name}, existingNs); getErr != nil {
				logger.V(3).Error(getErr, "Failed to get existing namespace after AlreadyExists error")
				return getErr
			}

			if m.ApplyMetadata(env, existingNs) {
				logger.Info("Updating metadata for existing namespace")
				if updateErr := m.Client.Update(ctx, existingNs); updateErr != nil {
					logger.V(3).Error(updateErr, "Failed to update existing namespace metadata")
					return updateErr
				}
			}
			return nil
		}
		logger.V(3).Error(err, "Failed to create namespace")
		return err
	}

	logger.Info("Namespace created successfully")
	m.Recorder.Eventf(env, corev1.EventTypeNormal, "NamespaceCreated", "Created namespace %s", name)
	return nil
}

// GetNamespace retrieves a namespace by name
func (m *DefaultNamespaceManager) GetNamespace(ctx context.Context, name string) (*corev1.Namespace, error) {
	logger := log.FromContext(ctx)
	namespace := &corev1.Namespace{}
	err := m.Client.Get(ctx, types.NamespacedName{Name: name}, namespace)

	if err != nil {
		if errors.IsNotFound(err) {
			logger.V(1).Info("Namespace not found", "namespaceName", name)
			return nil, err
		}
		logger.V(3).Error(err, "Failed to get namespace", "namespaceName", name)
		return nil, err
	}

	logger.V(2).Info("Found namespace", "namespaceName", name, "uid", namespace.UID)
	return namespace, nil
}

// ApplyMetadata applies standard labels and annotations to a namespace
func (m *DefaultNamespaceManager) ApplyMetadata(env *quixiov1.Environment, namespace *corev1.Namespace) bool {
	needsUpdate := false

	// Ensure namespace has labels map initialized
	if namespace.Labels == nil {
		namespace.Labels = make(map[string]string)
		needsUpdate = true
	}

	// Ensure required labels are set
	requiredLabels := map[string]string{
		"quix.io/managed-by":     "quix-environment-operator",
		"quix.io/environment-id": env.Spec.Id,
	}

	for key, value := range requiredLabels {
		if namespace.Labels[key] != value {
			namespace.Labels[key] = value
			needsUpdate = true
		}
	}

	// Apply custom labels from Environment
	for key, value := range env.Spec.Labels {
		if namespace.Labels[key] != value {
			namespace.Labels[key] = value
			needsUpdate = true
		}
	}

	// Ensure namespace has annotations map initialized
	if namespace.Annotations == nil {
		namespace.Annotations = make(map[string]string)
		needsUpdate = true
	}

	// Ensure required annotations are set
	requiredAnnotations := map[string]string{
		"quix.io/created-by":                "quix-environment-operator",
		"quix.io/environment-id":            env.Spec.Id,
		"quix.io/environment-crd-namespace": env.Namespace, // Namespace where the Environment CR lives
		"quix.io/environment-resource-name": env.Name,      // Actual name of the Environment CR
	}

	for key, value := range requiredAnnotations {
		if namespace.Annotations[key] != value {
			namespace.Annotations[key] = value
			needsUpdate = true
		}
	}

	// Apply custom annotations from Environment
	for key, value := range env.Spec.Annotations {
		if namespace.Annotations[key] != value {
			namespace.Annotations[key] = value
			needsUpdate = true
		}
	}

	return needsUpdate
}

// UpdateMetadata updates a namespace's metadata based on the environment
func (m *DefaultNamespaceManager) UpdateMetadata(ctx context.Context, env *quixiov1.Environment, namespace *corev1.Namespace) error {
	logger := log.FromContext(ctx)

	// Set phase to Updating if we're about to make changes and not already in a special phase
	if env.Status.Phase == quixiov1.PhaseReady {
		if err := m.StatusUpdater.UpdateStatus(ctx, env, func(status *quixiov1.EnvironmentStatus) {
			status.Phase = quixiov1.PhaseUpdating
			status.ErrorMessage = "" // Clear any previous errors
		}); err != nil {
			logger.Error(err, "Failed to update Environment phase to Updating")
			// Continue with the update even if status update fails
		}
	}

	// Apply namespace metadata and check if updates were made
	needsUpdate := m.ApplyMetadata(env, namespace)

	// Update the namespace if changes were made
	if needsUpdate {
		logger.Info("Updating namespace metadata",
			"namespace", namespace.Name,
			"updatedLabels", namespace.Labels,
			"updatedAnnotations", namespace.Annotations)

		if err := m.Client.Update(ctx, namespace); err != nil {
			// Handle the error through the status updater
			return m.StatusUpdater.SetErrorStatus(ctx, env,
				quixiov1.PhaseUpdateFailed,
				err,
				"Failed to update namespace metadata")
		}

		logger.Info("Successfully updated namespace metadata", "namespace", namespace.Name)
		m.Recorder.Eventf(env, corev1.EventTypeNormal, "NamespaceUpdated", "Updated metadata for namespace %s", namespace.Name)

		// Update status back to Ready after successful update
		if err := m.StatusUpdater.UpdateStatus(ctx, env, func(status *quixiov1.EnvironmentStatus) {
			status.Phase = quixiov1.PhaseReady
			status.ErrorMessage = "" // Clear any error message
		}); err != nil {
			logger.Error(err, "Failed to update Environment phase to Ready after update")
			// Continue even if status update fails
		}

		m.StatusUpdater.SetSuccessStatus(ctx, env, "Environment updated successfully")
	}

	return nil
}

// IsNamespaceDeleted checks if a namespace should be considered deleted
func (m *DefaultNamespaceManager) IsNamespaceDeleted(namespace *corev1.Namespace, err error) bool {
	// If we got a NotFound error, the namespace is considered deleted
	if err != nil {
		return errors.IsNotFound(err)
	}

	// Otherwise, consider it deleted only if it's nil
	return namespace == nil
}

// IsNamespaceManaged checks if a namespace is managed by this operator
func (m *DefaultNamespaceManager) IsNamespaceManaged(namespace *corev1.Namespace) bool {
	if namespace == nil || namespace.Labels == nil {
		return false
	}
	managedLabel, exists := namespace.Labels["quix.io/managed-by"]
	return exists && managedLabel == "quix-environment-operator"
}
