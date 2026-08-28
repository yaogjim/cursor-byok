package observability

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestNormalizeEventSemanticsProjectsSeverity(t *testing.T) {
	tests := []struct {
		name     string
		event    Event
		severity string
	}{
		{name: "success", event: Event{SemanticOutcome: OutcomeSucceeded}, severity: SeverityInfo},
		{name: "status error without category stays queryable and is not app error", event: Event{Status: "error", ErrorCategory: "client_error"}, severity: SeverityInfo},
		{name: "explicit warning wins over status error", event: Event{Status: "error", ErrorCategory: "client_error", Severity: SeverityWarning}, severity: SeverityWarning},
		{name: "client unknown ca", event: Event{Status: "error", ErrorCategory: "client_unknown_ca", SemanticOutcome: OutcomeFailed}, severity: SeverityWarning},
		{name: "handshake mismatch", event: Event{Status: "error", ErrorCategory: "handshake_mismatch"}, severity: SeverityWarning},
		{name: "filesync 404", event: Event{Status: "error", Capability: "filesync", ErrorCategory: "client_error", Fields: map[string]any{"status_code": 404}}, severity: SeverityWarning},
		{name: "provider failure not masked by filesync 404 fields", event: Event{Status: "error", Capability: "filesync", ErrorCategory: "provider_error", SemanticOutcome: OutcomeFailed, Fields: map[string]any{"status_code": 404}}, severity: SeverityError},
		{name: "explicit warning cannot mask provider_error", event: Event{Status: "error", Capability: "filesync", ErrorCategory: "provider_error", Severity: SeverityWarning, SemanticOutcome: OutcomeFailed, Fields: map[string]any{"status_code": 404}}, severity: SeverityError},
		{name: "explicit info cannot mask server_5xx", event: Event{Status: "error", ErrorCategory: "server_5xx", Severity: SeverityInfo}, severity: SeverityError},
		{name: "provider turn failure", event: Event{Status: "error", ErrorCategory: "provider_error", SemanticOutcome: OutcomeFailed}, severity: SeverityError},
		{name: "upstream tls", event: Event{Status: "error", ErrorCategory: "upstream_unknown_ca", SemanticOutcome: OutcomeFailed}, severity: SeverityError},
		{name: "backend unavailable", event: Event{Status: "error", ErrorCategory: "backend_unavailable"}, severity: SeverityError},
		{name: "timeout", event: Event{Status: "error", ErrorCategory: "timeout", SemanticOutcome: OutcomeTimeout}, severity: SeverityError},
		{name: "server 5xx", event: Event{Status: "error", ErrorCategory: "server_5xx", SemanticOutcome: OutcomeFailed}, severity: SeverityError},
		{name: "degraded skip", event: Event{Status: "degraded", SemanticOutcome: OutcomeDegraded}, severity: SeverityWarning},
		{name: "partial implementation", event: Event{ImplementationState: ImplementationPartial}, severity: SeverityWarning},
		{name: "compat outcome", event: Event{SemanticOutcome: OutcomeCompatOnly}, severity: SeverityWarning},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := normalizeEventSemantics(test.event).Severity; got != test.severity {
				t.Fatalf("Severity = %q, want %q", got, test.severity)
			}
		})
	}

	normalized := normalizeEventSemantics(Event{
		Capability:          "invented",
		Operation:           "Invalid Operation",
		Direction:           "sideways",
		SemanticOutcome:     "maybe",
		ImplementationState: "stubbed",
	})
	if normalized.Capability != "unknown" || normalized.Operation != "unknown.operation" || normalized.Direction != "" || normalized.SemanticOutcome != OutcomeUnknown || normalized.ImplementationState != ImplementationUnknown {
		t.Fatalf("unexpected normalized semantics: %+v", normalized)
	}
}

