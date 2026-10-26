package forwarder

import (
	"bufio"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"cursor/gen/agentv1"
	"cursor/internal/observability"
)

const compactionDiagnosticCanary = "COMPACTION_DIAGNOSTIC_BODY_CANARY_DO_NOT_EMIT"

func compactionDiagnosticTestFields() map[string]any {
	return map[string]any{
		"compaction_sequence":       2,
		"compaction_layout_version": int64(1),
		"prefix_start":              0,
		"prefix_end":                4,
		"tail_start":                4,
		"pinned_input_count":        1,
		"completed_batch_count":     3,
		"compiled_tokens_before":    int64(160000),
		"compiled_tokens_after":     int64(64000),
		"budget_tokens":             int64(200000),
		"pinned_input_tokens":       int64(1200),
		"current_input_tokens":      int64(1500),
		"tail_tokens":               int64(4000),
		"blocked_reason":            "pinned_input_exceeds_budget",
		"reason":                    "already_compacted_batch",
		"phase":                     "compaction",
		"error_category":            "budget",
		"body":                      `{"prompt":"` + compactionDiagnosticCanary + `"}`,
		"user":                      compactionDiagnosticCanary,
		"summary":                   compactionDiagnosticCanary,
		"tool_output":               compactionDiagnosticCanary,
		"tool_args":                 compactionDiagnosticCanary,
		"authorization":             "Bearer " + compactionDiagnosticCanary,
		"api_key":                   compactionDiagnosticCanary,
		"unknown_field":             compactionDiagnosticCanary,
	}
}

func compactionDiagnosticTestStream(t *testing.T) (*Service, *StreamBroker, *ActiveStream, *debugRecorderTestCapture) {
	t.Helper()
	broker := NewStreamBroker()
	stream, err := broker.OpenStream("request-1", "conversation-1", 3, "model-1", "Model 1", agentv1.AgentMode_AGENT_MODE_AGENT, "")
	if err != nil {
		t.Fatalf("OpenStream() error = %v", err)
	}
	capture := &debugRecorderTestCapture{}
	debug := newDebugRecorder(t.TempDir(), broker, debugRecorderTestConfig("basic"), capture)
	t.Cleanup(debug.Close)
	service := &Service{
		broker: broker,
		debug:  debug,
	}
	return service, broker, stream, capture
}

func TestCompactionDiagnosticsSeparatePinnedInputsAndTail(t *testing.T) {
	for _, withExecution := range []bool{false, true} {
		name := "输入仍在连续尾中"
		if withExecution {
			name = "输入已独立固定"
		}
		t.Run(name, func(t *testing.T) {
			conversation := compactionAppendOnlyConversation(t)
			if withExecution {
				appendSameTurnBatches(t, conversation, "diagnostic-boundary", 3)
			}
			plan := sameTurnLayoutPlan(t, conversation)
			fields := compactionPlanDiagnosticFields(conversation, plan)
			var wantPinned, wantTail, wantCurrent int64
			for index, entry := range conversation.Entries {
				tokens := estimateTextTokens(string(entry.Payload))
				if entry.TurnSeq == plan.CurrentTurnSeq && entry.RequestID == plan.CurrentRequestID && isCompactionInput(entry) {
					wantCurrent += tokens
				}
				if index >= plan.Layout.TailStart && isOrdinaryPromptReplayEntryKind(entry.Kind) {
					wantTail += tokens
				}
				for _, ref := range plan.Layout.PinnedInputs {
					if ref == compactionEntryRef(entry) {
						wantPinned += tokens
					}
				}
			}
			if got := readInt64Value(fields["pinned_input_tokens"]); got != wantPinned {
				t.Fatalf("固定引用计数与实际集合不符: pinned_count=%d tokens=%d want=%d", len(plan.Layout.PinnedInputs), got, wantPinned)
			}
			if got := readInt64Value(fields["tail_tokens"]); got != wantTail {
				t.Fatalf("连续尾占用遗漏了尾内输入: tokens=%d want=%d", got, wantTail)
			}
			if got := readInt64Value(fields["current_input_tokens"]); got != wantCurrent {
				t.Fatalf("当前输入占用不符: tokens=%d want=%d", got, wantCurrent)
			}
		})
	}
}

