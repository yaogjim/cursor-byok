package forwarder

import (
	"encoding/json"
	"fmt"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"cursor/internal/logger"
	"cursor/internal/logsink"
	"cursor/internal/observability"
)

const (
	debugQueueSize             = 2048
	debugEventSegmentMaxBytes  = 16 << 20
	debugEventMaxFiles         = 32
	debugEventMaxTotalBytes    = 256 << 20
	debugPayloadPackMaxBytes   = 64 << 20
	debugPayloadPackMaxFiles   = 16
	debugPayloadMaxTotalBytes  = 512 << 20
	debugInlinePayloadMaxBytes = 32 << 10
	debugPayloadPreviewBytes   = 512
	debugMaxOpenResources      = 128
	debugResourceIdleTimeout   = 2 * time.Minute
	debugResourceSweep         = time.Minute
	debugLogMaxAge             = 14 * 24 * time.Hour
	debugWarningInterval       = time.Minute
)

type debugSink struct {
	queue       chan debugWriteTask
	stop        chan struct{}
	done        chan struct{}
	closeOnce   sync.Once
	lifecycleMu sync.RWMutex
	closed      bool
	writers     map[string]*logsink.RotatingFile
	payloads    map[string]*logsink.PayloadPackStore
	lastUsed    map[string]time.Time
	dropped     atomic.Uint64
	lastWarning atomic.Int64
}

type debugWriteTask struct {
	dir    string
	stream string
	event  map[string]any
}

type debugFieldEncoder func() ([]byte, error)

func newDebugSink() *debugSink {
	sink := &debugSink{
		queue:    make(chan debugWriteTask, debugQueueSize),
		stop:     make(chan struct{}),
		done:     make(chan struct{}),
		writers:  make(map[string]*logsink.RotatingFile),
		payloads: make(map[string]*logsink.PayloadPackStore),
		lastUsed: make(map[string]time.Time),
	}
	go sink.run()
	return sink
}

func (sink *debugSink) Append(dir string, filename string, event map[string]any) {
	if sink == nil || strings.TrimSpace(dir) == "" || len(event) == 0 {
		return
	}
	cloned := cloneDebugEvent(event)
	task := debugWriteTask{
		dir:    strings.TrimSpace(dir),
		stream: debugStreamName(filename),
		event:  cloned,
	}
	sink.lifecycleMu.RLock()
	defer sink.lifecycleMu.RUnlock()
	if sink.closed {
		return
	}
	select {
	case sink.queue <- task:
	default:
		sink.dropped.Add(1)
		sink.warn(fmt.Errorf("debug log queue is full"), "queue_full")
	}
}

func (sink *debugSink) Close() {
	if sink == nil || sink.stop == nil {
		return
	}
	sink.closeOnce.Do(func() {
		sink.lifecycleMu.Lock()
		sink.closed = true
		close(sink.stop)
		sink.lifecycleMu.Unlock()
	})
	if sink.done != nil {
		<-sink.done
	}
}

func (sink *debugSink) run() {
	ticker := time.NewTicker(debugResourceSweep)
	defer func() {
		ticker.Stop()
		sink.closeAllResources()
		if sink.done != nil {
			close(sink.done)
		}
	}()
	for {
		select {
		case task := <-sink.queue:
			sink.writeSafely(task)
		case now := <-ticker.C:
			sink.closeIdleResources(now)
		case <-sink.stop:
			sink.drainPending()
			return
		}
	}
}

func (sink *debugSink) drainPending() {
	for {
		select {
		case task := <-sink.queue:
			sink.writeSafely(task)
		default:
			return
		}
	}
}

func (sink *debugSink) closeAllResources() {
	for key, writer := range sink.writers {
		if writer != nil {
			_ = writer.Close()
		}
		delete(sink.writers, key)
	}
	for key, store := range sink.payloads {
		if store != nil {
			_ = store.Close()
		}
		delete(sink.payloads, key)
	}
	clear(sink.lastUsed)
}

func (sink *debugSink) writeSafely(task debugWriteTask) {
	defer func() {
		if recovered := recover(); recovered != nil {
			sink.warn(fmt.Errorf("panic: %v", recovered), "panic")
		}
	}()
	if err := sink.write(task); err != nil {
		sink.warn(err, "write_failed")
	}
}

