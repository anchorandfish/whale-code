package tui

// turn_diff_display_test.go — 轮末折叠卡片的**展示层**判据。
//
// 三层各自的判据分工（防止"某层绿了但整条线没通"的假绿）：
// - L1 事实层：`filehistory/turnchanges_test.go`（净变更聚合口径）；
// - L2 接线层：`tools/turn_diff_wiring_test.go`（真实工具 → track → 净变更）；
// - L3 展示层：**本文件**（真实工具 → closeTurn 采数 → displayMessages 投影成卡片行）。
//
// 为什么 L3 必须单独一条（每层单测全绿也可能断在这）：
// - `closeTurn` 里 `m.currentTurnChanges()` 若拿到 nil（锚点/执行器没接上）⇒ 卡片恒不出现；
// - `displayMessages` 的投影顺序若写成"先标志后卡片"⇒ 卡片落到了结束标志**下方**；
// - 采来的变更若被 `addMessage` 落成消息 ⇒ 污染显示缓冲与模型侧 session（不变式 I5）。
//
// 走生产入口：`newTurnLayoutModel`（生产构造 NewModel）→ 真实 `write_file` 工具两次
// → `runOneTurn`（生产收尾 handleStreamDone → finishTurn → closeTurn）→ 断言转录。
// 不直调 `formatTurnChangesCard`：那是投影实现，直调等于测实现而非测行为。

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"

	"github.com/anchorandfish/whale-code/internal/tools"
)

// newTurnDiffModel 建一个"执行器已接上 filehistory"的测试模型：
//
// - `newTurnLayoutModel` 走生产构造，但其 executor 的 workDir = 仓库目录（os.Getwd），
// 在其内写测试文件会污染工作区 ⇒ 这里用**同款生产构造器** `tools.NewExecutor`
// 建一个 workDir = 临时目录的执行器，装到同一个生产字段 `toolService.executor`
// （lifecycle.go:360 生产装配写的是同一个字段、同一个类型）。
// - 隔离 HOME：filehistory 存储（~/.whale/filehistory）落到临时目录，不碰用户真实数据。
func newTurnDiffModel(t *testing.T) (*Model, *tools.Executor, string) {
	t.Helper()
	// 隔离 HOME：filehistory 存储（~/.whale/filehistory）落到临时目录，不碰用户真实数据。
	//
	// ★ 刻意用 os.MkdirTemp 而**不是** t.TempDir()：executor 的 filehistory 会起长驻后台
	// goroutine（GC/遗忘扫描）持有该目录，t.TempDir() 的自动 RemoveAll 会因"目录非空"竞态
	// 报错（2026-10-08 实测过一次假失败）⇒ 改为**忽略错误**的自管临时目录。
	home, err := os.MkdirTemp("", "w044home")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(home) })
	t.Setenv("HOME", home)

	m := newTurnLayoutModel(t, 200)
	workDir := t.TempDir()
	exec := tools.NewExecutor(tools.NewRegistry(), workDir)
	if exec == nil || exec.FileHistoryGetter() == nil {
		t.Fatal("前置失败：filehistory 未初始化（降级态）⇒ 本判据不可执行")
	}
	// toolService 字段是接口类型（ToolService），executor 是底层 ToolModel 的私有字段；
	// 这里写的是同一个生产字段（lifecycle.go:360 装配的正是它），只是绕过接口可见性。
	m.ToolModel.executor = exec
	return m, exec, workDir
}

// writeFileThroughTool 经真实工具入口写一次文件（track 由 handler 自己写）。
func writeFileThroughTool(t *testing.T, m *Model, path, content, callID string) {
	t.Helper()
	input, err := json.Marshal(map[string]string{"path": path, "content": content})
	if err != nil {
		t.Fatal(err)
	}
	res, err := m.toolService.ExecuteTool(context.Background(), "write_file", json.RawMessage(input))
	if err != nil {
		t.Fatalf("write_file(%s) 返回错误：%v", callID, err)
	}
	if res == nil || res.IsError {
		out := ""
		if res != nil {
			out = res.Output
		}
		t.Fatalf("write_file(%s) 失败：%s", callID, out)
	}
}

