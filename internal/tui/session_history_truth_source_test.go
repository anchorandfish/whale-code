package tui

import (
	"strings"
	"testing"

	"github.com/anchorandfish/whale-code/internal/session"
)

// 本文件覆盖「会话历史真相源」大修的三个切口，每个测试都可判别：
// 注入反例能把修复前的实现打红。

// TestFormatToolResultMessage_PreservesFullOutput 切口①：resume/fork 派生时，
// tool 消息必须渲染完整 Output（而非修复前的 truncateRunes(TextContent(),200)，
// 后者对 RoleTool 返回空串——TextContent 只提取 type=="text" 块）。
func TestFormatToolResultMessage_PreservesFullOutput(t *testing.T) {
	full := strings.Repeat("line\n", 300) // 300 行，远超 200 字与 5+5 折叠阈值
	msg := session.ToolResult("id-1", "bash", full, false)

	// seq=0：这两个用例断言的是"折叠保留完整输出/短输出不折叠"，与序号无关，
	// 传 0（无序号上下文）保持原有语义不变。序号同源另由 tool_result_seq_test.go 覆盖。
	got := formatToolResultMessage(msg, 0)

	// 判别点 1：必须含折叠提示（/tool 入口），证明走了 head+tail 折叠而非
	// 修复前的 truncateRunes(TextContent(),200)（后者对 RoleTool 返回 "📋 " 空内容）。
	if !strings.Contains(got, "/tool") {
		t.Fatalf("期望折叠提示含 /tool 入口，got=%q", truncateRunes(got, 120))
	}
	// 判别点 2：折叠后仍保留原内容（head/tail 行），证明内容不是被吞成空。
	if !strings.Contains(got, "line") {
		t.Fatalf("期望折叠输出含原内容，got=%q", truncateRunes(got, 120))
	}
}

// TestFormatToolResultMessage_ShortOutput 短输出（≤ head+tail 行）不折叠，原样显示。
func TestFormatToolResultMessage_ShortOutput(t *testing.T) {
	msg := session.ToolResult("id-1", "bash", "hello world", false)
	// seq=0：这两个用例断言的是"折叠保留完整输出/短输出不折叠"，与序号无关，
	// 传 0（无序号上下文）保持原有语义不变。序号同源另由 tool_result_seq_test.go 覆盖。
	got := formatToolResultMessage(msg, 0)
	if !strings.Contains(got, "hello world") {
		t.Fatalf("短输出应原样保留，got=%q", got)
	}
	if strings.Contains(got, "/tool") {
		t.Fatalf("短输出不应出现折叠提示，got=%q", got)
	}
}

// TestHandleToolCommand_ReadsFromSession 切口②：/tool 从 session 权威源现查，
// 不依赖 lastToolResult 副本；/tool <N> 回看第 N 条而非永远最近一条。
func TestHandleToolCommand_ReadsFromSession(t *testing.T) {
	m := NewModel(newTestConfig(), newTestClient("test-model"), newTestRegistry())
	m.reassignServices() // 修复按值返回后的悬空服务别名（值副本陷阱）

	sess := session.NewSession()
	sess.SetMessages([]session.Message{
		session.UserText("do it"),
		session.ToolResult("id-1", "bash", "FIRST OUTPUT", false),
		session.ToolResult("id-2", "read", "SECOND OUTPUT", false),
	})
	m.SessionModel.SetCurrentSession(sess)

	// /tool 默认 = 最近一条（SECOND）。
	if got := m.handleToolCommand(""); !strings.Contains(got, "SECOND OUTPUT") {
		t.Fatalf("/tool 默认应返回最近一条，got=%q", got)
	}
	// /tool 1 = 第一条（FIRST）—— 这是可判别点：修复前 lastToolResult 只会是最近一条。
	if got := m.handleToolCommand("1"); !strings.Contains(got, "FIRST OUTPUT") {
		t.Fatalf("/tool 1 应返回第一条，got=%q", got)
	}
	// /tool 2 = 第二条。
	if got := m.handleToolCommand("2"); !strings.Contains(got, "SECOND OUTPUT") {
		t.Fatalf("/tool 2 应返回第二条，got=%q", got)
	}
	// 越界提示。
	if got := m.handleToolCommand("99"); !strings.Contains(got, "Only 2 tool result") {
		t.Fatalf("/tool 99 应提示越界，got=%q", got)
	}
}

// TestHandleToolCommand_NoTools 无工具结果时的提示。
func TestHandleToolCommand_NoTools(t *testing.T) {
	m := NewModel(newTestConfig(), newTestClient("test-model"), newTestRegistry())
	m.reassignServices()
	sess := session.NewSession()
	sess.SetMessages([]session.Message{session.UserText("hello")})
	m.SessionModel.SetCurrentSession(sess)

	if got := m.handleToolCommand(""); !strings.Contains(got, "No tool results") {
		t.Fatalf("无工具结果应提示，got=%q", got)
	}
}

// TestFormatCompactBoundary_Bilingual 切口③：分界文案英文主行 + 中文缩进辅助。
func TestFormatCompactBoundary_Bilingual(t *testing.T) {
	got := formatCompactBoundary()
	if !strings.Contains(got, "Compacted") {
		t.Fatalf("分界应含英文主行，got=%q", got)
	}
	if !strings.Contains(got, "已压缩") {
		t.Fatalf("分界应含中文辅助，got=%q", got)
	}
}

// TestResumeDerivesBoundaryMarker 切口③：resume 派生时，compact_boundary marker
// （RoleSystem + Subtype=="compact_boundary"）应渲染成可见分界，而非被静默丢弃。
// 这是 的 resume 半边：修复前 switch 无 RoleSystem case，marker 从窗口消失。
func TestResumeDerivesBoundaryMarker(t *testing.T) {
	m := NewModel(newTestConfig(), newTestClient("test-model"), newTestRegistry())
	m.reassignServices() // 修复按值返回后的悬空服务别名

	sess := session.NewSession()
	marker := session.Message{
		Role:    session.RoleSystem,
		Subtype: "compact_boundary",
		Blocks:  []session.ContentBlock{{Type: "text", Text: "[compact boundary]"}},
	}
	sess.SetMessages([]session.Message{
		session.UserText("before compact"),
		marker,
		session.AssistantText("after compact", "test-model", nil),
	})

	// 会话 id 必须进程内唯一（不能用 "test-session"）：TestMain 让整个测试进程共享一个
	// HOME ⇒ 共享会话锁目录， 占用闸门会把「本进程上一次的持有」判成别的活进程，
	// 2s 超时后整体拒绝装载。详见 uniqueTestSessionID 的注释。
	if err := m.applyResumedSession(uniqueTestSessionID(t), sess, "Resumed"); err != nil {
		t.Fatalf("resume 装载失败：%v", err)
	}

	rendered := strings.Join(m.SessionModel.Messages(), "\n")
	if !strings.Contains(rendered, "已压缩") {
		t.Fatalf("resume 后应渲染压缩分界，got=%q", truncateRunes(rendered, 300))
	}
}
