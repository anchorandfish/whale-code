package tui

import (
	"strings"
	"testing"
	"time"

	"github.com/anchorandfish/whale-code/internal/session"
	"github.com/anchorandfish/whale-code/internal/tools"
	"github.com/charmbracelet/x/ansi"
)

// 「工具执行成功、但输出为空」这一族的两个面。
//
// 现场（2026-09-30 · I-8.2 真机手测）：`echo hi > out/i8_created.txt` 的 stdout
// 本来就是空的，回传给模型的 tool_result content 因此是**空串** —— 会话存档
// `~/.whale/sessions/01a0f1d9-….jsonl` 里该 block 只剩 type / tool_use_id /
// tool_name 三个字段（空串被 omitempty 抹掉）。模型分不清「成功但无输出」与
// 「压根没跑」，于是把同一条命令连发了 4 次，触发「已连续执行 3 次 / 4 次」两条
// 循环告警；用户则看到 `📋 bash:` 后面一片空白。
//
// 生产入口（R2）：Model.handleToolResults（internal/tui/tool_exec.go:709）
//
//	├─ 显示面 tools.FormatToolResultStructWithHint（tool_exec.go:737）
//	└─ 回传面 session.ToolResult（tool_exec.go:745）
//
// ★ 两个面**故意拆成两条用例**（R4-10）——合在一条里时，前一个断言 FailNow 会让
// 后一个面的红/绿不可观测，就无法核对「两族变体的红集互不相交」。拆开后：
// 回退回传面兜底 ⇒ 只有 Relay 红；回退显示面兜底 ⇒ 只有 Rendered 红。

// emptyBashOutputModel 走**生产入口**喂一条「成功但空输出」的 bash 结果。
func emptyBashOutputModel(t *testing.T) *Model {
	t.Helper()
	m := newTUIRegressionModel(t)
	m.state = StateToolExec
	m.addMessage(formatAssistantMessageWithThinking(
		[]session.ContentBlock{{
			Type: "tool_use", ID: "bash-1", Name: "bash",
			Input: `{"command":"echo hi > out/i8_created.txt"}`,
		}},
		"", time.Now().UnixMilli(), 76,
	))
	m.handleToolResults([]tools.Result{{
		ID: "bash-1", Name: "bash", Output: "", IsError: false,
	}})
	return m
}

// TestEmptyToolOutputRelayedToModelIsNotBlank 判**回传面**：模型收到的那条 tool_result
// 不得为空串。空串就是"模型看不到任何结果"，它会把同一条命令重发（现场连发 4 次）。
//
// 判别性：回退 internal/session/session.go 的兜底（变体 A）⇒ 本条必须红。
func TestEmptyToolOutputRelayedToModelIsNotBlank(t *testing.T) {
	m := emptyBashOutputModel(t)

	var relayed string
	found := false
	for _, msg := range m.sessionService.CurrentSession().GetMessages() {
		for _, b := range msg.Blocks {
			if b.Type == "tool_result" {
				relayed, found = b.Output, true
			}
		}
	}
	if !found {
		t.Fatalf("session 里必须有一条配对的 tool_result（缺了网关侧会报工具配对不成立）")
	}
	if strings.TrimSpace(relayed) == "" {
		t.Fatalf("回传给模型的 tool_result 不得为空串 —— 模型会当成「没跑」而重复调用同一条命令。"+
			"\n现场：同一条 bash 连发 4 次，触发 3 次/4 次两条循环告警。got %q", relayed)
	}
}

// TestEmptyToolOutputRenderedInTranscriptIsNotBlank 判**显示面**：转录里 `📋 bash:`
// 之后必须有内容。空着会让用户以为结果丢了（现场就长这样）。
//
// 判别性：回退 internal/tools/tools.go 的显示面兜底（变体 B）⇒ 本条必须红。
func TestEmptyToolOutputRenderedInTranscriptIsNotBlank(t *testing.T) {
	m := emptyBashOutputModel(t)

	transcript := ansi.Strip(strings.Join(m.Messages(), "\n"))
	idx := strings.Index(transcript, "📋 bash:")
	if idx < 0 {
		t.Fatalf("转录里应有工具结果块，got %q", transcript)
	}
	rest := strings.TrimLeft(transcript[idx+len("📋 bash:"):], "\n")
	firstLine, _, _ := strings.Cut(rest, "\n")
	if strings.TrimSpace(firstLine) == "" {
		t.Fatalf("显示面的 `📋 bash:` 之后不得为空（用户会以为结果丢了）。该块：%q", transcript[idx:])
	}
}
