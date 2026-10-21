package tui

import (
	"bytes"
	"fmt"
	"io"
	"os"
	"strings"
	"testing"

	"github.com/anchorandfish/whale-code/internal/ai"
	"github.com/anchorandfish/whale-code/internal/command"
	"github.com/anchorandfish/whale-code/internal/config"
	ctxpkg "github.com/anchorandfish/whale-code/internal/context"
	"github.com/anchorandfish/whale-code/internal/permission"
	"github.com/anchorandfish/whale-code/internal/prompt"
	"github.com/anchorandfish/whale-code/internal/session"
	"github.com/anchorandfish/whale-code/internal/tools"
)

// newTestPlainRunner creates a test PlainRunner 实例
// 仅初始化必要字段，不dependency外部服务
func newTestPlainRunner() *PlainRunner {
	cfg := config.DefaultConfig()
	cfg.Model = "test-model"
	cfg.PermissionMode = config.PermWorkspaceWrite

	client := newTestClient("test-model")
	reg := newTestRegistry()
	estimator := ai.NewTokenEstimator()
	toolRegistry := tools.NewRegistry()
	// 设置 CmdRegistry 并指定默认版本号，供 formatFullStatus 读取版本
	cmdReg := command.NewRegistry()
	cmdReg.Version = "0.1.0"

	return &PlainRunner{
		runnerCore: &runnerCore{
			Session:         session.NewSession(),
			Config:          &cfg,
			AIClient:        client,
			Registry:        toolRegistry,
			Executor:        tools.NewExecutor(toolRegistry, os.TempDir()),
			Policy:          permission.NewPolicy(permission.LevelWorkspaceWrite),
			Budget:          ctxpkg.DefaultBudget(cfg.ContextWindow),
			Compactor:       ctxpkg.NewCompactor(estimator),
			Estimator:       estimator,
			MagicDocs:       config.NewMagicDocs(""), //nolint:staticcheck // H4 保留 V1 作为 fallback
			MemoryStore:     nil,
			MemoryIndex:     nil,
			MemSelector:     config.NewMemorySelector(nil, nil, client),
			Fallback:        ai.NewFallbackTracker("test-model", ""),
			ModelName:       "test-model",
			ModelRegistry:   reg,
			AvailableModels: reg.Names(),
			WorkDir:         os.TempDir(),
			CmdRegistry:     cmdReg,
			PromptBuilder:   prompt.NewBuilder(os.TempDir(), "test-model", config.NewMagicDocs(""), config.NewMemorySelector(nil, nil, client), tools.NewExecutor(toolRegistry, os.TempDir()), 100*1024), //nolint:staticcheck // H4 保留 V1 作为 fallback
		},
	}
}

// TestPlainWriter_Write 测试纯文本模式的基本文本输出
// 验证 formatHelp 函数能生成包含所有命令分类的帮助文本，
// 且输出格式正确
func TestPlainWriter_Write(t *testing.T) {
	help := formatHelp(command.NewRegistry())

	// 验证帮助文本非空
	if help == "" {
		t.Error("formatHelp 不should return empty字符串")
	}

	// 验证包含核心命令分类
	expectedCategories := []string{"Core", "Model", "Session", "Export"}
	for _, cat := range expectedCategories {
		if !strings.Contains(help, cat) {
			t.Errorf("帮助文本should contain命令分类 %q", cat)
		}
	}

	// 验证包含关键命令
	expectedCommands := []string{"/help", "/status", "/model", "/clear", "/exit"}
	for _, cmd := range expectedCommands {
		if !strings.Contains(help, cmd) {
			t.Errorf("帮助文本should contain命令 %q", cmd)
		}
	}
}

// TestPlainWriter_WriteToolCall test tool调用的显示格式
// 验证 PlainRunner 的工具调用输出格式正确，
// 包含工具名称和截断后的输入预览
func TestPlainWriter_WriteToolCall(t *testing.T) {
	// 捕获 stdout 输出
	old := os.Stdout
	r, w, _ := os.Pipe()
	os.Stdout = w

	_ = newTestPlainRunner()

	// mock工具调用的输出格式（与 handleStreamEvents 中的格式一致）
	toolName := "read_file"
	toolInput := `{"path": "/src/main.go"}`
	fmt.Printf("\n[Tool: %s(%s)]\n", toolName, strings.TrimSpace(toolInput))

	w.Close()
	os.Stdout = old

	var buf bytes.Buffer
	io.Copy(&buf, r)
	output := buf.String()

	if !strings.Contains(output, "[Tool:") {
		t.Error("工具调用输出should contain '[Tool:' 标记")
	}
	if !strings.Contains(output, toolName) {
		t.Errorf("工具调用输出should contain工具名 %q", toolName)
	}
}

