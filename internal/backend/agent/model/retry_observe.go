package modeladapter

import (
	"context"
	"regexp"
	"strings"

	"cursor/internal/audit"
	"cursor/internal/observability"
)

var urlInMetadataPattern = regexp.MustCompile(`(?i)https?://[^\s"'<>]+`)

func recordProviderAttempt(ctx context.Context, observer *audit.Observer, requestID string, modelCallID string, event audit.Event) {
	if observer != nil {
		observer.Record(event)
	}
	recordProviderObservability(ctx, requestID, modelCallID, event)
}

func recordProviderObservability(ctx context.Context, requestID string, modelCallID string, auditEvent audit.Event) {
	sink := observability.ProcessSink()
	if sink == nil {
		return
	}
	if ctx == nil {
		ctx = context.Background()
	}
	requestID = strings.TrimSpace(requestID)
	modelCallID = strings.TrimSpace(modelCallID)
	correlation := observability.CorrelationFromContext(ctx)
	if correlation.CursorRequestID == "" {
		correlation.CursorRequestID = requestID
	}
	if correlation.ModelCallID == "" {
		correlation.ModelCallID = modelCallID
	}
	ctx = observability.WithCorrelation(ctx, correlation)
	event := providerObservabilityEvent(auditEvent, requestID, modelCallID)
	defer func() { _ = recover() }()
	_ = sink.Record(ctx, observability.Capture{Event: event})
	if strings.TrimSpace(auditEvent.RetryDecision) == "" {
		return
	}
	decision := event
	decision.Event = "retry_decision"
	decision.Operation = "provider.retry"
	_ = sink.Record(ctx, observability.Capture{Event: decision})
}

func providerObservabilityEvent(auditEvent audit.Event, requestID string, modelCallID string) observability.Event {
	status, outcome := providerObservabilityOutcome(auditEvent)
	eventName := strings.TrimSpace(auditEvent.Kind)
	operation := "provider.request"
	if eventName == "provider_response" {
		operation = "provider.response"
	}
	return observability.Event{
		CursorRequestID:     requestID,
		ModelCallID:         modelCallID,
		Layer:               "provider",
		Event:               eventName,
		Capability:          "provider",
		Operation:           operation,
		Direction:           observability.DirectionProxyToProvider,
		ExecutionTarget:     "provider",
		Protocol:            "http_stream",
		Status:              status,
		SemanticOutcome:     outcome,
		ImplementationState: observability.ImplementationImplemented,
		ErrorCategory:       strings.TrimSpace(auditEvent.ErrorCategory),
		DurationMS:          auditEvent.DurationMS,
		RequestBytes:        int64(auditEvent.RequestBytes),
		ResponseBytes:       auditEvent.ResponseBytes,
		Fields:              providerObservabilityFields(auditEvent),
	}
}

func providerObservabilityOutcome(auditEvent audit.Event) (string, string) {
	decision := strings.TrimSpace(auditEvent.RetryDecision)
	switch decision {
	case retryDecisionSuccess, retryDecisionSuccessAfterRetry:
		return "completed", observability.OutcomeSucceeded
	case retryDecisionRetry, retryDecisionStreamPreEventEOF:
		return "retrying", observability.OutcomeDegraded
	case retryDecisionExhausted, retryDecisionNoRetryStatus, retryDecisionNoRetryBuild, retryDecisionNoRetryWaitBudget, retryDecisionNoRetryStreamError, retryDecisionNoRetryStreamEvent, retryDecisionNoRetryStreamExhausted, retryDecisionNoRetryStreamRawBytes:
		return "error", observability.OutcomeFailed
	case retryDecisionNoRetryContext, retryDecisionNoRetryStreamContext:
		return "canceled", observability.OutcomeCanceled
	}
	if strings.TrimSpace(auditEvent.ErrorCategory) != "" {
		return "error", observability.OutcomeFailed
	}
	if auditEvent.Kind == "provider_request" {
		return "started", observability.OutcomeStarted
	}
	if auditEvent.Status != 0 && (auditEvent.Status < 200 || auditEvent.Status >= 300) {
		return "error", observability.OutcomeFailed
	}
	return "completed", observability.OutcomeSucceeded
}

