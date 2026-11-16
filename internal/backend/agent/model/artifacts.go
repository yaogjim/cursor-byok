package modeladapter

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"strconv"
	"strings"
	"time"
)

// recordLLMRequestArtifact 记录一次模型调用的原始请求工件。
func recordLLMRequestArtifact(req StreamRequest, provider string, model string, method string, url string, body any) {
	if req.Observer == nil {
		return
	}
	artifactCallID, channelIndex := fallbackArtifactIdentity(req)
	bodyBytes, _ := json.Marshal(body)
	payload := map[string]any{
		"request_id":                     req.RequestID,
		"run_id":                         req.RunID,
		"model_call_id":                  strings.TrimSpace(req.ModelCallID),
		"provider":                       strings.TrimSpace(provider),
		"openai_endpoint":                strings.TrimSpace(req.OpenAIEndpoint),
		"model":                          firstNonEmptyString(model, req.ModelID),
		"runtime_model_id":               strings.TrimSpace(req.ModelID),
		"resolved_channel_id":            strings.TrimSpace(req.ResolvedChannelID),
		"resolved_channel_name":          strings.TrimSpace(req.ResolvedChannelName),
		"resolved_context_window_tokens": req.ResolvedContextWindowTokens,
		"url":                            sanitizeModelArtifactURL(url),
		"method":                         method,
		"body_byte_len":                  len(bodyBytes),
		"request_knobs":                  req.RequestKnobs,
		"compile_summary":                req.CompileSummary,
		"stable_message_count":           req.StableMessageCount,
		"tools_summary":                  summarizeTools(req.Tools),
		"messages_summary":               summarizeMessages(req.Messages),
	}
	if retention := strings.TrimSpace(req.ImageRetention); retention != "" {
		payload["image_retention"] = retention
	}
	if artifactCallID != strings.TrimSpace(req.ModelCallID) {
		payload["artifact_model_call_id"] = artifactCallID
		payload["fallback_channel_index"] = channelIndex
	}
	path, err := req.Observer.RecordLLMRequest(req.RequestID, req.RunID, artifactCallID, payload)
	if err == nil && req.ArtifactPaths != nil {
		req.ArtifactPaths.RequestPath = path
	}
}

func fallbackArtifactIdentity(req StreamRequest) (artifactCallID string, channelIndex int) {
	canonical := strings.TrimSpace(req.ModelCallID)
	suffix := strings.TrimSpace(req.FallbackArtifactSuffix)
	if suffix == "" {
		return canonical, 0
	}
	artifactCallID = canonical + suffix
	indexText := strings.TrimPrefix(suffix, "_fb")
	if indexText == suffix {
		return artifactCallID, 0
	}
	index, err := strconv.Atoi(indexText)
	if err != nil || index < 0 {
		return artifactCallID, 0
	}
	return artifactCallID, index
}

// buildLLMSummaryPayload 生成 LLM 调用摘要工件内容。
func buildLLMSummaryPayload(
	req StreamRequest,
	provider string,
	model string,
	startedAt time.Time,
	firstEventAt time.Time,
	finishedAt time.Time,
	finishReason string,
	inputTokens int64,
	outputTokens int64,
	cacheReadTokens int64,
	cacheWriteTokens int64,
	err error,
	cacheReadPresent ...bool,
) map[string]any {
	finished := finishedAt
	if finished.IsZero() {
		finished = time.Now().UTC()
	}
	promptTokensTotal := inputTokens + cacheReadTokens + cacheWriteTokens
	requestTokensTotal := promptTokensTotal + outputTokens
	payload := map[string]any{
		"provider":             strings.TrimSpace(provider),
		"model":                strings.TrimSpace(model),
		"started_at":           normalizeModelArtifactTime(startedAt),
		"first_event_at":       normalizeModelArtifactTime(firstEventAt),
		"finished_at":          normalizeModelArtifactTime(finished),
		"finish_reason":        strings.TrimSpace(finishReason),
		"input_tokens":         inputTokens,
		"output_tokens":        outputTokens,
		"cache_read_tokens":    cacheReadTokens,
		"cache_write_tokens":   cacheWriteTokens,
		"prompt_tokens_total":  promptTokensTotal,
		"request_tokens_total": requestTokensTotal,
		"error":                summarizeModelArtifactError(err),
		"ttft_ms":              computeTTFTMS(startedAt, firstEventAt),
		"duration_ms":          computeDurationMS(startedAt, finished),
	}
	if len(cacheReadPresent) > 0 {
		payload["cache_read_present"] = cacheReadPresent[0]
	}
	if profile := strings.TrimSpace(req.CodexAffinityProfile); profile != "" {
		payload["codex_affinity_profile"] = profile
		payload["codex_affinity_injected"] = req.CodexAffinity != (CodexAffinity{})
	}
	if composition := strings.TrimSpace(req.RequestComposition); composition != "" {
		payload["request_composition"] = composition
	}
	return payload
}

