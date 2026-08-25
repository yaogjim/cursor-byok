//go:build !unix

package engine

import "os/exec"

func PlatformSupported() bool { return false }

func setProcAttr(cmd *exec.Cmd) {}

func stopChild(cmd *exec.Cmd) {}
