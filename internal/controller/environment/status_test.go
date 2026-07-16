package environment

import (
	"context"
	"testing"
	"time"

	v1 "github.com/quix-analytics/quix-environment-operator/api/v1"
	"github.com/quix-analytics/quix-environment-operator/internal/config"
	"github.com/quix-analytics/quix-environment-operator/internal/resources/rolebinding"
	corev1 "k8s.io/api/core/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
)

type recordingEnvironmentManager struct {
	updateCount int
	env         *v1.Environment
}

func (m *recordingEnvironmentManager) Get(context.Context, string) (*v1.Environment, error) {
	return m.env, nil
}

func (m *recordingEnvironmentManager) GetList(context.Context) (*v1.EnvironmentList, error) {
	return nil, nil
}

func (m *recordingEnvironmentManager) UpdateStatus(_ context.Context, _ *v1.Environment) error {
	m.updateCount++
	return nil
}

func (m *recordingEnvironmentManager) AddFinalizer(context.Context, *v1.Environment, string) error {
	return nil
}

func (m *recordingEnvironmentManager) RemoveFinalizer(context.Context, *v1.Environment, string) error {
	return nil
}

func (m *recordingEnvironmentManager) IsBeingDeleted(*v1.Environment) bool {
	return false
}

func (m *recordingEnvironmentManager) HasFinalizer(*v1.Environment, string) bool {
	return true
}

type readyNamespaceManager struct{}

func (m readyNamespaceManager) GetNamespaceName(env *v1.Environment) string {
	return env.Spec.Id + "-qdep"
}

func (m readyNamespaceManager) Delete(context.Context, *v1.Environment) error {
	return nil
}

func (m readyNamespaceManager) Get(context.Context, *v1.Environment) (*corev1.Namespace, error) {
	return &corev1.Namespace{}, nil
}

func (m readyNamespaceManager) Exists(context.Context, *v1.Environment) (bool, error) {
	return true, nil
}

func (m readyNamespaceManager) IsDeleting(context.Context, *v1.Environment) (bool, error) {
	return false, nil
}

func (m readyNamespaceManager) Reconcile(context.Context, *v1.Environment) (*corev1.Namespace, error) {
	return &corev1.Namespace{}, nil
}

type pendingRoleBindingManager struct{}

func (m pendingRoleBindingManager) Delete(context.Context, *v1.Environment) error {
	return nil
}

func (m pendingRoleBindingManager) GetName(env *v1.Environment) string {
	return env.Spec.Id + "-quix-crb"
}

func (m pendingRoleBindingManager) Exists(context.Context, *v1.Environment) (bool, error) {
	return true, nil
}

func (m pendingRoleBindingManager) Reconcile(context.Context, *v1.Environment) (*rbacv1.RoleBinding, error) {
	return nil, rolebinding.ErrRoleBindingRecreatePending
}

func TestUpdateStatusReadyRecoversFailedSubResourceStatuses(t *testing.T) {
	manager := &recordingEnvironmentManager{}
	reconciler := &EnvironmentReconciler{environmentManager: manager}
	env := &v1.Environment{
		ObjectMeta: metav1.ObjectMeta{Name: "recover-status", Generation: 2},
		Status: v1.EnvironmentStatus{
			Phase: v1.EnvironmentPhaseFailed,
			NamespaceStatus: &v1.ResourceStatus{
				Phase:   v1.ResourceStatusPhaseFailed,
				Message: "previous namespace failure",
			},
			RoleBindingStatus: &v1.ResourceStatus{
				Phase:   v1.ResourceStatusPhaseFailed,
				Message: "previous role binding failure",
			},
		},
	}

	if err := reconciler.updateStatus(context.Background(), env, v1.EnvironmentPhaseReady, "Environment is ready"); err != nil {
		t.Fatalf("updateStatus returned error: %v", err)
	}

	if manager.updateCount != 1 {
		t.Fatalf("expected one status update, got %d", manager.updateCount)
	}
	if env.Status.NamespaceStatus.Phase != v1.ResourceStatusPhaseActive {
		t.Fatalf("expected namespace status Active, got %q", env.Status.NamespaceStatus.Phase)
	}
	if env.Status.RoleBindingStatus.Phase != v1.ResourceStatusPhaseActive {
		t.Fatalf("expected role binding status Active, got %q", env.Status.RoleBindingStatus.Phase)
	}
}

