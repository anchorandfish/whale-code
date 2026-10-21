package tui

import (
	"fmt"
	"os"
	"strings"

	"github.com/anchorandfish/whale-code/internal/ai"
	"github.com/anchorandfish/whale-code/internal/config"
	ctxpkg "github.com/anchorandfish/whale-code/internal/context"
)

// findFastModel finds fast models from the model list (prefer haiku-class)
func findFastModel(models []string) string {
	for _, m := range models {
		lower := strings.ToLower(m)
		if strings.Contains(lower, "haiku") {
			return m
		}
	}
	for _, m := range models {
		lower := strings.ToLower(m)
		if strings.Contains(lower, "mini") || strings.Contains(lower, "flash") || strings.Contains(lower, "lite") {
			return m
		}
	}
	if len(models) > 0 {
		return models[0]
	}
	return ""
}

// bindFastModeCallbacks 将 /fast 回调绑定到当前 TUI Model，避免 Registry 闭包引用初始化期旧副本。
func (m *Model) bindFastModeCallbacks() {
	if m == nil || m.cmdRegistry == nil {
		return
	}
	m.cmdRegistry.OnGetFastModeState = func() (config.FastModeState, string, string) {
		fastModel := m.config.FastModel
		defaultModel := m.config.Model
		if m.config.FastModeEnabled {
			return config.FastModeOn, fastModel, defaultModel
		}
		return config.FastModeOff, fastModel, defaultModel
	}
	m.cmdRegistry.OnSetFastMode = func(enabled bool) error {
		if enabled && m.config.FastModel == "" {
			return fmt.Errorf("fast_model 未配置，请在 settings.json 设置 fast_model 或通过 WHALE_FAST_MODEL 环境变量配置")
		}
		m.config.FastModeEnabled = enabled
		targetModel := m.config.Model
		if enabled {
			targetModel = m.config.FastModel
		}
		if err := m.switchRuntimeModel(targetModel); err != nil {
			return err
		}
		loader := config.NewLoader(m.sessionService.HomeDir(), m.sessionService.WorkDir())
		return loader.Save(m.config)
	}
}

// buildWelcome build welcome message for first-time users
// buildAvailableModels builds the list of model names the user has actually configured.
// It collects the primary model and fallback model from config.
func buildAvailableModels(cfg *config.Config) []string {
	var models []string
	if cfg.Model != "" {
		models = append(models, cfg.Model)
	}
	if cfg.FallbackModel != "" {
		models = append(models, cfg.FallbackModel)
	}
	return models
}

// handleModelCommand handles /model command and subcommands (add/remove/edit/info)
func (m *Model) handleModelCommand(args string) string {
	// No args: show current model details
	if args == "" {
		cfg := m.aiService.ModelRegistry().Get(m.sessionService.ModelName())
		if cfg != nil {
			return ai.FormatModelInfo(cfg)
		}
		return fmt.Sprintf("Model: %s", m.sessionService.ModelName())
	}

	// Subcommands: add
	if strings.HasPrefix(args, "add ") {
		return m.handleModelAdd(strings.TrimPrefix(args, "add "))
	}
	if args == "add" {
		return "Usage: /model add <name> [--base-url <url>] [--api-key <key>] [--provider <openai|anthropic>]\n" +
			" [--context-window <tokens>] [--max-output <tokens>]\n" +
			" [--supports-thinking <supported|unsupported>] [--thinking-evidence <url>]\n\n" +
			"--supports-thinking declares the model's thinking capability. The declaration\n" +
			"wins over the built-in capability table and prefix inference. Omit to keep the\n" +
			"current value (new models: unset => unknown => the parameter is not sent).\n" +
			"--thinking-evidence records where that declaration comes from (vendor doc URL,\n" +
			"capture artifact path). Recorded and shown by /model info; never verified.\n" +
			"  --supports-thinking 声明该模型是否支持 thinking：声明优先于能力表与前缀推断；\n" +
			"  不写则沿用原值（新模型 = 未声明 = 未知 = 不下发该参数）。\n" +
			"  --thinking-evidence 记录该声明的来源（厂商文档 URL / 抓包产物路径）：只登记并在\n" +
			"  /model info 展示，不做校验。\n\n" +
			"Unspecified fields inherit defaults from current model config"
	}

	// Subcommands: edit
	if strings.HasPrefix(args, "edit ") {
		return m.handleModelEdit(strings.TrimPrefix(args, "edit "))
	}
	if args == "edit" {
		return "Usage: /model edit <name> [--base-url <url>] [--api-key <key>] [--provider <openai|anthropic>]\n" +
			" [--context-window <tokens>] [--max-output <tokens>]\n" +
			" [--supports-thinking <supported|unsupported>] [--thinking-evidence <url>]\n\n" +
			"--supports-thinking overrides the built-in capability table and prefix\n" +
			"inference for this model (declaration wins). Omit to keep the current value.\n" +
			"--thinking-evidence records where that declaration comes from; shown by\n" +
			"/model info and never verified. Omit to keep the current value.\n" +
			"  --supports-thinking 声明该模型是否支持 thinking：声明优先于能力表与前缀推断；\n" +
			"  不写则保留原值。\n" +
			"  --thinking-evidence 记录该声明的来源：只登记并在 /model info 展示，不做校验；\n" +
			"  不写则保留原值。\n\n" +
			"Only modify specified fields; unspecified fields keep original values"
	}

	// Subcommands: remove
	if strings.HasPrefix(args, "remove ") {
		return m.handleModelRemove(strings.TrimPrefix(args, "remove "))
	}
	if args == "remove" {
		return "Usage: /model remove <name>"
	}

	// Subcommands: info
	if strings.HasPrefix(args, "info ") {
		name := strings.TrimSpace(strings.TrimPrefix(args, "info "))
		cfg := m.aiService.ModelRegistry().Get(name)
		if cfg == nil {
			return ai.ModelNotConfiguredHint(name, m.aiService.ModelRegistry())
		}
		return ai.FormatModelInfo(cfg)
	}
	if args == "info" {
		return "Usage: /model info <name>"
	}

	// Default: switch model
	return m.handleModelSwitch(args)
}

