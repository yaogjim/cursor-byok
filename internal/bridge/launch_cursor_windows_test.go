//go:build windows

package bridge

import (
	"testing"
)

func TestCursorCommandDetachesOnWindows(t *testing.T) {
	cmd := cursorCommand("windows", "Cursor.exe")
	if cmd.SysProcAttr == nil {
		t.Fatal("windows cursor start must set SysProcAttr")
	}
	flags := cmd.SysProcAttr.CreationFlags
	if flags&windowsDetachedProcessFlag == 0 {
		t.Fatalf("DETACHED_PROCESS missing: %#x", flags)
	}
	if flags&windowsCreateNewProcessGroupFlag == 0 {
		t.Fatalf("CREATE_NEW_PROCESS_GROUP missing: %#x", flags)
	}

	plain := cursorCommand("darwin", "cursor")
	if plain.SysProcAttr != nil {
		t.Fatalf("injected non-windows goos must keep unix start behavior, got %#v", plain.SysProcAttr)
	}
}
