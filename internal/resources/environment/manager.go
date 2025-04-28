package environment

import (
	"context"
	"fmt"

	"github.com/go-logr/logr"
	v1 "github.com/quix-analytics/quix-environment-operator/api/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/log"
)

// DefaultManager implements the Manager interface
type DefaultManager struct {
	client client.Client
	logger logr.Logger
}

// NewManager creates a new instance of the DefaultManager
func NewManager(client client.Client) *DefaultManager {
	return &DefaultManager{
		client: client,
		logger: log.Log.WithName("environment-manager"),
	}
}

// Get retrieves an environment by name and namespace
func (m *DefaultManager) Get(ctx context.Context, name string, namespace string) (*v1.Environment, error) {
	env := &v1.Environment{}
	err := m.client.Get(ctx, types.NamespacedName{Name: name, Namespace: namespace}, env)
	if err != nil {
		return nil, err
	}
	return env, nil
}

// GetList retrieves a list of all environments
func (m *DefaultManager) GetList(ctx context.Context) (*v1.EnvironmentList, error) {
	environmentList := &v1.EnvironmentList{}
	err := m.client.List(ctx, environmentList)
	if err != nil {
		return nil, fmt.Errorf("failed to list environments: %w", err)
	}
	return environmentList, nil
}

// update updates an existing environment
func (m *DefaultManager) update(ctx context.Context, env *v1.Environment) error {
	m.logger.V(0).Info("Updating environment", "name", env.Name)
	return m.client.Update(ctx, env)
}

// UpdateStatus updates the status of an environment
func (m *DefaultManager) UpdateStatus(ctx context.Context, env *v1.Environment) error {
	m.logger.V(0).Info("Updating environment status", "name", env.Name, "phase", env.Status.Phase)
	return m.client.Status().Update(ctx, env)
}

// AddFinalizer adds a finalizer to an environment
func (m *DefaultManager) AddFinalizer(ctx context.Context, env *v1.Environment, finalizerName string) error {
	if containsString(env.Finalizers, finalizerName) {
		return nil
	}

	m.logger.V(0).Info("Adding finalizer to environment", "name", env.Name, "finalizer", finalizerName)
	env.Finalizers = append(env.Finalizers, finalizerName)
	return m.update(ctx, env)
}

// RemoveFinalizer removes a finalizer from an environment
func (m *DefaultManager) RemoveFinalizer(ctx context.Context, env *v1.Environment, finalizerName string) error {
	if !containsString(env.Finalizers, finalizerName) {
		return nil
	}

	m.logger.V(0).Info("Removing finalizer from environment", "name", env.Name, "finalizer", finalizerName)
	env.Finalizers = removeString(env.Finalizers, finalizerName)
	return m.update(ctx, env)
}

// IsBeingDeleted checks if the environment is marked for deletion
func (m *DefaultManager) IsBeingDeleted(env *v1.Environment) bool {
	return env.DeletionTimestamp != nil
}

// HasFinalizer checks if the environment has the specified finalizer
func (m *DefaultManager) HasFinalizer(env *v1.Environment, finalizerName string) bool {
	return containsString(env.Finalizers, finalizerName)
}

// Helper functions for finalizer management
func containsString(slice []string, s string) bool {
	for _, item := range slice {
		if item == s {
			return true
		}
	}
	return false
}

func removeString(slice []string, s string) []string {
	result := make([]string, 0, len(slice))
	for _, item := range slice {
		if item != s {
			result = append(result, item)
		}
	}
	return result
}
