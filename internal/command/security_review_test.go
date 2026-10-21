package command

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/anchorandfish/whale-code/internal/skill"
)

// TestSecurityReview_Registered 验证命令在 Registry 中正确注册
func TestSecurityReview_Registered(t *testing.T) {
	r := NewRegistry()
	cmd, ok := r.Get("security-review")
	if !ok {
		t.Fatal("security-review command not found in registry")
	}
	if cmd.Name != "security-review" {
		t.Errorf("Name = %q, want %q", cmd.Name, "security-review")
	}
	if cmd.Category != "Security" {
		t.Errorf("Category = %q, want %q", cmd.Category, "Security")
	}
	if cmd.Handler == nil {
		t.Fatal("Handler is nil")
	}
	if cmd.Description == "" {
		t.Error("Description is empty")
	}
	if cmd.Usage == "" {
		t.Error("Usage is empty")
	}
}

// TestSecurityReview_InHelp 验证帮助信息显示（V0.1-HIDDEN：默认隐藏，--all 可见）
func TestSecurityReview_InHelp(t *testing.T) {
	r := NewRegistry()
	help := FormatHelp(r, "")
	if strings.Contains(help, "security-review") {
		t.Error("hidden security-review should not appear in default help")
	}
	all := FormatHelp(r, "--all")
	if !strings.Contains(all, "Security") {
		t.Error("Help --all output should contain 'Security' category")
	}
	if !strings.Contains(all, "security-review") {
		t.Error("Help --all output should contain 'security-review' command")
	}
}

// TestSecurityReview_Handler_DefaultDiff 默认 diff 模式
func TestSecurityReview_Handler_DefaultDiff(t *testing.T) {
	r := NewRegistry()
	cmd, _ := r.Get("security-review")
	result, err := cmd.Handler("")
	if err != nil {
		t.Fatalf("Handler returned error: %v", err)
	}
	if result == "" {
		t.Fatal("Handler returned empty string")
	}
	if !strings.Contains(result, "Security review") {
		t.Errorf("Expected 'Security review' in result, got: %s", result)
	}
}

// TestSecurityReview_Handler_Full full 模式
func TestSecurityReview_Handler_Full(t *testing.T) {
	r := NewRegistry()
	cmd, _ := r.Get("security-review")
	result, err := cmd.Handler("full")
	if err != nil {
		t.Fatalf("Handler returned error: %v", err)
	}
	if result == "" {
		t.Fatal("Handler returned empty string")
	}
	// B7-P1-2 修复：桩层返回诚实降级提示（含小写 "full"），不再返回假成功的 "Full project scan..."
	if !strings.Contains(result, "full") {
		t.Errorf("Expected 'full' in result, got: %s", result)
	}
}

// TestSecurityReview_Handler_File file 模式
func TestSecurityReview_Handler_File(t *testing.T) {
	r := NewRegistry()
	cmd, _ := r.Get("security-review")
	result, err := cmd.Handler("main.go")
	if err != nil {
		t.Fatalf("Handler returned error: %v", err)
	}
	if result == "" {
		t.Fatal("Handler returned empty string")
	}
	if !strings.Contains(result, "main.go") {
		t.Errorf("Expected 'main.go' in result, got: %s", result)
	}
}

// TestSecurityReview_Handler_Staged staged 模式
func TestSecurityReview_Handler_Staged(t *testing.T) {
	r := NewRegistry()
	cmd, _ := r.Get("security-review")
	result, err := cmd.Handler("staged")
	if err != nil {
		t.Fatalf("Handler returned error: %v", err)
	}
	if result == "" {
		t.Fatal("Handler returned empty string")
	}
	// B7-P1-2 修复：桩层返回诚实降级提示（含小写 "staged"），不再返回假成功的 "Staged changes scan..."
	if !strings.Contains(result, "staged") {
		t.Errorf("Expected 'staged' in result, got: %s", result)
	}
}

// TestSecurityReview_Handler_NoError 所有模式都不返回错误
func TestSecurityReview_Handler_NoError(t *testing.T) {
	r := NewRegistry()
	cmd, _ := r.Get("security-review")

	testCases := []string{"", "full", "main.go", "staged", "src/pkg/util.go"}
	for _, args := range testCases {
		t.Run("args="+args, func(t *testing.T) {
			_, err := cmd.Handler(args)
			if err != nil {
				t.Errorf("Handler(%q) returned unexpected error: %v", args, err)
			}
		})
	}
}

// TestSecurityReview_Parse 验证命令解析
func TestSecurityReview_Parse(t *testing.T) {
	tests := []struct {
		input    string
		wantName string
		wantArgs string
	}{
		{"/security-review", "security-review", ""},
		{"/security-review full", "security-review", "full"},
		{"/security-review main.go", "security-review", "main.go"},
		{"/security-review staged", "security-review", "staged"},
		{"/security-review src/pkg/util.go", "security-review", "src/pkg/util.go"},
	}

	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			name, args := Parse(tt.input)
			if name != tt.wantName {
				t.Errorf("Parse(%q) name = %q, want %q", tt.input, name, tt.wantName)
			}
			if args != tt.wantArgs {
				t.Errorf("Parse(%q) args = %q, want %q", tt.input, args, tt.wantArgs)
			}
		})
	}
}

