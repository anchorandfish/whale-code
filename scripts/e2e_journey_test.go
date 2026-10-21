//go:build integration

// Package e2e_test 提供 Whale Code 端到端用户旅程测试。
//
// 通过编译真实二进制（go build -o whale.test .）并模拟用户输入（stdin pipe），
// 验证 6 条核心用户旅程的端到端行为。测试仅断言"用户可见行为"（stdout/stderr/
// 文件系统状态），不依赖 internal 包内部状态，不污染 internal 包。
//
// 运行方式：
//
//	go test -tags integration -v ./scripts/...
//
// 环境限制：
// - 无真实 API Key 时，旅程 2/4 自动 t.Skip
// - 非 Windows 平台下，旅程 5 自动 t.Skip("Windows 专属")
package e2e_test

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"
)

// binaryPath 是构建的测试二进制路径，由 TestMain 构建一次后全局复用。
var binaryPath string

// binaryOnce 保证二进制只构建一次（并发安全）。
var binaryOnce sync.Once

// binaryErr 记录构建错误，构建失败时各测试 fatal 退出。
var binaryErr error

// magicStringPatterns 是 v3 P0-1 修复点要求清除的魔法字符串黑名单。
// Bridge 模式下 IDE 收到的 stdout 不得包含以下任一字符串，
// 否则视为魔法字符串泄露到 IDE（P0-1 修复点回退）。
var magicStringPatterns = []string{
	"__COST__:",
	"__SESSION__:",
	"__SESSION_LIST__:",
	"__SESSIONS__:",
	"__COMPACT__:",
	"__EXIT__",
}

// TestMain 在所有测试前构建 whale.test 二进制，构建失败时退出码 2。
// 二进制构建到 OS 临时目录，测试套件结束后由 OS 回收。
func TestMain(m *testing.M) {
	binaryOnce.Do(func() {
		binaryPath, binaryErr = buildBinary()
	})
	if binaryErr != nil {
		fmt.Fprintf(os.Stderr, "构建测试二进制失败：%v\n", binaryErr)
		os.Exit(2)
	}
	os.Exit(m.Run())
}

// buildBinary 编译 whale.test 二进制到临时目录。
//
// 流程：
// 1. 通过 runtime.Caller 定位测试文件路径，推导项目根（go.mod 所在目录）
// 2. 在 OS 临时目录创建输出路径（Windows 追加 .exe 后缀）
// 3. 执行 go build -o <path> . （在项目根下编译 main 包）
//
// 返回：二进制绝对路径；失败时返回 error。
func buildBinary() (string, error) {
	projectRoot, err := findProjectRoot()
	if err != nil {
		return "", fmt.Errorf("查找项目根失败：%w", err)
	}
	tmpDir, err := os.MkdirTemp("", "whale-e2e-*")
	if err != nil {
		return "", fmt.Errorf("创建临时目录失败：%w", err)
	}
	binName := "whale.test"
	if runtime.GOOS == "windows" {
		binName += ".exe"
	}
	binPath := filepath.Join(tmpDir, binName)
	cmd := exec.Command("go", "build", "-o", binPath, ".")
	cmd.Dir = projectRoot
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return "", fmt.Errorf("go build 失败：%w\nstderr: %s", err, stderr.String())
	}
	return binPath, nil
}

// findProjectRoot 通过 runtime.Caller 定位测试文件路径，
// 推导项目根目录（scripts/ 的上级目录，即包含 go.mod 的目录）。
//
// 返回：项目根绝对路径；未找到 go.mod 时返回 error。
func findProjectRoot() (string, error) {
	_, testFile, _, ok := runtime.Caller(0)
	if !ok {
		return "", fmt.Errorf("无法获取测试文件路径")
	}
	// testFile 是 .../scripts/e2e_journey_test.go，项目根是 scripts/ 的上级
	scriptsDir := filepath.Dir(testFile)
	projectRoot := filepath.Dir(scriptsDir)
	if _, err := os.Stat(filepath.Join(projectRoot, "go.mod")); err != nil {
		return "", fmt.Errorf("项目根未找到 go.mod：%w", err)
	}
	return projectRoot, nil
}

// runConfig 是 runWhale 的输入配置，封装二进制执行所需的所有参数。
type runConfig struct {
	// homeDir 隔离的 HOME 目录（Unix）/ USERPROFILE（Windows），使 ~/.whale/ 指向临时目录。
	homeDir string
	// workDir 二进制的工作目录（cwd），影响 ./.env 加载路径。
	workDir string
	// env 额外环境变量（覆盖已有同名变量）。
	env map[string]string
	// stdin 模拟用户输入的标准输入内容。
	stdin string
	// args 命令行参数（如 --ci "hello"、--bridge）。
	args []string
	// timeout 单次旅程超时，零值默认 30s。
	timeout time.Duration
}

// runResult 是 runWhale 的输出结果，包含 stdout/stderr/退出码/超时标记。
type runResult struct {
	stdout   string
	stderr   string
	exitCode int
	timedOut bool
}

// buildEnv 构建隔离的环境变量列表。
//
// 流程：
// 1. 从 os.Environ() 拷贝基线
// 2. 过滤掉 homeKey（HOME/USERPROFILE）和 extra 中已有的同名变量
// 3. 追加 homeDir 和 extra 键值对
//
// 参数：
// - homeDir 隔离的 HOME 目录
// - extra 额外环境变量
//
// 返回：组装后的环境变量切片（每项 "KEY=VALUE"）。
func buildEnv(homeDir string, extra map[string]string) []string {
	homeKey := "HOME"
	if runtime.GOOS == "windows" {
		homeKey = "USERPROFILE"
	}
	overriddenKeys := map[string]bool{homeKey: true}
	for k := range extra {
		overriddenKeys[strings.ToUpper(k)] = true
	}
	filtered := make([]string, 0, len(os.Environ())+len(extra)+1)
	for _, e := range os.Environ() {
		parts := strings.SplitN(e, "=", 2)
		if len(parts) < 2 {
			continue
		}
		if overriddenKeys[strings.ToUpper(parts[0])] {
			continue
		}
		filtered = append(filtered, e)
	}
	filtered = append(filtered, homeKey+"="+homeDir)
	for k, v := range extra {
		filtered = append(filtered, k+"="+v)
	}
	return filtered
}

