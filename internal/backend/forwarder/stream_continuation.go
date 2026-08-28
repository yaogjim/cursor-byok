package forwarder

import (
	"context"
	"encoding/json"
	"errors"
	"strconv"
	"strings"
	"time"

	modeladapter "cursor/internal/backend/agent/model"
)

const (
	promptContextSourceStreamContinuation = "stream_continuation"
	streamSourceRunSSE                    = "run_sse"
	streamSourceGatewayChat               = "gateway_chat"
	streamSourceGatewayResponses          = "gateway_responses"

	defaultStreamContinuationMaxPerTurn     = 1
	defaultStreamContinuationDeadline       = 120 * time.Second
	defaultStreamContinuationOverlapWindow  = 2048
	minStreamContinuationOverlapWindowChars = 64
	maxStreamContinuationOverlapWindowChars = 8192

	continuationReasonDisabled             = "disabled"
	continuationReasonNested               = "nested_or_budget"
	continuationReasonSpawned              = "spawn_idempotent"
	continuationReasonNoPersistableContent = "no_persisted_text_or_reasoning"
	continuationReasonToolProgress         = "tool_progress"
	continuationReasonSideEffect           = "potential_side_effect"
	continuationReasonPendingInteraction   = "pending_interaction"
	continuationReasonCheckpoint           = "checkpoint_committed"
	continuationReasonClientCancel         = "client_cancel"
	continuationReasonDeadline             = "deadline"
	continuationReasonContextCanceled      = "context_canceled"
	continuationReasonSubagentChild        = "subagent_child"
	continuationReasonGatewayPath          = "gateway_chat_or_responses"
	continuationReasonNonRunSSE            = "non_run_sse"
	continuationReasonTurnDeadline         = "total_deadline"
	continuationReasonTerminal             = "stream_terminal"
	continuationReasonCompleted            = "protocol_completed"
	continuationReasonOverlapMismatch      = "continuation_overlap_mismatch"
	continuationReasonNoProgress           = "continuation_no_progress"
	continuationReasonChildTruncated       = "continuation_truncated"
)

type StreamContinuationSettings struct {
	Enabled            bool
	MaxPerTurn         int
	TotalDeadline      time.Duration
	OverlapWindowChars int
}

type streamContinuationSettingsSource interface {
	StreamContinuationSettings(context.Context) (bool, int, time.Duration, int)
}

type streamContinuationEvalInput struct {
	Settings                       StreamContinuationSettings
	Now                            time.Time
	TurnStartedAt                  time.Time
	ContinuationIndex              int
	ContinuationSpawned            bool
	PendingContinue                bool
	Status                         StreamStatus
	ProtocolFinalStatus            string
	StreamSource                   string
	SubagentChild                  bool
	ParentText                     string
	ParentReasoning                string
	ParentReasoningSignature       string
	ParentReasoningSignatureSource string
	PartialToolCount               int
	CompletedToolCount             int
	DispatchedToolCount            int
	ToolDispatchState              string
	PotentialSideEffect            string
	PendingExecCount               int
	PendingInteractionCount        int
	CheckpointCommitted            bool
	PendingCheckpoint              bool
	ClientCanceled                 bool
	DeadlineExceeded               bool
	ContextCanceled                bool
}

type streamContinuationDecision struct {
	Eligible bool
	Reason   string
}

func normalizeStreamContinuationSettings(input StreamContinuationSettings) StreamContinuationSettings {
	output := input
	if output.MaxPerTurn <= 0 {
		output.MaxPerTurn = defaultStreamContinuationMaxPerTurn
	}
	if output.MaxPerTurn > defaultStreamContinuationMaxPerTurn {
		output.MaxPerTurn = defaultStreamContinuationMaxPerTurn
	}
	if output.TotalDeadline <= 0 {
		output.TotalDeadline = defaultStreamContinuationDeadline
	}
	if output.OverlapWindowChars <= 0 {
		output.OverlapWindowChars = defaultStreamContinuationOverlapWindow
	}
	if output.OverlapWindowChars < minStreamContinuationOverlapWindowChars {
		output.OverlapWindowChars = minStreamContinuationOverlapWindowChars
	}
	if output.OverlapWindowChars > maxStreamContinuationOverlapWindowChars {
		output.OverlapWindowChars = maxStreamContinuationOverlapWindowChars
	}
	return output
}

