package tools

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/anchorandfish/whale-code/internal/ai"
	"github.com/anchorandfish/whale-code/internal/config"
	"github.com/anchorandfish/whale-code/internal/filehistory"
	"github.com/anchorandfish/whale-code/internal/hook"
	whalelog "github.com/anchorandfish/whale-code/internal/log"
	"github.com/anchorandfish/whale-code/internal/lsp"
	"github.com/anchorandfish/whale-code/internal/metrics"
	"github.com/anchorandfish/whale-code/internal/monitor"
	"github.com/anchorandfish/whale-code/internal/permission"
	"github.com/anchorandfish/whale-code/internal/plugin"
	"github.com/anchorandfish/whale-code/internal/safebuf"
	"github.com/anchorandfish/whale-code/internal/sandbox"
	"github.com/anchorandfish/whale-code/internal/skill"
	"github.com/anchorandfish/whale-code/internal/util"
	"github.com/anchorandfish/whale-code/internal/whalehome"
)

// AgentMessage represents an inter-agent message
type AgentMessage struct {
	From      string    `json:"from"`
	Content   string    `json:"content"`
	Timestamp time.Time `json:"timestamp"`
}

// Handler is the tool handler function signature
type Handler func(ctx context.Context, input json.RawMessage) (string, error)

// SessionStorer is the session storage interface for exporting the current session
type SessionStorer interface {
	ExportCurrentSession() ([]byte, error)
}

// Executor is the tool execution engine
type Executor struct {
	handlers   map[string]Handler
	registry   *Registry
	AIClient   ai.Client
	MonitorMgr *monitor.Manager
	// baseCtx 是 Executor 的**会话级存活面**（，2026-10-10）。
	//
	// 为什么需要它：fire-and-forget 的 fork 子 agent 需要一个「比单次工具调用长命、
	// 与会话共存亡」的 context 作寿命锚点 —— 挂在工具调用的 ctx 上会在工具返回时被
	// 调度器/批次收尾 cancel 掉（scheduler.go:308 / tool_exec.go:269），fork 当场死亡。
	// 取消时机 = Close()（main.go defer runner.Close() → runnerCore.Close → Executor.Close），
	// 语义对齐 同类 CLI 的 session.services.agent_control（子 agent 寿命归会话级 owner 管）。
	// ★ 惰性初始化（sync.Once）：Executor 存在多处构造点（NewExecutor 与字面量），
	// 惰性可保证零值 Executor 也能安全取用/关闭，不依赖每个构造点记得接线。
	baseCtx    context.Context
	baseCancel context.CancelFunc
	baseOnce   sync.Once
	// FileHistory 是统一文件历史系统（阶段 5：已替代旧 UndoStack/snapshot）。
	// 初始化失败时降级为 nil，调用方需做 nil 守卫。
	FileHistory  *filehistory.FileHistory
	currentMsgId string // 当前消息 ID（session.Message.UUID），由 TUI/runner 每轮对话开始前通过 SetCurrentMessageId 设置
	SkillMgr     *skill.Manager
	PluginMgr    *plugin.Manager
	Sandbox      *sandbox.Sandbox
	// SandboxMgr 是 Phase 0 引入的沙箱管理器接口（C1/B7）。
	// 非 nil 时，bashHandler 优先使用 Manager.Prepare + 进程组执行 + cleanup 模式，
	// 替代旧的 Sandbox.Validate 两步式验证。
	// 为 nil 时回退到 Sandbox.Validate（向后兼容）。
	SandboxMgr sandbox.Manager
	// enforcement 报告「OS 级沙箱是否真的在强制」（，2026-09-19）。
	//
	// 生产接线**不需要**直接赋值：该事实由 SandboxMgr 派生（见 reconcileEnforcement）——
	// 谁来兜住进程，谁才决定「沙箱内免问」这条豁免能不能开。
	// 本字段只是「显式覆盖」入口，供测试替身与脱离 sandbox.Manager 的装配方使用。
	//
	// nil = 不可强制（fail-closed 默认）⇒ 豁免出口 (c) 不生效，行为与改动前逐字一致。
	enforcement EnforcementStatus
	// workDir 是 Executor 创建时的工作目录，供 pluginToolHandler 等闭包复用
	// 以调用 sandbox.Manager.Prepare（P0-3/P0-6 集成）。
	workDir       string
	TaskMgr       *TaskManager
	CronMgr       *CronManager
	LoopScheduler *LoopScheduler
	// AgentTaskMgr 是 N1 subagent / Task tool 的编排器（/019）。
	// 在 NewExecutor 末尾初始化（需要 Executor 自身引用注入到 AgentTask）。
	// nil 表示未初始化（测试场景），TaskTool.IsEnabled 返回 false。
	AgentTaskMgr     *AgentTaskManager
	hookMgr          *hook.Manager     // hook manager reference, used for re-injecting hooks when plugins are enabled/disabled
	UltraplanManager *UltraplanManager // ultraplan mode manager
	BugHunterManager *BugHunterManager // bug hunter mode manager
	// FileCache is the file content cache for reducing repeated file read tokens
	FileCache *FileContentCache
	// Compressor is the output compressor for reducing bash/tool output tokens
	Compressor *OutputCompressor
	// agentBus is the inter-agent message bus
	agentBus   map[string][]AgentMessage
	agentBusMu sync.Mutex
	// Config is the runtime configuration key-value pairs for tools
	Config   map[string]string
	configMu sync.RWMutex
	// SessionStore is the session storage for exporting session data
	SessionStore SessionStorer
	// OnConfirm is the confirmation callback function
	// Called when Pipeline returns NeedConfirm=true, returns true for user confirmation, false for rejection
	OnConfirm func(toolName, input string) bool
	// OnSandboxEscalate（，2026-10-14）：on-failure 沙箱升级的询问通道。
	//
	// 触发条件（缺一不可，见 ExecuteWithConfirm）：命令**已经**在沙箱内跑过、
	// 失败像 OS 沙箱造成（`Result.SandboxEPERM`）、本调用不是批准重跑（`!call.Approved`）。
	// 用户 Yes ⇒ 本条命令以 sanctioned 通道**出沙箱重跑一次**（逐条、不记忆）；
	// No / 回调 nil ⇒ 原样返回诚实报告（fail-closed：形态 2 调用方不接本回调 = 现状不变）。
	//
	// 语义镜像 OnConfirm；签名带 command 与 reason（EPERM 现场），供确认框指认
	// 是哪条命令被沙箱卡住。HardDeny 结构性到不了这里（执行前拒绝不带 SandboxEPERM）。
	OnSandboxEscalate func(toolName, command, reason string) bool
	// AgentConfirmFn 是子代理工具调用的专用确认通道（G4，2026-09-08 真实冒烟缺口）。
	// 子代理（AgentContext.executeToolCall）遇到 NeedConfirm 时优先调用；
	// nil 时回退 OnConfirm（plain 模式终端 Y/N）。签名携带 ctx：父任务取消时
	// TUI 桥的等待立即退出，避免子代理悬挂到超时。
	//
	// 返回 ConfirmOutcome（R3，2026-09-14）：修复前返回 bool，四种成因
	// （用户拒绝 / ctx 取消 / 等待超时 / 确认槽占用）在下游被塌成同一句"用户拒绝"。
	// 现在由实现方注明 Decision × Source，下游按 Source 生成文案
	// —— **只有 SourceUser 才允许说"用户拒绝"**。
	AgentConfirmFn func(ctx context.Context, toolName, input string) ConfirmOutcome
	// UsageTracker 是子代理 API 用量落账目标（G1，2026-09-08 真实冒烟缺口）。
	// 由 TUI/plain 接线层注入（sessionService.UsageTracker）；nil 时子代理用量
	// 静默降级（仅 CacheStats 累计，/usage 不可见）。
	UsageTracker *ai.UsageTracker
	Pipeline     *ToolPipeline
	SearchCtx    *SearchContext // search context tracking
	// RenderedSystemPrompt is the currently rendered system prompt
	// Modeled after a comparable CLI's rendered system prompt: Fork sub-agent inherits this field for cache sharing
	RenderedSystemPrompt *ai.RenderedPrompt
	mu                   sync.RWMutex

	// loadedDeferredTools 记录会话内已加载的延迟工具名（F3 修复：状态注入路径）。
	// 延迟工具通过 ToolSearch({"load":"工具名"}) 或 Execute 自动加载兜底触发 LoadDeferredTool。
	// buildToolDefs 构造 tools 数组时调用 LoadedDeferredToolNames 合并这些工具的 schema。
	// 作用域：Executor 实例级（会话级），Fork 子代理继承父 Executor 引用故可见。
	loadedDeferredTools sync.Map

	// worktreeManager is the per-instance worktree manager, lazily initialized
	worktreeManager     *WorktreeManager
	worktreeManagerOnce sync.Once

	// auditLog 是沙箱审计日志实例（懒加载），供 ApplyResourceLimits 等场景复用记录审计事件。
	auditLog     *sandbox.AuditLog
	auditLogOnce sync.Once

	// lspClientCache caches LSP clients by language (per-instance)
	lspClientCache map[string]LSPClient
	lspClientMu    sync.Mutex

	// lspConfig 注入的 LSP 配置（T1-P1-A2 /）
	// nil 时 NewClientWithConfig 降级到 DefaultLSPConfig
	// 由 SIGHUP 配置热重载 hook 调用 SetLSPConfig 更新
	lspConfig *lsp.LSPConfig

	// lspVersions 跟踪每个文件的 DidChange version 计数（B4-3A 新增）
	// readBases 是会话级「读基准」登记表（，2026-10-12）：
	// 模型经 read_file 读过的文件 → 盘上内容 sha256。write_file 落盘前自动比对
	// （read_base.go）。惰性初始化，零值 Executor 安全。
	readBases   map[string]string
	readBasesMu sync.Mutex
	// key 为文件绝对路径，value 为上次 DidChange 的 version；首次 DidOpen 占 version=1
	// 每次调用 DidChange 前自增，保证 version 单调递增（LSP 协议要求）
	lspVersions   map[string]int
	lspVersionsMu sync.Mutex

	// AllowLocalhostForTest allows localhost URLs in test mode (SSRF protection bypass)
	AllowLocalhostForTest bool

	// Recovery fields for coordinated retry/degrade/compress in agent sub-calls
	RecoveryCoordinator *ai.RecoveryCoordinator
	RetryConfig         ai.RetryConfig
	Fallback            *ai.FallbackTracker

	// TurnLimiter 是 N16 每轮工具调用限制器（，可选）。
	// nil 时完全不介入执行路径（向后兼容，默认 MaxCallsPerTurn=0 不启用）。
	// 非 nil 时在 Execute 入口检查限制，出口记录调用，每轮 AI 响应后 Reset。
	TurnLimiter *TurnLimiter

	// CapabilityLookup 是 H11N27 capability 注入函数（桶 X3 病灶1）。
	// 由接线层（tui/memory_wiring.go）注入 memory.GetCapability 适配器，
	// 避免 tools→memory 循环依赖（memory 包已依赖 command→github→tools）。
	// nil 时跳过 capability 注入（保持与现有测试兼容）。
	// AgentContext.CapabilityLookup 未设置时 fallback 到此字段，
	// 使子 Agent 自动继承顶层 Executor 的 capability 接线。
	// 返回值：value=capability 内容；trust=信任标记（"verified"或"unverified"等）；
	// err 非 nil 且为 capability 未找到时静默跳过，其他 error 记 warn。
	CapabilityLookup func(key string) (value, trust string, err error)

	// ReflectTrigger 是 H11N27 反思触发函数（桶 X3 病灶1）。
	// 由接线层注入 memory.ReflectMemories 适配器。
	// nil 时跳过反思触发（保持与现有测试兼容）。
	// AgentContext.ReflectTrigger 未设置时 fallback 到此字段。
	// 反思是 best-effort 后台任务，error 由调用方静默降级（仅记 warn）。
	ReflectTrigger func(ctx context.Context, sourceNames []string, scope string) error

	// MemorySearchFunc 是 P4 记忆主动检索的注入函数（对标 同类 CLI memories.search）。
	// 由接线层（tui/memory_wiring.go）注入 memorybridge 适配器（显式 merge 层，
	// 与注入路径 List/Rank 同源），避免 tools→memory 循环依赖。
	// nil 时 memory_search handler 报"not wired"（见 memory_search.go 的降级语义）。
	MemorySearchFunc func(ctx context.Context, query string, topK int) ([]MemorySearchHit, error)
}

