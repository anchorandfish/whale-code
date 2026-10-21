// Package migrate 提供从 codex / claude-code 迁移配置到 whale 的能力。
// V3-7-1：支持 `whale migrate --from codex` 和 `whale migrate --from claude-code` 子命令。
// 仅导入必要的 API Key + Provider 信息，权限规则因语义差异不自动迁移（仅提示）。
package migrate

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/anchorandfish/whale-code/internal/config"
)

// codexConfigPath codex CLI 配置文件相对路径（相对 ~/.codex/）。
const codexConfigPath = "config.toml"

// claudeCodeConfigPath claude-code 配置文件相对路径（相对 ~/.claude/）。
const claudeCodeConfigPath = "settings.json"

// MigrateFromCodex 从 ~/.codex/ 导入配置到 whale。
// homeDir: 用户 home 目录（~）；返回：导入的配置项描述列表 + 错误。
// 行为：
//   - 检测 ~/.codex/ 是否存在
//   - 读取 config.toml 中的 API Key（codex 使用 OPENAI_API_KEY 或 config.toml 中 model_providers）
//   - 优先读取环境变量 OPENAI_API_KEY 作为 codex 默认 provider 的 key
//   - 导入到 whale 的 ~/.whale/settings.json（Provider=openai, APIKey=<key>）
//   - 不导入权限规则（codex sandbox 语义与 whale 不同）
func MigrateFromCodex(homeDir string) ([]string, error) {
	codexDir := filepath.Join(homeDir, ".codex")
	exists, _ := detectCodexConfig(codexDir)
	if !exists {
		return nil, fmt.Errorf("未检测到 codex 配置目录 %s，请先安装并配置 codex CLI", codexDir)
	}

	var imported []string

	// 1. 尝试从环境变量读取 OPENAI_API_KEY（codex 默认使用 OpenAI API）
	apiKey := os.Getenv("OPENAI_API_KEY")
	if apiKey == "" {
		// 2. 尝试从 codex config.toml 解析（TOML 简化解析：仅匹配 api_key 字段）
		apiKey = parseCodexAPIKey(filepath.Join(codexDir, codexConfigPath))
	}
	if apiKey == "" {
		return imported, fmt.Errorf("未在 codex 配置或环境变量中找到 API Key，请设置 OPENAI_API_KEY 后重试")
	}

	// 3. 导入到 whale 配置
	if err := importAPIKey("openai", apiKey); err != nil {
		return imported, fmt.Errorf("导入 API Key 失败: %w", err)
	}
	imported = append(imported, fmt.Sprintf("API Key (provider=openai, key=%s)", maskKey(apiKey)))

	// 4. 提示权限规则不迁移
	imported = append(imported, "提示：codex sandbox 权限规则未自动迁移（语义不同），请手动配置 whale 权限模式")

	return imported, nil
}

// MigrateFromClaudeCode 从 ~/.claude/ 导入配置到 whale。
// homeDir: 用户 home 目录（~）；返回：导入的配置项描述列表 + 错误。
// 行为：
//   - 检测 ~/.claude/ 是否存在
//   - 读取 settings.json 中的 apiKeyHelper 或环境变量 ANTHROPIC_API_KEY
//   - 导入到 whale 的 ~/.whale/settings.json（Provider=anthropic, APIKey=<key>）
//   - 不导入权限规则（claude-code permissions 语义与 whale 不同）
func MigrateFromClaudeCode(homeDir string) ([]string, error) {
	claudeDir := filepath.Join(homeDir, ".claude")
	exists, _ := detectClaudeCodeConfig(claudeDir)
	if !exists {
		return nil, fmt.Errorf("未检测到 claude-code 配置目录 %s，请先安装并配置 claude-code", claudeDir)
	}

	var imported []string

	// 1. 尝试从环境变量读取 ANTHROPIC_API_KEY（claude-code 默认使用 Anthropic API）
	apiKey := os.Getenv("ANTHROPIC_API_KEY")
	if apiKey == "" {
		// 2. 尝试从 claude-code settings.json 解析（apiKeyHelper 字段非空时提示用户手动配置）
		apiKey = parseClaudeCodeAPIKey(filepath.Join(claudeDir, claudeCodeConfigPath))
	}
	if apiKey == "" {
		return imported, fmt.Errorf("未在 claude-code 配置或环境变量中找到 API Key，请设置 ANTHROPIC_API_KEY 后重试")
	}

	// 3. 导入到 whale 配置
	if err := importAPIKey("anthropic", apiKey); err != nil {
		return imported, fmt.Errorf("导入 API Key 失败: %w", err)
	}
	imported = append(imported, fmt.Sprintf("API Key (provider=anthropic, key=%s)", maskKey(apiKey)))

	// 4. 提示权限规则不迁移
	imported = append(imported, "提示：claude-code permissions 规则未自动迁移（语义不同），请手动配置 whale 权限模式")

	return imported, nil
}