// handleModelSwitch 切换主模型（全链路联动：baseURL/apiKey/provider/contextWindow）
// P7-2：同步 Config.Model 并持久化到 settings.json（global 级，与同类 CLI code 一致）
func (m *Model) handleModelSwitch(name string) string {
	cfg := m.aiService.ModelRegistry().Get(name)
	if cfg == nil {
		return ai.ModelNotConfiguredHint(name, m.aiService.ModelRegistry())
	}
	m.sessionService.SetModelName(cfg.Name)
	m.aiService.Client().SetModelConfig(cfg)
	if caching := m.aiService.Caching(); caching != nil {
		caching.UpdateModel(cfg.Name)
	}
	m.aiService.Fallback().SetOriginalModel(cfg.Name)
	// P7-2：同步 Config.Model，避免 loader.Save 保存旧值
	m.sessionService.Config().Model = cfg.Name
	m.sessionService.Config().ContextWindow = cfg.ContextWindow
	// H8：Budget 从 Config 构造（原 DefaultBudget 硬编码阈值）
	m.aiService.SetBudget(ctxpkg.NewBudgetFromConfig(config.ResolveContextWindow(cfg.ContextWindow, cfg.Name), m.sessionService.Config()))
	// P7-2：持久化到 settings.json（复用 config 3-C 跨进程文件锁）
	loader := config.NewLoader(m.sessionService.HomeDir(), m.sessionService.WorkDir())
	if err := loader.Save(m.sessionService.Config()); err != nil {
		return fmt.Sprintf("Model: %s\nProvider: %s\nBase URL: %s\nContext: %d tokens\n⚠️ 持久化失败: %v",
			cfg.Name, cfg.Provider, cfg.BaseURL, cfg.ContextWindow, err)
	}
	return fmt.Sprintf("Model: %s\nProvider: %s\nBase URL: %s\nContext: %d tokens",
		cfg.Name, cfg.Provider, cfg.BaseURL, cfg.ContextWindow)
}

