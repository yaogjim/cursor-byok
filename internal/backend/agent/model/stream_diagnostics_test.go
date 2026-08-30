package modeladapter

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"syscall"
	"testing"
	"time"
)

func TestClassifyStreamCloseCauseTypedCauses(t *testing.T) {
	t.Parallel()
	resetErr := &net.OpError{Op: "read", Net: "tcp", Err: syscall.ECONNRESET}
	tests := []struct {
		name string
		err  error
		want string
	}{
		{name: "nil", err: nil, want: StreamCloseCauseNotRecorded},
		{name: "eof", err: io.EOF, want: StreamCloseCauseEOF},
		{name: "unexpected eof", err: io.ErrUnexpectedEOF, want: StreamCloseCauseUnexpectedEOF},
		{name: "reset errno", err: resetErr, want: StreamCloseCauseReset},
		{name: "tls record header", err: tls.RecordHeaderError{Msg: "first record does not look like a TLS handshake"}, want: StreamCloseCauseTLS},
		{name: "tls unknown authority", err: x509.UnknownAuthorityError{}, want: StreamCloseCauseTLS},
		{name: "idle timeout", err: &StreamIdleTimeoutError{Timeout: time.Second}, want: StreamCloseCauseIdleTimeout},
		{name: "context canceled", err: context.Canceled, want: StreamCloseCauseContextCanceled},
		{name: "deadline", err: context.DeadlineExceeded, want: StreamCloseCauseDeadline},
		{name: "json decode", err: &json.SyntaxError{}, want: StreamCloseCauseStreamDecode},
		{name: "http status", err: &HTTPStatusError{StatusCode: 524}, want: StreamCloseCauseHTTPStatus},
		{name: "missing marker", err: &StreamTruncatedError{}, want: StreamCloseCauseNotRecorded},
		{name: "truncated eof", err: &StreamTruncatedError{Err: io.EOF}, want: StreamCloseCauseEOF},
		{name: "truncated reset", err: &StreamTruncatedError{Err: resetErr}, want: StreamCloseCauseReset},
		{name: "plain reset string", err: errors.New("connection reset by peer"), want: StreamCloseCauseUnknown},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := ClassifyStreamCloseCause(test.err); got != test.want {
				t.Fatalf("ClassifyStreamCloseCause() = %q, want %q", got, test.want)
			}
		})
	}
}

func TestClassifyStreamCloseCauseDoesNotGuessFromStrings(t *testing.T) {
	t.Parallel()
	for _, text := range []string{
		"connection reset by peer",
		"connection reset",
		"tls: handshake failure",
		"first record does not look like a TLS handshake",
		"i/o timeout",
		"connection refused",
	} {
		got := ClassifyStreamCloseCause(errors.New(text))
		if got != StreamCloseCauseUnknown {
			t.Fatalf("string %q classified as %q, want unknown", text, got)
		}
	}
}

func TestRetryingStreamBodyHeaderOnlyTimeline(t *testing.T) {
	diag := &StreamDiagnostics{}
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		writer.Header().Set("Content-Type", "text/event-stream")
		writer.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	retry := instantRetry()
	retry.diagnostics = diag
	resp, err := doProviderRequest(context.Background(), server.Client(), "openai", "request-id", "model-call-id", func(ctx context.Context) (*http.Request, error) {
		return http.NewRequestWithContext(ctx, http.MethodPost, server.URL, strings.NewReader("{}"))
	}, nil, retry)
	if err != nil {
		t.Fatalf("doProviderRequest() error = %v", err)
	}
	body := newRetryingStreamBody(context.Background(), server.Client(), "openai", "request-id", "model-call-id", func(ctx context.Context) (*http.Request, error) {
		return http.NewRequestWithContext(ctx, http.MethodPost, server.URL, strings.NewReader("{}"))
	}, resp.Body, responseRetryState(resp), nil, retry, func() bool { return false })
	payload, readErr := io.ReadAll(body)
	_ = body.Close()
	if len(payload) != 0 {
		t.Fatalf("header-only payload = %q", payload)
	}
	if readErr != nil && ClassifyStreamCloseCause(readErr) != StreamCloseCauseEOF && ClassifyStreamCloseCause(readErr) != StreamCloseCauseStreamDecode {
		t.Fatalf("ReadAll() error = %v", readErr)
	}
	snap := diag.Snapshot()
	if snap.HeaderAt.IsZero() || snap.HTTPStatus != http.StatusOK {
		t.Fatalf("header timeline = %#v", snap)
	}
	if !snap.FirstByteAt.IsZero() || snap.RawBytesObserved {
		t.Fatalf("header-only recorded body bytes: %#v", snap)
	}
	if snap.BodyEndAt.IsZero() {
		t.Fatal("header-only missing body_end_at")
	}
	if snap.CloseCause != StreamCloseCauseEOF {
		t.Fatalf("header-only close_cause = %q, want eof", snap.CloseCause)
	}
	if snap.TransportOutcome != TransportOutcomeSucceeded {
		t.Fatalf("header-only transport = %q, want succeeded", snap.TransportOutcome)
	}
	if !snap.HeaderAt.Before(snap.BodyEndAt) && !snap.HeaderAt.Equal(snap.BodyEndAt) {
		t.Fatalf("header_at %v must not be after body_end_at %v", snap.HeaderAt, snap.BodyEndAt)
	}
}

