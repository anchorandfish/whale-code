// Package tools — N16 ToolDescription 测试。
// 覆盖反例：空 Purpose 降级 / 分段格式校验 / Parameters 渲染 / 段顺序。
package tools

import (
	"strings"
	"testing"
)

// TestToolDescription_RenderEmptyPurpose 反例 1：Purpose 为空时 Render 应降级返回占位符，不 panic。
func TestToolDescription_RenderEmptyPurpose(t *testing.T) {
	d := ToolDescription{Purpose: ""}
	got := d.Render()
	// 期望：返回占位符 + 日志，不 panic
	if !strings.Contains(got, "Purpose") {
		t.Fatalf("空 Purpose 应降级返回占位符，got: %q", got)
	}
	if !strings.Contains(got, "(unspecified)") {
		t.Errorf("空 Purpose 应包含 (unspecified) 占位符，got: %q", got)
	}
}

// TestToolDescription_RenderFormat 反例 2：分段格式校验，所有段标题用粗体标记。
func TestToolDescription_RenderFormat(t *testing.T) {
	d := ToolDescription{
		Purpose:    "Execute bash command",
		Behavior:   "Runs command in sandbox",
		Parameters: []ParamDesc{{Name: "command", Type: "string", Required: true, Desc: "The command to execute"}},
	}
	got := d.Render()
	if !strings.Contains(got, "**Purpose**") {
		t.Error("缺 Purpose 段")
	}
	if !strings.Contains(got, "**Parameters**") {
		t.Error("缺 Parameters 段")
	}
	if !strings.Contains(got, "**Behavior**") {
		t.Error("缺 Behavior 段")
	}
}

// TestToolDescription_RenderParameters 校验 Parameters 渲染格式（required/optional 标记）。
func TestToolDescription_RenderParameters(t *testing.T) {
	d := ToolDescription{
		Purpose: "test",
		Parameters: []ParamDesc{
			{Name: "command", Type: "string", Required: true, Desc: "cmd"},
			{Name: "timeout", Type: "integer", Required: false, Desc: "to"},
		},
	}
	got := d.Render()
	if !strings.Contains(got, "command (string, required): cmd") {
		t.Errorf("required 参数格式错误，got: %q", got)
	}
	if !strings.Contains(got, "timeout (integer, optional): to") {
		t.Errorf("optional 参数格式错误，got: %q", got)
	}
}

// TestToolDescription_RenderOptionalSections 校验 Constraints/Examples 可选段仅在有值时输出。
func TestToolDescription_RenderOptionalSections(t *testing.T) {
	// 无可选段
	d1 := ToolDescription{Purpose: "p", Behavior: "b"}
	got1 := d1.Render()
	if strings.Contains(got1, "**Constraints**") {
		t.Error("无 Constraints 时不应输出该段")
	}

	// 有可选段
	d2 := ToolDescription{Purpose: "p", Behavior: "b", Constraints: "c", Examples: "e"}
	got2 := d2.Render()
	if !strings.Contains(got2, "**Constraints**") {
		t.Error("有 Constraints 时应输出该段")
	}
	if !strings.Contains(got2, "**Examples**") {
		t.Error("有 Examples 时应输出该段")
	}
}

