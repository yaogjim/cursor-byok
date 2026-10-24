package observability

import (
	"fmt"
	"strings"
	"time"
)

const networkDiagnosticWindow = 30 * time.Second
const maxNetworkDiagnosticKeys = 128

type repeatedDiagnostic struct {
	event Event
	count uint64
}

// 仅聚合无会话关联的已知网络噪声；模型、工具和压缩失败始终逐条保留。
func networkDiagnosticKey(event Event) string {
	if event.ConversationID != "" || event.ModelCallID != "" || event.ToolCallID != "" {
		return ""
	}
	known := event.Event == "tls_handshake_failed" && event.ErrorCategory == "client_unknown_ca"
	if !known {
		return ""
	}
	return strings.Join([]string{event.Layer, event.Event, event.ErrorCategory, event.Route,
		fmt.Sprint(event.Fields["host"]), fmt.Sprint(event.Fields["target_host"]), fmt.Sprint(event.Fields["tls_role"])}, "\x00")
}

// 调用方持 recorder.mu；只在首条事件成功入队后注册该键。
func (recorder *Recorder) suppressRepeatedDiagnosticLocked(key string) bool {
	if key == "" {
		return false
	}
	if previous := recorder.repeatedDiagnostics[key]; previous != nil {
		previous.count++
		return true
	}
	return false
}

func (recorder *Recorder) trackDiagnosticLocked(key string, event Event) {
	if key == "" || len(recorder.repeatedDiagnostics) >= maxNetworkDiagnosticKeys {
		return
	}
	if recorder.repeatedDiagnostics == nil {
		recorder.repeatedDiagnostics = make(map[string]*repeatedDiagnostic)
	}
	recorder.repeatedDiagnostics[key] = &repeatedDiagnostic{event: event}
}

// 写入独立计数摘要而非静默丢弃；仅由写入协程调用，正常关闭也会补写尾批。
func (recorder *Recorder) flushRepeatedDiagnostics(sequence uint64) uint64 {
	recorder.mu.Lock()
	pending := recorder.repeatedDiagnostics
	recorder.repeatedDiagnostics = nil
	recorder.mu.Unlock()
	for _, repeated := range pending {
		if repeated.count == 0 {
			continue
		}
		event := repeated.event
		event.Timestamp = time.Now().UTC()
		event.Fields = make(map[string]any, len(repeated.event.Fields)+1)
		for key, value := range repeated.event.Fields {
			event.Fields[key] = value
		}
		event.Fields["repeated_count"] = repeated.count
		sequence = recorder.safeWriteCapture(sequence+1, Capture{Event: event})
	}
	return sequence
}
