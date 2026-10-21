package tui

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/anchorandfish/whale-code/internal/ai"
	"github.com/anchorandfish/whale-code/internal/approval"
	"github.com/anchorandfish/whale-code/internal/bus"
	"github.com/anchorandfish/whale-code/internal/command"
	"github.com/anchorandfish/whale-code/internal/config"
	ctxpkg "github.com/anchorandfish/whale-code/internal/context"
	"github.com/anchorandfish/whale-code/internal/filehistory"
	"github.com/anchorandfish/whale-code/internal/goal"
	imgproc "github.com/anchorandfish/whale-code/internal/image"
	"github.com/anchorandfish/whale-code/internal/keybindings"
	"github.com/anchorandfish/whale-code/internal/memorybridge"
	"github.com/anchorandfish/whale-code/internal/metrics"
	"github.com/anchorandfish/whale-code/internal/permission"
	"github.com/anchorandfish/whale-code/internal/prompt"
	"github.com/anchorandfish/whale-code/internal/sandbox/validator"
	"github.com/anchorandfish/whale-code/internal/secret"
	"github.com/anchorandfish/whale-code/internal/security"
	"github.com/anchorandfish/whale-code/internal/session"
	"github.com/anchorandfish/whale-code/internal/skill"
	"github.com/anchorandfish/whale-code/internal/tools"
	"github.com/anchorandfish/whale-code/internal/util"
	"github.com/anchorandfish/whale-code/internal/whalehome"
)

const (
	stateIdle = iota
	stateStreaming
	stateToolExec
)

// statusCode500Re 词边界锚定的 "500" 匹配（D9 修复）。
// 避免 "5000 tokens" / "error code 15000" 误匹配为 500 服务端错误。
var statusCode500Re = regexp.MustCompile(`\b500\b`)

var errSessionChangedDuringRequest = errors.New("session changed while request was streaming")

// ParsedStreamEvent represents a parsed stream event with unified structure
type ParsedStreamEvent struct {
	Type      string          // "text", "tool_use", "tool_result", "thinking", "error"
	Content   string          //
	ToolName  string          //
	ToolInput json.RawMessage //
	Err       error           //
}

// OutputHandler defines the interface for handling parsed stream events
// PlainRunner and BridgeRunner implement this interface with their own output mechanisms
type OutputHandler interface {
	OnText(text string)
	OnToolUse(name string, input json.RawMessage)
	OnToolResult(result string)
	OnThinking(text string)
	OnError(err error)
}

// TurnEndHook turn 结束钩子接口（N26）。
// 注册到 runnerCore，每轮对话结束时（autoSnapshotOnTurnEnd 之后）按注册顺序调用。
// goal.Manager 通过实现 OnTurnEnd(ctx, threadID, goal.TurnResult) error 方法隐式满足此接口（duck typing）。
// 单个钩子错误仅记录日志，不阻塞后续钩子或主流程（运行时冲突矩阵第 5 行：goal 续跑失败不阻塞用户输入）。
type TurnEndHook interface {
	OnTurnEnd(ctx context.Context, threadID string, turnResult goal.TurnResult) error
}

// runnerCore contains shared fields and methods for PlainRunner and BridgeRunner
type runnerCore struct {
	Session      *session.Session
	Config       *config.Config
	AIClient     *ai.SimpleClient
	Registry     *tools.Registry
	Executor     *tools.Executor
	CmdRegistry  *command.Registry
	Policy       *permission.Policy
	Budget       ctxpkg.Budget
	Compactor    *ctxpkg.Compactor
	AICompactor  *ctxpkg.AICompactor
	Estimator    *ai.TokenEstimator
	Store        *session.Store
	Fallback     *ai.FallbackTracker
	MagicDocs    *config.MagicDocs
	MemoryStore  *config.MemoryStore
	MemoryIndex  *config.MemoryIndex
	MemSelector  *config.MemorySelector
	MemExtractor *config.AutoMemoryExtractor
	UsageTracker *ai.UsageTracker
	CollapseMgr  *ctxpkg.ContextCollapseManager
	Caching      *ai.CachingIntegration
	// H13-G1: TokenSource 用于 BodyAfterPrefix scope 获取 cachedInputTokens。
	// 在 NewRunnerCore 中构造并存储（同时注入到 SimpleClient）。
	TokenSource ai.TokenSource

	// C20: 事件总线与生命周期处理器，用于 Session ↔ Context 解耦。
	// Store 发布 SessionForkEvent/SessionSwitchEvent；LifecycleHandler
	// 订阅并清空 Context 缓存 + 从新 session 重载。
	EventBus         *bus.Bus
	LifecycleHandler *ctxpkg.SessionLifecycleHandler
	// 方案 D：CacheBreakHandler 订阅 CacheBreakEvent，IsCacheBroken() 供压缩管线降级判断。
	CacheBreakHandler *ctxpkg.CacheBreakHandler

	// N11 修复 N1：CompactionPipeline 作为持久字段复用，断路器状态跨调用保留。
	// 每次 handleCompact 新建 pipeline → CircuitBreaker 状态重置 → CB 形同虚设。
	// 改为持久字段后，连续 compact 失败 3 次断路器正确打开（借鉴同类 CLI
	// MAX_CONSECUTIVE_AUTOCOMPACT_FAILURES=3 机制）。
	Pipeline *ctxpkg.CompactionPipeline

	// RecoveryCoordinator coordinates retry/degrade/compress to prevent cascading failures
	RecoveryCoordinator *ai.RecoveryCoordinator
	// RetryConfig holds retry policy for StreamWithRetry
	RetryConfig ai.RetryConfig

	// N7: Keybindings manager for configurable key mappings
	Keybindings *keybindings.Manager

	ModelName        string
	AvailableModels  []string
	ModelRegistry    *ai.ModelRegistry
	SessionName      string
	WorkDir          string
	InputTokens      int
	OutputTokens     int
	RequestCount     int // 当前会话已完成的 provider 请求数（chat + compact AI，/context Requests 行数据源）
	LastInputTokens  int
	LastOutputTokens int
	requestSnapshot  contextRequestSnapshot
	sessionEpoch     atomic.Uint64
	State            int
	StreamMu         sync.Mutex
	StateMu          sync.RWMutex
	// ThinkingEnabled 三态：nil=未指定 · 指向 true=显式开 · 指向 false=显式关。
	// 与 config.ThinkingEnabled / ai.ThinkingConfig.Enabled 同类型（零转换）。
	ThinkingEnabled      *bool
	SavedMsgCount        int
	FirstUserInput       string
	ResponseLanguage     string
	ThinkingBudgetTokens int

	// N15: prompt 统一构造器，收口原 4 个分散构造函数（对标同类 CLI getSystemPrompt 单入口）。
	PromptBuilder *prompt.Builder

	// N26: turnEndHooks turn 结束钩子列表（goal.Manager 通过 RegisterTurnEndHook 注册，）。
	// 每轮对话结束时（autoSnapshotOnTurnEnd 之后）按注册顺序调用。
	// 钩子错误仅记录日志，不阻塞主流程（goal 续跑失败不阻塞用户输入）。
	turnEndHooks []TurnEndHook

	// N26: goalManager 长期目标管理器（，/goal 命令接线层，plain/bridge 路径）。
	// 在 registerCommonCommands 中通过 newRunnerCoreGoalManager 构造，注册为 TurnEndHook。
	goalManager *goal.Manager

	// wiringCancel 执行 H11N27 memory 接线的取消与等待（ForgettingScanner）。
	// 由 initProductionWiring 设置，Close 时调用以避免 goroutine 泄漏和退出后继续写盘。
	// TUI 模式由 lifecycle.go 的 m.wiringCancel 管理，此处仅用于 PlainRunner/BridgeRunner。
	wiringCancel context.CancelFunc
}

func (c *runnerCore) replaceSession(sess *session.Session) {
	c.Session = sess
	c.sessionEpoch.Add(1)
	c.requestSnapshot = contextRequestSnapshot{}
	c.InputTokens = 0
	c.OutputTokens = 0
	c.RequestCount = 0
	c.LastInputTokens = 0
	c.LastOutputTokens = 0
}

// getState returns the current state in a thread-safe manner
func (c *runnerCore) getState() int {
	c.StateMu.RLock()
	defer c.StateMu.RUnlock()
	return c.State
}

// setState sets the state in a thread-safe manner
func (c *runnerCore) setState(s int) {
	c.StateMu.Lock()
	defer c.StateMu.Unlock()
	c.State = s
}

// parseEvent converts a single AI stream event into a ParsedStreamEvent
func parseEvent(event ai.Event) ParsedStreamEvent {
	switch e := event.(type) {
	case ai.TextDeltaEvent:
		return ParsedStreamEvent{Type: "text", Content: e.Content}
	case ai.ThinkingDeltaEvent:
		return ParsedStreamEvent{Type: "thinking", Content: e.Content}
	case ai.ToolUseEvent:
		return ParsedStreamEvent{
			Type:      "tool_use",
			ToolName:  e.Name,
			ToolInput: json.RawMessage(e.Input),
		}
	case ai.ToolInputDeltaEvent:
		return ParsedStreamEvent{
			Type:      "tool_use",
			ToolInput: json.RawMessage(e.PartialJSON),
		}
	case ai.UsageEvent:
		return ParsedStreamEvent{Type: "usage", Content: fmt.Sprintf("in:%d out:%d", e.InputTokens, e.OutputTokens)}
	case ai.ErrorEvent:
		return ParsedStreamEvent{Type: "error", Err: e.Err}
	default:
		return ParsedStreamEvent{Type: "unknown"}
	}
}

// ParseStreamEvents parses AI stream events into a slice of ParsedStreamEvent
func (r *runnerCore) ParseStreamEvents(events []ai.Event) []ParsedStreamEvent {
	parsed := make([]ParsedStreamEvent, 0, len(events))
	for _, event := range events {
		parsed = append(parsed, parseEvent(event))
	}
	return parsed
}

// processStreamEvents processes AI stream events and dispatches to the output handler
// Returns the collected response text, thinking text, pending tool blocks, and any stream error
func (c *runnerCore) processStreamEvents(ctx context.Context, events <-chan ai.Event, handler OutputHandler) (string, string, []session.ContentBlock, error) {
	c.LastInputTokens = 0
	c.LastOutputTokens = 0
	epoch := c.sessionEpoch.Load()
	var requestUsage ai.RequestUsageAccumulator
	defer c.finalizeRequestUsage(ctx, &requestUsage, epoch)
	var responseText string
	var thinkingText string
	var pendingTools []session.ContentBlock
	toolInputBuf := make(map[string]string)
	var currentToolID string
	// 空响应排障信号（2026-09-08 A2 事故观测缺口）
	stopReceived := false
	finishReason := ""

	for event := range events {
		switch e := event.(type) {
		case ai.TextDeltaEvent:
			responseText += e.Content
			handler.OnText(e.Content)
		case ai.ThinkingDeltaEvent:
			thinkingText += e.Content
			handler.OnThinking(e.Content)
		case ai.ToolUseEvent:
			c.StreamMu.Lock()
			if e.ID != "" && e.Name != "" {
				currentToolID = e.ID
				toolInputBuf[e.ID] = ""
				pendingTools = append(pendingTools, session.ContentBlock{
					Type: "tool_use",
					ID:   e.ID,
					Name: e.Name,
				})
				handler.OnToolUse(e.Name, json.RawMessage(e.Input))
			}
			if e.Input != "" {
				toolID := e.ID
				if toolID == "" {
					toolID = currentToolID
				}
				if toolID != "" {
					if buf, ok := toolInputBuf[toolID]; ok {
						toolInputBuf[toolID] = buf + e.Input
					}
				}
			}
			c.StreamMu.Unlock()
		case ai.ToolInputDeltaEvent:
			c.StreamMu.Lock()
			if currentToolID != "" {
				if buf, ok := toolInputBuf[currentToolID]; ok {
					toolInputBuf[currentToolID] = buf + e.PartialJSON
				}
			}
			c.StreamMu.Unlock()
		case ai.UsageEvent:
			requestUsage.Add(e)
		case ai.MessageStopEvent:
			stopReceived = true
			finishReason = e.FinishReason
		case ai.ErrorEvent:
			handler.OnError(e.Err)
			return responseText, thinkingText, pendingTools, e.Err
		}
	}
	// 空响应 canary（2026-09-08 A2 事故观测缺口）：
	// 流结束但无文本无工具无错误——此前完全静默（用户只见模型"没说话"）。
	// stopReceived=false → 流截断；finishReason=length → 输出被截断（典型仅 reasoning）。
	// 仅用户主动取消（Canceled）静默；超时（DeadlineExceeded）属于需观测的异常形态。
	if responseText == "" && len(pendingTools) == 0 && (ctx.Err() == nil || ctx.Err() == context.DeadlineExceeded) {
		snap := requestUsage.Snapshot()
		log.Printf("[runner] empty response (canary): model=%s stopReceived=%v finishReason=%q thinkingLen=%d inputTokens=%d outputTokens=%d",
			c.ModelName, stopReceived, finishReason, len(thinkingText), snap.InputTokens, snap.OutputTokens)
	}
	if c.sessionEpoch.Load() != epoch {
		return responseText, thinkingText, pendingTools, errSessionChangedDuringRequest
	}

	// Populate tool inputs from buffer
	for i := range pendingTools {
		pendingTools[i].Input = toolInputBuf[pendingTools[i].ID]
	}

	return responseText, thinkingText, pendingTools, nil
}

