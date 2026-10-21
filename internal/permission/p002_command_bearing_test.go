package permission

import "testing"

// P-002 / ★S-2（2026-09-22 反缝补扫描 · `补丁台账_2026-09-22.md` §2.6）——
// 「危险命令不进审批记忆」这条护栏只认手抄的工具名对 `{bash, PowerShell}`，
// 而真正会执行命令的工具是 `{bash, Monitor}`（PowerShell 是幽灵名）。
//
// 后果（违反已拍板铁律 -R1「危险档 = 强制确认永不免问」）：
//
//	用户在确认框上批了 `Monitor({"command":"shutdown -h now"})`
// ⇒ Remember("Monitor", …) 照常写入会话记忆（isDangerousCommand 认不出 Monitor）
// ⇒ 第二次同样的命令**直接免问**（Has 命中）⇒ 护栏失效。
//
// 同时 `permission.go:456` 的注释前提「只对 bash / PowerShell 生效（其余工具没有
// '危险命令'这一维）」本身不成立 —— Monitor 的入参就是一条会被执行的命令串
// （capability.go:105 原文），这一维对它完全成立。
//
// 修法：判据改用 `IsCommandBearingTool`（见 capability.go 的同名注释：
// 拦截语义用"越宽越安全"的集合，豁免语义才用 IsSandboxBoundedTool）。

// TestFix_P002_MonitorDangerousNotRemembered 是 ★S-2 的主判别器。
//
// BEFORE 必然红：`Remember` 记下了危险命令（`Has` 返回 true），
// 且 dont-ask 档下 `Check` 返回 (true,false) = 免问放行。
func TestFix_P002_MonitorDangerousNotRemembered(t *testing.T) {
	input := `{"command":"shutdown -h now"}`

	p := NewPolicy(ParseLevel("dont-ask"))

	// 模拟 TUI 确认框上的"允许本会话"。
	p.Remember("Monitor", input, true)

	if p.Has("Monitor", input) {
		t.Errorf("危险命令不得进入审批记忆: tool=Monitor input=%s ⇒ 否则第二次就免问了", input)
	}

	allowed, needConfirm := p.Check("Monitor", input)
	if !needConfirm {
		t.Errorf("Monitor + 危险命令不得免问放行: allowed=%v needConfirm=%v", allowed, needConfirm)
	}
}

// TestFix_P002_MustConfirmCoversMonitor 直接钉住两个**生产函数本体**的覆盖面。
//
// 与上面那条互补：上面走的是 `Policy`（记忆 + 档位判决），这条走的是被
// `guardrailFires` / `Remember` 直接调用的两个谓词 —— 覆盖哪一层坏掉都能指出来。
func TestFix_P002_MustConfirmCoversMonitor(t *testing.T) {
	dangerous := `{"command":"shutdown -h now"}`
	ordinary := `{"command":"npm run dev"}`

	if !isDangerousCommand("Monitor", dangerous) {
		t.Errorf("isDangerousCommand(Monitor) 必须为真 ⇒ 否则 ★S-2 原样存在")
	}
	if !mustConfirm("Monitor", dangerous) {
		t.Errorf("mustConfirm(Monitor) 必须为真 ⇒ 否则档位判定护栏对 Monitor 失效（★S-1 的权限层那一半）")
	}
	if isDangerousCommand("Monitor", ordinary) {
		t.Errorf("普通监控命令不得被判成危险档: input=%s", ordinary)
	}
	// 反向守卫：非命令载体的入参里即便出现危险词，也不得被误判（防"看走眼式放宽"）。
	if mustConfirm("write_file", `{"path":"x","content":"shutdown -h now"}`) {
		t.Errorf("写类工具没有'危险命令'这一维，不得被卷入危险档判定")
	}
}

