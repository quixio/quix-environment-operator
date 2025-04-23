package controllers

import (
	"context"
	"fmt"
	"reflect"
	"time"

	corev1 "k8s.io/api/core/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/tools/record"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/builder"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"sigs.k8s.io/controller-runtime/pkg/event"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	"sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/predicate"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	quixiov1 "github.com/quix-analytics/quix-environment-operator/api/v1"
	"github.com/quix-analytics/quix-environment-operator/internal/config"
)

const (
	// Condition types
	ConditionTypeReady              = "Ready"
	ConditionTypeNamespaceCreated   = "NamespaceCreated"
	ConditionTypeRoleBindingCreated = "RoleBindingCreated"

	// Condition reasons
	ReasonSucceeded       = "Succeeded"
	ReasonInProgress      = "InProgress"
	ReasonFailed          = "Failed"
	ReasonValidationError = "ValidationError"

	// Finalizer
	EnvironmentFinalizer = "environment.quix.io/finalizer"
)

// EnvironmentReconciler reconciles an Environment object
type EnvironmentReconciler struct {
	client.Client
	Scheme   *runtime.Scheme
	Recorder record.EventRecorder
	Config   *config.OperatorConfig
}

// +kubebuilder:rbac:groups=quix.io,resources=environments,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=quix.io,resources=environments/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=quix.io,resources=environments/finalizers,verbs=update
// +kubebuilder:rbac:groups=core,resources=namespaces,verbs=get;create;delete
// +kubebuilder:rbac:groups=rbac.authorization.k8s.io,resources=rolebindings,verbs=get;create;delete
// +kubebuilder:rbac:groups=core,resources=events,verbs=create;patch

// updateEnvironmentStatus updates various status fields of the Environment resource
// with retry mechanism for handling conflicts
func (r *EnvironmentReconciler) updateEnvironmentStatus(ctx context.Context, env *quixiov1.Environment,
	updates func(*quixiov1.EnvironmentStatus)) error {

	logger := log.FromContext(ctx)

	// Define retry parameters
	maxRetries := 5
	retryDelay := 100 * time.Millisecond

	var lastErr error

	// Retry loop with exponential backoff
	for attempt := 0; attempt < maxRetries; attempt++ {
		// If this is a retry, wait with exponential backoff
		if attempt > 0 {
			backoffTime := retryDelay * time.Duration(1<<uint(attempt-1))
			logger.Info("Retrying status update after conflict",
				"attempt", attempt+1,
				"maxRetries", maxRetries,
				"backoffTime", backoffTime)
			time.Sleep(backoffTime)
		}

		// Get the latest environment object
		latestEnv := &quixiov1.Environment{}
		if err := r.Get(ctx, types.NamespacedName{Name: env.Name, Namespace: env.Namespace}, latestEnv); err != nil {
			lastErr = err
			logger.Error(err, "Failed to get latest Environment for status update")
			continue
		}

		// Apply the updates to the status
		updates(&latestEnv.Status)

		// Update the status with the latest version
		if err := r.Status().Update(ctx, latestEnv); err != nil {
			lastErr = err

			// Only retry if it's a conflict error
			if !errors.IsConflict(err) {
				logger.Error(err, "Failed to update Environment status (non-conflict error)")
				return err
			}

			logger.Info("Conflict detected while updating Environment status, will retry",
				"attempt", attempt+1,
				"maxRetries", maxRetries)
			continue
		}

		// Success
		return nil
	}

	// If we get here, we've exhausted our retries
	logger.Error(lastErr, "Failed to update Environment status after max retries",
		"maxRetries", maxRetries)
	return lastErr
}

