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

func (r *EnvironmentReconciler) NamespaceManager() namespaces.NamespaceManager {
	return r.namespaceManager
}

func (r *EnvironmentReconciler) StatusUpdater() status.StatusUpdater {
	return r.statusUpdater
}

// +kubebuilder:rbac:groups=quix.io,resources=environments,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=quix.io,resources=environments/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=quix.io,resources=environments/finalizers,verbs=update
// +kubebuilder:rbac:groups=core,resources=namespaces,verbs=get;list;watch;create;delete;update;patch
// +kubebuilder:rbac:groups=rbac.authorization.k8s.io,resources=rolebindings,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=core,resources=events,verbs=create;patch

func (r *EnvironmentReconciler) getNamespace(ctx context.Context, name string) (*corev1.Namespace, error) {
	logger := log.FromContext(ctx)
	namespace := &corev1.Namespace{}
	err := r.Get(ctx, types.NamespacedName{Name: name}, namespace)

	if err != nil {
		if errors.IsNotFound(err) {
			logger.V(1).Info("Namespace not found", "namespaceName", name)
			return nil, err
		}
		logger.Error(err, "Failed to get namespace", "namespaceName", name)
		return nil, err
	}

	logger.V(2).Info("Found namespace", "namespaceName", name, "uid", namespace.UID)
	return namespace, nil
}

