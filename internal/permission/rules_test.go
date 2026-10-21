package permission

import (
	"strings"
	"testing"
)

// TestParseRuleString 规则解析测试
func TestParseRuleString(t *testing.T) {
	tests := []struct {
		name        string
		input       string
		behavior    RuleBehavior
		wantTool    string
		wantContent string
		wantNegate  bool
		wantErr     bool
	}{
		{"simple tool", "Bash", RuleAllow, "Bash", "", false, false},
		{"with content", "Bash(curl:*)", RuleDeny, "Bash", "curl:*", false, false},
		{"with space", "Bash(npm publish:*)", RuleAsk, "Bash", "npm publish:*", false, false},
		{"negate tool", "!(Bash)", RuleAllow, "Bash", "", true, false},
		{"negate with content", "!(Bash(curl:*))", RuleDeny, "Bash", "curl:*", true, false},
		{"empty", "", RuleAllow, "", "", false, true},
		{"unclosed paren", "Bash(", RuleAllow, "", "", false, true},
		{"nested paren", "Bash(())", RuleAllow, "", "", false, true},
		{"whitespace trim", "  Bash  ", RuleAllow, "Bash", "", false, false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rule, err := ParseRuleString(tt.input, tt.behavior, SourceSession)
			if tt.wantErr {
				if err == nil {
					t.Errorf("expected error, got nil")
				}
				return
			}
			if err != nil {
				t.Errorf("unexpected error: %v", err)
				return
			}
			if rule.ToolName != tt.wantTool {
				t.Errorf("ToolName = %q, want %q", rule.ToolName, tt.wantTool)
			}
			if rule.RuleContent != tt.wantContent {
				t.Errorf("RuleContent = %q, want %q", rule.RuleContent, tt.wantContent)
			}
			if rule.Negate != tt.wantNegate {
				t.Errorf("Negate = %v, want %v", rule.Negate, tt.wantNegate)
			}
			if rule.Behavior != tt.behavior {
				t.Errorf("Behavior = %q, want %q", rule.Behavior, tt.behavior)
			}
		})
	}
}

// TestRuleMatchToolLevel 工具级别规则匹配
func TestRuleMatchToolLevel(t *testing.T) {
	tests := []struct {
		name     string
		ruleName string
		toolName string
		expect   bool
	}{
		{"exact match", "Bash", "Bash", true},
		{"case insensitive", "Bash", "bash", true},
		{"no match", "Bash", "write_file", false},
		{"mcp prefix", "mcp__server1", "mcp__server1__tool1", true},
		{"mcp wrong server", "mcp__server1", "mcp__server2__tool1", false},
		{"negate match", "!(Bash)", "read_file", true},
		{"negate no match", "!(Bash)", "Bash", false},
		{"Edit(*) write_file", "Edit", "write_file", true},
		{"Edit(*) edit_file", "Edit", "edit_file", true},
		{"Edit(*) NotebookEdit", "Edit", "notebookedit", true},
		{"Edit(*) read_file", "Edit", "read_file", false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rule := &PermissionRule{
				ToolName:    tt.ruleName,
				RuleContent: "",
				Behavior:    RuleAllow,
				Source:      SourceSession,
			}
			if tt.ruleName == "Edit" {
				rule.RuleContent = "*"
			}
			if tt.name[:6] == "negate" {
				rule.Negate = true
				if strings.Contains(tt.ruleName, "!(") {
					rule.ToolName = tt.ruleName[2 : len(tt.ruleName)-1]
				}
			}

			// matchesTool 需一并传入 input（能力别名的效果视图用）。
			// 本组都是**静态工具名**用例，不含命令载体 ⇒ 空串等价。
			got := rule.matchesTool(tt.toolName, "")
			if got != tt.expect {
				t.Errorf("matchesTool(%q) = %v, want %v", tt.toolName, got, tt.expect)
			}
		})
	}
}

// TestRuleMatchContentLevel 内容级别规则匹配
func TestRuleMatchContentLevel(t *testing.T) {
	cmd := func(command string) string {
		return `{"command":"` + command + `"}`
	}
	fileInput := func(path string) string {
		return `{"file_path":"` + path + `"}`
	}

	tests := []struct {
		name    string
		content string
		input   string
		expect  bool
	}{
		{"curl prefix match", "curl:*", cmd("curl http://example.com"), true},
		{"curl no match", "curl:*", cmd("wget http://example.com"), false},
		{"git commit match", "git commit:*", cmd("git commit -m test"), true},
		{"git all match", "git:*", cmd("git status"), true},
		{"npm publish match", "npm publish:*", cmd("npm publish"), true},
		{"npm no match", "npm publish:*", cmd("npm install"), false},
		{"wildcard all", "*", cmd("anything"), true},
		{"exact contains", "rm", cmd("rm -rf node_modules"), true},
		{"prefix star", "echo*", cmd("echo hello"), true},
		{"suffix star", "*hello", cmd("echo hello"), true},
		{"compound command match first", "echo:*", cmd("echo hello && npm install"), true},
		{"compound command match second", "npm:*", cmd("echo hello && npm install"), true},
		{"file path match", "*.go", fileInput("src/main.go"), true},
		{"file path no match", "*.js", fileInput("src/main.go"), false},
		{"empty command no match", "curl:*", cmd(""), false},
		{"raw input", "test", "not json at all", false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rule := &PermissionRule{
				ToolName:    "Bash",
				RuleContent: tt.content,
				Behavior:    RuleAllow,
				Source:      SourceSession,
			}
			if strings.Contains(tt.name, "file path") {
				rule.ToolName = "write_file"
			}

			got := rule.Match(rule.ToolName, tt.input)
			if got != tt.expect {
				t.Errorf("Match(%q) = %v, want %v", tt.input, got, tt.expect)
			}
		})
	}
}

