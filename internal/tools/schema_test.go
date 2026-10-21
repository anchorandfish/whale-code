// Package tools — N16 ToolSchema 测试。
// 覆盖反例：json:"-" 排除 / ToMap 等价 / 嵌套结构 / 深度限制 / Go 类型映射。
package tools

import (
	"reflect"
	"testing"
)

// TestBuildSchemaFromStruct_ExcludesHiddenField 反例 3：BuildSchemaFromStruct 应排除 json:"-" 字段。
func TestBuildSchemaFromStruct_ExcludesHiddenField(t *testing.T) {
	type TestInput struct {
		Command string `json:"command"`
		Hidden  string `json:"-"`
	}
	s := BuildSchemaFromStruct(TestInput{})
	if _, ok := s.Properties["hidden"]; ok {
		t.Error("json:\"-\" 字段不应出现在 schema 中")
	}
	if _, ok := s.Properties["command"]; !ok {
		t.Error("command 字段应出现在 schema 中")
	}
}

// TestToolSchema_ToMapEquivalent 反例 4：ToMap 与原 map 等价。
func TestToolSchema_ToMapEquivalent(t *testing.T) {
	s := &ToolSchema{
		Type: "object",
		Properties: map[string]*SchemaProperty{
			"cmd": {Type: "string", Description: "command"},
		},
		Required: []string{"cmd"},
	}
	m := s.ToMap()
	if m["type"] != "object" {
		t.Error("type 不匹配")
	}
	props, ok := m["properties"].(map[string]interface{})
	if !ok {
		t.Fatal("properties 不是 map 类型")
	}
	cmdProp, ok := props["cmd"].(map[string]interface{})
	if !ok {
		t.Fatal("cmd 属性不是 map 类型")
	}
	if cmdProp["type"] != "string" {
		t.Error("cmd type 不匹配")
	}
}

// TestToolSchema_NilToMap 校验 nil 接收者 ToMap 返回最小 object schema。
func TestToolSchema_NilToMap(t *testing.T) {
	var s *ToolSchema
	m := s.ToMap()
	if m["type"] != "object" {
		t.Error("nil ToolSchema ToMap 应返回 type=object")
	}
}

// TestBuildSchemaFromStruct_Nested 校验嵌套结构体递归生成 Properties。
func TestBuildSchemaFromStruct_Nested(t *testing.T) {
	type Inner struct {
		Field1 string `json:"field1"`
	}
	type Outer struct {
		Name  string `json:"name"`
		Inner Inner  `json:"inner"`
	}
	s := BuildSchemaFromStruct(Outer{})
	if _, ok := s.Properties["inner"]; !ok {
		t.Fatal("inner 字段应出现在 schema 中")
	}
	innerProp := s.Properties["inner"]
	if innerProp.Type != "object" {
		t.Errorf("inner 类型应为 object，got %s", innerProp.Type)
	}
	if len(innerProp.Properties) == 0 {
		t.Error("inner 应有嵌套 properties")
	}
	if _, ok := innerProp.Properties["field1"]; !ok {
		t.Error("inner.field1 应出现在嵌套 schema 中")
	}
}

// TestBuildSchemaFromStruct_RequiredFields 校验无 omitempty 的字段进入 required 列表。
func TestBuildSchemaFromStruct_RequiredFields(t *testing.T) {
	type Input struct {
		Required string `json:"required"`
		Optional string `json:"optional,omitempty"`
	}
	s := BuildSchemaFromStruct(Input{})
	found := false
	for _, r := range s.Required {
		if r == "required" {
			found = true
		}
	}
	if !found {
		t.Error("required 字段应在 Required 列表中")
	}
	for _, r := range s.Required {
		if r == "optional" {
			t.Error("omitempty 字段不应在 Required 列表中")
		}
	}
}

// TestBuildSchemaFromStruct_OmitEmptyIsOptional 反例 5：omitempty 字段应为可选（不出现在 Required）。
func TestBuildSchemaFromStruct_OmitEmptyIsOptional(t *testing.T) {
	type Input struct {
		Must   string `json:"must"`
		Maybe  string `json:"maybe,omitempty"`
		Maybe2 int    `json:"maybe2,omitempty"`
	}
	s := BuildSchemaFromStruct(Input{})
	// must 应在 Required 中
	mustFound := false
	for _, r := range s.Required {
		if r == "must" {
			mustFound = true
		}
	}
	if !mustFound {
		t.Error("must 字段应在 Required 列表中")
	}
	// maybe/maybe2 不应在 Required 中
	for _, r := range s.Required {
		if r == "maybe" || r == "maybe2" {
			t.Errorf("omitempty 字段 %s 不应在 Required 列表中", r)
		}
	}
}

// TestGoTypeToJSONType 反例 8：Go 类型映射到 JSON Schema type。
// 覆盖 6 种 Go 类型：string/integer/number/boolean/array/object。
func TestGoTypeToJSONType(t *testing.T) {
	cases := []struct {
		goType   interface{}
		jsonType string
	}{
		{"", "string"},
		{0, "integer"},
		{0.0, "number"},
		{false, "boolean"},
		{[]string{}, "array"},
		{struct{}{}, "object"},
	}
	for _, c := range cases {
		got := goTypeToJSONType(reflect.TypeOf(c.goType))
		if got != c.jsonType {
			t.Errorf("Go type %T should map to %s, got %s", c.goType, c.jsonType, got)
		}
	}
}
