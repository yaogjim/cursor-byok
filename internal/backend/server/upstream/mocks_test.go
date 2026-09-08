package upstream

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"

	"cursor/gen/agentv1"
	legacyruntime "cursor/internal/runtime"

	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"
)

func TestBuildCLIModelDetailsManagedAdaptersUseSentinelWithoutBaseURL(t *testing.T) {
	got := buildCLIModelDetails([]legacyruntime.ModelAdapterConfig{
		{ID: "codex-channel", DisplayName: "managed-codex", ModelID: "gpt-codex", CredentialSource: "codex", BaseURL: "https://chatgpt.com/backend-api/codex/responses"},
		{ID: "grok-channel", DisplayName: "managed-grok", ModelID: "grok-3", CredentialSource: "grok", BaseURL: "https://api.x.ai/v1"},
	})
	if len(got) != 2 {
		t.Fatalf("managed catalog count = %d, want 2", len(got))
	}
	wantIDs := []string{"codex-channel", "grok-channel"}
	for i, model := range got {
		if model["modelId"] != wantIDs[i] || model["displayModelId"] != wantIDs[i] {
			t.Fatalf("model %d used provider name instead of channel ID: %#v", i, model)
		}
		if model["modelId"] == "gpt-codex" || model["modelId"] == "grok-3" {
			t.Fatalf("model %d leaked provider modelID: %#v", i, model)
		}
		credentials, _ := model["apiKeyCredentials"].(map[string]any)
		if credentials["apiKey"] != "cursor-byok-local" {
			t.Fatalf("model %d apiKey = %#v", i, credentials["apiKey"])
		}
		if _, hasBaseURL := credentials["baseUrl"]; hasBaseURL {
			t.Fatalf("model %d builder map included baseUrl: %#v", i, credentials)
		}
	}
}

func TestBuildCLIModelDetailsPreservesChannelMetadata(t *testing.T) {
	adapters := []legacyruntime.ModelAdapterConfig{
		{ID: " channel-a ", DisplayName: " Model A ", ModelID: "model-a", APIKey: "provider-secret-a", BaseURL: "https://provider-a.example/v1"},
		{ID: "channel-b", DisplayName: "Model B", ModelID: "model-a"},
		{ID: "", ModelID: "model-c"},
	}

	got := buildCLIModelDetails(adapters)
	want := []map[string]any{
		{"modelId": "channel-a", "displayModelId": "channel-a", "displayName": "Model A", "displayNameShort": "Model A", "apiKeyCredentials": map[string]any{"apiKey": "cursor-byok-local"}},
		{"modelId": "channel-b", "displayModelId": "channel-b", "displayName": "Model B", "displayNameShort": "Model B", "apiKeyCredentials": map[string]any{"apiKey": "cursor-byok-local"}},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("build CLI model details: got %v, want %v", got, want)
	}
	for i, model := range got {
		credentials, _ := model["apiKeyCredentials"].(map[string]any)
		if _, hasBaseURL := credentials["baseUrl"]; hasBaseURL {
			t.Fatalf("model %d builder map included baseUrl: %#v", i, credentials)
		}
		if credentials["apiKey"] != "cursor-byok-local" {
			t.Fatalf("model %d apiKey = %#v, want cursor-byok-local", i, credentials["apiKey"])
		}
	}
}

func TestDefaultThinkingEffortForOpenAIAdapterUsesDisabledWhenUnset(t *testing.T) {
	adapter := legacyruntime.ModelAdapterConfig{Type: "openai", ReasoningEffort: ""}

	if got := defaultThinkingEffortForAdapter(adapter); got != "disabled" {
		t.Fatalf("default thinking effort = %q, want disabled", got)
	}
}

func TestBuildAvailableModelEntriesUsesDisabledVariantWhenReasoningEffortUnset(t *testing.T) {
	entries := buildAvailableModelEntries([]legacyruntime.ModelAdapterConfig{{
		ID:          "channel-a",
		DisplayName: "Model A",
		ModelID:     "model-a",
		Type:        "openai",
	}})
	if len(entries) != 1 {
		t.Fatalf("entry count = %d, want 1", len(entries))
	}

	variants, ok := entries[0]["variants"].([]map[string]any)
	if !ok {
		t.Fatalf("variants type = %T, want []map[string]any", entries[0]["variants"])
	}
	if len(variants) == 0 {
		t.Fatal("variants should not be empty")
	}
	if got := variants[0]["variantStringRepresentation"]; got != "channel-a:disabled" {
		t.Fatalf("first variant representation = %#v, want channel-a:disabled", got)
	}
	if got := variants[0]["isDefaultNonMaxConfig"]; got != true {
		t.Fatalf("disabled variant default flag = %#v, want true", got)
	}
}

