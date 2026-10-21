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
	catalogTestOfficialHTML    = `Official Opus <span class="keep">brain</span>`
)

type stubModelAdapters struct {
	adapters []legacyruntime.ModelAdapterConfig
}

func (stub stubModelAdapters) ResolveModelAdapters(context.Context) ([]legacyruntime.ModelAdapterConfig, error) {
	return stub.adapters, nil
}

func TestHasOfficialIdentity(t *testing.T) {
	t.Parallel()
	local := "Bearer " + legacyruntime.LocalRelayToken
	cases := []struct {
		name   string
		header http.Header
		want   bool
	}{
		{name: "nil", want: false},
		{name: "absent", header: http.Header{}, want: false},
		{name: "empty bearer", header: http.Header{"Authorization": []string{"Bearer "}}, want: false},
		{name: "non bearer", header: http.Header{"Authorization": []string{"real-cursor-token"}}, want: false},
		{name: "local relay", header: http.Header{"Authorization": []string{local}}, want: false},
		{name: "official", header: http.Header{"Authorization": []string{catalogTestInboundAuth}}, want: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := hasOfficialIdentity(tc.header); got != tc.want {
				t.Fatalf("hasOfficialIdentity = %v, want %v", got, tc.want)
			}
		})
	}
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

func TestMergedCatalogLabelsDisplayFieldsOnlyAndKeepsIDs(t *testing.T) {
	official := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
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
	response := &aiserverv1.AvailableModelsResponse{}
	if err := proto.Unmarshal(recorder.Body.Bytes(), response); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	officialModel := catalogAvailableModel(response, catalogTestOfficialModel)
	localModel := catalogAvailableModel(response, catalogTestLocalChannel)
	if officialModel == nil || localModel == nil {
		t.Fatalf("models missing official=%v local=%v", officialModel != nil, localModel != nil)
	}
	if officialModel.GetName() != catalogTestOfficialModel || officialModel.GetServerModelName() != catalogTestOfficialModel {
		t.Fatalf("official IDs mutated: name=%q server=%q", officialModel.GetName(), officialModel.GetServerModelName())
	}
	if localModel.GetName() != catalogTestLocalChannel || localModel.GetServerModelName() != catalogTestLocalChannel {
		t.Fatalf("local IDs mutated: name=%q server=%q", localModel.GetName(), localModel.GetServerModelName())
	}
	if !strings.Contains(officialModel.GetClientDisplayName(), catalogDisplaySuffixOfficial) || !strings.Contains(officialModel.GetInputboxShortModelName(), catalogDisplaySuffixOfficial) {
		t.Fatalf("official display missing suffix: %#v", officialModel)
	}
	if strings.Contains(officialModel.GetName(), catalogDisplaySuffixOfficial) {
		t.Fatal("official name received a display suffix")
	}
	if !strings.Contains(localModel.GetClientDisplayName(), catalogDisplaySuffixBYOK) || !strings.Contains(localModel.GetInputboxShortModelName(), catalogDisplaySuffixBYOK) {
		t.Fatalf("local display missing suffix: %#v", localModel)
	}
	if strings.Count(localModel.GetClientDisplayName(), catalogDisplaySuffixBYOK) != 1 {
		t.Fatalf("repeated BYOK suffix: %q", localModel.GetClientDisplayName())
	}
	if len(officialModel.GetVariants()) == 0 || !strings.Contains(officialModel.GetVariants()[0].GetDisplayName(), catalogTestOfficialHTML) {
		t.Fatalf("official variant HTML lost: %#v", officialModel.GetVariants())
	}
	if !strings.Contains(officialModel.GetVariants()[0].GetDisplayName(), catalogDisplaySuffixOfficial) {
		t.Fatal("official variant display missing suffix")
	}
	if got := officialModel.GetVariants()[0].GetVariantStringRepresentation(); got != "official-opus:high" {
		t.Fatalf("official variant repr mutated: %q", got)
	}
	if len(localModel.GetVariants()) == 0 {
		t.Fatal("local variants missing")
	}
	localVariant := localModel.GetVariants()[0].GetDisplayName()
	if !strings.Contains(localVariant, catalogDisplaySuffixBYOK) {
		t.Fatalf("local variant missing suffix: %q", localVariant)
	}
	if span := strings.Index(localVariant, "<span"); span >= 0 {
		if strings.Index(localVariant, catalogDisplaySuffixBYOK) > span {
			t.Fatalf("local variant suffix applied after HTML: %q", localVariant)
		}
	}
	if strings.Contains(localModel.GetVariants()[0].GetVariantStringRepresentation(), catalogDisplaySuffixBYOK) {
		t.Fatal("local variant repr received a display suffix")
	}
}

