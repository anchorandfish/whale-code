// agent_guard_readonly_progress_test.go — 只读「获取信息」轮次必须计入进展（P0-3 防误杀）。
//
// 缺陷（2026-09-22 15:22 真机，会话存档 01a0c516）：
//
//	turnObservation 只把 `read_file` 计入 FilesRead，`glob_search` / `grep_search`
//	等只读工具**完全不计**。于是「连续多轮只读搜索」被算成「无文件改动的纯读轮」
//	⇒ P0-3 审查子循环上限（MaxReviewRounds=3）误判「疑似自我审查空转」停机。
//	真机轨迹：4 轮 = 3×AskUser（1 轮作答 + 2 轮被缓存跳过的重问）+ 1×glob_search。
//
// 判据分三层：
// 1. 单元：只读工具（glob_search / grep_search / git_log / lsp*）的成功调用必须
// 记入 FilesRead；非只读工具（TodoWrite…）不得记入（防「什么都算进展」的假绿）。
// 2. 端到端：真机轮次形状（3×AskUser + 1×只读搜索）**不得**触发审查超限停机。
// 3. 反例守卫：反复**同一个**搜索（原地打转）仍必须触发停机 —— 但 P0-3 于
// 2026-09-22 降级为软提醒后，接住它的必须是 **P0-4 重复检测**（rereadExceeded），
// 不是 P0-3。降级不得让真空转逃脱。
package tui

import (
	"fmt"
	"strings"
	"testing"

	"github.com/anchorandfish/whale-code/internal/agentguard"
	"github.com/anchorandfish/whale-code/internal/session"
	"github.com/anchorandfish/whale-code/internal/tools"
)

// readOnlyCall 构造一次成功的只读工具调用及其配对结果。
func readOnlyCall(id, name, input string) (session.ContentBlock, tools.Result) {
	return session.ContentBlock{Type: "tool_use", ID: id, Name: name, Input: input},
		tools.Result{ID: id, Name: name, Output: "ok"}
}

// ---------------------------------------------------------------------------
// 第 1 层：单元 —— 只读工具计入 FilesRead
// ---------------------------------------------------------------------------

// TestTurnObservation_ReadOnlyToolsCountedAsRead 只有只读工具才算「获取信息」。
func TestTurnObservation_ReadOnlyToolsCountedAsRead(t *testing.T) {
	cases := []struct {
		name  string
		tool  string
		input string
	}{
		{"glob_search", "glob_search", `{"pattern":"**/*memory*"}`},
		{"grep_search", "grep_search", `{"pattern":"func main","path":"/repo"}`},
		{"git_log", "git_log", `{"limit":5}`},
		{"lsp_diagnostics", "lspdiagnostics", `{"file_path":"a.go"}`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			call, res := readOnlyCall("c1", tc.tool, tc.input)
			obs := turnObservation([]session.ContentBlock{call}, []tools.Result{res}, "")
			if len(obs.FilesRead) != 1 {
				t.Fatalf("只读工具 %s 成功调用必须记入 FilesRead（否则多轮只读研究被误判空转），实际 %v",
					tc.tool, obs.FilesRead)
			}
			if len(obs.FilesChanged) != 0 {
				t.Fatalf("只读工具不得记入 FilesChanged，实际 %v", obs.FilesChanged)
			}
		})
	}

	// 反例：非只读工具不得记入（防「一切工具都算进展」的假绿）。
	call, res := readOnlyCall("t1", "TodoWrite", `{"todos":[]}`)
	if obs := turnObservation([]session.ContentBlock{call}, []tools.Result{res}, ""); len(obs.FilesRead) != 0 {
		t.Fatalf("TodoWrite 不是「获取信息」工具，不得记入 FilesRead，实际 %v", obs.FilesRead)
	}

	// 无回归：read_file 仍以裸路径为键（报告「最近关注」与同文件重读判定依赖它）。
	rc, rr := readOnlyCall("r1", "read_file", `{"path":"/repo/a.go"}`)
	if obs := turnObservation([]session.ContentBlock{rc}, []tools.Result{rr}, ""); len(obs.FilesRead) != 1 || obs.FilesRead[0] != "/repo/a.go" {
		t.Fatalf("read_file 的键必须保持为裸路径，实际 %v", obs.FilesRead)
	}
}

