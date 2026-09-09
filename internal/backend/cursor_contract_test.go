package backend

import (
	"bytes"
	"context"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"cursor/gen/agentv1"
	"cursor/gen/aiserverv1"
	modeladapter "cursor/internal/backend/agent/model"
	"cursor/internal/backend/server"
	serverconfig "cursor/internal/backend/server/config"
	"cursor/internal/modelchannel"
	legacyruntime "cursor/internal/runtime"
	"cursor/internal/subscriptionauth"

	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"
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

func TestGatewayDuoHostCatalogIdentity(t *testing.T) {
	const officialID = "model-a" // Deliberately collides with the local provider model, not its channel ID.
	for _, mode := range []string{"local", "upstream"} {
		t.Run(mode, func(t *testing.T) {
			manager := newHostConfigTestManager(t)
			cfg := DefaultHostTestConfig(t, manager, func(cfg *serverconfig.Config) {
				cfg.Routing.Mode = mode
				cfg.ModelAdapters = []serverconfig.ModelAdapterConfig{cliCatalogStaticAdapter("Model A", "https://provider.example/v1", "provider-secret", officialID, 1)}
			})
			host := &Host{configs: manager}
			if err := host.rebuild(cfg); err != nil {
				t.Fatal(err)
			}
			var officialHits cliCatalogAtomicInt
			official := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				officialHits.Add(1)
				if got := r.Header.Get("Authorization"); got != "Bearer official-test-token" {
					t.Errorf("official received identity %q", got)
				}
				model := &agentv1.ModelDetails{ModelId: officialID, DisplayModelId: officialID, DisplayName: "Model A", DisplayNameShort: "A"}
				var response proto.Message = &agentv1.GetUsableModelsResponse{Models: []*agentv1.ModelDetails{model}}
				if strings.HasSuffix(r.URL.Path, "/GetDefaultModelForCli") {
					response = &agentv1.GetDefaultModelForCliResponse{Model: model}
				}
				body, err := proto.Marshal(response)
				if err != nil {
					t.Error(err)
					return
				}
				w.Header().Set("Content-Type", "application/proto")
				_, _ = w.Write(body)
			}))
			defer official.Close()
			for _, auth := range []string{"", "Bearer " + legacyruntime.LocalRelayToken, "Bearer official-test-token"} {
				for _, service := range []string{"/aiserver.v1.AiService/", "/agent.v1.AgentService/"} {
					for _, method := range []string{"GetUsableModels", "GetDefaultModelForCli"} {
						path := service + method
						before := officialHits.Load()
						req := httptest.NewRequest(http.MethodPost, path, nil)
						req.Header.Set("Content-Type", "application/proto")
						req.Header.Set("Authorization", auth)
						req.Header.Set(server.HeaderServerUpstreamURL, official.URL+path)
						recorder := httptest.NewRecorder()
						host.mux.ServeHTTP(recorder, req)
						if recorder.Code != http.StatusOK {
							t.Fatalf("%s status=%d body=%s", path, recorder.Code, recorder.Body.String())
						}
						isOfficial := auth == "Bearer official-test-token"
						var models []*agentv1.ModelDetails
						if method == "GetUsableModels" {
							response := &agentv1.GetUsableModelsResponse{}
							if err := proto.Unmarshal(recorder.Body.Bytes(), response); err != nil {
								t.Fatal(err)
							}
							models = response.GetModels()
							want := 1
							if isOfficial {
								want = 2
							}
							if len(models) != want {
								t.Fatalf("%s model count=%d want=%d", path, len(models), want)
							}
						} else {
							response := &agentv1.GetDefaultModelForCliResponse{}
							if err := proto.Unmarshal(recorder.Body.Bytes(), response); err != nil {
								t.Fatal(err)
							}
							models = []*agentv1.ModelDetails{response.GetModel()}
						}
						for _, model := range models {
							if model.GetModelId() == officialID {
								if !isOfficial || model.GetDisplayName() != "Model A [官方]" || model.GetApiKeyCredentials() != nil {
									t.Fatalf("invalid official model: %v", model)
								}
							} else if model.GetDisplayName() != "Model A [BYOK]" || model.GetApiKeyCredentials().GetApiKey() != "cursor-byok-local" {
								t.Fatalf("invalid local model: %v", model)
							}
						}
						if (officialHits.Load() > before) != isOfficial {
							t.Fatalf("%s identity did not control upstream fetch", path)
						}
						if strings.Contains(recorder.Body.String(), "provider-secret") || strings.Contains(recorder.Body.String(), "provider.example") {
							t.Fatal("provider credentials leaked")
						}
					}
				}
			}
		})
	}
}

func TestGatewayDuoRunSSESurvivesConfigRebuild(t *testing.T) {
	manager := newHostConfigTestManager(t)
	cfg := DefaultHostTestConfig(t, manager, nil)
	host := &Host{configs: manager}
	if err := host.rebuild(cfg); err != nil {
		t.Fatal(err)
	}
	oldMux := host.mux
	official := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer official-test-token" {
			t.Error("official identity changed")
		}
		w.WriteHeader(http.StatusAccepted)
		_, _ = io.WriteString(w, "official-rebuilt")
	}))
	defer official.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	request := func(path string, body []byte) *http.Request {
		req := httptest.NewRequest(http.MethodPost, path, bytes.NewReader(body)).WithContext(ctx)
		req.Header.Set("Content-Type", "application/connect+proto")
		req.Header.Set("Authorization", "Bearer official-test-token")
		req.Header.Set(server.HeaderServerUpstreamURL, official.URL+path)
		return req
	}
	const requestID = "stream-across-rebuild"
	stream := request("/agent.v1.AgentService/RunSSE", duoAgentRunSSEBody(t, requestID))
	streamResult := httptest.NewRecorder()
	started, done := make(chan struct{}), make(chan struct{})
	go func() {
		close(started)
		oldMux.ServeHTTP(streamResult, stream)
		close(done)
	}()
	<-started
	// The request keeps its old handler even if it is scheduled after rebuild.
	cfg.Appearance.Theme = "dark"
	if _, err := host.SaveConfig(context.Background(), cfg); err != nil {
		t.Fatal(err)
	}
	appendResult := httptest.NewRecorder()
	host.mux.ServeHTTP(appendResult, request("/aiserver.v1.BidiService/BidiAppend", duoAgentBidiRunBody(t, requestID, "auto")))
	if appendResult.Code != http.StatusAccepted {
		t.Fatalf("append status=%d: %s", appendResult.Code, appendResult.Body.String())
	}
	<-done
	if streamResult.Code != http.StatusAccepted || streamResult.Body.String() != "official-rebuilt" {
		t.Fatalf("old stream lost route across config rebuild: status=%d body=%q", streamResult.Code, streamResult.Body.String())
	}
}

