package config

import (
	"context"
	"errors"
	"strings"
	"testing"

	"cursor/internal/netproxy"
	legacyruntime "cursor/internal/runtime"
	"cursor/internal/subscriptionauth"
)

func TestResolveChannelPlanProjectsFourCandidatesInOrder(t *testing.T) {
	adapters := testPhysicalAdapterSet(6)
	ids := adapterIDs(t, adapters)
	adapters[0].ProviderFallback = ProviderFallbackConfig{
		Enabled:             true,
		PrimaryChannelID:    ids[1],
		CandidateChannelIDs: []string{ids[2], ids[3], ids[4], ids[5]},
	}
	normalized, err := NormalizeModelAdapterConfigs(adapters)
	if err != nil {
		t.Fatalf("normalize: %v", err)
	}
	plan, err := resolveModelAdapterChannelPlan(normalized, normalized[0].ID)
	if err != nil {
		t.Fatalf("resolve plan: %v", err)
	}
	if !plan.FallbackEnabled || len(plan.Channels) != 5 {
		t.Fatalf("plan = %+v", plan)
	}
	want := []string{ids[1], ids[2], ids[3], ids[4], ids[5]}
	for i, channelID := range want {
		if plan.Channels[i].ID != channelID {
			t.Fatalf("plan.Channels[%d].ID = %q, want %q", i, plan.Channels[i].ID, channelID)
		}
	}
}

func TestResolveChannelPlanCarriesNormalizedBudget(t *testing.T) {
	adapters, idA, idB, idC := testFallbackChain(t)
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
	plan, err := resolveModelAdapterChannelPlan(normalized, idA)
	if err != nil {
		t.Fatalf("resolve plan: %v", err)
	}
	if !plan.FallbackEnabled || len(plan.Channels) != 2 {
		t.Fatalf("plan = %+v", plan)
	}
	if plan.MaxHttpAttempts != 7 || plan.MaxWaitSeconds != 20 {
		t.Fatalf("plan budget = %d/%d, want 7/20", plan.MaxHttpAttempts, plan.MaxWaitSeconds)
	}
}

func TestResolveChannelPlanDefaultsMissingBudget(t *testing.T) {
	adapters, idA, idB, idC := testFallbackChain(t)
	adapters[0].ProviderFallback = ProviderFallbackConfig{
		Enabled:             true,
		PrimaryChannelID:    idB,
		CandidateChannelIDs: []string{idC},
	}
	normalized, err := NormalizeModelAdapterConfigs(adapters)
	if err != nil {
		t.Fatalf("normalize: %v", err)
	}
	plan, err := resolveModelAdapterChannelPlan(normalized, idA)
	if err != nil {
		t.Fatalf("resolve plan: %v", err)
	}
	if plan.MaxHttpAttempts != DefaultProviderFallbackMaxHttpAttempts || plan.MaxWaitSeconds != DefaultProviderFallbackMaxWaitSeconds {
		t.Fatalf("default plan budget = %d/%d", plan.MaxHttpAttempts, plan.MaxWaitSeconds)
	}
}

func TestResolveChannelPlanClampsOutOfRangeRuntimeValues(t *testing.T) {
	adapters, idA, idB, idC := testFallbackChain(t)
	adapters[0].ProviderFallback = ProviderFallbackConfig{
		Enabled:             true,
		PrimaryChannelID:    idB,
		CandidateChannelIDs: []string{idC},
		MaxHttpAttempts:     5,
		MaxWaitSeconds:      8,
	}
	normalized, err := NormalizeModelAdapterConfigs(adapters)
	if err != nil {
		t.Fatalf("normalize: %v", err)
	}
	normalized[0].ProviderFallback.MaxHttpAttempts = 100
	normalized[0].ProviderFallback.MaxWaitSeconds = 99
	plan, err := resolveModelAdapterChannelPlan(normalized, idA)
	if err != nil {
		t.Fatalf("resolve plan: %v", err)
	}
	if plan.MaxHttpAttempts != legacyruntime.MaxFallbackMaxHttpAttempts || plan.MaxWaitSeconds != legacyruntime.MaxFallbackMaxWaitSeconds {
		t.Fatalf("clamped plan budget = %d/%d, want %d/%d", plan.MaxHttpAttempts, plan.MaxWaitSeconds, legacyruntime.MaxFallbackMaxHttpAttempts, legacyruntime.MaxFallbackMaxWaitSeconds)
	}

	normalized[0].ProviderFallback.MaxHttpAttempts = 1
	normalized[0].ProviderFallback.MaxWaitSeconds = 0
	plan, err = resolveModelAdapterChannelPlan(normalized, idA)
	if err != nil {
		t.Fatalf("resolve plan: %v", err)
	}
	if plan.MaxHttpAttempts != legacyruntime.MinFallbackMaxHttpAttempts || plan.MaxWaitSeconds != legacyruntime.DefaultFallbackMaxWaitSeconds {
		t.Fatalf("low/zero clamp = %d/%d", plan.MaxHttpAttempts, plan.MaxWaitSeconds)
	}

	normalized[0].ProviderFallback.MaxHttpAttempts = -3
	normalized[0].ProviderFallback.MaxWaitSeconds = -1
	plan, err = resolveModelAdapterChannelPlan(normalized, idA)
	if err != nil {
		t.Fatalf("resolve negative plan: %v", err)
	}
	if plan.MaxHttpAttempts != legacyruntime.MinFallbackMaxHttpAttempts || plan.MaxWaitSeconds != legacyruntime.MinFallbackMaxWaitSeconds {
		t.Fatalf("negative clamp = %d/%d, want %d/%d", plan.MaxHttpAttempts, plan.MaxWaitSeconds, legacyruntime.MinFallbackMaxHttpAttempts, legacyruntime.MinFallbackMaxWaitSeconds)
	}
}

