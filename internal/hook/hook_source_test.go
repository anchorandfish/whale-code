package hook

import (
	"testing"
)

// TestHookSource_Constants 测试钩子origin常量
func TestHookSource_Constants(t *testing.T) {
	if SourceBuiltin != "builtin" {
		t.Errorf("SourceBuiltin = %q, want %q", SourceBuiltin, "builtin")
	}
	if SourcePlugin != "plugin" {
		t.Errorf("SourcePlugin = %q, want %q", SourcePlugin, "plugin")
	}
	if SourceConfig != "config" {
		t.Errorf("SourceConfig = %q, want %q", SourceConfig, "config")
	}
}

// TestHook_SourceField 测试 Hook 的 Source 字段
func TestHook_SourceField(t *testing.T) {
	h := &HookCommand{
		HookBase: HookBase{
			Matcher: "Read",
			Source:  SourcePlugin,
			Action:  ActionAllow,
		},
		Command: "echo test",
	}
	if h.Base().Source != SourcePlugin {
		t.Errorf("Hook.Source = %q, want %q", h.Base().Source, SourcePlugin)
	}
}

// TestRemoveAllPluginHooks 测试移除所有插件钩子
func TestRemoveAllPluginHooks(t *testing.T) {
	mgr := NewManager("")

	// 通过 SetConfig 注册不同origin的钩子
	mgr.SetConfig(HookConfig{
		PreToolUse: []Hook{
			&HookCommand{HookBase: HookBase{Matcher: "Read", Source: SourceBuiltin, Action: ActionAllow}, Command: "builtin_hook"},
			&HookCommand{HookBase: HookBase{Matcher: "Read", Source: SourcePlugin, Action: ActionAllow}, Command: "plugin_hook1"},
			&HookCommand{HookBase: HookBase{Matcher: "Write", Source: SourceConfig, Action: ActionAllow}, Command: "config_hook"},
		},
		PostToolUse: []Hook{
			&HookCommand{HookBase: HookBase{Matcher: "Read", Source: SourcePlugin, Action: ActionAllow}, Command: "plugin_hook2"},
		},
	})

	// 移除所有插件钩子
	mgr.RemoveAllPluginHooks()

	// 验证 PreToolUse 中只剩 builtin 和 config 钩子
	cfg := mgr.Config()
	for _, h := range cfg.PreToolUse {
		if h.Base().Source == SourcePlugin {
			t.Errorf("Plugin hook should be removed from PreToolUse: %s", h.(*HookCommand).Command)
		}
	}

	// 验证 PostToolUse 中没有插件钩子
	for _, h := range cfg.PostToolUse {
		if h.Base().Source == SourcePlugin {
			t.Errorf("Plugin hook should be removed from PostToolUse: %s", h.(*HookCommand).Command)
		}
	}

	// 验证 builtin 和 config 钩子仍然存在
	var foundBuiltin, foundConfig bool
	for _, h := range cfg.PreToolUse {
		if h.Base().Source == SourceBuiltin {
			foundBuiltin = true
		}
		if h.Base().Source == SourceConfig {
			foundConfig = true
		}
	}
	if !foundBuiltin {
		t.Error("Builtin hook should not be removed")
	}
	if !foundConfig {
		t.Error("Config hook should not be removed")
	}
}

// TestRemoveAllPluginHooks_Empty 测试空钩子列表时移除不会 panic
func TestRemoveAllPluginHooks_Empty(t *testing.T) {
	mgr := NewManager("")
	// Should not panic
	mgr.RemoveAllPluginHooks()
}

// TestFilterHooksBySource 测试按origin过滤钩子
func TestFilterHooksBySource(t *testing.T) {
	hooks := []Hook{
		&HookCommand{HookBase: HookBase{Matcher: "Read", Source: SourceBuiltin}, Command: "b1"},
		&HookCommand{HookBase: HookBase{Matcher: "Read", Source: SourcePlugin}, Command: "p1"},
		&HookCommand{HookBase: HookBase{Matcher: "Read", Source: SourceConfig}, Command: "c1"},
		&HookCommand{HookBase: HookBase{Matcher: "Write", Source: SourcePlugin}, Command: "p2"},
	}

	// keep=true: 保留匹配 source 的钩子
	filtered := filterHooksBySource(hooks, SourcePlugin, true)
	if len(filtered) != 2 {
		t.Errorf("Expected 2 plugin hooks when keep=true, got %d", len(filtered))
	}

	// keep=false: 移除匹配 source 的钩子
	filtered = filterHooksBySource(hooks, SourcePlugin, false)
	if len(filtered) != 2 {
		t.Errorf("Expected 2 non-plugin hooks when keep=false, got %d", len(filtered))
	}

	// keep=true: 保留 builtin 钩子
	filtered = filterHooksBySource(hooks, SourceBuiltin, true)
	if len(filtered) != 1 {
		t.Errorf("Expected 1 builtin hook when keep=true, got %d", len(filtered))
	}
}

