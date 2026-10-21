package tui

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/anchorandfish/whale-code/internal/ai"
	"github.com/anchorandfish/whale-code/internal/bridge"
	"github.com/anchorandfish/whale-code/internal/bus"
	"github.com/anchorandfish/whale-code/internal/command"
	"github.com/anchorandfish/whale-code/internal/config"
	ctxpkg "github.com/anchorandfish/whale-code/internal/context"
	"github.com/anchorandfish/whale-code/internal/daemon"
	"github.com/anchorandfish/whale-code/internal/doctor"
	"github.com/anchorandfish/whale-code/internal/export"
	"github.com/anchorandfish/whale-code/internal/github"
	"github.com/anchorandfish/whale-code/internal/goal"
	"github.com/anchorandfish/whale-code/internal/keybindings"
	"github.com/anchorandfish/whale-code/internal/memorybridge"
	"github.com/anchorandfish/whale-code/internal/metrics"
	"github.com/anchorandfish/whale-code/internal/monitor"
	"github.com/anchorandfish/whale-code/internal/notify"
	"github.com/anchorandfish/whale-code/internal/onboarding"
	"github.com/anchorandfish/whale-code/internal/permission"
	"github.com/anchorandfish/whale-code/internal/sandbox"
	"github.com/anchorandfish/whale-code/internal/sandbox/validator"
	"github.com/anchorandfish/whale-code/internal/search"
	"github.com/anchorandfish/whale-code/internal/secret"
	"github.com/anchorandfish/whale-code/internal/security"
	"github.com/anchorandfish/whale-code/internal/session"
	"github.com/anchorandfish/whale-code/internal/skill"
	"github.com/anchorandfish/whale-code/internal/tools"
	"github.com/anchorandfish/whale-code/internal/typeahead"
	"github.com/anchorandfish/whale-code/internal/util"
	"github.com/anchorandfish/whale-code/internal/whalehome"
	"github.com/charmbracelet/bubbles/key"
	"github.com/charmbracelet/bubbles/textarea"
	"github.com/charmbracelet/bubbles/viewport"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

// programRef 是进程级单例 *tea.Program 的共享持有者（P0 修复，2026-09-13）。
//
// 为什么必须共享：Model 是**按值复制**的 —— NewModel 按值返回、tea.NewProgram
// 内部再拷一份、每次 Update 也返回新值。若把 *tea.Program 直接存成 Model 的
// **值字段**，SetProgram 只会写中"调用它的那一份副本"，而 Program 运行期真正
// 驱动的是它内部那份副本 ⇒ 那份的 program 字段**永远是 nil**。
// 后果是**整族**经 program.Send 回传的通道静默降级（它们都带 `if != nil` 守卫，
// 失败完全不可见）：
// - 子 agent 确认 overlay 永不弹出 → 所有子 agent 的 NeedConfirm 被伪造成
// "rejected by user (sub-agent confirmation)"，子 agent 写/执行能力实际不可用；
// - /memory edit 不暂停终端（编辑器与 bubbletea 抢终端）；
// - /doctor、/upgrade check 结果不回传；异步编排结果丢失；MCP elicitation 被丢弃。
//
// 指针字段在副本间共享 ⇒ 用一个小对象承载 Program 引用，即可从根上消除
// "注入到哪份副本"这个隐式契约（调顺序救不了：Program 需要 model 才能构造，
// 天然是"先 NewProgram 再 SetProgram"）。
//
// 不变量：programRef **必须且只能**在 NewModel 中创建一次，保证 NewModel 返回的
// 所有副本共享同一实例（由 TestNewModelInitializesAgentWiring 守卫）。
// 判据见 agent_confirm_program_binding_repro_test.go：调用**已接线**的 AgentConfirmFn，
// 断言其不早于用户决策返回（修复前该调用 3.037µs 即静默返回 false）。
type programRef struct {
	p *tea.Program
}

// get 返回已注入的 Program；未注入（或零值 Model）时返回 nil，由调用方降级。
func (r *programRef) get() *tea.Program {
	if r == nil {
		return nil
	}
	return r.p
}

// SetProgram 注入 bubbletea Program 引用，供 /memory edit 等需要暂停终端控制的场景使用。
// 应在 main.go tea.NewProgram 创建 Program 后立即调用。非线程安全，仅用于初始化期。
// 写入的是 programRef 共享实例，因此对 NewModel 的**任意**副本可见（含 Program 内部那份）。
func (m *Model) SetProgram(p *tea.Program) {
	if m.program == nil {
		// 防御：零值 Model（仅测试会出现）。生产路径由 NewModel 创建共享实例，
		// 此处不可作为"忘记在 NewModel 创建"的补救——那会让副本各持一份 ref。
		m.program = &programRef{}
	}
	m.program.p = p
}

// RegisterTurnEndHook 注册 turn 结束钩子到 TUI Model（N26）。
// 钩子在每轮对话结束时（snapshot 之后）按注册顺序调用。
// goal.Manager 通过此方法注入到 Model，实现 OnTurnEnd 回调桥接。
// 非线程安全，仅在初始化期调用（main.go / newGoalManager 接线时）。
func (m *Model) RegisterTurnEndHook(h TurnEndHook) {
	m.turnEndHooks = append(m.turnEndHooks, h)
}

// runTurnEndHooks 执行所有注册的 turn 结束钩子（N26）。
// 在 snapshot 之后调用，确保快照先完成（goal Evidence 收集依赖快照）。
// 单个钩子错误仅记录日志，不阻塞后续钩子或主流程（运行时冲突矩阵第 5 行）。
func (m *Model) runTurnEndHooks(ctx context.Context) {
	if len(m.turnEndHooks) == 0 {
		return
	}
	threadID := m.sessionService.SessionName()
	turnResult := goal.TurnResult{
		ThreadID:   threadID,
		TokensUsed: int64(m.sessionService.InputTokens() + m.sessionService.OutputTokens()),
	}
	for _, h := range m.turnEndHooks {
		if err := h.OnTurnEnd(ctx, threadID, turnResult); err != nil {
			log.Printf("[tui] turn end hook failed (non-fatal): %v", err)
		}
	}
}

