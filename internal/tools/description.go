// Package tools — N16 tool description 系统实现。
// 本文件定义 ToolDescription 结构化描述与 Render 渲染逻辑，
// 解决对标分析 G-01/G-02/G-09（描述格式不统一 / 段落缺失 / 中英混杂）。
package tools

import (
	"fmt"
	"log"
	"strings"
)

// ToolDescription 描述工具的结构化描述，遵守 N16 格式规范。
// 段顺序固定：Purpose → Parameters → Behavior → Constraints → Examples。
// Purpose 与 Behavior 必填，空值时 Render 降级返回占位符并记录日志。
type ToolDescription struct {
	Purpose     string      // 必填，一句话用途，≤200 字符
	Parameters  []ParamDesc // 必填（有参数时），参数说明列表
	Behavior    string      // 必填，行为说明
	Constraints string      // 可选，约束说明
	Examples    string      // 可选，使用示例
}

// ParamDesc 描述工具参数的说明，与 Schema 字段一一对应。
type ParamDesc struct {
	Name     string // 参数名
	Type     string // 类型（string/integer/boolean/object/array）
	Required bool   // 是否必填
	Desc     string // 参数说明
}

// Render 按标准格式渲染为发送给 AI 的 description 字符串。
// 段间用 \n\n 分隔，段标题用 **粗体** 标记。
// Purpose 为空时降级返回占位符并记录日志，不 panic（健壮性要求）。
func (d ToolDescription) Render() string {
	return d.render(true)
}

// RenderLean 渲染为不含 Parameters 段的精简 description（优化 A）。
// 前提：参数描述已由 InputSchema 的 property description 提供
// （见 ParametersCoveredBySchema），description 中重复一份属于双份传输，
// 浪费每次请求的固定 token。未覆盖时调用方应回退 Render()。
func (d ToolDescription) RenderLean() string {
	return d.render(false)
}

// render 按标准格式渲染，includeParams 控制是否输出 Parameters 段。
func (d ToolDescription) render(includeParams bool) string {
	var b strings.Builder
	// Purpose 必填，空则降级
	if d.Purpose == "" {
		log.Printf("[N16] ToolDescription.Purpose 为空，降级返回占位符")
		b.WriteString("**Purpose**: (unspecified)\n\n")
	} else {
		b.WriteString(fmt.Sprintf("**Purpose**: %s\n\n", d.Purpose))
	}
	// Parameters 段（有参数时必填；RenderLean 省略，由 schema 提供参数描述）
	if includeParams && len(d.Parameters) > 0 {
		b.WriteString("**Parameters**:\n")
		for _, p := range d.Parameters {
			req := "optional"
			if p.Required {
				req = "required"
			}
			b.WriteString(fmt.Sprintf("- %s (%s, %s): %s\n", p.Name, p.Type, req, p.Desc))
		}
		b.WriteString("\n")
	}
	// Behavior 必填
	if d.Behavior != "" {
		b.WriteString(fmt.Sprintf("**Behavior**: %s\n\n", d.Behavior))
	}
	// Constraints 可选
	if d.Constraints != "" {
		b.WriteString(fmt.Sprintf("**Constraints**: %s\n\n", d.Constraints))
	}
	// Examples 可选
	if d.Examples != "" {
		b.WriteString(fmt.Sprintf("**Examples**: %s", d.Examples))
	}
	return strings.TrimRight(b.String(), "\n")
}

// ParametersCoveredBySchema 判断 d.Parameters 中每个参数是否都已在 schema 的
// property description 中提供描述（优化 A 的安全门控）。
// 全部覆盖时可用 RenderLean 省去 description 中的 Parameters 段（消除双份传输）；
// 任一参数缺失或描述为空时必须回退 Render()，避免参数说明丢失。
// 无参数工具恒为 true。
func (d ToolDescription) ParametersCoveredBySchema(schema ToolSchema) bool {
	if len(d.Parameters) == 0 {
		return true
	}
	if len(schema.Properties) == 0 {
		return false
	}
	for _, p := range d.Parameters {
		prop, ok := schema.Properties[p.Name]
		if !ok || prop == nil || strings.TrimSpace(prop.Description) == "" {
			return false
		}
	}
	return true
}
