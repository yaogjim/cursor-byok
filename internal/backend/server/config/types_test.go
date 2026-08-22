package config

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestNormalizeConfigMigratesLegacyLogFlag(t *testing.T) {
	legacyFull := true
	full, err := NormalizeConfig(Config{LegacyLog: &legacyFull})
	if err != nil {
		t.Fatalf("normalize legacy full config: %v", err)
	}
	if full.Observability.Mode != ObservabilityModeFull {
		t.Fatalf("expected legacy log=true to map to full, got %+v", full.Observability)
	}

	legacyOff := false
	off, err := NormalizeConfig(Config{LegacyLog: &legacyOff})
	if err != nil {
		t.Fatalf("normalize legacy off config: %v", err)
	}
	if off.Observability.Mode != ObservabilityModeOff {
		t.Fatalf("expected legacy log=false to map to off, got %+v", off.Observability)
	}
}

func TestNormalizeConfigPrefersStructuredObservability(t *testing.T) {
	legacyFull := true
	config, err := NormalizeConfig(Config{
		LegacyLog: &legacyFull,
		Observability: ObservabilityConfig{
			Mode: ObservabilityModeBasic,
		},
	})
	if err != nil {
		t.Fatalf("normalize structured config: %v", err)
	}
	if config.Observability.Mode != ObservabilityModeBasic {
		t.Fatalf("expected basic mode, got %q", config.Observability.Mode)
	}
}

func TestNormalizeConfigRejectsInvalidObservabilityMode(t *testing.T) {
	_, err := NormalizeConfig(Config{
		Observability: ObservabilityConfig{Mode: "verbose"},
	})
	if err == nil {
		t.Fatal("expected invalid observability mode error")
	}
}

func TestStorePersistsStructuredObservabilityPrecedence(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "config.yaml")
	configYAML := "log: true\nobservability:\n    mode: basic\nbackendListenAddr: 127.0.0.1:18090\nproxyListenAddr: 127.0.0.1:18080\n"
	if err := os.WriteFile(path, []byte(configYAML), 0o644); err != nil {
		t.Fatalf("write structured config: %v", err)
	}
	store := NewStore(path, filepath.Join(root, "logs"))
	config, err := store.Load(context.Background())
	if err != nil {
		t.Fatalf("load structured config: %v", err)
	}
	if config.Observability.Mode != ObservabilityModeBasic {
		t.Fatalf("expected structured basic mode to override legacy flag, got %+v", config)
	}
	persisted, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read normalized structured config: %v", err)
	}
	if strings.Contains(string(persisted), "log:") {
		t.Fatalf("legacy log flag was not removed:\n%s", persisted)
	}
}

func TestStorePersistsMigratedObservabilityConfig(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "config.yaml")
	legacy := "log: true\nbackendListenAddr: 127.0.0.1:18090\nproxyListenAddr: 127.0.0.1:18080\n"
	if err := os.WriteFile(path, []byte(legacy), 0o644); err != nil {
		t.Fatalf("write legacy config: %v", err)
	}
	store := NewStore(path, filepath.Join(root, "logs"))
	config, err := store.Load(context.Background())
	if err != nil {
		t.Fatalf("load legacy config: %v", err)
	}
	if config.Observability.Mode != ObservabilityModeFull {
		t.Fatalf("expected migrated full mode, got %q", config.Observability.Mode)
	}
	persisted, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read migrated config: %v", err)
	}
	if !strings.Contains(string(persisted), "observability:\n    mode: full") {
		t.Fatalf("migrated config does not contain structured observability mode:\n%s", persisted)
	}
}

func testModelAdapter(displayName string, sortValue int) ModelAdapterConfig {
	return ModelAdapterConfig{
		Sort:            sortValue,
		DisplayName:     displayName,
		Type:            "openai",
		BaseURL:         "https://api.example.com/v1",
		APIKey:          "test-key",
		TooltipData:     displayName,
		ModelID:         displayName,
		ReasoningEffort: "medium",
		OpenAIEndpoint:  "/v1/responses",
	}
}

