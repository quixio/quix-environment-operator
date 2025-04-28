package namespace

import (
	"context"
	"fmt"
	"regexp"

	v1 "github.com/quix-analytics/quix-environment-operator/api/v1"
	"github.com/quix-analytics/quix-environment-operator/internal/config"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/tools/record"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/log"
)

const (
	ManagedByLabel         = "quix.io/managed-by"
	OperatorName           = "quix-environment-operator"
	LabelEnvironmentID     = "quix.io/environment-id"
	LabelEnvironmentName   = "quix.io/environment-name"
	AnnotationCreatedBy    = "quix.io/created-by"
	AnnotationCRDNamespace = "quix.io/environment-crd-namespace"
	AnnotationResourceName = "quix.io/environment-resource-name"
)

// DefaultManager implements the NamespaceManager interface
type DefaultManager struct {
	client   client.Client
	recorder record.EventRecorder
	config   config.ConfigProvider
}

// NewManager creates a new default namespace manager
func NewManager(
	client client.Client,
	recorder record.EventRecorder,
	config config.ConfigProvider,
) *DefaultManager {
	return &DefaultManager{
		client:   client,
		recorder: recorder,
		config:   config,
	}
}

// Exists checks if a namespace exists for an environment
func (m *DefaultManager) Exists(ctx context.Context, env *v1.Environment) (bool, error) {
	namespaceName := m.GetNamespaceName(env)
	namespace := &corev1.Namespace{}
	err := m.client.Get(ctx, types.NamespacedName{Name: namespaceName}, namespace)
	if err != nil {
		if errors.IsNotFound(err) {
			return false, nil
		}
		return false, err
	}
	return true, nil
}

// Create creates a new namespace for the environment
func (m *DefaultManager) create(ctx context.Context, env *v1.Environment) (*corev1.Namespace, error) {
	namespaceName := m.GetNamespaceName(env)
	isValid, err := m.isValidEnvironmentId(env.Spec.Id)
	if err != nil {
		return nil, fmt.Errorf("failed to validate environment ID: %w", err)
	}

	if !isValid {
		return nil, fmt.Errorf("invalid environment ID: %s", env.Spec.Id)
	}

	logger := log.FromContext(ctx).WithValues("namespace", namespaceName)
	logger.V(1).Info("Attempting to create namespace")

	namespace := &corev1.Namespace{
		ObjectMeta: metav1.ObjectMeta{
			Name: namespaceName,
		},
	}

	m.ApplyMetadata(env, namespace)

	if err := m.client.Create(ctx, namespace); err != nil {
		if errors.IsAlreadyExists(err) {
			logger.V(1).Info("Namespace already exists")

			existingNs := &corev1.Namespace{}
			if getErr := m.client.Get(ctx, types.NamespacedName{Name: namespaceName}, existingNs); getErr != nil {
				return nil, fmt.Errorf("failed to get existing namespace after AlreadyExists error: %w", getErr)
			}

			// Check if the namespace is managed by us before making changes
			if !m.IsManaged(env) {
				return nil, fmt.Errorf("cannot use existing namespace %s: not managed by this operator", namespaceName)
			}

			if m.ApplyMetadata(env, existingNs) {
				logger.V(0).Info("Updating metadata for existing namespace")
				if updateErr := m.client.Update(ctx, existingNs); updateErr != nil {
					return nil, fmt.Errorf("failed to update existing namespace metadata: %w", updateErr)
				}
			}
			return existingNs, nil
		}
		return nil, fmt.Errorf("failed to create namespace: %w", err)
	}

	logger.V(0).Info("Namespace created successfully")
	m.recorder.Eventf(env, corev1.EventTypeNormal, "NamespaceCreated", "Created namespace %s", namespaceName)
	return namespace, nil
}

