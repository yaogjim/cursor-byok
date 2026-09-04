// http_error.go 负责把非 2xx HTTP 响应整理成带响应体摘要的错误。
package modeladapter

import (
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

	// maxEncodedRequestBodyBytes 是最终 JSON 编码后、创建 HTTP 请求前的实际请求体字节上限。
	// 配置层没有对应项；取与现有 canary 检查同量级的保守包级上限，避免无界编码体进入发送路径。
	maxEncodedRequestBodyBytes = 32 << 20

	ProviderErrorStatus4xx         = "status_4xx"
	ProviderErrorRateLimited       = "rate_limited"
	ProviderErrorServer5xx         = "server_5xx"
	ProviderErrorTransport         = "transport"
	ProviderErrorContextCanceled   = "context_canceled"
	ProviderErrorStreamDecode      = "stream_decode"
	ProviderErrorStreamIdleTimeout = "stream_idle_timeout"
	ProviderErrorTerminal          = "provider_terminal"
	// ProviderErrorRequestBuild 表示请求序列化/构建阶段失败，此类错误禁止跨渠道 fallback。
	ProviderErrorRequestBuild = "request_build"
	// ProviderErrorCapacityUnavailable 表示物理上游组在固定短超时内没有空闲槽。
	// 零 HTTP / 零字节 / 零 model event 时可切到不同上游组；父 context 取消不得使用此分类。
	ProviderErrorCapacityUnavailable = "capacity_unavailable"

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
	Attempt           int
	MaxAttempts       int
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
// RawBytesObserved=true 表示在截断之前 provider 已经返回过至少一个原始字节；
// 此时 fallback 路由器禁止切换到其他渠道，即使尚未产生任何 model event。
type StreamTruncatedError struct {
	Provider         string
	Err              error
	RawBytesObserved bool
}

// ProviderTerminalStatusError 表示上游通过协议事件明确报告 failed/cancelled/incomplete。
// 它不是 HTTP transport 错误，也不得触发 EOF 恢复。
type ProviderTerminalStatusError struct {
	Provider string
	Status   string
	Message  string
}

func (err *ProviderTerminalStatusError) Error() string {
	if err == nil {
		return "provider terminal status"
	}
	provider := firstNonEmptyString(strings.TrimSpace(err.Provider), "provider")
	status := firstNonEmptyString(strings.TrimSpace(err.Status), "failed")
	if message := strings.TrimSpace(err.Message); message != "" {
		return fmt.Sprintf("%s terminal status=%s: %s", provider, status, limitSanitizedErrorText(message))
	}
	return fmt.Sprintf("%s terminal status=%s", provider, status)
}

func (err *ProviderTerminalStatusError) Category() string {
	return ProviderErrorTerminal
}

// CapacityUnavailableError 表示在固定 2 秒等待内无法获得上游组并发槽。
// 错误文本不得包含 API key、组哈希或 URL。
type CapacityUnavailableError struct{}

func (err *CapacityUnavailableError) Error() string {
	return "upstream capacity unavailable"
}

func (err *CapacityUnavailableError) Category() string {
	return ProviderErrorCapacityUnavailable
}

func isCapacityUnavailable(err error) bool {
	var capErr *CapacityUnavailableError
	return errors.As(err, &capErr)
}

// RequestBuildError 表示在发送 HTTP 请求之前的序列化/构建阶段出错。
// 包括 JSON marshal、编码体超限、extra params 解析、自定义 header 构建失败等。
// 此类错误来自本地逻辑，不可能通过切换 provider 渠道解决，因此禁止 fallback。
type RequestBuildError struct {
	Err    error
	Actual int
	Limit  int
}

// FallbackSafetyError 把一次渠道尝试的 typed 安全快照与原始错误绑定。
// Error/Unwrap 保持原错误分类与对外文本；fallback router 只读取 Safety。
type FallbackSafetyError struct {
	Err    error
	Safety FallbackSafetySnapshot
}

func (e *FallbackSafetyError) Error() string {
	if e == nil || e.Err == nil {
		return "provider fallback safety error"
	}
	return e.Err.Error()
}

func (e *FallbackSafetyError) Unwrap() error {
	if e == nil {
		return nil
	}
	return e.Err
}

func WrapFallbackSafetyError(err error, safety *FallbackSafetyInfo) error {
	if err == nil || safety == nil {
		return err
	}
	var existing *FallbackSafetyError
	if errors.As(err, &existing) {
		return err
	}
	return &FallbackSafetyError{Err: err, Safety: safety.Snapshot()}
}

// wrapRequestBuildFailure 标记发送前的本地构建失败，避免 fallback 诊断把零 HTTP 误报成 no_http_attempt。
func wrapRequestBuildFailure(req StreamRequest, err error) error {
	if err == nil {
		return nil
	}
	if req.FallbackSafety != nil {
		req.FallbackSafety.MarkRequestBuildFailed()
	}
	return WrapFallbackSafetyError(err, req.FallbackSafety)
}

func fallbackSafetyFromError(err error) (FallbackSafetySnapshot, bool) {
	var safetyErr *FallbackSafetyError
	if errors.As(err, &safetyErr) && safetyErr != nil {
		return safetyErr.Safety, true
	}
	return FallbackSafetySnapshot{}, false
}

func (e *RequestBuildError) Error() string {
	if e == nil {
		return "request build error"
	}
	if e.Limit > 0 {
		return fmt.Sprintf("request build error: encoded body bytes=%d limit=%d", e.Actual, e.Limit)
	}
	if e.Err == nil {
		return "request build error"
	}
	return "request build error: " + e.Err.Error()
}

func checkEncodedRequestBodyLimit(payload []byte) error {
	return checkEncodedRequestBodyBytes(len(payload), maxEncodedRequestBodyBytes)
}

func checkEncodedRequestBodyBytes(actual, limit int) error {
	if actual <= limit {
		return nil
	}
	return &RequestBuildError{Actual: actual, Limit: limit}
}

func (e *RequestBuildError) Unwrap() error {
	if e == nil {
		return nil
	}
	return e.Err
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
	var live *LivenessTimeoutError
	if errors.As(err, &live) && live != nil {
		return live.Category()
	}
	if isParentContextError(err) {
		return ProviderErrorContextCanceled
	}
	if isCapacityUnavailable(err) {
		return ProviderErrorCapacityUnavailable
	}
	if isProviderStreamIdleTimeout(err) {
		return ProviderErrorStreamIdleTimeout
	}
	var terminal *ProviderTerminalStatusError
	if errors.As(err, &terminal) && terminal != nil {
		return ProviderErrorTerminal
	}
	var truncated *StreamTruncatedError
	if errors.As(err, &truncated) && truncated != nil {
		return ProviderErrorStreamDecode
	}
	// RequestBuildError 是本地序列化/构建错误，不可通过切换渠道解决，
	// 必须在 StreamTruncatedError 之后检查，防止被误分类为 transport。
	var buildErr *RequestBuildError
	if errors.As(err, &buildErr) && buildErr != nil {
		return ProviderErrorRequestBuild
	}
	return ProviderErrorTransport
}

func isProviderStreamIdleTimeout(err error) bool {
	var idle *StreamIdleTimeoutError
	if errors.As(err, &idle) {
		return true
	}
	var live *LivenessTimeoutError
	if errors.As(err, &live) && live != nil && live.Phase == LivenessPhaseIdle {
		return true
	}
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
		Attempt:           responseRetryState(resp).attempt,
		MaxAttempts:       normalizeProviderRetry(providerRetry{}).maxAttempts,
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
