package byok

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"cursor-cli-model-pool/internal/identity"
)

func TestLoadFileComputesIDsAndDropsSecrets(t *testing.T) {
	dir := t.TempDir()
	key := "in-memory-only-key"
	path := filepath.Join(dir, "config.yaml")
	body := "" +
		"modelAdapters:\n" +
		"  - displayName: one\n" +
		"    type: openai\n" +
		"    baseURL: https://Example.com/v1/\n" +
		"    apiKey: " + key + "\n" +
		"    modelID: gpt\n" +
		"    providerFallback:\n" +
		"      enabled: false\n" +
		"  - displayName: two\n" +
		"    type: anthropic\n" +
		"    baseURL: https://example.com\n" +
		"    apiKey: " + key + "\n" +
		"    modelID: claude\n" +
		"    providerFallback:\n" +
		"      enabled: true\n"
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	got, err := LoadFile(path)
	if err != nil {
		t.Fatalf("LoadFile: %v", err)
	}
	wantOpenAI, err := identity.Compute("https://Example.com/v1/", "openai", "gpt", key, "one", "")
	if err != nil {
		t.Fatal(err)
	}
	wantAnthropic, err := identity.Compute("https://example.com", "anthropic", "claude", key, "two", "")
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 {
		t.Fatalf("len=%d", len(got))
	}
	if got[0].ID != wantOpenAI || got[0].FallbackEnabled {
		t.Fatalf("openai adapter = %+v", got[0])
	}
	if got[1].ID != wantAnthropic || !got[1].FallbackEnabled {
		t.Fatalf("anthropic adapter = %+v", got[1])
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(raw), key) {
		t.Fatal("test setup lost key on disk")
	}
}

func TestLoadFileRejectsDuplicateDerivedIDs(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	body := "" +
		"modelAdapters:\n" +
		"  - displayName: one\n" +
		"    type: openai\n" +
		"    baseURL: https://example.com/v1\n" +
		"    apiKey: k\n" +
		"    modelID: gpt\n" +
		"  - displayName: one\n" +
		"    type: openai\n" +
		"    baseURL: https://example.com/v1\n" +
		"    apiKey: k\n" +
		"    modelID: gpt\n"
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	_, err := LoadFile(path)
	if err == nil {
		t.Fatal("expected duplicate id error")
	}
	if strings.Contains(err.Error(), "k") && len(err.Error()) < 8 {
		t.Fatalf("error looks like it leaked a key: %v", err)
	}
}