func TestClassifyRequestCapabilityAndOperation(t *testing.T) {
	tests := []struct {
		path       string
		capability string
	}{
		{path: "/aiserver.v1.FileSyncService/UnknownOp", capability: "filesync"},
		{path: "file_sync", capability: "filesync"},
		{path: "/aiserver.v1.AiService/WriteGitCommitMessage", capability: "git"},
		{path: "ai_write_git_commit_message", capability: "git"},
		{path: "/aiserver.v1.RepositoryService/FastUpdateFileV2", capability: "repository"},
		{path: "repository_fast_update_file_v2", capability: "repository"},
		{path: "/aiserver.v1.BidiService/BidiAppend", capability: "unknown"},
		{path: "/unknown/resource", capability: "unknown"},
	}
	for _, test := range tests {
		if got := ClassifyRequestCapability(test.path); got != test.capability {
			t.Fatalf("ClassifyRequestCapability(%q) = %q, want %q", test.path, got, test.capability)
		}
	}
	if got := ClassifyRequestOperation("filesync", "transport.request"); got != "filesync.request" {
		t.Fatalf("filesync operation = %q", got)
	}
	if got := ClassifyRequestOperation("unknown", "transport.request"); got != "transport.request" {
		t.Fatalf("unknown operation = %q", got)
	}
}

func TestDeriveProjectIDIsStableAndDoesNotContainPaths(t *testing.T) {
	key := []byte("01234567890123456789012345678901")
	first := filepath.Join(t.TempDir(), "first")
	second := filepath.Join(t.TempDir(), "second")
	id := deriveProjectID(key, []string{second, first, first})
	reordered := deriveProjectID(key, []string{first, second})
	if id == "" || id != reordered {
		t.Fatalf("project IDs are not stable: %q != %q", id, reordered)
	}
	if strings.Contains(id, first) || strings.Contains(id, second) {
		t.Fatalf("project ID contains source path: %q", id)
	}
	if other := deriveProjectID([]byte("abcdefghijklmnopqrstuvwxyz123456"), []string{first, second}); other == id {
		t.Fatalf("different machine key produced same project ID: %q", id)
	}
}

func TestRecorderWritesV2SemanticsProjectAndManifestMetadata(t *testing.T) {
	root := t.TempDir()
	workspacePath := filepath.Join(t.TempDir(), "private-workspace")
	metadata := SessionMetadata{
		SourceKind:        "client",
		AppVersion:        "1.2.3",
		BuildID:           "revision-7",
		Platform:          "test-platform",
		ConfigFingerprint: "config-fingerprint",
	}
	recorder, err := NewRecorder(root, Settings{Mode: ModeBasic, RetentionDays: 7, MaxDiskMB: 64, Metadata: metadata})
	if err != nil {
		t.Fatalf("NewRecorder() error = %v", err)
	}
	status := recorder.Status()
	ctx := WithCorrelation(context.Background(), Correlation{
		ConversationID: "conversation-1",
		TurnID:         "conversation-1:4",
		TurnSequence:   4,
	})
	if !recorder.Record(ctx, Capture{
		Event: Event{
			Layer:               "runtime",
			Event:               "tool_result",
			Capability:          "tool",
			Operation:           "tool.result",
			Direction:           DirectionProxyToCursor,
			SemanticOutcome:     OutcomePartial,
			ImplementationState: ImplementationPartial,
		},
		ProjectPaths: []string{workspacePath},
	}) {
		t.Fatal("v2 event was not accepted")
	}
	if err := recorder.Close(); err != nil {
		t.Fatalf("Close() error = %v", err)
	}

	payload, err := os.ReadFile(filepath.Join(status.SessionPath, eventsFilename))
	if err != nil {
		t.Fatalf("read events: %v", err)
	}
	if strings.Contains(string(payload), workspacePath) {
		t.Fatalf("basic event leaked workspace path: %s", payload)
	}
	var event Event
	if err := json.Unmarshal([]byte(strings.TrimSpace(string(payload))), &event); err != nil {
		t.Fatalf("decode event: %v", err)
	}
	if event.SchemaVersion != 2 || !strings.HasPrefix(event.ProjectID, "project_") || event.TurnSequence != 4 || event.Severity != SeverityWarning {
		t.Fatalf("unexpected v2 event: %+v", event)
	}

	manifest, err := readManifest(filepath.Join(status.SessionPath, manifestFilename))
	if err != nil {
		t.Fatalf("read manifest: %v", err)
	}
	if manifest.SchemaVersion != 2 || manifest.SourceKind != metadata.SourceKind || manifest.AppVersion != metadata.AppVersion || manifest.BuildID != metadata.BuildID || manifest.Platform != metadata.Platform || manifest.ConfigFingerprint != metadata.ConfigFingerprint {
		t.Fatalf("unexpected v2 manifest: %+v", manifest)
	}
	assertPrivatePermissions(t, filepath.Join(root, projectKeyFilename), 0o600)
}
