package forwarder

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"cursor/gen/agentv1"
	"google.golang.org/protobuf/encoding/protojson"

	modeladapter "cursor/internal/backend/agent/model"
	legacyruntime "cursor/internal/runtime"
)

func toolImageMessage(callID string, path string, data []byte) modeladapter.Message {
	return modeladapter.Message{
		Role:       "tool",
		Content:    "Read image file: " + path,
		ToolCallID: callID,
		ContentParts: []modeladapter.ContentPart{
			{Type: "text", Text: "Read image file: " + path},
			{Type: "image", Image: &modeladapter.ImageContent{MIMEType: "image/png", Path: path, Data: data}},
		},
	}
}

func hasImagePart(message modeladapter.Message) bool {
	for _, part := range message.ContentParts {
		if part.Image != nil {
			return true
		}
	}
	return false
}

func TestProviderRequestByteBudgetDerivesFromConnectTimeout(t *testing.T) {
	const (
		mib = int64(1 << 20)
	)
	cases := []struct {
		timeout int64
		want    int64
	}{
		{timeout: 30, want: 30 * 256 * 1024},
		{timeout: 5, want: 2 * mib},
		{timeout: 120, want: 16 * mib},
		{timeout: 0, want: 30 * 256 * 1024},
		{timeout: -1, want: 30 * 256 * 1024},
	}
	for _, item := range cases {
		if got := providerRequestByteBudget(item.timeout); got != item.want {
			t.Fatalf("budget(%d) = %d, want %d", item.timeout, got, item.want)
		}
	}
}

func TestProviderImageRetentionKeepsImagesWithinBudget(t *testing.T) {
	messages := []modeladapter.Message{
		toolImageMessage("call-1", "/tmp/a.png", []byte(strings.Repeat("a", 1024))),
	}
	retained, retention := applyProviderImageBudget(messages, nil, 1<<20, 2)
	if retention.Omitted != 0 || retention.Deduped != 0 {
		t.Fatalf("unexpected retention: %#v", retention)
	}
	if !hasImagePart(retained[0]) {
		t.Fatalf("image dropped within budget: %#v", retained[0])
	}
	if retention.RetainedBytes <= 0 || retention.EstimatedBytes > retention.BudgetBytes {
		t.Fatalf("retention accounting = %#v", retention)
	}
	if retention.Summary() == "" || !strings.Contains(retention.Summary(), "budget_bytes=") {
		t.Fatalf("summary should always expose budget: %q", retention.Summary())
	}
}

func TestProviderImageRetentionDeduplicatesIdenticalImages(t *testing.T) {
	data := []byte(strings.Repeat("x", 2048))
	messages := []modeladapter.Message{
		toolImageMessage("call-1", "/tmp/a.png", data),
		toolImageMessage("call-2", "/tmp/a-again.png", data),
	}
	retained, retention := applyProviderImageBudget(messages, nil, 1<<20, 2)
	if retention.Deduped != 1 || retention.Omitted != 0 {
		t.Fatalf("dedup accounting = %#v", retention)
	}
	if !hasImagePart(retained[0]) {
		t.Fatalf("first occurrence should keep image: %#v", retained[0])
	}
	if hasImagePart(retained[1]) {
		t.Fatalf("duplicate should lose image: %#v", retained[1])
	}
	if !strings.Contains(retained[1].Content, "duplicate of the image returned by tool call call-1") {
		t.Fatalf("duplicate placeholder = %q", retained[1].Content)
	}
	if len(retained[1].ContentParts) != 0 {
		t.Fatalf("duplicate placeholder parts = %#v", retained[1].ContentParts)
	}
}

func TestProviderImageRetentionOmitsOldestImagesFirst(t *testing.T) {
	const mib = 1 << 20
	messages := []modeladapter.Message{
		toolImageMessage("call-old", "/tmp/old.png", []byte(strings.Repeat("o", mib))),
		toolImageMessage("call-mid", "/tmp/mid.png", []byte(strings.Repeat("m", mib))),
		toolImageMessage("call-new", "/tmp/new.png", []byte(strings.Repeat("n", mib))),
	}
	budget := int64(3 * mib)
	retained, retention := applyProviderImageBudget(messages, nil, budget, 2)
	// 3 MiB 预算约可容纳两张 1 MiB 图片的 base64，最旧一张必须被占位。
	if hasImagePart(retained[0]) {
		t.Fatalf("oldest image should be omitted: %#v", retention)
	}
	if !hasImagePart(retained[1]) || !hasImagePart(retained[2]) {
		t.Fatalf("newest images should be kept: %#v", retention)
	}
	if retention.Omitted != 1 || retention.OmittedBytes <= 0 {
		t.Fatalf("omit accounting = %#v", retention)
	}
	if !strings.Contains(retained[0].Content, "/tmp/old.png") || !strings.Contains(retained[0].Content, "Read") {
		t.Fatalf("omitted placeholder should keep path and re-read hint: %q", retained[0].Content)
	}
	if retention.EstimatedBytes > budget {
		t.Fatalf("estimated bytes %d exceed budget %d", retention.EstimatedBytes, budget)
	}
}

