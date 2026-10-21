package command

import (
	"strings"
	"testing"
)

func TestParse(t *testing.T) {
	tests := []struct {
		input    string
		wantName string
		wantArgs string
	}{
		{"/help", "help", ""},
		{"/model gpt-4o", "model", "gpt-4o"},
		{"/models", "models", ""},
		{"/compact --ai", "compact", "--ai"},
		{"/session list", "session list", ""},
		{"/session save", "session save", ""},
		{"/session switch abc", "session switch", "abc"},
		{"/cron add", "cron add", ""},
		{"/cron list", "cron list", ""},
		{"/Session List", "session list", ""},
		{"/session  list", "session list", ""},
	}

	for _, tt := range tests {
		name, args := Parse(tt.input)
		if name != tt.wantName {
			t.Errorf("Parse(%q) name = %q, want %q", tt.input, name, tt.wantName)
		}
		if args != tt.wantArgs {
			t.Errorf("Parse(%q) args = %q, want %q", tt.input, args, tt.wantArgs)
		}
	}
}

func TestNewRegistry(t *testing.T) {
	r := NewRegistry()
	if r == nil {
		t.Fatal("NewRegistry returned nil")
	}

	cmd, ok := r.Get("help")
	if !ok {
		t.Error("help command should exist")
	}
	if cmd.Name != "help" {
		t.Errorf("Name = %s, want help", cmd.Name)
	}

	cmd, ok = r.Get("session list")
	if !ok {
		t.Error("session list command should exist")
	}
	if cmd.Name != "session list" {
		t.Errorf("Name = %s, want session list", cmd.Name)
	}

	cmd, ok = r.Get("models")
	if !ok {
		t.Fatal("models command should exist - NOT FOUND in registry!")
	}
	if cmd.Name != "models" {
		t.Errorf("Name = %s, want models", cmd.Name)
	}
}

func TestRegistryAll(t *testing.T) {
	r := NewRegistry()
	all := r.All()
	if len(all) < 10 {
		t.Errorf("All() returned %d commands, expected at least 10", len(all))
	}
}

func TestRegistryByCategory(t *testing.T) {
	r := NewRegistry()
	categories := r.ByCategory()
	if len(categories) == 0 {
		t.Error("ByCategory should not be empty")
	}

	if _, ok := categories["Core"]; !ok {
		t.Error("Core category should exist")
	}
}

func TestSuggest(t *testing.T) {
	r := NewRegistry()

	suggestions := Suggest("hlp", r)
	if len(suggestions) == 0 {
		t.Error("Suggest should find similar commands")
	}

	suggestions = Suggest("xyznonexistent", r)
	// 可能没有建议
	_ = suggestions
}

func TestFormatHelp(t *testing.T) {
	r := NewRegistry()
	help := FormatHelp(r, "")
	if help == "" {
		t.Error("FormatHelp should not be empty")
	}
	if !containsString(help, "Core") {
		t.Error("FormatHelp should contain category headers")
	}
}

// TestFormatHelp_HideStubByDefault 测试默认 /help 隐藏桩命令
func TestFormatHelp_HideStubByDefault(t *testing.T) {
	r := NewRegistry()
	help := FormatHelp(r, "")
	// 桩命令 /login 不应出现在默认 /help 输出中
	if containsString(help, "/login") {
		t.Error("默认 /help 不应显示桩命令 /login")
	}
	if containsString(help, "/break-cache") {
		t.Error("默认 /help 不应显示桩命令 /break-cache")
	}
	if containsString(help, "/voice") {
		t.Error("默认 /help 不应显示桩命令 /voice")
	}
}

// TestFormatHelp_ShowAllWithStub 测试 /help --all 显示桩命令及 [未实现] 后缀
func TestFormatHelp_ShowAllWithStub(t *testing.T) {
	r := NewRegistry()
	help := FormatHelp(r, "--all")
	// 桩命令应在 --all 输出中显示
	if !containsString(help, "/login") {
		t.Error("/help --all 应显示桩命令 /login")
	}
	if !containsString(help, "/break-cache") {
		t.Error("/help --all 应显示桩命令 /break-cache")
	}
	// 桩命令应带 [stub 未实现] 后缀
	// （C3，2026-10-05： 的 [桩命令] 是行话；换成用户能直接读懂的标记。
	// ★ C-6，2026-10-10 有意翻转：双语化 → [stub 未实现]，勿当回归）
	if !containsString(help, "[stub 未实现]") {
		t.Error("/help --all 输出应包含 [stub 未实现] 后缀")
	}
}

