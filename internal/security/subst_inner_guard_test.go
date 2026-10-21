// subst_inner_guard_test.go — 端到端守卫（生产硬拒出口）。
//
// 与单元层判别器（`sandbox/validator/subst_inner_test.go`）的分工：
//
//	单元层 —— 问"validator 自己判得对不对"（经 NewValidator().Run）
//	本文件 —— 问"**装起来的**那个出口对不对"（经 security.Init + CheckCommand，
//
// 与 internal/tui/lifecycle.go 装配、executor 真正调用的是同一条链）
//
// 它同时钉住**两层合起来**才成立的事实：
//
//	validator —— 管"语法面危险"（`rm -rf /` 之类，它自己的检查集判得动）
//	permission —— 管"危险档名单"（`dd if=` / `git push --force` 之类，
//
// via cmdrules.ExecutionPayloads 下探载荷）
//
// 两层共用同一份**载荷抽取**（cmdrules 的单一声明）⇒ 任何一半被削弱，这里都会红。
package security

import (
	"strings"
	"testing"
)

// TestGuard_W021_BenignInnerNotHardBlocked：内层良性 ⇒ 生产出口不得硬拒（本体）。
func TestGuard_W021_BenignInnerNotHardBlocked(t *testing.T) {
	initSecurityForProbe(false)

	allowed := []string{
		"echo $(date)",
		"VAR=$(pwd)",
		`echo "$(whoami)"`,
		"cat <(echo hi)",
		"echo $(git rev-parse HEAD)",
	}
	for _, cmd := range allowed {
		d := CheckCommand(cmd)
		if d.HardBlock() {
			t.Errorf("★ 良性内层被硬拒（ 回退？）：%q ⇒ Action=%v DecidedBy=%q Reason=%q",
				cmd, d.Action, d.DecidedBy, d.Reason)
		}
	}
}

// TestGuard_W021_DangerousInnerStillDenied：内层危险 ⇒ 生产出口必须仍拒（维持）。
//
// 分两组断言，因为**兜住它的层不同**（这正是"两层各司其职"的机械证据）：
// - byValidator：validator 的检查集判得动（rm -rf / 族）
// - byOtherLayers：validator 判不动，但属危险档名单 ⇒ permission 层经载荷下探兜住
func TestGuard_W021_DangerousInnerStillDenied(t *testing.T) {
	initSecurityForProbe(false)

	byValidator := []string{
		"echo $(rm -rf /)",
		"x=$(rm -rf /)",
		"echo $(echo $(rm -rf /))",
		"cat <(rm -rf /)",
	}
	for _, cmd := range byValidator {
		d := CheckCommand(cmd)
		if d.Action != Deny {
			t.Errorf("★ 内层危险必须拒：%q ⇒ Action=%v DecidedBy=%q", cmd, d.Action, d.DecidedBy)
		}
	}

	// 这些内层属**危险档名单**（validator 的检查集里没有），必须由 permission 层
	// 经**同一份载荷抽取**兜住 —— 若有人把 cmdrules.ExecutionPayloads 抽掉，这里会红。
	byPermission := []string{
		"echo $(dd if=/dev/zero of=/dev/sda)",
		"echo $(git push --force origin main)",
		`sh -c "rm -rf /"`,
	}
	for _, cmd := range byPermission {
		d := CheckCommand(cmd)
		if d.Action != Deny {
			t.Errorf("★ 载荷下探被削弱（permission 层没兜住）：%q ⇒ Action=%v DecidedBy=%q",
				cmd, d.Action, d.DecidedBy)
		}
	}
}

// TestGuard_W021_ValidatorOnlyInnerDenied：**只有 validator 判得动**的内层必须被拒。
//
// ★ 为什么必须单独一组（2026-10-06 实测踩到，与 同一条教训）：
// 上面 byValidator 那组（`echo $(rm -rf /)` 之类）**并不判别本层** —— 把内层递归整个关掉，
// 它照样拒，因为 permission 层的载荷下探也会命中同一个 `rm -rf /`。
// 用 `-overlay` 实测：关掉 `innerFindings` ⇒ 单元层红、**本文件照样绿**（被另一层兜住）。
//
// ⇒ 要判别"validator 的递归是否真在跑"，样本必须满足：**permission 层拦不住它**。
//
// 本组唯一样本是「嵌套超深」：validator 有深度上限、超限 fail-closed；
// permission 的下探到 `maxDangerDepth` 就**停**（返回"非危险"）⇒ 不会替它拒。
// 实测（关掉 innerFindings）：本用例**红**，而 byValidator 组仍绿 —— 这一条红/一条绿
// 的差别就是"判别力"的机械证据。
//
// ⚠️ 试过但**不合格**的样本（留档，免得下一个人再试一遍）：
//
//	`echo $(echo {a,b})` —— 看着像"validator 独有的 BRACE-EXP"，实测**不判别**：
//	BRACE-EXP 是**字符串**检查，它在外层命令串上直接就能看到 `{a,b}`（载荷文本本来就在串里），
//	关掉递归照样拒。⇒ 凡是"字符串级"的危险，递归对它们都是冗余的；递归真正承重的是
//	**AST 级**事实（如 RM-RF-AST：AST 看不见被 skipSubstitutionBody 跳过的载荷）。
func TestGuard_W021_ValidatorOnlyInnerDenied(t *testing.T) {
	initSecurityForProbe(false)

	cmd := "echo $(echo $(echo $(echo $(echo $(echo $(x))))))"
	if d := CheckCommand(cmd); d.Action != Deny {
		t.Errorf("★ 只有 validator 能拦的形态（嵌套超深 ⇒ 深度上限 fail-closed）必须被拒（递归被关掉？）：%q ⇒ Action=%v DecidedBy=%q",
			cmd, d.Action, d.DecidedBy)
	}
}

// TestGuard_W021_UnbalancedFailClosed：内层**取不出配对** ⇒ 必须 fail-closed。
//
// 递归校验的前提是"能可靠取出内层"。取不出来时默认放行 = 把"解析失败"当成"没问题"，
// 正是 同族的错法。
func TestGuard_W021_UnbalancedFailClosed(t *testing.T) {
	initSecurityForProbe(false)

	for _, cmd := range []string{"echo $(date", "echo $(rm -rf /"} {
		if d := CheckCommand(cmd); d.Action != Deny {
			t.Errorf("★ 不平衡替换必须 fail-closed：%q ⇒ Action=%v Reason=%q", cmd, d.Action, d.Reason)
		}
	}
}

// TestGuard_W021_SubstitutionFamilyHasSingleOwner 是"单点实现"的**机械门**：
// 执行型替换族只应有一个 CheckID（`CMD-SUBST`）。
//
// 为什么要有这条：本族的判定此前在 AST 层与字符串层**各写一份**（`CMD-SUBST-AST` +
// `CMD-SUBST`），两处结论一致纯属巧合。 收口后若有人把第二份加回来，
// 这里立刻红 —— 那将重新制造"同语义两处实现"（本仓已为此栽过多次）。
func TestGuard_W021_SubstitutionFamilyHasSingleOwner(t *testing.T) {
	initSecurityForProbe(false)

	d := CheckCommand("echo $(rm -rf /)")
	if d.Action != Deny {
		t.Fatalf("前置失败：内层危险应被拒，实际 Action=%v", d.Action)
	}
	if strings.Contains(d.Reason, "CMD-SUBST-AST") {
		t.Errorf("替换族应只有一个 owner（CMD-SUBST），出现重复 CheckID：Reason=%q", d.Reason)
	}
}
