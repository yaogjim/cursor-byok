package forwarder

import "testing"

func TestUpdateConversationTokenStateUsesLatestProviderInputAsCompactionAnchor(t *testing.T) {
	conversation := &ConversationFile{
		ConversationID:        "conversation-1",
		RootConversationID:    "conversation-1",
		Mode:                  "agent",
		TokenDetailsMaxTokens: 50_000,
	}
	stream := &ActiveStream{
		ConversationID:         conversation.ConversationID,
		ModelID:                "test-model",
		CheckpointConversation: conversation,
	}
	service := &Service{}

	if err := service.updateConversationTokenState(stream, conversation.ConversationID, turnUsageSnapshot{
		InputTokens:  48_000,
		OutputTokens: 1_000,
		UsagePresent: true,
	}, "model-call-1", true); err != nil {
		t.Fatalf("first updateConversationTokenState() error = %v", err)
	}
	first := stream.CheckpointConversation
	if !first.AutoCompactionPending || first.AutoCompactionPromptTokens != 48_000 {
		t.Fatalf("first compaction state = pending:%v prompt:%d, want pending at 48000", first.AutoCompactionPending, first.AutoCompactionPromptTokens)
	}

	if err := service.updateConversationTokenState(stream, conversation.ConversationID, turnUsageSnapshot{
		InputTokens:       2_000,
		CacheReadTokens:   18_000,
		OutputTokens:      30_000,
		UsagePresent:      true,
		CacheReadPresent:  true,
		CacheWritePresent: true,
	}, "model-call-2", true); err != nil {
		t.Fatalf("second updateConversationTokenState() error = %v", err)
	}
	latest := stream.CheckpointConversation
	if latest.TokenDetailsUsedTokens != 20_000 {
		t.Fatalf("used tokens = %d, want latest provider input context 20000", latest.TokenDetailsUsedTokens)
	}
	if latest.AutoCompactionPending || latest.AutoCompactionPromptTokens != 0 || latest.AutoCompactionSourceModelCallID != "" {
		t.Fatalf("latest compaction state = pending:%v prompt:%d source:%q, want cleared from latest input context", latest.AutoCompactionPending, latest.AutoCompactionPromptTokens, latest.AutoCompactionSourceModelCallID)
	}
}

func TestUpdateConversationTokenStateIgnoresLargeOutputForCompactionAnchor(t *testing.T) {
	conversation := &ConversationFile{
		ConversationID:        "conversation-large-output",
		RootConversationID:    "conversation-large-output",
		Mode:                  "agent",
		TokenDetailsMaxTokens: 50_000,
	}
	stream := &ActiveStream{
		ConversationID:         conversation.ConversationID,
		ModelID:                "test-model",
		CheckpointConversation: conversation,
	}
	service := &Service{}

	if err := service.updateConversationTokenState(stream, conversation.ConversationID, turnUsageSnapshot{
		InputTokens:  35_000,
		OutputTokens: 40_000,
		UsagePresent: true,
	}, "model-call-large-output", true); err != nil {
		t.Fatalf("updateConversationTokenState() error = %v", err)
	}
	got := stream.CheckpointConversation
	if got.TokenDetailsUsedTokens != 35_000 {
		t.Fatalf("used tokens = %d, want 35000 without OutputTokens", got.TokenDetailsUsedTokens)
	}
	if got.AutoCompactionPending || got.AutoCompactionPromptTokens != 0 || got.AutoCompactionSourceModelCallID != "" {
		t.Fatalf("compaction state = pending:%v prompt:%d source:%q, want cleared because remaining 15000 > reserve 10000", got.AutoCompactionPending, got.AutoCompactionPromptTokens, got.AutoCompactionSourceModelCallID)
	}
}

func TestUpdateConversationTokenStateIncludesCacheReadAndWriteInAnchor(t *testing.T) {
	conversation := &ConversationFile{
		ConversationID:        "conversation-cache",
		RootConversationID:    "conversation-cache",
		Mode:                  "agent",
		TokenDetailsMaxTokens: 50_000,
	}
	stream := &ActiveStream{
		ConversationID:         conversation.ConversationID,
		ModelID:                "test-model",
		CheckpointConversation: conversation,
	}
	service := &Service{}

	if err := service.updateConversationTokenState(stream, conversation.ConversationID, turnUsageSnapshot{
		InputTokens:       2_000,
		CacheReadTokens:   30_000,
		CacheWriteTokens:  10_000,
		OutputTokens:      8_000,
		UsagePresent:      true,
		CacheReadPresent:  true,
		CacheWritePresent: true,
	}, "model-call-cache", true); err != nil {
		t.Fatalf("updateConversationTokenState() error = %v", err)
	}
	got := stream.CheckpointConversation
	if got.TokenDetailsUsedTokens != 42_000 {
		t.Fatalf("used tokens = %d, want 42000 from input+cache read+cache write", got.TokenDetailsUsedTokens)
	}
	if !got.AutoCompactionPending || got.AutoCompactionPromptTokens != 42_000 || got.AutoCompactionSourceModelCallID != "model-call-cache" {
		t.Fatalf("compaction state = pending:%v prompt:%d source:%q, want pending at 42000 from cache-inclusive prompt", got.AutoCompactionPending, got.AutoCompactionPromptTokens, got.AutoCompactionSourceModelCallID)
	}
}

