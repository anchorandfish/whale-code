package hook

import (
	"crypto/rand"
	"encoding/hex"
)

// toHook 根据 Type 字段构造具体的 Hook 实现
// 从配置文件反序列化的钩子没有 Source 字段（json:"-"），统一设为 SourceConfig
//
// D2（S7-P1-①）：`enabled` 的三态在**这里**落实 —— 配置未写该字段 ⇒ 默认启用；
// 显式写了 ⇒ 按值，并打上 enabledExplicit 标记（IsEnabled 的唯一判据）。
func (r hookRaw) toHook() Hook {
	base := HookBase{
		ID:       r.ID,
		Type:     r.Type,
		Matcher:  r.Matcher,
		Pattern:  r.Pattern,
		Action:   r.Action,
		Reason:   r.Reason,
		Source:   SourceConfig,
		Timeout:  r.Timeout,
		Enabled:  true, // 默认启用；下面按显式声明覆盖
		Priority: r.Priority,
	}
	if r.Enabled != nil {
		base.Enabled = *r.Enabled
		base.enabledExplicit = true
	}
	switch r.Type {
	case HookTypePrompt:
		return &HookPrompt{HookBase: base, Prompt: r.Prompt, Model: r.Model}
	case HookTypeAgent:
		return &HookAgent{HookBase: base, Prompt: r.Prompt, Model: r.Model}
	case HookTypeHTTP:
		return &HookHTTP{HookBase: base, URL: r.URL, Headers: r.Headers}
	case HookTypeCallback:
		return &HookCallback{HookBase: base}
	default:
		// 空 Type 或 "command" 都归为 HookCommand（向后兼容旧配置）
		if base.Type == "" {
			base.Type = HookTypeCommand
		}
		return &HookCommand{HookBase: base, Command: r.Command}
	}
}

// generateHookID 生成唯一钩子标识符
func generateHookID() string {
	b := make([]byte, 4)
	_, _ = rand.Read(b)
	return "hook_" + hex.EncodeToString(b)
}

// generateID 生成唯一钩子标识符（generateHookID 别名）
// 返回 "hook_" + 8 个十六进制字符，例如 "hook_a1b2c3d4"
func generateID() string {
	return generateHookID()
}

// NewHook 创建命令类型钩子（便捷构造函数）
// 自动生成唯一 ID 并设置默认值
func NewHook(event, command string) Hook {
	return &HookCommand{
		HookBase: HookBase{
			ID:      generateID(),
			Type:    HookTypeCommand,
			Matcher: "*",
			Action:  ActionAllow,
			Enabled: true,
			Source:  SourceConfig,
		},
		Command: command,
	}
}
