package runtimecore

import (
	"strings"
	"testing"
)

func TestDecodeMCPToolPayloadRejectsInvalidArguments(t *testing.T) {
	for _, value := range []string{`"{\"secret-marker\":"`, `["secret-marker"]`, `12`, `true`, `"[]"`, `"42"`, `"false"`} {
		for _, field := range []string{"arguments", "args"} {
			t.Run(field+value, func(t *testing.T) {
				raw := `{"server":"s","toolName":"cleanup","` + field + `":` + value + `}`
				if field == "arguments" {
					raw = strings.TrimSuffix(raw, "}") + `,"args":{"fallback":true}}`
				}
				_, err := DecodeMCPToolPayload([]byte(raw))
				if err == nil {
					t.Fatal("非法参数被接受")
				}
				if !strings.Contains(err.Error(), field) || strings.Contains(err.Error(), "secret-marker") {
					t.Fatalf("错误缺字段或泄漏正文: %v", err)
				}
			})
		}
	}
}

func TestDecodeMCPToolPayloadPreservesEmptyAndAliasCompatibility(t *testing.T) {
	for _, value := range []string{`null`, `""`, `"  "`, `"null"`, `{}`, `"{}"`} {
		raw := `{"arguments":` + value + `,"args":{"scope":"only"}}`
		payload, err := DecodeMCPToolPayload([]byte(raw))
		if err != nil || payload.Arguments["scope"] != "only" {
			t.Fatalf("空值别名兼容改变: %s %v %#v", value, err, payload)
		}
	}
	for _, raw := range []string{``, `null`, `{}`, `{"arguments":{"scope":"only"},"args":[]}`, `{"arguments":"{\"scope\":\"only\"}"}`} {
		payload, err := DecodeMCPToolPayload([]byte(raw))
		if err != nil {
			t.Fatalf("合法参数失败: %s %v", raw, err)
		}
		if payload.Arguments == nil {
			t.Fatal("空对象未保留")
		}
	}
}
