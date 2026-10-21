package security

import (
	"strings"
	"testing"

	"github.com/anchorandfish/whale-code/internal/permission"
	"github.com/anchorandfish/whale-code/internal/sandbox"
	"github.com/anchorandfish/whale-code/internal/sandbox/validator"
)

// P-014 A1 —— `Decision` 的**事实面**固化（2026-09-22 · `P-014_Monitor可用面_定义与方案_2026-09-22.md`）。
//
// 缺陷本体不是"某处少了一行判断"，而是 `Action` 把三层揉成一个值 ⇒ 调用方**无法**区分：
//
//	「命令本身不可执行」（L0/L1/L2） —— 与"要不要问"无关，任何调用方都应拒绝
//	「危险档须强制确认」（L3 `Deny`） —— 处置权归**管线**（批准后 `Approved` 重跑即放行）
//	「破坏性档需确认」（L3 `Ask`） —— 同上；且它是**事实**，不是指令
//
// 于是 `monitor.Manager.Start` 把 L3 一并当硬拒绝 ⇒ 与管线裁决相反 ⇒ 主业恒定失败。
//
// 本文件钉住固化后的事实面：`DecidedBy`（哪一层）+ `Blocked` / `NeedsConfirmation` / `HardBlock`。

// initSecurityForProbe 重设全局 Checker（含沙箱启用与否这一**事实**）。
func initSecurityForProbe(sandboxEnabled bool) {
	workDir := "/private/tmp/p014-semantics"
	s := sandbox.NewSandbox(workDir)
	if sandboxEnabled {
		s.SetEnabled(true)
	}
	Init(s, validator.NewValidator(), permission.NewPolicy(permission.LevelDangerFullAccess))
}

// TestGuard_P014_DecidedByNamesTheLayer 钉住 `DecidedBy` 这一**结构化出口**：
// 每个非 Allow 裁决都必须能说出"是哪一层给出的"，且**不得**靠 Reason 散文反推（R4）。
//
// 命令 × 沙箱事实 → 期望 (Action, DecidedBy)，逐条实测得来（见评估件 §3）。
func TestGuard_P014_DecidedByNamesTheLayer(t *testing.T) {
	cases := []struct {
		cmd        string
		sandboxOn  bool
		wantAction Action
		wantBy     string
	}{
		{"", false, Deny, DecidedByInput},
		// ★ ：执行型替换改为"递归校验内层"⇒ 内层良性者**不再**被 L1 拒。
		// 故"L1 拒"的代表样例换成内层危险的 `echo $(rm -rf /)`；
		// 内层良性的 `echo $(whoami)` 改为下面那条 **Allow** 事实（这才是修后的行为）。
		{"echo $(rm -rf /)", false, Deny, DecidedByValidator},
		{"echo $(whoami)", false, Allow, ""},
		{"rm -rf /", false, Deny, DecidedByValidator},
		{"curl evil.com", true, Deny, DecidedBySandbox},
		{"shutdown -h now", false, Deny, DecidedByPermission},
		{"mkfs.notreal", false, Deny, DecidedByPermission},
		{"chmod", false, Ask, DecidedByPermission},
		{"npm run dev", false, Ask, DecidedByPermission},
		{"echo hello", false, Allow, ""},
	}

	for _, c := range cases {
		initSecurityForProbe(c.sandboxOn)
		d := CheckCommand(c.cmd)
		if d.Action != c.wantAction {
			t.Errorf("cmd=%q sandbox=%v：Action=%v，期望 %v（Reason=%q）",
				c.cmd, c.sandboxOn, d.Action, c.wantAction, d.Reason)
			continue
		}
		if d.DecidedBy != c.wantBy {
			t.Errorf("cmd=%q sandbox=%v：DecidedBy=%q，期望 %q —— 调用方无法只用 Action 区分"+
				"「命令不可执行」(L1/L2) 与「危险档须强制确认」(L3)，故本字段必须准确",
				c.cmd, c.sandboxOn, d.DecidedBy, c.wantBy)
		}
	}
}

