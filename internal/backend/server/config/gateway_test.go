package config

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

func TestDefaultGatewayConfigIsDisabledLoopback(t *testing.T) {
	cfg := DefaultConfig()
	if cfg.Gateway.Enabled {
		t.Fatal("gateway must be disabled by default")
	}
	if cfg.Gateway.ListenAddr != DefaultGatewayListenAddr {
		t.Fatalf("listenAddr = %q, want %q", cfg.Gateway.ListenAddr, DefaultGatewayListenAddr)
	}
	if cfg.Gateway.Token != "" || cfg.Gateway.TokenConfigured {
		t.Fatalf("default token leaked: %+v", cfg.Gateway)
	}
	if cfg.Gateway.PublicModels == nil || len(cfg.Gateway.PublicModels) != 0 {
		t.Fatalf("publicModels = %#v, want empty slice", cfg.Gateway.PublicModels)
	}
}

func TestNormalizeGatewayConfigRejectsNonLoopbackAndCursorPorts(t *testing.T) {
	cfg := DefaultConfig()
	cfg.Gateway.ListenAddr = "0.0.0.0:18091"
	if _, err := NormalizeConfig(cfg); err == nil || !strings.Contains(err.Error(), "loopback") {
		t.Fatalf("non-loopback error = %v", err)
	}
	cfg.Gateway.ListenAddr = DefaultBackendListenAddr
	if _, err := NormalizeConfig(cfg); err == nil || !strings.Contains(err.Error(), "不能占用 Cursor 端口") {
		t.Fatalf("18090 error = %v", err)
	}
	cfg.Gateway.ListenAddr = DefaultProxyListenAddr
	if _, err := NormalizeConfig(cfg); err == nil || !strings.Contains(err.Error(), "不能占用 Cursor 端口") {
		t.Fatalf("18080 error = %v", err)
	}
	for _, addr := range []string{"localhost:18080", "localhost:18090", "[::1]:18080", "[::1]:18090"} {
		cfg.Gateway.ListenAddr = addr
		if _, err := NormalizeConfig(cfg); err == nil || !strings.Contains(err.Error(), "不能占用 Cursor 端口") {
			t.Fatalf("reserved alias %s error = %v", addr, err)
		}
	}
	cfg.Gateway.ListenAddr = "localhost:18091"
	if _, err := NormalizeConfig(cfg); err != nil {
		t.Fatalf("localhost:18091 error = %v", err)
	}
	cfg.Gateway.ListenAddr = "[::1]:18091"
	if _, err := NormalizeConfig(cfg); err != nil {
		t.Fatalf("[::1]:18091 error = %v", err)
	}
}

func TestNormalizeGatewayConfigRejectsDuplicatePublicModels(t *testing.T) {
	cfg := DefaultConfig()
	cfg.Gateway.PublicModels = []GatewayPublicModel{
		{ID: "alias", TargetAdapterID: "adapter-a"},
		{ID: "alias", TargetAdapterID: "adapter-b"},
	}
	if _, err := NormalizeConfig(cfg); err == nil || !strings.Contains(err.Error(), "重复") {
		t.Fatalf("duplicate error = %v", err)
	}
}

func TestGatewayJSONOmitsTokenAndYAMLKeepsToken(t *testing.T) {
	cfg := DefaultConfig()
	cfg.Gateway.Enabled = true
	cfg.Gateway.Token = "super-secret-gateway-token"
	cfg.Gateway.PublicModels = []GatewayPublicModel{{ID: "public-a", TargetAdapterID: "adapter-a"}}
	normalized, err := NormalizeConfig(cfg)
	if err != nil {
		t.Fatalf("NormalizeConfig() error = %v", err)
	}
	encoded, err := json.Marshal(normalized)
	if err != nil {
		t.Fatalf("json.Marshal() error = %v", err)
	}
	text := string(encoded)
	if strings.Contains(text, "super-secret-gateway-token") || strings.Contains(text, `"token"`) {
		t.Fatalf("JSON leaked token: %s", text)
	}
	if !strings.Contains(text, `"tokenConfigured":true`) {
		t.Fatalf("JSON missing tokenConfigured: %s", text)
	}

	yamlBytes, err := yaml.Marshal(normalized)
	if err != nil {
		t.Fatalf("yaml.Marshal() error = %v", err)
	}
	if !strings.Contains(string(yamlBytes), "super-secret-gateway-token") {
		t.Fatalf("YAML dropped token:\n%s", yamlBytes)
	}
}

func TestGatewayJSONUnmarshalIgnoresSubmittedToken(t *testing.T) {
	var cfg Config
	raw := []byte(`{"gateway":{"enabled":true,"listenAddr":"127.0.0.1:18091","token":"from-ui","publicModels":[]}}`)
	if err := json.Unmarshal(raw, &cfg); err != nil {
		t.Fatalf("json.Unmarshal() error = %v", err)
	}
	if cfg.Gateway.Token != "" {
		t.Fatalf("JSON unmarshal accepted token = %q", cfg.Gateway.Token)
	}
}

func TestStoreSaveUserConfigPreservesGatewayToken(t *testing.T) {
	store := newWriteTestStore(t)
	seed := seedWriteTestConfig(t, store, func(cfg *Config) {
		cfg.Gateway.Enabled = true
		cfg.Gateway.Token = "live-token"
		cfg.Appearance.Theme = "light"
	})
	if seed.Gateway.Token != "live-token" {
		t.Fatalf("seed token = %q", seed.Gateway.Token)
	}

	stale := seed
	stale.Gateway.Token = "stale-ui-token"
	stale.Gateway.Enabled = true
	stale.Appearance.Theme = "dark"
	got, err := store.SaveUserConfig(context.Background(), stale)
	if err != nil {
		t.Fatalf("SaveUserConfig() error = %v", err)
	}
	if got.Gateway.Token != "live-token" {
		t.Fatalf("token = %q, want live-token", got.Gateway.Token)
	}
	if got.Appearance.Theme != "dark" {
		t.Fatalf("theme = %q, want dark", got.Appearance.Theme)
	}
}

