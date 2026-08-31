package modeladapter

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"
	"unicode/utf8"
)

type failReader struct {
	err error
}

func (reader failReader) Read([]byte) (int, error) {
	return 0, reader.err
}

func (reader failReader) Close() error {
	return nil
}

func TestBuildHTTPStatusErrorIsTypedAndKeepsLegacyText(t *testing.T) {
	resp := &http.Response{
		StatusCode: http.StatusBadGateway,
		Body:       io.NopCloser(strings.NewReader("upstream unavailable")),
	}
	err := buildHTTPStatusError("openai adapter", resp)
	var httpErr *HTTPStatusError
	if !errors.As(err, &httpErr) {
		t.Fatalf("error is not HTTPStatusError: %v", err)
	}
	if httpErr.Provider != "openai adapter" || httpErr.StatusCode != http.StatusBadGateway {
		t.Fatalf("unexpected typed error: %+v", httpErr)
	}
	if httpErr.Body != "upstream unavailable" || httpErr.BodySummaryType != bodySummaryText {
		t.Fatalf("unexpected body summary: %+v", httpErr)
	}
	if got := err.Error(); got != "openai adapter status=502 body=upstream unavailable" {
		t.Fatalf("Error() = %q", got)
	}
}

func TestBuildHTTPStatusErrorCapturesRetryAttempt(t *testing.T) {
	req, err := http.NewRequest(http.MethodPost, "https://example.test", nil)
	if err != nil {
		t.Fatal(err)
	}
	resp := &http.Response{
		StatusCode: http.StatusTooManyRequests,
		Body:       io.NopCloser(strings.NewReader("rate limited")),
		Request:    req,
	}
	setResponseRetryState(resp, providerRetryState{attempt: 2, waited: time.Second})
	statusErr := buildHTTPStatusError("openai adapter", resp)
	var httpErr *HTTPStatusError
	if !errors.As(statusErr, &httpErr) {
		t.Fatalf("error is not HTTPStatusError: %v", statusErr)
	}
	if httpErr.StatusCode != http.StatusTooManyRequests || httpErr.Attempt != 2 || httpErr.MaxAttempts != providerRequestMaxAttempts {
		t.Fatalf("typed http error = %+v", httpErr)
	}
}

func TestBuildHTTPStatusErrorExtractsJSONAndRedactsSecrets(t *testing.T) {
	resp := &http.Response{
		StatusCode: http.StatusUnauthorized,
		Header:     http.Header{"Retry-After": []string{"2"}},
		Body: io.NopCloser(strings.NewReader(`{
			"error": {
				"message": "Invalid API key provided: sk-secret",
				"type": "invalid_request_error"
			}
		}`)),
	}
	err := buildHTTPStatusError("anthropic adapter", resp)
	var httpErr *HTTPStatusError
	if !errors.As(err, &httpErr) {
		t.Fatalf("error is not HTTPStatusError: %v", err)
	}
	if httpErr.BodySummaryType != bodySummaryJSONError || !httpErr.RetryAfterPresent {
		t.Fatalf("unexpected summary metadata: %+v", httpErr)
	}
	if strings.Contains(httpErr.Body, "sk-secret") {
		t.Fatalf("typed error retained secret: %q", httpErr.Body)
	}
	if !strings.Contains(httpErr.Body, "Invalid API key provided:") {
		t.Fatalf("typed error dropped JSON message: %q", httpErr.Body)
	}
	if !strings.Contains(err.Error(), "anthropic adapter status=401 retry_after=2 body=") {
		t.Fatalf("Error() lost legacy prefix: %q", err.Error())
	}
	if strings.Contains(err.Error(), "sk-secret") {
		t.Fatalf("Error() retained secret: %q", err.Error())
	}
}

func TestBuildHTTPStatusErrorNilResponseAndBodyReadError(t *testing.T) {
	nilErr := buildHTTPStatusError("openai adapter", nil)
	if got := nilErr.Error(); got != "openai adapter response is nil" {
		t.Fatalf("nil response Error() = %q", got)
	}
	var httpErr *HTTPStatusError
	if !errors.As(nilErr, &httpErr) || !httpErr.NilResponse {
		t.Fatalf("nil response is not typed: %v", nilErr)
	}

	readErr := buildHTTPStatusError("openai adapter", &http.Response{
		StatusCode: http.StatusInternalServerError,
		Body:       failReader{err: io.ErrUnexpectedEOF},
	})
	if !errors.As(readErr, &httpErr) {
		t.Fatalf("body read error is not typed: %v", readErr)
	}
	if httpErr.BodySummaryType != bodySummaryBodyReadError || !errors.Is(readErr, io.ErrUnexpectedEOF) {
		t.Fatalf("unexpected body read error: %+v", httpErr)
	}
	if got := readErr.Error(); got != "openai adapter status=500 body_read_error=unexpected EOF" {
		t.Fatalf("body read Error() = %q", got)
	}
}

