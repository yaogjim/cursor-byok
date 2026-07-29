package forwarder

import (
	"context"
	"encoding/json"
	"fmt"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"

	"cursor/gen/agentv1"
	"cursor/internal/observability"
)

type debugLogConfig interface {
	ObservabilityLogMode(context.Context) string
}

type captureRecorder interface {
	Record(context.Context, observability.Capture) bool
}

type debugRecorder struct {
	historyRoot  string
	broker       *StreamBroker
	config       debugLogConfig
	capture      captureRecorder
	sink         *debugSink
	mu           sync.Mutex
	correlations map[string]observability.Correlation
}

func newDebugRecorder(historyRoot string, broker *StreamBroker, config debugLogConfig, captures ...captureRecorder) *debugRecorder {
	var capture captureRecorder
	if len(captures) > 0 {
		capture = captures[0]
	}
	var sink *debugSink
	if config != nil || capture != nil {
		sink = newDebugSink()
	}
	return &debugRecorder{
		historyRoot:  strings.TrimSpace(historyRoot),
		broker:       broker,
		config:       config,
		capture:      capture,
		sink:         sink,
		correlations: make(map[string]observability.Correlation),
	}
}

func (recorder *debugRecorder) mode(ctx context.Context) string {
	if recorder == nil || recorder.config == nil {
		return "off"
	}
	if ctx == nil {
		ctx = context.Background()
	}
	switch strings.ToLower(strings.TrimSpace(recorder.config.ObservabilityLogMode(ctx))) {
	case "basic":
		return "basic"
	case "full":
		return "full"
	default:
		return "off"
	}
}

func (recorder *debugRecorder) enabled(ctx context.Context) bool {
	return recorder.mode(ctx) != "off"
}

func (recorder *debugRecorder) full(ctx context.Context) bool {
	return recorder.mode(ctx) == "full"
}

func (recorder *debugRecorder) LogBidiRaw(ctx context.Context, requestID string, conversationID string, appendSeqno int64, dataHex string, status string, extra map[string]any) {
	mode := recorder.mode(ctx)
	if mode == "off" {
		return
	}
	event := recorder.baseEvent("bidi_raw", requestID, conversationID)
	event["direction"] = "client_to_backend"
	event["procedure"] = "/aiserver.v1.BidiService/BidiAppend"
	event["append_seqno"] = appendSeqno
	event["status"] = strings.TrimSpace(status)
	event["data_len"] = len(dataHex)
	if mode == "full" {
		event["data_hex"] = dataHex
	} else {
		event["payload_omitted"] = "basic_mode"
	}
	for key, value := range extra {
		event[key] = value
	}
	recorder.appendJSONL(ctx, requestID, conversationID, "bidi.raw.jsonl", event)
}

func (recorder *debugRecorder) LogBidiDecoded(ctx context.Context, requestID string, conversationID string, appendSeqno int64, clientKind string, message *agentv1.AgentClientMessage, intent InboundIntent, extra map[string]any) {
	mode := recorder.mode(ctx)
	if mode == "off" {
		return
	}
	event := recorder.baseEvent("bidi_decoded", requestID, conversationID)
	event["schema_version"] = 2
	event["append_seqno"] = appendSeqno
	event["client_kind"] = strings.TrimSpace(clientKind)
	event["message_case"] = agentClientMessageCase(message)
	if mode == "full" {
		event["message"] = deferredProtoJSONDebugPayload(message)
		event["intent"] = deferredInboundIntentDebugPayload(intent)
	} else {
		event["intent_kind"] = strings.TrimSpace(intent.Kind)
		event["payload_omitted"] = "basic_mode"
	}
	if requestedModel := requestedModelDebugPayload(message); requestedModel != nil {
		event["requested_model"] = requestedModel
	}
	if actionCase := conversationActionCase(message); actionCase != "" {
		event["conversation_action"] = actionCase
	}
	for key, value := range extra {
		event[key] = value
	}
	recorder.appendJSONL(ctx, requestID, firstNonEmpty(conversationID, intent.ConversationID), "bidi.decoded.jsonl", event)
}

