package execbridge

import (
	"fmt"
	"strings"
	"testing"
	"unicode/utf8"

	"cursor/gen/agentv1"
)

func TestFreezeExecReplayBudgets(t *testing.T) {
	t.Parallel()
	want := map[string]int{
		"readReplayContentLimit":     64 * 1024,
		"readReplayLineLimit":        0,
		"readReplayBinaryLimit":      32 * 1024,
		"shellReplayStreamLimit":     16 * 1024,
		"grepReplayContentLimit":     32 * 1024,
		"grepReplayMatchLimit":       2 * 1024,
		"grepReplayMatchesPerFile":   100,
		"grepReplayTotalMatches":     300,
		"grepReplayListLimit":        300,
		"mcpReplayTextTotalLimit":    32 * 1024,
		"mcpReplayTextItemLimit":     32 * 1024,
		"mcpReplayContentItemLimit":  20,
		"mcpReplayStructuredLimit":   32 * 1024,
		"mcpReplayBinaryLimit":       32 * 1024,
		"mcpResourcesReplayLimit":    32 * 1024,
		"mcpResourcesReplayCount":    200,
		"mcpResourceDescriptionSize": 1024,
		"maxGlobReplayFiles":         200,
	}
	got := map[string]int{
		"readReplayContentLimit":     readReplayContentLimit,
		"readReplayLineLimit":        readReplayLineLimit,
		"readReplayBinaryLimit":      readReplayBinaryLimit,
		"shellReplayStreamLimit":     shellReplayStreamLimit,
		"grepReplayContentLimit":     grepReplayContentLimit,
		"grepReplayMatchLimit":       grepReplayMatchLimit,
		"grepReplayMatchesPerFile":   grepReplayMatchesPerFile,
		"grepReplayTotalMatches":     grepReplayTotalMatches,
		"grepReplayListLimit":        grepReplayListLimit,
		"mcpReplayTextTotalLimit":    mcpReplayTextTotalLimit,
		"mcpReplayTextItemLimit":     mcpReplayTextItemLimit,
		"mcpReplayContentItemLimit":  mcpReplayContentItemLimit,
		"mcpReplayStructuredLimit":   mcpReplayStructuredLimit,
		"mcpReplayBinaryLimit":       mcpReplayBinaryLimit,
		"mcpResourcesReplayLimit":    mcpResourcesReplayLimit,
		"mcpResourcesReplayCount":    mcpResourcesReplayCount,
		"mcpResourceDescriptionSize": mcpResourceDescriptionSize,
		"maxGlobReplayFiles":         maxGlobReplayFiles,
	}
	for name, wantLimit := range want {
		if got[name] != wantLimit {
			t.Fatalf("%s = %d, want %d", name, got[name], wantLimit)
		}
	}
}

func TestFreezeExecTextAndMiddleSamples(t *testing.T) {
	t.Parallel()
	fitted := truncateReplayText("Read", strings.Repeat("x", 200), 80)
	wantFitted := "xxxxxxxxxxx\n\n[truncated: Read result exceeded 80 bytes; showing 11 of 200 bytes]"
	if fitted != wantFitted {
		t.Fatalf("tail sample = %q, want %q", fitted, wantFitted)
	}
	if again := truncateReplayText("Read", fitted, 80); again != fitted {
		t.Fatal("tail truncation is not idempotent")
	}

	if got := truncateReplayText("Read", "你好😀世界", 4); got != "你" {
		t.Fatalf("chinese tiny = %q", got)
	}
	if got := truncateReplayText("Read", strings.Repeat("😀", 20), 40); got != strings.Repeat("😀", 10) {
		t.Fatalf("emoji tiny = %q", got)
	}

	body := strings.Repeat("H", 40) + strings.Repeat("T", 40)
	middle := truncateReplayTextMiddle("Shell stdout", body, 160)
	if middle != body {
		t.Fatalf("under-limit middle changed: %q", middle)
	}
	tinyMiddle := truncateReplayTextMiddle("Shell stdout", strings.Repeat("a", 50), 20)
	if tinyMiddle != strings.Repeat("a", 20) {
		t.Fatalf("tiny middle = %q", tinyMiddle)
	}
	if !utf8.ValidString(tinyMiddle) {
		t.Fatal("tiny middle is not valid UTF-8")
	}

	stdout := strings.Repeat("o", shellReplayStreamLimit+8)
	stderr := strings.Repeat("e", shellReplayStreamLimit+8)
	gotOut, gotErr := truncateShellStreamsForReplay(stdout, stderr)
	if len(gotOut) > shellReplayStreamLimit || len(gotErr) > shellReplayStreamLimit {
		t.Fatalf("shell stream limits exceeded: stdout=%d stderr=%d", len(gotOut), len(gotErr))
	}
	if !strings.Contains(gotOut, "omitted middle") || !strings.Contains(gotErr, "omitted middle") {
		t.Fatal("shell stream truncation missing middle notice")
	}
	againOut, againErr := truncateShellStreamsForReplay(gotOut, gotErr)
	if againOut != gotOut || againErr != gotErr {
		t.Fatal("shell stream truncation is not idempotent")
	}
}

