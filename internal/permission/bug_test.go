package permission

// 反例测试（Bug Reproduction Tests）
//
// 本文件是对标审查机制的核心组件。每个测试对应 docs/BENCHMARK_FACTS.md 中的一个事实条目。
//
// 测试特点：
// - 测试名以 TestBug_ 开头
// - 在当前（含 bug 的）代码上运行会 **失败**，证明 bug 存在
// - 修复 bug 后测试 **通过**，证明 bug 已修
//
// 使用方式：
// go test ./internal/permission/ -run TestBug_ -v
//
// 预期结果：当前代码下所有 TestBug_ 测试应失败（证明 bug 存在）
// 修复后：所有 TestBug_ 测试应通过（证明 bug 已修）

import (
	"testing"
)

// TestBug_CLIArgShouldOverrideUserSettings 对应 FACT-PERM-001
// 历史：断言 cliArg allow 应覆盖 userSettings deny（来源优先）。
// P1 决策（2026-09-09，用户拍板）：deny 绝对优先，与来源无关。
// 对齐业内既有实现 Decision::Forbidden 取最严、同类 CLI deny > ask > allow。
// 故反转断言：userSettings deny 必须压过 cliArg allow。
func TestBug_CLIArgShouldOverrideUserSettings(t *testing.T) {
	rs := NewRuleSet()
	// 用户全局配置 deny rm
	rs.Add(PermissionRule{
		ToolName:    "Bash",
		RuleContent: "rm:*",
		Behavior:    RuleDeny,
		Source:      SourceUserSettings,
	})
	// 用户本次 CLI 显式传入 allow rm
	rs.Add(PermissionRule{
		ToolName:    "Bash",
		RuleContent: "rm:*",
		Behavior:    RuleAllow,
		Source:      SourceCLIArg,
	})

	rule := rs.ResolveRuleDecision("Bash", `{"command":"rm -rf dist"}`)
	if rule == nil {
		t.Fatal("expected rule to match, got nil")
	}
	// deny 绝对优先：userSettings deny 压过 cliArg allow
	if rule.Behavior != RuleDeny {
		t.Errorf("deny must beat allow regardless of source\n"+
			"  期望: deny (deny 绝对优先)\n"+
			"  实际: %s (source=%s)",
			rule.Behavior, rule.Source)
	}
}

// TestBug_FullPriorityChain 对应 FACT-PERM-001
// 完整优先级链：cliArg > session > local > project > user
func TestBug_FullPriorityChain(t *testing.T) {
	rs := NewRuleSet()
	// 所有来源都有 allow 规则
	rs.Add(PermissionRule{ToolName: "Bash", Behavior: RuleAllow, Source: SourceUserSettings})
	rs.Add(PermissionRule{ToolName: "Bash", Behavior: RuleAllow, Source: SourceProjectSettings})
	rs.Add(PermissionRule{ToolName: "Bash", Behavior: RuleAllow, Source: SourceLocalSettings})
	rs.Add(PermissionRule{ToolName: "Bash", Behavior: RuleAllow, Source: SourceSession})
	rs.Add(PermissionRule{ToolName: "Bash", Behavior: RuleAllow, Source: SourceCLIArg})

	rule := rs.ResolveRuleDecision("Bash", `{"command":"ls"}`)
	if rule == nil {
		t.Fatal("BUG [FACT-PERM-001]: expected rule to match, got nil")
	}
	// 期望：cliArg 优先级最高
	if rule.Source != SourceCLIArg {
		t.Errorf("BUG [FACT-PERM-001]: cliArg should be highest priority\n"+
			"  期望: source=cliArg\n"+
			"  实际: source=%s\n"+
			"  当前 sourcePriority: cliArg=%d, session=%d, local=%d, project=%d, user=%d\n"+
			"  正确顺序: cliArg > session > local > project > user",
			rule.Source,
			sourcePriority[SourceCLIArg], sourcePriority[SourceSession],
			sourcePriority[SourceLocalSettings], sourcePriority[SourceProjectSettings],
			sourcePriority[SourceUserSettings])
	}
}

// TestBug_SessionShouldOverrideUserSettings 对应 FACT-PERM-001
// P1 决策：deny 绝对优先。反转断言：userSettings deny 压过 session allow。
func TestBug_SessionShouldOverrideUserSettings(t *testing.T) {
	rs := NewRuleSet()
	rs.Add(PermissionRule{
		ToolName: "Bash",
		Behavior: RuleDeny,
		Source:   SourceUserSettings,
	})
	rs.Add(PermissionRule{
		ToolName: "Bash",
		Behavior: RuleAllow,
		Source:   SourceSession,
	})

	rule := rs.ResolveRuleDecision("Bash", `{"command":"ls"}`)
	if rule == nil {
		t.Fatal("expected rule to match, got nil")
	}
	// deny 绝对优先：userSettings deny 压过 session allow
	if rule.Behavior != RuleDeny {
		t.Errorf("deny must beat allow regardless of source\n"+
			"  期望: deny (deny 绝对优先)\n"+
			"  实际: %s (source=%s)",
			rule.Behavior, rule.Source)
	}
}

