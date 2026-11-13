package protocol

import (
	"encoding/hex"
	"testing"

	"cursor/gen/agentv1"
	"cursor/gen/aiserverv1"

	"google.golang.org/protobuf/proto"
)

func TestReadAppendPayloadBinaryPreferred(t *testing.T) {
	t.Parallel()

	payload, err := proto.Marshal(&agentv1.AgentClientMessage{Message: &agentv1.AgentClientMessage_ClientHeartbeat{ClientHeartbeat: &agentv1.ClientHeartbeat{}}})
	if err != nil {
		t.Fatal(err)
	}
	got, source, err := ReadAppendPayload(&aiserverv1.BidiAppendRequest{
		Data:       "zz-not-hex",
		DataBinary: payload,
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if source != AppendPayloadSourceBinary {
		t.Fatalf("source=%q, want %q", source, AppendPayloadSourceBinary)
	}
	if string(got) != string(payload) {
		t.Fatal("binary payload was not returned verbatim")
	}
}

func TestReadAppendPayloadHex(t *testing.T) {
	t.Parallel()

	payload, err := proto.Marshal(&agentv1.AgentClientMessage{Message: &agentv1.AgentClientMessage_ClientHeartbeat{ClientHeartbeat: &agentv1.ClientHeartbeat{}}})
	if err != nil {
		t.Fatal(err)
	}
	got, source, err := ReadAppendPayload(&aiserverv1.BidiAppendRequest{Data: hex.EncodeToString(payload)})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if source != AppendPayloadSourceHex {
		t.Fatalf("source=%q, want %q", source, AppendPayloadSourceHex)
	}
	if string(got) != string(payload) {
		t.Fatal("hex payload decode mismatch")
	}
}

func TestReadAppendPayloadEmpty(t *testing.T) {
	t.Parallel()

	got, source, err := ReadAppendPayload(&aiserverv1.BidiAppendRequest{})
	if err != nil || got != nil || source != "" {
		t.Fatalf("got payload=%v source=%q err=%v, want empty", got, source, err)
	}
}

func TestReadAppendPayloadInvalidHex(t *testing.T) {
	t.Parallel()

	if _, _, err := ReadAppendPayload(&aiserverv1.BidiAppendRequest{Data: "zz"}); err == nil {
		t.Fatal("invalid hex must be rejected")
	}
}

func TestDecodeAgentClientMessageBytes(t *testing.T) {
	t.Parallel()

	payload, err := proto.Marshal(&agentv1.AgentClientMessage{Message: &agentv1.AgentClientMessage_ClientHeartbeat{ClientHeartbeat: &agentv1.ClientHeartbeat{}}})
	if err != nil {
		t.Fatal(err)
	}
	message, kind, err := DecodeAgentClientMessageBytes(payload)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if message == nil || kind != "client_heartbeat" {
		t.Fatalf("message=%v kind=%q", message, kind)
	}
}

func TestReadRequestedModelID(t *testing.T) {
	t.Parallel()

	t.Run("run request preferred over details", func(t *testing.T) {
		t.Parallel()
		got := ReadRequestedModelID(&agentv1.AgentClientMessage{
			Message: &agentv1.AgentClientMessage_RunRequest{
				RunRequest: &agentv1.AgentRunRequest{
					RequestedModel: &agentv1.RequestedModel{ModelId: "abcdef0123456789"},
					ModelDetails:   &agentv1.ModelDetails{ModelId: "grok-3"},
				},
			},
		})
		if got != "abcdef0123456789" {
			t.Fatalf("got %q", got)
		}
	})

	t.Run("variant string keeps model prefix", func(t *testing.T) {
		t.Parallel()
		got := ReadRequestedModelID(&agentv1.AgentClientMessage{
			Message: &agentv1.AgentClientMessage_RunRequest{
				RunRequest: &agentv1.AgentRunRequest{
					RequestedModel: &agentv1.RequestedModel{
						ModelId:                       "abcdef0123456789:high",
						IsVariantStringRepresentation: true,
					},
				},
			},
		})
		if got != "abcdef0123456789" {
			t.Fatalf("got %q", got)
		}
	})

	t.Run("prewarm request model", func(t *testing.T) {
		t.Parallel()
		got := ReadRequestedModelID(&agentv1.AgentClientMessage{
			Message: &agentv1.AgentClientMessage_PrewarmRequest{
				PrewarmRequest: &agentv1.PrewarmRequest{
					RequestedModel: &agentv1.RequestedModel{ModelId: "abcdef0123456789"},
				},
			},
		})
		if got != "abcdef0123456789" {
			t.Fatalf("got %q", got)
		}
	})

	t.Run("empty run is present without model", func(t *testing.T) {
		t.Parallel()
		message := &agentv1.AgentClientMessage{
			Message: &agentv1.AgentClientMessage_RunRequest{
				RunRequest: &agentv1.AgentRunRequest{},
			},
		}
		if got := ReadRequestedModelID(message); got != "" {
			t.Fatalf("got %q", got)
		}
		if !HasRunOrPrewarmRequest(message) {
			t.Fatal("empty run must still count as run/prewarm presence")
		}
	})

	t.Run("empty message is unknown follow-up", func(t *testing.T) {
		t.Parallel()
		if got := ReadRequestedModelID(nil); got != "" {
			t.Fatalf("got %q", got)
		}
		if HasRunOrPrewarmRequest(nil) {
			t.Fatal("nil message must not count as run/prewarm")
		}
		empty := &agentv1.AgentClientMessage{}
		if got := ReadRequestedModelID(empty); got != "" {
			t.Fatalf("got %q", got)
		}
		if HasRunOrPrewarmRequest(empty) {
			t.Fatal("empty follow-up must not count as run/prewarm")
		}
	})

	t.Run("conversation action is follow-up", func(t *testing.T) {
		t.Parallel()
		message := &agentv1.AgentClientMessage{
			Message: &agentv1.AgentClientMessage_ConversationAction{
				ConversationAction: &agentv1.ConversationAction{},
			},
		}
		if got := ReadRequestedModelID(message); got != "" {
			t.Fatalf("got %q", got)
		}
		if HasRunOrPrewarmRequest(message) {
			t.Fatal("conversation action must not count as run/prewarm")
		}
	})
}

func TestHasRunOrPrewarmRequest(t *testing.T) {
	t.Parallel()
	if !HasRunOrPrewarmRequest(&agentv1.AgentClientMessage{
		Message: &agentv1.AgentClientMessage_PrewarmRequest{
			PrewarmRequest: &agentv1.PrewarmRequest{},
		},
	}) {
		t.Fatal("empty prewarm must count as run/prewarm presence")
	}
}