func TestUpdateStatusFailedDoesNotInferSubResourceFromMessageText(t *testing.T) {
	manager := &recordingEnvironmentManager{}
	reconciler := &EnvironmentReconciler{environmentManager: manager}
	env := &v1.Environment{
		ObjectMeta: metav1.ObjectMeta{Name: "message-status", Generation: 1},
		Status: v1.EnvironmentStatus{
			Phase: v1.EnvironmentPhaseReady,
			NamespaceStatus: &v1.ResourceStatus{
				Phase:   v1.ResourceStatusPhaseActive,
				Message: "Namespace is active",
			},
			RoleBindingStatus: &v1.ResourceStatus{
				Phase:   v1.ResourceStatusPhaseActive,
				Message: "RoleBinding is active",
			},
		},
	}

	err := reconciler.updateStatus(context.Background(), env, v1.EnvironmentPhaseFailed, `Failed to reconcile role binding: namespaces "example" not found`)
	if err != nil {
		t.Fatalf("updateStatus returned error: %v", err)
	}

	if env.Status.NamespaceStatus.Phase != v1.ResourceStatusPhaseActive {
		t.Fatalf("expected namespace status to remain Active, got %q", env.Status.NamespaceStatus.Phase)
	}
	if env.Status.RoleBindingStatus.Phase != v1.ResourceStatusPhaseActive {
		t.Fatalf("expected role binding status to remain Active until explicit caller attribution, got %q", env.Status.RoleBindingStatus.Phase)
	}
}

func TestReconcileRoleBindingRecreatePendingClearsReadyPhase(t *testing.T) {
	assertRoleBindingRecreatePendingPhase(t, v1.EnvironmentPhaseReady)
}

func TestReconcileRoleBindingRecreatePendingClearsFailedPhase(t *testing.T) {
	assertRoleBindingRecreatePendingPhase(t, v1.EnvironmentPhaseFailed)
}

func assertRoleBindingRecreatePendingPhase(t *testing.T, initialPhase v1.EnvironmentPhase) {
	t.Helper()
	env := &v1.Environment{
		ObjectMeta: metav1.ObjectMeta{Name: "pending-status", Generation: 3},
		Spec:       v1.EnvironmentSpec{Id: "pending"},
		Status: v1.EnvironmentStatus{
			Phase: initialPhase,
			NamespaceStatus: &v1.ResourceStatus{
				Phase:        v1.ResourceStatusPhaseActive,
				Message:      "Namespace is active",
				ResourceName: "pending-qdep",
			},
			RoleBindingStatus: &v1.ResourceStatus{
				Phase:        v1.ResourceStatusPhaseActive,
				Message:      "RoleBinding is active",
				ResourceName: "pending-quix-crb",
			},
		},
	}
	envManager := &recordingEnvironmentManager{env: env}
	reconciler := &EnvironmentReconciler{
		environmentManager: envManager,
		operatorConfig:     &config.OperatorConfig{MaxConcurrentReconciles: 1},
		namespaceManager:   readyNamespaceManager{},
		roleBindingManager: pendingRoleBindingManager{},
	}

	result, err := reconciler.Reconcile(context.Background(), ctrl.Request{NamespacedName: types.NamespacedName{Name: env.Name}})
	if err != nil {
		t.Fatalf("Reconcile returned error: %v", err)
	}
	if !result.Requeue || result.RequeueAfter != time.Second {
		t.Fatalf("expected one-second requeue, got %+v", result)
	}
	if env.Status.Phase != v1.EnvironmentPhaseInProgress {
		t.Fatalf("expected Environment phase InProgress, got %q", env.Status.Phase)
	}
	if env.Status.RoleBindingStatus.Phase != v1.ResourceStatusPhaseCreating {
		t.Fatalf("expected RoleBindingStatus Creating, got %q", env.Status.RoleBindingStatus.Phase)
	}
	if envManager.updateCount != 1 {
		t.Fatalf("expected one pending status update, got %d", envManager.updateCount)
	}
}
