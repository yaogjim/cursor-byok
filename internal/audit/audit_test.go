package audit

import (
	"bufio"
	"encoding/binary"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"cursor/gen/aiserverv1"

	"google.golang.org/protobuf/proto"
)

func TestObserverIsClosedByDefaultAndWritesRestrictedMetadata(t *testing.T) {
	path := filepath.Join(t.TempDir(), "audit.jsonl")
	observer, err := New(Options{FilePath: path, Canary: "synthetic-canary", MaxEvents: 1})
	if err != nil {
		t.Fatal(err)
	}
	observer.Record(Event{Kind: "prearm", TargetHost: "prearm-should-not-appear"})
	observer.Record(Event{Kind: "test", TargetHost: "example.invalid", RequestBytes: 4, CanaryMatched: true, ScopeMatched: true})
	observer.Record(Event{Kind: "ignored", TargetHost: "should-not-appear"})
	if err := observer.Close(); err != nil {
		t.Fatal(err)
	}

	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if got := info.Mode().Perm(); got != 0o600 {
		t.Fatalf("audit file mode = %o, want 600", got)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(data), "synthetic-canary") {
		t.Fatal("audit file persisted the canary")
	}
	if strings.Contains(string(data), "prearm-should-not-appear") {
		t.Fatal("audit observer persisted an event before canary arming")
	}
	if strings.Contains(string(data), "should-not-appear") {
		t.Fatal("audit observer exceeded event quota")
	}
	if lines := countLines(data); lines != 1 {
		t.Fatalf("audit line count = %d, want 1", lines)
	}
}

func TestSanitizeMetadataTextRemovesCredentialsAndQueries(t *testing.T) {
	input := `Post "https://example.test/v1/messages?api_key=query-secret&model=safe": Authorization: Bearer bearer-secret cookie=cookie-secret`
	output := SanitizeMetadataText(input)
	for _, secret := range []string{"query-secret", "bearer-secret", "cookie-secret", "api_key=query-secret"} {
		if strings.Contains(output, secret) {
			t.Fatalf("sanitized metadata retained %q: %s", secret, output)
		}
	}
	if strings.Contains(output, "?") {
		t.Fatalf("sanitized metadata retained query: %s", output)
	}
	if !strings.Contains(output, "https://example.test/v1/messages") {
		t.Fatalf("sanitized metadata dropped host/path: %s", output)
	}
}

func TestSanitizeMetadataTextRedactsUnlabeledAPIKeysAndJWT(t *testing.T) {
	const jwt = "eyJhbGciOiJIUzI1NiIsInR5cCI6IkpXVCJ9.eyJzdWIiOiIxMjM0NTY3ODkwIn0.dBjftJeZ4CVP-mB92K27uhbUJU1p1r_wW1gFWFOEjXk"
	input := "rejected sk-unlabeled-secret sk-proj-unlabeled-secret sk-ant-unlabeled-secret " + jwt
	output := SanitizeMetadataText(input)
	for _, secret := range []string{"sk-unlabeled-secret", "sk-proj-unlabeled-secret", "sk-ant-unlabeled-secret", jwt} {
		if strings.Contains(output, secret) {
			t.Fatalf("sanitized metadata retained %q: %s", secret, output)
		}
	}
	if !strings.Contains(output, "rejected") {
		t.Fatalf("sanitized metadata dropped surrounding text: %s", output)
	}
	if strings.Count(output, redactedMetadataValue) < 4 {
		t.Fatalf("expected unlabeled secrets redacted: %s", output)
	}
}

func TestRecordSanitizesErrorMessage(t *testing.T) {
	path := filepath.Join(t.TempDir(), "audit.jsonl")
	observer, err := New(Options{FilePath: path})
	if err != nil {
		t.Fatal(err)
	}
	observer.Record(Event{
		Kind:          "provider_response",
		ErrorCategory: "transport",
		ErrorMessage:  `Post "https://example.test/v1/messages?token=query-secret": Authorization: Bearer bearer-secret`,
		Attempt:       1,
		MaxAttempts:   1,
		RetryDecision: "single_request",
	})
	if err := observer.Close(); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	output := string(data)
	for _, secret := range []string{"query-secret", "bearer-secret", "token=query-secret"} {
		if strings.Contains(output, secret) {
			t.Fatalf("audit file retained %q: %s", secret, output)
		}
	}
	if !strings.Contains(output, `"error_category":"transport"`) || !strings.Contains(output, `"attempt":1`) {
		t.Fatalf("audit file missing structured error fields: %s", output)
	}
}

