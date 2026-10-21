package client

import (
	"context"
	"errors"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	serverconfig "cursor/internal/backend/server/config"
	"cursor/internal/gateway"
)

func TestStartAndStopGatewayWithoutCursorRuntime(t *testing.T) {
	service := newConfigTransferTestService(t)
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	listenAddr := listener.Addr().String()
	_ = listener.Close()
	cfg := serverconfig.DefaultConfig()
	cfg.Gateway.Enabled = true
	cfg.Gateway.Token = "independent-gateway-token"
	cfg.Gateway.ListenAddr = listenAddr
	if _, err := service.store.Save(context.Background(), cfg); err != nil {
		t.Fatalf("seed config: %v", err)
	}

	state, err := service.StartGateway()
	if err != nil {
		t.Fatalf("StartGateway() error = %v", err)
	}
	if !state.GatewayRunning || state.BackendRunning || state.ProxyRunning || state.CursorSettingsApplied {
		t.Fatalf("independent start state = %#v", state)
	}
	testResult, err := service.TestGateway()
	if err != nil {
		t.Fatalf("TestGateway() error = %v", err)
	}
	if testResult.ListenAddr != listenAddr || testResult.ModelCount != 0 || testResult.LatencyMS < 0 {
		t.Fatalf("TestGateway() = %#v", testResult)
	}
	stopped, err := service.StopGateway()
	if err != nil {
		t.Fatalf("StopGateway() error = %v", err)
	}
	if stopped.GatewayRunning || stopped.BackendRunning || stopped.ProxyRunning || stopped.CursorSettingsApplied {
		t.Fatalf("independent stop state = %#v", stopped)
	}
	if _, err := service.TestGateway(); err == nil || !strings.Contains(err.Error(), "未运行") {
		t.Fatalf("TestGateway() after stop error = %v", err)
	}
}

func TestStartGatewayRequiresEnabledConfig(t *testing.T) {
	service := newConfigTransferTestService(t)
	state, err := service.StartGateway()
	if err == nil || state.GatewayRunning || !strings.Contains(err.Error(), "未启用") {
		t.Fatalf("StartGateway() state=%#v err=%v", state, err)
	}
}

func TestCopyAndRotateGatewayToken(t *testing.T) {
	service := newConfigTransferTestService(t)
	cfg := serverconfig.DefaultConfig()
	cfg.Gateway.Enabled = true
	cfg.Gateway.Token = "initial-token"
	if _, err := service.store.Save(context.Background(), cfg); err != nil {
		t.Fatalf("seed Save() error = %v", err)
	}
	got, err := service.CopyGatewayToken()
	if err != nil {
		t.Fatalf("CopyGatewayToken() error = %v", err)
	}
	if got != "initial-token" {
		t.Fatalf("CopyGatewayToken() = %q", got)
	}
	rotated, err := service.RotateGatewayToken()
	if err != nil {
		t.Fatalf("RotateGatewayToken() error = %v", err)
	}
	if rotated == "" || rotated == "initial-token" {
		t.Fatalf("RotateGatewayToken() = %q", rotated)
	}
	copied, err := service.CopyGatewayToken()
	if err != nil || copied != rotated {
		t.Fatalf("Copy after rotate = %q err=%v", copied, err)
	}
	loaded, err := service.LoadUserConfig()
	if err != nil {
		t.Fatalf("LoadUserConfig() error = %v", err)
	}
	if loaded.Gateway.Token != rotated {
		t.Fatalf("persisted token = %q", loaded.Gateway.Token)
	}
	projected := serverconfig.RedactGatewayTokenForUI(loaded)
	if projected.Gateway.Token != "" {
		t.Fatalf("frontend projection leaked token = %q", projected.Gateway.Token)
	}
	if !projected.Gateway.TokenConfigured {
		t.Fatal("frontend projection dropped tokenConfigured")
	}
}

func TestExportUserConfigStripsGatewayToken(t *testing.T) {
	service := newConfigTransferTestService(t)
	cfg := serverconfig.DefaultConfig()
	cfg.Gateway.Enabled = true
	cfg.Gateway.Token = "export-secret-token"
	if _, err := service.store.Save(context.Background(), cfg); err != nil {
		t.Fatalf("seed Save() error = %v", err)
	}
	path, err := service.ExportUserConfig(filepath.Join(t.TempDir(), "backup.yaml"))
	if err != nil {
		t.Fatalf("ExportUserConfig() error = %v", err)
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile() error = %v", err)
	}
	if strings.Contains(string(raw), "export-secret-token") {
		t.Fatalf("export leaked token:\n%s", raw)
	}

	if _, err := service.ImportUserConfig(path); err != nil {
		t.Fatalf("ImportUserConfig() error = %v", err)
	}
	loaded, err := service.LoadUserConfig()
	if err != nil {
		t.Fatalf("LoadUserConfig() after import error = %v", err)
	}
	if loaded.Gateway.Token != "export-secret-token" {
		t.Fatalf("import of stripped export rotated token = %q", loaded.Gateway.Token)
	}
}

