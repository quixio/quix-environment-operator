package security

import (
	"context"
	"errors"
	"fmt"
	"strings"

	rbacv1 "k8s.io/api/rbac/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

// ErrSecurityViolation is wrapped (via %w) into every validator rejection so callers can classify
// the failure with errors.Is instead of substring matching on the message text.
var ErrSecurityViolation = errors.New("security violation")

// aggregationLabelPrefix marks ClusterRoles whose rules are assembled at runtime by the
// aggregation controller. Such rules never appear in clusterRole.Rules, so they are invisible
// to validateRules — a labelled ClusterRole can therefore acquire arbitrary permissions after
// validation passes.
const aggregationLabelPrefix = "rbac.authorization.k8s.io/aggregate-to-"

// Validator provides security validation functions
type Validator struct {
	client client.Reader
}

// NewValidator creates a new security validator. It accepts a client.Reader (satisfied by a
// full client.Client) so callers can pass the manager's uncached API reader for checks that run
// before the cache is started, such as fail-fast validation at operator startup.
func NewValidator(client client.Reader) *Validator {
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

	// Reject aggregation labels: aggregated rules are injected at runtime and are not present
	// in clusterRole.Rules, so they would bypass validateRules entirely.
	if err := validateNoAggregationLabels(clusterRole.Labels, clusterRole.Name, "ClusterRole"); err != nil {
		return err
	}

	// Validate the rules
	if err := v.validateRules(clusterRole.Rules, clusterRole.Name, "ClusterRole"); err != nil {
		return err
	}

	return nil
}

// validateNoAggregationLabels rejects a ClusterRole carrying any rbac aggregation label, since
// such labels cause additional rules to be merged in at runtime that rule validation cannot see.
func validateNoAggregationLabels(labels map[string]string, name, kind string) error {
	for key := range labels {
		if strings.HasPrefix(key, aggregationLabelPrefix) {
			return fmt.Errorf("%w: %s '%s' has aggregation label '%s' that injects rules invisible to validation", ErrSecurityViolation, kind, name, key)
		}
	}
	return nil
}

// validateRules checks a set of PolicyRules for dangerous permissions
func (v *Validator) validateRules(rules []rbacv1.PolicyRule, name, kind string) error {
	// Check for dangerous rules that would allow role/binding manipulation
	for _, rule := range rules {
		// Check for permissions that would allow binding roles
		if ContainsAny(rule.APIGroups, []string{"rbac.authorization.k8s.io", "*"}) {
			if ContainsAny(rule.Resources, []string{"rolebindings", "clusterrolebindings", "*"}) {
				if ContainsAny(rule.Verbs, []string{"create", "update", "patch", "delete", "*"}) {
					return fmt.Errorf("%w: %s '%s' has dangerous role binding permissions that could allow privilege escalation", ErrSecurityViolation, kind, name)
				}
			}
		}

		// Check for escalate/bind on RBAC roles. 'escalate' lets the holder grant itself
		// permissions it does not already have; 'bind' lets it bind arbitrary roles to subjects.
		// Both are direct privilege-escalation vectors even without rolebinding write access.
		if ContainsAny(rule.APIGroups, []string{"rbac.authorization.k8s.io", "*"}) {
			if ContainsAny(rule.Resources, []string{"roles", "clusterroles", "rolebindings", "clusterrolebindings", "*"}) {
				if ContainsAny(rule.Verbs, []string{"escalate", "bind", "*"}) {
					return fmt.Errorf("%w: %s '%s' has escalate/bind permissions on RBAC resources that could allow privilege escalation", ErrSecurityViolation, kind, name)
				}
			}
		}

		// Check for the 'impersonate' verb on core-group identities. Impersonation lets the
		// holder act as any user, group, or service account, bypassing its own RBAC entirely.
		if ContainsAny(rule.APIGroups, []string{"", "*"}) {
			if ContainsAny(rule.Resources, []string{"users", "groups", "serviceaccounts", "*"}) {
				if ContainsAny(rule.Verbs, []string{"impersonate", "*"}) {
					return fmt.Errorf("%w: %s '%s' has impersonate permissions that could allow privilege escalation", ErrSecurityViolation, kind, name)
				}
			}
		}

		// Check for rules that grant complete access to everything (wildcard permissions)
		if ContainsAny(rule.APIGroups, []string{"*"}) &&
			ContainsAny(rule.Resources, []string{"*"}) &&
			ContainsAny(rule.Verbs, []string{"*"}) {
			return fmt.Errorf("%w: %s '%s' has excessive wildcard permissions that violate security posture", ErrSecurityViolation, kind, name)
		}

		// NOTE: Direct secrets access and serviceaccounts/token write are also escalation
		// vectors, but the shipped platform ClusterRole (the role this validator runs against
		// via rolebinding.Manager) currently grants exactly those verbs. Rejecting them here
		// would make the operator refuse its own configured role and fail to create
		// RoleBindings. Those checks are intentionally deferred until the platform role is
		// tightened to drop the grants (tracked separately).
	}

	return nil
}

// ContainsAny reports whether the two slices share at least one element. The check is
// symmetric (order-independent): it returns true when any element of source also appears in target.
func ContainsAny(source, target []string) bool {
	for _, s := range source {
		for _, t := range target {
			if s == t {
				return true
			}
		}
	}
	return false
}