// NewRunnerCore creates a shared runnerCore with all common initialization.
// Used by both PlainRunner and BridgeRunner to avoid code duplication.
func NewRunnerCore(cfg *config.Config, client *ai.SimpleClient, reg *ai.ModelRegistry, workDir, homeDir, sessionDir, responseLanguage string) *runnerCore {
	estimator := ai.NewTokenEstimator()
	toolRegistry := tools.NewRegistry()

	// C24: NewStore now returns an error (directory creation may fail).
	sessionStore, _ := session.NewStore(sessionDir)
	// 归档式压缩接线：settings.json compact_archive（默认 true）传播到 store。
	// 缺失此接线则用户关闭归档的设置静默失效。
	if cfg != nil && sessionStore != nil {
		sessionStore.SetArchiveEnabled(cfg.CompactArchive)
	}

	// N12: 创建 V2 统一记忆存储（workDir SHA256 隔离 + 0700/0600 + 跨进程锁）。
	// 失败时降级为 nil，下游 MemoryStore()/MemSelector()/MemExtractor() 均做 nil 防御。
	memStore, memStoreErr := config.NewMemoryStore(filepath.Join(whalehome.Dir(), "memory"), workDir)
	if memStoreErr != nil {
		fmt.Fprintf(os.Stderr, "[init] memory store init failed: %v\n", memStoreErr)
		memStore = nil
	}

	// P2：读路径数据源换成 memory 包的双 scope 合并视图（经 bridge 适配）。
	// 读路径 = MemoryIndex（MEMORY.md 入口）+ MemorySelector（注入 system prompt 的记忆段）。
	// 相对接线前的行为变化：能看到 _user/ 跨项目偏好；contradicted/已过期记忆不再注入；
	// 同名时 project 覆盖 user。写入路径（V1 迁移 / 偏好提取）仍走 memStore，不受影响。
	// 降级：bridge 构造失败时退回 memStore（行为回到接线前：仅 project scope、无有效性过滤）。
	var memoryReadSource config.MemorySource
	if memStore != nil {
		memoryReadSource = memStore
	}
	if src, srcErr := memorybridge.NewSource(homeDir, workDir); srcErr == nil {
		memoryReadSource = src
	} else {
		fmt.Fprintf(os.Stderr, "[init] memory read source (bridge) init failed, fallback to project-only: %v\n", srcErr)
	}

	// N12: 创建 MEMORY.md 索引（always loaded 入口，200 行/25KB 截断）。
	var memIndex *config.MemoryIndex
	if memoryReadSource != nil {
		memIndex = config.NewMemoryIndex(memoryReadSource)
	}
	// N12 : V1→V2 迁移（写入路径，仍用 config store；幂等，已迁移则跳过，
	// 迁移后 memory.json → memory.json.migrated）。
	if memStore != nil {
		v1Path := filepath.Join(whalehome.Dir(), "memory.json")
		if _, statErr := os.Stat(v1Path); statErr == nil {
			if migrated, migErr := config.MigrateV1ToV2(v1Path, memStore); migErr != nil {
				fmt.Fprintf(os.Stderr, "[init] V1 memory migration failed: %v\n", migErr)
			} else if migrated > 0 {
				fmt.Fprintf(os.Stderr, "[init] migrated %d V1 memory entries to V2\n", migrated)
			}
		}
	}

	// R-3：使用 NewExecutorWithConfig 接入 config 系统的 plugin 签名配置。
	// fail-closed：若 cfg.PluginSignature 配置非法（Enabled=true + Required=true + PublicKeyPath 空），
	// 返回 error 并 log.Fatalf 终止启动（不降级到 noopVerifier，避免用户误以为已启用签名验证）。
	runnerExecutor, sigErr := tools.NewExecutorWithConfig(toolRegistry, workDir, cfg)
	if sigErr != nil {
		log.Fatalf("[init] runner executor init failed (plugin signature config invalid): %v", sigErr)
	}

	core := &runnerCore{
		Session:     session.NewSession(),
		Config:      cfg,
		AIClient:    client,
		Registry:    toolRegistry,
		Executor:    runnerExecutor,
		CmdRegistry: command.NewRegistry(),
		Policy:      permission.NewPolicyFromModeName(string(cfg.PermissionMode)),
		// C4：窗口走唯一解析点 —— 用户显式值优先；config 里为 0（"由模型决定"）
		// 时按 cfg.Model 查模型表，不再把 0 原样灌进 Budget（那会让各阈值分支全线降级）。
		Budget:               ctxpkg.NewBudgetFromConfig(cfg.EffectiveContextWindow(), cfg),
		Compactor:            ctxpkg.NewCompactor(estimator),
		AICompactor:          ctxpkg.NewAICompactor(client, estimator),
		Estimator:            estimator,
		Store:                sessionStore,
		Fallback:             ai.NewFallbackTracker(client.Model(), cfg.FallbackModel),
		MagicDocs:            config.NewMagicDocs(workDir), //nolint:staticcheck // H4 保留 V1 作为 fallback
		MemoryStore:          memStore,
		MemoryIndex:          memIndex,
		MemSelector:          config.NewMemorySelector(memoryReadSource, memIndex, client),
		MemExtractor:         config.NewAutoMemoryExtractor(memStore),
		UsageTracker:         ai.NewUsageTracker(filepath.Join(whalehome.Dir(), "usage.json")),
		Caching:              ai.NewCachingIntegration(client.Model(), cfg.CacheBreakThreshold),
		RecoveryCoordinator:  ai.NewRecoveryCoordinator(),
		RetryConfig:          ai.DefaultRetryConfig(),
		ModelName:            client.Model(),
		ModelRegistry:        reg,
		AvailableModels:      reg.Names(),
		WorkDir:              workDir,
		ResponseLanguage:     responseLanguage,
		ThinkingEnabled:      cfg.ThinkingEnabled,
		ThinkingBudgetTokens: thinkingBudgetFromConfig(cfg),
	}

	// G1（2026-09-08 冒烟缺口）：子代理用量落账接线（plain/bridge 共用 runnerCore）。
	// 修复前子代理 UsageEvent 只进 CacheStats，/usage 不可见。
	core.Executor.UsageTracker = core.UsageTracker

	// B3 修复：初始化 Keybindings Manager（服务启动时静态加载 keybindings.json；运行时切换命令已移除）
	// 路径与 app.go 保持一致：~/.whale/keybindings.json（user）+ <workDir>/.whale/keybindings.json（project）
	userKeybindingsPath := filepath.Join(whalehome.Dir(), "keybindings.json")
	projectKeybindingsPath := filepath.Join(workDir, ".whale", "keybindings.json")
	kbMgr, kbErr := keybindings.NewManager(userKeybindingsPath, projectKeybindingsPath)
	if kbErr != nil {
		fmt.Fprintf(os.Stderr, "[init] failed to load keybindings config: %v, using defaults\n", kbErr)
		kbMgr, _ = keybindings.NewManager("", "")
	}
	// VimMode 联动：VimMode=true 时设为 vim 模式
	if cfg.VimMode && kbMgr != nil {
		kbMgr.SetMode("vim")
		_ = kbMgr.Reload()
	}
	core.Keybindings = kbMgr

	// N2: 注入 UsageTracker 适配器到 CmdRegistry，激活 /usage /stats 死代码（5-E 留下从未注入）。
	// 适配器把 ai.UsageTracker 适配到 command.UsageTrackerInterface，
	// 注入后 handleUsage/handleStats 可返回真实数据而非 "Usage tracking not available"。
	if core.UsageTracker != nil {
		core.CmdRegistry.UsageTracker = command.NewUsageTrackerAdapter(core.UsageTracker)
	}
	core.CmdRegistry.OnContext = core.handleContextInfo

	// T1-B：构造 TokenSource 并注入到 SimpleClient。
	// 三层 token 来源架构（L1 远端 usage / L2 启发式 + EMA offset / L3 tiktoken-go），
	// 注入后 SimpleClient.Stream 解析到 UsageEvent 时同步调用 RecordRemoteUsage 校准反馈。
	// offsetTTL 兜底 30 分钟（cfg.Token.OffsetTTL=0 时使用默认值，向后兼容旧 settings.json）。
	offsetTTL := time.Duration(cfg.Token.OffsetTTL) * time.Minute
	if offsetTTL <= 0 {
		offsetTTL = 30 * time.Minute
	}
	var tokenSource ai.TokenSource
	if cfg.Token.TiktokenEnabled {
		// 启用 L3 tiktoken-go 路径（critical model 才走，5 秒超时降级 L2）
		tokenSource = ai.NewTokenSourceWithTiktoken(estimator, core.UsageTracker, offsetTTL)
	} else {
		// 默认禁用 L3，仅用 L1 远端 usage + L2 启发式 + EMA offset
		tokenSource = ai.NewTokenSource(estimator, core.UsageTracker, false, offsetTTL)
	}
	client.SetTokenSource(tokenSource)
	// H13-G1: 存储 TokenSource 到 runnerCore，供 BodyAfterPrefix scope 使用
	core.TokenSource = tokenSource

	// H8：注入 Config 到 CmdRegistry，使 listAvailableModels 等命令能读取配置化值。
	// nil 时 Completion 方法会 fallback 到内置默认列表（向后兼容）。
	core.CmdRegistry.Config = cfg

	// Create collapse manager
	core.CollapseMgr = ctxpkg.NewContextCollapseManager(ctxpkg.DefaultCollapseConfig(), estimator, core.Budget)

	// N15: 初始化 prompt 统一构造器（100KB 单层字节预算，）。
	// H4 ：magic_docs V2 优先（DiscoverInstructionFiles），V1 兜底逻辑在 prompt.MergeMagicDocs 内；
	// memory 已统一为 V2（MemSelector），无 V1 fallback。
	// 优化 B（H13-G5 接线）：WHALE_LEAN_PROMPT=1 时启用精简 system prompt
	// （裁剪 user_memory + search_context，每次请求节省固定 token）。
	// 构造时确定、会话内稳定，不破坏 cache；默认关闭。
	core.PromptBuilder = prompt.NewBuilderWithLean(workDir, client.Model(), core.MagicDocs, core.MemSelector, core.Executor, 100*1024, prompt.LeanModeEnabled()) //nolint:staticcheck // H4 保留 V1 作为 fallback

	// Inject RecoveryCoordinator into sub-modules for coordinated retry/degrade/compress
	core.Executor.RecoveryCoordinator = core.RecoveryCoordinator
	core.Executor.RetryConfig = core.RetryConfig
	core.Executor.Fallback = core.Fallback
	core.AICompactor.SetRecoveryConfig(core.RecoveryCoordinator, core.RetryConfig, core.Fallback)
	// 省 token 第 1 批：压缩摘要走可配置的专用模型。
	// 空值 = 沿用主模型（默认，行为不变）；非空时只有摘要这一次调用换模型。
	if core.Config != nil {
		core.AICompactor.SetCompactModel(core.Config.CompactModel)
	}
	core.MemSelector.SetRecoveryConfig(core.RecoveryCoordinator, core.RetryConfig, core.Fallback)
	core.MemExtractor.SetRecoveryConfig(core.RecoveryCoordinator, core.RetryConfig, core.Fallback)
	// D4: 注入 UsageTracker，使记忆 AI 调用（SelectRelevant/ExtractAndSave）的 token cost 被记录
	core.MemSelector.SetUsageTracker(core.UsageTracker)
	core.MemExtractor.SetUsageTracker(core.UsageTracker)

	// C20: wire bus for Session ↔ Context event decoupling.
	// Store publishes SessionForkEvent/SessionSwitchEvent; the lifecycle
	// handler subscribes and clears context caches + reloads from session.
	// 若 sessionStore 为 nil（NewStore 失败），仅跳过 SetBus，handler 仍可启动
	// 但 switch 事件的 ReloadFromSession 会因 nil store 而跳过。
	eventBus := bus.NewBus()
	if sessionStore != nil {
		sessionStore.SetBus(eventBus)
	}
	ctxMgr := ctxpkg.NewSessionContextManager(core.CollapseMgr, nil, nil)
	lifecycleHandler := ctxpkg.NewSessionLifecycleHandler(eventBus, sessionStore, ctxMgr)
	if err := lifecycleHandler.Start(); err != nil {
		fmt.Fprintf(os.Stderr, "[init] session lifecycle handler start failed: %v\n", err)
		lifecycleHandler = nil
	}
	core.EventBus = eventBus
	core.LifecycleHandler = lifecycleHandler

	// B4 批次 2：接通 CacheBreakEvent 发布链路。
	// 将 eventBus 注入 CachingIntegration（延迟注入），并把 CachingIntegration
	// 挂到 RetryConfig.OnCachingIntegration，使 StreamWithRetry 在响应路径上
	// 调用 RecordStateBeforeRequest / CheckBreakAfterResponse，检测到断裂时
	// 通过 bus.Publish 发布 CacheBreakEvent 给已就绪的 context.CacheBreakHandler。
	// Plain/Bridge 模式均复用此 core，故同一接通点覆盖两条路径。
	core.Caching.SetBus(eventBus)
	core.RetryConfig.OnCachingIntegration = core.Caching

	// 方案 D + E：创建 CacheBreakHandler 并启动订阅，接通断裂检测 → 压缩降级链路。
	// - CachingIntegration 订阅 SessionSwitchEvent 重置基线（方案 E + R-3）
	// - CacheBreakHandler 订阅 CacheBreakEvent，IsCacheBroken() 供 CompactionPipeline 降级（方案 D）
	core.Caching.SubscribeSessionEvents(eventBus)
	core.CacheBreakHandler = ctxpkg.NewCacheBreakHandler(eventBus, nil)
	if err := core.CacheBreakHandler.Start(); err != nil {
		fmt.Fprintf(os.Stderr, "[init] cache break handler start failed: %v\n", err)
		core.CacheBreakHandler = nil
	}

	// Initialize file content cache
	cacheConfig := tools.DefaultFileCacheConfig()
	cacheConfig.PersistPath = filepath.Join(whalehome.Dir(), "file-cache.json")
	// 启用磁盘 LRU 内容缓存（T1-P1-B4）。
	// 内容持久化到 ~/.whale/cache/files/<sha256-prefix-2>/<sha256>，
	// 进程重启后内存 LRU miss 时查询磁盘 LRU，避免重新读原文件。
	// DiskStore 初始化失败时 NewFileContentCache 静默降级为仅内存模式。
	cacheConfig.DiskCache.Enabled = true
	cacheConfig.DiskCache.RootDir = filepath.Join(whalehome.Dir(), "cache", "files")
	core.Executor.FileCache = tools.NewFileContentCache(cacheConfig, estimator)
	_ = core.Executor.FileCache.Restore()

	// RuleStore 接线（P0，2026-09-09）：plain/bridge 路径此前同样未接 store，
	// settings.json 规则不加载、/permissions add 不持久化。与 lifecycle.go 对齐。
	if err := core.Policy.WireRuleStore(homeDir, workDir); err != nil {
		fmt.Fprintf(os.Stderr, "[init] WireRuleStore warning: %v\n", err)
	}

	// F3（S14-HG-4）接线（2026-09-15，O-U 轮）：项目级 ignore 注入。
	// 与 lifecycle.go 同落点、同语义（两处都是启动路径，缺一处即半接线）。
	// 见 lifecycle.go 处注释：`SetGlobalProjectIgnore` 无返回值，不造 error 分支。
	secret.SetGlobalProjectIgnore(workDir)

	// Initialize tool execution pipeline
	core.Executor.InitPipelineWithHooks(core.Policy, workDir)

	// P1-SEC：非 TUI 路径（plain/bridge）默认启用生产沙箱。
	// 修复前：SetSandboxMgr 仅在 TUI lifecycle.go 调用，本路径 SandboxMgr 为
	// nil → whale -p / bridge 模式完全无 OS 级沙箱（deny 命令直接执行）。
	// 与 TUI 共用 sandbox.NewProductionSandboxManager 单一事实源，避免两处
	// 平台开关逻辑漂移。
	core.Executor.EnableProductionSandbox(workDir)

	// P0-7/B7: 仅当 --enable-plugins 显式启用时加载并注册插件工具与钩子。
	// 默认插件禁用（高风险任意代码执行），需用户显式 opt-in。
	// 注意：NewBridgeRunner 会再次调用 InitPipelineWithHooks 并在之后重新启用，
	// 此处覆盖 PlainRunner 路径；BridgeRunner 路径由 bridge.go 兜底。
	if core.Config.PluginsEnabled {
		// M20-04：注册前清空累计器，注册后 drain 失败项并输出到 stderr/日志。
		tools.ResetToolRegistrationFailures()
		core.Executor.EnableAllPlugins()
		if failures := tools.DrainToolRegistrationFailures(); len(failures) > 0 {
			fmt.Fprintf(os.Stderr, "⚠️ %d plugin tool(s) failed to register during startup:\n", len(failures))
			fmt.Fprint(os.Stderr, tools.FormatToolRegistrationFailures(failures))
			log.Printf("[plugin] %d tool(s) failed to register during plain startup", len(failures))
		}
	}

	// B3: 接入 approval.Manager（C19 第三层交互层）。
	// 从 config 读取策略字符串，解析失败兜底为 on-request（B0 §5 最安全默认）；
	// 创建 Manager 后注入 PluginMgr（git_clone 等消费者）与 Pipeline（PermissionStage）。
	approvalMgr := newApprovalManagerFromConfig(cfg)
	core.Executor.PluginMgr.SetApprovalManager(approvalMgr)
	if core.Executor.Pipeline != nil {
		core.Executor.Pipeline.SetApprovalManager(approvalMgr)
	}
	// B0 §3.1 第3项：会话切换时清空 approval 缓存，避免新会话复用旧会话审批决策。
	if lifecycleHandler != nil {
		lifecycleHandler.SetCacheClearer(approvalCacheAdapter{mgr: approvalMgr})
	}

	return core
}

