package tools

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// (c) 判别器：**命令返回非零 ≠ 工具坏了**。
//
// 现场（总账 明细 (c)）：`go test` 因断言失败返回 exit 1（命令**正常跑完**，
// 它的答案就是"失败"）⇒ 界面显示 `❌ tool execution failed`。模型/用户把这句
// 误读成"whale 的工具坏了"。`apply_unified_diff` 的设计内冲突也吃同一句。
//
// 根因：exit code 被塞进 error 通道，而 error 通道在 `executor.go` **一律**等于
// 「工具故障」⇒ **一个正常结果被报成基础设施故障**（R4：结构化事实被压成散文短语）。
//
// 修法：给"命令非零退出"一个**结构化种类** `FailureKindCommandExit`（Terminal=false，
// 同 patch_conflict 档），措辞分层收敛到唯一声明 `ToolErrorOutput`。
//
// 生产入口 = `InitPipelineWithHooks` → `ExecuteWithConfirm`（与 TUI 同一条链）。

// 判别器 ①：非零退出走**数据口吻**，且带结构化种类。
func TestW018c_NonZeroExitIsDataNotToolFailure(t *testing.T) {
	ctx := context.Background()
	workDir := t.TempDir()
	e, _ := w023Executor(t, workDir)

	// 走**生产确认通道**放行：`sh -c` 不在只读白名单内 ⇒ 会先到确认阶段。
	// `e.OnConfirm` 正是 TUI 面板批准所设的同一条通道（executor.go:980），
	// 批准后管线以 `Approved=true` 重跑 —— 与本 bug 无关的权限面被正常走完，
	// 断言只打在"命令真跑完后，非零退出如何被报出"上。
	e.OnConfirm = func(toolName, input string) bool { return true }

	// 样本：三条命令都**真正跑完**，且它们的答案就是"非零退出"。
	// `cat`/`grep` 读不到目标 ⇒ 退出码 1；`sh -c 'exit 3'` ⇒ 退出码 3（换一个码，
	// 证明"退出码是**如实转述**的，不是硬编码 1"）。
	missing := filepath.Join(workDir, "definitely-not-here.txt")
	cases := []struct{ name, cmd, wantCode string }{
		{"cat-missing-file", "cat " + missing, "status 1"},
		{"grep-no-match", "grep zzz_no_such_pattern " + writeFile(t, workDir, "hay.txt", "nothing here\n"), "status 1"},
		{"exit-3", "sh -c 'exit 3'", "status 3"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			pr := e.ExecuteWithConfirm(ctx, Call{ID: "w018c-" + tc.name, Name: "bash", Input: bashInput(tc.cmd)})
			if pr.Result == nil {
				t.Fatalf("(c)：命令未被执行到（Result=nil）⇒ 本用例不构成判别；proceed=%v needConfirm=%v logs=%+v",
					pr.Proceed, pr.NeedConfirm, pr.StageLogs)
			}
			res := *pr.Result
			if !res.IsError {
				t.Errorf("(c)：非零退出仍须 IsError=true（模型必须知道命令失败了）")
			}
			if strings.Contains(res.Output, "tool execution failed") {
				t.Fatalf("(c)：命令**跑完了**、它的答案是失败 ⇒ 不得报成「工具坏了」；实际 Output=%q", res.Output)
			}
			if !strings.Contains(res.Output, tc.wantCode) {
				t.Errorf("(c)：退出码必须**如实可见**（模型据此判断是「测试失败」还是「没命中」）；"+
					"期望含 %q，实际 Output=%q", tc.wantCode, res.Output)
			}
			if res.FailureKind != FailureKindCommandExit {
				t.Errorf("(c)：应带结构化种类 command_exit，实际 %q", res.FailureKind)
			}
			if res.FailureKind.Terminal() {
				t.Errorf("(c)：command_exit 不得是终局拒绝 —— 一次可自愈的命令失败不得掐掉用户回合")
			}
			// 显示层去重的前提：payload 自报工具名 ⇒ 渲染头不再重复（第 4 件）
			if !errOutputSelfLabels(res.Output, "bash") {
				t.Errorf("(c)：数据口吻应自带 `<工具名>:` 前缀（供显示层去重），实际 %q", res.Output)
			}
			t.Logf("%-22s -> %s", tc.name, firstLine(res.Output))
		})
	}
}