func TestCursorAvailableModelsProjectsChannelHashNotProviderModelID(t *testing.T) {
	entries := buildAvailableModelEntries([]legacyruntime.ModelAdapterConfig{{
		ID:          "abcdef0123456789",
		DisplayName: "Grok",
		ModelID:     "grok-3",
		Type:        "openai",
		TooltipData: "note",
	}})
	if len(entries) != 1 {
		t.Fatalf("entry count = %d", len(entries))
	}
	if entries[0]["name"] != "abcdef0123456789" || entries[0]["serverModelName"] != "abcdef0123456789" {
		t.Fatalf("cursor model projection = %#v", entries[0])
	}
	if entries[0]["name"] == "grok-3" || entries[0]["serverModelName"] == "grok-3" {
		t.Fatal("cursor model projection used provider modelID")
	}
}

func TestCatalogCLIAndAvailableModelsShareChannelIDThinkingAndCapability(t *testing.T) {
	adapters := []legacyruntime.ModelAdapterConfig{
		{ID: "static-id", DisplayName: "static-byok", ModelID: "static-model", Type: "openai", ReasoningEffort: "medium", TooltipData: "static-byok", APIKey: "static-secret", BaseURL: "https://static.example/v1"},
		{ID: "codex-id", DisplayName: "managed-codex", ModelID: "managed-model", Type: "openai", CredentialSource: "codex", ReasoningEffort: "medium", TooltipData: "managed-codex", BaseURL: "https://chatgpt.com/backend-api/codex/responses"},
		{ID: "grok-id", DisplayName: "managed-grok", ModelID: "grok-3", Type: "openai", CredentialSource: "grok", ReasoningEffort: "medium", TooltipData: "managed-grok", BaseURL: "https://api.x.ai/v1"},
		{ID: "logical-id", DisplayName: "logical-alias", ModelID: "logical-model", Type: "openai", ReasoningEffort: "medium", TooltipData: "logical-alias", APIKey: "logical-secret", BaseURL: "https://logical.example/v1"},
		{ID: "primary-id", DisplayName: "fallback-primary", ModelID: "primary-model", Type: "openai", ReasoningEffort: "medium", TooltipData: "fallback-primary", APIKey: "primary-secret", BaseURL: "https://primary.example/v1"},
	}
	cli := buildCLIModelDetails(adapters)
	available := buildAvailableModelEntries(adapters)
	if len(cli) != len(adapters) || len(available) != len(adapters) {
		t.Fatalf("catalog sizes cli=%d available=%d adapters=%d", len(cli), len(available), len(adapters))
	}
	for i, adapter := range adapters {
		cliID, _ := cli[i]["modelId"].(string)
		if cliID != adapter.ID || cli[i]["displayModelId"] != adapter.ID {
			t.Fatalf("%s CLI ID = %#v", adapter.DisplayName, cli[i])
		}
		if cliID == adapter.ModelID {
			t.Fatalf("%s CLI catalog used provider modelID", adapter.DisplayName)
		}
		credentials, _ := cli[i]["apiKeyCredentials"].(map[string]any)
		if credentials["apiKey"] != "cursor-byok-local" {
			t.Fatalf("%s CLI apiKey = %#v", adapter.DisplayName, credentials["apiKey"])
		}
		if _, hasBaseURL := credentials["baseUrl"]; hasBaseURL {
			t.Fatalf("%s CLI included baseUrl", adapter.DisplayName)
		}
		entry := available[i]
		if entry["name"] != adapter.ID || entry["serverModelName"] != adapter.ID {
			t.Fatalf("%s available ID = %#v", adapter.DisplayName, entry)
		}
		if entry["name"] == adapter.ModelID {
			t.Fatalf("%s available catalog used provider modelID", adapter.DisplayName)
		}
		if entry["supportsAgent"] != true || entry["supportsThinking"] != true || entry["supportsImages"] != true || entry["supportsPlanMode"] != true {
			t.Fatalf("%s capabilities = %#v", adapter.DisplayName, entry)
		}
		params, _ := entry["parameterDefinitions"].([]map[string]any)
		if len(params) == 0 || params[0]["id"] != "thinking_effort" {
			t.Fatalf("%s thinking parameter = %#v", adapter.DisplayName, params)
		}
		variants, _ := entry["variants"].([]map[string]any)
		if len(variants) == 0 || variants[0]["isDefaultNonMaxConfig"] != true || variants[0]["variantStringRepresentation"] != adapter.ID+":medium" {
			t.Fatalf("%s default thinking variant = %#v", adapter.DisplayName, variants)
		}
	}
	if cli[3]["modelId"] != "logical-id" || available[3]["name"] != "logical-id" {
		t.Fatalf("logical catalog ID rewritten: cli=%#v available=%#v", cli[3], available[3])
	}
	if cli[3]["modelId"] == "primary-id" || available[3]["name"] == "primary-id" {
		t.Fatal("logical fallback ID became a physical pool ID")
	}
	if cli[4]["modelId"] != "primary-id" {
		t.Fatalf("physical fallback channel missing from CLI pool: %#v", cli[4])
	}
}

