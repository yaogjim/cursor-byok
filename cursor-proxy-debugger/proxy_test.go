package proxydebugger

import (
	"strings"
	"testing"
)

func TestValidateLoopbackAddress(t *testing.T) {
	t.Parallel()

	for _, address := range []string{"127.0.0.1:0", "localhost:9090", "[::1]:9091"} {
		address := address
		t.Run("accept_"+address, func(t *testing.T) {
			t.Parallel()
			if err := validateLoopbackAddress("测试服务", address); err != nil {
				t.Fatalf("validateLoopbackAddress(%q): %v", address, err)
			}
		})
	}

	for _, address := range []string{"0.0.0.0:9090", "[::]:9090", "192.0.2.10:9090", ":9090", "invalid"} {
		address := address
		t.Run("reject_"+address, func(t *testing.T) {
			t.Parallel()
			if err := validateLoopbackAddress("测试服务", address); err == nil {
				t.Fatalf("validateLoopbackAddress(%q) unexpectedly succeeded", address)
			}
		})
	}
}

func TestNewRejectsNonLoopbackListeners(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		config    Config
		wantError string
	}{
		{
			name:      "proxy",
			config:    Config{ProxyAddr: "0.0.0.0:9090"},
			wantError: "代理只能监听本机回环地址",
		},
		{
			name:      "ui",
			config:    Config{UIAddr: "0.0.0.0:9091"},
			wantError: "调试界面只能监听本机回环地址",
		},
	}

	for _, test := range tests {
		test := test
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			_, err := New(test.config)
			if err == nil || !strings.Contains(err.Error(), test.wantError) {
				t.Fatalf("New() error = %v, want containing %q", err, test.wantError)
			}
		})
	}
}
