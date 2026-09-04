package config

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

func TestStoreSaveUserConfigPreservesDiskHash(t *testing.T) {
	store := newWriteTestStore(t)
	seed := seedWriteTestConfig(t, store, func(cfg *Config) {
		cfg.LastAgentModelHash = "live-hash"
		cfg.Appearance.Theme = "light"
		cfg.ModelAdapters = []ModelAdapterConfig{testModelAdapter("ch-a", 1)}
	})

	stale := seed
	stale.LastAgentModelHash = "stale-ui-hash"
	stale.Appearance.Theme = "dark"
	stale.ModelAdapters = []ModelAdapterConfig{testModelAdapter("ch-b", 1)}

	got, err := store.SaveUserConfig(context.Background(), stale)
	if err != nil {
		t.Fatalf("SaveUserConfig() error = %v", err)
	}
	if got.LastAgentModelHash != "live-hash" {
		t.Fatalf("hash = %q, want live-hash", got.LastAgentModelHash)
	}
	if got.Appearance.Theme != "dark" {
		t.Fatalf("theme = %q, want dark", got.Appearance.Theme)
	}
	if len(got.ModelAdapters) != 1 || got.ModelAdapters[0].DisplayName != "ch-b" {
		t.Fatalf("adapters = %#v, want ch-b", got.ModelAdapters)
	}

	loaded, err := store.Load(context.Background())
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if loaded.LastAgentModelHash != "live-hash" || loaded.Appearance.Theme != "dark" {
		t.Fatalf("loaded = %+v", loaded)
	}
}

func TestStoreSaveLastAgentModelHashDoesNotRollbackAdaptersOrFallback(t *testing.T) {
	store := newWriteTestStore(t)
	seed := seedWriteTestConfig(t, store, func(cfg *Config) {
		cfg.LastAgentModelHash = "old-hash"
		cfg.ModelAdapters = writeTestFallbackAdapters(t)
	})
	if !seed.ModelAdapters[0].ProviderFallback.Enabled {
		t.Fatal("seed fallback was not enabled")
	}

	got, changed, err := store.SaveLastAgentModelHash(context.Background(), "new-hash")
	if err != nil {
		t.Fatalf("SaveLastAgentModelHash() error = %v", err)
	}
	if !changed {
		t.Fatal("SaveLastAgentModelHash() changed = false, want true")
	}
	assertWriteTestFallbackPreserved(t, got, seed, "new-hash")

	loaded, err := store.Load(context.Background())
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	assertWriteTestFallbackPreserved(t, loaded, seed, "new-hash")
}

func TestStoreSaveLastAgentModelHashNoopSkipsWrite(t *testing.T) {
	store := newWriteTestStore(t)
	_ = seedWriteTestConfig(t, store, func(cfg *Config) {
		cfg.LastAgentModelHash = "same-hash"
	})
	info, err := os.Stat(store.Path())
	if err != nil {
		t.Fatalf("Stat() error = %v", err)
	}

	got, changed, err := store.SaveLastAgentModelHash(context.Background(), " same-hash ")
	if err != nil {
		t.Fatalf("SaveLastAgentModelHash() error = %v", err)
	}
	if changed {
		t.Fatal("identical hash should be a no-op")
	}
	if got.LastAgentModelHash != "same-hash" {
		t.Fatalf("hash = %q, want same-hash", got.LastAgentModelHash)
	}

	after, err := os.Stat(store.Path())
	if err != nil {
		t.Fatalf("Stat() after error = %v", err)
	}
	if !after.ModTime().Equal(info.ModTime()) || after.Size() != info.Size() {
		t.Fatalf("no-op rewrote the file: before=%v/%d after=%v/%d", info.ModTime(), info.Size(), after.ModTime(), after.Size())
	}
}

