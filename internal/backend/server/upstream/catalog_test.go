package upstream

import (
	"bytes"
	"compress/gzip"
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"cursor/gen/agentv1"
	"cursor/gen/aiserverv1"
	"cursor/internal/backend/server"
	legacyruntime "cursor/internal/runtime"

	"google.golang.org/protobuf/encoding/protowire"
	"google.golang.org/protobuf/proto"
)

const (
	catalogTestOfficialModel   = "official-opus"
	catalogTestOfficialDefault = "official-default"
	catalogTestUnknownToken    = "keep-unknown-catalog-field"
	catalogTestLocalChannel    = "abcdef0123456789"
	catalogTestProviderSecret  = "sk-secret-should-not-leak"
	catalogTestProviderURL     = "https://provider.example/v1"
	catalogTestInboundAuth     = "Bearer real-cursor-token"
)

type stubModelAdapters struct {
	adapters []legacyruntime.ModelAdapterConfig
}

func (stub stubModelAdapters) ResolveModelAdapters(context.Context) ([]legacyruntime.ModelAdapterConfig, error) {
	return stub.adapters, nil
}

func TestMergedAvailableModelsAppendsLocalAndPreservesUnknownFields(t *testing.T) {
	var sawAuth string
	official := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		sawAuth = request.Header.Get("Authorization")
		if request.Header.Get(HeaderRawServerURL) != "" {
			t.Errorf("official catalog request leaked %s", HeaderRawServerURL)
		}
		writer.Header().Set("content-type", "application/proto")
		_, _ = writer.Write(officialAvailableModelsFixture(t))
	}))
	defer official.Close()

	recorder := invokeMergedCatalog(t, catalogInvokeOptions{
		path:          "/aiserver.v1.AiService/AvailableModels",
		protoType:     "aiserver.v1.AvailableModelsResponse",
		builder:       AvailableModelsMockBuilder,
		officialURL:   official.URL + "/aiserver.v1.AiService/AvailableModels",
		client:        official.Client(),
		authorization: catalogTestInboundAuth,
	})
	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d", recorder.Code)
	}
	if sawAuth != catalogTestInboundAuth {
		t.Fatalf("official authorization = %q, want inbound token", sawAuth)
	}

	response := &aiserverv1.AvailableModelsResponse{}
	if err := proto.Unmarshal(recorder.Body.Bytes(), response); err != nil {
		t.Fatalf("unmarshal merged catalog: %v", err)
	}
	if got := response.GetComposerModelConfig().GetDefaultModel(); got != catalogTestOfficialDefault {
		t.Fatalf("official default overwritten: %q", got)
	}
	if !catalogHasAvailableModel(response, catalogTestOfficialModel) {
		t.Fatal("official model missing from merged catalog")
	}
	if !catalogHasAvailableModel(response, catalogTestLocalChannel) {
		t.Fatal("local model was not appended")
	}
	if unknown := response.ProtoReflect().GetUnknown(); !bytes.Contains(unknown, []byte(catalogTestUnknownToken)) {
		t.Fatalf("unknown protobuf field discarded: %q", unknown)
	}
	assertCatalogHasNoSecrets(t, recorder.Body.Bytes())
}

func TestMergedUsableModelsAppendsLocalWithoutProviderKey(t *testing.T) {
	official := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.Header.Get("Authorization") != catalogTestInboundAuth {
			t.Errorf("authorization = %q", request.Header.Get("Authorization"))
		}
		body, err := proto.Marshal(&agentv1.GetUsableModelsResponse{
			Models: []*agentv1.ModelDetails{{
				ModelId:        catalogTestOfficialModel,
				DisplayModelId: catalogTestOfficialModel,
				DisplayName:    "Official Opus",
			}},
		})
		if err != nil {
			t.Fatalf("marshal official usable models: %v", err)
		}
		writer.Header().Set("content-type", "application/proto")
		_, _ = writer.Write(body)
	}))
	defer official.Close()

	recorder := invokeMergedCatalog(t, catalogInvokeOptions{
		path:          "/aiserver.v1.AiService/GetUsableModels",
		protoType:     "aiserver.v1.GetUsableModelsResponse",
		builder:       UsableModelsMockBuilder,
		officialURL:   official.URL + "/aiserver.v1.AiService/GetUsableModels",
		client:        official.Client(),
		authorization: catalogTestInboundAuth,
	})
	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d", recorder.Code)
	}
	response := &agentv1.GetUsableModelsResponse{}
	if err := proto.Unmarshal(recorder.Body.Bytes(), response); err != nil {
		t.Fatalf("unmarshal usable models: %v", err)
	}
	if len(response.GetModels()) != 2 {
		t.Fatalf("model count = %d, want 2", len(response.GetModels()))
	}
	if response.GetModels()[0].GetModelId() != catalogTestOfficialModel {
		t.Fatalf("first model = %q", response.GetModels()[0].GetModelId())
	}
	local := response.GetModels()[1]
	if local.GetModelId() != catalogTestLocalChannel {
		t.Fatalf("local model = %q", local.GetModelId())
	}
	if local.GetApiKeyCredentials() != nil {
		t.Fatalf("local usable model leaked credentials: %#v", local.GetApiKeyCredentials())
	}
	assertCatalogHasNoSecrets(t, recorder.Body.Bytes())
}

