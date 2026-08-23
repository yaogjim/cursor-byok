package forwarder

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"

	"google.golang.org/protobuf/encoding/protojson"

	"cursor/gen/agentv1"
	runtimecore "cursor/internal/backend/agent/core"
	modeladapter "cursor/internal/backend/agent/model"
)

func TestEvaluateCompletionGatePureQuestionIsNotApplicable(t *testing.T) {
	decision := evaluateCompletionEvidenceGate(completionGateInput{
		Mode:           agentv1.AgentMode_AGENT_MODE_AGENT,
		LatestUserText: "hello, what does this function do?",
		TurnSeq:        1,
		RequestID:      "request-1",
		Entries: []HistoryEntry{
			newAssistantTextEntry(1, "request-1", "I edited the file and all tests pass.", evidenceReasoningCanary, ""),
		},
	})
	if decision.Status != completionGateStatusNotApplicable || decision.Applicable {
		t.Fatalf("pure Q&A = %+v, want not_applicable", decision)
	}
}

func TestEvaluateCompletionGateAskAndPlanDoNotTrigger(t *testing.T) {
	t.Parallel()
	for _, mode := range []agentv1.AgentMode{agentv1.AgentMode_AGENT_MODE_ASK, agentv1.AgentMode_AGENT_MODE_PLAN} {
		decision := evaluateCompletionEvidenceGate(completionGateInput{
			Mode:           mode,
			LatestUserText: "请修改 main.go 的超时时间",
			TurnSeq:        1,
			RequestID:      "request-1",
			Entries:        []HistoryEntry{gateToolCallEntry(1, "request-1", "call-write-1", "Write")},
		})
		if decision.Status != completionGateStatusNotApplicable {
			t.Fatalf("mode %s = %s, want not_applicable", mode, decision.Status)
		}
	}
}

func TestEvaluateCompletionGateAnalysisExplainReviewDoNotTrigger(t *testing.T) {
	t.Parallel()
	texts := []string{
		"分析为什么没修改文件",
		"解释这次失败的原因",
		"review this change for bugs",
		"请评审这段实现",
		"why weren't the files modified?",
		"analyze why the edit did not happen",
	}
	for _, text := range texts {
		decision := evaluateCompletionEvidenceGate(completionGateInput{
			Mode:           agentv1.AgentMode_AGENT_MODE_AGENT,
			LatestUserText: text,
			TurnSeq:        1,
			RequestID:      "request-1",
			Entries:        []HistoryEntry{newAssistantTextEntry(1, "request-1", "done", "", "")},
		})
		if decision.Status != completionGateStatusNotApplicable {
			t.Fatalf("text %q = %s, want not_applicable", text, decision.Status)
		}
	}
}

func TestEvaluateCompletionGateEditPlusExplainIsInsufficientFirst(t *testing.T) {
	t.Parallel()
	for _, text := range []string{
		"请修改 main.go 并解释原因",
		"please fix main.go and explain why",
	} {
		decision := evaluateCompletionEvidenceGate(completionGateInput{
			Mode:           agentv1.AgentMode_AGENT_MODE_AGENT,
			LatestUserText: text,
			TurnSeq:        1,
			RequestID:      "request-1",
			Entries:        []HistoryEntry{newAssistantTextEntry(1, "request-1", "done", "", "")},
		})
		assertGateStatus(t, decision, completionGateStatusInsufficientFirst, 1, completionGateGapMissingSuccessfulMutation)
	}
}

func TestEvaluateCompletionGateChineseAdviceAndNoModifyAreNotApplicable(t *testing.T) {
	t.Parallel()
	for _, text := range []string{
		"这个文案改成什么比较好？",
		"请加上更多上下文（只需说明，不要修改文件）",
	} {
		decision := evaluateCompletionEvidenceGate(completionGateInput{
			Mode:           agentv1.AgentMode_AGENT_MODE_AGENT,
			LatestUserText: text,
			TurnSeq:        1,
			RequestID:      "request-1",
			Entries:        []HistoryEntry{newAssistantTextEntry(1, "request-1", "done", "", "")},
		})
		if decision.Status != completionGateStatusNotApplicable || decision.Applicable {
			t.Fatalf("text %q = %+v, want not_applicable", text, decision)
		}
	}
}

func TestEvaluateCompletionGateChineseRealEditsWithoutToolsAreInsufficientFirst(t *testing.T) {
	t.Parallel()
	for _, text := range []string{
		"请把配置改成 30 秒",
		"请在文件中加上字段",
	} {
		decision := evaluateCompletionEvidenceGate(completionGateInput{
			Mode:           agentv1.AgentMode_AGENT_MODE_AGENT,
			LatestUserText: text,
			TurnSeq:        1,
			RequestID:      "request-1",
			Entries:        []HistoryEntry{newAssistantTextEntry(1, "request-1", "done", "", "")},
		})
		assertGateStatus(t, decision, completionGateStatusInsufficientFirst, 1, completionGateGapMissingSuccessfulMutation)
	}
}

func TestEvaluateCompletionGateExplicitEditWithoutToolsIsInsufficientFirst(t *testing.T) {
	decision := evaluateCompletionEvidenceGate(completionGateInput{
		Mode:           agentv1.AgentMode_AGENT_MODE_AGENT,
		LatestUserText: "请修改 main.go 的超时时间",
		TurnSeq:        1,
		RequestID:      "request-1",
		Entries: []HistoryEntry{
			newAssistantTextEntry(1, "request-1", "I changed the timeout in main.go.", evidenceReasoningCanary, ""),
		},
	})
	assertGateStatus(t, decision, completionGateStatusInsufficientFirst, 1, completionGateGapMissingSuccessfulMutation)
}

func TestEvaluateCompletionGateFailedMutationIsInsufficientFirst(t *testing.T) {
	failed := mustGateEvidence(t, executionEvidenceInput{
		TurnSeq:    1,
		RequestID:  "request-1",
		ToolCallID: "call-write-fail",
		ToolName:   "Write",
		ToolCall:   evidenceEditErrorToolCall(evidencePathCanary, "disk full"),
		Sequence:   4,
	})
	decision := evaluateCompletionEvidenceGate(completionGateInput{
		Mode:           agentv1.AgentMode_AGENT_MODE_AGENT,
		LatestUserText: "hello",
		TurnSeq:        1,
		RequestID:      "request-1",
		Entries: []HistoryEntry{
			gateToolCallEntry(1, "request-1", "call-write-fail", "Write"),
			newExecutionEvidenceEntry(failed),
		},
	})
	assertGateStatus(t, decision, completionGateStatusInsufficientFirst, 1, completionGateGapMissingSuccessfulMutation, completionGateGapPendingOrFailedResult)
	if !decision.Applicable {
		t.Fatal("failed mutation attempt should apply the gate")
	}
}

func TestEvaluateCompletionGatePendingMutationIsInsufficientFirst(t *testing.T) {
	decision := evaluateCompletionEvidenceGate(completionGateInput{
		Mode:           agentv1.AgentMode_AGENT_MODE_AGENT,
		LatestUserText: "请修改配置",
		TurnSeq:        1,
		RequestID:      "request-1",
		Entries:        []HistoryEntry{gateToolCallEntry(1, "request-1", "call-write-pending", "Write")},
	})
	assertGateStatus(t, decision, completionGateStatusInsufficientFirst, 1, completionGateGapMissingSuccessfulMutation, completionGateGapPendingOrFailedResult)
}

func TestEvaluateCompletionGateMutationWithoutVerificationIsInsufficientFirst(t *testing.T) {
	decision := evaluateCompletionEvidenceGate(completionGateInput{
		Mode:           agentv1.AgentMode_AGENT_MODE_AGENT,
		LatestUserText: "请修改超时",
		TurnSeq:        1,
		RequestID:      "request-1",
		Entries:        []HistoryEntry{gateMutationSuccessEntry(t, 5, "call-write-1")},
	})
	assertGateStatus(t, decision, completionGateStatusInsufficientFirst, 1, completionGateGapMissingLaterVerification)
}

func TestEvaluateCompletionGateStaleVerificationIsInsufficientFirst(t *testing.T) {
	decision := evaluateCompletionEvidenceGate(completionGateInput{
		Mode:           agentv1.AgentMode_AGENT_MODE_AGENT,
		LatestUserText: "请修改超时",
		TurnSeq:        1,
		RequestID:      "request-1",
		Entries: []HistoryEntry{
			gateVerificationSuccessEntry(t, 3, "call-test-1"),
			gateMutationSuccessEntry(t, 8, "call-write-1"),
		},
	})
	assertGateStatus(t, decision, completionGateStatusInsufficientFirst, 1, completionGateGapMissingLaterVerification)
	if !decision.Summary.VerificationStale {
		t.Fatalf("summary = %+v, want stale verification", decision.Summary)
	}
}