func providerObservabilityFields(auditEvent audit.Event) map[string]any {
	fields := make(map[string]any, 8)
	if provider := strings.TrimSpace(auditEvent.Provider); provider != "" {
		fields["provider"] = provider
	}
	if endpoint := strings.TrimSpace(auditEvent.Endpoint); endpoint != "" {
		fields["endpoint"] = endpoint
	}
	if host := strings.TrimSpace(auditEvent.TargetHost); host != "" {
		fields["target_host"] = host
	}
	if auditEvent.Attempt > 0 {
		fields["attempt"] = auditEvent.Attempt
	}
	if auditEvent.MaxAttempts > 0 {
		fields["max_attempts"] = auditEvent.MaxAttempts
	}
	if decision := strings.TrimSpace(auditEvent.RetryDecision); decision != "" {
		fields["retry_decision"] = decision
	}
	if auditEvent.Status > 0 {
		fields["status_code"] = auditEvent.Status
	}
	if auditEvent.RetryAfterPresent {
		fields["retry_after_present"] = true
	}
	if auditEvent.CanaryMatched {
		fields["canary_matched"] = true
	}
	if message := strings.TrimSpace(auditEvent.ErrorMessage); message != "" {
		fields["error_message"] = observabilityErrorMessage(message)
	}
	appendFailureObservabilityFields(fields, auditEvent)
	if len(fields) == 0 {
		return nil
	}
	return fields
}

func appendFailureObservabilityFields(fields map[string]any, auditEvent audit.Event) {
	decision := strings.TrimSpace(auditEvent.RetryDecision)
	if auditEvent.Status <= 0 && strings.TrimSpace(auditEvent.ErrorCategory) == "" && decision == "" {
		return
	}
	failure := ProviderFailure{Category: strings.TrimSpace(auditEvent.ErrorCategory), Status: auditEvent.Status}
	if auditEvent.Status > 0 {
		failure = classifyHTTPStatusFailure(auditEvent.Status)
	} else {
		failure.Cause = failureCauseFromCategory(failure.Category, auditEvent.ErrorMessage)
		failure.Phase = failurePhaseFromCategory(failure.Category)
	}
	if failure.Category != "" {
		fields["failure_category"] = failure.Category
	}
	if failure.Cause != "" {
		fields["failure_cause"] = failure.Cause
	}
	if failure.Phase != "" {
		fields["failure_phase"] = failure.Phase
	}
	if failure.Status > 0 {
		fields["failure_status"] = failure.Status
	}
	if decision != "" {
		fields["recovery_action"] = recoveryActionFromDecision(decision)
	}
}

func failureCauseFromCategory(category, message string) string {
	lower := strings.ToLower(message)
	switch {
	case strings.Contains(lower, "certificate") || strings.Contains(lower, "unknown authority") || strings.Contains(lower, "x509"):
		return FailureCauseTLSVerify
	case strings.Contains(lower, "tls") && strings.Contains(lower, "handshake") && (strings.Contains(lower, "eof") || strings.Contains(lower, "unexpected")):
		return FailureCauseTLSHandshakeEOF
	case strings.Contains(lower, "no such host") || strings.Contains(lower, "nxdomain"):
		return FailureCauseDNSPermanent
	case strings.Contains(lower, LivenessPhaseConnect+" timeout"):
		return FailureCauseConnectTimeout
	case strings.Contains(lower, LivenessPhaseFirstEvent+" timeout"):
		return FailureCauseFirstEventTimeout
	}
	switch category {
	case ProviderErrorRequestBuild:
		return FailureCauseRequestBuild
	case ProviderErrorContextCanceled:
		return FailureCauseContextCanceled
	case ProviderErrorStreamIdleTimeout:
		return FailureCauseStreamIdleTimeout
	case ProviderErrorTerminal:
		return FailureCauseProviderTerminal
	case ProviderErrorStreamDecode:
		return FailureCauseProtocolDecode
	case ProviderErrorTransport:
		return FailureCauseTransport
	default:
		return category
	}
}

func failurePhaseFromCategory(category string) string {
	switch category {
	case ProviderErrorStreamIdleTimeout:
		return LivenessPhaseIdle
	case ProviderErrorStreamDecode, ProviderErrorTerminal:
		return LivenessPhaseStream
	case ProviderErrorContextCanceled:
		return LivenessPhaseCall
	case ProviderErrorRequestBuild, ProviderErrorRateLimited, ProviderErrorServer5xx, ProviderErrorStatus4xx:
		return LivenessPhaseHTTP
	default:
		return LivenessPhaseTransport
	}
}

func recoveryActionFromDecision(decision string) string {
	switch decision {
	case retryDecisionRetry, retryDecisionStreamPreEventEOF:
		return RecoveryActionRetrySame
	case retryDecisionSuccess, retryDecisionSuccessAfterRetry:
		return RecoveryActionSuccess
	case retryDecisionNoRetryWaitBudget:
		return RecoveryActionSwitch
	default:
		return RecoveryActionFailFast
	}
}

func observabilityErrorMessage(message string) string {
	message = audit.SanitizeMetadataText(message)
	return urlInMetadataPattern.ReplaceAllStringFunc(message, func(raw string) string {
		host := audit.HostFromURL(raw)
		if host == "" {
			return "http"
		}
		return host
	})
}
