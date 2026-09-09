package protocol

import (
	"testing"

	"cursor/gen/agentv1"
)

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