// NewExecutor creates a tool execution engine
//
// 向后兼容包装：等价于 NewExecutorWithConfig(registry, workDir, nil)。
// cfg 为 nil 时 buildPluginSignatureVerifier 走环境变量降级路径（桶 X5 行为）。
// 生产代码应优先使用 NewExecutorWithConfig 以接入 config 系统的 plugin 签名配置（R-3）。
func NewExecutor(registry *Registry, workDir string) *Executor {
	exec, err := NewExecutorWithConfig(registry, workDir, nil)
	if err != nil {
		// 防御性 panic：nil cfg 路径下 buildPluginSignatureVerifier 不返回 error
		// （环境变量降级路径无显式 fail-closed），到达此处表示逻辑错误。
		panic(fmt.Sprintf("NewExecutor: unexpected error from NewExecutorWithConfig with nil cfg: %v", err))
	}
	return exec
}

// NewExecutorWithConfig 创建工具执行引擎并注入 config 系统的 plugin 签名配置（R-3）。
//
// 与 NewExecutor 的差异：
// - cfg 非 nil 时，cfg.PluginSignature 传递给 buildPluginSignatureVerifier
// - cfg.PluginSignature.Enabled=true + Required=true + PublicKeyPath 为空（且 env 无 fallback）时
// 返回 error（fail-closed，R-3 核心要求）
// - cfg 为 nil 时退化为桶 X5 行为（环境变量降级）
//
// 参数：
// - registry: 工具注册表
// - workDir: 工作目录
// - cfg: 运行时配置（可为 nil，向后兼容）
//
// 返回：
// - *Executor: 工具执行引擎
// - error: 仅在 plugin 签名配置 fail-closed 时返回（其他初始化失败仍走降级路径）
func NewExecutorWithConfig(registry *Registry, workDir string, cfg *config.Config) (*Executor, error) {
	// 构建 skill 搜索目录（user 全局 → project，后者覆盖前者）
	skillDirs := []skill.SourceDir{}
	// user 全局目录（优先级低，可被 project 覆盖）
	// 收口：Dir() 返回空（home 不可得）⇒ 跳过 user 全局 skill 目录，行为同前。
	if whalehome.Dir() != "" {
		skillDirs = append(skillDirs, skill.SourceDir{
			Path:   filepath.Join(whalehome.Dir(), "skills"),
			Source: "user",
		})
	}
	// project 目录（优先级高，覆盖 user）
	if workDir != "" {
		skillDirs = append(skillDirs, skill.SourceDir{
			Path:   filepath.Join(workDir, ".whale", "skills"),
			Source: "project",
		})
	}

	// Build plugin search directories
	pluginDirs := []string{}
	if workDir != "" {
		pluginDirs = append(pluginDirs, filepath.Join(workDir, ".whale", "plugins"))
	}

	e := &Executor{
		handlers:         make(map[string]Handler),
		registry:         registry,
		MonitorMgr:       monitor.NewManager(),
		SkillMgr:         skill.NewManager(skillDirs...),
		PluginMgr:        plugin.NewManager(pluginDirs...),
		SearchCtx:        NewSearchContext(),
		Sandbox:          sandbox.NewSandbox(workDir),
		workDir:          workDir,
		TaskMgr:          NewTaskManager(),
		CronMgr:          NewCronManager(),
		Config:           make(map[string]string),
		agentBus:         make(map[string][]AgentMessage),
		UltraplanManager: NewUltraplanManager(),
		BugHunterManager: NewBugHunterManager(),
		lspClientCache:   make(map[string]LSPClient),
		lspVersions:      make(map[string]int),
	}

	// R-3：从 config 系统读取 plugin 签名配置（环境变量作为 fallback，向后兼容桶 X5 行为）。
	// cfg 为 nil 时使用零值 PluginSignatureConfig{}（Enabled=false → 走 env 降级路径）。
	// fail-closed：Enabled=true + Required=true + PublicKeyPath 为空（且 env 无 fallback）→ 返回 error。
	var sigCfg config.PluginSignatureConfig
	if cfg != nil {
		sigCfg = cfg.PluginSignature
	}
	verifier, err := buildPluginSignatureVerifier(sigCfg)
	if err != nil {
		return nil, fmt.Errorf("init plugin signature verifier: %w", err)
	}
	e.PluginMgr.SetSignatureVerifier(verifier)

	// 初始化 FileHistory（阶段 5：已替代旧 UndoStack/snapshot）
	// 存储目录 ~/.whale/filehistory/，内部按 workDir hash 隔离。
	// 初始化失败降级为 nil（e.FileHistory 保持零值 nil），调用方需做 nil 守卫。
	// 启动时全量 GC 在 NewFileHistory 内部执行。
	// 收口：Dir() 返回空（home 不可得）⇒ 跳过 filehistory 初始化（保持 nil 降级），行为同前。
	if whalehome.Dir() != "" {
		fhDir := filepath.Join(whalehome.Dir(), "filehistory")
		fh, fhErr := filehistory.NewFileHistory(workDir, fhDir)
		if fhErr != nil {
			log.Printf("[executor] filehistory init failed (degraded to nil): %v", fhErr)
		} else {
			e.FileHistory = fh
			log.Printf("[executor] filehistory initialized at %s", fhDir)
		}
	}

	e.SkillMgr.LoadAll()

	// B1（S19-H-A）：skill 信任门的生产开启点（对齐 hook 域 executor.go:618 的 hookMgr.EnableTrustGate）。
	// R5：本行是该门在生产路径上第一次被启用的地方 —— 两条生产装配入口
	// （tui/lifecycle.go:222、tui/runner_core.go:366，均走 NewExecutorWithConfig）都汇聚到这里。
	// 门本体在 internal/skill（skill.go 的 Execute / ExecuteWithMeta 两个漏斗，单一兜底点），
	// 这里只负责「打开」。开启后 project/plugin 来源的 skill 首次使用前不再注入正文
	// （Execute/ExecuteWithMeta 返回 ErrSkillUntrusted），user/builtin 来源免确认、行为不变。
	// 未开启时 IsInjectionAllowed 恒放行（trust.go:228）⇒ 旧调用方与库默认零感知。
	e.SkillMgr.EnableTrustGate()

	e.PluginMgr.LoadAll()
	e.registerBuiltinHandlers(workDir)
	e.RegisterPluginTools()
	// P0-5/B6: 注入卸载回调，Uninstall 时注销插件工具并重新注入钩子（清理残留）
	e.PluginMgr.SetOnUninstall(func(id string) {
		e.UnregisterPluginTools(id)
		_ = e.ReinjectPluginHooks()
	})

	// N1 subagent / Task tool：初始化 AgentTaskManager（需 Executor 自身引用）。
	// 使用默认并发与 spawn 上限，executor 注入到每个 AgentTask 供 runMultiRound 复用。
	e.AgentTaskMgr = NewAgentTaskManager(defaultMaxConcurrent, defaultMaxSpawn, e)

	// C-P0-1：两阶段初始化 — 将 Executor 引用注入 TaskManager，
	// 使后台任务接入完整沙箱链路（prepareSandbox → Restrict → PostStartHook）。
	// 延迟到此处注入避免 Executor 构造时的循环依赖。
	e.TaskMgr.SetExecutor(e)

	return e, nil
}

// buildPluginSignatureVerifier 根据 config 构造插件签名校验器（R-3：从环境变量迁移到 config 系统）。
//
// 优先级（向后兼容）：
// 1. cfg.Enabled=true → 使用 config.PublicKeyPath（读取文件内容作为 base64 公钥）；
// PublicKeyPath 为空时尝试 env WHALE_PLUGIN_SIGNING_PUBLIC_KEY 过渡兼容；
// Required=true 且公钥仍为空 → 返回 error（fail-closed，不降级）
// 2. cfg.Enabled=false → 使用环境变量 WHALE_PLUGIN_SIGNING_PUBLIC_KEY /
// WHALE_PLUGIN_SIGNING_REQUIRED（桶 X5 行为，向后兼容）
// 3. 都未配置 → NewSignatureVerifier 返回 noopVerifier（允许无签名安装）
//
// 环境变量（桶 X5 兼容，仅在 cfg.Enabled=false 时生效）：
// - WHALE_PLUGIN_SIGNING_PUBLIC_KEY：ed25519 公钥（base64 字符串，非文件路径）
// - WHALE_PLUGIN_SIGNING_REQUIRED：是否强制要求签名（"true"/"1" 为是）
//
// fail-closed 场景（R-3 核心要求）：
// - cfg.Enabled=true + cfg.Required=true + cfg.PublicKeyPath="" + env WHALE_PLUGIN_SIGNING_PUBLIC_KEY="" → 返回 error
// - cfg.PublicKeyPath 非空但文件读取失败 → 返回 error
//
// 返回：
// - plugin.SignatureVerifier: 校验器实例（noopVerifier 或 ed25519Verifier）
// - error: 仅在 cfg.Enabled=true 路径下的 fail-closed 场景返回
func buildPluginSignatureVerifier(cfg config.PluginSignatureConfig) (plugin.SignatureVerifier, error) {
	if cfg.Enabled {
		// R-3：config 启用路径 — 优先从 PublicKeyPath 读取公钥文件
		publicKey := ""
		if cfg.PublicKeyPath != "" {
			data, err := os.ReadFile(cfg.PublicKeyPath)
			if err != nil {
				return nil, fmt.Errorf("signature: read public key file %q: %w", cfg.PublicKeyPath, err)
			}
			publicKey = strings.TrimSpace(string(data))
		}
		// 过渡兼容：config.PublicKeyPath 为空时尝试 env WHALE_PLUGIN_SIGNING_PUBLIC_KEY
		// （旧用户可能已配置 env，迁移到 config 期间保留 fallback）
		if publicKey == "" {
			publicKey = os.Getenv("WHALE_PLUGIN_SIGNING_PUBLIC_KEY")
		}
		// fail-closed：Required=true 但公钥仍为空（config + env 都未提供）→ 返回 error，不降级
		if cfg.Required && publicKey == "" {
			return nil, fmt.Errorf("signature: plugin_signature.enabled=true and required=true but public_key_path empty and WHALE_PLUGIN_SIGNING_PUBLIC_KEY not set (fail-closed)")
		}
		return plugin.NewSignatureVerifier(plugin.SignatureConfig{
			PublicKey: publicKey,
			Required:  cfg.Required,
		}), nil
	}
	// cfg.Enabled=false → 桶 X5 行为：环境变量降级（向后兼容）
	// 此路径不显式 fail-closed（保留 NewSignatureVerifier 内置的 lazy fail-closed：
	// Required=true + PublicKey="" 时返回 ed25519Verifier，Verify 时报错 "public key not configured"）
	publicKey := os.Getenv("WHALE_PLUGIN_SIGNING_PUBLIC_KEY")
	required := false
	if v := os.Getenv("WHALE_PLUGIN_SIGNING_REQUIRED"); v == "true" || v == "1" {
		required = true
	}
	return plugin.NewSignatureVerifier(plugin.SignatureConfig{
		PublicKey: publicKey,
		Required:  required,
	}), nil
}

// toolCallIDCtxKey 是 context.Context 中携带工具调用 ID 的键类型。
// 通过 Execute 入口注入 call.ID，工具处理器（如 writeFileHandler/editFileHandler）
// 可经由 ToolCallIDFromCtx 取出，用于 FileHistory.Track/TrackAfter 关联。
type toolCallIDCtxKey struct{}

