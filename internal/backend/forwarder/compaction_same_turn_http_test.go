package forwarder

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"

	"cursor/gen/agentv1"
	runtimecore "cursor/internal/backend/agent/core"
	legacyruntime "cursor/internal/runtime"
)

// 本文件是「同回合多次自动压缩」的正式入口集成回归：
//   - 只经 handleRunIntent 启动一个回合，RequestID/TurnSeq 唯一，不再发送新用户消息；
//   - 上游是真实 httptest OpenAI Responses HTTP 适配器，工具调用由脚本按请求序列下发；
//   - 工具结果经正式 exec 结果入口（dispatchInboundIntent/exec_result）回传客户端结果；
//   - 工具结果按真实体积增长，直到默认编译器判定超预算，同回合自动压缩至少四次；
//   - 每次压缩后继续真实工具调用，最后正常收尾；
//   - 参数化注入摘要故障：网关重试成功，以及重试耗尽后走本地安全回退。
//
// 关键断言全部基于实际 HTTP 请求体与追加式历史，不使用伪造 usage 触发压缩。
const (
	// 窗口设置得足够容纳默认系统/工具 prompt，又能在真实结果增长后多次触发压缩。
	sameTurnHTTPWindowTokens = 60_000
	// 至少完成四次压缩，避免把“固定次数”当成实现特征。
	sameTurnHTTPTargetSummaries = 4
	// 普通请求上限，仅用于防止脚本失控；触发上限即视为回归失败。
	sameTurnHTTPMaxMainCalls = 64
	// 每次 Read 结果体积（纯 ASCII），保证 token 估算可预测。
	sameTurnHTTPResultBytes = 48 * 1024
	// 唯一当前指令，用于验证每个主请求只出现一次、未重复注入。
	sameTurnHTTPInstruction = "同回合压缩正式入口回归：继续执行读取工具直到完成。"
	// 上一回合已完成的工具正文：压缩后不得回流，压缩前必须真实出现。
	sameTurnHTTPPriorToolOutput = "PRIOR-TURN-TOOL-OUTPUT-DO-NOT-REPLAY"
	// 远端明确故障时的本地回退摘要特征句，用来区分“真实上游正文”和“安全回退”。
	sameTurnHTTPFallbackSummaryHint = "Earlier context was compacted into this summary"
)

// sameTurnHTTPRecord 保存一次真实上游请求的原始请求体。
type sameTurnHTTPRecord struct {
	kind            string // main / summary
	body            string
	mainIndex       int // 主请求序号（1 起）
	summariesBefore int // 本请求发出前已完成的摘要回合数
	attemptOrdinal  int // 摘要请求尝试序号（1 起；同一回合的重试会递增）
}

// sameTurnHTTPSummaryOutcome 记录一个压缩回合的摘要结果形态。
// 同一压缩回合的上游重试会累加 attempts，但只提交一次摘要，所以结果按回合记录。
type sameTurnHTTPSummaryOutcome struct {
	ordinal  int    // 压缩回合序号（1 起）
	attempts int    // 该回合真实发出的摘要请求数（含重试）
	marker   string // 成功时的上游标记；回退时为空
	fallback bool   // 是否为本地安全回退摘要
}

// sameTurnHTTPSummaryFault 指定注入到摘要请求的故障形态。
type sameTurnHTTPSummaryFault int

const (
	// sameTurnHTTPSummaryFaultNone 不注入故障。
	sameTurnHTTPSummaryFaultNone sameTurnHTTPSummaryFault = iota
	// sameTurnHTTPSummaryFaultRetryThenSucceed 首次尝试返回 503，网关重试后成功。
	sameTurnHTTPSummaryFaultRetryThenSucceed
	// sameTurnHTTPSummaryFaultExhausted 该压缩回合所有尝试都返回 503，必须走本地安全回退。
	sameTurnHTTPSummaryFaultExhausted
)

// sameTurnHTTPUpstream 是脚本化上游：按请求顺序决定返回工具调用、摘要或最终答复。
type sameTurnHTTPUpstream struct {
	targetSummaries int
	maxMainCalls    int
	faultEpisode    int // 注入故障的压缩回合序号（1 起），0 表示不注入
	fault           sameTurnHTTPSummaryFault

	mu              sync.Mutex
	mainCalls       int
	summaryAttempts int
	summaryEpisodes int
	episodeMainCall int // 当前压缩回合开始时的主请求序号，用于识别同一回合的重试
	toolCalls       int
	reads           int
	records         []sameTurnHTTPRecord
	summaries       []sameTurnHTTPSummaryOutcome
	anomalies       []string
}

func (upstream *sameTurnHTTPUpstream) noteAnomaly(format string, args ...any) {
	upstream.anomalies = append(upstream.anomalies, fmt.Sprintf(format, args...))
}

