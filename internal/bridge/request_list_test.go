package bridge

import (
	"path/filepath"
	"testing"
	"time"

	"cursor/internal/requestlog"
)

// seedRequestIndex 在临时 history 根目录下构造索引，返回指向该目录的 MetricsService。
func seedRequestIndex(t *testing.T) *MetricsService {
	t.Helper()
	store := requestlog.NewStore(t.TempDir(), 30*24*time.Hour)
	service := NewMetricsService()
	service.historyRoot = filepath.Dir(store.Dir())
	now := time.Now().UTC()
	for index := 0; index < 3; index++ {
		record := requestlog.Record{
			RequestID:        "req-" + string(rune('a'+index)),
			ConversationID:   "conv-1",
			ModelCallID:      "call-" + string(rune('a'+index)),
			RequestModel:     "group/smartdk",
			Provider:         "openai",
			Model:            "deepseek-flash",
			InputTokens:      100,
			OutputTokens:     10,
			CacheReadTokens:  90,
			CacheWriteTokens: 0,
			TTFTMS:           700,
			DurationMS:       1500,
			StatusCode:       200,
			Status:           "completed",
			At:               now.Add(-time.Duration(index) * time.Minute),
		}
		if index == 2 {
			record.Status = "provider_error"
			record.StatusCode = 429
			record.ErrorCategory = "rate_limit"
		}
		if err := store.Append(record); err != nil {
			t.Fatalf("seed request index: %v", err)
		}
	}
	return service
}

func TestListRequestsReturnsRowsAndSummary(t *testing.T) {
	service := seedRequestIndex(t)

	result, err := service.ListRequests(RequestListQuery{Range: "24h", Page: 1, PageSize: 2})
	if err != nil {
		t.Fatalf("ListRequests() error = %v", err)
	}
	if result.Total != 3 || len(result.Items) != 2 {
		t.Fatalf("total/items = %d/%d, want 3/2", result.Total, len(result.Items))
	}
	if result.Summary.Requests != 3 || result.Summary.InputTokens != 300 {
		t.Fatalf("summary = %+v, want 3 requests and 300 input tokens across all rows", result.Summary)
	}
	first := result.Items[0]
	if first.Model != "deepseek-flash" || first.CacheHitRate == nil {
		t.Fatalf("first row = %+v, want model and cache hit rate", first)
	}
	// InputTokens 是非缓存输入（适配器已扣除缓存读取），
	// 默认口径为 缓存读取 /（缓存读取 + 非缓存输入）= 90 / 190。
	if got, want := first.CacheHitRateValue(), 90.0/190.0; got != want {
		t.Fatalf("cache hit rate = %v, want %v", got, want)
	}
}

func TestListRequestsFiltersFailed(t *testing.T) {
	service := seedRequestIndex(t)

	result, err := service.ListRequests(RequestListQuery{Range: "24h", Status: "failed"})
	if err != nil {
		t.Fatalf("ListRequests() error = %v", err)
	}
	if result.Total != 1 {
		t.Fatalf("failed rows = %d, want 1", result.Total)
	}
	if result.Items[0].StatusCode != 429 || result.Items[0].ErrorCategory != "rate_limit" {
		t.Fatalf("failed row = %+v, want 429/rate_limit", result.Items[0])
	}
}

func TestListRequestsWithoutIndexReturnsEmptyResult(t *testing.T) {
	result, err := NewMetricsService().ListRequests(RequestListQuery{Range: "24h"})
	if err != nil {
		t.Fatalf("ListRequests() error = %v", err)
	}
	if result.Total != 0 || len(result.Items) != 0 {
		t.Fatalf("empty index must yield empty result, got %+v", result)
	}
	if result.Page != 1 || result.PageSize <= 0 {
		t.Fatalf("pagination defaults = %d/%d", result.Page, result.PageSize)
	}
}

func TestListRequestFilterOptionsReturnsWindowValues(t *testing.T) {
	service := seedRequestIndex(t)

	options, err := service.ListRequestFilterOptions("24h")
	if err != nil {
		t.Fatalf("ListRequestFilterOptions() error = %v", err)
	}
	if len(options.Providers) != 1 || options.Providers[0] != "openai" {
		t.Fatalf("providers = %v, want [openai]", options.Providers)
	}
	if len(options.Models) != 2 {
		t.Fatalf("models = %v, want actual model and request model", options.Models)
	}
}

func TestListRequestsCarriesRequestModelName(t *testing.T) {
	store := requestlog.NewStore(t.TempDir(), 30*24*time.Hour)
	record := requestlog.Record{
		RequestID:        "req-name",
		ModelCallID:      "call-name",
		RequestModel:     "eeb727d5b08b1fb8",
		RequestModelName: "glm5.3",
		Provider:         "openai",
		Model:            "glm-5.3",
		Status:           "completed",
		At:               time.Now().UTC(),
	}
	if err := store.Append(record); err != nil {
		t.Fatalf("seed request index: %v", err)
	}
	service := NewMetricsService()
	service.historyRoot = filepath.Dir(store.Dir())

	result, err := service.ListRequests(RequestListQuery{Range: "24h", Page: 1, PageSize: 10})
	if err != nil {
		t.Fatalf("ListRequests() error = %v", err)
	}
	if len(result.Items) != 1 {
		t.Fatalf("items = %d, want 1", len(result.Items))
	}
	if result.Items[0].RequestModel != "eeb727d5b08b1fb8" || result.Items[0].RequestModelName != "glm5.3" {
		t.Fatalf("row = %+v, want request model id and name", result.Items[0])
	}
}