// WithToolCallID 返回一个携带工具调用 ID 的 context。
// 在 Execute/ExecuteWithPipeline 入口处调用，确保整个工具执行链路可读到 toolCallId。
func WithToolCallID(ctx context.Context, id string) context.Context {
	return context.WithValue(ctx, toolCallIDCtxKey{}, id)
}

// ToolCallIDFromCtx 从 context 中取出工具调用 ID，未设置时返回空字符串。
// 工具处理器调用 pushFileChange 时用此函数获取 toolCallId 传给 FileHistory.TrackAfter。
func ToolCallIDFromCtx(ctx context.Context) string {
	if v, ok := ctx.Value(toolCallIDCtxKey{}).(string); ok {
		return v
	}
	return ""
}

// SetCurrentMessageId 设置当前消息 ID，供 FileHistory.Track/Snapshot 关联使用。
// 由 TUI/runner 在每轮对话开始前（用户消息创建后、工具执行前）调用。
// 使用写锁保护 currentMsgId 字段的并发访问。
func (e *Executor) SetCurrentMessageId(id string) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.currentMsgId = id
}

// CurrentMessageId 返回当前消息 ID。
// 工具处理器通过此方法读取当前 msgId 传给 FileHistory；
// 对话结束时 TUI 也可读取此值传给 FileHistory.Snapshot。
// 使用读锁保护，与 SetCurrentMessageId 互斥。
func (e *Executor) CurrentMessageId() string {
	e.mu.RLock()
	defer e.mu.RUnlock()
	return e.currentMsgId
}

// WorkDir 返回执行器的工作目录。
// 双写期 web.go 等调用方改用此方法替代旧 SnapStore 的工作目录获取，阶段 5 删除 SnapStore 后统一使用。
func (e *Executor) WorkDir() string {
	return e.workDir
}

// LoadDeferredTool 标记一个延迟工具为已加载（F3 修复：状态注入）。
// 调用后，后续 buildToolDefs 会将该工具的 schema 合并到 tools 数组。
// 幂等：重复加载同一工具名无副作用。
// 触发路径：①ToolSearch({"load":"工具名"}) ②Execute 自动加载兜底。
func (e *Executor) LoadDeferredTool(name string) {
	e.loadedDeferredTools.Store(name, true)
}

// LoadedDeferredToolNames 返回已加载的延迟工具名列表（F3 修复）。
// buildToolDefs 调用此方法合并已加载延迟工具的 schema 到 tools 数组。
// 返回顺序不确定（sync.Map.Range 不保证顺序），调用方无需依赖顺序。
func (e *Executor) LoadedDeferredToolNames() []string {
	var names []string
	e.loadedDeferredTools.Range(func(key, value any) bool {
		names = append(names, key.(string))
		return true
	})
	return names
}

// IsDeferredTool 检查工具是否为延迟工具（用于 Execute 自动加载兜底判断）。
// 通过 Registry.Get 查询 Spec.Exposure 是否为 ExposureDeferred。
func (e *Executor) IsDeferredTool(name string) bool {
	if e.registry == nil {
		return false
	}
	spec, ok := e.registry.Get(name)
	return ok && spec.Exposure == ExposureDeferred
}

// FileHistoryGetter 返回 Executor 持有的 FileHistory 实例。
// 供 TUI/会话层访问以调用 Snapshot/Rewind 等 API。
// 返回 nil 表示 FileHistory 未初始化（降级模式），调用方需做 nil 守卫。
func (e *Executor) FileHistoryGetter() *filehistory.FileHistory {
	e.mu.RLock()
	defer e.mu.RUnlock()
	return e.FileHistory
}

// InitPipeline initializes the tool execution pipeline
func (e *Executor) InitPipeline(hookMgr *hook.Manager, policy *permission.Policy, workDir string) {
	e.Pipeline = NewToolPipeline(e.registry, e, hookMgr, policy, workDir)
	// 管线是**新建**的（enforcement 零值 = 不可强制）⇒ 立刻重放一次
	// 已登记的强制状态。不加这一行的后果：先 SetSandboxMgr 再 InitPipeline 的装配
	// 顺序会把状态丢掉，"沙箱内免问"时灵时不灵（见 reconcileEnforcement）。
	e.reconcileEnforcement()
}

// SetSandboxMgr 注入沙箱管理器（C1/B7）。
// bashHandler 在 SandboxMgr 非 nil 时使用 Prepare + 进程组执行模式。
//
// 沙箱管理器同时是「进程类命令能否免问」这条豁免的**事实来源**
// —— 谁来兜住进程，谁才决定豁免能不能开。⇒ 注入时同步把强制状态推给管线，
// 而不是让审批层自己去猜（那会形成第二套"沙箱是否生效"的判据）。
func (e *Executor) SetSandboxMgr(mgr sandbox.Manager) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.SandboxMgr = mgr
	e.reconcileEnforcement()
}

// SetEnforcementStatus 显式注入「OS 级沙箱是否真的在强制」。
//
// 生产接线不需要调用它 —— 该事实由 SandboxMgr 派生（见 reconcileEnforcement）；
// 本方法供测试替身、以及需要脱离 sandbox.Manager 独立装配的调用方使用。
//
// 传 nil 可清除显式注入（此后回落到按 SandboxMgr 派生）；若 SandboxMgr 也为 nil，
// 则等价于「不可强制」⇒ 豁免出口 (c) 不生效。
func (e *Executor) SetEnforcementStatus(es EnforcementStatus) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.enforcement = es
	e.reconcileEnforcement()
}

// sandboxEnforcement 把 sandbox.Manager 适配成 EnforcementStatus。
//
// 判定逻辑**不在这里**：唯一真相源是 `sandbox.EnforcementActive`（它掌握
// Platform/Features/Degradations 与 degradedSandbox 的真相）。本类型只做接口适配，
// 保证审批层不会出现第二套"沙箱是否生效"的自造判据。
type sandboxEnforcement struct{ mgr sandbox.Manager }

func (s sandboxEnforcement) OSEnforcementActive() bool {
	return sandbox.EnforcementActive(s.mgr)
}

// reconcileEnforcement 把「谁在兜住进程」这一事实推到管线上。
//
// 优先级：显式注入（enforcement 字段）> 由 SandboxMgr 派生 > 不注入（= 不可强制）。
// 每次「管线重建 / 沙箱更换」都重放一次 ⇒ **接线顺序无关**。这是本函数存在的理由：
// 若只在 SetSandboxMgr 里推一次，那么"先注入沙箱、后建管线"的装配顺序会把状态丢掉，
// 结果是豁免功能时灵时不灵（注意方向：丢掉状态 ⇒ enforcementActive=false ⇒ **仍然问**
// ⇒ 是功能回退，不会静默放行；但用户会看到同一台机器上"有时问有时不问"的怪现象）。
//
// 调用方需自行持有 e.mu —— 本函数不加锁，避免与 SetSandboxMgr 形成重入。
func (e *Executor) reconcileEnforcement() {
	if e.Pipeline == nil {
		return
	}
	switch {
	case e.enforcement != nil:
		e.Pipeline.SetEnforcementStatus(e.enforcement)
	case e.SandboxMgr != nil:
		e.Pipeline.SetEnforcementStatus(sandboxEnforcement{mgr: e.SandboxMgr})
	default:
		e.Pipeline.SetEnforcementStatus(nil)
	}
}

// EnableProductionSandbox 一键启用生产沙箱（P1-SEC，非 TUI 入口共用）。
//
// 等价于 TUI lifecycle.go 的三步接线（单一事实源 sandbox.NewProductionSandboxManager）：
// 1. Sandbox Layer 2（命令黑名单/网络拦截/路径检查）启用
// 2. SandboxMgr（OS 级隔离：darwin Seatbelt / linux namespaces+Landlock /
// windows Job Object）注入
// 3. hookMgr 同步同一 Manager（HookCommand 走完整沙箱链路）
//
// 修复前：SetSandboxMgr 仅在 TUI lifecycle.go 调用；plain/bridge
// （runner_core.go）路径 SandboxMgr 为 nil → 非 TUI 模式（whale -p）
// 完全没有 OS 级沙箱，deny 列表命令直接执行。
//
// 前置条件：InitPipelineWithHooks 已调用（hookMgr 就绪）；
// 未调用时第 3 步为无操作（SyncHookSandboxManager 内部 nil 守卫）。
func (e *Executor) EnableProductionSandbox(workDir string) {
	if e.Sandbox != nil {
		e.Sandbox.SetEnabled(true)
	}
	sbMgr := sandbox.NewProductionSandboxManager(workDir)
	e.SetSandboxMgr(sbMgr)
	e.SyncHookSandboxManager(sandbox.SandboxLevelRelaxed)
	// 门 2 降级可见性：plain/bridge 启动时向用户显示实际隔离等级与降级项
	//（此前仅审计日志，TUI/CLI 不可见）。
	// 门 2 修复（真机验证）：走 sandbox.PrintIsolationBanner fd 2 直写——
	// os.Stderr 变量已被 main.go 重定向到日志文件，走变量 banner 会被吞
	//（终端永远看不到，躺在 ~/.whale/logs/whale.log）。
	sandbox.PrintIsolationBanner(sbMgr)

	// S7 · PR-A2（①/③ 拍板）：hook 信任门**按需**提示。
	// hook 未信任/被改 → 一律不运行；此处只在确有此类 hook 时提示一次，
	// 并给出一次性信任命令（**不自动执行** —— 不替用户做信任决策）。
	e.PrintHookTrustNoticeIfAny()
}

// HookTrustNotice 返回 hook 信任门的启动提示（无待审 hook 时为空串）。
//
// 空串语义很重要：调用方据此「无变更不打扰」（S7 · PR-A2 拍板 ③）。
func (e *Executor) HookTrustNotice() string {
	if e.hookMgr == nil {
		return ""
	}
	return e.hookMgr.RunTrustSummary()
}

// PrintHookTrustNoticeIfAny 把 hook 信任提示打到真实终端（fd 2），无待审 hook 时静默。
//
// 单一入口：TUI lifecycle 与 EnableProductionSandbox（plain/bridge）共用，
// 避免两处各写一份判空逻辑——两条路径的提示口径必须一致。
func (e *Executor) PrintHookTrustNoticeIfAny() {
	sandbox.PrintStartupNotice(e.HookTrustNotice())
}

// SyncHookSandboxManager 把 Executor.SandboxMgr 同步到 hookMgr（桶 X3 病灶2 接入点）。
//
// 修复前：hook.SetSandboxManager 接口已存在但生产路径无调用方，
// 导致用户在 hooks.json 中注册的 HookCommand（type=command）跳过沙箱执行。
//
// 修复后：调用方（main.go 接线层）在 SetSandboxMgr 之后调用此方法，
// 把同一个 sandboxMgr 注入到 hookMgr，使 HookCommand 走完整沙箱链路：
//
//	Prepare → Restrict → StartWithProcessGroup → PostStartHook
//
// 参数：
// - defaultLevel: hook 默认沙箱级别（与 Executor.SandboxMgr.Level() 可不同；
// hook 通常用 Relaxed 即可，避免过度限制）
//
// 等价于 e.hookMgr.SetSandboxManager(e.SandboxMgr, defaultLevel)；
// 提供 wrapper 是为了集中沙箱注入入口，避免接线层散落多处 SetSandboxManager 调用。
//
// 安全策略：hookMgr==nil 或 SandboxMgr==nil 时无操作（向后兼容，保留 hook 原 fail-open 行为）。
func (e *Executor) SyncHookSandboxManager(defaultLevel sandbox.SandboxLevel) {
	e.mu.RLock()
	mgr := e.SandboxMgr
	hookMgr := e.hookMgr
	e.mu.RUnlock()
	if mgr == nil || hookMgr == nil {
		return
	}
	hookMgr.SetSandboxManager(mgr, defaultLevel)
}

// RunSessionStartHooks 触发会话开始 hook（桶 X3 病灶2 接入点）。
// 转发到 hookMgr.RunSessionStartHooks；hookMgr==nil 时无操作。
// 应在 pipeline 初始化完成、会话正式开始时调用一次。
func (e *Executor) RunSessionStartHooks() {
	e.mu.RLock()
	hookMgr := e.hookMgr
	e.mu.RUnlock()
	if hookMgr == nil {
		return
	}
	hookMgr.RunSessionStartHooks()
}

