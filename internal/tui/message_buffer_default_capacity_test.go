package tui

import "testing"

// 守卫：messageBuffer 默认上限必须覆盖长会话（2026-10-08 用户真机报障拍板）。
//
// 背景：defaultMaxMessages=1000 时，长会话（如 53 分钟 / 数百轮）很快触顶，
// FIFO 淘汰最旧消息 ⇒ 用户看到「历史对话消失了很多」且无法滚回
// （alt-screen 下终端原生 scrollback 恒空，被淘汰的内容物理不可恢复）。
//
// 2026-10-08 拍板：救急方案 = 上限提到 10000（大修「原生 scrollback 接线」另议）。
//
// 判别力（before 必须红）：上限仍为 1000 时，第 1001 条追加会把首条挤掉，
// 本测试的「首条仍在」断言即红；常量下限断言同样红。
func TestMessageBufferDefaultCapacityCoversLongSessions(t *testing.T) {
	if defaultMaxMessages < 10000 {
		t.Fatalf("defaultMaxMessages = %d, want >= 10000（长会话救急拍板 2026-10-08：太小会让历史被 FIFO 淘汰消失）",
			defaultMaxMessages)
	}

	// 行为级：走生产兜底路径（maxMessages<=0 ⇒ 取 defaultMaxMessages），
	// 与三个生产构造点（service_session.go / lifecycle.go）同一来源。
	b := NewMessageBuffer(0, nil)
	total := 1001 // 旧上限 1000 的「压线反例」：恰好触发一次淘汰
	first := "first-message-that-must-survive"
	b.Append(first)
	for i := 0; i < total-1; i++ {
		b.Append("filler")
	}

	if got := b.Len(); got != total {
		t.Fatalf("Len = %d, want %d", got, total)
	}
	all := b.All()
	if all[0] != first {
		t.Fatalf("追加 %d 条后首条已被 FIFO 淘汰 ⇒ 默认上限不足，历史会从屏幕消失（旧 1000 上限的老毛病）", total)
	}
}