// TestRemoveAllPluginHooks_AllEvents 测试所有事件类型的插件钩子都被移除
func TestRemoveAllPluginHooks_AllEvents(t *testing.T) {
	mgr := NewManager("")

	pluginHook := &HookCommand{HookBase: HookBase{Matcher: "*", Source: SourcePlugin, Action: ActionAllow}, Command: "plugin_cmd"}
	builtinHook := &HookCommand{HookBase: HookBase{Matcher: "*", Source: SourceBuiltin, Action: ActionAllow}, Command: "builtin_cmd"}

	mgr.SetConfig(HookConfig{
		PreToolUse:   []Hook{pluginHook, builtinHook},
		PostToolUse:  []Hook{pluginHook},
		PreRead:      []Hook{pluginHook},
		PostRead:     []Hook{pluginHook},
		PreWrite:     []Hook{pluginHook},
		PostWrite:    []Hook{pluginHook},
		PreEdit:      []Hook{pluginHook},
		PostEdit:     []Hook{pluginHook},
		PreBash:      []Hook{pluginHook},
		PostBash:     []Hook{pluginHook},
		PreSearch:    []Hook{pluginHook},
		PostSearch:   []Hook{pluginHook},
		PreGit:       []Hook{pluginHook},
		PostGit:      []Hook{pluginHook},
		PreWeb:       []Hook{pluginHook},
		PostWeb:      []Hook{pluginHook},
		PreAgent:     []Hook{pluginHook},
		PostAgent:    []Hook{pluginHook},
		SessionStart: []Hook{pluginHook},
		SessionStop:  []Hook{pluginHook},
		Stop:         []Hook{pluginHook},
		PostCompact:  []Hook{pluginHook},
		Notification: []Hook{pluginHook},
	})

	mgr.RemoveAllPluginHooks()

	cfg := mgr.Config()

	// checkNoPlugin 检查指定事件列表中不应存在插件来源的钩子
	checkNoPlugin := func(t *testing.T, name string, hooks []Hook) {
		t.Helper()
		for _, h := range hooks {
			if h.Base().Source == SourcePlugin {
				t.Errorf("%s: plugin hook should be removed: %s", name, h.(*HookCommand).Command)
			}
		}
	}

	checkNoPlugin(t, "PreToolUse", cfg.PreToolUse)
	checkNoPlugin(t, "PostToolUse", cfg.PostToolUse)
	checkNoPlugin(t, "PreRead", cfg.PreRead)
	checkNoPlugin(t, "PostRead", cfg.PostRead)
	checkNoPlugin(t, "PreWrite", cfg.PreWrite)
	checkNoPlugin(t, "PostWrite", cfg.PostWrite)
	checkNoPlugin(t, "PreEdit", cfg.PreEdit)
	checkNoPlugin(t, "PostEdit", cfg.PostEdit)
	checkNoPlugin(t, "PreBash", cfg.PreBash)
	checkNoPlugin(t, "PostBash", cfg.PostBash)
	checkNoPlugin(t, "PreSearch", cfg.PreSearch)
	checkNoPlugin(t, "PostSearch", cfg.PostSearch)
	checkNoPlugin(t, "PreGit", cfg.PreGit)
	checkNoPlugin(t, "PostGit", cfg.PostGit)
	checkNoPlugin(t, "PreWeb", cfg.PreWeb)
	checkNoPlugin(t, "PostWeb", cfg.PostWeb)
	checkNoPlugin(t, "PreAgent", cfg.PreAgent)
	checkNoPlugin(t, "PostAgent", cfg.PostAgent)
	checkNoPlugin(t, "SessionStart", cfg.SessionStart)
	checkNoPlugin(t, "SessionStop", cfg.SessionStop)
	checkNoPlugin(t, "Stop", cfg.Stop)
	checkNoPlugin(t, "PostCompact", cfg.PostCompact)
	checkNoPlugin(t, "Notification", cfg.Notification)

	// 验证 builtin 钩子保留
	if len(cfg.PreToolUse) != 1 || cfg.PreToolUse[0].Base().Source != SourceBuiltin {
		t.Error("Builtin hook in PreToolUse should be preserved")
	}
}
