package upstream

import (
	"bytes"
	"context"
	"encoding/binary"
	"encoding/hex"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"cursor/gen/agentv1"
	"cursor/gen/aiserverv1"
	"cursor/internal/backend/server"
	legacyruntime "cursor/internal/runtime"

	"google.golang.org/protobuf/proto"
)

const (
	agentTestLocalChannel  = "abcdef0123456789"
	agentTestOfficialModel = "official-opus"
	agentTestProviderModel = "grok-3"
	agentTestInboundAuth   = "Bearer real-cursor-token"
	agentTestInboundCheck  = "inbound-checksum"
)

func TestDecideAgentDestination(t *testing.T) {
	t.Parallel()
	adapters := []legacyruntime.ModelAdapterConfig{{
		ID:      agentTestLocalChannel,
		ModelID: agentTestProviderModel,
	}}
	localIDs := LocalAdapterIDs(adapters)
	providerIDs := LocalProviderModelIDs(adapters)

	tests := []struct {
		name    string
		modelID string
		want    AgentDestination
	}{
		{name: "local hash", modelID: agentTestLocalChannel, want: AgentDestinationLocal},
		{name: "local variant", modelID: agentTestLocalChannel + ":high", want: AgentDestinationLocal},
		{name: "official unmatched", modelID: agentTestOfficialModel, want: AgentDestinationOfficial},
		{name: "provider id is unknown", modelID: agentTestProviderModel, want: AgentDestinationUnknown},
		{name: "provider variant is unknown", modelID: agentTestProviderModel + ":high", want: AgentDestinationUnknown},
		{name: "empty is unknown", modelID: "", want: AgentDestinationUnknown},
		{name: "auto alias is unknown", modelID: "auto", want: AgentDestinationUnknown},
		{name: "fast alias is unknown", modelID: "fast", want: AgentDestinationUnknown},
		{name: "default alias is unknown", modelID: "default", want: AgentDestinationUnknown},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			got := DecideAgentDestination(test.modelID, localIDs, providerIDs)
			if got != test.want {
				t.Fatalf("dest = %s, want %s", got, test.want)
			}
			if test.want == AgentDestinationUnknown && routingIDMatch(strings.TrimSpace(test.modelID), localIDs) {
				t.Fatal("unknown model matched a local adapter")
			}
			if test.want != AgentDestinationOfficial && got == AgentDestinationOfficial {
				t.Fatal("non-official model upgraded to official")
			}
		})
	}
}

func TestUnknownModelDoesNotUseFirstLocalAdapter(t *testing.T) {
	t.Parallel()
	adapters := []legacyruntime.ModelAdapterConfig{
		{ID: agentTestLocalChannel, ModelID: agentTestProviderModel},
		{ID: "1111111111111111", ModelID: "other"},
	}
	localIDs := LocalAdapterIDs(adapters)
	providerIDs := LocalProviderModelIDs(adapters)
	for _, modelID := range []string{"", "auto", "fast", "default", agentTestProviderModel} {
		dest := DecideAgentDestination(modelID, localIDs, providerIDs)
		if dest != AgentDestinationUnknown {
			t.Fatalf("model %q dest = %s, want unknown", modelID, dest)
		}
	}
	if dest := DecideAgentDestination(agentTestOfficialModel, localIDs, providerIDs); dest != AgentDestinationOfficial {
		t.Fatalf("official dest = %s", dest)
	}
}

func TestAgentRouteLocalHitUsesForwarderNotOfficial(t *testing.T) {
	t.Parallel()
	officialHits := int32(0)
	official := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		atomic.AddInt32(&officialHits, 1)
	}))
	defer official.Close()

	localHits := int32(0)
	local := http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		atomic.AddInt32(&localHits, 1)
		if got := request.Header.Get("Authorization"); got != agentTestInboundAuth {
			t.Errorf("local authorization = %q", got)
		}
		writer.WriteHeader(http.StatusNoContent)
	})

	sessions := NewAgentSessionStore()
	action := AgentRouteAction(agentRouteDeps(t, official.Client()), sessions, local)
	recorder := invokeAgentRoute(t, action, agentRouteInvoke{
		path:          bidiAppendProcedure,
		body:          bidiAppendBody(t, "req-local", agentTestLocalChannel),
		contentType:   "application/connect+proto",
		authorization: agentTestInboundAuth,
		officialURL:   official.URL + bidiAppendProcedure,
		mode:          server.ModeLocal,
	})
	if recorder.Code != http.StatusNoContent {
		t.Fatalf("status = %d", recorder.Code)
	}
	if atomic.LoadInt32(&localHits) != 1 {
		t.Fatalf("local hits = %d", localHits)
	}
	if atomic.LoadInt32(&officialHits) != 0 {
		t.Fatalf("official was called for a local model")
	}
}

