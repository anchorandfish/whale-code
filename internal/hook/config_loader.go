package hook

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"

	"gopkg.in/yaml.v3"
)

// UnmarshalJSON 自定义反序列化：HookConfig 的 []Hook 字段是接口切片，
// 需要先解析为 hookRaw 中间结构，再根据 Type 分发到具体类型
func (c *HookConfig) UnmarshalJSON(data []byte) error {
	type rawConfig struct {
		PreToolUse   []hookRaw `json:"pre_tool_use"`
		PostToolUse  []hookRaw `json:"post_tool_use"`
		PreRead      []hookRaw `json:"pre_read"`
		PostRead     []hookRaw `json:"post_read"`
		PreWrite     []hookRaw `json:"pre_write"`
		PostWrite    []hookRaw `json:"post_write"`
		PreEdit      []hookRaw `json:"pre_edit"`
		PostEdit     []hookRaw `json:"post_edit"`
		PreBash      []hookRaw `json:"pre_bash"`
		PostBash     []hookRaw `json:"post_bash"`
		PreSearch    []hookRaw `json:"pre_search"`
		PostSearch   []hookRaw `json:"post_search"`
		PreGit       []hookRaw `json:"pre_git"`
		PostGit      []hookRaw `json:"post_git"`
		PreWeb       []hookRaw `json:"pre_web"`
		PostWeb      []hookRaw `json:"post_web"`
		PreAgent     []hookRaw `json:"pre_agent"`
		PostAgent    []hookRaw `json:"post_agent"`
		SessionStart []hookRaw `json:"session_start"`
		SessionStop  []hookRaw `json:"session_stop"`
		Stop         []hookRaw `json:"stop"`
		PostCompact  []hookRaw `json:"post_compact"`
		Notification []hookRaw `json:"notification"`
	}
	var rc rawConfig
	if err := json.Unmarshal(data, &rc); err != nil {
		return err
	}
	c.PreToolUse = rawsToHooks(rc.PreToolUse)
	c.PostToolUse = rawsToHooks(rc.PostToolUse)
	c.PreRead = rawsToHooks(rc.PreRead)
	c.PostRead = rawsToHooks(rc.PostRead)
	c.PreWrite = rawsToHooks(rc.PreWrite)
	c.PostWrite = rawsToHooks(rc.PostWrite)
	c.PreEdit = rawsToHooks(rc.PreEdit)
	c.PostEdit = rawsToHooks(rc.PostEdit)
	c.PreBash = rawsToHooks(rc.PreBash)
	c.PostBash = rawsToHooks(rc.PostBash)
	c.PreSearch = rawsToHooks(rc.PreSearch)
	c.PostSearch = rawsToHooks(rc.PostSearch)
	c.PreGit = rawsToHooks(rc.PreGit)
	c.PostGit = rawsToHooks(rc.PostGit)
	c.PreWeb = rawsToHooks(rc.PreWeb)
	c.PostWeb = rawsToHooks(rc.PostWeb)
	c.PreAgent = rawsToHooks(rc.PreAgent)
	c.PostAgent = rawsToHooks(rc.PostAgent)
	c.SessionStart = rawsToHooks(rc.SessionStart)
	c.SessionStop = rawsToHooks(rc.SessionStop)
	c.Stop = rawsToHooks(rc.Stop)
	c.PostCompact = rawsToHooks(rc.PostCompact)
	c.Notification = rawsToHooks(rc.Notification)
	return nil
}

