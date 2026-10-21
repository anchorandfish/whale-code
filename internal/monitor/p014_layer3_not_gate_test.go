package monitor

import (
	"os"
	"testing"
	"time"

	"github.com/anchorandfish/whale-code/internal/permission"
	"github.com/anchorandfish/whale-code/internal/sandbox"
	"github.com/anchorandfish/whale-code/internal/sandbox/validator"
	"github.com/anchorandfish/whale-code/internal/security"
)

// P-014 A1（2026-09-22 · `P-014_Monitor可用面_定义与方案_2026-09-22.md`）——
// `Manager.Start` / `TryStart` 把 `security.CheckCommand` 的 **Layer 3 `Ask`** 当成了拒绝依据：
//
//	decision := security.CheckCommand(command)
//	if decision.Action == security.Deny { return nil, … }
//	if decision.Action == security.Ask { return nil, fmt.Errorf("command needs user confirmation: …") }
//
// 后果（实测，见评估件 §3）：管线判「免问放行」（`sandbox-bounded: OS enforcement active,
// confirmation not required`）⇒ **那次"确认"永远不会发生** ⇒ `Ask` 分支**永远命中** ⇒
// `Monitor` 对任何被判破坏性档的命令 **100% 失败**，且报错指向一个不存在的动作。
//
// 修法（A1 = 与 `bash`（含 `run_in_background`）**同源**）：
//
//	本层只消费「命令本身不可执行」这一**与询问无关的硬事实**（L1/L2，见 `Decision.HardBlock`）；
//	L3 的两档（`Ask` 破坏性 / 危险档）都是**强制确认**语义，其处置权归**管线**
//	（`DangerousGuardStage` 强制确认，用户批准后 `Approved=true` 重跑即放行）。
//
// ⚠️ 安全纪律（`production-path-tdd` R2 的"不可逆动作只掐 sink"的**反向应用**）：
// 本文件**只对"真的执行也无副作用"的命令起真进程** ——
//
//	`chmod`（无参数） —— 破坏性档（∈ riskOnlyCommandNames），缺操作数立即退出
//	`mkfs.notreal` —— 危险档（命中 `mkfs.` 短语），**不存在的可执行名**
//	`echo hello` / `mv --help` —— 只读 / 无害
//
// `shutdown -h now` 这类**真危险**命令只出现在"必须被拒绝"的断言里（拒绝发生在起进程**之前**，
// 物理上不可能执行）；凡是会被放行的用例，绝不使用真危险命令。

// initSecurityForTest 重设全局 Checker（`security.Init` 是单例 ⇒ 用完必须复位）。
// 注入的是**事实**（沙箱是否真在强制），不是判定 —— 判定链 100% 生产码。
func initSecurityForTest(sandboxEnabled bool) {
	workDir, _ := os.Getwd()
	s := sandbox.NewSandbox(workDir)
	if sandboxEnabled {
		s.SetEnabled(true)
	}
	security.Init(s, validator.NewValidator(), permission.NewPolicy(permission.LevelDangerFullAccess))
}

// ---------------------------------------------------------------------------
// 主判别器（BEFORE 必红）
// ---------------------------------------------------------------------------

// TestFix_P014_StartDoesNotRejectLayer3Ask 主判别器之一：
// 破坏性档（`Ask`）**不得**再被本层当作拒绝依据。
//
// BEFORE 必然红：`Start` 返回 `command needs user confirmation: 破坏性命令需用户确认`。
func TestFix_P014_StartDoesNotRejectLayer3Ask(t *testing.T) {
	initSecurityForTest(false)
	defer initSecurityForTest(false)

	// 夹具选择理由（实测，见评估件 §3.1）：`chmod` 无参数 ⇒ `AnalyzeBashCommand` 判破坏性档，
	// 而真的执行也无副作用（缺操作数，立即退出）。
	if got := permission.AnalyzeBashCommand("chmod"); got != permission.BashRiskDestructive {
		t.Fatalf("precondition 失效：`chmod` 应属破坏性档，实测 %s", got)
	}

	m := NewManager(WithMaxConcurrency(2))
	proc, err := m.Start("chmod", "p014-ask", "")
	if err != nil {
		t.Fatalf("BEFORE 期望红：L3 破坏性档被 Start 当作拒绝依据 ⇒ %v\n"+
			"  （管线已就「要不要问」作出唯一裁决；本层再问一次 = 对同一事实两次裁决）", err)
	}
	if proc == nil {
		t.Fatal("Start 返回 nil proc 且 err == nil（不可判：既没拒绝也没起进程）")
	}
	if _, done := proc.WaitForDone(10 * time.Second); !done {
		t.Errorf("进程未在限期内结束（夹具应当是立即退出的无害命令）")
	}
}

