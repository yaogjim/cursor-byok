package client

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	serverconfig "cursor/internal/backend/server/config"

	"gopkg.in/yaml.v3"
)

func TestExportAndImportUserConfigRoundTrip(t *testing.T) {
	source := newConfigTransferTestService(t)
	want := serverconfig.DefaultConfig()
	want.Log = true
	want.ModelAdapters = []serverconfig.ModelAdapterConfig{{
		DisplayName:     "迁移模型",
		Type:            "openai",
		BaseURL:         "https://provider.example/v1",
		APIKey:          "migration-secret",
		TooltipData:     "迁移备注",
		ModelID:         "model-a",
		ReasoningEffort: "medium",
		OpenAIEndpoint:  "/v1/responses",
	}}
	if err := source.SaveUserConfig(want); err != nil {
		t.Fatalf("SaveUserConfig() error = %v", err)
	}

	exportPath, err := source.ExportUserConfig(filepath.Join(t.TempDir(), "cursor-byok-backup"))
	if err != nil {
		t.Fatalf("ExportUserConfig() error = %v", err)
	}
	if filepath.Ext(exportPath) != ".yaml" {
		t.Fatalf("ExportUserConfig() path = %q, want .yaml extension", exportPath)
	}
	if runtime.GOOS != "windows" {
		info, statErr := os.Stat(exportPath)
		if statErr != nil {
			t.Fatalf("Stat() error = %v", statErr)
		}
		if gotMode := info.Mode().Perm(); gotMode != 0o600 {
			t.Fatalf("export mode = %o, want 600", gotMode)
		}
	}

	target := newConfigTransferTestService(t)
	got, err := target.ImportUserConfig(exportPath)
	if err != nil {
		t.Fatalf("ImportUserConfig() error = %v", err)
	}
	if !got.Log || len(got.ModelAdapters) != 1 {
		t.Fatalf("ImportUserConfig() = %#v", got)
	}
	adapter := got.ModelAdapters[0]
	if adapter.DisplayName != "迁移模型" || adapter.APIKey != "migration-secret" || adapter.ModelID != "model-a" {
		t.Fatalf("imported adapter = %#v", adapter)
	}

	persisted, err := target.LoadUserConfig()
	if err != nil {
		t.Fatalf("LoadUserConfig() error = %v", err)
	}
	if len(persisted.ModelAdapters) != 1 || persisted.ModelAdapters[0].APIKey != "migration-secret" {
		t.Fatalf("persisted config = %#v", persisted)
	}
}

func TestWriteExportedUserConfigReplacesExistingFile(t *testing.T) {
	directory := t.TempDir()
	path := filepath.Join(directory, "config.yaml")
	if err := os.WriteFile(path, []byte("old"), 0o600); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}
	if err := writeExportedUserConfig(path, []byte("new")); err != nil {
		t.Fatalf("writeExportedUserConfig() error = %v", err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile() error = %v", err)
	}
	if string(data) != "new" {
		t.Fatalf("exported content = %q, want new", data)
	}
	matches, err := filepath.Glob(filepath.Join(directory, ".cursor-byok-config-*.tmp"))
	if err != nil || len(matches) != 0 {
		t.Fatalf("temporary exports = %v, error = %v", matches, err)
	}
}

func TestEnsureConfigImportAllowedRejectsRunningService(t *testing.T) {
	for _, state := range []ProxyState{
		{BackendRunning: true},
		{ProxyRunning: true},
		{Running: true},
	} {
		if err := ensureConfigImportAllowed(state); err == nil {
			t.Fatalf("ensureConfigImportAllowed(%+v) error = nil", state)
		}
	}
	if err := ensureConfigImportAllowed(ProxyState{}); err != nil {
		t.Fatalf("ensureConfigImportAllowed(stopped) error = %v", err)
	}
}

func TestImportUserConfigWaitsForLifecycleTransition(t *testing.T) {
	service := newConfigTransferTestService(t)
	path := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(path, []byte("modelAdapters: []\n"), 0o600); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}

	service.lifecycleMu.Lock()
	done := make(chan error, 1)
	go func() {
		_, err := service.ImportUserConfig(path)
		done <- err
	}()
	select {
	case err := <-done:
		service.lifecycleMu.Unlock()
		t.Fatalf("ImportUserConfig() completed during lifecycle transition: %v", err)
	case <-time.After(100 * time.Millisecond):
	}
	service.lifecycleMu.Unlock()

	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("ImportUserConfig() error = %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("ImportUserConfig() did not resume after lifecycle transition")
	}
}

