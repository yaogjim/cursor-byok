package toolresult

import (
	"strings"

	runtimecore "cursor/internal/backend/agent/core"
)

const KiB = 1024

const (
	WebFetchMarkdownBytes = 32 * KiB
	WebSearchPayloadBytes = 16 * KiB
	WebSearchTitleBytes   = 512
	WebSearchSnippetBytes = 2 * KiB

	ReadContentBytes = 64 * KiB
	ReadLineBytes    = 0
	ReadBinaryBytes  = 32 * KiB

	ShellStreamBytes        = 16 * KiB
	ShellInterleavedBytes   = 32 * KiB
	ShellReplayPayloadBytes = 128 * KiB

	GrepContentBytes   = 32 * KiB
	GrepMatchBytes     = 2 * KiB
	GrepMatchesPerFile = 100
	GrepTotalMatches   = 300
	GrepListItems      = 300
	GlobFiles          = 200

	EditReplayBytes      = 32 * KiB
	PatchEditReplayBytes = 4 * KiB

	MCPTextTotalBytes           = 32 * KiB
	MCPTextItemBytes            = 32 * KiB
	MCPContentItems             = 20
	MCPStructuredBytes          = 32 * KiB
	MCPBinaryBytes              = 32 * KiB
	MCPResourcesBytes           = 32 * KiB
	MCPResourcesCount           = 200
	MCPResourceDescriptionBytes = KiB
	GenerateImageReplayBytes    = 16 * KiB
)

// Purpose 区分客户端展示与模型历史回放。调用方必须显式选择。
type Purpose int

const (
	PurposeDisplay Purpose = iota
	PurposeReplay
)

// PayloadBytes 返回整段结果文本预算。未登记的工具返回 ok=false。
func PayloadBytes(toolName string, purpose Purpose) (int, bool) {
	name := runtimecore.CanonicalToolName(strings.TrimSpace(toolName))
	switch purpose {
	case PurposeDisplay:
		switch name {
		case "WebFetch":
			return WebFetchMarkdownBytes, true
		case "WebSearch":
			return WebSearchPayloadBytes, true
		default:
			return 0, false
		}
	case PurposeReplay:
		switch name {
		case "GenerateImage":
			return GenerateImageReplayBytes, true
		case "Read":
			return ReadContentBytes, true
		case "Shell":
			return ShellReplayPayloadBytes, true
		case "Grep":
			return GrepContentBytes, true
		case "PatchEdit", "PatchEditLines", "PatchEditSpan":
			return PatchEditReplayBytes, true
		case "Edit", "Write":
			return EditReplayBytes, true
		case "WebFetch":
			return WebFetchMarkdownBytes, true
		case "WebSearch":
			return WebSearchPayloadBytes, true
		case "CallMcpTool", "FetchMcpResource", "ListMcpResources":
			return MCPResourcesBytes, true
		default:
			return 0, false
		}
	default:
		return 0, false
	}
}
