package command

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/anchorandfish/whale-code/internal/hook"
)

func TestHooksList_Empty(t *testing.T) {
	m := hook.NewManager(t.TempDir())
	handler := NewHooksCommandHandler(m, "")

	result := handler("list")
	if !strings.Contains(result, "no hook") && !strings.Contains(result, "empty") {
		t.Errorf("empty list should show no hook, actual: %s", result)
	}
	if !strings.Contains(result, "Total 0 hook") {
		t.Errorf("should show 0 hooks, actual: %s", result)
	}
}

func TestHooksList_WithHooks(t *testing.T) {
	m := hook.NewManager("")
	m.SetConfig(hook.HookConfig{
		PreToolUse:  []hook.Hook{&hook.HookCommand{HookBase: hook.HookBase{Matcher: "bash", Action: hook.ActionBlock, Reason: "no bash"}}},
		PostToolUse: []hook.Hook{&hook.HookCommand{HookBase: hook.HookBase{Matcher: "*", Action: hook.ActionAllow}, Command: "echo_done"}},
		Stop:        []hook.Hook{&hook.HookCommand{HookBase: hook.HookBase{Matcher: "*", Action: hook.ActionAllow}, Command: "echo_stopped"}},
	})
	handler := NewHooksCommandHandler(m, "")

	result := handler("list")
	checks := []string{"PreToolUse", "PostToolUse", "Stop", "bash", "block", "no bash", "echo_done", "echo_stopped"}
	for _, check := range checks {
		if !strings.Contains(result, check) {
			t.Errorf("输出should contain %q，actual: %s", check, result)
		}
	}
	if !strings.Contains(result, "Total 3 hook") {
		t.Errorf("should show 3 hooks")
	}
}

func TestHooksAdd_PreToolUse(t *testing.T) {
	tmpDir := t.TempDir()
	m := hook.NewManager(tmpDir)
	handler := NewHooksCommandHandler(m, tmpDir)

	if len(m.Config().PreToolUse) != 0 {
		t.Fatal("初始should be empty")
	}

	result := handler("add PreToolUse command block matcher=write_file reason=no_writes")
	if !strings.Contains(result, "Added") {
		t.Errorf("should return success message, actual: %s", result)
	}

	cfg := m.Config()
	if len(cfg.PreToolUse) != 1 {
		t.Fatalf("should have 1 hook, actual: %d", len(cfg.PreToolUse))
	}
	h := cfg.PreToolUse[0]
	if h.Base().Matcher != "write_file" {
		t.Errorf("matcher: got %q, want write_file", h.Base().Matcher)
	}
	if h.Base().Action != hook.ActionBlock {
		t.Errorf("action: got %q, want block", h.Base().Action)
	}
	if h.Base().Reason != "no_writes" {
		t.Errorf("reason: got %q, want no_writes", h.Base().Reason)
	}
}

func TestHooksAdd_PostToolUse(t *testing.T) {
	tmpDir := t.TempDir()
	m := hook.NewManager(tmpDir)
	handler := NewHooksCommandHandler(m, tmpDir)

	result := handler("add PostToolUse command allow matcher=* command=echo_done")
	if !strings.Contains(result, "Added") {
		t.Errorf("should return success message, actual: %s", result)
	}

	cfg := m.Config()
	if len(cfg.PostToolUse) != 1 {
		t.Error("PostToolUse hook not added correctly")
	} else {
		cmd, _ := cfg.PostToolUse[0].(*hook.HookCommand)
		if cmd == nil || cmd.Command != "echo_done" {
			t.Error("PostToolUse hook not added correctly")
		}
	}
}

func TestHooksAdd_WithOptions(t *testing.T) {
	tmpDir := t.TempDir()
	m := hook.NewManager(tmpDir)
	handler := NewHooksCommandHandler(m, tmpDir)

	handler("add PreToolUse command ask matcher=bash pattern=*.sh reason=confirm timeout=5000")

	h := m.Config().PreToolUse[0]
	if h.Base().Matcher != "bash" {
		t.Errorf("matcher: got %q, want bash", h.Base().Matcher)
	}
	if h.Base().Pattern != "*.sh" {
		t.Errorf("pattern: got %q, want *.sh", h.Base().Pattern)
	}
	if h.Base().Action != hook.ActionAsk {
		t.Errorf("action: got %q, want ask", h.Base().Action)
	}
	if h.Base().Reason != "confirm" {
		t.Errorf("reason: got %q, want confirm", h.Base().Reason)
	}
	if h.Base().Timeout != 5000 {
		t.Errorf("timeout: got %d, want 5000", h.Base().Timeout)
	}
}

func TestHooksAdd_InvalidEvent(t *testing.T) {
	m := hook.NewManager("")
	handler := NewHooksCommandHandler(m, "")

	result := handler("add InvalidEvent command block matcher=*")
	if !strings.Contains(result, "Error") && !strings.Contains(result, "unknown") {
		t.Errorf("invalid event should return error, actual: %s", result)
	}
}

func TestHooksAdd_InvalidType(t *testing.T) {
	m := hook.NewManager("")
	handler := NewHooksCommandHandler(m, "")

	result := handler("add PreToolUse invalidtype block matcher=*")
	if !strings.Contains(result, "Error") && !strings.Contains(result, "unknown") {
		t.Errorf("invalid hook type should return error, actual: %s", result)
	}
}

