// Package command — Prompt Skill 命令unit test
package command

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/anchorandfish/whale-code/internal/skill"
)

// setupSkillRegistry 创建带 OnSkillExecute 回调的 Registry，用于测试 skill 命令
func setupSkillRegistry(t *testing.T) *Registry {
	t.Helper()

	// 创建临时技能目录和技能文件
	dir := t.TempDir()

	// 创建 skillify 技能文件
	skillifyContent := "---\nname: skillify\ndescription: 从当前会话提取可复用技能并保存\n---\n技能提取模板\nUserrequirement：{{prompt}}"
	os.WriteFile(filepath.Join(dir, "skillify.md"), []byte(skillifyContent), 0644)

	// 创建 simplify 技能文件
	simplifyContent := "---\nname: simplify\ndescription: 三维度并行code review\n---\ncode review模板\n审查重点：{{prompt}}"
	os.WriteFile(filepath.Join(dir, "simplify.md"), []byte(simplifyContent), 0644)

	// 创建 verify 技能文件
	verifyContent := "---\nname: verify\ndescription: 验证代码变更是否正确工作\n---\n代码验证模板\n验证重点：{{prompt}}"
	os.WriteFile(filepath.Join(dir, "verify.md"), []byte(verifyContent), 0644)

	// 创建 batch 技能文件
	batchContent := "---\nname: batch\ndescription: 大规模并行变更\n---\n批量变更模板\n变更目标：{{prompt}}"
	os.WriteFile(filepath.Join(dir, "batch.md"), []byte(batchContent), 0644)

	// 创建技能管理器并加载技能
	mgr := skill.NewManager(skill.SourceDir{Path: dir, Source: "project"})
	if err := mgr.LoadAll(); err != nil {
		t.Fatalf("加载技能失败: %v", err)
	}

	// 创建 Registry 并设置回调
	r := NewRegistry()
	r.OnSkillExecute = func(skillName, args string) string {
		result, err := mgr.Execute(skillName, args)
		if err != nil {
			return ""
		}
		return result
	}

	return r
}

// TestSkillifyCommand_Registered 测试 /skillify 命令是否已注册
func TestSkillifyCommand_Registered(t *testing.T) {
	r := NewRegistry()
	cmd, ok := r.Get("skillify")
	if !ok {
		t.Fatal("/skillify 命令应在注册表中存在")
	}
	if cmd.Name != "skillify" {
		t.Errorf("Name = %q, expected \"skillify\"", cmd.Name)
	}
	if cmd.Category != "Automation" {
		t.Errorf("Category = %q, expected \"Automation\"", cmd.Category)
	}
	if cmd.Description == "" {
		t.Error("Description should not be empty")
	}
}

// TestSimplifyCommand_Registered 测试 /simplify 命令是否已注册
func TestSimplifyCommand_Registered(t *testing.T) {
	r := NewRegistry()
	cmd, ok := r.Get("simplify")
	if !ok {
		t.Fatal("/simplify command should exist in registry")
	}
	if cmd.Name != "simplify" {
		t.Errorf("Name = %q, expected \"simplify\"", cmd.Name)
	}
	if cmd.Category != "Automation" {
		t.Errorf("Category = %q, expected \"Automation\"", cmd.Category)
	}
}

// TestVerifyCommand_Registered 测试 /verify 命令是否已注册
func TestVerifyCommand_Registered(t *testing.T) {
	r := NewRegistry()
	cmd, ok := r.Get("verify")
	if !ok {
		t.Fatal("/verify command should exist in registry")
	}
	if cmd.Name != "verify" {
		t.Errorf("Name = %q, expected \"verify\"", cmd.Name)
	}
	if cmd.Category != "Automation" {
		t.Errorf("Category = %q, expected \"Automation\"", cmd.Category)
	}
}

// TestBatchCommand_Registered 测试 /batch 命令是否已注册
func TestBatchCommand_Registered(t *testing.T) {
	r := NewRegistry()
	cmd, ok := r.Get("batch")
	if !ok {
		t.Fatal("/batch command should exist in registry")
	}
	if cmd.Name != "batch" {
		t.Errorf("Name = %q, expected \"batch\"", cmd.Name)
	}
	if cmd.Category != "Automation" {
		t.Errorf("Category = %q, expected \"Automation\"", cmd.Category)
	}
}

