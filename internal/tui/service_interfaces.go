package tui

import (
	"context"
	"encoding/json"

	"github.com/anchorandfish/whale-code/internal/ai"
	"github.com/anchorandfish/whale-code/internal/bridge"
	"github.com/anchorandfish/whale-code/internal/command"
	"github.com/anchorandfish/whale-code/internal/config"
	ctxpkg "github.com/anchorandfish/whale-code/internal/context"
	"github.com/anchorandfish/whale-code/internal/daemon"
	"github.com/anchorandfish/whale-code/internal/filehistory"
	"github.com/anchorandfish/whale-code/internal/github"
	"github.com/anchorandfish/whale-code/internal/keybindings"
	"github.com/anchorandfish/whale-code/internal/monitor"
	"github.com/anchorandfish/whale-code/internal/permission"
	"github.com/anchorandfish/whale-code/internal/search"
	"github.com/anchorandfish/whale-code/internal/session"
	"github.com/anchorandfish/whale-code/internal/tools"
	"github.com/charmbracelet/bubbles/textarea"
	"github.com/charmbracelet/bubbles/viewport"
)

// UIService defines the interface for UI update and rendering operations.
// Implementations: UIModel (TUI mode), runnerCore (Plain/Bridge mode).
type UIService interface {
	// Update handles a message and returns a model update and command
	Update(msg interface{}) (interface{}, interface{})
	// View renders the current view
	View() string
	// Width returns the current terminal width
	Width() int
	// Height returns the current terminal height
	Height() int
	// SetWidth sets the terminal width
	SetWidth(w int)
	// SetHeight sets the terminal height
	SetHeight(h int)
	// Viewport returns the viewport model
	Viewport() *viewport.Model
	// Textarea returns the textarea model
	Textarea() *textarea.Model
	// SpinnerIdx returns the spinner animation index
	SpinnerIdx() int
	// SetSpinnerIdx sets the spinner animation index
	SetSpinnerIdx(idx int)
	// Ready returns whether the UI is ready
	Ready() bool
	// SetReady sets the ready flag
	SetReady(ready bool)
	// Dirty returns whether the display needs refresh
	Dirty() bool
	// SetDirty sets the dirty flag
	SetDirty(dirty bool)
	// XOffset returns the horizontal offset
	XOffset() int
	// SetXOffset sets the horizontal offset
	SetXOffset(offset int)
	// InputFocusedOnSend returns whether input is focused on send
	InputFocusedOnSend() bool
	// SetInputFocusedOnSend sets the send focus flag
	SetInputFocusedOnSend(focused bool)
	// InputFocusedOnStop returns whether input is focused on stop
	InputFocusedOnStop() bool
	// SetInputFocusedOnStop sets the stop focus flag
	SetInputFocusedOnStop(focused bool)
	// VimMode returns whether vim mode is enabled
	VimMode() bool
	// SetVimMode sets the vim mode flag
	SetVimMode(enabled bool)
	// VimModeState returns the current vim mode state
	VimModeState() VimModeState
	// SetVimModeState sets the vim mode state
	SetVimModeState(state VimModeState)
	// VimPendingKey returns the pending vim key
	VimPendingKey() string
	// SetVimPendingKey sets the pending vim key
	SetVimPendingKey(key string)
	// Keybindings returns the keybindings manager
	Keybindings() *keybindings.Manager
	// MDRenderer returns the markdown renderer
	MDRenderer() *MarkdownRenderer
	// ImagePasteHandler returns the image paste handler
	ImagePasteHandler() *ImagePasteHandler
	// PendingImages returns the pending images
	PendingImages() []PendingImage
	// SetPendingImages sets the pending images
	SetPendingImages(images []PendingImage)
	// DocLinks returns the document links
	DocLinks() []FileLink
	// SetDocLinks sets the document links
	SetDocLinks(links []FileLink)
	// DocLinkIdx returns the document link index
	DocLinkIdx() int
	// SetDocLinkIdx sets the document link index
	SetDocLinkIdx(idx int)
}

