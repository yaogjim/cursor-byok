// provider_failure.go 把 provider 错误收敛为 typed 分类，供同渠道重试与 fallback 共用。
package modeladapter

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"io"
	"net"
	"net/http"
	"strings"
)

const (
	FailureCauseHTTP500           = "http_500"
	FailureCauseHTTP502           = "http_502"
	FailureCauseHTTP503           = "http_503"
	FailureCauseHTTP504           = "http_504"
	FailureCauseHTTP524           = "http_524"
	FailureCauseHTTP429           = "http_429"
	FailureCauseHTTP401           = "http_401"
	FailureCauseHTTP403           = "http_403"
	FailureCauseHTTP4xx           = "http_4xx"
	FailureCauseHTTP529           = "http_529"
	FailureCauseHTTPStatus        = "http_status"
	FailureCauseTransport         = "transport"
	FailureCauseTLSHandshakeEOF   = "tls_handshake_eof"
	FailureCauseTLSVerify         = "tls_verify"
	FailureCauseDNSPermanent      = "dns_permanent"
	FailureCauseConnectTimeout    = "connect_timeout"
	FailureCauseFirstEventTimeout = "first_event_timeout"
	FailureCauseStreamIdleTimeout = "stream_idle_timeout"
	FailureCauseCallTimeout       = "call_timeout"
	FailureCauseContextCanceled   = "context_canceled"
	FailureCauseRequestBuild      = "request_build"
	FailureCauseProtocolDecode    = "protocol_decode"
	FailureCauseProviderTerminal  = "provider_terminal"
	FailureCauseCapacity          = "capacity_unavailable"
	FailureCauseStreamTruncated   = "stream_truncated"

	LivenessPhaseConnect    = "connect"
	LivenessPhaseFirstEvent = "first_event"
	LivenessPhaseIdle       = "stream_idle"
	LivenessPhaseCall       = "call"
	LivenessPhaseHTTP       = "http"
	LivenessPhaseTransport  = "transport"
	LivenessPhaseStream     = "stream"
)

// ProviderFailure 是一次失败的稳定分类，不含密钥、URL query、正文或账号标识。
type ProviderFailure struct {
	Category   string
	Cause      string
	Phase      string
	Status     int
	Retryable  bool
	Switchable bool
}

func classifyProviderFailure(err error, status int) ProviderFailure {
	if err == nil && status == 0 {
		return ProviderFailure{}
	}
	if err != nil {
		if failure, ok := classifyTypedProviderFailure(err); ok {
			return failure
		}
	}
	if status > 0 {
		return classifyHTTPStatusFailure(status)
	}
	if err == nil {
		return ProviderFailure{}
	}
	return ProviderFailure{
		Category:   ClassifyProviderError(err),
		Cause:      FailureCauseTransport,
		Phase:      LivenessPhaseTransport,
		Retryable:  true,
		Switchable: true,
	}
}

