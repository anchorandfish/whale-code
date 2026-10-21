package ai

import (
	"encoding/json"
	"fmt"
	"os"
	"sort"
	"strings"
	"sync"

	"github.com/anchorandfish/whale-code/internal/util"
)

// ModelSpec 已知模型的**客观规格**兜底（上下文窗口 / 最大输出）。
//
// ★ 2026-10-05（D 批 · 删除成本线）：本结构此前名为 ModelPricing，除了规格还带
// 4 个「每百万 token 单价」字段（input/output/cache create/cache read）。由它算出的
// 金额是我们**自己维护价格表估出来的**，既不是 provider 上报、也不是真实账单，
// 且混用 USD 与 RMB 两种币种被下游一律当美元 ⇒ 数字系统性失真。
// 结论：**不修、直接删**。规格（ContextWindow / MaxOutputTokens）是模型客观事实，
// 仍被 FillDefaults 使用，保留。
type ModelSpec struct {
	Name            string `json:"name"`
	ContextWindow   int    `json:"context_window"`
	MaxOutputTokens int    `json:"max_output_tokens"`
}

// ModelConfig is the complete configuration for a single model (including connection parameters)
type ModelConfig struct {
	Name            string `json:"name"`
	Provider        string `json:"provider"`
	BaseURL         string `json:"base_url"`
	APIKey          string `json:"api_key,omitempty"`     // 持久化到磁盘（支持保存/加载往返）
	APIKeyRef       string `json:"api_key_ref,omitempty"` // Environment variable reference for API key
	ContextWindow   int    `json:"context_window"`
	MaxOutputTokens int    `json:"max_output_tokens"`
	// ThinkingSupport 用户对该模型 thinking 能力的**声明**（B1，2026-10-05）。
	// 取值 = ThinkingCapability 的字面量："supported" / "unsupported" / ""（未声明）。
	//
	// ★ 语义 = **声明优先于推断**：非空时覆盖 internal/ai 的静态表与前缀推断
	// （唯一判据见 capability_declaration.go 的 ThinkingSupportFor）。之所以要能覆盖，
	// 是因为**用户面对的是他真实接的那个门，而前缀推断面对的只是模型名**。
	// 空串 = 不表态 ⇒ 回到推断，行为与引入本字段之前完全一致。
	ThinkingSupport string `json:"thinking_support,omitempty"`
	// ThinkingEvidence 是上面那条声明的**来源**（B3，2026-10-05）：厂商文档 URL、
	// 抓包产物路径、或用户自己的备注。
	//
	// ★ 它**不被解析、不被验证**，也没有"非空才允许声明"的门 —— 一个无法核实的
	// URL 字段做成必填门就是"信则灵"：随手填任何网址都能过，反而制造"我已核实"
	// 的假象（与本仓「判据必须可判别」相悖）。所以这里只做**记录 + 展示**
	// （`/model info`），把"我凭什么这么声明"留在配置里可追溯；至于声明**成不成立**，
	// 由 `/model info` 的 `effective` 档位如实呈现。
	// 空串 = 没记来源（**不等于**"没有证据"）。
	ThinkingEvidence string `json:"thinking_evidence,omitempty"`
}

// GetAPIKey returns the API key, resolving from environment variable reference if needed
func (c *ModelConfig) GetAPIKey() string {
	if c.APIKey != "" {
		return c.APIKey
	}
	if c.APIKeyRef != "" {
		return os.Getenv(c.APIKeyRef)
	}
	return ""
}

// modelRegistryFile is the file format of models.json
type modelRegistryFile struct {
	Models map[string]*ModelConfig `json:"models"`
}

// ModelRegistry is the model registry, managing all configured models
type ModelRegistry struct {
	mu     sync.RWMutex
	models map[string]*ModelConfig
}

// NewModelRegistry creates an empty model registry
func NewModelRegistry() *ModelRegistry {
	return &ModelRegistry{models: make(map[string]*ModelConfig)}
}