func TestResolveChannelPlanDisabledKeepsBudgetButSingleChannel(t *testing.T) {
	adapters := testFallbackAdapters()
	adapters[0].ProviderFallback = ProviderFallbackConfig{
		Enabled:         false,
		MaxHttpAttempts: 9,
		MaxWaitSeconds:  3,
	}
	normalized, err := NormalizeModelAdapterConfigs(adapters)
	if err != nil {
		t.Fatalf("normalize: %v", err)
	}
	plan, err := resolveModelAdapterChannelPlan(normalized, normalized[0].ID)
	if err != nil {
		t.Fatalf("resolve plan: %v", err)
	}
	if plan.FallbackEnabled || len(plan.Channels) != 1 {
		t.Fatalf("disabled plan = %+v", plan)
	}
	if plan.MaxHttpAttempts != 9 || plan.MaxWaitSeconds != 3 {
		t.Fatalf("disabled plan budget = %d/%d, want 9/3", plan.MaxHttpAttempts, plan.MaxWaitSeconds)
	}
}

func TestResolveChannelMapsUpstreamCapacityLimitAndGroupKey(t *testing.T) {
	a := testModelAdapter("model-a", 1)
	b := testModelAdapter("model-b", 2)
	c := testModelAdapter("model-c", 3)
	c.APIKey = "other-key"
	a.MaxConcurrentRequests = 2
	b.MaxConcurrentRequests = 2
	c.MaxConcurrentRequests = 8
	normalized, err := NormalizeModelAdapterConfigs([]ModelAdapterConfig{a, b, c})
	if err != nil {
		t.Fatalf("normalize: %v", err)
	}

	chA, err := resolveModelAdapterChannel(normalized, normalized[0].ID)
	if err != nil {
		t.Fatalf("resolve A: %v", err)
	}
	chB, err := resolveModelAdapterChannel(normalized, normalized[1].ID)
	if err != nil {
		t.Fatalf("resolve B: %v", err)
	}
	chC, err := resolveModelAdapterChannel(normalized, normalized[2].ID)
	if err != nil {
		t.Fatalf("resolve C: %v", err)
	}
	if chA.MaxConcurrentRequests != 2 || chB.MaxConcurrentRequests != 2 || chC.MaxConcurrentRequests != 8 {
		t.Fatalf("resolved limits = %d/%d/%d, want 2/2/8", chA.MaxConcurrentRequests, chB.MaxConcurrentRequests, chC.MaxConcurrentRequests)
	}
	if chA.UpstreamCapacityGroupKey == "" || chB.UpstreamCapacityGroupKey == "" || chC.UpstreamCapacityGroupKey == "" {
		t.Fatal("resolved channels missing UpstreamCapacityGroupKey")
	}
	if chA.UpstreamCapacityGroupKey != chB.UpstreamCapacityGroupKey {
		t.Fatal("same upstream group must share UpstreamCapacityGroupKey")
	}
	if chA.UpstreamCapacityGroupKey == chC.UpstreamCapacityGroupKey {
		t.Fatal("different API keys must not share UpstreamCapacityGroupKey")
	}
	wantKey := legacyruntime.BuildUpstreamCapacityGroupKey(normalized[0].Type, normalized[0].BaseURL, normalized[0].APIKey)
	if chA.UpstreamCapacityGroupKey != wantKey {
		t.Fatalf("group key = %q, want %q", chA.UpstreamCapacityGroupKey, wantKey)
	}
	if strings.Contains(chA.UpstreamCapacityGroupKey, "test-key") || strings.Contains(chA.UpstreamCapacityGroupKey, normalized[0].APIKey) {
		t.Fatal("group key leaked API key")
	}
}

