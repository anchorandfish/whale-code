package permission

// 权限规则来源优先级反转测试
//
// 本文件验证 posix 模式（对齐 POSIX ACL）与 legacy 模式（向后兼容）的优先级行为。
// 对标 POSIX ACL（@https://pubs.opengroup.org/onlinepubs/9699919799/ 访问 2026-07-21）：
// user:: > group:: > other:: 特异性递减原则。
//
// posix 模式优先级：local(1) < project(2) < user(3) < session(4) < cliArg(5)
// legacy 模式优先级：user(1) < project(2) < local(3) < session(4) < cliArg(5)

import "testing"

// TestDefaultRuleSetIsPosix 验证 NewRuleSet 默认为 posix 模式（修复 P1：默认优先级反直觉）。
// 修复前：默认 legacy（user 最低），反直觉。
// 修复后：默认 posix（user > project > local），对齐 POSIX ACL 行业惯例。
func TestDefaultRuleSetIsPosix(t *testing.T) {
	rs := NewRuleSet()
	if rs.PriorityMode() != PriorityPosix {
		t.Errorf("NewRuleSet 默认模式 = %s, want posix (user > project > default)", rs.PriorityMode())
	}
}

// TestPosixPriorityChain 验证 posix 模式完整优先级链：
// local(1) < project(2) < user(3) < session(4) < cliArg(5)。
func TestPosixPriorityChain(t *testing.T) {
	rs := NewRuleSet()
	rs.SetPriorityMode(PriorityPosix)

	rs.Add(PermissionRule{ToolName: "Bash", Behavior: RuleAllow, Source: SourceLocalSettings})
	rs.Add(PermissionRule{ToolName: "Bash", Behavior: RuleAllow, Source: SourceProjectSettings})
	rs.Add(PermissionRule{ToolName: "Bash", Behavior: RuleAllow, Source: SourceUserSettings})
	rs.Add(PermissionRule{ToolName: "Bash", Behavior: RuleAllow, Source: SourceSession})
	rs.Add(PermissionRule{ToolName: "Bash", Behavior: RuleAllow, Source: SourceCLIArg})

	rule := rs.ResolveRuleDecision("Bash", `{"command":"ls"}`)
	if rule == nil {
		t.Fatal("期望匹配到规则")
	}
	if rule.Source != SourceCLIArg {
		t.Errorf("posix 模式 cliArg 应为最高优先级, got source=%s", rule.Source)
	}
}

// TestPosixUserOverridesProject 验证 posix 模式 user 胜 project（对齐 POSIX ACL user:: > group::）。
func TestPosixUserOverridesProject(t *testing.T) {
	rs := NewRuleSet()
	rs.SetPriorityMode(PriorityPosix)

	rs.Add(PermissionRule{ToolName: "Bash", Behavior: RuleAllow, Source: SourceProjectSettings})
	rs.Add(PermissionRule{ToolName: "Bash", Behavior: RuleDeny, Source: SourceUserSettings})

	rule := rs.ResolveRuleDecision("Bash", `{"command":"ls"}`)
	if rule == nil {
		t.Fatal("期望匹配到规则")
	}
	if rule.Behavior != RuleDeny || rule.Source != SourceUserSettings {
		t.Errorf("posix 模式 user deny 应胜 project allow, got behavior=%s source=%s", rule.Behavior, rule.Source)
	}
}

// TestPosixUserOverridesLocal 验证 posix 模式 user 胜 local（对齐 POSIX ACL user:: > other::）。
// P1 决策后：deny 绝对优先凌驾于来源之上，故来源优先级改用「同行为类型」验证
// （deny vs deny，看哪个 source 胜），避免 allow/deny 混排被 deny 绝对优先干扰。
func TestPosixUserOverridesLocal(t *testing.T) {
	rs := NewRuleSet()
	rs.SetPriorityMode(PriorityPosix)

	rs.Add(PermissionRule{ToolName: "Bash", Behavior: RuleDeny, Source: SourceLocalSettings})
	rs.Add(PermissionRule{ToolName: "Bash", Behavior: RuleDeny, Source: SourceUserSettings})

	rule := rs.ResolveRuleDecision("Bash", `{"command":"rm -rf dist"}`)
	if rule == nil {
		t.Fatal("期望匹配到规则")
	}
	if rule.Source != SourceUserSettings {
		t.Errorf("posix 模式 user deny 应胜 local deny（同行为下来源优先）, got source=%s", rule.Source)
	}
}

