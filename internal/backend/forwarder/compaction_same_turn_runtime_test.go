package forwarder

import (
	"context"
	"encoding/json"
	"fmt"
	"reflect"
	"strings"
	"testing"
	"time"

	"cursor/gen/agentv1"
	modeladapter "cursor/internal/backend/agent/model"
)

func TestSameTurnCompactionBudgetControls(t *testing.T) {
	compiler := NewPromptCompiler(NewHistoryProjector(), NewToolCatalog(), NewReminderInjector(), nil)
	for _, oversizedInput := range []bool{false, true} {
		t.Run(fmt.Sprintf("oversizedInput=%v", oversizedInput), func(t *testing.T) {
			text := "有限的当前输入"
			if oversizedInput {
				text = strings.Repeat("大", 100_000)
			}
			conversation := &ConversationFile{Mode: "agent", TokenDetailsMaxTokens: 50_000, NextEntrySeq: 1}
			appendEntriesInPlace(conversation, []HistoryEntry{compactionTestUserEntry(t, 1, "request", text, "message")})
			compiled, err := compiler.Compile(conversation, agentv1.AgentMode_AGENT_MODE_AGENT, text, "gpt-6.1-sol")
			if err != nil {
				t.Fatal(err)
			}
			before := cloneFrozenHistoryEntries(conversation.Entries)
			plan, err := (&Service{}).buildAutoCompactionPlan(&ActiveStream{RequestID: "request", TurnSeq: 1, LatestUserText: text}, conversation, compiled)
			if plan != nil || (oversizedInput && err == nil) || (!oversizedInput && err != nil) {
				t.Fatalf("预算对照错误: oversized=%v tokens=%d plan=%v err=%v", oversizedInput, estimateCompiledPromptTokens(compiled), plan, err)
			}
			if !reflect.DeepEqual(before, conversation.Entries) {
				t.Fatal("预算受阻改写了输入历史")
			}
		})
	}
}

func TestSameTurnCompactionRejectsNonReducingCandidate(t *testing.T) {
	conversation := compactionAppendOnlyConversation(t)
	appendSameTurnBatches(t, conversation, "small", 3)
	compiler := NewPromptCompiler(NewHistoryProjector(), NewToolCatalog(), NewReminderInjector(), nil)
	stream := &ActiveStream{RequestID: "request-2", TurnSeq: 2, ConversationID: conversation.ConversationID, Mode: agentv1.AgentMode_AGENT_MODE_AGENT, CheckpointConversation: conversation}
	compiled, err := compiler.Compile(conversation, stream.Mode, "second question", "gpt-6.1-sol")
	if err != nil {
		t.Fatal(err)
	}
	plan := sameTurnLayoutPlan(t, conversation)
	plan.CompiledTokensBefore = estimateCompiledPromptTokens(guardCompiledConversationForProvider(compiled))
	before := cloneFrozenHistoryEntries(conversation.Entries)
	service := &Service{compiler: compiler}
	err = service.applyCompactionPlan(stream, conversation.ConversationID, plan, strings.Repeat("增", compactionSummaryMaxChars))
	if err == nil || !strings.Contains(err.Error(), "did not reduce") {
		t.Fatalf("没有拒绝不缩减的正常压缩: %v", err)
	}
	if !reflect.DeepEqual(before, stream.CheckpointConversation.Entries) {
		t.Fatal("不缩减候选被提交")
	}
}

