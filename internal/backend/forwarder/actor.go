package forwarder

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"strings"
	"time"

	"google.golang.org/protobuf/proto"

	"cursor/gen/agentv1"
	runtimecore "cursor/internal/backend/agent/core"
	modeladapter "cursor/internal/backend/agent/model"
)

type TurnPhase string

const (
	TurnPhaseIdle            TurnPhase = "idle"
	TurnPhaseProviderRunning TurnPhase = "provider_running"
	TurnPhaseWaitingExternal TurnPhase = "waiting_external"
	TurnPhaseAwaitingUser    TurnPhase = "awaiting_user"
	TurnPhaseCompacting      TurnPhase = "compacting"
	TurnPhaseCheckpointing   TurnPhase = "checkpointing"
	TurnPhaseCompleted       TurnPhase = "completed"
	TurnPhaseFailed          TurnPhase = "failed"
	TurnPhaseCanceled        TurnPhase = "canceled"
)

type providerAction string

const (
	providerActionNone     providerAction = ""
	providerActionStart    providerAction = "start"
	providerActionResume   providerAction = "resume"
	providerActionContinue providerAction = "continue"
)

type pendingCompletionDisposition string

const (
	completionDispositionNone                  pendingCompletionDisposition = ""
	completionDispositionResumeAfterExternal   pendingCompletionDisposition = "resume_after_external"
	completionDispositionCompleteAfterExternal pendingCompletionDisposition = "complete_after_external"
)

type streamCommandKind string

const (
	streamCommandRun               streamCommandKind = "run"
	streamCommandCancel            streamCommandKind = "cancel"
	streamCommandMetadata          streamCommandKind = "metadata"
	streamCommandExecResult        streamCommandKind = "exec_result"
	streamCommandExecControl       streamCommandKind = "exec_control"
	streamCommandInteractionResult streamCommandKind = "interaction_result"
	streamCommandProviderEvent     streamCommandKind = "provider_event"
	streamCommandTimerFired        streamCommandKind = "timer_fired"
	streamCommandCompactionEvent   streamCommandKind = "compaction_event"
	streamCommandMaybeOrphaned     streamCommandKind = "maybe_orphaned"
)

type streamTimerKind string

const (
	streamTimerProviderResume       streamTimerKind = "provider_resume"
	streamTimerNonStreamingRecovery streamTimerKind = "non_streaming_recovery"
	streamTimerShellForeground      streamTimerKind = "shell_foreground"
	streamTimerShellTransportClose  streamTimerKind = "shell_transport_close"
	streamTimerCheckpointBlobs      streamTimerKind = "checkpoint_blobs"
	streamTimerOrphanCancel         streamTimerKind = "orphan_cancel"
)

type streamProviderEvent struct {
	Token uint64
	Event modeladapter.ModelEvent
	Done  bool
	Err   error
}

type streamTimerEvent struct {
	Key       string
	Kind      streamTimerKind
	Token     uint64
	ExecID    string
	MessageID uint32
	Reason    string
}

type streamCompactionEvent struct {
	Token       uint64
	Plan        *PendingCompaction
	SummaryText string
	Err         error
}

type streamCommand struct {
	Kind       streamCommandKind
	Intent     InboundIntent
	Provider   *streamProviderEvent
	Timer      *streamTimerEvent
	Compaction *streamCompactionEvent
	Reason     string
}

type streamCommandEnvelope struct {
	command streamCommand
	result  chan error
}

func commandKindForIntent(intent InboundIntent) (streamCommandKind, error) {
	switch strings.TrimSpace(intent.Kind) {
	case "run":
		return streamCommandRun, nil
	case "cancel":
		return streamCommandCancel, nil
	case "metadata", "kv_result":
		return streamCommandMetadata, nil
	case "exec_result":
		return streamCommandExecResult, nil
	case "exec_control":
		return streamCommandExecControl, nil
	case "interaction_result":
		return streamCommandInteractionResult, nil
	default:
		return "", fmt.Errorf("unsupported inbound intent: %s", intent.Kind)
	}
}

func (service *Service) dispatchInboundIntent(intent InboundIntent) error {
	if service == nil {
		return fmt.Errorf("forwarder service is nil")
	}
	stream, err := service.streamForIntent(intent)
	if err != nil {
		return err
	}
	if stream == nil {
		return nil
	}
	commandKind, err := commandKindForIntent(intent)
	if err != nil {
		return err
	}
	return service.postStreamCommandWait(stream, streamCommand{
		Kind:   commandKind,
		Intent: intent,
	})
}

func (service *Service) streamForIntent(intent InboundIntent) (*ActiveStream, error) {
	switch strings.TrimSpace(intent.Kind) {
	case "run":
		stream, err := service.broker.OpenStream(
			intent.RequestID,
			intent.ConversationID,
			0,
			intent.ModelID,
			intent.ModelName,
			intent.Mode,
			userMessageText(intent.UserMessage),
		)
		if err != nil {
			return nil, err
		}
		if stream == nil {
			return nil, fmt.Errorf("open stream failed")
		}
		return stream, nil
	case "metadata", "kv_result":
		stream, ok := service.broker.Get(intent.RequestID)
		if !ok || stream == nil {
			if intent.HasExplicitMode || intent.StartsRun {
				return nil, fmt.Errorf("metadata intent requires active request context: %s", intent.RequestID)
			}
			return nil, nil
		}
		if isTerminalIntentStream(stream) {
			return nil, nil
		}
		return stream, nil
	default:
		stream, ok := service.broker.Get(intent.RequestID)
		if !ok || stream == nil {
			return nil, fmt.Errorf("request is not active: %s", intent.RequestID)
		}
		if isTerminalIntentStream(stream) {
			return nil, nil
		}
		return stream, nil
	}
}

func isTerminalIntentStream(stream *ActiveStream) bool {
	if stream == nil {
		return true
	}
	stream.mu.Lock()
	defer stream.mu.Unlock()
	if isTerminalStreamStatus(stream.Status) {
		return true
	}
	switch stream.Phase {
	case TurnPhaseCanceled, TurnPhaseCompleted, TurnPhaseFailed:
		return true
	default:
		return false
	}
}

func (service *Service) ensureStreamActor(stream *ActiveStream) (chan streamCommandEnvelope, chan struct{}, error) {
	if stream == nil {
		return nil, nil, fmt.Errorf("active stream is required")
	}
	stream.mu.Lock()
	if stream.ActorMailbox != nil && stream.ActorDone != nil {
		mailbox := stream.ActorMailbox
		done := stream.ActorDone
		stream.mu.Unlock()
		return mailbox, done, nil
	}
	mailbox := make(chan streamCommandEnvelope, 128)
	done := make(chan struct{})
	stream.ActorMailbox = mailbox
	stream.ActorDone = done
	if stream.TimerTokens == nil {
		stream.TimerTokens = make(map[string]uint64)
	}
	if strings.TrimSpace(string(stream.Phase)) == "" {
		stream.Phase = TurnPhaseIdle
	}
	stream.mu.Unlock()
	go service.runStreamActor(stream, mailbox, done)
	return mailbox, done, nil
}

func (service *Service) postStreamCommandWait(stream *ActiveStream, command streamCommand) error {
	if stream == nil {
		return nil
	}
	mailbox, done, err := service.ensureStreamActor(stream)
	if err != nil {
		return err
	}
	result := make(chan error, 1)
	envelope := streamCommandEnvelope{
		command: command,
		result:  result,
	}
	select {
	case <-done:
		return errProviderLoopInterrupted
	case mailbox <- envelope:
	}
	select {
	case <-done:
		return errProviderLoopInterrupted
	case err := <-result:
		return err
	}
}

func (service *Service) postStreamCommandAsync(stream *ActiveStream, command streamCommand) error {
	if stream == nil {
		return nil
	}
	mailbox, done, err := service.ensureStreamActor(stream)
	if err != nil {
		return err
	}
	envelope := streamCommandEnvelope{command: command}
	select {
	case <-done:
		return errProviderLoopInterrupted
	case mailbox <- envelope:
		return nil
	}
}

func (service *Service) runStreamActor(stream *ActiveStream, mailbox <-chan streamCommandEnvelope, done chan struct{}) {
	defer close(done)
	for {
		envelope, ok := <-mailbox
		if !ok {
			return
		}
		err := service.handleStreamCommand(stream, envelope.command)
		if envelope.result != nil {
			envelope.result <- err
		} else if err != nil {
			_ = service.failStream(stream, "unknown", err)
		}
		if shouldStopStreamActor(stream) {
			return
		}
	}
}

func shouldStopStreamActor(stream *ActiveStream) bool {
	if stream == nil {
		return true
	}
	stream.mu.Lock()
	defer stream.mu.Unlock()
	if isTerminalStreamStatus(stream.Status) {
		return true
	}
	switch stream.Phase {
	case TurnPhaseCompleted, TurnPhaseFailed, TurnPhaseCanceled:
		return true
	default:
		return false
	}
}