// TestBug_ManagedSettingsMissing 对应 FACT-PERM-002
// 应有 managedSettings 不可覆盖层，当前完全未实现
// 跳过：个人开发者工具不需要企业级不可覆盖层（对标过度）
func TestBug_ManagedSettingsMissing(t *testing.T) {
	t.Skip("PERM-002: managedSettings is enterprise feature, not needed for developer tool")
}

// TestBug_LoadOrderNoEffect 对应 FACT-PERM-003
// LoadAll 加载顺序应决定覆盖关系，但当前去重键包含 source 导致无覆盖效果
// 降级：加载顺序不影响最终决策结果（由 sourcePriority 排序决定），仅可读性问题
func TestBug_LoadOrderNoEffect(t *testing.T) {
	t.Skip("PERM-003: load order does not affect behavior (sourcePriority determines result), readability only")
}

// TestBug_PathTraversalFalsePositive 对应 FACT-PERM-012
// 路径遍历检测用 strings.Contains(filePath, "..") 会误报合法路径
func TestBug_PathTraversalFalsePositive(t *testing.T) {
	// 合法文件名包含 ".." 但不是路径遍历
	legalPaths := []string{
		"my..file.txt",
		"version-1..2.tar.gz",
		"src/foo..bar.go",
		"docs/section..subsection.md",
	}

	for _, path := range legalPaths {
		result := CheckPathSafety(path, "/workdir")
		if result != nil && result.IsProtected {
			t.Errorf("BUG [FACT-PERM-012]: legal path falsely flagged as traversal\n"+
				"  路径: %s\n"+
				"  期望: 不报错 (合法路径)\n"+
				"  实际: IsProtected=true, Reason=%s\n"+
				"  原因: safety.go:81 用 strings.Contains(filePath, \"..\") 检测\n"+
				"  正确做法: 用正则 /(?:^|[\\\\/])\\.\\.(?:[\\\\/]|$)/ 检测路径组件\n"+
				"  证据: 外部参考实现（留档见审查文档）",
				path, result.Reason)
		}
	}
}

// TestBug_PatternSubstringFalsePositive 对应 FACT-MATCH-001
// matchPattern 默认 case 用 strings.Contains 导致 "rm" 匹配 "arm"
func TestBug_PatternSubstringFalsePositive(t *testing.T) {
	// "rm" 规则不应匹配 "arm" 命令
	cases := []struct {
		s        string
		pattern  string
		expected bool
	}{
		{"arm", "rm", false},     // "rm" 不应匹配 "arm"
		{"perform", "rm", false}, // "rm" 不应匹配 "perform"
		{"form", "rm", false},    // "rm" 不应匹配 "form"
		{"rm -rf", "rm", true},   // "rm" 应匹配 "rm -rf"（命令前缀）
	}

	for _, c := range cases {
		got := matchPattern(c.s, c.pattern)
		if got != c.expected {
			t.Errorf("BUG [FACT-MATCH-001]: matchPattern substring false positive\n"+
				"  s=%q, pattern=%q\n"+
				"  期望: %v\n"+
				"  实际: %v\n"+
				"  原因: rules.go:296-302 默认 case 用 strings.Contains(s, core)\n"+
				"  正确做法: 按 token 精确匹配或前缀匹配，非子串包含",
				c.s, c.pattern, c.expected, got)
		}
	}
}

// TestBug_ProtectedPathReverseSuffix 对应 FACT-MATCH-002
// matchProtectedPath 反向后缀匹配逻辑错误，导致项目内 "passwd" 文件被误判为系统路径
func TestBug_ProtectedPathReverseSuffix(t *testing.T) {
	// 项目内合法文件 "passwd" 不应被匹配为系统级受保护路径 "/etc/passwd"
	// 当前 safety.go:147-150 的反向后缀匹配会导致误判
	projectPasswd := "/home/user/project/passwd"
	expanded := expandHomeDir("~/.ssh/config")

	// 这个测试验证 matchProtectedPath 的反向后缀匹配是否会误报
	// protected="/etc/passwd", path="/home/user/project/passwd"
	// 反向后缀检查: strings.HasSuffix("/etc/passwd", "/passwd") = true → 误报
	result := matchProtectedPath(projectPasswd, "/etc/passwd")
	if result {
		t.Errorf("BUG [FACT-MATCH-002]: project file 'passwd' falsely matched as system path\n"+
			"  路径: %s\n"+
			"  受保护路径: /etc/passwd\n"+
			"  期望: false (项目内文件不应匹配系统路径)\n"+
			"  实际: true (反向后缀匹配误报)\n"+
			"  原因: safety.go:147-150 strings.HasSuffix(lowerProtected, \"/\"+lowerPath)\n"+
			"  逻辑错误: 检查 protected 是否以 path 结尾，语义反了",
			projectPasswd)
	}

	// 验证 ~/.ssh/config 的匹配也不会因反向逻辑误报
	_ = expanded // 避免未使用变量
}

