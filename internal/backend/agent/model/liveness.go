// liveness.go 实现四阶段活性：建连、首个有效事件、流空闲、整段逻辑调用。
// 建连/首事件按 HTTP attempt；call timeout 跨整个 fallback，且不计 recovery wait。
package modeladapter

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"sync"
	"time"
)

// LivenessTimeoutError 是网关生成的建连/首事件/整呼超时。
type LivenessTimeoutError struct {
	Phase   string
	Timeout time.Duration
}

func (err *LivenessTimeoutError) Error() string {
	if err == nil {
		return "provider liveness timeout"
	}
	phase := err.Phase
	if phase == "" {
		phase = LivenessPhaseCall
	}
	seconds := int(err.Timeout / time.Second)
	if seconds > 0 && err.Timeout == time.Duration(seconds)*time.Second {
		return fmt.Sprintf("provider %s timeout after %ds", phase, seconds)
	}
	return fmt.Sprintf("provider %s timeout after %s", phase, err.Timeout)
}

func (err *LivenessTimeoutError) Category() string {
	if err == nil {
		return ProviderErrorTransport
	}
	return classifyLivenessTimeout(*err).Category
}

type StreamIdleTimeoutError struct {
	Timeout time.Duration
}

func (err *StreamIdleTimeoutError) Error() string {
	if err == nil {
		return "provider stream idle timeout without effective content"
	}
	seconds := int(err.Timeout / time.Second)
	if seconds > 0 && err.Timeout == time.Duration(seconds)*time.Second {
		return fmt.Sprintf("provider stream idle timeout after %ds without effective content", seconds)
	}
	return fmt.Sprintf("provider stream idle timeout after %s without effective content", err.Timeout)
}

func (err *StreamIdleTimeoutError) Category() string {
	return ProviderErrorStreamIdleTimeout
}

type providerLiveness struct {
	settings RecoverySettings
	ctx      context.Context
	cancel   context.CancelCauseFunc
	now      func() time.Time

	mu            sync.Mutex
	callTimer     *time.Timer
	phaseTimer    *time.Timer
	phase         string
	headersSeen   bool
	effective     bool
	paused        bool
	callDeadline  time.Time
	callRemaining time.Duration
	timedOut      bool
	stopped       bool
	err           error
	attemptErr    error
	body          io.Closer
	diagnostics   *StreamDiagnostics
	attemptCancel context.CancelCauseFunc
}

func newProviderLiveness(parent context.Context, settings RecoverySettings) (context.Context, *providerLiveness) {
	if parent == nil {
		parent = context.Background()
	}
	settings = NormalizeRecoverySettings(settings)
	ctx, cancel := context.WithCancelCause(parent)
	live := &providerLiveness{
		settings: settings,
		ctx:      ctx,
		cancel:   cancel,
		now:      time.Now,
	}
	live.callDeadline = live.now().Add(settings.CallTimeout)
	live.callTimer = time.AfterFunc(settings.CallTimeout, func() {
		live.expire(LivenessPhaseCall, settings.CallTimeout)
	})
	return ctx, live
}

func attachRequestLiveness(ctx context.Context, req *StreamRequest) (context.Context, *providerLiveness, bool) {
	if req != nil && req.liveness != nil {
		return req.liveness.Context(), req.liveness, false
	}
	settings := DefaultRecoverySettings()
	if req != nil {
		settings = req.normalizedRecoverySettings()
	}
	ctx, live := newProviderLiveness(ctx, settings)
	if req != nil {
		req.liveness = live
	}
	return ctx, live, true
}

func (live *providerLiveness) Context() context.Context {
	if live == nil {
		return context.Background()
	}
	return live.ctx
}

func (live *providerLiveness) AttachDiagnostics(diagnostics *StreamDiagnostics) {
	if live == nil {
		return
	}
	live.mu.Lock()
	live.diagnostics = diagnostics
	live.mu.Unlock()
}