func (service *Service) handleStreamCommand(stream *ActiveStream, command streamCommand) error {
	switch command.Kind {
	case streamCommandRun:
		return service.handleRunIntent(command.Intent)
	case streamCommandCancel:
		return service.handleCancelIntent(command.Intent)
	case streamCommandMetadata:
		if strings.TrimSpace(command.Intent.Kind) == "kv_result" {
			return service.handleCheckpointBlobResult(stream, command.Intent.KVClientMessage)
		}
		return service.handleMetadataIntent(command.Intent)
	case streamCommandExecResult:
		return service.handleExecResult(command.Intent)
	case streamCommandExecControl:
		return service.handleExecControl(command.Intent)
	case streamCommandInteractionResult:
		return service.handleInteractionResult(command.Intent)
	case streamCommandProviderEvent:
		return service.handleProviderEvent(stream, command.Provider)
	case streamCommandTimerFired:
		return service.handleTimerEvent(stream, command.Timer)
	case streamCommandCompactionEvent:
		return service.handleCompactionEvent(stream, command.Compaction)
	case streamCommandMaybeOrphaned:
		if stream == nil {
			return nil
		}
		stream.mu.Lock()
		subscriberCount := len(stream.Subscribers)
		status := stream.Status
		stream.mu.Unlock()
		if subscriberCount > 0 || isTerminalStreamStatus(status) {
			return nil
		}
		service.scheduleStreamTimer(stream, providerTimerKey(streamTimerOrphanCancel, ""), orphanSubscriberGracePeriod, streamTimerOrphanCancel, "", 0, command.Reason)
		return nil
	default:
		return fmt.Errorf("unsupported stream command kind: %s", command.Kind)
	}
}

func (service *Service) requestProviderAction(stream *ActiveStream, action providerAction) error {
	if stream == nil {
		return nil
	}
	stream.mu.Lock()
	switch action {
	case providerActionStart:
		stream.PendingProviderAction = providerActionStart
	case providerActionContinue:
		if stream.PendingProviderAction != providerActionStart {
			stream.PendingProviderAction = providerActionContinue
		}
	case providerActionResume:
		if stream.PendingProviderAction != providerActionStart && stream.PendingProviderAction != providerActionContinue {
			stream.PendingProviderAction = providerActionResume
		}
	default:
		stream.PendingProviderAction = providerActionNone
	}
	stream.UpdatedAt = time.Now().UTC()
	stream.mu.Unlock()
	return service.reconcileStream(stream)
}

func (service *Service) reconcileStream(stream *ActiveStream) error {
	if stream == nil {
		return nil
	}

	stream.mu.Lock()
	if isTerminalStreamStatus(stream.Status) {
		stream.mu.Unlock()
		return nil
	}
	providerActive := stream.ProviderActive
	pendingExecCount := len(stream.PendingExecs)
	pendingInteractionCount := len(stream.PendingInteractions)
	hasPendingCompaction := stream.PendingCompaction != nil
	action := stream.PendingProviderAction
	completion := stream.PendingProviderCompletion
	stream.mu.Unlock()

	if providerActive {
		return nil
	}
	if pendingExecCount+pendingInteractionCount > 0 {
		if hasPendingAwaitingUserInteraction(stream) {
			service.setTurnPhase(stream, TurnPhaseAwaitingUser)
		} else if hasPendingCompaction {
			service.setTurnPhase(stream, TurnPhaseCompacting)
		} else {
			service.setTurnPhase(stream, TurnPhaseWaitingExternal)
		}
		return nil
	}
	if hasPendingCompaction {
		service.setTurnPhase(stream, TurnPhaseCompacting)
		return nil
	}

	if completion != nil {
		if completion.Disposition == completionDispositionResumeAfterExternal {
			stream.mu.Lock()
			stream.PendingProviderCompletion = nil
			if stream.PendingProviderAction != providerActionStart {
				stream.PendingProviderAction = providerActionResume
			}
			stream.UpdatedAt = time.Now().UTC()
			stream.mu.Unlock()
			action = providerActionResume
		} else {
			clearPendingProviderCompletion(stream)
			if err := service.completeSuccessfulTurn(stream, *completion); err != nil {
				return service.failStreamIfNonTerminal(stream, "unknown", err)
			}
			return nil
		}
	}

	switch action {
	case providerActionStart, providerActionContinue:
		return service.driveProvider(stream)
	case providerActionResume:
		service.setTurnPhase(stream, TurnPhaseWaitingExternal)
		service.scheduleStreamTimer(stream, providerTimerKey(streamTimerProviderResume, ""), providerResumeDebounce, streamTimerProviderResume, "", 0, "")
		return nil
	default:
		service.setTurnPhase(stream, TurnPhaseIdle)
		return nil
	}
}

func (service *Service) handleProviderEvent(stream *ActiveStream, payload *streamProviderEvent) error {
	if stream == nil || payload == nil {
		return nil
	}
	if !providerTokenMatches(stream, payload.Token) {
		return nil
	}
	if payload.Done {
		return service.handleProviderDoneEvent(stream, payload)
	}
	return service.applyProviderModelEvent(stream, payload.Event)
}

func streamProviderCompletionMarker(stream *ActiveStream) bool {
	if stream == nil {
		return false
	}
	stream.mu.Lock()
	defer stream.mu.Unlock()
	return stream.ProviderStreamStats.CompletionMarker
}

func observeProviderModelEvent(stats *ProviderStreamStats, event modeladapter.ModelEvent) {
	if stats == nil {
		return
	}
	stats.ModelEventCount++
	occurred := providerEventTime(event.OccurredAt, time.Now().UTC())
	if stats.FirstEventAt.IsZero() {
		stats.FirstEventAt = occurred
	}
	if strings.TrimSpace(event.Provider) != "" {
		stats.Provider = strings.TrimSpace(event.Provider)
	}
	if strings.TrimSpace(event.Model) != "" {
		stats.Model = strings.TrimSpace(event.Model)
	}
	switch event.Kind {
	case modeladapter.ModelEventKindTextDelta:
		stats.ChunkCount++
		stats.VisibleTextBytes += len(event.Text)
		stats.LastEffectiveContentAt = occurred
		stats.PartialBoundary = modeladapter.PartialBoundaryText
	case modeladapter.ModelEventKindThinkingDelta:
		stats.ChunkCount++
		stats.ReasoningBytes += len(event.Text)
		stats.LastEffectiveContentAt = occurred
		stats.PartialBoundary = modeladapter.PartialBoundaryReasoning
	case modeladapter.ModelEventKindPartialToolCall:
		stats.PartialToolCount++
		stats.LastEffectiveContentAt = occurred
		stats.PartialBoundary = modeladapter.PartialBoundaryPartialTool
		if stats.ToolDispatchState == "not_dispatched" {
			stats.ToolDispatchState = "partial_not_dispatched"
		}
	case modeladapter.ModelEventKindToolLikeCompleted:
		stats.CompletedToolCount++
		stats.LastEffectiveContentAt = occurred
		stats.PartialBoundary = modeladapter.PartialBoundaryCompletedTool
		if stats.ToolDispatchState == "not_dispatched" || stats.ToolDispatchState == "partial_not_dispatched" {
			stats.ToolDispatchState = "completed_not_dispatched"
		}
	case modeladapter.ModelEventKindTurnFinished:
		stats.CompletionMarker = true
	}
}

func markProviderDownstreamPublished(stream *ActiveStream) {
	if stream == nil {
		return
	}
	stream.mu.Lock()
	stream.ProviderStreamStats.DownstreamPublished = true
	stream.mu.Unlock()
}

func providerEventTime(value time.Time, fallback time.Time) time.Time {
	if value.IsZero() {
		return fallback
	}
	return value.UTC()
}

func providerTerminalAttribution(err error) string {
	switch {
	case errors.Is(err, context.DeadlineExceeded), modeladapter.ClassifyProviderError(err) == modeladapter.ProviderErrorStreamIdleTimeout:
		return "deadline"
	case errors.Is(err, context.Canceled):
		return "client"
	default:
		return "provider"
	}
}

func applyProviderTerminalErrorStats(stats *ProviderStreamStats, err error) {
	if stats == nil {
		return
	}
	stats.ErrorCategory = firstNonEmpty(modeladapter.ClassifyProviderError(err), "not_recorded")
	if httpStatus := providerHTTPStatusFromError(err); httpStatus != "" {
		stats.HTTPStatus = httpStatus
	}
	if httpAttempt := providerHTTPAttemptFromError(err); httpAttempt > 0 {
		stats.HTTPAttempt = httpAttempt
	}
	retryable, reason, suppression := providerRetryObservation(err, *stats)
	stats.Retryable = retryable
	stats.RetryReason = reason
	stats.RetrySuppressionReason = suppression
}