func (service *Service) streamContinuationSettings() StreamContinuationSettings {
	if service == nil {
		return normalizeStreamContinuationSettings(StreamContinuationSettings{})
	}
	if service.testStreamContinuation != nil {
		return normalizeStreamContinuationSettings(*service.testStreamContinuation)
	}
	if service.streamContinuationSource != nil {
		enabled, maxPerTurn, deadline, overlap := service.streamContinuationSource.StreamContinuationSettings(context.Background())
		return normalizeStreamContinuationSettings(StreamContinuationSettings{
			Enabled:            enabled,
			MaxPerTurn:         maxPerTurn,
			TotalDeadline:      deadline,
			OverlapWindowChars: overlap,
		})
	}
	return normalizeStreamContinuationSettings(StreamContinuationSettings{})
}

func evaluateStreamContinuationEligibility(input streamContinuationEvalInput) streamContinuationDecision {
	settings := normalizeStreamContinuationSettings(input.Settings)
	if !settings.Enabled {
		return streamContinuationDecision{Reason: continuationReasonDisabled}
	}
	if isTerminalStreamStatus(input.Status) {
		return streamContinuationDecision{Reason: continuationReasonTerminal}
	}
	if strings.TrimSpace(input.ProtocolFinalStatus) == "completed" {
		return streamContinuationDecision{Reason: continuationReasonCompleted}
	}
	if input.ContinuationSpawned || input.PendingContinue {
		return streamContinuationDecision{Reason: continuationReasonSpawned}
	}
	if input.ContinuationIndex >= settings.MaxPerTurn {
		return streamContinuationDecision{Reason: continuationReasonNested}
	}
	source := strings.TrimSpace(input.StreamSource)
	switch source {
	case "", streamSourceRunSSE:
	case streamSourceGatewayChat, streamSourceGatewayResponses:
		return streamContinuationDecision{Reason: continuationReasonGatewayPath}
	default:
		return streamContinuationDecision{Reason: continuationReasonNonRunSSE}
	}
	if input.SubagentChild {
		return streamContinuationDecision{Reason: continuationReasonSubagentChild}
	}
	if input.ClientCanceled {
		return streamContinuationDecision{Reason: continuationReasonClientCancel}
	}
	if input.DeadlineExceeded {
		return streamContinuationDecision{Reason: continuationReasonDeadline}
	}
	if input.ContextCanceled {
		return streamContinuationDecision{Reason: continuationReasonContextCanceled}
	}
	if input.PendingExecCount > 0 || input.PendingInteractionCount > 0 {
		return streamContinuationDecision{Reason: continuationReasonPendingInteraction}
	}
	if input.CheckpointCommitted || input.PendingCheckpoint {
		return streamContinuationDecision{Reason: continuationReasonCheckpoint}
	}
	if input.PartialToolCount > 0 || input.CompletedToolCount > 0 || input.DispatchedToolCount > 0 {
		return streamContinuationDecision{Reason: continuationReasonToolProgress}
	}
	switch strings.TrimSpace(input.ToolDispatchState) {
	case "", "not_dispatched", "not_recorded":
	default:
		return streamContinuationDecision{Reason: continuationReasonToolProgress}
	}
	sideEffect := strings.TrimSpace(input.PotentialSideEffect)
	if sideEffect != "" && sideEffect != "none" {
		return streamContinuationDecision{Reason: continuationReasonSideEffect}
	}
	if strings.TrimSpace(input.ParentText) == "" && !hasReplayableReasoningPayload(input.ParentReasoning, input.ParentReasoningSignature, input.ParentReasoningSignatureSource) {
		return streamContinuationDecision{Reason: continuationReasonNoPersistableContent}
	}
	if !input.TurnStartedAt.IsZero() && settings.TotalDeadline > 0 && !input.Now.IsZero() {
		if input.Now.Sub(input.TurnStartedAt) >= settings.TotalDeadline {
			return streamContinuationDecision{Reason: continuationReasonTurnDeadline}
		}
	}
	return streamContinuationDecision{Eligible: true}
}

func longestParentSuffixPrefix(parent string, child string, window int) int {
	if parent == "" || child == "" {
		return 0
	}
	if window <= 0 {
		window = defaultStreamContinuationOverlapWindow
	}
	maxK := len(parent)
	if window < maxK {
		maxK = window
	}
	if len(child) < maxK {
		maxK = len(child)
	}
	for k := maxK; k >= 1; k-- {
		if strings.HasPrefix(child, parent[len(parent)-k:]) {
			return k
		}
	}
	return 0
}