func TestAgentRouteOfficialPreservesInboundAuthorization(t *testing.T) {
	t.Parallel()
	var (
		sawAuth      string
		sawChecksum  string
		sawUpstream  string
		sawRelay     bool
		officialHits int32
	)
	official := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		atomic.AddInt32(&officialHits, 1)
		sawAuth = request.Header.Get("Authorization")
		sawChecksum = request.Header.Get("x-cursor-checksum")
		sawUpstream = request.Header.Get(HeaderRawServerURL)
		if strings.Contains(strings.ToLower(sawAuth), "local-relay") || sawAuth == "Bearer "+legacyruntime.LocalRelayToken {
			sawRelay = true
		}
		if request.Header.Get(server.HeaderTraceID) != "" || request.Header.Get(server.HeaderParentSpanID) != "" {
			t.Errorf("internal correlation leaked to official: %v", request.Header)
		}
		writer.WriteHeader(http.StatusAccepted)
		_, _ = writer.Write([]byte("official-ok"))
	}))
	defer official.Close()

	localHits := int32(0)
	local := http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		atomic.AddInt32(&localHits, 1)
	})
	action := AgentRouteAction(agentRouteDeps(t, official.Client()), NewAgentSessionStore(), local)
	recorder := invokeAgentRoute(t, action, agentRouteInvoke{
		path:          bidiAppendProcedure,
		body:          bidiAppendBody(t, "req-official", agentTestOfficialModel),
		contentType:   "application/connect+proto",
		authorization: agentTestInboundAuth,
		checksum:      agentTestInboundCheck,
		officialURL:   official.URL + bidiAppendProcedure,
		mode:          server.ModeLocal,
	})
	if recorder.Code != http.StatusAccepted {
		t.Fatalf("status = %d body=%q", recorder.Code, recorder.Body.String())
	}
	if atomic.LoadInt32(&localHits) != 0 {
		t.Fatal("local handler ran for an official model")
	}
	if atomic.LoadInt32(&officialHits) != 1 {
		t.Fatalf("official hits = %d", officialHits)
	}
	if sawAuth != agentTestInboundAuth {
		t.Fatalf("authorization = %q, want inbound token", sawAuth)
	}
	if sawChecksum != agentTestInboundCheck {
		t.Fatalf("checksum = %q, want inbound checksum", sawChecksum)
	}
	if sawUpstream != "" {
		t.Fatalf("leaked %s", HeaderRawServerURL)
	}
	if sawRelay {
		t.Fatal("official request carried LocalRelayToken")
	}
	if recorder.Body.String() != "official-ok" {
		t.Fatalf("body = %q", recorder.Body.String())
	}
}

func TestAgentRouteUnknownDoesNotHitLocal(t *testing.T) {
	t.Parallel()
	officialHits := int32(0)
	official := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		atomic.AddInt32(&officialHits, 1)
		writer.WriteHeader(http.StatusCreated)
	}))
	defer official.Close()
	localHits := int32(0)
	local := http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		atomic.AddInt32(&localHits, 1)
	})
	action := AgentRouteAction(agentRouteDeps(t, official.Client()), NewAgentSessionStore(), local)
	_, err := invokeAgentRouteErr(t, action, agentRouteInvoke{
		path:          bidiAppendProcedure,
		body:          bidiAppendBody(t, "req-unknown-provider", agentTestProviderModel),
		contentType:   "application/connect+proto",
		authorization: agentTestInboundAuth,
		officialURL:   official.URL + bidiAppendProcedure,
		mode:          server.ModeLocal,
	})
	if err == nil {
		t.Fatal("provider model upgraded instead of failing closed")
	}
	if atomic.LoadInt32(&localHits) != 0 {
		t.Fatalf("unknown models hit local %d times", localHits)
	}
	if atomic.LoadInt32(&officialHits) != 0 {
		t.Fatalf("unknown models hit official %d times", officialHits)
	}
}

