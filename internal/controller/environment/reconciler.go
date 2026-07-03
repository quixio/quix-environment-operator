package environment

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/go-logr/logr"
	v1 "github.com/quix-analytics/quix-environment-operator/api/v1"
	"github.com/quix-analytics/quix-environment-operator/internal/config"
	"github.com/quix-analytics/quix-environment-operator/internal/resources/environment"
	"github.com/quix-analytics/quix-environment-operator/internal/resources/namespace"
	"github.com/quix-analytics/quix-environment-operator/internal/resources/rolebinding"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/tools/record"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/builder"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/manager"
	"sigs.k8s.io/controller-runtime/pkg/predicate"
)

const (
	// Finalizer for environment resources
	EnvironmentFinalizer = "quix.io/environment-finalizer"

	// Labels and annotations for resources
	ManagedByLabel       = "quix.io/managed-by"
	EnvironmentIdLabel   = "quix.io/environment-id"
	EnvironmentNameLabel = "quix.io/environment-name"
	CreatedByAnnotation  = "quix.io/created-by"

	RequeueDelay = time.Second * 1
)

// EnvironmentReconciler reconciles Environment resources
type EnvironmentReconciler struct {
	client             client.Client
	scheme             *runtime.Scheme
	environmentManager environment.Manager
	operatorConfig     *config.OperatorConfig
	logger             logr.Logger
	namespaceManager   namespace.Manager
	recorder           record.EventRecorder
	roleBindingManager rolebinding.Manager
}

// NewEnvironmentReconciler creates a new EnvironmentReconciler instance
func NewEnvironmentReconciler(
	client client.Client,
	scheme *runtime.Scheme,
	environmentManager environment.Manager,
	operatorConfig *config.OperatorConfig,
	namespaceManager namespace.Manager,
	recorder record.EventRecorder,
	roleBindingManager rolebinding.Manager,
) *EnvironmentReconciler {
	return &EnvironmentReconciler{
		client:             client,
		scheme:             scheme,
		environmentManager: environmentManager,
		operatorConfig:     operatorConfig,
		logger:             log.Log.WithName("environment-controller"),
		namespaceManager:   namespaceManager,
		recorder:           recorder,
		roleBindingManager: roleBindingManager,
	}
}

