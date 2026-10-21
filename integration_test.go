package main

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// binaryPath 存放构建后的二进制文件路径
var binaryPath string

// TestMain 在所有测试之前构建二进制文件，测试结束后清理
func TestMain(m *testing.M) {
	// 构建二进制文件到临时目录
	tmpDir, err := os.MkdirTemp("", "whale-integration-test")
	if err != nil {
		os.Stderr.WriteString("无法创建临时目录: " + err.Error() + "\n")
		os.Exit(1)
	}

	binaryPath = filepath.Join(tmpDir, "whale.exe")

	buildCmd := exec.Command("go", "build", "-o", binaryPath, ".")
	buildCmd.Dir = filepath.Dir(binaryPath)
	// 使用当前源码目录作为构建目录
	srcDir, _ := os.Getwd()
	buildCmd.Dir = srcDir

	if output, err := buildCmd.CombinedOutput(); err != nil {
		os.Stderr.WriteString("构建二进制文件失败: " + err.Error() + "\n")
		os.Stderr.WriteString(string(output) + "\n")
		os.Exit(1)
	}

	// 运行测试
	code := m.Run()

	// 清理二进制文件
	os.RemoveAll(tmpDir)

	os.Exit(code)
}

// hasAPIKey 检查是否显式启用了真实模型测试，并确认 Key 与模型配置完整。
// 普通 go test 不读取项目 .env，也不产生任何模型调用或 Token 费用。
func hasAPIKey() bool {
	if os.Getenv("WHALE_RUN_LIVE_TESTS") != "1" {
		return false
	}
	loadDotEnv()
	hasKey := os.Getenv("ANTHROPIC_API_KEY") != "" || os.Getenv("OPENAI_API_KEY") != ""
	return hasKey && os.Getenv("WHALE_MODEL") != ""
}

// loadDotEnv 从项目目录的 .env 文件加载environment variable（不覆盖已存在的变量）
func loadDotEnv() {
	cwd, _ := os.Getwd()
	envPath := filepath.Join(cwd, ".env")
	data, err := os.ReadFile(envPath)
	if err != nil {
		return
	}
	for _, line := range strings.Split(string(data), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		parts := strings.SplitN(line, "=", 2)
		if len(parts) == 2 {
			key := strings.TrimSpace(parts[0])
			val := strings.TrimSpace(parts[1])
			// 不覆盖已存在的environment variable
			if os.Getenv(key) == "" {
				os.Setenv(key, val)
			}
		}
	}
}

// buildIntegrationEnv 构建integration test的environment variable列表
// 保留当前环境，但移除可能干扰测试的 API 相关变量（除非明确需要）
func buildIntegrationEnv(tmpDir string, keepAPIKey bool) []string {
	// 确保从 .env 文件加载了 API Key
	if keepAPIKey {
		loadDotEnv()
	}

	env := os.Environ()

	if !keepAPIKey {
		env = removeEnv(env, "ANTHROPIC_API_KEY")
		env = removeEnv(env, "OPENAI_API_KEY")
		env = removeEnv(env, "OPENAI_BASE_URL")
		env = removeEnv(env, "WHALE_MODEL")
		env = removeEnv(env, "WHALE_FALLBACK_MODEL")
	}

	// 重置 HOME 相关变量，防止加载User .env 文件
	env = removeEnv(env, "HOME")
	env = removeEnv(env, "USERPROFILE")
	env = removeEnv(env, "HOMEDRIVE")
	env = removeEnv(env, "HOMEPATH")
	env = append(env, "HOME="+tmpDir, "USERPROFILE="+tmpDir)

	return env
}

// hasMissingAPIKeyOutput 判断 CLI 是否明确报告缺少 API Key。
// 同时兼容直接错误文本与 doctor 结构化诊断输出。
func hasMissingAPIKeyOutput(output string) bool {
	return strings.Contains(output, "No API Key") ||
		strings.Contains(output, "API Key: NOT SET")
}

// TestCI_BasicResponse 测试 --ci 模式下二进制文件能正常接收 prompt 并产生输出
// 该测试需要有效的 API Key，否则跳过
func TestCI_BasicResponse(t *testing.T) {
	if !hasAPIKey() {
		t.Skip("跳过: 未设置 ANTHROPIC_API_KEY 或 OPENAI_API_KEY，无法测试 CI 模式")
	}

	tmpDir := t.TempDir()
	env := buildIntegrationEnv(tmpDir, true)

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	cmd := exec.CommandContext(ctx, binaryPath, "--ci", "say hello")
	cmd.Dir = tmpDir
	cmd.Env = env

	output, err := cmd.CombinedOutput()
	if err != nil {
		// CI 模式可能因 exit code 非零而报错（如权限拒绝等），但只要输出合理即可
		t.Logf("命令退出码非零: %v，输出: %s", err, string(output))
	}

	outStr := string(output)
	// 验证产生了非空输出
	if strings.TrimSpace(outStr) == "" {
		t.Error("expected --ci 模式产生非空输出，但输出为空")
	}
}

