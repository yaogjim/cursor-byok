//go:build unix

package engine

import (
	"os/exec"
	"syscall"
	"time"
)

func PlatformSupported() bool { return true }

func setProcAttr(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
}

func stopChild(cmd *exec.Cmd) {
	if cmd == nil || cmd.Process == nil {
		return
	}
	pid := cmd.Process.Pid
	_ = syscall.Kill(-pid, syscall.SIGTERM)
	timer := time.NewTimer(2 * time.Second)
	defer timer.Stop()
	<-timer.C
	_ = syscall.Kill(-pid, syscall.SIGKILL)
}