func TestStoreSectionSavesDoNotOverwriteOtherPages(t *testing.T) {
	store := newWriteTestStore(t)
	seed := seedWriteTestConfig(t, store, func(cfg *Config) {
		cfg.LastAgentModelHash = "live-hash"
		cfg.Appearance.Theme = "dark"
		cfg.Advertising.Enabled = true
		cfg.Routing.Mode = "upstream"
		cfg.Observability.Mode = "basic"
		cfg.Gateway.Enabled = false
		cfg.Gateway.ListenAddr = DefaultGatewayListenAddr
		cfg.ModelAdapters = []ModelAdapterConfig{testModelAdapter("ch-a", 1)}
	})
	adapterID := seed.ModelAdapters[0].ID

	stale := seed
	stale.LastAgentModelHash = "stale-ui-hash"
	stale.Appearance.Theme = "light"
	stale.Advertising.Enabled = false
	stale.Routing.Mode = "local"
	stale.Observability.Mode = "full"
	stale.SubagentReschedule.Enabled = true
	stale.ModelAdapters = []ModelAdapterConfig{testModelAdapter("ch-b", 1)}
	stale.Gateway.Enabled = true
	stale.Gateway.Token = ""
	stale.Gateway.TokenConfigured = false
	stale.Gateway.PublicModels = []GatewayPublicModel{{ID: "grok", TargetAdapterID: adapterID}}

	got, err := store.SaveGatewayConfig(context.Background(), stale.Gateway)
	if err != nil {
		t.Fatalf("SaveGatewayConfig() error = %v", err)
	}
	if !got.Gateway.Enabled || got.Gateway.ListenAddr != DefaultGatewayListenAddr {
		t.Fatalf("gateway = %+v", got.Gateway)
	}
	if len(got.Gateway.PublicModels) != 1 || got.Gateway.PublicModels[0].ID != "grok" {
		t.Fatalf("publicModels = %#v", got.Gateway.PublicModels)
	}
	if strings.TrimSpace(got.Gateway.Token) == "" || !got.Gateway.TokenConfigured {
		t.Fatal("enabled gateway should generate a token")
	}
	if got.Appearance.Theme != "dark" || !got.Advertising.Enabled || got.Routing.Mode != "upstream" {
		t.Fatalf("gateway save overwrote other pages: %+v", got)
	}
	if got.LastAgentModelHash != "live-hash" || len(got.ModelAdapters) != 1 || got.ModelAdapters[0].DisplayName != "ch-a" {
		t.Fatalf("gateway save overwrote adapters/hash: %+v", got)
	}

	cleared := got.Gateway
	cleared.Token = ""
	cleared.TokenConfigured = false
	again, err := store.SaveGatewayConfig(context.Background(), cleared)
	if err != nil {
		t.Fatalf("SaveGatewayConfig() second error = %v", err)
	}
	if again.Gateway.Token != got.Gateway.Token {
		t.Fatalf("gateway token overwritten: %q vs %q", again.Gateway.Token, got.Gateway.Token)
	}

	cursor := stale
	cursor.Routing.Mode = "local"
	savedCursor, err := store.SaveCursorConfig(context.Background(), cursor)
	if err != nil {
		t.Fatalf("SaveCursorConfig() error = %v", err)
	}
	if savedCursor.Routing.Mode != "local" {
		t.Fatalf("cursor section not saved: %+v", savedCursor)
	}
	if savedCursor.Appearance.Theme != "dark" || !savedCursor.Gateway.Enabled || savedCursor.Gateway.Token != got.Gateway.Token {
		t.Fatalf("cursor save overwrote other pages: %+v", savedCursor)
	}

	settings := stale
	settings.Appearance.Theme = "light"
	settings.Advertising.Enabled = false
	settings.Observability.Mode = "full"
	savedSettings, err := store.SaveSystemSettings(context.Background(), settings)
	if err != nil {
		t.Fatalf("SaveSystemSettings() error = %v", err)
	}
	if savedSettings.Appearance.Theme != "light" || savedSettings.Advertising.Enabled || savedSettings.Observability.Mode != "full" || !savedSettings.SubagentReschedule.Enabled {
		t.Fatalf("settings section not saved: %+v", savedSettings)
	}
	if savedSettings.Routing.Mode != "local" || !savedSettings.Gateway.Enabled || savedSettings.Gateway.Token != got.Gateway.Token {
		t.Fatalf("settings save overwrote other pages: %+v", savedSettings)
	}

	emptyCursor := DefaultConfig()
	emptyCursor.Routing.Mode = "upstream"
	emptyCursor.BackendListenAddr = ""
	emptyCursor.ProxyListenAddr = ""
	preservedCursor, err := store.SaveCursorConfig(context.Background(), emptyCursor)
	if err != nil {
		t.Fatalf("SaveCursorConfig() empty payload error = %v", err)
	}
	if preservedCursor.Routing.Mode != "upstream" {
		t.Fatalf("empty cursor payload lost routing: %+v", preservedCursor)
	}
	if preservedCursor.BackendListenAddr != seed.BackendListenAddr || preservedCursor.ProxyListenAddr != seed.ProxyListenAddr {
		t.Fatalf("empty cursor payload overwrote listen addrs: %+v", preservedCursor)
	}

	adapters := append(append([]ModelAdapterConfig{}, savedSettings.ModelAdapters...), testModelAdapter("ch-c", 2))
	savedAdapters, err := store.SaveModelAdapters(context.Background(), adapters)
	if err != nil {
		t.Fatalf("SaveModelAdapters() error = %v", err)
	}
	if len(savedAdapters.ModelAdapters) != 2 || savedAdapters.ModelAdapters[1].DisplayName != "ch-c" {
		t.Fatalf("adapters section not saved: %+v", savedAdapters.ModelAdapters)
	}
	if savedAdapters.Appearance.Theme != "light" || savedAdapters.Routing.Mode != "upstream" || !savedAdapters.Gateway.Enabled {
		t.Fatalf("adapter save overwrote other pages: %+v", savedAdapters)
	}
}

