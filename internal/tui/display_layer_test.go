package tui

// display_layer_test.go — 「TUI 显示层四件小观察」判据。
//
// 四件：
// ② thinking 单行预览把原文 markdown 围栏 ``` 当噪声显示；
// ③ 工具调用头把「工具名」与「多行参数首行」直接拼接（`apply_unified_diff --- a/x` 像 shell 命令）；
// ④ CJK 行首禁则缺失（全角） 被折到行首）；
// （第 4 件「错误双重工具名前缀」是 internal/tools 面，判据在 tools 包。）
//
// before 自检（改动前必须红）：
// ② 预览含 ``` ⇒ 红；③ 头跨行 / 无分隔 ⇒ 红；④ 首行以） 开头 ⇒ 红。
// 都从**生产入口**进入：②③ 走 handleStreamDone（真实回合收尾 → viewportContent）；
// ④ 走 TextMeasurer.WrapText（wrapViewportContent 的唯一折行实现）。

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"

	"github.com/anchorandfish/whale-code/internal/session"
)

// w029ThinkingPreviewLine 返回转录里承载思考预览的那一行（无则 ""）。
func w029ThinkingPreviewLine(content string) string {
	for _, ln := range strings.Split(content, "\n") {
		if strings.Contains(ln, "🧠 Thinking") {
			return ln
		}
	}
	return ""
}

// ② thinking 预览不得把 markdown 围栏 ``` 当可见文本渲染，且自然语言仍在。
func TestW029_ThinkingPreviewStripsCodeFence(t *testing.T) {
	m := newTurnLayoutModel(t, 116)
	thinking := "先确认文件内容。\nexpected contents: ```json\n{\"a\": 1}\n```\n然后应用补丁。"
	_ = m.handleStreamDone(streamDoneMsg{
		blocks:    []session.ContentBlock{{Type: "text", Text: "答案"}},
		usage:     &session.TokenUsage{},
		model:     "test-model",
		requestID: "w029-fence",
		thinking:  thinking,
	})
	content := ansi.Strip(m.viewportContent)
	preview := w029ThinkingPreviewLine(content)
	if preview == "" {
		t.Fatalf("夹具不健全：转录里没有思考预览行：\n%s", content)
	}
	if strings.Contains(preview, "```") {
		t.Fatalf("思考预览仍把 markdown 围栏 ``` 当可见文本（W-029②）：\n%q", preview)
	}
	if !strings.Contains(preview, "先确认文件内容") {
		t.Fatalf("剥围栏后自然语言首句不得丢失（W-029②）：\n%q", preview)
	}
}

// ③ 工具调用头必须是单行、且名字与自由格式参数之间有显式分隔（不得读成 shell 命令）。
func TestW029_ToolHeader_MultilineParamNotGlued(t *testing.T) {
	m := newTurnLayoutModel(t, 116)
	patch := "--- a/t14_side.txt\n+++ b/t14_side.txt\n@@ -1,3 +1,3 @@\n side one\n-side two\n+side two patched\n side three\n"
	input, err := json.Marshal(map[string]string{"patch": patch})
	if err != nil {
		t.Fatalf("构造工具输入失败：%v", err)
	}
	_ = m.handleStreamDone(streamDoneMsg{
		blocks: []session.ContentBlock{
			{Type: "text", Text: "应用补丁。"},
			{Type: "tool_use", Name: "apply_unified_diff", Input: string(input)},
		},
		usage:     &session.TokenUsage{},
		model:     "test-model",
		requestID: "w029-header",
		thinking:  "",
	})
	content := ansi.Strip(m.viewportContent)

	// 头必须单行：原始多行参数的续行不得各自成行（+++ / @@ 出现在行首 = 头被拆成了多行）。
	for _, ln := range strings.Split(content, "\n") {
		trimmed := strings.TrimSpace(ln)
		if strings.HasPrefix(trimmed, "+++") || strings.HasPrefix(trimmed, "@@ ") {
			t.Fatalf("工具头把多行参数拆成了多行（W-029③），续行独立成行：\n%q\n---整屏---\n%s", ln, content)
		}
	}

	// 工具名行必须在场，且名字与参数之间出现显式分隔符 `· `。
	var header string
	found := 0
	for _, ln := range strings.Split(content, "\n") {
		if strings.Contains(ln, "apply_unified_diff") {
			header = ln
			found++
		}
	}
	if found != 1 {
		t.Fatalf("夹具不健全：apply_unified_diff 头应恰好 1 行，实际 %d 行：\n%s", found, content)
	}
	if !strings.Contains(header, "apply_unified_diff · ") {
		t.Fatalf("工具名与自由格式参数之间缺少显式分隔（W-029③）：\n%q", header)
	}
}

// ④ CJK 行首禁则：折行后任何一行都不得以「禁则字符」（。，、）等）开头；且字符不得丢。
func TestW029_WrapText_CJKLineStartKinsoku(t *testing.T) {
	m := NewRunewidthMeasurer(defaultWidthCacheSize)
	const limit = 4 // 2 个 CJK 宽字符/行
	cases := []struct {
		name string
		in   string
	}{
		{"全角闭括号居行首", "中文）文"},
		{"全角逗号居行首", "中文，文"},
		{"全角句号居行首", "中文。文"},
		{"连续两个禁则字符居行首", "中文），文"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			out := m.WrapText(tc.in, limit)
			lines := strings.Split(out, "\n")
			for i, ln := range lines {
				if ln == "" {
					continue
				}
				if isCJKLineStartForbidden([]rune(ln)[0]) {
					t.Fatalf("第 %d 行以禁则字符 %q 开头（W-029④）：\n%q", i+1, []rune(ln)[0], out)
				}
			}
			// 字符守恒：折行只动换行位置，不丢不改任何字符。
			if got := strings.ReplaceAll(out, "\n", ""); got != tc.in {
				t.Fatalf("折行改动了字符内容：want %q, got %q", tc.in, got)
			}
		})
	}
}

// ④ 的护栏：ANSI 着色内容不得因禁则处理被破坏（字符逐字守恒）。
func TestW029_WrapText_ANSIStyledContentNotCorrupted(t *testing.T) {
	m := NewRunewidthMeasurer(defaultWidthCacheSize)
	plain := "中文）文"
	styled := "\x1b[38;2;249;38;114m" + plain + "\x1b[0m"
	out := m.WrapText(styled, 4)
	if got := strings.ReplaceAll(ansi.Strip(out), "\n", ""); got != plain {
		t.Fatalf("ANSI 内容被禁则处理破坏：want %q, got %q（raw %q）", plain, got, out)
	}
}
