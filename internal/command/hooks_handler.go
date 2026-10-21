package command

import (
	"fmt"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/anchorandfish/whale-code/internal/hook"
)

// NewHooksCommandHandler creates the /hooks command handler
// Connects to hook.Manager, implements list/add/remove functionality
// workDir is used to persist config file to .whale/hooks.json
func NewHooksCommandHandler(m *hook.Manager, workDir string) func(args string) string {
	return func(args string) string {
		parts := splitHooksArgs(args)
		if len(parts) == 0 || parts[0] == "help" {
			return formatHooksHelp()
		}

		switch parts[0] {
		case "list":
			return formatHooksList(m)
		case "add":
			return handleHooksAdd(m, workDir, parts[1:])
		case "remove":
			return handleHooksRemove(m, workDir, parts[1:])
		// PR-A 信任门（Q1 拍板：对齐业内既有实现 startup_hooks_review —— 先 review 再 trust，
		// 未信任的 hook 静默跳过、根本不运行）。
		case "review":
			return handleHooksReview(m)
		case "trust":
			return handleHooksTrust(m, parts[1:])
		case "untrust":
			return handleHooksUntrust(m, parts[1:])
		// PR-A2（② 拍板：运行信任 ≠ 改写授权）。改写工具输入/输出是强能力，默认关。
		case "allow-rewrite":
			return handleHooksAllowRewrite(m, parts[1:])
		case "deny-rewrite":
			return handleHooksDenyRewrite(m, parts[1:])
		default:
			return fmt.Sprintf("Unknown subcommand: %s\n%s", parts[0], formatHooksHelp())
		}
	}
}

// splitHooksArgs splits a command string into tokens, respecting quoted strings
// e.g., reason="block all writes" is treated as a single token
// M09-05 改造：委托到 ShellSplitOrFields，统一使用 POSIX shell 引用规则，
// 支持单/双引号、反斜杠转义、行续接；解析失败时降级到 strings.Fields
// 注：原 splitArgs 与 alias.go:191 重名， 修复期重命名为 splitHooksArgs
func splitHooksArgs(s string) []string {
	return ShellSplitOrFields(s)
}

// formatHooksHelp returns the /hooks command help info
func formatHooksHelp() string {
	return `Hook management commands:
  /hooks list                          List all configured hooks
  /hooks add <event> <type> <action> [options]  Add hook
  /hooks remove <event> <id>           Remove specified hook by ID
  /hooks review                        List hooks pending trust review
  /hooks trust <id>|all                Trust a hook (or all pending) so it can run
  /hooks untrust <id>|all              Revoke trust; hook stops running
  /hooks allow-rewrite <id>|all        Grant the rewrite capability (updatedInput / Modified); OFF by default
  /hooks deny-rewrite <id>|all         Revoke the rewrite capability (trust to run is unaffected)

Available event types (23):
  Tool events: PreToolUse, PostToolUse, PreRead, PostRead,
           PreWrite, PostWrite, PreEdit, PostEdit,
           PreBash, PostBash, PreSearch, PostSearch,
           PreGit, PostGit, PreWeb, PostWeb,
           PreAgent, PostAgent
  Lifecycle: SessionStart, SessionStop, Stop, PostCompact, Notification

Hook types: command, prompt, agent, http, callback
Actions: allow, block, ask, warn
Options: matcher=<glob> command=<cmd> pattern=<glob> reason=<text> timeout=<ms> enabled=true|false priority=<num>

Examples:
  /hooks add PreToolUse command block matcher=write_file reason="block all writes"
  /hooks add PostToolUse command allow matcher=* command="echo tool executed"
  /hooks add Stop command allow matcher=* command="echo quitting"
  /hooks remove PreToolUse hook_a1b2c3d4`
}

