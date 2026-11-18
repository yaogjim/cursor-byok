package forwarder

import (
	"strings"
	"testing"

	modeladapter "cursor/internal/backend/agent/model"
)

func TestBuildTerminalStreamErrorQuotaIsActionable(t *testing.T) {
	retryable := false
	err := buildTerminalStreamError(StreamEvent{
		End:                   true,
		TerminalErrorCode:     "provider_error",
		TerminalErrorMessage:  "quota_exhausted status=429",
		TerminalRetryable:     &retryable,
		TerminalHTTPStatus:    "429",
		TerminalErrorCategory: modeladapter.ProviderErrorQuotaExhausted,
		TerminalModelCallID:   "model-call-1",
		TerminalRequestID:     "req-1",
	})
	details := extractRunSSEErrorDetails(t, err)
	custom := details.GetDetails()
	if custom == nil {
		t.Fatal("missing CustomErrorDetails")
	}
	if title := custom.GetTitle(); !strings.Contains(strings.ToLower(title), "quota") {
		t.Fatalf("title should name the quota cause: %q", title)
	}
	detail := custom.GetDetail()
	if !strings.Contains(strings.ToLower(detail), "balance") {
		t.Fatalf("detail should tell the user what to check: %q", detail)
	}
	// 终态文案必须是网关固定文案，不能透传上游原文。
	if strings.Contains(detail, "quota_exhausted status=429") {
		t.Fatalf("detail leaked internal summary: %q", detail)
	}
	if custom.GetIsRetryable() {
		t.Fatal("quota terminal must not be retryable")
	}
	info := custom.GetAdditionalInfo()
	if info["error_category"] != modeladapter.ProviderErrorQuotaExhausted {
		t.Fatalf("additional_info = %#v", info)
	}
}

func TestProviderTerminalFieldsReportQuotaCode(t *testing.T) {
	stats := ProviderStreamStats{
		ErrorCategory:     modeladapter.ProviderErrorQuotaExhausted,
		HTTPStatus:        "429",
		ProviderErrorCode: "insufficient_quota",
		Retryable:         "false",
	}
	fields := providerTerminalFields("model-call-1", stats)
	if got, _ := fields["provider_error_code"].(string); got != "insufficient_quota" {
		t.Fatalf("provider_error_code = %#v", fields["provider_error_code"])
	}
	if got, _ := fields["retryable"].(string); got != "false" {
		t.Fatalf("quota must not be retryable: %#v", fields["retryable"])
	}
}

func TestProviderRetryObservationTreatsQuotaAsNotRetryable(t *testing.T) {
	err := &modeladapter.HTTPStatusError{StatusCode: 429, QuotaExhausted: true}
	retryable, reason, suppression := providerRetryObservation(err, ProviderStreamStats{})
	if retryable != "false" {
		t.Fatalf("quota retryable = %q", retryable)
	}
	if reason != modeladapter.FailureCauseQuotaExhausted || suppression != modeladapter.FailureCauseQuotaExhausted {
		t.Fatalf("quota observation = %q/%q", reason, suppression)
	}
	// 普通 429 仍然可重试，不能被配额判定吞掉。
	plain := &modeladapter.HTTPStatusError{StatusCode: 429}
	if got, _, _ := providerRetryObservation(plain, ProviderStreamStats{}); got != "true" {
		t.Fatalf("plain 429 retryable = %q", got)
	}
}

func TestQuotaErrorReachesTerminalCategoryAndActionableCopy(t *testing.T) {
	// 串联验证：上游 429 quota 错误 → stats 分类 → 终态事件 → 用户可行动文案。
	quotaErr := &modeladapter.HTTPStatusError{
		StatusCode:     429,
		Code:           "insufficient_quota",
		QuotaExhausted: true,
		Body:           "you exceeded your current quota",
	}
	stats := ProviderStreamStats{}
	applyProviderTerminalErrorStats(&stats, quotaErr)
	if stats.ErrorCategory != modeladapter.ProviderErrorQuotaExhausted {
		t.Fatalf("stats category = %q", stats.ErrorCategory)
	}
	if stats.ProviderErrorCode != "insufficient_quota" {
		t.Fatalf("stats provider_error_code = %q", stats.ProviderErrorCode)
	}

	event := StreamEvent{
		End:                   true,
		TerminalErrorCode:     "provider_error",
		TerminalErrorMessage:  safeProviderErrorSummary(stats),
		TerminalHTTPStatus:    stats.HTTPStatus,
		TerminalErrorCategory: stats.ErrorCategory,
		TerminalModelCallID:   "model-call-1",
		TerminalRequestID:     "req-1",
	}
	custom := extractRunSSEErrorDetails(t, buildTerminalStreamError(event)).GetDetails()
	if !strings.Contains(strings.ToLower(custom.GetTitle()), "quota") {
		t.Fatalf("terminal title = %q", custom.GetTitle())
	}
	if strings.Contains(custom.GetDetail(), "exceeded your current quota") {
		t.Fatalf("terminal detail leaked upstream body: %q", custom.GetDetail())
	}
}

func TestStreamDiagnosticFieldKeysKeepProviderErrorCode(t *testing.T) {
	found := false
	for _, key := range streamDiagnosticFieldKeys() {
		if key == "provider_error_code" {
			found = true
		}
	}
	if !found {
		t.Fatal("provider_error_code missing from stream diagnostics allowlist")
	}
}
