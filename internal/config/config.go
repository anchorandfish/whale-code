// Package config — multi-source configuration loading and merging
package config

import (
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/anchorandfish/whale-code/internal/keybindings"
	"github.com/anchorandfish/whale-code/internal/permission"
	"github.com/anchorandfish/whale-code/internal/util"
	"github.com/anchorandfish/whale-code/internal/whalehome"
)

// PermissionMode permission mode
type PermissionMode string

const (
	PermReadOnly         PermissionMode = "read-only"
	PermWorkspaceWrite   PermissionMode = "workspace-write"
	PermDangerFullAccess PermissionMode = "danger-full-access"
)

// ErrCorruptConfig 表示配置文件**存在但无法解析**（损坏）。Load 遇到时返回该错误
// （Q1/A：fail-closed，绝不静默回退）。
// 注意：文件「缺失」**不是**错误（全新安装合法）；只有「存在但损坏」才触发。
var ErrCorruptConfig = errors.New("config: source file exists but is not valid JSON")

// ErrUnreadableConfig 表示配置文件**存在但读不出来**（权限 EACCES / IO 失败）。
// （A9：与 ErrCorruptConfig **同档** —— fail-closed T3「拒绝启动」。）
//
// 为什么与「损坏」分开：两类失效的**修复动作不同**（损坏 → 备份恢复 / doctor --repair；
// 不可读 → 修权限位），且「不可读」可能在读到任何字节之前就发生，无法归因于内容。
// 但两者的**语义后果一致**：都等于"拿不到用户的配置"⇒ 都不得静默回退到宽松默认值。
//
// 与「缺失」的区别（重要）：`os.IsNotExist` 仍**不是**错误 —— 全新安装没有配置文件是合法状态
// （对齐业内既有实现）。只有 `ReadFile` 返回「非 NotExist」的错误时才触发本 sentinel。
// 本包既有实现 migrateWithBackup 早已是这个口径（NotExist→跳过 / 其它→报错），
// A9 只是把 readSources 对齐到同一口径，消除同一包内两套判定。
var ErrUnreadableConfig = errors.New("config: source file exists but cannot be read")

// CompactionStrategy compaction strategy
type CompactionStrategy string

const (
	CompactBalanced     CompactionStrategy = "balanced"
	CompactPreserveUser CompactionStrategy = "preserve_user"
	CompactAggressive   CompactionStrategy = "aggressive"
)

// OutputStyle output style type
type OutputStyle string

const (
	OutputDefault   OutputStyle = "default"
	OutputConcise   OutputStyle = "concise"
	OutputVerbose   OutputStyle = "verbose"
	OutputTechnical OutputStyle = "technical"
)

// ApprovalConfig 审批策略配置（B0 §4.3，B3 接入）。
type ApprovalConfig struct {
	Policy string `json:"policy"` // 审批策略：untrusted/on-failure/on-request/never
}

// EvolutionConfig H12 Agent 自我进化系统配置（Feature Flag，）。
// 默认全部 false（零成本降级，不影响上线）。
// enabled=true 时 EvolutionLoop 才被实例化；
// auto_run=true 时 EvolutionLoop.Run 才被自动调用（false=仅手动触发）。
type EvolutionConfig struct {
	Enabled              bool `json:"enabled"`                 // 总开关，默认 false
	AutoRun              bool `json:"auto_run"`                // 自动触发开关，默认 false（即使 enabled=true 也需手动触发）
	TTLScanIntervalHours int  `json:"ttl_scan_interval_hours"` // 后台 TTL 扫描间隔（小时），默认 24
}

// TokenConfig T1-Btoken 估算配置。
//
// 控制 TokenSource 三层架构行为：
// - TiktokenEnabled: 是否启用 L3 tiktoken-go 路径（默认 false，critical model 才走 L3）
// - OffsetTTL: EMA offset 校准缓存时长（默认 30 分钟，超过则重置）
//
// 向后兼容：旧 settings.json 无此字段时使用零值（TiktokenEnabled=false, OffsetTTL=0），
// ResolveTokenConfig 兜底 OffsetTTL=30min。
type TokenConfig struct {
	TiktokenEnabled bool `json:"tiktoken_enabled"`   // L3 tiktoken-go 开关，默认 false
	OffsetTTL       int  `json:"offset_ttl_minutes"` // EMA offset 缓存时长（分钟），默认 30
}

// ResourceConfig 资源降级配置（V3-4-2，v1.0 仅配置层，v1.1 接线模块）。
// 默认全零值（不降级）。resource.low_memory=true 时由 ApplyLowMemoryDegradation 填充。
// v1.0 阶段：仅定义配置字段 + doctor --resource 检测 + 启动警告，不接线 LSP/上下文/memory 模块。
// v1.1 阶段：各模块读取 Resource 字段决定是否生效（模块未实现降级逻辑时字段被忽略，向后兼容）。
type ResourceConfig struct {
	LowMemory   bool `json:"low_memory"`   // 手动启用低内存模式
	DisableLSP  bool `json:"disable_lsp"`  // 禁用 LSP 索引（减少内存）
	ReduceCache bool `json:"reduce_cache"` // 减少上下文缓存
}

// BridgeConfig N10 IDE bridge 协议深度对标配置（P0/P1 安全修复）。
// 所有字段默认零值（空/0/false），仅在 bridge_enabled=true 时生效。
// 支持 WHALE_BRIDGE_JWT_SECRET 等 env 覆盖。
type BridgeConfig struct {
	JwtSecret            string `json:"jwt_secret"`             // HMAC-SHA256 密钥（来源委托 H3 env 白名单）
	ExpectedIssuer       string `json:"expected_issuer"`        // 期望的 iss claim（空则不校验）
	ExpectedAudience     string `json:"expected_audience"`      // 期望的 aud claim（空则不校验）
	ClockSkewSeconds     int64  `json:"clock_skew_seconds"`     // 时钟偏移窗口（秒，默认 30）
	CommandGuardEnabled  bool   `json:"command_guard_enabled"`  // IDE 命令校验是否启用（H2 hook 激活开关）
	ReconnectMaxAttempts int    `json:"reconnect_max_attempts"` // 重连最大重试次数（默认 5）
}

// PluginSignatureConfig 插件签名验证配置（R-3：从环境变量迁移到 config 系统）。
//
// 字段说明：
// - Enabled：是否启用签名验证（默认 false，向后兼容：旧配置无此字段时使用环境变量降级）
// - Required：是否强制要求签名（true 时未签名插件被拒绝；false 时警告但允许）
// - PublicKeyPath：签名公钥文件路径（ed25519 公钥，base64 编码文本文件）
//
// 优先级（buildPluginSignatureVerifier 实现）：
// 1. Enabled=true → 使用 config 配置（PublicKeyPath 为空时尝试 env 过渡兼容；
// Required=true 且公钥仍为空时 fail-closed 返回 error）
// 2. Enabled=false → 使用环境变量 WHALE_PLUGIN_SIGNING_PUBLIC_KEY /
// WHALE_PLUGIN_SIGNING_REQUIRED（向后兼容，桶 X5 接线行为）
// 3. 都未配置 → noopVerifier（允许无签名安装，向后兼容）
//
// 环境变量覆盖（applyEnvironmentOverrides）：
// - WHALE_PLUGIN_SIGNING_ENABLED（"1"/"true" 启用）
// - WHALE_PLUGIN_SIGNING_REQUIRED（"1"/"true" 启用）
// - WHALE_PLUGIN_SIGNING_PUBLIC_KEY_PATH（公钥文件路径）
//
// 安全约束：
// - fail-closed：Enabled=true + Required=true + PublicKeyPath 为空（且 env 无 fallback）→ 拒绝构建校验器
// - 公钥文件读取失败 → 拒绝构建校验器（不降级）
type PluginSignatureConfig struct {
	Enabled       bool   `json:"enabled"`         // 是否启用签名验证（默认 false，向后兼容）
	Required      bool   `json:"required"`        // 是否强制要求签名（true 时未签名插件被拒绝）
	PublicKeyPath string `json:"public_key_path"` // 签名公钥文件路径（ed25519 公钥 base64 文本）
}

// CurrentConfigVersion 是当前配置 schema 版本号。
// 引入破坏性配置结构变更时递增此值。
// R8 修复：与 internal/command/migrate_handler.go 的版本统一为 2（v2 新增 apiKeys/permission_mode 校验/compactThreshold→context_window 迁移）。
const CurrentConfigVersion = 2