func TestStoreSectionSavePreservesSystemTheme(t *testing.T) {
	store := newWriteTestStore(t)
	seed := seedWriteTestConfig(t, store, func(cfg *Config) {
		cfg.Appearance.Theme = "system"
		cfg.Advertising.Enabled = true
		cfg.Routing.Mode = "upstream"
	})
	if seed.Appearance.Theme != "system" {
		t.Fatalf("seed theme = %q, want system", seed.Appearance.Theme)
	}

	cursor := seed
	cursor.Appearance.Theme = "dark"
	cursor.Routing.Mode = "local"
	savedCursor, err := store.SaveCursorConfig(context.Background(), cursor)
	if err != nil {
		t.Fatalf("SaveCursorConfig() error = %v", err)
	}
	if savedCursor.Appearance.Theme != "system" || !savedCursor.Advertising.Enabled {
		t.Fatalf("cursor save overwrote settings: %+v", savedCursor)
	}
	if savedCursor.Routing.Mode != "local" {
		t.Fatalf("cursor routing = %q, want local", savedCursor.Routing.Mode)
	}

	settings := savedCursor
	settings.Appearance.Theme = "system"
	settings.Advertising.Enabled = false
	settings.Routing.Mode = "upstream"
	savedSettings, err := store.SaveSystemSettings(context.Background(), settings)
	if err != nil {
		t.Fatalf("SaveSystemSettings() error = %v", err)
	}
	if savedSettings.Appearance.Theme != "system" || savedSettings.Advertising.Enabled {
		t.Fatalf("settings section not saved: %+v", savedSettings)
	}
	if savedSettings.Routing.Mode != "local" {
		t.Fatalf("settings save overwrote other pages: %+v", savedSettings)
	}
}