// Add adds or updates a model configuration
func (r *ModelRegistry) Add(cfg *ModelConfig) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.models[cfg.Name] = cfg
}

// Get gets model configuration, returns nil if not found
func (r *ModelRegistry) Get(name string) *ModelConfig {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.models[name]
}

// Remove removes model configuration, returns whether successful
func (r *ModelRegistry) Remove(name string) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, ok := r.models[name]; !ok {
		return false
	}
	delete(r.models, name)
	return true
}

// List returns all registered models (sorted by name)
func (r *ModelRegistry) List() []*ModelConfig {
	r.mu.RLock()
	defer r.mu.RUnlock()
	result := make([]*ModelConfig, 0, len(r.models))
	for _, cfg := range r.models {
		result = append(result, cfg)
	}
	sort.Slice(result, func(i, j int) bool {
		return result[i].Name < result[j].Name
	})
	return result
}

// Names returns all registered model names
func (r *ModelRegistry) Names() []string {
	r.mu.RLock()
	defer r.mu.RUnlock()
	names := make([]string, 0, len(r.models))
	for name := range r.models {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

// HasModel checks whether a model name is registered
func (r *ModelRegistry) HasModel(name string) bool {
	r.mu.RLock()
	defer r.mu.RUnlock()
	_, ok := r.models[name]
	return ok
}

// ModelNotConfiguredHint 返回模型未配置时的友好提示，列出当前已注册的可用模型。
// 当用户尝试切换/查看一个未注册的模型时调用，帮助用户快速找到可用模型名。
func ModelNotConfiguredHint(name string, registry *ModelRegistry) string {
	names := registry.Names()
	if len(names) == 0 {
		return fmt.Sprintf("Model '%s' not configured. Use /model add to add a model.", name)
	}
	return fmt.Sprintf("Model '%s' not configured.\n\nAvailable models:\n  - %s\n\nUse '/model <name>' to switch, or '/model add %s ...' to add it.",
		name, strings.Join(names, "\n  - "), name)
}

// LoadModels loads model registry from models.json
func LoadModels(path string) (*ModelRegistry, error) {
	registry := NewModelRegistry()
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return registry, nil
		}
		return registry, fmt.Errorf("failed to read models.json: %w", err)
	}
	var f modelRegistryFile
	if err := json.Unmarshal(data, &f); err != nil {
		return registry, fmt.Errorf("failed to parse models.json: %w", err)
	}
	if f.Models != nil {
		registry.models = f.Models
	}
	return registry, nil
}

// SaveModels saves model registry to models.json using atomic write
func SaveModels(path string, registry *ModelRegistry) error {
	registry.mu.RLock()
	copy := make(map[string]*ModelConfig, len(registry.models))
	for k, v := range registry.models {
		vCopy := *v
		copy[k] = &vCopy
	}
	registry.mu.RUnlock()

	f := modelRegistryFile{Models: copy}
	data, err := json.MarshalIndent(f, "", "  ")
	if err != nil {
		return fmt.Errorf("failed to serialize: %w", err)
	}
	if err := util.AtomicWriteFile(path, data, 0600); err != nil {
		return fmt.Errorf("failed to write models.json: %w", err)
	}
	return nil
}

// FillDefaults fills missing fields in ModelConfig from DefaultModelSpecs
func (c *ModelConfig) FillDefaults() {
	p := SpecForModel(c.Name)
	if c.Provider == "" {
		c.Provider = ProviderOpenAI
	}
	if c.BaseURL == "" {
		// 与 NewSimpleClient 同源（A2 抽出 defaultBaseURLForProvider）——
		// 端点自 A2 起参与能力判定，两处各写一份会出现"显示按 A 判、运行按 B 判"。
		c.BaseURL = defaultBaseURLForProvider(c.Provider)
	}
	if c.ContextWindow == 0 {
		if p != nil && p.ContextWindow > 0 {
			c.ContextWindow = p.ContextWindow
		} else {
			c.ContextWindow = 200000
		}
	}
	if c.MaxOutputTokens == 0 {
		if p != nil && p.MaxOutputTokens > 0 {
			c.MaxOutputTokens = p.MaxOutputTokens
		} else {
			c.MaxOutputTokens = 16384
		}
	}
}