func TestGatewayDuoHostOAuthBothModes(t *testing.T) {
	for _, mode := range []string{"local", "upstream"} {
		t.Run(mode, func(t *testing.T) {
			manager := newHostConfigTestManager(t)
			cfg := DefaultHostTestConfig(t, manager, func(cfg *serverconfig.Config) { cfg.Routing.Mode = mode })
			host := &Host{configs: manager}
			if err := host.rebuild(cfg); err != nil {
				t.Fatal(err)
			}
			var hits cliCatalogAtomicInt
			official := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				hits.Add(1)
				body, _ := io.ReadAll(r.Body)
				if string(body) != `{"refresh_token":"real-refresh"}` || r.Header.Get("Authorization") != "Bearer official-test-token" {
					t.Error("OAuth body or identity was changed, or local refresh reached official")
				}
				w.Header().Set("Content-Type", "application/json")
				_, _ = io.WriteString(w, `{"access_token":"refreshed-official"}`)
			}))
			defer official.Close()
			for _, local := range []bool{true, false} {
				token, auth := "real-refresh", "Bearer official-test-token"
				if local {
					token, auth = legacyruntime.LocalRelayToken, "Bearer "+legacyruntime.LocalRelayToken
				}
				body := fmt.Sprintf(`{"refresh_token":%q}`, token)
				req := httptest.NewRequest(http.MethodPost, "/oauth/token", strings.NewReader(body))
				req.Header.Set("Content-Type", "application/json")
				req.Header.Set("Authorization", auth)
				req.Header.Set(server.HeaderServerUpstreamURL, official.URL+"/oauth/token")
				recorder := httptest.NewRecorder()
				host.mux.ServeHTTP(recorder, req)
				if recorder.Code != http.StatusOK {
					t.Fatalf("oauth status=%d", recorder.Code)
				}
				if local {
					if hits.Load() != 0 || !strings.Contains(recorder.Body.String(), token) {
						t.Fatal("local OAuth did not stay local")
					}
				} else if hits.Load() != 1 || !strings.Contains(recorder.Body.String(), "refreshed-official") {
					t.Fatal("real OAuth was not forwarded")
				}
			}
		})
	}
}

func TestGatewayDuoHostAgentRouteIdentity(t *testing.T) {
	const (
		officialAuth = "Bearer official-test-token"
		providerID   = "model-a"
		officialID   = "official-opus"
	)
	manager := newHostConfigTestManager(t)
	provider := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		writer.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(writer, "data: {\"choices\":[{\"delta\":{\"content\":\"local-ok\"},\"finish_reason\":\"stop\"}]}\n\n")
		_, _ = io.WriteString(writer, "data: [DONE]\n\n")
	}))
	defer provider.Close()

	cfg := DefaultHostTestConfig(t, manager, func(cfg *serverconfig.Config) {
		cfg.Routing.Mode = "local"
		cfg.ModelAdapters = []serverconfig.ModelAdapterConfig{
			cliCatalogStaticAdapter("Model A", provider.URL, "provider-secret", providerID, 1),
		}
	})
	host := &Host{configs: manager}
	if err := host.rebuild(cfg); err != nil {
		t.Fatal(err)
	}

	usable := &agentv1.GetUsableModelsResponse{}
	if err := proto.Unmarshal(postLocalCLICatalog(t, host, "/agent.v1.AgentService/GetUsableModels"), usable); err != nil {
		t.Fatal(err)
	}
	if len(usable.GetModels()) != 1 {
		t.Fatalf("catalog count=%d", len(usable.GetModels()))
	}
	localID := usable.GetModels()[0].GetModelId()
	if localID == "" || localID == providerID {
		t.Fatalf("catalog id %q should be the local channel hash", localID)
	}

	var officialHits cliCatalogAtomicInt
	official := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		officialHits.Add(1)
		if got := request.Header.Get("Authorization"); got != officialAuth {
			t.Errorf("official identity %q", got)
		}
		writer.WriteHeader(http.StatusAccepted)
		_, _ = writer.Write([]byte("official-ok"))
	}))
	defer official.Close()

	post := func(path, auth, requestID, modelID string, run bool) *httptest.ResponseRecorder {
		t.Helper()
		var body []byte
		if path == "/agent.v1.AgentService/RunSSE" {
			body = duoAgentRunSSEBody(t, requestID)
		} else if run {
			body = duoAgentBidiRunBody(t, requestID, modelID)
		} else {
			body = duoAgentBidiFollowupBody(t, requestID)
		}
		req := httptest.NewRequest(http.MethodPost, path, bytes.NewReader(body))
		req.Header.Set("Content-Type", "application/connect+proto")
		req.Header.Set("Authorization", auth)
		req.Header.Set(server.HeaderServerUpstreamURL, official.URL+path)
		recorder := httptest.NewRecorder()
		host.mux.ServeHTTP(recorder, req)
		return recorder
	}

	bidi := "/aiserver.v1.BidiService/BidiAppend"
	sse := "/agent.v1.AgentService/RunSSE"
	for i, modelID := range []string{officialID, providerID, "auto", ""} {
		rec := post(bidi, officialAuth, fmt.Sprintf("req-official-%d", i), modelID, true)
		if rec.Code != http.StatusAccepted || rec.Body.String() != "official-ok" {
			t.Fatalf("official model %q status=%d body=%q", modelID, rec.Code, rec.Body.String())
		}
	}
	if officialHits.Load() != 4 {
		t.Fatalf("official initial hits=%d", officialHits.Load())
	}

	const followID = "req-follow"
	if rec := post(bidi, officialAuth, followID, officialID, true); rec.Code != http.StatusAccepted {
		t.Fatalf("follow seed status=%d", rec.Code)
	}
	if rec := post(bidi, officialAuth, followID, "", false); rec.Code != http.StatusAccepted || rec.Body.String() != "official-ok" {
		t.Fatalf("follow-up status=%d body=%q", rec.Code, rec.Body.String())
	}
	if rec := post(sse, officialAuth, followID, "", false); rec.Code != http.StatusAccepted || rec.Body.String() != "official-ok" {
		t.Fatalf("runsse status=%d body=%q", rec.Code, rec.Body.String())
	}
	if officialHits.Load() != 7 {
		t.Fatalf("official hits after follow/sse=%d", officialHits.Load())
	}

	beforeLocal := officialHits.Load()
	localRec := post(bidi, officialAuth, "req-local-catalog", localID, true)
	if officialHits.Load() != beforeLocal {
		t.Fatalf("local catalog id hit official; status=%d body=%q", localRec.Code, localRec.Body.String())
	}
	if strings.Contains(localRec.Body.String(), "official-ok") {
		t.Fatalf("local catalog id returned official body %q", localRec.Body.String())
	}
}

