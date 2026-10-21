package observability

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
)

const (
	manifestFilename  = "manifest.json"
	eventsFilename    = "events.jsonl"
	payloadsDirname   = "payloads"
	tracesDirname     = "traces"
	eventReserveBytes = int64(1024 * 1024)
	// minimumSupportedSchemaVersion mirrors the log-analyzer contract, which
	// reads schema versions 1..SchemaVersion. Older but known manifests must
	// stay reclaimable; only truly unknown versions are protected.
	minimumSupportedSchemaVersion = 1
)

var errSessionQuotaExceeded = errors.New("observability session quota exceeded")

type sessionWriter struct {
	mu         sync.Mutex
	root       string
	dir        string
	sessionID  string
	settings   Settings
	eventsFile *os.File
	manifest   Manifest
	payloadSeq uint64
	budget     *logBudget
	// manifestWriteError records the last failed manifest persistence in memory
	// only. The on-disk manifest is left untouched on failure; status readers
	// surface this value so a failed rewrite is visible without a new schema.
	manifestWriteError string
}

type sessionInfo struct {
	path      string
	startedAt time.Time
	size      int64
	closed    bool
	mode      string
}

func openSession(root string, settings Settings, budget *logBudget) (*sessionWriter, error) {
	root = filepath.Clean(strings.TrimSpace(root))
	if root == "." || root == "" {
		return nil, errors.New("observability root is required")
	}
	settings = normalizeSettings(settings)
	if budget == nil {
		budget = newLogBudget(root, settings)
	}
	tracesRoot := filepath.Join(root, tracesDirname)
	if err := ensurePrivateDir(root); err != nil {
		return nil, err
	}
	if err := ensurePrivateDir(tracesRoot); err != nil {
		return nil, err
	}
	if _, err := cleanupClosedSessions(root, settings, 0); err != nil {
		return nil, err
	}

	now := time.Now().UTC()
	sessionID := now.Format("20060102T150405.000000000Z") + "-" + randomID(6)
	dir := filepath.Join(tracesRoot, sessionID)
	if err := os.Mkdir(dir, 0o700); err != nil {
		return nil, err
	}
	if err := os.Chmod(dir, 0o700); err != nil {
		return nil, err
	}
	eventsFile, err := os.OpenFile(filepath.Join(dir, eventsFilename), os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		_ = os.RemoveAll(dir)
		return nil, err
	}
	if err := eventsFile.Chmod(0o600); err != nil {
		_ = eventsFile.Close()
		_ = os.RemoveAll(dir)
		return nil, err
	}
	if settings.Mode == ModeFull {
		if err := ensurePrivateDir(filepath.Join(dir, payloadsDirname)); err != nil {
			_ = eventsFile.Close()
			_ = os.RemoveAll(dir)
			return nil, err
		}
	}
	writer := &sessionWriter{
		root:       root,
		dir:        dir,
		sessionID:  sessionID,
		settings:   settings,
		eventsFile: eventsFile,
		budget:     budget,
		manifest: Manifest{
			SchemaVersion:     SchemaVersion,
			AppSessionID:      sessionID,
			Mode:              settings.Mode,
			Status:            "open",
			StartedAt:         now,
			SourceKind:        settings.Metadata.SourceKind,
			AppVersion:        settings.Metadata.AppVersion,
			BuildID:           settings.Metadata.BuildID,
			Platform:          settings.Metadata.Platform,
			ConfigFingerprint: settings.Metadata.ConfigFingerprint,
		},
	}
	if err := writer.budget.initialize(); err != nil {
		_ = writer.close("open_failed")
		_ = os.RemoveAll(dir)
		return nil, err
	}
	if err := writer.writeManifest(); err != nil {
		_ = writer.close("open_failed")
		_ = os.RemoveAll(dir)
		return nil, err
	}
	return writer, nil
}

