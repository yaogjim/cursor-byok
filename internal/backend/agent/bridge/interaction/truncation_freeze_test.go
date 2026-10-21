package interaction

import (
	"strings"
	"testing"
	"unicode/utf8"

	"cursor/gen/agentv1"
	"cursor/internal/backend/agent/toolresult"
)

func TestFreezeInteractionDisplayBudgets(t *testing.T) {
	t.Parallel()
	if webFetchMarkdownLimit != 32*1024 {
		t.Fatalf("WebFetch display markdown limit = %d, want %d", webFetchMarkdownLimit, 32*1024)
	}
	if webSearchPayloadLimit != 16*1024 {
		t.Fatalf("WebSearch display payload limit = %d, want %d", webSearchPayloadLimit, 16*1024)
	}
	if webSearchTitleLimit != 512 {
		t.Fatalf("WebSearch display title limit = %d, want %d", webSearchTitleLimit, 512)
	}
	if webSearchChunkLimit != 2*1024 {
		t.Fatalf("WebSearch display chunk limit = %d, want %d", webSearchChunkLimit, 2*1024)
	}
}

func TestFreezeWebFetchDisplaySamples(t *testing.T) {
	t.Parallel()
	over := strings.Repeat("a", webFetchMarkdownLimit+8)
	got := truncateWebFetchMarkdown(over)
	assertFrozenTextSample(t, "WebFetch", got, webFetchMarkdownLimit, over)
	if got2 := truncateWebFetchMarkdown(got); got2 != got {
		t.Fatal("WebFetch display truncation is not idempotent")
	}

	tiny := toolresult.TruncateTail("WebFetch", "你好😀世界", 1)
	if tiny != "" {
		t.Fatalf("tiny budget 1 = %q, want empty UTF-8 cut", tiny)
	}
	if got := toolresult.TruncateTail("WebFetch", "你好😀世界", 4); got != "你" {
		t.Fatalf("tiny budget 4 = %q, want 你", got)
	}
	if got := toolresult.TruncateTail("WebFetch", "你好😀世界", 8); got != "你好" {
		t.Fatalf("tiny budget 8 = %q, want 你好", got)
	}

	cn := strings.Repeat("你", 20) + "世界"
	gotCN := toolresult.TruncateTail("WebFetch", cn, 40)
	if !utf8.ValidString(gotCN) || len(gotCN) > 40 {
		t.Fatalf("chinese sample invalid: %q len=%d", gotCN, len(gotCN))
	}
	if gotCN != strings.Repeat("你", 13) {
		t.Fatalf("chinese sample = %q", gotCN)
	}

	emoji := toolresult.TruncateTail("WebFetch", strings.Repeat("😀", 20), 40)
	if emoji != strings.Repeat("😀", 10) {
		t.Fatalf("emoji sample = %q", emoji)
	}

	fitted := toolresult.TruncateTail("WebFetch", strings.Repeat("x", 200), 80)
	wantFitted := "xxxxxxx\n\n[truncated: WebFetch result exceeded 80 bytes; showing 7 of 200 bytes]"
	if fitted != wantFitted {
		t.Fatalf("notice-in-budget sample = %q, want %q", fitted, wantFitted)
	}
	if len(fitted) > 80 {
		t.Fatalf("notice not included in budget: len=%d", len(fitted))
	}
	if again := toolresult.TruncateTail("WebFetch", fitted, 80); again != fitted {
		t.Fatal("notice-in-budget sample is not idempotent")
	}
}

func TestFreezeWebSearchDisplaySamples(t *testing.T) {
	t.Parallel()
	refs := []*agentv1.WebSearchReference{{
		Title: strings.Repeat("T", webSearchTitleLimit+16),
		Url:   "https://example.test/a",
		Chunk: strings.Repeat("C", webSearchChunkLimit+16),
	}}
	payload := strings.Repeat("P", webSearchPayloadLimit+32)
	gotRefs, gotPayload := truncateWebSearchReplay("query", refs, payload)
	if len(gotRefs) != 1 {
		t.Fatalf("reference count = %d, want 1", len(gotRefs))
	}
	if len(gotRefs[0].GetTitle()) > webSearchTitleLimit {
		t.Fatalf("title bytes = %d, want <= %d", len(gotRefs[0].GetTitle()), webSearchTitleLimit)
	}
	if len(gotPayload) > webSearchPayloadLimit {
		t.Fatalf("payload bytes = %d, want <= %d", len(gotPayload), webSearchPayloadLimit)
	}
	if !strings.Contains(gotPayload, "[truncated:") {
		t.Fatalf("payload missing truncation notice: %q", gotPayload[:min(len(gotPayload), 120)])
	}
	if gotRefs[0].GetUrl() != "https://example.test/a" {
		t.Fatalf("url mutated: %q", gotRefs[0].GetUrl())
	}

	againRefs, againPayload := truncateWebSearchReplay("query", gotRefs, gotPayload)
	if againPayload != gotPayload {
		t.Fatal("WebSearch payload truncation is not idempotent")
	}
	if len(againRefs) != 1 || againRefs[0].GetUrl() != gotRefs[0].GetUrl() {
		t.Fatal("WebSearch reference identity changed on re-application")
	}
	if strings.Count(againRefs[0].GetChunk(), "[truncated: WebSearch result exceeded") != strings.Count(gotRefs[0].GetChunk(), "[truncated: WebSearch result exceeded") {
		t.Fatal("WebSearch extra notice grew on re-application")
	}
}

func assertFrozenTextSample(t *testing.T, label, got string, limit int, original string) {
	t.Helper()
	if len(got) > limit {
		t.Fatalf("%s truncated length = %d, want <= %d", label, len(got), limit)
	}
	if !utf8.ValidString(got) {
		t.Fatalf("%s truncated text is not valid UTF-8", label)
	}
	if !strings.Contains(got, label) || !strings.Contains(got, "truncated") {
		t.Fatalf("%s missing truncation notice: %q", label, got[:min(len(got), 160)])
	}
	if !strings.HasPrefix(strings.TrimRight(got, "\n"), original[:min(8, len(original))]) {
		t.Fatalf("%s lost original prefix", label)
	}
}