// formatHooksList formats the hook list output
func formatHooksList(m *hook.Manager) string {
	eventNames := []string{
		"PreToolUse", "PostToolUse",
		"PreRead", "PostRead",
		"PreWrite", "PostWrite",
		"PreEdit", "PostEdit",
		"PreBash", "PostBash",
		"PreSearch", "PostSearch",
		"PreGit", "PostGit",
		"PreWeb", "PostWeb",
		"PreAgent", "PostAgent",
		"SessionStart", "SessionStop",
		"Stop", "PostCompact", "Notification",
	}

	var sb strings.Builder
	sb.WriteString("Hook configuration:\n")

	totalHooks := 0
	for _, eventName := range eventNames {
		hooks, _ := m.GetHooksByEvent(eventName)
		if len(hooks) == 0 {
			continue
		}
		sb.WriteString(fmt.Sprintf("\n  [%s] (%d hooks)\n", eventName, len(hooks)))
		for i, h := range hooks {
			b := h.Base()
			sb.WriteString(fmt.Sprintf("    %d. [%s] matcher: %s, action: %s", i, b.ID, b.Matcher, b.Action))
			if b.Reason != "" {
				sb.WriteString(fmt.Sprintf(", reason: %q", b.Reason))
			}
			if cmd, ok := h.(*hook.HookCommand); ok && cmd.Command != "" {
				sb.WriteString(fmt.Sprintf(", command: %q", cmd.Command))
			}
			if b.Pattern != "" {
				sb.WriteString(fmt.Sprintf(", pattern: %q", b.Pattern))
			}
			if b.Type != "" {
				sb.WriteString(fmt.Sprintf(", type: %s", b.Type))
			}
			if b.Timeout > 0 {
				sb.WriteString(fmt.Sprintf(", timeout: %dms", b.Timeout))
			}
			if !b.Enabled {
				sb.WriteString(", (disabled)")
			}
			if b.Priority != 0 {
				sb.WriteString(fmt.Sprintf(", priority: %d", b.Priority))
			}
			// PR-A / PR-A2：信任与改写特权（用户判断「为什么我的 hook 没生效」的关键信息）。
			sb.WriteString(fmt.Sprintf(", trust: %s", m.TrustStatus(h)))
			if m.RewriteGranted(h) {
				sb.WriteString(", rewrite: granted")
			} else {
				sb.WriteString(", rewrite: off")
			}
			sb.WriteString("\n")
		}
		totalHooks += len(hooks)
	}

	if totalHooks == 0 {
		sb.WriteString("\n  (no hooks configured)\n")
	}

	sb.WriteString(fmt.Sprintf("\nTotal %d hook configurations", totalHooks))
	return sb.String()
}