// Reconcile handles the reconciliation logic for Environment resources
func (r *EnvironmentReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	logger := log.FromContext(ctx)
	logger.Info("Reconciling Environment", "request", req.NamespacedName)

	env := &quixiov1.Environment{}
	if err := r.Get(ctx, req.NamespacedName, env); err != nil {
		if errors.IsNotFound(err) {
			logger.Info("Environment resource not found, ignoring")
			return ctrl.Result{}, nil
		}
		logger.Error(err, "Failed to get Environment")
		return ctrl.Result{RequeueAfter: 2 * time.Second}, nil
	}

	logger.V(1).Info("Retrieved Environment",
		"phase", env.Status.Phase,
		"hasFinalizer", controllerutil.ContainsFinalizer(env, EnvironmentFinalizer),
		"isBeingDeleted", !env.DeletionTimestamp.IsZero())

	// Handle finalizer addition
	if !controllerutil.ContainsFinalizer(env, EnvironmentFinalizer) {
		logger.Info("Adding finalizer")
		controllerutil.AddFinalizer(env, EnvironmentFinalizer)
		if err := r.Update(ctx, env); err != nil {
			if errors.IsConflict(err) {
				logger.Info("Conflict adding finalizer, requeuing")
				return ctrl.Result{Requeue: true}, nil
			}
			logger.Error(err, "Failed to add finalizer")
			return ctrl.Result{RequeueAfter: 1 * time.Second}, err
		}
		return ctrl.Result{Requeue: true}, nil
	}

	// Handle deletion
	if !env.DeletionTimestamp.IsZero() {
		if env.Status.Phase != quixiov1.PhaseDeleting {
			statusErr := r.StatusUpdater().UpdateStatus(ctx, env, func(st *quixiov1.EnvironmentStatus) {
				st.Phase = quixiov1.PhaseDeleting
			})
			if statusErr != nil {
				logger.Error(statusErr, "Failed to update phase to Deleting")
				return ctrl.Result{RequeueAfter: 1 * time.Second}, statusErr
			}
			return ctrl.Result{Requeue: true}, nil
		}
		return r.handleDeletion(ctx, env)
	}

	// --- Begin Active Reconciliation --- //

	// Set initial status if needed (first time reconcile)
	initialStatusUpdateNeeded := false
	if env.Status.Phase == "" {
		initialStatusUpdateNeeded = true
		env.Status.Phase = quixiov1.PhaseCreating
	}
	if env.Status.NamespacePhase == "" {
		initialStatusUpdateNeeded = true
		env.Status.NamespacePhase = string(quixiov1.PhaseStatePending)
	}
	if env.Status.RoleBindingPhase == "" {
		initialStatusUpdateNeeded = true
		env.Status.RoleBindingPhase = string(quixiov1.PhaseStatePending)
	}

	if initialStatusUpdateNeeded {
		logger.Info("Setting initial status fields")
		if err := r.StatusUpdater().UpdateStatus(ctx, env, func(st *quixiov1.EnvironmentStatus) {
			if st.Phase == "" {
				st.Phase = quixiov1.PhaseCreating
			}
			if st.NamespacePhase == "" {
				st.NamespacePhase = string(quixiov1.PhaseStatePending)
			}
			if st.RoleBindingPhase == "" {
				st.RoleBindingPhase = string(quixiov1.PhaseStatePending)
			}
		}); err != nil {
			logger.Error(err, "Failed to update initial status")
			return ctrl.Result{RequeueAfter: 1 * time.Second}, err
		}
		return ctrl.Result{Requeue: true}, nil
	}

	// Validate the environment spec
	if validationErr := r.validateEnvironment(ctx, env); validationErr != nil {
		r.Recorder.Eventf(env, corev1.EventTypeWarning, EventReasonValidationFailed, "Environment validation failed: %v", validationErr)
		_ = r.StatusUpdater().SetErrorStatus(ctx, env, quixiov1.PhaseCreateFailed,
			validationErr, "Environment validation failed")
		return ctrl.Result{}, nil
	}

	// --- Namespace Management --- //
	namespaceName := fmt.Sprintf("%s%s", env.Spec.Id, r.Config.NamespaceSuffix)
	namespace, nsGetErr := r.getNamespace(ctx, namespaceName)

	if nsGetErr != nil {
		if errors.IsNotFound(nsGetErr) {
			// --- Namespace Not Found: Create it --- //
			if env.Status.NamespacePhase != string(quixiov1.PhaseStateCreating) {
				statusUpdateErr := r.StatusUpdater().UpdateStatus(ctx, env, func(st *quixiov1.EnvironmentStatus) {
					st.NamespacePhase = string(quixiov1.PhaseStateCreating)
					st.Phase = quixiov1.PhaseCreating
				})
				if statusUpdateErr != nil {
					logger.Error(statusUpdateErr, "Failed to update NamespacePhase to Creating")
					return ctrl.Result{RequeueAfter: 1 * time.Second}, statusUpdateErr
				}
				return ctrl.Result{Requeue: true}, nil
			}

			createErr := r.createNamespace(ctx, env, namespaceName)
			if createErr != nil {
				logger.Error(createErr, "Failed to create namespace")
				r.Recorder.Eventf(env, corev1.EventTypeWarning, EventReasonNamespaceCreationFailed, "Failed to create namespace %s: %v", namespaceName, createErr)

				_ = r.StatusUpdater().SetErrorStatus(ctx, env, quixiov1.PhaseCreateFailed,
					createErr, fmt.Sprintf("Failed to create namespace %s", namespaceName))
				return ctrl.Result{RequeueAfter: 5 * time.Second}, createErr
			}

			logger.Info("Namespace creation request submitted, requeuing for confirmation", "namespace", namespaceName)
			return ctrl.Result{RequeueAfter: 500 * time.Millisecond}, nil

		} else {
			// Error other than NotFound when getting namespace
			logger.Error(nsGetErr, "Failed to get namespace")
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
			return ctrl.Result{RequeueAfter: 5 * time.Second}, nsGetErr
		}
	}

	// --- Namespace Found --- //

	// Check if namespace is managed by us
	if !r.NamespaceManager().IsNamespaceManaged(namespace) {
		collisionErr := fmt.Errorf("namespace '%s' already exists and is not managed by this operator", namespaceName)
		r.Recorder.Eventf(env, corev1.EventTypeWarning, EventReasonCollisionDetected, "Namespace %s already exists and is not managed by this operator", namespaceName)

		phase := env.Status.Phase
		if phase == "" || phase == quixiov1.PhaseCreating {
			phase = quixiov1.PhaseCreateFailed
		} else {
			phase = quixiov1.PhaseUpdateFailed
		}

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
			logger.Error(statusUpdateErr, "Failed to update status for collision")
			return ctrl.Result{RequeueAfter: 1 * time.Second}, statusUpdateErr
		}

		return ctrl.Result{}, nil
	}

	// Check if the namespace is terminating
	if r.NamespaceManager().IsNamespaceDeleted(namespace, nsGetErr) {
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
			logger.Error(statusUpdateErr, "Failed to update status for terminating namespace")
			return ctrl.Result{RequeueAfter: 1 * time.Second}, statusUpdateErr
		}
		return ctrl.Result{RequeueAfter: 5 * time.Second}, nil
	}

	// --- Namespace Exists, is Managed, and Not Terminating --- //

	// Ensure namespace metadata (labels, annotations) is up-to-date
	if r.NamespaceManager().ApplyMetadata(env, namespace) {
		if updateErr := r.Update(ctx, namespace); updateErr != nil {
			r.Recorder.Eventf(env, corev1.EventTypeWarning, EventReasonUpdateFailed,
				"Failed to update namespace metadata: %v", updateErr)

			_ = r.StatusUpdater().SetErrorStatus(ctx, env, quixiov1.PhaseUpdateFailed,
				updateErr, "Failed to update namespace metadata")

			return ctrl.Result{RequeueAfter: 5 * time.Second}, updateErr
		}
		logger.Info("Updated namespace metadata", "namespace", namespaceName)
	}

	// Update NamespacePhase to Ready if it wasn't already
	namespaceReadyUpdateNeeded := false
	if env.Status.NamespacePhase != string(quixiov1.PhaseStateReady) {
		namespaceReadyUpdateNeeded = true
	}

	// --- RoleBinding Management --- //
	rbResult, rbErr := r.reconcileRoleBinding(ctx, env, namespaceName)
	if rbErr != nil {
		return rbResult, rbErr
	}
	if !rbResult.IsZero() {
		return rbResult, nil
	}

	roleBindingReadyUpdateNeeded := false
	if env.Status.RoleBindingPhase != string(quixiov1.PhaseStateReady) {
		roleBindingReadyUpdateNeeded = true
	}

	// --- Final Status Update --- //
	isNamespaceReady := env.Status.NamespacePhase == string(quixiov1.PhaseStateReady) || namespaceReadyUpdateNeeded
	isRoleBindingReady := env.Status.RoleBindingPhase == string(quixiov1.PhaseStateReady) || roleBindingReadyUpdateNeeded
	isFullyReady := isNamespaceReady && isRoleBindingReady

	var targetReadyCondition metav1.Condition
	if isFullyReady {
		targetReadyCondition = metav1.Condition{
			Type:    status.ConditionTypeReady,
			Status:  metav1.ConditionTrue,
			Reason:  status.ReasonSucceeded,
			Message: MsgEnvProvisionSuccess,
		}
	} else {
		targetReadyCondition = metav1.Condition{
			Type:    status.ConditionTypeReady,
			Status:  metav1.ConditionFalse,
			Reason:  status.ReasonInProgress,
			Message: MsgDependenciesNotReady,
		}

		if env.Status.NamespacePhase == string(quixiov1.PhaseStateFailed) || env.Status.RoleBindingPhase == string(quixiov1.PhaseStateFailed) {
			targetReadyCondition.Reason = status.ReasonFailed
			targetReadyCondition.Message = MsgDependenciesFailed
		} else if env.Status.NamespacePhase == string(quixiov1.PhaseStateTerminating) {
			targetReadyCondition.Reason = status.ReasonNamespaceTerminating
			targetReadyCondition.Message = MsgNamespaceBeingDeleted
		}
	}

	currentReadyCondition := meta.FindStatusCondition(env.Status.Conditions, status.ConditionTypeReady)
	readyConditionChanged := currentReadyCondition == nil ||
		currentReadyCondition.Status != targetReadyCondition.Status ||
		currentReadyCondition.Reason != targetReadyCondition.Reason

	statusUpdateNeeded := initialStatusUpdateNeeded || namespaceReadyUpdateNeeded || roleBindingReadyUpdateNeeded || readyConditionChanged ||
		(isFullyReady && (env.Status.Phase != quixiov1.PhaseReady || env.Status.ErrorMessage != ""))

	if statusUpdateNeeded {
		logger.Info("Updating final status", "isReady", isFullyReady, "namespacePhase", env.Status.NamespacePhase, "roleBindingPhase", env.Status.RoleBindingPhase)
		finalStatusErr := r.StatusUpdater().UpdateStatus(ctx, env, func(st *quixiov1.EnvironmentStatus) {
			if namespaceReadyUpdateNeeded {
				st.NamespacePhase = string(quixiov1.PhaseStateReady)
				st.Namespace = namespaceName
			}
			if roleBindingReadyUpdateNeeded {
				st.RoleBindingPhase = string(quixiov1.PhaseStateReady)
			}

			if isFullyReady {
				st.Phase = quixiov1.PhaseReady
				st.ErrorMessage = ""
			} else {
				if st.Phase == quixiov1.PhaseReady {
					if st.NamespacePhase == string(quixiov1.PhaseStateCreating) || st.RoleBindingPhase == string(quixiov1.PhaseStateCreating) {
						st.Phase = quixiov1.PhaseCreating
					} else {
						st.Phase = quixiov1.PhaseUpdating
					}
				}
			}

			meta.SetStatusCondition(&st.Conditions, targetReadyCondition)

			// Clean up obsolete conditions
			meta.RemoveStatusCondition(&st.Conditions, status.ConditionTypeNamespaceCreated)
			meta.RemoveStatusCondition(&st.Conditions, status.ConditionTypeRoleBindingCreated)
			meta.RemoveStatusCondition(&st.Conditions, status.ConditionTypeNamespaceDeleted)
		})

		if finalStatusErr != nil {
			logger.Error(finalStatusErr, "Failed to update final status")
			return ctrl.Result{RequeueAfter: 1 * time.Second}, finalStatusErr
		}
		return ctrl.Result{Requeue: true}, nil
	}

	logger.Info("Reconciliation completed successfully", "phase", env.Status.Phase, "namespace", namespaceName)
	return ctrl.Result{}, nil
}