// serve 依据请求体判别摘要/主请求，并输出对应的 Responses SSE。
func (upstream *sameTurnHTTPUpstream) serve(writer http.ResponseWriter, request *http.Request) {
	raw, err := io.ReadAll(request.Body)
	if err != nil {
		upstream.mu.Lock()
		upstream.noteAnomaly("读取上游请求体失败: %v", err)
		upstream.mu.Unlock()
		writer.WriteHeader(http.StatusBadRequest)
		return
	}
	var body map[string]any
	if err := json.Unmarshal(raw, &body); err != nil {
		upstream.mu.Lock()
		upstream.noteAnomaly("上游请求体不是 JSON: %v", err)
		upstream.mu.Unlock()
		writer.WriteHeader(http.StatusBadRequest)
		return
	}
	// 摘要请求不带工具描述，且被固定为最低推理等级与 4096 输出额度。
	isSummary := body["tools"] == nil

	upstream.mu.Lock()
	var record sameTurnHTTPRecord
	var toolCall bool
	var callID, path string
	var failSummary bool
	if isSummary {
		upstream.summaryAttempts++
		attempt := upstream.summaryAttempts
		// 同一压缩回合的重试不会推进主请求序号；用主请求序号识别重试，避免把重试当成新的压缩回合。
		if upstream.summaryEpisodes == 0 || upstream.mainCalls != upstream.episodeMainCall {
			upstream.summaryEpisodes++
			upstream.episodeMainCall = upstream.mainCalls
			upstream.summaries = append(upstream.summaries, sameTurnHTTPSummaryOutcome{
				ordinal: upstream.summaryEpisodes,
				// Exhausted 故障下该回合必然回退，压缩仍会提交一条本地摘要。
				fallback: upstream.fault == sameTurnHTTPSummaryFaultExhausted && upstream.summaryEpisodes == upstream.faultEpisode,
			})
		}
		episode := upstream.summaryEpisodes
		outcome := &upstream.summaries[episode-1]
		// RetryThenSucceed 只让首次尝试失败，后续重试返回真实正文。
		failSummary = upstream.fault != sameTurnHTTPSummaryFaultNone && episode == upstream.faultEpisode &&
			(upstream.fault == sameTurnHTTPSummaryFaultExhausted || outcome.attempts == 0)
		outcome.attempts++
		if !failSummary {
			outcome.marker = fmt.Sprintf("SUMMARY-MARK-%02d", attempt)
			outcome.fallback = false
		}
		record = sameTurnHTTPRecord{kind: "summary", body: string(raw), mainIndex: upstream.mainCalls, summariesBefore: episode - 1, attemptOrdinal: attempt}
		if upstreamC, _ := body["max_output_tokens"].(float64); upstreamC != float64(compactionSummaryOutputMaxTokens) {
			upstream.noteAnomaly("摘要请求 max_output_tokens=%v，期望 %d", body["max_output_tokens"], compactionSummaryOutputMaxTokens)
		}
		if reasoning, _ := body["reasoning"].(map[string]any); reasoning["effort"] != "low" {
			upstream.noteAnomaly("摘要请求 reasoning.effort=%v，期望 low", reasoning["effort"])
		}
	} else {
		upstream.mainCalls++
		record = sameTurnHTTPRecord{kind: "main", body: string(raw), mainIndex: upstream.mainCalls, summariesBefore: upstream.summaryEpisodes}
		// 完成目标次数压缩后正常收尾；此外仍受主请求上限约束，避免死循环。
		toolCall = upstream.summaryEpisodes < upstream.targetSummaries && upstream.mainCalls <= upstream.maxMainCalls
		if toolCall {
			upstream.toolCalls++
			callID = fmt.Sprintf("call-%03d", upstream.mainCalls)
			path = fmt.Sprintf("/tmp/same-turn-read-%03d.txt", upstream.mainCalls)
		}
	}
	upstream.records = append(upstream.records, record)
	upstream.mu.Unlock()

	writer.Header().Set("Content-Type", "text/event-stream")
	if failSummary {
		// 模拟上游 503：属于可安全回退的远端故障，压缩必须继续并保持同回合。
		writer.Header().Set("Content-Type", "application/json")
		writer.WriteHeader(http.StatusServiceUnavailable)
		_, _ = fmt.Fprint(writer, `{"error":{"code":"server_error","message":"simulated upstream unavailable"}}`)
		return
	}
	switch {
	case isSummary:
		marker := fmt.Sprintf("SUMMARY-MARK-%02d", record.attemptOrdinal)
		writeSameTurnHTTPEvent(writer, map[string]any{"type": "response.output_text.delta", "delta": marker + " 归档边界已推进，仅保留继续协作所需事实。"})
		writeSameTurnHTTPEvent(writer, map[string]any{"type": "response.completed", "response": map[string]any{
			"id": fmt.Sprintf("resp-summary-%02d", record.attemptOrdinal), "model": "gpt-6.1-sol", "status": "completed",
		}})
	case toolCall:
		argsJSON, _ := json.Marshal(map[string]string{"path": path})
		itemID := fmt.Sprintf("fc-%03d", record.mainIndex)
		writeSameTurnHTTPEvent(writer, map[string]any{"type": "response.output_item.added", "output_index": 0, "item": map[string]any{
			"type": "function_call", "id": itemID, "call_id": callID, "name": "Read", "arguments": "",
		}})
		writeSameTurnHTTPEvent(writer, map[string]any{"type": "response.function_call_arguments.delta", "item_id": itemID, "output_index": 0, "delta": string(argsJSON)})
		writeSameTurnHTTPEvent(writer, map[string]any{"type": "response.completed", "response": map[string]any{
			"id": fmt.Sprintf("resp-main-%03d", record.mainIndex), "model": "gpt-6.1-sol", "status": "completed",
			"output": []any{map[string]any{
				"type": "function_call", "id": itemID, "call_id": callID, "name": "Read", "arguments": string(argsJSON), "status": "completed",
			}},
		}})
	default:
		writeSameTurnHTTPEvent(writer, map[string]any{"type": "response.output_text.delta", "delta": "同回合压缩正式入口回归完成"})
		writeSameTurnHTTPEvent(writer, map[string]any{"type": "response.completed", "response": map[string]any{
			"id": "resp-main-final", "model": "gpt-6.1-sol", "status": "completed",
		}})
	}
}

