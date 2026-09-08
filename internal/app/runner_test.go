package app

import (
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"testing"
)

func TestWindowsAdditionalBrowserArgs(t *testing.T) {
	tests := []struct {
		name string
		env  string
		want []string
	}{
		{name: "unset"},
		{name: "enabled with one", env: "1", want: []string{"--no-sandbox"}},
		{name: "enabled with true", env: " true ", want: []string{"--no-sandbox"}},
		{name: "disabled", env: "false"},
		{name: "invalid", env: "yes"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Setenv(disableWebViewSandboxEnv, tt.env)
			if got := windowsAdditionalBrowserArgs(); !reflect.DeepEqual(got, tt.want) {
				t.Fatalf("windowsAdditionalBrowserArgs() = %v, want %v", got, tt.want)
			}
		})
	}
}

type recordingQuitShutdown struct {
	calls []string
}

func (r *recordingQuitShutdown) ShutdownForQuitFrom(initiator string) {
	r.calls = append(r.calls, initiator)
}

func TestTrayAndOnShutdownShareProxyShutdownEntry(t *testing.T) {
	proxy := &recordingQuitShutdown{}
	runTrayQuit(proxy, func() {})
	runOnShutdown(proxy)
	if len(proxy.calls) != 2 {
		t.Fatalf("calls = %v, want tray then on_shutdown", proxy.calls)
	}
	if proxy.calls[0] != "tray" || proxy.calls[1] != "on_shutdown" {
		t.Fatalf("calls = %v", proxy.calls)
	}
}

func TestRequestInteractiveProxyStartShowsWindowAndEmits(t *testing.T) {
	var shown int
	var events []string
	requestInteractiveProxyStart(func() { shown++ }, func(name string) {
		events = append(events, name)
	})
	if shown != 1 {
		t.Fatalf("shown = %d", shown)
	}
	if len(events) != 1 || events[0] != proxyStartRequestedEvent {
		t.Fatalf("events = %v", events)
	}
}

func TestRunAutoStartProxyDoesNotEmitInteractiveStart(t *testing.T) {
	started := 0
	err := runAutoStartProxy(func() error {
		started++
		return errors.New("cursor_restart_confirmation_required: 请确认保存工作后重启 Cursor 以应用配置")
	})
	if started != 1 {
		t.Fatalf("started = %d", started)
	}
	if err == nil {
		t.Fatal("auto-start must surface confirmation error")
	}
}

func TestRunnerSourceSeparatesAutoStartFromTrayConfirm(t *testing.T) {
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("caller")
	}
	src, err := os.ReadFile(filepath.Join(filepath.Dir(file), "runner.go"))
	if err != nil {
		t.Fatal(err)
	}
	text := string(src)
	startIdx := strings.Index(text, "startItem.OnClick")
	stopIdx := strings.Index(text, "stopItem.OnClick")
	if startIdx < 0 || stopIdx <= startIdx {
		t.Fatal("missing tray start/stop handlers")
	}
	trayStart := text[startIdx:stopIdx]
	if strings.Contains(trayStart, "StartProxy") {
		t.Fatal("tray start must not call StartProxy synchronously")
	}
	if !strings.Contains(trayStart, "requestInteractiveProxyStart") {
		t.Fatal("tray start must show window and emit interactive start")
	}
	autoIdx := strings.Index(text, "ApplicationStarted")
	if autoIdx < 0 || autoIdx > startIdx {
		t.Fatal("missing ApplicationStarted auto-start")
	}
	autoBlock := text[autoIdx:startIdx]
	if !strings.Contains(autoBlock, "runAutoStartProxy") || !strings.Contains(autoBlock, "StartProxy") {
		t.Fatal("auto-start must still call StartProxy")
	}
	if strings.Contains(autoBlock, "requestInteractiveProxyStart") {
		t.Fatal("auto-start must not emit interactive start popup")
	}
	if !strings.Contains(text, "mainWindowName") || !strings.Contains(text, "Name:") {
		t.Fatal("main window must be named for main-only event listen")
	}
}
