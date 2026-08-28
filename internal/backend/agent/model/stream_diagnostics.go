package modeladapter

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"errors"
	"io"
	"net"
	"sync"
	"syscall"
	"time"
)

const (
	StreamCloseCauseEOF             = "eof"
	StreamCloseCauseUnexpectedEOF   = "unexpected_eof"
	StreamCloseCauseReset           = "reset"
	StreamCloseCauseTLS             = "tls"
	StreamCloseCauseIdleTimeout     = "idle_timeout"
	StreamCloseCauseContextCanceled = "context_canceled"
	StreamCloseCauseDeadline        = "deadline"
	StreamCloseCauseStreamDecode    = "stream_decode"
	StreamCloseCauseHTTPStatus      = "http_status"
	StreamCloseCauseUnknown         = "unknown"
	StreamCloseCauseNotRecorded     = "not_recorded"

	TransportOutcomeStarted   = "started"
	TransportOutcomeSucceeded = "succeeded"
	TransportOutcomeFailed    = "failed"
	TransportOutcomeCanceled  = "canceled"
	TransportOutcomeTimeout   = "timeout"

	PartialBoundaryNone          = "none"
	PartialBoundaryText          = "text"
	PartialBoundaryReasoning     = "reasoning"
	PartialBoundaryPartialTool   = "partial_tool"
	PartialBoundaryCompletedTool = "completed_tool"
	PartialBoundaryCheckpoint    = "checkpoint"
)

// StreamDiagnostics 收集单次 provider HTTP 流的可选时间线与关闭原因。
// 所有方法对 nil 接收者都是 no-op；旧日志缺失时调用方应投影为 unknown/not_recorded。
type StreamDiagnostics struct {
	mu                     sync.Mutex
	headerAt               time.Time
	firstByteAt            time.Time
	lastByteAt             time.Time
	bodyEndAt              time.Time
	lastEffectiveContentAt time.Time
	closeCause             string
	httpStatus             int
	httpAttempt            int
	transportOutcome       string
	rawBytes               bool
}

// StreamDiagnosticsSnapshot 是 StreamDiagnostics 的只读拷贝。
type StreamDiagnosticsSnapshot struct {
	HeaderAt               time.Time
	FirstByteAt            time.Time
	LastByteAt             time.Time
	BodyEndAt              time.Time
	LastEffectiveContentAt time.Time
	CloseCause             string
	HTTPStatus             int
	HTTPAttempt            int
	TransportOutcome       string
	RawBytesObserved       bool
}