// TestTurnObservation_SearchTargetKeyDistinguishesPatterns 搜索键须能区分不同搜索词：
// 换搜索词 = 新目标（前进），同一搜索词重复 = 同一目标（打转）。
func TestTurnObservation_SearchTargetKeyDistinguishesPatterns(t *testing.T) {
	keyOf := func(pattern string) string {
		call, res := readOnlyCall("s", "glob_search", fmt.Sprintf(`{"pattern":%q}`, pattern))
		obs := turnObservation([]session.ContentBlock{call}, []tools.Result{res}, "")
		if len(obs.FilesRead) != 1 {
			t.Fatalf("glob_search 未记入 FilesRead（pattern=%s）", pattern)
		}
		return obs.FilesRead[0]
	}
	if keyOf("**/*memory*") == keyOf("**/*.go") {
		t.Fatal("不同搜索词的键必须不同，否则「换搜索词前进」会被误判为重复打转")
	}
	if keyOf("**/*memory*") != keyOf("**/*memory*") {
		t.Fatal("同一搜索词的键必须相同，否则「反复同一搜索」永远抓不住")
	}
}

// ---------------------------------------------------------------------------
// 第 2 层：端到端 —— 真机轮次形状不得触发审查超限
// ---------------------------------------------------------------------------

// TestGuard_ReadOnlySearchRoundIsProgress_NotReviewSpin · 主用例（before 必红）。
//
// 复刻真机（2026-09-22 15:22）的轮次形状：3 轮 AskUser + 1 轮只读搜索。
// AskUser 是会话控制面（非只读），累计 3 轮；第 4 轮只读搜索是**获取新信息**
// ⇒ 必须重置审查计数，不得停机。
func TestGuard_ReadOnlySearchRoundIsProgress_NotReviewSpin(t *testing.T) {
	cfg := agentguard.DefaultConfig()
	cfg.MaxReviewRounds = 3
	g := agentguard.NewGuard(cfg)
	g.BeginRequest()

	for i := 1; i <= 3; i++ {
		id := fmt.Sprintf("a%d", i)
		askCall := session.ContentBlock{Type: "tool_use", ID: id, Name: "AskUser",
			Input: `{"question":"登录认证方式选哪种？","category":"dialogue-clarify"}`}
		askRes := tools.Result{ID: id, Name: "AskUser", Output: "回复我hello"}
		obs := turnObservation([]session.ContentBlock{askCall}, []tools.Result{askRes}, "")
		g.AfterTurn(obs)
		if d := g.BeforeModelCall(); d.Verdict != agentguard.VerdictProceed {
			t.Fatalf("第 %d 轮 AskUser 后不应停（阈值 3，未超限）, got %v", i, d.Verdict)
		}
	}

	// 第 4 轮：只读搜索（真机是 glob_search **/*memory*）。
	call, res := readOnlyCall("s1", "glob_search", `{"pattern":"**/*memory*"}`)
	g.AfterTurn(turnObservation([]session.ContentBlock{call}, []tools.Result{res}, ""))

	if d := g.BeforeModelCall(); d.Verdict != agentguard.VerdictProceed {
		t.Fatalf("只读搜索是「获取信息」= 实质进展，不得触发审查子循环超限停机（P0-3 误杀只读研究）,"+
			" got %v reason=%q", d.Verdict, d.Reason)
	}
}

// ---------------------------------------------------------------------------
// 第 3 层：反例守卫 —— 反复同一搜索仍是空转（before/after 必须都绿）
// ---------------------------------------------------------------------------

// TestGuard_RepeatedSameSearchStillCaughtBySpinning 反复搜同一个词 = 原地打转，
// 仍必须停机 —— 但 P0-3 降级为提醒后，**接住它的必须是 P0-4（重复检测）**。
//
// ★ 这是 P0-3 降级的**不退化守卫**：降级绝不能让真空转逃脱。
func TestGuard_RepeatedSameSearchStillCaughtBySpinning(t *testing.T) {
	cfg := agentguard.DefaultConfig()
	cfg.MaxReviewRounds = 3
	g := agentguard.NewGuard(cfg)
	g.BeginRequest()

	// 反复同一搜索：搜索目标计数累加超过 maxFileReads=5 ⇒ P0-4 rereadExceeded 成立。
	for i := 1; i <= 7; i++ {
		call, res := readOnlyCall(fmt.Sprintf("s%d", i), "glob_search", `{"pattern":"**/*memory*"}`)
		g.AfterTurn(turnObservation([]session.ContentBlock{call}, []tools.Result{res}, ""))
		if d := g.BeforeModelCall(); d.Verdict == agentguard.VerdictStop {
			if d.Report == nil || !strings.Contains(d.Report.Cause, "空转") {
				t.Fatalf("停机原因须来自 P0-4 重复检测（含「空转」），实际 %v", d.Report)
			}
			if strings.Contains(d.Report.Cause, "无文件改动") {
				t.Fatalf("不得再由 P0-3 停机（已降级为提醒），实际 %q", d.Report.Cause)
			}
			return // P0-4 接住真空转 ⇒ 守卫成立
		}
	}
	t.Fatal("反复搜同一个词 7 次仍未被判停 —— 保护退化（真空转逃脱）")
}