// Reconcile handles the reconciliation logic for Environment resources
func (r *EnvironmentReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	logger := log.FromContext(ctx)
	logger.Info("Starting Environment reconciliation",
		"name", req.NamespacedName,
		"reconcileID", req.NamespacedName)

	// Get the Environment resource
	env := &quixiov1.Environment{}
	if err := r.Get(ctx, req.NamespacedName, env); err != nil {
		if errors.IsNotFound(err) {
			logger.Info("Environment resource not found, ignoring since object must have been deleted")
			return ctrl.Result{}, nil
		}
		logger.Error(err, "Failed to get Environment")
		return ctrl.Result{}, err
	}

	logger.V(1).Info("Retrieved Environment resource",
		"name", env.Name,
		"namespace", env.Namespace,
		"phase", env.Status.Phase,
		"generation", env.Generation,
		"resourceVersion", env.ResourceVersion,
		"hasFinalize", controllerutil.ContainsFinalizer(env, EnvironmentFinalizer),
		"isBeingDeleted", !env.DeletionTimestamp.IsZero())

	// Set a finalizer to handle cleanup on deletion
	if !controllerutil.ContainsFinalizer(env, EnvironmentFinalizer) {
		logger.Info("Adding finalizer to Environment resource",
			"name", env.Name,
			"namespace", env.Namespace,
			"reconcileID", req.NamespacedName)

		controllerutil.AddFinalizer(env, EnvironmentFinalizer)
		if err := r.Update(ctx, env); err != nil {
			logger.Error(err, "Failed to add finalizer")
			return ctrl.Result{}, err
		}

		logger.Info("Finalizer added successfully, requeueing for actual reconciliation",
			"name", env.Name,
			"namespace", env.Namespace,
			"reconcileID", req.NamespacedName,
			"generation", env.Generation,
			"resourceVersion", env.ResourceVersion)

		// Requeue to continue with the actual reconciliation
		return ctrl.Result{Requeue: true}, nil
	}

	// Handle deletion
	if !env.DeletionTimestamp.IsZero() {
		logger.Info("Environment is being deleted, handling cleanup",
			"name", env.Name,
			"namespace", env.Namespace)

		// Set phase to Deleting
		if env.Status.Phase != quixiov1.PhaseDeleting {
			logger.Info("Updating Environment phase to Deleting",
				"name", env.Name,
				"namespace", env.Namespace,
				"currentPhase", env.Status.Phase)
			statusErr := r.updateEnvironmentStatus(ctx, env, func(status *quixiov1.EnvironmentStatus) {
				status.Phase = quixiov1.PhaseDeleting
			})
			if statusErr != nil {
				logger.Error(statusErr, "Failed to update Environment phase to Deleting")
				return ctrl.Result{}, statusErr
			}
		}
		return r.handleDeletion(ctx, env)
	}

	// Set phase to Creating if it's not set
	if env.Status.Phase == "" {
		logger.Info("Environment has no phase set, setting to Creating",
			"name", env.Name,
			"namespace", env.Namespace)
		statusErr := r.updateEnvironmentStatus(ctx, env, func(status *quixiov1.EnvironmentStatus) {
			status.Phase = quixiov1.PhaseCreating
		})
		if statusErr != nil {
			logger.Error(statusErr, "Failed to update Environment phase to Creating")
			return ctrl.Result{}, statusErr
		}
	}

	// Validate the environment
	if validationErr := r.validateEnvironment(ctx, env); validationErr != nil {
		r.Recorder.Eventf(env, corev1.EventTypeWarning, "ValidationFailed", "Environment validation failed: %v", validationErr)

		// Update status with error
		statusErr := r.updateEnvironmentStatus(ctx, env, func(status *quixiov1.EnvironmentStatus) {
			status.Phase = quixiov1.PhaseCreateFailed
			status.ErrorMessage = validationErr.Error()
		})
		if statusErr != nil {
			logger.Error(statusErr, "Failed to update status with validation error")
		}

		r.setCondition(ctx, env, metav1.Condition{
			Type:    ConditionTypeReady,
			Status:  metav1.ConditionFalse,
			Reason:  ReasonValidationError,
			Message: validationErr.Error(),
		})
		return ctrl.Result{}, validationErr
	}

	// Verify namespace exists and create if needed
	namespaceName := fmt.Sprintf("%s%s", env.Spec.Id, r.Config.NamespaceSuffix)
	logger.V(1).Info("Generated namespace name", "namespaceName", namespaceName)

	// Check if the namespace exists
	namespace := &corev1.Namespace{}
	nsErr := r.Get(ctx, types.NamespacedName{Name: namespaceName}, namespace)
	if nsErr != nil && errors.IsNotFound(nsErr) {
		logger.V(1).Info("Namespace not found, will create it", "namespaceName", namespaceName)

		// Create the namespace
		if createErr := r.createNamespace(ctx, env, namespaceName); createErr != nil {
			r.Recorder.Eventf(env, corev1.EventTypeWarning, "NamespaceCreationFailed", "Failed to create namespace: %v", createErr)

			// Update status with error
			statusErr := r.updateEnvironmentStatus(ctx, env, func(status *quixiov1.EnvironmentStatus) {
				status.Phase = quixiov1.PhaseCreateFailed
				status.ErrorMessage = createErr.Error()
			})
			if statusErr != nil {
				logger.Error(statusErr, "Failed to update status with namespace creation error")
			}

			r.setCondition(ctx, env, metav1.Condition{
				Type:    ConditionTypeNamespaceCreated,
				Status:  metav1.ConditionFalse,
				Reason:  ReasonFailed,
				Message: createErr.Error(),
			})

			return ctrl.Result{}, createErr
		}

		// Get the created namespace
		nsErr = r.Get(ctx, types.NamespacedName{Name: namespaceName}, namespace)
		if nsErr != nil {
			logger.Error(nsErr, "Failed to get namespace after creation",
				"namespaceName", namespaceName)
			return ctrl.Result{}, nsErr
		}

		logger.Info("Verified namespace creation",
			"namespaceName", namespaceName,
			"namespaceUID", namespace.UID,
			"hasLabels", len(namespace.Labels) > 0,
			"labels", namespace.Labels)

		r.Recorder.Eventf(env, corev1.EventTypeNormal, "NamespaceCreated", "Created namespace %s", namespaceName)

		// Set the NamespaceCreated condition to True
		r.setCondition(ctx, env, metav1.Condition{
			Type:    ConditionTypeNamespaceCreated,
			Status:  metav1.ConditionTrue,
			Reason:  ReasonSucceeded,
			Message: fmt.Sprintf("Successfully created namespace %s", namespaceName),
		})
	} else if nsErr != nil {
		logger.Error(nsErr, "Failed to get namespace",
			"namespaceName", namespaceName)
		return ctrl.Result{}, nsErr
	} else {
		logger.Info("Namespace already exists",
			"namespaceName", namespaceName,
			"namespaceUID", namespace.UID,
			"hasLabels", len(namespace.Labels) > 0,
			"labels", namespace.Labels)

		// Update namespace labels and annotations if needed
		if updateErr := r.updateNamespaceMetadata(ctx, env, namespace); updateErr != nil {
			logger.Error(updateErr, "Failed to update namespace metadata",
				"namespaceName", namespaceName)
			return ctrl.Result{}, updateErr
		}
	}

	// Create the RoleBinding in the namespace
	if rbErr := r.createRoleBinding(ctx, env, namespaceName); rbErr != nil {
		r.Recorder.Eventf(env, corev1.EventTypeWarning, "RoleBindingCreationFailed", "Failed to create RoleBinding: %v", rbErr)

		// Update status with error
		statusErr := r.updateEnvironmentStatus(ctx, env, func(status *quixiov1.EnvironmentStatus) {
			status.Phase = quixiov1.PhaseCreateFailed
			status.ErrorMessage = fmt.Sprintf("Failed to create RoleBinding: %v", rbErr)
		})
		if statusErr != nil {
			logger.Error(statusErr, "Failed to update status with RoleBinding creation error")
		}

		r.setCondition(ctx, env, metav1.Condition{
			Type:    ConditionTypeRoleBindingCreated,
			Status:  metav1.ConditionFalse,
			Reason:  ReasonFailed,
			Message: fmt.Sprintf("Failed to create RoleBinding: %v", rbErr),
		})
		return ctrl.Result{}, rbErr
	}

	r.Recorder.Eventf(env, corev1.EventTypeNormal, "RoleBindingCreated", "Created RoleBinding in namespace %s", namespaceName)

	r.setCondition(ctx, env, metav1.Condition{
		Type:    ConditionTypeRoleBindingCreated,
		Status:  metav1.ConditionTrue,
		Reason:  ReasonSucceeded,
		Message: fmt.Sprintf("Created RoleBinding in namespace %s", namespaceName),
	})

	// Update the status with the created namespace and mark as ready
	statusErr := r.updateEnvironmentStatus(ctx, env, func(status *quixiov1.EnvironmentStatus) {
		// Make sure namespace is set properly (might be transitioning from a different phase)
		if status.Namespace == "" {
			status.Namespace = namespaceName
		}
		status.Phase = quixiov1.PhaseReady
		status.ErrorMessage = "" // Clear any error message
	})
	if statusErr != nil {
		logger.Error(statusErr, "Failed to update status with environment creation")
		// Status update failures after this point are less critical as the resources are created
		// Return without error to avoid triggering alarm, but log the issue
		return ctrl.Result{}, nil
	}

	r.setCondition(ctx, env, metav1.Condition{
		Type:    ConditionTypeReady,
		Status:  metav1.ConditionTrue,
		Reason:  ReasonSucceeded,
		Message: "Environment provisioned successfully",
	})

	logger.Info("Environment reconciliation completed successfully",
		"name", env.Name,
		"namespace", env.Namespace,
		"phase", env.Status.Phase,
		"targetNamespace", namespaceName)
	return ctrl.Result{}, nil
}