func TestRecordHeader2xxDoesNotTreat5xxAsTransportSuccess(t *testing.T) {
	t.Parallel()
	diag := &StreamDiagnostics{}
	diag.RecordHeader(524, 1, time.Now())
	diag.RecordClose(&HTTPStatusError{StatusCode: 524})
	snap := diag.Snapshot()
	if snap.HTTPStatus != 524 || snap.HeaderAt.IsZero() {
		t.Fatalf("524 header snapshot = %#v", snap)
	}
	if !snap.FirstByteAt.IsZero() || snap.RawBytesObserved {
		t.Fatalf("524 headers invented body bytes: %#v", snap)
	}
	if snap.CloseCause != StreamCloseCauseHTTPStatus {
		t.Fatalf("524 close_cause = %q, want http_status", snap.CloseCause)
	}
	if snap.TransportOutcome != TransportOutcomeFailed {
		t.Fatalf("non-2xx transport = %q, want failed", snap.TransportOutcome)
	}
}

func TestRecordHTTPResponsePreservesAutomaticGzipMetadata(t *testing.T) {
	t.Parallel()
	diag := &StreamDiagnostics{}
	diag.RecordHTTPResponse(&http.Response{
		StatusCode:    http.StatusOK,
		Proto:         "HTTP/2.0",
		Header:        make(http.Header),
		Uncompressed:  true,
		ContentLength: -1,
	}, 1, time.Now())
	snap := diag.Snapshot()
	if snap.HTTPProtocol != "HTTP/2.0" || snap.ContentEncoding != "gzip" || !snap.AutoDecompressed || snap.ContentLength != -1 {
		t.Fatalf("automatic gzip metadata = %#v", snap)
	}
}

func TestPrior2xxSucceededDoesNotLingerAfterLaterHTTPFailure(t *testing.T) {
	t.Parallel()
	diag := &StreamDiagnostics{}
	diag.RecordHeader(http.StatusOK, 1, time.Now())
	if diag.Snapshot().TransportOutcome != TransportOutcomeSucceeded {
		t.Fatalf("2xx transport = %q", diag.Snapshot().TransportOutcome)
	}
	diag.RecordHeader(524, 2, time.Now())
	diag.RecordClose(&HTTPStatusError{StatusCode: 524, Attempt: 2})
	snap := diag.Snapshot()
	if snap.HTTPStatus != 524 || snap.HTTPAttempt != 2 {
		t.Fatalf("later attempt header = %#v", snap)
	}
	if snap.CloseCause != StreamCloseCauseHTTPStatus {
		t.Fatalf("close_cause = %q, want http_status", snap.CloseCause)
	}
	if snap.TransportOutcome != TransportOutcomeFailed {
		t.Fatalf("transport_outcome = %q, want failed after later 524", snap.TransportOutcome)
	}
	if snap.BodyEndAt.IsZero() {
		t.Fatal("HTTP failure missing body_end_at")
	}
}