// RunSessionStopHooks 触发会话结束 hook（桶 X3 病灶2 接入点）。
// 转发到 hookMgr.RunSessionStopHooks；hookMgr==nil 时无操作。
// 应在会话主动结束（用户退出、超时）时调用。
func (e *Executor) RunSessionStopHooks() {
	e.mu.RLock()
	hookMgr := e.hookMgr
	e.mu.RUnlock()
	if hookMgr == nil {
		return
	}
	hookMgr.RunSessionStopHooks()
}

// RunStopHooks 触发停止 hook（桶 X3 病灶2 接入点）。
// 转发到 hookMgr.RunStopHooks；hookMgr==nil 时无操作。
// 应在 Executor 关闭（进程退出）时调用。
func (e *Executor) RunStopHooks() {
	e.mu.RLock()
	hookMgr := e.hookMgr
	e.mu.RUnlock()
	if hookMgr == nil {
		return
	}
	hookMgr.RunStopHooks()
}

// EnableAllPlugins 启用所有已加载插件并注册其工具与钩子（P0-7/B7）。
// 由 main.go --enable-plugins flag 触发：默认禁用时插件加载到内存但 Enabled=false，
// 工具未注册；调用此方法后启用所有插件 + 注册工具 + 重新注入钩子。
func (e *Executor) EnableAllPlugins() {
	if e.PluginMgr == nil {
		return
	}
	e.PluginMgr.EnableAll()
	e.RegisterPluginTools()
	_ = e.ReinjectPluginHooks()
}

// InitPipelineWithHooks initializes the tool execution pipeline and auto-loads hook configuration
// Loads hooks.json config from working directory and user home directory, silently skipping missing config files
//
// 桶 X3 病灶2 修复：在 pipeline 初始化末尾自动触发 RunSessionStartHooks。
// 修复前：SessionStart/SessionStop/Stop 等生命周期 hook 在生产路径零调用，
// 用户在 hooks.json 中注册的 SessionStart hook 永远不会执行。
// 修复后：pipeline 初始化完成即视为会话开始，自动触发 SessionStart hook（一次会话一次）。
// SessionStop/Stop 由调用方（tui/runner/main.go）在合适时机显式调用 Executor.RunSessionStopHooks/RunStopHooks。
func (e *Executor) InitPipelineWithHooks(policy *permission.Policy, workDir string) {
	hookMgr := hook.NewManager(workDir)

	// Try loading hook configuration from working directory
	workHooksPath := filepath.Join(workDir, ".whale", "hooks.json")
	if _, err := os.Stat(workHooksPath); err == nil {
		_ = hookMgr.LoadConfig(workHooksPath)
	}

	// Try loading hook configuration from user home directory
	// 收口：Dir() 返回空（home 不可得）⇒ 跳过 user hooks，行为同前。
	if whalehome.Dir() != "" {
		homeHooksPath := filepath.Join(whalehome.Dir(), "hooks.json")
		if _, err := os.Stat(homeHooksPath); err == nil {
			_ = hookMgr.LoadConfig(homeHooksPath)
		}
	}

	// PR-A 信任门（S7 · Q1 拍板：对齐业内既有实现 startup_hooks_review，不信任则不运行）。
	// R5：本行是该门在生产路径上第一次被启用的地方 —— 三条生产入口
	// （tui/lifecycle.go:425、tui/runner_core.go:532、tui/bridge.go:274）都汇聚到本函数。
	// 注意：hook 是外部代码（hooks.json / 插件），未信任一律静默跳过；
	// 信任库落在 ~/.whale/hooks.trust.json，内容变更自动失效需重新信任。
	hookMgr.EnableTrustGate()

	// Save hook manager reference for ReinjectPluginHooks
	e.hookMgr = hookMgr

	// Inject plugin hooks into hook manager
	e.injectPluginHooks(hookMgr)

	e.InitPipeline(hookMgr, policy, workDir)

	// 桶 X3 病灶2：pipeline 初始化完成后触发 SessionStart hook（一次会话一次）
	// hookMgr 中若无 SessionStart hook，RunSessionStartHooks 返回 Allow（无操作）
	hookMgr.RunSessionStartHooks()
}

// Execute executes a tool call (preserves original behavior).
// B6-P1-1: 接线 RecordToolExecution，使用命名返回 + defer 在所有返回路径统一记录指标。
//
// 工具执行留痕（C9 · S13-G3）：函数入口记录 start，defer 在所有返回路径统一记录
// 工具类型 + 执行结果 + 耗时（含 TurnLimiter/ValidateInput/handler 调用）。
// 级别 = **Info**（可审计级别）：默认级别即 Info（internal/log/log.go:19 `level = slog.LevelInfo`），
// 原先用 Debug ⇒ 默认级别下**零留痕**，I12（可复现性）不成立。
// 工具调用每轮对话仅几次到几十次，不属于高频路径，升到 Info 不会显著影响日志体量。
// 决策侧（allow/deny/ask）的留痕走 delta F 通道（~/.whale/audit/tool-approval.jsonl），
// 本行只补「执行事件」在 whale.log 里的可还原性，**不**另起第二套审批留痕（族C-1）。
func (e *Executor) executeDirect(ctx context.Context, call Call) (result Result) {
	// 工具执行留痕：最先注册的 defer，最后执行，记录完整耗时。
	toolStart := time.Now()
	defer func() {
		// 补 `call_id`，与决策面留痕
		// （`pipeline.go` 的 `[audit] tool: rejected` / `: confirm required`）
		// **共用同一关联键**。修复前只有工具名与耗时 ⇒ 同批多条同名调用
		// 无法把某条"执行"对齐回它对应的那条"决策"（问/放/拒）。
		//
		// 取 `call.ID` 而不是 `result.ID`：ID 必须是**入参**的标识（决策侧
		// 用的也是入参 ID）；handler 允许不填 result.ID（零值），用它会让
		// 恰好"执行成功但忘了回填 ID"的调用丢失关联键 —— 那正是本缺陷的形态。
		whalelog.Info("[audit] tool: execute done",
			"phase", "tool_execute",
			"call_id", call.ID,
			"tool", result.Name,
			"is_error", result.IsError,
			"elapsed_ms", time.Since(toolStart).Milliseconds(),
		)
	}()
	defer func() {
		if result.IsError {
			metrics.Default.RecordToolExecution(fmt.Errorf("tool %s failed", result.Name))
		} else {
			metrics.Default.RecordToolExecution(nil)
		}
	}()

	call = normalizeToolCallInput(call)
	name := NormalizeToolName(call.Name)

	// N16: 每轮工具调用限制检查（，TurnLimiter 为 nil 时跳过）
	if e.TurnLimiter != nil {
		if err := e.TurnLimiter.CheckLimit(name); err != nil {
			return Result{
				ID:      call.ID,
				Name:    name,
				Output:  err.Error(),
				IsError: true,
			}
		}
		defer e.TurnLimiter.Record(name)
	}

	// Empty input is auto-filled as empty JSON object for compatibility when model does not pass parameters
	if strings.TrimSpace(call.Input) == "" {
		call.Input = "{}"
	}

	if err := ValidateInput(name, call.Input); err != nil {
		return Result{
			ID:      call.ID,
			Name:    name,
			Output:  err.Error(),
			IsError: true,
		}
	}

	e.mu.RLock()
	handler, ok := e.handlers[name]
	e.mu.RUnlock()

	if !ok {
		return Result{
			ID:      call.ID,
			Name:    name,
			Output:  fmt.Sprintf("unknown tool: %s", name),
			IsError: true,
		}
	}

	// F4 修复：自动加载兜底——若调用的工具是延迟工具但尚未加载，自动标记为已加载。
	// 覆盖场景：
	// ① 模型并行调用 ToolSearch + 目标工具（同一轮响应），目标工具 schema 尚未注入 tools 数组
	// ② API 透传非列表工具调用（部分 OpenAI 兼容端点允许模型调用 tools 数组外的工具）
	// ③ 旧会话恢复后模型调用已习惯的延迟工具
	// 安全性：handler 已确认存在（上面 ok==true），仅标记加载状态供下次 buildToolDefs 合并。
	// 本次执行不受影响（handler 正常执行），下次 buildRequest 会包含该工具 schema。
	if e.IsDeferredTool(name) {
		e.LoadDeferredTool(name)
	}

	// 注入 call.ID 到 ctx，供 writeFileHandler/editFileHandler 等工具处理器
	// 通过 ToolCallIDFromCtx 取出 toolCallId 传给 FileHistory.TrackAfter（双写）。
	ctx = WithToolCallID(ctx, call.ID)

	output, err := handler(ctx, json.RawMessage(call.Input))
	if err != nil {
		// S11 G1-B（软拒绝）：保留模型的非-error 契约（IsError=false），
		// 仅打结构化 FailureKind 标记，供编排/TUI 识别为终局拒绝。
		// 与下方 errSandboxDenied（bash 硬拒绝，IsError=true）语义刻意不同，
		// 统一（选项 A）另立轮次。
		if errors.Is(err, errSandboxDeniedSoft) {
			return Result{
				ID:          call.ID,
				Name:        name,
				Output:      output,
				IsError:     false,
				FailureKind: FailureKindSandboxDenied,
			}
		}
		if errors.Is(err, errSandboxDenied) {
			return Result{
				ID:          call.ID,
				Name:        name,
				Output:      output,
				IsError:     true,
				FailureKind: FailureKindSandboxDenied,
			}
		}
		// 命令**已经跑过**、失败像 OS 沙箱造成（EPERM）⇒
		// 结构化上报 SandboxEPERM，供 ExecuteWithConfirm 的升级询问消费。
		// 与上方 errSandboxDenied（执行前拒绝，命令一行没跑）刻意分哨兵：
		// 二者的用户语义不同（「没让跑」vs「跑了但被牢房卡住」），升级资格也不同。
		if errors.Is(err, errSandboxExecutionDenied) {
			return Result{
				ID:           call.ID,
				Name:         name,
				Output:       output,
				IsError:      true,
				FailureKind:  FailureKindSandboxDenied,
				SandboxEPERM: true,
			}
		}
		// ★ (c)：措辞**按结构化种类分层**，唯一声明 = `ToolErrorOutput`。
		//
		// 修前这里对**任意** handler error 一律拼 `tool execution failed: %v` ⇒
		// 命令非零退出（`go test` 断言失败 exit 1）与"工具真坏了"被合成同一句话：
		// **数据被当成故障**，模型因此以为用错了工具而放弃这条路（R4 的违反）。
		// 现在：带结构化标记的**普通失败**（command_exit / patch_conflict）走数据口吻，
		// `tool execution failed:` 只留给**未知错误 / 真故障**。
		kind := FailureKindOf(err)
		errMsg := ToolErrorOutput(name, kind, err)
		if output != "" {
			errMsg = fmt.Sprintf("%s\nOutput: %s", errMsg, output)
		}
		// C2 透传（R3/L3.2）：handler 边界只能传 (string, error)，故结构化种类
		// 由 error 携带；此处提取并落到 Result.FailureKind，供编排 / TUI 识别终局拒绝。
		// 与上方 errSandboxDenied 同款机制，只是把"种类"泛化到任意 FailureKind。
		// 零值时保持零值 ⇒ 普通失败不会被误标为终局拒绝（IK2）。
		return Result{
			ID:          call.ID,
			Name:        name,
			Output:      errMsg,
			IsError:     true,
			FailureKind: kind,
		}
	}

	// 路线 A已随「散文幻觉检测器」整体移除（，2026-10-08
	// 用户拍板）：文件效果的生产者通道（Result.FileEffects / deriveFileEffects）
	// 唯一消费者是检测器，随之删除。轮级真实变更展示若将来落地，
	// 应走盘面快照差分（filehistory），不从命令文本派生。
	return Result{
		ID:     call.ID,
		Name:   name,
		Output: output,
	}
}

