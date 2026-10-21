package tools

// —— `sleep` 阻断提示里那个秒数，必须**真的被放行**。
//
// ★ 编号沿革：本条在 `whale-code/内部问题台账` 里**曾登记为 **，与并发会话
// 同日登记的「面板批准下传」（`sanction_chokepoint_test.go`）**撞号**。
// 批 B 施工单 §1 建议顺延为 ****（总账改号是单独一步，本文件先按 命名）。
//
// 病历（`I-13.8` 转录复核）：模型第一次用 `sleep 2` 起 Monitor 被拦，
// 拦它的话却写「请保持在 **2 秒以内**」⇒ 照提示做（`sleep 2`）**再被拒一次**，白烧一轮。
//
// ★ 本文件考的不是"字符串改对了没"，而是 **文案与判定是否同源**：
// 从**实际渲染出来的提示文本**里机械读出它声明的整数上界 N，断言 `sleep N` **必须被放行**。
// - 修前：中文提示「2 秒以内」⇒ N=2 ⇒ `sleep 2` 实测被拒 ⇒ **红**（就是用户看到的那个现象）。
// - 修后：提示「1 秒及以下」⇒ N=1 ⇒ `sleep 1` 放行 ⇒ 绿。
// ★ 承重点：若只把字符串改对、数字仍是手写的，**下次任何人改上界，本条会再次变红**。
//
// ★ 本文件**不引用任何新增符号** ⇒ 修前即可编译 ⇒ 红是真语义红（不是 R11 的"编译失败"）。
// 引用了新增符号 `monitor.MaxAllowedSleepSeconds` 的那条单独放在
// `sleep_hint_single_source_test.go`（分工理由见那文件头）。
//
// 生产入口（R1）：`newProdExecutor`（NewExecutor 内建 Sandbox+MonitorMgr；
// p0_sandbox_precheck_test.go:68）+ `InitPipelineWithHooks` + `ExecuteWithConfirm`
// （与 n122 / p0 同形）。提示文本取 `PipelineResult.Result.Output` —— handler 的错误
// 正文被 `executeDirect` 拼进 Output（executor.go:829-840），不是函数返回值。

import (
	"context"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"regexp"
	"strconv"
	"strings"
	"testing"
)

// w056HintLimitRe 从渲染出的提示文本里抽「声明的整数上界」：
// 中文「N 秒」/ 英文「N second」两种写法。读不出整数 ⇒ 这句话不可判别。
var w056HintLimitRe = regexp.MustCompile(`(\d+)\s*(?:秒|second)`)

// w056SleepGuardFired 判定「sleep 闸」是否拦下了这条命令。
//
// 用**正向留痕**（闸自己印的原因词）而不是"没出现某个坏串"——后者会被
// 链路上任何更早的拒绝蒙对（R3「抑制类改动的负向断言易假绿」）。
func w056SleepGuardFired(out string) bool {
	return strings.Contains(out, "sleep at start of command") ||
		strings.Contains(out, "sleep in command chain")
}

func w056OutputOf(pr *PipelineResult) string {
	if pr == nil {
		return ""
	}
	if pr.Result != nil && pr.Result.Output != "" {
		return pr.Result.Output
	}
	return pr.Rejection
}

func w056RunSleep(t *testing.T, e *Executor, tool, cmd string) *PipelineResult {
	t.Helper()
	return e.ExecuteWithConfirm(context.Background(), Call{
		ID:    "w056-" + tool,
		Name:  tool,
		Input: mustMarshal(map[string]interface{}{"command": cmd}),
	})
}

func w056NewEnv(t *testing.T) *Executor {
	t.Helper()
	e := newProdExecutor(t, t.TempDir())
	if e.MonitorMgr != nil {
		t.Cleanup(e.MonitorMgr.StopAll)
	}
	e.OnConfirm = func(string, string) bool { return true }
	return e
}