func TestEvaluateCompletionGateMutationThenVerificationIsSatisfied(t *testing.T) {
	decision := evaluateCompletionEvidenceGate(completionGateInput{
		Mode:           agentv1.AgentMode_AGENT_MODE_AGENT,
		LatestUserText: "请修改超时",
		TurnSeq:        1,
		RequestID:      "request-1",
		Entries: []HistoryEntry{
			gateMutationSuccessEntry(t, 5, "call-write-1"),
			gateVerificationSuccessEntry(t, 9, "call-test-1"),
			newAssistantTextEntry(1, "request-1", "done", evidenceReasoningCanary, ""),
		},
	})
	if decision.Status != completionGateStatusSatisfied {
		t.Fatalf("status = %s gaps=%v, want satisfied", decision.Status, decision.Gaps)
	}
	if len(decision.Gaps) != 0 {
		t.Fatalf("satisfied gaps = %v, want empty", decision.Gaps)
	}
}

func TestEvaluateCompletionGatePairedToolCallsAndResultsAreSatisfied(t *testing.T) {
	decision := evaluateCompletionEvidenceGate(completionGateInput{
		Mode:           agentv1.AgentMode_AGENT_MODE_AGENT,
		LatestUserText: "请修改超时",
		TurnSeq:        1,
		RequestID:      "request-1",
		Entries: []HistoryEntry{
			gateToolCallEntry(1, "request-1", "call-write-1", "Write"),
			gateToolResultEntry(1, "request-1", "call-write-1", "Write"),
			gateMutationSuccessEntry(t, 5, "call-write-1"),
			gateShellToolCallEntry(t, 1, "request-1", "call-test-1", evidenceCommandCanary),
			gateToolResultEntry(1, "request-1", "call-test-1", "Shell"),
			gateVerificationSuccessEntry(t, 9, "call-test-1"),
		},
	})
	if decision.Status != completionGateStatusSatisfied {
		t.Fatalf("paired tool_call/result = %s gaps=%v, want satisfied", decision.Status, decision.Gaps)
	}
}

func TestEvaluateCompletionGateUnpairedVerificationToolCallIsPending(t *testing.T) {
	decision := evaluateCompletionEvidenceGate(completionGateInput{
		Mode:           agentv1.AgentMode_AGENT_MODE_AGENT,
		LatestUserText: "请修改超时",
		TurnSeq:        1,
		RequestID:      "request-1",
		Entries: []HistoryEntry{
			gateMutationSuccessEntry(t, 5, "call-write-1"),
			gateShellToolCallEntry(t, 1, "request-1", "call-test-pending", evidenceCommandCanary),
		},
	})
	assertGateStatus(t, decision, completionGateStatusInsufficientFirst, 1, completionGateGapMissingLaterVerification, completionGateGapPendingOrFailedResult)
}

func TestEvaluateCompletionGateUnpairedNeutralToolCallIsPending(t *testing.T) {
	decision := evaluateCompletionEvidenceGate(completionGateInput{
		Mode:           agentv1.AgentMode_AGENT_MODE_AGENT,
		LatestUserText: "请修改超时",
		TurnSeq:        1,
		RequestID:      "request-1",
		Entries: []HistoryEntry{
			gateMutationSuccessEntry(t, 5, "call-write-1"),
			gateVerificationSuccessEntry(t, 9, "call-test-1"),
			gateToolCallEntry(1, "request-1", "call-read-pending", "Read"),
		},
	})
	assertGateStatus(t, decision, completionGateStatusInsufficientFirst, 1, completionGateGapPendingOrFailedResult)
}

func TestEvaluateCompletionGateCanceledMutationIsInsufficientFirst(t *testing.T) {
	canceled := mustGateEvidence(t, executionEvidenceInput{
		TurnSeq:      1,
		RequestID:    "request-1",
		ToolCallID:   "call-write-canceled",
		ToolName:     "Write",
		TerminalHint: executionEvidenceHintCanceled,
		Sequence:     4,
	})
	decision := evaluateCompletionEvidenceGate(completionGateInput{
		Mode:           agentv1.AgentMode_AGENT_MODE_AGENT,
		LatestUserText: "hello",
		TurnSeq:        1,
		RequestID:      "request-1",
		Entries:        []HistoryEntry{newExecutionEvidenceEntry(canceled)},
	})
	assertGateStatus(t, decision, completionGateStatusInsufficientFirst, 1, completionGateGapMissingSuccessfulMutation, completionGateGapPendingOrFailedResult)
}

func TestEvaluateCompletionGateAfterRetryStillInsufficient(t *testing.T) {
	decision := evaluateCompletionEvidenceGate(completionGateInput{
		Mode:           agentv1.AgentMode_AGENT_MODE_AGENT,
		LatestUserText: "请修改超时",
		TurnSeq:        1,
		RequestID:      "request-1",
		Entries: []HistoryEntry{
			newCompletionGateEntry(completionGateRecord{
				SchemaVersion: completionGateSchemaVersion,
				Status:        completionGateStatusInsufficientFirst,
				RetryCount:    1,
				TurnSeq:       1,
				RequestID:     "request-1",
				Gaps:          []string{completionGateGapMissingSuccessfulMutation},
			}),
			newAssistantTextEntry(1, "request-1", "still done", "", ""),
		},
	})
	assertGateStatus(t, decision, completionGateStatusInsufficientAfterRetry, 1, completionGateGapMissingSuccessfulMutation)
}

func TestEvaluateCompletionGateAfterRetryBecomesSatisfied(t *testing.T) {
	decision := evaluateCompletionEvidenceGate(completionGateInput{
		Mode:           agentv1.AgentMode_AGENT_MODE_AGENT,
		LatestUserText: "请修改超时",
		TurnSeq:        1,
		RequestID:      "request-1",
		Entries: []HistoryEntry{
			newCompletionGateEntry(completionGateRecord{
				SchemaVersion: completionGateSchemaVersion,
				Status:        completionGateStatusInsufficientFirst,
				RetryCount:    1,
				TurnSeq:       1,
				RequestID:     "request-1",
			}),
			gateMutationSuccessEntry(t, 5, "call-write-1"),
			gateVerificationSuccessEntry(t, 9, "call-test-1"),
		},
	})
	if decision.Status != completionGateStatusSatisfied {
		t.Fatalf("status = %s, want satisfied after retry filled evidence", decision.Status)
	}
	if decision.RetryCount != 1 {
		t.Fatalf("retry_count = %d, want 1 preserved from history", decision.RetryCount)
	}
}

func TestEvaluateCompletionGateUnknownMCPDoesNotForgeEvidence(t *testing.T) {
	record := mustGateEvidence(t, executionEvidenceInput{
		TurnSeq:    1,
		RequestID:  "request-1",
		ToolCallID: "call-mcp-1",
		ToolName:   "CallMcpTool",
		ToolCall:   evidenceMCPSuccessToolCall(),
		Sequence:   4,
	})
	decision := evaluateCompletionEvidenceGate(completionGateInput{
		Mode:           agentv1.AgentMode_AGENT_MODE_AGENT,
		LatestUserText: "请修改超时",
		TurnSeq:        1,
		RequestID:      "request-1",
		Entries:        []HistoryEntry{newExecutionEvidenceEntry(record)},
	})
	assertGateStatus(t, decision, completionGateStatusInsufficientFirst, 1, completionGateGapMissingSuccessfulMutation, completionGateGapUnknownToolNotEvidence)
	if decision.Summary.MutationToolCount != 0 || decision.Summary.VerificationCommandCount != 0 {
		t.Fatalf("unknown MCP forged counts: %+v", decision.Summary)
	}
}

func TestEvaluateCompletionGateOldHistoryDoesNotForgeEvidence(t *testing.T) {
	decision := evaluateCompletionEvidenceGate(completionGateInput{
		Mode:           agentv1.AgentMode_AGENT_MODE_AGENT,
		LatestUserText: "请修改超时",
		TurnSeq:        1,
		RequestID:      "request-1",
		Entries: []HistoryEntry{
			newAssistantTextEntry(1, "request-1", "edited "+evidencePathCanary, evidenceReasoningCanary, ""),
			newToolResultEntry(1, "request-1", "call-old", "Write", `{"path":"`+evidencePathCanary+`"}`, evidenceFileBodyCanary, evidenceReasoningCanary, nil),
		},
	})
	assertGateStatus(t, decision, completionGateStatusInsufficientFirst, 1, completionGateGapMissingSuccessfulMutation)
	if decision.Summary.VerificationEvidence != executionEvidenceVerificationUnknown && decision.Summary.MutationToolCount != 0 {
		t.Fatalf("old history forged ledger: %+v", decision.Summary)
	}
}

func TestEvaluateCompletionGateIgnoresOtherTurnAndRequest(t *testing.T) {
	decision := evaluateCompletionEvidenceGate(completionGateInput{
		Mode:           agentv1.AgentMode_AGENT_MODE_AGENT,
		LatestUserText: "hello",
		TurnSeq:        2,
		RequestID:      "request-2",
		Entries: []HistoryEntry{
			gateMutationSuccessEntry(t, 5, "call-write-old"),
			withHistoryRequestID(gateVerificationSuccessEntry(t, 9, "call-test-old"), "request-1"),
		},
	})
	if decision.Status != completionGateStatusNotApplicable {
		t.Fatalf("cross turn/request leaked into current ledger: %+v", decision)
	}
}