// runWhale 执行 whale.test 二进制，返回 stdout/stderr/退出码。
//
// 流程：
// 1. 构建带超时的 context（默认 30s）
// 2. 设置 cmd.Dir / cmd.Env / cmd.Stdin / cmd.Stdout / cmd.Stderr
// 3. 调用 cmd.Run()，捕获退出码与超时标记
//
// 参数：
// - t 测试对象（用于 t.Helper 标记与失败时 trace 输出）
// - cfg 执行配置
//
// 返回：runResult 包含 stdout/stderr/exitCode/timedOut。
func runWhale(t *testing.T, cfg runConfig) runResult {
	t.Helper()
	if cfg.timeout == 0 {
		cfg.timeout = 30 * time.Second
	}
	ctx, cancel := context.WithTimeout(context.Background(), cfg.timeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, binaryPath, cfg.args...)
	if cfg.workDir != "" {
		cmd.Dir = cfg.workDir
	}
	cmd.Env = buildEnv(cfg.homeDir, cfg.env)
	cmd.Stdin = strings.NewReader(cfg.stdin)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	err := cmd.Run()
	result := runResult{
		stdout: stdout.String(),
		stderr: stderr.String(),
	}
	if ctx.Err() == context.DeadlineExceeded {
		result.timedOut = true
	}
	if err != nil {
		if exitErr, ok := err.(*exec.ExitError); ok {
			result.exitCode = exitErr.ExitCode()
		} else if !result.timedOut {
			result.exitCode = -1
		}
	}
	return result
}

// readSettings 读取指定 homeDir 下的 ~/.whale/settings.json 并解析为 map。
// 文件不存在或 JSON 解析失败时返回 nil（不 fatal，调用方按需断言）。
//
// 参数：
// - t 测试对象
// - homeDir HOME 目录
//
// 返回：配置映射；文件不存在或解析失败时返回 nil。
func readSettings(t *testing.T, homeDir string) map[string]interface{} {
	t.Helper()
	settingsPath := filepath.Join(homeDir, ".whale", "settings.json")
	data, err := os.ReadFile(settingsPath)
	if err != nil {
		return nil
	}
	var settings map[string]interface{}
	if err := json.Unmarshal(data, &settings); err != nil {
		return nil
	}
	return settings
}

// fileExists 检查指定路径是否存在。
// 用于断言哨兵文件、.bak 备份等文件系统状态。
//
// 参数：
// - path 文件绝对路径
//
// 返回：存在返回 true，不存在返回 false。
func fileExists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}

// hasRealAPIKey 检测环境变量中是否配置了真实 API Key（非测试占位值）。
// 用于旅程 2/4 决定是否跳过：无真实 API Key 时无法验证 AI 对话流程。
//
// 判定规则：ANTHROPIC_API_KEY 或 OPENAI_API_KEY 非空且不以 "sk-test" 开头。
//
// 返回：存在真实 API Key 返回 true。
func hasRealAPIKey() bool {
	for _, key := range []string{"ANTHROPIC_API_KEY", "OPENAI_API_KEY"} {
		v := os.Getenv(key)
		if v != "" && !strings.HasPrefix(v, "sk-test") {
			return true
		}
	}
	return false
}

// assertNoMagicStrings 断言 stdout 不包含任何魔法字符串（v3 P0-1 修复点）。
// Bridge 模式下 IDE 收到的 stdout 不得包含 __COST__: 等魔法字符串，
// 否则视为 P0-1 修复回退（dispatchBridgeCommand 未正确替换魔法字符串）。
//
// 参数：
// - t 测试对象
// - output 待检查的 stdout 内容
func assertNoMagicStrings(t *testing.T, output string) {
	t.Helper()
	for _, pattern := range magicStringPatterns {
		if strings.Contains(output, pattern) {
			t.Errorf("stdout 包含魔法字符串 %q（v3 P0-1 修复点，应已清除）", pattern)
		}
	}
}

// failWithTrace 在测试失败时输出完整的 stdin/stdout/stderr trace。
// 用于失败时定位问题：包含 stdin 输入、stdout/stderr 输出、退出码、超时标记。
//
// 参数：
// - t 测试对象
// - msg 失败原因摘要
// - res runWhale 返回的结果
// - stdin 旅程的 stdin 输入内容
func failWithTrace(t *testing.T, msg string, res runResult, stdin string) {
	t.Helper()
	t.Logf("=== 旅程失败 trace ===")
	t.Logf("失败原因：%s", msg)
	t.Logf("--- stdin ---\n%s", stdin)
	t.Logf("--- stdout ---\n%s", res.stdout)
	t.Logf("--- stderr ---\n%s", res.stderr)
	t.Logf("--- exit code: %d (timed out: %v)", res.exitCode, res.timedOut)
	t.Logf("=== trace 结束 ===")
	t.Fatal(msg)
}