// MaskAPIKey masks the API Key for desensitization
func MaskAPIKey(key string) string {
	if len(key) <= 8 {
		return "***"
	}
	return key[:4] + "***" + key[len(key)-4:]
}

// FormatModelInfo formats detailed info for a single model
func FormatModelInfo(cfg *ModelConfig) string {
	var sb strings.Builder
	sb.WriteString(fmt.Sprintf("Model: %s\n", cfg.Name))
	sb.WriteString(fmt.Sprintf("Provider: %s\n", cfg.Provider))
	sb.WriteString(fmt.Sprintf("Base URL: %s\n", cfg.BaseURL))
	sb.WriteString(fmt.Sprintf("API Key: %s\n", MaskAPIKey(cfg.GetAPIKey())))
	sb.WriteString(fmt.Sprintf("Context window: %d tokens\n", cfg.ContextWindow))
	sb.WriteString(fmt.Sprintf("Max output: %d tokens\n", cfg.MaxOutputTokens))
	// B1/B2：把「用户声明」与「系统最终判定」**并列**显示。
	// 只显示一个是误导 —— 声明为空不代表"不支持"，声明 supported 也不代表
	// 能力表也这么写（声明优先，但优先级高低得让人看得见）。
	decl := cfg.ThinkingSupport
	if decl == "" {
		decl = "(undeclared)"
	}
	// A2：判定要带**端点**。cfg 未填 base_url 时运行时用的是
	// provider 缺省端点 ⇒ 这里也解析出同一个值（同源，不猜第二遍）。
	effBaseURL := cfg.BaseURL
	if effBaseURL == "" {
		effBaseURL = defaultBaseURLForProvider(cfg.Provider)
	}
	sb.WriteString(fmt.Sprintf("Thinking: declared=%s effective=%s (endpoint=%s)\n",
		decl, ThinkingSupportFor(cfg.Name, effBaseURL), endpointTrustLabel(effBaseURL)))
	// B3：声明若记了来源就跟着显示一行（缺席 = 没记，不是"没证据"）。
	if cfg.ThinkingEvidence != "" {
		sb.WriteString(fmt.Sprintf("Thinking evidence: %s\n", cfg.ThinkingEvidence))
	}
	return sb.String()
}