// Config complete runtime configuration
type Config struct {
	ConfigVersion        int                        `json:"config_version"`
	Model                string                     `json:"model"`
	FallbackModel        string                     `json:"fallback_model"`
	PermissionMode       PermissionMode             `json:"permission_mode"`
	AllowedTools         []string                   `json:"allowed_tools"`
	ContextWindow        int                        `json:"context_window"`
	CompactionStrategy   CompactionStrategy         `json:"compaction_strategy"`
	AutoMemoryEnabled    bool                       `json:"auto_memory_enabled"`
	ThinkingEnabled      *bool                      `json:"thinking_enabled"`
	ThinkingBudgetTokens *int                       `json:"thinking_budget_tokens"`
	McpServers           map[string]McpServerConfig `json:"mcp_servers"`
	Hooks                HookConfig                 `json:"hooks"`
	SessionBudget        float64                    `json:"session_budget"`
	VimMode              bool                       `json:"vim_mode"`
	// Keybindings 按键绑定配置（N7 可配置化）。
	// 向后兼容：当 Keybindings.Mode 为空时，使用 VimMode 字段决定是否启用 Vim 模式。
	Keybindings keybindings.Config `json:"keybindings"`
	AutoCompact bool               `json:"auto_compact"`
	// CompactArchive 归档式压缩开关：压缩重写主文件前，先把被丢弃
	// 的消息追加到 <session>.archive.jsonl，防止破坏性压缩丢失原始上下文。
	// 默认 true（归档是压缩路径的安全网，不是可选项）；settings.json
	// compact_archive=false 显式关闭。关闭后压缩行为退回纯破坏性重写。
	CompactArchive bool        `json:"compact_archive"`
	OutputStyle    OutputStyle `json:"output_style"`
	// FastModeEnabled fast 模式开关（N14：切换到快速模型，对标同类 CLI fastMode.ts）。
	// 缺失时默认 false（向后兼容）。
	FastModeEnabled bool `json:"fast_mode_enabled"`
	// FastModel 快速模型名称（N14：可配置，对标同类 CLI FAST_MODE_MODEL_DISPLAY）。
	// 缺失时默认空（向后兼容），由调用方 fallback 到 DefaultModel。
	FastModel string `json:"fast_model"`
	// CompactModel 压缩摘要专用模型名（省 token 第 1 批，2026-10-14）。
	//
	// 为什么单开一个字段、不复用 FastModel：FastModel 的语义是"快速模式的**主模型**"
	// （用户切到 fast 模式后整个会话都换它），把压缩也挂上去会让两个需求互相打架
	// （想省压缩钱的人被迫切了整个会话的模型）。语义正确的做法是单独声明。
	//
	// 默认空 = 沿用主模型（行为与今天逐字相同）；配置后**只有压缩这一次摘要调用**换模型。
	// 降级：该模型调用失败时 AICompactor 自动回退主模型重算一次
	// （见 internal/context/ai_compact.go 的 generateAISummaryWithCostRecorder）。
	CompactModel        string            `json:"compact_model"`
	WhaleEnv            map[string]string `json:"whale_env"`
	CacheBreakThreshold float64           `json:"cache_break_threshold"`
	ModelsJSONPath      string            `json:"-"` // models.json file path (set at runtime, not serialized)
	MetricsEnabled      bool              `json:"metrics_enabled"`
	MetricsAddr         string            `json:"metrics_addr"`
	// MetricsAuthToken 是 /metrics 端点的 Bearer Token 鉴权令牌（M22-05，P0 可观测性安全增强）。
	// 默认空（向后兼容）：此时强制监听 127.0.0.1，仅允许同机访问。
	// 非空时：请求须携带 `Authorization: Bearer <MetricsAuthToken>` 才能访问 /metrics。
	// 可通过环境变量 WHALE_METRICS_AUTH_TOKEN 覆盖。
	// ⚠️ SNAPSHOT：auth_token 为空时的默认监听地址从原 `:9090`（全网开放）改为 `127.0.0.1:9090`（同机限定），
	// 属隐式行为变更。用户如需全网监听，必须显式配置 auth_token 并设置 metrics_addr。
	MetricsAuthToken string `json:"metrics_auth_token"`
	// PluginsEnabled 是否启用插件加载（P0-7/B7）。
	// 默认 false（插件默认禁用，高风险任意代码执行）；
	// 由 main.go --enable-plugins flag 设置为 true。
	PluginsEnabled bool `json:"plugins_enabled"`
	// BridgeEnabled 是否启用 bridge 模式（IDE 集成的 JSON-RPC over stdio）。
	// 默认 false：bridge 模块的 JWT 解码不验签（internal/bridge/jwt.go DecodeJWTPayload
	// 仅 base64 解码 payload，完全不验证签名），存在安全漏洞；
	// 个人单机用户不需要远程中继鉴权。需显式设置 bridge_enabled=true 或
	// 环境变量 WHALE_BRIDGE_ENABLED=1 才启用，使用方自行承担安全风险。
	BridgeEnabled bool `json:"bridge_enabled"`
	// Bridge N10 IDE bridge 协议深度对标配置项（P0/P1 安全修复）。
	Bridge BridgeConfig `json:"bridge"`
	// PluginSignature 插件签名验证配置（R-3：从环境变量迁移到 config 系统）。
	// 默认零值（Enabled=false），向后兼容旧 settings.json：
	// - Enabled=false 时使用环境变量 WHALE_PLUGIN_SIGNING_* 降级（桶 X5 行为）
	// - Enabled=true 时使用 config 配置，Required=true 且 PublicKeyPath 为空时 fail-closed
	PluginSignature PluginSignatureConfig `json:"plugin_signature"`
	// Approval 审批策略配置（B0 §4.3，B3 接入）。
	Approval ApprovalConfig `json:"approval"`
	// PermissionPriorityMode 权限规则来源优先级模式（，02 §4 仲裁契约变更）。
	// - "legacy"（默认）：user < project < local < session < cliArg（向后兼容）
	// - "posix"：local < project < user < session < cliArg（对齐 POSIX ACL user:: > group:: > other::）
	// 旧 settings.json 缺该字段时零值 ""，Validate 回填 "legacy"（SNAPSHOT--A，隐式行为变更：无）。
	// 环境变量 WHALE_PERMISSION_PRIORITY_MODE 可覆盖。
	PermissionPriorityMode string `json:"permission_priority_mode"`
	// Onboarded N18 Onboarding 完成标记（哨兵文件 ~/.whale/.onboarded 的双保险字段）。
	// 缺失时默认 false（向后兼容），触发首次运行 Onboarding 流程。
	Onboarded bool `json:"onboarded"`
	// Provider N18 Onboarding 选择的 AI Provider（anthropic/openai）。
	// 缺失时默认空（向后兼容），resolveProviderConfig 回退到环境变量检测。
	Provider string `json:"provider"`
	// APIKey N18 Onboarding 交互输入的 API Key（明文存储，与现有环境变量行为一致，未来 H3 可加密）。
	// 缺失时默认空（向后兼容），resolveProviderConfig 回退到环境变量检测。
	// S12-G5b 修复：加 omitempty —— 空值不被序列化，从而让 Save 的无损透传逻辑
	// （marshalConfigPreservingUnknown：磁盘有、结构体没输出 ⇒ 保留）把已有的 api_key 回填，
	// 避免一次无关保存（/env set、/output-style）把已存密钥清空。
	// 该逻辑原为 authSensitiveFields 白名单（wouldLoseAuthState/preserveAuthFields），
	// 现已一般化为「默认保留所有未建模键」，本字段只是它的一个受益者。
	APIKey string `json:"api_key,omitempty"`
	// CustomBaseURL N18 Onboarding 自定义 provider 的 base URL（仅 Provider=="openai" 兼容协议时有效）。
	// 缺失时默认空（向后兼容）。
	CustomBaseURL string `json:"custom_base_url"`

	// H8 硬编码脱钩新增字段（~111）
	// 所有字段默认值 = 原硬编码值，确保零行为变化（向后兼容）
	// 优先级：env > config file > DefaultConfig

	// StreamTimeoutSeconds 流式 AI 请求超时（秒），原 plain.go:227 硬编码 300
	StreamTimeoutSeconds int `json:"stream_timeout_seconds"`

	// CIMaxIterations CI 模式最大工具调用轮数，原 plain.go:35 硬编码 50
	CIMaxIterations int `json:"ci_max_iterations"`

	// CIMaxConsecutiveFailures CI 模式连续工具失败上限，原 plain.go:37 硬编码 3
	CIMaxConsecutiveFailures int `json:"ci_max_consecutive_failures"`

	// MaxTurnsPerRequest 交互模式单次用户请求内的模型调用轮次上限
	//（P0 轮次上限，agent 防护体系 2026-08-31）。复用 CIMaxIterations 配置模式：
	// env WHALE_MAX_TURNS_PER_REQUEST > config file > 默认 40。
	// 到限暂停交回控制权并输出进度快照（非报错退出）；进度检测判定收敛时
	// 自动续跑下一预算段（见 internal/agentguard）。
	MaxTurnsPerRequest int `json:"max_turns_per_request"`

	// CIPermission CI 模式默认权限，原 plain.go:1677 硬编码 "danger-full-access"
	// env WHALE_CI_PERMISSION 已可覆盖，此字段补 config file 层
	CIPermission string `json:"ci_permission"`

	// CITimeoutMinutes CI 模式超时（分钟），原 plain.go:1681 硬编码 10
	CITimeoutMinutes int `json:"ci_timeout_minutes"`

	// MaxSnap 每文件路径快照保留上限，原 snapshot.go:41 硬编码 50
	// 注：snapshot 包已被 FileHistory 替代，此字段保留用于未来 FileHistory 上限配置化
	MaxSnap int `json:"max_snap"`

	// MaxSearchMatchesPerSession 每会话搜索匹配详情上限，原 search.go:210 硬编码 5
	MaxSearchMatchesPerSession int `json:"max_search_matches_per_session"`

	// BudgetWarnPercent Budget 预警阈值，原 context.go:111 硬编码 0.75
	BudgetWarnPercent float64 `json:"budget_warn_percent"`

	// BudgetCompactPercent Budget 提示线（ShouldCompact 用户决策窗口）百分比，
	// 原 context.go:112 硬编码 0.85，压缩体系重构调整为 0.80。
	// 不驱动自动压缩触发（主力线由 AutoCompactBufferTokens 的 window-buffer 语义承担）。
	BudgetCompactPercent float64 `json:"budget_compact_percent"`

	// BudgetBlockPercent Budget 阻断阈值，原 context.go:113 硬编码 0.97
	BudgetBlockPercent float64 `json:"budget_block_percent"`

	// AutoCompactBufferTokens 自动压缩 token 缓冲，原 context.go:28 硬编码 13000
	AutoCompactBufferTokens int `json:"auto_compact_buffer_tokens"`

	// InputHistoryLimit 输入历史保留上限，原 app.go:1660 硬编码 500
	InputHistoryLimit int `json:"input_history_limit"`

	// AvailableModels /model 补全 fallback 模型列表，原 command_list.go:153 硬编码
	// 为空时使用内置默认列表（向后兼容旧 settings.json 无此字段的情况）
	AvailableModels []string `json:"available_models"`

	// Evolution H12 Agent 自我进化配置（Feature Flag，）。
	// 默认零值（Enabled=false, AutoRun=false），向后兼容旧 settings.json。
	// enabled=true 时 EvolutionLoop 才被实例化；
	// auto_run=true 时 EvolutionLoop.Run 才被自动调用（false=仅手动触发）。
	Evolution EvolutionConfig `json:"evolution"`
	// Token T1-Btoken 估算配置（三层架构 L1/L2/L3）。
	// 默认零值（TiktokenEnabled=false, OffsetTTL=0），向后兼容旧 settings.json。
	// OffsetTTL=0 时由 ResolveTokenConfig 兜底为 30 分钟。
	Token TokenConfig `json:"token"`

	// Resource V3-4-2 资源降级配置（v1.0 仅配置层，v1.1 接线模块）。
	// 默认零值（LowMemory=false, DisableLSP=false, ReduceCache=false），向后兼容旧 settings.json。
	// 用户可通过 `whale config set resource.low_memory true` 手动启用低资源模式。
	Resource ResourceConfig `json:"resource"`

	// SNAPSHOT-1（， 候选 B）：本轮新增字段
	// ModelCapabilities 用户自定义模型能力注册表（model prefix → capabilities）。
	// 为 nil 时使用内置注册表（向后兼容）。
	// 用户可通过此字段为自定义模型声明 effort/thinking/cache 支持能力，
	// 覆盖内置注册表（用户优先）。
	ModelCapabilities map[string]ModelCapabilities `json:"model_capabilities"`

	// LeanSystemPrompt 是否启用精简 system prompt 模式（H13-G5）。
	//
	// 默认 false（向后兼容，保持现有 14000 token/次的完整 system prompt）。
	// 启用时按层优先级裁剪非必要组件：先裁 SessionState（动态层），
	// 再裁 ProjectCtx（memory/search），CoreIdentity 和 ToolDefinitions 不裁。
	// 每次对话预计节省 3000-5000 token。
	//
	// 来源（优先级从高到低）：
	// - CLI flag `--lean-system-prompt`（main.go 解析后覆盖 cfg.LeanSystemPrompt=true）
	// - env WHALE_LEAN_SYSTEM_PROMPT=1/true（applyEnvironmentOverrides 读取）
	// - config file `lean_system_prompt: true`（mergeInto 解析）
	//
	// 约束（方案 4.6 节冲突 4）：会话内保持稳定，不动态切换（避免破坏 cache）。
	// config 热重载时此字段被 freeze（M22-04 RegisterReloadHook 仅应用 custom_pricing，不重新读取此字段）。
	LeanSystemPrompt bool `json:"lean_system_prompt"`
}

