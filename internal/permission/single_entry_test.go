package permission

import (
	"sync"
	"testing"
)

// 本文件验证「单一入口」这一不变量。
//
// 背景：Policy 曾有两个入口——Check() 不查规则，CheckWithRules() 才查。
// 而 internal/tui/tool_exec.go 与 internal/remote/rpc/client.go 调用的是 Check()，
// 导致用户配置的 deny 规则在这两条路径上被静默绕过。
//
// 语义基准（对齐业界实现，不是「能跑就行」）：
// - 同类 CLI：规则仲裁 deny > ask > allow，且 deny 在 bypassPermissions 下依然强制生效。
// - 同类 CLI：SandboxMode 与 AskForApproval 是两条独立的轴；规则在审批决策之前生效。
//
// 因此本文件断言的核心性质：
// 1. 任何入口对同一输入必须得到同一结果（单一入口的定义）。
// 2. deny 是绝对的：不受模式、不受来源优先级、不受 allow 规则影响。

// TestCheck_DenyRuleNotBypassable 是本次修复的核心回归测试。
// 修复前：Check() 忽略规则，返回 (true, true)，deny 被绕过。
func TestCheck_DenyRuleNotBypassable(t *testing.T) {
	p := NewPolicy(LevelWorkspaceWrite)
	rs := NewRuleSet()
	rs.Add(PermissionRule{
		ToolName: "bash",
		Behavior: RuleDeny,
		Source:   SourceSession,
	})
	p.SetRuleSet(rs)

	allowed, needConfirm := p.Check("bash", "echo hello")
	if allowed != false {
		t.Errorf("deny rule bypassed via Check(): allowed = %v, want false", allowed)
	}
	if needConfirm != false {
		t.Errorf("deny rule should not ask: needConfirm = %v, want false", needConfirm)
	}
}

// TestCheck_DenyRuleWithContentNotBypassable 验证带内容的 deny 规则同样生效，
// 且不匹配时正确回退到模式判定（不能因为加了规则就把所有调用都挡掉）。
func TestCheck_DenyRuleWithContentNotBypassable(t *testing.T) {
	p := NewPolicy(LevelWorkspaceWrite)
	rs := NewRuleSet()
	rs.Add(PermissionRule{
		ToolName:    "bash",
		RuleContent: "rm:*",
		Behavior:    RuleDeny,
		Source:      SourceSession,
	})
	p.SetRuleSet(rs)

	allowed, _ := p.Check("bash", "rm -rf /")
	if allowed != false {
		t.Errorf("matching deny rule bypassed: allowed = %v, want false", allowed)
	}

	// 不匹配的输入必须回退到模式判定，仍然放行（但需确认）。
	allowed, needConfirm := p.Check("bash", "echo hello")
	if allowed != true || needConfirm != true {
		t.Errorf("non-matching input should fall through to mode check: got (%v, %v), want (true, true)", allowed, needConfirm)
	}
}

// TestCheck_DenyEnforcedInAllModes 验证 deny 在所有模式下都生效，包括 danger-full-access。
// 对齐业内既有实现：deny 规则在 bypassPermissions 模式下同样强制生效。
func TestCheck_DenyEnforcedInAllModes(t *testing.T) {
	modes := []struct {
		name string
		mode Level
	}{
		{"workspace-write", LevelWorkspaceWrite},
		{"read-only", LevelReadOnly},
		{"danger-full-access", LevelDangerFullAccess},
		// 已删除模式经 ParseLevel 兼容翻译后的档位，deny 仍须生效
		{"accept-edits", ParseLevel("accept-edits")},
		{"plan", ParseLevel("plan")},
		{"dont-ask", ParseLevel("dont-ask")},
		{"auto", ParseLevel("auto")},
	}

	for _, tt := range modes {
		t.Run(tt.name, func(t *testing.T) {
			p := NewPolicy(tt.mode)
			rs := NewRuleSet()
			rs.Add(PermissionRule{
				ToolName: "bash",
				Behavior: RuleDeny,
				Source:   SourceSession,
			})
			p.SetRuleSet(rs)

			allowed, needConfirm := p.Check("bash", "echo hello")
			if allowed != false {
				t.Errorf("mode %s: deny rule not enforced, allowed = %v, want false", tt.name, allowed)
			}
			if needConfirm != false {
				t.Errorf("mode %s: deny should not ask, needConfirm = %v, want false", tt.name, needConfirm)
			}
		})
	}
}