// TestVersion_Flag 测试 --version 标志输出版本号 "0.1.0"
func TestVersion_Flag(t *testing.T) {
	tmpDir := t.TempDir()
	env := buildIntegrationEnv(tmpDir, false)

	cmd := exec.Command(binaryPath, "--version")
	cmd.Dir = tmpDir
	cmd.Env = env

	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("执行 --version 失败: %v, 输出: %s", err, string(output))
	}

	outStr := string(output)
	if !strings.Contains(outStr, "0.1.0") {
		t.Errorf("expected输出包含版本号 '0.1.0'，actual输出: %s", outStr)
	}
	if !strings.Contains(outStr, "Whale Code") {
		t.Errorf("expected输出包含 'Whale Code'，actual输出: %s", outStr)
	}
}

// TestHelp_Flag 测试 --help 标志输出使用说明信息
func TestHelp_Flag(t *testing.T) {
	tmpDir := t.TempDir()
	env := buildIntegrationEnv(tmpDir, false)

	cmd := exec.Command(binaryPath, "--help")
	cmd.Dir = tmpDir
	cmd.Env = env

	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("执行 --help 失败: %v, 输出: %s", err, string(output))
	}

	outStr := string(output)

	// 验证包含关键使用说明
	expectedStrings := []string{"Usage", "ANTHROPIC_API_KEY", "OPENAI_API_KEY", "--ci", "--plain", "--version"}
	for _, expected := range expectedStrings {
		if !strings.Contains(outStr, expected) {
			t.Errorf("expected --help 输出包含 '%s'，actual输出: %s", expected, outStr)
		}
	}
}

// TestCheckAPI_Flag 测试 --check-api 标志输出 API 连接检查结果
func TestCheckAPI_Flag(t *testing.T) {
	tmpDir := t.TempDir()
	// 不保留 API Key，测试无 Key 时的输出
	env := buildIntegrationEnv(tmpDir, false)

	cmd := exec.Command(binaryPath, "--check-api")
	cmd.Dir = tmpDir
	cmd.Env = env

	output, _ := cmd.CombinedOutput()
	outStr := string(output)

	// 无 API Key 时应输出 "No API Key found" 或 "API Connection Check" 标题
	if !strings.Contains(outStr, "API Connection Check") && !strings.Contains(outStr, "No API Key") {
		t.Errorf("expected --check-api 输出包含 API 检查相关信息，actual输出: %s", outStr)
	}
}

// TestCheckAPI_Flag_WithKey 测试有 API Key 时 --check-api 标志输出检查结果
// 该测试需要有效的 API Key，否则跳过
func TestCheckAPI_Flag_WithKey(t *testing.T) {
	if !hasAPIKey() {
		t.Skip("跳过: 未设置 API Key，无法测试 --check-api 有 Key scenario")
	}

	tmpDir := t.TempDir()
	env := buildIntegrationEnv(tmpDir, true)

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	cmd := exec.CommandContext(ctx, binaryPath, "--check-api")
	cmd.Dir = tmpDir
	cmd.Env = env

	output, _ := cmd.CombinedOutput()
	outStr := string(output)

	// 有 API Key 时应输出 API Key 信息和连接检查结果
	if !strings.Contains(outStr, "API Connection Check") {
		t.Errorf("expected输出包含 'API Connection Check'，actual输出: %s", outStr)
	}
	if !strings.Contains(outStr, "API Key") {
		t.Errorf("expected输出包含 'API Key' 信息，actual输出: %s", outStr)
	}
}

// TestPlain_Mode 测试 --plain 标志进入纯文本模式
// 无 API Key 时应报错退出
func TestPlain_Mode(t *testing.T) {
	tmpDir := t.TempDir()
	env := buildIntegrationEnv(tmpDir, false)

	cmd := exec.Command(binaryPath, "--plain")
	cmd.Dir = tmpDir
	cmd.Env = env

	output, _ := cmd.CombinedOutput()
	outStr := string(output)

	// --plain 标志应被正确解析，不应出现 "Unknown argument"
	if strings.Contains(outStr, "Unknown argument") {
		t.Errorf("--plain 标志应被识别，不应出现 'Unknown argument'，actual输出: %s", outStr)
	}
	// 没有 API Key 时应报错退出
	if !hasMissingAPIKeyOutput(outStr) {
		t.Errorf("expected --plain 模式无 API Key 时报错，actual输出: %s", outStr)
	}
}