// switchRuntimeModel 仅切换运行时模型（aiClient/Fallback/Budget/ModelName），
// 不修改 Config.Model 且不持久化。用于 /fast 命令的临时切换，避免覆盖默认主模型。
// P7-3 修复（v2）：原 OnSetFastMode 复用 handleModelSwitch 会覆盖 Config.Model，
// 导致 /fast off 无法恢复原 Model（致命漏洞，子审查第 1 轮发现）。
// 与 handleModelSwitch 的区别：不写 Config.Model/ContextWindow，不调 loader.Save。
func (m *Model) switchRuntimeModel(name string) error {
	cfg := m.aiService.ModelRegistry().Get(name)
	if cfg == nil {
		return fmt.Errorf("%s", ai.ModelNotConfiguredHint(name, m.aiService.ModelRegistry()))
	}
	m.sessionService.SetModelName(cfg.Name)
	m.aiService.Client().SetModelConfig(cfg)
	if caching := m.aiService.Caching(); caching != nil {
		caching.UpdateModel(cfg.Name)
	}
	m.aiService.Fallback().SetOriginalModel(cfg.Name)
	// H8：Budget 从 Config 构造（运行时切换也需更新 Budget 阈值）
	m.aiService.SetBudget(ctxpkg.NewBudgetFromConfig(config.ResolveContextWindow(cfg.ContextWindow, cfg.Name), m.sessionService.Config()))
	return nil
}

// syncThinkingDeclaration 把 ModelConfig 上的 thinking 声明同步进 ai 的声明层（B1，2026-10-05）。
//
// ★ 这是 /model add 与 /model edit 的**共享**实现，TUI（commands_model.go）与
// plain（plain.go）两条入口都调它 —— 本仓栽过"命令双实现只接了一侧"的复发坑
// ，所以判定收在这一处，调用方只做薄转发。
//
// 语义：能解析 ⇒ 按声明登记（**声明优先**于能力表/前缀推断）；
// 解析不出（空串或非法值）⇒ 撤销声明，回到能力表/前缀推断 —— 这正是"未声明"。
// 详细判定见 internal/ai/capability_declaration.go 的 ThinkingSupportFor。
func syncThinkingDeclaration(cfg *ai.ModelConfig) {
	if cfg == nil {
		return
	}
	if t, ok := ai.ParseThinkingCapability(cfg.ThinkingSupport); ok {
		ai.RegisterThinkingDeclaration(cfg.Name, t)
		return
	}
	ai.RegisterThinkingDeclaration(cfg.Name, ai.ThinkingCapUnknown)
}

// clearThinkingDeclaration 撤销某模型的 thinking 声明（/model remove 时调用）。
//
// 不撤销的话，同名模型下次被 add 回来会带着一个**用户已经删掉的**声明 ——
// ai 的声明层是进程级的，删模型不会自动清它。
func clearThinkingDeclaration(name string) {
	ai.RegisterThinkingDeclaration(name, ai.ThinkingCapUnknown)
}

// handleModelAdd handles /model add subcommand
// Unspecified fields inherit from current model config; then supplemented by FillDefaults
func (m *Model) handleModelAdd(args string) string {
	cfg, err := ai.ParseModelAddArgs(args)
	if err != nil {
		return fmt.Sprintf("Error: %v\nUsage: /model add <name> [--base-url <url>] [--api-key <key>]", err)
	}

	// Inherit unspecified fields from current model config
	var addWarning string
	currentCfg := m.aiService.ModelRegistry().Get(m.sessionService.ModelName())
	if currentCfg != nil {
		if cfg.APIKey == "" {
			cfg.APIKey = currentCfg.APIKey
		}
		if cfg.BaseURL == "" {
			cfg.BaseURL = currentCfg.BaseURL
		}
		if cfg.Provider == "" {
			cfg.Provider = currentCfg.Provider
		}
		if cfg.ContextWindow == 0 {
			cfg.ContextWindow = currentCfg.ContextWindow
		}
		if cfg.MaxOutputTokens == 0 {
			cfg.MaxOutputTokens = currentCfg.MaxOutputTokens
		}
	}

	// Environment variables as fallback
	if cfg.APIKey == "" {
		cfg.APIKey = os.Getenv("OPENAI_API_KEY")
	}
	if cfg.BaseURL == "" && cfg.Provider != ai.ProviderAnthropic {
		cfg.BaseURL = os.Getenv("OPENAI_BASE_URL")
	}
	cfg.FillDefaults()

	if cfg.BaseURL != "" && currentCfg != nil && cfg.BaseURL != currentCfg.BaseURL && cfg.APIKey == currentCfg.APIKey {
		addWarning = "\n⚠️ Warning: API key inherited from current model but base URL differs. Ensure the key is valid for this provider."
	}

	// Check if it is an update
	isUpdate := m.aiService.ModelRegistry().Get(cfg.Name) != nil

	// B1：声明"未指定则保留"—— 否则 `/model add <已存在的名字>` 会把之前用
	// --supports-thinking 写下的声明静默抹掉（模型被重加一次就退化）。
	if cfg.ThinkingSupport == "" {
		if existing := m.aiService.ModelRegistry().Get(cfg.Name); existing != nil {
			cfg.ThinkingSupport = existing.ThinkingSupport
		}
	}
	// B3：来源与声明同理 —— 重加同名模型时未被显式指定就保留已记的来源。
	if cfg.ThinkingEvidence == "" {
		if existing := m.aiService.ModelRegistry().Get(cfg.Name); existing != nil {
			cfg.ThinkingEvidence = existing.ThinkingEvidence
		}
	}

	m.aiService.ModelRegistry().Add(cfg)
	// B1：让声明**当场生效**（不等重启）。与 main.go 的启动单点同源 ——
	// 那边负责"重启后仍在"，这边负责"这一刻就生效"。
	syncThinkingDeclaration(cfg)
	m.sessionService.SetAvailableModels(m.aiService.ModelRegistry().Names())

	// Persist
	if m.sessionService.Config().ModelsJSONPath != "" {
		if saveErr := ai.SaveModels(m.sessionService.Config().ModelsJSONPath, m.aiService.ModelRegistry()); saveErr != nil {
			return fmt.Sprintf("Model added: %s（but save failed: %v）", cfg.Name, saveErr)
		}
	}

	if isUpdate {
		return fmt.Sprintf("Model updated: %s", cfg.Name)
	}
	return fmt.Sprintf("Model added: %s\nProvider: %s\nBase URL: %s\nContext: %d tokens\nUse /model %s to switch to this model%s",
		cfg.Name, cfg.Provider, cfg.BaseURL, cfg.ContextWindow, cfg.Name, addWarning)
}

