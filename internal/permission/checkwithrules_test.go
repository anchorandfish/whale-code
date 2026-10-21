package permission

import "testing"

// TestCheckWithRules_NoRules_NoMatchFallThrough 测试没有规则定义时，回退到普通 Check
func TestCheckWithRules_NoRules_NoMatchFallThrough(t *testing.T) {
	p := NewPolicy(LevelWorkspaceWrite)

	// 无规则时，bash 工具需要确认
	allowed, needConfirm := p.CheckWithRules("bash", "echo hello")
	if allowed != true {
		t.Errorf("expected allowed=true, got %v", allowed)
	}
	if needConfirm != true {
		t.Errorf("expected needConfirm=true, got %v", needConfirm)
	}

	// 无规则时，read_file 不需要确认
	allowed, needConfirm = p.CheckWithRules("read_file", "some/path")
	if allowed != true {
		t.Errorf("expected allowed=true, got %v", allowed)
	}
	if needConfirm != false {
		t.Errorf("expected needConfirm=false, got %v", needConfirm)
	}
}

// TestCheckWithRules_DenyRule_Blocks 测试 deny 规则阻止工具执行
func TestCheckWithRules_DenyRule_Blocks(t *testing.T) {
	p := NewPolicy(LevelWorkspaceWrite)
	rs := NewRuleSet()
	rs.Add(PermissionRule{
		ToolName: "bash",
		Behavior: RuleDeny,
		Source:   SourceSession,
	})
	p.SetRuleSet(rs)

	allowed, needConfirm := p.CheckWithRules("bash", "echo hello")
	if allowed != false {
		t.Errorf("expected allowed=false, got %v", allowed)
	}
	if needConfirm != false {
		t.Errorf("expected needConfirm=false, got %v", needConfirm)
	}
}

// TestCheckWithRules_DenyRule_WithContent 测试带内容的 deny 规则
func TestCheckWithRules_DenyRule_WithContent(t *testing.T) {
	p := NewPolicy(LevelWorkspaceWrite)
	rs := NewRuleSet()
	rs.Add(PermissionRule{
		ToolName:    "bash",
		RuleContent: "rm:*",
		Behavior:    RuleDeny,
		Source:      SourceSession,
	})
	p.SetRuleSet(rs)

	// 匹配 deny 规则的输入
	allowed, needConfirm := p.CheckWithRules("bash", "rm -rf /")
	if allowed != false {
		t.Errorf("expected allowed=false, got %v", allowed)
	}
	if needConfirm != false {
		t.Errorf("expected needConfirm=false, got %v", needConfirm)
	}

	// 不匹配 deny 规则的输入，回退到普通 Check
	allowed, needConfirm = p.CheckWithRules("bash", "echo hello")
	if allowed != true {
		t.Errorf("expected allowed=true (fallthrough), got %v", allowed)
	}
}

// TestCheckWithRules_AllowRule_AutoAllows 测试 allow 规则自动允许工具
func TestCheckWithRules_AllowRule_AutoAllows(t *testing.T) {
	p := NewPolicy(LevelWorkspaceWrite)
	rs := NewRuleSet()
	rs.Add(PermissionRule{
		ToolName: "bash",
		Behavior: RuleAllow,
		Source:   SourceSession,
	})
	p.SetRuleSet(rs)

	allowed, needConfirm := p.CheckWithRules("bash", "echo hello")
	if allowed != true {
		t.Errorf("expected allowed=true, got %v", allowed)
	}
	if needConfirm != false {
		t.Errorf("expected needConfirm=false, got %v", needConfirm)
	}
}