func TestBuildHTTPStatusErrorEmptyBodyKeepsLegacyText(t *testing.T) {
	err := buildHTTPStatusError("openai adapter", &http.Response{
		StatusCode: http.StatusServiceUnavailable,
		Body:       io.NopCloser(strings.NewReader("   ")),
	})
	if got := err.Error(); got != "openai adapter status=503" {
		t.Fatalf("empty body Error() = %q", got)
	}
}

func TestBuildHTTPStatusErrorTruncatesLongBody(t *testing.T) {
	body := strings.Repeat("x", 600)
	err := buildHTTPStatusError("openai adapter", &http.Response{
		StatusCode: http.StatusBadRequest,
		Body:       io.NopCloser(strings.NewReader(body)),
	})
	var httpErr *HTTPStatusError
	if !errors.As(err, &httpErr) {
		t.Fatalf("error is not HTTPStatusError: %v", err)
	}
	if httpErr.Body == body {
		t.Fatal("long body was not truncated")
	}
	if !strings.HasSuffix(httpErr.Body, "...") {
		t.Fatalf("long body was not truncated: %q", httpErr.Body)
	}
	if utf8.RuneCountInString(httpErr.Body) > 515 {
		t.Fatalf("body summary exceeded limit: %d", utf8.RuneCountInString(httpErr.Body))
	}
}

func TestClassifyHTTPStatusAndProviderError(t *testing.T) {
	if got := ClassifyHTTPStatus(http.StatusTooManyRequests); got != ProviderErrorRateLimited {
		t.Fatalf("429 category = %q", got)
	}
	if got := ClassifyHTTPStatus(http.StatusBadRequest); got != ProviderErrorStatus4xx {
		t.Fatalf("400 category = %q", got)
	}
	if got := ClassifyHTTPStatus(http.StatusNotFound); got != ProviderErrorStatus4xx {
		t.Fatalf("404 category = %q", got)
	}
	if got := ClassifyHTTPStatus(http.StatusInternalServerError); got != ProviderErrorServer5xx {
		t.Fatalf("500 category = %q", got)
	}
	if got := ClassifyHTTPStatus(HTTPStatusCloudflareTimeout); got != ProviderErrorServer5xx {
		t.Fatalf("524 category = %q", got)
	}
	if got := ClassifyHTTPStatus(529); got != ProviderErrorServer5xx {
		t.Fatalf("529 category = %q", got)
	}
	if got := ClassifyHTTPStatus(http.StatusOK); got != "" {
		t.Fatalf("200 category = %q", got)
	}

	if got := ClassifyProviderError(&HTTPStatusError{StatusCode: http.StatusTooManyRequests}); got != ProviderErrorRateLimited {
		t.Fatalf("typed 429 category = %q", got)
	}
	if got := ClassifyProviderError(context.Canceled); got != ProviderErrorContextCanceled {
		t.Fatalf("canceled category = %q", got)
	}
	if got := ClassifyProviderError(fmt.Errorf("round trip: %w", context.Canceled)); got != ProviderErrorContextCanceled {
		t.Fatalf("wrapped canceled category = %q", got)
	}
	if got := ClassifyProviderError(errors.New("provider stream idle timeout after 30s without effective content")); got != ProviderErrorStreamIdleTimeout {
		t.Fatalf("idle timeout category = %q", got)
	}
	if got := ClassifyProviderError(errors.New(`Post "https://example.test/v1/messages": connection refused`)); got != ProviderErrorTransport {
		t.Fatalf("transport category = %q", got)
	}
	if got := ClassifyProviderError(nil); got != "" {
		t.Fatalf("nil category = %q", got)
	}
	if got := ClassifyProviderError(newStreamTruncatedError("openai", nil)); got != ProviderErrorStreamDecode {
		t.Fatalf("truncated category = %q", got)
	}
	if got := ClassifyProviderError(fmt.Errorf("wrap: %w", newStreamTruncatedError("anthropic", io.ErrUnexpectedEOF))); got != ProviderErrorStreamDecode {
		t.Fatalf("wrapped truncated category = %q", got)
	}
	if got := ClassifyProviderError(&CapacityUnavailableError{}); got != ProviderErrorCapacityUnavailable {
		t.Fatalf("capacity category = %q", got)
	}
	if got := ClassifyProviderError(fmt.Errorf("wrap: %w", &CapacityUnavailableError{})); got != ProviderErrorCapacityUnavailable {
		t.Fatalf("wrapped capacity category = %q", got)
	}
	if got := ClassifyProviderError(&RequestBuildError{Err: errors.New("json marshal failed")}); got != ProviderErrorRequestBuild {
		t.Fatalf("request build category = %q", got)
	}
	if got := ClassifyProviderError(&RequestBuildError{Actual: 9, Limit: 8}); got != ProviderErrorRequestBuild {
		t.Fatalf("encoded body limit category = %q", got)
	}
	if ProviderErrorStreamDecode != "stream_decode" {
		t.Fatalf("reserved stream_decode category changed: %q", ProviderErrorStreamDecode)
	}
}

