package audit

import (
	"encoding/binary"
	"fmt"
	"strings"

	"google.golang.org/protobuf/encoding/protowire"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/reflect/protoreflect"
	"google.golang.org/protobuf/reflect/protoregistry"
	"google.golang.org/protobuf/types/dynamicpb"
)

// SummarizeProtoRequest resolves the generated request by RPC path and records
// schema metadata only. Connect envelopes are inspected without changing the
// forwarded body. Unknown or compressed payloads produce DecodeError.
func (observer *Observer) SummarizeProtoRequest(rpcPath string, contentType string, body []byte) ProtoSummary {
	summary := ProtoSummary{RequestBytes: len(body), StringBytes: map[string]int{}, BytesBytes: map[string]int{}, RepeatedCounts: map[string]int{}, OneofCases: map[string]string{}}
	messageType, err := resolveRequestType(rpcPath)
	if err != nil {
		summary.DecodeError = true
		return summary
	}
	payload, ok := protoPayload(contentType, body)
	if !ok {
		summary.DecodeError = true
		return summary
	}
	summary.MessageType = string(messageType.Descriptor().FullName())
	message := dynamicpb.NewMessage(messageType.Descriptor())
	if err := proto.Unmarshal(payload, message); err != nil {
		summary.DecodeError = true
		return summary
	}
	walkMessage(observer, message.ProtoReflect(), "", &summary, 0)
	return summary
}

func protoPayload(contentType string, body []byte) ([]byte, bool) {
	mediaType := strings.ToLower(strings.TrimSpace(strings.SplitN(contentType, ";", 2)[0]))
	if mediaType != "application/connect+proto" {
		return body, true
	}
	const envelopeHeaderBytes = 5
	if len(body) < envelopeHeaderBytes || body[0] != 0 {
		return nil, false
	}
	payloadBytes := binary.BigEndian.Uint32(body[1:envelopeHeaderBytes])
	if uint64(payloadBytes) != uint64(len(body)-envelopeHeaderBytes) {
		return nil, false
	}
	return body[envelopeHeaderBytes:], true
}

func resolveRequestType(rpcPath string) (protoreflect.MessageType, error) {
	parts := strings.Split(strings.Trim(rpcPath, "/"), "/")
	if len(parts) != 2 || parts[0] == "" || parts[1] == "" {
		return nil, fmt.Errorf("invalid rpc path")
	}
	service := protoreflect.FullName(parts[0])
	method := parts[1]
	packageName := protoreflect.FullName(service)
	if separator := strings.LastIndex(string(packageName), "."); separator >= 0 {
		packageName = protoreflect.FullName(string(packageName)[:separator])
	}
	candidates := []protoreflect.FullName{
		packageName + "." + protoreflect.FullName(method) + "Request",
		service + "." + protoreflect.FullName(method) + "Request",
		protoreflect.FullName(strings.TrimSuffix(string(service), "Service")) + "." + protoreflect.FullName(method) + "Request",
	}
	for _, candidate := range candidates {
		if messageType, err := protoregistry.GlobalTypes.FindMessageByName(candidate); err == nil {
			return messageType, nil
		}
	}
	return nil, fmt.Errorf("request message is not registered")
}

func walkMessage(observer *Observer, message protoreflect.Message, prefix string, summary *ProtoSummary, depth int) {
	if depth > 16 {
		return
	}
	message.Range(func(field protoreflect.FieldDescriptor, value protoreflect.Value) bool {
		name := string(field.Name())
		path := name
		if prefix != "" {
			path = prefix + "." + name
		}
		summary.FieldPresence = append(summary.FieldPresence, path)
		addSensitiveCategories(summary, name)
		if field.IsList() {
			list := value.List()
			summary.RepeatedCounts[path] = list.Len()
			for index := 0; index < list.Len(); index++ {
				walkValue(observer, field, list.Get(index), path, summary, depth)
			}
			return true
		}
		if field.IsMap() {
			mapped := value.Map()
			summary.RepeatedCounts[path] = mapped.Len()
			mapped.Range(func(_ protoreflect.MapKey, mapValue protoreflect.Value) bool {
				walkValue(observer, field.MapValue(), mapValue, path, summary, depth)
				return true
			})
			return true
		}
		walkValue(observer, field, value, path, summary, depth)
		if oneof := field.ContainingOneof(); oneof != nil && !oneof.IsSynthetic() {
			summary.OneofCases[string(oneof.Name())] = name
		}
		return true
	})
}

func walkValue(observer *Observer, field protoreflect.FieldDescriptor, value protoreflect.Value, path string, summary *ProtoSummary, depth int) {
	switch field.Kind() {
	case protoreflect.StringKind:
		text := value.String()
		summary.StringBytes[path] += len(text)
		if observer.MatchCanary([]byte(text)) {
			summary.CanaryMatched = true
		}
	case protoreflect.BytesKind:
		data := value.Bytes()
		summary.BytesBytes[path] += len(data)
		if observer.MatchCanary(data) {
			summary.CanaryMatched = true
		}
	case protoreflect.MessageKind, protoreflect.GroupKind:
		walkMessage(observer, value.Message(), path, summary, depth+1)
	case protoreflect.EnumKind:
		number := value.Enum()
		if enumValue := field.Enum().Values().ByNumber(number); enumValue != nil {
			summary.EnumPresence = append(summary.EnumPresence, path+"="+string(enumValue.Name()))
		} else {
			summary.EnumPresence = append(summary.EnumPresence, path)
		}
	}
}

func addSensitiveCategories(summary *ProtoSummary, fieldName string) {
	name := strings.ToLower(fieldName)
	categories := []struct {
		match    bool
		category string
	}{
		{strings.Contains(name, "prompt") || strings.Contains(name, "text"), "prompt_or_text"},
		{strings.Contains(name, "file") || strings.Contains(name, "content") || strings.Contains(name, "bytes"), "file_or_content"},
		{strings.Contains(name, "path") || strings.Contains(name, "workspace") || strings.Contains(name, "repository"), "path_or_workspace"},
		{strings.Contains(name, "diff") || strings.Contains(name, "edit") || strings.Contains(name, "history"), "edit_or_diff"},
		{strings.Contains(name, "git") || strings.Contains(name, "branch") || strings.Contains(name, "commit"), "git_metadata"},
		{strings.Contains(name, "token") || strings.Contains(name, "key") || strings.Contains(name, "secret") || strings.Contains(name, "credential"), "credential_like"},
	}
	for _, item := range categories {
		if item.match && !containsString(summary.SensitiveCategories, item.category) {
			summary.SensitiveCategories = append(summary.SensitiveCategories, item.category)
		}
	}
}

func containsString(values []string, target string) bool {
	for _, value := range values {
		if value == target {
			return true
		}
	}
	return false
}

// IsLikelyProto reports whether a body has a valid protobuf wire prefix. It is
// intentionally conservative and is used only for tests and diagnostics.
func IsLikelyProto(body []byte) bool {
	if len(body) == 0 {
		return true
	}
	_, _, consumed := protowire.ConsumeField(body)
	return consumed > 0
}
