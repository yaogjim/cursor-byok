// retry.go 在 HTTP 请求尚未产生任何 model event 的窗口内，对可重试的 provider 请求做有限次重试。
package modeladapter

import (
	"context"
	"errors"
	"io"
	"math/rand/v2"
	"net/http"
	"strconv"
	"strings"
	"time"

	"cursor/internal/audit"
)

const (
	providerRequestMaxAttempts = 3

	retryDecisionRetry             = "retry"
	retryDecisionSuccess           = "success"
	retryDecisionSuccessAfterRetry = "success_after_retry"
	retryDecisionExhausted         = "exhausted"
	retryDecisionNoRetryStatus     = "no_retry_status"
	retryDecisionNoRetryContext    = "no_retry_context"
	retryDecisionNoRetryBuild      = "no_retry_request_build"
	retryDecisionNoRetryWaitBudget = "no_retry_wait_budget"

	defaultRetryBaseDelay    = 200 * time.Millisecond
	defaultRetryMaxDelay     = 2 * time.Second
	defaultRetryMaxTotalWait = 4 * time.Second
	retryBodyDrainLimit      = 8 << 10
)

type providerRetry struct {
	maxAttempts  int
	baseDelay    time.Duration
	maxDelay     time.Duration
	maxTotalWait time.Duration
	sleep        func(context.Context, time.Duration) error
	jitter       func(time.Duration) time.Duration
	now          func() time.Time
}

type retryOutcome struct {
	retryable bool
	decision  string
}

func defaultProviderRetry() providerRetry {
	return providerRetry{
		maxAttempts:  providerRequestMaxAttempts,
		baseDelay:    defaultRetryBaseDelay,
		maxDelay:     defaultRetryMaxDelay,
		maxTotalWait: defaultRetryMaxTotalWait,
		sleep:        sleepContext,
		jitter:       fullJitter,
		now:          time.Now,
	}
}

func normalizeProviderRetry(retry providerRetry) providerRetry {
	if retry.maxAttempts <= 0 {
		retry.maxAttempts = providerRequestMaxAttempts
	}
	if retry.baseDelay <= 0 {
		retry.baseDelay = defaultRetryBaseDelay
	}
	if retry.maxDelay <= 0 {
		retry.maxDelay = defaultRetryMaxDelay
	}
	if retry.maxTotalWait <= 0 {
		retry.maxTotalWait = defaultRetryMaxTotalWait
	}
	if retry.sleep == nil {
		retry.sleep = sleepContext
	}
	if retry.jitter == nil {
		retry.jitter = fullJitter
	}
	if retry.now == nil {
		retry.now = time.Now
	}
	return retry
}

func sleepContext(ctx context.Context, delay time.Duration) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if delay <= 0 {
		return nil
	}
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

func fullJitter(delay time.Duration) time.Duration {
	if delay <= 0 {
		return 0
	}
	return time.Duration(rand.Int64N(int64(delay) + 1))
}

// DoProviderRequestWithRetry 对尚未产生 model event 的 provider HTTP 请求做最多 3 次尝试。
func DoProviderRequestWithRetry(
	ctx context.Context,
	client *http.Client,
	provider string,
	requestID string,
	modelCallID string,
	buildRequest func(context.Context) (*http.Request, error),
) (*http.Response, error) {
	return doProviderRequestWithRetry(ctx, client, provider, requestID, modelCallID, buildRequest)
}

func doProviderRequestWithRetry(
	ctx context.Context,
	client *http.Client,
	provider string,
	requestID string,
	modelCallID string,
	buildRequest func(context.Context) (*http.Request, error),
) (*http.Response, error) {
	return doProviderRequestWithAudit(ctx, client, provider, requestID, modelCallID, buildRequest, audit.Default())
}

func doProviderRequestWithAudit(
	ctx context.Context,
	client *http.Client,
	provider string,
	requestID string,
	modelCallID string,
	buildRequest func(context.Context) (*http.Request, error),
	observer *audit.Observer,
) (*http.Response, error) {
	return doProviderRequest(ctx, client, provider, requestID, modelCallID, buildRequest, observer, defaultProviderRetry())
}