// handleModelRemove handles /model remove subcommand
func (m *Model) handleModelRemove(name string) string {
	name = strings.TrimSpace(name)
	if name == m.sessionService.ModelName() {
		return "Cannot remove the current model; please switch to another model first"
	}
	if !m.aiService.ModelRegistry().Remove(name) {
		return fmt.Sprintf("Model '%s' not found", name)
	}
	// B1：删模型必须同时撤销它的声明（声明层是进程级的，不会被 Remove 连带清掉）。
	clearThinkingDeclaration(name)
	m.sessionService.SetAvailableModels(m.aiService.ModelRegistry().Names())

	// Persist
	if m.sessionService.Config().ModelsJSONPath != "" {
		if saveErr := ai.SaveModels(m.sessionService.Config().ModelsJSONPath, m.aiService.ModelRegistry()); saveErr != nil {
			return fmt.Sprintf("Model removed: %s（but save failed: %v）", name, saveErr)
		}
	}
	return fmt.Sprintf("Model removed: %s", name)
}

// handleModelEdit handles /model edit subcommand; modify specified fields of existing model
func (m *Model) handleModelEdit(args string) string {
	name, updates, err := ai.ParseModelEditArgs(args)
	if err != nil {
		return fmt.Sprintf("Error: %v\nUsage: /model edit <name> [--base-url <url>] [--provider <openai|anthropic>]", err)
	}

	cfg := m.aiService.ModelRegistry().Get(name)
	if cfg == nil {
		return ai.ModelNotConfiguredHint(name, m.aiService.ModelRegistry())
	}

	if err := ai.ApplyModelUpdates(cfg, updates); err != nil {
		return fmt.Sprintf("Modification failed: %v", err)
	}
	// B1：编辑声明后当场生效（同上，与 plain 侧共用 syncThinkingDeclaration）。
	syncThinkingDeclaration(cfg)

	// If modifying the current model, sync update runtime state
	if name == m.sessionService.ModelName() {
		m.aiService.Client().SetModelConfig(cfg)
		m.sessionService.Config().ContextWindow = cfg.ContextWindow
		// H8：Budget 从 Config 构造（原 DefaultBudget 硬编码阈值）
		m.aiService.SetBudget(ctxpkg.NewBudgetFromConfig(config.ResolveContextWindow(cfg.ContextWindow, cfg.Name), m.sessionService.Config()))
	}

	// Persist
	if m.sessionService.Config().ModelsJSONPath != "" {
		if saveErr := ai.SaveModels(m.sessionService.Config().ModelsJSONPath, m.aiService.ModelRegistry()); saveErr != nil {
			return fmt.Sprintf("Model modified: %s（but save failed: %v）", name, saveErr)
		}
	}

	return fmt.Sprintf("Model modified: %s\n%s", name, ai.FormatModelInfo(cfg))
}
