// Package config — 配置管理。
// fast_mode.go — 快速模式状态机，切换到快速响应模型。
// 对标同类 CLI FastModeRuntimeState。
// M9 修订：轻量化 cooldown（参考同类实现 简化，不实现指数退避）。
package config

import "time"

// FastModeState fast 模式状态。
// 对标同类 CLI getFastModeState 三态。
type FastModeState string

const (
	// FastModeOff 已关闭：使用默认模型
	FastModeOff FastModeState = "off"
	// FastModeCooldown 冷却中：限流/过载时自动进入，到期自动恢复
	FastModeCooldown FastModeState = "cooldown"
	// FastModeOn 已开启：使用快速模型
	FastModeOn FastModeState = "on"
)

// FastModeConfig fast 模式配置。
// 由 Config 统一管理，通过 Config 的读写锁保护（关卡 4 并发安全）。
type FastModeConfig struct {
	// Enabled 用户是否开启 fast mode
	Enabled bool
	// FastModel 快速模型名称（可配置，对标同类 CLI FAST_MODE_MODEL_DISPLAY）
	FastModel string
	// DefaultModel 默认模型名称
	DefaultModel string
	// CooldownUntil 冷却结束时间（M9：固定 60 秒，不实现指数退避）
	CooldownUntil time.Time
	// CooldownReason 冷却原因（rate_limit / overloaded）
	CooldownReason string
}

// GetFastModeState 获取当前 fast mode 状态。
// 对标同类 CLI getFastModeState。
// 行为规约：自动检查冷却是否到期，到期后自动恢复为 on。
func GetFastModeState(cfg FastModeConfig) FastModeState {
	if !cfg.Enabled {
		return FastModeOff
	}
	// 冷却到期检查：若 CooldownUntil 已过，自动恢复为 on
	if cfg.CooldownUntil.IsZero() || time.Now().After(cfg.CooldownUntil) {
		return FastModeOn
	}
	return FastModeCooldown
}

// TriggerCooldown 触发 fast mode 冷却。
// 对标同类 CLI triggerFastModeCooldown。
// M9 修订：固定 duration，不实现指数退避。
// 调用方需在写锁保护下进行（关卡 4 并发安全）。
func (c *FastModeConfig) TriggerCooldown(duration time.Duration, reason string) {
	c.CooldownUntil = time.Now().Add(duration)
	c.CooldownReason = reason
}

// ClearCooldown 清除冷却状态。
// 调用方需在写锁保护下进行（关卡 4 并发安全）。
func (c *FastModeConfig) ClearCooldown() {
	c.CooldownUntil = time.Time{}
	c.CooldownReason = ""
}

// GetCurrentModel 根据 fast mode 状态返回当前使用的模型。
// 对标同类 CLI getCurrentModel。
// 行为规约：fast 模式开启且非冷却时返回 FastModel，否则返回 DefaultModel。
// M13 协同：N2 cost tracker 通过此函数获取当前模型，按模型分别归集成本。
func GetCurrentModel(cfg FastModeConfig) string {
	state := GetFastModeState(cfg)
	if state == FastModeOn {
		return cfg.FastModel
	}
	return cfg.DefaultModel
}

// DefaultCooldownDuration fast mode 默认冷却时长（M9：固定 60 秒）。
// 对标同类 CLI cooldown 机制，但简化为固定时长。
const DefaultCooldownDuration = 60 * time.Second