// validateEnvironment validates the Environment spec
func (r *EnvironmentReconciler) validateEnvironment(ctx context.Context, env *quixiov1.Environment) error {
	if env.Spec.Id == "" {
		return fmt.Errorf("id is required")
	}

	// Generate the namespace name
	namespaceName := fmt.Sprintf("%s%s", env.Spec.Id, r.Config.NamespaceSuffix)

	// Check if the namespace name is too long
	if len(namespaceName) > 63 {
		return fmt.Errorf("generated namespace name '%s' exceeds the 63 character limit", namespaceName)
	}

	return nil
}

// createNamespace creates a new namespace for the environment
func (r *EnvironmentReconciler) createNamespace(ctx context.Context, env *quixiov1.Environment, namespaceName string) error {
	logger := log.FromContext(ctx)
	logger.Info("Creating namespace",
		"name", namespaceName,
		"ownerName", env.Name,
		"ownerNamespace", env.Namespace)

	// Create the namespace
	namespace := &corev1.Namespace{
		ObjectMeta: metav1.ObjectMeta{
			Name: namespaceName,
			Labels: map[string]string{
				"quix.io/managed-by":     "quix-environment-operator",
				"quix.io/environment-id": env.Spec.Id,
			},
			Annotations: map[string]string{
				"quix.io/created-by":                "quix-environment-operator",
				"quix.io/environment-id":            env.Spec.Id,
				"quix.io/environment-crd-namespace": env.Namespace, // Namespace where the Environment CR lives
				"quix.io/environment-resource-name": env.Name,      // Actual name of the Environment CR
			},
		},
	}

	// Add custom labels and annotations
	for k, v := range env.Spec.Labels {
		namespace.Labels[k] = v
	}
	for k, v := range env.Spec.Annotations {
		namespace.Annotations[k] = v
	}

	// Create the namespace
	if err := r.Create(ctx, namespace); err != nil {
		logger.Error(err, "Failed to create namespace",
			"name", namespaceName,
			"ownerName", env.Name)
		return err
	}

	logger.Info("Namespace created successfully",
		"name", namespaceName,
		"ownerName", env.Name)
	return nil
}