// McpServerConfig MCP server configuration
type McpServerConfig struct {
	Command string            `json:"command"`
	Args    []string          `json:"args"`
	Env     map[string]string `json:"env"`
	URL     string            `json:"url"`
}

// HookConfig hook configuration
type HookConfig struct {
	PreToolUse  []HookEntry `json:"pre_tool_use"`
	PostToolUse []HookEntry `json:"post_tool_use"`
	Stop        []HookEntry `json:"stop"`
}

// HookEntry hook entry
type HookEntry struct {
	Command string `json:"command"`
	Shell   string `json:"shell"`
}

// D 批：删除 `CustomPricing` 类型与 `Config.CustomPricing` 字段
// （settings.json 的 `custom_pricing` 配置项）。
//
// 它唯一的作用是让用户覆盖那张"每百万 token 单价"表 —— 而那张表算出来的金额
// 是我们自己估的（provider 不上报、也不等于账单，且混币种），已随成本线整体删除。
// 表没了，覆盖表的旋钮自然也不该留。
//
// ⚠️ 旧 settings.json 里若仍写着 custom_pricing，现在会被当成未知键忽略（不报错）。

// DefaultConfig default configuration
func DefaultConfig() Config {
	return Config{
		ConfigVersion:  CurrentConfigVersion,
		Model:          "",
		PermissionMode: PermWorkspaceWrite,
		// C4：默认窗口的**唯一定义**在 context_window.go（DefaultContextWindow）。
		// 它只是「用户没配」时的保守兜底；「0 = 由模型决定」的解析见 EffectiveContextWindow()。
		ContextWindow:      DefaultContextWindow,
		CompactionStrategy: CompactBalanced,
		AutoMemoryEnabled:  true,
		// AutoCompact 默认 true：对齐业内既有实现(90%)/同类 CLI(~93%) 在有余量时自动压缩，
		// 配合 ShouldCompact 档触发即自动 AI 压缩（方案 A），避免等到 97% Blocked 时
		// 无余量做 AI 摘要只能退化规则级。
		AutoCompact: true,
		// CompactArchive 默认 true：压缩前归档是安全网（对齐 auto_compact 覆盖语义，
		// 仅显式 false 关闭）。
		CompactArchive:      true,
		McpServers:          make(map[string]McpServerConfig),
		OutputStyle:         OutputDefault,
		WhaleEnv:            make(map[string]string),
		CacheBreakThreshold: 0.05,
		MetricsAddr:         ":9090",
		// B0 §5：默认 PolicyOnRequest（最安全）。
		Approval: ApprovalConfig{Policy: "on-request"},
		// 默认 legacy（向后兼容），posix 需显式 opt-in。
		PermissionPriorityMode: "legacy",
		// H8 新增字段默认值（= 原硬编码值，零行为变化）
		StreamTimeoutSeconds:       300,
		CIMaxIterations:            50,
		CIMaxConsecutiveFailures:   3,
		MaxTurnsPerRequest:         40,
		CIPermission:               string(PermDangerFullAccess),
		CITimeoutMinutes:           10,
		MaxSnap:                    50,
		MaxSearchMatchesPerSession: 5,
		BudgetWarnPercent:          0.75,
		BudgetCompactPercent:       0.80,
		BudgetBlockPercent:         0.97,
		AutoCompactBufferTokens:    13000,
		InputHistoryLimit:          500,
		// AvailableModels 留空（nil），各模块检测到 nil 时用内置默认列表
		// H12 自我进化系统 Feature Flag 默认全部 false（零成本降级，）。
		// TTLScanIntervalHours=24 为后台 TTL 扫描默认间隔。
		Evolution: EvolutionConfig{
			Enabled:              false,
			AutoRun:              false,
			TTLScanIntervalHours: 24,
		},
		// T1-Btoken 估算默认配置：
		// TiktokenEnabled=false（默认禁用 L3，避免首次下载 1.8MB 编码文件）
		// OffsetTTL=30（30 分钟 EMA offset 缓存时长）
		Token: TokenConfig{
			TiktokenEnabled: false,
			OffsetTTL:       30,
		},
	}
}

// DefaultConfigPtr returns a pointer to the default configuration
func DefaultConfigPtr() *Config {
	cfg := DefaultConfig()
	return &cfg
}

// Loader configuration loader
type Loader struct {
	userDir    string
	projectDir string
}

// NewLoader creates a configuration loader
func NewLoader(userDir, projectDir string) *Loader {
	return &Loader{userDir: userDir, projectDir: projectDir}
}

// readSources 只读读取并解析所有已存在的配置源（按 discoverSources 顺序）。
//
// 返回：
// - sources：全部候选路径（含缺失项，供 Load 的迁移步骤枚举）
// - raws：成功解析的源（与 sources 中「已存在且合法」项同序）
// - err：当「某源存在但不可读」时包装 ErrUnreadableConfig，
// 当「某源存在但 JSON 损坏」时包装 ErrCorruptConfig（两者均为 fail-closed）。
//
// **永不写盘**（Q4/A：Load 与预检均为纯只读）。抽出为单一实现，避免
// 「什么算损坏」在 Load / 预检两处漂移（这正是本域要根治的一类缺陷）。
//
// 读失败口径（A9，与同包 migrateWithBackup 一致）：
// - `os.IsNotExist` → 跳过（全新安装的合法状态，对齐业内既有实现）
// - 其它（EACCES / IO / EISDIR）→ **返回错误**（fail-closed，不得静默跳过）
//
// 注意「不可读」为何不能沿用 `continue`：读不到源 ⇒ 拿不到该源里的安全相关设置
// （如 `permission_mode`）。静默跳过后，进程会以**更宽的默认值**继续运行
// （实测：源里写 `read-only`，跳过时进程实际按 `workspace-write` 跑）——
// 这正是 I2 意义上的 fail-open，且用户完全不可见。
func (l *Loader) readSources() ([]string, []map[string]interface{}, error) {
	sources := l.discoverSources()
	raws := make([]map[string]interface{}, 0, len(sources))
	for _, source := range sources {
		data, err := os.ReadFile(source)
		if err != nil {
			if os.IsNotExist(err) {
				// 文件缺失 = 全新安装的合法状态，跳过（对齐业内既有实现：缺失不报错）。
				continue
			}
			// A9 · fail-closed T3：存在但读不出来（权限/IO）⇒ 拒绝启动。
			// 与 S12-PR#2 的「损坏拒启」同档（§2 A9 行）。
			// 与 A8/S10-G2「同族同档」：fail-open 家族一律收口为拒绝。
			return sources, nil, fmt.Errorf("%w: %s: %v", ErrUnreadableConfig, source, err)
		}

		var raw map[string]interface{}
		if err := json.Unmarshal(data, &raw); err != nil {
			// Q1/A：文件存在但损坏 → fail-closed（返回 error），绝不静默回退到宽松默认。
			// Q4/A：此处不恢复 —— 恢复是显式流程 Loader.RestoreFromBackup（doctor --repair）。
			return sources, nil, fmt.Errorf("%w: %s: %v", ErrCorruptConfig, source, err)
		}
		raws = append(raws, raw)
	}
	return sources, raws, nil
}

// CheckIntegrity 只读检测所有配置源是否可解析（Q1/A 启动前置预检）。
// 不修改任何文件；全部正常（含「文件缺失」）返回 nil，否则返回
// ErrCorruptConfig / ErrUnreadableConfig 包装错误。
// 用于在可能写盘的流程（如 onboarding 向导）**之前**先做 fail-closed 判定，
// 避免向导用新配置覆盖损坏文件、从而绕过「拒绝启动」。
func (l *Loader) CheckIntegrity() error {
	_, _, err := l.readSources()
	return err
}