func writeSameTurnHTTPEvent(writer http.ResponseWriter, payload map[string]any) {
	encoded, err := json.Marshal(payload)
	if err != nil {
		return
	}
	_, _ = fmt.Fprintf(writer, "data: %s\n\n", encoded)
}

// nextReadResult 为客户端执行桥结果分配唯一标记与真实体积的正文。
func (upstream *sameTurnHTTPUpstream) nextReadResult(path string) (marker string, content string) {
	upstream.mu.Lock()
	defer upstream.mu.Unlock()
	upstream.reads++
	marker = fmt.Sprintf("TOOLRESULT-MARK-%03d", upstream.reads)
	filler := sameTurnHTTPResultBytes - len(marker)
	if filler < 1 {
		filler = 1
	}
	// 标记放在尾部：压缩对保留结果的截断保留尾部，正断言因此仍然可查。
	return marker, strings.Repeat("a", filler) + marker
}

// sameTurnHTTPStats 是上游状态快照，避免调用方逐个解构返回值。
type sameTurnHTTPStats struct {
	records         []sameTurnHTTPRecord
	summaries       []sameTurnHTTPSummaryOutcome
	anomalies       []string
	mainCalls       int
	summaryEpisodes int
	summaryAttempts int
	toolCalls       int
	reads           int
}

func (upstream *sameTurnHTTPUpstream) snapshot() sameTurnHTTPStats {
	upstream.mu.Lock()
	defer upstream.mu.Unlock()
	return sameTurnHTTPStats{
		records:         append([]sameTurnHTTPRecord(nil), upstream.records...),
		summaries:       append([]sameTurnHTTPSummaryOutcome(nil), upstream.summaries...),
		anomalies:       append([]string(nil), upstream.anomalies...),
		mainCalls:       upstream.mainCalls,
		summaryEpisodes: upstream.summaryEpisodes,
		summaryAttempts: upstream.summaryAttempts,
		toolCalls:       upstream.toolCalls,
		reads:           upstream.reads,
	}
}

// trace 输出请求序列与摘要结果，便于失败时定位（仅诊断，不参与断言）。
func (upstream *sameTurnHTTPUpstream) trace() string {
	upstream.mu.Lock()
	defer upstream.mu.Unlock()
	var builder strings.Builder
	for _, record := range upstream.records {
		fmt.Fprintf(&builder, "\n  req kind=%s main=%d attempt=%d summariesBefore=%d bytes=%d",
			record.kind, record.mainIndex, record.attemptOrdinal, record.summariesBefore, len(record.body))
	}
	for _, outcome := range upstream.summaries {
		fmt.Fprintf(&builder, "\n  summary episode=%d attempts=%d fallback=%v marker=%q",
			outcome.ordinal, outcome.attempts, outcome.fallback, outcome.marker)
	}
	return builder.String()
}

var sameTurnHTTPMarkerPattern = regexp.MustCompile(`TOOLRESULT-MARK-\d{3}`)

// sameTurnHTTPMarkerIndexes 把结果标记映射回原始历史下标，用于校验边界之外的结果不再回流。
func sameTurnHTTPMarkerIndexes(entries []HistoryEntry) map[string]int {
	indexes := map[string]int{}
	for index, entry := range entries {
		if strings.TrimSpace(entry.Kind) != "tool_result" {
			continue
		}
		var payload toolResultEntryPayload
		if err := json.Unmarshal(entry.Payload, &payload); err != nil {
			continue
		}
		if marker := sameTurnHTTPMarkerPattern.FindString(payload.ResultText); marker != "" {
			indexes[marker] = index
		}
	}
	return indexes
}

// sameTurnHTTPPendingExecs 复制当前待处理执行桥请求，避免持有 stream 锁。
func sameTurnHTTPPendingExecs(stream *ActiveStream) []runtimecore.PendingExec {
	stream.mu.Lock()
	defer stream.mu.Unlock()
	pending := make([]runtimecore.PendingExec, 0, len(stream.PendingExecs))
	for _, item := range stream.PendingExecs {
		pending = append(pending, item)
	}
	return pending
}

