package permission

import (
	"testing"
)

// ============================================================================
// 阶段 2 · 收模式 8 → 3 的行为快照测试（TDD 红测）
//
// 目标模型（对齐业内既有实现 三档：read-only / workspace-write / danger-full-access）：
// - 枚举只保留 3 档；删除 accept-edits / dont-ask / plan / auto / bubble。
// - 旧模式字符串必须仍能被解析（向后兼容），翻译为 3 档之一，而非报错。
// - accept-edits 的语义（编辑免问 + 安全 bash 免问 + 危险 bash 确认）
// 通过「workspace-write + 默认 allow 规则」复刻，而不是保留独立档位。
// - dont-ask 并入 danger-full-access；danger 增加「危险命令二次确认」护栏，
// 使 dont-ask 的原有护栏（危险命令仍需确认）在并入后不丢失（不扩权）。
// - plan == read-only；auto 移除（同类 CLI 权限决策是确定性的，不靠 AI 猜）。
// - bubble 不再是会话档，降级为子代理委派参数；作为字符串解析时保守降级为
// workspace-write。
//
// 这些断言从「生产真实入口」进入：
// - ParseLevel：生产入口 plain.go:602（/permissions <mode>）与 plain.go:1821（CI 权限）
// - NewPolicy(...).Check：生产入口 lifecycle.go:294 / runner_core.go:378（启动装配）
// - Policy.SetModeByName：TUI /permissions <mode> 的单一入口
// ============================================================================

// --- 1. 旧字符串向后兼容解析为 3 档 ---

func TestParseLevel_LegacyModesCollapseToThreeTiers(t *testing.T) {
	cases := []struct {
		input string
		want  Level
	}{
		// 保留的 3 档
		{"read-only", LevelReadOnly},
		{"readonly", LevelReadOnly},
		{"workspace-write", LevelWorkspaceWrite},
		{"danger-full-access", LevelDangerFullAccess},
		// 删除的模式 → 迁移到 3 档
		{"accept-edits", LevelWorkspaceWrite},
		{"accept_edits", LevelWorkspaceWrite},
		{"acceptEdits", LevelWorkspaceWrite},
		{"dont-ask", LevelDangerFullAccess},
		{"dont_ask", LevelDangerFullAccess},
		{"dontAsk", LevelDangerFullAccess},
		{"plan", LevelReadOnly},
		{"auto", LevelWorkspaceWrite},
		{"bubble", LevelWorkspaceWrite},
	}
	for _, tc := range cases {
		got := ParseLevel(tc.input)
		if got != tc.want {
			t.Errorf("ParseLevel(%q) = %q, want %q（旧模式必须向后兼容翻译为 3 档之一）",
				tc.input, got.String(), tc.want.String())
		}
	}
}

func TestLevelString_CollapsedThreeTiers(t *testing.T) {
	// 3 档的字符串表示不变
	if LevelReadOnly.String() != "read-only" {
		t.Errorf("LevelReadOnly.String() = %q", LevelReadOnly.String())
	}
	if LevelWorkspaceWrite.String() != "workspace-write" {
		t.Errorf("LevelWorkspaceWrite.String() = %q", LevelWorkspaceWrite.String())
	}
	if LevelDangerFullAccess.String() != "danger-full-access" {
		t.Errorf("LevelDangerFullAccess.String() = %q", LevelDangerFullAccess.String())
	}
	// 越界值应回退到 "unknown"，而不是崩溃
	if Level(99).String() != "unknown" {
		t.Errorf("Level(99).String() = %q, want unknown", Level(99).String())
	}
}

// --- 2. danger-full-access 新增「危险命令二次确认」护栏 ---

