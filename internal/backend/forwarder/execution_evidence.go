package forwarder

import (
	"encoding/json"
	"strconv"
	"strings"
	"unicode"

	"google.golang.org/protobuf/encoding/protojson"

	"cursor/gen/agentv1"
)

const (
	executionEvidenceSchemaVersion = 1
	executionEvidencePayloadType   = "execution_evidence"
)

const (
	executionEvidenceCategoryMutation     = "mutation"
	executionEvidenceCategoryVerification = "verification"
	executionEvidenceCategoryNeutral      = "neutral"
	executionEvidenceCategoryUnknown      = "unknown"
)

const (
	executionEvidenceTerminalSuccess  = "success"
	executionEvidenceTerminalFailed   = "failed"
	executionEvidenceTerminalCanceled = "canceled"
	executionEvidenceTerminalUnknown  = "unknown"
)

const (
	executionEvidenceHintPending         = "pending"
	executionEvidenceHintStarted         = "started"
	executionEvidenceHintTransportClosed = "transport_closed"
	executionEvidenceHintCanceled        = "canceled"
	executionEvidenceHintFailed          = "failed"
	executionEvidenceHintSuccess         = "success"
)

const (
	executionEvidenceVerificationPresent = "present"
	executionEvidenceVerificationAbsent  = "absent"
	executionEvidenceVerificationStale   = "stale"
	executionEvidenceVerificationUnknown = "unknown"
)

const (
	executionEvidenceToolKindWrite         = "write"
	executionEvidenceToolKindPatchEdit     = "patch_edit"
	executionEvidenceToolKindDelete        = "delete"
	executionEvidenceToolKindShell         = "shell"
	executionEvidenceToolKindAwaitShell    = "await_shell"
	executionEvidenceToolKindMCP           = "mcp"
	executionEvidenceToolKindTask          = "task"
	executionEvidenceToolKindRead          = "read"
	executionEvidenceToolKindGrep          = "grep"
	executionEvidenceToolKindGlob          = "glob"
	executionEvidenceToolKindLs            = "ls"
	executionEvidenceToolKindReadLints     = "read_lints"
	executionEvidenceToolKindWebSearch     = "web_search"
	executionEvidenceToolKindWebFetch      = "web_fetch"
	executionEvidenceToolKindAskQuestion   = "ask_question"
	executionEvidenceToolKindSwitchMode    = "switch_mode"
	executionEvidenceToolKindTodoWrite     = "todo_write"
	executionEvidenceToolKindGenerateImage = "generate_image"
	executionEvidenceToolKindFetchMCP      = "fetch_mcp_resource"
	executionEvidenceToolKindWriteStdin    = "write_shell_stdin"
	executionEvidenceToolKindForceBG       = "force_background_shell"
	executionEvidenceToolKindUnknown       = "unknown"
)

const (
	executionEvidenceVerificationTest  = "test"
	executionEvidenceVerificationBuild = "build"
	executionEvidenceVerificationLint  = "lint"
	executionEvidenceVerificationVet   = "vet"
	executionEvidenceVerificationCheck = "check"
)

type executionEvidenceInput struct {
	TurnSeq          int64
	RequestID        string
	ModelCallID      string
	ToolCallID       string
	ToolName         string
	ArgsJSON         []byte
	ToolCall         *agentv1.ToolCall
	TerminalHint     string
	SubagentCategory SubagentTerminalCategory
	CommandOverride  string
	Sequence         int64
	ResultText       string
	Reasoning        string
}

type executionEvidenceRecord struct {
	SchemaVersion    int    `json:"schema_version"`
	EvidenceID       string `json:"evidence_id"`
	TurnSeq          int64  `json:"turn_seq,omitempty"`
	RequestID        string `json:"request_id,omitempty"`
	ModelCallID      string `json:"model_call_id,omitempty"`
	ToolCallID       string `json:"tool_call_id,omitempty"`
	Sequence         int64  `json:"sequence,omitempty"`
	ToolCategory     string `json:"tool_category"`
	ToolKind         string `json:"tool_kind,omitempty"`
	VerificationKind string `json:"verification_kind,omitempty"`
	TerminalStatus   string `json:"terminal_status"`
	Successful       bool   `json:"successful"`
}

