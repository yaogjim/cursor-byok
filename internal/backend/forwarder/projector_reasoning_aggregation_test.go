package forwarder

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"

	"google.golang.org/protobuf/encoding/protojson"

	"cursor/gen/agentv1"
	modeladapter "cursor/internal/backend/agent/model"
)

const replayReasoningCanary = "SHARED_MODEL_CALL_REASONING"

func TestHistoryEntryModelCallIDJSONDualRead(t *testing.T) {
	oldJSON := `{"seq":1,"turn_seq":1,"request_id":"request-1","role":"assistant","kind":"assistant_text","payload":{"text":"hi"},"created_at":"2026-01-01T00:00:00Z"}`
	var old HistoryEntry
	if err := json.Unmarshal([]byte(oldJSON), &old); err != nil {
		t.Fatalf("unmarshal legacy history entry: %v", err)
	}
	if old.ModelCallID != "" {
		t.Fatalf("legacy ModelCallID = %q, want empty", old.ModelCallID)
	}
	if old.TurnSeq != 1 || old.RequestID != "request-1" || old.Kind != "assistant_text" {
		t.Fatalf("legacy dual-read lost identity: %#v", old)
	}

	entry := withHistoryModelCallID(newAssistantTextEntry(1, "request-1", "hi", replayReasoningCanary, "sig-1"), "model-call-1")
	encoded, err := json.Marshal(entry)
	if err != nil {
		t.Fatalf("marshal history entry: %v", err)
	}
	if !bytes.Contains(encoded, []byte(`"model_call_id":"model-call-1"`)) {
		t.Fatalf("encoded history missing model_call_id: %s", encoded)
	}
	var loaded HistoryEntry
	if err := json.Unmarshal(encoded, &loaded); err != nil {
		t.Fatalf("unmarshal new history entry: %v", err)
	}
	if loaded.ModelCallID != "model-call-1" {
		t.Fatalf("round-trip ModelCallID = %q, want model-call-1", loaded.ModelCallID)
	}
	var payload assistantTextPayload
	if err := json.Unmarshal(loaded.Payload, &payload); err != nil {
		t.Fatalf("decode assistant payload: %v", err)
	}
	if payload.ReasoningContent != replayReasoningCanary || payload.ReasoningSignature != "sig-1" {
		t.Fatalf("canonical reasoning dropped during JSON round-trip: %#v", payload)
	}

	camelJSON := `{"seq":2,"turn_seq":1,"request_id":"request-1","modelCallID":"model-call-camel","role":"assistant","kind":"assistant_text","payload":{"text":"hi","reasoning_content":"SHARED_MODEL_CALL_REASONING"},"created_at":"2026-01-01T00:00:00Z"}`
	var camel HistoryEntry
	if err := json.Unmarshal([]byte(camelJSON), &camel); err != nil {
		t.Fatalf("unmarshal camelCase history entry: %v", err)
	}
	if camel.ModelCallID != "model-call-camel" {
		t.Fatalf("camelCase ModelCallID = %q, want model-call-camel", camel.ModelCallID)
	}

	bothJSON := `{"seq":3,"turn_seq":1,"request_id":"request-1","model_call_id":"model-call-snake","modelCallID":"model-call-camel","role":"assistant","kind":"assistant_text","payload":{"text":"hi"},"created_at":"2026-01-01T00:00:00Z"}`
	var both HistoryEntry
	if err := json.Unmarshal([]byte(bothJSON), &both); err != nil {
		t.Fatalf("unmarshal dual-key history entry: %v", err)
	}
	if both.ModelCallID != "model-call-snake" {
		t.Fatalf("dual-key ModelCallID = %q, want snake-case winner", both.ModelCallID)
	}
}

func TestProjectPromptReplayDeduplicatesReasoningForSameModelCall(t *testing.T) {
	conversation := replayAggregationConversation(t, []HistoryEntry{
		replayAggregationUserEntry(t, 1, "request-1", "edit two files"),
		withHistoryModelCallID(newAssistantTextEntryWithProviderMetadata(1, "request-1", "I will edit both", replayReasoningCanary, "sig-shared", modeladapter.ReasoningSignatureSourceAnthropic, "rsn_item_1", "completed", replayAggregationSummary()), "model-call-1"),
		replayAggregationToolCallEntry(t, 1, "request-1", "model-call-1", "call-1", "one.txt", "item-1", "fc-1", "completed"),
		replayAggregationToolCallEntry(t, 1, "request-1", "model-call-1", "call-2", "two.txt", "item-2", "fc-2", "in_progress"),
		replayAggregationToolResultEntry(t, 1, "request-1", "model-call-1", "call-1", "one.txt", "edited one"),
		replayAggregationToolResultEntry(t, 1, "request-1", "model-call-1", "call-2", "two.txt", "edited two"),
	})
	canonical := marshalReplayCanonicalHistory(t, conversation)

	replay := projectReplayAggregation(t, conversation)
	if got := countReplayReasoning(replay, replayReasoningCanary); got != 1 {
		t.Fatalf("same model call reasoning count = %d, want 1\nreplay=%s", got, replayAggregationDump(replay))
	}
	assertReplayReasoningTuple(t, firstReplayReasoningMessage(t, replay, replayReasoningCanary), replayReasoningCanary, "sig-shared", modeladapter.ReasoningSignatureSourceAnthropic, "rsn_item_1", "completed", replayAggregationSummary())
	assertReplayToolSequence(t, replay, []replayExpectedTool{
		{ID: "call-1", Path: "one.txt", ProviderItemID: "item-1", ProviderCallID: "fc-1", ProviderStatus: "completed", Result: "edited one"},
		{ID: "call-2", Path: "two.txt", ProviderItemID: "item-2", ProviderCallID: "fc-2", ProviderStatus: "in_progress", Result: "edited two"},
	})
	assertCanonicalHistoryUnchanged(t, conversation, canonical)
}

func TestProjectPromptReplayUpgradesStatusOnlyReasoningWhenCompleteTupleArrives(t *testing.T) {
	conversation := replayAggregationConversation(t, []HistoryEntry{
		replayAggregationUserEntry(t, 1, "request-1", "edit file"),
		withHistoryModelCallID(newAssistantTextEntryWithProviderMetadata(1, "request-1", "I will edit", "", "", "", "rsn_item_1", "in_progress", nil), "model-call-1"),
		replayAggregationToolCallEntry(t, 1, "request-1", "model-call-1", "call-1", "one.txt", "item-1", "fc-1", "completed"),
		replayAggregationToolResultEntry(t, 1, "request-1", "model-call-1", "call-1", "one.txt", "edited one"),
	})
	canonical := marshalReplayCanonicalHistory(t, conversation)

	replay := projectReplayAggregation(t, conversation)
	if got := countReplayReasoning(replay, replayReasoningCanary); got != 1 {
		t.Fatalf("upgraded status-only reasoning count = %d, want 1\nreplay=%s", got, replayAggregationDump(replay))
	}
	assertReplayReasoningTuple(t, firstAssistantReplayMessage(t, replay), replayReasoningCanary, "sig-shared", modeladapter.ReasoningSignatureSourceAnthropic, "rsn_item_1", "completed", replayAggregationSummary())
	assertReplayToolSequence(t, replay, []replayExpectedTool{
		{ID: "call-1", Path: "one.txt", ProviderItemID: "item-1", ProviderCallID: "fc-1", ProviderStatus: "completed", Result: "edited one"},
	})
	assertCanonicalHistoryUnchanged(t, conversation, canonical)
}

