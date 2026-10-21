package skill

// trust_command_test.go — /skill trust 命令出口（拍板材料 §2 选项 a，2026-10-11）判据。
//
// 缺陷（修复前）：信任门生产已开（executor.go EnableTrustGate），拦截文案让用户敲
// `/skill trust <name>`，但该命令从未注册 ⇒ 用户被卡死在不存在的出口上（幽灵命令）。
//
// SSOT = (*Manager).HandleTrustCommand：TUI（registerCommonCommands）与 TUI Model（lifecycle.go）
// 两侧薄转发，实现与文案只此一份（双实现纪律）。判据在本包打在 SSOT 上；
// 接线存在性由 tui 包的 AST 结构门与 command 包的转发判据分别钉住。

import (
	"path/filepath"
	"strings"
	"testing"
)

// newTrustCommandTestManager 构造带信任门的测试 Manager（信任库落 t.TempDir，不碰真实用户目录）。
func newTrustCommandTestManager(t *testing.T) *Manager {
	t.Helper()
	m := NewManager(SourceDir{Path: filepath.Join(t.TempDir(), "skills"), Source: "project"})
	m.SetTrustStorePath(filepath.Join(t.TempDir(), "skills.trust.json"))
	m.EnableTrustGate()
	// project 来源：需首次确认；user 来源：免确认。
	m.Set("deploy-docs", Skill{Name: "deploy-docs", Description: "deploy helper", Prompt: "p1", Source: "project"})
	m.Set("my-own", Skill{Name: "my-own", Description: "user skill", Prompt: "p2", Source: "user"})
	return m
}

func TestHandleTrustCommand_TrustSingle(t *testing.T) {
	m := newTrustCommandTestManager(t)

	out := m.HandleTrustCommand("trust deploy-docs")
	if !strings.Contains(out, "deploy-docs") || !strings.Contains(out, "trusted") {
		t.Fatalf("trust 成功回执应含名字与 trusted：\n%s", out)
	}
	if st := m.TrustStatus(m.skills["deploy-docs"]); st != SkillTrustTrusted {
		t.Fatalf("信任库未生效：status = %q", st)
	}
}

func TestHandleTrustCommand_TrustUnknownName(t *testing.T) {
	m := newTrustCommandTestManager(t)
	out := m.HandleTrustCommand("trust no-such-skill")
	if !strings.Contains(out, "no-such-skill") || !strings.Contains(out, "not found") {
		t.Fatalf("未知名字应如实报 not found：\n%s", out)
	}
}

func TestHandleTrustCommand_TrustUserSourceNoop(t *testing.T) {
	m := newTrustCommandTestManager(t)
	out := m.HandleTrustCommand("trust my-own")
	if !strings.Contains(out, "does not require confirmation") {
		t.Fatalf("user 来源本就免确认，应如实说明而非假装确认成功：\n%s", out)
	}
}

func TestHandleTrustCommand_TrustAll(t *testing.T) {
	m := newTrustCommandTestManager(t)
	m.Set("second-docs", Skill{Name: "second-docs", Description: "d", Prompt: "p3", Source: "project"})

	out := m.HandleTrustCommand("trust all")
	if !strings.Contains(out, "2") {
		t.Fatalf("trust all 回执应含确认数量：\n%s", out)
	}
	if st := m.TrustStatus(m.skills["deploy-docs"]); st != SkillTrustTrusted {
		t.Fatalf("trust all 后 deploy-docs = %q", st)
	}
	if st := m.TrustStatus(m.skills["second-docs"]); st != SkillTrustTrusted {
		t.Fatalf("trust all 后 second-docs = %q", st)
	}
}

func TestHandleTrustCommand_Untrust(t *testing.T) {
	m := newTrustCommandTestManager(t)

	if out := m.HandleTrustCommand("trust deploy-docs"); !strings.Contains(out, "trusted") {
		t.Fatalf("前置 trust 失败：\n%s", out)
	}
	out := m.HandleTrustCommand("untrust deploy-docs")
	if !strings.Contains(out, "deploy-docs") {
		t.Fatalf("untrust 回执应含名字：\n%s", out)
	}
	if st := m.TrustStatus(m.skills["deploy-docs"]); st == SkillTrustTrusted {
		t.Fatal("untrust 后仍为 trusted —— 信任记录未删除（RevokeTrust 未被接到出口）")
	}
}

func TestHandleTrustCommand_UntrustNoRecord(t *testing.T) {
	m := newTrustCommandTestManager(t)
	out := m.HandleTrustCommand("untrust deploy-docs")
	if !strings.Contains(out, "no trust record") {
		t.Fatalf("无记录的 untrust 应如实报错：\n%s", out)
	}
}

func TestHandleTrustCommand_PendingList(t *testing.T) {
	m := newTrustCommandTestManager(t)

	// 空参数 = 列待确认项（帮助用户发现该确认什么）。
	out := m.HandleTrustCommand("")
	if !strings.Contains(out, "deploy-docs") || !strings.Contains(out, "untrusted") {
		t.Fatalf("待确认列表应含 project 来源 skill 与状态：\n%s", out)
	}
	if strings.Contains(out, "my-own") {
		t.Fatalf("user 来源免确认，不应出现在待确认列表：\n%s", out)
	}
}

func TestHandleTrustCommand_UnknownSub(t *testing.T) {
	m := newTrustCommandTestManager(t)
	out := m.HandleTrustCommand("frobnicate")
	if !strings.Contains(out, "/skill trust") || !strings.Contains(out, "/skill untrust") {
		t.Fatalf("未知子命令应回 usage（含两个真实存在的子命令）：\n%s", out)
	}
}

func TestHandleTrustCommand_NilManager(t *testing.T) {
	var m *Manager
	out := m.HandleTrustCommand("trust x") // 不得 panic
	if !strings.Contains(out, "not initialized") {
		t.Fatalf("nil manager 应降级提示：\n%s", out)
	}
}

func TestFormatList_ShowsTrustStatus(t *testing.T) {
	m := newTrustCommandTestManager(t)

	// 门开 + project 来源未确认 ⇒ 列表必须亮出 untrusted（被拦可见性，勿先写无出口命令的反面教材）。
	list := m.FormatList()
	if !strings.Contains(list, "[untrusted]") || !strings.Contains(list, "deploy-docs") {
		t.Fatalf("门开时列表应含 [untrusted] 状态列：\n%s", list)
	}
	if strings.Contains(list, "[untrusted] my-own") {
		t.Fatalf("user 来源免确认，不应带状态标记：\n%s", list)
	}

	// 确认后翻转 trusted。
	m.HandleTrustCommand("trust deploy-docs")
	list = m.FormatList()
	if !strings.Contains(list, "deploy-docs [trusted]") {
		t.Fatalf("确认后列表应翻转 [trusted]：\n%s", list)
	}
}

func TestFormatList_GateOff_NoTrustColumn(t *testing.T) {
	m := NewManager()
	m.Set("deploy-docs", Skill{Name: "deploy-docs", Description: "d", Prompt: "p", Source: "project"})

	list := m.FormatList()
	if strings.Contains(list, "[untrusted]") {
		t.Fatalf("门未启用时不应显示信任状态（会误导用户去确认一个不影响行为的门）：\n%s", list)
	}
}
