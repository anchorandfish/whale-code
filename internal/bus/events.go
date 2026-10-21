// Package bus — 事件类型定义
//
// 本文件定义跨模块共享的事件类型。
// 权威定义方：internal/bus 包。
// 发布方和订阅方见各事件注释。
package bus

import "time"

// --- 缓存断裂事件 ---

// CacheBreakEvent 表示一次缓存断裂事件。
// 权威发布方：AI 模块（CachingIntegration.CheckBreakAfterResponse）。
// 订阅方：Context 模块（触发压缩）、TUI（显示缓存状态）。
//
// 注意：C18 冲突解决——CacheBreakEvent 的唯一发布方是 AI 模块，
// Context 模块订阅后触发压缩，不重复发布。
type CacheBreakEvent struct {
	Model         string
	BreakReason   string
	CacheReadDrop float64 // 缓存读取 token 下降比例
	OccurredAt    time.Time
}

// Type 返回事件类型标识。
func (e CacheBreakEvent) Type() string { return "cache_break" }

// Timestamp 返回事件发生时间。
func (e CacheBreakEvent) Timestamp() time.Time { return e.OccurredAt }

// --- 工具/会话相关事件 ---

// ToolsChangedEvent 表示工具列表变更事件。
// 发布方：MCP 模块（list_changed 触发时）。
// 订阅方：Context 模块（更新工具列表排序）、TUI（显示提示）。
//
// 注意：C21 冲突解决——list_changed 自动刷新 + 信息性事件提示，
// 不阻塞用户。
type ToolsChangedEvent struct {
	ServerName string // MCP 服务器名称
	Change     string // 变更类型（如 "added", "removed", "updated"）
	OccurredAt time.Time
}

// Type 返回事件类型标识。
func (e ToolsChangedEvent) Type() string { return "tools_changed" }

// Timestamp 返回事件发生时间。
func (e ToolsChangedEvent) Timestamp() time.Time { return e.OccurredAt }

// --- 资源变更事件（/ M17-01）---

// ResourcesChangedEvent 表示 MCP 服务器资源变更事件。
// 发布方：MCP 模块（resources/updated 或 resources/list_changed 通知触发时）。
// 订阅方：TUI 模块（异步刷新资源列表渲染）、Context 模块（更新资源缓存）。
//
// 注意： 冲突解决——资源变更通过事件解耦，MCP 不直接调用 TUI，
// TUI 订阅后异步渲染，不阻塞 MCP readLoop。
type ResourcesChangedEvent struct {
	ServerName string // MCP 服务器名称
	Method     string // 通知方法名（如 "resources/updated", "resources/list_changed"）
	URI        string // 变更资源 URI（list_changed 时为空串）
	OccurredAt time.Time
}

// Type 返回事件类型标识。
func (e ResourcesChangedEvent) Type() string { return "resources_changed" }

// Timestamp 返回事件发生时间。
func (e ResourcesChangedEvent) Timestamp() time.Time { return e.OccurredAt }

// SessionSwitchEvent 表示会话切换事件。
// 发布方：Session 模块（会话切换时）。
// 订阅方：Context 模块（触发 ClearSessionCaches + ReloadFromSession）。
//
// C20 冲突解决：通过事件解耦，Session 不直接调用 Context。
type SessionSwitchEvent struct {
	OldSessionID string
	NewSessionID string
	OccurredAt   time.Time
}

// Type 返回事件类型标识。
func (e SessionSwitchEvent) Type() string { return "session_switch" }

// Timestamp 返回事件发生时间。
func (e SessionSwitchEvent) Timestamp() time.Time { return e.OccurredAt }

// SessionForkEvent 表示会话 fork 事件。
// 发布方：Session 模块（ForkSession 时）。
// 订阅方：Context 模块（触发 ClearSessionCaches + ReloadFromSession）。
//
// C20 冲突解决：通过事件解耦，Session 不直接调用 Context。
type SessionForkEvent struct {
	ParentSessionID string
	ForkSessionID   string
	OccurredAt      time.Time
}

// Type 返回事件类型标识。
func (e SessionForkEvent) Type() string { return "session_fork" }

// Timestamp 返回事件发生时间。
func (e SessionForkEvent) Timestamp() time.Time { return e.OccurredAt }
