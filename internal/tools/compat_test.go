// Package tools — N16 向后兼容测试（~）。
// 覆盖反例 16：ai.ToolDefinition 结构不变（name/description/input_schema 三字段）。
// 覆盖反例 17：Spec.Description 仍是 string 类型（编译期检查）。
// 覆盖反例 18：Spec.InputSchema 仍是 map[string]interface{} 类型（编译期检查）。
// 覆盖反例 19：Spec 新增字段 Desc/Schema/Limits 用 json:"-"（不序列化）。
package tools

import (
	"encoding/json"
	"testing"

	"github.com/anchorandfish/whale-code/internal/ai"
)

// TestToolDefinition_StructUnchanged 反例 16：ai.ToolDefinition 结构不变。
// 序列化后应含 name/description/input_schema 三字段，无新增字段。
func TestToolDefinition_StructUnchanged(t *testing.T) {
	def := ai.ToolDefinition{
		Name:        "test",
		Description: "test desc",
		InputSchema: map[string]interface{}{"type": "object"},
	}
	b, _ := json.Marshal(def)
	var m map[string]interface{}
	_ = json.Unmarshal(b, &m)
	if _, ok := m["name"]; !ok {
		t.Error("ai.ToolDefinition 应有 name 字段")
	}
	if _, ok := m["description"]; !ok {
		t.Error("ai.ToolDefinition 应有 description 字段")
	}
	if _, ok := m["input_schema"]; !ok {
		t.Error("ai.ToolDefinition 应有 input_schema 字段")
	}
	// 校验无多余字段（仅 3 字段）
	if len(m) != 3 {
		t.Errorf("ai.ToolDefinition 应仅 3 字段，got %d: %v", len(m), m)
	}
}

// TestSpec_DescriptionStillString 反例 17：Spec.Description 仍是 string 类型。
// 编译期检查：若 Description 类型改变，编译失败。
func TestSpec_DescriptionStillString(t *testing.T) {
	s := Spec{
		Name:        "test",
		Description: "test desc",
	}
	// 编译期检查：若 Description 类型改变，编译失败
	var desc string = s.Description
	_ = desc
}

// TestSpec_InputSchemaStillMap 反例 18：Spec.InputSchema 仍是 map[string]interface{} 类型。
// 编译期检查：若 InputSchema 类型改变，编译失败。
func TestSpec_InputSchemaStillMap(t *testing.T) {
	s := Spec{
		Name:        "test",
		InputSchema: map[string]interface{}{"type": "object"},
	}
	// 编译期检查：若 InputSchema 类型改变，编译失败
	var schema map[string]interface{} = s.InputSchema
	_ = schema
}

// TestSpec_NewFieldsNotSerialized 反例 19：Spec 新增字段用 json:"-"（不序列化）。
// Desc/Schema/Limits 字段应标 json:"-"，序列化后不出现。
func TestSpec_NewFieldsNotSerialized(t *testing.T) {
	s := Spec{
		Name:        "test",
		Description: "test desc",
		Desc:        ToolDescription{Purpose: "test"},
		Schema:      ToolSchema{Type: "object"},
		Limits:      ToolLimits{MaxConcurrency: 1},
	}
	b, _ := json.Marshal(s)
	var m map[string]interface{}
	_ = json.Unmarshal(b, &m)
	if _, ok := m["desc"]; ok {
		t.Error("Desc 字段应标 json:\"-\"，不应序列化")
	}
	if _, ok := m["schema"]; ok {
		t.Error("Schema 字段应标 json:\"-\"，不应序列化")
	}
	if _, ok := m["limits"]; ok {
		t.Error("Limits 字段应标 json:\"-\"，不应序列化")
	}
	// 序列化后应仅含 name/description/input_schema/required_permission/concurrency_safe
	if _, ok := m["name"]; !ok {
		t.Error("Spec 应有 name 字段")
	}
	if _, ok := m["description"]; !ok {
		t.Error("Spec 应有 description 字段")
	}
}
