package modeladapter

import (
	"context"
	"errors"
	"testing"
	"time"
)

func TestFirstEventTimeoutUsesInjectedDurationNotReal90s(t *testing.T) {
	if DefaultRecoverySettings().FirstEventTimeout <= 90*time.Second {
		t.Fatalf("contract default first-event timeout must be >90s")
	}
	_, live := newProviderLiveness(context.Background(), RecoverySettings{
		CallTimeout:       time.Hour,
		ConnectTimeout:    time.Hour,
		FirstEventTimeout: 25 * time.Millisecond,
		StreamIdleTimeout: time.Hour,
	})
	defer live.Stop()
	started := time.Now()
	live.MarkHeadersReceived()
	deadline := time.After(time.Second)
	for live.Err() == nil {
		select {
		case <-deadline:
			t.Fatal("first-event timeout did not fire")
		case <-time.After(5 * time.Millisecond):
		}
	}
	if time.Since(started) > 500*time.Millisecond {
		t.Fatalf("waited %s, timing test must not sleep the 90s+ contract", time.Since(started))
	}
	var timeout *LivenessTimeoutError
	if !errors.As(live.Err(), &timeout) || timeout.Phase != LivenessPhaseFirstEvent {
		t.Fatalf("err = %v, want first_event liveness timeout", live.Err())
	}
}

func TestCallTimeoutPausesDuringRecoveryWait(t *testing.T) {
	_, live := newProviderLiveness(context.Background(), RecoverySettings{
		CallTimeout:       50 * time.Millisecond,
		ConnectTimeout:    time.Hour,
		FirstEventTimeout: time.Hour,
		StreamIdleTimeout: time.Hour,
	})
	defer live.Stop()
	live.PauseCallClock()
	time.Sleep(80 * time.Millisecond)
	if live.Err() != nil {
		t.Fatalf("paused call clock still fired: %v", live.Err())
	}
	live.ResumeCallClock()
	deadline := time.After(time.Second)
	for live.Err() == nil {
		select {
		case <-deadline:
			t.Fatal("call timeout did not fire after resume")
		case <-time.After(5 * time.Millisecond):
		}
	}
	var timeout *LivenessTimeoutError
	if !errors.As(live.Err(), &timeout) || timeout.Phase != LivenessPhaseCall {
		t.Fatalf("err = %v, want call timeout", live.Err())
	}
}

func TestAttemptTimeoutResetsForNextHTTPAttempt(t *testing.T) {
	_, live := newProviderLiveness(context.Background(), RecoverySettings{
		CallTimeout:       time.Hour,
		ConnectTimeout:    time.Hour,
		FirstEventTimeout: 25 * time.Millisecond,
		StreamIdleTimeout: time.Hour,
	})
	defer live.Stop()
	live.SetAttemptCancel(func(error) {})
	live.MarkHeadersReceived()
	deadline := time.After(time.Second)
	for live.Err() == nil {
		select {
		case <-deadline:
			t.Fatal("first attempt timeout did not fire")
		case <-time.After(5 * time.Millisecond):
		}
	}

	live.SetAttemptCancel(func(error) {})
	if err := live.Err(); err != nil {
		t.Fatalf("next attempt inherited timeout: %v", err)
	}
	live.MarkHeadersReceived()
	live.MarkEffectiveContent()
	time.Sleep(40 * time.Millisecond)
	if err := live.Err(); err != nil {
		t.Fatalf("healthy next attempt timed out: %v", err)
	}
}
