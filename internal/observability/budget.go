package observability

import (
	"errors"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"cursor/internal/logsink"
)

// logBudget splits the existing observability total budget (B) into a normal
// partition (B−D) and a diagnostics reserve (D = floor(B/8), at least one byte).
// Both partitions are enforced from the same mutex so a reconfigure that briefly
// overlaps an old and a new writer cannot over-commit the total.
type logBudget struct {
	mu              sync.Mutex
	root            string
	retention       time.Duration
	normalLimit     int64
	diagnosticLimit int64
	normalUsage     int64
	diagnosticUsage int64
	activeAppPath   string
	usageKnown      bool
	// activeDiagnostics maps each open diagnostics writer to the shard path it
	// currently holds. It is budget-local (never a process-global registry) and
	// is the single source of truth for which diagnostics shards may not be
	// reclaimed, so overlapping writers cannot delete each other's active file.
	activeDiagnostics map[*logsink.RotatingFile]string
}

func newLogBudget(root string, settings Settings) *logBudget {
	budget := &logBudget{root: root}
	budget.update(settings)
	return budget
}

func (b *logBudget) update(settings Settings) {
	if b == nil {
		return
	}
	totalBytes := int64(settings.MaxDiskMB) * 1024 * 1024
	reserve := diagnosticReserveBytes(totalBytes)
	b.mu.Lock()
	b.retention = time.Duration(settings.RetentionDays) * 24 * time.Hour
	b.diagnosticLimit = reserve
	b.normalLimit = totalBytes - reserve
	b.usageKnown = false
	b.mu.Unlock()
}

// refreshDiagnosticUsageLocked re-reads the diagnostics partition size from
// disk. Callers must hold b.mu. Diagnostics usage is never cached across
// writers: the directory is the single source of truth so an old and a new
// diagnostic writer cannot each reserve the full reserve. A scan error is
// returned so the caller can fail closed instead of trusting stale usage.
func (b *logBudget) refreshDiagnosticUsageLocked() error {
	if b == nil {
		return nil
	}
	usage, err := directorySize(filepath.Join(b.root, diagnosticsDirname))
	if err != nil {
		return err
	}
	b.diagnosticUsage = usage
	return nil
}

// registerDiagnosticWriterLocked starts tracking a diagnostics writer. The
// shard path is updated after every append because rotation changes it.
func (b *logBudget) registerDiagnosticWriterLocked(writer *logsink.RotatingFile) {
	if b == nil || writer == nil {
		return
	}
	if b.activeDiagnostics == nil {
		b.activeDiagnostics = make(map[*logsink.RotatingFile]string)
	}
	b.activeDiagnostics[writer] = ""
}

func (b *logBudget) unregisterDiagnosticWriterLocked(writer *logsink.RotatingFile) {
	if b == nil || writer == nil {
		return
	}
	delete(b.activeDiagnostics, writer)
}

func (b *logBudget) updateDiagnosticWriterPathLocked(writer *logsink.RotatingFile, path string) {
	if b == nil || writer == nil {
		return
	}
	if b.activeDiagnostics == nil {
		b.activeDiagnostics = make(map[*logsink.RotatingFile]string)
	}
	if strings.TrimSpace(path) == "" {
		b.activeDiagnostics[writer] = ""
		return
	}
	b.activeDiagnostics[writer] = filepath.Clean(path)
}

func (b *logBudget) isActiveDiagnosticPathLocked(path string) bool {
	cleaned := filepath.Clean(path)
	for _, active := range b.activeDiagnostics {
		if active != "" && filepath.Clean(active) == cleaned {
			return true
		}
	}
	return false
}

// peerActiveDiagnosticPathsLocked snapshots the shard paths held by every other
// tracked diagnostics writer. It is passed to the caller's own RotatingFile so
// its cleanup keeps peer shards while still reclaiming its own sealed ones.
func (b *logBudget) peerActiveDiagnosticPathsLocked(exclude *logsink.RotatingFile) map[string]struct{} {
	if b == nil {
		return nil
	}
	paths := make(map[string]struct{}, len(b.activeDiagnostics))
	for writer, path := range b.activeDiagnostics {
		if writer == exclude || path == "" {
			continue
		}
		paths[filepath.Clean(path)] = struct{}{}
	}
	return paths
}