// TestStubCommands_StillCallable 测试桩命令仍可调用返回占位文本
func TestStubCommands_StillCallable(t *testing.T) {
	r := NewRegistry()
	// review 已从桩命令升级为真实命令（P0-4 本地多 Agent 审查），不再属于桩命令列表
	// fast 已从桩命令升级为真实命令（N14 output-styles），不再属于桩命令列表
	stubCmds := []string{"login", "logout", "plan", "voice", "break-cache"}
	for _, name := range stubCmds {
		cmd, ok := r.Get(name)
		if !ok {
			t.Errorf("桩命令 /%s 应在注册表中存在", name)
			continue
		}
		if !cmd.Stub {
			t.Errorf("命令 /%s 应标记 Stub: true", name)
		}
		output, err := cmd.Handler("")
		if err != nil {
			t.Errorf("桩命令 /%s 调用失败: %v", name, err)
		}
		if output == "" {
			t.Errorf("桩命令 /%s 应返回非空占位文本", name)
		}
	}
}

func TestCommandExecution(t *testing.T) {
	r := NewRegistry()

	cmd, ok := r.Get("version")
	if !ok {
		t.Fatal("version command should exist")
	}

	output, err := cmd.Handler("")
	if err != nil {
		t.Fatalf("version handler failed: %v", err)
	}
	if output == "" {
		t.Error("version handler should return non-empty output")
	}

	cmd, ok = r.Get("exit")
	if !ok {
		t.Fatal("exit command should exist")
	}
	output, _ = cmd.Handler("")
	if output != "__EXIT__" {
		t.Errorf("exit handler = %s, want __EXIT__", output)
	}
}

func TestSessionListEndToEnd(t *testing.T) {
	r := NewRegistry()

	inputs := []string{"/session list", "/session save", "/session switch abc", "/cron add", "/cron list"}
	for _, input := range inputs {
		name, args := Parse(input)
		cmd, ok := r.Get(name)
		if !ok {
			t.Errorf("Parse(%q) returned name=%q, but Registry.Get(%q) failed", input, name, name)
			continue
		}
		output, err := cmd.Handler(args)
		if err != nil {
			t.Errorf("Handler for %q failed: %v", name, err)
		}
		if output == "" {
			t.Errorf("Handler for %q returned empty output", name)
		}
		t.Logf("Parse(%q) → name=%q args=%q → output=%q", input, name, args, output)
	}
}

func containsString(s, substr string) bool {
	for i := 0; i+len(substr) <= len(s); i++ {
		if s[i:i+len(substr)] == substr {
			return true
		}
	}
	return false
}

// TestRegistry_Get 测试 Get 方法获取已注册和未注册命令
func TestRegistry_Get(t *testing.T) {
	r := NewRegistry()

	// 获取已注册的 help 命令
	cmd, ok := r.Get("help")
	if !ok {
		t.Error("Get(\"help\") should return true")
	}
	if cmd.Name != "help" {
		t.Errorf("Get(\"help\") Name = %q, want \"help\"", cmd.Name)
	}
	if cmd.Description != "显示命令列表" {
		t.Errorf("Get(\"help\") Description = %q, want \"显示命令列表\"", cmd.Description)
	}

	// 获取不存在的命令
	_, ok = r.Get("nonexistent")
	if ok {
		t.Error("Get(\"nonexistent\") should return false")
	}
}

// TestRegistry_ByCategory 测试 ByCategory 方法按分类返回命令
func TestRegistry_ByCategory(t *testing.T) {
	r := NewRegistry()
	categories := r.ByCategory()

	if len(categories) == 0 {
		t.Error("ByCategory() 不should return empty map")
	}

	// 验证核心分类存在
	coreCmds, ok := categories["Core"]
	if !ok {
		t.Error("ByCategory() should contain \"Core\" category")
	}
	if len(coreCmds) == 0 {
		t.Error("\"Core\" category should have at least one command")
	}

	modelCmds, ok := categories["Model"]
	if !ok {
		t.Error("ByCategory() should contain \"Model\" category")
	}
	if len(modelCmds) == 0 {
		t.Error("\"Model\" category should have at least one command")
	}
}

