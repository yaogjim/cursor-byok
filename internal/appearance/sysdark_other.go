//go:build !darwin && !windows

package appearance

import (
	"context"
	"os"
	"os/exec"
	"strings"
	"time"
)

func detectSystemPrefersDark() bool {
	if strings.Contains(strings.ToLower(os.Getenv("GTK_THEME")), "dark") {
		return true
	}
	if os.Getenv("GTK_APPLICATION_PREFER_DARK_THEME") == "1" {
		return true
	}
	ctx, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
	defer cancel()
	out, err := exec.CommandContext(ctx, "gsettings", "get", "org.gnome.desktop.interface", "color-scheme").Output()
	if err != nil {
		return false
	}
	return strings.Contains(strings.ToLower(string(out)), "prefer-dark")
}