func TestDoProviderRequestNon2xxRecordsHTTPStatusClose(t *testing.T) {
	diag := &StreamDiagnostics{}
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		http.Error(writer, "nope", http.StatusBadRequest)
	}))
	defer server.Close()

	retry := instantRetry()
	retry.diagnostics = diag
	resp, err := doProviderRequest(context.Background(), server.Client(), "openai", "request-id", "model-call-id", func(ctx context.Context) (*http.Request, error) {
		return http.NewRequestWithContext(ctx, http.MethodPost, server.URL, strings.NewReader("{}"))
	}, nil, retry)
	if err != nil {
		t.Fatalf("doProviderRequest() error = %v", err)
	}
	if resp == nil || resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("resp = %#v", resp)
	}
	_ = resp.Body.Close()
	snap := diag.Snapshot()
	if snap.HeaderAt.IsZero() || snap.HTTPStatus != http.StatusBadRequest {
		t.Fatalf("400 header timeline = %#v", snap)
	}
	if snap.BodyEndAt.IsZero() {
		t.Fatal("non-2xx missing body_end_at")
	}
	if snap.CloseCause != StreamCloseCauseHTTPStatus {
		t.Fatalf("close_cause = %q, want http_status", snap.CloseCause)
	}
	if snap.TransportOutcome != TransportOutcomeFailed {
		t.Fatalf("transport_outcome = %q, want failed", snap.TransportOutcome)
	}
}

func TestOpenAINon2xxRecordsHTTPStatusClose(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		http.Error(writer, "nope", http.StatusBadRequest)
	}))
	defer server.Close()
	diag := &StreamDiagnostics{}
	adapter := &OpenAIAdapter{client: server.Client(), retry: instantRetry()}
	err := adapter.Stream(context.Background(), StreamRequest{
		RequestID:         "request-1",
		ModelCallID:       "model-call-1",
		BaseURL:           server.URL,
		APIKey:            "test-key",
		ProviderModelID:   "gpt-test",
		Messages:          []Message{{Role: "user", Content: "hello"}},
		MaxTokens:         128,
		StreamDiagnostics: diag,
	}, func(event ModelEvent) error { return nil })
	var httpErr *HTTPStatusError
	if !errors.As(err, &httpErr) || httpErr.StatusCode != http.StatusBadRequest {
		t.Fatalf("Stream() error = %v, want HTTP 400", err)
	}
	snap := diag.Snapshot()
	if snap.HTTPStatus != http.StatusBadRequest || snap.BodyEndAt.IsZero() {
		t.Fatalf("adapter 400 snapshot = %#v", snap)
	}
	if snap.CloseCause != StreamCloseCauseHTTPStatus || snap.TransportOutcome != TransportOutcomeFailed {
		t.Fatalf("adapter 400 close/transport = %#v", snap)
	}
}

func TestIdleWatchdogDoesNotRewriteCompletedEOF(t *testing.T) {
	parent, cancel := context.WithCancelCause(context.Background())
	defer cancel(nil)
	diag := &StreamDiagnostics{}
	diag.RecordHeader(http.StatusOK, 1, time.Now())
	diag.RecordClose(io.EOF)
	watchdog := &providerStreamIdleWatchdog{
		ctx:     parent,
		cancel:  cancel,
		timeout: time.Hour,
		err:     providerStreamIdleTimeoutError(time.Hour),
	}
	watchdog.AttachDiagnostics(diag)
	watchdog.expire()
	if watchdog.Err() != nil {
		t.Fatalf("expire after EOF set idle err: %v", watchdog.Err())
	}
	snap := diag.Snapshot()
	if snap.CloseCause != StreamCloseCauseEOF {
		t.Fatalf("close_cause = %q, want eof", snap.CloseCause)
	}
	if snap.TransportOutcome != TransportOutcomeSucceeded {
		t.Fatalf("transport_outcome = %q, want succeeded", snap.TransportOutcome)
	}
}

func TestIdleWatchdogStopPreventsIdleClose(t *testing.T) {
	parent, cancel := context.WithCancelCause(context.Background())
	defer cancel(nil)
	diag := &StreamDiagnostics{}
	diag.RecordHeader(http.StatusOK, 1, time.Now())
	diag.RecordClose(io.EOF)
	watchdog := &providerStreamIdleWatchdog{
		ctx:     parent,
		cancel:  cancel,
		timeout: time.Hour,
		err:     providerStreamIdleTimeoutError(time.Hour),
	}
	watchdog.AttachDiagnostics(diag)
	watchdog.Stop()
	watchdog.expire()
	if watchdog.Err() != nil {
		t.Fatalf("stopped watchdog reported idle: %v", watchdog.Err())
	}
	if diag.Snapshot().CloseCause != StreamCloseCauseEOF {
		t.Fatalf("stop+expire close_cause = %q", diag.Snapshot().CloseCause)
	}
}