func TestCursorCLIModelCatalogDualProtocolPaths(t *testing.T) {
	manager := newHostConfigTestManager(t)
	cfg := DefaultHostTestConfig(t, manager, func(cfg *serverconfig.Config) {
		cfg.Routing.Mode = "local"
		cfg.ModelAdapters = []serverconfig.ModelAdapterConfig{
			{
				Sort:            1,
				DisplayName:     "Model A",
				Type:            "openai",
				BaseURL:         "https://provider-a.example/v1",
				APIKey:          "provider-secret-a",
				TooltipData:     "Model A",
				ModelID:         "model-a",
				ReasoningEffort: "medium",
				OpenAIEndpoint:  "/v1/chat/completions",
			},
			{
				Sort:            2,
				DisplayName:     "Model B",
				Type:            "openai",
				BaseURL:         "https://provider-b.example/v1",
				APIKey:          "provider-secret-b",
				TooltipData:     "Model B",
				ModelID:         "model-b",
				ReasoningEffort: "medium",
				OpenAIEndpoint:  "/v1/chat/completions",
			},
		}
	})
	host := &Host{configs: manager}
	if err := host.rebuild(cfg); err != nil {
		t.Fatalf("rebuild() error = %v", err)
	}

	usablePaths := []string{
		"/aiserver.v1.AiService/GetUsableModels",
		"/agent.v1.AgentService/GetUsableModels",
	}
	defaultPaths := []string{
		"/aiserver.v1.AiService/GetDefaultModelForCli",
		"/agent.v1.AgentService/GetDefaultModelForCli",
	}

	var usableBodies [][]byte
	for _, path := range usablePaths {
		body := postLocalCLICatalog(t, host, path)
		response := &agentv1.GetUsableModelsResponse{}
		if err := proto.Unmarshal(body, response); err != nil {
			t.Fatalf("%s decode usable models: %v", path, err)
		}
		if len(response.GetModels()) != 2 {
			t.Fatalf("%s usable model count = %d, want 2", path, len(response.GetModels()))
		}
		if response.GetModels()[0].GetModelId() == "" {
			t.Fatalf("%s usable model id is empty", path)
		}
		assertCLICatalogSentinel(t, path, response.GetModels()[0].GetApiKeyCredentials(), body)
		usableBodies = append(usableBodies, body)
	}
	if !bytes.Equal(usableBodies[0], usableBodies[1]) {
		t.Fatal("GetUsableModels agent/aiserver paths must share the same builder encoding")
	}

	var defaultBodies [][]byte
	for _, path := range defaultPaths {
		body := postLocalCLICatalog(t, host, path)
		response := &agentv1.GetDefaultModelForCliResponse{}
		if err := proto.Unmarshal(body, response); err != nil {
			t.Fatalf("%s decode default model: %v", path, err)
		}
		if response.GetModel() == nil || response.GetModel().GetModelId() == "" {
			t.Fatalf("%s default model is empty: %#v", path, response.GetModel())
		}
		assertCLICatalogSentinel(t, path, response.GetModel().GetApiKeyCredentials(), body)
		defaultBodies = append(defaultBodies, body)
	}
	if !bytes.Equal(defaultBodies[0], defaultBodies[1]) {
		t.Fatal("GetDefaultModelForCli agent/aiserver paths must share the same builder encoding")
	}

	usable := &agentv1.GetUsableModelsResponse{}
	if err := proto.Unmarshal(usableBodies[0], usable); err != nil {
		t.Fatalf("decode usable models: %v", err)
	}
	def := &agentv1.GetDefaultModelForCliResponse{}
	if err := proto.Unmarshal(defaultBodies[0], def); err != nil {
		t.Fatalf("decode default model: %v", err)
	}
	if len(usable.GetModels()) != 2 {
		t.Fatalf("usable catalog length = %d, want 2", len(usable.GetModels()))
	}
	if def.GetModel() == nil || def.GetModel().GetModelId() != usable.GetModels()[0].GetModelId() {
		t.Fatalf("default model %q is not the first catalog model %q", def.GetModel().GetModelId(), usable.GetModels()[0].GetModelId())
	}
	if def.GetModel().GetModelId() == usable.GetModels()[1].GetModelId() {
		t.Fatal("GetDefaultModelForCli returned a list-shaped default instead of the first catalog model")
	}

	cfg.Routing.Mode = "upstream"
	saved, err := manager.Save(context.Background(), cfg)
	if err != nil {
		t.Fatalf("save upstream routing: %v", err)
	}
	if err := host.rebuild(saved); err != nil {
		t.Fatalf("rebuild upstream routing: %v", err)
	}
	for _, path := range append(append([]string{}, usablePaths...), defaultPaths...) {
		body := postLocalCLICatalog(t, host, path)
		if len(body) == 0 {
			t.Fatalf("%s upstream preference without target did not stay local", path)
		}
	}
}

func postLocalCLICatalog(t *testing.T, host *Host, path string) []byte {
	t.Helper()
	return postLocalCLICatalogWithUpstream(t, host, path, "")
}

func postLocalCLICatalogWithUpstream(t *testing.T, host *Host, path, upstreamTarget string) []byte {
	t.Helper()
	request := httptest.NewRequest(http.MethodPost, path, strings.NewReader(""))
	if upstreamTarget != "" {
		request.Header.Set(server.HeaderServerUpstreamURL, upstreamTarget)
	}
	recorder := httptest.NewRecorder()
	host.mux.ServeHTTP(recorder, request)
	if recorder.Code == http.StatusNotFound {
		t.Fatalf("%s was not registered", path)
	}
	if recorder.Code != http.StatusOK {
		t.Fatalf("%s status = %d body=%q", path, recorder.Code, recorder.Body.String())
	}
	return recorder.Body.Bytes()
}