func TestProjectPromptReplayUpgradesContentWhenSignatureArrivesLater(t *testing.T) {
	conversation := replayAggregationConversation(t, []HistoryEntry{
		replayAggregationUserEntry(t, 1, "request-1", "edit file"),
		withHistoryModelCallID(newAssistantTextEntryWithProviderMetadata(1, "request-1", "I will edit", replayReasoningCanary, "", "", "rsn_item_1", "in_progress", nil), "model-call-1"),
		withHistoryModelCallID(newToolCallEntryWithProviderMetadata(1, "request-1", "call-1", "Edit", replayReasoningCanary, "sig-shared", modeladapter.ReasoningSignatureSourceAnthropic, "rsn_item_1", "completed", replayAggregationSummary(), "item-1", "fc-1", "completed", transcriptTestEditToolCall(t, "one.txt")), "model-call-1"),
		replayAggregationToolResultEntry(t, 1, "request-1", "model-call-1", "call-1", "one.txt", "edited one"),
	})
	canonical := marshalReplayCanonicalHistory(t, conversation)

	replay := projectReplayAggregation(t, conversation)
	if got := countReplayReasoning(replay, replayReasoningCanary); got != 1 {
		t.Fatalf("upgraded signature reasoning count = %d, want 1\nreplay=%s", got, replayAggregationDump(replay))
	}
	assertReplayReasoningTuple(t, firstAssistantReplayMessage(t, replay), replayReasoningCanary, "sig-shared", modeladapter.ReasoningSignatureSourceAnthropic, "rsn_item_1", "completed", replayAggregationSummary())
	assertReplayToolSequence(t, replay, []replayExpectedTool{
		{ID: "call-1", Path: "one.txt", ProviderItemID: "item-1", ProviderCallID: "fc-1", ProviderStatus: "completed", Result: "edited one"},
	})
	assertCanonicalHistoryUnchanged(t, conversation, canonical)
}

func TestProjectPromptReplayUpgradesShorterCumulativeReasoning(t *testing.T) {
	const laterReasoning = replayReasoningCanary + "\n\ncontinue with the second file"
	conversation := replayAggregationConversation(t, []HistoryEntry{
		replayAggregationUserEntry(t, 1, "request-1", "edit files"),
		withHistoryModelCallID(newAssistantTextEntryWithProviderMetadata(1, "request-1", "I will edit", replayReasoningCanary, "sig-partial", modeladapter.ReasoningSignatureSourceAnthropic, "rsn_item_1", "in_progress", nil), "model-call-1"),
		withHistoryModelCallID(newToolCallEntryWithProviderMetadata(1, "request-1", "call-1", "Edit", laterReasoning, "sig-shared", modeladapter.ReasoningSignatureSourceAnthropic, "rsn_item_1", "completed", replayAggregationSummary(), "item-1", "fc-1", "completed", transcriptTestEditToolCall(t, "one.txt")), "model-call-1"),
		replayAggregationToolResultEntry(t, 1, "request-1", "model-call-1", "call-1", "one.txt", "edited one"),
	})
	canonical := marshalReplayCanonicalHistory(t, conversation)

	replay := projectReplayAggregation(t, conversation)
	if got := countReplayReasoning(replay, laterReasoning); got != 1 {
		t.Fatalf("upgraded cumulative reasoning count = %d, want 1\nreplay=%s", got, replayAggregationDump(replay))
	}
	if got := countReplayReasoning(replay, replayReasoningCanary); got != 1 {
		t.Fatalf("cumulative reasoning still contains prefix, count = %d, want 1\nreplay=%s", got, replayAggregationDump(replay))
	}
	assertReplayReasoningTuple(t, firstAssistantReplayMessage(t, replay), laterReasoning, "sig-shared", modeladapter.ReasoningSignatureSourceAnthropic, "rsn_item_1", "completed", replayAggregationSummary())
	assertReplayToolSequence(t, replay, []replayExpectedTool{
		{ID: "call-1", Path: "one.txt", ProviderItemID: "item-1", ProviderCallID: "fc-1", ProviderStatus: "completed", Result: "edited one"},
	})
	assertCanonicalHistoryUnchanged(t, conversation, canonical)
}

func TestProjectPromptReplayKeepsUpgradedReasoningWhenFirstCarrierIsTrimmed(t *testing.T) {
	conversation := replayAggregationConversation(t, []HistoryEntry{
		replayAggregationUserEntry(t, 1, "request-1", "edit file"),
		withHistoryModelCallID(newToolCallEntryWithProviderMetadata(1, "request-1", "call-dangling", "Edit", "", "", "", "rsn_item_1", "in_progress", nil, "item-dangling", "fc-dangling", "in_progress", transcriptTestEditToolCall(t, "missing.txt")), "model-call-1"),
		withHistoryModelCallID(newAssistantTextEntryWithProviderMetadata(1, "request-1", "I will edit", replayReasoningCanary, "sig-shared", modeladapter.ReasoningSignatureSourceAnthropic, "rsn_item_1", "completed", replayAggregationSummary()), "model-call-1"),
		replayAggregationToolCallEntry(t, 1, "request-1", "model-call-1", "call-1", "one.txt", "item-1", "fc-1", "completed"),
		replayAggregationToolResultEntry(t, 1, "request-1", "model-call-1", "call-1", "one.txt", "edited one"),
	})
	canonical := marshalReplayCanonicalHistory(t, conversation)

	replay := projectReplayAggregation(t, conversation)
	if got := countReplayReasoning(replay, replayReasoningCanary); got != 1 {
		t.Fatalf("trimmed-carrier reasoning count = %d, want 1\nreplay=%s", got, replayAggregationDump(replay))
	}
	first := firstAssistantReplayMessage(t, replay)
	if strings.TrimSpace(first.Content) != "I will edit" {
		t.Fatalf("first retained assistant content = %q, want I will edit\nreplay=%s", first.Content, replayAggregationDump(replay))
	}
	assertReplayReasoningTuple(t, first, replayReasoningCanary, "sig-shared", modeladapter.ReasoningSignatureSourceAnthropic, "rsn_item_1", "completed", replayAggregationSummary())
	assertReplayToolSequence(t, replay, []replayExpectedTool{
		{ID: "call-1", Path: "one.txt", ProviderItemID: "item-1", ProviderCallID: "fc-1", ProviderStatus: "completed", Result: "edited one"},
	})
	for _, message := range replay {
		for _, call := range message.ToolCalls {
			if call.ID == "call-dangling" {
				t.Fatalf("dangling first carrier was retained\nreplay=%s", replayAggregationDump(replay))
			}
		}
	}
	assertCanonicalHistoryUnchanged(t, conversation, canonical)
}

func TestProjectPromptReplayDoesNotRehomeReasoningAcrossModelCalls(t *testing.T) {
	const callOneReasoning = "CALL_ONE_ORPHAN_REASONING"
	conversation := replayAggregationConversation(t, []HistoryEntry{
		replayAggregationUserEntry(t, 1, "request-1", "two calls"),
		withHistoryModelCallID(newAssistantTextEntryWithProviderMetadata(1, "request-1", "", callOneReasoning, "sig-call-1", modeladapter.ReasoningSignatureSourceAnthropic, "rsn_item_1", "completed", replayAggregationSummary()), "model-call-1"),
		withHistoryModelCallID(newAssistantTextEntryWithProviderMetadata(1, "request-1", "second answer", "", "", "", "", "", nil), "model-call-2"),
	})
	canonical := marshalReplayCanonicalHistory(t, conversation)

	replay := projectReplayAggregation(t, conversation)
	if got := countReplayReasoning(replay, callOneReasoning); got != 1 {
		t.Fatalf("cross-call orphan reasoning count = %d, want 1\nreplay=%s", got, replayAggregationDump(replay))
	}
	assistants := assistantReplayMessages(t, replay)
	if len(assistants) != 2 {
		t.Fatalf("assistant count = %d, want 2 orphan + later text\nreplay=%s", len(assistants), replayAggregationDump(replay))
	}
	assertReplayReasoningTuple(t, assistants[0], callOneReasoning, "sig-call-1", modeladapter.ReasoningSignatureSourceAnthropic, "rsn_item_1", "completed", replayAggregationSummary())
	if strings.TrimSpace(assistants[1].Content) != "second answer" {
		t.Fatalf("call-2 content = %q, want second answer\nreplay=%s", assistants[1].Content, replayAggregationDump(replay))
	}
	if messageHasReplayReasoningTuple(assistants[1]) {
		t.Fatalf("call-1 reasoning was rehomed onto call-2\nreplay=%s", replayAggregationDump(replay))
	}
	assertCanonicalHistoryUnchanged(t, conversation, canonical)
}

