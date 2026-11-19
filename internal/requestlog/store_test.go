package requestlog

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

func fixedNow() time.Time {
	return time.Date(2026, 11, 17, 12, 0, 0, 0, time.UTC)
}

func testStore(t *testing.T) *Store {
	t.Helper()
	store := NewStore(t.TempDir(), 30*24*time.Hour)
	store.now = fixedNow
	return store
}

func sampleRecord(at time.Time, model string) Record {
	return Record{
		RequestID:        "req-1",
		ConversationID:   "conv-1",
		ModelCallID:      "call-1",
		RequestModel:     "group/smartdk",
		Provider:         "openai",
		Model:            model,
		InputTokens:      500,
		OutputTokens:     20,
		CacheReadTokens:  490,
		CacheWriteTokens: 0,
		TTFTMS:           765,
		DurationMS:       1700,
		StatusCode:       200,
		Status:           "completed",
		At:               at,
	}
}

func appendRecord(t *testing.T, store *Store, record Record) {
	t.Helper()
	if err := store.Append(record); err != nil {
		t.Fatalf("append record: %v", err)
	}
}

func listAll(t *testing.T, store *Store, query Query) Result {
	t.Helper()
	result, err := store.List(query)
	if err != nil {
		t.Fatalf("list requests: %v", err)
	}
	return result
}

func TestAppendWritesOneJSONLineWithFields(t *testing.T) {
	store := testStore(t)
	appendRecord(t, store, sampleRecord(fixedNow(), "deepseek-flash"))

	entries, err := os.ReadDir(store.Dir())
	if err != nil {
		t.Fatalf("read shard dir: %v", err)
	}
	if len(entries) != 1 {
		t.Fatalf("expected 1 shard, got %d", len(entries))
	}
	body, err := os.ReadFile(filepath.Join(store.Dir(), entries[0].Name()))
	if err != nil {
		t.Fatalf("read shard: %v", err)
	}
	lines := strings.Split(strings.TrimSpace(string(body)), "\n")
	if len(lines) != 1 {
		t.Fatalf("expected 1 line, got %d", len(lines))
	}
	for _, want := range []string{
		`"request_id":"req-1"`,
		`"input_tokens":500`,
		`"output_tokens":20`,
		`"cache_read_tokens":490`,
		`"cache_write_tokens":0`,
		`"ttft_ms":765`,
		`"duration_ms":1700`,
		`"model":"deepseek-flash"`,
		`"status_code":200`,
	} {
		if !strings.Contains(string(body), want) {
			t.Fatalf("shard line missing %s: %s", want, body)
		}
	}
}

func TestAppendSkipsEmptyModelCallID(t *testing.T) {
	store := testStore(t)
	record := sampleRecord(fixedNow(), "deepseek-flash")
	record.ModelCallID = "  "
	appendRecord(t, store, record)

	if result := listAll(t, store, Query{}); result.Total != 0 {
		t.Fatalf("expected empty index, got %d rows", result.Total)
	}
}

func TestListReturnsNewestFirst(t *testing.T) {
	store := testStore(t)
	base := fixedNow()
	appendRecord(t, store, sampleRecord(base.Add(-2*time.Hour), "old-model"))
	appendRecord(t, store, sampleRecord(base.Add(-1*time.Hour), "new-model"))

	result := listAll(t, store, Query{})
	if result.Total != 2 {
		t.Fatalf("expected 2 rows, got %d", result.Total)
	}
	if result.Items[0].Model != "new-model" || result.Items[1].Model != "old-model" {
		t.Fatalf("rows not newest first: %+v", result.Items)
	}
}

func TestListRangeFiltersByWindow(t *testing.T) {
	store := testStore(t)
	base := fixedNow()
	appendRecord(t, store, sampleRecord(base.Add(-30*time.Minute), "within-24h"))
	appendRecord(t, store, sampleRecord(base.Add(-3*24*time.Hour), "within-7d"))
	appendRecord(t, store, sampleRecord(base.Add(-20*24*time.Hour), "within-30d"))

	cases := []struct {
		rangeName string
		want      int
	}{
		{"today", 1},
		{"24h", 1},
		{"7d", 2},
		{"30d", 3},
	}
	for _, tc := range cases {
		if got := listAll(t, store, Query{Range: tc.rangeName}).Total; got != tc.want {
			t.Fatalf("range %s: expected %d rows, got %d", tc.rangeName, tc.want, got)
		}
	}
}