func TestMergedCatalogCollisionLocalReplacesOfficialThenAppendsLocalOrder(t *testing.T) {
	official := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		serverName := catalogTestLocalChannel
		body, err := proto.Marshal(&aiserverv1.AvailableModelsResponse{
			Models: []*aiserverv1.AvailableModelsResponse_AvailableModel{
				{Name: catalogTestLocalChannel, ServerModelName: proto.String(serverName), ClientDisplayName: proto.String("Official Same ID")},
				{Name: catalogTestOfficialModel, ClientDisplayName: proto.String("Official Opus")},
			},
		})
		if err != nil {
			t.Fatalf("marshal collision fixture: %v", err)
		}
		writer.Header().Set("content-type", "application/proto")
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
		t.Fatalf("unmarshal: %v", err)
	}
	if len(response.GetModels()) != 2 {
		t.Fatalf("model count = %d, want 2", len(response.GetModels()))
	}
	if response.GetModels()[0].GetName() != catalogTestOfficialModel {
		t.Fatalf("first model = %q, want remaining official order", response.GetModels()[0].GetName())
	}
	if response.GetModels()[1].GetName() != catalogTestLocalChannel {
		t.Fatalf("second model = %q, want local replacement", response.GetModels()[1].GetName())
	}
	if response.GetModels()[1].GetClientDisplayName() == "Official Same ID" {
		t.Fatal("colliding official display was kept")
	}
	if !strings.Contains(response.GetModels()[1].GetClientDisplayName(), catalogDisplaySuffixBYOK) {
		t.Fatalf("replaced local display = %q", response.GetModels()[1].GetClientDisplayName())
	}
}

