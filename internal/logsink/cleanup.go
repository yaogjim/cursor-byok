package logsink

import (
	"errors"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

type CleanupStats struct {
	Removed    int
	Kept       int
	FreedBytes int64
}

// ManagedShardCleanupConfig removes historical rotating shards while keeping
// the newest files and any caller-protected paths. Symlinks and non-regular
// entries are never followed or deleted.
type ManagedShardCleanupConfig struct {
	Prefix     string
	Extension  string
	KeepNewest int
	Protected  map[string]struct{}
}

func CleanupManagedShards(dir string, config ManagedShardCleanupConfig) (CleanupStats, error) {
	var stats CleanupStats
	dir = strings.TrimSpace(dir)
	if dir == "" {
		return stats, nil
	}
	prefix := strings.TrimSpace(config.Prefix)
	if prefix == "" {
		return stats, nil
	}
	extension := normalizeExtension(config.Extension)
	keepNewest := config.KeepNewest
	if keepNewest < 0 {
		keepNewest = 0
	}
	info, err := os.Lstat(dir)
	if errors.Is(err, os.ErrNotExist) {
		return stats, nil
	}
	if err != nil {
		return stats, err
	}
	if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return stats, errors.New("拒绝清理非真实日志目录")
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return stats, nil
		}
		return stats, err
	}
	type shard struct {
		path    string
		name    string
		size    int64
		modTime time.Time
	}
	shards := make([]shard, 0, len(entries))
	for _, entry := range entries {
		if !entry.Type().IsRegular() {
			continue
		}
		name := entry.Name()
		if !strings.HasPrefix(name, prefix+"-") || !strings.HasSuffix(name, extension) {
			continue
		}
		info, infoErr := entry.Info()
		if infoErr != nil {
			continue
		}
		shards = append(shards, shard{
			path:    filepath.Join(dir, name),
			name:    name,
			size:    info.Size(),
			modTime: info.ModTime(),
		})
	}
	sort.Slice(shards, func(left int, right int) bool {
		if shards[left].modTime.Equal(shards[right].modTime) {
			return shards[left].name < shards[right].name
		}
		return shards[left].modTime.Before(shards[right].modTime)
	})
	keepFrom := len(shards) - keepNewest
	if keepFrom < 0 {
		keepFrom = 0
	}
	for index, item := range shards {
		if index >= keepFrom || isProtectedPath(config.Protected, item.path) {
			stats.Kept++
			continue
		}
		if removeErr := os.Remove(item.path); removeErr != nil && !os.IsNotExist(removeErr) {
			return stats, removeErr
		}
		stats.Removed++
		stats.FreedBytes += item.size
	}
	return stats, nil
}

func isProtectedPath(protected map[string]struct{}, path string) bool {
	if len(protected) == 0 {
		return false
	}
	_, ok := protected[filepath.Clean(path)]
	return ok
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