func TestStoreSaveModelAdaptersRejectsBrokenGatewayPublicModels(t *testing.T) {
	store := newWriteTestStore(t)
	seed := seedWriteTestConfig(t, store, func(cfg *Config) {
		cfg.ModelAdapters = []ModelAdapterConfig{testModelAdapter("physical", 1)}
	})
	adapterID := seed.ModelAdapters[0].ID
	gateway := seed.Gateway
	gateway.PublicModels = []GatewayPublicModel{{ID: "public-a", TargetAdapterID: adapterID}}
	if _, err := store.SaveGatewayConfig(context.Background(), gateway); err != nil {
		t.Fatalf("SaveGatewayConfig() error = %v", err)
	}

	if _, err := store.SaveModelAdapters(context.Background(), nil); err == nil || !strings.Contains(err.Error(), "公开模型") {
		t.Fatalf("delete mapped adapter error = %v", err)
	}

	renamed := seed.ModelAdapters[0]
	renamed.DisplayName = "renamed-physical"
	renamedSave, err := store.SaveModelAdapters(context.Background(), []ModelAdapterConfig{renamed})
	if err != nil {
		t.Fatalf("rename mapped adapter error = %v", err)
	}
	if renamedSave.ModelAdapters[0].ID == adapterID {
		t.Fatal("renaming a mapped adapter must recompute its derived channel ID")
	}
	if renamedSave.Gateway.PublicModels[0].TargetAdapterID != renamedSave.ModelAdapters[0].ID {
		t.Fatalf("public model target = %q, want remapped %q", renamedSave.Gateway.PublicModels[0].TargetAdapterID, renamedSave.ModelAdapters[0].ID)
	}

	got, err := store.Load(context.Background())
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if len(got.ModelAdapters) != 1 || got.ModelAdapters[0].DisplayName != "renamed-physical" {
		t.Fatalf("rename save adapters = %+v", got.ModelAdapters)
	}
	if len(got.Gateway.PublicModels) != 1 || got.Gateway.PublicModels[0].TargetAdapterID != got.ModelAdapters[0].ID {
		t.Fatalf("rename save publicModels = %+v", got.Gateway.PublicModels)
	}

	extra := append(append([]ModelAdapterConfig{}, got.ModelAdapters...), testModelAdapter("other", 2))
	saved, err := store.SaveModelAdapters(context.Background(), extra)
	if err != nil {
		t.Fatalf("adding unmapped adapter error = %v", err)
	}
	if len(saved.ModelAdapters) != 2 || saved.Gateway.PublicModels[0].TargetAdapterID != got.ModelAdapters[0].ID {
		t.Fatalf("valid adapter save = %+v", saved)
	}
}

func TestStoreSaveModelAdaptersRemapsFallbackWhenPhysicalIdentityChanges(t *testing.T) {
	store := newWriteTestStore(t)
	seed := seedWriteTestConfig(t, store, func(cfg *Config) {
		cfg.ModelAdapters = writeTestFallbackAdapters(t)
	})
	oldPrimary := seed.ModelAdapters[0].ProviderFallback.PrimaryChannelID
	oldCandidate := seed.ModelAdapters[0].ProviderFallback.CandidateChannelIDs[0]
	if oldPrimary != seed.ModelAdapters[1].ID {
		t.Fatalf("seed primary = %q, want physical %q", oldPrimary, seed.ModelAdapters[1].ID)
	}
	gateway := seed.Gateway
	gateway.PublicModels = []GatewayPublicModel{{ID: "public-a", TargetAdapterID: oldPrimary}}
	if _, err := store.SaveGatewayConfig(context.Background(), gateway); err != nil {
		t.Fatalf("SaveGatewayConfig() error = %v", err)
	}
	if _, _, err := store.SaveLastAgentModelHash(context.Background(), oldPrimary); err != nil {
		t.Fatalf("SaveLastAgentModelHash() error = %v", err)
	}

	edited := cloneWriteTestAdapters(seed.ModelAdapters)
	edited[1].APIKey = "rotated-key"
	saved, err := store.SaveModelAdapters(context.Background(), edited)
	if err != nil {
		t.Fatalf("SaveModelAdapters() after apiKey rotation error = %v", err)
	}
	newPrimary := saved.ModelAdapters[1].ID
	if newPrimary == oldPrimary {
		t.Fatal("rotating apiKey must recompute the physical channel ID")
	}
	fb := saved.ModelAdapters[0].ProviderFallback
	if fb.PrimaryChannelID != newPrimary {
		t.Fatalf("primaryChannelID = %q, want remapped %q (old %q)", fb.PrimaryChannelID, newPrimary, oldPrimary)
	}
	if len(fb.CandidateChannelIDs) != 1 || fb.CandidateChannelIDs[0] != oldCandidate {
		t.Fatalf("candidateChannelIDs = %#v, want unchanged %q", fb.CandidateChannelIDs, oldCandidate)
	}
	if saved.LastAgentModelHash != newPrimary {
		t.Fatalf("lastAgentModelHash = %q, want remapped %q", saved.LastAgentModelHash, newPrimary)
	}
	if len(saved.Gateway.PublicModels) != 1 || saved.Gateway.PublicModels[0].TargetAdapterID != newPrimary {
		t.Fatalf("public model target = %#v, want remapped %q", saved.Gateway.PublicModels, newPrimary)
	}
}

