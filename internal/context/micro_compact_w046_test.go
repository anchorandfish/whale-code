package context

import (
	"regexp"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/anchorandfish/whale-code/internal/session"
)

// ============================================================================
// W-046a 同类点判别器：MicroCompact 清除占位符的行数口径
//
// 现象：micro_compact.go 的清除占位符 "[Old tool result content cleared — N chars, M lines…]"
// 用 strings.Count(output,"\n")+1 统计 —— 末尾带换行的工具输出会**多报 1 行**，
// 与 read_file（Scanner 编辑器语义）对同一内容的读数矛盾（W-046a 同族）。
//
// 判据：构造 50 行、末尾带换行的旧工具输出（时间上已超出 TimeThreshold ⇒ 必被清除），
// 断言占位符里的 M == 50（编辑器语义）。before（+1 语义）= 51 ⇒ 必红。
// ============================================================================

var w046ClearedRe = regexp.MustCompile(`(\d+) chars, (\d+) lines`)

func TestW046_MicroCompact_ClearedPlaceholderUsesEditorLineSemantics(t *testing.T) {
	cfg := MicroCompactConfig{
		TimeThreshold:       30 * time.Minute,
		PreserveRecent:      0,
		TruncationThreshold: 10, // 远小于夹具长度 ⇒ 必触发清除
	}
	const wantLines = 50
	output := strings.Repeat("line\n", wantLines) // 250 字节、编辑器语义 50 行、末尾带换行

	msg := session.Message{
		Role:      session.RoleTool,
		Timestamp: time.Now().UnixMilli() - 60*60*1000, // 1 小时前 ⇒ 非近期、PreserveRecent=0 ⇒ 必被处理
		Blocks: []session.ContentBlock{
			{Type: "tool_result", ToolName: "bash", Output: output},
		},
	}

	got := MicroCompact([]session.Message{msg}, cfg)
	out := got[0].Blocks[0].Output

	m := w046ClearedRe.FindStringSubmatch(out)
	if m == nil {
		t.Fatalf("占位符未生成（块未被清除？）。实际输出: %q", out)
	}
	gotLines, _ := strconv.Atoi(m[2])
	if gotLines != wantLines {
		t.Fatalf("W-046a 同类点：清除占位符报 %d 行，编辑器语义应为 %d（输出末尾带换行，多算了一行）。占位符: %q",
			gotLines, wantLines, out)
	}
}
