package config

import (
	"strings"
	"testing"
)

func validYAML(overrides string) string {
	base := "" +
		"schemaVersion: 1\n" +
		"agentPath: /usr/bin/agent\n" +
		"endpoint: http://127.0.0.1:18090\n" +
		"models:\n" +
		"  - 0123456789abcdef\n" +
		"mode: ask\n"
	return base + overrides
}

func TestParseAcceptsClosedSetAndDefaultPrefix(t *testing.T) {
	cfg, err := Parse([]byte(validYAML("")))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if cfg.WorktreeNamePrefix != DefaultWorktreePrefix {
		t.Fatalf("prefix = %q", cfg.WorktreeNamePrefix)
	}
	if cfg.Safety.AllowWrite {
		t.Fatal("allowWrite default must be false")
	}
	if cfg.Models[0] != "0123456789abcdef" {
		t.Fatalf("model = %q", cfg.Models[0])
	}
}

func TestParseRejectsUnknownField(t *testing.T) {
	_, err := Parse([]byte(validYAML("unknown: true\n")))
	if err == nil || !strings.Contains(err.Error(), "未知字段") {
		t.Fatalf("expected unknown field error, got %v", err)
	}
}

func TestParseRejectsForceYoloPrintenvAndCredentials(t *testing.T) {
	for _, extra := range []string{"force: true\n", "yolo: true\n", "printenv: true\n", "apiKey: secret\n", "credential: x\n"} {
		_, err := Parse([]byte(validYAML(extra)))
		if err == nil {
			t.Fatalf("expected reject for %q", extra)
		}
		if strings.Contains(err.Error(), "secret") {
			t.Fatalf("error leaked credential: %v", err)
		}
	}
}

func TestParseRejectsRelativeAgentPath(t *testing.T) {
	raw := strings.Replace(validYAML(""), "/usr/bin/agent", "agent", 1)
	if _, err := Parse([]byte(raw)); err == nil || !strings.Contains(err.Error(), "绝对路径") {
		t.Fatalf("expected relative agentPath reject, got %v", err)
	}
}

func TestParseRejectsWrongEndpointAndSchema(t *testing.T) {
	_, err := Parse([]byte(strings.Replace(validYAML(""), "http://127.0.0.1:18090", "https://example.com", 1)))
	if err == nil {
		t.Fatal("expected endpoint reject")
	}
	_, err = Parse([]byte(strings.Replace(validYAML(""), "schemaVersion: 1", "schemaVersion: 2", 1)))
	if err == nil {
		t.Fatal("expected schema reject")
	}
}

func TestParseWriteRequiresAllowWrite(t *testing.T) {
	raw := strings.Replace(validYAML(""), "mode: ask", "mode: write", 1)
	_, err := Parse([]byte(raw))
	if err == nil {
		t.Fatal("write without allowWrite must fail")
	}
	raw += "safety:\n  allowWrite: true\n"
	cfg, err := Parse([]byte(raw))
	if err != nil {
		t.Fatalf("write with allowWrite: %v", err)
	}
	if !cfg.Safety.AllowWrite || cfg.Mode != ModeWrite {
		t.Fatalf("cfg = %+v", cfg)
	}
}

func TestParseRejectsDuplicateAndInvalidModelIDs(t *testing.T) {
	dup := strings.Replace(validYAML(""), "  - 0123456789abcdef\n", "  - 0123456789abcdef\n  - 0123456789abcdef\n", 1)
	_, err := Parse([]byte(dup))
	if err == nil {
		t.Fatal("expected duplicate model reject")
	}
	bad := strings.Replace(validYAML(""), "0123456789abcdef", "gpt-4", 1)
	_, err = Parse([]byte(bad))
	if err == nil {
		t.Fatal("expected non-hex model reject")
	}
}

func TestParseRejectsUnknownSafetyField(t *testing.T) {
	_, err := Parse([]byte(validYAML("safety:\n  allowWrite: false\n  yolo: true\n")))
	if err == nil {
		t.Fatal("expected safety yolo reject")
	}
}
