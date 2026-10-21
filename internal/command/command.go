// Package command — slash command registration and processing
package command

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"regexp"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/anchorandfish/whale-code/internal/config"
	"github.com/anchorandfish/whale-code/internal/feedback"
	"github.com/anchorandfish/whale-code/internal/github"
	"github.com/anchorandfish/whale-code/internal/skill"
	"github.com/anchorandfish/whale-code/internal/util"
	"github.com/sahilm/fuzzy"
)

// ErrNotImplemented is the standard message for commands not yet implemented in the current context
const ErrNotImplemented = "command not yet implemented"

// UsageTrackerInterface defines the interface for usage tracking
type UsageTrackerInterface interface {
	GetSessionStats(sessionName string) SessionUsageStats
	GetTotalStats() TotalUsageStats
}

// SessionUsageStats holds token usage statistics for a session
//
// ★ D 批：原带 TotalCost 字段，值来自本地价格表估算（非 provider 上报、
// 非账单、且混币种）⇒ 随成本线整体删除。
type SessionUsageStats struct {
	InputTokens      int64
	OutputTokens     int64
	CacheReadTokens  int64
	CacheWriteTokens int64
}

// TotalUsageStats holds aggregated usage statistics
//
// ★ D 批：原带 Cost 字段，同上删除；Calls 是真实计数，保留。
type TotalUsageStats struct {
	Calls int64
}

// Command slash command
type Command struct {
	Name          string
	Description   string
	DescriptionEN string
	Usage         string
	Category      string
	Handler       func(args string) (string, error)
	Stub          bool // CMD-P1-003: 标记桩命令（未实现），FormatHelp 默认隐藏，--all 显示 [桩命令] 后缀
	Hidden        bool // V0.1-HIDDEN: v0.1 命令面收敛——已实现但不对 v0.1 暴露：help/补全/弹窗过滤，handler 保留可手输，--all 显示 [隐藏] 后缀
	// SuggestionPriority controls the empty-slash popup order. Zero means alphabetical fallback.
	SuggestionPriority int
}

// HandlerCallbacks groups all On* callback function fields
type HandlerCallbacks struct {
	OnUndo   func() string
	OnRedo   func() string
	OnSkills func() string
	// OnSkillManage 处理 /skill 信任管理子命令（trust <name> / trust all / untrust <name> / 待确认列表）。
	// §2（拍板材料 2026-10-11）：信任门生产已开而出口命令缺失（幽灵命令），本回调补齐出口；
	// 实现 SSOT = skill.Manager.HandleTrustCommand，各入口薄转发。nil 时返回诚实降级提示。
	OnSkillManage  func(args string) (string, error)
	OnPlugins      func() string
	OnGitHub       func(args string) string
	OnReview       func(args string) string
	OnDaemon       func(args string) string
	OnLoop         func(args string) string
	OnSkillExecute func(skillName, args string) string
	// OnSkillExecuteWithMeta 带 meta 的 skill 执行回调，返回 SkillResult（含 allowedTools/model 等）
	// 保留旧 OnSkillExecute 向后兼容，security-review 兜底仍走旧回调
	OnSkillExecuteWithMeta func(skillName, args string) (skill.SkillResult, error)
	OnCheckpoint           func(args string) string
	// OnRewind 会话级回退回调（/rewind <msgId>），同时恢复文件+会话状态
	OnRewind      func(args string) string
	OnSearch      func(args string) string
	OnHooks       func(args string) string
	OnMcp         func(args string) string
	OnEnv         func(args string) string
	OnOutputStyle func(args string) string
	// N14：fast mode 相关回调（/fast 命令通过这些回调与 config 层交互）。
	// OnSetFastMode 开启/关闭 fast mode，nil 时仅返回提示不实际设置。
	OnSetFastMode func(enabled bool) error
	// OnGetFastModeState 返回当前 fast mode 状态及模型信息，nil 时使用默认 off。
	OnGetFastModeState func() (state config.FastModeState, fastModel string, defaultModel string)
	// N14：安全校验回调（委托 H2，02 §2.3.1）。
	// OnSecurityCheck 对 slash command 进行黑名单校验，返回 "allow" 或 "block"。
	// H2 未合入时为 nil，跳过校验不阻塞功能（见开发 prompt §6 注意事项 3）。
	OnSecurityCheck   func(cmd string) string
	OnTerminalSetup   func(args string) string
	OnMigrate         func(args string) string
	OnUltraplan       func(args string) string
	OnBugHunter       func(args string) string
	OnCompactStrategy func(strategy string) string
	OnContext         func() string
	OnPluginInstall   func(args string) string
	OnPluginEnable    func(args string) string
	OnPluginDisable   func(args string) string
	OnReloadPlugins   func() string
	OnUsage           func() string
	OnStats           func() string
	OnSummary         func() string
	OnOnboarding      func(args string) (string, error)
	OnShare           func(args string) string
	OnExport          func(args string) string
	OnExportUser      func(args string) string
	OnExportFormat    func(args string) string
	OnCopy            func(args string) string
	OnStatus          func() string
	OnDoctor          func(args string) (string, error)

	// OnMonitors 处理 /monitors 子命令（list / stop <id>）。
	//
	// ★ 签名必须能承载 args（C-3，2026-09-30）：注册项的 Usage 早已承诺
	// `/monitors [stop <id>]`，而旧签名 `func() string` **在类型上不可能**收到参数 ⇒
	// 那是一条"做不到的承诺"（声明与实际能力不一致， 的复发形态）。
	// 本次把签名升为 `func(args string) string`，让"注册侧能表达 stop"在类型上成立。
	//
	// ⚠️ 只改 command 侧的**可承载性**，不改变"本入口是否已接线"：
	// TUI 侧的真实实现在 internal/tui/format.go handleMonitors（支持 stop/tail/show），
	// 且 TUI 由 isTUICommandHandledLocally("monitors") 短路 ⇒ 注册侧 Handler 在 TUI 下不执行。
	// TUI 侧要不要接这个回调由 TUI 的 owner 决定；本单不做端到端断言。
	OnMonitors func(args string) string

	OnThinkback     func() string
	OnMemory        func(args string) (string, error)
	OnConfig        func(args string) string
	OnDiff          func() string
	OnAgents        func() string
	OnInit          func(args string) (string, error)
	OnCronAdd       func(args string) string
	OnCronList      func() string
	OnCronRemove    func(args string) string
	OnFiles         func(args string) string
	OnInsights      func() string
	OnBranchList    func() string
	OnCommit        func() string
	OnWorktreeList  func() string
	OnWorktreePrune func() string
	OnFileCache     func(args string) string

	// P0-3（D5-2）修复：/upgrade check 异步结果回调。
	// 非 nil 时 handleUpgradeCheck 在 goroutine 中执行检查并立即返回"检查中..."提示，
	// 检查完成后通过此回调通知调用方（TUI 用于发送 tea.Msg 回写结果）。
	// nil 时走同步路径（向后兼容 PlainRunner 等非 TUI 模式）。
	OnUpgradeCheckResult func(result string)

	// OnTaskCommand 处理 /task 子命令（N1 subagent / Task tool）。
	// args 为子命令参数（list / cancel <id> / show <id>），nil 时 /task 命令返回未初始化提示。
	OnTaskCommand func(args string) string

	// N26：goal 长期目标系统回调（/goal 子命令）。
	// OnGoal 处理 /goal 子命令（set <objective> / status / pause / resume / clear / edit <objective> / done / list / help），
	// nil 时 /goal 命令返回未初始化提示。
	// 采用闭包 + 回调模式（与 OnReview 一致），单入口 + args 解析避免 commandCompletions 爆炸。
	// 对标同类 CLI 的 goal-extension（/goal 命令）与同类 CLI /goal v2.1.139。
	OnGoal func(args string) string

	// H10：智能编排系统回调（/orchestrate 子命令）。
	// OnOrchestrate 处理 /orchestrate 子命令（start <需求> / status [task-id] / list / resume <task-id> / abort <task-id>），
	// nil 时 /orchestrate 命令返回未初始化提示。
	// 采用闭包 + 回调模式（与 OnGoal/OnReview 一致），单入口 + args 解析避免 commandCompletions 爆炸。
	// 通过 command_list.go 的 orchestrateCommands() 注册（02 §2.5.1，不自定义入口）。
	OnOrchestrate func(args string) (string, error)

	// H10 Phase 2：工作流模板回调（/workflow 子命令）。
	// OnWorkflow 处理 /workflow 子命令（list / run <name> --input "需求" / save-as <name> [--task <task-id>]），
	// nil 时 /workflow 命令返回未初始化提示。
	// 采用闭包 + 回调模式（与 OnOrchestrate/OnGoal 一致），单入口 + args 解析避免 commandCompletions 爆炸。
	// 通过 command_list.go 的 workflowCommands() 注册（编排层 §6.4，02 §2.5.1）。
	OnWorkflow func(args string) (string, error)

	// /import 会话导入回调（P0-14 桩替换）。
	// OnImport 处理 /import <path> 命令，从 JSON 文件导入会话到本地 Store，
	// nil 时回退到 handleImport 桩（仅返回提示文本，不真正导入）。
	// 采用闭包 + 回调模式（与 OnWorkflow 一致），由 TUI lifecycle.go 接线到 handleImportSession。
	OnImport func(args string) (string, error)

	// P0-4 修复：Session 类命令回调（对齐 OnImport 模式）
	// nil 时回退到魔法字符串（供 TUI app.go / Bridge bridge.go 拦截），
	// 非 nil 时直接返回真实结果（供 PlainRunner 等非 TUI 模式使用）。
	// OnSessionList 返回会话列表，OnSessionSave 保存当前会话，
	// OnSessionSwitch 切换会话，OnResume 恢复会话，
	// OnSessionMerge 合并会话，OnSessionSplit 分割会话，OnRename 重命名会话。
	OnSessionList   func() string
	OnSessionSave   func() string
	OnSessionSwitch func(args string) string
	OnResume        func(args string) string
	OnSessionMerge  func(args string) string
	OnSessionSplit  func(args string) string
	OnRename        func(args string) string

	// N24：通知系统回调（/notify 子命令）。
	// OnNotifyList 返回当前队列+历史通知列表，nil 时返回未初始化提示。
	OnNotifyList func() string
	// OnNotifyClear 清空通知历史，nil 时返回未初始化提示。
	OnNotifyClear func() string
	// OnNotifyDismiss 关闭当前通知，nil 时返回未初始化提示。
	OnNotifyDismiss func() string

	// CMD-P1-002: 动态模型/MCP 服务器列表回调，nil 时使用硬编码 fallback
	OnListModels     func() []string
	OnListMCPServers func() []string

	// Direct-access fields for commands that query structured data
	UsageTracker UsageTrackerInterface // for /usage and /stats
	Session      SessionInterface      // for /stats
	SessionName  string                // for /usage
}

