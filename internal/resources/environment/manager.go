package environment

import (
	"context"
	"fmt"

	"github.com/go-logr/logr"
	v1 "github.com/quix-analytics/quix-environment-operator/api/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/util/retry"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/log"
)

// DefaultManager implements the Manager interface
type DefaultManager struct {
	client    client.Client
	apiReader client.Reader
	logger    logr.Logger
}

// NewManager creates a new instance of the DefaultManager
func NewManager(client client.Client, apiReader client.Reader) *DefaultManager {
	return &DefaultManager{
		client:    client,
		apiReader: apiReader,
		logger:    log.Log.WithName("environment-manager"),
	}
}

// Get retrieves an environment by name
func (m *DefaultManager) Get(ctx context.Context, name string) (*v1.Environment, error) {
	env := &v1.Environment{}
	key := types.NamespacedName{Name: name}

	// For cluster-scoped resources, always use empty namespace
	err := m.client.Get(ctx, key, env)
	if err == nil {
		return env, nil
	}

	// If that fails, log a debug message and try a direct API call
	m.logger.V(1).Info("Failed to get environment with empty namespace, attempting fallback",
		"name", name, "error", err)

	/// Use direct API call (bypasses cache)
	if err := m.apiReader.Get(ctx, key, env); err != nil {
		return nil, fmt.Errorf("failed to get Environment %q: %w", name, err)
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

// UpdateStatus updates the status of an environment. On a conflict it re-fetches the latest
// object, re-applies the caller's intended status, and retries, so a concurrent modification
// does not fail the write outright. The caller's env is kept current (ResourceVersion and
// Status) so subsequent writes in the same reconcile continue to work.
func (m *DefaultManager) UpdateStatus(ctx context.Context, env *v1.Environment) error {
	m.logger.V(0).Info("Updating environment status", "name", env.Name, "phase", env.Status.Phase)
	desiredStatus := env.Status.DeepCopy()
	if err := retry.RetryOnConflict(retry.DefaultRetry, func() error {
		latest := &v1.Environment{}
		if err := m.client.Get(ctx, types.NamespacedName{Name: env.Name}, latest); err != nil {
			return err
		}
		desiredStatus.DeepCopyInto(&latest.Status)
		if err := m.client.Status().Update(ctx, latest); err != nil {
			return err
		}
		// Advance only the caller's ResourceVersion so a subsequent write in the same
		// reconcile is current. The caller's Status already holds the intended values
		// (desiredStatus was copied from it), so leaving its Status pointers untouched keeps
		// their identity stable for any code still referencing them.
		env.ResourceVersion = latest.ResourceVersion
		return nil
	}); err != nil {
		return fmt.Errorf("failed to update status for Environment %q: %w", env.Name, err)
	}
	return nil
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

// RemoveFinalizer removes a finalizer from an environment.
//
// Returning nil when the finalizer is absent (the no-op path below) is safe because the
// reconciler now adds the finalizer at the very top of Reconcile — before any status
// mutation and before any managed resource (namespace, RoleBinding) is created. An object
// reaching deletion therefore always carries the finalizer when managed resources exist, so
// a missing finalizer here means there is nothing left to clean up. This keeps deletion
// reconciles idempotent without risking orphaned resources.
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