// TestCheckWithRules_AllowRule_WithContent 测试带内容的 allow 规则
func TestCheckWithRules_AllowRule_WithContent(t *testing.T) {
	p := NewPolicy(LevelWorkspaceWrite)
	rs := NewRuleSet()
	rs.Add(PermissionRule{
		ToolName:    "bash",
		RuleContent: "echo:*",
		Behavior:    RuleAllow,
		Source:      SourceSession,
	})
	p.SetRuleSet(rs)

	// 匹配 allow 规则
	allowed, needConfirm := p.CheckWithRules("bash", "echo hello")
	if allowed != true {
		t.Errorf("expected allowed=true, got %v", allowed)
	}
	if needConfirm != false {
		t.Errorf("expected needConfirm=false, got %v", needConfirm)
	}

	// 不匹配的输入回退到普通 Check
	allowed, needConfirm = p.CheckWithRules("bash", "ls -la")
	if allowed != true {
		t.Errorf("expected allowed=true (fallthrough), got %v", allowed)
	}
	if needConfirm != true {
		t.Errorf("expected needConfirm=true (fallthrough), got %v", needConfirm)
	}
}

// TestCheckWithRules_AskRule_NeedsConfirmation 测试 ask 规则需要确认
func TestCheckWithRules_AskRule_NeedsConfirmation(t *testing.T) {
	p := NewPolicy(LevelWorkspaceWrite)
	rs := NewRuleSet()
	rs.Add(PermissionRule{
		ToolName:    "bash",
		RuleContent: "npm publish:*",
		Behavior:    RuleAsk,
		Source:      SourceSession,
	})
	p.SetRuleSet(rs)

	allowed, needConfirm := p.CheckWithRules("bash", "npm publish my-package")
	if allowed != true {
		t.Errorf("expected allowed=true, got %v", allowed)
	}
	if needConfirm != true {
		t.Errorf("expected needConfirm=true, got %v", needConfirm)
	}
}

// TestCheckWithRules_AllowRule_DangerousBash_StillNeedsConfirm 测试 allow 规则对危险 bash 命令仍需确认
func TestCheckWithRules_AllowRule_DangerousBash_StillNeedsConfirm(t *testing.T) {
	p := NewPolicy(LevelWorkspaceWrite)
	rs := NewRuleSet()
	rs.Add(PermissionRule{
		ToolName: "bash",
		Behavior: RuleAllow,
		Source:   SourceSession,
	})
	p.SetRuleSet(rs)

	// 危险命令：即使有 allow 规则，仍需确认
	allowed, needConfirm := p.CheckWithRules("bash", "git push --force origin main")
	if allowed != true {
		t.Errorf("expected allowed=true, got %v", allowed)
	}
	if needConfirm != true {
		t.Errorf("expected needConfirm=true (dangerous override), got %v", needConfirm)
	}

	// 安全命令：allow 规则正常生效，不需要确认
	allowed, needConfirm = p.CheckWithRules("bash", "echo hello")
	if allowed != true {
		t.Errorf("expected allowed=true, got %v", allowed)
	}
	if needConfirm != false {
		t.Errorf("expected needConfirm=false, got %v", needConfirm)
	}
}

// TestCheckWithRules_AllowRule_CannotResurrectUnregisteredTool 验证 L1 的 fail-closed 不变量：
// **allow 规则不能把「未登记工具」从拒绝变成放行**。
//
// 沿革：`PowerShell` 曾是 permission 层硬编码识别的 shell 工具名（与 bash 同级），
// 故本用例原名 TestCheckWithRules_AllowRule_DangerousPowerShell_StillNeedsConfirm，
// 断言「有 allow 规则 ⇒ 允许，但危险命令仍需确认」。
//
// L1起档位唯一真相源是工具注册表里的 Spec.RequiredPermission，而
// `PowerShell` **从未注册为工具**：internal/tools/tools.go 的 registerBuiltinTools
// 无此项，T-B 的能力分桶表 capability.go 也无此项（其注释明言「没有默认桶」）。
// 它只是 TUI/权限层遗留的字符串特例。已核实生产**不可能**发出这个名字：
// Windows 下 shell 工具仍叫 `bash`，只换描述（internal/tui/stream.go:633），
// 故本改动对产品行为零影响。
//
// 按用户 2026-09-14 拍板「未登记兜底 → fail-closed 到最高档」，未登记工具在
// workspace-write 档应被**直接拒绝**；且沙箱硬边界（P1：allow 只能免问不能扩权）
// 要求 allow 规则不得越过它 —— 本用例正是这条不变量最直接的判别器。
func TestCheckWithRules_AllowRule_CannotResurrectUnregisteredTool(t *testing.T) {
	p := NewPolicy(LevelWorkspaceWrite)
	rs := NewRuleSet()
	rs.Add(PermissionRule{
		ToolName: "PowerShell",
		Behavior: RuleAllow,
		Source:   SourceSession,
	})
	p.SetRuleSet(rs)

	allowed, needConfirm := p.CheckWithRules("PowerShell", "git push --force origin main")
	if allowed {
		t.Errorf("未登记工具不得被 allow 规则放行（fail-closed 优先于 allow），got allowed=true")
	}
	if needConfirm {
		t.Errorf("被拒绝的调用不应请求确认，got needConfirm=true")
	}
}