func applyProviderStreamDiagnostics(stats *ProviderStreamStats, diagnostics *modeladapter.StreamDiagnostics, err error, checkpointCommitted bool) {
	if stats == nil {
		return
	}
	snap := diagnostics.Snapshot()
	if !snap.HeaderAt.IsZero() {
		stats.HeaderAt = snap.HeaderAt
	}
	if !snap.FirstByteAt.IsZero() {
		stats.FirstByteAt = snap.FirstByteAt
	}
	if !snap.LastByteAt.IsZero() {
		stats.LastByteAt = snap.LastByteAt
	}
	if !snap.BodyEndAt.IsZero() {
		stats.BodyEndAt = snap.BodyEndAt
	}
	if stats.LastEffectiveContentAt.IsZero() && !snap.LastEffectiveContentAt.IsZero() {
		stats.LastEffectiveContentAt = snap.LastEffectiveContentAt
	}
	if snap.HTTPStatus > 0 && (strings.TrimSpace(stats.HTTPStatus) == "" || stats.HTTPStatus == "not_recorded") {
		stats.HTTPStatus = fmt.Sprintf("%d", snap.HTTPStatus)
	}
	if snap.HTTPAttempt > 0 {
		stats.HTTPAttempt = snap.HTTPAttempt
	}
	stats.HTTPProtocol = snap.HTTPProtocol
	stats.ContentEncoding = snap.ContentEncoding
	stats.AutoDecompressed = snap.AutoDecompressed
	stats.ContentLength = snap.ContentLength
	stats.ConnectionReused = snap.ConnectionReused
	stats.ConnectionWasIdle = snap.ConnectionWasIdle
	stats.ConnectionObserved = snap.ConnectionObserved
	stats.RawByteCount = snap.RawByteCount
	stats.LastErrorType = snap.LastErrorType
	stats.LastSSEEventType = snap.LastSSEEventType
	stats.LastSSEEventIDHash = snap.LastSSEEventIDHash
	stats.LastSSESequence = snap.LastSSESequence
	stats.LastResponseStatus = snap.LastResponseStatus
	stats.StreamRecoveryAttempts = snap.StreamRecoveryAttempts
	if snap.TransportOutcome != "" {
		stats.TransportOutcome = snap.TransportOutcome
	}
	closeCause := snap.CloseCause
	classified := modeladapter.ClassifyStreamCloseCause(err)
	stats.CloseCause = modeladapter.PreferCloseCause(closeCause, classified)
	if stats.TransportOutcome == "" {
		stats.TransportOutcome = modeladapter.TransportOutcomeStarted
	}
	finalizeProviderPartialBoundary(stats, checkpointCommitted)
}

func finalizeProviderPartialBoundary(stats *ProviderStreamStats, checkpointCommitted bool) {
	if stats == nil {
		return
	}
	if stats.ProtocolFinalStatus == "completed" {
		stats.PartialBoundary = modeladapter.PartialBoundaryNone
		return
	}
	if checkpointCommitted {
		stats.PartialBoundary = modeladapter.PartialBoundaryCheckpoint
		return
	}
	if strings.TrimSpace(stats.PartialBoundary) == "" {
		stats.PartialBoundary = modeladapter.PartialBoundaryNone
	}
}

func providerHTTPStatusFromError(err error) string {
	var httpErr *modeladapter.HTTPStatusError
	if errors.As(err, &httpErr) && httpErr != nil && httpErr.StatusCode > 0 {
		return fmt.Sprintf("%d", httpErr.StatusCode)
	}
	return ""
}

func providerHTTPAttemptFromError(err error) int {
	var httpErr *modeladapter.HTTPStatusError
	if errors.As(err, &httpErr) && httpErr != nil && httpErr.Attempt > 0 {
		return httpErr.Attempt
	}
	return 0
}

func providerRetryObservation(err error, stats ProviderStreamStats) (retryable string, reason string, suppression string) {
	if stats.DownstreamPublished || stats.VisibleTextBytes > 0 || stats.ReasoningBytes > 0 || stats.PartialToolCount > 0 || stats.CompletedToolCount > 0 || stats.DispatchedToolCount > 0 || stats.ModelEventCount > 0 {
		return "false", "not_recorded", "output_or_tool_progress"
	}
	if errors.Is(err, context.Canceled) {
		return "false", "not_recorded", "context_canceled"
	}
	if errors.Is(err, context.DeadlineExceeded) || modeladapter.ClassifyProviderError(err) == modeladapter.ProviderErrorStreamIdleTimeout {
		return "false", "not_recorded", "deadline"
	}
	var truncated *modeladapter.StreamTruncatedError
	if errors.As(err, &truncated) {
		if truncated != nil && truncated.Err == nil {
			return "false", "not_recorded", "missing_completion_marker"
		}
		if stats.ChunkCount > 0 || stats.CompletionMarker {
			return "false", "not_recorded", "stream_raw_bytes"
		}
		return "false", "pre_event_eof", "not_recorded"
	}
	var httpErr *modeladapter.HTTPStatusError
	if errors.As(err, &httpErr) && httpErr != nil && httpErr.StatusCode > 0 {
		eligible, retryReason, retrySuppression := modeladapter.HTTPRetryObservation(httpErr.StatusCode)
		if eligible {
			return "true", retryReason, retrySuppression
		}
		return "false", retryReason, retrySuppression
	}
	category := modeladapter.ClassifyProviderError(err)
	switch category {
	case modeladapter.ProviderErrorRateLimited:
		return "true", "http_429", "not_recorded"
	case modeladapter.ProviderErrorServer5xx:
		return "false", "not_recorded", "http_status"
	case modeladapter.ProviderErrorTransport:
		return "true", "transport", "not_recorded"
	case modeladapter.ProviderErrorStatus4xx:
		return "false", "not_recorded", "http_status"
	default:
		return "unknown", "not_recorded", "not_recorded"
	}
}

func providerTerminalFields(modelCallID string, stats ProviderStreamStats) map[string]any {
	duration := stats.StreamDuration
	if duration <= 0 && !stats.StartedAt.IsZero() && !stats.FinishedAt.IsZero() {
		duration = stats.FinishedAt.Sub(stats.StartedAt)
	}
	if duration < 0 {
		duration = 0
	}
	fields := map[string]any{
		"model_call_id": modelCallID, "provider_pass": stats.Attempt,
		"status":       providerProtocolStatus(stats.ProtocolFinalStatus),
		"http_attempt": providerHTTPAttemptField(stats.HTTPAttempt), "http_status": firstNonEmpty(stats.HTTPStatus, "not_recorded"),
		"http_protocol": firstNonEmpty(stats.HTTPProtocol, "not_recorded"), "content_encoding": firstNonEmpty(stats.ContentEncoding, "not_recorded"),
		"auto_decompressed": stats.AutoDecompressed, "content_length": stats.ContentLength,
		"connection_observed": stats.ConnectionObserved, "connection_reused": stats.ConnectionReused, "connection_was_idle": stats.ConnectionWasIdle,
		"raw_byte_count": stats.RawByteCount, "last_error_type": firstNonEmpty(stats.LastErrorType, "not_recorded"),
		"last_sse_event_type": firstNonEmpty(stats.LastSSEEventType, "not_recorded"), "last_sse_event_id_hash": firstNonEmpty(stats.LastSSEEventIDHash, "not_recorded"),
		"last_sse_sequence": stats.LastSSESequence, "last_response_status": firstNonEmpty(stats.LastResponseStatus, "not_recorded"),
		"stream_recovery_attempts": stats.StreamRecoveryAttempts,
		"provider":                 firstNonEmpty(stats.Provider, "unknown"), "model": firstNonEmpty(stats.Model, "unknown"), "attribution": firstNonEmpty(stats.Attribution, "unknown"),
		"completion_marker": stats.CompletionMarker, "model_event_count": stats.ModelEventCount,
		"chunk_count": stats.ChunkCount, "visible_text_bytes": stats.VisibleTextBytes,
		"reasoning_bytes": stats.ReasoningBytes, "partial_tool_count": stats.PartialToolCount,
		"completed_tool_count": stats.CompletedToolCount, "dispatched_tool_count": stats.DispatchedToolCount,
		"tool_dispatch_state": firstNonEmpty(stats.ToolDispatchState, "not_recorded"), "downstream_published": stats.DownstreamPublished,
		"potential_side_effect": firstNonEmpty(stats.PotentialSideEffect, "unknown"), "first_event_at": optionalTimeField(stats.FirstEventAt),
		"header_at": optionalTimeField(stats.HeaderAt), "first_byte_at": optionalTimeField(stats.FirstByteAt),
		"last_byte_at": optionalTimeField(stats.LastByteAt), "body_end_at": optionalTimeField(stats.BodyEndAt),
		"last_effective_content_at": optionalTimeField(stats.LastEffectiveContentAt),
		"close_cause":               firstNonEmpty(stats.CloseCause, "not_recorded"), "partial_boundary": firstNonEmpty(stats.PartialBoundary, "none"),
		"transport_outcome": firstNonEmpty(stats.TransportOutcome, "started"),
		"duration_ms":       duration.Milliseconds(), "retryable": firstNonEmpty(stats.Retryable, "unknown"),
		"retry_reason": firstNonEmpty(stats.RetryReason, "not_recorded"), "retry_suppression_reason": firstNonEmpty(stats.RetrySuppressionReason, "not_recorded"),
		"protocol_final_status": firstNonEmpty(stats.ProtocolFinalStatus, "unknown"), "model_call_final_status": firstNonEmpty(stats.ModelCallFinalStatus, "not_recorded"),
		"failure_stage":  firstNonEmpty(stats.FailureStage, "not_recorded"),
		"error_category": firstNonEmpty(stats.ErrorCategory, "not_recorded"), "error_summary": safeProviderErrorSummary(stats),
	}
	if continuedFrom := strings.TrimSpace(stats.ContinuedFromModelCallID); continuedFrom != "" {
		fields["continued_from_model_call_id"] = continuedFrom
	}
	if stats.ContinuationIndex > 0 {
		fields["continuation_index"] = stats.ContinuationIndex
	}
	return fields
}

