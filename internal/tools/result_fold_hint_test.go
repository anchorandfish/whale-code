package tools

import (
	"strings"
	"testing"
)

// 本文件守卫「折叠省略行里的提示」这一条不变量：
// 提示里的**命令名**与**序号**都由调用方所处的入口决定，且提示只能有一个渲染点。
//
// 为什么值得单独立一组判据（C1 切口②）：
// 折叠提示写「输入 /tool 查看完整输出」，而无参 /tool 恒返回**最近一条**
// ⇒ 用户照着第 3 个折叠块的提示敲，拿到的是第 11 条 —— 提示指东、返回打西。
// 修法是让提示带上序号，因此 "Seq 被写进提示" 必须可判别（而不是"代码里传了 Seq"）。

// foldedOutput 造一条必然折叠的输出（> head+tail 行），带尾换行（生产形态，见）。
func foldedOutput(n int) string {
	var sb strings.Builder
	for i := 1; i <= n; i++ {
		sb.WriteString("LINE_" + string(rune('A'+i%26)) + "\n")
	}
	return sb.String()
}

func TestToolFoldHint_SeqAppearsInHint(t *testing.T) {
	got := FormatToolResultStructWithHint(
		Result{ID: "1", Name: "read_file", Output: foldedOutput(20)},
		MaxToolResultLength,
		ToolFoldHint{Command: "/tool", Seq: 3},
	)
	// ★ 反向半边：必须含带序号的新形态，且**不得**同时含无序号旧形态
	// （只断言 Contains 会漏掉"两个形态同时存在"这种假通过）。
	if !strings.Contains(got, "/tool 3") {
		t.Fatalf("提示缺序号：期望含 %q；got:\n%s", "/tool 3", got)
	}
	if strings.Contains(got, "(输入 /tool 查看完整输出)") {
		t.Fatalf("仍存在无序号旧提示（应被带序号形态取代）：\n%s", got)
	}
}

func TestToolFoldHint_NoSeqFallsBackToBareCommand(t *testing.T) {
	got := FormatToolResultStructWithHint(
		Result{ID: "1", Name: "read_file", Output: foldedOutput(20)},
		MaxToolResultLength,
		ToolFoldHint{Command: "/tool"},
	)
	if !strings.Contains(got, "(输入 /tool 查看完整输出)") {
		t.Fatalf("Seq<=0 应退回无序号形态（兼容无序号上下文的入口）；got:\n%s", got)
	}
	if strings.Contains(got, "/tool 0") {
		t.Fatalf("Seq<=0 不得拼出 0 号：\n%s", got)
	}
}

// TestToolFoldHint_EmptyCommandDoesNotPointToMissingCommand 钉住 plain 那一侧：
// --plain 里没有 /tool（internal/tui/plain.go 无 case "tool"），提示不得指向不存在的命令。
func TestToolFoldHint_EmptyCommandDoesNotPointToMissingCommand(t *testing.T) {
	got := FormatToolResultStructWithHint(
		Result{ID: "1", Name: "read_file", Output: foldedOutput(20)},
		MaxToolResultLength,
		ToolFoldHint{},
	)
	if strings.Contains(got, "/tool") {
		t.Fatalf("Command 为空时不得出现 /tool 提示（该入口没有这个命令）；got:\n%s", got)
	}
	if !strings.Contains(got, "+10 lines") {
		t.Fatalf("Command 为空仍应折叠并报告省略行数；got:\n%s", got)
	}
}

// TestToolFoldHint_PublicEntryUnchanged 回归：既有导出入口行为不变（无序号形态）。
func TestToolFoldHint_PublicEntryUnchanged(t *testing.T) {
	got := FormatToolResultStruct(Result{ID: "1", Name: "read_file", Output: foldedOutput(20)})
	if !strings.Contains(got, "(输入 /tool 查看完整输出)") {
		t.Fatalf("FormatToolResultStruct 应保持现行形态（无序号）；got:\n%s", got)
	}
}