func TestImportUserConfigReplacesGatewayTokenWhenPresent(t *testing.T) {
	service := newConfigTransferTestService(t)
	seed := serverconfig.DefaultConfig()
	seed.Gateway.Enabled = true
	seed.Gateway.Token = "live-token"
	if _, err := service.store.Save(context.Background(), seed); err != nil {
		t.Fatalf("seed Save() error = %v", err)
	}
	path := filepath.Join(t.TempDir(), "imported.yaml")
	content := "gateway:\n  enabled: true\n  listenAddr: 127.0.0.1:18091\n  token: imported-token\n  publicModels: []\nmodelAdapters: []\n"
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}
	got, err := service.ImportUserConfig(path)
	if err != nil {
		t.Fatalf("ImportUserConfig() error = %v", err)
	}
	if got.Gateway.Token != "imported-token" {
		t.Fatalf("imported token = %q, want imported-token", got.Gateway.Token)
	}
}

func TestSaveUserConfigPreservesGatewayToken(t *testing.T) {
	service := newConfigTransferTestService(t)
	seed := serverconfig.DefaultConfig()
	seed.Gateway.Enabled = true
	seed.Gateway.Token = "live-token"
	if _, err := service.store.Save(context.Background(), seed); err != nil {
		t.Fatalf("seed Save() error = %v", err)
	}
	stale := seed
	stale.Gateway.Token = ""
	stale.Appearance.Theme = "dark"
	if err := service.SaveUserConfig(stale); err != nil {
		t.Fatalf("SaveUserConfig() error = %v", err)
	}
	got, err := service.LoadUserConfig()
	if err != nil {
		t.Fatalf("LoadUserConfig() error = %v", err)
	}
	if got.Gateway.Token != "live-token" {
		t.Fatalf("token = %q", got.Gateway.Token)
	}
	if got.Appearance.Theme != "dark" {
		t.Fatalf("theme = %q", got.Appearance.Theme)
	}
}

func TestReconcileGatewayStartFailureDoesNotSetCursorError(t *testing.T) {
	root := t.TempDir()
	store := serverconfig.NewStore(filepath.Join(root, "config.yaml"), filepath.Join(root, "logs"))
	service := &ProxyService{store: store}
	cfg := serverconfig.DefaultConfig()
	cfg.Gateway.Enabled = true
	cfg.Gateway.ListenAddr = "0.0.0.0:0"
	cfg.Gateway.Token = "secret-token"
	service.gateway = gateway.New(nil, nil)
	service.reconcileGateway(cfg)
	if service.GetState().LastError != "" {
		t.Fatalf("cursor lastError = %q", service.GetState().LastError)
	}
	if service.GetState().GatewayLastError == "" {
		t.Fatal("expected independent gateway error")
	}
	if service.GetState().GatewayRunning {
		t.Fatal("gateway should not be running")
	}
}

func TestStopGatewayBestEffortClearsErrorWithoutCursorRollback(t *testing.T) {
	service := &ProxyService{}
	service.setGatewayError(errors.New("stale gateway error"))
	instance := gateway.New(nil, nil)
	cfg := serverconfig.DefaultConfig()
	cfg.Gateway.Enabled = true
	cfg.Gateway.ListenAddr = "127.0.0.1:0"
	cfg.Gateway.Token = "secret-token"
	if err := instance.Start(cfg.Gateway); err != nil {
		t.Fatalf("Start() error = %v", err)
	}
	service.gateway = instance
	service.stopGatewayBestEffort()
	if service.GetState().LastError != "" {
		t.Fatalf("cursor lastError = %q", service.GetState().LastError)
	}
	if service.GetState().GatewayLastError != "" {
		t.Fatalf("gateway lastError = %q, want cleared", service.GetState().GatewayLastError)
	}
	if service.GetState().GatewayRunning {
		t.Fatal("gateway should be stopped")
	}
}

func TestStopGatewayBestEffortWithNilGateway(t *testing.T) {
	service := &ProxyService{}
	service.stopGatewayBestEffort()
}

func TestGetStateSnapshotsGatewayWithoutDataRace(t *testing.T) {
	service := &ProxyService{}
	instance := gateway.New(nil, nil)
	cfg := serverconfig.DefaultConfig()
	cfg.Gateway.Enabled = true
	cfg.Gateway.ListenAddr = "127.0.0.1:0"
	cfg.Gateway.Token = "secret-token"
	if err := instance.Start(cfg.Gateway); err != nil {
		t.Fatalf("Start() error = %v", err)
	}
	t.Cleanup(func() { _ = instance.Stop(context.Background()) })
	service.gateway = instance

	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(2)
		go func() {
			defer wg.Done()
			for j := 0; j < 40; j++ {
				_ = service.GetState()
			}
		}()
		go func() {
			defer wg.Done()
			for j := 0; j < 40; j++ {
				if j%2 == 0 {
					service.reconcileGateway(cfg)
					continue
				}
				service.stopGatewayBestEffort()
			}
		}()
	}
	wg.Wait()
}
