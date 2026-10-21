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
	assertJSONEmptyArray(t, raw["hourly"])
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

	afterResetAt := time.Now().UTC()
	if err := store.UpsertEvent(usageFileEvent{
		EventID:      "evt-old",
		Kind:         usageEventKindProvider,
		At:           afterResetAt,
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
	wantDate := afterResetAt.Format("2006-01-02")
	wantHour := afterResetAt.Truncate(time.Hour).Format(time.RFC3339)
	if len(doc.Daily) != 1 || doc.Daily[0].Date != wantDate {
		t.Fatalf("daily after post-reset upsert = %+v", doc.Daily)
	}
	if len(doc.Hourly) != 1 || doc.Hourly[0].Hour != wantHour {
		t.Fatalf("hourly after post-reset upsert = %+v", doc.Hourly)
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

func TestUsageFileStoreMigratesOldSchemaWithoutInferringHourly(t *testing.T) {
	historyRoot := t.TempDir()
	usagePath := filepath.Join(historyRoot, usageFileName)
	oldBody := `{
  "schema_version": 2,
  "updated_at": "2026-08-25T12:00:00Z",
  "totals": {
    "provider_calls": 2,
    "turns_total": 0,
    "valid_turns_total": 0,
    "invalid_turns_total": 0,
    "input_tokens": 30,
    "output_tokens": 4,
    "cache_read_tokens": 0,
    "cache_write_tokens": 0,
    "total_tokens": 34
  },
  "daily": [
    {
      "date": "2026-08-25",
      "provider_calls": 2,
      "input_tokens": 30,
      "output_tokens": 4,
      "total_tokens": 34
    }
  ],
  "recent_events": [
    {
      "event_id": "evt-old-a",
      "kind": "provider_call",
      "at": "2026-08-25T10:15:00Z",
      "input_tokens": 10,
      "output_tokens": 1,
      "total_tokens": 11,
      "usage_present": true
    },
    {
      "event_id": "evt-old-b",
      "kind": "provider_call",
      "at": "2026-08-25T11:45:00Z",
      "input_tokens": 20,
      "output_tokens": 3,
      "total_tokens": 23,
      "usage_present": true
    }
  ]
}`
	if err := os.WriteFile(usagePath, []byte(oldBody), 0o644); err != nil {
		t.Fatalf("write old usage.json: %v", err)
	}

	before, err := readUsageFileDocument(usagePath)
	if err != nil {
		t.Fatalf("read old schema: %v", err)
	}
	if before.SchemaVersion != 2 {
		t.Fatalf("schema_version before write = %d, want 2", before.SchemaVersion)
	}
	if len(before.Hourly) != 0 {
		t.Fatalf("hourly before write = %+v, want empty (no backfill from recent_events)", before.Hourly)
	}

	store := NewUsageFileStore(historyRoot)
	now := time.Now().UTC()
	if err := store.UpsertEvent(usageFileEvent{
		EventID:      "evt-new",
		Kind:         usageEventKindProvider,
		At:           now,
		InputTokens:  7,
		OutputTokens: 2,
		UsagePresent: true,
	}); err != nil {
		t.Fatalf("UpsertEvent: %v", err)
	}

	doc, err := readUsageFileDocument(usagePath)
	if err != nil {
		t.Fatalf("read migrated usage: %v", err)
	}
	if doc.SchemaVersion != usageFileSchemaVersion {
		t.Fatalf("schema_version after write = %d, want %d", doc.SchemaVersion, usageFileSchemaVersion)
	}
	if doc.Totals.ProviderCalls != 3 || doc.Totals.InputTokens != 37 {
		t.Fatalf("totals after migrate upsert = %+v, want preserved old + new", doc.Totals)
	}
	daily := dailyByDate(doc.Daily)
	oldDay := daily["2026-08-25"]
	if oldDay.InputTokens < 30 {
		t.Fatalf("old daily not preserved: %+v", doc.Daily)
	}
	today := now.Format("2006-01-02")
	if today == "2026-08-25" {
		if oldDay.InputTokens != 37 {
			t.Fatalf("same-day migrate daily = %+v, want 37", oldDay)
		}
	} else if daily[today].InputTokens != 7 {
		t.Fatalf("new daily = %+v", daily[today])
	}
	hourly := hourlyByHour(doc.Hourly)
	if len(hourly) != 1 {
		t.Fatalf("hourly after migrate = %+v, want only the new event hour", doc.Hourly)
	}
	wantHour := now.Truncate(time.Hour).Format(time.RFC3339)
	got, ok := hourly[wantHour]
	if !ok || got.InputTokens != 7 || got.ProviderCalls != 1 {
		t.Fatalf("new hourly bucket = %+v", got)
	}
	if _, found := hourly["2026-08-25T10:00:00Z"]; found {
		t.Fatalf("hourly inferred evt-old-a from recent_events: %+v", doc.Hourly)
	}
	if _, found := hourly["2026-08-25T11:00:00Z"]; found {
		t.Fatalf("hourly inferred evt-old-b from recent_events: %+v", doc.Hourly)
	}
}

func TestUsageFileStoreHourlyUTCBoundaries(t *testing.T) {
	store := NewUsageFileStore(t.TempDir())
	now := time.Now().UTC()
	midnight := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, time.UTC)
	cst := time.FixedZone("CST", 8*60*60)
	offsetLocal := time.Date(midnight.Year(), midnight.Month(), midnight.Day(), 0, 30, 0, 0, cst)
	cases := []struct {
		name     string
		at       time.Time
		wantDate string
		wantHour string
		eventID  string
		tokens   int64
	}{
		{
			name:     "utc midnight belongs to new day and hour",
			at:       midnight,
			wantDate: midnight.Format("2006-01-02"),
			wantHour: midnight.Format(time.RFC3339),
			eventID:  "evt-midnight",
			tokens:   3,
		},
		{
			name:     "one second before utc midnight stays on previous day/hour",
			at:       midnight.Add(-time.Second),
			wantDate: midnight.Add(-time.Second).Format("2006-01-02"),
			wantHour: midnight.Add(-time.Hour).Format(time.RFC3339),
			eventID:  "evt-before-midnight",
			tokens:   5,
		},
		{
			name:     "offset timestamp is stored in utc",
			at:       offsetLocal,
			wantDate: offsetLocal.UTC().Format("2006-01-02"),
			wantHour: offsetLocal.UTC().Truncate(time.Hour).Format(time.RFC3339),
			eventID:  "evt-offset",
			tokens:   8,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if err := store.UpsertEvent(usageFileEvent{
				EventID:     tc.eventID,
				Kind:        usageEventKindProvider,
				At:          tc.at,
				InputTokens: tc.tokens,
			}); err != nil {
				t.Fatalf("UpsertEvent: %v", err)
			}
		})
	}

	doc, err := readUsageFileDocument(store.path)
	if err != nil {
		t.Fatalf("read usage: %v", err)
	}
	daily := dailyByDate(doc.Daily)
	hourly := hourlyByHour(doc.Hourly)
	for _, tc := range cases {
		day, ok := daily[tc.wantDate]
		if !ok {
			t.Fatalf("%s: missing daily %s in %+v", tc.name, tc.wantDate, doc.Daily)
		}
		hour, ok := hourly[tc.wantHour]
		if !ok {
			t.Fatalf("%s: missing hourly %s in %+v", tc.name, tc.wantHour, doc.Hourly)
		}
		if hour.InputTokens < tc.tokens {
			t.Fatalf("%s: hourly tokens = %d, want at least %d", tc.name, hour.InputTokens, tc.tokens)
		}
		if day.InputTokens < tc.tokens {
			t.Fatalf("%s: daily tokens = %d, want at least %d", tc.name, day.InputTokens, tc.tokens)
		}
	}
	previousDay := midnight.Add(-time.Second).Format("2006-01-02")
	today := midnight.Format("2006-01-02")
	if daily[previousDay].InputTokens != 13 {
		t.Fatalf("%s tokens = %d, want 13", previousDay, daily[previousDay].InputTokens)
	}
	if daily[today].InputTokens != 3 {
		t.Fatalf("%s tokens = %d, want 3", today, daily[today].InputTokens)
	}
}

func TestUsageFileStoreDuplicateEventUpsertAdjustsBuckets(t *testing.T) {
	store := NewUsageFileStore(t.TempDir())
	now := time.Now().UTC()
	day := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, time.UTC)
	firstAt := day.Add(10*time.Hour + 15*time.Minute)
	if err := store.UpsertEvent(usageFileEvent{
		EventID:      "evt-dup",
		Kind:         usageEventKindProvider,
		At:           firstAt,
		InputTokens:  100,
		OutputTokens: 20,
	}); err != nil {
		t.Fatalf("first upsert: %v", err)
	}

	secondAt := day.Add(11*time.Hour + 45*time.Minute)
	if err := store.UpsertEvent(usageFileEvent{
		EventID:      "evt-dup",
		Kind:         usageEventKindProvider,
		At:           secondAt,
		InputTokens:  40,
		OutputTokens: 6,
	}); err != nil {
		t.Fatalf("duplicate upsert: %v", err)
	}

	doc, err := readUsageFileDocument(store.path)
	if err != nil {
		t.Fatalf("read usage: %v", err)
	}
	if doc.Totals.ProviderCalls != 1 || doc.Totals.InputTokens != 40 || doc.Totals.OutputTokens != 6 || doc.Totals.TotalTokens != 46 {
		t.Fatalf("totals after duplicate upsert = %+v, want replaced counts", doc.Totals)
	}
	hourly := hourlyByHour(doc.Hourly)
	oldHour := firstAt.Truncate(time.Hour).Format(time.RFC3339)
	newHour := secondAt.Truncate(time.Hour).Format(time.RFC3339)
	if got := hourly[oldHour]; got.ProviderCalls != 0 || got.InputTokens != 0 {
		t.Fatalf("old hour after move = %+v, want zeros", got)
	}
	got := hourly[newHour]
	if got.ProviderCalls != 1 || got.InputTokens != 40 || got.OutputTokens != 6 {
		t.Fatalf("new hour after move = %+v", got)
	}
	wantDate := secondAt.Format("2006-01-02")
	if len(doc.Daily) != 1 || doc.Daily[0].Date != wantDate || doc.Daily[0].InputTokens != 40 || doc.Daily[0].ProviderCalls != 1 {
		t.Fatalf("daily after duplicate upsert = %+v", doc.Daily)
	}
}

