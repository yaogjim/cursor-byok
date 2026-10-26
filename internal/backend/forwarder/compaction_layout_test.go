package forwarder

import (
	"bytes"
	"encoding/json"
	"fmt"
	"reflect"
	"strings"
	"testing"

	modeladapter "cursor/internal/backend/agent/model"
)

func sameTurnLayoutPlan(t *testing.T, conversation *ConversationFile) *PendingCompaction {
	t.Helper()
	plan, err := (&Service{}).buildAutoCompactionPlanFromHistory(&compactionPlan{
		Trigger: "auto", CurrentTurnSeq: 2, CurrentRequestID: "request-2", CurrentUserText: "second question",
		PreserveCurrentTurnInputs: true, ContextWindowSize: 270_000,
		ExistingSummary: existingConversationSummaryText(conversation), IsFirstCompaction: latestCompactionSummaryIndex(conversation.Entries) < 0,
	}, conversation)
	if err != nil || plan == nil || plan.Layout == nil {
		t.Fatalf("新版压缩计划失败: plan=%v err=%v", plan, err)
	}
	return newPendingCompaction(plan)
}

func appendSameTurnBatches(t *testing.T, conversation *ConversationFile, prefix string, count int) {
	t.Helper()
	for i := 0; i < count; i++ {
		id := fmt.Sprintf("%s-%d", prefix, i)
		appendEntriesInPlace(conversation, []HistoryEntry{
			tailReadToolCallEntry(t, 2, "request-2", id, id),
			tailReadToolResultEntry(t, 2, "request-2", id, id, "/tmp/isolated.txt", "原始结果："+id),
		})
	}
}

func TestSameTurnLayoutStoreCheckpointAndContinue(t *testing.T) {
	for _, legacyFirst := range []bool{false, true} {
		t.Run(fmt.Sprintf("legacyFirst=%v", legacyFirst), func(t *testing.T) {
			conversation := compactionAppendOnlyConversation(t)
			if legacyFirst {
				// 真实旧格式，不给其补写新版字段。
				appendTailSummary(conversation, &PendingCompaction{Trigger: "auto", CurrentTurnSeq: 2, CurrentRequestID: "request-2", PreserveCurrentTurnInputs: true}, "旧版摘要")
			}
			store := NewConversationFileStore(t.TempDir())
			projector := NewHistoryProjector()
			persistedCount, previousEnd := 0, 0
			for cycle := 0; cycle < 4; cycle++ {
				appendSameTurnBatches(t, conversation, fmt.Sprint(cycle), 3)
				plan := sameTurnLayoutPlan(t, conversation)
				if plan.Layout.PrefixEnd <= previousEnd || (cycle > 0 && plan.Layout.PrefixStart != previousEnd) {
					t.Fatal("保存恢复后的覆盖范围没有持续前进")
				}
				if err := applyCompactionToConversation(conversation, plan, fmt.Sprintf("进度摘要%d", cycle)); err != nil {
					t.Fatal(err)
				}
				want, err := projector.ProjectPromptReplay(conversation)
				if err != nil {
					t.Fatal(err)
				}
				if _, err := store.SaveConversationWithEntries(conversation.ConversationID, conversation, conversation.Entries[persistedCount:]); err != nil {
					t.Fatal(err)
				}
				persistedCount = len(conversation.Entries)
				restored, err := store.LoadConversation(conversation.ConversationID)
				if err != nil {
					t.Fatal(err)
				}
				got, err := projector.ProjectPromptReplay(restored)
				if err != nil || !reflect.DeepEqual(want, got) {
					t.Fatalf("保存重读改变模型视图: err=%v", err)
				}
				for index, entry := range conversation.Entries {
					if isCompactionSummaryKind(entry.Kind) {
						continue // 仅重映射新摘要的序号引用及来源校验值。
					}
					wantRef, gotRef := compactionEntryRef(entry), compactionEntryRef(restored.Entries[index])
					gotRef.Seq = wantRef.Seq
					if wantRef != gotRef {
						t.Fatal("保存重读改写了原始输入或执行内容")
					}
				}
				// 默认检查点恢复活动视图，不把本地下标移植到重建历史。
				checkpoint, err := projector.ProjectCheckpointProjection(restored)
				if err != nil {
					t.Fatal(err)
				}
				messages, err := importedConversationStateModelMessages(checkpoint.State)
				if err != nil {
					t.Fatal(err)
				}
				// 既有检查点策略不在 ROOT 保存 Read 的瞬态执行结果；全历史仍在 Turns。
				// 本用例最新批次恰为一对 Read，因此 ROOT 应为摘要和完整用户输入。
				wantJSON, _ := json.Marshal(want[:2])
				gotJSON, _ := json.Marshal(messages)
				if !bytes.Equal(wantJSON, gotJSON) {
					t.Fatalf("默认检查点活动视图不一致:\nwant=%s\ngot=%s", wantJSON, gotJSON)
				}
				if !legacyFirst {
					// 独立原生写出暂不支持归档内仍活动的固定输入；默认检查点已在上面验证。
					native, err := projector.projectNativeCheckpointProjection(restored)
					if err != errNativeCheckpointPinnedInputsUnsupported || native != nil {
						t.Fatalf("带固定输入的原生写出未明确拒绝: %v", err)
					}
				}
				previousEnd = plan.Layout.PrefixEnd
				conversation = restored
			}
		})
	}
}