// newApprovalManagerFromConfig 从 config 解析审批策略并构造 approval.Manager。
// 解析失败时兜底为 PolicyOnRequest（B0 §5 最安全默认），AskFn 始终非 nil
// （nil 会导致 Manager fail-closed 返回 DecisionDeny，工具不可用）。
func newApprovalManagerFromConfig(cfg *config.Config) *approval.Manager {
	policy := approval.PolicyOnRequest
	if cfg != nil {
		if p, pErr := approval.ParsePolicy(cfg.Approval.Policy); pErr == nil {
			policy = p
		}
	}
	return approval.NewManager(policy, newApprovalAskFn())
}

// newApprovalAskFn 构造 approval.Manager 的用户交互回调（B3）。
//
// 实现策略：始终返回 DecisionAsk，将用户确认决策委托给 Pipeline 既有的
// NeedConfirm 暂停流程，从而复用 TUI 现有的 tool 确认机制，避免在 AskFn
// 内重复实现一套交互 UI：
//
// - PlainRunner：ExecuteWithConfirm 检测到 NeedConfirm 后调用
// executor.OnConfirm（终端 Y/N 提示），用户确认仅对当前调用生效。
// - BridgeRunner：executor.OnConfirm 为 nil，ExecuteWithConfirm 原样
// 返回 NeedConfirm，由 bubbletea 的 pendingConfirm UI 弹出确认提示，
// 用户确认后 executePipelineRemaining 按“本次/本会话”选择继续。
//
// 约束：不返回 nil（nil 会触发 fail-closed DecisionDeny），不恒返回
// DecisionDeny（会导致工具恒不可用）。DecisionAsk 由 PermissionStage
// 映射为 NeedConfirm=true，复用现有暂停等待确认逻辑。
func newApprovalAskFn() approval.AskFn {
	return func(ctx context.Context, toolName, input string) (approval.ApprovalDecision, string) {
		return approval.DecisionAsk, "delegate to pipeline need-confirm UI"
	}
}

// approvalCacheAdapter 适配 approval.Manager 到 context.cacheClearer 接口。
// Manager 的清理方法名为 ClearCache，与接口 Clear() 不匹配，故用适配器桥接。
type approvalCacheAdapter struct{ mgr *approval.Manager }

// Clear 实现 context.cacheClearer 接口，转发调用到 Manager.ClearCache。
func (a approvalCacheAdapter) Clear() {
	if a.mgr != nil {
		a.mgr.ClearCache()
	}
}

// registerCommonCommands registers shared command callbacks on the runner's CmdRegistry.
func registerCommonCommands(rc *runnerCore, handleUndo, handleRedo func() string, handlePluginInstall, handlePluginEnable, handlePluginDisable func(args string) string, handleReloadPlugins func() string, handleGitHub, handleDaemon func(args string) string) {
	if handleUndo != nil {
		rc.CmdRegistry.OnUndo = handleUndo
	}
	if handleRedo != nil {
		rc.CmdRegistry.OnRedo = handleRedo
	}
	rc.CmdRegistry.OnSkills = func() string { return rc.Executor.SkillMgr.FormatList() }
	rc.CmdRegistry.OnPlugins = func() string { return rc.Executor.PluginMgr.FormatList() }
	if handlePluginInstall != nil {
		rc.CmdRegistry.OnPluginInstall = handlePluginInstall
	}
	if handlePluginEnable != nil {
		rc.CmdRegistry.OnPluginEnable = handlePluginEnable
	}
	if handlePluginDisable != nil {
		rc.CmdRegistry.OnPluginDisable = handlePluginDisable
	}
	if handleReloadPlugins != nil {
		rc.CmdRegistry.OnReloadPlugins = handleReloadPlugins
	}
	rc.CmdRegistry.OnHooks = func(args string) string {
		return command.NewHooksCommandHandler(
			rc.Executor.HookManager(),
			rc.WorkDir,
		)(args)
	}
	// O-2：/skill 信任管理出口接线（plain/bridge 共用 runnerCore 装配）。
	// SSOT = skill.Manager.HandleTrustCommand，薄转发。
	rc.CmdRegistry.OnSkillManage = func(args string) (string, error) {
		return rc.Executor.SkillMgr.HandleTrustCommand(args), nil
	}
	rc.CmdRegistry.OnUltraplan = func(args string) string {
		return rc.Executor.UltraplanManager.Toggle()
	}
	rc.CmdRegistry.OnBugHunter = func(args string) string {
		switch args {
		case "start":
			return rc.Executor.BugHunterManager.Start()
		case "stop":
			return rc.Executor.BugHunterManager.Stop()
		case "status":
			return rc.Executor.BugHunterManager.Status()
		default:
			return rc.Executor.BugHunterManager.Status()
		}
	}
	if handleGitHub != nil {
		rc.CmdRegistry.OnGitHub = handleGitHub
	}
	if handleDaemon != nil {
		rc.CmdRegistry.OnDaemon = handleDaemon
	}
	// N1 subagent / Task tool：注册 /task 命令回调（plain/bridge 模式复用 TUI 的渲染逻辑）
	rc.CmdRegistry.OnTaskCommand = func(args string) string {
		if rc.Executor == nil || rc.Executor.AgentTaskMgr == nil {
			return "🤖 Sub-Agent Tasks: 管理器未初始化"
		}
		mgr := rc.Executor.AgentTaskMgr
		parts := strings.Fields(args)
		if len(parts) == 0 || parts[0] == "list" {
			return RenderTaskList(mgr)
		}
		switch parts[0] {
		case "cancel":
			if len(parts) < 2 {
				return "用法: /task cancel <id>"
			}
			fullID := resolveTaskID(mgr, parts[1])
			if fullID == "" {
				return fmt.Sprintf("❌ 未找到任务: %s", parts[1])
			}
			if err := mgr.Cancel(fullID); err != nil {
				return fmt.Sprintf("❌ 取消失败: %v", err)
			}
			return fmt.Sprintf("🚫 已取消任务 %s", shortTaskID(fullID))
		case "show":
			if len(parts) < 2 {
				return "用法: /task show <id>"
			}
			fullID := resolveTaskID(mgr, parts[1])
			if fullID == "" {
				return fmt.Sprintf("❌ 未找到任务: %s", parts[1])
			}
			info, err := mgr.Get(fullID)
			if err != nil {
				return fmt.Sprintf("❌ 查询失败: %v", err)
			}
			return formatTaskDetail(info)
		default:
			return fmt.Sprintf("未知子命令: %s\n可用: list / cancel <id> / show <id>", parts[0])
		}
	}
	rc.CmdRegistry.OnSkillExecute = func(skillName, args string) string {
		result, err := rc.Executor.SkillMgr.Execute(skillName, args)
		if err != nil {
			return ""
		}
		return result
	}
	// OnSkillExecuteWithMeta 带 meta 的 skill 执行回调（供需要 model/allowedTools 的调用方使用）
	rc.CmdRegistry.OnSkillExecuteWithMeta = func(skillName, args string) (skill.SkillResult, error) {
		return rc.Executor.SkillMgr.ExecuteWithMeta(skillName, args)
	}
	// N12: /memory 命令回调（Plain/Bridge 模式）。TUI 模式由 app.go switch 直接处理。
	rc.CmdRegistry.OnMemory = func(args string) (string, error) {
		return HandleMemoryCommand(rc.MemoryStore, args)
	}

	// N26：初始化 goal.Manager（+ ，plain/bridge 路径）
	// compact 回调为 no-op；launchTurn 通过 print + 假调用模拟（plain/bridge 无 SendPrompt）；
	// hasUserInput 返回 false（plain/bridge 无 textarea）；collectEvidence 留空（评估 Agent 不强依赖）。
	if rc.AIClient != nil && rc.UsageTracker != nil {
		homeDir, _ := os.UserHomeDir()
		rc.goalManager = newGoalManager(
			homeDir,
			rc.AIClient,
			metrics.Default,
			rc.UsageTracker,
			nil, // sendPrompt=nil（plain/bridge 无 SendPrompt，launchTurn 返回错误）
			nil, // hasUserInput=nil（返回 false）
			nil, // collectEvidence=nil（返回空 Evidence）
		)
		// 注册 goal.Manager 为 TurnEndHook（每轮结束时触发评估 + 续跑，）
		if rc.goalManager != nil {
			rc.RegisterTurnEndHook(rc.goalManager)
		}
	}
	// N26：/goal 命令回调（单入口 + args 解析模式，）
	rc.CmdRegistry.OnGoal = func(args string) string {
		return rc.handleRunnerGoalCmd(args)
	}
}

// HandleMemoryCommand 处理 /memory 命令的共享逻辑，供 TUI（app.go）与 Plain/Bridge（runner_core）复用。
// 支持 list/edit/show/add/delete 五个子命令。store 为 nil 时返回错误。
func HandleMemoryCommand(store *config.MemoryStore, args string) (string, error) {
	if store == nil {
		return "", fmt.Errorf("memory store is not initialized")
	}

	parts := strings.Fields(args)
	if len(parts) == 0 {
		return handleMemoryListShared(store)
	}

	switch parts[0] {
	case "list":
		return handleMemoryListShared(store)
	case "edit":
		if len(parts) < 2 {
			return "", fmt.Errorf("usage: /memory edit <filename>")
		}
		if err := store.EditInEditor(parts[1]); err != nil {
			return "", err
		}
		return biSame("Memory edited: %s", "已编辑记忆: %s", parts[1]), nil
	case "show":
		if len(parts) < 2 {
			return "", fmt.Errorf("usage: /memory show <filename>")
		}
		entry, err := store.Read(parts[1])
		if err != nil {
			return "", err
		}
		return fmt.Sprintf("[%s] %s\n\n%s", entry.Type, entry.Name, entry.Content), nil
	case "add":
		if len(parts) < 3 {
			return "", fmt.Errorf("usage: /memory add <name> <content>")
		}
		name := parts[1]
		content := strings.Join(parts[2:], " ")
		entry := config.MemoryEntryV2{
			Name:        name,
			Description: truncateRunes(content, 80),
			Type:        config.MemoryTypeUser,
			Content:     content,
		}
		if err := store.Write(entry); err != nil {
			return "", err
		}
		return biSame("Memory saved: %s", "记忆已保存: %s", name), nil
	case "delete":
		if len(parts) < 2 {
			return "", fmt.Errorf("usage: /memory delete <filename>")
		}
		if err := store.Delete(parts[1]); err != nil {
			return "", err
		}
		return biSame("Memory deleted: %s", "记忆已删除: %s", parts[1]), nil
	default:
		return "", fmt.Errorf("unknown subcommand: %s\nusage: /memory [list|edit <filename>|show <filename>|add <name> <content>|delete <filename>]", parts[0])
	}
}

// handleMemoryListShared 列出所有记忆文件，按 UpdatedAt 降序（最新在前）。
func handleMemoryListShared(store *config.MemoryStore) (string, error) {
	entries, err := store.List()
	if err != nil {
		return "", err
	}
	if len(entries) == 0 {
		return bi("No memories yet\nUse /memory add <name> <content> to add one",
			"暂无记忆\n使用 /memory add <name> <content> 添加"), nil
	}
	var parts []string
	parts = append(parts, biSame("Memories (%d):", "记忆列表 (%d):", len(entries)))
	for _, e := range entries {
		freshness := config.MemoryFreshnessText(e.UpdatedAt)
		desc := truncateRunes(e.Description, 50)
		parts = append(parts, fmt.Sprintf("  [%s] %s — %s %s", e.Type, e.Name, desc, freshness))
	}
	parts = append(parts, bi("\nCommands: /memory [list|edit <filename>|show <filename>|add <name> <content>|delete <filename>]",
		"\n命令: /memory [list|edit <filename>|show <filename>|add <name> <content>|delete <filename>]"))
	return strings.Join(parts, "\n"), nil
}

// buildRequest builds AI request from current session
func (c *runnerCore) buildRequest() ai.Request {
	layers := c.buildSystemPrompt()
	messages := c.buildMessages()
	toolDefs := c.buildToolDefs()

	maxTokens := c.Budget.PostCompactMaxTokens()
	if maxTokens <= 0 {
		maxTokens = c.Config.ContextWindow - ctxpkg.RecoveryBudgetTokens
	}
	modelMaxOutput := ai.MaxOutputTokensForModel(c.ModelName)
	if maxTokens > modelMaxOutput {
		maxTokens = modelMaxOutput
	}

	req := ai.Request{
		SystemPrompt:       layerBlocksToStrings(layers),
		SystemPromptLayers: layerBlocksToComponents(layers),
		Messages:           messages,
		MaxTokens:          maxTokens,
		Tools:              toolDefs,
		Model:              c.ModelName,
		Thinking: &ai.ThinkingConfig{
			Enabled:      c.ThinkingEnabled,
			BudgetTokens: c.ThinkingBudgetTokens,
		},
	}
	c.requestSnapshot = snapshotRequest(
		c.Estimator, req, estimateSessionSystemPromptTokens(c.Estimator, c.Session.GetMessages()),
	)
	return req
}

func (c *runnerCore) handleContextInfo() string {
	cacheHitRate := 0.0
	if c.UsageTracker != nil {
		cacheHitRate = c.UsageTracker.CacheHitRate()
	}
	compactCount := 0
	if c.Session != nil {
		compactCount = c.Session.Metadata.CompactionCount
	}
	strategy := string(config.CompactBalanced)
	if c.Config != nil {
		strategy = string(c.Config.CompactionStrategy)
	}
	snapshot := c.requestSnapshot
	if snapshot.Valid && snapshot.Model != "" && snapshot.Model != c.ModelName {
		snapshot = contextRequestSnapshot{}
	}
	report := formatContextStatus(
		c.Session, c.Config, c.Estimator, snapshot,
		c.InputTokens, c.OutputTokens, c.RequestCount, compactCount, strategy, cacheHitRate, c.AICompactor != nil,
		c.TokenSource, c.ModelName,
	)
	// B-P4 可观测性：plain 模式 /context 同样展示 EMA 校准状态
	if c.TokenSource != nil && c.ModelName != "" {
		report += formatCalibrationStatus(c.TokenSource, c.ModelName)
	}
	return report
}

