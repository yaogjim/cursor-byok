package gateway

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	modeladapter "cursor/internal/backend/agent/model"
	"cursor/internal/backend/forwarder"
	serverconfig "cursor/internal/backend/server/config"
	"cursor/internal/logger"
	"cursor/internal/observability"
)

type parsedResponsesRequest struct {
	Model          string
	Messages       []modeladapter.Message
	Tools          []json.RawMessage
	RequestKnobs   map[string]any
	ConversationID string
	ThinkingEffort string
	MaxTokens      int
}

func parseResponsesRequest(body []byte) (parsedResponsesRequest, error) {
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(body, &raw); err != nil || raw == nil {
		return parsedResponsesRequest{}, errors.New("请求体必须是 JSON 对象")
	}
	for _, key := range []string{"previous_response_id", "websocket", "background"} {
		if value, ok := raw[key]; ok && !isJSONNull(value) {
			return parsedResponsesRequest{}, fmt.Errorf("暂不支持 %s", key)
		}
	}
	if value, ok := raw["store"]; ok && !isJSONNull(value) {
		var store bool
		if err := json.Unmarshal(value, &store); err != nil || store {
			return parsedResponsesRequest{}, errors.New("store 仅支持 false")
		}
	}
	if value, ok := raw["stream"]; ok {
		var stream bool
		if err := json.Unmarshal(value, &stream); err != nil || !stream {
			return parsedResponsesRequest{}, errors.New("stream 仅支持 true")
		}
	}
	model, err := jsonString(raw["model"])
	if err != nil || strings.TrimSpace(model) == "" {
		return parsedResponsesRequest{}, errors.New("model 不能为空")
	}
	if err := validateResponsesToolChoice(raw["tool_choice"]); err != nil {
		return parsedResponsesRequest{}, err
	}
	messages, err := parseResponsesInput(raw["input"])
	if err != nil {
		return parsedResponsesRequest{}, err
	}
	if value, ok := raw["instructions"]; ok && !isJSONNull(value) {
		instructions, err := jsonString(value)
		if err != nil {
			return parsedResponsesRequest{}, errors.New("instructions 必须是字符串")
		}
		if strings.TrimSpace(instructions) != "" {
			messages = append([]modeladapter.Message{{Role: "system", Content: instructions}}, messages...)
		}
	}
	tools, err := parseResponsesTools(raw["tools"])
	if err != nil {
		return parsedResponsesRequest{}, err
	}
	requestKnobs := make(map[string]any)
	if value, ok := raw["parallel_tool_calls"]; ok && !isJSONNull(value) {
		var enabled bool
		if err := json.Unmarshal(value, &enabled); err != nil {
			return parsedResponsesRequest{}, errors.New("parallel_tool_calls 必须是布尔值")
		}
		requestKnobs["parallel_tool_calls"] = enabled
	}
	conversationID := ""
	if value, ok := raw["prompt_cache_key"]; ok && !isJSONNull(value) {
		conversationID, err = jsonString(value)
		if err != nil {
			return parsedResponsesRequest{}, errors.New("prompt_cache_key 必须是字符串")
		}
		conversationID = strings.TrimSpace(conversationID)
	}
	thinkingEffort := ""
	if value, ok := raw["reasoning"]; ok && !isJSONNull(value) {
		var reasoning struct {
			Effort string `json:"effort"`
		}
		if err := json.Unmarshal(value, &reasoning); err != nil {
			return parsedResponsesRequest{}, errors.New("reasoning 必须是对象")
		}
		thinkingEffort = strings.TrimSpace(reasoning.Effort)
	}
	maxTokens := 0
	if value, ok := raw["max_output_tokens"]; ok && !isJSONNull(value) {
		if err := json.Unmarshal(value, &maxTokens); err != nil || maxTokens < 0 {
			return parsedResponsesRequest{}, errors.New("max_output_tokens 必须是非负整数")
		}
	}
	return parsedResponsesRequest{
		Model:          strings.TrimSpace(model),
		Messages:       messages,
		Tools:          tools,
		RequestKnobs:   requestKnobs,
		ConversationID: conversationID,
		ThinkingEffort: thinkingEffort,
		MaxTokens:      maxTokens,
	}, nil
}

