package controllers

import (
	"context"
	"fmt"
	"time"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/tools/record"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"sigs.k8s.io/controller-runtime/pkg/log"

	quixiov1 "github.com/quix-analytics/quix-environment-operator/api/v1"
	"github.com/quix-analytics/quix-environment-operator/internal/config"
	"github.com/quix-analytics/quix-environment-operator/internal/namespaces"
	"github.com/quix-analytics/quix-environment-operator/internal/status"
)

const (
	// Finalizer for environment resources
	EnvironmentFinalizer = "quix.io/environment-finalizer"
	ManagedByLabel       = "quix.io/managed-by"
	OperatorName         = "quix-environment-operator"

	// Labels for environment ownership tracking
	LabelEnvironmentPrefix = "quix.io/environment-"
	LabelEnvironmentName   = "quix.io/environment-name"
	LabelEnvironmentID     = "quix.io/environment-id"

	// Annotations for environment tracking
	AnnotationEnvironmentCRDNamespace = "quix.io/environment-crd-namespace"

	// Event reasons
	EventReasonValidationFailed           = "ValidationFailed"
	EventReasonNamespaceCreationFailed    = "NamespaceCreationFailed"
	EventReasonCollisionDetected          = "CollisionDetected"
	EventReasonUpdateFailed               = "UpdateFailed"
	EventReasonNamespaceCreated           = "NamespaceCreated"
	EventReasonRoleBindingCreated         = "RoleBindingCreated"
	EventReasonRoleBindingUpdated         = "RoleBindingUpdated"
	EventReasonNamespaceDeleteFailed      = "NamespaceDeleteFailed"
	EventReasonNamespaceDeletionInitiated = "NamespaceDeletionInitiated"
	EventReasonNamespaceDeleted           = "NamespaceDeleted"

	// Common error and status messages
	MsgNamespaceBeingDeleted = "Managed namespace is being deleted"
	MsgEnvProvisionSuccess   = "Environment provisioned successfully"
	MsgDependenciesNotReady  = "Environment dependencies are not yet ready"
	MsgDependenciesFailed    = "One or more environment dependencies failed"
)

// EnvironmentReconciler reconciles an Environment object
type EnvironmentReconciler struct {
	client.Client
	Scheme   *runtime.Scheme
	Recorder record.EventRecorder
	Config   *config.OperatorConfig

	namespaceManager namespaces.NamespaceManager
	statusUpdater    status.StatusUpdater
}

// +kubebuilder:rbac:groups=quix.io,resources=environments,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=quix.io,resources=environments/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=quix.io,resources=environments/finalizers,verbs=update
// +kubebuilder:rbac:groups=core,resources=namespaces,verbs=get;list;watch;create;delete;update;patch
// +kubebuilder:rbac:groups=rbac.authorization.k8s.io,resources=rolebindings,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=core,resources=events,verbs=create;patch

// NewEnvironmentReconciler creates a new reconciler with required dependencies
func NewEnvironmentReconciler(
	client client.Client,
	scheme *runtime.Scheme,
	recorder record.EventRecorder,
	config *config.OperatorConfig,
	namespaceManager namespaces.NamespaceManager,
	statusUpdater status.StatusUpdater,
) (*EnvironmentReconciler, error) {
	if namespaceManager == nil {
		return nil, fmt.Errorf("NamespaceManager is required but was not provided")
	}
	if statusUpdater == nil {
		return nil, fmt.Errorf("StatusUpdater is required but was not provided")
	}

	return &EnvironmentReconciler{
		Client:           client,
		Scheme:           scheme,
		Recorder:         recorder,
		Config:           config,
		namespaceManager: namespaceManager,
		statusUpdater:    statusUpdater,
	}, nil
}

