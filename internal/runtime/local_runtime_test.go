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

func TestClampChannelPlanRecovery(t *testing.T) {
	missing := &ChannelPlan{}
	ClampChannelPlanRecovery(missing)
	if missing.MaxHttpAttempts != DefaultFallbackMaxHttpAttempts || missing.MaxWaitSeconds != DefaultFallbackMaxWaitSeconds {
		t.Fatalf("missing attempts/wait = %d/%d", missing.MaxHttpAttempts, missing.MaxWaitSeconds)
	}
	if missing.MaxAttemptsPerChannel != DefaultFallbackMaxAttemptsPerChannel || missing.ConnectTimeoutSeconds != DefaultFallbackConnectTimeoutSeconds {
		t.Fatalf("missing per-channel/connect = %d/%d", missing.MaxAttemptsPerChannel, missing.ConnectTimeoutSeconds)
	}
	if missing.FirstEventTimeoutSeconds != DefaultFallbackFirstEventTimeoutSeconds || missing.StreamIdleTimeoutSeconds != DefaultFallbackStreamIdleTimeoutSeconds || missing.CallTimeoutSeconds != DefaultFallbackCallTimeoutSeconds {
		t.Fatalf("missing liveness = %d/%d/%d", missing.FirstEventTimeoutSeconds, missing.StreamIdleTimeoutSeconds, missing.CallTimeoutSeconds)
	}

	legal := &ChannelPlan{
		MaxHttpAttempts:          7,
		MaxWaitSeconds:           20,
		MaxAttemptsPerChannel:    3,
		ConnectTimeoutSeconds:    5,
		FirstEventTimeoutSeconds: 60,
		StreamIdleTimeoutSeconds: 30,
		CallTimeoutSeconds:       900,
	}
	ClampChannelPlanRecovery(legal)
	if legal.MaxAttemptsPerChannel != 3 || legal.ConnectTimeoutSeconds != 5 || legal.FirstEventTimeoutSeconds != 60 || legal.StreamIdleTimeoutSeconds != 30 || legal.CallTimeoutSeconds != 900 {
		t.Fatalf("legal clamp mutated: %+v", legal)
	}

	oor := &ChannelPlan{
		MaxAttemptsPerChannel:    9,
		ConnectTimeoutSeconds:    1,
		FirstEventTimeoutSeconds: 10,
		StreamIdleTimeoutSeconds: 10,
		CallTimeoutSeconds:       100,
	}
	ClampChannelPlanRecovery(oor)
	if oor.MaxAttemptsPerChannel != MaxFallbackMaxAttemptsPerChannel || oor.ConnectTimeoutSeconds != MinFallbackConnectTimeoutSeconds {
		t.Fatalf("oor clamp = %+v", oor)
	}
	if oor.FirstEventTimeoutSeconds != MinFallbackFirstEventTimeoutSeconds || oor.StreamIdleTimeoutSeconds != MinFallbackStreamIdleTimeoutSeconds || oor.CallTimeoutSeconds != MinFallbackCallTimeoutSeconds {
		t.Fatalf("oor liveness clamp = %+v", oor)
	}

	high := &ChannelPlan{
		MaxAttemptsPerChannel:    4,
		ConnectTimeoutSeconds:    1000,
		FirstEventTimeoutSeconds: 10000,
		StreamIdleTimeoutSeconds: 10000,
		CallTimeoutSeconds:       100000,
	}
	ClampChannelPlanRecovery(high)
	if high.MaxAttemptsPerChannel != MaxFallbackMaxAttemptsPerChannel || high.ConnectTimeoutSeconds != MaxFallbackConnectTimeoutSeconds || high.FirstEventTimeoutSeconds != MaxFallbackFirstEventTimeoutSeconds || high.StreamIdleTimeoutSeconds != MaxFallbackStreamIdleTimeoutSeconds || high.CallTimeoutSeconds != MaxFallbackCallTimeoutSeconds {
		t.Fatalf("high clamp = %+v", high)
	}

	ClampChannelPlanRecovery(nil)
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

func TestNormalizeModelAdapterConfigsOpenAIImageGenerationEnabled(t *testing.T) {
	allowed := testRuntimeModelAdapter("")
	allowed.OpenAIImageGenerationEnabled = true
	got, err := NormalizeModelAdapterConfigs([]ModelAdapterConfig{allowed})
	if err != nil {
		t.Fatalf("openai+static+responses should allow true: %v", err)
	}
	if !got[0].OpenAIImageGenerationEnabled {
		t.Fatal("allowed true was dropped")
	}

	chat := testRuntimeModelAdapter("")
	chat.OpenAIEndpoint = "/v1/chat/completions"
	chat.OpenAIImageGenerationEnabled = true
	if _, err := NormalizeModelAdapterConfigs([]ModelAdapterConfig{chat}); err == nil || !strings.Contains(err.Error(), "openAIImageGenerationEnabled") {
		t.Fatalf("chat completions true should be rejected: %v", err)
	}

	invalid := []struct {
		name   string
		mutate func(*ModelAdapterConfig)
	}{
		{name: "codex", mutate: func(adapter *ModelAdapterConfig) { adapter.CredentialSource = "codex" }},
		{name: "grok", mutate: func(adapter *ModelAdapterConfig) { adapter.CredentialSource = "grok" }},
		{name: "custom_endpoint", mutate: func(adapter *ModelAdapterConfig) { adapter.OpenAIEndpoint = "/custom" }},
		{name: "anthropic", mutate: func(adapter *ModelAdapterConfig) {
			adapter.Type = "anthropic"
			adapter.AnthropicThinkingEffort = "xhigh"
		}},
	}
	for _, test := range invalid {
		t.Run(test.name, func(t *testing.T) {
			adapter := testRuntimeModelAdapter("")
			adapter.OpenAIImageGenerationEnabled = true
			test.mutate(&adapter)
			if _, err := NormalizeModelAdapterConfigs([]ModelAdapterConfig{adapter}); err == nil || !strings.Contains(err.Error(), "openAIImageGenerationEnabled") {
				t.Fatalf("incompatible true should be rejected: %v", err)
			}
		})
	}
}

func TestSelectChannelForModelProjectsOpenAIImageGenerationEnabled(t *testing.T) {
	svc := NewConfigurableChannelService(func(context.Context) (RuntimeConfigSnapshot, error) {
		on := testRuntimeModelAdapter("")
		on.DisplayName = "img-on"
		on.ModelID = "img-on"
		on.TooltipData = "img-on"
		on.OpenAIImageGenerationEnabled = true
		off := testRuntimeModelAdapter("")
		off.DisplayName = "img-off"
		off.ModelID = "img-off"
		off.TooltipData = "img-off"
		off.APIKey = "other-key"
		return RuntimeConfigSnapshot{ModelAdapters: []ModelAdapterConfig{on, off}}, nil
	}, "")

	chOn, err := svc.SelectChannelForModel(context.Background(), "img-on")
	if err != nil {
		t.Fatalf("select on: %v", err)
	}
	chOff, err := svc.SelectChannelForModel(context.Background(), "img-off")
	if err != nil {
		t.Fatalf("select off: %v", err)
	}
	if !chOn.OpenAIImageGenerationEnabled {
		t.Fatal("runtime resolved channel lost OpenAIImageGenerationEnabled")
	}
	if chOff.OpenAIImageGenerationEnabled {
		t.Fatal("disabled runtime channel projected OpenAIImageGenerationEnabled=true")
	}
}