// buildSystemPrompt 构造分层 system prompt，委托 prompt.Builder.Build（N15）。
// 返回 []ai.PromptLayerBlock 供 buildRequest 填充 Request.SystemPromptLayers（N3 接入点）+ SystemPrompt（向后兼容）。
// 追加延迟工具目录层，让模型知道哪些工具可用但需通过 ToolSearch 加载（三模式一致）。
func (c *runnerCore) buildSystemPrompt() []ai.PromptLayerBlock {
	homeDir, _ := os.UserHomeDir()
	layers := c.PromptBuilder.Build(homeDir, c.FirstUserInput, c.ResponseLanguage)
	// 追加延迟工具目录（与 TUI buildSystemPrompt 保持一致）
	if c.Registry != nil {
		if dir := c.Registry.DeferredToolDirectory(); dir != "" {
			layers = append(layers, ai.PromptLayerBlock{
				Text:   dir,
				Stable: false,
				Layer:  ai.LayerSessionState,
			})
		}
	}
	return append(layers, sessionSystemPromptBlocks(c.Session.GetMessages())...)
}

// layerBlocksToStrings 将分层 block 转为字符串切片，填充 Request.SystemPrompt（向后兼容 N3 降级路径）。
func layerBlocksToStrings(blocks []ai.PromptLayerBlock) []string {
	result := make([]string, 0, len(blocks))
	for _, b := range blocks {
		if b.Text != "" {
			result = append(result, b.Text)
		}
	}
	return result
}

// layerBlocksToComponents 将分层 block 转为 PromptComponent 切片，填充 Request.SystemPromptLayers（N3 消费）。
// 方案B：保留 N3 已有的 []PromptComponent 字段类型，通过转换桥接 N15 的 []PromptLayerBlock 产出。
func layerBlocksToComponents(blocks []ai.PromptLayerBlock) []ai.PromptComponent {
	result := make([]ai.PromptComponent, 0, len(blocks))
	for _, b := range blocks {
		if b.Text == "" {
			continue
		}
		result = append(result, ai.PromptComponent{
			Name:    b.Layer.String(),
			Content: b.Text,
			Stable:  b.Stable,
			Layer:   b.Layer,
		})
	}
	return result
}

// buildMessages builds message list for API
func (c *runnerCore) buildMessages() []ai.MessageEntry {
	return sessionMessagesToAPI(c.Session.GetMessages())
}

// buildToolDefs 构造 tools 数组（F1 修复：延迟工具加载，三模式一致）。
// 使用 AllDirect() 只加载 ExposureDirect 工具 + 已加载的延迟工具。
// 与 stream.go Model.buildToolDefs 逻辑保持同步，确保 TUI/PlainRunner/Bridge 三模式一致。
func (c *runnerCore) buildToolDefs() []ai.ToolDefinition {
	specs := c.Registry.AllDirect()
	// 合并已加载的延迟工具 schema
	if c.Executor != nil {
		for _, name := range c.Executor.LoadedDeferredToolNames() {
			if spec, ok := c.Registry.Get(name); ok {
				specs = append(specs, spec)
			}
		}
	}
	defs := make([]ai.ToolDefinition, 0, len(specs))
	for _, spec := range specs {
		defs = append(defs, ai.ToolDefinition{
			Name:        spec.Name,
			Description: spec.Description,
			// P0-2：发给模型的 schema 走有界投影（内建不超预算=恒等；外部 MCP schema 降损）。
			// 本体 spec.InputSchema 不受影响，仍供 SchemaValidateStage 校验参数。
			InputSchema: tools.CompactSchemaForModel(spec.InputSchema),
		})
	}
	return defs
}

// handleCompact handles compaction command
//
// N11 修复（G1 + G3 + G10 接线）：
// - G1：构造 CompactCostRecorder 并注入到 AICompactor，使 compact 的 AI 调用
// usage 走 N2 metrics.RecordUsage + N1 UsageTracker.RecordAgentUsage
// （AgentID="compact"）★ D 批：此处原先还记一笔本地估算金额，已删。
// - G3：注入 CachingIntegration 到 CompactionPipeline，使 compact 后调
// NotifyCacheDeletion/NotifyCompaction 重建 cache（复用 N3 机制）
// - G10：构造 pipeline 后检查 AICompactor.circuitBreaker.IsOpen()，
// 打开时通过返回值显示 WarningMessage 告警
func (c *runnerCore) handleCompact(args string) string {
	// /compact purge — 删除当前会话的压缩归档（归档式压缩，2026-09-07）。
	// 与 TUI Model.handleCompact 共用 handleCompactPurgeShared，保证两条
	// 生产路径行为一致（TestPlainRunnerHandleCompactPurgeRouting 锁定）。
	if args == "purge" {
		return handleCompactPurgeShared(c.Store, c.SessionName)
	}
	return c.handleCompactWithTrigger(args, ctxpkg.CompactionTriggerManual, ctxpkg.CompactionPolicyForce)
}

func (c *runnerCore) handleCompactWithTrigger(args string, trigger ctxpkg.CompactionTrigger, policy ctxpkg.CompactionPolicy) string {
	strategy := config.CompactBalanced
	if c.Config != nil {
		strategy = c.Config.CompactionStrategy
	}
	compactConfig, usage := parseManualCompactConfig(args, strategy)
	if usage != "" {
		return usage
	}
	compactConfig.Trigger = trigger
	staticOverhead, systemOverhead := c.compactRequestOverheads(c.Budget)
	compactConfig = compactConfig.WithBudgetAndOverhead(c.Budget, staticOverhead)
	compactConfig.SystemMessageOverheadTokens = systemOverhead
	expectedVersion, expectedRevision, err := c.captureCompactState()
	if err != nil {
		return fmt.Sprintf("Compaction failed: %v", err)
	}

	// Recorder belongs to this operation and is passed explicitly into either
	// compact path. Shared compactor/pipeline instances never retain it.
	costRecorder := newCompactCostRecorder(c.ModelName, c.UsageTracker)
	compactSessionEpoch := c.sessionEpoch.Load()
	compactModel := c.ModelName
	compactCacheEpoch := cacheCapabilityEpoch(c.Caching)
	defer c.applyCompactRecorderUsage(costRecorder, compactSessionEpoch, compactModel, compactCacheEpoch)
	// P0-1 (X1) + N11 G9：5 层 CompactionPipeline 默认启用
	// （WHALE_CTX_PIPELINE=0 可显式关闭回退旧路径）
	if ctxpkg.IsPipelineEnabled() && c.Estimator != nil && c.AIClient != nil {
		// N11 修复 N1：复用持久 Pipeline，断路器状态跨调用保留。
		// 原实现每次 handleCompact 新建 pipeline → CircuitBreaker 状态重置 →
		// CB 形同虚设。改为持久字段后，连续 compact 失败 3 次断路器正确打开。
		// 首次调用时创建并注入所有依赖，后续调用直接复用。
		if c.Pipeline == nil {
			c.Pipeline = ctxpkg.NewCompactionPipeline(c.Estimator, c.AIClient)
			if c.CollapseMgr != nil {
				c.Pipeline.SetCollapseManager(c.CollapseMgr)
			}
			// 方案 D：注入 CacheBreakHandler，断裂时压缩降级为 inner.Compact
			if c.CacheBreakHandler != nil {
				c.Pipeline.SetCacheBreakHandler(c.CacheBreakHandler)
			}
			// G3 修复：注入 CachingIntegration，使 compact 后 cache 重建复用 N3 机制
			// （NotifyCacheDeletion + NotifyCompaction，02 §2.1.3 契约）
			if c.Caching != nil {
				c.Pipeline.SetCachingIntegration(c.Caching)
			}
		}
		// G10 修复：检查断路器是否打开，打开时显示告警。
		// 借鉴同类 CLI MAX_CONSECUTIVE_AUTOCOMPACT_FAILURES=3
		// 的静默停止机制，whale-code 在静默停止的同时向用户显示告警。
		// N1 修复后 pipeline 为持久实例，CircuitBreakerWarning() 跨调用累积。
		warningPrefix := c.Pipeline.CircuitBreakerWarning()

		// N2 修复：从 CachingIntegration 获取最近一次 API 响应的 CacheInfo，
		// 传入 pipeline 使 CacheAwareMicroCompact 在生产路径生效。
		// 原实现传 nil → G5 cache-aware 功能始终降级为普通 MicroCompact。
		var cacheInfo *ai.CacheInfo
		if c.Caching != nil {
			cacheInfo = c.Caching.GetCacheInfo()
		}

		result := c.Pipeline.CompactViaPipelineWithOptions(
			c.Session.GetMessages(),
			c.Budget,
			cacheInfo,
			ctxpkg.ExecuteOptions{
				Trigger:      trigger,
				Policy:       policy,
				Config:       compactConfig,
				CostRecorder: costRecorder,
			},
		)
		if !result.Changed {
			// G10 修复：compact 未生效时，附加断路器告警（若有）
			if warningPrefix != "" {
				return warningPrefix + "\n" + formatCompactResult(result, compactConfig)
			}
			return formatCompactResult(result, compactConfig)
		}
		if err := c.commitCompactResult(result, compactConfig, c.Pipeline, expectedVersion, expectedRevision); err != nil {
			return fmt.Sprintf("Compaction failed: %v", err)
		}
		if !result.Changed {
			return formatCompactResult(result, compactConfig)
		}
		if warningPrefix != "" {
			return warningPrefix + "\n" + formatCompactResult(result, compactConfig)
		}
		return formatCompactResult(result, compactConfig)
	}

	// Old single-stage path (default, backward-compatible)
	microConfig := ctxpkg.DefaultMicroCompactConfig()
	originalMessages := c.Session.GetMessages()
	microMessages := ctxpkg.MicroCompact(originalMessages, microConfig)
	microChanged := !ctxpkg.MessagesEqual(originalMessages, microMessages)
	var result *ctxpkg.CompactResult
	aiRequestedButUnavailable := compactConfig.UseAISummary && (c.AICompactor == nil || c.AIClient == nil)
	if compactConfig.UseAISummary && !aiRequestedButUnavailable {
		result = c.AICompactor.CompactWithCostRecorder(microMessages, compactConfig, costRecorder)
	} else {
		result = c.Compactor.Compact(microMessages, compactConfig)
	}

	result.Changed = result.Changed || microChanged
	if !result.Changed {
		formatted := formatCompactResult(result, compactConfig)
		if aiRequestedButUnavailable {
			return "AI summary unavailable; used deterministic fallback.\n" + formatted
		}
		return formatted
	}

	if err := c.commitCompactResult(result, compactConfig, nil, expectedVersion, expectedRevision); err != nil {
		return fmt.Sprintf("Compaction failed: %v", err)
	}
	if !result.Changed {
		return formatCompactResult(result, compactConfig)
	}

	formatted := formatCompactResult(result, compactConfig)
	if aiRequestedButUnavailable {
		return "AI summary unavailable; used deterministic fallback.\n" + formatted
	}
	return formatted
}

// handleSummaryShared 生成基于统计的对话摘要（B7-P1-1 修复）。
// 对齐 TUI Model.handleSummary 的统计逻辑（主题/持续时间/轮次/消息计数/涉及文件），
// 供 PlainRunner 和 Bridge 模式通过 OnSummary 回调复用，避免假成功。
// 不触发 AI 请求，仅基于会话元数据生成摘要文本。
func (c *runnerCore) handleSummaryShared() string {
	msgs := c.Session.GetMessages()
	if len(msgs) < 2 {
		return "📝 对话内容不足，无法生成摘要\n（至少需要 2 条消息）"
	}

	var sb strings.Builder
	sb.WriteString("📝 对话摘要\n\n")

	topic := c.SessionName
	if topic == "" && c.FirstUserInput != "" {
		if len(c.FirstUserInput) > 60 {
			topic = c.FirstUserInput[:60] + "..."
		} else {
			topic = c.FirstUserInput
		}
	}
	if topic == "" {
		topic = "(未命名)"
	}
	sb.WriteString(fmt.Sprintf("主题: %s\n", topic))

	if c.Session.Metadata.CreatedAt > 0 {
		created := time.UnixMilli(c.Session.Metadata.CreatedAt)
		duration := time.Since(created)
		sb.WriteString(fmt.Sprintf("持续时间: %v\n", duration.Round(time.Second)))
	}

	turnCount := c.Session.Metadata.TurnCount
	sb.WriteString(fmt.Sprintf("轮次: %d\n\n", turnCount))

	userMsgCount := 0
	assistantMsgCount := 0
	toolCallCount := 0
	filesSeen := make(map[string]bool)

	for _, msg := range msgs {
		role := string(msg.Role)
		switch role {
		case "user":
			userMsgCount++
		case "assistant":
			assistantMsgCount++
		case "tool":
			toolCallCount++
		}
		for _, block := range msg.Blocks {
			contentStr := block.Text
			for _, ext := range []string{".go", ".ts", ".js", ".py", ".rs", ".java", ".md", ".json", ".yaml", ".yml"} {
				if strings.Contains(contentStr, ext) {
					idx := strings.LastIndex(contentStr, ext)
					if idx >= 0 {
						start := strings.LastIndex(contentStr[:idx], string(os.PathSeparator))
						if start == -1 {
							start = strings.LastIndex(contentStr[:idx], "/")
						}
						if start == -1 {
							start = strings.LastIndex(contentStr[:idx], "\"")
						}
						if start == -1 {
							start = strings.LastIndex(contentStr[:idx], " ")
						}
						if start >= 0 && start < idx {
							fileName := contentStr[start+1 : idx+len(ext)]
							filesSeen[fileName] = true
						}
					}
				}
			}
		}
	}

	sb.WriteString(fmt.Sprintf("消息统计:\n 用户: %d | AI: %d | 工具: %d\n", userMsgCount, assistantMsgCount, toolCallCount))

	if len(filesSeen) > 0 {
		sb.WriteString("\n涉及文件:\n")
		for f := range filesSeen {
			sb.WriteString(fmt.Sprintf(" • %s\n", f))
		}
	}

	return sb.String()
}