func (live *providerLiveness) AttachBody(body io.Closer) {
	if live == nil || body == nil {
		return
	}
	live.mu.Lock()
	live.body = body
	shouldClose := live.timedOut
	live.mu.Unlock()
	if shouldClose {
		_ = body.Close()
	}
}

func (live *providerLiveness) SetAttemptCancel(cancel context.CancelCauseFunc) {
	if live == nil {
		return
	}
	live.mu.Lock()
	live.attemptCancel = cancel
	live.attemptErr = nil
	live.body = nil
	live.headersSeen = false
	live.effective = false
	if live.phaseTimer != nil {
		live.phaseTimer.Stop()
		live.phaseTimer = nil
	}
	live.phase = LivenessPhaseConnect
	live.mu.Unlock()
}

func (live *providerLiveness) MarkHeadersReceived() {
	if live == nil {
		return
	}
	live.mu.Lock()
	defer live.mu.Unlock()
	live.headersSeen = true
	if live.effective || live.timedOut {
		return
	}
	live.resetPhaseTimerLocked(LivenessPhaseFirstEvent, live.settings.FirstEventTimeout)
}

func (live *providerLiveness) MarkEffectiveContent() {
	if live == nil {
		return
	}
	live.mu.Lock()
	if live.timedOut {
		live.mu.Unlock()
		return
	}
	live.effective = true
	live.resetPhaseTimerLocked(LivenessPhaseIdle, live.settings.StreamIdleTimeout)
	diagnostics := live.diagnostics
	live.mu.Unlock()
	if diagnostics != nil {
		diagnostics.MarkEffectiveContent()
	}
}

func (live *providerLiveness) PauseCallClock() {
	if live == nil {
		return
	}
	live.mu.Lock()
	defer live.mu.Unlock()
	if live.paused || live.timedOut || live.callTimer == nil {
		return
	}
	if !live.callTimer.Stop() {
		return
	}
	live.paused = true
	remaining := live.callDeadline.Sub(live.now())
	if remaining < 0 {
		remaining = 0
	}
	live.callRemaining = remaining
}

func (live *providerLiveness) ResumeCallClock() {
	if live == nil {
		return
	}
	live.mu.Lock()
	if !live.paused || live.timedOut {
		live.mu.Unlock()
		return
	}
	live.paused = false
	remaining := live.callRemaining
	if remaining < 0 {
		remaining = 0
	}
	if remaining == 0 {
		timeout := live.settings.CallTimeout
		live.mu.Unlock()
		live.expire(LivenessPhaseCall, timeout)
		return
	}
	live.callDeadline = live.now().Add(remaining)
	live.callTimer = time.AfterFunc(remaining, func() {
		live.expire(LivenessPhaseCall, live.settings.CallTimeout)
	})
	live.mu.Unlock()
}

func (live *providerLiveness) Stop() {
	if live == nil {
		return
	}
	live.mu.Lock()
	live.stopped = true
	live.stopTimersLocked()
	live.body = nil
	live.attemptCancel = nil
	live.mu.Unlock()
	live.cancel(nil)
}

func (live *providerLiveness) Err() error {
	if live == nil {
		return nil
	}
	live.mu.Lock()
	defer live.mu.Unlock()
	if live.timedOut {
		return live.err
	}
	return live.attemptErr
}

func (live *providerLiveness) expire(phase string, timeout time.Duration) {
	if live == nil {
		return
	}
	live.mu.Lock()
	if live.timedOut || live.stopped {
		live.mu.Unlock()
		return
	}
	attemptScoped := phase == LivenessPhaseConnect || phase == LivenessPhaseFirstEvent
	if phase == LivenessPhaseConnect && live.headersSeen {
		live.mu.Unlock()
		return
	}
	if phase == LivenessPhaseFirstEvent && live.effective {
		live.mu.Unlock()
		return
	}
	if phase == LivenessPhaseCall && live.paused {
		live.mu.Unlock()
		return
	}
	err := livenessTimeoutError(phase, timeout)
	existingCause := ""
	if live.diagnostics != nil {
		existingCause = live.diagnostics.Snapshot().CloseCause
		live.diagnostics.RecordClose(err)
	}
	if closeCauseRank(existingCause) > 0 {
		live.stopTimersLocked()
		live.mu.Unlock()
		return
	}
	if attemptScoped {
		live.attemptErr = err
	} else {
		live.timedOut = true
		live.err = err
	}
	body := live.body
	attemptCancel := live.attemptCancel
	cancelStream := !attemptScoped
	live.stopTimersLocked()
	live.mu.Unlock()

	if attemptCancel != nil {
		attemptCancel(err)
	}
	if cancelStream {
		live.cancel(err)
	}
	if body != nil {
		_ = body.Close()
	}
}