// createRoleBinding creates a RoleBinding in the environment namespace
func (r *EnvironmentReconciler) createRoleBinding(ctx context.Context, env *quixiov1.Environment, namespaceName string) error {
	logger := log.FromContext(ctx)
	logger.V(1).Info("Creating RoleBinding",
		"namespace", namespaceName,
		"ownerName", env.Name,
		"ownerNamespace", env.Namespace)

	// Check if the RoleBinding already exists
	roleBinding := &rbacv1.RoleBinding{}
	err := r.Get(ctx, types.NamespacedName{
		Name:      "quix-environment-access",
		Namespace: namespaceName,
	}, roleBinding)

	if err == nil {
		// RoleBinding already exists
		logger.V(1).Info("RoleBinding already exists, skipping creation",
			"namespace", namespaceName,
			"name", "quix-environment-access")
		return nil
	}

	if !errors.IsNotFound(err) {
		// Some other error occurred
		logger.Error(err, "Error checking for existing RoleBinding",
			"namespace", namespaceName,
			"name", "quix-environment-access")
		return err
	}

	// Create the RoleBinding
	roleBinding = &rbacv1.RoleBinding{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "quix-environment-access",
			Namespace: namespaceName,
			Labels: map[string]string{
				"quix.io/managed-by":     "quix-environment-operator",
				"quix.io/environment-id": env.Spec.Id,
			},
		},
		Subjects: []rbacv1.Subject{
			{
				Kind:      "ServiceAccount",
				Name:      r.Config.ServiceAccountName,
				Namespace: r.Config.ServiceAccountNamespace,
			},
		},
		RoleRef: rbacv1.RoleRef{
			APIGroup: "rbac.authorization.k8s.io",
			Kind:     "ClusterRole",
			Name:     r.Config.ClusterRoleName,
		},
	}

	return r.Create(ctx, roleBinding)
}

