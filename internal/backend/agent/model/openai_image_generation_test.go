package modeladapter

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	legacyruntime "cursor/internal/runtime"
)

func generateImageToolJSON() json.RawMessage {
	return json.RawMessage(`{"name":"GenerateImage","description":"Generate or display an image","parameters":{"type":"object","properties":{}}}`)
}

func generateImageFunctionToolJSON() json.RawMessage {
	return json.RawMessage(`{"type":"function","function":{"name":"GenerateImage","parameters":{"type":"object","properties":{}}}}`)
}

func TestOpenAITextLooksLikeImageGenerationRequest(t *testing.T) {
	tests := []struct {
		name string
		text string
		want bool
	}{
		{name: "english_generate_image", text: "generate an image of a cat", want: true},
		{name: "english_create_picture", text: "create a picture of a mountain", want: true},
		{name: "english_draw_portrait", text: "draw a portrait of a fox", want: true},
		{name: "chinese_generate", text: "帮我生成一张图片", want: true},
		{name: "chinese_draw_poster", text: "画一张海报", want: true},
		{name: "chinese_create_avatar", text: "创建一个头像", want: true},
		{name: "chinese_paint_illustration", text: "请绘制一幅插画", want: true},
		{name: "chinese_long_visual_description", text: "请生成一幅充满丰富细节与柔和光影的赛博朋克城市夜景图片", want: true},
		{name: "generate_then_analyze_protocol", text: "generate an image of a cat, then analyze the protocol and MIME type", want: true},
		{name: "do_not_forget_to_generate", text: "do not forget to generate an image of a lake", want: true},
		{name: "especially_generate_chinese", text: "特别生成一张图片", want: true},
		{name: "tagged_current_user_request", text: "ignore history\n<current_user_request>\ngenerate an image of a sunset\n</current_user_request>", want: true},
		{name: "mention_image_only", text: "this image looks fine", want: false},
		{name: "past_tense_created_image", text: "the created image looks fine", want: false},
		{name: "past_tense_painted_portrait", text: "the painted portrait looks fine", want: false},
		{name: "third_person_generates_image", text: "this function generates an image response", want: false},
		{name: "mime_analysis", text: "what's the MIME type of this image", want: false},
		{name: "base64_analysis", text: "decode the base64 image payload", want: false},
		{name: "protocol_analysis", text: "explain the image generation protocol", want: false},
		{name: "result_analysis", text: "inspect the GenerateImage result", want: false},
		{name: "test_text", text: "write a test for image handling", want: false},
		{name: "create_test_for_image_handling", text: "create a test for image handling", want: false},
		{name: "create_testdata_for_image_handling", text: "create testdata for image handling", want: false},
		{name: "create_fixture_for_image_payload", text: "create a fixture for image payload parsing", want: false},
		{name: "create_image_loader", text: "create an image loader for the upload form", want: false},
		{name: "generate_image_response_code", text: "generate an image response object in the API client", want: false},
		{name: "discuss_how_to_generate", text: "explain how to generate an image with this API", want: false},
		{name: "chinese_discuss_how_to_generate", text: "分析如何生成图片并处理返回格式", want: false},
		{name: "create_image_for_test_report", text: "create an image for the test report", want: true},
		{name: "english_negated", text: "don't generate an image", want: false},
		{name: "english_not_to", text: "not to generate an image", want: false},
		{name: "english_asked_not_to", text: "I asked you not to generate an image", want: false},
		{name: "english_do_not_want", text: "do not want to generate an image", want: false},
		{name: "english_whenever_positive", text: "whenever you generate an image, save the result", want: true},
		{name: "english_dont_forget_positive", text: "don't forget to generate an image", want: true},
		{name: "english_do_not", text: "do not create a picture", want: false},
		{name: "never_draw", text: "never draw a portrait", want: false},
		{name: "chinese_negated", text: "不要生成图片", want: false},
		{name: "chinese_bie_negated", text: "请别生成图片", want: false},
		{name: "chinese_analysis", text: "分析这张图片的内容", want: false},
		{name: "chinese_draw_compound_frame", text: "画面很清晰", want: false},
		{name: "chinese_animation_compound", text: "动画里的图片", want: false},
		{name: "empty", text: "   ", want: false},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			text := test.text
			if strings.Contains(text, "<current_user_request>") {
				text = openAILatestUserRequestText(StreamRequest{Messages: []Message{{Role: "user", Content: test.text}}})
			}
			if got := openAITextLooksLikeImageGenerationRequest(text); got != test.want {
				t.Fatalf("openAITextLooksLikeImageGenerationRequest(%q) = %v, want %v", test.text, got, test.want)
			}
		})
	}
}

