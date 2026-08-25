package snapshot

import (
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"

	"github.com/fsnotify/fsnotify"
)

// Monitor watches the nearest existing ancestor of a Cursor-managed worktree.
// Any filesystem event related to the target path is sticky: later deletion or
// recreation cannot reopen the model-switch window.
type Monitor struct {
	watcher  *fsnotify.Watcher
	target   string
	observed atomic.Bool
	failed   atomic.Bool
	done     chan struct{}
}

func Watch(target string) (*Monitor, error) {
	cleanTarget, err := filepath.Abs(filepath.Clean(target))
	if err != nil || cleanTarget == "" {
		return nil, ErrCaptureFailed
	}
	ancestor := filepath.Dir(cleanTarget)
	for {
		info, statErr := os.Stat(ancestor)
		if statErr == nil {
			if !info.IsDir() {
				return nil, ErrCaptureFailed
			}
			break
		}
		if !os.IsNotExist(statErr) {
			return nil, ErrCaptureFailed
		}
		parent := filepath.Dir(ancestor)
		if parent == ancestor {
			return nil, ErrCaptureFailed
		}
		ancestor = parent
	}
	watcher, err := fsnotify.NewWatcher()
	if err != nil {
		return nil, ErrCaptureFailed
	}
	if err := watcher.Add(ancestor); err != nil {
		_ = watcher.Close()
		return nil, ErrCaptureFailed
	}
	monitor := &Monitor{
		watcher: watcher,
		target:  cleanTarget,
		done:    make(chan struct{}),
	}
	go monitor.run()
	return monitor, nil
}

func (m *Monitor) run() {
	defer close(m.done)
	for {
		select {
		case event, ok := <-m.watcher.Events:
			if !ok {
				return
			}
			if relatedPath(m.target, event.Name) {
				m.observed.Store(true)
			}
		case _, ok := <-m.watcher.Errors:
			if !ok {
				return
			}
			m.failed.Store(true)
		}
	}
}

func (m *Monitor) Close() (observed bool, failed bool) {
	if m == nil {
		return false, false
	}
	if err := m.watcher.Close(); err != nil {
		m.failed.Store(true)
	}
	<-m.done
	return m.observed.Load(), m.failed.Load()
}

func relatedPath(target, eventPath string) bool {
	event, err := filepath.Abs(filepath.Clean(eventPath))
	if err != nil {
		return true
	}
	return within(target, event) || within(event, target)
}

func within(path, root string) bool {
	rel, err := filepath.Rel(root, path)
	if err != nil {
		return false
	}
	return rel == "." || (rel != ".." && !filepath.IsAbs(rel) && !strings.HasPrefix(rel, ".."+string(filepath.Separator)))
}