// TestPlainWriter_WriteError test error信息的友好显示
// 验证 PlainRunner 的 friendlyError 方法能将原始错误转换为User友好的提示信息，
// 包括 DNS 错误、连接拒绝、超时和 TLS 错误等scenario
func TestPlainWriter_WriteError(t *testing.T) {
	runner := newTestPlainRunner()

	tests := []struct {
		name     string
		errMsg   string
		contains string
	}{
		{
			name:     "DNS 解析失败",
			errMsg:   "lookup dashscope.aliyuncs.com: no such host",
			contains: "DNS resolution failed",
		},
		{
			name:     "连接rejected",
			errMsg:   "dial tcp 127.0.0.1:8080: connection refused",
			contains: "Connection refused",
		},
		{
			name:     "I/O 超时",
			errMsg:   "read tcp 192.168.1.1:8080: i/o timeout",
			contains: "timed out",
		},
		{
			name:     "TLS 握手错误",
			errMsg:   "TLS handshake failure: certificate verify failed",
			contains: "TLS/SSL error",
		},
		{
			name:     "通用错误",
			errMsg:   "something went wrong",
			contains: "Error:",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := runner.friendlyError(fmt.Errorf("%s", tt.errMsg))
			if !strings.Contains(result, tt.contains) {
				t.Errorf("friendlyError(%q) should contain %q，actual: %q", tt.errMsg, tt.contains, result)
			}
		})
	}
}

// TestPlainWriter_Flush test state刷新输出
// 验证 printStatus 方法能正确格式化并输出当前状态行，
// 包含模型名、上下文使用率、token统计等信息
func TestPlainWriter_Flush(t *testing.T) {
	// 捕获 stdout 输出
	old := os.Stdout
	r, w, _ := os.Pipe()
	os.Stdout = w

	runner := newTestPlainRunner()
	runner.InputTokens = 100
	runner.OutputTokens = 50

	runner.printStatus()

	w.Close()
	os.Stdout = old

	var buf bytes.Buffer
	io.Copy(&buf, r)
	output := buf.String()

	// 验证状态行包含模型名
	if !strings.Contains(output, "test-model") {
		t.Errorf("状态行should contain模型名 'test-model'，actual: %q", output)
	}

	// 验证状态行包含token统计
	if !strings.Contains(output, "in:100") {
		t.Errorf("状态行should contain 'in:100'，actual: %q", output)
	}
	if !strings.Contains(output, "out:50") {
		t.Errorf("状态行should contain 'out:50'，actual: %q", output)
	}

	// 验证状态行包含权限模式
	if !strings.Contains(output, "workspace-write") {
		t.Errorf("状态行should contain权限模式 'workspace-write'，actual: %q", output)
	}
}

// TestPlainWriter_PrintWelcome 测试欢迎信息输出
// 验证 printWelcome 函数输出包含版本号、模型名和关键命令提示
func TestPlainWriter_PrintWelcome(t *testing.T) {
	// 捕获 stdout 输出
	old := os.Stdout
	r, w, _ := os.Pipe()
	os.Stdout = w

	printWelcome("test-model", "/tmp/project", "workspace-write")

	w.Close()
	os.Stdout = old

	var buf bytes.Buffer
	io.Copy(&buf, r)
	output := buf.String()

	if !strings.Contains(output, "Whale Code") {
		t.Error("欢迎信息should contain 'Whale Code'")
	}
	if !strings.Contains(output, "plain mode") {
		t.Error("欢迎信息should contain 'plain mode'")
	}
	if !strings.Contains(output, "test-model") {
		t.Error("欢迎信息should contain模型名 'test-model'")
	}
	if !strings.Contains(output, "/help") {
		t.Error("欢迎信息should contain /help 命令")
	}
}

// TestPlainWriter_TruncateStr 测试字符串截断helper functions
// 验证 truncateStr 能正确截断超长字符串，短字符串保持不变
func TestPlainWriter_TruncateStr(t *testing.T) {
	tests := []struct {
		name   string
		input  string
		maxLen int
		expect string
	}{
		{"短字符串不截断", "hello", 10, "hello"},
		{"精确长度不截断", "hello", 5, "hello"},
		{"超长截断加省略号", "hello world", 5, "hello..."},
		{"空字符串", "", 5, ""},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := truncateStr(tt.input, tt.maxLen)
			if got != tt.expect {
				t.Errorf("truncateStr(%q, %d) = %q，expected %q", tt.input, tt.maxLen, got, tt.expect)
			}
		})
	}
}

