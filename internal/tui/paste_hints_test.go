package tui

import (
	"encoding/base64"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/anchorandfish/whale-code/internal/testutil"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/x/ansi"
)

// ②③④（2026-10-16 用户「继续」）：贴图可用性的收尾三件。
//
//	R1 生产接线断言：
// ③ 报错教学化 —— Update(clipboardPasteMsg{err}) → applyClipboardPaste（clipboard.go）
// 的错误分支 → addMessage。守卫 canAcceptComposerText 在空闲态+输入框聚焦时成立。
// ④ 贴入文字是图片路径 ⇒ 自动附图 —— readClipboardPasteCmd（cmd goroutine，阻塞面合法）
// 的 plainText 分支先试 tryPasteImagePath → 复用 imgproc.FromPath（10MB 守卫内建，
// 与 @文件 mention 同一口径）。测试注入输入源 = getClipboardRichText var
// （R6b：注入的是「剪贴板读到了什么」，不换任何判定分支）。
// ② 剪贴板有图提示 —— tea.FocusMsg（main.go 需开 WithReportFocus）→ Update 返回
// clipboardImageProbeCmd → probe（可注入 var，R6b）→ clipboardImageProbeMsg → Update
// 置位 clipboardHasImage → renderInputArea 条件提示行。
// （对齐 同类 CLI：检测到「终端聚焦 + 剪贴板有图」才提示按键；同类 CLI 未做。）

// w104TinyPNG 是一张真实 1×10 PNG（魔数可被 imgproc 识别），base64 形式内嵌。
const w104TinyPNGB64 = "iVBORw0KGgoAAAANSUhEUgAAAAoAAAAKCAYAAACNMs+9AAAAFUlEQVR42mP8z8AARIQBExMDDQMDAwCVAgVDPMS3AAAAAElFTkSuQmCC"

// w104RealPNGFile 在临时目录写一张**魔数真实**的 PNG，返回路径。
func w104RealPNGFile(t *testing.T, name string) string {
	t.Helper()
	data, err := base64.StdEncoding.DecodeString(w104TinyPNGB64)
	if err != nil {
		t.Fatalf("前置：解码内置 PNG 失败: %v", err)
	}
	path := filepath.Join(t.TempDir(), name)
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatalf("前置：写 PNG 失败: %v", err)
	}
	return path
}

// ---------------------------------------------------------------------------
// ③ 空剪贴板报错必须「教学」：说出正确的按键（Ctrl+V）并解释 ⌘V 为什么无效。
// ---------------------------------------------------------------------------

func TestW104_EmptyPasteErrorTeachesImageShortcut(t *testing.T) {
	m := newComposerSelectionModel(t)
	m = w104Apply(t, m, clipboardPasteMsg{seq: 1, err: fmt.Errorf("no image in clipboard")})

	all := strings.Join(m.messages.All(), "\n")
	// 必须点名正确按键（文案里的键名是第二份实现，行为侧已有
	// TestW104_AltCtrlVMatchesImagePasteKey 锁住 Ctrl+V/Ctrl+Alt+V 真的触发贴图）。
	if !strings.Contains(all, "Ctrl+V") {
		t.Fatalf("③ 空剪贴板报错必须教学正确按键（Ctrl+V），got %q", all)
	}
	// 必须解释 ⌘V 为什么无效——这正是用户「贴不了图」误报的根因。
	if !strings.Contains(all, "⌘V") {
		t.Fatalf("③ 报错必须解释 ⌘V 会被终端截走（用户误报根因），got %q", all)
	}
	// 双语（EN 主行 + 中文辅行）：中文辅行必须包含根因解释。
	if !strings.Contains(all, "截走") {
		t.Fatalf("③ 应含中文辅行解释（…终端截走…），got %q", all)
	}
}

// 反向半边：非「空剪贴板」族错误保持原格式（不得被教学文案吞掉）。
func TestW104_OtherPasteErrorsKeepPlainFormat(t *testing.T) {
	m := newComposerSelectionModel(t)
	m = w104Apply(t, m, clipboardPasteMsg{seq: 1, err: fmt.Errorf("clipboard read failed (text: x; image: y)")})

	all := strings.Join(m.messages.All(), "\n")
	if !strings.Contains(all, "Clipboard paste failed: clipboard read failed") {
		t.Fatalf("③ 非空剪贴板族错误应保持原格式，got %q", all)
	}
	if strings.Contains(all, "⌘V") {
		t.Fatalf("③ 教学文案不得泄漏到其它错误族，got %q", all)
	}
}

// ---------------------------------------------------------------------------
// ④ 贴入文字是图片路径 ⇒ 自动附图（对齐 同类 CLI handle_paste_image_path）。
// ---------------------------------------------------------------------------