// Load load and merge all configuration sources
// Priority order (highest to lowest): environment variables > project config > user config > defaults
func (l *Loader) Load() (*Config, error) {
	config := DefaultConfig()

	sources, raws, err := l.readSources()
	if err != nil {
		// Q1/A：损坏配置 → fail-closed，不返回可用配置。
		// A9：不可读配置 → 同档 fail-closed（T3），同样不得返回默认配置。
		return nil, err
	}
	for _, raw := range raws {
		mergeInto(&config, raw)
	}

	// Migrate config if version is outdated
	// V3-1-3：迁移前自动备份所有已加载源文件，避免迁移失败导致配置丢失。
	if config.ConfigVersion < CurrentConfigVersion {
		for _, src := range sources {
			if _, err := migrateWithBackup(src); err != nil {
				log.Printf("[config] backup before migration failed for %s: %v", src, err)
			}
		}
		migrateConfig(&config)
	}

	l.applyEnvironmentOverrides(&config)

	// Validate config after merge
	if errs := config.Validate(); len(errs) > 0 {
		// 3-C-3 修复：不再 _ = err 显式忽略，通过 log.Printf 记录验证警告。
		// 保留 Validate 内自动回填默认值副作用（决策 6 选 A，向后兼容）。
		for _, err := range errs {
			log.Printf("[config] validation warning: %v", err)
		}
	}

	// validate 补充字段范围验证（Validate() 未覆盖的关键字段，如 max_tokens）
	if err := config.validate(); err != nil {
		log.Printf("[config] validation error: %v", err)
	}

	// negative ContextWindow treated as unset, determined by model
	if config.ContextWindow < 0 {
		config.ContextWindow = 0
	}

	return &config, nil
}

// Sources 返回按优先级排序的配置源路径列表（显式修复流程用于枚举）。PR#2 新增。
func (l *Loader) Sources() []string { return l.discoverSources() }

// RestoreFromBackup 显式从最新的 .bak 备份恢复配置源（Q4/A：恢复不再隐式发生在 Load 中）。
//
// 这是**显式恢复入口**，含写副作用：原子改写 source + 消费掉已用的 .bak。
// 仅应由显式流程（`whale doctor --repair`）调用；Load 已不再自动触发它。
//
// 备份命名兼容：优先 glob `{source}.*.bak`（Save/migrateWithBackup 的时间戳格式），
// 回退到固定名 `{source}.bak`（旧格式）。
// 返回：是否成功恢复；写盘失败返回 error（不静默吞掉）。
func (l *Loader) RestoreFromBackup(source string) (bool, error) {
	matches, _ := filepath.Glob(source + ".*.bak")
	if len(matches) == 0 {
		// 回退到固定名（兼容旧格式）
		matches = []string{source + ".bak"}
	}
	// 按文件名排序取最新
	sort.Sort(sort.Reverse(sort.StringSlice(matches)))
	for _, backupPath := range matches {
		data, err := os.ReadFile(backupPath)
		if err != nil {
			continue
		}
		// 备份本身必须是合法 JSON，否则跳过
		var test map[string]interface{}
		if json.Unmarshal(data, &test) != nil {
			continue
		}
		if err := util.AtomicWriteFile(source, data, 0o600); err != nil {
			return false, fmt.Errorf("从备份 %s 恢复 %s 失败: %w", backupPath, source, err)
		}
		os.Remove(backupPath)
		return true, nil
	}
	return false, nil
}

// discoverSources discover config files (ordered by priority)
func (l *Loader) discoverSources() []string {
	var sources []string

	if l.userDir != "" {
		sources = append(sources,
			filepath.Join(l.userDir, ".whale.json"),
			filepath.Join(whalehome.Dir(), "settings.json"),
		)
	}

	if l.projectDir != "" {
		sources = append(sources,
			filepath.Join(l.projectDir, ".whale.json"),
			filepath.Join(l.projectDir, ".whale", "settings.json"),
			filepath.Join(l.projectDir, ".whale", "settings.local.json"),
		)
	}

	return sources
}

// applyEnvironmentOverrides 应用环境变量覆盖配置。
//
// 三模型字段（Model/FallbackModel/FastModel）：环境变量仅在 settings.json 对应字段
// 为空时作为 fallback 生效，使 settings.json 显式配置优先于环境变量；
// 其余字段：环境变量保持最高优先级，覆盖 settings.json 值（向后兼容）。
func (l *Loader) applyEnvironmentOverrides(c *Config) {
	// 仅当 settings.json 字段为空时才读 env 作为 fallback（P6 候选 B）
	if c.Model == "" {
		if v := os.Getenv("WHALE_MODEL"); v != "" {
			c.Model = v
		}
	}
	if c.FallbackModel == "" {
		if v := os.Getenv("WHALE_FALLBACK_MODEL"); v != "" {
			c.FallbackModel = v
		}
	}
	// Q5/A（PR#2）：env 权限模式**只能收紧或持平**，不得凌驾 settings 放宽。
	// 对齐业内既有实现/同类 CLI：放宽权限必须走显式 CLI flag（如 --dangerously-bypass-...），
	// env 不是放宽入口。模式名→Level 解析统一走 permission.LookupModeName（单一真相源）。
	// Level 语义：LevelReadOnly(0) < LevelWorkspaceWrite(1) < LevelDangerFullAccess(2)，越小越严。
	if v := os.Getenv("WHALE_PERMISSION_MODE"); v != "" {
		if envLevel, ok := permission.LookupModeName(v); ok {
			current, _ := permission.LookupModeName(string(c.PermissionMode))
			if envLevel <= current {
				c.PermissionMode = PermissionMode(envLevel.String())
			} else {
				log.Printf("[config] ignore WHALE_PERMISSION_MODE=%q: env 不得放宽权限（当前 %s）",
					v, c.PermissionMode)
			}
		} else {
			log.Printf("[config] ignore unknown WHALE_PERMISSION_MODE=%q", v)
		}
	}
	if v := os.Getenv("WHALE_CONTEXT_WINDOW"); v != "" {
		var n int
		if _, err := fmt.Sscanf(v, "%d", &n); err == nil && n > 0 {
			c.ContextWindow = n
		}
	}
	if v := os.Getenv("WHALE_SESSION_BUDGET"); v != "" {
		var f float64
		if _, err := fmt.Sscanf(v, "%f", &f); err == nil {
			c.SessionBudget = f
		}
	}
	// N14：WHALE_FAST_MODE 覆盖 fast_mode_enabled（"1"/"true" 启用，其他均视为禁用）
	if v := os.Getenv("WHALE_FAST_MODE"); v != "" {
		c.FastModeEnabled = v == "1" || v == "true"
	}
	// N14：WHALE_FAST_MODEL 仅当 settings.json fast_model 为空时作为 fallback（P6 候选 B）
	if c.FastModel == "" {
		if v := os.Getenv("WHALE_FAST_MODEL"); v != "" {
			c.FastModel = v
		}
	}
	// 2026-10-14：WHALE_COMPACT_MODEL 仅当 settings.json compact_model 为空时作为 fallback
	// （与三模型字段同规矩：settings.json 显式配置优先）。
	if c.CompactModel == "" {
		if v := os.Getenv("WHALE_COMPACT_MODEL"); v != "" {
			c.CompactModel = v
		}
	}
	// WHALE_BRIDGE_ENABLED 覆盖 bridge_enabled 配置（"1"/"true" 启用，其他均视为禁用）
	if v := os.Getenv("WHALE_BRIDGE_ENABLED"); v != "" {
		c.BridgeEnabled = v == "1" || v == "true"
	}
	// N10：bridge 安全配置项 env 覆盖（WHALE_BRIDGE_JWT_SECRET 等）
	if v := os.Getenv("WHALE_BRIDGE_JWT_SECRET"); v != "" {
		c.Bridge.JwtSecret = v
	}
	if v := os.Getenv("WHALE_BRIDGE_EXPECTED_ISSUER"); v != "" {
		c.Bridge.ExpectedIssuer = v
	}
	if v := os.Getenv("WHALE_BRIDGE_EXPECTED_AUDIENCE"); v != "" {
		c.Bridge.ExpectedAudience = v
	}
	if v := os.Getenv("WHALE_BRIDGE_CLOCK_SKEW_SECONDS"); v != "" {
		var n int64
		if _, err := fmt.Sscanf(v, "%d", &n); err == nil && n >= 0 {
			c.Bridge.ClockSkewSeconds = n
		}
	}
	if v := os.Getenv("WHALE_BRIDGE_COMMAND_GUARD_ENABLED"); v != "" {
		c.Bridge.CommandGuardEnabled = v == "1" || v == "true"
	}
	if v := os.Getenv("WHALE_BRIDGE_RECONNECT_MAX_ATTEMPTS"); v != "" {
		var n int
		if _, err := fmt.Sscanf(v, "%d", &n); err == nil && n > 0 {
			c.Bridge.ReconnectMaxAttempts = n
		}
	}
	// N10 默认值：ClockSkewSeconds=30, ReconnectMaxAttempts=5
	if c.Bridge.ClockSkewSeconds == 0 {
		c.Bridge.ClockSkewSeconds = 30
	}
	if c.Bridge.ReconnectMaxAttempts == 0 {
		c.Bridge.ReconnectMaxAttempts = 5
	}

	// H8 新增 env 覆盖（统一 WHALE_ 前缀，）
	// WHALE_STREAM_TIMEOUT 覆盖 stream_timeout_seconds
	if v := os.Getenv("WHALE_STREAM_TIMEOUT"); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			c.StreamTimeoutSeconds = n
		}
	}
	// WHALE_CI_MAX_ITERATIONS 覆盖 ci_max_iterations
	if v := os.Getenv("WHALE_CI_MAX_ITERATIONS"); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			c.CIMaxIterations = n
		}
	}
	// WHALE_CI_MAX_FAILURES 覆盖 ci_max_consecutive_failures
	if v := os.Getenv("WHALE_CI_MAX_FAILURES"); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			c.CIMaxConsecutiveFailures = n
		}
	}
	// WHALE_MAX_TURNS_PER_REQUEST 覆盖 max_turns_per_request（P0 轮次上限）
	if v := os.Getenv("WHALE_MAX_TURNS_PER_REQUEST"); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			c.MaxTurnsPerRequest = n
		}
	}
	// WHALE_CI_PERMISSION 覆盖 ci_permission（收口原 plain.go:1675 的 env 读取）
	if v := os.Getenv("WHALE_CI_PERMISSION"); v != "" {
		c.CIPermission = v
	}
	// WHALE_CI_TIMEOUT 覆盖 ci_timeout_minutes
	if v := os.Getenv("WHALE_CI_TIMEOUT"); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			c.CITimeoutMinutes = n
		}
	}
	// WHALE_MAX_SNAP 覆盖 max_snap
	if v := os.Getenv("WHALE_MAX_SNAP"); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			c.MaxSnap = n
		}
	}
	// WHALE_MAX_SEARCH_MATCHES 覆盖 max_search_matches_per_session
	if v := os.Getenv("WHALE_MAX_SEARCH_MATCHES"); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			c.MaxSearchMatchesPerSession = n
		}
	}
	// WHALE_BUDGET_WARN_PERCENT 覆盖 budget_warn_percent
	if v := os.Getenv("WHALE_BUDGET_WARN_PERCENT"); v != "" {
		if f, err := strconv.ParseFloat(v, 64); err == nil {
			c.BudgetWarnPercent = f
		}
	}
	// WHALE_BUDGET_COMPACT_PERCENT 覆盖 budget_compact_percent
	if v := os.Getenv("WHALE_BUDGET_COMPACT_PERCENT"); v != "" {
		if f, err := strconv.ParseFloat(v, 64); err == nil {
			c.BudgetCompactPercent = f
		}
	}
	// WHALE_BUDGET_BLOCK_PERCENT 覆盖 budget_block_percent
	if v := os.Getenv("WHALE_BUDGET_BLOCK_PERCENT"); v != "" {
		if f, err := strconv.ParseFloat(v, 64); err == nil {
			c.BudgetBlockPercent = f
		}
	}
	// WHALE_AUTOCOMPACT_BUFFER 覆盖 auto_compact_buffer_tokens
	if v := os.Getenv("WHALE_AUTOCOMPACT_BUFFER"); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			c.AutoCompactBufferTokens = n
		}
	}
	// WHALE_INPUT_HISTORY_LIMIT 覆盖 input_history_limit
	if v := os.Getenv("WHALE_INPUT_HISTORY_LIMIT"); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			c.InputHistoryLimit = n
		}
	}

	// M22-05：WHALE_METRICS_AUTH_TOKEN 覆盖 metrics_auth_token
	if v := os.Getenv("WHALE_METRICS_AUTH_TOKEN"); v != "" {
		c.MetricsAuthToken = v
	}
	// WHALE_PERMISSION_PRIORITY_MODE 覆盖 permission_priority_mode
	if v := os.Getenv("WHALE_PERMISSION_PRIORITY_MODE"); v != "" {
		c.PermissionPriorityMode = v
	}
	// R-3：WHALE_PLUGIN_SIGNING_* env 覆盖 plugin_signature 配置
	// 优先级：env > config file > default（与项目其他 env override 一致）
	if v := os.Getenv("WHALE_PLUGIN_SIGNING_ENABLED"); v != "" {
		c.PluginSignature.Enabled = v == "1" || v == "true"
	}
	if v := os.Getenv("WHALE_PLUGIN_SIGNING_REQUIRED"); v != "" {
		c.PluginSignature.Required = v == "1" || v == "true"
	}
	if v := os.Getenv("WHALE_PLUGIN_SIGNING_PUBLIC_KEY_PATH"); v != "" {
		c.PluginSignature.PublicKeyPath = v
	}
	// H13-G5：WHALE_LEAN_SYSTEM_PROMPT 覆盖 lean_system_prompt（"1"/"true" 启用，其他均视为禁用）
	if v := os.Getenv("WHALE_LEAN_SYSTEM_PROMPT"); v != "" {
		c.LeanSystemPrompt = v == "1" || v == "true"
	}
}