// reclaimSealedDiagnosticsLocked removes the oldest sealed diagnostics shards
// until the incoming record fits inside D. Active shards of any writer and
// unknown files or symlinks are never removed, so the reserve stays a hard cap
// over the whole directory (including bytes the budget does not own).
func (b *logBudget) reclaimSealedDiagnosticsLocked(incoming int64) error {
	if b == nil || b.diagnosticLimit <= 0 {
		return nil
	}
	dir := filepath.Join(b.root, diagnosticsDirname)
	entries, err := os.ReadDir(dir)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			b.diagnosticUsage = 0
			return nil
		}
		return err
	}
	usage, err := directorySize(dir)
	if err != nil {
		return err
	}
	b.diagnosticUsage = usage
	if usage+incoming <= b.diagnosticLimit {
		return nil
	}
	candidates := make([]reclaimCandidate, 0, len(entries))
	for _, entry := range entries {
		if entry.IsDir() || entry.Type()&os.ModeSymlink != 0 {
			continue
		}
		name := entry.Name()
		if !strings.HasPrefix(name, diagnosticsPrefix+"-") || !strings.HasSuffix(name, diagnosticsExtension) {
			continue
		}
		path := filepath.Join(dir, name)
		if b.isActiveDiagnosticPathLocked(path) {
			continue
		}
		info, infoErr := entry.Info()
		if infoErr != nil {
			continue
		}
		candidates = append(candidates, reclaimCandidate{path: path, when: info.ModTime().UTC(), size: info.Size()})
	}
	sort.Slice(candidates, func(left int, right int) bool {
		return lessCandidate(candidates[left], candidates[right])
	})
	for _, candidate := range candidates {
		if usage+incoming <= b.diagnosticLimit {
			break
		}
		if removeErr := os.Remove(candidate.path); removeErr != nil && !os.IsNotExist(removeErr) {
			return removeErr
		}
		usage -= candidate.size
	}
	b.diagnosticUsage = usage
	return nil
}

// sealOwnActiveDiagnosticsLocked closes the caller's own active shard so the
// sealed-shard reclaim can remove it. It is only used when no other reclaimable
// room remains: peer active shards and unknown files stay protected. The writer
// stays usable because RotatingFile.Close only closes the current handle, not
// the sink, so the next append opens a fresh shard.
func (b *logBudget) sealOwnActiveDiagnosticsLocked(writer *logsink.RotatingFile, incoming int64) error {
	if b == nil || writer == nil {
		return nil
	}
	if strings.TrimSpace(writer.CurrentPath()) == "" {
		return nil
	}
	if err := writer.Close(); err != nil {
		return err
	}
	b.updateDiagnosticWriterPathLocked(writer, "")
	return b.reclaimSealedDiagnosticsLocked(incoming)
}