// 生产入口行为判据：readClipboardPasteCmd 在 plainText 是真实图片路径时返回**图片消息**。
func TestW104_PastedImagePathAutoAttaches(t *testing.T) {
	pngPath := w104RealPNGFile(t, "shot.png")

	orig := getClipboardRichText
	getClipboardRichText = func() (string, string, error) {
		return "", pngPath, nil // Finder 复制图片文件后 ⌘V：终端贴进来的就是路径文本
	}
	t.Cleanup(func() { getClipboardRichText = orig })

	msg := readClipboardPasteCmd(true, 7)()
	got, ok := msg.(clipboardPasteMsg)
	if !ok {
		t.Fatalf("④ 应返回 clipboardPasteMsg，got %T", msg)
	}
	if got.image == nil {
		t.Fatalf("④ 贴入真实图片路径应自动附图，got text=%q err=%v", got.text, got.err)
	}
	if got.image.MimeType != "image/png" {
		t.Fatalf("④ MIME 应来自魔数识别，got %q", got.image.MimeType)
	}
	// ⑧ 联动：附的图必须带 Path（指向**原文件**，点击占位符即可打开原图）。
	if got.image.Path != pngPath {
		t.Fatalf("④ Path 应指向原文件（⑧ 点击打开原图），got %q want %q", got.image.Path, pngPath)
	}
}

// 反向半边（一刀切防护）：不是图片路径的文本必须**原样贴入**，不得被吞。
func TestW104_PastedNonImagePathStaysText(t *testing.T) {
	plain := "this is just a sentence about /tmp/x.png"
	orig := getClipboardRichText
	getClipboardRichText = func() (string, string, error) { return "", plain, nil }
	t.Cleanup(func() { getClipboardRichText = orig })

	msg := readClipboardPasteCmd(true, 7)()
	got, ok := msg.(clipboardPasteMsg)
	if !ok {
		t.Fatalf("④ 应返回 clipboardPasteMsg，got %T", msg)
	}
	if got.image != nil {
		t.Fatalf("④ 普通句子（含 png 字样但不是路径）不得被附图，got %+v", got.image)
	}
	if got.text != plain {
		t.Fatalf("④ 文本应原样保留，got %q want %q", got.text, plain)
	}
}

// 分类函数表驱动：路径形态的接受/拒绝边界（含引号包裹、~ 展开、多行、假扩展名）。
func TestW104_PastedImagePathClassification(t *testing.T) {
	realPNG := w104RealPNGFile(t, "real.png")
	junkPNG := filepath.Join(t.TempDir(), "junk.png")
	if err := os.WriteFile(junkPNG, []byte("definitely not an image"), 0o600); err != nil {
		t.Fatalf("前置: %v", err)
	}

	cases := []struct {
		name string
		in   string
		want bool
	}{
		{"裸路径", realPNG, true},
		{"双引号包裹", `"` + realPNG + `"`, true},
		{"尾随空格/换行", realPNG + "\n", true}, // Finder 复制常带尾随换行
		{"多行文本", realPNG + "\nsecond line", false},
		{"文件不存在", filepath.Join(t.TempDir(), "nope.png"), false},
		{"png 扩展名但非图片", junkPNG, false},
		{"非图片扩展名", "README.md", false},
		{"超长文本", strings.Repeat("a", 2048) + ".png", false},
	}
	for _, c := range cases {
		if _, _, got := tryPasteImagePath(c.in); got != c.want {
			t.Fatalf("④ tryPasteImagePath(%q)=%v, want %v（case %s）", c.in, got, c.want, c.name)
		}
	}
}

// 波浪号路径：~/ 展开后命中真实文件 ⇒ 附图（Path 为展开后的绝对路径）。
func TestW104_PastedHomeRelativeImagePath(t *testing.T) {
	// NoTempDirHomeIsolation 门修法：HOME 隔离走全仓唯一承载体 testutil.IsolatedHomeDir
	// （宽松清理，规避后台协程写 $HOME 与 TempDir RemoveAll 的 TOCTOU 竞态）。
	t.Setenv("HOME", testutil.IsolatedHomeDir(t))
	home := os.Getenv("HOME")
	rel := filepath.Join(home, "shot-home.png")
	data, _ := base64.StdEncoding.DecodeString(w104TinyPNGB64)
	if err := os.WriteFile(rel, data, 0o600); err != nil {
		t.Fatalf("前置: %v", err)
	}

	ref, got, ok := tryPasteImagePath("~/shot-home.png")
	if !ok {
		t.Fatalf("④ ~/ 路径应展开后命中并附图")
	}
	if got != rel {
		t.Fatalf("④ Path 应为展开后的绝对路径，got %q want %q", got, rel)
	}
	if ref.MediaType != "image/png" {
		t.Fatalf("④ MIME 应为 image/png，got %q", ref.MediaType)
	}
}

