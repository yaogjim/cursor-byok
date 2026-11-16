package backend

import (
	"bytes"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"cursor/gen/aiserverv1"
	"cursor/internal/backend/server"
	legacyruntime "cursor/internal/runtime"

	"google.golang.org/protobuf/proto"
)

// §19.7：剩余本地未实现接口的空响应合并切片。NameTab 依字段证据排除并保持 404。

func TestHostScmConnectionStatusLocalDisconnected(t *testing.T) {
	manager := newHostConfigTestManager(t)
	host := &Host{configs: manager}
	if err := host.rebuild(DefaultHostTestConfig(t, manager, nil)); err != nil {
		t.Fatal(err)
	}
	const path = "/aiserver.v1.DashboardService/GetScmConnectionStatus"
	for _, authorization := range []string{"", "Bearer " + legacyruntime.LocalRelayToken, "Bearer official-placeholder"} {
		req := httptest.NewRequest(http.MethodPost, path, bytes.NewReader([]byte{}))
		req.Header.Set("Content-Type", "application/proto")
		if authorization != "" {
			req.Header.Set("Authorization", authorization)
		}
		// 任何身份都不得外发；离线上游陷阱只用于证明这一点。
		trap := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			t.Error("GetScmConnectionStatus 意外访问上游")
			w.WriteHeader(http.StatusTeapot)
		}))
		req.Header.Set(server.HeaderServerUpstreamURL, trap.URL)
		rec := httptest.NewRecorder()
		host.mux.ServeHTTP(rec, req)
		trap.Close()
		if rec.Code != http.StatusOK {
			t.Fatalf("auth=%q status=%d body=%q", authorization, rec.Code, rec.Body.String())
		}
		response := &aiserverv1.GetScmConnectionStatusResponse{}
		if err := proto.Unmarshal(rec.Body.Bytes(), response); err != nil {
			t.Fatalf("auth=%q unmarshal: %v body=%x", authorization, err, rec.Body.Bytes())
		}
		if response.GetConnected() || response.GetProviderDisabled() {
			t.Fatalf("auth=%q connected=%v providerDisabled=%v，期望本地未连接", authorization, response.GetConnected(), response.GetProviderDisabled())
		}
	}
}

func TestHostReportCommitAiAnalyticsReturnsEmpty(t *testing.T) {
	manager := newHostConfigTestManager(t)
	host := &Host{configs: manager}
	if err := host.rebuild(DefaultHostTestConfig(t, manager, nil)); err != nil {
		t.Fatal(err)
	}
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/aiserver.v1.AiService/ReportCommitAiAnalytics", bytes.NewReader([]byte{}))
	req.Header.Set("Content-Type", "application/proto")
	host.mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK || rec.Body.Len() != 0 {
		t.Fatalf("status=%d body=%x", rec.Code, rec.Body.Bytes())
	}
}

func TestHostUpdateConversationMetadataLocalEmptyOfficialForwarded(t *testing.T) {
	const path = "/agent.v1.AgentService/UpdateConversationMetadata"
	manager := newHostConfigTestManager(t)
	host := &Host{configs: manager}
	if err := host.rebuild(DefaultHostTestConfig(t, manager, nil)); err != nil {
		t.Fatal(err)
	}

	for _, authorization := range []string{"", "Bearer " + legacyruntime.LocalRelayToken} {
		req := httptest.NewRequest(http.MethodPost, path, bytes.NewReader([]byte{}))
		req.Header.Set("Content-Type", "application/proto")
		if authorization != "" {
			req.Header.Set("Authorization", authorization)
		}
		trap := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			t.Error("本地身份意外转发官方")
			w.WriteHeader(http.StatusTeapot)
		}))
		req.Header.Set(server.HeaderServerUpstreamURL, trap.URL)
		rec := httptest.NewRecorder()
		host.mux.ServeHTTP(rec, req)
		trap.Close()
		if rec.Code != http.StatusOK || rec.Body.Len() != 0 {
			t.Fatalf("auth=%q status=%d body=%x", authorization, rec.Code, rec.Body.Bytes())
		}
	}

	const officialAuth = "Bearer official-metadata-token"
	trapHits := 0
	trap := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
		trapHits++
		if request.Header.Get("Authorization") != officialAuth {
			t.Errorf("官方身份被改写: %q", request.Header.Get("Authorization"))
		}
		w.Header().Set("Content-Type", "application/proto")
		w.WriteHeader(http.StatusForbidden)
		_, _ = w.Write([]byte("official-denied"))
	}))
	defer trap.Close()
	req := httptest.NewRequest(http.MethodPost, path, bytes.NewReader([]byte("payload")))
	req.Header.Set("Content-Type", "application/proto")
	req.Header.Set("Authorization", officialAuth)
	req.Header.Set(server.HeaderServerUpstreamURL, trap.URL+path)
	rec := httptest.NewRecorder()
	host.mux.ServeHTTP(rec, req)
	if trapHits != 1 {
		t.Fatalf("官方转发次数=%d，期望=1", trapHits)
	}
	if rec.Code != http.StatusForbidden || strings.TrimSpace(rec.Body.String()) != "official-denied" {
		t.Fatalf("官方错误未原样透传: status=%d body=%q", rec.Code, rec.Body.String())
	}
}

func TestHostNameTabStaysNotFound(t *testing.T) {
	manager := newHostConfigTestManager(t)
	host := &Host{configs: manager}
	if err := host.rebuild(DefaultHostTestConfig(t, manager, nil)); err != nil {
		t.Fatal(err)
	}
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/aiserver.v1.AiService/NameTab", io.NopCloser(strings.NewReader("")))
	req.Header.Set("Content-Type", "application/connect+proto")
	host.mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("NameTab status=%d，期望保持 404（NameTabResponse.name 空值有害）", rec.Code)
	}
}
