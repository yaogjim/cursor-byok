package forwarder

import (
	"errors"
	"strings"
	"testing"

	"connectrpc.com/connect"

	"cursor/gen/aiserverv1"
	modeladapter "cursor/internal/backend/agent/model"
)

func TestBuildTerminalStreamErrorTyped524KeepsStatusAndIsNotRetryable(t *testing.T) {
	retryable := false
	err := buildTerminalStreamError(StreamEvent{
		End:                   true,
		TerminalErrorCode:     "provider_error",
		TerminalErrorMessage:  "server_5xx status=524",
		TerminalRetryable:     &retryable,
		TerminalHTTPStatus:    "524",
		TerminalErrorCategory: modeladapter.ProviderErrorServer5xx,
		TerminalModelCallID:   "model-call-1",
		TerminalRequestID:     "req-1",
	})
	details := extractRunSSEErrorDetails(t, err)
	custom := details.GetDetails()
	if custom == nil {
		t.Fatal("missing CustomErrorDetails")
	}
	if custom.GetDetail() != "server_5xx status=524" {
		t.Fatalf("detail = %q", custom.GetDetail())
	}
	if custom.GetIsRetryable() {
		t.Fatal("RunSSE IsRetryable must be false after terminal; gateway will not send more HTTP")
	}
	if !custom.GetShowRequestId() {
		t.Fatal("ShowRequestId should stay enabled for real request correlation")
	}
	info := custom.GetAdditionalInfo()
	if info["http_status"] != "524" || info["error_category"] != modeladapter.ProviderErrorServer5xx {
		t.Fatalf("additional_info = %#v", info)
	}
	if info["model_call_id"] != "model-call-1" || info["request_id"] != "req-1" {
		t.Fatalf("correlation additional_info = %#v", info)
	}
	if strings.Contains(custom.GetDetail(), "sk-") || strings.Contains(custom.GetDetail(), "Authorization") {
		t.Fatalf("terminal leaked secret: %q", custom.GetDetail())
	}
}

func TestBuildTerminalStreamErrorTransportOmitsInventedHTTPStatus(t *testing.T) {
	err := buildTerminalStreamError(StreamEvent{
		End:                   true,
		TerminalErrorCode:     "provider_error",
		TerminalErrorMessage:  "transport status=not_recorded",
		TerminalHTTPStatus:    "not_recorded",
		TerminalErrorCategory: modeladapter.ProviderErrorTransport,
	})
	details := extractRunSSEErrorDetails(t, err)
	custom := details.GetDetails()
	if custom.GetDetail() != "transport status=not_recorded" {
		t.Fatalf("detail = %q", custom.GetDetail())
	}
	if custom.GetIsRetryable() {
		t.Fatal("provider terminal IsRetryable must be false")
	}
	info := custom.GetAdditionalInfo()
	if _, ok := info["http_status"]; ok {
		t.Fatalf("transport terminal must not invent http_status: %#v", info)
	}
	if info["error_category"] != modeladapter.ProviderErrorTransport {
		t.Fatalf("additional_info = %#v", info)
	}
}

func TestBuildTerminalStreamErrorIsRetryableMatchesTerminalDecision(t *testing.T) {
	stillRetrying := true
	err := buildTerminalStreamError(StreamEvent{
		End:                  true,
		TerminalErrorCode:    "provider_error",
		TerminalErrorMessage: "server_5xx status=524",
		TerminalRetryable:    &stillRetrying,
	})
	details := extractRunSSEErrorDetails(t, err)
	if !details.GetDetails().GetIsRetryable() {
		t.Fatal("explicit TerminalRetryable=true must pass through")
	}

	err = buildTerminalStreamError(StreamEvent{
		End:                  true,
		TerminalErrorCode:    "provider_error",
		TerminalErrorMessage: "server_5xx status=524",
	})
	details = extractRunSSEErrorDetails(t, err)
	if details.GetDetails().GetIsRetryable() {
		t.Fatal("provider_error without explicit retryable must default false, not hardcoded true")
	}
}

func TestFailStreamSanitizesTypedHTTPStatusError(t *testing.T) {
	service, stream, _ := testCheckpointBlobProjection(t)
	stream.mu.Lock()
	stream.ProviderStreamStats = ProviderStreamStats{Attempt: 1, HTTPStatus: "not_recorded"}
	stream.mu.Unlock()
	secret := "sk-secret and full prompt"
	if err := service.failStream(stream, "provider_error", providerTerminalError{cause: &modeladapter.HTTPStatusError{
		StatusCode: modeladapter.HTTPStatusCloudflareTimeout,
		Body:       secret,
	}}); err != nil {
		t.Fatalf("failStream() error = %v", err)
	}
	acknowledgeCheckpointBlobs(t, service, stream)
	events := readCheckpointTestEvents(t, service, stream)
	var terminal StreamEvent
	found := false
	for _, event := range events {
		if event.End {
			terminal = event
			found = true
		}
	}
	if !found {
		t.Fatal("missing failed terminal event")
	}
	if terminal.TerminalErrorMessage != "server_5xx status=524" {
		t.Fatalf("terminal message = %q", terminal.TerminalErrorMessage)
	}
	if strings.Contains(terminal.TerminalErrorMessage, secret) {
		t.Fatalf("terminal leaked body: %q", terminal.TerminalErrorMessage)
	}
	if terminal.TerminalHTTPStatus != "524" {
		t.Fatalf("terminal http status = %q, want 524", terminal.TerminalHTTPStatus)
	}
	if terminal.TerminalRetryable == nil || *terminal.TerminalRetryable {
		t.Fatalf("terminal retryable = %v, want false", terminal.TerminalRetryable)
	}
	runSSEErr := buildTerminalStreamError(terminal)
	details := extractRunSSEErrorDetails(t, runSSEErr)
	if details.GetDetails().GetIsRetryable() {
		t.Fatal("RunSSE IsRetryable must match terminal false")
	}
	if details.GetDetails().GetAdditionalInfo()["http_status"] != "524" {
		t.Fatalf("RunSSE additional_info = %#v", details.GetDetails().GetAdditionalInfo())
	}
}

func extractRunSSEErrorDetails(t *testing.T, err error) *aiserverv1.ErrorDetails {
	t.Helper()
	var connectErr *connect.Error
	if !errors.As(err, &connectErr) {
		t.Fatalf("error type %T (%v), want connect.Error", err, err)
	}
	for _, detail := range connectErr.Details() {
		value, valueErr := detail.Value()
		if valueErr != nil {
			continue
		}
		details, ok := value.(*aiserverv1.ErrorDetails)
		if ok && details != nil {
			return details
		}
	}
	t.Fatal("missing ErrorDetails on connect error")
	return nil
}