func TestUsageFileStoreHourlyRetentionDropsOldBuckets(t *testing.T) {
	store := NewUsageFileStore(t.TempDir())
	now := time.Now().UTC()
	oldAt := now.Add(-72 * time.Hour)
	recentAt := now.Add(-time.Hour)
	if err := store.UpsertEvent(usageFileEvent{
		EventID:     "evt-old-hour",
		At:          oldAt,
		InputTokens: 9,
	}); err != nil {
		t.Fatalf("old upsert: %v", err)
	}
	if err := store.UpsertEvent(usageFileEvent{
		EventID:     "evt-recent-hour",
		At:          recentAt,
		InputTokens: 4,
	}); err != nil {
		t.Fatalf("recent upsert: %v", err)
	}

	doc, err := readUsageFileDocument(store.path)
	if err != nil {
		t.Fatalf("read usage: %v", err)
	}
	if doc.Totals.InputTokens != 13 || doc.Totals.ProviderCalls != 2 {
		t.Fatalf("totals = %+v, want both events in totals", doc.Totals)
	}
	if len(dailyByDate(doc.Daily)) < 1 {
		t.Fatalf("daily missing, got %+v", doc.Daily)
	}
	hourly := hourlyByHour(doc.Hourly)
	oldHour := oldAt.Truncate(time.Hour).Format(time.RFC3339)
	recentHour := recentAt.Truncate(time.Hour).Format(time.RFC3339)
	if _, found := hourly[oldHour]; found {
		t.Fatalf("hourly retained %s beyond 48h window: %+v", oldHour, doc.Hourly)
	}
	got, ok := hourly[recentHour]
	if !ok || got.InputTokens != 4 {
		t.Fatalf("recent hourly = %+v found=%v", got, ok)
	}
}