func TestDecodeImportedUserConfigRejectsNonMappingDocuments(t *testing.T) {
	for _, raw := range []string{"", "null\n", "[]\n", "value\n"} {
		if _, err := decodeImportedUserConfig([]byte(raw)); err == nil {
			t.Fatalf("decodeImportedUserConfig(%q) error = nil", raw)
		}
	}
}

func TestDecodeImportedUserConfigAcceptsEmptyReasoningEffort(t *testing.T) {
	raw := []byte("modelAdapters:\n  - displayName: model\n    type: openai\n    baseURL: https://example.com/v1\n    apiKey: secret\n    tooltipData: migrated model\n    modelID: model-a\n    reasoningEffort: ''\n    openAIEndpoint: /v1/responses\n")
	got, err := decodeImportedUserConfig(raw)
	if err != nil {
		t.Fatalf("decodeImportedUserConfig() error = %v", err)
	}
	if got.ModelAdapters[0].ReasoningEffort != "" {
		t.Fatalf("reasoningEffort = %q, want empty", got.ModelAdapters[0].ReasoningEffort)
	}
}

func TestImportUserConfigRejectsUnknownFieldsWithoutOverwriting(t *testing.T) {
	service := newConfigTransferTestService(t)
	current := serverconfig.DefaultConfig()
	current.Log = true
	if err := service.SaveUserConfig(current); err != nil {
		t.Fatalf("SaveUserConfig() error = %v", err)
	}

	path := filepath.Join(t.TempDir(), "unknown.yaml")
	if err := os.WriteFile(path, []byte("modelAdapters: []\nunknownSetting: true\n"), 0o600); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}
	if _, err := service.ImportUserConfig(path); err == nil || !strings.Contains(err.Error(), "未知字段") {
		t.Fatalf("ImportUserConfig() error = %v, want unknown field error", err)
	}

	persisted, err := service.LoadUserConfig()
	if err != nil {
		t.Fatalf("LoadUserConfig() error = %v", err)
	}
	if !persisted.Log {
		t.Fatal("invalid import overwrote the existing config")
	}
}

func TestImportUserConfigRejectsMultipleDocuments(t *testing.T) {
	service := newConfigTransferTestService(t)
	path := filepath.Join(t.TempDir(), "multiple.yaml")
	content := "modelAdapters: []\n---\nmodelAdapters: []\n"
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}
	if _, err := service.ImportUserConfig(path); err == nil || !strings.Contains(err.Error(), "单个 YAML 文档") {
		t.Fatalf("ImportUserConfig() error = %v, want multiple document error", err)
	}
}

func TestImportUserConfigRejectsOversizedFile(t *testing.T) {
	service := newConfigTransferTestService(t)
	path := filepath.Join(t.TempDir(), "oversized.yaml")
	content := make([]byte, maxConfigTransferFileSize+1)
	if err := os.WriteFile(path, content, 0o600); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}
	if _, err := service.ImportUserConfig(path); err == nil || !strings.Contains(err.Error(), "不能超过") {
		t.Fatalf("ImportUserConfig() error = %v, want size limit error", err)
	}
}

func TestDecodeImportedUserConfigNormalizesValues(t *testing.T) {
	raw := []byte("backendListenAddr: ' 127.0.0.1:12345 '\nproxyListenAddr: '127.0.0.1:12346'\nmodelAdapters: []\n")
	got, err := decodeImportedUserConfig(raw)
	if err != nil {
		t.Fatalf("decodeImportedUserConfig() error = %v", err)
	}
	if got.BackendListenAddr != "127.0.0.1:12345" || got.ProxyListenAddr != "127.0.0.1:12346" {
		t.Fatalf("decodeImportedUserConfig() = %#v", got)
	}

	encoded, err := yaml.Marshal(got)
	if err != nil {
		t.Fatalf("yaml.Marshal() error = %v", err)
	}
	if !strings.Contains(string(encoded), "backendListenAddr: 127.0.0.1:12345") {
		t.Fatalf("encoded config = %s", encoded)
	}
}

func TestDecodeImportedUserConfigAcceptsLegacyRouting(t *testing.T) {
	raw := []byte("modelAdapters: []\nrouting:\n  strategy: legacy\n")
	got, err := decodeImportedUserConfig(raw)
	if err != nil {
		t.Fatalf("decodeImportedUserConfig() error = %v", err)
	}
	if len(got.ModelAdapters) != 0 {
		t.Fatalf("decodeImportedUserConfig() = %#v", got)
	}
}

func newConfigTransferTestService(t *testing.T) *ProxyService {
	t.Helper()
	root := t.TempDir()
	return &ProxyService{
		store: serverconfig.NewStore(filepath.Join(root, "config.yaml"), filepath.Join(root, "logs")),
	}
}
