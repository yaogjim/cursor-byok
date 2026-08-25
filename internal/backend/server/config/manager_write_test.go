package config

import (
	"context"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
)

func TestManagerStaleUserPayloadDoesNotClobberHash(t *testing.T) {
	manager := newWriteTestManager(t)
	seed := seedWriteTestManagerConfig(t, manager, func(cfg *Config) {
		cfg.LastAgentModelHash = "old-hash"
		cfg.Appearance.Theme = "light"
		cfg.ModelAdapters = []ModelAdapterConfig{testModelAdapter("ch-a", 1)}
	})
	if err := manager.SaveLastAgentModelHash(context.Background(), "live-hash"); err != nil {
		t.Fatalf("SaveLastAgentModelHash() error = %v", err)
	}

	stale := seed
	stale.LastAgentModelHash = "stale-ui-hash"
	stale.Appearance.Theme = "dark"
	stale.ModelAdapters = []ModelAdapterConfig{testModelAdapter("ch-b", 1)}
	got, err := manager.SaveUserConfig(context.Background(), stale)
	if err != nil {
		t.Fatalf("SaveUserConfig() error = %v", err)
	}
	if got.LastAgentModelHash != "live-hash" {
		t.Fatalf("hash = %q, want live-hash", got.LastAgentModelHash)
	}
	if got.Appearance.Theme != "dark" || len(got.ModelAdapters) != 1 || got.ModelAdapters[0].DisplayName != "ch-b" {
		t.Fatalf("user fields = %+v", got)
	}

	current := manager.Current()
	if current.LastAgentModelHash != "live-hash" || current.Appearance.Theme != "dark" {
		t.Fatalf("Current() = %+v", current)
	}
}

func TestManagerHashUpdateDoesNotRollbackAdaptersOrFallback(t *testing.T) {
	manager := newWriteTestManager(t)
	seed := seedWriteTestManagerConfig(t, manager, func(cfg *Config) {
		cfg.LastAgentModelHash = "old-hash"
		cfg.ModelAdapters = writeTestFallbackAdapters(t)
	})

	user := seed
	user.LastAgentModelHash = "stale-ui-hash"
	user.Appearance.Theme = "dark"
	if _, err := manager.SaveUserConfig(context.Background(), user); err != nil {
		t.Fatalf("SaveUserConfig() error = %v", err)
	}
	if err := manager.SaveLastAgentModelHash(context.Background(), "new-hash"); err != nil {
		t.Fatalf("SaveLastAgentModelHash() error = %v", err)
	}

	got := manager.Current()
	if got.Appearance.Theme != "dark" {
		t.Fatalf("theme rolled back to %q", got.Appearance.Theme)
	}
	assertWriteTestFallbackPreserved(t, got, seed, "new-hash")
}

func TestManagerDeterministicInterleavingHashThenUser(t *testing.T) {
	manager := newWriteTestManager(t)
	seed := seedWriteTestManagerConfig(t, manager, func(cfg *Config) {
		cfg.LastAgentModelHash = "old-hash"
		cfg.ModelAdapters = []ModelAdapterConfig{testModelAdapter("ch-a", 1)}
	})
	if err := manager.SaveLastAgentModelHash(context.Background(), "new-hash"); err != nil {
		t.Fatalf("SaveLastAgentModelHash() error = %v", err)
	}

	stale := seed
	stale.LastAgentModelHash = "stale-ui-hash"
	stale.ModelAdapters = []ModelAdapterConfig{testModelAdapter("ch-b", 1)}
	got, err := manager.SaveUserConfig(context.Background(), stale)
	if err != nil {
		t.Fatalf("SaveUserConfig() error = %v", err)
	}
	if got.LastAgentModelHash != "new-hash" || got.ModelAdapters[0].DisplayName != "ch-b" {
		t.Fatalf("interleaving hash-then-user = %+v", got)
	}
}

func TestManagerDeterministicInterleavingUserThenHash(t *testing.T) {
	manager := newWriteTestManager(t)
	seed := seedWriteTestManagerConfig(t, manager, func(cfg *Config) {
		cfg.LastAgentModelHash = "old-hash"
		cfg.ModelAdapters = []ModelAdapterConfig{testModelAdapter("ch-a", 1)}
	})

	user := seed
	user.LastAgentModelHash = "stale-ui-hash"
	user.ModelAdapters = []ModelAdapterConfig{testModelAdapter("ch-b", 1)}
	if _, err := manager.SaveUserConfig(context.Background(), user); err != nil {
		t.Fatalf("SaveUserConfig() error = %v", err)
	}
	if err := manager.SaveLastAgentModelHash(context.Background(), "new-hash"); err != nil {
		t.Fatalf("SaveLastAgentModelHash() error = %v", err)
	}

	got := manager.Current()
	if got.LastAgentModelHash != "new-hash" || got.ModelAdapters[0].DisplayName != "ch-b" {
		t.Fatalf("interleaving user-then-hash = %+v", got)
	}
}