// ParseModelAddArgs parses /model add command arguments
// Format: <name> [--base-url <url>] [--api-key <key>] [--provider <openai|anthropic>]
//
//	[--context-window <tokens>] [--max-output <tokens>]
//	[--supports-thinking <supported|unsupported>] [--thinking-evidence <url>]
func ParseModelAddArgs(args string) (*ModelConfig, error) {
	fields := splitFields(args)
	if len(fields) == 0 {
		return nil, fmt.Errorf("missing model name")
	}

	cfg := &ModelConfig{Name: fields[0]}
	i := 1
	for i < len(fields) {
		switch fields[i] {
		case "--base-url":
			i++
			if i >= len(fields) {
				return nil, fmt.Errorf("--base-url missing value")
			}
			cfg.BaseURL = fields[i]
		case "--api-key":
			i++
			if i >= len(fields) {
				return nil, fmt.Errorf("--api-key missing value")
			}
			cfg.APIKey = fields[i]
		case "--provider":
			i++
			if i >= len(fields) {
				return nil, fmt.Errorf("--provider missing value")
			}
			cfg.Provider = fields[i]
		case "--context-window":
			i++
			if i >= len(fields) {
				return nil, fmt.Errorf("--context-window missing value")
			}
			var n int
			if _, err := fmt.Sscanf(fields[i], "%d", &n); err != nil || n <= 0 {
				return nil, fmt.Errorf("--context-window invalid value: %s", fields[i])
			}
			cfg.ContextWindow = n
		case "--max-output":
			i++
			if i >= len(fields) {
				return nil, fmt.Errorf("--max-output missing value")
			}
			var n int
			if _, err := fmt.Sscanf(fields[i], "%d", &n); err != nil || n <= 0 {
				return nil, fmt.Errorf("--max-output invalid value: %s", fields[i])
			}
			cfg.MaxOutputTokens = n
		case "--supports-thinking":
			i++
			if i >= len(fields) {
				return nil, fmt.Errorf("--supports-thinking missing value")
			}
			t, ok := ParseThinkingCapability(fields[i])
			if !ok {
				// ★ 拼错必须报错，不能静默当成"未声明"—— 否则用户以为声明过了，
				// 而实际什么都没发生（假生效，比报错危险得多）。
				return nil, fmt.Errorf("--supports-thinking invalid value: %q (want supported|unsupported)", fields[i])
			}
			cfg.ThinkingSupport = t.String()
		case "--thinking-evidence":
			i++
			if i >= len(fields) {
				return nil, fmt.Errorf("--thinking-evidence missing value")
			}
			cfg.ThinkingEvidence = fields[i]
		default:
			return nil, fmt.Errorf("unknown argument: %s", fields[i])
		}
		i++
	}
	return cfg, nil
}

// ParseModelEditArgs parses /model edit command arguments
// Format: <name> [--base-url <url>] [--api-key <key>] [--provider <openai|anthropic>]
//
//	[--context-window <tokens>] [--max-output <tokens>]
//	[--supports-thinking <supported|unsupported>] [--thinking-evidence <url>]
//
// Only modifies specified fields, unspecified fields keep original values
func ParseModelEditArgs(args string) (string, map[string]string, error) {
	fields := splitFields(args)
	if len(fields) == 0 {
		return "", nil, fmt.Errorf("missing model name")
	}

	name := fields[0]
	updates := make(map[string]string)
	i := 1
	for i < len(fields) {
		switch fields[i] {
		case "--base-url":
			i++
			if i >= len(fields) {
				return "", nil, fmt.Errorf("--base-url missing value")
			}
			updates["base_url"] = fields[i]
		case "--api-key":
			i++
			if i >= len(fields) {
				return "", nil, fmt.Errorf("--api-key missing value")
			}
			updates["api_key"] = fields[i]
		case "--provider":
			i++
			if i >= len(fields) {
				return "", nil, fmt.Errorf("--provider missing value")
			}
			updates["provider"] = fields[i]
		case "--context-window":
			i++
			if i >= len(fields) {
				return "", nil, fmt.Errorf("--context-window missing value")
			}
			updates["context_window"] = fields[i]
		case "--max-output":
			i++
			if i >= len(fields) {
				return "", nil, fmt.Errorf("--max-output missing value")
			}
			updates["max_output_tokens"] = fields[i]
		case "--supports-thinking":
			i++
			if i >= len(fields) {
				return "", nil, fmt.Errorf("--supports-thinking missing value")
			}
			t, ok := ParseThinkingCapability(fields[i])
			if !ok {
				return "", nil, fmt.Errorf("--supports-thinking invalid value: %q (want supported|unsupported)", fields[i])
			}
			updates["thinking_support"] = t.String()
		case "--thinking-evidence":
			i++
			if i >= len(fields) {
				return "", nil, fmt.Errorf("--thinking-evidence missing value")
			}
			updates["thinking_evidence"] = fields[i]
		default:
			return "", nil, fmt.Errorf("unknown argument: %s", fields[i])
		}
		i++
	}
	if len(updates) == 0 {
		return "", nil, fmt.Errorf("please specify at least one field to modify")
	}
	return name, updates, nil
}

