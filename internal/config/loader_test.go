package config

import (
	"strings"
	"testing"
	"time"
)

func clearConfigEnv(t *testing.T) {
	t.Helper()
	for _, key := range []string{
		"MAX_CONCURRENT_RECONCILES",
		"ENVIRONMENT_REGEX",
		"CACHE_SYNC_PERIOD",
		"NAMESPACE_SUFFIX",
		"SERVICE_ACCOUNT_NAME",
		"SERVICE_ACCOUNT_NAMESPACE",
		"CLUSTER_ROLE_NAME",
	} {
		t.Setenv(key, "")
	}
}

func TestLoadConfigValidatesEnvironmentRegex(t *testing.T) {
	l := NewEnvConfigLoader()

	t.Run("invalid regex returns an error naming ENVIRONMENT_REGEX", func(t *testing.T) {
		clearConfigEnv(t)
		t.Setenv("ENVIRONMENT_REGEX", "(?P<invalid")
		_, err := l.LoadConfig()
		if err == nil {
			t.Fatalf("expected an error for an invalid ENVIRONMENT_REGEX")
		}
		if !strings.Contains(err.Error(), "ENVIRONMENT_REGEX") {
			t.Fatalf("error should name ENVIRONMENT_REGEX, got: %v", err)
		}
	})

	t.Run("valid regex loads successfully", func(t *testing.T) {
		clearConfigEnv(t)
		t.Setenv("ENVIRONMENT_REGEX", "^[a-z0-9-]+$")
		cfg, err := l.LoadConfig()
		if err != nil {
			t.Fatalf("valid regex should load, got: %v", err)
		}
		if cfg.EnvironmentRegex != "^[a-z0-9-]+$" {
			t.Fatalf("EnvironmentRegex = %q", cfg.EnvironmentRegex)
		}
	})

	t.Run("empty regex loads successfully", func(t *testing.T) {
		clearConfigEnv(t)
		t.Setenv("ENVIRONMENT_REGEX", "")
		cfg, err := l.LoadConfig()
		if err != nil {
			t.Fatalf("empty regex should load, got: %v", err)
		}
		if cfg.EnvironmentRegex != "" {
			t.Fatalf("EnvironmentRegex = %q, want empty", cfg.EnvironmentRegex)
		}
	})
}

func TestLoadConfigNumericParseFallbacks(t *testing.T) {
	l := NewEnvConfigLoader()

	t.Run("invalid MAX_CONCURRENT_RECONCILES falls back to 5", func(t *testing.T) {
		clearConfigEnv(t)
		t.Setenv("MAX_CONCURRENT_RECONCILES", "nope")
		cfg, err := l.LoadConfig()
		if err != nil {
			t.Fatalf("LoadConfig returned error: %v", err)
		}
		if cfg.MaxConcurrentReconciles != 5 {
			t.Fatalf("MaxConcurrentReconciles = %d, want 5", cfg.MaxConcurrentReconciles)
		}
	})

	t.Run("valid MAX_CONCURRENT_RECONCILES is parsed", func(t *testing.T) {
		clearConfigEnv(t)
		t.Setenv("MAX_CONCURRENT_RECONCILES", "8")
		cfg, err := l.LoadConfig()
		if err != nil {
			t.Fatalf("LoadConfig returned error: %v", err)
		}
		if cfg.MaxConcurrentReconciles != 8 {
			t.Fatalf("MaxConcurrentReconciles = %d, want 8", cfg.MaxConcurrentReconciles)
		}
	})

	t.Run("unset uses default", func(t *testing.T) {
		clearConfigEnv(t)
		cfg, err := l.LoadConfig()
		if err != nil {
			t.Fatalf("LoadConfig returned error: %v", err)
		}
		if cfg.MaxConcurrentReconciles != 5 {
			t.Fatalf("MaxConcurrentReconciles = %d, want 5", cfg.MaxConcurrentReconciles)
		}
	})
}

func TestLoadConfigCacheSyncPeriod(t *testing.T) {
	l := NewEnvConfigLoader()

	t.Run("unset uses default", func(t *testing.T) {
		clearConfigEnv(t)
		cfg, err := l.LoadConfig()
		if err != nil {
			t.Fatalf("unset CACHE_SYNC_PERIOD should load, got: %v", err)
		}
		if cfg.CacheSyncPeriod != 10*time.Minute {
			t.Fatalf("CacheSyncPeriod = %s, want 10m", cfg.CacheSyncPeriod)
		}
	})

	t.Run("empty uses default", func(t *testing.T) {
		clearConfigEnv(t)
		t.Setenv("CACHE_SYNC_PERIOD", "")
		cfg, err := l.LoadConfig()
		if err != nil {
			t.Fatalf("empty CACHE_SYNC_PERIOD should load, got: %v", err)
		}
		if cfg.CacheSyncPeriod != 10*time.Minute {
			t.Fatalf("CacheSyncPeriod = %s, want 10m", cfg.CacheSyncPeriod)
		}
	})

	t.Run("whitespace uses default", func(t *testing.T) {
		clearConfigEnv(t)
		t.Setenv("CACHE_SYNC_PERIOD", "   ")
		cfg, err := l.LoadConfig()
		if err != nil {
			t.Fatalf("whitespace CACHE_SYNC_PERIOD should load, got: %v", err)
		}
		if cfg.CacheSyncPeriod != 10*time.Minute {
			t.Fatalf("CacheSyncPeriod = %s, want 10m", cfg.CacheSyncPeriod)
		}
	})

	t.Run("valid duration is parsed", func(t *testing.T) {
		clearConfigEnv(t)
		t.Setenv("CACHE_SYNC_PERIOD", "1h")
		cfg, err := l.LoadConfig()
		if err != nil {
			t.Fatalf("valid CACHE_SYNC_PERIOD should load, got: %v", err)
		}
		if cfg.CacheSyncPeriod != time.Hour {
			t.Fatalf("CacheSyncPeriod = %s, want 1h", cfg.CacheSyncPeriod)
		}
	})

	t.Run("invalid duration returns an error", func(t *testing.T) {
		clearConfigEnv(t)
		t.Setenv("CACHE_SYNC_PERIOD", "not-a-duration")
		_, err := l.LoadConfig()
		if err == nil {
			t.Fatalf("expected an error for invalid CACHE_SYNC_PERIOD")
		}
		if !strings.Contains(err.Error(), "CACHE_SYNC_PERIOD") {
			t.Fatalf("error should name CACHE_SYNC_PERIOD, got: %v", err)
		}
	})
}