// Execute executes a tool call through the unified permission decision point.
//
// F1 (I1 单一强制点 / Q2 统一走权限检查)：当 Pipeline 已初始化（生产路径）时，
// 强制经 ToolPipeline 决策（含 PermissionStage 权限检查），消除「直连 Execute 绕过权限」双入口。
// Pipeline == nil 时回退到 executeDirect（非生产 / 未接线路径，文档标注）。
//
// 生产入口对齐（R1 接线断言 §1.2）：
// - tui/service_tool.go:24 ExecuteTool
// - tui/runner_core.go:2553 ExecuteTool（N23 本地分支）
// - executor.go:796/821 ExecuteBatch（safe / unsafe）
// - executor.go:715/733 ExecuteWithPipeline/Confirm 的 Pipeline==nil 回退
//
// 这些入口现在都收口到本方法，Pipeline 已初始化时统一过权限。
func (e *Executor) Execute(ctx context.Context, call Call) Result {
	// F1：单一强制点 —— 管线已初始化时，走完整八阶段（含 PermissionStage）。
	if e.Pipeline != nil {
		pr := e.Pipeline.Execute(ctx, call)
		if pr.Result != nil {
			return *pr.Result
		}
		// 管线拒绝（Proceed=false）或未产生结果
		name := NormalizeToolName(call.Name)
		if !pr.Proceed {
			out := pr.Rejection
			if out == "" {
				out = "tool execution rejected by pipeline"
			}
			return Result{
				ID:          call.ID,
				Name:        name,
				Output:      out,
				IsError:     true,
				FailureKind: pr.FailureKind,
			}
		}
		return Result{
			ID:      call.ID,
			Name:    name,
			Output:  "pipeline proceeded without a result (fail-closed)",
			IsError: true,
		}
	}
	// Pipeline 未初始化：非生产回退，直连 handler（保持既有单测/降级行为）。
	return e.executeDirect(ctx, call)
}

// ExecuteWithPipeline executes a tool call through the six-stage pipeline
func (e *Executor) ExecuteWithPipeline(ctx context.Context, call Call) *PipelineResult {
	if e.Pipeline == nil {
		result := e.Execute(ctx, call)
		return &PipelineResult{
			Proceed: true,
			Result:  &result,
			StageLogs: []StageLog{
				{Stage: "execute", Status: "pass", Message: "pipeline not initialized, executing directly"},
			},
		}
	}

	return e.Pipeline.Execute(ctx, call)
}

// ExecuteWithConfirm 执行带确认流程的工具调用
// 当 Pipeline 返回 NeedConfirm=true 时，调用 OnConfirm 回调等待用户决策
// 用户确认则继续执行工具；用户拒绝则返回包含"拒绝"的拒绝结果
//
// 契约（2026-09-16 · 移交单 的 B 项**被打回**后补写，
// 目的是让这条语义有字面依据 + 有守卫测试，见
// internal/tools/d002_needconfirm_invariant_test.go 的 TestD002_NilConfirmChannel_Contract）：
//
// **`OnConfirm == nil` 分支的语义是「透传 NeedConfirm 给调用方」，不是「静默忽略」。**
// 两个消费方形态不同、都必须被满足：
//
// 1. **有回调的调用方**（`tui/plain.go:1976` 终端 Y/N、`tui/runner_core.go:1433`
// 由 `executeToolsAndContinue` 注入的 onConfirm）⇒ 本函数内联问用户，
// 批准则 `call.Approved = true` 重跑整条管线，拒绝则返回拒绝结果。
// 2. **无回调、但自己实现暂停 UI 的调用方**（`tui/tool_exec.go:296` 与 `:174`
// 的 `if pr.NeedConfirm → pipelineConfirmMsg`，即 bubbletea 的 pendingConfirm
// 弹窗；`tui/bridge.go:938` 传 `nil` onConfirm 后同样落到这条路径）
// ⇒ 它们**故意不接** `OnConfirm`，而是把「暂停 + 等用户决策 + 以
// `Approved:true` 重跑」放在 TUI 层实现，因此**必须**拿到
// `NeedConfirm == true` 才能弹窗。
//
// ⇒ 因此**不得**在 `OnConfirm == nil` 时把 `NeedConfirm` 改成 `false` 或
// 直接返回终局拒绝：那会让形态 2 的确认弹窗**彻底失效**，所有需确认的调用
// 被静默拒绝（以 B 方案实测：`TestExecuteWithConfirm_NoOnConfirm` 与
// `TestD002_NilConfirmChannel_Contract` 同时变红）。
//
// 安全轴不受影响：无论哪条分支，`OnConfirm == nil` 时都是
// `Proceed == false` ⇒ **调用方不会执行工具**；且由
// `ToolPipeline.Execute` 的 不变量守卫，`NeedConfirm == true` 恒伴随
// `Proceed == false`（见 pipeline.go 成功出口）⇒ 不存在「未确认却放行」的组合。
func (e *Executor) ExecuteWithConfirm(ctx context.Context, call Call) *PipelineResult {
	if e.Pipeline == nil {
		result := e.Execute(ctx, call)
		return &PipelineResult{
			Proceed: true,
			Result:  &result,
			StageLogs: []StageLog{
				{Stage: "execute", Status: "pass", Message: "pipeline not initialized, executing directly"},
			},
		}
	}

	result := e.Pipeline.Execute(ctx, call)

	// 票面落点：`e.OnConfirm == nil` 时**不进入**本分支，`NeedConfirm`
	// 由 :876 `return result` 原样透传给调用方 —— 这是**有意的**，TUI 的
	// pendingConfirm 弹窗依赖它（见本函数头部 契约注释）。
	// ⚠️ 不要在此处加「nil 通道 ⇒ 终局拒绝」：那会让 TUI 确认弹窗失效。
	if result.NeedConfirm && e.OnConfirm != nil {
		confirmed := e.OnConfirm(call.Name, call.Input)
		if !confirmed {
			return &PipelineResult{
				Proceed: false,
				// ★ ：双语 SSOT 形态（util.Bi）—— 修前是
				// "user denied tool execution / 用户拒绝工具执行" 行内并列。本串会被
				// tool_exec.go 的 pipelineRejectionResult 拼进用户可见输出（B2 会话的形态门隐患）。
				Rejection:   util.Bi("user denied tool execution", "用户拒绝工具执行"),
				NeedConfirm: false,
				FailureKind: FailureKindPolicyDenied,
				// **只有这一条分支**可以说"用户拒绝"—— 用户确实在
				// `OnConfirm` 的确认框上按了 N。调用方（runnerCore / plain）据此
				// 选文案；不得由调用方从 Rejection 文本反推。
				ConfirmSource: SourceUser,
				StageLogs:     result.StageLogs,
			}
		}
		// Re-run the complete pipeline with an internal approval marker. This
		// avoids bypassing validation and lets the already-approved safety stages
		// proceed without asking the same question again.
		call.Approved = true
		// ★ ：批准**下传执行链**的产生点已收口到
		// `ExecuteStage`（pipeline.go，三入口必经咽喉）——本处原先用
		// sanctionedContext 包一层 ctx，但那只覆盖 plain 路径的这次重跑；
		// TUI 面板批准（tool_exec.go:201）与子代理批准恢复（agent.go:1023）
		// 是以 `Approved=true` 重入 Pipeline.Execute 的**另一形态**，
		// 永远到不了这里 ⇒ 真机上"批了仍拒"（取证回执 2026-10-08）。
		// 收口后两处注入 = 一个产生点，不再是双实现（原注释迁往 sandbox_sanction.go）。
		execResult := e.Pipeline.Execute(ctx, call)
		return execResult
	}

	// on-failure 沙箱升级：命令**已经**在沙箱内跑过、失败像
	// OS 沙箱造成（EPERM）⇒ 问用户一次，Yes 则这一条**出沙箱重跑**。
	//
	// 设计拍板：内部工作回执
	// （对标同类 CLI：沙箱内被拒 → 重新 request_approval → 批准后 SandboxType::None 重跑）。
	//
	// 四条边界，缺一不可：
	// ① `Result.SandboxEPERM` —— 只有「已运行 + EPERM」的失败才升级；执行**前**的
	// 沙箱拒绝（errSandboxDenied）不带本标记 ⇒ HardDeny 结构性到不了这里（-R1）；
	// ② `!call.Approved` —— 批准重跑不得再升级 ⇒ 一次性、无循环；
	// ③ 回调 nil ⇒ 不升级（fail-closed：形态 2 调用方——bubbletea 面板——不接本回调
	// 时行为与改前逐字一致，只拿诚实报告）；
	// ④ 用户 No ⇒ 原样返回（不重试、不静默放行）。
	// 批准后重跑 = `Approved=true` + `SandboxEscalated=true` 走**整条管线**
	// （与 NeedConfirm 批准重跑同构，不旁路任何阶段）；出沙箱由
	// `sanctionedContext` 的 SandboxEscalated 分支 → sanctioned 通道完成，
	// HardDeny / validator Critical 的纵深防御全部继承。
	if result.Result != nil && result.Result.SandboxEPERM && !call.Approved && e.OnSandboxEscalate != nil {
		command := pipelineExtractField(call.Input, "command")
		if e.OnSandboxEscalate(call.Name, command, result.Result.Output) {
			call.Approved = true
			call.SandboxEscalated = true
			return e.Pipeline.Execute(ctx, call)
		}
	}

	return result
}

// maxConcurrentSafeTools is the maximum number of concurrent safe tool executions
const maxConcurrentSafeTools = 8

// ExecuteBatch executes multiple tool calls in batch
// Safe tools are executed concurrently, unsafe tools are executed sequentially.
// When ctx is cancelled, returns immediately with any results collected so far.
func (e *Executor) ExecuteBatch(ctx context.Context, calls []Call) []Result {
	results := make([]Result, len(calls))

	safeCalls := make(map[int]Call)
	unsafeCalls := make(map[int]Call)

	for i, call := range calls {
		name := NormalizeToolName(call.Name)
		if e.IsConcurrencySafe(name) {
			safeCalls[i] = call
		} else {
			unsafeCalls[i] = call
		}
	}

	sem := make(chan struct{}, maxConcurrentSafeTools)
	var wg sync.WaitGroup
	for i, call := range safeCalls {
		wg.Add(1)
		go func(idx int, c Call) {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()
			results[idx] = e.Execute(ctx, c)
		}(i, call)
	}

	done := make(chan struct{})
	go func() {
		wg.Wait()
		close(done)
	}()

	select {
	case <-done:
	case <-ctx.Done():
	}

	for i, call := range unsafeCalls {
		select {
		case <-ctx.Done():
			results[i] = Result{
				ID:      call.ID,
				Name:    NormalizeToolName(call.Name),
				Output:  "tool execution cancelled",
				IsError: true,
			}
		default:
			results[i] = e.Execute(ctx, call)
		}
	}

	return results
}

// RegisterHandler registers a custom tool handler function
func (e *Executor) RegisterHandler(name string, handler Handler) error {
	e.mu.Lock()
	defer e.mu.Unlock()
	if _, exists := e.handlers[name]; exists {
		return fmt.Errorf("tool %q handler already exists", name)
	}
	e.handlers[name] = handler
	return nil
}

