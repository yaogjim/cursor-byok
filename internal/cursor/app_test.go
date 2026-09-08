package cursor

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

type fakeDirInfo struct{}

func (fakeDirInfo) Name() string       { return "Cursor.app" }
func (fakeDirInfo) Size() int64        { return 0 }
func (fakeDirInfo) Mode() os.FileMode  { return os.ModeDir | 0o755 }
func (fakeDirInfo) ModTime() time.Time { return time.Time{} }
func (fakeDirInfo) IsDir() bool        { return true }
func (fakeDirInfo) Sys() any           { return nil }

func TestParseBundleIDFromPlist(t *testing.T) {
	id, err := parseBundleIDFromPlist([]byte(`<?xml version="1.0"?>
<plist><dict>
<key>CFBundleName</key><string>Cursor</string>
<key>CFBundleIdentifier</key>
<string>com.todesktop.230313mzl4w4u92</string>
</dict></plist>`))
	if err != nil {
		t.Fatal(err)
	}
	if id != CursorBundleID {
		t.Fatalf("id = %q", id)
	}
}

func TestIdentifyDarwinRequiresKnownBundleID(t *testing.T) {
	env := appEnv{
		goos: "darwin",
		stat: func(path string) (os.FileInfo, error) {
			if path == "/Applications/Cursor.app" {
				return fakeDirInfo{}, nil
			}
			return nil, os.ErrNotExist
		},
		homeDir: func() (string, error) { return "/Users/dev", nil },
		readBundleID: func(string) (string, error) {
			return "com.example.notcursor", nil
		},
	}
	_, err := env.Identify()
	if !errors.Is(err, ErrAppNotFound) {
		t.Fatalf("err = %v", err)
	}
}

func TestIdentifyDarwinAcceptsVerifiedBundle(t *testing.T) {
	root := t.TempDir()
	appPath := filepath.Join(root, "Applications", "Cursor.app")
	env := appEnv{
		goos: "darwin",
		stat: func(path string) (os.FileInfo, error) {
			if path == appPath {
				return fakeDirInfo{}, nil
			}
			return nil, os.ErrNotExist
		},
		homeDir: func() (string, error) { return root, nil },
		readBundleID: func(path string) (string, error) {
			if path != appPath {
				t.Fatalf("unexpected path %s", path)
			}
			return CursorBundleID, nil
		},
		allowedBundleID: CursorBundleID,
	}
	id, err := env.Identify()
	if err != nil {
		t.Fatal(err)
	}
	if id.BundleID != CursorBundleID || id.AppPath != appPath {
		t.Fatalf("id = %+v", id)
	}
}

func TestIdentifyNonDarwinIsUnsupportedNotMissing(t *testing.T) {
	for _, goos := range []string{"linux", "windows"} {
		_, err := (appEnv{goos: goos}).Identify()
		if !errors.Is(err, ErrUnsupportedPlatform) {
			t.Fatalf("goos=%s err=%v", goos, err)
		}
		if errors.Is(err, ErrAppNotFound) {
			t.Fatalf("goos=%s must not look like a missing app", goos)
		}
	}
}

func TestRunningUnsupportedDoesNotClaimStopped(t *testing.T) {
	running, err := (appEnv{goos: "linux"}).Running(context.Background(), AppIdentity{AppPath: "/usr/bin/cursor"})
	if running {
		t.Fatal("unsupported platform must not report Cursor running")
	}
	if !errors.Is(err, ErrUnsupportedPlatform) {
		t.Fatalf("err = %v", err)
	}
}

func TestQuitGracefullyUsesAppleEventNotKill(t *testing.T) {
	var scripts []string
	var started [][]string
	running := true
	env := appEnv{
		goos:            "darwin",
		allowedBundleID: CursorBundleID,
		quitPoll:        time.Millisecond,
		osa: func(_ context.Context, script string) (string, error) {
			scripts = append(scripts, script)
			if strings.Contains(script, "tell application id") && strings.Contains(script, "to quit") {
				running = false
			}
			if strings.Contains(script, "every process whose bundle identifier") {
				if running {
					return "1", nil
				}
				return "0", nil
			}
			return "", nil
		},
		run: func(_ context.Context, name string, args ...string) error {
			started = append(started, append([]string{name}, args...))
			return nil
		},
	}
	id := AppIdentity{BundleID: CursorBundleID, AppPath: "/tmp/fake/Cursor.app"}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := env.QuitGracefully(ctx, id); err != nil {
		t.Fatal(err)
	}
	joined := strings.Join(scripts, "\n")
	if !strings.Contains(joined, `tell application id "com.todesktop.230313mzl4w4u92" to quit`) {
		t.Fatalf("scripts = %q", joined)
	}
	for _, script := range scripts {
		lower := strings.ToLower(script)
		if strings.Contains(lower, "kill") || strings.Contains(script, "do shell script") {
			t.Fatalf("quit must not shell out to kill: %q", script)
		}
	}
	if len(started) != 0 {
		t.Fatalf("launch/start must not run during quit: %v", started)
	}
}

func TestQuitGracefullyUnsupportedNeverKills(t *testing.T) {
	var started [][]string
	env := appEnv{
		goos: "linux",
		run: func(_ context.Context, name string, args ...string) error {
			started = append(started, append([]string{name}, args...))
			return nil
		},
		osa: func(context.Context, string) (string, error) {
			t.Fatal("osascript must not run on unsupported platforms")
			return "", nil
		},
	}
	err := env.QuitGracefully(context.Background(), AppIdentity{AppPath: "/usr/bin/cursor"})
	if !errors.Is(err, ErrGracefulQuitUnsupported) {
		t.Fatalf("err = %v", err)
	}
	if len(started) != 0 {
		t.Fatalf("started = %v", started)
	}
}