func (sink *debugSink) write(task debugWriteTask) error {
	event := task.event
	if dropped := sink.dropped.Swap(0); dropped > 0 {
		event["dropped_before"] = dropped
	}
	if err := sink.externalizeLargeFields(task.dir, task.stream, event); err != nil {
		event["capture_error"] = err.Error()
	}
	if sanitized, ok := observability.Sanitize(event).(map[string]any); ok {
		event = sanitized
	}
	encoded, err := json.Marshal(event)
	if err != nil {
		return fmt.Errorf("encode debug event: %w", err)
	}
	encoded = append(encoded, '\n')
	writer := sink.eventWriter(task.dir, task.stream)
	if _, err := writer.Append(encoded); err != nil {
		_ = writer.Close()
		key := debugWriterKey(task.dir, task.stream)
		delete(sink.writers, key)
		delete(sink.lastUsed, "event:"+key)
		return fmt.Errorf("append debug event stream=%s: %w", task.stream, err)
	}
	return nil
}

func (sink *debugSink) externalizeLargeFields(dir string, stream string, event map[string]any) error {
	if len(event) == 0 {
		return nil
	}
	var externalizeErr error
	for key, value := range event {
		if value == nil || strings.HasSuffix(key, "_ref") {
			continue
		}
		encoded, err := encodeDebugField(value)
		if err != nil {
			externalizeErr = fmt.Errorf("encode debug field %s: %w", key, err)
			event[key+"_capture_error"] = err.Error()
			delete(event, key)
			continue
		}
		encoded = sanitizeDebugPayload(encoded)
		if !isDebugPayloadField(key) && len(encoded) <= debugInlinePayloadMaxBytes {
			continue
		}
		metadata := debugPayloadMetadata(event, stream, key)
		ref, err := sink.payloadStore(dir).Put(stream+"."+key, encoded, metadata)
		if err != nil {
			externalizeErr = err
			event[key+"_capture_error"] = err.Error()
		} else {
			event[key+"_ref"] = ref
		}
		event[key+"_byte_len"] = len(encoded)
		event[key+"_preview"] = debugPayloadPreview(encoded)
		delete(event, key)
	}
	return externalizeErr
}

func (sink *debugSink) eventWriter(dir string, stream string) *logsink.RotatingFile {
	key := debugWriterKey(dir, stream)
	resourceKey := "event:" + key
	if writer := sink.writers[key]; writer != nil {
		sink.touchResource(resourceKey)
		return writer
	}
	sink.ensureResourceCapacity()
	writer := logsink.NewRotatingFile(filepath.Join(dir, filepath.FromSlash(stream)), logsink.RotationConfig{
		Prefix:        "event",
		Extension:     ".jsonl",
		MaxBytes:      debugEventSegmentMaxBytes,
		MaxFiles:      debugEventMaxFiles,
		MaxTotalBytes: debugEventMaxTotalBytes,
		MaxAge:        debugLogMaxAge,
	})
	sink.writers[key] = writer
	sink.touchResource(resourceKey)
	return writer
}

func (sink *debugSink) payloadStore(dir string) *logsink.PayloadPackStore {
	resourceKey := "payload:" + dir
	if store := sink.payloads[dir]; store != nil {
		sink.touchResource(resourceKey)
		return store
	}
	sink.ensureResourceCapacity()
	store := logsink.NewPayloadPackStore(dir, logsink.RotationConfig{
		Prefix:        "pack",
		Extension:     ".jsonl",
		MaxBytes:      debugPayloadPackMaxBytes,
		MaxFiles:      debugPayloadPackMaxFiles,
		MaxTotalBytes: debugPayloadMaxTotalBytes,
		MaxAge:        debugLogMaxAge,
	})
	sink.payloads[dir] = store
	sink.touchResource(resourceKey)
	return store
}

func (sink *debugSink) touchResource(key string) {
	if sink.lastUsed == nil {
		sink.lastUsed = make(map[string]time.Time)
	}
	sink.lastUsed[key] = time.Now()
}