// ★ 反例（判别力）：**真故障**仍须走故障口吻、且**不得**被贴上结构化普通失败标记。
//
// 没有这一条，"把所有 error 都说成数据"的过度修复也能让 ① 全绿 ——
// 而那会把真正的工具故障（handler 崩溃、依赖缺失）也说成"这是命令的结果"。
//
// 手法：只替换**副作用出口**（bash handler）为真故障，裁决链 100% 是生产码
// （与技能 production-path-tdd 的"sink 桩"同款；断言只打在 Result 的措辞与种类上）。
func TestW018c_RealFaultStillUsesToolExecutionFailed(t *testing.T) {
	ctx := context.Background()
	workDir := t.TempDir()
	e, _ := w023Executor(t, workDir)

	e.mu.Lock()
	e.handlers["bash"] = func(ctx context.Context, raw json.RawMessage) (string, error) {
		return "partial output", errors.New("injected real fault: dependency missing")
	}
	e.mu.Unlock()

	pr := e.ExecuteWithConfirm(ctx, Call{ID: "w018c-fault", Name: "bash", Input: bashInput("cat go.mod")})
	if pr.Result == nil {
		t.Fatalf("(c) 反例：未执行到 handler；proceed=%v needConfirm=%v logs=%+v",
			pr.Proceed, pr.NeedConfirm, pr.StageLogs)
	}
	res := *pr.Result
	if !strings.Contains(res.Output, "tool execution failed") {
		t.Fatalf("(c) 反例：**未知错误 / 真故障**仍须走故障口吻（否则两类被合并成一类）；实际 %q", res.Output)
	}
	if res.FailureKind != "" {
		t.Errorf("(c) 反例：真故障不得被贴上结构化「普通失败」标记，实际 %q", res.FailureKind)
	}
	if !strings.Contains(res.Output, "partial output") {
		t.Errorf("(c) 反例：既有语义（错误时仍带上已产出的 output）不得丢；实际 %q", res.Output)
	}
}

// 单一声明（`ToolErrorOutput`）的表驱动判据 —— 措辞与"哪一类失败"必须同源。
//
// 这条钉的是"分层规则本身"，与上面两条 E2E 互为交叉验证：
// 若有人把规则改回"一律 tool execution failed"，或改成"一律数据口吻"，本表必红其一。
func TestW018c_WordingLayeringIsSingleSourced(t *testing.T) {
	base := errors.New("boom")
	cases := []struct {
		name           string
		kind           FailureKind
		wantContains   string
		wantNotContain string
	}{
		{"command_exit-数据口吻", FailureKindCommandExit, "bash: ", "tool execution failed"},
		{"patch_conflict-数据口吻", FailureKindPatchConflict, "apply_unified_diff: ", "tool execution failed"},
		{"未知错误-故障口吻", "", "tool execution failed", ""},
		{"未注册名字-按未知处理", FailureKind("nonexistent_kind"), "tool execution failed", ""},
		{"终局种类-故障口吻", FailureKindSandboxDenied, "tool execution failed", ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := ToolErrorOutput("bash", tc.kind, base)
			if tc.kind == FailureKindPatchConflict {
				got = ToolErrorOutput("apply_unified_diff", tc.kind, base)
			}
			if !strings.Contains(got, tc.wantContains) {
				t.Errorf("应含 %q，实际 %q", tc.wantContains, got)
			}
			if tc.wantNotContain != "" && strings.Contains(got, tc.wantNotContain) {
				t.Errorf("不得含 %q，实际 %q", tc.wantNotContain, got)
			}
		})
	}
}

// 同源面：`ToolExecError.Terminal()` 必须跟随 `FailureKind.Terminal()`（不得各判各的）——
// 这条是"不掐回合"的最后一环：管线/TUI 读的是前者。
func TestW018c_ToolExecErrorTerminalFollowsKind(t *testing.T) {
	if (&ToolExecError{Tool: "bash", Kind: FailureKindCommandExit}).Terminal() {
		t.Error("ToolExecError.Terminal 未跟随 FailureKind.Terminal ⇒ 命令非零退出会掐掉用户回合")
	}
}

// ---------------------------------------------------------------- helpers

func writeFile(t *testing.T, dir, name, content string) string {
	t.Helper()
	p := filepath.Join(dir, name)
	if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
		t.Fatalf("precondition: 造文件失败: %v", err)
	}
	return p
}

func firstLine(s string) string {
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		return fmt.Sprintf("%s …", s[:i])
	}
	return s
}