func TestStoreSaveModelAdaptersRemapsFallbackWhenCandidateAPIKeyChanges(t *testing.T) {
	store := newWriteTestStore(t)
	seed := seedWriteTestConfig(t, store, func(cfg *Config) {
		cfg.ModelAdapters = writeTestFallbackAdapters(t)
	})
	oldPrimary := seed.ModelAdapters[0].ProviderFallback.PrimaryChannelID
	oldCandidate := seed.ModelAdapters[0].ProviderFallback.CandidateChannelIDs[0]
	if oldCandidate != seed.ModelAdapters[2].ID {
		t.Fatalf("seed candidate = %q, want physical %q", oldCandidate, seed.ModelAdapters[2].ID)
	}

	edited := cloneWriteTestAdapters(seed.ModelAdapters)
	edited[2].APIKey = "rotated-candidate-key"
	saved, err := store.SaveModelAdapters(context.Background(), edited)
	if err != nil {
		t.Fatalf("SaveModelAdapters() after candidate apiKey rotation error = %v", err)
	}
	newCandidate := saved.ModelAdapters[2].ID
	if newCandidate == oldCandidate {
		t.Fatal("rotating candidate apiKey must recompute the physical channel ID")
	}
	fb := saved.ModelAdapters[0].ProviderFallback
	if fb.PrimaryChannelID != oldPrimary {
		t.Fatalf("primaryChannelID = %q, want unchanged %q", fb.PrimaryChannelID, oldPrimary)
	}
	if len(fb.CandidateChannelIDs) != 1 || fb.CandidateChannelIDs[0] != newCandidate {
		t.Fatalf("candidateChannelIDs = %#v, want remapped %q", fb.CandidateChannelIDs, newCandidate)
	}
}

func TestStoreSaveModelAdaptersRejectsFallbackWhenOpenAITypeBecomesAnthropic(t *testing.T) {
	store := newWriteTestStore(t)
	seed := seedWriteTestConfig(t, store, func(cfg *Config) {
		cfg.ModelAdapters = writeTestFallbackAdapters(t)
	})

	edited := cloneWriteTestAdapters(seed.ModelAdapters)
	edited[1].Type = "anthropic"
	edited[1].OpenAIEndpoint = ""
	edited[1].AnthropicThinkingEffort = "high"
	_, err := store.SaveModelAdapters(context.Background(), edited)
	if err == nil {
		t.Fatal("expected same adapter type/endpoint validation error")
	}
	if !strings.Contains(err.Error(), "相同的适配器类型和协议端点") {
		t.Fatalf("SaveModelAdapters() after type change error = %v", err)
	}
}

func TestStoreSaveModelAdaptersRemapsFallbackWhenModelIDChanges(t *testing.T) {
	store := newWriteTestStore(t)
	seed := seedWriteTestConfig(t, store, func(cfg *Config) {
		cfg.ModelAdapters = writeTestFallbackAdapters(t)
	})
	oldPrimary := seed.ModelAdapters[1].ID

	edited := cloneWriteTestAdapters(seed.ModelAdapters)
	edited[1].ModelID = "rotated-model"
	saved, err := store.SaveModelAdapters(context.Background(), edited)
	if err != nil {
		t.Fatalf("SaveModelAdapters() after modelID change error = %v", err)
	}
	newPrimary := saved.ModelAdapters[1].ID
	if newPrimary == oldPrimary {
		t.Fatal("changing modelID must recompute the physical channel ID")
	}
	if saved.ModelAdapters[0].ProviderFallback.PrimaryChannelID != newPrimary {
		t.Fatalf("primaryChannelID = %q, want remapped %q", saved.ModelAdapters[0].ProviderFallback.PrimaryChannelID, newPrimary)
	}
}

