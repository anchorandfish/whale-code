// ask_settings.go — T3 S3 提问相关配置读取。
// 从 ~/.whale/settings.json 读取 interaction.ask_threshold 字段。
// 默认值 1，范围 0-3（越界 clamp，REF-降级策略-优化4）。
// v0.2 边界：不做热加载（settings.json 修改需重启进程，v0.3 再加 fsnotify）。
package tui

import (
	"encoding/json"
	"github.com/anchorandfish/whale-code/internal/whalehome"
	"os"
	"path/filepath"
)

// AskThresholdDefault 默认提问阈值（每条用户消息最多提问次数，REF-降级策略-优化4）。
const AskThresholdDefault = 1

// AskThresholdMin ask_threshold 下限（0=完全禁用日常对话提问）。
const AskThresholdMin = 0

// AskThresholdMax ask_threshold 上限（防恶意 settings.json 设置超大值绕过限制）。
const AskThresholdMax = 3

// AskSettings 提问相关配置（REF-T3-S3-新增，从 ~/.whale/settings.json 读取）。
// AskThreshold：每条用户消息最多提问次数（0-3，默认 1）。
type AskSettings struct {
	AskThreshold int `json:"ask_threshold"` // 每条用户消息最多提问次数（0-3，默认 1）
}

// LoadAskSettings 从 settings.json 读取提问配置（REF-T3-S3-新增）。
// 路径：~/.whale/settings.json 的 interaction.ask_threshold 字段。
// clamp 逻辑：<0 视为 0，>3 视为 3（REF-降级策略-优化4）。
// 任何读取/解析错误均降级为默认值 1，绝不返回 nil。
// 「块/键缺失」⇒ 默认值 1；「显式写 0」⇒ 0（禁用提问）。两者不得共用零值判定。
func LoadAskSettings() *AskSettings {
	// 收口：Dir() 返回空（home 不可得）⇒ 默认值，行为同前。
	if whalehome.Dir() == "" {
		return &AskSettings{AskThreshold: AskThresholdDefault}
	}
	path := filepath.Join(whalehome.Dir(), "settings.json")
	data, err := os.ReadFile(path)
	if err != nil {
		return &AskSettings{AskThreshold: AskThresholdDefault}
	}

	// 仅解析 interaction.ask_threshold 字段（其他字段忽略，避免影响）
	//
	// ★ ：必须用**指针**承载「键是否存在」，不能用 int 的零值。
	//
	//	int 的零值是 0，而 0 在本字段是**合法且有极端语义**的值（0 = 完全禁用日常对话提问）
	//	⇒ 「没写这个键」与「显式写了 0」在 int 上不可区分，会一起被解成"用户要禁用提问"。
	//
	//	真实事故（取证 · 2026-10-06）：外层 Save 曾把磁盘上的 interaction 块整体删除
	//	（配置层已于 根治：保存改为读-改-写），本函数随后把"块不存在"读成 0
	//	⇒ tool_exec.go:555 / :834 的 `m.askCount >= m.askSettings.AskThreshold` 变成
	//	`0 >= 0` **恒真** ⇒ 模型每次调用 AskUser 都被静默跳过 ⇒ 面板永不弹。
	//	用户看到的现象是"AI 不问我了"，完全看不出根因在配置读取的零值歧义上。
	//
	//	语义（两侧都由判据钉住，见 ask_threshold_presence_test.go）：
	// · interaction 块缺失 / ask_threshold 键缺失 ⇒ **回落默认值**（提问未被禁用）
	// · 显式写 0 ⇒ **尊重用户意图**（禁用提问）
	var raw struct {
		Interaction *struct {
			AskThreshold *int `json:"ask_threshold"`
		} `json:"interaction"`
	}
	if err := json.Unmarshal(data, &raw); err != nil {
		return &AskSettings{AskThreshold: AskThresholdDefault}
	}
	if raw.Interaction == nil || raw.Interaction.AskThreshold == nil {
		return &AskSettings{AskThreshold: AskThresholdDefault}
	}

	threshold := *raw.Interaction.AskThreshold
	// clamp 到 0-3 范围（REF-降级策略-优化4）
	if threshold < AskThresholdMin {
		threshold = AskThresholdMin
	} else if threshold > AskThresholdMax {
		threshold = AskThresholdMax
	}
	return &AskSettings{AskThreshold: threshold}
}

// DefaultAskSettings 返回默认提问配置（用于无法读取 settings.json 时降级）。
func DefaultAskSettings() *AskSettings {
	return &AskSettings{AskThreshold: AskThresholdDefault}
}
