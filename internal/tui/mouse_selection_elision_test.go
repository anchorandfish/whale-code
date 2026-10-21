package tui

// mouse_selection_elision_test.go — 钉住「显示层每一种省略，复制都能拿到原文」，
// 以及「硬折行不再把一条逻辑行复制成两段」。
//
// 起因（用户真机报障 ·）：拖选跨过 `🧠 Thinking · …` / `⏺ bash …` 的行时，
// 被省略掉的那半截原文拿不到 —— 旧实现只认得「工具结果折叠」这一种省略
// （靠屏幕可见字面反查），思考预览与命令截断**静默丢内容**。
//
// 批 C 换前提：渲染层为每一显示行登记真值（谁省略谁登记），复制按**逻辑行**取数。
// 本文件的判据一律从**生产入口**进入：
//
//	formatAssistantMessageWithThinkingSource → AddMessageWithSources
//	→ refreshTranscriptViewport → Update(tea.MouseMsg)
//
// 旧版（before 必红）曾以 `//go:build probe` 挂在默认套件之外；修完按计划翻绿并摘掉 tag。

import (
	"fmt"
	"os"
	"strings"
	"testing"

	"github.com/anchorandfish/whale-code/internal/session"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/x/ansi"
)

func countLineSources(sources []lineSource) int {
	n := 0
	for _, s := range sources {
		if s.hasOverride() {
			n++
		}
	}
	return n
}

func thinkingOf(blocks []session.ContentBlock) string {
	for _, b := range blocks {
		if b.Type == session.ThinkingBlockType {
			return b.Thinking
		}
	}
	return ""
}

// TestMouseSelectionCopiesElidedThinkingAndCommand —— 的 two halves：
// 思考折叠预览（60 列）与命令截断（160 rune）都不能把被省略的原文丢掉。
func TestMouseSelectionCopiesElidedThinkingAndCommand(t *testing.T) {
	cases := []struct {
		label  string
		blocks []session.ContentBlock
		// 屏幕上不可能出现（只在完整原文里出现）的唯一标记
		marker string
	}{
		{
			label: "🧠 Thinking 折叠行（预览 60 列，其余丢失）",
			blocks: []session.ContentBlock{{
				Type:     session.ThinkingBlockType,
				Thinking: "THINK_HEAD " + strings.Repeat("think ", 30) + "THINK_TAIL_MARKER",
			}},
			marker: "THINK_TAIL_MARKER",
		},
		{
			label: "⏺ bash 调用行（命令按 160 rune 截断，其余丢失）",
			blocks: []session.ContentBlock{{
				Type:  "tool_use",
				Name:  "bash",
				Input: fmt.Sprintf(`{"command":%q}`, "echo START "+strings.Repeat("x", 200)+" CMD_TAIL_MARKER"),
			}},
			marker: "CMD_TAIL_MARKER",
		},
	}

	for _, tc := range cases {
		t.Run(tc.label, func(t *testing.T) {
			m := newTUIRegressionModel(t)
			res, _ := m.Update(tea.WindowSizeMsg{Width: 120, Height: 35})
			mm := res.(Model)
			mm.reassignServices()

			mm.sessionService.CurrentSession().SetMessages([]session.Message{
				session.UserText("hi"),
				{Role: session.RoleAssistant, Blocks: tc.blocks},
			})
			// 生产入口：渲染产地连同 source map 一起产出，再交给显示缓冲。
			display, srcs := formatAssistantMessageWithThinkingSource(tc.blocks, thinkingOf(tc.blocks), 0, 76)
			mm.sessionService.SetMessagesWithSources(
				[]string{formatUserMessage("hi", 0), display},
				[][]lineSource{nil, srcs},
			)
			mm.refreshTranscriptViewport()
			mm.uiService.Viewport().GotoTop()
			mm.recalcViewportHeight()

			got, after := dragSelectToBottom(t, &mm, 0, 0)

			// ① 行为面：被省略的原文必须拿到。
			if !strings.Contains(got, tc.marker) {
				t.Errorf("拖选拿不到省略掉的那段（%s 缺失）—— 复制到的是屏幕摘要：\n%s", tc.marker, got)
			}
			// ② 机制面（R4-7：判别器要指向机制，不是指向结果）：
			// source map 真的登记了省略行 —— 否则"结果碰巧对"会掩盖链路已断。
			if n := countLineSources(after.mouseSelection.sources); n == 0 {
				t.Fatalf("source map 里没有任何省略行登记 ⇒ 渲染产地与复制层之间的登记链路已断")
			}
		})
	}
}