func TestSameTurnLayoutRejectsCorruption(t *testing.T) {
	original := compactionAppendOnlyConversation(t)
	appendSameTurnBatches(t, original, "batch", 3)
	if err := applyCompactionToConversation(original, sameTurnLayoutPlan(t, original), "进度摘要"); err != nil {
		t.Fatal(err)
	}
	for _, kind := range []string{"null", "missing_layout", "missing_field", "unknown_version", "outer_version", "zero_version_without_layout", "null_version_without_layout", "missing_outer_version", "inverted", "missing_input", "empty_inputs", "disabled_preservation", "disabled_with_inputs", "identity", "payload", "source_model_identity", "tail_overlap", "archive_mismatch"} {
		t.Run(kind, func(t *testing.T) {
			conversation := cloneConversationFile(original)
			summary := latestCompactionSummaryIndex(conversation.Entries)
			payload, _ := decodeCompactionSummaryPayload(conversation.Entries[summary].Payload)
			layout, _ := parseCompactionLayout(payload.Layout)
			switch kind {
			case "null":
				payload.Layout = json.RawMessage("null")
			case "missing_layout":
				payload.Layout = nil
			case "outer_version":
				payload.LayoutVersion++
			case "source_model_identity":
				conversation.Entries[4].ModelCallID = "different-model-call"
			case "missing_field":
				payload.Layout = json.RawMessage(`{"version":1}`)
			case "unknown_version":
				layout.Version++
			case "inverted":
				layout.PrefixStart = summary + 1
			case "missing_input":
				layout.PinnedInputs = nil
			case "empty_inputs":
				layout.PinnedInputs = []compactionEntryReference{}
			case "disabled_preservation":
				payload.PreserveCurrentTurnInputs = false
				layout.PinnedInputs = []compactionEntryReference{}
			case "disabled_with_inputs":
				payload.PreserveCurrentTurnInputs = false
			case "identity":
				layout.PinnedInputs[0].Seq = 999
			case "payload":
				conversation.Entries[2].Payload = json.RawMessage(`{"text":"篡改当前输入"}`)
			case "tail_overlap":
				layout.TailStart++
			case "archive_mismatch":
				var source compactionNativeArchiveSource
				_ = json.Unmarshal(payload.NativeArchiveSource, &source)
				source.PrefixEnd--
				payload.NativeArchiveSource, _ = json.Marshal(source)
			}
			if kind != "null" && kind != "missing_field" && kind != "missing_layout" {
				payload.Layout, _ = json.Marshal(layout)
			}
			conversation.Entries[summary].Payload, _ = json.Marshal(payload)
			if kind == "zero_version_without_layout" || kind == "null_version_without_layout" || kind == "missing_outer_version" {
				var fields map[string]json.RawMessage
				_ = json.Unmarshal(conversation.Entries[summary].Payload, &fields)
				if kind == "missing_outer_version" {
					delete(fields, "compaction_layout_version")
				} else {
					delete(fields, "compaction_layout")
					fields["compaction_layout_version"] = json.RawMessage("0")
					if kind == "null_version_without_layout" {
						fields["compaction_layout_version"] = json.RawMessage("null")
					}
				}
				conversation.Entries[summary].Payload, _ = json.Marshal(fields)
			}
			before, _ := json.Marshal(conversation)
			if _, err := NewHistoryProjector().ProjectPromptReplay(conversation); err == nil {
				t.Fatal("损坏记录静默退回旧投影")
			}
			if _, err := NewHistoryProjector().ProjectCheckpointProjection(conversation); err == nil {
				t.Fatal("损坏记录生成了默认检查点")
			}
			if _, err := NewHistoryProjector().projectNativeCheckpointProjection(conversation); err == nil {
				t.Fatal("损坏记录生成了原生检查点")
			}
			after, _ := json.Marshal(conversation)
			if !bytes.Equal(before, after) {
				t.Fatal("拒绝损坏布局时改写历史")
			}
		})
	}
}