func TestMergedCatalogFallsBackToLocalOnOfficialError(t *testing.T) {
	official := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		writer.WriteHeader(http.StatusUnauthorized)
	}))
	defer official.Close()

	recorder := invokeMergedCatalog(t, catalogInvokeOptions{
		path:          "/aiserver.v1.AiService/AvailableModels",
		protoType:     "aiserver.v1.AvailableModelsResponse",
		builder:       AvailableModelsMockBuilder,
		officialURL:   official.URL + "/aiserver.v1.AiService/AvailableModels",
		client:        official.Client(),
		authorization: catalogTestInboundAuth,
	})
	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d", recorder.Code)
	}
	response := &aiserverv1.AvailableModelsResponse{}
	if err := proto.Unmarshal(recorder.Body.Bytes(), response); err != nil {
		t.Fatalf("unmarshal local fallback: %v", err)
	}
	if catalogHasAvailableModel(response, catalogTestOfficialModel) {
		t.Fatal("official model appeared after catalog failure")
	}
	if !catalogHasAvailableModel(response, catalogTestLocalChannel) {
		t.Fatal("local fallback catalog missing local model")
	}
	if got := response.GetComposerModelConfig().GetDefaultModel(); got != catalogTestLocalChannel {
		t.Fatalf("local fallback default = %q", got)
	}
	assertCatalogHasNoSecrets(t, recorder.Body.Bytes())
}

func TestMergedCatalogFallsBackWhenOfficialTargetMissing(t *testing.T) {
	recorder := invokeMergedCatalog(t, catalogInvokeOptions{
		path:          "/aiserver.v1.AiService/AvailableModels",
		protoType:     "aiserver.v1.AvailableModelsResponse",
		builder:       AvailableModelsMockBuilder,
		authorization: catalogTestInboundAuth,
	})
	response := &aiserverv1.AvailableModelsResponse{}
	if err := proto.Unmarshal(recorder.Body.Bytes(), response); err != nil {
		t.Fatalf("unmarshal local catalog: %v", err)
	}
	if catalogHasAvailableModel(response, catalogTestOfficialModel) {
		t.Fatal("official model appeared without official fetch")
	}
	if !catalogHasAvailableModel(response, catalogTestLocalChannel) {
		t.Fatal("local catalog missing local model")
	}
}

func TestMergedCatalogSkipsOfficialFetchForLocalRelayToken(t *testing.T) {
	fetched := false
	official := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		fetched = true
		writer.WriteHeader(http.StatusOK)
	}))
	defer official.Close()

	_ = invokeMergedCatalog(t, catalogInvokeOptions{
		path:          "/aiserver.v1.AiService/AvailableModels",
		protoType:     "aiserver.v1.AvailableModelsResponse",
		builder:       AvailableModelsMockBuilder,
		officialURL:   official.URL + "/aiserver.v1.AiService/AvailableModels",
		client:        official.Client(),
		authorization: "Bearer " + legacyruntime.LocalRelayToken,
	})
	if fetched {
		t.Fatal("official catalog was fetched with local relay token")
	}
}

