package config

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestDefaultConfigUsesSafeClientPreferences(t *testing.T) {
	cfg := DefaultConfig()

	if cfg.Appearance.Theme != "light" {
		t.Fatalf("default theme = %q, want light", cfg.Appearance.Theme)
	}
	if cfg.Advertising.Enabled {
		t.Fatal("advertising must be disabled by default")
	}
	if cfg.Updates.CheckOnStartup {
		t.Fatal("startup update checks must be disabled by default")
	}
}

func TestNormalizeConfigNormalizesThemeAndPreservesPreferences(t *testing.T) {
	input := DefaultConfig()
	input.Appearance.Theme = " DARK "
	input.Advertising.Enabled = true
	input.Updates.CheckOnStartup = true

	normalized, err := NormalizeConfig(input)
	if err != nil {
		t.Fatalf("NormalizeConfig() error = %v", err)
	}
	if normalized.Appearance.Theme != "dark" {
		t.Fatalf("normalized theme = %q, want dark", normalized.Appearance.Theme)
	}
	if !normalized.Advertising.Enabled || !normalized.Updates.CheckOnStartup {
		t.Fatal("client preferences were not preserved")
	}

	input.Appearance.Theme = "unsupported"
	normalized, err = NormalizeConfig(input)
	if err != nil {
		t.Fatalf("NormalizeConfig() invalid theme error = %v", err)
	}
	if normalized.Appearance.Theme != DefaultTheme {
		t.Fatalf("invalid theme normalized to %q, want %q", normalized.Appearance.Theme, DefaultTheme)
	}
}

func TestStoreLoadMigratesMissingClientPreferences(t *testing.T) {
	root := t.TempDir()
	configPath := filepath.Join(root, "config.yaml")
	legacy := strings.Join([]string{
		"log: false",
		"providerStreamIdleTimeout: 240",
		"backendListenAddr: 127.0.0.1:18090",
		"proxyListenAddr: 127.0.0.1:18080",
		"modelAdapters: []",
		"routing:",
		"  mode: local",
		"homeMetrics:",
		"  includeCacheWriteInHitRate: false",
		"lastAgentModelHash: ''",
		"",
	}, "\n")
	if err := os.WriteFile(configPath, []byte(legacy), 0o600); err != nil {
		t.Fatalf("write legacy config: %v", err)
	}

	store := NewStore(configPath, filepath.Join(root, "logs"))
	cfg, err := store.Load(context.Background())
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if cfg.Appearance.Theme != "light" || cfg.Advertising.Enabled || cfg.Updates.CheckOnStartup {
		t.Fatalf("unexpected migrated preferences: %+v", cfg)
	}

	persisted, err := os.ReadFile(configPath)
	if err != nil {
		t.Fatalf("read migrated config: %v", err)
	}
	for _, key := range []string{"appearance:", "advertising:", "updates:"} {
		if !strings.Contains(string(persisted), key) {
			t.Fatalf("migrated config missing %q:\n%s", key, persisted)
		}
	}
}
