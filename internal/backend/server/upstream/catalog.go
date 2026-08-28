package upstream

import (
	"bytes"
	"compress/gzip"
	"encoding/binary"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"

	"cursor/gen/agentv1"
	"cursor/gen/aiserverv1"
	legacyruntime "cursor/internal/runtime"

	"google.golang.org/protobuf/proto"
)

func handleMergedCatalog(reqCtx *RequestContext, route *Route) error {
	if merged, ok := tryMergeOfficialCatalog(reqCtx, route); ok {
		return writeMockProtoResponse(reqCtx, route, merged)
	}
	return handleMockProto(reqCtx, route)
}

func handleOfficialDefaultModel(reqCtx *RequestContext, route *Route) error {
	if reqCtx == nil || route == nil {
		return fmt.Errorf("official default model is unavailable")
	}
	if isLocalRelayAuthorization(reqCtx.Headers) {
		return fmt.Errorf("official default model is unavailable")
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
	return writeMockProtoResponse(reqCtx, route, payload)
}

func tryMergeOfficialCatalog(reqCtx *RequestContext, route *Route) ([]byte, bool) {
	if reqCtx == nil || route == nil {
		return nil, false
	}
	if isLocalRelayAuthorization(reqCtx.Headers) {
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
	stripCatalogSecrets(message)
	return message, nil
}

func appendLocalCatalogModels(official proto.Message, local proto.Message) error {
	switch dest := official.(type) {
	case *aiserverv1.AvailableModelsResponse:
		src, ok := local.(*aiserverv1.AvailableModelsResponse)
		if !ok {
			return fmt.Errorf("local catalog type mismatch")
		}
		seen := catalogModelNames(dest.GetModels())
		for _, model := range src.GetModels() {
			if model == nil {
				continue
			}
			name := strings.TrimSpace(model.GetName())
			if name == "" || seen[name] {
				continue
			}
			cloned, _ := proto.Clone(model).(*aiserverv1.AvailableModelsResponse_AvailableModel)
			dest.Models = append(dest.Models, cloned)
			seen[name] = true
		}
		return nil
	case *agentv1.GetUsableModelsResponse:
		src, ok := local.(*agentv1.GetUsableModelsResponse)
		if !ok {
			return fmt.Errorf("local catalog type mismatch")
		}
		seen := catalogUsableModelIDs(dest.GetModels())
		for _, model := range src.GetModels() {
			if model == nil {
				continue
			}
			id := strings.TrimSpace(model.GetModelId())
			if id == "" || seen[id] {
				continue
			}
			cloned, _ := proto.Clone(model).(*agentv1.ModelDetails)
			if cloned != nil {
				cloned.Credentials = nil
			}
			dest.Models = append(dest.Models, cloned)
			seen[id] = true
		}
		return nil
	default:
		return fmt.Errorf("unsupported catalog message %T", official)
	}
}

func stripCatalogSecrets(message proto.Message) {
	switch catalog := message.(type) {
	case *agentv1.GetUsableModelsResponse:
		for _, model := range catalog.GetModels() {
			if model != nil {
				model.Credentials = nil
			}
		}
	}
}

func catalogModelNames(models []*aiserverv1.AvailableModelsResponse_AvailableModel) map[string]bool {
	seen := make(map[string]bool, len(models))
	for _, model := range models {
		if model == nil {
			continue
		}
		if name := strings.TrimSpace(model.GetName()); name != "" {
			seen[name] = true
		}
	}
	return seen
}

func catalogUsableModelIDs(models []*agentv1.ModelDetails) map[string]bool {
	seen := make(map[string]bool, len(models))
	for _, model := range models {
		if model == nil {
			continue
		}
		if id := strings.TrimSpace(model.GetModelId()); id != "" {
			seen[id] = true
		}
	}
	return seen
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

func isLocalRelayAuthorization(headers http.Header) bool {
	if headers == nil {
		return false
	}
	local := formatBearerAuthorization(legacyruntime.LocalRelayToken)
	if local == "" {
		return false
	}
	return strings.EqualFold(strings.TrimSpace(headers.Get("Authorization")), local)
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
	// Connect unary success is a message frame plus an end-stream trailer.
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