type executionEvidenceIndex struct {
	HasLedger                     bool
	MutationToolCount             int
	VerificationCommandCount      int
	LastSuccessfulMutationSeq     int64
	LastSuccessfulVerificationSeq int64
	LastSuccessfulVerificationID  string
	seenIDs                       map[string]struct{}
}

type ExecutionEvidenceSummary struct {
	MutationToolCount        int
	VerificationCommandCount int
	VerificationStale        bool
	VerificationEvidence     string
	VerificationEvidenceID   string
}

func buildExecutionEvidence(input executionEvidenceInput) (executionEvidenceRecord, bool) {
	toolCallID := strings.TrimSpace(input.ToolCallID)
	toolName := strings.TrimSpace(input.ToolName)
	if toolCallID == "" || toolName == "" {
		return executionEvidenceRecord{}, false
	}
	terminalStatus, successful, record := resolveExecutionEvidenceTerminal(input)
	if !record {
		return executionEvidenceRecord{}, false
	}
	category, toolKind := classifyExecutionEvidenceTool(toolName)
	command := firstNonEmpty(strings.TrimSpace(input.CommandOverride), extractShellCommandForClassification(input.ArgsJSON, input.ToolCall))
	verificationKind := ""
	if toolKind == executionEvidenceToolKindShell || toolKind == executionEvidenceToolKindAwaitShell {
		verificationKind = classifyVerificationCommand(command)
		if verificationKind != "" {
			category = executionEvidenceCategoryVerification
		} else if category == "" {
			category = executionEvidenceCategoryNeutral
		}
	}
	if category == "" {
		category = executionEvidenceCategoryUnknown
	}
	if category != executionEvidenceCategoryVerification {
		verificationKind = ""
	}
	if category == executionEvidenceCategoryNeutral && terminalStatus == executionEvidenceTerminalUnknown {
		return executionEvidenceRecord{}, false
	}
	return executionEvidenceRecord{
		SchemaVersion:    executionEvidenceSchemaVersion,
		EvidenceID:       executionEvidenceID(input.TurnSeq, strings.TrimSpace(input.RequestID), toolCallID),
		TurnSeq:          input.TurnSeq,
		RequestID:        strings.TrimSpace(input.RequestID),
		ModelCallID:      strings.TrimSpace(input.ModelCallID),
		ToolCallID:       toolCallID,
		Sequence:         input.Sequence,
		ToolCategory:     category,
		ToolKind:         toolKind,
		VerificationKind: verificationKind,
		TerminalStatus:   terminalStatus,
		Successful:       successful,
	}, true
}

func resolveExecutionEvidenceTerminal(input executionEvidenceInput) (string, bool, bool) {
	hint := normalizeExecutionEvidenceHint(input.TerminalHint)
	switch hint {
	case executionEvidenceHintPending, executionEvidenceHintStarted, executionEvidenceHintTransportClosed:
		return hint, false, false
	}
	if status, successful, ok := typedTerminalFromToolCall(input.ToolCall); ok {
		if isNonTerminalEvidenceStatus(status) {
			return status, false, false
		}
		return status, successful, true
	}
	if input.SubagentCategory != "" {
		return terminalFromSubagentCategory(input.SubagentCategory)
	}
	switch hint {
	case executionEvidenceHintCanceled:
		return executionEvidenceTerminalCanceled, false, true
	case executionEvidenceHintFailed:
		return executionEvidenceTerminalFailed, false, true
	case executionEvidenceHintSuccess:
		return executionEvidenceTerminalSuccess, true, true
	default:
		return executionEvidenceTerminalUnknown, false, true
	}
}

func isNonTerminalEvidenceStatus(status string) bool {
	switch normalizeExecutionEvidenceHint(status) {
	case executionEvidenceHintPending, executionEvidenceHintStarted, executionEvidenceHintTransportClosed:
		return true
	default:
		return false
	}
}

