package tui

import (
	"strings"
	"testing"
	"time"

	"github.com/anchorandfish/whale-code/internal/session"
)

// copy_all_wysiwyg_test.go — ：Copy all 改为「所见即所得」后的契约判据。
//
// 用户真机报障：「copy all 和 tui 的显示不是完全一致」。
// 拍板：Copy all 读**显示缓冲**（displayEntries），屏幕上有的
// （UI 回执 / 轮末标记 / 思考预览）都进剪贴板；省略行按 source map 还原成
// 完整原文（与鼠标拖选同一套机制，见 transcript_source.go）。
//
// 判据分工：
// - 本文件钉「屏幕独有内容必须进剪贴板」（修前必红：旧实现只读模型侧 session）；
// - 「折叠工具输出还原成完整原文、两个复制入口同源」由
// mouse_selection_fold_gap_test.go 的 TestMouseSelectionAndCopyAllAgreeOnFoldedToolOutput
// 钉住（那条在修前修后都必须绿）。

// TestCopyAllCopiesScreenOnlyReceipts —— 只存在于显示缓冲的 UI 回执必须进剪贴板。
//
// 修前（红）：formatAllForCopy 读 CurrentSession().GetMessages()，回执不在
// 模型侧 session ⇒ 剪贴板里没有它；用户消息也是 [User] 前缀而非屏幕上的 👤 You:。
func TestCopyAllCopiesScreenOnlyReceipts(t *testing.T) {
	m := newTUIRegressionModel(t)
	// 模型侧 session 只有一条用户消息（旧实现的唯一取数源）。
	m.sessionService.CurrentSession().SetMessages([]session.Message{
		session.UserText("hello"),
	})
	// 显示缓冲 = 渲染后的用户消息 + 一条系统回执（带 ANSI；只在屏幕、不在 session）。
	m.sessionService.SetMessages([]string{
		formatUserMessage("hello", 0),
		errorStyle.Render("boom: something failed"),
	})

	got := m.formatAllForCopy()
	for _, want := range []string{"👤 You:", "hello", "boom: something failed"} {
		if !strings.Contains(got, want) {
			t.Errorf("Copy all 应包含屏幕内容 %q，实际：\n%s", want, got)
		}
	}
	if strings.Contains(got, "\x1b[") {
		t.Errorf("剪贴板不应包含 ANSI 控制序列，实际：%q", got)
	}
}

// TestCopyAllIncludesTurnBoundaryMarks —— 轮末结束标志只进显示侧（不变式 I5：
// 永不进 session / 模型侧），所见即所得要求它出现在剪贴板里。
//
// 修前（红）：旧实现读 session，边界标记不在其中。
func TestCopyAllIncludesTurnBoundaryMarks(t *testing.T) {
	m := newTUIRegressionModel(t)
	m.sessionService.SetMessages([]string{formatUserMessage("hi", 0)})
	at := time.Date(2026, 10, 12, 15, 4, 5, 0, time.UTC)
	m.turn.marks = []turnMark{{after: 1, at: at}}

	got := m.formatAllForCopy()
	want := at.Format("2006-01-02 15:04:05")
	if !strings.Contains(got, want) {
		t.Errorf("Copy all 应包含轮末结束标志（时刻 %s），实际：\n%s", want, got)
	}
}

// TestCopyAllRestoresFoldedToolOutputWithoutMarker —— 折叠行按 source map 还原为
// 完整原文；屏幕上那行「… +N lines (/tool K)」摘要不得以摘要形态进剪贴板。
//
// 与拖选入口共用同一还原机制 ⇒ 两个入口对同一条工具结果给出同一份原文。
func TestCopyAllRestoresFoldedToolOutputWithoutMarker(t *testing.T) {
	m := foldGapFixture30(t, 35)
	all := m.formatAllForCopy()

	if n := strings.Count(all, "OUT_ONE"); n != foldGapFullLines {
		t.Errorf("Copy all 应还原完整 %d 行原文，实际 %d 行", foldGapFullLines, n)
	}
	if strings.Contains(all, "… +") {
		t.Errorf("折叠省略行是屏幕摘要，不该出现在复制结果里：\n%s", all)
	}
}