func doProviderRequest(
	ctx context.Context,
	client *http.Client,
	provider string,
	requestID string,
	modelCallID string,
	buildRequest func(context.Context) (*http.Request, error),
	observer *audit.Observer,
	retry providerRetry,
) (*http.Response, error) {
	if client == nil {
		client = http.DefaultClient
	}
	if observer == nil {
		observer = audit.Default()
	}
	retry = normalizeProviderRetry(retry)

	var waited time.Duration
	for attempt := 1; attempt <= retry.maxAttempts; attempt++ {
		if attempt > 1 {
			if err := ctx.Err(); err != nil {
				return nil, err
			}
		}

		httpReq, err := buildRequest(ctx)
		if err != nil {
			observer.Record(audit.Event{
				Kind:          "provider_request",
				Provider:      provider,
				ErrorCategory: "request_build",
				Attempt:       attempt,
				MaxAttempts:   retry.maxAttempts,
				RetryDecision: retryDecisionNoRetryBuild,
			})
			return nil, err
		}

		startedAt := time.Now()
		targetHost := ""
		endpointKind := "custom"
		if httpReq.URL != nil {
			targetHost = audit.HostFromURL(httpReq.URL.String())
			endpointKind = audit.EndpointKind(httpReq.URL.String())
		}
		canaryMatched := matchProviderRequestCanary(observer, httpReq)
		observer.Record(audit.Event{
			Kind:          "provider_request",
			Provider:      provider,
			Endpoint:      endpointKind,
			TargetHost:    targetHost,
			RequestBytes:  requestContentLength(httpReq),
			CanaryMatched: canaryMatched,
			ScopeMatched:  canaryMatched,
			Attempt:       attempt,
			MaxAttempts:   retry.maxAttempts,
		})

		resp, err := client.Do(httpReq)
		outcome := classifyAttempt(err, resp, attempt, retry.maxAttempts)
		var delay time.Duration
		if outcome.retryable {
			waitDelay, canWait := retry.retryWait(attempt, resp, waited)
			if !canWait {
				outcome.retryable = false
				outcome.decision = retryDecisionNoRetryWaitBudget
			} else {
				delay = waitDelay
			}
		}

		responseEvent := audit.Event{
			Kind:          "provider_response",
			Provider:      provider,
			Endpoint:      endpointKind,
			TargetHost:    targetHost,
			DurationMS:    time.Since(startedAt).Milliseconds(),
			ScopeMatched:  canaryMatched,
			Attempt:       attempt,
			MaxAttempts:   retry.maxAttempts,
			RetryDecision: outcome.decision,
		}
		if resp != nil {
			responseEvent.Status = resp.StatusCode
			responseEvent.RetryAfterPresent = retryAfterPresent(resp)
			if resp.ContentLength > 0 {
				responseEvent.ResponseBytes = resp.ContentLength
			}
		}
		if err != nil {
			responseEvent.ErrorCategory = ClassifyProviderError(err)
			responseEvent.ErrorMessage = audit.SanitizeMetadataText(err.Error())
		} else if resp != nil && (resp.StatusCode < 200 || resp.StatusCode >= 300) {
			responseEvent.ErrorCategory = ClassifyHTTPStatus(resp.StatusCode)
			responseEvent.ErrorMessage = audit.SanitizeMetadataText(httpStatusErrorMessage(resp.StatusCode))
		}
		observer.Record(responseEvent)

		if !outcome.retryable {
			if err != nil {
				closeResponseBody(resp)
				return nil, err
			}
			return resp, nil
		}

		closeResponseBody(resp)
		if sleepErr := retry.sleep(ctx, delay); sleepErr != nil {
			observer.Record(audit.Event{
				Kind:          "provider_response",
				Provider:      provider,
				Endpoint:      endpointKind,
				TargetHost:    targetHost,
				ErrorCategory: ClassifyProviderError(sleepErr),
				ErrorMessage:  audit.SanitizeMetadataText(sleepErr.Error()),
				Attempt:       attempt,
				MaxAttempts:   retry.maxAttempts,
				RetryDecision: retryDecisionNoRetryContext,
			})
			return nil, sleepErr
		}
		waited += delay
	}
	return nil, errors.New("provider request retry exhausted")
}

