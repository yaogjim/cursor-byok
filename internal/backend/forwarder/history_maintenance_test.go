package forwarder

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestHistoryMaintenanceCleansManagedDebugArtifactsOnly(t *testing.T) {
	historyRoot := t.TempDir()
	conversationDir := filepath.Join(historyRoot, "conversation-1")
	debugDir := filepath.Join(conversationDir, "debug")
	oldTime := time.Now().Add(-debugLogMaxAge - time.Hour)

	statePath := filepath.Join(conversationDir, "state.json")
	legacyPayloadPath := filepath.Join(debugDir, "payloads", "payload-legacy.json")
	oldPackPath := filepath.Join(debugDir, "payloads", "pack-old.jsonl")
	freshPackPath := filepath.Join(debugDir, "payloads", "pack-fresh.jsonl")
	oldEventPath := filepath.Join(debugDir, "provider", "event-old.jsonl")
	freshEventPath := filepath.Join(debugDir, "provider", "event-fresh.jsonl")
	unmanagedPath := filepath.Join(debugDir, "provider", "notes.jsonl")
	orphanEventPath := filepath.Join(historyRoot, "_debug", "orphan", "request-1", "runtime", "event-old.jsonl")

	for path, body := range map[string]string{
		statePath:         `{"id":"conversation-1"}`,
		legacyPayloadPath: `legacy`,
		oldPackPath:       `old pack`,
		freshPackPath:     `fresh pack`,
		oldEventPath:      `old event`,
		freshEventPath:    `fresh event`,
		unmanagedPath:     `unmanaged`,
		orphanEventPath:   `old orphan event`,
	} {
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatalf("create history directory for %s: %v", path, err)
		}
		if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
			t.Fatalf("create history artifact %s: %v", path, err)
		}
	}
	for _, path := range []string{oldPackPath, oldEventPath, orphanEventPath} {
		if err := os.Chtimes(path, oldTime, oldTime); err != nil {
			t.Fatalf("age history artifact %s: %v", path, err)
		}
	}

	service := &Service{store: NewConversationFileStore(historyRoot)}
	if err := service.runHistoryMaintenance(); err != nil {
		t.Fatalf("run history maintenance: %v", err)
	}

	for _, path := range []string{legacyPayloadPath, oldPackPath, oldEventPath, orphanEventPath} {
		if _, err := os.Stat(path); !os.IsNotExist(err) {
			t.Fatalf("expected managed artifact removed %s, err=%v", path, err)
		}
	}
	for _, path := range []string{statePath, freshPackPath, freshEventPath, unmanagedPath} {
		if _, err := os.Stat(path); err != nil {
			t.Fatalf("expected artifact preserved %s: %v", path, err)
		}
	}
}