func TestCompactionDiagnosticsWithoutFrozenLayout(t *testing.T) {
	service, _, stream, capture := compactionDiagnosticTestStream(t)
	stream.RequestID, stream.TurnSeq = "request-2", 2
	conversation := &ConversationFile{ConversationID: stream.ConversationID, Mode: "agent", TokenDetailsMaxTokens: 50_000, NextEntrySeq: 1}
	text := strings.Repeat("大", 100_000)
	appendEntriesInPlace(conversation, []HistoryEntry{compactionTestUserEntry(t, stream.TurnSeq, stream.RequestID, text, "oversized-input")})
	compiler := NewPromptCompiler(NewHistoryProjector(), NewToolCatalog(), NewReminderInjector(), nil)
	compiled, err := compiler.Compile(conversation, stream.Mode, text, "gpt-6.1-sol")
	if err != nil {
		t.Fatal(err)
	}
	plan, err := service.buildAutoCompactionPlan(stream, conversation, compiled)
	if err == nil || plan != nil {
		t.Fatalf("夹具未触发无可压缩内容的超大输入: plan=%v err=%v", plan, err)
	}
	fields := capture.onlyCapture(t).Event.Fields
	if fields["blocked_reason"] != "current_input_exceeds_budget" {
		t.Fatalf("无冻结引用却误报固定输入: %v", fields)
	}
	if readInt64Value(fields["current_input_tokens"]) <= compactionBudgetTokens(50_000) || readInt64Value(fields["pinned_input_tokens"]) != 0 {
		t.Fatalf("当前输入与固定引用占用混淆: %v", fields)
	}
}

func TestRecordCompactionDecisionWhitelistsSafeFields(t *testing.T) {
	service, _, stream, capture := compactionDiagnosticTestStream(t)
	done := make(chan struct{})
	go func() {
		// The recorder resolves the conversation through the broker, which locks
		// the stream again; the helper must release stream.mu before logging.
		service.recordCompactionDecision(stream, "compaction_planned", compactionDiagnosticTestFields())
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("recordCompactionDecision deadlocked on the stream lock")
	}
	if len(capture.captures) != 1 {
		t.Fatalf("capture count = %d, want 1", len(capture.captures))
	}
	captured := capture.captures[0]
	if captured.Event.Layer != "runtime" || captured.Event.Event != "compaction_planned" {
		t.Fatalf("unexpected event identity: layer=%q event=%q", captured.Event.Layer, captured.Event.Event)
	}
	if captured.Event.Status != compactionDiagnosticStatusPlanned {
		t.Fatalf("planned status = %q, want %q", captured.Event.Status, compactionDiagnosticStatusPlanned)
	}
	for key, want := range map[string]int64{
		"compaction_sequence":       2,
		"compaction_layout_version": 1,
		"prefix_start":              0,
		"prefix_end":                4,
		"tail_start":                4,
		"pinned_input_count":        1,
		"completed_batch_count":     3,
		"compiled_tokens_before":    160000,
		"compiled_tokens_after":     64000,
		"budget_tokens":             200000,
		"pinned_input_tokens":       1200,
		"current_input_tokens":      1500,
		"tail_tokens":               4000,
	} {
		if _, ok := captured.Event.Fields[key]; !ok {
			t.Fatalf("missing compaction field %q: %#v", key, captured.Event.Fields)
		}
		if got := readInt64Value(captured.Event.Fields[key]); got != want {
			t.Fatalf("field %q = %v (read %d), want %d; fields=%#v", key, captured.Event.Fields[key], got, want, captured.Event.Fields)
		}
	}
	for key, want := range map[string]string{
		"blocked_reason": "pinned_input_exceeds_budget",
		"reason":         "already_compacted_batch",
		"phase":          "compaction",
		"error_category": "budget",
	} {
		if got, _ := captured.Event.Fields[key].(string); got != want {
			t.Fatalf("field %q = %#v, want %q", key, captured.Event.Fields[key], want)
		}
	}
	for _, forbidden := range []string{
		"body", "user", "summary", "tool_output", "tool_args", "authorization", "api_key", "unknown_field",
		"status", "turn_seq",
	} {
		if _, ok := captured.Event.Fields[forbidden]; ok {
			t.Fatalf("capture fields exposed %q: %#v", forbidden, captured.Event.Fields)
		}
	}
	blob, err := json.Marshal(captured)
	if err != nil {
		t.Fatalf("marshal capture: %v", err)
	}
	if strings.Contains(string(blob), compactionDiagnosticCanary) {
		t.Fatalf("capture leaked sensitive content: %s", blob)
	}
	if got, _ := captured.Event.Fields["error_category"].(string); got != "budget" {
		t.Fatalf("explicit error_category = %q, want budget", got)
	}
	if strings.Contains(strings.ToLower(captured.Event.Event), "terminal") {
		t.Fatalf("compaction decision must not synthesize terminal: %q", captured.Event.Event)
	}
}