// Reconcile processes Environment resources
func (r *EnvironmentReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	logger := log.FromContext(ctx)
	logger.V(1).Info("starting reconcile for cluster-scoped environment", "name", req.Name)

	environment, err := r.environmentManager.Get(ctx, req.Name)
	if err != nil {
		if errors.IsNotFound(err) {
			logger.V(1).Info("environment not found, ignoring", "name", req.Name)
			return ctrl.Result{}, nil
		}
		return ctrl.Result{}, fmt.Errorf("error retrieving environment: %w", err)
	}

	// Add the finalizer before any status mutation or deletion handling, so the
	// deletion handler is guaranteed to run and managed resources are never orphaned.
	// Guard with the deletion timestamp: a deleting object skips finalizer addition (the
	// API server rejects adding a finalizer to an object marked for deletion) and falls
	// through to handleDeletion below. On success we requeue so the next pass re-fetches
	// the persisted object (carrying the finalizer) before creating any namespace or
	// RoleBinding.
	//
	// Invariant: the finalizer is persisted before any managed resource (namespace/
	// RoleBinding) is created. This is what lets RemoveFinalizer no-op safely when the
	// finalizer is absent (see resources/environment/manager.go RemoveFinalizer).
	if environment.GetDeletionTimestamp() == nil && !r.environmentManager.HasFinalizer(environment, EnvironmentFinalizer) {
		if err := r.environmentManager.AddFinalizer(ctx, environment, EnvironmentFinalizer); err != nil {
			logger.Error(err, "Failed to add finalizer")
			return ctrl.Result{Requeue: true}, err
		}
		return ctrl.Result{Requeue: true}, nil
	}

	if environment.Status.Phase == "" {
		if err := r.updateStatus(ctx, environment, v1.EnvironmentPhaseInProgress, "Processing environment"); err != nil {
			return ctrl.Result{}, err
		}
	}

	// Check for deletion and handle finalizer
	if environment.GetDeletionTimestamp() != nil {
		return r.handleDeletion(ctx, environment)
	}

	// Check if namespace exists
	_, err = r.namespaceManager.Get(ctx, environment)
	if err != nil {
		if errors.IsNotFound(err) {
			// Create namespace if it doesn't exist
			logger.V(0).Info("creating namespace for environment", "environment", environment.Name)

			// Update status to show namespace creation is in progress, only if not already set
			if environment.Status.NamespaceStatus == nil || environment.Status.NamespaceStatus.Phase == "" {
				initResourceStatus(&environment.Status.NamespaceStatus, v1.ResourceStatusPhaseCreating, "Creating namespace for environment")
				if err := r.environmentManager.UpdateStatus(ctx, environment); err != nil {
					logger.Error(err, "Failed to update namespace status")
				}
			}

			_, err := r.namespaceManager.Reconcile(ctx, environment)
			if err != nil {
				r.recorder.Event(environment, corev1.EventTypeWarning, "NamespaceCreationFailed", "Failed to create namespace")
				logger.Error(err, "error creating namespace", "environment", environment.Name)

				// Persist the namespace name so deletion can rely on the stored value
				setResourceNameIfEmpty(&environment.Status.NamespaceStatus, r.namespaceManager.GetNamespaceName(environment))

				// Update namespace status to Failed, but only if not already set to Failed
				if environment.Status.NamespaceStatus != nil {
					updateResourceStatusIfNotFailed(environment.Status.NamespaceStatus, v1.ResourceStatusPhaseFailed,
						fmt.Sprintf("Failed to create namespace: %v", err))
					_ = r.environmentManager.UpdateStatus(ctx, environment)
				}

				// Update environment status to Failed
				_ = r.updateStatus(ctx, environment, v1.EnvironmentPhaseFailed, fmt.Sprintf("Failed to create namespace: %v", err))
				return ctrl.Result{}, err
			}

			// Update namespace status to Active if current status is Creating
			if environment.Status.NamespaceStatus != nil {
				updateResourceStatusIfCreating(environment.Status.NamespaceStatus, v1.ResourceStatusPhaseActive, "Namespace created successfully")

				// Set the namespace name on the NamespaceStatus resource if not already set.
				// The name was just newly computed for a created namespace, so persisting it
				// is required to avoid orphaning the namespace; fail closed if persistence fails.
				newName := setResourceNameIfEmpty(&environment.Status.NamespaceStatus, r.namespaceManager.GetNamespaceName(environment))

				if err := r.environmentManager.UpdateStatus(ctx, environment); err != nil {
					if newName {
						return ctrl.Result{}, err
					}
					logger.Error(err, "Failed to update namespace status")
				}
			}

			r.recorder.Event(environment, corev1.EventTypeNormal, "NamespaceCreated", "Created namespace for environment")

			// Update RoleBinding status to Creating if not already set
			if environment.Status.RoleBindingStatus == nil || environment.Status.RoleBindingStatus.Phase == "" {
				initResourceStatus(&environment.Status.RoleBindingStatus, v1.ResourceStatusPhaseCreating, "Creating role binding")
				_ = r.environmentManager.UpdateStatus(ctx, environment)
			}

			// Create RoleBinding for the environment
			if _, err := r.roleBindingManager.Reconcile(ctx, environment); err != nil {
				r.recorder.Event(environment, corev1.EventTypeWarning, "RoleBindingCreationFailed", "Failed to create role binding")

				// Update RoleBinding status to Failed if not already set
				if environment.Status.RoleBindingStatus != nil {
					updateResourceStatusIfNotFailed(environment.Status.RoleBindingStatus, v1.ResourceStatusPhaseFailed,
						fmt.Sprintf("Failed to create role binding: %v", err))
					_ = r.environmentManager.UpdateStatus(ctx, environment)
				}

				_ = r.updateStatus(ctx, environment, v1.EnvironmentPhaseFailed, fmt.Sprintf("Failed to create role binding: %v", err))
				return ctrl.Result{}, fmt.Errorf("error creating role binding: %w", err)
			}

			// Update RoleBinding status to Active if current status is Creating
			if environment.Status.RoleBindingStatus != nil {
				updateResourceStatusIfCreating(environment.Status.RoleBindingStatus, v1.ResourceStatusPhaseActive, "Role binding created successfully")

				// Set the role binding name on the RoleBindingStatus resource if not already set.
				// The name was just newly computed for a created role binding, so persisting it
				// is required to avoid orphaning it; fail closed if persistence fails.
				newName := setResourceNameIfEmpty(&environment.Status.RoleBindingStatus, r.roleBindingManager.GetName(environment))

				if err := r.environmentManager.UpdateStatus(ctx, environment); err != nil {
					if newName {
						return ctrl.Result{}, err
					}
					logger.Error(err, "Failed to update role binding status")
				}
			}

			r.recorder.Event(environment, corev1.EventTypeNormal, "RoleBindingCreated", "Created role binding for environment")

			return ctrl.Result{Requeue: true}, nil
		}
		return ctrl.Result{}, fmt.Errorf("error checking namespace existence: %w", err)
	}

	// Reconcile the namespace - this handles updates as needed
	_, err = r.namespaceManager.Reconcile(ctx, environment)
	if err != nil {
		r.recorder.Event(environment, corev1.EventTypeWarning, "NamespaceReconciliationFailed", "Failed to reconcile namespace")

		// Persist the namespace name so deletion can rely on the stored value
		setResourceNameIfEmpty(&environment.Status.NamespaceStatus, r.namespaceManager.GetNamespaceName(environment))

		// Update namespace status to Failed, but only if not already Failed
		if environment.Status.NamespaceStatus != nil {
			updateResourceStatusIfNotFailed(environment.Status.NamespaceStatus, v1.ResourceStatusPhaseFailed,
				fmt.Sprintf("Failed to reconcile namespace: %v", err))
			_ = r.environmentManager.UpdateStatus(ctx, environment)
		}

		// Update environment status to Failed if namespace cannot be reconciled
		if strings.Contains(err.Error(), "not managed by this operator") ||
			strings.Contains(err.Error(), "Cannot use existing namespace") ||
			strings.Contains(err.Error(), "owned by a different environment") {
			_ = r.updateStatus(ctx, environment, v1.EnvironmentPhaseFailed,
				fmt.Sprintf("Namespace conflict: %v", err))
		} else {
			_ = r.updateStatus(ctx, environment, v1.EnvironmentPhaseFailed,
				fmt.Sprintf("Failed to reconcile namespace: %v", err))
		}

		return ctrl.Result{}, fmt.Errorf("error reconciling namespace: %w", err)
	}

	// Set or update Namespace resource name if needed
	if environment.Status.NamespaceStatus != nil && environment.Status.NamespaceStatus.ResourceName == "" {
		// Persist the resolved namespace name; fail closed if persistence fails
		setResourceNameIfEmpty(&environment.Status.NamespaceStatus, r.namespaceManager.GetNamespaceName(environment))
		if err := r.environmentManager.UpdateStatus(ctx, environment); err != nil {
			return ctrl.Result{}, err
		}
	}

	// Reconcile the role binding - this handles creation or updates as needed
	_, err = r.roleBindingManager.Reconcile(ctx, environment)
	if err != nil {
		r.recorder.Event(environment, corev1.EventTypeWarning, "RoleBindingReconciliationFailed", "Failed to reconcile role binding")

		// Update RoleBinding status to Failed, but only if not already Failed
		if environment.Status.RoleBindingStatus != nil {
			updateResourceStatusIfNotFailed(environment.Status.RoleBindingStatus, v1.ResourceStatusPhaseFailed,
				fmt.Sprintf("Failed to reconcile role binding: %v", err))
			_ = r.environmentManager.UpdateStatus(ctx, environment)
		}

		// If this is a security validation error, mark the environment as Failed
		if strings.Contains(err.Error(), "security violation") {
			_ = r.updateStatus(ctx, environment, v1.EnvironmentPhaseFailed,
				fmt.Sprintf("Security validation failed: %v", err))
			return ctrl.Result{}, err
		} else {
			_ = r.updateStatus(ctx, environment, v1.EnvironmentPhaseFailed,
				fmt.Sprintf("Failed to reconcile role binding: %v", err))
		}

		return ctrl.Result{}, fmt.Errorf("error reconciling role binding: %w", err)
	}

	// Set or update RoleBinding resource name if needed
	if environment.Status.RoleBindingStatus != nil && environment.Status.RoleBindingStatus.ResourceName == "" {
		// Persist the resolved role binding name; fail closed if persistence fails
		setResourceNameIfEmpty(&environment.Status.RoleBindingStatus, r.roleBindingManager.GetName(environment))
		if err := r.environmentManager.UpdateStatus(ctx, environment); err != nil {
			return ctrl.Result{}, err
		}
	}

	// Update environment status if needed
	if environment.Status.Phase != v1.EnvironmentPhaseReady {
		if err := r.updateStatus(ctx, environment, v1.EnvironmentPhaseReady, "Environment is ready"); err != nil {
			return ctrl.Result{}, err
		}
		r.recorder.Event(environment, corev1.EventTypeNormal, "EnvironmentReady", "Environment is ready")
	}

	return ctrl.Result{RequeueAfter: RequeueDelay}, nil
}

