// retry.go 保留 provider HTTP 请求入口的历史命名；provider 错误交给客户端重连链路处理。
package modeladapter

import (
	"context"
	"io"
	"net/http"
	"time"

	"cursor/internal/audit"
)

// DoProviderRequestWithRetry 保留旧入口名；本地模式不在服务端重试 provider 请求。
func DoProviderRequestWithRetry(
	ctx context.Context,
	client *http.Client,
	provider string,
	requestID string,
	modelCallID string,
	buildRequest func(context.Context) (*http.Request, error),
) (*http.Response, error) {
	return doProviderRequestWithRetry(ctx, client, provider, requestID, modelCallID, buildRequest)
}

func doProviderRequestWithRetry(
	ctx context.Context,
	client *http.Client,
	provider string,
	requestID string,
	modelCallID string,
	buildRequest func(context.Context) (*http.Request, error),
) (*http.Response, error) {
	return doProviderRequestWithAudit(ctx, client, provider, requestID, modelCallID, buildRequest, audit.Default())
}

func doProviderRequestWithAudit(
	ctx context.Context,
	client *http.Client,
	provider string,
	requestID string,
	modelCallID string,
	buildRequest func(context.Context) (*http.Request, error),
	observer *audit.Observer,
) (*http.Response, error) {
	if client == nil {
		client = http.DefaultClient
	}
	if observer == nil {
		observer = audit.Default()
	}
	httpReq, err := buildRequest(ctx)
	if err != nil {
		observer.Record(audit.Event{
			Kind:          "provider_request",
			Provider:      provider,
			ErrorCategory: "request_build",
		})
		return nil, err
	}
	startedAt := time.Now()
	targetHost := ""
	endpointKind := "custom"
	if httpReq.URL != nil {
		targetHost = audit.HostFromURL(httpReq.URL.String())
		endpointKind = audit.EndpointKind(httpReq.URL.String())
	}
	canaryMatched := matchProviderRequestCanary(observer, httpReq)
	observer.Record(audit.Event{
		Kind:          "provider_request",
		Provider:      provider,
		Endpoint:      endpointKind,
		TargetHost:    targetHost,
		RequestBytes:  requestContentLength(httpReq),
		CanaryMatched: canaryMatched,
		ScopeMatched:  canaryMatched,
	})
	resp, err := client.Do(httpReq)
	responseEvent := audit.Event{
		Kind:         "provider_response",
		Provider:     provider,
		Endpoint:     endpointKind,
		TargetHost:   targetHost,
		DurationMS:   time.Since(startedAt).Milliseconds(),
		ScopeMatched: canaryMatched,
	}
	if resp != nil {
		responseEvent.Status = resp.StatusCode
		if resp.ContentLength > 0 {
			responseEvent.ResponseBytes = resp.ContentLength
		}
	}
	if err != nil {
		responseEvent.ErrorCategory = "transport"
		if resp != nil && resp.Body != nil {
			_ = resp.Body.Close()
		}
		observer.Record(responseEvent)
		return nil, err
	}
	observer.Record(responseEvent)
	return resp, nil
}

const maxProviderCanaryInspectionBytes = 32 << 20

func matchProviderRequestCanary(observer *audit.Observer, request *http.Request) bool {
	if observer == nil || !observer.Enabled() || request == nil || request.GetBody == nil {
		return false
	}
	if request.ContentLength > maxProviderCanaryInspectionBytes {
		return false
	}
	body, err := request.GetBody()
	if err != nil {
		return false
	}
	defer body.Close()
	data, err := io.ReadAll(io.LimitReader(body, maxProviderCanaryInspectionBytes+1))
	if err != nil || len(data) > maxProviderCanaryInspectionBytes {
		return false
	}
	return observer.MatchCanary(data)
}
func requestContentLength(request *http.Request) int {
	if request == nil || request.ContentLength <= 0 {
		return 0
	}
	return int(request.ContentLength)
}

func ProviderRetryAttemptSummary(resp *http.Response) string {
	return ""
}