func TestStoreSaveUserConfigRemapsFallbackWhenPhysicalIdentityChanges(t *testing.T) {
	store := newWriteTestStore(t)
	seed := seedWriteTestConfig(t, store, func(cfg *Config) {
		cfg.LastAgentModelHash = "live-hash"
		cfg.ModelAdapters = writeTestFallbackAdapters(t)
	})
	oldPrimary := seed.ModelAdapters[1].ID
	stale := seed
	stale.LastAgentModelHash = "stale-ui-hash"
	stale.ModelAdapters = cloneWriteTestAdapters(seed.ModelAdapters)
	stale.ModelAdapters[1].APIKey = "rotated-key"
	saved, err := store.SaveUserConfig(context.Background(), stale)
	if err != nil {
		t.Fatalf("SaveUserConfig() after apiKey rotation error = %v", err)
	}
	if saved.LastAgentModelHash != "live-hash" {
		t.Fatalf("hash = %q, want live-hash", saved.LastAgentModelHash)
	}
	newPrimary := saved.ModelAdapters[1].ID
	if newPrimary == oldPrimary {
		t.Fatal("rotating apiKey must recompute the physical channel ID")
	}
	if saved.ModelAdapters[0].ProviderFallback.PrimaryChannelID != newPrimary {
		t.Fatalf("primaryChannelID = %q, want remapped %q", saved.ModelAdapters[0].ProviderFallback.PrimaryChannelID, newPrimary)
	}
}

func TestStoreSaveModelAdaptersStillRejectsTrueDanglingFallback(t *testing.T) {
	store := newWriteTestStore(t)
	seed := seedWriteTestConfig(t, store, func(cfg *Config) {
		cfg.ModelAdapters = writeTestFallbackAdapters(t)
	})
	edited := cloneWriteTestAdapters(seed.ModelAdapters)
	edited[0].ProviderFallback.PrimaryChannelID = "dce4005402c60e65"
	_, err := store.SaveModelAdapters(context.Background(), edited)
	if err == nil || !strings.Contains(err.Error(), `模型适配器 providerFallback.primaryChannelID 引用了不存在的渠道 "dce4005402c60e65"`) {
		t.Fatalf("dangling primaryChannelID error = %v", err)
	}
}

func TestStoreSaveModelAdaptersDoesNotRemapDeletedChannelToNewAdapter(t *testing.T) {
	store := newWriteTestStore(t)
	seed := seedWriteTestConfig(t, store, func(cfg *Config) {
		cfg.ModelAdapters = writeTestFallbackAdapters(t)
	})
	oldPrimary := seed.ModelAdapters[1].ID
	edited := cloneWriteTestAdapters(seed.ModelAdapters)
	replacement := testModelAdapter("replacement", 2)
	replacement.BaseURL = "https://replacement.example.com/v1"
	edited[1] = replacement

	_, err := store.SaveModelAdapters(context.Background(), edited)
	if err == nil || !strings.Contains(err.Error(), oldPrimary) {
		t.Fatalf("delete+add replacement error = %v, want dangling old channel %q", err, oldPrimary)
	}
}

func TestNormalizeConfigWithoutPreviousAdaptersReproducesMissingChannelError(t *testing.T) {
	previous, err := NormalizeModelAdapterConfigs(writeTestFallbackAdapters(t))
	if err != nil {
		t.Fatalf("NormalizeModelAdapterConfigs() error = %v", err)
	}
	oldPrimary := previous[1].ID
	edited := cloneWriteTestAdapters(previous)
	edited[1].APIKey = "rotated-key"
	_, err = NormalizeConfig(Config{
		ModelAdapters: edited,
		Gateway:       DefaultGatewayConfig(),
	})
	if err == nil || !strings.Contains(err.Error(), "模型适配器 providerFallback.primaryChannelID 引用了不存在的渠道") {
		t.Fatalf("NormalizeConfig() without previous adapters error = %v, want missing-channel symptom (old primary %q)", err, oldPrimary)
	}
	if !strings.Contains(err.Error(), oldPrimary) {
		t.Fatalf("NormalizeConfig() error = %v, want stale primary %q", err, oldPrimary)
	}
}

