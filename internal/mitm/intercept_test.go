package mitm

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	legacyruntime "cursor/internal/runtime"

	"github.com/elazarl/goproxy"
)

const interceptOfficialAuth = "Bearer official-cursor-token"

func TestShouldForwardToLocalBackendPathLevel(t *testing.T) {
	tests := []struct {
		name   string
		host   string
		method string
		path   string
		want   bool
	}{
		{name: "agent bidi", host: "api2.cursor.sh", method: http.MethodPost, path: "/aiserver.v1.BidiService/BidiAppend", want: true},
		{name: "agent run sse", host: "api2.cursor.sh", method: http.MethodPost, path: "/agent.v1.AgentService/RunSSE", want: true},
		{name: "catalog", host: "api2.cursor.sh", method: http.MethodPost, path: "/aiserver.v1.AiService/AvailableModels", want: true},
		{name: "usable models", host: "api3.cursor.sh:443", method: http.MethodPost, path: "/aiserver.v1.AiService/GetUsableModels", want: true},
		{name: "control server time", host: "api2.cursor.sh", method: http.MethodPost, path: "/aiserver.v1.AiService/ServerTime", want: true},
		{name: "healthz", host: "api2.cursor.sh", method: http.MethodGet, path: "/healthz", want: true},
		{name: "existing tab", host: "api2.cursor.sh", method: http.MethodPost, path: "/aiserver.v1.AiService/StreamCpp", want: true},
		{name: "existing filesync", host: "api4.cursor.sh", method: http.MethodPost, path: "/aiserver.v1.FileSyncService/FSSyncFile", want: true},
		{name: "existing repo", host: "api2.cursor.sh", method: http.MethodPost, path: "/aiserver.v1.RepositoryService/FastUpdateFileV2", want: true},
		{name: "existing docs", host: "api2.cursor.sh", method: http.MethodPost, path: "/aiserver.v1.AiService/AvailableDocs", want: true},
		{name: "existing mcp", host: "api2.cursor.sh", method: http.MethodPost, path: "/aiserver.v1.DashboardService/GetAvailableMcpServers", want: true},
		{name: "unmanaged unknown", host: "api2.cursor.sh", method: http.MethodPost, path: "/unknown/resource", want: false},
		{name: "unmanaged empty path", host: "api2.cursor.sh", method: http.MethodPost, path: "/", want: false},
		{name: "non whitelist", host: "example.com", method: http.MethodPost, path: "/aiserver.v1.BidiService/BidiAppend", want: false},
		{name: "auth service get email", host: "api2.cursor.sh", method: http.MethodPost, path: "/aiserver.v1.AuthService/GetEmail", want: true},
		{name: "oauth token", host: "api2.cursor.sh", method: http.MethodPost, path: "/oauth/token", want: true},
		// Boundary: host.go mocks /auth/* on the local listen addr only. MITM
		// does not intercept them, so real Cursor authenticator traffic is not
		// half-mocked. Do not expand this to other relays.
		{name: "auth poll passthrough", host: "api2.cursor.sh", method: http.MethodGet, path: "/auth/poll", want: false},
		{name: "auth stripe passthrough", host: "api2.cursor.sh", method: http.MethodGet, path: "/auth/full_stripe_profile", want: false},
		{name: "auth logout passthrough", host: "api2.cursor.sh", method: http.MethodPost, path: "/auth/logout", want: false},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := shouldForwardToLocalBackend(test.host, test.method, test.path); got != test.want {
				t.Fatalf("shouldForwardToLocalBackend() = %v, want %v", got, test.want)
			}
		})
	}
}

func TestInterceptRequestPreservesOfficialAuthorization(t *testing.T) {
	var hits atomic.Int32
	backend := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		hits.Add(1)
		got := request.Header.Get("Authorization")
		if got != interceptOfficialAuth {
			t.Errorf("authorization = %q, want inbound official token", got)
		}
		if got == "Bearer "+legacyruntime.LocalRelayToken || strings.Contains(strings.ToLower(got), "local-relay") {
			t.Error("official request Authorization was replaced with LocalRelayToken")
		}
		if request.Header.Get(HeaderServerUpstreamURL) == "" {
			t.Error("missing upstream URL header for backend routing")
		}
		writer.WriteHeader(http.StatusOK)
		_, _ = io.WriteString(writer, "ok")
	}))
	t.Cleanup(backend.Close)

	server, err := NewProxyServer("127.0.0.1:0", backend.URL, "", "", nil)
	if err != nil {
		t.Fatalf("NewProxyServer() error = %v", err)
	}
	req := httptest.NewRequest(http.MethodPost, "https://api2.cursor.sh/agent.v1.AgentService/RunSSE", strings.NewReader("body"))
	req.Host = "api2.cursor.sh"
	req.Header.Set("Authorization", interceptOfficialAuth)
	req.Header.Set("Cookie", "session=cookie-secret")

	_, resp := server.interceptRequest(req, &goproxy.ProxyCtx{})
	if resp == nil {
		t.Fatal("managed agent path was not forwarded to backend")
	}
	t.Cleanup(func() { _ = resp.Body.Close() })
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d", resp.StatusCode)
	}
	if hits.Load() != 1 {
		t.Fatalf("backend hits = %d, want 1", hits.Load())
	}
}