func TestAgentRouteMetaAliasDoesNotUpgradeToOfficial(t *testing.T) {
	t.Parallel()
	officialHits := int32(0)
	official := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		atomic.AddInt32(&officialHits, 1)
		writer.WriteHeader(http.StatusCreated)
	}))
	defer official.Close()
	localHits := int32(0)
	local := http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		atomic.AddInt32(&localHits, 1)
	})
	action := AgentRouteAction(agentRouteDeps(t, official.Client()), NewAgentSessionStore(), local)
	for _, modelID := range []string{"", "auto", "fast", "default"} {
		_, err := invokeAgentRouteErr(t, action, agentRouteInvoke{
			path:          bidiAppendProcedure,
			body:          bidiAppendBody(t, "req-meta-"+modelID, modelID),
			contentType:   "application/connect+proto",
			authorization: agentTestInboundAuth,
			officialURL:   official.URL + bidiAppendProcedure,
			mode:          server.ModeLocal,
		})
		if err == nil {
			t.Fatalf("model %q implicitly upgraded without a decision", modelID)
		}
	}
	if atomic.LoadInt32(&localHits) != 0 || atomic.LoadInt32(&officialHits) != 0 {
		t.Fatalf("meta alias routed local=%d official=%d", localHits, officialHits)
	}
}

func TestAgentRouteEmptyModelFollowsRememberedDecision(t *testing.T) {
	t.Parallel()
	officialHits := int32(0)
	official := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		atomic.AddInt32(&officialHits, 1)
		writer.WriteHeader(http.StatusAccepted)
	}))
	defer official.Close()
	local := http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		t.Error("local handler must not run")
	})
	action := AgentRouteAction(agentRouteDeps(t, official.Client()), NewAgentSessionStore(), local)
	const requestID = "req-follow"
	if got := invokeAgentRoute(t, action, agentRouteInvoke{
		path:          bidiAppendProcedure,
		body:          bidiAppendBody(t, requestID, agentTestOfficialModel),
		contentType:   "application/connect+proto",
		authorization: agentTestInboundAuth,
		officialURL:   official.URL + bidiAppendProcedure,
		mode:          server.ModeLocal,
	}); got.Code != http.StatusAccepted {
		t.Fatalf("first append status = %d", got.Code)
	}
	if got := invokeAgentRoute(t, action, agentRouteInvoke{
		path:          bidiAppendProcedure,
		body:          bidiAppendBody(t, requestID, ""),
		contentType:   "application/connect+proto",
		authorization: agentTestInboundAuth,
		officialURL:   official.URL + bidiAppendProcedure,
		mode:          server.ModeLocal,
	}); got.Code != http.StatusAccepted {
		t.Fatalf("empty follow-up status = %d", got.Code)
	}
	if atomic.LoadInt32(&officialHits) != 2 {
		t.Fatalf("official hits = %d", officialHits)
	}
}

func TestAgentRouteMissingRequestIDReturnsError(t *testing.T) {
	t.Parallel()
	officialHits := int32(0)
	official := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		atomic.AddInt32(&officialHits, 1)
	}))
	defer official.Close()
	action := AgentRouteAction(agentRouteDeps(t, official.Client()), NewAgentSessionStore(), http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		t.Error("local handler must not run")
	}))
	_, err := invokeAgentRouteErr(t, action, agentRouteInvoke{
		path:          runSSEProcedure,
		body:          runSSEBody(t, ""),
		contentType:   "application/connect+proto",
		authorization: agentTestInboundAuth,
		officialURL:   official.URL + runSSEProcedure,
		mode:          server.ModeLocal,
	})
	if err == nil {
		t.Fatal("missing request_id returned nil success")
	}
	_, err = invokeAgentRouteErr(t, action, agentRouteInvoke{
		path:          bidiAppendProcedure,
		body:          bidiAppendBody(t, "", agentTestOfficialModel),
		contentType:   "application/connect+proto",
		authorization: agentTestInboundAuth,
		officialURL:   official.URL + bidiAppendProcedure,
		mode:          server.ModeLocal,
	})
	if err == nil {
		t.Fatal("bidi missing request_id returned nil success")
	}
	if atomic.LoadInt32(&officialHits) != 0 {
		t.Fatalf("official hits = %d", officialHits)
	}
}

