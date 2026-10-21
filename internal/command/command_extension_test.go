// Package command — 斜杠命令扩展unit test
package command

import (
	"strings"
	"testing"

	"github.com/anchorandfish/whale-code/internal/ai"
	"github.com/anchorandfish/whale-code/internal/config"
)

// TestRegistry_CommandCount 命令总数 >= 80
func TestRegistry_CommandCount(t *testing.T) {
	r := NewRegistry()
	count := len(r.List())
	if count < 80 {
		t.Errorf("Expected at least 80 commands, got %d", count)
	}
}

// TestRegistry_NoDuplicateNames 无重复命令名
func TestRegistry_NoDuplicateNames(t *testing.T) {
	r := NewRegistry()
	names := r.List()
	seen := make(map[string]bool)
	for _, name := range names {
		if seen[name] {
			t.Errorf("Duplicate command name: %s", name)
		}
		seen[name] = true
	}
}

// TestRegistry_AllCategories 所有分类都有命令
func TestRegistry_AllCategories(t *testing.T) {
	r := NewRegistry()
	cats := r.ByCategory()

	expectedCats := []string{"Core", "Model", "Session", "Permission", "Export", "Git", "Plugin"}
	for _, cat := range expectedCats {
		if _, ok := cats[cat]; !ok {
			t.Errorf("Missing category: %s", cat)
		}
	}
}

// TestCommand_CompactStrategy 压缩策略切换
func TestCommand_CompactStrategy(t *testing.T) {
	r := NewRegistry()
	cmd, ok := r.Get("compact-strategy")
	if !ok {
		t.Fatal("compact-strategy command not found")
	}

	result, err := cmd.Handler("balanced")
	if err != nil || !strings.Contains(result, "balanced") {
		t.Errorf("Expected balanced strategy, got: %s, err: %v", result, err)
	}

	result, _ = cmd.Handler("")
	if !strings.Contains(result, "Current") {
		t.Errorf("Expected current strategy info, got: %s", result)
	}

	result, _ = cmd.Handler("invalid")
	if !strings.Contains(result, "Invalid") {
		t.Errorf("Expected invalid message, got: %s", result)
	}
}

// TestCommand_Context 上下文信息
func TestCommand_Context(t *testing.T) {
	r := NewRegistry()
	cmd, ok := r.Get("context")
	if !ok {
		t.Fatal("context command not found")
	}
	result, _ := cmd.Handler("")
	if !strings.Contains(result, "Context") {
		t.Errorf("Expected context info, got: %s", result)
	}
}

// TestCommand_Plan 计划模式
func TestCommand_Plan(t *testing.T) {
	r := NewRegistry()
	cmd, ok := r.Get("plan")
	if !ok {
		t.Fatal("plan command not found")
	}

	result, _ := cmd.Handler("on")
	if !strings.Contains(result, "enabled") {
		t.Errorf("Expected plan on, got: %s", result)
	}

	result, _ = cmd.Handler("off")
	if !strings.Contains(result, "disabled") {
		t.Errorf("Expected plan off, got: %s", result)
	}
}

// TestCommand_Privacy 隐私选项
func TestCommand_Privacy(t *testing.T) {
	r := NewRegistry()
	cmd, ok := r.Get("privacy")
	if !ok {
		t.Fatal("privacy command not found")
	}
	result, _ := cmd.Handler("show")
	if !strings.Contains(result, "Privacy") {
		t.Errorf("Expected privacy info, got: %s", result)
	}
}

// TestCommand_Stats 详细统计
func TestCommand_Stats(t *testing.T) {
	r := NewRegistry()
	cmd, ok := r.Get("stats")
	if !ok {
		t.Fatal("stats command not found")
	}
	result, _ := cmd.Handler("")
	if !strings.Contains(result, "statistics") {
		t.Errorf("Expected stats, got: %s", result)
	}
}

// TestCommand_Summary 对话摘要
func TestCommand_Summary(t *testing.T) {
	r := NewRegistry()
	cmd, ok := r.Get("summary")
	if !ok {
		t.Fatal("summary command not found")
	}
	result, _ := cmd.Handler("")
	// B7-P1-1 修复：桩层未接线 OnSummary 时返回诚实降级提示（含 "Summary" 关键字），
	// 不再返回假成功的 "Generating summary..." 静态文本。
	if !strings.Contains(result, "Summary") {
		t.Errorf("Expected Summary fallback, got: %s", result)
	}
}