// 接线护栏（读盘型结构门，与 TestW104_AllComposerOutletsUseSingleDecoratedForm 同法）：
// readClipboardPasteCmd 必须真的调用 tryPasteImagePath——否则分类函数是死代码。
func TestW104_PasteCmdWiresImagePathDetection(t *testing.T) {
	src, err := os.ReadFile("clipboard.go")
	if err != nil {
		t.Fatalf("前置: %v", err)
	}
	if n := strings.Count(string(src), "tryPasteImagePath(plainText)"); n != 1 {
		t.Fatalf("④ readClipboardPasteCmd 应恰好有 1 处 tryPasteImagePath(plainText) 调用，got %d", n)
	}
}

// ---------------------------------------------------------------------------
// ② 剪贴板有图 + 终端聚焦 ⇒ 输入框旁提示按键（同类 CLI 模式）。
// ---------------------------------------------------------------------------

// 接线护栏：main.go 的 Program 必须开 WithReportFocus —— 不开则终端不上报焦点事件，
// FocusMsg 永远不来 ⇒ ② 的整条链是死代码（R5）。
func TestW104_ProgramReportsFocusEvents(t *testing.T) {
	src, err := os.ReadFile(filepath.Join("..", "..", "main.go"))
	if err != nil {
		t.Skipf("读不到 main.go（包布局变更？）: %v", err)
	}
	if !strings.Contains(string(src), "tea.WithReportFocus()") {
		t.Fatal("② main.go 的 newTUIProgramOptions 必须开 tea.WithReportFocus()，否则 FocusMsg 永远不触发")
	}
}

func TestW104_FocusProbeShowsClipboardImageHint(t *testing.T) {
	orig := clipboardImageProbe
	clipboardImageProbe = func() bool { return true }
	t.Cleanup(func() { clipboardImageProbe = orig })

	m := newComposerSelectionModel(t)

	// 生产入口：终端聚焦事件 → 应派发探针命令。
	updated, cmd := m.Update(tea.FocusMsg{})
	if cmd == nil {
		t.Fatal("② FocusMsg 应派发剪贴板探针命令")
	}
	m = modelFromTea(updated)

	// 执行探针命令（probe 已被注入为 true）→ 喂回结果 → 提示出现。
	probeMsg, ok := cmd().(clipboardImageProbeMsg)
	if !ok {
		t.Fatalf("② 探针命令应返回 clipboardImageProbeMsg，got %T", cmd())
	}
	m = w104Apply(t, m, probeMsg)

	// 提示行挂在 renderInputArea（输入框整体渲染），不是 textarea 的装饰视图。
	view := m.renderInputArea()
	if !strings.Contains(view, "Ctrl+V") || !strings.Contains(ansi.Strip(view), "剪贴板里有图片") {
		t.Fatalf("② 剪贴板有图时输入框旁应出现按键提示，got %q", view)
	}
}

// 反向半边：剪贴板没图 ⇒ 不得出现提示（恒提示 = 噪声）。
func TestW104_NoHintWhenClipboardHasNoImage(t *testing.T) {
	orig := clipboardImageProbe
	clipboardImageProbe = func() bool { return false }
	t.Cleanup(func() { clipboardImageProbe = orig })

	m := newComposerSelectionModel(t)
	updated, cmd := m.Update(tea.FocusMsg{})
	if cmd == nil {
		t.Fatal("② FocusMsg 应派发探针命令（无论结果如何）")
	}
	m = modelFromTea(updated)
	m = w104Apply(t, m, cmd().(clipboardImageProbeMsg))

	if strings.Contains(m.renderInputArea(), "剪贴板里有图片") {
		t.Fatalf("② 剪贴板没图不得提示，got %q", m.renderInputArea())
	}
}

// 消费即撤：贴图成功后提示必须消失（否则用户贴完了还看到"按 Ctrl+V"）。
func TestW104_ImagePasteConsumesHint(t *testing.T) {
	m := newComposerSelectionModel(t)
	m.clipboardHasImage = true // 预置：提示在显示中

	m = w104Apply(t, m, clipboardPasteMsg{seq: 1, image: &PendingImage{MimeType: "image/png"}})

	if m.clipboardHasImage {
		t.Fatal("② 贴图成功后 clipboardHasImage 应复位")
	}
	if strings.Contains(m.renderInputArea(), "剪贴板里有图片") {
		t.Fatalf("② 贴图成功后提示应消失，got %q", m.renderInputArea())
	}
}

// 失败也复核：空剪贴板报错后探针状态应清零（剪贴板刚才明确没有图）。
func TestW104_PasteErrorClearsStaleHint(t *testing.T) {
	m := newComposerSelectionModel(t)
	m.clipboardHasImage = true

	m = w104Apply(t, m, clipboardPasteMsg{seq: 1, err: fmt.Errorf("no image in clipboard")})

	if m.clipboardHasImage {
		t.Fatal("② 明确读到无图后，过期提示状态应清零")
	}
}