func TestSameTurnLayoutDuplicateInputsAndFork(t *testing.T) {
	conversation := compactionAppendOnlyConversation(t)
	appendEntriesInPlace(conversation, []HistoryEntry{compactionTestUserEntry(t, 2, "request-2", "second question", "different-message-id")})
	appendSameTurnBatches(t, conversation, "before", 3)
	plan := sameTurnLayoutPlan(t, conversation)
	if len(plan.Layout.PinnedInputs) != 2 {
		t.Fatal("相同文本的不同消息身份被合并")
	}
	if err := applyCompactionToConversation(conversation, plan, "压缩后分叉点"); err != nil {
		t.Fatal(err)
	}
	fork := cloneConversationFile(conversation)
	fork.ConversationID = "isolated-fork"
	appendSameTurnBatches(t, conversation, "parent-only", 2)
	forkedMessages, err := NewHistoryProjector().ProjectPromptReplay(fork)
	if err != nil {
		t.Fatal(err)
	}
	encoded, _ := json.Marshal(forkedMessages)
	if bytes.Count(encoded, []byte("second question")) != 2 || bytes.Contains(encoded, []byte("parent-only")) {
		t.Fatal("分叉丢失重复输入身份或混入后续历史")
	}
	// 正式回退路径保留完整前缀时，重编号不能破坏布局。
	(&Service{}).applyRunRewindToConversation(fork, runRewindDecision{Apply: true, PrefixEntries: cloneFrozenHistoryEntries(fork.Entries)}, nil, InboundIntent{}, 3)
	if _, err := NewHistoryProjector().ProjectPromptReplay(fork); err != nil {
		t.Fatalf("完整前缀回退破坏布局: %v", err)
	}
	newPlan := sameTurnLayoutPlan(t, conversation)
	if newPlan.Layout.PrefixStart != plan.Layout.PrefixEnd || strings.Contains(fmt.Sprint(fork.Entries), "parent-only") {
		t.Fatal("分叉与父会话边界互相影响")
	}
}