func validateResponsesToolChoice(raw json.RawMessage) error {
	if len(raw) == 0 || isJSONNull(raw) {
		return nil
	}
	choice, err := jsonString(raw)
	if err != nil || strings.TrimSpace(choice) != "auto" {
		return errors.New("tool_choice 仅支持 auto")
	}
	return nil
}

func parseResponsesTools(raw json.RawMessage) ([]json.RawMessage, error) {
	if len(raw) == 0 || isJSONNull(raw) {
		return nil, nil
	}
	var items []map[string]json.RawMessage
	if err := json.Unmarshal(raw, &items); err != nil {
		return nil, errors.New("tools 必须是数组")
	}
	tools := make([]json.RawMessage, 0, len(items))
	for _, item := range items {
		kind, err := jsonString(item["type"])
		if err != nil {
			return nil, errors.New("tools[].type 必须是字符串")
		}
		// Codex 0.144.4 在每个请求中同时携带其未使用的 namespace/web_search
		// 描述与普通 function。它们是客户端本地能力，既不能由 Gateway 执行，
		// 也不能展平为顶层函数；跳过后仍可把普通 function 交给客户端 loop。
		switch strings.TrimSpace(kind) {
		case "namespace", "web_search", "tool_search":
			continue
		case "function":
		default:
			return nil, fmt.Errorf("tools[] 不支持 type=%s", strings.TrimSpace(kind))
		}
		name, err := jsonString(item["name"])
		if err != nil || strings.TrimSpace(name) == "" {
			return nil, errors.New("tools[].name 不能为空")
		}
		function := map[string]any{"name": strings.TrimSpace(name)}
		if value, ok := item["description"]; ok && !isJSONNull(value) {
			var description string
			if err := json.Unmarshal(value, &description); err != nil {
				return nil, errors.New("tools[].description 必须是字符串")
			}
			function["description"] = description
		}
		if value, ok := item["parameters"]; ok && !isJSONNull(value) {
			var parameters any
			if err := json.Unmarshal(value, &parameters); err != nil {
				return nil, errors.New("tools[].parameters 必须是 JSON")
			}
			function["parameters"] = parameters
		} else {
			function["parameters"] = map[string]any{"type": "object", "properties": map[string]any{}}
		}
		if value, ok := item["strict"]; ok && !isJSONNull(value) {
			var strict bool
			if err := json.Unmarshal(value, &strict); err != nil {
				return nil, errors.New("tools[].strict 必须是布尔值")
			}
			function["strict"] = strict
		}
		encoded, err := json.Marshal(map[string]any{"type": "function", "function": function})
		if err != nil {
			return nil, err
		}
		tools = append(tools, encoded)
	}
	return tools, nil
}