func (live *providerLiveness) resetPhaseTimerLocked(phase string, timeout time.Duration) {
	if live.phaseTimer != nil {
		live.phaseTimer.Stop()
		live.phaseTimer = nil
	}
	live.phase = phase
	if timeout <= 0 || live.timedOut {
		return
	}
	live.phaseTimer = time.AfterFunc(timeout, func() { live.expire(phase, timeout) })
}

func (live *providerLiveness) stopTimersLocked() {
	if live.phaseTimer != nil {
		live.phaseTimer.Stop()
		live.phaseTimer = nil
	}
	if live.callTimer != nil {
		live.callTimer.Stop()
		live.callTimer = nil
	}
}

func livenessTimeoutError(phase string, timeout time.Duration) error {
	if phase == LivenessPhaseIdle {
		return &StreamIdleTimeoutError{Timeout: timeout}
	}
	return &LivenessTimeoutError{Phase: phase, Timeout: timeout}
}

func (retry providerRetry) connectTimeout() time.Duration {
	if retry.recovery.ConnectTimeout > 0 {
		return retry.recovery.ConnectTimeout
	}
	return defaultConnectTimeout
}

func (retry providerRetry) startHTTPAttempt(parent context.Context) (context.Context, func(*http.Response, error) error) {
	if parent == nil {
		parent = context.Background()
	}
	timeout := retry.connectTimeout()
	ctx, cancel := context.WithCancelCause(parent)
	if retry.liveness != nil {
		retry.liveness.SetAttemptCancel(cancel)
	}
	var timer *time.Timer
	if timeout > 0 {
		timer = time.AfterFunc(timeout, func() {
			if parent.Err() != nil {
				return
			}
			if retry.liveness != nil {
				retry.liveness.expire(LivenessPhaseConnect, timeout)
				return
			}
			cancel(&LivenessTimeoutError{Phase: LivenessPhaseConnect, Timeout: timeout})
		})
	}
	return ctx, func(resp *http.Response, err error) error {
		if timer != nil {
			timer.Stop()
		}
		if resp != nil {
			if retry.liveness != nil && resp.StatusCode >= http.StatusOK && resp.StatusCode < http.StatusMultipleChoices {
				retry.liveness.MarkHeadersReceived()
			}
			return err
		}
		if liveErr := retry.livenessError(); liveErr != nil {
			return liveErr
		}
		if cause := context.Cause(ctx); cause != nil {
			var live *LivenessTimeoutError
			if errors.As(cause, &live) {
				return live
			}
			if parent.Err() == nil && timeout > 0 && (errors.Is(cause, context.Canceled) || errors.Is(cause, context.DeadlineExceeded)) {
				return &LivenessTimeoutError{Phase: LivenessPhaseConnect, Timeout: timeout}
			}
		}
		if err != nil {
			cancel(err)
			return err
		}
		cancel(nil)
		return nil
	}
}

func (retry providerRetry) pauseCallClock() {
	if retry.liveness != nil {
		retry.liveness.PauseCallClock()
	}
}

func (retry providerRetry) resumeCallClock() {
	if retry.liveness != nil {
		retry.liveness.ResumeCallClock()
	}
}

func (retry providerRetry) livenessError() error {
	if retry.liveness == nil {
		return nil
	}
	return retry.liveness.Err()
}
