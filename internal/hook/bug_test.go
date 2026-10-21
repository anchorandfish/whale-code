package hook

// 反例测试（Bug Reproduction Tests）
//
// 本文件是对标审查机制的核心组件。每个测试对应 docs/BENCHMARK_FACTS.md 中的一个事实条目。
//
// 测试特点：
// - 测试名以 TestBug_ 开头
// - 在当前（含 bug 的）代码上运行会 **失败**，证明 bug 存在
// - 修复 bug 后测试 **通过**，证明 bug 已修
//
// 使用方式：
//   go test ./internal/hook/ -run TestBug_ -v
//
// 预期结果：当前代码下部分 TestBug_ 测试失败（证明 bug 存在）
// 修复后：所有 TestBug_ 测试通过（证明 bug 已修）

import (
	"reflect"
	"strings"
	"testing"
)

// TestBug_MergeConfigDuplication 对应 FACT-HOOK-001
// 钩子配置合并必须无重复代码
// 当前实现：mergeConfig 有 23 行重复的 append 代码（hook.go:321-345）
// 正确做法：用反射或统一函数处理所有 []Hook 字段
// 注：此测试验证功能正确性（所有字段都被合并），代码异味在审查报告中说明
func TestBug_MergeConfigDuplication(t *testing.T) {
	// FACT-HOOK-001
	m := NewManager("")

	// 构造一个所有 []Hook 字段都有数据的配置（FACT-HOOK-004 修复后使用具体类型）
	mkCmd := func(cmd string) Hook {
		return &HookCommand{HookBase: HookBase{Matcher: "*"}, Command: cmd}
	}
	newCfg := HookConfig{
		PreToolUse:   []Hook{mkCmd("echo pre_tool_use")},
		PostToolUse:  []Hook{mkCmd("echo post_tool_use")},
		PreRead:      []Hook{mkCmd("echo pre_read")},
		PostRead:     []Hook{mkCmd("echo post_read")},
		PreWrite:     []Hook{mkCmd("echo pre_write")},
		PostWrite:    []Hook{mkCmd("echo post_write")},
		PreEdit:      []Hook{mkCmd("echo pre_edit")},
		PostEdit:     []Hook{mkCmd("echo post_edit")},
		PreBash:      []Hook{mkCmd("echo pre_bash")},
		PostBash:     []Hook{mkCmd("echo post_bash")},
		PreSearch:    []Hook{mkCmd("echo pre_search")},
		PostSearch:   []Hook{mkCmd("echo post_search")},
		PreGit:       []Hook{mkCmd("echo pre_git")},
		PostGit:      []Hook{mkCmd("echo post_git")},
		PreWeb:       []Hook{mkCmd("echo pre_web")},
		PostWeb:      []Hook{mkCmd("echo post_web")},
		PreAgent:     []Hook{mkCmd("echo pre_agent")},
		PostAgent:    []Hook{mkCmd("echo post_agent")},
		SessionStart: []Hook{mkCmd("echo session_start")},
		SessionStop:  []Hook{mkCmd("echo session_stop")},
		Stop:         []Hook{mkCmd("echo stop")},
		PostCompact:  []Hook{mkCmd("echo post_compact")},
		Notification: []Hook{mkCmd("echo notification")},
	}

	m.mergeConfig(newCfg)
	config := m.Config()

	// 用反射验证所有 []Hook 字段都被合并
	// 如果新增事件类型但忘记更新 mergeConfig，此测试会失败
	cfgVal := reflect.ValueOf(config)
	cfgType := cfgVal.Type()
	for i := 0; i < cfgType.NumField(); i++ {
		field := cfgType.Field(i)
		if field.Type.Kind() == reflect.Slice && field.Type.Elem().Name() == "Hook" {
			slice := cfgVal.Field(i)
			if slice.Len() == 0 {
				t.Errorf("BUG [FACT-HOOK-001]: field %s not merged\n"+
					"  mergeConfig 遗漏了 %s 字段\n"+
					"  原因: mergeConfig 使用硬编码逐字段 append（hook.go:321-345），新增字段易遗漏\n"+
					"  正确做法: 用反射统一合并所有 []Hook 字段，消除 23 行重复代码",
					field.Name, field.Name)
			}
		}
	}
}