func TestShouldExposeOpenAIResponsesImageGenerationCapabilityMatrix(t *testing.T) {
	generateImageTools := []map[string]any{{"type": "function", "name": "GenerateImage"}}
	functionShapeTools := []map[string]any{{"function": map[string]any{"name": "GenerateImage"}}}
	otherTools := []map[string]any{{"type": "function", "name": "Read"}}
	generateReq := StreamRequest{
		OpenAIImageGenerationEnabled: true,
		Messages:                     []Message{{Role: "user", Content: "generate an image of a lake"}},
	}

	tests := []struct {
		name  string
		req   StreamRequest
		tools []map[string]any
		want  bool
	}{
		{
			name:  "enabled_tool_and_action",
			req:   generateReq,
			tools: generateImageTools,
			want:  true,
		},
		{
			name:  "function_shape_tool",
			req:   generateReq,
			tools: functionShapeTools,
			want:  true,
		},
		{
			name: "capability_false",
			req: StreamRequest{
				OpenAIImageGenerationEnabled: false,
				Messages:                     []Message{{Role: "user", Content: "generate an image of a lake"}},
			},
			tools: generateImageTools,
			want:  false,
		},
		{
			name:  "missing_canonical_tool",
			req:   generateReq,
			tools: otherTools,
			want:  false,
		},
		{
			name: "analysis_text",
			req: StreamRequest{
				OpenAIImageGenerationEnabled: true,
				Messages:                     []Message{{Role: "user", Content: "what's the MIME type of this image"}},
			},
			tools: generateImageTools,
			want:  false,
		},
		{
			name: "generate_then_analyze_still_exposes",
			req: StreamRequest{
				OpenAIImageGenerationEnabled: true,
				Messages:                     []Message{{Role: "user", Content: "generate an image of a lake, then write a test for the protocol"}},
			},
			tools: generateImageTools,
			want:  true,
		},
		{
			name: "latest_user_not_history",
			req: StreamRequest{
				OpenAIImageGenerationEnabled: true,
				Messages: []Message{
					{Role: "user", Content: "generate an image of a cat"},
					{Role: "assistant", Content: "ok"},
					{Role: "user", Content: "explain the image generation protocol"},
				},
			},
			tools: generateImageTools,
			want:  false,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := shouldExposeOpenAIResponsesImageGeneration(test.req, test.tools); got != test.want {
				t.Fatalf("shouldExpose = %v, want %v", got, test.want)
			}
		})
	}
}

func TestEnsureOpenAIResponsesImageGenerationToolDedup(t *testing.T) {
	existing := []map[string]any{{"type": "image_generation"}}
	got := ensureOpenAIResponsesImageGenerationTool(existing)
	if len(got) != 1 {
		t.Fatalf("dedup len = %d, want 1", len(got))
	}
	added := ensureOpenAIResponsesImageGenerationTool([]map[string]any{{"type": "function", "name": "GenerateImage"}})
	if countNativeImageGenerationToolsFromSlice(added) != 1 {
		t.Fatalf("added tools = %#v, want one native image_generation", added)
	}
}

func TestOpenAIResponsesInjectsNativeImageGenerationInRequestBody(t *testing.T) {
	knobs := map[string]any{}
	body := captureOpenAIResponsesRequestBody(t, StreamRequest{
		OpenAIImageGenerationEnabled: true,
		Messages:                     []Message{{Role: "user", Content: "generate an image of a red balloon"}},
		Tools:                        []json.RawMessage{generateImageToolJSON()},
		RequestKnobs:                 knobs,
	})
	if got := countNativeImageGenerationTools(body); got != 1 {
		t.Fatalf("native image_generation count = %d, want 1; tools=%#v", got, body["tools"])
	}
	if knobs["openai_responses_image_generation_tool"] != "auto" {
		t.Fatalf("injection knob = %#v, want auto", knobs["openai_responses_image_generation_tool"])
	}
}