// buildSecurityReviewPrompt 根据参数构建安全审查的 AI 提示词（B7-P1-2 修复）。
// 对齐 TUI Model.handleSecurityReview 的提示词构建逻辑：
// 优先通过 OnSkillExecute 回调加载 security-review skill 模板，
// 回退到内置简化模板。返回完整的 reviewPrompt 供调用方注入会话并触发 AI 流。
func (c *runnerCore) buildSecurityReviewPrompt(args string) string {
	args = strings.TrimSpace(args)

	var scopeDesc string
	switch {
	case args == "full":
		scopeDesc = "Please perform a comprehensive security review of the entire project. Scan all source code files, checking the following security vulnerability categories:\n- Input validation vulnerabilities (SQL injection, command injection, path traversal, etc.)\n- Authentication and authorization issues (auth bypass, privilege escalation, session management, etc.)\n- Cryptography and key management (hardcoded keys, weak encryption algorithms, etc.)\n- Injection and code execution (deserialization RCE, eval injection, XSS, etc.)\n- Data exposure (sensitive data leaks, PII handling violations, etc.)"
	case args == "staged":
		scopeDesc = "Please perform a security review of the staged but uncommitted code changes in git diff --staged. First execute git diff --staged to get the change content, then analyze following the standard security review process."
	case args != "":
		scopeDesc = fmt.Sprintf("Please perform a targeted security review of file %s. Carefully read the complete content of this file and perform a comprehensive analysis following the standard security review process.", args)
	default:
		scopeDesc = "Please perform a security review of code changes on the current branch relative to origin/HEAD. First execute the following commands to get change content:\n1. git status — view repository status\n2. git diff --name-only origin/HEAD... — get changed file list\n3. git log --no-decorate origin/HEAD... — view commit history\n4. git diff origin/HEAD... — get full diff content\n\nThen perform a comprehensive analysis of the diff code changes following the standard security review process."
	}

	var reviewPrompt string
	if c.CmdRegistry.OnSkillExecute != nil {
		skillPrompt := c.CmdRegistry.OnSkillExecute("security-review", scopeDesc)
		if skillPrompt != "" {
			reviewPrompt = skillPrompt
		}
	}

	if reviewPrompt == "" {
		reviewPrompt = fmt.Sprintf(`You are a senior security engineer. Please perform a security review of the following code changes.

%s

Please check the following categories:
1. Input validation vulnerabilities (SQL injection, command injection, XXE, template injection, NoSQL injection, path traversal)
2. Authentication and authorization issues (auth bypass, privilege escalation, session management, JWT vulnerabilities)
3. Cryptography and key management (hardcoded keys/passwords/tokens, weak encryption algorithms)
4. Injection and code execution (deserialization RCE, eval injection, XSS)
5. Data exposure (sensitive data logging, PII violations, API data leaks)

Output format:
- Each vulnerability includes: file path, line number, severity level (HIGH/MEDIUM/LOW), category, description, exploit scenario, fix recommendation
- Only report vulnerabilities with confidence ≥ 80%%
- Exclude DOS, outdated third-party libraries, test-only files, and documentation file issues`, scopeDesc)
	}

	return reviewPrompt
}

// handleCompactStrategyShared 切换压缩策略并持久化到 settings.json（B7-P1-6 修复）。
// 对齐 TUI Model.handleCompactStrategy 的逻辑，供 PlainRunner 和 Bridge 模式通过
// OnCompactStrategy 回调复用。原桩层返回 "switched to: <args>" 但不持久化（假成功），
// 修复后实际更新 Config.CompactionStrategy 并调用 config.SaveToFile 持久化。
func (c *runnerCore) handleCompactStrategyShared(strategy string) string {
	strategyDescriptions := map[config.CompactionStrategy]string{
		config.CompactBalanced:     "Balanced — Keep recent messages + AI summary",
		config.CompactPreserveUser: "Preserve User — Prioritize user intent",
		config.CompactAggressive:   "Aggressive — Max compression, keep last 3 messages only",
	}

	if strategy == "" {
		desc := strategyDescriptions[c.Config.CompactionStrategy]
		return fmt.Sprintf("Current compaction strategy: %s\nDescription: %s\nAvailable: balanced, preserve-user, aggressive", displayCompactionStrategy(string(c.Config.CompactionStrategy)), desc)
	}

	var target config.CompactionStrategy
	switch strategy {
	case "balanced":
		target = config.CompactBalanced
	case "preserve-user":
		target = config.CompactPreserveUser
	case "aggressive":
		target = config.CompactAggressive
	default:
		return fmt.Sprintf("Invalid strategy: %s\nAvailable: balanced, preserve-user, aggressive", strategy)
	}

	c.Config.CompactionStrategy = target

	// 持久化到 ~/.whale/settings.json（与 Bridge OnOutputStyle 等回调的持久化路径一致）
	settingsPath := filepath.Join(whalehome.Dir(), "settings.json")
	if err := config.SaveToFile(c.Config, settingsPath); err != nil {
		return fmt.Sprintf("Compaction strategy switched to: %s（persist failed: %v）", displayCompactionStrategy(string(target)), err)
	}

	desc := strategyDescriptions[target]
	return fmt.Sprintf("Compaction strategy switched to: %s（%s）", displayCompactionStrategy(string(target)), desc)
}

// autoNameSession 给会话打上身份：UUID 主键 + 从首条输入派生的可读别名。
//
// 改造前这里生成的是「<首条输入前10字>_<秒级时间戳>」并直接当文件名，
// 于是两个实例在同一秒内首句相同时会落到同一个文件（撞名），历史互相混写。
// 现在文件名是 UUID，别名另行持久化（见 makeSessionIdentity）。
//
// 幂等守卫（同 Model.autoNameSession）：主键是 UUID ⇒ 重复调用会铸出新身份，
// 把刚写的会话文件变成孤儿。现有调用点都已有 `if SessionName == ""` 外判，
// 守卫把前提从「调用方记得」变成「结构上不可能违反」。
func (c *runnerCore) autoNameSession() {
	if c.SessionName != "" {
		return
	}
	id, alias := makeSessionIdentity(c.FirstUserInput)
	if id == "" {
		return
	}
	c.SessionName = id
	c.Session.Metadata.Name = alias
}

// autoSnapshotOnTurnEnd 每轮对话结束时自动创建 FileHistory 快照（修复）。
// 对齐开发 prompt 改动 6 要求："每轮对话结束调 Snapshot（双写期，checkpoint 并行运行）"。
// 快照包含 sessionMsgCount，确保 Rewind 会话恢复不因缺少 snapshot 记录而失败。
// 失败不影响旧路径（非致命），仅日志告警。
func (c *runnerCore) autoSnapshotOnTurnEnd() {
	if c.Executor == nil {
		return
	}
	fh := c.Executor.FileHistoryGetter()
	if fh == nil {
		return
	}
	msgId := c.Executor.CurrentMessageId()
	msgCount := len(c.Session.GetMessages())
	// 落盘标签 = 会话人话名（别名），不是会话主键 —— 的写侧修复。
	alias := ""
	if c.Session != nil {
		alias = c.Session.Metadata.Name
	}
	label := snapshotLabelForStorage(alias, c.SessionName)
	if _, err := fh.Snapshot(msgId, label, msgCount); err != nil {
		log.Printf("[filehistory] auto snapshot on turn end failed (non-fatal): %v", err)
	}
}

// RegisterTurnEndHook 注册 turn 结束钩子（N26）。
// 钩子在每轮对话结束时（autoSnapshotOnTurnEnd 之后）按注册顺序调用。
// goal.Manager 通过此方法注入到 runnerCore，实现 OnTurnEnd 回调桥接。
// 非线程安全，仅在初始化期调用（main.go / newGoalManager 接线时）。
func (c *runnerCore) RegisterTurnEndHook(h TurnEndHook) {
	c.turnEndHooks = append(c.turnEndHooks, h)
}

// runTurnEndHooks 执行所有注册的 turn 结束钩子（N26）。
// 在 autoSnapshotOnTurnEnd 之后调用，确保快照先完成（goal Evidence 收集依赖快照）。
// 单个钩子错误仅记录日志，不阻塞后续钩子或主流程（运行时冲突矩阵第 5 行）。
// D 批：goal.TurnResult.CostCents 已随成本线删除（该字段在生产里
// 本来就从未被赋过值 —— 恒为 0，接它的那条成本熔断因此永远不会响）。
func (c *runnerCore) runTurnEndHooks(ctx context.Context) {
	if len(c.turnEndHooks) == 0 {
		return
	}
	threadID := c.SessionName
	turnResult := goal.TurnResult{
		ThreadID:   threadID,
		TokensUsed: int64(c.InputTokens + c.OutputTokens),
	}
	for _, h := range c.turnEndHooks {
		if err := h.OnTurnEnd(ctx, threadID, turnResult); err != nil {
			log.Printf("[tui] turn end hook failed (non-fatal): %v", err)
		}
	}
}

// saveSession incrementally saves session, returns error for caller to handle
//
// A6 顺带修根（本次引入的回归）：A6 让 `guard == nil` 也走 renderGuardStop，
// 而 renderGuardStop 会无条件调本方法 ⇒ 最小化装配的 runner（零值 Store）
// 由「静默放行」变成「停机时 panic」。停机/收尾路径**不得**比被拒行为更危险。
// 根因 = 本方法是 Store 的唯一解引用点却无守卫；持久化是 best-effort
// （调用方普遍 `_ = r.saveSession()`），故与 LoadSession / captureCompactState
// 同口径：未装配 Store 即返回 error，不 panic。
func (c *runnerCore) saveSession() error {
	if c.Session == nil {
		return fmt.Errorf("saveSession: no active session")
	}
	if c.Store == nil {
		return fmt.Errorf("saveSession: session store not initialized")
	}

	if c.SessionName == "" {
		c.autoNameSession()
		if c.SessionName == "" {
			c.SessionName = session.GenerateID()
		}
	}

	if c.SavedMsgCount < len(c.Session.GetMessages()) {
		pending := c.Session.GetMessages()[c.SavedMsgCount:]
		if len(pending) > 0 {
			// 别名随首批消息写入文件头（见 Store.AppendBatchNamed）：
			// 会话主键是 UUID，别名不落盘就只剩一串 hex 可看。
			alias := ""
			if c.Session != nil {
				alias = c.Session.Metadata.Name
			}
			if err := c.Store.AppendBatchNamed(c.SessionName, alias, pending); err != nil {
				fmt.Fprintf(os.Stderr, "[saveSession] AppendBatch failed for session %s: %v\n", c.SessionName, err)
				return fmt.Errorf("saveSession: AppendBatch failed for session %s: %w", c.SessionName, err)
			}
			c.SavedMsgCount = len(c.Session.GetMessages())
		}
	}
	return nil
}

// executeToolsAndContinue executes tool calls and continues the AI stream
// resultCallback is called for each tool result to allow custom output (console print, JSON-RPC send, etc.)
// onConfirm is an optional callback for Pipeline safety confirmation
func (c *runnerCore) executeToolsAndContinue(
	pendingTools []session.ContentBlock,
	resultCallback func(res tools.Result),
	onConfirm func(toolName, input string) bool,
) {
	// build tool calls
	calls := make([]tools.Call, 0, len(pendingTools))
	for _, pt := range pendingTools {
		calls = append(calls, tools.Call{
			ID:    pt.ID,
			Name:  pt.Name,
			Input: pt.Input,
		})
	}

	var results []tools.Result

	if c.Executor.Pipeline != nil {
		// Pipeline path: execute tools one by one with confirmation
		if onConfirm != nil {
			c.Executor.OnConfirm = onConfirm
		}
		results = make([]tools.Result, 0, len(pendingTools))
		for _, call := range calls {
			pr := c.Executor.ExecuteWithConfirm(context.Background(), call)
			if !pr.Proceed && !pr.NeedConfirm {
				// 收口到 tools.DenialResult（唯一实现点）。
				//
				// 修复前这里是本地拼的三行，把"管线拒绝"**无条件**写成
				// `"user cancelled tool execution"` —— 管线在确认之前短路时
				// 用户连框都没见过 ⇒ 谎称"用户取消"，且 FailureKind 被丢弃
				// （非交互路径上 Terminal() 恒假 ⇒ 模型可在同一条 deny 规则下
				// 换工具重试）。`tui/plain.go` 有**逐字相同**的一份，两处同批改。
				results = append(results, tools.DenialResult(call.ID, call.Name, pr))
				if resultCallback != nil {
					resultCallback(results[len(results)-1])
				}
				continue
			}
			if pr.Proceed && pr.Result != nil {
				results = append(results, *pr.Result)
			} else if !pr.Proceed {
				results = append(results, tools.Result{
					ID:      call.ID,
					Name:    call.Name,
					Output:  fmt.Sprintf("⚠️ Pipeline Rejected: %s", pr.Rejection),
					IsError: true,
				})
			} else {
				results = append(results, tools.Result{
					ID:      call.ID,
					Name:    call.Name,
					Output:  "⚠️ Pipeline execution returned no result",
					IsError: true,
				})
			}
			if resultCallback != nil {
				resultCallback(results[len(results)-1])
			}
		}
	} else {
		// Fallback path: batch execution
		results = c.Executor.ExecuteBatch(context.Background(), calls)
		if resultCallback != nil {
			for _, res := range results {
				resultCallback(res)
			}
		}
	}

	// convert results to tool messages
	toolMessages := make([]session.Message, 0, len(results))
	for _, res := range results {
		toolMessages = append(toolMessages,
			session.ToolResult(res.ID, res.Name, res.Output, res.IsError))
	}

	c.Session.SetMessages(append(c.Session.GetMessages(), toolMessages...))
	c.setState(stateStreaming)
}

// estimateCurrentTokens estimates current token count for all content blocks
func (c *runnerCore) estimateCurrentTokens() int {
	total := 0
	for _, msg := range c.Session.GetMessages() {
		for _, block := range msg.Blocks {
			switch block.Type {
			case "text", "thinking":
				total += len(block.Text) / 4 // ~4 chars = 1 token
			case "tool_use":
				total += len(block.Text)/4 + len(block.Input)/4
			case "tool_result":
				total += len(block.Output) / 4
			case "image":
				// Image token estimation: base64字符数 × 0.125
				if len(block.Base64Data) > 0 {
					total += imgproc.EstimateTokens(block.Base64Data)
				}
			}
		}
	}
	return total
}

func (c *runnerCore) estimateCurrentInputContextTokens() int {
	snapshot := c.requestSnapshot
	if snapshot.Valid && snapshot.Model != "" && snapshot.Model != c.ModelName {
		snapshot = contextRequestSnapshot{}
	}
	return estimateCurrentInputContext(c.Session, c.Estimator, snapshot).Total
}

// friendlyError converts errors to user-friendly messages
func (c *runnerCore) friendlyError(err error) string {
	return formatFriendlyError(err)
}

