package security

import (
	"context"
	"fmt"

	rbacv1 "k8s.io/api/rbac/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

// Validator provides security validation functions
type Validator struct {
	client client.Client
}

// NewValidator creates a new security validator
func NewValidator(client client.Client) *Validator {
	return &Validator{
		client: client,
	}
}

// ValidateClusterRole checks if a ClusterRole has dangerous permissions that could allow privilege escalation
func (v *Validator) ValidateClusterRole(ctx context.Context, roleName string) error {
	// Get the ClusterRole
	clusterRole := &rbacv1.ClusterRole{}
	if err := v.client.Get(ctx, types.NamespacedName{Name: roleName}, clusterRole); err != nil {
		return fmt.Errorf("failed to get ClusterRole %s: %w", roleName, err)
	}

	// Validate the rules
	if err := v.validateRules(clusterRole.Rules, clusterRole.Name, "ClusterRole"); err != nil {
		return err
	}

	return nil
}

// validateRules checks a set of PolicyRules for dangerous permissions
func (v *Validator) validateRules(rules []rbacv1.PolicyRule, name, kind string) error {
	// Check for dangerous rules that would allow role/binding manipulation
	for _, rule := range rules {
		// Check for permissions that would allow binding roles
		if containsAny(rule.APIGroups, []string{"rbac.authorization.k8s.io", "*"}) {
			if containsAny(rule.Resources, []string{"rolebindings", "clusterrolebindings", "*"}) {
				if containsAny(rule.Verbs, []string{"create", "update", "patch", "delete", "*"}) {
					return fmt.Errorf("security violation: %s '%s' has dangerous role binding permissions that could allow privilege escalation", kind, name)
				}
			}
		}

		// Check for rules that grant complete access to everything (wildcard permissions)
		if containsAny(rule.APIGroups, []string{"*"}) &&
			containsAny(rule.Resources, []string{"*"}) &&
			containsAny(rule.Verbs, []string{"*"}) {
			return fmt.Errorf("security violation: %s '%s' has excessive wildcard permissions that violate security posture", kind, name)
		}
	}

	return nil
}

// ValidateRole checks if a Role has excessively permissive rules
func (v *Validator) ValidateRole(ctx context.Context, role *rbacv1.Role) error {
	if role == nil {
		return fmt.Errorf("role cannot be nil")
	}

	// Validate the rules using the same logic as ClusterRole
	if err := v.validateRules(role.Rules, role.Name, "Role"); err != nil {
		return err
	}

	return nil
}

// containsAny checks if any element in source is in target
func containsAny(source, target []string) bool {
	for _, s := range source {
		for _, t := range target {
			if s == t {
				return true
			}
		}
	}
	return false
}
