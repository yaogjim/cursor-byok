package app

import (
	"testing"

	"github.com/wailsapp/wails/v3/pkg/application"
)

type recordingDockIcon struct {
	shows int
	hides int
}

func (r *recordingDockIcon) ShowAppIcon() { r.shows++ }
func (r *recordingDockIcon) HideAppIcon() { r.hides++ }

func TestStartupActivationPolicyFollowsDockSetting(t *testing.T) {
	if got := startupActivationPolicy(false); got != application.ActivationPolicyAccessory {
		t.Fatalf("policy = %v, want accessory", got)
	}
	if got := startupActivationPolicy(true); got != application.ActivationPolicyRegular {
		t.Fatalf("policy = %v, want regular", got)
	}
}

func TestApplyDockIconSwitchesVisibility(t *testing.T) {
	controller := &recordingDockIcon{}
	applyDockIcon(controller, true)
	if controller.shows != 1 || controller.hides != 0 {
		t.Fatalf("shows=%d hides=%d, want 1/0", controller.shows, controller.hides)
	}
	applyDockIcon(controller, false)
	if controller.shows != 1 || controller.hides != 1 {
		t.Fatalf("shows=%d hides=%d, want 1/1", controller.shows, controller.hides)
	}
}

func TestHandleDockReopenRestoresHiddenWindow(t *testing.T) {
	var shown, focused int
	handleDockReopen(true, func() { shown++ }, func() { focused++ })
	if shown != 0 || focused != 0 {
		t.Fatalf("visible window should not be reshown: shown=%d focused=%d", shown, focused)
	}
	handleDockReopen(false, func() { shown++ }, func() { focused++ })
	if shown != 1 || focused != 1 {
		t.Fatalf("hidden window not restored: shown=%d focused=%d", shown, focused)
	}
}