func TestRecordCompactionDecisionAssignsStatuses(t *testing.T) {
	cases := []struct {
		event string
		want  string
	}{
		{"compaction_planned", compactionDiagnosticStatusPlanned},
		{"compaction_applied", compactionDiagnosticStatusApplied},
		{"compaction_blocked", compactionDiagnosticStatusBlocked},
	}
	for _, test := range cases {
		t.Run(test.event, func(t *testing.T) {
			service, _, stream, capture := compactionDiagnosticTestStream(t)
			service.recordCompactionDecision(stream, test.event, map[string]any{"compaction_sequence": 1})
			captured := capture.onlyCapture(t)
			if captured.Event.Status != test.want {
				t.Fatalf("status = %q, want %q", captured.Event.Status, test.want)
			}
			if captured.Event.ErrorCategory != "" {
				t.Fatalf("status %q synthesized error_category %q", test.event, captured.Event.ErrorCategory)
			}
		})
	}
}

func TestRecordCompactionDecisionIgnoresUnknownEventsAndNilInputs(t *testing.T) {
	service, _, stream, capture := compactionDiagnosticTestStream(t)
	service.recordCompactionDecision(stream, "compaction_unknown", map[string]any{"compaction_sequence": 1})
	service.recordCompactionDecision(stream, "", map[string]any{"compaction_sequence": 1})
	service.recordCompactionDecision(nil, "compaction_applied", map[string]any{"compaction_sequence": 1})
	(*Service)(nil).recordCompactionDecision(stream, "compaction_applied", map[string]any{"compaction_sequence": 1})
	(&Service{}).recordCompactionDecision(stream, "compaction_applied", map[string]any{"compaction_sequence": 1})
	if len(capture.captures) != 0 {
		t.Fatalf("unknown events or nil inputs produced captures: %#v", capture.captures)
	}
}

func TestRecordCompactionDecisionRejectsNonNumericAndOversizedValues(t *testing.T) {
	service, _, stream, capture := compactionDiagnosticTestStream(t)
	service.recordCompactionDecision(stream, "compaction_blocked", map[string]any{
		"compaction_sequence":    "2",
		"compiled_tokens_before": -1,
		"budget_tokens":          compactionDiagnosticCanary,
		"blocked_reason":         "pinned_input_exceeds_budget " + compactionDiagnosticCanary,
		"phase":                  "compaction",
	})
	captured := capture.onlyCapture(t)
	if _, ok := captured.Event.Fields["compaction_sequence"]; ok {
		t.Fatalf("string-encoded number retained: %#v", captured.Event.Fields)
	}
	if _, ok := captured.Event.Fields["compiled_tokens_before"]; ok {
		t.Fatalf("negative token count retained: %#v", captured.Event.Fields)
	}
	if _, ok := captured.Event.Fields["budget_tokens"]; ok {
		t.Fatalf("free-text token value retained: %#v", captured.Event.Fields)
	}
	if _, ok := captured.Event.Fields["blocked_reason"]; ok {
		t.Fatalf("free-text reason retained: %#v", captured.Event.Fields)
	}
	if captured.Event.Fields["phase"] != "compaction" {
		t.Fatalf("valid enum dropped: %#v", captured.Event.Fields)
	}
}

func TestDebugRecorderKeepsCompactionDiagnosticFields(t *testing.T) {
	capture := &debugRecorderTestCapture{}
	recorder := newDebugRecorder(t.TempDir(), nil, debugRecorderTestConfig("basic"), capture)
	t.Cleanup(recorder.Close)
	recorder.LogRuntime(context.Background(), "request-1", "conversation-1", "compaction_applied", compactionDiagnosticTestFields())
	captured := capture.onlyCapture(t)
	for _, key := range compactionDiagnosticFieldKeys() {
		if _, ok := captured.Event.Fields[key]; !ok {
			t.Fatalf("debug recorder whitelist dropped compaction field %q: %#v", key, captured.Event.Fields)
		}
	}
	for _, forbidden := range []string{
		"body", "user", "summary", "tool_output", "tool_args", "authorization", "api_key", "unknown_field",
	} {
		if _, ok := captured.Event.Fields[forbidden]; ok {
			t.Fatalf("debug recorder whitelist exposed %q: %#v", forbidden, captured.Event.Fields)
		}
	}
}