func TestNormalizeModelAdapterConfigsPreservesLegacyArrayOrder(t *testing.T) {
	adapters, err := NormalizeModelAdapterConfigs([]ModelAdapterConfig{
		testModelAdapter("first", 0),
		testModelAdapter("second", 0),
		testModelAdapter("third", 0),
	})
	if err != nil {
		t.Fatalf("NormalizeModelAdapterConfigs returned error: %v", err)
	}

	for index, expectedName := range []string{"first", "second", "third"} {
		if adapters[index].DisplayName != expectedName {
			t.Fatalf("adapter %d = %q, want %q", index, adapters[index].DisplayName, expectedName)
		}
		if adapters[index].Sort != index+1 {
			t.Fatalf("adapter %d sort = %d, want %d", index, adapters[index].Sort, index+1)
		}
	}
}

func TestNormalizeModelAdapterConfigsUsesStableExplicitSort(t *testing.T) {
	adapters, err := NormalizeModelAdapterConfigs([]ModelAdapterConfig{
		testModelAdapter("legacy", 0),
		testModelAdapter("third", 30),
		testModelAdapter("first", 10),
		testModelAdapter("second-a", 20),
		testModelAdapter("second-b", 20),
	})
	if err != nil {
		t.Fatalf("NormalizeModelAdapterConfigs returned error: %v", err)
	}

	expectedNames := []string{"first", "second-a", "second-b", "third", "legacy"}
	for index, expectedName := range expectedNames {
		if adapters[index].DisplayName != expectedName {
			t.Fatalf("adapter %d = %q, want %q", index, adapters[index].DisplayName, expectedName)
		}
		if adapters[index].Sort != index+1 {
			t.Fatalf("adapter %d sort = %d, want %d", index, adapters[index].Sort, index+1)
		}
	}
}

func TestNormalizeModelAdapterConfigsAllowsBlankReasoningEffort(t *testing.T) {
	adapter := testModelAdapter("non-reasoning-model", 1)
	adapter.ReasoningEffort = ""

	adapters, err := NormalizeModelAdapterConfigs([]ModelAdapterConfig{adapter})
	if err != nil {
		t.Fatalf("NormalizeModelAdapterConfigs returned error: %v", err)
	}
	if got := adapters[0].ReasoningEffort; got != "" {
		t.Fatalf("ReasoningEffort = %q, want blank", got)
	}
}

func TestNormalizeModelAdapterConfigsRejectsUnknownReasoningEffort(t *testing.T) {
	adapter := testModelAdapter("invalid-reasoning-effort", 1)
	adapter.ReasoningEffort = "unsupported"

	if _, err := NormalizeModelAdapterConfigs([]ModelAdapterConfig{adapter}); err == nil {
		t.Fatal("NormalizeModelAdapterConfigs should reject an unknown reasoning effort")
	}
}

// ──── ProviderFallback 校验测试 ────────────────────────────────────────────────

func testFallbackAdapters() []ModelAdapterConfig {
	a := testModelAdapter("ch-a", 1)
	b := testModelAdapter("ch-b", 2)
	b.BaseURL = "https://api2.example.com/v1"
	return []ModelAdapterConfig{a, b}
}

func TestProviderFallbackDisabledPassesThrough(t *testing.T) {
	adapters := testFallbackAdapters()
	// ProviderFallback.Enabled=false（默认）不做任何校验，直接通过
	result, err := NormalizeModelAdapterConfigs(adapters)
	if err != nil {
		t.Fatalf("disabled fallback should pass: %v", err)
	}
	if len(result) != 2 {
		t.Fatalf("expected 2 adapters, got %d", len(result))
	}
}

