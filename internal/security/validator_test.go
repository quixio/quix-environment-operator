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

func rule(groups, resources, verbs []string) rbacv1.PolicyRule {
	return rbacv1.PolicyRule{APIGroups: groups, Resources: resources, Verbs: verbs}
}

func TestValidateRules(t *testing.T) {
	v := &Validator{}
	tests := []struct {
		name     string
		rules    []rbacv1.PolicyRule
		wantViol bool
	}{
		{
			name:     "escalate verb on clusterroles is rejected",
			rules:    []rbacv1.PolicyRule{rule([]string{"rbac.authorization.k8s.io"}, []string{"clusterroles"}, []string{"escalate"})},
			wantViol: true,
		},
		{
			name:     "bind verb on rolebindings is rejected",
			rules:    []rbacv1.PolicyRule{rule([]string{"rbac.authorization.k8s.io"}, []string{"rolebindings"}, []string{"bind"})},
			wantViol: true,
		},
		{
			name:     "impersonate on serviceaccounts (core group) is rejected",
			rules:    []rbacv1.PolicyRule{rule([]string{""}, []string{"serviceaccounts"}, []string{"impersonate"})},
			wantViol: true,
		},
		{
			name:     "impersonate on users (core group) is rejected",
			rules:    []rbacv1.PolicyRule{rule([]string{""}, []string{"users"}, []string{"impersonate"})},
			wantViol: true,
		},
		{
			name:     "impersonate on groups (core group) is rejected",
			rules:    []rbacv1.PolicyRule{rule([]string{""}, []string{"groups"}, []string{"impersonate"})},
			wantViol: true,
		},
		{
			// Guards the wildcard-verb gap: a role with verbs ["*"] on a core identity must
			// still be caught as an impersonate vector.
			name:     "wildcard verb on serviceaccounts (core group) is rejected as impersonate",
			rules:    []rbacv1.PolicyRule{rule([]string{""}, []string{"serviceaccounts"}, []string{"*"})},
			wantViol: true,
		},
		{
			name:     "impersonate via wildcard apiGroup is rejected",
			rules:    []rbacv1.PolicyRule{rule([]string{"*"}, []string{"users"}, []string{"impersonate"})},
			wantViol: true,
		},
		{
			name:     "escalate via wildcard apiGroup is rejected",
			rules:    []rbacv1.PolicyRule{rule([]string{"*"}, []string{"clusterroles"}, []string{"escalate"})},
			wantViol: true,
		},
		{
			name:     "read-only access to RBAC roles is not falsely rejected",
			rules:    []rbacv1.PolicyRule{rule([]string{"rbac.authorization.k8s.io"}, []string{"roles", "clusterroles"}, []string{"get", "list", "watch"})},
			wantViol: false,
		},
		{
			name:     "existing rolebinding write check still rejects",
			rules:    []rbacv1.PolicyRule{rule([]string{"rbac.authorization.k8s.io"}, []string{"rolebindings"}, []string{"create"})},
			wantViol: true,
		},
		{
			name:     "wildcard-everything still rejected",
			rules:    []rbacv1.PolicyRule{rule([]string{"*"}, []string{"*"}, []string{"*"})},
			wantViol: true,
		},
		{
			name:     "benign workload rule is accepted",
			rules:    []rbacv1.PolicyRule{rule([]string{""}, []string{"pods", "services", "configmaps"}, []string{"get", "list", "watch", "create"})},
			wantViol: false,
		},
		{
			// Guards the intentional deferral: secrets/serviceaccounts reads must NOT be
			// rejected yet, since the shipped platform role relies on them.
			name:     "secrets and serviceaccounts access is still accepted (deferred check)",
			rules:    []rbacv1.PolicyRule{rule([]string{""}, []string{"secrets", "serviceaccounts"}, []string{"get", "list", "watch", "create", "update", "patch", "delete"})},
			wantViol: false,
		},
		{
			name:     "impersonate in a non-identity resource is not falsely flagged",
			rules:    []rbacv1.PolicyRule{rule([]string{""}, []string{"pods"}, []string{"get", "list"})},
			wantViol: false,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := v.validateRules(tt.rules, "test-role", "ClusterRole")
			gotViol := err != nil
			if gotViol != tt.wantViol {
				t.Fatalf("validateRules() error = %v, wantViol = %v", err, tt.wantViol)
			}
			if gotViol && !strings.Contains(err.Error(), "security violation") {
				t.Fatalf("violation error must contain 'security violation', got: %v", err)
			}
		})
	}
}

func TestValidateNoAggregationLabels(t *testing.T) {
	if err := validateNoAggregationLabels(map[string]string{"app": "x"}, "r", "ClusterRole"); err != nil {
		t.Fatalf("benign labels must be accepted, got: %v", err)
	}
	err := validateNoAggregationLabels(map[string]string{"rbac.authorization.k8s.io/aggregate-to-admin": "true"}, "r", "ClusterRole")
	if err == nil || !strings.Contains(err.Error(), "security violation") {
		t.Fatalf("aggregation label must be rejected with a security violation, got: %v", err)
	}
}

func TestValidateClusterRoleRejectsAggregationLabel(t *testing.T) {
	sch := runtime.NewScheme()
	if err := rbacv1.AddToScheme(sch); err != nil {
		t.Fatalf("failed to build scheme: %v", err)
	}
	cr := &rbacv1.ClusterRole{
		ObjectMeta: metav1.ObjectMeta{
			Name:   "aggregated-role",
			Labels: map[string]string{"rbac.authorization.k8s.io/aggregate-to-edit": "true"},
		},
	}
	c := fake.NewClientBuilder().WithScheme(sch).WithObjects(cr).Build()
	v := NewValidator(c)
	err := v.ValidateClusterRole(context.Background(), "aggregated-role")
	if err == nil || !strings.Contains(err.Error(), "security violation") {
		t.Fatalf("ValidateClusterRole must reject an aggregation-labelled role, got: %v", err)
	}
}

func TestContainsAny(t *testing.T) {
	cases := []struct {
		name   string
		a, b   []string
		expect bool
	}{
		{"both empty", nil, nil, false},
		{"no overlap", []string{"a", "b"}, []string{"c", "d"}, false},
		{"single overlap", []string{"a", "b"}, []string{"b", "c"}, true},
		{"overlap regardless of order", []string{"b", "c"}, []string{"a", "b"}, true},
		{"duplicate elements", []string{"x", "x"}, []string{"x"}, true},
		{"empty source", nil, []string{"a"}, false},
		{"empty target", []string{"a"}, nil, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := ContainsAny(c.a, c.b); got != c.expect {
				t.Fatalf("ContainsAny(%v, %v) = %v, want %v", c.a, c.b, got, c.expect)
			}
			// symmetry: swapping arguments must not change the result
			if got := ContainsAny(c.b, c.a); got != c.expect {
				t.Fatalf("ContainsAny is not symmetric for %v / %v", c.a, c.b)
			}
		})
	}
}