func TestPruneUsageHourlyRetentionBoundary(t *testing.T) {
	now := time.Date(2026, 8, 26, 15, 4, 5, 0, time.UTC)
	cutoff := time.Date(2026, 8, 24, 15, 0, 0, 0, time.UTC)
	if got := hourlyRetentionCutoff(now); !got.Equal(cutoff) {
		t.Fatalf("cutoff = %s, want %s", got.Format(time.RFC3339), cutoff.Format(time.RFC3339))
	}

	keptHour := cutoff.Format(time.RFC3339)
	droppedHour := cutoff.Add(-time.Hour).Format(time.RFC3339)
	newerHour := cutoff.Add(time.Hour).Format(time.RFC3339)
	doc := usageFileDocument{
		Hourly: []usageFileHourly{
			{Hour: droppedHour, ProviderCalls: 1, InputTokens: 9, TotalTokens: 9},
			{Hour: keptHour, ProviderCalls: 2, InputTokens: 4, TotalTokens: 4},
			{Hour: newerHour, ProviderCalls: 3, InputTokens: 6, TotalTokens: 6},
		},
	}
	pruneUsageHourly(&doc, now)
	hourly := hourlyByHour(doc.Hourly)
	if _, found := hourly[droppedHour]; found {
		t.Fatalf("kept hour before cutoff: %+v", doc.Hourly)
	}
	if got := hourly[keptHour]; got.InputTokens != 4 || got.ProviderCalls != 2 {
		t.Fatalf("cutoff hour = %+v, want kept", got)
	}
	if got := hourly[newerHour]; got.InputTokens != 6 || got.ProviderCalls != 3 {
		t.Fatalf("newer hour = %+v, want kept", got)
	}
	if len(doc.Hourly) != 2 {
		t.Fatalf("hourly after prune = %+v, want cutoff and newer only", doc.Hourly)
	}
}