func normalizeExecutionEvidenceHint(hint string) string {
	switch strings.ToLower(strings.TrimSpace(hint)) {
	case "pending":
		return executionEvidenceHintPending
	case "started":
		return executionEvidenceHintStarted
	case "transport_closed", "transport-closed":
		return executionEvidenceHintTransportClosed
	case "canceled", "cancelled":
		return executionEvidenceHintCanceled
	case "failed":
		return executionEvidenceHintFailed
	case "success":
		return executionEvidenceHintSuccess
	default:
		return strings.TrimSpace(hint)
	}
}

func typedTerminalFromToolCall(toolCall *agentv1.ToolCall) (string, bool, bool) {
	if toolCall == nil {
		return "", false, false
	}
	switch typed := toolCall.GetTool().(type) {
	case *agentv1.ToolCall_EditToolCall:
		return terminalFromEditResult(typed.EditToolCall.GetResult())
	case *agentv1.ToolCall_DeleteToolCall:
		return terminalFromDeleteResult(typed.DeleteToolCall.GetResult())
	case *agentv1.ToolCall_ShellToolCall:
		return terminalFromShellResult(typed.ShellToolCall.GetResult())
	case *agentv1.ToolCall_AwaitToolCall:
		return terminalFromAwaitResult(typed.AwaitToolCall.GetResult())
	case *agentv1.ToolCall_McpToolCall:
		return terminalFromMCPToolResult(typed.McpToolCall.GetResult())
	default:
		return "", false, false
	}
}

func terminalFromEditResult(result *agentv1.EditResult) (string, bool, bool) {
	if result == nil || result.GetResult() == nil {
		return "", false, false
	}
	switch result.GetResult().(type) {
	case *agentv1.EditResult_Success:
		return executionEvidenceTerminalSuccess, true, true
	default:
		return executionEvidenceTerminalFailed, false, true
	}
}

func terminalFromDeleteResult(result *agentv1.DeleteResult) (string, bool, bool) {
	if result == nil || result.GetResult() == nil {
		return "", false, false
	}
	switch result.GetResult().(type) {
	case *agentv1.DeleteResult_Success:
		return executionEvidenceTerminalSuccess, true, true
	default:
		return executionEvidenceTerminalFailed, false, true
	}
}

func terminalFromShellResult(result *agentv1.ShellResult) (string, bool, bool) {
	if result == nil || result.GetResult() == nil {
		return "", false, false
	}
	if result.GetIsBackground() {
		return executionEvidenceHintPending, false, true
	}
	switch typed := result.GetResult().(type) {
	case *agentv1.ShellResult_Success:
		if typed.Success != nil && typed.Success.GetExitCode() != 0 {
			return executionEvidenceTerminalFailed, false, true
		}
		return executionEvidenceTerminalSuccess, true, true
	case *agentv1.ShellResult_Failure:
		if typed.Failure != nil && typed.Failure.GetAborted() {
			return executionEvidenceTerminalCanceled, false, true
		}
		return executionEvidenceTerminalFailed, false, true
	default:
		return executionEvidenceTerminalFailed, false, true
	}
}

func terminalFromAwaitResult(result *agentv1.AwaitResult) (string, bool, bool) {
	if result == nil || result.GetResult() == nil {
		return "", false, false
	}
	switch typed := result.GetResult().(type) {
	case *agentv1.AwaitResult_Complete:
		if typed.Complete == nil || typed.Complete.ExitCode == nil {
			return executionEvidenceHintPending, false, true
		}
		if typed.Complete.GetExitCode() == 0 {
			return executionEvidenceTerminalSuccess, true, true
		}
		return executionEvidenceTerminalFailed, false, true
	case *agentv1.AwaitResult_Success:
		inner := typed.Success.GetAwaitResult()
		if complete, ok := inner.(*agentv1.AwaitSuccess_Complete); ok {
			return terminalFromAwaitResult(&agentv1.AwaitResult{Result: &agentv1.AwaitResult_Complete{Complete: complete.Complete}})
		}
		return executionEvidenceHintPending, false, true
	case *agentv1.AwaitResult_StillRunning:
		return executionEvidenceHintPending, false, true
	default:
		return executionEvidenceTerminalFailed, false, true
	}
}

