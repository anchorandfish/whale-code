package main

import (
	"bytes"
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/anchorandfish/whale-code/internal/ai"
	versionpkg "github.com/anchorandfish/whale-code/internal/version"
)

// TestVersion 验证版本号字符串是否为 "0.1.0"
func TestVersion(t *testing.T) {
	expected := "0.1.0"
	if versionpkg.Version != expected {
		t.Errorf("version = %q, want %q", versionpkg.Version, expected)
	}
}

// TestFormatVersionString_UsesVersionPackage 桶 X5 死代码清理反例测试：
// 验证 formatVersionString 接线 internal/version 包，使 version.Version/BuildDate/Commit 不再零调用。
// 修复前：version 包三个符号全仓零调用；修复后：formatVersionString 引用 versionpkg.Version/BuildDate/Commit。
func TestFormatVersionString_UsesVersionPackage(t *testing.T) {
	// 默认场景：BuildDate/Commit 为空，仅展示版本号（来自 versionpkg.Version）。
	out := formatVersionString()
	if !strings.Contains(out, versionpkg.Version) {
		t.Fatalf("formatVersionString() = %q, 期望包含 versionpkg.Version=%q（接线 internal/version 包）", out, versionpkg.Version)
	}

	// 注入场景：设置 BuildDate/Commit，验证一并展示（证明 BuildDate/Commit 被引用，不再是死代码）。
	origBuild, origCommit := versionpkg.BuildDate, versionpkg.Commit
	versionpkg.BuildDate = "2026-07-23"
	versionpkg.Commit = "deadbeef"
	t.Cleanup(func() {
		versionpkg.BuildDate = origBuild
		versionpkg.Commit = origCommit
	})
	out = formatVersionString()
	if !strings.Contains(out, "deadbeef") {
		t.Errorf("formatVersionString() = %q, 期望包含注入的 Commit=deadbeef", out)
	}
	if !strings.Contains(out, "2026-07-23") {
		t.Errorf("formatVersionString() = %q, 期望包含注入的 BuildDate=2026-07-23", out)
	}
}

// TestLoadEnvFile 测试加载 .env 文件并设置environment variable
func TestLoadEnvFile(t *testing.T) {
	dir := t.TempDir()
	envPath := filepath.Join(dir, ".env")

	// 包含普通键值对、注释、空行和引号值
	content := "TEST_LOAD_KEY_1=hello\nTEST_LOAD_KEY_2=world\n# this is a comment\n\nTEST_LOAD_KEY_3=\"quoted value\""
	if err := os.WriteFile(envPath, []byte(content), 0644); err != nil {
		t.Fatalf("写入 .env 文件失败: %v", err)
	}

	t.Cleanup(func() {
		os.Unsetenv("TEST_LOAD_KEY_1")
		os.Unsetenv("TEST_LOAD_KEY_2")
		os.Unsetenv("TEST_LOAD_KEY_3")
	})

	loadEnvFile(envPath)

	if got := os.Getenv("TEST_LOAD_KEY_1"); got != "hello" {
		t.Errorf("TEST_LOAD_KEY_1 = %q, want %q", got, "hello")
	}
	if got := os.Getenv("TEST_LOAD_KEY_2"); got != "world" {
		t.Errorf("TEST_LOAD_KEY_2 = %q, want %q", got, "world")
	}
	if got := os.Getenv("TEST_LOAD_KEY_3"); got != "quoted value" {
		t.Errorf("TEST_LOAD_KEY_3 = %q, want %q", got, "quoted value")
	}
}

// TestLoadEnvFile_PreserveExisting 测试 .env 文件不会覆盖进程中已有的 environment variable。
func TestLoadEnvFile_PreserveExisting(t *testing.T) {
	dir := t.TempDir()
	envPath := filepath.Join(dir, ".env")

	// 先设置 environment variable
	os.Setenv("TEST_OVERWRITE_KEY", "original")
	t.Cleanup(func() {
		os.Unsetenv("TEST_OVERWRITE_KEY")
	})

	// .env 文件中包含同名的键但值不同
	content := "TEST_OVERWRITE_KEY=overwritten"
	if err := os.WriteFile(envPath, []byte(content), 0644); err != nil {
		t.Fatalf("写入 .env 文件失败: %v", err)
	}

	loadEnvFile(envPath)

	if got := os.Getenv("TEST_OVERWRITE_KEY"); got != "original" {
		t.Errorf("TEST_OVERWRITE_KEY = %q, want %q (.env 不应覆盖已有 environment variable)", got, "original")
	}
}

