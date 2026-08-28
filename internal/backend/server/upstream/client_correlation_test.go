package upstream

import (
	"context"
	"net/http"
	"net/url"
	"testing"

	"cursor/internal/backend/server"
	"cursor/internal/observability"
	legacyruntime "cursor/internal/runtime"
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
	request.Header.Set("Authorization", "Bearer official-account")
	request.Header.Set("x-cursor-checksum", "official-checksum")

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
	if official.Header.Get("Authorization") != "Bearer official-account" || official.Header.Get("x-cursor-checksum") != "official-checksum" {
		t.Fatalf("upstream mode replaced official account identity: %v", official.Header)
	}
	relay := build("https://tab.leokun.cn/test")
	if relay.Header.Get(server.HeaderTraceID) != correlation.TraceID || relay.Header.Get(server.HeaderParentSpanID) != correlation.SpanID {
		t.Fatalf("relay correlation mismatch: %v", relay.Header)
	}
}

func TestBuildUpstreamRequestRewritesCursorIdentityOnlyInLocalMode(t *testing.T) {
	request := newUpstreamTestRequest(t)
	target, err := url.Parse("https://api2.cursor.sh/test")
	if err != nil {
		t.Fatal(err)
	}

	built, _, err := buildUpstreamRequest(&RequestContext{
		Request:   request,
		TargetURL: target,
		Method:    http.MethodPost,
		Headers:   request.Header.Clone(),
		Mode:      server.ModeLocal,
		Deps:      &Dependencies{},
	}, nil, ForwardOptions{})
	if err != nil {
		t.Fatal(err)
	}

	wantAuthorization := "Bearer " + legacyruntime.LocalRelayToken
	if built.Header.Get("Authorization") != wantAuthorization {
		t.Fatalf("authorization = %q, want local relay identity", built.Header.Get("Authorization"))
	}
	if built.Header.Get("x-cursor-checksum") != BuildCursorChecksum(wantAuthorization) {
		t.Fatal("local relay checksum does not match rewritten identity")
	}
}

func TestBuildUpstreamRequestAllowsAuthenticatedControlPlaneIdentity(t *testing.T) {
	request := newUpstreamTestRequest(t)
	target, err := url.Parse("https://api2.cursor.sh/test")
	if err != nil {
		t.Fatal(err)
	}
	const authorization = "Bearer control-plane-account"

	built, _, err := buildUpstreamRequest(&RequestContext{
		Request:   request,
		TargetURL: target,
		Method:    http.MethodPost,
		Headers:   request.Header.Clone(),
		Mode:      server.ModeLocal,
		Deps:      &Dependencies{},
	}, nil, ForwardOptions{
		PatchHeaders: func(headers http.Header) {
			headers.Set("Authorization", authorization)
			headers.Set("x-cursor-checksum", BuildCursorChecksum(authorization))
		},
	})
	if err != nil {
		t.Fatal(err)
	}

	if built.Header.Get("Authorization") != authorization {
		t.Fatalf("authorization = %q, want control-plane identity", built.Header.Get("Authorization"))
	}
	if built.Header.Get("x-cursor-checksum") != BuildCursorChecksum(authorization) {
		t.Fatal("control-plane checksum does not match account identity")
	}
}

func TestBuildUpstreamRequestPreservesInboundIdentityWhenRequested(t *testing.T) {
	request := newUpstreamTestRequest(t)
	target, err := url.Parse("https://api2.cursor.sh/aiserver.v1.AiService/AvailableModels")
	if err != nil {
		t.Fatal(err)
	}

	built, _, err := buildUpstreamRequest(&RequestContext{
		Request:   request,
		TargetURL: target,
		Method:    http.MethodPost,
		Headers:   request.Header.Clone(),
		Mode:      server.ModeLocal,
		Deps:      &Dependencies{},
	}, nil, ForwardOptions{PreserveInboundIdentity: true})
	if err != nil {
		t.Fatal(err)
	}

	if built.Header.Get("Authorization") != "Bearer incoming-identity" {
		t.Fatalf("authorization = %q, want inbound identity", built.Header.Get("Authorization"))
	}
	if built.Header.Get("x-cursor-checksum") != "incoming-checksum" {
		t.Fatalf("checksum = %q, want inbound checksum", built.Header.Get("x-cursor-checksum"))
	}
}

func newUpstreamTestRequest(t *testing.T) *http.Request {
	t.Helper()
	request, err := http.NewRequestWithContext(context.Background(), http.MethodPost, "http://backend.local/test", nil)
	if err != nil {
		t.Fatal(err)
	}
	request.Header.Set("Authorization", "Bearer incoming-identity")
	request.Header.Set("x-cursor-checksum", "incoming-checksum")
	return request
}
