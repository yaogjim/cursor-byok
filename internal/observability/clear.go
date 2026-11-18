package observability

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"cursor/internal/logsink"
)

// clearCloseTimeout bounds how long a clear waits for the previous writers to
// stop. If they do not stop, nothing is deleted: removing files a writer still
// holds would let it recreate content behind an apparently successful clear.
const clearCloseTimeout = 10 * time.Second

// ClearResult reports what a "clear all logs" operation removed. FailedItems
// counts managed items (sessions or shards) that could not be removed, so a
// partial clear is never reported as a full one. Collecting reports whether
// capture was restored after the clear; LastError carries a fixed category when
// it was not.
type ClearResult struct {
	RemovedSessions int    `json:"removed_sessions"`
	RemovedShards   int    `json:"removed_shards"`
	FreedBytes      int64  `json:"freed_bytes"`
	FailedItems     int    `json:"failed_items"`
	NewSessionID    string `json:"new_session_id,omitempty"`
	NewSessionPath  string `json:"new_session_path,omitempty"`
	Collecting      bool   `json:"collecting"`
	LastError       string `json:"last_error,omitempty"`
}

// ClearStoredLogs deletes every managed log artifact under root: all trace
// session directories with a known manifest, managed app log shards and managed
// diagnostics shards. Unknown formats, unmanaged files and symlinks are kept.
//
// The caller must first stop every writer that holds files under root. The
// Controller path guarantees that by closing its recorder and app log sink
// before calling here. A structural failure (invalid root, unreadable traces
// directory) is returned as an error; individual removals that fail are counted
// in FailedItems so the rest of the clear still proceeds.
func ClearStoredLogs(root string) (ClearResult, error) {
	root = strings.TrimSpace(root)
	if root == "" {
		return ClearResult{}, errors.New("observability root is required")
	}
	root = filepath.Clean(root)
	if root == "." {
		return ClearResult{}, errors.New("observability root is required")
	}

	cleanupAllClosedSessionsMu.Lock()
	defer cleanupAllClosedSessionsMu.Unlock()

	// 与轮转、预算回收共享锁，避免删除尚在重命名的预留分片。
	budget := liveBudgetForRoot(root)
	if budget != nil {
		budget.mu.Lock()
		defer budget.mu.Unlock()
		defer func() { budget.usageKnown = false }()
	}

	if err := validateLogDirs(root); err != nil {
		return ClearResult{}, err
	}

	var result ClearResult
	tracesRoot := filepath.Join(root, tracesDirname)
	entries, err := os.ReadDir(tracesRoot)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return ClearResult{}, err
	}
	for _, entry := range entries {
		if !entry.IsDir() || entry.Type()&os.ModeSymlink != 0 {
			continue
		}
		path := filepath.Join(tracesRoot, entry.Name())
		manifest, readErr := readManifest(filepath.Join(path, manifestFilename))
		if readErr != nil || !knownManagedSession(entry.Name(), manifest) {
			continue
		}
		size, sizeErr := directorySize(path)
		if sizeErr != nil {
			result.FailedItems++
			fmt.Fprintf(os.Stderr, "[observability] clear: measure session failed: %v\n", sizeErr)
			continue
		}
		if removeErr := os.RemoveAll(path); removeErr != nil {
			result.FailedItems++
			fmt.Fprintf(os.Stderr, "[observability] clear: remove session failed: %v\n", removeErr)
			continue
		}
		result.RemovedSessions++
		result.FreedBytes += size
	}

	appStats, appErr := logsink.CleanupManagedShards(filepath.Join(root, appLogDirname), logsink.ManagedShardCleanupConfig{
		Prefix:     appLogPrefix,
		Extension:  ".log",
		KeepNewest: 0,
	})
	result.RemovedShards += appStats.Removed
	result.FreedBytes += appStats.FreedBytes
	if appErr != nil {
		result.FailedItems++
		fmt.Fprintf(os.Stderr, "[observability] clear: remove app shards failed: %v\n", appErr)
	}

	diagStats, diagErr := logsink.CleanupManagedShards(filepath.Join(root, diagnosticsDirname), logsink.ManagedShardCleanupConfig{
		Prefix:     diagnosticsPrefix,
		Extension:  diagnosticsExtension,
		KeepNewest: 0,
	})
	result.RemovedShards += diagStats.Removed
	result.FreedBytes += diagStats.FreedBytes
	if diagErr != nil {
		result.FailedItems++
		fmt.Fprintf(os.Stderr, "[observability] clear: remove diagnostics shards failed: %v\n", diagErr)
	}

	if budget != nil {
		budget.activeAppPath = ""
		budget.normalUsage = 0
		budget.diagnosticUsage = 0
		budget.usageKnown = false
	}
	return result, nil
}

