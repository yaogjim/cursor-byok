// http_error.go 负责把非 2xx HTTP 响应整理成带响应体摘要的错误。
package modeladapter

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"

	"cursor/internal/audit"
)

const (
	// maxErrorBodyBytes 表示错误响应体最多读取的字节数。
	maxErrorBodyBytes = 8192

	ProviderErrorStatus4xx         = "status_4xx"
	ProviderErrorRateLimited       = "rate_limited"
	ProviderErrorServer5xx         = "server_5xx"
	ProviderErrorTransport         = "transport"
	ProviderErrorContextCanceled   = "context_canceled"
	ProviderErrorStreamDecode      = "stream_decode"
	ProviderErrorStreamIdleTimeout = "stream_idle_timeout"

	bodySummaryEmpty         = "empty"
	bodySummaryJSONError     = "json_error"
	bodySummaryText          = "text"
	bodySummaryBodyReadError = "body_read_error"
	bodySummaryNilResponse   = "nil_response"
)

// HTTPStatusError 是可 errors.As 的 provider HTTP 状态错误。
type HTTPStatusError struct {
	Provider          string
	StatusCode        int
	Body              string
	BodyReadError     error
	BodySummaryType   string
	RetrySummary      string
	RetryAfterPresent bool
	NilResponse       bool
}

func (err *HTTPStatusError) Error() string {
	if err == nil {
		return "http status error"
	}
	prefix := strings.TrimSpace(err.Provider)
	if prefix == "" {
		prefix = "provider"
	}
	if err.NilResponse {
		return fmt.Sprintf("%s response is nil", prefix)
	}
	retrySummary := strings.TrimSpace(err.RetrySummary)
	switch {
	case err.BodyReadError != nil && retrySummary != "":
		return fmt.Sprintf("%s status=%d %s body_read_error=%v", prefix, err.StatusCode, retrySummary, err.BodyReadError)
	case err.BodyReadError != nil:
		return fmt.Sprintf("%s status=%d body_read_error=%v", prefix, err.StatusCode, err.BodyReadError)
	case err.Body == "" && retrySummary != "":
		return fmt.Sprintf("%s status=%d %s", prefix, err.StatusCode, retrySummary)
	case err.Body == "":
		return fmt.Sprintf("%s status=%d", prefix, err.StatusCode)
	case retrySummary != "":
		return fmt.Sprintf("%s status=%d %s body=%s", prefix, err.StatusCode, retrySummary, err.Body)
	default:
		return fmt.Sprintf("%s status=%d body=%s", prefix, err.StatusCode, err.Body)
	}
}

func (err *HTTPStatusError) Unwrap() error {
	if err == nil {
		return nil
	}
	return err.BodyReadError
}

func (err *HTTPStatusError) Category() string {
	if err == nil {
		return ""
	}
	return ClassifyHTTPStatus(err.StatusCode)
}

// StreamTruncatedError 表示 SSE 在没有明确 completion marker 时结束，或底层流读取失败。
type StreamTruncatedError struct {
	Provider string
	Err      error
}

func (err *StreamTruncatedError) Error() string {
	if err == nil {
		return "stream truncated"
	}
	provider := strings.TrimSpace(err.Provider)
	if provider == "" {
		provider = "provider"
	}
	if err.Err == nil {
		return provider + " stream truncated: missing completion marker"
	}
	return fmt.Sprintf("%s stream truncated: %v", provider, err.Err)
}

func (err *StreamTruncatedError) Unwrap() error {
	if err == nil {
		return nil
	}
	return err.Err
}

func (err *StreamTruncatedError) Category() string {
	return ProviderErrorStreamDecode
}

func newStreamTruncatedError(provider string, cause error) error {
	return &StreamTruncatedError{Provider: strings.TrimSpace(provider), Err: cause}
}

// ClassifyHTTPStatus 按 HTTP 状态码给出稳定错误分类。
func ClassifyHTTPStatus(statusCode int) string {
	switch {
	case statusCode == http.StatusTooManyRequests:
		return ProviderErrorRateLimited
	case statusCode >= http.StatusInternalServerError:
		return ProviderErrorServer5xx
	case statusCode >= http.StatusBadRequest:
		return ProviderErrorStatus4xx
	default:
		return ""
	}
}

// ClassifyProviderError 从 typed HTTP 错误或请求层错误推断分类。
func ClassifyProviderError(err error) string {
	var httpErr *HTTPStatusError
	if errors.As(err, &httpErr) && httpErr != nil {
		return httpErr.Category()
	}
	if err == nil {
		return ""
	}
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return ProviderErrorContextCanceled
	}
	if isProviderStreamIdleTimeout(err) {
		return ProviderErrorStreamIdleTimeout
	}
	var truncated *StreamTruncatedError
	if errors.As(err, &truncated) && truncated != nil {
		return ProviderErrorStreamDecode
	}
	return ProviderErrorTransport
}

func isProviderStreamIdleTimeout(err error) bool {
	if err == nil {
		return false
	}
	return strings.Contains(err.Error(), "provider stream idle timeout")
}

func retryAfterPresent(resp *http.Response) bool {
	return resp != nil && strings.TrimSpace(resp.Header.Get("Retry-After")) != ""
}

// buildHTTPStatusError 读取响应体摘要并生成带状态码的错误。
func buildHTTPStatusError(prefix string, resp *http.Response) error {
	provider := strings.TrimSpace(prefix)
	if resp == nil {
		return &HTTPStatusError{
			Provider:        provider,
			BodySummaryType: bodySummaryNilResponse,
			NilResponse:     true,
		}
	}

	statusError := &HTTPStatusError{
		Provider:          provider,
		StatusCode:        resp.StatusCode,
		RetrySummary:      ProviderRetryAttemptSummary(resp),
		RetryAfterPresent: retryAfterPresent(resp),
	}
	limitedBody, err := io.ReadAll(io.LimitReader(resp.Body, maxErrorBodyBytes))
	if err != nil {
		statusError.BodyReadError = err
		statusError.BodySummaryType = bodySummaryBodyReadError
		return statusError
	}
	statusError.Body, statusError.BodySummaryType = summarizeProviderErrorBody(string(limitedBody))
	return statusError
}

func summarizeProviderErrorBody(raw string) (string, string) {
	bodyText := strings.TrimSpace(raw)
	if bodyText == "" {
		return "", bodySummaryEmpty
	}
	if message := extractJSONErrorMessage(bodyText); message != "" {
		return limitSanitizedErrorText(message), bodySummaryJSONError
	}
	return limitSanitizedErrorText(bodyText), bodySummaryText
}

func extractJSONErrorMessage(raw string) string {
	var payload any
	if err := json.Unmarshal([]byte(raw), &payload); err != nil {
		return ""
	}
	return extractJSONErrorMessageValue(payload, 0)
}

func extractJSONErrorMessageValue(value any, depth int) string {
	if depth > 8 || value == nil {
		return ""
	}
	switch typed := value.(type) {
	case string:
		return strings.TrimSpace(typed)
	case map[string]any:
		if message := extractJSONErrorMessageValue(typed["error"], depth+1); message != "" {
			return message
		}
		for _, key := range []string{"message", "msg"} {
			message, _ := typed[key].(string)
			if trimmed := strings.TrimSpace(message); trimmed != "" {
				return trimmed
			}
		}
	}
	return ""
}

func limitSanitizedErrorText(value string) string {
	return audit.SanitizeMetadataText(value)
}
