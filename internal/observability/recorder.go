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
	projectKey []byte
	humanSink  HumanSink
	queue      chan Capture
	done       chan struct{}

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
	settings = normalizeSettings(settings)
	projectKey, err := loadOrCreateProjectKey(root)
	if err != nil {
		return nil, err
	}
	writer, err := openSession(root, settings)
	if err != nil {
		return nil, err
	}
	recorder := &Recorder{
		root:       root,
		settings:   settings,
		writer:     writer,
		projectKey: projectKey,
		humanSink:  humanSink,
		queue:      make(chan Capture, settings.QueueSize),
		done:       make(chan struct{}),
		status: Status{
			Enabled:     true,
			Mode:        settings.Mode,
			SessionID:   writer.sessionID,
			SessionPath: writer.dir,
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

	recorder.mu.RLock()
	if recorder.closed || !recorder.status.Enabled {
		recorder.mu.RUnlock()
		return false
	}
	select {
	case recorder.queue <- capture:
		recorder.mu.RUnlock()
		return true
	default:
		recorder.mu.RUnlock()
		dropped := recorder.dropped.Add(1)
		recorder.mu.Lock()
		recorder.setDroppedLocked(dropped)
		recorder.mu.Unlock()
		return false
	}
}

func (recorder *Recorder) RecordEvent(ctx context.Context, event Event) bool {
	return recorder.Record(ctx, Capture{Event: event})
}

func (recorder *Recorder) Status() Status {
	if recorder == nil {
		return Status{}
	}
	recorder.mu.RLock()
	defer recorder.mu.RUnlock()
	return recorder.status
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
		recorder.setFatal(category)
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
		if err := recorder.writer.appendEvent(degradedEvent); err != nil {
			recorder.setFatal("event_write_failed")
		}
	}
	return sequence
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