type reopenOutcome struct {
	collecting  bool
	sessionID   string
	sessionPath string
	err         error
}

// ClearAllLogs removes every managed log artifact, including the currently
// active trace session, then reopens capture with the current settings. Writers
// are closed first and only a confirmed stop allows deletion; if they do not
// stop in time the clear is aborted and capture is restored.
func (controller *Controller) ClearAllLogs() (ClearResult, error) {
	if controller == nil {
		return ClearResult{}, errors.New("日志采集未初始化")
	}
	controller.updateMu.Lock()
	defer controller.updateMu.Unlock()

	controller.mu.RLock()
	closed := controller.closed
	settings := controller.settings
	controller.mu.RUnlock()
	if closed {
		return ClearResult{}, errors.New("日志采集已停止")
	}

	// Detach the writers first so no new capture reaches them while they drain.
	controller.mu.Lock()
	recorder := controller.recorder
	appLog := controller.appLog
	controller.recorder = nil
	controller.appLog = nil
	controller.mu.Unlock()

	stopped, closeErr := closeRecorderWithin(recorder, clearCloseTimeout)
	if !stopped {
		outcome := controller.reopenLogging(settings)
		result := ClearResult{Collecting: outcome.collecting, NewSessionID: outcome.sessionID, NewSessionPath: outcome.sessionPath}
		if outcome.err != nil {
			result.LastError = "collector_restart_failed"
		}
		return result, errors.New("日志写入未能在时限内停止，已取消清空并恢复采集")
	}
	if closeErr != nil {
		fmt.Fprintf(os.Stderr, "[observability] clear: close recorder failed: %v\n", closeErr)
	}
	if appLog != nil {
		if err := appLog.close(); err != nil {
			fmt.Fprintf(os.Stderr, "[observability] clear: close app log failed: %v\n", err)
		}
	}

	result, storedErr := ClearStoredLogs(controller.root)
	if storedErr != nil {
		outcome := controller.reopenLogging(settings)
		result.Collecting = outcome.collecting
		result.NewSessionID = outcome.sessionID
		result.NewSessionPath = outcome.sessionPath
		if outcome.err != nil {
			result.LastError = "collector_restart_failed"
		}
		return result, storedErr
	}

	outcome := controller.reopenLogging(settings)
	result.Collecting = outcome.collecting
	result.NewSessionID = outcome.sessionID
	result.NewSessionPath = outcome.sessionPath
	if outcome.err != nil {
		result.LastError = "collector_restart_failed"
		fmt.Fprintf(os.Stderr, "[observability] clear: reopen recorder failed: %v\n", outcome.err)
	}
	return result, nil
}

// reopenLogging installs fresh writers after a clear. Trace capture is reopened
// with the same settings (ModeOff stays off); the app log sink is always
// reopened because it is independent of the capture level.
func (controller *Controller) reopenLogging(settings Settings) reopenOutcome {
	var outcome reopenOutcome
	appLog, appErr := openAppLogSink(controller.root, controller.budget)
	if appErr != nil {
		appLog = &appLogSink{budget: controller.budget, degraded: true, lastErr: "app_log_unavailable"}
		fmt.Fprintf(os.Stderr, "[observability] clear: reopen app log failed: %v\n", appErr)
	}
	var recorder *Recorder
	if settings.Mode != ModeOff {
		var err error
		recorder, err = newRecorderWithBudget(controller.root, settings, controller.humanSink, controller.budget)
		if err != nil {
			outcome.err = err
		}
	}
	if recorder != nil {
		status := recorder.Status()
		outcome.collecting = true
		outcome.sessionID = status.SessionID
		outcome.sessionPath = status.SessionPath
	}
	controller.mu.Lock()
	controller.recorder = recorder
	controller.appLog = appLog
	controller.mu.Unlock()
	return outcome
}

// closeRecorderWithin reports whether the recorder actually stopped before the
// timeout. A nil error with stopped=false means the close is still in flight and
// the caller must not delete files it may still write.
func closeRecorderWithin(recorder *Recorder, timeout time.Duration) (bool, error) {
	if recorder == nil {
		return true, nil
	}
	done := make(chan error, 1)
	go func() {
		done <- recorder.Close()
	}()
	timer := time.NewTimer(timeout)
	defer timer.Stop()
	select {
	case err := <-done:
		return true, err
	case <-timer.C:
		return false, nil
	}
}