// NewModel 创建并初始化 TUI 模型及其输入、服务、缓存和后台组件。
func NewModel(cfg *config.Config, client *ai.SimpleClient, reg *ai.ModelRegistry) Model {
	ta := textarea.New()
	ta.Placeholder = "Enter=send, Shift+Enter=newline. Type /help for commands."
	ta.Focus()
	// bubbles textarea 在 CharLimit>0 时每次插入都会执行 O(n) Length()；输入上限改由
	// sendInput/显式粘贴路径单次校验，避免长文本下每键扫描全部内容。
	ta.CharLimit = 0
	// MaxHeight 会限制逻辑行数并静默截断多行粘贴，不是单纯的视觉高度限制。
	// 视觉高度继续由 SetHeight + maxInputHeight 控制，逻辑行数不设上限。
	ta.MaxHeight = 0
	ta.SetWidth(50)
	ta.SetHeight(3)
	ta.ShowLineNumbers = false
	// textarea 默认在每个可视行左侧绘制 "┃ "。外层已有圆角边框，
	// 两者叠加会看起来像输入框内有一条断开的竖线。
	ta.Prompt = ""
	// Prompt 会参与 textarea 内部宽度计算，清空后重新计算内容宽度。
	ta.SetWidth(50)

	ta.KeyMap.InsertNewline = key.NewBinding(key.WithKeys("shift+enter"), key.WithHelp("shift+enter", "insert newline"))
	ta.KeyMap.Paste = key.NewBinding(key.WithKeys("ctrl+v"), key.WithHelp("ctrl+v", "paste"))

	vp := viewport.New(60, 20)

	workDir, _ := os.Getwd()
	// B-1 修复：初始化 typeahead 工作目录，供 fileIndex 后台刷新使用
	typeahead.InitWorkDir(workDir)
	// P0-08：暂不在此处注入 SkillProvider，因 m.executor 尚未创建
	// 在 m.executor 创建后通过 typeahead.SetSkillProvider 注入（见下方 initSkillTypeahead 调用）
	homeDir, err := os.UserHomeDir()
	if err != nil {
		homeDir = os.Getenv("USERPROFILE")
		if homeDir == "" {
			homeDir = os.Getenv("HOME")
			if homeDir == "" {
				homeDir = "."
			}
		}
	}
	sessionDir := filepath.Join(whalehome.Dir(), "sessions")

	estimator := ai.NewTokenEstimator()
	toolRegistry := tools.NewRegistry()

	// C24: NewStore now returns an error (directory creation may fail).
	sessionStore, storeErr := session.NewStore(sessionDir)
	if storeErr != nil {
		// Surface the error via the model's message list so the user sees
		// why sessions are unavailable; the TUI stays usable for non-session
		// operations.
		sessionStore = nil
	}
	// 归档式压缩接线：settings.json compact_archive（默认 true）
	// 传播到 store。缺失此接线则用户关闭归档的设置静默失效
	// （分层验证链第 2 层：配置被内核接受，由 TestNewModelWiresArchiveEnabled 锁定）。
	if sessionStore != nil {
		sessionStore.SetArchiveEnabled(cfg != nil && cfg.CompactArchive)
	}

	// N12: 创建 V2 统一记忆存储（workDir SHA256 隔离 + 0700/0600 + 跨进程锁）。
	// 失败时降级为 nil，下游 memSelector/memExtractor 均做 nil 防御。
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

	// C20: wire bus for Session ↔ Context event decoupling.
	// Store 发布 SessionForkEvent/SessionSwitchEvent；LifecycleHandler 订阅
	// 并清空 Context 缓存 + 从新 session 重载。若 sessionStore 为 nil
	// （NewStore 失败），仅跳过 SetBus，handler 仍可启动。
	eventBus := bus.NewBus()
	if sessionStore != nil {
		sessionStore.SetBus(eventBus)
	}
	ctxMgr := ctxpkg.NewSessionContextManager(nil, nil, nil)
	lifecycleHandler := ctxpkg.NewSessionLifecycleHandler(eventBus, sessionStore, ctxMgr)
	if err := lifecycleHandler.Start(); err != nil {
		fmt.Fprintf(os.Stderr, "[init] session lifecycle handler start failed: %v\n", err)
		lifecycleHandler = nil
	}

	// R-3：使用 NewExecutorWithConfig 接入 config 系统的 plugin 签名配置。
	// fail-closed：若 cfg.PluginSignature 配置非法（Enabled=true + Required=true + PublicKeyPath 空），
	// 返回 error 并 log.Fatalf 终止启动（不降级到 noopVerifier，避免用户误以为已启用签名验证）。
	signatureExecutor, sigErr := tools.NewExecutorWithConfig(toolRegistry, workDir, cfg)
	if sigErr != nil {
		log.Fatalf("[init] executor init failed (plugin signature config invalid): %v", sigErr)
	}

	// N13 L0 引擎初始化（02 §2.2.2 契约）。
	// 顺序：renderCache → messageBuffer（onEvict 闭包引用 renderCache）→
	// textMeasurer → virtualScroll。
	// onEvict 回调在 messageBuffer 淘汰消息时失效对应 RenderCache 条目，
	// 防止裁剪后缓存残留导致显示不一致。
	// （M10-02）：NewLRURenderCache 增加字节上限参数（64MB），防超长会话 OOM。
	renderCache := NewLRURenderCache(defaultCacheEntries, defaultCacheMaxBytes)
	initialMsgs := buildInitialMessages(client.Model(), workDir, string(cfg.PermissionMode), storeErr)
	msgBuffer := NewMessageBuffer(defaultMaxMessages, func(evicted string) {
		renderCache.Invalidate(hashMessage(evicted))
	})
	for _, m := range initialMsgs {
		msgBuffer.Append(m)
	}
	textMeasurer := NewRunewidthMeasurer(defaultWidthCacheSize)
	// T1-P1-B7 ：切换为 chunkedVirtualScroll（行级切片 + 视口缓冲 + 增量更新）。
	// bufferLines=0 → 默认 viewportH/3（SetViewport 后延迟计算），至少 5 行。
	// 保留 NewMessageVirtualScroll 作为 fallback 构造函数（未删除）。
	virtualScroll := NewChunkedVirtualScroll(renderCache, textMeasurer, 0)

	userKeybindingsPath := filepath.Join(whalehome.Dir(), "keybindings.json")
	projectKeybindingsPath := filepath.Join(workDir, ".whale", "keybindings.json")
	keybindingsMgr, err := keybindings.NewManager(userKeybindingsPath, projectKeybindingsPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "[init] failed to load keybindings config: %v, using defaults\n", err)
		keybindingsMgr, _ = keybindings.NewManager("", "")
	}
	// D6 修复 + F5 修复后补全：VimMode 与 keybindings.Mode 联动
	// F5 修复后 load() 不再在 Mode 为空时默认设为 "vim"（改为返回空 bindings），
	// 因此 cfg.VimMode=true 时需显式 SetMode("vim") + Reload() 加载 vim 默认键绑定表。
	// 否则 vim 模式切换键（'i'/Escape）无键绑定，TestVimMode_NormalToInsert/InsertToNormal 会失败。
	// VimMode=false 时保持默认（emacs）键位，不在此强制覆盖。
	if cfg.VimMode {
		keybindingsMgr.SetMode("vim")
		if err := keybindingsMgr.Reload(); err != nil {
			fmt.Fprintf(os.Stderr, "[init] keybindings reload after SetMode(vim) failed: %v\n", err)
		}
	}

	m := Model{
		// programRef 在此创建且仅创建一次：NewModel 按值返回，返回的每份副本
		// （含 main 的 model 与 tea.NewProgram 内部那份）共享同一实例，
		// SetProgram 对任意一份的写入对全体可见。详见 programRef 文档。
		program: &programRef{},
		// monitorStats 同上：下方回调闭包捕获的是**本函数局部**的 m，
		// 而 NewModel 按值返回 ⇒ 计数必须经共享指针持有，否则写进死副本。
		monitorStats: &monitorStats{},
		UIModel: UIModel{
			viewport: vp,
			textarea: ta,
			width:    0,
			height:   0,
			vimMode:  cfg.VimMode,
			vimModeState: func() VimModeState {
				if cfg.VimMode {
					return VimNormal
				}
				return VimInsert
			}(),
			keybindings:       keybindingsMgr,
			imagePasteHandler: NewImagePasteHandler(),
			suggestionState:   NewSuggestionState(), // N5 typeahead：@ / # / slash 补全弹窗
		},
		SessionModel: SessionModel{
			session:             session.NewSession(),
			config:              cfg,
			store:               sessionStore,
			workDir:             workDir,
			homeDir:             homeDir,
			onboardingCompleted: onboarding.IsCompleted(homeDir), // P3：初始化时缓存，避免渲染时重复 os.Stat
			modelName:           client.Model(),
			availableModels:     reg.Names(),
			messages:            msgBuffer, // N13 G-04：messageBuffer（FIFO 裁剪 + onEvict 失效 RenderCache）
			usageTracker:        ai.NewUsageTracker(filepath.Join(whalehome.Dir(), "usage.json")),
			currentAgent:        "coder",
			compactCount:        0,
			autoCompactCooldown: false,
			configLoader:        config.NewLoader(homeDir, ""),
			eventBus:            eventBus,
			lifecycleHandler:    lifecycleHandler,
			// P0-8/9 新增：排队输入队列上限（默认 50，对标同类 CLI 无上限的反例加防护）
			queuedInputsMax: DefaultQueuedInputsMax,
		},
		// N13 L0 引擎字段（接口类型，内部含指针，Model 复制时底层实例共享）。
		virtualScroll: virtualScroll,
		renderCache:   renderCache,
		textMeasurer:  textMeasurer,
		AIModel: AIModel{
			aiClient:  client,
			estimator: estimator,
			// C4：窗口走唯一解析点（同 runner_core.go）。
			budget:              ctxpkg.NewBudgetFromConfig(cfg.EffectiveContextWindow(), cfg),
			compactor:           ctxpkg.NewCompactor(estimator),
			aiCompactor:         ctxpkg.NewAICompactor(client, estimator),
			fallback:            ai.NewFallbackTracker(client.Model(), cfg.FallbackModel),
			modelRegistry:       reg,
			magicDocs:           config.NewMagicDocs(workDir), //nolint:staticcheck // H4 保留 V1 作为 fallback
			memStore:            memStore,
			memIndex:            memIndex,
			memSelector:         config.NewMemorySelector(memoryReadSource, memIndex, client),
			memExtractor:        config.NewAutoMemoryExtractor(memStore),
			recoveryCoordinator: ai.NewRecoveryCoordinator(),
			retryConfig:         ai.DefaultRetryConfig(),
			caching:             ai.NewCachingIntegration(client.Model(), cfg.CacheBreakThreshold),
			// V3-6-2/3：初始化 AI 安全检测器（循环/拒绝）。
			// 阈值使用组件默认值：循环阈值 3（成功）/5（失败）/5（无进展）。
			// 拒绝处理启用强制执行机制（用户可输入"我确认强制执行"覆盖）。
			// （原 hallucinationDetector 已随 整体移除，2026-10-08。）
			loopDetector:   ai.NewLoopDetector(0, 0, 0), // 0→使用默认阈值 3/5/5
			refusalHandler: ai.NewRefusalHandler(true),  // enabled=true，启用强制执行
		},
		ToolModel: ToolModel{
			registry:    toolRegistry,
			executor:    signatureExecutor,
			cmdRegistry: command.NewRegistry(),
			policy:      permission.NewPolicyFromModeName(string(cfg.PermissionMode)),
			monitorMgr:  monitor.NewManager(),
			monitorCh:   make(chan monitorEvent, 256),
			// A-5：done 事件阻塞发送的逃生口，退出流程里关闭（见 cleanupOnExit）。
			monitorEscape: newMonitorEscape(),
			loopCh:        make(chan loopFireMsg, 256),
			toolInputBuf:  make(map[string]string),
		},
	}

	// D4 修复：初始化 saveMu 指针，保护 saveSession 异步化后的读-改-写区间。
	// 使用指针而非值字段，确保 Model 值复制（bubbletea Update 返回新值）时仍指向同一锁实例。
	m.saveMu = &sync.Mutex{}
	m.mouseEpoch = &atomic.Uint64{}
	// 请求内审批复用表：**必须在这里 make 一次** —— 不能靠"用到时懒初始化"，
	// 也不能另开一个值字段做作用域标识。理由见 Model.reqApprovals 的字段注释：
	// NewModel 按值返回，此后每份副本共享**同一个 map 实例**（引用类型），
	// 只对**内容**做增删才跨副本可见；值字段与"懒赋值"都会写进副本而丢失。
	m.reqApprovals = make(map[string]string)

	// T1-B：TUI 路径注入 TokenSource（runnerCore 路径已有，此处补齐）。
	// 注入后 SimpleClient.Stream 解析到 UsageEvent 时调用 RecordRemoteUsage，
	// provider 真实 usage 与本地估算的 EMA 偏差持续回流；estimateCurrentInputContextTokens
	// 通过 ApplyCalibration 应用偏移，使状态栏/预警/自动压缩判定收敛到真实 usage。
	// offsetTTL 兜底 30 分钟（cfg.Token.OffsetTTL=0 时使用默认值，向后兼容旧 settings.json）。
	if ts := client.TokenSource(); ts != nil {
		m.calibrationSource = ts
	} else {
		offsetTTL := time.Duration(cfg.Token.OffsetTTL) * time.Minute
		if offsetTTL <= 0 {
			offsetTTL = 30 * time.Minute
		}
		var tokenSource ai.TokenSource
		if cfg.Token.TiktokenEnabled {
			tokenSource = ai.NewTokenSourceWithTiktoken(estimator, m.usageTracker, offsetTTL)
		} else {
			tokenSource = ai.NewTokenSource(estimator, m.usageTracker, false, offsetTTL)
		}
		client.SetTokenSource(tokenSource)
		m.calibrationSource = tokenSource
	}

	// H8：注入 Config 到 CmdRegistry，使 listAvailableModels 等命令能读取配置化值
	m.cmdRegistry.Config = cfg

	if r, err := NewMarkdownRenderer("dark"); err == nil {
		m.mdRenderer = r
	}
	// （M10-05）：初始化 themeManager，支持 /theme 命令运行时热切换。
	// mdRenderer 字段指向 themeMgr.Renderer() 快照，向后兼容既有调用方。
	m.themeMgr = newThemeManager("dark")
	if m.themeMgr != nil {
		m.mdRenderer = m.themeMgr.Renderer()
	}

	// N24：初始化通知系统（Notifier + ToastModel）
	// NewToastModel 内部会设置 Notifier 的 onPush 回调（通过 pushCh 桥接到 tea.Msg）
	m.notifier = notify.NewNotifier()
	m.toast = NewToastModel(m.notifier)

	// TUI inline scrollback 批次2：初始化流式 active_cell + frame_limiter。
	// activeCell 是流式文本真相源；frameLimiter 是 8.33ms 帧率限制器。
	// 所有使用点 nil 守卫，未初始化时退化为原行为。
	// REF-外部: 同类 CLI frame_rate_limiter / chatwidget active_cell
	m.activeCell = NewActiveCell()
	m.frameLimiter = NewFrameLimiter()

	// P0 agent 防护体系：轮次上限/无进展熔断/审查子循环上限/
	// 进度检测。NewModel 统一初始化（生产与测试共用此入口），配置来自
	// config.MaxTurnsPerRequest（env WHALE_MAX_TURNS_PER_REQUEST 覆盖）+ WHALE_AUTONOMY。
	m.guard = newAgentGuard(cfg)

	// N24 N1 预留钩子：N1 实施后启用。
	// taskManager.onTaskEvent = func(e TaskEvent) {
	// m.notifier.Push(notify.NewTaskNotification(e.TaskID, e.Status))
	// }

	// N24 N6 预留骨架：N6 实施后启用。
	// 在 app.go 错误处理路径（AI 调用返回错误处），若错误是 *ai.Error（N6 结构化错误），
	// 提取 aiErr.ID，调用 m.notifier.Push(notify.NewErrorNotification(string(aiErr.ID), aiErr.Message))
	// 仅对特定 ErrorID 触发通知（如 ErrAgentTimeout/ErrAgentRecursionLimit/ErrCompactFailed）。

	// N26：初始化 goal.Manager（+ ，/goal 命令接线层）
	// 注入 4 个桥接回调：compact（no-op，）/ launchTurn（sendPrompt）/
	// hasUserInput（textarea 非空判断）/ collectEvidence（FileHistory + session 提取）
	m.goalManager = newGoalManager(
		homeDir,
		client,
		metrics.Default,
		m.usageTracker,
		func(prompt string) { m.SendPrompt(prompt) },
		func() bool { return m.hasUserInputPending() },
		func(ctx context.Context, threadID string, turnResult goal.TurnResult) (goal.Evidence, error) {
			return m.collectGoalEvidence(ctx, threadID, turnResult)
		},
	)
	// 注册 goal.Manager 为 TurnEndHook（每轮结束时触发评估 + 续跑，）
	m.RegisterTurnEndHook(m.goalManager)

	m.policy.SetAllowedTools(cfg.AllowedTools)

	// RuleStore 接线（P0，2026-09-09）：此前 NewRuleStore/SetRuleStore/SetPathForSource
	// 在生产零调用点，settings.json 的 allow/deny/ask 从不被加载，/permissions add 重启即丢。
	// WireRuleStore 建立 store + 启动即加载规则；加载失败不致命（store 仍在，/permissions reload 可重试）。
	if err := m.policy.WireRuleStore(homeDir, workDir); err != nil {
		fmt.Fprintf(os.Stderr, "[init] WireRuleStore warning: %v\n", err)
	}

	// F3（S14-HG-4）接线（2026-09-15，O-U 轮）：项目级 ignore 注入。
	//
	// 缺陷：`secret.SetGlobalProjectIgnore` 全仓此前**只有定义 + 测试**（零生产调用）
	// ⇒ `<proj>/.whaleignore` 与 `~/.whale/whaleignore` 在运行时完全不生效，
	// 敏感文件判定只剩内置默认表。
	// 方向 = fail-safe（内置默认表仍生效，不构成放开）⇒ 拍板取 A「启动注入 cwd」。
	// 落点与 WireRuleStore 同处（同一条启动路径、同一个 workDir 语义），
	// `SetGlobalProjectIgnore` 无返回值 ⇒ 不造 error 分支。
	secret.SetGlobalProjectIgnore(workDir)

	// S9 delta E（2026-09-14，用户拍板「严格会话级」）：**不再接审批记忆的持久化**。
	//
	// 原来的三行（SetMemoryStorePath + LoadMemory）使「允许本会话」的授权在重启后
	// 从 <home>/.whale/permissions.json 恢复 = 持久授权，与选项文案「允许本会话」不符。
	//
	// ★ 为什么连**读路径**也一并摘除（而不是只靠"Remember 不再写"）：
	// 只摘写路径时，"不持久化"只是**偶然成立**——一旦 <home>/.whale/permissions.json
	// 存在（历史版本残留 / 手工放置 / 其他工具写入），旧读路径就会把它加载成有效授权。
	// ~/.whale/ 是本地可写面 ⇒ 这就是一条提权向量。判据必须落在**与"谁会写"无关**的那一层。
	// （同一教训见 G5 回执 §1.7-E：发现机制不能作为边界的唯一承载。）
	//
	// 处置：只**检测并警告**，不加载、不删除（启动路径不改动用户目录下的文件）。
	stalePermPath := filepath.Join(whalehome.Dir(), "permissions.json")
	if _, statErr := os.Stat(stalePermPath); statErr == nil {
		fmt.Fprintf(os.Stderr, "[init] 检测到遗留的 %s —— 已忽略（严格会话级：审批记忆不跨重启）。"+
			"该文件不再生效，可安全删除。\n", stalePermPath)
	}

	// delta F（2026-09-14，用户拍板族 C）：**审批决策留痕**通道。
	//
	// 与上面 delta E 的"授权记忆"是**两回事**，二者共享同一个 homeDir 但互不读写：
	// - delta E：Policy.memory（[A] 允许本会话），严格会话级、**不落盘**；
	// - delta F：审计条目，只写不读、**落盘可追溯**。
	// ★ 没有任何代码把审计文件读回 Policy —— 那会让"留痕"退化成"持久授权"
	// （守卫测试 TestDeltaF_T1_AuditIsNotReadBackAsAuthorization）。
	//
	// 路径由 approvalAuditRelPath 单点给出（tui/approval_audit.go），避免两处各写一遍。
	m.approvalAudit = bridge.NewApprovalAudit(filepath.Join(homeDir, filepath.FromSlash(approvalAuditRelPath)))

	m.executor.AIClient = client
	m.executor.MonitorMgr = m.monitorMgr

	// P0-08（M11-03 + M25-01 合并）：注入 SkillMgr 适配器到 typeahead
	// 使 TUI 输入框中 # 触发符能调 SkillMgr.List() 获取 skill 候选
	typeahead.SetSkillProvider(newSkillProviderAdapter(&m.executor.SkillMgr))

	// Inject RecoveryCoordinator into sub-modules for coordinated retry/degrade/compress
	m.executor.RecoveryCoordinator = m.recoveryCoordinator
	m.executor.RetryConfig = m.retryConfig
	m.executor.Fallback = m.fallback

	// G1/G4（2026-09-08 真实冒烟缺口）：子代理编排能力接线。
	// UsageTracker：子代理 UsageEvent 经 RecordAgentUsage 分桶落账，/usage 可见
	//（修复前子代理 token 隐形，"减少 token 使用"无观测基础）。
	// AgentConfirmFn：子代理 NeedConfirm 经 agentConfirmBridge 路由到 TUI 确认
	// overlay（修复前 coder 子代理写文件被静默拒绝，编排退化为顾问模式）。
	m.executor.UsageTracker = m.usageTracker
	m.executor.AgentConfirmFn = m.agentConfirmBridge
	// on-failure 沙箱升级——沙箱内 EPERM 后经 TUI 弹一次
	// 「出沙箱重跑」询问（bridge 阻塞等决策；plain 模式在 plain.go 自行接线终端 Y/N）。
	m.executor.OnSandboxEscalate = m.sandboxEscalateBridge
	m.aiCompactor.SetRecoveryConfig(m.recoveryCoordinator, m.retryConfig, m.fallback)
	// 省 token 第 1 批：压缩摘要走可配置的专用模型（空 = 沿用主模型）。
	if m.config != nil {
		m.aiCompactor.SetCompactModel(m.config.CompactModel)
	}
	m.memSelector.SetRecoveryConfig(m.recoveryCoordinator, m.retryConfig, m.fallback)
	m.memExtractor.SetRecoveryConfig(m.recoveryCoordinator, m.retryConfig, m.fallback)

	// Initialize tool execution pipeline (integrated hook system and permission policy)
	m.executor.InitPipelineWithHooks(m.policy, workDir)

	// H2：初始化全局命令安全校验器（02 §2.3.1 契约）。
	// hook/monitor 委托 security.CheckCommand 进行命令安全校验。
	// 注入 executor.Sandbox + 新建 Validator + 已创建的 Policy。
	security.Init(m.executor.Sandbox, validator.NewValidator(), m.policy)

	// SE-1 修复：生产环境默认启用 sandbox Layer 2（命令黑名单/网络拦截/路径检查）。
	// Sandbox.enabled 默认 false（安全默认值），仅生产初始化路径显式启用。
	// 用户仍可通过 /sandbox off 关闭。
	m.executor.Sandbox.SetEnabled(true)

	// S-1 + P0-2 修复：生产环境接线 sandbox.Manager（OS 级隔离）。
	// P1-SEC：平台开关收敛到 sandbox.NewProductionSandboxManager 单一事实源
	// （darwin Seatbelt / linux namespaces+Landlock / windows Job Object），
	// 与非 TUI 路径（runner_core.go EnableProductionSandbox）共用，避免漂移。
	sbMgr := sandbox.NewProductionSandboxManager(workDir)
	m.executor.SetSandboxMgr(sbMgr)

	// 门 2 降级可见性：启动时向用户显示实际隔离等级与降级项
	//（此前仅审计日志，TUI 不可见）。
	// 门 2 修复（真机验证）：走 sandbox.PrintIsolationBanner fd 2 直写——
	// os.Stderr 变量已被 main.go 重定向到日志文件，走变量 banner 会被吞
	//（终端永远看不到，躺在 ~/.whale/logs/whale.log）。
	sandbox.PrintIsolationBanner(sbMgr)

	// 桶 X3 病灶2 接线层补充：把 Executor.SandboxMgr 同步到 hookMgr，
	// 使 HookCommand（type=command）走完整沙箱链路：Prepare → Restrict →
	// StartWithProcessGroup → PostStartHook。
	// 修复前：hook.SetSandboxManager 接口已存在但生产路径无调用方，
	// 导致用户在 hooks.json 中注册的 HookCommand 跳过沙箱执行。
	// hook 默认沙箱级别用 Relaxed（避免过度限制 hook 脚本），
	// 与 Executor.SandboxMgr.Level()=Strict 解耦，hook 可单独在 hooks.json
	// 中按 hook 配置 SandboxLevel 字段覆盖默认值。
	// SyncHookSandboxManager 内部对 mgr==nil 或 hookMgr==nil 已做无操作处理，
	// 此处无需额外 nil 守卫。
	m.executor.SyncHookSandboxManager(sandbox.SandboxLevelRelaxed)

	// S7 · PR-A2（①/③ 拍板）：hook 信任门**按需**提示，对齐业内既有实现 "Hooks need review"。
	// 只有确实存在「未信任 / 信任后被改」的 hook 时才提示（否则 HookTrustNotice 返回空串、
	// PrintStartupNotice 直接返回 → 不打扰用户）。提示里给出一键命令，但**不自动执行**。
	m.executor.PrintHookTrustNoticeIfAny()

	// H3：注入密钥脱敏器 + 错误包装回调（守 02 §2.3.1/§2.3.2 契约）。
	// secret.SecretRedactor 实现 security.Redactor 接口，替代 H2 nil 时 no-op 行为。
	// secret.SetErrorWrapper 绑定 ai.WithID，使 WrapLeak/WrapMissing 返回 N6 结构化错误。
	security.SetRedactor(secret.SecretRedactor{})
	secret.SetErrorWrapper(func(err error, id string) error {
		return ai.WithID(err, ai.ErrorID(id))
	})

	// P0-7/B7: 仅当 --enable-plugins 显式启用时加载并注册插件工具与钩子。
	// 默认插件禁用（高风险任意代码执行），需用户显式 opt-in。
	if m.config.PluginsEnabled {
		// M20-04：注册前清空累计器，注册后 drain 失败项并输出到 stderr/日志。
		tools.ResetToolRegistrationFailures()
		m.executor.EnableAllPlugins()
		if failures := tools.DrainToolRegistrationFailures(); len(failures) > 0 {
			fmt.Fprintf(os.Stderr, "⚠️ %d plugin tool(s) failed to register during startup:\n", len(failures))
			fmt.Fprint(os.Stderr, tools.FormatToolRegistrationFailures(failures))
			log.Printf("[plugin] %d tool(s) failed to register during startup", len(failures))
		}
	}

	// B3: 接入 approval.Manager（C19 第三层交互层）。
	// 从 config 读取策略字符串，解析失败兜底为 on-request（B0 §5 最安全默认）；
	// 创建 Manager 后注入 PluginMgr（git_clone 等消费者）与 Pipeline（PermissionStage）。
	// 对比 SetOnUninstall（internal/tools/executor.go:147）的同等接线处理。
	approvalMgr := newApprovalManagerFromConfig(cfg)
	m.executor.PluginMgr.SetApprovalManager(approvalMgr)
	if m.executor.Pipeline != nil {
		m.executor.Pipeline.SetApprovalManager(approvalMgr)
	}
	// B0 §3.1 第3项：会话切换时清空 approval 缓存，避免新会话复用旧会话审批决策。
	if lifecycleHandler != nil {
		lifecycleHandler.SetCacheClearer(approvalCacheAdapter{mgr: approvalMgr})
	}

	// P0-7：注册全局 MCP elicitation handler（对标同类 CLI ElicitationRequestManager）
	// handler 在 getMCPClient 创建新 client 时自动注入。
	// handler 内部通过 program.Send 非阻塞发送 elicitationRequestMsg 到 TUI 主循环。
	// 对标同类 CLI make_sender closure 的 tx_event.send
	// 依赖 programRef 共享：闭包在运行期调用 get()，此时 SetProgram 已完成注入。
	// （若像修复前那样在 NewModel 期捕获瞬时值，捕获到的是 nil —— 那时 program 尚未注入。）
	tools.SetMCPElicitationHandler(func(serverName string, requestID json.RawMessage, message string) {
		prog := m.program.get()
		if prog == nil {
			// fail-safe：program 未初始化，忽略请求（client 侧 300s TTL 超时后自动 cancel）
			return
		}
		// program.Send 是非阻塞的（bubbletea 内部 buffered channel），满足 handler 非阻塞契约
		prog.Send(elicitationRequestMsg{
			serverName: serverName,
			requestID:  requestID,
			message:    message,
		})
	})

	// Discover and register MCP tools (async, non-blocking)
	//
	// S6/G8 ③ 阶段一：失败不再静默 —— error 落 ~/.whale/logs/whale.log。
	// 非 debug 模式下 main.go 已把标准库 log 的输出重定向到该文件
	// （os.Stderr = stderrFile + stdlog.SetOutput(stderrFile)）。
	//
	// ⚠️ 诚实边界：本阶段**只承诺"事后查得到"**，不承诺"用户看得见"。
	// fd 2 直写的启动提示会被 TUI alt-screen 覆盖，且本 discovery 是**异步**的
	// （失败要等 initialize 超时）⇒ 提示必然晚于 alt-screen 切换点。
	// "会话期间可见"须走 TUI 消息通道，与 S7 §7.2 合并决策（见 S6 方案 §6.3/§6.4）。
	go func() {
		if err := m.executor.DiscoverAndRegisterMCPTools(workDir); err != nil {
			log.Printf("[mcp] tool discovery failed: %v", err)
		}
	}()

	m.cmdRegistry.OnUndo = func() string {
		return m.handleUndoWithStack()
	}
	m.cmdRegistry.OnRedo = func() string {
		return m.handleRedoWithStack()
	}
	m.cmdRegistry.OnSkills = func() string {
		return m.executor.SkillMgr.FormatList()
	}
	m.cmdRegistry.OnPlugins = func() string {
		return m.executor.PluginMgr.FormatList()
	}
	m.cmdRegistry.OnPluginInstall = func(args string) string {
		return m.handlePluginInstall(args)
	}
	m.cmdRegistry.OnPluginEnable = func(args string) string {
		return m.handlePluginEnable(args)
	}
	m.cmdRegistry.OnPluginDisable = func(args string) string {
		return m.handlePluginDisable(args)
	}
	m.cmdRegistry.OnReloadPlugins = func() string {
		return m.handleReloadPlugins()
	}
	m.cmdRegistry.OnHooks = func(args string) string {
		return command.NewHooksCommandHandler(
			m.executor.HookManager(),
			m.workDir,
		)(args)
	}
	// O-2：/skill 信任管理出口接线 —— SSOT 在 skill.Manager.HandleTrustCommand，
	// 这里只做薄转发。与 OnHooks 同批装配，两条生产装配入口（lifecycle/runner_core）都接。
	m.cmdRegistry.OnSkillManage = func(args string) (string, error) {
		return m.executor.SkillMgr.HandleTrustCommand(args), nil
	}
	m.cmdRegistry.OnMcp = func(args string) string {
		return command.NewMcpCommandHandler(
			m.executor,
			m.workDir,
		)(args)
	}
	m.cmdRegistry.OnEnv = func(args string) string {
		return command.NewEnvCommandHandler(
			command.NewConfigEnvPersister(
				func() map[string]string { return m.config.WhaleEnv },
				func() error {
					if m.configLoader != nil {
						return m.configLoader.Save(m.config)
					}
					return nil
				},
			),
			m.cmdRegistry.EnvStore, // CMD-P1-004: 进程内 EnvStore，废除 os.Setenv
		)(args)
	}
	m.cmdRegistry.OnOutputStyle = func(args string) string {
		return command.NewOutputStyleCommandHandler(
			&configOutputStyleSetter{
				config: m.config,
				saver: func() error {
					if m.configLoader != nil {
						return m.configLoader.Save(m.config)
					}
					return nil
				},
			},
		)(args)
	}
	// P7-3（v2）：注入 /fast 命令回调，让 command/fast_handler.go 真正生效。
	// 原问题：TUI 未注入 OnSetFastMode/OnGetFastModeState，导致 /fast on/off 仅返回提示不实际切换；
	// 同时 tui/commands_task.go 的 handleFast 是死代码（未被路由调用），已删除。
	// 三模型统一：FastModel 走 settings.json + env fallback（WHALE_FAST_MODEL）。
	// 致命漏洞修复（子审查第 1 轮）：原方案复用 handleModelSwitch 会覆盖 Config.Model，
	// 导致 /fast off 无法恢复原 Model。改用 switchRuntimeModel（仅运行时切换，不覆盖 Config.Model），
	// Config.Model 始终保持默认主模型语义，/fast off 切回 Config.Model 即可正确恢复。
	m.bindFastModeCallbacks()
	m.cmdRegistry.OnTerminalSetup = func(args string) string {
		return command.NewTerminalSetupCommandHandler()(args)
	}
	homeDirForMigrate := homeDir
	m.cmdRegistry.OnMigrate = func(args string) string {
		return command.NewMigrateCommandHandler(homeDirForMigrate)(args)
	}
	m.cmdRegistry.OnSkillExecute = func(skillName, args string) string {
		result, err := m.executor.SkillMgr.Execute(skillName, args)
		if err != nil {
			return ""
		}
		return result
	}
	// OnSkillExecuteWithMeta 带 meta 的 skill 执行回调（供需要 model/allowedTools 的调用方使用）
	m.cmdRegistry.OnSkillExecuteWithMeta = func(skillName, args string) (skill.SkillResult, error) {
		return m.executor.SkillMgr.ExecuteWithMeta(skillName, args)
	}

	// Initialize search engine
	// H8：MaxSearchMatchesPerSession 从 Config 读取（原硬编码 5，<=0 时 NewSearcherWithConfig 内部 fallback）
	m.searcher = search.NewSearcherWithConfig(m.store, m.config.MaxSearchMatchesPerSession)

	// Set checkpoint command callback（阶段 4：改为调 FileHistory）
	m.cmdRegistry.OnCheckpoint = func(args string) string {
		return m.handleCheckpoint(args)
	}
	// OnRewind 不在此接线（家族收口 + 附带的 AST 门发现，2026-10-14）：
	// "rewind" 在 isTUICommandHandledLocally 表内（app.go）⇒ TUI 内 cmd.Handler 永不触发
	// （闸门见 app.go 的 `if !isTUICommandHandledLocally(name) { cmd.Handler(args) }`），
	// 此处捕获 NewModel 值局部 m 的闭包是**死代码**。唯一活面 = 本地 case "rewind"
	// → m.handleRewind；plain/bridge 各自在自己的 registry 上独立接线（plain.go 调度表 / bridge.go）。
	//
	// ★ 为什么现在才删（而不是像 OnContext/OnNotifyDismiss/OnTaskCommand 那样 2026-10-11 就删）：
	// 当时该闭包的活面只碰**跨副本共享**的指针/接口字段，AST 值捕获门不响；
	// 给 handleRewind 加了无参开选择器分支（openRewindPicker 调 m.recalcViewportHeight()），
	// 按值写（lastInputAreaTop/lastPreviewHeight/unreadBelow 等）被拉进闭包可达集 ⇒
	// TestValueCaptureASTGate 立刻打红（定点消融实测：只摘掉该 recalc 即复绿）。
	// 解法与 一致 —— 死闭包不该存在，而不是把门基线放宽。
	// ★ 别在此重新接回：门（§① 新增即红）会再打一次。

	// Set search command callback
	m.cmdRegistry.OnSearch = func(args string) string {
		return m.handleSearch(args)
	}

	// Set Monitor callback, pass events to TUI main loop via channel
	//
	// ★ A-5：三个回调的 `default:` 空分支已去掉/补计数。
	// 原文三处都是
	// select { case m.monitorCh <- ev: default: }
	// ⇒ 事件静默蒸发：用户既看不到"少了什么"，也判不出界面是否还跟得上。
	// 现在区分两档：
	// · 丢**采样**（output 行）：可辩护 —— 行本身在 Monitor 的有界权威副本里
	// （DefaultMaxLines=10000），丢的只是"催重绘"的通知；计数进 monitorOutputDropped，
	// 由主界面摘要与 /monitors 显示。
	// · 丢**状态迁移**（done = 进程结束）：不可接受 ⇒ 改为阻塞发送 + 逃生口，
	// 写法对齐 internal/bridge/ws_transport.go:206-210（无 default:，阻塞 + closeCh 逃生）。
	// ★ 另有一层结构兜底：monitor.Manager 在调用 onDone **之前**已把
	// proc.Status 置为终态（monitor.go:520 置状态 → :530 回调），所以即使
	// TUI 侧漏掉这一次通知，投影（现读权威副本）下次重算就是真值。
	m.monitorMgr.SetOutputCallback(func(monitorID, line string) {
		select {
		case m.monitorCh <- monitorEvent{eventType: "output", monitorID: monitorID, line: line}:
		default:
			// 丢采样：可计数、可见（A-5）。**不是**静默丢弃。
			// ★ 必须经共享指针计数：本闭包捕获的是 NewModel 的局部 Model（按值返回），
			// 写值字段会落进死副本（见 monitorStats 文档）。
			m.monitorStats.outputDropped.Add(1)
		}
	})
	m.monitorMgr.SetDoneCallback(func(monitorID string, status monitor.MonitorStatus, exitCode int, output string) {
		ev := monitorEvent{eventType: "done", monitorID: monitorID, status: status, exitCode: exitCode, output: output}
		// 状态迁移：**不得**丢。阻塞直到 TUI 取走，或在退出流程关掉逃生口时解开。
		select {
		case m.monitorCh <- ev:
		case <-m.monitorEscape.done():
			m.monitorStats.doneDropped.Add(1)
		}
	})

	// Initialize LoopScheduler, pass trigger events to TUI main loop via channel
	// Persist to ~/.whale/loop_tasks.json
	loopFilePath := filepath.Join(whalehome.Dir(), "loop_tasks.json")
	m.loopScheduler = tools.NewLoopScheduler(func(taskID, prompt string) {
		select {
		case m.loopCh <- loopFireMsg{taskID: taskID, prompt: prompt}:
		default:
			// A-5：丢触发可辩护（handleLoopFire 本身也会跳过 non-idle 的触发，
			// 是既有取舍），但**必须可计数**，不再静默蒸发。
			// 同 monitorStats：经共享指针计数（闭包捕获的是局部 Model 副本）。
			m.monitorStats.loopDropped.Add(1)
		}
	}, loopFilePath)
	m.loopScheduler.Start()
	m.executor.LoopScheduler = m.loopScheduler

	// Initialize daemon (background task management)
	// Daemon does not auto-start; user starts manually with /daemon start
	daemonCfg := daemon.DaemonConfig{
		WorkDir:    workDir,
		MaxWorkers: 2,
	}
	m.daemon = daemon.NewDaemon(daemonCfg)
	m.daemonControl = daemon.NewController(m.daemon.GetConfig())
	// Daemon uses deferred start mode: only create instance, do not auto-call Start()
	// User manually starts daemon with /daemon start command
	// This way even if Start() fails due to stale PID file, user can clean up and retry

	// Set daemon command callback
	m.cmdRegistry.OnDaemon = func(args string) string {
		if m.daemonControl == nil {
			return "Daemon not initialized; please restart Whale Code"
		}
		return m.handleDaemon(args)
	}

	// Set loop task command callback
	m.cmdRegistry.OnLoop = func(args string) string {
		if m.loopScheduler == nil {
			return "Loop scheduler not initialized"
		}
		return m.handleLoopCmd(args)
	}

	// ★ Task/context/notify-dismiss 三条回调**不在此接线**（家族收口，2026-10-11）：
	// 这三条命令（"context"、"task list/cancel/show"、"notify dismiss"）全部在
	// isTUICommandHandledLocally 表内（app.go），TUI 内 cmd.Handler 永不触发，
	// 此处的值捕获闭包是死代码（捕获 NewModel 值局部 m）。唯一活面 = 本地 switch
	// 直调 m.handleTaskCommand / m.handleContextInfo / m.handleNotifyDismiss；
	// plain/bridge 由 runner_core.go 在各自 registry 实例上独立接线。
	// 若重新接回值捕获形态，AST 门（value_capture_ast_gate_test.go）按"新引入"打红。
	// Set super plan mode callback
	m.cmdRegistry.OnUltraplan = func(args string) string {
		return m.executor.UltraplanManager.Toggle()
	}

	// Set Bug hunter mode callback
	m.cmdRegistry.OnBugHunter = func(args string) string {
		switch args {
		case "start":
			return m.executor.BugHunterManager.Start()
		case "stop":
			return m.executor.BugHunterManager.Stop()
		case "status":
			return m.executor.BugHunterManager.Status()
		default:
			return m.executor.BugHunterManager.Status()
		}
	}

	// Set compaction strategy switch callback
	m.cmdRegistry.OnCompactStrategy = func(strategy string) string {
		return m.handleCompactStrategy(strategy)
	}

	// P0-3（D5-2）修复：接线 /upgrade check 异步结果回调。
	// command 模块在 goroutine 中执行检查后通过此回调通知结果，
	// TUI 通过 program.Send 将结果发送到 Update 循环显示，避免同步阻塞主线程。
	m.cmdRegistry.OnUpgradeCheckResult = func(result string) {
		if prog := m.program.get(); prog != nil {
			prog.Send(upgradeCheckResultMsg{result: result})
		}
	}

	// OnContext 不在此接线（家族收口，见上方注释块）；
	// /context 活面 = 本地 case "context" → handleContext → handleContextInfo。
	// Set doctor diagnostic callback（N17：接线 /doctor 命令到 internal/doctor 包）
	// D4 修复：异步执行诊断，不阻塞主循环。
	// 同步返回"⏳ 诊断中..."占位，goroutine 内执行 doc.Run + doc.Render，
	// 完成后通过 m.program.Send(doctorResultMsg{...}) 回传到 Update 显示结果。
	m.cmdRegistry.OnDoctor = func(args string) (string, error) {
		settingsPath := filepath.Join(whalehome.Dir(), "settings.json")
		doc := doctor.NewDoctor(m.config, m.aiClient, m.workDir, settingsPath)
		go func() {
			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()
			results := doc.Run(ctx)
			rendered := doc.Render(results)
			if prog := m.program.get(); prog != nil {
				prog.Send(doctorResultMsg{rendered: rendered})
			}
		}()
		return "⏳ 诊断中...（结果将异步显示，最多等待 30s）", nil
	}

	// Set onboarding callback（N18：接线 /onboarding 命令到 internal/onboarding 包）
	// 审查 v1 B1+B3 修复：TUI raw 模式下直接读 stdin 会挂起，/onboarding 在 TUI 内
	// 不执行交互流程，仅返回指引文本。reset 子命令仍可用（仅删哨兵文件，不读 stdin）。
	// 真正的 4 步骤交互式引导由 loadConfig（TUI 启动前）触发。
	m.cmdRegistry.OnOnboarding = func(args string) (string, error) {
		if args == "reset" {
			if err := onboarding.Reset(m.homeDir); err != nil {
				return "", fmt.Errorf("reset onboarding failed: %w", err)
			}
			return "已重置 Onboarding 状态。请退出 TUI（Ctrl+C 或 /exit）后重新运行 whale-code，将自动触发首次引导流程。", nil
		}
		return "Onboarding 交互流程需在 TUI 外执行（避免 stdin 冲突）。\n" +
			"  - 首次运行：退出 TUI 后直接运行 whale-code，自动触发 4 步骤引导\n" +
			"  - 重置重跑：使用 /onboarding reset，然后退出 TUI 重启\n" +
			"  - 查看帮助：输入 /help 查看所有命令", nil
	}

	// Set init callback（N18：接线 /init 命令，生成项目级 .whale/ 目录 + WHALE.md）
	// 审查 v1 B2 修复：合并原 handleInit 的 WHALE.md 创建逻辑，避免 switch-case 双重执行。
	m.cmdRegistry.OnInit = func(args string) (string, error) {
		whaleDir := filepath.Join(m.workDir, ".whale")
		if err := os.MkdirAll(whaleDir, 0755); err != nil {
			return "", fmt.Errorf("create .whale dir failed: %w", err)
		}
		// 生成默认 skills/ 目录
		if err := os.MkdirAll(filepath.Join(whaleDir, "skills"), 0755); err != nil {
			return "", fmt.Errorf("create skills dir failed: %w", err)
		}
		// 生成 WHALE.md（合并原 handleInit 行为，保留项目上下文文件能力）
		whaleMd := `# Whale Code Project Info

This file is auto-read by Whale Code for project context.

## Project Structure
Describe your project structure here.

## Coding Standards
Describe your coding conventions here.

## Notes
Add any important notes here.
`
		whaleMdPath := filepath.Join(m.workDir, "WHALE.md")
		var initMsg string
		if _, err := os.Stat(whaleMdPath); err == nil {
			initMsg = "WHALE.md already exists"
		} else if err := util.AtomicWriteFile(whaleMdPath, []byte(whaleMd), 0644); err != nil {
			return "", fmt.Errorf("create WHALE.md failed: %w", err)
		} else {
			initMsg = "Created WHALE.md (edit it to describe your project)"
			m.aiService.MagicDocs().Refresh()
		}
		return fmt.Sprintf("项目配置已初始化：.whale/ 目录已创建（含 skills/ 子目录）。%s", initMsg), nil
	}

	// Initialize GitHub tools
	m.githubTools = github.NewGitHubTools(client, workDir)
	// Inject RecoveryCoordinator into GitHub AIAdapter for coordinated retry/degrade/compress
	if adapter := m.githubTools.GetAIAdapter(); adapter != nil {
		adapter.SetRecoveryConfig(m.recoveryCoordinator, m.retryConfig, m.fallback)
	}
	if err := m.githubTools.RegisterSpecs(m.registry); err != nil {
		// Silently fail, do not affect startup
	}
	if err := m.githubTools.RegisterHandlers(m.executor); err != nil {
		// Silently fail, do not affect startup
	}

	// Set GitHub slash command callback
	m.cmdRegistry.OnGitHub = func(args string) string {
		return m.githubTools.HandleCommand(context.Background(), args)
	}

	// Set local /review slash command callback (read-only multi-agent review, not published to GitHub)
	m.cmdRegistry.OnReview = func(args string) string {
		return m.githubTools.HandleLocalReview(args)
	}

	// /：/remote 的 OnRemote 回调接线已删除；同日晚 /remote 整面
	// 砍除，handleRemote 与 remote.go 一并删除。
	// ★ 别在此重新接回任何 remote 闭包：真机实测 connect 三缺陷实锤（RPC 通道无人接听 /
	// 部署不交叉编译 / workDir 传本地路径），恢复请先推翻 拍板并重做架构评审。

	// N24：通知系统命令回调
	m.cmdRegistry.OnNotifyList = func() string {
		return m.handleNotifyList()
	}
	m.cmdRegistry.OnNotifyClear = func() string {
		return m.handleNotifyClear()
	}
	// OnNotifyDismiss 不在此接线（家族收口，见上方注释块）；
	// /notify dismiss 活面 = 本地 case "notify dismiss" → handleNotifyDismiss。

	// N26：/goal 命令回调（，单入口 + args 解析模式）
	m.cmdRegistry.OnGoal = func(args string) string {
		return m.handleGoalCmd(args)
	}

	// P0-14：/import 命令回调，接线到真实的 handleImportSession（替换 handleImport 桩）
	m.cmdRegistry.OnImport = func(args string) (string, error) {
		return m.handleImportSession(args), nil
	}

	// P0-15：/workflow 命令回调，接线到 handleWorkflow（诚实降级实现）
	m.cmdRegistry.OnWorkflow = func(args string) (string, error) {
		return m.handleWorkflow(args)
	}

	// N21: Set export/share/copy slash command callbacks
	redactor := &export.DefaultRedactor{}
	exporter := export.NewExporter(m.session, redactor, homeDir)
	clipboard := export.NewClipboard()

	m.cmdRegistry.OnExport = func(args string) string {
		parts := splitExportArgs(args)
		format := export.FmtMarkdown
		outPath := ""
		for i := 0; i < len(parts); i++ {
			if parts[i] == "--format" && i+1 < len(parts) {
				format = export.Format(parts[i+1])
				i++
			} else if parts[i] == "--full" {
				// handled below via separate path
			} else if !strings.HasPrefix(parts[i], "--") {
				outPath = parts[i]
			}
		}
		fullMeta := strings.Contains(args, "--full")
		result, err := exporter.Export(export.ExportOption{
			Format:     format,
			OutputPath: outPath,
			FullMeta:   fullMeta,
		})
		if err != nil {
			return biSame("Export failed: %v", "导出失败: %v", err)
		}
		return biSame("✅ Export succeeded\nFormat: %s\nPath: %s\nMessages: %d\nRedacted: %d",
			"✅ 导出成功\n格式: %s\n路径: %s\n消息数: %d\n脱敏: %d 处",
			result.Format, result.Path, result.MessageCount, result.Redacted)
	}

	m.cmdRegistry.OnExportUser = func(args string) string {
		parts := splitExportArgs(args)
		outPath := ""
		for _, p := range parts {
			if !strings.HasPrefix(p, "--") {
				outPath = p
				break
			}
		}
		result, err := exporter.Export(export.ExportOption{
			Format:     export.FmtMarkdown,
			UserOnly:   true,
			OutputPath: outPath,
		})
		if err != nil {
			return biSame("Export failed: %v", "导出失败: %v", err)
		}
		return biSame("✅ Export succeeded (user messages only)\nPath: %s\nMessages: %d\nRedacted: %d",
			"✅ 导出成功（仅用户消息）\n路径: %s\n消息数: %d\n脱敏: %d 处",
			result.Path, result.MessageCount, result.Redacted)
	}

	m.cmdRegistry.OnExportFormat = func(args string) string {
		parts := splitExportArgs(args)
		if len(parts) == 0 {
			return bi("Usage: /export-format json|markdown|html [path]",
				"用法: /export-format json|markdown|html [path]")
		}
		format := export.Format(parts[0])
		if format == export.FmtHTML {
			return bi("HTML format is not supported; available formats: markdown, json",
				"HTML 格式暂不支持，可用格式：markdown, json")
		}
		outPath := ""
		if len(parts) > 1 {
			outPath = parts[1]
		}
		fullMeta := strings.Contains(args, "--full")
		result, err := exporter.Export(export.ExportOption{
			Format:     format,
			OutputPath: outPath,
			FullMeta:   fullMeta,
		})
		if err != nil {
			return biSame("Export failed: %v", "导出失败: %v", err)
		}
		return biSame("✅ Export succeeded\nFormat: %s\nPath: %s\nMessages: %d\nRedacted: %d",
			"✅ 导出成功\n格式: %s\n路径: %s\n消息数: %d\n脱敏: %d 处",
			result.Format, result.Path, result.MessageCount, result.Redacted)
	}

	m.cmdRegistry.OnShare = func(args string) string {
		result, err := exporter.Export(export.ExportOption{
			Format: export.FmtMarkdown,
		})
		if err != nil {
			return biSame("Export failed: %v", "导出失败: %v", err)
		}
		if err := clipboard.CopyPath(result.Path); err != nil {
			return biSame("Export succeeded, but copying the path to the clipboard failed: %v\nPath: %s",
				"导出成功，但复制到剪贴板失败: %v\n路径: %s", err, result.Path)
		}
		return biSame("✅ Shared\nPath copied to clipboard: %s",
			"✅ 分享成功\n路径已复制到剪贴板: %s", result.Path)
	}

	m.cmdRegistry.OnCopy = func(args string) string {
		msgs := m.session.GetMessages()
		var text string
		args = strings.TrimSpace(args)
		switch {
		case args == "" || args == "last":
			for i := len(msgs) - 1; i >= 0; i-- {
				if msgs[i].Role == session.RoleAssistant && !msgs[i].Tombstoned {
					text = msgs[i].TextContent()
					break
				}
			}
		case args == "all":
			var sb strings.Builder
			for _, msg := range msgs {
				if msg.Tombstoned {
					continue
				}
				if msg.Role == session.RoleAssistant {
					sb.WriteString(msg.TextContent())
					sb.WriteString("\n\n")
				}
			}
			text = strings.TrimSpace(sb.String())
		default:
			n, err := strconv.Atoi(args)
			if err != nil || n <= 0 {
				return bi("Usage: /copy [last|all|N]\n  last: copy the last assistant reply\n  all: copy all assistant replies\n  N: copy the Nth message",
					"用法: /copy [last|all|N]\n  last: 复制最后一条助手回复\n  all: 复制所有助手回复\n  N: 复制第 N 条消息")
			}
			count := 0
			for i := len(msgs) - 1; i >= 0; i-- {
				if msgs[i].Role == session.RoleAssistant && !msgs[i].Tombstoned {
					count++
					if count == n {
						text = msgs[i].TextContent()
						break
					}
				}
			}
			if text == "" {
				return biSame("No assistant reply found at index %d", "未找到第 %d 条助手回复", n)
			}
		}
		if text == "" {
			return bi("Nothing to copy", "没有可复制的内容")
		}
		// S7: 复制前强制脱敏，防止密钥泄露到剪贴板
		scrubbed, redactedCount := redactor.Scrub(text)
		text = scrubbed
		if err := clipboard.Copy(text); err != nil {
			return biSame("Failed to copy to clipboard: %v", "复制到剪贴板失败: %v", err)
		}
		if redactedCount > 0 {
			return biSame("✅ Copied to clipboard (%d chars, %d redacted)", "✅ 已复制到剪贴板（%d 字符，脱敏 %d 处）", len(text), redactedCount)
		}
		return biSame("✅ Copied to clipboard (%d chars)", "✅ 已复制到剪贴板（%d 字符）", len(text))
	}

	// 设置快速模型：优先使用 cfg.FastModel（用户显式配置），为空时才用 findFastModel 自动推断兜底
	// 修复 FastModel 孤岛字段（P7-1，）：原实现忽略 cfg.FastModel，仅按名字猜
	if cfg.FastModel != "" {
		m.fastModel = cfg.FastModel
	} else {
		m.fastModel = findFastModel(reg.Names())
	}
	m.outputStyle = "default"
	// ★ 档3 接线：让 settings.json 里的 thinking 配置真正生效。
	// 修复前 `config.ThinkingEnabled`（*bool，三态本来就有）**全仓零读取点**，
	// 而 `SessionModel.thinkingEnabled` 也**没有任何初始化点**（下面只有一行硬编码 budget）
	// ⇒ 用户在 settings 里怎么配都没用，请求里永远是「假关闭」。
	// 三态直传（零转换）：nil=未指定（不下发，交服务端默认）· true=显式开 · false=显式关。
	m.thinkingEnabled = cfg.ThinkingEnabled
	m.thinkingBudgetTokens = thinkingBudgetFromConfig(cfg)

	// T3 S3: 初始化日常对话提问配置 + 缓存。
	// askSettings 从 ~/.whale/settings.json 读取 interaction.ask_threshold（默认 1，范围 0-3）。
	// askCache 5 分钟 TTL 防同问题重复问（进程内 map，v0.2 不持久化）。
	m.askSettings = LoadAskSettings()
	m.askCache = NewAskCache()

	// Initialize service interfaces with sub-struct implementations.
	// Each embedded sub-struct now implements its corresponding service interface,
	// enabling interface-based access for decoupling and testability.
	// Use reassignServices() after value-receiver operations to keep pointers fresh.
	m.reassignServices()

	// H11N27 接线（Step 1，独立于 T2-A，）。
	// wiringCtx 在 main.go 退出时通过 tuiShutdown.RegisterCleanup 联动 cancel，
	// 取消 ForgettingScanner 等 wiring goroutine（避免泄漏，吸纳优化 3）。
	wiringCtx, wiringCancel := context.WithCancel(context.Background())
	m.wiringCancel = wiringCancel
	m.wiringWait = wireH11N27Memory(cfg, client, m.cmdRegistry, homeDir, workDir, wiringCtx)

	// 桶 X3 病灶1 接线层补充：把 memory.GetCapability / memory.ReflectMemories
	// 适配为闭包注入到 Executor.CapabilityLookup / ReflectTrigger 函数字段。
	// 必须在 wireH11N27Memory 之后调用（依赖 SetGlobalMergeLayer + SetAICaller 已完成），
	// 否则 ReflectMemories 会因 aiCaller==nil 返回 error。
	// executor==nil 时 wireExecutorMemoryCallbacks 内部静默降级（不 panic）。
	// homeDir/workDir 同时供 P4 MemorySearchFunc（memorybridge 检索 Source）构建。
	wireExecutorMemoryCallbacks(m.executor, homeDir, workDir)

	// H12 接线（Step 3，仅当 T2-A ExperienceOrchestrator 已实例化时，）。
	// B-P0-5 修复：setupOrchestrator 现已创建 ExperienceOrchestrator 并注入 m.experienceOrch，
	// 故此 guard 自动放行，wireH12Evolution 自动执行（EvolutionLoop 构造 + 注入 + 后台 TTL 扫描）。
	// ExperienceOrchestrator 创建失败时 m.experienceOrch 为 nil，此处走降级（跳过 H12 wiring）。
	if m.experienceOrch != nil {
		persistencePath := filepath.Join(whalehome.Dir(), "orchestrator", "convergence.json")
		wireH12Evolution(cfg, m.experienceOrch, m.expStore, m.qualityStore, m.l1Scorer, persistencePath, wiringCtx)
	}

	return m
}