// TestPosixProjectOverridesLocal 验证 posix 模式 project 胜 local（对齐 POSIX ACL group:: > other::）。
func TestPosixProjectOverridesLocal(t *testing.T) {
	rs := NewRuleSet()
	rs.SetPriorityMode(PriorityPosix)

	rs.Add(PermissionRule{ToolName: "Bash", Behavior: RuleAllow, Source: SourceLocalSettings})
	rs.Add(PermissionRule{ToolName: "Bash", Behavior: RuleAsk, Source: SourceProjectSettings})

	rule := rs.ResolveRuleDecision("Bash", `{"command":"ls"}`)
	if rule == nil {
		t.Fatal("期望匹配到规则")
	}
	if rule.Behavior != RuleAsk || rule.Source != SourceProjectSettings {
		t.Errorf("posix 模式 project ask 应胜 local allow, got behavior=%s source=%s", rule.Behavior, rule.Source)
	}
}

// TestPosixCLIStillHighest 验证 posix 模式 cliArg 仍为最高（runtime override 保留，REF-CLAUDE）。
// P1 决策后：deny 绝对优先凌驾于来源，故改用同行为类型（deny vs deny）验证来源优先级。
func TestPosixCLIStillHighest(t *testing.T) {
	rs := NewRuleSet()
	rs.SetPriorityMode(PriorityPosix)

	rs.Add(PermissionRule{ToolName: "Bash", Behavior: RuleDeny, Source: SourceUserSettings})
	rs.Add(PermissionRule{ToolName: "Bash", Behavior: RuleDeny, Source: SourceCLIArg})

	rule := rs.ResolveRuleDecision("Bash", `{"command":"rm -rf dist"}`)
	if rule == nil {
		t.Fatal("期望匹配到规则")
	}
	if rule.Source != SourceCLIArg {
		t.Errorf("posix 模式 cliArg deny 应胜 user deny（同行为下来源优先）, got source=%s", rule.Source)
	}
}

// TestPosixSessionStillHigh 验证 posix 模式 session 仍高于 user（runtime override 保留）。
// P1 决策后：deny 绝对优先凌驾于来源，故改用同行为类型（deny vs deny）验证来源优先级。
func TestPosixSessionStillHigh(t *testing.T) {
	rs := NewRuleSet()
	rs.SetPriorityMode(PriorityPosix)

	rs.Add(PermissionRule{ToolName: "Bash", Behavior: RuleDeny, Source: SourceUserSettings})
	rs.Add(PermissionRule{ToolName: "Bash", Behavior: RuleDeny, Source: SourceSession})

	rule := rs.ResolveRuleDecision("Bash", `{"command":"rm -rf dist"}`)
	if rule == nil {
		t.Fatal("期望匹配到规则")
	}
	if rule.Source != SourceSession {
		t.Errorf("posix 模式 session deny 应胜 user deny（同行为下来源优先）, got source=%s", rule.Source)
	}
}

// TestLegacyUnchanged 验证 legacy 模式来源优先级（同行为类型）。
// P1 决策后：deny 绝对优先凌驾于来源，故改用同行为类型（allow vs allow）验证 legacy 来源优先级。
func TestLegacyUnchanged(t *testing.T) {
	rs := NewRuleSet()
	// 显式设置 legacy 模式（默认已改为 posix，需显式指定才能测试 legacy 行为）
	rs.SetPriorityMode(PriorityLegacy)

	rs.Add(PermissionRule{ToolName: "Bash", Behavior: RuleAllow, Source: SourceUserSettings})
	rs.Add(PermissionRule{ToolName: "Bash", Behavior: RuleAllow, Source: SourceCLIArg})

	rule := rs.ResolveRuleDecision("Bash", `{"command":"echo hello"}`)
	if rule == nil {
		t.Fatal("期望匹配到规则")
	}
	// legacy 模式：cliArg(5) > userSettings(1)，cliArg 胜出
	if rule.Source != SourceCLIArg {
		t.Errorf("legacy 模式 cliArg allow 应胜 user allow（同行为下来源优先）, got source=%s", rule.Source)
	}
}