func childStillWithinParentSuffix(parent string, child string, window int) bool {
	if child == "" {
		return true
	}
	if parent == "" {
		return false
	}
	if window <= 0 {
		window = defaultStreamContinuationOverlapWindow
	}
	maxK := len(parent)
	if window < maxK {
		maxK = window
	}
	windowText := parent[len(parent)-maxK:]
	for i := 0; i < len(windowText); i++ {
		if strings.HasPrefix(windowText[i:], child) {
			return true
		}
	}
	return false
}

func consumeContinuationDeltaLocked(stream *ActiveStream, kind string) (publish string, suppress bool, mismatch bool) {
	if stream == nil || stream.ContinuationIndex < 1 {
		return "", false, false
	}
	if stream.ContinuationOverlapMismatch {
		return "", true, true
	}
	parent := stream.ContinuationParentText
	accumulated := stream.ProviderAccumulatedText
	remainderField := &stream.ContinuationRemainderText
	if kind == "reasoning" {
		parent = stream.ContinuationParentReasoning
		accumulated = stream.ProviderAccumulatedReasoning
		remainderField = &stream.ContinuationRemainderReasoning
	}
	window := stream.ContinuationOverlapWindow
	if parent == "" {
		already := *remainderField
		publish = continuationRemainderDelta(already, accumulated)
		*remainderField = accumulated
		stream.ContinuationNewVisibleBytes += len(publish)
		if kind == "text" {
			stream.ContinuationOverlapResolved = true
		}
		return publish, false, false
	}
	overlap := longestParentSuffixPrefix(parent, accumulated, window)
	if overlap > 0 {
		remainder := accumulated[overlap:]
		publish = continuationRemainderDelta(*remainderField, remainder)
		*remainderField = remainder
		stream.ContinuationNewVisibleBytes += len(publish)
		if kind == "text" {
			stream.ContinuationOverlapResolved = true
		}
		return publish, false, false
	}
	if childStillWithinParentSuffix(parent, accumulated, window) {
		return "", true, false
	}
	stream.ContinuationOverlapMismatch = true
	return "", true, true
}

func continuationRemainderDelta(already string, remainder string) string {
	if remainder == "" {
		return ""
	}
	if already == "" {
		return remainder
	}
	if strings.HasPrefix(remainder, already) {
		return remainder[len(already):]
	}
	return remainder
}

func continuationBreakpointTail(value string, window int) string {
	trimmed := strings.TrimSpace(value)
	if trimmed == "" {
		return ""
	}
	if window <= 0 {
		window = defaultStreamContinuationOverlapWindow
	}
	if len(trimmed) <= window {
		return trimmed
	}
	return trimmed[len(trimmed)-window:]
}

func streamContinuationReminderText(parentText string, parentReasoning string, window int) string {
	builder := strings.Builder{}
	builder.WriteString("The previous assistant output was interrupted. Continue ONLY from the breakpoint. Do not repeat any already written text or reasoning. Do not call tools. Do not restart the answer.")
	if tail := continuationBreakpointTail(parentText, window); tail != "" {
		builder.WriteString("\n\nAlready persisted assistant text breakpoint (do not repeat):\n")
		builder.WriteString(tail)
	}
	if tail := continuationBreakpointTail(parentReasoning, window); tail != "" {
		builder.WriteString("\n\nAlready persisted assistant reasoning breakpoint (do not repeat):\n")
		builder.WriteString(tail)
	}
	return builder.String()
}

func streamContinuationPromptIdempotencyKey(turnSeq int64, requestID string, index int) string {
	return strings.Join([]string{
		"stream_continuation_prompt",
		strconv.FormatInt(turnSeq, 10),
		strings.TrimSpace(requestID),
		strconv.Itoa(index),
	}, ":")
}

func newStreamContinuationPromptEntry(turnSeq int64, requestID string, index int, parentText string, parentReasoning string, window int) HistoryEntry {
	context := newPromptContextReminder(promptContextSourceStreamContinuation, streamContinuationReminderText(parentText, parentReasoning, window))
	context.Persist = true
	entry := newPromptContextEntry(turnSeq, requestID, context)
	entry.IdempotencyKey = streamContinuationPromptIdempotencyKey(turnSeq, requestID, index)
	return entry
}