func unwrapProviderTerminalCause(err error) error {
	var providerErr providerTerminalError
	if errors.As(err, &providerErr) {
		return providerErr.Unwrap()
	}
	return err
}

func optionalTimeField(value time.Time) any {
	if value.IsZero() {
		return "not_recorded"
	}
	return value.UTC().Format(time.RFC3339Nano)
}

func providerHTTPAttemptField(attempt int) any {
	if attempt < 1 {
		return "not_recorded"
	}
	return attempt
}

func providerProtocolStatus(protocolStatus string) string {
	switch strings.TrimSpace(protocolStatus) {
	case "completed":
		return "completed"
	case "canceled":
		return "canceled"
	case "timeout":
		return "timeout"
	case "truncated", "provider_failed":
		return "error"
	default:
		return "unknown"
	}
}

func (service *Service) recordProviderTerminal(stream *ActiveStream) {
	if service == nil || stream == nil || service.debug == nil {
		return
	}
	stream.mu.Lock()
	if stream.ProviderTerminalRecorded || strings.TrimSpace(stream.CurrentModelCallID) == "" || stream.ProviderStreamStats.Attempt < 1 {
		stream.mu.Unlock()
		return
	}
	stream.ProviderTerminalRecorded = true
	stats := stream.ProviderStreamStats
	requestID, conversationID, modelCallID := stream.RequestID, stream.ConversationID, stream.CurrentModelCallID
	stream.mu.Unlock()
	service.debug.LogProvider(context.Background(), requestID, conversationID, "provider_stream_finished", providerTerminalFields(modelCallID, stats))
}

func (service *Service) recordModelCallFinal(stream *ActiveStream, businessOutcome string) {
	if service == nil || stream == nil || service.debug == nil {
		return
	}
	businessOutcome = strings.TrimSpace(businessOutcome)
	if businessOutcome == "" {
		businessOutcome = "failed"
	}
	stream.mu.Lock()
	modelCallID := strings.TrimSpace(stream.CurrentModelCallID)
	if modelCallID == "" || stream.ProviderStreamStats.Attempt < 1 {
		stream.mu.Unlock()
		return
	}
	// Check per-model_call_id deduplication map instead of stream-level bool
	if stream.FinalizedModelCallIDs == nil {
		stream.FinalizedModelCallIDs = make(map[string]struct{})
	}
	if _, alreadyFinalized := stream.FinalizedModelCallIDs[modelCallID]; alreadyFinalized {
		stream.mu.Unlock()
		return
	}
	stream.FinalizedModelCallIDs[modelCallID] = struct{}{}
	stream.ProviderStreamStats.ModelCallFinalStatus = businessOutcome
	stats := stream.ProviderStreamStats
	requestID, conversationID := stream.RequestID, stream.ConversationID
	stream.mu.Unlock()
	fields := providerTerminalFields(modelCallID, stats)
	fields["status"] = businessOutcome
	fields["business_outcome"] = businessOutcome
	service.debug.LogRuntime(context.Background(), requestID, conversationID, "model_call_final", fields)
}

func safeProviderErrorSummary(stats ProviderStreamStats) string {
	if strings.TrimSpace(stats.ErrorCategory) == "" {
		return "not_recorded"
	}
	return strings.TrimSpace(stats.ErrorCategory) + " status=" + firstNonEmpty(strings.TrimSpace(stats.HTTPStatus), "not_recorded")
}

func providerFailureBusinessOutcome(stats ProviderStreamStats) string {
	if stats.ModelEventCount > 0 || stats.DownstreamPublished || stats.VisibleTextBytes > 0 || stats.ReasoningBytes > 0 || stats.PartialToolCount > 0 || stats.CompletedToolCount > 0 || stats.DispatchedToolCount > 0 {
		return "partial"
	}
	if stats.ProtocolFinalStatus == "timeout" {
		return "timeout"
	}
	if stats.ProtocolFinalStatus == "canceled" {
		return "canceled"
	}
	return "failed"
}

func providerTokenMatches(stream *ActiveStream, token uint64) bool {
	if stream == nil || token == 0 {
		return false
	}
	stream.mu.Lock()
	defer stream.mu.Unlock()
	return stream.CurrentProviderToken == token
}