// runSameTurnHTTPClient 以客户端身份循环回传执行结果，直到回合正常收口。
func runSameTurnHTTPClient(t *testing.T, service *Service, stream *ActiveStream, upstream *sameTurnHTTPUpstream) {
	t.Helper()
	deadline := time.Now().Add(180 * time.Second)
	handled := map[string]struct{}{}
	for {
		acknowledgeCheckpointBlobs(t, service, stream)
		for _, pending := range sameTurnHTTPPendingExecs(stream) {
			if _, done := handled[pending.ExecID]; done {
				continue
			}
			handled[pending.ExecID] = struct{}{}
			var message *agentv1.ExecClientMessage
			switch strings.TrimSpace(pending.ExecKind) {
			case "read":
				var args struct {
					Path string `json:"path"`
				}
				if err := json.Unmarshal(pending.ArgsJSON, &args); err != nil || strings.TrimSpace(args.Path) == "" {
					t.Fatalf("Read 执行桥参数无效: %s err=%v", pending.ArgsJSON, err)
				}
				_, content := upstream.nextReadResult(args.Path)
				message = &agentv1.ExecClientMessage{
					Id: pending.MessageID, ExecId: pending.ExecID,
					Message: &agentv1.ExecClientMessage_ReadResult{ReadResult: &agentv1.ReadResult{
						Result: &agentv1.ReadResult_Success{Success: &agentv1.ReadSuccess{
							Path:       args.Path,
							TotalLines: 1,
							FileSize:   int64(len(content)),
							Output:     &agentv1.ReadSuccess_Content{Content: content},
						}},
					}},
				}
			case "execute_hook_pre_compact":
				// 压缩前 hook 由客户端回传空 user_message：摘要流程继续，不额外注入用户输入。
				message = &agentv1.ExecClientMessage{
					Id: pending.MessageID, ExecId: pending.ExecID,
					Message: &agentv1.ExecClientMessage_ExecuteHookResult{ExecuteHookResult: &agentv1.ExecuteHookResult{
						Response: &agentv1.ExecuteHookResponse{Response: &agentv1.ExecuteHookResponse_PreCompact{
							PreCompact: &agentv1.PreCompactRequestResponse{},
						}},
					}},
				}
			default:
				t.Fatalf("出现未预期的执行桥类型: %q", pending.ExecKind)
			}
			if err := service.dispatchInboundIntent(InboundIntent{
				Kind:              "exec_result",
				RequestID:         stream.RequestID,
				ConversationID:    stream.ConversationID,
				ExecClientMessage: message,
			}); err != nil {
				t.Fatalf("回传 %q 执行结果失败: %v", pending.ExecKind, err)
			}
		}
		stream.mu.Lock()
		status := stream.Status
		pendingWrites := len(stream.PendingCheckpointBlobWrites)
		pendingExecs := len(stream.PendingExecs)
		stream.mu.Unlock()
		if isTerminalStreamStatus(status) && pendingWrites == 0 && pendingExecs == 0 {
			return
		}
		if time.Now().After(deadline) {
			stats := upstream.snapshot()
			t.Fatalf("同回合未在超时内收口: status=%s pending_execs=%d pending_writes=%d main=%d episodes=%d attempts=%d tool=%d reads=%d records=%d%s",
				status, pendingExecs, pendingWrites, stats.mainCalls, stats.summaryEpisodes, stats.summaryAttempts,
				stats.toolCalls, stats.reads, len(stats.records), upstream.trace())
		}
		time.Sleep(5 * time.Millisecond)
	}
}

// TestSameTurnCompactionThroughRunIntentHTTP 是正式入口 HTTP 集成回归主用例。
// 参数化覆盖：正常摘要、第二次摘要首次失败后被网关重试接住、第二次摘要持续失败后安全回退。
func TestSameTurnCompactionThroughRunIntentHTTP(t *testing.T) {
	for _, testCase := range []struct {
		name         string
		faultEpisode int
		fault        sameTurnHTTPSummaryFault
	}{
		{name: "正常摘要"},
		{name: "第二次摘要首次失败后重试成功", faultEpisode: 2, fault: sameTurnHTTPSummaryFaultRetryThenSucceed},
		{name: "第二次摘要持续失败安全回退", faultEpisode: 2, fault: sameTurnHTTPSummaryFaultExhausted},
		{name: "第三次摘要持续失败安全回退", faultEpisode: 3, fault: sameTurnHTTPSummaryFaultExhausted},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			runSameTurnHTTPCompactionCase(t, testCase.faultEpisode, testCase.fault)
		})
	}
}