func TestMergedUsableModelsKeepsLocalSentinelAndOfficialCredentials(t *testing.T) {
	official := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.Header.Get("Authorization") != catalogTestInboundAuth {
			t.Errorf("authorization = %q", request.Header.Get("Authorization"))
		}
		body, err := proto.Marshal(&agentv1.GetUsableModelsResponse{
			Models: []*agentv1.ModelDetails{{
				ModelId:        catalogTestOfficialModel,
				DisplayModelId: catalogTestOfficialModel,
				DisplayName:    "Official Opus",
				Credentials: &agentv1.ModelDetails_ApiKeyCredentials{
					ApiKeyCredentials: &agentv1.ApiKeyCredentials{ApiKey: "official-secret"},
				},
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
	officialModel := response.GetModels()[0]
	if officialModel.GetModelId() != catalogTestOfficialModel || officialModel.GetDisplayModelId() != catalogTestOfficialModel {
		t.Fatalf("official IDs mutated: %#v", officialModel)
	}
	if !strings.Contains(officialModel.GetDisplayName(), catalogDisplaySuffixOfficial) || !strings.Contains(officialModel.GetDisplayNameShort(), catalogDisplaySuffixOfficial) {
		t.Fatalf("official CLI display missing suffix: %#v", officialModel)
	}
	if officialModel.GetApiKeyCredentials() == nil || officialModel.GetApiKeyCredentials().GetApiKey() != "official-secret" {
		t.Fatalf("official credentials mutated: %#v", officialModel.GetApiKeyCredentials())
	}
	local := response.GetModels()[1]
	if local.GetModelId() != catalogTestLocalChannel || local.GetDisplayModelId() != catalogTestLocalChannel {
		t.Fatalf("local IDs mutated: %#v", local)
	}
	if !strings.Contains(local.GetDisplayName(), catalogDisplaySuffixBYOK) {
		t.Fatalf("local CLI display missing suffix: %q", local.GetDisplayName())
	}
	if local.GetApiKeyCredentials() == nil || local.GetApiKeyCredentials().GetApiKey() != localCLICatalogAPIKey {
		t.Fatalf("local usable model lost sentinel: %#v", local.GetApiKeyCredentials())
	}
	if local.GetApiKeyCredentials().BaseUrl != nil {
		t.Fatalf("local usable model included baseUrl: %#v", local.GetApiKeyCredentials())
	}
	assertCatalogHasNoSecrets(t, recorder.Body.Bytes())
}

func TestMergedCatalogFallsBackToLocalOnOfficialErrorWithoutRecommendations(t *testing.T) {
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
	if response.GetComposerModelConfig() != nil || response.GetCmdKModelConfig() != nil || response.GetBackgroundComposerModelConfig() != nil ||
		response.GetPlanExecutionModelConfig() != nil || response.GetSpecModelConfig() != nil || response.GetDeepSearchModelConfig() != nil ||
		response.GetQuickAgentModelConfig() != nil || len(response.GetSubagentModelConfigs()) != 0 {
		t.Fatalf("fallback synthesized recommendations: %#v", response)
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
	if response.GetComposerModelConfig() != nil {
		t.Fatal("missing-target fallback kept local default recommendations")
	}
}

func TestMergedCatalogSkipsOfficialFetchWithoutOfficialIdentity(t *testing.T) {
	fetched := false
	official := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		fetched = true
		writer.WriteHeader(http.StatusOK)
	}))
	defer official.Close()

	for _, auth := range []string{"", "Bearer " + legacyruntime.LocalRelayToken} {
		fetched = false
		recorder := invokeMergedCatalog(t, catalogInvokeOptions{
			path:          "/aiserver.v1.AiService/AvailableModels",
			protoType:     "aiserver.v1.AvailableModelsResponse",
			builder:       AvailableModelsMockBuilder,
			officialURL:   official.URL + "/aiserver.v1.AiService/AvailableModels",
			client:        official.Client(),
			authorization: auth,
		})
		if fetched {
			t.Fatalf("official catalog was fetched with authorization %q", auth)
		}
		response := &aiserverv1.AvailableModelsResponse{}
		if err := proto.Unmarshal(recorder.Body.Bytes(), response); err != nil {
			t.Fatalf("unmarshal: %v", err)
		}
		if !catalogHasAvailableModel(response, catalogTestLocalChannel) {
			t.Fatal("pure local catalog missing local model")
		}
		if response.GetComposerModelConfig().GetDefaultModel() != catalogTestLocalChannel {
			t.Fatalf("pure local dropped recommendations: %#v", response.GetComposerModelConfig())
		}
		if !strings.Contains(catalogAvailableModel(response, catalogTestLocalChannel).GetClientDisplayName(), catalogDisplaySuffixBYOK) {
			t.Fatal("pure local catalog missing BYOK display label")
		}
		localModel := catalogAvailableModel(response, catalogTestLocalChannel)
		if len(localModel.GetVariants()) == 0 || !strings.Contains(localModel.GetVariants()[0].GetDisplayName(), catalogDisplaySuffixBYOK) {
			t.Fatalf("pure local variants missing BYOK: %#v", localModel.GetVariants())
		}
		if span := strings.Index(localModel.GetVariants()[0].GetDisplayName(), "<span"); span >= 0 {
			if strings.Index(localModel.GetVariants()[0].GetDisplayName(), catalogDisplaySuffixBYOK) > span {
				t.Fatalf("pure local variant suffix applied after HTML: %q", localModel.GetVariants()[0].GetDisplayName())
			}
		}
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

func TestOfficialDefaultModelUsesLocalBuilderWithoutOfficialIdentity(t *testing.T) {
	fetched := false
	official := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		fetched = true
	}))
	defer official.Close()
	recorder, err := invokeOfficialDefaultModel(t, catalogInvokeOptions{
		path:          "/aiserver.v1.AiService/GetDefaultModel",
		protoType:     "aiserver.v1.GetDefaultModelResponse",
		builder:       DefaultModelMockBuilder,
		officialURL:   official.URL + "/aiserver.v1.AiService/GetDefaultModel",
		client:        official.Client(),
		authorization: "Bearer " + legacyruntime.LocalRelayToken,
	})
	if err != nil {
		t.Fatalf("local default: %v", err)
	}
	if fetched {
		t.Fatal("official default was fetched with local relay authorization")
	}
	response := &aiserverv1.GetDefaultModelResponse{}
	if err := proto.Unmarshal(recorder.Body.Bytes(), response); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if response.GetModel() != catalogTestLocalChannel {
		t.Fatalf("local default = %q", response.GetModel())
	}
}

func TestPureLocalCLIDefaultLabelsBYOKAndKeepsSentinel(t *testing.T) {
	for _, auth := range []string{"", "Bearer " + legacyruntime.LocalRelayToken} {
		recorder, err := invokeOfficialDefaultModel(t, catalogInvokeOptions{
			path:          "/aiserver.v1.AiService/GetDefaultModelForCli",
			protoType:     "aiserver.v1.GetDefaultModelForCliResponse",
			builder:       DefaultModelForCliMockBuilder,
			authorization: auth,
		})
		if err != nil {
			t.Fatalf("auth %q local CLI default: %v", auth, err)
		}
		response := &agentv1.GetDefaultModelForCliResponse{}
		if err := proto.Unmarshal(recorder.Body.Bytes(), response); err != nil {
			t.Fatalf("unmarshal: %v", err)
		}
		model := response.GetModel()
		if model.GetModelId() != catalogTestLocalChannel || model.GetDisplayModelId() != catalogTestLocalChannel {
			t.Fatalf("CLI default IDs mutated: %#v", model)
		}
		if !strings.Contains(model.GetDisplayName(), catalogDisplaySuffixBYOK) || !strings.Contains(model.GetDisplayNameShort(), catalogDisplaySuffixBYOK) {
			t.Fatalf("CLI default display missing BYOK: name=%q short=%q", model.GetDisplayName(), model.GetDisplayNameShort())
		}
		if strings.Count(model.GetDisplayName(), catalogDisplaySuffixBYOK) != 1 {
			t.Fatalf("repeated BYOK suffix: %q", model.GetDisplayName())
		}
		if model.GetApiKeyCredentials() == nil || model.GetApiKeyCredentials().GetApiKey() != localCLICatalogAPIKey {
			t.Fatalf("CLI default lost sentinel: %#v", model.GetApiKeyCredentials())
		}
		if model.GetApiKeyCredentials().BaseUrl != nil {
			t.Fatalf("CLI default included baseUrl: %#v", model.GetApiKeyCredentials())
		}
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
		builder:       DefaultModelMockBuilder,
		officialURL:   official.URL + "/aiserver.v1.AiService/GetDefaultModel",
		client:        official.Client(),
		authorization: catalogTestInboundAuth,
	})
	if err == nil {
		t.Fatal("missing official default returned the first local adapter")
	}
}