func TestUpdateConversationTokenStateLatestCallOverwritesPendingWithoutAccumulating(t *testing.T) {
	conversation := &ConversationFile{
		ConversationID:        "conversation-overwrite",
		RootConversationID:    "conversation-overwrite",
		Mode:                  "agent",
		TokenDetailsMaxTokens: 50_000,
	}
	stream := &ActiveStream{
		ConversationID:         conversation.ConversationID,
		ModelID:                "test-model",
		CheckpointConversation: conversation,
	}
	service := &Service{}

	if err := service.updateConversationTokenState(stream, conversation.ConversationID, turnUsageSnapshot{
		InputTokens:  48_000,
		OutputTokens: 2_000,
		UsagePresent: true,
	}, "model-call-1", true); err != nil {
		t.Fatalf("first updateConversationTokenState() error = %v", err)
	}
	if err := service.updateConversationTokenState(stream, conversation.ConversationID, turnUsageSnapshot{
		InputTokens:  47_000,
		OutputTokens: 3_000,
		UsagePresent: true,
	}, "model-call-2", true); err != nil {
		t.Fatalf("second updateConversationTokenState() error = %v", err)
	}
	got := stream.CheckpointConversation
	if got.TokenDetailsUsedTokens != 47_000 {
		t.Fatalf("used tokens = %d, want latest call 47000 not accumulated 95000", got.TokenDetailsUsedTokens)
	}
	if !got.AutoCompactionPending || got.AutoCompactionPromptTokens != 47_000 || got.AutoCompactionSourceModelCallID != "model-call-2" {
		t.Fatalf("compaction state = pending:%v prompt:%d source:%q, want overwritten pending at latest 47000", got.AutoCompactionPending, got.AutoCompactionPromptTokens, got.AutoCompactionSourceModelCallID)
	}
}

func TestUpdateConversationTokenStateFinalizeFalseDoesNotChangePending(t *testing.T) {
	conversation := &ConversationFile{
		ConversationID:        "conversation-partial",
		RootConversationID:    "conversation-partial",
		Mode:                  "agent",
		TokenDetailsMaxTokens: 50_000,
	}
	stream := &ActiveStream{
		ConversationID:         conversation.ConversationID,
		ModelID:                "test-model",
		CheckpointConversation: conversation,
	}
	service := &Service{}

	if err := service.updateConversationTokenState(stream, conversation.ConversationID, turnUsageSnapshot{
		InputTokens:  48_000,
		OutputTokens: 1_000,
		UsagePresent: true,
	}, "model-call-partial", false); err != nil {
		t.Fatalf("partial-only updateConversationTokenState() error = %v", err)
	}
	partial := stream.CheckpointConversation
	if partial.TokenDetailsUsedTokens != 48_000 {
		t.Fatalf("used tokens after finalize=false = %d, want 48000", partial.TokenDetailsUsedTokens)
	}
	if partial.AutoCompactionPending || partial.AutoCompactionPromptTokens != 0 || partial.AutoCompactionSourceModelCallID != "" {
		t.Fatalf("compaction state after finalize=false seed = pending:%v prompt:%d source:%q, want unchanged empty pending", partial.AutoCompactionPending, partial.AutoCompactionPromptTokens, partial.AutoCompactionSourceModelCallID)
	}

	if err := service.updateConversationTokenState(stream, conversation.ConversationID, turnUsageSnapshot{
		InputTokens:  48_000,
		OutputTokens: 1_000,
		UsagePresent: true,
	}, "model-call-final", true); err != nil {
		t.Fatalf("finalize updateConversationTokenState() error = %v", err)
	}
	if err := service.updateConversationTokenState(stream, conversation.ConversationID, turnUsageSnapshot{
		InputTokens:  2_000,
		OutputTokens: 40_000,
		UsagePresent: true,
	}, "model-call-partial-2", false); err != nil {
		t.Fatalf("second finalize=false updateConversationTokenState() error = %v", err)
	}
	got := stream.CheckpointConversation
	if got.TokenDetailsUsedTokens != 2_000 {
		t.Fatalf("used tokens after later finalize=false = %d, want latest prompt 2000", got.TokenDetailsUsedTokens)
	}
	if !got.AutoCompactionPending || got.AutoCompactionPromptTokens != 48_000 || got.AutoCompactionSourceModelCallID != "model-call-final" {
		t.Fatalf("compaction state after finalize=false = pending:%v prompt:%d source:%q, want previous pending at 48000", got.AutoCompactionPending, got.AutoCompactionPromptTokens, got.AutoCompactionSourceModelCallID)
	}
}
