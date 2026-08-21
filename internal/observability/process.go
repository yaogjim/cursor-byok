package observability

import (
	"context"
	"reflect"
	"sync"
)

// Sink is the process-wide capture destination used when a caller cannot
// thread an explicit recorder, such as the MITM proxy constructed before
// backend observability exists.
type Sink interface {
	Record(context.Context, Capture) bool
}

var (
	processMu   sync.RWMutex
	processSink Sink
)

// SetProcessSink installs the sink used by ProcessSink. Production registers
// the backend Controller so MITM events still reach traces/*/events.jsonl
// when the proxy was constructed without an explicit capture argument.
func SetProcessSink(sink Sink) {
	processMu.Lock()
	defer processMu.Unlock()
	if isNilSink(sink) {
		processSink = nil
		return
	}
	processSink = sink
}

// ClearProcessSink removes sink if it is the current process sink.
func ClearProcessSink(sink Sink) {
	if isNilSink(sink) {
		return
	}
	processMu.Lock()
	defer processMu.Unlock()
	if processSink == sink {
		processSink = nil
	}
}

// ProcessSink returns the current process-wide capture destination.
func ProcessSink() Sink {
	processMu.RLock()
	defer processMu.RUnlock()
	return processSink
}

func isNilSink(sink Sink) bool {
	if sink == nil {
		return true
	}
	value := reflect.ValueOf(sink)
	switch value.Kind() {
	case reflect.Pointer, reflect.Interface, reflect.Map, reflect.Slice, reflect.Func:
		return value.IsNil()
	default:
		return false
	}
}