func classifyTypedProviderFailure(err error) (ProviderFailure, bool) {
	if err == nil {
		return ProviderFailure{}, false
	}

	var live *LivenessTimeoutError
	if errors.As(err, &live) && live != nil {
		return classifyLivenessTimeout(*live), true
	}

	var idle *StreamIdleTimeoutError
	if errors.As(err, &idle) {
		return ProviderFailure{
			Category:   ProviderErrorStreamIdleTimeout,
			Cause:      FailureCauseStreamIdleTimeout,
			Phase:      LivenessPhaseIdle,
			Retryable:  false,
			Switchable: false,
		}, true
	}

	if isParentContextError(err) {
		return ProviderFailure{
			Category:   ProviderErrorContextCanceled,
			Cause:      FailureCauseContextCanceled,
			Phase:      LivenessPhaseCall,
			Retryable:  false,
			Switchable: false,
		}, true
	}

	var httpErr *HTTPStatusError
	if errors.As(err, &httpErr) && httpErr != nil {
		failure := classifyHTTPStatusFailure(httpErr.StatusCode)
		failure.Status = httpErr.StatusCode
		return failure, true
	}

	if isCapacityUnavailable(err) {
		return ProviderFailure{
			Category:   ProviderErrorCapacityUnavailable,
			Cause:      FailureCauseCapacity,
			Phase:      LivenessPhaseHTTP,
			Retryable:  false,
			Switchable: true,
		}, true
	}

	var terminal *ProviderTerminalStatusError
	if errors.As(err, &terminal) && terminal != nil {
		return ProviderFailure{
			Category:   ProviderErrorTerminal,
			Cause:      FailureCauseProviderTerminal,
			Phase:      LivenessPhaseStream,
			Retryable:  false,
			Switchable: false,
		}, true
	}

	var buildErr *RequestBuildError
	if errors.As(err, &buildErr) && buildErr != nil {
		return ProviderFailure{
			Category:   ProviderErrorRequestBuild,
			Cause:      FailureCauseRequestBuild,
			Phase:      LivenessPhaseHTTP,
			Retryable:  false,
			Switchable: false,
		}, true
	}

	var trunc *StreamTruncatedError
	if errors.As(err, &trunc) && trunc != nil {
		if trunc.RawBytesObserved {
			return ProviderFailure{
				Category:   ProviderErrorStreamDecode,
				Cause:      FailureCauseProtocolDecode,
				Phase:      LivenessPhaseStream,
				Retryable:  false,
				Switchable: false,
			}, true
		}
		if IsRecoverableTruncatedStreamError(err) {
			return ProviderFailure{
				Category:   ProviderErrorStreamDecode,
				Cause:      FailureCauseStreamTruncated,
				Phase:      LivenessPhaseStream,
				Retryable:  true,
				Switchable: true,
			}, true
		}
		return ProviderFailure{
			Category:   ProviderErrorStreamDecode,
			Cause:      FailureCauseProtocolDecode,
			Phase:      LivenessPhaseStream,
			Retryable:  false,
			Switchable: false,
		}, true
	}

	if isPermanentTLSError(err) {
		return ProviderFailure{
			Category:   ProviderErrorTransport,
			Cause:      FailureCauseTLSVerify,
			Phase:      LivenessPhaseConnect,
			Retryable:  false,
			Switchable: false,
		}, true
	}
	if isPermanentDNSError(err) {
		return ProviderFailure{
			Category:   ProviderErrorTransport,
			Cause:      FailureCauseDNSPermanent,
			Phase:      LivenessPhaseConnect,
			Retryable:  false,
			Switchable: false,
		}, true
	}
	if isTLSHandshakeEOF(err) {
		return ProviderFailure{
			Category:   ProviderErrorTransport,
			Cause:      FailureCauseTLSHandshakeEOF,
			Phase:      LivenessPhaseConnect,
			Retryable:  true,
			Switchable: true,
		}, true
	}
	return ProviderFailure{}, false
}

func classifyHTTPStatusFailure(status int) ProviderFailure {
	failure := ProviderFailure{Status: status, Phase: LivenessPhaseHTTP, Category: ClassifyHTTPStatus(status)}
	switch status {
	case http.StatusInternalServerError:
		failure.Cause = FailureCauseHTTP500
		failure.Retryable = true
		failure.Switchable = true
	case http.StatusBadGateway:
		failure.Cause = FailureCauseHTTP502
		failure.Retryable = true
		failure.Switchable = true
	case http.StatusServiceUnavailable:
		failure.Cause = FailureCauseHTTP503
		failure.Retryable = true
		failure.Switchable = true
	case http.StatusGatewayTimeout:
		failure.Cause = FailureCauseHTTP504
		failure.Retryable = true
		failure.Switchable = true
	case HTTPStatusCloudflareTimeout:
		failure.Cause = FailureCauseHTTP524
		failure.Retryable = true
		failure.Switchable = true
	case http.StatusTooManyRequests:
		failure.Cause = FailureCauseHTTP429
		failure.Retryable = true
		failure.Switchable = true
	case http.StatusUnauthorized:
		failure.Cause = FailureCauseHTTP401
		failure.Retryable = false
		failure.Switchable = false
	case http.StatusForbidden:
		failure.Cause = FailureCauseHTTP403
		failure.Retryable = false
		failure.Switchable = false
	case 529:
		failure.Cause = FailureCauseHTTP529
		failure.Retryable = false
		failure.Switchable = false
	default:
		if status >= http.StatusBadRequest && status < http.StatusInternalServerError {
			failure.Cause = FailureCauseHTTP4xx
		} else {
			failure.Cause = FailureCauseHTTPStatus
		}
		failure.Retryable = false
		failure.Switchable = false
	}
	return failure
}