func TestOfficialDefaultCLIModelLabelsDisplayAndKeepsCredentials(t *testing.T) {
	official := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		body, err := proto.Marshal(&agentv1.GetDefaultModelForCliResponse{
			Model: &agentv1.ModelDetails{
				ModelId:        catalogTestOfficialDefault,
				DisplayModelId: catalogTestOfficialDefault,
				DisplayName:    "Official Default",
				Credentials: &agentv1.ModelDetails_ApiKeyCredentials{
					ApiKeyCredentials: &agentv1.ApiKeyCredentials{ApiKey: "official-secret"},
				},
			},
		})
		if err != nil {
			t.Fatalf("marshal: %v", err)
		}
		writer.Header().Set("content-type", "application/proto")
		_, _ = writer.Write(body)
	}))
	defer official.Close()
	recorder, err := invokeOfficialDefaultModel(t, catalogInvokeOptions{
		path:          "/aiserver.v1.AiService/GetDefaultModelForCli",
		protoType:     "aiserver.v1.GetDefaultModelForCliResponse",
		builder:       DefaultModelForCliMockBuilder,
		officialURL:   official.URL + "/aiserver.v1.AiService/GetDefaultModelForCli",
		client:        official.Client(),
		authorization: catalogTestInboundAuth,
	})
	if err != nil {
		t.Fatalf("official CLI default: %v", err)
	}
	response := &agentv1.GetDefaultModelForCliResponse{}
	if err := proto.Unmarshal(recorder.Body.Bytes(), response); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	model := response.GetModel()
	if model.GetModelId() != catalogTestOfficialDefault || model.GetDisplayModelId() != catalogTestOfficialDefault {
		t.Fatalf("CLI default IDs mutated: %#v", model)
	}
	if !strings.Contains(model.GetDisplayName(), catalogDisplaySuffixOfficial) {
		t.Fatalf("CLI default display missing suffix: %q", model.GetDisplayName())
	}
	if model.GetApiKeyCredentials() == nil || model.GetApiKeyCredentials().GetApiKey() != "official-secret" {
		t.Fatalf("official CLI default credentials mutated: %#v", model.GetApiKeyCredentials())
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
	reqCtx, route, recorder := newCatalogTestContext(t, options)
	if err := handleMergedCatalog(reqCtx, route); err != nil {
		t.Fatalf("handleMergedCatalog: %v", err)
	}
	return recorder
}

