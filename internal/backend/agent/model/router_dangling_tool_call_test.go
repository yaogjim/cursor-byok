package modeladapter

import "testing"

func routerToolCall(id string, name string) ToolCallDescriptor {
	return ToolCallDescriptor{
		ID:   id,
		Type: "function",
		Function: ToolCallFunctionShape{
			Name:      name,
			Arguments: `{"pattern":"ref"}`,
		},
	}
}

// 模型在同一条响应里先输出 function_call 再输出说明文本时，
// sanitize 后的消息序列为 assistant[tool_call] -> assistant[text] -> tool[result]。
// trimDanglingAssistantToolCalls 需要越过中间的文本消息收集工具结果，
// 否则会产生孤儿 function_call_output（Responses API 400）。
func TestTrimDanglingAssistantToolCallsKeepsInterleavedTextResponses(t *testing.T) {
	input := []Message{
		{Role: "user", Content: "query"},
		{
			Role: "assistant",
			ToolCalls: []ToolCallDescriptor{
				routerToolCall("call_1", "Grep"),
				routerToolCall("call_2", "Read"),
			},
		},
		{Role: "tool", Name: "Grep", ToolCallID: "call_1", Content: "grep result"},
		{Role: "assistant", Content: "我先快速定位上下文"},
		{Role: "tool", Name: "Read", ToolCallID: "call_2", Content: "read result"},
		{Role: "user", Content: "next"},
	}

	trimmed := sanitizeProviderMessages(input)
	if len(trimmed) != 6 {
		t.Fatalf("expected 6 messages, got %d: %+v", len(trimmed), trimmed)
	}
	if len(trimmed[1].ToolCalls) != 2 {
		t.Fatalf("expected both tool calls to survive, got %+v", trimmed[1].ToolCalls)
	}
	if trimmed[3].Role != "assistant" || trimmed[3].Content == "" {
		t.Fatalf("expected interleaved assistant text to survive, got %+v", trimmed[3])
	}
	if trimmed[4].ToolCallID != "call_2" {
		t.Fatalf("expected call_2 result to survive, got %+v", trimmed[4])
	}
}

// 完全没有结果回放的调用仍应被剥离，且孤儿 tool 结果不得保留。
func TestTrimDanglingAssistantToolCallsDropsUnrespondedCallsAndOrphanResults(t *testing.T) {
	input := []Message{
		{Role: "user", Content: "query"},
		{
			Role: "assistant",
			ToolCalls: []ToolCallDescriptor{
				routerToolCall("call_1", "Grep"),
				routerToolCall("call_2", "Read"),
			},
		},
		{Role: "tool", Name: "Grep", ToolCallID: "call_1", Content: "grep result"},
		{Role: "tool", Name: "Read", ToolCallID: "call_3", Content: "orphan result"},
		{Role: "user", Content: "next"},
	}

	trimmed := sanitizeProviderMessages(input)
	if len(trimmed) != 4 {
		t.Fatalf("expected 4 messages, got %d: %+v", len(trimmed), trimmed)
	}
	if len(trimmed[1].ToolCalls) != 1 || trimmed[1].ToolCalls[0].ID != "call_1" {
		t.Fatalf("expected only call_1 to survive, got %+v", trimmed[1].ToolCalls)
	}
	if trimmed[2].ToolCallID != "call_1" {
		t.Fatalf("expected only call_1 result to survive, got %+v", trimmed[2])
	}
}