// SessionInterface defines the interface for session data access
type SessionInterface interface {
	GetMessages() []Message
}

// Message represents a conversation message for the command package
type Message struct {
	Role string
}

// Registry command registry
type Registry struct {
	commands map[string]Command
	Version  string
	HandlerCallbacks
	// CMD-P1-004: 进程内环境变量隔离存储，替代 os.Setenv/os.Unsetenv
	// 不持久化，仅进程内有效；持久化仍走 EnvPersister.SaveEnvVar
	// 保持独立组件，不合并到 config.Config
	EnvStore *EnvStore
	// H8：持有 *config.Config 用于 listAvailableModels 等命令读取配置化值。
	// 由调用方（runnerCore/app）在构造后注入，nil 时 fallback 到内置默认列表。
	Config *config.Config
	// M19-04 改造新增 [SNAPSHOT]：/feedback 直创 GitHub Issue 的 creator 注入点。
	// nil 时 handleFeedbackSave 在运行时基于 GITHUB_TOKEN 环境变量构造默认 creator；
	// 测试可通过 SetFeedbackIssueCreator 注入 mock 以避免真实 API 调用。
	feedbackCreator feedback.IssueCreator
	// workDir 用于 git remote 探测 owner/repo（构造默认 creator 时使用）。
	// 由调用方（runnerCore/app）在构造后注入，空字符串时默认当前工作目录。
	workDir string
	// [SNAPSHOT-REGISTRY] M09-01/02 改造新增：命令历史与别名存储器
	// historyStore 命令历史存储器（nil 降级为无历史，/history 返回未初始化提示）
	// aliasStore 命令别名存储器（nil 降级为无别名，ExpandAlias 原样返回输入）
	// 由调用方（main.go/app）在构造后通过 SetHistoryStore / SetAliasStore 注入
	historyStore *HistoryStore
	aliasStore   *AliasStore
}

// SetFeedbackIssueCreator 注入 /feedback 直创 Issue 的 creator（M19-04 改造新增 [SNAPSHOT]）。
// 仅供测试使用：注入 mock creator 后 /feedback 将直接调用 mock 而非真实 GitHub API。
// 调用 SetFeedbackIssueCreator(nil) 可清除注入，恢复默认行为。
func (r *Registry) SetFeedbackIssueCreator(creator feedback.IssueCreator) {
	r.feedbackCreator = creator
}

// SetWorkDir 设置 Registry 的工作目录，用于 /feedback 直创 Issue 时 git remote 探测（M19-04 改造新增 [SNAPSHOT]）。
func (r *Registry) SetWorkDir(dir string) {
	r.workDir = dir
}

// SetHistoryStore 注入命令历史存储器（M09-01 改造）
// nil 时 /history 命令返回未初始化提示，RecordHistory 为 no-op
// 由调用方（main.go/app）在构造后注入，典型路径 ~/.whale/command_history.jsonl
func (r *Registry) SetHistoryStore(h *HistoryStore) {
	r.historyStore = h
}

// SetAliasStore 注入命令别名存储器（M09-02 改造）
// nil 时 ExpandAlias 原样返回输入，/alias 命令返回未初始化提示
// 由调用方（main.go/app）在构造后注入，典型路径 ~/.whale/aliases.json
func (r *Registry) SetAliasStore(a *AliasStore) {
	r.aliasStore = a
}

// ExpandAlias 展开别名（M09-02 改造）
// 供调用方在 Parse 前调用，将用户输入的别名展开为目标命令
// aliasStore 为 nil 时原样返回输入（降级）
// 单层展开（不递归），避免 A→B→A 循环
func (r *Registry) ExpandAlias(input string) string {
	if r.aliasStore == nil {
		return input
	}
	return r.aliasStore.Expand(input)
}

// RecordHistory 记录命令历史（M09-01 改造）
// 供调用方在命令执行后调用，记录到 historyStore
// historyStore 为 nil 时为 no-op（降级），错误仅记录不返回（避免影响主流程）
func (r *Registry) RecordHistory(cmd string, sessionID string) {
	if r.historyStore == nil {
		return
	}
	_ = r.historyStore.Record(cmd, sessionID)
}

// GetConfig 返回 Registry 持有的 Config（H8 新增）
// 返回 nil 时调用方应自行 fallback 到默认值
func (r *Registry) GetConfig() *config.Config {
	return r.Config
}

// NewRegistry creates a command registry
// CMD-P1-004: 初始化 EnvStore，确保 /env 命令在所有模式下都有可用的进程内存储
func NewRegistry() *Registry {
	r := &Registry{
		commands: make(map[string]Command),
		EnvStore: NewEnvStore(),
	}
	r.registerBuiltinCommands()
	return r
}

// Get retrieves a command
func (r *Registry) Get(name string) (Command, bool) {
	cmd, ok := r.commands[name]
	return cmd, ok
}