func TestEvaluateCompletionGateQuestionWithUpdateVerbIsNotApplicable(t *testing.T) {
	t.Parallel()
	for _, text := range []string{
		"what does update do?",
		"how do I fix this conceptually?",
		"please add more context about this function",
		"这段代码是做什么的？",
		"这个函数是什么意思？",
		"update me on the current behavior",
	} {
		decision := evaluateCompletionEvidenceGate(completionGateInput{
			Mode:           agentv1.AgentMode_AGENT_MODE_AGENT,
			LatestUserText: text,
			TurnSeq:        1,
			RequestID:      "request-1",
			Entries:        []HistoryEntry{newAssistantTextEntry(1, "request-1", "here is an explanation", "", "")},
		})
		if decision.Status != completionGateStatusNotApplicable {
			t.Fatalf("text %q = %s, want not_applicable", text, decision.Status)
		}
	}
}

func TestEvaluateCompletionGateFailedThenSuccessfulEvidenceIsSatisfied(t *testing.T) {
	failed := mustGateEvidence(t, executionEvidenceInput{
		TurnSeq:    1,
		RequestID:  "request-1",
		ToolCallID: "call-write-fail",
		ToolName:   "Write",
		ToolCall:   evidenceEditErrorToolCall(evidencePathCanary, "disk full"),
		Sequence:   4,
	})
	decision := evaluateCompletionEvidenceGate(completionGateInput{
		Mode:           agentv1.AgentMode_AGENT_MODE_AGENT,
		LatestUserText: "请修改超时",
		TurnSeq:        1,
		RequestID:      "request-1",
		Entries: []HistoryEntry{
			newExecutionEvidenceEntry(failed),
			gateMutationSuccessEntry(t, 6, "call-write-ok"),
			gateVerificationSuccessEntry(t, 10, "call-test-1"),
		},
	})
	if decision.Status != completionGateStatusSatisfied {
		t.Fatalf("status = %s gaps=%v, want satisfied after later success", decision.Status, decision.Gaps)
	}
}

func TestEvaluateCompletionGateFailedThenSuccessfulVerificationIsSatisfied(t *testing.T) {
	failed := mustGateEvidence(t, executionEvidenceInput{
		TurnSeq:         1,
		RequestID:       "request-1",
		ToolCallID:      "call-test-fail",
		ToolName:        "Shell",
		ArgsJSON:        []byte(`{"command":"` + evidenceCommandCanary + `"}`),
		ToolCall:        evidenceShellSuccessToolCall(evidenceCommandCanary, 1),
		Sequence:        7,
		CommandOverride: evidenceCommandCanary,
	})
	decision := evaluateCompletionEvidenceGate(completionGateInput{
		Mode:           agentv1.AgentMode_AGENT_MODE_AGENT,
		LatestUserText: "请修改超时",
		TurnSeq:        1,
		RequestID:      "request-1",
		Entries: []HistoryEntry{
			gateMutationSuccessEntry(t, 5, "call-write-1"),
			newExecutionEvidenceEntry(failed),
			gateVerificationSuccessEntry(t, 11, "call-test-ok"),
		},
	})
	if decision.Status != completionGateStatusSatisfied {
		t.Fatalf("status = %s gaps=%v, want satisfied after later verification", decision.Status, decision.Gaps)
	}
	if len(decision.Gaps) != 0 {
		t.Fatalf("later verification left gaps = %v", decision.Gaps)
	}
}

func TestEvaluateCompletionGateFailedVerificationIsNotSuccess(t *testing.T) {
	failed := mustGateEvidence(t, executionEvidenceInput{
		TurnSeq:         1,
		RequestID:       "request-1",
		ToolCallID:      "call-test-fail",
		ToolName:        "Shell",
		ArgsJSON:        []byte(`{"command":"` + evidenceCommandCanary + `"}`),
		ToolCall:        evidenceShellSuccessToolCall(evidenceCommandCanary, 1),
		Sequence:        9,
		CommandOverride: evidenceCommandCanary,
	})
	decision := evaluateCompletionEvidenceGate(completionGateInput{
		Mode:           agentv1.AgentMode_AGENT_MODE_AGENT,
		LatestUserText: "请修改超时",
		TurnSeq:        1,
		RequestID:      "request-1",
		Entries: []HistoryEntry{
			gateMutationSuccessEntry(t, 5, "call-write-1"),
			newExecutionEvidenceEntry(failed),
		},
	})
	assertGateStatus(t, decision, completionGateStatusInsufficientFirst, 1, completionGateGapMissingLaterVerification, completionGateGapPendingOrFailedResult)
}

func TestEvaluateCompletionGateLivePendingBlocksSatisfaction(t *testing.T) {
	decision := evaluateCompletionEvidenceGate(completionGateInput{
		Mode:             agentv1.AgentMode_AGENT_MODE_AGENT,
		LatestUserText:   "请修改超时",
		TurnSeq:          1,
		RequestID:        "request-1",
		PendingExecCount: 1,
		Entries: []HistoryEntry{
			gateMutationSuccessEntry(t, 5, "call-write-1"),
			gateVerificationSuccessEntry(t, 9, "call-test-1"),
		},
	})
	assertGateStatus(t, decision, completionGateStatusInsufficientFirst, 1, completionGateGapPendingOrFailedResult)
}

func TestEvaluateCompletionGateDoesNotThirdRetry(t *testing.T) {
	decision := evaluateCompletionEvidenceGate(completionGateInput{
		Mode:           agentv1.AgentMode_AGENT_MODE_AGENT,
		LatestUserText: "请修改超时",
		TurnSeq:        1,
		RequestID:      "request-1",
		Entries: []HistoryEntry{
			newCompletionGateEntry(completionGateRecord{
				SchemaVersion: completionGateSchemaVersion,
				Status:        completionGateStatusInsufficientAfterRetry,
				RetryCount:    1,
				TurnSeq:       1,
				RequestID:     "request-1",
			}),
		},
	})
	if decision.Status != completionGateStatusInsufficientAfterRetry || decision.RetryCount != 1 {
		t.Fatalf("third evaluation = %+v, want insufficient_after_retry retry=1", decision)
	}
}

func TestCompletionGateMetadataAndReminderPrivacy(t *testing.T) {
	record := gateRecordFromDecision(1, "request-1", completionGateDecision{
		Status:     completionGateStatusInsufficientFirst,
		RetryCount: 1,
		Gaps:       []string{completionGateGapMissingSuccessfulMutation},
		Summary: ExecutionEvidenceSummary{
			VerificationEvidence:   executionEvidenceVerificationAbsent,
			VerificationEvidenceID: "ev:1:request-1:call-write-1",
		},
	})
	meta := newCompletionGateEntry(record)
	prompt := newCompletionGatePromptContext(1, "request-1")
	blob := string(mustJSON(t, meta)) + string(mustJSON(t, prompt)) + string(prompt.Payload) + string(meta.Payload)
	for _, canary := range []string{
		evidenceReasoningCanary,
		evidenceStdoutCanary,
		evidenceFileBodyCanary,
		evidenceCredentialCanary,
		evidencePathCanary,
		evidenceCommandCanary,
		evidenceCookieCanary,
		"sk-live-evidence-canary",
	} {
		if strings.Contains(blob, canary) {
			t.Fatalf("gate artifact leaked %q: %s", canary, blob)
		}
	}
	assertCompletionGateWhitelist(t, meta)
	var payload promptContextEntryPayload
	if err := json.Unmarshal(prompt.Payload, &payload); err != nil {
		t.Fatalf("decode prompt payload: %v", err)
	}
	if payload.Source != promptContextSourceCompletionEvidenceGate {
		t.Fatalf("prompt source = %q", payload.Source)
	}
	if payload.Content != wrapSystemReminder(completionEvidenceGateReminderText) {
		t.Fatalf("prompt content was not the fixed gap reminder")
	}
}

func TestCompleteSuccessfulTurnPureQuestionWritesTurnCompleted(t *testing.T) {
	service, stream := testCompletionGateStream(t, "hello")
	if err := service.completeSuccessfulTurn(stream, gateCompletion(stream)); err != nil {
		t.Fatalf("completeSuccessfulTurn() error = %v", err)
	}
	conversation := snapshotGateConversation(t, service, stream)
	if len(metadataEntriesOfType(conversation.Entries, "turn_completed")) != 1 {
		t.Fatalf("turn_completed count = %d, want 1", len(metadataEntriesOfType(conversation.Entries, "turn_completed")))
	}
	if len(completionGateMetadataEntries(conversation.Entries)) != 0 {
		t.Fatalf("Q&A persisted a gate: %+v", conversation.Entries)
	}
}