// Reconcile handles the reconciliation logic for Environment resources
func (r *EnvironmentReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	logger := log.FromContext(ctx)
	logger.Info("Reconciling Environment", "request", req.NamespacedName)

	// Fetch the Environment resource
	env, result, err := r.fetchEnvironment(ctx, req)
	if err != nil || !result.IsZero() {
		return result, err
	}

	// Environment not found, nothing to do
	if env == nil {
		return ctrl.Result{}, nil
	}

	logger.V(3).Info("Retrieved Environment",
		"phase", env.Status.Phase,
		"hasFinalizer", controllerutil.ContainsFinalizer(env, EnvironmentFinalizer),
		"isBeingDeleted", !env.DeletionTimestamp.IsZero())

	// Handle finalizer addition
	if result, err := r.ensureFinalizer(ctx, env); !result.IsZero() || err != nil {
		return result, err
	}

	// Handle deletion flow if needed
	if !env.DeletionTimestamp.IsZero() {
		return r.handleDeletionFlow(ctx, env)
	}

	// Active Reconciliation
	return r.reconcileActive(ctx, env)
}

// Accessors - Exported for testing

// NamespaceManager returns the namespaceManager dependency
func (r *EnvironmentReconciler) NamespaceManager() namespaces.NamespaceManager {
	return r.namespaceManager
}

// StatusUpdater returns the statusUpdater dependency
func (r *EnvironmentReconciler) StatusUpdater() status.StatusUpdater {
	return r.statusUpdater
}

// handleDeletionFlow manages the deletion process for an Environment
func (r *EnvironmentReconciler) handleDeletionFlow(ctx context.Context, env *quixiov1.Environment) (ctrl.Result, error) {
	if env.Status.Phase != quixiov1.PhaseDeleting {
		statusErr := r.StatusUpdater().UpdateStatus(ctx, env, func(st *quixiov1.EnvironmentStatus) {
			st.Phase = quixiov1.PhaseDeleting
		})
		if statusErr != nil {
			return r.handleStatusUpdateError(ctx, statusErr, "Failed to update phase to Deleting")
		}
		return ctrl.Result{Requeue: true}, nil
	}

	return r.processEnvironmentDeletion(ctx, env)
}

// getNamespaceName returns the namespace name for an environment
func (r *EnvironmentReconciler) getNamespaceName(env *quixiov1.Environment) string {
	return fmt.Sprintf("%s%s", env.Spec.Id, r.Config.NamespaceSuffix)
}

// processEnvironmentDeletion handles the environment deletion process
func (r *EnvironmentReconciler) processEnvironmentDeletion(ctx context.Context, env *quixiov1.Environment) (ctrl.Result, error) {
	logger := log.FromContext(ctx).WithValues("environment", env.Name)
	logger.Info("Processing environment deletion")

	// Use helper method to get namespace name
	namespaceName := r.getNamespaceName(env)

	// Check namespace state
	nsState, err := r.checkNamespaceStateForDeletion(ctx, env, namespaceName)
	if err != nil {
		return ctrl.Result{RequeueAfter: 5 * time.Second}, err
	}

	logger.Info("Namespace state during deletion", "state", nsState)

	// Always update the status first
	if nsState == quixiov1.PhaseStateDeleted || nsState == quixiov1.PhaseStateUnmanaged {
		r.updateNamespaceStatusForDeletion(ctx, env, nsState, namespaceName)
	}

	// Handle namespace based on its state
	return r.handleNamespaceDeletionByState(ctx, env, namespaceName, nsState)
}

// reconcileActive handles the active reconciliation process for an Environment
func (r *EnvironmentReconciler) reconcileActive(ctx context.Context, env *quixiov1.Environment) (ctrl.Result, error) {
	// Initialize status if needed
	if result, err := r.initializeStatus(ctx, env); !result.IsZero() || err != nil {
		return result, err
	}

	// Validate the environment spec
	if validationErr := r.validateEnvironment(ctx, env); validationErr != nil {
		r.Recorder.Eventf(env, corev1.EventTypeWarning, EventReasonValidationFailed, "Environment validation failed: %v", validationErr)
		_ = r.StatusUpdater().SetErrorStatus(ctx, env, quixiov1.PhaseCreateFailed,
			validationErr, "Environment validation failed")
		return ctrl.Result{}, nil
	}

	// Use helper method to get namespace name
	namespaceName := r.getNamespaceName(env)

	// Reconcile namespace
	namespaceResult, namespaceReadyUpdateNeeded, err := r.reconcileNamespace(ctx, env, namespaceName)
	if !namespaceResult.IsZero() || err != nil {
		return namespaceResult, err
	}

	// Reconcile RoleBinding
	rbResult, roleBindingReadyUpdateNeeded, err := r.reconcileRoleBindingAndStatus(ctx, env, namespaceName)
	if !rbResult.IsZero() || err != nil {
		return rbResult, err
	}

	// Update final status
	return r.updateFinalStatus(ctx, env, namespaceReadyUpdateNeeded, roleBindingReadyUpdateNeeded)
}