func terminalFromMCPToolResult(result *agentv1.McpToolResult) (string, bool, bool) {
	if result == nil || result.GetResult() == nil {
		return "", false, false
	}
	switch typed := result.GetResult().(type) {
	case *agentv1.McpToolResult_Success:
		if typed.Success != nil && typed.Success.GetIsError() {
			return executionEvidenceTerminalFailed, false, true
		}
		return executionEvidenceTerminalSuccess, true, true
	default:
		return executionEvidenceTerminalFailed, false, true
	}
}

func terminalFromSubagentCategory(category SubagentTerminalCategory) (string, bool, bool) {
	switch category {
	case SubagentTerminalSucceeded:
		return executionEvidenceTerminalSuccess, true, true
	case SubagentTerminalCanceled:
		return executionEvidenceTerminalCanceled, false, true
	case "":
		return "", false, false
	default:
		return executionEvidenceTerminalFailed, false, true
	}
}

func classifyExecutionEvidenceTool(toolName string) (string, string) {
	switch strings.TrimSpace(toolName) {
	case "Write":
		return executionEvidenceCategoryMutation, executionEvidenceToolKindWrite
	case "PatchEdit":
		return executionEvidenceCategoryMutation, executionEvidenceToolKindPatchEdit
	case "Delete":
		return executionEvidenceCategoryMutation, executionEvidenceToolKindDelete
	case "Shell":
		return "", executionEvidenceToolKindShell
	case "AwaitShell":
		return "", executionEvidenceToolKindAwaitShell
	case "CallMcpTool":
		return executionEvidenceCategoryUnknown, executionEvidenceToolKindMCP
	case "Task":
		return executionEvidenceCategoryNeutral, executionEvidenceToolKindTask
	case "Read":
		return executionEvidenceCategoryNeutral, executionEvidenceToolKindRead
	case "Grep":
		return executionEvidenceCategoryNeutral, executionEvidenceToolKindGrep
	case "Glob":
		return executionEvidenceCategoryNeutral, executionEvidenceToolKindGlob
	case "Ls":
		return executionEvidenceCategoryNeutral, executionEvidenceToolKindLs
	case "ReadLints":
		return executionEvidenceCategoryNeutral, executionEvidenceToolKindReadLints
	case "WebSearch":
		return executionEvidenceCategoryNeutral, executionEvidenceToolKindWebSearch
	case "WebFetch":
		return executionEvidenceCategoryNeutral, executionEvidenceToolKindWebFetch
	case "AskQuestion":
		return executionEvidenceCategoryNeutral, executionEvidenceToolKindAskQuestion
	case "SwitchMode":
		return executionEvidenceCategoryNeutral, executionEvidenceToolKindSwitchMode
	case "TodoWrite":
		return executionEvidenceCategoryNeutral, executionEvidenceToolKindTodoWrite
	case "GenerateImage":
		return executionEvidenceCategoryNeutral, executionEvidenceToolKindGenerateImage
	case "FetchMcpResource":
		return executionEvidenceCategoryNeutral, executionEvidenceToolKindFetchMCP
	case "WriteShellStdin":
		return executionEvidenceCategoryNeutral, executionEvidenceToolKindWriteStdin
	case "ForceBackgroundShell":
		return executionEvidenceCategoryNeutral, executionEvidenceToolKindForceBG
	default:
		if isKnownToolName(toolName) {
			return executionEvidenceCategoryNeutral, executionEvidenceToolKindUnknown
		}
		return executionEvidenceCategoryUnknown, executionEvidenceToolKindUnknown
	}
}

