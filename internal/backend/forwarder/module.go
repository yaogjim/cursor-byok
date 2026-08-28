// module.go 负责把 forwarder service 装配成 legacy HTTP/Connect handler。
package forwarder

import (
	"context"
	"net/http"

	"connectrpc.com/connect"

	modeladapter "cursor/internal/backend/agent/model"
	"cursor/internal/subscriptionauth"
)

type Module struct {
	Service                  *Service
	LocalBidiHandler         http.Handler
	LocalRunSSE              http.Handler
	AiHandler                http.Handler
	RepositoryServiceHandler http.Handler
	UploadServiceHandler     http.Handler
}

func (module *Module) Close() {
	if module == nil || module.Service == nil {
		return
	}
	module.Service.Close()
}

func (module *Module) ActiveProviderCount() int {
	if module == nil || module.Service == nil {
		return 0
	}
	return module.Service.ActiveProviderCount()
}

func (module *Module) WaitForProvidersIdle(ctx context.Context) {
	if module == nil || module.Service == nil {
		return
	}
	module.Service.WaitForProvidersIdle(ctx)
}

func (module *Module) CancelActiveProviders(message string) int {
	if module == nil || module.Service == nil {
		return 0
	}
	return module.Service.CancelActiveProviders(message)
}

// NewModule 创建 forwarder 模块，并导出本地 Bidi / RunSSE 处理器。
func NewModule(historyRoot string, channelService modeladapter.ChannelResolver, captures ...captureRecorder) *Module {
	return NewModuleWithCredentials(historyRoot, channelService, nil, captures...)
}

func NewModuleWithCredentials(historyRoot string, channelService modeladapter.ChannelResolver, credentials subscriptionauth.CredentialResolver, captures ...captureRecorder) *Module {
	service := NewServiceWithCredentials(historyRoot, channelService, credentials, captures...)
	legacyBidiAppendProcedure := "/aiserver.v1.BidiService/BidiAppend"
	legacyRunSSEProcedure := "/agent.v1.AgentService/RunSSE"
	return &Module{
		Service:                  service,
		LocalBidiHandler:         connect.NewUnaryHandler(legacyBidiAppendProcedure, service.BidiAppend),
		LocalRunSSE:              NewLegacyRunSSEHandler(legacyRunSSEProcedure, service.RunSSE),
		AiHandler:                newAIHandler(service),
		RepositoryServiceHandler: newRepositoryServiceHandler(service),
		UploadServiceHandler:     newUploadServiceHandler(service),
	}
}
