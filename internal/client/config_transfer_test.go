package client

import (
	"context"
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
	want.LegacyLog = configTransferTestBoolPtr(true)
	want.Observability = serverconfig.ObservabilityConfig{
		Mode:          serverconfig.ObservabilityModeFull,
		RetentionDays: serverconfig.DefaultObservabilityRetentionDays,
		MaxDiskMB:     serverconfig.DefaultObservabilityMaxDiskMB,
	}
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
	if got.Observability.Mode != serverconfig.ObservabilityModeFull || len(got.ModelAdapters) != 1 {
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
	current.Observability = serverconfig.ObservabilityConfig{
		Mode:          serverconfig.ObservabilityModeFull,
		RetentionDays: serverconfig.DefaultObservabilityRetentionDays,
		MaxDiskMB:     serverconfig.DefaultObservabilityMaxDiskMB,
	}
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
	if persisted.Observability.Mode != serverconfig.ObservabilityModeFull {
		t.Fatal("invalid import overwrote the existing config")
	}
}

func TestImportUserConfigReplacesLastAgentModelHash(t *testing.T) {
	service := newConfigTransferTestService(t)
	current := serverconfig.DefaultConfig()
	current.LastAgentModelHash = "live-hash"
	current.Appearance.Theme = "light"
	if _, err := service.store.Save(context.Background(), current); err != nil {
		t.Fatalf("seed Save() error = %v", err)
	}

	path := filepath.Join(t.TempDir(), "imported.yaml")
	content := "lastAgentModelHash: imported-hash\nappearance:\n  theme: dark\nmodelAdapters: []\n"
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}
	got, err := service.ImportUserConfig(path)
	if err != nil {
		t.Fatalf("ImportUserConfig() error = %v", err)
	}
	if got.LastAgentModelHash != "imported-hash" {
		t.Fatalf("imported hash = %q, want imported-hash", got.LastAgentModelHash)
	}
	if got.Appearance.Theme != "dark" {
		t.Fatalf("imported theme = %q, want dark", got.Appearance.Theme)
	}

	persisted, err := service.LoadUserConfig()
	if err != nil {
		t.Fatalf("LoadUserConfig() error = %v", err)
	}
	if persisted.LastAgentModelHash != "imported-hash" {
		t.Fatalf("persisted hash = %q, want imported-hash", persisted.LastAgentModelHash)
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

func TestReadModelAdaptersForImportExtractsModelsWithoutSaving(t *testing.T) {
	service := newConfigTransferTestService(t)
	current := serverconfig.DefaultConfig()
	current.Observability = serverconfig.ObservabilityConfig{
		Mode:          serverconfig.ObservabilityModeFull,
		RetentionDays: serverconfig.DefaultObservabilityRetentionDays,
		MaxDiskMB:     serverconfig.DefaultObservabilityMaxDiskMB,
	}
	current.Appearance.Theme = "dark"
	current.ModelAdapters = []serverconfig.ModelAdapterConfig{{
		DisplayName: "已有模型",
		Type:        "openai",
		BaseURL:     "https://live.example/v1",
		APIKey:      "live-secret",
		TooltipData: "live",
		ModelID:     "live-model",
	}}
	if err := service.SaveUserConfig(current); err != nil {
		t.Fatalf("SaveUserConfig() error = %v", err)
	}

	path := filepath.Join(t.TempDir(), "exported.yaml")
	content := strings.Join([]string{
		"observability:",
		"  mode: off",
		"appearance:",
		"  theme: light",
		"unknownRootField: ignored",
		"modelAdapters:",
		"  - displayName: 导入模型",
		"    type: openai",
		"    baseURL: https://import.example/v1",
		"    apiKey: import-secret",
		"    tooltipData: imported",
		"    modelID: import-model",
		"    reasoningEffort: medium",
		"    openAIEndpoint: /v1/responses",
		"    outboundProxy:",
		"      enabled: true",
		"      url: socks5://127.0.0.1:1080",
		"",
	}, "\n")
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}

	got, err := service.ReadModelAdaptersForImport(path)
	if err != nil {
		t.Fatalf("ReadModelAdaptersForImport() error = %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("ReadModelAdaptersForImport() len = %d, want 1", len(got))
	}
	if got[0].DisplayName != "导入模型" || got[0].APIKey != "import-secret" || got[0].ModelID != "import-model" {
		t.Fatalf("imported adapters = %#v", got)
	}
	if !got[0].OutboundProxy.Enabled || got[0].OutboundProxy.URL != "socks5://127.0.0.1:1080" {
		t.Fatalf("imported outboundProxy = %#v", got[0].OutboundProxy)
	}

	persisted, err := service.LoadUserConfig()
	if err != nil {
		t.Fatalf("LoadUserConfig() error = %v", err)
	}
	if persisted.Observability.Mode != serverconfig.ObservabilityModeFull {
		t.Fatalf("observability.mode = %q, want full", persisted.Observability.Mode)
	}
	if persisted.Appearance.Theme != "dark" {
		t.Fatalf("appearance.theme = %q, want dark", persisted.Appearance.Theme)
	}
	if len(persisted.ModelAdapters) != 1 || persisted.ModelAdapters[0].DisplayName != "已有模型" {
		t.Fatalf("persisted adapters = %#v", persisted.ModelAdapters)
	}
}

func TestReadModelAdaptersForImportAllowsRunningService(t *testing.T) {
	service := newConfigTransferTestService(t)
	path := filepath.Join(t.TempDir(), "models.yaml")
	if err := os.WriteFile(path, []byte("backendListenAddr: '127.0.0.1:1'\nmodelAdapters: []\n"), 0o600); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}
	if _, err := service.ReadModelAdaptersForImport(path); err != nil {
		t.Fatalf("ReadModelAdaptersForImport() error = %v", err)
	}
}

func TestReadModelAdaptersForImportMissingModelAdaptersReturnsEmpty(t *testing.T) {
	service := newConfigTransferTestService(t)
	path := filepath.Join(t.TempDir(), "settings-only.yaml")
	if err := os.WriteFile(path, []byte("appearance:\n  theme: dark\n"), 0o600); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}
	got, err := service.ReadModelAdaptersForImport(path)
	if err != nil {
		t.Fatalf("ReadModelAdaptersForImport() error = %v", err)
	}
	if got == nil || len(got) != 0 {
		t.Fatalf("ReadModelAdaptersForImport() = %#v, want empty slice", got)
	}
}