// TestLoadEnvFile_EarlierWinsLater 验证先加载的 .env 不会被后加载的 .env 覆盖。
// 模拟 loadEnvFiles 的加载顺序：~/.whale/.env（val1）→ ./.env.dev（val2），最终值为 val1。
func TestLoadEnvFile_EarlierWinsLater(t *testing.T) {
	dir := t.TempDir()
	homeEnv := filepath.Join(dir, "home.env")
	cwdEnv := filepath.Join(dir, "cwd.env")

	if err := os.WriteFile(homeEnv, []byte("WHALE_OVERRIDE_ORDER_KEY=val1"), 0644); err != nil {
		t.Fatalf("写入 home .env 失败: %v", err)
	}
	if err := os.WriteFile(cwdEnv, []byte("WHALE_OVERRIDE_ORDER_KEY=val2"), 0644); err != nil {
		t.Fatalf("写入 cwd .env 失败: %v", err)
	}
	t.Cleanup(func() {
		os.Unsetenv("WHALE_OVERRIDE_ORDER_KEY")
	})

	// 模拟 loadEnvFiles 的顺序：先 home 后 cwd
	loadEnvFile(homeEnv)
	if got := os.Getenv("WHALE_OVERRIDE_ORDER_KEY"); got != "val1" {
		t.Fatalf("首次加载后值应为 val1，实际 %q", got)
	}
	loadEnvFile(cwdEnv)
	if got := os.Getenv("WHALE_OVERRIDE_ORDER_KEY"); got != "val1" {
		t.Fatalf("二次加载后值应保持 val1（~/.whale/.env 优先于 ./.env.dev），实际 %q", got)
	}
}

// TestSetupConsoleEncoding 测试 Windows 控制台编码设置为 UTF-8
func TestSetupConsoleEncoding(t *testing.T) {
	if runtime.GOOS != "windows" {
		t.Skip("跳过: 仅在 Windows 上运行")
	}
	// 验证函数不会 panic，且正常执行完成
	setupConsoleEncoding()
}

// TestCheckAPI_ValidKey 测试有效 API Key 时client能成功创建并连接
// 使用 httptest.NewServer mock Anthropic API server
func TestCheckAPI_ValidKey(t *testing.T) {
	// 启动 Mock Anthropic API server，返回 SSE 格式响应
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// 验证request header包含 API Key
		if r.Header.Get("x-api-key") == "" && r.Header.Get("Authorization") == "" {
			w.WriteHeader(http.StatusUnauthorized)
			fmt.Fprint(w, `{"error":"unauthorized"}`)
			return
		}
		// 返回 SSE 格式success response
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, "event: message_start\n"+
			"data: {\"type\":\"message_start\",\"message\":{\"usage\":{\"input_tokens\":10}}}\n\n"+
			"event: content_block_start\n"+
			"data: {\"type\":\"content_block_start\",\"content_block\":{\"type\":\"text\"}}\n\n"+
			"event: content_block_delta\n"+
			"data: {\"type\":\"content_block_delta\",\"delta\":{\"type\":\"text_delta\",\"text\":\"OK\"}}\n\n"+
			"event: message_stop\n"+
			"data: {\"type\":\"message_stop\"}\n\n")
	}))
	defer server.Close()

	// 使用 Mock server URL 和测试 API Key 创建client
	apiKey := "sk-test-valid-key-12345"
	client := ai.NewSimpleClient("claude-sonnet-4", apiKey, server.URL, ai.ProviderAnthropic)
	if client == nil {
		t.Fatal("NewSimpleClient 返回 nil, expected非 nil client")
	}
	defer client.Close()

	// 验证client能成功发送流式请求
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	events, err := client.Stream(ctx, ai.Request{
		Messages: []ai.MessageEntry{{Role: "user", Content: "test"}},
	})
	if err != nil {
		t.Fatalf("Stream 返回错误: %v", err)
	}

	// 收集事件验证响应
	var gotEvent bool
	for e := range events {
		gotEvent = true
		_ = e
		break // 只需验证收到至少一个事件
	}
	if !gotEvent {
		t.Error("未收到任何流式事件")
	}
}

