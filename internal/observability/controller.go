package observability

import (
	"context"
	"sync"
	"time"
)

const recorderCloseTimeout = 2 * time.Second

type Controller struct {
	root      string
	updateMu  sync.Mutex
	mu        sync.RWMutex
	settings  Settings
	humanSink HumanSink
	recorder  *Recorder
	closed    bool
	closing   sync.WaitGroup
}

func NewController(root string, settings Settings) (*Controller, error) {
	return NewControllerWithHumanSink(root, settings, nil)
}

func NewControllerWithHumanSink(root string, settings Settings, humanSink HumanSink) (*Controller, error) {
	normalized := normalizeSettings(settings)
	var recorder *Recorder
	var err error
	if normalized.Mode != ModeOff {
		recorder, err = NewRecorderWithHumanSink(root, normalized, humanSink)
		if err != nil {
			return nil, err
		}
	}
	controller := &Controller{
		root:      root,
		settings:  normalized,
		humanSink: humanSink,
		recorder:  recorder,
	}
	SetProcessSink(controller)
	return controller, nil
}

func (controller *Controller) Record(ctx context.Context, capture Capture) bool {
	if controller == nil {
		return false
	}
	controller.mu.RLock()
	recorder := controller.recorder
	runtimeFingerprint := controller.settings.RuntimeFingerprint
	controller.mu.RUnlock()
	if runtimeFingerprint != "" {
		if capture.Event.Fields == nil {
			capture.Event.Fields = map[string]any{"runtime_fingerprint": runtimeFingerprint}
		} else if _, exists := capture.Event.Fields["runtime_fingerprint"]; !exists {
			fields := make(map[string]any, len(capture.Event.Fields)+1)
			for key, value := range capture.Event.Fields {
				fields[key] = value
			}
			fields["runtime_fingerprint"] = runtimeFingerprint
			capture.Event.Fields = fields
		}
	}
	return recorder.Record(ctx, capture)
}

func (controller *Controller) RecordEvent(ctx context.Context, event Event) bool {
	return controller.Record(ctx, Capture{Event: event})
}

func (controller *Controller) Reconfigure(settings Settings) error {
	if controller == nil {
		return nil
	}
	controller.updateMu.Lock()
	defer controller.updateMu.Unlock()
	controller.mu.RLock()
	if controller.closed {
		controller.mu.RUnlock()
		return nil
	}
	controller.mu.RUnlock()
	normalized := normalizeSettings(settings)
	controller.mu.RLock()
	storageUnchanged := storageSettingsEqual(controller.settings, normalized)
	controller.mu.RUnlock()
	if storageUnchanged {
		controller.mu.Lock()
		controller.settings = normalized
		controller.mu.Unlock()
		return nil
	}
	var next *Recorder
	var err error
	if normalized.Mode != ModeOff {
		next, err = NewRecorderWithHumanSink(controller.root, normalized, controller.humanSink)
		if err != nil {
			return err
		}
	}
	controller.mu.Lock()
	previous := controller.recorder
	controller.recorder = next
	controller.settings = normalized
	controller.mu.Unlock()
	controller.closeRecorderAsync(previous)
	return nil
}

func (controller *Controller) Status() Status {
	if controller == nil {
		return Status{}
	}
	controller.mu.RLock()
	recorder := controller.recorder
	mode := controller.settings.Mode
	controller.mu.RUnlock()
	if recorder == nil {
		return Status{Mode: mode}
	}
	return recorder.Status()
}

func (controller *Controller) Close() error {
	if controller == nil {
		return nil
	}
	controller.updateMu.Lock()
	defer controller.updateMu.Unlock()
	ClearProcessSink(controller)
	controller.mu.Lock()
	recorder := controller.recorder
	controller.recorder = nil
	controller.closed = true
	controller.mu.Unlock()
	err := closeRecorderBounded(recorder, recorderCloseTimeout)
	controller.waitAsyncCloses(recorderCloseTimeout)
	return err
}

func storageSettingsEqual(left Settings, right Settings) bool {
	return left.Mode == right.Mode &&
		left.RetentionDays == right.RetentionDays &&
		left.MaxDiskMB == right.MaxDiskMB &&
		left.QueueSize == right.QueueSize
}

func (controller *Controller) closeRecorderAsync(recorder *Recorder) {
	if recorder == nil {
		return
	}
	controller.closing.Add(1)
	go func() {
		defer controller.closing.Done()
		_ = closeRecorderBounded(recorder, recorderCloseTimeout)
	}()
}

func (controller *Controller) waitAsyncCloses(timeout time.Duration) {
	done := make(chan struct{})
	go func() {
		controller.closing.Wait()
		close(done)
	}()
	timer := time.NewTimer(timeout)
	defer timer.Stop()
	select {
	case <-done:
	case <-timer.C:
	}
}

func closeRecorderBounded(recorder *Recorder, timeout time.Duration) error {
	if recorder == nil {
		return nil
	}
	done := make(chan error, 1)
	go func() {
		done <- recorder.Close()
	}()
	timer := time.NewTimer(timeout)
	defer timer.Stop()
	select {
	case err := <-done:
		return err
	case <-timer.C:
		return nil
	}
}