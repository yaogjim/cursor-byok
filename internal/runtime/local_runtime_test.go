package runtime

import (
	"context"
	"strings"
	"testing"
)

func testRuntimeModelAdapter(reasoningEffort string) ModelAdapterConfig {
	return ModelAdapterConfig{
		DisplayName:     "non-reasoning-model",
		Type:            "openai",
		BaseURL:         "https://api.example.com/v1",
		APIKey:          "test-key",
		TooltipData:     "non-reasoning-model",
		ModelID:         "non-reasoning-model",
		ReasoningEffort: reasoningEffort,
		OpenAIEndpoint:  "/v1/responses",
	}
}

func TestNormalizeModelAdapterConfigsAllowsBlankReasoningEffort(t *testing.T) {
	adapters, err := NormalizeModelAdapterConfigs([]ModelAdapterConfig{testRuntimeModelAdapter("")})
	if err != nil {
		t.Fatalf("NormalizeModelAdapterConfigs returned error: %v", err)
	}
	if got := adapters[0].ReasoningEffort; got != "" {
		t.Fatalf("ReasoningEffort = %q, want blank", got)
	}
}

func TestNormalizeModelAdapterConfigsRejectsUnknownReasoningEffort(t *testing.T) {
	if _, err := NormalizeModelAdapterConfigs([]ModelAdapterConfig{testRuntimeModelAdapter("unsupported")}); err == nil {
		t.Fatal("NormalizeModelAdapterConfigs should reject an unknown reasoning effort")
	}
}

func TestClampFallbackChainBudget(t *testing.T) {
	cases := []struct {
		name                   string
		attempts, wait         int
		wantAttempts, wantWait int
	}{
		{name: "missing", attempts: 0, wait: 0, wantAttempts: DefaultFallbackMaxHttpAttempts, wantWait: DefaultFallbackMaxWaitSeconds},
		{name: "legal", attempts: 7, wait: 20, wantAttempts: 7, wantWait: 20},
		{name: "low_positive", attempts: 1, wait: 0, wantAttempts: MinFallbackMaxHttpAttempts, wantWait: DefaultFallbackMaxWaitSeconds},
		{name: "negative", attempts: -3, wait: -1, wantAttempts: MinFallbackMaxHttpAttempts, wantWait: MinFallbackMaxWaitSeconds},
		{name: "high", attempts: 100, wait: 99, wantAttempts: MaxFallbackMaxHttpAttempts, wantWait: MaxFallbackMaxWaitSeconds},
		{name: "bounds", attempts: 2, wait: 1, wantAttempts: 2, wantWait: 1},
		{name: "max", attempts: 9, wait: 30, wantAttempts: 9, wantWait: 30},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			gotAttempts, gotWait := ClampFallbackChainBudget(test.attempts, test.wait)
			if gotAttempts != test.wantAttempts || gotWait != test.wantWait {
				t.Fatalf("ClampFallbackChainBudget(%d,%d) = %d/%d, want %d/%d", test.attempts, test.wait, gotAttempts, gotWait, test.wantAttempts, test.wantWait)
			}
		})
	}
}

func TestNormalizeModelAdapterConfigsMaxConcurrentRequests(t *testing.T) {
	missing, err := NormalizeModelAdapterConfigs([]ModelAdapterConfig{testRuntimeModelAdapter("")})
	if err != nil {
		t.Fatalf("missing capacity: %v", err)
	}
	if missing[0].MaxConcurrentRequests != 0 {
		t.Fatalf("missing capacity = %d, want 0", missing[0].MaxConcurrentRequests)
	}

	adapter := testRuntimeModelAdapter("")
	adapter.MaxConcurrentRequests = 16
	got, err := NormalizeModelAdapterConfigs([]ModelAdapterConfig{adapter})
	if err != nil {
		t.Fatalf("legal capacity 16: %v", err)
	}
	if got[0].MaxConcurrentRequests != 16 {
		t.Fatalf("capacity = %d, want 16", got[0].MaxConcurrentRequests)
	}

	adapter.MaxConcurrentRequests = 17
	if _, err := NormalizeModelAdapterConfigs([]ModelAdapterConfig{adapter}); err == nil {
		t.Fatal("capacity 17 should be rejected")
	}
}

func TestSelectChannelForModelMapsUpstreamCapacity(t *testing.T) {
	svc := NewConfigurableChannelService(func(context.Context) (RuntimeConfigSnapshot, error) {
		a := testRuntimeModelAdapter("")
		a.DisplayName = "model-a"
		a.ModelID = "model-a"
		a.TooltipData = "model-a"
		a.MaxConcurrentRequests = 2
		b := testRuntimeModelAdapter("")
		b.DisplayName = "model-b"
		b.ModelID = "model-b"
		b.TooltipData = "model-b"
		b.MaxConcurrentRequests = 2
		c := testRuntimeModelAdapter("")
		c.DisplayName = "model-c"
		c.ModelID = "model-c"
		c.TooltipData = "model-c"
		c.APIKey = "other-key"
		c.MaxConcurrentRequests = 1
		return RuntimeConfigSnapshot{ModelAdapters: []ModelAdapterConfig{a, b, c}}, nil
	}, "")

	chA, err := svc.SelectChannelForModel(context.Background(), "model-a")
	if err != nil {
		t.Fatalf("select A: %v", err)
	}
	chB, err := svc.SelectChannelForModel(context.Background(), "model-b")
	if err != nil {
		t.Fatalf("select B: %v", err)
	}
	chC, err := svc.SelectChannelForModel(context.Background(), "model-c")
	if err != nil {
		t.Fatalf("select C: %v", err)
	}
	if chA.MaxConcurrentRequests != 2 || chB.MaxConcurrentRequests != 2 || chC.MaxConcurrentRequests != 1 {
		t.Fatalf("resolved limits = %d/%d/%d, want 2/2/1", chA.MaxConcurrentRequests, chB.MaxConcurrentRequests, chC.MaxConcurrentRequests)
	}
	if chA.UpstreamCapacityGroupKey == "" || chA.UpstreamCapacityGroupKey != chB.UpstreamCapacityGroupKey {
		t.Fatalf("same-group keys = %q / %q", chA.UpstreamCapacityGroupKey, chB.UpstreamCapacityGroupKey)
	}
	if chA.UpstreamCapacityGroupKey == chC.UpstreamCapacityGroupKey {
		t.Fatal("different API keys must not share group key")
	}
	if strings.Contains(chA.UpstreamCapacityGroupKey, "test-key") {
		t.Fatal("group key leaked API key")
	}
	want := BuildUpstreamCapacityGroupKey(chA.Provider, chA.BaseURL, chA.APIKey)
	if chA.UpstreamCapacityGroupKey != want {
		t.Fatalf("group key = %q, want %q", chA.UpstreamCapacityGroupKey, want)
	}
	normalizedEquivalent := BuildUpstreamCapacityGroupKey(" OpenAI ", "HTTPS://API.EXAMPLE.COM/v1/", chA.APIKey)
	canonicalEquivalent := BuildUpstreamCapacityGroupKey("openai", "https://api.example.com/v1", chA.APIKey)
	if normalizedEquivalent != canonicalEquivalent {
		t.Fatal("group key must normalize provider type and base URL")
	}
}
