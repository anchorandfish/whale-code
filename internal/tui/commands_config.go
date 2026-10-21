package tui

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/anchorandfish/whale-code/internal/permission"
	"github.com/anchorandfish/whale-code/internal/util"
)

// 权限模式 → 运行期 Level 的转换已收敛到 permission.LookupModeName（单一真相源）。
// 启动装配用 permission.NewPolicyFromModeName，运行期切换用 Policy.SetModeByName，
// 二者都会对旧配置里的已删除模式（accept-edits / dont-ask / plan / auto / bubble）
// 做兼容翻译，并对 accept-edits 注入编辑/安全命令的默认 allow 规则。

const permissionModeChoiceToolID = "__whale_permissions_mode__"

type permissionModeMenuItem struct {
	level         permission.Level
	label         string
	description   string
	descriptionZH string
}

// permissionModeMenu 是 /permissions 交互式菜单的档位列表（阶段 2 收模式：8 → 3）。
//
// 只保留同类 CLI 三档能力：read-only / workspace-write / danger-full-access。
// 已删除的模式（accept-edits / dont-ask / plan / auto / bubble）不再出现在菜单里，
// 但旧名称仍可被 /permissions <mode> 解析（向后兼容，见 parsePermissionModeName）。
//
// ★ 2026-09-18：label 改为**英文主**（直接用 mode 名，与命令输入一一对应，用户可照抄）；
// 中文释义拆进 descriptionZH（渲染为缩进辅助行，形态 SSOT = bilingual.go）。
var permissionModeMenu = []permissionModeMenuItem{
	{permission.LevelReadOnly, "read-only", "Read only; writes are blocked",
		"只读；不能修改文件或执行写操作"},
	{permission.LevelWorkspaceWrite, "workspace-write", "Workspace writes ask for confirmation",
		"工作区内写入需确认，危险操作仍会拦截"},
	{permission.LevelDangerFullAccess, "danger-full-access",
		"High-risk operations are allowed; use only when intentional",
		"允许高风险操作；仅在你明确知道后果时使用"},
}

func permissionModeMenuChoices(current permission.Level) ([]ChoiceOption, int) {
	options := make([]ChoiceOption, 0, len(permissionModeMenu))
	selected := 0
	for i, item := range permissionModeMenu {
		options = append(options, ChoiceOption{
			Label:         item.label,
			Value:         item.level.String(),
			Description:   item.description,
			DescriptionZH: item.descriptionZH,
		})
		if item.level == current {
			selected = i
		}
	}
	return options, selected
}

// parsePermissionModeName 解析模式名（含已删除模式的向后兼容别名）。
//
// 单一真相源在 permission.LookupModeName，这里只做薄封装，避免 TUI 与权限包
// 两处解析实现漂移（影子实现）。已删除的模式名（accept-edits / dont-ask / plan /
// auto / bubble）仍能被识别，由 SetModeByName 翻译到 3 档。
func parsePermissionModeName(name string) (permission.Level, bool) {
	return permission.LookupModeName(name)
}

// handleMemory 处理 /memory 命令（TUI 模式入口）。
// 支持 list/edit/show/add/delete 五个子命令。store 为 nil 时返回错误。
//
// edit 子命令需启动外部编辑器（$VISUAL/$EDITOR），会直接读写 stdin/stdout/stderr，
// 与 bubbletea 的 AltScreen 终端控制冲突。因此在调用共享 HandleMemoryCommand 前，
// 先通过 program.ReleaseTerminal() 暂停 bubbletea 的终端接管，编辑器退出后再
// program.RestoreTerminal() 恢复。program 为 nil（未注入）时降级为直接调用，
// 保持与 Plain/Bridge 模式一致的行为。
func (m *Model) handleMemory(args string) (string, error) {
	parts := strings.Fields(args)
	if len(parts) >= 2 && parts[0] == "edit" {
		store := m.aiService.MemoryStore()
		if store == nil {
			return "", fmt.Errorf("memory store 未初始化")
		}
		if prog := m.program.get(); prog != nil {
			if err := prog.ReleaseTerminal(); err != nil {
				return "", fmt.Errorf("释放终端失败: %w", err)
			}
			defer prog.RestoreTerminal()
		}
		if err := store.EditInEditor(parts[1]); err != nil {
			return "", err
		}
		return fmt.Sprintf("已编辑记忆: %s", parts[1]), nil
	}
	return HandleMemoryCommand(m.aiService.MemoryStore(), args)
}

