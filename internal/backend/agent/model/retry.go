// retry.go 在 HTTP 请求尚未产生任何 model event 的窗口内，对可重试的 provider 请求做有限次重试。
package modeladapter

import (
	"context"
	"errors"
	"fmt"
	"io"
	"math/rand/v2"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"cursor/internal/audit"
)

const (
	providerRequestMaxAttempts = 3

	retryDecisionRetry                  = "retry"
	retryDecisionSuccess                = "success"
	retryDecisionSuccessAfterRetry      = "success_after_retry"
	retryDecisionExhausted              = "exhausted"
	retryDecisionNoRetryStatus          = "no_retry_status"
	retryDecisionNoRetryContext         = "no_retry_context"
	retryDecisionNoRetryBuild           = "no_retry_request_build"
	retryDecisionNoRetryWaitBudget      = "no_retry_wait_budget"
	retryDecisionStreamPreEventEOF      = "retry_stream_pre_event_eof"
	retryDecisionNoRetryStreamError     = "no_retry_stream_error"
	retryDecisionNoRetryStreamEvent     = "no_retry_stream_event_observed"
	retryDecisionNoRetryStreamContext   = "no_retry_stream_context"
	retryDecisionNoRetryStreamExhausted = "no_retry_stream_exhausted"
	retryDecisionNoRetryStreamRawBytes  = "no_retry_stream_raw_bytes"

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
	// fallbackSafety/fallbackBudget 仅由启用 fallback 的请求设置；普通路径为 nil。
	fallbackSafety *FallbackSafetyInfo
	fallbackBudget *FallbackRetryBudget
}

type providerAttemptContextKey struct{}

type providerRetryState struct {
	attempt int
	waited  time.Duration
}

func responseRetryState(resp *http.Response) providerRetryState {
	if resp == nil || resp.Request == nil {
		return providerRetryState{attempt: 1}
	}
	state, _ := resp.Request.Context().Value(providerAttemptContextKey{}).(providerRetryState)
	if state.attempt < 1 {
		state.attempt = 1
	}
	return state
}

