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
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/log"
)

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

func (r *EnvironmentReconciler) cleanupObsoleteConditions(st *quixiov1.EnvironmentStatus) {
	meta.RemoveStatusCondition(&st.Conditions, status.ConditionTypeNamespaceCreated)
	meta.RemoveStatusCondition(&st.Conditions, status.ConditionTypeRoleBindingCreated)
	meta.RemoveStatusCondition(&st.Conditions, status.ConditionTypeNamespaceDeleted)
}

func (r *EnvironmentReconciler) handleStatusUpdateError(ctx context.Context, err error, msg string) (ctrl.Result, error) {
	logger := log.FromContext(ctx)
	logger.V(3).Error(err, msg)
	return ctrl.Result{RequeueAfter: 1 * time.Second}, err
}

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
