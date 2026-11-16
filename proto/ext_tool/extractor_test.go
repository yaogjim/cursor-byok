package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const compactBundleFixture = `exports.modules={1(e,t,n){
var o="agent.v1.";
class s extends n.HL{static $p(){return o}}
var a=(0,n.QT)(n.proto3,o,"Mode",[[0,"UNSPECIFIED"],[1,"FAST"]],1);
class d extends s{constructor(){super(),n.proto3.util.initPartial(undefined,this)}static $(){return["Hello|1 mode #0|2 name 9|3 tags 9*|4 m 9,#0",a]}}
class h extends s{static $(){return["Frame|1 hello #0 one|2 mode #1 result|3 nested #0",d,a]}}
class k extends d{static $(){return["Hello.Nested|1 value 8?|2 items #0*",d]}}
}}`

func TestExtractCompactMessagesAndEnums(t *testing.T) {
	text := compactBundleFixture
	moduleStarts := buildModuleStarts(text)
	messages, enums, err := extractCompactTypes(text, moduleStarts)
	if err != nil {
		t.Fatalf("extract compact types: %v", err)
	}

	byName := make(map[string]Message, len(messages))
	for _, msg := range messages {
		byName[msg.TypeName] = msg
	}
	if len(messages) != 3 {
		t.Fatalf("messages = %d, want 3: %#v", len(messages), byName)
	}
	for _, want := range []string{"agent.v1.Hello", "agent.v1.Frame", "agent.v1.Hello.Nested"} {
		if _, ok := byName[want]; !ok {
			t.Fatalf("missing %s in %#v", want, byName)
		}
	}

	hello := byName["agent.v1.Hello"]
	if hello.Package != "agent.v1" || hello.ShortName != "Hello" || hello.VarName != "d" {
		t.Fatalf("hello identity = %+v", hello)
	}
	fields := make(map[string]Field, len(hello.Fields))
	for _, field := range hello.Fields {
		fields[field.Name] = field
	}
	if field := fields["mode"]; field.Kind != "enum" || field.T != "a" {
		t.Fatalf("mode field = %+v", field)
	}
	if field := fields["name"]; field.Kind != "scalar" || field.T != 9 {
		t.Fatalf("name field = %+v", field)
	}
	if field := fields["tags"]; !field.Repeated || field.Kind != "scalar" || field.T != 9 {
		t.Fatalf("tags field = %+v", field)
	}
	if field := fields["m"]; field.Kind != "map" || field.MapKey != 9 || field.MapValueKind != "enum" || field.MapValueT != "a" {
		t.Fatalf("map field = %+v", field)
	}

	frame := byName["agent.v1.Frame"]
	if frame.Package != "agent.v1" {
		t.Fatalf("frame package = %q", frame.Package)
	}
	var oneofs []string
	for _, field := range frame.Fields {
		if field.Oneof != "" {
			oneofs = append(oneofs, field.Name+":"+field.Oneof)
		}
	}
	if strings.Join(oneofs, ",") != "hello:one,mode:result" {
		t.Fatalf("frame oneofs = %#v", oneofs)
	}

	nested := byName["agent.v1.Hello.Nested"]
	if nested.ShortName != "Hello.Nested" {
		t.Fatalf("nested short name = %q", nested.ShortName)
	}
	nestedFields := make(map[string]Field, len(nested.Fields))
	for _, field := range nested.Fields {
		nestedFields[field.Name] = field
	}
	if field := nestedFields["value"]; !field.Opt || field.Kind != "scalar" || field.T != 8 {
		t.Fatalf("nested value field = %+v", field)
	}
	if field := nestedFields["items"]; !field.Repeated || field.Kind != "message" || field.T != "d" {
		t.Fatalf("nested items field = %+v", field)
	}

	if len(enums) != 1 {
		t.Fatalf("enums = %d, want 1", len(enums))
	}
	enum := enums[0]
	if enum.TypeName != "agent.v1.Mode" || enum.Package != "agent.v1" || enum.VarName != "a" {
		t.Fatalf("enum identity = %+v", enum)
	}
	wantValues := []EnumValue{{No: 0, Name: "MODE_UNSPECIFIED"}, {No: 1, Name: "MODE_FAST"}}
	if len(enum.Values) != len(wantValues) {
		t.Fatalf("enum values = %#v", enum.Values)
	}
	for i := range wantValues {
		if enum.Values[i] != wantValues[i] {
			t.Fatalf("enum value[%d] = %+v, want %+v", i, enum.Values[i], wantValues[i])
		}
	}
}

func TestExtractCompactTypesRejectsUnresolvedReference(t *testing.T) {
	text := `exports.modules={1(e,t,n){
var o="agent.v1.";
class s extends n.HL{static $p(){return o}}
class d extends s{static $(){return["Broken|1 item #3"]}}
}}`
	if _, _, err := extractCompactTypes(text, buildModuleStarts(text)); err == nil {
		t.Fatal("expected out-of-range compact reference to fail")
	}
}