// TestSetPriorityModeRuntimeSwitch 验证运行时切换 mode 后（同行为类型下）来源优先级变化。
// P1 决策后：deny 绝对优先，故改用同行为类型（allow vs allow）验证 legacy→posix 切换的来源优先级。
func TestSetPriorityModeRuntimeSwitch(t *testing.T) {
	rs := NewRuleSet()
	// 显式设置 legacy 模式（默认已改为 posix，需显式指定才能测试 legacy→posix 切换）
	rs.SetPriorityMode(PriorityLegacy)
	rs.Add(PermissionRule{ToolName: "Bash", Behavior: RuleAllow, Source: SourceLocalSettings})
	rs.Add(PermissionRule{ToolName: "Bash", Behavior: RuleAllow, Source: SourceUserSettings})

	// legacy 模式：local(3) > user(1)，local 胜出
	rule := rs.ResolveRuleDecision("Bash", `{"command":"echo hello"}`)
	if rule == nil {
		t.Fatal("期望匹配到规则")
	}
	if rule.Source != SourceLocalSettings {
		t.Errorf("legacy 模式 local allow 应胜 user allow, got source=%s", rule.Source)
	}

	// 切换到 posix 模式：user(3) > local(1)，user 胜出
	rs.SetPriorityMode(PriorityPosix)
	rule = rs.ResolveRuleDecision("Bash", `{"command":"echo hello"}`)
	if rule == nil {
		t.Fatal("期望匹配到规则")
	}
	if rule.Source != SourceUserSettings {
		t.Errorf("posix 模式 user allow 应胜 local allow, got source=%s", rule.Source)
	}
}

// TestSetPriorityModeInvalidFallback 验证非法 mode 值回填 posix（fail-closed，与默认值一致）。
func TestSetPriorityModeInvalidFallback(t *testing.T) {
	rs := NewRuleSet()
	rs.SetPriorityMode(PriorityMode("invalid"))
	if rs.PriorityMode() != PriorityPosix {
		t.Errorf("非法 mode 应回填 posix (与默认值一致), got %s", rs.PriorityMode())
	}
}

// TestLegacyPriorityMapBackwardCompat 验证 sourcePriority 别名仍指向 legacy（向后兼容）。
// 旧代码（bug_test.go）直接引用 sourcePriority[SourceCLIArg] 等，须保持工作。
func TestLegacyPriorityMapBackwardCompat(t *testing.T) {
	if sourcePriority[SourceCLIArg] != sourcePriorityLegacy[SourceCLIArg] {
		t.Errorf("sourcePriority 别名应等于 sourcePriorityLegacy")
	}
	if sourcePriority[SourceUserSettings] != 1 {
		t.Errorf("legacy 模式 userSettings 优先级应为 1, got %d", sourcePriority[SourceUserSettings])
	}
}

// TestPosixPriorityMapValues 验证 posix 优先级映射值正确。
func TestPosixPriorityMapValues(t *testing.T) {
	if sourcePriorityPosix[SourceLocalSettings] != 1 {
		t.Errorf("posix 模式 localSettings 应为 1, got %d", sourcePriorityPosix[SourceLocalSettings])
	}
	if sourcePriorityPosix[SourceProjectSettings] != 2 {
		t.Errorf("posix 模式 projectSettings 应为 2, got %d", sourcePriorityPosix[SourceProjectSettings])
	}
	if sourcePriorityPosix[SourceUserSettings] != 3 {
		t.Errorf("posix 模式 userSettings 应为 3, got %d", sourcePriorityPosix[SourceUserSettings])
	}
	if sourcePriorityPosix[SourceSession] != 4 {
		t.Errorf("posix 模式 session 应为 4, got %d", sourcePriorityPosix[SourceSession])
	}
	if sourcePriorityPosix[SourceCLIArg] != 5 {
		t.Errorf("posix 模式 cliArg 应为 5, got %d", sourcePriorityPosix[SourceCLIArg])
	}
}