// TestCommand_Thinkback 思考回顾
func TestCommand_Thinkback(t *testing.T) {
	r := NewRegistry()
	cmd, ok := r.Get("thinkback")
	if !ok {
		t.Fatal("thinkback command not found")
	}
	result, _ := cmd.Handler("")
	if !strings.Contains(result, "thinking") {
		t.Errorf("Expected thinkback, got: %s", result)
	}
}

// TestCommand_Ultraplan 超级规划
func TestCommand_Ultraplan(t *testing.T) {
	r := NewRegistry()
	cmd, ok := r.Get("ultraplan")
	if !ok {
		t.Fatal("ultraplan command not found")
	}
	result, _ := cmd.Handler("")
	if !strings.Contains(result, "planning") {
		t.Errorf("Expected ultraplan, got: %s", result)
	}
}

// TestCommand_Usage token用量
func TestCommand_Usage(t *testing.T) {
	r := NewRegistry()
	cmd, ok := r.Get("usage")
	if !ok {
		t.Fatal("usage command not found")
	}
	result, _ := cmd.Handler("")
	// Without UsageTracker, should return "not available" message
	if !strings.Contains(result, "not available") {
		t.Errorf("Expected 'not available' when no tracker, got: %s", result)
	}
}

// TestCommand_Voice 语音模式
func TestCommand_Voice(t *testing.T) {
	r := NewRegistry()
	cmd, ok := r.Get("voice")
	if !ok {
		t.Fatal("voice command not found")
	}
	result, _ := cmd.Handler("on")
	if !strings.Contains(result, "enabled") {
		t.Errorf("Expected voice on, got: %s", result)
	}
}

// TestCommand_BreakCache 打破缓存
func TestCommand_BreakCache(t *testing.T) {
	r := NewRegistry()
	cmd, ok := r.Get("break-cache")
	if !ok {
		t.Fatal("break-cache command not found")
	}
	result, _ := cmd.Handler("")
	if !strings.Contains(result, "Cache") {
		t.Errorf("Expected cache break, got: %s", result)
	}
}

// TestCommand_Onboarding 新手引导
func TestCommand_Onboarding(t *testing.T) {
	r := NewRegistry()
	cmd, ok := r.Get("onboarding")
	if !ok {
		t.Fatal("onboarding command not found")
	}
	result, _ := cmd.Handler("")
	if !strings.Contains(result, "Welcome") {
		t.Errorf("Expected welcome, got: %s", result)
	}
}

// TestCommand_AddDir 添加工作目录
func TestCommand_AddDir(t *testing.T) {
	r := NewRegistry()
	cmd, ok := r.Get("add-dir")
	if !ok {
		t.Fatal("add-dir command not found")
	}
	result, _ := cmd.Handler("./src")
	if !strings.Contains(result, "./src") {
		t.Errorf("Expected dir added, got: %s", result)
	}
	result, _ = cmd.Handler("")
	if !strings.Contains(result, "Usage") {
		t.Errorf("Expected usage, got: %s", result)
	}
}

// TestCommand_Files 项目文件概览
func TestCommand_Files(t *testing.T) {
	r := NewRegistry()
	cmd, ok := r.Get("files")
	if !ok {
		t.Fatal("files command not found")
	}
	result, _ := cmd.Handler("")
	if !strings.Contains(result, "file") {
		t.Errorf("Expected files info, got: %s", result)
	}
}

// TestCommand_Insights 项目洞察
func TestCommand_Insights(t *testing.T) {
	r := NewRegistry()
	cmd, ok := r.Get("insights")
	if !ok {
		t.Fatal("insights command not found")
	}
	result, _ := cmd.Handler("")
	if !strings.Contains(result, "insight") {
		t.Errorf("Expected insights, got: %s", result)
	}
}

// TestCommand_Env environment variable
func TestCommand_Env(t *testing.T) {
	r := NewRegistry()
	cmd, ok := r.Get("env")
	if !ok {
		t.Fatal("env command not found")
	}
	result, _ := cmd.Handler("")
	if !strings.Contains(result, "Environment") {
		t.Errorf("Expected env info, got: %s", result)
	}
}