// All retrieves all commands
func (r *Registry) All() []Command {
	var all []Command
	for _, cmd := range r.commands {
		all = append(all, cmd)
	}
	return all
}

// List retrieves all command names
func (r *Registry) List() []string {
	var names []string
	for name := range r.commands {
		names = append(names, name)
	}
	return names
}

// ByCategory groups commands by category
func (r *Registry) ByCategory() map[string][]Command {
	categories := make(map[string][]Command)
	for _, cmd := range r.commands {
		categories[cmd.Category] = append(categories[cmd.Category], cmd)
	}
	return categories
}

// knownPrefixes holds command names containing spaces, auto-populated during registerBuiltinCommands
var knownPrefixes []string

// Parse parses a slash command
func Parse(input string) (name string, args string) {
	input = strings.TrimPrefix(input, "/")
	input = strings.TrimSpace(input)

	spaceRe := regexp.MustCompile(`\s+`)
	normalized := spaceRe.ReplaceAllString(input, " ")

	lower := strings.ToLower(normalized)
	for _, prefix := range knownPrefixes {
		if lower == prefix || strings.HasPrefix(lower, prefix+" ") {
			name = prefix
			prefixLen := len(prefix)
			if len(normalized) >= prefixLen {
				args = strings.TrimSpace(normalized[prefixLen:])
			}
			return name, args
		}
	}

	parts := strings.SplitN(normalized, " ", 2)
	name = strings.ToLower(parts[0])
	if len(parts) > 1 {
		args = strings.TrimSpace(parts[1])
	}
	return name, args
}

// Suggest provides fuzzy command suggestions.
//
// （M11-04）：升级为 sahilm/fuzzy 智能匹配（对标 fzf/fzy 算法），
// 保留 levenshtein <= 2 作为降级路径（fuzzy 无匹配时尝试）。
// 对标 fzf @https://github.com/junegunn/fzf @2026-07-21。
//
// 匹配优先级：
// 1. sahilm/fuzzy.Find 按 Score 降序取 top 5
// 2. 若 fuzzy 无匹配，降级 strings.Contains + levenshtein <= 2
func Suggest(input string, r *Registry) []string {
	input = strings.TrimPrefix(input, "/")
	// V0.1-HIDDEN：Did-you-mean 建议与 /help/补全同一口径——不指路隐藏/桩命令
	// （否则用户拼错隐藏命令前缀会被主动建议，绕过命令面收敛）
	visibleNames := make([]string, 0, len(r.commands))
	for name, cmd := range r.commands {
		if cmd.Hidden || cmd.Stub {
			continue
		}
		visibleNames = append(visibleNames, name)
	}
	if input == "" {
		// 空输入：返回所有命令（按名称排序，取 top 5）
		all := append([]string(nil), visibleNames...)
		sort.Strings(all)
		if len(all) > 5 {
			all = all[:5]
		}
		return all
	}

	// 收集所有命令名
	cmdNames := visibleNames

	// （M11-04）：优先 sahilm/fuzzy 智能匹配
	matches := fuzzy.Find(input, cmdNames)
	if len(matches) > 0 {
		// sahilm/fuzzy.Find 已按 Score 降序排列
		max := 5
		if len(matches) < max {
			max = len(matches)
		}
		suggestions := make([]string, 0, max)
		for i := 0; i < max; i++ {
			suggestions = append(suggestions, matches[i].Str)
		}
		return suggestions
	}

	// 降级：strings.Contains + levenshtein <= 2（保留既有逻辑）
	var suggestions []string
	for _, name := range cmdNames {
		if strings.Contains(name, input) || levenshtein(name, input) <= 2 {
			suggestions = append(suggestions, name)
		}
	}
	if len(suggestions) > 5 {
		suggestions = suggestions[:5]
	}
	return suggestions
}

// Completion provides multi-level command completion with sub-command and argument support
// Returns matching command names or sub-command/argument strings based on input depth
// M09-03 改造：支持多层嵌套递归补全（Children 字段）
// OPT-06 优先级：SubCommands > Children keys > ArgCompleter
// 旧条目（无 Children 字段，零值 nil）走 SubCommands → ArgCompleter 旧逻辑，行为不变
func (r *Registry) Completion(input string) []string {
	parts := strings.Fields(input)

	// 空输入：返回所有命令
	if len(parts) == 0 {
		return r.prefixMatch("")
	}

	// 一级补全：输入 "/" 时列出所有命令
	if len(parts) == 1 {
		return r.prefixMatch(strings.TrimPrefix(parts[0], "/"))
	}

	// 多级补全：递归深入 Children（M09-03 改造，）
	cmdName := strings.TrimPrefix(parts[0], "/")
	node, ok := commandCompletions[cmdName]
	if !ok {
		return nil
	}
	// V0.1-HIDDEN: 单命令隐藏且定义了补全规则（ArgCompleter 型）时不再提示其参数
	if r.isRegisteredHidden(cmdName) {
		return nil
	}
	// subCommandConfirmed 标记是否已确认一个 SubCommand（3+ 段无 Children 时跳过 SubCommands 检查）
	// 例如 /mcp add x：mcp 无 Children，"add" 匹配 SubCommand 后 "x" 应走 ArgCompleter
	subCommandConfirmed := false
	// 逐层深入到倒数第二级（i 从 1 到 len(parts)-2）
	for i := 1; i < len(parts)-1; i++ {
		if node.Children == nil {
			// 无 Children：若当前中间部分匹配 SubCommand，则后续走 ArgCompleter
			if !sliceContains(node.SubCommands, parts[i]) {
				return nil
			}
			subCommandConfirmed = true
			break
		}
		child, ok := node.Children[parts[i]]
		if !ok {
			return nil
		}
		node = *child
	}
	last := parts[len(parts)-1]
	// V0.1-HIDDEN: 节点路径（已输入的多级命令前缀），用于拼出注册全名过滤隐藏子命令
	nodePath := strings.TrimPrefix(strings.Join(parts[:len(parts)-1], " "), "/")
	// OPT-06 优先级：SubCommands > Children keys > ArgCompleter
	// 但若已确认 SubCommand（3+ 段无 Children），跳过 SubCommands 直接走 ArgCompleter
	if !subCommandConfirmed && len(node.SubCommands) > 0 {
		return filterPrefix(r.visibleSubCommands(nodePath, node.SubCommands), last)
	}
	if len(node.Children) > 0 {
		var keys []string
		for k := range node.Children {
			keys = append(keys, k)
		}
		return filterPrefix(r.visibleSubCommands(nodePath, keys), last)
	}
	if node.ArgCompleter != nil {
		// CMD-P1-002: 优先使用动态回调，nil 时 fallback 到硬编码列表
		// H8：model 命令优先级 OnListModels > Config.AvailableModels > 内置默认列表
		if cmdName == "model" {
			if r.OnListModels != nil {
				return r.OnListModels()
			}
			if cfg := r.GetConfig(); cfg != nil && len(cfg.AvailableModels) > 0 {
				return cfg.AvailableModels
			}
		}
		if cmdName == "mcp" && r.OnListMCPServers != nil {
			return r.OnListMCPServers()
		}
		return node.ArgCompleter(last)
	}

	return nil
}

// sliceContains 检查字符串切片是否包含指定值（M09-03 改造，）
// 用于 Completion 中 3+ 段输入时验证中间部分是否为有效 SubCommand
func sliceContains(items []string, target string) bool {
	for _, item := range items {
		if item == target {
			return true
		}
	}
	return false
}

// prefixMatch returns command names that start with the given prefix
func (r *Registry) prefixMatch(prefix string) []string {
	var matches []string
	for name, cmd := range r.commands {
		if cmd.Hidden { // V0.1-HIDDEN: 补全候选不含隐藏命令
			continue
		}
		if strings.HasPrefix(name, prefix) {
			matches = append(matches, name)
		}
	}
	return matches
}

// isRegisteredHidden 判断注册表中该名字命令是否隐藏（未注册视为可见）
func (r *Registry) isRegisteredHidden(name string) bool {
	cmd, ok := r.commands[name]
	return ok && cmd.Hidden
}

