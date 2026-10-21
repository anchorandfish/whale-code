package hook

import (
	"encoding/json"
	"fmt"
	"os"
	"path"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/anchorandfish/whale-code/internal/sandbox"
	"github.com/anchorandfish/whale-code/internal/secret"
)

// truncateEnvValue 截断超长环境变量值并追加截断标记
func truncateEnvValue(value string, maxSize int) string {
	if len(value) <= maxSize {
		return value
	}
	return value[:maxSize] + "\n... (truncated)"
}

// buildHookEnv 构建 hook 子进程的环境变量列表
// H3 收口：白名单过滤改调 secret.ScrubEnv（守 02 §2.3.1 契约，行为等价）。
//
// PATH 安全过滤：对 PATH 值调用 sandbox.FilterPathEntries，
// 移除包含敏感目录（.ssh/.gnupg/.aws/.config 等）的条目，
// 防止 hook 子进程通过 PATH 中的可执行文件访问敏感目录。
// 沙箱启用时 sctx.Env 会整体覆盖此结果，但 PATH 过滤仍在此处生效，
// 保证非沙箱路径下 hook 子进程同样受到保护。
func buildHookEnv(payload HookPayload, eventType HookEventType) []string {
	env := secret.ScrubEnv(os.Environ())

	// PATH 值过滤：移除包含敏感目录的条目（如 ~/.ssh/bin）
	for i, kv := range env {
		name, value, ok := strings.Cut(kv, "=")
		if !ok || name != "PATH" {
			continue
		}
		env[i] = "PATH=" + sandbox.FilterPathEntries(value, nil)
		break
	}

	env = append(env, fmt.Sprintf("HOOK_EVENT=%s", eventType))

	if payload.ToolName != "" {
		env = append(env, fmt.Sprintf("HOOK_TOOL_NAME=%s", payload.ToolName))
	}
	if payload.ToolInputJSON != "" {
		env = append(env, fmt.Sprintf("HOOK_TOOL_INPUT=%s", truncateEnvValue(payload.ToolInputJSON, maxToolInputEnvSize)))
	}
	if payload.ToolOutput != "" {
		env = append(env, fmt.Sprintf("HOOK_TOOL_OUTPUT=%s", truncateEnvValue(payload.ToolOutput, maxToolOutputEnvSize)))
	}
	if payload.ToolResultIsError {
		env = append(env, "HOOK_TOOL_IS_ERROR=true")
	}
	if payload.SessionPath != "" {
		env = append(env, fmt.Sprintf("HOOK_SESSION_PATH=%s", payload.SessionPath))
	}
	if payload.MessageCount > 0 {
		env = append(env, fmt.Sprintf("HOOK_MESSAGE_COUNT=%d", payload.MessageCount))
	}
	if payload.RemovedMessageCount > 0 {
		env = append(env, fmt.Sprintf("HOOK_REMOVED_MESSAGE_COUNT=%d", payload.RemovedMessageCount))
	}
	if payload.ResultingMessageCount > 0 {
		env = append(env, fmt.Sprintf("HOOK_RESULTING_MESSAGE_COUNT=%d", payload.ResultingMessageCount))
	}
	if payload.FilePath != "" {
		env = append(env, fmt.Sprintf("HOOK_FILE=%s", payload.FilePath))
		env = append(env, fmt.Sprintf("HOOK_FILE_PATH=%s", payload.FilePath))
	}
	if payload.Command != "" {
		env = append(env, fmt.Sprintf("HOOK_COMMAND=%s", payload.Command))
	}

	return env
}

// matchHook 在钩子列表中查找匹配的钩子
func matchHook(hooks []Hook, toolName, filePath string) Hook {
	for i := range hooks {
		h := hooks[i]
		b := h.Base()
		if !matchGlob(b.Matcher, toolName) {
			continue
		}
		if b.Pattern != "" && filePath != "" && !matchGlob(b.Pattern, filePath) {
			continue
		}
		if b.Pattern != "" && filePath == "" {
			continue
		}
		return h
	}
	return nil
}

// matchGlob 匹配 glob 模式
func matchGlob(pattern, name string) bool {
	if pattern == "*" {
		return true
	}
	if pattern == name {
		return true
	}
	normalized := filepath.ToSlash(name)
	if !strings.Contains(pattern, "/") {
		matched, err := path.Match(pattern, filepath.Base(normalized))
		return err == nil && matched
	}
	matched, err := path.Match(pattern, normalized)
	return err == nil && matched
}

// extractPathFromInput 从工具输入 JSON 中提取 path 字段
func extractPathFromInput(input string) string {
	var parsed map[string]interface{}
	if err := json.Unmarshal([]byte(input), &parsed); err != nil {
		return ""
	}
	if p, ok := parsed["path"].(string); ok {
		return p
	}
	return ""
}