func TestQuitGracefullyTimeoutKeepsRunning(t *testing.T) {
	env := appEnv{
		goos:            "darwin",
		allowedBundleID: CursorBundleID,
		quitPoll:        time.Millisecond,
		osa: func(_ context.Context, script string) (string, error) {
			if strings.Contains(script, "every process whose bundle identifier") {
				return "1", nil
			}
			return "", nil
		},
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	err := env.QuitGracefully(ctx, AppIdentity{BundleID: CursorBundleID})
	if !errors.Is(err, ErrGracefulQuitTimeout) {
		t.Fatalf("err = %v", err)
	}
}

func TestQuitGracefullyCancelStopsBlockingHelper(t *testing.T) {
	sleepBin, err := exec.LookPath("sleep")
	if err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	env := appEnv{
		goos:            "darwin",
		allowedBundleID: CursorBundleID,
		quitPoll:        time.Millisecond,
		osa: func(ctx context.Context, _ string) (string, error) {
			wg.Add(1)
			defer wg.Done()
			_, runErr := runHelper(ctx, sleepBin, "30")
			return "", runErr
		},
	}
	ctx, cancel := context.WithTimeout(context.Background(), 80*time.Millisecond)
	defer cancel()
	started := time.Now()
	quitErr := env.QuitGracefully(ctx, AppIdentity{BundleID: CursorBundleID})
	if elapsed := time.Since(started); elapsed > time.Second {
		t.Fatalf("blocking helper exceeded bound: %s", elapsed)
	}
	if !errors.Is(quitErr, ErrGracefulQuitTimeout) {
		t.Fatalf("err = %v", quitErr)
	}
	done := make(chan struct{})
	go func() {
		wg.Wait()
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("helper invocation leaked after cancel")
	}
}

func TestRunHelperCancelCompletesWithinBound(t *testing.T) {
	sleepBin, err := exec.LookPath("sleep")
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 80*time.Millisecond)
	defer cancel()
	started := time.Now()
	_, runErr := runHelper(ctx, sleepBin, "30")
	if elapsed := time.Since(started); elapsed > time.Second {
		t.Fatalf("runHelper exceeded bound: %s", elapsed)
	}
	if runErr == nil {
		t.Fatal("expected helper cancel error")
	}
}

func TestLaunchDarwinUsesVerifiedAppPathOnce(t *testing.T) {
	var started [][]string
	env := appEnv{
		goos:            "darwin",
		allowedBundleID: CursorBundleID,
		run: func(_ context.Context, name string, args ...string) error {
			started = append(started, append([]string{name}, args...))
			return nil
		},
	}
	id := AppIdentity{BundleID: CursorBundleID, AppPath: "/Applications/Cursor.app"}
	if err := env.Launch(context.Background(), id); err != nil {
		t.Fatal(err)
	}
	if len(started) != 1 || started[0][0] != "open" || started[0][1] != "/Applications/Cursor.app" {
		t.Fatalf("started = %v", started)
	}
}

func TestLaunchAwaitsNonzeroHelperAndDoesNotFallback(t *testing.T) {
	helper := writeDelayExitHelper(t, 80*time.Millisecond, 1)
	var calls [][]string
	env := appEnv{
		goos:            "darwin",
		allowedBundleID: CursorBundleID,
		run: func(ctx context.Context, name string, args ...string) error {
			calls = append(calls, append([]string{name}, args...))
			_, err := runHelper(ctx, helper)
			return err
		},
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	started := time.Now()
	err := env.Launch(ctx, AppIdentity{BundleID: CursorBundleID, AppPath: "/Applications/Cursor.app"})
	elapsed := time.Since(started)
	if elapsed < 50*time.Millisecond {
		t.Fatalf("Launch returned before helper finished: %s", elapsed)
	}
	if !errors.Is(err, ErrLaunchFailed) {
		t.Fatalf("err = %v", err)
	}
	if len(calls) != 1 {
		t.Fatalf("must not issue a second launch, calls=%v", calls)
	}
	if calls[0][0] != "open" || calls[0][1] != "/Applications/Cursor.app" {
		t.Fatalf("calls = %v", calls)
	}
}

func TestLaunchUnsupportedPlatform(t *testing.T) {
	err := (appEnv{goos: "windows"}).Launch(context.Background(), AppIdentity{
		BundleID: CursorBundleID,
		AppPath:  `C:\Cursor\Cursor.exe`,
	})
	if !errors.Is(err, ErrUnsupportedPlatform) {
		t.Fatalf("err = %v", err)
	}
}

func TestUnsupportedPlatformDoesNotSupportGracefulQuit(t *testing.T) {
	env := appEnv{goos: "windows"}
	if env.SupportsGracefulQuit() {
		t.Fatal("windows must not silently gain kill-based quit")
	}
}

func writeDelayExitHelper(t *testing.T, delay time.Duration, code int) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "helper.sh")
	script := fmt.Sprintf("#!/bin/sh\nsleep %.3f\nexit %d\n", delay.Seconds(), code)
	if err := os.WriteFile(path, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	return path
}
