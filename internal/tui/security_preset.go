package tui

// security_preset.go — `/security-preset <id>`：**双轴**写入面（G13，2026-09-16）。
//
// # 与两个单轴写入面的分工（用户明确要求把这件事讲清）
//
// 本仓有三个安全相关的写入面，各自写的轴**不同**，必须让用户一眼分清：
//
//	/permissions <mode> → 审批轴 only（"问不问"） **不含沙箱**
//	/sandbox on|off → 能力轴 only（OS 隔离） **不含审批档**
//	/security-preset <id> → **两轴一起**（本文件）
//
// `permissions` 这个名字极易被当成"总的权限开关"（用户实测反馈），所以三个面的
// 命令描述 / 帮助行 / 运行输出都必须**显式写出轴归属**，而不是让用户去猜。
//
// # 为什么要单独有双轴面（而不是让 /sandbox 顺手改档）
//
// 存在一类**正当**的双轴诉求："一键变安全"/"一键完全放开"。此前它被错误地挂在
// `/sandbox` 上（G12 已按 Opt-A 摘除，见 sandbox_toggle.go 的 sandboxAxisNotice）。
// 本文件的预设层就是给这类诉求一个**合法的家**：
//
//	I1 · 一轴一写者：审批轴只由 /permissions 写，能力轴只由 /sandbox 写。
//	I2 · 跨轴写入点的名字必须声明它写哪些轴。`/security-preset` 声明"整体安全姿态"，
// 且预设 ID 用**独立命名空间**（safe / standard / unrestricted），
// 绝不复用单轴 SSOT token（read-only / workspace-write / danger-full-access）。
// 该约束由 internal/permission 的
// TestSecurityPresetIDsDoNotCollideWithModeNames 机器校验。
//	I3 · 写入值不得取自启动配置：预设是**静态三元组**，不读 cfg。因此
// `on∘off` 类对合性自动成立，不存在"回落 cfg 冒充上一档"的取值来源错误。
//	I4 · 预设层的"当前项"判定对**两轴取合取**，无匹配时显式显示 `Custom`，
// 不得四舍五入到最近的预设（见 permission.MatchSecurityPreset）。
//
// 对照同类 CLI：它的 TUI 只暴露 3 个打包预设（`Permissions` 弹窗 / slash 命令表里
// 没有单轴命令），CLI 层才给单轴 flag（`-s` / `-a`，互不写入）
// ——``、``。
// 本仓把两层都开放：新手用套餐，高级用户用单轴自由组合（比同类 CLI TUI 更强）。

import (
	"fmt"
	"strings"

	"github.com/anchorandfish/whale-code/internal/permission"
	"github.com/anchorandfish/whale-code/internal/tools"
)

// securityPresetChoiceToolID 是 /security-preset 交互菜单的标识。
//
// 与 permissionModeChoiceToolID 共用同一套 PendingChoice 机制 ⇒ 上下键 / Enter / Esc
// 的按键处理**复用 app.go 的同一段代码**（StateWaitingChoice 分支），不新增按键分支。
const securityPresetChoiceToolID = "__whale_security_preset__"

// securityPresetMenuChoices 把内置预设转成可上下键选择的菜单项。
//
// 与 permissionModeMenuChoices 同形（同一套渲染路径），差别只在数据源：
// 这里是**双轴**预设（permission.BuiltinSecurityPresets），那里是单轴档位。
//
// 当前项判定复用 permission.MatchSecurityPreset（I4：**两轴取合取**）。
// 无匹配（= Custom）时高亮第 0 项 —— 只是高亮；用户不按 Enter 就不会写入任何轴，
// 因此不存在"打开菜单即改档"的副作用。
func securityPresetMenuChoices(level permission.Level, sandboxOn bool) ([]ChoiceOption, int) {
	presets := permission.BuiltinSecurityPresets()
	options := make([]ChoiceOption, 0, len(presets))
	selected := 0
	currentID := ""
	if cur, ok := permission.MatchSecurityPreset(level, sandboxOn); ok {
		currentID = cur.ID
	}
	for i, p := range presets {
		options = append(options, ChoiceOption{
			Label:         p.Label, // 英文主（Safe / Standard / Unrestricted）
			Value:         p.ID,
			Description:   p.Description,   // 英文主
			DescriptionZH: p.DescriptionZH, // 中文辅（渲染为缩进辅助行）
		})
		if p.ID == currentID {
			selected = i
		}
	}
	return options, selected
}

// handleSecurityPreset 是 Model 侧入口。
//
// ★ 与 runnerCore 的**有意差异**（：两侧判据必须分开验，不许假设"也一样"）：
// - TUI 无参 ⇒ 打开**上下键选择器**（与 /permissions 同一套机制）；
// - `--plain` 无参 ⇒ 仍打印文本清单（该路径没有键盘交互可用）。
//
// 两侧的**应用语义**仍然同源：都走 applySecurityPreset + securityPresetAppliedNotice。
func (m *Model) handleSecurityPreset(args string) string {
	if strings.TrimSpace(args) == "" {
		return m.openSecurityPresetPicker()
	}
	return securityPresetCommand(m.toolService.Policy(), m.toolService.Executor(), args)
}

