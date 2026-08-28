//go:build windows

package bridge

import (
	"os/exec"
	"syscall"
)

func applyCursorProcessAttr(cmd *exec.Cmd, attr cursorProcessAttr) {
	if cmd == nil || !attr.Detach {
		return
	}
	cmd.SysProcAttr = &syscall.SysProcAttr{
		CreationFlags: attr.CreationFlags,
	}
}
