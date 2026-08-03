package contract

import "testing"

func TestSupportedSchemaVersions(t *testing.T) {
	if !IsSupportedSchemaVersion(1) || !IsSupportedSchemaVersion(2) {
		t.Fatal("schema v1 and v2 must be supported")
	}
	if IsSupportedSchemaVersion(0) || IsSupportedSchemaVersion(3) {
		t.Fatal("schemas outside v1..v2 must be rejected")
	}
}

func TestValidateEventSemantics(t *testing.T) {
	valid := Event{
		SchemaVersion:       2,
		Capability:          "tool",
		Operation:           "tool.result",
		Direction:           "proxy_to_cursor",
		SemanticOutcome:     "succeeded",
		ImplementationState: "implemented",
	}
	if err := ValidateEventSemantics(valid); err != nil {
		t.Fatalf("ValidateEventSemantics() valid error = %v", err)
	}
	invalid := valid
	invalid.Capability = "invented"
	if err := ValidateEventSemantics(invalid); err == nil {
		t.Fatal("ValidateEventSemantics() accepted unknown capability")
	}
	invalid = valid
	invalid.Severity = "critical"
	if err := ValidateEventSemantics(invalid); err == nil {
		t.Fatal("ValidateEventSemantics() accepted unknown severity")
	}
	legacy := invalid
	legacy.SchemaVersion = 1
	if err := ValidateEventSemantics(legacy); err != nil {
		t.Fatalf("ValidateEventSemantics() rejected legacy event: %v", err)
	}
}

func TestValidateManifestSemantics(t *testing.T) {
	if err := ValidateManifestSemantics(Manifest{SchemaVersion: 2, SourceKind: "client"}); err != nil {
		t.Fatalf("ValidateManifestSemantics() valid error = %v", err)
	}
	if err := ValidateManifestSemantics(Manifest{SchemaVersion: 2, SourceKind: "remote"}); err == nil {
		t.Fatal("ValidateManifestSemantics() accepted unknown source kind")
	}
}
