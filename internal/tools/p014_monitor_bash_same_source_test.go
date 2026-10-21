package tools

import (
	"context"
	"testing"

	"github.com/anchorandfish/whale-code/internal/permission"
)

// P-014 A1 —— **同源守卫**。
//
// 缺陷本体不是"某个枚举漏了一项"，而是 **「要不要问」这件事有两条判定链**：
//
//	管线面（`pipeline.go`）：用 `cmdrules.MatchDestructive`（**枚举**口径）
//	handler 面（`monitor.Manager.Start`）：用 `AnalyzeBashCommand`（**兜底**口径，未知即 destructive）
//
// ⇒ `npm run dev` 在管线面"不需问"、在 handler 面"需确认 ⇒ 拒绝"，两条链结论相反。
// 属本仓 `` / ``「同一事实跨入口必须同源」的具体形态。
//
// 本文件钉住 A1 的**用户可见契约**：工具名（`Monitor` vs `bash`）**不得改变**「要不要问」的结论。
// 装配走生产入口（复用 `p002Pipeline`：`InitPipelineWithHooks` + 只 stub 执行端 sink）。
//
// ⚠️ 安全：`shutdown -h now` / `rm -rf build` 只走到**决策面**，执行端是 stub（`STUBBED-NOT-EXECUTED`），
// 本文件**不会**真的执行任何命令。

func p014RunOne(t *testing.T, tool, input string, approved bool) (needConfirm, proceed bool) {
	t.Helper()
	e := p002Pipeline(t, permission.LevelWorkspaceWrite)
	// 注入事实：OS 强制生效（这正是 `npm run dev` 走"沙箱内免问"那条出口的前提）。
	e.SetEnforcementStatus(p002Enforcement{active: true})
	pr := e.ExecuteWithConfirm(context.Background(), Call{
		ID: "p014-same-source", Name: tool, Input: input, Approved: approved,
	})
	return pr.NeedConfirm, pr.Proceed
}

// TestGuard_P014_MonitorAndBashSameConfirmationDecision A1 的第一半：
// 同一命令、同一管线事实下，`Monitor` 与 `bash` 必须给出**逐字相同**的 `(NeedConfirm, Proceed)`。
//
// 断言形式刻意写成"两侧相等"（parity）—— 但 parity 只防漂移、**不证明内容正确**
// （`production-path-tdd` R4-11）⇒ 故本文件另配 `TestGuard_P014_SameSourceExpectedValues`
// 一组**内容判据**，把"相等"钉在具体期望值上。
func TestGuard_P014_MonitorAndBashSameConfirmationDecision(t *testing.T) {
	cases := []struct{ label, input string }{
		{"普通监控命令(长跑)", `{"command":"npm run dev"}`},
		{"只读", `{"command":"tail -f log.txt"}`},
		{"破坏性档", `{"command":"rm -rf build"}`},
		{"危险档", `{"command":"shutdown -h now"}`},
	}

	for _, c := range cases {
		t.Run(c.label, func(t *testing.T) {
			mNeed, mProceed := p014RunOne(t, "Monitor", c.input, false)
			bNeed, bProceed := p014RunOne(t, "bash", c.input, false)
			if mNeed != bNeed || mProceed != bProceed {
				t.Errorf("Monitor 与 bash 对同一命令的裁决必须同源（P-014 A1）：\n"+
					"  Monitor: NeedConfirm=%v Proceed=%v\n  bash:    NeedConfirm=%v Proceed=%v",
					mNeed, mProceed, bNeed, bProceed)
			}
		})
	}
}

// TestGuard_P014_SameSourceExpectedValues A1 第一半的**内容判据**（补 parity 的不足）。
//
// 三档的期望（两态均绿，属回归守卫；内容依据见 `P-014_…定义与方案…md` §3.2）：
//
//	普通监控命令 `npm run dev` ⇒ 免问 （沙箱在强制 ⇒ 出口 (c)；这是 Monitor 的**主业**，必须能跑）
//	只读 `tail -f log.txt` ⇒ 免问
//	破坏性 `rm -rf build` ⇒ 必须问
//	危险 `shutdown -h now` ⇒ 必须问（危险档永不免问）
func TestGuard_P014_SameSourceExpectedValues(t *testing.T) {
	cases := []struct {
		input      string
		need, proc bool
	}{
		{`{"command":"npm run dev"}`, false, true},
		{`{"command":"tail -f log.txt"}`, false, true},
		{`{"command":"rm -rf build"}`, true, false},
		{`{"command":"shutdown -h now"}`, true, false},
	}
	for _, c := range cases {
		for _, tool := range []string{"Monitor", "bash"} {
			need, proceed := p014RunOne(t, tool, c.input, false)
			if need != c.need || proceed != c.proc {
				t.Errorf("内容判据不符：tool=%s input=%s 期望 (need=%v proceed=%v) 实测 (need=%v proceed=%v)",
					tool, c.input, c.need, c.proc, need, proceed)
			}
		}
	}
}

// TestGuard_P014_ApprovedDangerousProceedsForBoth A1 的第二半（"用户批准后，危险档也执行"）：
// 危险档 = **强制确认**，不是硬拒绝（`-R1`）⇒ 用户批准后管线带 `Approved=true` 重跑，
// 两个工具都必须放行到执行阶段（执行端是 stub）。
//
// 这一半正是 A1 与 A2 的分界：A2 会在这里**只放行 bash**（Monitor 侧硬拒）。
func TestGuard_P014_ApprovedDangerousProceedsForBoth(t *testing.T) {
	for _, tool := range []string{"Monitor", "bash"} {
		t.Run(tool, func(t *testing.T) {
			need, proceed := p014RunOne(t, tool, `{"command":"shutdown -h now"}`, true)
			if !proceed {
				t.Fatalf("用户批准后（Approved=true）危险档必须放行到执行阶段（tool=%s）：NeedConfirm=%v Proceed=%v",
					tool, need, proceed)
			}
		})
	}
}