// fetchEnvironment retrieves the Environment resource
func (r *EnvironmentReconciler) fetchEnvironment(ctx context.Context, req ctrl.Request) (*quixiov1.Environment, ctrl.Result, error) {
	logger := log.FromContext(ctx)
	env := &quixiov1.Environment{}

	if err := r.Get(ctx, req.NamespacedName, env); err != nil {
		if errors.IsNotFound(err) {
			logger.Info("Environment resource not found, ignoring")
			return nil, ctrl.Result{}, nil
		}
		logger.V(3).Error(err, "Failed to get Environment")
		return nil, ctrl.Result{RequeueAfter: 2 * time.Second}, nil
	}

	return env, ctrl.Result{}, nil
}

// ensureFinalizer ensures the finalizer is added to the Environment resource
func (r *EnvironmentReconciler) ensureFinalizer(ctx context.Context, env *quixiov1.Environment) (ctrl.Result, error) {
	logger := log.FromContext(ctx)

	if !controllerutil.ContainsFinalizer(env, EnvironmentFinalizer) {
		logger.Info("Adding finalizer")
		controllerutil.AddFinalizer(env, EnvironmentFinalizer)
		if err := r.Update(ctx, env); err != nil {
			if errors.IsConflict(err) {
				logger.Info("Conflict adding finalizer, requeuing")
				return ctrl.Result{Requeue: true}, nil
			}
			return r.handleStatusUpdateError(ctx, err, "Failed to add finalizer")
		}
		return ctrl.Result{Requeue: true}, nil
	}

	return ctrl.Result{}, nil
}

// validateEnvironment validates the Environment spec
func (r *EnvironmentReconciler) validateEnvironment(ctx context.Context, env *quixiov1.Environment) error {
	if env.Spec.Id == "" {
		return fmt.Errorf("spec.id is required")
	}

	namespaceName := r.getNamespaceName(env)

	if len(namespaceName) > 63 {
		return fmt.Errorf("generated namespace name '%s' (from spec.id '%s' and suffix '%s') exceeds the 63 character limit",
			namespaceName, env.Spec.Id, r.Config.NamespaceSuffix)
	}

	return nil
}

// removeFinalizer removes the finalizer from the Environment resource
func (r *EnvironmentReconciler) removeFinalizer(ctx context.Context, env *quixiov1.Environment) (ctrl.Result, error) {
	logger := log.FromContext(ctx)
	logger.Info("Proceeding to remove finalizer")

	latestEnv := &quixiov1.Environment{}
	// Use client.ObjectKeyFromObject to get the key for fetching
	if err := r.Get(ctx, client.ObjectKeyFromObject(env), latestEnv); err != nil {
		if errors.IsNotFound(err) {
			logger.Info("Environment resource already deleted while attempting finalizer removal")
			return ctrl.Result{}, nil
		}
		logger.V(3).Error(err, "Failed to refetch Environment before finalizer removal")
		return ctrl.Result{RequeueAfter: 2 * time.Second}, err
	}

	if controllerutil.ContainsFinalizer(latestEnv, EnvironmentFinalizer) {
		controllerutil.RemoveFinalizer(latestEnv, EnvironmentFinalizer)
		if err := r.Update(ctx, latestEnv); err != nil {
			if errors.IsConflict(err) {
				logger.Info("Conflict removing finalizer, requeuing")
				return ctrl.Result{Requeue: true}, nil
			}
			logger.V(3).Error(err, "Failed to remove finalizer")
			return ctrl.Result{RequeueAfter: 2 * time.Second}, err
		}
		logger.Info("Finalizer removed successfully")
	}

	return ctrl.Result{}, nil
}
