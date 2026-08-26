package historymetrics

import (
	"os"
	"path/filepath"
	"testing"
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
