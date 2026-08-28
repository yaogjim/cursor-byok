package historymetrics

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestLoadUsageSummaryKeepsProviderCallsSeparateFromTurns(t *testing.T) {
	path := filepath.Join(t.TempDir(), "usage.json")
	body := `{
  "totals": {
    "provider_calls": 9,
    "turns_total": 4,
    "valid_turns_total": 3,
    "invalid_turns_total": 1,
    "input_tokens": 10,
    "output_tokens": 5,
    "cache_read_tokens": 2,
    "cache_write_tokens": 1,
    "total_tokens": 18
  },
  "daily": [
    {
      "date": "2026-08-25",
      "provider_calls": 9,
      "turns_total": 4,
      "valid_turns_total": 3,
      "invalid_turns_total": 1,
      "input_tokens": 10,
      "output_tokens": 5,
      "cache_read_tokens": 2,
      "cache_write_tokens": 1,
      "total_tokens": 18
    }
  ]
}`
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}

	summary, err := LoadUsageSummary(path)
	if err != nil {
		t.Fatalf("LoadUsageSummary() error = %v", err)
	}
	if summary.ProviderCallsTotal != 9 {
		t.Fatalf("ProviderCallsTotal = %d, want 9", summary.ProviderCallsTotal)
	}
	if summary.TurnsTotal != 4 {
		t.Fatalf("TurnsTotal = %d, want 4", summary.TurnsTotal)
	}
	if summary.ValidTurnsTotal != 3 || summary.InvalidTurnsTotal != 1 {
		t.Fatalf("valid/invalid = %d/%d, want 3/1", summary.ValidTurnsTotal, summary.InvalidTurnsTotal)
	}
	if len(summary.Daily) != 1 {
		t.Fatalf("daily len = %d, want 1", len(summary.Daily))
	}
	if summary.Daily[0].ProviderCalls != 9 || summary.Daily[0].TurnsTotal != 4 {
		t.Fatalf("daily = %+v", summary.Daily[0])
	}
}

func TestLoadUsageSummaryOldSchemaLeavesHourlyEmpty(t *testing.T) {
	path := filepath.Join(t.TempDir(), "usage.json")
	body := `{
  "schema_version": 2,
  "updated_at": "2026-08-26T12:00:00Z",
  "totals": {
    "provider_calls": 2,
    "input_tokens": 30,
    "output_tokens": 4,
    "total_tokens": 34
  },
  "daily": [
    { "date": "2026-08-26", "provider_calls": 2, "input_tokens": 30, "output_tokens": 4, "total_tokens": 34 }
  ],
  "recent_events": [
    {
      "event_id": "evt-recent",
      "kind": "provider_call",
      "at": "2026-08-26T11:15:00Z",
      "input_tokens": 30,
      "output_tokens": 4,
      "total_tokens": 34
    }
  ]
}`
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}
	summary, err := LoadUsageSummary(path)
	if err != nil {
		t.Fatalf("LoadUsageSummary() error = %v", err)
	}
	if summary.SchemaVersion != 2 {
		t.Fatalf("SchemaVersion = %d, want 2", summary.SchemaVersion)
	}
	if len(summary.Hourly) != 0 {
		t.Fatalf("Hourly = %+v, want empty (no inference from recent_events)", summary.Hourly)
	}
	if len(summary.Daily) != 1 || summary.Daily[0].Date != "2026-08-26" {
		t.Fatalf("Daily = %+v", summary.Daily)
	}
	now := time.Date(2026, 8, 26, 12, 0, 0, 0, time.UTC)
	report := BuildRangeReport(summary, "24h", now)
	if report.Granularity != "hour" || len(report.Buckets) != 24 {
		t.Fatalf("24h report buckets = %d granularity=%s", len(report.Buckets), report.Granularity)
	}
	for _, bucket := range report.Buckets {
		if bucket.ProviderCalls != 0 || bucket.RequestTokens != 0 {
			t.Fatalf("24h bucket inferred from daily/recent_events: %+v", bucket)
		}
	}
	if len(report.Daily) != 0 {
		t.Fatalf("24h daily = %+v, want empty", report.Daily)
	}
}

