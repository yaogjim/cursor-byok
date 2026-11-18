//go:build darwin

package app

import "github.com/wailsapp/wails/v3/pkg/services/dock"

func newDockIconController() dockIconController {
	return dock.New()
}