// TestSkillifyCommand_WithCallback 测试 /skillify 命令带回调时的执行
func TestSkillifyCommand_WithCallback(t *testing.T) {
	r := setupSkillRegistry(t)

	cmd, ok := r.Get("skillify")
	if !ok {
		t.Fatal("/skillify 命令应在注册表中存在")
	}

	output, err := cmd.Handler("PR workflow")
	if err != nil {
		t.Fatalf("skillify 命令执行失败: %v", err)
	}
	if !strings.Contains(output, "技能提取模板") {
		t.Errorf("expected输出包含技能模板内容，actual: %q", output)
	}
	if !strings.Contains(output, "PR workflow") {
		t.Errorf("expected输出包含User参数 'PR workflow'，actual: %q", output)
	}
}

// TestSimplifyCommand_WithCallback 测试 /simplify 命令带回调时的执行
func TestSimplifyCommand_WithCallback(t *testing.T) {
	r := setupSkillRegistry(t)

	cmd, ok := r.Get("simplify")
	if !ok {
		t.Fatal("/simplify 命令应在注册表中存在")
	}

	output, err := cmd.Handler("错误处理")
	if err != nil {
		t.Fatalf("simplify 命令执行失败: %v", err)
	}
	if !strings.Contains(output, "code review模板") {
		t.Errorf("expected输出包含审查模板内容，actual: %q", output)
	}
	if !strings.Contains(output, "错误处理") {
		t.Errorf("expected输出包含User参数 '错误处理'，actual: %q", output)
	}
}

// TestVerifyCommand_WithCallback 测试 /verify 命令带回调时的执行
func TestVerifyCommand_WithCallback(t *testing.T) {
	r := setupSkillRegistry(t)

	cmd, ok := r.Get("verify")
	if !ok {
		t.Fatal("/verify 命令应在注册表中存在")
	}

	output, err := cmd.Handler("API interface")
	if err != nil {
		t.Fatalf("verify 命令执行失败: %v", err)
	}
	if !strings.Contains(output, "代码验证模板") {
		t.Errorf("expected输出包含验证模板内容，actual: %q", output)
	}
	if !strings.Contains(output, "API interface") {
		t.Errorf("expected输出包含User参数 'API interface'，actual: %q", output)
	}
}

// TestBatchCommand_WithCallback 测试 /batch 命令带回调时的执行
func TestBatchCommand_WithCallback(t *testing.T) {
	r := setupSkillRegistry(t)

	cmd, ok := r.Get("batch")
	if !ok {
		t.Fatal("/batch 命令应在注册表中存在")
	}

	output, err := cmd.Handler("重命名所有文件")
	if err != nil {
		t.Fatalf("batch 命令执行失败: %v", err)
	}
	if !strings.Contains(output, "批量变更模板") {
		t.Errorf("expected输出包含批量变更模板内容，actual: %q", output)
	}
	if !strings.Contains(output, "重命名所有文件") {
		t.Errorf("expected输出包含User参数 '重命名所有文件'，actual: %q", output)
	}
}

// TestSkillCommand_WithoutCallback 测试 skill 命令无回调时返回降级消息
func TestSkillCommand_WithoutCallback(t *testing.T) {
	r := NewRegistry() // 无 OnSkillExecute 回调

	skillCommands := []string{"skillify", "simplify", "verify", "batch"}
	for _, name := range skillCommands {
		cmd, ok := r.Get(name)
		if !ok {
			t.Fatalf("/%s 命令应在注册表中存在", name)
		}
		output, err := cmd.Handler("test")
		if err != nil {
			t.Fatalf("/%s 命令执行失败: %v", name, err)
		}
		if !strings.Contains(output, "not initialized") {
			t.Errorf("/%s without callback should return not initialized message, actual: %q", name, output)
		}
	}
}

// TestSkillCommand_EmptyArgs 测试 skill 命令无参数时的执行
func TestSkillCommand_EmptyArgs(t *testing.T) {
	r := setupSkillRegistry(t)

	skillCommands := []string{"skillify", "simplify", "verify", "batch"}
	for _, name := range skillCommands {
		cmd, ok := r.Get(name)
		if !ok {
			t.Fatalf("/%s 命令应在注册表中存在", name)
		}
		output, err := cmd.Handler("")
		if err != nil {
			t.Fatalf("/%s 命令无参数执行失败: %v", name, err)
		}
		if output == "" {
			t.Errorf("/%s 无参数时should return模板内容", name)
		}
	}
}

