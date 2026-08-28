package analyze

import (
	"context"
	"fmt"
	"strings"

	"cursor-log-analyzer/internal/workspace"
)

type expectedNoiseBuffer struct {
	datasetID int64
	buckets   map[string]*expectedNoiseBucket
}

type expectedNoiseBucket struct {
	capability       string
	operation        string
	status           string
	count            int
	firstIngestOrder int64
}

func newExpectedNoiseBuffer(datasetID int64) *expectedNoiseBuffer {
	return &expectedNoiseBuffer{datasetID: datasetID, buckets: make(map[string]*expectedNoiseBucket)}
}

func (buffer *expectedNoiseBuffer) add(event workspace.EventRecord) {
	if buffer == nil || !isExpectedNoise(event) {
		return
	}
	capability := firstNonEmpty(event.Capability, "unknown")
	operation := firstNonEmpty(event.Operation, "unknown")
	status := firstNonEmpty(event.Status, "error")
	key := capability + "|" + operation + "|" + status
	bucket := buffer.buckets[key]
	if bucket == nil {
		bucket = &expectedNoiseBucket{
			capability:       capability,
			operation:        operation,
			status:           status,
			firstIngestOrder: event.IngestOrder,
		}
		buffer.buckets[key] = bucket
	}
	bucket.count++
	if event.IngestOrder < bucket.firstIngestOrder {
		bucket.firstIngestOrder = event.IngestOrder
	}
}

func (buffer *expectedNoiseBuffer) flush(ctx context.Context, store *workspace.Workspace) error {
	if buffer == nil || store == nil {
		return nil
	}
	for _, bucket := range buffer.buckets {
		if bucket == nil || bucket.count == 0 {
			continue
		}
		if err := store.InsertFinding(ctx, workspace.FindingRecord{
			DatasetID:        buffer.datasetID,
			Severity:         "warning",
			SeverityRank:     severityRank("warning"),
			Code:             "expected_noise",
			Message:          fmt.Sprintf("%s/%s status=%s", bucket.capability, bucket.operation, bucket.status),
			TraceKey:         bucket.capability + ":" + bucket.operation + ":" + bucket.status,
			Count:            bucket.count,
			FirstIngestOrder: bucket.firstIngestOrder,
		}); err != nil {
			return err
		}
	}
	buffer.buckets = make(map[string]*expectedNoiseBucket)
	return nil
}

func isExpectedNoise(event workspace.EventRecord) bool {
	if isRealFailureCategory(event.ErrorCategory) {
		return false
	}
	if isClientHandshakeNoise(event.ErrorCategory) {
		return true
	}
	return isExpectedNotFound(event)
}

func isRealFailureCategory(category string) bool {
	category = strings.ToLower(strings.TrimSpace(category))
	switch category {
	case "upstream_unknown_ca", "upstream_remote_unknown_certificate", "upstream_tls_handshake_failed",
		"hostname_mismatch", "upstream_http2", "backend_unavailable", "server_error", "timeout",
		"server_5xx", "transport", "stream_idle_timeout", "stream_decode", "provider_error",
		"mitm_tls_config_failed", "handler_error":
		return true
	default:
		return strings.HasPrefix(category, "upstream_") || strings.Contains(category, "provider")
	}
}

func isClientHandshakeNoise(category string) bool {
	switch strings.ToLower(strings.TrimSpace(category)) {
	case "client_unknown_ca", "handshake_mismatch", "client_tls_handshake_failed":
		return true
	default:
		return false
	}
}

func isExpectedNotFound(event workspace.EventRecord) bool {
	fields := parseSafeFields(event.SafeFieldsJSON)
	code, ok := fieldInt(fields, "status_code")
	if !ok || code != 404 {
		return false
	}
	return isSCMOrFileSync(event, fields)
}

func isSCMOrFileSync(event workspace.EventRecord, fields map[string]any) bool {
	switch strings.ToLower(strings.TrimSpace(event.Capability)) {
	case "git", "filesync", "repository":
		return true
	}
	blob := strings.ToLower(strings.Join([]string{
		event.Route,
		event.Operation,
		fieldString(fields, "path"),
		fieldString(fields, "traffic_class"),
	}, " "))
	return strings.Contains(blob, "filesync") ||
		strings.Contains(blob, "file_sync") ||
		strings.Contains(blob, "writegit") ||
		strings.Contains(blob, "gitservice") ||
		strings.Contains(blob, "repositoryservice") ||
		strings.Contains(blob, "scm")
}

func shouldEmitRequestError(event workspace.EventRecord) bool {
	if event.ErrorCategory == "" && !strings.EqualFold(event.Status, "error") {
		return false
	}
	return !isExpectedNoise(event)
}
