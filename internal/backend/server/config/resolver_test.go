package config

import (
	"strings"
	"testing"

	legacyruntime "cursor/internal/runtime"
)

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
