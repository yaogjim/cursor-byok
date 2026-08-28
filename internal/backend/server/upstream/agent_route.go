package upstream

import (
	"strings"

	"cursor/gen/agentv1"
	"cursor/gen/aiserverv1"
	"cursor/internal/backend/agent/protocol"
	"cursor/internal/modelchannel"
	legacyruntime "cursor/internal/runtime"

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

// LocalAdapterIDs returns the advertised local channel hashes used for Agent routing.
// Provider model IDs are intentionally excluded so official names cannot collide into BYOK.
func LocalAdapterIDs(adapters []legacyruntime.ModelAdapterConfig) map[string]struct{} {
	ids := make(map[string]struct{}, len(adapters))
	for _, adapter := range adapters {
		id := strings.TrimSpace(adapter.ID)
		if id == "" {
			continue
		}
		ids[id] = struct{}{}
	}
	return ids
}

// LocalProviderModelIDs returns configured provider model IDs. These are not
// Cursor-advertised channel hashes and must not be upgraded to Official.
func LocalProviderModelIDs(adapters []legacyruntime.ModelAdapterConfig) map[string]struct{} {
	ids := make(map[string]struct{}, len(adapters))
	for _, adapter := range adapters {
		id := strings.TrimSpace(adapter.ModelID)
		if id == "" {
			continue
		}
		ids[id] = struct{}{}
	}
	return ids
}

// DecideAgentDestination maps a request model ID onto Local, Official, or Unknown.
// Local adapter hashes (and hash:variant) are Local. Empty IDs, meta aliases, and
// configured provider model IDs are Unknown and must not upgrade to Official.
// Remaining unmatched IDs are Official. There is no implicit first-adapter fallback.
func DecideAgentDestination(modelID string, localIDs map[string]struct{}, providerIDs map[string]struct{}) AgentDestination {
	id := strings.TrimSpace(modelID)
	if id == "" || modelchannel.IsMetaModelAlias(id) {
		return AgentDestinationUnknown
	}
	if routingIDMatch(id, localIDs) {
		return AgentDestinationLocal
	}
	if routingIDMatch(id, providerIDs) {
		return AgentDestinationUnknown
	}
	return AgentDestinationOfficial
}

func routingIDMatch(id string, ids map[string]struct{}) bool {
	if id == "" || len(ids) == 0 {
		return false
	}
	if _, ok := ids[id]; ok {
		return true
	}
	index := strings.LastIndex(id, ":")
	if index <= 0 {
		return false
	}
	_, ok := ids[strings.TrimSpace(id[:index])]
	return ok
}

func parseBidiAppendRouting(contentType string, body []byte) (requestID string, modelID string, err error) {
	payload, err := extractCatalogProtoPayload(contentType, "", body)
	if err != nil {
		return "", "", err
	}
	message := &aiserverv1.BidiAppendRequest{}
	if len(payload) > 0 {
		if err := proto.Unmarshal(payload, message); err != nil {
			return "", "", err
		}
	}
	requestID = protocol.NormalizeRequestID(protocol.ReadAppendRequestID(message))
	clientMessage, _, decodeErr := protocol.DecodeAgentClientMessage(message.GetData())
	if decodeErr != nil {
		return requestID, "", decodeErr
	}
	if clientMessage == nil && len(message.GetDataBinary()) > 0 {
		clientMessage = &agentv1.AgentClientMessage{}
		if err := proto.Unmarshal(message.GetDataBinary(), clientMessage); err != nil {
			return requestID, "", err
		}
	}
	return requestID, protocol.ReadRequestedModelID(clientMessage), nil
}

func parseRunSSERequestID(contentType string, body []byte) (string, error) {
	payload, err := extractCatalogProtoPayload(contentType, "", body)
	if err != nil {
		return "", err
	}
	message := &aiserverv1.BidiRequestId{}
	if len(payload) > 0 {
		if err := proto.Unmarshal(payload, message); err != nil {
			return "", err
		}
	}
	return protocol.NormalizeRequestID(protocol.ReadBidiRequestID(message)), nil
}
