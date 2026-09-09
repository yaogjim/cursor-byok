package upstream

import (
	"fmt"
	"net/http"
	"strings"

	"cursor/internal/backend/server"
	legacyruntime "cursor/internal/runtime"
)

// AgentRouteAction splits BidiAppend/RunSSE by model identity.
// Local hits the existing BYOK forwarder; unmatched models with official
// identity go to official upstream with the inbound Authorization preserved.
func AgentRouteAction(deps Dependencies, sessions *AgentSessionStore, local http.Handler) server.HandlerFunc {
	if sessions == nil {
		sessions = NewAgentSessionStore()
	}
	return func(ctx *server.Context) error {
		reqCtx, _, err := newCompatRouteObjects(ctx, deps, CompatRouteConfig{Name: ctx.RouteName})
		if err != nil {
			return err
		}
		if reqCtx == nil || reqCtx.Request == nil {
			return fmt.Errorf("agent route request context is unavailable")
		}
		path := ""
		if reqCtx.Request.URL != nil {
			path = reqCtx.Request.URL.Path
		}
		switch path {
		case bidiAppendProcedure:
			return routeBidiAppend(reqCtx, sessions, local)
		case runSSEProcedure:
			return routeRunSSE(reqCtx, sessions, local)
		default:
			return serveLocalAgent(reqCtx, local)
		}
	}
}

func routeBidiAppend(reqCtx *RequestContext, sessions *AgentSessionStore, local http.Handler) error {
	requestID, modelID, runOrPrewarm, parseErr := parseBidiAppendRouting(reqCtx.ContentType, reqCtx.RequestBody)
	if strings.TrimSpace(requestID) == "" {
		if parseErr != nil {
			return parseErr
		}
		return fmt.Errorf("agent bidi append missing request_id")
	}
	if dest, ok := sessions.Lookup(requestID); ok {
		return dispatchAgent(reqCtx, dest, local)
	}
	if parseErr != nil {
		return parseErr
	}
	if !runOrPrewarm {
		return fmt.Errorf("agent destination is unknown")
	}
	official := hasOfficialIdentity(reqCtx.Headers)
	var adapters []legacyruntime.ModelAdapterConfig
	if official {
		loaded, err := loadConfiguredModelAdapters(reqCtx)
		if err != nil {
			return err
		}
		adapters = loaded
	}
	dest := sessions.Remember(requestID, DecideAgentDestination(modelID, adapters, official))
	return dispatchAgent(reqCtx, dest, local)
}

func routeRunSSE(reqCtx *RequestContext, sessions *AgentSessionStore, local http.Handler) error {
	requestID, err := parseRunSSERequestID(reqCtx.ContentType, reqCtx.RequestBody)
	if err != nil {
		return err
	}
	if strings.TrimSpace(requestID) == "" {
		return fmt.Errorf("agent run sse missing request_id")
	}
	if dest, ok := sessions.Lookup(requestID); ok {
		return dispatchAgent(reqCtx, dest, local)
	}
	ctx := reqCtx.Request.Context()
	dest, ok := sessions.Wait(ctx, requestID)
	if !ok {
		if err := ctx.Err(); err != nil {
			return err
		}
		return fmt.Errorf("agent run sse canceled before routing decision")
	}
	return dispatchAgent(reqCtx, dest, local)
}

func dispatchAgent(reqCtx *RequestContext, dest AgentDestination, local http.Handler) error {
	switch dest {
	case AgentDestinationLocal:
		return serveLocalAgent(reqCtx, local)
	case AgentDestinationOfficial:
		return forwardOfficialAgent(reqCtx)
	default:
		return fmt.Errorf("agent destination is unknown")
	}
}

func serveLocalAgent(reqCtx *RequestContext, local http.Handler) error {
	if reqCtx == nil || reqCtx.Request == nil || reqCtx.ResponseWriter == nil {
		return fmt.Errorf("agent route request context is unavailable")
	}
	if local == nil {
		return fmt.Errorf("local agent handler is unavailable")
	}
	local.ServeHTTP(reqCtx.ResponseWriter, reqCtx.Request)
	return nil
}

func forwardOfficialAgent(reqCtx *RequestContext) error {
	if reqCtx == nil {
		return fmt.Errorf("agent route request context is unavailable")
	}
	if !hasOfficialIdentity(reqCtx.Headers) {
		return fmt.Errorf("official agent route rejected local relay authorization")
	}
	target := catalogFetchTarget(reqCtx)
	if target == nil {
		return fmt.Errorf("official agent upstream target is unavailable")
	}
	reqCtx.TargetURL = target
	_, err := ForwardToUpstream(reqCtx, ForwardOptions{PreserveInboundIdentity: true})
	return err
}
