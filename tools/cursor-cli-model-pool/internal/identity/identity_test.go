package identity

import (
	"strings"
	"testing"
)

func TestNormalizeBaseURLLowercasesHostAndTrimsSlash(t *testing.T) {
	got, err := NormalizeBaseURL("https://Example.com/v1/")
	if err != nil {
		t.Fatalf("NormalizeBaseURL: %v", err)
	}
	if got != "https://example.com/v1" {
		t.Fatalf("NormalizeBaseURL = %q", got)
	}
}

func TestNormalizeOpenAIEmptyEndpointUsesChatCompletions(t *testing.T) {
	if got := NormalizeOpenAIEndpoint("openai", ""); got != OpenAIEndpointChatCompletions {
		t.Fatalf("openai empty endpoint = %q", got)
	}
	if got := NormalizeOpenAIEndpoint("openai", "/v1/not-a-real-endpoint"); got != "" {
		t.Fatalf("unsupported openai endpoint must be empty, got %q", got)
	}
	if got := NormalizeOpenAIEndpoint("openai", OpenAIEndpointResponses); got != OpenAIEndpointResponses {
		t.Fatalf("responses = %q", got)
	}
	if got := NormalizeOpenAIEndpoint("anthropic", OpenAIEndpointResponses); got != "" {
		t.Fatalf("non-openai endpoint must be empty, got %q", got)
	}
}

func TestOpenAIFivePartDiffersFromLegacyFourPart(t *testing.T) {
	base := "https://example.com/v1"
	five := BuildChannelID(base, "model", "k", "name", OpenAIEndpointChatCompletions)
	four := BuildChannelID(base, "model", "k", "name", "")
	if len(five) != ChannelIDHexLength || len(four) != ChannelIDHexLength {
		t.Fatalf("id length five=%d four=%d", len(five), len(four))
	}
	if five == four {
		t.Fatal("openai five-part identity must differ from legacy four-part")
	}
}

func TestComputeOpenAIUsesNormalizedFivePart(t *testing.T) {
	id, err := Compute("https://Example.com/v1/", "openai", "model", "k", "name", "")
	if err != nil {
		t.Fatalf("Compute: %v", err)
	}
	base, err := NormalizeBaseURL("https://Example.com/v1/")
	if err != nil {
		t.Fatal(err)
	}
	want := BuildChannelID(base, "model", "k", "name", OpenAIEndpointChatCompletions)
	if id != want {
		t.Fatalf("Compute = %q, want %q", id, want)
	}
}

func TestComputeAnthropicUsesLegacyFourPart(t *testing.T) {
	id, err := Compute("https://example.com", "anthropic", "claude", "k", "name", "/v1/responses")
	if err != nil {
		t.Fatalf("Compute: %v", err)
	}
	want := BuildLegacyChannelID("https://example.com", "claude", "k", "name")
	if id != want {
		t.Fatalf("anthropic Compute = %q, want %q", id, want)
	}
}

func TestComputeErrorsDoNotContainAPIKey(t *testing.T) {
	secret := "super-secret-adapter-key"
	_, err := Compute("not a url", "openai", "model", secret, "name", "")
	if err == nil {
		t.Fatal("expected error")
	}
	if strings.Contains(err.Error(), secret) {
		t.Fatalf("error leaked api key: %v", err)
	}
}