func TestProviderImageRetentionLeavesNonToolImagesAlone(t *testing.T) {
	userImage := modeladapter.Message{
		Role:    "user",
		Content: "screenshot",
		ContentParts: []modeladapter.ContentPart{
			{Type: "text", Text: "screenshot"},
			{Type: "image", Image: &modeladapter.ImageContent{MIMEType: "image/png", Path: "/tmp/user.png", Data: []byte(strings.Repeat("u", 1<<20))}},
		},
	}
	retained, retention := applyProviderImageBudget([]modeladapter.Message{userImage}, nil, 1024, 2)
	if retention.Omitted != 0 || !hasImagePart(retained[0]) {
		t.Fatalf("non-tool image must not be trimmed: %#v %#v", retention, retained[0])
	}
}

func TestApplyProviderImageBudgetDoesNotMutateInput(t *testing.T) {
	original := toolImageMessage("call-1", "/tmp/a.png", []byte(strings.Repeat("a", 4096)))
	messages := []modeladapter.Message{original}
	_, retention := applyProviderImageBudget(messages, nil, 1024, 2)
	if retention.Omitted != 1 {
		t.Fatalf("expected omission: %#v", retention)
	}
	if !hasImagePart(messages[0]) {
		t.Fatalf("input message mutated: %#v", messages[0])
	}
}

func TestProviderImageRetentionSummaryOmitsContent(t *testing.T) {
	messages := []modeladapter.Message{
		toolImageMessage("call-1", "/secret/path/a.png", []byte(strings.Repeat("a", 4096))),
	}
	_, retention := applyProviderImageBudget(messages, []json.RawMessage{json.RawMessage(`{"type":"function"}`)}, 1024, 2)
	summary := retention.Summary()
	if strings.Contains(summary, "/secret/path") || strings.Contains(summary, "aaaa") {
		t.Fatalf("summary leaked content: %q", summary)
	}
	for _, want := range []string{"deduped=", "omitted=", "omitted_bytes=", "retained_image_bytes=", "budget_bytes=", "estimated_bytes="} {
		if !strings.Contains(summary, want) {
			t.Fatalf("summary missing %q: %q", want, summary)
		}
	}
}

type providerImageBudgetTestResolver struct {
	timeoutSeconds int
}

func (resolver providerImageBudgetTestResolver) SelectChannelForModel(context.Context, string) (*legacyruntime.ResolvedChannel, error) {
	return &legacyruntime.ResolvedChannel{}, nil
}

func (resolver providerImageBudgetTestResolver) SelectChannelPlanForModel(context.Context, string) (*legacyruntime.ChannelPlan, error) {
	return &legacyruntime.ChannelPlan{ConnectTimeoutSeconds: resolver.timeoutSeconds}, nil
}

func TestServiceProviderImageRetentionUsesChannelConnectTimeout(t *testing.T) {
	const mib = 1 << 20
	compiled := CompiledConversation{Messages: []modeladapter.Message{
		toolImageMessage("call-old", "/tmp/old.png", []byte(strings.Repeat("o", mib))),
		toolImageMessage("call-new", "/tmp/new.png", []byte(strings.Repeat("n", mib))),
	}}

	service := &Service{resolver: providerImageBudgetTestResolver{timeoutSeconds: 120}}
	retained, summary := service.applyProviderImageRetention(compiled, "model-1", 2)
	if !strings.Contains(summary, "budget_bytes=16777216") || !strings.Contains(summary, "omitted=0") {
		t.Fatalf("wide timeout should keep both images: %q", summary)
	}
	if !hasImagePart(retained.Messages[0]) || !hasImagePart(retained.Messages[1]) {
		t.Fatalf("images trimmed under wide budget: %#v", retained.Messages)
	}

	service = &Service{resolver: providerImageBudgetTestResolver{timeoutSeconds: 5}}
	retained, summary = service.applyProviderImageRetention(compiled, "model-1", 2)
	if hasImagePart(retained.Messages[0]) || !hasImagePart(retained.Messages[1]) {
		t.Fatalf("short timeout should omit oldest image only: %q", summary)
	}
	if !strings.Contains(summary, "budget_bytes=2097152") || !strings.Contains(summary, "omitted=1") {
		t.Fatalf("short timeout retention summary = %q", summary)
	}
}