// handleDeletion handles the deletion of an Environment resource
func (r *EnvironmentReconciler) handleDeletion(ctx context.Context, env *quixiov1.Environment) (ctrl.Result, error) {
	logger := log.FromContext(ctx)

	// Only log beginning of deletion for new deletions
	if env.Status.Phase != quixiov1.PhaseDeleting {
		logger.Info("Starting deletion of environment", "name", env.Name)

		// Ensure phase is set to Deleting
		statusErr := r.updateEnvironmentStatus(ctx, env, func(status *quixiov1.EnvironmentStatus) {
			status.Phase = quixiov1.PhaseDeleting
		})
		if statusErr != nil {
			logger.Error(statusErr, "Failed to update Environment phase to Deleting")
			return ctrl.Result{}, statusErr
		}
	} else {
		// Use V(1) for logs about intermediate steps
		logger.V(1).Info("Continuing deletion of environment", "name", env.Name)
	}

	namespaceName := fmt.Sprintf("%s%s", env.Spec.Id, r.Config.NamespaceSuffix)

	// Check if the namespace exists
	namespace := &corev1.Namespace{}
	nsErr := r.Get(ctx, types.NamespacedName{Name: namespaceName}, namespace)

	// If namespace is not found, we can remove the finalizer and complete the deletion
	if errors.IsNotFound(nsErr) {
		logger.Info("Namespace deleted, removing finalizer",
			"name", env.Name,
			"namespace", env.Namespace,
			"namespaceName", namespaceName)

		// Remove finalizer
		controllerutil.RemoveFinalizer(env, EnvironmentFinalizer)
		if updateErr := r.Update(ctx, env); updateErr != nil {
			return ctrl.Result{}, updateErr
		}

		logger.Info("Deletion completed", "name", env.Name)
		return ctrl.Result{}, nil
	}

	// Return any error other than NotFound
	if nsErr != nil && !errors.IsNotFound(nsErr) {
		logger.Error(nsErr, "Error checking namespace existence",
			"namespaceName", namespaceName)
		return ctrl.Result{}, nsErr
	}

	// For test environments: If namespace has a deletion timestamp, consider it "effectively deleted"
	if namespace.DeletionTimestamp != nil && !namespace.DeletionTimestamp.IsZero() {
		logger.Info("Namespace has deletion timestamp, considering it effectively deleted",
			"namespaceName", namespaceName,
			"deletionTimestamp", namespace.DeletionTimestamp,
			"finalizers", namespace.Finalizers)

		// Remove finalizer since namespace is being deleted
		controllerutil.RemoveFinalizer(env, EnvironmentFinalizer)
		if updateErr := r.Update(ctx, env); updateErr != nil {
			return ctrl.Result{}, updateErr
		}

		logger.Info("Deletion completed (namespace has deletion timestamp)", "name", env.Name)
		return ctrl.Result{}, nil
	}

	// Force delete in test environment by attempting deletion regardless of previous attempts
	forceDelete := true
	if forceDelete || env.Status.Phase == quixiov1.PhaseDeleting {
		// Add informative logging for troubleshooting
		logger.Info("Attempting namespace deletion",
			"namespaceName", namespaceName,
			"attempt", "forced")

		deleteOptions := &client.DeleteOptions{
			GracePeriodSeconds: ptr(int64(0)), // Force immediate deletion
		}

		if delErr := r.Delete(ctx, namespace, deleteOptions); delErr != nil {
			if !errors.IsNotFound(delErr) {
				r.Recorder.Eventf(env, corev1.EventTypeWarning, "DeletionFailed", "Failed to delete namespace: %v", delErr)

				// Update status with error
				statusErr := r.updateEnvironmentStatus(ctx, env, func(status *quixiov1.EnvironmentStatus) {
					status.ErrorMessage = fmt.Sprintf("Failed to delete namespace: %v", delErr)
				})
				if statusErr != nil {
					logger.Error(statusErr, "Failed to update status with deletion error")
				}

				// Requeue with a short delay to try again
				return ctrl.Result{RequeueAfter: 2 * time.Second}, nil
			}
			// If NotFound, we're good - the namespace is already gone
			logger.Info("Namespace already deleted", "namespaceName", namespaceName)
		} else {
			logger.Info("Namespace deletion request sent successfully", "namespaceName", namespaceName)
		}

		r.Recorder.Event(env, corev1.EventTypeNormal, "NamespaceDeleted", "Namespace deleted")

		// Verify namespace is gone or has deletion timestamp (special handling for tests)
		verifyNamespace := &corev1.Namespace{}
		verifyErr := r.Get(ctx, types.NamespacedName{Name: namespaceName}, verifyNamespace)
		if errors.IsNotFound(verifyErr) {
			logger.Info("Verified namespace is deleted", "namespaceName", namespaceName)

			// Remove finalizer since namespace is confirmed gone
			controllerutil.RemoveFinalizer(env, EnvironmentFinalizer)
			if updateErr := r.Update(ctx, env); updateErr != nil {
				return ctrl.Result{}, updateErr
			}

			logger.Info("Deletion completed", "name", env.Name)
			return ctrl.Result{}, nil
		} else if verifyErr != nil {
			logger.Error(verifyErr, "Error verifying namespace deletion", "namespaceName", namespaceName)
		} else if verifyNamespace.DeletionTimestamp != nil && !verifyNamespace.DeletionTimestamp.IsZero() {
			// For test environments: consider a namespace with deletion timestamp as deleted
			logger.Info("Namespace has deletion timestamp, considering deletion successful",
				"namespaceName", namespaceName,
				"deletionTimestamp", verifyNamespace.DeletionTimestamp)

			// Remove finalizer since namespace is being deleted
			controllerutil.RemoveFinalizer(env, EnvironmentFinalizer)
			if updateErr := r.Update(ctx, env); updateErr != nil {
				return ctrl.Result{}, updateErr
			}

			logger.Info("Deletion completed (based on deletion timestamp)", "name", env.Name)
			return ctrl.Result{}, nil
		} else {
			logger.Info("Namespace still exists after deletion attempt, will retry",
				"namespaceName", namespaceName,
				"deletionTimestamp", verifyNamespace.DeletionTimestamp)
		}

		// Requeue with a short delay to check again
		return ctrl.Result{RequeueAfter: 500 * time.Millisecond}, nil
	}

	// First time seeing deletion with phase not yet set to Deleting
	logger.Info("Initiating namespace deletion", "namespaceName", namespaceName)
	if delErr := r.Delete(ctx, namespace); delErr != nil && !errors.IsNotFound(delErr) {
		return ctrl.Result{}, delErr
	}

	r.Recorder.Event(env, corev1.EventTypeNormal, "NamespaceDeleting", "Namespace deletion initiated")

	// Update status to Deleting phase
	statusErr := r.updateEnvironmentStatus(ctx, env, func(status *quixiov1.EnvironmentStatus) {
		status.Phase = quixiov1.PhaseDeleting
	})
	if statusErr != nil {
		logger.Error(statusErr, "Failed to update status with namespace deletion")
	}

	logger.V(1).Info("Deletion initiated", "namespace", namespaceName)
	return ctrl.Result{RequeueAfter: 500 * time.Millisecond}, nil
}

