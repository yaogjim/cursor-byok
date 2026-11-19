package forwarder

import (
	"log"
	"strconv"
	"strings"
	"time"

	"cursor/internal/requestlog"
)

// requestIndexRetention 是请求列表索引的保留期；与列表默认的最大时间范围一致。
const requestIndexRetention = 30 * 24 * time.Hour

// recordRequestIndex 把一次业务模型调用追加到请求索引。
// 内部摘要调用、缺少身份信息的调用直接跳过；写入失败只记日志，不影响主链路。
func (service *Service) recordRequestIndex(stream *ActiveStream, requestID string, modelCallID string, status string, usage turnUsageSnapshot, recordedAt time.Time) {
	if service == nil || service.requestIndex == nil || stream == nil {
		return
	}
	stream.mu.Lock()
	purpose := stream.CurrentProviderPurpose
	conversationID := strings.TrimSpace(stream.ConversationID)
	requestModel := strings.TrimSpace(stream.ModelID)
	requestModelName := strings.TrimSpace(stream.ModelName)
	createdAt := stream.CreatedAt
	stats := stream.ProviderStreamStats
	stream.mu.Unlock()

	if !purpose.requestIndexEligible() {
		return
	}
	modelCallID = strings.TrimSpace(modelCallID)
	if modelCallID == "" {
		return
	}
	if recordedAt.IsZero() {
		recordedAt = time.Now().UTC()
	}
	record := requestlog.Record{
		RequestID:        strings.TrimSpace(requestID),
		ConversationID:   conversationID,
		ModelCallID:      modelCallID,
		RequestModel:     requestModel,
		RequestModelName: requestModelName,
		Provider:         firstNonEmpty(usage.Provider, stats.Provider),
		Model:            firstNonEmpty(usage.Model, stats.Model),
		InputTokens:      usage.InputTokens,
		OutputTokens:     usage.OutputTokens,
		CacheReadTokens:  usage.CacheReadTokens,
		CacheWriteTokens: usage.CacheWriteTokens,
		TTFTMS:           requestIndexTTFT(stream, stats),
		DurationMS:       requestIndexDuration(stream, stats, createdAt, recordedAt),
		StatusCode:       requestIndexStatusCode(stats.HTTPStatus),
		ErrorCategory:    strings.TrimSpace(stats.ErrorCategory),
		Status:           strings.TrimSpace(status),
		At:               recordedAt.UTC(),
	}
	if err := service.requestIndex.Append(record); err != nil {
		log.Printf("request index append failed request_id=%s model_call_id=%s err=%v", record.RequestID, modelCallID, err)
	}
}

// requestIndexDuration 优先使用观测到的流时长；缺失时退回流创建时刻到收口时刻。
func requestIndexDuration(stream *ActiveStream, stats ProviderStreamStats, createdAt time.Time, recordedAt time.Time) int64 {
	duration := stats.StreamDuration
	if duration <= 0 && !stats.StartedAt.IsZero() && !stats.FinishedAt.IsZero() {
		duration = stats.FinishedAt.Sub(stats.StartedAt)
	}
	if duration <= 0 && !createdAt.IsZero() {
		duration = recordedAt.Sub(createdAt)
	}
	if duration <= 0 {
		return 0
	}
	return duration.Milliseconds()
}

// requestIndexTTFT 返回首个 provider 事件相对 provider 起点的耗时；两者缺一即视为未知。
func requestIndexTTFT(stream *ActiveStream, stats ProviderStreamStats) int64 {
	startedAt := stats.StartedAt
	if startedAt.IsZero() && stream != nil {
		startedAt = stream.CreatedAt
	}
	if startedAt.IsZero() || stats.FirstEventAt.IsZero() {
		return 0
	}
	delay := stats.FirstEventAt.Sub(startedAt)
	if delay <= 0 {
		return 0
	}
	return delay.Milliseconds()
}

// requestIndexStatusCode 解析 HTTP 状态；"not_recorded" 与非数字取值都视为未知。
func requestIndexStatusCode(value string) int {
	value = strings.TrimSpace(value)
	if value == "" {
		return 0
	}
	code, err := strconv.Atoi(value)
	if err != nil || code <= 0 {
		return 0
	}
	return code
}