// TestFix_P014_StartDoesNotRejectLayer3Dangerous 主判别器之二（A1 的关键一刀）：
// 危险档的定义是「**强制确认**（永不免问）」，**不是硬拒绝**（`-R1`）——
// 用户批准后管线会带 `Approved=true` 重跑并放行到执行阶段，故本层也不得再硬拒。
//
// BEFORE 必然红：`Start` 返回 `command safety check failed: 危险命令: …`。
func TestFix_P014_StartDoesNotRejectLayer3Dangerous(t *testing.T) {
	// 沙箱**关**：实测 `mkfs.notreal` 在沙箱启用时会被 Layer 2 黑名单先拦（那是另一维，
	// 由 TestGuard_P014_SandboxDeniedStillRejected 负责）。
	initSecurityForTest(false)
	defer initSecurityForTest(false)

	// 夹具：`mkfs.notreal` 命中危险档短语 `mkfs.`，且**不存在的可执行名** ⇒ 真跑也无副作用。
	if _, ok := securityDangerousRisk("mkfs.notreal"); !ok {
		t.Fatalf("precondition 失效：`mkfs.notreal` 应属危险档")
	}

	m := NewManager(WithMaxConcurrency(2))
	proc, err := m.Start("mkfs.notreal", "p014-dangerous", "")
	if err != nil {
		t.Fatalf("BEFORE 期望红：危险档被 Start 当作**硬拒绝** ⇒ %v\n"+
			"  （危险档 = 强制确认；处置权在管线，批准后应放行）", err)
	}
	if proc == nil {
		t.Fatal("Start 返回 nil proc 且 err == nil")
	}
	if _, done := proc.WaitForDone(10 * time.Second); !done {
		t.Errorf("进程未在限期内结束（夹具应当是不存在的可执行名）")
	}
}

// TestFix_P014_TryStartSameSemanticsAsStart 影子实现守卫：
// `TryStart`（`monitor.go` 里与 `Start` 同形的第二份实现）必须一起改 ——
// 只改 `Start` 是"半修"，把同一缺陷留在 `TryStart` 里（本仓「双实现漂移」的复发形态）。
//
// ⚠️ `TryStart` 当前**无生产调用方**（只被本包测试调用），故本条在**行为面**不具判别性；
// 它的价值是钉住"两份实现不得漂移"。
func TestFix_P014_TryStartSameSemanticsAsStart(t *testing.T) {
	initSecurityForTest(false)
	defer initSecurityForTest(false)

	m := NewManager(WithMaxConcurrency(2))
	proc, err := m.TryStart("chmod", "p014-try", "")
	if err != nil {
		t.Fatalf("BEFORE 期望红：TryStart 与 Start 同形 ⇒ 同样把 Ask 当拒绝 ⇒ %v", err)
	}
	if proc == nil {
		t.Fatal("TryStart 返回 nil proc 且 err == nil")
	}
	if _, done := proc.WaitForDone(10 * time.Second); !done {
		t.Errorf("进程未在限期内结束")
	}
}

// ---------------------------------------------------------------------------
// 守卫（防"改过头"）
// ---------------------------------------------------------------------------