// TestPolicySetPriorityMode 验证 Policy 透传 SetPriorityMode。
func TestPolicySetPriorityMode(t *testing.T) {
	p := NewPolicy(LevelWorkspaceWrite)
	if p.PriorityMode() != PriorityPosix {
		t.Errorf("Policy 默认模式应为 posix (与 NewRuleSet 一致), got %s", p.PriorityMode())
	}

	p.SetPriorityMode(PriorityLegacy)
	if p.PriorityMode() != PriorityLegacy {
		t.Errorf("Policy 设置 legacy 后应为 legacy, got %s", p.PriorityMode())
	}
}

// TestDefaultPriority_UserOverridesProjectOverridesDefault 反例测试（P1 修复核心验证）。
// 验证默认模式（posix）下完整优先级链：user > project > local(default)。
// P1 决策后：deny 绝对优先，故改用同行为类型（allow vs allow）验证默认优先级链。
// 对标 POSIX ACL（@https://pubs.opengroup.org/onlinepubs/9699919799/ 访问 2026-07-21）：
// user:: > group:: > other:: 特异性递减原则。
func TestDefaultPriority_UserOverridesProjectOverridesDefault(t *testing.T) {
	rs := NewRuleSet()
	// 不显式设置 priorityMode，使用默认值（修复后为 posix）

	// local(default) allow → project allow → user allow（同行为，测来源优先级）
	rs.Add(PermissionRule{ToolName: "Bash", Behavior: RuleAllow, Source: SourceLocalSettings})
	rs.Add(PermissionRule{ToolName: "Bash", Behavior: RuleAllow, Source: SourceProjectSettings})
	rs.Add(PermissionRule{ToolName: "Bash", Behavior: RuleAllow, Source: SourceUserSettings})

	rule := rs.ResolveRuleDecision("Bash", `{"command":"echo hello"}`)
	if rule == nil {
		t.Fatal("期望匹配到规则")
	}
	if rule.Source != SourceUserSettings {
		t.Errorf("默认 posix 模式 user allow 应胜 project allow + local allow\n"+
			"  期望: source=userSettings (user > project > local)\n"+
			"  实际: source=%s", rule.Source)
	}
}

// TestDefaultPriority_ProjectOverridesDefault 反例测试（P1 修复辅助验证）。
// 验证默认模式（posix）下 project 覆盖 local(default)。
func TestDefaultPriority_ProjectOverridesDefault(t *testing.T) {
	rs := NewRuleSet()
	// 不显式设置 priorityMode，使用默认值（修复后为 posix）

	rs.Add(PermissionRule{ToolName: "Bash", Behavior: RuleAllow, Source: SourceLocalSettings})
	rs.Add(PermissionRule{ToolName: "Bash", Behavior: RuleDeny, Source: SourceProjectSettings})

	rule := rs.ResolveRuleDecision("Bash", `{"command":"ls"}`)
	if rule == nil {
		t.Fatal("期望匹配到规则")
	}
	if rule.Behavior != RuleDeny || rule.Source != SourceProjectSettings {
		t.Errorf("默认 posix 模式 project deny 应胜 local allow\n"+
			"  期望: behavior=deny, source=projectSettings (project > local)\n"+
			"  实际: behavior=%s, source=%s", rule.Behavior, rule.Source)
	}
}

// TestDefaultPriority_UserDenyOverridesProjectAllow 反例测试（P1 修复辅助验证）。
// 验证默认模式（posix）下 user deny 覆盖 project allow（安全优先：deny 应能被 user 强制）。
func TestDefaultPriority_UserDenyOverridesProjectAllow(t *testing.T) {
	rs := NewRuleSet()
	// 不显式设置 priorityMode，使用默认值（修复后为 posix）

	rs.Add(PermissionRule{ToolName: "Bash", Behavior: RuleAllow, Source: SourceProjectSettings})
	rs.Add(PermissionRule{ToolName: "Bash", Behavior: RuleDeny, Source: SourceUserSettings})

	rule := rs.ResolveRuleDecision("Bash", `{"command":"ls"}`)
	if rule == nil {
		t.Fatal("期望匹配到规则")
	}
	if rule.Behavior != RuleDeny || rule.Source != SourceUserSettings {
		t.Errorf("默认 posix 模式 user deny 应胜 project allow\n"+
			"  期望: behavior=deny, source=userSettings (user > project)\n"+
			"  实际: behavior=%s, source=%s", rule.Behavior, rule.Source)
	}
}