func TestProjectPromptReplayRehomesOrphanReasoningWithinSameModelCall(t *testing.T) {
	conversation := replayAggregationConversation(t, []HistoryEntry{
		replayAggregationUserEntry(t, 1, "request-1", "same call"),
		withHistoryModelCallID(newAssistantTextEntryWithProviderMetadata(1, "request-1", "", replayReasoningCanary, "sig-shared", modeladapter.ReasoningSignatureSourceAnthropic, "rsn_item_1", "completed", replayAggregationSummary()), "model-call-1"),
		withHistoryModelCallID(newAssistantTextEntryWithProviderMetadata(1, "request-1", "I will edit", "", "", "", "", "", nil), "model-call-1"),
	})
	canonical := marshalReplayCanonicalHistory(t, conversation)

	replay := projectReplayAggregation(t, conversation)
	if got := countReplayReasoning(replay, replayReasoningCanary); got != 1 {
		t.Fatalf("same-call rehome reasoning count = %d, want 1\nreplay=%s", got, replayAggregationDump(replay))
	}
	first := firstAssistantReplayMessage(t, replay)
	if strings.TrimSpace(first.Content) != "I will edit" {
		t.Fatalf("same-call rehome first assistant content = %q, want I will edit\nreplay=%s", first.Content, replayAggregationDump(replay))
	}
	assertReplayReasoningTuple(t, first, replayReasoningCanary, "sig-shared", modeladapter.ReasoningSignatureSourceAnthropic, "rsn_item_1", "completed", replayAggregationSummary())
	assertCanonicalHistoryUnchanged(t, conversation, canonical)
}

func TestProjectPromptReplayDoesNotRehomeReasoningWhenIdentityIsInsufficient(t *testing.T) {
	const orphanReasoning = "LEGACY_ORPHAN_REASONING"
	conversation := replayAggregationConversation(t, []HistoryEntry{
		replayAggregationUserEntry(t, 1, "request-1", "legacy orphan"),
		newAssistantTextEntryWithProviderMetadata(1, "request-1", "", orphanReasoning, "sig-legacy", modeladapter.ReasoningSignatureSourceAnthropic, "rsn_item_legacy", "completed", replayAggregationSummary()),
		newAssistantTextEntryWithProviderMetadata(1, "request-1", "later text", "", "", "", "", "", nil),
	})
	canonical := marshalReplayCanonicalHistory(t, conversation)

	replay := projectReplayAggregation(t, conversation)
	if got := countReplayReasoning(replay, orphanReasoning); got != 1 {
		t.Fatalf("insufficient-identity orphan reasoning count = %d, want 1\nreplay=%s", got, replayAggregationDump(replay))
	}
	assistants := assistantReplayMessages(t, replay)
	if len(assistants) != 2 {
		t.Fatalf("insufficient-identity assistant count = %d, want 2\nreplay=%s", len(assistants), replayAggregationDump(replay))
	}
	assertReplayReasoningTuple(t, assistants[0], orphanReasoning, "sig-legacy", modeladapter.ReasoningSignatureSourceAnthropic, "rsn_item_legacy", "completed", replayAggregationSummary())
	if messageHasReplayReasoningTuple(assistants[1]) {
		t.Fatalf("insufficient-identity orphan was rehomed\nreplay=%s", replayAggregationDump(replay))
	}
	assertCanonicalHistoryUnchanged(t, conversation, canonical)
}

func TestProjectPromptReplayKeepsSignatureWhenCumulativeContentLacksSignature(t *testing.T) {
	const firstReasoning = "HELLO"
	const laterReasoning = "HELLO more"
	conversation := replayAggregationConversation(t, []HistoryEntry{
		replayAggregationUserEntry(t, 1, "request-1", "edit file"),
		withHistoryModelCallID(newAssistantTextEntryWithProviderMetadata(1, "request-1", "I will edit", firstReasoning, "sig-keep", modeladapter.ReasoningSignatureSourceAnthropic, "rsn_item_keep", "in_progress", replayAggregationSummary()), "model-call-1"),
		withHistoryModelCallID(newToolCallEntryWithProviderMetadata(1, "request-1", "call-1", "Edit", laterReasoning, "", "", "rsn_item_other", "completed", replayAggregationOtherSummary(), "item-1", "fc-1", "completed", transcriptTestEditToolCall(t, "one.txt")), "model-call-1"),
		replayAggregationToolResultEntry(t, 1, "request-1", "model-call-1", "call-1", "one.txt", "edited one"),
	})
	canonical := marshalReplayCanonicalHistory(t, conversation)

	replay := projectReplayAggregation(t, conversation)
	if got := countReplayReasoning(replay, laterReasoning); got != 0 {
		t.Fatalf("unsigned longer content was mixed onto signed tuple, count = %d, want 0\nreplay=%s", got, replayAggregationDump(replay))
	}
	assertReplayReasoningTuple(t, firstAssistantReplayMessage(t, replay), firstReasoning, "sig-keep", modeladapter.ReasoningSignatureSourceAnthropic, "rsn_item_keep", "completed", replayAggregationSummary())
	assertReplayToolSequence(t, replay, []replayExpectedTool{
		{ID: "call-1", Path: "one.txt", ProviderItemID: "item-1", ProviderCallID: "fc-1", ProviderStatus: "completed", Result: "edited one"},
	})
	assertCanonicalHistoryUnchanged(t, conversation, canonical)
}

func TestProjectPromptReplayUpgradesLaterSignatureOnSameContent(t *testing.T) {
	conversation := replayAggregationConversation(t, []HistoryEntry{
		replayAggregationUserEntry(t, 1, "request-1", "edit file"),
		withHistoryModelCallID(newAssistantTextEntryWithProviderMetadata(1, "request-1", "I will edit", "HELLO", "", "", "rsn_item_1", "in_progress", nil), "model-call-1"),
		withHistoryModelCallID(newToolCallEntryWithProviderMetadata(1, "request-1", "call-1", "Edit", "HELLO", "sig-new", modeladapter.ReasoningSignatureSourceAnthropic, "rsn_item_1", "completed", replayAggregationSummary(), "item-1", "fc-1", "completed", transcriptTestEditToolCall(t, "one.txt")), "model-call-1"),
		replayAggregationToolResultEntry(t, 1, "request-1", "model-call-1", "call-1", "one.txt", "edited one"),
	})
	canonical := marshalReplayCanonicalHistory(t, conversation)

	replay := projectReplayAggregation(t, conversation)
	if got := countReplayReasoning(replay, "HELLO"); got != 1 {
		t.Fatalf("later signature upgrade count = %d, want 1\nreplay=%s", got, replayAggregationDump(replay))
	}
	assertReplayReasoningTuple(t, firstAssistantReplayMessage(t, replay), "HELLO", "sig-new", modeladapter.ReasoningSignatureSourceAnthropic, "rsn_item_1", "completed", replayAggregationSummary())
	assertReplayToolSequence(t, replay, []replayExpectedTool{
		{ID: "call-1", Path: "one.txt", ProviderItemID: "item-1", ProviderCallID: "fc-1", ProviderStatus: "completed", Result: "edited one"},
	})
	assertCanonicalHistoryUnchanged(t, conversation, canonical)
}