// TestW056_SleepHintSaysWhatTheGateActuallyAllows 是本单的**主判据**（：两侧都验）。
//
// 判别性：修前中文提示「保持在 2 秒以内」⇒ N=2 ⇒ `sleep 2` 被 `>= 2` 的闸拦下 ⇒ 本判据红。
func TestW056_SleepHintSaysWhatTheGateActuallyAllows(t *testing.T) {
	for _, tool := range []string{"bash", "Monitor"} {
		t.Run(tool, func(t *testing.T) {
			e := w056NewEnv(t)

			// ① 触发阻断，取**渲染出来的**提示。
			hint := w056OutputOf(w056RunSleep(t, e, tool, "sleep 2"))
			if !w056SleepGuardFired(hint) {
				t.Fatalf("前提不成立：`sleep 2` 未被 sleep 闸拦下，本测试失去意义：\n%s", hint)
			}

			// ② 从提示里机械读出整数上界 N。
			m := w056HintLimitRe.FindStringSubmatch(hint)
			if m == nil {
				t.Fatalf("提示文本里读不出整数上界 ⇒ 这句话不可判别：\n%q", hint)
			}
			n, err := strconv.Atoi(m[1])
			if err != nil {
				t.Fatalf("解析上界失败: %v", err)
			}

			// ③ 照提示做：`sleep N` 必须被放行。
			out := w056OutputOf(w056RunSleep(t, e, tool, fmt.Sprintf("sleep %d", n)))
			if w056SleepGuardFired(out) {
				t.Fatalf("提示说上界是 %d 秒，但 `sleep %d` 仍被 sleep 闸拦下 —— "+
					"文案与判定不同源（用户照提示做会再被拒一次，）：\n%s", n, n, out)
			}
		})
	}
}

// TestW056_SleepHintHasNoHardcodedSeconds 是同源面的**结构判据**：
// `internal/tools/{monitor,bash}.go` 的**字符串字面量**里不得出现「数字+秒/second」——
// 秒数只能经 `SleepBlock.LimitSeconds`（= monitor.MaxAllowedSleepSeconds）插值。
//
// 只扫**字符串字面量**（AST），不扫注释 —— 注释里写病历（"2 秒以内"）是允许的。
//
// 判别性：修前 `bash.go:818` 有 `"…keep it under 2 seconds."`、
// `monitor.go:89` 有 `"…请保持在 2 秒以内"` ⇒ 本判据**红**；修后（`%d`）绿。
// （本判据**不引用新符号** ⇒ 修前即可编译，红是真语义红。）
//
// ⚠️ 边界：本判据只挡"字面量写死"这一种复发形态；若有人用 `strconv.Itoa(2)+" seconds"`
// 绕开，它测不出 —— 那一路由 `sleep_hint_single_source_test.go` 的数值同源判据兜。
func TestW056_SleepHintHasNoHardcodedSeconds(t *testing.T) {
	re := regexp.MustCompile(`\d+\s*(?:秒|seconds?)`)
	for _, f := range []string{"monitor.go", "bash.go"} {
		t.Run(f, func(t *testing.T) {
			fset := token.NewFileSet()
			af, err := parser.ParseFile(fset, f, nil, 0)
			if err != nil {
				t.Fatalf("解析 %s 失败（改名/删除会让本守卫静默失效）: %v", f, err)
			}
			if af.Name == nil || af.Name.Name != "tools" {
				t.Fatalf("本判据只对 package tools 的文件有效，实际 %v", af.Name)
			}
			found := 0
			ast.Inspect(af, func(n ast.Node) bool {
				lit, ok := n.(*ast.BasicLit)
				if !ok || lit.Kind != token.STRING {
					return true
				}
				if re.MatchString(lit.Value) {
					found++
					t.Errorf("%s:%d 字符串字面量里写死了秒数（应为 %%d + SleepBlock.LimitSeconds）: %s",
						f, fset.Position(lit.Pos()).Line, lit.Value)
				}
				return true
			})
			if found == 0 {
				t.Logf("%s: 未发现写死的秒数字面量 ✓", f)
			}
		})
	}
}