// TestRuleConflictResolution 规则冲突解决
func TestRuleConflictResolution(t *testing.T) {
	rs := NewRuleSet()

	// add allow + deny for same tool
	rs.Add(PermissionRule{ToolName: "Bash", RuleContent: "curl:*", Behavior: RuleAllow, Source: SourceSession})
	rs.Add(PermissionRule{ToolName: "Bash", RuleContent: "curl:*", Behavior: RuleDeny, Source: SourceSession})

	rules := rs.GetMatchingRules("Bash", `{"command":"curl example.com"}`)
	if len(rules) < 1 {
		t.Fatal("expected at least 1 matching rule")
	}
	if rules[0].Behavior != RuleDeny {
		t.Errorf("first rule should be deny, got %s", rules[0].Behavior)
	}
}

// TestRuleConflictPriority 规则冲突优先级测试
// P1 决策：deny 绝对优先，与来源无关。
// userSettings deny 必须压过 session allow（对齐业内既有实现/同类 CLI）。
func TestRuleConflictPriority(t *testing.T) {
	rs := NewRuleSet()

	rs.Add(PermissionRule{ToolName: "Bash", RuleContent: "", Behavior: RuleDeny, Source: SourceUserSettings})
	rs.Add(PermissionRule{ToolName: "Bash", RuleContent: "", Behavior: RuleAllow, Source: SourceSession})

	rule := rs.ResolveRuleDecision("Bash", `{"command":"ls"}`)
	if rule == nil {
		t.Fatal("expected a rule decision")
	}
	if rule.Behavior != RuleDeny {
		t.Errorf("deny must beat allow regardless of source, got %s (source=%s)", rule.Behavior, rule.Source)
	}
}

// TestRuleSetCRUD 规则集增删改查
func TestRuleSetCRUD(t *testing.T) {
	rs := NewRuleSet()

	r1 := PermissionRule{ToolName: "Bash", RuleContent: "echo:*", Behavior: RuleAllow, Source: SourceSession}
	r2 := PermissionRule{ToolName: "Bash", RuleContent: "curl:*", Behavior: RuleDeny, Source: SourceSession}

	// Add
	rs.Add(r1)
	rs.Add(r2)
	if rs.Count() != 2 {
		t.Errorf("Count() = %d, want 2", rs.Count())
	}

	// Dedup
	rs.Add(r1)
	if rs.Count() != 2 {
		t.Errorf("dedup failed: Count() = %d, want 2", rs.Count())
	}

	// ListBySource
	sessionRules := rs.ListBySource(SourceSession)
	if len(sessionRules) != 2 {
		t.Errorf("ListBySource = %d, want 2", len(sessionRules))
	}

	// Remove
	removed := rs.Remove("Bash", "echo:*", RuleAllow, SourceSession)
	if removed != 1 {
		t.Errorf("Remove returned %d, want 1", removed)
	}
	if rs.Count() != 1 {
		t.Errorf("Count() = %d after remove, want 1", rs.Count())
	}

	// Remove non-existent
	removed = rs.Remove("NonExist", "", RuleAllow, SourceSession)
	if removed != 0 {
		t.Errorf("removing non-existent returned %d, want 0", removed)
	}

	// Clear
	rs.Clear()
	if rs.Count() != 0 {
		t.Errorf("Count() = %d after clear, want 0", rs.Count())
	}
}

// TestRuleAskBehavior 测试 ask 规则行为
func TestRuleAskBehavior(t *testing.T) {
	rs := NewRuleSet()

	rs.Add(PermissionRule{ToolName: "Bash", RuleContent: "npm publish:*", Behavior: RuleAsk, Source: SourceSession})

	rule := rs.ResolveRuleDecision("Bash", `{"command":"npm publish --dry-run"}`)
	if rule == nil {
		t.Fatal("expected a rule decision")
	}
	if rule.Behavior != RuleAsk {
		t.Errorf("expected ask behavior, got %s", rule.Behavior)
	}
}