func TestLoadUsageSummaryV3HourlyRoundTrip(t *testing.T) {
	path := filepath.Join(t.TempDir(), "usage.json")
	body := `{
  "schema_version": 3,
  "updated_at": "2026-08-26T12:34:56Z",
  "totals": {
    "provider_calls": 6,
    "turns_total": 2,
    "valid_turns_total": 1,
    "invalid_turns_total": 1,
    "input_tokens": 25,
    "output_tokens": 7,
    "cache_read_tokens": 3,
    "cache_write_tokens": 1,
    "total_tokens": 36
  },
  "daily": [
    { "date": "2026-08-26", "provider_calls": 6, "input_tokens": 25, "output_tokens": 7, "total_tokens": 36 }
  ],
  "hourly": [
    {
      "hour": "2026-08-26T11:00:00Z",
      "provider_calls": 2,
      "turns_total": 1,
      "valid_turns_total": 1,
      "input_tokens": 10,
      "output_tokens": 4,
      "cache_read_tokens": 2,
      "cache_write_tokens": 1,
      "total_tokens": 17
    },
    {
      "hour": "2026-08-26T11:00:00+00:00",
      "provider_calls": 1,
      "input_tokens": 5,
      "output_tokens": 1,
      "total_tokens": 6
    },
    {
      "hour": "2026-08-26T19:00:00+08:00",
      "provider_calls": 3,
      "turns_total": 1,
      "invalid_turns_total": 1,
      "input_tokens": 10,
      "output_tokens": 2,
      "cache_read_tokens": 1,
      "total_tokens": 13
    }
  ]
}`
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}
	summary, err := LoadUsageSummary(path)
	if err != nil {
		t.Fatalf("LoadUsageSummary() error = %v", err)
	}
	if summary.SchemaVersion != 3 {
		t.Fatalf("SchemaVersion = %d, want 3", summary.SchemaVersion)
	}
	if !summary.UpdatedAt.Equal(time.Date(2026, 8, 26, 12, 34, 56, 0, time.UTC)) {
		t.Fatalf("UpdatedAt = %s", summary.UpdatedAt)
	}
	if len(summary.Hourly) != 1 {
		t.Fatalf("Hourly = %+v, want merged single UTC hour", summary.Hourly)
	}
	hour := summary.Hourly[0]
	if hour.Hour != "2026-08-26T11:00:00Z" {
		t.Fatalf("hour = %s", hour.Hour)
	}
	if hour.ProviderCalls != 6 || hour.TurnsTotal != 2 || hour.ValidTurnsTotal != 1 || hour.InvalidTurnsTotal != 1 {
		t.Fatalf("hourly counts = %+v", hour)
	}
	if hour.InputTokens != 25 || hour.OutputTokens != 7 || hour.CacheReadTokens != 3 || hour.CacheWriteTokens != 1 {
		t.Fatalf("hourly tokens = %+v", hour)
	}
	if hour.RequestTokens != 36 || hour.PromptTokens != 29 {
		t.Fatalf("hourly request/prompt = %d/%d", hour.RequestTokens, hour.PromptTokens)
	}
	if len(summary.Daily) != 1 || summary.Daily[0].Date != "2026-08-26" || summary.Daily[0].ProviderCalls != 6 {
		t.Fatalf("Daily = %+v", summary.Daily)
	}

	now := time.Date(2026, 8, 26, 12, 0, 0, 0, time.UTC)
	report := BuildRangeReport(summary, "24h", now)
	if report.DataVersion != "3@2026-08-26T12:34:56Z" {
		t.Fatalf("dataVersion = %s", report.DataVersion)
	}
	if report.GeneratedAt != "2026-08-26T12:00:00Z" {
		t.Fatalf("generatedAt = %s", report.GeneratedAt)
	}
	var matched *Bucket
	for index := range report.Buckets {
		if report.Buckets[index].Start == "2026-08-26T11:00:00Z" {
			matched = &report.Buckets[index]
			break
		}
	}
	if matched == nil {
		t.Fatalf("missing 11:00 bucket in %+v", report.Buckets)
	}
	if matched.ProviderCalls != 6 || matched.RequestTokens != 36 || matched.PromptTokens != 29 {
		t.Fatalf("24h bucket = %+v", matched)
	}
}
