// sandbox_toggle.go — `/sandbox on|off` 的**两套门同生同灭**（决策项 A1，2026-09-15）。
//
// # 缺陷（移交单 G0-002，实测）
//
// `/sandbox off` 只翻了 legacy 门 `Executor.Sandbox.SetEnabled(false)`，而
// `internal/tools/bash.go:987-991` 的 Manager 路径**只认**
// `e.SandboxMgr != nil && e.SandboxMgr.Level() != SandboxLevelDisabled`，
// **完全不读** `Sandbox.Enabled()`：
//
//	if e.Sandbox != nil && e.Sandbox.Enabled() { ... } // ← 只加了一层校验
//	if e.SandboxMgr != nil { level := ...; ... } // ← 与上面的开关无关，照跑
//
// ⇒ 用户敲 `/sandbox off` 后 TUI 报告 "Sandbox OFF"，命令**仍被 Manager 门校验并拦截**。
// 这是 A1「两套门不一致」唯一还看得见的用户级后果（`G0_STATUS.md` §5.2）。
//
// # 拍板语义（`集中拍板_决策表与并行修复分组_2026-09-14.md` §2 A1 = Opt-C）
//
// - 保留 opt-in（不默认 ON）；
// - `/sandbox off` **同步清 `SandboxMgr`**，使两门同灭；
// - 未启用时危险命令走 **T1**：降级到权限层 + **告警**（族A「缺的是能力 → T1」）。
//
// # 为什么恢复时复用 `EnableProductionSandbox`
//
// `Executor.EnableProductionSandbox` 是本仓既定的**唯一生产启用入口**
// （`internal/sandbox/production.go` 头注释：TUI lifecycle 与 runner_core 共用，
// 「避免两处平台开关逻辑漂移」）。`/sandbox on` 若自己拼 `SetEnabled + SetSandboxMgr +
// SyncHookSandboxManager`，等于在 TUI 里再造第二条启用路径——正是该注释要防的漂移。
// 因此这里只调用它，不复制它的接线步骤。
package tui

import (
	"fmt"
	"strings"

	"github.com/anchorandfish/whale-code/internal/permission"
	"github.com/anchorandfish/whale-code/internal/sandbox"
	"github.com/anchorandfish/whale-code/internal/tools"
)

// sandboxUnknownArgNotice 是出口③「敲错参数回用法」的**唯一**实现
// （：两个 UI 的 handleSandbox default 分支共用本函数，防双实现漂移）。
//
// # 为什么要有这一句（可发现性三出口）
//
// 用户能"看见"一条子命令只有三个出口：`/` 弹窗（按注册名前缀匹配）、/help 的 Usage 行、
// 命令自己的回执。此前敲错参数只会无声返回状态输出 —— 用户永远得不到"合法参数有哪些"
// 的提示（对照 /mouse 敲错即回 "Usage: /mouse on|off|status" 的既有形态）。
//
// 合法输入（on/off/status/空）必须返回空串 —— 判据
// TestSandboxUnknownArgReturnsUsageHint 的反向半边钉住"不得误伤合法输入"。
func sandboxUnknownArgNotice(args string) string {
	switch strings.ToLower(strings.TrimSpace(args)) {
	case "", "on", "off", "status":
		return ""
	}
	return bi(
		"Unknown argument: "+args+" — Usage: /sandbox [on|off|status]",
		"未知参数："+args+"（用法：/sandbox [on|off|status]）",
	)
}

// sandboxDisable 关闭**两套门**（A1）。
//
// `SetSandboxMgr(nil)` 是 A1 的核心：`tools/bash.go` 的路径 1 因此不再进入，
// 命令落到路径 2/3（无 Manager 门）。此时沙箱能力缺失，危险命令由**权限层**兜底
// —— 即族A 的 T1，调用方必须把这一降级对用户可见（见 sandboxDegradeNotice）。
func sandboxDisable(exec *tools.Executor) {
	if exec == nil {
		return
	}
	if exec.Sandbox != nil {
		exec.Sandbox.SetEnabled(false)
	}
	exec.SetSandboxMgr(nil)
}

// sandboxDisableNotice 是 T1 降级的**可见出口**（族A：降级不得静默）。
//
// 形态 = bilingual.go（英文主行 + 缩进中文辅助行）⇒ 因此是 var 而非 const。
var sandboxDisableNotice = bi(
	"Sandbox disabled: dangerous commands now rely on the permission layer only (T1 degrade)",
	"沙箱已关闭：危险命令改由权限层兜底（T1 降级）")

// sandboxEnable 打开**两套门**（A1），并保持 legacy / Manager / hookMgr 三者同源。
func sandboxEnable(exec *tools.Executor) {
	if exec == nil {
		return
	}
	exec.EnableProductionSandbox(exec.WorkDir())
}

