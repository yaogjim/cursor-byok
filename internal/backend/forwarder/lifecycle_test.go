package forwarder

import (
	"context"
	"encoding/json"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"cursor/gen/agentv1"
	execbridge "cursor/internal/backend/agent/bridge/exec"
	runtimecore "cursor/internal/backend/agent/core"

	"google.golang.org/protobuf/proto"
)

func TestSubagentModelParametersRemainDistinctAfterInboundDecode(t *testing.T) {
	service, _, _ := testCheckpointBlobProjection(t)
	decode := func(value string) InboundIntent {
		t.Helper()
		message := &agentv1.AgentClientMessage{
			Message: &agentv1.AgentClientMessage_RunRequest{RunRequest: &agentv1.AgentRunRequest{
				ConversationId: stringPtr("parameter-parent"),
				RequestedModel: &agentv1.RequestedModel{ModelId: "parent-model"},
				SubagentModelOverrides: []*agentv1.SubagentModelOverride{{
					SubagentType: "explore",
					Selection: &agentv1.SubagentModelOverride_Model{Model: &agentv1.RequestedModel{
						ModelId:    "child-model",
						Parameters: []*agentv1.RequestedModel_ModelParameterValue{{Id: "reasoning", Value: value}},
					}},
				}},
			}},
		}
		intent, err := service.decodeInboundIntent("parameter-request", message, "run_request")
		if err != nil {
			t.Fatalf("decodeInboundIntent() error = %v", err)
		}
		return intent
	}
	low, high := decode("low"), decode("high")
	if low.SubagentModelOverrides["explore"].ModelID != "child-model" || high.SubagentModelOverrides["explore"].ModelID != "child-model" {
		t.Fatal("子模型 ID 对照未保留")
	}
	if reflect.DeepEqual(low.SubagentModelOverrides, high.SubagentModelOverrides) {
		t.Fatal("子模型 low/high 参数经真实入口解析后成为相同状态，参数键值已丢失")
	}
}

func TestSubagentModelParameterSnapshotsAreIndependent(t *testing.T) {
	first := &agentv1.RequestedModel_ModelParameterValue{Id: "reasoning", Value: " high "}
	first.ProtoReflect().SetUnknown([]byte{0x22, 0x03, 'x', 'y', 'z'})
	original := []*agentv1.RequestedModel_ModelParameterValue{
		first, {Id: "reasoning", Value: "low"}, {Id: "", Value: ""}, nil,
	}
	parsed := parseSubagentModelOverrides([]*agentv1.SubagentModelOverride{{
		SubagentType: "explore",
		Selection: &agentv1.SubagentModelOverride_Model{Model: &agentv1.RequestedModel{
			ModelId: "child-model", MaxMode: true, BuiltInModel: true, IsVariantStringRepresentation: true,
			Parameters: original,
		}},
	}}).Overrides
	selection := parsed["explore"]
	if selection.ParameterCount != len(original) || len(selection.Parameters) != len(original) || !selection.MaxMode || !selection.BuiltInModel || !selection.IsVariantStringRepresentation {
		t.Fatalf("参数数量或模型标识未保留: %#v", selection)
	}
	for index, parameter := range original {
		if !proto.Equal(selection.Parameters[index], parameter) {
			t.Fatalf("参数 %d 的顺序、值或未知字段改变", index)
		}
		if parameter != nil && selection.Parameters[index] == parameter {
			t.Fatalf("参数 %d 仍引用客户端原对象", index)
		}
	}
	first.Value = "changed-client"
	first.ProtoReflect().SetUnknown(nil)
	original[1] = nil
	if selection.Parameters[0].GetValue() != " high " || len(selection.Parameters[0].ProtoReflect().GetUnknown()) == 0 || selection.Parameters[1].GetValue() != "low" {
		t.Fatal("客户端修改污染解析快照")
	}
	cloned := cloneSubagentModelOverrides(parsed)
	cloned["explore"].Parameters[0].Value = "changed-clone"
	cloned["explore"].Parameters[1] = nil
	if parsed["explore"].Parameters[0].GetValue() != " high " || parsed["explore"].Parameters[1] == nil {
		t.Fatal("克隆快照共享参数对象或切片")
	}
	lookedUp, matched, ok := runtimecore.LookupSubagentModelOverride(parsed, "generalPurpose")
	if !ok || matched != "explore" {
		t.Fatal("既有子类型别名查找退化")
	}
	lookedUp.Parameters[0].Value = "changed-lookup"
	lookedUp.Parameters[1] = nil
	if parsed["explore"].Parameters[0].GetValue() != " high " || parsed["explore"].Parameters[1] == nil {
		t.Fatal("查找返回值污染父快照")
	}
	for _, parameters := range [][]*agentv1.RequestedModel_ModelParameterValue{nil, {}, {nil}} {
		copy := (runtimecore.SubagentModelOverrideSelection{Parameters: parameters}).Clone()
		if !reflect.DeepEqual(parameters, copy.Parameters) {
			t.Fatal("nil/空参数形状改变")
		}
	}
}

