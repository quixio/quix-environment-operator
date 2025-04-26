package controllers

import (
	"context"
	"fmt"
	"reflect"
	"strings"
	"time"

	"github.com/go-logr/logr"
	quixiov1 "github.com/quix-analytics/quix-environment-operator/api/v1"
	corev1 "k8s.io/api/core/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"sigs.k8s.io/controller-runtime/pkg/log"
)

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