func TestAgentRouteRunSSECancelReturnsError(t *testing.T) {
	t.Parallel()
	official := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		t.Error("official must not run")
	}))
	defer official.Close()
	action := AgentRouteAction(agentRouteDeps(t, official.Client()), NewAgentSessionStore(), http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		t.Error("local must not run")
	}))
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := invokeAgentRouteErr(t, action, agentRouteInvoke{
		path:          runSSEProcedure,
		body:          runSSEBody(t, "req-canceled"),
		contentType:   "application/connect+proto",
		authorization: agentTestInboundAuth,
		officialURL:   official.URL + runSSEProcedure,
		mode:          server.ModeLocal,
		ctx:           ctx,
	})
	if err == nil {
		t.Fatal("canceled RunSSE returned nil success")
	}
}

func TestAgentRouteAdapterLoadFailureDoesNotSendLocalHashOfficial(t *testing.T) {
	t.Parallel()
	officialHits := int32(0)
	official := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		atomic.AddInt32(&officialHits, 1)
	}))
	defer official.Close()
	deps := agentRouteDeps(t, official.Client())
	deps.SystemSettingService = errModelAdapters{err: fmt.Errorf("adapters unavailable")}
	action := AgentRouteAction(deps, NewAgentSessionStore(), http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		t.Error("local must not run")
	}))
	_, err := invokeAgentRouteErr(t, action, agentRouteInvoke{
		path:          bidiAppendProcedure,
		body:          bidiAppendBody(t, "req-fail-closed", agentTestLocalChannel),
		contentType:   "application/connect+proto",
		authorization: agentTestInboundAuth,
		officialURL:   official.URL + bidiAppendProcedure,
		mode:          server.ModeLocal,
	})
	if err == nil {
		t.Fatal("adapter load failure returned nil success")
	}
	if atomic.LoadInt32(&officialHits) != 0 {
		t.Fatal("local hash was sent official after adapter load failure")
	}
}

func TestAgentSessionDoesNotRandomlyEvictDecided(t *testing.T) {
	t.Parallel()
	store := NewAgentSessionStore()
	if dest := store.Remember("req-decided", AgentDestinationOfficial); dest != AgentDestinationOfficial {
		t.Fatalf("remember = %s", dest)
	}
	store.mu.Lock()
	now := time.Now()
	for i := 0; i < agentSessionMaxSize+8; i++ {
		store.sessions[fmt.Sprintf("pending-%d", i)] = &agentSession{updated: now.Add(-time.Duration(i) * time.Millisecond)}
	}
	store.evictLocked(now)
	decided := store.sessions["req-decided"]
	over := len(store.sessions) > agentSessionMaxSize
	store.mu.Unlock()
	if decided == nil || !decided.decided || decided.dest != AgentDestinationOfficial {
		t.Fatal("decided session was evicted")
	}
	if over {
		t.Fatalf("undecided overflow was not evicted: size=%d", len(store.sessions))
	}
}

