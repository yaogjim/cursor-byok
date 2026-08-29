package config

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
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

func TestNormalizeModelAdapterConfigsPinsManagedSubscriptionEndpoints(t *testing.T) {
	tests := []struct {
		name               string
		credentialSource   string
		wantBaseURL        string
		wantOpenAIEndpoint string
	}{
		{
			name:               "codex",
			credentialSource:   "codex",
			wantBaseURL:        "https://chatgpt.com/backend-api/codex/responses",
			wantOpenAIEndpoint: "/v1/responses",
		},
		{
			name:               "grok",
			credentialSource:   "grok",
			wantBaseURL:        "https://api.x.ai/v1",
			wantOpenAIEndpoint: "/v1/chat/completions",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			adapter := testModelAdapter(test.name, 1)
			adapter.BaseURL = "http://127.0.0.1:18091/v1"
			adapter.APIKey = "must-not-persist"
			adapter.CredentialSource = test.credentialSource
			adapter.OpenAIEndpoint = "/v1/responses"

			adapters, err := NormalizeModelAdapterConfigs([]ModelAdapterConfig{adapter})
			if err != nil {
				t.Fatalf("NormalizeModelAdapterConfigs returned error: %v", err)
			}
			got := adapters[0]
			if got.BaseURL != test.wantBaseURL || got.OpenAIEndpoint != test.wantOpenAIEndpoint {
				t.Fatalf("managed endpoint = %q %q, want %q %q", got.BaseURL, got.OpenAIEndpoint, test.wantBaseURL, test.wantOpenAIEndpoint)
			}
			if got.APIKey != "" {
				t.Fatalf("managed api key persisted: %q", got.APIKey)
			}
		})
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

func testPhysicalAdapterSet(n int) []ModelAdapterConfig {
	adapters := make([]ModelAdapterConfig, n)
	for i := 0; i < n; i++ {
		item := testModelAdapter("ch-"+strconv.Itoa(i), i+1)
		item.BaseURL = "https://api" + strconv.Itoa(i) + ".example.com/v1"
		adapters[i] = item
	}
	return adapters
}

func adapterIDs(t *testing.T, adapters []ModelAdapterConfig) []string {
	t.Helper()
	normalized, err := NormalizeModelAdapterConfigs(adapters)
	if err != nil {
		t.Fatalf("normalize adapter set: %v", err)
	}
	ids := make([]string, len(normalized))
	for i := range normalized {
		ids[i] = normalized[i].ID
	}
	return ids
}

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

func TestProviderFallbackRejectsLogicalChannelsInsideChain(t *testing.T) {
	adapters := append(testFallbackAdapters(), testModelAdapter("ch-c", 3), testModelAdapter("ch-d", 4))
	adapters[2].BaseURL = "https://api3.example.com/v1"
	adapters[3].BaseURL = "https://api4.example.com/v1"
	normalized, err := NormalizeModelAdapterConfigs(adapters)
	if err != nil {
		t.Fatal(err)
	}
	idA, idB, idC := normalized[0].ID, normalized[1].ID, normalized[2].ID
	adapters[0].ProviderFallback = ProviderFallbackConfig{
		Enabled:             true,
		PrimaryChannelID:    idB,
		CandidateChannelIDs: []string{idC},
	}

	adapters[3].ProviderFallback = ProviderFallbackConfig{
		Enabled:             true,
		PrimaryChannelID:    idA,
		CandidateChannelIDs: []string{idC},
	}
	if _, err := NormalizeModelAdapterConfigs(adapters); err == nil || !strings.Contains(err.Error(), "物理渠道") {
		t.Fatalf("logical primary must be rejected, err=%v", err)
	}

	adapters[3].ProviderFallback = ProviderFallbackConfig{
		Enabled:             true,
		PrimaryChannelID:    idB,
		CandidateChannelIDs: []string{idA},
	}
	if _, err := NormalizeModelAdapterConfigs(adapters); err == nil || !strings.Contains(err.Error(), "物理渠道") {
		t.Fatalf("logical candidate must be rejected, err=%v", err)
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

func TestProviderFallbackAcceptsFourCandidates(t *testing.T) {
	adapters := testPhysicalAdapterSet(6)
	ids := adapterIDs(t, adapters)
	adapters[0].ProviderFallback = ProviderFallbackConfig{
		Enabled:             true,
		PrimaryChannelID:    ids[1],
		CandidateChannelIDs: []string{ids[2], ids[3], ids[4], ids[5]},
	}
	got, err := NormalizeModelAdapterConfigs(adapters)
	if err != nil {
		t.Fatalf("4 candidates should pass: %v", err)
	}
	gotIDs := got[0].ProviderFallback.CandidateChannelIDs
	want := []string{ids[2], ids[3], ids[4], ids[5]}
	if len(gotIDs) != 4 {
		t.Fatalf("candidates = %#v, want %#v", gotIDs, want)
	}
	for i := range want {
		if gotIDs[i] != want[i] {
			t.Fatalf("candidates = %#v, want %#v", gotIDs, want)
		}
	}
}

func TestProviderFallbackRejectsTooManyCandidates(t *testing.T) {
	adapters := testPhysicalAdapterSet(7)
	ids := adapterIDs(t, adapters)
	adapters[0].ProviderFallback = ProviderFallbackConfig{
		Enabled:             true,
		PrimaryChannelID:    ids[1],
		CandidateChannelIDs: []string{ids[2], ids[3], ids[4], ids[5], ids[6]},
	}
	_, err := NormalizeModelAdapterConfigs(adapters)
	if err == nil {
		t.Fatal("more than 4 candidateChannelIDs should be rejected")
	}
	if !strings.Contains(err.Error(), "1–4") {
		t.Fatalf("error should mention 1–4, got %v", err)
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

func testFallbackChain(t *testing.T) (adapters []ModelAdapterConfig, idA, idB, idC string) {
	t.Helper()
	c := testModelAdapter("ch-c", 3)
	c.BaseURL = "https://api3.example.com/v1"
	adapters = append(testFallbackAdapters(), c)
	normalized, err := NormalizeModelAdapterConfigs(adapters)
	if err != nil {
		t.Fatalf("normalize fallback chain: %v", err)
	}
	return adapters, normalized[0].ID, normalized[1].ID, normalized[2].ID
}

func TestProviderFallbackBudgetDefaultsMissingAndZero(t *testing.T) {
	adapters, _, idB, idC := testFallbackChain(t)
	adapters[0].ProviderFallback = ProviderFallbackConfig{
		Enabled:             true,
		PrimaryChannelID:    idB,
		CandidateChannelIDs: []string{idC},
	}
	got, err := NormalizeModelAdapterConfigs(adapters)
	if err != nil {
		t.Fatalf("missing budget should normalize: %v", err)
	}
	fb := got[0].ProviderFallback
	if fb.MaxHttpAttempts != DefaultProviderFallbackMaxHttpAttempts || fb.MaxWaitSeconds != DefaultProviderFallbackMaxWaitSeconds {
		t.Fatalf("missing budget = %d/%d, want %d/%d", fb.MaxHttpAttempts, fb.MaxWaitSeconds, DefaultProviderFallbackMaxHttpAttempts, DefaultProviderFallbackMaxWaitSeconds)
	}

	adapters[0].ProviderFallback.MaxHttpAttempts = 0
	adapters[0].ProviderFallback.MaxWaitSeconds = 0
	got, err = NormalizeModelAdapterConfigs(adapters)
	if err != nil {
		t.Fatalf("zero budget should normalize: %v", err)
	}
	fb = got[0].ProviderFallback
	if fb.MaxHttpAttempts != DefaultProviderFallbackMaxHttpAttempts || fb.MaxWaitSeconds != DefaultProviderFallbackMaxWaitSeconds {
		t.Fatalf("zero budget = %d/%d, want %d/%d", fb.MaxHttpAttempts, fb.MaxWaitSeconds, DefaultProviderFallbackMaxHttpAttempts, DefaultProviderFallbackMaxWaitSeconds)
	}
}

func TestProviderFallbackBudgetLegalBoundsPreserved(t *testing.T) {
	cases := []struct {
		attempts, wait int
	}{
		{2, 1},
		{5, 8},
		{7, 20},
		{9, 30},
	}
	for _, test := range cases {
		adapters, _, idB, idC := testFallbackChain(t)
		adapters[0].ProviderFallback = ProviderFallbackConfig{
			Enabled:             true,
			PrimaryChannelID:    idB,
			CandidateChannelIDs: []string{idC},
			MaxHttpAttempts:     test.attempts,
			MaxWaitSeconds:      test.wait,
		}
		got, err := NormalizeModelAdapterConfigs(adapters)
		if err != nil {
			t.Fatalf("legal budget %d/%d rejected: %v", test.attempts, test.wait, err)
		}
		fb := got[0].ProviderFallback
		if fb.MaxHttpAttempts != test.attempts || fb.MaxWaitSeconds != test.wait {
			t.Fatalf("budget = %d/%d, want %d/%d", fb.MaxHttpAttempts, fb.MaxWaitSeconds, test.attempts, test.wait)
		}
	}
}

func TestProviderFallbackBudgetOutOfRangeFailsTyped(t *testing.T) {
	cases := []struct {
		name           string
		attempts, wait int
		field          string
	}{
		{"attempts_1", 1, 8, "maxHttpAttempts"},
		{"attempts_10", 10, 8, "maxHttpAttempts"},
		{"attempts_negative", -3, 8, "maxHttpAttempts"},
		{"wait_31", 5, 31, "maxWaitSeconds"},
		{"wait_negative", 5, -1, "maxWaitSeconds"},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			adapters, _, idB, idC := testFallbackChain(t)
			adapters[0].ProviderFallback = ProviderFallbackConfig{
				Enabled:             true,
				PrimaryChannelID:    idB,
				CandidateChannelIDs: []string{idC},
				MaxHttpAttempts:     test.attempts,
				MaxWaitSeconds:      test.wait,
			}
			_, err := NormalizeModelAdapterConfigs(adapters)
			if err == nil {
				t.Fatal("expected typed budget validation error")
			}
			var typed *InvalidProviderFallbackBudgetError
			if !errors.As(err, &typed) || typed.Field != test.field {
				t.Fatalf("error = %v, want InvalidProviderFallbackBudgetError field %s", err, test.field)
			}
		})
	}
}

func TestProviderFallbackBudgetRetainedWhenDisabled(t *testing.T) {
	adapters := testFallbackAdapters()
	adapters[0].ProviderFallback = ProviderFallbackConfig{
		Enabled:         false,
		MaxHttpAttempts: 7,
		MaxWaitSeconds:  12,
	}
	got, err := NormalizeModelAdapterConfigs(adapters)
	if err != nil {
		t.Fatalf("disabled fallback with legal budget should pass: %v", err)
	}
	fb := got[0].ProviderFallback
	if fb.Enabled {
		t.Fatal("disabled fallback became enabled")
	}
	if fb.MaxHttpAttempts != 7 || fb.MaxWaitSeconds != 12 {
		t.Fatalf("disabled budget = %d/%d, want 7/12", fb.MaxHttpAttempts, fb.MaxWaitSeconds)
	}
}

func TestProviderFallbackBudgetYAMLJSONRoundtrip(t *testing.T) {
	adapters, _, idB, idC := testFallbackChain(t)
	adapters[0].ProviderFallback = ProviderFallbackConfig{
		Enabled:             true,
		PrimaryChannelID:    idB,
		CandidateChannelIDs: []string{idC},
		MaxHttpAttempts:     7,
		MaxWaitSeconds:      20,
	}
	normalized, err := NormalizeModelAdapterConfigs(adapters)
	if err != nil {
		t.Fatalf("normalize: %v", err)
	}
	payload, err := yaml.Marshal(normalized)
	if err != nil {
		t.Fatalf("yaml marshal: %v", err)
	}
	if !strings.Contains(string(payload), "maxHttpAttempts: 7") || !strings.Contains(string(payload), "maxWaitSeconds: 20") {
		t.Fatalf("yaml lost budget fields:\n%s", payload)
	}
	var decoded []ModelAdapterConfig
	if err := yaml.Unmarshal(payload, &decoded); err != nil {
		t.Fatalf("yaml unmarshal: %v", err)
	}
	roundtrip, err := NormalizeModelAdapterConfigs(decoded)
	if err != nil {
		t.Fatalf("normalize roundtrip: %v", err)
	}
	fb := roundtrip[0].ProviderFallback
	if !fb.Enabled || fb.MaxHttpAttempts != 7 || fb.MaxWaitSeconds != 20 || fb.PrimaryChannelID != idB {
		t.Fatalf("roundtrip fallback = %+v", fb)
	}

	jsonPayload, err := json.Marshal(normalized)
	if err != nil {
		t.Fatalf("json marshal: %v", err)
	}
	var jsonDecoded []ModelAdapterConfig
	if err := json.Unmarshal(jsonPayload, &jsonDecoded); err != nil {
		t.Fatalf("json unmarshal: %v", err)
	}
	jsonRoundtrip, err := NormalizeModelAdapterConfigs(jsonDecoded)
	if err != nil {
		t.Fatalf("normalize json roundtrip: %v", err)
	}
	jsonFB := jsonRoundtrip[0].ProviderFallback
	if !jsonFB.Enabled || jsonFB.MaxHttpAttempts != 7 || jsonFB.MaxWaitSeconds != 20 || jsonFB.PrimaryChannelID != idB {
		t.Fatalf("json roundtrip fallback = %+v", jsonFB)
	}
}

func TestMaxConcurrentRequestsDefaultsMissingAndZero(t *testing.T) {
	adapters := []ModelAdapterConfig{testModelAdapter("ch-a", 1)}
	got, err := NormalizeModelAdapterConfigs(adapters)
	if err != nil {
		t.Fatalf("missing maxConcurrentRequests should default: %v", err)
	}
	if got[0].MaxConcurrentRequests != 0 {
		t.Fatalf("missing maxConcurrentRequests = %d, want 0", got[0].MaxConcurrentRequests)
	}

	adapters[0].MaxConcurrentRequests = 0
	got, err = NormalizeModelAdapterConfigs(adapters)
	if err != nil {
		t.Fatalf("zero maxConcurrentRequests should pass: %v", err)
	}
	if got[0].MaxConcurrentRequests != 0 {
		t.Fatalf("zero maxConcurrentRequests = %d, want 0", got[0].MaxConcurrentRequests)
	}
}

func TestMaxConcurrentRequestsLegalBoundsPreserved(t *testing.T) {
	for _, limit := range []int{1, 8, 16} {
		adapters := []ModelAdapterConfig{testModelAdapter("ch-a", 1)}
		adapters[0].MaxConcurrentRequests = limit
		got, err := NormalizeModelAdapterConfigs(adapters)
		if err != nil {
			t.Fatalf("legal maxConcurrentRequests %d rejected: %v", limit, err)
		}
		if got[0].MaxConcurrentRequests != limit {
			t.Fatalf("maxConcurrentRequests = %d, want %d", got[0].MaxConcurrentRequests, limit)
		}
	}
}

func TestMaxConcurrentRequestsOutOfRangeFailsTyped(t *testing.T) {
	cases := []int{-1, 17, 32}
	for _, limit := range cases {
		t.Run(strconv.Itoa(limit), func(t *testing.T) {
			adapters := []ModelAdapterConfig{testModelAdapter("ch-a", 1)}
			adapters[0].MaxConcurrentRequests = limit
			_, err := NormalizeModelAdapterConfigs(adapters)
			if err == nil {
				t.Fatal("expected typed maxConcurrentRequests validation error")
			}
			var typed *InvalidMaxConcurrentRequestsError
			if !errors.As(err, &typed) || typed.Value != limit {
				t.Fatalf("error = %v, want InvalidMaxConcurrentRequestsError value %d", err, limit)
			}
			if strings.Contains(err.Error(), "test-key") {
				t.Fatalf("error leaked API key: %v", err)
			}
		})
	}
}

func TestMaxConcurrentRequestsRejectsLogicalAliasNonZero(t *testing.T) {
	adapters, idA, idB, idC := testFallbackChain(t)
	adapters[0].ProviderFallback = ProviderFallbackConfig{
		Enabled:             true,
		PrimaryChannelID:    idB,
		CandidateChannelIDs: []string{idC},
	}
	adapters[0].MaxConcurrentRequests = 2
	_, err := NormalizeModelAdapterConfigs(adapters)
	if err == nil || !strings.Contains(err.Error(), "逻辑") {
		t.Fatalf("logical alias non-zero maxConcurrentRequests must be rejected, err=%v", err)
	}
	if strings.Contains(err.Error(), "test-key") {
		t.Fatalf("error leaked API key: %v", err)
	}
	_ = idA
}

func TestMaxConcurrentRequestsAllowsLogicalAliasZeroWithPhysicalLimit(t *testing.T) {
	logical := testModelAdapter("logical", 1)
	physicalSameGroup := testModelAdapter("physical-same", 2)
	physicalOther := testModelAdapter("physical-other", 3)
	physicalOther.BaseURL = "https://api3.example.com/v1"
	normalized, err := NormalizeModelAdapterConfigs([]ModelAdapterConfig{logical, physicalSameGroup, physicalOther})
	if err != nil {
		t.Fatalf("normalize ids: %v", err)
	}
	idB, idC := normalized[1].ID, normalized[2].ID
	logical.ProviderFallback = ProviderFallbackConfig{
		Enabled:             true,
		PrimaryChannelID:    idB,
		CandidateChannelIDs: []string{idC},
	}
	logical.MaxConcurrentRequests = 0
	physicalSameGroup.MaxConcurrentRequests = 4
	physicalOther.MaxConcurrentRequests = 2
	got, err := NormalizeModelAdapterConfigs([]ModelAdapterConfig{logical, physicalSameGroup, physicalOther})
	if err != nil {
		t.Fatalf("alias zero with physical limits should pass: %v", err)
	}
	if got[0].MaxConcurrentRequests != 0 || got[1].MaxConcurrentRequests != 4 || got[2].MaxConcurrentRequests != 2 {
		t.Fatalf("limits = %d/%d/%d, want 0/4/2", got[0].MaxConcurrentRequests, got[1].MaxConcurrentRequests, got[2].MaxConcurrentRequests)
	}
}

func TestMaxConcurrentRequestsSameUpstreamGroupMustMatch(t *testing.T) {
	a := testModelAdapter("model-a", 1)
	b := testModelAdapter("model-b", 2)
	a.MaxConcurrentRequests = 2
	b.MaxConcurrentRequests = 3
	_, err := NormalizeModelAdapterConfigs([]ModelAdapterConfig{a, b})
	if err == nil || !strings.Contains(err.Error(), "必须相同") {
		t.Fatalf("same-group mismatch must be rejected, err=%v", err)
	}
	if strings.Contains(err.Error(), "test-key") || strings.Contains(strings.ToLower(err.Error()), "sha") {
		t.Fatalf("error leaked API key or derived group key: %v", err)
	}

	b.MaxConcurrentRequests = 2
	got, err := NormalizeModelAdapterConfigs([]ModelAdapterConfig{a, b})
	if err != nil {
		t.Fatalf("same-group matching limits should pass: %v", err)
	}
	if got[0].MaxConcurrentRequests != 2 || got[1].MaxConcurrentRequests != 2 {
		t.Fatalf("matching group limits = %d/%d, want 2/2", got[0].MaxConcurrentRequests, got[1].MaxConcurrentRequests)
	}

	b.APIKey = "other-key"
	b.MaxConcurrentRequests = 8
	got, err = NormalizeModelAdapterConfigs([]ModelAdapterConfig{a, b})
	if err != nil {
		t.Fatalf("different groups may use different limits: %v", err)
	}
	if got[0].MaxConcurrentRequests != 2 || got[1].MaxConcurrentRequests != 8 {
		t.Fatalf("different group limits = %d/%d, want 2/8", got[0].MaxConcurrentRequests, got[1].MaxConcurrentRequests)
	}
}

func TestMaxConcurrentRequestsYAMLJSONRoundtrip(t *testing.T) {
	adapters := []ModelAdapterConfig{testModelAdapter("ch-a", 1)}
	adapters[0].MaxConcurrentRequests = 2
	normalized, err := NormalizeModelAdapterConfigs(adapters)
	if err != nil {
		t.Fatalf("normalize: %v", err)
	}

	payload, err := yaml.Marshal(normalized)
	if err != nil {
		t.Fatalf("yaml marshal: %v", err)
	}
	text := string(payload)
	if !strings.Contains(text, "maxConcurrentRequests: 2") {
		t.Fatalf("yaml lost maxConcurrentRequests:\n%s", text)
	}
	if strings.Contains(text, "upstreamCapacityGroupKey") || strings.Contains(text, "UpstreamCapacityGroupKey") {
		t.Fatalf("yaml persisted derived group key:\n%s", text)
	}

	var decoded []ModelAdapterConfig
	if err := yaml.Unmarshal(payload, &decoded); err != nil {
		t.Fatalf("yaml unmarshal: %v", err)
	}
	roundtrip, err := NormalizeModelAdapterConfigs(decoded)
	if err != nil {
		t.Fatalf("normalize yaml roundtrip: %v", err)
	}
	if roundtrip[0].MaxConcurrentRequests != 2 {
		t.Fatalf("yaml roundtrip maxConcurrentRequests = %d, want 2", roundtrip[0].MaxConcurrentRequests)
	}

	jsonPayload, err := json.Marshal(normalized)
	if err != nil {
		t.Fatalf("json marshal: %v", err)
	}
	if strings.Contains(string(jsonPayload), "upstreamCapacityGroupKey") {
		t.Fatalf("json persisted derived group key: %s", jsonPayload)
	}
	var jsonDecoded []ModelAdapterConfig
	if err := json.Unmarshal(jsonPayload, &jsonDecoded); err != nil {
		t.Fatalf("json unmarshal: %v", err)
	}
	jsonRoundtrip, err := NormalizeModelAdapterConfigs(jsonDecoded)
	if err != nil {
		t.Fatalf("normalize json roundtrip: %v", err)
	}
	if jsonRoundtrip[0].MaxConcurrentRequests != 2 {
		t.Fatalf("json roundtrip maxConcurrentRequests = %d, want 2", jsonRoundtrip[0].MaxConcurrentRequests)
	}

	zeroAdapters := []ModelAdapterConfig{testModelAdapter("ch-zero", 1)}
	zeroNormalized, err := NormalizeModelAdapterConfigs(zeroAdapters)
	if err != nil {
		t.Fatalf("normalize zero: %v", err)
	}
	zeroPayload, err := yaml.Marshal(zeroNormalized)
	if err != nil {
		t.Fatalf("yaml marshal zero: %v", err)
	}
	if strings.Contains(string(zeroPayload), "maxConcurrentRequests") {
		t.Fatalf("zero/missing maxConcurrentRequests should be omitted from yaml:\n%s", zeroPayload)
	}
}

func TestNormalizeStreamContinuationMissingIsDisabled(t *testing.T) {
	normalized, err := NormalizeConfig(Config{})
	if err != nil {
		t.Fatalf("NormalizeConfig() error = %v", err)
	}
	if normalized.StreamContinuation.Enabled {
		t.Fatal("missing streamContinuation must stay disabled")
	}
	if normalized.StreamContinuation != (StreamContinuationConfig{}) {
		t.Fatalf("missing streamContinuation = %+v, want zero value so yaml omitempty skips disk writes", normalized.StreamContinuation)
	}

	enabled, err := NormalizeConfig(Config{StreamContinuation: StreamContinuationConfig{Enabled: true, MaxPerTurn: 9}})
	if err != nil {
		t.Fatalf("NormalizeConfig(enabled) error = %v", err)
	}
	if !enabled.StreamContinuation.Enabled {
		t.Fatal("explicit enabled was dropped")
	}
	if enabled.StreamContinuation.MaxPerTurn != DefaultStreamContinuationMaxPerTurn {
		t.Fatalf("maxPerTurn = %d, want capped %d", enabled.StreamContinuation.MaxPerTurn, DefaultStreamContinuationMaxPerTurn)
	}
	if enabled.StreamContinuation.TotalDeadlineSeconds != DefaultStreamContinuationDeadlineSeconds {
		t.Fatalf("deadline = %d, want %d", enabled.StreamContinuation.TotalDeadlineSeconds, DefaultStreamContinuationDeadlineSeconds)
	}
	if enabled.StreamContinuation.OverlapWindowChars != DefaultStreamContinuationOverlapWindowChars {
		t.Fatalf("overlap = %d, want %d", enabled.StreamContinuation.OverlapWindowChars, DefaultStreamContinuationOverlapWindowChars)
	}
}

func TestNormalizeModelAdapterConfigsAllowsManagedCredentialSourceWithoutAPIKey(t *testing.T) {
	adapter := testModelAdapter("codex-model", 1)
	adapter.APIKey = ""
	adapter.CredentialSource = "codex"
	got, err := NormalizeModelAdapterConfigs([]ModelAdapterConfig{adapter})
	if err != nil {
		t.Fatalf("managed adapter should allow empty apiKey: %v", err)
	}
	if got[0].APIKey != "" {
		t.Fatalf("managed adapter persisted apiKey %q", got[0].APIKey)
	}
	if got[0].CredentialSource != "codex" {
		t.Fatalf("credentialSource = %q", got[0].CredentialSource)
	}
	static := testModelAdapter("static-model", 1)
	static.APIKey = ""
	if _, err := NormalizeModelAdapterConfigs([]ModelAdapterConfig{static}); err == nil {
		t.Fatal("static adapter still requires apiKey")
	}
	tokenA := testModelAdapter("same", 1)
	tokenA.APIKey = ""
	tokenA.CredentialSource = "codex"
	tokenB := tokenA
	tokenB.APIKey = "should-be-stripped"
	normalizedA, _ := NormalizeModelAdapterConfigs([]ModelAdapterConfig{tokenA})
	normalizedB, _ := NormalizeModelAdapterConfigs([]ModelAdapterConfig{tokenB})
	if normalizedA[0].ID != normalizedB[0].ID {
		t.Fatal("managed channel ID must not depend on a rotating token")
	}
}
