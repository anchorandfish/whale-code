// composer_input_guard_test.go — 输入框「只接受文本」闸门的判据。
//
// 判据设计（production-path-tdd）：
// - 走**生产入口**：`Model.Update`（不是直接调闸门），并且跑一遍真 bubbletea 输入解析器
// （`readAnsiInputs`/`detectOneMsg`）来产出入参 —— 碎片形状不是我们编的，是解析器真给的。
// - **可判别**：修前跑本文件的 `TestComposerGuardDropsSplitEscapeSequence` 必红
// （实测修前输入框 = `[<0;57;40M`，与用户会话存档 `01a11672-….jsonl` 逐字节一致）。
// - 正例与反例成对：反例证明碎片被拦下，正例证明**用户真实文字一个字符都没被吞**。
package tui

import (
	"io"
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
)

// guardChunkReader 每次 Read 只吐一个预置块 —— 模拟「一次 tty read」的边界。
// 碎片之所以产生，就是因为 tty 把一条鼠标上报拆成两次 read。
type guardChunkReader struct {
	chunks [][]byte
	i      int
}

func (r *guardChunkReader) Read(p []byte) (int, error) {
	if r.i >= len(r.chunks) {
		time.Sleep(300 * time.Millisecond)
		return 0, io.EOF
	}
	n := copy(p, r.chunks[r.i])
	r.i++
	return n, nil
}

type guardRecorder struct {
	msgs  []tea.Msg
	seen  int
	limit int
}

func (r *guardRecorder) Init() tea.Cmd { return nil }

func (r *guardRecorder) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	if _, ok := msg.(tea.WindowSizeMsg); !ok {
		r.msgs = append(r.msgs, msg)
		r.seen++
		if r.limit > 0 && r.seen >= r.limit {
			return r, tea.Quit
		}
	}
	return r, nil
}

func (r *guardRecorder) View() string { return "" }

// guardParseAnsiInput 用**真 bubbletea Program**解析给定字节块，返回收到的事件。
// 这是「上游到底交给我们什么」的权威来源。
func guardParseAnsiInput(t *testing.T, chunks [][]byte, limit int) []tea.Msg {
	t.Helper()
	rec := &guardRecorder{limit: limit}
	p := tea.NewProgram(rec,
		tea.WithInput(&guardChunkReader{chunks: chunks}),
		tea.WithOutput(io.Discard),
		tea.WithoutRenderer(),
		tea.WithoutSignalHandler(),
	)
	done := make(chan struct{})
	go func() { defer close(done); _, _ = p.Run() }()
	select {
	case <-done:
	case <-time.After(6 * time.Second):
		p.Kill()
		<-done
	}
	return rec.msgs
}

// guardFeedIntoComposer 把解析出来的按键事件喂进**生产入口** Model.Update，
// 并按生产路径落盘（合并器缓冲由 8ms tick 触发，这里直接调同一个 flush 函数）。
// delay>0 时在每个事件之间等待，用于验证「重同步窗口的时间上界」。
func guardFeedIntoComposer(t *testing.T, msgs []tea.Msg) string {
	return guardFeedIntoComposerDelay(t, msgs, 0)
}

func guardFeedIntoComposerDelay(t *testing.T, msgs []tea.Msg, delay time.Duration) string {
	t.Helper()
	m := newTUIRegressionModel(t)
	m.uiService.Textarea().Focus()
	cur := *m
	for i, msg := range msgs {
		if i > 0 && delay > 0 {
			time.Sleep(delay)
		}
		if _, ok := msg.(tea.KeyMsg); !ok {
			continue
		}
		res, _ := cur.Update(msg)
		next, ok := res.(Model)
		if !ok {
			t.Fatalf("模型类型丢失: %T", res)
		}
		next.reassignServices()
		cur = next
	}
	cur.reassignServices()
	cur.flushCoalescedInputSync()
	cur.reassignServices()
	return cur.uiService.Textarea().Value()
}

// TestComposerGuardUpstreamSplitProducesDebris 钉住上游行为（不是判据，是取证）：
// 同一条鼠标上报，完整到达 ⇒ MouseMsg；被拆开 ⇒ Alt+`[` + 载荷 rune。
//
// 这条测试的价值：将来有人怀疑「解析器是不是已经修好了」，它会立刻打红。
func TestComposerGuardUpstreamSplitProducesDebris(t *testing.T) {
	full := guardParseAnsiInput(t, [][]byte{[]byte("\x1b[<0;57;40M")}, 1)
	if len(full) != 1 {
		t.Fatalf("完整单读应当只产出 1 个事件，got %d: %v", len(full), full)
	}
	if _, ok := full[0].(tea.MouseMsg); !ok {
		t.Fatalf("完整单读应解析为 MouseMsg，got %T（%v）", full[0], full[0])
	}

	split := guardParseAnsiInput(t, [][]byte{[]byte("\x1b[<0;57;"), []byte("40M")}, 3)
	if len(split) != 3 {
		t.Fatalf("拆读应产出 3 个事件，got %d: %v", len(split), split)
	}
	if got := split[0].(tea.KeyMsg).String(); got != "alt+[" {
		t.Fatalf("拆读首事件应为 alt+[（ESC 被当 Alt 吃掉），got %q", got)
	}
}