func TestPolicy_Check_DangerFullAccess_DangerousCommandNeedsConfirm(t *testing.T) {
	p := NewPolicy(LevelDangerFullAccess)

	// 普通写操作：全放、免问
	if allowed, needConfirm := p.Check("write_file", `{"path":"a.go"}`); !allowed || needConfirm {
		t.Errorf("danger 模式 write_file: allowed=%v needConfirm=%v, want true/false", allowed, needConfirm)
	}

	// 危险 bash：允许，但必须二次确认（dont-ask 护栏并入后的不变量）
	allowed, needConfirm := p.Check("bash", `{"command":"rm -rf /"}`)
	if !allowed {
		t.Errorf("danger 模式危险 bash 应允许（带确认），got allowed=false")
	}
	if !needConfirm {
		t.Errorf("danger 模式危险 bash 必须二次确认（护栏不变量），got needConfirm=false")
	}

	// ★ P-002：原为幽灵名 `PowerShell`（从未注册）⇒ 改指真身 `Monitor`。
	// 危险 Monitor：同上
	allowed, needConfirm = p.Check("Monitor", `{"command":"rm -rf /"}`)
	if !allowed || !needConfirm {
		t.Errorf("danger 模式危险 Monitor: allowed=%v needConfirm=%v, want true/true", allowed, needConfirm)
	}
}

// --- 3. dont-ask 并入 danger：语义不变（不扩权、不缩权） ---

func TestPolicy_Check_DontAskBehaviorPreservedUnderDanger(t *testing.T) {
	p := NewPolicy(ParseLevel("dont-ask"))

	// 写工具自动接受
	if allowed, needConfirm := p.Check("write_file", `{"path":"a.go"}`); !allowed || needConfirm {
		t.Errorf("dont-ask 迁移后 write_file: allowed=%v needConfirm=%v, want true/false", allowed, needConfirm)
	}
	// 安全 bash 自动接受
	if allowed, needConfirm := p.Check("bash", `{"command":"ls -la"}`); !allowed || needConfirm {
		t.Errorf("dont-ask 迁移后安全 bash: allowed=%v needConfirm=%v, want true/false", allowed, needConfirm)
	}
	// 危险 bash 仍需确认（关键：并入 danger 后护栏必须保留）
	if allowed, needConfirm := p.Check("bash", `{"command":"rm -rf /"}`); !allowed || !needConfirm {
		t.Errorf("dont-ask 迁移后危险 bash: allowed=%v needConfirm=%v, want true/true（护栏不得丢失）", allowed, needConfirm)
	}
}

// --- 4. plan == read-only ---

func TestPolicy_Check_PlanCollapsesToReadOnly(t *testing.T) {
	p := NewPolicy(ParseLevel("plan"))
	if allowed, needConfirm := p.Check("read_file", `{"path":"a.go"}`); !allowed || needConfirm {
		t.Errorf("plan 迁移后 read_file: allowed=%v needConfirm=%v, want true/false", allowed, needConfirm)
	}
	if allowed, _ := p.Check("write_file", `{"path":"a.go"}`); allowed {
		t.Errorf("plan 迁移后 write_file 必须被拒（= read-only）")
	}
	if allowed, _ := p.Check("bash", `{"command":"ls"}`); allowed {
		t.Errorf("plan 迁移后 bash 必须被拒（= read-only）")
	}
}

// --- 5. accept-edits 语义由「workspace-write + 默认 allow 规则」复刻 ---

