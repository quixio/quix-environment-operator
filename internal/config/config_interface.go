package config

import (
	"time"
)

// ConfigLoader defines methods for loading operator configuration
type ConfigLoader interface {
	// LoadConfig loads the operator configuration from the environment or other sources
	LoadConfig() (*OperatorConfig, error)
}

// ConfigProvider defines methods for accessing operator configuration
type ConfigProvider interface {
	// GetNamespaceSuffix returns the suffix to use for creating namespaces
	GetNamespaceSuffix() string

	// GetEnvironmentRegex returns the regex pattern that environment IDs must match
	GetEnvironmentRegex() string

	// GetServiceAccountName returns the name of the ServiceAccount to bind to RoleBindings
	GetServiceAccountName() string

	// GetServiceAccountNamespace returns the namespace of the ServiceAccount
	GetServiceAccountNamespace() string

	// GetClusterRoleName returns the name of the ClusterRole to bind to the ServiceAccount
	GetClusterRoleName() string

	// GetReconcileInterval returns the reconcile interval duration
	GetReconcileInterval() int64

	// GetCacheSyncPeriod returns the cache sync period duration
	GetCacheSyncPeriod() time.Duration
}
