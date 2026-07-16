package security

import (
	"context"
	"strings"
	"testing"

	rbacv1 "k8s.io/api/rbac/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
)

func clusterRoleScheme(t *testing.T) *runtime.Scheme {
	t.Helper()
	sch := runtime.NewScheme()
	if err := rbacv1.AddToScheme(sch); err != nil {
		t.Fatalf("add rbac scheme: %v", err)
	}
	return sch
}

func TestValidateClusterRoleMissingRole(t *testing.T) {
	c := fake.NewClientBuilder().WithScheme(clusterRoleScheme(t)).Build()
	v := NewValidator(c)
	err := v.ValidateClusterRole(context.Background(), "does-not-exist")
	if err == nil {
		t.Fatalf("expected an error for a missing ClusterRole, got nil")
	}
}

func TestValidateClusterRoleSafeRolePasses(t *testing.T) {
	cr := &rbacv1.ClusterRole{
		ObjectMeta: metav1.ObjectMeta{Name: "safe-role"},
		Rules: []rbacv1.PolicyRule{
			{APIGroups: []string{""}, Resources: []string{"pods", "services"}, Verbs: []string{"get", "list", "watch", "create"}},
		},
	}
	c := fake.NewClientBuilder().WithScheme(clusterRoleScheme(t)).WithObjects(cr).Build()
	v := NewValidator(c)
	if err := v.ValidateClusterRole(context.Background(), "safe-role"); err != nil {
		t.Fatalf("expected a safe ClusterRole to pass, got: %v", err)
	}
}

func TestValidateClusterRoleUnsafeRoleRejected(t *testing.T) {
	cr := &rbacv1.ClusterRole{
		ObjectMeta: metav1.ObjectMeta{Name: "unsafe-role"},
		Rules: []rbacv1.PolicyRule{
			{APIGroups: []string{"rbac.authorization.k8s.io"}, Resources: []string{"clusterroles"}, Verbs: []string{"escalate"}},
		},
	}
	c := fake.NewClientBuilder().WithScheme(clusterRoleScheme(t)).WithObjects(cr).Build()
	v := NewValidator(c)
	err := v.ValidateClusterRole(context.Background(), "unsafe-role")
	if err == nil || !strings.Contains(err.Error(), "security violation") {
		t.Fatalf("expected an unsafe ClusterRole to be rejected with a security violation, got: %v", err)
	}
}