func TestAgentRouteRunSSEWaitsForBidiAppend(t *testing.T) {
	t.Parallel()
	var officialAuth []string
	var officialMu sync.Mutex
	official := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		officialMu.Lock()
		officialAuth = append(officialAuth, request.Header.Get("Authorization"))
		officialMu.Unlock()
		if request.Header.Get("Authorization") == "Bearer "+legacyruntime.LocalRelayToken {
			t.Error("official RunSSE used LocalRelayToken")
		}
		writer.WriteHeader(http.StatusOK)
		_, _ = writer.Write([]byte(request.URL.Path))
	}))
	defer official.Close()

	var localPaths []string
	var localMu sync.Mutex
	local := http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		localMu.Lock()
		localPaths = append(localPaths, request.URL.Path)
		localMu.Unlock()
		writer.WriteHeader(http.StatusNoContent)
	})

	sessions := NewAgentSessionStore()
	action := AgentRouteAction(agentRouteDeps(t, official.Client()), sessions, local)
	const requestID = "req-order"

	runStarted := make(chan struct{})
	runDone := make(chan int, 1)
	go func() {
		close(runStarted)
		recorder := invokeAgentRoute(t, action, agentRouteInvoke{
			path:          runSSEProcedure,
			body:          runSSEBody(t, requestID),
			contentType:   "application/connect+proto",
			authorization: agentTestInboundAuth,
			officialURL:   official.URL + runSSEProcedure,
			mode:          server.ModeLocal,
		})
		runDone <- recorder.Code
	}()
	<-runStarted
	waitForAgentWaiter(t, sessions, requestID)

	bidi := invokeAgentRoute(t, action, agentRouteInvoke{
		path:          bidiAppendProcedure,
		body:          bidiAppendBody(t, requestID, agentTestLocalChannel),
		contentType:   "application/connect+proto",
		authorization: agentTestInboundAuth,
		officialURL:   official.URL + bidiAppendProcedure,
		mode:          server.ModeLocal,
	})
	if bidi.Code != http.StatusNoContent {
		t.Fatalf("bidi status = %d", bidi.Code)
	}

	select {
	case code := <-runDone:
		if code != http.StatusNoContent {
			t.Fatalf("runsse status = %d", code)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("RunSSE did not follow BidiAppend decision")
	}

	localMu.Lock()
	defer localMu.Unlock()
	if len(localPaths) != 2 {
		t.Fatalf("local paths = %v, want BidiAppend then RunSSE", localPaths)
	}
	if localPaths[0] != bidiAppendProcedure || localPaths[1] != runSSEProcedure {
		t.Fatalf("order = %v", localPaths)
	}
	officialMu.Lock()
	defer officialMu.Unlock()
	if len(officialAuth) != 0 {
		t.Fatalf("official was called: %v", officialAuth)
	}
}

func TestAgentRouteRunSSEFollowsOfficialBidiAppend(t *testing.T) {
	t.Parallel()
	officialHits := int32(0)
	official := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		atomic.AddInt32(&officialHits, 1)
		if request.Header.Get("Authorization") != agentTestInboundAuth {
			t.Errorf("authorization = %q", request.Header.Get("Authorization"))
		}
		writer.WriteHeader(http.StatusAccepted)
	}))
	defer official.Close()
	local := http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		t.Error("local handler must not run")
	})
	sessions := NewAgentSessionStore()
	action := AgentRouteAction(agentRouteDeps(t, official.Client()), sessions, local)
	const requestID = "req-official-order"

	runStarted := make(chan struct{})
	runDone := make(chan int, 1)
	go func() {
		close(runStarted)
		recorder := invokeAgentRoute(t, action, agentRouteInvoke{
			path:          runSSEProcedure,
			body:          runSSEBody(t, requestID),
			contentType:   "application/connect+proto",
			authorization: agentTestInboundAuth,
			officialURL:   official.URL + runSSEProcedure,
			mode:          server.ModeLocal,
		})
		runDone <- recorder.Code
	}()
	<-runStarted
	waitForAgentWaiter(t, sessions, requestID)

	if got := invokeAgentRoute(t, action, agentRouteInvoke{
		path:          bidiAppendProcedure,
		body:          bidiAppendBody(t, requestID, agentTestOfficialModel),
		contentType:   "application/connect+proto",
		authorization: agentTestInboundAuth,
		officialURL:   official.URL + bidiAppendProcedure,
		mode:          server.ModeLocal,
	}); got.Code != http.StatusAccepted {
		t.Fatalf("bidi status = %d", got.Code)
	}
	select {
	case code := <-runDone:
		if code != http.StatusAccepted {
			t.Fatalf("runsse status = %d", code)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("RunSSE did not complete")
	}
	if atomic.LoadInt32(&officialHits) != 2 {
		t.Fatalf("official hits = %d", officialHits)
	}
}

