package upstream

import (
	"fmt"
	"strings"

	"cursor/gen/agentv1"
	"cursor/gen/aiserverv1"
	"cursor/internal/backend/agent/protocol"
	"cursor/internal/modelchannel"
	legacyruntime "cursor/internal/runtime"

	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"
)

const (
	bidiAppendProcedure = "/aiserver.v1.BidiService/BidiAppend"
	runSSEProcedure     = "/agent.v1.AgentService/RunSSE"
)

// AgentDestination is an explicit Local/Official routing decision.
// Unknown is a distinct state and must never be treated as the first local adapter.
type AgentDestination int

const (
	AgentDestinationUnknown AgentDestination = iota
	AgentDestinationLocal
	AgentDestinationOfficial
)

func (dest AgentDestination) String() string {
	switch dest {
	case AgentDestinationLocal:
		return "local"
	case AgentDestinationOfficial:
		return "official"
	default:
		return "unknown"
	}
}

// DecideAgentDestination maps a first run/prewarm onto Local or Official.
// Current channel IDs, variants, and a unique legacy hash stay Local.
// Official identity sends remaining IDs (provider collisions, auto/fast/default, empty) Official.
// Missing/placeholder identity always returns Local so the existing resolver owns exact failures.
func DecideAgentDestination(modelID string, adapters []legacyruntime.ModelAdapterConfig, officialIdentity bool) AgentDestination {
	if matchesLocalCurrentOrUniqueLegacy(modelID, adapters) {
		return AgentDestinationLocal
	}
	if officialIdentity {
		return AgentDestinationOfficial
	}
	return AgentDestinationLocal
}

func matchesLocalCurrentOrUniqueLegacy(modelID string, adapters []legacyruntime.ModelAdapterConfig) bool {
	candidates := routingIDCandidates(modelID)
	if len(candidates) == 0 {
		return false
	}
	for _, adapter := range adapters {
		current := strings.TrimSpace(adapter.ID)
		if current == "" {
			continue
		}
		if _, ok := candidates[current]; ok {
			return true
		}
	}
	legacyHits := 0
	for _, adapter := range adapters {
		legacy := strings.TrimSpace(modelchannel.BuildLegacyChannelID(adapter.BaseURL, adapter.ModelID, adapter.APIKey, adapter.DisplayName))
		if legacy == "" {
			continue
		}
		if _, ok := candidates[legacy]; !ok {
			continue
		}
		legacyHits++
		if legacyHits > 1 {
			return false
		}
	}
	return legacyHits == 1
}

func routingIDCandidates(modelID string) map[string]struct{} {
	id := strings.TrimSpace(modelID)
	if id == "" || modelchannel.IsMetaModelAlias(id) {
		return nil
	}
	ids := map[string]struct{}{id: {}}
	index := strings.LastIndex(id, ":")
	if index <= 0 {
		return ids
	}
	prefix := strings.TrimSpace(id[:index])
	if prefix != "" && !modelchannel.IsMetaModelAlias(prefix) {
		ids[prefix] = struct{}{}
	}
	return ids
}

func parseBidiAppendRouting(contentType string, encoding string, body []byte) (requestID string, modelID string, runOrPrewarm bool, err error) {
	payload, err := decodeAgentRoutePayload(contentType, encoding, body)
	if err != nil {
		return "", "", false, err
	}
	message := &aiserverv1.BidiAppendRequest{}
	if len(payload) > 0 {
		if err := unmarshalAgentRouteMessage(contentType, payload, message); err != nil {
			return "", "", false, err
		}
	}
	requestID = protocol.NormalizeRequestID(protocol.ReadAppendRequestID(message))
	clientMessage, _, decodeErr := protocol.DecodeAgentClientMessage(message.GetData())
	if decodeErr != nil {
		return requestID, "", false, decodeErr
	}
	if clientMessage == nil && len(message.GetDataBinary()) > 0 {
		clientMessage = &agentv1.AgentClientMessage{}
		if err := proto.Unmarshal(message.GetDataBinary(), clientMessage); err != nil {
			return requestID, "", false, err
		}
	}
	return requestID, protocol.ReadRequestedModelID(clientMessage), protocol.HasRunOrPrewarmRequest(clientMessage), nil
}

func parseRunSSERequestID(contentType string, encoding string, body []byte) (string, error) {
	payload, err := decodeAgentRoutePayload(contentType, encoding, body)
	if err != nil {
		return "", err
	}
	message := &aiserverv1.BidiRequestId{}
	if len(payload) > 0 {
		if err := unmarshalAgentRouteMessage(contentType, payload, message); err != nil {
			return "", err
		}
	}
	return protocol.NormalizeRequestID(protocol.ReadBidiRequestID(message)), nil
}

// decodeAgentRoutePayload is a read-only copy: HTTP Content-Encoding first,
// then Connect frames when the media type is framed. Frame gzip is not HTTP gzip.
func decodeAgentRoutePayload(contentType string, encoding string, body []byte) ([]byte, error) {
	payload, err := decodeAgentRouteHTTPBody(encoding, body)
	if err != nil {
		return nil, err
	}
	switch connectMediaType(contentType) {
	case "application/connect+proto", "application/connect+json":
		return extractConnectUnaryMessage(payload)
	default:
		return payload, nil
	}
}

func decodeAgentRouteHTTPBody(encoding string, body []byte) ([]byte, error) {
	switch strings.ToLower(strings.TrimSpace(encoding)) {
	case "", "identity":
		return body, nil
	case "gzip", "x-gzip":
		return gunzipBytes(body)
	default:
		return nil, fmt.Errorf("unsupported content encoding %q", encoding)
	}
}

func unmarshalAgentRouteMessage(contentType string, payload []byte, message proto.Message) error {
	switch connectMediaType(contentType) {
	case "application/json", "application/connect+json":
		return protojson.UnmarshalOptions{DiscardUnknown: true}.Unmarshal(payload, message)
	default:
		return proto.Unmarshal(payload, message)
	}
}