func runSameTurnHTTPCompactionCase(t *testing.T, faultEpisode int, fault sameTurnHTTPSummaryFault) {
	t.Helper()
	// HOME 隔离：避免读取真实用户目录下的规则/配置影响默认编译器与存储路径。
	t.Setenv("HOME", t.TempDir())

	service, seed := testOverflowRecoveryStream(t)
	// 先写入一对带 ModelCallID 的上一回合工具记录：它必须可被归档，
	// 且压缩后不得再出现在真实请求中；旧格式身份兼容另有永久回归覆盖。
	appendSameTurnHTTPPriorToolHistory(t, service, seed)
	service.compiler = NewPromptCompiler(service.projector, NewToolCatalog(), NewReminderInjector(), nil)
	conversationID := seed.ConversationID
	// 启动前只调整渠道窗口与本地预算基线，不触碰运行中的流状态。
	resolver := compactionHTTPChannelResolver{channel: &legacyruntime.ResolvedChannel{
		ID: "same-turn-channel", Provider: "openai", Model: "gpt-6.1-sol",
		ContextWindowTokens: sameTurnHTTPWindowTokens, ReasoningEffort: "high",
	}}
	service.resolver = resolver
	if _, err := service.updateConversationMetaAndCheckpoint(seed, conversationID, func(item *ConversationFile) error {
		item.TokenDetailsMaxTokens = sameTurnHTTPWindowTokens
		item.TokenDetailsUsedTokens = 0
		clearConversationAutoCompactionState(item)
		return nil
	}); err != nil {
		t.Fatal(err)
	}

	upstream := &sameTurnHTTPUpstream{
		targetSummaries: sameTurnHTTPTargetSummaries,
		maxMainCalls:    sameTurnHTTPMaxMainCalls,
		faultEpisode:    faultEpisode,
		fault:           fault,
	}
	server := httptest.NewServer(http.HandlerFunc(upstream.serve))
	defer server.Close()
	// 同一个 resolver 既服务上下文窗口，也驱动真实 Responses HTTP 上游。
	channelResolver := compactionHTTPChannelResolver{channel: &legacyruntime.ResolvedChannel{
		ID: "same-turn-channel", Provider: "openai", Model: "gpt-6.1-sol", BaseURL: server.URL,
		APIKey: "test-only", OpenAIEndpoint: "/v1/responses", ReasoningEffort: "high",
		ContextWindowTokens: sameTurnHTTPWindowTokens,
	}}
	service.provider = NewProviderGateway(channelResolver)

	requestID := "same-turn-http-request"
	intent := InboundIntent{
		RequestID: requestID, ConversationID: conversationID,
		ModelID: seed.ModelID, ModelName: "gpt-6.1-sol", Mode: agentv1.AgentMode_AGENT_MODE_AGENT,
		UserMessage: &agentv1.UserMessage{Text: sameTurnHTTPInstruction},
	}
	// 正式回合入口只调用一次。
	if err := service.handleRunIntent(intent); err != nil {
		t.Fatalf("handleRunIntent() error = %v", err)
	}
	stream, ok := service.broker.Get(requestID)
	if !ok || stream == nil {
		t.Fatal("新回合未注册")
	}
	runSameTurnHTTPClient(t, service, stream, upstream)

	stats := upstream.snapshot()
	records, summaries := stats.records, stats.summaries
	mainCalls, summaryEpisodes := stats.mainCalls, stats.summaryEpisodes
	toolCalls, reads := stats.toolCalls, stats.reads
	for _, anomaly := range stats.anomalies {
		t.Errorf("上游请求合同异常: %s", anomaly)
	}
	if summaryEpisodes < sameTurnHTTPTargetSummaries {
		var dump strings.Builder
		for _, record := range records {
			fmt.Fprintf(&dump, "\n  %s#%d attempt=%d summariesBefore=%d bytes=%d tools=%v instructions=%d function_calls=%d",
				record.kind, record.mainIndex, record.attemptOrdinal, record.summariesBefore, len(record.body),
				strings.Contains(record.body, `"tools"`), strings.Count(record.body, sameTurnHTTPInstruction),
				strings.Count(record.body, `"function_call"`))
		}
		stream.mu.Lock()
		dumpStatus, dumpPhase := stream.Status, stream.Phase
		dumpKinds := projectedPromptEntryKinds(stream.CheckpointConversation.Entries)
		var failedPayloads []string
		for _, entry := range stream.CheckpointConversation.Entries {
			if entry.Kind == "compaction_failed" {
				failedPayloads = append(failedPayloads, string(entry.Payload))
			}
		}
		stream.mu.Unlock()
		t.Fatalf("自动压缩次数不足: episodes=%d，期望至少 %d（说明真实结果增长未推进预算边界）status=%s phase=%s failed=%v records:%s\n  history_kinds=%v",
			summaryEpisodes, sameTurnHTTPTargetSummaries, dumpStatus, dumpPhase, failedPayloads, dump.String(), dumpKinds)
	}
	if toolCalls < sameTurnHTTPTargetSummaries {
		t.Fatalf("每次压缩后都必须继续真实工具: tool_calls=%d episodes=%d", toolCalls, summaryEpisodes)
	}
	if reads != toolCalls {
		t.Fatalf("客户端回传的 Read 结果数=%d，上游派发工具数=%d", reads, toolCalls)
	}

	stream.mu.Lock()
	status := stream.Status
	turnSeq := stream.TurnSeq
	entries := cloneHistoryEntries(stream.CheckpointConversation.Entries)
	stream.mu.Unlock()
	if status != StreamStatusCompleted {
		t.Fatalf("同回合最终状态=%s，期望 %s", status, StreamStatusCompleted)
	}
	// 追加式历史内部一致性：布局引用、身份与摘要范围必须自洽。
	if err := validateCompactionLayouts(entries); err != nil {
		t.Fatalf("压缩布局校验失败: %v", err)
	}

	// 一次回合：新写入条目只能属于唯一 TurnSeq/RequestID。
	newEntries := 0
	toolCallIDs := map[string]int{}
	toolResultIDs := map[string]int{}
	userMessages := 0
	runRequests := 0
	summaryEntryIndexes := []int{}
	for index, entry := range entries {
		if isCompactionSummaryKind(entry.Kind) {
			summaryEntryIndexes = append(summaryEntryIndexes, index)
		}
		if entry.TurnSeq != turnSeq || strings.TrimSpace(entry.RequestID) != requestID {
			continue
		}
		newEntries++
		switch strings.TrimSpace(entry.Kind) {
		case "user_message":
			userMessages++
		case "tool_call":
			toolCallIDs[historyEntryToolCallID(entry)]++
		case "tool_result":
			toolResultIDs[historyEntryToolCallID(entry)]++
		case "metadata":
			var payload metadataPayload
			if err := json.Unmarshal(entry.Payload, &payload); err == nil && strings.TrimSpace(payload.Type) == "run_request" {
				runRequests++
			}
		}
	}
	if newEntries == 0 {
		t.Fatal("回合没有写入任何历史")
	}
	if userMessages != 1 {
		t.Fatalf("同回合用户消息数=%d，期望仅启动指令一次", userMessages)
	}
	if runRequests != 1 {
		t.Fatalf("run_request 元数据数=%d，期望正式入口只启动一次", runRequests)
	}
	if len(toolCallIDs) != toolCalls || len(toolResultIDs) != toolCalls {
		t.Fatalf("工具历史不完整: 调用=%d 结果=%d 上游派发=%d", len(toolCallIDs), len(toolResultIDs), toolCalls)
	}
	for id, count := range toolCallIDs {
		if id == "" || count != 1 {
			t.Fatalf("工具调用只应写一次: id=%q count=%d", id, count)
		}
	}
	for id, count := range toolResultIDs {
		if id == "" || count != 1 {
			t.Fatalf("工具结果只应写一次: id=%q count=%d", id, count)
		}
	}
	if len(summaryEntryIndexes) != summaryEpisodes {
		var committed strings.Builder
		for _, index := range summaryEntryIndexes {
			payload, err := decodeCompactionSummaryPayload(entries[index].Payload)
			if err != nil {
				fmt.Fprintf(&committed, "\n  committed@%d 解析失败: %v", index, err)
				continue
			}
			prefix := payload.Summary
			if len(prefix) > 80 {
				prefix = prefix[:80]
			}
			fmt.Fprintf(&committed, "\n  committed@%d trigger=%s fallback_hint=%v summary=%q",
				index, payload.Trigger, strings.Contains(payload.Summary, sameTurnHTTPFallbackSummaryHint), prefix)
		}
		t.Fatalf("已提交摘要=%d，上游压缩回合=%d（尝试=%d）%s%s",
			len(summaryEntryIndexes), summaryEpisodes, stats.summaryAttempts, upstream.trace(), committed.String())
	}
	if len(summaries) != summaryEpisodes {
		t.Fatalf("摘要回合结果记录=%d，上游压缩回合=%d%s", len(summaries), summaryEpisodes, upstream.trace())
	}
	expectedFallbacks := 0
	if fault == sameTurnHTTPSummaryFaultExhausted {
		expectedFallbacks = 1
	}
	fellBackSummaries := 0

	// 每个摘要都必须来自同回合自动压缩，并让归档边界严格前进。
	layouts := make([]*compactionLayout, 0, len(summaryEntryIndexes))
	previousEnd := -1
	toolResultsSinceSummary := 0
	for position, index := range summaryEntryIndexes {
		payload, err := decodeCompactionSummaryPayload(entries[index].Payload)
		if err != nil {
			t.Fatalf("解析摘要载荷失败: %v", err)
		}
		if payload.Trigger != "auto" {
			t.Fatalf("第%d次压缩 trigger=%q，期望 auto", position+1, payload.Trigger)
		}
		if !payload.PreserveCurrentTurnInputs {
			t.Fatalf("第%d次压缩未保留当前回合输入", position+1)
		}
		layout, _, err := validateCompactionLayout(entries, index, payload)
		if err != nil || layout == nil {
			t.Fatalf("第%d次压缩布局无效: layout=%v err=%v", position+1, layout, err)
		}
		if layout.PrefixEnd <= previousEnd {
			t.Fatalf("第%d次压缩边界未前进: previous_end=%d prefix=[%d,%d)", position+1, previousEnd, layout.PrefixStart, layout.PrefixEnd)
		}
		if layout.TailStart != layout.PrefixEnd {
			t.Fatalf("第%d次压缩 tail_start=%d，期望等于 prefix_end=%d", position+1, layout.TailStart, layout.PrefixEnd)
		}
		layouts = append(layouts, layout)
		previousEnd = layout.PrefixEnd
		// 摘要正文来源有两种合法形态：真实上游正文带唯一标记；远端明确故障时由本地历史生成回退摘要。
		if position >= len(summaries) {
			t.Fatalf("第%d次压缩缺少对应的摘要请求结果", position+1)
		}
		outcome := summaries[position]
		if outcome.ordinal != position+1 {
			t.Fatalf("摘要结果顺序错乱: position=%d ordinal=%d", position+1, outcome.ordinal)
		}
		if outcome.fallback {
			fellBackSummaries++
			// 回退摘要必须由本地历史生成：正文以本地模板开头，而不是上游返回的标记正文。
			if !strings.HasPrefix(strings.TrimSpace(payload.Summary), "Conversation summary") {
				t.Fatalf("第%d次回退摘要不是本地生成模板: %q", position+1, payload.Summary)
			}
			if !strings.Contains(payload.Summary, sameTurnHTTPFallbackSummaryHint) {
				t.Fatalf("第%d次回退摘要缺少本地生成特征: %q", position+1, payload.Summary)
			}
		} else {
			if outcome.marker == "" {
				t.Fatalf("第%d次摘要没有可用的上游标记，尝试数=%d", position+1, outcome.attempts)
			}
			if !strings.Contains(payload.Summary, outcome.marker) {
				t.Fatalf("第%d次摘要正文缺少真实上游标记 %q: %q", position+1, outcome.marker, payload.Summary)
			}
		}
		// 每次压缩之前都必须有真实工具结果增长，而不是靠状态或用量伪造触发。
		if position == 0 {
			for probe := 0; probe < index; probe++ {
				if strings.TrimSpace(entries[probe].Kind) == "tool_result" {
					toolResultsSinceSummary++
				}
			}
		} else {
			toolResultsSinceSummary = 0
			for probe := summaryEntryIndexes[position-1] + 1; probe < index; probe++ {
				if strings.TrimSpace(entries[probe].Kind) == "tool_result" {
					toolResultsSinceSummary++
				}
			}
		}
		if toolResultsSinceSummary == 0 {
			t.Fatalf("第%d次压缩前没有新的工具结果增长", position+1)
		}
	}
	if fellBackSummaries != expectedFallbacks {
		t.Fatalf("回退摘要数=%d，期望=%d（第%d个压缩回合注入故障 %v）%s",
			fellBackSummaries, expectedFallbacks, faultEpisode, fault, upstream.trace())
	}
	// 同一压缩回合的网关重试必须收拢到一个回合里：重试不产生额外摘要，也不复制归档边界。
	for _, outcome := range summaries {
		if outcome.attempts < 1 {
			t.Fatalf("第%d个压缩回合没有任何摘要请求", outcome.ordinal)
		}
	}
	if fault == sameTurnHTTPSummaryFaultRetryThenSucceed {
		if got := summaries[faultEpisode-1].attempts; got < 2 {
			t.Fatalf("第%d个压缩回合期望至少一次重试，实际尝试=%d", faultEpisode, got)
		}
	}
	if fault == sameTurnHTTPSummaryFaultExhausted {
		if got := summaries[faultEpisode-1].attempts; got < 2 {
			t.Fatalf("第%d个压缩回合期望重试耗尽后回退，实际尝试=%d", faultEpisode, got)
		}
	}
	if fault == sameTurnHTTPSummaryFaultNone && stats.summaryAttempts != summaryEpisodes {
		t.Fatalf("无故障时摘要尝试=%d，应等于压缩回合=%d%s", stats.summaryAttempts, summaryEpisodes, upstream.trace())
	}

	markerIndexes := sameTurnHTTPMarkerIndexes(entries)
	mainRecords := make([]sameTurnHTTPRecord, 0, mainCalls)
	for _, record := range records {
		if record.kind == "main" {
			mainRecords = append(mainRecords, record)
		}
	}
	if len(mainRecords) != mainCalls {
		t.Fatalf("捕获的主请求数=%d，计数=%d", len(mainRecords), mainCalls)
	}
	for _, record := range mainRecords {
		// 当前指令在本回合每个真实主请求里恰好出现一次，不能因 pinned 输入而重复。
		// 当前指令在本回合每个真实主请求里只能作为一条用户消息原样出现一次。
		// 派生出来的 <current_user_request> 上下文允许存在（最多一条），历史 pinned 输入不得复制用户消息。
		var decoded struct {
			Input []map[string]any `json:"input"`
		}
		if err := json.Unmarshal([]byte(record.body), &decoded); err != nil {
			t.Fatalf("主请求#%d 无法解析 input: %v", record.mainIndex, err)
		}
		userQueryBlocks, derivedInstructionBlocks, strayInstruction := 0, 0, 0
		for _, item := range decoded.Input {
			texts := sameTurnHTTPInputTexts(item)
			for _, text := range texts {
				switch {
				case strings.Contains(text, "<user_query>"):
					// 更早回合的 user_query 正文属于正常历史；只统计包含当前指令的那一条。
					if strings.Contains(text, sameTurnHTTPInstruction) {
						userQueryBlocks++
					}
				case strings.Contains(text, "<current_user_request>"):
					derivedInstructionBlocks++
					if !strings.Contains(text, sameTurnHTTPInstruction) {
						t.Fatalf("主请求#%d 的派生当前请求上下文不是当前指令", record.mainIndex)
					}
				case strings.Contains(text, sameTurnHTTPInstruction):
					strayInstruction++
				}
			}
		}
		if userQueryBlocks != 1 || strayInstruction != 0 || derivedInstructionBlocks > 1 {
			t.Fatalf("主请求#%d 当前指令分布异常: user_query=%d derived=%d stray=%d（期望 user_query=1 derived<=1 stray=0）",
				record.mainIndex, userQueryBlocks, derivedInstructionBlocks, strayInstruction)
		}
		// 工具配对闭合：每个 function_call_output 都必须有同请求内已出现的 function_call。
		calls := map[string]struct{}{}
		callCount, outputCount := 0, 0
		for _, item := range decoded.Input {
			itemType, _ := item["type"].(string)
			callID, _ := item["call_id"].(string)
			switch itemType {
			case "function_call":
				callCount++
				if _, duplicated := calls[callID]; duplicated {
					t.Fatalf("主请求#%d 重复派发工具调用 %q", record.mainIndex, callID)
				}
				calls[callID] = struct{}{}
				if arguments, _ := item["arguments"].(string); arguments == "{}" {
					t.Fatalf("主请求#%d 出现占位 function_call，说明工具配对曾经悬空", record.mainIndex)
				}
			case "function_call_output":
				outputCount++
				if _, ok := calls[callID]; !ok {
					t.Fatalf("主请求#%d 出现无前置调用的工具结果 %q", record.mainIndex, callID)
				}
			}
		}
		if outputCount > callCount {
			t.Fatalf("主请求#%d 工具结果=%d 多于调用=%d", record.mainIndex, outputCount, callCount)
		}
		if record.summariesBefore == 0 {
			continue
		}
		layout := layouts[record.summariesBefore-1]
		// 最新一次摘要正文必须进入后续每一个真实请求；更早已归档的摘要会被下一次摘要取代。
		// 回退摘要没有上游标记，改用本地生成特征校验同一位置。
		latest := summaries[record.summariesBefore-1]
		if latest.fallback {
			if !strings.Contains(record.body, sameTurnHTTPFallbackSummaryHint) {
				t.Fatalf("主请求#%d 丢失了第%d次回退摘要正文", record.mainIndex, record.summariesBefore)
			}
		} else if latest.marker == "" || !strings.Contains(record.body, latest.marker) {
			t.Fatalf("主请求#%d 丢失了第%d次摘要正文（marker=%q）", record.mainIndex, record.summariesBefore, latest.marker)
		}
		// 旧结果不回流：任何仍在请求中的工具结果都必须位于最近一次压缩边界之后。
		for _, marker := range sameTurnHTTPMarkerPattern.FindAllString(record.body, -1) {
			index, ok := markerIndexes[marker]
			if !ok {
				t.Fatalf("主请求#%d 出现历史中不存在的工具结果标记 %q", record.mainIndex, marker)
			}
			if index < layout.PrefixEnd {
				t.Fatalf("主请求#%d 回流了已归档结果 %q（index=%d < prefix_end=%d）", record.mainIndex, marker, index, layout.PrefixEnd)
			}
		}
		if strings.Contains(record.body, sameTurnHTTPPriorToolOutput) {
			t.Fatalf("主请求#%d 仍包含压缩前历史工具正文", record.mainIndex)
		}
	}
	// 首次压缩后真实请求中就应看不到旧结果，说明断言有实际覆盖。
	if len(layouts) > 0 {
		for _, record := range mainRecords {
			if record.summariesBefore > 0 {
				break
			}
			if !strings.Contains(record.body, sameTurnHTTPPriorToolOutput) {
				t.Fatalf("压缩前主请求#%d 未复现历史工具正文，回归夹具失效", record.mainIndex)
			}
		}
	}
	// 通过时也输出关键计数，作为验收证据（仅日志，不参与断言）。
	t.Logf("同回合正式入口压缩回归通过: 主请求=%d 压缩回合=%d 摘要尝试=%d 工具派发=%d 客户端结果=%d 回退摘要=%d 摘要条目=%d",
		mainCalls, summaryEpisodes, stats.summaryAttempts, toolCalls, reads, fellBackSummaries, len(summaryEntryIndexes))
}