func TestOfficialDefaultModelPassthroughKeepsOfficialDefault(t *testing.T) {
	official := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.Header.Get("Authorization") != catalogTestInboundAuth {
			t.Error("official default used a non-inbound authorization")
		}
		body, err := proto.Marshal(&aiserverv1.GetDefaultModelResponse{
			Model:         catalogTestOfficialDefault,
			ThinkingModel: catalogTestOfficialDefault,
		})
		if err != nil {
			t.Fatalf("marshal official default: %v", err)
		}
		writer.Header().Set("content-type", "application/proto")
		_, _ = writer.Write(body)
	}))
	defer official.Close()

	recorder, err := invokeOfficialDefaultModel(t, catalogInvokeOptions{
		path:          "/aiserver.v1.AiService/GetDefaultModel",
		protoType:     "aiserver.v1.GetDefaultModelResponse",
		officialURL:   official.URL + "/aiserver.v1.AiService/GetDefaultModel",
		client:        official.Client(),
		authorization: catalogTestInboundAuth,
	})
	if err != nil {
		t.Fatalf("official default: %v", err)
	}
	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d", recorder.Code)
	}
	response := &aiserverv1.GetDefaultModelResponse{}
	if err := proto.Unmarshal(recorder.Body.Bytes(), response); err != nil {
		t.Fatalf("unmarshal default model: %v", err)
	}
	if response.GetModel() != catalogTestOfficialDefault || response.GetThinkingModel() != catalogTestOfficialDefault {
		t.Fatalf("default = %q thinking = %q", response.GetModel(), response.GetThinkingModel())
	}
	if response.GetModel() == catalogTestLocalChannel {
		t.Fatal("official default projected the first local adapter")
	}
}

func TestOfficialDefaultModelFailClosedForLocalRelayToken(t *testing.T) {
	fetched := false
	official := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		fetched = true
	}))
	defer official.Close()
	_, err := invokeOfficialDefaultModel(t, catalogInvokeOptions{
		path:          "/aiserver.v1.AiService/GetDefaultModel",
		protoType:     "aiserver.v1.GetDefaultModelResponse",
		officialURL:   official.URL + "/aiserver.v1.AiService/GetDefaultModel",
		client:        official.Client(),
		authorization: "Bearer " + legacyruntime.LocalRelayToken,
	})
	if err == nil {
		t.Fatal("local relay default model returned success")
	}
	if fetched {
		t.Fatal("official default was fetched with local relay authorization")
	}
}

func TestOfficialDefaultModelFailClosedWhenOfficialUnavailable(t *testing.T) {
	official := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		writer.WriteHeader(http.StatusUnauthorized)
	}))
	defer official.Close()
	_, err := invokeOfficialDefaultModel(t, catalogInvokeOptions{
		path:          "/aiserver.v1.AiService/GetDefaultModel",
		protoType:     "aiserver.v1.GetDefaultModelResponse",
		officialURL:   official.URL + "/aiserver.v1.AiService/GetDefaultModel",
		client:        official.Client(),
		authorization: catalogTestInboundAuth,
	})
	if err == nil {
		t.Fatal("missing official default returned the first local adapter")
	}
}

func TestMergedCatalogAcceptsConnectEnvelopeOfficialPayload(t *testing.T) {
	official := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		payload := officialAvailableModelsFixture(t)
		envelope := encodeConnectEnvelope(0, payload)
		writer.Header().Set("content-type", "application/connect+proto")
		_, _ = writer.Write(envelope)
	}))
	defer official.Close()

	recorder := invokeMergedCatalog(t, catalogInvokeOptions{
		path:          "/aiserver.v1.AiService/AvailableModels",
		protoType:     "aiserver.v1.AvailableModelsResponse",
		builder:       AvailableModelsMockBuilder,
		officialURL:   official.URL + "/aiserver.v1.AiService/AvailableModels",
		client:        official.Client(),
		authorization: catalogTestInboundAuth,
	})
	response := &aiserverv1.AvailableModelsResponse{}
	if err := proto.Unmarshal(recorder.Body.Bytes(), response); err != nil {
		t.Fatalf("unmarshal connect catalog: %v", err)
	}
	if !catalogHasAvailableModel(response, catalogTestOfficialModel) || !catalogHasAvailableModel(response, catalogTestLocalChannel) {
		t.Fatalf("connect envelope merge failed: %#v", response.GetModels())
	}
	if unknown := response.ProtoReflect().GetUnknown(); !bytes.Contains(unknown, []byte(catalogTestUnknownToken)) {
		t.Fatal("unknown field discarded for connect envelope")
	}
}