func TestPreferCloseCauseIdleDoesNotBeatEOF(t *testing.T) {
	t.Parallel()
	if got := PreferCloseCause(StreamCloseCauseEOF, StreamCloseCauseIdleTimeout); got != StreamCloseCauseEOF {
		t.Fatalf("PreferCloseCause(eof, idle) = %q", got)
	}
	if got := PreferCloseCause("", StreamCloseCauseIdleTimeout); got != StreamCloseCauseIdleTimeout {
		t.Fatalf("PreferCloseCause(empty, idle) = %q", got)
	}
	if got := PreferCloseCause(StreamCloseCauseIdleTimeout, StreamCloseCauseContextCanceled); got != StreamCloseCauseIdleTimeout {
		t.Fatalf("PreferCloseCause(idle, canceled) = %q", got)
	}
}

func TestRetryingStreamBodyRecordsRawBytesAndBodyEnd(t *testing.T) {
	diag := &StreamDiagnostics{}
	retry := instantRetry()
	retry.diagnostics = diag
	body := newRetryingStreamBody(context.Background(), &http.Client{}, "openai", "request-id", "model-call-id", func(ctx context.Context) (*http.Request, error) {
		return http.NewRequestWithContext(ctx, http.MethodPost, "https://example.test", nil)
	}, io.NopCloser(strings.NewReader("hello-world")), providerRetryState{attempt: 1}, nil, retry, func() bool { return false })
	payload, err := io.ReadAll(body)
	_ = body.Close()
	if err != nil || string(payload) != "hello-world" {
		t.Fatalf("payload=%q err=%v", payload, err)
	}
	snap := diag.Snapshot()
	if snap.FirstByteAt.IsZero() || snap.LastByteAt.IsZero() || snap.BodyEndAt.IsZero() {
		t.Fatalf("byte timeline missing: %#v", snap)
	}
	if snap.LastEffectiveContentAt.IsZero() == false {
		t.Fatalf("raw bytes must not mark effective content: %#v", snap)
	}
	if snap.CloseCause != StreamCloseCauseEOF {
		t.Fatalf("close_cause = %q, want eof", snap.CloseCause)
	}
}

func TestRetryingStreamBodyPartialUnexpectedEOF(t *testing.T) {
	diag := &StreamDiagnostics{}
	retry := instantRetry()
	retry.diagnostics = diag
	reader, writer := io.Pipe()
	go func() {
		_, _ = writer.Write([]byte("partial-bytes"))
		_ = writer.CloseWithError(io.ErrUnexpectedEOF)
	}()
	body := newRetryingStreamBody(context.Background(), &http.Client{}, "openai", "request-id", "model-call-id", func(ctx context.Context) (*http.Request, error) {
		return http.NewRequestWithContext(ctx, http.MethodPost, "https://example.test", nil)
	}, reader, providerRetryState{attempt: 1}, nil, retry, func() bool { return false })
	_, err := io.ReadAll(body)
	_ = body.Close()
	if !errors.Is(err, io.ErrUnexpectedEOF) {
		t.Fatalf("err=%v, want unexpected EOF", err)
	}
	snap := diag.Snapshot()
	if !snap.RawBytesObserved || snap.FirstByteAt.IsZero() || snap.BodyEndAt.IsZero() {
		t.Fatalf("partial close timeline = %#v", snap)
	}
	if snap.CloseCause != StreamCloseCauseUnexpectedEOF {
		t.Fatalf("close_cause = %q, want unexpected_eof", snap.CloseCause)
	}
}

