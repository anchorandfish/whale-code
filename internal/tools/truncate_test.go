package tools

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/anchorandfish/whale-code/internal/permission"
)

// 判据（先红）：截断提示必须报「真实丢弃量」，且同一输出只能有一个截断标记。
//
// 生产入口 = InitPipelineWithHooks → ExecuteWithConfirm（与 TUI 同链，见 w023 判据）。
//
// 现场动机（T-9 真机，2026-10-07）：`cat big.txt`（79,974 字节）屏幕显示
// `(truncated 23 characters)`，真实丢弃 ≈29,974。根因：50000 上限在 bash.go 与
// tools.go 两处声明、各砍一刀，第二刀把第一刀的标记切掉，报出的 23 恰是
// 上游标记串自身的长度 —— 提示自我覆盖，差三个数量级。
//
// 收口后不变量：
// 1. 砍尾只此一处权威（TruncateTail），标记带真实丢弃量；
// 2. 下游（formatResult 模型面 / foldToolResult 显示面）遇到上游已砍过的输出，
// 不再二次追加标记，而是并入上游计数（单一标记）。
func w034Executor(t *testing.T, workDir string) *Executor {
	t.Helper()
	e := NewExecutor(NewRegistry(), workDir)
	pol := permission.NewPolicy(permission.LevelWorkspaceWrite)
	e.InitPipelineWithHooks(pol, workDir)
	if e.Pipeline == nil {
		t.Fatal("precondition: 生产装配下 Pipeline 必须非 nil")
	}
	return e
}

func TestW034_TruncationCountIsTrueDroppedAmount(t *testing.T) {
	ctx := context.Background()
	workDir := t.TempDir()
	e := w034Executor(t, workDir)

	// 79,974 字节纯 ASCII（与 T-9 现场同构：字节数 == rune 数，期望丢弃量可精确计算）。
	content := strings.Repeat("row-0000-0123456789abcdef\n", 3076)
	big := filepath.Join(workDir, "big.txt")
	if err := os.WriteFile(big, []byte(content), 0o644); err != nil {
		t.Fatalf("precondition: 造 big.txt 失败: %v", err)
	}
	wantDropped := len(content) - 50000 // bash 上限 50000（ASCII ⇒ rune 数相同）

	pr := e.ExecuteWithConfirm(ctx, Call{ID: "w034-1", Name: "bash", Input: bashInput("cat " + big)})
	if pr.NeedConfirm || !pr.Proceed || pr.Result == nil {
		t.Fatalf("precondition: bash 应免问且执行; needConfirm=%v proceed=%v result=%+v",
			pr.NeedConfirm, pr.Proceed, pr.Result)
	}
	out := pr.Result.Output

	// ① 执行面（executor 出口）就该带真实丢弃量。
	wantMark := fmt.Sprintf("(truncated %d characters)", wantDropped)
	if !strings.Contains(out, wantMark) {
		t.Fatalf("W-034①：执行面输出应含真实丢弃量标记 %q; 尾部=%q", wantMark, tail(out, 120))
	}

	// ② 模型面（formatResult，喂给 LLM 的那份）。
	model := e.formatResult(out)
	if !strings.Contains(model, wantMark) {
		t.Fatalf("W-034②：模型面应保留真实丢弃量 %q; 尾部=%q", wantMark, tail(model, 120))
	}
	if strings.Contains(model, "(truncated 23 characters)") {
		t.Fatalf("W-034②：模型面不得再出现「上游标记串长度」形态的假计数; 尾部=%q", tail(model, 120))
	}
	if got := strings.Count(model, "(truncated "); got != 1 {
		t.Fatalf("W-034②：模型面只能有 1 个截断标记，实际 %d 个; 尾部=%q", got, tail(model, 120))
	}

	// ③ 显示面（foldToolResult，屏幕那份）。
	display := FormatToolResultStruct(Result{Name: "bash", Output: out})
	if !strings.Contains(display, wantMark) {
		t.Fatalf("W-034③：显示面应保留真实丢弃量 %q; 尾部=%q", wantMark, tail(display, 120))
	}
	if strings.Contains(display, "(truncated 23 characters)") {
		t.Fatalf("W-034③：显示面不得出现假计数 23（这正是 T-9 现场症状）; 尾部=%q", tail(display, 120))
	}
	if got := strings.Count(display, "(truncated "); got != 1 {
		t.Fatalf("W-034③：显示面只能有 1 个截断标记，实际 %d 个; 尾部=%q", got, tail(display, 120))
	}
}