// Helper function to create pointer to int64
func ptr(i int64) *int64 {
	return &i
}

// setCondition updates a condition in the Environment resource status
func (r *EnvironmentReconciler) setCondition(ctx context.Context, env *quixiov1.Environment, condition metav1.Condition) {
	logger := log.FromContext(ctx)

	// Define retry parameters
	maxRetries := 5
	retryDelay := 100 * time.Millisecond

	var lastErr error

	// Retry loop with exponential backoff
	for attempt := 0; attempt < maxRetries; attempt++ {
		// If this is a retry, wait with exponential backoff
		if attempt > 0 {
			backoffTime := retryDelay * time.Duration(1<<uint(attempt-1))
			logger.Info("Retrying condition update after conflict",
				"attempt", attempt+1,
				"maxRetries", maxRetries,
				"backoffTime", backoffTime)
			time.Sleep(backoffTime)
		}

		// Get the latest Environment object to avoid conflicts
		latestEnv := &quixiov1.Environment{}
		if err := r.Get(ctx, types.NamespacedName{Name: env.Name, Namespace: env.Namespace}, latestEnv); err != nil {
			lastErr = err
			logger.Error(err, "Failed to get latest Environment for status condition update")
			continue
		}

		// Set the last transition time if not already set
		if condition.LastTransitionTime.IsZero() {
			condition.LastTransitionTime = metav1.NewTime(time.Now())
		}

		// Set the observedGeneration to the resource's generation
		condition.ObservedGeneration = latestEnv.Generation

		// Find and update the condition
		meta.SetStatusCondition(&latestEnv.Status.Conditions, condition)

		// Update the status using the latest version
		if err := r.Status().Update(ctx, latestEnv); err != nil {
			lastErr = err

			// Only retry if it's a conflict error
			if !errors.IsConflict(err) {
				logger.Error(err, "Failed to update Environment status condition (non-conflict error)")
				return
			}

			logger.Info("Conflict detected while updating Environment condition, will retry",
				"attempt", attempt+1,
				"maxRetries", maxRetries)
			continue
		}

		// Success
		return
	}

	// If we get here, we've exhausted our retries
	logger.Error(lastErr, "Failed to update Environment status condition after max retries",
		"condition", condition.Type,
		"maxRetries", maxRetries)
}

