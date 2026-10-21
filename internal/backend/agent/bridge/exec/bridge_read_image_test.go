package execbridge

import (
	"bytes"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"image"
	"image/color"
	"image/png"
	"strings"
	"testing"
	"unicode/utf8"

	"google.golang.org/protobuf/encoding/protojson"

	"cursor/gen/agentv1"
	runtimecore "cursor/internal/backend/agent/core"
)

func TestApplyExecClientMessageReturnsContentAddressedReadImage(t *testing.T) {
	imageData := validReadTestPNG(t)
	wantBlobID := sha256.Sum256(imageData)
	result, err := NewBridge().ApplyExecClientMessage(&agentv1.ExecClientMessage{
		Message: &agentv1.ExecClientMessage_ReadResult{
			ReadResult: &agentv1.ReadResult{
				Result: &agentv1.ReadResult_Success{
					Success: &agentv1.ReadSuccess{
						Path:         "diagram.png",
						FileSize:     int64(len(imageData)),
						OutputBlobId: append([]byte(nil), wantBlobID[:]...),
						Output:       &agentv1.ReadSuccess_Data{Data: imageData},
					},
				},
			},
		},
	}, runtimecore.PendingExec{
		ExecKind:   "read",
		ToolCallID: "call-1",
		ArgsJSON:   []byte(`{"path":"diagram.png"}`),
	})
	if err != nil {
		t.Fatalf("ApplyExecClientMessage() error = %v", err)
	}
	if len(result.ContentBlobs) != 1 {
		t.Fatalf("content blob count = %d, want 1", len(result.ContentBlobs))
	}
	if !bytes.Equal(result.ContentBlobs[0].ID, wantBlobID[:]) || !bytes.Equal(result.ContentBlobs[0].Data, imageData) {
		t.Fatalf("content blob = %#v", result.ContentBlobs[0])
	}
	readSuccess := result.ToolCall.GetReadToolCall().GetResult().GetSuccess()
	if readSuccess == nil {
		t.Fatal("read tool result is not successful")
	}
	if !bytes.Equal(readSuccess.GetDataBlobId(), wantBlobID[:]) {
		t.Fatalf("data_blob_id = %x, want %x", readSuccess.GetDataBlobId(), wantBlobID)
	}
	if len(readSuccess.GetData()) != 0 {
		t.Fatalf("read tool result retained %d image bytes", len(readSuccess.GetData()))
	}
}

func TestApplyExecClientMessageUsesComputedImageBlobID(t *testing.T) {
	imageData := validReadTestPNG(t)
	wantBlobID := sha256.Sum256(imageData)
	result, err := NewBridge().ApplyExecClientMessage(&agentv1.ExecClientMessage{
		Message: &agentv1.ExecClientMessage_ReadResult{
			ReadResult: &agentv1.ReadResult{
				Result: &agentv1.ReadResult_Success{
					Success: &agentv1.ReadSuccess{
						Path:         "diagram.png",
						OutputBlobId: bytes.Repeat([]byte{0xff}, sha256.Size),
						Output:       &agentv1.ReadSuccess_Data{Data: imageData},
					},
				},
			},
		},
	}, runtimecore.PendingExec{ExecKind: "read", ToolCallID: "call-1"})
	if err != nil {
		t.Fatalf("ApplyExecClientMessage() error = %v", err)
	}
	if !bytes.Equal(result.ContentBlobs[0].ID, wantBlobID[:]) {
		t.Fatalf("content blob id = %x, want computed %x", result.ContentBlobs[0].ID, wantBlobID)
	}
}

func TestConvertReadResultKeepsTextAndLimitsUnsupportedBinary(t *testing.T) {
	textResult := convertReadResultToReadToolResult(&agentv1.ReadResult{
		Result: &agentv1.ReadResult_Success{
			Success: &agentv1.ReadSuccess{
				Path:   "notes.txt",
				Output: &agentv1.ReadSuccess_Content{Content: "hello"},
			},
		},
	})
	if got := textResult.GetSuccess().GetContent(); got != "hello" {
		t.Fatalf("text read content = %q, want hello", got)
	}

	largeBinary := bytes.Repeat([]byte{0xff}, readReplayBinaryLimit+1)
	binaryResult := convertReadResultToReadToolResult(&agentv1.ReadResult{
		Result: &agentv1.ReadResult_Success{
			Success: &agentv1.ReadSuccess{
				Path:   "archive.bin",
				Output: &agentv1.ReadSuccess_Data{Data: largeBinary},
			},
		},
	})
	binarySuccess := binaryResult.GetSuccess()
	if binarySuccess == nil || !binarySuccess.GetExceededLimit() {
		t.Fatal("large non-image binary was not limited")
	}
	if binarySuccess.GetData() != nil || binarySuccess.GetDataBlobId() != nil {
		t.Fatal("large non-image binary was retained")
	}
	if !strings.Contains(binarySuccess.GetContent(), "Read binary data") {
		t.Fatalf("large binary fallback = %q", binarySuccess.GetContent())
	}
}