func TestAgentSessionUnknownDoesNotCloseRoute(t *testing.T) {
	t.Parallel()
	store := NewAgentSessionStore()
	if dest := store.Remember("req-pending", AgentDestinationUnknown); dest != AgentDestinationUnknown {
		t.Fatalf("remember unknown = %s", dest)
	}
	if _, ok := store.Lookup("req-pending"); ok {
		t.Fatal("unknown must not decide the session")
	}
	if dest := store.Remember("req-pending", AgentDestinationLocal); dest != AgentDestinationLocal {
		t.Fatalf("later local = %s", dest)
	}
	if dest, ok := store.Lookup("req-pending"); !ok || dest != AgentDestinationLocal {
		t.Fatalf("lookup = %s ok=%t", dest, ok)
	}
}

func TestAgentRouteOfficialRejectsLocalRelayToken(t *testing.T) {
	t.Parallel()
	officialHits := int32(0)
	official := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		atomic.AddInt32(&officialHits, 1)
	}))
	defer official.Close()
	action := AgentRouteAction(agentRouteDeps(t, official.Client()), NewAgentSessionStore(), http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		t.Error("local handler must not run")
	}))
	_, err := invokeAgentRouteErr(t, action, agentRouteInvoke{
		path:          bidiAppendProcedure,
		body:          bidiAppendBody(t, "req-relay-official", agentTestOfficialModel),
		contentType:   "application/connect+proto",
		authorization: "Bearer " + legacyruntime.LocalRelayToken,
		officialURL:   official.URL + bidiAppendProcedure,
		mode:          server.ModeLocal,
	})
	if err == nil {
		t.Fatal("local relay official route returned success")
	}
	if atomic.LoadInt32(&officialHits) != 0 {
		t.Fatal("official upstream was called with local relay authorization")
	}
}

func TestAgentSessionActivityRefreshesTTL(t *testing.T) {
	t.Parallel()
	store := NewAgentSessionStore()
	if dest := store.Remember("req-activity", AgentDestinationOfficial); dest != AgentDestinationOfficial {
		t.Fatalf("remember = %s", dest)
	}
	store.mu.Lock()
	store.sessions["req-activity"].updated = time.Now().Add(-agentSessionTTL - time.Minute)
	store.mu.Unlock()
	if dest, ok := store.Lookup("req-activity"); !ok || dest != AgentDestinationOfficial {
		t.Fatalf("lookup after idle = %s ok=%t", dest, ok)
	}
	store.mu.Lock()
	store.evictLocked(time.Now())
	kept := store.sessions["req-activity"]
	store.mu.Unlock()
	if kept == nil || !kept.decided {
		t.Fatal("active session expired")
	}

	store.mu.Lock()
	store.sessions["req-activity"].updated = time.Now().Add(-agentSessionTTL - time.Minute)
	store.evictLocked(time.Now())
	expired := store.sessions["req-activity"]
	store.mu.Unlock()
	if expired != nil {
		t.Fatal("idle session was not evicted")
	}
}

func TestAgentSessionWaitersBoundedAndExpire(t *testing.T) {
	t.Parallel()
	store := NewAgentSessionStore()
	const requestID = "req-waiters"
	accepted := int32(0)
	rejected := int32(0)
	var wg sync.WaitGroup
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	for i := 0; i < agentSessionMaxWaiters+8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, ok := store.Wait(ctx, requestID); ok {
				atomic.AddInt32(&accepted, 1)
				return
			}
			atomic.AddInt32(&rejected, 1)
		}()
	}
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		store.mu.Lock()
		waiting := 0
		if session := store.sessions[requestID]; session != nil {
			waiting = len(session.waiters)
		}
		store.mu.Unlock()
		if waiting == agentSessionMaxWaiters && atomic.LoadInt32(&rejected) >= 8 {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}
	store.mu.Lock()
	session := store.sessions[requestID]
	waiting := 0
	if session != nil {
		waiting = len(session.waiters)
		session.updated = time.Now().Add(-agentSessionTTL - time.Minute)
	}
	store.evictLocked(time.Now())
	_, still := store.sessions[requestID]
	store.mu.Unlock()
	if waiting != agentSessionMaxWaiters {
		t.Fatalf("waiters = %d, want %d", waiting, agentSessionMaxWaiters)
	}
	if still {
		t.Fatal("expired waiter session was not evicted")
	}
	wg.Wait()
	if atomic.LoadInt32(&accepted) != 0 {
		t.Fatalf("expired waiters resolved as a route: %d", accepted)
	}
	if atomic.LoadInt32(&rejected) != int32(agentSessionMaxWaiters+8) {
		t.Fatalf("rejected = %d", rejected)
	}
}

