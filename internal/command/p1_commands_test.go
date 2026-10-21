package command

import (
	"testing"
)

// TestP1CommandsRegistered 验证所有 P1 命令已在 Registry 中正确注册
func TestP1CommandsRegistered(t *testing.T) {
	r := NewRegistry()

	p1Commands := []struct {
		name        string
		description string
		category    string
	}{
		{"plan", "Toggle plan mode (read-only)", "Core"},
		{"privacy", "Show/set privacy options", "Core"},
		{"stats", "Show detailed statistics", "Core"},
		{"summary", "Generate conversation summary", "Core"},
		{"thinkback", "Review thinking process", "Core"},
		{"usage", "Show token usage details", "Core"},
		{"break-cache", "Force break prompt cache", "Core"},
		{"onboarding", "Re-run onboarding", "Core"},
		{"tasks", "Show background task list", "Core"},
		{"context", "Show current context info", "Core"},
	}

	for _, cmd := range p1Commands {
		t.Run(cmd.name, func(t *testing.T) {
			c, ok := r.Get(cmd.name)
			if !ok {
				t.Fatalf("命令 /%s 未在 Registry 中注册", cmd.name)
			}
			if c.Name != cmd.name {
				t.Errorf("Name = %q, want %q", c.Name, cmd.name)
			}
			if c.Description == "" {
				t.Errorf("命令 /%s 的 Description 为空", cmd.name)
			}
			if c.Category != cmd.category {
				t.Errorf("Category = %q, want %q", c.Category, cmd.category)
			}
			if c.Handler == nil {
				t.Errorf("命令 /%s 的 Handler 为 nil", cmd.name)
			}

			output, err := c.Handler("")
			if err != nil {
				t.Errorf("Handler(\"\") 返回错误: %v", err)
			}
			if output == "" {
				t.Error("Handler(\"\") 返回空字符串")
			}
		})
	}
}

// TestP1CommandExecution 测试各 P1 命令的 Handler 执行
func TestP1CommandExecution(t *testing.T) {
	r := NewRegistry()

	tests := []struct {
		name        string
		args        string
		wantContain string
	}{
		{"plan", "on", "enabled"},
		{"plan", "off", "disabled"},
		{"privacy", "", "Privacy"},
		{"privacy", "analytics-on", "analytics-on"},
		{"stats", "", "statistics"},
		{"summary", "", "Summary"},
		{"thinkback", "", "thinking"},
		{"usage", "", "Usage tracking"},
		{"break-cache", "", "Cache"},
		{"onboarding", "", "Welcome"},
		{"tasks", "", "not initialized"},
		{"context", "", "Context"},
	}

	for _, tt := range tests {
		t.Run(tt.name+"_"+tt.args, func(t *testing.T) {
			cmd, ok := r.Get(tt.name)
			if !ok {
				t.Fatalf("命令 %s 未注册", tt.name)
			}
			output, err := cmd.Handler(tt.args)
			if err != nil {
				t.Fatalf("执行失败: %v", err)
			}
			if output == "" {
				t.Fatal("返回空字符串")
			}
			if tt.wantContain != "" && !containsString(output, tt.wantContain) {
				t.Errorf("输出不包含 %q\nactual输出: %q", tt.wantContain, output)
			}
			t.Logf("/%s %s → %q", tt.name, tt.args, truncateStr(output, 100))
		})
	}
}

// TestPlanCommandArgs 测试 /plan 命令各种参数
func TestPlanCommandArgs(t *testing.T) {
	r := NewRegistry()
	cmd, _ := r.Get("plan")

	tests := []struct {
		args string
		want string
	}{
		{"", "Plan mode"},
		{"on", "enabled"},
		{"off", "disabled"},
	}

	for _, tt := range tests {
		t.Run(tt.args, func(t *testing.T) {
			output, _ := cmd.Handler(tt.args)
			if !containsString(output, tt.want) {
				t.Errorf("/plan %s → 不包含 %q, got: %s", tt.args, tt.want, truncateStr(output, 80))
			}
		})
	}
}

// TestBreakCacheCommandArgs 测试 /break-cache 命令（占位 Handler 层）
func TestBreakCacheCommandArgs(t *testing.T) {
	r := NewRegistry()
	cmd, _ := r.Get("break-cache")

	output, _ := cmd.Handler("")
	if !containsString(output, "Cache") {
		t.Error("/break-cache should return Cache related content")
	}

	output, _ = cmd.Handler("status")
	if output == "" {
		t.Error("/break-cache status should return non-empty")
	}
}

// truncateStr 截断字符串用于日志显示
func truncateStr(s string, maxLen int) string {
	if len(s) <= maxLen {
		return s
	}
	return s[:maxLen] + "...(截断)"
}