func TestProjectPromptReplayPrefersSignedShorterTupleOverUnsignedLongerContent(t *testing.T) {
	const unsignedLonger = "HELLO more"
	const signedShorter = "HELLO"
	conversation := replayAggregationConversation(t, []HistoryEntry{
		replayAggregationUserEntry(t, 1, "request-1", "edit file"),
		withHistoryModelCallID(newAssistantTextEntryWithProviderMetadata(1, "request-1", "I will edit", unsignedLonger, "", "", "rsn_item_long", "in_progress", replayAggregationOtherSummary()), "model-call-1"),
		withHistoryModelCallID(newToolCallEntryWithProviderMetadata(1, "request-1", "call-1", "Edit", signedShorter, "sig-keep", modeladapter.ReasoningSignatureSourceAnthropic, "rsn_item_keep", "completed", replayAggregationSummary(), "item-1", "fc-1", "completed", transcriptTestEditToolCall(t, "one.txt")), "model-call-1"),
		replayAggregationToolResultEntry(t, 1, "request-1", "model-call-1", "call-1", "one.txt", "edited one"),
	})
	canonical := marshalReplayCanonicalHistory(t, conversation)

	replay := projectReplayAggregation(t, conversation)
	if got := countReplayReasoning(replay, unsignedLonger); got != 0 {
		t.Fatalf("short signature was attached to longer unsigned content, count = %d, want 0\nreplay=%s", got, replayAggregationDump(replay))
	}
	assertReplayReasoningTuple(t, firstAssistantReplayMessage(t, replay), signedShorter, "sig-keep", modeladapter.ReasoningSignatureSourceAnthropic, "rsn_item_keep", "completed", replayAggregationSummary())
	assertReplayToolSequence(t, replay, []replayExpectedTool{
		{ID: "call-1", Path: "one.txt", ProviderItemID: "item-1", ProviderCallID: "fc-1", ProviderStatus: "completed", Result: "edited one"},
	})
	assertCanonicalHistoryUnchanged(t, conversation, canonical)
}

func TestProjectPromptReplayAdoptsLongerSignedTupleOverUnsignedShorterContent(t *testing.T) {
	const unsignedShorter = "HELLO"
	const signedLonger = "HELLO more"
	conversation := replayAggregationConversation(t, []HistoryEntry{
		replayAggregationUserEntry(t, 1, "request-1", "edit file"),
		withHistoryModelCallID(newAssistantTextEntryWithProviderMetadata(1, "request-1", "I will edit", unsignedShorter, "", "", "", "in_progress", nil), "model-call-1"),
		withHistoryModelCallID(newToolCallEntryWithProviderMetadata(1, "request-1", "call-1", "Edit", signedLonger, "sig-new", modeladapter.ReasoningSignatureSourceAnthropic, "rsn_item_new", "completed", replayAggregationSummary(), "item-1", "fc-1", "completed", transcriptTestEditToolCall(t, "one.txt")), "model-call-1"),
		replayAggregationToolResultEntry(t, 1, "request-1", "model-call-1", "call-1", "one.txt", "edited one"),
	})
	canonical := marshalReplayCanonicalHistory(t, conversation)

	replay := projectReplayAggregation(t, conversation)
	if got := countReplayReasoning(replay, signedLonger); got != 1 {
		t.Fatalf("longer signed tuple count = %d, want 1\nreplay=%s", got, replayAggregationDump(replay))
	}
	assertReplayReasoningTuple(t, firstAssistantReplayMessage(t, replay), signedLonger, "sig-new", modeladapter.ReasoningSignatureSourceAnthropic, "rsn_item_new", "completed", replayAggregationSummary())
	assertReplayToolSequence(t, replay, []replayExpectedTool{
		{ID: "call-1", Path: "one.txt", ProviderItemID: "item-1", ProviderCallID: "fc-1", ProviderStatus: "completed", Result: "edited one"},
	})
	assertCanonicalHistoryUnchanged(t, conversation, canonical)
}

func TestProjectPromptReplayGrowsUnsignedPrefixReasoningContent(t *testing.T) {
	const firstReasoning = "HELLO"
	const laterReasoning = "HELLO more"
	conversation := replayAggregationConversation(t, []HistoryEntry{
		replayAggregationUserEntry(t, 1, "request-1", "edit file"),
		withHistoryModelCallID(newAssistantTextEntryWithProviderMetadata(1, "request-1", "I will edit", firstReasoning, "", "", "rsn_item_1", "in_progress", nil), "model-call-1"),
		withHistoryModelCallID(newToolCallEntryWithProviderMetadata(1, "request-1", "call-1", "Edit", laterReasoning, "", "", "rsn_item_1", "completed", replayAggregationSummary(), "item-1", "fc-1", "completed", transcriptTestEditToolCall(t, "one.txt")), "model-call-1"),
		replayAggregationToolResultEntry(t, 1, "request-1", "model-call-1", "call-1", "one.txt", "edited one"),
	})
	canonical := marshalReplayCanonicalHistory(t, conversation)

	replay := projectReplayAggregation(t, conversation)
	if got := countReplayReasoning(replay, laterReasoning); got != 1 {
		t.Fatalf("unsigned prefix growth count = %d, want 1\nreplay=%s", got, replayAggregationDump(replay))
	}
	assertReplayReasoningTuple(t, firstAssistantReplayMessage(t, replay), laterReasoning, "", "", "rsn_item_1", "completed", replayAggregationSummary())
	assertReplayToolSequence(t, replay, []replayExpectedTool{
		{ID: "call-1", Path: "one.txt", ProviderItemID: "item-1", ProviderCallID: "fc-1", ProviderStatus: "completed", Result: "edited one"},
	})
	assertCanonicalHistoryUnchanged(t, conversation, canonical)
}

func TestProjectPromptReplayDoesNotMixProviderMetadataWhenReasoningContentIsIncompatible(t *testing.T) {
	const signedReasoning = "HELLO"
	const otherReasoning = "OTHER_REASONING"
	conversation := replayAggregationConversation(t, []HistoryEntry{
		replayAggregationUserEntry(t, 1, "request-1", "edit file"),
		withHistoryModelCallID(newAssistantTextEntryWithProviderMetadata(1, "request-1", "I will edit", signedReasoning, "sig-keep", modeladapter.ReasoningSignatureSourceAnthropic, "rsn_item_keep", "in_progress", replayAggregationSummary()), "model-call-1"),
		withHistoryModelCallID(newToolCallEntryWithProviderMetadata(1, "request-1", "call-1", "Edit", otherReasoning, "", "", "rsn_item_other", "completed", replayAggregationOtherSummary(), "item-1", "fc-1", "completed", transcriptTestEditToolCall(t, "one.txt")), "model-call-1"),
		replayAggregationToolResultEntry(t, 1, "request-1", "model-call-1", "call-1", "one.txt", "edited one"),
	})
	canonical := marshalReplayCanonicalHistory(t, conversation)

	replay := projectReplayAggregation(t, conversation)
	if got := countReplayReasoning(replay, otherReasoning); got != 0 {
		t.Fatalf("incompatible reasoning content was mixed, count = %d, want 0\nreplay=%s", got, replayAggregationDump(replay))
	}
	assertReplayReasoningTuple(t, firstAssistantReplayMessage(t, replay), signedReasoning, "sig-keep", modeladapter.ReasoningSignatureSourceAnthropic, "rsn_item_keep", "in_progress", replayAggregationSummary())
	assertReplayToolSequence(t, replay, []replayExpectedTool{
		{ID: "call-1", Path: "one.txt", ProviderItemID: "item-1", ProviderCallID: "fc-1", ProviderStatus: "completed", Result: "edited one"},
	})
	assertCanonicalHistoryUnchanged(t, conversation, canonical)
}

