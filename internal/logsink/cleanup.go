package logsink

import (
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"
)

type CleanupStats struct {
	Removed int
	Kept    int
}

type DebugCleanupConfig struct {
	MaxAge time.Duration
	Now    func() time.Time
}

func CleanupDebugTree(root string, config DebugCleanupConfig) (CleanupStats, error) {
	var stats CleanupStats
	root = strings.TrimSpace(root)
	if root == "" {
		return stats, nil
	}
	if config.Now == nil {
		config.Now = time.Now
	}
	err := cleanupDebugDirectory(root, config.Now(), config.MaxAge, &stats)
	if errors.Is(err, os.ErrNotExist) {
		return stats, nil
	}
	return stats, err
}

func cleanupDebugDirectory(dir string, now time.Time, maxAge time.Duration, stats *CleanupStats) error {
	handle, err := os.Open(dir)
	if err != nil {
		return err
	}
	defer handle.Close()
	for {
		entries, readErr := handle.ReadDir(256)
		for _, entry := range entries {
			path := filepath.Join(dir, entry.Name())
			if entry.IsDir() {
				if err := cleanupDebugDirectory(path, now, maxAge, stats); err != nil && !errors.Is(err, os.ErrNotExist) {
					return err
				}
				continue
			}
			if !isManagedDebugLogName(entry.Name()) {
				stats.Kept++
				continue
			}
			info, err := entry.Info()
			if err != nil {
				if errors.Is(err, os.ErrNotExist) {
					continue
				}
				return err
			}
			if maxAge <= 0 || now.Sub(info.ModTime()) <= maxAge {
				stats.Kept++
				continue
			}
			if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
				return err
			}
			stats.Removed++
		}
		if errors.Is(readErr, io.EOF) {
			return nil
		}
		if readErr != nil {
			return readErr
		}
	}
}

func CleanupPayloadDirectory(dir string, preservePacks bool) (CleanupStats, error) {
	var stats CleanupStats
	dir = strings.TrimSpace(dir)
	if dir == "" {
		return stats, nil
	}
	handle, err := os.Open(dir)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return stats, nil
		}
		return stats, err
	}
	defer handle.Close()

	for {
		names, readErr := handle.Readdirnames(256)
		for _, name := range names {
			if preservePacks && isPayloadPackName(name) {
				stats.Kept++
				continue
			}
			if removeErr := os.RemoveAll(filepath.Join(dir, name)); removeErr != nil && !errors.Is(removeErr, os.ErrNotExist) {
				return stats, removeErr
			}
			stats.Removed++
		}
		if errors.Is(readErr, io.EOF) {
			break
		}
		if readErr != nil {
			return stats, readErr
		}
	}
	if !preservePacks {
		_ = handle.Close()
		if removeErr := os.Remove(dir); removeErr != nil && !errors.Is(removeErr, os.ErrNotExist) {
			return stats, removeErr
		}
	}
	return stats, nil
}

func isPayloadPackName(name string) bool {
	trimmed := strings.TrimSpace(name)
	return strings.HasPrefix(trimmed, "pack-") && strings.HasSuffix(trimmed, ".jsonl")
}

func isManagedDebugLogName(name string) bool {
	trimmed := strings.TrimSpace(name)
	if strings.HasSuffix(trimmed, ".jsonl") && (strings.HasPrefix(trimmed, "event-") || strings.HasPrefix(trimmed, "pack-")) {
		return true
	}
	switch trimmed {
	case "event.jsonl", "provider.jsonl", "runsse.jsonl", "runtime.jsonl", "bidi.raw.jsonl", "bidi.decoded.jsonl":
		return true
	default:
		return false
	}
}