// Update updates an existing namespace
func (m *DefaultManager) update(ctx context.Context, env *v1.Environment) error {
	namespaceName := m.GetNamespaceName(env)
	logger := log.FromContext(ctx).WithValues("namespace", namespaceName)
	logger.V(1).Info("Updating namespace")

	namespace, err := m.Get(ctx, env)
	if err != nil {
		return err
	}

	// Check if the namespace is managed by us before making changes
	if !m.IsManaged(env) {
		return fmt.Errorf("cannot update namespace %s: not managed by this operator", namespaceName)
	}

	if m.ApplyMetadata(env, namespace) {
		if err := m.client.Update(ctx, namespace); err != nil {
			return fmt.Errorf("failed to update namespace: %w", err)
		}
		logger.V(0).Info("Namespace updated successfully")
	} else {
		logger.V(1).Info("No updates needed for namespace")
	}

	return nil
}

// Delete deletes a namespace for an environment
func (m *DefaultManager) Delete(ctx context.Context, env *v1.Environment) error {
	namespaceName := m.GetNamespaceName(env)
	logger := log.FromContext(ctx).WithValues("namespace", namespaceName)
	logger.V(1).Info("Attempting to delete namespace")

	namespace := &corev1.Namespace{
		ObjectMeta: metav1.ObjectMeta{
			Name: namespaceName,
		},
	}

	if err := m.client.Delete(ctx, namespace); err != nil {
		if errors.IsNotFound(err) {
			logger.V(1).Info("Namespace already deleted")
			return nil
		}
		return fmt.Errorf("failed to delete namespace: %w", err)
	}

	logger.V(0).Info("Namespace deletion initiated")
	return nil
}

// Get retrieves a namespace for an environment
func (m *DefaultManager) Get(ctx context.Context, env *v1.Environment) (*corev1.Namespace, error) {
	namespaceName := m.GetNamespaceName(env)
	logger := log.FromContext(ctx)
	namespace := &corev1.Namespace{}
	err := m.client.Get(ctx, types.NamespacedName{Name: namespaceName}, namespace)

	if err != nil {
		if errors.IsNotFound(err) {
			logger.V(1).Info("Namespace not found", "namespaceName", namespaceName)
			return nil, err
		}
		return nil, fmt.Errorf("failed to get namespace %s: %w", namespaceName, err)
	}

	logger.V(1).Info("Found namespace", "namespaceName", namespaceName, "uid", namespace.UID)
	return namespace, nil
}

// IsManaged checks if a namespace is managed by this operator
func (m *DefaultManager) IsManaged(env *v1.Environment) bool {
	if env == nil {
		return false
	}

	ctx := context.Background()
	namespace, err := m.Get(ctx, env)
	if err != nil || namespace == nil {
		return false
	}

	// A namespace is considered managed if it has our management label
	return namespace.Labels != nil && namespace.Labels[ManagedByLabel] == OperatorName
}

// IsDeleting checks if a namespace is deleting
func (m *DefaultManager) IsDeleting(ctx context.Context, env *v1.Environment) (bool, error) {
	namespaceName := m.GetNamespaceName(env)
	namespace := &corev1.Namespace{}
	err := m.client.Get(ctx, types.NamespacedName{Name: namespaceName}, namespace)
	if err != nil {
		if errors.IsNotFound(err) {
			return true, nil
		}
		return false, err
	}

	return namespace.DeletionTimestamp != nil, nil
}