// sandboxGateStatus 报告**两套门各自的真实状态**（R4：读结构化事实，不解析散文文案）。
//
// 此前 status 分支只读 `Sandbox.Enabled()` ⇒ 会同时漏报两种不一致
// （"报告 OFF 实际 ON" 与 "报告 ON 实际无 Manager"）。这里两条都报。
//
// ★ ：`manager=` 此前只报**配置等级**（`Level().String()`），
// 而等级是"用户要多少"，不是"实际拦不拦得住" —— 于是"用户开了、本机装不上"时
// 这里报 `manager=strict`，与 banner 的 `degraded` **互相矛盾**，且让 I-3.1 的
// A/B 在 `/sandbox status` 上同形。现在补上强制事实：装不上时追加 `(unenforced)`。
// 追加而非改写 ⇒ 既有按 `manager=` / `manager=none` 检索的判据不受影响。
func sandboxGateStatus(exec *tools.Executor) string {
	if exec == nil {
		return "legacy=unknown, manager=unknown"
	}
	legacy := "off"
	if exec.Sandbox != nil && exec.Sandbox.Enabled() {
		legacy = "on"
	}
	manager := "none"
	if exec.SandboxMgr != nil {
		manager = exec.SandboxMgr.Level().String()
		if !sandbox.EnforcementActive(exec.SandboxMgr) {
			manager += "(unenforced)"
		}
	}
	return fmt.Sprintf("legacy=%s, manager=%s", legacy, manager)
}

// sandboxCapabilityOn 是**能力轴**的单一结构化判定（R4：不解析任何文案）。
//
// A1 保证 `/sandbox on|off` 让 legacy 门与 Manager 门**同生同灭**，因此 legacy 门
// 的状态即能力轴的总状态；两套门的**细节**由 sandboxGateStatus 提供。
// 抽成函数的理由：此前同一表达式散在三处（doctor、Model status 分支、runner status 分支），
// 正是"两处同形各自维护再漂移"的高发形态（本文件头注释记录过同类事故）。
//
// ★ —— 本函数回答的是**意图**（"用户要沙箱吗"），
// **不是事实**（"本机拦得住吗"），两者故意分开：
// - 预设匹配（`MatchSecurityPreset`）、`/security-preset` 回执等按**意图**工作，
// 不受"本机装不上"影响（否则用户开着的沙箱会莫名显示成"未启用"）；
// - **对用户的宣称**走 `sandboxAxisBadge` / `sandboxGateStatus`，那里**必须**再读
// `sandbox.EnforcementActive`（事实），装不上时不得报 `on`。
func sandboxCapabilityOn(exec *tools.Executor) bool {
	return exec != nil && exec.Sandbox != nil && exec.Sandbox.Enabled()
}

// sandboxAxisBadge 是状态栏用的**短**能力轴标签（详情走 /sandbox 或 /permissions doctor）。
//
// 三态，缺一不可：
//
//	off (T1) —— 用户关了（`/sandbox off`）：危险命令降级到权限层（族A 的 T1）。
//	on —— 用户开了**且本机真的在强制**（`sandbox.EnforcementActive`）。
//	degraded —— 用户开了，但本机**装不上**（如 `sandbox_apply` 被拒 / 平台不提供文件系统约束）。
//
// ★ ：修复前只有两态，"用户开了"就直接报 `on` ——
// 而"开了"只是**意图**，不是**事实**。在装不上的机器上那条 `on` 就是**谎报隔离**
// （`人工验收_基础设施_I-3` I-3.1：B 格"不得出现 `Sandbox: on`"）。
// 这里引入第三态后，`on` 只在下游真能拦得住时才出现 ⇒ 判据面可判别。
//
// ⚠️ `EnforcementActive` 的口径（复用审批轴同一判据，单一事实源）：网络被显式放行
// （`WHALE_SANDBOX_ALLOW_NETWORK=1`）或平台只做资源限制（windows，GAP-WIN-P3）时亦为
// false —— 那不是"没开"，而是"**关不住**"，所以同样不得报 `on`。
func sandboxAxisBadge(exec *tools.Executor) string {
	if !sandboxCapabilityOn(exec) {
		return "off (T1)"
	}
	if !sandbox.EnforcementActive(exec.SandboxMgr) {
		return "degraded"
	}
	return "on"
}