func TestResolveChannelPlanCarriesPhysicalCapacityNotAlias(t *testing.T) {
	adapters, idA, idB, idC := testFallbackChain(t)
	adapters[0].ProviderFallback = ProviderFallbackConfig{
		Enabled:             true,
		PrimaryChannelID:    idB,
		CandidateChannelIDs: []string{idC},
	}
	adapters[0].MaxConcurrentRequests = 0
	adapters[1].MaxConcurrentRequests = 2
	adapters[2].MaxConcurrentRequests = 4
	normalized, err := NormalizeModelAdapterConfigs(adapters)
	if err != nil {
		t.Fatalf("normalize: %v", err)
	}
	plan, err := resolveModelAdapterChannelPlan(normalized, idA)
	if err != nil {
		t.Fatalf("resolve plan: %v", err)
	}
	if !plan.FallbackEnabled || len(plan.Channels) != 2 {
		t.Fatalf("plan = %+v", plan)
	}
	if plan.Channels[0].MaxConcurrentRequests != 2 || plan.Channels[1].MaxConcurrentRequests != 4 {
		t.Fatalf("plan channel limits = %d/%d, want 2/4", plan.Channels[0].MaxConcurrentRequests, plan.Channels[1].MaxConcurrentRequests)
	}
	if plan.Channels[0].UpstreamCapacityGroupKey == "" || plan.Channels[1].UpstreamCapacityGroupKey == "" {
		t.Fatal("plan channels missing UpstreamCapacityGroupKey")
	}
	if plan.Channels[0].UpstreamCapacityGroupKey == plan.Channels[1].UpstreamCapacityGroupKey {
		t.Fatal("different physical upstreams must not share capacity group key")
	}
}

func TestResolveAdapterToChannelProjectsOpenAIImageGenerationEnabled(t *testing.T) {
	enabled := testModelAdapter("img-on", 1)
	enabled.OpenAIImageGenerationEnabled = true
	disabled := testModelAdapter("img-off", 2)
	disabled.BaseURL = "https://api2.example.com/v1"
	normalized, err := NormalizeModelAdapterConfigs([]ModelAdapterConfig{enabled, disabled})
	if err != nil {
		t.Fatalf("normalize: %v", err)
	}
	on := resolveAdapterToChannel(normalized[0])
	off := resolveAdapterToChannel(normalized[1])
	if !on.OpenAIImageGenerationEnabled {
		t.Fatal("resolved channel lost OpenAIImageGenerationEnabled")
	}
	if off.OpenAIImageGenerationEnabled {
		t.Fatal("disabled adapter projected OpenAIImageGenerationEnabled=true")
	}

	plan, err := resolveModelAdapterChannelPlan(normalized, normalized[0].ID)
	if err != nil {
		t.Fatalf("resolve plan: %v", err)
	}
	if len(plan.Channels) != 1 || !plan.Channels[0].OpenAIImageGenerationEnabled {
		t.Fatalf("single-channel plan = %+v", plan)
	}
}

func TestResolveChannelPlanKeepsOpenAIImageGenerationPerCandidate(t *testing.T) {
	adapters, aliasID, primaryID, candidateID := testFallbackChain(t)
	adapters[0].ProviderFallback = ProviderFallbackConfig{
		Enabled:             true,
		PrimaryChannelID:    primaryID,
		CandidateChannelIDs: []string{candidateID},
	}
	adapters[1].OpenAIImageGenerationEnabled = true
	adapters[2].OpenAIImageGenerationEnabled = false
	normalized, err := NormalizeModelAdapterConfigs(adapters)
	if err != nil {
		t.Fatalf("normalize: %v", err)
	}
	plan, err := resolveModelAdapterChannelPlan(normalized, aliasID)
	if err != nil {
		t.Fatalf("resolve plan: %v", err)
	}
	if !plan.FallbackEnabled || len(plan.Channels) != 2 {
		t.Fatalf("plan = %+v", plan)
	}
	if !plan.Channels[0].OpenAIImageGenerationEnabled {
		t.Fatal("primary channel lost enabled capability")
	}
	if plan.Channels[1].OpenAIImageGenerationEnabled {
		t.Fatal("candidate channel inherited primary capability")
	}
}