// SessionService defines the interface for session management operations.
// Implementations: SessionModel (TUI mode), runnerCore (Plain/Bridge mode).
type SessionService interface {
	// LoadSession loads a session by name
	LoadSession(name string) error
	// SaveSession saves the current session
	SaveSession() error
	// CurrentSession returns the current session
	CurrentSession() *session.Session
	// SetCurrentSession sets the current session
	SetCurrentSession(sess *session.Session)
	// SessionName returns the current session name
	SessionName() string
	// SetSessionName sets the current session name
	SetSessionName(name string)
	// WorkDir returns the working directory
	WorkDir() string
	// HomeDir returns the home directory
	HomeDir() string
	// State returns the current interaction state
	State() InteractionState
	// SetState sets the interaction state
	SetState(state InteractionState)
	// ModelName returns the current AI model name
	ModelName() string
	// SetModelName sets the current AI model name
	SetModelName(name string)
	// Config returns the current configuration
	Config() *config.Config
	// Store returns the session store
	Store() *session.Store
	// UsageTracker returns the usage tracker
	UsageTracker() *ai.UsageTracker
	// AddMessage appends a message to the message list
	AddMessage(msg string)
	// AddMessageWithSources appends a display message together with its per-line
	// source map (batch C / transcript_source.go): "what does this line represent"
	// is registered by the render producer, not reverse-looked-up from screen text.
	AddMessageWithSources(msg string, sources []lineSource)
	// Messages returns the message list
	Messages() []string
	// MessageEntries returns display entries (text + per-line source map).
	MessageEntries() []displayEntry
	// SetMessagesWithSources replaces the message list, carrying per-message
	// source maps (entries[i] text ← msgs[i], sources ← sources[i]).
	SetMessagesWithSources(msgs []string, sources [][]lineSource)
	// SetMessages replaces the message list
	SetMessages(msgs []string)
	// FirstUserInput returns the first user input in the session
	FirstUserInput() string
	// SetFirstUserInput sets the first user input
	SetFirstUserInput(input string)
	// SavedMsgCount returns the saved message count
	SavedMsgCount() int
	// SetSavedMsgCount sets the saved message count
	SetSavedMsgCount(count int)
	// InputTokens returns the input token count
	InputTokens() int
	// SetInputTokens sets the input token count
	SetInputTokens(tokens int)
	// OutputTokens returns the output token count
	OutputTokens() int
	// SetOutputTokens sets the output token count
	SetOutputTokens(tokens int)
	// RequestCount returns the number of provider requests attributed to the current session
	RequestCount() int
	// SetRequestCount sets the provider request count
	SetRequestCount(count int)
	// Quitting returns whether the application is quitting
	Quitting() bool
	// SetQuitting sets the quitting flag
	SetQuitting(q bool)
	// AvailableModels returns the list of available model names
	AvailableModels() []string
	// SetAvailableModels sets the list of available model names
	SetAvailableModels(models []string)
	// CurrentText returns the current streaming text
	CurrentText() string
	// SetCurrentText sets the current streaming text
	SetCurrentText(text string)
	// CurrentThinking returns the current thinking content
	CurrentThinking() string
	// SetCurrentThinking sets the current thinking content
	SetCurrentThinking(thinking string)
	// Interrupted returns whether the stream was interrupted
	Interrupted() bool
	// SetInterrupted sets the interrupted flag
	SetInterrupted(interrupted bool)
	// ThinkingEnabled 返回三态 thinking 设置（nil = 未指定，交回服务端默认）
	ThinkingEnabled() *bool
	// SetThinkingEnabled 设置三态 thinking（nil = 未指定）
	SetThinkingEnabled(enabled *bool)
	// ThinkingBudgetTokens returns the thinking budget in tokens
	ThinkingBudgetTokens() int
	// SetThinkingBudgetTokens sets the thinking budget in tokens
	SetThinkingBudgetTokens(tokens int)
	// CurrentAgent returns the current agent name
	CurrentAgent() string
	// SetCurrentAgent sets the current agent name
	SetCurrentAgent(agent string)
	// ActiveSkills returns the list of active skills
	ActiveSkills() []string
	// SetActiveSkills sets the list of active skills
	SetActiveSkills(skills []string)
	// CompactCount returns the compaction count
	CompactCount() int
	// SetCompactCount sets the compaction count
	SetCompactCount(count int)
	// AutoCompactCooldown returns whether auto-compact is in cooldown
	AutoCompactCooldown() bool
	// SetAutoCompactCooldown sets the auto-compact cooldown flag
	SetAutoCompactCooldown(cooldown bool)
	// PlanMode returns whether plan mode is active
	PlanMode() bool
	// SetPlanMode sets the plan mode flag
	SetPlanMode(plan bool)
	// FastModel returns the fast model name
	FastModel() string
	// SetFastModel sets the fast model name
	SetFastModel(model string)
	// OutputStyle returns the output style
	OutputStyle() string
	// SetOutputStyle sets the output style
	SetOutputStyle(style string)
	// ConfigLoader returns the config loader
	ConfigLoader() *config.Loader
	// CacheBroken returns whether the cache is broken
	CacheBroken() bool
	// SetCacheBroken sets the cache broken flag
	SetCacheBroken(broken bool)
	// CacheBreakCount returns the cache break count
	CacheBreakCount() int
	// SetCacheBreakCount sets the cache break count
	SetCacheBreakCount(count int)
	// PrivacyConfig returns the privacy configuration
	PrivacyConfig() *PrivacyConfig
	// SetPrivacyConfig sets the privacy configuration
	SetPrivacyConfig(cfg *PrivacyConfig)
	// Marks returns the session marks
	Marks() []sessionMark
	// SetMarks sets the session marks
	SetMarks(marks []sessionMark)
	// InputHistory returns the input history
	InputHistory() []string
	// SetInputHistory sets the input history
	SetInputHistory(history []string)
	// HistoryIdx returns the history navigation index
	HistoryIdx() int
	// SetHistoryIdx sets the history navigation index
	SetHistoryIdx(idx int)
	// HistoryDraft returns the history draft
	HistoryDraft() string
	// SetHistoryDraft sets the history draft
	SetHistoryDraft(draft string)
	// ★ 2026-10-03：`ThinkingHistory` / `SetThinkingHistory` / `AddThinkingRecord`
	// 三条声明随 `SessionModel.thinkingHistory` 一并删除 —— 那是「思考记录」的第二份存储，
	// 生产零写入点。唯一真相源是会话存档（assistant 消息的 thinking 块），
	// 由 `/thinkback` 经 `(*Model).thinkingRecords()` 读取。
}

