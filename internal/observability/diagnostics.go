package observability

import (
	"encoding/json"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"cursor/internal/logsink"
)

const (
	diagnosticsDirname   = "diagnostics"
	diagnosticsPrefix    = "diagnostics"
	diagnosticsExtension = ".jsonl"
)

// diagnosticSink keeps a bounded, independently rotating copy of WARN/ERROR
// events. It reuses logsink.RotatingFile and never recurses into the trace or
// human sinks, so a failure here cannot disable capture or business calls.
type diagnosticSink struct {
	mu       sync.Mutex
	budget   *logBudget
	closed   bool
	degraded bool
	dropped  uint64
	lastErr  string
	writer   *logsink.RotatingFile
}

// diagnosticPieceBytes caps a single diagnostics shard at a quarter of the
// reserve (never more than 10MiB) so a rotation cycle can still hold several
// shards inside D.
func diagnosticPieceBytes(total int64) int64 {
	if total <= 0 {
		return 0
	}
	piece := total / 4
	const maxPiece = int64(10 << 20)
	if piece > maxPiece {
		piece = maxPiece
	}
	if piece < 1 {
		piece = 1
	}
	return piece
}

func openDiagnosticSink(root string, budget *logBudget, retention time.Duration, maxBytes int64) (*diagnosticSink, error) {
	if maxBytes <= 0 {
		return &diagnosticSink{budget: budget}, nil
	}
	dir := filepath.Join(root, diagnosticsDirname)
	if err := ensurePrivateDir(dir); err != nil {
		return nil, err
	}
	writer := logsink.NewRotatingFile(dir, logsink.RotationConfig{
		Prefix:        diagnosticsPrefix,
		Extension:     diagnosticsExtension,
		MaxBytes:      diagnosticPieceBytes(maxBytes),
		MaxTotalBytes: maxBytes,
		MaxAge:        retention,
	})
	sink := &diagnosticSink{budget: budget, writer: writer}
	if budget != nil {
		budget.mu.Lock()
		budget.registerDiagnosticWriterLocked(writer)
		budget.mu.Unlock()
	}
	return sink, nil
}

func (sink *diagnosticSink) write(event Event) {
	if sink == nil {
		return
	}
	severity := strings.ToLower(strings.TrimSpace(event.Severity))
	if severity != SeverityWarning && severity != SeverityError {
		return
	}
	sink.mu.Lock()
	defer sink.mu.Unlock()
	if sink.closed {
		return
	}
	if sink.writer == nil {
		sink.markDegradedLocked("diagnostic_unavailable")
		return
	}
	event.PayloadRef = ""
	payload, err := json.Marshal(event)
	if err != nil {
		sink.markDegradedLocked("diagnostic_marshal_failed")
		return
	}
	payload = append(payload, '\n')
	var appendErr error
	if sink.budget != nil {
		// Serialize the append against the shared budget so an old and a new
		// diagnostic writer cannot each retain the full reserve. The effective
		// directory size is the single source of truth: a second writer sees
		// the first writer's bytes, so the diagnostics partition never grows
		// past D even while two writers overlap. Because the reserve gate runs
		// before Append, sealed shards are reclaimed first; otherwise rotation
		// could never run again once the directory is within one record of D.
		budget := sink.budget
		incoming := int64(len(payload))
		budget.mu.Lock()
		if err := budget.refreshDiagnosticUsageLocked(); err != nil {
			budget.mu.Unlock()
			sink.markDegradedLocked("diagnostic_usage_scan_failed")
			return
		}
		if budget.diagnosticLimit > 0 && incoming > budget.diagnosticLimit {
			// A single record larger than the whole reserve can never fit, so
			// reject it before touching any sealed shard.
			budget.mu.Unlock()
			sink.markDegradedLocked("diagnostic_reserve_exceeded")
			return
		}
		if budget.diagnosticLimit > 0 && budget.diagnosticUsage+incoming > budget.diagnosticLimit {
			if err := budget.reclaimSealedDiagnosticsLocked(incoming); err != nil {
				budget.mu.Unlock()
				sink.markDegradedLocked("diagnostic_reclaim_failed")
				return
			}
		}
		if budget.diagnosticLimit > 0 && budget.diagnosticUsage+incoming > budget.diagnosticLimit {
			// No sealed shard could free room: the active shard itself may be
			// oversized (a single record can exceed MaxBytes) or a reconfigure
			// may have shrunk D below it. Seal our own shard once so it becomes
			// reclaimable, then retry; this runs at most once so cleanup cannot
			// loop on an oversized record.
			if err := budget.sealOwnActiveDiagnosticsLocked(sink.writer, incoming); err != nil {
				budget.mu.Unlock()
				sink.markDegradedLocked("diagnostic_reclaim_failed")
				return
			}
		}
		if budget.diagnosticLimit > 0 && budget.diagnosticUsage+incoming > budget.diagnosticLimit {
			budget.mu.Unlock()
			sink.markDegradedLocked("diagnostic_reserve_exceeded")
			return
		}
		sink.writer.SetProtectedPaths(budget.peerActiveDiagnosticPathsLocked(sink.writer))
		_, err := sink.writer.Append(payload)
		appendErr = err
		// CurrentPath reflects the shard actually held after Append, including
		// when rotation replaced it; result.Path can be empty on an early
		// failure while the previous handle is still open.
		budget.updateDiagnosticWriterPathLocked(sink.writer, sink.writer.CurrentPath())
		_ = budget.refreshDiagnosticUsageLocked()
		budget.mu.Unlock()
	} else {
		_, appendErr = sink.writer.Append(payload)
	}
	if appendErr != nil {
		sink.markDegradedLocked("diagnostic_write_failed")
	}
}

func (sink *diagnosticSink) markDegradedLocked(category string) {
	sink.degraded = true
	sink.dropped++
	sink.lastErr = category
}

func (sink *diagnosticSink) markUnavailable(category string) {
	if sink == nil {
		return
	}
	sink.mu.Lock()
	defer sink.mu.Unlock()
	sink.degraded = true
	sink.lastErr = category
}

func (sink *diagnosticSink) close() error {
	if sink == nil {
		return nil
	}
	sink.mu.Lock()
	defer sink.mu.Unlock()
	sink.closed = true
	if sink.writer == nil {
		return nil
	}
	if sink.budget == nil {
		return sink.writer.Close()
	}
	// Close the handle while still registered under the shared budget lock,
	// then unregister, so a peer writer cannot reclaim this shard in the window
	// between unregistering and actually closing the file. Lock order stays
	// sink → budget → writer.
	sink.budget.mu.Lock()
	defer sink.budget.mu.Unlock()
	err := sink.writer.Close()
	sink.budget.unregisterDiagnosticWriterLocked(sink.writer)
	return err
}

type diagnosticStatus struct {
	enabled  bool
	degraded bool
	dropped  uint64
	lastErr  string
}

func (sink *diagnosticSink) status() diagnosticStatus {
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