func classifyAttempt(err error, resp *http.Response, attempt, maxAttempts int) retryOutcome {
	if err != nil {
		if isContextDoneError(err) {
			return retryOutcome{false, retryDecisionNoRetryContext}
		}
		if attempt >= maxAttempts {
			return retryOutcome{false, retryDecisionExhausted}
		}
		return retryOutcome{true, retryDecisionRetry}
	}
	if resp != nil && resp.StatusCode >= 200 && resp.StatusCode < 300 {
		if attempt == 1 {
			return retryOutcome{false, retryDecisionSuccess}
		}
		return retryOutcome{false, retryDecisionSuccessAfterRetry}
	}
	if resp != nil && isRetryableHTTPStatus(resp.StatusCode) {
		if attempt >= maxAttempts {
			return retryOutcome{false, retryDecisionExhausted}
		}
		return retryOutcome{true, retryDecisionRetry}
	}
	return retryOutcome{false, retryDecisionNoRetryStatus}
}

func isContextDoneError(err error) bool {
	return errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded)
}

func isRetryableHTTPStatus(status int) bool {
	switch status {
	case http.StatusTooManyRequests, http.StatusInternalServerError, http.StatusBadGateway, http.StatusServiceUnavailable, http.StatusGatewayTimeout:
		return true
	default:
		return false
	}
}

func (retry providerRetry) retryWait(failedAttempt int, resp *http.Response, waited time.Duration) (time.Duration, bool) {
	remaining := retry.maxTotalWait - waited
	if remaining <= 0 {
		return 0, false
	}

	var delay time.Duration
	retryAfter, hasRetryAfter := parseRetryAfter(resp, retry.now())
	if hasRetryAfter {
		if retryAfter < 0 {
			retryAfter = 0
		}
		if retryAfter > remaining {
			return 0, false
		}
		delay = retryAfter
	} else {
		delay = retry.backoffDelay(failedAttempt)
		if delay < 0 {
			delay = 0
		}
		if delay > retry.maxDelay {
			delay = retry.maxDelay
		}
		if delay > remaining {
			delay = remaining
		}
	}

	if delay == 0 {
		return 0, true
	}
	return delay, true
}

func (retry providerRetry) backoffDelay(failedAttempt int) time.Duration {
	if failedAttempt < 1 {
		failedAttempt = 1
	}
	delay := retry.baseDelay * time.Duration(1<<(failedAttempt-1))
	if delay <= 0 || delay > retry.maxDelay {
		delay = retry.maxDelay
	}
	delay = retry.jitter(delay)
	if delay < 0 {
		return 0
	}
	if delay > retry.maxDelay {
		return retry.maxDelay
	}
	return delay
}

func parseRetryAfter(resp *http.Response, now time.Time) (time.Duration, bool) {
	if resp == nil {
		return 0, false
	}
	raw := strings.TrimSpace(resp.Header.Get("Retry-After"))
	if raw == "" {
		return 0, false
	}
	if seconds, err := strconv.ParseInt(raw, 10, 64); err == nil {
		if seconds < 0 {
			return 0, true
		}
		return time.Duration(seconds) * time.Second, true
	}
	when, err := http.ParseTime(raw)
	if err != nil {
		return 0, false
	}
	delay := when.Sub(now)
	if delay < 0 {
		return 0, true
	}
	return delay, true
}

func closeResponseBody(resp *http.Response) {
	if resp == nil || resp.Body == nil {
		return
	}
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, retryBodyDrainLimit))
	_ = resp.Body.Close()
}

func httpStatusErrorMessage(statusCode int) string {
	return "http status=" + strconv.Itoa(statusCode)
}

const maxProviderCanaryInspectionBytes = 32 << 20

func matchProviderRequestCanary(observer *audit.Observer, request *http.Request) bool {
	if observer == nil || !observer.Enabled() || request == nil || request.GetBody == nil {
		return false
	}
	if request.ContentLength > maxProviderCanaryInspectionBytes {
		return false
	}
	body, err := request.GetBody()
	if err != nil {
		return false
	}
	defer body.Close()
	data, err := io.ReadAll(io.LimitReader(body, maxProviderCanaryInspectionBytes+1))
	if err != nil || len(data) > maxProviderCanaryInspectionBytes {
		return false
	}
	return observer.MatchCanary(data)
}

func requestContentLength(request *http.Request) int {
	if request == nil || request.ContentLength <= 0 {
		return 0
	}
	return int(request.ContentLength)
}

func ProviderRetryAttemptSummary(resp *http.Response) string {
	if resp == nil {
		return ""
	}
	retryAfter := strings.TrimSpace(resp.Header.Get("Retry-After"))
	if retryAfter == "" {
		return ""
	}
	return "retry_after=" + retryAfter
}