// sameTurnHTTPInputTexts 取出 Responses input 项里所有文本内容，兼容字符串与分段两种形态。
func sameTurnHTTPInputTexts(item map[string]any) []string {
	var texts []string
	if text, ok := item["content"].(string); ok {
		texts = append(texts, text)
	}
	parts, _ := item["content"].([]any)
	for _, part := range parts {
		partMap, ok := part.(map[string]any)
		if !ok {
			continue
		}
		if text, ok := partMap["text"].(string); ok {
			texts = append(texts, text)
		}
	}
	return texts
}

// appendSameTurnHTTPPriorToolHistory 在启动前写入上一回合的完整工具记录。
// 必须带 ModelCallID：不完整的旧调用会阻止压缩边界前进，属于另一类输入。
func appendSameTurnHTTPPriorToolHistory(t *testing.T, service *Service, stream *ActiveStream) {
	t.Helper()
	entries := []HistoryEntry{
		tailReadToolCallEntry(t, 1, "request-1", "call-prior", "model-call-prior"),
		tailReadToolResultEntry(t, 1, "request-1", "call-prior", "model-call-prior", "/tmp/prior.txt", sameTurnHTTPPriorToolOutput),
	}
	persisted, _, err := service.store.AppendEntries(stream.ConversationID, resetEntrySequences(entries))
	if err != nil {
		t.Fatalf("AppendEntries() error = %v", err)
	}
	if err := service.replaceCheckpointConversation(stream, persisted); err != nil {
		t.Fatalf("replaceCheckpointConversation() error = %v", err)
	}
}