func TestProjectPromptReplayKeepsFirstSignedTupleWhenLaterSignedTupleIsWeaker(t *testing.T) {
	conversation := replayAggregationConversation(t, []HistoryEntry{
		replayAggregationUserEntry(t, 1, "request-1", "edit file"),
		withHistoryModelCallID(newAssistantTextEntryWithProviderMetadata(1, "request-1", "I will edit", "HELLO", "sig-keep", modeladapter.ReasoningSignatureSourceAnthropic, "rsn_item_keep", "completed", replayAggregationSummary()), "model-call-1"),
		withHistoryModelCallID(newToolCallEntryWithProviderMetadata(1, "request-1", "call-1", "Edit", "WORLD", "sig-other", "", "", "", nil, "item-1", "fc-1", "completed", transcriptTestEditToolCall(t, "one.txt")), "model-call-1"),
		replayAggregationToolResultEntry(t, 1, "request-1", "model-call-1", "call-1", "one.txt", "edited one"),
	})
	canonical := marshalReplayCanonicalHistory(t, conversation)

	replay := projectReplayAggregation(t, conversation)
	if got := countReplayReasoning(replay, "WORLD"); got != 0 {
		t.Fatalf("weaker later signed tuple replaced first, count = %d, want 0\nreplay=%s", got, replayAggregationDump(replay))
	}
	assertReplayReasoningTuple(t, firstAssistantReplayMessage(t, replay), "HELLO", "sig-keep", modeladapter.ReasoningSignatureSourceAnthropic, "rsn_item_keep", "completed", replayAggregationSummary())
	assertReplayToolSequence(t, replay, []replayExpectedTool{
		{ID: "call-1", Path: "one.txt", ProviderItemID: "item-1", ProviderCallID: "fc-1", ProviderStatus: "completed", Result: "edited one"},
	})
	assertCanonicalHistoryUnchanged(t, conversation, canonical)
}

func TestProjectPromptReplayDoesNotMergeSameTextAcrossModelCalls(t *testing.T) {
	conversation := replayAggregationConversation(t, []HistoryEntry{
		replayAggregationUserEntry(t, 1, "request-1", "two answers"),
		withHistoryModelCallID(newAssistantTextEntryWithProviderMetadata(1, "request-1", "first answer", replayReasoningCanary, "sig-a", modeladapter.ReasoningSignatureSourceAnthropic, "rsn_item_a", "completed", replayAggregationSummary()), "model-call-1"),
		withHistoryModelCallID(newAssistantTextEntryWithProviderMetadata(1, "request-1", "second answer", replayReasoningCanary, "sig-b", modeladapter.ReasoningSignatureSourceAnthropic, "rsn_item_b", "completed", replayAggregationSummary()), "model-call-2"),
	})
	canonical := marshalReplayCanonicalHistory(t, conversation)

	replay := projectReplayAggregation(t, conversation)
	if got := countReplayReasoning(replay, replayReasoningCanary); got != 2 {
		t.Fatalf("same-text different-call reasoning count = %d, want 2\nreplay=%s", got, replayAggregationDump(replay))
	}
	assistants := assistantReplayMessages(t, replay)
	if len(assistants) != 2 {
		t.Fatalf("same-text different-call assistant count = %d, want 2\nreplay=%s", len(assistants), replayAggregationDump(replay))
	}
	assertReplayReasoningTuple(t, assistants[0], replayReasoningCanary, "sig-a", modeladapter.ReasoningSignatureSourceAnthropic, "rsn_item_a", "completed", replayAggregationSummary())
	assertReplayReasoningTuple(t, assistants[1], replayReasoningCanary, "sig-b", modeladapter.ReasoningSignatureSourceAnthropic, "rsn_item_b", "completed", replayAggregationSummary())
	assertCanonicalHistoryUnchanged(t, conversation, canonical)
}

func TestProjectPromptReplayKeepsReasoningForDifferentModelCalls(t *testing.T) {
	conversation := replayAggregationConversation(t, []HistoryEntry{
		replayAggregationUserEntry(t, 1, "request-1", "edit twice"),
		withHistoryModelCallID(newAssistantTextEntryWithProviderMetadata(1, "request-1", "first pass", replayReasoningCanary, "sig-a", modeladapter.ReasoningSignatureSourceAnthropic, "rsn_item_a", "completed", replayAggregationSummary()), "model-call-1"),
		replayAggregationToolCallEntry(t, 1, "request-1", "model-call-1", "call-1", "one.txt", "item-1", "fc-1", "completed"),
		replayAggregationToolResultEntry(t, 1, "request-1", "model-call-1", "call-1", "one.txt", "edited one"),
		withHistoryModelCallID(newAssistantTextEntryWithProviderMetadata(1, "request-1", "second pass", replayReasoningCanary, "sig-b", modeladapter.ReasoningSignatureSourceAnthropic, "rsn_item_b", "completed", replayAggregationSummary()), "model-call-2"),
		replayAggregationToolCallEntry(t, 1, "request-1", "model-call-2", "call-2", "two.txt", "item-2", "fc-2", "completed"),
		replayAggregationToolResultEntry(t, 1, "request-1", "model-call-2", "call-2", "two.txt", "edited two"),
	})

	replay := projectReplayAggregation(t, conversation)
	if got := countReplayReasoning(replay, replayReasoningCanary); got != 2 {
		t.Fatalf("different model call reasoning count = %d, want 2\nreplay=%s", got, replayAggregationDump(replay))
	}
	assertReplayToolSequence(t, replay, []replayExpectedTool{
		{ID: "call-1", Path: "one.txt", ProviderItemID: "item-1", ProviderCallID: "fc-1", ProviderStatus: "completed", Result: "edited one"},
		{ID: "call-2", Path: "two.txt", ProviderItemID: "item-2", ProviderCallID: "fc-2", ProviderStatus: "completed", Result: "edited two"},
	})
}

func TestProjectPromptReplayDoesNotAggregateAcrossTurnOrRequest(t *testing.T) {
	conversation := replayAggregationConversation(t, []HistoryEntry{
		replayAggregationUserEntry(t, 1, "request-1", "first turn"),
		withHistoryModelCallID(newAssistantTextEntryWithProviderMetadata(1, "request-1", "turn one", replayReasoningCanary, "sig-1", modeladapter.ReasoningSignatureSourceAnthropic, "rsn_item_1", "completed", replayAggregationSummary()), "model-call-shared"),
		replayAggregationUserEntry(t, 2, "request-2", "second turn"),
		withHistoryModelCallID(newAssistantTextEntryWithProviderMetadata(2, "request-2", "turn two", replayReasoningCanary, "sig-1", modeladapter.ReasoningSignatureSourceAnthropic, "rsn_item_1", "completed", replayAggregationSummary()), "model-call-shared"),
	})

	replay := projectReplayAggregation(t, conversation)
	if got := countReplayReasoning(replay, replayReasoningCanary); got != 2 {
		t.Fatalf("cross turn/request reasoning count = %d, want 2\nreplay=%s", got, replayAggregationDump(replay))
	}
}