func TestAgentRouteMalformedFollowUpUsesRememberedRoute(t *testing.T) {
	t.Parallel()
	officialHits := int32(0)
	official := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		atomic.AddInt32(&officialHits, 1)
		writer.WriteHeader(http.StatusAccepted)
	}))
	defer official.Close()
	local := http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		t.Error("local handler must not run")
	})
	action := AgentRouteAction(agentRouteDeps(t, official.Client()), NewAgentSessionStore(), local)
	const requestID = "req-malformed-follow"
	if got := invokeAgentRoute(t, action, agentRouteInvoke{
		path:          bidiAppendProcedure,
		body:          bidiAppendBody(t, requestID, agentTestOfficialModel),
		contentType:   "application/connect+proto",
		authorization: agentTestInboundAuth,
		officialURL:   official.URL + bidiAppendProcedure,
		mode:          server.ModeLocal,
	}); got.Code != http.StatusAccepted {
		t.Fatalf("first append status = %d", got.Code)
	}
	if got := invokeAgentRoute(t, action, agentRouteInvoke{
		path:          bidiAppendProcedure,
		body:          malformedBidiAppendBody(t, requestID),
		contentType:   "application/connect+proto",
		authorization: agentTestInboundAuth,
		officialURL:   official.URL + bidiAppendProcedure,
		mode:          server.ModeLocal,
	}); got.Code != http.StatusAccepted {
		t.Fatalf("malformed follow-up status = %d", got.Code)
	}
	if atomic.LoadInt32(&officialHits) != 2 {
		t.Fatalf("official hits = %d", officialHits)
	}
}

func TestAgentRouteMalformedFollowUpFailClosedWithoutSession(t *testing.T) {
	t.Parallel()
	officialHits := int32(0)
	official := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		atomic.AddInt32(&officialHits, 1)
	}))
	defer official.Close()
	action := AgentRouteAction(agentRouteDeps(t, official.Client()), NewAgentSessionStore(), http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		t.Error("local handler must not run")
	}))
	_, err := invokeAgentRouteErr(t, action, agentRouteInvoke{
		path:          bidiAppendProcedure,
		body:          malformedBidiAppendBody(t, "req-malformed-new"),
		contentType:   "application/connect+proto",
		authorization: agentTestInboundAuth,
		officialURL:   official.URL + bidiAppendProcedure,
		mode:          server.ModeLocal,
	})
	if err == nil {
		t.Fatal("malformed append without a session returned success")
	}
	if atomic.LoadInt32(&officialHits) != 0 {
		t.Fatal("malformed append without a session hit official")
	}
}

type agentRouteInvoke struct {
	path          string
	body          []byte
	contentType   string
	authorization string
	checksum      string
	officialURL   string
	mode          server.ExecutionMode
	ctx           context.Context
}

func invokeAgentRoute(t *testing.T, action server.HandlerFunc, options agentRouteInvoke) *httptest.ResponseRecorder {
	t.Helper()
	recorder, err := invokeAgentRouteErr(t, action, options)
	if err != nil {
		t.Fatalf("agent route: %v", err)
	}
	return recorder
}

func invokeAgentRouteErr(t *testing.T, action server.HandlerFunc, options agentRouteInvoke) (*httptest.ResponseRecorder, error) {
	t.Helper()
	request := httptest.NewRequest(http.MethodPost, "http://backend.local"+options.path, bytes.NewReader(options.body))
	if options.ctx != nil {
		request = request.WithContext(options.ctx)
	}
	request.Header.Set("Authorization", options.authorization)
	request.Header.Set("content-type", options.contentType)
	if options.checksum != "" {
		request.Header.Set("x-cursor-checksum", options.checksum)
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
	}
	ctx := &server.Context{
		Writer:      recorder,
		Request:     request,
		RouteName:   "agent_route_test",
		UpstreamURL: target,
		Mode:        options.mode,
		StartedAt:   time.Now(),
	}
	return recorder, action(ctx)
}

