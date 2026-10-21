package tui

import (
	"strings"
	"testing"

	"github.com/anchorandfish/whale-code/internal/session"
	tea "github.com/charmbracelet/bubbletea"
)

// mouse_selection_fold_gap_test.go — 钉住「两个复制入口必须同源」这条不变量。
//
// 用户真机报障：「tui 页面上中间有一段我复制不了」。
//
// 根因：两条复制路径本来取的不是同一份事实 ——
// - 鼠标拖选（mouse_selection.go mouseSelectionText）取**渲染后的视口文本**；
// 工具结果在渲染层被折成 head5+tail5（tools.go foldToolResult，
// 入口 tool_render.go formatToolResultMessage）⇒ 中间 N 行根本不在屏幕上。
// - Copy all / /copy N / /tool N（format.go formatAllForCopy / handleToolCommand）取
// **session 权威源**（session.ToolResult 的 Output 恒为完整输出）⇒ 完整、未折叠。
//
// ★ 2026-10-05 已修（本文件第 1 条判据**有意翻转**，下一个人别当回归）：
// 修复前它钉的是「拖选只拿到 head5+tail5（中段复制不到）」，那正是要被修掉的缺陷。
// 现在拖选会按 tools.ToolFoldMarkerFor 给出的省略行字面，把中段还原成原文
// （映射实现见 mouse_selection_copy_source.go）—— 两个入口对同一条工具结果给出同一份原文。
//
// 判据面分工（改任一处请对照着读）：
// - 本文件的 5 条在 `internal/tui` 走**生产入口**（Update(tea.MouseMsg) / formatAllForCopy）；
// - 「中段原文 == 被省略掉的那几行」由 `internal/tools/tool_fold_marker_test.go` 钉住。

const foldGapFullLines = 30

// foldGapFixture 造一条必折叠的工具结果（30 行 > head5+tail5），并让两个复制入口
// 都能取到它：display 侧喂 sessionService（渲染用），session 侧喂 CurrentSession（copy 用）。
//
// 渲染调用与 lifecycle.go 的 resume/fork 重建**逐字同形**（含序号取自 toolResultSeq），
// 否则复制侧的省略行定位会与实际显示的字面失配（R2：判据必须落在生产入口层）。
func foldGapFixture(t *testing.T, height int, output string) *Model {
	t.Helper()
	m := newTUIRegressionModel(t)
	res, _ := m.Update(tea.WindowSizeMsg{Width: 120, Height: height})
	mm := res.(Model)
	mm.reassignServices()

	toolMsg := session.ToolResult("id-1", "bash", output, false)
	sessionMsgs := []session.Message{session.UserText("run it"), toolMsg}
	mm.sessionService.CurrentSession().SetMessages(sessionMsgs)

	toolSeq := newToolResultSeq(sessionMsgs)
	// ★ 批 C 更新：夹具入口由「setViewportContent(wrapViewportContent(
	// renderMessages()))」改为**新的生产唯一入口** refreshTranscriptViewport，并让
	// 工具结果走带 source map 的产地（formatToolResultMessageSource）。
	// —— 断言一条没放宽：这 5 条仍是本批的回归护栏，只是"生产入口"本身换了一句。
	toolDisplay, toolSrcs := formatToolResultMessageSource(toolMsg, toolSeq.seqOf(toolMsg, 0))
	mm.sessionService.SetMessagesWithSources(
		[]string{formatUserMessage("run it", 0), toolDisplay},
		[][]lineSource{nil, toolSrcs},
	)
	mm.refreshTranscriptViewport()
	mm.uiService.Viewport().GotoTop()
	mm.recalcViewportHeight()
	return &mm
}

func foldGapFixture30(t *testing.T, height int) *Model {
	t.Helper()
	return foldGapFixture(t, height, strings.Repeat("OUT_ONE\n", foldGapFullLines))
}

// foldMarkerContentLine 返回渲染产物里折叠省略行所在的**内容行号**；不存在 ⇒ -1。
func foldMarkerContentLine(m *Model) int {
	for i, line := range strings.Split(m.viewportContent, "\n") {
		if strings.Contains(line, "… +") {
			return i
		}
	}
	return -1
}