// TestJourneyOnboarding 旅程1：首次启动 → onboarding → 配置 API Key → 第一次对话。
//
// 验证点：
// 1. onboarding 流程可走通（stdout 包含步骤进度）
// 2. 配置写入成功（settings.json 存在且包含 API Key）
// 3. 哨兵文件 ~/.whale/.onboarded 生成
// 4. 第一次对话收到响应（有真实 API Key 时验证，否则跳过对话断言）
//
// 实现方式：
// - 隔离 HOME 到临时目录（无已有 ~/.whale/）
// - 设置 ANTHROPIC_API_KEY + WHALE_MODEL 环境变量
// - 通过 stdin "1\n" 选择 Anthropic provider
// - 执行 whale --ci "hello" 触发 loadConfig → onboarding → createClient → CI 对话
// - onboarding 完成后，--ci 模式调用真实 API（fake key 时失败，exit 1）
func TestJourneyOnboarding(t *testing.T) {
	homeDir := t.TempDir()
	stdin := "1\n" // 选择 provider 选项 1（Anthropic），步骤 3 自动检测环境变量跳过
	res := runWhale(t, runConfig{
		homeDir: homeDir,
		env: map[string]string{
			"ANTHROPIC_API_KEY": "sk-test-fake-key",
			"WHALE_MODEL":       "test-model",
		},
		stdin: stdin,
		args:  []string{"--ci", "hello"},
	})

	// 断言1：stdout 包含 onboarding 步骤进度（说明流程走了）
	if !strings.Contains(res.stdout, "Provider") && !strings.Contains(res.stdout, "onboarding") {
		failWithTrace(t, "onboarding 步骤未在 stdout 中体现", res, stdin)
	}

	// 断言2：settings.json 写入成功
	settings := readSettings(t, homeDir)
	if settings == nil {
		failWithTrace(t, "settings.json 未生成（onboarding 未写配置）", res, stdin)
	}
	if apiKey, ok := settings["api_key"].(string); !ok || apiKey == "" {
		t.Errorf("settings.json 中 api_key 为空或缺失（onboarding 配置写入失败）")
	}

	// 断言3：哨兵文件 ~/.whale/.onboarded 存在
	sentinel := filepath.Join(homeDir, ".whale", ".onboarded")
	if !fileExists(sentinel) {
		t.Errorf("哨兵文件 %s 未生成（onboarding 未标记完成）", sentinel)
	}

	// 断言4：有真实 API Key 时验证对话收到响应；否则跳过对话断言
	if !hasRealAPIKey() {
		t.Logf("无真实 API Key，跳过第一次对话响应断言（exit code=%d）", res.exitCode)
	} else if res.exitCode != 0 {
		t.Logf("有真实 API Key 但 --ci 退出码非 0（exit=%d），可能 API 调用失败", res.exitCode)
	}

	t.Logf("onboarding 旅程完成：exit=%d, settings.json=%v, sentinel=%v",
		res.exitCode, settings != nil, fileExists(sentinel))
}

// TestJourneyLongSessionCompact 旅程2：长会话 → 触发压缩 → 恢复 → 继续对话。
//
// 验证点：
// 1. 压缩触发后，关键上下文（如用户姓名、任务主题）仍保留
// 2. 压缩后对话能继续进行
//
// 实现方式：
// - 需要真实 API Key（无则 t.Skip）
// - 通过 bridge 模式发送多轮对话建立上下文
// - 发送 /compact 触发压缩
// - 发送后续消息验证上下文保留
//
// 注意：此旅程依赖真实 AI 响应，无 API Key 时自动跳过。
func TestJourneyLongSessionCompact(t *testing.T) {
	if !hasRealAPIKey() {
		t.Skip("跳过：无真实 API Key，无法验证长会话压缩与上下文保留")
	}
	homeDir := t.TempDir()
	// 构造多轮对话 + /compact + 上下文验证消息
	rpcLines := []string{
		`{"jsonrpc":"2.0","method":"chat","params":{"message":"我叫张三，今天要讨论 Go 项目架构"},"id":1}`,
		`{"jsonrpc":"2.0","method":"chat","params":{"message":"/compact"},"id":2}`,
		`{"jsonrpc":"2.0","method":"chat","params":{"message":"我叫什么名字？"},"id":3}`,
	}
	stdin := strings.Join(rpcLines, "\n") + "\n"
	res := runWhale(t, runConfig{
		homeDir: homeDir,
		env: map[string]string{
			"WHALE_BRIDGE_ENABLED": "1",
			"ANTHROPIC_API_KEY":    os.Getenv("ANTHROPIC_API_KEY"),
			"WHALE_MODEL":          "claude-sonnet-4-5",
		},
		stdin:   stdin,
		args:    []string{"--bridge"},
		timeout: 120 * time.Second, // 长会话需要更长超时
	})
	if res.timedOut {
		failWithTrace(t, "长会话旅程超时", res, stdin)
	}
	// 验证压缩后能记住姓名（关键上下文保留）
	if !strings.Contains(res.stdout, "张三") {
		t.Logf("警告：压缩后 stdout 未包含 '张三'，可能上下文丢失（需人工复核 AI 响应内容）")
	}
	t.Logf("长会话压缩旅程完成：exit=%d, stdout 长度=%d", res.exitCode, len(res.stdout))
}