func TestInterceptRequestSkipsUnmanagedPaths(t *testing.T) {
	var hits atomic.Int32
	backend := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		hits.Add(1)
		writer.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(backend.Close)

	server, err := NewProxyServer("127.0.0.1:0", backend.URL, "", "", nil)
	if err != nil {
		t.Fatalf("NewProxyServer() error = %v", err)
	}

	tests := []struct {
		name string
		host string
		path string
	}{
		{name: "unknown cursor path", host: "api2.cursor.sh", path: "/unknown/resource"},
		{name: "non whitelist host", host: "example.com", path: "/aiserver.v1.BidiService/BidiAppend"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodPost, "https://"+test.host+test.path, strings.NewReader("body"))
			req.Host = test.host
			req.Header.Set("Authorization", interceptOfficialAuth)
			out, resp := server.interceptRequest(req, &goproxy.ProxyCtx{})
			if resp != nil {
				_ = resp.Body.Close()
				t.Fatal("unmanaged path was locally intercepted")
			}
			if out.Header.Get("Authorization") != interceptOfficialAuth {
				t.Fatalf("passthrough authorization = %q", out.Header.Get("Authorization"))
			}
			if out.Header.Get("Authorization") == "Bearer "+legacyruntime.LocalRelayToken {
				t.Fatal("passthrough Authorization was replaced with LocalRelayToken")
			}
		})
	}
	if hits.Load() != 0 {
		t.Fatalf("backend hits = %d, want 0", hits.Load())
	}
}

func TestInterceptRequestKeepsExistingLocalPaths(t *testing.T) {
	var hits atomic.Int32
	backend := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		hits.Add(1)
		if request.Header.Get("Authorization") != interceptOfficialAuth {
			t.Errorf("path %s authorization rewritten", request.URL.Path)
		}
		writer.WriteHeader(http.StatusNoContent)
	}))
	t.Cleanup(backend.Close)

	server, err := NewProxyServer("127.0.0.1:0", backend.URL, "", "", nil)
	if err != nil {
		t.Fatalf("NewProxyServer() error = %v", err)
	}

	paths := []string{
		"/aiserver.v1.BidiService/BidiAppend",
		"/agent.v1.AgentService/RunSSE",
		"/aiserver.v1.AiService/AvailableModels",
		"/aiserver.v1.AiService/GetUsableModels",
		"/aiserver.v1.AiService/ServerTime",
		"/healthz",
		"/aiserver.v1.AiService/StreamCpp",
		"/aiserver.v1.FileSyncService/FSSyncFile",
		"/aiserver.v1.RepositoryService/FastUpdateFileV2",
		"/aiserver.v1.AiService/AvailableDocs",
		"/aiserver.v1.DashboardService/GetAvailableMcpServers",
	}
	for _, path := range paths {
		req := httptest.NewRequest(http.MethodPost, "https://api2.cursor.sh"+path, strings.NewReader("body"))
		req.Host = "api2.cursor.sh"
		req.Header.Set("Authorization", interceptOfficialAuth)
		_, resp := server.interceptRequest(req, &goproxy.ProxyCtx{})
		if resp == nil {
			t.Fatalf("existing local path %s was not forwarded", path)
		}
		_ = resp.Body.Close()
	}
	if int(hits.Load()) != len(paths) {
		t.Fatalf("backend hits = %d, want %d", hits.Load(), len(paths))
	}
}

func TestInterceptRequestOmitsSecretsFromPassthroughDecision(t *testing.T) {
	if shouldForwardToLocalBackend("api2.cursor.sh", http.MethodPost, "/unknown/resource?token=secret-token") {
		t.Fatal("query on unmanaged path must not trigger local intercept")
	}
}