func classifyLivenessTimeout(err LivenessTimeoutError) ProviderFailure {
	switch err.Phase {
	case LivenessPhaseConnect:
		return ProviderFailure{
			Category:   ProviderErrorTransport,
			Cause:      FailureCauseConnectTimeout,
			Phase:      LivenessPhaseConnect,
			Retryable:  true,
			Switchable: true,
		}
	case LivenessPhaseFirstEvent:
		return ProviderFailure{
			Category:   ProviderErrorTransport,
			Cause:      FailureCauseFirstEventTimeout,
			Phase:      LivenessPhaseFirstEvent,
			Retryable:  true,
			Switchable: true,
		}
	case LivenessPhaseIdle:
		return ProviderFailure{
			Category:   ProviderErrorStreamIdleTimeout,
			Cause:      FailureCauseStreamIdleTimeout,
			Phase:      LivenessPhaseIdle,
			Retryable:  false,
			Switchable: false,
		}
	default:
		return ProviderFailure{
			Category:   ProviderErrorContextCanceled,
			Cause:      FailureCauseCallTimeout,
			Phase:      LivenessPhaseCall,
			Retryable:  false,
			Switchable: false,
		}
	}
}

func isParentContextError(err error) bool {
	if err == nil {
		return false
	}
	var live *LivenessTimeoutError
	if errors.As(err, &live) {
		return false
	}
	var idle *StreamIdleTimeoutError
	if errors.As(err, &idle) {
		return false
	}
	return errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded)
}

func isPermanentTLSError(err error) bool {
	if err == nil {
		return false
	}
	var verify *tls.CertificateVerificationError
	if errors.As(err, &verify) {
		return true
	}
	var unknownAuth x509.UnknownAuthorityError
	if errors.As(err, &unknownAuth) {
		return true
	}
	var hostname x509.HostnameError
	if errors.As(err, &hostname) {
		return true
	}
	var certInvalid x509.CertificateInvalidError
	if errors.As(err, &certInvalid) {
		return true
	}
	var sysRoots x509.SystemRootsError
	return errors.As(err, &sysRoots)
}

func isPermanentDNSError(err error) bool {
	var dnsErr *net.DNSError
	if !errors.As(err, &dnsErr) || dnsErr == nil {
		return false
	}
	if dnsErr.IsTimeout || dnsErr.IsTemporary {
		return false
	}
	if dnsErr.IsNotFound {
		return true
	}
	return !dnsErr.Temporary()
}

func isTLSHandshakeEOF(err error) bool {
	if err == nil || isPermanentTLSError(err) {
		return false
	}
	if !errors.Is(err, io.EOF) && !errors.Is(err, io.ErrUnexpectedEOF) {
		return false
	}
	if isTypedTLSError(err) {
		return true
	}
	var opErr *net.OpError
	if errors.As(err, &opErr) && opErr != nil {
		op := strings.ToLower(strings.TrimSpace(opErr.Op))
		if strings.Contains(op, "handshake") || op == "remote error" || op == "local error" {
			return true
		}
	}
	text := strings.ToLower(err.Error())
	return strings.Contains(text, "tls") && strings.Contains(text, "handshake")
}

func httpStatusFromError(err error) int {
	var httpErr *HTTPStatusError
	if errors.As(err, &httpErr) && httpErr != nil {
		return httpErr.StatusCode
	}
	return 0
}
