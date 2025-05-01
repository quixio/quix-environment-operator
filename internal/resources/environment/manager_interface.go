package environment

import (
	"context"

	v1 "github.com/quix-analytics/quix-environment-operator/api/v1"
)

// Manager handles operations for environment resources
type Manager interface {
	// Get retrieves an environment by name
	Get(ctx context.Context, name string) (*v1.Environment, error)
	// GetList retrieves a list of all environments
	GetList(ctx context.Context) (*v1.EnvironmentList, error)
	// UpdateStatus updates the status of an environment
	UpdateStatus(ctx context.Context, env *v1.Environment) error
	// AddFinalizer adds a finalizer to an environment
	AddFinalizer(ctx context.Context, env *v1.Environment, finalizerName string) error
	// RemoveFinalizer removes a finalizer from an environment
	RemoveFinalizer(ctx context.Context, env *v1.Environment, finalizerName string) error
	// IsBeingDeleted checks if the environment is marked for deletion
	IsBeingDeleted(env *v1.Environment) bool
	// HasFinalizer checks if the environment has the specified finalizer
	HasFinalizer(env *v1.Environment, finalizerName string) bool
}