func TestNormalizeConfigRemapsStaleFallbackAfterPhysicalAPIKeyChange(t *testing.T) {
	previous, err := NormalizeModelAdapterConfigs(writeTestFallbackAdapters(t))
	if err != nil {
		t.Fatalf("NormalizeModelAdapterConfigs() error = %v", err)
	}
	oldPrimary := previous[1].ID
	edited := cloneWriteTestAdapters(previous)
	edited[1].APIKey = "rotated-key"
	got, err := normalizeConfig(Config{
		ModelAdapters: edited,
		Gateway:       DefaultGatewayConfig(),
	}, previous)
	if err != nil {
		t.Fatalf("normalizeConfig() after apiKey rotation error = %v", err)
	}
	newPrimary := got.ModelAdapters[1].ID
	if newPrimary == oldPrimary {
		t.Fatal("rotating apiKey must recompute the physical channel ID")
	}
	if got.ModelAdapters[0].ProviderFallback.PrimaryChannelID != newPrimary {
		t.Fatalf("primaryChannelID = %q, want remapped %q", got.ModelAdapters[0].ProviderFallback.PrimaryChannelID, newPrimary)
	}
}

func TestStoreSaveHomeMetricsDoesNotOverwriteOtherSections(t *testing.T) {
	store := newWriteTestStore(t)
	seed := seedWriteTestConfig(t, store, func(cfg *Config) {
		cfg.Appearance.Theme = "dark"
		cfg.Gateway.Enabled = true
		cfg.Gateway.Token = "live-token"
		cfg.HomeMetrics.IncludeCacheWriteInHitRate = false
		cfg.ModelAdapters = []ModelAdapterConfig{testModelAdapter("ch-a", 1)}
	})

	stale := seed
	stale.Appearance.Theme = "light"
	stale.Gateway.Token = ""
	stale.HomeMetrics.IncludeCacheWriteInHitRate = true
	stale.ModelAdapters = []ModelAdapterConfig{testModelAdapter("ch-b", 1)}
	got, err := store.SaveHomeMetrics(context.Background(), stale)
	if err != nil {
		t.Fatalf("SaveHomeMetrics() error = %v", err)
	}
	if !got.HomeMetrics.IncludeCacheWriteInHitRate {
		t.Fatal("home metrics section not saved")
	}
	if got.Appearance.Theme != "dark" || got.Gateway.Token != seed.Gateway.Token {
		t.Fatalf("home metrics save overwrote other pages: %+v", got)
	}
	if len(got.ModelAdapters) != 1 || got.ModelAdapters[0].DisplayName != "ch-a" {
		t.Fatalf("home metrics save overwrote adapters: %+v", got.ModelAdapters)
	}
}

func TestStoreSaveReplacesHash(t *testing.T) {
	store := newWriteTestStore(t)
	_ = seedWriteTestConfig(t, store, func(cfg *Config) {
		cfg.LastAgentModelHash = "live-hash"
		cfg.Appearance.Theme = "light"
	})

	replacement := DefaultConfig()
	replacement.LastAgentModelHash = "imported-hash"
	replacement.Appearance.Theme = "dark"
	got, err := store.Save(context.Background(), replacement)
	if err != nil {
		t.Fatalf("Save() error = %v", err)
	}
	if got.LastAgentModelHash != "imported-hash" {
		t.Fatalf("hash = %q, want imported-hash", got.LastAgentModelHash)
	}
	if got.Appearance.Theme != "dark" {
		t.Fatalf("theme = %q, want dark", got.Appearance.Theme)
	}
}

func TestStoreSaveUserConfigRejectDoesNotAdvanceDisk(t *testing.T) {
	store := newWriteTestStore(t)
	seed := seedWriteTestConfig(t, store, func(cfg *Config) {
		cfg.LastAgentModelHash = "live-hash"
		cfg.Appearance.Theme = "light"
	})

	invalid := seed
	invalid.Observability.Mode = "not-a-mode"
	invalid.LastAgentModelHash = "stale-hash"
	if _, err := store.SaveUserConfig(context.Background(), invalid); err == nil {
		t.Fatal("SaveUserConfig() error = nil, want invalid observability")
	}

	loaded, err := store.Load(context.Background())
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if loaded.LastAgentModelHash != "live-hash" || loaded.Appearance.Theme != "light" {
		t.Fatalf("invalid save advanced disk: %+v", loaded)
	}
}