func TestCheckEncodedRequestBodyLimitRejectsOversizeWithoutBody(t *testing.T) {
	if err := checkEncodedRequestBodyBytes(8, 8); err != nil {
		t.Fatalf("at limit: %v", err)
	}
	if err := checkEncodedRequestBodyLimit(make([]byte, maxEncodedRequestBodyBytes)); err != nil {
		t.Fatalf("package limit accepted: %v", err)
	}
	err := checkEncodedRequestBodyBytes(9, 8)
	var buildErr *RequestBuildError
	if !errors.As(err, &buildErr) || buildErr.Actual != 9 || buildErr.Limit != 8 {
		t.Fatalf("oversize err = %#v", err)
	}
	got := err.Error()
	if got != "request build error: encoded body bytes=9 limit=8" {
		t.Fatalf("Error() = %q", got)
	}
	if strings.Contains(got, "{") || strings.Contains(got, "pad") {
		t.Fatalf("Error() leaked body: %q", got)
	}
}

func assertRequestBuildLimitError(t *testing.T, err error, hits int) {
	t.Helper()
	if hits != 0 {
		t.Fatalf("http attempts = %d, want 0", hits)
	}
	var buildErr *RequestBuildError
	if !errors.As(err, &buildErr) || buildErr.Limit != maxEncodedRequestBodyBytes || buildErr.Actual <= buildErr.Limit {
		t.Fatalf("err = %v, want RequestBuildError over encoded body limit", err)
	}
	if ClassifyProviderError(err) != ProviderErrorRequestBuild {
		t.Fatalf("category = %q, want %q", ClassifyProviderError(err), ProviderErrorRequestBuild)
	}
	if isFallbackEligibleError(err) {
		t.Fatal("encoded body limit must not be fallback eligible")
	}
	if IsRetryableZeroEventStreamError(err) {
		t.Fatal("encoded body limit must not recover or retry")
	}
	got := err.Error()
	want := fmt.Sprintf("request build error: encoded body bytes=%d limit=%d", buildErr.Actual, buildErr.Limit)
	if got != want {
		t.Fatalf("Error() = %q, want %q", got, want)
	}
	safety, ok := fallbackSafetyFromError(err)
	if !ok {
		return
	}
	if !safety.RequestBuildFailed {
		t.Fatal("RequestBuildFailed must be marked for pre-HTTP build failures")
	}
	if safety.HTTPAttempts != 0 {
		t.Fatalf("safety HTTPAttempts = %d, want 0", safety.HTTPAttempts)
	}
	if got := fallbackSuppressionReason(err); got != "request_build" {
		t.Fatalf("suppression = %q, want request_build", got)
	}
}

func TestWrapRequestBuildFailureMarksRequestBuildNotNoHTTP(t *testing.T) {
	safety := &FallbackSafetyInfo{}
	err := wrapRequestBuildFailure(StreamRequest{FallbackSafety: safety}, &RequestBuildError{Actual: 9, Limit: 8})
	if !safety.Snapshot().RequestBuildFailed {
		t.Fatal("MarkRequestBuildFailed was not called")
	}
	if got := fallbackSuppressionReason(err); got != "request_build" {
		t.Fatalf("marked suppression = %q, want request_build", got)
	}
	unmarked := WrapFallbackSafetyError(&RequestBuildError{Actual: 9, Limit: 8}, &FallbackSafetyInfo{})
	if got := fallbackSuppressionReason(unmarked); got != "no_http_attempt" {
		t.Fatalf("unmarked suppression = %q, want no_http_attempt", got)
	}
}