func TestOpenAIResponsesInjectsNativeImageGenerationWhenAnalysisFollows(t *testing.T) {
	knobs := map[string]any{}
	body := captureOpenAIResponsesRequestBody(t, StreamRequest{
		OpenAIImageGenerationEnabled: true,
		Messages:                     []Message{{Role: "user", Content: "generate an image of a red balloon, then analyze the MIME type"}},
		Tools:                        []json.RawMessage{generateImageToolJSON()},
		RequestKnobs:                 knobs,
	})
	if got := countNativeImageGenerationTools(body); got != 1 {
		t.Fatalf("native image_generation count = %d, want 1; tools=%#v", got, body["tools"])
	}
}

func TestOpenAIResponsesDoesNotInjectNativeImageGenerationForFalsePositives(t *testing.T) {
	cases := []struct {
		name string
		text string
	}{
		{name: "mime", text: "what's the MIME type of this image"},
		{name: "base64", text: "decode the base64 image payload"},
		{name: "protocol", text: "explain the image generation protocol"},
		{name: "result", text: "inspect the GenerateImage result"},
		{name: "past_tense", text: "the created image looks fine"},
		{name: "third_person", text: "this function generates an image response"},
		{name: "test", text: "write a test for image handling"},
		{name: "create_test", text: "create a test for image handling"},
		{name: "create_testdata", text: "create testdata for image handling"},
		{name: "create_fixture", text: "create a fixture for image payload parsing"},
		{name: "create_loader", text: "create an image loader for the upload form"},
		{name: "generate_response_code", text: "generate an image response object in the API client"},
		{name: "discuss_how_to_generate", text: "explain how to generate an image with this API"},
		{name: "chinese_discuss_how_to_generate", text: "分析如何生成图片并处理返回格式"},
		{name: "negated", text: "don't generate an image"},
		{name: "not_to", text: "I asked you not to generate an image"},
		{name: "do_not_want", text: "do not want to generate an image"},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			knobs := map[string]any{}
			body := captureOpenAIResponsesRequestBody(t, StreamRequest{
				OpenAIImageGenerationEnabled: true,
				Messages:                     []Message{{Role: "user", Content: test.text}},
				Tools:                        []json.RawMessage{generateImageToolJSON()},
				RequestKnobs:                 knobs,
			})
			if got := countNativeImageGenerationTools(body); got != 0 {
				t.Fatalf("native image_generation count = %d, want 0; tools=%#v", got, body["tools"])
			}
			if _, ok := knobs["openai_responses_image_generation_tool"]; ok {
				t.Fatalf("injection knob should stay unset: %#v", knobs)
			}
		})
	}
}

func TestOpenAIResponsesExtraParamsCannotBypassImageGenerationGate(t *testing.T) {
	body := captureOpenAIResponsesRequestBody(t, StreamRequest{
		OpenAIImageGenerationEnabled: false,
		OpenAIExtraParamsEnabled:     true,
		OpenAIExtraParamsJSON:        `{"tools":[{"type":"image_generation"}]}`,
		Messages:                     []Message{{Role: "user", Content: "analyze the image MIME type"}},
		Tools:                        []json.RawMessage{generateImageToolJSON()},
		RequestKnobs:                 map[string]any{},
	})
	if got := countNativeImageGenerationTools(body); got != 0 {
		t.Fatalf("extra params bypassed image-generation gate: %#v", body["tools"])
	}
}

func TestOpenAIResponsesExtraParamsCannotDuplicateAllowedImageGenerationTool(t *testing.T) {
	body := captureOpenAIResponsesRequestBody(t, StreamRequest{
		OpenAIImageGenerationEnabled: true,
		OpenAIExtraParamsEnabled:     true,
		OpenAIExtraParamsJSON:        `{"tools":[{"type":"image_generation"},{"type":"image_generation"}]}`,
		Messages:                     []Message{{Role: "user", Content: "generate an image of a red balloon"}},
		Tools:                        []json.RawMessage{generateImageToolJSON()},
		RequestKnobs:                 map[string]any{},
	})
	if got := countNativeImageGenerationTools(body); got != 1 {
		t.Fatalf("native image_generation count = %d, want 1; tools=%#v", got, body["tools"])
	}
}

