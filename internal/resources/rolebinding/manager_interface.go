package rolebinding

import (
	"context"

	v1 "github.com/quix-analytics/quix-environment-operator/api/v1"
	rbacv1 "k8s.io/api/rbac/v1"
)

// Manager defines operations for role binding management
type Manager interface {
	// Delete deletes a role binding
	Delete(ctx context.Context, env *v1.Environment) error

	// GetName returns the name of the role binding for the environment
	GetName(env *v1.Environment) string

	// Exists checks if a role binding exists
	Exists(ctx context.Context, env *v1.Environment) (bool, error)

	// Reconcile creates or updates a role binding for an environment
	Reconcile(ctx context.Context, env *v1.Environment) (*rbacv1.RoleBinding, error)
}