// validateEnvironment validates the Environment spec
func (r *EnvironmentReconciler) validateEnvironment(ctx context.Context, env *quixiov1.Environment) error {
	if env.Spec.Id == "" {
		return fmt.Errorf("spec.id is required")
	}

	namespaceName := fmt.Sprintf("%s%s", env.Spec.Id, r.Config.NamespaceSuffix)

	if len(namespaceName) > 63 {
		return fmt.Errorf("generated namespace name '%s' (from spec.id '%s' and suffix '%s') exceeds the 63 character limit",
			namespaceName, env.Spec.Id, r.Config.NamespaceSuffix)
	}

	return nil
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
				logger.Error(getErr, "Failed to get existing namespace after AlreadyExists error")
				return getErr
			}

			if r.NamespaceManager().ApplyMetadata(env, existingNs) {
				logger.Info("Updating metadata for existing namespace")
				if updateErr := r.Update(ctx, existingNs); updateErr != nil {
					logger.Error(updateErr, "Failed to update existing namespace metadata")
					return updateErr
				}
			}
			return nil
		}
		logger.Error(err, "Failed to create namespace")
		return err
	}

	logger.Info("Namespace created successfully")
	r.Recorder.Eventf(env, corev1.EventTypeNormal, EventReasonNamespaceCreated, "Created namespace %s", namespaceName)
	return nil
}

