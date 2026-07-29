package config

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestNormalizeConfigMigratesLegacyLogFlag(t *testing.T) {
	legacyFull := true
	full, err := NormalizeConfig(Config{LegacyLog: &legacyFull})
	if err != nil {
		t.Fatalf("normalize legacy full config: %v", err)
	}
	if full.Observability.Mode != ObservabilityModeFull {
		t.Fatalf("expected legacy log=true to map to full, got %+v", full.Observability)
	}

	legacyOff := false
	off, err := NormalizeConfig(Config{LegacyLog: &legacyOff})
	if err != nil {
		t.Fatalf("normalize legacy off config: %v", err)
	}
	if off.Observability.Mode != ObservabilityModeOff {
		t.Fatalf("expected legacy log=false to map to off, got %+v", off.Observability)
	}
}

func TestNormalizeConfigPrefersStructuredObservability(t *testing.T) {
	legacyFull := true
	config, err := NormalizeConfig(Config{
		LegacyLog: &legacyFull,
		Observability: ObservabilityConfig{
			Mode: ObservabilityModeBasic,
		},
	})
	if err != nil {
		t.Fatalf("normalize structured config: %v", err)
	}
	if config.Observability.Mode != ObservabilityModeBasic {
		t.Fatalf("expected basic mode, got %q", config.Observability.Mode)
	}
}

func TestNormalizeConfigRejectsInvalidObservabilityMode(t *testing.T) {
	_, err := NormalizeConfig(Config{
		Observability: ObservabilityConfig{Mode: "verbose"},
	})
	if err == nil {
		t.Fatal("expected invalid observability mode error")
	}
}

func TestStorePersistsStructuredObservabilityPrecedence(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "config.yaml")
	configYAML := "log: true\nobservability:\n    mode: basic\nbackendListenAddr: 127.0.0.1:18090\nproxyListenAddr: 127.0.0.1:18080\n"
	if err := os.WriteFile(path, []byte(configYAML), 0o644); err != nil {
		t.Fatalf("write structured config: %v", err)
	}
	store := NewStore(path, filepath.Join(root, "logs"))
	config, err := store.Load(context.Background())
	if err != nil {
		t.Fatalf("load structured config: %v", err)
	}
	if config.Observability.Mode != ObservabilityModeBasic {
		t.Fatalf("expected structured basic mode to override legacy flag, got %+v", config)
	}
	persisted, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read normalized structured config: %v", err)
	}
	if strings.Contains(string(persisted), "log:") {
		t.Fatalf("legacy log flag was not removed:\n%s", persisted)
	}
}

func TestStorePersistsMigratedObservabilityConfig(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "config.yaml")
	legacy := "log: true\nbackendListenAddr: 127.0.0.1:18090\nproxyListenAddr: 127.0.0.1:18080\n"
	if err := os.WriteFile(path, []byte(legacy), 0o644); err != nil {
		t.Fatalf("write legacy config: %v", err)
	}
	store := NewStore(path, filepath.Join(root, "logs"))
	config, err := store.Load(context.Background())
	if err != nil {
		t.Fatalf("load legacy config: %v", err)
	}
	if config.Observability.Mode != ObservabilityModeFull {
		t.Fatalf("expected migrated full mode, got %q", config.Observability.Mode)
	}
	persisted, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read migrated config: %v", err)
	}
	if !strings.Contains(string(persisted), "observability:\n    mode: full") {
		t.Fatalf("migrated config does not contain structured observability mode:\n%s", persisted)
	}
}
