package ads

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
)

func TestDisabledServiceBlocksFetchAndCachedRuntime(t *testing.T) {
	var requests atomic.Int64
	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		requests.Add(1)
		response.WriteHeader(http.StatusNotFound)
	}))
	defer server.Close()

	originalSlots := Slots
	Slots = []Slot{{ID: "1", FetchURL: server.URL}}
	t.Cleanup(func() { Slots = originalSlots })

	service := NewService(Options{
		StoreRoot:  t.TempDir(),
		HTTPClient: server.Client(),
		Enabled:    false,
	})

	if _, err := service.FetchOnce(context.Background()); err != nil {
		t.Fatalf("FetchOnce() error = %v", err)
	}
	if requests.Load() != 0 {
		t.Fatalf("disabled service sent %d requests", requests.Load())
	}

	runtimeState, err := service.GetRuntime(context.Background())
	if err != nil {
		t.Fatalf("GetRuntime() error = %v", err)
	}
	if runtimeState.Available || runtimeState.Enabled || len(runtimeState.Slots) != 0 {
		t.Fatalf("disabled runtime exposed ad state: %+v", runtimeState)
	}
}

func TestSetEnabledControlsNetworkAccess(t *testing.T) {
	var requests atomic.Int64
	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		requests.Add(1)
		response.WriteHeader(http.StatusNotFound)
	}))
	defer server.Close()

	originalSlots := Slots
	Slots = []Slot{{ID: "1", FetchURL: server.URL}}
	t.Cleanup(func() { Slots = originalSlots })

	service := NewService(Options{
		StoreRoot:  t.TempDir(),
		HTTPClient: server.Client(),
		Enabled:    false,
	})
	service.SetEnabled(true)
	if _, err := service.FetchOnce(context.Background()); err != nil {
		t.Fatalf("enabled FetchOnce() error = %v", err)
	}
	if requests.Load() != 1 {
		t.Fatalf("enabled service sent %d requests, want 1", requests.Load())
	}

	service.SetEnabled(false)
	if _, err := service.FetchOnce(context.Background()); err != nil {
		t.Fatalf("disabled FetchOnce() error = %v", err)
	}
	if requests.Load() != 1 {
		t.Fatalf("disabled service sent another request; total = %d", requests.Load())
	}
}
