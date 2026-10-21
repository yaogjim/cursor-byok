package historymetrics

import (
	"testing"
	"time"
)

func TestBuildRangeReportNormalizesRangeAndGranularity(t *testing.T) {
	now := time.Date(2026, 8, 26, 15, 4, 0, 0, time.UTC)
	summary := Summary{SchemaVersion: 3, UpdatedAt: now}

	hourly := BuildRangeReport(summary, "24H", now)
	if hourly.Range != "24h" || hourly.Granularity != "hour" {
		t.Fatalf("24h range/granularity = %s/%s", hourly.Range, hourly.Granularity)
	}
	if hourly.Timezone != "UTC" {
		t.Fatalf("timezone = %s", hourly.Timezone)
	}
	if hourly.GeneratedAt != "2026-08-26T15:04:00Z" {
		t.Fatalf("generatedAt = %s", hourly.GeneratedAt)
	}
	if hourly.DataVersion != "3@2026-08-26T15:04:00Z" {
		t.Fatalf("dataVersion = %s", hourly.DataVersion)
	}
	if hourly.Start != "2026-08-25T16:00:00Z" || hourly.End != "2026-08-26T16:00:00Z" {
		t.Fatalf("24h bounds = %s..%s", hourly.Start, hourly.End)
	}

	daily := BuildRangeReport(summary, "30d", now)
	if daily.Range != "30d" || daily.Granularity != "day" {
		t.Fatalf("30d range/granularity = %s/%s", daily.Range, daily.Granularity)
	}

	compat := BuildRangeReport(summary, "7d", now)
	if compat.Range != "7d" || compat.Granularity != "day" || len(compat.Daily) != 7 {
		t.Fatalf("7d compat = range=%s granularity=%s daily=%d", compat.Range, compat.Granularity, len(compat.Daily))
	}

	all := BuildRangeReport(summary, "weird", now)
	if all.Range != "all" || all.Granularity != "day" {
		t.Fatalf("unknown range = %s/%s", all.Range, all.Granularity)
	}
}

func TestBuildRangeReport24hFillsMissingHours(t *testing.T) {
	now := time.Date(2026, 8, 26, 15, 4, 0, 0, time.UTC)
	summary := Summary{
		SchemaVersion: 3,
		UpdatedAt:     now,
		Hourly: []HourlySummary{
			{Hour: "2026-08-26T13:00:00Z", ProviderCalls: 2, InputTokens: 10, RequestTokens: 12, PromptTokens: 10},
			{Hour: "2026-08-26T15:00:00Z", ProviderCalls: 5, InputTokens: 20, RequestTokens: 25, PromptTokens: 20},
		},
		Daily: []DailySummary{
			{Date: "2026-08-26", ProviderCalls: 99, RequestTokens: 999},
		},
	}

	report := BuildRangeReport(summary, "24h", now)
	if len(report.Buckets) != 24 {
		t.Fatalf("bucket len = %d, want 24", len(report.Buckets))
	}
	if report.Buckets[0].Start != "2026-08-25T16:00:00Z" || report.Buckets[23].Start != "2026-08-26T15:00:00Z" {
		t.Fatalf("bucket window = %s..%s", report.Buckets[0].Start, report.Buckets[23].Start)
	}
	if report.Buckets[21].ProviderCalls != 2 || report.Buckets[22].ProviderCalls != 0 || report.Buckets[23].ProviderCalls != 5 {
		t.Fatalf("filled hours = %+v", report.Buckets[21:])
	}
	if report.Summary.ProviderCallsTotal != 7 || report.Summary.RequestTokensTotal != 37 {
		t.Fatalf("summary = %+v, want calls=7 tokens=37 from hourly only", report.Summary)
	}
	if len(report.Daily) != 0 {
		t.Fatalf("24h daily should stay empty, got %+v", report.Daily)
	}
}

