package upstream

import (
	"bytes"
	"compress/gzip"
	"encoding/binary"
	"fmt"
	"io"
	"net"
	"net/url"
	"strings"

	"cursor/gen/agentv1"
	"cursor/gen/aiserverv1"

	"google.golang.org/protobuf/proto"
)

const (
	catalogDisplaySuffixOfficial = "[官方]"
	catalogDisplaySuffixBYOK     = "[BYOK]"
	localCLICatalogAPIKey        = "cursor-byok-local"
)

func handleMergedCatalog(reqCtx *RequestContext, route *Route) error {
	if reqCtx == nil || route == nil {
		return fmt.Errorf("merged catalog is unavailable")
	}
	if !hasOfficialIdentity(reqCtx.Headers) {
		return handleMockProto(reqCtx, route)
	}
	if merged, ok := tryMergeOfficialCatalog(reqCtx, route); ok {
		return writeMockProtoResponse(reqCtx, route, merged)
	}
	return writeLocalCatalogWithoutRecommendations(reqCtx, route)
}

func handleOfficialDefaultModel(reqCtx *RequestContext, route *Route) error {
	if reqCtx == nil || route == nil {
		return fmt.Errorf("official default model is unavailable")
	}
	if !hasOfficialIdentity(reqCtx.Headers) {
		return handleMockProto(reqCtx, route)
	}
	target := catalogFetchTarget(reqCtx)
	if target == nil {
		return fmt.Errorf("official default model is unavailable")
	}
	fetchCtx := *reqCtx
	fetchCtx.TargetURL = target
	fetched, err := FetchUpstream(&fetchCtx, ForwardOptions{PreserveInboundIdentity: true})
	if err != nil || fetched == nil || fetched.StatusCode < 200 || fetched.StatusCode >= 300 {
		return fmt.Errorf("official default model is unavailable")
	}
	payload, err := extractCatalogProtoPayload(fetched.ContentType, fetched.Encoding, fetched.Body)
	if err != nil || len(payload) == 0 {
		return fmt.Errorf("official default model is unavailable")
	}
	payload, err = projectOfficialDefaultPayload(route.MockProtoType, payload)
	if err != nil {
		return fmt.Errorf("official default model is unavailable")
	}
	return writeMockProtoResponse(reqCtx, route, payload)
}

func tryMergeOfficialCatalog(reqCtx *RequestContext, route *Route) ([]byte, bool) {
	if reqCtx == nil || route == nil {
		return nil, false
	}
	if !hasOfficialIdentity(reqCtx.Headers) {
		return nil, false
	}
	target := catalogFetchTarget(reqCtx)
	if target == nil {
		return nil, false
	}
	fetchCtx := *reqCtx
	fetchCtx.TargetURL = target
	fetched, err := FetchUpstream(&fetchCtx, ForwardOptions{PreserveInboundIdentity: true})
	if err != nil || fetched == nil || fetched.StatusCode < 200 || fetched.StatusCode >= 300 {
		return nil, false
	}
	payload, err := extractCatalogProtoPayload(fetched.ContentType, fetched.Encoding, fetched.Body)
	if err != nil {
		return nil, false
	}
	official, err := newProtoMessage(route.MockProtoType)
	if err != nil {
		return nil, false
	}
	if err := proto.Unmarshal(payload, official); err != nil {
		return nil, false
	}
	local, err := localCatalogMessage(reqCtx, route)
	if err != nil {
		return nil, false
	}
	if err := appendLocalCatalogModels(official, local); err != nil {
		return nil, false
	}
	merged, err := proto.Marshal(official)
	if err != nil {
		return nil, false
	}
	return merged, true
}

func writeLocalCatalogWithoutRecommendations(reqCtx *RequestContext, route *Route) error {
	local, err := localCatalogMessage(reqCtx, route)
	if err != nil {
		return err
	}
	stripLocalCatalogRecommendations(local)
	payload, err := proto.Marshal(local)
	if err != nil {
		return err
	}
	return writeMockProtoResponse(reqCtx, route, payload)
}

func localCatalogMessage(reqCtx *RequestContext, route *Route) (proto.Message, error) {
	payload := map[string]any{}
	if route != nil && route.MockPayloadBuilder != nil {
		built, err := route.MockPayloadBuilder(reqCtx)
		if err != nil {
			return nil, err
		}
		payload = built
	}
	encoded, err := encodeMockProto(route.MockProtoType, payload)
	if err != nil {
		return nil, err
	}
	message, err := newProtoMessage(route.MockProtoType)
	if err != nil {
		return nil, err
	}
	if err := proto.Unmarshal(encoded, message); err != nil {
		return nil, err
	}
	projectLocalCatalog(message)
	return message, nil
}