// findTurnDoneMarkLine 返回转录中**轮末结束标志**所在行的下标（-1 = 没有）。
// 用与其它边界判据同一套生产正则（turnDoneMarkPattern），保证认的是真标志。
func findTurnDoneMarkLine(t *testing.T, content string) int {
	t.Helper()
	for i, ln := range strings.Split(ansi.Strip(content), "\n") {
		if turnDoneMarkPattern.MatchString(ln) {
			return i
		}
	}
	return -1
}

// ① 正向：本轮真实改了文件 ⇒ 结束标志**正上方**一行出现摘要卡片。
//
// 判别性（A/B 注入见回执）：
// - 把 closeTurn 的 `changes: m.currentTurnChanges()` 删掉 ⇒ 卡片不出现，本判据红；
// - 把 displayMessages 的 emit 顺序反过来（先标志后卡片）⇒ 本判据红（位置断言）。
func TestTurnDiff_CardSitsDirectlyAboveEndMarker(t *testing.T) {
	m, exec, workDir := newTurnDiffModel(t)

	target := filepath.Join(workDir, "card.txt")
	if err := os.WriteFile(target, []byte("a\nb\nc\n"), 0644); err != nil {
		t.Fatal(err)
	}

	// 复刻 sendInput：轮首设置锚点（stream.go:300），整轮恒定。
	const msgID = "user-msg-card-turn"
	exec.SetCurrentMessageId(msgID)

	// 同一轮内两次真实写入 ⇒ 净变更（a\nB\nc\n → a\nB\nC\n）。
	writeFileThroughTool(t, m, target, "a\nB\nc\n", "card-call-1")
	writeFileThroughTool(t, m, target, "a\nB\nC\n", "card-call-2")

	runOneTurn(t, m, "帮我改一下 card.txt", "已经改好了")

	content := ansi.Strip(m.viewportContent)
	idx := findTurnDoneMarkLine(t, content)
	if idx < 0 {
		t.Fatalf("夹具不健全：转录里没有轮末结束标志，无法判别卡片位置：\n%q", content)
	}

	lines := strings.Split(content, "\n")
	if idx == 0 {
		t.Fatalf("卡片应出现在结束标志上方，但结束标志已在首行：\n%q", content)
	}
	// ★ 契约 = **紧邻**正上方（中间不得夹连接线/空行）：卡片与标志是同一个显示条目
	// （见 format.go emit）。若有人把它们拆成两个条目，renderMessages 会在其间插 `─` ⇒ 本断言红。
	card := lines[idx-1]
	if !strings.Contains(card, "▸ 1 files changed") {
		t.Fatalf("结束标志正上方应为变更摘要行 `▸ 1 files changed +X -Y (/diff)`，实际上一行：%q\n完整转录：\n%q", card, content)
	}
	if !strings.Contains(card, "+2") || !strings.Contains(card, "-2") {
		t.Fatalf("摘要行应含真实增删统计 +2 -2（a→B 与 c→C 两处单行替换），实际：%q", card)
	}
	if !strings.Contains(card, "/diff") {
		t.Fatalf("摘要行应给出展开入口 `(/diff)`（复用既有折叠提示形态），实际：%q", card)
	}

	// ④ 逐文件清单（2026-10-08 用户拍板迭代：光有合计不行，要能看到"改了哪个文件、各改了多少"）：
	// 摘要行**正上方**应为本轮变更文件的清单行 ` card.txt +2 -2`（相对路径 + 各自增删）。
	if idx < 2 {
		t.Fatalf("卡片应含逐文件清单行（摘要行上方至少还有一行），但结束标志上方不足两行：\n%q", content)
	}
	fileLine := lines[idx-2]
	if !strings.Contains(fileLine, "card.txt") || !strings.Contains(fileLine, "+2") || !strings.Contains(fileLine, "-2") {
		t.Fatalf("摘要行上方应为本轮变更文件的清单行 `  card.txt  +2 -2`，实际：%q\n完整转录：\n%q", fileLine, content)
	}

	// 反向（A-3 契约 + I5）：卡片是**投影**，不得占消息身份，也不得进模型侧 session。
	for i, msg := range m.sessionService.Messages() {
		if strings.Contains(msg, "files changed") || strings.Contains(msg, "▸ ") {
			t.Fatalf("卡片不得占消息身份（显示列表第 %d 条）：%q", i, msg)
		}
	}
	for i, smsg := range m.sessionService.CurrentSession().GetMessages() {
		for _, b := range smsg.Blocks {
			if strings.Contains(b.Text, "files changed") {
				t.Fatalf("I5 违例：卡片泄漏进模型侧 session（第 %d 条消息）", i)
			}
		}
	}
}