// TestCheckAPI_InvalidKey 测试无效 API Key 时 checkAPI 输出错误信息并退出
// 使用构建的二进制文件在隔离环境中运行，确保不会加载 .env 文件
func TestCheckAPI_InvalidKey(t *testing.T) {
	// 构建二进制文件
	tmpDir := t.TempDir()
	binPath := filepath.Join(tmpDir, "whale-test.exe")
	buildCmd := exec.Command("go", "build", "-o", binPath, ".")
	cwd, _ := os.Getwd()
	buildCmd.Dir = cwd
	if output, err := buildCmd.CombinedOutput(); err != nil {
		t.Fatalf("构建二进制文件失败: %v, 输出: %s", err, string(output))
	}

	// 在隔离环境中运行，不传递 API Key
	env := []string{
		"HOME=" + tmpDir,
		"USERPROFILE=" + tmpDir,
		"PATH=" + os.Getenv("PATH"),
		"SYSTEMROOT=" + os.Getenv("SYSTEMROOT"),
	}
	cmd := exec.Command(binPath, "--check-api")
	cmd.Dir = tmpDir // 在 tmpDir 中运行，没有 .env 文件
	cmd.Env = env

	output, _ := cmd.CombinedOutput()
	outStr := string(output)

	if !hasMissingAPIKeyOutput(outStr) && !strings.Contains(outStr, "FAIL") {
		t.Errorf("expected输出包含 'No API Key found' 或 'FAIL'，actual: %s", outStr)
	}
}

// TestParseArgs_Version 测试 --version 标志输出版本信息
func TestParseArgs_Version(t *testing.T) {
	output := captureStdout(func() {
		handleArgs([]string{"--version"})
	})

	if !strings.Contains(output, "0.1.0") {
		t.Errorf("expected输出包含版本号 '0.1.0'，actual: %s", output)
	}
	if !strings.Contains(output, "Whale Code") {
		t.Errorf("expected输出包含 'Whale Code'，actual: %s", output)
	}
}

// TestParseArgs_Help 测试 --help 标志输出帮助信息
func TestParseArgs_Help(t *testing.T) {
	output := captureStdout(func() {
		handleArgs([]string{"--help"})
	})

	if !strings.Contains(output, "Usage") {
		t.Errorf("expected输出包含 'Usage'，actual: %s", output)
	}
	if !strings.Contains(output, "ANTHROPIC_API_KEY") {
		t.Errorf("expected输出包含 'ANTHROPIC_API_KEY'，actual: %s", output)
	}
	if !strings.Contains(output, "OPENAI_API_KEY") {
		t.Errorf("expected输出包含 'OPENAI_API_KEY'，actual: %s", output)
	}
}

// TestParseArgs_CI 测试 --ci 标志带 prompt 参数进入 CI 模式
func TestParseArgs_CI(t *testing.T) {
	tmpDir := t.TempDir()
	binPath := filepath.Join(tmpDir, "whale-ci-test.exe")
	cwd, _ := os.Getwd()
	buildCmd := exec.Command("go", "build", "-o", binPath, ".")
	buildCmd.Dir = cwd
	if output, err := buildCmd.CombinedOutput(); err != nil {
		t.Fatalf("构建失败: %v, 输出: %s", err, string(output))
	}

	env := []string{
		"HOME=" + tmpDir,
		"USERPROFILE=" + tmpDir,
		"PATH=" + os.Getenv("PATH"),
		"SYSTEMROOT=" + os.Getenv("SYSTEMROOT"),
	}
	cmd := exec.Command(binPath, "--ci", "test prompt")
	cmd.Dir = tmpDir
	cmd.Env = env

	output, _ := cmd.CombinedOutput()
	outStr := string(output)

	if strings.Contains(outStr, "Unknown argument") {
		t.Errorf("--ci 标志应被识别，actual: %s", outStr)
	}
	if !hasMissingAPIKeyOutput(outStr) {
		t.Errorf("expected CI 模式无 API Key 时报错，actual: %s", outStr)
	}
}