// TestCommand_OutputStyle 输出风格
func TestCommand_OutputStyle(t *testing.T) {
	r := NewRegistry()
	cmd, ok := r.Get("output-style")
	if !ok {
		t.Fatal("output-style command not found")
	}
	result, _ := cmd.Handler("concise")
	if !strings.Contains(result, "concise") {
		t.Errorf("Expected concise style, got: %s", result)
	}
}

// TestCommand_KeybindingsRemoved /vim 与 /keybindings 均已删除：
// 非 vim 用户无运行时切换键位的需求，默认（emacs）键位是唯一行为。
// 底层 keybindings 解析系统保留（服务默认键位与 keybindings.json 静态配置），
// 仅移除运行时切换命令入口。
func TestCommand_KeybindingsRemoved(t *testing.T) {
	r := NewRegistry()
	for _, name := range []string{"vim", "keybindings"} {
		if _, ok := r.Get(name); ok {
			t.Fatalf("%s command should be removed", name)
		}
	}
	// All() 中不应残留 keybindings（防止通过别名/其他注册路径复活）
	for _, c := range r.All() {
		if c.Name == "keybindings" || c.Name == "vim" {
			t.Errorf("command %q should be removed from registry", c.Name)
		}
	}
}

// TestCommand_Login 登录
func TestCommand_Login(t *testing.T) {
	r := NewRegistry()
	cmd, ok := r.Get("login")
	if !ok {
		t.Fatal("login command not found")
	}
	result, _ := cmd.Handler("")
	if !strings.Contains(result, "API Key") {
		t.Errorf("Expected login info, got: %s", result)
	}
}

// TestCommand_Logout 登出
func TestCommand_Logout(t *testing.T) {
	r := NewRegistry()
	cmd, ok := r.Get("logout")
	if !ok {
		t.Fatal("logout command not found")
	}
	result, _ := cmd.Handler("")
	if !strings.Contains(result, "Logged out") {
		t.Errorf("Expected logout, got: %s", result)
	}
}

// TestCommand_CommitPushPr 一键提交推送PR
func TestCommand_CommitPushPr(t *testing.T) {
	r := NewRegistry()
	cmd, ok := r.Get("commit-push-pr")
	if !ok {
		t.Fatal("commit-push-pr command not found")
	}
	result, _ := cmd.Handler("")
	if !strings.Contains(result, "COMMIT_PUSH_PR") {
		t.Errorf("Expected COMMIT_PUSH_PR signal, got: %s", result)
	}
}

// TestCommand_IssueCreate 创建Issue
func TestCommand_IssueCreate(t *testing.T) {
	r := NewRegistry()
	cmd, ok := r.Get("issue create")
	if !ok {
		t.Fatal("issue create command not found")
	}
	result, _ := cmd.Handler("bug: crash on startup")
	if !strings.Contains(result, "GitHub") {
		t.Errorf("Expected issue create, got: %s", result)
	}
}

// TestCommand_ExportFormat 格式导出
func TestCommand_ExportFormat(t *testing.T) {
	r := NewRegistry()
	cmd, ok := r.Get("export-format")
	if !ok {
		t.Fatal("export-format command not found")
	}
	result, _ := cmd.Handler("json output.json")
	if !strings.Contains(result, "json") {
		t.Errorf("Expected json export, got: %s", result)
	}
}

// TestCommand_Share 分享对话
func TestCommand_Share(t *testing.T) {
	r := NewRegistry()
	cmd, ok := r.Get("share")
	if !ok {
		t.Fatal("share command not found")
	}
	result, _ := cmd.Handler("")
	if !strings.Contains(result, "share") {
		t.Errorf("Expected share, got: %s", result)
	}
}