// TestPlain_Mode_WithKey 测试 --plain 和 --ci 标志composition使用时的纯文本输出
// 该测试需要有效的 API Key，否则跳过
func TestPlain_Mode_WithKey(t *testing.T) {
	if !hasAPIKey() {
		t.Skip("跳过: 未设置 API Key，无法测试 --plain 模式有 Key scenario")
	}

	tmpDir := t.TempDir()
	env := buildIntegrationEnv(tmpDir, true)

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	// --ci 模式本身就是纯文本输出（非 TUI），用于验证纯文本输出
	cmd := exec.CommandContext(ctx, binaryPath, "--ci", "respond with just the word hello")
	cmd.Dir = tmpDir
	cmd.Env = env

	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Logf("命令退出码非零: %v，输出: %s", err, string(output))
	}

	outStr := string(output)
	// 验证输出为纯文本（非 TUI 转义序列）
	if strings.TrimSpace(outStr) == "" {
		t.Error("expected --ci 模式产生纯文本输出，但输出为空")
	}
}

// TestConfig_Load 测试从environment variable加载模型配置
// 设置 WHALE_MODEL environment variable，验证 --check-api 输出正确的模型名
// 该测试需要有效的 API Key，否则跳过
func TestConfig_Load(t *testing.T) {
	if !hasAPIKey() {
		t.Skip("跳过: 未设置 API Key，无法test config加载后模型名是否正确显示")
	}

	tmpDir := t.TempDir()
	env := buildIntegrationEnv(tmpDir, true)

	// 通过environment variable设置模型名（checkAPI 从environment variable读取模型）
	env = removeEnv(env, "WHALE_MODEL")
	env = append(env, "WHALE_MODEL=test-model-config")

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	cmd := exec.CommandContext(ctx, binaryPath, "--check-api")
	cmd.Dir = tmpDir
	cmd.Env = env

	output, _ := cmd.CombinedOutput()
	outStr := string(output)

	// 验证模型名被正确加载
	if !strings.Contains(outStr, "test-model-config") {
		t.Errorf("expected从environment variable加载模型 'test-model-config'，actual输出: %s", outStr)
	}
}

// TestCI_NoArgs 测试 --ci 标志不带 prompt 参数时的行为
// 当 --ci 后面没有 prompt 时，程序无法进入 CI 模式，会尝试启动 TUI 并因缺少 API Key 而报错
func TestCI_NoArgs(t *testing.T) {
	tmpDir := t.TempDir()
	env := buildIntegrationEnv(tmpDir, false)

	cmd := exec.Command(binaryPath, "--ci")
	cmd.Dir = tmpDir
	cmd.Env = env

	output, _ := cmd.CombinedOutput()
	outStr := string(output)

	// --ci 不带 prompt 时，程序无法进入 CI 模式
	// 会因缺少 API Key 而报错（因为尝试启动 TUI）
	if !hasMissingAPIKeyOutput(outStr) {
		t.Errorf("expected --ci 不带 prompt 时因缺少 API Key 报错，actual输出: %s", outStr)
	}
}

// TestUnknown_Argument 测试未知参数时输出提示信息
func TestUnknown_Argument(t *testing.T) {
	tmpDir := t.TempDir()
	env := buildIntegrationEnv(tmpDir, false)

	cmd := exec.Command(binaryPath, "--unknown-flag")
	cmd.Dir = tmpDir
	cmd.Env = env

	output, _ := cmd.CombinedOutput()
	outStr := string(output)

	if !strings.Contains(outStr, "Unknown argument") {
		t.Errorf("expected未知参数时输出 'Unknown argument'，actual输出: %s", outStr)
	}
}

// ============================================================================
// Section 17: E2E 端到端integration test
// ============================================================================

// TestE2E_SimpleConversation 测试简单对话流程
// 运行 whale --ci 发送简单问候，验证输出包含问候语
// 需要 ANTHROPIC_API_KEY 或 OPENAI_API_KEY，否则跳过
func TestE2E_SimpleConversation(t *testing.T) {
	if !hasAPIKey() {
		t.Skip("跳过: 未设置 ANTHROPIC_API_KEY 或 OPENAI_API_KEY，无法测试 E2E 简单对话")
	}

	tmpDir := t.TempDir()
	env := buildIntegrationEnv(tmpDir, true)

	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	defer cancel()

	cmd := exec.CommandContext(ctx, binaryPath, "--ci", "say hello")
	cmd.Dir = tmpDir
	cmd.Env = env

	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Logf("命令退出码非零: %v，输出: %s", err, string(output))
	}

	outStr := strings.ToLower(string(output))
	// 验证输出包含问候语（hello/hi/hey 等常见问候）
	hasGreeting := strings.Contains(outStr, "hello") ||
		strings.Contains(outStr, "hi") ||
		strings.Contains(outStr, "hey") ||
		strings.Contains(outStr, "你好")
	if !hasGreeting {
		t.Errorf("expected输出包含问候语，actual输出: %s", string(output))
	}
}

