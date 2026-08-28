package config

import "testing"

func TestDeriveChannelIDRemapPairsUniqueIdentityChange(t *testing.T) {
	previous := []ModelAdapterConfig{
		{ID: "old-a"},
		{ID: "keep-b"},
		{ID: "keep-l"},
	}
	submitted := []ModelAdapterConfig{
		{ID: "old-a"},
		{ID: "keep-b"},
		{ID: "keep-l"},
	}
	next := []ModelAdapterConfig{
		{ID: "new-a"},
		{ID: "keep-b"},
		{ID: "keep-l"},
	}
	got := deriveChannelIDRemap(previous, submitted, next)
	if len(got) != 1 || got["old-a"] != "new-a" {
		t.Fatalf("remap = %#v, want old-a→new-a", got)
	}
}

func TestDeriveChannelIDRemapPairsMultipleIdentityChanges(t *testing.T) {
	previous := []ModelAdapterConfig{{ID: "a"}, {ID: "b"}, {ID: "c"}}
	submitted := []ModelAdapterConfig{{ID: "a"}, {ID: "b"}, {ID: "c"}}
	next := []ModelAdapterConfig{{ID: "a2"}, {ID: "b2"}, {ID: "c"}}
	got := deriveChannelIDRemap(previous, submitted, next)
	if got["a"] != "a2" || got["b"] != "b2" || len(got) != 2 {
		t.Fatalf("remap = %#v", got)
	}
}

func TestDeriveChannelIDRemapIgnoresReorderWithoutIdentityChange(t *testing.T) {
	previous := []ModelAdapterConfig{{ID: "a"}, {ID: "b"}, {ID: "c"}}
	submitted := []ModelAdapterConfig{{ID: "c"}, {ID: "a"}, {ID: "b"}}
	next := []ModelAdapterConfig{{ID: "c"}, {ID: "a"}, {ID: "b"}}
	if got := deriveChannelIDRemap(previous, submitted, next); got != nil {
		t.Fatalf("reorder remap = %#v, want nil", got)
	}
}

func TestDeriveChannelIDRemapSupportsAddAndEdit(t *testing.T) {
	previous := []ModelAdapterConfig{{ID: "a"}, {ID: "b"}}
	submitted := []ModelAdapterConfig{{ID: "a"}, {ID: "b"}, {ID: ""}}
	next := []ModelAdapterConfig{{ID: "a2"}, {ID: "b"}, {ID: "c"}}
	got := deriveChannelIDRemap(previous, submitted, next)
	if len(got) != 1 || got["a"] != "a2" {
		t.Fatalf("add+edit remap = %#v, want a→a2", got)
	}
}

func TestDeriveChannelIDRemapDoesNotPairDeleteWithAdd(t *testing.T) {
	previous := []ModelAdapterConfig{{ID: "old-a"}, {ID: "keep-b"}}
	submitted := []ModelAdapterConfig{{ID: ""}, {ID: "keep-b"}}
	next := []ModelAdapterConfig{{ID: "new-x"}, {ID: "keep-b"}}
	if got := deriveChannelIDRemap(previous, submitted, next); got != nil {
		t.Fatalf("delete+add remap = %#v, want nil", got)
	}
}

func TestDeriveChannelIDRemapRejectsDuplicateIdentityHint(t *testing.T) {
	previous := []ModelAdapterConfig{{ID: "old-a"}, {ID: "keep-b"}}
	submitted := []ModelAdapterConfig{{ID: "old-a"}, {ID: "old-a"}}
	next := []ModelAdapterConfig{{ID: "new-a"}, {ID: "new-x"}}
	if got := deriveChannelIDRemap(previous, submitted, next); got != nil {
		t.Fatalf("duplicate hint remap = %#v, want nil", got)
	}
}

func TestApplyChannelIDRemapRewritesFallbackAndGateway(t *testing.T) {
	adapters := []ModelAdapterConfig{{
		ProviderFallback: ProviderFallbackConfig{
			Enabled:             true,
			PrimaryChannelID:    "old-a",
			CandidateChannelIDs: []string{"keep-b", "old-a"},
		},
	}}
	gateway := GatewayConfig{PublicModels: []GatewayPublicModel{{ID: "pub", TargetAdapterID: "old-a"}}}
	remap := map[string]string{"old-a": "new-a"}
	applyChannelIDRemap(adapters, remap)
	applyChannelIDRemapToGateway(&gateway, remap)
	if adapters[0].ProviderFallback.PrimaryChannelID != "new-a" {
		t.Fatalf("primary = %q", adapters[0].ProviderFallback.PrimaryChannelID)
	}
	if adapters[0].ProviderFallback.CandidateChannelIDs[1] != "new-a" {
		t.Fatalf("candidates = %#v", adapters[0].ProviderFallback.CandidateChannelIDs)
	}
	if gateway.PublicModels[0].TargetAdapterID != "new-a" {
		t.Fatalf("gateway target = %q", gateway.PublicModels[0].TargetAdapterID)
	}
}