func TestSubagentModelParameterPreservationKeepsSelectionRules(t *testing.T) {
	model := &agentv1.SubagentModelOverride{
		SubagentType: "explore",
		Selection: &agentv1.SubagentModelOverride_Model{Model: &agentv1.RequestedModel{
			ModelId: "child-model", Parameters: []*agentv1.RequestedModel_ModelParameterValue{{Id: "reasoning", Value: "high"}},
		}},
	}
	for _, test := range []struct {
		name string
		last *agentv1.SubagentModelOverride
		want string
	}{
		{"inherit", &agentv1.SubagentModelOverride{SubagentType: "explore", Selection: &agentv1.SubagentModelOverride_Inherit{Inherit: true}}, "inherit"},
		{"disabled", &agentv1.SubagentModelOverride{SubagentType: "explore", Selection: &agentv1.SubagentModelOverride_Disabled{Disabled: true}}, "disabled"},
		{"empty-model-ignored", &agentv1.SubagentModelOverride{SubagentType: "explore", Selection: &agentv1.SubagentModelOverride_Model{Model: &agentv1.RequestedModel{}}}, "model"},
	} {
		t.Run(test.name, func(t *testing.T) {
			parsed := parseSubagentModelOverrides([]*agentv1.SubagentModelOverride{nil, model, test.last})
			selection := parsed.Overrides["explore"]
			if selection.Selection != test.want {
				t.Fatalf("选择=%s，预期=%s", selection.Selection, test.want)
			}
			if test.want != "model" && (len(selection.Parameters) != 0 || selection.ParameterCount != 0 || selection.ModelID != "") {
				t.Fatal("inherit/disabled 携带了前一个显式模型的参数")
			}
		})
	}
}

func TestSubagentModelParameterValuesStayOutOfSummaries(t *testing.T) {
	overrides := parseSubagentModelOverrides([]*agentv1.SubagentModelOverride{{
		SubagentType: "explore",
		Selection: &agentv1.SubagentModelOverride_Model{Model: &agentv1.RequestedModel{
			ModelId: "child-model", Parameters: []*agentv1.RequestedModel_ModelParameterValue{{Id: "private-knob", Value: "private-value"}},
		}},
	}}).Overrides
	invocation := runtimecore.ToolInvocation{CallID: "task-1", ToolName: "Task", ArgsJSON: []byte(`{"subagent_type":"explore"}`)}
	for _, summary := range []any{subagentModelOverrideSummaries(overrides), taskSubagentModelResolutionPayload(invocation, "parent-model", overrides)} {
		encoded, err := json.Marshal(summary)
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(string(encoded), "private-knob") || strings.Contains(string(encoded), "private-value") || strings.Contains(string(encoded), `"parameters"`) {
			t.Fatalf("参数值进入日志摘要: %s", encoded)
		}
	}
	if subagentModelOverrideSummaries(overrides)[0]["parameter_count"] != 1 {
		t.Fatal("既有参数数量摘要退化")
	}
}

type subagentParameterSnapshotBridge struct {
	execbridge.ExecBridge
	context        execbridge.OpenExecContext
	modelID        string
	parameterValue string
}

func (bridge *subagentParameterSnapshotBridge) OpenExec(openContext execbridge.OpenExecContext, invocation runtimecore.ToolInvocation) (*agentv1.AgentServerMessage, runtimecore.PendingExec, error) {
	bridge.context = openContext
	bridge.parameterValue = openContext.SubagentModelOverrides["explore"].Parameters[0].GetValue()
	message, pending, err := bridge.ExecBridge.OpenExec(openContext, invocation)
	if err == nil {
		bridge.modelID = message.GetExecServerMessage().GetSubagentArgs().GetModelId()
	}
	// 模拟执行桥修改自己的副本，父 stream 必须保持原选择。
	openContext.SubagentModelOverrides["explore"].Parameters[0].Value = "changed-bridge"
	return message, pending, err
}