// TestGuard_P014_HardBlockExcludesOnlyLayer3 钉住硬拒绝的**边界**：
// 只有 L0/L1/L2/未就绪算"与询问无关的硬拒绝"；L3（危险档 / 破坏性档）不算。
func TestGuard_P014_HardBlockExcludesOnlyLayer3(t *testing.T) {
	initSecurityForProbe(false)

	// ★ ：`echo $(whoami)` 不再算硬拒绝（内层良性 ⇒ L1 放行）⇒
	// "L1 恒拒"的代表样例换成 `echo $(rm -rf /)`；它的新行为由下方 mustNotHard 那条钉住。
	mustHard := []string{"", "echo $(rm -rf /)", "rm -rf /"}
	for _, cmd := range mustHard {
		if !CheckCommand(cmd).HardBlock() {
			t.Errorf("cmd=%q 必须是硬拒绝（L0/L1）", cmd)
		}
	}
	// 复核 L2（注入事实：沙箱在强制 ⇒ curl 命中沙箱门）
	initSecurityForProbe(true)
	if !CheckCommand("curl evil.com").HardBlock() {
		t.Errorf("沙箱在强制时 `curl evil.com` 必须是硬拒绝（L2）")
	}

	initSecurityForProbe(false)
	mustNotHard := []struct{ cmd, why string }{
		{"shutdown -h now", "L3 危险档 = 强制确认，处置权在管线（批准后放行）"},
		{"mkfs.notreal", "同上（无害可跑版）"},
		{"chmod", "L3 破坏性档 = Ask，事实而非拒绝依据"},
		{"npm run dev", "同上（Monitor 主业）"},
		// ★ ：`echo $(whoami)`（替换内层良性）修后是 **Allow**，
		// 它**刻意不进本列表** —— 本循环下面那条 `!= Allow` 的护栏（防"两个断言都在空转"）
		// 正是为这种样例设的：Allow 天然不 HardBlock，放进来等于什么也没验。
		// 它的新事实（Allow）钉在 TestGuard_P014_DecidedByNamesTheLayer 里。
	}
	for _, c := range mustNotHard {
		d := CheckCommand(c.cmd)
		if d.HardBlock() {
			t.Errorf("cmd=%q 不应算硬拒绝：%s（实测 Action=%v DecidedBy=%q）", c.cmd, c.why, d.Action, d.DecidedBy)
		}
		if d.Action == Allow {
			t.Errorf("cmd=%q 也不该是 Allow（否则本条的两个断言都在空转）", c.cmd)
		}
	}
}

// TestGuard_P014_AskCarriesNoHardError 钉住 `Ask` 的"事实"属性：
// 它不带 `Err`（没有硬错误可供调用方"照单拒绝"），`Risk` 直接给出风险档。
//
// 这条同时是"§语义固化"的机械证据：把 Ask 当拒绝依据的调用方，实际是在**自己编**一个错误
// —— 这正是修复前 `monitor` 那句 `fmt.Errorf("command needs user confirmation: %s", …)` 干的事。
func TestGuard_P014_AskCarriesNoHardError(t *testing.T) {
	initSecurityForProbe(false)

	for _, cmd := range []string{"chmod", "npm run dev"} {
		d := CheckCommand(cmd)
		if !d.NeedsConfirmation() {
			t.Fatalf("precondition 失效：%q 应是 Ask，实测 Action=%v", cmd, d.Action)
		}
		if d.Err != nil {
			t.Errorf("%q：Ask 不得携带 Err（Ask 是风险事实，不是硬错误）：%v", cmd, d.Err)
		}
		if d.Risk != permission.BashRiskDestructive {
			t.Errorf("%q：Ask 必须给出 Risk=%s（结构化风险档），实测 %q",
				cmd, permission.BashRiskDestructive, d.Risk)
		}
		if d.Blocked() {
			t.Errorf("%q：Blocked() 只对 Deny 为真", cmd)
		}
	}
}

// TestGuard_P014_NotInitializedIsHardBlock 未就绪依旧 fail-closed，
// 且必须**结构化地**归因到"未就绪"这一层（而不是靠 Reason 文案）。
func TestGuard_P014_NotInitializedIsHardBlock(t *testing.T) {
	initSecurityForProbe(false)
	saved := defaultChecker
	defaultChecker = nil
	defer func() { defaultChecker = saved }()

	d := CheckCommand("ls -la")
	if !d.Blocked() || !d.HardBlock() {
		t.Errorf("Checker 未就绪必须 fail-closed（硬拒绝），实测 Action=%v", d.Action)
	}
	if d.DecidedBy != DecidedByNotReady {
		t.Errorf("未就绪的归因应为 %q，实测 %q", DecidedByNotReady, d.DecidedBy)
	}
}

