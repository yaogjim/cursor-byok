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
)

var errSessionQuotaExceeded = errors.New("observability session quota exceeded")

type sessionWriter struct {
	mu          sync.Mutex
	root        string
	dir         string
	sessionID   string
	settings    Settings
	eventsFile  *os.File
	manifest    Manifest
	usageBytes  int64
	payloadSeq  uint64
	maxDiskByte int64
}

type sessionInfo struct {
	path      string
	startedAt time.Time
	size      int64
	closed    bool
	mode      string
}

func openSession(root string, settings Settings) (*sessionWriter, error) {
	root = filepath.Clean(strings.TrimSpace(root))
	if root == "." || root == "" {
		return nil, errors.New("observability root is required")
	}
	settings = normalizeSettings(settings)
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
		root:        root,
		dir:         dir,
		sessionID:   sessionID,
		settings:    settings,
		eventsFile:  eventsFile,
		maxDiskByte: int64(settings.MaxDiskMB) * 1024 * 1024,
		manifest: Manifest{
			SchemaVersion: SchemaVersion,
			AppSessionID:  sessionID,
			Mode:          settings.Mode,
			Status:        "open",
			StartedAt:     now,
		},
	}
	if writer.usageBytes, err = directorySize(root); err != nil {
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
	if !writer.canWrite(int64(len(payload)), 0) {
		return errSessionQuotaExceeded
	}
	written, err := writer.eventsFile.Write(payload)
	writer.usageBytes += int64(written)
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
	if !writer.canWritePayload(int64(len(encoded))) {
		return "", errSessionQuotaExceeded
	}
	path := filepath.Join(writer.dir, filepath.FromSlash(relativePath))
	file, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		return "", err
	}
	if err := file.Chmod(0o600); err != nil {
		_ = file.Close()
		_ = os.Remove(path)
		return "", err
	}
	written, writeErr := file.Write(encoded)
	closeErr := file.Close()
	if writeErr != nil {
		_ = os.Remove(path)
		return "", writeErr
	}
	if closeErr != nil {
		_ = os.Remove(path)
		return "", closeErr
	}
	writer.usageBytes += int64(written)
	return relativePath, nil
}

func (writer *sessionWriter) canWritePayload(additionalBytes int64) bool {
	return writer.canWrite(additionalBytes, eventReserveBytes)
}

func (writer *sessionWriter) canWrite(additionalBytes int64, reserveBytes int64) bool {
	if writer == nil || writer.maxDiskByte <= 0 {
		return false
	}
	requiredBytes := additionalBytes + reserveBytes
	if writer.usageBytes+requiredBytes <= writer.maxDiskByte {
		return true
	}
	usage, err := cleanupClosedSessions(writer.root, writer.settings, requiredBytes)
	if err != nil {
		return false
	}
	writer.usageBytes = usage
	return usage+requiredBytes <= writer.maxDiskByte
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
	if err := os.WriteFile(tempPath, payload, 0o600); err != nil {
		return err
	}
	if err := os.Chmod(tempPath, 0o600); err != nil {
		_ = os.Remove(tempPath)
		return err
	}
	if err := os.Rename(tempPath, path); err != nil {
		_ = os.Remove(tempPath)
		return err
	}
	return nil
}

type CleanupResult struct {
	RemovedSessions int   `json:"removed_sessions"`
	FreedBytes      int64 `json:"freed_bytes"`
}

func CleanupAllClosedSessions(root string) (CleanupResult, error) {
	root = filepath.Clean(strings.TrimSpace(root))
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
		if readErr != nil || manifest.Status != "closed" {
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
		if closed && manifest.StartedAt.Before(cutoff) {
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
			return err
		}
		total += info.Size()
		return nil
	})
	if errors.Is(err, os.ErrNotExist) {
		return 0, nil
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
	return settings
}