// formatFriendlyError 将错误转换为用户友好的消息。
// 优先基于 ErrorID 驱动显示，ErrorID 为空时降级为字符串匹配。
func formatFriendlyError(err error) string {
	if err == nil {
		return ""
	}

	errorID := ai.GetErrorID(err)
	if errorID != "" {
		return errorMessageByID(errorID, err)
	}

	msg := err.Error()

	// P1-26 修复 + 5维度审查优化：根据已设置的 API Key 推断 Provider，动态选择 BASE_URL 环境变量名
	// 避免 Anthropic 用户看到 OPENAI_BASE_URL 提示被误导（三模型统一）
	// 5维度审查修复：补充 GEMINI/DEEPSEEK 推断，避免误提示 ANTHROPIC_BASE_URL
	baseURLEnv := "ANTHROPIC_BASE_URL" // 默认 Anthropic（whale-code 默认推荐 Provider）
	if os.Getenv("OPENAI_API_KEY") != "" && os.Getenv("ANTHROPIC_API_KEY") == "" {
		baseURLEnv = "OPENAI_BASE_URL"
	} else if os.Getenv("GEMINI_API_KEY") != "" && os.Getenv("ANTHROPIC_API_KEY") == "" && os.Getenv("OPENAI_API_KEY") == "" {
		baseURLEnv = "GEMINI_BASE_URL"
	} else if os.Getenv("DEEPSEEK_API_KEY") != "" && os.Getenv("ANTHROPIC_API_KEY") == "" && os.Getenv("OPENAI_API_KEY") == "" && os.Getenv("GEMINI_API_KEY") == "" {
		baseURLEnv = "DEEPSEEK_BASE_URL"
	}

	if strings.Contains(msg, "lookup ") || strings.Contains(msg, "no such host") {
		return fmt.Sprintf("DNS resolution failed: %s\nPossible causes:\n  - No internet connection\n  - DNS server unreachable\n  - VPN/proxy not configured\nYour %s: %s", msg, baseURLEnv, os.Getenv(baseURLEnv))
	}
	if strings.Contains(msg, "connection refused") {
		return fmt.Sprintf("Connection refused: %s\nCheck %s: %s", msg, baseURLEnv, os.Getenv(baseURLEnv))
	}
	if strings.Contains(msg, "i/o timeout") || strings.Contains(msg, "deadline exceeded") {
		return fmt.Sprintf("Request timed out: %s\nThe API server did not respond in time.", msg)
	}
	if strings.Contains(msg, "TLS handshake") || strings.Contains(msg, "certificate") {
		return fmt.Sprintf("TLS/SSL error: %s\nThis is usually a proxy or firewall issue.", msg)
	}
	if strings.Contains(msg, "connection reset") || strings.Contains(msg, "EOF") {
		return fmt.Sprintf("Connection was reset: %s\nThe server may have terminated the connection unexpectedly.", msg)
	}

	if strings.Contains(msg, "401") || strings.Contains(msg, "Unauthorized") {
		return fmt.Sprintf("Authentication failed: Invalid API key.\nCheck your API key environment variable (ANTHROPIC_API_KEY or OPENAI_API_KEY).")
	}
	if strings.Contains(msg, "403") || strings.Contains(msg, "Forbidden") {
		return fmt.Sprintf("Access denied: Your API key doesn't have permission for this operation.")
	}
	if strings.Contains(msg, "429") || strings.Contains(msg, "rate") {
		return fmt.Sprintf("Rate limit exceeded: %s\nPlease wait and try again.", msg)
	}
	if statusCode500Re.MatchString(msg) || strings.Contains(msg, "Internal Server Error") {
		return fmt.Sprintf("API server error: The AI service is experiencing issues. Please try again later.")
	}
	if strings.Contains(msg, "502") || strings.Contains(msg, "503") || strings.Contains(msg, "529") {
		return fmt.Sprintf("Service unavailable: The AI service is temporarily overloaded. Retrying...")
	}

	if strings.Contains(msg, "invalid character") && strings.Contains(msg, "json") {
		return fmt.Sprintf("Failed to parse AI response: The model returned malformed JSON.\nThis is usually a temporary issue, please try again.")
	}
	if strings.Contains(msg, "unexpected end of JSON input") {
		return fmt.Sprintf("Incomplete response from AI: The response was truncated.\nTry reducing the context size with /compact.")
	}

	if strings.Contains(msg, "context") && strings.Contains(msg, "overflow") {
		return fmt.Sprintf("Context window exceeded: The conversation is too long.\nUse /compact to reduce context, or increase context_window in settings.")
	}
	if strings.Contains(msg, "token") && strings.Contains(msg, "limit") {
		return fmt.Sprintf("Token limit reached: The output is too long.\nTry breaking your request into smaller parts.")
	}

	if strings.Contains(msg, "permission denied") {
		return fmt.Sprintf("Permission denied: %s\nCheck file permissions or try running with appropriate privileges.", msg)
	}
	if strings.Contains(msg, "no such file") || strings.Contains(msg, "not found") {
		return fmt.Sprintf("File not found: %s\nVerify the file path is correct.", msg)
	}

	return fmt.Sprintf("Error: %s", msg)
}

// errorMessageByID 根据 ErrorID 返回用户友好的错误消息和建议动作。
func errorMessageByID(id ai.ErrorID, err error) string {
	msg := err.Error()
	switch id {
	case ai.ErrAIApiOverload:
		return fmt.Sprintf("[AI-API-OVERLOAD] AI 服务过载（529）\n建议：等待几秒后重试，系统已自动尝试降级到备用模型。")
	case ai.ErrAIApiRateLimit:
		return fmt.Sprintf("[AI-API-RATELIMIT] 请求频率超限（429）\n建议：请稍后再试，或检查 API 密钥配额。")
	case ai.ErrAIApiServer:
		return fmt.Sprintf("[AI-API-SERVER] AI 服务端错误（5xx）\n建议：系统将自动重试，请稍后观察。")
	case ai.ErrAIRetryable:
		return fmt.Sprintf("[AI-RETRYABLE] 可重试错误\n建议：系统将自动重试。")
	case ai.ErrAIFatal:
		return fmt.Sprintf("[AI-FATAL] 不可恢复错误\n%s\n建议：检查 API 密钥和权限设置。", msg)
	case ai.ErrSecurityBlocked:
		return fmt.Sprintf("[SECURITY-BLOCKED] 安全拦截\n建议：检查请求内容是否包含敏感信息。")
	case ai.ErrSecuritySandbox:
		return fmt.Sprintf("[SECURITY-SANDBOX] 沙箱安全限制\n建议：检查操作是否在允许范围内。")
	case ai.ErrSecretLeak:
		return fmt.Sprintf("[SECRET-LEAK] 密钥泄漏检测\n建议：立即检查并轮换相关密钥。")
	case ai.ErrSecretMissing:
		return fmt.Sprintf("[SECRET-MISSING] 密钥缺失\n建议：配置必要的 API 密钥环境变量。")
	case ai.ErrAgentTimeout:
		return fmt.Sprintf("[AGENT-TIMEOUT] 代理执行超时\n建议：简化任务或增加超时时间。")
	case ai.ErrAgentRecursionLimit:
		return fmt.Sprintf("[AGENT-RECURSION] 代理递归深度超限\n建议：检查任务是否存在无限循环。")
	case ai.ErrBridgeJWT:
		return fmt.Sprintf("[BRIDGE-JWT] Bridge JWT 认证失败\n建议：检查 JWT 令牌配置。")
	case ai.ErrBridgeReconnect:
		return fmt.Sprintf("[BRIDGE-RECONNECT] Bridge 重连失败\n建议：检查网络连接和 Bridge 服务状态。")
	default:
		return fmt.Sprintf("[%s] %s", id, msg)
	}
}

// Close cleans up resources held by the runner core
func (c *runnerCore) Close() {
	// 取消并等待 H11N27 memory 接线启动的 ForgettingScanner goroutine，避免泄漏和退出写盘竞态。
	// TUI 模式的 wiringCancel 由 Model.wiringCancel 管理，此处仅对 PlainRunner/BridgeRunner 生效。
	if c.wiringCancel != nil {
		c.wiringCancel()
	}
	if c.LifecycleHandler != nil {
		c.LifecycleHandler.Stop()
	}
	if c.Executor != nil {
		c.Executor.Close()
	}
	if c.AIClient != nil {
		c.AIClient.Close()
	}
}

// initProductionWiring 在 PlainRunner/BridgeRunner 初始化时完成生产环境安全与 memory 接线。
//
// 等价于 TUI 模式 lifecycle.go 中的 4 步初始化：
// 1. security.Init — 初始化全局命令安全校验器（Checker 单例），未初始化时 CheckCommand fail-closed
// 2. sandbox.SetEnabled(true) — 启用 sandbox Layer 2（命令黑名单/网络拦截/路径检查）
// 3. wireH11N27Memory — 接线 H11N27 6 接口（verification/capability/forgetting 文件持久化）
// 4. wireExecutorMemoryCallbacks — 接线 Executor.CapabilityLookup/ReflectTrigger 函数字段
//
// 修复问题：§4.1 非 TUI 模式 memory 接线缺失 + §4.5 sandbox Layer 2 非 TUI 路径未启用。
// 修复前 PlainRunner/BridgeRunner 路径 security.CheckCommand fail-closed（未 Init），
// sandbox Layer 2 失效（未 SetEnabled），memory 走内存 map stub（未 wireH11N27Memory）。
//
// 时序约束：必须在 registerCommonCommands 之后调用（wireH11N27Memory 要求 OnMemory 已接线）。
// 降级策略：各步失败均 warn 不阻塞，下游走对应降级路径（与 TUI 模式一致）。
//
// 参数：
// - cfg：配置实例
// - client：AI SimpleClient（用于 aiCallerAdapter，nil 时跳过 SetAICaller）
// - core：runnerCore 实例（提供 Executor.Sandbox/CmdRegistry/Policy）
// - homeDir：用户主目录（~/.whale/memory/_user/）
// - workDir：工作目录（~/.whale/memory/{hash}/）
//
// 返回：context.CancelFunc 兼容签名的清理函数；调用方在 Close 时调用，按 cancel→wait 收敛 ForgettingScanner。
func initProductionWiring(cfg *config.Config, client *ai.SimpleClient, core *runnerCore, homeDir, workDir string) context.CancelFunc {
	// 1. 初始化全局命令安全校验器（等价 lifecycle.go:360）。
	// 未初始化时 security.CheckCommand fail-closed 返回 Deny，导致 hook/monitor/命令校验全部拒绝。
	if core.Executor != nil && core.Executor.Sandbox != nil {
		security.Init(core.Executor.Sandbox, validator.NewValidator(), core.Policy)
	} else {
		fmt.Fprintf(os.Stderr, "[wiring] security.Init skipped (executor or sandbox nil)\n")
	}

	// 2. 启用 sandbox Layer 2（等价 lifecycle.go:365）。
	// Sandbox.enabled 默认 false（安全默认值），仅生产路径显式启用。
	// 用户仍可通过 /sandbox off 关闭（handleSandbox 命令）。
	if core.Executor != nil && core.Executor.Sandbox != nil {
		core.Executor.Sandbox.SetEnabled(true)
	} else {
		fmt.Fprintf(os.Stderr, "[wiring] sandbox.SetEnabled skipped (executor or sandbox nil)\n")
	}

	// 3. 接线 H11N27 memory 6 接口（等价 lifecycle.go:988）。
	// wiringCtx 在 Close 时由返回的清理函数取消，并等待 ForgettingScanner goroutine 退出。
	wiringCtx, wiringCancel := context.WithCancel(context.Background())
	wiringWait := wireH11N27Memory(cfg, client, core.CmdRegistry, homeDir, workDir, wiringCtx)

	// 4. 接线 Executor.CapabilityLookup/ReflectTrigger（等价 lifecycle.go:995）。
	// 必须在 wireH11N27Memory 之后调用（依赖 SetGlobalMergeLayer + SetAICaller 已完成）。
	// homeDir/workDir 同时供 P4 MemorySearchFunc（memorybridge 检索 Source）构建。
	wireExecutorMemoryCallbacks(core.Executor, homeDir, workDir)

	return func() {
		wiringCancel()
		wiringWait()
	}
}

// handleBranchList shows git branches
func (c *runnerCore) handleBranchList() string {
	cmd := exec.Command("git", "branch", "-a", "--color=never")
	cmd.Dir = c.WorkDir
	output, err := cmd.Output()
	if err != nil {
		return fmt.Sprintf("Git error: %v\nNot a git repository?", err)
	}
	lines := strings.Split(strings.TrimSpace(string(output)), "\n")
	var current string
	var others []string
	for _, line := range lines {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "* ") {
			current = strings.TrimPrefix(line, "* ")
		} else {
			name := strings.TrimPrefix(line, "remotes/origin/")
			others = append(others, name)
		}
	}
	var sb strings.Builder
	sb.WriteString("Git Branches:\n")
	if current != "" {
		sb.WriteString(fmt.Sprintf("  * %s (current)\n", current))
	}
	for _, b := range others {
		sb.WriteString(fmt.Sprintf("    %s\n", b))
	}
	return sb.String()
}

// handleCommit auto commits changes
func (c *runnerCore) handleCommit() string {
	cmd := exec.Command("git", "status", "--porcelain")
	cmd.Dir = c.WorkDir
	output, err := cmd.Output()
	if err != nil {
		return fmt.Sprintf("Git error: %v", err)
	}
	if len(strings.TrimSpace(string(output))) == 0 {
		return "Nothing to commit"
	}
	addCmd := exec.Command("git", "add", "-u")
	addCmd.Dir = c.WorkDir
	if _, err := addCmd.Output(); err != nil {
		return fmt.Sprintf("git add failed: %v", err)
	}
	diffCmd := exec.Command("git", "diff", "--cached", "--stat")
	diffCmd.Dir = c.WorkDir
	statOutput, _ := diffCmd.Output()
	commitMsg := fmt.Sprintf("Whale Code auto-commit\n\n%s", string(statOutput))
	commitCmd := exec.Command("git", "commit", "-m", commitMsg)
	commitCmd.Dir = c.WorkDir
	result, err := commitCmd.CombinedOutput()
	if err != nil {
		return fmt.Sprintf("Commit failed: %v\n%s", err, string(result))
	}
	lines := strings.Split(string(result), "\n")
	summary := ""
	if len(lines) > 0 {
		summary = lines[0]
	}
	return fmt.Sprintf("Committed: %s", summary)
}

// handleWorktreeList shows worktrees
func (c *runnerCore) handleWorktreeList() string {
	cmd := exec.Command("git", "worktree", "list")
	cmd.Dir = c.WorkDir
	output, err := cmd.Output()
	if err != nil {
		return fmt.Sprintf("Git error: %v", err)
	}
	return fmt.Sprintf("Worktrees:\n%s", string(output))
}

// handleWorktreeAdd creates a worktree
func (c *runnerCore) handleWorktreeAdd(args string) string {
	parts := strings.Fields(args)
	if len(parts) == 0 {
		return "Usage: /worktree add <path> [branch]"
	}

	path := parts[0]
	branch := ""
	if len(parts) > 1 {
		branch = parts[1]
	}

	cmdArgs := []string{"worktree", "add", path}
	if branch != "" {
		cmdArgs = append(cmdArgs, "-b", branch)
	}
	cmdArgs = append(cmdArgs, "HEAD")

	cmd := exec.Command("git", cmdArgs...)
	cmd.Dir = c.WorkDir
	output, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Sprintf("create worktree failed: %v\n%s", err, string(output))
	}
	return fmt.Sprintf("Worktree created:\n%s", string(output))
}