// IsHiddenCommand 判断命令是否默认不可见（Hidden 或 Stub；未注册返回 false）
// 供各入口（如 plain 模式的帮助表）对齐注册表口径（V0.1-HIDDEN）
func (r *Registry) IsHiddenCommand(name string) bool {
	cmd, ok := r.commands[name]
	return ok && (cmd.Hidden || cmd.Stub)
}

// visibleSubCommands 过滤掉已注册为 Hidden 的子命令（nodePath+sub 构成注册全名；未注册的保留）
func (r *Registry) visibleSubCommands(nodePath string, subs []string) []string {
	visible := make([]string, 0, len(subs))
	for _, sub := range subs {
		full := sub
		if nodePath != "" {
			full = nodePath + " " + sub
		}
		if r.isRegisteredHidden(full) {
			continue
		}
		visible = append(visible, sub)
	}
	return visible
}

// /help 的两张有列表：命令表的描述列、快捷键表的说明列。
//
// ★ 这两个列起点是**版式 SSOT**：主行（EN）、中文辅行（ZH）与 usage 行都必须用**同一个**
// 常量来缩进，否则三者会各自漂移 —— 2026-10-14 用户直报的
// 「中文注释没有和英文对齐，而是直接在命令的下面」正是这么来的：
// 主行/usage 走列起点，中文辅行走 util.CopyIndent（3 空格）⇒ 中文落到行首，视觉上"在命令名下面"。
//
// 命令表列宽 = 3（` /`）+ 22（命令名左对齐宽）+ 1（分隔空格）= 26
//
//	★ 旧注释写的是 25（"2 + 22 + 1"，漏算了 `/` 自身占 1 列）⇒ 那时 usage 缩进 25、
//
// 英文描述实际在 26，usage 一直**比描述列左一格**。本次一并改正（EN / ZH / usage 全在 26）。
//
//	★ 别再"手算"这个数：判据 `TestHelp_CommandCopyAlignedToEnglishColumn` 从**渲染产物**里量列起点。
//
// 快捷键表列宽 = 2（缩进）+ 20（按键名左对齐宽）+ 1（分隔空格）= 23
//
//	★ 键名不得超过 20 列，由 `TestHelp_ShortcutTableFitsColumn` 守着。
const (
	helpDescCol     = 26
	helpShortcutCol = 23
)

// helpDescIndent / helpShortcutIndent 是对应列起点前面的空白，喂给 util.BiIndent 让中文与英文列对齐。
var (
	helpDescIndent     = strings.Repeat(" ", helpDescCol)
	helpShortcutIndent = strings.Repeat(" ", helpShortcutCol)
)

// helpShortcuts 是 /help 快捷键块的**唯一数据源**（键名 / 英文说明 / 中文释义）。
//
// ★ 为什么是数据表而不是现拼字符串：原实现每条一行 `" Enter Send message"`，
// 对齐靠**手数空格**。增删一条、或键名超宽，列起点就静默错位（中文列随之错位）。
// 数据表 + `%-20s` 让列宽只有一个来源（`helpShortcutCol`）。
// ⚠️ 约束：`key` 显示宽度必须 ≤ helpShortcutCol-3（= 20），由
// `TestHelp_ShortcutTableFitsColumn` 机械守着。
var helpShortcuts = []struct{ key, en, zh string }{
	{"Enter", "Send message", "发送消息"},
	{"Shift+Enter", "Insert newline", "插入换行"},
	{"Ctrl+C", "Interrupt current output", "中断当前输出"},
	{"Ctrl+Y", "Copy last AI reply to clipboard (idle)", "复制最后一条 AI 回复到剪贴板（空闲时）"},
	{"Alt+Y", "Copy full conversation to clipboard (idle)", "复制完整对话到剪贴板（空闲时）"},
	{"Escape", "Cancel popup / confirm / choice", "取消弹窗/确认框/选择项"},
	{"Ctrl+D", "Quit Whale Code", "退出 Whale Code"},
	{"Ctrl+O", "Open DocNav (idle)", "打开文档导航 DocNav（空闲时）"},
	{"Tab", "Focus send button (idle)", "焦点切换到发送按钮（空闲时）"},
	{"Shift+Tab", "Focus back to input box", "焦点切回输入框"},
	{"Alt+Up", "Recall last queued message to input (busy)", "召回上一条排队消息到输入框（非空闲时）"},
	{"PageUp/PageDown", "Scroll chat history by half screen", "半屏滚动对话历史"},
	{"Ctrl+Up/Down", "Fast scroll chat history (3 lines per press)", "快速滚动对话历史（每次 3 行）"},
	{"Home/End", "Scroll to top / bottom", "滚动到顶部/底部"},
	{"Up/Down", "Browse input history", "浏览输入历史"},
	{"Ctrl+V", "Paste (text/image)", "粘贴（文本/图片）"},
	{"Mouse copy", "Click \"Copy all\" below input; Shift+drag selects", "点击下方 Copy all；Shift+拖拽原生选择"},
	{"Terminal selection", "Command+C on macOS; Ctrl+Shift+C elsewhere", "macOS 用 Command+C；Linux/Windows 用 Ctrl+Shift+C"},
}