func TestPolicy_SetModeByName_AcceptEditsPreservesSemantics(t *testing.T) {
	p := NewPolicy(LevelWorkspaceWrite)

	// 经 TUI 生产入口切换（/permissions accept-edits）
	if !p.SetModeByName("accept-edits") {
		t.Fatalf("SetModeByName(accept-edits) 应成功（向后兼容）")
	}
	if p.Mode() != LevelWorkspaceWrite {
		t.Errorf("accept-edits 应收敛到 workspace-write，got %q", p.Mode().String())
	}

	// 编辑工具免问（accept-edits 的核心语义）
	for _, tool := range []string{"write_file", "edit_file", "NotebookEdit"} {
		if allowed, needConfirm := p.Check(tool, `{"path":"a.go"}`); !allowed || needConfirm {
			t.Errorf("accept-edits 迁移后 %s: allowed=%v needConfirm=%v, want true/false", tool, allowed, needConfirm)
		}
	}
	// 安全 bash 免问
	if allowed, needConfirm := p.Check("bash", `{"command":"ls -la"}`); !allowed || needConfirm {
		t.Errorf("accept-edits 迁移后安全 bash: allowed=%v needConfirm=%v, want true/false", allowed, needConfirm)
	}
	// 危险 bash 仍需确认
	if allowed, needConfirm := p.Check("bash", `{"command":"rm -rf /"}`); !allowed || !needConfirm {
		t.Errorf("accept-edits 迁移后危险 bash: allowed=%v needConfirm=%v, want true/true", allowed, needConfirm)
	}
	// L2声明即真相：未声明 agent_type 的 Agent 默认只读角色 explore（§7⑤），
	// 只拿只读工具面 ⇒ 无需确认。旧断言 want true/true 的前提是「未声明 = 任意能力」，
	// 已被「默认只读、不扩权」取代（需要写能力须显式声明 agent_type）。
	if allowed, needConfirm := p.Check("Agent", `{"task":"x"}`); !allowed || needConfirm {
		t.Errorf("accept-edits 迁移后 Agent(未声明类型): allowed=%v needConfirm=%v, want true/false（默认 explore 只读）", allowed, needConfirm)
	}
}

// accept-edits 注入的默认规则不能越过沙箱硬边界（P1：allow 只能免问不能扩权）。
func TestPolicy_SetModeByName_AcceptEditsDoesNotExpandCapability(t *testing.T) {
	p := NewPolicy(LevelReadOnly)
	p.SetModeByName("accept-edits")
	p.SetMode(LevelReadOnly) // 用户随后切回 read-only

	// read-only 下写操作必须仍被拒（默认规则不得扩权）
	if allowed, _ := p.Check("write_file", `{"path":"a.go"}`); allowed {
		t.Errorf("read-only 下 accept-edits 注入的 allow 规则不得放行写操作（沙箱硬边界）")
	}
}

// --- 6. auto / bubble 字符串保守降级 ---
func TestPolicy_SetModeByName_AutoBubbleDegradeToWorkspaceWrite(t *testing.T) {
	p := NewPolicy(LevelReadOnly)
	if !p.SetModeByName("auto") {
		t.Fatalf("SetModeByName(auto) 应成功（向后兼容）")
	}
	if p.Mode() != LevelWorkspaceWrite {
		t.Errorf("auto 字符串应降级为 workspace-write，got %q", p.Mode().String())
	}
	if !p.SetModeByName("bubble") {
		t.Fatalf("SetModeByName(bubble) 应成功（向后兼容）")
	}
	if p.Mode() != LevelWorkspaceWrite {
		t.Errorf("bubble 字符串应降级为 workspace-write，got %q", p.Mode().String())
	}
}

func TestPolicy_SetModeByName_UnknownFails(t *testing.T) {
	p := NewPolicy(LevelWorkspaceWrite)
	if p.SetModeByName("no-such-mode") {
		t.Errorf("SetModeByName(no-such-mode) 应返回 false")
	}
	if p.Mode() != LevelWorkspaceWrite {
		t.Errorf("未知模式不得改变当前模式")
	}
}

// --- 7. ModeCycle 收敛为 read-only ⇄ workspace-write（danger 需显式进入） ---

func TestModeCycle_TwoTierCycle(t *testing.T) {
	mc := NewModeCycle(LevelWorkspaceWrite)
	if got := mc.Next(); got != LevelReadOnly {
		t.Errorf("workspace-write.Next() = %q, want read-only", got.String())
	}
	if got := mc.Next(); got != LevelWorkspaceWrite {
		t.Errorf("read-only.Next() = %q, want workspace-write", got.String())
	}
	// 循环中不得出现 danger-full-access
	for _, l := range mc.CycleLevels() {
		if l == LevelDangerFullAccess {
			t.Errorf("danger-full-access 不应出现在 Shift+Tab 循环中（需显式进入）")
		}
	}
	if len(mc.CycleLevels()) != 2 {
		t.Errorf("循环档位应为 2，got %d", len(mc.CycleLevels()))
	}
}