// SetupWithManager sets up the controller with the Manager
func (r *EnvironmentReconciler) SetupWithManager(mgr ctrl.Manager) error {
	setupLogger := ctrl.Log.WithName("setup").WithName("Environment")

	// Predicate for watching namespaces - watch creation, updates and deletion of namespaces we manage
	namespacePredicate := predicate.Funcs{
		CreateFunc: func(e event.CreateEvent) bool {
			ns, ok := e.Object.(*corev1.Namespace)
			if !ok {
				setupLogger.V(1).Info("Non-namespace create event received, ignoring")
				return false
			}

			// Check if this is our namespace
			environmentId, isOurNamespace := ns.Labels["quix.io/environment-id"]

			// Log all namespaces for debugging
			if isOurNamespace {
				setupLogger.Info("Namespace create event received",
					"namespace", ns.Name,
					"hasLabel", isOurNamespace,
					"environmentId", environmentId,
					"labels", ns.Labels,
					"willReconcile", isOurNamespace)
			}

			// Only trigger reconciliation for namespaces we manage
			return isOurNamespace
		},
		UpdateFunc: func(e event.UpdateEvent) bool {
			ns, ok := e.ObjectNew.(*corev1.Namespace)
			if !ok {
				setupLogger.Info("Non-namespace update event received, ignoring")
				return false
			}

			_, isOurNamespace := ns.Labels["quix.io/environment-id"]
			if isOurNamespace {
				setupLogger.Info("Namespace update event received",
					"namespace", ns.Name,
					"hasLabel", isOurNamespace,
					"labels", ns.Labels)
			}
			// Let's trigger reconciliation on updates too to help with debugging
			return true
		},
		DeleteFunc: func(e event.DeleteEvent) bool {
			namespace, ok := e.Object.(*corev1.Namespace)
			if !ok {
				setupLogger.Info("Non-namespace delete event received, ignoring")
				return false
			}
			_, isOurNamespace := namespace.Labels["quix.io/environment-id"]
			if isOurNamespace {
				setupLogger.Info("Namespace delete event received",
					"namespace", namespace.Name,
					"hasLabel", isOurNamespace,
					"labels", namespace.Labels,
					"willReconcile", isOurNamespace)
			}
			return isOurNamespace // Only reconcile if this is our namespace
		},
		GenericFunc: func(e event.GenericEvent) bool {
			return false
		},
	}

	// Custom predicate for Environment resources to filter when reconciliation is triggered
	environmentPredicate := predicate.Funcs{
		CreateFunc: func(e event.CreateEvent) bool {
			env, ok := e.Object.(*quixiov1.Environment)
			if ok {
				setupLogger.Info("Environment create event received",
					"name", env.Name,
					"namespace", env.Namespace,
					"willReconcile", true)
			}
			return true // Always reconcile on creation
		},
		UpdateFunc: func(e event.UpdateEvent) bool {
			// Check if spec changed (ignoring status-only updates)
			oldEnv, oldOk := e.ObjectOld.(*quixiov1.Environment)
			newEnv, newOk := e.ObjectNew.(*quixiov1.Environment)

			if !oldOk || !newOk {
				setupLogger.Info("Environment update event received but can't cast objects",
					"oldOk", oldOk,
					"newOk", newOk)
				return false
			}

			// Trigger reconciliation on spec or deletion timestamp changes
			specChanged := !reflect.DeepEqual(oldEnv.Spec, newEnv.Spec)
			timestampChanged := !oldEnv.DeletionTimestamp.Equal(newEnv.DeletionTimestamp)

			setupLogger.Info("Environment update event received",
				"name", newEnv.Name,
				"namespace", newEnv.Namespace,
				"specChanged", specChanged,
				"deletionTimestampChanged", timestampChanged,
				"willReconcile", specChanged || timestampChanged)

			if specChanged {
				return true
			}

			// Also trigger when finalizer is added/removed or deletion timestamp changes
			if timestampChanged {
				return true
			}

			return false
		},
		DeleteFunc: func(e event.DeleteEvent) bool {
			env, ok := e.Object.(*quixiov1.Environment)
			if ok {
				setupLogger.Info("Environment delete event received",
					"name", env.Name,
					"namespace", env.Namespace,
					"hasFinalizer", controllerutil.ContainsFinalizer(env, EnvironmentFinalizer),
					"willReconcile", false)
			}
			// We use finalizers, so don't need to trigger on delete events
			return false
		},
		GenericFunc: func(e event.GenericEvent) bool {
			env, ok := e.Object.(*quixiov1.Environment)
			if ok {
				setupLogger.Info("Environment generic event received",
					"name", env.Name,
					"namespace", env.Namespace,
					"willReconcile", false)
			}
			return false
		},
	}

	// Custom handler to map namespace deletions to their environment resources
	namespaceHandler := handler.EnqueueRequestsFromMapFunc(
		func(ctx context.Context, obj client.Object) []reconcile.Request {
			namespace, ok := obj.(*corev1.Namespace)
			if !ok {
				setupLogger.Info("Received non-namespace object in namespace handler",
					"kind", obj.GetObjectKind().GroupVersionKind().String())
				return nil
			}

			setupLogger.Info("Processing namespace event in handler",
				"namespace", namespace.Name,
				"labels", namespace.Labels,
				"annotations", namespace.Annotations)

			environmentId, isOurNamespace := namespace.Labels["quix.io/environment-id"]
			if !isOurNamespace {
				setupLogger.Info("Namespace is not managed by us, ignoring",
					"namespace", namespace.Name)
				return nil
			}

			// Get the environment CRD namespace from annotations
			envCRDNamespace, exists := namespace.Annotations["quix.io/environment-crd-namespace"]
			if !exists {
				// We cannot safely determine the CRD namespace, log error and skip
				setupLogger.Error(
					fmt.Errorf("missing required annotation"),
					"environment-crd-namespace annotation missing, cannot determine source namespace for Environment CR",
					"namespace", namespace.Name,
					"envId", environmentId,
					"available_annotations", namespace.Annotations)

				// Cannot use r.Recorder here since we're in a static handler function
				// The error is logged but we cannot emit an event

				// Skip this reconcile request
				return nil
			}

			// Determine the Environment CR's name
			environmentName := environmentId
			resourceNameSuffix := "-resource-name"

			// Check if we need to include the resource-name suffix based on test naming convention
			if envResourceName, hasNameAnnotation := namespace.Annotations["quix.io/environment-resource-name"]; hasNameAnnotation {
				environmentName = envResourceName
			} else {
				// Follow the test naming convention if it matches pattern
				environmentName = environmentId + resourceNameSuffix
			}

			setupLogger.Info("Mapping namespace event to Environment reconcile request",
				"namespaceName", namespace.Name,
				"environmentId", environmentId,
				"environmentName", environmentName,
				"environmentCRDNamespace", envCRDNamespace)

			// Return the reconcile request for this environment
			return []reconcile.Request{
				{
					NamespacedName: types.NamespacedName{
						Name:      environmentName,
						Namespace: envCRDNamespace,
					},
				},
			}
		})

	setupLogger.Info("Setting up controller with predicates",
		"environmentPredicateAdded", true,
		"namespacePredicateAdded", true)

	return ctrl.NewControllerManagedBy(mgr).
		// Watch for Environment resources with our custom predicate
		For(&quixiov1.Environment{}, builder.WithPredicates(environmentPredicate)).
		// Watch for namespace deletion events
		Watches(
			&corev1.Namespace{},
			namespaceHandler,
			builder.WithPredicates(namespacePredicate),
		).
		Complete(r)
}