func TestIdleWatchdogRecordsIdleTimeoutNotRawBytes(t *testing.T) {
	parent, cancel := context.WithCancelCause(context.Background())
	defer cancel(nil)
	diag := &StreamDiagnostics{}
	watchdog := &providerStreamIdleWatchdog{
		ctx:     parent,
		cancel:  cancel,
		timeout: 40 * time.Millisecond,
		err:     providerStreamIdleTimeoutError(40 * time.Millisecond),
	}
	watchdog.timer = time.AfterFunc(watchdog.timeout, watchdog.expire)
	watchdog.AttachDiagnostics(diag)
	diag.RecordBytes(4, time.Now())
	select {
	case <-parent.Done():
	case <-time.After(time.Second):
		t.Fatal("watchdog did not expire")
	}
	if ClassifyStreamCloseCause(watchdog.Err()) != StreamCloseCauseIdleTimeout {
		t.Fatalf("watchdog err cause = %q", ClassifyStreamCloseCause(watchdog.Err()))
	}
	snap := diag.Snapshot()
	if snap.CloseCause != StreamCloseCauseIdleTimeout {
		t.Fatalf("close_cause = %q, want idle_timeout", snap.CloseCause)
	}
	if snap.LastEffectiveContentAt.IsZero() == false {
		t.Fatalf("idle timeout after raw bytes must not invent effective content: %#v", snap)
	}
}

func TestIdleWatchdogEffectiveContentResetsTimer(t *testing.T) {
	parent, cancel := context.WithCancelCause(context.Background())
	defer cancel(nil)
	diag := &StreamDiagnostics{}
	watchdog := &providerStreamIdleWatchdog{
		ctx:     parent,
		cancel:  cancel,
		timeout: 60 * time.Millisecond,
		err:     providerStreamIdleTimeoutError(60 * time.Millisecond),
	}
	watchdog.timer = time.AfterFunc(watchdog.timeout, watchdog.expire)
	watchdog.AttachDiagnostics(diag)
	time.Sleep(30 * time.Millisecond)
	watchdog.MarkEffectiveContent()
	time.Sleep(30 * time.Millisecond)
	if watchdog.Err() != nil {
		t.Fatalf("watchdog expired despite effective content: %v", watchdog.Err())
	}
	if diag.Snapshot().LastEffectiveContentAt.IsZero() {
		t.Fatal("effective content timestamp not recorded")
	}
	watchdog.Stop()
}

func TestOpenAIHeaderOnlyDoesNotCompleteProtocol(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		writer.Header().Set("Content-Type", "text/event-stream")
		writer.WriteHeader(http.StatusOK)
	}))
	defer server.Close()
	diag := &StreamDiagnostics{}
	adapter := &OpenAIAdapter{client: server.Client(), retry: instantRetry()}
	events := make([]ModelEvent, 0)
	err := adapter.Stream(context.Background(), StreamRequest{
		RequestID:         "request-1",
		ModelCallID:       "model-call-1",
		BaseURL:           server.URL,
		APIKey:            "test-key",
		ProviderModelID:   "gpt-test",
		Messages:          []Message{{Role: "user", Content: "hello"}},
		MaxTokens:         128,
		StreamDiagnostics: diag,
	}, func(event ModelEvent) error {
		events = append(events, event)
		return nil
	})
	assertOpenAIStreamTruncated(t, err, "EOF")
	if len(events) != 0 {
		t.Fatalf("header-only emitted events: %#v", events)
	}
	snap := diag.Snapshot()
	if snap.HTTPStatus != http.StatusOK || snap.HeaderAt.IsZero() {
		t.Fatalf("header snapshot = %#v", snap)
	}
	if snap.RawBytesObserved || !snap.FirstByteAt.IsZero() {
		t.Fatalf("header-only must not invent body bytes: %#v", snap)
	}
	if snap.CloseCause != StreamCloseCauseEOF {
		t.Fatalf("header-only close_cause = %q, want eof", snap.CloseCause)
	}
	if snap.TransportOutcome != TransportOutcomeSucceeded {
		t.Fatalf("200 headers must mark transport succeeded, got %q", snap.TransportOutcome)
	}
}