func parseResponsesInput(raw json.RawMessage) ([]modeladapter.Message, error) {
	if len(raw) == 0 || isJSONNull(raw) {
		return nil, errors.New("input 不能为空")
	}
	var text string
	if err := json.Unmarshal(raw, &text); err == nil {
		return []modeladapter.Message{{Role: "user", Content: text}}, nil
	}
	var items []map[string]json.RawMessage
	if err := json.Unmarshal(raw, &items); err != nil {
		return nil, errors.New("input 必须是字符串或 typed item 数组")
	}
	messages := make([]modeladapter.Message, 0, len(items))
	callNames := make(map[string]string)
	for _, item := range items {
		kind, _ := jsonString(item["type"])
		switch strings.TrimSpace(kind) {
		case "message":
			message, err := parseResponsesMessage(item)
			if err != nil {
				return nil, err
			}
			messages = append(messages, message)
		case "function_call":
			callID, err := jsonString(item["call_id"])
			if err != nil || strings.TrimSpace(callID) == "" {
				return nil, errors.New("function_call.call_id 不能为空")
			}
			name, err := jsonString(item["name"])
			if err != nil || strings.TrimSpace(name) == "" {
				return nil, errors.New("function_call.name 不能为空")
			}
			arguments, err := jsonString(item["arguments"])
			if err != nil {
				return nil, errors.New("function_call.arguments 必须是字符串")
			}
			itemID, _ := jsonString(item["id"])
			callID = strings.TrimSpace(callID)
			name = strings.TrimSpace(name)
			callNames[callID] = name
			messages = append(messages, modeladapter.Message{
				Role: "assistant",
				ToolCalls: []modeladapter.ToolCallDescriptor{{
					ID:                    callID,
					Index:                 0,
					Type:                  "function",
					OpenAIResponsesID:     strings.TrimSpace(itemID),
					OpenAIResponsesCallID: callID,
					Function:              modeladapter.ToolCallFunctionShape{Name: name, Arguments: arguments},
				}},
			})
		case "function_call_output":
			callID, err := jsonString(item["call_id"])
			if err != nil || strings.TrimSpace(callID) == "" {
				return nil, errors.New("function_call_output.call_id 不能为空")
			}
			output, err := parseResponsesFunctionOutput(item["output"])
			if err != nil {
				return nil, err
			}
			callID = strings.TrimSpace(callID)
			messages = append(messages, modeladapter.Message{Role: "tool", ToolCallID: callID, Name: callNames[callID], Content: output})
		case "reasoning":
			signature, err := jsonString(item["encrypted_content"])
			if err != nil || strings.TrimSpace(signature) == "" {
				return nil, errors.New("reasoning.encrypted_content 不能为空")
			}
			itemID, _ := jsonString(item["id"])
			status, _ := jsonString(item["status"])
			var summary json.RawMessage
			if value, ok := item["summary"]; ok && !isJSONNull(value) {
				summary = append([]byte(nil), value...)
			}
			messages = append(messages, modeladapter.Message{
				Role:                            "assistant",
				ReasoningSignature:              strings.TrimSpace(signature),
				ReasoningSignatureSource:        modeladapter.ReasoningSignatureSourceOpenAIResponses,
				OpenAIResponsesReasoningID:      strings.TrimSpace(itemID),
				OpenAIResponsesReasoningStatus:  strings.TrimSpace(status),
				OpenAIResponsesReasoningSummary: summary,
			})
		default:
			return nil, fmt.Errorf("暂不支持 input item type=%s", strings.TrimSpace(kind))
		}
	}
	if len(messages) == 0 {
		return nil, errors.New("input 不能为空")
	}
	return messages, nil
}

func parseResponsesMessage(item map[string]json.RawMessage) (modeladapter.Message, error) {
	role, err := jsonString(item["role"])
	if err != nil {
		return modeladapter.Message{}, errors.New("message.role 必须是字符串")
	}
	role = strings.ToLower(strings.TrimSpace(role))
	switch role {
	case "developer", "system":
		role = "system"
	case "user", "assistant":
	default:
		return modeladapter.Message{}, fmt.Errorf("不支持的 message.role %s", role)
	}
	contentRaw := item["content"]
	var text string
	if err := json.Unmarshal(contentRaw, &text); err == nil {
		return modeladapter.Message{Role: role, Content: text}, nil
	}
	var parts []map[string]json.RawMessage
	if err := json.Unmarshal(contentRaw, &parts); err != nil || len(parts) == 0 {
		return modeladapter.Message{}, errors.New("message.content 必须是文本或文本块数组")
	}
	var content strings.Builder
	for _, part := range parts {
		kind, _ := jsonString(part["type"])
		if kind != "input_text" && kind != "output_text" {
			return modeladapter.Message{}, fmt.Errorf("暂不支持 message content type=%s", kind)
		}
		partText, err := jsonString(part["text"])
		if err != nil {
			return modeladapter.Message{}, errors.New("message content text 必须是字符串")
		}
		content.WriteString(partText)
	}
	return modeladapter.Message{Role: role, Content: content.String()}, nil
}

