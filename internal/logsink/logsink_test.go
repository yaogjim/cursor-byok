package logsink

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestRotatingFileSplitsAndRetains(t *testing.T) {
	dir := t.TempDir()
	writer := NewRotatingFile(dir, RotationConfig{
		Prefix:    "event",
		Extension: ".jsonl",
		MaxBytes:  12,
		MaxFiles:  2,
	})
	for _, payload := range []string{"first-line\n", "second-line\n", "third-line\n"} {
		if _, err := writer.Append([]byte(payload)); err != nil {
			t.Fatalf("append payload: %v", err)
		}
	}
	if err := writer.Close(); err != nil {
		t.Fatalf("close writer: %v", err)
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("read log directory: %v", err)
	}
	if len(entries) != 2 {
		t.Fatalf("expected 2 retained segments, got %d", len(entries))
	}
}

func TestRotatingFileRotatesWhenActiveShardExpires(t *testing.T) {
	dir := t.TempDir()
	now := time.Now().UTC()
	writer := NewRotatingFile(dir, RotationConfig{
		Prefix:    "event",
		Extension: ".jsonl",
		MaxBytes:  1 << 20,
		MaxAge:    time.Hour,
		Now:       func() time.Time { return now },
	})
	if _, err := writer.Append([]byte("first\n")); err != nil {
		t.Fatalf("append first payload: %v", err)
	}
	firstPath := writer.path
	old := now.Add(-2 * time.Hour)
	if err := os.Chtimes(firstPath, old, old); err != nil {
		t.Fatalf("age active shard: %v", err)
	}
	if _, err := writer.Append([]byte("second\n")); err != nil {
		t.Fatalf("append second payload: %v", err)
	}
	if writer.path == firstPath {
		t.Fatal("expired active shard was not rotated")
	}
	if _, err := os.Stat(firstPath); !os.IsNotExist(err) {
		t.Fatalf("expired shard was not removed: %v", err)
	}
	if err := writer.Close(); err != nil {
		t.Fatalf("close writer: %v", err)
	}
}

func TestRotatingFileCleanupKeepsProtectedPaths(t *testing.T) {
	dir := t.TempDir()
	config := RotationConfig{
		Prefix:        "diagnostics",
		Extension:     ".jsonl",
		MaxBytes:      1024,
		MaxTotalBytes: 4096,
	}
	first := NewRotatingFile(dir, config)
	second := NewRotatingFile(dir, config)
	if _, err := first.Append([]byte("first\n")); err != nil {
		t.Fatalf("first append: %v", err)
	}
	firstPath := first.CurrentPath()
	if firstPath == "" {
		t.Fatal("first writer has no active path")
	}
	// The second writer protects the first writer's open shard while it rotates
	// and reclaims the shared directory.
	second.SetProtectedPaths(map[string]struct{}{firstPath: {}})
	for index := 0; index < 40; index++ {
		if _, err := second.Append([]byte(strings.Repeat("s", 300))); err != nil {
			t.Fatalf("second append: %v", err)
		}
	}
	if _, err := os.Stat(firstPath); err != nil {
		t.Fatalf("protected shard was removed: %v", err)
	}
}

// TestRotatingFileCleanupSkipsSymlinkedShards guards that cleanup only manages
// regular files: a symlink whose name matches the prefix must not be removed or
// followed during rotation.
func TestRotatingFileCleanupSkipsSymlinkedShards(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(t.TempDir(), "outside.jsonl")
	if err := os.WriteFile(target, []byte("outside\n"), 0o600); err != nil {
		t.Fatalf("write symlink target: %v", err)
	}
	linkName := filepath.Join(dir, "event-20260101T000000.000000000Z-000001.jsonl")
	if err := os.Symlink(target, linkName); err != nil {
		t.Fatalf("create symlinked shard: %v", err)
	}
	writer := NewRotatingFile(dir, RotationConfig{
		Prefix:        "event",
		Extension:     ".jsonl",
		MaxBytes:      16,
		MaxTotalBytes: 64,
	})
	for index := 0; index < 10; index++ {
		if _, err := writer.Append([]byte("payload-xxxxxxxx\n")); err != nil {
			t.Fatalf("append payload: %v", err)
		}
	}
	if err := writer.Close(); err != nil {
		t.Fatalf("close writer: %v", err)
	}
	if _, err := os.Lstat(linkName); err != nil {
		t.Fatalf("symlinked shard was removed: %v", err)
	}
	if _, err := os.Stat(target); err != nil {
		t.Fatalf("symlink target was removed: %v", err)
	}
}