func TestSubagentModelParametersReachTaskBridgeAsIsolatedSnapshot(t *testing.T) {
	service, _, _ := testCheckpointBlobProjection(t)
	bridge := &subagentParameterSnapshotBridge{ExecBridge: execbridge.NewBridge()}
	service.execBridge = bridge
	parameter := &agentv1.RequestedModel_ModelParameterValue{Id: "reasoning", Value: "high"}
	message := &agentv1.AgentClientMessage{Message: &agentv1.AgentClientMessage_RunRequest{RunRequest: &agentv1.AgentRunRequest{
		ConversationId: stringPtr("parameter-parent"), RequestedModel: &agentv1.RequestedModel{ModelId: "parent-model"},
		SubagentModelOverrides: []*agentv1.SubagentModelOverride{{SubagentType: "explore", Selection: &agentv1.SubagentModelOverride_Model{Model: &agentv1.RequestedModel{
			ModelId: "child-model", MaxMode: true, Parameters: []*agentv1.RequestedModel_ModelParameterValue{parameter},
		}}}},
	}}}
	intent, err := service.decodeInboundIntent("parameter-request", message, "run_request")
	if err != nil {
		t.Fatal(err)
	}
	// 走相同初始化路径，但不拉起真实 provider。
	intent.Prewarm = true
	if err := service.handleRunIntent(intent); err != nil {
		t.Fatal(err)
	}
	stream, ok := service.broker.Get(intent.RequestID)
	if !ok {
		t.Fatal("父 stream 未建立")
	}
	parameter.Value = "changed-client"
	intent.SubagentModelOverrides["explore"].Parameters[0].Value = "changed-intent"
	stream.mu.Lock()
	selection := stream.SubagentModelOverrides["explore"]
	stream.CurrentModelCallID = "parameter-call"
	stream.ProviderActive = true
	stream.mu.Unlock()
	if selection.Parameters[0].GetValue() != "high" || !selection.MaxMode {
		t.Fatal("请求副本污染 stream 或 Max Mode 丢失")
	}
	if err := service.handleToolInvocation(stream, runtimecore.ToolInvocation{
		CallID: "task-parameters", ToolName: "Task", ModelCallID: "parameter-call",
		ArgsJSON: []byte(`{"subagent_type":"explore","model":"requested-other","prompt":"合成任务"}`),
	}); err != nil {
		t.Fatal(err)
	}
	if bridge.modelID != "child-model" || bridge.parameterValue != "high" || !bridge.context.SubagentModelOverrides["explore"].MaxMode {
		t.Fatal("Task 桥未收到既有选模优先级或完整选择")
	}
	stream.mu.Lock()
	defer stream.mu.Unlock()
	if stream.SubagentModelOverrides["explore"].Parameters[0].GetValue() != "high" {
		t.Fatal("执行桥修改污染父 stream 参数")
	}
}

func TestBrokerCancelActiveProvidersLeavesIdleStreams(t *testing.T) {
	broker := NewStreamBroker()
	idle, err := broker.OpenStream("idle", "conv", 1, "model", "name", agentv1.AgentMode_AGENT_MODE_AGENT, "hi")
	if err != nil || idle == nil {
		t.Fatalf("OpenStream(idle) error = %v stream=%v", err, idle)
	}
	var canceled atomic.Bool
	active, err := broker.OpenStream("active", "conv", 1, "model", "name", agentv1.AgentMode_AGENT_MODE_AGENT, "hi")
	if err != nil || active == nil {
		t.Fatalf("OpenStream(active) error = %v stream=%v", err, active)
	}
	active.mu.Lock()
	active.ProviderActive = true
	active.ProviderCancel = func() { canceled.Store(true) }
	active.mu.Unlock()
	if got := broker.ActiveProviderCount(); got != 1 {
		t.Fatalf("ActiveProviderCount() = %d, want 1", got)
	}
	if got := broker.CancelActiveProviders("shutdown reason=app_quit"); got != 1 {
		t.Fatalf("CancelActiveProviders() = %d, want 1", got)
	}
	if !canceled.Load() {
		t.Fatal("active provider was not canceled")
	}
	if got := broker.ActiveProviderCount(); got != 0 {
		t.Fatalf("ActiveProviderCount() after cancel = %d", got)
	}
	idle.mu.Lock()
	idleStatus := idle.Status
	idle.mu.Unlock()
	if idleStatus == StreamStatusCanceled {
		t.Fatal("idle stream was canceled")
	}
}