// handleConfig handle config command
func (m *Model) handleConfig(args string) string {
	if args == "env" {
		return m.formatEnvConfig()
	}

	// Support auto_compact config setting
	parts := strings.Fields(args)
	if len(parts) == 2 && parts[0] == "auto_compact" {
		val := strings.ToLower(parts[1])
		switch val {
		case "true", "on", "1", "yes":
			m.sessionService.Config().AutoCompact = true
			if m.sessionService.ConfigLoader() != nil {
				m.sessionService.ConfigLoader().Save(m.sessionService.Config())
			}
			return "auto_compact = true (uses the current model context threshold)"
		case "false", "off", "0", "no":
			m.sessionService.Config().AutoCompact = false
			if m.sessionService.ConfigLoader() != nil {
				m.sessionService.ConfigLoader().Save(m.sessionService.Config())
			}
			return "auto_compact = false (threshold crossings only warn)"
		default:
			return fmt.Sprintf("auto_compact invalid parameter: %s，Please enter true or false", parts[1])
		}
	}
	if len(parts) == 1 && parts[0] == "auto_compact" {
		return fmt.Sprintf("auto_compact: %v\nUse /config auto_compact true|false to toggle", m.sessionService.Config().AutoCompact)
	}

	return m.formatCurrentConfig()
}

// formatCurrentConfig format current config
func (m *Model) formatCurrentConfig() string {
	var sb strings.Builder
	sb.WriteString("Config:\n")
	sb.WriteString(fmt.Sprintf(" model: %s\n", m.sessionService.Config().Model))
	sb.WriteString(fmt.Sprintf(" fallback: %s\n", m.sessionService.Config().FallbackModel))
	sb.WriteString(fmt.Sprintf(" permission: %s\n", m.sessionService.Config().PermissionMode))
	sb.WriteString(fmt.Sprintf(" contextWindow: %d\n", m.sessionService.Config().ContextWindow))
	sb.WriteString(fmt.Sprintf(" compaction: %s\n", m.sessionService.Config().CompactionStrategy))
	sb.WriteString(fmt.Sprintf(" autoMemory: %v\n", m.sessionService.Config().AutoMemoryEnabled))
	sb.WriteString(fmt.Sprintf(" autoCompact: %v\n", m.sessionService.Config().AutoCompact))
	return sb.String()
}

// formatEnvConfig format env vars
func (m *Model) formatEnvConfig() string {
	envVars := []string{
		"ANTHROPIC_API_KEY", "OPENAI_API_KEY", "OPENAI_BASE_URL",
		"WHALE_MODEL", "WHALE_FALLBACK_MODEL", "WHALE_PERMISSION_MODE",
		"WHALE_CONTEXT_WINDOW", "WHALE_SESSION_BUDGET",
	}

	var sb strings.Builder
	sb.WriteString("Environment:\n")
	for _, v := range envVars {
		val := os.Getenv(v)
		if val != "" {
			if strings.Contains(strings.ToUpper(v), "KEY") {
				if len(val) > 8 {
					val = val[:4] + "..." + val[len(val)-4:]
				}
			}
			sb.WriteString(fmt.Sprintf(" %s=%s\n", v, val))
		} else {
			sb.WriteString(fmt.Sprintf(" %s=(not set)\n", v))
		}
	}
	return sb.String()
}