func TestExtractCompactTypesRejectsMissingPackage(t *testing.T) {
	text := `exports.modules={1(e,t,n){
class d extends n.HL{static $(){return["Orphan|1 name 9"]}}
}}`
	if _, _, err := extractCompactTypes(text, buildModuleStarts(text)); err == nil {
		t.Fatal("expected missing compact package to fail")
	}
}

// 覆盖 bundle → 提取入口 → 既有 service 保留/过滤 → 实际 proto 文件。
func TestCompactExtractionPreservesServicesAndGeneratesFields(t *testing.T) {
	dir := t.TempDir()
	input := filepath.Join(dir, "bundle.js")
	output := filepath.Join(dir, "agent_v1.proto")
	if err := os.WriteFile(input, []byte(compactBundleFixture), 0600); err != nil {
		t.Fatal(err)
	}
	seed := `syntax = "proto3";
package agent.v1;
service AgentService {
  rpc Run (stream Frame) returns (stream Hello);
  rpc Removed (Gone) returns (Hello);
}
`
	if err := os.WriteFile(output, []byte(seed), 0600); err != nil {
		t.Fatal(err)
	}
	ExtractProtos(input, dir)
	data, err := os.ReadFile(output)
	if err != nil {
		t.Fatal(err)
	}
	got := string(data)
	for _, want := range []string{"message Hello", "string name = 2;", "repeated string tags = 3;", "map<string, Mode> m = 4;", "optional bool value = 1;", "oneof one", "rpc Run (stream Frame) returns (stream Hello);"} {
		if !strings.Contains(got, want) {
			t.Errorf("生成 proto 缺少 %q", want)
		}
	}
	if strings.Contains(got, "rpc Removed") || strings.Contains(got, "field_") {
		t.Fatal("输出包含悬空 RPC 或占位字段")
	}
}

func TestParseFieldObjectSupportsShorthandType(t *testing.T) {
	field, err := parseFieldObject(`{no:4,name:"file_not_found",kind:"message",T,oneof:"result"}`)
	if err != nil {
		t.Fatalf("parse shorthand T: %v", err)
	}
	if field.T != "T" {
		t.Fatalf("parsed shorthand T as %#v, want T", field.T)
	}
}

func TestWebpackExportAliasResolvesServiceMessageType(t *testing.T) {
	const bundle = `
1:(e,t,n)=>{
  n.d(t,{KS:()=>T,_B:()=>r});
  var r;
  class T {}
  T.typeName="agent.v1.AgentClientMessage";
  n.proto3.util.setEnumType(r,"agent.v1.DiagnosticSeverity",[]);
},
2:(e,t,n)=>{
  var r=n(1);
  const service={typeName:"agent.v1.AgentService",methods:{run:{name:"Run",I:r.KS,O:r.KS,kind:n.MethodKind.BiDiStreaming}}};
}`

	moduleStarts := buildModuleStarts(bundle)
	messages := []Message{{
		TypeName:     "agent.v1.AgentClientMessage",
		VarName:      "T",
		InternalName: "T",
		Package:      "agent.v1",
		Pos:          35,
		ModuleStart:  moduleStartForPos(moduleStarts, 35),
	}}
	enums := []Enum{{
		TypeName:    "agent.v1.DiagnosticSeverity",
		VarName:     "r",
		Package:     "agent.v1",
		Pos:         100,
		ModuleStart: moduleStartForPos(moduleStarts, 100),
	}}

	resolver := newTypeResolver(messages, enums, buildAliasIndex(bundle, moduleStarts), buildWebpackExportAliasIndex(bundle, moduleStarts))
	resolver.moduleImports = buildModuleImportIndex(bundle, moduleStarts)
	typeName, ok := resolver.ResolveTypeName("r.KS", len(bundle)-1, moduleStartForPos(moduleStarts, len(bundle)-1), "agent.v1", "message")
	if !ok {
		t.Fatal("expected webpack export alias to resolve")
	}
	if typeName != "agent.v1.AgentClientMessage" {
		t.Fatalf("resolved r.KS to %q, want agent.v1.AgentClientMessage", typeName)
	}
}

func TestResolverPrefersExpectedKindOverCurrentPackage(t *testing.T) {
	resolver := &TypeResolver{bySymbol: map[string][]symbolDef{
		"nt": {
			{TypeName: "git_forge.v1.GetTagResponse", Kind: "message", Pos: 10, ModuleStart: 1},
			{TypeName: "origin.v1.TeamGroupKind", Kind: "enum", Pos: 20, ModuleStart: 1},
		},
	}}

	typeName, ok := resolver.ResolveTypeName("nt", 30, 1, "origin.v1", "message")
	if !ok {
		t.Fatal("expected cross-package message type to resolve")
	}
	if typeName != "git_forge.v1.GetTagResponse" {
		t.Fatalf("resolved nt to %q, want git_forge.v1.GetTagResponse", typeName)
	}
}