// reconcileRoleBinding ensures the managed RoleBinding exists and is correctly configured.
func (r *EnvironmentReconciler) reconcileRoleBinding(ctx context.Context, env *quixiov1.Environment, namespaceName string) (ctrl.Result, error) {
	logger := log.FromContext(ctx).WithValues("namespace", namespaceName)
	rbName := r.Config.GetRoleBindingName()
	logger = logger.WithValues("roleBinding", rbName)

	desiredRb := r.defineManagedRoleBinding(env, namespaceName)

	foundRb := &rbacv1.RoleBinding{}
	err := r.Get(ctx, types.NamespacedName{Name: rbName, Namespace: namespaceName}, foundRb)

	if err != nil && errors.IsNotFound(err) {
		logger.Info("RoleBinding not found, attempting creation")
		if env.Status.RoleBindingPhase != string(quixiov1.PhaseStateCreating) {
			statusUpdateErr := r.statusUpdater.UpdateStatus(ctx, env, func(st *quixiov1.EnvironmentStatus) {
				st.RoleBindingPhase = string(quixiov1.PhaseStateCreating)
				if st.Phase == quixiov1.PhaseReady {
					st.Phase = quixiov1.PhaseCreating
				}
			})
			if statusUpdateErr != nil {
				logger.Error(statusUpdateErr, "Failed to update RoleBindingPhase to Creating")
				return ctrl.Result{RequeueAfter: 1 * time.Second}, statusUpdateErr
			}
			return ctrl.Result{Requeue: true}, nil
		}

		logger.Info("Creating managed RoleBinding")
		if createErr := r.Create(ctx, desiredRb); createErr != nil {
			logger.Error(createErr, "Failed to create managed RoleBinding")
			_ = r.statusUpdater.SetErrorStatus(ctx, env, quixiov1.PhaseCreateFailed,
				createErr, fmt.Sprintf("Failed to create RoleBinding %s", rbName))
			return ctrl.Result{RequeueAfter: 5 * time.Second}, createErr
		}

		logger.Info("Managed RoleBinding creation submitted")
		r.Recorder.Eventf(env, corev1.EventTypeNormal, EventReasonRoleBindingCreated, "Created RoleBinding %s", rbName)
		return ctrl.Result{RequeueAfter: 500 * time.Millisecond}, nil

	} else if err != nil {
		logger.Error(err, "Failed to get managed RoleBinding")
		_ = r.statusUpdater.SetErrorStatus(ctx, env, quixiov1.PhaseUpdateFailed,
			err, fmt.Sprintf("Failed to get RoleBinding %s", rbName))
		return ctrl.Result{RequeueAfter: 5 * time.Second}, err
	}

	// Check if RoleBinding needs update
	updateNeeded := false

	if len(desiredRb.OwnerReferences) > 0 {
		tempDesiredRbWithOwner := desiredRb.DeepCopy()
		if ownerErr := controllerutil.SetControllerReference(env, foundRb, r.Scheme); ownerErr != nil {
			if strings.Contains(ownerErr.Error(), "cross-namespace owner references are disallowed") {
				logger.Info("Using labels for ownership due to cross-namespace constraint")
			} else {
				logger.Error(ownerErr, "Failed to verify/set owner reference on existing RoleBinding")
				_ = r.statusUpdater.SetErrorStatus(ctx, env, quixiov1.PhaseUpdateFailed,
					ownerErr, "Failed to set owner reference for existing RoleBinding")
				return ctrl.Result{RequeueAfter: 2 * time.Second}, ownerErr
			}
		} else {
			if !reflect.DeepEqual(tempDesiredRbWithOwner.OwnerReferences, foundRb.OwnerReferences) {
				logger.Info("Updating owner reference on existing RoleBinding")
				foundRb.OwnerReferences = tempDesiredRbWithOwner.OwnerReferences
				updateNeeded = true
			}
		}
	}

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

	if !reflect.DeepEqual(desiredRb.RoleRef, foundRb.RoleRef) {
		logger.Info("RoleBinding RoleRef needs update", "desired", desiredRb.RoleRef, "found", foundRb.RoleRef)
		foundRb.RoleRef = desiredRb.RoleRef
		updateNeeded = true
	}

	if !reflect.DeepEqual(desiredRb.Subjects, foundRb.Subjects) {
		logger.Info("RoleBinding Subjects need update", "desired", desiredRb.Subjects, "found", foundRb.Subjects)
		foundRb.Subjects = desiredRb.Subjects
		updateNeeded = true
	}

	for k, v := range desiredRb.Labels {
		if !strings.HasPrefix(k, LabelEnvironmentPrefix) && foundRb.Labels[k] != v {
			logger.Info("RoleBinding Labels need update", "label", k, "desired", v, "found", foundRb.Labels[k])
			foundRb.Labels[k] = v
			updateNeeded = true
		}
	}

	if updateNeeded {
		logger.Info("Updating managed RoleBinding")
		if updateErr := r.Update(ctx, foundRb); updateErr != nil {
			if errors.IsConflict(updateErr) {
				logger.Info("Conflict updating RoleBinding, requeuing")
				return ctrl.Result{Requeue: true}, nil
			}
			logger.Error(updateErr, "Failed to update managed RoleBinding")
			_ = r.statusUpdater.SetErrorStatus(ctx, env, quixiov1.PhaseUpdateFailed,
				updateErr, fmt.Sprintf("Failed to update RoleBinding %s", rbName))
			return ctrl.Result{RequeueAfter: 5 * time.Second}, updateErr
		}
		logger.Info("Managed RoleBinding updated successfully")
		r.Recorder.Eventf(env, corev1.EventTypeNormal, EventReasonRoleBindingUpdated, "Updated RoleBinding %s", rbName)
		return ctrl.Result{Requeue: true}, nil
	}

	logger.V(1).Info("Managed RoleBinding is up-to-date")
	return ctrl.Result{}, nil
}

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