func parseResponsesFunctionOutput(raw json.RawMessage) (string, error) {
	var output string
	if err := json.Unmarshal(raw, &output); err == nil {
		return output, nil
	}
	var parts []map[string]json.RawMessage
	if err := json.Unmarshal(raw, &parts); err != nil || len(parts) == 0 {
		return "", errors.New("function_call_output.output 必须是字符串或文本块数组")
	}
	var text strings.Builder
	for _, part := range parts {
		kind, _ := jsonString(part["type"])
		if kind != "input_text" {
			return "", fmt.Errorf("暂不支持 function_call_output type=%s", kind)
		}
		value, err := jsonString(part["text"])
		if err != nil {
			return "", errors.New("function_call_output text 必须是字符串")
		}
		text.WriteString(value)
	}
	return text.String(), nil
}

type responsesAggregator struct {
	responseID      string
	publicModel     string
	createdAt       int64
	text            strings.Builder
	output          []map[string]any
	toolCallIDs     map[string]struct{}
	inputTokens     int64
	outputTokens    int64
	cacheTokens     int64
	usagePresent    bool
	textItemID      string
	textStarted     bool
	textOutputIndex int
}

func (agg *responsesAggregator) startTextEvents() []map[string]any {
	if agg.textStarted {
		return nil
	}
	agg.textStarted = true
	agg.textItemID = "msg_" + strings.TrimPrefix(agg.responseID, "resp_")
	agg.textOutputIndex = len(agg.output)
	item := map[string]any{
		"id": agg.textItemID, "type": "message", "status": "in_progress", "role": "assistant", "content": []any{},
	}
	// Reserve the text output slot when its first delta is emitted so subsequent
	// items use indices consistent with both the SSE stream and final output.
	agg.output = append(agg.output, item)
	return []map[string]any{
		{"type": "response.output_item.added", "response_id": agg.responseID, "output_index": agg.textOutputIndex, "item": item},
		{"type": "response.content_part.added", "response_id": agg.responseID, "output_index": agg.textOutputIndex, "content_index": 0, "part": map[string]any{"type": "output_text", "text": "", "annotations": []any{}}},
	}
}

func (agg *responsesAggregator) finishTextEvents() []map[string]any {
	if !agg.textStarted {
		return nil
	}
	item := map[string]any{
		"id": agg.textItemID, "type": "message", "status": "completed", "role": "assistant",
		"content": []map[string]any{{"type": "output_text", "text": agg.text.String(), "annotations": []any{}}},
	}
	agg.output[agg.textOutputIndex] = item
	return []map[string]any{
		{"type": "response.content_part.done", "response_id": agg.responseID, "output_index": agg.textOutputIndex, "content_index": 0, "part": item["content"].([]map[string]any)[0]},
		{"type": "response.output_item.done", "response_id": agg.responseID, "output_index": agg.textOutputIndex, "item": item},
	}
}
func newResponsesAggregator(responseID, publicModel string) *responsesAggregator {
	return &responsesAggregator{
		responseID: responseID, publicModel: publicModel, createdAt: time.Now().Unix(),
		toolCallIDs: make(map[string]struct{}), textOutputIndex: -1,
	}
}

func (agg *responsesAggregator) response(status string, responseError any) map[string]any {
	usage := map[string]any{
		"input_tokens":          agg.inputTokens,
		"input_tokens_details":  map[string]any{"cached_tokens": agg.cacheTokens},
		"output_tokens":         agg.outputTokens,
		"output_tokens_details": map[string]any{"reasoning_tokens": 0},
		"total_tokens":          agg.inputTokens + agg.outputTokens,
	}
	response := map[string]any{
		"id": agg.responseID, "object": "response", "created_at": agg.createdAt,
		"status": status, "model": agg.publicModel, "output": agg.completedOutput(),
		"parallel_tool_calls": true, "usage": usage,
	}
	if responseError != nil {
		response["error"] = responseError
	}
	return response
}