// handleWorktreeRemove removes a worktree
func (c *runnerCore) handleWorktreeRemove(args string) string {
	path := strings.TrimSpace(args)
	if path == "" {
		return "Usage: /worktree remove <path>"
	}

	cmd := exec.Command("git", "worktree", "remove", path)
	cmd.Dir = c.WorkDir
	output, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Sprintf("remove worktree failed: %v\n%s", err, string(output))
	}
	return fmt.Sprintf("Worktree removed: %s", path)
}

// handleWorktreePrune prunes stale worktrees
func (c *runnerCore) handleWorktreePrune() string {
	cmd := exec.Command("git", "worktree", "prune")
	cmd.Dir = c.WorkDir
	output, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Sprintf("prune worktrees failed: %v\n%s", err, string(output))
	}
	result := "stale worktree references pruned"
	if len(strings.TrimSpace(string(output))) > 0 {
		result += fmt.Sprintf("\n%s", string(output))
	}
	return result
}

// handleDiff 展示文件变更（：**薄转发** —— 唯一实现见 turn_diff.go 的 handleDiffFor）。
// headless（/plain、--print）与 TUI 从此外同源：同一实现、同一事实源、同一渲染器。
func (c *runnerCore) handleDiff(args string) string {
	return handleDiffFor(c.Executor, c.WorkDir, args)
}

// handlePluginList lists installed plugins
func (c *runnerCore) handlePluginList() string {
	pluginDir := filepath.Join(whalehome.Dir(), "plugins")
	entries, err := os.ReadDir(pluginDir)
	if err != nil {
		if os.IsNotExist(err) {
			return "No plugins installed\nUse /plugin install <url> to install"
		}
		return fmt.Sprintf("Error: %v", err)
	}
	if len(entries) == 0 {
		return "No plugins installed"
	}
	var sb strings.Builder
	sb.WriteString("Plugins:\n")
	for _, entry := range entries {
		if entry.IsDir() {
			sb.WriteString(fmt.Sprintf("  %s/\n", entry.Name()))
		}
	}
	return sb.String()
}

// handlePluginInstall installs a plugin from URL or local path.
//
// M20-04：安装后若插件默认启用且工具注册失败，会输出失败列表供用户感知。
func (c *runnerCore) handlePluginInstall(source string) string {
	if source == "" {
		return "Usage: /plugin install <url|path>"
	}
	plugin, err := c.Executor.PluginMgr.Install(source)
	if err != nil {
		return fmt.Sprintf("❌ %v", err)
	}
	if plugin.Enabled {
		// M20-04：清空累计器，确保 Drain 仅返回本次 install 的失败项。
		tools.ResetToolRegistrationFailures()
		if err := c.Executor.RegisterSinglePluginTools(plugin.ID); err != nil {
			return fmt.Sprintf("⚠️ Plugin installed but tool registration failed: %v\nInstalled: %s (%s) v%s", err, plugin.Name, plugin.ID, plugin.Version)
		}
		c.Executor.ReinjectPluginHooks()

		// M20-04：Drain 失败项并附加输出（若有）。
		if failures := tools.DrainToolRegistrationFailures(); len(failures) > 0 {
			var sb strings.Builder
			sb.WriteString(fmt.Sprintf("⚠️ Installed: %s (%s) v%s but %d tool(s) failed to register:\n", plugin.Name, plugin.ID, plugin.Version, len(failures)))
			sb.WriteString(tools.FormatToolRegistrationFailures(failures))
			return sb.String()
		}
	}
	return fmt.Sprintf("✅ Installed: %s (%s) v%s", plugin.Name, plugin.ID, plugin.Version)
}

// handlePluginEnable enables a plugin and registers its tools.
//
// M20-04：启用后若有工具注册失败，会输出失败列表供用户感知。
func (c *runnerCore) handlePluginEnable(id string) string {
	if id == "" {
		return "Usage: /plugin enable <id>"
	}
	if err := c.Executor.PluginMgr.Enable(id); err != nil {
		return fmt.Sprintf("❌ %v", err)
	}
	// M20-04：在 RegisterSinglePluginTools 前清空累计器，确保后续 Drain 仅返回本次失败。
	tools.ResetToolRegistrationFailures()
	if err := c.Executor.RegisterSinglePluginTools(id); err != nil {
		return fmt.Sprintf("⚠️ Plugin enabled but tool registration failed: %v", err)
	}
	c.Executor.ReinjectPluginHooks()

	// M20-04：Drain 本次注册失败项，若有则附加输出。
	failures := tools.DrainToolRegistrationFailures()
	if len(failures) == 0 {
		return fmt.Sprintf("✅ Plugin %s enabled", id)
	}
	var sb strings.Builder
	sb.WriteString(fmt.Sprintf("⚠️ Plugin %s enabled but %d tool(s) failed to register:\n", id, len(failures)))
	sb.WriteString(tools.FormatToolRegistrationFailures(failures))
	sb.WriteString(fmt.Sprintf("Other tools of %s are still available.\n", id))
	return sb.String()
}

// handlePluginDisable disables a plugin and removes its tools
func (c *runnerCore) handlePluginDisable(id string) string {
	if id == "" {
		return "Usage: /plugin disable <id>"
	}
	if err := c.Executor.PluginMgr.Disable(id); err != nil {
		return fmt.Sprintf("❌ %v", err)
	}
	c.Executor.UnregisterPluginTools(id)
	c.Executor.ReinjectPluginHooks()
	return fmt.Sprintf("✅ Plugin %s disabled", id)
}

// handleReloadPlugins reloads all plugins.
//
// M20-04：重载后若有工具注册失败，会输出失败列表供用户感知。
func (c *runnerCore) handleReloadPlugins() string {
	plugins, err := c.Executor.PluginMgr.ReloadAll()
	if err != nil {
		return fmt.Sprintf("❌ Plugin reload failed: %v", err)
	}

	c.Executor.UnregisterAllPluginTools()
	// M20-04：清空累计器，确保 Drain 仅返回本次 reload 的失败项。
	tools.ResetToolRegistrationFailures()
	c.Executor.RegisterPluginTools()
	c.Executor.ReinjectPluginHooks()

	var sb strings.Builder
	sb.WriteString("Plugin reload complete:\n")
	for _, p := range plugins {
		status := "✅"
		if !p.Enabled {
			status = "⏸️"
		}
		state := "enabled"
		if !p.Enabled {
			state = "disabled"
		}
		sb.WriteString(fmt.Sprintf("  %s %s (v%s) - %s\n", status, p.Name, p.Version, state))
	}

	// M20-04：Drain 失败项并附加输出（若有）。
	if failures := tools.DrainToolRegistrationFailures(); len(failures) > 0 {
		sb.WriteString(fmt.Sprintf("⚠️ %d tool(s) failed to register during reload:\n", len(failures)))
		sb.WriteString(tools.FormatToolRegistrationFailures(failures))
	}
	return sb.String()
}

// handleCronAdd adds a scheduled task
func (c *runnerCore) handleCronAdd(args string) string {
	if args == "" {
		return `Usage: /cron add "<schedule>" "<message>"`
	}
	parts := strings.SplitN(args, "\" \"", 2)
	if len(parts) < 2 {
		return `Usage: /cron add "<schedule>" "<message>"`
	}
	schedule := strings.Trim(parts[0], "\" ")
	message := strings.TrimSuffix(parts[1], "\"")
	cronFile := filepath.Join(whalehome.Dir(), "cron.json")
	var entries []map[string]string
	if data, err := os.ReadFile(cronFile); err == nil {
		if err := json.Unmarshal(data, &entries); err != nil {
			entries = []map[string]string{}
		}
	}
	entry := map[string]string{
		"id":       fmt.Sprintf("%d", time.Now().Unix()),
		"schedule": schedule,
		"message":  message,
		"enabled":  "true",
	}
	entries = append(entries, entry)
	data, _ := json.MarshalIndent(entries, "", "  ")
	if err := util.AtomicWriteFile(cronFile, data, 0644); err != nil {
		return fmt.Sprintf("Error saving cron: %v", err)
	}
	return fmt.Sprintf("Cron added: %s -> %s", schedule, message)
}

// handleCronList lists scheduled tasks
func (c *runnerCore) handleCronList() string {
	cronFile := filepath.Join(whalehome.Dir(), "cron.json")
	data, err := os.ReadFile(cronFile)
	if err != nil {
		return "No scheduled tasks"
	}
	var entries []map[string]string
	if err := json.Unmarshal(data, &entries); err != nil {
		return "No scheduled tasks (invalid data)"
	}
	if len(entries) == 0 {
		return "No scheduled tasks"
	}
	var sb strings.Builder
	sb.WriteString("Scheduled Tasks:\n")
	for i, e := range entries {
		status := "[ON]"
		if e["enabled"] == "false" {
			status = "[OFF]"
		}
		sb.WriteString(fmt.Sprintf("  %d. %s %s: %s\n", i+1, status, e["schedule"], e["message"]))
	}
	return sb.String()
}

// handleCronRemove removes a scheduled task
func (c *runnerCore) handleCronRemove(args string) string {
	if args == "" {
		return "Usage: /cron remove <id>"
	}
	cronFile := filepath.Join(whalehome.Dir(), "cron.json")
	data, err := os.ReadFile(cronFile)
	if err != nil {
		return "No scheduled tasks"
	}
	var entries []map[string]string
	if err := json.Unmarshal(data, &entries); err != nil {
		return "No scheduled tasks (invalid data)"
	}
	found := false
	var removed string
	for i, e := range entries {
		if e["id"] == args {
			removed = e["message"]
			entries = append(entries[:i], entries[i+1:]...)
			found = true
			break
		}
	}
	if !found {
		return fmt.Sprintf("Task not found: %s", args)
	}
	newData, _ := json.MarshalIndent(entries, "", "  ")
	if err := util.AtomicWriteFile(cronFile, newData, 0644); err != nil {
		return fmt.Sprintf("Error saving cron: %v", err)
	}
	return fmt.Sprintf("Removed: %s", removed)
}

// handleExport exports session to markdown file
func (c *runnerCore) handleExport(filename string) string {
	if filename == "" {
		filename = "whale-export.md"
	}
	if !filepath.IsAbs(filename) {
		filename = filepath.Join(c.WorkDir, filename)
	}
	var sb strings.Builder
	for _, msg := range c.Session.GetMessages() {
		switch msg.Role {
		case session.RoleUser:
			sb.WriteString(fmt.Sprintf("## User\n\n%s\n\n", msg.TextContent()))
		case session.RoleAssistant:
			sb.WriteString(fmt.Sprintf("## Assistant\n\n%s\n\n", msg.TextContent()))
		}
	}
	if err := util.AtomicWriteFile(filename, []byte(sb.String()), 0644); err != nil {
		return fmt.Sprintf("Export failed: %v", err)
	}
	return fmt.Sprintf("Exported to %s", filename)
}

// handleImportSessionShared 从 JSON 文件导入会话到本地 Store（P0-16 修复，v4 模块审查）。
//
// 本方法为 PlainRunner 和 BridgeRunner 提供共享的 /import 命令实现，
// 与 TUI 模式的 Model.handleImportSession（commands_misc.go:611）行为一致：
// 1. 校验路径非空
// 2. 校验文件存在
// 3. 读取文件内容
// 4. JSON 反序列化为 session.Session
// 5. 为空 name 时生成默认名称（imported-YYYYMMDD-HHMMSS）
// 6. 通过 Store.AppendBatch 持久化消息
//
// 参数：
// - path: JSON 会话文件路径
//
// 返回：
// - string: 用户可读的结果信息（成功/失败）
func (c *runnerCore) handleImportSessionShared(path string) string {
	path = strings.TrimSpace(path)
	if path == "" {
		return "Usage: /import <path>"
	}

	if _, err := os.Stat(path); os.IsNotExist(err) {
		return fmt.Sprintf("File not found: %s", path)
	}

	data, err := os.ReadFile(path)
	if err != nil {
		return fmt.Sprintf("Read file failed: %v", err)
	}

	var importedSession session.Session
	if err := json.Unmarshal(data, &importedSession); err != nil {
		return fmt.Sprintf("Invalid session format: %v", err)
	}

	if importedSession.Metadata.Name == "" {
		importedSession.Metadata.Name = fmt.Sprintf("imported-%s", time.Now().Format("20060102-150405"))
	}

	sessionID := importedSession.Metadata.Name
	if err := c.Store.AppendBatch(sessionID, importedSession.GetMessages()); err != nil {
		return fmt.Sprintf("Save session failed: %v", err)
	}

	return fmt.Sprintf("Imported session: %s (%d messages)", importedSession.Metadata.Name, len(importedSession.GetMessages()))
}

// promptConfirm prompts user in terminal to confirm an operation (Y/N).
func (c *runnerCore) promptConfirm() bool {
	fmt.Print("  Allow once / 允许本次? (Y/N): ")
	reader := bufio.NewReader(os.Stdin)
	line, _ := reader.ReadString('\n')
	line = strings.TrimSpace(line)
	return strings.EqualFold(line, "Y") || strings.EqualFold(line, "yes")
}

// handleSandbox 切换**能力轴**（plain/bridge 路径；A1 与 Model 侧同语义，
// 共用 sandbox_toggle.go 的 helper —— 见该文件头注释）。
//
// G12：与 Model 侧**同形同改** —— Opt-A 把跨轴偷写摘掉，
// 两处必须一起改（只改一侧 = plain/bridge 路径照旧越界，见
// g12_axis_ownership_test.go 的 runnerCore 判据）。
func (c *runnerCore) handleSandbox(args string) string {
	// R4：档位是**本命令不会修改**的结构化事实，读一次即可。
	mode := c.Policy.Mode()
	switch args {
	case "on":
		sandboxEnable(c.Executor)
		return fmt.Sprintf("Sandbox ON - command validation enabled [%s]\n%s",
			sandboxGateStatus(c.Executor), sandboxAxisNotice(mode))
	case "off":
		sandboxDisable(c.Executor)
		return fmt.Sprintf("Sandbox OFF [%s]\n⚠️ %s\n%s",
			sandboxGateStatus(c.Executor), sandboxDisableNotice, sandboxAxisNotice(mode))
	default:
		out := fmt.Sprintf("Sandbox: %s [%s]\n%s",
			sandboxAxisBadge(c.Executor), sandboxGateStatus(c.Executor), sandboxAxisNotice(mode))
		// 出口③：敲错参数回用法（唯一实现 = sandboxUnknownArgNotice， 两侧同源）。
		if hint := sandboxUnknownArgNotice(args); hint != "" {
			out = hint + "\n" + out
		}
		return out
	}
}