func TestCompleteSuccessfulTurnEditWithoutToolsRemindsOnce(t *testing.T) {
	service, stream := testCompletionGateStream(t, "请修改 main.go 的超时时间")
	if err := service.completeSuccessfulTurn(stream, gateCompletion(stream)); err != nil {
		t.Fatalf("completeSuccessfulTurn() error = %v", err)
	}
	conversation := snapshotGateConversation(t, service, stream)
	if len(metadataEntriesOfType(conversation.Entries, "turn_completed")) != 0 {
		t.Fatal("first insufficient wrote turn_completed")
	}
	gateEntries := completionGateMetadataEntries(conversation.Entries)
	if len(gateEntries) != 1 {
		t.Fatalf("gate metadata count = %d, want 1", len(gateEntries))
	}
	record, ok := decodeCompletionGate(gateEntries[0])
	if !ok || record.Status != completionGateStatusInsufficientFirst || record.RetryCount != 1 {
		t.Fatalf("gate record = %+v", record)
	}
	assertCompletionGateWhitelist(t, gateEntries[0])
	if len(completionGatePromptEntries(conversation.Entries)) != 1 {
		t.Fatalf("prompt_context count = %d, want 1", len(completionGatePromptEntries(conversation.Entries)))
	}
	stream.mu.Lock()
	action := stream.PendingProviderAction
	stream.mu.Unlock()
	if action != providerActionResume {
		t.Fatalf("pending action = %q, want resume", action)
	}
	invalidateGateResumeTimer(stream)
}

func TestCompleteSuccessfulTurnRetryPersistIsIdempotent(t *testing.T) {
	service, stream := testCompletionGateStream(t, "请修改超时")
	if err := service.persistCompletionGateRetry(stream, stream.ConversationID, stream.TurnSeq, stream.RequestID, completionGateDecision{
		Status:     completionGateStatusInsufficientFirst,
		RetryCount: 1,
		Gaps:       []string{completionGateGapMissingSuccessfulMutation},
	}); err != nil {
		t.Fatalf("persist retry: %v", err)
	}
	if err := service.persistCompletionGateRetry(stream, stream.ConversationID, stream.TurnSeq, stream.RequestID, completionGateDecision{
		Status:     completionGateStatusInsufficientFirst,
		RetryCount: 1,
		Gaps:       []string{completionGateGapMissingSuccessfulMutation},
	}); err != nil {
		t.Fatalf("duplicate persist retry: %v", err)
	}
	conversation := snapshotGateConversation(t, service, stream)
	if len(completionGateMetadataEntries(conversation.Entries)) != 1 {
		t.Fatalf("duplicate gate metadata count = %d, want 1", len(completionGateMetadataEntries(conversation.Entries)))
	}
	if len(completionGatePromptEntries(conversation.Entries)) != 1 {
		t.Fatalf("duplicate prompt count = %d, want 1", len(completionGatePromptEntries(conversation.Entries)))
	}
}

func TestCompleteSuccessfulTurnSecondInsufficientCompletesWithoutThirdResume(t *testing.T) {
	service, stream := testCompletionGateStream(t, "请修改超时")
	if err := service.completeSuccessfulTurn(stream, gateCompletion(stream)); err != nil {
		t.Fatalf("first complete: %v", err)
	}
	invalidateGateResumeTimer(stream)
	if err := service.completeSuccessfulTurn(stream, gateCompletion(stream)); err != nil {
		t.Fatalf("second complete: %v", err)
	}
	conversation := snapshotGateConversation(t, service, stream)
	if len(metadataEntriesOfType(conversation.Entries, "turn_completed")) != 1 {
		t.Fatalf("turn_completed count = %d, want 1", len(metadataEntriesOfType(conversation.Entries, "turn_completed")))
	}
	if len(completionGatePromptEntries(conversation.Entries)) != 1 {
		t.Fatalf("third reminder was written: prompt count=%d", len(completionGatePromptEntries(conversation.Entries)))
	}
	var sawAfterRetry bool
	for _, entry := range completionGateMetadataEntries(conversation.Entries) {
		record, _ := decodeCompletionGate(entry)
		if record.Status == completionGateStatusInsufficientAfterRetry && record.RetryCount == 1 {
			sawAfterRetry = true
		}
	}
	if !sawAfterRetry {
		t.Fatal("missing insufficient_after_retry diagnostic")
	}
	stream.mu.Lock()
	action := stream.PendingProviderAction
	stream.mu.Unlock()
	if action == providerActionResume {
		t.Fatal("second insufficient scheduled a third provider resume")
	}
	if err := service.completeSuccessfulTurn(stream, gateCompletion(stream)); err != nil {
		t.Fatalf("third complete: %v", err)
	}
	conversation = snapshotGateConversation(t, service, stream)
	if len(metadataEntriesOfType(conversation.Entries, "turn_completed")) != 1 {
		t.Fatalf("third complete duplicated turn_completed: %d", len(metadataEntriesOfType(conversation.Entries, "turn_completed")))
	}
	if len(completionGatePromptEntries(conversation.Entries)) != 1 {
		t.Fatalf("third complete wrote another reminder: %d", len(completionGatePromptEntries(conversation.Entries)))
	}
	stream.mu.Lock()
	action = stream.PendingProviderAction
	stream.mu.Unlock()
	if action == providerActionResume {
		t.Fatal("third complete scheduled another provider resume")
	}
}

func TestCompleteSuccessfulTurnRetryThenEvidenceCompletesOnce(t *testing.T) {
	service, stream := testCompletionGateStream(t, "请修改超时")
	if err := service.completeSuccessfulTurn(stream, gateCompletion(stream)); err != nil {
		t.Fatalf("first complete: %v", err)
	}
	invalidateGateResumeTimer(stream)
	if err := service.appendToolResult(stream, "call-write-1", "Write", []byte(`{"path":"`+evidencePathCanary+`"}`), evidenceFileBodyCanary, evidenceReasoningCanary, evidenceEditSuccessToolCall(evidencePathCanary), "model-call-2"); err != nil {
		t.Fatalf("append mutation: %v", err)
	}
	if err := service.appendToolResult(stream, "call-test-1", "Shell", []byte(`{"command":"`+evidenceCommandCanary+`"}`), evidenceStdoutCanary, "", evidenceShellSuccessToolCall(evidenceCommandCanary, 0), "model-call-2"); err != nil {
		t.Fatalf("append verification: %v", err)
	}
	if err := service.completeSuccessfulTurn(stream, gateCompletion(stream)); err != nil {
		t.Fatalf("second complete: %v", err)
	}
	conversation := snapshotGateConversation(t, service, stream)
	if len(metadataEntriesOfType(conversation.Entries, "turn_completed")) != 1 {
		t.Fatalf("turn_completed count = %d, want 1", len(metadataEntriesOfType(conversation.Entries, "turn_completed")))
	}
	if len(completionGatePromptEntries(conversation.Entries)) != 1 {
		t.Fatalf("prompt count = %d, want the original reminder only", len(completionGatePromptEntries(conversation.Entries)))
	}
}

func TestCompleteSuccessfulTurnSatisfiedMutationAndVerificationSkipsReminder(t *testing.T) {
	service, stream := testCompletionGateStream(t, "请修改超时")
	if err := service.appendToolResult(stream, "call-write-1", "Write", []byte(`{"path":"`+evidencePathCanary+`"}`), evidenceFileBodyCanary, evidenceReasoningCanary, evidenceEditSuccessToolCall(evidencePathCanary), "model-call-1"); err != nil {
		t.Fatalf("append mutation: %v", err)
	}
	if err := service.appendToolResult(stream, "call-test-1", "Shell", []byte(`{"command":"`+evidenceCommandCanary+`"}`), evidenceStdoutCanary, "", evidenceShellSuccessToolCall(evidenceCommandCanary, 0), "model-call-1"); err != nil {
		t.Fatalf("append verification: %v", err)
	}
	if err := service.completeSuccessfulTurn(stream, gateCompletion(stream)); err != nil {
		t.Fatalf("completeSuccessfulTurn() error = %v", err)
	}
	conversation := snapshotGateConversation(t, service, stream)
	if len(metadataEntriesOfType(conversation.Entries, "turn_completed")) != 1 {
		t.Fatalf("turn_completed count = %d, want 1", len(metadataEntriesOfType(conversation.Entries, "turn_completed")))
	}
	if len(completionGatePromptEntries(conversation.Entries)) != 0 {
		t.Fatal("satisfied path wrote a gap reminder")
	}
}

func TestCompleteSuccessfulTurnSatisfiedWithToolCallResultPair(t *testing.T) {
	service, stream := testCompletionGateStream(t, "请修改超时")
	if _, err := service.appendConversationEntries(stream, stream.ConversationID, []HistoryEntry{
		gateWriteToolCallEntry(t, stream.TurnSeq, stream.RequestID, "call-write-1"),
		gateShellToolCallEntry(t, stream.TurnSeq, stream.RequestID, "call-test-1", "go test ./internal/backend/forwarder"),
	}); err != nil {
		t.Fatalf("append tool calls: %v", err)
	}
	if err := service.appendToolResult(stream, "call-write-1", "Write", []byte(`{"path":"`+evidencePathCanary+`"}`), evidenceFileBodyCanary, evidenceReasoningCanary, evidenceEditSuccessToolCall(evidencePathCanary), "model-call-1"); err != nil {
		t.Fatalf("append mutation: %v", err)
	}
	if err := service.appendToolResult(stream, "call-test-1", "Shell", []byte(`{"command":"`+evidenceCommandCanary+`"}`), evidenceStdoutCanary, "", evidenceShellSuccessToolCall(evidenceCommandCanary, 0), "model-call-1"); err != nil {
		t.Fatalf("append verification: %v", err)
	}
	if err := service.completeSuccessfulTurn(stream, gateCompletion(stream)); err != nil {
		t.Fatalf("completeSuccessfulTurn() error = %v", err)
	}
	conversation := snapshotGateConversation(t, service, stream)
	if len(metadataEntriesOfType(conversation.Entries, "turn_completed")) != 1 {
		t.Fatalf("turn_completed count = %d, want 1 gaps=%v", len(metadataEntriesOfType(conversation.Entries, "turn_completed")), completionGateMetadataEntries(conversation.Entries))
	}
	if len(completionGatePromptEntries(conversation.Entries)) != 0 {
		t.Fatal("paired tool_call/result wrote a gap reminder")
	}
}