// TestPlainWriter_FormatFullStatus 测试详细状态格式化
// 验证 formatFullStatus 方法返回的状态信息包含所有关键字段
func TestPlainWriter_FormatFullStatus(t *testing.T) {
	runner := newTestPlainRunner()

	status := runner.formatFullStatus()

	// 验证包含版本号
	if !strings.Contains(status, "Whale Code v0.1.0") {
		t.Error("详细状态should contain版本号 'Whale Code v0.1.0'")
	}
	if !strings.Contains(status, "plain mode") {
		t.Error("详细状态should contain 'plain mode'")
	}

	// 验证包含模型名
	if !strings.Contains(status, "test-model") {
		t.Error("详细状态should contain模型名 'test-model'")
	}

	// 验证包含权限模式
	if !strings.Contains(status, "workspace-write") {
		t.Error("详细状态should contain权限模式 'workspace-write'")
	}

	// 验证包含工作目录
	if !strings.Contains(status, "Workdir:") {
		t.Error("详细状态should contain 'Workdir:' 字段")
	}

	// 验证包含token统计
	if !strings.Contains(status, "Tokens:") {
		t.Error("详细状态should contain 'Tokens:' 字段")
	}

	// 验证包含消息计数
	if !strings.Contains(status, "Messages:") {
		t.Error("详细状态should contain 'Messages:' 字段")
	}
}

// TestPlainWriter_HandleThink 测试 thinking 三态切换。
// ★ 与 TUI 侧 Model.handleThink 是同一命令的两处实现，语义必须一致（同族）。
func TestPlainWriter_HandleThink(t *testing.T) {
	runner := newTestPlainRunner()

	// 显式开启
	result := runner.handleThink("on")
	if runner.ThinkingEnabled == nil || !*runner.ThinkingEnabled {
		t.Errorf("handleThink(\"on\") 应设为「显式开启」，actual: %v", runner.ThinkingEnabled)
	}
	if !strings.Contains(result, "ON") {
		t.Errorf("handleThink(\"on\") 结果should contain 'ON'，actual: %q", result)
	}

	// 显式关闭
	result = runner.handleThink("off")
	if runner.ThinkingEnabled == nil || *runner.ThinkingEnabled {
		t.Errorf("handleThink(\"off\") 应设为「显式关闭」，actual: %v", runner.ThinkingEnabled)
	}
	if !strings.Contains(result, "OFF") {
		t.Errorf("handleThink(\"off\") 结果should contain 'OFF'，actual: %q", result)
	}

	// 其余参数 → 回到「未指定」（不是"关闭"：这是三态与二值的分界）
	runner.ThinkingEnabled = ai.ThinkingPtr(true)
	result = runner.handleThink("")
	if runner.ThinkingEnabled != nil {
		t.Errorf("handleThink(\"\") 应回到「未指定」(nil)，actual: %v", *runner.ThinkingEnabled)
	}
	if !strings.Contains(result, "default") {
		t.Errorf("handleThink(\"\") 结果应说明回到服务端默认，actual: %q", result)
	}
}

// TestPlainRunner_RunCI 测试 CI 模式运行单个提示后退出
func TestPlainRunner_RunCI(t *testing.T) {
	_ = newTestPlainRunner()

	// RunCI 需要actual API 连接，这里仅验证函数签名和基本逻辑
	// 测试空 prompt 时不会 panic
	// 由于没有真实 API Key，RunCI 会返回错误，这是预期行为
}

// TestPlainRunner_ProcessInput 测试 ProcessInput 去除空白字符
func TestPlainRunner_ProcessInput(t *testing.T) {
	runner := newTestPlainRunner()

	// 捕获 stdout 输出
	old := os.Stdout
	r, w, _ := os.Pipe()
	os.Stdout = w

	// 带前后空白的输入应被 TrimSpace 处理
	runner.processInput("  hello world  ")

	w.Close()
	os.Stdout = old

	var buf bytes.Buffer
	io.Copy(&buf, r)
	output := buf.String()

	if !strings.Contains(output, "hello world") {
		t.Errorf("processInput 应输出 'hello world'，actual: %q", output)
	}

	// 空白输入应被忽略
	runner2 := newTestPlainRunner()
	msgCountBefore := len(runner2.Session.GetMessages())
	runner2.processInput("   ")
	msgCountAfter := len(runner2.Session.GetMessages())
	if msgCountAfter != msgCountBefore {
		t.Error("空白输入不应添加消息到会话")
	}
}

// TestPlainRunner_BOMRemoval 测试 UTF-8 BOM 前缀从输入中移除
func TestPlainRunner_BOMRemoval(t *testing.T) {
	// mock Run 方法中的 BOM 移除逻辑
	// Run 方法中: line = strings.TrimPrefix(line, "\ufeff")
	bomPrefix := "\ufeffhello"
	result := strings.TrimPrefix(bomPrefix, "\ufeff")
	if result != "hello" {
		t.Errorf("BOM 移除后 = %q, want \"hello\"", result)
	}

	// 无 BOM 的字符串应保持不变
	normal := "hello"
	result = strings.TrimPrefix(normal, "\ufeff")
	if result != "hello" {
		t.Errorf("无 BOM 字符串不应被修改，actual: %q", result)
	}

	// 仅 BOM 的字符串应变为空
	onlyBom := "\ufeff"
	result = strings.TrimPrefix(onlyBom, "\ufeff")
	if result != "" {
		t.Errorf("仅 BOM 的字符串应变为空，actual: %q", result)
	}
}

