package backend

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestCursorBackendContractHealthzTracesAndProcedures(t *testing.T) {
	manager := newHostConfigTestManager(t)
	cfg := DefaultHostTestConfig(t, manager, nil)
	host := &Host{configs: manager}
	if err := host.rebuild(cfg); err != nil {
		t.Fatalf("rebuild() error = %v", err)
	}
	if host.mux == nil {
		t.Fatal("mux is nil")
	}

	health := httptest.NewRecorder()
	host.mux.ServeHTTP(health, httptest.NewRequest(http.MethodGet, "/healthz", nil))
	if health.Code != http.StatusOK || strings.TrimSpace(health.Body.String()) != "ok" {
		t.Fatalf("healthz = %d %q", health.Code, health.Body.String())
	}

	traces := httptest.NewRecorder()
	host.mux.ServeHTTP(traces, httptest.NewRequest(http.MethodPost, "/v1/traces", strings.NewReader("{}")))
	if traces.Code != http.StatusOK {
		t.Fatalf("traces = %d", traces.Code)
	}

	for _, path := range []string{
		"/aiserver.v1.BidiService/BidiAppend",
		"/agent.v1.AgentService/RunSSE",
	} {
		recorder := httptest.NewRecorder()
		host.mux.ServeHTTP(recorder, httptest.NewRequest(http.MethodPost, path, strings.NewReader("")))
		if recorder.Code == http.StatusNotFound {
			t.Fatalf("%s was not registered", path)
		}
	}
}