func TestStoreConcurrentUserAndHashWritesPreserveBoth(t *testing.T) {
	store := newWriteTestStore(t)
	_ = seedWriteTestConfig(t, store, func(cfg *Config) {
		cfg.LastAgentModelHash = "old-hash"
		cfg.Appearance.Theme = "light"
		cfg.ModelAdapters = []ModelAdapterConfig{testModelAdapter("ch-a", 1)}
	})

	user := DefaultConfig()
	user.LastAgentModelHash = "stale-ui-hash"
	user.Appearance.Theme = "dark"
	user.ModelAdapters = []ModelAdapterConfig{testModelAdapter("ch-b", 1)}

	var wg sync.WaitGroup
	errCh := make(chan error, 2)
	wg.Add(2)
	go func() {
		defer wg.Done()
		_, err := store.SaveUserConfig(context.Background(), user)
		errCh <- err
	}()
	go func() {
		defer wg.Done()
		_, _, err := store.SaveLastAgentModelHash(context.Background(), "new-hash")
		errCh <- err
	}()
	wg.Wait()
	close(errCh)
	for err := range errCh {
		if err != nil {
			t.Fatalf("concurrent write error = %v", err)
		}
	}

	got, err := store.Load(context.Background())
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if got.LastAgentModelHash != "new-hash" {
		t.Fatalf("hash = %q, want new-hash", got.LastAgentModelHash)
	}
	if got.Appearance.Theme != "dark" || len(got.ModelAdapters) != 1 || got.ModelAdapters[0].DisplayName != "ch-b" {
		t.Fatalf("user fields lost: %+v", got)
	}
}

func newWriteTestStore(t *testing.T) *Store {
	t.Helper()
	root := t.TempDir()
	return NewStore(filepath.Join(root, "config.yaml"), filepath.Join(root, "logs"))
}

func seedWriteTestConfig(t *testing.T, store *Store, mutate func(*Config)) Config {
	t.Helper()
	cfg := DefaultConfig()
	if mutate != nil {
		mutate(&cfg)
	}
	saved, err := store.Save(context.Background(), cfg)
	if err != nil {
		t.Fatalf("seed Save() error = %v", err)
	}
	return saved
}

func cloneWriteTestAdapters(adapters []ModelAdapterConfig) []ModelAdapterConfig {
	return append([]ModelAdapterConfig{}, adapters...)
}

func writeTestFallbackAdapters(t *testing.T) []ModelAdapterConfig {
	t.Helper()
	adapters := append(testFallbackAdapters(), testModelAdapter("ch-c", 3))
	adapters[2].BaseURL = "https://api3.example.com/v1"
	normalized, err := NormalizeModelAdapterConfigs(adapters)
	if err != nil {
		t.Fatalf("NormalizeModelAdapterConfigs() error = %v", err)
	}
	adapters[0].ProviderFallback = ProviderFallbackConfig{
		Enabled:             true,
		PrimaryChannelID:    normalized[1].ID,
		CandidateChannelIDs: []string{normalized[2].ID},
		MaxHttpAttempts:     4,
		MaxWaitSeconds:      6,
	}
	return adapters
}

func assertWriteTestFallbackPreserved(t *testing.T, got Config, seed Config, wantHash string) {
	t.Helper()
	if got.LastAgentModelHash != wantHash {
		t.Fatalf("hash = %q, want %q", got.LastAgentModelHash, wantHash)
	}
	if len(got.ModelAdapters) != len(seed.ModelAdapters) {
		t.Fatalf("adapter count = %d, want %d", len(got.ModelAdapters), len(seed.ModelAdapters))
	}
	for i := range seed.ModelAdapters {
		if got.ModelAdapters[i].DisplayName != seed.ModelAdapters[i].DisplayName {
			t.Fatalf("adapter[%d] = %q, want %q", i, got.ModelAdapters[i].DisplayName, seed.ModelAdapters[i].DisplayName)
		}
	}
	fb := got.ModelAdapters[0].ProviderFallback
	want := seed.ModelAdapters[0].ProviderFallback
	if !fb.Enabled || fb.PrimaryChannelID != want.PrimaryChannelID || fb.MaxHttpAttempts != want.MaxHttpAttempts || fb.MaxWaitSeconds != want.MaxWaitSeconds {
		t.Fatalf("fallback = %+v, want %+v", fb, want)
	}
	if len(fb.CandidateChannelIDs) != 1 || fb.CandidateChannelIDs[0] != want.CandidateChannelIDs[0] {
		t.Fatalf("fallback candidates = %#v, want %#v", fb.CandidateChannelIDs, want.CandidateChannelIDs)
	}
}