func (recorder *debugRecorder) LogRuntime(ctx context.Context, requestID string, conversationID string, eventName string, fields map[string]any) {
	if !recorder.enabled(ctx) {
		return
	}
	event := recorder.baseEvent("runtime", requestID, conversationID)
	event["event"] = strings.TrimSpace(eventName)
	for key, value := range fields {
		event[key] = value
	}
	recorder.appendJSONL(ctx, requestID, conversationID, "runtime.jsonl", event)
}

func (recorder *debugRecorder) LogRunSSE(ctx context.Context, requestID string, conversationID string, eventName string, fields map[string]any) {
	if !recorder.enabled(ctx) {
		return
	}
	event := recorder.baseEvent("runsse", requestID, conversationID)
	event["event"] = strings.TrimSpace(eventName)
	for key, value := range fields {
		event[key] = value
	}
	recorder.appendJSONL(ctx, requestID, conversationID, "runsse.jsonl", event)
}

func (recorder *debugRecorder) LogProvider(ctx context.Context, requestID string, conversationID string, eventName string, fields map[string]any) {
	if !recorder.enabled(ctx) {
		return
	}
	event := recorder.baseEvent("provider", requestID, conversationID)
	event["event"] = strings.TrimSpace(eventName)
	for key, value := range fields {
		event[key] = value
	}
	recorder.appendJSONL(ctx, requestID, conversationID, "provider.jsonl", event)
}

func (recorder *debugRecorder) LogProviderArtifact(ctx context.Context, requestID string, conversationID string, modelCallID string, eventName string, payload map[string]any) {
	mode := recorder.mode(ctx)
	if mode == "off" {
		return
	}
	fields := map[string]any{
		"model_call_id": strings.TrimSpace(modelCallID),
	}
	if mode == "full" {
		fields["payload"] = payload
	} else {
		fields["payload_summary"] = summarizeDebugArtifactPayload(payload)
		fields["payload_omitted"] = "basic_mode"
	}
	recorder.LogProvider(ctx, requestID, conversationID, eventName, fields)
}

func (recorder *debugRecorder) ServerMessagePayload(ctx context.Context, message *agentv1.AgentServerMessage) any {
	if !recorder.full(ctx) || message == nil {
		return nil
	}
	return deferredProtoJSONDebugPayload(message)
}

func (recorder *debugRecorder) Close() {
	if recorder == nil {
		return
	}
	if recorder.sink != nil {
		recorder.sink.Close()
	}
}

func (recorder *debugRecorder) baseEvent(layer string, requestID string, conversationID string) map[string]any {
	resolvedConversationID := firstNonEmpty(strings.TrimSpace(conversationID), recorder.conversationIDForRequest(requestID))
	return map[string]any{
		"schema_version":  1,
		"at":              time.Now().UTC().Format(time.RFC3339Nano),
		"layer":           strings.TrimSpace(layer),
		"request_id":      strings.TrimSpace(requestID),
		"conversation_id": resolvedConversationID,
	}
}

func (recorder *debugRecorder) appendJSONL(ctx context.Context, requestID string, conversationID string, filename string, event map[string]any) {
	if len(event) == 0 || recorder == nil {
		return
	}
	if recorder.capture != nil {
		recorder.recordCapture(ctx, requestID, conversationID, filename, event)
	}
	if recorder.sink == nil {
		return
	}
	dir := recorder.debugDir(requestID, conversationID)
	if strings.TrimSpace(dir) == "" {
		return
	}
	recorder.sink.Append(dir, filename, event)
}

