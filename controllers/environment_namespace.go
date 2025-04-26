package controllers

import (
	"context"
	"fmt"
	"time"

	quixiov1 "github.com/quix-analytics/quix-environment-operator/api/v1"
	"github.com/quix-analytics/quix-environment-operator/internal/status"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/log"
)

// reconcileNamespace reconciles the namespace for the environment
func (r *EnvironmentReconciler) reconcileNamespace(ctx context.Context, env *quixiov1.Environment, namespaceName string) (ctrl.Result, bool, error) {
	// Get namespace and delegate to appropriate handler
	namespace, nsGetErr := r.namespaceManager.GetNamespace(ctx, namespaceName)

	if nsGetErr != nil {
		return r.handleNamespaceNotFound(ctx, env, namespaceName, nsGetErr)
	}

	return r.handleExistingNamespace(ctx, env, namespace, namespaceName)
}

// handleNamespaceNotFound handles the case when a namespace is not found or there's an error getting it
func (r *EnvironmentReconciler) handleNamespaceNotFound(ctx context.Context, env *quixiov1.Environment, namespaceName string, nsGetErr error) (ctrl.Result, bool, error) {
	logger := log.FromContext(ctx)

	if errors.IsNotFound(nsGetErr) {
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

		createErr := r.namespaceManager.CreateNamespace(ctx, env, namespaceName)
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

// checkNamespaceStateForDeletion determines the state of the namespace during environment deletion
func (r *EnvironmentReconciler) checkNamespaceStateForDeletion(ctx context.Context, env *quixiov1.Environment, namespaceName string) (quixiov1.SubResourcePhase, error) {
	namespace, nsGetErr := r.namespaceManager.GetNamespace(ctx, namespaceName)

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
	namespace, err := r.namespaceManager.GetNamespace(ctx, namespaceName)
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