func TestSameTurnLayoutLegacyToolsDoNotLockFutureCompaction(t *testing.T) {
	for _, interrupted := range []bool{false, true} {
		t.Run(fmt.Sprintf("interrupted=%v", interrupted), func(t *testing.T) {
			conversation := &ConversationFile{ConversationID: "legacy-tools", Mode: "agent", NextEntrySeq: 1}
			appendEntriesInPlace(conversation, []HistoryEntry{
				compactionTestUserEntry(t, 1, "old-request", "已结束旧回合", "old-message"),
				tailReadToolCallEntry(t, 1, "old-request", "legacy-call", ""),
			})
			if !interrupted {
				appendEntriesInPlace(conversation, []HistoryEntry{tailReadToolResultEntry(t, 1, "old-request", "legacy-call", "", "/tmp/old.txt", "旧工具已完成")})
			}
			appendEntriesInPlace(conversation, []HistoryEntry{compactionTestUserEntry(t, 2, "request-2", "second question", "current-message")})
			previousEnd := 0
			for cycle := 0; cycle < 4; cycle++ {
				appendSameTurnBatches(t, conversation, fmt.Sprint(cycle), 3)
				plan := sameTurnLayoutPlan(t, conversation)
				if plan.FrozenPrefixEnd <= previousEnd {
					t.Fatal("旧回合不完整身份或中断调用锁住了后续压缩")
				}
				if err := applyCompactionToConversation(conversation, plan, "历史进度"); err != nil {
					t.Fatal(err)
				}
				previousEnd = plan.FrozenPrefixEnd
			}
		})
	}
}

func TestSameTurnLayoutKeepsParallelAndUnfinishedBatches(t *testing.T) {
	for _, unfinished := range []bool{false, true} {
		t.Run(fmt.Sprintf("unfinished=%v", unfinished), func(t *testing.T) {
			conversation := compactionAppendOnlyConversation(t)
			appendSameTurnBatches(t, conversation, "old-completed", 3)
			batchStart := len(conversation.Entries)
			appendEntriesInPlace(conversation, []HistoryEntry{
				tailReadToolCallEntry(t, 2, "request-2", "parallel-a", "parallel-model"),
				newMetadataEntry(2, "request-2", "evidence", map[string]any{"kind": "tool"}),
				tailReadToolCallEntry(t, 2, "request-2", "parallel-b", "parallel-model"),
				tailReadToolResultEntry(t, 2, "request-2", "parallel-a", "parallel-model", "/tmp/a", "a"),
			})
			if unfinished {
				// 后面还有已完成批次，也不能越过前面的未完成调用。
				appendSameTurnBatches(t, conversation, "later-completed", 2)
			} else {
				appendEntriesInPlace(conversation, []HistoryEntry{tailReadToolResultEntry(t, 2, "request-2", "parallel-b", "parallel-model", "/tmp/b", "b")})
			}
			plan := sameTurnLayoutPlan(t, conversation)
			if plan.FrozenPrefixEnd != batchStart {
				t.Fatalf("并行/未完成批次被拆开: end=%d batchStart=%d", plan.FrozenPrefixEnd, batchStart)
			}
			if err := applyCompactionToConversation(conversation, plan, "保留近期完整关系"); err != nil {
				t.Fatal(err)
			}
			ids := projectedToolCallIDs(compactedPromptProjectionEntries(conversation.Entries))
			if len(ids) < 2 || ids[0] != "parallel-a" || ids[1] != "parallel-b" {
				t.Fatalf("近期批次遗漏兄弟调用: %v", ids)
			}
			// 没有新增可压缩执行记录时，不能反复摘要同一前缀。
			planAgain, err := (&Service{}).buildAutoCompactionPlanFromHistory(&compactionPlan{
				Trigger: "auto", CurrentTurnSeq: 2, CurrentRequestID: "request-2", PreserveCurrentTurnInputs: true,
				ExistingSummary: existingConversationSummaryText(conversation),
			}, conversation)
			if err != nil || planAgain != nil {
				t.Fatalf("同一范围被重复压缩: plan=%v err=%v", planAgain, err)
			}
		})
	}
}