// openSecurityPresetPicker 打开双轴预设的上下键选择器。
//
// 只**打开**面板，不写入任何轴 —— 写入发生在用户按 Enter 之后
// （app.go 的 choiceResultMsg 分支），Esc 则什么都不改。
func (m *Model) openSecurityPresetPicker() string {
	exec := m.toolService.Executor()
	level := permission.LevelWorkspaceWrite
	if p := m.toolService.Policy(); p != nil {
		level = p.Mode()
	}
	sandboxOn := sandboxCapabilityOn(exec)
	options, selected := securityPresetMenuChoices(level, sandboxOn)

	m.toolService.SetPendingChoice(&PendingChoice{
		Prompt:     "Select security preset — writes BOTH axes (approval + sandbox)",
		Options:    options,
		ToolID:     securityPresetChoiceToolID,
		DefaultIdx: selected,
		Category:   "security-preset",
	})
	m.toolService.SetChoiceIdx(selected)
	m.sessionService.SetState(StateWaitingChoice)
	m.recalcViewportHeight()

	// ★ 「当前状态」**复用 securityPresetCurrentLine**（与 listing / plain 侧同源）。
	// 不在这里另写一套措辞 —— 同一事实跨入口必须同源（的教训），
	// 否则 TUI 报 `approval=...`、plain 报 `Current: standard`，两处各自漂移。
	return securityPresetCurrentLine(m.toolService.Policy(), exec) + "\n" +
		bi("↑↓ select · Enter apply · Esc cancel", "↑↓ 选择 · Enter 应用 · Esc 取消")
}

// handleSecurityPreset 是 runnerCore 侧入口（plain/bridge 路径）。
//
// 两侧都只是**薄转发**到同一个实现 —— 这是刻意的：历史上"两处同形各自维护"
// 产生过漂移（见 sandbox_toggle.go 头注释），转发式接线让漂移不可能发生。
func (c *runnerCore) handleSecurityPreset(args string) string {
	return securityPresetCommand(c.Policy, c.Executor, args)
}

// securityPresetCommand 是**双轴**写入面的唯一实现（Model 与 runnerCore 共用）。
func securityPresetCommand(policy *permission.Policy, exec *tools.Executor, args string) string {
	id := strings.ToLower(strings.TrimSpace(args))
	switch id {
	case "", "list", "status":
		return securityPresetListing(policy, exec)
	}

	preset, ok := permission.LookupSecurityPreset(id)
	if !ok {
		return securityPresetUnknownNotice(id)
	}
	applySecurityPreset(policy, exec, preset)
	return securityPresetAppliedNotice(preset, exec)
}

// applySecurityPreset 是**唯一**允许同时写两轴的地方（G12 的 AST 白名单里登记在案）。
//
// 顺序：先审批轴、后能力轴。两者互不读取，顺序无语义影响；固定顺序只为可读性。
func applySecurityPreset(policy *permission.Policy, exec *tools.Executor, preset permission.SecurityPreset) {
	if policy != nil {
		// 走 SetModeByName：模式名解析的单一真相源（与 /permissions 同一条路径）。
		policy.SetModeByName(preset.Level.String())
	}
	if preset.SandboxOn {
		sandboxEnable(exec)
	} else {
		sandboxDisable(exec)
	}
}

// securityPresetListing 是**无参**输出：既当"我能选什么"，也当"我现在在哪"。
//
// 它同时承担**轴归属说明**的职责 —— 用户要求 `/permissions` 与 `/sandbox` 的
// 区别必须看得见，而这里正是三条命令并排出现的地方。
func securityPresetListing(policy *permission.Policy, exec *tools.Executor) string {
	var sb strings.Builder
	// 形态 SSOT = bilingual.go（英文主行 + 缩进中文辅助行）。
	sb.WriteString(bi("Security presets — each writes BOTH axes (approval + sandbox)",
		"安全套餐 —— 每个套餐同时写两轴（审批轴 × 能力轴）"))
	sb.WriteString("\n\n")
	sb.WriteString(securityPresetCurrentLine(policy, exec) + "\n\n")
	sb.WriteString(bi("Available:", "可选：") + "\n")
	for _, p := range permission.BuiltinSecurityPresets() {
		// 第一行只放**两轴的实际取值**（可扫读），说明另起一行缩进 ——
		// 不把长说明塞进同一行，否则 TUI 里会被硬换行切碎。
		sb.WriteString(fmt.Sprintf("   %-13s approval=%-18s sandbox=%s\n",
			p.ID, p.Level.String(), sandboxStateWord(p.SandboxOn)))
		sb.WriteString(bi(p.Description, p.DescriptionZH) + "\n")
	}
	sb.WriteString("\n")
	sb.WriteString(bi("Apply: /security-preset <id>", "应用：/security-preset <id>"))
	sb.WriteString("\n\n")
	sb.WriteString(bi("One axis at a time (the two axes are independent):",
		"想只动一轴（两者互不干扰）："))
	sb.WriteString("\n")
	sb.WriteString("   /permissions <read-only|workspace-write|danger-full-access>\n")
	sb.WriteString(bi("approval axis only — does not touch the sandbox",
		"只改审批轴，不含沙箱") + "\n")
	sb.WriteString("   /sandbox on|off\n")
	sb.WriteString(bi("capability (sandbox) axis only — does not touch the approval mode",
		"只改能力轴，不含审批档"))
	return sb.String()
}