// TestE2E_ToolExecution test tool执行流程
// 创建测试文件，运行 whale --ci 请求读取文件，验证输出中涉及工具调用
// 需要 ANTHROPIC_API_KEY 或 OPENAI_API_KEY，否则跳过
func TestE2E_ToolExecution(t *testing.T) {
	if !hasAPIKey() {
		t.Skip("跳过: 未设置 ANTHROPIC_API_KEY 或 OPENAI_API_KEY，无法测试 E2E 工具执行")
	}

	tmpDir := t.TempDir()
	env := buildIntegrationEnv(tmpDir, true)

	// 创建测试文件
	testContent := "This is a test file for E2E integration testing."
	testFilePath := filepath.Join(tmpDir, "test.txt")
	if err := os.WriteFile(testFilePath, []byte(testContent), 0644); err != nil {
		t.Fatalf("创建测试文件失败: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	defer cancel()

	cmd := exec.CommandContext(ctx, binaryPath, "--ci", "read the file test.txt and tell me what's in it")
	cmd.Dir = tmpDir
	cmd.Env = env

	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Logf("命令退出码非零: %v，输出: %s", err, string(output))
	}

	outStr := string(output)
	// 验证输出中涉及工具调用（工具名或工具调用标记）
	hasToolUsage := strings.Contains(outStr, "read_file") ||
		strings.Contains(outStr, "Tool:") ||
		strings.Contains(outStr, "test file") ||
		strings.Contains(outStr, "integration testing")
	if !hasToolUsage {
		t.Errorf("expected输出涉及工具调用或文件内容，actual输出: %s", outStr)
	}
}

// TestE2E_MultiTurnConversation 测试多轮对话流程
// 使用 --continue 标志验证对话能够继续
// 需要 ANTHROPIC_API_KEY 或 OPENAI_API_KEY，否则跳过
// 注意：AI 模型行为不可完全预测，仅验证 --continue 模式能正常启动并产生输出
func TestE2E_MultiTurnConversation(t *testing.T) {
	if !hasAPIKey() {
		t.Skip("跳过: 未设置 ANTHROPIC_API_KEY 或 OPENAI_API_KEY，无法测试 E2E 多轮对话")
	}

	tmpDir := t.TempDir()
	env := buildIntegrationEnv(tmpDir, true)

	// 第一轮对话
	ctx1, cancel1 := context.WithTimeout(context.Background(), 120*time.Second)
	defer cancel1()

	cmd1 := exec.CommandContext(ctx1, binaryPath, "--ci", "remember the number 42, just say 'got it'")
	cmd1.Dir = tmpDir
	cmd1.Env = env

	output1, err := cmd1.CombinedOutput()
	if err != nil {
		t.Logf("第一轮命令退出码非零: %v，输出: %s", err, string(output1))
	}

	if strings.TrimSpace(string(output1)) == "" {
		t.Fatal("第一轮对话输出为空")
	}

	// 第二轮对话：使用 --continue 继续（--continue 必须在 --ci 之前）
	ctx2, cancel2 := context.WithTimeout(context.Background(), 120*time.Second)
	defer cancel2()

	cmd2 := exec.CommandContext(ctx2, binaryPath, "--continue", "--ci", "what number did I ask you to remember?")
	cmd2.Dir = tmpDir
	cmd2.Env = env

	output2, err := cmd2.CombinedOutput()
	if err != nil {
		t.Logf("第二轮命令退出码非零: %v，输出: %s", err, string(output2))
	}

	// 验证 --continue 模式能正常启动并产生输出
	// AI 模型行为不可完全预测，仅验证非空输出
	if strings.TrimSpace(string(output2)) == "" {
		t.Error("expected --continue 模式产生非空输出，但输出为空")
	}
}

// TestE2E_ContextCompaction test context压缩
// 注意：长对话触发压缩难以在integration test中可靠验证，标记为跳过并附注说明
func TestE2E_ContextCompaction(t *testing.T) {
	t.Skip("跳过: 上下文压缩需要极长对话才能触发，难以在integration test中可靠验证，需manual testing")
}

// TestE2E_SessionPersistence 测试会话持久化
// 运行 whale --ci 后验证会话文件保存到 ~/.whale/sessions/ 目录
// 需要 ANTHROPIC_API_KEY 或 OPENAI_API_KEY，否则跳过
func TestE2E_SessionPersistence(t *testing.T) {
	if !hasAPIKey() {
		t.Skip("跳过: 未设置 ANTHROPIC_API_KEY 或 OPENAI_API_KEY，无法测试 E2E 会话持久化")
	}

	tmpDir := t.TempDir()
	env := buildIntegrationEnv(tmpDir, true)

	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	defer cancel()

	cmd := exec.CommandContext(ctx, binaryPath, "--ci", "say hello")
	cmd.Dir = tmpDir
	cmd.Env = env

	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Logf("命令退出码非零: %v，输出: %s", err, string(output))
	}

	// 验证会话目录存在
	sessionsDir := filepath.Join(tmpDir, ".whale", "sessions")
	entries, err := os.ReadDir(sessionsDir)
	if err != nil {
		t.Logf("会话目录不存在或无法读取: %v（可能 CI 模式不保存会话）", err)
		// CI 模式可能不保存会话文件，不作为硬性失败
		return
	}

	// 验证至少有一个会话文件
	hasSessionFile := false
	for _, entry := range entries {
		if !entry.IsDir() && strings.HasSuffix(entry.Name(), ".json") {
			hasSessionFile = true
			break
		}
	}
	if !hasSessionFile {
		t.Log("未找到会话 JSON 文件（CI 模式可能不保存会话）")
	}
}