func TestSameTurnLayoutPromptContextsSurviveRepeatedCompaction(t *testing.T) {
	conversation := compactionAppendOnlyConversation(t)
	contexts := []PromptContextMessage{
		newPromptContextMessage(promptContextSourceSelectedCursorCommands, modeladapter.Message{Role: "user", Content: "当前命令的必要指令"}, true),
		newPromptContextMessage("completion_gate", modeladapter.Message{Role: "system", Content: "完成前必须保留的提醒"}, true),
	}
	for _, context := range contexts {
		appendEntriesInPlace(conversation, []HistoryEntry{newPromptContextEntry(2, "request-2", context)})
	}
	store := NewConversationFileStore(t.TempDir())
	persistedCount := 0
	for cycle := 0; cycle < 4; cycle++ {
		appendSameTurnBatches(t, conversation, fmt.Sprintf("context-%d", cycle), 3)
		plan := sameTurnLayoutPlan(t, conversation)
		if len(plan.Layout.PinnedInputs) != 1+len(contexts) {
			t.Fatalf("第%d轮遗漏持久提示上下文: %v", cycle+1, plan.Layout.PinnedInputs)
		}
		if err := applyCompactionToConversation(conversation, plan, "执行进度"); err != nil {
			t.Fatal(err)
		}
		if _, err := store.SaveConversationWithEntries(conversation.ConversationID, conversation, conversation.Entries[persistedCount:]); err != nil {
			t.Fatal(err)
		}
		persistedCount = len(conversation.Entries)
		var err error
		conversation, err = store.LoadConversation(conversation.ConversationID)
		if err != nil {
			t.Fatal(err)
		}
		messages, err := NewHistoryProjector().ProjectPromptReplay(conversation)
		if err != nil {
			t.Fatal(err)
		}
		encoded, _ := json.Marshal(messages)
		for _, context := range contexts {
			if bytes.Count(encoded, []byte(context.Message.Content)) != 1 {
				t.Fatalf("第%d轮提示上下文丢失或重复: %s", cycle+1, encoded)
			}
		}
	}
}

func TestSameTurnLayoutNativeRejectsPinnedWithoutChangingDefault(t *testing.T) {
	conversation := compactionAppendOnlyConversation(t)
	appendSameTurnBatches(t, conversation, "native-pinned", 3)
	plan := sameTurnLayoutPlan(t, conversation)
	if len(plan.Layout.PinnedInputs) == 0 {
		t.Fatal("夹具未覆盖独立重放输入")
	}
	if err := applyCompactionToConversation(conversation, plan, "进度摘要"); err != nil {
		t.Fatal(err)
	}
	before, _ := json.Marshal(conversation)
	projection, err := NewHistoryProjector().projectNativeCheckpointProjection(conversation)
	if err == nil || !strings.Contains(err.Error(), "pinned inputs not supported") || projection != nil {
		t.Fatalf("原生写出没有阻止归档/ROOT重复输入: projection=%v err=%v", projection != nil, err)
	}
	if _, err := nativeCheckpointMessages(conversation); err == nil || !strings.Contains(err.Error(), "pinned inputs not supported") {
		t.Fatalf("第二原生入口绕过独立输入门禁: %v", err)
	}
	checkpoint, err := NewHistoryProjector().ProjectCheckpointProjection(conversation)
	if err != nil || checkpoint == nil {
		t.Fatalf("默认检查点被原生门禁阻断: %v", err)
	}
	messages, err := importedConversationStateModelMessages(checkpoint.State)
	if err != nil {
		t.Fatal(err)
	}
	encoded, _ := json.Marshal(messages)
	if bytes.Count(encoded, []byte("second question")) != 1 {
		t.Fatalf("默认检查点输入丢失或重复: %s", encoded)
	}
	after, _ := json.Marshal(conversation)
	if !bytes.Equal(before, after) {
		t.Fatal("原生拒绝或默认检查点改写历史")
	}
}

func TestSameTurnLayoutRequestContextIsPinned(t *testing.T) {
	conversation := compactionAppendOnlyConversation(t)
	context := HistoryEntry{TurnSeq: 2, RequestID: "request-2", Role: "system", Kind: "request_context", Payload: json.RawMessage(`{"file_contents":{"/tmp/input.txt":"必要的原样上下文"}}`)}
	appendEntriesInPlace(conversation, []HistoryEntry{context})
	appendSameTurnBatches(t, conversation, "context", 3)
	plan := sameTurnLayoutPlan(t, conversation)
	if len(plan.Layout.PinnedInputs) != 2 {
		t.Fatalf("遗漏当前输入或请求上下文: %v", plan.Layout.PinnedInputs)
	}
	if err := applyCompactionToConversation(conversation, plan, "完成执行摘要"); err != nil {
		t.Fatal(err)
	}
	projected := compactedPromptProjectionEntries(conversation.Entries)
	if countHistoryKind(projected, "user_message") != 1 || countHistoryKind(projected, "request_context") != 1 {
		t.Fatal("用户输入及其请求上下文必须独立原样保留")
	}
}