// parseSimpleYAMLInto 简单 YAML 解析器，将 YAML 解析到指定的 HookConfig（不修改 Manager 状态）
// 此函数为热重载场景提取，允许解析到临时配置再决定是否替换（M16-02）
//
// Deprecated: 使用 gopkg.in/yaml.v3 替代。
// 本函数仅保留为兼容入口，新代码应使用 yaml.Unmarshal。
// 简化版不支持多行字符串、锚点引用、流式语法等 YAML 1.2 特性。
// LoadConfig / parseConfigBytes 已切换到 yaml.v3，本函数不再被生产路径调用。
func parseSimpleYAMLInto(data []byte, cfg *HookConfig) error {
	lines := strings.Split(string(data), "\n")
	var currentRaw *hookRaw
	var hooks *[]Hook

	flushHook := func() {
		if currentRaw != nil && hooks != nil {
			*hooks = append(*hooks, currentRaw.toHook())
			currentRaw = nil
		}
	}

	for _, line := range lines {
		trimmed := strings.TrimRight(line, " \t\r")
		if trimmed == "" || strings.HasPrefix(strings.TrimLeft(trimmed, " "), "#") {
			continue
		}

		indent := len(line) - len(strings.TrimLeft(line, " \t"))
		content := strings.TrimLeft(line, " \t")

		if indent == 0 && strings.Contains(content, ":") {
			key := strings.TrimSpace(strings.SplitN(content, ":", 2)[0])
			flushHook()
			currentRaw = nil
			hooks = yamlKeyToHooks(cfg, key)
			continue
		}

		if hooks == nil {
			continue
		}

		if strings.HasPrefix(content, "- ") {
			flushHook()
			currentRaw = &hookRaw{}
			rest := strings.TrimPrefix(content, "- ")
			applyKeyValue(rest, currentRaw)
			continue
		}

		if currentRaw != nil && strings.Contains(content, ":") {
			applyKeyValue(content, currentRaw)
		}
	}

	flushHook()
	return nil
}

// yamlKeyToHooks 将 YAML 键名映射到 HookConfig 对应字段
func yamlKeyToHooks(cfg *HookConfig, key string) *[]Hook {
	switch key {
	case "pre_tool_use":
		cfg.PreToolUse = []Hook{}
		return &cfg.PreToolUse
	case "post_tool_use":
		cfg.PostToolUse = []Hook{}
		return &cfg.PostToolUse
	case "pre_read":
		cfg.PreRead = []Hook{}
		return &cfg.PreRead
	case "post_read":
		cfg.PostRead = []Hook{}
		return &cfg.PostRead
	case "pre_write":
		cfg.PreWrite = []Hook{}
		return &cfg.PreWrite
	case "post_write":
		cfg.PostWrite = []Hook{}
		return &cfg.PostWrite
	case "pre_edit":
		cfg.PreEdit = []Hook{}
		return &cfg.PreEdit
	case "post_edit":
		cfg.PostEdit = []Hook{}
		return &cfg.PostEdit
	case "pre_bash":
		cfg.PreBash = []Hook{}
		return &cfg.PreBash
	case "post_bash":
		cfg.PostBash = []Hook{}
		return &cfg.PostBash
	case "pre_search":
		cfg.PreSearch = []Hook{}
		return &cfg.PreSearch
	case "post_search":
		cfg.PostSearch = []Hook{}
		return &cfg.PostSearch
	case "pre_git":
		cfg.PreGit = []Hook{}
		return &cfg.PreGit
	case "post_git":
		cfg.PostGit = []Hook{}
		return &cfg.PostGit
	case "pre_web":
		cfg.PreWeb = []Hook{}
		return &cfg.PreWeb
	case "post_web":
		cfg.PostWeb = []Hook{}
		return &cfg.PostWeb
	case "pre_agent":
		cfg.PreAgent = []Hook{}
		return &cfg.PreAgent
	case "post_agent":
		cfg.PostAgent = []Hook{}
		return &cfg.PostAgent
	case "session_start":
		cfg.SessionStart = []Hook{}
		return &cfg.SessionStart
	case "session_stop":
		cfg.SessionStop = []Hook{}
		return &cfg.SessionStop
	case "stop":
		cfg.Stop = []Hook{}
		return &cfg.Stop
	case "post_compact":
		cfg.PostCompact = []Hook{}
		return &cfg.PostCompact
	case "notification":
		cfg.Notification = []Hook{}
		return &cfg.Notification
	default:
		return nil
	}
}

// applyKeyValue 将键值对应用到 hookRaw 中间结构
func applyKeyValue(kv string, r *hookRaw) {
	parts := strings.SplitN(kv, ":", 2)
	if len(parts) != 2 {
		return
	}
	key := strings.TrimSpace(parts[0])
	value := strings.TrimSpace(parts[1])
	value = strings.Trim(value, "\"'")

	switch key {
	case "matcher":
		r.Matcher = value
	case "pattern":
		r.Pattern = value
	case "action":
		r.Action = Action(value)
	case "reason":
		r.Reason = value
	case "command":
		r.Command = value
	case "type":
		r.Type = HookType(value)
	case "prompt":
		r.Prompt = value
	case "model":
		r.Model = value
	case "url":
		r.URL = value
	case "timeout":
		if v, err := strconv.Atoi(value); err == nil {
			r.Timeout = v
		}
	}
}
