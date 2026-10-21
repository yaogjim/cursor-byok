package modeladapter

import (
	"testing"
	"time"
)

func TestNormalizeRecoverySettingsDefaults(t *testing.T) {
	got := NormalizeRecoverySettings(RecoverySettings{})
	if got.MaxTotalAttempts != 5 || got.MaxAttemptsPerChannel != 2 || got.MaxTotalWait != 8*time.Second {
		t.Fatalf("budget defaults = %+v", got)
	}
	if got.ConnectTimeout != 30*time.Second || got.FirstEventTimeout != 600*time.Second {
		t.Fatalf("liveness defaults = %+v", got)
	}
	if got.StreamIdleTimeout != 240*time.Second || got.CallTimeout != 7200*time.Second {
		t.Fatalf("idle/call defaults = %+v", got)
	}
	if got.FirstEventTimeout <= 90*time.Second {
		t.Fatalf("first event default %s must exceed 90s contract", got.FirstEventTimeout)
	}
}

func TestStreamRequestIdleTimeoutOverride(t *testing.T) {
	req := StreamRequest{RecoverySettings: RecoverySettings{StreamIdleTimeout: 12 * time.Second}}
	got := req.normalizedRecoverySettings()
	if got.StreamIdleTimeout != 12*time.Second {
		t.Fatalf("idle overlay = %s, want 12s", got.StreamIdleTimeout)
	}
}
