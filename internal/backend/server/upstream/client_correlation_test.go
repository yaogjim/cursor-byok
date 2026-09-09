package upstream

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

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

func TestHandleMockOAuthEchoesKnownPlaceholderJSON(t *testing.T) {
	body := []byte(`{"grant_type":"refresh_token","refresh_token":"` + legacyruntime.InjectAuthToken + `"}`)
	recorder, err := invokeMockOAuth(t, oauthInvokeOptions{
		contentType:   "application/json",
		body:          body,
		authorization: catalogTestInboundAuth,
	})
	if err != nil {
		t.Fatalf("mock oauth: %v", err)
	}
	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d", recorder.Code)
	}
	var payload map[string]any
	if err := json.Unmarshal(recorder.Body.Bytes(), &payload); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if payload["access_token"] != legacyruntime.InjectAuthToken || payload["id_token"] != legacyruntime.InjectAuthToken {
		t.Fatalf("placeholder echo = %#v", payload)
	}
}

func TestHandleMockOAuthEchoesKnownPlaceholderForm(t *testing.T) {
	body := []byte("grant_type=refresh_token&refresh_token=" + url.QueryEscape(legacyruntime.InjectAuthToken))
	recorder, err := invokeMockOAuth(t, oauthInvokeOptions{
		contentType: "application/x-www-form-urlencoded",
		body:        body,
	})
	if err != nil {
		t.Fatalf("mock oauth form: %v", err)
	}
	var payload map[string]any
	if err := json.Unmarshal(recorder.Body.Bytes(), &payload); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if payload["access_token"] != legacyruntime.InjectAuthToken {
		t.Fatalf("form placeholder echo = %#v", payload)
	}
}

func TestHandleMockOAuthForwardsUnknownRefreshWithOriginalBodyAndIdentity(t *testing.T) {
	original := []byte(`{"grant_type":"refresh_token","refresh_token":"real-refresh-token"}`)
	var gotBody []byte
	var gotAuth string
	official := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		gotAuth = request.Header.Get("Authorization")
		gotBody, _ = io.ReadAll(request.Body)
		writer.Header().Set("content-type", "application/json")
		_, _ = writer.Write([]byte(`{"access_token":"official-access"}`))
	}))
	defer official.Close()

	recorder, err := invokeMockOAuth(t, oauthInvokeOptions{
		contentType:   "application/json",
		body:          original,
		authorization: catalogTestInboundAuth,
		officialURL:   official.URL + "/oauth/token",
		client:        official.Client(),
	})
	if err != nil {
		t.Fatalf("forward oauth: %v", err)
	}
	if gotAuth != catalogTestInboundAuth {
		t.Fatalf("forwarded authorization = %q", gotAuth)
	}
	if !bytes.Equal(gotBody, original) {
		t.Fatalf("forwarded body mutated: %q", gotBody)
	}
	if recorder.Body.String() != `{"access_token":"official-access"}` {
		t.Fatalf("forwarded response = %q", recorder.Body.String())
	}
}

func TestHandleMockOAuthForwardsUnknownFormGrant(t *testing.T) {
	original := []byte("grant_type=authorization_code&code=abc&redirect_uri=https://example.invalid")
	var gotBody []byte
	official := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		gotBody, _ = io.ReadAll(request.Body)
		writer.WriteHeader(http.StatusOK)
		_, _ = writer.Write([]byte(`{"ok":true}`))
	}))
	defer official.Close()
	_, err := invokeMockOAuth(t, oauthInvokeOptions{
		contentType:   "application/x-www-form-urlencoded",
		body:          original,
		authorization: catalogTestInboundAuth,
		officialURL:   official.URL + "/oauth/token",
		client:        official.Client(),
	})
	if err != nil {
		t.Fatalf("forward form oauth: %v", err)
	}
	if !bytes.Equal(gotBody, original) {
		t.Fatalf("form body mutated: %q", gotBody)
	}
}

func TestHandleMockOAuthErrorsWhenTargetMissing(t *testing.T) {
	recorder, err := invokeMockOAuth(t, oauthInvokeOptions{
		contentType:   "application/json",
		body:          []byte(`{"grant_type":"refresh_token","refresh_token":"real-refresh-token"}`),
		authorization: catalogTestInboundAuth,
	})
	if err == nil {
		t.Fatal("missing oauth target returned fake success")
	}
	if recorder.Code != http.StatusOK || recorder.Body.Len() != 0 {
		t.Fatalf("fake oauth response written: status=%d body=%q", recorder.Code, recorder.Body.String())
	}
}

type oauthInvokeOptions struct {
	contentType   string
	body          []byte
	authorization string
	officialURL   string
	client        HTTPClient
}

func invokeMockOAuth(t *testing.T, options oauthInvokeOptions) (*httptest.ResponseRecorder, error) {
	t.Helper()
	request := httptest.NewRequest(http.MethodPost, "http://backend.local/oauth/token", bytes.NewReader(options.body))
	request.Header.Set("content-type", options.contentType)
	if options.authorization != "" {
		request.Header.Set("Authorization", options.authorization)
	}
	if options.officialURL != "" {
		request.Header.Set(HeaderRawServerURL, options.officialURL)
	}
	recorder := httptest.NewRecorder()
	var target *url.URL
	if options.officialURL != "" {
		parsed, err := url.Parse(options.officialURL)
		if err != nil {
			t.Fatal(err)
		}
		target = parsed
	} else {
		copyURL := *request.URL
		target = &copyURL
	}
	err := handleMockOAuth(&RequestContext{
		ResponseWriter: recorder,
		Request:        request,
		StartedAt:      time.Now(),
		RawURL:         strings.TrimSpace(request.Header.Get(HeaderRawServerURL)),
		TargetURL:      target,
		Method:         http.MethodPost,
		Headers:        request.Header.Clone(),
		ContentType:    options.contentType,
		RequestBody:    append([]byte(nil), options.body...),
		Mode:           server.ModeLocal,
		Deps:           &Dependencies{HTTPClient: options.client},
	}, &Route{Name: "oauth_token"})
	return recorder, err
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