// TestMouseSelectionHardWrapReturnsWholeLogicalLine —— 。
//
// 一条超宽逻辑行会被 ansi.Hardwrap 插入真实 \n 折成 N 条视觉行（全仓唯一折行点，
// 已拍板"勿改 WrapText"）。旧实现的复制读的是折后产物 ⇒ 复制出来中间**硬断**。
// 换前提后复制按**逻辑行**取数（同 owner 的视觉行整组只取一次）⇒ 拿到整条。
func TestMouseSelectionHardWrapReturnsWholeLogicalLine(t *testing.T) {
	m := newTUIRegressionModel(t)
	res, _ := m.Update(tea.WindowSizeMsg{Width: 60, Height: 35})
	mm := res.(Model)
	mm.reassignServices()

	long := "AAAA" + strings.Repeat("B", 200) + "WRAP_TAIL_MARKER"
	mm.sessionService.SetMessages([]string{formatUserMessage(long, 0)})
	mm.refreshTranscriptViewport()
	mm.uiService.Viewport().GotoTop()
	mm.recalcViewportHeight()

	// 夹具自检：该逻辑行确实被硬折成了多条视觉行，否则本判据不可判别。
	if n := strings.Count(ansi.Strip(mm.viewportContent), "\n"); n < 2 {
		t.Fatalf("夹具不健全：超宽行未被硬折行（视口内容仅 %d 行），本判据不可判别", n+1)
	}

	got, _ := dragSelectToBottom(t, &mm, 0, 0)

	// 整条逻辑行必须**连续**出现（中间没有被折行断开的 \n）。
	if !strings.Contains(got, long) {
		t.Errorf("超宽逻辑行被复制成了断片（中间硬断）—— 期望整条连续：\n%q", got)
	}
}

// TestCopyPathHasNoLiteralMarkerMatching —— 结构面判据（读盘型）。
//
// 批 C 的核心是"换前提"：复制的真值改由渲染层登记的 source map 定义，
// 复制层**不得**再按屏幕可见字面反查省略点（那是旧实现静默失配的根源：
// 窄视口 wrap / compact 序号漂移都会让字面对不上）。
//
// ★ 这是读盘型判据：按 R4-8 禁用 `-overlay`（overlay 只改编译期输入，
// 对"运行期读磁盘"的判别器物理无效）。此处直接读包内源文件。
func TestCopyPathHasNoLiteralMarkerMatching(t *testing.T) {
	// ① 旧的字面反查实现必须已退役。
	if _, err := os.Stat("mouse_selection_copy_source.go"); err == nil {
		t.Fatal("mouse_selection_copy_source.go 仍在盘：复制路径可能退回「按屏幕字面反查省略点」")
	}

	// ② 复制路径不得残留按字面定位省略点的字段/调用。
	// 判据只看**代码**（注释里说明"旧实现叫什么"不算违规）⇒ 先剥行注释。
	src, err := os.ReadFile("mouse_selection.go")
	if err != nil {
		t.Fatalf("读取 mouse_selection.go 失败：%v", err)
	}
	code := stripGoLineComments(string(src))
	for _, banned := range []string{"copyOverrides", "ToolFoldMarkerFor"} {
		if strings.Contains(code, banned) {
			t.Errorf("复制路径仍引用 %q —— 说明它是按屏幕字面定位省略点，而不是消费渲染层登记的 source map", banned)
		}
	}
	// ③ 而且它确实在消费 source map（正面断言，防"两边都不查"的假绿）。
	for _, required := range []string{"lineSource", "sel.sources", "sel.owner", "sel.logical"} {
		if !strings.Contains(code, required) {
			t.Errorf("复制路径未消费 source map（缺 %q）", required)
		}
	}

	// ④ 每个省略产地都必须有对应的 source 产出点（谁省略谁登记）。
	producers := map[string][]string{
		"tool_render.go": {"renderToolUseCompactSource", "formatToolResultMessageSource"},
		"format.go":      {"formatAssistantMessageWithThinkingSource", "collapsedThinkingSource"},
	}
	for file, funcs := range producers {
		body, err := os.ReadFile(file)
		if err != nil {
			t.Fatalf("读取 %s 失败：%v", file, err)
		}
		for _, fn := range funcs {
			if !strings.Contains(string(body), "func "+fn) {
				t.Errorf("%s 缺少省略产地 %s（该种省略未登记 ⇒ 复制拿不到原文）", file, fn)
			}
		}
	}
}

// stripGoLineComments 去掉 Go 源码里的行注释（判据只看代码，不看散文）。
//
// 适度简化：不处理字符串字面量里的 `//`（本文件扫描的目标源码里没有这种形态）。
func stripGoLineComments(src string) string {
	lines := strings.Split(src, "\n")
	var b strings.Builder
	for _, l := range lines {
		if i := strings.Index(l, "//"); i >= 0 {
			l = l[:i]
		}
		b.WriteString(l)
		b.WriteString("\n")
	}
	return b.String()
}
