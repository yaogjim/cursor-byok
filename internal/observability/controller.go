package observability

import (
	"context"
	"sync"
)

type Controller struct {
	root      string
	updateMu  sync.Mutex
	mu        sync.RWMutex
	settings  Settings
	humanSink HumanSink
	recorder  *Recorder
	closed    bool
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
	return &Controller{
		root:      root,
		settings:  normalized,
		humanSink: humanSink,
		recorder:  recorder,
	}, nil
}

func (controller *Controller) Record(ctx context.Context, capture Capture) bool {
	if controller == nil {
		return false
	}
	controller.mu.RLock()
	recorder := controller.recorder
	controller.mu.RUnlock()
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
	unchanged := controller.settings == normalized
	controller.mu.RUnlock()
	if unchanged {
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
	return previous.Close()
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
	controller.mu.Lock()
	recorder := controller.recorder
	controller.recorder = nil
	controller.closed = true
	controller.mu.Unlock()
	return recorder.Close()
}