func appendLocalCatalogModels(official proto.Message, local proto.Message) error {
	switch dest := official.(type) {
	case *aiserverv1.AvailableModelsResponse:
		src, ok := local.(*aiserverv1.AvailableModelsResponse)
		if !ok {
			return fmt.Errorf("local catalog type mismatch")
		}
		localByID := map[string]*aiserverv1.AvailableModelsResponse_AvailableModel{}
		localOrder := make([]string, 0, len(src.GetModels()))
		for _, model := range src.GetModels() {
			if model == nil {
				continue
			}
			id := strings.TrimSpace(model.GetName())
			if id == "" {
				continue
			}
			if _, exists := localByID[id]; exists {
				continue
			}
			cloned, _ := proto.Clone(model).(*aiserverv1.AvailableModelsResponse_AvailableModel)
			labelAvailableModel(cloned, catalogDisplaySuffixBYOK)
			localByID[id] = cloned
			localOrder = append(localOrder, id)
		}
		merged := make([]*aiserverv1.AvailableModelsResponse_AvailableModel, 0, len(dest.GetModels())+len(localOrder))
		for _, model := range dest.GetModels() {
			if model == nil {
				continue
			}
			id := strings.TrimSpace(model.GetName())
			if id != "" {
				if _, exists := localByID[id]; exists {
					continue
				}
			}
			cloned, _ := proto.Clone(model).(*aiserverv1.AvailableModelsResponse_AvailableModel)
			labelAvailableModel(cloned, catalogDisplaySuffixOfficial)
			merged = append(merged, cloned)
		}
		for _, id := range localOrder {
			merged = append(merged, localByID[id])
		}
		dest.Models = merged
		return nil
	case *agentv1.GetUsableModelsResponse:
		src, ok := local.(*agentv1.GetUsableModelsResponse)
		if !ok {
			return fmt.Errorf("local catalog type mismatch")
		}
		localByID := map[string]*agentv1.ModelDetails{}
		localOrder := make([]string, 0, len(src.GetModels()))
		for _, model := range src.GetModels() {
			if model == nil {
				continue
			}
			id := strings.TrimSpace(model.GetModelId())
			if id == "" {
				continue
			}
			if _, exists := localByID[id]; exists {
				continue
			}
			cloned, _ := proto.Clone(model).(*agentv1.ModelDetails)
			labelCLIModelDetails(cloned, catalogDisplaySuffixBYOK)
			rebuildLocalCLICredentials(cloned)
			localByID[id] = cloned
			localOrder = append(localOrder, id)
		}
		merged := make([]*agentv1.ModelDetails, 0, len(dest.GetModels())+len(localOrder))
		for _, model := range dest.GetModels() {
			if model == nil {
				continue
			}
			id := strings.TrimSpace(model.GetModelId())
			if id != "" {
				if _, exists := localByID[id]; exists {
					continue
				}
			}
			cloned, _ := proto.Clone(model).(*agentv1.ModelDetails)
			labelCLIModelDetails(cloned, catalogDisplaySuffixOfficial)
			merged = append(merged, cloned)
		}
		for _, id := range localOrder {
			merged = append(merged, localByID[id])
		}
		dest.Models = merged
		return nil
	default:
		return fmt.Errorf("unsupported catalog message %T", official)
	}
}

func projectLocalCatalog(message proto.Message) {
	switch catalog := message.(type) {
	case *aiserverv1.AvailableModelsResponse:
		for _, model := range catalog.GetModels() {
			labelAvailableModel(model, catalogDisplaySuffixBYOK)
		}
	case *agentv1.GetUsableModelsResponse:
		for _, model := range catalog.GetModels() {
			labelCLIModelDetails(model, catalogDisplaySuffixBYOK)
			rebuildLocalCLICredentials(model)
		}
	case *agentv1.GetDefaultModelForCliResponse:
		labelCLIModelDetails(catalog.GetModel(), catalogDisplaySuffixBYOK)
		rebuildLocalCLICredentials(catalog.GetModel())
	}
}