// mergeInto merge raw JSON into config
func mergeInto(c *Config, raw map[string]interface{}) {
	if v, ok := getFloat(raw, "config_version"); ok {
		c.ConfigVersion = int(v)
	}
	if v, ok := getString(raw, "model"); ok {
		c.Model = v
	}
	if v, ok := getString(raw, "fallback_model"); ok {
		c.FallbackModel = v
	}
	if v, ok := getString(raw, "permission_mode"); ok {
		c.PermissionMode = PermissionMode(v)
	}
	// 权限规则来源优先级模式解析（legacy/posix）。
	if v, ok := getString(raw, "permission_priority_mode"); ok {
		c.PermissionPriorityMode = v
	}
	if v, ok := getFloat(raw, "context_window"); ok {
		c.ContextWindow = int(v)
	}
	if v, ok := getString(raw, "compaction_strategy"); ok {
		if v == "preserve-user" {
			v = string(CompactPreserveUser)
		}
		c.CompactionStrategy = CompactionStrategy(v)
	}
	if v, ok := getBool(raw, "auto_memory_enabled"); ok {
		c.AutoMemoryEnabled = v
	}
	if v, ok := getBool(raw, "thinking_enabled"); ok {
		c.ThinkingEnabled = &v
	}
	if v, ok := getFloat(raw, "thinking_budget_tokens"); ok {
		n := int(v)
		c.ThinkingBudgetTokens = &n
	}
	if v, ok := getFloat(raw, "session_budget"); ok {
		c.SessionBudget = v
	}
	if v, ok := getStringSlice(raw, "allowed_tools"); ok {
		c.AllowedTools = v
	}
	if v, ok := getBool(raw, "vim_mode"); ok {
		c.VimMode = v
	}
	// N7：keybindings 配置解析。若 raw 中无 keybindings 字段，保持零值 Config{}（向后兼容）。
	if v, ok := raw["keybindings"]; ok {
		if data, ok := v.([]byte); ok {
			if err := json.Unmarshal(data, &c.Keybindings); err != nil {
				log.Printf("warning: failed to unmarshal keybindings config: %v", err)
			}
		}
	}
	if v, ok := getBool(raw, "auto_compact"); ok {
		c.AutoCompact = v
	}
	// CompactArchive 合并：默认 true，仅显式 false 关闭（归档安全网）。
	if v, ok := getBool(raw, "compact_archive"); ok {
		c.CompactArchive = v
	}
	// BridgeEnabled 合并：默认 false，仅显式 true 才启用（安全 fail-closed）
	if v, ok := getBool(raw, "bridge_enabled"); ok {
		c.BridgeEnabled = v
	}
	// N10：bridge 安全配置项合并
	if bridgeRaw, ok := raw["bridge"].(map[string]interface{}); ok {
		if v, ok := getString(bridgeRaw, "jwt_secret"); ok {
			c.Bridge.JwtSecret = v
		}
		if v, ok := getString(bridgeRaw, "expected_issuer"); ok {
			c.Bridge.ExpectedIssuer = v
		}
		if v, ok := getString(bridgeRaw, "expected_audience"); ok {
			c.Bridge.ExpectedAudience = v
		}
		if v, ok := getFloat(bridgeRaw, "clock_skew_seconds"); ok {
			c.Bridge.ClockSkewSeconds = int64(v)
		}
		if v, ok := getBool(bridgeRaw, "command_guard_enabled"); ok {
			c.Bridge.CommandGuardEnabled = v
		}
		if v, ok := getFloat(bridgeRaw, "reconnect_max_attempts"); ok {
			c.Bridge.ReconnectMaxAttempts = int(v)
		}
	}
	// N10 默认值（mergeInto 后再补默认值，避免被零值覆盖）
	if c.Bridge.ClockSkewSeconds == 0 {
		c.Bridge.ClockSkewSeconds = 30
	}
	if c.Bridge.ReconnectMaxAttempts == 0 {
		c.Bridge.ReconnectMaxAttempts = 5
	}
	// R-3：合并 plugin_signature 子结构（向后兼容，缺失时保持零值 Enabled=false）
	// 旧 settings.json 无此字段时 PluginSignatureConfig{}.Enabled=false，
	// buildPluginSignatureVerifier 走 env fallback 路径（桶 X5 行为）。
	if sigRaw, ok := raw["plugin_signature"].(map[string]interface{}); ok {
		if v, ok := getBool(sigRaw, "enabled"); ok {
			c.PluginSignature.Enabled = v
		}
		if v, ok := getBool(sigRaw, "required"); ok {
			c.PluginSignature.Required = v
		}
		if v, ok := getString(sigRaw, "public_key_path"); ok {
			c.PluginSignature.PublicKeyPath = v
		}
	}
	// N18：合并 Onboarding 相关字段（onboarded/provider/api_key/custom_base_url）。
	// 缺失时保持零值（false/""），向后兼容旧 settings.json。
	if v, ok := getBool(raw, "onboarded"); ok {
		c.Onboarded = v
	}
	if v, ok := getString(raw, "provider"); ok {
		c.Provider = v
	}
	if v, ok := getString(raw, "api_key"); ok {
		c.APIKey = v
	}
	if v, ok := getString(raw, "custom_base_url"); ok {
		c.CustomBaseURL = v
	}
	// approval 为嵌套对象，从子键 policy 读取并合并到 Config.Approval.Policy。
	if approvalRaw, ok := raw["approval"].(map[string]interface{}); ok {
		if v, ok := approvalRaw["policy"].(string); ok {
			c.Approval.Policy = v
		}
	}
	// D 批：custom_pricing 合并分支随成本线删除（见 Config 字段处注释）。

	// 2b 修复：补齐遗漏的 7 个字段合并分支（FACT-CONFIG-002）

	// mcp_servers：map[string]McpServerConfig，整体覆盖（非深合并）
	if mcpRaw, ok := raw["mcp_servers"].(map[string]interface{}); ok && len(mcpRaw) > 0 {
		merged := make(map[string]McpServerConfig, len(mcpRaw))
		for name, val := range mcpRaw {
			m, ok := val.(map[string]interface{})
			if !ok {
				continue
			}
			mc := McpServerConfig{}
			if v, ok := m["command"].(string); ok {
				mc.Command = v
			}
			if args, ok := m["args"].([]interface{}); ok {
				mc.Args = make([]string, 0, len(args))
				for _, a := range args {
					if s, ok := a.(string); ok {
						mc.Args = append(mc.Args, s)
					}
				}
			}
			if envRaw, ok := m["env"].(map[string]interface{}); ok {
				mc.Env = make(map[string]string, len(envRaw))
				for k, ev := range envRaw {
					if s, ok := ev.(string); ok {
						mc.Env[k] = s
					}
				}
			}
			if v, ok := m["url"].(string); ok {
				mc.URL = v
			}
			merged[name] = mc
		}
		if len(merged) > 0 {
			c.McpServers = merged
		}
	}

	// hooks：嵌套结构，解析 PreToolUse/PostToolUse/Stop 三个子数组
	if hooksRaw, ok := raw["hooks"].(map[string]interface{}); ok {
		hc := HookConfig{}
		hc.PreToolUse = parseHookEntries(hooksRaw["pre_tool_use"])
		hc.PostToolUse = parseHookEntries(hooksRaw["post_tool_use"])
		hc.Stop = parseHookEntries(hooksRaw["stop"])
		// 仅在至少一个子数组非空时覆盖，避免空 hooks 覆盖默认
		if len(hc.PreToolUse) > 0 || len(hc.PostToolUse) > 0 || len(hc.Stop) > 0 {
			c.Hooks = hc
		}
	}

	// output_style
	if v, ok := getString(raw, "output_style"); ok {
		c.OutputStyle = OutputStyle(v)
	}

	// N14：fast_mode_enabled / fast_model 合并（向后兼容，缺失时保持零值）
	if v, ok := getBool(raw, "fast_mode_enabled"); ok {
		c.FastModeEnabled = v
	}
	if v, ok := getString(raw, "fast_model"); ok {
		c.FastModel = v
	}
	// 省 token 第 1 批：compact_model 合并（缺失时保持空 ⇒ 沿用主模型）。
	// ★ 注意：settings.json 不走通用 json.Unmarshal，而是这里的**逐字段手工合并** ——
	// 新增任何配置字段都必须在这张表里补一行，否则字段"能声明、读不到"（静默）。
	if v, ok := getString(raw, "compact_model"); ok {
		c.CompactModel = v
	}

	// whale_env：map[string]string
	if envRaw, ok := raw["whale_env"].(map[string]interface{}); ok && len(envRaw) > 0 {
		merged := make(map[string]string, len(envRaw))
		for k, v := range envRaw {
			if s, ok := v.(string); ok {
				merged[k] = s
			}
		}
		if len(merged) > 0 {
			c.WhaleEnv = merged
		}
	}

	// cache_break_threshold
	if v, ok := getFloat(raw, "cache_break_threshold"); ok {
		c.CacheBreakThreshold = v
	}

	// metrics_enabled
	if v, ok := getBool(raw, "metrics_enabled"); ok {
		c.MetricsEnabled = v
	}

	// metrics_addr
	if v, ok := getString(raw, "metrics_addr"); ok {
		c.MetricsAddr = v
	}

	// M22-05：metrics_auth_token 合并（向后兼容，缺失时保持空）
	if v, ok := getString(raw, "metrics_auth_token"); ok {
		c.MetricsAuthToken = v
	}

	// H8 新增字段合并（模式：源字段非零值则覆盖目标）
	if v, ok := getFloat(raw, "stream_timeout_seconds"); ok {
		c.StreamTimeoutSeconds = int(v)
	}
	if v, ok := getFloat(raw, "ci_max_iterations"); ok {
		c.CIMaxIterations = int(v)
	}
	if v, ok := getFloat(raw, "ci_max_consecutive_failures"); ok {
		c.CIMaxConsecutiveFailures = int(v)
	}
	if v, ok := getFloat(raw, "max_turns_per_request"); ok {
		c.MaxTurnsPerRequest = int(v)
	}
	if v, ok := getString(raw, "ci_permission"); ok {
		c.CIPermission = v
	}
	if v, ok := getFloat(raw, "ci_timeout_minutes"); ok {
		c.CITimeoutMinutes = int(v)
	}
	if v, ok := getFloat(raw, "max_snap"); ok {
		c.MaxSnap = int(v)
	}
	if v, ok := getFloat(raw, "max_search_matches_per_session"); ok {
		c.MaxSearchMatchesPerSession = int(v)
	}
	if v, ok := getFloat(raw, "budget_warn_percent"); ok {
		c.BudgetWarnPercent = v
	}
	if v, ok := getFloat(raw, "budget_compact_percent"); ok {
		c.BudgetCompactPercent = v
	}
	if v, ok := getFloat(raw, "budget_block_percent"); ok {
		c.BudgetBlockPercent = v
	}
	if v, ok := getFloat(raw, "auto_compact_buffer_tokens"); ok {
		c.AutoCompactBufferTokens = int(v)
	}
	if v, ok := getFloat(raw, "input_history_limit"); ok {
		c.InputHistoryLimit = int(v)
	}
	if v, ok := getStringSlice(raw, "available_models"); ok {
		c.AvailableModels = v
	}

	// H12 Evolution 配置合并（向后兼容，缺失时保持零值）
	if evolutionRaw, ok := raw["evolution"].(map[string]interface{}); ok {
		if v, ok := getBool(evolutionRaw, "enabled"); ok {
			c.Evolution.Enabled = v
		}
		if v, ok := getBool(evolutionRaw, "auto_run"); ok {
			c.Evolution.AutoRun = v
		}
		if v, ok := getFloat(evolutionRaw, "ttl_scan_interval_hours"); ok {
			c.Evolution.TTLScanIntervalHours = int(v)
		}
	}

	// 候选 B：解析 model_capabilities 字段（用户自定义模型能力注册表）。
	// 落实审查建议优化 3：解析后 log warning 提示用户覆盖了内置注册表。
	if mcRaw, ok := raw["model_capabilities"].(map[string]interface{}); ok {
		c.ModelCapabilities = make(map[string]ModelCapabilities)
		for k, v := range mcRaw {
			if capsMap, ok := v.(map[string]interface{}); ok {
				caps := ModelCapabilities{}
				if b, ok := getBool(capsMap, "supports_effort"); ok {
					caps.SupportsEffort = b
				}
				if b, ok := getBool(capsMap, "supports_max_effort"); ok {
					caps.SupportsMaxEffort = b
				}
				// ★ B5：`supports_thinking` / `supports_cache` 两个键已废弃
				// —— 对应的能力位在任何地方都不被读取（原字段已删）。这里**不静默忽略**，
				// 而是打印一条 log 指路：用户写了却以为生效，正是 那类误解。
				if _, deprecated := capsMap["supports_thinking"]; deprecated {
					log.Printf("config: model_capabilities[%q].supports_thinking is IGNORED (deprecated 2026-10-05); "+
						"use `/model add|edit <model> --supports-thinking <supported|unsupported>` instead", k)
				}
				if _, deprecated := capsMap["supports_cache"]; deprecated {
					log.Printf("config: model_capabilities[%q].supports_cache is IGNORED (deprecated 2026-10-05); "+
						"cache capability comes from the internal/ai table + runtime probing (no user override)", k)
				}
				// S6 / L1：原生检索能力位（只用于**启用**，见 ModelCapabilities.SupportsNativeSearch 注释）
				if b, ok := getBool(capsMap, "supports_native_search"); ok {
					caps.SupportsNativeSearch = b
				}
				c.ModelCapabilities[k] = caps
			}
		}
		if len(c.ModelCapabilities) > 0 {
			log.Printf("config: user model_capabilities overrides builtin registry (%d entries)", len(c.ModelCapabilities))
		}
	}

	// V3-4-2：resource 资源降级配置合并（v1.0 仅配置层，v1.1 接线模块）。
	// 默认全零值（不降级），向后兼容旧 settings.json 无此字段的情况。
	if resourceRaw, ok := raw["resource"].(map[string]interface{}); ok {
		if v, ok := getBool(resourceRaw, "low_memory"); ok {
			c.Resource.LowMemory = v
		}
		if v, ok := getBool(resourceRaw, "disable_lsp"); ok {
			c.Resource.DisableLSP = v
		}
		if v, ok := getBool(resourceRaw, "reduce_cache"); ok {
			c.Resource.ReduceCache = v
		}
	}

	// H13-G5：lean_system_prompt 字段合并（向后兼容，缺失时保持零值 false）。
	// 旧 settings.json 无此字段时 LeanSystemPrompt=false（保持现有完整 system prompt 行为）。
	if v, ok := getBool(raw, "lean_system_prompt"); ok {
		c.LeanSystemPrompt = v
	}
}