func TestMergedCatalogAcceptsConnectTrailerAfterMessage(t *testing.T) {
	official := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		payload := officialAvailableModelsFixture(t)
		body := append(encodeConnectEnvelope(0, payload), encodeConnectEnvelope(connectFlagEndStream, []byte(`{}`))...)
		writer.Header().Set("content-type", "application/connect+proto")
		_, _ = writer.Write(body)
	}))
	defer official.Close()

	recorder := invokeMergedCatalog(t, catalogInvokeOptions{
		path:          "/aiserver.v1.AiService/AvailableModels",
		protoType:     "aiserver.v1.AvailableModelsResponse",
		builder:       AvailableModelsMockBuilder,
		officialURL:   official.URL + "/aiserver.v1.AiService/AvailableModels",
		client:        official.Client(),
		authorization: catalogTestInboundAuth,
	})
	response := &aiserverv1.AvailableModelsResponse{}
	if err := proto.Unmarshal(recorder.Body.Bytes(), response); err != nil {
		t.Fatalf("unmarshal trailer catalog: %v", err)
	}
	if !catalogHasAvailableModel(response, catalogTestOfficialModel) || !catalogHasAvailableModel(response, catalogTestLocalChannel) {
		t.Fatal("trailer frame caused official catalog merge to fail")
	}
	if unknown := response.ProtoReflect().GetUnknown(); !bytes.Contains(unknown, []byte(catalogTestUnknownToken)) {
		t.Fatal("unknown field discarded after trailer frame")
	}
}

func TestMergedCatalogSeparatesHTTPGzipFromConnectEnvelopeGzip(t *testing.T) {
	payload := officialAvailableModelsFixture(t)

	t.Run("http gzip raw proto", func(t *testing.T) {
		got, err := extractCatalogProtoPayload("application/proto", "gzip", gzipCatalogBytes(t, payload))
		if err != nil {
			t.Fatal(err)
		}
		assertOfficialCatalogPayload(t, got)
	})
	t.Run("connect envelope gzip flag", func(t *testing.T) {
		envelope := encodeConnectEnvelope(connectFlagCompressed, gzipCatalogBytes(t, payload))
		got, err := extractCatalogProtoPayload("application/connect+proto", "", envelope)
		if err != nil {
			t.Fatal(err)
		}
		assertOfficialCatalogPayload(t, got)
	})
	t.Run("connect header is not whole-body gzip", func(t *testing.T) {
		envelope := append(encodeConnectEnvelope(0, payload), encodeConnectEnvelope(connectFlagEndStream, []byte(`{}`))...)
		got, err := extractCatalogProtoPayload("application/connect+proto", "", envelope)
		if err != nil {
			t.Fatal(err)
		}
		assertOfficialCatalogPayload(t, got)
		if _, err := extractCatalogProtoPayload("application/connect+proto", "gzip", envelope); err == nil {
			t.Fatal("HTTP gzip of an uncompressed connect envelope should fail closed")
		}
	})
}

func TestMergedCatalogDoesNotTreatConnectEncodingAsHTTPGzip(t *testing.T) {
	official := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		payload := officialAvailableModelsFixture(t)
		body := append(encodeConnectEnvelope(0, payload), encodeConnectEnvelope(connectFlagEndStream, []byte(`{}`))...)
		writer.Header().Set("content-type", "application/connect+proto")
		writer.Header().Set("Connect-Content-Encoding", "gzip")
		_, _ = writer.Write(body)
	}))
	defer official.Close()

	recorder := invokeMergedCatalog(t, catalogInvokeOptions{
		path:          "/aiserver.v1.AiService/AvailableModels",
		protoType:     "aiserver.v1.AvailableModelsResponse",
		builder:       AvailableModelsMockBuilder,
		officialURL:   official.URL + "/aiserver.v1.AiService/AvailableModels",
		client:        official.Client(),
		authorization: catalogTestInboundAuth,
	})
	response := &aiserverv1.AvailableModelsResponse{}
	if err := proto.Unmarshal(recorder.Body.Bytes(), response); err != nil {
		t.Fatalf("unmarshal after connect-content-encoding: %v", err)
	}
	if !catalogHasAvailableModel(response, catalogTestOfficialModel) {
		t.Fatal("Connect-Content-Encoding gzip was treated as whole-body gzip")
	}
	if unknown := response.ProtoReflect().GetUnknown(); !bytes.Contains(unknown, []byte(catalogTestUnknownToken)) {
		t.Fatal("unknown field discarded")
	}
}

