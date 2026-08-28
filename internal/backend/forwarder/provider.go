// provider.go 把 forwarder 的 canonical 请求转交给现有的 provider adapter 层。
package forwarder

import (
	"context"
	"encoding/json"
	"strings"

	modeladapter "cursor/internal/backend/agent/model"
)

type DefaultProviderGateway struct {
	router modeladapter.ModelAdapterRouter
}

// NewProviderGateway 创建默认 provider 网关。
// 若 resolver 同时实现了 ChannelPlanResolver（如 config.Manager），则使用
// FallbackAwareRouter；否则退回到普通 Router，行为与之前完全一致。
func NewProviderGateway(resolver modeladapter.ChannelResolver) *DefaultProviderGateway {
	var router modeladapter.ModelAdapterRouter
	if planResolver, ok := resolver.(modeladapter.ChannelPlanResolver); ok {
		router = modeladapter.NewFallbackAwareRouter(modeladapter.NewRouter(resolver), planResolver)
	} else {
		router = modeladapter.NewRouter(resolver)
	}
	return &DefaultProviderGateway{router: router}
}

// StartStream 把 forwarder 的 provider 请求翻译成 modeladapter.StreamRequest 并发起流式调用。
func (gateway *DefaultProviderGateway) StartStream(ctx context.Context, req ProviderRequest, sink func(modeladapter.ModelEvent) error) error {
	if ctx == nil {
		ctx = context.Background()
	}
	defer releaseArtifactSession(req.Observer, req.RequestID, req.ModelCallID)
	requestKnobs := make(map[string]any, len(req.RequestKnobs)+2)
	for key, value := range req.RequestKnobs {
		requestKnobs[key] = value
	}
	requestKnobs["stream"] = true
	if req.MaxTokens > 0 {
		requestKnobs["max_tokens"] = req.MaxTokens
	}
	if strings.TrimSpace(req.ThinkingEffort) != "" {
		requestKnobs["runtime_thinking_effort"] = strings.TrimSpace(req.ThinkingEffort)
	}
	err := gateway.router.Stream(ctx, modeladapter.StreamRequest{
		RequestID:           req.RequestID,
		RunID:               req.RunID,
		ModelCallID:         req.ModelCallID,
		ConversationID:      req.ConversationID,
		Mode:                req.Mode,
		ModelID:             req.ModelID,
		ThinkingEffort:      req.ThinkingEffort,
		Messages:            req.Messages,
		StableMessageCount:  req.StableMessageCount,
		Tools:               append([]json.RawMessage(nil), req.Tools...),
		MaxTokens:           req.MaxTokens,
		Stream:              true,
		RequestKnobs:        requestKnobs,
		CompileSummary:      req.CompileSummary,
		Observer:            req.Observer,
		ArtifactPaths:       req.ArtifactPaths,
		RequestBodyOverride: req.RequestBodyOverride,
		StreamDiagnostics:   req.StreamDiagnostics,
	}, sink)
	if err != nil {
		return providerTerminalError{cause: err}
	}
	return nil
}

type artifactSessionCleaner interface {
	ClearActiveArtifacts(requestID string, modelCallID string)
}

func releaseArtifactSession(observer modeladapter.LLMArtifactObserver, requestID string, modelCallID string) {
	cleaner, ok := observer.(artifactSessionCleaner)
	if !ok {
		return
	}
	cleaner.ClearActiveArtifacts(requestID, modelCallID)
}