// sandboxAxisNotice 是 `/sandbox` 的**轴归属声明**（G12，2026-09-16 拍板 Opt-A）。
//
// # 为什么必须显式写出来
//
// `/sandbox` 与 `/permissions` 极易被当成同一件事（"那个权限开关"），但它们是两个
// **正交**轴：`/sandbox` 写能力轴（OS 隔离，"拦不拦得住"），`/permissions` 写审批轴
// （"问不问"）。此前 `/sandbox` 还**顺带写审批轴**（`on` → read-only；
// `off` → 回落 `cfg.PermissionMode`，默认 workspace-write），把这个误解坐实成了事实：
//
//	① 取值来源错：`cfg.PermissionMode` 是**本次启动的初始档**、不是"上一档"
//
// ——`/permissions` 的档位切换根本不落盘（只改内存 Policy），
// 所以它从定义上就不可能是"上一档"；
//
//	② 不可逆：danger 用户 `on`→read-only→`off`→workspace-write，原档**永久丢失**；
//
// `danger + 沙箱关`（= 同类 CLI 的 Full Access）在 UI 上因此不可达；
//
//	③ fail-open：read-only 用户敲 `off`，**写入被放开** —— 命令名承诺"减少限制"，
//
// 实际在另一个轴上扩权。
//
// 拍板：Opt-A —— `/sandbox on|off` **只翻能力轴**，审批轴由 `/permissions` 独占。
// 想要"一键两轴"的用户走 `/security-preset`（名字声明写两轴，见 security_preset.go）。
//
// # 为什么由调用方读档位再传入
//
// R4：`mode` 必须是调用方从 `Policy().Mode()`（结构化事实）读出后传入的，
// 本函数不解析任何文案、也不自己去猜档位。
//
// # 2026-09-21 补充：为什么必须把「跨轴**后果**」也写出来
//
// 原文只声明「审批轴不受影响 / the approval axis is untouched」。该句在**档位层为真**
// （`SetMode` 确实没被调用），但在**单次调用的结论层为假**：`/sandbox off` 会让
// `sandboxBoundedTools`（`bash` / `Monitor`）失去免问前提（出口 (c)），
// 于是 workspace-write 档下**每一次** shell 调用都重新弹确认。
// 用户据此把「关沙箱」读成「减少限制」—— 方向恰好相反（真机手测暴露，见
// `I-2手测_弹窗与幻觉告警_取证回执_2026-09-21.md`）。
//
// ⇒ 修法：**保留**轴归属声明（G12 契约，`g12_axis_ownership_test.go:144` 明示不得删），
// **追加**按档位分叉的后果句（`sandboxShellExemptionNote`）。
func sandboxAxisNotice(mode permission.Level) string {
	enNote, zhNote := sandboxShellExemptionNote(mode)
	return biSame(
		"This command changes the sandbox (capability axis only) — the approval axis is untouched. "+
			"Current approval mode: %s (switch it with /permissions <mode>)."+enNote,
		"本命令只改能力轴（沙箱），审批轴不受影响。当前审批档：%s；改档请用 /permissions <mode>。"+zhNote,
		mode)
}

// sandboxShellExemptionNote 返回「关掉沙箱之后会发生什么」的英/中两句，**按档位分叉**。
//
// # 为什么不能是一句万能话（这是本轮的关键判断）
//
// 三档下「关沙箱的后果」**互不相同**，由 `permission.checkModeLocked`
// （`internal/permission/permission.go:201-247`）决定；而 `bash` / `Monitor` 的档位是
// **静态定级**（`requiredForTool` 只对 `Agent` 走输入感知，见 `:335-343`）⇒ 三档分别是：
//
//	read-only : required=workspace-write > read-only ⇒ (false,false) **拒绝**
//
// ⇒ 关沙箱**不改变询问**（本来就问不到）
//
//	workspace-write : 未免问 ⇒ (true, required>read-only) = (true,true) ⇒ **每次调用都要确认**
//	danger-full-access : (true,false) ⇒ 本来就不问；关沙箱**不会多弹**，抽走的是背后的隔离
//
// 因此这句话**不能**塞进 `sandboxDisableNotice`：那个 var **无 `mode` 参数**、被 4 处
// mode-blind 语境复用（`commands_plugin.go` · `runner_core.go` · `security_preset.go` ·
// `commands_config.go`），在 `unrestricted`（= danger 档 + 沙箱关）的场景会**说出假话**。
// 唯一带 `mode` 的相关文案函数就是本族 ⇒ 落点在这里。
//
// ★ 三档「确实不同」不是注释声称的，而由**事实前置判据**钉住：
// `sandbox_axis_notice_test.go` 的 `TestSandboxAxisNotice_Premise_ModesDiffer`
// 用 `Policy.Check("bash"/"Monitor", …)` 实测三档裁决。事实若变，该判据先红。
//
// ★ 顺带记录一处**潜在**不一致（本轮不改、未登记）：能力表用**小写** `"monitor"`
// （`internal/permission/capability.go:307`，其守卫断言大小写不敏感），
// 而定级表 `LevelOf` 是**精确名匹配**，注册名是 `Monitor`（`internal/tools/tools.go:921`）
// ⇒ 只有生产名可达正确档位；小写形态会 fail-closed 到最高档。生产调用方一律传注册名，
// 故当前不可达。详见本轮回执。
func sandboxShellExemptionNote(mode permission.Level) (en, zh string) {
	switch mode {
	case permission.LevelReadOnly:
		return "\nNote: this mode already blocks shell commands, so turning the sandbox off " +
				"does not change what is asked here.",
			"\n注意：该档下 shell 类命令本来就被拒绝，关沙箱不改变此处的询问。"
	case permission.LevelDangerFullAccess:
		return "\nNote: this mode does not confirm regular operations either way — turning the " +
				"sandbox off removes the isolation behind them rather than adding prompts.",
			"\n注意：该档下常规操作本来就不确认；关掉沙箱抽走的是背后的隔离，而不是多出确认。"
	default:
		return "\nNote: shell commands (bash / Monitor) are exempt from confirmation only while " +
				"the sandbox is enforced — with the sandbox off, each call is confirmed again.",
			"\n注意：shell 类命令（bash / Monitor）的免问以「沙箱在强制」为前提；沙箱关闭后每次调用都会重新确认。"
	}
}

