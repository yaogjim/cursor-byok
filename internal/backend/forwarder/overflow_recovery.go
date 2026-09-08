package forwarder

import (
	"errors"
	"strings"

	modeladapter "cursor/internal/backend/agent/model"
)

type overflowRecoveryCallState struct {
	Text              string
	Reasoning         string
	SyntheticThinking bool
	HadTool           bool
	PartialTools      bool
	Pending           int
	Continuation      bool
	AlreadyAttempted  bool
	Terminal          bool
}

func overflowRecoveryAllowed(err error, state overflowRecoveryCallState) bool {
	if state.Terminal || state.AlreadyAttempted || state.Continuation || state.HadTool || state.PartialTools || state.Pending > 0 {
		return false
	}
	if state.Text != "" || state.Reasoning != "" || state.SyntheticThinking {
		return false
	}
	return modeladapter.IsContextOverflowError(err)
}

func (service *Service) tryAcceptOverflowRecovery(stream *ActiveStream, cause error, state overflowRecoveryCallState, usage turnUsageSnapshot) (bool, error) {
	if service == nil || stream == nil {
		return false, nil
	}
	if modeladapter.IsContextOverflowError(cause) && state.AlreadyAttempted {
		return false, compactionTerminalError{
			code:    compactionOverflowTerminalCode,
			message: "provider context overflow after compaction recovery",
		}
	}
	if !overflowRecoveryAllowed(cause, state) {
		return false, nil
	}
	stream.mu.Lock()
	if isTerminalStreamStatus(stream.Status) || stream.OverflowRecoveryAttempted {
		stream.mu.Unlock()
		return false, compactionTerminalError{
			code:    compactionOverflowTerminalCode,
			message: "provider context overflow after compaction recovery",
		}
	}
	stream.OverflowRecoveryAttempted = true
	stream.CurrentProviderToken++
	stream.mu.Unlock()

	conversationID := ""
	requestID := ""
	turnSeq := int64(0)
	modelCallID := ""
	stream.mu.Lock()
	conversationID = stream.ConversationID
	requestID = stream.RequestID
	turnSeq = stream.TurnSeq
	modelCallID = stream.CurrentModelCallID
	stream.mu.Unlock()
	if err := service.recordTurnUsageSnapshot(stream, conversationID, turnSeq, requestID, modelCallID, "provider_error", usage, safeProviderTerminalMessage(cause), false); err != nil {
		return false, compactionTerminalError{
			code:    "usage_persistence_error",
			message: err.Error(),
		}
	}
	service.recordModelCallFinal(stream, "failed")

	plan, err := service.buildForcedAutoCompactionPlan(stream)
	if err != nil {
		return false, err
	}
	if plan == nil {
		return false, compactionTerminalError{
			code:    compactionOverflowTerminalCode,
			message: "no compactable history remains after provider context overflow",
		}
	}
	if err := service.beginPendingCompaction(stream, plan); err != nil {
		return false, err
	}
	return true, nil
}

func (service *Service) buildForcedAutoCompactionPlan(stream *ActiveStream) (*compactionPlan, error) {
	if service == nil || stream == nil {
		return nil, nil
	}
	conversation, _, _, err := service.snapshotCheckpointConversation(stream)
	if err != nil {
		return nil, err
	}
	if conversation == nil {
		return nil, compactionTerminalError{
			code:    compactionOverflowTerminalCode,
			message: "no compactable history remains after provider context overflow",
		}
	}
	compiled := CompiledConversation{}
	if service.compiler != nil {
		latestUserText := ""
		stream.mu.Lock()
		mode := stream.Mode
		modelName := stream.ModelName
		latestUserText = stream.LatestUserText
		stream.mu.Unlock()
		compiled, err = service.compiler.Compile(conversation, mode, latestUserText, modelName)
		if err != nil {
			return nil, err
		}
	}
	return service.buildAutoCompactionPlanForced(stream, conversation, compiled)
}

func (service *Service) buildAutoCompactionPlanForced(stream *ActiveStream, conversation *ConversationFile, compiled CompiledConversation) (*compactionPlan, error) {
	return service.buildAutoCompactionPlanWithForce(stream, conversation, compiled, true)
}

func overflowRecoveryFailureCode(err error) string {
	if err == nil {
		return "unknown"
	}
	var coded interface{ TerminalCode() string }
	if errors.As(err, &coded) {
		if code := strings.TrimSpace(coded.TerminalCode()); code != "" {
			return code
		}
	}
	return "unknown"
}