func TestSameTurnLayoutKeepsModelCallReasoningCarrier(t *testing.T) {
	conversation := compactionAppendOnlyConversation(t)
	appendSameTurnBatches(t, conversation, "earlier", 3)
	carrierIndex := len(conversation.Entries)
	carrier := withHistoryModelCallID(newAssistantTextEntry(2, "request-2", "准备调用工具", "仅推理内容而没有传统签名", ""), "reasoning-call")
	appendEntriesInPlace(conversation, []HistoryEntry{
		carrier,
		tailReadToolCallEntry(t, 2, "request-2", "with-carrier", "reasoning-call"),
		tailReadToolResultEntry(t, 2, "request-2", "with-carrier", "reasoning-call", "/tmp/carrier", "结果"),
	})
	plan := sameTurnLayoutPlan(t, conversation)
	if plan.FrozenPrefixEnd != carrierIndex {
		t.Fatalf("把模型调用的文本/推理载体归档而只保留工具: end=%d carrier=%d", plan.FrozenPrefixEnd, carrierIndex)
	}
	if err := applyCompactionToConversation(conversation, plan, "压缩较早批次"); err != nil {
		t.Fatal(err)
	}
	projected := compactedPromptProjectionEntries(conversation.Entries)
	if countHistoryKind(projected, "assistant_text") != 1 || countHistoryKind(projected, "tool_call") != 1 || countHistoryKind(projected, "tool_result") != 1 {
		t.Fatal("文本/推理和工具关系未完整保留")
	}
}

func TestSameTurnLayoutReplaceEntriesRemapsAndRejectsInvalidPrefix(t *testing.T) {
	conversation := compactionAppendOnlyConversation(t)
	appendSameTurnBatches(t, conversation, "before-rewind", 3)
	if err := applyCompactionToConversation(conversation, sameTurnLayoutPlan(t, conversation), "回退前进度"); err != nil {
		t.Fatal(err)
	}
	store := NewConversationFileStore(t.TempDir())
	persisted, err := store.SaveConversationWithEntries(conversation.ConversationID, conversation, conversation.Entries)
	if err != nil {
		t.Fatal(err)
	}
	projector := NewHistoryProjector()
	want, err := projector.ProjectPromptReplay(persisted)
	if err != nil {
		t.Fatal(err)
	}
	// 正式磁盘回退路径：替换完整有效前缀并重新编号，必须显式重映射输入引用。
	rebuilt, err := store.ReplaceEntries(conversation.ConversationID, persisted.Entries, nil)
	if err != nil {
		t.Fatal(err)
	}
	got, err := projector.ProjectPromptReplay(rebuilt)
	if err != nil || !reflect.DeepEqual(want, got) {
		t.Fatalf("ReplaceEntries 重建改变模型视图: %v", err)
	}
	appendSameTurnBatches(t, rebuilt, "after-rewind", 3)
	if err := applyCompactionToConversation(rebuilt, sameTurnLayoutPlan(t, rebuilt), "回退后再次压缩"); err != nil {
		t.Fatal(err)
	}
	before, err := store.LoadConversation(conversation.ConversationID)
	if err != nil {
		t.Fatal(err)
	}
	// 删去来源条目却保留摘要是非法重建，不能通过下标猜测修补，更不能污染磁盘。
	if _, err := store.ReplaceEntries(conversation.ConversationID, before.Entries[1:], nil); err == nil {
		t.Fatal("非法来源前缀被写入")
	}
	after, err := store.LoadConversation(conversation.ConversationID)
	if err != nil || !reflect.DeepEqual(before.Entries, after.Entries) {
		t.Fatalf("非法重建改变了持久历史: %v", err)
	}
}