func TestCompleteSuccessfulTurnPlanModeDoesNotRemind(t *testing.T) {
	service, stream := testCompletionGateStream(t, "请修改 main.go")
	stream.mu.Lock()
	stream.Mode = agentv1.AgentMode_AGENT_MODE_PLAN
	stream.mu.Unlock()
	if err := service.completeSuccessfulTurn(stream, gateCompletion(stream)); err != nil {
		t.Fatalf("completeSuccessfulTurn() error = %v", err)
	}
	conversation := snapshotGateConversation(t, service, stream)
	if len(metadataEntriesOfType(conversation.Entries, "turn_completed")) != 1 {
		t.Fatal("Plan mode did not complete")
	}
	if len(completionGatePromptEntries(conversation.Entries)) != 0 {
		t.Fatal("Plan mode wrote a completion gate reminder")
	}
}

func TestCompleteSuccessfulTurnDoesNotDuplicateAfterCompleted(t *testing.T) {
	service, stream := testCompletionGateStream(t, "hello")
	if err := service.completeSuccessfulTurn(stream, gateCompletion(stream)); err != nil {
		t.Fatalf("first complete: %v", err)
	}
	if err := service.completeSuccessfulTurn(stream, gateCompletion(stream)); err != nil {
		t.Fatalf("second complete: %v", err)
	}
	conversation := snapshotGateConversation(t, service, stream)
	if got := len(metadataEntriesOfType(conversation.Entries, "turn_completed")); got != 1 {
		t.Fatalf("turn_completed count = %d, want 1", got)
	}
}

func TestCompleteSuccessfulTurnAskModeDoesNotRemind(t *testing.T) {
	service, stream := testCompletionGateStream(t, "请修改 main.go")
	stream.mu.Lock()
	stream.Mode = agentv1.AgentMode_AGENT_MODE_ASK
	stream.mu.Unlock()
	if err := service.completeSuccessfulTurn(stream, gateCompletion(stream)); err != nil {
		t.Fatalf("completeSuccessfulTurn() error = %v", err)
	}
	conversation := snapshotGateConversation(t, service, stream)
	if len(metadataEntriesOfType(conversation.Entries, "turn_completed")) != 1 {
		t.Fatal("Ask mode did not complete")
	}
	if len(completionGatePromptEntries(conversation.Entries)) != 0 {
		t.Fatal("Ask mode wrote a completion gate reminder")
	}
}

func TestCompleteSuccessfulTurnRestartDoesNotRemindTwice(t *testing.T) {
	service, stream := testCompletionGateStream(t, "请修改超时")
	if err := service.completeSuccessfulTurn(stream, gateCompletion(stream)); err != nil {
		t.Fatalf("first complete: %v", err)
	}
	invalidateGateResumeTimer(stream)
	conversation := snapshotGateConversation(t, service, stream)
	restart := cloneCompletionGateStream(t, service, stream, conversation)
	if err := service.completeSuccessfulTurn(restart, gateCompletion(restart)); err != nil {
		t.Fatalf("restart complete: %v", err)
	}
	restartConversation := snapshotGateConversation(t, service, restart)
	if len(metadataEntriesOfType(restartConversation.Entries, "turn_completed")) != 1 {
		t.Fatalf("restart turn_completed count = %d, want 1", len(metadataEntriesOfType(restartConversation.Entries, "turn_completed")))
	}
	if len(completionGatePromptEntries(restartConversation.Entries)) != 1 {
		t.Fatalf("restart wrote another reminder: %d", len(completionGatePromptEntries(restartConversation.Entries)))
	}
}

func TestHandleProviderDoneErrorDoesNotRetryGate(t *testing.T) {
	service, stream := testCompletionGateStream(t, "请修改超时")
	stream.mu.Lock()
	stream.CurrentProviderToken = 1
	stream.ProviderActive = true
	stream.Status = StreamStatusStreaming
	stream.ProviderStreamStats.CompletionMarker = true
	stream.mu.Unlock()
	if err := service.handleProviderDoneEvent(stream, &streamProviderEvent{
		Token: 1,
		Done:  true,
		Err:   providerTerminalError{cause: &modeladapter.HTTPStatusError{Provider: "openai adapter", StatusCode: 500, Attempt: 1, MaxAttempts: 1}},
	}); err != nil {
		t.Fatalf("handleProviderDoneEvent() error = %v", err)
	}
	conversation := snapshotGateConversation(t, service, stream)
	if len(completionGateMetadataEntries(conversation.Entries)) != 0 {
		t.Fatalf("provider fail persisted a gate: %+v", conversation.Entries)
	}
	if len(completionGatePromptEntries(conversation.Entries)) != 0 {
		t.Fatal("provider fail persisted a gate reminder")
	}
	if len(metadataEntriesOfType(conversation.Entries, "turn_completed")) != 0 {
		t.Fatal("provider fail wrote turn_completed")
	}
}

func TestHandleProviderDoneCancelDoesNotRetryGate(t *testing.T) {
	service, stream := testCompletionGateStream(t, "请修改超时")
	stream.mu.Lock()
	stream.CurrentProviderToken = 1
	stream.ProviderActive = true
	stream.Status = StreamStatusStreaming
	stream.mu.Unlock()
	if err := service.handleProviderDoneEvent(stream, &streamProviderEvent{
		Token: 1,
		Done:  true,
		Err:   providerTerminalError{cause: errProviderLoopInterrupted},
	}); err != nil {
		t.Fatalf("handleProviderDoneEvent() error = %v", err)
	}
	conversation := snapshotGateConversation(t, service, stream)
	if len(completionGateMetadataEntries(conversation.Entries)) != 0 || len(metadataEntriesOfType(conversation.Entries, "turn_completed")) != 0 {
		t.Fatalf("cancel path wrote gate/turn_completed: %+v", conversation.Entries)
	}
}

func TestHandleProviderDoneSuccessEditRemindsOnceAfterFlush(t *testing.T) {
	const flushed = "I changed the timeout."
	service, stream := testCompletionGateStream(t, "请修改 main.go 的超时时间")
	stream.mu.Lock()
	stream.CurrentProviderToken = 1
	stream.ProviderActive = true
	stream.Status = StreamStatusStreaming
	stream.ProviderStreamStats.CompletionMarker = true
	stream.ProviderStreamStats.Attempt = 1
	stream.ProviderAccumulatedText = flushed
	stream.mu.Unlock()
	if err := service.handleProviderDoneEvent(stream, &streamProviderEvent{Token: 1, Done: true}); err != nil {
		t.Fatalf("handleProviderDoneEvent() error = %v", err)
	}
	conversation := snapshotGateConversation(t, service, stream)
	if len(metadataEntriesOfType(conversation.Entries, "turn_completed")) != 0 {
		t.Fatal("first insufficient wrote turn_completed on provider done")
	}
	sawFlush := false
	sawGateAfterFlush := false
	for _, entry := range conversation.Entries {
		if strings.TrimSpace(entry.Kind) == "assistant_text" {
			var payload assistantTextPayload
			if err := json.Unmarshal(entry.Payload, &payload); err != nil {
				t.Fatalf("decode assistant text: %v", err)
			}
			if payload.Text == flushed {
				sawFlush = true
			}
		}
		if _, ok := decodeCompletionGate(entry); ok && sawFlush {
			sawGateAfterFlush = true
		}
	}
	if !sawFlush {
		t.Fatal("provider done did not flush assistant text before the gate")
	}
	if !sawGateAfterFlush {
		t.Fatal("gate metadata was not persisted after flushAssistantText")
	}
	if len(completionGatePromptEntries(conversation.Entries)) != 1 {
		t.Fatalf("prompt_context count = %d, want 1", len(completionGatePromptEntries(conversation.Entries)))
	}
	stream.mu.Lock()
	action := stream.PendingProviderAction
	stream.mu.Unlock()
	if action != providerActionResume {
		t.Fatalf("pending action = %q, want resume", action)
	}
}