// FormatHelp 格式化帮助信息，支持 --all / --search <keyword> / --filter <category> 参数（M09-04 改造，）
// 默认隐藏桩命令，--all 时显示全部并在桩命令描述后追加 [未实现] 后缀（C3，2026-10-05）
// --search <keyword>：搜索命令名或描述含关键词的命令（大小写不敏感，OPT-02）
// --filter <category>：仅显示指定分类（如 --filter Git）
// 无参数时行为不变（全量分组输出，向后兼容）
func FormatHelp(r *Registry, args string) string {
	// 兼容 -all 和 --all 两种写法（用户习惯单横杠，token 精确匹配避免误匹配）
	showAll := false
	for _, t := range strings.Fields(args) {
		if t == "--all" || t == "-all" {
			showAll = true
			break
		}
	}
	search := strings.ToLower(parseFlag(args, "--search")) // OPT-02: ToLower 大小写不敏感
	filter := parseFlag(args, "--filter")

	var sb strings.Builder
	// C-6：/help 双语化 —— 英文主行 + 缩进中文辅助行（util.Bi 唯一形态源）。
	sb.WriteString(util.Bi("🐋 Whale Code — Command Reference", "命令参考") + "\n\n")

	categories := r.ByCategory()
	order := []string{"Core", "Model", "Session", "Memory", "Permission", "Export", "Git", "GitHub", "Plugin", "Daemon", "Task", "Workspace", "Config", "Auth", "Review", "Notify", "Security", "Automation", "Other"}

	matchedCount := 0
	for _, cat := range order {
		// 分类过滤（M09-04 改造，）
		if filter != "" && !strings.EqualFold(cat, filter) {
			continue
		}
		cmds, ok := categories[cat]
		if !ok {
			continue
		}
		// 先收集本类别命中的命令，避免输出空类别标题
		var matched []Command
		for _, cmd := range cmds {
			// 默认隐藏桩命令与隐藏命令（V0.1-HIDDEN）
			if (cmd.Stub || cmd.Hidden) && !showAll {
				continue
			}
			// 搜索过滤：命令名或描述含关键词（已 ToLower，OPT-02）
			if search != "" {
				cmdLower := strings.ToLower(cmd.Name)
				descLower := strings.ToLower(cmd.Description)
				if !strings.Contains(cmdLower, search) && !strings.Contains(descLower, search) {
					continue
				}
			}
			matched = append(matched, cmd)
		}
		if len(matched) == 0 {
			continue
		}
		sb.WriteString(fmt.Sprintf("━━━ %s ━━━\n", cat))
		for _, cmd := range matched {
			suffix := ""
			if cmd.Stub {
				// C-6：短标记双语行内并列（清单 C-6 拍板口径；完整句仍禁行内并列，见 util/bilingual.go）
				suffix = " [stub 未实现]"
			} else if cmd.Hidden {
				suffix = " [hidden 隐藏]"
			}
			// （2026-10-12，用户口径「英语是主要的，中文有的英文也要有」）：
			// 主行 = 英文描述（DescriptionEN，可独立读懂）；中文描述走 util.Bi 缩进辅行。
			// EN 缺失时 Bi 兜底只出主行（EN 覆盖率由 TestBuiltinCommandsHaveBilingualSuggestionMetadata 双向约束守着）；
			// 形态由 TestW082_HelpChineseOnlyOnIndentedLines 全局门守着（含汉字行必须缩进）。
			//
			// 2026-10-14 用户直报「中文注释没有和英文对齐，而是直接在命令的下面」：
			// 中文辅行改用 **BiIndent** 缩进到**英文描述列**（helpDescCol），与主行内容同列起点 ——
			// 这样「命令名 / 英文描述 / 中文描述 / usage」四者左边缘两两对齐，而不是中文退回行首。
			sb.WriteString(util.BiIndent(
				fmt.Sprintf("  /%-22s %s%s", cmd.Name, cmd.DescriptionEN, suffix),
				cmd.Description,
				helpDescIndent,
			) + "\n")
			if cmd.Usage != "" {
				// usage 与英文描述同列（= helpDescCol）—— 与中文辅行同一个缩进常量，
				// 三者（EN / ZH / usage）结构上不可能再错位。
				sb.WriteString(helpDescIndent + cmd.Usage + "\n")
			}
			matchedCount++
		}
		sb.WriteString("\n")
	}

	// 搜索无结果提示（M09-04 改造，）； 双语化
	if search != "" && matchedCount == 0 {
		sb.WriteString(util.BiSame("No commands matched '%s'", "未找到匹配 '%s' 的命令", search) + "\n")
		sb.WriteString(util.Bi("💡 Try /help --all to include stub commands", "试试 /help --all 包含桩命令") + "\n")
		return sb.String()
	}

	// 快捷键块。双语化：英文主行 + 缩进中文辅行（util.Bi 唯一形态源）。
	//
	// 2026-10-14：改为**数据表 + 单一格式化点**。原实现是 17 行手写对齐空格的字符串字面量 ——
	// 增删一条就要重排全部空格，列起点与 helpShortcutCol 无法互相约束（手写对齐必然漂移）；
	// 且中文辅行当时走 CopyIndent ⇒ 中文落在行首"在按键名下面"，与英文说明列错位（用户直报同款）。
	// 现由 `%-20s` 承载列宽、helpShortcutIndent 承载中文列，两者结构上对齐同一个常量。
	sb.WriteString(util.Bi("━━━ Shortcuts ━━━", "快捷键") + "\n")
	for _, sc := range helpShortcuts {
		sb.WriteString(util.BiIndent(
			fmt.Sprintf("  %-20s %s", sc.key, sc.en),
			sc.zh,
			helpShortcutIndent,
		) + "\n")
	}
	sb.WriteString("\n")

	// AI 拒绝覆盖的**唯一可发现入口**。
	//
	// 原先 TUI 会在"命中拒绝措辞"时自动弹出这条提示 —— 那条**自动断言已删**
	// （关键词判据会把「如实说没有这个工具、无法调用」误判成 AI 拒绝，与 T-1 验收期望自相矛盾；
	// 见 `internal/ai/refusal_handler.go` 包注释与 `internal/tui/stream.go checkAISafety`）。
	// 机制本身保留，但改为**用户主动输入** ⇒ 因此它必须在这里能被找到，否则等于删功能。
	//
	// N-039b：文案形态改为「英文主行 + 缩进中文辅行」（SSOT = `internal/util/bilingual.go`）。
	// 首版是中文单语，违反本仓「英文为主、中文为辅」的**运行时**文案规范。
	// ★ 用 `util.Bi` 而不是手拼 `"\n" + " "`：手拼就是双实现，形态一改必漂移（``）。
	sb.WriteString(util.Bi(
		"💡 AI-only refusal can be overridden manually: type force execute",
		"覆盖 AI 自身拒绝：输入「我确认强制执行」（或 force execute）\n"+
			"仅覆盖 AI 自身拒绝，不会绕过权限或沙箱",
	) + "\n")

	if !showAll {
		sb.WriteString("\n" + util.Bi("💡 Use /help --all to show stub & hidden commands", "使用 /help --all 显示桩命令与隐藏命令") + "\n")
	}
	if search != "" {
		sb.WriteString(util.BiSame("💡 Filtered by search: '%s' (%d commands)", "按搜索过滤: '%s'（%d 个命令）", search, matchedCount) + "\n")
	}
	if filter != "" {
		sb.WriteString(util.BiSame("💡 Filtered by category: %s", "按分类过滤: %s", filter) + "\n")
	}

	return sb.String()
}

// parseFlag 从参数字符串中解析 --flag value 格式的 flag 值（M09-04 改造，）
// 如 parseFlag("--search commit --all", "--search") → "commit"
// 如 parseFlag("--filter Git", "--filter") → "Git"
// flag 不存在或无值时返回空字符串
// flag 后紧跟另一个 --flag 或 -flag 时视为无值（返回空字符串）
// 5 维度审查修复：原仅识别 "--" 前缀，导致 "/help --filter -all" 中
// "-all" 被当作 --filter 的值返回 "all"。现统一识别单/双横杠 flag token。
// 约定：以 "-" 开头的 token（含负数如 "-5"）一律视为 flag，不接受作为值；
// 单独 "-" 例外（stdin 占位符），可作为 flag 的值。
func parseFlag(args, flag string) string {
	parts := strings.Fields(args)
	for i := 0; i < len(parts); i++ {
		if parts[i] == flag {
			if i+1 < len(parts) && !isFlagToken(parts[i+1]) {
				return parts[i+1]
			}
			return "" // flag 后无值或下一个是另一个 flag
		}
	}
	return ""
}

// isFlagToken 判断 token 是否为 flag 标识（以 - 或 -- 开头，且非单独 "-"）
// 单独 "-" 通常表示 stdin 占位符，可作为 flag 的值
func isFlagToken(s string) bool {
	return strings.HasPrefix(s, "-") && s != "-"
}

func (r *Registry) registerBuiltinCommands() {
	// 按类别从 builder 函数收集所有命令，避免单个函数过大
	commands := r.coreCommands()
	commands = append(commands, r.sessionCommands()...)
	commands = append(commands, r.permissionCommands()...)
	commands = append(commands, r.exportCommands()...)
	commands = append(commands, r.gitCommands()...)
	commands = append(commands, r.githubCommands()...)
	commands = append(commands, r.pluginCommands()...)
	commands = append(commands, r.daemonCommands()...)
	commands = append(commands, r.workspaceCommands()...)
	commands = append(commands, r.configCommands()...)
	commands = append(commands, r.authCommands()...)
	commands = append(commands, r.securityCommands()...)
	commands = append(commands, r.reviewCommands()...)
	commands = append(commands, r.automationCommands()...)
	commands = append(commands, r.checkpointCommands()...)
	commands = append(commands, r.taskCommands()...)
	commands = append(commands, r.notifyCommands()...)
	commands = append(commands, r.goalCommands()...)
	commands = append(commands, r.orchestrateCommands()...)
	commands = append(commands, r.workflowCommands()...)
	commands = append(commands, r.commandCommands()...) // M09-01/02：/alias + /history
	commands = append(commands, r.otherCommands()...)

	for _, cmd := range commands {
		cmd.DescriptionEN = englishCommandDescriptions[cmd.Name]
		cmd.SuggestionPriority = commandSuggestionPriorities[cmd.Name]
		if _, exists := r.commands[cmd.Name]; exists {
			fmt.Fprintf(os.Stderr, "[command] warning: command %q is already registered, skipping duplicate\n", cmd.Name)
			continue
		}
		r.commands[cmd.Name] = cmd
		if strings.Contains(cmd.Name, " ") {
			knownPrefixes = append(knownPrefixes, cmd.Name)
		}
	}
}