// SetVersion sets current version number to command registry
func (m *Model) SetVersion(version string) {
	m.cmdRegistry.Version = version
}

// skillProviderAdapter 将 *skill.Manager 适配为 typeahead.SkillProvider 接口
// P0-08（M11-03 + M25-01 合并）：让 typeahead 包在不直接依赖 skill 包的前提下
// 通过 ListSkills() 拿到 skill 列表，用于 # 触发符补全
type skillProviderAdapter struct {
	mgr **skill.Manager // 指向 executor.SkillMgr 字段的指针（executor 内部可能重建 Manager，故用二级指针）
}

// newSkillProviderAdapter 创建适配器，参数为指向 *skill.Manager 的指针
// 用二级指针是为了在 executor 重建 SkillMgr 时仍能取到最新实例
func newSkillProviderAdapter(mgrPtr **skill.Manager) *skillProviderAdapter {
	return &skillProviderAdapter{mgr: mgrPtr}
}

// ListSkills 实现 typeahead.SkillProvider 接口
// 返回当前 SkillMgr 中所有 skill 的元数据（name + description）
// SkillMgr 为 nil 时返回 nil（# 触发符不弹窗）
func (a *skillProviderAdapter) ListSkills() []typeahead.SkillItem {
	if a == nil || a.mgr == nil || *a.mgr == nil {
		return nil
	}
	skills := (*a.mgr).List()
	if len(skills) == 0 {
		return nil
	}
	items := make([]typeahead.SkillItem, 0, len(skills))
	for _, s := range skills {
		items = append(items, typeahead.SkillItem{
			Name:        s.Name,
			Description: s.Description,
		})
	}
	return items
}