func extractShellCommandForClassification(argsJSON []byte, toolCall *agentv1.ToolCall) string {
	if len(argsJSON) > 0 {
		var parsed struct {
			Command string `json:"command"`
		}
		if err := json.Unmarshal(argsJSON, &parsed); err == nil {
			if command := strings.TrimSpace(parsed.Command); command != "" {
				return command
			}
		}
	}
	if toolCall != nil && toolCall.GetShellToolCall() != nil && toolCall.GetShellToolCall().GetArgs() != nil {
		return strings.TrimSpace(toolCall.GetShellToolCall().GetArgs().GetCommand())
	}
	return ""
}

func classifyVerificationCommand(command string) string {
	command = strings.TrimSpace(command)
	if command == "" {
		return ""
	}
	for _, segment := range splitShellCommandSegments(command) {
		if kind := classifyVerificationTokens(tokenizeShellCommand(segment)); kind != "" {
			return kind
		}
	}
	return ""
}

func splitShellCommandSegments(command string) []string {
	segments := make([]string, 0, 4)
	current := strings.Builder{}
	flush := func() {
		if text := strings.TrimSpace(current.String()); text != "" {
			segments = append(segments, text)
		}
		current.Reset()
	}
	skipUntilSeparator := false
	for i := 0; i < len(command); i++ {
		if skipUntilSeparator {
			if command[i] == ';' {
				skipUntilSeparator = false
				continue
			}
			if i+1 < len(command) && ((command[i] == '&' && command[i+1] == '&') || (command[i] == '|' && command[i+1] == '|')) {
				skipUntilSeparator = false
				i++
				continue
			}
			continue
		}
		if command[i] == ';' {
			flush()
			continue
		}
		if i+1 < len(command) && ((command[i] == '&' && command[i+1] == '&') || (command[i] == '|' && command[i+1] == '|')) {
			flush()
			i++
			continue
		}
		if command[i] == '|' {
			flush()
			skipUntilSeparator = true
			continue
		}
		current.WriteByte(command[i])
	}
	flush()
	return segments
}

func tokenizeShellCommand(segment string) []string {
	fields := strings.Fields(segment)
	tokens := make([]string, 0, len(fields))
	for _, field := range fields {
		token := strings.Trim(field, `"'`)
		if token == "" {
			continue
		}
		tokens = append(tokens, token)
	}
	return tokens
}

func classifyVerificationTokens(tokens []string) string {
	tokens = skipShellEnvAssignments(tokens)
	if len(tokens) == 0 {
		return ""
	}
	switch strings.ToLower(tokens[0]) {
	case "cd", "true", ":", "command", "env", "echo", "printf", "cat", "tee":
		return ""
	case "go":
		return classifyGoVerification(tokens[1:])
	case "npm", "yarn", "pnpm", "bun", "npx":
		return classifyNodeVerification(tokens)
	case "make", "task":
		if len(tokens) > 1 {
			return verificationKindFromScript(tokens[1])
		}
		return ""
	case "cargo":
		return classifyCargoVerification(tokens[1:])
	case "pytest", "py.test":
		return executionEvidenceVerificationTest
	case "python", "python3":
		if len(tokens) >= 3 && tokens[1] == "-m" && (tokens[2] == "pytest" || tokens[2] == "unittest") {
			return executionEvidenceVerificationTest
		}
		return ""
	case "eslint", "golangci-lint", "staticcheck", "ruff":
		return executionEvidenceVerificationLint
	default:
		return verificationKindFromScript(tokens[0])
	}
}

func skipShellEnvAssignments(tokens []string) []string {
	i := 0
	for i < len(tokens) {
		if !strings.Contains(tokens[i], "=") {
			break
		}
		name, _, ok := strings.Cut(tokens[i], "=")
		if !ok || name == "" {
			break
		}
		valid := true
		for _, r := range name {
			if r != '_' && !unicode.IsLetter(r) && !unicode.IsDigit(r) {
				valid = false
				break
			}
		}
		if !valid {
			break
		}
		i++
	}
	return tokens[i:]
}

