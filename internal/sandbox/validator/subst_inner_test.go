// subst_inner_test.go — 判别器：命令替换「递归校验内层」。
//
// 判据口径（用户 2026-10-06 拍板）：
// - **维持**：替换体**内层**若危险（经同一套完整检查判定），仍**硬拒**；
// - **递归校验内层**：内层良性则**放行** —— 不再「看到 `$(` 就一刀切拒」。
//
// 为什么这是正确的口径（不是放宽安全）：改前 `cat /etc/shadow`（直接跑）**放行**，
// 而 `cat <(cat /etc/shadow)` 却因「有进程替换」被拒 —— 同一条命令，换个写法就换个结论。
// 递归校验把内外拉平：**替换体里的命令，按它直接执行的同一套标准判定**。
//
// 全部经生产入口 `NewValidator().Run`（= `internal/tui/lifecycle.go` 装配的那个实例，
// 即 `security.CheckCommand` 的 L1 硬拒出口）进入，不构造旁路。
package validator

import (
	"fmt"
	"strings"
	"testing"
)

// summarize 把结果压成一行，便于在失败信息里看清「哪条检查拒的」。
func summarize(res []SecurityResult) string {
	if len(res) == 0 {
		return "(无)"
	}
	parts := make([]string, 0, len(res))
	for _, r := range res {
		parts = append(parts, fmt.Sprintf("%s/sev=%d", r.CheckID, r.Severity))
	}
	return strings.Join(parts, ", ")
}

// TestW021_InnerBenignAllowed：内层良性 ⇒ 不得被替换族硬拒。
//
// 这些形态是日常写法（`go test ./... 2>&1` 同族的**误伤**面）；改前全部被
// `[CMD-SUBST]` / `[CMD-SUBST-AST]` 判 Critical 硬拒 —— 本用例改前**红**。
func TestW021_InnerBenignAllowed(t *testing.T) {
	benign := []string{
		`echo $(date)`,
		`VAR=$(pwd)`,
		`echo "$(whoami)"`,
		`echo $(seq 3)`,
		"echo `date`",
		`cat <(echo hi)`,
		`echo $(git rev-parse HEAD)`,
		`echo $(echo nested-ok)`,
	}
	for _, cmd := range benign {
		res := NewValidator().Run(cmd)
		if HasBlocking(res) {
			t.Errorf("★ 良性内层被硬拒（ 未修）：%q ⇒ %s", cmd, summarize(res))
		}
	}
}

// TestW021_InnerDangerousStillBlocked：内层危险 ⇒ **必须**仍硬拒。
//
// 这是修复的「安全另一半」：递归下探必须真的下探到，否则「消误报」会把
// `echo $(rm -rf /)` 一起放进来。
//
// ★ 样本选择纪律（2026-10-06 踩到一次，与 同一条教训）：只放**本层判得动**的
// 危险 —— 判据必须选「只有被测层能拦」的形态。`dd if=…` / `mkfs.ext4 …` 属
// permission 层的危险档（validator 的检查集里**本来就没有**），放这里会得到一个
// **假的对照**（改前靠一刀切而"绿"、改后靠不住 —— 实测确认过）。
// 它们由 E2E 守卫 `internal/security/subst_inner_guard_test.go` 在**生产硬拒出口**上验。
func TestW021_InnerDangerousStillBlocked(t *testing.T) {
	dangerous := []string{
		`echo $(rm -rf /)`,
		`x=$(rm -rf /)`,
		`echo $(echo $(rm -rf /))`, // 嵌套：必须逐层下探
		"echo `rm -rf /`",
		`cat <(rm -rf /)`,
		`echo $(rm -fr /)`,
		`echo $(rm -rf $HOME)`,
		`echo $(:(){ :|:& };:)`,
	}
	for _, cmd := range dangerous {
		res := NewValidator().Run(cmd)
		if !HasBlocking(res) {
			t.Errorf("★ 安全语义被削弱（危险内层被放行）：%q ⇒ %s", cmd, summarize(res))
		}
	}
}

// TestW021_UnbalancedFailClosed：抽取不出配对时**仍拒**（fail-closed）。
//
// 递归校验的前提是「能可靠地取出内层」。取不出来时不能默认良性 —— 那是把
// 「解析失败」当成「没问题」，正是 同族的错法。
func TestW021_UnbalancedFailClosed(t *testing.T) {
	unbalanced := []string{
		`echo $(rm -rf /`,
		`echo $(date`,
		`echo $(date $(`,
	}
	for _, cmd := range unbalanced {
		res := NewValidator().Run(cmd)
		if !HasBlocking(res) {
			t.Errorf("★ 不平衡替换必须 fail-closed：%q ⇒ %s", cmd, summarize(res))
		}
	}
}

// TestW021_DirectDangerousUnaffected：直接危险命令不受影响（对照基线）。
//
// 同样只放本层判得动的样本：`rm -rf /`（RM-RF-AST）与 fork 炸弹（FORK-BOMB）。
func TestW021_DirectDangerousUnaffected(t *testing.T) {
	for _, cmd := range []string{`rm -rf /`, `rm -fr $HOME`, `:(){ :|:& };:`} {
		if res := NewValidator().Run(cmd); !HasBlocking(res) {
			t.Errorf("直接危险命令必须仍拦：%q ⇒ %s", cmd, summarize(res))
		}
	}
}