func TestObserverExpires(t *testing.T) {
	observer, err := New(Options{FilePath: filepath.Join(t.TempDir(), "audit.jsonl"), TTL: time.Millisecond})
	if err != nil {
		t.Fatal(err)
	}
	defer observer.Close()
	time.Sleep(5 * time.Millisecond)
	if observer.Enabled() {
		t.Fatal("expired observer remained enabled")
	}
}

func TestSummarizeProtoRequestRecordsPresenceAndCanaryOnly(t *testing.T) {
	observer, err := New(Options{FilePath: filepath.Join(t.TempDir(), "audit.jsonl"), Canary: "synthetic-canary"})
	if err != nil {
		t.Fatal(err)
	}
	model := "synthetic-canary"
	body, err := proto.Marshal(&aiserverv1.CppConfigRequest{Model: model})
	if err != nil {
		t.Fatal(err)
	}
	summary := observer.SummarizeProtoRequest("/aiserver.v1.AiService/CppConfig", "application/proto", body)
	if summary.DecodeError {
		messageType, resolveErr := resolveRequestType("/aiserver.v1.AiService/CppConfig")
		t.Fatalf("valid protobuf request was rejected: type=%v err=%v", messageType, resolveErr)
	}
	if summary.MessageType != "aiserver.v1.CppConfigRequest" {
		t.Fatalf("message type = %q", summary.MessageType)
	}
	if !containsString(summary.FieldPresence, "model") {
		t.Fatalf("field presence = %#v", summary.FieldPresence)
	}
	if summary.StringBytes["model"] != len(model) {
		t.Fatalf("model bytes = %d, want %d", summary.StringBytes["model"], len(model))
	}
	if !summary.CanaryMatched {
		t.Fatal("canary was not matched in memory")
	}
}

func TestSummarizeProtoRequestHandlesConnectEnvelope(t *testing.T) {
	const canary = "synthetic-connect-canary"
	observer, err := New(Options{FilePath: filepath.Join(t.TempDir(), "audit.jsonl"), Canary: canary})
	if err != nil {
		t.Fatal(err)
	}
	defer observer.Close()

	payload, err := proto.Marshal(&aiserverv1.CppConfigRequest{Model: canary})
	if err != nil {
		t.Fatal(err)
	}
	envelope := make([]byte, 5+len(payload))
	binary.BigEndian.PutUint32(envelope[1:5], uint32(len(payload)))
	copy(envelope[5:], payload)

	summary := observer.SummarizeProtoRequest("/aiserver.v1.AiService/CppConfig", "application/connect+proto", envelope)
	if summary.DecodeError || !summary.CanaryMatched {
		t.Fatalf("valid Connect envelope summary = %#v", summary)
	}
	if summary.RequestBytes != len(envelope) {
		t.Fatalf("request bytes = %d, want %d", summary.RequestBytes, len(envelope))
	}
}

func TestSummarizeProtoRequestRejectsUnsupportedConnectEnvelope(t *testing.T) {
	observer := &Observer{}
	tests := []struct {
		name string
		body []byte
	}{
		{name: "short header", body: []byte{0, 0, 0, 0}},
		{name: "invalid length", body: []byte{0, 0, 0, 0, 2, 1}},
		{name: "compressed", body: []byte{1, 0, 0, 0, 0}},
		{name: "end stream", body: []byte{2, 0, 0, 0, 0}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			summary := observer.SummarizeProtoRequest("/aiserver.v1.AiService/CppConfig", "application/connect+proto; charset=utf-8", test.body)
			if !summary.DecodeError {
				t.Fatalf("unsupported Connect envelope was decoded: %#v", summary)
			}
		})
	}
}

func countLines(data []byte) int {
	scanner := bufio.NewScanner(strings.NewReader(string(data)))
	count := 0
	for scanner.Scan() {
		count++
	}
	return count
}