func continuationFlushText(continuationIndex int, mismatch bool, accumulated string, remainder string) string {
	if continuationIndex < 1 {
		return accumulated
	}
	if mismatch {
		return ""
	}
	return remainder
}

func (service *Service) logStreamContinuationEvent(stream *ActiveStream, eventName string, fields map[string]any) {
	if service == nil || stream == nil || service.debug == nil {
		return
	}
	stream.mu.Lock()
	requestID := stream.RequestID
	conversationID := stream.ConversationID
	stream.mu.Unlock()
	service.debug.LogRuntime(context.Background(), requestID, conversationID, eventName, fields)
}

func (service *Service) trySpawnStreamContinuation(
	stream *ActiveStream,
	payload *streamProviderEvent,
	conversationID string,
	turnSeq int64,
	requestID string,
	modelCallID string,
	providerPass int,
	accumulatedText string,
	accumulatedReasoning string,
	accumulatedReasoningSignature string,
	accumulatedReasoningSignatureSource string,
	accumulatedReasoningItemID string,
	accumulatedReasoningStatus string,
	accumulatedReasoningSummary json.RawMessage,
	usage turnUsageSnapshot,
	hadToolInvocation bool,
) (bool, error) {
	if service == nil || stream == nil || payload == nil || payload.Err == nil {
		return false, nil
	}
	settings := service.streamContinuationSettings()
	if !settings.Enabled {
		return false, nil
	}

	stream.mu.Lock()
	eval := streamContinuationEvalInput{
		Settings:                       settings,
		Now:                            time.Now().UTC(),
		TurnStartedAt:                  stream.TurnProviderStartedAt,
		ContinuationIndex:              stream.ContinuationIndex,
		ContinuationSpawned:            stream.ContinuationSpawned,
		PendingContinue:                stream.PendingProviderAction == providerActionContinue,
		Status:                         stream.Status,
		ProtocolFinalStatus:            stream.ProviderStreamStats.ProtocolFinalStatus,
		StreamSource:                   stream.StreamSource,
		ParentText:                     accumulatedText,
		ParentReasoning:                accumulatedReasoning,
		ParentReasoningSignature:       accumulatedReasoningSignature,
		ParentReasoningSignatureSource: accumulatedReasoningSignatureSource,
		PartialToolCount:               stream.ProviderStreamStats.PartialToolCount,
		CompletedToolCount:             stream.ProviderStreamStats.CompletedToolCount,
		DispatchedToolCount:            stream.ProviderStreamStats.DispatchedToolCount,
		ToolDispatchState:              stream.ProviderStreamStats.ToolDispatchState,
		PotentialSideEffect:            stream.ProviderStreamStats.PotentialSideEffect,
		PendingExecCount:               len(stream.PendingExecs),
		PendingInteractionCount:        len(stream.PendingInteractions),
		CheckpointCommitted:            len(stream.ConfirmedCheckpointBlobs) > 0,
		PendingCheckpoint:              stream.PendingCheckpoint != nil,
		ClientCanceled:                 stream.ProviderStreamStats.Attribution == "client" || errors.Is(payload.Err, context.Canceled),
		DeadlineExceeded:               stream.ProviderStreamStats.Attribution == "deadline" || errors.Is(payload.Err, context.DeadlineExceeded) || modeladapter.ClassifyProviderError(payload.Err) == modeladapter.ProviderErrorStreamIdleTimeout,
		ContextCanceled:                errors.Is(payload.Err, context.Canceled),
	}
	if stream.CheckpointConversation != nil {
		eval.SubagentChild = isChildConversationSubagentTypeName(stream.CheckpointConversation.SubagentTypeName) || strings.TrimSpace(stream.CheckpointConversation.ParentConversationID) != ""
	}
	if eval.TurnStartedAt.IsZero() {
		eval.TurnStartedAt = stream.ProviderStreamStats.StartedAt
	}
	stream.mu.Unlock()

	if hadToolInvocation {
		eval.DispatchedToolCount++
	}

	decision := evaluateStreamContinuationEligibility(eval)
	if !decision.Eligible {
		service.logStreamContinuationEvent(stream, "stream_continuation_suppressed", map[string]any{
			"model_call_id": strings.TrimSpace(modelCallID),
			"reason":        decision.Reason,
		})
		return false, nil
	}

	if err := service.flushFailedProviderOutput(
		stream, conversationID, turnSeq, requestID, modelCallID, providerPass,
		accumulatedText, accumulatedReasoning, accumulatedReasoningSignature, accumulatedReasoningSignatureSource,
		accumulatedReasoningItemID, accumulatedReasoningStatus, accumulatedReasoningSummary, !hadToolInvocation,
	); err != nil {
		return false, err
	}
	if err := service.recordTurnUsageSnapshot(stream, conversationID, turnSeq, requestID, modelCallID, "partial", usage, "", false); err != nil {
		return false, err
	}
	if err := service.updateConversationTokenState(stream, conversationID, usage, modelCallID, false); err != nil {
		return false, err
	}
	service.recordModelCallFinal(stream, "partial")

	nextIndex := eval.ContinuationIndex + 1
	entry := newStreamContinuationPromptEntry(turnSeq, requestID, nextIndex, accumulatedText, accumulatedReasoning, settings.OverlapWindowChars)
	if _, err := service.appendConversationEntries(stream, conversationID, []HistoryEntry{entry}); err != nil {
		return false, err
	}

	stream.mu.Lock()
	if stream.ContinuationSpawned || stream.PendingProviderAction == providerActionContinue || isTerminalStreamStatus(stream.Status) {
		stream.mu.Unlock()
		service.logStreamContinuationEvent(stream, "stream_continuation_suppressed", map[string]any{
			"model_call_id": strings.TrimSpace(modelCallID),
			"reason":        continuationReasonSpawned,
		})
		return false, nil
	}
	stream.ContinuationSpawned = true
	stream.ContinuationIndex = nextIndex
	stream.ContinuedFromModelCallID = strings.TrimSpace(modelCallID)
	stream.ContinuationParentText = accumulatedText
	stream.ContinuationParentReasoning = accumulatedReasoning
	stream.ContinuationOverlapWindow = settings.OverlapWindowChars
	stream.ContinuationOverlapResolved = false
	stream.ContinuationOverlapMismatch = false
	stream.ContinuationRemainderText = ""
	stream.ContinuationRemainderReasoning = ""
	stream.ContinuationNewVisibleBytes = 0
	if settings.TotalDeadline > 0 {
		started := stream.TurnProviderStartedAt
		if started.IsZero() {
			started = stream.ProviderStreamStats.StartedAt
		}
		if started.IsZero() {
			started = time.Now().UTC()
		}
		stream.ContinuationDeadline = started.Add(settings.TotalDeadline)
	}
	stream.UpdatedAt = time.Now().UTC()
	stream.mu.Unlock()

	service.logStreamContinuationEvent(stream, "stream_continuation_spawned", map[string]any{
		"continued_from_model_call_id": strings.TrimSpace(modelCallID),
		"continuation_index":           nextIndex,
		"turn_seq":                     turnSeq,
		"reason":                       firstNonEmpty(strings.TrimSpace(eval.ProtocolFinalStatus), "truncated"),
	})
	if err := service.requestProviderAction(stream, providerActionContinue); err != nil {
		return false, err
	}
	return true, nil
}