// handleDeletion processes the deletion of an environment
func (r *EnvironmentReconciler) handleDeletion(ctx context.Context, env *v1.Environment) (ctrl.Result, error) {
	logger := r.logger.WithValues("environment", env.Name)
	logger.V(1).Info("Handling deletion of environment")

	// Update status to Deleting if not already set
	if env.Status.Phase != v1.EnvironmentPhaseDeleting {
		if err := r.updateStatus(ctx, env, v1.EnvironmentPhaseDeleting, "Environment deletion in progress"); err != nil {
			return ctrl.Result{}, err
		}
		return ctrl.Result{Requeue: true}, nil
	}

	// First, try to delete the role binding as it depends on the namespace
	// This is a no-op if the role binding doesn't exist anymore
	if err := r.roleBindingManager.Delete(ctx, env); err != nil {
		logger.Error(err, "Failed to delete role binding")
		// Log error but continue to namespace deletion
	}

	var namespaceName string = r.namespaceManager.GetNamespaceName(env)
	skipWaiting := false

	// Check if namespace still exists
	exists, err := r.namespaceManager.Exists(ctx, env)
	if err != nil && !errors.IsNotFound(err) {
		return ctrl.Result{Requeue: true}, fmt.Errorf("failed to check if namespace exists during deletion: %w", err)
	}

	if exists {
		// Check if namespace is already being deleted
		isDeleting, _ := r.namespaceManager.IsDeleting(ctx, env)
		if !isDeleting {
			// Namespace exists and is not being deleted, so delete it
			logger.V(0).Info("Deleting namespace", "namespace", namespaceName)
			err := r.namespaceManager.Delete(ctx, env)
			if err != nil {
				// If error is due to namespace not being managed, log but continue with Environment deletion
				if strings.Contains(err.Error(), "not managed by this operator") {
					logger.V(0).Info("Namespace not managed by this operator, continuing with Environment deletion", "namespace", namespaceName)
					r.recorder.Event(env, corev1.EventTypeWarning, "NamespaceNotManaged",
						fmt.Sprintf("Namespace %s is not managed by this operator and was not deleted", namespaceName))

					// Update status to reflect unmanaged namespace, but only if current phase isn't already Failed
					if env.Status.NamespaceStatus != nil {
						updateResourceStatusIfNotFailed(env.Status.NamespaceStatus, v1.ResourceStatusPhaseFailed,
							fmt.Sprintf("Namespace %s is not managed by this operator", namespaceName))

						if updateErr := r.environmentManager.UpdateStatus(ctx, env); updateErr != nil {
							logger.Error(updateErr, "Failed to update namespace status")
						}
					}

					// For unmanaged namespaces, skip waiting and proceed to finalizer removal
					skipWaiting = true
				} else if strings.Contains(err.Error(), "environment-id mismatch") {
					// The namespace carries our management label but belongs to a different
					// Environment, so it is not ours to delete. Leave it intact and proceed to
					// finalizer removal — waiting for it to disappear would wedge this
					// Environment's deletion forever (the foreign namespace is never deleted).
					logger.V(0).Info("Namespace owned by a different environment, continuing with Environment deletion", "namespace", namespaceName)

					// Update status to reflect the ownership mismatch, but only if current phase isn't already Failed
					if env.Status.NamespaceStatus != nil {
						updateResourceStatusIfNotFailed(env.Status.NamespaceStatus, v1.ResourceStatusPhaseFailed,
							fmt.Sprintf("Namespace %s is owned by a different environment", namespaceName))

						if updateErr := r.environmentManager.UpdateStatus(ctx, env); updateErr != nil {
							logger.Error(updateErr, "Failed to update namespace status")
						}
					}

					// Foreign namespace: skip waiting and proceed to finalizer removal
					skipWaiting = true
				} else {
					// For other errors, retry
					return ctrl.Result{Requeue: true}, fmt.Errorf("failed to delete namespace: %w", err)
				}
			} else {
				// Successfully initiated namespace deletion, only update if not already in a special state
				if env.Status.NamespaceStatus != nil {
					updateResourceStatusIfNotSpecial(env.Status.NamespaceStatus, v1.ResourceStatusPhaseDeleting,
						fmt.Sprintf("Deleting namespace %s", namespaceName))

					if updateErr := r.environmentManager.UpdateStatus(ctx, env); updateErr != nil {
						logger.Error(updateErr, "Failed to update namespace status")
					}
				}
			}
		}

		// Wait for the namespace deletion to complete (unless we're skipping for unmanaged namespaces)
		if !skipWaiting {
			logger.V(1).Info("Namespace is being deleted, waiting to complete", "namespace", namespaceName)
			return ctrl.Result{Requeue: true, RequeueAfter: time.Second * 5}, nil
		}
	}

	// Namespace is gone or not managed, remove finalizer and complete deletion
	logger.V(1).Info("Environment resources cleaned up, removing finalizer")

	if err := r.environmentManager.RemoveFinalizer(ctx, env, EnvironmentFinalizer); err != nil {
		return ctrl.Result{Requeue: true}, fmt.Errorf("failed to remove finalizer: %w", err)
	}

	logger.V(0).Info("Environment deletion completed")
	return ctrl.Result{}, nil
}