func classifyGoVerification(args []string) string {
	i := 0
	for i < len(args) {
		if args[i] == "-C" && i+1 < len(args) {
			i += 2
			continue
		}
		if strings.HasPrefix(args[i], "-") {
			i++
			continue
		}
		break
	}
	if i >= len(args) {
		return ""
	}
	switch args[i] {
	case "test":
		return executionEvidenceVerificationTest
	case "build":
		return executionEvidenceVerificationBuild
	case "vet":
		return executionEvidenceVerificationVet
	default:
		return ""
	}
}

func classifyNodeVerification(tokens []string) string {
	if len(tokens) < 2 {
		return ""
	}
	bin := strings.ToLower(tokens[0])
	if bin == "npx" {
		return classifyVerificationTokens(tokens[1:])
	}
	switch strings.ToLower(tokens[1]) {
	case "test":
		return executionEvidenceVerificationTest
	case "build":
		return executionEvidenceVerificationBuild
	case "lint":
		return executionEvidenceVerificationLint
	case "run":
		if len(tokens) >= 3 {
			return verificationKindFromScript(tokens[2])
		}
	}
	return ""
}

func classifyCargoVerification(args []string) string {
	if len(args) == 0 {
		return ""
	}
	switch args[0] {
	case "test":
		return executionEvidenceVerificationTest
	case "build":
		return executionEvidenceVerificationBuild
	case "check":
		return executionEvidenceVerificationCheck
	case "clippy":
		return executionEvidenceVerificationLint
	default:
		return ""
	}
}

func verificationKindFromScript(name string) string {
	name = strings.ToLower(strings.TrimSpace(name))
	if name == "" {
		return ""
	}
	base := name
	if index := strings.IndexByte(name, ':'); index >= 0 {
		if index == 0 || index == len(name)-1 {
			return ""
		}
		base = name[:index]
	}
	switch base {
	case "test":
		return executionEvidenceVerificationTest
	case "build":
		return executionEvidenceVerificationBuild
	case "lint":
		return executionEvidenceVerificationLint
	case "vet":
		return executionEvidenceVerificationVet
	case "check":
		return executionEvidenceVerificationCheck
	default:
		return ""
	}
}

func executionEvidenceID(turnSeq int64, requestID string, toolCallID string) string {
	return strings.Join([]string{
		"ev",
		strconv.FormatInt(turnSeq, 10),
		strings.TrimSpace(requestID),
		strings.TrimSpace(toolCallID),
	}, ":")
}

func executionEvidenceIdempotencyKey(turnSeq int64, requestID string, toolCallID string) string {
	return strings.Join([]string{
		"execution_evidence",
		strconv.Itoa(executionEvidenceSchemaVersion),
		strconv.FormatInt(turnSeq, 10),
		strings.TrimSpace(requestID),
		strings.TrimSpace(toolCallID),
	}, ":")
}

func newExecutionEvidenceEntry(record executionEvidenceRecord) HistoryEntry {
	values := executionEvidenceValueMap(record)
	entry := newMetadataEntry(record.TurnSeq, record.RequestID, executionEvidencePayloadType, values)
	entry.ModelCallID = strings.TrimSpace(record.ModelCallID)
	entry.ToolCallID = strings.TrimSpace(record.ToolCallID)
	entry.IdempotencyKey = executionEvidenceIdempotencyKey(record.TurnSeq, record.RequestID, record.ToolCallID)
	return entry
}

func executionEvidenceValueMap(record executionEvidenceRecord) map[string]any {
	encoded, err := json.Marshal(executionEvidenceRecord{
		SchemaVersion:    executionEvidenceSchemaVersion,
		EvidenceID:       strings.TrimSpace(record.EvidenceID),
		TurnSeq:          record.TurnSeq,
		RequestID:        strings.TrimSpace(record.RequestID),
		ModelCallID:      strings.TrimSpace(record.ModelCallID),
		ToolCallID:       strings.TrimSpace(record.ToolCallID),
		Sequence:         record.Sequence,
		ToolCategory:     strings.TrimSpace(record.ToolCategory),
		ToolKind:         strings.TrimSpace(record.ToolKind),
		VerificationKind: strings.TrimSpace(record.VerificationKind),
		TerminalStatus:   strings.TrimSpace(record.TerminalStatus),
		Successful:       record.Successful,
	})
	if err != nil {
		return map[string]any{"schema_version": executionEvidenceSchemaVersion}
	}
	var values map[string]any
	if err := json.Unmarshal(encoded, &values); err != nil {
		return map[string]any{"schema_version": executionEvidenceSchemaVersion}
	}
	return values
}

