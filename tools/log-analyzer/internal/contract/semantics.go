package contract

import (
	"fmt"
	"regexp"
	"strings"
)

var operationPattern = regexp.MustCompile(`^[a-z][a-z0-9_]*(\.[a-z][a-z0-9_]*)+$`)

func IsSupportedSchemaVersion(version int) bool {
	return version >= MinimumSupportedSchemaVersion && version <= SupportedSchemaVersion
}

func ValidateEventSemantics(event Event) error {
	if event.SchemaVersion != 2 {
		return nil
	}
	checks := []struct {
		field string
		value string
		valid func(string) bool
	}{
		{field: "capability", value: event.Capability, valid: validCapability},
		{field: "operation", value: event.Operation, valid: validOperation},
		{field: "direction", value: event.Direction, valid: validDirection},
		{field: "semantic_outcome", value: event.SemanticOutcome, valid: validSemanticOutcome},
		{field: "implementation_state", value: event.ImplementationState, valid: validImplementationState},
		{field: "severity", value: event.Severity, valid: validSeverity},
	}
	for _, check := range checks {
		value := strings.TrimSpace(check.value)
		if value != "" && !check.valid(value) {
			return fmt.Errorf("invalid %s=%q", check.field, value)
		}
	}
	return nil
}

func ValidateManifestSemantics(manifest Manifest) error {
	if manifest.SchemaVersion != 2 || strings.TrimSpace(manifest.SourceKind) == "" {
		return nil
	}
	switch strings.TrimSpace(manifest.SourceKind) {
	case "client", "relay", "imported":
		return nil
	default:
		return fmt.Errorf("invalid source_kind=%q", manifest.SourceKind)
	}
}

func validCapability(value string) bool {
	switch value {
	case "agent", "provider", "tool", "repository", "docs", "upload", "tab", "filesync", "git", "config", "update", "unknown":
		return true
	default:
		return false
	}
}

func validOperation(value string) bool {
	return operationPattern.MatchString(value)
}

func validDirection(value string) bool {
	switch value {
	case "cursor_to_proxy", "proxy_internal", "proxy_to_provider", "proxy_to_upstream", "proxy_to_cursor":
		return true
	default:
		return false
	}
}

func validSemanticOutcome(value string) bool {
	switch value {
	case "started", "succeeded", "failed", "canceled", "timeout", "degraded", "unsupported", "partial", "compat_only", "unknown":
		return true
	default:
		return false
	}
}

func validSeverity(value string) bool {
	switch value {
	case "info", "warning", "error":
		return true
	default:
		return false
	}
}

func validImplementationState(value string) bool {
	switch value {
	case "implemented", "partial", "compat", "unsupported", "unknown":
		return true
	default:
		return false
	}
}
