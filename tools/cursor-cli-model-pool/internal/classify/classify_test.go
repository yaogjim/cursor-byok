package classify

import (
	"strings"
	"testing"
)

func TestParseLineTypedCategoriesAndFreeTextIgnored(t *testing.T) {
	ev, err := ParseLine([]byte(`{"type":"result","subtype":"error","error_category":"http_429"}`))
	if err != nil || ev.Category != CatHTTP429 {
		t.Fatalf("typed 429: %+v err=%v", ev, err)
	}
	ev, err = ParseLine([]byte(`{"type":"result","subtype":"error","error":{"category":"transport"}}`))
	if err != nil || ev.Category != CatTransport {
		t.Fatalf("nested transport: %+v err=%v", ev, err)
	}
	ev, err = ParseLine([]byte(`{"type":"result","subtype":"error","http_status":503}`))
	if err != nil || ev.Category != CatHTTP503 {
		t.Fatalf("status 503: %+v err=%v", ev, err)
	}
	ev, err = ParseLine([]byte(`{"type":"result","subtype":"error","error":"HTTP 429 rate limit please retry"}`))
	if err != nil {
		t.Fatal(err)
	}
	if ev.Category != CatNone {
		t.Fatalf("free text error must not classify, got %q", ev.Category)
	}
}

func TestSuccessHTTPStatusDoesNotBecomeError(t *testing.T) {
	ev, err := ParseLine([]byte(`{"type":"result","subtype":"success","http_status":200}`))
	if err != nil {
		t.Fatal(err)
	}
	if ev.Category != CatNone {
		t.Fatalf("success status 200 category = %q", ev.Category)
	}
}

func TestMissingTypedCategoryIsUnknownForSwitching(t *testing.T) {
	ev, err := ParseLine([]byte(`{"type":"result","subtype":"error","message":"429"}`))
	if err != nil {
		t.Fatal(err)
	}
	if Switchable(PhasePreOutput, ev.Category) {
		t.Fatal("untyped result must be fail-closed")
	}
	if Switchable(PhasePreOutput, CatHTTP500) || Switchable(PhasePreOutput, CatHTTP4xx) || Switchable(PhasePreOutput, CatCancel) {
		t.Fatal("500/4xx/cancel must not switch")
	}
	if Switchable(PhaseObserved, CatHTTP429) || Switchable(PhaseMutated, CatTransport) {
		t.Fatal("observed/mutated must not switch")
	}
}

func TestThinkingAssistantToolCloseWindow(t *testing.T) {
	for _, line := range []string{
		`{"type":"thinking"}`,
		`{"type":"assistant"}`,
		`{"type":"tool_call","subtype":"started"}`,
	} {
		ev, err := ParseLine([]byte(line))
		if err != nil {
			t.Fatal(err)
		}
		if !ClosesSwitchWindow(ev) {
			t.Fatalf("%s should close window", line)
		}
	}
	for _, line := range []string{
		`{"type":"system","subtype":"init","session_id":"s"}`,
		`{"type":"user"}`,
		`{"type":"retry"}`,
		`{"type":"connection"}`,
	} {
		ev, err := ParseLine([]byte(line))
		if err != nil {
			t.Fatal(err)
		}
		if !StayPreOutput(ev) || ClosesSwitchWindow(ev) {
			t.Fatalf("%s should stay pre_output", line)
		}
	}
}

func TestUnknownEventTypeFailClosed(t *testing.T) {
	ev, err := ParseLine([]byte(`{"type":"mystery","text":"hello"}`))
	if err != nil {
		t.Fatal(err)
	}
	if !ev.UnknownType || !ClosesSwitchWindow(ev) {
		t.Fatalf("unknown type must close window: %+v", ev)
	}
}

func TestParseLineDoesNotKeepFreeText(t *testing.T) {
	secret := "PROMPT-SECRET-TEXT"
	ev, err := ParseLine([]byte(`{"type":"assistant","text":"` + secret + `"}`))
	if err != nil {
		t.Fatal(err)
	}
	encoded := ev.Type + ev.Subtype + ev.SessionID + string(ev.Category)
	if strings.Contains(encoded, secret) {
		t.Fatal("event retained free text")
	}
}
