package tui

import (
	"fmt"

	"github.com/anchorandfish/whale-code/internal/ai"
	"github.com/anchorandfish/whale-code/internal/config"
	"github.com/anchorandfish/whale-code/internal/session"
)

// LoadSession loads a session by name
func (s *SessionModel) LoadSession(name string) error {
	if s.store == nil {
		return fmt.Errorf("session store not initialized")
	}
	sess, err := s.store.Load(name)
	if err != nil {
		return fmt.Errorf("load session %s: %w", name, err)
	}
	s.replaceSession(sess)
	s.sessionName = name
	s.compactCount = sess.Metadata.CompactionCount
	s.savedMsgCount = len(sess.GetMessages())
	return nil
}

// SaveSession saves the current session
func (s *SessionModel) SaveSession() error {
	if s.store == nil {
		return fmt.Errorf("session store not initialized")
	}
	return s.store.Save(s.sessionName, s.session)
}

// CurrentSession returns the current session
func (s *SessionModel) CurrentSession() *session.Session {
	return s.session
}

// SetCurrentSession sets the current session
func (s *SessionModel) SetCurrentSession(sess *session.Session) {
	s.replaceSession(sess)
}

// replaceSession is the single session replacement boundary. It invalidates
// request-local estimates and usage so an old stream cannot be attributed to a
// newly selected session.
func (s *SessionModel) replaceSession(sess *session.Session) {
	s.session = sess
	s.sessionEpoch++
	s.requestSnapshot = contextRequestSnapshot{}
	s.requestUsage = ai.RequestUsageAccumulator{}
	s.requestEpoch = 0
	s.usageRequestID = ""
	s.inputTokens = 0
	s.outputTokens = 0
	s.requestCount = 0
	s.lastInputTokens = 0
	s.lastOutputTokens = 0
	if sess != nil {
		s.compactCount = sess.Metadata.CompactionCount
	} else {
		s.compactCount = 0
	}
}

// SessionName returns the current session name
func (s *SessionModel) SessionName() string {
	return s.sessionName
}

// SetSessionName sets the current session name
func (s *SessionModel) SetSessionName(name string) {
	s.sessionName = name
}

// WorkDir returns the working directory
func (s *SessionModel) WorkDir() string {
	return s.workDir
}

// HomeDir returns the home directory
func (s *SessionModel) HomeDir() string {
	return s.homeDir
}

// State returns the current interaction state
func (s *SessionModel) State() InteractionState {
	return s.state
}

// SetState sets the interaction state
func (s *SessionModel) SetState(state InteractionState) {
	s.state = state
}

// ModelName returns the current AI model name
func (s *SessionModel) ModelName() string {
	return s.modelName
}

// SetModelName sets the current AI model name
func (s *SessionModel) SetModelName(name string) {
	if s.modelName != name {
		s.modelEpoch++
	}
	s.modelName = name
	s.requestSnapshot = contextRequestSnapshot{}
}

// Config returns the current configuration
func (s *SessionModel) Config() *config.Config {
	return s.config
}

// Store returns the session store
func (s *SessionModel) Store() *session.Store {
	return s.store
}

// UsageTracker returns the usage tracker
func (s *SessionModel) UsageTracker() *ai.UsageTracker {
	return s.usageTracker
}

// AddMessage appends a message to the message list
//
// N13 G-04：委托 messageBuffer.Append，超限时 FIFO 淘汰最旧并触发 onEvict
// 失效对应 RenderCache 条目。
func (s *SessionModel) AddMessage(msg string) {
	s.AddMessageWithSources(msg, nil)
}

// AddMessageWithSources 追加一条显示消息，并登记它的**逐行 source map**。
//
// sources 与 strings.Split(msg, "\n") 等长（nil = 全普通行）。批 C 起，
// 凡渲染产地做过省略（思考预览 / 命令截断 / 工具结果折叠）的消息都走本入口，
// 把"这一行代表哪段原文"随内容一起存下来（见 transcript_source.go）。
func (s *SessionModel) AddMessageWithSources(msg string, sources []lineSource) {
	if s.messages == nil {
		// 防御性：未初始化时即时构造一个无回调的 buffer，避免 nil panic。
		s.messages = NewMessageBuffer(defaultMaxMessages, nil)
	}
	s.messages.AppendEntry(displayEntry{text: msg, sources: sources})
}

// Messages returns the message list
//
// N13 G-04：返回 messageBuffer.All 的副本，调用方可安全使用。
func (s *SessionModel) Messages() []string {
	if s.messages == nil {
		return nil
	}
	return s.messages.All()
}

// MessageEntries 返回显示条目（文本 + 逐行 source map）的副本。
//
// 与 Messages() 同一批数据、同一顺序；渲染侧需要 source map 时读它。
func (s *SessionModel) MessageEntries() []displayEntry {
	if s.messages == nil {
		return nil
	}
	return s.messages.AllEntries()
}

// SetMessages replaces the message list
//
// N13 G-04：重建 messageBuffer（保留原 onEvict 回调），批量 SetAll。
// 超限时 FIFO 淘汰并触发 onEvict。
// 回核 D-优化：用 SetAll 替代逐条 Append，单次加锁批量插入更高效。
func (s *SessionModel) SetMessages(msgs []string) {
	s.SetMessageEntries(nil, msgs)
}