func TestFreezeShellExitCodeAndFields(t *testing.T) {
	t.Parallel()
	payload := buildShellSuccessPayload(shellResultArgs{
		Command:          "echo hi",
		WorkingDirectory: "/tmp/work",
	}, strings.Repeat("o", shellReplayStreamLimit+32), "err-line", &agentv1.ShellStreamExit{
		Code: 7,
		Cwd:  "/tmp/work",
	})
	if payload.GetExitCode() != 7 {
		t.Fatalf("exit code = %d, want 7", payload.GetExitCode())
	}
	if payload.GetCommand() != "echo hi" {
		t.Fatalf("command = %q", payload.GetCommand())
	}
	if payload.GetWorkingDirectory() != "/tmp/work" {
		t.Fatalf("cwd = %q", payload.GetWorkingDirectory())
	}
	if payload.GetStdout() == strings.Repeat("o", shellReplayStreamLimit+32) {
		t.Fatal("stdout was not truncated")
	}
	if payload.GetStderr() != "err-line" {
		t.Fatalf("stderr mutated: %q", payload.GetStderr())
	}
}

func TestFreezeGrepAndMcpStructuredSamples(t *testing.T) {
	t.Parallel()
	matches := make([]*agentv1.GrepFileMatch, 0, 2)
	matches = append(matches, &agentv1.GrepFileMatch{
		File: "a.go",
		Matches: []*agentv1.GrepContentMatch{{
			LineNumber: 3,
			Content:    strings.Repeat("m", grepReplayMatchLimit+16),
		}},
	})
	result := &agentv1.GrepResult{
		Result: &agentv1.GrepResult_Success{
			Success: &agentv1.GrepSuccess{
				WorkspaceResults: map[string]*agentv1.GrepUnionResult{
					"/workspace": {
						Result: &agentv1.GrepUnionResult_Content{
							Content: &agentv1.GrepContentResult{Matches: matches},
						},
					},
				},
			},
		},
	}
	got := truncateGrepResultForReplay(result)
	content := got.GetSuccess().GetWorkspaceResults()["/workspace"].GetContent()
	if !content.GetClientTruncated() {
		t.Fatal("grep content was not marked truncated")
	}
	last := content.GetMatches()[len(content.GetMatches())-1]
	notice := last.GetMatches()[len(last.GetMatches())-1].GetContent()
	if !strings.Contains(notice, "[truncated: Grep result exceeded") {
		t.Fatalf("grep notice = %q", notice)
	}
	again := truncateGrepResultForReplay(got)
	againContent := again.GetSuccess().GetWorkspaceResults()["/workspace"].GetContent()
	firstNoticeCount := countGrepNotices(content)
	secondNoticeCount := countGrepNotices(againContent)
	if secondNoticeCount != firstNoticeCount {
		t.Fatalf("grep notices grew on re-application: %d -> %d", firstNoticeCount, secondNoticeCount)
	}

	resources := make([]*agentv1.ListMcpResourcesExecResult_McpResource, 0, mcpResourcesReplayCount+3)
	for i := 0; i < mcpResourcesReplayCount+3; i++ {
		resources = append(resources, &agentv1.ListMcpResourcesExecResult_McpResource{Uri: "mcp://keep/" + strings.Repeat("x", 1)})
	}
	list := &agentv1.ListMcpResourcesExecResult{
		Result: &agentv1.ListMcpResourcesExecResult_Success{
			Success: &agentv1.ListMcpResourcesSuccess{Resources: resources},
		},
	}
	listed := truncateListMcpResourcesResultForReplay(list)
	items := listed.GetSuccess().GetResources()
	if items[len(items)-1].GetUri() != "truncated:list-mcp-resources" {
		t.Fatalf("resource notice uri = %q", items[len(items)-1].GetUri())
	}
	againList := truncateListMcpResourcesResultForReplay(listed)
	againItems := againList.GetSuccess().GetResources()
	if againItems[len(againItems)-1].GetUri() != "truncated:list-mcp-resources" {
		t.Fatal("resource notice uri changed on re-application")
	}
	noticeCount := 0
	for _, item := range againItems {
		if item.GetUri() == "truncated:list-mcp-resources" {
			noticeCount++
		}
	}
	if noticeCount != 1 {
		t.Fatalf("resource notices = %d, want 1 after re-application", noticeCount)
	}

	mcp := &agentv1.McpToolResult{
		Result: &agentv1.McpToolResult_Success{
			Success: &agentv1.McpSuccess{
				Content: make([]*agentv1.McpToolResultContentItem, 0, mcpReplayContentItemLimit+3),
			},
		},
	}
	for i := 0; i < mcpReplayContentItemLimit+3; i++ {
		mcp.GetSuccess().Content = append(mcp.GetSuccess().Content, &agentv1.McpToolResultContentItem{
			Content: &agentv1.McpToolResultContentItem_Text{Text: &agentv1.McpTextContent{Text: fmt.Sprintf("item-%d", i)}},
		})
	}
	gotMCP := truncateMcpToolResultForReplay(mcp)
	againMCP := truncateMcpToolResultForReplay(gotMCP)
	if len(againMCP.GetSuccess().GetContent()) != len(gotMCP.GetSuccess().GetContent()) {
		t.Fatalf("MCP content items grew on re-application: %d -> %d", len(gotMCP.GetSuccess().GetContent()), len(againMCP.GetSuccess().GetContent()))
	}
}

func countGrepNotices(content *agentv1.GrepContentResult) int {
	count := 0
	for _, file := range content.GetMatches() {
		for _, match := range file.GetMatches() {
			if strings.Contains(match.GetContent(), "[truncated: Grep ") {
				count++
			}
		}
	}
	return count
}
