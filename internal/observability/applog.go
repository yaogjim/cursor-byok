package observability

import (
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"

	"cursor/internal/logsink"
)

const (
	appLogDirname         = "app"
	appLogPrefix          = "app"
	appLogSegmentMaxBytes = int64(10 << 20)
	appLogMaxFiles        = 10
	appLogMaxTotalBytes   = int64(100 << 20)
	appLogMaxAge          = 14 * 24 * time.Hour
)

// appLogSink is the ordinary application log file. Every byte it writes first
// passes the shared normal-partition admission so app logs, traces and payloads
// all count against the same B−D budget instead of a separate cap.
type appLogSink struct {
	mu       sync.Mutex
	budget   *logBudget
	writer   *logsink.RotatingFile
	closed   bool
	degraded bool
	dropped  uint64
	lastErr  string
}

func openAppLogSink(root string, budget *logBudget) (*appLogSink, error) {
	dir := filepath.Join(root, appLogDirname)
	if err := ensurePrivateDir(dir); err != nil {
		return nil, err
	}
	writer := logsink.NewRotatingFile(dir, logsink.RotationConfig{
		Prefix:        appLogPrefix,
		Extension:     ".log",
		MaxBytes:      appLogSegmentMaxBytes,
		MaxFiles:      appLogMaxFiles,
		MaxTotalBytes: appLogMaxTotalBytes,
		MaxAge:        appLogMaxAge,
	})
	return &appLogSink{budget: budget, writer: writer}, nil
}

// write admits the payload against the shared normal budget, then appends it.
// A quota rejection is dropped without error so a full disk never turns into a
// recursive logging failure; the caller (the logger gate) still sees a
// successful write.
func (sink *appLogSink) write(payload []byte) (int, error) {
	if sink == nil || sink.writer == nil {
		return len(payload), nil
	}
	sink.mu.Lock()
	defer sink.mu.Unlock()
	if sink.closed {
		return len(payload), nil
	}
	budget := sink.budget
	if budget == nil {
		result, err := sink.writer.Append(payload)
		return int(result.Length), err
	}
	budget.mu.Lock()
	// Keep the same eventReserveBytes headroom as trace writes so ordinary app
	// log bytes can never consume the room reserved for session metadata.
	if !budget.admitNormalLocked(int64(len(payload)), eventReserveBytes, "") {
		budget.mu.Unlock()
		sink.markDroppedLocked("app_log_quota_exceeded")
		return len(payload), nil
	}
	result, err := sink.writer.Append(payload)
	if result.Path != "" {
		budget.activeAppPath = filepath.Clean(result.Path)
	}
	if result.Length > 0 {
		budget.normalUsage += result.Length
		budget.usageKnown = true
	}
	budget.mu.Unlock()
	if err != nil {
		sink.markDroppedLocked("app_log_write_failed")
		return int(result.Length), err
	}
	return int(result.Length), nil
}

func (sink *appLogSink) markDroppedLocked(category string) {
	sink.degraded = true
	sink.dropped++
	if sink.lastErr == category {
		return
	}
	sink.lastErr = category
	_, _ = fmt.Fprintf(os.Stderr, "[observability] app log sink dropped writes: %s\n", category)
}

func (sink *appLogSink) close() error {
	if sink == nil {
		return nil
	}
	sink.mu.Lock()
	defer sink.mu.Unlock()
	sink.closed = true
	if sink.writer == nil {
		return nil
	}
	return sink.writer.Close()
}

func (sink *appLogSink) status() diagnosticStatus {
	if sink == nil {
		return diagnosticStatus{}
	}
	sink.mu.Lock()
	defer sink.mu.Unlock()
	return diagnosticStatus{
		enabled:  sink.writer != nil && !sink.closed,
		degraded: sink.degraded,
		dropped:  sink.dropped,
		lastErr:  sink.lastErr,
	}
}