// TestCheckWithRules_RulePriority_HigherSourceWinsOverLowerBehavior tests deny
// absolute priority (P1 decision): deny beats allow regardless of source.
// A cliArg(5) allow must NOT override a userSettings(1) deny — deny always wins,
// aligning with a comparable CLI (Decision::Forbidden via max) and 同类 CLI (deny > ask > allow).
func TestCheckWithRules_RulePriority_HigherSourceWinsOverLowerBehavior(t *testing.T) {
	p := NewPolicy(LevelWorkspaceWrite)
	rs := NewRuleSet()
	// cliArg 级别 allow (source priority = 5)
	rs.Add(PermissionRule{
		ToolName: "bash",
		Behavior: RuleAllow,
		Source:   SourceCLIArg,
	})
	// userSettings 级别 deny (source priority = 1)
	rs.Add(PermissionRule{
		ToolName: "bash",
		Behavior: RuleDeny,
		Source:   SourceUserSettings,
	})
	p.SetRuleSet(rs)

	// deny 绝对优先：userSettings deny 必须压过 cliArg allow
	allowed, needConfirm := p.CheckWithRules("bash", "echo hello")
	if allowed != false {
		t.Errorf("expected allowed=false (deny beats allow across sources), got %v", allowed)
	}
	if needConfirm != false {
		t.Errorf("expected needConfirm=false (deny does not ask), got %v", needConfirm)
	}
}

// TestCheckWithRules_RulePriority_SameBehavior_CliArgOverSession 测试同行为类型时来源优先级：cliArg > session
func TestCheckWithRules_RulePriority_SameBehavior_CliArgOverSession(t *testing.T) {
	p := NewPolicy(LevelWorkspaceWrite)
	rs := NewRuleSet()
	// session 级别 ask
	rs.Add(PermissionRule{
		ToolName:    "bash",
		RuleContent: "npm:*",
		Behavior:    RuleAsk,
		Source:      SourceSession,
	})
	// cliArg 级别 ask（同行为，来源优先级更高，但结果都是 ask）
	rs.Add(PermissionRule{
		ToolName:    "bash",
		RuleContent: "npm:*",
		Behavior:    RuleAsk,
		Source:      SourceCLIArg,
	})
	p.SetRuleSet(rs)

	// 两条 ask 规则，结果都是 allowed=true, needConfirm=true
	allowed, needConfirm := p.CheckWithRules("bash", "npm publish")
	if allowed != true {
		t.Errorf("expected allowed=true, got %v", allowed)
	}
	if needConfirm != true {
		t.Errorf("expected needConfirm=true, got %v", needConfirm)
	}
}