func getString(m map[string]interface{}, key string) (string, bool) {
	v, ok := m[key].(string)
	return v, ok
}

func getFloat(m map[string]interface{}, key string) (float64, bool) {
	switch v := m[key].(type) {
	case float64:
		return v, true
	case json.Number:
		if f, err := v.Float64(); err == nil {
			return f, true
		}
	}
	return 0, false
}

func getBool(m map[string]interface{}, key string) (bool, bool) {
	v, ok := m[key].(bool)
	return v, ok
}

func getStringSlice(m map[string]interface{}, key string) ([]string, bool) {
	v, ok := m[key].([]interface{})
	if !ok {
		return nil, false
	}
	result := make([]string, 0, len(v))
	for _, item := range v {
		if s, ok := item.(string); ok {
			result = append(result, s)
		}
	}
	return result, true
}

// ── 保存的无损性：Save / SaveToFile 共用的「读-改-写」─────────────────────
//
// ★ ：为什么必须是「默认保留未建模键」，而不是「白名单」。
//
// settings.json 的真相源是**文件本身**；*Config 只是它的一个**部分视图** ——
// 结构体只声明了它关心的那些键。而 Save 早先的实现是：把内存结构体整份序列化后
// 覆盖写回 ⇒ **视图没建模的键一律被删除**（结构体不知道它们存在，于是"如实"地删掉了）。
//
// 事故（取证）：2026-10-06 16:48:56 一次普通保存抹掉了 `interaction` 块
// （同一时刻的备份 .20261006-164856.bak 内它还在），而 tui.LoadAskSettings 把它读成 0
// ⇒ 「模型要问用户」被静默关掉 ⇒ AskUser 面板永不弹。用户看到的是"AI 不问我了"，
// 完全看不出根因在一次无关的设置保存上。
//
// 当时的补丁是给 4 个 auth 键加白名单（原 authSensitiveFields）。那是**默认破坏方向**的
// 修补：默认丢弃，靠人记得把每个新键加进来 ⇒ 任何**别的子系统读的键**都会再被吃一次
// （这正是它一周内复发、且这次以 interaction 的形态复发的原因）。
// 本实现把默认翻转为**保留**，让"忘记"这个失败模式在结构上不可能发生。
//
// 两条方向自证（都是判据，见 preserve_unknown_keys_test.go）：
//
//	① 磁盘上有、结构体没声明 ⇒ 原样保留（TestW038_Save_PreservesUnknown*）
//	② 结构体声明过的键 ⇒ 以内存新值为准，不得被旧值覆盖（…_StructStillWins）
//
// 为什么不需要「已退休键」删除名单：本仓唯一会**退休/重命名**配置键的地方是 /migrate
// （internal/command/migrate_handler.go），它直接读写 raw map（删除 apiKey / compactThreshold
// 后原子写回），不经过 Config ⇒ 天然无损，也不会被这里的保留逻辑"复活"。
// 若将来确实需要"经 Save 删除某个键"，应当把它**显式**列进一个具名清单并写明理由，
// 而不是继续依赖"结构体忘了建模"这种隐式副作用。

