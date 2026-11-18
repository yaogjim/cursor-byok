package app

import "cursor/internal/backend"

// handleWindowClose 按配置决定关闭窗口后的动作：
// 继续后台运行时隐藏窗口，否则走完整退出收尾。
func handleWindowClose(exitOnWindowClose bool, hide func(), quit func()) {
	if exitOnWindowClose {
		if quit != nil {
			quit()
		}
		return
	}
	if hide != nil {
		hide()
	}
}

func runQuitFrom(proxy quitShutdown, initiator string, quit func()) {
	if proxy != nil {
		proxy.ShutdownForQuitFrom(initiator)
	}
	if quit != nil {
		quit()
	}
}

func runTrayQuit(proxy quitShutdown, quit func()) {
	runQuitFrom(proxy, backend.ShutdownInitiatorTray, quit)
}