func (d *StreamDiagnostics) RecordHeader(status, attempt int, at time.Time) {
	if d == nil {
		return
	}
	if at.IsZero() {
		at = time.Now()
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	d.headerAt = at.UTC()
	if status > 0 {
		d.httpStatus = status
	}
	if attempt > 0 {
		d.httpAttempt = attempt
	}
	// 2xx headers prove HTTP transport succeeded, not protocol completion.
	// A later non-2xx attempt must not keep the previous succeeded outcome.
	if status >= 200 && status < 300 {
		d.transportOutcome = TransportOutcomeSucceeded
	} else if status > 0 {
		d.transportOutcome = TransportOutcomeStarted
	}
}

func (d *StreamDiagnostics) RecordBytes(n int, at time.Time) {
	if d == nil || n <= 0 {
		return
	}
	if at.IsZero() {
		at = time.Now()
	}
	at = at.UTC()
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.firstByteAt.IsZero() {
		d.firstByteAt = at
	}
	d.lastByteAt = at
	d.rawBytes = true
}

func (d *StreamDiagnostics) RecordBodyEnd(at time.Time) {
	if d == nil {
		return
	}
	if at.IsZero() {
		at = time.Now()
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.bodyEndAt.IsZero() {
		d.bodyEndAt = at.UTC()
	}
}

func (d *StreamDiagnostics) MarkEffectiveContent() {
	if d == nil {
		return
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	d.lastEffectiveContentAt = time.Now().UTC()
}

func (d *StreamDiagnostics) RecordClose(err error) {
	if d == nil {
		return
	}
	now := time.Now().UTC()
	cause := ClassifyStreamCloseCause(err)
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.bodyEndAt.IsZero() {
		d.bodyEndAt = now
	}
	if cause == StreamCloseCauseIdleTimeout && closeCauseRank(d.closeCause) > 0 {
		return
	}
	d.closeCause = PreferCloseCause(d.closeCause, cause)
	d.transportOutcome = applyTransportOnClose(d.transportOutcome, cause)
}

func (d *StreamDiagnostics) Snapshot() StreamDiagnosticsSnapshot {
	if d == nil {
		return StreamDiagnosticsSnapshot{CloseCause: StreamCloseCauseNotRecorded}
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	closeCause := d.closeCause
	if closeCause == "" {
		closeCause = StreamCloseCauseNotRecorded
	}
	transport := d.transportOutcome
	if transport == "" {
		transport = TransportOutcomeStarted
	}
	return StreamDiagnosticsSnapshot{
		HeaderAt:               d.headerAt,
		FirstByteAt:            d.firstByteAt,
		LastByteAt:             d.lastByteAt,
		BodyEndAt:              d.bodyEndAt,
		LastEffectiveContentAt: d.lastEffectiveContentAt,
		CloseCause:             closeCause,
		HTTPStatus:             d.httpStatus,
		HTTPAttempt:            d.httpAttempt,
		TransportOutcome:       transport,
		RawBytesObserved:       d.rawBytes,
	}
}

func PreferCloseCause(existing, incoming string) string {
	// Idle must not rewrite an already recorded stream close (EOF/Stop/HTTP/reset).
	// It still outranks a later context.Canceled from the watchdog's own cancel.
	if incoming == StreamCloseCauseIdleTimeout && closeCauseRank(existing) > 0 {
		return existing
	}
	if closeCauseRank(incoming) > closeCauseRank(existing) {
		return incoming
	}
	if existing == "" {
		return StreamCloseCauseNotRecorded
	}
	return existing
}

func closeCauseRank(cause string) int {
	switch cause {
	case StreamCloseCauseIdleTimeout:
		return 90
	case StreamCloseCauseDeadline:
		return 85
	case StreamCloseCauseTLS, StreamCloseCauseReset:
		return 80
	case StreamCloseCauseUnexpectedEOF:
		return 70
	case StreamCloseCauseHTTPStatus:
		return 65
	case StreamCloseCauseStreamDecode:
		return 50
	case StreamCloseCauseEOF:
		return 40
	case StreamCloseCauseContextCanceled:
		return 30
	case StreamCloseCauseUnknown:
		return 10
	default:
		return 0
	}
}

func transportOutcomeForCloseCause(cause string) string {
	switch cause {
	case StreamCloseCauseContextCanceled:
		return TransportOutcomeCanceled
	case StreamCloseCauseDeadline, StreamCloseCauseIdleTimeout:
		return TransportOutcomeTimeout
	case StreamCloseCauseNotRecorded, "":
		return TransportOutcomeStarted
	default:
		return TransportOutcomeFailed
	}
}

func applyTransportOnClose(current, cause string) string {
	next := transportOutcomeForCloseCause(cause)
	switch next {
	case TransportOutcomeFailed, TransportOutcomeTimeout, TransportOutcomeCanceled:
		if cause == StreamCloseCauseEOF && current == TransportOutcomeSucceeded {
			return current
		}
		return next
	default:
		if current == "" || current == TransportOutcomeStarted {
			return next
		}
		return current
	}
}

// ClassifyStreamCloseCause 只用 typed unwrap 分类关闭原因。
// 禁止凭错误字符串把 transport 混同为 reset/tls；无法精确识别时返回 unknown/not_recorded。
func ClassifyStreamCloseCause(err error) string {
	if err == nil {
		return StreamCloseCauseNotRecorded
	}
	var idle *StreamIdleTimeoutError
	if errors.As(err, &idle) {
		return StreamCloseCauseIdleTimeout
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return StreamCloseCauseDeadline
	}
	if errors.Is(err, context.Canceled) {
		return StreamCloseCauseContextCanceled
	}
	var httpErr *HTTPStatusError
	if errors.As(err, &httpErr) && httpErr != nil {
		return StreamCloseCauseHTTPStatus
	}
	if isTypedTLSError(err) {
		return StreamCloseCauseTLS
	}
	if isTypedResetError(err) {
		return StreamCloseCauseReset
	}
	if errors.Is(err, io.ErrUnexpectedEOF) {
		return StreamCloseCauseUnexpectedEOF
	}
	if errors.Is(err, io.EOF) {
		return StreamCloseCauseEOF
	}
	var syntax *json.SyntaxError
	var unmarshal *json.UnmarshalTypeError
	if errors.As(err, &syntax) || errors.As(err, &unmarshal) {
		return StreamCloseCauseStreamDecode
	}
	var truncated *StreamTruncatedError
	if errors.As(err, &truncated) {
		if truncated != nil && truncated.Err != nil {
			return ClassifyStreamCloseCause(truncated.Err)
		}
		// Missing completion marker is a protocol fact, not a transport close cause.
		return StreamCloseCauseNotRecorded
	}
	return StreamCloseCauseUnknown
}

func isTypedTLSError(err error) bool {
	if err == nil {
		return false
	}
	var rec tls.RecordHeaderError
	if errors.As(err, &rec) {
		return true
	}
	var alert tls.AlertError
	if errors.As(err, &alert) {
		return true
	}
	var verify *tls.CertificateVerificationError
	if errors.As(err, &verify) {
		return true
	}
	var unknownAuth x509.UnknownAuthorityError
	if errors.As(err, &unknownAuth) {
		return true
	}
	var hostname x509.HostnameError
	if errors.As(err, &hostname) {
		return true
	}
	var certInvalid x509.CertificateInvalidError
	if errors.As(err, &certInvalid) {
		return true
	}
	var sysRoots x509.SystemRootsError
	if errors.As(err, &sysRoots) {
		return true
	}
	return false
}

func isTypedResetError(err error) bool {
	if err == nil {
		return false
	}
	if errors.Is(err, syscall.ECONNRESET) {
		return true
	}
	var opErr *net.OpError
	if errors.As(err, &opErr) && opErr != nil {
		return isTypedResetError(opErr.Err)
	}
	return false
}
