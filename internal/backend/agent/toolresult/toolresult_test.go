package toolresult

import (
	"strings"
	"testing"
	"unicode/utf8"
)

func TestFreezePayloadBudgetsByPurpose(t *testing.T) {
	t.Parallel()
	display := []struct {
		name string
		want int
	}{
		{name: "WebFetch", want: WebFetchMarkdownBytes},
		{name: "WebSearch", want: WebSearchPayloadBytes},
	}
	for _, test := range display {
		got, ok := PayloadBytes(test.name, PurposeDisplay)
		if !ok || got != test.want {
			t.Fatalf("display %s = %d ok=%v, want %d", test.name, got, ok, test.want)
		}
	}
	if _, ok := PayloadBytes("Shell", PurposeDisplay); ok {
		t.Fatal("Shell must not have a whole-payload display budget")
	}
	if _, ok := PayloadBytes("GenerateImage", PurposeDisplay); ok {
		t.Fatal("GenerateImage display must keep original payload; no omit budget")
	}

	replay := []struct {
		name string
		want int
	}{
		{name: "GenerateImage", want: 16 * 1024},
		{name: "Read", want: 64 * 1024},
		{name: "Shell", want: 128 * 1024},
		{name: "shell", want: 128 * 1024},
		{name: "Bash", want: 128 * 1024},
		{name: "bash", want: 128 * 1024},
		{name: "Grep", want: 32 * 1024},
		{name: "PatchEdit", want: 4 * 1024},
		{name: "Edit", want: 32 * 1024},
		{name: "Write", want: 32 * 1024},
		{name: "WebFetch", want: 32 * 1024},
		{name: "WebSearch", want: 16 * 1024},
		{name: "CallMcpTool", want: 32 * 1024},
		{name: "ListMcpResources", want: 32 * 1024},
	}
	for _, test := range replay {
		got, ok := PayloadBytes(test.name, PurposeReplay)
		if !ok || got != test.want {
			t.Fatalf("replay %s = %d ok=%v, want %d", test.name, got, ok, test.want)
		}
	}
}

func TestTruncateTailNoticeBudgetUTF8AndIdempotent(t *testing.T) {
	t.Parallel()
	fitted := TruncateTail("WebFetch", strings.Repeat("x", 200), 80)
	want := "xxxxxxx\n\n[truncated: WebFetch result exceeded 80 bytes; showing 7 of 200 bytes]"
	if fitted != want {
		t.Fatalf("fitted sample = %q, want %q", fitted, want)
	}
	if len(fitted) > 80 {
		t.Fatalf("notice not within budget: %d", len(fitted))
	}
	if again := TruncateTail("WebFetch", fitted, 80); again != fitted {
		t.Fatal("TruncateTail is not idempotent")
	}

	if got := TruncateTail("WebFetch", "你好😀世界", 1); got != "" {
		t.Fatalf("tiny 1 = %q", got)
	}
	if got := TruncateTail("WebFetch", "你好😀世界", 4); got != "你" {
		t.Fatalf("tiny 4 = %q", got)
	}
	if got := TruncateTail("WebFetch", "你好😀世界", 8); got != "你好" {
		t.Fatalf("tiny 8 = %q", got)
	}
	if got := TruncateTail("WebFetch", strings.Repeat("😀", 20), 40); got != strings.Repeat("😀", 10) {
		t.Fatalf("emoji tiny = %q", got)
	}
	cn := TruncateTail("WebFetch", strings.Repeat("你", 20)+"世界", 40)
	if !utf8.ValidString(cn) || cn != strings.Repeat("你", 13) {
		t.Fatalf("chinese tiny = %q", cn)
	}

	readFitted := TruncateTail("Read", strings.Repeat("x", 200), 80)
	wantRead := "xxxxxxxxxxx\n\n[truncated: Read result exceeded 80 bytes; showing 11 of 200 bytes]"
	if readFitted != wantRead {
		t.Fatalf("read sample = %q, want %q", readFitted, wantRead)
	}
}

func TestTruncateMiddleNoticeAndIdempotent(t *testing.T) {
	t.Parallel()
	body := strings.Repeat("H", 40) + strings.Repeat("T", 40)
	if got := TruncateMiddle("Shell stdout", body, 160); got != body {
		t.Fatalf("under limit middle changed: %q", got)
	}
	tiny := TruncateMiddle("Shell stdout", strings.Repeat("a", 50), 20)
	if tiny != strings.Repeat("a", 20) {
		t.Fatalf("tiny middle = %q", tiny)
	}
	over := TruncateMiddle("Shell stdout", strings.Repeat("a", 400), 160)
	if !strings.Contains(over, "omitted middle") {
		t.Fatalf("middle notice missing: %q", over)
	}
	if len(over) > 160 {
		t.Fatalf("middle notice not within budget: %d", len(over))
	}
	if again := TruncateMiddle("Shell stdout", over, 160); again != over {
		t.Fatal("TruncateMiddle is not idempotent")
	}
}

func TestNoticeConstructors(t *testing.T) {
	t.Parallel()
	if got := BytesNotice("WebFetch", 32768, 100, 40000); got != "[truncated: WebFetch result exceeded 32768 bytes; showing 100 of 40000 bytes]" {
		t.Fatalf("bytes notice = %q", got)
	}
	if got := CountNotice("ListMcpResources", "resources", 200, 200, 250); got != "[truncated: ListMcpResources result exceeded 200 resources; showing 200 of 250 resources]" {
		t.Fatalf("count notice = %q", got)
	}
	if got := ItemsNotice("MCP content items", 20, 20, 25); got != "[truncated: MCP content items exceeded 20 items; showing 20 of 25 items]" {
		t.Fatalf("items notice = %q", got)
	}
}