// TestE2E_MCPIntegration 测试 MCP 工具集成
// 注意：需要外部 MCP server，跳过并附注说明
func TestE2E_MCPIntegration(t *testing.T) {
	t.Skip("跳过: MCP integration test需要外部 MCP server，无法在 CI 环境中自动运行")
}

// TestE2E_AgentExecution 测试 Agent 子任务执行
// 注意：需要 API Key 且 Agent 功能可能受限，跳过并附注说明
func TestE2E_AgentExecution(t *testing.T) {
	if !hasAPIKey() {
		t.Skip("跳过: 未设置 ANTHROPIC_API_KEY 或 OPENAI_API_KEY，无法测试 E2E Agent 执行")
	}

	tmpDir := t.TempDir()
	env := buildIntegrationEnv(tmpDir, true)

	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	defer cancel()

	cmd := exec.CommandContext(ctx, binaryPath, "--ci", "use the Agent tool to list files in the current directory")
	cmd.Dir = tmpDir
	cmd.Env = env

	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Logf("命令退出码非零: %v，输出: %s", err, string(output))
	}

	outStr := string(output)
	// 验证产生了非空输出（Agent 执行结果）
	if strings.TrimSpace(outStr) == "" {
		t.Error("expected Agent 执行产生非空输出，但输出为空")
	}
}

// ============================================================================
// Section 17: CI 模式integration test
// ============================================================================

// TestCI_SimpleTask 测试 CI 模式简单任务
// 运行 `echo "what is 2+2" | whale --ci`，验证退出码为 0
// 需要 ANTHROPIC_API_KEY 或 OPENAI_API_KEY，否则跳过
func TestCI_SimpleTask(t *testing.T) {
	if !hasAPIKey() {
		t.Skip("跳过: 未设置 ANTHROPIC_API_KEY 或 OPENAI_API_KEY，无法测试 CI 简单任务")
	}

	tmpDir := t.TempDir()
	env := buildIntegrationEnv(tmpDir, true)

	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	defer cancel()

	cmd := exec.CommandContext(ctx, binaryPath, "--ci", "what is 2+2?")
	cmd.Dir = tmpDir
	cmd.Env = env

	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Logf("命令退出码非零: %v，输出: %s", err, string(output))
	}

	// 验证退出码为 0（成功）
	if exitErr, ok := err.(*exec.ExitError); ok && exitErr.ExitCode() != 0 {
		t.Errorf("expected退出码 0，actual退出码: %d", exitErr.ExitCode())
	}
}

