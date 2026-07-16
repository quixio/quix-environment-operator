package rolebinding

import (
	"context"
	"testing"

	"github.com/go-logr/logr"
	v1 "github.com/quix-analytics/quix-environment-operator/api/v1"
	"github.com/quix-analytics/quix-environment-operator/internal/config"
	"github.com/quix-analytics/quix-environment-operator/internal/resources/namespace"
	corev1 "k8s.io/api/core/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/tools/record"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
)

// GetName must be a pure getter: it derives the role binding name without mutating env.Status.
func TestGetNameIsPure(t *testing.T) {
	m := &DefaultManager{}
	env := &v1.Environment{Spec: v1.EnvironmentSpec{Id: "abc"}}

	if got := m.GetName(env); got != "abc-quix-crb" {
		t.Fatalf("default name = %q, want %q", got, "abc-quix-crb")
	}
	if env.Status.RoleBindingStatus != nil {
		t.Fatalf("GetName mutated env.Status.RoleBindingStatus to %+v", env.Status.RoleBindingStatus)
	}

	env.Status.RoleBindingStatus = &v1.ResourceStatus{ResourceName: "stored-rb"}
	if got := m.GetName(env); got != "stored-rb" {
		t.Fatalf("stored name = %q, want %q", got, "stored-rb")
	}
}

// Delete derives its target names from the pure getters, so it must not mutate env.Status even
// when retried during deletion.
func TestDeleteDoesNotMutateStatus(t *testing.T) {
	sch := runtime.NewScheme()
	if err := rbacv1.AddToScheme(sch); err != nil {
		t.Fatalf("add rbac scheme: %v", err)
	}
	if err := corev1.AddToScheme(sch); err != nil {
		t.Fatalf("add core scheme: %v", err)
	}
	c := fake.NewClientBuilder().WithScheme(sch).Build()
	cfg := &config.OperatorConfig{NamespaceSuffix: "-qdep"}
	nsMgr := namespace.NewManager(c, record.NewFakeRecorder(1), cfg)
	m := NewManager(c, cfg, logr.Discard(), nsMgr)

	env := &v1.Environment{Spec: v1.EnvironmentSpec{Id: "abc"}}
	if err := m.Delete(context.Background(), env); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if env.Status.NamespaceStatus != nil || env.Status.RoleBindingStatus != nil {
		t.Fatalf("Delete mutated env.Status: ns=%+v rb=%+v", env.Status.NamespaceStatus, env.Status.RoleBindingStatus)
	}
}