func TestProjectPromptReplayLegacyStrictIdentityDedupes(t *testing.T) {
	conversation := replayAggregationConversation(t, []HistoryEntry{
		replayAggregationUserEntry(t, 1, "request-1", "legacy strict"),
		newAssistantTextEntryWithProviderMetadata(1, "request-1", "legacy text", replayReasoningCanary, "sig-legacy", modeladapter.ReasoningSignatureSourceOpenAIResponses, "rsn_item_legacy", "completed", replayAggregationSummary()),
		replayAggregationLegacyToolCallEntry(t, 1, "request-1", "call-1", "one.txt", "item-1", "fc-1", "completed", replayReasoningCanary, "sig-legacy", modeladapter.ReasoningSignatureSourceOpenAIResponses, "rsn_item_legacy"),
		replayAggregationLegacyToolCallEntry(t, 1, "request-1", "call-2", "two.txt", "item-2", "fc-2", "completed", replayReasoningCanary, "sig-legacy", modeladapter.ReasoningSignatureSourceOpenAIResponses, "rsn_item_legacy"),
		replayAggregationToolResultEntry(t, 1, "request-1", "", "call-1", "one.txt", "edited one"),
		replayAggregationToolResultEntry(t, 1, "request-1", "", "call-2", "two.txt", "edited two"),
	})
	canonical := marshalReplayCanonicalHistory(t, conversation)

	replay := projectReplayAggregation(t, conversation)
	if got := countReplayReasoning(replay, replayReasoningCanary); got != 1 {
		t.Fatalf("legacy strict-identity reasoning count = %d, want 1\nreplay=%s", got, replayAggregationDump(replay))
	}
	assertReplayToolSequence(t, replay, []replayExpectedTool{
		{ID: "call-1", Path: "one.txt", ProviderItemID: "item-1", ProviderCallID: "fc-1", ProviderStatus: "completed", Result: "edited one"},
		{ID: "call-2", Path: "two.txt", ProviderItemID: "item-2", ProviderCallID: "fc-2", ProviderStatus: "completed", Result: "edited two"},
	})
	assertCanonicalHistoryUnchanged(t, conversation, canonical)
}

func TestProjectPromptReplayLegacyInsufficientIdentityKeepsOldBehavior(t *testing.T) {
	conversation := replayAggregationConversation(t, []HistoryEntry{
		replayAggregationUserEntry(t, 1, "request-1", "legacy conservative"),
		newAssistantTextEntry(1, "request-1", "legacy text", replayReasoningCanary, ""),
		newToolCallEntry(1, "request-1", "call-1", "Edit", replayReasoningCanary, "", transcriptTestEditToolCall(t, "one.txt")),
		newToolCallEntry(1, "request-1", "call-2", "Edit", replayReasoningCanary, "", transcriptTestEditToolCall(t, "two.txt")),
		newToolResultEntry(1, "request-1", "call-1", "Edit", `{"path":"one.txt"}`, "edited one", "", transcriptTestEditToolCall(t, "one.txt")),
		newToolResultEntry(1, "request-1", "call-2", "Edit", `{"path":"two.txt"}`, "edited two", "", transcriptTestEditToolCall(t, "two.txt")),
	})

	replay := projectReplayAggregation(t, conversation)
	if got := countReplayReasoning(replay, replayReasoningCanary); got < 2 {
		t.Fatalf("legacy insufficient-identity reasoning count = %d, want old duplicate behavior (>=2)\nreplay=%s", got, replayAggregationDump(replay))
	}
}

func TestProjectPromptReplayToolResultFallbackAggregatesSameModelCall(t *testing.T) {
	conversation := replayAggregationConversation(t, []HistoryEntry{
		replayAggregationUserEntry(t, 1, "request-1", "fallback tools"),
		replayAggregationToolResultFallbackEntry(t, 1, "request-1", "model-call-1", "call-1", "one.txt", "edited one", "item-1", "fc-1", "completed"),
		replayAggregationToolResultFallbackEntry(t, 1, "request-1", "model-call-1", "call-2", "two.txt", "edited two", "item-2", "fc-2", "completed"),
	})
	canonical := marshalReplayCanonicalHistory(t, conversation)

	replay := projectReplayAggregation(t, conversation)
	if got := countReplayReasoning(replay, replayReasoningCanary); got != 1 {
		t.Fatalf("tool_result fallback reasoning count = %d, want 1\nreplay=%s", got, replayAggregationDump(replay))
	}
	assertReplayToolSequence(t, replay, []replayExpectedTool{
		{ID: "call-1", Path: "one.txt", ProviderItemID: "item-1", ProviderCallID: "fc-1", ProviderStatus: "completed", Result: "edited one"},
		{ID: "call-2", Path: "two.txt", ProviderItemID: "item-2", ProviderCallID: "fc-2", ProviderStatus: "completed", Result: "edited two"},
	})
	assertCanonicalHistoryUnchanged(t, conversation, canonical)
}

func TestProjectPromptReplayInterruptedOutputKeepsModelCallReasoning(t *testing.T) {
	conversation := replayAggregationConversation(t, []HistoryEntry{
		replayAggregationUserEntry(t, 1, "request-1", "interrupted"),
		withHistoryModelCallID(newAssistantTextEntryWithProviderMetadata(1, "request-1", "partial answer", replayReasoningCanary, "sig-int", modeladapter.ReasoningSignatureSourceAnthropic, "rsn_item_int", "incomplete", replayAggregationSummary()), "model-call-1"),
	})

	replay := projectReplayAggregation(t, conversation)
	found := false
	for _, message := range replay {
		if message.Role == "assistant" && strings.TrimSpace(message.Content) == "partial answer" {
			found = true
			assertReplayReasoningTuple(t, message, replayReasoningCanary, "sig-int", modeladapter.ReasoningSignatureSourceAnthropic, "rsn_item_int", "incomplete", replayAggregationSummary())
		}
	}
	if !found {
		t.Fatalf("interrupted assistant output missing from replay: %s", replayAggregationDump(replay))
	}
}

func TestProjectCheckpointAndForkKeepAggregatedReplay(t *testing.T) {
	conversation := replayAggregationConversation(t, []HistoryEntry{
		replayAggregationUserEntry(t, 1, "request-1", "checkpoint fork"),
		withHistoryModelCallID(newAssistantTextEntryWithProviderMetadata(1, "request-1", "parent answer", replayReasoningCanary, "sig-shared", modeladapter.ReasoningSignatureSourceAnthropic, "rsn_item_1", "completed", replayAggregationSummary()), "model-call-1"),
		replayAggregationToolCallEntry(t, 1, "request-1", "model-call-1", "call-1", "one.txt", "item-1", "fc-1", "completed"),
		replayAggregationToolCallEntry(t, 1, "request-1", "model-call-1", "call-2", "two.txt", "item-2", "fc-2", "completed"),
		replayAggregationToolResultEntry(t, 1, "request-1", "model-call-1", "call-1", "one.txt", "edited one"),
		replayAggregationToolResultEntry(t, 1, "request-1", "model-call-1", "call-2", "two.txt", "edited two"),
	})
	before := marshalReplayCanonicalHistory(t, conversation)

	projection, err := NewHistoryProjector().ProjectCheckpointProjection(conversation)
	if err != nil {
		t.Fatalf("ProjectCheckpointProjection() error = %v", err)
	}
	if projection == nil || projection.State == nil || len(projection.State.GetTurns()) != 1 {
		t.Fatalf("checkpoint projection = %#v, want one turn", projection)
	}
	assertCanonicalHistoryUnchanged(t, conversation, before)

	replay := projectReplayAggregation(t, conversation)
	if got := countReplayReasoning(replay, replayReasoningCanary); got != 1 {
		t.Fatalf("checkpoint/fork replay reasoning count = %d, want 1\nreplay=%s", got, replayAggregationDump(replay))
	}
	assertReplayToolSequence(t, replay, []replayExpectedTool{
		{ID: "call-1", Path: "one.txt", ProviderItemID: "item-1", ProviderCallID: "fc-1", ProviderStatus: "completed", Result: "edited one"},
		{ID: "call-2", Path: "two.txt", ProviderItemID: "item-2", ProviderCallID: "fc-2", ProviderStatus: "completed", Result: "edited two"},
	})

	imported, err := importedConversationStateModelMessages(projection.State)
	if err != nil {
		t.Fatalf("importedConversationStateModelMessages() error = %v", err)
	}
	if got := countReplayReasoning(imported, replayReasoningCanary); got != 1 {
		t.Fatalf("fork imported replay reasoning count = %d, want 1\nreplay=%s", got, replayAggregationDump(imported))
	}
}