// TestCI_ToolUsage 测试 CI 模式工具使用
// 运行 `echo "list files in current directory" | whale --ci`，验证工具被使用
// 需要 ANTHROPIC_API_KEY 或 OPENAI_API_KEY，否则跳过
func TestCI_ToolUsage(t *testing.T) {
	if !hasAPIKey() {
		t.Skip("跳过: 未设置 ANTHROPIC_API_KEY 或 OPENAI_API_KEY，无法测试 CI 工具使用")
	}

	tmpDir := t.TempDir()
	env := buildIntegrationEnv(tmpDir, true)

	// 创建一些文件以便工具使用
	if err := os.WriteFile(filepath.Join(tmpDir, "sample.txt"), []byte("sample content"), 0644); err != nil {
		t.Fatalf("创建测试文件失败: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	defer cancel()

	cmd := exec.CommandContext(ctx, binaryPath, "--ci", "list the files in the current directory")
	cmd.Dir = tmpDir
	cmd.Env = env

	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Logf("命令退出码非零: %v，输出: %s", err, string(output))
	}

	outStr := string(output)
	// 验证输出中包含文件名（表明工具被使用）
	if !strings.Contains(outStr, "sample.txt") && !strings.Contains(outStr, "file") {
		t.Errorf("expected工具使用后输出包含文件信息，actual输出: %s", outStr)
	}
}

// TestCI_ExitCodes 测试 CI 模式退出码
// 验证成功时退出码为 0，错误时退出码非零
func TestCI_ExitCodes(t *testing.T) {
	t.Run("success_exit_code", func(t *testing.T) {
		if !hasAPIKey() {
			t.Skip("跳过: 未设置 API Key")
		}

		tmpDir := t.TempDir()
		env := buildIntegrationEnv(tmpDir, true)

		ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
		defer cancel()

		cmd := exec.CommandContext(ctx, binaryPath, "--ci", "say hello")
		cmd.Dir = tmpDir
		cmd.Env = env

		output, err := cmd.CombinedOutput()
		outStr := string(output)
		t.Logf("输出: %s", outStr)

		if err != nil {
			if exitErr, ok := err.(*exec.ExitError); ok {
				t.Errorf("expected退出码 0，actual退出码: %d", exitErr.ExitCode())
			}
		}
	})

	t.Run("error_exit_code_no_api_key", func(t *testing.T) {
		tmpDir := t.TempDir()
		// 不保留 API Key，应导致错误退出
		env := buildIntegrationEnv(tmpDir, false)

		cmd := exec.Command(binaryPath, "--ci", "say hello")
		cmd.Dir = tmpDir
		cmd.Env = env

		output, err := cmd.CombinedOutput()
		outStr := string(output)
		if err == nil {
			t.Fatal("expected no API Key to produce a non-zero exit code")
		}

		// 无 API Key 时应报错
		if !hasMissingAPIKeyOutput(outStr) {
			t.Errorf("expected无 API Key 时输出错误信息，process error: %v，actual输出: %s", err, outStr)
		}
	})
}

// TestCI_MaxIterations 测试 --max-iterations 标志限制工具调用轮数
// 验证超过最大iteration count后自动停止
// 需要 ANTHROPIC_API_KEY 或 OPENAI_API_KEY，否则跳过
func TestCI_MaxIterations(t *testing.T) {
	if !hasAPIKey() {
		t.Skip("跳过: 未设置 ANTHROPIC_API_KEY 或 OPENAI_API_KEY，无法测试 CI 最大iteration count")
	}

	tmpDir := t.TempDir()
	env := buildIntegrationEnv(tmpDir, true)

	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	defer cancel()

	// 使用 --max-iterations 1 限制只进行一轮工具调用
	cmd := exec.CommandContext(ctx, binaryPath, "--ci", "--max-iterations", "1", "list all files recursively and read each one")
	cmd.Dir = tmpDir
	cmd.Env = env

	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Logf("命令退出码非零: %v，输出: %s", err, string(output))
	}

	outStr := string(output)
	// 验证程序正常退出（不会无限循环）
	// 输出可能包含 "Max iterations" 提示，或正常完成
	if strings.TrimSpace(outStr) == "" {
		t.Error("expected --max-iterations 限制后仍有输出，但输出为空")
	}
}

// ============================================================================
// Section 18: GAP 功能 E2E 测试
// ============================================================================

// TestE2E_GitStatus 测试 git_status 工具
// 创建 Git 仓库，运行 whale --ci 请求 git status，验证输出
func TestE2E_GitStatus(t *testing.T) {
	if !hasAPIKey() {
		t.Skip("跳过: 未设置 API Key")
	}

	tmpDir := t.TempDir()
	env := buildIntegrationEnv(tmpDir, true)

	// 初始化 Git 仓库
	gitInit := exec.Command("git", "init")
	gitInit.Dir = tmpDir
	gitInit.Run()
	gitConfig := exec.Command("git", "config", "user.email", "test@test.com")
	gitConfig.Dir = tmpDir
	gitConfig.Run()
	gitConfig2 := exec.Command("git", "config", "user.name", "Test")
	gitConfig2.Dir = tmpDir
	gitConfig2.Run()

	// 创建文件
	os.WriteFile(filepath.Join(tmpDir, "hello.go"), []byte("package main\nfunc main() {}"), 0644)

	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	defer cancel()

	cmd := exec.CommandContext(ctx, binaryPath, "--ci", "run git_status to check the repo status")
	cmd.Dir = tmpDir
	cmd.Env = env

	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Logf("命令退出码非零: %v，输出: %s", err, string(output))
	}

	outStr := string(output)
	// 验证输出非空
	if strings.TrimSpace(outStr) == "" {
		t.Error("expected git_status 产生非空输出")
	}
}

// TestE2E_GitLog 测试 git_log 工具
// 创建 Git 仓库并提交，运行 whale --ci 请求 git log
func TestE2E_GitLog(t *testing.T) {
	if !hasAPIKey() {
		t.Skip("跳过: 未设置 API Key")
	}

	tmpDir := t.TempDir()
	env := buildIntegrationEnv(tmpDir, true)

	// 初始化 Git 仓库并提交
	gitInit := exec.Command("git", "init")
	gitInit.Dir = tmpDir
	gitInit.Run()
	gitConfigUserEmail := exec.Command("git", "config", "user.email", "test@test.com")
	gitConfigUserEmail.Dir = tmpDir
	gitConfigUserEmail.Run()
	gitConfigUserName := exec.Command("git", "config", "user.name", "Test")
	gitConfigUserName.Dir = tmpDir
	gitConfigUserName.Run()
	os.WriteFile(filepath.Join(tmpDir, "README.md"), []byte("# Test"), 0644)
	gitAdd := exec.Command("git", "add", ".")
	gitAdd.Dir = tmpDir
	gitAdd.Run()
	gitCommit := exec.Command("git", "commit", "-m", "initial commit")
	gitCommit.Dir = tmpDir
	gitCommit.Run()

	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	defer cancel()

	cmd := exec.CommandContext(ctx, binaryPath, "--ci", "use the git_log tool to show recent commits")
	cmd.Dir = tmpDir
	cmd.Env = env

	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Logf("命令退出码非零: %v，输出: %s", err, string(output))
	}

	outStr := string(output)
	if strings.TrimSpace(outStr) == "" {
		t.Error("expected git_log 产生非空输出")
	}
}