func TestPayloadPackStoreAggregatesPayloads(t *testing.T) {
	root := t.TempDir()
	store := NewPayloadPackStore(root, RotationConfig{
		MaxBytes: 1 << 20,
		MaxFiles: 4,
	})
	firstRef, err := store.Put("provider_request", []byte(`{"model":"test"}`), map[string]string{"request_id": "req-1"})
	if err != nil {
		t.Fatalf("put first payload: %v", err)
	}
	secondRef, err := store.Put("provider_chunk", []byte("data: hello"), nil)
	if err != nil {
		t.Fatalf("put second payload: %v", err)
	}
	if err := store.Close(); err != nil {
		t.Fatalf("close store: %v", err)
	}
	if firstRef.Path != secondRef.Path {
		t.Fatalf("expected payloads in one pack, got %q and %q", firstRef.Path, secondRef.Path)
	}
	if !strings.HasPrefix(firstRef.Path, "payloads/pack-") {
		t.Fatalf("unexpected payload ref path %q", firstRef.Path)
	}
	payloadDir := filepath.Join(root, "payloads")
	entries, err := os.ReadDir(payloadDir)
	if err != nil {
		t.Fatalf("read payload directory: %v", err)
	}
	if len(entries) != 1 {
		t.Fatalf("expected one pack file, got %d", len(entries))
	}
	body, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(firstRef.Path)))
	if err != nil {
		t.Fatalf("read payload pack: %v", err)
	}
	lines := strings.Split(strings.TrimSpace(string(body)), "\n")
	if len(lines) != 2 {
		t.Fatalf("expected two payload records, got %d", len(lines))
	}
	var first map[string]any
	if err := json.Unmarshal([]byte(lines[0]), &first); err != nil {
		t.Fatalf("decode first payload record: %v", err)
	}
	if first["encoding"] != "base64" {
		t.Fatalf("expected base64 encoding, got %v", first["encoding"])
	}
}

func TestPayloadPackStoreSplitsOversizedPayload(t *testing.T) {
	root := t.TempDir()
	const maxBytes = int64(4 << 10)
	store := NewPayloadPackStore(root, RotationConfig{
		MaxBytes: maxBytes,
		MaxFiles: 16,
	})
	ref, err := store.Put("provider_request", []byte(strings.Repeat("x", 12<<10)), nil)
	if err != nil {
		t.Fatalf("put oversized payload: %v", err)
	}
	if err := store.Close(); err != nil {
		t.Fatalf("close oversized payload store: %v", err)
	}
	if len(ref.Chunks) < 2 {
		t.Fatalf("expected chunked payload ref, got %+v", ref)
	}
	for _, chunk := range ref.Chunks {
		info, err := os.Stat(filepath.Join(root, filepath.FromSlash(chunk.Path)))
		if err != nil {
			t.Fatalf("stat payload chunk: %v", err)
		}
		if info.Size() > maxBytes {
			t.Fatalf("payload pack exceeded max bytes: %d > %d", info.Size(), maxBytes)
		}
	}
}

func TestPayloadPackStoreSplitsEscapedOversizedPayload(t *testing.T) {
	root := t.TempDir()
	const maxBytes = int64(4 << 10)
	store := NewPayloadPackStore(root, RotationConfig{
		MaxBytes: maxBytes,
		MaxFiles: 32,
	})
	payload := []byte(`{"body":"` + strings.Repeat(`\\`, 12<<10) + `"}`)
	ref, err := store.Put("provider_request", payload, nil)
	if err != nil {
		t.Fatalf("put escaped oversized payload: %v", err)
	}
	if err := store.Close(); err != nil {
		t.Fatalf("close escaped oversized payload store: %v", err)
	}
	if len(ref.Chunks) < 2 {
		t.Fatalf("expected chunked escaped payload ref, got %+v", ref)
	}
	for _, chunk := range ref.Chunks {
		info, err := os.Stat(filepath.Join(root, filepath.FromSlash(chunk.Path)))
		if err != nil {
			t.Fatalf("stat escaped payload chunk: %v", err)
		}
		if info.Size() > maxBytes {
			t.Fatalf("escaped payload pack exceeded max bytes: %d > %d", info.Size(), maxBytes)
		}
	}
}