// TestGuard_P002_GhostPowerShellIsNotCommandCarrier 钉住**幽灵名的清除**。
//
// `PowerShell` 从未注册为工具（tools/tools.go 的 registerBuiltinTools 无此项、
// 能力表也无此项，见 `s4_ssot_level_drift_probe_test.go:177` 的守卫）。
// 它被当成命令载体，等于让一个**不存在的工具**参与判定 —— 于是"改哪个名字才生效"
// 这件事被掩盖了整整一段时间（这正是 P-002 能长期存活的原因）。
//
// ⇒ 本守卫声明：命令载体 = 能力表里真有 CapExec 的工具；幽灵名一律 false。
func TestGuard_P002_GhostPowerShellIsNotCommandCarrier(t *testing.T) {
	for _, ghost := range []string{"PowerShell", "powershell", "  powershell  "} {
		if IsCommandBearingTool(ghost) {
			t.Errorf("幽灵名 %q 不得被认作命令载体（它未注册、也不在能力表里）", ghost)
		}
		if isDangerousCommand(ghost, `{"command":"rm -rf /"}`) {
			t.Errorf("幽灵名 %q 不得参与危险档判定 —— 这会让'改对真名'这件事被掩盖", ghost)
		}
	}
}

// TestGuard_P002_ApprovalScopeDeliberatelyUnchangedForMonitor 把"**刻意不改**"这件事
// 从注释升级成判据（例外也要说清）。
//
// `approvalKeyFor` 对 bash 会把审批记忆键**归一化为命令作用域前缀**
// （`commandScopeKey`：批准一次 `tail -f a.log` 即长期免问同前缀命令，见 S9/HG-A2）。
// 若把 Monitor 也纳入这条归一化，等于**放宽**（原先每次输入都是一条独立记忆键）
// ⇒ 与本次"只增加确认、不放松任何判定"的方向相反。
//
// ⇒ 本判据的失败若来自"顺手把 Monitor 加进 isShellTool"，请先回答：
// 你是在收窄还是在放宽？允许一次长跑命令就不再问同前缀命令，属放宽。
func TestGuard_P002_ApprovalScopeDeliberatelyUnchangedForMonitor(t *testing.T) {
	monitorInput := `{"command":"tail -f a.log"}`

	if got, want := approvalKeyFor("Monitor", monitorInput), "Monitor\x00"+monitorInput; got != want {
		t.Errorf("Monitor 的审批记忆键必须保持「整段输入」语义（不归一化）: got=%q want=%q", got, want)
	}

	bashInput := `{"command":"tail -f a.log"}`
	if got := approvalKeyFor("bash", bashInput); got == "bash\x00"+bashInput {
		t.Errorf("bash 的审批记忆键本应归一化为命令作用域（本判据只用于确认这条既有语义仍成立）: got=%q", got)
	}
}

// TestFix_P002_AllowRuleCannotExemptMonitorDangerousCommand 覆盖 **allow 分支内**那道
// 危险档护栏（checkModeLocked 里 allow 命中后唯一的危险档检查）。
//
// 与 `DangerousGuardStage` 的分工：那条装在**管线**上（排在全部免问出口之前）；
// 这条在**权限层**，是档位判决里的最后一道。P-002 的 12 处手抄里它属"有 SSOT 却手抄"
// 且后果最直接的一类 —— `allow Monitor` 规则 + 危险命令 = **免问**。
func TestFix_P002_AllowRuleCannotExemptMonitorDangerousCommand(t *testing.T) {
	p := NewPolicy(LevelWorkspaceWrite)
	if err := p.AddRule(PermissionRule{
		ToolName: "Monitor",
		Behavior: RuleAllow,
		Source:   SourceSession,
	}); err != nil {
		t.Fatalf("构造 allow 规则失败: %v", err)
	}

	allowed, needConfirm := p.CheckWithRules("Monitor", `{"command":"shutdown -h now"}`)
	if !allowed {
		t.Fatalf("allow 规则命中时应允许执行（本护栏只改「要不要问」，不改「能不能做」）: allowed=%v", allowed)
	}
	if !needConfirm {
		t.Errorf("allow 规则不得免掉危险命令的确认（Monitor）: needConfirm=false ⇒ ★S-1 的权限层缺口")
	}
}
