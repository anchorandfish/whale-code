package hook

// hook 模块 P0 安全漏洞修复测试
//
// 覆盖两个修复点：
//  1. buildHookEnv 白名单传递 + 剥离密钥 + HOOK_TOOL_OUTPUT 长度截断
//  2. HOOK_TOOL_INPUT 赋值 bug 修复（ToolName → ToolInputJSON）+ 截断
//
// H2：修复点3（命令安全校验）已迁移至 internal/security/security_test.go，
// 由 security.CheckCommand 三层校验统一覆盖（全角/多空格/变量展开/引号分割等绕过）。

import (
	"strings"
	"testing"
)

// envValue 从环境变量列表中提取指定变量的值，变量不存在时返回 "" 和 false。
func envValue(env []string, key string) (string, bool) {
	prefix := key + "="
	for _, e := range env {
		if strings.HasPrefix(e, prefix) {
			return strings.TrimPrefix(e, prefix), true
		}
	}
	return "", false
}

// containsEnvKey 检查环境变量列表中是否包含指定变量。
func containsEnvKey(env []string, key string) bool {
	prefix := key + "="
	for _, e := range env {
		if strings.HasPrefix(e, prefix) {
			return true
		}
	}
	return false
}

// --- 修复点1：buildHookEnv 白名单 + 剥离密钥 ---

// TestBuildHookEnv_StripsSecrets 验证 buildHookEnv 不传递敏感密钥环境变量。
// 设置 OPENAI_API_KEY/GITHUB_TOKEN/ANTHROPIC_API_KEY 后，输出中不应包含这些变量。
func TestBuildHookEnv_StripsSecrets(t *testing.T) {
	// 注入敏感密钥（t.Setenv 会在测试结束后自动还原）
	t.Setenv("OPENAI_API_KEY", "sk-test-openai-secret")
	t.Setenv("GITHUB_TOKEN", "ghp_test_github_secret")
	t.Setenv("ANTHROPIC_API_KEY", "sk-ant-test-secret")
	t.Setenv("SECRET_TOKEN", "super-secret-value")

	payload := HookPayload{ToolName: "read_file"}
	env := buildHookEnv(payload, EventPreToolUse)

	secretKeys := []string{
		"OPENAI_API_KEY", "GITHUB_TOKEN", "ANTHROPIC_API_KEY", "SECRET_TOKEN",
	}
	for _, key := range secretKeys {
		if containsEnvKey(env, key) {
			val, _ := envValue(env, key)
			t.Errorf("安全漏洞: 敏感环境变量 %s 被传递给 hook 子进程\n"+
				"  期望: 不含 %s（白名单应剥离密钥）\n"+
				"  实际: %s=%q\n"+
				"  修复: buildHookEnv 改用 safeEnvWhitelist 白名单，不再 os.Environ() 全量传递",
				key, key, key, val)
		}
	}
}

// TestBuildHookEnv_WhitelistPresent 验证 buildHookEnv 传递白名单安全变量（PATH/HOME 等）。
func TestBuildHookEnv_WhitelistPresent(t *testing.T) {
	// 确保白名单变量有值
	t.Setenv("PATH", "/usr/bin:/bin")
	t.Setenv("HOME", "/home/testuser")

	payload := HookPayload{ToolName: "read_file"}
	env := buildHookEnv(payload, EventPreToolUse)

	if v, ok := envValue(env, "PATH"); !ok || v != "/usr/bin:/bin" {
		t.Errorf("白名单变量 PATH 应被传递\n  期望: PATH=/usr/bin:/bin\n  实际: ok=%v, v=%q", ok, v)
	}
	if v, ok := envValue(env, "HOME"); !ok || v != "/home/testuser" {
		t.Errorf("白名单变量 HOME 应被传递\n  期望: HOME=/home/testuser\n  实际: ok=%v, v=%q", ok, v)
	}
}

// TestBuildHookEnv_ToolOutputTruncation 验证 HOOK_TOOL_OUTPUT 超过 maxToolOutputEnvSize 时被截断。
func TestBuildHookEnv_ToolOutputTruncation(t *testing.T) {
	// 构造超长 ToolOutput（超过 32768 字节）
	longOutput := strings.Repeat("A", maxToolOutputEnvSize+10000)
	payload := HookPayload{
		ToolName:   "read_file",
		ToolOutput: longOutput,
	}
	env := buildHookEnv(payload, EventPostToolUse)

	val, ok := envValue(env, "HOOK_TOOL_OUTPUT")
	if !ok {
		t.Fatal("HOOK_TOOL_OUTPUT 未设置")
	}
	if !strings.HasSuffix(val, "\n... (truncated)") {
		t.Errorf("HOOK_TOOL_OUTPUT 超长时应以截断标记结尾\n"+
			"  期望: 以 '\\n... (truncated)' 结尾\n"+
			"  实际: 长度=%d, 末尾=%q",
			len(val), val[len(val)-30:])
	}
	// 截断后长度 = maxToolOutputEnvSize + len("\n... (truncated)")
	expectedLen := maxToolOutputEnvSize + len("\n... (truncated)")
	if len(val) != expectedLen {
		t.Errorf("HOOK_TOOL_OUTPUT 截断后长度不正确\n  期望: %d\n  实际: %d", expectedLen, len(val))
	}
}