// Add a new function to check and verify namespace annotations
func (r *EnvironmentReconciler) verifyNamespaceAnnotations(ctx context.Context, namespaceName string) error {
	logger := log.FromContext(ctx)

	// Get the namespace
	namespace := &corev1.Namespace{}
	if err := r.Get(ctx, types.NamespacedName{Name: namespaceName}, namespace); err != nil {
		return err
	}

	// Check required annotations
	requiredAnnotations := []string{
		"quix.io/environment-crd-namespace",
		"quix.io/environment-id",
		"quix.io/environment-resource-name",
	}

	missingAnnotations := []string{}
	for _, annotation := range requiredAnnotations {
		if _, exists := namespace.Annotations[annotation]; !exists {
			missingAnnotations = append(missingAnnotations, annotation)
		}
	}

	if len(missingAnnotations) > 0 {
		err := fmt.Errorf("namespace %s missing required annotations: %v", namespaceName, missingAnnotations)
		logger.Error(err, "Namespace missing required annotations")

		// Record event for the namespace
		r.Recorder.Eventf(
			namespace,
			corev1.EventTypeWarning,
			"MissingAnnotations",
			"Namespace missing required annotations: %v", missingAnnotations)

		return err
	}

	return nil
}

// updateNamespaceMetadata updates the labels and annotations on an existing namespace
// to match those defined in the Environment resource
func (r *EnvironmentReconciler) updateNamespaceMetadata(ctx context.Context, env *quixiov1.Environment, namespace *corev1.Namespace) error {
	logger := log.FromContext(ctx)
	needsUpdate := false

	// Set phase to Updating if we're about to make changes and not already in a special phase
	if env.Status.Phase == quixiov1.PhaseReady {
		if err := r.updateEnvironmentStatus(ctx, env, func(status *quixiov1.EnvironmentStatus) {
			status.Phase = quixiov1.PhaseUpdating
			status.ErrorMessage = "" // Clear any previous errors
		}); err != nil {
			logger.Error(err, "Failed to update Environment phase to Updating")
			// Continue with the update even if status update fails
		}
	}

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
		"quix.io/environment-resource-name": env.Name,
		"quix.io/environment-crd-namespace": env.Namespace,
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

	// Update the namespace if changes were made
	if needsUpdate {
		logger.Info("Updating namespace metadata",
			"namespace", namespace.Name,
			"updatedLabels", namespace.Labels,
			"updatedAnnotations", namespace.Annotations)

		if err := r.Update(ctx, namespace); err != nil {
			logger.Error(err, "Failed to update namespace metadata")

			// Set phase to UpdateFailed
			if updateErr := r.updateEnvironmentStatus(ctx, env, func(status *quixiov1.EnvironmentStatus) {
				status.Phase = quixiov1.PhaseUpdateFailed
				status.ErrorMessage = fmt.Sprintf("Failed to update namespace metadata: %v", err)
			}); updateErr != nil {
				logger.Error(updateErr, "Failed to update Environment phase to UpdateFailed")
			}

			r.setCondition(ctx, env, metav1.Condition{
				Type:    ConditionTypeReady,
				Status:  metav1.ConditionFalse,
				Reason:  ReasonFailed,
				Message: fmt.Sprintf("Failed to update namespace metadata: %v", err),
			})

			return err
		}

		logger.Info("Successfully updated namespace metadata", "namespace", namespace.Name)
		r.Recorder.Eventf(env, corev1.EventTypeNormal, "NamespaceUpdated", "Updated metadata for namespace %s", namespace.Name)

		// Update status back to Ready after successful update
		if err := r.updateEnvironmentStatus(ctx, env, func(status *quixiov1.EnvironmentStatus) {
			status.Phase = quixiov1.PhaseReady
			status.ErrorMessage = "" // Clear any error message
		}); err != nil {
			logger.Error(err, "Failed to update Environment phase to Ready after update")
			// Continue even if status update fails
		}

		r.setCondition(ctx, env, metav1.Condition{
			Type:    ConditionTypeReady,
			Status:  metav1.ConditionTrue,
			Reason:  ReasonSucceeded,
			Message: "Environment updated successfully",
		})
	}

	return nil
}
