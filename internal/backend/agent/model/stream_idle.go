package modeladapter

import (
	"context"
	"fmt"
	"io"
	"sync"
	"time"
)

const (
	defaultProviderStreamIdleTimeout = 4 * time.Minute
	minProviderStreamIdleTimeout     = 30 * time.Second
)

type providerStreamIdleWatchdog struct {
	ctx     context.Context
	cancel  context.CancelCauseFunc
	timeout time.Duration
	timer   *time.Timer

	mu          sync.Mutex
	body        io.Closer
	stopped     bool
	timedOut    bool
	err         error
	diagnostics *StreamDiagnostics
}

func newProviderStreamIdleWatchdog(parent context.Context, timeout time.Duration) (context.Context, *providerStreamIdleWatchdog) {
	if parent == nil {
		parent = context.Background()
	}
	timeout = normalizeProviderStreamIdleTimeoutDuration(timeout)
	ctx, cancel := context.WithCancelCause(parent)
	watchdog := &providerStreamIdleWatchdog{
		ctx:     ctx,
		cancel:  cancel,
		timeout: timeout,
		err:     providerStreamIdleTimeoutError(timeout),
	}
	watchdog.timer = time.AfterFunc(watchdog.timeout, watchdog.expire)
	return ctx, watchdog
}

func (watchdog *providerStreamIdleWatchdog) AttachDiagnostics(diagnostics *StreamDiagnostics) {
	if watchdog == nil {
		return
	}
	watchdog.mu.Lock()
	watchdog.diagnostics = diagnostics
	watchdog.mu.Unlock()
}

func (watchdog *providerStreamIdleWatchdog) AttachBody(body io.Closer) {
	if watchdog == nil || body == nil {
		return
	}
	watchdog.mu.Lock()
	watchdog.body = body
	shouldClose := watchdog.timedOut || watchdog.stopped
	watchdog.mu.Unlock()
	if shouldClose {
		_ = body.Close()
	}
}

func (watchdog *providerStreamIdleWatchdog) MarkEffectiveContent() {
	if watchdog == nil {
		return
	}
	watchdog.mu.Lock()
	if watchdog.stopped || watchdog.timedOut || watchdog.timer == nil {
		watchdog.mu.Unlock()
		return
	}
	watchdog.timer.Reset(watchdog.timeout)
	diagnostics := watchdog.diagnostics
	watchdog.mu.Unlock()
	diagnostics.MarkEffectiveContent()
}

func (watchdog *providerStreamIdleWatchdog) Stop() {
	if watchdog == nil {
		return
	}
	watchdog.mu.Lock()
	if watchdog.stopped {
		watchdog.mu.Unlock()
		return
	}
	watchdog.stopped = true
	watchdog.body = nil
	if watchdog.timer != nil {
		watchdog.timer.Stop()
	}
	watchdog.mu.Unlock()
	watchdog.cancel(nil)
}

func (watchdog *providerStreamIdleWatchdog) Err() error {
	if watchdog == nil {
		return nil
	}
	watchdog.mu.Lock()
	defer watchdog.mu.Unlock()
	if watchdog.timedOut {
		return watchdog.err
	}
	return nil
}

func (watchdog *providerStreamIdleWatchdog) expire() {
	watchdog.mu.Lock()
	if watchdog.stopped || watchdog.timedOut {
		watchdog.mu.Unlock()
		return
	}
	diagnostics := watchdog.diagnostics
	if closeCauseRank(diagnostics.Snapshot().CloseCause) > 0 {
		watchdog.stopped = true
		if watchdog.timer != nil {
			watchdog.timer.Stop()
		}
		watchdog.mu.Unlock()
		return
	}
	watchdog.timedOut = true
	body := watchdog.body
	err := watchdog.err
	watchdog.mu.Unlock()

	diagnostics.RecordClose(err)
	watchdog.cancel(err)
	if body != nil {
		_ = body.Close()
	}
}

func normalizeProviderStreamIdleTimeoutDuration(timeout time.Duration) time.Duration {
	if timeout <= 0 {
		return defaultProviderStreamIdleTimeout
	}
	if timeout < minProviderStreamIdleTimeout {
		return minProviderStreamIdleTimeout
	}
	return timeout
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

func providerStreamIdleTimeoutError(timeout time.Duration) error {
	return &StreamIdleTimeoutError{Timeout: timeout}
}