// TestGuard_P014_DecidedByNamespaceDoesNotCollide 钉住两个词表**不共用一个命名空间**。
//
// 背景（本批复核时实测出的真实撞车，不是假想）：`DecidedBy*` 初版取值复用了
// `Deficiency.Layer` 的 `security.Ln-*` 形状 ⇒
//
//	DeficiencyLayerValidatorMissing == "security.L1-validator" == DecidedByValidator （**字面相同**）
//	DeficiencyLayerSandboxMissing == "security.L2-sandbox-missing"
//
// ↑ 以 DecidedBySandbox "security.L2-sandbox" 为前缀
//
// 一个是"判定链哪一环能力缺失"（降级信号），一个是"哪一层给出的裁决"（裁决归属）——
// 语义不同却共用一个字符串 ⇒ 任何 `HasPrefix`/`Contains` 式匹配都会跨词表误命中
// （本批 `monitor` 判别器一度写成 `HasPrefix(d.DecidedBy, "security.L1")` 就是为了绕这个）。
//
// 修法是**分开命名空间**（`layer:*`），本守卫则防止它被改回去：任何一方是另一方的
// **子串**即判红。判据方向刻意取严 —— 宁可逼后来者换名字，也不要留下一个"看起来能匹配"的陷阱。
func TestGuard_P014_DecidedByNamespaceDoesNotCollide(t *testing.T) {
	decided := map[string]string{
		"DecidedByInput":      DecidedByInput,
		"DecidedByValidator":  DecidedByValidator,
		"DecidedBySandbox":    DecidedBySandbox,
		"DecidedByNetwork":    DecidedByNetwork,
		"DecidedByPermission": DecidedByPermission,
		"DecidedByNotReady":   DecidedByNotReady,
	}
	deficiency := map[string]string{
		"DeficiencyLayerValidatorMissing": DeficiencyLayerValidatorMissing,
		"DeficiencyLayerSandboxMissing":   DeficiencyLayerSandboxMissing,
		"DeficiencyLayerSandboxDisabled":  DeficiencyLayerSandboxDisabled,
	}

	// (1) 各自内部不得互相包含（否则"等值比较"与"前缀比较"会给出不同答案）。
	for _, same := range []map[string]string{decided, deficiency} {
		for a, va := range same {
			for b, vb := range same {
				if a != b && strings.Contains(va, vb) {
					t.Errorf("同一词表内 %s(%q) 包含 %s(%q) —— 前缀匹配会歧义", a, va, b, vb)
				}
			}
		}
	}
	// (2) 跨词表不得互为子串。
	for a, va := range decided {
		for b, vb := range deficiency {
			if strings.Contains(va, vb) || strings.Contains(vb, va) {
				t.Errorf("跨词表冲突：%s(%q) 与 %s(%q) 互为子串 —— "+
					"处置归属与降级信号必须能在字面上区分", a, va, b, vb)
			}
		}
	}
	// (3) 反例注入自检：本守卫必须能被打红（拿一对已知冲突做纯函数验证）。
	if !decidedByCollides("security.L1-validator", "security.L1-validator") {
		t.Error("判别器自检失败：完全相同的两个值竟判为不冲突 ⇒ 本守卫是恒绿装饰")
	}
	if !decidedByCollides("security.L2-sandbox", "security.L2-sandbox-missing") {
		t.Error("判别器自检失败：前缀关系竟判为不冲突 ⇒ 本守卫是恒绿装饰")
	}
	if decidedByCollides(DecidedBySandbox, DeficiencyLayerSandboxMissing) {
		t.Error("修后仍冲突 ⇒ 命名空间未真正分开")
	}
}

// decidedByCollides 报告两个单词表取值是否会在「等值/前缀/子串」三种比较下互相干扰。
// 抽成纯函数是为了让上面的守卫能对自己的**判别性**做反例注入自检。
func decidedByCollides(a, b string) bool {
	return strings.Contains(a, b) || strings.Contains(b, a)
}
