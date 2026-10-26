package forwarder

import (
	"fmt"
	"reflect"
	"strings"
	"testing"

	"cursor/gen/agentv1"
)

// 先真实提交一次摘要，再在同一请求、同一回合积累执行记录。
// 每轮必须由默认编译器确认超预算，不能靠伪造上游用量触发。
func TestSameTurnCompactionRepeatedBudget(t *testing.T) {
	for _, window := range []uint32{50_000, 270_000} {
		t.Run(fmt.Sprint(window), func(t *testing.T) {
			conversation := compactionAppendOnlyConversation(t)
			conversation.TokenDetailsMaxTokens = window
			conversation.TokenDetailsUsedTokens = 0
			service := &Service{}
			compiler := NewPromptCompiler(NewHistoryProjector(), NewToolCatalog(), NewReminderInjector(), nil)
			stream := &ActiveStream{ConversationID: conversation.ConversationID, RequestID: "request-2", TurnSeq: 2, LatestUserText: "second question", Mode: agentv1.AgentMode_AGENT_MODE_AGENT}
			first, err := service.buildAutoCompactionPlanFromHistory(&compactionPlan{
				Trigger: "auto", CurrentTurnSeq: stream.TurnSeq, CurrentRequestID: stream.RequestID,
				CurrentUserText: stream.LatestUserText, PreserveCurrentTurnInputs: true,
				ContextWindowSize: int64(window), IsFirstCompaction: true,
			}, conversation)
			if err != nil || first == nil {
				t.Fatalf("首次计划失败: plan=%v err=%v", first, err)
			}
			if err := applyCompactionToConversation(conversation, newPendingCompaction(first), "首次已完成历史摘要"); err != nil {
				t.Fatal(err)
			}
			previousEnd := first.FrozenPrefixEnd
			nextBatch := 0
			for cycle := 0; cycle < 4; cycle++ {
				var compiled CompiledConversation
				for added := 0; ; added++ {
					compiled, err = compiler.Compile(conversation, stream.Mode, stream.LatestUserText, "gpt-6.1-sol")
					if err != nil {
						t.Fatal(err)
					}
					if estimateCompiledPromptTokens(compiled) > compactionBudgetTokens(int64(window)) {
						break
					}
					if added >= 500 {
						t.Fatal("夹具未能通过默认编译器触达超预算")
					}
					nextBatch++
					id := fmt.Sprintf("batch-%d", nextBatch)
					appendEntriesInPlace(conversation, []HistoryEntry{
						tailReadToolCallEntry(t, 2, stream.RequestID, id, id),
						tailReadToolResultEntry(t, 2, stream.RequestID, id, id, "/tmp/isolated.txt", id+strings.Repeat("执行结果", 1024)),
					})
				}
				before := cloneFrozenHistoryEntries(conversation.Entries)
				beforeTokens := estimateCompiledPromptTokens(compiled)
				plan, err := service.buildAutoCompactionPlan(stream, conversation, compiled)
				if err != nil || plan == nil {
					t.Fatalf("同回合第%d次再次压缩失败: estimated=%d budget=%d plan=%v err=%v", cycle+1, beforeTokens, compactionBudgetTokens(int64(window)), plan, err)
				}
				if plan.FrozenPrefixEnd <= previousEnd || plan.MessagesToCompact == 0 {
					t.Fatalf("压缩范围未推进: previous=%d new=[%d,%d)", previousEnd, plan.FrozenPrefixStart, plan.FrozenPrefixEnd)
				}
				if err := applyCompactionToConversation(conversation, newPendingCompaction(plan), fmt.Sprintf("第%d轮执行摘要", cycle+1)); err != nil {
					t.Fatal(err)
				}
				if !reflect.DeepEqual(before, conversation.Entries[:len(before)]) {
					t.Fatal("压缩改写原始历史")
				}
				after, err := compiler.Compile(conversation, stream.Mode, stream.LatestUserText, "gpt-6.1-sol")
				if err != nil {
					t.Fatal(err)
				}
				afterTokens := estimateCompiledPromptTokens(after)
				if afterTokens >= beforeTokens || afterTokens > compactionBudgetTokens(int64(window)) {
					t.Fatalf("压缩后预算不合格: before=%d after=%d", beforeTokens, afterTokens)
				}
				projected := compactedPromptProjectionEntries(conversation.Entries)
				if countHistoryKind(projected, "user_message") != 1 || countHistoryKind(projected, "tool_call") != 1 || countHistoryKind(projected, "tool_result") != 1 {
					t.Fatalf("输入或近期工具关系错误: %v", projectedPromptEntryKinds(projected))
				}
				previousEnd = plan.FrozenPrefixEnd
			}
		})
	}
}