// updateStatus updates the environment status
func (r *EnvironmentReconciler) updateStatus(ctx context.Context, env *v1.Environment, phase v1.EnvironmentPhase, message string) error {
	logger := r.logger.WithValues("environment", env.Name)
	logger.V(0).Info("Updating environment status", "phase", phase, "message", message)

	if env.Status.Phase != phase || env.Status.Message != message {
		// Update status fields
		env.Status.Phase = phase
		env.Status.Message = message
		env.Status.LastUpdated = metav1.Now()

		// Update ObservedGeneration to track which version we're processing
		if env.Status.ObservedGeneration != env.Generation {
			env.Status.ObservedGeneration = env.Generation
		}

		// Initialize sub-resource statuses if they don't exist
		if env.Status.NamespaceStatus == nil {
			env.Status.NamespaceStatus = &v1.ResourceStatus{}
		}

		if env.Status.RoleBindingStatus == nil {
			env.Status.RoleBindingStatus = &v1.ResourceStatus{}
		}

		// Only update sub-resource statuses when:
		// 1. They haven't been set yet (empty phase)
		// 2. Current phase is lower priority than new phase (Creating -> Active -> Deleting)
		// 3. Environment phase indicates failure

		// Update namespace status based on environment phase
		switch phase {
		case v1.EnvironmentPhaseInProgress:
			// Only set Creating if status hasn't been set yet
			updateResourceStatusIfEmpty(env.Status.NamespaceStatus, v1.ResourceStatusPhaseCreating, "Namespace is being provisioned")
			updateResourceStatusIfEmpty(env.Status.RoleBindingStatus, v1.ResourceStatusPhaseCreating, "RoleBinding will be created after namespace")

		case v1.EnvironmentPhaseReady:
			// Only upgrade to Active if current phase is Creating
			updateResourceStatusIfCreating(env.Status.NamespaceStatus, v1.ResourceStatusPhaseActive, "Namespace is active")
			updateResourceStatusIfCreating(env.Status.RoleBindingStatus, v1.ResourceStatusPhaseActive, "RoleBinding is active")

		case v1.EnvironmentPhaseFailed:
			// Only update to Failed if the message indicates a resource-specific failure
			if strings.Contains(message, "namespace") || strings.Contains(message, "Namespace") {
				updateResourceStatusIfNotFailed(env.Status.NamespaceStatus, v1.ResourceStatusPhaseFailed, "Namespace creation or reconciliation failed")
			}

			if strings.Contains(message, "role binding") || strings.Contains(message, "Role binding") {
				updateResourceStatusIfNotFailed(env.Status.RoleBindingStatus, v1.ResourceStatusPhaseFailed, "RoleBinding creation or reconciliation failed")
			}

		case v1.EnvironmentPhaseDeleting:
			// Only update if not already failed or deleting
			updateResourceStatusIfNotSpecial(env.Status.NamespaceStatus, v1.ResourceStatusPhaseDeleting, "Namespace is being deleted")
			updateResourceStatusIfNotSpecial(env.Status.RoleBindingStatus, v1.ResourceStatusPhaseDeleting, "RoleBinding will be deleted with namespace")
		}

		// Use environment manager to update status
		return r.environmentManager.UpdateStatus(ctx, env)
	}

	return nil
}