// CancelWiring 取消 wiring goroutine，并等待 H11N27 扫描协程退出（H11N27+H12 接线公共方法）。
// 供 main.go 在 tuiShutdown.RegisterCleanup 中调用，替代直接访问 unexported 字段 wiringCancel。
// 多次调用安全：context.CancelFunc 幂等，Wait 在协程退出后可重复返回。
func (m *Model) CancelWiring() {
	if m.wiringCancel != nil {
		m.wiringCancel()
	}
	if m.wiringWait != nil {
		m.wiringWait()
	}
	// T-1 修复（全模块审查）：停止 loopScheduler goroutine，避免泄漏。
	// Stop 使用 sync.Once，多次调用安全。
	if m.loopScheduler != nil {
		m.loopScheduler.Stop()
	}
}

// ResumeLastSession resume the most recent session
// ResumeLastSession 恢复最近一个会话（`-c` / `--continue`）。
//
// 返回值只承载**占用冲突**（*session.SessionInUseError）—— 存储故障仍按
// 既有行为静默跳过（只记日志），不得因为读不到列表就阻断启动。
func (m *Model) ResumeLastSession() error {
	// 修复：main.go 在 p.Run() 之前调用本方法（whale -c），
	// 此时 model 是 NewModel 按值返回后的副本，接口字段仍指向 NewModel 局部变量
	// （悬空别名）。若不先 reassignServices，续接结果会写进旧副本，真实 model
	// 仍是空会话——用户现象：-c 启动后无上次历史、新一轮另起新会话文件。
	// 与 SendPrompt 入口的既有约定保持一致（app.go:969）。
	m.reassignServices()
	summaries, err := m.sessionService.Store().List()
	if err != nil {
		// canary（对抗式审查）：存储故障必须可观测，否则用户无法区分
		// "确实没有会话"和"会话目录读失败"，-c 静默无历史会掩盖真实故障。
		log.Printf("[tui] ResumeLastSession: list sessions failed: %v", err)
		return nil
	}
	if len(summaries) == 0 {
		return nil
	}

	latest := summaries[0]
	loaded, err := m.sessionService.Store().Load(latest.ID)
	if err != nil {
		// canary（对抗式审查）：会话文件损坏/不可读必须可观测。
		log.Printf("[tui] ResumeLastSession: load session %q failed: %v", latest.ID, err)
		return nil
	}

	return m.applyResumedSession(latest.ID, loaded, fmt.Sprintf("Resumed session: %s (%d messages)", latest.ID, len(loaded.GetMessages())))
}