// ApplyModelUpdates applies edit parameters to existing ModelConfig, only modifying specified fields
func ApplyModelUpdates(cfg *ModelConfig, updates map[string]string) error {
	for key, value := range updates {
		switch key {
		case "base_url":
			cfg.BaseURL = value
		case "api_key":
			cfg.APIKey = value
		case "provider":
			cfg.Provider = value
		case "context_window":
			var n int
			if _, err := fmt.Sscanf(value, "%d", &n); err != nil || n <= 0 {
				return fmt.Errorf("context_window invalid value: %s", value)
			}
			cfg.ContextWindow = n
		case "max_output_tokens":
			var n int
			if _, err := fmt.Sscanf(value, "%d", &n); err != nil || n <= 0 {
				return fmt.Errorf("max_output_tokens invalid value: %s", value)
			}
			cfg.MaxOutputTokens = n
		case "thinking_support":
			// B1：声明 thinking 能力（"supported"/"unsupported"）。
			// 由 ParseModelEditArgs 归一过（只可能是这两个字面量之一），这里仍复验一次 ——
			// ApplyModelUpdates 是公开函数，绕过解析器直接调用的路径也必须安全。
			if _, ok := ParseThinkingCapability(value); !ok {
				return fmt.Errorf("thinking_support invalid value: %s", value)
			}
			cfg.ThinkingSupport = value
		case "thinking_evidence":
			// B3：声明的来源。不解析、不验证，空串 = 清除已记来源。
			cfg.ThinkingEvidence = value
		}
	}
	return nil
}

// splitFields splits string by spaces, supports spaces within quotes and escaped quotes
func splitFields(s string) []string {
	var fields []string
	var current strings.Builder
	inQuote := false
	quoteChar := byte(0)

	for i := 0; i < len(s); i++ {
		c := s[i]
		if inQuote {
			if c == '\\' && i+1 < len(s) && (s[i+1] == quoteChar || s[i+1] == '\\') {
				current.WriteByte(s[i+1])
				i++
			} else if c == quoteChar {
				inQuote = false
			} else {
				current.WriteByte(c)
			}
		} else {
			if c == '"' || c == '\'' {
				inQuote = true
				quoteChar = c
			} else if c == ' ' || c == '\t' {
				if current.Len() > 0 {
					fields = append(fields, current.String())
					current.Reset()
				}
			} else {
				current.WriteByte(c)
			}
		}
	}
	if current.Len() > 0 {
		fields = append(fields, current.String())
	}
	return fields
}

// DefaultModelSpecs 已知模型的规格兜底表（上下文窗口 / 最大输出）。
//
// 用途：用户 `model add` 没填 --context-window / --max-output 时由 FillDefaults 补，
// 以及 ContextWindowForModel / MaxOutputTokensForModel 的查询源。
//
// ★ 2026-10-05（D 批）：删除本表原有 4 个单价字段（见 ModelSpec 注释）。
var DefaultModelSpecs = []ModelSpec{
	{Name: "claude-sonnet-4-20250514", ContextWindow: 200000, MaxOutputTokens: 16384},
	{Name: "claude-opus-4-20250514", ContextWindow: 200000, MaxOutputTokens: 16384},
	{Name: "claude-3-5-haiku", ContextWindow: 200000, MaxOutputTokens: 8192},
	{Name: "gpt-4o", ContextWindow: 128000, MaxOutputTokens: 16384},
	{Name: "gpt-4o-mini", ContextWindow: 128000, MaxOutputTokens: 16384},
	{Name: "qwen-plus", ContextWindow: 131072, MaxOutputTokens: 8192},
	{Name: "qwen-turbo", ContextWindow: 131072, MaxOutputTokens: 8192},
	{Name: "qwen3", ContextWindow: 131072, MaxOutputTokens: 65536},
	{Name: "qwen3.5-flash", ContextWindow: 1048576, MaxOutputTokens: 65536},
	{Name: "qwen3.5-35b-a3b", ContextWindow: 262144, MaxOutputTokens: 65536},
	{Name: "qwen3.6-27b", ContextWindow: 131072, MaxOutputTokens: 65536},
	{Name: "qwen3.6-35b-a3b", ContextWindow: 262144, MaxOutputTokens: 65536},
	{Name: "deepseek-chat", ContextWindow: 65536, MaxOutputTokens: 8192},
	{Name: "deepseek-reasoner", ContextWindow: 65536, MaxOutputTokens: 8192},
	{Name: "minimax-m1", ContextWindow: 1000000, MaxOutputTokens: 16384},
	{Name: "MiniMax-M2.5", ContextWindow: 204800, MaxOutputTokens: 8192},
	{Name: "gemini-2.0-flash", ContextWindow: 1048576, MaxOutputTokens: 8192},
	{Name: "gemini-1.5-pro", ContextWindow: 2097152, MaxOutputTokens: 8192},
	{Name: "qwen-max", ContextWindow: 32768, MaxOutputTokens: 8192},
	{Name: "glm-4-plus", ContextWindow: 131072, MaxOutputTokens: 4096},
}

