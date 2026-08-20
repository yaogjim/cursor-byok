package modeladapter

import "testing"

// 历史损坏时（旧版本回放逻辑剥离了 assistant 调用但保留了结果），
// function_call_output 会缺少配对的 function_call，Responses API 会拒绝。
// 这里验证 adapter 会为孤儿结果补一个占位 function_call，让旧会话可以继续。
func TestNormalizeOpenAIResponsesInputSynthesizesCallForOrphanToolOutput(t *testing.T) {
	messages := []Message{
		{Role: "user", Content: "query"},
		{Role: "assistant", Content: "我先快速定位上下文"},
		{Role: "tool", Name: "Grep", ToolCallID: "call_xrN6", Content: "grep result"},
		{Role: "user", Content: "next"},
	}

	_, items, err := normalizeOpenAIResponsesInput(messages)
	if err != nil {
		t.Fatalf("normalizeOpenAIResponsesInput failed: %v", err)
	}

	var callIndexes []int
	var outputIndexes []int
	for index, item := range items {
		if item["type"] == "function_call" && item["call_id"] == "call_xrN6" {
			callIndexes = append(callIndexes, index)
		}
		if item["type"] == "function_call_output" && item["call_id"] == "call_xrN6" {
			outputIndexes = append(outputIndexes, index)
		}
	}
	if len(callIndexes) != 1 {
		t.Fatalf("expected 1 synthesized function_call, got %d: %+v", len(callIndexes), items)
	}
	if len(outputIndexes) != 1 || outputIndexes[0] != callIndexes[0]+1 {
		t.Fatalf("expected function_call_output right after synthesized function_call, calls=%v outputs=%v", callIndexes, outputIndexes)
	}
	if got := items[callIndexes[0]]["name"]; got != "Grep" {
		t.Fatalf("expected synthesized function_call name Grep, got %v", got)
	}
}

// 连工具名都没有的孤儿结果只能丢弃，避免上游 400。
func TestNormalizeOpenAIResponsesInputDropsNamelessOrphanToolOutput(t *testing.T) {
	messages := []Message{
		{Role: "user", Content: "query"},
		{Role: "tool", ToolCallID: "call_unknown", Content: "orphan result"},
		{Role: "user", Content: "next"},
	}

	_, items, err := normalizeOpenAIResponsesInput(messages)
	if err != nil {
		t.Fatalf("normalizeOpenAIResponsesInput failed: %v", err)
	}
	for _, item := range items {
		if item["type"] == "function_call_output" {
			t.Fatalf("expected orphan function_call_output to be dropped, got %+v", item)
		}
	}
}

// 正常配对的调用与结果不应受防御逻辑影响。
func TestNormalizeOpenAIResponsesInputKeepsPairedCallAndOutput(t *testing.T) {
	messages := []Message{
		{Role: "user", Content: "query"},
		{
			Role: "assistant",
			ToolCalls: []ToolCallDescriptor{{
				ID:   "call_xrN6",
				Type: "function",
				Function: ToolCallFunctionShape{
					Name:      "Grep",
					Arguments: `{"pattern":"ref"}`,
				},
			}},
		},
		{Role: "tool", Name: "Grep", ToolCallID: "call_xrN6", Content: "grep result"},
		{Role: "user", Content: "next"},
	}

	_, items, err := normalizeOpenAIResponsesInput(messages)
	if err != nil {
		t.Fatalf("normalizeOpenAIResponsesInput failed: %v", err)
	}
	var calls, outputs int
	for _, item := range items {
		if item["type"] == "function_call" && item["call_id"] == "call_xrN6" {
			calls++
		}
		if item["type"] == "function_call_output" && item["call_id"] == "call_xrN6" {
			outputs++
		}
	}
	if calls != 1 || outputs != 1 {
		t.Fatalf("expected exactly one paired function_call/output, got calls=%d outputs=%d", calls, outputs)
	}
}