func decodeExecutionEvidence(entry HistoryEntry) (executionEvidenceRecord, bool) {
	if strings.TrimSpace(entry.Kind) != "metadata" {
		return executionEvidenceRecord{}, false
	}
	var payload metadataPayload
	if err := json.Unmarshal(entry.Payload, &payload); err != nil {
		return executionEvidenceRecord{}, false
	}
	if strings.TrimSpace(payload.Type) != executionEvidencePayloadType {
		return executionEvidenceRecord{}, false
	}
	encoded, err := json.Marshal(payload.Value)
	if err != nil {
		return executionEvidenceRecord{}, false
	}
	var record executionEvidenceRecord
	if err := json.Unmarshal(encoded, &record); err != nil {
		return executionEvidenceRecord{}, false
	}
	if record.SchemaVersion == 0 {
		record.SchemaVersion = executionEvidenceSchemaVersion
	}
	if record.ToolCallID == "" {
		record.ToolCallID = strings.TrimSpace(entry.ToolCallID)
	}
	if record.RequestID == "" {
		record.RequestID = strings.TrimSpace(entry.RequestID)
	}
	if record.ModelCallID == "" {
		record.ModelCallID = strings.TrimSpace(entry.ModelCallID)
	}
	if record.TurnSeq == 0 {
		record.TurnSeq = entry.TurnSeq
	}
	if entry.Seq > 0 {
		record.Sequence = entry.Seq
	}
	if record.EvidenceID == "" && record.ToolCallID != "" {
		record.EvidenceID = executionEvidenceID(record.TurnSeq, record.RequestID, record.ToolCallID)
	}
	return record, strings.TrimSpace(record.EvidenceID) != "" || strings.TrimSpace(record.ToolCallID) != ""
}

func applyExecutionEvidence(index *executionEvidenceIndex, record executionEvidenceRecord) {
	if index == nil {
		return
	}
	if index.seenIDs == nil {
		index.seenIDs = make(map[string]struct{})
	}
	id := strings.TrimSpace(record.EvidenceID)
	if id == "" {
		id = executionEvidenceID(record.TurnSeq, record.RequestID, record.ToolCallID)
	}
	if id != "" {
		if _, exists := index.seenIDs[id]; exists {
			return
		}
		index.seenIDs[id] = struct{}{}
	}
	index.HasLedger = true
	if !record.Successful {
		return
	}
	switch record.ToolCategory {
	case executionEvidenceCategoryMutation:
		index.MutationToolCount++
		if record.Sequence >= index.LastSuccessfulMutationSeq {
			index.LastSuccessfulMutationSeq = record.Sequence
		}
	case executionEvidenceCategoryVerification:
		index.VerificationCommandCount++
		if record.Sequence >= index.LastSuccessfulVerificationSeq {
			index.LastSuccessfulVerificationSeq = record.Sequence
			index.LastSuccessfulVerificationID = id
		}
	}
}

func rebuildExecutionEvidenceIndex(entries []HistoryEntry, turnSeq int64) executionEvidenceIndex {
	var index executionEvidenceIndex
	for _, entry := range entries {
		if turnSeq > 0 && entry.TurnSeq != turnSeq {
			continue
		}
		record, ok := decodeExecutionEvidence(entry)
		if !ok {
			continue
		}
		applyExecutionEvidence(&index, record)
	}
	return index
}

