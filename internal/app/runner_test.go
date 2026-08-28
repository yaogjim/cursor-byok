package app

import (
	"reflect"
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
