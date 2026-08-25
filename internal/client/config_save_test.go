package client

import (
	"context"
	"testing"

	serverconfig "cursor/internal/backend/server/config"
)

func TestSaveUserConfigPreservesLastAgentModelHash(t *testing.T) {
	service := newConfigTransferTestService(t)
	seed := serverconfig.DefaultConfig()
	seed.LastAgentModelHash = "live-hash"
	seed.Appearance.Theme = "light"
	if _, err := service.store.Save(context.Background(), seed); err != nil {
		t.Fatalf("seed Save() error = %v", err)
	}

	stale := seed
	stale.LastAgentModelHash = "stale-ui-hash"
	stale.Appearance.Theme = "dark"
	if err := service.SaveUserConfig(stale); err != nil {
		t.Fatalf("SaveUserConfig() error = %v", err)
	}

	got, err := service.LoadUserConfig()
	if err != nil {
		t.Fatalf("LoadUserConfig() error = %v", err)
	}
	if got.LastAgentModelHash != "live-hash" {
		t.Fatalf("hash = %q, want live-hash", got.LastAgentModelHash)
	}
	if got.Appearance.Theme != "dark" {
		t.Fatalf("theme = %q, want dark", got.Appearance.Theme)
	}
}