// handlePermissions 切换**审批轴**（plain/bridge 路径；与 Model 侧同语义，
// 共用 sandbox_toggle.go 的 approvalAxisNotice —— 见该文件头注释）。
//
// G12b：修复前 `--plain` 侧在 plain.go 里**重新实现**了一遍
// `/permissions`，真机探针（HOME 隔离 + 预置 settings.json）实证三个问题：
//
//	① 无轴归属文案 —— 用户看起来它就是"总的权限开关"；
//	② 不认 `status` —— 直接回 `Unknown mode: status`；
//	③ 文案与 TUI 侧各写各的 ⇒ 天然漂移（同一句命令在两个 UI 里含义不同）。
//
// 现在实现收进本方法，plain.go 只做转发 ⇒ 两 UI 共用同一份实现与同一份文案。
//
// R4：档位是**本命令会修改**的目标值，走 SetModeByName 这个 SSOT 解析
// （它同时完成「解析」与「已删除模式的审批语义迁移」，如 accept-edits 会注入
// 编辑/安全命令的默认 allow 规则）。
func (c *runnerCore) handlePermissions(args string) string {
	args = strings.TrimSpace(args)

	// 无参数 / status：只读报告，不写盘。能力轴（沙箱）的**当前值**一并报出，
	// 便于用户对照"另一轴现在是什么"——这正是用户要求的"说清具体管哪个轴"。
	if args == "" || strings.EqualFold(args, "status") {
		return fmt.Sprintf("Permission: %s\n  %s", c.Policy.Mode(),
			approvalAxisNotice(c.Policy.Mode(), c.Executor))
	}

	if !c.Policy.SetModeByName(args) {
		// 刻意保留 "Unknown mode" 前缀：预设 id（safe/standard/unrestricted）
		// 属于 `/security-preset` 的命名空间，在这里被拒是**期望行为**（I2）。
		// 文案走 unknownApprovalModeNotice（与 TUI 侧共用 SSOT）——它会认出
		// "敲的其实是预设 id"并指路，而不是干巴巴报错。
		return unknownApprovalModeNotice(args)
	}
	return fmt.Sprintf("Permission: %s\n  %s", c.Policy.Mode(),
		approvalAxisNotice(c.Policy.Mode(), c.Executor))
}

// handleUndo 使用 FileHistory 撤销最近一次工具调用的文件变更（工具级，仅文件）
func (c *runnerCore) handleUndo() string {
	fh := c.Executor.FileHistoryGetter()
	if fh == nil {
		return "文件历史未初始化"
	}
	msgId := c.Executor.CurrentMessageId()
	if !fh.CanUndo(msgId) {
		return "没有可撤销的操作"
	}
	entry, err := fh.Undo(msgId)
	if err != nil {
		return fmt.Sprintf("撤销失败: %v", err)
	}
	var files []string
	for _, fc := range entry.Files {
		files = append(files, filepath.Base(fc.Path))
	}
	return fmt.Sprintf("✅ 已撤销: %s\n文件: %s\n重做: /redo", entry.ToolName, strings.Join(files, ", "))
}

// handleRedo 使用 FileHistory 重做最近一次撤销的工具调用（工具级，仅文件）
func (c *runnerCore) handleRedo() string {
	fh := c.Executor.FileHistoryGetter()
	if fh == nil {
		return "文件历史未初始化"
	}
	msgId := c.Executor.CurrentMessageId()
	if !fh.CanRedo(msgId) {
		return "没有可重做的操作"
	}
	entry, err := fh.Redo(msgId)
	if err != nil {
		return fmt.Sprintf("重做失败: %v", err)
	}
	var files []string
	for _, fc := range entry.Files {
		files = append(files, filepath.Base(fc.Path))
	}
	return fmt.Sprintf("✅ 已重做: %s\n文件: %s", entry.ToolName, strings.Join(files, ", "))
}

// handleRewind 处理 /rewind 命令（会话级回退，同时恢复文件+会话状态）
// 对称于 app.go 的 handleRewind，供 runner/bridge 路径使用
// 支持 --preview 标志：仅显示 DiffStats 预览，不实际执行回退
func (c *runnerCore) handleRewind(args string) string {
	if strings.TrimSpace(args) == "" {
		// 非交互入口（--plain / bridge）：这里**没有选择器可开**，所以无参数时打印
		// 清单 + 用法 —— 与 TUI 的选择器是同一份数据（formatRewindList），只是呈现
		// 方式不同。★ bubbletea TUI 里的交互选择器在本入口不存在（见回执「未验证」）。
		sessionID, alias, currentMsgID := c.rewindViewContext()
		rows, err := rewindRowsFromHistory(c.Executor.FileHistoryGetter(), sessionID, alias, currentMsgID)
		if err != nil {
			return fmt.Sprintf("Failed to list snapshots: %v", err)
		}
		return formatRewindUsage() + "\n\n" + formatRewindList(rows)
	}
	fh := c.Executor.FileHistoryGetter()
	if fh == nil {
		return "File history not initialized"
	}
	// 解析 --preview 标志
	preview := false
	target := strings.TrimSpace(args)
	if strings.HasSuffix(target, " --preview") {
		preview = true
		target = strings.TrimSuffix(target, " --preview")
	}
	// 解析目标：`#N` / 裸 N（清单序号，1 = 最新）· 短码 · 完整锚点。
	// 与 Model.doRewind 共用 resolveRewindArg —— 两侧只薄转发，不各写一份。
	viewSessionID, viewAlias, viewMsgID := c.rewindViewContext()
	rows, listErr := rewindRowsFromHistory(fh, viewSessionID, viewAlias, viewMsgID)
	if listErr != nil {
		return fmt.Sprintf("Failed to list snapshots: %v", listErr)
	}
	msgId, resolveErr := resolveRewindArg(target, rows)
	if resolveErr != "" {
		return resolveErr
	}
	// preview 模式：仅显示 DiffStats，不执行实际回退
	if preview {
		currentMsgId := c.Executor.CurrentMessageId()
		if currentMsgId == "" {
			return "Cannot preview: no active message"
		}
		stats, err := fh.DiffStats(msgId, currentMsgId)
		if err != nil {
			return fmt.Sprintf("Preview failed: %v", err)
		}
		return fmt.Sprintf("⏮️ Rewind preview (to %s)\n Insertions: %d\n Deletions: %d\n Files changed: %d\n\nRun /rewind %s to confirm and execute",
			msgId, stats.Insertions, stats.Deletions, stats.FilesChanged, msgId)
	}
	// 构造 SessionRestorer 适配器
	var restorer filehistory.SessionRestorer
	if c.Store != nil {
		restorer = &sessionRestorerAdapter{store: c.Store}
	}
	sessionId := c.SessionName
	result, err := fh.Rewind(msgId, restorer, sessionId)
	if err != nil {
		return fmt.Sprintf("Rewind failed: %v", err)
	}
	// 恢复后重载会话
	// A8：失败不得静默 —— 见 commands_rewind.go 的 rewindReloadAfterRestore 头注释。
	reloadWarn := ""
	if result.SessionRestored && sessionId != "" {
		reloadWarn = rewindReloadAfterRestore(c.Store, sessionId, func(s *session.Session) { c.replaceSession(s) })
	}
	var sb strings.Builder
	sb.WriteString(fmt.Sprintf("✅ Rewound to %s\n Files restored: %d, deleted: %d\n Session restored: %v",
		result.TargetMsgId, result.FilesRestored, result.FilesDeleted, result.SessionRestored))
	if reloadWarn != "" {
		sb.WriteString(fmt.Sprintf("\n ⚠️ %s", reloadWarn))
	}
	if result.BackupMsgId != "" {
		sb.WriteString(fmt.Sprintf("\n Backup snapshot: %s", result.BackupMsgId))
	}
	for _, w := range result.Warnings {
		sb.WriteString(fmt.Sprintf("\n ⚠️ %s", w))
	}
	return sb.String()
}

// handleCheckpoint 处理 /checkpoint 命令（阶段 4：调 FileHistory，对称于 app.go）
func (c *runnerCore) handleCheckpoint(args string) string {
	fh := c.Executor.FileHistoryGetter()
	if fh == nil {
		return "File history not initialized"
	}
	parts := strings.Fields(args)
	if len(parts) == 0 {
		return formatCheckpointUsage()
	}
	action := parts[0]
	rest := ""
	if len(parts) > 1 {
		rest = strings.Join(parts[1:], " ")
	}
	switch action {
	case "save":
		msgId := c.Executor.CurrentMessageId()
		if msgId == "" {
			return "No active message; cannot create snapshot"
		}
		label := rest
		if label == "" {
			// 落盘标签改存会话人话名（别名），不再存会话主键 UUID。
			alias := ""
			if c.Session != nil {
				alias = c.Session.Metadata.Name
			}
			label = snapshotLabelForStorage(alias, c.SessionName)
		}
		msgCount := 0
		if c.Session != nil {
			msgCount = len(c.Session.GetMessages())
		}
		snap, err := fh.Snapshot(msgId, label, msgCount)
		if err != nil {
			return fmt.Sprintf("Failed to create snapshot: %v", err)
		}
		return fmt.Sprintf("✅ Snapshot created\n MsgId: %s\n Label: %s\n Files: %d, Messages: %d",
			snap.MsgId, snap.Label, len(snap.Files), snap.SessionMsgCount)
	case "list":
		// 渲染收口到 formatRewindList（与 Model.handleCheckpoint 同一实现）。
		sessionID, alias, currentMsgID := c.rewindViewContext()
		rows, err := rewindRowsFromHistory(fh, sessionID, alias, currentMsgID)
		if err != nil {
			return fmt.Sprintf("Failed to list snapshots: %v", err)
		}
		return formatRewindList(rows)
	case "restore":
		if rest == "" {
			return bi("Usage: /checkpoint restore #<n>", "用法：/checkpoint restore #<n>")
		}
		return c.handleRewind(rest)
	case "show":
		if rest == "" {
			return bi("Usage: /checkpoint show #<n>", "用法：/checkpoint show #<n>")
		}
		// show 与 restore 用同一套解析：`#N` / 短码 / 完整锚点都能进。
		viewSessionID, viewAlias, viewMsgID := c.rewindViewContext()
		showRows, listErr := rewindRowsFromHistory(fh, viewSessionID, viewAlias, viewMsgID)
		if listErr != nil {
			return fmt.Sprintf("Failed to list snapshots: %v", listErr)
		}
		target, resolveErr := resolveRewindArg(rest, showRows)
		if resolveErr != "" {
			return resolveErr
		}
		snap, err := fh.GetSnapshot(target)
		if err != nil {
			return fmt.Sprintf("Failed to get snapshot: %v", err)
		}
		if snap == nil {
			return fmt.Sprintf("Snapshot not found: %s", rest)
		}
		// 旧索引里的 label 可能是会话主键（之前的写入）⇒ 展示前换回人话别名/短码。
		labelStr := displaySnapshotLabel(snap.Label, viewSessionID, viewAlias)
		return fmt.Sprintf("📌 Snapshot details\n MsgId: %s\n Label: %s\n Files: %d\n Messages: %d\n Created: %s",
			snap.MsgId, labelStr, len(snap.Files), snap.SessionMsgCount,
			snap.Timestamp.Format("2006-01-02 15:04:05"))
	default:
		return fmt.Sprintf("Unknown subcommand: %s\nAvailable: save, list, restore, show", action)
	}
}

// --- UIService implementation ---

// Update handles a UI message and returns an update result
func (c *runnerCore) Update(msg interface{}) (interface{}, interface{}) {
	// UIService stub: actual UI update logic lives in the TUI Model (app.go)
	return nil, nil
}

// View renders the current UI view
func (c *runnerCore) View() string {
	// UIService stub: actual view rendering lives in the TUI Model (app.go)
	return ""
}

// --- SessionService implementation (basic) ---
// Note: runnerCore only implements the basic SessionService methods here.
// 2026-09-27：原文写「其余方法由 runnerCoreServiceAdapter 提供」——该适配器已按**死码**删除
// （全仓零生产构造点），此处不再有第二个实现者；TUI 侧的生产实现是 `&m.SessionModel`。

// LoadSession loads a session by name
func (c *runnerCore) LoadSession(name string) error {
	if c.Store == nil {
		return fmt.Errorf("session store not initialized")
	}
	loaded, err := c.Store.Load(name)
	if err != nil {
		return fmt.Errorf("load session %s: %w", name, err)
	}
	c.replaceSession(loaded)
	c.SessionName = name
	c.SavedMsgCount = len(loaded.GetMessages())
	return nil
}

// SaveSession saves the current session
func (c *runnerCore) SaveSession() error {
	return c.saveSession()
}

// CurrentSession returns the current session
func (c *runnerCore) CurrentSession() *session.Session {
	return c.Session
}

// SessionNameVal returns the current session name
func (c *runnerCore) SessionNameVal() string {
	return c.SessionName
}

// CurrentModel returns the current model name
func (c *runnerCore) CurrentModel() string {
	return c.ModelName
}

// SetModel changes the current model
func (c *runnerCore) SetModel(model string) {
	c.ModelName = model
	c.AIClient.SetModel(model)
	if c.Caching != nil {
		c.Caching.UpdateModel(model)
	}
	c.requestSnapshot = contextRequestSnapshot{}
}

// --- AIService implementation (basic) ---

// StreamRequest sends a streaming AI request with retry/degrade/compress recovery
func (c *runnerCore) StreamRequest(ctx context.Context, req *ai.Request) (<-chan ai.Event, error) {
	return ai.StreamWithRetry(ctx, c.AIClient, *req, c.RetryConfig, c.Fallback, c.RecoveryCoordinator)
}

// HandleStreamError processes streaming errors
func (c *runnerCore) HandleStreamError(err error) error {
	c.setState(stateIdle)
	return fmt.Errorf("stream error: %w", err)
}

// --- ToolService implementation (basic) ---

// ExecuteTool 执行工具。
func (c *runnerCore) ExecuteTool(ctx context.Context, name string, input json.RawMessage) (*tools.Result, error) {
	if c.Executor == nil {
		return nil, fmt.Errorf("executor not initialized")
	}
	result := c.Executor.Execute(ctx, tools.Call{Name: name, Input: string(input)})
	return &result, nil
}

// IsConcurrencySafe checks if a tool can run concurrently
func (c *runnerCore) IsConcurrencySafe(name string) bool {
	if c.Registry == nil {
		return false
	}
	spec, ok := c.Registry.Get(name)
	if !ok {
		return false
	}
	return spec.ConcurrencySafe
}

// GetToolSpec returns a tool's specification
func (c *runnerCore) GetToolSpec(name string) (tools.Spec, bool) {
	if c.Registry == nil {
		return tools.Spec{}, false
	}
	return c.Registry.Get(name)
}

// EstimateTokens estimates the current token count
func (c *runnerCore) EstimateTokens() int {
	return c.estimateCurrentInputContextTokens()
}

// Compact compacts the context
func (c *runnerCore) Compact() error {
	c.handleCompact("")
	return nil
}

// ContextState returns the current context state
func (c *runnerCore) ContextState() string {
	estimated := c.estimateCurrentInputContextTokens()
	cached := ctxpkg.GetCachedInputTokens(c.TokenSource, c.ModelName, estimated, func() {
		if c.Caching != nil {
			c.Caching.RecordCacheAnomaly()
		}
	})
	return c.Budget.CheckState(estimated, cached).String()
}