func (b *logBudget) diagnosticMaxBytes() int64 {
	if b == nil {
		return 0
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.diagnosticLimit
}

func (b *logBudget) initialize() error {
	if b == nil {
		return nil
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	usage, err := normalUsageBytes(b.root)
	if err != nil {
		return err
	}
	b.normalUsage = usage
	b.usageKnown = true
	return nil
}

func diagnosticReserveBytes(totalBytes int64) int64 {
	if totalBytes <= 0 {
		return 0
	}
	reserve := totalBytes / 8
	if reserve < 1 {
		reserve = 1
	}
	if reserve > totalBytes {
		reserve = totalBytes
	}
	return reserve
}

// admitNormalLocked reserves normal-partition space for an incoming write,
// reclaiming reclaimable normal artifacts first. excludeDir, when non-empty, is
// the caller's own session directory and is never reclaimed: a writer may still
// rewrite its manifest after the session was marked closed on disk, and that
// rewrite must not delete the very session it belongs to. Callers must hold
// b.mu.
func (b *logBudget) admitNormalLocked(additionalBytes int64, reserveBytes int64, excludeDir string) bool {
	if b == nil || b.normalLimit <= 0 {
		return false
	}
	if !b.usageKnown {
		usage, err := normalUsageBytes(b.root)
		if err != nil {
			return false
		}
		b.normalUsage = usage
		b.usageKnown = true
	}
	required := additionalBytes + reserveBytes
	if b.normalUsage+required <= b.normalLimit {
		return true
	}
	if err := b.reclaimNormalLocked(required, excludeDir); err != nil {
		return false
	}
	return b.normalUsage+required <= b.normalLimit
}

// reclaimNormal removes reclaimable normal artifacts until required bytes fit.
func (b *logBudget) reclaimNormal(required int64) error {
	if b == nil {
		return nil
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.reclaimNormalLocked(required, "")
}

func (b *logBudget) reclaimNormalLocked(required int64, excludeDir string) error {
	if b.normalLimit <= 0 {
		return errSessionQuotaExceeded
	}
	b.removeExpiredNormalLocked(excludeDir)
	usage, err := normalUsageBytes(b.root)
	if err != nil {
		return err
	}
	b.normalUsage = usage
	b.usageKnown = true
	if usage+required <= b.normalLimit {
		return nil
	}
	fullCandidates, mergedCandidates := b.reclaimCandidatesLocked(excludeDir)
	usage = b.removeCandidatesLocked(fullCandidates, usage, required)
	if usage+required > b.normalLimit {
		usage = b.removeCandidatesLocked(mergedCandidates, usage, required)
	}
	b.normalUsage = usage
	b.usageKnown = true
	if usage+required > b.normalLimit {
		return errSessionQuotaExceeded
	}
	return nil
}

func sameExcludedDir(path string, excludeDir string) bool {
	return excludeDir != "" && filepath.Clean(path) == filepath.Clean(excludeDir)
}

func (b *logBudget) removeCandidatesLocked(candidates []reclaimCandidate, usage int64, required int64) int64 {
	for _, candidate := range candidates {
		if usage+required <= b.normalLimit {
			break
		}
		if err := os.RemoveAll(candidate.path); err != nil && !os.IsNotExist(err) {
			continue
		}
		usage -= candidate.size
	}
	return usage
}

type reclaimCandidate struct {
	path string
	when time.Time
	size int64
}

// removeExpiredNormalLocked drops managed normal artifacts past the retention
// window. It protects open traces, the newest app shard, unknown files and
// symlink targets, and the caller's own session directory.
func (b *logBudget) removeExpiredNormalLocked(excludeDir string) {
	if b == nil || b.retention <= 0 {
		return
	}
	cutoff := time.Now().UTC().Add(-b.retention)
	tracesRoot := filepath.Join(b.root, tracesDirname)
	if entries, err := os.ReadDir(tracesRoot); err == nil {
		for _, entry := range entries {
			if !entry.IsDir() || entry.Type()&os.ModeSymlink != 0 {
				continue
			}
			path := filepath.Join(tracesRoot, entry.Name())
			if sameExcludedDir(path, excludeDir) {
				continue
			}
			manifest, readErr := readManifest(filepath.Join(path, manifestFilename))
			if readErr != nil || !reclaimableManifest(entry.Name(), manifest) || !manifest.StartedAt.Before(cutoff) {
				continue
			}
			_ = os.RemoveAll(path)
		}
	}
	for _, shard := range reclaimableAppShards(b.root, b.activeAppPath) {
		if shard.when.Before(cutoff) {
			_ = os.RemoveAll(shard.path)
		}
	}
}

// reclaimCandidatesLocked collects closed traces and archived app shards in
// the approved reclaim order: closed full traces form their own first tier,
// while closed basic traces and archived app log shards are merged into a
// single tier ordered by time (ties broken by path) with lessCandidate.
// Diagnostics shards are intentionally excluded: the diagnostics partition
// reclaims itself through its own rotating sink.
func (b *logBudget) reclaimCandidatesLocked(excludeDir string) (fullCandidates []reclaimCandidate, mergedCandidates []reclaimCandidate) {
	tracesRoot := filepath.Join(b.root, tracesDirname)
	if entries, err := os.ReadDir(tracesRoot); err == nil {
		for _, entry := range entries {
			if !entry.IsDir() || entry.Type()&os.ModeSymlink != 0 {
				continue
			}
			path := filepath.Join(tracesRoot, entry.Name())
			if sameExcludedDir(path, excludeDir) {
				continue
			}
			manifest, readErr := readManifest(filepath.Join(path, manifestFilename))
			if readErr != nil || !reclaimableManifest(entry.Name(), manifest) {
				continue
			}
			size, sizeErr := directorySize(path)
			if sizeErr != nil {
				continue
			}
			candidate := reclaimCandidate{path: path, when: manifest.StartedAt, size: size}
			if manifest.Mode == ModeFull {
				fullCandidates = append(fullCandidates, candidate)
			} else {
				mergedCandidates = append(mergedCandidates, candidate)
			}
		}
	}
	for _, shard := range reclaimableAppShards(b.root, b.activeAppPath) {
		mergedCandidates = append(mergedCandidates, reclaimCandidate{path: shard.path, when: shard.when, size: shard.size})
	}
	for _, candidates := range [][]reclaimCandidate{fullCandidates, mergedCandidates} {
		sort.Slice(candidates, func(left int, right int) bool {
			return lessCandidate(candidates[left], candidates[right])
		})
	}
	return fullCandidates, mergedCandidates
}

func lessCandidate(left reclaimCandidate, right reclaimCandidate) bool {
	if left.when.Equal(right.when) {
		return left.path < right.path
	}
	return left.when.Before(right.when)
}

type appShard struct {
	path string
	when time.Time
	size int64
}

// reclaimableAppShards lists archived app log shards. While the process is
// writing, the exact active path is protected even if its mtime is not newest;
// before the first append, the newest shard is conservatively protected.
func reclaimableAppShards(root string, activePath string) []appShard {
	dir := filepath.Join(root, "app")
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil
	}
	activePath = filepath.Clean(strings.TrimSpace(activePath))
	shards := make([]appShard, 0, len(entries))
	for _, entry := range entries {
		if entry.IsDir() || entry.Type()&os.ModeSymlink != 0 {
			continue
		}
		name := entry.Name()
		if !strings.HasPrefix(name, "app-") || !strings.HasSuffix(name, ".log") {
			continue
		}
		path := filepath.Join(dir, name)
		if activePath != "." && activePath != "" && filepath.Clean(path) == activePath {
			continue
		}
		info, infoErr := entry.Info()
		if infoErr != nil {
			continue
		}
		shards = append(shards, appShard{
			path: path,
			when: info.ModTime().UTC(),
			size: info.Size(),
		})
	}
	sort.Slice(shards, func(left int, right int) bool {
		if shards[left].when.Equal(shards[right].when) {
			return shards[left].path < shards[right].path
		}
		return shards[left].when.Before(shards[right].when)
	})
	if (activePath == "." || activePath == "") && len(shards) > 0 {
		shards = shards[:len(shards)-1]
	}
	return shards
}

// normalUsageBytes counts everything in the logs root except the diagnostics
// partition. Unknown files still count toward the normal budget.
func normalUsageBytes(root string) (int64, error) {
	total, err := directorySize(root)
	if err != nil {
		return 0, err
	}
	diagnosticBytes, err := directorySize(filepath.Join(root, diagnosticsDirname))
	if err != nil {
		return 0, err
	}
	usage := total - diagnosticBytes
	if usage < 0 {
		usage = 0
	}
	return usage, nil
}
