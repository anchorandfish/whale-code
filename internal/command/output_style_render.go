// Package command — 命令注册与处理。
// output_style_render.go — output style 的渲染层配置，供 N9 消费。
// 契约：N14 自定义边界（非 02 §2.2.2，M4 修复），style 影响 render（与 N9 协同）。
// 待 N9 合入后对接：N9 渲染 Message/diff 组件时消费 OutputStyleRenderConfig。
package command

import "strings"

// OutputStyleRenderConfig 输出风格的渲染配置。
// N9 diff 渲染模块消费此配置控制展示细节（N14 自定义边界，M4 修复：非 02 §2.2.2）。
// 关卡 6 性能：值类型，无堆分配。
type OutputStyleRenderConfig struct {
	// ShowThinking 是否显示思考过程
	ShowThinking bool
	// ShowCodeExplanation 是否显示代码解释
	ShowCodeExplanation bool
	// ShowDiffContext diff 上下文行数（concise 时减少，verbose 时增加）
	ShowDiffContext int
	// CollapseLongOutput 是否折叠长输出
	CollapseLongOutput bool
	// MaxDisplayLines 最大显示行数
	MaxDisplayLines int
}

// GetOutputStyleRenderConfig 根据 output style 获取渲染配置。
// 契约：N14 自定义边界（非 02 §2.2.2，M4 修复），N9 消费此配置。
// 行为规约：
//   - concise：隐藏思考/代码解释，减少 diff 上下文，折叠长输出，限 50 行
//   - default：显示思考/代码解释，标准 diff 上下文，不折叠，限 200 行
//   - verbose：显示思考/代码解释，增加 diff 上下文，不折叠，限 500 行
//   - technical：显示思考/代码解释，较多 diff 上下文，不折叠，限 300 行
func GetOutputStyleRenderConfig(style string) OutputStyleRenderConfig {
	switch strings.ToLower(style) {
	case "concise":
		return OutputStyleRenderConfig{
			ShowThinking:        false,
			ShowCodeExplanation: false,
			ShowDiffContext:     3,
			CollapseLongOutput:  true,
			MaxDisplayLines:     50,
		}
	case "verbose":
		return OutputStyleRenderConfig{
			ShowThinking:        true,
			ShowCodeExplanation: true,
			ShowDiffContext:     10,
			CollapseLongOutput:  false,
			MaxDisplayLines:     500,
		}
	case "technical":
		return OutputStyleRenderConfig{
			ShowThinking:        true,
			ShowCodeExplanation: true,
			ShowDiffContext:     8,
			CollapseLongOutput:  false,
			MaxDisplayLines:     300,
		}
	default: // "default" 及未知值
		return OutputStyleRenderConfig{
			ShowThinking:        true,
			ShowCodeExplanation: true,
			ShowDiffContext:     5,
			CollapseLongOutput:  false,
			MaxDisplayLines:     200,
		}
	}
}