// ResumeSessionByID 恢复**用户点名**的会话（UUID / UUID 前缀 / 可读名 / 旧式文件名）。
//
// 与 ResumeLastSession 的关键差别：点名失败必须返回错误，而不是静默开一个新会话。
// 静默兜底会让用户以为接上了旧对话，实际另起一份 —— 这是比报错更糟的失败模式。
func (m *Model) ResumeSessionByID(idOrName string) error {
	// 修复（同 ResumeLastSession）：p.Run() 前的值副本调用，必须先修复接口别名。
	m.reassignServices()
	store := m.sessionService.Store()
	if store == nil {
		return fmt.Errorf("session store unavailable")
	}
	resolved, err := store.Resolve(idOrName)
	if err != nil {
		return err
	}
	loaded, err := store.Load(resolved)
	if err != nil {
		return fmt.Errorf("load session %s: %w", resolved, err)
	}
	return m.applyResumedSession(resolved, loaded, fmt.Sprintf("Resumed session: %s (%d messages)", resolved, len(loaded.GetMessages())))
}

// applyResumedSession 把一个已加载的会话装载进当前 Model —— 会话恢复的**唯一装载点**。
//
// 收敛原因：恢复会话要做的事很多（切换当前会话、发切换事件、重置计数与输入历史、
// 清空排队输入、重渲染历史），此前 ResumeLastSession / /resume / handleSessionSwitch
// 各写一份，极易漂移（历史 bug 模式：一侧改了另一侧没改）。新增 --session 后会
// 有第四份，故在此收口：调用方只负责「解析 + 落盘旧会话」，装载统一走这里。
//
// header 是切换后显示的首行文案，由调用方决定措辞（-c / --session / /resume 的
// 来源不同，提示语也不同），装载动作本身保持单一。
func (m *Model) applyResumedSession(id string, loaded *session.Session, header string) error {
	// ★ 占用闸门：装载前必须先拿到该会话的**占用锁**；拿不到（另一个存活进程
	// 正开着它）就**不装载**并把错误上抛。
	//
	// 为什么闸门必须装在这里：本函数是「会话恢复的唯一装载点」，-c / --session /
	// /resume / /session switch 四条路都经过它 ⇒ 一处收口胜过四处各加一道 if。
	//
	// 不闸门会怎样（修复前的实测）：两个窗口都能进入同一会话，各自 append，
	// transcript 按写入时刻交错成一段前后不接的对话（探针实测「被拒进程数=0」），
	// 且两个窗口的内存上下文互相看不到对方的新消息。
	if err := m.holdSession(id); err != nil {
		return err
	}
	oldID := m.sessionService.SessionName()
	m.sessionService.SetSessionName(id)
	m.sessionService.SetCurrentSession(loaded)
	// C20: 发布会话切换事件，供 Context 模块清空缓存并重载（bus 未注入时静默跳过）
	m.sessionService.Store().NotifySessionSwitch(oldID, id)
	m.sessionService.SetSavedMsgCount(len(loaded.GetMessages()))
	m.sessionService.SetFirstUserInput("")
	m.sessionService.SetInputTokens(0)
	m.sessionService.SetOutputTokens(0)
	m.sessionService.SetInputHistory(nil)
	m.sessionService.SetHistoryIdx(0)
	m.sessionService.SetHistoryDraft("")

	// 会话切换时清空排队队列（避免跨会话污染）
	m.ClearQueuedInputs()

	m.sessionService.SetMessages([]string{titleStyle.Render(header)})
	// 工具结果序号在**这批消息**上算一次：折叠提示的 "/tool <N>" 与 /tool 命令的 [N/M]
	// 共用这一个 SSOT（toolResultSeq），避免两处各数一遍（C1 切口②）。
	toolSeq := newToolResultSeq(loaded.GetMessages())
	for _, msg := range loaded.GetMessages() {
		switch msg.Role {
		case session.RoleUser:
			m.sessionService.AddMessage(formatUserMessage(msg.TextContent(), msg.Timestamp))
		case session.RoleAssistant:
			// ★ 批 C：恢复路径与实时路径**共用同一登记口径**（formatAssistantMessageSource）——
			// 否则同一个省略行"实时登记、恢复不登记"，拖选复制在 resume 后静默退回修前。
			display, srcs := m.formatAssistantMessageSource(msg.Blocks, msg.Timestamp)
			m.sessionService.AddMessageWithSources(display, srcs)
		case session.RoleTool:
			display, srcs := formatToolResultMessageSource(msg, toolSeq.seqOf(msg, 0))
			m.sessionService.AddMessageWithSources(display, srcs)
		case session.RoleSystem:
			// 切口③：压缩分界 marker（Subtype=="compact_boundary"）是 UI-only 分界信号，
			// resume/fork 时渲染成可见分界；其余 system 消息（如 system prompt）不进窗口。
			if msg.Subtype == "compact_boundary" {
				m.sessionService.AddMessage(formatCompactBoundary())
			}
		}
	}
	m.refreshTranscriptViewport()
	m.uiService.Viewport().GotoBottom()
	return nil
}