func assertCLICatalogSentinel(t *testing.T, path string, credentials *agentv1.ApiKeyCredentials, body []byte) {
	t.Helper()
	if credentials == nil || credentials.GetApiKey() != "cursor-byok-local" {
		t.Fatalf("%s credentials = %#v", path, credentials)
	}
	if credentials.BaseUrl != nil {
		t.Fatalf("%s protobuf baseUrl is set: %q", path, credentials.GetBaseUrl())
	}
	encoded, err := protojson.Marshal(credentials)
	if err != nil {
		t.Fatalf("%s protojson marshal credentials: %v", path, err)
	}
	if strings.Contains(strings.ToLower(string(encoded)), "baseurl") || strings.Contains(string(encoded), "base_url") {
		t.Fatalf("%s JSON credentials included baseUrl: %s", path, encoded)
	}
	if bytes.Contains(body, []byte("provider-secret")) || bytes.Contains(body, []byte("provider-a.example")) || bytes.Contains(body, []byte("provider-b.example")) {
		t.Fatalf("%s catalog leaked provider secret or base URL", path)
	}
}

func TestCLICatalogChannelIDsResolveWithServerCredentials(t *testing.T) {
	const providerSecret = "provider-secret-a"
	manager := newHostConfigTestManager(t)
	cfg := DefaultHostTestConfig(t, manager, func(cfg *serverconfig.Config) {
		cfg.ModelAdapters = []serverconfig.ModelAdapterConfig{{
			Sort:            1,
			DisplayName:     "Model A",
			Type:            "openai",
			BaseURL:         "https://provider-a.example/v1",
			APIKey:          providerSecret,
			TooltipData:     "Model A",
			ModelID:         "model-a",
			ReasoningEffort: "medium",
			OpenAIEndpoint:  "/v1/chat/completions",
		}}
	})
	host := &Host{configs: manager}
	if err := host.rebuild(cfg); err != nil {
		t.Fatalf("rebuild() error = %v", err)
	}

	body := postLocalCLICatalog(t, host, "/agent.v1.AgentService/GetUsableModels")
	catalog := &agentv1.GetUsableModelsResponse{}
	if err := proto.Unmarshal(body, catalog); err != nil {
		t.Fatalf("decode catalog: %v", err)
	}
	if len(catalog.GetModels()) != 1 {
		t.Fatalf("catalog count = %d, want 1", len(catalog.GetModels()))
	}
	model := catalog.GetModels()[0]
	credentials := model.GetApiKeyCredentials()
	if credentials == nil || credentials.GetApiKey() != "cursor-byok-local" {
		t.Fatalf("catalog credentials = %#v", credentials)
	}
	if credentials.BaseUrl != nil {
		t.Fatalf("catalog protobuf HasBaseUrl: %q", credentials.GetBaseUrl())
	}
	if strings.Contains(string(body), providerSecret) || strings.Contains(string(body), "provider-a.example") {
		t.Fatal("catalog proto leaked provider secret or base URL")
	}

	channel, err := manager.SelectChannelForModel(context.Background(), model.GetModelId())
	if err != nil {
		t.Fatalf("SelectChannelForModel(%q): %v", model.GetModelId(), err)
	}
	if channel.APIKey != providerSecret {
		t.Fatalf("resolved APIKey = %q, want provider secret", channel.APIKey)
	}
	if channel.APIKey == "cursor-byok-local" {
		t.Fatal("SelectChannelForModel returned catalog sentinel")
	}
	if channel.BaseURL != "https://provider-a.example/v1" {
		t.Fatalf("resolved BaseURL = %q", channel.BaseURL)
	}
}

func TestCLICatalogEmptyModelsStayLegal(t *testing.T) {
	manager := newHostConfigTestManager(t)
	cfg := DefaultHostTestConfig(t, manager, nil)
	host := &Host{configs: manager}
	if err := host.rebuild(cfg); err != nil {
		t.Fatalf("rebuild() error = %v", err)
	}

	usable := &agentv1.GetUsableModelsResponse{}
	if err := proto.Unmarshal(postLocalCLICatalog(t, host, "/aiserver.v1.AiService/GetUsableModels"), usable); err != nil {
		t.Fatalf("decode empty usable models: %v", err)
	}
	if len(usable.GetModels()) != 0 {
		t.Fatalf("empty usable models = %#v", usable.GetModels())
	}
	def := &agentv1.GetDefaultModelForCliResponse{}
	if err := proto.Unmarshal(postLocalCLICatalog(t, host, "/agent.v1.AgentService/GetDefaultModelForCli"), def); err != nil {
		t.Fatalf("decode empty default model: %v", err)
	}
	if def.GetModel() == nil {
		t.Fatal("empty default model should remain present")
	}
	if def.GetModel().GetModelId() != "" {
		t.Fatalf("empty default modelId = %q", def.GetModel().GetModelId())
	}
}

func TestCLICatalogLocalAndUpstreamPolicyWithTargetHeader(t *testing.T) {
	paths := []string{
		"/aiserver.v1.AiService/GetUsableModels",
		"/agent.v1.AgentService/GetUsableModels",
		"/aiserver.v1.AiService/GetDefaultModelForCli",
		"/agent.v1.AgentService/GetDefaultModelForCli",
	}
	var officialHits sync.Map
	official := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		officialHits.Store(request.URL.Path, true)
		writer.Header().Set("content-type", "text/plain")
		writer.WriteHeader(http.StatusOK)
		_, _ = io.WriteString(writer, "official-catalog")
	}))
	defer official.Close()

	manager := newHostConfigTestManager(t)
	cfg := DefaultHostTestConfig(t, manager, func(cfg *serverconfig.Config) {
		cfg.Routing.Mode = "local"
		cfg.ModelAdapters = []serverconfig.ModelAdapterConfig{cliCatalogStaticAdapter("Model A", "https://provider-a.example/v1", "provider-secret-a", "model-a", 1)}
	})
	host := &Host{configs: manager}
	if err := host.rebuild(cfg); err != nil {
		t.Fatalf("rebuild() error = %v", err)
	}

	for _, path := range paths {
		body := postLocalCLICatalogWithUpstream(t, host, path, official.URL+path)
		if bytes.Contains(body, []byte("official-catalog")) {
			t.Fatalf("%s local mode with upstream target forwarded to official", path)
		}
		if path == "/aiserver.v1.AiService/GetUsableModels" || path == "/agent.v1.AgentService/GetUsableModels" {
			response := &agentv1.GetUsableModelsResponse{}
			if err := proto.Unmarshal(body, response); err != nil {
				t.Fatalf("%s decode local catalog: %v", path, err)
			}
			if len(response.GetModels()) != 1 {
				t.Fatalf("%s local catalog count = %d", path, len(response.GetModels()))
			}
			assertCLICatalogSentinel(t, path, response.GetModels()[0].GetApiKeyCredentials(), body)
		} else {
			response := &agentv1.GetDefaultModelForCliResponse{}
			if err := proto.Unmarshal(body, response); err != nil {
				t.Fatalf("%s decode local default: %v", path, err)
			}
			assertCLICatalogSentinel(t, path, response.GetModel().GetApiKeyCredentials(), body)
		}
		if _, hit := officialHits.Load(path); hit {
			t.Fatalf("%s local mode with upstream target hit official", path)
		}
	}

	cfg.Routing.Mode = "upstream"
	saved, err := manager.Save(context.Background(), cfg)
	if err != nil {
		t.Fatalf("save upstream routing: %v", err)
	}
	if err := host.rebuild(saved); err != nil {
		t.Fatalf("rebuild upstream routing: %v", err)
	}
	for _, path := range paths {
		body := postLocalCLICatalogWithUpstream(t, host, path, official.URL+path)
		if bytes.Contains(body, []byte("official-catalog")) || !bytes.Contains(body, []byte("Model A [BYOK]")) {
			t.Fatalf("%s without official identity must keep local catalog in upstream mode: %q", path, body)
		}
		if _, hit := officialHits.Load(path); hit {
			t.Fatalf("%s without official identity hit official", path)
		}
	}
}

