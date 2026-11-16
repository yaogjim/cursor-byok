package backend

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"cursor/internal/backend/server"
	"cursor/internal/backend/server/upstream"
)

func TestHostControlPlaneEmptyFallbacks(t *testing.T) {
	for _, signedOut := range []bool{false, true} {
		manager := newHostConfigTestManager(t)
		host := &Host{configs: manager}
		if signedOut {
			host.controlPlaneAuth = unsignedControlPlaneAuth{}
		}
		if err := host.rebuild(DefaultHostTestConfig(t, manager, nil)); err != nil {
			t.Fatal(err)
		}
		for _, path := range []string{
			"/aiserver.v1.DashboardService/GetEffectiveUserPlugins",
			"/aiserver.v1.MCPRegistryService/GetKnownServers",
			"/aiserver.v1.AnalyticsService/Batch",
		} {
			for _, framed := range []bool{false, true} {
				body := []byte{10, 0}
				contentType := "application/proto"
				expected := []byte{}
				if framed {
					contentType = "application/connect+proto"
					body = []byte{0, 0, 0, 0, 2, 10, 0}
					expected = []byte{0, 0, 0, 0, 0, 2, 0, 0, 0, 2, '{', '}'}
				}
				req := httptest.NewRequest(http.MethodPost, path, bytes.NewReader(body))
				req.Header.Set("Content-Type", contentType)
				rec := httptest.NewRecorder()
				host.mux.ServeHTTP(rec, req)
				if rec.Code != http.StatusOK || !bytes.Equal(rec.Body.Bytes(), expected) || rec.Header().Get("Content-Type") != contentType {
					t.Fatalf("signedOut=%v path=%s framed=%v status=%d contentType=%s body=%x", signedOut, path, framed, rec.Code, rec.Header().Get("Content-Type"), rec.Body.Bytes())
				}
			}
		}
		for _, path := range []string{"/aiserver.v1.DashboardService/InstallUserPlugin", "/aiserver.v1.DashboardService/AddMarketplace"} {
			rec := httptest.NewRecorder()
			host.mux.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, path, nil))
			if rec.Code != http.StatusNotFound {
				t.Fatalf("%s status=%d", path, rec.Code)
			}
		}
	}
}

type fallbackSignedInAuth struct{}

func (fallbackSignedInAuth) SignedIn() bool { return true }
func (fallbackSignedInAuth) Authorization(context.Context) (string, error) {
	return "Bearer offline-test", nil
}

type controlPlaneTestClient struct {
	t    *testing.T
	hits int
}

func (client *controlPlaneTestClient) Do(req *http.Request) (*http.Response, error) {
	client.hits++
	if req.URL.Host != "api2.cursor.sh:443" || req.Header.Get("Authorization") != "Bearer offline-test" {
		client.t.Fatal("未沿独立控制面账号转发官方")
	}
	return &http.Response{StatusCode: http.StatusForbidden, Header: http.Header{"Content-Type": {"application/proto"}}, Body: io.NopCloser(bytes.NewReader([]byte("official-denied")))}, nil
}

func TestControlPlaneSignedInDoesNotUseEmptyFallback(t *testing.T) {
	client := &controlPlaneTestClient{t: t}
	deps := upstream.Dependencies{HTTPClient: client}
	fallback := upstream.EmptyProtoAction(deps, upstream.CompatRouteConfig{})
	action := cursorControlPlaneAction(fallbackSignedInAuth{}, deps, "offline-control-plane", fallback)
	for _, path := range []string{"/aiserver.v1.DashboardService/GetEffectiveUserPlugins", "/aiserver.v1.MCPRegistryService/GetKnownServers"} {
		rec := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodPost, path, nil)
		req.Header.Set("Content-Type", "application/proto")
		if err := action(&server.Context{Writer: rec, Request: req}); err != nil {
			t.Fatal(err)
		}
		if rec.Code != http.StatusForbidden || rec.Body.String() != "official-denied" {
			t.Fatalf("status=%d body=%q", rec.Code, rec.Body.String())
		}
	}
	if client.hits != 2 {
		t.Fatalf("hits=%d", client.hits)
	}
}

func TestHostAnalyticsBatchSignedInStaysLocal(t *testing.T) {
	manager := newHostConfigTestManager(t)
	host := &Host{configs: manager, controlPlaneAuth: fallbackSignedInAuth{}}
	if err := host.rebuild(DefaultHostTestConfig(t, manager, nil)); err != nil {
		t.Fatal(err)
	}
	for _, authorization := range []string{"", "Bearer offline-official"} {
		req := httptest.NewRequest(http.MethodPost, "/aiserver.v1.AnalyticsService/Batch", bytes.NewReader([]byte{10, 0}))
		req.Header.Set("Content-Type", "application/proto")
		req.Header.Set("Authorization", authorization)
		// 若路由错误地转发，则只能命中离线陷阱，不能访问真实服务。
		trap := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			t.Error("Batch 意外访问上游")
			w.WriteHeader(http.StatusTeapot)
		}))
		req.Header.Set(server.HeaderServerUpstreamURL, trap.URL)
		rec := httptest.NewRecorder()
		host.mux.ServeHTTP(rec, req)
		trap.Close()
		if rec.Code != http.StatusOK || rec.Body.Len() != 0 {
			t.Fatalf("Batch status=%d body=%x", rec.Code, rec.Body.Bytes())
		}
	}
}