func TestEncodeCLIModelsUsesAgentModelDetailsWireFormat(t *testing.T) {
	payload := map[string]any{"models": buildCLIModelDetails([]legacyruntime.ModelAdapterConfig{{ID: "channel-a", DisplayName: "Model A", APIKey: "provider-secret", BaseURL: "https://provider.example/v1"}})}
	encoded, err := encodeMockProto("aiserver.v1.GetUsableModelsResponse", payload)
	if err != nil {
		t.Fatalf("encode CLI models: %v", err)
	}

	response := &agentv1.GetUsableModelsResponse{}
	if err := proto.Unmarshal(encoded, response); err != nil {
		t.Fatalf("decode CLI models with agent proto: %v", err)
	}
	if len(response.Models) != 1 {
		t.Fatalf("decoded model count: got %d, want 1", len(response.Models))
	}
	model := response.Models[0]
	if model.GetModelId() != "channel-a" || model.GetDisplayModelId() != "channel-a" {
		t.Fatalf("decoded channel IDs: model=%q display=%q", model.GetModelId(), model.GetDisplayModelId())
	}
	if model.GetDisplayName() != "Model A" || model.GetDisplayNameShort() != "Model A" {
		t.Fatalf("decoded display names: name=%q short=%q", model.GetDisplayName(), model.GetDisplayNameShort())
	}
	credentials := model.GetApiKeyCredentials()
	if credentials == nil {
		t.Fatal("decoded credentials are nil")
	}
	if credentials.GetApiKey() != "cursor-byok-local" {
		t.Fatalf("decoded apiKey = %q, want cursor-byok-local", credentials.GetApiKey())
	}
	if credentials.GetApiKey() == "provider-secret" || credentials.GetBaseUrl() == "https://provider.example/v1" {
		t.Fatalf("decoded credentials leaked provider secret: %#v", credentials)
	}
	if credentials.BaseUrl != nil {
		t.Fatalf("protobuf HasBaseUrl: base_url=%q, want unset", credentials.GetBaseUrl())
	}
	assertCLICredentialsJSONOmitsBaseURL(t, credentials)
}

func TestEncodeCLIDefaultModelOmitsBaseURLAndUsesSentinel(t *testing.T) {
	payload := map[string]any{"model": buildCLIModelDetails([]legacyruntime.ModelAdapterConfig{{ID: "channel-a", DisplayName: "Model A", APIKey: "provider-secret", BaseURL: "https://provider.example/v1"}})[0]}
	encoded, err := encodeMockProto("aiserver.v1.GetDefaultModelForCliResponse", payload)
	if err != nil {
		t.Fatalf("encode default CLI model: %v", err)
	}
	response := &agentv1.GetDefaultModelForCliResponse{}
	if err := proto.Unmarshal(encoded, response); err != nil {
		t.Fatalf("decode default CLI model: %v", err)
	}
	if response.GetModel() == nil || response.GetModel().GetModelId() != "channel-a" {
		t.Fatalf("decoded default model = %#v", response.GetModel())
	}
	credentials := response.GetModel().GetApiKeyCredentials()
	if credentials == nil || credentials.GetApiKey() != "cursor-byok-local" {
		t.Fatalf("decoded default credentials = %#v", credentials)
	}
	if credentials.BaseUrl != nil {
		t.Fatalf("protobuf HasBaseUrl: base_url=%q, want unset", credentials.GetBaseUrl())
	}
	assertCLICredentialsJSONOmitsBaseURL(t, credentials)
}