// holdSession 让本进程长期占用 id（占用锁随进程存活，切会话时自动释放上一个）。
//
// store 为 nil（无持久化 / 测试装配）时不设闸门 —— 没有持久化就没有「两个窗口写
// 同一份文件」这件事，闸门也就无事可做。
func (m *Model) holdSession(id string) error {
	if id == "" {
		return nil
	}
	store := m.sessionService.Store()
	if store == nil {
		return nil
	}
	return store.HoldSession(id)
}

// ForkLastSession forks the latest session: creates new session and copies history
// Mirrors a comparable CLI's --fork-session: new ID + copy history, both evolve independently
func (m *Model) ForkLastSession() {
	// 修复（同 ResumeLastSession）：p.Run() 前的值副本调用，必须先修复接口别名。
	m.reassignServices()
	summaries, err := m.sessionService.Store().List()
	if err != nil {
		// canary（对抗式审查）：同 ResumeLastSession，存储故障必须可观测。
		log.Printf("[tui] ForkLastSession: list sessions failed: %v", err)
		return
	}
	if len(summaries) == 0 {
		return
	}

	latest := summaries[0]
	// 存储键是 ID（文件名/UUID），Name 只是展示用别名 —— 两者已分离，混用会把
	// 「别名」当路径去 load，得到 no such file。
	forkID, err := m.sessionService.Store().ForkSession(latest.ID)
	if err != nil {
		// canary（对抗式审查）：fork 失败静默会导致用户以为已 fork。
		log.Printf("[tui] ForkLastSession: fork session %q failed: %v", latest.ID, err)
		return
	}

	// Load the new forked session
	loaded, err := m.sessionService.Store().Load(forkID)
	if err != nil {
		// canary（对抗式审查）：fork 后加载失败必须可观测。
		log.Printf("[tui] ForkLastSession: load forked session %q failed: %v", forkID, err)
		return
	}

	m.sessionService.SetSessionName(forkID)
	m.sessionService.SetCurrentSession(loaded)
	m.sessionService.SetSavedMsgCount(len(loaded.GetMessages()))
	m.sessionService.SetFirstUserInput("")
	m.sessionService.SetInputTokens(0)
	m.sessionService.SetOutputTokens(0)
	m.sessionService.SetInputHistory(nil)
	m.sessionService.SetHistoryIdx(0)
	m.sessionService.SetHistoryDraft("")

	m.sessionService.SetMessages([]string{titleStyle.Render(fmt.Sprintf("Forked session: %s → %s (%d messages)", latest.Name, forkID, len(loaded.GetMessages())))})
	// 工具结果序号在**这批消息**上算一次：折叠提示的 "/tool <N>" 与 /tool 命令的 [N/M]
	// 共用这一个 SSOT（toolResultSeq），避免两处各数一遍（C1 切口②）。
	toolSeq := newToolResultSeq(loaded.GetMessages())
	for _, msg := range loaded.GetMessages() {
		switch msg.Role {
		case session.RoleUser:
			m.sessionService.AddMessage(formatUserMessage(msg.TextContent(), msg.Timestamp))
		case session.RoleAssistant:
			display := m.formatAssistantMessage(msg.Blocks, msg.Timestamp)
			m.sessionService.AddMessage(display)
		case session.RoleTool:
			m.sessionService.AddMessage(formatToolResultMessage(msg, toolSeq.seqOf(msg, 0)))
		case session.RoleSystem:
			// 切口③：压缩分界 marker（Subtype=="compact_boundary"）是 UI-only 分界信号，
			// resume/fork 时渲染成可见分界；其余 system 消息（如 system prompt）不进窗口。
			if msg.Subtype == "compact_boundary" {
				m.sessionService.AddMessage(formatCompactBoundary())
			}
		}
	}
}

