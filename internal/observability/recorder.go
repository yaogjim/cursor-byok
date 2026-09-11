package observability

import (
	"context"
	"errors"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

type HumanSink func(Event)

type Recorder struct {
	root       string
	settings   Settings
	writer     *sessionWriter
	diag       *diagnosticSink
	projectKey []byte
	humanSink  HumanSink
	queue      chan Capture
	done       chan struct{}
	// reserve is the number of queue slots kept for WARN/ERROR captures when
	// diagnostics are enabled; ordinary captures are admitted only below
	// cap(queue)-reserve. It is max(1, QueueSize/8), clamped to QueueSize, and 0
	// in ModeOff so off mode keeps the previous shared-queue behavior.
	reserve int
	// diagQueueDropped counts WARN/ERROR captures rejected at queue admission,
	// as opposed to diagnostic sink write failures. DiagnosticDropped reports
	// the sum of both.
	diagQueueDropped atomic.Uint64

	mu        sync.RWMutex
	closed    bool
	status    Status
	closeErr  error
	closeOnce sync.Once
	dropped   atomic.Uint64
}

func NewRecorder(root string, settings Settings) (*Recorder, error) {
	return NewRecorderWithHumanSink(root, settings, nil)
}

func NewRecorderWithHumanSink(root string, settings Settings, humanSink HumanSink) (*Recorder, error) {
	normalized := normalizeSettings(settings)
	return newRecorderWithBudget(root, normalized, humanSink, newLogBudget(root, normalized))
}

func newRecorderWithBudget(root string, settings Settings, humanSink HumanSink, budget *logBudget) (*Recorder, error) {
	settings = normalizeSettings(settings)
	projectKey, err := loadOrCreateProjectKey(root)
	if err != nil {
		return nil, err
	}
	writer, err := openSession(root, settings, budget)
	if err != nil {
		return nil, err
	}
	var diag *diagnosticSink
	if settings.Mode != ModeOff {
		reserve := budget.diagnosticMaxBytes()
		diag, err = openDiagnosticSink(root, budget, time.Duration(settings.RetentionDays)*24*time.Hour, reserve)
		if err != nil {
			diag = &diagnosticSink{budget: budget}
			diag.markUnavailable("diagnostic_unavailable")
		}
	}
	diagnosticStatus := diag.status()
	reserve := 0
	if settings.Mode != ModeOff {
		reserve = settings.QueueSize / 8
		if reserve < 1 {
			reserve = 1
		}
		if reserve > settings.QueueSize {
			reserve = settings.QueueSize
		}
	}
	recorder := &Recorder{
		root:       root,
		settings:   settings,
		writer:     writer,
		diag:       diag,
		projectKey: projectKey,
		humanSink:  humanSink,
		queue:      make(chan Capture, settings.QueueSize),
		done:       make(chan struct{}),
		reserve:    reserve,
		status: Status{
			Enabled:             true,
			Mode:                settings.Mode,
			SessionID:           writer.sessionID,
			SessionPath:         writer.dir,
			DiagnosticEnabled:   diagnosticStatus.enabled,
			DiagnosticDegraded:  diagnosticStatus.degraded,
			DiagnosticDropped:   diagnosticStatus.dropped,
			DiagnosticLastError: diagnosticStatus.lastErr,
		},
	}
	go recorder.run()
	return recorder, nil
}

func (recorder *Recorder) Record(ctx context.Context, capture Capture) (accepted bool) {
	if recorder == nil {
		return false
	}
	defer func() {
		if recover() != nil {
			recorder.setFatal("capture_panic")
			accepted = false
		}
	}()
	applyCorrelation(&capture.Event, CorrelationFromContext(ctx))
	if capture.Event.ProjectID == "" {
		capture.Event.ProjectID = deriveProjectID(recorder.projectKey, capture.ProjectPaths)
	}
	capture.ProjectPaths = nil
	capture.Event.Layer = strings.TrimSpace(capture.Event.Layer)
	capture.Event.Event = strings.TrimSpace(capture.Event.Event)
	capture.Event.ProjectID = strings.TrimSpace(capture.Event.ProjectID)
	capture.Event.TurnID = strings.TrimSpace(capture.Event.TurnID)
	capture.Event.Route = sanitizeString(capture.Event.Route)
	capture.Event.ExecutionTarget = strings.TrimSpace(capture.Event.ExecutionTarget)
	capture.Event.Protocol = strings.TrimSpace(capture.Event.Protocol)
	capture.Event.Status = strings.TrimSpace(capture.Event.Status)
	capture.Event.ErrorCategory = strings.TrimSpace(capture.Event.ErrorCategory)
	capture.Event = normalizeEventSemantics(capture.Event)
	if capture.Event.Fields != nil {
		capture.Event.Fields = sanitizedMap(capture.Event.Fields)
	}
	if recorder.settings.Mode != ModeFull {
		capture.Payload = nil
	} else if capture.Payload != nil {
		payloadCopy := *capture.Payload
		payloadCopy.Name = strings.TrimSpace(payloadCopy.Name)
		payloadCopy.ContentType = strings.TrimSpace(payloadCopy.ContentType)
		payloadCopy.Data = Sanitize(payloadCopy.Data)
		capture.Payload = &payloadCopy
	}

	// Admission is serialized on the same mutex that guards closed/Enabled and
	// the queue close, so checking len and then sending cannot race a concurrent
	// close or another producer. A WARN/ERROR capture may use the slots reserved
	// for diagnostics; an ordinary capture is capped at QueueSize-reserve so it
	// can never consume the reserve.
	recorder.mu.Lock()
	if recorder.closed || !recorder.status.Enabled {
		recorder.mu.Unlock()
		return false
	}
	diagnostic := isDiagnosticEvent(capture.Event)
	limit := cap(recorder.queue)
	if !diagnostic {
		limit -= recorder.reserve
	}
	if len(recorder.queue) < limit {
		select {
		case recorder.queue <- capture:
			recorder.mu.Unlock()
			return true
		default:
		}
	}
	dropped := recorder.dropped.Add(1)
	recorder.setDroppedLocked(dropped)
	if diagnostic && recorder.diag != nil {
		recorder.markDiagnosticQueueDroppedLocked()
	}
	recorder.mu.Unlock()
	return false
}

func (recorder *Recorder) RecordEvent(ctx context.Context, event Event) bool {
	return recorder.Record(ctx, Capture{Event: event})
}

func (recorder *Recorder) Status() Status {
	if recorder == nil {
		return Status{}
	}
	recorder.mu.RLock()
	status := recorder.status
	recorder.mu.RUnlock()
	if recorder.writer != nil {
		if manifestErr := recorder.writer.manifestError(); manifestErr != "" {
			if status.LastError == "" {
				status.LastError = manifestErr
			} else {
				status.LastError = status.LastError + "; " + manifestErr
			}
		}
	}
	return status
}

func (recorder *Recorder) Close() error {
	if recorder == nil {
		return nil
	}
	recorder.closeOnce.Do(func() {
		recorder.mu.Lock()
		recorder.closed = true
		recorder.status.Enabled = false
		close(recorder.queue)
		recorder.mu.Unlock()
	})
	<-recorder.done
	if recorder.diag != nil {
		diagErr := recorder.diag.close()
		diagStatus := recorder.diag.status()
		recorder.mu.Lock()
		recorder.mergeDiagnosticStatusLocked(diagStatus)
		recorder.mu.Unlock()
		if diagErr != nil {
			recorder.mu.Lock()
			recorder.closeErr = errors.Join(recorder.closeErr, diagErr)
			recorder.mu.Unlock()
		}
	}
	recorder.mu.RLock()
	defer recorder.mu.RUnlock()
	return recorder.closeErr
}

func (recorder *Recorder) run() {
	defer close(recorder.done)
	var sequence uint64
	for capture := range recorder.queue {
		sequence = recorder.safeWriteCapture(sequence+1, capture)
	}
	dropped := recorder.dropped.Load()
	recorder.writer.updateDropped(dropped)
	if err := recorder.writer.close("closed"); err != nil {
		recorder.mu.Lock()
		recorder.closeErr = err
		recorder.mu.Unlock()
		recorder.setFatal("manifest_close_failed")
	}
}

func (recorder *Recorder) safeWriteCapture(sequence uint64, capture Capture) (nextSequence uint64) {
	nextSequence = sequence
	defer func() {
		if recover() != nil {
			recorder.setFatal("capture_sink_panic")
		}
	}()
	return recorder.writeCapture(sequence, capture)
}

func (recorder *Recorder) writeCapture(sequence uint64, capture Capture) uint64 {
	event := capture.Event
	event.SchemaVersion = SchemaVersion
	event.Sequence = sequence
	event.AppSessionID = recorder.writer.sessionID
	if event.Timestamp.IsZero() {
		event.Timestamp = time.Now().UTC()
	} else {
		event.Timestamp = event.Timestamp.UTC()
	}
	event.DroppedEvents = recorder.dropped.Load()

	// Identity (sequence/app_session_id/timestamp) is fixed before any sink
	// receives the event, and diagnostics is attempted independently of the
	// trace write.
	recorder.writeDiagnostic(event)

	var payloadError string
	if capture.Payload != nil && !recorder.Status().PayloadDegraded {
		payloadRef, err := recorder.writer.appendPayload(*capture.Payload, event.Timestamp)
		if err != nil {
			payloadError = "payload_write_failed"
			if errors.Is(err, errSessionQuotaExceeded) {
				payloadError = "payload_quota_exceeded"
			}
			recorder.setPayloadDegraded(payloadError)
		} else {
			event.PayloadRef = payloadRef
		}
	}
	if err := recorder.writer.appendEvent(event); err != nil {
		category := "event_write_failed"
		if errors.Is(err, errSessionQuotaExceeded) {
			category = "event_quota_exceeded"
		}
		recorder.setTraceDegraded(category)
		return sequence
	}
	recorder.writer.updateDropped(event.DroppedEvents)
	if recorder.humanSink != nil {
		recorder.humanSink(event)
	}
	if payloadError != "" {
		sequence++
		degradedEvent := Event{
			SchemaVersion:       SchemaVersion,
			Timestamp:           time.Now().UTC(),
			Sequence:            sequence,
			AppSessionID:        recorder.writer.sessionID,
			ProjectID:           event.ProjectID,
			TraceID:             event.TraceID,
			SpanID:              event.SpanID,
			Layer:               "observability",
			Event:               "payload_capture_disabled",
			Capability:          "config",
			Operation:           "observability.payload_capture",
			Direction:           DirectionProxyInternal,
			Status:              "degraded",
			SemanticOutcome:     OutcomeDegraded,
			ImplementationState: ImplementationImplemented,
			Severity:            SeverityWarning,
			ErrorCategory:       payloadError,
			DroppedEvents:       recorder.dropped.Load(),
		}
		recorder.writeDiagnostic(degradedEvent)
		if err := recorder.writer.appendEvent(degradedEvent); err != nil {
			recorder.setTraceDegraded("event_write_failed")
		}
	}
	return sequence
}

// writeDiagnostic projects WARN/ERROR events into the diagnostics partition
// before the trace write. Diagnostics failures are recorded independently and
// never disable capture or the caller.
func (recorder *Recorder) writeDiagnostic(event Event) {
	if recorder == nil || recorder.diag == nil {
		return
	}
	recorder.diag.write(event)
	status := recorder.diag.status()
	recorder.mu.Lock()
	recorder.mergeDiagnosticStatusLocked(status)
	recorder.mu.Unlock()
}

// isDiagnosticEvent reports whether a normalized event is persisted to the
// diagnostics partition (WARN/ERROR severity). Severity is always projected by
// normalizeEventSemantics before admission, so this matches the sink filter.
func isDiagnosticEvent(event Event) bool {
	switch strings.ToLower(strings.TrimSpace(event.Severity)) {
	case SeverityWarning, SeverityError:
		return true
	default:
		return false
	}
}

// markDiagnosticQueueDroppedLocked records a WARN/ERROR rejected at queue
// admission. Callers must hold recorder.mu. DiagnosticDropped is published as
// the sink drop count plus this queue-drop count, so incrementing the published
// counter in step keeps that sum exact: a queue rejection does not change the
// sink count.
func (recorder *Recorder) markDiagnosticQueueDroppedLocked() {
	recorder.diagQueueDropped.Add(1)
	recorder.status.DiagnosticDegraded = true
	recorder.status.DiagnosticDropped++
	recorder.status.DiagnosticLastError = "diagnostic_queue_full"
}

// mergeDiagnosticStatusLocked publishes the sink status together with queue
// admission losses. DiagnosticDropped is the sum of sink drops and queue
// rejections, and a queue rejection keeps DiagnosticDegraded/LastError visible
// so a later successful sink write or Close cannot erase the loss. Callers must
// hold recorder.mu.
func (recorder *Recorder) mergeDiagnosticStatusLocked(sink diagnosticStatus) {
	queueDropped := recorder.diagQueueDropped.Load()
	recorder.status.DiagnosticEnabled = sink.enabled
	recorder.status.DiagnosticDegraded = sink.degraded || queueDropped > 0
	recorder.status.DiagnosticDropped = sink.dropped + queueDropped
	if queueDropped > 0 {
		recorder.status.DiagnosticLastError = "diagnostic_queue_full"
		return
	}
	recorder.status.DiagnosticLastError = sink.lastErr
}

func (recorder *Recorder) setPayloadDegraded(category string) {
	recorder.mu.Lock()
	recorder.status.PayloadDegraded = true
	recorder.status.LastError = strings.TrimSpace(category)
	dropped := recorder.status.DroppedEvents
	recorder.mu.Unlock()
	recorder.writer.markDegraded(dropped, category)
}

func (recorder *Recorder) setFatal(category string) {
	recorder.mu.Lock()
	recorder.status.Enabled = false
	recorder.status.LastError = strings.TrimSpace(category)
	recorder.mu.Unlock()
	recorder.writer.markDegraded(recorder.dropped.Load(), category)
}

// setTraceDegraded records a trace-sink failure without disabling capture, so
// the diagnostics sink can keep persisting WARN/ERROR events on its own budget.
func (recorder *Recorder) setTraceDegraded(category string) {
	recorder.mu.Lock()
	recorder.status.LastError = strings.TrimSpace(category)
	if category == "event_quota_exceeded" || category == "payload_quota_exceeded" {
		recorder.status.QuotaBlocked = true
	}
	recorder.mu.Unlock()
	recorder.writer.markDegraded(recorder.dropped.Load(), category)
}

func (recorder *Recorder) setDroppedLocked(dropped uint64) {
	recorder.status.DroppedEvents = dropped
	recorder.status.LastError = "event_queue_full"
}

func sanitizedMap(input map[string]any) map[string]any {
	if input == nil {
		return nil
	}
	sanitized, ok := Sanitize(input).(map[string]any)
	if !ok {
		return map[string]any{"omitted": true, "reason": "sanitize_failed"}
	}
	return sanitized
}

func CloseAll(recorders ...*Recorder) error {
	var closeErr error
	for _, recorder := range recorders {
		closeErr = errors.Join(closeErr, recorder.Close())
	}
	return closeErr
}
