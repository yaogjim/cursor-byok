package journal

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestAppendWritesAllowlistedFieldsAndMode0600(t *testing.T) {
	path := filepath.Join(t.TempDir(), "journal.jsonl")
	w, err := OpenPath(path)
	if err != nil {
		t.Fatal(err)
	}
	code := 1
	if err := w.Append(Record{
		OrchestrationID:  "abc",
		ModelID:          "0123456789abcdef",
		ModelIndex:       0,
		Phase:            "terminal",
		ErrorCategory:    "http_429",
		OutputObserved:   false,
		MutationObserved: false,
		WorktreeName:     "cursor-poolabc",
		ExitCode:         &code,
	}); err != nil {
		t.Fatal(err)
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("perm = %o", info.Mode().Perm())
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var rec map[string]any
	if err := json.Unmarshal(raw, &rec); err != nil {
		t.Fatal(err)
	}
	allowed := map[string]struct{}{
		"schema_version": {}, "orchestration_id": {}, "model_id": {}, "model_index": {},
		"phase": {}, "session_id": {}, "request_id": {}, "exit_code": {},
		"error_category": {}, "output_observed": {}, "mutation_observed": {},
		"worktree_name": {}, "time": {},
	}
	for key := range rec {
		if _, ok := allowed[key]; !ok {
			t.Fatalf("unexpected journal field %s", key)
		}
	}
	if rec["session_id"] != Unknown || rec["request_id"] != Unknown {
		t.Fatalf("missing ids must be unknown: %#v", rec)
	}
}

func TestAppendDoesNotWritePromptOrAbsolutePath(t *testing.T) {
	path := filepath.Join(t.TempDir(), "journal.jsonl")
	w, err := OpenPath(path)
	if err != nil {
		t.Fatal(err)
	}
	prompt := "UNIQUE-PROMPT-BODY"
	abs := filepath.Join(t.TempDir(), "repo")
	if err := w.Append(Record{
		OrchestrationID: "deadbeefdeadbeef",
		ModelID:         "0123456789abcdef",
		Phase:           "pre_output",
		WorktreeName:    "cursor-pooldeadbeefdeadbeef",
	}); err != nil {
		t.Fatal(err)
	}
	_ = w.Close()
	raw, _ := os.ReadFile(path)
	text := string(raw)
	if strings.Contains(text, prompt) || strings.Contains(text, abs) {
		t.Fatalf("journal leaked prompt or path: %s", text)
	}
}