func (recorder *debugRecorder) recordCapture(ctx context.Context, requestID string, conversationID string, filename string, rawEvent map[string]any) {
	requestID = strings.TrimSpace(requestID)
	correlation := observability.CorrelationFromContext(ctx)
	stored := recorder.correlationForRequest(requestID)
	if stored.TraceID != "" {
		useStoredTrace := stored.HTTPRequestID != "" || correlation.HTTPRequestID == ""
		if useStoredTrace {
			correlation.TraceID = stored.TraceID
			if correlation.SpanID == "" {
				correlation.SpanID = stored.SpanID
				correlation.ParentSpanID = stored.ParentSpanID
			}
		}
		correlation.HTTPRequestID = firstNonEmpty(correlation.HTTPRequestID, stored.HTTPRequestID)
	} else if correlation.TraceID == "" {
		correlation = observability.NewTrace()
	}
	correlation.CursorRequestID = firstNonEmpty(correlation.CursorRequestID, requestID)
	correlation.ConversationID = firstNonEmpty(
		correlation.ConversationID,
		strings.TrimSpace(conversationID),
		recorder.conversationIDForRequest(requestID),
		stored.ConversationID,
	)
	correlation.ModelCallID = firstNonEmpty(correlation.ModelCallID, debugString(rawEvent, "model_call_id"))
	recorder.rememberCorrelation(requestID, correlation)

	layer := firstNonEmpty(debugString(rawEvent, "layer"), "forwarder")
	eventName := strings.TrimSuffix(strings.TrimSpace(filename), filepath.Ext(filename))
	if value := debugString(rawEvent, "event"); value != "" {
		eventName = value
	}
	status := debugString(rawEvent, "status")
	errorCategory := ""
	payloadFields, _ := rawEvent["payload"].(map[string]any)
	errorText := firstNonEmpty(debugString(rawEvent, "error"), debugString(payloadFields, "error"))
	if errorText != "" {
		errorCategory = layer + "_error"
		status = "error"
	}
	if status == "" {
		switch eventName {
		case "terminal", "terminal_after_context_done":
			if debugString(rawEvent, "terminal_error_code") == "" {
				status = "completed"
			} else {
				status = "error"
				errorCategory = "terminal_error"
			}
		case "llm_summary":
			status = "completed"
		case "llm_request", "subscribe":
			status = "started"
		}
	}
	fields := make(map[string]any)
	for _, key := range []string{"append_seqno", "byte_len", "client_kind", "data_len", "direction", "kind", "message_case", "finish_reason", "ttft_ms"} {
		if value, ok := rawEvent[key]; ok {
			fields[key] = value
		} else if value, ok := payloadFields[key]; ok {
			fields[key] = value
		}
	}
	protocol := ""
	executionTarget := "local_runtime"
	switch layer {
	case "bidi_raw", "bidi_decoded":
		protocol = "connect_unary"
	case "runsse":
		protocol = "connect_stream"
	case "provider":
		protocol = "http_stream"
		executionTarget = "provider"
	}
	requestBytes := int64(0)
	responseBytes := int64(0)
	if layer == "bidi_raw" {
		requestBytes = readInt64Value(rawEvent["data_len"])
	}
	if layer == "provider" && eventName == "llm_response_chunk" {
		responseBytes = readInt64Value(payloadFields["byte_len"])
	}
	durationMS := readInt64Value(payloadFields["duration_ms"])
	payloadData := any(rawEvent)
	decodeError := false
	if layer == "bidi_raw" {
		metadata := make(map[string]any, len(rawEvent))
		for key, value := range rawEvent {
			if key == "data_hex" {
				continue
			}
			metadata[key] = value
		}
		metadata["raw_omitted"] = true
		payloadData = metadata
		decodeError = true
	}
	recorder.capture.Record(observability.WithCorrelation(ctx, correlation), observability.Capture{
		Event: observability.Event{
			Layer:           layer,
			Event:           eventName,
			Route:           debugString(rawEvent, "procedure"),
			ExecutionTarget: executionTarget,
			Protocol:        protocol,
			Status:          status,
			ErrorCategory:   errorCategory,
			DurationMS:      durationMS,
			RequestBytes:    requestBytes,
			ResponseBytes:   responseBytes,
			DecodeError:     decodeError,
			Fields:          fields,
		},
		Payload: &observability.Payload{
			Name:        eventName,
			ContentType: "application/json",
			Data:        payloadData,
		},
	})
	if eventName == "terminal" || eventName == "terminal_after_context_done" {
		recorder.forgetCorrelation(requestID)
	}
}