// registerBuiltinHandlers registers builtin tool handler functions
func (e *Executor) registerBuiltinHandlers(workDir string) {
	e.handlers["bash"] = e.bashHandler(workDir)
	e.handlers["read_file"] = e.readFileHandler()
	e.handlers["write_file"] = e.writeFileHandler()
	e.handlers["edit_file"] = e.editFileHandler()
	// G5：unified diff 应用（写类工具，与 write_file/edit_file 同一条沙箱/原子写纪律）。
	e.handlers[applyUnifiedDiffToolName] = e.applyUnifiedDiffHandler(workDir)
	e.handlers["glob_search"] = e.globHandler(workDir)
	e.handlers["grep_search"] = e.grepHandler(workDir)
	// P4：模型主动检索记忆（对标 同类 CLI memories.search；只读，注入函数缺失时软失败）。
	e.handlers[memorySearchToolName] = e.memorySearchHandler()
	e.handlers["WebFetch"] = e.webFetchHandler()
	// ★ S3（裁定 C=②）：执行门也走**同一谓词** toolAvailable。
	//
	// 【实测修正设计文档】§3.2 的执行门行称"registerBuiltinTools() 不建 spec ⇒ handler 亦不可达"。
	// 该说法精度不够，实测分两条路：
	// - **生产管线**（Pipeline != nil，TUI 主路径）：在 schema_validate 阶段按**注册表**拦
	// （pipeline.go:452 `tool %q not registered (fail-closed)`）⇒ 它自动跟随注册点，无需额外接线；
	// - **非管线路径**（Pipeline == nil ⇒ executeDirect，executor.go:782 记录的回退路径）：
	// 只查 e.handlers，而 handler 由本函数**独立于 registry** 注册 ⇒ 不补闸门的话，
	// 它会退化成"缺少配置"的软提示（而不是"不可用"），两条路对同一事实给出不同答案。
	// 故在此补闸门（belt-and-braces），把非管线路径也对齐到"不可用"语义。
	// 别名 `search` 经 NormalizeToolName 解析到同一名字，因此天然落到同一出口（B1 连带项）。
	if toolAvailable("WebSearch") {
		e.handlers["WebSearch"] = e.webSearchHandler()
	}
	e.handlers["Agent"] = e.agentHandler()
	// ★ ：fork 子 agent 的**结论读回**入口（拉）。
	//
	// 为什么必须注册（执行门）：Agent(agent_type=fork) 是 fire-and-forget，回执只给
	// task_id；而 `ForkManager.GetTask` 在修复前**生产零调用点** ⇒ 子 agent 的结论
	// 既不落盘也没人消费。原「完成通知注入对话流」方案已撤回（撞 tui/app.go:3528-3552
	// 的 AuthorAsync 硬规则），故唯一自洽解是给模型一个拉取入口。
	e.handlers["ForkStatus"] = e.forkStatusHandler()
	e.handlers["TodoWrite"] = e.todoWriteHandler()
	e.handlers["AskUser"] = e.askUserHandler()
	e.handlers["Skill"] = e.skillHandler()
	// ★ /（W5）：LSP/MCP 元工具的执行门也走**同一谓词** toolAvailable。
	//
	// 与上方 WebSearch 闸门同理由（belt-and-braces）：生产管线路径在 schema_validate 阶段
	// 按注册表拦（自动跟随注册点），但**非管线路径**（Pipeline == nil ⇒ executeDirect）只查
	// e.handlers，若这里无条件注册，零 server 时会退化成"LSP/MCP not available"的**软失败**
	// （error == nil 被记成成功）—— 正是 / 要消灭的"对不存在的能力做承诺"。
	// 补闸门后，非管线路径也落到"不可用"语义，与注册点/档位目录三面一致。
	//
	// 判据与注册点**逐名读同一个谓词**（不在这里再造"零 server 与否"的第二份判断）：
	// 名单唯一权威 = lspToolNames / mcpMetaToolNames，handler 工厂表随名单一一对应
	// （名单漂移守卫在 mcp_lsp_availability_test.go，新增工具忘注册 handler 会编译/测试红）。
	for name, factory := range lspHandlerFactories(e) {
		if toolAvailable(name) {
			e.handlers[name] = factory()
		}
	}
	for name, factory := range mcpHandlerFactories(e) {
		if toolAvailable(name) {
			e.handlers[name] = factory()
		}
	}
	e.handlers["NotebookEdit"] = e.notebookEditHandler()
	e.handlers["Monitor"] = e.monitorHandler(workDir)
	// ★ ：Monitor 输出的**模型向读回**入口（拉）。
	//
	// 内容源早已存在且是权威副本（`monitor.Process.GetOutput()`，上限 DefaultMaxLines=10000），
	// 缺的只是模型侧入口 —— 修复前后台回执只给人类斜杠命令 `/monitors`（模型走不通）。
	// 与既有人向入口 `/monitors tail <id>`（tui/format.go:697-720）**同构**。
	e.handlers["MonitorTail"] = e.monitorTailHandler()
	e.handlers["git_diff"] = e.gitDiffHandler(workDir)
	e.handlers["git_status"] = e.gitStatusHandler(workDir)
	e.handlers["git_log"] = e.gitLogHandler(workDir)
	e.handlers["git_add"] = e.gitAddHandler(workDir)
	e.handlers["git_commit"] = e.gitCommitHandler(workDir)
	e.handlers["git_branch"] = e.gitBranchHandler(workDir)
	e.handlers["schedule_cron"] = e.scheduleCronHandler()
	e.handlers["user_choice"] = e.userChoiceHandler()
	e.handlers["config_get"] = e.configGetHandler()
	e.handlers["config_set"] = e.configSetHandler()
	e.handlers["export_session"] = e.exportSessionHandler()
	e.handlers["structured_output"] = e.structuredOutputHandler()
	e.handlers["enter_worktree"] = e.enterWorktreeHandler(workDir)
	e.handlers["exit_worktree"] = e.exitWorktreeHandler(workDir)
	// 12 additional tools
	e.handlers["ToolSearch"] = e.toolSearchHandler()
	e.handlers["SendMessage"] = e.sendMessageHandler()
	e.handlers["TaskCreate"] = e.taskCreateHandler()
	e.handlers["TaskList"] = e.taskListHandler()
	e.handlers["TaskStop"] = e.taskStopHandler()
	// ★ ：后台任务**输出读回**入口（拉），与 TaskCreate/List/Stop 同族。
	//
	// 权限面论据（方案对照 §0′ 第 2 条要求显式写出，不得默认）：任务在 Create 时已过
	// 权限审批（TaskCreate 声明 LevelWorkspaceWrite），读其**已采集的**输出不新增越权面
	// —— 不是"绕过审批"，而是"不复述一遍已作出的裁决"（同类 CLI write_stdin 同构）。
	// 故 spec 声明 LevelReadOnly（tools.go 同族条目）。
	e.handlers["TaskOutput"] = e.taskOutputHandler()
	e.handlers["CronList"] = e.cronListHandler()
	e.handlers["CronDelete"] = e.cronDeleteHandler()
	e.handlers["Sleep"] = e.sleepHandler()
	// N1 subagent / Task tool：注册 Task handler（委托给 AgentTaskMgr + TaskTool）。
	e.handlers["Task"] = e.taskHandler()
}

// parseInput parse tool input JSON
func parseInput(raw json.RawMessage, target interface{}) error {
	if len(raw) == 0 {
		return fmt.Errorf("input is empty")
	}
	return json.Unmarshal(raw, target)
}