// matchSpec 在规格表中按 exact → 双向包含 → 前缀 三级模糊匹配查找模型规格。
// 命中返回该条目的拷贝指针，未命中返回 nil。
//
// ★ D 批只删"钱"，**不改匹配策略**：改了会连带改变上下文窗口的兜底取值
// （例如 qwen3.7-plus 现在靠"双向包含"落到 qwen3），那是另一件事。
func matchSpec(modelName string, table []ModelSpec) *ModelSpec {
	// 1. 精确匹配（大小写不敏感）
	for i := range table {
		if strings.EqualFold(modelName, table[i].Name) {
			p := table[i]
			return &p
		}
	}
	// 2. 双向包含匹配
	for i := range table {
		if containsIgnoreCase(modelName, table[i].Name) ||
			containsIgnoreCase(table[i].Name, modelName) {
			p := table[i]
			return &p
		}
	}
	// 3. 前缀匹配（取名字前 8 字符）
	for i := range table {
		name := table[i].Name
		if len(name) > 8 && containsIgnoreCase(modelName, name[:8]) {
			p := table[i]
			return &p
		}
	}
	return nil
}

// SpecForModel 按模糊匹配查找已知模型规格；未知模型返回 nil。
func SpecForModel(modelName string) *ModelSpec {
	return matchSpec(modelName, DefaultModelSpecs)
}

// ContextWindowForModel get context window size for model
func ContextWindowForModel(modelName string) int {
	p := SpecForModel(modelName)
	if p != nil && p.ContextWindow > 0 {
		return p.ContextWindow
	}
	return 200000
}

// MaxOutputTokensForModel get max output tokens for model
func MaxOutputTokensForModel(modelName string) int {
	p := SpecForModel(modelName)
	if p != nil && p.MaxOutputTokens > 0 {
		return p.MaxOutputTokens
	}
	return 16384
}