// TestCheck_DenyBeatsAllowAcrossSources 验证行为优先级 deny > allow 压过来源优先级。
//
// P1 决策：deny 绝对优先已落地（rules.go GetMatchingRules 先按行为排序）。
// 该测试原为 Skip（记录「来源优先」的已知偏差），现取消 Skip 转为正向断言。
// 同类 CLI 语义：deny > ask > allow 绝对优先（来源优先级只用于配置作用域合并）。
func TestCheck_DenyBeatsAllowAcrossSources(t *testing.T) {
	p := NewPolicy(LevelWorkspaceWrite)
	rs := NewRuleSet()
	rs.Add(PermissionRule{ToolName: "bash", Behavior: RuleDeny, Source: SourceProjectSettings})
	rs.Add(PermissionRule{ToolName: "bash", Behavior: RuleAllow, Source: SourceCLIArg})
	p.SetRuleSet(rs)

	if allowed, _ := p.Check("bash", "echo hello"); allowed != false {
		t.Errorf("deny must beat allow regardless of source, got allowed = %v", allowed)
	}
}

// TestCheck_AllowRuleDoesNotExemptDangerousCommand 验证 allow 规则不能豁免危险命令。
// 对齐业内既有实现：审批策略可以免问，但不能免掉危险命令的确认。
func TestCheck_AllowRuleDoesNotExemptDangerousCommand(t *testing.T) {
	p := NewPolicy(LevelWorkspaceWrite)
	rs := NewRuleSet()
	rs.Add(PermissionRule{
		ToolName: "bash",
		Behavior: RuleAllow,
		Source:   SourceSession,
	})
	p.SetRuleSet(rs)

	allowed, needConfirm := p.Check("bash", "git push --force origin main")
	if allowed != true || needConfirm != true {
		t.Errorf("dangerous command under allow rule: got (%v, %v), want (true, true)", allowed, needConfirm)
	}
}

// TestCheck_AllowRuleCannotEscalateSandbox 验证沙箱硬边界（P1 决策 2）：
// allow 规则只能「免问」，不能「扩权」。read-only 模式下，写操作即便有 allow 规则也须被拒绝。
// 对齐业内既有实现：审批策略决定「要不要问」，沙箱（模式）决定「能不能做」，两者正交；
// 当「该问但不能问」时结果是 Reject 而非 AutoAllow（safety.rs:102）。
func TestCheck_AllowRuleCannotEscalateSandbox(t *testing.T) {
	p := NewPolicy(LevelReadOnly)
	rs := NewRuleSet()
	rs.Add(PermissionRule{
		ToolName: "write_file",
		Behavior: RuleAllow,
		Source:   SourceSession,
	})
	p.SetRuleSet(rs)

	// read-only 模式下写操作：即便 allow 规则命中，也须拒绝（硬边界不可扩权）
	allowed, needConfirm := p.Check("write_file", `{"path":"a.go","content":"x"}`)
	if allowed != false {
		t.Errorf("allow rule must not escalate write_file under read-only sandbox: allowed=%v, want false", allowed)
	}
	if needConfirm != false {
		t.Errorf("escalated write should be rejected without asking: needConfirm=%v, want false", needConfirm)
	}

	// 对照：read-only 模式下的读操作，allow 规则正常免问（不扩权也不拦）
	rs2 := NewRuleSet()
	rs2.Add(PermissionRule{ToolName: "read_file", Behavior: RuleAllow, Source: SourceSession})
	p2 := NewPolicy(LevelReadOnly)
	p2.SetRuleSet(rs2)
	allowed2, needConfirm2 := p2.Check("read_file", `{"path":"a.go"}`)
	if allowed2 != true || needConfirm2 != false {
		t.Errorf("allow rule on read_file under read-only should be allowed without confirm: got (%v, %v), want (true, false)", allowed2, needConfirm2)
	}
}