// marshalConfigPreservingUnknown 序列化 cfg，并把 existingData 中「cfg 未声明」的键保留下来
// （对嵌套对象递归）。语义 = 读-改-写：以磁盘上已有的 JSON 为底，把新序列化的结构体**叠上去**；
// 结构体只对它自己输出的键权威，未提及的键原样透传。
//
// existingData 为空或不可解析时退化为普通序列化（与历史行为一致，且不拿损坏的旧文件污染新内容）。
// 注意：合并后由 map 序列化 ⇒ 顶层键按字母序输出（与 Save 既有的 auth 透传路径一致，
// 也是本机 settings.json 的既有形态）；测试只断言内容，不锁定顺序。
func marshalConfigPreservingUnknown(cfg *Config, existingData []byte) ([]byte, error) {
	newBytes, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return nil, err
	}
	if len(existingData) == 0 {
		return newBytes, nil
	}
	var newMap, oldMap map[string]interface{}
	if json.Unmarshal(newBytes, &newMap) != nil {
		return newBytes, nil
	}
	if json.Unmarshal(existingData, &oldMap) != nil {
		return newBytes, nil
	}
	merged, err := json.MarshalIndent(mergeUnknownKeys(newMap, oldMap), "", "  ")
	if err != nil {
		return newBytes, nil
	}
	return merged, nil
}

// mergeUnknownKeys 把 fallback 中「authoritative 未声明」的键补进 authoritative 并返回它（就地修改）。
// 两边同为本 JSON 对象时递归；否则 authoritative 胜（数组与标量整体替换 —— 结构体对它们权威）。
func mergeUnknownKeys(authoritative, fallback map[string]interface{}) map[string]interface{} {
	for k, v := range fallback {
		av, declared := authoritative[k]
		if !declared {
			authoritative[k] = v
			continue
		}
		aMap, aIsMap := av.(map[string]interface{})
		fMap, fIsMap := v.(map[string]interface{})
		if aIsMap && fIsMap {
			authoritative[k] = mergeUnknownKeys(aMap, fMap)
		}
	}
	return authoritative
}

// parseHookEntries 从 raw JSON 数组解析 HookEntry 列表。
// val 为 raw["pre_tool_use"] 等子键的值（可能为 nil/[]interface{}）。
func parseHookEntries(val interface{}) []HookEntry {
	arr, ok := val.([]interface{})
	if !ok {
		return nil
	}
	result := make([]HookEntry, 0, len(arr))
	for _, item := range arr {
		m, ok := item.(map[string]interface{})
		if !ok {
			continue
		}
		he := HookEntry{}
		if v, ok := m["command"].(string); ok {
			he.Command = v
		}
		if v, ok := m["shell"].(string); ok {
			he.Shell = v
		}
		result = append(result, he)
	}
	return result
}

// Save persist configuration to settings.json in user directory.
// 3-C-4 修复：使用跨进程文件锁（acquireLock）防止并发写入冲突；
// 时间戳命名备份（settings.json.<timestamp>.bak）并在成功后保留最近 5 个（不再删除）。
// Uses atomic write (temp file + rename) to prevent corruption on crash.
// File permissions set to 0600 to protect sensitive data (API keys etc.).
// 无损保存 —— 以磁盘既有 JSON 为底读-改-写，保留 Config 未建模的键
// （见 marshalConfigPreservingUnknown 头注释）。任何"只改了结构体里某个字段"的保存，
// 都不得删除别的子系统（如 tui 的 interaction）拥有的键。
func (l *Loader) Save(cfg *Config) error {
	if l.userDir == "" {
		return fmt.Errorf("userDir not set")
	}
	settingsDir := whalehome.Dir()
	if err := os.MkdirAll(settingsDir, 0700); err != nil {
		return err
	}
	cfg.ConfigVersion = CurrentConfigVersion
	settingsPath := filepath.Join(settingsDir, "settings.json")

	// 3-C-4 新增：获取跨进程文件锁，防止并发写入冲突
	release, err := acquireLock(settingsPath)
	if err != nil {
		return fmt.Errorf("acquire lock: %w", err)
	}
	defer release()

	// 2a 修复 + 3-C-4：读取已有配置一次，同时用于「无损透传」与时间戳备份，避免重复 I/O。
	var existingData []byte
	if d, readErr := os.ReadFile(settingsPath); readErr == nil {
		existingData = d
		// 3-C-4 修复：时间戳命名备份（非固定 .bak），成功后不再删除（FACT-CONFIG-004）
		backupPath := fmt.Sprintf("%s.%s.bak", settingsPath, time.Now().Format("20060102-150405"))
		_ = util.AtomicWriteFile(backupPath, d, 0600)
	}

	// 读-改-写 —— 磁盘上存在、而 Config 未建模的键（如 interaction）必须原样保留。
	// 旧实现用 authSensitiveFields 白名单只保 4 个键；白名单已被本机制取代（见其头注释），
	// 历史接线 FACT-CONFIG-001 的 OAuth token 场景现在被同一机制覆盖，且覆盖面更宽。
	data, err := marshalConfigPreservingUnknown(cfg, existingData)
	if err != nil {
		return err
	}

	if err := util.AtomicWriteFile(settingsPath, data, 0600); err != nil {
		return err
	}

	// 3-C-4 修复：不再删除备份，保留最近 5 个（决策 5 选 A）
	cleanupOldBackups(settingsPath, 5)

	return nil
}

// SaveToFile save configuration to specified file path.
// Uses atomic write (temp file + rename) to prevent corruption on crash.
// File permissions set to 0600 to protect sensitive data.
// 与 Loader.Save 共用「读-改-写」无损透传 —— 本路径被 /output-style
// （internal/tui/runner_core.go）与 bridge（internal/tui/bridge.go）直接使用，
// 此前连 auth 白名单都没有，是比 Loader.Save 更漏的一条；两条路径必须由同一函数供能。
// 全程持**同一把**跨进程锁（acquireLock ⇒ <path>.lock，
// 与 Loader.Save:1297 同名同源）。修复前本路径无锁，与 Loader.Save 并发时
// last-writer-wins 丢更新；原子写只防撕裂、不防丢更新。
// ★ 刻意不用 util.WithFileLock（flock）—— 那是与 acquireLock **不同名**的第二把锁，
//
//	两条写路径各持一把 ⇒ 互斥形同虚设（「两把锁」病）。
func SaveToFile(cfg *Config, filePath string) error {
	dir := filepath.Dir(filePath)
	if err := os.MkdirAll(dir, 0700); err != nil {
		return err
	}
	cfg.ConfigVersion = CurrentConfigVersion

	// read→merge→write 全程持锁（与 Loader.Save 同一把 <path>.lock）
	release, err := acquireLock(filePath)
	if err != nil {
		return fmt.Errorf("acquire lock: %w", err)
	}
	defer release()

	var existingData []byte
	if d, readErr := os.ReadFile(filePath); readErr == nil {
		existingData = d
	}
	data, err := marshalConfigPreservingUnknown(cfg, existingData)
	if err != nil {
		return err
	}
	return util.AtomicWriteFile(filePath, data, 0600)
}

// migrateConfig 对配置应用 schema 迁移，将其升级到 CurrentConfigVersion。
// 引入破坏性配置变更时在此追加迁移步骤。
// R8 修复：新增 case 1 v1→v2 迁移（参考 internal/command/migrate_handler.go 的 migrateV1toV2 逻辑）。
// 注意：migrateV1toV2 在 raw JSON map 层处理 legacy 字段重命名（apiKey→apiKeys、compactThreshold→context_window），
// 此处 migrateConfig 作用于已解析的 *Config 结构体，legacy camelCase 字段在 mergeInto 阶段已被丢弃，
// 因此仅对结构体已有字段做等价校验（permission_mode 合法性），raw 层迁移由 /migrate 命令负责。
func migrateConfig(c *Config) {
	for c.ConfigVersion < CurrentConfigVersion {
		switch c.ConfigVersion {
		case 0:
			// Version 0 means config was loaded from a file without config_version field.
			// No structural changes needed, just bump the version.
			c.ConfigVersion = 1
		case 1:
			// v1→v2：参考 migrate_handler.go migrateV1toV2 的 permission_mode 校验。
			// 无效 permission_mode 回填 PermReadOnly（fail-closed，与 Validate() 保持一致，
			// 比 migrateV1toV2 的 workspace-write 默认更安全）。
			// legacy 字段重命名（apiKey→apiKeys、compactThreshold→context_window）属于 raw JSON 层迁移，
			// 由 /migrate 命令处理，此处结构体层无对应字段，跳过。
			validModes := map[PermissionMode]bool{
				PermReadOnly: true, PermWorkspaceWrite: true, PermDangerFullAccess: true,
			}
			if !validModes[c.PermissionMode] {
				c.PermissionMode = PermReadOnly
			}
			c.ConfigVersion = 2
		default:
			return
		}
	}
}

