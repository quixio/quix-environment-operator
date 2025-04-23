package config

import (
	"os"
	"strings"
)

// OperatorConfig holds the configuration for the operator
type OperatorConfig struct {
	// NamespaceSuffix is the suffix to use for creating namespaces
	NamespaceSuffix string

	// ServiceAccountName is the name of the ServiceAccount to bind to RoleBindings
	ServiceAccountName string

	// ServiceAccountNamespace is the namespace of the ServiceAccount
	ServiceAccountNamespace string

	// ClusterRoleName is the name of the ClusterRole to bind to the ServiceAccount
	ClusterRoleName string
}

// GetConfig loads the operator configuration from environment variables
func GetConfig() *OperatorConfig {
	return &OperatorConfig{
		NamespaceSuffix:         getEnvOrDefault("NAMESPACE_SUFFIX", "-qenv"),
		ServiceAccountName:      getEnvOrDefault("SERVICE_ACCOUNT_NAME", "quix-environment-user"),
		ServiceAccountNamespace: getEnvOrDefault("SERVICE_ACCOUNT_NAMESPACE", "quix-environment"),
		ClusterRoleName:         getEnvOrDefault("CLUSTER_ROLE_NAME", "quix-environment-user-role"),
	}
}

// getEnvOrDefault returns the value of the environment variable or the default value
func getEnvOrDefault(key, defaultValue string) string {
	value := os.Getenv(key)
	if value == "" {
		return defaultValue
	}
	return strings.TrimSpace(value)
}