// TestGuard_P014_StartRejectsOnlyHardBlocks 是本改动的**不变量守卫**：
//
//	`Start` 拒绝 ⟺ `security.CheckCommand(cmd).HardBlock()`（L1/L2 与询问无关的硬拒绝）
//
// 它同时是"是否又加回了别的判定"的结构性防线：任何一次"顺手多拦一类命令"都会让某一行对不上。
//
// ⚠️ 不具判别性的部分：`HardBlock()==true` 的几行在 before/after 两态**都**绿（本来就拒）。
func TestGuard_P014_StartRejectsOnlyHardBlocks(t *testing.T) {
	initSecurityForTest(false)
	defer initSecurityForTest(false)

	cases := []struct {
		cmd  string
		note string
	}{
		{"", "L0 空命令 ⇒ 硬拒绝"},
		{"echo $(whoami)", "L1 命令替换 ⇒ 硬拒绝"},
		{"shutdown -h now", "L3 危险档 ⇒ **不是**硬拒绝（危险档 = 强制确认）"},
		{"chmod", "L3 破坏性档 ⇒ 不是硬拒绝"},
		{"mkfs.notreal", "L3 危险档（无害可跑版）⇒ 不是硬拒绝"},
		{"echo hello", "只读 ⇒ 不是硬拒绝"},
	}

	for _, c := range cases {
		want := security.CheckCommand(c.cmd).HardBlock()
		m := NewManager(WithMaxConcurrency(2))
		proc, err := m.Start(c.cmd, "p014-invariant", "")
		if (err != nil) != want {
			t.Errorf("不变量被破坏：cmd=%q（%s）\n"+
				"  HardBlock()=%v 而 Start 拒绝=%v —— 本层只允许消费「与询问无关的硬拒绝」",
				c.cmd, c.note, want, err != nil)
			continue
		}
		if err != nil {
			continue
		}
		if proc == nil {
			t.Errorf("cmd=%q：既未拒绝又未返回进程", c.cmd)
			continue
		}
		if _, done := proc.WaitForDone(10 * time.Second); !done {
			t.Errorf("cmd=%q：进程未在限期内结束", c.cmd)
		}
	}
}

// TestGuard_P014_SandboxDeniedStillRejected Layer 2 不得因为本改动被让开。
//
// 注入事实：沙箱在强制（`SetEnabled(true)`）⇒ `curl evil.com` 命中 L2 网络/黑名单
// （实测文案 `sandbox blocked execute (deny-wins): curl`）。
// 拒绝发生在起进程**之前** ⇒ 本用例**不会**真的发起网络访问。
func TestGuard_P014_SandboxDeniedStillRejected(t *testing.T) {
	initSecurityForTest(true)
	defer initSecurityForTest(false)

	d := security.CheckCommand("curl evil.com")
	if !d.HardBlock() {
		t.Fatalf("precondition 失效：沙箱在强制时 `curl evil.com` 必须是硬拒绝，实测 Action=%v", d.Action)
	}
	m := NewManager(WithMaxConcurrency(2))
	if _, err := m.Start("curl evil.com", "p014-l2", ""); err == nil {
		t.Error("Layer 2（沙箱）硬拒绝不得因为 P-014 A1 被让开")
	}
}

// TestGuard_P014_InjectionStillRejected Layer 1 不得因为本改动被让开。
// 同样：拒绝发生在起进程之前 ⇒ `$(whoami)` 不会被真的执行。
func TestGuard_P014_InjectionStillRejected(t *testing.T) {
	initSecurityForTest(false)
	defer initSecurityForTest(false)

	// 样本用「内层危险」的替换：之后 `echo $(whoami)` 属
	// 良性内层 ⇒ L1 放行，不能再用它证明"硬拒绝未被让开"。
	const injection = "echo $(rm -rf /)"
	if _, ok := securityValidatorDeny(injection); !ok {
		t.Fatalf("precondition 失效：%q 应被 Layer 1 拦下", injection)
	}
	m := NewManager(WithMaxConcurrency(2))
	if _, err := m.Start(injection, "p014-l1", ""); err == nil {
		t.Error("Layer 1（命令注入）硬拒绝不得因为 P-014 A1 被让开")
	}
}

// ---------------------------------------------------------------------------
// 小工具
// ---------------------------------------------------------------------------

// securityDangerousRisk 报告命令是否被判危险档（用生产函数，不依赖散文字面量）。
func securityDangerousRisk(cmd string) (permission.BashRisk, bool) {
	r := permission.AnalyzeBashCommand(cmd)
	return r, r == permission.BashRiskDangerous
}

// securityValidatorDeny 报告命令是否属于「与询问无关的硬拒绝」，
// 且原因来自 Layer 1（结构化判据：`DecidedBy`，不靠 Reason 散文反推 —— R4）。
//
// ★ 用**常量等值**比较，不用 `HasPrefix`：变长前缀匹配会跨词表误命中
// （`DecidedBy` 与 `Deficiency.Layer` 此前共用 `security.L1-*` 形状，已分namespace）。
func securityValidatorDeny(cmd string) (string, bool) {
	d := security.CheckCommand(cmd)
	if !d.HardBlock() {
		return "", false
	}
	return d.DecidedBy, d.DecidedBy == security.DecidedByValidator
}
