package tools

// error_prefix_test.go — 第 4 件：错误消息**双重工具名前缀**。
//
// 真机（T-14b 22:13）所见：
//
//	❌ apply_unified_diff: tool execution failed: apply_unified_diff: patch conflict: …
//	└─ 外层渲染头（本仓加的） └─ 错误自身的结构化前缀（管线加的）—— 工具名出现两次
//
// 本判据只钉**显示冗余**这一面（措辞/包装分层属 (c)，不在本项）：
// 折叠显示里工具名只允许出现一次。

import (
	"strings"
	"testing"
)

func TestW029_ErrorDisplay_ToolNameAppearsOnce(t *testing.T) {
	got := FormatToolResultStruct(Result{
		ID:      "1",
		Name:    "apply_unified_diff",
		Output:  "tool execution failed: apply_unified_diff: patch conflict: hunk #1 FAILED at 1",
		IsError: true,
	})
	if n := strings.Count(got, "apply_unified_diff"); n != 1 {
		t.Fatalf("错误显示里工具名应恰好出现 1 次（ 第4件），实际 %d 次：\n%s", n, got)
	}
	if !strings.Contains(got, "❌") {
		t.Fatalf("错误显示应保留 ❌ 标识：\n%s", got)
	}
}

// 对照组（护栏）：错误 payload 自身**不含**工具名时，渲染头仍须补上工具名
// —— 防止「为了去重把工具名整体删掉」把非结构化的普通失败也变成无主错误。
func TestW029_ErrorDisplay_KeepsNameForPlainErrors(t *testing.T) {
	got := FormatToolResultStruct(Result{
		ID:      "2",
		Name:    "bash",
		Output:  "exit status 1",
		IsError: true,
	})
	if !strings.Contains(got, "bash") {
		t.Fatalf("非结构化错误仍须带工具名（否则用户不知道谁失败）：\n%s", got)
	}
}