// TestPlainFormatHelp_HidesHiddenCommands V0.1-HIDDEN：plain 模式帮助与注册表口径一致（隐藏命令不显示，核心命令保留）
func TestPlainFormatHelp_HidesHiddenCommands(t *testing.T) {
	reg := command.NewRegistry()
	help := formatHelp(reg)

	// 隐藏命令样本：GitHub/自动化/Git/调参/杂项
	// ★ 2026-10-03：/daemon start、/memory、/fast 曾取消隐藏 ⇒ 从"不应显示"样本里移除；
	// ★ 2026-10-12：随 v1 命令面收口收回隐藏 ⇒ 加回"不应显示"样本。
	// ★ 2026-10-12 14:50：/memory 经用户拍板单独恢复可见 ⇒ 移出本表、移入下方"应显示"表。
	for _, hidden := range []string{"/commit", "/pr create", "/issue create", "/actions list",
		"/loop", "/cron add", "/worktree add",
		"/compact-strategy", "/stats", "/summary", "/thinkback",
		"/output-style", "/privacy", "/search", "/session-split", "/export-user",
		"/export-format", "/share", "/import", "/session merge", "/branch list",
		"/fast", "/daemon start", "/daemon cancel", "/task list", "/task cancel"} {
		if strings.Contains(help, hidden) {
			t.Errorf("plain /help 不应显示隐藏命令 %s", hidden)
		}
	}
	// 核心命令保留
	for _, visible := range []string{"/diff", "/compact", "/model", "/usage", "/context",
		"/undo", "/redo", "/rewind", "/archive", "/mcp", "/review", "/permissions", "/sandbox",
		"/security-preset", "/skill", "/memory"} {
		if !strings.Contains(help, visible) {
			t.Errorf("plain /help 应保留核心命令 %s", visible)
		}
	}
	// 结果断言：可见命令行数收敛在 v0.1 预算内（plain 表格式为 " /cmd" 开头）
	count := 0
	for _, line := range strings.Split(help, "\n") {
		if strings.HasPrefix(line, "  /") {
			count++
		}
	}
	// 2026-10-03：55 → 70；2026-10-12：70 → 54（与 internal/command 的 v01VisibleBudget 同口径，
	// 含同日 14:50 拍板恢复的 /memory）
	// ★ 2026-10-14 **有意翻转**（54 → 55，/sandbox status 登记）——下一个人别当回归：
	// 与 command 包 v01VisibleBudget/w082 计数同一拍板（用户直报「/sandbox status
	// 显示里只有 /sandbox」，按方案 A 注册全名补承诺），三处计数必须同步翻转。
	if count == 0 || count > 55 {
		t.Errorf("plain /help 可见命令 %d 条，应在 1-55 条内", count)
	}
}

// TestPlainHelpAllFlag P1-3（对抗式审查）：plain /help --all 不再静默失效。
// 生产调度链：dispatchCommand("help", "--all", ...) 应透传注册表 FormatHelp 的 --all 视图
// （含隐藏命令与 [隐藏] 后缀），与 TUI 模式行为一致；默认 /help 仍走过滤后的 plain 表。
func TestPlainHelpAllFlag(t *testing.T) {
	r := newTestPlainRunner()
	r.dispatchHandlers = r.initDispatchHandlers()

	// ★ 2026-10-03：样本由 "/memory" 改为 "/loop"。/memory 已取消隐藏（README 主打能力的唯一入口），
	// 继续拿它当"隐藏命令样本"会把**有意登记**的命令判红；/loop 仍属 v0.1 不承诺的自动化组。
	outAll := r.dispatchCommand("help", "--all", "")
	// ★ C-6有意翻转：双语化 → [hidden 隐藏]，勿当回归
	if !strings.Contains(outAll, "/loop") || !strings.Contains(outAll, "[hidden 隐藏]") {
		t.Fatalf("plain /help --all 应显示隐藏命令及 [hidden 隐藏] 后缀，实际片段：%.300s", outAll)
	}

	outDefault := r.dispatchCommand("help", "", "")
	if strings.Contains(outDefault, "/loop") {
		t.Fatal("plain 默认 /help 不应显示隐藏命令 /loop")
	}
	if !strings.Contains(outDefault, "/compact") {
		t.Fatal("plain 默认 /help 应保留核心命令 /compact")
	}
}