// handleDeletion handles the deletion of an Environment resource
func (r *EnvironmentReconciler) handleDeletion(ctx context.Context, env *quixiov1.Environment) (ctrl.Result, error) {
	logger := log.FromContext(ctx).WithValues("environment", env.Name)
	logger.Info("Handling environment deletion")

	namespaceName := fmt.Sprintf("%s%s", env.Spec.Id, r.Config.NamespaceSuffix)

	namespace, nsGetErr := r.getNamespace(ctx, namespaceName)

	isDeleted := r.namespaceManager.IsNamespaceDeleted(namespace, nsGetErr)
	var nsState quixiov1.SubResourcePhase

	if isDeleted {
		nsState = quixiov1.PhaseStateDeleted
	} else if nsGetErr != nil {
		logger.Error(nsGetErr, "Failed to get namespace during deletion cleanup")
		nsState = quixiov1.PhaseStateFailed
		_ = r.StatusUpdater().UpdateStatus(ctx, env, func(st *quixiov1.EnvironmentStatus) {
			st.NamespacePhase = string(quixiov1.PhaseStateFailed)
			st.ErrorMessage = fmt.Sprintf("Failed to get namespace %s during deletion: %v", namespaceName, nsGetErr)
		})
		return ctrl.Result{RequeueAfter: 5 * time.Second}, nsGetErr

	} else {
		if r.namespaceManager.IsNamespaceManaged(namespace) {
			nsState = quixiov1.PhaseStateReady
		} else {
			nsState = quixiov1.PhaseStateUnmanaged
		}
	}

	logger.Info("Namespace state during deletion", "state", nsState)

	shouldRemoveFinalizer := false
	var deletionErr error
	requeueDuration := 2 * time.Second

	switch nsState {
	case quixiov1.PhaseStateDeleted:
		logger.Info("Managed namespace is confirmed deleted or never existed.")
		shouldRemoveFinalizer = true
	case quixiov1.PhaseStateUnmanaged:
		logger.Info("Namespace exists but is not managed by this operator. Skipping deletion.")
		shouldRemoveFinalizer = true
	case quixiov1.PhaseStateTerminating:
		logger.Info("Managed namespace is terminating. Waiting...")
		_ = r.StatusUpdater().UpdateStatus(ctx, env, func(st *quixiov1.EnvironmentStatus) {
			st.NamespacePhase = string(quixiov1.PhaseStateTerminating)
		})
	case quixiov1.PhaseStateReady:
		logger.Info("Attempting to delete managed namespace")
		statusUpdateErr := r.StatusUpdater().UpdateStatus(ctx, env, func(st *quixiov1.EnvironmentStatus) {
			st.NamespacePhase = string(quixiov1.PhaseStateTerminating)
		})
		if statusUpdateErr != nil {
			logger.Error(statusUpdateErr, "Failed to update NamespacePhase to Terminating before deletion")
			deletionErr = statusUpdateErr
			requeueDuration = 1 * time.Second
			break
		}

		if err := r.Delete(ctx, namespace); err != nil {
			if errors.IsNotFound(err) {
				logger.Info("Managed namespace was already deleted concurrently.")
				shouldRemoveFinalizer = true
			} else {
				logger.Error(err, "Failed to delete managed namespace")
				r.Recorder.Eventf(env, corev1.EventTypeWarning, EventReasonNamespaceDeleteFailed, "Failed to delete namespace %s: %v", namespaceName, err)
				deletionErr = err
				requeueDuration = 5 * time.Second
			}
		} else {
			logger.Info("Managed namespace deletion initiated.")
			r.Recorder.Eventf(env, corev1.EventTypeNormal, EventReasonNamespaceDeletionInitiated, "Deletion initiated for namespace %s", namespaceName)
		}
	}

	if (nsState == quixiov1.PhaseStateDeleted || nsState == quixiov1.PhaseStateUnmanaged) && env.Status.NamespacePhase != string(nsState) {
		_ = r.StatusUpdater().UpdateStatus(ctx, env, func(st *quixiov1.EnvironmentStatus) {
			st.NamespacePhase = string(nsState)
			meta.RemoveStatusCondition(&st.Conditions, status.ConditionTypeReady)
		})

		if nsState == quixiov1.PhaseStateDeleted && env.Status.NamespacePhase == string(quixiov1.PhaseStateTerminating) {
			r.Recorder.Eventf(env, corev1.EventTypeNormal, EventReasonNamespaceDeleted, "Namespace %s was deleted", namespaceName)
		}
	}

	if shouldRemoveFinalizer {
		logger.Info("Proceeding to remove finalizer")
		latestEnv := &quixiov1.Environment{}
		if err := r.Get(ctx, types.NamespacedName{Name: env.Name, Namespace: env.Namespace}, latestEnv); err != nil {
			if errors.IsNotFound(err) {
				logger.Info("Environment resource already deleted while attempting finalizer removal")
				return ctrl.Result{}, nil
			}
			logger.Error(err, "Failed to refetch Environment before finalizer removal")
			return ctrl.Result{RequeueAfter: 2 * time.Second}, err
		}

		if controllerutil.ContainsFinalizer(latestEnv, EnvironmentFinalizer) {
			controllerutil.RemoveFinalizer(latestEnv, EnvironmentFinalizer)
			if err := r.Update(ctx, latestEnv); err != nil {
				if errors.IsConflict(err) {
					logger.Info("Conflict removing finalizer, requeuing")
					return ctrl.Result{Requeue: true}, nil
				}
				logger.Error(err, "Failed to remove finalizer")
				return ctrl.Result{RequeueAfter: 2 * time.Second}, err
			}
			logger.Info("Finalizer removed successfully")
		}
		return ctrl.Result{}, nil
	}

	if deletionErr != nil {
		return ctrl.Result{RequeueAfter: requeueDuration}, deletionErr
	}
	return ctrl.Result{RequeueAfter: requeueDuration}, nil
}

