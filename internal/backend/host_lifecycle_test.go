package backend

import (
	"context"
	"net"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	serverconfig "cursor/internal/backend/server/config"
	"cursor/internal/observability"
)

func TestObservabilitySettingsFingerprintIgnoresRoutingAndModels(t *testing.T) {
	base := serverconfig.DefaultConfig()
	base.Observability.Mode = serverconfig.ObservabilityModeBasic
	base.Observability.RetentionDays = 7
	base.Observability.MaxDiskMB = 1024
	changed := base
	changed.Routing.Mode = "upstream"
	changed.HomeMetrics.IncludeCacheWriteInHitRate = true
	changed.ModelAdapters = []serverconfig.ModelAdapterConfig{{
		Type:    "openai",
		ModelID: "model-b",
	}}
	left := observabilitySettings(base)
	right := observabilitySettings(changed)
	if left.Metadata.ConfigFingerprint != right.Metadata.ConfigFingerprint {
		t.Fatalf("storage fingerprint changed with routing/model metadata: %q vs %q", left.Metadata.ConfigFingerprint, right.Metadata.ConfigFingerprint)
	}
	if left.RuntimeFingerprint == right.RuntimeFingerprint {
		t.Fatal("runtime fingerprint did not change with routing/model metadata")
	}
	storageChanged := base
	storageChanged.Observability.Mode = serverconfig.ObservabilityModeFull
	if observabilitySettings(base).Metadata.ConfigFingerprint == observabilitySettings(storageChanged).Metadata.ConfigFingerprint {
		t.Fatal("storage fingerprint ignored observability mode change")
	}
}

func TestHostSaveConfigDoesNotCancelActiveProviders(t *testing.T) {
	host := newLifecycleTestHost(t)
	var canceled atomic.Bool
	finish := host.BeginActiveProvider("save-config", func() { canceled.Store(true) })
	defer finish()
	if host.ActiveProviderCount() != 1 {
		t.Fatalf("active providers = %d, want 1", host.ActiveProviderCount())
	}
	first := host.Observability().Status()
	cfg := host.configs.Current()
	cfg.Routing.Mode = "upstream"
	if _, err := host.SaveConfig(context.Background(), cfg); err != nil {
		t.Fatalf("SaveConfig() error = %v", err)
	}
	if canceled.Load() {
		t.Fatal("SaveConfig canceled an in-flight provider")
	}
	if host.ActiveProviderCount() != 1 {
		t.Fatalf("active providers after SaveConfig = %d, want 1", host.ActiveProviderCount())
	}
	second := host.Observability().Status()
	if second.SessionID != first.SessionID {
		t.Fatalf("routing/model save rotated observability session: first=%+v second=%+v", first, second)
	}
	if host.LastShutdown().Reason != "" {
		t.Fatalf("SaveConfig entered shutdown path: %+v", host.LastShutdown())
	}
}

func TestHostStopDrainsThenCancelsProviders(t *testing.T) {
	t.Run("drain success", func(t *testing.T) {
		host := newLifecycleTestHost(t)
		var canceled atomic.Bool
		finish := host.BeginActiveProvider("drain-ok", func() { canceled.Store(true) })
		done := make(chan struct{})
		go func() {
			time.Sleep(20 * time.Millisecond)
			finish()
			close(done)
		}()
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		if err := host.StopWithCause(ctx, ShutdownCause{Reason: ShutdownReasonAppQuit, Initiator: ShutdownInitiatorTray}); err != nil {
			t.Fatalf("StopWithCause() error = %v", err)
		}
		<-done
		if canceled.Load() {
			t.Fatal("provider was canceled after draining")
		}
		report := host.LastShutdown()
		if report.Reason != ShutdownReasonAppQuit || report.Initiator != ShutdownInitiatorTray {
			t.Fatalf("shutdown report identity = %+v", report)
		}
		if report.ActiveProviderCount != 1 {
			t.Fatalf("active_provider_count = %d, want 1", report.ActiveProviderCount)
		}
		if report.CancelCount != 0 || report.Outcome != ShutdownOutcomeDrained {
			t.Fatalf("shutdown report = %+v", report)
		}
	})

	t.Run("drain timeout cancels", func(t *testing.T) {
		host := newLifecycleTestHost(t)
		canceled := make(chan struct{})
		host.BeginActiveProvider("drain-timeout", func() { close(canceled) })
		ctx, cancel := context.WithTimeout(context.Background(), 40*time.Millisecond)
		defer cancel()
		started := time.Now()
		if err := host.StopWithCause(ctx, ShutdownCause{Reason: ShutdownReasonAppQuit, Initiator: ShutdownInitiatorOnShutdown}); err != nil {
			t.Fatalf("StopWithCause() error = %v", err)
		}
		select {
		case <-canceled:
		case <-time.After(time.Second):
			t.Fatal("provider was not canceled after drain timeout")
		}
		if elapsed := time.Since(started); elapsed > 500*time.Millisecond {
			t.Fatalf("stop took %s, want bounded drain", elapsed)
		}
		report := host.LastShutdown()
		if report.CancelCount != 1 || report.Outcome != ShutdownOutcomeCanceled {
			t.Fatalf("shutdown report = %+v", report)
		}
		if report.ActiveProviderCount != 1 {
			t.Fatalf("active_provider_count = %d, want 1", report.ActiveProviderCount)
		}
	})

	t.Run("idempotent already stopped", func(t *testing.T) {
		host := newLifecycleTestHost(t)
		if err := host.Stop(context.Background()); err != nil {
			t.Fatalf("first Stop() error = %v", err)
		}
		if err := host.StopWithCause(context.Background(), ShutdownCause{Reason: ShutdownReasonAppQuit, Initiator: ShutdownInitiatorTray}); err != nil {
			t.Fatalf("second Stop() error = %v", err)
		}
		report := host.LastShutdown()
		if report.Outcome != ShutdownOutcomeAlreadyStopped {
			t.Fatalf("second stop outcome = %q, want %s", report.Outcome, ShutdownOutcomeAlreadyStopped)
		}
		if report.CancelCount != 0 {
			t.Fatalf("second stop cancel_count = %d", report.CancelCount)
		}
	})
}

func TestHostObservabilityRotationDoesNotCancelProviders(t *testing.T) {
	host := newLifecycleTestHost(t)
	var canceled atomic.Bool
	finish := host.BeginActiveProvider("rotate", func() { canceled.Store(true) })
	defer finish()
	first := host.Observability().Status()
	if err := host.Observability().Reconfigure(observability.Settings{
		Mode:               observability.ModeFull,
		RetentionDays:      7,
		MaxDiskMB:          1024,
		RuntimeFingerprint: "ignored",
	}); err != nil {
		t.Fatalf("Reconfigure() error = %v", err)
	}
	if canceled.Load() {
		t.Fatal("observability rotation canceled an in-flight provider")
	}
	second := host.Observability().Status()
	if second.SessionID == first.SessionID {
		t.Fatal("storage settings change did not rotate recorder")
	}
}

func newLifecycleTestHost(t *testing.T) *Host {
	t.Helper()
	root := t.TempDir()
	store := serverconfig.NewStore(filepath.Join(root, "config.yaml"), filepath.Join(root, "logs"))
	cfg := serverconfig.DefaultConfig()
	cfg.BackendListenAddr = mustFreeListenAddr(t)
	cfg.ProxyListenAddr = mustFreeListenAddr(t)
	if _, err := store.Save(context.Background(), cfg); err != nil {
		t.Fatalf("Save() error = %v", err)
	}
	host, err := NewHost(store, nil)
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
	return host
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