func TestResolveChannelDefaultsMissingCapacityUnlimited(t *testing.T) {
	adapters := []ModelAdapterConfig{testModelAdapter("ch-a", 1)}
	normalized, err := NormalizeModelAdapterConfigs(adapters)
	if err != nil {
		t.Fatalf("normalize: %v", err)
	}
	ch, err := resolveModelAdapterChannel(normalized, normalized[0].ID)
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if ch.MaxConcurrentRequests != 0 {
		t.Fatalf("missing capacity = %d, want 0", ch.MaxConcurrentRequests)
	}
	if ch.UpstreamCapacityGroupKey == "" {
		t.Fatal("unlimited channel still needs an in-memory group key")
	}
}

func TestSelectChannelForModelResolvesCatalogIDsWithoutSentinel(t *testing.T) {
	const sentinel = "cursor-byok-local"
	manager := newWriteTestManager(t)
	static := testModelAdapter("static-byok", 1)
	static.APIKey = "real-static-secret"
	static.BaseURL = "https://static.example/v1"
	managed := testModelAdapter("managed-codex", 2)
	managed.APIKey = ""
	managed.CredentialSource = "codex"
	managed.BaseURL = "https://ignored.example/v1"
	grok := testModelAdapter("managed-grok", 3)
	grok.APIKey = ""
	grok.CredentialSource = "grok"
	grok.BaseURL = "https://ignored.example/v1"
	logical, _, primary, candidate := testFallbackChain(t)
	logical[0].ProviderFallback = ProviderFallbackConfig{
		Enabled:             true,
		PrimaryChannelID:    primary,
		CandidateChannelIDs: []string{candidate},
	}

	saved := seedWriteTestManagerConfig(t, manager, func(cfg *Config) {
		cfg.ModelAdapters = append([]ModelAdapterConfig{static, managed, grok}, logical...)
	})

	idByName := map[string]string{}
	for _, adapter := range saved.ModelAdapters {
		idByName[adapter.DisplayName] = adapter.ID
	}
	staticID := idByName["static-byok"]
	managedID := idByName["managed-codex"]
	grokID := idByName["managed-grok"]
	logicalID := idByName["ch-a"]
	if staticID == "" || managedID == "" || grokID == "" || logicalID == "" {
		t.Fatalf("missing catalog IDs: %#v", idByName)
	}

	staticCh, err := manager.SelectChannelForModel(context.Background(), staticID)
	if err != nil {
		t.Fatalf("static SelectChannelForModel: %v", err)
	}
	if staticCh.APIKey != "real-static-secret" || staticCh.APIKey == sentinel {
		t.Fatalf("static APIKey = %q", staticCh.APIKey)
	}
	if staticCh.BaseURL != "https://static.example/v1" {
		t.Fatalf("static BaseURL = %q", staticCh.BaseURL)
	}

	managedCh, err := manager.SelectChannelForModel(context.Background(), managedID)
	if err != nil {
		t.Fatalf("managed SelectChannelForModel: %v", err)
	}
	if managedCh.APIKey != "" || managedCh.APIKey == sentinel {
		t.Fatalf("managed APIKey = %q, want empty until runtime resolve", managedCh.APIKey)
	}
	if managedCh.CredentialSource != "codex" {
		t.Fatalf("managed CredentialSource = %q", managedCh.CredentialSource)
	}
	if managedCh.BaseURL != subscriptionauth.CodexResponsesURL {
		t.Fatalf("managed BaseURL = %q, want pinned Codex endpoint", managedCh.BaseURL)
	}

	grokCh, err := manager.SelectChannelForModel(context.Background(), grokID)
	if err != nil {
		t.Fatalf("grok SelectChannelForModel: %v", err)
	}
	if grokCh.APIKey != "" || grokCh.APIKey == sentinel {
		t.Fatalf("grok APIKey = %q, want empty until runtime resolve", grokCh.APIKey)
	}
	if grokCh.CredentialSource != "grok" {
		t.Fatalf("grok CredentialSource = %q", grokCh.CredentialSource)
	}
	if grokCh.BaseURL != subscriptionauth.GrokAPIBaseURL {
		t.Fatalf("grok BaseURL = %q, want pinned Grok endpoint", grokCh.BaseURL)
	}

	plan, err := manager.SelectChannelPlanForModel(context.Background(), logicalID)
	if err != nil {
		t.Fatalf("fallback SelectChannelPlanForModel: %v", err)
	}
	if !plan.FallbackEnabled || len(plan.Channels) != 2 {
		t.Fatalf("fallback plan = %+v", plan)
	}
	for i, channel := range plan.Channels {
		if channel.APIKey == sentinel || channel.APIKey == "" {
			t.Fatalf("fallback channel[%d] APIKey = %q", i, channel.APIKey)
		}
		if channel.ID == sentinel {
			t.Fatalf("fallback channel[%d] ID leaked sentinel", i)
		}
	}

	logicalCh, err := manager.SelectChannelForModel(context.Background(), logicalID)
	if err != nil {
		t.Fatalf("logical SelectChannelForModel: %v", err)
	}
	if logicalCh.ID != logicalID || logicalCh.ID == primary || logicalCh.ID == candidate {
		t.Fatalf("logical runtime ID = %q, rewritten to physical pool", logicalCh.ID)
	}
	if plan.Channels[0].ID != primary || plan.Channels[1].ID != candidate {
		t.Fatalf("fallback pool IDs = %q %q, want %q %q", plan.Channels[0].ID, plan.Channels[1].ID, primary, candidate)
	}
	if staticCh.ReasoningEffort != "medium" || managedCh.ReasoningEffort != "medium" || grokCh.ReasoningEffort != "medium" {
		t.Fatalf("runtime thinking static=%q codex=%q grok=%q", staticCh.ReasoningEffort, managedCh.ReasoningEffort, grokCh.ReasoningEffort)
	}

	_, err = manager.SelectChannelForModel(context.Background(), "stale-catalog-id")
	if !errors.Is(err, legacyruntime.ErrChannelNotAvailable) {
		t.Fatalf("stale ID error = %v, want ErrChannelNotAvailable", err)
	}
	first, err := manager.SelectChannelForModel(context.Background(), staticID)
	if err != nil || first.ID != staticID {
		t.Fatalf("known ID after stale miss = %+v err=%v", first, err)
	}
}

