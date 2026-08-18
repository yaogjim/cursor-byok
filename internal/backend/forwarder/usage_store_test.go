package forwarder

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"
)

func TestUsageFileStoreResetClearsStatsAndKeepsHistory(t *testing.T) {
	historyRoot := t.TempDir()
	conversationPath := filepath.Join(historyRoot, "conversation-keep", "state.json")
	if err := os.MkdirAll(filepath.Dir(conversationPath), 0o755); err != nil {
		t.Fatalf("create conversation history: %v", err)
	}
	if err := os.WriteFile(conversationPath, []byte(`{"id":"conversation-keep"}`), 0o644); err != nil {
		t.Fatalf("write conversation history: %v", err)
	}

	store := NewUsageFileStore(historyRoot)
	oldAt := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
	if err := store.UpsertEvent(usageFileEvent{
		EventID:      "evt-old",
		Kind:         usageEventKindProvider,
		At:           oldAt,
		InputTokens:  100,
		OutputTokens: 20,
		UsagePresent: true,
	}); err != nil {
		t.Fatalf("seed usage event: %v", err)
	}
	if _, found, err := store.LookupEvent("evt-old"); err != nil || !found {
		t.Fatalf("seeded event missing: found=%v err=%v", found, err)
	}

	beforeReset := time.Now().UTC().Add(-time.Second)
	if err := store.Reset(); err != nil {
		t.Fatalf("Reset() error = %v", err)
	}

	usagePath := filepath.Join(historyRoot, usageFileName)
	if _, err := os.Stat(usagePath); err != nil {
		t.Fatalf("usage.json should still exist: %v", err)
	}
	if _, err := os.Stat(conversationPath); err != nil {
		t.Fatalf("conversation history should still exist: %v", err)
	}

	body, err := os.ReadFile(usagePath)
	if err != nil {
		t.Fatalf("read usage.json: %v", err)
	}
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(body, &raw); err != nil {
		t.Fatalf("decode usage.json: %v", err)
	}
	assertJSONNumber(t, raw["schema_version"], usageFileSchemaVersion)
	assertJSONEmptyArray(t, raw["daily"])
	assertJSONEmptyArray(t, raw["recent_events"])
	assertJSONEmptyObject(t, raw["event_index"])

	var totals usageFileTotals
	if err := json.Unmarshal(raw["totals"], &totals); err != nil {
		t.Fatalf("decode totals: %v", err)
	}
	if totals != (usageFileTotals{}) {
		t.Fatalf("totals after reset = %+v, want zeros", totals)
	}

	var updatedAt time.Time
	if err := json.Unmarshal(raw["updated_at"], &updatedAt); err != nil {
		t.Fatalf("decode updated_at: %v", err)
	}
	if updatedAt.Before(beforeReset) {
		t.Fatalf("updated_at %s was not refreshed", updatedAt)
	}

	if _, found, err := store.LookupEvent("evt-old"); err != nil || found {
		t.Fatalf("LookupEvent after reset found=%v err=%v, want empty", found, err)
	}

	if err := store.UpsertEvent(usageFileEvent{
		EventID:      "evt-old",
		Kind:         usageEventKindProvider,
		At:           time.Date(2026, 8, 17, 1, 2, 3, 0, time.UTC),
		InputTokens:  7,
		OutputTokens: 3,
		UsagePresent: true,
	}); err != nil {
		t.Fatalf("UpsertEvent after reset: %v", err)
	}
	doc, err := readUsageFileDocument(usagePath)
	if err != nil {
		t.Fatalf("read usage after upsert: %v", err)
	}
	if doc.Totals.InputTokens != 7 || doc.Totals.OutputTokens != 3 || doc.Totals.ProviderCalls != 1 || doc.Totals.TotalTokens != 10 {
		t.Fatalf("totals after post-reset upsert = %+v, want from-zero counts", doc.Totals)
	}
	if len(doc.Daily) != 1 || doc.Daily[0].Date != "2026-08-17" {
		t.Fatalf("daily after post-reset upsert = %+v", doc.Daily)
	}
}

func TestUsageFileStoreResetIsConcurrentSafe(t *testing.T) {
	historyRoot := t.TempDir()
	store := NewUsageFileStore(historyRoot)
	if err := store.UpsertEvent(usageFileEvent{
		EventID:     "evt-seed",
		InputTokens: 11,
	}); err != nil {
		t.Fatalf("seed usage event: %v", err)
	}

	const workers = 8
	var wg sync.WaitGroup
	errs := make(chan error, workers*2)
	for i := 0; i < workers; i++ {
		wg.Add(2)
		go func(index int) {
			defer wg.Done()
			if err := store.Reset(); err != nil {
				errs <- err
			}
		}(i)
		go func(index int) {
			defer wg.Done()
			if err := store.UpsertEvent(usageFileEvent{
				EventID:     "evt-concurrent",
				InputTokens: int64(index + 1),
			}); err != nil {
				errs <- err
			}
		}(i)
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Fatalf("concurrent usage store error: %v", err)
	}

	doc, err := readUsageFileDocument(filepath.Join(historyRoot, usageFileName))
	if err != nil {
		t.Fatalf("read usage after concurrent reset: %v", err)
	}
	if doc.SchemaVersion != usageFileSchemaVersion {
		t.Fatalf("schema_version = %d, want %d", doc.SchemaVersion, usageFileSchemaVersion)
	}
	if doc.Totals.InputTokens < 0 || doc.Totals.ProviderCalls < 0 {
		t.Fatalf("corrupted totals after concurrent reset: %+v", doc.Totals)
	}
	if doc.EventIndex == nil {
		t.Fatalf("event_index is nil after concurrent reset")
	}
}

func assertJSONNumber(t *testing.T, raw json.RawMessage, want int) {
	t.Helper()
	var got int
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatalf("decode number %s: %v", raw, err)
	}
	if got != want {
		t.Fatalf("number = %d, want %d", got, want)
	}
}

func assertJSONEmptyArray(t *testing.T, raw json.RawMessage) {
	t.Helper()
	var items []json.RawMessage
	if err := json.Unmarshal(raw, &items); err != nil {
		t.Fatalf("decode array %s: %v", raw, err)
	}
	if len(items) != 0 {
		t.Fatalf("array = %s, want []", raw)
	}
}

func assertJSONEmptyObject(t *testing.T, raw json.RawMessage) {
	t.Helper()
	var object map[string]json.RawMessage
	if err := json.Unmarshal(raw, &object); err != nil {
		t.Fatalf("decode object %s: %v", raw, err)
	}
	if len(object) != 0 {
		t.Fatalf("object = %s, want {}", raw)
	}
}
