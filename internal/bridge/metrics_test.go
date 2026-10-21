package bridge

import (
	"testing"
	"time"

	"cursor/internal/historymetrics"
)

func TestHomeMetricsReportFromRangeMapsSnapshotFields(t *testing.T) {
	hitRate := 0.25
	built := historymetrics.RangeReport{
		Range:       "24h",
		Granularity: "hour",
		Timezone:    "UTC",
		Start:       "2026-08-25T16:00:00Z",
		End:         "2026-08-26T16:00:00Z",
		GeneratedAt: "2026-08-26T15:04:00Z",
		DataVersion: "3@2026-08-26T12:00:00Z",
		Summary: historymetrics.RangeAggregate{
			ProviderCallsTotal: 6,
			TurnsTotal:         2,
			ValidTurnsTotal:    1,
			InvalidTurnsTotal:  1,
			RequestTokensTotal: 36,
			PromptTokensTotal:  29,
			CacheReadTokens:    3,
			CacheWriteTokens:   1,
			CacheHitRate:       &hitRate,
		},
		Daily: []historymetrics.DailySummary{},
		Buckets: []historymetrics.Bucket{
			{Start: "2026-08-26T11:00:00Z", ProviderCalls: 6, RequestTokens: 36, PromptTokens: 29},
		},
	}

	got := homeMetricsReportFromRange(built)
	if got.Range != "24h" || got.Granularity != "hour" || got.Timezone != "UTC" {
		t.Fatalf("identity = %+v", got)
	}
	if got.Start != built.Start || got.End != built.End {
		t.Fatalf("bounds = %s..%s", got.Start, got.End)
	}
	if got.GeneratedAt != built.GeneratedAt {
		t.Fatalf("generatedAt = %s", got.GeneratedAt)
	}
	if got.DataVersion != built.DataVersion {
		t.Fatalf("dataVersion = %s", got.DataVersion)
	}
	if got.DataVersion == got.GeneratedAt {
		t.Fatal("dataVersion should identify the usage snapshot, not the report clock")
	}
	if got.Summary.ProviderCallsTotal != 6 || got.Summary.TurnsTotal != 2 || got.Summary.RequestTokensTotal != 36 {
		t.Fatalf("summary = %+v", got.Summary)
	}
	if got.Summary.CacheHitRate == nil || *got.Summary.CacheHitRate != hitRate {
		t.Fatalf("cacheHitRate = %v", got.Summary.CacheHitRate)
	}
	if len(got.Daily) != 0 {
		t.Fatalf("daily = %+v, want empty for 24h", got.Daily)
	}
	if len(got.Buckets) != 1 || got.Buckets[0].Start != "2026-08-26T11:00:00Z" || got.Buckets[0].ProviderCalls != 6 {
		t.Fatalf("buckets = %+v", got.Buckets)
	}
}

func TestHomeMetricsReportFromRangeKeepsDailyCompatibility(t *testing.T) {
	built := historymetrics.BuildRangeReport(historymetrics.Summary{
		SchemaVersion: 3,
		UpdatedAt:     time.Date(2026, 8, 26, 12, 0, 0, 0, time.UTC),
		Daily: []historymetrics.DailySummary{
			{Date: "2026-08-26", ProviderCalls: 4, RequestTokens: 20},
		},
	}, "7d", time.Date(2026, 8, 26, 15, 4, 0, 0, time.UTC))

	got := homeMetricsReportFromRange(built)
	if got.Range != "7d" || got.Granularity != "day" {
		t.Fatalf("7d identity = %s/%s", got.Range, got.Granularity)
	}
	if got.DataVersion != "3@2026-08-26T12:00:00Z" {
		t.Fatalf("dataVersion = %s", got.DataVersion)
	}
	if len(got.Daily) != 7 || got.Daily[6].Date != "2026-08-26" || got.Daily[6].ProviderCalls != 4 {
		t.Fatalf("daily = %+v", got.Daily)
	}
	if len(got.Buckets) != 7 || got.Buckets[6].Start != "2026-08-26T00:00:00Z" {
		t.Fatalf("buckets = %+v", got.Buckets)
	}
	if got.Summary.ProviderCallsTotal != 4 || got.Summary.RequestTokensTotal != 20 {
		t.Fatalf("summary = %+v", got.Summary)
	}
}