func TestOpenAIResponsesRequestBodyOverrideCannotBypassImageGenerationGate(t *testing.T) {
	body := captureOpenAIResponsesRequestBody(t, StreamRequest{
		OpenAIImageGenerationEnabled: true,
		Messages:                     []Message{{Role: "user", Content: "generate an image of a cat"}},
		Tools:                        []json.RawMessage{json.RawMessage(`{"name":"Read","parameters":{"type":"object"}}`)},
		RequestBodyOverride: map[string]any{
			"model":  "gpt-5.4",
			"input":  []any{map[string]any{"role": "user", "content": "generate an image of a cat"}},
			"stream": true,
			"tools":  []map[string]any{{"type": "image_generation"}},
		},
		RequestKnobs: map[string]any{},
	})
	if got := countNativeImageGenerationTools(body); got != 0 {
		t.Fatalf("request body override bypassed canonical tool gate: %#v", body["tools"])
	}
}

func TestOpenAIResponsesDoesNotInjectWhenCapabilityDisabled(t *testing.T) {
	body := captureOpenAIResponsesRequestBody(t, StreamRequest{
		OpenAIImageGenerationEnabled: false,
		Messages:                     []Message{{Role: "user", Content: "generate an image of a cat"}},
		Tools:                        []json.RawMessage{generateImageToolJSON()},
		RequestKnobs:                 map[string]any{},
	})
	if got := countNativeImageGenerationTools(body); got != 0 {
		t.Fatalf("disabled capability still injected native tool: %#v", body["tools"])
	}
}

func TestOpenAIChatCompletionsDoesNotInjectNativeImageGeneration(t *testing.T) {
	var requestBody map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if err := json.NewDecoder(request.Body).Decode(&requestBody); err != nil {
			http.Error(writer, err.Error(), http.StatusBadRequest)
			return
		}
		writer.Header().Set("Content-Type", "text/event-stream")
		_, _ = fmt.Fprint(writer, "data: {\"model\":\"gpt-test\",\"choices\":[{\"delta\":{\"content\":\"done\"},\"finish_reason\":\"stop\"}]}\n\n")
		_, _ = fmt.Fprint(writer, "data: [DONE]\n\n")
	}))
	defer server.Close()

	adapter := &OpenAIAdapter{client: server.Client()}
	err := adapter.Stream(context.Background(), StreamRequest{
		RequestID:                    "request-1",
		RunID:                        "run-1",
		ModelCallID:                  "model-call-1",
		BaseURL:                      server.URL,
		APIKey:                       "test-key",
		ProviderModelID:              "gpt-test",
		OpenAIEndpoint:               "/v1/chat/completions",
		OpenAIImageGenerationEnabled: true,
		OpenAIExtraParamsEnabled:     true,
		OpenAIExtraParamsJSON:        `{"tools":[{"type":"image_generation"}]}`,
		Messages:                     []Message{{Role: "user", Content: "generate an image of a cat"}},
		Tools:                        []json.RawMessage{generateImageFunctionToolJSON()},
		MaxTokens:                    128,
		RequestKnobs:                 map[string]any{},
	}, func(ModelEvent) error { return nil })
	if err != nil {
		t.Fatalf("stream failed: %v", err)
	}
	if got := countNativeImageGenerationTools(requestBody); got != 0 {
		t.Fatalf("chat completions injected native image_generation: %#v", requestBody["tools"])
	}
}

func TestRouterProjectsOpenAIImageGenerationEnabled(t *testing.T) {
	openAI := &recordingModelAdapter{}
	router := &Router{
		openai: openAI,
		resolver: staticChannelResolver{channel: &legacyruntime.ResolvedChannel{
			ID:                           "channel-a",
			Provider:                     "openai",
			Model:                        "gpt-test",
			OpenAIEndpoint:               "/v1/responses",
			OpenAIImageGenerationEnabled: true,
		}},
	}
	requestKnobs := map[string]any{}
	err := router.Stream(context.Background(), StreamRequest{
		ModelID:      "channel-a",
		RequestKnobs: requestKnobs,
	}, func(ModelEvent) error { return nil })
	if err != nil {
		t.Fatalf("Stream returned error: %v", err)
	}
	if !openAI.request.OpenAIImageGenerationEnabled {
		t.Fatal("OpenAIImageGenerationEnabled was not projected onto StreamRequest")
	}
	if got := openAI.request.RequestKnobs["openai_image_generation_enabled"]; got != true {
		t.Fatalf("diagnostic knob = %#v, want true", got)
	}
	if _, ok := requestKnobs["openai_image_generation_enabled"]; ok {
		t.Fatalf("original request knobs were mutated: %#v", requestKnobs)
	}
}