func TestReadModelAdaptersForImportRejectsMultipleDocuments(t *testing.T) {
	service := newConfigTransferTestService(t)
	path := filepath.Join(t.TempDir(), "multiple.yaml")
	content := "modelAdapters: []\n---\nmodelAdapters: []\n"
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}
	if _, err := service.ReadModelAdaptersForImport(path); err == nil || !strings.Contains(err.Error(), "单个 YAML 文档") {
		t.Fatalf("ReadModelAdaptersForImport() error = %v, want multiple document error", err)
	}
}

func TestReadModelAdaptersForImportRejectsOversizedFile(t *testing.T) {
	service := newConfigTransferTestService(t)
	path := filepath.Join(t.TempDir(), "oversized.yaml")
	content := make([]byte, maxConfigTransferFileSize+1)
	if err := os.WriteFile(path, content, 0o600); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}
	if _, err := service.ReadModelAdaptersForImport(path); err == nil || !strings.Contains(err.Error(), "不能超过") {
		t.Fatalf("ReadModelAdaptersForImport() error = %v, want size limit error", err)
	}
}

func TestReadModelAdaptersForImportDoesNotRequireKnownRootFields(t *testing.T) {
	raw := []byte("legacySetting: true\nmodelAdapters:\n  - displayName: only-models\n    type: anthropic\n    baseURL: https://anthropic.example\n    apiKey: secret\n    tooltipData: note\n    modelID: claude\n")
	got, err := decodeImportedModelAdapters(raw)
	if err != nil {
		t.Fatalf("decodeImportedModelAdapters() error = %v", err)
	}
	if len(got) != 1 || got[0].DisplayName != "only-models" || got[0].Type != "anthropic" {
		t.Fatalf("decodeImportedModelAdapters() = %#v", got)
	}
}

