package gateway

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	modeladapter "cursor/internal/backend/agent/model"
)

type parsedChatRequest struct {
	Model        string
	Messages     []modeladapter.Message
	Tools        []json.RawMessage
	RequestKnobs map[string]any
	Stream       bool
	MaxTokens    int
}

var unsupportedRequestKeys = []string{
	"functions",
	"function_call",
	"reasoning",
	"reasoning_effort",
	"thinking",
	"thinking_budget",
	"modalities",
	"audio",
	"prediction",
}

func parseChatCompletionRequest(body []byte) (parsedChatRequest, error) {
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(body, &raw); err != nil {
		return parsedChatRequest{}, errors.New("请求体必须是 JSON 对象")
	}
	for _, key := range unsupportedRequestKeys {
		if value, ok := raw[key]; ok && !isJSONNull(value) {
			return parsedChatRequest{}, fmt.Errorf("暂不支持 %s", key)
		}
	}

	model, err := jsonString(raw["model"])
	if err != nil || strings.TrimSpace(model) == "" {
		return parsedChatRequest{}, errors.New("model 不能为空")
	}
	messagesRaw, ok := raw["messages"]
	if !ok {
		return parsedChatRequest{}, errors.New("messages 不能为空")
	}
	var messagePayloads []json.RawMessage
	if err := json.Unmarshal(messagesRaw, &messagePayloads); err != nil {
		return parsedChatRequest{}, errors.New("messages 必须是数组")
	}
	if len(messagePayloads) == 0 {
		return parsedChatRequest{}, errors.New("messages 不能为空")
	}
	messages := make([]modeladapter.Message, 0, len(messagePayloads))
	for _, payload := range messagePayloads {
		message, err := parseChatMessage(payload)
		if err != nil {
			return parsedChatRequest{}, err
		}
		messages = append(messages, message)
	}

	tools, err := parseChatTools(raw["tools"])
	if err != nil {
		return parsedChatRequest{}, err
	}
	if err := validateToolChoice(raw["tool_choice"]); err != nil {
		return parsedChatRequest{}, err
	}
	requestKnobs, err := parseChatRequestKnobs(raw)
	if err != nil {
		return parsedChatRequest{}, err
	}

	stream := false
	if value, ok := raw["stream"]; ok {
		if err := json.Unmarshal(value, &stream); err != nil {
			return parsedChatRequest{}, errors.New("stream 必须是布尔值")
		}
	}
	maxTokens := 0
	if value, ok := raw["max_tokens"]; ok && !isJSONAbsent(value) {
		if err := json.Unmarshal(value, &maxTokens); err != nil || maxTokens < 0 {
			return parsedChatRequest{}, errors.New("max_tokens 必须是非负整数")
		}
	}
	return parsedChatRequest{
		Model:        strings.TrimSpace(model),
		Messages:     messages,
		Tools:        tools,
		RequestKnobs: requestKnobs,
		Stream:       stream,
		MaxTokens:    maxTokens,
	}, nil
}

func parseChatRequestKnobs(raw map[string]json.RawMessage) (map[string]any, error) {
	output := make(map[string]any)
	for _, key := range []string{"temperature", "top_p"} {
		value, ok := raw[key]
		if !ok || isJSONNull(value) {
			continue
		}
		var number float64
		if err := json.Unmarshal(value, &number); err != nil {
			return nil, fmt.Errorf("%s 必须是数字", key)
		}
		output[key] = number
	}
	if value, ok := raw["parallel_tool_calls"]; ok && !isJSONNull(value) {
		var enabled bool
		if err := json.Unmarshal(value, &enabled); err != nil {
			return nil, errors.New("parallel_tool_calls 必须是布尔值")
		}
		output["parallel_tool_calls"] = enabled
	}
	return output, nil
}