func (service *Service) applyProviderModelEvent(stream *ActiveStream, event modeladapter.ModelEvent) error {
	if stream == nil {
		return nil
	}
	stream.mu.Lock()
	requestID := stream.RequestID
	conversationID := stream.ConversationID
	turnSeq := stream.TurnSeq
	modelCallID := stream.CurrentModelCallID
	accumulatedText := stream.ProviderAccumulatedText
	accumulatedReasoning := stream.ProviderAccumulatedReasoning
	accumulatedReasoningSignature := stream.ProviderAccumulatedReasoningSignature
	accumulatedReasoningSignatureSource := stream.ProviderAccumulatedReasoningSignatureSource
	accumulatedReasoningItemID := stream.ProviderAccumulatedReasoningItemID
	accumulatedReasoningStatus := stream.ProviderAccumulatedReasoningStatus
	accumulatedReasoningSummary := append([]byte(nil), stream.ProviderAccumulatedReasoningSummary...)
	observeProviderModelEvent(&stream.ProviderStreamStats, event)
	dropChildUnsafe := continuationShouldDropChildEventLocked(stream, event)
	stream.mu.Unlock()
	if dropChildUnsafe {
		return nil
	}

	switch event.Kind {
	case modeladapter.ModelEventKindTextDelta:
		stream.mu.Lock()
		stream.ProviderAccumulatedText += event.Text
		publishText := event.Text
		suppressPublish := false
		mismatch := false
		if stream.ContinuationIndex > 0 {
			publishText, suppressPublish, mismatch = consumeContinuationDeltaLocked(stream, "text")
		}
		stream.UpdatedAt = time.Now().UTC()
		stream.mu.Unlock()
		if mismatch || suppressPublish || publishText == "" {
			return nil
		}
		if err := service.broker.Publish(requestID, StreamEvent{Message: buildTextDeltaMessage(publishText)}); err != nil {
			return err
		}
		markProviderDownstreamPublished(stream)
		return nil
	case modeladapter.ModelEventKindThinkingDelta:
		stream.mu.Lock()
		stream.ProviderAccumulatedReasoning += event.Text
		publishText := event.Text
		suppressPublish := false
		mismatch := false
		if stream.ContinuationIndex > 0 {
			publishText, suppressPublish, mismatch = consumeContinuationDeltaLocked(stream, "reasoning")
		}
		stream.UpdatedAt = time.Now().UTC()
		stream.mu.Unlock()
		if mismatch || suppressPublish || publishText == "" {
			return nil
		}
		if err := service.broker.Publish(requestID, StreamEvent{Message: buildThinkingDeltaMessage(publishText, event.ThinkingStyle)}); err != nil {
			return err
		}
		markProviderDownstreamPublished(stream)
		return nil
	case modeladapter.ModelEventKindThinkingCompleted:
		shouldEmitSyntheticThinking := false
		suppressThinkingCompleted := false
		completedDuration := event.ThinkingDurationMS
		if strings.TrimSpace(event.ThinkingSignature) != "" {
			stream.mu.Lock()
			stream.ProviderAccumulatedReasoningSignature = strings.TrimSpace(event.ThinkingSignature)
			stream.ProviderAccumulatedReasoningSignatureSource = strings.TrimSpace(event.ThinkingSignatureSource)
			stream.ProviderAccumulatedReasoningItemID = strings.TrimSpace(event.ProviderItemID)
			stream.ProviderAccumulatedReasoningStatus = strings.TrimSpace(event.ProviderStatus)
			stream.ProviderAccumulatedReasoningSummary = append([]byte(nil), event.ProviderSummary...)
			shouldEmitSyntheticThinking = strings.TrimSpace(stream.ProviderAccumulatedReasoning) == "" &&
				strings.TrimSpace(event.ThinkingSignatureSource) == modeladapter.ReasoningSignatureSourceOpenAIResponses
			if shouldEmitSyntheticThinking {
				if stream.ProviderSyntheticThinkingStartedAt.IsZero() {
					stream.ProviderSyntheticThinkingStartedAt = time.Now().UTC()
				}
				if completedDuration <= 0 {
					completedDuration = int32(time.Since(stream.ProviderSyntheticThinkingStartedAt).Milliseconds())
					if completedDuration <= 0 {
						completedDuration = 1
					}
				}
				if !stream.ProviderSyntheticThinkingPublished {
					stream.ProviderSyntheticThinkingPublished = true
				} else {
					shouldEmitSyntheticThinking = false
					suppressThinkingCompleted = true
				}
			}
			stream.UpdatedAt = time.Now().UTC()
			stream.mu.Unlock()
		}
		if shouldEmitSyntheticThinking {
			if err := service.broker.Publish(requestID, StreamEvent{Message: buildThinkingDeltaMessage("The reasoning process is encrypted. Please wait a moment. (This message does not affect any functionality; it only indicates the current reasoning status.)", event.ThinkingStyle)}); err != nil {
				return err
			}
			markProviderDownstreamPublished(stream)
		}
		if suppressThinkingCompleted {
			return nil
		}
		if err := service.broker.Publish(requestID, StreamEvent{Message: buildThinkingCompletedMessage(completedDuration)}); err != nil {
			return err
		}
		markProviderDownstreamPublished(stream)
		return nil
	case modeladapter.ModelEventKindPartialToolCall:
		toolCallID := strings.TrimSpace(event.ToolCallID)
		if toolCallID == "" || event.ToolCall == nil {
			return nil
		}
		displayToolCall := service.rewriteTaskToolCallModelForDisplay(stream, event.ToolCall)
		stream.mu.Lock()
		if stream.PartialToolCallIDs == nil {
			stream.PartialToolCallIDs = make(map[string]struct{})
		}
		stream.PartialToolCallIDs[toolCallID] = struct{}{}
		stream.UpdatedAt = time.Now().UTC()
		stream.mu.Unlock()
		var publishErr error
		if inferToolName(displayToolCall) == "GenerateImage" {
			publishErr = service.broker.Publish(requestID, StreamEvent{
				Message: buildToolCallStartedMessage(toolCallID, modelCallID, displayToolCall),
			})
		} else {
			publishErr = service.broker.Publish(requestID, StreamEvent{
				Message: buildPartialToolCallMessage(toolCallID, modelCallID, displayToolCall, event.ArgsTextDelta),
			})
		}
		if publishErr != nil {
			return publishErr
		}
		markProviderDownstreamPublished(stream)
		return nil
	case modeladapter.ModelEventKindToolCallDelta:
		if strings.TrimSpace(event.ToolCallID) == "" || event.ToolCallDelta == nil {
			return nil
		}
		if err := service.broker.Publish(requestID, StreamEvent{
			Message: buildToolCallDeltaMessage(event.ToolCallID, modelCallID, event.ToolCallDelta),
		}); err != nil {
			return err
		}
		markProviderDownstreamPublished(stream)
		return nil
	case modeladapter.ModelEventKindToolLikeCompleted:
		reasoningForTool := accumulatedReasoning
		reasoningSignatureForTool := accumulatedReasoningSignature
		reasoningSignatureSourceForTool := accumulatedReasoningSignatureSource
		reasoningItemIDForTool := accumulatedReasoningItemID
		reasoningStatusForTool := accumulatedReasoningStatus
		reasoningSummaryForTool := append([]byte(nil), accumulatedReasoningSummary...)
		if strings.TrimSpace(accumulatedText) != "" {
			if err := service.flushAssistantText(stream, conversationID, turnSeq, requestID, accumulatedText, accumulatedReasoning, accumulatedReasoningSignature, accumulatedReasoningSignatureSource, accumulatedReasoningItemID, accumulatedReasoningStatus, accumulatedReasoningSummary, false); err != nil {
				return err
			}
		}
		if event.ToolInvocation == nil {
			return fmt.Errorf("tool invocation is required")
		}
		invocation := *event.ToolInvocation
		invocation.ReasoningContent = reasoningForTool
		invocation.ReasoningSignature = reasoningSignatureForTool
		invocation.ReasoningSignatureSource = reasoningSignatureSourceForTool
		invocation.ReasoningProviderItemID = reasoningItemIDForTool
		invocation.ReasoningProviderStatus = reasoningStatusForTool
		invocation.ReasoningProviderSummary = reasoningSummaryForTool
		invocation.ModelCallID = modelCallID
		stream.mu.Lock()
		stream.ProviderAccumulatedText = ""
		stream.UpdatedAt = time.Now().UTC()
		stream.mu.Unlock()
		if err := service.handleToolInvocation(stream, invocation); err != nil {
			return err
		}
		stream.mu.Lock()
		stream.ProviderStreamStats.DispatchedToolCount++
		stream.ProviderStreamStats.ToolDispatchState = "dispatched"
		stream.ProviderStreamStats.PotentialSideEffect = "possible"
		stream.mu.Unlock()
		return nil
	case modeladapter.ModelEventKindTurnFinished:
		stream.mu.Lock()
		stream.ProviderFinishReason = strings.TrimSpace(event.FinishReason)
		stream.ProviderUsage = turnUsageSnapshot{
			Provider:          event.Provider,
			Model:             event.Model,
			InputTokens:       event.InputTokens,
			OutputTokens:      event.OutputTokens,
			CacheReadTokens:   event.CacheReadTokens,
			CacheWriteTokens:  event.CacheWriteTokens,
			UsagePresent:      event.UsagePresent,
			CacheReadPresent:  event.CacheReadPresent,
			CacheWritePresent: event.CacheWritePresent,
		}
		stream.UpdatedAt = time.Now().UTC()
		stream.mu.Unlock()
		return nil
	case modeladapter.ModelEventKindProviderError:
		if event.Err != nil {
			return providerTerminalError{cause: event.Err}
		}
		return providerTerminalError{cause: fmt.Errorf("provider error")}
	default:
		return nil
	}
}

func (service *Service) rewriteTaskToolCallModelForDisplay(stream *ActiveStream, toolCall *agentv1.ToolCall) *agentv1.ToolCall {
	if service == nil || stream == nil || toolCall == nil {
		return toolCall
	}
	taskToolCall := toolCall.GetTaskToolCall()
	if taskToolCall == nil || taskToolCall.GetArgs() == nil {
		return toolCall
	}
	subagentType := taskSubagentTypeNameForDisplay(taskToolCall.GetArgs().GetSubagentType())
	stream.mu.Lock()
	parentModelID := strings.TrimSpace(stream.ModelID)
	overrides := cloneSubagentModelOverrides(stream.SubagentModelOverrides)
	stream.mu.Unlock()
	effectiveModelID := effectiveTaskDisplayModelID(subagentType, parentModelID, overrides)
	if effectiveModelID == "" {
		return toolCall
	}
	cloned, ok := proto.Clone(toolCall).(*agentv1.ToolCall)
	if !ok || cloned == nil {
		return toolCall
	}
	clonedTaskToolCall := cloned.GetTaskToolCall()
	if clonedTaskToolCall == nil || clonedTaskToolCall.GetArgs() == nil {
		return toolCall
	}
	clonedTaskToolCall.Args.Model = &effectiveModelID
	return cloned
}

func taskSubagentTypeNameForDisplay(subagentType *agentv1.SubagentType) string {
	if subagentType == nil || subagentType.GetType() == nil {
		return ""
	}
	switch item := subagentType.GetType().(type) {
	case *agentv1.SubagentType_Explore:
		return "explore"
	case *agentv1.SubagentType_BrowserUse:
		return "browser-use"
	case *agentv1.SubagentType_Shell:
		return "shell"
	case *agentv1.SubagentType_Custom:
		return strings.TrimSpace(item.Custom.GetName())
	default:
		return ""
	}
}

func effectiveTaskDisplayModelID(subagentType string, parentModelID string, overrides map[string]runtimecore.SubagentModelOverrideSelection) string {
	if override, _, ok := runtimecore.LookupSubagentModelOverride(overrides, subagentType); ok {
		switch strings.TrimSpace(override.Selection) {
		case "model":
			return strings.TrimSpace(override.ModelID)
		case "inherit":
			return strings.TrimSpace(parentModelID)
		case "disabled":
			return ""
		}
	}
	return ""
}

