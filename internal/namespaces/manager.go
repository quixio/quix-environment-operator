package namespaces

import (
	"context"

	corev1 "k8s.io/api/core/v1"

	quixiov1 "github.com/quix-analytics/quix-environment-operator/api/v1"
)

// NamespaceManager defines operations for managing namespace metadata
type NamespaceManager interface {
	// ApplyMetadata applies standard labels and annotations to a namespace
	// Returns true if changes were made
	ApplyMetadata(env *quixiov1.Environment, namespace *corev1.Namespace) bool

	// UpdateMetadata ensures namespace metadata is up to date with environment spec
	// Returns error if the update fails
	UpdateMetadata(ctx context.Context, env *quixiov1.Environment, namespace *corev1.Namespace) error

	// IsNamespaceDeleted checks if a namespace should be considered deleted
	// Returns true if deleted
	IsNamespaceDeleted(namespace *corev1.Namespace, err error) bool

	// IsNamespaceManaged checks if a namespace is managed by this operator based on labels
	IsNamespaceManaged(namespace *corev1.Namespace) bool
}
