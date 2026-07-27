package upstream

import (
	"context"
	"net/http"
	"net/url"
	"testing"

	"cursor/internal/backend/server"
	"cursor/internal/observability"
)

func TestBuildUpstreamRequestOnlyPropagatesCorrelationToRelay(t *testing.T) {
	correlation := observability.Correlation{TraceID: "trace-123", SpanID: "span-123"}
	request, err := http.NewRequestWithContext(
		observability.WithCorrelation(context.Background(), correlation),
		http.MethodPost,
		"http://backend.local/test",
		nil,
	)
	if err != nil {
		t.Fatal(err)
	}
	request.Header.Set(server.HeaderTraceID, "untrusted-trace")
	request.Header.Set(server.HeaderParentSpanID, "untrusted-span")

	build := func(rawTarget string) *http.Request {
		t.Helper()
		target, err := url.Parse(rawTarget)
		if err != nil {
			t.Fatal(err)
		}
		built, _, err := buildUpstreamRequest(&RequestContext{
			Request:   request,
			TargetURL: target,
			Method:    http.MethodPost,
			Headers:   request.Header.Clone(),
			Mode:      server.ModeUpstream,
			Deps:      &Dependencies{},
		}, nil, ForwardOptions{})
		if err != nil {
			t.Fatal(err)
		}
		return built
	}

	official := build("https://api2.cursor.sh/test")
	if official.Header.Get(server.HeaderTraceID) != "" || official.Header.Get(server.HeaderParentSpanID) != "" {
		t.Fatalf("internal headers leaked to official upstream: %v", official.Header)
	}
	relay := build("https://tab.leokun.cn/test")
	if relay.Header.Get(server.HeaderTraceID) != correlation.TraceID || relay.Header.Get(server.HeaderParentSpanID) != correlation.SpanID {
		t.Fatalf("relay correlation mismatch: %v", relay.Header)
	}
}