func TestListFiltersProviderModelAndStatus(t *testing.T) {
	store := testStore(t)
	base := fixedNow()
	ok := sampleRecord(base.Add(-time.Minute), "deepseek-flash")
	failed := sampleRecord(base.Add(-2*time.Minute), "deepseek-flash")
	failed.Status = "provider_error"
	failed.StatusCode = 429
	other := sampleRecord(base.Add(-3*time.Minute), "gpt-5")
	other.Provider = "anthropic"
	for _, record := range []Record{ok, failed, other} {
		appendRecord(t, store, record)
	}

	if got := listAll(t, store, Query{Provider: "openai"}).Total; got != 2 {
		t.Fatalf("provider filter: expected 2 rows, got %d", got)
	}
	if got := listAll(t, store, Query{Model: "gpt-5"}).Total; got != 1 {
		t.Fatalf("model filter: expected 1 row, got %d", got)
	}
	if got := listAll(t, store, Query{Status: "failed"}).Total; got != 1 {
		t.Fatalf("status filter: expected 1 row, got %d", got)
	}
}

func TestListPaginationAndSummaryCoverFilteredRows(t *testing.T) {
	store := testStore(t)
	base := fixedNow()
	for index := 0; index < 5; index++ {
		record := sampleRecord(base.Add(-time.Duration(index)*time.Minute), "deepseek-flash")
		record.RequestID = "req-" + string(rune('a'+index))
		appendRecord(t, store, record)
	}

	page := listAll(t, store, Query{Page: 2, PageSize: 2})
	if page.Total != 5 {
		t.Fatalf("expected total 5, got %d", page.Total)
	}
	if len(page.Items) != 2 {
		t.Fatalf("expected 2 items on page 2, got %d", len(page.Items))
	}
	if page.Items[0].RequestID != "req-c" || page.Items[1].RequestID != "req-d" {
		t.Fatalf("unexpected page slice: %+v", page.Items)
	}
	if page.Summary.Requests != 5 || page.Summary.InputTokens != 2500 {
		t.Fatalf("summary must cover all filtered rows: %+v", page.Summary)
	}
}

func TestTodayRangeIncludesLocalMidnightBoundary(t *testing.T) {
	store := testStore(t)
	base := fixedNow()
	local := base.In(time.Local)
	midnight := time.Date(local.Year(), local.Month(), local.Day(), 0, 0, 0, 0, time.Local)
	appendRecord(t, store, sampleRecord(midnight.UTC(), "at-midnight"))
	appendRecord(t, store, sampleRecord(midnight.Add(-time.Second).UTC(), "before-midnight"))

	result := listAll(t, store, Query{Range: "today"})
	if result.Total != 1 || result.Items[0].Model != "at-midnight" {
		t.Fatalf("today range must start at local midnight: %+v", result.Items)
	}
}

func TestListSkipsCorruptLine(t *testing.T) {
	store := testStore(t)
	appendRecord(t, store, sampleRecord(fixedNow(), "deepseek-flash"))

	entries, err := os.ReadDir(store.Dir())
	if err != nil {
		t.Fatalf("read shard dir: %v", err)
	}
	shardPath := filepath.Join(store.Dir(), entries[0].Name())
	file, err := os.OpenFile(shardPath, os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		t.Fatalf("open shard: %v", err)
	}
	if _, err := file.WriteString("{not json\n"); err != nil {
		t.Fatalf("append corrupt line: %v", err)
	}
	_ = file.Close()

	if result := listAll(t, store, Query{}); result.Total != 1 {
		t.Fatalf("expected corrupt line to be skipped, got %d rows", result.Total)
	}
}

func TestDistinctReturnsSortedProvidersAndModels(t *testing.T) {
	store := testStore(t)
	base := fixedNow()
	first := sampleRecord(base.Add(-time.Minute), "deepseek-flash")
	first.Provider = "openai"
	second := sampleRecord(base.Add(-2*time.Minute), "gpt-5")
	second.Provider = "anthropic"
	// 窗口外的记录不得进入筛选候选。
	outside := sampleRecord(base.Add(-10*24*time.Hour), "ancient-model")
	outside.Provider = "legacy"
	for _, record := range []Record{first, second, outside} {
		appendRecord(t, store, record)
	}

	providers, models, err := store.Distinct("24h")
	if err != nil {
		t.Fatalf("distinct: %v", err)
	}
	if len(providers) != 2 || providers[0] != "anthropic" || providers[1] != "openai" {
		t.Fatalf("providers = %v, want [anthropic openai]", providers)
	}
	// 模型候选同时包含实际模型与请求模型，与 List 的模型筛选口径一致。
	want := []string{"deepseek-flash", "gpt-5", "group/smartdk"}
	if len(models) != len(want) {
		t.Fatalf("models = %v, want %v", models, want)
	}
	for index := range want {
		if models[index] != want[index] {
			t.Fatalf("models = %v, want %v", models, want)
		}
	}
}

func TestConcurrentAppendKeepsEveryLine(t *testing.T) {
	store := testStore(t)
	const workers = 8
	const perWorker = 20
	var group sync.WaitGroup
	for worker := 0; worker < workers; worker++ {
		group.Add(1)
		go func(worker int) {
			defer group.Done()
			for index := 0; index < perWorker; index++ {
				record := sampleRecord(fixedNow(), "deepseek-flash")
				record.ModelCallID = "call-" + strconv.Itoa(worker) + "-" + strconv.Itoa(index)
				appendRecord(t, store, record)
			}
		}(worker)
	}
	group.Wait()

	if result := listAll(t, store, Query{}); result.Total != workers*perWorker {
		t.Fatalf("expected %d rows after concurrent append, got %d", workers*perWorker, result.Total)
	}
}