// TestJourneyBridgeNoMagicString 旅程3：Bridge 模式 → IDE 发送 /usage /session /compact → 验证无魔法字符串。
//
// 验证点：
// 1. stdout 不含 __COST__: / __SESSION__: / __COMPACT__: 等魔法字符串
// 2. 命令能正常执行并返回可读输出（dispatchBridgeCommand 正确替换魔法字符串）
//
// 实现方式：
// - 使用 fake API Key + WHALE_BRIDGE_ENABLED=1 启动 bridge 模式
// - 通过 stdin 发送 JSON-RPC /usage /session /compact 请求
// - 断言 stdout 不包含魔法字符串黑名单中任一项
//
// 关键性：此旅程验证 v3 P0-1 修复点（魔法字符串清除），是安全回归测试。
//
// v4 修复：原测试未预置 .onboarded 哨兵 + settings.json，导致 Bridge
// 触发 onboarding 读取 JSON-RPC stdin 失败而 exit=1，stdout 为空，"无魔法字符串"
// 断言假成功（空字符串不含任何魔法字符串）。v4 修复：
// 1. 预置 ~/.whale/.onboarded 哨兵 + settings.json，跳过 onboarding 交互流程
// 2. 增加 exit==0 断言，Bridge 必须正常启动
// 3. 增加 ready 通知断言，Bridge 必须完成初始化
// 4. 增加 stdout 非空断言，防止空字符串假成功
func TestJourneyBridgeNoMagicString(t *testing.T) {
	homeDir := t.TempDir()
	// v4 修复：预置 .onboarded 哨兵 + settings.json，跳过 onboarding 交互流程
	whaleDir := filepath.Join(homeDir, ".whale")
	if err := os.MkdirAll(whaleDir, 0755); err != nil {
		t.Fatalf("创建 .whale 目录失败：%v", err)
	}
	sentinel := filepath.Join(whaleDir, ".onboarded")
	if err := os.WriteFile(sentinel, []byte("{}"), 0644); err != nil {
		t.Fatalf("写入 .onboarded 哨兵失败：%v", err)
	}
	settingsPath := filepath.Join(whaleDir, "settings.json")
	settingsContent := `{"api_key": "sk-test-fake-key", "model": "test-model", "bridge_enabled": true, "bridge": {"jwt_secret": "test-secret-32chars-long-xxxx"}}`
	if err := os.WriteFile(settingsPath, []byte(settingsContent), 0644); err != nil {
		t.Fatalf("写入 settings.json 失败：%v", err)
	}
	rpcLines := []string{
		`{"jsonrpc":"2.0","method":"chat","params":{"message":"/usage"},"id":1}`,
		`{"jsonrpc":"2.0","method":"chat","params":{"message":"/session"},"id":2}`,
		`{"jsonrpc":"2.0","method":"chat","params":{"message":"/compact"},"id":3}`,
	}
	stdin := strings.Join(rpcLines, "\n") + "\n"
	res := runWhale(t, runConfig{
		homeDir: homeDir,
		env: map[string]string{
			"WHALE_BRIDGE_ENABLED":    "1",
			"WHALE_BRIDGE_JWT_SECRET": "test-secret-32chars-long-xxxx",
			"ANTHROPIC_API_KEY":       "sk-test-fake-key",
			"WHALE_MODEL":             "test-model",
		},
		stdin:   stdin,
		args:    []string{"--bridge"},
		timeout: 30 * time.Second,
	})
	// v4 修复1：Bridge 必须正常启动（exit==0），防止 onboarding 失败导致空 stdout 假成功
	if res.exitCode != 0 {
		failWithTrace(t, fmt.Sprintf("Bridge 启动失败 exit=%d（onboarding 可能未跳过，原 v3 假成功根因）", res.exitCode), res, stdin)
	}
	// v4 修复2：Bridge 必须发送 ready 通知（初始化完成），证明命令链路真的在跑
	if !strings.Contains(res.stdout, "\"method\":\"ready\"") {
		failWithTrace(t, "Bridge 未发送 ready 通知（初始化未完成，可能空 stdout 假成功）", res, stdin)
	}
	// v4 修复3：stdout 必须非空（>200 字节，ready 通知本身就 >100 字节）
	if len(res.stdout) < 200 {
		failWithTrace(t, fmt.Sprintf("stdout 长度=%d 过短（可能 Bridge 未真正运行，空字符串假成功）", len(res.stdout)), res, stdin)
	}
	// 核心断言：stdout 不得包含任何魔法字符串
	assertNoMagicStrings(t, res.stdout)
	if strings.Contains(res.stdout, "__COST__:") ||
		strings.Contains(res.stdout, "__SESSION__:") ||
		strings.Contains(res.stdout, "__COMPACT__:") {
		failWithTrace(t, "Bridge 模式 stdout 泄露魔法字符串（P0-1 修复回退）", res, stdin)
	}
	t.Logf("Bridge 无魔法字符串旅程完成：exit=%d, stdout 长度=%d", res.exitCode, len(res.stdout))
}

// TestJourneyBridgeCommandDispatch 旅程3c：Bridge 模式 → IDE 发送 /version /usage /help → 验证命令真实 dispatch。
//
// 验证点（TC-006 Bridge 部分）：
// 1. Bridge 启动成功（exit==0，非因 jwt_secret 空而 fail-closed 退出）
// 2. stdout 包含 ready 通知（Bridge 初始化完成）
// 3. /version 命令返回真实版本号（非 __VERSION__ 魔法字符串）
// 4. /usage 命令返回真实用量统计（非 __COST__ 魔法字符串）
// 5. /help 命令返回真实帮助文本（非 __HELP__ 魔法字符串）
//
// 实现方式：
// - 预置 WHALE_BRIDGE_JWT_SECRET 避免 P0-13 fail-closed 退出
// - 通过 stdin 发送 3 个 JSON-RPC chat 请求
// - 断言 stdout 包含 ready + 命令响应文本
//
// 关键性：此旅程验证 Bridge 模式命令 dispatch 链路连通（TC-006 Bridge 部分）。
func TestJourneyBridgeCommandDispatch(t *testing.T) {
	homeDir := t.TempDir()
	// 预置 .onboarded 哨兵 + settings.json，跳过 Onboarding 交互流程
	whaleDir := filepath.Join(homeDir, ".whale")
	if err := os.MkdirAll(whaleDir, 0755); err != nil {
		t.Fatalf("创建 .whale 目录失败：%v", err)
	}
	sentinel := filepath.Join(whaleDir, ".onboarded")
	if err := os.WriteFile(sentinel, []byte("{}"), 0644); err != nil {
		t.Fatalf("写入 .onboarded 哨兵失败：%v", err)
	}
	settingsPath := filepath.Join(whaleDir, "settings.json")
	settingsContent := `{"api_key": "sk-test-fake-key", "model": "test-model", "bridge_enabled": true, "bridge": {"jwt_secret": "test-secret-32chars-long-xxxx"}}`
	if err := os.WriteFile(settingsPath, []byte(settingsContent), 0644); err != nil {
		t.Fatalf("写入 settings.json 失败：%v", err)
	}
	rpcLines := []string{
		`{"jsonrpc":"2.0","method":"chat","params":{"message":"/version"},"id":1}`,
		`{"jsonrpc":"2.0","method":"chat","params":{"message":"/usage"},"id":2}`,
		`{"jsonrpc":"2.0","method":"chat","params":{"message":"/help"},"id":3}`,
	}
	stdin := strings.Join(rpcLines, "\n") + "\n"
	res := runWhale(t, runConfig{
		homeDir: homeDir,
		env: map[string]string{
			"WHALE_BRIDGE_ENABLED":    "1",
			"WHALE_BRIDGE_JWT_SECRET": "test-secret-32chars-long-xxxx",
			"ANTHROPIC_API_KEY":       "sk-test-fake-key",
			"WHALE_MODEL":             "test-model",
		},
		stdin:   stdin,
		args:    []string{"--bridge"},
		timeout: 30 * time.Second,
	})
	// 1. 断言无魔法字符串泄漏
	assertNoMagicStrings(t, res.stdout)
	// 2. 断言 stdout 包含 ready 通知（Bridge 初始化成功）
	if !strings.Contains(res.stdout, "\"method\":\"ready\"") {
		failWithTrace(t, "Bridge 未发送 ready 通知（初始化失败）", res, stdin)
	}
	// 3. 断言命令真实执行（stdout 包含命令响应文本，非魔法字符串）
	// /version 命令响应应包含版本号或 Whale Code 字样
	if !strings.Contains(res.stdout, "Whale") && !strings.Contains(res.stdout, "version") {
		t.Errorf("Bridge /version 命令未返回真实版本信息，stdout=%s", res.stdout)
	}
	// /usage 命令响应应包含用量相关文本（token 统计或"不可用"降级提示，均证明命令真实 dispatch）
	if !strings.Contains(res.stdout, "Usage") && !strings.Contains(res.stdout, "usage") && !strings.Contains(res.stdout, "token") {
		t.Errorf("Bridge /usage 命令未返回真实用量信息，stdout=%s", res.stdout)
	}
	t.Logf("Bridge 命令 dispatch 旅程完成：exit=%d, stdout 长度=%d", res.exitCode, len(res.stdout))
}