func TestManagerHashUpdateDoesNotNotify(t *testing.T) {
	manager := newWriteTestManager(t)
	_ = seedWriteTestManagerConfig(t, manager, func(cfg *Config) {
		cfg.LastAgentModelHash = "old-hash"
	})

	var notified atomic.Int32
	unsub := manager.Subscribe(func(Config) {
		notified.Add(1)
	})
	defer unsub()

	if err := manager.SaveLastAgentModelHash(context.Background(), "new-hash"); err != nil {
		t.Fatalf("SaveLastAgentModelHash() error = %v", err)
	}
	if got := notified.Load(); got != 0 {
		t.Fatalf("hash update notified %d listeners, want 0", got)
	}
	if manager.Current().LastAgentModelHash != "new-hash" {
		t.Fatalf("Current() hash = %q, want new-hash", manager.Current().LastAgentModelHash)
	}

	user := manager.Current()
	user.LastAgentModelHash = "stale-ui-hash"
	user.Appearance.Theme = "dark"
	if _, err := manager.SaveUserConfig(context.Background(), user); err != nil {
		t.Fatalf("SaveUserConfig() error = %v", err)
	}
	if got := notified.Load(); got != 1 {
		t.Fatalf("user save notified %d listeners, want 1", got)
	}
}

func TestManagerSaveReplacesHash(t *testing.T) {
	manager := newWriteTestManager(t)
	_ = seedWriteTestManagerConfig(t, manager, func(cfg *Config) {
		cfg.LastAgentModelHash = "live-hash"
		cfg.Appearance.Theme = "light"
	})

	replacement := DefaultConfig()
	replacement.LastAgentModelHash = "imported-hash"
	replacement.Appearance.Theme = "dark"
	got, err := manager.Save(context.Background(), replacement)
	if err != nil {
		t.Fatalf("Save() error = %v", err)
	}
	if got.LastAgentModelHash != "imported-hash" || manager.Current().LastAgentModelHash != "imported-hash" {
		t.Fatalf("replace hash = %q, current = %q", got.LastAgentModelHash, manager.Current().LastAgentModelHash)
	}
}

func TestManagerLegacyRuntimeSnapshotProjectsCapacity(t *testing.T) {
	manager := newWriteTestManager(t)
	_ = seedWriteTestManagerConfig(t, manager, func(cfg *Config) {
		adapter := testModelAdapter("ch-a", 1)
		adapter.MaxConcurrentRequests = 4
		cfg.ModelAdapters = []ModelAdapterConfig{adapter}
	})

	snapshot, err := manager.LegacyRuntimeSnapshot(context.Background())
	if err != nil {
		t.Fatalf("LegacyRuntimeSnapshot() error = %v", err)
	}
	if len(snapshot.ModelAdapters) != 1 || snapshot.ModelAdapters[0].MaxConcurrentRequests != 4 {
		t.Fatalf("snapshot capacity = %#v, want 4", snapshot.ModelAdapters)
	}
}

func TestManagerWriteFailureDoesNotAdvanceMemory(t *testing.T) {
	manager := newWriteTestManager(t)
	seed := seedWriteTestManagerConfig(t, manager, func(cfg *Config) {
		cfg.LastAgentModelHash = "live-hash"
		cfg.Appearance.Theme = "light"
	})

	invalid := seed
	invalid.Observability.Mode = "not-a-mode"
	if _, err := manager.SaveUserConfig(context.Background(), invalid); err == nil {
		t.Fatal("SaveUserConfig() error = nil, want invalid observability")
	}
	current := manager.Current()
	if current.LastAgentModelHash != "live-hash" || current.Appearance.Theme != "light" {
		t.Fatalf("failed write advanced memory: %+v", current)
	}
}

func TestManagerConcurrentUserAndHashWritesPreserveBoth(t *testing.T) {
	manager := newWriteTestManager(t)
	_ = seedWriteTestManagerConfig(t, manager, func(cfg *Config) {
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
		_, err := manager.SaveUserConfig(context.Background(), user)
		errCh <- err
	}()
	go func() {
		defer wg.Done()
		errCh <- manager.SaveLastAgentModelHash(context.Background(), "new-hash")
	}()
	wg.Wait()
	close(errCh)
	for err := range errCh {
		if err != nil {
			t.Fatalf("concurrent write error = %v", err)
		}
	}

	got := manager.Current()
	if got.LastAgentModelHash != "new-hash" {
		t.Fatalf("hash = %q, want new-hash", got.LastAgentModelHash)
	}
	if got.Appearance.Theme != "dark" || len(got.ModelAdapters) != 1 || got.ModelAdapters[0].DisplayName != "ch-b" {
		t.Fatalf("user fields lost: %+v", got)
	}
}

func newWriteTestManager(t *testing.T) *Manager {
	t.Helper()
	root := t.TempDir()
	store := NewStore(filepath.Join(root, "config.yaml"), filepath.Join(root, "logs"))
	manager, err := NewManager(context.Background(), store)
	if err != nil {
		t.Fatalf("NewManager() error = %v", err)
	}
	return manager
}

func seedWriteTestManagerConfig(t *testing.T, manager *Manager, mutate func(*Config)) Config {
	t.Helper()
	cfg := DefaultConfig()
	if mutate != nil {
		mutate(&cfg)
	}
	saved, err := manager.Save(context.Background(), cfg)
	if err != nil {
		t.Fatalf("seed Save() error = %v", err)
	}
	return saved
}