func agentRouteDeps(t *testing.T, client HTTPClient) Dependencies {
	t.Helper()
	return Dependencies{
		SystemSettingService: stubModelAdapters{adapters: []legacyruntime.ModelAdapterConfig{{
			ID:          agentTestLocalChannel,
			DisplayName: "Local Grok",
			ModelID:     agentTestProviderModel,
			Type:        "openai",
			APIKey:      "sk-secret-should-not-leak",
			BaseURL:     "https://provider.example/v1",
		}}},
		HTTPClient: client,
	}
}

type errModelAdapters struct {
	err error
}

func (stub errModelAdapters) ResolveModelAdapters(context.Context) ([]legacyruntime.ModelAdapterConfig, error) {
	return nil, stub.err
}

func bidiAppendBody(t *testing.T, requestID string, modelID string) []byte {
	t.Helper()
	clientMessage := &agentv1.AgentClientMessage{}
	if strings.TrimSpace(modelID) != "" {
		clientMessage.Message = &agentv1.AgentClientMessage_RunRequest{
			RunRequest: &agentv1.AgentRunRequest{
				RequestedModel: &agentv1.RequestedModel{ModelId: modelID},
			},
		}
	}
	encoded, err := proto.Marshal(clientMessage)
	if err != nil {
		t.Fatal(err)
	}
	payload, err := proto.Marshal(&aiserverv1.BidiAppendRequest{
		RequestId: &aiserverv1.BidiRequestId{RequestId: requestID},
		Data:      hex.EncodeToString(encoded),
	})
	if err != nil {
		t.Fatal(err)
	}
	return connectEnvelope(payload)
}

func malformedBidiAppendBody(t *testing.T, requestID string) []byte {
	t.Helper()
	payload, err := proto.Marshal(&aiserverv1.BidiAppendRequest{
		RequestId: &aiserverv1.BidiRequestId{RequestId: requestID},
		Data:      "not-valid-hex",
	})
	if err != nil {
		t.Fatal(err)
	}
	return connectEnvelope(payload)
}

func runSSEBody(t *testing.T, requestID string) []byte {
	t.Helper()
	payload, err := proto.Marshal(&aiserverv1.BidiRequestId{RequestId: requestID})
	if err != nil {
		t.Fatal(err)
	}
	return connectEnvelope(payload)
}

func connectEnvelope(payload []byte) []byte {
	envelope := make([]byte, 5+len(payload))
	binary.BigEndian.PutUint32(envelope[1:5], uint32(len(payload)))
	copy(envelope[5:], payload)
	return envelope
}

func waitForAgentWaiter(t *testing.T, store *AgentSessionStore, requestID string) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		store.mu.Lock()
		session := store.sessions[requestID]
		waiting := session != nil && len(session.waiters) > 0
		store.mu.Unlock()
		if waiting {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatal("RunSSE waiter was not registered")
}

func TestParseBidiAppendRoutingFromConnectEnvelope(t *testing.T) {
	t.Parallel()
	body := bidiAppendBody(t, "req-parse", agentTestLocalChannel+":high")
	requestID, modelID, err := parseBidiAppendRouting("application/connect+proto", body)
	if err != nil {
		t.Fatal(err)
	}
	if requestID != "req-parse" || modelID != agentTestLocalChannel+":high" {
		t.Fatalf("request=%q model=%q", requestID, modelID)
	}
}

func TestParseRunSSERequestIDFromConnectEnvelope(t *testing.T) {
	t.Parallel()
	requestID, err := parseRunSSERequestID("application/connect+proto", runSSEBody(t, "req-sse"))
	if err != nil {
		t.Fatal(err)
	}
	if requestID != "req-sse" {
		t.Fatalf("request=%q", requestID)
	}
}

func TestForwardOfficialAgentDoesNotWriteWhenTargetMissing(t *testing.T) {
	t.Parallel()
	reqCtx := &RequestContext{
		ResponseWriter: httptest.NewRecorder(),
		Request:        httptest.NewRequest(http.MethodPost, "http://backend.local"+bidiAppendProcedure, bytes.NewReader(nil)),
		Method:         http.MethodPost,
		Headers:        http.Header{"Authorization": []string{agentTestInboundAuth}},
		Mode:           server.ModeLocal,
		Deps:           &Dependencies{},
	}
	err := forwardOfficialAgent(reqCtx)
	if err == nil {
		t.Fatal("expected missing target error")
	}
}