func (agg *responsesAggregator) completedOutput() []map[string]any {
	return append([]map[string]any(nil), agg.output...)
}

func (agg *responsesAggregator) consume(event modeladapter.ModelEvent) (map[string]any, error) {
	if event.UsagePresent {
		agg.usagePresent = true
		agg.inputTokens = event.InputTokens
		agg.outputTokens = event.OutputTokens
		agg.cacheTokens = event.CacheReadTokens
	}
	switch event.Kind {
	case modeladapter.ModelEventKindTextDelta:
		agg.text.WriteString(event.Text)
		if event.Text != "" {
			return map[string]any{"type": "response.output_text.delta", "response_id": agg.responseID, "output_index": agg.textOutputIndex, "content_index": 0, "delta": event.Text}, nil
		}
	case modeladapter.ModelEventKindThinkingDelta:
		if event.Text != "" {
			return map[string]any{"type": "response.reasoning_summary_text.delta", "response_id": agg.responseID, "output_index": len(agg.output), "summary_index": 0, "delta": event.Text}, nil
		}
	case modeladapter.ModelEventKindThinkingCompleted:
		if strings.TrimSpace(event.ThinkingSignature) != "" {
			item := map[string]any{
				"id":   firstNonEmpty(strings.TrimSpace(event.ProviderItemID), "rs_"+randomID(8)),
				"type": "reasoning", "status": firstNonEmpty(strings.TrimSpace(event.ProviderStatus), "completed"),
				"encrypted_content": strings.TrimSpace(event.ThinkingSignature), "summary": []any{},
			}
			if len(event.ProviderSummary) > 0 {
				item["summary"] = json.RawMessage(append([]byte(nil), event.ProviderSummary...))
			}
			agg.output = append(agg.output, item)
			return map[string]any{"type": "response.output_item.done", "response_id": agg.responseID, "output_index": len(agg.output) - 1, "item": item}, nil
		}
	case modeladapter.ModelEventKindToolLikeCompleted:
		invocation := event.ToolInvocation
		if invocation == nil || strings.TrimSpace(invocation.ToolName) == "" {
			return nil, errors.New("provider 返回了无效工具调用")
		}
		callID := firstNonEmpty(strings.TrimSpace(invocation.ProviderCallID), strings.TrimSpace(invocation.CallID))
		if callID == "" {
			return nil, errors.New("provider 返回了缺少 call_id 的工具调用")
		}
		if _, exists := agg.toolCallIDs[callID]; exists {
			return nil, nil
		}
		agg.toolCallIDs[callID] = struct{}{}
		item := map[string]any{
			"id":   firstNonEmpty(strings.TrimSpace(invocation.ProviderItemID), "fc_"+randomID(8)),
			"type": "function_call", "status": firstNonEmpty(strings.TrimSpace(invocation.ProviderStatus), "completed"),
			"call_id": callID, "name": strings.TrimSpace(invocation.ToolName), "arguments": string(invocation.ArgsJSON),
		}
		agg.output = append(agg.output, item)
		return map[string]any{"type": "response.output_item.done", "response_id": agg.responseID, "output_index": len(agg.output) - 1, "item": item}, nil
	}
	return nil, nil
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return strings.TrimSpace(value)
		}
	}
	return ""
}