func (recorder *debugRecorder) correlationForRequest(requestID string) observability.Correlation {
	if recorder == nil || strings.TrimSpace(requestID) == "" {
		return observability.Correlation{}
	}
	recorder.mu.Lock()
	defer recorder.mu.Unlock()
	return recorder.correlations[strings.TrimSpace(requestID)]
}

func (recorder *debugRecorder) rememberCorrelation(requestID string, correlation observability.Correlation) {
	if recorder == nil || strings.TrimSpace(requestID) == "" {
		return
	}
	recorder.mu.Lock()
	if len(recorder.correlations) >= 4096 {
		for key := range recorder.correlations {
			delete(recorder.correlations, key)
			break
		}
	}
	recorder.correlations[strings.TrimSpace(requestID)] = correlation
	recorder.mu.Unlock()
}

func (recorder *debugRecorder) forgetCorrelation(requestID string) {
	if recorder == nil || strings.TrimSpace(requestID) == "" {
		return
	}
	recorder.mu.Lock()
	delete(recorder.correlations, strings.TrimSpace(requestID))
	recorder.mu.Unlock()
}

func debugString(event map[string]any, key string) string {
	value, ok := event[key]
	if !ok || value == nil {
		return ""
	}
	return strings.TrimSpace(fmt.Sprint(value))
}

func (recorder *debugRecorder) debugDir(requestID string, conversationID string) string {
	if recorder == nil || strings.TrimSpace(recorder.historyRoot) == "" {
		return ""
	}
	conversationID = firstNonEmpty(strings.TrimSpace(conversationID), recorder.conversationIDForRequest(requestID))
	if conversationID != "" && conversationID != "unknown" {
		return filepath.Join(recorder.historyRoot, sanitizeArtifactName(conversationID), "debug")
	}
	requestID = firstNonEmpty(strings.TrimSpace(requestID), "unknown")
	return filepath.Join(recorder.historyRoot, "_debug", "orphan", sanitizeArtifactName(requestID))
}

func (recorder *debugRecorder) conversationIDForRequest(requestID string) string {
	if recorder == nil || recorder.broker == nil {
		return ""
	}
	stream, ok := recorder.broker.Get(requestID)
	if !ok || stream == nil {
		return ""
	}
	stream.mu.Lock()
	defer stream.mu.Unlock()
	return strings.TrimSpace(stream.ConversationID)
}

func agentClientMessageCase(message *agentv1.AgentClientMessage) string {
	if message == nil {
		return ""
	}
	switch message.GetMessage().(type) {
	case *agentv1.AgentClientMessage_RunRequest:
		return "run_request"
	case *agentv1.AgentClientMessage_PrewarmRequest:
		return "prewarm_request"
	case *agentv1.AgentClientMessage_ConversationAction:
		return "conversation_action"
	case *agentv1.AgentClientMessage_ExecClientMessage:
		return "exec_client_message"
	case *agentv1.AgentClientMessage_ExecClientControlMessage:
		return "exec_client_control_message"
	case *agentv1.AgentClientMessage_InteractionResponse:
		return "interaction_response"
	case *agentv1.AgentClientMessage_ClientHeartbeat:
		return "client_heartbeat"
	case *agentv1.AgentClientMessage_KvClientMessage:
		return "kv_client_message"
	default:
		return fmt.Sprintf("%T", message.GetMessage())
	}
}

func agentServerMessageCase(message *agentv1.AgentServerMessage) string {
	if message == nil {
		return ""
	}
	switch message.GetMessage().(type) {
	case *agentv1.AgentServerMessage_InteractionUpdate:
		return "interaction_update"
	case *agentv1.AgentServerMessage_ExecServerMessage:
		return "exec_server_message"
	case *agentv1.AgentServerMessage_ExecServerControlMessage:
		return "exec_server_control_message"
	case *agentv1.AgentServerMessage_ConversationCheckpointUpdate:
		return "conversation_checkpoint_update"
	case *agentv1.AgentServerMessage_KvServerMessage:
		return "kv_server_message"
	case *agentv1.AgentServerMessage_InteractionQuery:
		return "interaction_query"
	default:
		return fmt.Sprintf("%T", message.GetMessage())
	}
}