// TestBug_HookToolInputWrong 对应 FACT-HOOK-002
// HOOK_TOOL_INPUT 必须设为工具输入而非工具名
// 当前实现：hook.go:1387 HOOK_TOOL_INPUT 误设为 payload.ToolName
func TestBug_HookToolInputWrong(t *testing.T) {
	// FACT-HOOK-002
	payload := HookPayload{
		ToolName:      "write_file",
		ToolInputJSON: `{"path":"/tmp/test.go"}`,
		FilePath:      "/tmp/test.go",
	}

	env := buildHookEnv(payload, EventPreToolUse)

	var toolInputEnv string
	var toolNameEnv string
	for _, e := range env {
		if strings.HasPrefix(e, "HOOK_TOOL_INPUT=") {
			toolInputEnv = strings.TrimPrefix(e, "HOOK_TOOL_INPUT=")
		}
		if strings.HasPrefix(e, "HOOK_TOOL_NAME=") {
			toolNameEnv = strings.TrimPrefix(e, "HOOK_TOOL_NAME=")
		}
	}

	// HOOK_TOOL_INPUT 应该是工具输入 JSON，而不是工具名
	expected := payload.ToolInputJSON
	if toolInputEnv != expected {
		t.Errorf("BUG [FACT-HOOK-002]: HOOK_TOOL_INPUT set to tool name instead of tool input\n"+
			"  期望: %q (工具输入 JSON)\n"+
			"  实际: %q (工具名)\n"+
			"  原因: hook.go:1387 HOOK_TOOL_INPUT 误用 payload.ToolName\n"+
			"  正确做法: HOOK_TOOL_INPUT 应设为 payload.ToolInputJSON\n"+
			"  证据: 外部参考实现 环境变量",
			expected, toolInputEnv)
	}

	// HOOK_TOOL_NAME 和 HOOK_TOOL_INPUT 不应该相同（除非输入恰好等于工具名）
	if toolInputEnv == toolNameEnv && toolInputEnv != "" {
		t.Errorf("BUG [FACT-HOOK-002]: HOOK_TOOL_INPUT equals HOOK_TOOL_NAME\n"+
			"  HOOK_TOOL_NAME=%q\n"+
			"  HOOK_TOOL_INPUT=%q\n"+
			"  两者相同说明 HOOK_TOOL_INPUT 误设为工具名",
			toolNameEnv, toolInputEnv)
	}
}

// TestBug_HookCommandSafetyBypass 对应 FACT-HOOK-003 — 已迁移至 internal/security/security_test.go
// H2 黑名单统一收口：绕过手法测试由 security.CheckCommand 三层校验覆盖
// （TestCheckCommand_BypassVariants_FullWidth / _MultiSpace / _VarExpansion / _QuoteSplit / _LongFlag）。
// 原 validateHookCommandSafety 已删除，hook 命令校验委托 security.CheckCommand（02 §2.3.1 契约）。