// UnregisterPluginTools removes tools of the specified plugin from Registry and Handler
func (e *Executor) UnregisterPluginTools(pluginID string) {
	if e.PluginMgr == nil {
		return
	}
	p, ok := e.PluginMgr.Get(pluginID)
	if !ok {
		return
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	// Remove plugin tools from Registry (thread-safe)
	for _, td := range p.Tools {
		e.registry.UnregisterPlugin(td.Name)
		delete(e.handlers, td.Name)
	}
}

// UnregisterAllPluginTools removes all plugin tools from Registry and Handler
func (e *Executor) UnregisterAllPluginTools() {
	if e.PluginMgr == nil {
		return
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	plugins := e.PluginMgr.List()
	for _, p := range plugins {
		for _, td := range p.Tools {
			e.registry.UnregisterPlugin(td.Name)
			delete(e.handlers, td.Name)
		}
	}
}

// RegisterSinglePluginTools registers tools of the specified plugin to Registry and Handler.
//
// M20-04：工具注册失败不再静默跳过。失败项收集到返回的 ToolRegistrationFailures 切片中
// （含 plugin ID + tool name + 失败原因），调用方可输出到 stderr/日志供用户感知。
// 注册成功一项立即接续下一项，单个工具失败不影响其他工具的注册。
func (e *Executor) RegisterSinglePluginTools(pluginID string) error {
	if e.PluginMgr == nil {
		return fmt.Errorf("plugin manager not initialized")
	}
	p, ok := e.PluginMgr.Get(pluginID)
	if !ok {
		return fmt.Errorf("plugin %q does not exist", pluginID)
	}
	for _, td := range p.Tools {
		spec := Spec{
			Name:               td.Name,
			Description:        td.Description,
			InputSchema:        td.InputSchema,
			RequiredPermission: LevelWorkspaceWrite,
			ConcurrencySafe:    false,
		}
		if err := e.registry.RegisterPlugin(spec); err != nil {
			// M20-04：收集失败项到全局累计器，由调用方汇总输出。
			recordToolRegistrationFailure(pluginID, td.Name, err)
			continue
		}
		command := td.Command
		e.mu.Lock()
		e.handlers[td.Name] = e.pluginToolHandler(command)
		e.mu.Unlock()
	}
	return nil
}

// injectPluginHooks injects enabled plugin hook definitions into the hook manager
func (e *Executor) injectPluginHooks(hookMgr *hook.Manager) {
	if e.PluginMgr == nil {
		return
	}
	pluginHooks := e.PluginMgr.GetEnabledHooks()
	config := hookMgr.Config()
	for _, hd := range pluginHooks {
		h := &hook.HookCommand{
			HookBase: hook.HookBase{
				Matcher: hd.ToolName,
				Pattern: "*",
				Action:  hook.ActionAllow,
				Source:  hook.SourcePlugin,
			},
			Command: hd.Command,
		}
		switch hd.Event {
		case "PreToolUse":
			config.PreToolUse = append(config.PreToolUse, h)
		case "PostToolUse":
			config.PostToolUse = append(config.PostToolUse, h)
		case "Stop":
			config.Stop = append(config.Stop, h)
		}
	}
	hookMgr.SetConfig(config)
}

// ReinjectPluginHooks re-injects hooks of all active plugins
// Called after plugin enable/disable to ensure hook list matches active plugins
func (e *Executor) ReinjectPluginHooks() error {
	if e.hookMgr == nil {
		return nil
	}

	// Remove all plugin-sourced hooks
	e.hookMgr.RemoveAllPluginHooks()

	// Re-inject all active plugin hooks
	e.injectPluginHooks(e.hookMgr)

	return nil
}

// HookManager returns the hook manager reference for external components like /hooks command
func (e *Executor) HookManager() *hook.Manager {
	return e.hookMgr
}

// ListMCPServers lists all connected MCP server names
func (e *Executor) ListMCPServers() ([]string, error) {
	servers := e.registry.ListMCPServers()
	return servers, nil
}

// RemoveMCPClient disconnects and removes a cached MCP client
func (e *Executor) RemoveMCPClient(name string) error {
	if err := RemoveMCPClient(name); err != nil {
		return err
	}
	e.registry.UnregisterMCP(name)
	return nil
}

// RegisterPluginTools registers enabled plugin tools to Registry and Handler.
//
// M20-04：工具注册失败不再静默跳过。失败项累计到 process-global 失败列表，
// 调用方可通过 DrainToolRegistrationFailures() 取出并输出到 stderr/日志，
// 启动时由 main 路径或 /plugin enable 命令路径汇总输出给用户。
func (e *Executor) RegisterPluginTools() {
	if e.PluginMgr == nil {
		return
	}

	pluginTools := e.PluginMgr.GetEnabledTools()
	for _, td := range pluginTools {
		spec := Spec{
			Name:               td.Name,
			Description:        td.Description,
			InputSchema:        td.InputSchema,
			RequiredPermission: LevelWorkspaceWrite,
			ConcurrencySafe:    false,
		}

		if err := e.registry.RegisterPlugin(spec); err != nil {
			// M20-04：收集失败项（含 plugin/tool/原因）替代旧的仅日志输出。
			// 失败后立即 continue 处理下一项，单工具失败不影响其他工具注册。
			pluginID := e.findPluginIDByToolName(td.Name)
			recordToolRegistrationFailure(pluginID, td.Name, err)
			log.Printf("[Executor] plugin tool %q registration skipped: %v", td.Name, err)
			continue
		}

		command := td.Command
		e.mu.Lock()
		e.handlers[td.Name] = e.pluginToolHandler(command)
		e.mu.Unlock()
	}
}

// findPluginIDByToolName 反查 tool name 所属的 plugin ID（用于失败提示）。
// 遍历所有已加载插件，匹配首个含该 tool name 的插件返回其 ID。
// 未匹配时返回空字符串（不应发生，仅作防御性兜底）。
func (e *Executor) findPluginIDByToolName(toolName string) string {
	if e.PluginMgr == nil {
		return ""
	}
	for _, p := range e.PluginMgr.List() {
		for _, td := range p.Tools {
			if td.Name == toolName {
				return p.ID
			}
		}
	}
	return ""
}

// getAuditLog 懒加载沙箱审计日志实例，供 ApplyResourceLimits 等场景复用。
// 使用 sync.Once 保证并发安全且仅初始化一次，避免每次命令执行都创建新实例。
func (e *Executor) getAuditLog() *sandbox.AuditLog {
	e.auditLogOnce.Do(func() {
		e.auditLog = sandbox.NewAuditLog()
	})
	return e.auditLog
}

// pluginToolHandler creates a handler for plugin tools that executes the plugin command.
//
// P0-3/P0-6 安全集成：
// - 命令执行前调用 sandbox.Manager.Prepare（经 prepareSandbox 统一入口），
// sandbox 不可用时降级到无沙箱但仍强制 env 白名单。
// - exec.Cmd.Env 强制使用白名单（sandbox.BuildEnv + DefaultEnvConfig），
// 仅继承 PATH/HOME/USER/LANG/LC_ALL/TERM，剥离 *_API_KEY/*_TOKEN/*_SECRET 等，
// 追加 PLUGIN_TOOL_INPUT 供插件读取结构化输入。
// - 命令解析走 parseCommandToArgs（不经 shell），shell 元字符不会被解释。
func (e *Executor) pluginToolHandler(command string) Handler {
	return func(ctx context.Context, raw json.RawMessage) (string, error) {
		var input map[string]interface{}
		if err := json.Unmarshal(raw, &input); err != nil {
			return "", fmt.Errorf("failed to parse plugin tool input: %w", err)
		}

		inputJSON, _ := json.Marshal(input)

		// 替换 {{key}} 占位符；输入值仅剥离换行（防止命令行参数断裂），
		// 命令注入由 sandbox ValidateCommand + 无 shell 执行兜底（P0-2 删除黑名单后改走 sandbox）。
		cmd := command
		for k, v := range input {
			strVal := sanitizePluginInput(fmt.Sprintf("%v", v))
			cmd = strings.ReplaceAll(cmd, "{{"+k+"}}", strVal)
		}

		// Parse command into program + args and execute directly without a shell
		// to prevent command injection via shell metacharacter interpretation.
		args := parseCommandToArgs(cmd)
		if len(args) == 0 {
			return "", fmt.Errorf("plugin command is empty after parsing")
		}

		// P0-3: 沙箱准备（Manager.Prepare 优先，不可用降级到 legacy/无沙箱）。
		// 沙箱拒绝时文案保持不变（模型的非-error 契约），但返回软哨兵
		// errSandboxDeniedSoft，由 executeDirect 打上 FailureKind=SandboxDenied
		// （S11 G1-B 定型，G1-A 收口；与 write_file/edit_file 同形）。
		sbCtx, cleanup, sandboxed, sandboxErr := e.prepareSandbox(ctx, cmd, e.workDir)
		if sandboxErr != nil {
			return fmt.Sprintf("sandbox denied: %s", sandboxErr.Error()), errSandboxDeniedSoft
		}
		if cleanup != nil {
			defer cleanup()
		}

		execCmd := exec.CommandContext(ctx, args[0], args[1:]...)

		// P0-6: env 白名单 — 无论是否沙箱化都剥离密钥变量（GITHUB_TOKEN/*_API_KEY 等）。
		// sandboxed 时复用 sbCtx.Env（已白名单过滤）；否则手动构造白名单。
		var env []string
		if sandboxed && len(sbCtx.Env) > 0 {
			env = sbCtx.Env
		} else {
			env = sandbox.BuildEnv(sandbox.DefaultEnvConfig())
		}
		env = append(env, "PLUGIN_TOOL_INPUT="+string(inputJSON))
		execCmd.Env = env

		// 工作目录：sandboxed 用沙箱上下文的 WorkDir，否则用 Executor 创建时的工作目录
		if sandboxed && sbCtx.WorkDir != "" {
			execCmd.Dir = sbCtx.WorkDir
		} else if e.workDir != "" {
			execCmd.Dir = e.workDir
		}

		var stdout, stderr safebuf.Buffer
		execCmd.Stdout = &stdout
		execCmd.Stderr = &stderr

		// B7/C11: **两条分支都用进程组启动**（与 bash.go 同一收敛， 批 D）。
		var startErr error
		if sandboxed {
			// P0-1 修复：执行命令前应用 OS 级隔离（同 bash.go，fail-closed 防绕过）。
			if osSb := sbCtx.OSSandbox(); osSb != nil {
				if restrictErr := osSb.Restrict(execCmd); restrictErr != nil {
					return "", fmt.Errorf("sandbox: OS restrict failed (command not started for safety): %w", restrictErr)
				}
			}
		}
		startErr = sandbox.StartCommandWithProcessGroupContext(ctx, execCmd)
		if startErr != nil {
			return "", fmt.Errorf("plugin command start failed: %w", startErr)
		}

		// P0-3：Start 后调用 PostStartHook（Windows Job Object AssignProcess）。
		// 同 bash.go 模式，fail-closed 防绕过。
		if sandboxed && sbCtx.PostStartHook != nil {
			if hookErr := sbCtx.PostStartHook(execCmd); hookErr != nil {
				_ = sandbox.KillProcessGroup(execCmd)
				_ = execCmd.Wait()
				return "", fmt.Errorf("sandbox: post-start hook failed: %w", hookErr)
			}
		}

		// P1-1：Start 后应用资源限制（Linux prlimit / macOS setrlimit）。
		if sandboxed && sbCtx.ResourceLimits != (sandbox.ResourceLimits{}) {
			_ = sandbox.ApplyResourceLimits(execCmd, sbCtx.ResourceLimits, e.getAuditLog())
		}

		// 与 bash.go 共用同一单一实现；两条分支共用（execCmd 已由 StartCommandWithProcessGroup 建组）。
		_, err := sandbox.WaitProcessGroupWithContext(ctx, execCmd)
		if err != nil {
			return stdout.String(), fmt.Errorf("plugin command execution failed: %w\n%s", err, stderr.String())
		}

		return stdout.String(), nil
	}
}

// parseCommandToArgs splits a command string into program and arguments,
// respecting double-quoted and single-quoted segments. This avoids shell
// interpretation entirely when used with exec.Command(program, args...).
func parseCommandToArgs(cmd string) []string {
	var args []string
	var current strings.Builder
	inQuote := false
	quoteChar := byte(0)

	for i := 0; i < len(cmd); i++ {
		ch := cmd[i]
		switch {
		case ch == '"' || ch == '\'':
			if inQuote && ch == quoteChar {
				inQuote = false
				quoteChar = 0
			} else if !inQuote {
				inQuote = true
				quoteChar = ch
			} else {
				current.WriteByte(ch)
			}
		case (ch == ' ' || ch == '\t') && !inQuote:
			if current.Len() > 0 {
				args = append(args, current.String())
				current.Reset()
			}
		default:
			current.WriteByte(ch)
		}
	}
	if current.Len() > 0 {
		args = append(args, current.String())
	}
	return args
}

// scheduleCronHandler is the cron job scheduling handler
func (e *Executor) scheduleCronHandler() Handler {
	return func(ctx context.Context, raw json.RawMessage) (string, error) {
		var input struct {
			Schedule    string `json:"schedule"`
			Command     string `json:"command"`
			Description string `json:"description"`
		}
		if err := json.Unmarshal(raw, &input); err != nil {
			return "", fmt.Errorf("failed to parse input: %w", err)
		}
		if input.Schedule == "" {
			return "", fmt.Errorf("schedule is required")
		}
		if input.Command == "" {
			return "", fmt.Errorf("command is required")
		}
		job, err := e.CronMgr.Create(input.Schedule, input.Command, input.Description)
		if err != nil {
			return "", err
		}
		return fmt.Sprintf("Cron job created: %s [%s] %s", job.ID, job.Schedule, job.Command), nil
	}
}

// userChoiceHandler is the multiple choice user selection handler
func (e *Executor) userChoiceHandler() Handler {
	return func(ctx context.Context, raw json.RawMessage) (string, error) {
		var input struct {
			Question string   `json:"question"`
			Options  []string `json:"options"`
		}
		if err := json.Unmarshal(raw, &input); err != nil {
			return "", fmt.Errorf("failed to parse input: %w", err)
		}
		var opts []string
		for i, opt := range input.Options {
			opts = append(opts, fmt.Sprintf("%d. %s", i+1, opt))
		}
		return fmt.Sprintf("Please choose: %s\n%s", input.Question, strings.Join(opts, "\n")), nil
	}
}

// configGetHandler is the read config handler
func (e *Executor) configGetHandler() Handler {
	return func(ctx context.Context, raw json.RawMessage) (string, error) {
		var input struct {
			Key string `json:"key"`
		}
		if err := json.Unmarshal(raw, &input); err != nil {
			return "", fmt.Errorf("failed to parse input: %w", err)
		}
		e.configMu.RLock()
		value, ok := e.Config[input.Key]
		e.configMu.RUnlock()
		if ok {
			return fmt.Sprintf("%s = %s", input.Key, value), nil
		}
		return fmt.Sprintf("config key %q not set", input.Key), nil
	}
}

// configSetHandler is the write config handler
// only allows setting whitelisted configuration keys for safety
func (e *Executor) configSetHandler() Handler {
	// Whitelist of allowed configuration keys
	allowedKeys := map[string]bool{
		"model": true, "permission_level": true, "max_tokens": true,
		"temperature": true, "search_budget": true, "bash_timeout": true,
		"bash_max_output": true, "format_max_output": true,
		"tool_result_max_length": true,
	}
	return func(ctx context.Context, raw json.RawMessage) (string, error) {
		var input struct {
			Key   string `json:"key"`
			Value string `json:"value"`
		}
		if err := json.Unmarshal(raw, &input); err != nil {
			return "", fmt.Errorf("failed to parse input: %w", err)
		}
		if input.Key == "" {
			return "", fmt.Errorf("key cannot be empty")
		}
		if !allowedKeys[input.Key] {
			return "", fmt.Errorf("config key %q is not allowed to be set via config_set", input.Key)
		}
		e.configMu.Lock()
		e.Config[input.Key] = input.Value
		e.configMu.Unlock()
		// Apply config to runtime variables
		e.applyConfig(input.Key)
		return fmt.Sprintf("set %s = %s", input.Key, input.Value), nil
	}
}

// applyConfig applies a config key-value to runtime package-level variables
func (e *Executor) applyConfig(key string) {
	e.configMu.RLock()
	val := e.Config[key]
	e.configMu.RUnlock()
	switch key {
	case "tool_result_max_length":
		var n int
		if _, err := fmt.Sscanf(val, "%d", &n); err == nil && n > 0 {
			MaxToolResultLength = n
		}
	}
}

// exportSessionHandler is the export session handler
func (e *Executor) exportSessionHandler() Handler {
	return func(ctx context.Context, raw json.RawMessage) (string, error) {
		var input struct {
			Path string `json:"path"`
		}
		if err := json.Unmarshal(raw, &input); err != nil {
			return "", fmt.Errorf("failed to parse input: %w", err)
		}
		if input.Path == "" {
			return "", fmt.Errorf("path is required")
		}
		if err := validateFilePath(input.Path); err != nil {
			return "", err
		}
		if e.SessionStore != nil {
			data, err := e.SessionStore.ExportCurrentSession()
			if err != nil {
				return "", fmt.Errorf("failed to export session: %w", err)
			}
			if err := util.AtomicWriteFile(input.Path, data, 0644); err != nil {
				return "", fmt.Errorf("failed to write file: %w", err)
			}
			return fmt.Sprintf("session exported to %s", input.Path), nil
		}
		return "no active session to export", nil
	}
}

// structuredOutputHandler is the structured output handler
func (e *Executor) structuredOutputHandler() Handler {
	return func(ctx context.Context, raw json.RawMessage) (string, error) {
		var input struct {
			Schema map[string]interface{} `json:"schema"`
			Data   json.RawMessage        `json:"data"`
		}
		if err := json.Unmarshal(raw, &input); err != nil {
			return "", fmt.Errorf("failed to parse input: %w", err)
		}
		if len(input.Data) > 0 {
			return string(input.Data), nil
		}
		schemaJSON, _ := json.MarshalIndent(input.Schema, "", "  ")
		return fmt.Sprintf("Please output in the following JSON Schema format:\n%s", string(schemaJSON)), nil
	}
}

// IsConcurrencySafe checks if a tool is concurrency-safe via the executor's registry
func (e *Executor) IsConcurrencySafe(name string) bool {
	e.mu.RLock()
	defer e.mu.RUnlock()
	if e.registry != nil {
		return e.registry.IsConcurrencySafe(name)
	}
	return false
}

// BaseContext 返回 Executor 的会话级 context。
//
// 消费者 = fire-and-forget 的 fork 子 agent（agent.go RunForkAsync）：
// 它的寿命锚在这里 —— Close() 取消它时，所有仍在运行的 fork 陪葬（owner 收杀）。
// 惰性初始化 ⇒ 零值 Executor（测试/字面量构造）也可安全调用。
func (e *Executor) BaseContext() context.Context {
	e.baseOnce.Do(func() {
		e.baseCtx, e.baseCancel = context.WithCancel(context.Background())
	})
	return e.baseCtx
}

// Close gracefully shuts down the Executor, releasing MCP clients and other resources
func (e *Executor) Close() {
	// 会话收场 ⇒ 取消会话级 ctx ⇒ 仍在运行的 fork 子 agent 进入终态
	// （owner 收杀，承接 H1-2「父取消传播」意图的正确锚点）。幂等：cancel 可重复调用。
	e.BaseContext()
	if e.baseCancel != nil {
		e.baseCancel()
	}
	CloseAllMCPClients()
}

// nextLSPVersion 返回文件的下一次 DidChange version 并自增内部计数器（B4-3A 新增）
// 首次调用返回 2（DidOpen 已占 version=1），后续单调递增以满足 LSP 协议要求
// 并发安全：内部通过 lspVersionsMu 加锁
func (e *Executor) nextLSPVersion(filePath string) int {
	absPath, err := filepath.Abs(filePath)
	if err != nil {
		absPath = filePath
	}
	e.lspVersionsMu.Lock()
	defer e.lspVersionsMu.Unlock()
	e.lspVersions[absPath]++
	if e.lspVersions[absPath] < 2 {
		e.lspVersions[absPath] = 2 // DidOpen 已占 version=1
	}
	return e.lspVersions[absPath]
}

// notifyLSPDidChange 写文件后通知 LSP 内容变更（B4-3A 新增）
// 首次通知某文件时先发 DidOpen（version=1）再发 DidChange，避免 server 因未 DidOpen 报错
// getLSPClient 失败（语言不支持 / server 未装）则静默跳过，DidChange 错误不传播（不阻塞写文件主流程）
func (e *Executor) notifyLSPDidChange(ctx context.Context, filePath, newContent string) {
	client, err := e.getLSPClient(ctx, filePath)
	if err != nil || client == nil {
		return // 语言不支持或 server 未装，静默跳过
	}

	absPath, err := filepath.Abs(filePath)
	if err != nil {
		absPath = filePath
	}

	// 检查是否首次通知该文件：若是，先 DidOpen（version=1），DidOpen 幂等故并发重复发送可接受
	e.lspVersionsMu.Lock()
	_, didOpened := e.lspVersions[absPath]
	e.lspVersionsMu.Unlock()

	if !didOpened {
		_ = client.DidOpen(ctx, filePath, lsp.DetectLanguageID(filePath), newContent)
	}

	version := e.nextLSPVersion(filePath)
	_ = client.DidChange(ctx, filePath, version, newContent)
}

// toolSearchHandler is the ToolSearch tool handler.
// 支持两种模式（F3 修复）：
// - 搜索模式：query 非空时，按关键词搜索工具名+描述，返回匹配列表
// - 加载模式：load 非空时，加载指定工具的 schema 到会话级已加载集（LoadDeferredTool），
// 后续 buildToolDefs 会合并该工具 schema 到 tools 数组
//
// 对标同类 CLI tool_search：模型先搜索发现工具，再加载 schema，实现按需加载。
func (e *Executor) toolSearchHandler() Handler {
	return func(ctx context.Context, raw json.RawMessage) (string, error) {
		var input struct {
			Query    string `json:"query"`
			Category string `json:"category"`
			Load     string `json:"load"`
		}
		if err := parseInput(raw, &input); err != nil {
			return "", fmt.Errorf("failed to parse input: %w", err)
		}

		// 加载模式：加载指定延迟工具的 schema
		if input.Load != "" {
			spec, ok := e.registry.Get(input.Load)
			if !ok {
				return fmt.Sprintf("tool %q not found", input.Load), nil
			}
			if spec.Exposure != ExposureDeferred {
				return fmt.Sprintf("tool %q is already directly available (no need to load)", input.Load), nil
			}
			e.LoadDeferredTool(input.Load)
			// 返回工具 schema 摘要供模型理解参数
			desc := spec.Description
			if len(desc) > 200 {
				desc = desc[:200] + "..."
			}
			return fmt.Sprintf("✅ Tool %q loaded. It will be available in subsequent requests.\nDescription: %s\nYou can now call %s with the appropriate parameters.", input.Load, desc, input.Load), nil
		}

		// 搜索模式：按关键词搜索
		if input.Query == "" {
			return "", fmt.Errorf("either 'query' (to search) or 'load' (to load a tool) must be provided")
		}
		if input.Category == "" {
			input.Category = "all"
		}

		query := strings.ToLower(input.Query)
		var results []string

		allSpecs := e.registry.All()
		for _, spec := range allSpecs {
			// filter by category
			if input.Category != "all" {
				if _, ok := e.registry.builtin[spec.Name]; ok && input.Category != "builtin" {
					continue
				}
				if _, ok := e.registry.mcp[spec.Name]; ok && input.Category != "mcp" {
					continue
				}
				if _, ok := e.registry.plugin[spec.Name]; ok && input.Category != "plugin" {
					continue
				}
			}

			// match by keyword
			if strings.Contains(strings.ToLower(spec.Name), query) ||
				strings.Contains(strings.ToLower(spec.Description), query) {
				results = append(results, fmt.Sprintf("%s — %s", spec.Name, spec.Description))
			}
		}

		if len(results) == 0 {
			return fmt.Sprintf("no tools found matching %q", input.Query), nil
		}
		return fmt.Sprintf("Found %d tools:\n%s\n\nTo load a tool's schema, call ToolSearch with {\"load\": \"<tool_name>\"}", len(results), strings.Join(results, "\n")), nil
	}
}

// sendMessageHandler is the SendMessage tool handler
func (e *Executor) sendMessageHandler() Handler {
	return func(ctx context.Context, raw json.RawMessage) (string, error) {
		var input struct {
			AgentID string `json:"agent_id"`
			Message string `json:"message"`
		}
		if err := parseInput(raw, &input); err != nil {
			return "", fmt.Errorf("failed to parse input: %w", err)
		}
		if input.AgentID == "" {
			return "", fmt.Errorf("agent_id cannot be empty")
		}
		if input.Message == "" {
			return "", fmt.Errorf("message cannot be empty")
		}
		// Store message in agent message bus
		e.agentBusMu.Lock()
		if e.agentBus[input.AgentID] == nil {
			e.agentBus[input.AgentID] = make([]AgentMessage, 0)
		}
		const maxAgentBusMessages = 1000
		msgs := e.agentBus[input.AgentID]
		if len(msgs) >= maxAgentBusMessages {
			msgs = msgs[len(msgs)-maxAgentBusMessages+1:]
		}
		e.agentBus[input.AgentID] = append(msgs, AgentMessage{
			From:      "main",
			Content:   input.Message,
			Timestamp: time.Now(),
		})
		e.agentBusMu.Unlock()
		return fmt.Sprintf("Message sent to agent %s", input.AgentID), nil
	}
}

// sleepHandler is the Sleep tool handler
func (e *Executor) sleepHandler() Handler {
	return func(ctx context.Context, raw json.RawMessage) (string, error) {
		var input struct {
			Seconds float64 `json:"seconds"`
		}
		if err := parseInput(raw, &input); err != nil {
			return "", fmt.Errorf("failed to parse input: %w", err)
		}
		if input.Seconds < 0.1 {
			return "", fmt.Errorf("wait time cannot be less than 0.1 seconds")
		}
		if input.Seconds > 60 {
			return "", fmt.Errorf("wait time cannot exceed 60 seconds")
		}

		duration := time.Duration(input.Seconds * float64(time.Second))
		select {
		case <-time.After(duration):
			return fmt.Sprintf("Waited %.1f seconds", input.Seconds), nil
		case <-ctx.Done():
			return "", ctx.Err()
		}
	}
}

// taskHandler 返回 Task tool 的 Handler 适配器（N1 subagent / Task tool，）。
//
// 将 TaskTool（实现 Tool 接口）适配为旧 Handler 函数签名，复用现有 Executor.handlers 分发机制。
// 内部委托给 TaskTool.Call，提取 ToolResult.Content 作为返回字符串。
// AgentTaskMgr 未初始化时返回错误（测试场景兜底）。
func (e *Executor) taskHandler() Handler {
	return func(ctx context.Context, raw json.RawMessage) (string, error) {
		if e.AgentTaskMgr == nil {
			return "", fmt.Errorf("AgentTaskManager not initialized for Task tool")
		}
		taskTool := NewTaskTool(e.AgentTaskMgr)
		_, result, err := taskTool.Call(ctx, raw)
		if err != nil {
			return "", err
		}
		if result == nil {
			return "", nil
		}
		if result.IsError {
			// R3/L3.2：子 agent 的终局拒绝种类必须**结构化**穿过 (string, error) 边界，
			// 由 executeDirect 落到 Result.FailureKind（R1 契约 C2）。
			// 修复前这里只留文本 ⇒ 上层无法区分"终局拒绝"与"普通失败"，
			// 子 agent 被拒后可以自由换工具重试。
			return result.Content, &ToolExecError{
				Tool:    "Task",
				Kind:    result.FailureKind,
				Confirm: result.Confirm,
				Msg:     fmt.Sprintf("task tool error: %s", result.Content),
			}
		}
		return result.Content, nil
	}
}

// sanitizePluginInput sanitizes plugin tool input values to prevent command-line argument breakage.
//
// P0-2：删除 shellChars 黑名单转义逻辑。命令注入防御改由三层兜底：
// - parseCommandToArgs 无 shell 执行（元字符不被 shell 解释）
// - sandbox.Manager.ValidateCommand 执行前拦截危险命令模式
// - pluginToolHandler 使用 env 白名单 + 进程组隔离
//
// 保留换行符剥离：换行会断裂命令行参数（与注入防御无关，纯参数完整性）。
func sanitizePluginInput(val string) string {
	replacer := strings.NewReplacer(
		"\n", "",
		"\r", "",
	)
	return replacer.Replace(val)
}

// formatResult formats a tool result string, reading truncation length from Config
func (e *Executor) formatResult(result string) string {
	maxLen := MaxToolResultLength
	if v, ok := e.Config["tool_result_max_length"]; ok {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			maxLen = n
		}
	}
	return FormatToolResult(result, maxLen)
}