func TestCompactionDiagnosticsSurviveBasicObservabilityCapture(t *testing.T) {
	logsRoot := t.TempDir()
	capture, err := observability.NewRecorder(logsRoot, observability.Settings{
		Mode:          observability.ModeBasic,
		RetentionDays: 7,
		MaxDiskMB:     64,
	})
	if err != nil {
		t.Fatalf("NewRecorder() error = %v", err)
	}
	status := capture.Status()
	debug := newDebugRecorder(filepath.Join(t.TempDir(), "history"), nil, debugRecorderTestConfig("basic"), capture)
	t.Cleanup(debug.Close)
	service := &Service{debug: debug}
	stream := &ActiveStream{RequestID: "request-basic", ConversationID: "conversation-basic", TurnSeq: 7}
	decisionEvents := []struct {
		name   string
		status string
	}{
		{compactionDiagnosticEventPlanned, compactionDiagnosticStatusPlanned},
		{compactionDiagnosticEventApplied, compactionDiagnosticStatusApplied},
		{compactionDiagnosticEventBlocked, compactionDiagnosticStatusBlocked},
	}
	for _, decision := range decisionEvents {
		service.recordCompactionDecision(stream, decision.name, compactionDiagnosticTestFields())
	}
	if err := capture.Close(); err != nil {
		t.Fatalf("capture.Close() error = %v", err)
	}

	file, err := os.Open(filepath.Join(status.SessionPath, "events.jsonl"))
	if err != nil {
		t.Fatalf("open events.jsonl: %v", err)
	}
	defer file.Close()
	wantStatus := make(map[string]string, len(decisionEvents))
	for _, decision := range decisionEvents {
		wantStatus[decision.name] = decision.status
	}
	found := make(map[string]bool, len(decisionEvents))
	scanner := bufio.NewScanner(file)
	scanner.Buffer(make([]byte, 0, 64*1024), 4*1024*1024)
	for scanner.Scan() {
		line := scanner.Text()
		if strings.Contains(line, compactionDiagnosticCanary) {
			t.Fatalf("basic events leaked canary: %s", line)
		}
		var event map[string]any
		if err := json.Unmarshal([]byte(line), &event); err != nil {
			t.Fatalf("decode event: %v", err)
		}
		name, _ := event["event"].(string)
		want, ok := wantStatus[name]
		if !ok {
			continue
		}
		found[name] = true
		if event["status"] != want {
			t.Fatalf("event %q status = %#v, want %q", name, event["status"], want)
		}
		if got := readInt64Value(event["turn_sequence"]); got != 7 {
			t.Fatalf("event %q turn_sequence = %#v, want 7", name, event["turn_sequence"])
		}
		fields, _ := event["fields"].(map[string]any)
		for key, want := range map[string]int64{
			"compaction_sequence":       2,
			"compaction_layout_version": 1,
			"prefix_start":              0,
			"prefix_end":                4,
			"tail_start":                4,
			"pinned_input_count":        1,
			"completed_batch_count":     3,
			"compiled_tokens_before":    160000,
			"compiled_tokens_after":     64000,
			"budget_tokens":             200000,
			"pinned_input_tokens":       1200,
			"tail_tokens":               4000,
		} {
			if _, ok := fields[key]; !ok {
				t.Fatalf("basic capture missing field %q: %#v", key, fields)
			}
			if got := readInt64Value(fields[key]); got != want {
				t.Fatalf("basic capture field %q = %#v, want %d; fields=%#v", key, fields[key], want, fields)
			}
		}
		if fields["blocked_reason"] != "pinned_input_exceeds_budget" || fields["phase"] != "compaction" {
			t.Fatalf("basic capture dropped enum fields: %#v", fields)
		}
		for _, forbidden := range []string{"body", "user", "summary", "tool_output", "tool_args", "authorization", "api_key", "unknown_field"} {
			if _, ok := fields[forbidden]; ok {
				t.Fatalf("basic capture exposed %q: %#v", forbidden, fields)
			}
		}
		if _, ok := event["payload_ref"]; ok {
			t.Fatalf("basic capture retained payload_ref: %#v", event["payload_ref"])
		}
	}
	if err := scanner.Err(); err != nil {
		t.Fatalf("scan events: %v", err)
	}
	for _, decision := range decisionEvents {
		if !found[decision.name] {
			t.Fatalf("basic events.jsonl missing %s: %s", decision.name, status.SessionPath)
		}
	}
}