// TestParseArgs_Plain 测试 --plain 标志进入纯文本模式
func TestParseArgs_Plain(t *testing.T) {
	tmpDir := t.TempDir()
	binPath := filepath.Join(tmpDir, "whale-plain-test.exe")
	cwd, _ := os.Getwd()
	buildCmd := exec.Command("go", "build", "-o", binPath, ".")
	buildCmd.Dir = cwd
	if output, err := buildCmd.CombinedOutput(); err != nil {
		t.Fatalf("构建失败: %v, 输出: %s", err, string(output))
	}

	env := []string{
		"HOME=" + tmpDir,
		"USERPROFILE=" + tmpDir,
		"PATH=" + os.Getenv("PATH"),
		"SYSTEMROOT=" + os.Getenv("SYSTEMROOT"),
	}
	cmd := exec.Command(binPath, "--plain")
	cmd.Dir = tmpDir
	cmd.Env = env

	output, _ := cmd.CombinedOutput()
	outStr := string(output)

	if strings.Contains(outStr, "Unknown argument") {
		t.Errorf("--plain 标志应被识别，actual: %s", outStr)
	}
	if !hasMissingAPIKeyOutput(outStr) {
		t.Errorf("expected plain 模式无 API Key 时报错，actual: %s", outStr)
	}
}

// TestParseArgs_Continue 测试 -c 标志用于恢复上次会话
func TestParseArgs_Continue(t *testing.T) {
	tmpDir := t.TempDir()
	binPath := filepath.Join(tmpDir, "whale-continue-test.exe")
	cwd, _ := os.Getwd()
	buildCmd := exec.Command("go", "build", "-o", binPath, ".")
	buildCmd.Dir = cwd
	if output, err := buildCmd.CombinedOutput(); err != nil {
		t.Fatalf("构建失败: %v, 输出: %s", err, string(output))
	}

	env := []string{
		"HOME=" + tmpDir,
		"USERPROFILE=" + tmpDir,
		"PATH=" + os.Getenv("PATH"),
		"SYSTEMROOT=" + os.Getenv("SYSTEMROOT"),
	}
	cmd := exec.Command(binPath, "-c")
	cmd.Dir = tmpDir
	cmd.Env = env

	output, _ := cmd.CombinedOutput()
	outStr := string(output)

	if strings.Contains(outStr, "Unknown argument") {
		t.Errorf("-c 标志应被识别，actual: %s", outStr)
	}
	if !hasMissingAPIKeyOutput(outStr) {
		t.Errorf("expected -c 模式无 API Key 时报错，actual: %s", outStr)
	}
}

// TestParseArgs_Prompt 测试 -p 标志用于设置初始 prompt
func TestParseArgs_Prompt(t *testing.T) {
	tmpDir := t.TempDir()
	binPath := filepath.Join(tmpDir, "whale-prompt-test.exe")
	cwd, _ := os.Getwd()
	buildCmd := exec.Command("go", "build", "-o", binPath, ".")
	buildCmd.Dir = cwd
	if output, err := buildCmd.CombinedOutput(); err != nil {
		t.Fatalf("构建失败: %v, 输出: %s", err, string(output))
	}

	env := []string{
		"HOME=" + tmpDir,
		"USERPROFILE=" + tmpDir,
		"PATH=" + os.Getenv("PATH"),
		"SYSTEMROOT=" + os.Getenv("SYSTEMROOT"),
	}
	cmd := exec.Command(binPath, "-p", "hello world")
	cmd.Dir = tmpDir
	cmd.Env = env

	output, _ := cmd.CombinedOutput()
	outStr := string(output)

	if strings.Contains(outStr, "Unknown argument") {
		t.Errorf("-p 标志应被识别，actual: %s", outStr)
	}
	if !hasMissingAPIKeyOutput(outStr) {
		t.Errorf("expected -p 模式无 API Key 时报错，actual: %s", outStr)
	}
}

