package server

import (
	"bufio"
	"fmt"
	"net"
	"net/http"
)

type responseWrittenTracker interface {
	ResponseWritten() bool
}

type trackedResponseWriter struct {
	http.ResponseWriter
	wroteHeader  bool
	statusCode   int
	bytesWritten int64
}

func newTrackedResponseWriter(writer http.ResponseWriter) *trackedResponseWriter {
	return &trackedResponseWriter{ResponseWriter: writer}
}

func (writer *trackedResponseWriter) WriteHeader(statusCode int) {
	if writer == nil || writer.ResponseWriter == nil {
		return
	}
	if writer.wroteHeader {
		return
	}
	writer.wroteHeader = true
	writer.statusCode = statusCode
	writer.ResponseWriter.WriteHeader(statusCode)
}

func (writer *trackedResponseWriter) Write(payload []byte) (int, error) {
	if writer == nil || writer.ResponseWriter == nil {
		return 0, http.ErrAbortHandler
	}
	if !writer.wroteHeader {
		writer.WriteHeader(http.StatusOK)
	}
	written, err := writer.ResponseWriter.Write(payload)
	writer.bytesWritten += int64(written)
	return written, err
}

func (writer *trackedResponseWriter) Flush() {
	if writer == nil || writer.ResponseWriter == nil {
		return
	}
	if !writer.wroteHeader {
		writer.WriteHeader(http.StatusOK)
	}
	if flusher, ok := writer.ResponseWriter.(http.Flusher); ok {
		flusher.Flush()
	}
}

func (writer *trackedResponseWriter) Hijack() (net.Conn, *bufio.ReadWriter, error) {
	if writer == nil || writer.ResponseWriter == nil {
		return nil, nil, fmt.Errorf("response writer is nil")
	}
	hijacker, ok := writer.ResponseWriter.(http.Hijacker)
	if !ok {
		return nil, nil, fmt.Errorf("response writer does not support hijacking")
	}
	writer.wroteHeader = true
	return hijacker.Hijack()
}

func (writer *trackedResponseWriter) Unwrap() http.ResponseWriter {
	if writer == nil {
		return nil
	}
	return writer.ResponseWriter
}

func (writer *trackedResponseWriter) ResponseWritten() bool {
	return writer != nil && writer.wroteHeader
}

func (writer *trackedResponseWriter) StatusCode() int {
	if writer == nil || !writer.wroteHeader {
		return http.StatusOK
	}
	return writer.statusCode
}

func (writer *trackedResponseWriter) BytesWritten() int64 {
	if writer == nil {
		return 0
	}
	return writer.bytesWritten
}

func responseWriterHasWrittenHeader(writer http.ResponseWriter) bool {
	for writer != nil {
		if tracker, ok := writer.(responseWrittenTracker); ok {
			if tracker.ResponseWritten() {
				return true
			}
		}
		unwrapper, ok := writer.(interface{ Unwrap() http.ResponseWriter })
		if !ok {
			return false
		}
		next := unwrapper.Unwrap()
		if next == nil || next == writer {
			return false
		}
		writer = next
	}
	return false
}