// 这里直接驱动摘要状态机验证第二/第三次取消；成功续接证据由独立的正式 HTTP 入口测试提供。
func TestSameTurnCompactionLaterCancelRejectsLateFallback(t *testing.T) {
	for _, ordinal := range []int{2, 3} {
		t.Run(fmt.Sprint(ordinal), func(t *testing.T) {
			service, stream := testOverflowRecoveryStream(t)
			var nextPlan *compactionPlan
			for cycle := 0; cycle < ordinal; cycle++ {
				for batch := 0; batch < 3; batch++ {
					id := fmt.Sprintf("cancel-%d-%d", cycle, batch)
					if _, err := service.appendConversationEntries(stream, stream.ConversationID, []HistoryEntry{
						tailReadToolCallEntry(t, stream.TurnSeq, stream.RequestID, id, id),
						tailReadToolResultEntry(t, stream.TurnSeq, stream.RequestID, id, id, "/tmp/isolated", "已完成结果"),
					}); err != nil {
						t.Fatal(err)
					}
				}
				conversation, _, _, err := service.snapshotCheckpointConversation(stream)
				if err != nil {
					t.Fatal(err)
				}
				nextPlan, err = service.buildAutoCompactionPlanFromHistory(&compactionPlan{Trigger: "auto", CurrentTurnSeq: stream.TurnSeq, CurrentRequestID: stream.RequestID, PreserveCurrentTurnInputs: true, ContextWindowSize: 128_000, ExistingSummary: existingConversationSummaryText(conversation)}, conversation)
				if err != nil || nextPlan == nil {
					t.Fatalf("准备第%d次摘要失败: %v", cycle+1, err)
				}
				if cycle < ordinal-1 {
					if err := service.applyCompactionPlan(stream, stream.ConversationID, newPendingCompaction(nextPlan), "前次已提交摘要"); err != nil {
						t.Fatal(err)
					}
				}
			}
			started, finished := make(chan struct{}), make(chan struct{})
			service.provider = &overflowRecoveryTestProvider{handler: func(ctx context.Context, _ ProviderRequest, _ func(modeladapter.ModelEvent) error) error {
				close(started)
				<-ctx.Done()
				defer close(finished)
				return &modeladapter.HTTPStatusError{StatusCode: 503}
			}}
			if err := service.beginPendingCompaction(stream, nextPlan); err != nil {
				t.Fatal(err)
			}
			completeOverflowPreCompactHook(t, service, stream)
			select {
			case <-started:
			case <-time.After(5 * time.Second):
				t.Fatal("后续摘要未开始")
			}
			if err := service.handleCancelIntent(InboundIntent{RequestID: stream.RequestID, CancelReason: "用户取消"}); err != nil {
				t.Fatal(err)
			}
			select {
			case <-finished:
			case <-time.After(5 * time.Second):
				t.Fatal("摘要没有收到取消")
			}
			_, done, err := service.ensureStreamActor(stream)
			if err != nil {
				t.Fatal(err)
			}
			select {
			case <-done:
			case <-time.After(5 * time.Second):
				t.Fatal("取消后状态机未停止")
			}
			stream.mu.Lock()
			status := stream.Status
			count := countHistoryKind(stream.CheckpointConversation.Entries, "compacted_summary")
			stream.mu.Unlock()
			if status != StreamStatusCanceled || count != ordinal-1 {
				t.Fatalf("迟到结果提交了摘要或复活请求: status=%s count=%d", status, count)
			}
		})
	}
}

func TestSameTurnCompactionDecisionProductionApply(t *testing.T) {
	service, _, stream, capture := compactionDiagnosticTestStream(t)
	conversation := compactionAppendOnlyConversation(t)
	appendSameTurnBatches(t, conversation, "diagnostic", 3)
	stream.TurnSeq, stream.RequestID, stream.ConversationID = 2, "request-2", conversation.ConversationID
	stream.Mode, stream.CheckpointConversation = agentv1.AgentMode_AGENT_MODE_AGENT, conversation
	service.compiler = NewPromptCompiler(NewHistoryProjector(), NewToolCatalog(), NewReminderInjector(), nil)
	plan := sameTurnLayoutPlan(t, conversation)
	if err := service.applyCompactionPlan(stream, conversation.ConversationID, plan, "简短摘要"); err != nil {
		t.Fatal(err)
	}
	found := false
	for _, item := range capture.captures {
		if item.Event.Event != compactionDiagnosticEventApplied {
			continue
		}
		found = true
		if readInt64Value(item.Event.Fields["prefix_end"]) <= 0 || readInt64Value(item.Event.Fields["compiled_tokens_after"]) <= 0 || readInt64Value(item.Event.Fields["compaction_layout_version"]) != 1 {
			t.Fatalf("生产提交诊断缺少实际布局/预算: %v", item.Event.Fields)
		}
		encoded, _ := json.Marshal(item)
		if strings.Contains(string(encoded), "second question") || strings.Contains(string(encoded), "简短摘要") || strings.Contains(string(encoded), "原始结果") {
			t.Fatal("生产诊断泄漏正文")
		}
	}
	if !found {
		t.Fatal("实际提交未产生基础诊断")
	}
}

func TestSameTurnCompactionManualKeepsExistingBudgetContract(t *testing.T) {
	conversation := compactionAppendOnlyConversation(t)
	compiler := NewPromptCompiler(NewHistoryProjector(), NewToolCatalog(), NewReminderInjector(), nil)
	service := &Service{compiler: compiler}
	stream := &ActiveStream{RequestID: "request-2", TurnSeq: 2, ConversationID: conversation.ConversationID, Mode: agentv1.AgentMode_AGENT_MODE_AGENT, CheckpointConversation: conversation}
	compiled, err := compiler.Compile(conversation, stream.Mode, "second question", "gpt-6.1-sol")
	if err != nil {
		t.Fatal(err)
	}
	plan, err := service.buildManualCompactionPlan(stream, conversation, compiled, "手动整理")
	if err != nil || plan == nil || plan.Layout == nil {
		t.Fatalf("正式手动计划失败: plan=%v err=%v", plan, err)
	}
	// 模拟正式入口写入的 before；用户主动摘要只要求预算合格，不能冒充上下文溢出。
	plan.CompiledTokensBefore = estimateCompiledPromptTokens(guardCompiledConversationForProvider(compiled))
	if err := service.applyCompactionPlan(stream, conversation.ConversationID, newPendingCompaction(plan), strings.Repeat("中", compactionSummaryMaxChars)); err != nil {
		t.Fatalf("正常预算内手动压缩被自动 no-shrink 规则误杀: %v", err)
	}
	if countHistoryKind(stream.CheckpointConversation.Entries, "compacted_summary") != 1 {
		t.Fatal("手动摘要未提交")
	}
}