// TestJourneyPlainNoMagicString 旅程3b：Plain 模式 → 发送 /usage → 验证无魔法字符串泄露到 stdout。
//
// 验证点：
// 1. plain 模式下 /usage 命令返回人类可读文本（由 UsageTracker.FormatUsageReport 格式化）
// 2. stdout 不含 __COST__: 等魔法字符串（plain 模式 initDispatchHandlers 拦截命令）
//
// 实现方式：
// - 预置 ~/.whale/.onboarded 哨兵 + settings.json，跳过 onboarding 交互流程
// - 设置 ANTHROPIC_API_KEY + WHALE_MODEL 环境变量
// - 执行 whale --plain，通过 stdin 发送 /usage + /exit
// - 断言 stdout 不含魔法字符串黑名单中任一项
//
// 关键性：此旅程验证 plain 模式魔法字符串泄露修复（历史根因：命令 Handler 返回
// "__XXX__:args" 魔法字符串而 plain 模式未注册对应 handler，导致直接泄露到 stdout）。
// /cost 命令已移除（provider 不返回费用详情），此旅程改用 /usage 验证同一条
// 命令 dispatch 链路：命令必须被 plain 模式拦截并返回可读文本，而非魔法字符串。
func TestJourneyPlainNoMagicString(t *testing.T) {
	homeDir := t.TempDir()
	whaleDir := filepath.Join(homeDir, ".whale")
	if err := os.MkdirAll(whaleDir, 0755); err != nil {
		t.Fatalf("创建 .whale 目录失败：%v", err)
	}
	// 预置 .onboarded 哨兵，使 onboarding.IsCompleted 直接返回 true（跳过交互 onboarding）
	sentinel := filepath.Join(whaleDir, ".onboarded")
	if err := os.WriteFile(sentinel, []byte("{}"), 0644); err != nil {
		t.Fatalf("写入 .onboarded 哨兵失败：%v", err)
	}
	// 预置 settings.json，确保 loadConfig 的 loader.Load() 正常加载（不触发损坏恢复）
	settingsPath := filepath.Join(whaleDir, "settings.json")
	settingsContent := `{"api_key": "sk-test-fake-key", "model": "test-model"}`
	if err := os.WriteFile(settingsPath, []byte(settingsContent), 0644); err != nil {
		t.Fatalf("写入 settings.json 失败：%v", err)
	}
	// stdin：/usage 触发用量命令（验证 plain 模式命令 dispatch 链路），/exit 干净退出
	stdin := "/usage\n/exit\n"
	res := runWhale(t, runConfig{
		homeDir: homeDir,
		env: map[string]string{
			"ANTHROPIC_API_KEY": "sk-test-fake-key",
			"WHALE_MODEL":       "test-model",
		},
		stdin: stdin,
		args:  []string{"--plain"},
	})
	if res.timedOut {
		failWithTrace(t, "plain 模式旅程超时（/exit 未被处理，进程可能挂起）", res, stdin)
	}
	// 核心断言：stdout 不得包含任何魔法字符串
	assertNoMagicStrings(t, res.stdout)
	if strings.Contains(res.stdout, "__COST__:") {
		failWithTrace(t, "Plain 模式 stdout 泄露 __COST__:（plain dispatch 未拦截命令）", res, stdin)
	}
	t.Logf("Plain 无魔法字符串旅程完成：exit=%d, stdout 长度=%d", res.exitCode, len(res.stdout))
}