func TestStoreSaveGeneratesTokenWhenEnabledAndEmpty(t *testing.T) {
	store := newWriteTestStore(t)
	cfg := DefaultConfig()
	cfg.Gateway.Enabled = true
	saved, err := store.Save(context.Background(), cfg)
	if err != nil {
		t.Fatalf("Save() error = %v", err)
	}
	if saved.Gateway.Token == "" || len(saved.Gateway.Token) != GatewayTokenByteLength*2 {
		t.Fatalf("generated token = %q", saved.Gateway.Token)
	}
}

func TestStoreWritesConfigWith0600AndPreGatewayBackup(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "config.yaml")
	legacy := "log: true\nbackendListenAddr: 127.0.0.1:18090\nproxyListenAddr: 127.0.0.1:18080\n"
	if err := os.WriteFile(path, []byte(legacy), 0o644); err != nil {
		t.Fatalf("write legacy: %v", err)
	}
	store := NewStore(path, filepath.Join(root, "logs"))
	if _, err := store.Load(context.Background()); err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	backupPath := path + PreGatewayBackupSuffix
	backup, err := os.ReadFile(backupPath)
	if err != nil {
		t.Fatalf("read backup: %v", err)
	}
	if !strings.Contains(string(backup), "log: true") || strings.Contains(string(backup), "gateway:") {
		t.Fatalf("backup should be pre-gateway original:\n%s", backup)
	}
	persisted, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read persisted: %v", err)
	}
	if !strings.Contains(string(persisted), "gateway:") {
		t.Fatalf("persisted config missing gateway:\n%s", persisted)
	}
	if runtime.GOOS == "windows" {
		return
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat config: %v", err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("config mode = %o, want 600", info.Mode().Perm())
	}
	backupInfo, err := os.Stat(backupPath)
	if err != nil {
		t.Fatalf("stat backup: %v", err)
	}
	if backupInfo.Mode().Perm() != 0o600 {
		t.Fatalf("backup mode = %o, want 600", backupInfo.Mode().Perm())
	}
}

func TestLegacyStructDropsGatewayFieldsOnResave(t *testing.T) {
	type legacyConfig struct {
		BackendListenAddr string `yaml:"backendListenAddr"`
		ProxyListenAddr   string `yaml:"proxyListenAddr"`
	}
	current := DefaultConfig()
	current.Gateway.Enabled = true
	current.Gateway.Token = "must-not-survive-downgrade"
	raw, err := yaml.Marshal(current)
	if err != nil {
		t.Fatalf("marshal current: %v", err)
	}
	var old legacyConfig
	if err := yaml.Unmarshal(raw, &old); err != nil {
		t.Fatalf("unmarshal legacy: %v", err)
	}
	rewritten, err := yaml.Marshal(old)
	if err != nil {
		t.Fatalf("marshal legacy: %v", err)
	}
	if strings.Contains(string(rewritten), "gateway:") || strings.Contains(string(rewritten), "must-not-survive-downgrade") {
		t.Fatalf("legacy resave retained gateway:\n%s", rewritten)
	}
}

func TestResolveGatewayPublicModelRequiresExplicitMapping(t *testing.T) {
	adapter := testModelAdapter("physical", 1)
	normalized, err := NormalizeModelAdapterConfigs([]ModelAdapterConfig{adapter})
	if err != nil {
		t.Fatalf("normalize adapters: %v", err)
	}
	cfg := DefaultConfig()
	cfg.ModelAdapters = normalized
	cfg.Gateway.PublicModels = []GatewayPublicModel{{ID: "public-a", TargetAdapterID: normalized[0].ID}}
	cfg, err = NormalizeConfig(cfg)
	if err != nil {
		t.Fatalf("NormalizeConfig() error = %v", err)
	}

	target, stale, ok := ResolveGatewayPublicModel(cfg, "public-a")
	if !ok || stale || target != normalized[0].ID {
		t.Fatalf("explicit map = %q stale=%t ok=%t", target, stale, ok)
	}
	if _, _, ok := ResolveGatewayPublicModel(cfg, normalized[0].ID); ok {
		t.Fatal("internal hash must not resolve without explicit mapping")
	}
	if _, _, ok := ResolveGatewayPublicModel(cfg, normalized[0].ModelID); ok {
		t.Fatal("provider modelID must not resolve without explicit mapping")
	}

	cfg.Gateway.PublicModels = []GatewayPublicModel{{ID: "stale-a", TargetAdapterID: "missing-adapter"}}
	cfg, err = NormalizeConfig(cfg)
	if err != nil {
		t.Fatalf("NormalizeConfig() stale mapping error = %v", err)
	}
	target, stale, ok = ResolveGatewayPublicModel(cfg, "stale-a")
	if !ok || !stale || target != "missing-adapter" {
		t.Fatalf("stale map = %q stale=%t ok=%t", target, stale, ok)
	}
}

func TestStripGatewayTokenRemovesSecret(t *testing.T) {
	cfg := DefaultConfig()
	cfg.Gateway.Token = "secret-token"
	cfg.Gateway.TokenConfigured = true
	stripped := StripGatewayToken(cfg)
	if stripped.Gateway.Token != "" || stripped.Gateway.TokenConfigured {
		t.Fatalf("strip failed: %+v", stripped.Gateway)
	}
	if cfg.Gateway.Token != "secret-token" {
		t.Fatal("StripGatewayToken mutated the original")
	}
}
