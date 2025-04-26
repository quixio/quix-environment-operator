package namespace

import (
	"context"

	v1 "github.com/quix-analytics/quix-environment-operator/api/v1"
	corev1 "k8s.io/api/core/v1"
)

// Manager defines operations for managing namespaces
type Manager interface {
	// GetNamespaceName returns the namespace name for an environment
	GetNamespaceName(env *v1.Environment) string
	// Delete deletes a namespace for an environment
	Delete(ctx context.Context, env *v1.Environment) error

	// Get retrieves a namespace for an environment
	Get(ctx context.Context, env *v1.Environment) (*corev1.Namespace, error)

	// Exists checks if a namespace exists for an environment
	Exists(ctx context.Context, env *v1.Environment) (bool, error)

	// IsDeleting checks if a namespace for an environment is deleting
	IsDeleting(ctx context.Context, env *v1.Environment) (bool, error)

	// IsManaged checks if a namespace is managed by this operator
	IsManaged(env *v1.Environment) bool

	// Reconcile creates or updates a namespace for an environment
	Reconcile(ctx context.Context, env *v1.Environment) (*corev1.Namespace, error)
}
