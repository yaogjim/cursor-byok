package modeladapter

import (
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"
)

func quotaTestResponse(t *testing.T, status int, body string) *http.Response {
	t.Helper()
	return &http.Response{
		StatusCode: status,
		Header:     http.Header{},
		Body:       io.NopCloser(strings.NewReader(body)),
	}
}

func TestHTTPStatusErrorDetectsQuotaExhaustedByCode(t *testing.T) {
	body := `{"error":{"message":"You exceeded your current quota","type":"insufficient_quota","code":"insufficient_quota"}}`
	err := buildHTTPStatusError("openai", quotaTestResponse(t, http.StatusTooManyRequests, body))
	var httpErr *HTTPStatusError
	if !errors.As(err, &httpErr) {
		t.Fatalf("not an HTTPStatusError: %T", err)
	}
	if !httpErr.QuotaExhausted {
		t.Fatalf("quota signal missed: %#v", httpErr)
	}
	if got := httpErr.Category(); got != ProviderErrorQuotaExhausted {
		t.Fatalf("category = %q", got)
	}
	failure := classifyProviderFailure(err, 0)
	if failure.Cause != FailureCauseQuotaExhausted || failure.Category != ProviderErrorQuotaExhausted {
		t.Fatalf("failure = %#v", failure)
	}
	// 同 key 重试无意义；换渠道可能仍有额度，必须仍可切换。
	if failure.Retryable || !failure.Switchable {
		t.Fatalf("quota retry/switch policy = %#v", failure)
	}
}

func TestHTTPStatusErrorDetectsQuotaExhaustedByTypeOnly(t *testing.T) {
	// 部分中转站只填 error.type，不填 error.code。
	body := `{"error":{"message":"billing hard limit reached","type":"insufficient_quota"}}`
	err := buildHTTPStatusError("openai", quotaTestResponse(t, http.StatusTooManyRequests, body))
	var httpErr *HTTPStatusError
	if !errors.As(err, &httpErr) || !httpErr.QuotaExhausted {
		t.Fatalf("type-only quota signal missed: %#v", err)
	}
}

func TestHTTPStatusErrorDetectsQuotaExhaustedByMessage(t *testing.T) {
	// 中转站常见形态：只有一句中文或英文余额提示，没有稳定 code。
	for _, body := range []string{
		`{"error":{"message":"Insufficient balance, please top up"}}`,
		`{"error":{"message":"当前账户余额不足"}}`,
	} {
		err := buildHTTPStatusError("openai", quotaTestResponse(t, http.StatusTooManyRequests, body))
		var httpErr *HTTPStatusError
		if !errors.As(err, &httpErr) || !httpErr.QuotaExhausted {
			t.Fatalf("message quota signal missed for %q: %#v", body, err)
		}
	}
}

func TestHTTPStatusErrorKeepsPlainRateLimitAsRateLimited(t *testing.T) {
	body := `{"error":{"message":"Rate limit reached for requests","type":"requests","code":"rate_limit_exceeded"}}`
	err := buildHTTPStatusError("openai", quotaTestResponse(t, http.StatusTooManyRequests, body))
	var httpErr *HTTPStatusError
	if !errors.As(err, &httpErr) {
		t.Fatalf("not an HTTPStatusError: %T", err)
	}
	if httpErr.QuotaExhausted || httpErr.Category() != ProviderErrorRateLimited {
		t.Fatalf("plain rate limit misclassified: %#v", httpErr)
	}
}

func TestHTTPStatusErrorDoesNotGuessQuotaFrom5xxMessage(t *testing.T) {
	// 5xx 正文出现余额字样也可能是网关转发其他人的错误，不能据此判定额度。
	body := `{"error":{"message":"Insufficient balance"}}`
	err := buildHTTPStatusError("openai", quotaTestResponse(t, http.StatusServiceUnavailable, body))
	var httpErr *HTTPStatusError
	if !errors.As(err, &httpErr) {
		t.Fatalf("not an HTTPStatusError: %T", err)
	}
	if httpErr.QuotaExhausted {
		t.Fatalf("5xx message must not imply quota: %#v", httpErr)
	}
}

func TestHTTPStatusErrorQuotaSummaryStaysSanitized(t *testing.T) {
	body := `{"error":{"code":"insufficient_quota","message":"key sk-live-secret exposed quota"}}`
	err := buildHTTPStatusError("openai", quotaTestResponse(t, http.StatusTooManyRequests, body))
	var httpErr *HTTPStatusError
	if !errors.As(err, &httpErr) {
		t.Fatalf("not an HTTPStatusError: %T", err)
	}
	if httpErr.QuotaExhausted {
		// 命中 code 即可，但摘要不能带出密钥形态原文。
		t.Log("quota detected")
	}
	if strings.Contains(httpErr.Body, "sk-live-secret") {
		t.Fatalf("body summary leaked secret: %q", httpErr.Body)
	}
}