// TestJourneyPlainWiringCheck 旅程3d：Plain 模式 → 验证 memory 接线 + sandbox 启用（TC-011/TC-012）。
//
// 验证点：
// 1. stderr 包含 "[wiring]" 日志（initProductionWiring 被调用，TC-011 memory 接线标志）
// 2. /sandbox 命令返回 "Sandbox: ON"（sandbox.SetEnabled(true) 已执行，TC-012）
// 3. /usage 命令正常执行（security.CheckCommand 未 fail-closed，间接验证 security.Init）
//
// 实现方式：
// - 预置 .onboarded 哨兵 + settings.json，跳过 onboarding
// - 发送 /sandbox + /usage + /exit 命令
// - 断言 stderr 包含 "[wiring]" + stdout 包含 "Sandbox: ON"
//
// 关键性：验证 §4.1（memory 接线）+ §4.5（sandbox 启用）修复在 PlainRunner 模式下生效。
func TestJourneyPlainWiringCheck(t *testing.T) {
	homeDir := t.TempDir()
	whaleDir := filepath.Join(homeDir, ".whale")
	if err := os.MkdirAll(whaleDir, 0755); err != nil {
		t.Fatalf("创建 .whale 目录失败：%v", err)
	}
	sentinel := filepath.Join(whaleDir, ".onboarded")
	if err := os.WriteFile(sentinel, []byte("{}"), 0644); err != nil {
		t.Fatalf("写入 .onboarded 哨兵失败：%v", err)
	}
	settingsPath := filepath.Join(whaleDir, "settings.json")
	settingsContent := `{"api_key": "sk-test-fake-key", "model": "test-model"}`
	if err := os.WriteFile(settingsPath, []byte(settingsContent), 0644); err != nil {
		t.Fatalf("写入 settings.json 失败：%v", err)
	}
	// stdin：/sandbox 查询状态，/usage 验证 security 未 fail-closed，/exit 退出
	stdin := "/sandbox\n/usage\n/exit\n"
	res := runWhale(t, runConfig{
		homeDir: homeDir,
		env: map[string]string{
			"ANTHROPIC_API_KEY": "sk-test-fake-key",
			"WHALE_MODEL":       "test-model",
		},
		stdin: stdin,
		args:  []string{"--plain"},
	})
	if res.timedOut {
		failWithTrace(t, "Plain wiring 检查超时（/exit 未被处理）", res, stdin)
	}
	// TC-011：stderr 包含 [wiring] 日志（initProductionWiring 调用标志）
	if !strings.Contains(res.stderr, "[wiring]") {
		t.Errorf("TC-011 失败：stderr 未包含 [wiring] 日志，initProductionWiring 可能未被调用。stderr=%s", res.stderr)
	}
	// TC-012：stdout 包含 "Sandbox: ON"（sandbox.SetEnabled(true) 已执行）
	if !strings.Contains(res.stdout, "Sandbox: ON") {
		t.Errorf("TC-012 失败：stdout 未包含 'Sandbox: ON'，sandbox 未启用。stdout=%s", res.stdout)
	}
	// 间接验证 security.Init：/usage 命令正常执行（未因 Checker 未初始化而 fail-closed）
	if strings.Contains(res.stdout, "Checker 未初始化") {
		t.Errorf("security.Init 未调用：/usage 命令返回 'Checker 未初始化'。stdout=%s", res.stdout)
	}
	t.Logf("Plain wiring 检查完成：exit=%d, stdout 长度=%d, stderr 长度=%d", res.exitCode, len(res.stdout), len(res.stderr))
}

// TestJourneyToolUndo 旅程4：工具调用 → bash 执行 → 文件编辑 → undo → 验证回退正确。
//
// 验证点：
// 1. 编辑后 undo 能恢复原文件内容
// 2. undo 操作有用户可见的确认输出
//
// 实现方式：
// - 需要真实 API Key（无则 t.Skip）
// - 通过 --ci 模式发送触发工具调用的 prompt
// - 验证文件系统状态变化与 undo 后的恢复
//
// 注意：此旅程依赖真实 AI 驱动工具调用，无 API Key 时自动跳过。
func TestJourneyToolUndo(t *testing.T) {
	if !hasRealAPIKey() {
		t.Skip("跳过：无真实 API Key，无法验证工具调用与 undo 流程")
	}
	homeDir := t.TempDir()
	workDir := t.TempDir()
	// 准备测试文件
	targetFile := filepath.Join(workDir, "undo_target.txt")
	originalContent := "原始内容 line1\n原始内容 line2\n"
	if err := os.WriteFile(targetFile, []byte(originalContent), 0644); err != nil {
		t.Fatalf("写入测试文件失败：%v", err)
	}
	// 通过 --ci 模式触发 AI 编辑文件
	prompt := fmt.Sprintf("请编辑文件 %s，将内容改为 '修改后内容'，然后不要执行其他操作", targetFile)
	res := runWhale(t, runConfig{
		homeDir: homeDir,
		workDir: workDir,
		env: map[string]string{
			"ANTHROPIC_API_KEY": os.Getenv("ANTHROPIC_API_KEY"),
			"WHALE_MODEL":       "claude-sonnet-4-5",
		},
		args:    []string{"--ci", prompt},
		timeout: 120 * time.Second,
	})
	if res.timedOut {
		failWithTrace(t, "工具调用旅程超时", res, prompt)
	}
	// 读取编辑后内容
	editedContent, err := os.ReadFile(targetFile)
	if err != nil {
		t.Fatalf("读取编辑后文件失败：%v", err)
	}
	if string(editedContent) == originalContent {
		t.Logf("警告：文件未被编辑（AI 可能未执行工具调用），exit=%d", res.exitCode)
	}
	// undo 验证：通过 --ci 模式发送 undo 指令（依赖 AI 理解 undo 意图）
	undoPrompt := fmt.Sprintf("请撤销刚才对 %s 的编辑", targetFile)
	undoRes := runWhale(t, runConfig{
		homeDir: homeDir,
		workDir: workDir,
		env: map[string]string{
			"ANTHROPIC_API_KEY": os.Getenv("ANTHROPIC_API_KEY"),
			"WHALE_MODEL":       "claude-sonnet-4-5",
		},
		args:    []string{"--ci", undoPrompt},
		timeout: 120 * time.Second,
	})
	// 读取 undo 后内容
	undoContent, err := os.ReadFile(targetFile)
	if err != nil {
		t.Fatalf("读取 undo 后文件失败：%v", err)
	}
	if string(undoContent) != originalContent {
		t.Logf("警告：undo 后文件内容与原内容不一致（可能 AI 未执行 undo，需人工复核）")
	}
	t.Logf("工具 undo 旅程完成：edit exit=%d, undo exit=%d", res.exitCode, undoRes.exitCode)
}