func TestMergedCatalogReturnsConnectEnvelopeForInboundConnect(t *testing.T) {
	official := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		writer.Header().Set("content-type", "application/proto")
		_, _ = writer.Write(officialAvailableModelsFixture(t))
	}))
	defer official.Close()

	recorder := invokeMergedCatalog(t, catalogInvokeOptions{
		path:               "/aiserver.v1.AiService/AvailableModels",
		protoType:          "aiserver.v1.AvailableModelsResponse",
		builder:            AvailableModelsMockBuilder,
		officialURL:        official.URL + "/aiserver.v1.AiService/AvailableModels",
		client:             official.Client(),
		authorization:      catalogTestInboundAuth,
		inboundContentType: "application/connect+proto",
	})
	if got := recorder.Header().Get("content-type"); got != "application/connect+proto" {
		t.Fatalf("content-type = %q", got)
	}
	payload, err := extractCatalogProtoPayload(recorder.Header().Get("content-type"), "", recorder.Body.Bytes())
	if err != nil {
		t.Fatalf("decode inbound connect response: %v", err)
	}
	response := &aiserverv1.AvailableModelsResponse{}
	if err := proto.Unmarshal(payload, response); err != nil {
		t.Fatalf("unmarshal connect response payload: %v", err)
	}
	if !catalogHasAvailableModel(response, catalogTestOfficialModel) || !catalogHasAvailableModel(response, catalogTestLocalChannel) {
		t.Fatal("connect inbound response missing models")
	}
	if unknown := response.ProtoReflect().GetUnknown(); !bytes.Contains(unknown, []byte(catalogTestUnknownToken)) {
		t.Fatal("unknown field discarded in connect inbound response")
	}
}

type catalogInvokeOptions struct {
	path               string
	protoType          string
	builder            func(*RequestContext) (map[string]any, error)
	officialURL        string
	client             HTTPClient
	authorization      string
	inboundContentType string
}

func invokeMergedCatalog(t *testing.T, options catalogInvokeOptions) *httptest.ResponseRecorder {
	t.Helper()
	contentType := options.inboundContentType
	if contentType == "" {
		contentType = "application/proto"
	}
	request := httptest.NewRequest(http.MethodPost, "http://backend.local"+options.path, bytes.NewReader(nil))
	request.Header.Set("Authorization", options.authorization)
	request.Header.Set("content-type", contentType)
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
	reqCtx := &RequestContext{
		ResponseWriter: recorder,
		Request:        request,
		StartedAt:      time.Now(),
		RawURL:         strings.TrimSpace(request.Header.Get(HeaderRawServerURL)),
		TargetURL:      target,
		Method:         http.MethodPost,
		Headers:        request.Header.Clone(),
		ContentType:    contentType,
		Mode:           server.ModeLocal,
		Deps: &Dependencies{
			SystemSettingService: stubModelAdapters{adapters: []legacyruntime.ModelAdapterConfig{{
				ID:          catalogTestLocalChannel,
				DisplayName: "Local Grok",
				ModelID:     "grok-3",
				Type:        "openai",
				APIKey:      catalogTestProviderSecret,
				BaseURL:     catalogTestProviderURL,
			}}},
			HTTPClient: options.client,
		},
	}
	if err := handleMergedCatalog(reqCtx, &Route{
		Name:               "catalog_test",
		StatusCode:         http.StatusOK,
		MockProtoType:      options.protoType,
		MockPayloadBuilder: options.builder,
	}); err != nil {
		t.Fatalf("handleMergedCatalog: %v", err)
	}
	return recorder
}

func invokeOfficialDefaultModel(t *testing.T, options catalogInvokeOptions) (*httptest.ResponseRecorder, error) {
	t.Helper()
	contentType := options.inboundContentType
	if contentType == "" {
		contentType = "application/proto"
	}
	request := httptest.NewRequest(http.MethodPost, "http://backend.local"+options.path, bytes.NewReader(nil))
	request.Header.Set("Authorization", options.authorization)
	request.Header.Set("content-type", contentType)
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
	reqCtx := &RequestContext{
		ResponseWriter: recorder,
		Request:        request,
		StartedAt:      time.Now(),
		RawURL:         strings.TrimSpace(request.Header.Get(HeaderRawServerURL)),
		TargetURL:      target,
		Method:         http.MethodPost,
		Headers:        request.Header.Clone(),
		ContentType:    contentType,
		Mode:           server.ModeLocal,
		Deps: &Dependencies{
			SystemSettingService: stubModelAdapters{adapters: []legacyruntime.ModelAdapterConfig{{
				ID:          catalogTestLocalChannel,
				DisplayName: "Local Grok",
				ModelID:     "grok-3",
				Type:        "openai",
				APIKey:      catalogTestProviderSecret,
				BaseURL:     catalogTestProviderURL,
			}}},
			HTTPClient: options.client,
		},
	}
	err := handleOfficialDefaultModel(reqCtx, &Route{
		Name:          "default_model_test",
		StatusCode:    http.StatusOK,
		MockProtoType: options.protoType,
	})
	return recorder, err
}

