package hook

import (
	"fmt"
	"net/http"
	"time"

	"github.com/anchorandfish/whale-code/internal/sandbox"
)

// NewManager 创建钩子管理器
func NewManager(workDir string) *Manager {
	return &Manager{
		workDir:    workDir,
		httpClient: &http.Client{Timeout: 30 * time.Second},
	}
}

// SetAIClient 设置 AI 客户端（prompt/agent 类型必需）
func (m *Manager) SetAIClient(client HookAIClient) {
	m.aiClient = client
}

// SetHTTPClient 设置 HTTP 客户端（http 类型必需）
func (m *Manager) SetHTTPClient(client *http.Client) {
	m.httpClient = client
}

// SetSandboxManager 注入沙箱管理器 + 设置默认沙箱级别（SNAPSHOT）。
//
// 子审查优化 2 吸纳：同时接收 defaultLevel，避免用户忘记设置导致沙箱不生效。
//
// 参数：
// - mgr: 沙箱管理器实例（nil=禁用沙箱，向后兼容）
// - defaultLevel: 默认沙箱级别（disabled=跳过沙箱 / relaxed / strict / isolated）
//
// 行为：
// - sandboxMgr=nil 时所有 HookCommand 跳过沙箱（与现状一致）
// - sandboxMgr!=nil 时 HookCommand 按 hook.SandboxLevel（fallback 到 defaultLevel）调用 Prepare
// - Prepare/Restrict 失败时降级执行 + 记录日志（fail-open）
//
// 官方源： @2026-07-20（OSSandbox 接口）
func (m *Manager) SetSandboxManager(mgr sandbox.Manager, defaultLevel sandbox.SandboxLevel) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.sandboxMgr = mgr
	m.defaultSandboxLevel = defaultLevel
}

// SetDefaultSandboxLevel 运行时调整默认沙箱级别（SNAPSHOT）。
//
// 用于在不重新创建 Manager 的情况下调整沙箱策略。
// 不影响已设置的 sandboxMgr（仅调整级别）。
//
// 参数：
// - level: 新的默认沙箱级别
func (m *Manager) SetDefaultSandboxLevel(level sandbox.SandboxLevel) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.defaultSandboxLevel = level
}

// SetObservabilityConfig 配置可观测性（SNAPSHOT）。
//
// obs=nil 时全部 noop（零开销）；调用此方法后启用 trace/stats/slow-alert。
// 配置覆盖：旧 obs 状态会被新状态替换（不影响已运行的 hook 执行）。
//
// 参数：
// - cfg.EnableTrace: 是否收集 span 到 ringBuffer（默认 false，零开销）
// - cfg.EnableStats: 是否收集统计（默认 true，用 atomic.Int64，轻量）
// - cfg.SlowHookThreshold: 慢钩子阈值（0 用 DefaultSlowHookThreshold=5s）
// - cfg.TraceSink / cfg.StatsSink: 可注入 Prometheus / log / OTel adapter
// - cfg.SlowHookAlert: 慢钩子告警回调（含 5s TTL 去重）
func (m *Manager) SetObservabilityConfig(cfg ObservabilityConfig) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.obs = newObservabilityState(cfg)
}

// startSpan 在 ExecuteHookWithPayload 入口启动 span（SNAPSHOT）。
//
// obs=nil 时返回零值 span（不收集），保证零开销。
func (m *Manager) startSpan(h Hook, payload HookPayload, eventType HookEventType) HookSpan {
	b := h.Base()
	return HookSpan{
		HookID:    b.ID,
		HookType:  b.Type,
		EventType: eventType,
		ToolName:  payload.ToolName,
		Start:     time.Now(),
	}
}

// endSpan 在 ExecuteHookWithPayload 出口记录 span + 触发慢钩子告警（SNAPSHOT）。
//
// 通过 defer 调用，捕获 result 指针读取最终值；
// obs=nil 时直接返回，保证零开销。
func (m *Manager) endSpan(span *HookSpan, result *HookResult) {
	span.Duration = time.Since(span.Start)
	span.Action = result.Action
	span.Error = result.Error
	span.Output = result.Output
	if m.obs != nil {
		m.obs.recordSpan(*span)
	}
}