func TestHandleProviderDonePendingBridgeDoesNotTriggerGate(t *testing.T) {
	service, stream := testCompletionGateStream(t, "请修改超时")
	stream.mu.Lock()
	stream.CurrentProviderToken = 1
	stream.ProviderActive = true
	stream.Status = StreamStatusStreaming
	stream.ProviderStreamStats.CompletionMarker = true
	stream.PendingExecs["exec-1"] = runtimecore.PendingExec{ExecID: "exec-1", ToolCallID: "call-write-1"}
	stream.ProviderAccumulatedText = "working"
	stream.mu.Unlock()
	if err := service.handleProviderDoneEvent(stream, &streamProviderEvent{Token: 1, Done: true}); err != nil {
		t.Fatalf("handleProviderDoneEvent() error = %v", err)
	}
	conversation := snapshotGateConversation(t, service, stream)
	if len(completionGatePromptEntries(conversation.Entries)) != 0 {
		t.Fatal("pending bridge provider done wrote a gate reminder")
	}
	if len(metadataEntriesOfType(conversation.Entries, "turn_completed")) != 0 {
		t.Fatal("pending bridge provider done wrote turn_completed")
	}
	if pendingBridgeCount(stream) != 1 {
		t.Fatalf("pending bridge count = %d, want 1", pendingBridgeCount(stream))
	}
}

func TestCompleteSuccessfulTurnLeavesPendingBridgeAlone(t *testing.T) {
	service, stream := testCompletionGateStream(t, "请修改超时")
	stream.mu.Lock()
	stream.PendingExecs["exec-1"] = runtimecore.PendingExec{ExecID: "exec-1", ToolCallID: "call-write-1"}
	stream.mu.Unlock()
	if err := service.completeSuccessfulTurn(stream, gateCompletion(stream)); err != nil {
		t.Fatalf("completeSuccessfulTurn() error = %v", err)
	}
	conversation := snapshotGateConversation(t, service, stream)
	if len(completionGatePromptEntries(conversation.Entries)) != 0 {
		t.Fatal("pending bridge caused a gate resume reminder")
	}
	if len(metadataEntriesOfType(conversation.Entries, "turn_completed")) != 1 {
		t.Fatal("pending path should fall through to existing complete")
	}
}

func TestHasExplicitImperativeEditIntentNarrowScope(t *testing.T) {
	t.Parallel()
	if !hasExplicitImperativeEditIntent("请修改 main.go 的超时时间") {
		t.Fatal("narrow Chinese imperative should apply")
	}
	if !hasExplicitImperativeEditIntent("edit the timeout in main.go") {
		t.Fatal("narrow English imperative should apply")
	}
	if hasExplicitImperativeEditIntent("hello") {
		t.Fatal("plain greeting should not apply")
	}
	if hasExplicitImperativeEditIntent("what does update do?") {
		t.Fatal("Q&A containing update should not apply")
	}
	if hasExplicitImperativeEditIntent("please add more context about this function") {
		t.Fatal("request for more context should not apply")
	}
	if hasExplicitImperativeEditIntent("这段代码是做什么的？") {
		t.Fatal("Chinese Q&A should not apply")
	}
	if hasExplicitImperativeEditIntent("what does this function do?") {
		t.Fatal("English Q&A should not apply")
	}
	if hasExplicitImperativeEditIntent("how do I fix this conceptually?") {
		t.Fatal("conceptual how-to should not apply")
	}
	if hasExplicitImperativeEditIntent("update me on the current behavior") {
		t.Fatal("status update request should not apply")
	}
	if !hasExplicitImperativeEditIntent("please add a timeout") {
		t.Fatal("narrow add-timeout request should apply")
	}
	if !hasExplicitImperativeEditIntent("请修改 main.go 并解释原因") {
		t.Fatal("Chinese edit plus explain should apply")
	}
	if !hasExplicitImperativeEditIntent("please fix main.go and explain why") {
		t.Fatal("English edit plus explain should apply")
	}
	if hasExplicitImperativeEditIntent("这个文案改成什么比较好？") {
		t.Fatal("Chinese copywriting Q&A should not apply")
	}
	if hasExplicitImperativeEditIntent("请加上更多上下文（只需说明，不要修改文件）") {
		t.Fatal("advice-only add-context request should not apply")
	}
	if !hasExplicitImperativeEditIntent("请把配置改成 30 秒") {
		t.Fatal("Chinese change-to-value imperative should apply")
	}
	if !hasExplicitImperativeEditIntent("请在文件中加上字段") {
		t.Fatal("Chinese add-field-in-file imperative should apply")
	}
	long := strings.Repeat("请修改这一大段需求并实现整个系统 ", 80)
	if hasExplicitImperativeEditIntent(long) {
		t.Fatal("over-long text should not count as narrow-scope intent")
	}
}

func assertGateStatus(t *testing.T, decision completionGateDecision, status string, retry int, gaps ...string) {
	t.Helper()
	if decision.Status != status {
		t.Fatalf("status = %s, want %s gaps=%v", decision.Status, status, decision.Gaps)
	}
	if decision.RetryCount != retry {
		t.Fatalf("retry_count = %d, want %d", decision.RetryCount, retry)
	}
	if len(decision.Gaps) != len(gaps) {
		t.Fatalf("gaps = %v, want %v", decision.Gaps, gaps)
	}
	for i, gap := range gaps {
		if decision.Gaps[i] != gap {
			t.Fatalf("gaps = %v, want %v", decision.Gaps, gaps)
		}
	}
}

func mustGateEvidence(t *testing.T, input executionEvidenceInput) executionEvidenceRecord {
	t.Helper()
	return mustEvidence(t, input)
}

func gateMutationSuccessEntry(t *testing.T, sequence int64, toolCallID string) HistoryEntry {
	t.Helper()
	record := mustGateEvidence(t, executionEvidenceInput{
		TurnSeq:    1,
		RequestID:  "request-1",
		ToolCallID: toolCallID,
		ToolName:   "Write",
		ToolCall:   evidenceEditSuccessToolCall(evidencePathCanary),
		Sequence:   sequence,
	})
	entry := newExecutionEvidenceEntry(record)
	entry.Seq = sequence
	return stampExecutionEvidenceSequence(entry)
}

func gateVerificationSuccessEntry(t *testing.T, sequence int64, toolCallID string) HistoryEntry {
	t.Helper()
	record := mustGateEvidence(t, executionEvidenceInput{
		TurnSeq:         1,
		RequestID:       "request-1",
		ToolCallID:      toolCallID,
		ToolName:        "Shell",
		ArgsJSON:        []byte(`{"command":"` + evidenceCommandCanary + `"}`),
		ToolCall:        evidenceShellSuccessToolCall(evidenceCommandCanary, 0),
		Sequence:        sequence,
		ResultText:      evidenceStdoutCanary,
		CommandOverride: evidenceCommandCanary,
	})
	entry := newExecutionEvidenceEntry(record)
	entry.Seq = sequence
	return stampExecutionEvidenceSequence(entry)
}

func gateToolCallEntry(turnSeq int64, requestID string, toolCallID string, toolName string) HistoryEntry {
	return newToolCallEntry(turnSeq, requestID, toolCallID, toolName, "", "", nil)
}

func gateWriteToolCallEntry(t *testing.T, turnSeq int64, requestID string, toolCallID string) HistoryEntry {
	t.Helper()
	payload, err := protojson.Marshal(&agentv1.ToolCall{
		Tool: &agentv1.ToolCall_EditToolCall{
			EditToolCall: &agentv1.EditToolCall{
				Args: &agentv1.EditArgs{Path: "main.go"},
			},
		},
	})
	if err != nil {
		t.Fatalf("marshal write tool call: %v", err)
	}
	return newToolCallEntry(turnSeq, requestID, toolCallID, "Write", "", "", payload)
}

func gateShellToolCallEntry(t *testing.T, turnSeq int64, requestID string, toolCallID string, command string) HistoryEntry {
	t.Helper()
	payload, err := protojson.Marshal(&agentv1.ToolCall{
		Tool: &agentv1.ToolCall_ShellToolCall{
			ShellToolCall: &agentv1.ShellToolCall{
				Args: &agentv1.ShellArgs{Command: command},
			},
		},
	})
	if err != nil {
		t.Fatalf("marshal shell tool call: %v", err)
	}
	return newToolCallEntry(turnSeq, requestID, toolCallID, "Shell", "", "", payload)
}

func gateToolResultEntry(turnSeq int64, requestID string, toolCallID string, toolName string) HistoryEntry {
	return newToolResultEntry(turnSeq, requestID, toolCallID, toolName, "", "ok", "", nil)
}

func withHistoryRequestID(entry HistoryEntry, requestID string) HistoryEntry {
	entry.RequestID = requestID
	return entry
}

func testCompletionGateStream(t *testing.T, userText string) (*Service, *ActiveStream) {
	t.Helper()
	service, stream := testExecutionEvidenceStream(t)
	stream.mu.Lock()
	stream.LatestUserText = userText
	stream.CurrentModelCallID = "model-call-1"
	stream.ProviderPassCount = 1
	stream.Status = StreamStatusStreaming
	stream.Phase = TurnPhaseProviderRunning
	stream.mu.Unlock()
	t.Cleanup(func() {
		invalidateGateResumeTimer(stream)
		stream.mu.Lock()
		stream.Status = StreamStatusCompleted
		stream.mu.Unlock()
	})
	return service, stream
}