// dragSelectToBottom 从**内容行** startContentY 拖到聊天区底部（触发边缘自动滚动到内容末尾），
// 返回「将写入剪贴板的文本」与**拖选后的 Model**。全程走生产鼠标入口（Update(tea.MouseMsg)）。
//
// ⚠️ 必须把拖选后的 Model 交回调用方：bubbletea 的 Update 是**值语义**（返回新 Model），
// 在原始 Model 上读 mouseSelection 会读到**未更新的旧值** —— 那样的断言恒真/恒假，
// 是典型的假判据（本文件第一次跑就踩到，见 git 历史中的 TestMouseSelectionRecordsFoldOverride）。
func dragSelectToBottom(t *testing.T, m *Model, startContentY, startX int) (string, Model) {
	t.Helper()
	lm := m.measureScreenLayout()
	if !lm.valid {
		t.Fatal("夹具不健全：几何账本不可用")
	}
	screenY := lm.chatTop + (startContentY - m.uiService.Viewport().YOffset)
	if screenY < lm.chatTop || screenY >= lm.inputTop {
		t.Fatalf("夹具不健全：内容行 %d 不在屏幕内（screenY=%d chatTop=%d inputTop=%d）",
			startContentY, screenY, lm.chatTop, lm.inputTop)
	}
	res, _ := m.Update(tea.MouseMsg{X: startX, Y: screenY, Type: tea.MouseLeft, Button: tea.MouseButtonLeft, Action: tea.MouseActionPress})
	mm := res.(Model)
	if !mm.mouseSelection.dragging {
		t.Fatal("夹具不健全：press 未进入拖选态")
	}
	bottomY := lm.inputTop - 1
	res2, cmd := mm.Update(tea.MouseMsg{X: 200, Y: bottomY, Type: tea.MouseLeft, Button: tea.MouseButtonLeft, Action: tea.MouseActionMotion})
	mm = res2.(Model)
	ticks := 0
	for cmd != nil && ticks < 80 {
		res3, c := mm.Update(mouseSelectionAutoScrollMsg{})
		mm = res3.(Model)
		cmd = c
		ticks++
	}
	return mm.mouseSelectionText(), mm
}

// TestMouseSelectionCopiesFullFoldedMiddle —— ★ 有意翻转。
//
// 翻转前（缺陷态）钉的是：拖选折叠块只拿到 head5+tail5，中段 N 行复制不到。
// 翻转后钉的是修复目标：拖选把**整段原文**（30 行）交出来，屏幕上那行
// 「… +N lines (/tool K)」不再以摘要形态出现在剪贴板里。
func TestMouseSelectionCopiesFullFoldedMiddle(t *testing.T) {
	m := foldGapFixture30(t, 35)
	if idx := foldMarkerContentLine(m); idx < 0 {
		t.Fatal("夹具不健全：渲染产物里没有折叠省略行，本判据不可判别")
	}
	got, _ := dragSelectToBottom(t, m, 0, 0)

	if n := strings.Count(got, "OUT_ONE"); n != foldGapFullLines {
		t.Errorf("拖选应拿到完整 %d 行原文，实际 %d 行（少 %d 行）",
			foldGapFullLines, n, foldGapFullLines-n)
	}
	if strings.Contains(got, "… +") {
		t.Errorf("折叠省略行是屏幕摘要，不该出现在复制结果里：\n%s", got)
	}
}