// AIService defines the interface for AI interaction operations.
// Implementations: AIModel (TUI mode), runnerCore (Plain/Bridge mode).
type AIService interface {
	// StreamRequest sends a streaming AI request
	StreamRequest(ctx context.Context, req *ai.Request) (<-chan ai.Event, error)
	// HandleStreamError processes streaming errors
	HandleStreamError(err error) error
	// Client returns the AI client
	Client() *ai.SimpleClient
	// ModelRegistry returns the model registry
	ModelRegistry() *ai.ModelRegistry
	// Budget returns the context budget
	Budget() ctxpkg.Budget
	// SetBudget sets the context budget
	SetBudget(budget ctxpkg.Budget)
	// Estimator returns the token estimator
	Estimator() *ai.TokenEstimator
	// Compactor returns the context compactor
	Compactor() *ctxpkg.Compactor
	// AICompactor returns the AI compactor
	AICompactor() *ctxpkg.AICompactor
	// Fallback returns the fallback tracker
	Fallback() *ai.FallbackTracker
	// RecoveryCoordinator returns the recovery coordinator
	RecoveryCoordinator() *ai.RecoveryCoordinator
	// SetStreamCancel sets the stream cancellation function
	SetStreamCancel(cancel context.CancelFunc)
	// StreamCancel returns the stream cancellation function
	StreamCancel() context.CancelFunc
	// MagicDocs returns the magic docs manager
	MagicDocs() *config.MagicDocs
	// MemoryStore returns the unified memory store
	MemoryStore() *config.MemoryStore
	// MemoryIndex returns the MEMORY.md index
	MemoryIndex() *config.MemoryIndex
	// MemSelector returns the memory selector
	MemSelector() *config.MemorySelector
	// MemExtractor returns the auto memory extractor
	MemExtractor() *config.AutoMemoryExtractor
	// RequestID returns the current request ID
	RequestID() string
	// SetRequestID sets the current request ID
	SetRequestID(id string)
	// RetryConfig returns the retry configuration
	RetryConfig() ai.RetryConfig
	// SetRetryConfig sets the retry configuration
	SetRetryConfig(config ai.RetryConfig)
	// Caching returns the caching integration layer (N11 修复 N2：打通 TUI 路径 cacheInfo 通路)
	Caching() *ai.CachingIntegration
}