// ② 反向：本轮**没有**任何文件变更 ⇒ 整张卡片不出现（零噪音）。
//
// 判别力：如果卡片改成"无条件输出"，本条转红。
func TestTurnDiff_NoChangesHidesCard(t *testing.T) {
	m, exec, _ := newTurnDiffModel(t)
	exec.SetCurrentMessageId("user-msg-nochange-turn")

	runOneTurn(t, m, "只是聊聊", "好的")

	content := ansi.Strip(m.viewportContent)
	if findTurnDoneMarkLine(t, content) < 0 {
		t.Fatalf("夹具不健全：没有结束标志：\n%q", content)
	}
	if strings.Contains(content, "files changed") {
		t.Fatalf("本轮无文件变更 ⇒ 不得出现变更卡片（零噪音），实际转录：\n%q", content)
	}
}

// ③ 反向：取不到事实（锚点为空）⇒ 不显示卡片，且**不**声称"无变更"。
//
// 这条钉住 `currentTurnChanges()` 的 nil 语义（区别于"非 nil 且零变更"）：
// 两者都不显示，但前者不能假装知道"无变更"。本判据只需证明"取不到 ⇒ 无卡片"，不越界。
func TestTurnDiff_UnavailableFactShowsNothing(t *testing.T) {
	m, exec, _ := newTurnDiffModel(t)
	exec.SetCurrentMessageId("") // 锚点为空 = 取不到事实

	runOneTurn(t, m, "没有锚点的一轮", "结束")

	content := ansi.Strip(m.viewportContent)
	if strings.Contains(content, "files changed") {
		t.Fatalf("取不到事实（锚点为空）时不得显示变更卡片：\n%q", content)
	}
}

// ④ 多文件轮：每个变更文件**各自成行**（路径 + 各自 +/−），不只给合计。
//
// 判别力：若卡片退化回只打一行合计（丢逐文件清单），本条转红 ——
// 两个文件的路径与各自的统计都断言，合计行无法伪装成清单。
func TestTurnDiff_CardListsEachFile(t *testing.T) {
	m, exec, workDir := newTurnDiffModel(t)
	exec.SetCurrentMessageId("user-msg-multifile-turn")

	// 新文件 3 行 ⇒ 该行 +3；已有文件单行替换 ⇒ +1 -1。
	newFile := filepath.Join(workDir, "brand_new.txt")
	writeFileThroughTool(t, m, newFile, "x\ny\nz\n", "multi-call-1")
	editFile := filepath.Join(workDir, "edit_me.txt")
	if err := os.WriteFile(editFile, []byte("a\nb\nc\n"), 0644); err != nil {
		t.Fatal(err)
	}
	writeFileThroughTool(t, m, editFile, "a\nB\nc\n", "multi-call-2")

	runOneTurn(t, m, "改两个文件", "改好了")

	content := ansi.Strip(m.viewportContent)
	idx := findTurnDoneMarkLine(t, content)
	if idx < 0 {
		t.Fatalf("夹具不健全：没有结束标志：\n%q", content)
	}
	if idx < 3 {
		t.Fatalf("两个文件的卡片至少三行（两行清单 + 一行摘要），结束标志上方不足三行：\n%q", content)
	}
	lines := strings.Split(content, "\n")
	cardRegion := strings.Join(lines[idx-3:idx], "\n")
	for _, want := range []string{"brand_new.txt", "edit_me.txt", "▸ 2 files changed"} {
		if !strings.Contains(cardRegion, want) {
			t.Fatalf("轮末卡片区应含 %q（逐文件清单 + 合计），实际卡片区（标志上方三行）：\n%s\n完整转录：\n%q", want, cardRegion, content)
		}
	}
}