func TestHooksRemove_ByIndex(t *testing.T) {
	tmpDir := t.TempDir()
	m := hook.NewManager(tmpDir)
	m.SetConfig(hook.HookConfig{
		PreToolUse: []hook.Hook{
			&hook.HookCommand{HookBase: hook.HookBase{ID: "hook_a1b2c3d4", Matcher: "bash"}},
			&hook.HookCommand{HookBase: hook.HookBase{ID: "hook_e5f6g7h8", Matcher: "write_file"}},
		},
	})
	handler := NewHooksCommandHandler(m, tmpDir)

	result := handler("remove PreToolUse hook_e5f6g7h8")
	if !strings.Contains(result, "Removed") {
		t.Errorf("should return success message, actual: %s", result)
	}

	cfg := m.Config()
	if len(cfg.PreToolUse) != 1 || cfg.PreToolUse[0].Base().Matcher != "bash" {
		t.Error("remaining hooks incorrect after removal")
	}
}

func TestHooksRemove_InvalidIndex(t *testing.T) {
	m := hook.NewManager("")
	handler := NewHooksCommandHandler(m, "")

	result := handler("remove PreToolUse 99")
	if !strings.Contains(result, "Failed") {
		t.Errorf("invalid index should return error, actual: %s", result)
	}
}

func TestHooksRemove_InvalidEvent(t *testing.T) {
	m := hook.NewManager("")
	handler := NewHooksCommandHandler(m, "")

	result := handler("remove InvalidEvent 0")
	if !strings.Contains(result, "Failed") {
		t.Errorf("invalid event type should return error, actual: %s", result)
	}
}

func TestHooksAdd_PersistConfig(t *testing.T) {
	tmpDir := t.TempDir()
	hooksDir := filepath.Join(tmpDir, ".whale")
	os.MkdirAll(hooksDir, 0755)

	m := hook.NewManager(tmpDir)
	handler := NewHooksCommandHandler(m, tmpDir)

	handler("add PreToolUse command block matcher=* reason=test_persistence")

	hooksPath := filepath.Join(hooksDir, "hooks.json")
	data, err := os.ReadFile(hooksPath)
	if err != nil {
		t.Fatalf("config file not persisted: %v", err)
	}

	var cfg hook.HookConfig
	json.Unmarshal(data, &cfg)
	if len(cfg.PreToolUse) != 1 || cfg.PreToolUse[0].Base().Reason != "test_persistence" {
		t.Error("persisted config content incorrect")
	}
}

func TestHooksRemove_PersistConfig(t *testing.T) {
	tmpDir := t.TempDir()
	hooksDir := filepath.Join(tmpDir, ".whale")
	os.MkdirAll(hooksDir, 0755)

	m := hook.NewManager(tmpDir)
	m.SetConfig(hook.HookConfig{
		PreToolUse: []hook.Hook{
			&hook.HookCommand{HookBase: hook.HookBase{ID: "hook_abc12345", Matcher: "bash", Action: hook.ActionBlock}},
			&hook.HookCommand{HookBase: hook.HookBase{ID: "hook_def67890", Matcher: "write_file", Action: hook.ActionBlock}},
		},
	})
	handler := NewHooksCommandHandler(m, tmpDir)

	handler("remove PreToolUse hook_abc12345")

	hooksPath := filepath.Join(hooksDir, "hooks.json")
	data, err := os.ReadFile(hooksPath)
	if err != nil {
		t.Fatalf("config file not persisted: %v", err)
	}

	var cfg hook.HookConfig
	json.Unmarshal(data, &cfg)
	if len(cfg.PreToolUse) != 1 || cfg.PreToolUse[0].Base().Matcher != "write_file" {
		t.Errorf("persisted content incorrect after removal, remaining: %d", len(cfg.PreToolUse))
	}
}

func TestHooksAdd_BashEvent(t *testing.T) {
	tmpDir := t.TempDir()
	m := hook.NewManager(tmpDir)
	handler := NewHooksCommandHandler(m, tmpDir)

	handler("add PreBash command block matcher=bash reason=bash_disabled")
	cfg := m.Config()
	if len(cfg.PreBash) != 1 || cfg.PreBash[0].Base().Matcher != "bash" {
		t.Error("PreBash hook not added correctly")
	}

	handler("add PostBash command allow matcher=bash command=echo_post")
	cfg = m.Config()
	if len(cfg.PostBash) != 1 {
		t.Error("PostBash hook not added correctly")
	} else {
		cmd, _ := cfg.PostBash[0].(*hook.HookCommand)
		if cmd == nil || cmd.Command != "echo_post" {
			t.Error("PostBash hook not added correctly")
		}
	}
}

func TestHooksAdd_StopEvent(t *testing.T) {
	tmpDir := t.TempDir()
	m := hook.NewManager(tmpDir)
	handler := NewHooksCommandHandler(m, tmpDir)

	handler("add Stop command allow matcher=* command=echo_quitting")

	cfg := m.Config()
	if len(cfg.Stop) != 1 {
		t.Error("Stop hook not added correctly")
	} else {
		cmd, _ := cfg.Stop[0].(*hook.HookCommand)
		if cmd == nil || cmd.Command != "echo_quitting" {
			t.Error("Stop hook not added correctly")
		}
	}
}

func TestHooksHelp_Format(t *testing.T) {
	m := hook.NewManager("")
	handler := NewHooksCommandHandler(m, "")

	result := handler("")
	checks := []string{"list", "add", "remove", "PreToolUse", "PostToolUse"}
	for _, check := range checks {
		if !strings.Contains(result, check) {
			t.Errorf("help info should contain %q, actual: %s", check, result)
		}
	}
}