// handleDoctor handle diagnostics
func (m *Model) handleDoctor() string {
	var parts []string
	parts = append(parts, "Diagnostics:")

	hasAnthropic := os.Getenv("ANTHROPIC_API_KEY") != ""
	hasOpenAI := os.Getenv("OPENAI_API_KEY") != ""
	if hasAnthropic {
		parts = append(parts, " [OK] ANTHROPIC_API_KEY set")
	} else {
		parts = append(parts, " [!!] ANTHROPIC_API_KEY not set")
	}
	if hasOpenAI {
		parts = append(parts, " [OK] OPENAI_API_KEY set")
	} else {
		parts = append(parts, " [!!] OPENAI_API_KEY not set")
	}
	if !hasAnthropic && !hasOpenAI {
		parts = append(parts, " [XX] No API Key set, cannot use AI")
	}

	model := m.sessionService.ModelName()
	envModel := os.Getenv("WHALE_MODEL")
	if model != "" {
		modelInfo := fmt.Sprintf(" [OK] Model: %s", model)
		if envModel != "" && envModel != model {
			modelInfo += fmt.Sprintf(" (WHALE_MODEL=%s)", envModel)
		}
		parts = append(parts, modelInfo)
	} else if envModel != "" {
		parts = append(parts, fmt.Sprintf(" [OK] Model: %s (from WHALE_MODEL)", envModel))
	} else {
		parts = append(parts, " [XX] No model set (set WHALE_MODEL or use /model)")
	}

	baseURL := os.Getenv("OPENAI_BASE_URL")
	if baseURL != "" {
		parts = append(parts, fmt.Sprintf(" [OK] OPENAI_BASE_URL: %s", baseURL))
	}

	if _, err := os.Stat(m.sessionService.WorkDir()); err == nil {
		parts = append(parts, fmt.Sprintf(" [OK] Workdir: %s", m.sessionService.WorkDir()))
	} else {
		parts = append(parts, fmt.Sprintf(" [!!] Workdir inaccessible: %s", m.sessionService.WorkDir()))
	}

	docs := m.aiService.MagicDocs().List()
	if len(docs) > 0 {
		parts = append(parts, fmt.Sprintf(" [OK] Magic Docs: %d files", len(docs)))
	} else {
		parts = append(parts, " [..] No WHALE.md detected")
	}

	// N12: V2 记忆文件计数（store 为 nil 时为 0）。
	memCount := 0
	if store := m.aiService.MemoryStore(); store != nil {
		if entries, err := store.List(); err == nil {
			memCount = len(entries)
		}
	}
	parts = append(parts, fmt.Sprintf(" Memory entries: %d", memCount))

	if m.aiService.Fallback().IsDegraded() {
		parts = append(parts, fmt.Sprintf(" [!] Degraded -> %s", m.aiService.Fallback().ActiveModel()))
	} else {
		parts = append(parts, " [OK] Model OK")
	}

	return strings.Join(parts, "\n")
}

// handlePermissionsCommand handles /permissions and its subcommands
func (m *Model) handlePermissionsCommand(args string) string {
	// No args: open an interactive mode picker. The long form remains available
	// below for scripts and users who prefer typing the exact mode name.
	if args == "" {
		current := m.toolService.Policy().Mode()
		options, selected := permissionModeMenuChoices(current)
		m.toolService.SetPendingChoice(&PendingChoice{
			// G12：提示语必须点明"不含沙箱"，否则这个菜单看起来就是"总的权限开关"。
			Prompt:     fmt.Sprintf("Select approval mode — not the sandbox (current: %s)", current),
			Options:    options,
			ToolID:     permissionModeChoiceToolID,
			DefaultIdx: selected,
			Category:   "permissions",
		})
		m.toolService.SetChoiceIdx(selected)
		m.sessionService.SetState(StateWaitingChoice)
		m.recalcViewportHeight()
		return biSame("Current permission: %s (↑↓ select · Enter apply · Esc cancel)",
			"当前权限：%s（↑↓ 选择 · Enter 应用 · Esc 取消）", current) + "\n" +
			bi("approval axis only — does not touch the sandbox "+
				"(use /sandbox on|off or /security-preset for the sandbox)",
				"仅审批轴，不含沙箱（沙箱请用 /sandbox on|off 或 /security-preset）")

	}

	// The following status output is kept for the explicit `status` subcommand.
	if strings.EqualFold(strings.TrimSpace(args), "status") {
		var sb strings.Builder
		sb.WriteString(fmt.Sprintf("Permission: %s", m.toolService.Policy().Mode()))
		// G12/G12b：轴归属必须显式 —— `permissions` 极易被当成"总的权限开关"，
		// 而它完全不碰 OS 隔离。文案收在 approvalAxisNotice（与 --plain 共用同一份
		// SSOT，见 sandbox_toggle.go），此处只补门内部状态，避免两 UI 各自漂移。
		exec := m.toolService.Executor()
		sb.WriteString(fmt.Sprintf("\n  capability axis  : sandbox=%s [%s]",
			sandboxStateWord(sandboxCapabilityOn(exec)), sandboxGateStatus(exec)))
		sb.WriteString("\n  " + approvalAxisNotice(m.toolService.Policy().Mode(), exec))
		sb.WriteString("\nAvailable modes: read-only, workspace-write, danger-full-access")
		sb.WriteString("\n (legacy names accept-edits, dont-ask, plan, auto, bubble are still accepted)")

		ruleSet := m.toolService.Policy().RuleSet()
		if ruleSet != nil && ruleSet.Count() > 0 {
			sb.WriteString(fmt.Sprintf("\n\nRules: %d active", ruleSet.Count()))
			sb.WriteString("\nUse /permissions list to view rules")
		} else {
			sb.WriteString("\n\nNo custom rules. Use /permissions add to create rules")
		}
		return sb.String()
	}

	// Parse subcommands: permissions <subcommand> [args...]
	fields := splitArgs(args)
	if len(fields) == 0 {
		return errorStyle.Render("Invalid arguments")
	}

	subCmd := strings.ToLower(fields[0])

	switch subCmd {
	case "list", "rules":
		return m.handlePermissionsList()

	case "add":
		return m.handlePermissionsAdd(fields[1:])

	case "remove", "rm":
		return m.handlePermissionsRemove(fields[1:])

	case "doctor":
		return m.handlePermissionsDoctor()

	case "reset":
		return m.handlePermissionsReset()

	case "reload":
		return m.handlePermissionsReload()

	default:
		// Try to handle as mode name.
		// 走 SetModeByName：它同时完成「解析」与「已删除模式的审批语义迁移」
		// （如 accept-edits 会注入编辑/安全命令的默认 allow 规则）。
		if m.toolService.Policy().SetModeByName(fields[0]) {
			// G12：回执必须自报轴归属 —— 本命令只写审批轴，沙箱不受影响。
			// G12b：文案走 approvalAxisNotice（与 --plain 共用 SSOT，见 sandbox_toggle.go）。
			return fmt.Sprintf("Permission: %s\n  %s",
				m.toolService.Policy().Mode(),
				approvalAxisNotice(m.toolService.Policy().Mode(), m.toolService.Executor()))
		}

		return errorStyle.Render(unknownApprovalModeNotice(fields[0]) +
			"\nUsage: /permissions (menu) | [mode] | status | add | remove | list | doctor | reset | reload")
	}
}

