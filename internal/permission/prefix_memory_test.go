package permission

import "testing"

// 判别器（先红）：权限层的「允许该命令前缀」会话级记忆。
//
// 生产消费点是 `PermissionStage` 的免问出口 (d)（`HasCommandPrefix`）——
// 不是 `checkModeLocked`：前缀免问必须排在自指保护 () **之后**，
// 否则批准过 `seq` 前缀就能顺带免问「动 whale 自身源码」的调用。
// 该顺序不变量由 internal/tools 侧的 E2E 守卫钉住。
//
// 四条不可退让的语义（用户 2026-10-06 拍板「允许该命令前缀」）：
// ① 同头**单一简单命令**豁免（`seq 1 9` 批准 ⇒ `seq 1 3` 免问）；
// ② **复合命令**不被前缀豁免（`seq 1 3 && echo x` 仍问）；
// ③ **危险/破坏性**命令永不豁免，且**不得被记成前缀**（rm 前缀永远建立不起来）；
// ④ 前缀记忆**严格会话级** + **按工具隔离**（新 Policy / 别的工具都不受影响）。

func bashInput(cmd string) string { return `{"command":"` + cmd + `"}` }

// ① 同头、单一简单命令 ⇒ 免问。
func TestW022_SameHeadSingleCommandExempted(t *testing.T) {
	p := NewPolicy(LevelWorkspaceWrite)

	if _, need := p.Check("bash", bashInput("seq 1 3")); !need {
		t.Fatal("precondition: 未记忆时 seq 1 3 在 ww 档应需确认")
	}
	if p.HasCommandPrefix("bash", bashInput("seq 1 3")) {
		t.Fatal("precondition: 未批准前缀前不应命中")
	}

	// 用户在面板上选择「允许该命令前缀」（对 `seq 1 9`）
	p.RememberPrefix("bash", bashInput("seq 1 9"))

	if !p.HasCommandPrefix("bash", bashInput("seq 1 3")) {
		t.Fatal("W-022①：批准 `seq` 前缀后，同头单一简单命令 `seq 1 3` 应命中免问")
	}
	if !p.HasCommandPrefix("bash", bashInput("seq 5 7 -w")) {
		t.Fatal("W-022①：参数不同但头词相同，仍应命中")
	}
	// 头词不同 ⇒ 不命中
	if p.HasCommandPrefix("bash", bashInput("sleep 1")) {
		t.Fatal("W-022①：头词不同（sleep）不得被 `seq` 前缀豁免")
	}
}

// ② 复合命令不被前缀豁免。
func TestW022_CompoundNotExempted(t *testing.T) {
	p := NewPolicy(LevelWorkspaceWrite)
	p.RememberPrefix("bash", bashInput("seq 1 9"))

	cases := []string{
		"seq 1 3 && echo done",
		"seq 1 3 | cat",
		"seq 1 3 ; echo done",
		"seq 1 3 > /tmp/out",
		"echo $(seq 1 3)",
	}
	for _, cmd := range cases {
		if p.HasCommandPrefix("bash", bashInput(cmd)) {
			t.Errorf("W-022②：复合/含替换/含重定向的命令 %q 不得被前缀豁免", cmd)
		}
	}
}

// ③ 危险 / 破坏性命令不得被记成前缀，也永不豁免。
func TestW022_DangerousNeverRememberedOrExempted(t *testing.T) {
	p := NewPolicy(LevelWorkspaceWrite)

	// 破坏性（rm 在确认名单里）⇒ RememberPrefix 必须是 no-op
	p.RememberPrefix("bash", bashInput("rm ./build/x"))
	if p.HasCommandPrefix("bash", bashInput("rm ./build/other")) {
		t.Fatal("W-022③：破坏性命令不得被记成前缀（rm 前缀永远建立不起来）")
	}
	// 危险 ⇒ 同样不得记
	p.RememberPrefix("bash", bashInput("rm -rf /"))
	if p.HasCommandPrefix("bash", bashInput("rm /tmp/x")) {
		t.Fatal("W-022③：危险命令不得被记成前缀")
	}

	// 即便（理论上）某前缀已存在，危险命令仍不得命中 —— 双保险
	p2 := NewPolicy(LevelWorkspaceWrite)
	p2.RememberPrefix("bash", bashInput("mkfs /dev/x")) // 破坏性 ⇒ no-op
	if p2.HasCommandPrefix("bash", bashInput("mkfs.ext4 /dev/x")) {
		t.Fatal("W-022③：危险命令永不豁免")
	}
}