func TestModeCycle_DangerInitialFallsBackToWorkspaceWrite(t *testing.T) {
	mc := NewModeCycle(LevelDangerFullAccess)
	if mc.Current() != LevelWorkspaceWrite {
		t.Errorf("danger 不在循环中，初始应回退 workspace-write，got %q", mc.Current().String())
	}
}

// --- 8. 全局不变量：3 档之外的 Level 值不再被生产决策使用 ---

func TestCheckModeLocked_OnlyThreeTiersProduceDecisions(t *testing.T) {
	// 每个保留档位都必须给出确定性决策（不 panic、不落 default 的 false/true）
	for _, lvl := range []Level{LevelReadOnly, LevelWorkspaceWrite, LevelDangerFullAccess} {
		p := NewPolicy(lvl)
		if _, _ = p.Check("read_file", `{"path":"a.go"}`); p.Mode() != lvl {
			t.Errorf("mode 装配不一致: %q", p.Mode().String())
		}
	}
}

// --- 9. 启动装配入口 NewPolicyFromModeName：旧配置模式得到与运行期切换一致的语义 ---

func TestNewPolicyFromModeName_AcceptEditsInjectsRules(t *testing.T) {
	// 启动装配（config 里写 accept-edits）必须与运行期 /permissions accept-edits 一致：
	// 编辑工具免问，而不是退化为普通 workspace-write（写需确认）。
	p := NewPolicyFromModeName("accept-edits")
	if p.Mode() != LevelWorkspaceWrite {
		t.Fatalf("NewPolicyFromModeName(accept-edits).Mode() = %q, want workspace-write", p.Mode().String())
	}
	if allowed, needConfirm := p.Check("edit_file", `{"path":"a.go"}`); !allowed || needConfirm {
		t.Errorf("启动装配 accept-edits 后 edit_file 应免问: allowed=%v needConfirm=%v", allowed, needConfirm)
	}
	// 注入规则数 = 5（write_file / edit_file / NotebookEdit / bash / PowerShell）。
	// TUI 人工测试 B-1 的「Rules: 5 active」断言以此为准。
	if n := p.RuleSet().Count(); n != 5 {
		t.Errorf("accept-edits 应注入 5 条默认 allow 规则，实际 %d", n)
	}
}

func TestNewPolicyFromModeName_PlanIsReadOnly(t *testing.T) {
	// 关键安全点：旧配置里的 plan 必须翻译为 read-only，而不是静默放权为 workspace-write。
	p := NewPolicyFromModeName("plan")
	if p.Mode() != LevelReadOnly {
		t.Fatalf("NewPolicyFromModeName(plan).Mode() = %q, want read-only", p.Mode().String())
	}
	if allowed, _ := p.Check("write_file", `{"path":"a.go"}`); allowed {
		t.Errorf("plan 装配后 write_file 必须被拒（read-only）")
	}
}

func TestNewPolicyFromModeName_DontAskIsDangerWithGuardrail(t *testing.T) {
	// 旧配置里的 dont-ask 必须翻译为 danger，且保留危险命令护栏。
	p := NewPolicyFromModeName("dont-ask")
	if p.Mode() != LevelDangerFullAccess {
		t.Fatalf("NewPolicyFromModeName(dont-ask).Mode() = %q, want danger-full-access", p.Mode().String())
	}
	if allowed, needConfirm := p.Check("bash", `{"command":"rm -rf /"}`); !allowed || !needConfirm {
		t.Errorf("dont-ask 装配后危险 bash 必须仍需确认: allowed=%v needConfirm=%v", allowed, needConfirm)
	}
}

func TestNewPolicyFromModeName_UnknownFallsBackWorkspaceWrite(t *testing.T) {
	p := NewPolicyFromModeName("bogus-mode")
	if p.Mode() != LevelWorkspaceWrite {
		t.Errorf("未知模式应回退 workspace-write，got %q", p.Mode().String())
	}
}