func TestProjectPromptReplayCompactionPreservesAggregatedCurrentTurn(t *testing.T) {
	conversation := replayAggregationConversation(t, []HistoryEntry{
		replayAggregationUserEntry(t, 1, "request-1", "old question"),
		withHistoryModelCallID(newAssistantTextEntry(1, "request-1", "old answer", "old reasoning", "old-sig"), "model-call-old"),
		replayAggregationUserEntry(t, 2, "request-2", "current question"),
		withHistoryModelCallID(newAssistantTextEntryWithProviderMetadata(2, "request-2", "current plan", replayReasoningCanary, "sig-shared", modeladapter.ReasoningSignatureSourceAnthropic, "rsn_item_1", "completed", replayAggregationSummary()), "model-call-2"),
		replayAggregationToolCallEntry(t, 2, "request-2", "model-call-2", "call-1", "one.txt", "item-1", "fc-1", "completed"),
		replayAggregationToolCallEntry(t, 2, "request-2", "model-call-2", "call-2", "two.txt", "item-2", "fc-2", "completed"),
		replayAggregationToolResultEntry(t, 2, "request-2", "model-call-2", "call-1", "one.txt", "edited one"),
		replayAggregationToolResultEntry(t, 2, "request-2", "model-call-2", "call-2", "two.txt", "edited two"),
	})
	if err := applyCompactionToConversation(conversation, &PendingCompaction{
		Trigger:                   "auto",
		CurrentTurnSeq:            2,
		CurrentRequestID:          "request-2",
		PreserveCurrentTurnInputs: true,
	}, "earlier context summary"); err != nil {
		t.Fatalf("applyCompactionToConversation() error = %v", err)
	}

	var preservedModelCallIDs int
	for _, entry := range conversation.Entries {
		if strings.TrimSpace(entry.ModelCallID) == "model-call-2" {
			preservedModelCallIDs++
		}
		if strings.TrimSpace(entry.Kind) == "tool_call" {
			var payload toolCallEntryPayload
			if err := json.Unmarshal(entry.Payload, &payload); err != nil {
				t.Fatalf("decode compacted tool_call: %v", err)
			}
			if payload.ReasoningContent != replayReasoningCanary {
				t.Fatalf("compaction deleted canonical tool_call reasoning: %#v", payload)
			}
		}
	}
	if preservedModelCallIDs == 0 {
		t.Fatal("compaction dropped current-turn ModelCallID")
	}

	replay := projectReplayAggregation(t, conversation)
	if got := countReplayReasoning(replay, replayReasoningCanary); got != 1 {
		t.Fatalf("compacted replay reasoning count = %d, want 1\nreplay=%s", got, replayAggregationDump(replay))
	}
	if countReplayReasoning(replay, "old reasoning") != 0 {
		t.Fatalf("compacted replay leaked old-turn reasoning: %s", replayAggregationDump(replay))
	}
}

func TestCancelPersistsInterruptedOutputModelCallID(t *testing.T) {
	service, stream, _ := testCheckpointBlobProjection(t)
	conversation, _, _, err := service.snapshotCheckpointConversation(stream)
	if err != nil {
		t.Fatalf("snapshotCheckpointConversation() error = %v", err)
	}
	if _, err := service.store.SaveConversationWithEntries(stream.ConversationID, conversation, conversation.Entries); err != nil {
		t.Fatalf("SaveConversationWithEntries() error = %v", err)
	}

	stream.mu.Lock()
	stream.CurrentModelCallID = "model-call-1"
	stream.ProviderAccumulatedText = "partial answer"
	stream.ProviderAccumulatedReasoning = replayReasoningCanary
	stream.mu.Unlock()

	if err := service.handleCancelIntent(InboundIntent{
		Kind:         "cancel",
		RequestID:    stream.RequestID,
		CancelReason: "[canceled] Superseded by newer request",
	}); err != nil {
		t.Fatalf("handleCancelIntent() error = %v", err)
	}

	persisted, err := service.store.LoadConversation(stream.ConversationID)
	if err != nil {
		t.Fatalf("LoadConversation() error = %v", err)
	}
	found := false
	for _, entry := range persisted.Entries {
		if entry.Kind != "assistant_text" {
			continue
		}
		var payload assistantTextPayload
		if err := json.Unmarshal(entry.Payload, &payload); err != nil {
			t.Fatalf("decode interrupted assistant entry: %v", err)
		}
		if payload.Text != "partial answer" {
			continue
		}
		found = true
		if entry.ModelCallID != "model-call-1" {
			t.Fatalf("interrupted assistant ModelCallID = %q, want model-call-1", entry.ModelCallID)
		}
		if payload.ReasoningContent != replayReasoningCanary {
			t.Fatalf("interrupted assistant reasoning = %q", payload.ReasoningContent)
		}
	}
	if !found {
		t.Fatal("interrupted assistant_text entry was not persisted")
	}
}

type replayExpectedTool struct {
	ID             string
	Path           string
	ProviderItemID string
	ProviderCallID string
	ProviderStatus string
	Result         string
}

func projectReplayAggregation(t *testing.T, conversation *ConversationFile) []modeladapter.Message {
	t.Helper()
	replay, err := NewHistoryProjector().ProjectPromptReplay(conversation)
	if err != nil {
		t.Fatalf("ProjectPromptReplay() error = %v", err)
	}
	return replay
}

func replayAggregationConversation(t *testing.T, entries []HistoryEntry) *ConversationFile {
	t.Helper()
	conversation := &ConversationFile{
		ConversationID:     "conversation-1",
		RootConversationID: "conversation-1",
		Mode:               "agent",
		NextTurnSeq:        1,
		NextEntrySeq:       1,
	}
	appendEntriesInPlace(conversation, entries)
	return conversation
}

func replayAggregationUserEntry(t *testing.T, turnSeq int64, requestID string, text string) HistoryEntry {
	t.Helper()
	payload, err := protojson.Marshal(&agentv1.UserMessage{Text: text, MessageId: "message-" + requestID})
	if err != nil {
		t.Fatalf("marshal user message: %v", err)
	}
	return HistoryEntry{
		TurnSeq:   turnSeq,
		RequestID: requestID,
		Role:      "user",
		Kind:      "user_message",
		Payload:   payload,
	}
}

func replayAggregationToolCallEntry(t *testing.T, turnSeq int64, requestID string, modelCallID string, toolCallID string, path string, providerItemID string, providerCallID string, providerStatus string) HistoryEntry {
	t.Helper()
	return withHistoryModelCallID(newToolCallEntryWithProviderMetadata(
		turnSeq,
		requestID,
		toolCallID,
		"Edit",
		replayReasoningCanary,
		"sig-shared",
		modeladapter.ReasoningSignatureSourceAnthropic,
		"rsn_item_1",
		"completed",
		replayAggregationSummary(),
		providerItemID,
		providerCallID,
		providerStatus,
		transcriptTestEditToolCall(t, path),
	), modelCallID)
}

func replayAggregationLegacyToolCallEntry(t *testing.T, turnSeq int64, requestID string, toolCallID string, path string, providerItemID string, providerCallID string, providerStatus string, reasoning string, signature string, source string, itemID string) HistoryEntry {
	t.Helper()
	return newToolCallEntryWithProviderMetadata(
		turnSeq,
		requestID,
		toolCallID,
		"Edit",
		reasoning,
		signature,
		source,
		itemID,
		"completed",
		replayAggregationSummary(),
		providerItemID,
		providerCallID,
		providerStatus,
		transcriptTestEditToolCall(t, path),
	)
}