// updateResourceStatusIfEmpty updates a resource status if its phase is empty
func updateResourceStatusIfEmpty(status *v1.ResourceStatus, phase v1.ResourceStatusPhase, message string) {
	if status.Phase == "" {
		status.Phase = phase
		status.Message = message
	}
}

// updateResourceStatusIfCreating updates a resource status if its phase is Creating
func updateResourceStatusIfCreating(status *v1.ResourceStatus, phase v1.ResourceStatusPhase, message string) {
	if status.Phase == v1.ResourceStatusPhaseCreating {
		status.Phase = phase
		status.Message = message
	}
}

// updateResourceStatusIfNotFailed updates a resource status if its phase is not Failed
func updateResourceStatusIfNotFailed(status *v1.ResourceStatus, phase v1.ResourceStatusPhase, message string) {
	if status.Phase != v1.ResourceStatusPhaseFailed {
		status.Phase = phase
		status.Message = message
	}
}

// updateResourceStatusIfNotSpecial updates a resource status if it's not in a special state (Failed or Deleting)
func updateResourceStatusIfNotSpecial(status *v1.ResourceStatus, phase v1.ResourceStatusPhase, message string) {
	if status.Phase != v1.ResourceStatusPhaseFailed && status.Phase != v1.ResourceStatusPhaseDeleting {
		status.Phase = phase
		status.Message = message
	}
}