func TestTruncateReplayTextStaysWithinByteLimitAndValidUTF8(t *testing.T) {
	content := strings.Repeat("😀x", 100)
	for limit := 1; limit <= 250; limit++ {
		got := truncateReplayText("MCP text", content, limit)
		if len(got) > limit {
			t.Fatalf("limit %d produced %d bytes", limit, len(got))
		}
		if !utf8.ValidString(got) {
			t.Fatalf("limit %d produced invalid UTF-8: %q", limit, got)
		}
	}
}

func TestTruncateMcpImageReplayPreservesMimeAndValidBase64Projection(t *testing.T) {
	originalData := bytes.Repeat([]byte{0xff, 0x00, 0x7f}, mcpReplayBinaryLimit/3+10)
	result := &agentv1.McpToolResult{
		Result: &agentv1.McpToolResult_Success{
			Success: &agentv1.McpSuccess{Content: []*agentv1.McpToolResultContentItem{{
				Content: &agentv1.McpToolResultContentItem_Image{Image: &agentv1.McpImageContent{
					Data:     append([]byte(nil), originalData...),
					MimeType: "image/png",
				}},
			}}},
		},
	}

	got := truncateMcpToolResultForReplay(result)
	image := got.GetSuccess().GetContent()[0].GetImage()
	if image == nil || image.GetMimeType() != "image/png" {
		t.Fatalf("truncated image lost MIME type: %#v", image)
	}
	if len(image.GetData()) != mcpReplayBinaryLimit {
		t.Fatalf("truncated image bytes = %d, want %d", len(image.GetData()), mcpReplayBinaryLimit)
	}
	if len(result.GetSuccess().GetContent()[0].GetImage().GetData()) != len(originalData) {
		t.Fatal("truncation mutated the original MCP result")
	}

	encoded, err := protojson.Marshal(got)
	if err != nil || !json.Valid(encoded) {
		t.Fatalf("projected MCP result is not valid JSON: %s err=%v", encoded, err)
	}
	var payload map[string]any
	if err := json.Unmarshal(encoded, &payload); err != nil {
		t.Fatalf("decode projected MCP result: %v", err)
	}
	success := payload["success"].(map[string]any)
	content := success["content"].([]any)
	imagePayload := content[0].(map[string]any)["image"].(map[string]any)
	if _, err := base64.StdEncoding.DecodeString(imagePayload["data"].(string)); err != nil {
		t.Fatalf("projected MCP image data is not valid base64: %v", err)
	}
}

func TestListMcpResourcesReplayNoticeUsesResourceCount(t *testing.T) {
	resources := make([]*agentv1.ListMcpResourcesExecResult_McpResource, 0, mcpResourcesReplayCount+50)
	for index := 0; index < mcpResourcesReplayCount+50; index++ {
		resources = append(resources, &agentv1.ListMcpResourcesExecResult_McpResource{Uri: fmt.Sprintf("mcp://resource/%d", index)})
	}
	result := &agentv1.ListMcpResourcesExecResult{
		Result: &agentv1.ListMcpResourcesExecResult_Success{
			Success: &agentv1.ListMcpResourcesSuccess{Resources: resources},
		},
	}

	got := truncateListMcpResourcesResultForReplay(result)
	items := got.GetSuccess().GetResources()
	if len(items) != mcpResourcesReplayCount+1 {
		t.Fatalf("resource count = %d, want %d plus notice", len(items), mcpResourcesReplayCount)
	}
	notice := items[len(items)-1]
	want := "[truncated: ListMcpResources result exceeded 200 resources; showing 200 of 250 resources]"
	if notice.GetUri() != "truncated:list-mcp-resources" || notice.GetDescription() != want {
		t.Fatalf("resource truncation notice = uri:%q description:%q, want %q", notice.GetUri(), notice.GetDescription(), want)
	}
}

func validReadTestPNG(t *testing.T) []byte {
	t.Helper()
	value := image.NewRGBA(image.Rect(0, 0, 2, 2))
	value.Set(0, 0, color.RGBA{R: 0x44, G: 0x88, B: 0xcc, A: 0xff})
	var encoded bytes.Buffer
	if err := png.Encode(&encoded, value); err != nil {
		t.Fatalf("encode test png: %v", err)
	}
	return encoded.Bytes()
}
