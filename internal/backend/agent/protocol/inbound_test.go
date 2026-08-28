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

	t.Run("empty message is unknown", func(t *testing.T) {
		t.Parallel()
		if got := ReadRequestedModelID(nil); got != "" {
			t.Fatalf("got %q", got)
		}
		if got := ReadRequestedModelID(&agentv1.AgentClientMessage{}); got != "" {
			t.Fatalf("got %q", got)
		}
	})
}