// TestCheckWithRules_RulePriority_SameBehavior_SessionOverUserSettings 测试同行为类型时来源优先级：session > userSettings
func TestCheckWithRules_RulePriority_SameBehavior_SessionOverUserSettings(t *testing.T) {
	p := NewPolicy(LevelWorkspaceWrite)
	rs := NewRuleSet()
	// userSettings 级别 allow
	rs.Add(PermissionRule{
		ToolName:    "bash",
		RuleContent: "echo:*",
		Behavior:    RuleAllow,
		Source:      SourceUserSettings,
	})
	// session 级别 allow（同行为，来源优先级更高）
	rs.Add(PermissionRule{
		ToolName:    "bash",
		RuleContent: "echo:*",
		Behavior:    RuleAllow,
		Source:      SourceSession,
	})
	p.SetRuleSet(rs)

	// 两条 allow 规则，结果都是 allowed=true, needConfirm=false
	allowed, needConfirm := p.CheckWithRules("bash", "echo hello")
	if allowed != true {
		t.Errorf("expected allowed=true, got %v", allowed)
	}
	if needConfirm != false {
		t.Errorf("expected needConfirm=false, got %v", needConfirm)
	}
}

// TestCheckWithRules_RulePriority_DenyHigherSourceWins 测试高优先级来源的 deny 胜过低优先级来源的 allow
func TestCheckWithRules_RulePriority_DenyHigherSourceWins(t *testing.T) {
	p := NewPolicy(LevelWorkspaceWrite)
	rs := NewRuleSet()
	rs.Add(PermissionRule{
		ToolName: "bash",
		Behavior: RuleAllow,
		Source:   SourceUserSettings,
	})
	rs.Add(PermissionRule{
		ToolName: "bash",
		Behavior: RuleDeny,
		Source:   SourceSession,
	})
	p.SetRuleSet(rs)

	// session deny 胜过 userSettings allow
	allowed, needConfirm := p.CheckWithRules("bash", "echo hello")
	if allowed != false {
		t.Errorf("expected allowed=false (session deny wins), got %v", allowed)
	}
	if needConfirm != false {
		t.Errorf("expected needConfirm=false, got %v", needConfirm)
	}
}

// TestCheckWithRules_DenyRuleForOtherTool_NoEffect 测试对其他工具的 deny 规则不影响当前工具
func TestCheckWithRules_DenyRuleForOtherTool_NoEffect(t *testing.T) {
	p := NewPolicy(LevelWorkspaceWrite)
	rs := NewRuleSet()
	rs.Add(PermissionRule{
		ToolName: "write_file",
		Behavior: RuleDeny,
		Source:   SourceSession,
	})
	p.SetRuleSet(rs)

	// bash 没有匹配规则，回退到普通 Check
	allowed, needConfirm := p.CheckWithRules("bash", "echo hello")
	if allowed != true {
		t.Errorf("expected allowed=true (fallthrough), got %v", allowed)
	}
	if needConfirm != true {
		t.Errorf("expected needConfirm=true (fallthrough), got %v", needConfirm)
	}
}

// TestCheckWithRules_FullAccessMode_NoRules 测试全访问模式下无规则时的行为
func TestCheckWithRules_FullAccessMode_NoRules(t *testing.T) {
	p := NewPolicy(LevelDangerFullAccess)

	allowed, needConfirm := p.CheckWithRules("bash", "echo hello")
	if allowed != true {
		t.Errorf("expected allowed=true, got %v", allowed)
	}
	if needConfirm != false {
		t.Errorf("expected needConfirm=false, got %v", needConfirm)
	}
}

// TestCheckWithRules_FullAccessMode_DenyRuleOverrides 测试全访问模式下 deny 规则仍然生效
func TestCheckWithRules_FullAccessMode_DenyRuleOverrides(t *testing.T) {
	p := NewPolicy(LevelDangerFullAccess)
	rs := NewRuleSet()
	rs.Add(PermissionRule{
		ToolName: "bash",
		Behavior: RuleDeny,
		Source:   SourceCLIArg,
	})
	p.SetRuleSet(rs)

	allowed, needConfirm := p.CheckWithRules("bash", "echo hello")
	if allowed != false {
		t.Errorf("expected allowed=false (deny rule overrides full access), got %v", allowed)
	}
	if needConfirm != false {
		t.Errorf("expected needConfirm=false, got %v", needConfirm)
	}
}
