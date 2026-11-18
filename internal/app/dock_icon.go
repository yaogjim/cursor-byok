package app

import "github.com/wailsapp/wails/v3/pkg/application"

// startupActivationPolicy 根据配置决定启动时的 macOS 激活策略。
// Regular 会在 Dock 中显示图标；Accessory 只保留菜单栏应用形态。
func startupActivationPolicy(showDockIcon bool) application.ActivationPolicy {
	if showDockIcon {
		return application.ActivationPolicyRegular
	}
	return application.ActivationPolicyAccessory
}

// dockIconController 抽象 Dock 图标切换，使运行逻辑可独立测试。
type dockIconController interface {
	ShowAppIcon()
	HideAppIcon()
}

// applyDockIcon 在运行时显示或隐藏 Dock 图标。
func applyDockIcon(controller dockIconController, show bool) {
	if controller == nil {
		return
	}
	if show {
		controller.ShowAppIcon()
		return
	}
	controller.HideAppIcon()
}

// handleDockReopen 在 Dock 图标被点击且窗口不可见时恢复并聚焦窗口。
func handleDockReopen(visible bool, show func(), focus func()) {
	if visible {
		return
	}
	if show != nil {
		show()
	}
	if focus != nil {
		focus()
	}
}
