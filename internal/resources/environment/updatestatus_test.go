package environment

import (
	"context"
	"fmt"
	"testing"

	v1 "github.com/quix-analytics/quix-environment-operator/api/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"
)

// UpdateStatus must retry on a Conflict by re-fetching and re-applying the desired status, so a
// single concurrent modification does not fail the write.
func TestUpdateStatusRetriesOnConflict(t *testing.T) {
	sch := runtime.NewScheme()
	if err := v1.AddToScheme(sch); err != nil {
		t.Fatalf("add scheme: %v", err)
	}
	env := &v1.Environment{
		ObjectMeta: metav1.ObjectMeta{Name: "env-a", Generation: 2},
		Status:     v1.EnvironmentStatus{Phase: v1.EnvironmentPhaseReady, ObservedGeneration: 1},
	}
	base := fake.NewClientBuilder().WithScheme(sch).WithObjects(env).WithStatusSubresource(&v1.Environment{}).Build()

	calls := 0
	c := interceptor.NewClient(base, interceptor.Funcs{
		SubResourceUpdate: func(ctx context.Context, cl client.Client, subResourceName string, obj client.Object, opts ...client.SubResourceUpdateOption) error {
			calls++
			if calls == 1 {
				return apierrors.NewConflict(schema.GroupResource{Group: "quix.io", Resource: "environments"}, obj.GetName(), fmt.Errorf("simulated conflict"))
			}
			return cl.Status().Update(ctx, obj, opts...)
		},
	})

	m := NewManager(c, c)

	in := env.DeepCopy()
	in.Status.ObservedGeneration = 2
	if err := m.UpdateStatus(context.Background(), in); err != nil {
		t.Fatalf("UpdateStatus should succeed after retrying the conflict, got: %v", err)
	}
	if calls < 2 {
		t.Fatalf("expected UpdateStatus to retry the status write (>=2 attempts), got %d", calls)
	}

	got := &v1.Environment{}
	if err := base.Get(context.Background(), types.NamespacedName{Name: "env-a"}, got); err != nil {
		t.Fatalf("get persisted env: %v", err)
	}
	if got.Status.ObservedGeneration != 2 {
		t.Fatalf("persisted ObservedGeneration = %d, want 2", got.Status.ObservedGeneration)
	}
}