func setResponseRetryState(resp *http.Response, state providerRetryState) {
	if resp == nil || resp.Request == nil {
		return
	}
	resp.Request = resp.Request.WithContext(context.WithValue(resp.Request.Context(), providerAttemptContextKey{}, state))
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
	if retry.maxTotalWait < 0 {
		retry.maxTotalWait = 0
	}
	if retry.maxTotalWait == 0 && retry.fallbackBudget == nil {
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

// applyStreamRequestRetry 先按普通单渠道规则 normalize，再在 fallback 路径
// 用共享预算覆盖 maxAttempts / maxTotalWait。FallbackBudget != nil 时，
// maxTotalWait 直接覆盖为剩余 wait（包括 0），不得回落到 4s。
func applyStreamRequestRetry(retry providerRetry, req StreamRequest) providerRetry {
	retry = normalizeProviderRetry(retry)
	if req.FallbackMaxAttempts > 0 && req.FallbackMaxAttempts < retry.maxAttempts {
		retry.maxAttempts = req.FallbackMaxAttempts
	}
	if req.FallbackBudget != nil {
		retry.maxTotalWait = req.FallbackRemainingWait
		if retry.maxTotalWait < 0 {
			retry.maxTotalWait = 0
		}
	}
	retry.fallbackSafety = req.FallbackSafety
	retry.fallbackBudget = req.FallbackBudget
	return retry
}

func (retry providerRetry) consumeFallbackAttempt() bool {
	if retry.fallbackBudget != nil && !retry.fallbackBudget.TryConsumeAttempt() {
		return false
	}
	if retry.fallbackSafety != nil {
		retry.fallbackSafety.markHTTPAttempt()
	}
	return true
}

func (retry providerRetry) reserveFallbackWait(delay time.Duration) bool {
	if retry.fallbackBudget != nil && !retry.fallbackBudget.TryReserveWait(delay) {
		return false
	}
	if retry.fallbackSafety != nil {
		retry.fallbackSafety.markWaited(delay)
	}
	return true
}

func (retry providerRetry) markWaitBudgetBlocked() {
	if retry.fallbackSafety != nil {
		retry.fallbackSafety.markWaitBudgetBlocked()
	}
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

func doProviderStreamRequestWithRetry(ctx context.Context, client *http.Client, provider string, requestID string, modelCallID string, buildRequest func(context.Context) (*http.Request, error), retry providerRetry) (*http.Response, error) {
	return doProviderRequest(ctx, client, provider, requestID, modelCallID, buildRequest, audit.Default(), retry)
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
			if retry.fallbackSafety != nil {
				retry.fallbackSafety.MarkRequestBuildFailed()
			}
			recordProviderAttempt(ctx, observer, requestID, modelCallID, audit.Event{
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
		recordProviderAttempt(ctx, observer, requestID, modelCallID, audit.Event{
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

		if !retry.consumeFallbackAttempt() {
			return nil, fmt.Errorf("provider fallback HTTP attempt budget exhausted")
		}
		resp, err := client.Do(httpReq)
		outcome := classifyAttempt(err, resp, attempt, retry.maxAttempts)
		var delay time.Duration
		if outcome.retryable {
			waitDelay, canWait := retry.retryWait(attempt, resp, waited)
			if !canWait {
				outcome.retryable = false
				outcome.decision = retryDecisionNoRetryWaitBudget
				retry.markWaitBudgetBlocked()
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
		recordProviderAttempt(ctx, observer, requestID, modelCallID, responseEvent)

		if !outcome.retryable {
			if err != nil {
				closeResponseBody(resp)
				return nil, err
			}
			setResponseRetryState(resp, providerRetryState{attempt: attempt, waited: waited})
			return resp, nil
		}

		if !retry.reserveFallbackWait(delay) {
			retry.markWaitBudgetBlocked()
			if err != nil {
				closeResponseBody(resp)
				return nil, err
			}
			statusErr := buildHTTPStatusError(provider+" adapter", resp)
			closeResponseBody(resp)
			return nil, statusErr
		}
		closeResponseBody(resp)
		if sleepErr := retry.sleep(ctx, delay); sleepErr != nil {
			recordProviderAttempt(ctx, observer, requestID, modelCallID, audit.Event{
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

// retryingStreamBody retries a 2xx stream only when the body ends before the
// adapter observes or publishes any event. It owns every abandoned response body.
type retryingStreamBody struct {
	ctx          context.Context
	client       *http.Client
	provider     string
	requestID    string
	modelCallID  string
	buildRequest func(context.Context) (*http.Request, error)
	observer     *audit.Observer
	retry        providerRetry

	mu       sync.Mutex
	body     io.ReadCloser
	state    providerRetryState
	canRetry func() bool
	rawBytes bool
	closed   bool
}

func newRetryingStreamBody(ctx context.Context, client *http.Client, provider, requestID, modelCallID string, buildRequest func(context.Context) (*http.Request, error), body io.ReadCloser, state providerRetryState, observer *audit.Observer, retry providerRetry, canRetry func() bool) io.ReadCloser {
	if client == nil {
		client = http.DefaultClient
	}
	if observer == nil {
		observer = audit.Default()
	}
	if state.attempt < 1 {
		state.attempt = 1
	}
	return &retryingStreamBody{ctx: ctx, client: client, provider: provider, requestID: requestID, modelCallID: modelCallID, buildRequest: buildRequest, observer: observer, retry: normalizeProviderRetry(retry), body: body, state: state, canRetry: canRetry}
}

func (body *retryingStreamBody) Read(p []byte) (int, error) {
	for {
		inner, err := body.activeBody()
		if err != nil {
			return 0, err
		}
		n, err := inner.Read(p)
		if n > 0 {
			body.markRawBytes()
			if err != nil && !errors.Is(err, io.EOF) {
				body.recordDecision(retryDecisionNoRetryStreamRawBytes, newStreamTruncatedError(body.provider, err))
			}
			// Never retry after any raw bytes: Scanner could otherwise combine a
			// partial SSE frame from this body with bytes from the next response.
			return n, err
		}
		if err == nil {
			return 0, nil
		}
		if body.hasRawBytes() {
			if errors.Is(err, io.EOF) {
				return 0, io.EOF
			}
			body.recordDecision(retryDecisionNoRetryStreamRawBytes, newStreamTruncatedError(body.provider, err))
			return 0, err
		}
		if !isRetryableStreamReadError(err) {
			if ctxErr := body.ctx.Err(); ctxErr != nil {
				body.recordDecision(retryDecisionNoRetryStreamContext, ctxErr)
				return 0, ctxErr
			}
			body.recordDecision(retryDecisionNoRetryStreamError, newStreamTruncatedError(body.provider, err))
			return 0, err
		}
		if retryErr := body.retryAfterPreEventFailure(err); retryErr != nil {
			return 0, retryErr
		}
	}
}

func isRetryableStreamReadError(err error) bool {
	return errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF)
}

func (body *retryingStreamBody) activeBody() (io.ReadCloser, error) {
	body.mu.Lock()
	defer body.mu.Unlock()
	if body.closed || body.body == nil {
		return nil, io.ErrClosedPipe
	}
	return body.body, nil
}

func (body *retryingStreamBody) markRawBytes() {
	body.mu.Lock()
	body.rawBytes = true
	body.mu.Unlock()
	if body.retry.fallbackSafety != nil {
		body.retry.fallbackSafety.MarkRawBytesObserved()
	}
}

func (body *retryingStreamBody) hasRawBytes() bool {
	body.mu.Lock()
	defer body.mu.Unlock()
	return body.rawBytes
}

// HasRawBytes 报告此流是否已向调用方返回过任意原始字节。
// 供适配器的 fail 闭包在构造 StreamTruncatedError 时设置 RawBytesObserved，
// 以便 FallbackAwareRouter 精确检测"有字节但无 model event"的非法/不完整 SSE 场景。
func (body *retryingStreamBody) HasRawBytes() bool {
	return body.hasRawBytes()
}

// RawBytesReporter 是对外暴露"是否已返回过原始字节"状态的接口。
// retryingStreamBody 实现此接口；适配器通过接口访问，无需依赖具体类型。
// FallbackAwareRouter 通过 StreamTruncatedError.RawBytesObserved 消费该状态。
type RawBytesReporter interface {
	HasRawBytes() bool
}

func (body *retryingStreamBody) Close() error {
	body.mu.Lock()
	body.closed = true
	current := body.body
	body.body = nil
	body.mu.Unlock()
	if current == nil {
		return nil
	}
	return current.Close()
}

func (body *retryingStreamBody) retryAfterPreEventFailure(cause error) error {
	truncatedErr := newStreamTruncatedError(body.provider, cause)
	body.mu.Lock()
	if body.closed {
		body.mu.Unlock()
		return io.ErrClosedPipe
	}
	if body.rawBytes {
		body.mu.Unlock()
		body.recordDecision(retryDecisionNoRetryStreamRawBytes, truncatedErr)
		if errors.Is(cause, io.EOF) {
			return io.EOF
		}
		return cause
	}
	if err := body.ctx.Err(); err != nil {
		body.mu.Unlock()
		body.recordDecision(retryDecisionNoRetryStreamContext, err)
		return err
	}
	if body.canRetry == nil || !body.canRetry() {
		body.mu.Unlock()
		body.recordDecision(retryDecisionNoRetryStreamEvent, truncatedErr)
		return truncatedErr
	}
	if body.state.attempt >= body.retry.maxAttempts {
		body.mu.Unlock()
		body.recordDecision(retryDecisionNoRetryStreamExhausted, truncatedErr)
		return truncatedErr
	}
	delay, canWait := body.retry.retryWait(body.state.attempt, nil, body.state.waited)
	if !canWait || !body.retry.reserveFallbackWait(delay) {
		body.mu.Unlock()
		body.retry.markWaitBudgetBlocked()
		body.recordDecision(retryDecisionNoRetryWaitBudget, truncatedErr)
		return truncatedErr
	}
	old := body.body
	body.body = nil
	body.mu.Unlock()
	body.recordDecision(retryDecisionStreamPreEventEOF, truncatedErr)
	closeResponseBody(&http.Response{Body: old})
	if err := body.retry.sleep(body.ctx, delay); err != nil {
		body.recordDecision(retryDecisionNoRetryStreamContext, err)
		return err
	}
	body.mu.Lock()
	body.state.waited += delay
	body.mu.Unlock()

	for {
		body.mu.Lock()
		if body.closed {
			body.mu.Unlock()
			return io.ErrClosedPipe
		}
		if body.state.attempt >= body.retry.maxAttempts {
			body.mu.Unlock()
			return fmt.Errorf("provider stream retry exhausted")
		}
		body.state.attempt++
		attempt := body.state.attempt
		waited := body.state.waited
		body.mu.Unlock()

		request, err := body.buildRequest(body.ctx)
		if err != nil {
			if body.retry.fallbackSafety != nil {
				body.retry.fallbackSafety.MarkRequestBuildFailed()
			}
			body.recordRequestBuildFailure(err)
			return err
		}
		startedAt := time.Now()
		endpoint, targetHost, canaryMatched := body.recordRequest(request)
		if !body.retry.consumeFallbackAttempt() {
			return fmt.Errorf("provider fallback HTTP attempt budget exhausted")
		}
		resp, err := body.client.Do(request)
		outcome := classifyAttempt(err, resp, attempt, body.retry.maxAttempts)
		var nextDelay time.Duration
		if outcome.retryable {
			nextDelay, canWait = body.retry.retryWait(attempt, resp, waited)
			if !canWait {
				outcome.retryable = false
				outcome.decision = retryDecisionNoRetryWaitBudget
				body.retry.markWaitBudgetBlocked()
			}
		}
		if resp != nil {
			setResponseRetryState(resp, providerRetryState{attempt: attempt, waited: waited})
		}
		body.recordHTTPResponse(endpoint, targetHost, canaryMatched, startedAt, resp, err, outcome.decision)
		if !outcome.retryable {
			if err != nil {
				closeResponseBody(resp)
				return err
			}
			if resp.StatusCode < 200 || resp.StatusCode >= 300 {
				statusErr := buildHTTPStatusError(body.provider+" adapter", resp)
				closeResponseBody(resp)
				return statusErr
			}
			body.mu.Lock()
			if body.closed {
				body.mu.Unlock()
				closeResponseBody(resp)
				return io.ErrClosedPipe
			}
			body.body = resp.Body
			body.mu.Unlock()
			return nil
		}
		if !body.retry.reserveFallbackWait(nextDelay) {
			body.retry.markWaitBudgetBlocked()
			body.recordDecision(retryDecisionNoRetryWaitBudget, truncatedErr)
			if err != nil {
				closeResponseBody(resp)
				return err
			}
			statusErr := buildHTTPStatusError(body.provider+" adapter", resp)
			closeResponseBody(resp)
			return statusErr
		}
		closeResponseBody(resp)
		if err := body.retry.sleep(body.ctx, nextDelay); err != nil {
			body.recordDecision(retryDecisionNoRetryStreamContext, err)
			return err
		}
		body.mu.Lock()
		body.state.waited += nextDelay
		body.mu.Unlock()
	}
}

func (body *retryingStreamBody) recordRequest(request *http.Request) (endpoint, targetHost string, canaryMatched bool) {
	endpoint = "custom"
	if request != nil && request.URL != nil {
		targetHost = audit.HostFromURL(request.URL.String())
		endpoint = audit.EndpointKind(request.URL.String())
	}
	canaryMatched = matchProviderRequestCanary(body.observer, request)
	body.mu.Lock()
	attempt := body.state.attempt
	body.mu.Unlock()
	recordProviderAttempt(body.ctx, body.observer, body.requestID, body.modelCallID, audit.Event{Kind: "provider_request", Provider: body.provider, Endpoint: endpoint, TargetHost: targetHost, RequestBytes: requestContentLength(request), CanaryMatched: canaryMatched, ScopeMatched: canaryMatched, Attempt: attempt, MaxAttempts: body.retry.maxAttempts})
	return endpoint, targetHost, canaryMatched
}

func (body *retryingStreamBody) recordRequestBuildFailure(err error) {
	body.recordDecision(retryDecisionNoRetryBuild, err)
}

func (body *retryingStreamBody) recordHTTPResponse(endpoint, targetHost string, canaryMatched bool, startedAt time.Time, resp *http.Response, err error, decision string) {
	body.mu.Lock()
	attempt := body.state.attempt
	body.mu.Unlock()
	event := audit.Event{Kind: "provider_response", Provider: body.provider, Endpoint: endpoint, TargetHost: targetHost, DurationMS: time.Since(startedAt).Milliseconds(), ScopeMatched: canaryMatched, Attempt: attempt, MaxAttempts: body.retry.maxAttempts, RetryDecision: decision}
	if resp != nil {
		event.Status = resp.StatusCode
		event.RetryAfterPresent = retryAfterPresent(resp)
		if resp.ContentLength > 0 {
			event.ResponseBytes = resp.ContentLength
		}
	}
	if err != nil {
		event.ErrorCategory = ClassifyProviderError(err)
		event.ErrorMessage = audit.SanitizeMetadataText(err.Error())
	} else if resp != nil && (resp.StatusCode < 200 || resp.StatusCode >= 300) {
		event.ErrorCategory = ClassifyHTTPStatus(resp.StatusCode)
		event.ErrorMessage = audit.SanitizeMetadataText(httpStatusErrorMessage(resp.StatusCode))
	}
	recordProviderAttempt(body.ctx, body.observer, body.requestID, body.modelCallID, event)
}

func (body *retryingStreamBody) recordDecision(decision string, err error) {
	body.mu.Lock()
	attempt := body.state.attempt
	body.mu.Unlock()
	event := audit.Event{Kind: "provider_response", Provider: body.provider, Attempt: attempt, MaxAttempts: body.retry.maxAttempts, RetryDecision: decision}
	if err != nil {
		event.ErrorCategory = ClassifyProviderError(err)
		event.ErrorMessage = audit.SanitizeMetadataText(err.Error())
	}
	recordProviderAttempt(body.ctx, body.observer, body.requestID, body.modelCallID, event)
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
