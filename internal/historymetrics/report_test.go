package historymetrics

import (
	"testing"
	"time"
)

func TestSelectDailyRangeFillsMissingDays(t *testing.T) {
	now := time.Date(2026, 8, 26, 15, 4, 0, 0, time.UTC)
	items := []DailySummary{
		{Date: "2026-08-24", ProviderCalls: 3, RequestTokens: 10},
		{Date: "2026-08-26", ProviderCalls: 5, RequestTokens: 20},
	}

	got := SelectDailyRange(items, "7d", now)
	if len(got) != 7 {
		t.Fatalf("7d len = %d, want 7", len(got))
	}
	if got[0].Date != "2026-08-20" || got[6].Date != "2026-08-26" {
		t.Fatalf("7d range = %s..%s", got[0].Date, got[6].Date)
	}
	if got[4].ProviderCalls != 3 || got[5].ProviderCalls != 0 || got[6].ProviderCalls != 5 {
		t.Fatalf("7d filled = %+v", got[4:])
	}

	all := SelectDailyRange(items, "all", now)
	if len(all) != 3 {
		t.Fatalf("all len = %d, want 3", len(all))
	}
	if all[0].Date != "2026-08-24" || all[1].Date != "2026-08-25" || all[2].Date != "2026-08-26" {
		t.Fatalf("all dates = %+v", all)
	}
	if all[1].ProviderCalls != 0 {
		t.Fatalf("gap day should be zero, got %+v", all[1])
	}
}

func TestSelectDailyRange24hReturnsEmpty(t *testing.T) {
	now := time.Date(2026, 8, 26, 15, 4, 0, 0, time.UTC)
	items := []DailySummary{{Date: "2026-08-26", ProviderCalls: 5}}
	got := SelectDailyRange(items, "24h", now)
	if len(got) != 0 {
		t.Fatalf("24h daily select = %+v, want empty", got)
	}
}

func TestNormalizeRange(t *testing.T) {
	if NormalizeRange("30D") != "30d" {
		t.Fatalf("30D = %q", NormalizeRange("30D"))
	}
	if NormalizeRange("24H") != "24h" {
		t.Fatalf("24H = %q", NormalizeRange("24H"))
	}
	if NormalizeRange("7d") != "7d" {
		t.Fatalf("7d = %q", NormalizeRange("7d"))
	}
	if NormalizeRange("custom") != "all" {
		t.Fatalf("custom = %q", NormalizeRange("custom"))
	}
}
