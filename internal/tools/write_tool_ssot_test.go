package tools

import (
	"testing"

	"github.com/anchorandfish/whale-code/internal/permission"
)

// TestFix_IsWriteTool_SingleDeclarationBothSidesAgree 是「B1 第二半」的**跨包判别器**：
// 「目标受限写类工具」这一语义在全仓**只准有一个答案**。
//
// # 它防的是什么（收敛前实测状态）
//
// 两侧各有一份名单，且对 `bash` 的答案**直接相反** —— 更糟的是**各自的测试各自钉死了相反答案**
// （`permission/safety_test.go` 期望 true，本包 `pipeline_test.go` 期望 false）。
// 于是"同一份调用"在工作区边界闸与受保护路径检查两个面得到**不同结论**，而没有任何测试报警。
//
// # 判别性
//
// 让 `isWriteTool` 重新长出本地名单（或只改 `permission.WriteToolSet` 的一侧）
// ⇒ 本测试立刻红，并精确报出是哪个工具分了叉。
// 覆盖点包含历史上分歧的两个工具：`export_session`（pipeline 有 / permission 无）
// 与 `NotebookEdit` 的**大小写**写法（pipeline 精确匹配 / permission 归一化）。
func TestFix_IsWriteTool_SingleDeclarationBothSidesAgree(t *testing.T) {
	names := []string{
		"write_file", "edit_file",
		"NotebookEdit", "notebookedit", // ★ 历史大小写分歧
		"LspRename", "lsprename",
		"git_commit", "git_add", "git_branch",
		"export_session", // ★ 历史成员分歧
		applyUnifiedDiffToolName,
		"bash", "powershell", "monitor", "Monitor",
		"read_file", "grep_search", "todowrite", "task",
	}

	diverged := 0
	for _, n := range names {
		got, want := isWriteTool(n), permission.IsWriteTool(n)
		if got != want {
			diverged++
			t.Errorf("%q：tools.isWriteTool=%v 而 permission.IsWriteTool=%v "+
				"⇒ 同一语义两处答案不一致（B1 第二半复发）", n, got, want)
		}
	}

	// 哨兵：判别器必须**仍能打到历史上的分歧点**。若这个前提被改动，
	// 本测试就退化成一条恒真断言（看似在守，其实守不住）。
	if permission.IsWriteTool("bash") {
		t.Error(`哨兵失效：permission.IsWriteTool("bash") 为 true ⇒ 本测试已打不到历史上的分歧点`)
	}
	if !isWriteTool("export_session") {
		t.Error("哨兵失效：export_session 应为写类 ⇒ 历史分歧点被改动，请复核本测试")
	}
	t.Logf("已核 %d 个工具名，两侧逐工具答案一致（分叉 %d 处）", len(names), diverged)
}
