package config

import (
	"fmt"
	"os"
	"regexp"
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
	maxConcurrentReconciles, err := strconv.Atoi(getEnvOrDefault("MAX_CONCURRENT_RECONCILES", "5"))
	if err != nil {
		maxConcurrentReconciles = 5 // Default to 5 if invalid
	}

	// Validate the environment regex at startup so a malformed pattern fails fast with a clear
	// error instead of only surfacing on the first reconcile. An empty regex means "all IDs
	// valid" and is intentionally left unvalidated.
	environmentRegex := getEnvOrDefault("ENVIRONMENT_REGEX", "")
	if environmentRegex != "" {
		if _, err := regexp.Compile(environmentRegex); err != nil {
			return nil, fmt.Errorf("invalid ENVIRONMENT_REGEX %q: %w", environmentRegex, err)
		}
	}

	// Get cache sync period from env or use default (10 minutes)
	cacheSyncPeriodStr := getEnvOrDefault("CACHE_SYNC_PERIOD", "10m")
	cacheSyncPeriod, err := time.ParseDuration(cacheSyncPeriodStr)
	if err != nil {
		return nil, fmt.Errorf("invalid CACHE_SYNC_PERIOD %q: %w", cacheSyncPeriodStr, err)
	}

	return &OperatorConfig{
		NamespaceSuffix:         getEnvOrDefault("NAMESPACE_SUFFIX", "-qdep"),
		EnvironmentRegex:        environmentRegex,
		ServiceAccountName:      getEnvOrDefault("SERVICE_ACCOUNT_NAME", "quix-platform-account"),
		ServiceAccountNamespace: getEnvOrDefault("SERVICE_ACCOUNT_NAMESPACE", "quix-environment"),
		ClusterRoleName:         getEnvOrDefault("CLUSTER_ROLE_NAME", "quix-platform-account-role"),
		MaxConcurrentReconciles: maxConcurrentReconciles,
		CacheSyncPeriod:         cacheSyncPeriod,
	}, nil
}

// getEnvOrDefault returns the value of the environment variable or the default value
func getEnvOrDefault(key, defaultValue string) string {
	value := strings.TrimSpace(os.Getenv(key))
	if value == "" {
		return defaultValue
	}
	return value
}
