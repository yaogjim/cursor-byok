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

	if cfg.Observability != (ObservabilityConfig{
		Mode:          DefaultObservabilityMode,
		RetentionDays: DefaultObservabilityRetentionDays,
		MaxDiskMB:     DefaultObservabilityMaxDiskMB,
	}) {
		t.Fatalf("default observability = %+v", cfg.Observability)
	}
	if cfg.LegacyLog != nil {
		t.Fatal("default config must not retain the legacy log field")
	}
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

func TestNormalizeConfigMigratesAndBoundsObservability(t *testing.T) {
	legacyEnabled := true
	input := DefaultConfig()
	input.Observability = ObservabilityConfig{}
	input.LegacyLog = &legacyEnabled

	normalized, err := NormalizeConfig(input)
	if err != nil {
		t.Fatalf("NormalizeConfig() legacy error = %v", err)
	}
	if normalized.Observability.Mode != "full" {
		t.Fatalf("legacy log=true mode = %q, want full", normalized.Observability.Mode)
	}
	if normalized.LegacyLog != nil {
		t.Fatal("normalized config retained legacy log field")
	}

	input.Observability = ObservabilityConfig{
		Mode:          " unsupported ",
		RetentionDays: MinObservabilityRetentionDays - 1,
		MaxDiskMB:     MaxObservabilityMaxDiskMB + 1,
	}
	normalized, err = NormalizeConfig(input)
	if err != nil {
		t.Fatalf("NormalizeConfig() observability error = %v", err)
	}
	if normalized.Observability.Mode != "basic" {
		t.Fatalf("explicit invalid mode = %q, want basic", normalized.Observability.Mode)
	}
	if normalized.Observability.RetentionDays != DefaultObservabilityRetentionDays {
		t.Fatalf("retention days = %d, want default %d", normalized.Observability.RetentionDays, DefaultObservabilityRetentionDays)
	}
	if normalized.Observability.MaxDiskMB != MaxObservabilityMaxDiskMB {
		t.Fatalf("max disk MB = %d, want max %d", normalized.Observability.MaxDiskMB, MaxObservabilityMaxDiskMB)
	}

	input.LegacyLog = nil
	input.Observability = ObservabilityConfig{
		Mode:          " FULL ",
		RetentionDays: MaxObservabilityRetentionDays + 1,
		MaxDiskMB:     MinObservabilityMaxDiskMB - 1,
	}
	normalized, err = NormalizeConfig(input)
	if err != nil {
		t.Fatalf("NormalizeConfig() full error = %v", err)
	}
	if normalized.Observability != (ObservabilityConfig{
		Mode:          "full",
		RetentionDays: MaxObservabilityRetentionDays,
		MaxDiskMB:     MinObservabilityMaxDiskMB,
	}) {
		t.Fatalf("bounded full observability = %+v", normalized.Observability)
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
	if cfg.Observability.Mode != "basic" {
		t.Fatalf("legacy log=false mode = %q, want basic", cfg.Observability.Mode)
	}
	if cfg.Appearance.Theme != "light" || cfg.Advertising.Enabled || cfg.Updates.CheckOnStartup {
		t.Fatalf("unexpected migrated preferences: %+v", cfg)
	}

	persisted, err := os.ReadFile(configPath)
	if err != nil {
		t.Fatalf("read migrated config: %v", err)
	}
	for _, key := range []string{"observability:", "appearance:", "advertising:", "updates:"} {
		if !strings.Contains(string(persisted), key) {
			t.Fatalf("migrated config missing %q:\n%s", key, persisted)
		}
	}
	if strings.Contains(string(persisted), "\nlog:") || strings.HasPrefix(string(persisted), "log:") {
		t.Fatalf("migrated config retained legacy log field:\n%s", persisted)
	}
}

func TestStoreLoadMigratesLegacyEnabledLogToFull(t *testing.T) {
	root := t.TempDir()
	configPath := filepath.Join(root, "config.yaml")
	legacy := strings.Join([]string{
		"log: true",
		"backendListenAddr: 127.0.0.1:18090",
		"proxyListenAddr: 127.0.0.1:18080",
		"modelAdapters: []",
		"routing:",
		"  mode: local",
		"appearance:",
		"  theme: light",
		"advertising:",
		"  enabled: false",
		"updates:",
		"  checkOnStartup: false",
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
	if cfg.Observability.Mode != "full" {
		t.Fatalf("legacy log=true mode = %q, want full", cfg.Observability.Mode)
	}

	persisted, err := os.ReadFile(configPath)
	if err != nil {
		t.Fatalf("read migrated config: %v", err)
	}
	text := string(persisted)
	if !strings.Contains(text, "observability:\n    mode: full") {
		t.Fatalf("migrated config missing full observability:\n%s", persisted)
	}
	if strings.Contains(text, "\nlog:") || strings.HasPrefix(text, "log:") {
		t.Fatalf("migrated config retained legacy log field:\n%s", persisted)
	}
}