func (writer *sessionWriter) appendEvent(event Event) error {
	if writer == nil {
		return errors.New("observability session is closed")
	}
	writer.mu.Lock()
	defer writer.mu.Unlock()
	if writer.eventsFile == nil {
		return errors.New("observability session is closed")
	}
	payload, err := json.Marshal(event)
	if err != nil {
		return err
	}
	payload = append(payload, '\n')
	writer.budget.mu.Lock()
	defer writer.budget.mu.Unlock()
	// Ordinary event bytes leave the same eventReserveBytes headroom that
	// payload writes already keep, so the normal partition can never be filled
	// to the point where the terminal manifest rewrite is denied.
	if !writer.budget.admitNormalLocked(int64(len(payload)), eventReserveBytes, writer.dir) {
		return errSessionQuotaExceeded
	}
	written, err := writer.eventsFile.Write(payload)
	if written > 0 {
		writer.budget.normalUsage += int64(written)
		writer.budget.usageKnown = true
	}
	return err
}

func (writer *sessionWriter) appendPayload(payload Payload, timestamp time.Time) (string, error) {
	if writer == nil {
		return "", nil
	}
	writer.mu.Lock()
	defer writer.mu.Unlock()
	if writer.settings.Mode != ModeFull {
		return "", nil
	}
	writer.payloadSeq++
	filename := fmt.Sprintf("%09d-%s.json", writer.payloadSeq, sanitizeFilename(payload.Name))
	relativePath := filepath.ToSlash(filepath.Join(payloadsDirname, filename))
	envelope := map[string]any{
		"schema_version": SchemaVersion,
		"timestamp":      timestamp,
		"content_type":   strings.TrimSpace(payload.ContentType),
		"name":           strings.TrimSpace(payload.Name),
		"data":           Sanitize(payload.Data),
	}
	encoded, err := json.Marshal(envelope)
	if err != nil {
		return "", err
	}
	encoded = append(encoded, '\n')
	writer.budget.mu.Lock()
	if !writer.budget.admitNormalLocked(int64(len(encoded)), eventReserveBytes, writer.dir) {
		writer.budget.mu.Unlock()
		return "", errSessionQuotaExceeded
	}
	path := filepath.Join(writer.dir, filepath.FromSlash(relativePath))
	file, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		writer.budget.mu.Unlock()
		return "", err
	}
	if err := file.Chmod(0o600); err != nil {
		writer.budget.usageKnown = false
		writer.budget.mu.Unlock()
		_ = file.Close()
		_ = os.Remove(path)
		return "", err
	}
	written, writeErr := file.Write(encoded)
	closeErr := file.Close()
	if writeErr != nil {
		// The failed cleanup may have left bytes the cached counter does not
		// know about, so force the next admission to re-scan.
		writer.budget.usageKnown = false
		writer.budget.mu.Unlock()
		_ = os.Remove(path)
		return "", writeErr
	}
	if closeErr != nil {
		writer.budget.usageKnown = false
		writer.budget.mu.Unlock()
		_ = os.Remove(path)
		return "", closeErr
	}
	if written > 0 {
		writer.budget.normalUsage += int64(written)
		writer.budget.usageKnown = true
	}
	writer.budget.mu.Unlock()
	return relativePath, nil
}

func (writer *sessionWriter) markDegraded(dropped uint64, lastError string) {
	if writer == nil {
		return
	}
	writer.mu.Lock()
	defer writer.mu.Unlock()
	writer.manifest.PayloadDegraded = true
	writer.manifest.DroppedEvents = dropped
	writer.manifest.LastError = strings.TrimSpace(lastError)
	_ = writer.writeManifestUnlocked()
}

func (writer *sessionWriter) updateDropped(dropped uint64) {
	if writer == nil {
		return
	}
	writer.mu.Lock()
	defer writer.mu.Unlock()
	if writer.manifest.DroppedEvents == dropped {
		return
	}
	writer.manifest.DroppedEvents = dropped
	_ = writer.writeManifestUnlocked()
}