func TestRotatingFileReturnsWriteError(t *testing.T) {
	root := t.TempDir()
	blockedPath := filepath.Join(root, "blocked")
	if err := os.WriteFile(blockedPath, []byte("not a directory"), 0o644); err != nil {
		t.Fatalf("create blocking file: %v", err)
	}
	writer := NewRotatingFile(filepath.Join(blockedPath, "logs"), RotationConfig{})
	if _, err := writer.Append([]byte("payload\n")); err == nil {
		t.Fatal("expected write error")
	}
}

func TestCleanupDebugTreeRemovesOnlyExpiredManagedLogs(t *testing.T) {
	root := t.TempDir()
	oldEvent := filepath.Join(root, "provider", "event-old.jsonl")
	oldPack := filepath.Join(root, "payloads", "pack-old.jsonl")
	freshEvent := filepath.Join(root, "runtime", "event-fresh.jsonl")
	unmanaged := filepath.Join(root, "notes.jsonl")
	for _, path := range []string{oldEvent, oldPack, freshEvent, unmanaged} {
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatalf("create debug directory: %v", err)
		}
		if err := os.WriteFile(path, []byte("payload\n"), 0o644); err != nil {
			t.Fatalf("create debug artifact %s: %v", path, err)
		}
	}
	now := time.Date(2026, time.March, 14, 12, 0, 0, 0, time.UTC)
	oldTime := now.Add(-15 * 24 * time.Hour)
	for _, path := range []string{oldEvent, oldPack} {
		if err := os.Chtimes(path, oldTime, oldTime); err != nil {
			t.Fatalf("age debug artifact %s: %v", path, err)
		}
	}
	freshTime := now.Add(-time.Hour)
	if err := os.Chtimes(freshEvent, freshTime, freshTime); err != nil {
		t.Fatalf("set fresh debug artifact time: %v", err)
	}

	stats, err := CleanupDebugTree(root, DebugCleanupConfig{
		MaxAge: 14 * 24 * time.Hour,
		Now:    func() time.Time { return now },
	})
	if err != nil {
		t.Fatalf("cleanup debug tree: %v", err)
	}
	if stats.Removed != 2 {
		t.Fatalf("expected two expired debug logs removed, got %+v", stats)
	}
	for _, path := range []string{freshEvent, unmanaged} {
		if _, err := os.Stat(path); err != nil {
			t.Fatalf("expected artifact to remain %s: %v", path, err)
		}
	}
	for _, path := range []string{oldEvent, oldPack} {
		if _, err := os.Stat(path); !os.IsNotExist(err) {
			t.Fatalf("expected expired artifact removed %s, err=%v", path, err)
		}
	}
}

func TestCleanupPayloadDirectoryPreservesPackFiles(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "payloads")
	if err := os.MkdirAll(filepath.Join(dir, "legacy-shard"), 0o755); err != nil {
		t.Fatalf("create payload directory: %v", err)
	}
	for _, name := range []string{"payload-1.json", "payload-2.bin", "pack-20260101T000000Z-000001.jsonl"} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte("payload"), 0o644); err != nil {
			t.Fatalf("create payload file %s: %v", name, err)
		}
	}
	stats, err := CleanupPayloadDirectory(dir, true)
	if err != nil {
		t.Fatalf("cleanup payload directory: %v", err)
	}
	if stats.Removed != 3 || stats.Kept != 1 {
		t.Fatalf("unexpected cleanup stats: %+v", stats)
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("read cleaned payload directory: %v", err)
	}
	if len(entries) != 1 || !strings.HasPrefix(entries[0].Name(), "pack-") {
		t.Fatalf("expected only payload pack to remain, got %v", entries)
	}
}