func TestServiceProviderImageRetentionFallsBackToDefaultTimeout(t *testing.T) {
	service := &Service{resolver: compactionHTTPChannelResolver{}}
	_, summary := service.applyProviderImageRetention(CompiledConversation{}, "model-1", 2)
	if !strings.Contains(summary, "budget_bytes=7864320") {
		t.Fatalf("default timeout budget = %q", summary)
	}
}

func TestSelectedUserImagesReplayThroughServiceBudget(t *testing.T) {
	entries := make([]HistoryEntry, 0, 3)
	for turn := int64(1); turn <= 2; turn++ {
		payload, err := protojson.Marshal(&agentv1.UserMessage{
			Text: "screenshot question",
			SelectedContext: &agentv1.SelectedContext{SelectedImages: []*agentv1.SelectedImage{{
				MimeType:     "image/png",
				DataOrBlobId: &agentv1.SelectedImage_Data{Data: []byte(strings.Repeat("x", 1<<20))},
			}}},
		})
		if err != nil {
			t.Fatal(err)
		}
		entries = append(entries, HistoryEntry{Kind: "user_message", TurnSeq: turn, Payload: payload})
	}
	// 尾部同为 user role 的提醒不能改变当前附图的保护范围。
	entries = append(entries, replayAggregationUserEntry(t, 2, "current", "tail reminder"))
	messages, err := NewHistoryProjector().ProjectPromptReplay(&ConversationFile{Entries: entries, NextTurnSeq: 3})
	if err != nil {
		t.Fatal(err)
	}
	if len(messages) != 3 || messages[0].SourceTurnSeq != 1 || messages[1].SourceTurnSeq != 2 {
		t.Fatalf("replay lost source turns: %#v", messages)
	}
	service := &Service{resolver: providerImageBudgetTestResolver{timeoutSeconds: 5}}
	retained, summary := service.applyProviderImageRetention(CompiledConversation{Messages: messages}, "model-1", 2)
	if hasImagePart(retained.Messages[0]) || !hasImagePart(retained.Messages[1]) {
		t.Fatalf("wrong user image retained: %s", summary)
	}
	if !strings.Contains(summary, "user_omitted=1") || !strings.Contains(summary, "over_budget=false") {
		t.Fatalf("retention summary = %s", summary)
	}
	if !hasImagePart(messages[0]) || messages[0].ContentParts[1].Image == nil {
		t.Fatal("budget changed original replay messages")
	}
}

func TestProviderImageRetentionProtectsUnknownTurnUserImages(t *testing.T) {
	message := userImageMessage("unknown turn", "", []byte(strings.Repeat("x", 4096)))
	message.SourceTurnSeq = 0
	retained, retention := applyProviderImageBudget([]modeladapter.Message{message}, nil, 1024, 2)
	if !hasImagePart(retained[0]) || retention.UserOmitted != 0 || retention.OverBudgetBytes <= 0 {
		t.Fatalf("unknown turn should remain protected: %#v", retention)
	}
}

func userImageMessage(text string, path string, data []byte) modeladapter.Message {
	return modeladapter.Message{
		SourceTurnSeq: 1,
		Role:          "user",
		Content:       text,
		ContentParts: []modeladapter.ContentPart{
			{Type: "text", Text: text},
			{Type: "image", Image: &modeladapter.ImageContent{MIMEType: "image/png", Path: path, Data: data}},
		},
	}
}

