package bridge

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"
)

type fakeFileInfo struct {
	dir bool
}

func (info fakeFileInfo) Name() string       { return "Cursor" }
func (info fakeFileInfo) Size() int64        { return 0 }
func (info fakeFileInfo) Mode() os.FileMode  { return 0o755 }
func (info fakeFileInfo) ModTime() time.Time { return time.Time{} }
func (info fakeFileInfo) IsDir() bool        { return info.dir }
func (info fakeFileInfo) Sys() any           { return nil }

func TestLaunchCursorDarwinOpensAppBundle(t *testing.T) {
	var started [][]string
	env := cursorLaunchEnv{
		goos: "darwin",
		lookPath: func(string) (string, error) {
			return "", exec.ErrNotFound
		},
		stat: func(path string) (os.FileInfo, error) {
			if path == "/Applications/Cursor.app" {
				return fakeFileInfo{dir: true}, nil
			}
			return nil, os.ErrNotExist
		},
		getenv: func(string) string { return "" },
		homeDir: func() (string, error) {
			return "/Users/dev", nil
		},
		start: func(name string, args ...string) error {
			started = append(started, append([]string{name}, args...))
			return nil
		},
	}

	if err := launchCursor(env); err != nil {
		t.Fatal(err)
	}
	if len(started) != 1 || started[0][0] != "open" || started[0][1] != "/Applications/Cursor.app" {
		t.Fatalf("started = %v", started)
	}
}

func TestLaunchCursorNotFoundDoesNotStart(t *testing.T) {
	started := false
	env := cursorLaunchEnv{
		goos: "linux",
		lookPath: func(string) (string, error) {
			return "", exec.ErrNotFound
		},
		stat: func(string) (os.FileInfo, error) {
			return nil, os.ErrNotExist
		},
		getenv:  func(string) string { return "" },
		homeDir: func() (string, error) { return "/home/dev", nil },
		start: func(string, ...string) error {
			started = true
			return nil
		},
	}

	err := launchCursor(env)
	if !errors.Is(err, errCursorNotFound) {
		t.Fatalf("err = %v", err)
	}
	if started {
		t.Fatal("start should not run when Cursor is missing")
	}
}

func TestLaunchCursorPermissionOnStat(t *testing.T) {
	env := cursorLaunchEnv{
		goos: "darwin",
		lookPath: func(string) (string, error) {
			return "", exec.ErrNotFound
		},
		stat: func(path string) (os.FileInfo, error) {
			if path == "/Applications/Cursor.app" {
				return nil, os.ErrPermission
			}
			return nil, os.ErrNotExist
		},
		getenv:  func(string) string { return "" },
		homeDir: func() (string, error) { return "/Users/dev", nil },
		start: func(string, ...string) error {
			t.Fatal("start should not run after permission failure")
			return nil
		},
	}

	err := launchCursor(env)
	if !errors.Is(err, errCursorPermission) {
		t.Fatalf("err = %v", err)
	}
}

func TestLaunchCursorStartFailureIsExplicit(t *testing.T) {
	env := cursorLaunchEnv{
		goos: "linux",
		lookPath: func(string) (string, error) {
			return "/usr/bin/cursor", nil
		},
		stat: func(path string) (os.FileInfo, error) {
			if path == "/usr/bin/cursor" {
				return fakeFileInfo{}, nil
			}
			return nil, os.ErrNotExist
		},
		getenv:  func(string) string { return "" },
		homeDir: func() (string, error) { return "/home/dev", nil },
		start: func(string, ...string) error {
			return errors.New("exec format error")
		},
	}

	err := launchCursor(env)
	if err == nil || errors.Is(err, errCursorNotFound) || errors.Is(err, errCursorPermission) {
		t.Fatalf("err = %v", err)
	}
	if err.Error() != "启动 Cursor 失败: exec format error" {
		t.Fatalf("err = %q", err.Error())
	}
}

func TestLaunchCursorWindowsUsesKnownPath(t *testing.T) {
	exe := filepath.Join("C:\\Users", "dev", "AppData", "Local", "Programs", "cursor", "Cursor.exe")
	var started [][]string
	env := cursorLaunchEnv{
		goos: "windows",
		lookPath: func(string) (string, error) {
			return "", exec.ErrNotFound
		},
		stat: func(path string) (os.FileInfo, error) {
			if path == exe {
				return fakeFileInfo{}, nil
			}
			return nil, os.ErrNotExist
		},
		getenv: func(key string) string {
			if key == "LOCALAPPDATA" {
				return filepath.Join("C:\\Users", "dev", "AppData", "Local")
			}
			return ""
		},
		homeDir: func() (string, error) { return "", errors.New("unused") },
		start: func(name string, args ...string) error {
			started = append(started, append([]string{name}, args...))
			return nil
		},
	}

	if err := launchCursor(env); err != nil {
		t.Fatal(err)
	}
	if len(started) != 1 || started[0][0] != exe {
		t.Fatalf("started = %v", started)
	}
}

func TestLaunchCursorRejectsUnknownPlatform(t *testing.T) {
	err := launchCursor(cursorLaunchEnv{
		goos: "plan9",
		lookPath: func(string) (string, error) {
			return "/bin/cursor", nil
		},
		stat:    func(string) (os.FileInfo, error) { return fakeFileInfo{}, nil },
		getenv:  func(string) string { return "" },
		homeDir: func() (string, error) { return "/", nil },
		start: func(string, ...string) error {
			t.Fatal("start should not run on unknown platforms")
			return nil
		},
	})
	if !errors.Is(err, errCursorNotFound) {
		t.Fatalf("err = %v", err)
	}
}

func TestLooksLikeCursor(t *testing.T) {
	if !looksLikeCursor("/usr/bin/cursor") || looksLikeCursor("/usr/bin/code") {
		t.Fatal("cursor name filter failed")
	}
}

func TestCursorProcessAttrDetachesOnWindowsOnly(t *testing.T) {
	windowsAttr := cursorProcessAttrForGOOS("windows")
	if !windowsAttr.Detach {
		t.Fatal("windows cursor start must detach from the assistant process")
	}
	if windowsAttr.CreationFlags&windowsDetachedProcessFlag == 0 {
		t.Fatalf("DETACHED_PROCESS missing: %#x", windowsAttr.CreationFlags)
	}
	if windowsAttr.CreationFlags&windowsCreateNewProcessGroupFlag == 0 {
		t.Fatalf("CREATE_NEW_PROCESS_GROUP missing: %#x", windowsAttr.CreationFlags)
	}

	for _, goos := range []string{"darwin", "linux"} {
		attr := cursorProcessAttrForGOOS(goos)
		if attr.Detach || attr.CreationFlags != 0 {
			t.Fatalf("%s attr = %+v", goos, attr)
		}
	}
}

func TestCursorCommandKeepsUnixStartUnadorned(t *testing.T) {
	cmd := cursorCommand("darwin", "cursor")
	if cmd.SysProcAttr != nil {
		t.Fatalf("darwin SysProcAttr = %#v", cmd.SysProcAttr)
	}
	cmd = cursorCommand("linux", "cursor")
	if cmd.SysProcAttr != nil {
		t.Fatalf("linux SysProcAttr = %#v", cmd.SysProcAttr)
	}
}
