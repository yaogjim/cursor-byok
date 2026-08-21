package forwarder

import (
	"testing"
	"time"

	"cursor/gen/agentv1"
	modeladapter "cursor/internal/backend/agent/model"
)

// TestModelCallFinalPerModelCallIDDeduplication 验证每个 model_call_id 至多记录一次 model_call_final
func TestModelCallFinalPerModelCallIDDeduplication(t *testing.T) {
	broker := NewStreamBroker()
	service := &Service{
		store:     NewConversationFileStore(t.TempDir()),
		projector: NewHistoryProjector(),
		broker:    broker,
		debug:     newDebugRecorder(t.TempDir(), broker, nil), // 初始化 debug recorder
	}
	stream, err := broker.OpenStream(
		"request-1", "conversation-1", 1, "default", "default",
		agentv1.AgentMode_AGENT_MODE_AGENT, "hello",
	)
	if err != nil {
		t.Fatalf("OpenStream() error = %v", err)
	}

	// 模拟第一个 provider pass
	stream.mu.Lock()
	stream.CurrentProviderToken = 1
	stream.CurrentModelCallID = "model-call-1"
	stream.ProviderPassCount = 1
	stream.ProviderActive = true
	stream.Status = StreamStatusStreaming
	stream.Phase = TurnPhaseProviderRunning
	stream.ProviderStreamStats = ProviderStreamStats{Attempt: 1, ProtocolFinalStatus: "streaming"}
	if stream.FinalizedModelCallIDs == nil {
		stream.FinalizedModelCallIDs = make(map[string]struct{})
	}
	stream.mu.Unlock()

	// 完成第一个 pass，产生 tool invocation 需要 resume
	if err := service.applyProviderModelEvent(stream, modeladapter.ModelEvent{
		Kind:         modeladapter.ModelEventKindTurnFinished,
		FinishReason: "tool_use",
		OccurredAt:   time.Now(),
	}); err != nil {
		t.Fatalf("applyProviderModelEvent() error = %v", err)
	}

	stream.mu.Lock()
	stream.ProviderStreamStats.CompletionMarker = true
	stream.mu.Unlock()

	if err := service.handleProviderDoneEvent(stream, &streamProviderEvent{Token: 1, Done: true}); err != nil {
		t.Fatalf("handleProviderDoneEvent() error = %v", err)
	}

	// 验证第一个 model_call_id 已记录
	stream.mu.Lock()
	_, finalized1 := stream.FinalizedModelCallIDs["model-call-1"]
	stream.mu.Unlock()
	if !finalized1 {
		t.Fatal("model-call-1 should be finalized after first pass")
	}

	// 模拟第二个 provider pass（工具结果后 resume）
	stream.mu.Lock()
	stream.CurrentProviderToken = 2
	stream.CurrentModelCallID = "model-call-2" // 新的 model_call_id
	stream.ProviderPassCount = 2
	stream.ProviderActive = true
	stream.Status = StreamStatusStreaming
	stream.Phase = TurnPhaseProviderRunning
	stream.ProviderStreamStats = ProviderStreamStats{Attempt: 2, ProtocolFinalStatus: "streaming"}
	stream.mu.Unlock()

	// 完成第二个 pass，正常结束（terminal）
	if err := service.applyProviderModelEvent(stream, modeladapter.ModelEvent{
		Kind:       modeladapter.ModelEventKindTextDelta,
		Text:       "final response",
		OccurredAt: time.Now(),
	}); err != nil {
		t.Fatalf("applyProviderModelEvent() error = %v", err)
	}

	if err := service.applyProviderModelEvent(stream, modeladapter.ModelEvent{
		Kind:         modeladapter.ModelEventKindTurnFinished,
		FinishReason: "stop",
		OccurredAt:   time.Now(),
	}); err != nil {
		t.Fatalf("applyProviderModelEvent() error = %v", err)
	}

	stream.mu.Lock()
	stream.ProviderStreamStats.CompletionMarker = true
	stream.mu.Unlock()

	if err := service.handleProviderDoneEvent(stream, &streamProviderEvent{Token: 2, Done: true}); err != nil {
		t.Fatalf("handleProviderDoneEvent() error = %v", err)
	}

	// 验证两个 model_call_id 都已记录且不重复
	stream.mu.Lock()
	_, finalized2 := stream.FinalizedModelCallIDs["model-call-2"]
	totalFinalized := len(stream.FinalizedModelCallIDs)
	finalizedIDs := make([]string, 0, len(stream.FinalizedModelCallIDs))
	for id := range stream.FinalizedModelCallIDs {
		finalizedIDs = append(finalizedIDs, id)
	}
	stream.mu.Unlock()

	if !finalized2 {
		t.Fatal("model-call-2 should be finalized after second pass")
	}
	if totalFinalized != 2 {
		t.Fatalf("total finalized model_call_ids = %d (ids: %v), want 2", totalFinalized, finalizedIDs)
	}
}

// TestModelCallFinalIdempotency 验证多次调用 recordModelCallFinal 是幂等的
func TestModelCallFinalIdempotency(t *testing.T) {
	broker := NewStreamBroker()
	service := &Service{
		store:     NewConversationFileStore(t.TempDir()),
		projector: NewHistoryProjector(),
		broker:    broker,
		debug:     newDebugRecorder(t.TempDir(), broker, nil), // 初始化 debug recorder
	}
	stream, err := broker.OpenStream(
		"request-1", "conversation-1", 1, "default", "default",
		agentv1.AgentMode_AGENT_MODE_AGENT, "hello",
	)
	if err != nil {
		t.Fatalf("OpenStream() error = %v", err)
	}

	stream.mu.Lock()
	stream.CurrentModelCallID = "model-call-idempotent"
	stream.ProviderStreamStats = ProviderStreamStats{Attempt: 1}
	if stream.FinalizedModelCallIDs == nil {
		stream.FinalizedModelCallIDs = make(map[string]struct{})
	}
	stream.mu.Unlock()

	// 多次调用 recordModelCallFinal
	service.recordModelCallFinal(stream, "succeeded")
	service.recordModelCallFinal(stream, "succeeded")
	service.recordModelCallFinal(stream, "succeeded")

	// 验证 map 中只有一个条目（幂等性）
	stream.mu.Lock()
	totalFinalized := len(stream.FinalizedModelCallIDs)
	_, exists := stream.FinalizedModelCallIDs["model-call-idempotent"]
	stream.mu.Unlock()

	if !exists {
		t.Fatal("model-call-idempotent should exist in FinalizedModelCallIDs")
	}
	if totalFinalized != 1 {
		t.Fatalf("finalized count = %d, want 1", totalFinalized)
	}
}
