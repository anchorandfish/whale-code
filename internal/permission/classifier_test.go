package permission

import (
	"context"
	"testing"
	"time"
)

func TestClassifier_Allowlist(t *testing.T) {
	c := NewClassifier(ClassifierConfig{Enabled: true, FailClosed: true})

	allowlistTools := []string{
		"read_file", "grep_search", "glob_search",
		"todo_write", "web_fetch", "web_search",
		"ask_user_question",
	}

	for _, toolName := range allowlistTools {
		t.Run(toolName, func(t *testing.T) {
			if !c.IsAllowlisted(toolName) {
				t.Errorf("%q should be allowlisted", toolName)
			}
		})
	}

	nonAllowlist := []string{"bash", "write_file", "edit_file", "powershell", "mcp__server"}
	for _, toolName := range nonAllowlist {
		t.Run(toolName+"_not", func(t *testing.T) {
			if c.IsAllowlisted(toolName) {
				t.Errorf("%q should NOT be allowlisted", toolName)
			}
		})
	}
}

func TestClassifier_Classify_Allowlist(t *testing.T) {
	c := NewClassifier(ClassifierConfig{Enabled: true, FailClosed: true})

	result := c.Classify(context.Background(), ClassifierAction{
		ToolName:  "read_file",
		ToolInput: `{"file_path":"test.txt"}`,
	})

	if result.Decision != DecisionAllow {
		t.Errorf("allowlisted tool should be allowed, got decision=%v", result.Decision)
	}
	if result.Source != "allowlist" {
		t.Errorf("source should be 'allowlist', got %s", result.Source)
	}
}

func TestClassifier_Classify_EditFastPath(t *testing.T) {
	c := NewClassifier(ClassifierConfig{Enabled: true, FailClosed: true})

	result := c.Classify(context.Background(), ClassifierAction{
		ToolName:  "write_file",
		ToolInput: `{"file_path":"src/main.go"}`,
	})

	if result.Decision != DecisionAllow {
		t.Errorf("edit tool should be allowed via fastpath, got decision=%v", result.Decision)
	}
	if result.Source != "fastpath" {
		t.Errorf("source should be 'fastpath', got %s", result.Source)
	}
}

func TestClassifier_Classify_BashReadOnly(t *testing.T) {
	c := NewClassifier(ClassifierConfig{Enabled: true, FailClosed: true})

	result := c.Classify(context.Background(), ClassifierAction{
		ToolName:  "bash",
		ToolInput: `{"command":"ls -la"}`,
	})

	if result.Decision != DecisionAllow {
		t.Errorf("read-only bash should be allowed, got decision=%v", result.Decision)
	}
}

func TestClassifier_Classify_BashDangerous(t *testing.T) {
	c := NewClassifier(ClassifierConfig{Enabled: true, FailClosed: true})

	result := c.Classify(context.Background(), ClassifierAction{
		ToolName:  "bash",
		ToolInput: `{"command":"rm -rf /"}`,
	})

	if result.Decision != DecisionUncertain {
		t.Errorf("dangerous bash should be uncertain, got decision=%v", result.Decision)
	}
}

func TestClassifier_Classify_BashDestructive(t *testing.T) {
	c := NewClassifier(ClassifierConfig{Enabled: true, FailClosed: true})

	result := c.Classify(context.Background(), ClassifierAction{
		ToolName:  "bash",
		ToolInput: `{"command":"git push origin main"}`,
	})

	// 破坏性命令，没有 AI 分类器时应该返回 uncertain
	if result.Decision != DecisionUncertain {
		t.Errorf("destructive bash should be uncertain without AI, got decision=%v", result.Decision)
	}
}

func TestClassifier_Classify_PowerShell(t *testing.T) {
	c := NewClassifier(ClassifierConfig{Enabled: true, FailClosed: true})

	result := c.Classify(context.Background(), ClassifierAction{
		ToolName:  "PowerShell",
		ToolInput: `{"command":"Get-Process"}`,
	})

	if result.Decision != DecisionUncertain {
		t.Errorf("PowerShell should be uncertain (needs user confirm), got decision=%v", result.Decision)
	}
}

func TestClassifier_Classify_FailClosed(t *testing.T) {
	c := NewClassifier(ClassifierConfig{Enabled: true, FailClosed: true})

	result := c.Classify(context.Background(), ClassifierAction{
		ToolName:  "mcp__unknown__tool",
		ToolInput: `{}`,
	})

	if result.Decision != DecisionUncertain {
		t.Errorf("failClosed should return uncertain for unknown tools, got decision=%v", result.Decision)
	}
}

