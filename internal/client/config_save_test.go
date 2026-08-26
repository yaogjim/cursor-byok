package client

import (
	"context"
	"strings"
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

func TestSaveHomeMetricsDoesNotOverwriteOtherSections(t *testing.T) {
	service := newConfigTransferTestService(t)
	seed := serverconfig.DefaultConfig()
	seed.Gateway.Enabled = true
	seed.Gateway.Token = "live-token"
	seed.Appearance.Theme = "dark"
	seed.HomeMetrics.IncludeCacheWriteInHitRate = false
	if _, err := service.store.Save(context.Background(), seed); err != nil {
		t.Fatalf("seed Save() error = %v", err)
	}

	stale := seed
	stale.Appearance.Theme = "light"
	stale.Gateway.Token = ""
	stale.HomeMetrics.IncludeCacheWriteInHitRate = true
	if err := service.SaveHomeMetrics(stale); err != nil {
		t.Fatalf("SaveHomeMetrics() error = %v", err)
	}

	got, err := service.LoadUserConfig()
	if err != nil {
		t.Fatalf("LoadUserConfig() error = %v", err)
	}
	if !got.HomeMetrics.IncludeCacheWriteInHitRate {
		t.Fatal("home metrics section not saved")
	}
	if got.Appearance.Theme != "dark" {
		t.Fatalf("theme = %q, want dark", got.Appearance.Theme)
	}
	if got.Gateway.Token != "live-token" {
		t.Fatalf("token = %q", got.Gateway.Token)
	}
}

func TestUserConfigChangedPayloadOmitsGatewayToken(t *testing.T) {
	cfg := serverconfig.DefaultConfig()
	cfg.Gateway.Token = "event-secret-token"
	cfg.Gateway.TokenConfigured = true
	payload := serverconfig.RedactGatewayTokenForUI(cfg)
	if payload.Gateway.Token != "" {
		t.Fatalf("event payload leaked token = %q", payload.Gateway.Token)
	}
	if !payload.Gateway.TokenConfigured {
		t.Fatal("event payload dropped tokenConfigured")
	}
}

func TestSaveModelAdaptersRejectsBrokenGatewayPublicModels(t *testing.T) {
	service := newConfigTransferTestService(t)
	seed := serverconfig.DefaultConfig()
	seed.ModelAdapters = []serverconfig.ModelAdapterConfig{{
		DisplayName:     "physical",
		Type:            "openai",
		BaseURL:         "https://api.example.com/v1",
		APIKey:          "test-key",
		TooltipData:     "physical",
		ModelID:         "physical",
		ReasoningEffort: "medium",
		OpenAIEndpoint:  "/v1/responses",
	}}
	saved, err := service.store.Save(context.Background(), seed)
	if err != nil {
		t.Fatalf("seed Save() error = %v", err)
	}
	gateway := saved.Gateway
	gateway.PublicModels = []serverconfig.GatewayPublicModel{{
		ID:              "public-a",
		TargetAdapterID: saved.ModelAdapters[0].ID,
	}}
	if _, err := service.store.SaveGatewayConfig(context.Background(), gateway); err != nil {
		t.Fatalf("SaveGatewayConfig() error = %v", err)
	}

	cleared := saved
	cleared.ModelAdapters = []serverconfig.ModelAdapterConfig{}
	if err := service.SaveModelAdapters(cleared); err == nil || !strings.Contains(err.Error(), "公开模型") {
		t.Fatalf("SaveModelAdapters() error = %v", err)
	}
}