// SetupWithManager sets up the controller with the Manager
func (r *EnvironmentReconciler) SetupWithManager(mgr ctrl.Manager) error {
	setupLogger := ctrl.Log.WithName("setup").WithName("Environment")

	// Predicate for watching namespaces we might manage
	namespacePredicate := predicate.Funcs{
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

	// Custom predicate for Environment resources
	environmentPredicate := predicate.Funcs{
		CreateFunc: func(e event.CreateEvent) bool {
			return true // Always reconcile on creation
		},
		UpdateFunc: func(e event.UpdateEvent) bool {
			oldEnv, oldOk := e.ObjectOld.(*quixiov1.Environment)
			newEnv, newOk := e.ObjectNew.(*quixiov1.Environment)

			if !oldOk || !newOk {
				return false
			}

			specChanged := !reflect.DeepEqual(oldEnv.Spec, newEnv.Spec)
			timestampChanged := !oldEnv.DeletionTimestamp.Equal(newEnv.DeletionTimestamp)
			generationChanged := oldEnv.Generation != newEnv.Generation

			if specChanged {
				setupLogger.V(1).Info("Reconciling due to spec change", "environment", newEnv.Name)
			} else if timestampChanged {
				setupLogger.V(1).Info("Reconciling due to deletion timestamp change", "environment", newEnv.Name)
			} else if generationChanged {
				setupLogger.V(1).Info("Reconciling due to metadata.generation change", "environment", newEnv.Name)
			}

			return specChanged || timestampChanged || generationChanged
		},
		DeleteFunc: func(e event.DeleteEvent) bool {
			return false // We use finalizers, so don't need to trigger on delete events
		},
		GenericFunc: func(e event.GenericEvent) bool {
			return false
		},
	}

	// Handler to map namespace events to their environment resources
	namespaceHandler := handler.EnqueueRequestsFromMapFunc(func(ctx context.Context, obj client.Object) []reconcile.Request {
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

		environmentCRDNamespace := namespace.Annotations[AnnotationEnvironmentCRDNamespace]
		listOptions := []client.ListOption{}
		if environmentCRDNamespace != "" {
			listOptions = append(listOptions, client.InNamespace(environmentCRDNamespace))
		}
		listOptions = append(listOptions, client.MatchingFields{".spec.id": environmentId})

		var environments quixiov1.EnvironmentList
		if err := mgr.GetClient().List(ctx, &environments, listOptions...); err != nil {
			nsMapperLogger.Error(err, "Failed to list environments for namespace event", "environmentId", environmentId)
			return nil
		}

		var requests []reconcile.Request
		for _, env := range environments.Items {
			nsMapperLogger.Info("Queueing reconcile request for Environment due to namespace event",
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
			nsMapperLogger.V(1).Info("No matching Environment found for namespace event", "environmentId", environmentId, "namespace", namespace.Name)
		}

		return requests
	})

	// Index the Environment spec.id field for efficient lookups
	if err := mgr.GetFieldIndexer().IndexField(context.Background(), &quixiov1.Environment{}, ".spec.id", func(rawObj client.Object) []string {
		env, ok := rawObj.(*quixiov1.Environment)
		if !ok {
			return nil
		}
		if env.Spec.Id == "" {
			return nil
		}
		return []string{env.Spec.Id}
	}); err != nil {
		setupLogger.Error(err, "Failed to set up index for .spec.id")
		return err
	}

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