func cloneCompletionGateStream(t *testing.T, service *Service, original *ActiveStream, conversation *ConversationFile) *ActiveStream {
	t.Helper()
	restart, err := service.broker.OpenStream(
		"request-restart-1", original.ConversationID, original.TurnSeq, original.ModelID, original.ModelName,
		original.Mode, original.LatestUserText,
	)
	if err != nil {
		t.Fatalf("OpenStream() error = %v", err)
	}
	if err := service.replaceCheckpointConversation(restart, conversation); err != nil {
		t.Fatalf("replaceCheckpointConversation() error = %v", err)
	}
	restart.mu.Lock()
	restart.LatestUserText = original.LatestUserText
	restart.CurrentModelCallID = "model-call-restart"
	restart.RequestID = original.RequestID
	restart.Status = StreamStatusStreaming
	restart.mu.Unlock()
	t.Cleanup(func() {
		invalidateGateResumeTimer(restart)
		restart.mu.Lock()
		restart.Status = StreamStatusCompleted
		restart.mu.Unlock()
	})
	return restart
}

func gateCompletion(stream *ActiveStream) pendingTurnCompletion {
	return pendingTurnCompletion{
		ConversationID: stream.ConversationID,
		RequestID:      stream.RequestID,
		TurnSeq:        stream.TurnSeq,
		ModelCallID:    firstNonEmpty(stream.CurrentModelCallID, "model-call-1"),
		ProviderPass:   1,
	}
}

func snapshotGateConversation(t *testing.T, service *Service, stream *ActiveStream) *ConversationFile {
	t.Helper()
	conversation, _, _, err := service.snapshotCheckpointConversation(stream)
	if err != nil {
		t.Fatalf("snapshotCheckpointConversation() error = %v", err)
	}
	return conversation
}

func invalidateGateResumeTimer(stream *ActiveStream) {
	if stream == nil {
		return
	}
	stream.mu.Lock()
	defer stream.mu.Unlock()
	if stream.TimerTokens == nil {
		stream.TimerTokens = make(map[string]uint64)
	}
	for key := range stream.TimerTokens {
		stream.TimerTokens[key]++
	}
	if stream.PendingProviderAction == providerActionResume {
		stream.PendingProviderAction = providerActionNone
	}
}

func completionGateMetadataEntries(entries []HistoryEntry) []HistoryEntry {
	found := make([]HistoryEntry, 0)
	for _, entry := range entries {
		if _, ok := decodeCompletionGate(entry); ok {
			found = append(found, entry)
		}
	}
	return found
}

func completionGatePromptEntries(entries []HistoryEntry) []HistoryEntry {
	found := make([]HistoryEntry, 0)
	for _, entry := range entries {
		if strings.TrimSpace(entry.Kind) != "prompt_context" {
			continue
		}
		var payload promptContextEntryPayload
		if err := json.Unmarshal(entry.Payload, &payload); err != nil {
			continue
		}
		if strings.TrimSpace(payload.Source) == promptContextSourceCompletionEvidenceGate {
			found = append(found, entry)
		}
	}
	return found
}

func metadataEntriesOfType(entries []HistoryEntry, eventType string) []HistoryEntry {
	found := make([]HistoryEntry, 0)
	eventType = strings.TrimSpace(eventType)
	for _, entry := range entries {
		if strings.TrimSpace(entry.Kind) != "metadata" {
			continue
		}
		var payload metadataPayload
		if err := json.Unmarshal(entry.Payload, &payload); err != nil {
			continue
		}
		if strings.TrimSpace(payload.Type) == eventType {
			found = append(found, entry)
		}
	}
	return found
}

func assertCompletionGateWhitelist(t *testing.T, entry HistoryEntry) {
	t.Helper()
	var payload metadataPayload
	if err := json.Unmarshal(entry.Payload, &payload); err != nil {
		t.Fatalf("decode metadata payload: %v", err)
	}
	if payload.Type != completionGatePayloadType {
		t.Fatalf("payload type = %q, want %s", payload.Type, completionGatePayloadType)
	}
	allowed := map[string]struct{}{
		"schema_version":             {},
		"status":                     {},
		"retry_count":                {},
		"gaps":                       {},
		"turn_seq":                   {},
		"request_id":                 {},
		"mutation_tool_count":        {},
		"verification_command_count": {},
		"verification_stale":         {},
		"verification_evidence":      {},
		"verification_evidence_id":   {},
	}
	for key := range payload.Value {
		if _, ok := allowed[key]; !ok {
			t.Fatalf("unexpected gate field %q in %s", key, entry.Payload)
		}
	}
	encoded, err := json.Marshal(payload.Value)
	if err != nil {
		t.Fatalf("marshal value: %v", err)
	}
	if bytes.Contains(encoded, []byte(evidenceCommandCanary)) || bytes.Contains(encoded, []byte(evidencePathCanary)) || bytes.Contains(encoded, []byte(evidenceReasoningCanary)) {
		t.Fatalf("sensitive canary leaked in gate value: %s", encoded)
	}
}

func mustJSON(t *testing.T, value any) []byte {
	t.Helper()
	encoded, err := json.Marshal(value)
	if err != nil {
		t.Fatalf("json.Marshal: %v", err)
	}
	return encoded
}

func TestEvaluateCompletionGateAssistantTextIsNeverEvidence(t *testing.T) {
	decision := evaluateCompletionEvidenceGate(completionGateInput{
		Mode:           agentv1.AgentMode_AGENT_MODE_AGENT,
		LatestUserText: "请修改超时",
		TurnSeq:        1,
		RequestID:      "request-1",
		Entries: []HistoryEntry{
			newAssistantTextEntryWithProviderMetadata(1, "request-1", "Patched "+evidencePathCanary+" and ran "+evidenceCommandCanary, evidenceReasoningCanary, "sig", "provider", "item-1", "completed", json.RawMessage(`{"summary":"`+evidenceStdoutCanary+`"}`)),
		},
	})
	assertGateStatus(t, decision, completionGateStatusInsufficientFirst, 1, completionGateGapMissingSuccessfulMutation)
	if decision.Summary.MutationToolCount != 0 {
		t.Fatalf("assistant text counted as mutation: %+v", decision.Summary)
	}
}

func TestCompletionGateIdempotencyKeyStable(t *testing.T) {
	first := completionGateIdempotencyKey(1, "request-1", completionGateStatusInsufficientFirst)
	second := completionGateIdempotencyKey(1, "request-1", completionGateStatusInsufficientFirst)
	if first != second || !strings.Contains(first, "retry") {
		t.Fatalf("idempotency key unstable: %q %q", first, second)
	}
	after := completionGateIdempotencyKey(1, "request-1", completionGateStatusInsufficientAfterRetry)
	if after == first {
		t.Fatal("after-retry key collided with first-retry key")
	}
}

func TestFilterHistoryForTurnRequestKeepsEmptyRequestIDsOnSameTurn(t *testing.T) {
	entries := []HistoryEntry{
		{TurnSeq: 1, RequestID: "", Kind: "assistant_text"},
		{TurnSeq: 1, RequestID: "request-1", Kind: "metadata"},
		{TurnSeq: 1, RequestID: "request-other", Kind: "metadata"},
		{TurnSeq: 2, RequestID: "request-1", Kind: "metadata"},
	}
	filtered := filterHistoryForTurnRequest(entries, 1, "request-1")
	if len(filtered) != 2 {
		t.Fatalf("filtered = %d, want 2", len(filtered))
	}
}

func TestHandleProviderDoneTruncatedDoesNotRetryGate(t *testing.T) {
	service, stream := testCompletionGateStream(t, "请修改超时")
	stream.mu.Lock()
	stream.CurrentProviderToken = 1
	stream.ProviderActive = true
	stream.Status = StreamStatusStreaming
	stream.mu.Unlock()
	if err := service.handleProviderDoneEvent(stream, &streamProviderEvent{Token: 1, Done: true}); err != nil {
		t.Fatalf("handleProviderDoneEvent() error = %v", err)
	}
	conversation := snapshotGateConversation(t, service, stream)
	if len(completionGateMetadataEntries(conversation.Entries)) != 0 || len(metadataEntriesOfType(conversation.Entries, "turn_completed")) != 0 {
		t.Fatalf("truncated path wrote gate/turn_completed: %+v", conversation.Entries)
	}
}

