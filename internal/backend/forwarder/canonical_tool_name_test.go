package forwarder

import (
	"encoding/json"
	"strings"
	"testing"

	"google.golang.org/protobuf/encoding/protojson"

	"cursor/gen/agentv1"
	execbridge "cursor/internal/backend/agent/bridge/exec"
	runtimecore "cursor/internal/backend/agent/core"
	modeladapter "cursor/internal/backend/agent/model"
)

var canonicalShellAliasNames = []string{"Shell", "shell", "Bash", "bash"}

const canonicalShellAliasCommand = "echo alias-fixture"

func TestCatalogStillPublishesOnlyCanonicalShell(t *testing.T) {
	_, names, err := NewToolCatalog().Load(agentv1.AgentMode_AGENT_MODE_AGENT, "")
	if err != nil {
		t.Fatalf("catalog.Load() error = %v", err)
	}
	if !containsString(names, "Shell") {
		t.Fatal("catalog must still publish Shell")
	}
	for _, alias := range []string{"shell", "Bash", "bash"} {
		if containsString(names, alias) {
			t.Fatalf("catalog published alias %q; catalog must stay Shell-only", alias)
		}
		if isKnownToolName(alias) {
			t.Fatalf("isKnownToolName(%q) = true, aliases must not expand the catalog", alias)
		}
		if isToolAllowedInMode(agentv1.AgentMode_AGENT_MODE_AGENT, "", alias) {
			t.Fatalf("isToolAllowedInMode(%q) = true, aliases must not bypass permissions", alias)
		}
	}
}

