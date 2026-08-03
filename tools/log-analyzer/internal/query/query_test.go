package query

import (
	"reflect"
	"strings"
	"testing"
	"time"
)

func TestCompileStructuredAndKeywordQuery(t *testing.T) {
	predicate, err := Compile(`severity:error capability:tool operation:tool.result "Patch Edit" duration:>=250`)
	if err != nil {
		t.Fatalf("Compile() error = %v", err)
	}
	where := predicate.WhereSQL()
	for _, fragment := range []string{"e.severity", "e.capability", "e.operation", "LIKE ?", "e.duration_ms >= ?"} {
		if !strings.Contains(where, fragment) {
			t.Fatalf("WhereSQL() missing %q: %s", fragment, where)
		}
	}
	for _, raw := range []string{`'error'`, "tool.result", "Patch Edit", "250"} {
		if strings.Contains(where, raw) {
			t.Fatalf("WhereSQL() contains unparameterized value %q: %s", raw, where)
		}
	}
	args := predicate.Args()
	if len(args) != 10 {
		t.Fatalf("Args() length = %d, want 10: %#v", len(args), args)
	}
	if args[0] != "error" || args[1] != "tool" || args[2] != "tool.result" || args[len(args)-1] != int64(250) {
		t.Fatalf("unexpected args: %#v", args)
	}
}

func TestCompileOrGroupAndNegation(t *testing.T) {
	predicate, err := Compile(`(outcome:failed OR outcome:timeout) -implementation:compat`)
	if err != nil {
		t.Fatalf("Compile() error = %v", err)
	}
	where := predicate.WhereSQL()
	if !strings.Contains(where, " OR ") || !strings.Contains(where, "NOT (") {
		t.Fatalf("unexpected predicate: %s", where)
	}
	wantArgs := []any{"failed", "timeout", "compat"}
	if !reflect.DeepEqual(predicate.Args(), wantArgs) {
		t.Fatalf("Args() = %#v, want %#v", predicate.Args(), wantArgs)
	}
}

func TestCompileRejectsUnknownFieldAndInjection(t *testing.T) {
	for _, input := range []string{
		`unknown_field:value`,
		`severity:error);DROP_TABLE_events;--`,
		`(severity:error`,
		`keyword:"unterminated`,
	} {
		if _, err := Compile(input); err == nil {
			t.Fatalf("Compile(%q) unexpectedly succeeded", input)
		}
	}
}

func TestCompileEscapesLikeWildcards(t *testing.T) {
	predicate, err := Compile(`keyword:100%_done`)
	if err != nil {
		t.Fatalf("Compile() error = %v", err)
	}
	for _, arg := range predicate.Args() {
		if arg != `%100\%\_done%` {
			t.Fatalf("LIKE arg = %q, want escaped pattern", arg)
		}
	}
}

func TestCompileDateRangeAndRecentDuration(t *testing.T) {
	dateRange, err := Compile(`time:2026-03-14..2026-03-14`)
	if err != nil {
		t.Fatalf("Compile(date range) error = %v", err)
	}
	args := dateRange.Args()
	if len(args) != 6 {
		t.Fatalf("date range args = %#v, want 6 boundaries", args)
	}
	start, err := time.Parse("2006-01-02", "2026-03-14")
	if err != nil {
		t.Fatal(err)
	}
	end := start.Add(24*time.Hour - time.Nanosecond)
	if args[0] != start.Unix() || args[3] != end.Unix() || args[5] != end.Nanosecond() {
		t.Fatalf("unexpected date range args: %#v", args)
	}

	now := time.Date(2026, 3, 14, 12, 0, 0, 0, time.UTC)
	recent, err := compileRecentTime("7d", now)
	if err != nil {
		t.Fatalf("compileRecentTime() error = %v", err)
	}
	wantStart := now.Add(-7 * 24 * time.Hour).Unix()
	if recent.Args()[0] != wantStart {
		t.Fatalf("recent start = %#v, want %d", recent.Args(), wantStart)
	}
}

func TestCompileTimeAndBooleanFilters(t *testing.T) {
	predicate, err := Compile(`time:2026-03-14T10:00:00Z..2026-03-14T11:00:00Z has_payload:true decode_error:false status_code:>=500`)
	if err != nil {
		t.Fatalf("Compile() error = %v", err)
	}
	where := predicate.WhereSQL()
	for _, fragment := range []string{"timestamp_seconds", "payload_ref", "decode_error = 0", "json_extract", ">= ?"} {
		if !strings.Contains(where, fragment) {
			t.Fatalf("WhereSQL() missing %q: %s", fragment, where)
		}
	}
}