// approvalAxisNotice 是 sandboxAxisNotice 的**镜像**：说明 `/permissions` 的轴归属。
//
// # 为什么必须与 sandboxAxisNotice 同文件、同形状
//
// 用户的原始困惑正是文案面造成的：`permissions` 这个词**天然**读起来像
// "总的权限开关"，而它在本项目里只写审批轴、完全不碰 OS 隔离。所以
// 「一轴一写者」（I1）在代码层落地之后，还必须在**文案层**再落地一次，
// 且两个方向必须成对——只写一侧会让用户以为"另一侧也顺手改了"。
//
// # 为什么参数是 (mode, exec) 而不是各 UI 自己拼
//
// 本项目有两个 UI：TUI（`Model.handlePermissionsCommand`）与 `--plain`
// （`runnerCore.handlePermissions`）。修复前两侧各写各的文案，
// 结果 `--plain` 一侧完全没有轴归属（2026-09-16 真机探针实证）。
// 把文案收进本函数 = 唯一真相源，两个 UI 都只能引用它，无法各自漂移。
//
// 契约：调用方传**已读出**的结构化事实（R4），本函数不解析任何散文。
func approvalAxisNotice(mode permission.Level, exec *tools.Executor) string {
	sandbox := sandboxStateWord(sandboxCapabilityOn(exec))
	return biSame(
		"This command writes the approval axis only — it does not touch the sandbox (capability axis). "+
			"To change the sandbox use /sandbox on|off or /security-preset. "+
			"Current: approval=%s, sandbox=%s.",
		"本命令只写审批轴，不碰沙箱（能力轴）；要改沙箱请用 /sandbox on|off 或 /security-preset。"+
			"当前：审批=%s，沙箱=%s。",
		mode, sandbox)
}

// unknownApprovalModeNotice 生成"审批档名不认识"的回执。
//
// ★ 为什么不能只回一句 `Unknown mode: xxx`
//
// `/permissions` 是**最容易被当成"总的权限开关"**的那条命令（用户原始反馈）。
// 而它的两个合法输入域是**互斥命名空间**（I2）：
//
//	/permissions <read-only|workspace-write|danger-full-access> —— 单轴：只写审批
//	/security-preset <safe|standard|unrestricted> —— 双轴：同时写两轴
//
// 用户敲 `/permissions safe`（把预设 id 当成档名）时，如果只回"Unknown mode"，
// 那就等于**把"两套 id 各管什么"的困惑原样还给用户** —— 恰恰是本轮要消除的东西。
// 所以这里做一次**指路**：认出这是预设 id 就明确说"它写两轴，请走 /security-preset"。
//
// 两个 UI（TUI 的 commands_config.go 与 --plain 的 runner_core.go）共用本函数
// —— 的教训就是"同一命令两处实现必然会漂移"，这里不再重犯。
func unknownApprovalModeNotice(arg string) string {
	if p, ok := permission.LookupSecurityPreset(arg); ok {
		return biSame(
			"Unknown mode: %s\n"+
				"   `%s` is a security-preset id — it writes BOTH axes (approval + sandbox), "+
				"so it is not an approval mode name.\n"+
				"   Use /security-preset %s (run /security-preset with no args to pick from a list).",
			"未知审批档：%s\n"+
				"   `%s` 是安全套餐 id（同时写两轴：审批 + 沙箱），不属于审批档名。\n"+
				"   请用 /security-preset %s（无参可打开选择列表）。",
			arg, arg, p.ID)
	}
	return biSame(
		"Unknown mode: %s\n   Available: read-only, workspace-write, danger-full-access",
		"未知审批档：%s\n   可选：read-only、workspace-write、danger-full-access",
		arg)
}