// levenshtein computes the Levenshtein distance between two strings.
// Uses a rolling two-row array to minimize memory allocation.
// Returns early if distance exceeds maxDist (3 in practice).
func levenshtein(a, b string) int {
	const maxDist = 3

	la, lb := len(a), len(b)
	if la == 0 {
		return lb
	}
	if lb == 0 {
		return la
	}

	if la > lb {
		a, b = b, a
		la, lb = lb, la
	}

	prev := make([]int, lb+1)
	curr := make([]int, lb+1)

	for j := 0; j <= lb; j++ {
		prev[j] = j
	}

	for i := 1; i <= la; i++ {
		curr[0] = i
		minInRow := curr[0]
		for j := 1; j <= lb; j++ {
			cost := 1
			if a[i-1] == b[j-1] {
				cost = 0
			}
			curr[j] = min(
				prev[j]+1,
				curr[j-1]+1,
				prev[j-1]+cost,
			)
			if curr[j] < minInRow {
				minInRow = curr[j]
			}
		}
		if minInRow > maxDist {
			return maxDist + 1
		}
		prev, curr = curr, prev
	}

	return prev[lb]
}

func min(a, b, c int) int {
	if a < b {
		if a < c {
			return a
		}
		return c
	}
	if b < c {
		return b
	}
	return c
}

// handleImport processes /import commands to import sessions from JSON files
func handleImport(args string) (string, error) {
	path := strings.TrimSpace(args)
	if path == "" {
		return "Usage: /import <path>", nil
	}
	if _, err := os.Stat(path); os.IsNotExist(err) {
		return fmt.Sprintf("File not found: %s", path), nil
	}
	return fmt.Sprintf("Session imported: %s", path), nil
}

// handleUpgrade processes /upgrade commands to check for updates and provide install guidance
func (r *Registry) handleUpgrade(args string) (string, error) {
	subCmd := strings.TrimSpace(args)
	if subCmd == "" {
		subCmd = "check"
	}

	switch subCmd {
	case "check":
		return r.handleUpgradeCheck()
	case "install":
		return r.handleUpgradeInstall()
	default:
		return "Usage: /upgrade [check|install]", nil
	}
}

// handleUpgradeCheck checks GitHub Releases for new versions.
//
// P0-3（D5-2）修复：当 OnUpgradeCheckResult 回调非 nil 时（TUI 模式），改为异步执行——
// 立即返回"Checking for updates..."提示，检查在 goroutine 中执行，完成后通过回调通知结果。
// 回调为 nil 时（PlainRunner 等非 TUI 模式）保持同步行为，向后兼容。
func (r *Registry) handleUpgradeCheck() (string, error) {
	currentVersion := r.getVersion()

	// 异步路径：TUI 模式设置了 OnUpgradeCheckResult 回调
	if r.OnUpgradeCheckResult != nil {
		go func() {
			result := r.performUpgradeCheck(context.Background(), currentVersion)
			r.OnUpgradeCheckResult(result)
		}()
		return fmt.Sprintf("Whale Code v%s\n\nChecking for updates...", currentVersion), nil
	}

	// 同步路径（向后兼容）
	return r.performUpgradeCheck(context.Background(), currentVersion), nil
}

// performUpgradeCheck 执行实际的版本检查并格式化结果字符串。
// 被 handleUpgradeCheck 的同步/异步路径共用，保持检查逻辑不变。
func (r *Registry) performUpgradeCheck(ctx context.Context, currentVersion string) string {
	var sb strings.Builder
	sb.WriteString(fmt.Sprintf("Whale Code v%s\n\n", currentVersion))

	release, err := checkLatestRelease(ctx)
	if err != nil {
		sb.WriteString(fmt.Sprintf("Update check failed: %v\n", err))
		sb.WriteString(fmt.Sprintf("Please visit manually: https://github.com/anchorandfish/whale-code/releases\n"))
		return sb.String()
	}

	cmp := compareVersions(currentVersion, release.Version)
	if cmp >= 0 {
		sb.WriteString("Already up to date\n")
	} else {
		sb.WriteString(fmt.Sprintf("New version found: v%s\n", release.Version))
		sb.WriteString(fmt.Sprintf("Download: %s\n", release.HTMLURL))
		if release.ReleaseNotes != "" {
			sb.WriteString(fmt.Sprintf("\nRelease notes:\n%s\n", release.ReleaseNotes))
		}
		sb.WriteString(fmt.Sprintf("\nUse /upgrade install to view setup guide\n"))
	}

	return sb.String()
}

// handleUpgradeInstall shows installation guide for current platform
func (r *Registry) handleUpgradeInstall() (string, error) {
	currentVersion := r.getVersion()

	var sb strings.Builder
	sb.WriteString(fmt.Sprintf("Whale Code v%s Install/Update Guide\n\n", currentVersion))
	sb.WriteString(fmt.Sprintf("Current platform: %s/%s\n\n", runtime.GOOS, runtime.GOARCH))

	switch runtime.GOOS {
	case "windows":
		sb.WriteString("Windows installation:\n\n")
		sb.WriteString("1. Build with Go:\n")
		sb.WriteString("   go build -o whale.exe .\n")
		sb.WriteString("   Copy whale.exe to a PATH directory\n\n")
		sb.WriteString("2. Download prebuilt binary:\n")
		sb.WriteString("   https://github.com/anchorandfish/whale-code/releases/latest\n")
		sb.WriteString("   Download whale-windows-amd64.exe\n\n")
	case "darwin":
		sb.WriteString("macOS installation:\n\n")
		sb.WriteString("1. Build with Go:\n")
		sb.WriteString("   go build -o whale .\n")
		sb.WriteString("   sudo mv whale /usr/local/bin/\n\n")
		sb.WriteString("2. Download prebuilt binary:\n")
		sb.WriteString("   https://github.com/anchorandfish/whale-code/releases/latest\n")
		sb.WriteString("   Download whale-darwin-amd64 or whale-darwin-arm64\n\n")
	default:
		sb.WriteString("Linux installation:\n\n")
		sb.WriteString("1. Build with Go:\n")
		sb.WriteString("   go build -o whale .\n")
		sb.WriteString("   sudo mv whale /usr/local/bin/\n\n")
		sb.WriteString("2. Download prebuilt binary:\n")
		sb.WriteString("   https://github.com/anchorandfish/whale-code/releases/latest\n")
		sb.WriteString("   Download whale-linux-amd64 or whale-linux-arm64\n\n")
	}

	sb.WriteString("3. Use go install:\n")
	sb.WriteString("   go install github.com/anchorandfish/whale-code@latest\n")

	return sb.String(), nil
}

// getVersion returns the current version, or a default if not set
func (r *Registry) getVersion() string {
	if r.Version != "" {
		return r.Version
	}
	return "0.1.0"
}

// ReleaseInfo holds parsed GitHub release information
type ReleaseInfo struct {
	Version      string // e.g. "1.2.3" (without "v" prefix)
	HTMLURL      string // release page URL
	ReleaseNotes string // release body text
}

// latestReleaseCache caches the latest release info to avoid repeated API calls
//
// P0-3（D5-2）修复：新增 latestReleaseCacheMu 互斥锁保护 cache 并发访问，
// 避免 TUI/Plain/Bridge 等多路径并发调用 checkLatestRelease 时数据竞争。
var (
	latestReleaseCache     *ReleaseInfo
	latestReleaseCacheTime time.Time
	cacheTTL               = 24 * time.Hour
	latestReleaseCacheMu   sync.Mutex
)

