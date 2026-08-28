package client

import (
	"context"
	"net"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"cursor/internal/backend"
	serverconfig "cursor/internal/backend/server/config"
)

func TestShutdownForQuitIsIdempotent(t *testing.T) {
	service := newLifecycleTestService(t)
	var canceled atomic.Int32
	service.backendHost.BeginActiveProvider("quit-once", func() { canceled.Add(1) })
	service.ShutdownForQuitFrom(backend.ShutdownInitiatorTray)
	service.ShutdownForQuit()
	service.ShutdownForQuitFrom(backend.ShutdownInitiatorOnShutdown)
	if canceled.Load() != 1 {
		t.Fatalf("cancel count = %d, want 1", canceled.Load())
	}
	report := service.backendHost.LastShutdown()
	if report.Reason != backend.ShutdownReasonAppQuit || report.Initiator != backend.ShutdownInitiatorTray {
		t.Fatalf("first initiator was not preserved: %+v", report)
	}
	if report.CancelCount != 1 || report.Outcome != backend.ShutdownOutcomeCanceled {
		t.Fatalf("shutdown report = %+v", report)
	}
}

func TestSaveUserConfigDoesNotEnterShutdownCancelPath(t *testing.T) {
	service := newLifecycleTestService(t)
	var canceled atomic.Bool
	finish := service.backendHost.BeginActiveProvider("save-user-config", func() { canceled.Store(true) })
	defer finish()
	cfg := serverconfig.DefaultConfig()
	cfg.Appearance.Theme = "dark"
	cfg.Routing.Mode = "upstream"
	if err := service.SaveUserConfig(cfg); err != nil {
		t.Fatalf("SaveUserConfig() error = %v", err)
	}
	if canceled.Load() {
		t.Fatal("SaveUserConfig canceled an in-flight provider")
	}
	if service.backendHost.LastShutdown().Reason != "" {
		t.Fatalf("SaveUserConfig entered shutdown path: %+v", service.backendHost.LastShutdown())
	}
	if service.backendHost.ActiveProviderCount() != 1 {
		t.Fatalf("active providers = %d, want 1", service.backendHost.ActiveProviderCount())
	}
}

func newLifecycleTestService(t *testing.T) *ProxyService {
	t.Helper()
	root := t.TempDir()
	store := serverconfig.NewStore(filepath.Join(root, "config.yaml"), filepath.Join(root, "logs"))
	cfg := serverconfig.DefaultConfig()
	cfg.BackendListenAddr = mustFreeListenAddr(t)
	cfg.ProxyListenAddr = mustFreeListenAddr(t)
	if _, err := store.Save(context.Background(), cfg); err != nil {
		t.Fatalf("Save() error = %v", err)
	}
	host, err := backend.NewHost(store, nil)
	if err != nil {
		t.Fatalf("NewHost() error = %v", err)
	}
	if err := host.Start(); err != nil {
		t.Fatalf("Start() error = %v", err)
	}
	t.Cleanup(func() {
		_ = host.Stop(context.Background())
		_ = host.CloseObservability()
	})
	return &ProxyService{
		backendHost: host,
		store:       store,
	}
}

func mustFreeListenAddr(t *testing.T) string {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	addr := listener.Addr().String()
	if err := listener.Close(); err != nil {
		t.Fatalf("close listener: %v", err)
	}
	return addr
}

func TestShutdownForQuitUsesFreshDrainBudget(t *testing.T) {
	service := newLifecycleTestService(t)
	canceled := make(chan struct{})
	service.backendHost.BeginActiveProvider("quit-timeout", func() { close(canceled) })
	started := time.Now()
	service.ShutdownForQuitFrom(backend.ShutdownInitiatorOnShutdown)
	select {
	case <-canceled:
	case <-time.After(6 * time.Second):
		t.Fatal("shutdown did not cancel leftover provider")
	}
	if elapsed := time.Since(started); elapsed > 6*time.Second {
		t.Fatalf("shutdown took %s", elapsed)
	}
}