func TestReadModelAdaptersForImportRestoresCanonicalIDsFromExportedYAML(t *testing.T) {
	service := newConfigTransferTestService(t)
	want := configTransferPhysicalAliasCollection(t)
	cfg := serverconfig.DefaultConfig()
	cfg.ModelAdapters = want
	if err := service.SaveUserConfig(cfg); err != nil {
		t.Fatalf("SaveUserConfig() error = %v", err)
	}
	persisted, err := service.LoadUserConfig()
	if err != nil {
		t.Fatalf("LoadUserConfig() error = %v", err)
	}
	if len(persisted.ModelAdapters) != len(want) {
		t.Fatalf("persisted adapters len = %d, want %d", len(persisted.ModelAdapters), len(want))
	}

	exportPath, err := service.ExportUserConfig(filepath.Join(t.TempDir(), "physical-alias.yaml"))
	if err != nil {
		t.Fatalf("ExportUserConfig() error = %v", err)
	}
	exported, err := os.ReadFile(exportPath)
	if err != nil {
		t.Fatalf("ReadFile() error = %v", err)
	}
	var exportedDocument importedModelAdaptersDocument
	if err := yaml.Unmarshal(exported, &exportedDocument); err != nil {
		t.Fatalf("yaml.Unmarshal() error = %v", err)
	}
	if len(exportedDocument.ModelAdapters) != len(want) {
		t.Fatalf("exported adapters len = %d, want %d", len(exportedDocument.ModelAdapters), len(want))
	}
	for i, row := range exportedDocument.ModelAdapters {
		if row.ID != "" {
			t.Fatalf("exported YAML included adapter[%d] ID %q", i, row.ID)
		}
	}

	got, err := service.ReadModelAdaptersForImport(exportPath)
	if err != nil {
		t.Fatalf("ReadModelAdaptersForImport() error = %v", err)
	}
	if len(got) != len(persisted.ModelAdapters) {
		t.Fatalf("ReadModelAdaptersForImport() len = %d, want %d", len(got), len(persisted.ModelAdapters))
	}
	for i := range persisted.ModelAdapters {
		if got[i].ID != persisted.ModelAdapters[i].ID {
			t.Fatalf("imported adapter[%d].ID = %q, want %q", i, got[i].ID, persisted.ModelAdapters[i].ID)
		}
	}
	alias := persisted.ModelAdapters[0]
	importedAlias := got[0]
	if !importedAlias.ProviderFallback.Enabled {
		t.Fatal("imported alias lost providerFallback.enabled")
	}
	if importedAlias.ProviderFallback.PrimaryChannelID != alias.ProviderFallback.PrimaryChannelID {
		t.Fatalf("imported primaryChannelID = %q, want %q", importedAlias.ProviderFallback.PrimaryChannelID, alias.ProviderFallback.PrimaryChannelID)
	}
	if len(importedAlias.ProviderFallback.CandidateChannelIDs) != 1 || importedAlias.ProviderFallback.CandidateChannelIDs[0] != alias.ProviderFallback.CandidateChannelIDs[0] {
		t.Fatalf("imported candidateChannelIDs = %#v, want %#v", importedAlias.ProviderFallback.CandidateChannelIDs, alias.ProviderFallback.CandidateChannelIDs)
	}
	if importedAlias.ProviderFallback.PrimaryChannelID != persisted.ModelAdapters[1].ID {
		t.Fatalf("primaryChannelID = %q, want physical ID %q", importedAlias.ProviderFallback.PrimaryChannelID, persisted.ModelAdapters[1].ID)
	}
	if importedAlias.ProviderFallback.CandidateChannelIDs[0] != persisted.ModelAdapters[2].ID {
		t.Fatalf("candidateChannelID = %q, want physical ID %q", importedAlias.ProviderFallback.CandidateChannelIDs[0], persisted.ModelAdapters[2].ID)
	}
	if !importedAlias.OutboundProxy.Enabled || importedAlias.OutboundProxy.URL != "socks5://127.0.0.1:1080" {
		t.Fatalf("imported outboundProxy = %#v", importedAlias.OutboundProxy)
	}
}