func officialAvailableModelsFixture(t *testing.T) []byte {
	t.Helper()
	serverName := catalogTestOfficialModel
	body, err := proto.Marshal(&aiserverv1.AvailableModelsResponse{
		Models: []*aiserverv1.AvailableModelsResponse_AvailableModel{{
			Name:            catalogTestOfficialModel,
			ServerModelName: &serverName,
		}},
		ComposerModelConfig: &aiserverv1.AvailableModelsResponse_FeatureModelConfig{
			DefaultModel: catalogTestOfficialDefault,
		},
	})
	if err != nil {
		t.Fatalf("marshal official catalog: %v", err)
	}
	body = protowire.AppendTag(body, 999, protowire.BytesType)
	body = protowire.AppendString(body, catalogTestUnknownToken)
	return body
}

func catalogHasAvailableModel(response *aiserverv1.AvailableModelsResponse, name string) bool {
	for _, model := range response.GetModels() {
		if model != nil && model.GetName() == name {
			return true
		}
	}
	return false
}

func assertCatalogHasNoSecrets(t *testing.T, body []byte) {
	t.Helper()
	text := string(body)
	for _, secret := range []string{catalogTestProviderSecret, catalogTestProviderURL, "grok-3"} {
		if strings.Contains(text, secret) {
			t.Fatalf("catalog leaked %q", secret)
		}
	}
}

func gzipCatalogBytes(t *testing.T, body []byte) []byte {
	t.Helper()
	var buffer bytes.Buffer
	writer := gzip.NewWriter(&buffer)
	if _, err := writer.Write(body); err != nil {
		t.Fatal(err)
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	return buffer.Bytes()
}

func assertOfficialCatalogPayload(t *testing.T, payload []byte) {
	t.Helper()
	response := &aiserverv1.AvailableModelsResponse{}
	if err := proto.Unmarshal(payload, response); err != nil {
		t.Fatalf("unmarshal fixture payload: %v", err)
	}
	if !catalogHasAvailableModel(response, catalogTestOfficialModel) {
		t.Fatal("official model missing from fixture payload")
	}
	if unknown := response.ProtoReflect().GetUnknown(); !bytes.Contains(unknown, []byte(catalogTestUnknownToken)) {
		t.Fatal("unknown field discarded from fixture payload")
	}
}

func TestFetchUpstreamDoesNotWriteClientResponse(t *testing.T) {
	official := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		_, _ = io.ReadAll(request.Body)
		writer.WriteHeader(http.StatusCreated)
		_, _ = writer.Write([]byte("fixture-body"))
	}))
	defer official.Close()
	target, err := url.Parse(official.URL + "/aiserver.v1.AiService/AvailableModels")
	if err != nil {
		t.Fatal(err)
	}
	request := httptest.NewRequest(http.MethodPost, "http://backend.local/aiserver.v1.AiService/AvailableModels", bytes.NewReader([]byte("req")))
	recorder := httptest.NewRecorder()
	fetched, err := FetchUpstream(&RequestContext{
		ResponseWriter: recorder,
		Request:        request,
		TargetURL:      target,
		Method:         http.MethodPost,
		Headers:        request.Header.Clone(),
		RequestBody:    []byte("req"),
		Mode:           server.ModeLocal,
		Deps:           &Dependencies{HTTPClient: official.Client()},
	}, ForwardOptions{PreserveInboundIdentity: true})
	if err != nil {
		t.Fatal(err)
	}
	if fetched.StatusCode != http.StatusCreated || string(fetched.Body) != "fixture-body" {
		t.Fatalf("fetched = %+v", fetched)
	}
	if recorder.Body.Len() != 0 || recorder.Header().Get("content-type") != "" {
		t.Fatalf("client response was written: status=%d body=%q headers=%v", recorder.Code, recorder.Body.String(), recorder.Header())
	}
}