// RemoveAllPluginHooks 移除所有来源为 plugin 的钩子
// 插件启用/禁用后重新注入前调用，确保钩子列表与活跃插件一致
func (m *Manager) RemoveAllPluginHooks() {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.config.PreToolUse = filterHooksBySource(m.config.PreToolUse, SourcePlugin, false)
	m.config.PostToolUse = filterHooksBySource(m.config.PostToolUse, SourcePlugin, false)
	m.config.PreRead = filterHooksBySource(m.config.PreRead, SourcePlugin, false)
	m.config.PostRead = filterHooksBySource(m.config.PostRead, SourcePlugin, false)
	m.config.PreWrite = filterHooksBySource(m.config.PreWrite, SourcePlugin, false)
	m.config.PostWrite = filterHooksBySource(m.config.PostWrite, SourcePlugin, false)
	m.config.PreEdit = filterHooksBySource(m.config.PreEdit, SourcePlugin, false)
	m.config.PostEdit = filterHooksBySource(m.config.PostEdit, SourcePlugin, false)
	m.config.PreBash = filterHooksBySource(m.config.PreBash, SourcePlugin, false)
	m.config.PostBash = filterHooksBySource(m.config.PostBash, SourcePlugin, false)
	m.config.PreSearch = filterHooksBySource(m.config.PreSearch, SourcePlugin, false)
	m.config.PostSearch = filterHooksBySource(m.config.PostSearch, SourcePlugin, false)
	m.config.PreGit = filterHooksBySource(m.config.PreGit, SourcePlugin, false)
	m.config.PostGit = filterHooksBySource(m.config.PostGit, SourcePlugin, false)
	m.config.PreWeb = filterHooksBySource(m.config.PreWeb, SourcePlugin, false)
	m.config.PostWeb = filterHooksBySource(m.config.PostWeb, SourcePlugin, false)
	m.config.PreAgent = filterHooksBySource(m.config.PreAgent, SourcePlugin, false)
	m.config.PostAgent = filterHooksBySource(m.config.PostAgent, SourcePlugin, false)
	m.config.SessionStart = filterHooksBySource(m.config.SessionStart, SourcePlugin, false)
	m.config.SessionStop = filterHooksBySource(m.config.SessionStop, SourcePlugin, false)
	m.config.Stop = filterHooksBySource(m.config.Stop, SourcePlugin, false)
	m.config.PostCompact = filterHooksBySource(m.config.PostCompact, SourcePlugin, false)
	m.config.Notification = filterHooksBySource(m.config.Notification, SourcePlugin, false)
}

// AddHook 添加钩子到指定事件类型
// eventName 使用字符串（如 "PreToolUse"、"Stop" 等），对应 HookEventType
// 注意：此方法会修改传入 Hook 的 ID（若为空则自动生成）和 Source（设为 SourceConfig）。
// 调用方在调用后不应复用该 Hook 变量，因为 Base() 返回的是内部指针。
// Enabled 字段不会被强制修改，调用方需自行设置（Go bool 零值为 false）。
func (m *Manager) AddHook(eventName string, h Hook) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	b := h.Base()
	if b.ID == "" {
		b.ID = generateHookID()
	}
	b.Source = SourceConfig
	hooks := m.hooksByEventName(eventName)
	if hooks == nil {
		return fmt.Errorf("unknown event type: %s", eventName)
	}
	*hooks = append(*hooks, h)
	m.sortHooksByEvent(eventName)
	return nil
}

// RemoveHook 按索引从指定事件类型移除钩子
func (m *Manager) RemoveHook(eventName string, index int) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	hooks := m.hooksByEventName(eventName)
	if hooks == nil {
		return fmt.Errorf("unknown event type: %s", eventName)
	}
	if index < 0 || index >= len(*hooks) {
		return fmt.Errorf("invalid index %d (range 0-%d)", index, len(*hooks)-1)
	}
	*hooks = append((*hooks)[:index], (*hooks)[index+1:]...)
	return nil
}