func TestCLICatalogIDsApplyRuntimeCredentialsToSyntheticProvider(t *testing.T) {
	const (
		staticSecret    = "static-runtime-secret"
		primarySecret   = "fallback-primary-secret"
		candidateSecret = "fallback-candidate-secret"
		sentinel        = "cursor-byok-local"
	)

	staticAuths, staticHits := newCLICatalogAuthRecorder()
	staticProvider := httptest.NewServer(cliCatalogOpenAIHandler(t, staticAuths, staticHits, sentinel))
	defer staticProvider.Close()

	logicalHits := 0
	logicalProvider := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		logicalHits++
		t.Error("fallback logical alias was contacted")
	}))
	defer logicalProvider.Close()

	primaryAuths, primaryHits := newCLICatalogAuthRecorder()
	primaryProvider := httptest.NewServer(cliCatalogOpenAIHandler(t, primaryAuths, primaryHits, sentinel))
	defer primaryProvider.Close()

	candidateHits := 0
	candidateProvider := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		candidateHits++
		t.Error("fallback candidate was contacted")
	}))
	defer candidateProvider.Close()

	manager := newHostConfigTestManager(t)
	seed := DefaultHostTestConfig(t, manager, func(cfg *serverconfig.Config) {
		cfg.ModelAdapters = []serverconfig.ModelAdapterConfig{
			cliCatalogStaticAdapter("static-byok", staticProvider.URL, staticSecret, "static-model", 1),
			cliCatalogManagedAdapter("managed-codex", "codex", "managed-model", 2),
			cliCatalogManagedAdapter("managed-grok", "grok", "grok-model", 3),
			cliCatalogStaticAdapter("logical-alias", logicalProvider.URL, "logical-unused-secret", "logical-model", 4),
			cliCatalogStaticAdapter("fallback-primary", primaryProvider.URL, primarySecret, "primary-model", 5),
			cliCatalogStaticAdapter("fallback-candidate", candidateProvider.URL, candidateSecret, "candidate-model", 6),
		}
	})
	idByName := map[string]string{}
	for _, adapter := range seed.ModelAdapters {
		idByName[adapter.DisplayName] = adapter.ID
	}
	logical := seed.ModelAdapters
	for i := range logical {
		if logical[i].DisplayName != "logical-alias" {
			continue
		}
		logical[i].ProviderFallback = serverconfig.ProviderFallbackConfig{
			Enabled:             true,
			PrimaryChannelID:    idByName["fallback-primary"],
			CandidateChannelIDs: []string{idByName["fallback-candidate"]},
		}
	}
	saved, err := manager.Save(context.Background(), seed)
	if err != nil {
		t.Fatalf("save fallback plan: %v", err)
	}
	host := &Host{configs: manager}
	if err := host.rebuild(saved); err != nil {
		t.Fatalf("rebuild() error = %v", err)
	}

	catalog := &agentv1.GetUsableModelsResponse{}
	if err := proto.Unmarshal(postLocalCLICatalog(t, host, "/agent.v1.AgentService/GetUsableModels"), catalog); err != nil {
		t.Fatalf("decode catalog: %v", err)
	}
	modelByName := map[string]*agentv1.ModelDetails{}
	for _, model := range catalog.GetModels() {
		assertCLICatalogSentinel(t, model.GetDisplayName(), model.GetApiKeyCredentials(), nil)
		modelByName[model.GetDisplayName()] = model
	}
	staticModel := modelByName["static-byok [BYOK]"]
	managedModel := modelByName["managed-codex [BYOK]"]
	grokModel := modelByName["managed-grok [BYOK]"]
	logicalModel := modelByName["logical-alias [BYOK]"]
	if staticModel == nil || managedModel == nil || grokModel == nil || logicalModel == nil {
		t.Fatalf("catalog missing expected models: %#v", modelByName)
	}

	staticCh, err := manager.SelectChannelForModel(context.Background(), staticModel.GetModelId())
	if err != nil {
		t.Fatalf("static SelectChannelForModel: %v", err)
	}
	if staticCh.APIKey != staticSecret || staticCh.APIKey == sentinel {
		t.Fatalf("static APIKey = %q", staticCh.APIKey)
	}

	managedCh, err := manager.SelectChannelForModel(context.Background(), managedModel.GetModelId())
	if err != nil {
		t.Fatalf("managed SelectChannelForModel: %v", err)
	}
	if managedCh.APIKey != "" || managedCh.APIKey == sentinel {
		t.Fatalf("managed APIKey = %q, want empty until applyRuntimeCredentials", managedCh.APIKey)
	}
	if managedCh.CredentialSource != "codex" {
		t.Fatalf("managed CredentialSource = %q", managedCh.CredentialSource)
	}
	if managedCh.BaseURL != subscriptionauth.CodexResponsesURL {
		t.Fatalf("managed BaseURL = %q, want pinned Codex endpoint", managedCh.BaseURL)
	}
	if managedCh.OpenAIEndpoint != modelchannel.OpenAIEndpointResponses {
		t.Fatalf("managed OpenAIEndpoint = %q, want %s", managedCh.OpenAIEndpoint, modelchannel.OpenAIEndpointResponses)
	}
	if managedCh.Model != "managed-model" {
		t.Fatalf("managed Model = %q", managedCh.Model)
	}

	grokCh, err := manager.SelectChannelForModel(context.Background(), grokModel.GetModelId())
	if err != nil {
		t.Fatalf("grok SelectChannelForModel: %v", err)
	}
	if grokCh.APIKey != "" || grokCh.APIKey == sentinel {
		t.Fatalf("grok APIKey = %q, want empty until applyRuntimeCredentials", grokCh.APIKey)
	}
	if grokCh.CredentialSource != "grok" {
		t.Fatalf("grok CredentialSource = %q", grokCh.CredentialSource)
	}
	if grokCh.BaseURL != subscriptionauth.GrokAPIBaseURL {
		t.Fatalf("grok BaseURL = %q, want pinned Grok endpoint", grokCh.BaseURL)
	}
	if grokCh.OpenAIEndpoint != modelchannel.OpenAIEndpointChatCompletions {
		t.Fatalf("grok OpenAIEndpoint = %q, want %s", grokCh.OpenAIEndpoint, modelchannel.OpenAIEndpointChatCompletions)
	}
	if grokCh.Model != "grok-model" {
		t.Fatalf("grok Model = %q", grokCh.Model)
	}

	plan, err := manager.SelectChannelPlanForModel(context.Background(), logicalModel.GetModelId())
	if err != nil {
		t.Fatalf("fallback SelectChannelPlanForModel: %v", err)
	}
	if !plan.FallbackEnabled || len(plan.Channels) != 2 {
		t.Fatalf("fallback plan = %+v", plan)
	}
	if plan.Channels[0].APIKey != primarySecret || plan.Channels[1].APIKey != candidateSecret {
		t.Fatalf("fallback plan keys = %q, %q", plan.Channels[0].APIKey, plan.Channels[1].APIKey)
	}

	creds := &cliCatalogCredentialStub{token: "managed-runtime-token", accountID: "codex:synthetic"}
	router := modeladapter.NewRouter(manager)
	router.SetCredentialResolver(creds)
	fallback := modeladapter.NewFallbackAwareRouter(router, manager)
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	req := func(modelID string) modeladapter.StreamRequest {
		return modeladapter.StreamRequest{
			ModelID:  modelID,
			Messages: []modeladapter.Message{{Role: "user", Content: "ping"}},
		}
	}
	if err := fallback.Stream(ctx, req(staticModel.GetModelId()), func(modeladapter.ModelEvent) error { return nil }); err != nil {
		t.Fatalf("static Stream: %v", err)
	}
	if staticHits.Load() != 1 {
		t.Fatalf("static provider hits = %d, want 1", staticHits.Load())
	}
	if got := staticAuths.Load(); got != staticSecret {
		t.Fatalf("static Authorization = %q, want server secret", got)
	}

	if err := fallback.Stream(ctx, req(logicalModel.GetModelId()), func(modeladapter.ModelEvent) error { return nil }); err != nil {
		t.Fatalf("fallback Stream: %v", err)
	}
	if primaryHits.Load() != 1 {
		t.Fatalf("fallback primary hits = %d, want 1", primaryHits.Load())
	}
	if got := primaryAuths.Load(); got != primarySecret {
		t.Fatalf("fallback Authorization = %q, want primary secret", got)
	}
	if logicalHits != 0 || candidateHits != 0 {
		t.Fatalf("fallback extra hits logical=%d candidate=%d", logicalHits, candidateHits)
	}
	if got := creds.resolveCount(); got != 0 {
		t.Fatalf("static/fallback Stream resolved managed credentials %d times: %#v", got, creds.resolvedSources())
	}
}