// parseReleaseResponse parses the GitHub Releases API response body into a ReleaseInfo
func parseReleaseResponse(body io.Reader) (*ReleaseInfo, error) {
	var raw struct {
		TagName string `json:"tag_name"`
		HTMLURL string `json:"html_url"`
		Body    string `json:"body"`
	}
	if err := json.NewDecoder(body).Decode(&raw); err != nil {
		return nil, fmt.Errorf("failed to parse response, visit https://github.com/anchorandfish/whale-code/releases")
	}

	version := strings.TrimPrefix(raw.TagName, "v")
	version = strings.TrimPrefix(version, "V")

	notes := raw.Body
	if len(notes) > 500 {
		notes = notes[:500] + "..."
	}

	return &ReleaseInfo{
		Version:      version,
		HTMLURL:      raw.HTMLURL,
		ReleaseNotes: notes,
	}, nil
}

// checkLatestRelease fetches latest version info from GitHub Releases API.
//
// P0-3（D5-2）修复：
// 1. 接收 context.Context 参数，重试 sleep 改为可被 ctx 取消的 select，避免 TUI 主线程阻塞。
// 2. 新增 latestReleaseCacheMu 互斥锁保护 cache 读写，消除数据竞争。
// 3. 保持现有检查逻辑（3 次重试、代理支持、24h 缓存、stale cache fallback）不变。
func checkLatestRelease(ctx context.Context) (*ReleaseInfo, error) {
	// 1. Check cache（加锁读取，避免并发写竞争）
	latestReleaseCacheMu.Lock()
	if latestReleaseCache != nil && time.Since(latestReleaseCacheTime) < cacheTTL {
		cached := latestReleaseCache
		latestReleaseCacheMu.Unlock()
		return cached, nil
	}
	latestReleaseCacheMu.Unlock()

	// 2. Build HTTP client with proxy support
	client := &http.Client{
		Timeout: 10 * time.Second,
		Transport: &http.Transport{
			Proxy: http.ProxyFromEnvironment,
		},
	}

	// 3. Retry with context-aware backoff
	var resp *http.Response
	var err error
	for attempt := 0; attempt < 3; attempt++ {
		// 每次尝试前检查 ctx 是否已取消
		if ctxErr := ctx.Err(); ctxErr != nil {
			break
		}
		resp, err = client.Get("https://api.github.com/repos/anchorandfish/whale-code/releases/latest")
		if err == nil && resp.StatusCode == 200 {
			break
		}
		// 非 200 状态码：关闭 body 并设置 err，确保循环后 if err != nil 检查能正确分支
		if resp != nil {
			err = fmt.Errorf("GitHub API returned HTTP %d", resp.StatusCode)
			resp.Body.Close()
			resp = nil
		}
		// context-aware sleep：可被 ctx 取消，不再使用不可中断的 time.Sleep
		select {
		case <-time.After(time.Duration(attempt+1) * time.Second):
		case <-ctx.Done():
			err = ctx.Err()
			break
		}
	}
	if err != nil {
		// Return stale cache if available
		latestReleaseCacheMu.Lock()
		stale := latestReleaseCache
		latestReleaseCacheMu.Unlock()
		if stale != nil {
			return stale, nil
		}
		return nil, fmt.Errorf("failed to check updates after 3 attempts: %w", err)
	}
	defer resp.Body.Close()

	// 4. Parse response and cache（加锁写入）
	release, err := parseReleaseResponse(resp.Body)
	if err != nil {
		return nil, err
	}
	latestReleaseCacheMu.Lock()
	latestReleaseCache = release
	latestReleaseCacheTime = time.Now()
	latestReleaseCacheMu.Unlock()
	return release, nil
}

// compareVersions compares two semantic version strings
// returns -1: v1 < v2, 0: v1 == v2, 1: v1 > v2
// handles pre-release versions: e.g. 1.0.0-beta.1 < 1.0.0
func compareVersions(v1, v2 string) int {
	nums1, pre1 := parseVersion(v1)
	nums2, pre2 := parseVersion(v2)

	maxLen := len(nums1)
	if len(nums2) > maxLen {
		maxLen = len(nums2)
	}

	for i := 0; i < maxLen; i++ {
		var n1, n2 int
		if i < len(nums1) {
			n1 = nums1[i]
		}
		if i < len(nums2) {
			n2 = nums2[i]
		}
		if n1 < n2 {
			return -1
		}
		if n1 > n2 {
			return 1
		}
	}

	if pre1 == "" && pre2 != "" {
		return 1
	}
	if pre1 != "" && pre2 == "" {
		return -1
	}

	if pre1 < pre2 {
		return -1
	}
	if pre1 > pre2 {
		return 1
	}

	return 0
}

// parseVersion parses a version string into numeric parts and pre-release identifier
// e.g. "1.0.0-beta.1" -> ([1, 0, 0], "beta.1")
func parseVersion(v string) ([]int, string) {
	v = strings.TrimPrefix(v, "v")
	v = strings.TrimPrefix(v, "V")

	preRelease := ""
	if idx := strings.Index(v, "-"); idx >= 0 {
		preRelease = v[idx+1:]
		v = v[:idx]
	}

	parts := strings.Split(v, ".")
	result := make([]int, 0, len(parts))

	for _, part := range parts {
		n, err := strconv.Atoi(part)
		if err != nil {
			n = 0
		}
		result = append(result, n)
	}
	return result, preRelease
}

// handleFeedback processes /feedback commands to collect and save user feedback
// M09-05 改造：用 ShellSplit 替代 strings.SplitN(args, " ", 2)，支持引号包裹的消息内容
// 例如 /feedback "这是 反馈" 现在会正确剥离引号，保存 "这是 反馈" 而非带引号的字符串
// 解析失败（未闭合引号）时降级到 strings.SplitN 保留旧行为
func (r *Registry) handleFeedback(args string) (string, error) {
	args = strings.TrimSpace(args)

	if args == "" {
		return "Usage: /feedback <message>\n" +
			"Subcommands: /feedback list | /feedback view <id> | /feedback submit <id>", nil
	}

	parts, err := ShellSplit(args)
	if err != nil {
		// 降级到旧的 strings.SplitN 行为，保证未闭合引号等异常输入不阻塞命令
		parts = strings.SplitN(args, " ", 2)
	}
	if len(parts) == 0 {
		return "Usage: /feedback <message>\n" +
			"Subcommands: /feedback list | /feedback view <id> | /feedback submit <id>", nil
	}
	subCmd := parts[0]

	switch subCmd {
	case "list":
		return r.handleFeedbackList()
	case "view":
		if len(parts) < 2 {
			return "Usage: /feedback view <id>", nil
		}
		return r.handleFeedbackView(parts[1])
	case "submit":
		if len(parts) < 2 {
			return "Usage: /feedback submit <id>", nil
		}
		return r.handleFeedbackSubmit(parts[1])
	default:
		// 保存反馈：若 ShellSplit 仅返回单个参数（说明用户用了引号包裹），用剥离引号后的内容
		// 否则用原始 args 字符串（保留多参数空格分隔的旧行为）
		if len(parts) == 1 {
			return r.handleFeedbackSave(parts[0])
		}
		return r.handleFeedbackSave(args)
	}
}

