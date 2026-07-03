package config

import "time"

// OperatorConfig holds the configuration for the operator
type OperatorConfig struct {
	// NamespaceSuffix is the suffix to use for creating namespaces
	NamespaceSuffix string

	// EnvironmentRegex is an OPTIONAL additional regex an environment ID must match, applied on
	// top of the CRD's built-in id pattern. Empty disables the extra runtime constraint (the CRD
	// pattern still applies).
	EnvironmentRegex string

	// ServiceAccountName is the name of the ServiceAccount to bind to RoleBindings
	ServiceAccountName string

	// ServiceAccountNamespace is the namespace of the ServiceAccount
	ServiceAccountNamespace string

	// ClusterRoleName is the name of the ClusterRole to bind to the ServiceAccount
	ClusterRoleName string

	// MaxConcurrentReconciles is the maximum number of concurrent reconciliations
	MaxConcurrentReconciles int

	// CacheSyncPeriod is the duration between cache syncs
	CacheSyncPeriod time.Duration
}

// Ensure OperatorConfig implements ConfigProvider
func (c *OperatorConfig) GetNamespaceSuffix() string {
	return c.NamespaceSuffix
}

func (c *OperatorConfig) GetEnvironmentRegex() string {
	return c.EnvironmentRegex
}

func (c *OperatorConfig) GetServiceAccountName() string {
	return c.ServiceAccountName
}

func (c *OperatorConfig) GetServiceAccountNamespace() string {
	return c.ServiceAccountNamespace
}

func (c *OperatorConfig) GetClusterRoleName() string {
	return c.ClusterRoleName
}

func (c *OperatorConfig) GetCacheSyncPeriod() time.Duration {
	return c.CacheSyncPeriod
}