// RemoveHookByID 按唯一 ID 从指定事件类型移除钩子
func (m *Manager) RemoveHookByID(eventName string, id string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	hooks := m.hooksByEventName(eventName)
	if hooks == nil {
		return fmt.Errorf("unknown event type: %s", eventName)
	}
	for i, hook := range *hooks {
		if hook.Base().ID == id {
			*hooks = append((*hooks)[:i], (*hooks)[i+1:]...)
			return nil
		}
	}
	return fmt.Errorf("hook with ID %s not found in event %s", id, eventName)
}

// GetHooksByEvent 按事件类型名获取钩子列表
func (m *Manager) GetHooksByEvent(eventName string) ([]Hook, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	hooks := m.hooksByEventName(eventName)
	if hooks == nil {
		return nil, fmt.Errorf("unknown event type: %s", eventName)
	}
	return *hooks, nil
}

// hooksByEventName 将字符串事件名映射到 HookConfig 中的切片指针
func (m *Manager) hooksByEventName(name string) *[]Hook {
	switch name {
	case "PreToolUse":
		return &m.config.PreToolUse
	case "PostToolUse":
		return &m.config.PostToolUse
	case "PreRead":
		return &m.config.PreRead
	case "PostRead":
		return &m.config.PostRead
	case "PreWrite":
		return &m.config.PreWrite
	case "PostWrite":
		return &m.config.PostWrite
	case "PreEdit":
		return &m.config.PreEdit
	case "PostEdit":
		return &m.config.PostEdit
	case "PreBash":
		return &m.config.PreBash
	case "PostBash":
		return &m.config.PostBash
	case "PreSearch":
		return &m.config.PreSearch
	case "PostSearch":
		return &m.config.PostSearch
	case "PreGit":
		return &m.config.PreGit
	case "PostGit":
		return &m.config.PostGit
	case "PreWeb":
		return &m.config.PreWeb
	case "PostWeb":
		return &m.config.PostWeb
	case "PreAgent":
		return &m.config.PreAgent
	case "PostAgent":
		return &m.config.PostAgent
	case "SessionStart":
		return &m.config.SessionStart
	case "SessionStop":
		return &m.config.SessionStop
	case "Stop":
		return &m.config.Stop
	case "PostCompact":
		return &m.config.PostCompact
	case "Notification":
		return &m.config.Notification
	default:
		return nil
	}
}

// sortHooksByEvent 按优先级排序指定事件的钩子列表
func (m *Manager) sortHooksByEvent(eventName string) {
	hooks := m.hooksByEventName(eventName)
	if hooks == nil || len(*hooks) <= 1 {
		return
	}
	for i := 0; i < len(*hooks)-1; i++ {
		for j := i + 1; j < len(*hooks); j++ {
			if (*hooks)[i].Base().Priority > (*hooks)[j].Base().Priority {
				(*hooks)[i], (*hooks)[j] = (*hooks)[j], (*hooks)[i]
			}
		}
	}
}

// MatchPreHook 匹配 pre-hook，返回克隆以避免竞态
func (m *Manager) MatchPreHook(toolName, filePath string) Hook {
	m.mu.RLock()
	defer m.mu.RUnlock()
	h := matchHook(m.config.PreToolUse, toolName, filePath)
	if h == nil {
		return nil
	}
	return h.Clone()
}

// MatchPostHook 匹配 post-hook，返回克隆以避免竞态
func (m *Manager) MatchPostHook(toolName, filePath string) Hook {
	m.mu.RLock()
	defer m.mu.RUnlock()
	h := matchHook(m.config.PostToolUse, toolName, filePath)
	if h == nil {
		return nil
	}
	return h.Clone()
}

// MatchStopHook 匹配 stop hook，返回克隆以避免竞态
func (m *Manager) MatchStopHook(toolName, filePath string) Hook {
	m.mu.RLock()
	defer m.mu.RUnlock()
	h := matchHook(m.config.Stop, toolName, filePath)
	if h == nil {
		return nil
	}
	return h.Clone()
}