func TestResolveAdapterToChannelProjectsOutboundProxy(t *testing.T) {
	enabled := testModelAdapter("proxy-on", 1)
	enabled.OutboundProxy = netproxy.Config{Enabled: true, URL: "http://model.example:8080"}
	disabled := testModelAdapter("proxy-off", 2)
	disabled.BaseURL = "https://api2.example.com/v1"
	disabled.OutboundProxy = netproxy.Config{Enabled: false, URL: "http://unused.example:9"}
	normalized, err := NormalizeModelAdapterConfigs([]ModelAdapterConfig{enabled, disabled})
	if err != nil {
		t.Fatalf("normalize: %v", err)
	}
	on := resolveAdapterToChannel(normalized[0])
	off := resolveAdapterToChannel(normalized[1])
	if !on.OutboundProxy.Enabled || on.OutboundProxy.URL != "http://model.example:8080" {
		t.Fatalf("enabled channel proxy = %+v", on.OutboundProxy)
	}
	if off.OutboundProxy.Enabled || off.OutboundProxy.URL != "http://unused.example:9" {
		t.Fatalf("disabled channel must retain unused URL: %+v", off.OutboundProxy)
	}
}

func TestResolveChannelPlanKeepsOutboundProxyPerCandidate(t *testing.T) {
	adapters, aliasID, primaryID, candidateID := testFallbackChain(t)
	adapters[0].OutboundProxy = netproxy.Config{Enabled: true, URL: "http://alias.example:1"}
	adapters[0].ProviderFallback = ProviderFallbackConfig{
		Enabled:             true,
		PrimaryChannelID:    primaryID,
		CandidateChannelIDs: []string{candidateID},
	}
	adapters[1].OutboundProxy = netproxy.Config{Enabled: true, URL: "http://primary.example:2"}
	adapters[2].OutboundProxy = netproxy.Config{Enabled: true, URL: "socks5://candidate.example:3"}
	normalized, err := NormalizeModelAdapterConfigs(adapters)
	if err != nil {
		t.Fatalf("normalize: %v", err)
	}
	plan, err := resolveModelAdapterChannelPlan(normalized, aliasID)
	if err != nil {
		t.Fatalf("resolve plan: %v", err)
	}
	if !plan.FallbackEnabled || len(plan.Channels) != 2 {
		t.Fatalf("plan = %+v", plan)
	}
	if plan.Channels[0].OutboundProxy.URL != "http://primary.example:2" {
		t.Fatalf("primary used alias proxy: %+v", plan.Channels[0].OutboundProxy)
	}
	if plan.Channels[1].OutboundProxy.URL != "socks5://candidate.example:3" {
		t.Fatalf("candidate used alias/primary proxy: %+v", plan.Channels[1].OutboundProxy)
	}
}