// TestCommand_SecurityReview 安全审查
func TestCommand_SecurityReview(t *testing.T) {
	r := NewRegistry()
	cmd, ok := r.Get("security-review")
	if !ok {
		t.Fatal("security-review command not found")
	}

	// 验证命令属性
	if cmd.Name != "security-review" {
		t.Errorf("Name = %q, want %q", cmd.Name, "security-review")
	}
	if cmd.Category != "Security" {
		t.Errorf("Category = %q, want %q", cmd.Category, "Security")
	}
	if cmd.Handler == nil {
		t.Fatal("Handler is nil")
	}
	if cmd.Usage == "" {
		t.Error("Usage is empty")
	}
	if cmd.Description == "" {
		t.Error("Description is empty")
	}

	// 测试各参数模式
	// B7-P1-2 修复：桩层不触发 AI 扫描，返回诚实降级提示（含对应模式关键字），
	// 不再返回假成功的 "Security review started — AI will scan..." 静态文本。
	tests := []struct {
		name     string
		args     string
		contains string
	}{
		{"default diff", "", "Security review"},
		{"full mode", "full", "full"},
		{"file mode", "main.go", "main.go"},
		{"staged mode", "staged", "staged"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result, err := cmd.Handler(tt.args)
			if err != nil {
				t.Errorf("Handler(%q) returned error: %v", tt.args, err)
			}
			if !strings.Contains(result, tt.contains) {
				t.Errorf("Handler(%q) = %q, want contains %q", tt.args, result, tt.contains)
			}
		})
	}
}

// TestCommand_Bughunter Bug猎手
func TestCommand_Bughunter(t *testing.T) {
	r := NewRegistry()
	cmd, ok := r.Get("bughunter start")
	if !ok {
		t.Fatal("bughunter start command not found")
	}
	result, _ := cmd.Handler("")
	if !strings.Contains(result, "Bug") {
		t.Errorf("Expected bughunter, got: %s", result)
	}
}

// TestCommand_Rename 重命名（别名）
func TestCommand_Rename(t *testing.T) {
	r := NewRegistry()
	cmd, ok := r.Get("rename")
	if !ok {
		t.Fatal("rename command not found")
	}
	result, _ := cmd.Handler("my-session")
	if !strings.Contains(result, "my-session") {
		t.Errorf("Expected rename, got: %s", result)
	}
}

// TestCommand_Tasks 后台任务
func TestCommand_Tasks(t *testing.T) {
	r := NewRegistry()
	cmd, ok := r.Get("tasks")
	if !ok {
		t.Fatal("tasks command not found")
	}
	result, _ := cmd.Handler("")
	if result == "" {
		t.Error("Expected tasks output")
	}
}

// TestCommand_ReloadPlugins 重新加载插件
func TestCommand_ReloadPlugins(t *testing.T) {
	r := NewRegistry()
	cmd, ok := r.Get("reload-plugins")
	if !ok {
		t.Fatal("reload-plugins command not found")
	}
	result, _ := cmd.Handler("")
	if !strings.Contains(result, "reload") {
		t.Errorf("Expected reload message, got: %s", result)
	}

	// 验证回调函数覆盖行为
	r.OnReloadPlugins = func() string { return "reloaded 3 plugins" }
	result, _ = cmd.Handler("")
	if result != "reloaded 3 plugins" {
		t.Errorf("Expected callback result, got: %s", result)
	}
}

// TestCommand_TerminalSetup 终端设置
func TestCommand_TerminalSetup(t *testing.T) {
	r := NewRegistry()
	cmd, ok := r.Get("terminal-setup")
	if !ok {
		t.Fatal("terminal-setup command not found")
	}
	result, _ := cmd.Handler("")
	if !strings.Contains(result, "Terminal") {
		t.Errorf("Expected terminal setup, got: %s", result)
	}
}