func (writer *sessionWriter) close(status string) error {
	if writer == nil {
		return nil
	}
	writer.mu.Lock()
	defer writer.mu.Unlock()
	var closeErr error
	if writer.eventsFile != nil {
		closeErr = writer.eventsFile.Close()
		writer.eventsFile = nil
	}
	closedAt := time.Now().UTC()
	writer.manifest.Status = firstNonEmpty(status, "closed")
	writer.manifest.ClosedAt = &closedAt
	// A denied terminal manifest is returned instead of swallowed so the
	// recorder/controller can report it: a genuinely full, protected disk leaves
	// the trace in "open" and must not hide that from the caller. The manifest
	// admission still runs against the same hard cap, so this never bypasses it.
	return errors.Join(closeErr, writer.writeManifestUnlocked())
}

func (writer *sessionWriter) writeManifest() error {
	if writer == nil {
		return errors.New("observability session is not initialized")
	}
	writer.mu.Lock()
	defer writer.mu.Unlock()
	return writer.writeManifestUnlocked()
}

func (writer *sessionWriter) writeManifestUnlocked() error {
	if writer == nil || strings.TrimSpace(writer.dir) == "" {
		return errors.New("observability session is not initialized")
	}
	payload, err := json.MarshalIndent(writer.manifest, "", "  ")
	if err != nil {
		return err
	}
	payload = append(payload, '\n')
	path := filepath.Join(writer.dir, manifestFilename)
	tempPath := path + ".tmp"
	write := func() error {
		if err := os.WriteFile(tempPath, payload, 0o600); err != nil {
			// A failed write can leave partial temp bytes behind; drop them so
			// they do not linger as untracked usage.
			return errors.Join(err, removeTempManifest(tempPath))
		}
		if err := os.Chmod(tempPath, 0o600); err != nil {
			return errors.Join(err, removeTempManifest(tempPath))
		}
		if err := os.Rename(tempPath, path); err != nil {
			return errors.Join(err, removeTempManifest(tempPath))
		}
		return nil
	}
	budget := writer.budget
	if budget == nil {
		err := write()
		writer.setManifestWriteError(err)
		return err
	}
	// Manifest bytes belong to the managed normal partition. Admission counts
	// the full incoming payload because the previous manifest and the temp file
	// coexist until the atomic rename. A denied admission leaves the previous
	// good manifest untouched rather than writing over the limit, and the
	// failure is recorded in memory for status readers.
	budget.mu.Lock()
	defer budget.mu.Unlock()
	previous := int64(0)
	if info, statErr := os.Stat(path); statErr == nil {
		previous = info.Size()
	}
	if !budget.admitNormalLocked(int64(len(payload)), 0, writer.dir) {
		writer.setManifestWriteError(errSessionQuotaExceeded)
		return errSessionQuotaExceeded
	}
	if err := write(); err != nil {
		// The in-memory usage no longer reflects disk: a partial temp file or a
		// failed cleanup may have left bytes behind. Force the next admission to
		// re-scan instead of trusting the cached counter.
		budget.usageKnown = false
		writer.setManifestWriteError(err)
		return err
	}
	budget.normalUsage += int64(len(payload)) - previous
	if budget.normalUsage < 0 {
		budget.normalUsage = 0
	}
	budget.usageKnown = true
	writer.setManifestWriteError(nil)
	return nil
}

// setManifestWriteError records (or clears) the last manifest persistence
// failure. Callers must hold writer.mu; the value never reaches the on-disk
// manifest schema.
func (writer *sessionWriter) setManifestWriteError(err error) {
	if err == nil {
		writer.manifestWriteError = ""
		return
	}
	if errors.Is(err, errSessionQuotaExceeded) {
		writer.manifestWriteError = "manifest_quota_exceeded"
		return
	}
	writer.manifestWriteError = "manifest_write_failed"
}

func (writer *sessionWriter) manifestError() string {
	if writer == nil {
		return ""
	}
	writer.mu.Lock()
	defer writer.mu.Unlock()
	return writer.manifestWriteError
}

func removeTempManifest(path string) error {
	if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
		return err
	}
	return nil
}

