package controllers

import (
	"context"
	"fmt"
	"reflect"
	"strings"
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

	"github.com/go-logr/logr"
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

	// Private fields with required dependencies
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

	logger.V(1).Info("Retrieved Environment",
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

// SetupWithManager sets up the controller with the Manager
func (r *EnvironmentReconciler) SetupWithManager(mgr ctrl.Manager) error {
	setupLogger := ctrl.Log.WithName("setup").WithName("Environment")

	// Set up the field indexer for efficient lookups
	if err := r.setupFieldIndexer(mgr, setupLogger); err != nil {
		return err
	}

	// Create predicates for filtering events
	namespacePredicate := r.createNamespacePredicate()
	environmentPredicate := r.createEnvironmentPredicate(setupLogger)

	// Create handler for mapping namespace events to environment reconcile requests
	namespaceHandler := r.createNamespaceHandler(mgr)

	// Configure and build the controller
	return r.configureController(mgr, environmentPredicate, namespacePredicate, namespaceHandler)
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

// Core reconciliation flow
// ------------------------------

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

// Namespace management
// ------------------------------

// checkNamespaceExists checks if a namespace exists and logs appropriately
func (r *EnvironmentReconciler) checkNamespaceExists(ctx context.Context, name string) (*corev1.Namespace, error) {
	logger := log.FromContext(ctx)
	namespace := &corev1.Namespace{}
	err := r.Get(ctx, types.NamespacedName{Name: name}, namespace)

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

// getNamespaceName returns the namespace name for an environment
func (r *EnvironmentReconciler) getNamespaceName(env *quixiov1.Environment) string {
	return fmt.Sprintf("%s%s", env.Spec.Id, r.Config.NamespaceSuffix)
}

// getNamespace is kept for backward compatibility but delegates to checkNamespaceExists
func (r *EnvironmentReconciler) getNamespace(ctx context.Context, name string) (*corev1.Namespace, error) {
	return r.checkNamespaceExists(ctx, name)
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

// reconcileNamespace reconciles the namespace for the environment
func (r *EnvironmentReconciler) reconcileNamespace(ctx context.Context, env *quixiov1.Environment, namespaceName string) (ctrl.Result, bool, error) {
	// Get namespace and delegate to appropriate handler
	namespace, nsGetErr := r.getNamespace(ctx, namespaceName)

	if nsGetErr != nil {
		return r.handleNamespaceNotFound(ctx, env, namespaceName, nsGetErr)
	}

	// --- Namespace Found --- //
	return r.handleExistingNamespace(ctx, env, namespace, namespaceName)
}

// handleNamespaceNotFound handles the case when a namespace is not found or there's an error getting it
func (r *EnvironmentReconciler) handleNamespaceNotFound(ctx context.Context, env *quixiov1.Environment, namespaceName string, nsGetErr error) (ctrl.Result, bool, error) {
	logger := log.FromContext(ctx)

	if errors.IsNotFound(nsGetErr) {
		// --- Namespace Not Found: Create it --- //
		if env.Status.NamespacePhase != string(quixiov1.PhaseStateCreating) {
			statusUpdateErr := r.StatusUpdater().UpdateStatus(ctx, env, func(st *quixiov1.EnvironmentStatus) {
				st.NamespacePhase = string(quixiov1.PhaseStateCreating)
				st.Phase = quixiov1.PhaseCreating
			})
			if statusUpdateErr != nil {
				logger.V(3).Error(statusUpdateErr, "Failed to update NamespacePhase to Creating")
				return ctrl.Result{RequeueAfter: 1 * time.Second}, false, statusUpdateErr
			}
			return ctrl.Result{Requeue: true}, false, nil
		}

		createErr := r.createNamespace(ctx, env, namespaceName)
		if createErr != nil {
			logger.V(3).Error(createErr, "Failed to create namespace")
			r.Recorder.Eventf(env, corev1.EventTypeWarning, EventReasonNamespaceCreationFailed, "Failed to create namespace %s: %v", namespaceName, createErr)

			_ = r.StatusUpdater().SetErrorStatus(ctx, env, quixiov1.PhaseCreateFailed,
				createErr, fmt.Sprintf("Failed to create namespace %s", namespaceName))
			return ctrl.Result{RequeueAfter: 5 * time.Second}, false, createErr
		}

		log.FromContext(ctx).Info("Namespace creation request submitted, requeuing for confirmation", "namespace", namespaceName)
		return ctrl.Result{RequeueAfter: 500 * time.Millisecond}, false, nil

	} else {
		// Error other than NotFound when getting namespace
		logger.V(3).Error(nsGetErr, "Failed to get namespace")
		currentPhase := env.Status.Phase
		if currentPhase != quixiov1.PhaseDeleting {
			if currentPhase == quixiov1.PhaseCreating {
				currentPhase = quixiov1.PhaseCreateFailed
			} else {
				currentPhase = quixiov1.PhaseUpdateFailed
			}
		}
		_ = r.StatusUpdater().SetErrorStatus(ctx, env, currentPhase,
			nsGetErr, fmt.Sprintf("Failed to get namespace %s", namespaceName))
		return ctrl.Result{RequeueAfter: 5 * time.Second}, false, nsGetErr
	}
}

// handleExistingNamespace handles reconciliation for an existing namespace
func (r *EnvironmentReconciler) handleExistingNamespace(ctx context.Context, env *quixiov1.Environment, namespace *corev1.Namespace, namespaceName string) (ctrl.Result, bool, error) {
	logger := log.FromContext(ctx)

	// Check if namespace is managed by us
	if !r.NamespaceManager().IsNamespaceManaged(namespace) {
		collisionErr := fmt.Errorf("namespace '%s' already exists and is not managed by this operator", namespaceName)
		r.Recorder.Eventf(env, corev1.EventTypeWarning, EventReasonCollisionDetected, "Namespace %s already exists and is not managed by this operator", namespaceName)

		// Always use CreateFailed for namespace collisions, since this is a blocking error
		phase := quixiov1.PhaseCreateFailed

		statusUpdateErr := r.StatusUpdater().UpdateStatus(ctx, env, func(st *quixiov1.EnvironmentStatus) {
			st.Phase = phase
			st.NamespacePhase = string(quixiov1.PhaseStateUnmanaged)
			st.ErrorMessage = collisionErr.Error()
			meta.SetStatusCondition(&st.Conditions, metav1.Condition{
				Type:    status.ConditionTypeReady,
				Status:  metav1.ConditionFalse,
				Reason:  status.ReasonFailed,
				Message: st.ErrorMessage,
			})
		})
		if statusUpdateErr != nil {
			logger.V(3).Error(statusUpdateErr, "Failed to update status for collision")
			return ctrl.Result{RequeueAfter: 1 * time.Second}, false, statusUpdateErr
		}

		return ctrl.Result{}, false, nil
	}

	// Check if the namespace is terminating
	if r.NamespaceManager().IsNamespaceDeleted(namespace, nil) {
		logger.Info("Managed namespace is terminating, waiting for deletion", "namespace", namespaceName)
		statusUpdateErr := r.StatusUpdater().UpdateStatus(ctx, env, func(st *quixiov1.EnvironmentStatus) {
			st.NamespacePhase = string(quixiov1.PhaseStateTerminating)
			st.Phase = quixiov1.PhaseCreating
			meta.SetStatusCondition(&st.Conditions, metav1.Condition{
				Type:    status.ConditionTypeReady,
				Status:  metav1.ConditionFalse,
				Reason:  status.ReasonNamespaceTerminating,
				Message: MsgNamespaceBeingDeleted,
			})
		})
		if statusUpdateErr != nil {
			logger.V(3).Error(statusUpdateErr, "Failed to update status for terminating namespace")
			return ctrl.Result{RequeueAfter: 1 * time.Second}, false, statusUpdateErr
		}
		return ctrl.Result{RequeueAfter: 5 * time.Second}, false, nil
	}

	// --- Namespace Exists, is Managed, and Not Terminating --- //

	// Ensure namespace metadata (labels, annotations) is up-to-date
	if r.NamespaceManager().ApplyMetadata(env, namespace) {
		if updateErr := r.Update(ctx, namespace); updateErr != nil {
			logger.V(3).Error(updateErr, "Failed to update namespace metadata")
			r.Recorder.Eventf(env, corev1.EventTypeWarning, EventReasonUpdateFailed,
				"Failed to update namespace metadata: %v", updateErr)

			_ = r.StatusUpdater().SetErrorStatus(ctx, env, quixiov1.PhaseUpdateFailed,
				updateErr, "Failed to update namespace metadata")

			return ctrl.Result{RequeueAfter: 5 * time.Second}, false, updateErr
		}
		logger.Info("Updated namespace metadata", "namespace", namespaceName)
	}

	// Update NamespacePhase to Ready if it wasn't already
	namespaceReadyUpdateNeeded := env.Status.NamespacePhase != string(quixiov1.PhaseStateReady)

	return ctrl.Result{}, namespaceReadyUpdateNeeded, nil
}

// reconcileRoleBinding ensures the managed RoleBinding exists and is correctly configured.
func (r *EnvironmentReconciler) reconcileRoleBinding(ctx context.Context, env *quixiov1.Environment, namespaceName string) (ctrl.Result, error) {
	logger := log.FromContext(ctx).WithValues("namespace", namespaceName)
	rbName := r.Config.GetRoleBindingName()
	logger = logger.WithValues("roleBinding", rbName)

	desiredRb := r.defineManagedRoleBinding(env, namespaceName)
	foundRb := &rbacv1.RoleBinding{}

	err := r.Get(ctx, types.NamespacedName{Name: rbName, Namespace: namespaceName}, foundRb)
	if err != nil {
		if errors.IsNotFound(err) {
			return r.createRoleBinding(ctx, env, desiredRb, rbName, logger)
		} else {
			// Error getting RoleBinding
			logger.V(3).Error(err, "Failed to get managed RoleBinding")
			_ = r.statusUpdater.SetErrorStatus(ctx, env, quixiov1.PhaseUpdateFailed,
				err, fmt.Sprintf("Failed to get RoleBinding %s", rbName))
			return ctrl.Result{RequeueAfter: 5 * time.Second}, err
		}
	}

	// RoleBinding exists, check if update needed
	return r.updateRoleBindingIfNeeded(ctx, env, desiredRb, foundRb, rbName, logger)
}

// createRoleBinding handles the creation of a new RoleBinding
func (r *EnvironmentReconciler) createRoleBinding(
	ctx context.Context,
	env *quixiov1.Environment,
	desiredRb *rbacv1.RoleBinding,
	rbName string,
	logger logr.Logger,
) (ctrl.Result, error) {
	logger.Info("RoleBinding not found, attempting creation")
	if env.Status.RoleBindingPhase != string(quixiov1.PhaseStateCreating) {
		statusUpdateErr := r.statusUpdater.UpdateStatus(ctx, env, func(st *quixiov1.EnvironmentStatus) {
			st.RoleBindingPhase = string(quixiov1.PhaseStateCreating)
			if st.Phase == quixiov1.PhaseReady {
				st.Phase = quixiov1.PhaseCreating
			}
		})
		if statusUpdateErr != nil {
			logger.V(3).Error(statusUpdateErr, "Failed to update RoleBindingPhase to Creating")
			return ctrl.Result{RequeueAfter: 1 * time.Second}, statusUpdateErr
		}
		return ctrl.Result{Requeue: true}, nil
	}

	logger.Info("Creating managed RoleBinding")
	if createErr := r.Create(ctx, desiredRb); createErr != nil {
		logger.V(3).Error(createErr, "Failed to create managed RoleBinding")
		_ = r.statusUpdater.SetErrorStatus(ctx, env, quixiov1.PhaseCreateFailed,
			createErr, fmt.Sprintf("Failed to create RoleBinding %s", rbName))
		return ctrl.Result{RequeueAfter: 5 * time.Second}, createErr
	}

	logger.Info("Managed RoleBinding creation submitted")
	r.Recorder.Eventf(env, corev1.EventTypeNormal, EventReasonRoleBindingCreated, "Created RoleBinding %s", rbName)
	return ctrl.Result{RequeueAfter: 500 * time.Millisecond}, nil
}

// updateRoleBindingIfNeeded checks if a RoleBinding needs updates and applies them
func (r *EnvironmentReconciler) updateRoleBindingIfNeeded(
	ctx context.Context,
	env *quixiov1.Environment,
	desiredRb *rbacv1.RoleBinding,
	foundRb *rbacv1.RoleBinding,
	rbName string,
	logger logr.Logger,
) (ctrl.Result, error) {
	// Check if updates are needed
	updateNeeded := r.checkRoleBindingUpdates(env, desiredRb, foundRb, logger)

	if updateNeeded {
		return r.applyRoleBindingUpdates(ctx, env, foundRb, rbName, logger)
	}

	logger.V(1).Info("Managed RoleBinding is up-to-date")
	return ctrl.Result{}, nil
}

// checkRoleBindingUpdates verifies if any RoleBinding properties need updating
func (r *EnvironmentReconciler) checkRoleBindingUpdates(
	env *quixiov1.Environment,
	desiredRb *rbacv1.RoleBinding,
	foundRb *rbacv1.RoleBinding,
	logger logr.Logger,
) bool {
	// Check each field independently and combine the results
	ownerRefsUpdated := r.checkOwnerReferences(env, desiredRb, foundRb, logger)
	labelsUpdated := r.checkAndUpdateLabels(env, desiredRb, foundRb, logger)
	roleRefUpdated := r.checkAndUpdateRoleRef(desiredRb, foundRb, logger)
	subjectsUpdated := r.checkAndUpdateSubjects(desiredRb, foundRb, logger)

	// Return true if any field was updated
	return ownerRefsUpdated || labelsUpdated || roleRefUpdated || subjectsUpdated
}

// applyRoleBindingUpdates applies the updates to a RoleBinding
func (r *EnvironmentReconciler) applyRoleBindingUpdates(
	ctx context.Context,
	env *quixiov1.Environment,
	foundRb *rbacv1.RoleBinding,
	rbName string,
	logger logr.Logger,
) (ctrl.Result, error) {
	logger.Info("Updating managed RoleBinding")
	if updateErr := r.Update(ctx, foundRb); updateErr != nil {
		if errors.IsConflict(updateErr) {
			logger.Info("Conflict updating RoleBinding, requeuing")
			return ctrl.Result{Requeue: true}, nil
		}
		logger.V(3).Error(updateErr, "Failed to update managed RoleBinding")
		_ = r.statusUpdater.SetErrorStatus(ctx, env, quixiov1.PhaseUpdateFailed,
			updateErr, fmt.Sprintf("Failed to update RoleBinding %s", rbName))
		return ctrl.Result{RequeueAfter: 5 * time.Second}, updateErr
	}
	logger.Info("Managed RoleBinding updated successfully")
	r.Recorder.Eventf(env, corev1.EventTypeNormal, EventReasonRoleBindingUpdated, "Updated RoleBinding %s", rbName)
	return ctrl.Result{Requeue: true}, nil
}

// checkOwnerReferences checks and updates owner references if needed
func (r *EnvironmentReconciler) checkOwnerReferences(
	env *quixiov1.Environment,
	desiredRb *rbacv1.RoleBinding,
	foundRb *rbacv1.RoleBinding,
	logger logr.Logger,
) bool {
	updateNeeded := false

	if len(desiredRb.OwnerReferences) > 0 {
		tempDesiredRbWithOwner := desiredRb.DeepCopy()
		if ownerErr := controllerutil.SetControllerReference(env, foundRb, r.Scheme); ownerErr != nil {
			if strings.Contains(ownerErr.Error(), "cross-namespace owner references are disallowed") {
				logger.Info("Using labels for ownership due to cross-namespace constraint")
			} else {
				logger.V(3).Error(ownerErr, "Failed to verify/set owner reference on existing RoleBinding")
				return false
			}
		} else {
			if !reflect.DeepEqual(tempDesiredRbWithOwner.OwnerReferences, foundRb.OwnerReferences) {
				logger.Info("Updating owner reference on existing RoleBinding")
				foundRb.OwnerReferences = tempDesiredRbWithOwner.OwnerReferences
				updateNeeded = true
			}
		}
	}

	return updateNeeded
}

// checkAndUpdateLabels checks and updates labels if needed
func (r *EnvironmentReconciler) checkAndUpdateLabels(
	env *quixiov1.Environment,
	desiredRb *rbacv1.RoleBinding,
	foundRb *rbacv1.RoleBinding,
	logger logr.Logger,
) bool {
	updateNeeded := false

	if foundRb.Labels == nil {
		foundRb.Labels = make(map[string]string)
		updateNeeded = true
	}

	// Check and update Environment ownership labels
	envOwnershipLabels := map[string]string{
		LabelEnvironmentName: env.Name,
	}
	for k, v := range envOwnershipLabels {
		if foundRb.Labels[k] != v {
			foundRb.Labels[k] = v
			updateNeeded = true
		}
	}

	// Check other labels
	for k, v := range desiredRb.Labels {
		if !strings.HasPrefix(k, LabelEnvironmentPrefix) && foundRb.Labels[k] != v {
			logger.Info("RoleBinding Labels need update", "label", k, "desired", v, "found", foundRb.Labels[k])
			foundRb.Labels[k] = v
			updateNeeded = true
		}
	}

	return updateNeeded
}

// checkAndUpdateRoleRef checks and updates roleRef if needed
func (r *EnvironmentReconciler) checkAndUpdateRoleRef(
	desiredRb *rbacv1.RoleBinding,
	foundRb *rbacv1.RoleBinding,
	logger logr.Logger,
) bool {
	if !reflect.DeepEqual(desiredRb.RoleRef, foundRb.RoleRef) {
		logger.Info("RoleBinding RoleRef needs update", "desired", desiredRb.RoleRef, "found", foundRb.RoleRef)
		foundRb.RoleRef = desiredRb.RoleRef
		return true
	}
	return false
}

// checkAndUpdateSubjects checks and updates subjects if needed
func (r *EnvironmentReconciler) checkAndUpdateSubjects(
	desiredRb *rbacv1.RoleBinding,
	foundRb *rbacv1.RoleBinding,
	logger logr.Logger,
) bool {
	if !reflect.DeepEqual(desiredRb.Subjects, foundRb.Subjects) {
		logger.Info("RoleBinding Subjects need update", "desired", desiredRb.Subjects, "found", foundRb.Subjects)
		foundRb.Subjects = desiredRb.Subjects
		return true
	}
	return false
}

// reconcileRoleBindingAndStatus reconciles the RoleBinding and determines if status needs update
func (r *EnvironmentReconciler) reconcileRoleBindingAndStatus(ctx context.Context, env *quixiov1.Environment, namespaceName string) (ctrl.Result, bool, error) {
	rbResult, rbErr := r.reconcileRoleBinding(ctx, env, namespaceName)
	if rbErr != nil {
		return rbResult, false, rbErr
	}
	if !rbResult.IsZero() {
		return rbResult, false, nil
	}

	roleBindingReadyUpdateNeeded := env.Status.RoleBindingPhase != string(quixiov1.PhaseStateReady)
	return ctrl.Result{}, roleBindingReadyUpdateNeeded, nil
}

// RoleBinding management
// ------------------------------

// defineManagedRoleBinding defines the desired state for the RoleBinding.
func (r *EnvironmentReconciler) defineManagedRoleBinding(env *quixiov1.Environment, namespaceName string) *rbacv1.RoleBinding {
	rbName := r.Config.GetRoleBindingName()
	return &rbacv1.RoleBinding{
		ObjectMeta: metav1.ObjectMeta{
			Name:      rbName,
			Namespace: namespaceName,
			Labels: map[string]string{
				ManagedByLabel: OperatorName,
			},
		},
		Subjects: []rbacv1.Subject{
			{
				Kind:      rbacv1.ServiceAccountKind,
				Name:      r.Config.ServiceAccountName,
				Namespace: r.Config.ServiceAccountNamespace,
			},
		},
		RoleRef: rbacv1.RoleRef{
			APIGroup: rbacv1.GroupName,
			Kind:     "ClusterRole",
			Name:     r.Config.ClusterRoleName,
		},
	}
}

// updateFinalStatus updates the final status based on namespace and rolebinding phases
func (r *EnvironmentReconciler) updateFinalStatus(ctx context.Context, env *quixiov1.Environment, namespaceReadyUpdateNeeded, roleBindingReadyUpdateNeeded bool) (ctrl.Result, error) {
	logger := log.FromContext(ctx)

	// Determine if components are ready and if status update is needed
	isFullyReady := (env.Status.NamespacePhase == string(quixiov1.PhaseStateReady) || namespaceReadyUpdateNeeded) &&
		(env.Status.RoleBindingPhase == string(quixiov1.PhaseStateReady) || roleBindingReadyUpdateNeeded)

	// Determine target ready condition
	targetReadyCondition := r.determineReadyCondition(env, isFullyReady)

	// Check if status update is needed
	statusUpdateNeeded := r.isStatusUpdateNeeded(env, namespaceReadyUpdateNeeded, roleBindingReadyUpdateNeeded, targetReadyCondition, isFullyReady)

	if statusUpdateNeeded {
		logger.Info("Updating final status", "isReady", isFullyReady, "namespacePhase", env.Status.NamespacePhase, "roleBindingPhase", env.Status.RoleBindingPhase)
		return r.applyStatusUpdate(ctx, env, namespaceReadyUpdateNeeded, roleBindingReadyUpdateNeeded, isFullyReady, targetReadyCondition)
	}

	logger.Info("Reconciliation completed successfully", "phase", env.Status.Phase, "namespace", fmt.Sprintf("%s%s", env.Spec.Id, r.Config.NamespaceSuffix))
	return ctrl.Result{}, nil
}

// isStatusUpdateNeeded determines whether a status update is needed based on current state
func (r *EnvironmentReconciler) isStatusUpdateNeeded(
	env *quixiov1.Environment,
	namespaceReadyUpdateNeeded bool,
	roleBindingReadyUpdateNeeded bool,
	targetReadyCondition metav1.Condition,
	isFullyReady bool,
) bool {
	// Check if ready condition has changed
	currentReadyCondition := meta.FindStatusCondition(env.Status.Conditions, status.ConditionTypeReady)
	readyConditionChanged := currentReadyCondition == nil ||
		currentReadyCondition.Status != targetReadyCondition.Status ||
		currentReadyCondition.Reason != targetReadyCondition.Reason

	// Check if any component status has changed or if we need to clean error message
	return namespaceReadyUpdateNeeded ||
		roleBindingReadyUpdateNeeded ||
		readyConditionChanged ||
		(isFullyReady && (env.Status.Phase != quixiov1.PhaseReady || env.Status.ErrorMessage != ""))
}

// determineReadyCondition determines the appropriate ready condition based on environment state
func (r *EnvironmentReconciler) determineReadyCondition(env *quixiov1.Environment, isFullyReady bool) metav1.Condition {
	if isFullyReady {
		return metav1.Condition{
			Type:    status.ConditionTypeReady,
			Status:  metav1.ConditionTrue,
			Reason:  status.ReasonSucceeded,
			Message: MsgEnvProvisionSuccess,
		}
	}

	condition := metav1.Condition{
		Type:    status.ConditionTypeReady,
		Status:  metav1.ConditionFalse,
		Reason:  status.ReasonInProgress,
		Message: MsgDependenciesNotReady,
	}

	// Check for specific failure conditions
	if env.Status.NamespacePhase == string(quixiov1.PhaseStateFailed) || env.Status.RoleBindingPhase == string(quixiov1.PhaseStateFailed) {
		condition.Reason = status.ReasonFailed
		condition.Message = MsgDependenciesFailed
	} else if env.Status.NamespacePhase == string(quixiov1.PhaseStateTerminating) {
		condition.Reason = status.ReasonNamespaceTerminating
		condition.Message = MsgNamespaceBeingDeleted
	}

	return condition
}

// applyStatusUpdate applies the final status update based on component states
func (r *EnvironmentReconciler) applyStatusUpdate(
	ctx context.Context,
	env *quixiov1.Environment,
	namespaceReadyUpdateNeeded bool,
	roleBindingReadyUpdateNeeded bool,
	isFullyReady bool,
	targetReadyCondition metav1.Condition,
) (ctrl.Result, error) {
	logger := log.FromContext(ctx)

	finalStatusErr := r.StatusUpdater().UpdateStatus(ctx, env, func(st *quixiov1.EnvironmentStatus) {
		// Update namespace status if needed
		if namespaceReadyUpdateNeeded {
			st.NamespacePhase = string(quixiov1.PhaseStateReady)
			st.Namespace = fmt.Sprintf("%s%s", env.Spec.Id, r.Config.NamespaceSuffix)
		}

		// Update role binding status if needed
		if roleBindingReadyUpdateNeeded {
			st.RoleBindingPhase = string(quixiov1.PhaseStateReady)
		}

		// Update overall phase based on component states
		r.updateOverallPhase(st, isFullyReady)

		// Set ready condition
		meta.SetStatusCondition(&st.Conditions, targetReadyCondition)

		// Clean up obsolete conditions
		r.cleanupObsoleteConditions(st)
	})

	if finalStatusErr != nil {
		logger.V(3).Error(finalStatusErr, "Failed to update final status")
		return ctrl.Result{RequeueAfter: 1 * time.Second}, finalStatusErr
	}

	return ctrl.Result{Requeue: true}, nil
}

// updateOverallPhase updates the overall phase based on component states
func (r *EnvironmentReconciler) updateOverallPhase(st *quixiov1.EnvironmentStatus, isFullyReady bool) {
	if isFullyReady {
		st.Phase = quixiov1.PhaseReady
		st.ErrorMessage = ""
	} else if st.Phase == quixiov1.PhaseReady {
		// If we were ready but now we're not, determine the appropriate phase
		if st.NamespacePhase == string(quixiov1.PhaseStateCreating) || st.RoleBindingPhase == string(quixiov1.PhaseStateCreating) {
			st.Phase = quixiov1.PhaseCreating
		} else {
			st.Phase = quixiov1.PhaseUpdating
		}
	}
	// Otherwise keep existing phase
}

// cleanupObsoleteConditions removes obsolete conditions from status
func (r *EnvironmentReconciler) cleanupObsoleteConditions(st *quixiov1.EnvironmentStatus) {
	meta.RemoveStatusCondition(&st.Conditions, status.ConditionTypeNamespaceCreated)
	meta.RemoveStatusCondition(&st.Conditions, status.ConditionTypeRoleBindingCreated)
	meta.RemoveStatusCondition(&st.Conditions, status.ConditionTypeNamespaceDeleted)
}

// createNamespace creates a new namespace for the environment if it doesn't exist
func (r *EnvironmentReconciler) createNamespace(ctx context.Context, env *quixiov1.Environment, namespaceName string) error {
	logger := log.FromContext(ctx).WithValues("namespace", namespaceName)
	logger.Info("Attempting to create namespace")

	namespace := &corev1.Namespace{
		ObjectMeta: metav1.ObjectMeta{
			Name: namespaceName,
		},
	}

	r.NamespaceManager().ApplyMetadata(env, namespace)

	if err := r.Create(ctx, namespace); err != nil {
		if errors.IsAlreadyExists(err) {
			logger.Info("Namespace already exists")

			existingNs := &corev1.Namespace{}
			if getErr := r.Get(ctx, types.NamespacedName{Name: namespaceName}, existingNs); getErr != nil {
				logger.V(3).Error(getErr, "Failed to get existing namespace after AlreadyExists error")
				return getErr
			}

			if r.NamespaceManager().ApplyMetadata(env, existingNs) {
				logger.Info("Updating metadata for existing namespace")
				if updateErr := r.Update(ctx, existingNs); updateErr != nil {
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
	r.Recorder.Eventf(env, corev1.EventTypeNormal, EventReasonNamespaceCreated, "Created namespace %s", namespaceName)
	return nil
}

// Controller setup helpers
// ------------------------------

// configureController sets up the controller with watches and predicates
func (r *EnvironmentReconciler) configureController(
	mgr ctrl.Manager,
	environmentPredicate predicate.Predicate,
	namespacePredicate predicate.Predicate,
	namespaceHandler handler.EventHandler,
) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&quixiov1.Environment{}, builder.WithPredicates(environmentPredicate)).
		Owns(&corev1.Namespace{}).
		Owns(&rbacv1.RoleBinding{}).
		Watches(
			&corev1.Namespace{},
			namespaceHandler,
			builder.WithPredicates(namespacePredicate),
		).
		Complete(r)
}

// setupFieldIndexer creates an index on Environment.spec.id field for efficient lookups
func (r *EnvironmentReconciler) setupFieldIndexer(mgr ctrl.Manager, logger logr.Logger) error {
	err := mgr.GetFieldIndexer().IndexField(context.Background(), &quixiov1.Environment{}, ".spec.id", func(rawObj client.Object) []string {
		env, ok := rawObj.(*quixiov1.Environment)
		if !ok {
			return nil
		}
		if env.Spec.Id == "" {
			return nil
		}
		return []string{env.Spec.Id}
	})

	if err != nil {
		logger.V(3).Error(err, "Failed to set up index for .spec.id")
		return err
	}

	return nil
}

// createNamespacePredicate creates a predicate for filtering namespace events
func (r *EnvironmentReconciler) createNamespacePredicate() predicate.Predicate {
	return predicate.Funcs{
		CreateFunc: func(e event.CreateEvent) bool {
			ns, ok := e.Object.(*corev1.Namespace)
			if !ok {
				return false
			}
			_, managed := ns.Labels[ManagedByLabel]
			return managed
		},
		UpdateFunc: func(e event.UpdateEvent) bool {
			oldNs, oldOk := e.ObjectOld.(*corev1.Namespace)
			newNs, newOk := e.ObjectNew.(*corev1.Namespace)
			if !oldOk || !newOk {
				return false
			}
			labelsChanged := !reflect.DeepEqual(oldNs.Labels, newNs.Labels)
			deletionChanged := !oldNs.DeletionTimestamp.Equal(newNs.DeletionTimestamp)
			phaseChanged := oldNs.Status.Phase != newNs.Status.Phase

			_, oldManaged := oldNs.Labels[ManagedByLabel]
			_, newManaged := newNs.Labels[ManagedByLabel]
			return (oldManaged || newManaged) && (labelsChanged || deletionChanged || phaseChanged)
		},
		DeleteFunc: func(e event.DeleteEvent) bool {
			ns, ok := e.Object.(*corev1.Namespace)
			if !ok {
				return false
			}
			_, managed := ns.Labels[ManagedByLabel]
			return managed
		},
		GenericFunc: func(e event.GenericEvent) bool {
			return false
		},
	}
}

// createEnvironmentPredicate creates a predicate for filtering environment resource events
func (r *EnvironmentReconciler) createEnvironmentPredicate(logger logr.Logger) predicate.Predicate {
	return predicate.Funcs{
		CreateFunc: func(e event.CreateEvent) bool {
			return true // Always reconcile on creation
		},
		UpdateFunc: func(e event.UpdateEvent) bool {
			return r.shouldReconcileOnUpdate(e, logger)
		},
		DeleteFunc: func(e event.DeleteEvent) bool {
			return false // We use finalizers, so don't need to trigger on delete events
		},
		GenericFunc: func(e event.GenericEvent) bool {
			return false
		},
	}
}

// shouldReconcileOnUpdate determines if an update event should trigger reconciliation
func (r *EnvironmentReconciler) shouldReconcileOnUpdate(e event.UpdateEvent, logger logr.Logger) bool {
	oldEnv, oldOk := e.ObjectOld.(*quixiov1.Environment)
	newEnv, newOk := e.ObjectNew.(*quixiov1.Environment)

	if !oldOk || !newOk {
		return false
	}

	specChanged := !reflect.DeepEqual(oldEnv.Spec, newEnv.Spec)
	timestampChanged := !oldEnv.DeletionTimestamp.Equal(newEnv.DeletionTimestamp)
	generationChanged := oldEnv.Generation != newEnv.Generation

	if specChanged {
		logger.V(1).Info("Reconciling due to spec change", "environment", newEnv.Name)
	} else if timestampChanged {
		logger.V(1).Info("Reconciling due to deletion timestamp change", "environment", newEnv.Name)
	} else if generationChanged {
		logger.V(1).Info("Reconciling due to metadata.generation change", "environment", newEnv.Name)
	}

	return specChanged || timestampChanged || generationChanged
}

// createNamespaceHandler creates a handler to map namespace events to environment reconcile requests
func (r *EnvironmentReconciler) createNamespaceHandler(mgr ctrl.Manager) handler.EventHandler {
	return handler.EnqueueRequestsFromMapFunc(func(ctx context.Context, obj client.Object) []reconcile.Request {
		nsMapperLogger := ctrl.Log.WithName("namespaceMapper")
		namespace, ok := obj.(*corev1.Namespace)
		if !ok {
			nsMapperLogger.V(1).Info("Object is not a Namespace, ignoring map request")
			return nil
		}

		environmentId, ok := namespace.Labels[LabelEnvironmentID]
		if !ok {
			nsMapperLogger.V(1).Info("Namespace does not have environment-id label, ignoring", "namespace", namespace.Name)
			return nil
		}

		return r.findEnvironmentsForNamespace(ctx, mgr, namespace, environmentId, nsMapperLogger)
	})
}

// findEnvironmentsForNamespace finds environments associated with a namespace
func (r *EnvironmentReconciler) findEnvironmentsForNamespace(
	ctx context.Context,
	mgr ctrl.Manager,
	namespace *corev1.Namespace,
	environmentId string,
	logger logr.Logger,
) []reconcile.Request {
	environmentCRDNamespace := namespace.Annotations[AnnotationEnvironmentCRDNamespace]
	listOptions := []client.ListOption{}

	if environmentCRDNamespace != "" {
		listOptions = append(listOptions, client.InNamespace(environmentCRDNamespace))
	}

	listOptions = append(listOptions, client.MatchingFields{".spec.id": environmentId})

	var environments quixiov1.EnvironmentList
	if err := mgr.GetClient().List(ctx, &environments, listOptions...); err != nil {
		logger.V(3).Error(err, "Failed to list environments for namespace event", "environmentId", environmentId)
		return nil
	}

	var requests []reconcile.Request
	for _, env := range environments.Items {
		logger.Info("Queueing reconcile request for Environment due to namespace event",
			"environment", env.Name,
			"namespace", env.Namespace,
			"triggeringNamespace", namespace.Name)
		requests = append(requests, reconcile.Request{
			NamespacedName: types.NamespacedName{
				Name:      env.Name,
				Namespace: env.Namespace,
			},
		})
	}

	if len(requests) == 0 {
		logger.V(1).Info("No matching Environment found for namespace event", "environmentId", environmentId, "namespace", namespace.Name)
	}

	return requests
}

// handleStatusUpdateError logs an error and returns appropriate Result for status update failures
func (r *EnvironmentReconciler) handleStatusUpdateError(ctx context.Context, err error, msg string) (ctrl.Result, error) {
	logger := log.FromContext(ctx)
	logger.V(3).Error(err, msg)
	return ctrl.Result{RequeueAfter: 1 * time.Second}, err
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

// Finalizer management
// ------------------------------

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

// Status management
// ------------------------------

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

// initializeStatus initializes the status fields if needed (first time reconcile)
func (r *EnvironmentReconciler) initializeStatus(ctx context.Context, env *quixiov1.Environment) (ctrl.Result, error) {
	logger := log.FromContext(ctx)

	// Check if any status fields need initialization
	needsInitialization := env.Status.Phase == "" ||
		env.Status.NamespacePhase == "" ||
		env.Status.RoleBindingPhase == ""

	if !needsInitialization {
		return ctrl.Result{}, nil
	}

	logger.Info("Setting initial status fields")
	return r.setInitialStatusValues(ctx, env)
}

// setInitialStatusValues sets default initial values for status fields
func (r *EnvironmentReconciler) setInitialStatusValues(ctx context.Context, env *quixiov1.Environment) (ctrl.Result, error) {
	err := r.StatusUpdater().UpdateStatus(ctx, env, func(st *quixiov1.EnvironmentStatus) {
		if st.Phase == "" {
			st.Phase = quixiov1.PhaseCreating
		}
		if st.NamespacePhase == "" {
			st.NamespacePhase = string(quixiov1.PhaseStatePending)
		}
		if st.RoleBindingPhase == "" {
			st.RoleBindingPhase = string(quixiov1.PhaseStatePending)
		}
	})

	if err != nil {
		return r.handleStatusUpdateError(ctx, err, "Failed to update initial status")
	}

	return ctrl.Result{Requeue: true}, nil
}

// checkNamespaceStateForDeletion determines the state of the namespace during environment deletion
func (r *EnvironmentReconciler) checkNamespaceStateForDeletion(ctx context.Context, env *quixiov1.Environment, namespaceName string) (quixiov1.SubResourcePhase, error) {
	namespace, nsGetErr := r.getNamespace(ctx, namespaceName)

	isDeleted := r.namespaceManager.IsNamespaceDeleted(namespace, nsGetErr)

	if isDeleted {
		return quixiov1.PhaseStateDeleted, nil
	} else if nsGetErr != nil {
		_ = r.StatusUpdater().UpdateStatus(ctx, env, func(st *quixiov1.EnvironmentStatus) {
			st.NamespacePhase = string(quixiov1.PhaseStateFailed)
			st.ErrorMessage = fmt.Sprintf("Failed to get namespace %s during deletion: %v", namespaceName, nsGetErr)
		})
		return quixiov1.PhaseStateFailed, nsGetErr
	} else {
		if r.namespaceManager.IsNamespaceManaged(namespace) {
			return quixiov1.PhaseStateReady, nil
		} else {
			return quixiov1.PhaseStateUnmanaged, nil
		}
	}
}

// handleNamespaceDeletionByState processes namespace deletion based on its state
func (r *EnvironmentReconciler) handleNamespaceDeletionByState(ctx context.Context, env *quixiov1.Environment, namespaceName string, nsState quixiov1.SubResourcePhase) (ctrl.Result, error) {
	logger := log.FromContext(ctx).WithValues("namespaceState", nsState)

	switch nsState {
	case quixiov1.PhaseStateDeleted, quixiov1.PhaseStateUnmanaged:
		return r.removeFinalizer(ctx, env)

	case quixiov1.PhaseStateTerminating:
		logger.Info("Managed namespace is terminating. Waiting...")
		_ = r.StatusUpdater().UpdateStatus(ctx, env, func(st *quixiov1.EnvironmentStatus) {
			st.NamespacePhase = string(quixiov1.PhaseStateTerminating)
		})
		return ctrl.Result{RequeueAfter: 2 * time.Second}, nil

	case quixiov1.PhaseStateReady:
		return r.deleteNamespace(ctx, env, namespaceName)

	default:
		// For PhaseStateFailed or any other unexpected state
		return ctrl.Result{RequeueAfter: 2 * time.Second}, nil
	}
}

// deleteNamespace attempts to delete the managed namespace
func (r *EnvironmentReconciler) deleteNamespace(ctx context.Context, env *quixiov1.Environment, namespaceName string) (ctrl.Result, error) {
	logger := log.FromContext(ctx)
	logger.Info("Attempting to delete managed namespace")

	// Update status to terminating first
	statusUpdateErr := r.StatusUpdater().UpdateStatus(ctx, env, func(st *quixiov1.EnvironmentStatus) {
		st.NamespacePhase = string(quixiov1.PhaseStateTerminating)
	})
	if statusUpdateErr != nil {
		logger.V(3).Error(statusUpdateErr, "Failed to update NamespacePhase to Terminating before deletion")
		return r.handleStatusUpdateError(ctx, statusUpdateErr, "Failed to update NamespacePhase to Terminating before deletion")
	}

	// Get the namespace first
	namespace, err := r.getNamespace(ctx, namespaceName)
	if err != nil {
		if errors.IsNotFound(err) {
			logger.Info("Namespace not found during deletion, considering already deleted")
			return r.removeFinalizer(ctx, env)
		}
		logger.V(3).Error(err, "Failed to get namespace before deletion")
		return ctrl.Result{RequeueAfter: 2 * time.Second}, err
	}

	// Try to delete the namespace
	if err := r.Delete(ctx, namespace); err != nil {
		if errors.IsNotFound(err) {
			logger.Info("Managed namespace was already deleted concurrently")
			return r.removeFinalizer(ctx, env)
		} else {
			logger.V(3).Error(err, "Failed to delete managed namespace")
			r.Recorder.Eventf(env, corev1.EventTypeWarning, EventReasonNamespaceDeleteFailed,
				"Failed to delete namespace %s: %v", namespaceName, err)
			return ctrl.Result{RequeueAfter: 5 * time.Second}, err
		}
	}

	logger.Info("Managed namespace deletion initiated")
	r.Recorder.Eventf(env, corev1.EventTypeNormal, EventReasonNamespaceDeletionInitiated,
		"Deletion initiated for namespace %s", namespaceName)
	return ctrl.Result{RequeueAfter: 2 * time.Second}, nil
}

// updateNamespaceStatusForDeletion updates namespace status during deletion process
func (r *EnvironmentReconciler) updateNamespaceStatusForDeletion(ctx context.Context, env *quixiov1.Environment, nsState quixiov1.SubResourcePhase, namespaceName string) {
	logger := log.FromContext(ctx)

	// First check if the Environment still exists
	updatedEnv := &quixiov1.Environment{}
	err := r.Get(ctx, types.NamespacedName{Name: env.Name, Namespace: env.Namespace}, updatedEnv)
	if err != nil {
		if errors.IsNotFound(err) {
			logger.Info("Environment resource no longer exists, skipping status update")
			return
		}
		logger.V(3).Error(err, "Failed to get Environment before status update")
		return
	}

	_ = r.StatusUpdater().UpdateStatus(ctx, updatedEnv, func(st *quixiov1.EnvironmentStatus) {
		st.NamespacePhase = string(nsState)
		meta.RemoveStatusCondition(&st.Conditions, status.ConditionTypeReady)
	})

	if nsState == quixiov1.PhaseStateDeleted &&
		updatedEnv.Status.NamespacePhase == string(quixiov1.PhaseStateTerminating) {
		logger.Info("Recording namespace deletion event")
		r.Recorder.Eventf(updatedEnv, corev1.EventTypeNormal, EventReasonNamespaceDeleted,
			"Namespace %s was deleted", namespaceName)
	}
}

// removeFinalizer removes the finalizer from the Environment resource
func (r *EnvironmentReconciler) removeFinalizer(ctx context.Context, env *quixiov1.Environment) (ctrl.Result, error) {
	logger := log.FromContext(ctx)
	logger.Info("Proceeding to remove finalizer")

	latestEnv := &quixiov1.Environment{}
	if err := r.Get(ctx, types.NamespacedName{Name: env.Name, Namespace: env.Namespace}, latestEnv); err != nil {
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
