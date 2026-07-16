package environment

import (
	"context"
	"testing"

	"github.com/quix-analytics/quix-environment-operator/internal/resources/namespace"
	"github.com/quix-analytics/quix-environment-operator/internal/resources/rolebinding"
	rbacv1 "k8s.io/api/rbac/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

func TestMapRoleBindingToEnvironment(t *testing.T) {
	rb := &rbacv1.RoleBinding{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "env-id-quix-crb",
			Namespace: "env-id-qdep",
			Labels: map[string]string{
				namespace.ManagedByLabel:       rolebinding.ManagedByValue,
				namespace.LabelEnvironmentID:   "env-id",
				namespace.LabelEnvironmentName: "env-resource-name",
			},
		},
	}

	reqs := mapRoleBindingToEnvironment(context.Background(), rb)
	if len(reqs) != 1 {
		t.Fatalf("expected one reconcile request, got %d", len(reqs))
	}
	if got := reqs[0].Name; got != "env-resource-name" {
		t.Fatalf("request name = %q, want %q", got, "env-resource-name")
	}
	if got := reqs[0].Namespace; got != "" {
		t.Fatalf("request namespace = %q, want empty namespace for cluster-scoped Environment", got)
	}
}

func TestMapRoleBindingToEnvironmentIgnoresUnmanagedRoleBindings(t *testing.T) {
	cases := []struct {
		name   string
		labels map[string]string
	}{
		{
			name: "missing labels",
		},
		{
			name: "foreign managed-by",
			labels: map[string]string{
				namespace.ManagedByLabel:       "other-controller",
				namespace.LabelEnvironmentName: "env-resource-name",
			},
		},
		{
			name: "missing environment name",
			labels: map[string]string{
				namespace.ManagedByLabel:     rolebinding.ManagedByValue,
				namespace.LabelEnvironmentID: "env-id",
			},
		},
		{
			name: "missing environment id",
			labels: map[string]string{
				namespace.ManagedByLabel:       rolebinding.ManagedByValue,
				namespace.LabelEnvironmentName: "env-resource-name",
			},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			rb := &rbacv1.RoleBinding{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "env-id-quix-crb",
					Namespace: "env-id-qdep",
					Labels:    tc.labels,
				},
			}
			if reqs := mapRoleBindingToEnvironment(context.Background(), rb); len(reqs) != 0 {
				t.Fatalf("expected no reconcile requests, got %#v", reqs)
			}
		})
	}
}