func TestOpenAICompletionMarkerVersusMissingMarker(t *testing.T) {
	t.Run("completed", func(t *testing.T) {
		server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
			writer.Header().Set("Content-Type", "text/event-stream")
			_, _ = io.WriteString(writer, "data: {\"model\":\"gpt-test\",\"choices\":[{\"delta\":{\"content\":\"done\"},\"finish_reason\":\"stop\"}]}\n\n")
			_, _ = io.WriteString(writer, "data: [DONE]\n\n")
		}))
		defer server.Close()
		diag := &StreamDiagnostics{}
		adapter := &OpenAIAdapter{client: server.Client(), retry: instantRetry()}
		events, err := collectOpenAIStreamWithDiagnostics(t, adapter, server.URL, "/v1/chat/completions", diag)
		if err != nil {
			t.Fatalf("completed stream error = %v", err)
		}
		assertOpenAIEventKindCount(t, events, ModelEventKindTurnFinished, 1)
		snap := diag.Snapshot()
		if snap.HTTPStatus != http.StatusOK || snap.CloseCause != StreamCloseCauseEOF {
			t.Fatalf("completed snapshot = %#v", snap)
		}
		if snap.LastEffectiveContentAt.IsZero() || snap.FirstByteAt.IsZero() || snap.BodyEndAt.IsZero() {
			t.Fatalf("completed timeline incomplete: %#v", snap)
		}
	})
	t.Run("missing marker", func(t *testing.T) {
		server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
			writer.Header().Set("Content-Type", "text/event-stream")
			_, _ = io.WriteString(writer, "data: {\"model\":\"gpt-test\",\"choices\":[{\"delta\":{\"content\":\"hello\"},\"finish_reason\":\"\"}]}\n\n")
		}))
		defer server.Close()
		diag := &StreamDiagnostics{}
		adapter := &OpenAIAdapter{client: server.Client(), retry: instantRetry()}
		events, err := collectOpenAIStreamWithDiagnostics(t, adapter, server.URL, "/v1/chat/completions", diag)
		assertOpenAIStreamTruncated(t, err, "missing completion marker")
		assertOpenAIEventKindCount(t, events, ModelEventKindTextDelta, 1)
		assertOpenAIEventKindCount(t, events, ModelEventKindTurnFinished, 0)
		snap := diag.Snapshot()
		if snap.HTTPStatus != http.StatusOK {
			t.Fatalf("missing marker still received 200 headers: %#v", snap)
		}
		if snap.CloseCause != StreamCloseCauseEOF {
			t.Fatalf("missing marker close_cause = %q, want eof", snap.CloseCause)
		}
		if snap.LastEffectiveContentAt.IsZero() {
			t.Fatal("text delta must record last_effective_content_at")
		}
		if snap.TransportOutcome != TransportOutcomeSucceeded {
			t.Fatalf("missing marker transport = %q, want succeeded", snap.TransportOutcome)
		}
	})
}

func TestOpenAIRawCommentBytesVersusEffectiveContent(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		writer.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(writer, ": ping\n\n")
	}))
	defer server.Close()
	diag := &StreamDiagnostics{}
	adapter := &OpenAIAdapter{client: server.Client(), retry: instantRetry()}
	events, err := collectOpenAIStreamWithDiagnostics(t, adapter, server.URL, "/v1/chat/completions", diag)
	assertOpenAIStreamTruncated(t, err, "missing completion marker")
	if len(events) != 0 {
		t.Fatalf("comment-only emitted events: %#v", events)
	}
	snap := diag.Snapshot()
	if !snap.RawBytesObserved || snap.FirstByteAt.IsZero() {
		t.Fatalf("comment bytes missing from raw timeline: %#v", snap)
	}
	if !snap.LastEffectiveContentAt.IsZero() {
		t.Fatalf("SSE comment must not count as effective content: %#v", snap)
	}
}

func collectOpenAIStreamWithDiagnostics(t *testing.T, adapter *OpenAIAdapter, baseURL, endpoint string, diag *StreamDiagnostics) ([]ModelEvent, error) {
	t.Helper()
	events := make([]ModelEvent, 0, 8)
	err := adapter.Stream(context.Background(), StreamRequest{
		RequestID:         "request-1",
		RunID:             "run-1",
		ModelCallID:       "model-call-1",
		BaseURL:           baseURL,
		APIKey:            "test-key",
		ProviderModelID:   "gpt-test",
		OpenAIEndpoint:    endpoint,
		Messages:          []Message{{Role: "user", Content: "hello"}},
		MaxTokens:         128,
		StreamDiagnostics: diag,
	}, func(event ModelEvent) error {
		events = append(events, event)
		return nil
	})
	return events, err
}
