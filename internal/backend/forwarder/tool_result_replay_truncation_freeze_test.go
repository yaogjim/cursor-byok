package forwarder

import (
	"encoding/json"
	"strings"
	"testing"
	"unicode/utf8"
)

func TestFreezeReplayPayloadBudgets(t *testing.T) {
	t.Parallel()
	cases := []struct {
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
		{name: "PatchEditLines", want: 4 * 1024},
		{name: "PatchEditSpan", want: 4 * 1024},
		{name: "Edit", want: 32 * 1024},
		{name: "Write", want: 32 * 1024},
		{name: "WebFetch", want: 32 * 1024},
		{name: "WebSearch", want: 16 * 1024},
		{name: "CallMcpTool", want: 32 * 1024},
		{name: "FetchMcpResource", want: 32 * 1024},
		{name: "ListMcpResources", want: 32 * 1024},
	}
	for _, test := range cases {
		got, ok := projectedToolReplayLimit(test.name)
		if !ok {
			t.Fatalf("projectedToolReplayLimit(%q) missing", test.name)
		}
		if got != test.want {
			t.Fatalf("projectedToolReplayLimit(%q) = %d, want %d", test.name, got, test.want)
		}
	}
	if projectedShellStreamLimit != 16*1024 {
		t.Fatalf("shell stream replay limit = %d", projectedShellStreamLimit)
	}
	if projectedShellInterleavedLimit != 32*1024 {
		t.Fatalf("shell interleaved replay limit = %d", projectedShellInterleavedLimit)
	}
}

func TestFreezeReplayTextAndImageSamples(t *testing.T) {
	t.Parallel()
	fitted := truncateProjectedReplayText("WebFetch", strings.Repeat("x", 200), 80)
	wantFitted := "xxxxxxx\n\n[truncated: WebFetch result exceeded 80 bytes; showing 7 of 200 bytes]"
	if fitted != wantFitted {
		t.Fatalf("replay tail sample = %q, want %q", fitted, wantFitted)
	}
	if again := truncateProjectedReplayText("WebFetch", fitted, 80); again != fitted {
		t.Fatal("replay tail truncation is not idempotent")
	}
	if !utf8.ValidString(truncateProjectedReplayText("WebFetch", "你好😀世界", 4)) {
		t.Fatal("replay tiny chinese/emoji is not valid UTF-8")
	}

	imageJSON := `{"image_data":"` + strings.Repeat("A", 64) + `","caption":"ok"}`
	compacted := limitProjectedToolResultReplay("GenerateImage", imageJSON, "", false, false)
	if strings.Contains(compacted, strings.Repeat("A", 64)) {
		t.Fatalf("replay kept image base64: %s", compacted)
	}
	if !strings.Contains(compacted, "base64 image data omitted from replay") {
		t.Fatalf("replay missing image omit notice: %s", compacted)
	}
	if again := limitProjectedToolResultReplay("GenerateImage", compacted, "", false, false); again != compacted {
		t.Fatal("GenerateImage replay omit is not idempotent")
	}
	displayKeep := imageJSON
	if !strings.Contains(displayKeep, strings.Repeat("A", 64)) {
		t.Fatal("display fixture lost image base64")
	}

	shellJSON := `{"stdout":"` + strings.Repeat("o", projectedShellStreamLimit+8) + `","stderr":"e","exit_code":3}`
	gotShell := limitProjectedToolResultReplay("Shell", shellJSON, "", false, false)
	var payload map[string]any
	if err := json.Unmarshal([]byte(gotShell), &payload); err != nil {
		t.Fatalf("shell replay json: %v %s", err, gotShell)
	}
	if payload["exit_code"] != float64(3) {
		t.Fatalf("shell exit_code mutated: %#v", payload["exit_code"])
	}
	stdout, _ := payload["stdout"].(string)
	if len(stdout) > projectedShellStreamLimit {
		t.Fatalf("shell stdout replay = %d", len(stdout))
	}
	if payload["stderr"] != "e" {
		t.Fatalf("shell stderr mutated: %#v", payload["stderr"])
	}
	againShell := limitProjectedToolResultReplay("Shell", gotShell, "", false, false)
	if againShell != gotShell {
		t.Fatal("shell field replay truncation is not idempotent")
	}
	for _, alias := range []string{"shell", "Bash", "bash"} {
		if got := limitProjectedToolResultReplay(alias, shellJSON, "", false, false); got != gotShell {
			t.Fatalf("alias %q replay diverged from Shell", alias)
		}
	}

	errJSON := `{"error":{"error":"old edit failed with a long explanation","modelVisibleError":"old edit failed with a long explanation"}}`
	compactErr := limitProjectedToolResultReplay("Edit", errJSON, "", false, true)
	if !strings.Contains(compactErr, "historical edit error omitted from replay") {
		t.Fatalf("historical edit error was not compacted: %s", compactErr)
	}
	if strings.Contains(compactErr, "old edit failed with a long explanation") {
		t.Fatalf("historical edit error kept redundant text: %s", compactErr)
	}
	if again := limitProjectedToolResultReplay("Edit", compactErr, "", false, true); again != compactErr {
		t.Fatal("historical edit error compact is not idempotent")
	}
}