func conversationActionCase(message *agentv1.AgentClientMessage) string {
	if message == nil {
		return ""
	}
	action := message.GetConversationAction()
	if action == nil && message.GetRunRequest() != nil {
		action = message.GetRunRequest().GetAction()
	}
	if action == nil {
		return ""
	}
	return conversationActionKind(action)
}

func requestedModelDebugPayload(message *agentv1.AgentClientMessage) map[string]any {
	if message == nil {
		return nil
	}
	if runRequest := message.GetRunRequest(); runRequest != nil {
		return requestedModelPayload(runRequest.GetRequestedModel())
	}
	if prewarm := message.GetPrewarmRequest(); prewarm != nil {
		return requestedModelPayload(prewarm.GetRequestedModel())
	}
	return nil
}

func requestedModelPayload(model *agentv1.RequestedModel) map[string]any {
	if model == nil {
		return nil
	}
	parameters := make([]map[string]string, 0, len(model.GetParameters()))
	for _, parameter := range model.GetParameters() {
		if parameter == nil {
			continue
		}
		parameters = append(parameters, map[string]string{
			"id":    parameter.GetId(),
			"value": parameter.GetValue(),
		})
	}
	return map[string]any{
		"model_id":                         strings.TrimSpace(model.GetModelId()),
		"max_mode":                         model.GetMaxMode(),
		"built_in_model":                   model.GetBuiltInModel(),
		"is_variant_string_representation": model.GetIsVariantStringRepresentation(),
		"parameters":                       parameters,
	}
}

func summarizeDebugArtifactPayload(payload map[string]any) map[string]any {
	if len(payload) == 0 {
		return nil
	}
	summary := make(map[string]any)
	for _, key := range []string{
		"request_id",
		"run_id",
		"model_call_id",
		"provider",
		"openai_endpoint",
		"model",
		"runtime_model_id",
		"resolved_channel_id",
		"resolved_channel_name",
		"started_at",
		"first_event_at",
		"finished_at",
		"finish_reason",
		"input_tokens",
		"output_tokens",
		"cache_read_tokens",
		"cache_write_tokens",
		"prompt_tokens_total",
		"request_tokens_total",
		"error",
		"ttft_ms",
		"duration_ms",
		"byte_len",
	} {
		if value, ok := payload[key]; ok {
			summary[key] = value
		}
	}
	if body, ok := payload["body"]; ok && body != nil {
		summary["body_omitted"] = true
	}
	if chunk, ok := payload["raw_chunk"].(string); ok {
		summary["raw_chunk_byte_len"] = len([]byte(chunk))
	}
	return summary
}

func deferredProtoJSONDebugPayload(message proto.Message) debugFieldEncoder {
	return func() ([]byte, error) {
		if message == nil {
			return []byte("null"), nil
		}
		return protojson.MarshalOptions{
			UseProtoNames:   true,
			EmitUnpopulated: false,
		}.Marshal(message)
	}
}

func deferredInboundIntentDebugPayload(intent InboundIntent) debugFieldEncoder {
	return func() ([]byte, error) {
		return json.Marshal(inboundIntentDebugPayload(intent))
	}
}

func protoJSONDebugPayload(message proto.Message) any {
	if message == nil {
		return nil
	}
	payload, err := protojson.MarshalOptions{
		UseProtoNames:   true,
		EmitUnpopulated: false,
	}.Marshal(message)
	if err != nil {
		return map[string]any{"marshal_error": err.Error()}
	}
	var decoded any
	if err := json.Unmarshal(payload, &decoded); err != nil {
		return string(payload)
	}
	return decoded
}

