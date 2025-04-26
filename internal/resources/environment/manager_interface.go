package environment

import (
	"context"

	v1 "github.com/quix-analytics/quix-environment-operator/api/v1"
)

// Manager defines the operations for managing environment resources
type Manager interface {
	// Get retrieves an environment by name from the Kubernetes API
	Get(ctx context.Context, name string, namespace string) (*v1.Environment, error)

	// GetList retrieves a list of all environments from the Kubernetes API
	GetList(ctx context.Context) (*v1.EnvironmentList, error)

	// UpdateStatus updates only the status of an environment resource
	UpdateStatus(ctx context.Context, env *v1.Environment) error

	// AddFinalizer adds a finalizer to the environment resource
	AddFinalizer(ctx context.Context, env *v1.Environment, finalizerName string) error

	// RemoveFinalizer removes a finalizer from the environment resource
	RemoveFinalizer(ctx context.Context, env *v1.Environment, finalizerName string) error

	// IsBeingDeleted checks if the environment is marked for deletion
	IsBeingDeleted(env *v1.Environment) bool

	// HasFinalizer checks if the environment has the specified finalizer
	HasFinalizer(env *v1.Environment, finalizerName string) bool
}