// ApplyMetadata applies standard labels and annotations to a namespace
func (m *DefaultManager) ApplyMetadata(env *v1.Environment, namespace *corev1.Namespace) bool {
	needsUpdate := false

	// Ensure namespace has labels map initialized
	if namespace.Labels == nil {
		namespace.Labels = make(map[string]string)
		needsUpdate = true
	}

	// Ensure required labels are set
	requiredLabels := map[string]string{
		ManagedByLabel:       OperatorName,
		LabelEnvironmentID:   env.Spec.Id,
		LabelEnvironmentName: env.Name,
	}

	for key, value := range requiredLabels {
		if namespace.Labels[key] != value {
			namespace.Labels[key] = value
			needsUpdate = true
		}
	}

	// Define a list of protected labels that cannot be overridden
	protectedLabels := []string{
		ManagedByLabel,
		LabelEnvironmentID,
		LabelEnvironmentName,
		"kubernetes.io/metadata.name",
		"control-plane",
	}

	// Apply custom labels from Environment
	for key, value := range env.Spec.Labels {
		// Skip protected labels
		if isProtectedLabel(key, protectedLabels) {
			continue
		}

		// Enforce quix.io/ prefix for custom labels
		if !isValidLabelPrefix(key) {
			continue
		}

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
		AnnotationCreatedBy:    OperatorName,
		AnnotationCRDNamespace: env.Namespace,
		AnnotationResourceName: env.Name,
	}

	for key, value := range requiredAnnotations {
		if namespace.Annotations[key] != value {
			namespace.Annotations[key] = value
			needsUpdate = true
		}
	}

	// Define a list of protected annotations that cannot be overridden
	protectedAnnotations := []string{
		AnnotationCreatedBy,
		AnnotationCRDNamespace,
		AnnotationResourceName,
	}

	// Apply custom annotations from Environment
	for key, value := range env.Spec.Annotations {
		// Skip protected annotations
		if isProtectedAnnotation(key, protectedAnnotations) {
			continue
		}

		// Enforce quix.io/ prefix for custom annotations
		if !isValidLabelPrefix(key) {
			continue
		}

		if namespace.Annotations[key] != value {
			namespace.Annotations[key] = value
			needsUpdate = true
		}
	}

	return needsUpdate
}

// isProtectedLabel checks if the given key is in the list of protected labels
func isProtectedLabel(key string, protectedLabels []string) bool {
	for _, protectedKey := range protectedLabels {
		if key == protectedKey {
			return true
		}
	}
	return false
}

// isProtectedAnnotation checks if the given key is in the list of protected annotations
func isProtectedAnnotation(key string, protectedAnnotations []string) bool {
	for _, protectedKey := range protectedAnnotations {
		if key == protectedKey {
			return true
		}
	}
	return false
}

// isValidLabelPrefix checks if a label or annotation has a valid prefix (must be quix.io/)
func isValidLabelPrefix(key string) bool {
	const validPrefix = "quix.io/"
	return len(key) > len(validPrefix) && key[:len(validPrefix)] == validPrefix
}

// GetNamespaceName returns the standardized name for the environment namespace
func (m *DefaultManager) GetNamespaceName(env *v1.Environment) string {
	return fmt.Sprintf("%s%s", env.Spec.Id, m.config.GetNamespaceSuffix())
}

// isValidEnvironmentId checks if the environment ID matches the configured regex pattern
func (m *DefaultManager) isValidEnvironmentId(envId string) (bool, error) {
	// If no regex is configured, all IDs are valid
	if m.config.GetEnvironmentRegex() == "" {
		return true, nil
	}

	// Compile the regex pattern
	pattern, err := regexp.Compile(m.config.GetEnvironmentRegex())
	if err != nil {
		return false, fmt.Errorf("invalid environment regex pattern: %w", err)
	}

	// Check if the environment ID matches the pattern
	return pattern.MatchString(envId), nil
}

// Reconcile creates or updates a namespace for an environment
func (m *DefaultManager) Reconcile(ctx context.Context, env *v1.Environment) (*corev1.Namespace, error) {
	namespaceName := m.GetNamespaceName(env)
	logger := log.FromContext(ctx).WithValues("namespace", namespaceName)
	logger.Info("Reconciling namespace")

	// Check if namespace exists
	exists, err := m.Exists(ctx, env)
	if !exists {
		if err != nil {
			return nil, fmt.Errorf("failed to get namespace: %w", err)
		}
		// Create namespace if it doesn't exist
		newNamespace, err := m.create(ctx, env)
		if err != nil {
			return nil, fmt.Errorf("failed to create namespace: %w", err)
		}
		return newNamespace, nil
	}

	// Update existing namespace
	err = m.update(ctx, env)
	if err != nil {
		return nil, fmt.Errorf("failed to update namespace: %w", err)
	}

	// Refresh namespace status
	updatedNs := &corev1.Namespace{}
	err = m.client.Get(ctx, types.NamespacedName{Name: namespaceName}, updatedNs)
	if err != nil {
		return nil, fmt.Errorf("failed to get updated namespace: %w", err)
	}

	return updatedNs, nil
}