// TestBug_HookTypeNotDiscriminated 对应 FACT-HOOK-004
// 钩子类型必须用 discriminatedUnion（接口 + 具体类型）
// 修复后：Hook 是密封接口，每种具体类型只有自己的字段，编译期保证类型安全
func TestBug_HookTypeNotDiscriminated(t *testing.T) {
	// FACT-HOOK-004
	// 在新的 discriminated union 设计下：
	// - HookHTTP 只有 URL/Headers 字段，没有 Command/Prompt
	// - HookCommand 只有 Command 字段，没有 URL/Prompt
	// - HookPrompt 只有 Prompt/Model 字段，没有 Command/URL
	// - HookAgent 只有 Prompt/Model 字段，没有 Command/URL
	// - HookCallback 只有 Callback 函数，没有 Command/URL/Prompt
	// 编译期就保证了类型安全，不可能出现 Type=http 但 Command 非空的不安全状态

	// 验证 1：所有具体类型都实现了 Hook 接口
	httpHook := &HookHTTP{
		HookBase: HookBase{Type: HookTypeHTTP, Matcher: "*"},
		URL:      "http://example.com",
	}
	cmdHook := &HookCommand{
		HookBase: HookBase{Type: HookTypeCommand, Matcher: "*"},
		Command:  "echo hello",
	}
	promptHook := &HookPrompt{
		HookBase: HookBase{Type: HookTypePrompt, Matcher: "*"},
		Prompt:   "evaluate this",
	}
	agentHook := &HookAgent{
		HookBase: HookBase{Type: HookTypeAgent, Matcher: "*"},
		Prompt:   "verify operation",
	}
	callbackHook := &HookCallback{
		HookBase: HookBase{Type: HookTypeCallback, Matcher: "*"},
		Callback: func(p HookPayload) HookResult { return HookResult{Action: ActionAllow} },
	}

	var _ Hook = httpHook
	var _ Hook = cmdHook
	var _ Hook = promptHook
	var _ Hook = agentHook
	var _ Hook = callbackHook

	// 验证 2：编译期类型安全 — 每种类型只能访问自己的字段
	// 以下行如果取消注释会导致编译错误（证明了类型安全）：
	// _ = httpHook.Command    // HookHTTP 没有 Command 字段
	// _ = httpHook.Prompt     // HookHTTP 没有 Prompt 字段
	// _ = cmdHook.URL         // HookCommand 没有 URL 字段
	// _ = cmdHook.Prompt      // HookCommand 没有 Prompt 字段
	// _ = promptHook.Command  // HookPrompt 没有 Command 字段
	// _ = promptHook.URL      // HookPrompt 没有 URL 字段

	// 验证 3：通过 Base() 访问公共字段
	if httpHook.Base().Type != HookTypeHTTP {
		t.Errorf("BUG [FACT-HOOK-004]: HookHTTP Base().Type mismatch\n  期望: %s\n  实际: %s",
			HookTypeHTTP, httpHook.Base().Type)
	}
	if cmdHook.Base().Type != HookTypeCommand {
		t.Errorf("BUG [FACT-HOOK-004]: HookCommand Base().Type mismatch\n  期望: %s\n  实际: %s",
			HookTypeCommand, cmdHook.Base().Type)
	}
	if promptHook.Base().Type != HookTypePrompt {
		t.Errorf("BUG [FACT-HOOK-004]: HookPrompt Base().Type mismatch\n  期望: %s\n  实际: %s",
			HookTypePrompt, promptHook.Base().Type)
	}

	// 验证 4：类型断言只能访问对应类型的字段
	// HookHTTP 不能断言为 *HookCommand
	if _, ok := interface{}(httpHook).(*HookCommand); ok {
		t.Error("BUG [FACT-HOOK-004]: HookHTTP should not be assertable to *HookCommand")
	}
	// HookCommand 不能断言为 *HookHTTP
	if _, ok := interface{}(cmdHook).(*HookHTTP); ok {
		t.Error("BUG [FACT-HOOK-004]: HookCommand should not be assertable to *HookHTTP")
	}

	// 验证 5：Clone() 返回相同类型
	httpClone := httpHook.Clone()
	if _, ok := httpClone.(*HookHTTP); !ok {
		t.Error("BUG [FACT-HOOK-004]: HookHTTP.Clone() should return *HookHTTP")
	}
	cmdClone := cmdHook.Clone()
	if _, ok := cmdClone.(*HookCommand); !ok {
		t.Error("BUG [FACT-HOOK-004]: HookCommand.Clone() should return *HookCommand")
	}

	// 验证 6：hookRaw.toHook() 根据 Type 正确分发
	raw := hookRaw{
		Type:    HookTypeHTTP,
		Matcher: "*",
		URL:     "http://test.com",
		Command: "should be ignored", // http 类型应忽略 Command
	}
	h := raw.toHook()
	if _, ok := h.(*HookHTTP); !ok {
		t.Errorf("BUG [FACT-HOOK-004]: hookRaw{Type:http}.toHook() should return *HookHTTP, got %T", h)
	}
	httpResult, ok := h.(*HookHTTP)
	if !ok || httpResult.URL != "http://test.com" {
		t.Errorf("BUG [FACT-HOOK-004]: HookHTTP URL mismatch")
	}
	// Command 字段应不存在于 HookHTTP（通过反射验证）
	httpType := reflect.TypeOf(*httpResult)
	for i := 0; i < httpType.NumField(); i++ {
		if httpType.Field(i).Name == "Command" {
			t.Error("BUG [FACT-HOOK-004]: HookHTTP should not have Command field")
		}
	}
}

// TestBug_MergeIntoMissingHookField 对应 FACT-CONFIG-002
// 配置合并必须覆盖所有字段（hook 配置合并相关）
// 当前实现：config.go mergeInto 不合并 Hooks 字段
func TestBug_MergeIntoMissingHookField(t *testing.T) {
	// FACT-CONFIG-002
	// config.mergeInto (config.go:258-299) 不合并 Hooks 字段
	// 此测试需要在 config 包中运行（因 import cycle 无法在 hook 包中直接测试）
	t.Skip("FACT-CONFIG-002: config.mergeInto 不合并 Hooks 字段。\n" +
		"  原因: hook 包不能 import config 包（循环依赖）\n" +
		"  手动验证: 在 config 包中运行 TestBug_MergeIntoMissingFields\n" +
		"  验证步骤:\n" +
		"  1. 创建含 Hooks 字段的 raw map: {\"hooks\": {\"pre_tool_use\": [...]}}\n" +
		"  2. 调用 mergeInto(&cfg, raw)\n" +
		"  3. 检查 cfg.Hooks.PreToolUse 是否非空（当前为空，证明 Hooks 未被合并）\n" +
		"  代码位置: internal/config/config.go:258-299 mergeInto 函数\n" +
		"  缺失字段: Hooks, McpServers")
}