// migrateWithBackup 在迁移前自动备份配置文件。
// path: 配置文件路径；返回：备份文件路径 + 错误。
// 备份命名格式：{path}.{YYYYMMDD-HHMMSS}.bak，权限 0600（S12-G6 修复：与 Save 的 timestamp.bak 权限一致，避免同机他人读取含 api_key/jwt_secret 的备份）。
// 文件不存在时跳过备份（返回空路径 + nil 错误）。
func migrateWithBackup(path string) (string, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			// 源文件不存在时跳过备份，不视为错误
			return "", nil
		}
		return "", fmt.Errorf("读取配置文件失败: %w", err)
	}
	backupPath := fmt.Sprintf("%s.%s.bak", path, time.Now().Format("20060102-150405"))
	if err := util.AtomicWriteFile(backupPath, data, 0600); err != nil {
		return "", fmt.Errorf("创建配置备份失败: %w", err)
	}
	return backupPath, nil
}

// Validate performs post-merge validation of configuration values.
// Returns a list of validation errors (non-fatal; defaults are used where possible).
func (c *Config) Validate() []error {
	var errs []error

	// Validate permission mode
	// 3-C-3 修复：无效 permission_mode 回填到 PermReadOnly（fail-closed，最安全），
	// 而非 PermWorkspaceWrite。无效输入不应获得写权限。
	validModes := map[PermissionMode]bool{
		PermReadOnly: true, PermWorkspaceWrite: true, PermDangerFullAccess: true,
		"accept-edits": true, "dont-ask": true, "plan": true, "auto": true, "bubble": true,
	}
	if !validModes[c.PermissionMode] {
		errs = append(errs, fmt.Errorf("invalid permission_mode %q, using read-only (fail-closed)", c.PermissionMode))
		c.PermissionMode = PermReadOnly
	}

	// 校验权限规则来源优先级模式，非法值 fail-closed 回填 legacy（向后兼容）。
	validPriorityModes := map[string]bool{"legacy": true, "posix": true}
	if !validPriorityModes[c.PermissionPriorityMode] {
		errs = append(errs, fmt.Errorf("invalid permission_priority_mode %q, using legacy", c.PermissionPriorityMode))
		c.PermissionPriorityMode = "legacy"
	}

	// Validate context window
	// 小窗口（< 4096）不再回填：ContextWindow > 0 表示用户显式设置（如压缩阈值测试用
	// WHALE_CONTEXT_WINDOW=800 快速触发 Warning/ShouldCompact/Blocked），应尊重原值。
	// 语义：0 = 由模型决定；负数由 validate() 拦截并归零。
	// 安全性：CompactThreshold 在 window-buffer ≤ 0 时回退 window×CompactPercent（下限 1），无除零风险。

	// Validate compaction strategy
	validStrategies := map[CompactionStrategy]bool{
		CompactBalanced: true, CompactPreserveUser: true, CompactAggressive: true,
	}
	if !validStrategies[c.CompactionStrategy] {
		errs = append(errs, fmt.Errorf("invalid compaction_strategy %q, using default", c.CompactionStrategy))
		c.CompactionStrategy = CompactBalanced
	}

	// Validate output style
	validStyles := map[OutputStyle]bool{
		OutputDefault: true, OutputConcise: true, OutputVerbose: true, OutputTechnical: true,
	}
	if !validStyles[c.OutputStyle] {
		errs = append(errs, fmt.Errorf("invalid output_style %q, using default", c.OutputStyle))
		c.OutputStyle = OutputDefault
	}

	// Validate session budget (non-negative)
	if c.SessionBudget < 0 {
		errs = append(errs, fmt.Errorf("negative session_budget %f, using 0", c.SessionBudget))
		c.SessionBudget = 0
	}

	// 校验审批策略 Policy 取值合法性，非法时回填默认 on-request（B0 §5）。
	validPolicies := map[string]bool{
		"untrusted": true, "on-failure": true, "on-request": true, "never": true,
	}
	if !validPolicies[c.Approval.Policy] {
		errs = append(errs, fmt.Errorf("invalid approval.policy %q, using default on-request", c.Approval.Policy))
		c.Approval.Policy = "on-request"
	}

	// H8 新增字段校验（13 字段，非法值回填默认值，）
	if c.StreamTimeoutSeconds < 30 {
		errs = append(errs, fmt.Errorf("stream_timeout_seconds %d too small, using minimum 30", c.StreamTimeoutSeconds))
		c.StreamTimeoutSeconds = 30
	}
	if c.CIMaxIterations < 1 {
		errs = append(errs, fmt.Errorf("ci_max_iterations %d too small, using default 50", c.CIMaxIterations))
		c.CIMaxIterations = 50
	}
	if c.CIMaxConsecutiveFailures < 1 {
		errs = append(errs, fmt.Errorf("ci_max_consecutive_failures %d too small, using default 3", c.CIMaxConsecutiveFailures))
		c.CIMaxConsecutiveFailures = 3
	}
	if c.MaxTurnsPerRequest < 1 {
		errs = append(errs, fmt.Errorf("max_turns_per_request %d too small, using default 40", c.MaxTurnsPerRequest))
		c.MaxTurnsPerRequest = 40
	}
	// CIPermission 校验：非合法 PermissionMode 回填 PermDangerFullAccess（保持原硬编码值）
	switch c.CIPermission {
	case string(PermReadOnly), string(PermWorkspaceWrite), string(PermDangerFullAccess):
		// 合法
	default:
		errs = append(errs, fmt.Errorf("ci_permission %q invalid, using default danger-full-access", c.CIPermission))
		c.CIPermission = string(PermDangerFullAccess)
	}
	if c.CITimeoutMinutes < 1 {
		errs = append(errs, fmt.Errorf("ci_timeout_minutes %d too small, using default 10", c.CITimeoutMinutes))
		c.CITimeoutMinutes = 10
	}
	if c.MaxSnap < 1 {
		errs = append(errs, fmt.Errorf("max_snap %d too small, using default 50", c.MaxSnap))
		c.MaxSnap = 50
	}
	if c.MaxSearchMatchesPerSession < 1 {
		errs = append(errs, fmt.Errorf("max_search_matches_per_session %d too small, using default 5", c.MaxSearchMatchesPerSession))
		c.MaxSearchMatchesPerSession = 5
	}
	if c.BudgetWarnPercent < 0 || c.BudgetWarnPercent > 1 {
		errs = append(errs, fmt.Errorf("budget_warn_percent %f invalid, using default 0.75", c.BudgetWarnPercent))
		c.BudgetWarnPercent = 0.75
	}
	if c.BudgetCompactPercent < 0 || c.BudgetCompactPercent > 1 {
		errs = append(errs, fmt.Errorf("budget_compact_percent %f invalid, using default 0.80", c.BudgetCompactPercent))
		c.BudgetCompactPercent = 0.80
	}
	if c.BudgetBlockPercent < 0 || c.BudgetBlockPercent > 1 {
		errs = append(errs, fmt.Errorf("budget_block_percent %f invalid, using default 0.97", c.BudgetBlockPercent))
		c.BudgetBlockPercent = 0.97
	}
	if c.AutoCompactBufferTokens < 1000 {
		errs = append(errs, fmt.Errorf("auto_compact_buffer_tokens %d too small, using default 13000", c.AutoCompactBufferTokens))
		c.AutoCompactBufferTokens = 13000
	}
	if c.InputHistoryLimit < 10 {
		errs = append(errs, fmt.Errorf("input_history_limit %d too small, using default 500", c.InputHistoryLimit))
		c.InputHistoryLimit = 500
	}
	// AvailableModels 不校验（nil 即 fallback 到内置默认列表）

	// H12 Evolution 校验：TTLScanIntervalHours<=0 时回填默认 24（向后兼容）
	if c.Evolution.TTLScanIntervalHours <= 0 {
		errs = append(errs, fmt.Errorf("evolution.ttl_scan_interval_hours %d invalid, using default 24", c.Evolution.TTLScanIntervalHours))
		c.Evolution.TTLScanIntervalHours = 24
	}

	// 候选 B：将用户自定义模型能力注册表注入全局 userRegistry。
	// 在 Validate 末尾执行，确保 mergeInto 已完成 model_capabilities 解析。
	// 注入后 LookupModelCapabilities 查询时用户注册表优先于内置注册表。
	// model_capabilities 为 nil 时跳过（使用内置注册表，向后兼容）。
	if c.ModelCapabilities != nil {
		RegisterModelCapabilitiesFromMap(c.ModelCapabilities)
	}

	return errs
}

// validate 执行字段范围验证，返回合并后的错误信息。
// 与 Validate() 互补：validate 检查 Validate() 未覆盖的关键字段范围
// （如 ThinkingBudgetTokens、CacheBreakThreshold、ContextWindow），
// 确保非法值不被静默接受。验证失败时返回明确的错误信息，
// 调用方可选择记录日志或拒绝配置加载。
func (c *Config) validate() error {
	var errs []string

	// max_tokens 类字段验证：ThinkingBudgetTokens 若设置则必须 > 0
	if c.ThinkingBudgetTokens != nil && *c.ThinkingBudgetTokens <= 0 {
		errs = append(errs, fmt.Sprintf("thinking_budget_tokens must be > 0, got %d", *c.ThinkingBudgetTokens))
	}

	// CacheBreakThreshold 必须非负
	if c.CacheBreakThreshold < 0 {
		errs = append(errs, fmt.Sprintf("cache_break_threshold must be >= 0, got %f", c.CacheBreakThreshold))
	}

	// ContextWindow 若设置则必须 >= 0（0 表示由模型决定）
	if c.ContextWindow < 0 {
		errs = append(errs, fmt.Sprintf("context_window must be >= 0, got %d", c.ContextWindow))
	}

	if len(errs) > 0 {
		return fmt.Errorf("config validation failed: %s", strings.Join(errs, "; "))
	}
	return nil
}