func parseChatTools(raw json.RawMessage) ([]json.RawMessage, error) {
	if len(raw) == 0 || isJSONNull(raw) {
		return nil, nil
	}
	var tools []json.RawMessage
	if err := json.Unmarshal(raw, &tools); err != nil {
		return nil, errors.New("tools 必须是数组")
	}
	for _, toolRaw := range tools {
		var tool map[string]json.RawMessage
		if err := json.Unmarshal(toolRaw, &tool); err != nil {
			return nil, errors.New("tools[] 必须是对象")
		}
		toolType, err := jsonString(tool["type"])
		if err != nil || strings.TrimSpace(toolType) != "function" {
			return nil, errors.New("tools[] 仅支持 type=function")
		}
		var function map[string]json.RawMessage
		if err := json.Unmarshal(tool["function"], &function); err != nil {
			return nil, errors.New("tools[].function 必须是对象")
		}
		name, err := jsonString(function["name"])
		if err != nil || strings.TrimSpace(name) == "" {
			return nil, errors.New("tools[].function.name 不能为空")
		}
	}
	return append([]json.RawMessage(nil), tools...), nil
}

func validateToolChoice(raw json.RawMessage) error {
	if len(raw) == 0 || isJSONNull(raw) {
		return nil
	}
	choice, err := jsonString(raw)
	if err != nil || strings.TrimSpace(choice) != "auto" {
		return errors.New("tool_choice 仅支持 auto")
	}
	return nil
}

func parseChatMessage(raw json.RawMessage) (modeladapter.Message, error) {
	var payload map[string]json.RawMessage
	if err := json.Unmarshal(raw, &payload); err != nil {
		return modeladapter.Message{}, errors.New("message 必须是对象")
	}
	role, err := jsonString(payload["role"])
	if err != nil {
		return modeladapter.Message{}, errors.New("message.role 必须是字符串")
	}
	role = strings.ToLower(strings.TrimSpace(role))
	if role != "system" && role != "user" && role != "assistant" && role != "tool" {
		return modeladapter.Message{}, fmt.Errorf("不支持的 message.role %s", role)
	}

	message := modeladapter.Message{Role: role}
	if role == "assistant" {
		toolCalls, err := parseAssistantToolCalls(payload["tool_calls"])
		if err != nil {
			return modeladapter.Message{}, err
		}
		message.ToolCalls = toolCalls
	}
	if role == "tool" {
		callID, err := jsonString(payload["tool_call_id"])
		if err != nil || strings.TrimSpace(callID) == "" {
			return modeladapter.Message{}, errors.New("tool message.tool_call_id 不能为空")
		}
		message.ToolCallID = strings.TrimSpace(callID)
		if name, err := jsonString(payload["name"]); err == nil {
			message.Name = strings.TrimSpace(name)
		}
	}

	contentRaw, hasContent := payload["content"]
	if !hasContent || isJSONNull(contentRaw) {
		if role == "assistant" && len(message.ToolCalls) > 0 {
			return message, nil
		}
		return modeladapter.Message{}, errors.New("message.content 不能为空")
	}
	var asString string
	if err := json.Unmarshal(contentRaw, &asString); err == nil {
		message.Content = asString
		return message, nil
	}
	if role == "tool" {
		return modeladapter.Message{}, errors.New("tool message.content 必须是字符串")
	}
	var parts []json.RawMessage
	if err := json.Unmarshal(contentRaw, &parts); err != nil || len(parts) == 0 {
		return modeladapter.Message{}, errors.New("暂不支持多模态 content，仅允许纯文本")
	}
	var text strings.Builder
	for _, partRaw := range parts {
		var part map[string]json.RawMessage
		if err := json.Unmarshal(partRaw, &part); err != nil {
			return modeladapter.Message{}, errors.New("暂不支持多模态 content，仅允许纯文本")
		}
		partType, err := jsonString(part["type"])
		if err != nil || !strings.EqualFold(strings.TrimSpace(partType), "text") {
			return modeladapter.Message{}, errors.New("暂不支持多模态 content，仅允许纯文本")
		}
		partText, err := jsonString(part["text"])
		if err != nil {
			return modeladapter.Message{}, errors.New("文本 content.part.text 必须是字符串")
		}
		text.WriteString(partText)
	}
	message.Content = text.String()
	return message, nil
}

