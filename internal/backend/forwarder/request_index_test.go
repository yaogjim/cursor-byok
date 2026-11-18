package forwarder

import (
	"os"
	"testing"
	"time"

	"cursor/internal/requestlog"
)

func newRequestIndexTestStream(historyRoot string) *ActiveStream {
	return &ActiveStream{
		RequestID:      "request-1",
		ConversationID: "conversation-1",
		TurnSeq:        1,
		ModelID:        "group/smartdk",
		ModelName:      "group/smartdk",
		// 与 provider pass 起点（service.go 中 stats 初始化）保持一致。
		CurrentProviderPurpose: purposeChat,
		CheckpointConversation: &ConversationFile{
			ConversationID:     "conversation-1",
			RootConversationID: "conversation-1",
			Mode:               "agent",
		},
		CreatedAt: time.Date(2026, 11, 17, 10, 0, 0, 0, time.UTC),
		UpdatedAt: time.Date(2026, 11, 17, 10, 0, 5, 0, time.UTC),
		ProviderStreamStats: ProviderStreamStats{
			StartedAt:    time.Date(2026, 11, 17, 10, 0, 0, 500_000_000, time.UTC),
			FirstEventAt: time.Date(2026, 11, 17, 10, 0, 1, 265_000_000, time.UTC),
			FinishedAt:   time.Date(2026, 11, 17, 10, 0, 5, 0, time.UTC),
			HTTPStatus:   "200",
			Provider:     "openai",
			Model:        "deepseek-flash",
		},
	}
}

func usageForRequestIndex() turnUsageSnapshot {
	return turnUsageSnapshot{
		Provider:          "openai",
		Model:             "deepseek-flash",
		InputTokens:       500,
		OutputTokens:      20,
		CacheReadTokens:   490,
		UsagePresent:      true,
		CacheReadPresent:  true,
		CacheWritePresent: true,
	}
}

func listRequestIndex(t *testing.T, historyRoot string) requestlog.Result {
	t.Helper()
	result, err := requestlog.NewStore(historyRoot, 30*24*time.Hour).List(requestlog.Query{})
	if err != nil {
		t.Fatalf("list request index: %v", err)
	}
	return result
}

func TestRecordTurnUsageSnapshotWritesRequestIndexRow(t *testing.T) {
	root := t.TempDir()
	service := NewService(root, nil)
	stream := newRequestIndexTestStream(root)
	stream.mu.Lock()
	stream.CurrentModelCallID = "model-call-1"
	stream.mu.Unlock()

	if err := service.recordTurnUsageSnapshot(stream, "conversation-1", 1, "request-1", "model-call-1", "completed", usageForRequestIndex(), "", false); err != nil {
		t.Fatalf("recordTurnUsageSnapshot() error = %v", err)
	}

	result := listRequestIndex(t, root)
	if result.Total != 1 {
		t.Fatalf("request index rows = %d, want 1", result.Total)
	}
	row := result.Items[0]
	if row.ModelCallID != "model-call-1" || row.ConversationID != "conversation-1" {
		t.Fatalf("row identity = %q/%q, want model-call-1/conversation-1", row.ModelCallID, row.ConversationID)
	}
	if row.Model != "deepseek-flash" || row.Provider != "openai" {
		t.Fatalf("row model = %q/%q, want deepseek-flash/openai", row.Model, row.Provider)
	}
	if row.RequestModel != "group/smartdk" {
		t.Fatalf("row request model = %q, want group/smartdk", row.RequestModel)
	}
	if row.InputTokens != 500 || row.OutputTokens != 20 || row.CacheReadTokens != 490 || row.CacheWriteTokens != 0 {
		t.Fatalf("row tokens = %d/%d/%d/%d, want 500/20/490/0", row.InputTokens, row.OutputTokens, row.CacheReadTokens, row.CacheWriteTokens)
	}
	if row.TTFTMS != 765 {
		t.Fatalf("row ttft = %d, want 765", row.TTFTMS)
	}
	// provider 起点 0.5s，收口 5.0s；耗时取 provider 观测边界，不含编译与排队。
	if row.DurationMS != 4500 {
		t.Fatalf("row duration = %d, want 4500", row.DurationMS)
	}
	if row.StatusCode != 200 || row.Status != "completed" {
		t.Fatalf("row status = %d/%q, want 200/completed", row.StatusCode, row.Status)
	}
}

func TestRecordTurnUsageSnapshotRecordsProviderErrorStatus(t *testing.T) {
	root := t.TempDir()
	service := NewService(root, nil)
	stream := newRequestIndexTestStream(root)
	stream.mu.Lock()
	stream.ProviderStreamStats.HTTPStatus = "429"
	stream.ProviderStreamStats.ErrorCategory = "rate_limit"
	stream.mu.Unlock()

	if err := service.recordTurnUsageSnapshot(stream, "conversation-1", 1, "request-1", "model-call-err", "provider_error", turnUsageSnapshot{}, "rate limited", false); err != nil {
		t.Fatalf("recordTurnUsageSnapshot() error = %v", err)
	}

	result := listRequestIndex(t, root)
	if result.Total != 1 {
		t.Fatalf("request index rows = %d, want 1", result.Total)
	}
	row := result.Items[0]
	if row.Status != "provider_error" || row.StatusCode != 429 || row.ErrorCategory != "rate_limit" {
		t.Fatalf("row status = %q/%d/%q, want provider_error/429/rate_limit", row.Status, row.StatusCode, row.ErrorCategory)
	}
}

func TestRecordTurnUsageSnapshotSkipsSummaryPurpose(t *testing.T) {
	root := t.TempDir()
	service := NewService(root, nil)
	stream := newRequestIndexTestStream(root)
	stream.mu.Lock()
	stream.CurrentProviderPurpose = purposeSummary
	stream.mu.Unlock()

	if err := service.recordTurnUsageSnapshot(stream, "conversation-1", 1, "request-1", "model-call-summary", "completed", usageForRequestIndex(), "", false); err != nil {
		t.Fatalf("recordTurnUsageSnapshot() error = %v", err)
	}

	if result := listRequestIndex(t, root); result.Total != 0 {
		t.Fatalf("summary call must not enter request index, got %d rows", result.Total)
	}
}

func TestRecordTurnUsageSnapshotSurvivesIndexFailure(t *testing.T) {
	root := t.TempDir()
	service := NewService(root, nil)
	// 用普通文件占住 requests 目录，使追加必然失败。
	blocked := root + "/requests"
	if err := os.WriteFile(blocked, []byte("not a directory"), 0o600); err != nil {
		t.Fatalf("prepare blocking file: %v", err)
	}
	stream := newRequestIndexTestStream(root)

	if err := service.recordTurnUsageSnapshot(stream, "conversation-1", 1, "request-1", "model-call-1", "completed", usageForRequestIndex(), "", false); err != nil {
		t.Fatalf("index failure must not fail the main path, got %v", err)
	}
}