func (service *Service) handleProviderDoneEvent(stream *ActiveStream, payload *streamProviderEvent) error {
	if stream == nil || payload == nil || errors.Is(payload.Err, errProviderLoopInterrupted) {
		return nil
	}

	stream.mu.Lock()
	if isTerminalStreamStatus(stream.Status) {
		stream.mu.Unlock()
		return nil
	}
	requestID := stream.RequestID
	conversationID := stream.ConversationID
	turnSeq := stream.TurnSeq
	modelCallID := stream.CurrentModelCallID
	accumulatedText := stream.ProviderAccumulatedText
	accumulatedReasoning := stream.ProviderAccumulatedReasoning
	accumulatedReasoningSignature := stream.ProviderAccumulatedReasoningSignature
	accumulatedReasoningSignatureSource := stream.ProviderAccumulatedReasoningSignatureSource
	accumulatedReasoningItemID := stream.ProviderAccumulatedReasoningItemID
	accumulatedReasoningStatus := stream.ProviderAccumulatedReasoningStatus
	accumulatedReasoningSummary := append([]byte(nil), stream.ProviderAccumulatedReasoningSummary...)
	finishReason := stream.ProviderFinishReason
	usage := stream.ProviderUsage
	hadToolInvocation := stream.ToolInvocationCount > 0
	terminalToolInvocation := stream.ProviderTerminalToolInvocation
	existingCompletion := stream.PendingProviderCompletion
	providerPass := stream.ProviderPassCount
	continuationIndex := stream.ContinuationIndex
	continuationMismatch := stream.ContinuationOverlapMismatch
	continuationRemainderText := stream.ContinuationRemainderText
	continuationRemainderReasoning := stream.ContinuationRemainderReasoning
	stream.ProviderStreamStats.ContinuedFromModelCallID = stream.ContinuedFromModelCallID
	stream.ProviderStreamStats.ContinuationIndex = stream.ContinuationIndex
	stream.ProviderStreamStats.FinishedAt = time.Now().UTC()
	if !stream.ProviderStreamStats.StartedAt.IsZero() {
		stream.ProviderStreamStats.StreamDuration = stream.ProviderStreamStats.FinishedAt.Sub(stream.ProviderStreamStats.StartedAt)
	}
	if payload.Err != nil {
		stream.ProviderStreamStats.Attribution = providerTerminalAttribution(payload.Err)
		stream.ProviderStreamStats.FailureStage = "provider_stream"
		applyProviderTerminalErrorStats(&stream.ProviderStreamStats, payload.Err)
		switch stream.ProviderStreamStats.Attribution {
		case "client":
			stream.ProviderStreamStats.ProtocolFinalStatus = "canceled"
		case "deadline":
			stream.ProviderStreamStats.ProtocolFinalStatus = "timeout"
		default:
			stream.ProviderStreamStats.ProtocolFinalStatus = "provider_failed"
		}
	} else if stream.ProviderStreamStats.CompletionMarker {
		stream.ProviderStreamStats.ProtocolFinalStatus = "completed"
		stream.ProviderStreamStats.Attribution = "provider"
		stream.ProviderStreamStats.Retryable = "false"
		stream.ProviderStreamStats.RetryReason = "not_recorded"
		stream.ProviderStreamStats.RetrySuppressionReason = "not_recorded"
	} else {
		stream.ProviderStreamStats.ProtocolFinalStatus = "truncated"
		stream.ProviderStreamStats.Attribution = "protocol"
		stream.ProviderStreamStats.ErrorCategory = "stream_decode"
		stream.ProviderStreamStats.FailureStage = "provider_protocol"
		applyProviderTerminalErrorStats(&stream.ProviderStreamStats, &modeladapter.StreamTruncatedError{Provider: firstNonEmpty(stream.ProviderStreamStats.Provider, "provider")})
	}
	checkpointCommitted := len(stream.ConfirmedCheckpointBlobs) > 0
	diagErr := unwrapProviderTerminalCause(payload.Err)
	if diagErr == nil && stream.ProviderStreamStats.ProtocolFinalStatus == "truncated" {
		diagErr = &modeladapter.StreamTruncatedError{Provider: firstNonEmpty(stream.ProviderStreamStats.Provider, "provider")}
	}
	applyProviderStreamDiagnostics(&stream.ProviderStreamStats, stream.ProviderStreamDiagnostics, diagErr, checkpointCommitted)
	stream.ProviderStreamStats.ModelCallFinalStatus = "not_finalized"
	stream.ProviderActive = false
	stream.ProviderCancel = nil
	stream.PendingProviderAction = providerActionNone
	stream.ProviderAccumulatedText = ""
	stream.ProviderAccumulatedReasoning = ""
	stream.ProviderAccumulatedReasoningSignature = ""
	stream.ProviderAccumulatedReasoningSignatureSource = ""
	stream.ProviderAccumulatedReasoningItemID = ""
	stream.ProviderAccumulatedReasoningStatus = ""
	stream.ProviderAccumulatedReasoningSummary = nil
	stream.ProviderFinishReason = ""
	stream.ProviderUsage = turnUsageSnapshot{}
	stream.ProviderTerminalToolInvocation = false
	stream.ToolInvocationCount = 0
	status := stream.Status
	stream.UpdatedAt = time.Now().UTC()
	stream.mu.Unlock()

	if isTerminalStreamStatus(status) {
		return nil
	}
	if payload.Err == nil && !streamProviderCompletionMarker(stream) {
		payload.Err = providerTerminalError{cause: &modeladapter.StreamTruncatedError{Provider: "provider"}}
	}
	service.recordProviderTerminal(stream)
	if payload.Err != nil {
		spawned, spawnErr := service.trySpawnStreamContinuation(
			stream, payload, conversationID, turnSeq, requestID, modelCallID, providerPass,
			accumulatedText, accumulatedReasoning, accumulatedReasoningSignature, accumulatedReasoningSignatureSource,
			accumulatedReasoningItemID, accumulatedReasoningStatus, accumulatedReasoningSummary, usage, hadToolInvocation,
		)
		if spawnErr != nil {
			return service.failStreamIfNonTerminal(stream, "unknown", spawnErr)
		}
		if spawned {
			return nil
		}
		flushText := continuationFlushText(continuationIndex, continuationMismatch, accumulatedText, continuationRemainderText)
		flushReasoning := continuationFlushText(continuationIndex, continuationMismatch, accumulatedReasoning, continuationRemainderReasoning)
		if fuse, reason := continuationShouldFusePartial(stream, payload); fuse {
			service.logStreamContinuationEvent(stream, "stream_continuation_fused", map[string]any{
				"model_call_id": strings.TrimSpace(modelCallID),
				"reason":        reason,
			})
		}
		stream.mu.Lock()
		businessOutcome := providerFailureBusinessOutcome(stream.ProviderStreamStats)
		stream.mu.Unlock()
		var terminalErr error
		var providerErr providerTerminalError
		if errors.As(payload.Err, &providerErr) {
			service.setTurnPhase(stream, TurnPhaseFailed)
			terminalErr = service.closeStreamWithProviderError(stream, conversationID, turnSeq, requestID, flushText, flushReasoning, accumulatedReasoningSignature, accumulatedReasoningSignatureSource, accumulatedReasoningItemID, accumulatedReasoningStatus, accumulatedReasoningSummary, usage, providerErr, !hadToolInvocation)
		} else {
			if err := service.flushFailedProviderOutput(stream, conversationID, turnSeq, requestID, modelCallID, providerPass, flushText, flushReasoning, accumulatedReasoningSignature, accumulatedReasoningSignatureSource, accumulatedReasoningItemID, accumulatedReasoningStatus, accumulatedReasoningSummary, !hadToolInvocation); err != nil {
				terminalErr = service.failStream(stream, "unknown", fmt.Errorf("flush failed provider output: %w", err))
			} else {
				service.setTurnPhase(stream, TurnPhaseFailed)
				terminalErr = service.failStream(stream, "unknown", payload.Err)
			}
		}
		service.recordModelCallFinal(stream, businessOutcome)
		return terminalErr
	}
	if fuse, reason := continuationShouldFusePartial(stream, payload); fuse {
		service.logStreamContinuationEvent(stream, "stream_continuation_fused", map[string]any{
			"model_call_id": strings.TrimSpace(modelCallID),
			"reason":        reason,
		})
		flushText := continuationFlushText(continuationIndex, continuationMismatch, accumulatedText, continuationRemainderText)
		flushReasoning := continuationFlushText(continuationIndex, continuationMismatch, accumulatedReasoning, continuationRemainderReasoning)
		if err := service.flushFailedProviderOutput(stream, conversationID, turnSeq, requestID, modelCallID, providerPass, flushText, flushReasoning, accumulatedReasoningSignature, accumulatedReasoningSignatureSource, accumulatedReasoningItemID, accumulatedReasoningStatus, accumulatedReasoningSummary, !hadToolInvocation); err != nil {
			return service.failStreamIfNonTerminal(stream, "unknown", err)
		}
		if err := service.recordTurnUsageSnapshot(stream, conversationID, turnSeq, requestID, modelCallID, "partial", usage, reason, false); err != nil {
			return service.failStreamIfNonTerminal(stream, "usage_persistence_error", err)
		}
		service.recordModelCallFinal(stream, "partial")
		service.setTurnPhase(stream, TurnPhaseFailed)
		return service.failStream(stream, "provider_error", errors.New(reason))
	}
	flushText := continuationFlushText(continuationIndex, continuationMismatch, accumulatedText, continuationRemainderText)
	flushReasoning := continuationFlushText(continuationIndex, continuationMismatch, accumulatedReasoning, continuationRemainderReasoning)
	if err := service.flushAssistantText(stream, conversationID, turnSeq, requestID, flushText, flushReasoning, accumulatedReasoningSignature, accumulatedReasoningSignatureSource, accumulatedReasoningItemID, accumulatedReasoningStatus, accumulatedReasoningSummary, !hadToolInvocation); err != nil {
		return service.failStreamIfNonTerminal(stream, "unknown", err)
	}
	if err := service.recordTurnUsageSnapshot(stream, conversationID, turnSeq, requestID, modelCallID, "completed", usage, "", false); err != nil {
		return service.failStreamIfNonTerminal(stream, "usage_persistence_error", err)
	}
	if err := service.updateConversationTokenState(stream, conversationID, usage, modelCallID, true); err != nil {
		return service.failStreamIfNonTerminal(stream, "unknown", err)
	}
	if err := service.syncSummaryCarryForward(conversationID, requestID, modelCallID); err != nil {
		return service.failStreamIfNonTerminal(stream, "unknown", err)
	}

	pendingCount := pendingBridgeCount(stream)
	if pendingCount > 0 {
		awaitingUser := hasPendingAwaitingUserInteraction(stream)
		forceComplete := awaitingUser
		disposition := completionDispositionForExternalResults(finishReason, forceComplete, hadToolInvocation)
		rememberPendingProviderCompletion(stream, pendingTurnCompletion{
			ConversationID: conversationID,
			RequestID:      requestID,
			TurnSeq:        turnSeq,
			ModelCallID:    modelCallID,
			ProviderPass:   currentProviderPass(stream),
			Usage:          usage,
			Disposition:    disposition,
		})
		if awaitingUser {
			service.setTurnPhase(stream, TurnPhaseAwaitingUser)
		} else {
			service.setTurnPhase(stream, TurnPhaseWaitingExternal)
		}
		if err := service.publishCheckpoint(requestID, conversationID); err != nil {
			return service.failStreamIfNonTerminal(stream, "unknown", err)
		}
		// For terminal disposition, model_call_final recorded in finishSuccessfulTurnAfterCheckpoint
		// For resume disposition, current model call succeeded before next pass
		if disposition == completionDispositionResumeAfterExternal {
			service.recordModelCallFinal(stream, "succeeded")
		}
		return nil
	}

	if existingCompletion == nil {
		handled, err := service.handleSubagentEmptyStopAfterToolResult(stream, conversationID, turnSeq, requestID, modelCallID, finishReason, accumulatedText)
		if err != nil {
			return service.failStreamIfNonTerminal(stream, "unknown", err)
		}
		if handled {
			// Subagent empty stop: either failed (model_call_final in failStream) or resumed (not final yet)
			return nil
		}
	}

	if existingCompletion != nil {
		completion := *existingCompletion
		if strings.TrimSpace(completion.ModelCallID) == "" {
			completion.ModelCallID = modelCallID
		}
		if completion.ProviderPass == 0 {
			completion.ProviderPass = currentProviderPass(stream)
		}
		completion.Usage = usage
		clearPendingProviderCompletion(stream)
		if completion.Disposition == completionDispositionResumeAfterExternal {
			if err := service.publishCheckpoint(requestID, conversationID); err != nil {
				return service.failStreamIfNonTerminal(stream, "unknown", err)
			}
			if err := service.requestProviderAction(stream, providerActionResume); err != nil {
				return service.failStreamIfNonTerminal(stream, "unknown", err)
			}
			// Current model call completed successfully before next pass starts
			service.recordModelCallFinal(stream, "succeeded")
			return nil
		}
		// Terminal completion: delegate to completeSuccessfulTurn which will queue checkpoint and finalize in finishSuccessfulTurnAfterCheckpoint
		if err := service.completeSuccessfulTurn(stream, completion); err != nil {
			return service.failStreamIfNonTerminal(stream, "unknown", err)
		}
		return nil
	}

	if (hadToolInvocation || shouldResumeAfterToolResults(finishReason)) && !terminalToolInvocation {
		if err := service.publishCheckpoint(requestID, conversationID); err != nil {
			return service.failStreamIfNonTerminal(stream, "unknown", err)
		}
		if err := service.requestProviderAction(stream, providerActionResume); err != nil {
			return service.failStreamIfNonTerminal(stream, "unknown", err)
		}
		// Current model call completed successfully before next pass starts
		service.recordModelCallFinal(stream, "succeeded")
		return nil
	}

	clearPendingProviderCompletion(stream)
	if err := service.completeSuccessfulTurn(stream, pendingTurnCompletion{
		ConversationID: conversationID,
		RequestID:      requestID,
		TurnSeq:        turnSeq,
		ModelCallID:    modelCallID,
		ProviderPass:   currentProviderPass(stream),
		Usage:          usage,
	}); err != nil {
		return service.failStreamIfNonTerminal(stream, "unknown", err)
	}
	return nil
}