func parseAssistantToolCalls(raw json.RawMessage) ([]modeladapter.ToolCallDescriptor, error) {
	if len(raw) == 0 || isJSONNull(raw) {
		return nil, nil
	}
	var calls []struct {
		ID       string `json:"id"`
		Type     string `json:"type"`
		Function struct {
			Name      string `json:"name"`
			Arguments string `json:"arguments"`
		} `json:"function"`
	}
	if err := json.Unmarshal(raw, &calls); err != nil {
		return nil, errors.New("assistant tool_calls 必须是数组")
	}
	output := make([]modeladapter.ToolCallDescriptor, 0, len(calls))
	for index, call := range calls {
		callType := strings.TrimSpace(call.Type)
		if callType == "" {
			callType = "function"
		}
		if strings.TrimSpace(call.ID) == "" || callType != "function" || strings.TrimSpace(call.Function.Name) == "" {
			return nil, errors.New("assistant tool_calls[] 必须包含 id 和 function.name")
		}
		output = append(output, modeladapter.ToolCallDescriptor{
			ID:    strings.TrimSpace(call.ID),
			Index: index,
			Type:  "function",
			Function: modeladapter.ToolCallFunctionShape{
				Name:      strings.TrimSpace(call.Function.Name),
				Arguments: call.Function.Arguments,
			},
		})
	}
	return output, nil
}

func jsonString(raw json.RawMessage) (string, error) {
	if isJSONAbsent(raw) {
		return "", errors.New("missing")
	}
	var value string
	if err := json.Unmarshal(raw, &value); err != nil {
		return "", err
	}
	return value, nil
}

func isJSONAbsent(raw json.RawMessage) bool {
	text := strings.TrimSpace(string(raw))
	return text == "" || text == "null" || text == "[]" || text == "{}"
}

func isJSONNull(raw json.RawMessage) bool {
	return strings.TrimSpace(string(raw)) == "null"
}

type chatAggregator struct {
	text         strings.Builder
	toolCalls    []modeladapter.ToolCallDescriptor
	toolCallIDs  map[string]struct{}
	inputTokens  int64
	outputTokens int64
	usagePresent bool
	finish       string
}

func newChatAggregator() *chatAggregator {
	return &chatAggregator{finish: "stop", toolCallIDs: make(map[string]struct{})}
}

func (agg *chatAggregator) consume(event modeladapter.ModelEvent) error {
	if agg == nil {
		return nil
	}
	switch event.Kind {
	case modeladapter.ModelEventKindTextDelta:
		agg.text.WriteString(event.Text)
	case modeladapter.ModelEventKindPartialToolCall, modeladapter.ModelEventKindToolCallDelta:
		// 通用 Provider 只保证完成工具事件；Gateway 不伪造参数增量。
	case modeladapter.ModelEventKindToolLikeCompleted:
		invocation := event.ToolInvocation
		if invocation == nil || strings.TrimSpace(invocation.CallID) == "" || strings.TrimSpace(invocation.ToolName) == "" {
			return errors.New("provider 返回了无效工具调用")
		}
		callID := strings.TrimSpace(invocation.CallID)
		if _, exists := agg.toolCallIDs[callID]; exists {
			break
		}
		agg.toolCallIDs[callID] = struct{}{}
		agg.toolCalls = append(agg.toolCalls, modeladapter.ToolCallDescriptor{
			ID:    callID,
			Index: len(agg.toolCalls),
			Type:  "function",
			Function: modeladapter.ToolCallFunctionShape{
				Name:      strings.TrimSpace(invocation.ToolName),
				Arguments: string(invocation.ArgsJSON),
			},
		})
		agg.finish = "tool_calls"
	case modeladapter.ModelEventKindTurnFinished:
		if len(agg.toolCalls) == 0 && strings.TrimSpace(event.FinishReason) != "" {
			agg.finish = event.FinishReason
		}
	}
	if event.UsagePresent {
		agg.usagePresent = true
		if event.InputTokens > 0 {
			agg.inputTokens = event.InputTokens
		}
		if event.OutputTokens > 0 {
			agg.outputTokens = event.OutputTokens
		}
	}
	return nil
}

