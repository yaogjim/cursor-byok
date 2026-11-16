package forwarder

import (
	"net"
	"strings"
	"syscall"
	"testing"
	"time"

	modeladapter "cursor/internal/backend/agent/model"
)

// 上传失败必须能落到 provider terminal 事件里，否则超时只能靠猜。
func TestProviderTerminalFieldsExposeWroteRequestFailure(t *testing.T) {
	diag := &modeladapter.StreamDiagnostics{}
	diag.BeginHTTPAttempt()
	diag.RecordWroteRequest(time.Now(), &net.OpError{Op: "write", Net: "tcp", Err: syscall.EPIPE})
	stats := ProviderStreamStats{}
	applyProviderStreamDiagnostics(&stats, diag, nil, false)

	fields := providerTerminalFields("model-call-1", stats)
	if got, _ := fields["wrote_request_err"].(string); got != "broken_pipe" {
		t.Fatalf("wrote_request_err = %#v", fields["wrote_request_err"])
	}
	for key, value := range fields {
		if text, ok := value.(string); ok && strings.Contains(text, "write tcp") {
			t.Fatalf("raw write error leaked into %s: %q", key, text)
		}
	}
}

func TestProviderTerminalFieldsKeepSuccessWithoutWroteRequestErr(t *testing.T) {
	diag := &modeladapter.StreamDiagnostics{}
	diag.BeginHTTPAttempt()
	diag.RecordWroteRequest(time.Now(), nil)
	stats := ProviderStreamStats{}
	applyProviderStreamDiagnostics(&stats, diag, nil, false)

	fields := providerTerminalFields("model-call-1", stats)
	if got, _ := fields["wrote_request_err"].(string); got != "not_recorded" {
		t.Fatalf("successful write should not report an error: %#v", fields["wrote_request_err"])
	}
	if stats.WroteRequestAt.IsZero() {
		t.Fatal("successful write lost its completion time")
	}
}