func projectOfficialDefaultPayload(protoType string, payload []byte) ([]byte, error) {
	if strings.TrimSpace(protoType) != "aiserver.v1.GetDefaultModelForCliResponse" {
		return payload, nil
	}
	message := &agentv1.GetDefaultModelForCliResponse{}
	if err := proto.Unmarshal(payload, message); err != nil {
		return nil, err
	}
	labelCLIModelDetails(message.GetModel(), catalogDisplaySuffixOfficial)
	return proto.Marshal(message)
}

func stripLocalCatalogRecommendations(message proto.Message) {
	catalog, ok := message.(*aiserverv1.AvailableModelsResponse)
	if !ok || catalog == nil {
		return
	}
	catalog.ComposerModelConfig = nil
	catalog.CmdKModelConfig = nil
	catalog.BackgroundComposerModelConfig = nil
	catalog.PlanExecutionModelConfig = nil
	catalog.SpecModelConfig = nil
	catalog.DeepSearchModelConfig = nil
	catalog.QuickAgentModelConfig = nil
	catalog.SubagentModelConfigs = nil
}

func labelAvailableModel(model *aiserverv1.AvailableModelsResponse_AvailableModel, suffix string) {
	if model == nil {
		return
	}
	fallback := strings.TrimSpace(model.GetName())
	if labeled := applyCatalogDisplaySuffix(model.GetClientDisplayName(), fallback, suffix); labeled != "" {
		model.ClientDisplayName = proto.String(labeled)
	}
	if labeled := applyCatalogDisplaySuffix(model.GetInputboxShortModelName(), fallback, suffix); labeled != "" {
		model.InputboxShortModelName = proto.String(labeled)
	}
	for _, variant := range model.GetVariants() {
		if variant == nil {
			continue
		}
		variant.DisplayName = applyCatalogDisplaySuffix(variant.GetDisplayName(), fallback, suffix)
		if strings.TrimSpace(variant.GetDisplayNameOutsidePicker()) != "" {
			variant.DisplayNameOutsidePicker = proto.String(applyCatalogDisplaySuffix(variant.GetDisplayNameOutsidePicker(), fallback, suffix))
		}
	}
}

func labelCLIModelDetails(model *agentv1.ModelDetails, suffix string) {
	if model == nil {
		return
	}
	fallback := strings.TrimSpace(model.GetModelId())
	model.DisplayName = applyCatalogDisplaySuffix(model.GetDisplayName(), fallback, suffix)
	model.DisplayNameShort = applyCatalogDisplaySuffix(model.GetDisplayNameShort(), fallback, suffix)
}

func rebuildLocalCLICredentials(model *agentv1.ModelDetails) {
	if model == nil {
		return
	}
	model.Credentials = &agentv1.ModelDetails_ApiKeyCredentials{
		ApiKeyCredentials: &agentv1.ApiKeyCredentials{
			ApiKey: localCLICatalogAPIKey,
		},
	}
}

func applyCatalogDisplaySuffix(value string, fallback string, suffix string) string {
	suffix = strings.TrimSpace(suffix)
	text := strings.TrimSpace(value)
	if suffix == "" {
		return strings.TrimSpace(value)
	}
	if text == "" {
		text = strings.TrimSpace(fallback)
	}
	if text == "" {
		return ""
	}
	if strings.Contains(text, suffix) {
		return text
	}
	return text + " " + suffix
}

func localCatalogDisplaySuffix(reqCtx *RequestContext) string {
	return catalogDisplaySuffixBYOK
}

func catalogFetchTarget(reqCtx *RequestContext) *url.URL {
	if reqCtx == nil {
		return nil
	}
	var localHost string
	if reqCtx.Request != nil && reqCtx.Request.URL != nil {
		localHost = reqCtx.Request.URL.Host
	}
	candidates := make([]*url.URL, 0, 2)
	if parsed, err := ParseAndValidateRawURL(reqCtx.RawURL); err == nil {
		candidates = append(candidates, parsed)
	}
	if reqCtx.TargetURL != nil && strings.TrimSpace(reqCtx.TargetURL.Host) != "" {
		candidates = append(candidates, reqCtx.TargetURL)
	}
	for _, candidate := range candidates {
		if candidate == nil || strings.TrimSpace(candidate.Host) == "" {
			continue
		}
		if sameHTTPHost(candidate.Host, localHost) {
			continue
		}
		return candidate
	}
	return nil
}

func sameHTTPHost(left string, right string) bool {
	return normalizeCatalogHost(left) == normalizeCatalogHost(right)
}

