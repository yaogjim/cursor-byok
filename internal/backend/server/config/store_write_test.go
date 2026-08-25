package config

import (
	"context"
	"os"
	"path/filepath"
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