// reclaimableManifest reports whether a closed trace is safe to reclaim. Only
// fully identified manifests with a supported schema version and known mode are
// eligible: unknown schema versions, unknown modes, and invalid identities are
// protected so a future format is never silently deleted by an older build.
func reclaimableManifest(dirName string, manifest Manifest) bool {
	if manifest.Status != "closed" {
		return false
	}
	if manifest.SchemaVersion < minimumSupportedSchemaVersion || manifest.SchemaVersion > SchemaVersion {
		return false
	}
	if manifest.Mode != ModeFull && manifest.Mode != ModeBasic {
		return false
	}
	if strings.TrimSpace(manifest.AppSessionID) != dirName {
		return false
	}
	if manifest.StartedAt.IsZero() {
		return false
	}
	return true
}

type CleanupResult struct {
	RemovedSessions int   `json:"removed_sessions"`
	FreedBytes      int64 `json:"freed_bytes"`
}

var cleanupAllClosedSessionsMu sync.Mutex

func CleanupAllClosedSessions(root string) (CleanupResult, error) {
	root = strings.TrimSpace(root)
	if root == "" {
		return CleanupResult{}, errors.New("observability root is required")
	}
	root = filepath.Clean(root)
	if root == "." {
		return CleanupResult{}, errors.New("observability root is required")
	}

	cleanupAllClosedSessionsMu.Lock()
	defer cleanupAllClosedSessionsMu.Unlock()

	tracesRoot := filepath.Join(root, tracesDirname)
	entries, err := os.ReadDir(tracesRoot)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return CleanupResult{}, nil
		}
		return CleanupResult{}, err
	}
	var result CleanupResult
	for _, entry := range entries {
		if !entry.IsDir() || entry.Type()&os.ModeSymlink != 0 {
			continue
		}
		path := filepath.Join(tracesRoot, entry.Name())
		manifest, readErr := readManifest(filepath.Join(path, manifestFilename))
		if readErr != nil || !reclaimableManifest(entry.Name(), manifest) {
			continue
		}
		size, sizeErr := directorySize(path)
		if sizeErr != nil {
			return result, sizeErr
		}
		if removeErr := os.RemoveAll(path); removeErr != nil {
			return result, removeErr
		}
		result.RemovedSessions++
		result.FreedBytes += size
	}
	return result, nil
}

func CleanupClosedSessions(root string, settings Settings) error {
	_, err := cleanupClosedSessions(root, normalizeSettings(settings), 0)
	return err
}

func cleanupClosedSessions(root string, settings Settings, reserveBytes int64) (int64, error) {
	root = filepath.Clean(strings.TrimSpace(root))
	tracesRoot := filepath.Join(root, tracesDirname)
	entries, err := os.ReadDir(tracesRoot)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return 0, nil
		}
		return 0, err
	}
	cutoff := time.Now().UTC().Add(-time.Duration(settings.RetentionDays) * 24 * time.Hour)
	sessions := make([]sessionInfo, 0, len(entries))
	for _, entry := range entries {
		if !entry.IsDir() || entry.Type()&os.ModeSymlink != 0 {
			continue
		}
		path := filepath.Join(tracesRoot, entry.Name())
		manifest, readErr := readManifest(filepath.Join(path, manifestFilename))
		if readErr != nil {
			continue
		}
		size, sizeErr := directorySize(path)
		if sizeErr != nil {
			return 0, sizeErr
		}
		closed := manifest.Status == "closed"
		if reclaimableManifest(entry.Name(), manifest) && manifest.StartedAt.Before(cutoff) {
			if removeErr := os.RemoveAll(path); removeErr != nil {
				return 0, removeErr
			}
			continue
		}
		sessions = append(sessions, sessionInfo{
			path:      path,
			startedAt: manifest.StartedAt,
			size:      size,
			closed:    closed,
			mode:      manifest.Mode,
		})
	}
	usage, err := directorySize(root)
	if err != nil {
		return 0, err
	}
	maxBytes := int64(settings.MaxDiskMB) * 1024 * 1024
	if maxBytes <= 0 || usage+reserveBytes <= maxBytes {
		return usage, nil
	}
	sort.Slice(sessions, func(left int, right int) bool {
		return sessions[left].startedAt.Before(sessions[right].startedAt)
	})
	for _, session := range sessions {
		if !session.closed || session.mode != ModeFull || usage+reserveBytes <= maxBytes {
			continue
		}
		if err := os.RemoveAll(session.path); err != nil {
			return usage, err
		}
		usage -= session.size
	}
	return usage, nil
}

