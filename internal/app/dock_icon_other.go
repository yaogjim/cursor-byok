//go:build !darwin

package app

// 非 macOS 平台不提供 Dock 图标开关。
func newDockIconController() dockIconController {
	return nil
}