// TestSkillCommand_TemplateReplacement 测试模板中 {{prompt}} 占位符替换
func TestSkillCommand_TemplateReplacement(t *testing.T) {
	r := setupSkillRegistry(t)

	cmd, ok := r.Get("skillify")
	if !ok {
		t.Fatal("/skillify 命令应在注册表中存在")
	}

	// 有参数时，{{prompt}} 应被替换
	output, err := cmd.Handler("我的requirement")
	if err != nil {
		t.Fatalf("skillify 命令执行失败: %v", err)
	}
	if strings.Contains(output, "{{prompt}}") {
		t.Errorf("{{prompt}} 占位符应被替换，actual输出: %q", output)
	}
	if !strings.Contains(output, "我的requirement") {
		t.Errorf("输出should containUser参数 '我的requirement'，actual: %q", output)
	}
}

// TestSkillCommand_Parse 测试斜杠命令解析
func TestSkillCommand_Parse(t *testing.T) {
	tests := []struct {
		input    string
		wantName string
		wantArgs string
	}{
		{"/skillify", "skillify", ""},
		{"/skillify PR workflow", "skillify", "PR workflow"},
		{"/simplify 错误处理", "simplify", "错误处理"},
		{"/verify API interface", "verify", "API interface"},
		{"/batch 重命名文件", "batch", "重命名文件"},
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

// TestSkillCommands_IncrementCount 测试新增 skill 命令后总数增加
func TestSkillCommands_IncrementCount(t *testing.T) {
	r := NewRegistry()
	count := len(r.List())
	if count < 84 {
		t.Errorf("新增 4 个 skill 命令后总数应 >= 84，actual: %d", count)
	}
}

// TestSkillCommands_AllHaveHandlers 测试所有 skill 命令都有 Handler
func TestSkillCommands_AllHaveHandlers(t *testing.T) {
	r := NewRegistry()
	skillCommands := []string{"skillify", "simplify", "verify", "batch"}
	for _, name := range skillCommands {
		cmd, ok := r.Get(name)
		if !ok {
			t.Fatalf("/%s 命令应在注册表中存在", name)
		}
		if cmd.Handler == nil {
			t.Errorf("/%s 命令的 Handler 不should be nil", name)
		}
	}
}

// TestSkillCommands_AllHaveUsage 测试所有 skill 命令都有 Usage 说明
func TestSkillCommands_AllHaveUsage(t *testing.T) {
	r := NewRegistry()
	skillCommands := []string{"skillify", "simplify", "verify", "batch"}
	for _, name := range skillCommands {
		cmd, ok := r.Get(name)
		if !ok {
			t.Fatalf("/%s 命令应在注册表中存在", name)
		}
		if cmd.Usage == "" {
			t.Errorf("/%s 命令应有 Usage 说明", name)
		}
	}
}

// TestOnSkillExecuteCallback 测试 OnSkillExecute 回调字段存在
func TestOnSkillExecuteCallback(t *testing.T) {
	r := NewRegistry()
	// 初始时回调should be nil
	if r.OnSkillExecute != nil {
		t.Error("新建 Registry 的 OnSkillExecute should be nil")
	}

	// 设置回调后应可用
	called := false
	r.OnSkillExecute = func(skillName, args string) string {
		called = true
		return "rendered prompt"
	}

	result := r.OnSkillExecute("skillify", "test")
	if !called {
		t.Error("OnSkillExecute 回调应被调用")
	}
	if result != "rendered prompt" {
		t.Errorf("OnSkillExecute 返回值 = %q, expected \"rendered prompt\"", result)
	}
}

// TestSkillCommands_FormatHelp 测试帮助信息中包含 skill 命令（V0.1-HIDDEN：默认隐藏，--all 可见）
func TestSkillCommands_FormatHelp(t *testing.T) {
	r := NewRegistry()
	help := FormatHelp(r, "")
	skillCommands := []string{"skillify", "simplify", "verify", "batch"}
	for _, name := range skillCommands {
		if strings.Contains(help, name) {
			t.Errorf("隐藏命令 /%s 不应出现在默认帮助信息", name)
		}
	}
	if strings.Contains(help, "Automation") {
		t.Error("隐藏命令的 Automation 分类不应出现在默认帮助信息")
	}
	all := FormatHelp(r, "--all")
	for _, name := range skillCommands {
		if !strings.Contains(all, name) {
			t.Errorf("帮助信息 --all should contain /%s 命令", name)
		}
	}
	if !strings.Contains(all, "Automation") {
		t.Error("help --all should contain \"Automation\" category")
	}
}
