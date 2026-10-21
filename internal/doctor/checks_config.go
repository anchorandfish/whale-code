package doctor

import (
	"os"
	"strconv"

	"github.com/anchorandfish/whale-code/internal/config"
)

// checkConfig 检测配置完整性（5 项）。
// 检测：settings.json 存在性、API Key 非空、Model 字段非空、permission_mode 合法、context_window ≥ 4096（仅提示）。
// 对标同类 CLI Invalid Settings 区块。
// 不修改 config.Validate()，必填字段检测在 doctor 内做（避免影响启动流程）。
func (d *Doctor) checkConfig() []CheckResult {
	var results []CheckResult

	// 1. settings.json 存在性（用传入的 settingsPath，因 Config 无 SettingsPath 字段）
	results = append(results, d.checkSettingsFile())

	// 2. API Key 非空（检测环境变量，对标 main.go:731-745 checkAPI 逻辑）
	results = append(results, d.checkAPIKey())

	// 3. Model 字段非空
	results = append(results, d.checkModel())

	// 4. permission_mode 合法
	results = append(results, d.checkPermissionMode())

	// 5. context_window ≥ 4096
	results = append(results, d.checkContextWindow())

	return results
}

// checkSettingsFile 检测 settings.json 是否存在且可解析。
// 不存在时标记 Warn（非阻塞，可用默认配置启动）并建议运行 /onboarding。
func (d *Doctor) checkSettingsFile() CheckResult {
	if d.settingsPath == "" {
		return CheckResult{
			Category:   "Config",
			Name:       "settings.json",
			Severity:   SeverityWarn,
			Message:    "settings path not provided",
			FixSuggest: "run /onboarding to initialize config",
		}
	}
	if _, err := os.Stat(d.settingsPath); err != nil {
		return CheckResult{
			Category:   "Config",
			Name:       "settings.json",
			Severity:   SeverityWarn,
			Message:    "not found at " + d.settingsPath,
			FixSuggest: "run /onboarding to initialize config",
		}
	}
	return CheckResult{
		Category: "Config",
		Name:     "settings.json",
		Severity: SeverityOK,
		Message:  d.settingsPath + " (found)",
	}
}

// checkAPIKey 检测 API Key 是否已设置（环境变量优先，回退 settings.json）。
// 复用 main.go resolveProviderConfig 的优先级逻辑（env > settings.json）。
// API Key 缺失时标记 Fatal（阻塞 API 连通性），但不阻塞其他检测项执行。
// P5 修复：原实现仅查环境变量，settings.json 已配置 api_key 时仍误报 FATAL。
func (d *Doctor) checkAPIKey() CheckResult {
	// 1. 优先环境变量（与 resolveProviderConfig 一致）
	anthropicKey := os.Getenv("ANTHROPIC_API_KEY")
	openaiKey := os.Getenv("OPENAI_API_KEY")

	key := anthropicKey
	provider := "anthropic"
	if key == "" {
		key = openaiKey
		provider = "openai"
	}

	// 2. 环境变量均空时回退 settings.json 的 api_key 字段
	if key == "" && d.cfg != nil && d.cfg.APIKey != "" {
		key = d.cfg.APIKey
		provider = "settings.json"
		if d.cfg.Provider != "" {
			provider = d.cfg.Provider
		}
	}

	if key == "" {
		return CheckResult{
			Category:   "Config",
			Name:       "API Key",
			Severity:   SeverityFatal,
			Message:    "NOT SET",
			FixSuggest: "set ANTHROPIC_API_KEY or OPENAI_API_KEY env var, or api_key in settings.json",
		}
	}
	// 脱敏显示（对标 main.go:746 ai.MaskAPIKey）
	masked := maskKey(key)
	return CheckResult{
		Category: "Config",
		Name:     "API Key",
		Severity: SeverityOK,
		Message:  provider + ": " + masked,
	}
}

// checkModel 检测 Model 字段是否非空。
// Model 为空时标记 Fatal（无目标模型无法发起请求）。
// 修复：原实现只读 d.cfg.Model，不读 WHALE_MODEL 环境变量，
// 但 FixSuggest 声称 "set WHALE_MODEL environment variable"——这是失实。
// 修复后：d.cfg.Model 为空时 fallback 读取 os.Getenv("WHALE_MODEL")，
// 使 FixSuggest 描述的行为真实生效（与 main.go loadOrCreateModelRegistry 一致）。
func (d *Doctor) checkModel() CheckResult {
	model := ""
	if d.cfg != nil {
		model = d.cfg.Model
	}
	// Fallback: 读取 WHALE_MODEL 环境变量（与 FixSuggest 描述一致）
	if model == "" {
		model = os.Getenv("WHALE_MODEL")
	}
	if model == "" {
		return CheckResult{
			Category:   "Config",
			Name:       "Model",
			Severity:   SeverityFatal,
			Message:    "empty",
			FixSuggest: "set model in settings.json or WHALE_MODEL environment variable",
		}
	}
	return CheckResult{
		Category: "Config",
		Name:     "Model",
		Severity: SeverityOK,
		Message:  model,
	}
}

// checkPermissionMode 检测 permission_mode 是否合法。
// 复用 config.Validate() 的合法值列表（config.go:666-669），但不修改 config 模块。
func (d *Doctor) checkPermissionMode() CheckResult {
	if d.cfg == nil {
		return CheckResult{
			Category: "Config",
			Name:     "permission_mode",
			Severity: SeverityWarn,
			Message:  "config not loaded",
		}
	}
	validModes := map[config.PermissionMode]bool{
		config.PermReadOnly:         true,
		config.PermWorkspaceWrite:   true,
		config.PermDangerFullAccess: true,
	}
	if !validModes[d.cfg.PermissionMode] {
		return CheckResult{
			Category:   "Config",
			Name:       "permission_mode",
			Severity:   SeverityWarn,
			Message:    "invalid: " + string(d.cfg.PermissionMode),
			FixSuggest: "use read-only, workspace-write, or danger-full-access",
		}
	}
	return CheckResult{
		Category: "Config",
		Name:     "permission_mode",
		Severity: SeverityOK,
		Message:  string(d.cfg.PermissionMode),
	}
}

// checkContextWindow 检测 context_window 是否 ≥ 4096。
// 小于 4096 时标记 Warn（仅诊断提示，不回填：小窗口是合法的压缩阈值测试配置，
// config.Validate 已不再强制回填 4096，详见 config.go Validate 注释）。
func (d *Doctor) checkContextWindow() CheckResult {
	if d.cfg == nil {
		return CheckResult{
			Category: "Config",
			Name:     "context_window",
			Severity: SeverityWarn,
			Message:  "config not loaded",
		}
	}
	if d.cfg.ContextWindow > 0 && d.cfg.ContextWindow < 4096 {
		return CheckResult{
			Category:   "Config",
			Name:       "context_window",
			Severity:   SeverityWarn,
			Message:    formatInt(d.cfg.ContextWindow) + " (recommended >= 4096)",
			FixSuggest: "increase context_window in settings.json",
		}
	}
	return CheckResult{
		Category: "Config",
		Name:     "context_window",
		Severity: SeverityOK,
		Message:  formatInt(d.cfg.ContextWindow),
	}
}

// maskKey 完全隐藏 API Key，避免终端截图和诊断日志泄露凭据片段。
func maskKey(key string) string {
	return "***"
}

// formatInt 将整数格式化为字符串（strconv.Itoa 的简短别名）。
func formatInt(n int) string {
	return strconv.Itoa(n)
}