func (server *Server) handleResponses(writer http.ResponseWriter, request *http.Request) {
	if err := requireJSONContentType(request); err != nil {
		writeAPIError(writer, http.StatusUnsupportedMediaType, "invalid_request_error", "invalid_content_type", err.Error())
		return
	}
	if encoding := strings.TrimSpace(request.Header.Get("Content-Encoding")); encoding != "" && !strings.EqualFold(encoding, "identity") {
		writeAPIError(writer, http.StatusBadRequest, "invalid_request_error", "unsupported_encoding", "暂不支持压缩请求体")
		return
	}
	body, err := io.ReadAll(limitBody(request))
	if err != nil {
		writeAPIError(writer, http.StatusBadRequest, "invalid_request_error", "invalid_body", "请求体过大或无法读取")
		return
	}
	parsed, err := parseResponsesRequest(body)
	if err != nil {
		writeAPIError(writer, http.StatusBadRequest, "invalid_request_error", "unsupported_parameter", err.Error())
		return
	}
	observeGatewayRequest(request, "openai_responses", parsed.Model)
	cfg := server.currentConfig()
	target, stale, ok := serverconfig.ResolveGatewayPublicModel(cfg, parsed.Model)
	if !ok {
		writeAPIError(writer, http.StatusNotFound, "invalid_request_error", "model_not_found", "模型不存在或未配置公开别名")
		return
	}
	if stale {
		writeAPIError(writer, http.StatusBadRequest, "invalid_request_error", "mapping_invalid", "公开模型映射已失效，请重新选择目标适配器")
		return
	}
	if server.streamer == nil {
		writeAPIError(writer, http.StatusServiceUnavailable, "api_error", "provider_unavailable", "Gateway provider 未初始化")
		return
	}
	responseID := "resp_" + randomID(12)
	conversationID := parsed.ConversationID
	if conversationID == "" {
		conversationID = "gateway-" + randomID(8)
	}
	correlation := observability.CorrelationFromContext(request.Context())
	correlation.HTTPRequestID = responseID
	correlation.ModelCallID = responseID
	correlation.ConversationID = conversationID
	providerContext := observability.WithCorrelation(request.Context(), correlation)
	providerReq := forwarder.ProviderRequest{
		RequestID: responseID, ConversationID: conversationID, RunID: responseID, ModelCallID: responseID,
		ModelID: target, ThinkingEffort: parsed.ThinkingEffort, Messages: parsed.Messages, Tools: parsed.Tools,
		RequestKnobs: parsed.RequestKnobs, MaxTokens: parsed.MaxTokens,
		CompileSummary: fmt.Sprintf("gateway responses public_model=%s", parsed.Model),
	}
	server.streamResponses(providerContext, writer, responseID, parsed.Model, providerReq)
}

func (server *Server) streamResponses(ctx context.Context, writer http.ResponseWriter, responseID, publicModel string, req forwarder.ProviderRequest) {
	flusher, _ := writer.(http.Flusher)
	writer.Header().Set("Content-Type", "text/event-stream")
	writer.Header().Set("Cache-Control", "no-cache")
	writer.Header().Set("Connection", "keep-alive")
	writer.WriteHeader(http.StatusOK)
	if flusher != nil {
		flusher.Flush()
	}
	agg := newResponsesAggregator(responseID, publicModel)
	_ = writeSSE(writer, flusher, map[string]any{"type": "response.created", "response": agg.response("in_progress", nil)})
	err := server.streamer.StartStream(ctx, req, func(event modeladapter.ModelEvent) error {
		if event.Kind == modeladapter.ModelEventKindTextDelta && event.Text != "" {
			for _, payload := range agg.startTextEvents() {
				if err := writeSSE(writer, flusher, payload); err != nil {
					return err
				}
			}
		}
		payload, err := agg.consume(event)
		if err != nil || payload == nil {
			return err
		}
		return writeSSE(writer, flusher, payload)
	})
	if err != nil {
		code := "provider_error"
		message := "上游模型请求失败"
		if errors.Is(err, context.Canceled) || errors.Is(ctx.Err(), context.Canceled) {
			code = "cancelled"
			message = "请求已取消"
		}
		logger.Errorf("gateway responses stream failed public_model=%s err=%v", publicModel, err)
		failure := apiError{Message: message, Type: "api_error", Code: code}
		_ = writeSSE(writer, flusher, map[string]any{"type": "response.failed", "response": agg.response("failed", failure)})
		return
	}
	for _, payload := range agg.finishTextEvents() {
		_ = writeSSE(writer, flusher, payload)
	}
	_ = writeSSE(writer, flusher, map[string]any{"type": "response.completed", "response": agg.response("completed", nil)})
}