const subagentEmptyStopErrorText = "subagent returned empty response after tool result"

func (service *Service) handleSubagentEmptyStopAfterToolResult(stream *ActiveStream, conversationID string, turnSeq int64, requestID string, modelCallID string, finishReason string, accumulatedText string) (bool, error) {
	if stream == nil || strings.TrimSpace(finishReason) != "stop" || strings.TrimSpace(accumulatedText) != "" {
		return false, nil
	}
	conversation, _, _, err := service.snapshotCheckpointConversation(stream)
	if err != nil {
		return true, err
	}
	if conversation == nil || !isChildConversationSubagentTypeName(conversation.SubagentTypeName) || !currentTurnHasToolResult(conversation, turnSeq) {
		return false, nil
	}
	if currentTurnHasPromptContextSource(conversation, turnSeq, promptContextSourceSubagentEmptyStopRecovery) {
		service.setTurnPhase(stream, TurnPhaseFailed)
		return true, service.failStream(stream, "empty_response", errors.New(subagentEmptyStopErrorText))
	}
	context := newPromptContextReminder(promptContextSourceSubagentEmptyStopRecovery, subagentEmptyStopRecoveryText())
	if _, err := service.appendConversationEntries(stream, conversationID, []HistoryEntry{
		newPromptContextEntry(turnSeq, requestID, context),
	}); err != nil {
		return true, err
	}
	if err := service.syncSummaryCarryForward(conversationID, requestID, modelCallID); err != nil {
		return true, err
	}
	if err := service.publishCheckpoint(requestID, conversationID); err != nil {
		return true, err
	}
	if err := service.requestProviderAction(stream, providerActionResume); err != nil {
		return true, err
	}
	// Current model call (empty stop) completed, recovery prompt injected, next pass will start
	service.recordModelCallFinal(stream, "succeeded")
	return true, nil
}

func subagentEmptyStopRecoveryText() string {
	return "During this subagent turn, a prior provider pass stopped after tool results without visible assistant output. Continue from the latest tool result and return a concise investigation result for the parent. Only call another allowed read-only tool if necessary."
}

func currentTurnHasToolResult(conversation *ConversationFile, turnSeq int64) bool {
	if conversation == nil || turnSeq <= 0 {
		return false
	}
	for _, entry := range conversation.Entries {
		if entry.TurnSeq == turnSeq && strings.TrimSpace(entry.Kind) == "tool_result" {
			return true
		}
	}
	return false
}

func currentTurnHasPromptContextSource(conversation *ConversationFile, turnSeq int64, source string) bool {
	if conversation == nil || turnSeq <= 0 || strings.TrimSpace(source) == "" {
		return false
	}
	for _, entry := range conversation.Entries {
		if entry.TurnSeq != turnSeq || strings.TrimSpace(entry.Kind) != "prompt_context" {
			continue
		}
		var payload promptContextEntryPayload
		if err := json.Unmarshal(entry.Payload, &payload); err != nil {
			continue
		}
		if strings.TrimSpace(payload.Source) == strings.TrimSpace(source) {
			return true
		}
	}
	return false
}

func hasPendingAwaitingUserInteraction(stream *ActiveStream) bool {
	if stream == nil {
		return false
	}
	stream.mu.Lock()
	defer stream.mu.Unlock()
	for _, pending := range stream.PendingInteractions {
		if !shouldAutoResumeAfterInteraction(pending) {
			return true
		}
	}
	return false
}

func providerTimerKey(kind streamTimerKind, execID string) string {
	if strings.TrimSpace(execID) == "" {
		return string(kind)
	}
	return string(kind) + ":" + strings.TrimSpace(execID)
}