// handlePermissionsList lists all permission rules
func (m *Model) handlePermissionsList() string {
	ruleSet := m.toolService.Policy().RuleSet()
	if ruleSet == nil || ruleSet.Count() == 0 {
		return "No custom rules."
	}

	var sb strings.Builder
	sb.WriteString("Permission Rules:\n")

	rules := ruleSet.List()
	for _, rule := range rules {
		sourceLabel := fmt.Sprintf("[%s]", rule.Source)
		sb.WriteString(fmt.Sprintf(" %s %s %s\n",
			sourceLabel, rule.Behavior, rule.String()))
	}

	return sb.String()
}

// handlePermissionsAdd handles /permissions add <behavior> <rule>
func (m *Model) handlePermissionsAdd(fields []string) string {
	if len(fields) < 2 {
		return "Usage: /permissions add <allow|deny|ask> <rule>\n" +
			" --session (session only, not persisted)\n" +
			" --project (project settings)\n" +
			" --user (user settings)"
	}

	// Default to project-level persistence so rules survive restart
	// (aligns with a comparable CLI: /permissions add writes project settings by default).
	// --session keeps runtime-only memory semantics for the caller who opts in.
	source := permission.SourceProjectSettings

	// Check --session / --project / --user flags
	startIdx := 0
	if len(fields) > 2 {
		flag := strings.ToLower(fields[0])
		switch flag {
		case "--session":
			source = permission.SourceSession
			startIdx = 1
		case "--project":
			source = permission.SourceProjectSettings
			startIdx = 1
		case "--user":
			source = permission.SourceUserSettings
			startIdx = 1
		}
	}

	behaviorStr := strings.ToLower(fields[startIdx])
	ruleStr := strings.Join(fields[startIdx+1:], " ")

	// Strip quotes from rule string
	ruleStr = strings.Trim(ruleStr, `"'`)

	var behavior permission.RuleBehavior
	switch behaviorStr {
	case "allow":
		behavior = permission.RuleAllow
	case "deny":
		behavior = permission.RuleDeny
	case "ask":
		behavior = permission.RuleAsk
	default:
		return errorStyle.Render(fmt.Sprintf("Invalid behavior: %s (use allow, deny, or ask)", behaviorStr))
	}

	rule, err := permission.ParseRuleString(ruleStr, behavior, source)
	if err != nil {
		return errorStyle.Render(fmt.Sprintf("Invalid rule syntax: %v", err))
	}

	rule.Behavior = behavior
	rule.Source = source

	if err := m.toolService.Policy().AddRule(*rule); err != nil {
		return errorStyle.Render(fmt.Sprintf("Failed to add rule: %v", err))
	}

	return fmt.Sprintf("Rule added: %s %s [%s]", behavior, ruleStr, source)
}

