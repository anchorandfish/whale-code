package tui

import (
	"path/filepath"
	"strings"
	"testing"
)

// 生产入口层：TUI 用首条用户输入自动命名会话（autoNameSession）。
//
// 实测现象：用户输入 "请执行 rm /tmp/whale-ab/victim.txt"，
// 取前 10 字符得到 "请执行 rm /tm..."，保存时报
//
//	Save failed: failed to open session file: open
//	.../sessions/请执行 rm /tm..._2026-09-09_18-33-26.jsonl: no such file or directory
//
// 根因：会话名里的 '/' 被 filepath.Join 当成路径分隔符，
// "请执行 rm " 被当成不存在的子目录。
//
// 同类实现已在 runner 路径（runner_core.go autoNameSession）净化过，
// TUI 这份漏了 —— 同一功能两份实现发生漂移。
//
// 2026-09-20 改造后：会话主键是 UUID，不再来自输入，因此「主键含 '/'」在结构上
// 已不可能；但**别名**仍来自输入且会进 /session list 与选择器渲染，净化规则
// 依然必须生效 —— 所以本用例保留，并把断言从主键转移到别名。
func TestAutoNameSession_UserInputContainingSlash(t *testing.T) {
	m := newApprovalTestModel(t)
	m.reassignServices()

	// 真实用户输入：让用户操作文件路径是最常见的场景
	m.sessionService.SetFirstUserInput("请执行 rm /tmp/whale-ab/victim.txt")
	m.autoNameSession()

	name := m.sessionService.SessionName()
	if name == "" {
		t.Fatal("autoNameSession 未生成会话主键")
	}
	if strings.ContainsRune(name, '/') {
		t.Fatalf("会话主键含 '/': %q", name)
	}

	// 关键断言：拼进目录后不得逃出该目录（对应用户可见的 ENOENT）
	dir := t.TempDir()
	full := filepath.Join(dir, name+".jsonl")
	if got := filepath.Dir(full); got != dir {
		t.Fatalf("会话文件路径逃出会话目录: got %q, want %q", got, dir)
	}

	// 别名（会进列表/选择器）同样不得残留路径分隔符
	alias := m.sessionService.CurrentSession().Metadata.Name
	if alias == "" {
		t.Fatal("autoNameSession 未生成可读别名")
	}
	if strings.ContainsRune(alias, '/') {
		t.Fatalf("会话别名含 '/'，会破坏列表与导出文件名: %q", alias)
	}
}