// TestBuildHookEnv_ToolOutputNotTruncated 验证 HOOK_TOOL_OUTPUT 未超长时不被截断。
func TestBuildHookEnv_ToolOutputNotTruncated(t *testing.T) {
	shortOutput := "file content here"
	payload := HookPayload{
		ToolName:   "read_file",
		ToolOutput: shortOutput,
	}
	env := buildHookEnv(payload, EventPostToolUse)

	val, ok := envValue(env, "HOOK_TOOL_OUTPUT")
	if !ok {
		t.Fatal("HOOK_TOOL_OUTPUT 未设置")
	}
	if val != shortOutput {
		t.Errorf("HOOK_TOOL_OUTPUT 未超长时应原样传递\n  期望: %q\n  实际: %q", shortOutput, val)
	}
}

// --- 修复点2：HOOK_TOOL_INPUT 赋值 bug 修复 ---

// TestBuildHookEnv_ToolInputIsJSON 验证 HOOK_TOOL_INPUT 赋值为 ToolInputJSON 而非 ToolName。
// 对应 FACT-HOOK-002：原实现误赋 payload.ToolName。
func TestBuildHookEnv_ToolInputIsJSON(t *testing.T) {
	payload := HookPayload{
		ToolName:      "write_file",
		ToolInputJSON: `{"path":"/tmp/test.go","content":"hello"}`,
	}
	env := buildHookEnv(payload, EventPreToolUse)

	inputVal, hasInput := envValue(env, "HOOK_TOOL_INPUT")
	nameVal, hasName := envValue(env, "HOOK_TOOL_NAME")

	if !hasInput {
		t.Fatal("HOOK_TOOL_INPUT 未设置（ToolInputJSON 非空时应设置）")
	}
	if !hasName {
		t.Fatal("HOOK_TOOL_NAME 未设置")
	}
	if inputVal != payload.ToolInputJSON {
		t.Errorf("HOOK_TOOL_INPUT 应为 ToolInputJSON\n  期望: %q\n  实际: %q",
			payload.ToolInputJSON, inputVal)
	}
	if inputVal == nameVal {
		t.Errorf("HOOK_TOOL_INPUT 不应等于 HOOK_TOOL_NAME\n  TOOL_INPUT=%q\n  TOOL_NAME=%q",
			inputVal, nameVal)
	}
}

// TestBuildHookEnv_ToolInputEmptyNotSet 验证 ToolInputJSON 为空时 HOOK_TOOL_INPUT 不设置。
func TestBuildHookEnv_ToolInputEmptyNotSet(t *testing.T) {
	payload := HookPayload{
		ToolName: "read_file",
		// ToolInputJSON 为空
	}
	env := buildHookEnv(payload, EventPreToolUse)

	if containsEnvKey(env, "HOOK_TOOL_INPUT") {
		val, _ := envValue(env, "HOOK_TOOL_INPUT")
		t.Errorf("ToolInputJSON 为空时不应设置 HOOK_TOOL_INPUT\n  实际: HOOK_TOOL_INPUT=%q", val)
	}
	// HOOK_TOOL_NAME 仍应正常设置
	if !containsEnvKey(env, "HOOK_TOOL_NAME") {
		t.Error("ToolName 非空时 HOOK_TOOL_NAME 应设置")
	}
}

// TestBuildHookEnv_ToolInputTruncation 验证 HOOK_TOOL_INPUT 超过 maxToolInputEnvSize 时被截断。
func TestBuildHookEnv_ToolInputTruncation(t *testing.T) {
	longInput := strings.Repeat("X", maxToolInputEnvSize+5000)
	payload := HookPayload{
		ToolName:      "write_file",
		ToolInputJSON: longInput,
	}
	env := buildHookEnv(payload, EventPreToolUse)

	val, ok := envValue(env, "HOOK_TOOL_INPUT")
	if !ok {
		t.Fatal("HOOK_TOOL_INPUT 未设置")
	}
	if !strings.HasSuffix(val, "\n... (truncated)") {
		t.Errorf("HOOK_TOOL_INPUT 超长时应以截断标记结尾\n  实际长度=%d", len(val))
	}
	expectedLen := maxToolInputEnvSize + len("\n... (truncated)")
	if len(val) != expectedLen {
		t.Errorf("HOOK_TOOL_INPUT 截断后长度不正确\n  期望: %d\n  实际: %d", expectedLen, len(val))
	}
}