func TestShellAliasNamesShareCanonicalInvocationChain(t *testing.T) {
	argsJSON := []byte(`{"command":"` + canonicalShellAliasCommand + `"}`)
	var (
		wantStarted *agentv1.ToolCall
		wantReplay  []modeladapter.Message
		wantLimit   string
		wantKind    string
	)
	largeReplay := strings.Repeat("x", projectedShellReplayLimit+64)
	wantLimit = limitProjectedToolResultReplay("Shell", largeReplay, "", false, false)

	for _, name := range canonicalShellAliasNames {
		name := name
		t.Run(name, func(t *testing.T) {
			service, stream := testCanonicalShellAliasStream(t)
			callID := "call-alias-1"

			placeholder := buildStartedToolCall(runtimecore.ToolInvocation{
				CallID:   callID,
				ToolName: name,
				ArgsJSON: argsJSON,
			})
			if placeholder == nil || placeholder.GetShellToolCall() == nil {
				t.Fatalf("placeholder ToolCall for %q is not Shell", name)
			}
			if got := placeholder.GetShellToolCall().GetArgs().GetToolCallId(); got != callID {
				t.Fatalf("placeholder call id = %q, want %q", got, callID)
			}
			if got := placeholder.GetShellToolCall().GetArgs().GetCommand(); got != canonicalShellAliasCommand {
				t.Fatalf("placeholder command = %q, want %q", got, canonicalShellAliasCommand)
			}
			if wantStarted == nil {
				wantStarted = placeholder
			}

			if err := service.applyProviderModelEvent(stream, modeladapter.ModelEvent{
				Kind:       modeladapter.ModelEventKindPartialToolCall,
				ToolCallID: callID,
				ToolCall:   placeholder,
			}); err != nil {
				t.Fatalf("partial applyProviderModelEvent() error = %v", err)
			}
			stream.mu.Lock()
			pendingAfterPartial := len(stream.PendingExecs)
			partialRecorded := 0
			if stream.PartialToolCallIDs != nil {
				if _, ok := stream.PartialToolCallIDs[callID]; ok {
					partialRecorded = 1
				}
			}
			stream.mu.Unlock()
			if pendingAfterPartial != 0 || partialRecorded != 1 {
				t.Fatalf("partial path pending=%d recorded=%d, want placeholder without execution", pendingAfterPartial, partialRecorded)
			}

			if err := service.applyProviderModelEvent(stream, modeladapter.ModelEvent{
				Kind: modeladapter.ModelEventKindToolLikeCompleted,
				ToolInvocation: &runtimecore.ToolInvocation{
					CallID:      callID,
					ToolName:    name,
					ArgsJSON:    argsJSON,
					ModelCallID: "model-call-1",
				},
			}); err != nil {
				t.Fatalf("completed applyProviderModelEvent() error = %v", err)
			}

			pending := canonicalShellPendingExec(t, stream)
			if pending.ExecKind != "shell" {
				t.Fatalf("pending exec kind = %q, want shell", pending.ExecKind)
			}
			if pending.ToolCallID != callID {
				t.Fatalf("pending tool call id = %q, want %q", pending.ToolCallID, callID)
			}
			if string(pending.ArgsJSON) != string(argsJSON) {
				t.Fatalf("pending args = %s, want unchanged %s", pending.ArgsJSON, argsJSON)
			}

			if err := service.handleExecResult(InboundIntent{
				Kind:      "exec_result",
				RequestID: stream.RequestID,
				ExecClientMessage: &agentv1.ExecClientMessage{
					Id:     pending.MessageID,
					ExecId: pending.ExecID,
					Message: &agentv1.ExecClientMessage_ShellStream{
						ShellStream: &agentv1.ShellStream{
							Event: &agentv1.ShellStream_Exit{
								Exit: &agentv1.ShellStreamExit{Code: 0},
							},
						},
					},
				},
			}); err != nil {
				t.Fatalf("handleExecResult() error = %v", err)
			}

			stream.mu.Lock()
			pendingAfterResult := len(stream.PendingExecs)
			stream.mu.Unlock()
			if pendingAfterResult != 0 {
				t.Fatalf("pending execs after result = %d, want 0", pendingAfterResult)
			}

			conversation, _, _, err := service.snapshotCheckpointConversation(stream)
			if err != nil {
				t.Fatalf("snapshotCheckpointConversation() error = %v", err)
			}
			toolCall := mustHistoryToolPayload(t, conversation.Entries, "tool_call", callID)
			if toolCall.ToolName != "Shell" {
				t.Fatalf("new tool_call name = %q, want canonical Shell (provider name was %q)", toolCall.ToolName, name)
			}
			if strings.Contains(string(toolCall.raw), "nonexistent tool") {
				t.Fatalf("tool_call stored unknown-tool failure for %q: %s", name, toolCall.raw)
			}
			toolResult := mustHistoryToolPayload(t, conversation.Entries, "tool_result", callID)
			if toolResult.ToolName != "Shell" {
				t.Fatalf("new tool_result name = %q, want canonical Shell", toolResult.ToolName)
			}
			if toolResult.Arguments != "" && toolResult.Arguments != string(argsJSON) {
				t.Fatalf("tool_result args = %s, want unchanged", toolResult.Arguments)
			}

			evidence, ok := decodeCanonicalShellEvidence(conversation.Entries, callID)
			if !ok {
				t.Fatal("missing execution evidence for shell alias result")
			}
			if evidence.ToolKind != executionEvidenceToolKindShell {
				t.Fatalf("evidence tool_kind = %q, want %q", evidence.ToolKind, executionEvidenceToolKindShell)
			}
			if wantKind == "" {
				wantKind = evidence.ToolKind
			} else if evidence.ToolKind != wantKind {
				t.Fatalf("evidence tool_kind = %q, want same as Shell %q", evidence.ToolKind, wantKind)
			}

			replay, err := service.projector.ProjectPromptReplay(conversation)
			if err != nil {
				t.Fatalf("ProjectPromptReplay() error = %v", err)
			}
			assertCanonicalShellReplay(t, replay, callID)
			if wantReplay == nil {
				wantReplay = replay
			} else if !canonicalShellReplayCompatible(wantReplay, replay) {
				t.Fatalf("replay for %q diverged from canonical Shell replay", name)
			}

			stream.mu.Lock()
			pendingAfterReplay := len(stream.PendingExecs)
			stream.mu.Unlock()
			if pendingAfterReplay != 0 {
				t.Fatalf("replay created pending execs = %d", pendingAfterReplay)
			}

			gotLimit := limitProjectedToolResultReplay(name, largeReplay, "", false, false)
			if gotLimit != wantLimit {
				t.Fatalf("truncation budget for %q diverged from Shell", name)
			}

			assertCanonicalShellEvents(t, service, stream, callID)
		})
	}
}