// detectCodexConfig 检测 ~/.codex/ 是否存在配置文件。
// codexDir: ~/.codex/ 路径；返回：是否存在 + 配置文件路径。
// 检测规则：目录存在 + config.toml 存在 → 视为已配置。
func detectCodexConfig(codexDir string) (bool, string) {
	if codexDir == "" {
		return false, ""
	}
	info, err := os.Stat(codexDir)
	if err != nil || !info.IsDir() {
		return false, ""
	}
	configFile := filepath.Join(codexDir, codexConfigPath)
	if _, err := os.Stat(configFile); err != nil {
		// 目录存在但 config.toml 不存在，仍视为已配置（可能仅用环境变量）
		return true, ""
	}
	return true, configFile
}

// detectClaudeCodeConfig 检测 ~/.claude/ 是否存在配置文件。
// claudeDir: ~/.claude/ 路径；返回：是否存在 + 配置文件路径。
// 检测规则：目录存在 + settings.json 存在 → 视为已配置。
func detectClaudeCodeConfig(claudeDir string) (bool, string) {
	if claudeDir == "" {
		return false, ""
	}
	info, err := os.Stat(claudeDir)
	if err != nil || !info.IsDir() {
		return false, ""
	}
	configFile := filepath.Join(claudeDir, claudeCodeConfigPath)
	if _, err := os.Stat(configFile); err != nil {
		// 目录存在但 settings.json 不存在，仍视为已配置（可能仅用环境变量）
		return true, ""
	}
	return true, configFile
}

// importAPIKey 从外部配置导入 API Key 到 whale 配置。
// provider: AI provider（anthropic/openai）；apiKey: API Key 明文。
// 行为：
//   - 加载 ~/.whale/settings.json（不存在时用默认配置）
//   - 设置 Provider + APIKey 字段
//   - 写回 settings.json（保留其他字段 + auth 透传）
func importAPIKey(provider, apiKey string) error {
	homeDir, err := os.UserHomeDir()
	if err != nil {
		return fmt.Errorf("获取用户 home 目录失败: %w", err)
	}

	loader := config.NewLoader(homeDir, "")
	cfg, err := loader.Load()
	if err != nil {
		return fmt.Errorf("加载 whale 配置失败: %w", err)
	}

	cfg.Provider = provider
	cfg.APIKey = apiKey
	// 同时设置 Model 默认值（避免 onboarding 流程再次提示）
	if cfg.Model == "" {
		switch provider {
		case "anthropic":
			cfg.Model = "claude-sonnet-4-5"
		case "openai":
			cfg.Model = "gpt-4o"
		}
	}
	cfg.Onboarded = true

	if err := loader.Save(cfg); err != nil {
		return fmt.Errorf("保存 whale 配置失败: %w", err)
	}
	return nil
}

// parseCodexAPIKey 简化解析 codex config.toml 中的 api_key 字段。
// codex 的 TOML 格式复杂，这里仅做最小化字符串匹配：
// 匹配 `api_key = "..."` 或 `api_key='...'` 行。
// configFile: codex config.toml 路径；返回：API Key 或空字符串。
func parseCodexAPIKey(configFile string) string {
	data, err := os.ReadFile(configFile)
	if err != nil {
		return ""
	}
	content := string(data)
	lines := strings.Split(content, "\n")
	for _, line := range lines {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "#") {
			continue
		}
		// 匹配 api_key = "..." 或 api_key = '...'
		if strings.HasPrefix(line, "api_key") {
			idx := strings.Index(line, "=")
			if idx < 0 {
				continue
			}
			val := strings.TrimSpace(line[idx+1:])
			// 去除引号
			val = strings.Trim(val, `"'`)
			if val != "" {
				return val
			}
		}
	}
	return ""
}

// parseClaudeCodeAPIKey 解析 claude-code settings.json 中的 API Key。
// claude-code 的 settings.json 是 JSON 格式，但通常不含明文 key（使用 apiKeyHelper 脚本）。
// 这里优先解析顶层 apiKey 字段（部分版本支持），找不到则返回空。
// configFile: claude-code settings.json 路径；返回：API Key 或空字符串。
func parseClaudeCodeAPIKey(configFile string) string {
	data, err := os.ReadFile(configFile)
	if err != nil {
		return ""
	}
	var raw map[string]interface{}
	if err := json.Unmarshal(data, &raw); err != nil {
		return ""
	}
	// claude-code 部分版本支持顶层 apiKey 字段
	if v, ok := raw["apiKey"].(string); ok && v != "" {
		return v
	}
	return ""
}

// maskKey 脱敏 API Key，避免在迁移日志中泄露完整 key。
// 格式：前4位 + *** + 后4位；长度 ≤ 8 时返回 ***。
func maskKey(key string) string {
	if len(key) <= 8 {
		return "***"
	}
	return key[:4] + "***" + key[len(key)-4:]
}