func invokeOfficialDefaultModel(t *testing.T, options catalogInvokeOptions) (*httptest.ResponseRecorder, error) {
	t.Helper()
	reqCtx, route, recorder := newCatalogTestContext(t, options)
	err := handleOfficialDefaultModel(reqCtx, route)
	return recorder, err
}

func newCatalogTestContext(t *testing.T, options catalogInvokeOptions) (*RequestContext, *Route, *httptest.ResponseRecorder) {
	t.Helper()
	contentType := options.inboundContentType
	if contentType == "" {
		contentType = "application/proto"
	}
	request := httptest.NewRequest(http.MethodPost, "http://backend.local"+options.path, bytes.NewReader(nil))
	if options.authorization != "" {
		request.Header.Set("Authorization", options.authorization)
	}
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
	return reqCtx, &Route{
		Name:               "catalog_test",
		StatusCode:         http.StatusOK,
		MockProtoType:      options.protoType,
		MockPayloadBuilder: options.builder,
	}, recorder
}

func officialAvailableModelsFixture(t *testing.T) []byte {
	t.Helper()
	serverName := catalogTestOfficialModel
	body, err := proto.Marshal(&aiserverv1.AvailableModelsResponse{
		Models: []*aiserverv1.AvailableModelsResponse_AvailableModel{{
			Name:                   catalogTestOfficialModel,
			ServerModelName:        &serverName,
			ClientDisplayName:      proto.String("Official Opus"),
			InputboxShortModelName: proto.String("Opus"),
			Variants: []*aiserverv1.AvailableModelsResponse_ModelVariantConfig{{
				DisplayName:                 catalogTestOfficialHTML,
				DisplayNameOutsidePicker:    proto.String(catalogTestOfficialHTML),
				VariantStringRepresentation: proto.String("official-opus:high"),
				ParameterValues: []*aiserverv1.RequestedModel_ModelParameterValue{{
					Id:    "thinking_effort",
					Value: "high",
				}},
			}},
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
	return catalogAvailableModel(response, name) != nil
}

func catalogAvailableModel(response *aiserverv1.AvailableModelsResponse, name string) *aiserverv1.AvailableModelsResponse_AvailableModel {
	for _, model := range response.GetModels() {
		if model != nil && model.GetName() == name {
			return model
		}
	}
	return nil
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
