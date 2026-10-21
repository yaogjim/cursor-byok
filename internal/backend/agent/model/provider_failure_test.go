package modeladapter

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"testing"
	"time"
)

func TestClassifyHTTP500IsRetryableAndSwitchable(t *testing.T) {
	failure := classifyProviderFailure(&HTTPStatusError{StatusCode: http.StatusInternalServerError}, http.StatusInternalServerError)
	if failure.Category != ProviderErrorServer5xx || failure.Cause != FailureCauseHTTP500 {
		t.Fatalf("500 classification = %+v", failure)
	}
	if !failure.Retryable || !failure.Switchable {
		t.Fatalf("500 should retry then switch, got %+v", failure)
	}
}

func TestClassifyTLSHandshakeEOFVsPermanentTLS(t *testing.T) {
	eof := fmt.Errorf("tls: handshake error: %w", io.EOF)
	failure := classifyProviderFailure(eof, 0)
	if failure.Cause != FailureCauseTLSHandshakeEOF || !failure.Retryable || !failure.Switchable {
		t.Fatalf("handshake EOF = %+v", failure)
	}

	permanent := x509.UnknownAuthorityError{}
	failure = classifyProviderFailure(permanent, 0)
	if failure.Cause != FailureCauseTLSVerify || failure.Retryable || failure.Switchable {
		t.Fatalf("permanent TLS = %+v", failure)
	}

	verify := &tls.CertificateVerificationError{Err: errors.New("verify failed")}
	failure = classifyProviderFailure(verify, 0)
	if failure.Cause != FailureCauseTLSVerify || failure.Retryable {
		t.Fatalf("cert verify = %+v", failure)
	}
}

func TestClassifyPermanentDNSIsFailFast(t *testing.T) {
	err := &net.DNSError{Err: "no such host", Name: "missing.example", IsNotFound: true}
	failure := classifyProviderFailure(err, 0)
	if failure.Cause != FailureCauseDNSPermanent || failure.Retryable || failure.Switchable {
		t.Fatalf("permanent DNS = %+v", failure)
	}
}

func TestClassifyParentCancelIsFailFast(t *testing.T) {
	failure := classifyProviderFailure(context.Canceled, 0)
	if failure.Cause != FailureCauseContextCanceled || failure.Retryable || failure.Switchable {
		t.Fatalf("parent cancel = %+v", failure)
	}
	failure = classifyProviderFailure(context.DeadlineExceeded, 0)
	if failure.Retryable || failure.Switchable {
		t.Fatalf("parent deadline = %+v", failure)
	}
}

func TestClassifyGatewayConnectAndFirstEventTimeouts(t *testing.T) {
	connect := &LivenessTimeoutError{Phase: LivenessPhaseConnect, Timeout: 30 * time.Second}
	failure := classifyProviderFailure(connect, 0)
	if failure.Cause != FailureCauseConnectTimeout || !failure.Retryable || !failure.Switchable {
		t.Fatalf("connect timeout = %+v", failure)
	}
	first := &LivenessTimeoutError{Phase: LivenessPhaseFirstEvent, Timeout: defaultFirstEventTimeout}
	failure = classifyProviderFailure(first, 0)
	if failure.Cause != FailureCauseFirstEventTimeout || !failure.Retryable || !failure.Switchable {
		t.Fatalf("first event timeout = %+v", failure)
	}
}

func TestSafetyGateBlocksReplayAndSwitch(t *testing.T) {
	cases := []FallbackSafetySnapshot{
		{RawBytesObserved: true},
		{ModelEventObserved: true},
		{SideEffectObserved: true},
	}
	failure := classifyHTTPStatusFailure(http.StatusInternalServerError)
	for _, safety := range cases {
		if sameChannelRetryable(failure, 1, 2, safety) {
			t.Fatalf("same-channel retry allowed for %+v", safety)
		}
		if fallbackSwitchable(failure, safety) {
			t.Fatalf("switch allowed for %+v", safety)
		}
		if !safety.BlocksReplayOrSwitch() {
			t.Fatalf("BlocksReplayOrSwitch=false for %+v", safety)
		}
	}
}

func TestProviderFailureActionMatrix(t *testing.T) {
	cases := []struct {
		name       string
		err        error
		status     int
		retryable  bool
		switchable bool
		cause      string
	}{
		{"429", &HTTPStatusError{StatusCode: http.StatusTooManyRequests}, http.StatusTooManyRequests, true, true, FailureCauseHTTP429},
		{"500", &HTTPStatusError{StatusCode: http.StatusInternalServerError}, http.StatusInternalServerError, true, true, FailureCauseHTTP500},
		{"502", &HTTPStatusError{StatusCode: http.StatusBadGateway}, http.StatusBadGateway, true, true, FailureCauseHTTP502},
		{"503", &HTTPStatusError{StatusCode: http.StatusServiceUnavailable}, http.StatusServiceUnavailable, true, true, FailureCauseHTTP503},
		{"504", &HTTPStatusError{StatusCode: http.StatusGatewayTimeout}, http.StatusGatewayTimeout, true, true, FailureCauseHTTP504},
		{"524", &HTTPStatusError{StatusCode: HTTPStatusCloudflareTimeout}, HTTPStatusCloudflareTimeout, true, true, FailureCauseHTTP524},
		{"529", &HTTPStatusError{StatusCode: 529}, 529, false, false, FailureCauseHTTP529},
		{"401", &HTTPStatusError{StatusCode: http.StatusUnauthorized}, http.StatusUnauthorized, false, false, FailureCauseHTTP401},
		{"403", &HTTPStatusError{StatusCode: http.StatusForbidden}, http.StatusForbidden, false, false, FailureCauseHTTP403},
		{"tls_eof", fmt.Errorf("tls: handshake error: %w", io.EOF), 0, true, true, FailureCauseTLSHandshakeEOF},
		{"tls_cert", x509.UnknownAuthorityError{}, 0, false, false, FailureCauseTLSVerify},
		{"parent_cancel", context.Canceled, 0, false, false, FailureCauseContextCanceled},
		{"connect_timeout", &LivenessTimeoutError{Phase: LivenessPhaseConnect, Timeout: time.Second}, 0, true, true, FailureCauseConnectTimeout},
		{"first_event_timeout", &LivenessTimeoutError{Phase: LivenessPhaseFirstEvent, Timeout: time.Second}, 0, true, true, FailureCauseFirstEventTimeout},
		{"idle_timeout", &StreamIdleTimeoutError{Timeout: time.Second}, 0, false, false, FailureCauseStreamIdleTimeout},
		{"call_timeout", &LivenessTimeoutError{Phase: LivenessPhaseCall, Timeout: time.Second}, 0, false, false, FailureCauseCallTimeout},
		{"request_build", &RequestBuildError{Err: errors.New("marshal")}, 0, false, false, FailureCauseRequestBuild},
		{"decode", &StreamTruncatedError{RawBytesObserved: true}, 0, false, false, FailureCauseProtocolDecode},
		{"provider_terminal", &ProviderTerminalStatusError{Status: "failed"}, 0, false, false, FailureCauseProviderTerminal},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			failure := classifyProviderFailure(test.err, test.status)
			if failure.Cause != test.cause || failure.Retryable != test.retryable || failure.Switchable != test.switchable {
				t.Fatalf("got %+v, want cause=%s retryable=%v switchable=%v", failure, test.cause, test.retryable, test.switchable)
			}
		})
	}
}