// handleHooksAdd handles the hook add command
func handleHooksAdd(m *hook.Manager, workDir string, args []string) string {
	if len(args) < 3 {
		return "Usage: /hooks add <event> <type> <action> [options]\nOptions: matcher=<glob> command=<cmd> pattern=<glob> reason=<text> timeout=<ms> enabled=true|false priority=<num>"
	}

	eventName := args[0]
	hookType := args[1]
	action := args[2]

	// Validate event type
	if _, err := m.GetHooksByEvent(eventName); err != nil {
		return fmt.Sprintf("Error: unknown event type %q\nAvailable events: PreToolUse, PostToolUse, PreRead, PostRead, PreWrite, PostWrite, PreEdit, PostEdit, PreBash, PostBash, PreSearch, PostSearch, PreGit, PostGit, PreWeb, PostWeb, PreAgent, PostAgent, SessionStart, SessionStop, Stop, PostCompact, Notification", eventName)
	}

	// Validate hook type
	validTypes := map[string]bool{"command": true, "prompt": true, "agent": true, "http": true, "callback": true}
	if !validTypes[hookType] {
		return fmt.Sprintf("Error: unknown hook type %q\nAvailable types: command, prompt, agent, http, callback", hookType)
	}

	// Validate action
	validActions := map[string]bool{"allow": true, "block": true, "ask": true, "warn": true}
	if !validActions[action] {
		return fmt.Sprintf("Error: unknown action %q\nAvailable actions: allow, block, ask, warn", action)
	}

	// 解析选项到临时变量（Hook 现为接口，无法直接设置字段）
	matcher := "*"
	var command, pattern, reason, prompt, model, url string
	timeout := 0
	enabled := true
	priority := 0

	// 解析选项键值对，写入临时变量
	for _, opt := range args[3:] {
		kv := strings.SplitN(opt, "=", 2)
		if len(kv) != 2 {
			continue
		}
		key, value := kv[0], kv[1]

		switch key {
		case "matcher":
			matcher = value
		case "command":
			command = value
		case "pattern":
			pattern = value
		case "reason":
			reason = value
		case "prompt":
			prompt = value
		case "model":
			model = value
		case "url":
			url = value
		case "timeout":
			if v, err := strconv.Atoi(value); err == nil {
				timeout = v
			}
		case "enabled":
			if value == "false" || value == "0" {
				enabled = false
			}
		case "priority":
			if v, err := strconv.Atoi(value); err == nil {
				priority = v
			}
		}
	}

	// 构造公共字段 HookBase
	base := hook.HookBase{
		Type:     hook.HookType(hookType),
		Action:   hook.Action(action),
		Matcher:  matcher,
		Pattern:  pattern,
		Reason:   reason,
		Timeout:  timeout,
		Enabled:  enabled,
		Priority: priority,
	}

	// 根据 hookType 构造对应的具体 Hook 实现
	var h hook.Hook
	switch hookType {
	case "command":
		h = &hook.HookCommand{HookBase: base, Command: command}
	case "prompt":
		h = &hook.HookPrompt{HookBase: base, Prompt: prompt, Model: model}
	case "agent":
		h = &hook.HookAgent{HookBase: base, Prompt: prompt, Model: model}
	case "http":
		h = &hook.HookHTTP{HookBase: base, URL: url}
	case "callback":
		h = &hook.HookCallback{HookBase: base}
	}

	if err := m.AddHook(eventName, h); err != nil {
		return fmt.Sprintf("Failed to add hook: %v", err)
	}

	// 持久化到文件
	if workDir != "" {
		hooksPath := filepath.Join(workDir, ".whale", "hooks.json")
		if err := m.SaveConfig(hooksPath); err != nil {
			return fmt.Sprintf("Hook added but persistence failed: %v", err)
		}
	}

	// 构造描述输出，通过 Base() 和类型断言访问字段
	var desc string
	desc = fmt.Sprintf("matcher=%s, action=%s", h.Base().Matcher, h.Base().Action)
	if cmd, ok := h.(*hook.HookCommand); ok && cmd.Command != "" {
		desc += fmt.Sprintf(", command=%q", cmd.Command)
	}
	if h.Base().Reason != "" {
		desc += fmt.Sprintf(", reason=%q", h.Base().Reason)
	}

	return fmt.Sprintf("Added %s hook: %s", eventName, desc)
}

// handleHooksRemove handles the hook remove command
// Uses ID-based removal for safe concurrent modifications
func handleHooksRemove(m *hook.Manager, workDir string, args []string) string {
	if len(args) < 2 {
		return "Usage: /hooks remove <event> <id>"
	}

	eventName := args[0]
	id := args[1]

	if err := m.RemoveHookByID(eventName, id); err != nil {
		return fmt.Sprintf("Failed to remove hook: %v", err)
	}

	// Persist to file
	if workDir != "" {
		hooksPath := filepath.Join(workDir, ".whale", "hooks.json")
		if err := m.SaveConfig(hooksPath); err != nil {
			return fmt.Sprintf("Hook removed but persistence failed: %v", err)
		}
	}

	return fmt.Sprintf("Removed %s hook %s", eventName, id)
}

