package modeladapter

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"
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
	if ProviderErrorStreamDecode != "stream_decode" {
		t.Fatalf("reserved stream_decode category changed: %q", ProviderErrorStreamDecode)
	}
}