// ③b 危险命令必须在 Policy.Check 处**仍然需要确认**（护栏不被前缀机制软化）。
func TestW022_DangerousStillNeedsConfirm(t *testing.T) {
	p := NewPolicy(LevelWorkspaceWrite)
	p.RememberPrefix("bash", bashInput("seq 1 9"))

	allowed, need := p.Check("bash", bashInput("rm -rf /tmp/x"))
	if !allowed || !need {
		t.Fatalf("W-022③：危险命令必须仍 (allowed=true, needConfirm=true)，got allowed=%v need=%v", allowed, need)
	}
}

// ④ 严格会话级 + 按工具隔离。
func TestW022_SessionScopedAndToolIsolated(t *testing.T) {
	p := NewPolicy(LevelWorkspaceWrite)
	p.RememberPrefix("bash", bashInput("seq 1 9"))

	// 另一个新 Policy（= 新会话 / 重启）不得继承 —— 前缀不落盘
	fresh := NewPolicy(LevelWorkspaceWrite)
	if fresh.HasCommandPrefix("bash", bashInput("seq 1 3")) {
		t.Fatal("W-022④：前缀记忆必须严格会话级，不得跨 Policy 存活（不落盘）")
	}

	// 工具隔离：`bash` 批准的前缀不得让 `Monitor` 免问
	if p.HasCommandPrefix("Monitor", bashInput("seq 1 3")) {
		t.Fatal("W-022④：前缀记忆按工具隔离，bash 的批准不得外溢到 Monitor")
	}
	// 非命令载体工具根本不存在前缀概念
	if p.HasCommandPrefix("write_file", bashInput("seq 1 3")) {
		t.Fatal("W-022④：write_file 不是命令载体，不得有前缀豁免")
	}
}

// ④b `Remember`（本会话档）与 `RememberPrefix` 是两条**不同**的记忆：
// 全量本会话批准不产生前缀豁免；前缀批准也不产生全量批准。
func TestW022_PrefixIsNotFullSessionApproval(t *testing.T) {
	p := NewPolicy(LevelWorkspaceWrite)

	p.Remember("bash", bashInput("seq 1 7"), true) // 全量批准 `seq 1 7`
	if p.HasCommandPrefix("bash", bashInput("seq 1 9")) {
		t.Fatal("W-022④b：全量本会话批准 ≠ 前缀批准，不得互相转化")
	}

	q := NewPolicy(LevelWorkspaceWrite)
	q.RememberPrefix("bash", bashInput("seq 1 7"))
	if q.Has("bash", bashInput("seq 1 8")) {
		t.Fatal("W-022④b：前缀批准 ≠ 全量批准，`seq 1 8` 不得被 Has 命中")
	}
}

// ④c 关闭危险护栏（无人值守装配）不得让前缀豁免顺带放过危险命令。
func TestW022_GuardrailDisabledDoesNotLoosenPrefix(t *testing.T) {
	p := NewPolicy(LevelWorkspaceWrite)
	p.SetDangerousCommandGuardrail(false)
	p.RememberPrefix("bash", bashInput("seq 1 9"))

	if p.HasCommandPrefix("bash", bashInput("rm -rf /tmp/x")) {
		t.Fatal("W-022④c：关闭危险护栏只影响「是否二次确认」，不得让前缀豁免放过危险命令")
	}
}