func TestHandleProviderDoneGateRetryFinalizesModelCallWithoutTurnComplete(t *testing.T) {
	service, stream := testCompletionGateStream(t, "请修改 main.go 的超时时间")
	capture := &debugRecorderTestCapture{}
	service.debug = newDebugRecorder(t.TempDir(), service.broker, debugRecorderTestConfig("basic"), capture)
	t.Cleanup(service.debug.Close)
	stream.mu.Lock()
	stream.CurrentProviderToken = 1
	stream.ProviderActive = true
	stream.Status = StreamStatusStreaming
	stream.ProviderStreamStats.CompletionMarker = true
	stream.ProviderStreamStats.Attempt = 1
	stream.ProviderAccumulatedText = "I changed the timeout."
	stream.mu.Unlock()
	if err := service.handleProviderDoneEvent(stream, &streamProviderEvent{Token: 1, Done: true}); err != nil {
		t.Fatalf("handleProviderDoneEvent() error = %v", err)
	}
	conversation := snapshotGateConversation(t, service, stream)
	if len(metadataEntriesOfType(conversation.Entries, "turn_completed")) != 0 {
		t.Fatal("gate retry wrote turn_completed")
	}
	if len(completionGatePromptEntries(conversation.Entries)) != 1 {
		t.Fatalf("prompt_context count = %d, want 1", len(completionGatePromptEntries(conversation.Entries)))
	}
	stream.mu.Lock()
	status := stream.Status
	action := stream.PendingProviderAction
	_, finalized := stream.FinalizedModelCallIDs["model-call-1"]
	lastCall := ""
	if stream.CheckpointConversation != nil && stream.CheckpointConversation.LastProviderCall != nil {
		lastCall = stream.CheckpointConversation.LastProviderCall.Status
	}
	stream.mu.Unlock()
	if status == StreamStatusCompleted {
		t.Fatal("gate retry completed the stream")
	}
	if action != providerActionResume {
		t.Fatalf("pending action = %q, want resume", action)
	}
	if !finalized {
		t.Fatal("gate retry did not finalize the current model call")
	}
	if got := countCapturedEvent(capture, "model_call_final"); got != 1 {
		t.Fatalf("model_call_final count = %d, want 1", got)
	}
	if lastCall != "completed" {
		t.Fatalf("usage status = %q, want completed before gate retry", lastCall)
	}
	acknowledgeCheckpointBlobs(t, service, stream)
	conversation = snapshotGateConversation(t, service, stream)
	if len(metadataEntriesOfType(conversation.Entries, "turn_completed")) != 0 {
		t.Fatal("checkpoint ACK after gate retry wrote turn_completed")
	}
	stream.mu.Lock()
	status = stream.Status
	stream.mu.Unlock()
	if status == StreamStatusCompleted {
		t.Fatal("checkpoint ACK after gate retry completed the stream")
	}
}

func TestCompleteSuccessfulTurnDuplicateDoesNotRetriggerGate(t *testing.T) {
	service, stream := testCompletionGateStream(t, "请修改超时")
	if err := service.completeSuccessfulTurn(stream, gateCompletion(stream)); err != nil {
		t.Fatalf("first completeSuccessfulTurn() error = %v", err)
	}
	conversation := snapshotGateConversation(t, service, stream)
	if len(completionGatePromptEntries(conversation.Entries)) != 1 {
		t.Fatalf("first complete prompt_context count = %d, want 1", len(completionGatePromptEntries(conversation.Entries)))
	}
	stream.mu.Lock()
	stream.Status = StreamStatusStreaming
	action := stream.PendingProviderAction
	stream.mu.Unlock()
	if action != providerActionResume {
		t.Fatalf("pending action = %q, want resume", action)
	}
	if err := service.completeSuccessfulTurn(stream, gateCompletion(stream)); err != nil {
		t.Fatalf("second completeSuccessfulTurn() error = %v", err)
	}
	conversation = snapshotGateConversation(t, service, stream)
	if len(completionGatePromptEntries(conversation.Entries)) != 1 {
		t.Fatalf("duplicate complete prompt_context count = %d, want 1", len(completionGatePromptEntries(conversation.Entries)))
	}
	if len(completionGateMetadataEntries(conversation.Entries)) != 1 {
		t.Fatalf("duplicate complete gate metadata count = %d, want 1", len(completionGateMetadataEntries(conversation.Entries)))
	}
	if len(metadataEntriesOfType(conversation.Entries, "turn_completed")) != 0 {
		t.Fatal("duplicate complete wrote turn_completed")
	}
	stream.mu.Lock()
	status := stream.Status
	action = stream.PendingProviderAction
	stream.mu.Unlock()
	if status == StreamStatusCompleted {
		t.Fatal("duplicate complete completed the stream")
	}
	if action != providerActionResume {
		t.Fatalf("duplicate complete cleared resume action = %q", action)
	}
}

func TestCompleteSuccessfulTurnDoesNotRetriggerAfterReminder(t *testing.T) {
	service, stream := testCompletionGateStream(t, "请修改超时")
	if err := service.completeSuccessfulTurn(stream, gateCompletion(stream)); err != nil {
		t.Fatalf("first completeSuccessfulTurn() error = %v", err)
	}
	if err := service.completeSuccessfulTurn(stream, gateCompletion(stream)); err != nil {
		t.Fatalf("second completeSuccessfulTurn() error = %v", err)
	}
	conversation := snapshotGateConversation(t, service, stream)
	if len(completionGatePromptEntries(conversation.Entries)) != 1 {
		t.Fatalf("prompt_context count = %d, want 1", len(completionGatePromptEntries(conversation.Entries)))
	}
	if len(completionGateMetadataEntries(conversation.Entries)) != 1 {
		t.Fatalf("completion_evidence_gate metadata count = %d, want 1", len(completionGateMetadataEntries(conversation.Entries)))
	}
	if len(metadataEntriesOfType(conversation.Entries, "turn_completed")) != 0 {
		t.Fatal("duplicate complete after reminder wrote turn_completed")
	}
}

func TestCompleteSuccessfulTurnAfterFailedThenSuccessfulVerification(t *testing.T) {
	service, stream := testCompletionGateStream(t, "请修改超时")
	if err := service.appendToolResult(stream, "call-write-1", "Write", []byte(`{"path":"`+evidencePathCanary+`"}`), evidenceFileBodyCanary, evidenceReasoningCanary, evidenceEditSuccessToolCall(evidencePathCanary), "model-call-1"); err != nil {
		t.Fatalf("append mutation: %v", err)
	}
	if err := service.appendToolResult(stream, "call-test-fail", "Shell", []byte(`{"command":"`+evidenceCommandCanary+`"}`), "lint failed", "", evidenceShellSuccessToolCall(evidenceCommandCanary, 1), "model-call-1"); err != nil {
		t.Fatalf("append failed verification: %v", err)
	}
	if err := service.appendToolResult(stream, "call-test-ok", "Shell", []byte(`{"command":"`+evidenceCommandCanary+`"}`), evidenceStdoutCanary, "", evidenceShellSuccessToolCall(evidenceCommandCanary, 0), "model-call-1"); err != nil {
		t.Fatalf("append successful verification: %v", err)
	}
	if err := service.completeSuccessfulTurn(stream, gateCompletion(stream)); err != nil {
		t.Fatalf("completeSuccessfulTurn() error = %v", err)
	}
	conversation := snapshotGateConversation(t, service, stream)
	if len(completionGatePromptEntries(conversation.Entries)) != 0 {
		t.Fatalf("later successful verification still retried: %+v", conversation.Entries)
	}
	if len(metadataEntriesOfType(conversation.Entries, "turn_completed")) != 1 {
		t.Fatalf("turn_completed count = %d, want 1", len(metadataEntriesOfType(conversation.Entries, "turn_completed")))
	}
}

func TestCompleteSuccessfulTurnQADoesNotRetrigger(t *testing.T) {
	service, stream := testCompletionGateStream(t, "这段代码是做什么的？")
	if err := service.completeSuccessfulTurn(stream, gateCompletion(stream)); err != nil {
		t.Fatalf("completeSuccessfulTurn() error = %v", err)
	}
	conversation := snapshotGateConversation(t, service, stream)
	if len(completionGatePromptEntries(conversation.Entries)) != 0 {
		t.Fatalf("Q&A reply retried: %+v", conversation.Entries)
	}
	if len(metadataEntriesOfType(conversation.Entries, "turn_completed")) != 1 {
		t.Fatalf("turn_completed count = %d, want 1", len(metadataEntriesOfType(conversation.Entries, "turn_completed")))
	}
}

func TestCompleteSuccessfulTurnQAExecutionClaimDoesNotTrigger(t *testing.T) {
	service, stream := testCompletionGateStream(t, "这段代码是做什么的？")
	if _, err := service.appendConversationEntries(stream, stream.ConversationID, []HistoryEntry{
		newAssistantTextEntry(stream.TurnSeq, stream.RequestID, "I changed the timeout.", evidenceReasoningCanary, ""),
	}); err != nil {
		t.Fatalf("append assistant claim: %v", err)
	}
	if err := service.completeSuccessfulTurn(stream, gateCompletion(stream)); err != nil {
		t.Fatalf("completeSuccessfulTurn() error = %v", err)
	}
	conversation := snapshotGateConversation(t, service, stream)
	if len(completionGatePromptEntries(conversation.Entries)) != 0 {
		t.Fatalf("assistant execution claim on Q&A retried: %+v", conversation.Entries)
	}
	if len(metadataEntriesOfType(conversation.Entries, "turn_completed")) != 1 {
		t.Fatalf("turn_completed count = %d, want 1", len(metadataEntriesOfType(conversation.Entries, "turn_completed")))
	}
}