func TestCLICatalogRuntimeConsistencyWithoutManagedRefresh(t *testing.T) {
	const sentinel = "cursor-byok-local"
	creds := &cliCatalogCredentialStub{token: "must-not-resolve", accountID: "codex:unused"}
	manager := newHostConfigTestManager(t)
	seed := DefaultHostTestConfig(t, manager, func(cfg *serverconfig.Config) {
		cfg.ModelAdapters = []serverconfig.ModelAdapterConfig{
			cliCatalogStaticAdapter("static-byok", "https://static.example/v1", "static-runtime-secret", "static-model", 1),
			cliCatalogManagedAdapter("managed-codex", "codex", "managed-model", 2),
			cliCatalogManagedAdapter("managed-grok", "grok", "grok-model", 3),
			cliCatalogStaticAdapter("logical-alias", "https://logical.example/v1", "logical-unused-secret", "logical-model", 4),
			cliCatalogStaticAdapter("fallback-primary", "https://primary.example/v1", "fallback-primary-secret", "primary-model", 5),
			cliCatalogStaticAdapter("fallback-candidate", "https://candidate.example/v1", "fallback-candidate-secret", "candidate-model", 6),
		}
	})
	idByName := map[string]string{}
	for _, adapter := range seed.ModelAdapters {
		idByName[adapter.DisplayName] = adapter.ID
	}
	for i := range seed.ModelAdapters {
		if seed.ModelAdapters[i].DisplayName != "logical-alias" {
			continue
		}
		seed.ModelAdapters[i].ProviderFallback = serverconfig.ProviderFallbackConfig{
			Enabled:             true,
			PrimaryChannelID:    idByName["fallback-primary"],
			CandidateChannelIDs: []string{idByName["fallback-candidate"]},
		}
	}
	saved, err := manager.Save(context.Background(), seed)
	if err != nil {
		t.Fatalf("save fallback plan: %v", err)
	}
	host := &Host{configs: manager}
	if err := host.rebuild(saved); err != nil {
		t.Fatalf("rebuild() error = %v", err)
	}
	router := modeladapter.NewRouter(manager)
	router.SetCredentialResolver(creds)

	catalog := &agentv1.GetUsableModelsResponse{}
	if err := proto.Unmarshal(postLocalCLICatalog(t, host, "/agent.v1.AgentService/GetUsableModels"), catalog); err != nil {
		t.Fatalf("decode CLI catalog: %v", err)
	}
	available := &aiserverv1.AvailableModelsResponse{}
	if err := proto.Unmarshal(postLocalCLICatalog(t, host, "/aiserver.v1.AiService/AvailableModels"), available); err != nil {
		t.Fatalf("decode available models: %v", err)
	}
	if got := creds.resolveCount(); got != 0 {
		t.Fatalf("catalog load resolved managed credentials %d times: %#v", got, creds.resolvedSources())
	}

	cliByName := map[string]*agentv1.ModelDetails{}
	for _, model := range catalog.GetModels() {
		assertCLICatalogSentinel(t, model.GetDisplayName(), model.GetApiKeyCredentials(), nil)
		cliByName[model.GetDisplayName()] = model
	}
	availableByName := map[string]*aiserverv1.AvailableModelsResponse_AvailableModel{}
	for _, model := range available.GetModels() {
		availableByName[model.GetClientDisplayName()] = model
	}

	cases := []struct {
		display      string
		providerID   string
		source       string
		baseURL      string
		endpoint     string
		fallbackPlan bool
	}{
		{display: "static-byok", providerID: "static-model", source: "static", baseURL: "https://static.example/v1", endpoint: modelchannel.OpenAIEndpointChatCompletions},
		{display: "managed-codex", providerID: "managed-model", source: "codex", baseURL: subscriptionauth.CodexResponsesURL, endpoint: modelchannel.OpenAIEndpointResponses},
		{display: "managed-grok", providerID: "grok-model", source: "grok", baseURL: subscriptionauth.GrokAPIBaseURL, endpoint: modelchannel.OpenAIEndpointChatCompletions},
		{display: "logical-alias", providerID: "logical-model", source: "static", fallbackPlan: true},
	}
	for _, tc := range cases {
		cli := cliByName[tc.display+" [BYOK]"]
		avail := availableByName[tc.display+" [BYOK]"]
		if cli == nil || avail == nil {
			t.Fatalf("%s missing from catalogs cli=%v available=%v", tc.display, cli != nil, avail != nil)
		}
		catalogID := cli.GetModelId()
		if catalogID == "" || catalogID == tc.providerID || catalogID == sentinel {
			t.Fatalf("%s catalog ID = %q", tc.display, catalogID)
		}
		if avail.GetName() != catalogID || avail.GetServerModelName() != catalogID {
			t.Fatalf("%s available ID name=%q server=%q, want catalog %q", tc.display, avail.GetName(), avail.GetServerModelName(), catalogID)
		}
		if !avail.GetSupportsAgent() || !avail.GetSupportsThinking() || !avail.GetSupportsImages() || !avail.GetSupportsPlanMode() {
			t.Fatalf("%s capabilities agent=%t thinking=%t images=%t plan=%t", tc.display, avail.GetSupportsAgent(), avail.GetSupportsThinking(), avail.GetSupportsImages(), avail.GetSupportsPlanMode())
		}
		if len(avail.GetParameterDefinitions()) == 0 || avail.GetParameterDefinitions()[0].GetId() != "thinking_effort" {
			t.Fatalf("%s thinking parameter = %#v", tc.display, avail.GetParameterDefinitions())
		}
		if got := defaultAvailableThinkingVariant(avail); got != catalogID+":medium" {
			t.Fatalf("%s default thinking variant = %q, want %s:medium", tc.display, got, catalogID)
		}

		if tc.fallbackPlan {
			if catalogID == idByName["fallback-primary"] || catalogID == idByName["fallback-candidate"] {
				t.Fatalf("logical catalog ID %q was rewritten to a physical fallback ID", catalogID)
			}
			logicalCh, err := manager.SelectChannelForModel(context.Background(), catalogID)
			if err != nil {
				t.Fatalf("logical SelectChannelForModel: %v", err)
			}
			if logicalCh.ID != catalogID || logicalCh.ID == idByName["fallback-primary"] {
				t.Fatalf("logical runtime ID = %q, want catalog logical ID", logicalCh.ID)
			}
			plan, err := manager.SelectChannelPlanForModel(context.Background(), catalogID)
			if err != nil {
				t.Fatalf("logical SelectChannelPlanForModel: %v", err)
			}
			if !plan.FallbackEnabled || len(plan.Channels) != 2 {
				t.Fatalf("logical fallback plan = %+v", plan)
			}
			if plan.Channels[0].ID != idByName["fallback-primary"] || plan.Channels[1].ID != idByName["fallback-candidate"] {
				t.Fatalf("fallback pool IDs = %q %q", plan.Channels[0].ID, plan.Channels[1].ID)
			}
			continue
		}

		channel, err := manager.SelectChannelForModel(context.Background(), catalogID)
		if err != nil {
			t.Fatalf("%s SelectChannelForModel: %v", tc.display, err)
		}
		if channel.ID != catalogID || channel.Name != tc.display || channel.Model != tc.providerID {
			t.Fatalf("%s runtime identity = id=%q name=%q model=%q", tc.display, channel.ID, channel.Name, channel.Model)
		}
		if channel.CredentialSource != tc.source || channel.BaseURL != tc.baseURL || channel.OpenAIEndpoint != tc.endpoint {
			t.Fatalf("%s runtime target source=%q base=%q endpoint=%q", tc.display, channel.CredentialSource, channel.BaseURL, channel.OpenAIEndpoint)
		}
		if channel.ReasoningEffort != "medium" {
			t.Fatalf("%s runtime thinking = %q, want medium", tc.display, channel.ReasoningEffort)
		}
		if tc.source != "static" && channel.APIKey != "" {
			t.Fatalf("%s managed catalog leaked runtime key %q", tc.display, channel.APIKey)
		}
	}
	if cliByName["fallback-primary [BYOK]"] == nil || cliByName["fallback-candidate [BYOK]"] == nil {
		t.Fatal("fallback pool restriction changed: physical channels missing from CLI catalog")
	}

	_, err = manager.SelectChannelForModel(context.Background(), "stale-catalog-id")
	if !errors.Is(err, legacyruntime.ErrChannelNotAvailable) {
		t.Fatalf("stale catalog ID error = %v, want ErrChannelNotAvailable", err)
	}
	if err := modeladapter.NewFallbackAwareRouter(router, manager).Stream(context.Background(), modeladapter.StreamRequest{ModelID: "stale-catalog-id"}, func(modeladapter.ModelEvent) error { return nil }); !errors.Is(err, legacyruntime.ErrChannelNotAvailable) {
		t.Fatalf("stale Stream error = %v, want ErrChannelNotAvailable", err)
	}
	if got := creds.resolveCount(); got != 0 {
		t.Fatalf("stale ID or catalog resolved managed credentials %d times", got)
	}
}

