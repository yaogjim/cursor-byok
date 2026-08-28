//go:build !windows

package bridge

import "os/exec"

func applyCursorProcessAttr(_ *exec.Cmd, _ cursorProcessAttr) {}