// FormatModelList 格式化可用模型为可读列表（含 provider、base URL、上下文窗口、最大输出）。
// 当 registry 中无已注册模型时，回退显示全部 DefaultModelSpecs 条目。
func FormatModelList(currentModel string, availableModels []string, registry *ModelRegistry) string {
	var sb strings.Builder

	// Collect model configs from registry; fall back to DefaultModelSpecs for unknown models
	type displayEntry struct {
		Name            string
		Provider        string
		BaseURL         string
		ContextWindow   int
		MaxOutputTokens int
		IsCurrent       bool
	}

	var entries []displayEntry
	for _, name := range availableModels {
		cfg := registry.Get(name)
		isCurrent := strings.EqualFold(currentModel, name)

		if cfg != nil {
			entries = append(entries, displayEntry{
				Name:            cfg.Name,
				Provider:        cfg.Provider,
				BaseURL:         cfg.BaseURL,
				ContextWindow:   cfg.ContextWindow,
				MaxOutputTokens: cfg.MaxOutputTokens,
				IsCurrent:       isCurrent,
			})
		} else {
			// Fall back to DefaultModelSpecs
			p := SpecForModel(name)
			ctx := 0
			maxOut := 0
			if p != nil {
				ctx = p.ContextWindow
				maxOut = p.MaxOutputTokens
			}
			entries = append(entries, displayEntry{
				Name:            name,
				Provider:        ProviderOpenAI,
				BaseURL:         "",
				ContextWindow:   ctx,
				MaxOutputTokens: maxOut,
				IsCurrent:       isCurrent,
			})
		}
	}

	// If no registered models, show all DefaultModelSpecs entries
	if len(entries) == 0 {
		for _, p := range DefaultModelSpecs {
			entries = append(entries, displayEntry{
				Name:            p.Name,
				Provider:        ProviderOpenAI,
				BaseURL:         "",
				ContextWindow:   p.ContextWindow,
				MaxOutputTokens: p.MaxOutputTokens,
				IsCurrent:       strings.EqualFold(currentModel, p.Name),
			})
		}
	}

	sb.WriteString(fmt.Sprintf("📋 可用模型列表 (共 %d 个)\n\n", len(entries)))

	// Calculate column widths for alignment
	maxName := 20
	maxProvider := 8
	maxURL := 40
	for _, e := range entries {
		if len(e.Name) > maxName {
			maxName = len(e.Name)
		}
		if len(e.Provider) > maxProvider {
			maxProvider = len(e.Provider)
		}
		urlDisplay := e.BaseURL
		if urlDisplay == "" {
			urlDisplay = "(default)"
		}
		if len(urlDisplay) > maxURL {
			maxURL = len(urlDisplay)
		}
	}

	// Header row
	header := fmt.Sprintf("%-*s  %-*s  %-*s  %-*s  %s",
		maxName, "Model Name",
		maxProvider, "Provider",
		maxURL, "Base URL",
		12, "Context Window",
		"Max Output")
	sb.WriteString(header + "\n")
	sb.WriteString(strings.Repeat("─", len(header)+4))
	sb.WriteString("\n")

	// Data rows
	for _, e := range entries {
		urlDisplay := e.BaseURL
		if urlDisplay == "" {
			urlDisplay = "(default)"
		}
		ctxStr := formatContextWindow(e.ContextWindow)
		maxOutStr := fmt.Sprintf("%d", e.MaxOutputTokens)
		if e.MaxOutputTokens == 0 {
			maxOutStr = "-"
		}

		marker := ""
		if e.IsCurrent {
			marker = " ◄ 当前"
		}

		sb.WriteString(fmt.Sprintf("%-*s  %-*s  %-*s  %-*s  %s%s\n",
			maxName, e.Name,
			maxProvider, e.Provider,
			maxURL, urlDisplay,
			12, ctxStr,
			maxOutStr, marker))
	}

	sb.WriteString("\nUse /model <name> to switch model")
	return sb.String()
}

// formatContextWindow convert context window number to human-readable string
func formatContextWindow(n int) string {
	if n >= 1_000_000 {
		return fmt.Sprintf("%dM", n/1_000_000)
	}
	if n >= 1024 {
		return fmt.Sprintf("%dK", n/1024)
	}
	return fmt.Sprintf("%d", n)
}

func containsIgnoreCase(s, substr string) bool {
	sl := len(s)
	subl := len(substr)
	if subl > sl {
		return false
	}
	for i := 0; i <= sl-subl; i++ {
		match := true
		for j := 0; j < subl; j++ {
			sc := s[i+j]
			tc := substr[j]
			if sc >= 'A' && sc <= 'Z' {
				sc += 32
			}
			if tc >= 'A' && tc <= 'Z' {
				tc += 32
			}
			if sc != tc {
				match = false
				break
			}
		}
		if match {
			return true
		}
	}
	return false
}
