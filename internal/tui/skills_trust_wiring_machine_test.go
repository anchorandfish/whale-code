package tui

// /skill 信任出口接线机测（2026-10-11，O-2 收口；收敛到拍板材料 §2 选项 a 契约）。
//
// 背景：skill 信任门生产已开（tools/executor.go:312 EnableTrustGate），project/plugin
// 来源的 skill 首次使用被拦，拦截文案教用户跑 `/skill trust <name>` —— 但该命令从未注册
// （写侧 API TrustSkill/TrustAllPending/RevokeTrust 零调用点）。
//
// 收敛后的结构：
// SSOT = skill.Manager.HandleTrustCommand（internal/skill/trust_command_test.go 在本包判）
// 注册 = command 包 "skill" 正门（internal/command/command_skill_trust_test.go 判）
// 接线 = TUI lifecycle.go NewModel 与 plain/bridge runner_core.go registerCommonCommands
// 各自把 cmdRegistry.OnSkillManage 薄转发到 SSOT —— 本文件判这两条接线的行为面。
//
// 生产路径（R1）：
// TUI: m.handleCommand("/skill trust ...") (app.go → cmdRegistry.Get("skill"))
// → Command.Handler → OnSkillManage 回调 → HandleTrustCommand → 信任库落盘
// plain: registerCommonCommands（与生产 NewPlainRunner 同一装配）→ 同一 Handler/回调链
//
// 判别性：before（无注册/无接线）时 handleCommand("/skill ...") 返回 Unknown、
// Handler 返回 "not initialized" ⇒ 全部语义红（本轮已实测）。
// 信任库落 t.TempDir，不写真实 ~/.whale/skills.trust.json。

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/anchorandfish/whale-code/internal/skill"
)

func TestSkillTrustCommandEndToEnd(t *testing.T) {
	m := newTUIRegressionModel(t)
	store := filepath.Join(t.TempDir(), "skills.trust.json")
	m.executor.SkillMgr.SetTrustStorePath(store) // 隔离：不写真实用户信任库
	m.executor.SkillMgr.Set("zz-trust-probe", skill.Skill{
		Name: "zz-trust-probe", Description: "probe", Prompt: "probe body", Source: "project",
	})

	// 前置不回归：/skills 列表分支不受 /skill 正门影响
	if out := m.handleCommand("/skills"); !strings.Contains(out, "Available skills") {
		t.Fatalf("前置失败：/skills 列表分支被破坏：%q", out)
	}

	// ① 列待确认（走生产入口 handleCommand → 注册表 → 回调 → SSOT）
	out := m.handleCommand("/skill trust")
	if !strings.Contains(out, "zz-trust-probe") {
		t.Fatalf("/skill trust 应列出待确认 skill，实际输出不含探针名（注册/接线/SSOT 任一环缺失）：%q", out)
	}

	// ② 确认单个（写路径经 TrustSkill）
	out = m.handleCommand("/skill trust zz-trust-probe")
	if !strings.Contains(out, "zz-trust-probe") || !strings.Contains(out, "trusted") {
		t.Fatalf("/skill trust <name> 应确认成功：%q", out)
	}

	// ③ 产物面：信任库真的落盘且含该 skill（R4 产物面判据，非"调了函数"接线断言）
	data, err := os.ReadFile(store)
	if err != nil {
		t.Fatalf("信任库未落盘（写丢失）：%v", err)
	}
	if !strings.Contains(string(data), "zz-trust-probe") {
		t.Fatalf("信任库内容不含探针 skill：%q", string(data))
	}

	// ④ 确认后不再列为待确认
	if out := m.handleCommand("/skill trust"); strings.Contains(out, "zz-trust-probe") {
		t.Fatalf("确认后 /skill trust 仍列出该 skill（写丢失或状态未刷新）：%q", out)
	}

	// ⑤ 撤销（untrust）后重新回到待确认 —— 门要有开也要有关
	out = m.handleCommand("/skill untrust zz-trust-probe")
	if !strings.Contains(out, "zz-trust-probe") {
		t.Fatalf("/skill untrust 应回执名字：%q", out)
	}
	if out := m.handleCommand("/skill trust"); !strings.Contains(out, "zz-trust-probe") {
		t.Fatalf("untrust 后 /skill trust 应回到待确认列表（撤销链断裂）：%q", out)
	}
}

func TestSkillTrustPlainWiring(t *testing.T) {
	r := newTestPlainRunner()
	// 与生产 NewPlainRunner 同一装配函数（runner_core.go registerCommonCommands）——
	// 不手写回调（那会制造第二份接线，测的不是生产）。
	registerCommonCommands(r.runnerCore, nil, nil, nil, nil, nil, nil, nil, nil)

	store := filepath.Join(t.TempDir(), "skills.trust.json")
	r.Executor.SkillMgr.SetTrustStorePath(store)
	r.Executor.SkillMgr.Set("zz-trust-probe-plain", skill.Skill{
		Name: "zz-trust-probe-plain", Description: "probe", Prompt: "probe body", Source: "project",
	})

	if r.CmdRegistry.OnSkillManage == nil {
		t.Fatal("plain 生产装配（registerCommonCommands）未接 OnSkillManage —— plain 侧出口断裂（ 只验一侧=半修）")
	}
	cmd, ok := r.CmdRegistry.Get("skill")
	if !ok {
		t.Fatal("plain CmdRegistry 里没有 /skill 命令")
	}
	out, err := cmd.Handler("trust zz-trust-probe-plain")
	if err != nil {
		t.Fatalf("plain /skill trust Handler 报错：%v", err)
	}
	if !strings.Contains(out, "zz-trust-probe-plain") || !strings.Contains(out, "trusted") {
		t.Fatalf("plain /skill trust 应确认成功：%q", out)
	}
	data, err := os.ReadFile(store)
	if err != nil || !strings.Contains(string(data), "zz-trust-probe-plain") {
		t.Fatalf("plain 侧信任库未落盘或内容缺失（data=%q, err=%v）", string(data), err)
	}
}
