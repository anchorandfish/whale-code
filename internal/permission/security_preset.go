package permission

// security_preset.go — 「审批轴 × 能力轴」的**具名组合**（预设层）。
//
// # 为什么需要这一层
//
// 本仓有两个**正交**轴（详见 `internal/tui/commands_config.go` 的 doctor 注释）：
//
//	审批轴（ask/deny 策略）：Policy.Mode()        —— 决定"问不问"
//	能力轴（OS 隔离）      ：Executor.Sandbox/Mgr —— 决定"拦不拦得住"
//
// 单轴写入面各管一轴：`/permissions <mode>` 只写审批轴，`/sandbox on|off` 只写能力轴。
// 但存在一类**正当的双轴诉求**——"一键变安全"/"一键完全放开"。此前它被错误地
// 挂在 `/sandbox` 上（`/sandbox on` 顺手把审批档改成 read-only），这违反
// 「写入点的名字必须声明它写哪些轴」（见 `internal/tui/security_preset.go` 的 I2）。
//
// 预设层就是给这类诉求一个**合法的家**：名字声明"整体安全姿态"，一次写两轴。
// 对标同类 CLI 的 ``
// （`builtin_approval_presets()`——"pairing an approval policy with a permission
// profile"），本文件同样保持 **UI 无关**，只描述"是什么"，不描述"怎么应用"。
//
// # ★ I2 命名纪律：ID 不得复用单轴 SSOT token
//
// 单轴 token 的唯一真相源是 `LookupModeName`（read-only / workspace-write /
// danger-full-access，外加历史别名）。预设 ID **必须与它们不相交**，否则同一个
// 词在两个入口指不同事物（`/permissions workspace-write` = 只设审批档，
// `/security-preset workspace-write` = 双轴），比"名字只说一轴却写两轴"更难排查。
//
// 同类 CLI 做了同一件事，只是用前缀区分：它的权限档 ID 带 `:` 前缀
// （`:read-only` / `:workspace` / `:danger-full-access`，`protocol/src/models.rs:396,399,402`，
// 注释写明 "Reserved identifier"），而预设 ID 无前缀
// （`read-only` / `auto` / `full-access`，`approval-presets/src/lib.rs:31,41,51`）。
// ★ 注意中间那个：它应用的 profile 是 `PermissionProfile::workspace_write()`，
// 预设 ID 却**刻意叫 `auto` 而不是 `workspace-write`**；第三个同理叫 `full-access`
// 而权限档叫 `danger-full-access`。两层命名空间是**故意错开**的。
//
// 本仓不引入 `:` 前缀约定（改动面太大），改用**自造词**表达"整体姿态"。
// 该约束由 `TestSecurityPresetIDsDoNotCollideWithModeNames` 机器校验。

// SecurityPreset 是「审批档 + 沙箱开关」的一个具名整体。
//
// 字段语义与同类 CLI 的 `ApprovalPreset`（approval + permission_profile）一一对应。
type SecurityPreset struct {
	// ID 是稳定标识符，也是 `/security-preset <id>` 接受的输入。
	// ★ 不得与 LookupModeName 认得的任何 token 相同（I2）。
	ID string
	// Label 是 UI 展示名（英文为主，与同类 CLI 的 label 同风格）。
	Label string
	// Description 必须**把所写的两轴都说清**——这是 I2 对"声明"的要求：
	// 名字可以短，但描述里两轴都要出现，否则用户无法判断它到底改了什么。
	//
	// ★ 2026-09-18 起**只装英文**（主文案）；中文释义走 DescriptionZH。
	// 拆成两个字段、而不是把「英文 / 中文」拼在同一个串里：拼串会逼渲染方
	// 去猜分隔符才能分行（= 从散文推断结构），那是 R4 明令禁止的形态。
	Description string
	// DescriptionZH 是 Description 的中文辅助层（按需显示，缩进另起一行）。
	DescriptionZH string
	// Level 是本预设要写入的**审批档**。
	Level Level
	// SandboxOn 是本预设要写入的**能力轴**状态。
	SandboxOn bool
}

// BuiltinSecurityPresets 返回内置预设表（3 条，对标同类 CLI 的三档）。
//
// Description 是**单行、可直接打印**的说明（供 `/security-preset` 的 listing 用；
// 不做超长句子 —— TUI 没有弹窗式换行，长句会把输出撑坏）。
// 每条都必须把**两轴**都说清（I2 对"声明"的要求）。
//
// 三条恰好覆盖 3 个状态；两轴共 6 态，故另 3 态**无预设**——调用方必须显式
// 显示为 `Custom`，不得四舍五入到最近预设（见 MatchSecurityPreset 的 I4 说明）。
func BuiltinSecurityPresets() []SecurityPreset {
	return []SecurityPreset{
		{
			ID:            "safe",
			Label:         "Safe",
			Description:   "Read-only approval + sandbox ON: writes blocked, no unapproved escape.",
			DescriptionZH: "审批 read-only，沙箱开",
			Level:         LevelReadOnly,
			SandboxOn:     true,
		},
		{
			ID:            "standard",
			Label:         "Standard",
			Description:   "Workspace writes ask for confirmation; dangerous commands still blocked.",
			DescriptionZH: "审批 workspace-write，沙箱开（启动默认）",
			Level:         LevelWorkspaceWrite,
			SandboxOn:     true,
		},
		{
			ID:            "unrestricted",
			Label:         "Unrestricted",
			Description:   "Nothing asks and nothing isolates. Exercise caution.",
			DescriptionZH: "审批 danger-full-access，沙箱关（两轴全放开，无任何拦截）",
			Level:         LevelDangerFullAccess,
			SandboxOn:     false,
		},
	}
}

// LookupSecurityPreset 按 ID 查预设。第二个返回值为 false 表示 ID 未知
// （调用方必须报错，**不得**静默回退到某个默认预设）。
func LookupSecurityPreset(id string) (SecurityPreset, bool) {
	for _, p := range BuiltinSecurityPresets() {
		if p.ID == id {
			return p, true
		}
	}
	return SecurityPreset{}, false
}

// MatchSecurityPreset 回答"当前这个状态是哪个预设"。
//
// ★ I4：判定必须对**两轴取合取**（Level ∧ SandboxOn），且无匹配时返回 false。
//
// 为什么不能"只比审批档"：两轴共 6 态而预设只有 3 条，若只比审批档，
// `danger-full-access + 沙箱开` 会被判成 `unrestricted`（沙箱其实是开的）
// ——预设名开始骗人，正是 F2 那类"报告与事实不符"换个地方复发。
// 调用方拿到 false 必须显示 `Custom`，不得四舍五入到最近预设。
func MatchSecurityPreset(level Level, sandboxOn bool) (SecurityPreset, bool) {
	for _, p := range BuiltinSecurityPresets() {
		if p.Level == level && p.SandboxOn == sandboxOn {
			return p, true
		}
	}
	return SecurityPreset{}, false
}
