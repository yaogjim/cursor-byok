package forwarder

import (
	"testing"

	modeladapter "cursor/internal/backend/agent/model"
)

func replayToolCall(id string, name string) modeladapter.ToolCallDescriptor {
	return modeladapter.ToolCallDescriptor{
		ID:   id,
		Type: "function",
		Function: modeladapter.ToolCallFunctionShape{
			Name:      name,
			Arguments: "{}",
		},
	}
}

// 模型在同一条响应里先输出 function_call 再输出说明文本时，
// 历史回放顺序为 assistant[tool_call] -> assistant[text] -> tool[result]。
// trimReplayDanglingAssistantToolCalls 需要越过中间的文本消息收集工具结果。
func TestTrimReplayDanglingAssistantToolCallsKeepsInterleavedTextResponses(t *testing.T) {
	messages := []modeladapter.Message{
		{Role: "user", Content: "query"},
		{Role: "assistant", ToolCalls: []modeladapter.ToolCallDescriptor{replayToolCall("call_1", "Grep")}},
		{Role: "assistant", Content: "我先快速定位上下文"},
		{Role: "tool", Name: "Grep", ToolCallID: "call_1", Content: "grep result"},
		{Role: "user", Content: "next"},
	}

	trimmed := trimReplayDanglingAssistantToolCalls(messages)
	if len(trimmed) != 5 {
		t.Fatalf("expected 5 messages, got %d: %+v", len(trimmed), trimmed)
	}
	if len(trimmed[1].ToolCalls) != 1 || trimmed[1].ToolCalls[0].ID != "call_1" {
		t.Fatalf("expected assistant tool call call_1 to survive, got %+v", trimmed[1].ToolCalls)
	}
	if trimmed[3].Role != "tool" || trimmed[3].ToolCallID != "call_1" {
		t.Fatalf("expected tool result call_1 to survive, got %+v", trimmed[3])
	}
}

// 没有任何结果回放的调用仍应被剥离；被剥离调用对应的 tool 结果（若存在）也不得保留。
func TestTrimReplayDanglingAssistantToolCallsDropsUnrespondedCallsAndOrphanResults(t *testing.T) {
	messages := []modeladapter.Message{
		{Role: "user", Content: "query"},
		{Role: "assistant", ToolCalls: []modeladapter.ToolCallDescriptor{
			replayToolCall("call_1", "Grep"),
			replayToolCall("call_2", "Read"),
		}},
		{Role: "tool", Name: "Grep", ToolCallID: "call_1", Content: "grep result"},
		{Role: "tool", Name: "Read", ToolCallID: "call_3", Content: "orphan result"},
		{Role: "user", Content: "next"},
	}

	trimmed := trimReplayDanglingAssistantToolCalls(messages)
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

// 调用全部悬空但消息携带可回放 reasoning 时，保留为无调用的 assistant 消息。
func TestTrimReplayDanglingAssistantToolCallsKeepsReasoningOnlyShell(t *testing.T) {
	messages := []modeladapter.Message{
		{Role: "user", Content: "query"},
		{
			Role:                     "assistant",
			ToolCalls:                []modeladapter.ToolCallDescriptor{replayToolCall("call_1", "Grep")},
			ReasoningSignature:       "sig",
			ReasoningSignatureSource: modeladapter.ReasoningSignatureSourceOpenAIResponses,
		},
		{Role: "user", Content: "next"},
	}

	trimmed := trimReplayDanglingAssistantToolCalls(messages)
	if len(trimmed) != 3 {
		t.Fatalf("expected 3 messages, got %d: %+v", len(trimmed), trimmed)
	}
	if len(trimmed[1].ToolCalls) != 0 {
		t.Fatalf("expected tool calls to be trimmed, got %+v", trimmed[1].ToolCalls)
	}
}