func TestProviderFallbackValidPrimaryAndCandidate(t *testing.T) {
	adapters := testFallbackAdapters()
	// 计算 ch-a 和 ch-b 的 ID（NormalizeModelAdapterConfigs 会计算 ID）
	normalized, _ := NormalizeModelAdapterConfigs(adapters)
	idA := normalized[0].ID
	idB := normalized[1].ID

	// ch-a 启用 fallback，primary=ch-b（另一个渠道），candidate=[ch-b] 是合法的
	// 但不能 primary == 自身，也不能 primary 在 candidates 里重复。
	// 简化：primary=ch-b，候选 = 另建第三个渠道
	c := testModelAdapter("ch-c", 3)
	c.BaseURL = "https://api3.example.com/v1"
	allAdapters := append(adapters, c)
	normalizedAll, _ := NormalizeModelAdapterConfigs(allAdapters)
	idA = normalizedAll[0].ID
	idB = normalizedAll[1].ID
	idC := normalizedAll[2].ID
	_ = idB

	allAdapters[0].ProviderFallback = ProviderFallbackConfig{
		Enabled:             true,
		PrimaryChannelID:    idA, // 自引用 → 应报错
		CandidateChannelIDs: []string{idC},
	}
	if _, err := NormalizeModelAdapterConfigs(allAdapters); err == nil {
		t.Fatal("self-referential primaryChannelID should be rejected")
	}

	// 有效配置：primary=ch-b, candidate=[ch-c]
	allAdapters[0].ProviderFallback = ProviderFallbackConfig{
		Enabled:             true,
		PrimaryChannelID:    idB,
		CandidateChannelIDs: []string{idC},
	}
	if _, err := NormalizeModelAdapterConfigs(allAdapters); err != nil {
		t.Fatalf("valid fallback config should pass: %v", err)
	}
}

func TestProviderFallbackRejectsDanglingRef(t *testing.T) {
	adapters := testFallbackAdapters()
	adapters[0].ProviderFallback = ProviderFallbackConfig{
		Enabled:             true,
		PrimaryChannelID:    "nonexistent-id",
		CandidateChannelIDs: []string{"also-nonexistent"},
	}
	if _, err := NormalizeModelAdapterConfigs(adapters); err == nil {
		t.Fatal("dangling primaryChannelID should be rejected")
	}
}

func TestProviderFallbackRejectsDuplicateCandidates(t *testing.T) {
	c := testModelAdapter("ch-c", 3)
	c.BaseURL = "https://api3.example.com/v1"
	allAdapters := append(testFallbackAdapters(), c)
	normalizedAll, _ := NormalizeModelAdapterConfigs(allAdapters)
	idB := normalizedAll[1].ID
	idC := normalizedAll[2].ID

	allAdapters[0].ProviderFallback = ProviderFallbackConfig{
		Enabled:             true,
		PrimaryChannelID:    idB,
		CandidateChannelIDs: []string{idC, idC}, // 重复
	}
	if _, err := NormalizeModelAdapterConfigs(allAdapters); err == nil {
		t.Fatal("duplicate candidateChannelIDs should be rejected")
	}
}

func TestProviderFallbackRejectsTooManyCandidates(t *testing.T) {
	adapters := testFallbackAdapters()
	normalizedAll, _ := NormalizeModelAdapterConfigs(adapters)
	idB := normalizedAll[1].ID

	// 3 个候选（超过最大 2 个）
	adapters[0].ProviderFallback = ProviderFallbackConfig{
		Enabled:             true,
		PrimaryChannelID:    idB,
		CandidateChannelIDs: []string{idB, idB, idB},
	}
	if _, err := NormalizeModelAdapterConfigs(adapters); err == nil {
		t.Fatal("more than 2 candidateChannelIDs should be rejected")
	}
}

func TestProviderFallbackRejectsEmptyCandidates(t *testing.T) {
	adapters := testFallbackAdapters()
	normalizedAll, _ := NormalizeModelAdapterConfigs(adapters)
	idB := normalizedAll[1].ID

	adapters[0].ProviderFallback = ProviderFallbackConfig{
		Enabled:             true,
		PrimaryChannelID:    idB,
		CandidateChannelIDs: []string{}, // 空列表
	}
	if _, err := NormalizeModelAdapterConfigs(adapters); err == nil {
		t.Fatal("empty candidateChannelIDs should be rejected when fallback enabled")
	}
}