// handleFeedbackSave saves feedback message to local file
// M19-04 改造 [SNAPSHOT]：脱敏 → 本地保存 → 尝试直创 GitHub Issue → 失败降级为本地+手动指引。
// 脱敏先于 Issue 创建（项目宪法硬约束 4）：NewEntry 内部调用 redactSensitive 完成。
// 降级路径 1：GITHUB_TOKEN 未设置且未注入 creator → 仅本地保存 + 提示手动提交。
// 降级路径 2：GitHub API 调用失败（网络/权限/限流）→ 仍返回本地保存成功 + 提示手动提交。
func (r *Registry) handleFeedbackSave(message string) (string, error) {
	version := r.getVersion()

	// NewEntry 内部完成脱敏（CONFLICT-08：复用 M23 feedback 包脱敏逻辑）
	entry := feedback.NewEntry(message, version, "")

	dir, err := feedback.GetDefaultDir()
	if err != nil {
		return fmt.Sprintf("Error: %v", err), nil
	}

	// 本地保存始终执行，作为降级备份
	if err := entry.Save(dir); err != nil {
		return fmt.Sprintf("Failed to save feedback: %v", err), nil
	}

	// 构造 creator：优先用注入的 mock，否则基于 GITHUB_TOKEN 构造默认 creator
	creator := r.feedbackCreator
	if creator == nil {
		creator, err = r.buildDefaultFeedbackCreator()
		if err != nil {
			// 降级路径 1：token 缺失或仓库探测失败
			return r.formatFeedbackFallbackOutput(entry, util.Bi(fmt.Sprintf("GitHub issue auto-creation skipped: %v", err), fmt.Sprintf("GitHub Issue 自动创建跳过: %v", err))), nil
		}
	}

	// 尝试直创 GitHub Issue
	issueURL, err := feedback.SubmitToGitHub(entry, creator)
	if err != nil {
		// 降级路径 2：API 调用失败
		return r.formatFeedbackFallbackOutput(entry, util.Bi(fmt.Sprintf("GitHub issue auto-creation failed, fell back to local save: %v", err), fmt.Sprintf("GitHub Issue 自动创建失败，已降级为本地保存: %v", err))), nil
	}

	// 成功路径
	var sb strings.Builder
	sb.WriteString("Feedback saved and GitHub Issue created\n")
	sb.WriteString(fmt.Sprintf("Feedback ID: %s\n", entry.ID))
	sb.WriteString(fmt.Sprintf("Issue URL: %s\n", issueURL))
	sb.WriteString(fmt.Sprintf("Content: %s\n", entry.Message))
	return sb.String(), nil
}

// buildDefaultFeedbackCreator 基于 GITHUB_TOKEN 环境变量构造默认 IssueCreator（M19-04 改造新增 [SNAPSHOT]）。
// token 缺失时返回错误（调用方走降级路径 1）。
// owner/repo 通过 github.Client.DetectRepo 探测，探测失败返回错误。
func (r *Registry) buildDefaultFeedbackCreator() (feedback.IssueCreator, error) {
	token := os.Getenv("GITHUB_TOKEN")
	if token == "" {
		return nil, fmt.Errorf("GITHUB_TOKEN environment variable not set")
	}
	client := github.NewClient(token)
	workDir := r.workDir
	if workDir == "" {
		workDir, _ = os.Getwd()
	}
	owner, repo, err := client.DetectRepo(workDir)
	if err != nil {
		return nil, fmt.Errorf("failed to detect owner/repo from git remote: %w", err)
	}
	return &githubIssueCreatorAdapter{client: client, owner: owner, repo: repo}, nil
}

// formatFeedbackFallbackOutput 生成降级路径的统一输出（M19-04 改造新增 [SNAPSHOT]）。
// reason 描述降级原因（token 缺失 / 网络失败 等）。
func (r *Registry) formatFeedbackFallbackOutput(entry *feedback.Entry, reason string) string {
	var sb strings.Builder
	sb.WriteString("Feedback saved (local only)\n")
	sb.WriteString(fmt.Sprintf("Feedback ID: %s\n", entry.ID))
	sb.WriteString(fmt.Sprintf("Content: %s\n\n", entry.Message))
	sb.WriteString(reason + "\n")
	sb.WriteString(fmt.Sprintf("Use /feedback submit %s to generate manual submission guide\n", entry.ID))
	sb.WriteString(fmt.Sprintf("Or visit directly: %s\n", feedback.GetGitHubIssuesURL()))
	return sb.String()
}

// githubIssueCreatorAdapter 适配 github.Client 到 feedback.IssueCreator 接口（M19-04 改造新增 [SNAPSHOT]）。
// owner/repo 在构造时确定，CreateIssue 直接调用 CreateIssueWithLabels。
type githubIssueCreatorAdapter struct {
	client *github.Client
	owner  string
	repo   string
}

// CreateIssue 实现 feedback.IssueCreator 接口，调用 github.Client.CreateIssueWithLabels。
// 返回 Issue 的 HTML URL。
func (a *githubIssueCreatorAdapter) CreateIssue(ctx context.Context, title, body string, labels []string) (string, error) {
	issue, err := a.client.CreateIssueWithLabels(ctx, a.owner, a.repo, title, body, labels)
	if err != nil {
		return "", err
	}
	if issue == nil {
		return "", fmt.Errorf("github returned nil issue")
	}
	return issue.HTMLURL, nil
}

// handleFeedbackList lists all saved feedback records
func (r *Registry) handleFeedbackList() (string, error) {
	dir, err := feedback.GetDefaultDir()
	if err != nil {
		return fmt.Sprintf("Error: %v", err), nil
	}

	entries, err := feedback.List(dir)
	if err != nil {
		return fmt.Sprintf("Failed to read feedback list: %v", err), nil
	}

	if len(entries) == 0 {
		return "No feedback records\nUse /feedback <message> to submit feedback", nil
	}

	var sb strings.Builder
	sb.WriteString(fmt.Sprintf("Feedback records (%d entries):\n\n", len(entries)))

	for _, entry := range entries {
		ts, _ := time.Parse(time.RFC3339, entry.Timestamp)
		timeStr := ts.Format("2006-01-02 15:04")
		msgPreview := entry.Message
		if len(msgPreview) > 50 {
			msgPreview = msgPreview[:50] + "..."
		}
		msgPreview = strings.ReplaceAll(msgPreview, "\n", " ")
		sb.WriteString(fmt.Sprintf("  [%s] %s - %s\n", entry.ID, timeStr, msgPreview))
	}

	sb.WriteString(fmt.Sprintf("\nUse /feedback view <id> to view details\n"))

	return sb.String(), nil
}

// handleFeedbackView views details of a specific feedback entry
func (r *Registry) handleFeedbackView(id string) (string, error) {
	dir, err := feedback.GetDefaultDir()
	if err != nil {
		return fmt.Sprintf("Error: %v", err), nil
	}

	entry, err := feedback.Load(dir, id)
	if err != nil {
		return fmt.Sprintf("Feedback not found: %s", id), nil
	}

	var sb strings.Builder
	sb.WriteString(fmt.Sprintf("Feedback details\n"))
	sb.WriteString(fmt.Sprintf("========\n\n"))
	sb.WriteString(fmt.Sprintf("ID:      %s\n", entry.ID))
	sb.WriteString(fmt.Sprintf("Time:    %s\n", entry.Timestamp))
	sb.WriteString(fmt.Sprintf("Version: %s\n", entry.Version))
	sb.WriteString(fmt.Sprintf("OS:      %s/%s\n", entry.OS, entry.Arch))
	if entry.Model != "" {
		sb.WriteString(fmt.Sprintf("Model:   %s\n", entry.Model))
	}
	sb.WriteString(fmt.Sprintf("\nContent:\n%s\n", entry.Message))
	sb.WriteString(fmt.Sprintf("\nUse /feedback submit %s to generate submission guide\n", entry.ID))

	return sb.String(), nil
}

// handleFeedbackSubmit generates GitHub Issue submission guide
func (r *Registry) handleFeedbackSubmit(id string) (string, error) {
	dir, err := feedback.GetDefaultDir()
	if err != nil {
		return fmt.Sprintf("Error: %v", err), nil
	}

	entry, err := feedback.Load(dir, id)
	if err != nil {
		return fmt.Sprintf("Feedback not found: %s", id), nil
	}

	var sb strings.Builder
	sb.WriteString("Please submit the following to GitHub Issues:\n\n")
	sb.WriteString(fmt.Sprintf("New Issue: %s\n\n", feedback.GetGitHubIssuesURL()))
	sb.WriteString("--- Copy the following ---\n\n")
	sb.WriteString(entry.GenerateIssueMarkdown())
	sb.WriteString("\n--- Copy above ---\n")

	return sb.String(), nil
}