func TestProviderImageRetentionProtectsCurrentTurnUserImages(t *testing.T) {
	const mib = 1 << 20
	messages := []modeladapter.Message{
		userImageMessage("old screenshot question", "/tmp/old-shot.png", []byte(strings.Repeat("o", mib))),
		userImageMessage("current screenshot question", "/tmp/current-shot.png", []byte(strings.Repeat("c", mib))),
	}
	messages[len(messages)-1].SourceTurnSeq = 2
	retained, retention := applyProviderImageBudget(messages, nil, 2*mib+200*1024, 2)
	if hasImagePart(retained[0]) {
		t.Fatalf("historical user image should be omitted first: %#v", retention)
	}
	if !hasImagePart(retained[1]) {
		t.Fatalf("current turn user image must be protected: %#v", retention)
	}
	if !strings.Contains(retained[0].Content, "old screenshot question") {
		t.Fatalf("user query text must survive omission: %q", retained[0].Content)
	}
	if !strings.Contains(retained[0].Content, "ask the user to re-attach") {
		t.Fatalf("user image placeholder = %q", retained[0].Content)
	}
	if retention.UserOmitted != 1 || retention.Omitted != 1 {
		t.Fatalf("user omission accounting = %#v", retention)
	}
}

func TestProviderImageRetentionOmitsToolImagesBeforeUserImages(t *testing.T) {
	const mib = 1 << 20
	messages := []modeladapter.Message{
		toolImageMessage("call-1", "/tmp/tool.png", []byte(strings.Repeat("t", mib))),
		userImageMessage("what is in this screenshot", "/tmp/history-shot.png", []byte(strings.Repeat("h", mib))),
		userImageMessage("current question", "/tmp/current-shot.png", []byte(strings.Repeat("c", 100))),
	}
	messages[len(messages)-1].SourceTurnSeq = 2
	retained, retention := applyProviderImageBudget(messages, nil, 2*mib+200*1024, 2)
	if hasImagePart(retained[0]) {
		t.Fatalf("oldest tool image should be omitted first: %#v", retention)
	}
	if !hasImagePart(retained[1]) {
		t.Fatalf("historical user image should outrank tool image: %#v", retention)
	}
	if !hasImagePart(retained[2]) {
		t.Fatalf("current user image must be protected: %#v", retention)
	}
	if retention.UserOmitted != 0 {
		t.Fatalf("user images should not be omitted here: %#v", retention)
	}
}

func TestProviderImageRetentionKeepsCurrentUserImageWhenOverBudget(t *testing.T) {
	messages := []modeladapter.Message{
		userImageMessage("current question", "/tmp/current-shot.png", []byte(strings.Repeat("c", 1<<20))),
	}
	messages[0].SourceTurnSeq = 2
	retained, retention := applyProviderImageBudget(messages, nil, 1024, 2)
	if !hasImagePart(retained[0]) {
		t.Fatalf("current user image must not be trimmed: %#v", retention)
	}
	if retention.OverBudgetBytes <= 0 {
		t.Fatalf("expected over-budget signal: %#v", retention)
	}
	summary := retention.Summary()
	for _, want := range []string{"user_omitted=0", "over_budget_bytes="} {
		if !strings.Contains(summary, want) {
			t.Fatalf("summary missing %q: %q", want, summary)
		}
	}
}

func TestProviderImageRetentionUserPlaceholderKeepsOtherImagesAlive(t *testing.T) {
	const mib = 1 << 20
	userMessage := modeladapter.Message{
		SourceTurnSeq: 1,
		Role:          "user",
		Content:       "two screenshots",
		ContentParts: []modeladapter.ContentPart{
			{Type: "text", Text: "two screenshots"},
			{Type: "image", Image: &modeladapter.ImageContent{MIMEType: "image/png", Path: "/tmp/a.png", Data: []byte(strings.Repeat("a", mib))}},
			{Type: "image", Image: &modeladapter.ImageContent{MIMEType: "image/png", Path: "/tmp/b.png", Data: []byte(strings.Repeat("b", mib))}},
		},
	}
	messages := []modeladapter.Message{
		userMessage,
		userImageMessage("current question", "/tmp/current.png", []byte(strings.Repeat("c", 100))),
	}
	messages[len(messages)-1].SourceTurnSeq = 2
	retained, retention := applyProviderImageBudget(messages, nil, 2*mib+200*1024, 2)
	images := 0
	placeholders := 0
	for _, part := range retained[0].ContentParts {
		if part.Image != nil {
			images++
		}
		if strings.Contains(part.Text, "ask the user to re-attach") {
			placeholders++
		}
	}
	if images != 1 || placeholders != 1 {
		t.Fatalf("expected one retained and one placeholder part: images=%d placeholders=%d %#v", images, placeholders, retention)
	}
	if !strings.Contains(retained[0].Content, "two screenshots") {
		t.Fatalf("user query text lost: %q", retained[0].Content)
	}
}