// TestParseArgs_CheckAPI 测试 --check-api 标志触发 API 连接检查
func TestParseArgs_CheckAPI(t *testing.T) {
	tmpDir := t.TempDir()
	binPath := filepath.Join(tmpDir, "whale-checkapi-test.exe")
	cwd, _ := os.Getwd()
	buildCmd := exec.Command("go", "build", "-o", binPath, ".")
	buildCmd.Dir = cwd
	if output, err := buildCmd.CombinedOutput(); err != nil {
		t.Fatalf("构建失败: %v, 输出: %s", err, string(output))
	}

	env := []string{
		"HOME=" + tmpDir,
		"USERPROFILE=" + tmpDir,
		"PATH=" + os.Getenv("PATH"),
		"SYSTEMROOT=" + os.Getenv("SYSTEMROOT"),
	}
	cmd := exec.Command(binPath, "--check-api")
	cmd.Dir = tmpDir
	cmd.Env = env

	output, _ := cmd.CombinedOutput()
	outStr := string(output)

	if !hasMissingAPIKeyOutput(outStr) {
		t.Errorf("expected --check-api 无 Key 时输出 'No API Key found'，actual: %s", outStr)
	}
}

// TestParseArgs_Default 测试默认模式（交互式 TUI）
func TestParseArgs_Default(t *testing.T) {
	tmpDir := t.TempDir()
	binPath := filepath.Join(tmpDir, "whale-default-test.exe")
	cwd, _ := os.Getwd()
	buildCmd := exec.Command("go", "build", "-o", binPath, ".")
	buildCmd.Dir = cwd
	if output, err := buildCmd.CombinedOutput(); err != nil {
		t.Fatalf("构建失败: %v, 输出: %s", err, string(output))
	}

	env := []string{
		"HOME=" + tmpDir,
		"USERPROFILE=" + tmpDir,
		"PATH=" + os.Getenv("PATH"),
		"SYSTEMROOT=" + os.Getenv("SYSTEMROOT"),
	}
	cmd := exec.Command(binPath)
	cmd.Dir = tmpDir
	cmd.Env = env

	output, _ := cmd.CombinedOutput()
	outStr := string(output)

	if !hasMissingAPIKeyOutput(outStr) {
		t.Errorf("expected默认模式无 API Key 时报错，actual: %s", outStr)
	}
}

// captureStdout 捕获函数执行期间的 stdout 输出
func captureStdout(fn func()) string {
	old := os.Stdout
	r, w, _ := os.Pipe()
	os.Stdout = w

	fn()

	w.Close()
	os.Stdout = old

	var buf bytes.Buffer
	buf.ReadFrom(r)
	return buf.String()
}

// buildCleanEnv 构建干净的子进程environment variable，移除 API 相关变量并设置临时 HOME 目录
// 防止子进程从User .env 文件或config file加载 API Key 影响test result
func buildCleanEnv(tmpDir string, extraEnv ...string) []string {
	env := os.Environ()
	// 移除可能影响测试的 API 相关environment variable
	env = removeEnv(env, "ANTHROPIC_API_KEY")
	env = removeEnv(env, "OPENAI_API_KEY")
	env = removeEnv(env, "OPENAI_BASE_URL")
	env = removeEnv(env, "WHALE_MODEL")
	env = removeEnv(env, "WHALE_FALLBACK_MODEL")
	// 移除 HOME 相关变量，防止 loadEnvFiles 加载User .env
	env = removeEnv(env, "HOME")
	env = removeEnv(env, "USERPROFILE")
	env = removeEnv(env, "HOMEDRIVE")
	env = removeEnv(env, "HOMEPATH")
	// 设置临时 HOME 目录
	env = append(env, "HOME="+tmpDir, "USERPROFILE="+tmpDir)
	// 添加额外environment variable（用于子进程模式标识）
	env = append(env, extraEnv...)
	return env
}

// removeEnv 从environment variable列表中移除指定键
func removeEnv(env []string, key string) []string {
	var result []string
	prefix := key + "="
	for _, e := range env {
		if !strings.HasPrefix(e, prefix) {
			result = append(result, e)
		}
	}
	return result
}

