package config

import (
	"os"
	"strconv"
	"strings"
	"time"
)

// EnvConfigLoader loads configuration from environment variables
type EnvConfigLoader struct{}

// NewEnvConfigLoader creates a new environment variable configuration loader
func NewEnvConfigLoader() *EnvConfigLoader {
	return &EnvConfigLoader{}
}

// LoadConfig loads the operator configuration from environment variables
func (l *EnvConfigLoader) LoadConfig() (*OperatorConfig, error) {
	reconcileInterval, _ := strconv.ParseInt(getEnvOrDefault("RECONCILE_INTERVAL_SECONDS", "60"), 10, 64)
	maxConcurrentReconciles, _ := strconv.Atoi(getEnvOrDefault("MAX_CONCURRENT_RECONCILES", "5"))

	return &OperatorConfig{
		NamespaceSuffix:         getEnvOrDefault("NAMESPACE_SUFFIX", "-qenv"),
		EnvironmentRegex:        getEnvOrDefault("ENVIRONMENT_REGEX", ""),
		ServiceAccountName:      getEnvOrDefault("SERVICE_ACCOUNT_NAME", "quix-environment-user"),
		ServiceAccountNamespace: getEnvOrDefault("SERVICE_ACCOUNT_NAMESPACE", "quix-environment"),
		ClusterRoleName:         getEnvOrDefault("CLUSTER_ROLE_NAME", "quix-environment-user-role"),
		ReconcileInterval:       time.Duration(reconcileInterval) * time.Second,
		MaxConcurrentReconciles: maxConcurrentReconciles,
	}, nil
}

// getEnvOrDefault returns the value of the environment variable or the default value
func getEnvOrDefault(key, defaultValue string) string {
	value := os.Getenv(key)
	if value == "" {
		return defaultValue
	}
	return strings.TrimSpace(value)
}