// TestComposerGuardDropsSplitEscapeSequence 是 的**主判据**。
// 修前：输入框 = `[<0;57;40M`（用户报障原文）⇒ 本用例红。
func TestComposerGuardDropsSplitEscapeSequence(t *testing.T) {
	split := guardParseAnsiInput(t, [][]byte{[]byte("\x1b[<0;57;"), []byte("40M")}, 3)
	if got := guardFeedIntoComposer(t, split); got != "" {
		t.Fatalf("被拆散的鼠标上报碎片不得进入输入框，got %q", got)
	}
}

// TestComposerGuardDropsDraggedMotionFlood 拖动鼠标时的连续 motion 上报（用户贴出的另一段形状：
// `<32;34;15M<32;25;12M…`）同样不得留在输入框里。
func TestComposerGuardDropsDraggedMotionFlood(t *testing.T) {
	chunks := [][]byte{
		[]byte("\x1b[<32;34;15M\x1b[<32;25;"),
		[]byte("12M\x1b[<32;5;"),
		[]byte("4M\x1b[<36;18;"),
		[]byte("14M"),
	}
	msgs := guardParseAnsiInput(t, chunks, 10)
	if got := guardFeedIntoComposer(t, msgs); got != "" {
		t.Fatalf("拖动期间的鼠标上报碎片不得进入输入框，got %q", got)
	}
}

// TestComposerGuardKeepsUserTextAfterEscapeHead 正例（防误伤）：
// 真按了 Alt+[ 之后打的是**普通文字**，文字必须原样进输入框 ——
// 宁可放行可疑文本，也绝不吞用户数据。
func TestComposerGuardKeepsUserTextAfterEscapeHead(t *testing.T) {
	msgs := []tea.Msg{
		tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("["), Alt: true},
		tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("hello")},
	}
	if got := guardFeedIntoComposer(t, msgs); got != "hello" {
		t.Fatalf("序列头之后的普通文字必须放行，want %q, got %q", "hello", got)
	}
}

// TestComposerGuardKeepsCJKAfterEscapeHead 中文（多字节 rune）绝不能被误判成载荷。
func TestComposerGuardKeepsCJKAfterEscapeHead(t *testing.T) {
	msgs := []tea.Msg{
		tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("["), Alt: true},
		tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("你好世界")},
	}
	if got := guardFeedIntoComposer(t, msgs); got != "你好世界" {
		t.Fatalf("中文必须原样放行，got %q", got)
	}
}

// TestComposerGuardStripsControlRunesInTyping 直接带控制字符的 KeyRunes 不得进输入框。
func TestComposerGuardStripsControlRunesInTyping(t *testing.T) {
	msgs := []tea.Msg{
		tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("a\x1bb\x01c")},
	}
	if got := guardFeedIntoComposer(t, msgs); got != "abc" {
		t.Fatalf("控制字符应被剥掉、可见文字保留，got %q", got)
	}
}

// TestComposerGuardNormalTypingUnaffected 回归守卫：
// 正常打字（含中文）一个字符都不能被吞，合并器（快速连打 → 缓冲批量刷新）行为不变。
func TestComposerGuardNormalTypingUnaffected(t *testing.T) {
	msgs := []tea.Msg{
		tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("读取 t10.txt")},
		tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("写回同一个文件")},
	}
	want := "读取 t10.txt写回同一个文件"
	if got := guardFeedIntoComposer(t, msgs); got != want {
		t.Fatalf("正常输入被改动，want %q, got %q", want, got)
	}
}

// TestComposerGuardDoesNotSwallowLaterTypingAfterHead 时间上界：
// 吞掉序列头之后**过了很久**才来的文字，那是用户在打字 ⇒ 必须放行，闸门不得留下长期吞字状态。
// （真实载荷与头同批到达，微秒级；人类按键 ≥100ms/次）
func TestComposerGuardDoesNotSwallowLaterTypingAfterHead(t *testing.T) {
	msgs := []tea.Msg{
		tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("["), Alt: true},
		tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("40M")},
	}
	// 300ms > composerResyncTimeout(250ms)
	got := guardFeedIntoComposerDelay(t, msgs, 300*time.Millisecond)
	if got != "40M" {
		t.Fatalf("超时后的文字必须放行，got %q", got)
	}
}

// TestComposerGuardPasteStripsEscape 粘贴入口（insertComposerText）同一不变量：只收文本。
func TestComposerGuardPasteStripsEscape(t *testing.T) {
	m := newTUIRegressionModel(t)
	m.uiService.Textarea().Focus()
	m.reassignServices()
	m.insertComposerText("[38;5;36m--- a/handler.go\x1b[0m\n+after")
	m.reassignServices()
	got := m.uiService.Textarea().Value()
	if strings.ContainsAny(got, "\x1b\x01\x02") {
		t.Fatalf("粘贴内容里的控制字符必须剥掉，got %q", got)
	}
	if !strings.Contains(got, "--- a/handler.go") || !strings.HasSuffix(got, "\n+after") {
		t.Fatalf("粘贴的可见文字与换行必须保留（只有控制字符被剥掉），got %q", got)
	}
}