// ToolService defines the interface for tool execution operations.
// Implementations: ToolModel (TUI mode), runnerCore (Plain/Bridge mode).
type ToolService interface {
	// ExecuteTool executes a tool by name with the given input
	ExecuteTool(ctx context.Context, name string, input json.RawMessage) (*tools.Result, error)
	// IsConcurrencySafe checks if a tool can run concurrently
	IsConcurrencySafe(name string) bool
	// Executor returns the tool executor
	Executor() *tools.Executor
	// Registry returns the tool registry
	Registry() *tools.Registry
	// CmdRegistry returns the command registry
	CmdRegistry() *command.Registry
	// Policy returns the permission policy
	Policy() *permission.Policy
	// PendingTools returns the list of pending tool calls
	PendingTools() []session.ContentBlock
	// SetPendingTools sets the list of pending tool calls
	SetPendingTools(tools []session.ContentBlock)
	// CurrentToolID returns the current tool ID being processed
	CurrentToolID() string
	// SetCurrentToolID sets the current tool ID
	SetCurrentToolID(id string)
	// ToolInputBuf returns the tool input buffer map
	ToolInputBuf() map[string]string
	// SetToolInputBuf sets the tool input buffer map
	SetToolInputBuf(buf map[string]string)
	// ToolCancel returns the tool cancellation function
	ToolCancel() context.CancelFunc
	// SetToolCancel sets the tool cancellation function
	SetToolCancel(cancel context.CancelFunc)
	// MonitorMgr returns the monitor manager
	MonitorMgr() *monitor.Manager
	// Daemon returns the daemon instance
	Daemon() *daemon.Daemon
	// GitHubTools returns the GitHub tools instance
	GitHubTools() *github.GitHubTools
	// FileHistory returns the unified file history instance (替代旧 CheckpointStore)
	FileHistory() *filehistory.FileHistory
	// Searcher returns the search instance
	Searcher() *search.Searcher
	// PendingConfirm returns the pending confirmation
	PendingConfirm() *PendingConfirm
	// SetPendingConfirm sets the pending confirmation
	SetPendingConfirm(confirm *PendingConfirm)
	// PendingChoice returns the pending choice
	PendingChoice() *PendingChoice
	// SetPendingChoice sets the pending choice
	SetPendingChoice(choice *PendingChoice)
	// ChoiceIdx returns the choice index
	ChoiceIdx() int
	// SetChoiceIdx sets the choice index
	SetChoiceIdx(idx int)
	// LoopScheduler returns the loop scheduler
	LoopScheduler() *tools.LoopScheduler
	// MonitorCh returns the monitor event channel
	MonitorCh() chan monitorEvent
	// MonitorEscape 返回 monitor done 阻塞发送的逃生口（A-5）；零值 Model 返回 nil。
	MonitorEscape() *monitorEscape
	// LoopCh returns the loop fire channel
	LoopCh() chan loopFireMsg
	// ApprovalAudit returns the tool-approval audit sink (delta F, 2026-09-14).
	// nil-safe —— 未接线时返回 nil（Plain/Bridge 模式没有交互式审批 UI）。
	ApprovalAudit() bridge.ApprovalAudit
}

// Ensure sub-structs satisfy the service interfaces at compile time.
// This enables Model to use &m.SessionModel etc. as service implementations.
var _ UIService = (*UIModel)(nil)

var _ SessionService = (*SessionModel)(nil)

var _ AIService = (*AIModel)(nil)

var _ ToolService = (*ToolModel)(nil)

// 2026-09-27：`runnerCoreServiceAdapter` 及其 4 条编译断言已删除。
// 依据：该适配器**全仓零生产构造点**（唯一构造点在 service_test.go），属死码；
// 生产装配恒定走 `reassignServices()` → `&m.UIModel`/`&m.SessionModel`/`&m.AIModel`/`&m.ToolModel`。
// 反悔坐标：`内部工作回执*.bak`（4 件 md5 已对账）。