func summarizeExecutionEvidence(index executionEvidenceIndex) ExecutionEvidenceSummary {
	summary := ExecutionEvidenceSummary{
		MutationToolCount:        index.MutationToolCount,
		VerificationCommandCount: index.VerificationCommandCount,
		VerificationEvidenceID:   index.LastSuccessfulVerificationID,
	}
	if !index.HasLedger {
		summary.VerificationEvidence = executionEvidenceVerificationUnknown
		summary.VerificationEvidenceID = ""
		return summary
	}
	if index.LastSuccessfulVerificationID == "" {
		summary.VerificationEvidence = executionEvidenceVerificationAbsent
		return summary
	}
	if index.LastSuccessfulMutationSeq > 0 && index.LastSuccessfulVerificationSeq <= index.LastSuccessfulMutationSeq {
		summary.VerificationEvidence = executionEvidenceVerificationStale
		summary.VerificationStale = true
		return summary
	}
	summary.VerificationEvidence = executionEvidenceVerificationPresent
	return summary
}

func rebuildStreamExecutionEvidenceLocked(stream *ActiveStream) {
	if stream == nil {
		return
	}
	if stream.CheckpointConversation == nil {
		stream.ExecutionEvidence = executionEvidenceIndex{}
		return
	}
	stream.ExecutionEvidence = rebuildExecutionEvidenceIndex(stream.CheckpointConversation.Entries, stream.TurnSeq)
}

func rebuildStreamExecutionEvidence(stream *ActiveStream) {
	if stream == nil {
		return
	}
	stream.mu.Lock()
	defer stream.mu.Unlock()
	rebuildStreamExecutionEvidenceLocked(stream)
}

func stampExecutionEvidenceSequence(entry HistoryEntry) HistoryEntry {
	if entry.Seq <= 0 {
		return entry
	}
	var payload metadataPayload
	if err := json.Unmarshal(entry.Payload, &payload); err != nil {
		return entry
	}
	if strings.TrimSpace(payload.Type) != executionEvidencePayloadType {
		return entry
	}
	if payload.Value == nil {
		payload.Value = map[string]any{}
	}
	payload.Value["sequence"] = entry.Seq
	encoded, err := json.Marshal(payload)
	if err != nil {
		return entry
	}
	entry.Payload = encoded
	return entry
}

func backgroundShellCommandForEvidence(stream *ActiveStream, argsJSON []byte) string {
	if stream == nil || len(argsJSON) == 0 {
		return ""
	}
	var parsed struct {
		ShellID string `json:"shell_id"`
		TaskID  string `json:"task_id"`
	}
	if err := json.Unmarshal(argsJSON, &parsed); err != nil {
		return ""
	}
	id := firstNonEmpty(strings.TrimSpace(parsed.ShellID), strings.TrimSpace(parsed.TaskID))
	if id == "" {
		return ""
	}
	stream.mu.Lock()
	defer stream.mu.Unlock()
	state := stream.BackgroundShells[id]
	if state == nil {
		return ""
	}
	return strings.TrimSpace(state.Command)
}

func maybeExecutionEvidenceEntry(input executionEvidenceInput) (HistoryEntry, bool) {
	record, ok := buildExecutionEvidence(input)
	if !ok {
		return HistoryEntry{}, false
	}
	return newExecutionEvidenceEntry(record), true
}

func decodeToolCallJSON(encoded []byte) *agentv1.ToolCall {
	if len(encoded) == 0 {
		return nil
	}
	toolCall := &agentv1.ToolCall{}
	if err := protojson.Unmarshal(encoded, toolCall); err != nil {
		return nil
	}
	return toolCall
}

func newSubagentExecutionEvidenceEntry(turnSeq int64, requestID string, modelCallID string, toolCallID string, toolName string, argsJSON []byte, toolCallEncoded []byte, category SubagentTerminalCategory, sequence int64) (HistoryEntry, bool) {
	return maybeExecutionEvidenceEntry(executionEvidenceInput{
		TurnSeq:          turnSeq,
		RequestID:        requestID,
		ModelCallID:      modelCallID,
		ToolCallID:       toolCallID,
		ToolName:         toolName,
		ArgsJSON:         argsJSON,
		ToolCall:         decodeToolCallJSON(toolCallEncoded),
		SubagentCategory: category,
		Sequence:         sequence,
	})
}