func TestPruneUsageHourlyMergesEquivalentKeys(t *testing.T) {
	now := time.Date(2026, 8, 26, 15, 4, 0, 0, time.UTC)
	doc := usageFileDocument{
		Hourly: []usageFileHourly{
			{Hour: "2026-08-26T15:00:00Z", ProviderCalls: 1, InputTokens: 10, OutputTokens: 1, TotalTokens: 11},
			{Hour: "2026-08-26T15:00:00+00:00", ProviderCalls: 2, InputTokens: 5, OutputTokens: 2, TotalTokens: 7},
			{Hour: "2026-08-26T23:00:00+08:00", ProviderCalls: 3, InputTokens: 7, CacheReadTokens: 1, TotalTokens: 8},
			{Hour: "2026-08-26T15:00:00.000Z", ProviderCalls: 1, InputTokens: 3, TotalTokens: 3},
			{Hour: "not-a-hour", ProviderCalls: 9, InputTokens: 99, TotalTokens: 99},
		},
	}
	pruneUsageHourly(&doc, now)
	if len(doc.Hourly) != 1 {
		t.Fatalf("hourly = %+v, want one merged UTC hour", doc.Hourly)
	}
	got := doc.Hourly[0]
	if got.Hour != "2026-08-26T15:00:00Z" {
		t.Fatalf("merged hour = %s", got.Hour)
	}
	if got.ProviderCalls != 7 || got.InputTokens != 25 || got.OutputTokens != 3 || got.CacheReadTokens != 1 || got.TotalTokens != 29 {
		t.Fatalf("merged counts = %+v, want summed equivalent keys", got)
	}
}

func dailyByDate(items []usageFileDaily) map[string]usageFileDaily {
	result := make(map[string]usageFileDaily, len(items))
	for _, item := range items {
		result[item.Date] = item
	}
	return result
}

func hourlyByHour(items []usageFileHourly) map[string]usageFileHourly {
	result := make(map[string]usageFileHourly, len(items))
	for _, item := range items {
		result[item.Hour] = item
	}
	return result
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
