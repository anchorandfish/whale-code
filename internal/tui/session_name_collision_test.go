package tui

import (
	"testing"

	"github.com/anchorandfish/whale-code/internal/session"
)

// 多开撞名：改造前 TUI 的会话名是从「首条用户输入 + 秒级时间戳」派生的，
// 不含任何进程唯一/随机成分（对比 session.GenerateID 带随机后缀）。
//
// 生产场景：两个终端同时开 whale，若首条输入的前 10 个字符相同（例如都问
// "你好"、或都贴同一段报错、或用同一个 prompt 模板），且落在同一秒内，
// 两个进程会生成**完全相同**的会话名 —— 于是两个独立对话写进同一个文件，
// 触发会话锁争用（表现为间歇 "Save failed"），历史也会混在一起。
//
// 本用例是改造后的**判别器**：会话主键必须进程唯一。
// 把实现退回「首条输入_时间戳」当主键，name1 == name2，本用例立刻转红。
// （改造前该缺陷的复现记录见 内部工作回执）
func TestAutoNameSession_ConcurrentInstancesGetDistinctIDs(t *testing.T) {
	const input = "帮我重构这段登录逻辑"

	m1 := newApprovalTestModel(t)
	m1.reassignServices()
	m2 := newApprovalTestModel(t)
	m2.reassignServices()

	m1.sessionService.SetFirstUserInput(input)
	m2.sessionService.SetFirstUserInput(input)

	m1.autoNameSession()
	m2.autoNameSession()

	name1 := m1.sessionService.SessionName()
	name2 := m2.sessionService.SessionName()
	if name1 == "" || name2 == "" {
		t.Fatalf("autoNameSession 未生成会话主键: %q / %q", name1, name2)
	}

	// 主键必须唯一 —— 这是「两个终端各跑一个 whale 不互相踩」的根条件。
	if name1 == name2 {
		t.Fatalf("两个实例生成相同会话主键 %q：两个对话会写进同一个文件", name1)
	}
	if !session.IsSessionID(name1) || !session.IsSessionID(name2) {
		t.Fatalf("会话主键应为 UUID，实际 %q / %q", name1, name2)
	}

	// 别名允许相同（用户输入一样，展示名自然一样）—— 别名不是身份，主键才是。
	alias1 := m1.sessionService.CurrentSession().Metadata.Name
	alias2 := m2.sessionService.CurrentSession().Metadata.Name
	if alias1 == "" || alias2 == "" {
		t.Fatalf("两个实例都应派生出可读别名，实际 %q / %q", alias1, alias2)
	}
}

// TestAutoNameSession_IsIdempotent 已有身份时不得再铸一个新主键。
//
// 为什么这条必须有：主键是 UUID，每次调用都会生成**新**值。重复调用会让
// 上一次已经落盘的会话文件变成孤儿（下一次 save 落到新文件，旧文件永不再被续写）。
// 改造前名字是「输入+时间戳」，重复调用收敛到同一个名字，所以这条不变量是
// UUID 化**引入的新前提**，必须显式判。
func TestAutoNameSession_IsIdempotent(t *testing.T) {
	m := isolatedHomeModel(t)

	m.sessionService.SetFirstUserInput("第一次输入")
	m.autoNameSession()
	first := m.sessionService.SessionName()
	if first == "" {
		t.Fatal("首次 autoNameSession 未生成主键")
	}

	// 第二次输入不同内容后再次命名 —— 身份必须保持
	m.sessionService.SetFirstUserInput("第二次输入")
	m.autoNameSession()
	if got := m.sessionService.SessionName(); got != first {
		t.Fatalf("第二次 autoNameSession 改写了会话主键：%q -> %q（已落盘会话会被孤儿化）", first, got)
	}
}
