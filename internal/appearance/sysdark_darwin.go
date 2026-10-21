//go:build darwin

package appearance

import (
	"context"
	"os/exec"
	"strings"
	"time"
)

func detectSystemPrefersDark() bool {
	ctx, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
	defer cancel()
	out, err := exec.CommandContext(ctx, "defaults", "read", "-g", "AppleInterfaceStyle").Output()
	if err != nil {
		return false
	}
	return strings.EqualFold(strings.TrimSpace(string(out)), "Dark")
}