func continuationProviderContext(deadline time.Time) (context.Context, context.CancelFunc) {
	if deadline.IsZero() {
		return context.WithCancel(context.Background())
	}
	return context.WithDeadline(context.Background(), deadline)
}

func continuationShouldDropChildEventLocked(stream *ActiveStream, event modeladapter.ModelEvent) bool {
	if stream == nil || stream.ContinuationIndex < 1 {
		return false
	}
	switch event.Kind {
	case modeladapter.ModelEventKindPartialToolCall, modeladapter.ModelEventKindToolCallDelta, modeladapter.ModelEventKindToolLikeCompleted:
		if strings.TrimSpace(stream.ContinuationAbortReason) == "" {
			stream.ContinuationAbortReason = continuationReasonToolProgress
		}
		return true
	default:
		return false
	}
}

func continuationShouldFusePartial(stream *ActiveStream, payload *streamProviderEvent) (bool, string) {
	if stream == nil {
		return false, ""
	}
	if stream.ContinuationIndex < 1 {
		return false, ""
	}
	if reason := strings.TrimSpace(stream.ContinuationAbortReason); reason != "" {
		return true, reason
	}
	if stream.ContinuationOverlapMismatch {
		return true, continuationReasonOverlapMismatch
	}
	if stream.ContinuationNewVisibleBytes <= 0 {
		return true, continuationReasonNoProgress
	}
	if payload != nil && payload.Err != nil {
		return true, continuationReasonChildTruncated
	}
	return false, ""
}