// TestRuleNegateMatch 测试否定规则匹配
func TestRuleNegateMatch(t *testing.T) {
	rs := NewRuleSet()

	// !(Bash) allow = 所有工具除了 Bash 都 allow
	rs.Add(PermissionRule{ToolName: "Bash", RuleContent: "", Behavior: RuleAllow, Source: SourceSession, Negate: true})

	// write_file 应该匹配（被允许）
	rules := rs.GetMatchingRules("write_file", "{}")
	if len(rules) == 0 {
		t.Error("write_file should match !(Bash)")
	}

	// Bash 不应该匹配
	rules = rs.GetMatchingRules("Bash", "{}")
	if len(rules) > 0 {
		t.Error("Bash should not match !(Bash)")
	}
}

// TestRuleString 测试规则字符串表示
func TestRuleString(t *testing.T) {
	tests := []struct {
		rule     PermissionRule
		expected string
	}{
		{PermissionRule{ToolName: "Bash", Behavior: RuleAllow}, "Bash"},
		{PermissionRule{ToolName: "Bash", RuleContent: "curl:*", Behavior: RuleDeny}, "Bash(curl:*)"},
		{PermissionRule{ToolName: "Bash", Negate: true, Behavior: RuleAllow}, "!(Bash)"},
		{PermissionRule{ToolName: "Bash", RuleContent: "curl:*", Negate: true, Behavior: RuleDeny}, "!(Bash(curl:*))"},
	}

	for _, tt := range tests {
		t.Run(tt.expected, func(t *testing.T) {
			got := tt.rule.String()
			if got != tt.expected {
				t.Errorf("String() = %q, want %q", got, tt.expected)
			}
		})
	}
}

// TestMatchPattern 模式匹配
func TestMatchPattern(t *testing.T) {
	tests := []struct {
		s       string
		pattern string
		expect  bool
	}{
		// wildcard all
		{"anything", "*", true},
		{"", "*", true},
		// prefix match
		{"echo hello", "echo*", true},
		{"echo", "echo*", true},
		{"xecho", "echo*", false},
		// suffix match
		{"hello world", "*world", true},
		{"world", "*world", true},
		{"worldly", "*world", false},
		// contains
		{"the test is here", "*test*", true},
		{"testing", "*test*", true},
		{"no match", "*xyz*", false},
		// exact match or prefix match (no wildcard)
		{"rm -rf node_modules", "rm", true},
		{"rm", "rm", true},
		{"arm", "rm", false},
		{"perform", "rm", false},
		{"not here", "curl", false},
		// to match substring, use *curl*
		{"the curl command", "*curl*", true},
	}

	for _, tt := range tests {
		t.Run(tt.s+"_"+tt.pattern, func(t *testing.T) {
			got := matchPattern(tt.s, tt.pattern)
			if got != tt.expect {
				t.Errorf("matchPattern(%q, %q) = %v, want %v", tt.s, tt.pattern, got, tt.expect)
			}
		})
	}
}

// TestSplitCompoundCommand 复合命令拆分
func TestSplitCompoundCommand(t *testing.T) {
	tests := []struct {
		cmd    string
		expect []string
	}{
		{"echo hello", []string{"echo hello"}},
		{"echo hello && echo world", []string{"echo hello", "echo world"}},
		{"ls; pwd", []string{"ls", "pwd"}},
		{"echo 'hello && world'", []string{"echo 'hello && world'"}},
		{`echo "hello && world"`, []string{`echo "hello && world"`}},
		{"cmd1 && cmd2 && cmd3", []string{"cmd1", "cmd2", "cmd3"}},
		{"", nil},
	}

	for _, tt := range tests {
		t.Run(tt.cmd, func(t *testing.T) {
			got := splitCompoundCommand(tt.cmd)
			if len(got) != len(tt.expect) {
				t.Errorf("len = %d, want %d, got: %v", len(got), len(tt.expect), got)
				return
			}
			for i := range got {
				if got[i] != tt.expect[i] {
					t.Errorf("[%d] = %q, want %q", i, got[i], tt.expect[i])
				}
			}
		})
	}
}

// TestRuleSetReplace 测试origin规则替换
func TestRuleSetReplace(t *testing.T) {
	rs := NewRuleSet()

	rs.Add(PermissionRule{ToolName: "Bash", Behavior: RuleAllow, Source: SourceSession})
	rs.Add(PermissionRule{ToolName: "Bash", Behavior: RuleDeny, Source: SourceLocalSettings})

	newRules := []PermissionRule{
		{ToolName: "read_file", Behavior: RuleAllow},
		{ToolName: "grep_search", Behavior: RuleAllow},
	}

	rs.Replace(SourceLocalSettings, newRules)

	// session rule should remain
	if rs.Count() != 3 {
		t.Errorf("Count() = %d, want 3 (1 session + 2 local)", rs.Count())
	}

	// local rules should be replaced
	localRules := rs.ListBySource(SourceLocalSettings)
	if len(localRules) != 2 {
		t.Errorf("local settings rules = %d, want 2", len(localRules))
	}
}