func replayAggregationToolResultEntry(t *testing.T, turnSeq int64, requestID string, modelCallID string, toolCallID string, path string, result string) HistoryEntry {
	t.Helper()
	return withHistoryModelCallID(newToolResultEntry(
		turnSeq,
		requestID,
		toolCallID,
		"Edit",
		`{"path":"`+path+`"}`,
		result,
		"",
		transcriptTestEditToolCall(t, path),
	), modelCallID)
}

func replayAggregationToolResultFallbackEntry(t *testing.T, turnSeq int64, requestID string, modelCallID string, toolCallID string, path string, result string, providerItemID string, providerCallID string, providerStatus string) HistoryEntry {
	t.Helper()
	payload, err := json.Marshal(toolResultEntryPayload{
		ToolCallID:               toolCallID,
		ToolName:                 "Edit",
		Arguments:                `{"path":"` + path + `"}`,
		ResultText:               result,
		ReasoningContent:         replayReasoningCanary,
		ReasoningSignature:       "sig-shared",
		ReasoningSignatureSource: modeladapter.ReasoningSignatureSourceAnthropic,
		ReasoningItemID:          "rsn_item_1",
		ReasoningStatus:          "completed",
		ReasoningSummary:         replayAggregationSummary(),
		ProviderItemID:           providerItemID,
		ProviderCallID:           providerCallID,
		ProviderStatus:           providerStatus,
		ToolCall:                 transcriptTestEditToolCall(t, path),
	})
	if err != nil {
		t.Fatalf("marshal tool_result fallback: %v", err)
	}
	return HistoryEntry{
		TurnSeq:     turnSeq,
		RequestID:   requestID,
		ModelCallID: modelCallID,
		Role:        "tool",
		Kind:        "tool_result",
		ToolCallID:  toolCallID,
		Payload:     payload,
	}
}

func replayAggregationSummary() json.RawMessage {
	return json.RawMessage(`[{"type":"summary_text","text":"shared plan"}]`)
}

func replayAggregationOtherSummary() json.RawMessage {
	return json.RawMessage(`[{"type":"summary_text","text":"other plan"}]`)
}

func countReplayReasoning(messages []modeladapter.Message, reasoning string) int {
	count := 0
	needle := strings.TrimSpace(reasoning)
	for _, message := range messages {
		content := strings.TrimSpace(message.ReasoningContent)
		if content == "" || needle == "" {
			continue
		}
		if content == needle {
			count++
			continue
		}
		count += strings.Count(content, needle)
	}
	return count
}

func firstAssistantReplayMessage(t *testing.T, messages []modeladapter.Message) modeladapter.Message {
	t.Helper()
	assistants := assistantReplayMessages(t, messages)
	return assistants[0]
}

func assistantReplayMessages(t *testing.T, messages []modeladapter.Message) []modeladapter.Message {
	t.Helper()
	assistants := make([]modeladapter.Message, 0)
	for _, message := range messages {
		if strings.TrimSpace(message.Role) != "assistant" {
			continue
		}
		assistants = append(assistants, message)
	}
	if len(assistants) == 0 {
		t.Fatalf("assistant replay message not found\nreplay=%s", replayAggregationDump(messages))
	}
	return assistants
}

func firstReplayReasoningMessage(t *testing.T, messages []modeladapter.Message, reasoning string) modeladapter.Message {
	t.Helper()
	for _, message := range messages {
		if strings.TrimSpace(message.ReasoningContent) == reasoning {
			return message
		}
	}
	t.Fatalf("no replay message carried reasoning %q: %s", reasoning, replayAggregationDump(messages))
	return modeladapter.Message{}
}

func assertReplayReasoningTuple(t *testing.T, message modeladapter.Message, content string, signature string, source string, itemID string, status string, summary json.RawMessage) {
	t.Helper()
	if strings.TrimSpace(message.ReasoningContent) != content {
		t.Fatalf("reasoning content = %q, want %q", message.ReasoningContent, content)
	}
	if strings.TrimSpace(message.ReasoningSignature) != signature {
		t.Fatalf("reasoning signature = %q, want %q", message.ReasoningSignature, signature)
	}
	if strings.TrimSpace(message.ReasoningSignatureSource) != source {
		t.Fatalf("reasoning signature source = %q, want %q", message.ReasoningSignatureSource, source)
	}
	if strings.TrimSpace(message.OpenAIResponsesReasoningID) != itemID {
		t.Fatalf("reasoning item id = %q, want %q", message.OpenAIResponsesReasoningID, itemID)
	}
	if strings.TrimSpace(message.OpenAIResponsesReasoningStatus) != status {
		t.Fatalf("reasoning status = %q, want %q", message.OpenAIResponsesReasoningStatus, status)
	}
	if string(bytes.TrimSpace(message.OpenAIResponsesReasoningSummary)) != string(bytes.TrimSpace(summary)) {
		t.Fatalf("reasoning summary = %s, want %s", message.OpenAIResponsesReasoningSummary, summary)
	}
}

func assertReplayToolSequence(t *testing.T, messages []modeladapter.Message, want []replayExpectedTool) {
	t.Helper()
	got := collectReplayExpectedTools(messages)
	if len(got) != len(want) {
		t.Fatalf("replay tools = %#v, want %#v\nreplay=%s", got, want, replayAggregationDump(messages))
	}
	for index := range want {
		if got[index] != want[index] {
			t.Fatalf("replay tool[%d] = %#v, want %#v\nreplay=%s", index, got[index], want[index], replayAggregationDump(messages))
		}
	}
}

func collectReplayExpectedTools(messages []modeladapter.Message) []replayExpectedTool {
	results := make(map[string]string)
	order := make([]replayExpectedTool, 0)
	seen := make(map[string]int)
	for _, message := range messages {
		if strings.TrimSpace(message.Role) == "tool" {
			results[strings.TrimSpace(message.ToolCallID)] = strings.TrimSpace(message.Content)
			continue
		}
		if strings.TrimSpace(message.Role) != "assistant" {
			continue
		}
		for _, toolCall := range message.ToolCalls {
			id := strings.TrimSpace(toolCall.ID)
			if id == "" {
				continue
			}
			item := replayExpectedTool{
				ID:             id,
				Path:           replayToolCallPath(toolCall.Function.Arguments),
				ProviderItemID: strings.TrimSpace(toolCall.OpenAIResponsesID),
				ProviderCallID: strings.TrimSpace(toolCall.OpenAIResponsesCallID),
				ProviderStatus: strings.TrimSpace(toolCall.OpenAIResponsesStatus),
			}
			if existing, ok := seen[id]; ok {
				order[existing] = item
				continue
			}
			seen[id] = len(order)
			order = append(order, item)
		}
	}
	for index, item := range order {
		item.Result = results[item.ID]
		order[index] = item
	}
	return order
}

func replayToolCallPath(arguments string) string {
	var payload struct {
		Path string `json:"path"`
	}
	if err := json.Unmarshal([]byte(arguments), &payload); err != nil {
		return ""
	}
	return payload.Path
}

func marshalReplayCanonicalHistory(t *testing.T, conversation *ConversationFile) []byte {
	t.Helper()
	encoded, err := json.Marshal(conversation.Entries)
	if err != nil {
		t.Fatalf("marshal canonical history: %v", err)
	}
	return encoded
}

func assertCanonicalHistoryUnchanged(t *testing.T, conversation *ConversationFile, before []byte) {
	t.Helper()
	after := marshalReplayCanonicalHistory(t, conversation)
	if !bytes.Equal(before, after) {
		t.Fatalf("projection mutated canonical history\nbefore=%s\nafter=%s", before, after)
	}
}

func replayAggregationDump(messages []modeladapter.Message) string {
	encoded, err := json.MarshalIndent(messages, "", "  ")
	if err != nil {
		return err.Error()
	}
	return string(encoded)
}