// securityPresetCurrentLine 报"当前是哪个预设"，或 `Custom`。
//
// ★ I4：判定对两轴取合取；无匹配时**必须**显示 Custom。
// 若只比审批档，`danger-full-access + 沙箱开` 会被误判成 `unrestricted`
// （沙箱其实开着）——预设名开始骗人，正是 F2 那类"报告与事实不符"换个地方复发。
func securityPresetCurrentLine(policy *permission.Policy, exec *tools.Executor) string {
	level := permission.LevelWorkspaceWrite
	if policy != nil {
		level = policy.Mode()
	}
	sandboxOn := sandboxCapabilityOn(exec)

	if preset, ok := permission.MatchSecurityPreset(level, sandboxOn); ok {
		return biSame("Current: %s — approval=%s, sandbox=%s",
			"当前：%s — 审批=%s，沙箱=%s",
			preset.ID, level.String(), sandboxStateWord(sandboxOn))
	}
	return biSame("Current: Custom — approval=%s, sandbox=%s (no preset matches these two axes)",
		"当前：Custom — 审批=%s，沙箱=%s（两轴组合不属于任何套餐）",
		level.String(), sandboxStateWord(sandboxOn))
}

// securityPresetAppliedNotice 是应用成功的回执：**逐轴**报出实际生效值。
//
// 逐轴列出而不是只说一句"已应用" —— 用户必须能核对"两轴都被写到了什么"，
// 这与 doctor 的分节渲染是同一套世界观（两轴正交，各自如实报）。
func securityPresetAppliedNotice(preset permission.SecurityPreset, exec *tools.Executor) string {
	var sb strings.Builder
	sb.WriteString(biSame("Security preset applied: %s", "已应用安全套餐：%s", preset.ID))
	sb.WriteString("\n")
	sb.WriteString(fmt.Sprintf("   approval axis   : %s\n", preset.Level.String()))
	sb.WriteString(fmt.Sprintf("   capability axis : sandbox=%s [%s]\n",
		sandboxStateWord(preset.SandboxOn), sandboxGateStatus(exec)))
	if !preset.SandboxOn {
		sb.WriteString(fmt.Sprintf("   ⚠️ %s\n", sandboxDisableNotice))
	}
	sb.WriteString(bi("This command wrote BOTH axes. To adjust one axis at a time, use "+
		"/permissions <mode> (approval) and /sandbox on|off (capability).",
		"本命令同时写了两轴；想分开微调请用 /permissions <mode>（审批轴）与 /sandbox on|off（能力轴）。"))
	return sb.String()
}

// securityPresetUnknownNotice 是未知 ID 的回执。
//
// 不静默回退到任何默认预设（那会变成"打错字 = 悄悄改安全档"）；
// 并顺带重申命名空间纪律，避免用户拿审批档名来当预设名。
func securityPresetUnknownNotice(id string) string {
	ids := make([]string, 0, 3)
	for _, p := range permission.BuiltinSecurityPresets() {
		ids = append(ids, p.ID)
	}
	available := strings.Join(ids, " | ")
	en := fmt.Sprintf("Unknown security preset: %s\n"+
		"   Available: %s\n"+
		"   Preset ids deliberately never reuse permission-mode names "+
		"(read-only / workspace-write / danger-full-access) —\n"+
		"   those are inputs for /permissions <mode>: approval axis only, sandbox untouched.",
		id, available)
	zh := fmt.Sprintf("未知安全套餐：%s\n"+
		"   可选：%s\n"+
		"   预设 id 刻意不复用审批轴的单轴档名（read-only / workspace-write / danger-full-access）——\n"+
		"   那些是 /permissions <mode> 的输入，只改审批轴，不含沙箱。",
		id, available)
	return bi(en, zh)
}

// sandboxStateWord 是能力轴状态的统一措辞（footer / 预设输出 / listing 共用一处）。
func sandboxStateWord(on bool) string {
	if on {
		return "on"
	}
	return "off"
}