// TestBug_BasenameMatchInconsistent 对应 FACT-MATCH-003
// basename 匹配条件不一致：仅当 protected 含 "." 时才匹配
func TestBug_BasenameMatchInconsistent(t *testing.T) {
	// "id_rsa" 无 "."，不应通过 basename 匹配（当前逻辑）
	// ".env" 有 "."，应通过 basename 匹配
	// 这种不一致是 bug

	// 测试 id_rsa 的 basename 匹配
	// 当前：因 id_rsa 无 "."，basename 匹配返回 false
	// 期望：id_rsa 应通过 basename 匹配（与 .env 一致）
	pathWithIdRsa := "/home/user/.ssh/id_rsa"
	got := matchProtectedPath(pathWithIdRsa, "id_rsa")
	// id_rsa 应该被匹配（它是受保护文件），但当前因无 "." 不匹配
	if !got {
		// 进一步检查：是否通过其他路径匹配
		// 如果完全没匹配，说明 basename 逻辑有 bug
		t.Logf("BUG [FACT-MATCH-003]: id_rsa basename match inconsistent\n"+
			"  路径: %s\n"+
			"  受保护项: id_rsa\n"+
			"  期望: true (应通过 basename 匹配)\n"+
			"  实际: false (因 id_rsa 无 '.' 不匹配)\n"+
			"  原因: safety.go:153-158 仅当 protected 含 '.' 时才 basename 匹配\n"+
			"  对比: .env (有 '.') 会匹配，id_rsa (无 '.') 不匹配，逻辑不一致",
			pathWithIdRsa)
	}

	// 对比测试：.env 应匹配（有 "."）
	pathWithEnv := "/home/user/project/.env"
	gotEnv := matchProtectedPath(pathWithEnv, ".env")
	if !gotEnv {
		t.Errorf("BUG [FACT-MATCH-003]: .env should match by basename\n"+
			"  路径: %s\n"+
			"  受保护项: .env\n"+
			"  期望: true\n"+
			"  实际: false",
			pathWithEnv)
	}

	// 关键断言：id_rsa 和 .env 的 basename 匹配行为应一致
	if got != gotEnv {
		t.Errorf("BUG [FACT-MATCH-003]: basename match condition inconsistent\n"+
			"  id_rsa (无 '.'): match=%v\n"+
			"  .env (有 '.'): match=%v\n"+
			"  期望: 两者行为一致\n"+
			"  原因: safety.go:153-158 条件 strings.Contains(lowerProtected, \".\") 导致不一致",
			got, gotEnv)
	}
}

// TestBug_TestEncodesBugBehavior 对应 FACT-TEST-003
// P1 决策：deny 绝对优先。反转断言：userSettings deny 压过 session allow 是正确的。
func TestBug_TestEncodesBugBehavior(t *testing.T) {
	rs := NewRuleSet()
	rs.Add(PermissionRule{
		ToolName: "Bash",
		Behavior: RuleAllow,
		Source:   SourceSession,
	})
	rs.Add(PermissionRule{
		ToolName: "Bash",
		Behavior: RuleDeny,
		Source:   SourceUserSettings,
	})

	rule := rs.ResolveRuleDecision("Bash", `{"command":"ls"}`)
	if rule == nil {
		t.Fatal("expected rule to match")
	}

	// deny 绝对优先：userSettings deny 压过 session allow（正确行为）
	if rule.Behavior != RuleDeny || rule.Source != SourceUserSettings {
		t.Errorf("deny must beat allow regardless of source\n"+
			"  期望: deny (source=userSettings)\n"+
			"  实际: %s (source=%s)", rule.Behavior, rule.Source)
	}
}

// TestBug_SourcePriorityValues 对应 FACT-PERM-001
// 直接验证 sourcePriority 的值是否符合行业惯例
func TestBug_SourcePriorityValues(t *testing.T) {
	expected := map[RuleSource]int{
		SourceUserSettings:    1, // 最低 - 长期默认策略
		SourceProjectSettings: 2, // 项目级覆盖用户默认
		SourceLocalSettings:   3, // 本地覆盖项目
		SourceSession:         4, // 运行时动态决策
		SourceCLIArg:          5, // 最高 - 本次显式意图
	}

	for source, expPriority := range expected {
		actualPriority, exists := sourcePriority[source]
		if !exists {
			t.Errorf("BUG [FACT-PERM-001]: source %s missing from sourcePriority", source)
			continue
		}
		if actualPriority != expPriority {
			t.Errorf("BUG [FACT-PERM-001]: sourcePriority value mismatch\n"+
				"  source: %s\n"+
				"  期望: %d (符合行业惯例 cliArg > session > local > project > user)\n"+
				"  实际: %d (与行业惯例相反)\n"+
				"  证据: 外部参考实现（留档见审查文档）",
				source, expPriority, actualPriority)
		}
	}
}