func readManifest(path string) (Manifest, error) {
	payload, err := os.ReadFile(path)
	if err != nil {
		return Manifest{}, err
	}
	var manifest Manifest
	if err := json.Unmarshal(payload, &manifest); err != nil {
		return Manifest{}, err
	}
	return manifest, nil
}

func directorySize(root string) (int64, error) {
	var total int64
	err := filepath.WalkDir(root, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			// Files can be rotated/renamed concurrently (for example a manifest
			// .tmp file); a transient disappearance must not discard the bytes
			// counted so far.
			if errors.Is(walkErr, os.ErrNotExist) {
				return nil
			}
			return walkErr
		}
		if entry.Type()&os.ModeSymlink != 0 {
			return nil
		}
		if !entry.Type().IsRegular() {
			return nil
		}
		info, err := entry.Info()
		if err != nil {
			if errors.Is(err, os.ErrNotExist) {
				return nil
			}
			return err
		}
		total += info.Size()
		return nil
	})
	if errors.Is(err, os.ErrNotExist) {
		return total, nil
	}
	return total, err
}

func ensurePrivateDir(path string) error {
	if err := os.MkdirAll(path, 0o700); err != nil {
		return err
	}
	return os.Chmod(path, 0o700)
}

func sanitizeFilename(value string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return "payload"
	}
	var builder strings.Builder
	for _, character := range value {
		switch {
		case character >= 'a' && character <= 'z':
			builder.WriteRune(character)
		case character >= 'A' && character <= 'Z':
			builder.WriteRune(character)
		case character >= '0' && character <= '9':
			builder.WriteRune(character)
		case character == '-' || character == '_':
			builder.WriteRune(character)
		default:
			builder.WriteByte('_')
		}
		if builder.Len() >= 64 {
			break
		}
	}
	if builder.Len() == 0 {
		return "payload"
	}
	return builder.String()
}

func normalizeSettings(settings Settings) Settings {
	switch strings.ToLower(strings.TrimSpace(settings.Mode)) {
	case ModeOff:
		settings.Mode = ModeOff
	case ModeFull:
		settings.Mode = ModeFull
	default:
		settings.Mode = ModeBasic
	}
	if settings.RetentionDays <= 0 {
		settings.RetentionDays = 7
	} else if settings.RetentionDays > 90 {
		settings.RetentionDays = 90
	}
	if settings.MaxDiskMB <= 0 {
		settings.MaxDiskMB = 1024
	} else if settings.MaxDiskMB < 64 {
		settings.MaxDiskMB = 64
	} else if settings.MaxDiskMB > 10240 {
		settings.MaxDiskMB = 10240
	}
	if settings.QueueSize <= 0 {
		settings.QueueSize = 1024
	} else if settings.QueueSize > 65536 {
		settings.QueueSize = 65536
	}
	settings.Metadata.SourceKind = strings.ToLower(strings.TrimSpace(settings.Metadata.SourceKind))
	if settings.Metadata.SourceKind == "" {
		settings.Metadata.SourceKind = "client"
	}
	settings.Metadata.AppVersion = strings.TrimSpace(settings.Metadata.AppVersion)
	settings.Metadata.BuildID = strings.TrimSpace(settings.Metadata.BuildID)
	settings.Metadata.Platform = strings.TrimSpace(settings.Metadata.Platform)
	settings.Metadata.ConfigFingerprint = strings.TrimSpace(settings.Metadata.ConfigFingerprint)
	settings.RuntimeFingerprint = strings.TrimSpace(settings.RuntimeFingerprint)
	return settings
}
