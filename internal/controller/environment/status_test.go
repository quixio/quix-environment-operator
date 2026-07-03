package environment

import (
	"context"
	"testing"

	v1 "github.com/quix-analytics/quix-environment-operator/api/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

type recordingEnvironmentManager struct {
	updateCount int
}

func (m *recordingEnvironmentManager) Get(context.Context, string) (*v1.Environment, error) {
	return nil, nil
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
	return false
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
