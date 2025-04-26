package namespaces

import (
	"context"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/errors"

	quixiov1 "github.com/quix-analytics/quix-environment-operator/api/v1"
)

// MockNamespaceManager implements NamespaceManager for tests
type MockNamespaceManager struct {
	// Functions that can be set by tests to override behavior
	ApplyMetadataFunc      func(env *quixiov1.Environment, namespace *corev1.Namespace) bool
	UpdateMetadataFunc     func(ctx context.Context, env *quixiov1.Environment, namespace *corev1.Namespace) error
	IsNamespaceDeletedFunc func(namespace *corev1.Namespace, err error) bool
	IsNamespaceManagedFunc func(namespace *corev1.Namespace) bool
	GetNamespaceFunc       func(ctx context.Context, name string) (*corev1.Namespace, error)
	CreateNamespaceFunc    func(ctx context.Context, env *quixiov1.Environment, name string) error

	// Embed default implementation for non-mocked methods
	DefaultManager *DefaultNamespaceManager
}

// NewMockNamespaceManager creates a new mock namespace manager with default behavior
func NewMockNamespaceManager(defaultManager *DefaultNamespaceManager) *MockNamespaceManager {
	return &MockNamespaceManager{
		DefaultManager: defaultManager,
	}
}

// CreateNamespace uses the mock function if provided, or falls back to default behavior
func (m *MockNamespaceManager) CreateNamespace(ctx context.Context, env *quixiov1.Environment, name string) error {
	if m.CreateNamespaceFunc != nil {
		return m.CreateNamespaceFunc(ctx, env, name)
	}
	if m.DefaultManager != nil {
		return m.DefaultManager.CreateNamespace(ctx, env, name)
	}
	// Default implementation if no other behavior is specified
	return nil
}

// GetNamespace uses the mock function if provided, or falls back to default behavior
func (m *MockNamespaceManager) GetNamespace(ctx context.Context, name string) (*corev1.Namespace, error) {
	if m.GetNamespaceFunc != nil {
		return m.GetNamespaceFunc(ctx, name)
	}
	if m.DefaultManager != nil {
		return m.DefaultManager.GetNamespace(ctx, name)
	}
	// Default implementation if no other behavior is specified
	return nil, errors.NewNotFound(corev1.Resource("namespaces"), name)
}

// ApplyMetadata uses the mock function if provided, or falls back to default behavior
func (m *MockNamespaceManager) ApplyMetadata(env *quixiov1.Environment, namespace *corev1.Namespace) bool {
	if m.ApplyMetadataFunc != nil {
		return m.ApplyMetadataFunc(env, namespace)
	}
	if m.DefaultManager != nil {
		return m.DefaultManager.ApplyMetadata(env, namespace)
	}
	// Default implementation if no other behavior is specified
	return false
}

// UpdateMetadata uses the mock function if provided, or falls back to default behavior
func (m *MockNamespaceManager) UpdateMetadata(ctx context.Context, env *quixiov1.Environment, namespace *corev1.Namespace) error {
	if m.UpdateMetadataFunc != nil {
		return m.UpdateMetadataFunc(ctx, env, namespace)
	}
	if m.DefaultManager != nil {
		return m.DefaultManager.UpdateMetadata(ctx, env, namespace)
	}
	// Default implementation if no other behavior is specified
	return nil
}

// IsNamespaceDeleted checks if a namespace should be considered deleted
func (m *MockNamespaceManager) IsNamespaceDeleted(namespace *corev1.Namespace, err error) bool {
	if m.IsNamespaceDeletedFunc != nil {
		return m.IsNamespaceDeletedFunc(namespace, err)
	}
	// Default implementation if no other behavior is specified
	if err != nil {
		return errors.IsNotFound(err)
	}
	// Also check the DeletionTimestamp in the default fallback
	return namespace == nil || !namespace.DeletionTimestamp.IsZero()
}

// IsNamespaceManaged uses the mock function if provided, or falls back to default behavior
func (m *MockNamespaceManager) IsNamespaceManaged(namespace *corev1.Namespace) bool {
	if m.IsNamespaceManagedFunc != nil {
		return m.IsNamespaceManagedFunc(namespace)
	}
	if m.DefaultManager != nil {
		return m.DefaultManager.IsNamespaceManaged(namespace)
	}
	// Default implementation if no other behavior is specified
	return false
}
