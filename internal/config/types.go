package config

import "time"

// OperatorConfig holds the configuration for the operator
type OperatorConfig struct {
	// NamespaceSuffix is the suffix to use for creating namespaces
	NamespaceSuffix string `validate:"required,max=10"`

	// EnvironmentRegex is the regex pattern that environment IDs must match
	EnvironmentRegex string `validate:"omitempty"`

	// ServiceAccountName is the name of the ServiceAccount to bind to RoleBindings
	ServiceAccountName string `validate:"required"`

	// ServiceAccountNamespace is the namespace of the ServiceAccount
	ServiceAccountNamespace string `validate:"required"`

	// ClusterRoleName is the name of the ClusterRole to bind to the ServiceAccount
	ClusterRoleName string `validate:"required"`

	// ReconcileInterval is the duration between reconciliation attempts
	ReconcileInterval time.Duration `validate:"min=5s"`

	// MaxConcurrentReconciles is the maximum number of concurrent reconciliations
	MaxConcurrentReconciles int `validate:"min=1,max=100"`
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

func (c *OperatorConfig) GetReconcileInterval() int64 {
	return int64(c.ReconcileInterval.Seconds())
}