// TestParse_SingleWord 测试解析单个词的斜杠命令
func TestParse_SingleWord(t *testing.T) {
	name, args := Parse("/help")
	if name != "help" {
		t.Errorf("Parse(\"/help\") name = %q, want \"help\"", name)
	}
	if args != "" {
		t.Errorf("Parse(\"/help\") args = %q, want \"\"", args)
	}
}

// TestParse_MultiWord 测试解析多词斜杠命令
func TestParse_MultiWord(t *testing.T) {
	name, args := Parse("/model add qwen3")
	if name != "model" {
		t.Errorf("Parse(\"/model add qwen3\") name = %q, want \"model\"", name)
	}
	if args != "add qwen3" {
		t.Errorf("Parse(\"/model add qwen3\") args = %q, want \"add qwen3\"", args)
	}
}

// TestSuggest_ExactMatch 测试精确匹配的建议
func TestSuggest_ExactMatch(t *testing.T) {
	r := NewRegistry()
	suggestions := Suggest("hel", r)

	found := false
	for _, s := range suggestions {
		if s == "help" {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("Suggest(\"hel\") should contain \"help\"，actual: %v", suggestions)
	}
}

// TestSuggest_FuzzyMatch 测试模糊/拼写错误的建议
func TestSuggest_FuzzyMatch(t *testing.T) {
	r := NewRegistry()
	suggestions := Suggest("hlep", r)

	// "hlep" 与 "help" 的 Levenshtein 距离为 2，应被模糊匹配
	found := false
	for _, s := range suggestions {
		if s == "help" {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("Suggest(\"hlep\") 应模糊匹配到 \"help\"，actual: %v", suggestions)
	}
}

// TestSuggest_NoMatch 测试no match的建议
func TestSuggest_NoMatch(t *testing.T) {
	r := NewRegistry()
	suggestions := Suggest("xyz", r)

	if len(suggestions) != 0 {
		t.Errorf("Suggest(\"xyz\") should return empty列表，actual: %v", suggestions)
	}
}

// TestLevenshtein 测试 Levenshtein 距离计算
func TestLevenshtein(t *testing.T) {
	tests := []struct {
		a, b string
		want int
	}{
		{"kitten", "sitting", 3},
		{"", "abc", 3},
		{"abc", "abc", 0},
		{"", "", 0},
		{"abc", "", 3},
	}

	for _, tt := range tests {
		got := levenshtein(tt.a, tt.b)
		if got != tt.want {
			t.Errorf("levenshtein(%q, %q) = %d, want %d", tt.a, tt.b, got, tt.want)
		}
	}
}

// TestCompletion 测试前缀匹配命令补全
func TestCompletion(t *testing.T) {
	r := NewRegistry()

	matches := r.Completion("/ses")
	if len(matches) == 0 {
		t.Error("Completion(\"/ses\") should return matches for session commands")
	}
	for _, name := range matches {
		if !startsWith(name, "ses") {
			t.Errorf("Completion match %q does not start with 'ses'", name)
		}
	}

	matches = r.Completion("")
	if len(matches) == 0 {
		t.Error("Completion(\"\") should return all commands")
	}

	matches = r.Completion("xyz")
	if len(matches) != 0 {
		t.Error("Completion(\"xyz\") should return empty for no matches")
	}

	matches = r.Completion("/h")
	if len(matches) == 0 {
		t.Error("Completion(\"/h\") should return matches like help, hooks")
	}
}

func startsWith(s, prefix string) bool {
	return len(s) >= len(prefix) && s[:len(prefix)] == prefix
}

// TestCommandExecution_Compact 测试 /compact 命令的注册和执行
// 验证 compact 命令在注册表中存在，且其 Handler 能正常执行并返回非空输出
func TestCommandExecution_Compact(t *testing.T) {
	r := NewRegistry()

	cmd, ok := r.Get("compact")
	if !ok {
		t.Fatal("compact 命令应在注册表中存在")
	}
	if cmd.Name != "compact" {
		t.Errorf("compact 命令 Name = %q, expected \"compact\"", cmd.Name)
	}
	if cmd.Category != "Core" {
		t.Errorf("compact command Category = %q, expected \"Core\"", cmd.Category)
	}

	output, err := cmd.Handler("")
	if err != nil {
		t.Fatalf("compact 命令执行失败: %v", err)
	}
	if output == "" {
		t.Error("compact 命令should return non-empty输出")
	}
}

// TestCommandExecution_Clear 测试 /clear 命令的注册和执行
// 验证 clear 命令在注册表中存在，且其 Handler 能正常执行
func TestCommandExecution_Clear(t *testing.T) {
	r := NewRegistry()

	cmd, ok := r.Get("clear")
	if !ok {
		t.Fatal("clear 命令应在注册表中存在")
	}
	if cmd.Name != "clear" {
		t.Errorf("clear 命令 Name = %q, expected \"clear\"", cmd.Name)
	}
	if cmd.Category != "Core" {
		t.Errorf("clear command Category = %q, expected \"Core\"", cmd.Category)
	}

	output, err := cmd.Handler("")
	if err != nil {
		t.Fatalf("clear 命令执行失败: %v", err)
	}
	if output == "" {
		t.Error("clear 命令should return non-empty输出")
	}
}

// TestCommandExecution_Model 测试 /model 命令的注册和执行
// 验证 model 命令在注册表中存在，且其 Handler 能正常执行
func TestCommandExecution_Model(t *testing.T) {
	r := NewRegistry()

	cmd, ok := r.Get("model")
	if !ok {
		t.Fatal("model 命令应在注册表中存在")
	}
	if cmd.Name != "model" {
		t.Errorf("model 命令 Name = %q, expected \"model\"", cmd.Name)
	}
	if cmd.Category != "Model" {
		t.Errorf("model command Category = %q, expected \"Model\"", cmd.Category)
	}

	// 测试无参数执行
	output, err := cmd.Handler("")
	if err != nil {
		t.Fatalf("model 命令执行失败: %v", err)
	}
	if output == "" {
		t.Error("model 命令无参数时should return non-empty输出")
	}

	// 测试带参数执行
	output, err = cmd.Handler("qwen3.5-plus")
	if err != nil {
		t.Fatalf("model 命令带参数执行失败: %v", err)
	}
	if output == "" {
		t.Error("model 命令带参数时should return non-empty输出")
	}
}

// TestHooksCommand 测试 /hooks 命令是否已注册
func TestHooksCommand(t *testing.T) {
	registry := NewRegistry()
	_, ok := registry.Get("hooks")
	if !ok {
		t.Error("hooks command should be registered")
	}
}

// TestMcpCommand 测试 /mcp 命令是否已注册
func TestMcpCommand(t *testing.T) {
	registry := NewRegistry()
	_, ok := registry.Get("mcp")
	if !ok {
		t.Error("mcp command should be registered")
	}
}

// TestImportCommand 测试 /import 命令是否已注册
func TestImportCommand(t *testing.T) {
	registry := NewRegistry()
	_, ok := registry.Get("import")
	if !ok {
		t.Error("import command should be registered")
	}
}

// TestMigrateCommand 测试 /migrate 命令是否已注册
func TestMigrateCommand(t *testing.T) {
	registry := NewRegistry()
	_, ok := registry.Get("migrate")
	if !ok {
		t.Error("migrate command should be registered")
	}
}

// TestUpgradeCommand 测试 /upgrade 命令是否已注册
func TestUpgradeCommand(t *testing.T) {
	registry := NewRegistry()
	_, ok := registry.Get("upgrade")
	if !ok {
		t.Error("upgrade command should be registered")
	}
}

// TestFeedbackCommand 测试 /feedback 命令是否已注册
func TestFeedbackCommand(t *testing.T) {
	registry := NewRegistry()
	_, ok := registry.Get("feedback")
	if !ok {
		t.Error("feedback command should be registered")
	}
}

// TestNewCommandsCount 测试新增命令后总命令数
func TestNewCommandsCount(t *testing.T) {
	registry := NewRegistry()
	count := len(registry.List())
	if count < 54 {
		t.Errorf("Expected at least 54 commands, got %d", count)
	}
}

// TestCommandExecution_Exit 测试 /exit 命令的注册和执行
// 验证 exit 命令在注册表中存在，且其 Handler 返回 "__EXIT__" 信号
func TestCommandExecution_Exit(t *testing.T) {
	r := NewRegistry()

	cmd, ok := r.Get("exit")
	if !ok {
		t.Fatal("exit 命令应在注册表中存在")
	}
	if cmd.Name != "exit" {
		t.Errorf("exit 命令 Name = %q, expected \"exit\"", cmd.Name)
	}
	if cmd.Category != "Core" {
		t.Errorf("exit command Category = %q, expected \"Core\"", cmd.Category)
	}

	output, err := cmd.Handler("")
	if err != nil {
		t.Fatalf("exit 命令执行失败: %v", err)
	}
	if output != "__EXIT__" {
		t.Errorf("exit 命令should return \"__EXIT__\"，actual %q", output)
	}
}

// ========== 守护进程命令路由测试 ==========

// TestDaemonCommandsRegistered 测试所有 daemon 命令是否已在注册表中
func TestDaemonCommandsRegistered(t *testing.T) {
	r := NewRegistry()

	daemonCmds := []string{
		"daemon start",
		"daemon stop",
		"daemon restart",
		"daemon status",
		"daemon add",
		"daemon list",
		"daemon cancel",
		"daemon log",
	}

	for _, name := range daemonCmds {
		cmd, ok := r.Get(name)
		if !ok {
			t.Errorf("daemon 命令 %q 应已注册", name)
			continue
		}
		if cmd.Category != "Daemon" {
			t.Errorf("daemon command %q Category = %q, expected \"Daemon\"", name, cmd.Category)
		}
	}
}

// TestTasksCommandRegistered 测试 /tasks 命令是否已注册
func TestTasksCommandRegistered(t *testing.T) {
	r := NewRegistry()

	cmd, ok := r.Get("tasks")
	if !ok {
		t.Fatal("tasks 命令应已注册")
	}
	if cmd.Category != "Core" {
		t.Errorf("tasks command Category = %q, expected \"Core\"", cmd.Category)
	}
}

// TestDaemonCommandsRouteToOnDaemon 测试所有 daemon 命令是否路由到 OnDaemon 回调
func TestDaemonCommandsRouteToOnDaemon(t *testing.T) {
	r := NewRegistry()

	var calledWith string
	r.OnDaemon = func(args string) string {
		calledWith = args
		return "OK"
	}

	testCases := []struct {
		cmdName     string
		handlerArg  string
		expectedArg string
	}{
		{"daemon start", "", "start"},
		{"daemon stop", "", "stop"},
		{"daemon restart", "", "restart"},
		{"daemon status", "", "status"},
		{"daemon add", "测试任务", "add 测试任务"},
		{"daemon list", "", "list"},
		{"daemon cancel", "task-123", "cancel task-123"},
		{"daemon log", "", "log"},
	}

	for _, tc := range testCases {
		t.Run(tc.cmdName, func(t *testing.T) {
			calledWith = ""
			cmd, ok := r.Get(tc.cmdName)
			if !ok {
				t.Fatalf("命令 %q 不存在", tc.cmdName)
			}

			output, err := cmd.Handler(tc.handlerArg)
			if err != nil {
				t.Fatalf("命令 %q 执行失败: %v", tc.cmdName, err)
			}
			if output != "OK" {
				t.Errorf("命令 %q 返回 %q, expected \"OK\"", tc.cmdName, output)
			}
			if calledWith != tc.expectedArg {
				t.Errorf("命令 %q OnDaemon 参数 = %q, expected %q", tc.cmdName, calledWith, tc.expectedArg)
			}
		})
	}
}

// TestTasksCommandRouteToOnDaemon 测试 /tasks 命令是否路由到 OnDaemon
func TestTasksCommandRouteToOnDaemon(t *testing.T) {
	r := NewRegistry()

	var calledWith string
	r.OnDaemon = func(args string) string {
		calledWith = args
		return "Task list"
	}

	cmd, ok := r.Get("tasks")
	if !ok {
		t.Fatal("tasks command should be registered")
	}

	output, err := cmd.Handler("")
	if err != nil {
		t.Fatalf("tasks command execution failed: %v", err)
	}
	if output != "Task list" {
		t.Errorf("tasks returned %q, expected \"Task list\"", output)
	}
	if calledWith != "list" {
		t.Errorf("tasks OnDaemon 参数 = %q, expected \"list\"", calledWith)
	}
}

// TestDaemonCommandsNoCallback 测试未设置 OnDaemon 时 daemon 命令返回默认消息
func TestDaemonCommandsNoCallback(t *testing.T) {
	r := NewRegistry()

	daemonCmds := []string{
		"daemon start", "daemon stop", "daemon restart", "daemon status",
		"daemon add", "daemon list", "daemon cancel", "daemon log",
	}

	for _, name := range daemonCmds {
		t.Run(name, func(t *testing.T) {
			cmd, ok := r.Get(name)
			if !ok {
				t.Fatalf("命令 %q 不存在", name)
			}

			output, err := cmd.Handler("")
			if err != nil {
				t.Fatalf("命令 %q 执行失败: %v", name, err)
			}

			if !containsString(output, "not initialized") {
				t.Errorf("command %q without callback should return 'not initialized', actual: %q", name, output)
			}
		})
	}
}

// TestTasksCommandNoCallbackReportsNotWired 测试未设置 OnDaemon 时 /tasks 返回「未接线」提示。
//
// ★ 有意翻转：原断言是
//
//	containsString(output, "none") && containsString(output, "Background tasks")
//
// 它把**假事实**（未接线却报"没有后台任务"）锁成了绿色契约 —— 改成诚实文案会让它变红，
// 而最自然的反应是"改回假话"。故改为断言**不变量本身**（台账 §14 ·）：
//
//	I-1 同源：与 /daemon list 的 fallback 逐字相同（两条命令问的是同一件事）
//	I-2 不断言：不得含任何任务数量断言（none / 0 tasks / no tasks / 暂无）
//	I-3 契约：保留子串 "not initialized"（既有 8 条 daemon 契约 + TestDaemonCommandsNoCallback）
func TestTasksCommandNoCallbackReportsNotWired(t *testing.T) {
	r := NewRegistry()

	tasksCmd, ok := r.Get("tasks")
	if !ok {
		t.Fatal("tasks 命令应已注册")
	}
	listCmd, ok := r.Get("daemon list")
	if !ok {
		t.Fatal("daemon list 命令应已注册")
	}

	gotTasks, err := tasksCmd.Handler("")
	if err != nil {
		t.Fatalf("/tasks 执行失败: %v", err)
	}
	gotList, err := listCmd.Handler("")
	if err != nil {
		t.Fatalf("/daemon list 执行失败: %v", err)
	}

	// I-1 同源。⚠️ 本判据是**漂移守卫**，不是内容判据：两侧共用同一 SSOT 时，
	// SSOT 变空两侧一起变空 ⇒ 仍然相等 ⇒ 恒绿。内容正确性由下面 I-2 / I-3 负责。
	// 三条判据用 Errorf 累积报（不用 Fatalf）⇒ before 态能同时暴露 I-1/I-2/I-3，
	// 归因清晰；Fatalf 会在 I-1 短路，看不到后两条的独立判别力。
	if gotTasks != gotList {
		t.Errorf("I-1 同源被破坏：/tasks=%q /daemon list=%q", gotTasks, gotList)
	}
	// I-2 不断言任务数量（禁词集取自 的 I-2 定义）
	lower := strings.ToLower(gotTasks)
	for _, banned := range []string{"none", "0 tasks", "no tasks", "暂无"} {
		if strings.Contains(lower, banned) {
			t.Errorf("I-2 被破坏：未接线时不得断言任务数量（命中禁词 %q）：%q", banned, gotTasks)
		}
	}
	// I-3 保留既有契约子串
	if !strings.Contains(gotTasks, "not initialized") {
		t.Errorf("I-3 被破坏：未接线提示须保留既有契约子串 'not initialized'：%q", gotTasks)
	}
}

// TestDaemonParseRouting 测试 Parse 对 daemon 命令的解析
func TestDaemonParseRouting(t *testing.T) {
	testCases := []struct {
		input    string
		wantName string
		wantArgs string
	}{
		{"/daemon start", "daemon start", ""},
		{"/daemon stop", "daemon stop", ""},
		{"/daemon restart", "daemon restart", ""},
		{"/daemon status", "daemon status", ""},
		{"/daemon add test task", "daemon add", "test task"},
		{"/daemon list", "daemon list", ""},
		{"/daemon cancel task-123", "daemon cancel", "task-123"},
		{"/daemon log", "daemon log", ""},
	}

	for _, tc := range testCases {
		t.Run(tc.input, func(t *testing.T) {
			name, args := Parse(tc.input)
			if name != tc.wantName {
				t.Errorf("Parse(%q) name = %q, expected %q", tc.input, name, tc.wantName)
			}
			if args != tc.wantArgs {
				t.Errorf("Parse(%q) args = %q, expected %q", tc.input, args, tc.wantArgs)
			}
		})
	}
}