// handleHooksReview 列出所有待信任评审的 hook（PR-A · Q1，对齐业内既有实现 /hooks review）。
//
// 语义：未信任（untrusted）与被改过（modified）的 hook 都不会运行，只有在这里
// 出现、并被 /hooks trust 之后才会生效。
func handleHooksReview(m *hook.Manager) string {
	items := m.PendingReview()
	if len(items) == 0 {
		return "No hooks pending review. All configured hooks are trusted and will run."
	}
	var b strings.Builder
	b.WriteString(fmt.Sprintf("%d hook(s) pending review — they will NOT run until trusted:\n", len(items)))
	for _, it := range items {
		cmd := it.Command
		if cmd == "" {
			cmd = "(no command)"
		}
		b.WriteString(fmt.Sprintf("  [%s] id=%s type=%s status=%s\n      %s\n", it.Event, it.HookID, it.Type, it.Status, cmd))
	}
	b.WriteString("\nRun: /hooks trust <id>  — or  /hooks trust all")
	return b.String()
}

// handleHooksTrust 信任 hook（PR-A · Q1）。
// 信任后 hook 才会参与工具决策；hook 内容一旦变更，指纹失效，需重新信任。
func handleHooksTrust(m *hook.Manager, args []string) string {
	if len(args) == 0 {
		return "Usage: /hooks trust <id>|all"
	}
	if args[0] == "all" {
		if err := m.TrustAllPending(); err != nil {
			return fmt.Sprintf("Trust all failed: %v", err)
		}
		return "All pending hooks trusted.\nNote: trusted hooks run with agent privileges — review them before trusting."
	}
	if err := m.TrustHookByID(args[0]); err != nil {
		return fmt.Sprintf("Trust failed: %v", err)
	}
	return fmt.Sprintf("Hook %s trusted.", args[0])
}

// handleHooksUntrust 撤销信任，hook 立即停止运行（下次执行即被跳过）。
func handleHooksUntrust(m *hook.Manager, args []string) string {
	if len(args) == 0 {
		return "Usage: /hooks untrust <id>|all"
	}
	if args[0] == "all" {
		if err := m.RevokeAllTrust(); err != nil {
			return fmt.Sprintf("Untrust all failed: %v", err)
		}
		return "All hook trust revoked. No hooks will run until re-trusted."
	}
	if err := m.RevokeTrustByID(args[0]); err != nil {
		return fmt.Sprintf("Untrust failed: %v", err)
	}
	return fmt.Sprintf("Trust revoked for hook %s.", args[0])
}

// handleHooksAllowRewrite 授予改写特权（PR-A2 · ② 拍板）。
//
// 语义：hook "可运行" 只代表它有权参与决策；改写工具输入（updatedInput）或
// 喂回模型的输出（Modified）会直接改变 agent 行为，属 Q4 强能力 —— 默认关闭，
// 必须由用户逐条显式授权。授权按内容指纹记录，hook 一改即失效。
func handleHooksAllowRewrite(m *hook.Manager, args []string) string {
	if len(args) == 0 {
		return "Usage: /hooks allow-rewrite <id>|all"
	}
	if args[0] == "all" {
		if err := m.GrantRewriteAll(); err != nil {
			return fmt.Sprintf("Grant rewrite (all) failed: %v", err)
		}
		return "Rewrite capability granted to ALL hooks.\nNote: they may now change tool inputs and the output fed back to the model."
	}
	if err := m.GrantRewriteByID(args[0]); err != nil {
		return fmt.Sprintf("Grant rewrite failed: %v", err)
	}
	return fmt.Sprintf("Rewrite capability granted to hook %s.", args[0])
}

// handleHooksDenyRewrite 撤销改写特权（运行信任保留，hook 仍会执行决策）。
func handleHooksDenyRewrite(m *hook.Manager, args []string) string {
	if len(args) == 0 {
		return "Usage: /hooks deny-rewrite <id>|all"
	}
	if args[0] == "all" {
		if err := m.RevokeRewriteAll(); err != nil {
			return fmt.Sprintf("Revoke rewrite (all) failed: %v", err)
		}
		return "Rewrite capability revoked for ALL hooks (they can still run)."
	}
	if err := m.RevokeRewriteByID(args[0]); err != nil {
		return fmt.Sprintf("Revoke rewrite failed: %v", err)
	}
	return fmt.Sprintf("Rewrite capability revoked for hook %s.", args[0])
}