func TestBrokerWaitForIdleReturnsWhenProviderFinishes(t *testing.T) {
	broker := NewStreamBroker()
	stream, err := broker.OpenStream("wait", "conv", 1, "model", "name", agentv1.AgentMode_AGENT_MODE_AGENT, "hi")
	if err != nil || stream == nil {
		t.Fatalf("OpenStream() error = %v stream=%v", err, stream)
	}
	stream.mu.Lock()
	stream.ProviderActive = true
	stream.ProviderCancel = func() {}
	stream.mu.Unlock()
	go func() {
		time.Sleep(20 * time.Millisecond)
		stream.mu.Lock()
		stream.ProviderActive = false
		stream.ProviderCancel = nil
		stream.mu.Unlock()
	}()
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	broker.WaitForIdle(ctx)
	if broker.ActiveProviderCount() != 0 {
		t.Fatal("WaitForIdle returned while provider still active")
	}
}

func TestUnknownToolInvocationRecordsFailedResultWithoutPendingOrFailedStream(t *testing.T) {
	service, stream, _ := testCheckpointBlobProjection(t)
	stream.mu.Lock()
	stream.CurrentProviderToken = 1
	stream.CurrentModelCallID = "model-call-1"
	stream.ProviderActive = true
	stream.Status = StreamStatusStreaming
	stream.Phase = TurnPhaseProviderRunning
	stream.mu.Unlock()

	err := service.handleToolInvocation(stream, runtimecore.ToolInvocation{
		CallID:      "call-mystery",
		ToolName:    "MysteryRemovedTool",
		ArgsJSON:    []byte(`{}`),
		ModelCallID: "model-call-1",
	})
	if err != nil {
		t.Fatalf("handleToolInvocation() error = %v", err)
	}

	conversation, _, _, err := service.snapshotCheckpointConversation(stream)
	if err != nil {
		t.Fatalf("snapshotCheckpointConversation() error = %v", err)
	}
	foundFailedResult := false
	for _, entry := range conversation.Entries {
		if entry.Kind != "tool_result" || entry.ToolCallID != "call-mystery" {
			continue
		}
		payload := string(entry.Payload)
		if !strings.Contains(payload, "MysteryRemovedTool") || !strings.Contains(payload, "nonexistent tool") {
			t.Fatalf("tool_result payload = %s, want failed unknown-tool result", payload)
		}
		foundFailedResult = true
	}
	if !foundFailedResult {
		t.Fatal("missing failed tool_result for MysteryRemovedTool")
	}

	foundStarted := false
	foundCompleted := false
	for _, event := range readCheckpointTestEvents(t, service, stream) {
		interaction := event.Message.GetInteractionUpdate()
		if interaction == nil {
			continue
		}
		if started := interaction.GetToolCallStarted(); started != nil && started.GetCallId() == "call-mystery" {
			foundStarted = true
		}
		if completed := interaction.GetToolCallCompleted(); completed != nil && completed.GetCallId() == "call-mystery" {
			foundCompleted = true
		}
	}
	if !foundStarted {
		t.Fatal("missing ToolCallStarted for MysteryRemovedTool")
	}
	if !foundCompleted {
		t.Fatal("missing ToolCallCompleted for MysteryRemovedTool")
	}

	acknowledgeCheckpointBlobs(t, service, stream)
	foundCheckpoint := false
	for _, event := range readCheckpointTestEvents(t, service, stream) {
		if event.Message.GetConversationCheckpointUpdate() != nil {
			foundCheckpoint = true
			break
		}
	}
	if !foundCheckpoint {
		t.Fatal("missing ConversationCheckpointUpdate after unknown-tool checkpoint ACK")
	}

	stream.mu.Lock()
	pending := len(stream.PendingExecs) + len(stream.PendingInteractions)
	status := stream.Status
	stream.mu.Unlock()
	if pending != 0 {
		t.Fatalf("pending execs/interactions = %d, want 0", pending)
	}
	if status == StreamStatusFailed {
		t.Fatalf("stream status = %s, unknown tool must not fail the stream", status)
	}
}