func (service *Service) scheduleStreamTimer(stream *ActiveStream, key string, delay time.Duration, kind streamTimerKind, execID string, messageID uint32, reason string) {
	if stream == nil || strings.TrimSpace(key) == "" {
		return
	}
	stream.mu.Lock()
	if stream.TimerTokens == nil {
		stream.TimerTokens = make(map[string]uint64)
	}
	stream.TimerTokens[key]++
	token := stream.TimerTokens[key]
	stream.UpdatedAt = time.Now().UTC()
	stream.mu.Unlock()

	go func() {
		if delay > 0 {
			timer := time.NewTimer(delay)
			defer timer.Stop()
			<-timer.C
		}
		if err := service.postStreamCommandAsync(stream, streamCommand{
			Kind: streamCommandTimerFired,
			Timer: &streamTimerEvent{
				Key:       key,
				Kind:      kind,
				Token:     token,
				ExecID:    strings.TrimSpace(execID),
				MessageID: messageID,
				Reason:    strings.TrimSpace(reason),
			},
		}); err != nil && !errors.Is(err, errProviderLoopInterrupted) {
			log.Printf("forwarder timer post failed request_id=%s key=%s err=%v", strings.TrimSpace(stream.RequestID), strings.TrimSpace(key), err)
		}
	}()
}

func timerEventMatches(stream *ActiveStream, payload *streamTimerEvent) bool {
	if stream == nil || payload == nil || strings.TrimSpace(payload.Key) == "" {
		return false
	}
	stream.mu.Lock()
	defer stream.mu.Unlock()
	return stream.TimerTokens[payload.Key] == payload.Token
}

func clearStreamTimer(stream *ActiveStream, key string) {
	if stream == nil || strings.TrimSpace(key) == "" {
		return
	}
	stream.mu.Lock()
	delete(stream.TimerTokens, key)
	stream.UpdatedAt = time.Now().UTC()
	stream.mu.Unlock()
}

func (service *Service) handleTimerEvent(stream *ActiveStream, payload *streamTimerEvent) error {
	if stream == nil || payload == nil {
		return nil
	}
	if !timerEventMatches(stream, payload) {
		return nil
	}
	clearStreamTimer(stream, payload.Key)

	switch payload.Kind {
	case streamTimerProviderResume:
		stream.mu.Lock()
		providerActive := stream.ProviderActive
		action := stream.PendingProviderAction
		status := stream.Status
		stream.mu.Unlock()
		if providerActive || isTerminalStreamStatus(status) || action != providerActionResume || pendingBridgeCount(stream) > 0 {
			return nil
		}
		return service.driveProvider(stream)
	case streamTimerNonStreamingRecovery:
		current, ok := snapshotPendingExec(stream, payload.ExecID)
		if !ok || current.MessageID != payload.MessageID || current.StreamState != "transport_closed" {
			return nil
		}
		return service.recoverNonStreamingExecAfterStreamClose(stream, current)
	case streamTimerShellForeground:
		return service.recoverShellWithoutTerminalIfNeeded(stream, payload.ExecID, payload.MessageID, shellRecoveryReasonForegroundDeadline)
	case streamTimerShellTransportClose:
		current, status, found := snapshotPendingExecWithStatus(stream, payload.ExecID)
		if !found || current.MessageID != payload.MessageID || current.StreamState != "transport_closed" || isTerminalStreamStatus(status) {
			return nil
		}
		return service.recoverShellWithoutTerminal(stream, current, shellRecoveryReasonTransportClosed)
	case streamTimerCheckpointBlobs:
		return service.handleCheckpointBlobTimeout(stream)
	case streamTimerOrphanCancel:
		stream.mu.Lock()
		subscriberCount := len(stream.Subscribers)
		status := stream.Status
		stream.mu.Unlock()
		if subscriberCount > 0 || isTerminalStreamStatus(status) {
			return nil
		}
		return service.handleCancelIntent(InboundIntent{
			Kind:         "cancel",
			RequestID:    stream.RequestID,
			CancelReason: firstNonEmpty(payload.Reason, "[canceled] RunSSE client disconnected"),
		})
	default:
		return nil
	}
}

func (service *Service) scheduleOrphanCancelActor(requestID string, reason string) bool {
	if service == nil || service.broker == nil {
		return false
	}
	stream, ok := service.broker.Get(requestID)
	if !ok || stream == nil {
		return false
	}
	stream.mu.Lock()
	placeholder := strings.TrimSpace(stream.ConversationID) == "" &&
		!stream.ProviderActive &&
		len(stream.PendingExecs) == 0 &&
		len(stream.PendingInteractions) == 0 &&
		len(stream.Backlog) == 0
	terminal := isTerminalStreamStatus(stream.Status)
	stream.mu.Unlock()
	if placeholder || terminal {
		return false
	}
	if err := service.postStreamCommandAsync(stream, streamCommand{
		Kind:   streamCommandMaybeOrphaned,
		Reason: firstNonEmpty(strings.TrimSpace(reason), "[canceled] RunSSE client disconnected"),
	}); err != nil {
		return false
	}
	return true
}

func (service *Service) cancelOtherConversationActors(conversationID string, keepRequestID string, reason string) {
	if service == nil || service.broker == nil || strings.TrimSpace(conversationID) == "" {
		return
	}
	for _, requestID := range service.broker.OtherConversationRequestIDs(conversationID, keepRequestID) {
		stream, ok := service.broker.Get(requestID)
		if !ok || stream == nil {
			continue
		}
		if err := service.postStreamCommandWait(stream, streamCommand{
			Kind: streamCommandCancel,
			Intent: InboundIntent{
				Kind:         "cancel",
				RequestID:    requestID,
				CancelReason: reason,
			},
		}); err != nil && !errors.Is(err, errProviderLoopInterrupted) {
			log.Printf("forwarder cancel superseded stream failed request_id=%s err=%v", strings.TrimSpace(requestID), err)
		}
	}
}

func (service *Service) setTurnPhase(stream *ActiveStream, phase TurnPhase) {
	if stream == nil {
		return
	}
	stream.mu.Lock()
	stream.Phase = phase
	stream.UpdatedAt = time.Now().UTC()
	stream.mu.Unlock()
}

func rememberPendingProviderCompletion(stream *ActiveStream, completion pendingTurnCompletion) {
	if stream == nil {
		return
	}
	stream.mu.Lock()
	copy := mergePendingProviderCompletion(stream.PendingProviderCompletion, completion)
	stream.PendingProviderCompletion = &copy
	stream.UpdatedAt = time.Now().UTC()
	stream.mu.Unlock()
}

func mergePendingProviderCompletion(existing *pendingTurnCompletion, incoming pendingTurnCompletion) pendingTurnCompletion {
	if existing == nil {
		if incoming.Disposition == completionDispositionNone {
			incoming.Disposition = completionDispositionCompleteAfterExternal
		}
		return incoming
	}
	merged := *existing
	if merged.ConversationID == "" && incoming.ConversationID != "" {
		merged.ConversationID = incoming.ConversationID
	}
	if merged.RequestID == "" && incoming.RequestID != "" {
		merged.RequestID = incoming.RequestID
	}
	if merged.TurnSeq <= 0 && incoming.TurnSeq > 0 {
		merged.TurnSeq = incoming.TurnSeq
	}
	if strings.TrimSpace(merged.ModelCallID) == "" && strings.TrimSpace(incoming.ModelCallID) != "" {
		merged.ModelCallID = incoming.ModelCallID
	}
	if merged.ProviderPass == 0 && incoming.ProviderPass != 0 {
		merged.ProviderPass = incoming.ProviderPass
	}
	if incoming.Usage.hasAny() {
		merged.Usage = incoming.Usage
	}
	merged.Disposition = mergeCompletionDisposition(merged.Disposition, incoming.Disposition)
	return merged
}

func mergeCompletionDisposition(existing pendingCompletionDisposition, incoming pendingCompletionDisposition) pendingCompletionDisposition {
	if existing == completionDispositionCompleteAfterExternal || incoming == completionDispositionCompleteAfterExternal {
		return completionDispositionCompleteAfterExternal
	}
	if existing == completionDispositionResumeAfterExternal || incoming == completionDispositionResumeAfterExternal {
		return completionDispositionResumeAfterExternal
	}
	return completionDispositionCompleteAfterExternal
}

func completionDispositionForExternalResults(finishReason string, forceComplete bool, hadToolInvocation bool) pendingCompletionDisposition {
	if forceComplete {
		return completionDispositionCompleteAfterExternal
	}
	// Some providers may report end_turn even after emitting a valid tool_use block.
	if hadToolInvocation || shouldResumeAfterToolResults(finishReason) {
		return completionDispositionResumeAfterExternal
	}
	return completionDispositionCompleteAfterExternal
}

func clearPendingProviderCompletion(stream *ActiveStream) {
	if stream == nil {
		return
	}
	stream.mu.Lock()
	stream.PendingProviderCompletion = nil
	stream.UpdatedAt = time.Now().UTC()
	stream.mu.Unlock()
}