func TestLegacyShellAliasHistoryReplaysWithoutRewriteOrRerun(t *testing.T) {
	argsJSON := `{"command":"` + canonicalShellAliasCommand + `"}`
	started := buildStartedToolCall(runtimecore.ToolInvocation{
		CallID:   "legacy-call",
		ToolName: "Shell",
		ArgsJSON: []byte(argsJSON),
	})
	startedPayload, err := protojson.Marshal(started)
	if err != nil {
		t.Fatalf("marshal started tool call: %v", err)
	}
	completed := evidenceShellSuccessToolCall(canonicalShellAliasCommand, 0)
	completedPayload, err := protojson.Marshal(completed)
	if err != nil {
		t.Fatalf("marshal completed tool call: %v", err)
	}

	largeStdout := strings.Repeat("y", projectedShellStreamLimit+32)
	resultJSON := `{"stdout":"` + largeStdout + `","stderr":""}`
	shellLimit, _ := projectedToolReplayLimit("Shell")
	wantTruncated := limitProjectedToolResultReplay("Shell", resultJSON, "", false, true)

	for _, name := range []string{"shell", "Bash", "bash"} {
		name := name
		t.Run(name, func(t *testing.T) {
			toolCallEntry := newToolCallEntry(1, "request-1", "legacy-call", name, "", "", startedPayload)
			toolResultEntry := newToolResultEntry(1, "request-1", "legacy-call", name, argsJSON, resultJSON, "", completedPayload)
			originalCall := append([]byte(nil), toolCallEntry.Payload...)
			originalResult := append([]byte(nil), toolResultEntry.Payload...)

			conversation := &ConversationFile{Entries: []HistoryEntry{toolCallEntry, toolResultEntry}}
			record, ok := buildExecutionEvidence(executionEvidenceInput{
				TurnSeq:    1,
				RequestID:  "request-1",
				ToolCallID: "legacy-call",
				ToolName:   name,
				ArgsJSON:   []byte(argsJSON),
				ToolCall:   completed,
			})
			if !ok {
				t.Fatal("legacy alias should classify as shell evidence")
			}
			if record.ToolKind != executionEvidenceToolKindShell {
				t.Fatalf("legacy evidence tool_kind = %q, want shell", record.ToolKind)
			}

			messages, err := NewHistoryProjector().ProjectPromptReplay(conversation)
			if err != nil {
				t.Fatalf("ProjectPromptReplay() error = %v", err)
			}
			assertCanonicalShellReplay(t, messages, "legacy-call")
			if got, _ := projectedToolReplayLimit(name); got != shellLimit {
				t.Fatalf("legacy truncation limit = %d, want %d", got, shellLimit)
			}
			if got := limitProjectedToolResultReplay(name, resultJSON, "", false, true); got != wantTruncated {
				t.Fatalf("legacy truncation output diverged from Shell")
			}
			if string(conversation.Entries[0].Payload) != string(originalCall) || string(conversation.Entries[1].Payload) != string(originalResult) {
				t.Fatal("legacy disk history payloads were rewritten")
			}
			var stored toolCallEntryPayload
			if err := json.Unmarshal(conversation.Entries[0].Payload, &stored); err != nil {
				t.Fatalf("decode stored tool_call: %v", err)
			}
			if stored.ToolName != name {
				t.Fatalf("stored history name = %q, want original %q", stored.ToolName, name)
			}
		})
	}
}

func testCanonicalShellAliasStream(t *testing.T) (*Service, *ActiveStream) {
	t.Helper()
	service, stream, _ := testCheckpointBlobProjection(t)
	service.execBridge = execbridge.NewBridge()
	stream.mu.Lock()
	stream.CurrentProviderToken = 1
	stream.CurrentModelCallID = "model-call-1"
	stream.ProviderActive = true
	stream.Status = StreamStatusStreaming
	stream.Phase = TurnPhaseProviderRunning
	stream.mu.Unlock()
	return service, stream
}

func canonicalShellPendingExec(t *testing.T, stream *ActiveStream) runtimecore.PendingExec {
	t.Helper()
	stream.mu.Lock()
	defer stream.mu.Unlock()
	if len(stream.PendingExecs) != 1 {
		t.Fatalf("pending execs = %d, want exactly 1 execution", len(stream.PendingExecs))
	}
	for _, pending := range stream.PendingExecs {
		return pending
	}
	t.Fatal("missing pending exec")
	return runtimecore.PendingExec{}
}

type canonicalHistoryToolPayload struct {
	ToolName  string
	Arguments string
	raw       []byte
}