func defaultAvailableThinkingVariant(model *aiserverv1.AvailableModelsResponse_AvailableModel) string {
	for _, variant := range model.GetVariants() {
		if variant.GetIsDefaultNonMaxConfig() {
			return variant.GetVariantStringRepresentation()
		}
	}
	return ""
}

func cliCatalogStaticAdapter(display, baseURL, apiKey, modelID string, sort int) serverconfig.ModelAdapterConfig {
	return serverconfig.ModelAdapterConfig{
		Sort:            sort,
		DisplayName:     display,
		Type:            "openai",
		BaseURL:         baseURL,
		APIKey:          apiKey,
		TooltipData:     display,
		ModelID:         modelID,
		ReasoningEffort: "medium",
		OpenAIEndpoint:  "/v1/chat/completions",
	}
}

func cliCatalogManagedAdapter(display, source, modelID string, sort int) serverconfig.ModelAdapterConfig {
	return serverconfig.ModelAdapterConfig{
		Sort:             sort,
		DisplayName:      display,
		Type:             "openai",
		BaseURL:          "https://ignored.example/v1",
		CredentialSource: source,
		TooltipData:      display,
		ModelID:          modelID,
		ReasoningEffort:  "medium",
		OpenAIEndpoint:   "/v1/chat/completions",
	}
}