// UnmarshalYAML 实现 yaml.Unmarshaler 接口（SNAPSHOT）。
//
// 子审查优化 2 吸纳：用 yaml.Node 中间结构（type rawConfig + yaml tag），
// 复用 rawsToHooks + toHook() Type 分发，与 UnmarshalJSON 路径一致。
// 支持 YAML 1.2 完整语法：block scalar / anchor / flow / multi-doc。
//
// 官方源：https://pkg.go.dev/gopkg.in/yaml.v3 @2026-07-21
func (c *HookConfig) UnmarshalYAML(value *yaml.Node) error {
	type rawConfig struct {
		PreToolUse   []hookRaw `yaml:"pre_tool_use"`
		PostToolUse  []hookRaw `yaml:"post_tool_use"`
		PreRead      []hookRaw `yaml:"pre_read"`
		PostRead     []hookRaw `yaml:"post_read"`
		PreWrite     []hookRaw `yaml:"pre_write"`
		PostWrite    []hookRaw `yaml:"post_write"`
		PreEdit      []hookRaw `yaml:"pre_edit"`
		PostEdit     []hookRaw `yaml:"post_edit"`
		PreBash      []hookRaw `yaml:"pre_bash"`
		PostBash     []hookRaw `yaml:"post_bash"`
		PreSearch    []hookRaw `yaml:"pre_search"`
		PostSearch   []hookRaw `yaml:"post_search"`
		PreGit       []hookRaw `yaml:"pre_git"`
		PostGit      []hookRaw `yaml:"post_git"`
		PreWeb       []hookRaw `yaml:"pre_web"`
		PostWeb      []hookRaw `yaml:"post_web"`
		PreAgent     []hookRaw `yaml:"pre_agent"`
		PostAgent    []hookRaw `yaml:"post_agent"`
		SessionStart []hookRaw `yaml:"session_start"`
		SessionStop  []hookRaw `yaml:"session_stop"`
		Stop         []hookRaw `yaml:"stop"`
		PostCompact  []hookRaw `yaml:"post_compact"`
		Notification []hookRaw `yaml:"notification"`
	}
	var rc rawConfig
	if err := value.Decode(&rc); err != nil {
		return err
	}
	c.PreToolUse = rawsToHooks(rc.PreToolUse)
	c.PostToolUse = rawsToHooks(rc.PostToolUse)
	c.PreRead = rawsToHooks(rc.PreRead)
	c.PostRead = rawsToHooks(rc.PostRead)
	c.PreWrite = rawsToHooks(rc.PreWrite)
	c.PostWrite = rawsToHooks(rc.PostWrite)
	c.PreEdit = rawsToHooks(rc.PreEdit)
	c.PostEdit = rawsToHooks(rc.PostEdit)
	c.PreBash = rawsToHooks(rc.PreBash)
	c.PostBash = rawsToHooks(rc.PostBash)
	c.PreSearch = rawsToHooks(rc.PreSearch)
	c.PostSearch = rawsToHooks(rc.PostSearch)
	c.PreGit = rawsToHooks(rc.PreGit)
	c.PostGit = rawsToHooks(rc.PostGit)
	c.PreWeb = rawsToHooks(rc.PreWeb)
	c.PostWeb = rawsToHooks(rc.PostWeb)
	c.PreAgent = rawsToHooks(rc.PreAgent)
	c.PostAgent = rawsToHooks(rc.PostAgent)
	c.SessionStart = rawsToHooks(rc.SessionStart)
	c.SessionStop = rawsToHooks(rc.SessionStop)
	c.Stop = rawsToHooks(rc.Stop)
	c.PostCompact = rawsToHooks(rc.PostCompact)
	c.Notification = rawsToHooks(rc.Notification)
	return nil
}

// rawsToHooks 将 hookRaw 切片转换为 Hook 接口切片
func rawsToHooks(raws []hookRaw) []Hook {
	if len(raws) == 0 {
		return nil
	}
	hooks := make([]Hook, 0, len(raws))
	for _, r := range raws {
		hooks = append(hooks, r.toHook())
	}
	return hooks
}

// LoadConfigFromBytes 从字节加载钩子配置
// JSON 优先（向后兼容 hooks.json），失败 fallback 到 yaml.v3（YAML 1.2 完整支持， SNAPSHOT）
func (m *Manager) LoadConfigFromBytes(data []byte) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	// 1. JSON 优先（向后兼容 hooks.json）
	if err := json.Unmarshal(data, &m.config); err == nil {
		return nil
	}
	// 2. JSON 失败，尝试 yaml.v3（YAML 1.2 完整支持）
	if err := yaml.Unmarshal(data, &m.config); err != nil {
		return fmt.Errorf("failed to parse hook config (json+yaml.v3): %w", err)
	}
	return nil
}

// LoadConfig 从文件加载钩子配置（追加合并模式）
// 同事件列表追加到现有列表末尾
//
// SNAPSHOT：JSON 优先（向后兼容 hooks.json），失败 fallback 到 yaml.v3。
// 子审查优化 5 吸纳：错误信息含配置文件路径。
func (m *Manager) LoadConfig(p string) error {
	data, err := os.ReadFile(p)
	if err != nil {
		return fmt.Errorf("failed to read hook config %s: %w", p, err)
	}

	var newConfig HookConfig
	// 1. JSON 优先（向后兼容 hooks.json）
	if err := json.Unmarshal(data, &newConfig); err == nil {
		m.mu.Lock()
		m.mergeConfig(newConfig)
		m.mu.Unlock()
		return nil
	}
	// 2. JSON 失败，尝试 yaml.v3（YAML 1.2 完整支持）
	if err := yaml.Unmarshal(data, &newConfig); err != nil {
		// 子审查优化 5：错误信息含路径 + yaml.v3 错误（带行号）
		return fmt.Errorf("failed to parse hook config %s (json+yaml.v3): %w", p, err)
	}

	m.mu.Lock()
	m.mergeConfig(newConfig)
	m.mu.Unlock()
	return nil
}