func mustHistoryToolPayload(t *testing.T, entries []HistoryEntry, kind string, callID string) canonicalHistoryToolPayload {
	t.Helper()
	for _, entry := range entries {
		if entry.Kind != kind || entry.ToolCallID != callID {
			continue
		}
		switch kind {
		case "tool_call":
			var payload toolCallEntryPayload
			if err := json.Unmarshal(entry.Payload, &payload); err != nil {
				t.Fatalf("decode tool_call: %v", err)
			}
			return canonicalHistoryToolPayload{ToolName: payload.ToolName, raw: entry.Payload}
		case "tool_result":
			var payload toolResultEntryPayload
			if err := json.Unmarshal(entry.Payload, &payload); err != nil {
				t.Fatalf("decode tool_result: %v", err)
			}
			return canonicalHistoryToolPayload{ToolName: payload.ToolName, Arguments: payload.Arguments, raw: entry.Payload}
		}
	}
	t.Fatalf("missing %s entry for %s", kind, callID)
	return canonicalHistoryToolPayload{}
}

func decodeCanonicalShellEvidence(entries []HistoryEntry, callID string) (executionEvidenceRecord, bool) {
	for _, entry := range entries {
		record, ok := decodeExecutionEvidence(entry)
		if ok && record.ToolCallID == callID {
			return record, true
		}
	}
	return executionEvidenceRecord{}, false
}

func assertCanonicalShellReplay(t *testing.T, messages []modeladapter.Message, callID string) {
	t.Helper()
	foundCall := false
	foundResult := false
	for _, message := range messages {
		for _, toolCall := range message.ToolCalls {
			if toolCall.ID != callID {
				continue
			}
			foundCall = true
			if toolCall.Function.Name != "Shell" {
				t.Fatalf("replayed tool name = %q, want Shell", toolCall.Function.Name)
			}
		}
		if message.Role == "tool" && message.ToolCallID == callID {
			foundResult = true
			if message.Name != "Shell" {
				t.Fatalf("replayed tool result name = %q, want Shell", message.Name)
			}
		}
	}
	if !foundCall || !foundResult {
		t.Fatalf("replay missing call/result for %s: call=%v result=%v", callID, foundCall, foundResult)
	}
}

func canonicalShellReplayCompatible(left []modeladapter.Message, right []modeladapter.Message) bool {
	if len(left) != len(right) {
		return false
	}
	for index := range left {
		if left[index].Role != right[index].Role {
			return false
		}
		if len(left[index].ToolCalls) != len(right[index].ToolCalls) {
			return false
		}
		for toolIndex := range left[index].ToolCalls {
			if left[index].ToolCalls[toolIndex].Function.Name != right[index].ToolCalls[toolIndex].Function.Name {
				return false
			}
		}
		if left[index].Name != right[index].Name {
			return false
		}
	}
	return true
}

func assertCanonicalShellEvents(t *testing.T, service *Service, stream *ActiveStream, callID string) {
	t.Helper()
	foundStarted := false
	foundExec := false
	foundCompleted := false
	for _, event := range readCheckpointTestEvents(t, service, stream) {
		if started := event.Message.GetInteractionUpdate().GetToolCallStarted(); started != nil && started.GetCallId() == callID {
			foundStarted = true
			if started.GetToolCall().GetShellToolCall() == nil {
				t.Fatal("ToolCallStarted placeholder is not Shell")
			}
		}
		if completed := event.Message.GetInteractionUpdate().GetToolCallCompleted(); completed != nil && completed.GetCallId() == callID {
			foundCompleted = true
			if completed.GetToolCall().GetShellToolCall() == nil {
				t.Fatal("ToolCallCompleted is not Shell")
			}
		}
		if exec := event.Message.GetExecServerMessage(); exec != nil && exec.GetShellStreamArgs() != nil {
			foundExec = true
			if exec.GetShellStreamArgs().GetToolCallId() != callID {
				t.Fatalf("exec tool call id = %q, want %q", exec.GetShellStreamArgs().GetToolCallId(), callID)
			}
			if exec.GetShellStreamArgs().GetCommand() != canonicalShellAliasCommand {
				t.Fatalf("exec command = %q, want unchanged", exec.GetShellStreamArgs().GetCommand())
			}
		}
	}
	if !foundStarted || !foundExec || !foundCompleted {
		t.Fatalf("event chain started=%v exec=%v completed=%v", foundStarted, foundExec, foundCompleted)
	}
}
