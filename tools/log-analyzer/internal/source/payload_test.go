package source

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestReadPayloadAcceptsBoundedSessionRelativeJSON(t *testing.T) {
	session := t.TempDir()
	payloadDir := filepath.Join(session, "payloads")
	if err := os.Mkdir(payloadDir, 0o700); err != nil {
		t.Fatal(err)
	}
	eventsPath := filepath.Join(session, "events.jsonl")
	if err := os.WriteFile(eventsPath, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	payloadPath := filepath.Join(payloadDir, "0001.json")
	if err := os.WriteFile(payloadPath, []byte("  {\"prompt\":\"sensitive\"}\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	document, err := ReadPayload(PayloadRequest{EventsFilePath: eventsPath, Reference: "payloads/0001.json"})
	if err != nil {
		t.Fatalf("ReadPayload() error = %v", err)
	}
	if !document.Sensitive || string(document.Content) != `{"prompt":"sensitive"}` {
		t.Fatalf("unexpected document: %+v", document)
	}
}

func TestReadPayloadRejectsEscapeWrongScopeAndOversize(t *testing.T) {
	session := t.TempDir()
	payloadDir := filepath.Join(session, "payloads")
	if err := os.Mkdir(payloadDir, 0o700); err != nil {
		t.Fatal(err)
	}
	eventsPath := filepath.Join(session, "events.jsonl")
	if err := os.WriteFile(eventsPath, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(payloadDir, "large.json"), []byte(`{"value":"too large"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(session, "manifest.json"), []byte(`{}`), 0o600); err != nil {
		t.Fatal(err)
	}

	tests := []struct {
		name      string
		reference string
		maxBytes  int64
		contains  string
	}{
		{name: "parent escape", reference: "../outside.json", contains: "escapes"},
		{name: "absolute", reference: filepath.Join(string(filepath.Separator), "outside.json"), contains: "relative"},
		{name: "wrong scope", reference: "manifest.json", contains: "payloads/"},
		{name: "oversize", reference: "payloads/large.json", maxBytes: 4, contains: "exceeds"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			_, err := ReadPayload(PayloadRequest{EventsFilePath: eventsPath, Reference: test.reference, MaxBytes: test.maxBytes})
			if err == nil || !strings.Contains(err.Error(), test.contains) {
				t.Fatalf("ReadPayload() error = %v, want containing %q", err, test.contains)
			}
		})
	}
}

func TestReadPayloadRejectsSymlink(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("symlink creation is not reliably available")
	}
	session := t.TempDir()
	payloadDir := filepath.Join(session, "payloads")
	if err := os.Mkdir(payloadDir, 0o700); err != nil {
		t.Fatal(err)
	}
	eventsPath := filepath.Join(session, "events.jsonl")
	if err := os.WriteFile(eventsPath, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	outside := filepath.Join(t.TempDir(), "outside.json")
	if err := os.WriteFile(outside, []byte(`{"secret":true}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(payloadDir, "link.json")); err != nil {
		t.Fatal(err)
	}
	if _, err := ReadPayload(PayloadRequest{EventsFilePath: eventsPath, Reference: "payloads/link.json"}); err == nil || !strings.Contains(err.Error(), "symbolic link") {
		t.Fatalf("ReadPayload() symlink error = %v", err)
	}
}