// SetMessagesWithSources 重建显示缓冲：entries[i] 的文本与 sources[i] 一一对应。
//
// 用于 resume / fork 重建：整批显示条目（含省略行的 source map）一次写入，
// 与实时路径共用同一入口，避免"实时登记、恢复不登记"的半新半旧。
func (s *SessionModel) SetMessagesWithSources(msgs []string, sources [][]lineSource) {
	s.SetMessageEntries(sources, msgs)
}

// SetMessageEntries 是显示缓冲重建的**唯一实现**：msgs 为文本，sources 为对应的
// 逐行来源（可为 nil，表示全普通行）。
func (s *SessionModel) SetMessageEntries(sources [][]lineSource, msgs []string) {
	// 保留原 onEvict 回调（RenderCache 失效）。
	var onEvict func(string)
	if s.messages != nil {
		s.messages.mu.Lock()
		onEvict = s.messages.onEvict
		s.messages.mu.Unlock()
	}
	entries := make([]displayEntry, len(msgs))
	for i, m := range msgs {
		entries[i] = displayEntry{text: m}
		if i < len(sources) {
			entries[i].sources = sources[i]
		}
	}
	s.messages = NewMessageBuffer(defaultMaxMessages, onEvict)
	s.messages.SetAllEntries(entries)
}

// FirstUserInput returns the first user input in the session
func (s *SessionModel) FirstUserInput() string {
	return s.firstUserInput
}

// SetFirstUserInput sets the first user input
func (s *SessionModel) SetFirstUserInput(input string) {
	s.firstUserInput = input
}

// SavedMsgCount returns the saved message count
func (s *SessionModel) SavedMsgCount() int {
	return s.savedMsgCount
}

// SetSavedMsgCount sets the saved message count
func (s *SessionModel) SetSavedMsgCount(count int) {
	s.savedMsgCount = count
}

// InputTokens returns the input token count
func (s *SessionModel) InputTokens() int {
	return s.inputTokens
}

// SetInputTokens sets the input token count
func (s *SessionModel) SetInputTokens(tokens int) {
	s.inputTokens = tokens
}

// OutputTokens returns the output token count
func (s *SessionModel) OutputTokens() int {
	return s.outputTokens
}

// SetOutputTokens sets the output token count
func (s *SessionModel) SetOutputTokens(tokens int) {
	s.outputTokens = tokens
}

// RequestCount returns the number of provider requests attributed to the current session
func (s *SessionModel) RequestCount() int {
	return s.requestCount
}

// SetRequestCount sets the provider request count
func (s *SessionModel) SetRequestCount(count int) {
	s.requestCount = count
}

// Quitting returns whether the application is quitting
func (s *SessionModel) Quitting() bool {
	return s.quitting
}

// SetQuitting sets the quitting flag
func (s *SessionModel) SetQuitting(q bool) {
	s.quitting = q
}

// AvailableModels returns the list of available model names
func (s *SessionModel) AvailableModels() []string {
	return s.availableModels
}

// SetAvailableModels sets the list of available model names
func (s *SessionModel) SetAvailableModels(models []string) {
	s.availableModels = models
}

// CurrentText returns the current streaming text
func (s *SessionModel) CurrentText() string {
	return s.currentText
}

// SetCurrentText sets the current streaming text
func (s *SessionModel) SetCurrentText(text string) {
	s.currentText = text
}

// CurrentThinking returns the current thinking content
func (s *SessionModel) CurrentThinking() string {
	return s.currentThinking
}

// SetCurrentThinking sets the current thinking content
func (s *SessionModel) SetCurrentThinking(thinking string) {
	s.currentThinking = thinking
}

// Interrupted returns whether the stream was interrupted
func (s *SessionModel) Interrupted() bool {
	return s.interrupted
}

// SetInterrupted sets the interrupted flag
func (s *SessionModel) SetInterrupted(interrupted bool) {
	s.interrupted = interrupted
}

// ThinkingEnabled 返回 thinking 的**三态**设置：
//   - nil        → 未指定（不下发该字段，由服务端默认决定）
//   - 指向 true  → 显式开启
//   - 指向 false → 显式关闭
//
// 类型与 config.ThinkingEnabled、ai.ThinkingConfig.Enabled 一致 ⇒ 设置到请求零转换。
func (s *SessionModel) ThinkingEnabled() *bool {
	return s.thinkingEnabled
}

// SetThinkingEnabled 设置三态 thinking（nil = 未指定，交回服务端默认）。
func (s *SessionModel) SetThinkingEnabled(enabled *bool) {
	s.thinkingEnabled = enabled
}

// thinkingBudgetFromConfig 解析 thinking 预算：settings 显式配置优先，否则保留原默认 10000。
// （仅在 thinking 为「显式开启」时才会被下发，见 ai.ThinkingConfig.Resolve。）
func thinkingBudgetFromConfig(cfg *config.Config) int {
	if cfg != nil && cfg.ThinkingBudgetTokens != nil && *cfg.ThinkingBudgetTokens > 0 {
		return *cfg.ThinkingBudgetTokens
	}
	return 10000
}