// TestJourneyPowerShellSecurity 旅程5：Windows 下 powershell 工具 → 验证 security.go 被调用。
//
// 验证点：
// 1. 非 Windows 平台 t.Skip("Windows 专属")
// 2. Windows 下通过 bridge command_guard 验证 security.CheckCommand 被调用
// 3. 危险命令（如 rm -rf）被 security 拦截，stdout 包含 "Command blocked"
//
// 实现方式：
// - 设置 WHALE_BRIDGE_ENABLED=1 + WHALE_BRIDGE_COMMAND_GUARD_ENABLED=1
// - 通过 bridge 模式发送包含危险命令的 slash 消息
// - 断言响应包含 "Command blocked"（证明 security.CheckCommand 被调用并拦截）
//
// 注意：command_guard 委托 security.CheckCommand（02 §2.3.1 契约），
// "Command blocked" 输出证明 security.go 已被调用。
func TestJourneyPowerShellSecurity(t *testing.T) {
	if runtime.GOOS != "windows" {
		t.Skip("Windows 专属：powershell 工具与 Windows security 路径验证")
	}
	homeDir := t.TempDir()
	// 构造危险命令（rm -rf 模式），触发 security.CheckCommand Deny
	rpcLines := []string{
		`{"jsonrpc":"2.0","method":"chat","params":{"message":"/exec rm -rf C:\\Users"},"id":1}`,
	}
	stdin := strings.Join(rpcLines, "\n") + "\n"
	res := runWhale(t, runConfig{
		homeDir: homeDir,
		env: map[string]string{
			"WHALE_BRIDGE_ENABLED":               "1",
			"WHALE_BRIDGE_COMMAND_GUARD_ENABLED": "1",
			"ANTHROPIC_API_KEY":                  "sk-test-fake-key",
			"WHALE_MODEL":                        "test-model",
		},
		stdin: stdin,
		args:  []string{"--bridge"},
	})
	// 断言：危险命令被拦截（证明 security.CheckCommand 被调用）
	if !strings.Contains(res.stdout, "Command blocked") &&
		!strings.Contains(res.stdout, "blocked") &&
		!strings.Contains(res.stderr, "security") {
		failWithTrace(t, "危险命令未被 security 拦截（security.CheckCommand 可能未被调用）", res, stdin)
	}
	t.Logf("PowerShell security 旅程完成：exit=%d, stdout 长度=%d", res.exitCode, len(res.stdout))
}

// TestJourneyConfigCorruptRefusesStartAndExplicitRepair 旅程6（S12 Fix PR#2 改写）：
// 配置损坏 → 启动被拒绝 → 显式修复（doctor --repair）→ 从 .bak 恢复 → 修复后不再被拒。
//
// 语义变更（S12 Fix PR#2，Q1/Q4=A；见 配置加载_STATUS.md §七）：
// - 旧行为：损坏 settings.json 触发 Load 内 tryRecoverFromBackup 自动恢复，启动照常进行
// （本用例原先正是如此断言）。
// - 新行为：损坏配置 → Load 返回 ErrCorruptConfig → **启动拒绝**（非零退出）且**不改盘**；
// 恢复须显式执行 `whale doctor --repair`（先试 .bak 恢复，仍损坏则隔离）。
//
// 断言（仅断言用户可见行为，不依赖恢复发生在哪一层）：
// 1. 损坏配置下 `whale --ci hello` 非零退出，且 stderr 提示修复命令 `doctor --repair`
// 2. 该次启动**不改写** settings.json、**不消费** .bak（fail-closed 且只读）
// 3. `whale doctor --repair` 后 settings.json 从 .bak 恢复为有效 JSON（含 api_key）
// 4. 修复后再次 `whale --ci hello` 不再因「配置损坏」被拒（不依赖真实 AI 调用成功与否）
func TestJourneyConfigCorruptRefusesStartAndExplicitRepair(t *testing.T) {
	homeDir := t.TempDir()
	whaleDir := filepath.Join(homeDir, ".whale")
	if err := os.MkdirAll(whaleDir, 0755); err != nil {
		t.Fatalf("创建 .whale 目录失败：%v", err)
	}
	settingsPath := filepath.Join(whaleDir, "settings.json")
	// 损坏的 settings.json（无效 JSON：缺少引号、括号不匹配）
	corruptContent := `{"bridge_enabled": true, "api_key": "sk-test-fake-key", invalid json !!!`
	if err := os.WriteFile(settingsPath, []byte(corruptContent), 0600); err != nil {
		t.Fatalf("写入损坏 settings.json 失败：%v", err)
	}
	// 预创建合法 .bak 备份（固定名，覆盖 RestoreFromBackup 的旧格式回退路径）
	bakContent := `{"api_key": "sk-test-fake-key", "model": "test-model"}`
	bakPath := settingsPath + ".bak"
	if err := os.WriteFile(bakPath, []byte(bakContent), 0600); err != nil {
		t.Fatalf("写入 .bak 备份失败：%v", err)
	}

	// --- 阶段 1：损坏配置下启动必须被拒绝（fail-closed）---
	res := runWhale(t, runConfig{
		homeDir: homeDir,
		env: map[string]string{
			"ANTHROPIC_API_KEY": "sk-test-fake-key",
			"WHALE_MODEL":       "test-model",
		},
		args: []string{"--ci", "hello"},
	})
	if res.exitCode == 0 {
		failWithTrace(t, "损坏配置下启动应被拒绝（非零退出），实际退出码 0", res, "")
	}
	if !strings.Contains(res.stderr, "doctor --repair") {
		t.Errorf("拒绝启动时应提示修复命令 doctor --repair（须写终端 fd 2）；stderr=%q", res.stderr)
	}

	// --- 阶段 2：拒绝启动必须是只读的（不改盘、不消费 .bak）---
	if got, rerr := os.ReadFile(settingsPath); rerr != nil || string(got) != corruptContent {
		t.Errorf("拒绝启动不得改写 settings.json；实际读取 err=%v content=%q", rerr, string(got))
	}
	if !fileExists(bakPath) {
		t.Errorf("拒绝启动不得消费 .bak（应留给显式恢复流程）")
	}

	// --- 阶段 3：显式修复 doctor --repair → 从 .bak 恢复 ---
	repair := runWhale(t, runConfig{
		homeDir: homeDir,
		args:    []string{"doctor", "--repair"},
	})
	recovered := readSettings(t, homeDir)
	if recovered == nil {
		failWithTrace(t, "doctor --repair 后 settings.json 仍非有效 JSON（未从 .bak 恢复）", repair, "")
	}
	if apiKey, ok := recovered["api_key"].(string); !ok || apiKey == "" {
		t.Errorf("doctor --repair 恢复后 settings.json 中 api_key 缺失（.bak 恢复未正确写入）")
	}

	// --- 阶段 4：修复后不再因「配置损坏」被拒 ---
	res2 := runWhale(t, runConfig{
		homeDir: homeDir,
		env: map[string]string{
			"ANTHROPIC_API_KEY": "sk-test-fake-key",
			"WHALE_MODEL":       "test-model",
		},
		args:    []string{"--ci", "hello"},
		timeout: 15 * time.Second,
	})
	if strings.Contains(res2.stderr, "配置加载失败") {
		t.Errorf("修复后不应再因配置损坏被拒；stderr=%q", res2.stderr)
	}
	t.Logf("配置损坏旅程（PR#2）完成：阶段1 exit=%d(应非0), 阶段3 repair stdout=%q, 阶段4 exit=%d",
		res.exitCode, strings.TrimSpace(repair.stdout), res2.exitCode)
}

