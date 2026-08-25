package backend

import (
	"context"
	"net/http"
	"path/filepath"
	"testing"

	serverconfig "cursor/internal/backend/server/config"
)

func TestHostSaveConfigPreservesLatestHash(t *testing.T) {
	manager := newHostConfigTestManager(t)
	seed := DefaultHostTestConfig(t, manager, func(cfg *serverconfig.Config) {
		cfg.LastAgentModelHash = "old-hash"
		cfg.Appearance.Theme = "light"
	})
	if err := manager.SaveLastAgentModelHash(context.Background(), "live-hash"); err != nil {
		t.Fatalf("SaveLastAgentModelHash() error = %v", err)
	}

	host := &Host{configs: manager, httpServer: &http.Server{}}
	stale := seed
	stale.LastAgentModelHash = "stale-ui-hash"
	stale.Appearance.Theme = "dark"
	got, err := host.SaveConfig(context.Background(), stale)
	if err != nil {
		t.Fatalf("SaveConfig() error = %v", err)
	}
	if got.LastAgentModelHash != "live-hash" {
		t.Fatalf("hash = %q, want live-hash", got.LastAgentModelHash)
	}
	if got.Appearance.Theme != "dark" {
		t.Fatalf("theme = %q, want dark", got.Appearance.Theme)
	}
}

func TestHostReplaceConfigReplacesHash(t *testing.T) {
	manager := newHostConfigTestManager(t)
	_ = DefaultHostTestConfig(t, manager, func(cfg *serverconfig.Config) {
		cfg.LastAgentModelHash = "live-hash"
		cfg.Appearance.Theme = "light"
	})

	host := &Host{configs: manager, httpServer: &http.Server{}}
	replacement := serverconfig.DefaultConfig()
	replacement.LastAgentModelHash = "imported-hash"
	replacement.Appearance.Theme = "dark"
	got, err := host.ReplaceConfig(context.Background(), replacement)
	if err != nil {
		t.Fatalf("ReplaceConfig() error = %v", err)
	}
	if got.LastAgentModelHash != "imported-hash" {
		t.Fatalf("hash = %q, want imported-hash", got.LastAgentModelHash)
	}
	if manager.Current().LastAgentModelHash != "imported-hash" || manager.Current().Appearance.Theme != "dark" {
		t.Fatalf("Current() = %+v", manager.Current())
	}
}

func newHostConfigTestManager(t *testing.T) *serverconfig.Manager {
	t.Helper()
	root := t.TempDir()
	store := serverconfig.NewStore(filepath.Join(root, "config.yaml"), filepath.Join(root, "logs"))
	manager, err := serverconfig.NewManager(context.Background(), store)
	if err != nil {
		t.Fatalf("NewManager() error = %v", err)
	}
	return manager
}

func DefaultHostTestConfig(t *testing.T, manager *serverconfig.Manager, mutate func(*serverconfig.Config)) serverconfig.Config {
	t.Helper()
	cfg := serverconfig.DefaultConfig()
	if mutate != nil {
		mutate(&cfg)
	}
	saved, err := manager.Save(context.Background(), cfg)
	if err != nil {
		t.Fatalf("seed Save() error = %v", err)
	}
	return saved
}