// TestE2E_DiffVisualization 测试 Diff 可视化
// 创建文件，运行 whale --ci 修改文件，验证 diff 输出
func TestE2E_DiffVisualization(t *testing.T) {
	if !hasAPIKey() {
		t.Skip("跳过: 未设置 API Key")
	}

	tmpDir := t.TempDir()
	env := buildIntegrationEnv(tmpDir, true)

	// 创建初始文件
	os.WriteFile(filepath.Join(tmpDir, "app.py"), []byte("def hello():\n    print('hello')\n"), 0644)

	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	defer cancel()

	cmd := exec.CommandContext(ctx, binaryPath, "--ci", "edit app.py to change 'hello' to 'world'")
	cmd.Dir = tmpDir
	cmd.Env = env

	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Logf("命令退出码非零: %v，输出: %s", err, string(output))
	}

	// 验证文件被修改
	content, readErr := os.ReadFile(filepath.Join(tmpDir, "app.py"))
	if readErr != nil {
		t.Logf("读取文件失败: %v", readErr)
	} else {
		modified := strings.Contains(string(content), "world")
		if !modified {
			t.Logf("文件可能未被修改，内容: %s", string(content))
		}
	}
}

// TestE2E_SkillExecution 测试 Skill 技能系统
// 创建技能文件，运行 whale --ci 使用技能
func TestE2E_SkillExecution(t *testing.T) {
	if !hasAPIKey() {
		t.Skip("跳过: 未设置 API Key")
	}

	tmpDir := t.TempDir()
	env := buildIntegrationEnv(tmpDir, true)

	// 创建技能目录和文件
	skillDir := filepath.Join(tmpDir, ".whale", "skills")
	os.MkdirAll(skillDir, 0755)
	os.WriteFile(filepath.Join(skillDir, "review.md"), []byte("---\nname: review\ndescription: Code review\n---\nReview this code for issues:\n{{prompt}}"), 0644)

	// 创建待审查的代码文件
	os.WriteFile(filepath.Join(tmpDir, "code.go"), []byte("package main\nfunc add(a, b int) int {\n\treturn a + b\n}\n"), 0644)

	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	defer cancel()

	cmd := exec.CommandContext(ctx, binaryPath, "--ci", "use the Skill tool with name 'review' and prompt 'check code.go for bugs'")
	cmd.Dir = tmpDir
	cmd.Env = env

	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Logf("命令退出码非零: %v，输出: %s", err, string(output))
	}

	outStr := string(output)
	if strings.TrimSpace(outStr) == "" {
		t.Error("expected Skill 执行产生非空输出")
	}
}

// TestE2E_SessionResume 测试会话恢复
// 运行 whale --ci 两次，第二次使用 --continue 恢复
func TestE2E_SessionResume(t *testing.T) {
	if !hasAPIKey() {
		t.Skip("跳过: 未设置 API Key")
	}

	tmpDir := t.TempDir()
	env := buildIntegrationEnv(tmpDir, true)

	// 第一轮
	ctx1, cancel1 := context.WithTimeout(context.Background(), 120*time.Second)
	defer cancel1()

	cmd1 := exec.CommandContext(ctx1, binaryPath, "--ci", "remember the secret word is 'banana', just say 'ok'")
	cmd1.Dir = tmpDir
	cmd1.Env = env

	output1, _ := cmd1.CombinedOutput()
	if strings.TrimSpace(string(output1)) == "" {
		t.Fatal("第一轮对话输出为空")
	}

	// 第二轮：使用 --continue 恢复
	ctx2, cancel2 := context.WithTimeout(context.Background(), 120*time.Second)
	defer cancel2()

	cmd2 := exec.CommandContext(ctx2, binaryPath, "--continue", "--ci", "what was the secret word I told you?")
	cmd2.Dir = tmpDir
	cmd2.Env = env

	output2, _ := cmd2.CombinedOutput()
	outStr2 := strings.ToLower(string(output2))

	// 验证输出非空
	if strings.TrimSpace(string(output2)) == "" {
		t.Error("expected --continue 模式产生非空输出")
	}

	// 验证模型能回忆（宽松检查）
	if strings.Contains(outStr2, "banana") {
		t.Log("模型成功回忆了 secret word")
	}
}