func (sink *debugSink) ensureResourceCapacity() {
	if len(sink.writers)+len(sink.payloads) < debugMaxOpenResources {
		return
	}
	oldestKey := ""
	var oldestTime time.Time
	for key, usedAt := range sink.lastUsed {
		if oldestKey == "" || usedAt.Before(oldestTime) {
			oldestKey = key
			oldestTime = usedAt
		}
	}
	if oldestKey != "" {
		sink.closeResource(oldestKey)
	}
}

func (sink *debugSink) closeIdleResources(now time.Time) {
	for key, usedAt := range sink.lastUsed {
		if now.Sub(usedAt) >= debugResourceIdleTimeout {
			sink.closeResource(key)
		}
	}
}

func (sink *debugSink) closeResource(resourceKey string) {
	switch {
	case strings.HasPrefix(resourceKey, "event:"):
		key := strings.TrimPrefix(resourceKey, "event:")
		if writer := sink.writers[key]; writer != nil {
			_ = writer.Close()
		}
		delete(sink.writers, key)
	case strings.HasPrefix(resourceKey, "payload:"):
		key := strings.TrimPrefix(resourceKey, "payload:")
		if store := sink.payloads[key]; store != nil {
			_ = store.Close()
		}
		delete(sink.payloads, key)
	}
	delete(sink.lastUsed, resourceKey)
}

func (sink *debugSink) warn(err error, reason string) {
	if sink == nil || err == nil {
		return
	}
	now := time.Now().UnixNano()
	last := sink.lastWarning.Load()
	if last > 0 && time.Duration(now-last) < debugWarningInterval {
		return
	}
	if !sink.lastWarning.CompareAndSwap(last, now) {
		return
	}
	dropped := sink.dropped.Load()
	go logger.Warn("会话调试日志写入已隔离", "reason", reason, "error", err, "dropped", dropped)
}

func debugWriterKey(dir string, stream string) string {
	return strings.TrimSpace(dir) + "\x00" + strings.TrimSpace(stream)
}

func debugStreamName(filename string) string {
	switch strings.TrimSpace(filename) {
	case "bidi.raw.jsonl":
		return "bidi/raw"
	case "bidi.decoded.jsonl":
		return "bidi/decoded"
	case "runtime.jsonl":
		return "runtime"
	case "runsse.jsonl":
		return "runsse"
	case "provider.jsonl":
		return "provider"
	default:
		name := strings.TrimSuffix(filepath.Base(strings.TrimSpace(filename)), filepath.Ext(filename))
		if name == "" || name == "." {
			return "unknown"
		}
		return sanitizeArtifactName(name)
	}
}

func cloneDebugEvent(source map[string]any) map[string]any {
	cloned := make(map[string]any, len(source)+1)
	for key, value := range source {
		cloned[key] = value
	}
	return cloned
}

func encodeDebugField(value any) ([]byte, error) {
	switch typed := value.(type) {
	case debugFieldEncoder:
		return typed()
	case string:
		return []byte(typed), nil
	default:
		return json.Marshal(value)
	}
}

func sanitizeDebugPayload(payload []byte) []byte {
	var decoded any
	if err := json.Unmarshal(payload, &decoded); err == nil {
		if sanitized, marshalErr := json.Marshal(observability.Sanitize(decoded)); marshalErr == nil {
			return sanitized
		}
	}
	return []byte(observability.SanitizeText(string(payload)))
}

func isDebugPayloadField(key string) bool {
	switch strings.TrimSpace(key) {
	case "data_hex", "message", "intent", "payload", "raw_chunk":
		return true
	default:
		return false
	}
}

func debugPayloadMetadata(event map[string]any, stream string, field string) map[string]string {
	return map[string]string{
		"stream":          strings.TrimSpace(stream),
		"field":           strings.TrimSpace(field),
		"request_id":      strings.TrimSpace(readStringValue(event["request_id"])),
		"conversation_id": strings.TrimSpace(readStringValue(event["conversation_id"])),
		"event":           strings.TrimSpace(readStringValue(event["event"])),
	}
}

func debugPayloadPreview(payload []byte) string {
	if len(payload) <= debugPayloadPreviewBytes {
		return string(payload)
	}
	return string(payload[:debugPayloadPreviewBytes]) + "..."
}