// appendLLMResponseArtifact 只记录响应分块大小；Provider 原始响应正文不得进入工件或观测日志。
func appendLLMResponseArtifact(req StreamRequest, chunk string) (string, error) {
	if req.Observer == nil {
		return "", nil
	}
	artifactCallID := req.ModelCallID + req.FallbackArtifactSuffix
	path, err := req.Observer.AppendLLMResponseChunk(req.RequestID, req.RunID, artifactCallID, chunk)
	if err == nil && req.ArtifactPaths != nil {
		req.ArtifactPaths.ResponsePath = path
	}
	return path, err
}

// recordLLMSummaryArtifact 记录模型调用摘要。
func recordLLMSummaryArtifact(req StreamRequest, payload map[string]any) {
	if req.Observer == nil {
		return
	}
	artifactCallID := req.ModelCallID + req.FallbackArtifactSuffix
	path, err := req.Observer.RecordLLMSummary(req.RequestID, req.RunID, artifactCallID, payload)
	if err == nil && req.ArtifactPaths != nil {
		req.ArtifactPaths.SummaryPath = path
	}
}

// summarizeTools 生成工具列表摘要。
func summarizeTools(items []json.RawMessage) []string {
	result := make([]string, 0, len(items))
	for _, item := range items {
		var wrapper struct {
			Function struct {
				Name string `json:"name"`
			} `json:"function"`
		}
		if err := json.Unmarshal(item, &wrapper); err != nil {
			continue
		}
		if strings.TrimSpace(wrapper.Function.Name) != "" {
			result = append(result, strings.TrimSpace(wrapper.Function.Name))
		}
	}
	return result
}

// summarizeMessages 生成消息列表摘要。
func summarizeMessages(items []Message) []map[string]any {
	result := make([]map[string]any, 0, len(items))
	for _, item := range items {
		contentLength := len([]rune(strings.TrimSpace(item.Content)))
		imageCount := 0
		for _, part := range item.ContentParts {
			if normalizeContentPartType(part.Type) == contentPartTypeImage {
				imageCount++
			}
		}
		result = append(result, map[string]any{
			"role":            item.Role,
			"content_length":  contentLength,
			"image_count":     imageCount,
			"tool_call_count": len(item.ToolCalls),
			"tool_call_id":    item.ToolCallID,
			"name":            item.Name,
		})
	}
	return result
}

// summarizeRequestComposition 生成不含正文的请求体组成统计，用于定位超大请求。
// 只输出字节数与计数，不包含 message、tool result 或图片内容。
func summarizeRequestComposition(body map[string]any, totalBytes int) string {
	if body == nil {
		return ""
	}
	items := requestCompositionItems(body, "messages")
	if items == nil {
		items = requestCompositionItems(body, "input")
	}
	var itemsBytes, largest, imageBytes, toolResultBytes int64
	imageCount := 0
	for _, raw := range items {
		encoded, err := json.Marshal(raw)
		if err != nil {
			continue
		}
		size := int64(len(encoded))
		itemsBytes += size
		if size > largest {
			largest = size
		}
		item, ok := raw.(map[string]any)
		if !ok {
			continue
		}
		role, _ := item["role"].(string)
		itemType, _ := item["type"].(string)
		if role == "tool" || itemType == "function_call_output" {
			toolResultBytes += size
		}
		// 图片可能出现在消息 content（chat/tool 消息）或 Responses 工具结果 output，
		// 两处都要统计，否则大图会被算进 tool_result_bytes 却报告 image_count=0。
		for _, key := range []string{"content", "output"} {
			count, bytes := requestContentImageStats(item[key])
			imageCount += count
			imageBytes += bytes
		}
	}
	return fmt.Sprintf(
		"total_bytes=%d;messages_bytes=%d;tools_bytes=%d;instructions_bytes=%d;message_count=%d;image_count=%d;image_bytes=%d;tool_result_bytes=%d;largest_message_bytes=%d",
		totalBytes, itemsBytes, jsonEncodedSize(body["tools"]),
		jsonEncodedSize(body["instructions"])+jsonEncodedSize(body["system"]),
		len(items), imageCount, imageBytes, toolResultBytes, largest,
	)
}