func TestEncodeEmptyCLIDefaultModelKeepsLegalEmptyResponse(t *testing.T) {
	encoded, err := encodeMockProto("aiserver.v1.GetDefaultModelForCliResponse", map[string]any{"model": map[string]any{}})
	if err != nil {
		t.Fatalf("encode empty default CLI model: %v", err)
	}
	response := &agentv1.GetDefaultModelForCliResponse{}
	if err := proto.Unmarshal(encoded, response); err != nil {
		t.Fatalf("decode empty default CLI model: %v", err)
	}
	if response.GetModel() == nil {
		t.Fatal("empty default model should remain a present empty message")
	}
	if response.GetModel().GetModelId() != "" {
		t.Fatalf("empty default modelId = %q", response.GetModel().GetModelId())
	}
}

func assertCLICredentialsJSONOmitsBaseURL(t *testing.T, credentials *agentv1.ApiKeyCredentials) {
	t.Helper()
	encodedJSON, err := protojson.Marshal(credentials)
	if err != nil {
		t.Fatalf("protojson marshal credentials: %v", err)
	}
	if strings.Contains(strings.ToLower(string(encodedJSON)), "baseurl") || strings.Contains(string(encodedJSON), "base_url") {
		t.Fatalf("JSON credentials included baseUrl: %s", encodedJSON)
	}
}

func TestBuildBootstrapStatsigConfigJSONDisablesAlwaysLocalDecompositionGate(t *testing.T) {
	payload, err := buildBootstrapStatsigConfigJSON(12345, "test-auth-id")
	if err != nil {
		t.Fatalf("build bootstrap statsig config: %v", err)
	}

	var decoded statsigBootstrapTemplate
	if err := json.Unmarshal(payload, &decoded); err != nil {
		t.Fatalf("decode bootstrap statsig config: %v", err)
	}

	gate, ok := decoded.FeatureGates[bootstrapStatsigDecomposeAlwaysLocalExtHostGate]
	if !ok {
		t.Fatalf("missing feature gate %q", bootstrapStatsigDecomposeAlwaysLocalExtHostGate)
	}
	if value, _ := gate["value"].(bool); value {
		t.Fatalf("expected %q to be disabled", bootstrapStatsigDecomposeAlwaysLocalExtHostGate)
	}
	if ruleID, _ := gate["rule_id"].(string); ruleID != "local_disabled" {
		t.Fatalf("unexpected rule_id: %q", ruleID)
	}
}

func TestBuildBootstrapStatsigConfigJSONEnablesTerminalOutputUIStreaming(t *testing.T) {
	payload, err := buildBootstrapStatsigConfigJSON(12345, "test-auth-id")
	if err != nil {
		t.Fatalf("build bootstrap statsig config: %v", err)
	}

	var decoded statsigBootstrapTemplate
	if err := json.Unmarshal(payload, &decoded); err != nil {
		t.Fatalf("decode bootstrap statsig config: %v", err)
	}

	gate, ok := decoded.FeatureGates[bootstrapStatsigDisableTerminalOutputUIStreaming]
	if !ok {
		t.Fatalf("missing feature gate %q", bootstrapStatsigDisableTerminalOutputUIStreaming)
	}
	if value, _ := gate["value"].(bool); value {
		t.Fatalf("expected %q to be disabled", bootstrapStatsigDisableTerminalOutputUIStreaming)
	}
	if ruleID, _ := gate["rule_id"].(string); ruleID != "local_disabled" {
		t.Fatalf("unexpected rule_id: %q", ruleID)
	}
}