func TestLateProviderDoneEventOnTerminalStreamKeepsStatusAndDoesNotAppendSecondEnd(t *testing.T) {
	cases := []struct {
		name   string
		status StreamStatus
		term   func(*testing.T, *Service, *ActiveStream)
	}{
		{"canceled", StreamStatusCanceled, func(t *testing.T, service *Service, stream *ActiveStream) {
			t.Helper()
			if err := service.broker.Cancel(stream.RequestID, "user stopped"); err != nil {
				t.Fatalf("Cancel() error = %v", err)
			}
		}},
		{"failed", StreamStatusFailed, func(t *testing.T, service *Service, stream *ActiveStream) {
			t.Helper()
			if err := service.broker.Fail(stream.RequestID, "provider_error", "provider failed"); err != nil {
				t.Fatalf("Fail() error = %v", err)
			}
		}},
		{"completed", StreamStatusCompleted, func(t *testing.T, service *Service, stream *ActiveStream) {
			t.Helper()
			if err := service.broker.Complete(stream.RequestID, "", ""); err != nil {
				t.Fatalf("Complete() error = %v", err)
			}
		}},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			service, stream, _ := testCheckpointBlobProjection(t)
			stream.mu.Lock()
			stream.CurrentProviderToken = 1
			stream.CurrentModelCallID = "model-call-1"
			stream.ProviderActive = true
			stream.Status = StreamStatusStreaming
			stream.Phase = TurnPhaseProviderRunning
			stream.mu.Unlock()

			test.term(t, service, stream)
			if got := countStreamEndEvents(t, service, stream); got != 1 {
				t.Fatalf("end events after %s = %d, want 1", test.name, got)
			}

			if err := service.handleProviderDoneEvent(stream, &streamProviderEvent{Token: 1, Done: true}); err != nil {
				t.Fatalf("late handleProviderDoneEvent() error = %v", err)
			}
			stream.mu.Lock()
			status := stream.Status
			stream.mu.Unlock()
			if status != test.status {
				t.Fatalf("status after late done = %s, want %s", status, test.status)
			}
			if got := countStreamEndEvents(t, service, stream); got != 1 {
				t.Fatalf("end events after late done = %d, want 1", got)
			}
		})
	}
}