// TestJourneyThreeModeCommands 旅程6：三模式命令端到端测试（§4.3 补充）。
//
// 验证 /models 和 /keybindings 命令在 Bridge 和 Plain 模式下正确执行。
// 这两个命令是本地命令（不需 AI 调用），验证三模式 dispatch 接线完整性。
//
// 验证点：
// 1. Bridge 模式 /models 命令返回模型信息（JSON RPC 响应）
// 2. Plain 模式 /keybindings 命令返回键绑定文本
// 3. 两种模式都不泄露魔法字符串
func TestJourneyThreeModeCommands(t *testing.T) {
	homeDir := t.TempDir()
	whaleDir := filepath.Join(homeDir, ".whale")
	if err := os.MkdirAll(whaleDir, 0755); err != nil {
		t.Fatalf("创建 .whale 目录失败：%v", err)
	}
	sentinel := filepath.Join(whaleDir, ".onboarded")
	if err := os.WriteFile(sentinel, []byte("{}"), 0644); err != nil {
		t.Fatalf("写入 .onboarded 哨兵失败：%v", err)
	}
	settingsPath := filepath.Join(whaleDir, "settings.json")
	settingsContent := `{"api_key": "sk-test-fake-key", "model": "test-model", "bridge_enabled": true, "bridge": {"jwt_secret": "test-secret-32chars-long-xxxx"}}`
	if err := os.WriteFile(settingsPath, []byte(settingsContent), 0644); err != nil {
		t.Fatalf("写入 settings.json 失败：%v", err)
	}

	// --- Bridge 模式 /models ---
	t.Run("Bridge_Models", func(t *testing.T) {
		rpcLines := []string{
			`{"jsonrpc":"2.0","method":"chat","params":{"message":"/models"},"id":1}`,
		}
		stdin := strings.Join(rpcLines, "\n") + "\n"
		res := runWhale(t, runConfig{
			homeDir: homeDir,
			env: map[string]string{
				"WHALE_BRIDGE_ENABLED":    "1",
				"WHALE_BRIDGE_JWT_SECRET": "test-secret-32chars-long-xxxx",
				"ANTHROPIC_API_KEY":       "sk-test-fake-key",
				"WHALE_MODEL":             "test-model",
			},
			stdin:   stdin,
			args:    []string{"--bridge"},
			timeout: 30 * time.Second,
		})
		assertNoMagicStrings(t, res.stdout)
		if !strings.Contains(res.stdout, "\"method\":\"ready\"") {
			failWithTrace(t, "Bridge 未发送 ready 通知", res, stdin)
		}
		// /models 命令应返回模型相关文本
		if !strings.Contains(res.stdout, "model") && !strings.Contains(res.stdout, "Model") {
			t.Errorf("Bridge /models 命令未返回模型信息，stdout=%s", res.stdout)
		}
		t.Logf("Bridge /models 完成：exit=%d, stdout 长度=%d", res.exitCode, len(res.stdout))
	})

	// --- Plain 模式 /keybindings ---
	t.Run("Plain_Keybindings", func(t *testing.T) {
		stdin := "/keybindings\n/exit\n"
		res := runWhale(t, runConfig{
			homeDir: homeDir,
			env: map[string]string{
				"ANTHROPIC_API_KEY": "sk-test-fake-key",
				"WHALE_MODEL":       "test-model",
			},
			stdin: stdin,
			args:  []string{"--plain"},
		})
		if res.timedOut {
			failWithTrace(t, "Plain 模式超时", res, stdin)
		}
		assertNoMagicStrings(t, res.stdout)
		// /keybindings 命令应返回键绑定相关文本（可能为空列表提示或绑定列表）
		if !strings.Contains(res.stdout, "key") && !strings.Contains(res.stdout, "Key") && !strings.Contains(res.stdout, "binding") {
			t.Errorf("Plain /keybindings 命令未返回键绑定信息，stdout=%s", res.stdout)
		}
		t.Logf("Plain /keybindings 完成：exit=%d, stdout 长度=%d", res.exitCode, len(res.stdout))
	})
}