func requestCompositionItems(body map[string]any, key string) []any {
	if items, ok := body[key].([]any); ok {
		return items
	}
	if items, ok := body[key].([]map[string]any); ok {
		result := make([]any, len(items))
		for index := range items {
			result[index] = items[index]
		}
		return result
	}
	return nil
}

func requestContentImageStats(content any) (count int, bytes int64) {
	for _, raw := range requestContentParts(content) {
		part, ok := raw.(map[string]any)
		if !ok {
			continue
		}
		partType, _ := part["type"].(string)
		if partType != "image_url" && partType != "input_image" {
			continue
		}
		url := ""
		switch value := part["image_url"].(type) {
		case string:
			url = value
		case map[string]any:
			url, _ = value["url"].(string)
		}
		count++
		bytes += int64(len(url))
	}
	return count, bytes
}

// requestContentParts 归一化内容部件容器，兼容 []any 与 []map[string]any 两种构造形态。
func requestContentParts(content any) []any {
	switch typed := content.(type) {
	case []any:
		return typed
	case []map[string]any:
		parts := make([]any, len(typed))
		for index := range typed {
			parts[index] = typed[index]
		}
		return parts
	default:
		return nil
	}
}

func jsonEncodedSize(value any) int64 {
	if value == nil {
		return 0
	}
	encoded, err := json.Marshal(value)
	if err != nil {
		return 0
	}
	return int64(len(encoded))
}

// normalizeModelArtifactTime 把时间格式化为 RFC3339Nano。
func normalizeModelArtifactTime(value time.Time) string {
	if value.IsZero() {
		return ""
	}
	return value.UTC().Format(time.RFC3339Nano)
}

// computeTTFTMS 计算首事件耗时。
func computeTTFTMS(startedAt time.Time, firstEventAt time.Time) int64 {
	if startedAt.IsZero() || firstEventAt.IsZero() {
		return 0
	}
	value := firstEventAt.Sub(startedAt).Milliseconds()
	if value < 0 {
		return 0
	}
	return value
}

// computeDurationMS 计算调用总耗时。
func computeDurationMS(startedAt time.Time, finishedAt time.Time) int64 {
	if startedAt.IsZero() || finishedAt.IsZero() {
		return 0
	}
	value := finishedAt.Sub(startedAt).Milliseconds()
	if value < 0 {
		return 0
	}
	return value
}

// summarizeModelArtifactError 返回不含 Provider 响应正文和凭证的结构化错误摘要。
func summarizeModelArtifactError(err error) string {
	if err == nil {
		return ""
	}
	category := strings.TrimSpace(ClassifyProviderError(err))
	if category == "" {
		category = "provider_error"
	}
	var httpErr *HTTPStatusError
	if errors.As(err, &httpErr) && httpErr != nil && httpErr.StatusCode > 0 {
		return fmt.Sprintf("%s status=%d", category, httpErr.StatusCode)
	}
	return category
}

// sanitizeModelArtifactURL 保留定位所需的 scheme/host/path，但绝不记录 query 或 fragment。
func sanitizeModelArtifactURL(raw string) string {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return ""
	}
	parsed, err := url.Parse(trimmed)
	if err == nil {
		parsed.RawQuery = ""
		parsed.ForceQuery = false
		parsed.Fragment = ""
		return parsed.String()
	}
	if index := strings.IndexAny(trimmed, "?#"); index >= 0 {
		return trimmed[:index]
	}
	return trimmed
}

// firstNonEmptyString 返回第一个非空字符串。
func firstNonEmptyString(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return strings.TrimSpace(value)
		}
	}
	return ""
}