// 单元：TruncateTail 无上游标记时的基本形态 —— 真实丢弃量 + rune 安全。
func TestW034_TruncateTailPlainCut(t *testing.T) {
	// ASCII：200 字符砍到 100 ⇒ 丢弃 100。
	out := TruncateTail(strings.Repeat("a", 200), 100)
	if got := len([]rune(out)) - len([]rune(fmt.Sprintf(truncMarkerFmt, 100))); got != 100 {
		t.Fatalf("砍后内容应恰 100 rune，实际 %d; out=%q", got, out)
	}
	if !strings.HasSuffix(out, fmt.Sprintf(truncMarkerFmt, 100)) {
		t.Fatalf("标记应报真实丢弃量 100; out=%q", out)
	}

	// 不超限 ⇒ 原样返回。
	if s := TruncateTail("abc", 100); s != "abc" {
		t.Fatalf("未超限应原样返回; got=%q", s)
	}

	// 多字节：切断点落在 CJK 中间 ⇒ 输出必须仍是合法 UTF-8。
	long := strings.Repeat("汉", 300) // 300 rune，600+ 字节
	out2 := TruncateTail(long, 250)
	if !utf8.ValidString(out2) {
		t.Fatalf("rune 砍尾不得产出非法 UTF-8; out=%q", out2)
	}
	if !strings.HasSuffix(out2, fmt.Sprintf(truncMarkerFmt, 50)) {
		t.Fatalf("CJK 丢弃量应按 rune 计 50; out=%q", out2)
	}
}

// 单元：TruncateTail 合并语义 —— 上游已带本权威标记 ⇒ 单一标记 + 计数并入。
func TestW034_TruncateTailMergesUpstreamMarker(t *testing.T) {
	content := strings.Repeat("b", 200)

	// 第一刀：砍到 100，丢弃 100。
	first := TruncateTail(content, 100)
	if !strings.HasSuffix(first, fmt.Sprintf(truncMarkerFmt, 100)) {
		t.Fatalf("precondition: 第一刀标记应为 100; out=%q", first)
	}

	// 第二刀砍得更狠（50）：丢弃量应 = 上游 100 + 本刀补砍的内容 50 = 150。
	second := TruncateTail(first, 50)
	if !strings.HasSuffix(second, fmt.Sprintf(truncMarkerFmt, 150)) {
		t.Fatalf("合并后应报总丢弃量 150; out=%q", second)
	}
	if got := strings.Count(second, "(truncated "); got != 1 {
		t.Fatalf("合并后只能有 1 个标记，实际 %d; out=%q", got, second)
	}

	// 第二刀砍得更松（110，只咬掉标记串本身）：内容不再丢，计数保持上游 100。
	third := TruncateTail(first, 110)
	if !strings.HasSuffix(third, fmt.Sprintf(truncMarkerFmt, 100)) {
		t.Fatalf("松刀只咬标记时应保持上游计数 100; out=%q", third)
	}
	if got := strings.Count(third, "(truncated "); got != 1 {
		t.Fatalf("松刀合并后只能有 1 个标记，实际 %d; out=%q", got, third)
	}
}

// 守卫：TruncationDropped 只认本权威自产的标记形态。
func TestW034_TruncationDroppedGrammar(t *testing.T) {
	if _, ok := TruncationDropped("plain output"); ok {
		t.Fatal("无标记输出不应报告丢弃量")
	}
	if _, ok := TruncationDropped("\n... (output truncated)"); ok {
		t.Fatal("旧 ad-hoc 文案（无计数）不应被当作权威标记")
	}
	marked := TruncateTail(strings.Repeat("c", 120), 20)
	got, ok := TruncationDropped(marked)
	if !ok || got != 100 {
		t.Fatalf("round-trip 应返回 (100,true)，实际 (%d,%v)", got, ok)
	}
	// 尾部有附加说明（非紧贴结尾）⇒ 不认（上游合并由 TruncateTail 在追加前完成）。
	if _, ok := TruncationDropped(marked + "\n[timeout clamped: 1s -> 2s]"); ok {
		t.Fatal("标记后还有别的内容时不应报告（避免误并入无关尾部）")
	}
}

// 守卫：模型面与显示面对「上游已标记」的输出一律不再二次追加（的结构不变量）。
func TestW034_NoDoubleAppendOnMarkedOutput(t *testing.T) {
	e := NewExecutor(NewRegistry(), t.TempDir())
	marked := TruncateTail(strings.Repeat("d", 60000), 50000)

	for name, got := range map[string]string{
		"formatResult": e.formatResult(marked),
		"foldTool":     FormatToolResultStruct(Result{Name: "bash", Output: marked}),
	} {
		if n := strings.Count(got, "(truncated "); n != 1 {
			t.Fatalf(" 守卫[%s]：上游已标记 ⇒ 只允许 1 个标记，实际 %d; 尾部=%q", name, n, tail(got, 120))
		}
	}
}