// ThinkingBudgetTokens returns the thinking budget in tokens
func (s *SessionModel) ThinkingBudgetTokens() int {
	return s.thinkingBudgetTokens
}

// SetThinkingBudgetTokens sets the thinking budget in tokens
func (s *SessionModel) SetThinkingBudgetTokens(tokens int) {
	s.thinkingBudgetTokens = tokens
}

// CurrentAgent returns the current agent name
func (s *SessionModel) CurrentAgent() string {
	return s.currentAgent
}

// SetCurrentAgent sets the current agent name
func (s *SessionModel) SetCurrentAgent(agent string) {
	s.currentAgent = agent
}

// ActiveSkills returns the list of active skills
func (s *SessionModel) ActiveSkills() []string {
	return s.activeSkills
}

// SetActiveSkills sets the list of active skills
func (s *SessionModel) SetActiveSkills(skills []string) {
	s.activeSkills = skills
}

// CompactCount returns the compaction count
func (s *SessionModel) CompactCount() int {
	return s.compactCount
}

// SetCompactCount sets the compaction count
func (s *SessionModel) SetCompactCount(count int) {
	s.compactCount = count
	if s.session != nil {
		s.session.SetCompactionCount(count)
	}
}

// AutoCompactCooldown returns whether auto-compact is in cooldown
func (s *SessionModel) AutoCompactCooldown() bool {
	return s.autoCompactCooldown
}

// SetAutoCompactCooldown sets the auto-compact cooldown flag
func (s *SessionModel) SetAutoCompactCooldown(cooldown bool) {
	s.autoCompactCooldown = cooldown
}

// PlanMode returns whether plan mode is active
func (s *SessionModel) PlanMode() bool {
	return s.planMode
}

// SetPlanMode sets the plan mode flag
func (s *SessionModel) SetPlanMode(plan bool) {
	s.planMode = plan
}

// FastModel returns the fast model name
func (s *SessionModel) FastModel() string {
	return s.fastModel
}

// SetFastModel sets the fast model name
func (s *SessionModel) SetFastModel(model string) {
	s.fastModel = model
}

// OutputStyle returns the output style
func (s *SessionModel) OutputStyle() string {
	return s.outputStyle
}

// SetOutputStyle sets the output style
func (s *SessionModel) SetOutputStyle(style string) {
	s.outputStyle = style
}

// ConfigLoader returns the config loader
func (s *SessionModel) ConfigLoader() *config.Loader {
	return s.configLoader
}

// CacheBroken returns whether the cache is broken
func (s *SessionModel) CacheBroken() bool {
	return s.cacheBroken
}

// SetCacheBroken sets the cache broken flag
func (s *SessionModel) SetCacheBroken(broken bool) {
	s.cacheBroken = broken
}

// CacheBreakCount returns the cache break count
func (s *SessionModel) CacheBreakCount() int {
	return s.cacheBreakCount
}

// SetCacheBreakCount sets the cache break count
func (s *SessionModel) SetCacheBreakCount(count int) {
	s.cacheBreakCount = count
}

// PrivacyConfig returns the privacy configuration
func (s *SessionModel) PrivacyConfig() *PrivacyConfig {
	return s.privacyConfig
}

// SetPrivacyConfig sets the privacy configuration
func (s *SessionModel) SetPrivacyConfig(cfg *PrivacyConfig) {
	s.privacyConfig = cfg
}

// Marks returns the session marks
func (s *SessionModel) Marks() []sessionMark {
	return s.marks
}

// SetMarks sets the session marks
func (s *SessionModel) SetMarks(marks []sessionMark) {
	s.marks = marks
}

// InputHistory returns the input history
func (s *SessionModel) InputHistory() []string {
	return s.inputHistory
}

// SetInputHistory sets the input history
func (s *SessionModel) SetInputHistory(history []string) {
	s.inputHistory = history
}

// HistoryIdx returns the history navigation index
func (s *SessionModel) HistoryIdx() int {
	return s.historyIdx
}

// SetHistoryIdx sets the history navigation index
func (s *SessionModel) SetHistoryIdx(idx int) {
	s.historyIdx = idx
}

// HistoryDraft returns the history draft
func (s *SessionModel) HistoryDraft() string {
	return s.historyDraft
}

// SetHistoryDraft sets the history draft
func (s *SessionModel) SetHistoryDraft(draft string) {
	s.historyDraft = draft
}

// ★ 2026-10-03 删除 `ThinkingHistory` / `SetThinkingHistory` / `AddThinkingRecord`
// 与它们背后的 `SessionModel.thinkingHistory` 字段（见同名回执）。
//
// 删除理由：这是「思考记录」这个语义的第二份存储 —— 一份内存 ring buffer，
// 在生产**零写入点**（唯一写入者 `saveThinkingRecord` 只有测试调用），
// 却让 `/thinkback` 看起来已经实现了「历史记录」能力。实测真机上永远读到空。
// 现在唯一真相源是会话存档（assistant 消息的 thinking 块），退出后 resume 依然可回顾。