// ForkSessionByID forks a session by specified ID
func (m *Model) ForkSessionByID(sessionID string) {
	// 修复（同 ResumeLastSession）：p.Run() 前的值副本调用，必须先修复接口别名。
	m.reassignServices()
	forkID, err := m.sessionService.Store().ForkSessionByID(sessionID)
	if err != nil {
		m.addMessage(errorStyle.Render(fmt.Sprintf("Fork session failed: %v", err)))
		return
	}

	loaded, err := m.sessionService.Store().Load(forkID)
	if err != nil {
		m.addMessage(errorStyle.Render(fmt.Sprintf("Load forked session failed: %v", err)))
		return
	}

	m.sessionService.SetSessionName(forkID)
	m.sessionService.SetCurrentSession(loaded)
	m.sessionService.SetSavedMsgCount(len(loaded.GetMessages()))
	m.sessionService.SetFirstUserInput("")
	m.sessionService.SetInputTokens(0)
	m.sessionService.SetOutputTokens(0)
	m.sessionService.SetInputHistory(nil)
	m.sessionService.SetHistoryIdx(0)
	m.sessionService.SetHistoryDraft("")

	m.sessionService.SetMessages([]string{titleStyle.Render(fmt.Sprintf("Forked session: %s → %s (%d messages)", sessionID, forkID, len(loaded.GetMessages())))})
	// 工具结果序号在**这批消息**上算一次：折叠提示的 "/tool <N>" 与 /tool 命令的 [N/M]
	// 共用这一个 SSOT（toolResultSeq），避免两处各数一遍（C1 切口②）。
	toolSeq := newToolResultSeq(loaded.GetMessages())
	for _, msg := range loaded.GetMessages() {
		switch msg.Role {
		case session.RoleUser:
			m.sessionService.AddMessage(formatUserMessage(msg.TextContent(), msg.Timestamp))
		case session.RoleAssistant:
			display := m.formatAssistantMessage(msg.Blocks, msg.Timestamp)
			m.sessionService.AddMessage(display)
		case session.RoleTool:
			m.sessionService.AddMessage(formatToolResultMessage(msg, toolSeq.seqOf(msg, 0)))
		case session.RoleSystem:
			// 切口③：压缩分界 marker（Subtype=="compact_boundary"）是 UI-only 分界信号，
			// resume/fork 时渲染成可见分界；其余 system 消息（如 system prompt）不进窗口。
			if msg.Subtype == "compact_boundary" {
				m.sessionService.AddMessage(formatCompactBoundary())
			}
		}
	}
}

// ★ ：原 CheckSessionLock（启动时「看一眼」锁、被占则打印一句提示）已删除。
//
// 它拦不住任何东西 —— 那把锁当时只在**单次写入**期间被持有（毫秒级），启动时几乎
// 永远落在空隙里（探针实测：两个进程各自连续写，被拒进程数 = 0），两个窗口照旧
// 都能进入同一会话各自 append。现在「占用」由 applyResumedSession 里的**真实持有**
// 表达（holdSession → store.HoldSession）：拿不到就拒绝装载并上抛，拿得到则自己
// 持有到进程退出 —— 不再需要「看一眼」这种事后检查。

// buildInitialMessages builds the initial message list for the TUI, appending
// a session-store error notice (if any) to the welcome message so the user
// is aware that session persistence is unavailable.
func buildInitialMessages(model, workDir, permMode string, storeErr error) []string {
	msgs := []string{buildWelcome(model, workDir, permMode)}
	if storeErr != nil {
		msgs = append(msgs, lipgloss.NewStyle().
			Foreground(lipgloss.Color("#ED8796")).
			Render("Warning: session store unavailable: "+storeErr.Error()))
	}
	return msgs
}