func TestBuildRangeReport30dAndAllUseDailyBuckets(t *testing.T) {
	now := time.Date(2026, 8, 26, 15, 4, 0, 0, time.UTC)
	summary := Summary{
		SchemaVersion: 3,
		UpdatedAt:     now,
		Daily: []DailySummary{
			{Date: "2026-08-24", ProviderCalls: 3, RequestTokens: 10, InputTokens: 8},
			{Date: "2026-08-26", ProviderCalls: 5, RequestTokens: 20, InputTokens: 15},
		},
		Hourly: []HourlySummary{
			{Hour: "2026-08-26T15:00:00Z", ProviderCalls: 50, RequestTokens: 500},
		},
	}

	report30 := BuildRangeReport(summary, "30d", now)
	if report30.Granularity != "day" || len(report30.Daily) != 30 || len(report30.Buckets) != 30 {
		t.Fatalf("30d lens daily=%d buckets=%d", len(report30.Daily), len(report30.Buckets))
	}
	if report30.Start != "2026-07-28T00:00:00Z" || report30.End != "2026-08-27T00:00:00Z" {
		t.Fatalf("30d bounds = %s..%s", report30.Start, report30.End)
	}
	if report30.Daily[27].Date != "2026-08-24" || report30.Daily[28].ProviderCalls != 0 || report30.Daily[29].ProviderCalls != 5 {
		t.Fatalf("30d filled = %+v", report30.Daily[26:])
	}
	if report30.Buckets[27].Start != "2026-08-24T00:00:00Z" {
		t.Fatalf("30d bucket start = %s", report30.Buckets[27].Start)
	}
	if report30.Summary.ProviderCallsTotal != 8 {
		t.Fatalf("30d summary used hourly data: %+v", report30.Summary)
	}

	all := BuildRangeReport(summary, "all", now)
	if len(all.Daily) != 3 || all.Daily[1].Date != "2026-08-25" || all.Daily[1].ProviderCalls != 0 {
		t.Fatalf("all daily = %+v", all.Daily)
	}
	if all.Start != "2026-08-24T00:00:00Z" || all.End != "2026-08-27T00:00:00Z" {
		t.Fatalf("all bounds = %s..%s", all.Start, all.End)
	}
}

func TestBuildRangeReportUTCHourBoundary(t *testing.T) {
	now := time.Date(2026, 8, 26, 0, 10, 0, 0, time.UTC)
	summary := Summary{
		Hourly: []HourlySummary{
			{Hour: "2026-08-25T23:00:00Z", ProviderCalls: 1, RequestTokens: 4},
			{Hour: "2026-08-26T00:00:00Z", ProviderCalls: 2, RequestTokens: 6},
		},
	}
	report := BuildRangeReport(summary, "24h", now)
	if report.Start != "2026-08-25T01:00:00Z" || report.End != "2026-08-26T01:00:00Z" {
		t.Fatalf("bounds = %s..%s", report.Start, report.End)
	}
	if report.Buckets[0].Start != "2026-08-25T01:00:00Z" {
		t.Fatalf("first bucket = %s", report.Buckets[0].Start)
	}
	last := report.Buckets[len(report.Buckets)-1]
	prev := report.Buckets[len(report.Buckets)-2]
	if prev.Start != "2026-08-25T23:00:00Z" || prev.ProviderCalls != 1 {
		t.Fatalf("pre-midnight bucket = %+v", prev)
	}
	if last.Start != "2026-08-26T00:00:00Z" || last.ProviderCalls != 2 {
		t.Fatalf("midnight bucket = %+v", last)
	}
}

func TestBuildRangeReportDataVersionUsesSnapshotNotClock(t *testing.T) {
	now := time.Date(2026, 8, 26, 15, 4, 0, 0, time.UTC)
	updatedAt := time.Date(2026, 8, 26, 12, 0, 0, 0, time.UTC)
	summary := Summary{
		SchemaVersion: 3,
		UpdatedAt:     updatedAt,
		Hourly: []HourlySummary{
			{Hour: "2026-08-26T15:00:00Z", ProviderCalls: 4, RequestTokens: 10, PromptTokens: 8},
		},
	}
	report := BuildRangeReport(summary, "24h", now)
	if report.GeneratedAt != "2026-08-26T15:04:00Z" {
		t.Fatalf("generatedAt = %s", report.GeneratedAt)
	}
	if report.DataVersion != "3@2026-08-26T12:00:00Z" {
		t.Fatalf("dataVersion = %s, want snapshot updated_at", report.DataVersion)
	}
	if report.Summary.ProviderCallsTotal != 4 || report.Summary.RequestTokensTotal != 10 {
		t.Fatalf("summary = %+v, want same snapshot hourly aggregate", report.Summary)
	}
}

func TestSelectHourlyRangeMergesEquivalentHourKeys(t *testing.T) {
	now := time.Date(2026, 8, 26, 15, 4, 0, 0, time.UTC)
	got := SelectHourlyRange([]HourlySummary{
		{Hour: "2026-08-26T15:00:00Z", ProviderCalls: 1, InputTokens: 4, RequestTokens: 5},
		{Hour: "2026-08-26T15:00:00+00:00", ProviderCalls: 2, InputTokens: 6, RequestTokens: 7},
		{Hour: "2026-08-26T23:00:00+08:00", ProviderCalls: 3, InputTokens: 1, RequestTokens: 2},
		{Hour: "not-a-hour", ProviderCalls: 9, RequestTokens: 99},
	}, now)
	if len(got) != 24 {
		t.Fatalf("len = %d", len(got))
	}
	last := got[len(got)-1]
	if last.Hour != "2026-08-26T15:00:00Z" {
		t.Fatalf("last hour = %s", last.Hour)
	}
	if last.ProviderCalls != 6 || last.InputTokens != 11 || last.RequestTokens != 14 {
		t.Fatalf("merged last hour = %+v, want summed equivalent keys", last)
	}
}