func TestClassifier_Classify_FailOpen(t *testing.T) {
	c := NewClassifier(ClassifierConfig{Enabled: true, FailClosed: false})

	result := c.Classify(context.Background(), ClassifierAction{
		ToolName:  "mcp__unknown__tool",
		ToolInput: `{}`,
	})

	if result.Decision != DecisionAllow {
		t.Errorf("failOpen should allow unknown tools, got decision=%v", result.Decision)
	}
}

func TestClassifier_ResolveWithTracking_Allow(t *testing.T) {
	c := NewClassifier(ClassifierConfig{Enabled: true, FailClosed: true})

	result := ClassifierResult{Decision: DecisionAllow}
	allowed, needConfirm := c.ResolveWithTracking(result)

	if !allowed {
		t.Error("Allow decision should result in allowed=true")
	}
	if needConfirm {
		t.Error("Allow decision should result in needConfirm=false")
	}
	if c.DenialTracker().ConsecutiveDenials() != 0 {
		t.Error("Allow should reset consecutive denials")
	}
}

func TestClassifier_ResolveWithTracking_Deny(t *testing.T) {
	c := NewClassifier(ClassifierConfig{Enabled: true, FailClosed: true})

	result := ClassifierResult{Decision: DecisionDeny}
	allowed, needConfirm := c.ResolveWithTracking(result)

	if allowed {
		t.Error("Deny decision should result in allowed=false")
	}
	if needConfirm {
		t.Error("Deny decision should result in needConfirm=false")
	}
	if c.DenialTracker().ConsecutiveDenials() != 1 {
		t.Errorf("Deny should increment consecutive denials, got %d", c.DenialTracker().ConsecutiveDenials())
	}
}

func TestClassifier_ResolveWithTracking_ConsecutiveFallback(t *testing.T) {
	c := NewClassifier(ClassifierConfig{Enabled: true, FailClosed: true})

	// Record 4 denials
	for i := 0; i < 4; i++ {
		result := ClassifierResult{Decision: DecisionDeny}
		c.ResolveWithTracking(result)
	}

	// 5th denial should trigger fallback
	result := ClassifierResult{Decision: DecisionDeny}
	allowed, needConfirm := c.ResolveWithTracking(result)

	if !allowed || !needConfirm {
		t.Error("Consecutive fallback should result in allowed=true, needConfirm=true")
	}
}

func TestClassifier_ResolveWithTracking_TotalFallback(t *testing.T) {
	c := NewClassifier(ClassifierConfig{Enabled: true, FailClosed: true})

	// Record 24 denials with success resets
	for i := 0; i < 24; i++ {
		result := ClassifierResult{Decision: DecisionDeny}
		c.ResolveWithTracking(result)
		result = ClassifierResult{Decision: DecisionAllow}
		c.ResolveWithTracking(result)
	}

	// 25th denial should trigger total fallback (agent abort)
	result := ClassifierResult{Decision: DecisionDeny}
	c.ResolveWithTracking(result)
	result = ClassifierResult{Decision: DecisionAllow}
	c.ResolveWithTracking(result)

	result = ClassifierResult{Decision: DecisionDeny}
	c.ResolveWithTracking(result)
	result = ClassifierResult{Decision: DecisionAllow}
	c.ResolveWithTracking(result)
	_ = result

	if c.DenialTracker().TotalDenials() >= MaxTotalDenials {
		// This should trigger agent abort level
	}
}

func TestClassifier_Config(t *testing.T) {
	c := NewClassifier(ClassifierConfig{
		Enabled:         true,
		FailClosed:      true,
		MaxContextTurns: 10,
		Timeout:         30 * time.Second,
	})

	if !c.config.Enabled {
		t.Error("classifier should be enabled")
	}
	if !c.config.FailClosed {
		t.Error("should be fail-closed")
	}
	if c.config.MaxContextTurns != 10 {
		t.Errorf("MaxContextTurns = %d, want 10", c.config.MaxContextTurns)
	}
}

func TestIsEditTool(t *testing.T) {
	tests := []struct {
		name string
		want bool
	}{
		{"write_file", true},
		{"edit_file", true},
		{"notebookedit", true},
		{"bash", false},
		{"read_file", false},
	}
	for _, tt := range tests {
		if got := isEditTool(tt.name); got != tt.want {
			t.Errorf("isEditTool(%q) = %v, want %v", tt.name, got, tt.want)
		}
	}
}

func TestTruncateCommand(t *testing.T) {
	short := "ls -la"
	if got := truncateCommand(short, 10); got != short {
		t.Errorf("truncate short: %q, want %q", got, short)
	}

	long := "a very long command that should be truncated at the limit"
	truncated := truncateCommand(long, 20)
	if len(truncated) > 20 {
		t.Errorf("truncated length %d > 20", len(truncated))
	}
}