// TestParse_NewCompoundCommands 新复合命令解析
func TestParse_NewCompoundCommands(t *testing.T) {
	tests := []struct {
		input    string
		wantName string
		wantArgs string
	}{
		{"/issue create bug report", "issue create", "bug report"},
		{"/issue list", "issue list", ""},
		{"/issue view 42", "issue view", "42"},
		{"/bughunter start", "bughunter start", ""},
		{"/bughunter stop", "bughunter stop", ""},
		{"/bughunter status", "bughunter status", ""},
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

// TestSuggest_NewCommands 新命令的模糊建议
func TestSuggest_NewCommands(t *testing.T) {
	r := NewRegistry()
	suggestions := Suggest("compact", r)
	found := false
	for _, s := range suggestions {
		if strings.Contains(s, "compact") {
			found = true
		}
	}
	if !found {
		t.Errorf("Expected compact-related suggestions, got %v", suggestions)
	}
}

// TestIntegration_AllCommandsHaveHandlers 所有命令都有Handler
func TestIntegration_AllCommandsHaveHandlers(t *testing.T) {
	r := NewRegistry()
	for _, cmd := range r.All() {
		if cmd.Handler == nil {
			t.Errorf("Command %q has no handler", cmd.Name)
		}
	}
}

// TestIntegration_FormatHelp 帮助信息格式化
func TestIntegration_FormatHelp(t *testing.T) {
	r := NewRegistry()
	help := FormatHelp(r, "--all")
	if !strings.Contains(help, "Core") {
		t.Error("Help should contain Core category")
	}
	if !strings.Contains(help, "Workspace") {
		t.Error("Help should contain Workspace category")
	}
}

// TestIntegration_AliasCommands 别名命令工作
func TestIntegration_AliasCommands(t *testing.T) {
	r := NewRegistry()

	// rename 是 name 的别名
	renameCmd, ok := r.Get("rename")
	if !ok {
		t.Fatal("rename command not found")
	}
	result, _ := renameCmd.Handler("test")
	if !strings.Contains(result, "test") {
		t.Errorf("Expected rename to work, got: %s", result)
	}
}

// TestCommand_PluginInstall 插件安装命令注册
func TestCommand_PluginInstall(t *testing.T) {
	r := NewRegistry()
	cmd, ok := r.Get("plugin install")
	if !ok {
		t.Fatal("plugin install command not found")
	}
	if cmd.Handler == nil {
		t.Fatal("plugin install handler is nil")
	}
	if cmd.Usage != "/plugin install <url|path>" {
		t.Errorf("expected usage '/plugin install <url|path>', got %q", cmd.Usage)
	}

	// 无回调时返回占位
	result, _ := cmd.Handler("test")
	if !strings.Contains(result, "not initialized") {
		t.Errorf("Expected 'not initialized', got: %s", result)
	}

	r.OnPluginInstall = func(args string) string { return "installed " + args }
	result, _ = cmd.Handler("repo.git")
	if result != "installed repo.git" {
		t.Errorf("expected 'installed repo.git', got: %s", result)
	}
}

// TestCommand_PluginEnable 插件启用命令注册
func TestCommand_PluginEnable(t *testing.T) {
	r := NewRegistry()
	cmd, ok := r.Get("plugin enable")
	if !ok {
		t.Fatal("plugin enable command not found")
	}
	if cmd.Handler == nil {
		t.Fatal("plugin enable handler is nil")
	}
	if cmd.Usage != "/plugin enable <id>" {
		t.Errorf("expected usage '/plugin enable <id>', got %q", cmd.Usage)
	}

	result, _ := cmd.Handler("")
	if !strings.Contains(result, "not initialized") {
		t.Errorf("Expected 'not initialized', got: %s", result)
	}

	r.OnPluginEnable = func(args string) string { return "enabled " + args }
	result, _ = cmd.Handler("p1")
	if result != "enabled p1" {
		t.Errorf("expected 'enabled p1', got: %s", result)
	}
}

// TestCommand_PluginDisable 插件禁用命令注册
func TestCommand_PluginDisable(t *testing.T) {
	r := NewRegistry()
	cmd, ok := r.Get("plugin disable")
	if !ok {
		t.Fatal("plugin disable command not found")
	}
	if cmd.Handler == nil {
		t.Fatal("plugin disable handler is nil")
	}
	if cmd.Usage != "/plugin disable <id>" {
		t.Errorf("expected usage '/plugin disable <id>', got %q", cmd.Usage)
	}

	result, _ := cmd.Handler("")
	if !strings.Contains(result, "not initialized") {
		t.Errorf("Expected 'not initialized', got: %s", result)
	}

	r.OnPluginDisable = func(args string) string { return "disabled " + args }
	result, _ = cmd.Handler("p1")
	if result != "disabled p1" {
		t.Errorf("expected 'disabled p1', got: %s", result)
	}
}

// TestCommand_PluginList 插件列表命令
func TestCommand_PluginList(t *testing.T) {
	r := NewRegistry()
	cmd, ok := r.Get("plugin list")
	if !ok {
		t.Fatal("plugin list command not found")
	}
	if cmd.Handler == nil {
		t.Fatal("plugin list handler is nil")
	}

	r.OnPlugins = func() string { return "2 plugins" }
	result, _ := cmd.Handler("")
	if result != "2 plugins" {
		t.Errorf("expected '2 plugins', got: %s", result)
	}
}

// TestParse_PluginCommands 插件复合命令解析
func TestParse_PluginCommands(t *testing.T) {
	tests := []struct {
		input    string
		wantName string
		wantArgs string
	}{
		{"/plugin list", "plugin list", ""},
		{"/plugin install https://github.com/user/repo.git", "plugin install", "https://github.com/user/repo.git"},
		{"/plugin enable my-plugin", "plugin enable", "my-plugin"},
		{"/plugin disable my-plugin", "plugin disable", "my-plugin"},
		{"/reload-plugins", "reload-plugins", ""},
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

// TestCommand_PluginCategory 插件命令归属分类
func TestCommand_PluginCategory(t *testing.T) {
	r := NewRegistry()
	cmd, _ := r.Get("plugin install")
	if cmd.Category != "Plugin" {
		t.Errorf("expected category 'Plugin', got %q", cmd.Category)
	}

	cmd, _ = r.Get("reload-plugins")
	if cmd.Category != "Plugin" {
		t.Errorf("expected category 'Plugin', got %q", cmd.Category)
	}
}

// TestCommand_FastNoArgs /fast 无参数显示当前状态
func TestCommand_FastNoArgs(t *testing.T) {
	r := NewRegistry()
	cmd, ok := r.Get("fast")
	if !ok {
		t.Fatal("fast command not found")
	}
	if cmd.Stub {
		t.Error("fast command should not be stub")
	}
	result, err := cmd.Handler("")
	if err != nil {
		t.Errorf("fast no args returned error: %v", err)
	}
	if !strings.Contains(result, "快速模式") {
		t.Errorf("expected fast mode info, got: %s", result)
	}
}

// TestCommand_FastStatus /fast status 显示状态
func TestCommand_FastStatus(t *testing.T) {
	r := NewRegistry()
	r.OnGetFastModeState = func() (config.FastModeState, string, string) {
		return config.FastModeOn, "opus-4-fast", "claude-3.5-sonnet"
	}

	cmd, _ := r.Get("fast")
	result, err := cmd.Handler("status")
	if err != nil {
		t.Errorf("fast status returned error: %v", err)
	}
	if !strings.Contains(result, "已开启") {
		t.Errorf("expected '已开启', got: %s", result)
	}
	if !strings.Contains(result, "opus-4-fast") {
		t.Errorf("expected fast model name, got: %s", result)
	}
}

// TestCommand_FastOn /fast on 开启快速模式
func TestCommand_FastOn(t *testing.T) {
	r := NewRegistry()
	var enabled bool
	r.OnSetFastMode = func(en bool) error {
		enabled = en
		return nil
	}

	cmd, _ := r.Get("fast")
	result, err := cmd.Handler("on")
	if err != nil {
		t.Errorf("fast on returned error: %v", err)
	}
	if !enabled {
		t.Error("OnSetFastMode not called with true")
	}
	if !strings.Contains(result, "已开启") {
		t.Errorf("expected '已开启', got: %s", result)
	}
}

// TestCommand_FastOff /fast off 关闭快速模式
func TestCommand_FastOff(t *testing.T) {
	r := NewRegistry()
	var enabled bool
	r.OnSetFastMode = func(en bool) error {
		enabled = en
		return nil
	}

	cmd, _ := r.Get("fast")
	result, err := cmd.Handler("off")
	if err != nil {
		t.Errorf("fast off returned error: %v", err)
	}
	if enabled {
		t.Error("OnSetFastMode not called with false")
	}
	if !strings.Contains(result, "已关闭") {
		t.Errorf("expected '已关闭', got: %s", result)
	}
}

// TestCommand_FastInvalid /fast 无效参数返回错误
func TestCommand_FastInvalid(t *testing.T) {
	r := NewRegistry()
	cmd, _ := r.Get("fast")

	_, err := cmd.Handler("invalid")
	if err == nil {
		t.Error("expected error for invalid fast mode argument")
	}
}

// TestCommand_FastCooldownStatus /fast cooldown 状态显示
func TestCommand_FastCooldownStatus(t *testing.T) {
	r := NewRegistry()
	r.OnGetFastModeState = func() (config.FastModeState, string, string) {
		return config.FastModeCooldown, "opus-4-fast", "claude-3.5-sonnet"
	}

	cmd, _ := r.Get("fast")
	result, err := cmd.Handler("status")
	if err != nil {
		t.Errorf("fast status cooldown returned error: %v", err)
	}
	if !strings.Contains(result, "冷却中") {
		t.Errorf("expected '冷却中', got: %s", result)
	}
}

// TestCommand_FastSecurityCheck /fast 安全校验拦截（委托 H2）
func TestCommand_FastSecurityCheck(t *testing.T) {
	r := NewRegistry()
	r.OnSecurityCheck = func(cmd string) string { return "block" }

	cmd, _ := r.Get("fast")
	_, err := cmd.Handler("on")
	if err == nil {
		t.Error("expected error when security check blocks")
	}
}

// TestCommand_FastUsage /fast Usage 字段更新
func TestCommand_FastUsage(t *testing.T) {
	r := NewRegistry()
	cmd, _ := r.Get("fast")
	if !strings.Contains(cmd.Usage, "on") || !strings.Contains(cmd.Usage, "off") || !strings.Contains(cmd.Usage, "status") {
		t.Errorf("fast Usage = %q, want contains on/off/status", cmd.Usage)
	}
}

// --- N14 v2 修复测试（B1/D1/D2/D3/D4/D5 回归验证） ---

// TestCommand_FastSecurityCheckErrorID D1 验证：安全拦截错误 wrap ErrSecurityBlocked
func TestCommand_FastSecurityCheckErrorID(t *testing.T) {
	r := NewRegistry()
	r.OnSecurityCheck = func(cmd string) string { return "block" }

	cmd, _ := r.Get("fast")
	_, err := cmd.Handler("on")
	if err == nil {
		t.Fatal("expected error when security check blocks")
	}
	if ai.GetErrorID(err) != ai.ErrSecurityBlocked {
		t.Errorf("ErrorID = %q, want %q", ai.GetErrorID(err), ai.ErrSecurityBlocked)
	}
}

// TestCommand_FastInvalidErrorID D1 验证：无效参数错误 wrap ErrConfigInvalid
func TestCommand_FastInvalidErrorID(t *testing.T) {
	r := NewRegistry()
	cmd, _ := r.Get("fast")
	_, err := cmd.Handler("invalid")
	if err == nil {
		t.Fatal("expected error for invalid fast mode argument")
	}
	if ai.GetErrorID(err) != ai.ErrConfigInvalid {
		t.Errorf("ErrorID = %q, want %q", ai.GetErrorID(err), ai.ErrConfigInvalid)
	}
}

// TestCommand_OutputStyleInvalidErrorID D3 验证：无效风格错误 wrap ErrConfigInvalid
func TestCommand_OutputStyleInvalidErrorID(t *testing.T) {
	r := NewRegistry()
	cmd, _ := r.Get("output-style")
	_, err := cmd.Handler("invalid-style")
	if err == nil {
		t.Fatal("expected error for invalid output style")
	}
	if ai.GetErrorID(err) != ai.ErrConfigInvalid {
		t.Errorf("ErrorID = %q, want %q", ai.GetErrorID(err), ai.ErrConfigInvalid)
	}
}

// TestCommand_OutputStyleChinese D2 验证：用户可见消息已中文化
func TestCommand_OutputStyleChinese(t *testing.T) {
	r := NewRegistry()
	cmd, _ := r.Get("output-style")

	// 无参数：显示当前风格
	result, _ := cmd.Handler("")
	if !strings.Contains(result, "当前输出风格") {
		t.Errorf("expected Chinese '当前输出风格', got: %s", result)
	}

	// 合法风格：切换成功（fallback 路径，无"已保存到配置文件"）
	result, _ = cmd.Handler("concise")
	if !strings.Contains(result, "输出风格已切换为") {
		t.Errorf("expected Chinese '输出风格已切换为', got: %s", result)
	}
}
