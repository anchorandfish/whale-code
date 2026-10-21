package tui

import (
	"strings"
	"testing"
)

// TestRenderElicitationUI_SanitizesControlChars 是 的判别器。
//
// 断言 renderElicitationUI() 的输出**不含**终端控制字符（\x1b、\r 等），
// 即使 PendingConfirm 里的 McpServerName / McpMessage 被注入了这些字符
// （模拟被攻陷 MCP server 的 elicitation 载荷）。
//
// ★ 判别力自证：改前 renderElicitationUI 只 truncateRunes(200) 纯截断，
//
//	注入 \x1b[2J / \r 会原样透传到输出 ⇒ 本测试为红；接入 sanitize 后转绿。
func TestRenderElicitationUI_SanitizesControlChars(t *testing.T) {
	m := newApprovalTestModel(t)

	// 4 种攻击载荷：清屏、回车伪造 Server 名、隐藏、改终端标题。
	evilServerName := "evil-server\rTrusted"
	evilMessage := "\x1b[2JPlease enter credentials\x1b[8mhidden\x1b]0;evil-title"

	pc := &PendingConfirm{
		Kind:          ApprovalKindMcpElicitation,
		Options:       buildApprovalOptions(ApprovalKindMcpElicitation),
		SelectIdx:     0,
		McpServerName: evilServerName,
		McpRequestID:  []byte(`"req-1"`),
		McpMessage:    evilMessage,
	}
	m.toolService.SetPendingConfirm(pc)

	out := m.renderElicitationUI()

	// 输出不得含任何 ESC / CR / 其它 C0 控制字符（\n \t 除外）。
	forbidden := []string{"\x1b", "\r", "\x00", "\x07", "\x1b[2J", "\x1b[8m", "\x1b]0;"}
	for _, f := range forbidden {
		if strings.Contains(out, f) {
			t.Errorf("renderElicitationUI 输出含控制序列 %q（未净化）\n输出：%q", f, out)
		}
	}

	// 净化后仍应保留可读内容（而不是整体丢弃）。
	if !strings.Contains(out, "Please enter credentials") {
		t.Errorf("净化后应保留可读 message 内容，输出：%q", out)
	}
}