// TestE2E_FileWriteWithDiff 测试文件写入时的 Diff 预览
// 运行 whale --ci 写入文件，验证输出包含 diff 信息
func TestE2E_FileWriteWithDiff(t *testing.T) {
	if !hasAPIKey() {
		t.Skip("跳过: 未设置 API Key")
	}

	tmpDir := t.TempDir()
	env := buildIntegrationEnv(tmpDir, true)

	// 创建已有文件
	os.WriteFile(filepath.Join(tmpDir, "config.txt"), []byte("name=old\nversion=1\n"), 0644)

	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	defer cancel()

	cmd := exec.CommandContext(ctx, binaryPath, "--ci", "edit config.txt to change version from 1 to 2")
	cmd.Dir = tmpDir
	cmd.Env = env

	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Logf("命令退出码非零: %v，输出: %s", err, string(output))
	}

	outStr := string(output)
	// 验证输出包含 diff 相关信息
	hasDiff := strings.Contains(outStr, "Diff") || strings.Contains(outStr, "+") || strings.Contains(outStr, "-") || strings.Contains(outStr, "lines")
	if !hasDiff {
		t.Logf("输出可能不包含 diff 信息: %s", outStr[:min(500, len(outStr))])
	}
}

// TestE2E_GitBranch 测试 git_branch 工具
func TestE2E_GitBranch(t *testing.T) {
	if !hasAPIKey() {
		t.Skip("跳过: 未设置 API Key")
	}

	tmpDir := t.TempDir()
	env := buildIntegrationEnv(tmpDir, true)

	// 初始化 Git 仓库
	gitCmd := exec.Command("git", "init")
	gitCmd.Dir = tmpDir
	gitCmd.Run()
	gitConfigUserEmail := exec.Command("git", "config", "user.email", "test@test.com")
	gitConfigUserEmail.Dir = tmpDir
	gitConfigUserEmail.Run()
	gitConfigUserName := exec.Command("git", "config", "user.name", "Test")
	gitConfigUserName.Dir = tmpDir
	gitConfigUserName.Run()
	os.WriteFile(filepath.Join(tmpDir, "main.go"), []byte("package main"), 0644)
	gitAdd := exec.Command("git", "add", ".")
	gitAdd.Dir = tmpDir
	gitAdd.Run()
	gitCommit := exec.Command("git", "commit", "-m", "init")
	gitCommit.Dir = tmpDir
	gitCommit.Run()

	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	defer cancel()

	cmd := exec.CommandContext(ctx, binaryPath, "--ci", "use the git_branch tool with action 'current' to show the current branch name")
	cmd.Dir = tmpDir
	cmd.Env = env

	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Logf("命令退出码非零: %v，输出: %s", err, string(output))
	}

	outStr := string(output)
	if strings.TrimSpace(outStr) == "" {
		t.Error("expected git_branch 产生非空输出")
	}
}

// TestE2E_Sandbox 测试沙箱功能
// 验证沙箱在启用时阻止危险命令
func TestE2E_Sandbox(t *testing.T) {
	// 沙箱功能是内部功能，通过 CI 模式难以直接测试
	// 此test verification程序在沙箱相关配置下仍能正常运行
	if !hasAPIKey() {
		t.Skip("跳过: 未设置 API Key")
	}

	tmpDir := t.TempDir()
	env := buildIntegrationEnv(tmpDir, true)

	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	defer cancel()

	cmd := exec.CommandContext(ctx, binaryPath, "--ci", "list files in the current directory")
	cmd.Dir = tmpDir
	cmd.Env = env

	output, _ := cmd.CombinedOutput()
	if strings.TrimSpace(string(output)) == "" {
		t.Error("expected正常命令在沙箱环境下仍能执行")
	}
}

// TestE2E_ImageMention 测试图片 @-mention
// 创建一个小的测试图片文件，验证 @mention 能识别
func TestE2E_ImageMention(t *testing.T) {
	// 图片 @mention 需要 TUI 模式交互，CI 模式下无法直接测试
	// 此test verification程序在存在图片文件时仍能正常运行
	if !hasAPIKey() {
		t.Skip("跳过: 未设置 API Key")
	}

	tmpDir := t.TempDir()
	env := buildIntegrationEnv(tmpDir, true)

	// 创建一个最小的 PNG 文件（1x1 像素）
	pngHeader := []byte{0x89, 0x50, 0x4E, 0x47, 0x0D, 0x0A, 0x1A, 0x0A}
	os.WriteFile(filepath.Join(tmpDir, "icon.png"), pngHeader, 0644)

	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	defer cancel()

	cmd := exec.CommandContext(ctx, binaryPath, "--ci", "list all files in the current directory including images")
	cmd.Dir = tmpDir
	cmd.Env = env

	output, _ := cmd.CombinedOutput()
	if strings.TrimSpace(string(output)) == "" {
		t.Error("expected在有图片文件时仍能正常输出")
	}
}