// TestMouseSelectionRecordsFoldOverride 钉住"映射真的建立了"，而不是"恰好没崩"。
//
// 为什么单独写：本修复的失败形态是**静默失配** —— 渲染侧与实际显示的省略行字面
// 一旦不一致（hint 口径分叉、序号口径分叉、wrap 折断了省略行），映射会安静地返回 nil，
// 行为退回修前。只断言"复制结果里有 30 行"在失配时同样会红（好），
// 但断言映射本身命中 1 处，能把"映射这条链路活着"与"结果碰巧对"分开
// （R4-7：判别器要指向机制，不是指向结果）。
func TestMouseSelectionRecordsFoldOverride(t *testing.T) {
	m := foldGapFixture30(t, 35)
	_, after := dragSelectToBottom(t, m, 0, 0) // 触发一次拖选（快照在 press 时建立）

	if n := countLineSources(after.mouseSelection.sources); n != 1 {
		t.Fatalf("折叠省略行的原文登记应命中 1 处，实际 %d 处 ⇒ 渲染产地与复制层之间的登记链路已断"+
			"（此时拖选会静默退回「只复制 head5+tail5」）", n)
	}
}

// TestMouseSelectionFoldedMiddleAtBoundary 钉住"选区边界正好落在折叠行上"这一格。
//
// 拖到折叠行**起头**时，那一行是 start.y（边界行）。边界行的常规处理是按显示列切片，
// 而折叠行的可见字面是摘要、列号与原文无对应关系 ⇒ 必须整段还原，不能切片。
func TestMouseSelectionFoldedMiddleAtBoundary(t *testing.T) {
	m := foldGapFixture30(t, 35)
	markerLine := foldMarkerContentLine(m)
	if markerLine < 0 {
		t.Fatal("夹具不健全：渲染产物里没有折叠省略行，本判据不可判别")
	}
	got, _ := dragSelectToBottom(t, m, markerLine, 0)

	// 中段 20 行 + tail 5 行 = 25 行，全部来自原文。
	if n := strings.Count(got, "OUT_ONE"); n != foldGapFullLines-5 {
		t.Errorf("从折叠行起拖到底应拿到中段+尾段 = %d 行，实际 %d 行",
			foldGapFullLines-5, n)
	}
	if strings.Contains(got, "… +") {
		t.Errorf("边界上的折叠行也应整段还原，不该把摘要交出去：\n%s", got)
	}
}

// TestMouseSelectionUnfoldedOutputUnchanged 钉住不变式 I-B：
// 未发生折叠的内容**逐字不变** —— 映射只覆盖折叠省略行，不碰其它任何行。
func TestMouseSelectionUnfoldedOutputUnchanged(t *testing.T) {
	m := foldGapFixture(t, 35, "alpha\nbeta\ngamma\n")
	if idx := foldMarkerContentLine(m); idx >= 0 {
		t.Fatal("夹具不健全：短输出不该出现折叠省略行，本判据不可判别")
	}
	got, after := dragSelectToBottom(t, m, 0, 0)

	if n := countLineSources(after.mouseSelection.sources); n != 0 {
		t.Errorf("未折叠内容不该登记任何原文覆盖，实际 %d 处", n)
	}
	for _, want := range []string{"alpha", "beta", "gamma"} {
		if !strings.Contains(got, want) {
			t.Errorf("未折叠内容应逐字保留 %q，实际：\n%s", want, got)
		}
	}
}

// TestMouseSelectionAndCopyAllAgreeOnFoldedToolOutput 钉住不变式 I-A ——
// 两个复制入口对同一条工具结果给出**同一份原文**（本文件存在的全部意义）。
//
// ⚠️ 只写 parity（拖选 == Copy all）是不够的（R4-11：两者若共享同一个坏源会一起变错），
// 故同时钉**内容**：两边都必须是整整 30 行 OUT_ONE。
func TestMouseSelectionAndCopyAllAgreeOnFoldedToolOutput(t *testing.T) {
	m := foldGapFixture30(t, 35)
	drag, _ := dragSelectToBottom(t, m, 0, 0)
	all := m.formatAllForCopy()

	dragN := strings.Count(drag, "OUT_ONE")
	allN := strings.Count(all, "OUT_ONE")
	if allN != foldGapFullLines {
		t.Fatalf("Copy all 应给出完整 %d 行（session 权威源），实际 %d 行", foldGapFullLines, allN)
	}
	if dragN != allN {
		t.Errorf("两个复制入口不同源：拖选 %d 行 vs Copy all %d 行", dragN, allN)
	}
}