func TestApplyChannelToRequestIsolatesOpenAIImageGenerationPerChannel(t *testing.T) {
	shared := map[string]any{"keep": true}
	req := StreamRequest{RequestKnobs: shared, MaxTokens: 1}
	enabled := applyChannelToRequest(req, &legacyruntime.ResolvedChannel{
		Provider:                     "openai",
		OpenAIEndpoint:               "/v1/responses",
		OpenAIImageGenerationEnabled: true,
	})
	disabled := applyChannelToRequest(req, &legacyruntime.ResolvedChannel{
		Provider:                     "openai",
		OpenAIEndpoint:               "/v1/chat/completions",
		OpenAIImageGenerationEnabled: false,
	})
	if !enabled.OpenAIImageGenerationEnabled {
		t.Fatal("enabled channel lost OpenAIImageGenerationEnabled")
	}
	if disabled.OpenAIImageGenerationEnabled {
		t.Fatal("disabled channel inherited OpenAIImageGenerationEnabled")
	}
	if enabled.RequestKnobs["openai_image_generation_enabled"] != true {
		t.Fatalf("enabled knob = %#v", enabled.RequestKnobs["openai_image_generation_enabled"])
	}
	if disabled.RequestKnobs["openai_image_generation_enabled"] != false {
		t.Fatalf("disabled knob = %#v", disabled.RequestKnobs["openai_image_generation_enabled"])
	}
	enabled.RequestKnobs["openai_responses_image_generation_tool"] = "auto"
	if _, ok := disabled.RequestKnobs["openai_responses_image_generation_tool"]; ok {
		t.Fatal("fallback channels must not share request knobs")
	}
	if _, ok := shared["openai_responses_image_generation_tool"]; ok {
		t.Fatal("original request knobs were mutated")
	}
	if shared["keep"] != true {
		t.Fatal("original request knobs lost unrelated keys")
	}
}

func captureOpenAIResponsesRequestBody(t *testing.T, req StreamRequest) map[string]any {
	t.Helper()
	var requestBody map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if err := json.NewDecoder(request.Body).Decode(&requestBody); err != nil {
			http.Error(writer, err.Error(), http.StatusBadRequest)
			return
		}
		writer.Header().Set("Content-Type", "text/event-stream")
		_, _ = fmt.Fprint(writer, "data: {\"type\":\"response.completed\",\"response\":{\"id\":\"resp-1\",\"model\":\"gpt-5.4\",\"status\":\"completed\",\"output_text\":\"done\"}}\n\n")
		_, _ = fmt.Fprint(writer, "data: [DONE]\n\n")
	}))
	defer server.Close()

	if strings.TrimSpace(req.RequestID) == "" {
		req.RequestID = "request-1"
	}
	if strings.TrimSpace(req.RunID) == "" {
		req.RunID = "run-1"
	}
	if strings.TrimSpace(req.ModelCallID) == "" {
		req.ModelCallID = "model-call-1"
	}
	req.BaseURL = server.URL
	if strings.TrimSpace(req.APIKey) == "" {
		req.APIKey = "test-key"
	}
	if strings.TrimSpace(req.ProviderModelID) == "" {
		req.ProviderModelID = "gpt-5.4"
	}
	if strings.TrimSpace(req.OpenAIEndpoint) == "" {
		req.OpenAIEndpoint = "/v1/responses"
	}
	if req.MaxTokens == 0 {
		req.MaxTokens = 128
	}
	if req.RequestKnobs == nil {
		req.RequestKnobs = map[string]any{}
	}

	adapter := &OpenAIAdapter{client: server.Client()}
	if err := adapter.Stream(context.Background(), req, func(ModelEvent) error { return nil }); err != nil {
		t.Fatalf("stream failed: %v", err)
	}
	return requestBody
}

func countNativeImageGenerationTools(body map[string]any) int {
	tools, _ := body["tools"].([]any)
	count := 0
	for _, item := range tools {
		tool, _ := item.(map[string]any)
		if strings.TrimSpace(fmt.Sprint(tool["type"])) == "image_generation" {
			count++
		}
	}
	return count
}

func countNativeImageGenerationToolsFromSlice(tools []map[string]any) int {
	count := 0
	for _, tool := range tools {
		if strings.TrimSpace(fmt.Sprint(tool["type"])) == "image_generation" {
			count++
		}
	}
	return count
}