func inboundIntentDebugPayload(intent InboundIntent) map[string]any {
	payload := map[string]any{
		"kind":               strings.TrimSpace(intent.Kind),
		"request_id":         strings.TrimSpace(intent.RequestID),
		"conversation_id":    strings.TrimSpace(intent.ConversationID),
		"model_id":           strings.TrimSpace(intent.ModelID),
		"model_name":         strings.TrimSpace(intent.ModelName),
		"thinking_effort":    strings.TrimSpace(intent.ThinkingEffort),
		"mode":               intent.Mode.String(),
		"has_explicit_mode":  intent.HasExplicitMode,
		"mode_source":        string(intent.ModeSource),
		"starts_run":         intent.StartsRun,
		"subagent_type_name": strings.TrimSpace(intent.SubagentTypeName),
		"cancel_reason":      strings.TrimSpace(intent.CancelReason),
		"prewarm":            intent.Prewarm,
	}
	if len(intent.SubagentModelOverrides) > 0 {
		payload["subagent_model_overrides"] = subagentModelOverrideSummaries(intent.SubagentModelOverrides)
		payload["subagent_model_override_count"] = len(intent.SubagentModelOverrides)
	}
	if intent.ClientMessage != nil {
		payload["client_message"] = protoJSONDebugPayload(intent.ClientMessage)
	}
	if intent.ConversationState != nil {
		payload["conversation_state"] = protoJSONDebugPayload(intent.ConversationState)
	}
	if intent.UserMessage != nil {
		payload["user_message"] = protoJSONDebugPayload(intent.UserMessage)
	}
	if intent.RequestContext != nil {
		payload["request_context"] = protoJSONDebugPayload(intent.RequestContext)
	}
	if strings.TrimSpace(intent.IgnoredReason) != "" {
		payload["ignored_reason"] = strings.TrimSpace(intent.IgnoredReason)
		payload["ignored_empty_resume"] = strings.TrimSpace(intent.IgnoredReason) == "empty_resume_without_pending_continuation"
	}
	if intent.ExecClientMessage != nil {
		payload["exec_client_message"] = protoJSONDebugPayload(intent.ExecClientMessage)
	}
	if intent.ExecClientControlMessage != nil {
		payload["exec_client_control_message"] = protoJSONDebugPayload(intent.ExecClientControlMessage)
	}
	if intent.InteractionResponse != nil {
		payload["interaction_response"] = protoJSONDebugPayload(intent.InteractionResponse)
	}
	if intent.KVClientMessage != nil {
		payload["kv_client_message"] = protoJSONDebugPayload(intent.KVClientMessage)
	}
	return payload
}

func conversationActionKind(action *agentv1.ConversationAction) string {
	if action == nil {
		return ""
	}
	switch action.GetAction().(type) {
	case *agentv1.ConversationAction_UserMessageAction:
		return "user_message_action"
	case *agentv1.ConversationAction_ResumeAction:
		return "resume_action"
	case *agentv1.ConversationAction_CancelAction:
		return "cancel_action"
	case *agentv1.ConversationAction_SummarizeAction:
		return "summarize_action"
	case *agentv1.ConversationAction_ShellCommandAction:
		return "shell_command_action"
	case *agentv1.ConversationAction_StartPlanAction:
		return "start_plan_action"
	case *agentv1.ConversationAction_ExecutePlanAction:
		return "execute_plan_action"
	case *agentv1.ConversationAction_AsyncAskQuestionCompletionAction:
		return "async_ask_question_completion_action"
	case *agentv1.ConversationAction_CancelSubagentAction:
		return "cancel_subagent_action"
	case *agentv1.ConversationAction_BackgroundTaskCompletionAction:
		return "background_task_completion_action"
	case *agentv1.ConversationAction_BackgroundShellAction:
		return "background_shell_action"
	case *agentv1.ConversationAction_BackgroundSubagentAction:
		return "background_subagent_action"
	default:
		return fmt.Sprintf("%T", action.GetAction())
	}
}