// mergeConfig 合并配置（追加模式）
func (m *Manager) mergeConfig(newCfg HookConfig) {
	m.config.PreToolUse = append(m.config.PreToolUse, newCfg.PreToolUse...)
	m.config.PostToolUse = append(m.config.PostToolUse, newCfg.PostToolUse...)
	m.config.PreRead = append(m.config.PreRead, newCfg.PreRead...)
	m.config.PostRead = append(m.config.PostRead, newCfg.PostRead...)
	m.config.PreWrite = append(m.config.PreWrite, newCfg.PreWrite...)
	m.config.PostWrite = append(m.config.PostWrite, newCfg.PostWrite...)
	m.config.PreEdit = append(m.config.PreEdit, newCfg.PreEdit...)
	m.config.PostEdit = append(m.config.PostEdit, newCfg.PostEdit...)
	m.config.PreBash = append(m.config.PreBash, newCfg.PreBash...)
	m.config.PostBash = append(m.config.PostBash, newCfg.PostBash...)
	m.config.PreSearch = append(m.config.PreSearch, newCfg.PreSearch...)
	m.config.PostSearch = append(m.config.PostSearch, newCfg.PostSearch...)
	m.config.PreGit = append(m.config.PreGit, newCfg.PreGit...)
	m.config.PostGit = append(m.config.PostGit, newCfg.PostGit...)
	m.config.PreWeb = append(m.config.PreWeb, newCfg.PreWeb...)
	m.config.PostWeb = append(m.config.PostWeb, newCfg.PostWeb...)
	m.config.PreAgent = append(m.config.PreAgent, newCfg.PreAgent...)
	m.config.PostAgent = append(m.config.PostAgent, newCfg.PostAgent...)
	m.config.SessionStart = append(m.config.SessionStart, newCfg.SessionStart...)
	m.config.SessionStop = append(m.config.SessionStop, newCfg.SessionStop...)
	m.config.Stop = append(m.config.Stop, newCfg.Stop...)
	m.config.PostCompact = append(m.config.PostCompact, newCfg.PostCompact...)
	m.config.Notification = append(m.config.Notification, newCfg.Notification...)
}

// SetConfig 设置钩子配置
func (m *Manager) SetConfig(cfg HookConfig) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.config = cfg
	allEvents := []string{
		"PreToolUse", "PostToolUse", "PreRead", "PostRead",
		"PreWrite", "PostWrite", "PreEdit", "PostEdit",
		"PreBash", "PostBash", "PreSearch", "PostSearch",
		"PreGit", "PostGit", "PreWeb", "PostWeb",
		"PreAgent", "PostAgent",
		"SessionStart", "SessionStop", "Stop", "PostCompact", "Notification",
	}
	for _, event := range allEvents {
		m.sortHooksByEvent(event)
	}
}

// Config 获取当前钩子配置
func (m *Manager) Config() HookConfig {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.config
}

// Reload 实现 hook.Reloadable 接口，全量重新加载配置文件（C14 复用承诺）
// 使 *Manager 可被 HotReloader 通过 Reloadable 接口复用（替代原 reloadNow 直接调用 SetConfig）
// 行为等价于原 reloadNow：读文件 + parseConfigBytes + SetConfig（全量替换，非追加合并）
// 注意：与 LoadConfig 不同——LoadConfig 是追加合并模式，Reload 是全量替换模式（保留原 reloadNow 语义）
// 失败时保留旧配置 + 返回错误（由 HotReloader 记录 lastErr）
func (m *Manager) Reload(path string) error {
	data, err := os.ReadFile(path)
	if err != nil {
		return fmt.Errorf("read hook config %s: %w", path, err)
	}
	newCfg, err := parseConfigBytes(data)
	if err != nil {
		return fmt.Errorf("parse hook config %s: %w", path, err)
	}
	m.SetConfig(newCfg)
	return nil
}

// SaveConfig 持久化钩子配置到文件
// 对标同类 CLI hooks config 持久化，自动写入 .whale/hooks.json
func (m *Manager) SaveConfig(p string) error {
	m.mu.RLock()
	data, err := json.MarshalIndent(m.config, "", "  ")
	m.mu.RUnlock()
	if err != nil {
		return fmt.Errorf("failed to serialize hook config: %w", err)
	}
	dir := filepath.Dir(p)
	if err := os.MkdirAll(dir, 0755); err != nil {
		return fmt.Errorf("failed to create config directory: %w", err)
	}
	if err := os.WriteFile(p, data, 0600); err != nil {
		return fmt.Errorf("failed to write hook config: %w", err)
	}
	return nil
}