func TestReadModelAdaptersForImportAcceptsPartialAliasReferencingExistingChannels(t *testing.T) {
	collection := configTransferPhysicalAliasCollection(t)
	alias := collection[0]
	if _, err := serverconfig.NormalizeModelAdapterConfigs([]serverconfig.ModelAdapterConfig{alias}); err == nil {
		t.Fatal("NormalizeModelAdapterConfigs should reject alias without the candidate set")
	}

	payload, err := yaml.Marshal(serverconfig.Config{ModelAdapters: []serverconfig.ModelAdapterConfig{alias}})
	if err != nil {
		t.Fatalf("yaml.Marshal() error = %v", err)
	}
	got, err := decodeImportedModelAdapters(payload)
	if err != nil {
		t.Fatalf("decodeImportedModelAdapters() error = %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("decodeImportedModelAdapters() len = %d, want 1", len(got))
	}
	if got[0].ID != alias.ID {
		t.Fatalf("partial alias ID = %q, want canonical %q", got[0].ID, alias.ID)
	}
	if !got[0].ProviderFallback.Enabled || got[0].ProviderFallback.PrimaryChannelID != alias.ProviderFallback.PrimaryChannelID {
		t.Fatalf("partial alias fallback = %+v, want %+v", got[0].ProviderFallback, alias.ProviderFallback)
	}
	if len(got[0].ProviderFallback.CandidateChannelIDs) != 1 || got[0].ProviderFallback.CandidateChannelIDs[0] != alias.ProviderFallback.CandidateChannelIDs[0] {
		t.Fatalf("partial alias candidates = %#v, want %#v", got[0].ProviderFallback.CandidateChannelIDs, alias.ProviderFallback.CandidateChannelIDs)
	}
	if !got[0].OutboundProxy.Enabled || got[0].OutboundProxy.URL != alias.OutboundProxy.URL {
		t.Fatalf("partial alias outboundProxy = %#v", got[0].OutboundProxy)
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

func TestDecodeImportedUserConfigRoutingContract(t *testing.T) {
	// noad schema 为准：routing 仅支持 mode 字段。
	raw := []byte("modelAdapters: []\nrouting:\n  mode: local\n")
	got, err := decodeImportedUserConfig(raw)
	if err != nil {
		t.Fatalf("decodeImportedUserConfig() error = %v", err)
	}
	if got.Routing.Mode != "local" || len(got.ModelAdapters) != 0 {
		t.Fatalf("decodeImportedUserConfig() = %#v", got)
	}

	// main 时代的 legacy routing(strategy) 不属于 noad schema，按未知字段拒绝。
	legacy := []byte("modelAdapters: []\nrouting:\n  strategy: legacy\n")
	if _, err := decodeImportedUserConfig(legacy); err == nil || !strings.Contains(err.Error(), "未知字段") {
		t.Fatalf("decodeImportedUserConfig(legacy routing) error = %v, want unknown field error", err)
	}
}

func newConfigTransferTestService(t *testing.T) *ProxyService {
	t.Helper()
	root := t.TempDir()
	return &ProxyService{
		store: serverconfig.NewStore(filepath.Join(root, "config.yaml"), filepath.Join(root, "logs")),
	}
}

func configTransferPhysicalAliasCollection(t *testing.T) []serverconfig.ModelAdapterConfig {
	t.Helper()
	physicalA := serverconfig.ModelAdapterConfig{
		DisplayName:     "physical-a",
		Type:            "openai",
		BaseURL:         "https://api-a.example.com/v1",
		APIKey:          "secret-a",
		TooltipData:     "physical-a",
		ModelID:         "model-a",
		ReasoningEffort: "medium",
		OpenAIEndpoint:  "/v1/responses",
	}
	physicalA.OutboundProxy.Enabled = true
	physicalA.OutboundProxy.URL = "socks5://127.0.0.1:1080"
	physicalB := serverconfig.ModelAdapterConfig{
		DisplayName:     "physical-b",
		Type:            "openai",
		BaseURL:         "https://api-b.example.com/v1",
		APIKey:          "secret-b",
		TooltipData:     "physical-b",
		ModelID:         "model-b",
		ReasoningEffort: "medium",
		OpenAIEndpoint:  "/v1/responses",
	}
	physicalC := serverconfig.ModelAdapterConfig{
		DisplayName:     "physical-c",
		Type:            "openai",
		BaseURL:         "https://api-c.example.com/v1",
		APIKey:          "secret-c",
		TooltipData:     "physical-c",
		ModelID:         "model-c",
		ReasoningEffort: "medium",
		OpenAIEndpoint:  "/v1/responses",
	}
	ids, err := serverconfig.NormalizeModelAdapterConfigs([]serverconfig.ModelAdapterConfig{physicalA, physicalB, physicalC})
	if err != nil {
		t.Fatalf("NormalizeModelAdapterConfigs() error = %v", err)
	}
	physicalA.ProviderFallback = serverconfig.ProviderFallbackConfig{
		Enabled:             true,
		PrimaryChannelID:    ids[1].ID,
		CandidateChannelIDs: []string{ids[2].ID},
	}
	normalized, err := serverconfig.NormalizeModelAdapterConfigs([]serverconfig.ModelAdapterConfig{physicalA, physicalB, physicalC})
	if err != nil {
		t.Fatalf("NormalizeModelAdapterConfigs(alias collection) error = %v", err)
	}
	return normalized
}

func configTransferTestBoolPtr(value bool) *bool {
	return &value
}