// TestSecurityReview_Suggest 验证模糊建议（V0.1-HIDDEN：Did-you-mean 不指路隐藏命令）
func TestSecurityReview_Suggest(t *testing.T) {
	r := NewRegistry()
	for _, s := range Suggest("sec", r) {
		if s == "security-review" {
			t.Fatalf("hidden 'security-review' should not be suggested for 'sec', got %v", Suggest("sec", r))
		}
	}
	// 同包直改注册表取消隐藏后，建议恢复（隐藏只影响可见性）
	cmd := r.commands["security-review"]
	cmd.Hidden = false
	r.commands["security-review"] = cmd
	found := false
	for _, s := range Suggest("sec", r) {
		if s == "security-review" {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("Expected 'security-review' in suggestions for 'sec' after unhide, got %v", Suggest("sec", r))
	}
}

// TestSecurityReview_Completion 验证补全
func TestSecurityReview_Completion(t *testing.T) {
	r := NewRegistry()
	// V0.1-HIDDEN：隐藏命令不进补全候选
	matches := r.Completion("security")
	for _, m := range matches {
		if m == "security-review" {
			t.Fatalf("hidden 'security-review' should not be in completions, got %v", matches)
		}
	}
	// 同包直改注册表取消隐藏后，补全恢复（隐藏只影响可见性）
	cmd := r.commands["security-review"]
	cmd.Hidden = false
	r.commands["security-review"] = cmd
	found := false
	for _, m := range r.Completion("security") {
		if m == "security-review" {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("Expected 'security-review' in completions for 'security' after unhide, got %v", r.Completion("security"))
	}
}

// TestSecurityReview_SkillFileExists 验证技能文件存在且可加载
func TestSecurityReview_SkillFileExists(t *testing.T) {
	skillDir := filepath.Join("..", "..", ".whale", "skills")
	skillFile := filepath.Join(skillDir, "security-review.md")

	if _, err := os.Stat(skillFile); os.IsNotExist(err) {
		t.Skipf("Skill file not found: %s (optional for unit tests)", skillFile)
	}

	data, err := os.ReadFile(skillFile)
	if err != nil {
		t.Fatalf("Failed to read skill file: %v", err)
	}
	if len(data) < 500 {
		t.Errorf("Skill template too short: %d bytes (expected >= 500)", len(data))
	}
}

// TestSecurityReview_SkillContent 验证技能模板内容完整
func TestSecurityReview_SkillContent(t *testing.T) {
	skillDir := filepath.Join("..", "..", ".whale", "skills")
	skillFile := filepath.Join(skillDir, "security-review.md")

	if _, err := os.Stat(skillFile); os.IsNotExist(err) {
		t.Skipf("Skill file not found: %s", skillFile)
	}

	data, err := os.ReadFile(skillFile)
	if err != nil {
		t.Fatalf("Failed to read skill file: %v", err)
	}

	content := string(data)

	requiredSections := []string{
		"安全",
		"审查",
		"注入",
		"报告",
		"误报",
		"严重级别",
	}

	for _, section := range requiredSections {
		if !strings.Contains(content, section) {
			t.Errorf("Skill template missing required section: %q", section)
		}
	}
}

// TestSecurityReview_SkillMgrLoad 验证 SkillMgr 加载
func TestSecurityReview_SkillMgrLoad(t *testing.T) {
	skillDir := filepath.Join("..", "..", ".whale", "skills")

	if _, err := os.Stat(skillDir); os.IsNotExist(err) {
		t.Skipf("Skill directory not found: %s", skillDir)
	}

	mgr := skill.NewManager(skill.SourceDir{Path: skillDir, Source: "project"})
	if err := mgr.LoadAll(); err != nil {
		t.Fatalf("Failed to load skills: %v", err)
	}

	s, ok := mgr.Get("security-review")
	if !ok {
		t.Fatal("security-review skill not found in manager")
	}

	if s.Name != "security-review" {
		t.Errorf("Skill Name = %q, want 'security-review'", s.Name)
	}
	if s.Prompt == "" {
		t.Error("Skill Prompt is empty")
	}
}

// TestSecurityReview_SkillRender 验证技能模板渲染
func TestSecurityReview_SkillRender(t *testing.T) {
	skillDir := filepath.Join("..", "..", ".whale", "skills")

	if _, err := os.Stat(skillDir); os.IsNotExist(err) {
		t.Skipf("Skill directory not found: %s", skillDir)
	}

	mgr := skill.NewManager(skill.SourceDir{Path: skillDir, Source: "project"})
	mgr.LoadAll()

	_, ok := mgr.Get("security-review")
	if !ok {
		t.Skip("security-review skill not found")
	}

	tests := []struct {
		name     string
		args     string
		contains string
	}{
		{"diff", "审查当前 diff", "审查当前 diff"},
		{"full", "审查整个项目", "审查整个项目"},
		{"file", "审查文件 main.go", "审查文件 main.go"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result, err := mgr.Execute("security-review", tt.args)
			if err != nil {
				t.Fatalf("Execute failed: %v", err)
			}
			if !strings.Contains(result, tt.contains) {
				t.Errorf("Rendered prompt should contain %q, got: %s",
					tt.contains, truncateStr(result, 200))
			}
		})
	}
}