func TestAppendPrunesShardsOlderThanRetention(t *testing.T) {
	store := testStore(t)
	expired := filepath.Join(store.Dir(), shardName(fixedNow().Add(-40*24*time.Hour)))
	if err := os.MkdirAll(store.Dir(), 0o700); err != nil {
		t.Fatalf("create shard dir: %v", err)
	}
	if err := os.WriteFile(expired, []byte("{}\n"), 0o600); err != nil {
		t.Fatalf("write expired shard: %v", err)
	}

	appendRecord(t, store, sampleRecord(fixedNow(), "deepseek-flash"))
	// 清理在后台执行，等待后才能断言结果。
	store.WaitPrune()

	if _, err := os.Stat(expired); !os.IsNotExist(err) {
		t.Fatalf("expired shard should be pruned, stat err=%v", err)
	}
}

// 窗口起点之前的旧分片不得被读入：其中即便有命中记录也不能出现在结果里。
func TestListIgnoresShardsBeforeWindow(t *testing.T) {
	store := testStore(t)
	base := fixedNow()
	old := sampleRecord(base.Add(-20*24*time.Hour), "ancient-model")
	if err := store.Append(Record{At: old.At}); err != nil {
		t.Fatalf("seed: %v", err)
	}
	appendRecord(t, store, sampleRecord(base.Add(-time.Minute), "recent-model"))

	result := listAll(t, store, Query{Range: "today"})
	if result.Total != 1 || result.Items[0].Model != "recent-model" {
		t.Fatalf("today range must not read old shards: %+v", result.Items)
	}
	if result.Summary.InputTokens != 500 {
		t.Fatalf("summary must only cover in-window rows: %+v", result.Summary)
	}
}

// 单次查询的扫描行数有上限，避免超大历史导致一次查询无限变慢。
func TestListCapsScannedRecords(t *testing.T) {
	store := testStore(t)
	base := fixedNow()
	lines := make([]string, 0, maxScanRecords+10)
	for index := 0; index < maxScanRecords+10; index++ {
		value := sampleRecord(base.Add(-time.Duration(index)*time.Second), "bulk-model")
		value.ModelCallID = "cap-" + strconv.Itoa(index)
		line, err := json.Marshal(value)
		if err != nil {
			t.Fatalf("marshal: %v", err)
		}
		lines = append(lines, string(line))
	}
	if err := os.MkdirAll(store.Dir(), 0o700); err != nil {
		t.Fatalf("create shard dir: %v", err)
	}
	path := filepath.Join(store.Dir(), shardName(base))
	if err := os.WriteFile(path, []byte(strings.Join(lines, "\n")+"\n"), 0o600); err != nil {
		t.Fatalf("write shard: %v", err)
	}

	result := listAll(t, store, Query{Range: "30d", PageSize: 1})
	if result.Total != maxScanRecords {
		t.Fatalf("scanned rows must be capped at %d, got %d", maxScanRecords, result.Total)
	}
	// 倒序读入，截断发生在最旧的行上，首页仍是最新记录。
	if len(result.Items) != 1 || result.Items[0].ModelCallID != "cap-0" {
		t.Fatalf("first page must keep newest rows: %+v", result.Items)
	}
}

func TestRequestModelNameRoundTripAndFilter(t *testing.T) {
	store := testStore(t)
	record := sampleRecord(fixedNow().Add(-time.Minute), "glm-5.3")
	record.RequestModel = "eeb727d5b08b1fb8"
	record.RequestModelName = "glm5.3"
	appendRecord(t, store, record)

	result := listAll(t, store, Query{})
	if len(result.Items) != 1 || result.Items[0].RequestModelName != "glm5.3" {
		t.Fatalf("request model name = %+v, want glm5.3", result.Items)
	}
	if byName := listAll(t, store, Query{Model: "glm5.3"}); byName.Total != 1 {
		t.Fatalf("filter by request model name total = %d, want 1", byName.Total)
	}
	if byID := listAll(t, store, Query{Model: "eeb727d5b08b1fb8"}); byID.Total != 1 {
		t.Fatalf("filter by request model id total = %d, want 1", byID.Total)
	}

	_, models, err := store.Distinct("24h")
	if err != nil {
		t.Fatalf("distinct: %v", err)
	}
	for _, want := range []string{"eeb727d5b08b1fb8", "glm-5.3", "glm5.3"} {
		found := false
		for _, model := range models {
			if model == want {
				found = true
				break
			}
		}
		if !found {
			t.Fatalf("models = %v, want to contain %q", models, want)
		}
	}
}