// handlePermissionsRemove handles /permissions remove <behavior> <rule>
func (m *Model) handlePermissionsRemove(fields []string) string {
	if len(fields) < 2 {
		return "Usage: /permissions remove <allow|deny|ask> <rule>"
	}

	behaviorStr := strings.ToLower(fields[0])
	ruleStr := strings.Join(fields[1:], " ")
	ruleStr = strings.Trim(ruleStr, `"'`)

	var behavior permission.RuleBehavior
	switch behaviorStr {
	case "allow":
		behavior = permission.RuleAllow
	case "deny":
		behavior = permission.RuleDeny
	case "ask":
		behavior = permission.RuleAsk
	default:
		return errorStyle.Render(fmt.Sprintf("Invalid behavior: %s", behaviorStr))
	}

	// Try to parse rule for toolName and ruleContent
	// Use session as temp source
	parsed, err := permission.ParseRuleString(ruleStr, behavior, permission.SourceSession)
	if err != nil {
		return errorStyle.Render(fmt.Sprintf("Invalid rule syntax: %v", err))
	}

	removed := m.toolService.Policy().RuleSet().Remove(parsed.ToolName, parsed.RuleContent, behavior, permission.SourceSession)
	if removed == 0 {
		removed = m.toolService.Policy().RuleSet().Remove(parsed.ToolName, parsed.RuleContent, behavior, permission.SourceProjectSettings)
	}
	if removed == 0 {
		removed = m.toolService.Policy().RuleSet().Remove(parsed.ToolName, parsed.RuleContent, behavior, permission.SourceLocalSettings)
	}
	if removed == 0 {
		removed = m.toolService.Policy().RuleSet().Remove(parsed.ToolName, parsed.RuleContent, behavior, permission.SourceUserSettings)
	}

	if removed > 0 {
		// Save to persistent storage
		if store := m.toolService.Policy().RuleStore(); store != nil {
			store.Save(permission.SourceSession, m.toolService.Policy().RuleSet())
			store.Save(permission.SourceProjectSettings, m.toolService.Policy().RuleSet())
			store.Save(permission.SourceLocalSettings, m.toolService.Policy().RuleSet())
			store.Save(permission.SourceUserSettings, m.toolService.Policy().RuleSet())
		}
		return "Rule removed."
	}

	return "Rule not found."
}