type cliCatalogAtomicString struct {
	mu    sync.Mutex
	value string
}

func (box *cliCatalogAtomicString) Store(value string) {
	box.mu.Lock()
	box.value = value
	box.mu.Unlock()
}

func (box *cliCatalogAtomicString) Load() string {
	box.mu.Lock()
	defer box.mu.Unlock()
	return box.value
}

type cliCatalogAtomicInt struct {
	mu    sync.Mutex
	value int
}

func (box *cliCatalogAtomicInt) Add(delta int) {
	box.mu.Lock()
	box.value += delta
	box.mu.Unlock()
}

func (box *cliCatalogAtomicInt) Load() int {
	box.mu.Lock()
	defer box.mu.Unlock()
	return box.value
}

func newCLICatalogAuthRecorder() (*cliCatalogAtomicString, *cliCatalogAtomicInt) {
	return &cliCatalogAtomicString{}, &cliCatalogAtomicInt{}
}

func cliCatalogOpenAIHandler(t *testing.T, auths *cliCatalogAtomicString, hits *cliCatalogAtomicInt, sentinel string) http.HandlerFunc {
	t.Helper()
	return func(writer http.ResponseWriter, request *http.Request) {
		hits.Add(1)
		token := strings.TrimPrefix(request.Header.Get("Authorization"), "Bearer ")
		auths.Store(token)
		if token == sentinel || token == "" {
			t.Errorf("provider received catalog sentinel or empty key %q", token)
			writer.WriteHeader(http.StatusUnauthorized)
			return
		}
		writer.Header().Set("Content-Type", "text/event-stream")
		_, _ = fmt.Fprintf(writer, "data: {\"choices\":[{\"delta\":{\"content\":%q},\"finish_reason\":\"stop\"}]}\n\n", "ok")
		_, _ = io.WriteString(writer, "data: [DONE]\n\n")
	}
}

type cliCatalogCredentialStub struct {
	mu        sync.Mutex
	token     string
	accountID string
	resolveN  int
	sources   []subscriptionauth.CredentialSource
}

func (stub *cliCatalogCredentialStub) resolveCount() int {
	stub.mu.Lock()
	defer stub.mu.Unlock()
	return stub.resolveN
}

func (stub *cliCatalogCredentialStub) resolvedSources() []subscriptionauth.CredentialSource {
	stub.mu.Lock()
	defer stub.mu.Unlock()
	out := make([]subscriptionauth.CredentialSource, len(stub.sources))
	copy(out, stub.sources)
	return out
}

func (stub *cliCatalogCredentialStub) Resolve(_ context.Context, source subscriptionauth.CredentialSource) (subscriptionauth.Credential, error) {
	stub.mu.Lock()
	stub.resolveN++
	stub.sources = append(stub.sources, source)
	stub.mu.Unlock()
	return subscriptionauth.Credential{Provider: subscriptionauth.ProviderCodex, AccountID: stub.accountID, AccessToken: stub.token}, nil
}

func (stub *cliCatalogCredentialStub) ResolveAfterUnauthorized(context.Context, subscriptionauth.CredentialSource, string) (subscriptionauth.Credential, error) {
	return subscriptionauth.Credential{Provider: subscriptionauth.ProviderCodex, AccountID: stub.accountID, AccessToken: stub.token}, nil
}

func (stub *cliCatalogCredentialStub) MarkQuotaExhausted(context.Context, string) error {
	return nil
}

func (stub *cliCatalogCredentialStub) RefreshUsage(context.Context, subscriptionauth.ProviderKind) (subscriptionauth.UsageSnapshot, error) {
	return subscriptionauth.UsageSnapshot{}, nil
}

func duoAgentBidiRunBody(t *testing.T, requestID, modelID string) []byte {
	t.Helper()
	run := &agentv1.AgentRunRequest{}
	if strings.TrimSpace(modelID) != "" {
		run.RequestedModel = &agentv1.RequestedModel{ModelId: modelID}
	}
	return duoAgentMarshalBidi(t, requestID, &agentv1.AgentClientMessage{
		Message: &agentv1.AgentClientMessage_RunRequest{RunRequest: run},
	})
}

func duoAgentBidiFollowupBody(t *testing.T, requestID string) []byte {
	t.Helper()
	return duoAgentMarshalBidi(t, requestID, &agentv1.AgentClientMessage{})
}

func duoAgentMarshalBidi(t *testing.T, requestID string, message *agentv1.AgentClientMessage) []byte {
	t.Helper()
	encoded, err := proto.Marshal(message)
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
	return duoAgentConnectEnvelope(payload)
}

func duoAgentRunSSEBody(t *testing.T, requestID string) []byte {
	t.Helper()
	payload, err := proto.Marshal(&aiserverv1.BidiRequestId{RequestId: requestID})
	if err != nil {
		t.Fatal(err)
	}
	return duoAgentConnectEnvelope(payload)
}

func duoAgentConnectEnvelope(payload []byte) []byte {
	envelope := make([]byte, 5+len(payload))
	binary.BigEndian.PutUint32(envelope[1:5], uint32(len(payload)))
	copy(envelope[5:], payload)
	return envelope
}