// TestCheck_EquivalentToCheckWithRules 是不变量测试：
// 两个入口对同一输入必须返回完全相同的结果，否则「单一入口」就是假的。
func TestCheck_EquivalentToCheckWithRules(t *testing.T) {
	cases := []struct {
		name  string
		mode  Level
		rules []PermissionRule
		tool  string
		input string
	}{
		{name: "no-rules-bash", mode: LevelWorkspaceWrite, tool: "bash", input: "echo hello"},
		{name: "no-rules-write", mode: LevelWorkspaceWrite, tool: "write_file", input: `{"path":"a.go"}`},
		{name: "no-rules-read", mode: LevelWorkspaceWrite, tool: "read_file", input: `{"path":"a.go"}`},
		{name: "deny-bash", mode: LevelWorkspaceWrite, tool: "bash", input: "echo hello",
			rules: []PermissionRule{{ToolName: "bash", Behavior: RuleDeny, Source: SourceSession}}},
		{name: "allow-bash", mode: LevelWorkspaceWrite, tool: "bash", input: "echo hello",
			rules: []PermissionRule{{ToolName: "bash", Behavior: RuleAllow, Source: SourceSession}}},
		{name: "ask-bash", mode: LevelWorkspaceWrite, tool: "bash", input: "echo hello",
			rules: []PermissionRule{{ToolName: "bash", Behavior: RuleAsk, Source: SourceSession}}},
		{name: "readonly-write-denied", mode: LevelReadOnly, tool: "write_file", input: `{"path":"a.go"}`},
		{name: "readonly-read-allowed", mode: LevelReadOnly, tool: "read_file", input: `{"path":"a.go"}`},
		{name: "full-access-deny", mode: LevelDangerFullAccess, tool: "bash", input: "echo hello",
			rules: []PermissionRule{{ToolName: "bash", Behavior: RuleDeny, Source: SourceSession}}},
		{name: "full-access-no-rule", mode: LevelDangerFullAccess, tool: "bash", input: "rm -rf /"},
		{name: "accept-edits-write", mode: ParseLevel("accept-edits"), tool: "write_file", input: `{"path":"a.go"}`},
		// 已知待议项：allow 规则目前可以越过 read-only 沙箱放行写操作。
		// 同类 CLI 把沙箱当作硬边界、allow 只能免问不能扩权；本阶段只保证两个入口一致，
		// 不改变该语义（留待收模式阶段决策，见 PERMISSION_HANDOFF.md 待拍板项）。
		{name: "readonly-write-allow-override", mode: LevelReadOnly, tool: "write_file", input: `{"path":"a.go"}`,
			rules: []PermissionRule{{ToolName: "write_file", Behavior: RuleAllow, Source: SourceSession}}},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			p := NewPolicy(tc.mode)
			rs := NewRuleSet()
			for _, r := range tc.rules {
				rs.Add(r)
			}
			p.SetRuleSet(rs)

			gotAllowed, gotConfirm := p.Check(tc.tool, tc.input)
			wantAllowed, wantConfirm := p.CheckWithRules(tc.tool, tc.input)

			if gotAllowed != wantAllowed || gotConfirm != wantConfirm {
				t.Errorf("Check() = (%v, %v), CheckWithRules() = (%v, %v): entry points must agree",
					gotAllowed, gotConfirm, wantAllowed, wantConfirm)
			}
		})
	}
}

// TestCheck_NoMatchingRulePreservesModeBehavior 是回归保护：
// 配置规则但当前调用不匹配时，行为必须与「没有规则」时完全一致。
// 修复若侵入了模式判定，这里会第一个红。
func TestCheck_NoMatchingRulePreservesModeBehavior(t *testing.T) {
	tools := []struct{ name, input string }{
		{"bash", "echo hello"},
		{"write_file", `{"path":"a.go"}`},
		{"read_file", `{"path":"a.go"}`},
		{"edit_file", `{"path":"a.go"}`},
		{"Agent", `{"prompt":"x"}`},
	}
	modes := []Level{LevelReadOnly, LevelWorkspaceWrite, LevelDangerFullAccess, ParseLevel("accept-edits"), ParseLevel("plan")}

	for _, mode := range modes {
		for _, tool := range tools {
			// 无规则基线
			bare := NewPolicy(mode)
			wantAllowed, wantConfirm := bare.Check(tool.name, tool.input)

			// 配一条匹配不到的规则
			p := NewPolicy(mode)
			rs := NewRuleSet()
			rs.Add(PermissionRule{
				ToolName:    "bash",
				RuleContent: "never-matches:*",
				Behavior:    RuleDeny,
				Source:      SourceSession,
			})
			p.SetRuleSet(rs)

			gotAllowed, gotConfirm := p.Check(tool.name, tool.input)
			if gotAllowed != wantAllowed || gotConfirm != wantConfirm {
				t.Errorf("mode=%v tool=%s: with unmatched rule got (%v,%v), bare policy got (%v,%v)",
					mode, tool.name, gotAllowed, gotConfirm, wantAllowed, wantConfirm)
			}
		}
	}
}

// TestCheck_ConcurrentRuleAccess 守护本次的锁结构调整：
// 修复把规则查询并入 Check，必须避免 RWMutex 读锁重入造成的死锁。
// 用 -race 运行时应通过。
func TestCheck_ConcurrentRuleAccess(t *testing.T) {
	p := NewPolicy(LevelWorkspaceWrite)
	rs := NewRuleSet()
	rs.Add(PermissionRule{
		ToolName: "bash",
		Behavior: RuleDeny,
		Source:   SourceSession,
	})
	p.SetRuleSet(rs)

	done := make(chan struct{})
	go func() {
		defer close(done)
		for i := 0; i < 200; i++ {
			p.Check("bash", "echo hello")
		}
	}()

	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < 200; j++ {
				p.Check("bash", "echo hello")
				p.CheckWithRules("write_file", `{"path":"a.go"}`)
			}
		}()
	}
	wg.Wait()
	<-done
}