// TestToolDescription_RenderLean 优化 A：schema 已含参数描述时 description 不再重复 Parameters 段。
// 参数描述双份传输（desc tag → schema property description + Parameters 段 → description）
// 浪费固定 token，RenderLean 省去 Parameters 段，其余段保持不变。
func TestToolDescription_RenderLean(t *testing.T) {
	d := ToolDescription{
		Purpose:    "Execute bash command",
		Behavior:   "Runs command in sandbox",
		Parameters: []ParamDesc{{Name: "command", Type: "string", Required: true, Desc: "The command to execute"}},
	}
	got := d.RenderLean()
	if strings.Contains(got, "**Parameters**") {
		t.Errorf("RenderLean 不应包含 Parameters 段（schema 已提供），got: %q", got)
	}
	if strings.Contains(got, "The command to execute") {
		t.Errorf("RenderLean 不应重复参数描述文本，got: %q", got)
	}
	for _, want := range []string{"**Purpose**", "Execute bash command", "**Behavior**", "Runs command in sandbox"} {
		if !strings.Contains(got, want) {
			t.Errorf("RenderLean 应保留 %q，got: %q", want, got)
		}
	}
	// Render() 原行为不变（作为 schema 未覆盖时的兜底）
	if !strings.Contains(d.Render(), "**Parameters**") {
		t.Error("Render() 应保持包含 Parameters 段（兜底路径）")
	}
}

// TestToolDescription_ParametersCoveredBySchema 校验"参数描述已被 schema 覆盖"判定。
func TestToolDescription_ParametersCoveredBySchema(t *testing.T) {
	d := ToolDescription{
		Purpose: "p",
		Parameters: []ParamDesc{
			{Name: "command", Type: "string", Required: true, Desc: "cmd"},
			{Name: "timeout", Type: "integer", Required: false, Desc: "to"},
		},
	}
	// 全部参数有 schema description → covered
	covered := ToolSchema{
		Type: "object",
		Properties: map[string]*SchemaProperty{
			"command": {Type: "string", Description: "cmd"},
			"timeout": {Type: "integer", Description: "to"},
		},
	}
	if !d.ParametersCoveredBySchema(covered) {
		t.Error("所有参数均有 schema description 时应判定 covered")
	}
	// 缺一个参数 → not covered
	partial := ToolSchema{
		Type: "object",
		Properties: map[string]*SchemaProperty{
			"command": {Type: "string", Description: "cmd"},
		},
	}
	if d.ParametersCoveredBySchema(partial) {
		t.Error("schema 缺少 timeout 参数时应判定 not covered")
	}
	// 参数存在但 description 为空 → not covered
	emptyDesc := ToolSchema{
		Type: "object",
		Properties: map[string]*SchemaProperty{
			"command": {Type: "string", Description: ""},
			"timeout": {Type: "integer", Description: "to"},
		},
	}
	if d.ParametersCoveredBySchema(emptyDesc) {
		t.Error("command 的 schema description 为空时应判定 not covered")
	}
	// 无参数工具 → 恒 covered
	if !(ToolDescription{Purpose: "p"}).ParametersCoveredBySchema(ToolSchema{}) {
		t.Error("无参数工具应恒为 covered")
	}
}

// TestRegistryBuiltinDescriptionNoParameterDuplication 优化 A 集成验证：
// 注册后 builtin 工具（如 bash，schema 全覆盖参数描述）的 Description 不含 Parameters 段，
// 且 InputSchema 的 property description 完整保留（单一信息源）。
func TestRegistryBuiltinDescriptionNoParameterDuplication(t *testing.T) {
	r := NewRegistry()
	spec, ok := r.Get("bash")
	if !ok {
		t.Fatal("builtin bash tool should exist")
	}
	if strings.Contains(spec.Description, "**Parameters**") {
		t.Errorf("bash Description 不应再含 Parameters 段（schema 已覆盖），got: %q", spec.Description)
	}
	if !strings.Contains(spec.Description, "**Purpose**") || !strings.Contains(spec.Description, "**Behavior**") {
		t.Errorf("bash Description 应保留 Purpose/Behavior 段，got: %q", spec.Description)
	}
	schema, ok := spec.InputSchema["properties"].(map[string]interface{})
	if !ok {
		t.Fatalf("bash InputSchema 缺少 properties: %#v", spec.InputSchema)
	}
	cmdProp, ok := schema["command"].(map[string]interface{})
	if !ok || cmdProp["description"] != "Bash command to execute" {
		t.Errorf("schema property description 丢失: %#v", schema["command"])
	}
}