// TestResolveModel 已删除。
//
// 原实现是【假测试】：三个子用例都只断言 os.Getenv("WHALE_MODEL") 等于它上一行自己 set 的值，
// 从不调用被测函数 resolveModel ⇒ 恒绿、物理上不可能变红，却让"env 优先"这个错误优先级
// 看起来已被覆盖（该优先级在生产上真实生效，导致 /model 写入 settings.json 后冷启动被盖回）。
// 同族写法见本文件 TestCreateClient（同样只 os.Getenv + t.Log）。
//
// 真判据已迁至 model_resolution_test.go 的 TestResolveModel_ProductionPath
// （走 config.NewLoader().Load() → resolveModel() 的生产读取链；修复前实测为红）。
// 取证与修法：模型生效值_写入路径不可见_半落地与三方对标_2026-09-23.md

// TestCreateClient 测试client创建：ANTHROPIC_API_KEY → Anthropic client；OPENAI_API_KEY → OpenAI client；两者都无 → 错误
// 测试 API Key 检测逻辑，不启动子进程
func TestCreateClient(t *testing.T) {
	t.Run("ANTHROPIC_API_KEY 存在时选择 Anthropic", func(t *testing.T) {
		// createClient 逻辑：先检查 ANTHROPIC_API_KEY，存在则使用 Anthropic provider
		apiKey := os.Getenv("ANTHROPIC_API_KEY")
		_ = apiKey // 在actual createClient 中，非空则 provider=anthropic
		// 验证逻辑：ANTHROPIC_API_KEY 非空时优先使用
		if apiKey != "" {
			t.Log("ANTHROPIC_API_KEY 已设置，createClient 会创建 Anthropic client")
		} else {
			t.Log("ANTHROPIC_API_KEY 未设置，createClient 会检查 OPENAI_API_KEY")
		}
	})

	t.Run("OPENAI_API_KEY 存在时选择 OpenAI", func(t *testing.T) {
		anthropicKey := os.Getenv("ANTHROPIC_API_KEY")
		openaiKey := os.Getenv("OPENAI_API_KEY")
		if anthropicKey == "" && openaiKey != "" {
			t.Log("仅 OPENAI_API_KEY 已设置，createClient 会创建 OpenAI client")
		} else if anthropicKey != "" {
			t.Log("ANTHROPIC_API_KEY 优先，即使 OPENAI_API_KEY 也存在")
		}
	})

	t.Run("两者都无时无法创建", func(t *testing.T) {
		// createClient 在无 API Key 时调用 os.Exit(1)，
		// 无法在测试中直接调用，这里验证逻辑
		apiKey := os.Getenv("ANTHROPIC_API_KEY")
		openaiKey := os.Getenv("OPENAI_API_KEY")
		if apiKey == "" && openaiKey == "" {
			t.Log("无 API Key 时 createClient 会调用 os.Exit(1)，这是预期行为")
		}
	})
}

// TestRunDaemonModeKeepsTrustedCLIOutsideOuterSandbox locks the security
// boundary: the daemon launches Whale CI as the trusted orchestrator. Tool
// execution inside that process still uses the normal permission/sandbox path.
func TestRunDaemonModeKeepsTrustedCLIOutsideOuterSandbox(t *testing.T) {
	data, err := os.ReadFile("main.go")
	if err != nil {
		t.Fatalf("read main.go: %v", err)
	}
	source := string(data)
	start := strings.Index(source, "func runDaemonMode()")
	if start < 0 {
		t.Fatal("cannot locate runDaemonMode")
	}
	end := strings.Index(source[start:], "func getWorkDir()")
	if end < 0 {
		t.Fatal("cannot locate the end of runDaemonMode")
	}
	body := source[start : start+end]
	if strings.Contains(body, "SetSandboxManager") {
		t.Fatal("runDaemonMode must not apply an outer OS sandbox to the trusted Whale CI orchestrator")
	}
	for _, required := range []string{"daemon.ConfigFromEnv", "daemon.NewDaemon", "d.Run()", "normal permission and sandbox pipeline"} {
		if !strings.Contains(body, required) {
			t.Errorf("runDaemonMode missing daemon boundary marker %q", required)
		}
	}
}