func (agg *chatAggregator) finishReason() string {
	if agg != nil && len(agg.toolCalls) > 0 {
		return "tool_calls"
	}
	if agg == nil || strings.TrimSpace(agg.finish) == "" {
		return "stop"
	}
	if strings.TrimSpace(agg.finish) == "tool_use" {
		return "tool_calls"
	}
	return agg.finish
}

func (agg *chatAggregator) usage() map[string]any {
	prompt := agg.inputTokens
	completion := agg.outputTokens
	return map[string]any{
		"prompt_tokens":     prompt,
		"completion_tokens": completion,
		"total_tokens":      prompt + completion,
	}
}

func (agg *chatAggregator) completion(id, model string) map[string]any {
	content := ""
	if agg != nil {
		content = agg.text.String()
	}
	message := map[string]any{
		"role":    "assistant",
		"content": content,
	}
	if agg != nil && len(agg.toolCalls) > 0 {
		message["tool_calls"] = openAIToolCalls(agg.toolCalls)
	}
	return map[string]any{
		"id":      id,
		"object":  "chat.completion",
		"created": time.Now().Unix(),
		"model":   model,
		"choices": []map[string]any{{
			"index":         0,
			"message":       message,
			"finish_reason": agg.finishReason(),
		}},
		"usage": agg.usage(),
	}
}

func openAIToolCalls(calls []modeladapter.ToolCallDescriptor) []map[string]any {
	output := make([]map[string]any, 0, len(calls))
	for _, call := range calls {
		output = append(output, map[string]any{
			"id":   call.ID,
			"type": "function",
			"function": map[string]any{
				"name":      call.Function.Name,
				"arguments": call.Function.Arguments,
			},
		})
	}
	return output
}

func (agg *chatAggregator) toolChunk(id, model string, call modeladapter.ToolCallDescriptor, includeRole bool) map[string]any {
	delta := map[string]any{
		"tool_calls": []map[string]any{{
			"index": call.Index,
			"id":    call.ID,
			"type":  "function",
			"function": map[string]any{
				"name":      call.Function.Name,
				"arguments": call.Function.Arguments,
			},
		}},
	}
	if includeRole {
		delta["role"] = "assistant"
	}
	return map[string]any{
		"id":      id,
		"object":  "chat.completion.chunk",
		"created": time.Now().Unix(),
		"model":   model,
		"choices": []map[string]any{{
			"index":         0,
			"delta":         delta,
			"finish_reason": nil,
		}},
	}
}

func (agg *chatAggregator) chunk(id, model, delta, finish string, includeRole bool) map[string]any {
	deltaPayload := map[string]any{}
	if includeRole {
		deltaPayload["role"] = "assistant"
	}
	if delta != "" {
		deltaPayload["content"] = delta
	}
	choice := map[string]any{
		"index": 0,
		"delta": deltaPayload,
	}
	payload := map[string]any{
		"id":      id,
		"object":  "chat.completion.chunk",
		"created": time.Now().Unix(),
		"model":   model,
		"choices": []map[string]any{choice},
	}
	if finish != "" {
		choice["finish_reason"] = finish
		payload["usage"] = agg.usage()
		payload["choices"] = []map[string]any{choice}
	} else {
		choice["finish_reason"] = nil
	}
	return payload
}