func normalizeCatalogHost(host string) string {
	value := strings.ToLower(strings.TrimSpace(host))
	if value == "" {
		return ""
	}
	if hostname, port, err := net.SplitHostPort(value); err == nil {
		if port == "80" || port == "443" {
			return hostname
		}
		return hostname + ":" + port
	}
	return value
}

const (
	connectEnvelopeHeaderBytes = 5
	connectFlagCompressed      = 0x01
	connectFlagEndStream       = 0x02
)

func protoResponseForInbound(reqCtx *RequestContext, message []byte) (contentType string, body []byte) {
	if inboundConnectProto(reqCtx) {
		return "application/connect+proto", encodeConnectUnaryResponse(message)
	}
	return "application/proto", message
}

func inboundConnectProto(reqCtx *RequestContext) bool {
	if reqCtx == nil {
		return false
	}
	return connectMediaType(reqCtx.ContentType) == "application/connect+proto"
}

func connectMediaType(contentType string) string {
	return strings.ToLower(strings.TrimSpace(strings.SplitN(contentType, ";", 2)[0]))
}

func encodeConnectUnaryResponse(message []byte) []byte {
	trailer := []byte("{}")
	body := make([]byte, 0, 2*connectEnvelopeHeaderBytes+len(message)+len(trailer))
	body = append(body, encodeConnectEnvelope(0, message)...)
	body = append(body, encodeConnectEnvelope(connectFlagEndStream, trailer)...)
	return body
}

func encodeConnectEnvelope(flags byte, payload []byte) []byte {
	envelope := make([]byte, connectEnvelopeHeaderBytes+len(payload))
	envelope[0] = flags
	binary.BigEndian.PutUint32(envelope[1:connectEnvelopeHeaderBytes], uint32(len(payload)))
	copy(envelope[connectEnvelopeHeaderBytes:], payload)
	return envelope
}

// extractCatalogProtoPayload separates HTTP Content-Encoding gzip from Connect
// per-envelope gzip. encoding must be the HTTP Content-Encoding value only;
// Connect-Content-Encoding is not whole-body compression.
func extractCatalogProtoPayload(contentType string, encoding string, body []byte) ([]byte, error) {
	payload := body
	if isGzipEncoding(encoding) {
		decoded, err := gunzipBytes(payload)
		if err != nil {
			return nil, err
		}
		payload = decoded
	}
	if connectMediaType(contentType) != "application/connect+proto" {
		return payload, nil
	}
	return extractConnectUnaryMessage(payload)
}

func extractConnectUnaryMessage(payload []byte) ([]byte, error) {
	offset := 0
	var message []byte
	for offset < len(payload) {
		if len(payload)-offset < connectEnvelopeHeaderBytes {
			return nil, fmt.Errorf("connect envelope is truncated")
		}
		flags := payload[offset]
		size := binary.BigEndian.Uint32(payload[offset+1 : offset+connectEnvelopeHeaderBytes])
		offset += connectEnvelopeHeaderBytes
		if uint64(size) > uint64(len(payload)-offset) {
			return nil, fmt.Errorf("connect envelope is truncated")
		}
		framed := payload[offset : offset+int(size)]
		offset += int(size)
		if flags&connectFlagCompressed != 0 {
			decoded, err := gunzipBytes(framed)
			if err != nil {
				return nil, err
			}
			framed = decoded
		}
		if flags&connectFlagEndStream != 0 {
			if message == nil {
				return nil, fmt.Errorf("connect unary response is an error trailer")
			}
			return message, nil
		}
		if message != nil {
			return nil, fmt.Errorf("connect unary response has multiple message frames")
		}
		message = framed
	}
	if message == nil {
		return nil, fmt.Errorf("connect unary response missing message frame")
	}
	return message, nil
}

func isGzipEncoding(value string) bool {
	return strings.Contains(strings.ToLower(strings.TrimSpace(value)), "gzip")
}

func gunzipBytes(body []byte) ([]byte, error) {
	reader, err := gzip.NewReader(bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	defer reader.Close()
	decoded, err := io.ReadAll(io.LimitReader(reader, maxFetchedUpstreamBody+1))
	if err != nil {
		return nil, err
	}
	if len(decoded) > maxFetchedUpstreamBody {
		return nil, fmt.Errorf("gzip payload exceeds %d bytes", maxFetchedUpstreamBody)
	}
	return decoded, nil
}