// handlePermissionsDoctor shows permission health check.
//
// F1（2026-09-16 修复）：本函数此前把**同一套系统状态渲染了两遍** ——
// 上半段 `switch mode` 按审批档位硬编码散文（danger 档那句承诺
// "All operations will be executed without confirmation"），末尾又独立读
// `Sandbox.Enabled()` 打 "Sandbox: true — permissions are restricted"。
// danger 档下两句**同屏互相打脸**（真机 2026-09-16 02:45，用户每次必当成 bug 报）。
//
// 根因**不是**"少一句说明"，而是**两个正交轴被混在一处、各自渲染**：
//
//	审批轴（ask/deny 策略）：Policy().Mode() —— 决定"问不问"
//	能力轴（OS 隔离） ：sandboxGateStatus() —— 决定"拦不拦得住"
//
// 二者在 danger-full-access 下**必然分叉**：本仓 danger 档只翻审批轴，
// 完全不碰 `Sandbox.Enabled` / `SandboxMgr`（对照同类 CLI
// `--dangerously-bypass-approvals-and-sandbox` 是**同时**翻两轴，
// “）。所以在 danger 档无条件承诺"无需确认"
// 本身就是错口径；正确写法是把它限定到审批轴上，再把能力轴单独、如实地报出来。
//
// 修法（**零语义变更，只动渲染**）：
// - 分两节渲染，两节都读**结构化事实**；
// - 能力轴复用 `/sandbox` 的同一 SSOT `sandboxGateStatus()`（`sandbox_toggle.go`）
// —— 此前 doctor 自己读 `Sandbox.Enabled()`，正是 A1 已修过的那个假报面
// （只报 legacy 门、看不见 Manager 门 ⇒ 会出现"显示 ON 实际无 Manager"）；
// - danger 档那句只在**能力轴也放开**时才不加限定。
//
// R4：这里读 `Policy().Mode()` / `Sandbox.Enabled()` / `SandboxMgr.Level()`
// 三个结构化事实，**不解析任何散文文案**。
func (m *Model) handlePermissionsDoctor() string {
	var sb strings.Builder
	sb.WriteString("Permission Health Check:\n\n")

	exec := m.toolService.Executor()
	// 能力轴的唯一结构化判定（单点，见 sandbox_toggle.go；与 /sandbox 同源）。
	sandboxOn := sandboxCapabilityOn(exec)

	// ── 轴 1：审批 —— 决定"问不问" ──────────────────────────────────
	mode := m.toolService.Policy().Mode()
	sb.WriteString(" Approval axis / 审批轴 (whether it asks)\n")
	sb.WriteString(fmt.Sprintf(" Mode: %s\n", mode))
	switch mode {
	case permission.LevelDangerFullAccess:
		if sandboxOn {
			// F1：能力轴仍在收窄 ⇒ 这句必须自我限定，否则与下面「轴 2」打脸。
			sb.WriteString(" Regular operations are NOT confirmed here,\n")
			sb.WriteString(" but the sandbox below still restricts them.\n")
		} else {
			sb.WriteString(" Regular operations are NOT confirmed here,\n")
			sb.WriteString(" and no sandbox is installed to back that up.\n")
		}
		sb.WriteString(" (dangerous commands still require a second confirmation).\n")
	case permission.LevelReadOnly:
		sb.WriteString(" Read-only mode active. All write operations are blocked.\n")
	case permission.LevelWorkspaceWrite:
		sb.WriteString(" Standard mode. Write operations require confirmation.\n")
	}

	// ── 轴 2：能力 —— 决定"拦不拦得住" ──────────────────────────────
	sb.WriteString("\n Capability axis / 能力轴 (whether it can still stop things)\n")
	sb.WriteString(fmt.Sprintf(" Gates: %s\n", sandboxGateStatus(exec)))
	if !sandboxOn {
		// 复用 `/sandbox off` 的同一 T1 降级文案（SSOT：sandbox_toggle.go）。
		sb.WriteString(fmt.Sprintf(" ⚠️ %s\n", sandboxDisableNotice))
	}

	// ── 自定义规则 ────────────────────────────────────────────────
	ruleSet := m.toolService.Policy().RuleSet()
	if ruleSet != nil {
		sb.WriteString(fmt.Sprintf("\n Custom Rules: %d\n", ruleSet.Count()))
		denyCount := 0
		for _, rule := range ruleSet.List() {
			if rule.Behavior == permission.RuleDeny {
				denyCount++
			}
		}
		if denyCount > 0 {
			sb.WriteString(fmt.Sprintf(" Blocking rules: %d\n", denyCount))
		}
	}

	return sb.String()
}

// handlePermissionsReset clears all custom rules
func (m *Model) handlePermissionsReset() string {
	ruleSet := m.toolService.Policy().RuleSet()
	if ruleSet == nil {
		return "No rules to reset."
	}

	count := ruleSet.Count()
	ruleSet.Clear()

	// Clear persistent storage
	if store := m.toolService.Policy().RuleStore(); store != nil {
		store.Save(permission.SourceSession, ruleSet)
		store.Save(permission.SourceProjectSettings, ruleSet)
		store.Save(permission.SourceLocalSettings, ruleSet)
		store.Save(permission.SourceUserSettings, ruleSet)
	}

	return fmt.Sprintf("All %d custom rules cleared.", count)
}

// handlePermissionsReload reloads rules from settings files
func (m *Model) handlePermissionsReload() string {
	store := m.toolService.Policy().RuleStore()
	if store == nil {
		return "Rule store not configured."
	}

	ruleSet, err := store.LoadAll()
	if err != nil {
		return errorStyle.Render(fmt.Sprintf("Failed to reload rules: %v", err))
	}

	m.toolService.Policy().SetRuleSet(ruleSet)
	return fmt.Sprintf("Rules reloaded. %d rules active.", ruleSet.Count())
}

// handleInit init WHALE.md
func (m *Model) handleInit() string {
	whaleMd := `# Whale Code Project Info

This file is auto-read by Whale Code for project context.

## Project Structure
Describe your project structure here.

## Coding Standards
Describe your coding conventions here.

## Notes
Add any important notes here.
`
	path := filepath.Join(m.sessionService.WorkDir(), "WHALE.md")
	if _, err := os.Stat(path); err == nil {
		return "WHALE.md already exists"
	}
	if err := util.AtomicWriteFile(path, []byte(whaleMd), 0644); err != nil {
		return fmt.Sprintf("Create failed: %v", err)
	}
	m.aiService.MagicDocs().Refresh()
	return "Created WHALE.md (edit it to describe your project)"
}