// setResourceNameIfEmpty stores the resolved name on the resource status, creating the
// status struct if needed, but only when no name has been persisted yet. It reports
// whether a new name was assigned (so the caller can persist it). The getters are pure,
// so the reconciler owns persistence of the name.
func setResourceNameIfEmpty(statusPtr **v1.ResourceStatus, name string) bool {
	if *statusPtr == nil {
		*statusPtr = &v1.ResourceStatus{}
	}
	if (*statusPtr).ResourceName == "" {
		(*statusPtr).ResourceName = name
		return true
	}
	return false
}

// initResourceStatus initializes a resource status if it's nil or updates it if it has an empty phase
func initResourceStatus(statusPtr **v1.ResourceStatus, phase v1.ResourceStatusPhase, message string) {
	if *statusPtr == nil {
		*statusPtr = &v1.ResourceStatus{}
	}
	(*statusPtr).Phase = phase
	(*statusPtr).Message = message
}

// SetupWithManager sets up the controller with the Manager
func (r *EnvironmentReconciler) SetupWithManager(mgr ctrl.Manager) error {
	// Setup a channel to indicate when the cache is started
	startedCh := make(chan struct{})

	// Add an event handler for when the cache starts
	if err := mgr.Add(
		manager.RunnableFunc(func(ctx context.Context) error {
			<-mgr.Elected()
			close(startedCh)
			return nil
		}),
	); err != nil {
		return err
	}

	// Trigger reconciliation for all environments after cache has started
	go func() {
		<-startedCh
		r.ReconcileAllEnvironments(context.Background())
	}()

	return ctrl.NewControllerManagedBy(mgr).
		// Use a predicate that ignores namespace fields for cluster-scoped resources
		For(&v1.Environment{}, builder.WithPredicates(predicate.ResourceVersionChangedPredicate{})).
		Owns(&corev1.Namespace{}).
		Complete(r)
}

// ReconcileAllEnvironments lists all environments and triggers reconciliation for each
func (r *EnvironmentReconciler) ReconcileAllEnvironments(ctx context.Context) {
	logger := r.logger.WithName("startup-reconciler")
	logger.Info("Triggering reconciliation for all environments")

	environmentList, err := r.environmentManager.GetList(ctx)
	if err != nil {
		logger.Error(err, "Failed to list environments")
		return
	}

	count := len(environmentList.Items)
	if count == 0 {
		logger.Info("No environments found")
		return
	}

	logger.Info("Queueing environments for reconciliation", "count", count)

	for _, env := range environmentList.Items {
		req := ctrl.Request{
			NamespacedName: types.NamespacedName{
				Name: env.Name,
			},
		}
		go r.Reconcile(ctx, req)
	}

	logger.Info("All environments queued for reconciliation")
}