func TestTerminalStreamStatusIsIrreversible(t *testing.T) {
	cases := []struct {
		name   string
		first  func(*testing.T, *Service, *ActiveStream)
		late   func(*testing.T, *Service, *ActiveStream)
		status StreamStatus
		phase  TurnPhase
	}{
		{
			name:   "completed_then_cancel_intent",
			status: StreamStatusCompleted,
			phase:  TurnPhaseProviderRunning,
			first: func(t *testing.T, service *Service, stream *ActiveStream) {
				t.Helper()
				if err := service.broker.Complete(stream.RequestID, "", ""); err != nil {
					t.Fatalf("Complete() error = %v", err)
				}
			},
			late: func(t *testing.T, service *Service, stream *ActiveStream) {
				t.Helper()
				if err := service.handleCancelIntent(InboundIntent{RequestID: stream.RequestID, CancelReason: "late stop"}); err != nil {
					t.Fatalf("handleCancelIntent() error = %v", err)
				}
			},
		},
		{
			name:   "completed_then_fail",
			status: StreamStatusCompleted,
			phase:  TurnPhaseProviderRunning,
			first: func(t *testing.T, service *Service, stream *ActiveStream) {
				t.Helper()
				if err := service.broker.Complete(stream.RequestID, "", ""); err != nil {
					t.Fatalf("Complete() error = %v", err)
				}
			},
			late: func(t *testing.T, service *Service, stream *ActiveStream) {
				t.Helper()
				if err := service.broker.Fail(stream.RequestID, "provider_error", "late fail"); err != nil {
					t.Fatalf("Fail() error = %v", err)
				}
			},
		},
		{
			name:   "canceled_then_cancel",
			status: StreamStatusCanceled,
			phase:  TurnPhaseProviderRunning,
			first: func(t *testing.T, service *Service, stream *ActiveStream) {
				t.Helper()
				if err := service.broker.Cancel(stream.RequestID, "user stopped"); err != nil {
					t.Fatalf("Cancel() error = %v", err)
				}
			},
			late: func(t *testing.T, service *Service, stream *ActiveStream) {
				t.Helper()
				if err := service.broker.Cancel(stream.RequestID, "second cancel"); err != nil {
					t.Fatalf("late Cancel() error = %v", err)
				}
			},
		},
		{
			name:   "canceled_then_fail",
			status: StreamStatusCanceled,
			phase:  TurnPhaseProviderRunning,
			first: func(t *testing.T, service *Service, stream *ActiveStream) {
				t.Helper()
				if err := service.broker.Cancel(stream.RequestID, "user stopped"); err != nil {
					t.Fatalf("Cancel() error = %v", err)
				}
			},
			late: func(t *testing.T, service *Service, stream *ActiveStream) {
				t.Helper()
				if err := service.broker.Fail(stream.RequestID, "provider_error", "late fail"); err != nil {
					t.Fatalf("Fail() error = %v", err)
				}
			},
		},
		{
			name:   "failed_then_fail",
			status: StreamStatusFailed,
			phase:  TurnPhaseProviderRunning,
			first: func(t *testing.T, service *Service, stream *ActiveStream) {
				t.Helper()
				if err := service.broker.Fail(stream.RequestID, "provider_error", "provider failed"); err != nil {
					t.Fatalf("Fail() error = %v", err)
				}
			},
			late: func(t *testing.T, service *Service, stream *ActiveStream) {
				t.Helper()
				if err := service.broker.Fail(stream.RequestID, "provider_error", "second fail"); err != nil {
					t.Fatalf("late Fail() error = %v", err)
				}
			},
		},
		{
			name:   "failed_then_cancel",
			status: StreamStatusFailed,
			phase:  TurnPhaseProviderRunning,
			first: func(t *testing.T, service *Service, stream *ActiveStream) {
				t.Helper()
				if err := service.broker.Fail(stream.RequestID, "provider_error", "provider failed"); err != nil {
					t.Fatalf("Fail() error = %v", err)
				}
			},
			late: func(t *testing.T, service *Service, stream *ActiveStream) {
				t.Helper()
				if err := service.broker.Cancel(stream.RequestID, "late cancel"); err != nil {
					t.Fatalf("Cancel() error = %v", err)
				}
			},
		},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			service, stream, _ := testCheckpointBlobProjection(t)
			stream.mu.Lock()
			stream.CurrentProviderToken = 1
			stream.CurrentModelCallID = "model-call-1"
			stream.ProviderActive = true
			stream.Status = StreamStatusStreaming
			stream.Phase = TurnPhaseProviderRunning
			stream.mu.Unlock()

			test.first(t, service, stream)
			if got := countStreamEndEvents(t, service, stream); got != 1 {
				t.Fatalf("end events after first terminal = %d, want 1", got)
			}

			test.late(t, service, stream)
			stream.mu.Lock()
			status := stream.Status
			phase := stream.Phase
			stream.mu.Unlock()
			if status != test.status {
				t.Fatalf("status after late op = %s, want %s", status, test.status)
			}
			if phase != test.phase {
				t.Fatalf("phase after late op = %s, want %s", phase, test.phase)
			}
			if got := countStreamEndEvents(t, service, stream); got != 1 {
				t.Fatalf("end events after late op = %d, want 1", got)
			}

			if test.name == "completed_then_cancel_intent" {
				conversation, _, _, err := service.snapshotCheckpointConversation(stream)
				if err != nil {
					t.Fatalf("snapshotCheckpointConversation() error = %v", err)
				}
				for _, entry := range conversation.Entries {
					if entry.Kind == "metadata" && strings.Contains(string(entry.Payload), `"status":"canceled"`) {
						t.Fatalf("late cancel polluted checkpoint metadata: %s", entry.Payload)
					}
				}
			}
		})
	}
}

func countStreamEndEvents(t *testing.T, service *Service, stream *ActiveStream) int {
	t.Helper()
	count := 0
	for _, event := range readCheckpointTestEvents(t, service, stream) {
		if event.End {
			count++
		}
	}
	return count
}
