package hook

import (
	"context"

	"encoding/json"

	"net/http"

	"sync"

	"github.com/anchorandfish/whale-code/internal/sandbox"
	"github.com/anchorandfish/whale-code/internal/secret"
)

// Action 钩子动作
type Action string

const (
	ActionAllow Action = "allow"
	ActionBlock Action = "block"
	ActionAsk   Action = "ask"
	ActionWarn  Action = "warn"
)

// HookEventType 钩子事件类型，对标同类 CLI 的 HookEvent 枚举
type HookEventType string

const (
	// 工具生命周期
	EventPreToolUse  HookEventType = "PreToolUse"
	EventPostToolUse HookEventType = "PostToolUse"

	// 文件操作
	EventPreRead   HookEventType = "PreRead"
	EventPostRead  HookEventType = "PostRead"
	EventPreWrite  HookEventType = "PreWrite"
	EventPostWrite HookEventType = "PostWrite"
	EventPreEdit   HookEventType = "PreEdit"
	EventPostEdit  HookEventType = "PostEdit"

	// Bash
	EventPreBash  HookEventType = "PreBash"
	EventPostBash HookEventType = "PostBash"

	// 搜索
	EventPreSearch  HookEventType = "PreSearch"
	EventPostSearch HookEventType = "PostSearch"

	// Git
	EventPreGit  HookEventType = "PreGit"
	EventPostGit HookEventType = "PostGit"

	// Web
	EventPreWeb  HookEventType = "PreWeb"
	EventPostWeb HookEventType = "PostWeb"

	// 子代理
	EventPreAgent  HookEventType = "PreAgent"
	EventPostAgent HookEventType = "PostAgent"

	// 会话生命周期
	EventSessionStart      HookEventType = "SessionStart"
	EventSessionStop       HookEventType = "SessionStop"
	EventStop              HookEventType = "Stop"
	EventPostCompact       HookEventType = "PostCompact"
	EventNotification      HookEventType = "Notification"
	EventPermissionRequest HookEventType = "PermissionRequest"
)

// IsLifecycleEvent 判断是否为生命周期事件（deny 被忽略）
// 对标同类 CLI 的 "deny is ignored for lifecycle hooks"
func (e HookEventType) IsLifecycleEvent() bool {
	switch e {
	case EventPostCompact, EventSessionStop, EventNotification, EventSessionStart, EventStop:
		return true
	default:
		return false
	}
}

// HookType 钩子执行类型
type HookType string

const (
	HookTypeCommand  HookType = "command"
	HookTypePrompt   HookType = "prompt"
	HookTypeAgent    HookType = "agent"
	HookTypeHTTP     HookType = "http"
	HookTypeCallback HookType = "callback"
)

// HookSource 钩子来源标识
type HookSource string

const (
	// SourceBuiltin 内置钩子
	SourceBuiltin HookSource = "builtin"
	// SourcePlugin 插件钩子
	SourcePlugin HookSource = "plugin"
	// SourceConfig 配置文件钩子
	SourceConfig HookSource = "config"
)

// hookMarker 未导出标记类型，用于密封 Hook 接口（外部包无法实现 Hook）
type hookMarker struct{}

// Hook 是钩子的 discriminated union 接口（FACT-HOOK-004 修复）
// 每种具体类型（HookCommand/HookPrompt/HookAgent/HookHTTP/HookCallback）
// 只包含自己的类型特有字段，编译期保证不会出现 Type=http 但 Command 非空的不安全状态。
type Hook interface {
	// Base 返回公共字段指针
	Base() *HookBase
	// Clone 深拷贝当前钩子，避免返回的指针指向 Manager 内部 config 切片
	Clone() Hook
	// sealed 密封方法，外部包无法实现 Hook 接口
	sealed() hookMarker
}

// HookBase 所有钩子类型的公共字段（嵌入到各具体类型中）
type HookBase struct {
	ID       string     `json:"id,omitempty"`
	Type     HookType   `json:"type"`     // 钩子类型，构造时设置，不可变
	Matcher  string     `json:"matcher"`  // 工具名 glob 匹配
	Pattern  string     `json:"pattern"`  // 文件路径 glob 匹配
	Action   Action     `json:"action"`   // 动作：allow/block/ask/warn
	Reason   string     `json:"reason"`   // 阻止原因
	Source   HookSource `json:"-"`        // 钩子来源，不序列化到配置文件
	Timeout  int        `json:"timeout"`  // 超时毫秒，0=默认 30s
	Enabled  bool       `json:"enabled"`  // 是否启用（false 且由配置显式声明 ⇒ 不运行，见 IsEnabled）
	Priority int        `json:"priority"` // 优先级，数字小先执行
	// enabledExplicit 标记 Enabled 是否由**配置显式声明**（`enabled: true|false`）。
	//
	// 为什么需要它（D2 / S7-P1-① 根因）：bool 无法区分「配置里没写该字段」与「写了 enabled:false」，
	// 而两者语义必须不同 —— 前者是"默认启用"（现存所有 hooks.json 都没写这个字段，不能因此全部停摆），
	// 后者是用户明确关闭（修复前该字段是**死配置**：`enabled:false` 照跑，而 `/hooks` 列表却显示 (disabled)）。
	// 因此三态在**反序列化边界**（registry.toHook）落实：未写 ⇒ Enabled=true，写了 ⇒ 按值。
	// 程序化构造（AddHook / 插件注入 / 测试）恒为 false ⇒ 默认启用（零行为变更），
	// 需要显式禁用时调用 SetEnabled(false)。
	enabledExplicit bool
	// SNAPSHOT：per-hook 沙箱级别（omitempty，默认 disabled=0 向后兼容）。
	// 用户可在配置文件中单独指定每个 hook 的沙箱级别，覆盖 Manager.defaultSandboxLevel。
	// 取值：disabled（跳过沙箱）/ relaxed / strict / isolated。
	// 官方源： @2026-07-20（SandboxLevel 枚举）
	SandboxLevel sandbox.SandboxLevel `json:"sandbox_level,omitempty" yaml:"sandbox_level,omitempty"`
}

// IsEnabled 报告该 hook 是否参与执行（D2：`enabled:false` 必须真的生效）。
//
// 语义（三态，见 enabledExplicit 注释）：
// - 配置显式 `enabled:false` ⇒ false（不运行，且不得短路后续 hook 链）
// - 配置显式 `enabled:true` / 未写该字段 / 程序化构造 ⇒ true
func (b *HookBase) IsEnabled() bool {
	if b == nil {
		return false
	}
	return !b.enabledExplicit || b.Enabled
}

// SetEnabled 显式设置启用状态（并标记为"显式"）。
//
// 程序化路径（如 /hooks add 解析出 enabled=false）用它表达用户的显式意图 ——
// 直接写 `Enabled` 字段只在配置反序列化路径被解释，这是刻意的不对称：
// 否则 AddHook / 插件注入等"没写该字段"的调用方会被 bool 零值静默禁用。
func (b *HookBase) SetEnabled(enabled bool) {
	if b == nil {
		return
	}
	b.Enabled = enabled
	b.enabledExplicit = true
}

// HookCommand 命令类型钩子：通过子进程执行 shell 命令
type HookCommand struct {
	HookBase
	Command string `json:"command"` // shell 命令
}

// Base 返回公共字段指针
func (h *HookCommand) Base() *HookBase { return &h.HookBase }

// Clone 深拷贝
func (h *HookCommand) Clone() Hook {
	cp := *h
	return &cp
}

func (h *HookCommand) sealed() hookMarker { return hookMarker{} }

// HookPrompt prompt 类型钩子：调用 AI 评估 prompt 并返回决策
type HookPrompt struct {
	HookBase
	Prompt string `json:"prompt"` // prompt 文本
	Model  string `json:"model"`  // 模型名
}

// Base 返回公共字段指针
func (h *HookPrompt) Base() *HookBase { return &h.HookBase }

// Clone 深拷贝
func (h *HookPrompt) Clone() Hook {
	cp := *h
	return &cp
}

func (h *HookPrompt) sealed() hookMarker { return hookMarker{} }

// HookAgent agent 类型钩子：启动子代理评估（只读模式）
type HookAgent struct {
	HookBase
	Prompt string `json:"prompt"` // agent prompt 文本
	Model  string `json:"model"`  // 模型名
}

// Base 返回公共字段指针
func (h *HookAgent) Base() *HookBase { return &h.HookBase }

// Clone 深拷贝
func (h *HookAgent) Clone() Hook {
	cp := *h
	return &cp
}

func (h *HookAgent) sealed() hookMarker { return hookMarker{} }

// HookHTTP http 类型钩子：发送 HTTP POST 请求并解析响应
type HookHTTP struct {
	HookBase
	URL     string            `json:"url"`     // 请求 URL
	Headers map[string]string `json:"headers"` // 自定义请求头
}

// Base 返回公共字段指针
func (h *HookHTTP) Base() *HookBase { return &h.HookBase }

// Clone 深拷贝
func (h *HookHTTP) Clone() Hook {
	cp := *h
	if h.Headers != nil {
		cp.Headers = make(map[string]string, len(h.Headers))
		for k, v := range h.Headers {
			cp.Headers[k] = v
		}
	}
	return &cp
}

func (h *HookHTTP) sealed() hookMarker { return hookMarker{} }

// HookCallback callback 类型钩子：直接调用 Go 函数
type HookCallback struct {
	HookBase
	Callback func(HookPayload) HookResult `json:"-"` // 回调函数，不序列化
}

// Base 返回公共字段指针
func (h *HookCallback) Base() *HookBase { return &h.HookBase }

// Clone 深拷贝（Callback 函数引用共享，不可深拷贝）
func (h *HookCallback) Clone() Hook {
	cp := *h
	return &cp
}

func (h *HookCallback) sealed() hookMarker { return hookMarker{} }

// hookRaw 是 JSON/YAML 反序列化的中间结构，包含所有可能的字段
// 根据 Type 字段分发到具体 Hook 实现，保证类型安全
//
// SNAPSHOT：字段 tag 增加 yaml 支持（与 json 对齐），
// 子审查优化 1 吸纳：所有字段同时含 yaml + json tag，字段名一致。
type hookRaw struct {
	ID      string            `json:"id,omitempty" yaml:"id,omitempty"`
	Type    HookType          `json:"type" yaml:"type"`
	Matcher string            `json:"matcher" yaml:"matcher"`
	Pattern string            `json:"pattern" yaml:"pattern"`
	Action  Action            `json:"action" yaml:"action"`
	Reason  string            `json:"reason" yaml:"reason"`
	Command string            `json:"command" yaml:"command"`
	Timeout int               `json:"timeout" yaml:"timeout"`
	Prompt  string            `json:"prompt" yaml:"prompt"`
	Model   string            `json:"model" yaml:"model"`
	URL     string            `json:"url" yaml:"url"`
	Headers map[string]string `json:"headers" yaml:"headers"`
	// Enabled 是指针：nil = 配置未写该字段（默认启用），非 nil = 显式声明（D2 三态）。
	Enabled  *bool `json:"enabled" yaml:"enabled"`
	Priority int   `json:"priority" yaml:"priority"`
}

// HookConfig 钩子配置，对标同类 CLI 20+ Hook Events
type HookConfig struct {
	// 工具生命周期
	PreToolUse  []Hook `json:"pre_tool_use"`
	PostToolUse []Hook `json:"post_tool_use"`

	// 文件操作
	PreRead   []Hook `json:"pre_read"`
	PostRead  []Hook `json:"post_read"`
	PreWrite  []Hook `json:"pre_write"`
	PostWrite []Hook `json:"post_write"`
	PreEdit   []Hook `json:"pre_edit"`
	PostEdit  []Hook `json:"post_edit"`

	// Bash
	PreBash  []Hook `json:"pre_bash"`
	PostBash []Hook `json:"post_bash"`

	// 搜索
	PreSearch  []Hook `json:"pre_search"`
	PostSearch []Hook `json:"post_search"`

	// Git
	PreGit  []Hook `json:"pre_git"`
	PostGit []Hook `json:"post_git"`

	// Web
	PreWeb  []Hook `json:"pre_web"`
	PostWeb []Hook `json:"post_web"`

	// 子代理
	PreAgent  []Hook `json:"pre_agent"`
	PostAgent []Hook `json:"post_agent"`

	// 会话生命周期
	SessionStart []Hook `json:"session_start"`
	SessionStop  []Hook `json:"session_stop"`
	Stop         []Hook `json:"stop"`
	PostCompact  []Hook `json:"post_compact"`
	Notification []Hook `json:"notification"`
}

// HookResult 钩子执行结果，对标同类 CLI PreToolHookResult / HookRunResult
type HookResult struct {
	Action       Action
	Reason       string
	Output       string
	Error        error
	Modified     *string  // PostHook 修改工具输出
	UpdatedInput *string  // PreHook 修改工具输入（JSON 字符串）
	Messages     []string // 钩子产生的消息
	// Skipped 为 true 表示本次结果**不是一次决策**，而是"该 hook 没有参与"（未信任 / 显式禁用）。
	//
	// 为什么必须是结构化的字段（R4 教训：可判定的结构化事实不得由散文关键词断言）：
	// 8 个 Run* 路径靠 `shouldReturnResult(h)` 决定是否短路整条链，而跳过结果恰好是
	// Action=Allow + 非空 Command ⇒ 旧实现会让"被跳过的 hook"静默吃掉后面已信任 hook 的 block
	// （S7-P0-③ 红测：[未信任 allow, 已信任 block] ⇒ 期望拦、实际放行）。
	// 调用方必须先看 Skipped：为 true 则 `continue` 下一条，而不是 return。
	Skipped bool
	// SkipReason 是 Skipped 的机器判据（枚举，不是给模型读的散文）。
	SkipReason HookSkipReason
}

// HookSkipReason 是"未参与决策"的结构化原因（D1/D2 的机器判据）。
type HookSkipReason string

const (
	// SkipNone 未跳过。
	SkipNone HookSkipReason = ""
	// SkipUntrusted 信任门未信任（PR-A Q1）：hook 不运行，但也不得阻断工具调用。
	SkipUntrusted HookSkipReason = "untrusted"
	// SkipDisabled 配置显式 enabled:false（D2）：用户关掉的 hook 必须真的不运行。
	SkipDisabled HookSkipReason = "disabled"
)

// HookPayload 钩子命令 stdin payload，对标同类 CLI 的 hook payload 机制
type HookPayload struct {
	HookEventName         string      `json:"hook_event_name"`
	ToolName              string      `json:"tool_name,omitempty"`
	ToolInput             interface{} `json:"tool_input,omitempty"`
	ToolInputJSON         string      `json:"tool_input_json,omitempty"`
	ToolOutput            string      `json:"tool_output,omitempty"`
	ToolResultIsError     bool        `json:"tool_result_is_error,omitempty"`
	SessionPath           string      `json:"session_path,omitempty"`
	MessageCount          int         `json:"message_count,omitempty"`
	RemovedMessageCount   int         `json:"removed_message_count,omitempty"`
	ResultingMessageCount int         `json:"resulting_message_count,omitempty"`
	FilePath              string      `json:"file_path,omitempty"`
	OldContent            string      `json:"old_content,omitempty"`
	NewContent            string      `json:"new_content,omitempty"`
	Command               string      `json:"command,omitempty"`
	Cwd                   string      `json:"cwd"`
}

// HookJSONControl JSON 控制格式，钩子命令 stdout 返回
// 对标同类 CLI PreHookJsonControl
type HookJSONControl struct {
	Decision     string          `json:"decision,omitempty"`     // "allow" | "deny"
	UpdatedInput json.RawMessage `json:"updatedInput,omitempty"` // 替换工具输入
	Message      string          `json:"message,omitempty"`      // 附加消息
}

// HookAIClient 钩子系统的 AI 客户端接口
type HookAIClient interface {
	// EvaluatePrompt 评估 prompt 并返回响应文本
	EvaluatePrompt(ctx context.Context, prompt string, model string) (string, error)
}

// Manager 钩子管理器
type Manager struct {
	mu         sync.RWMutex
	config     HookConfig
	workDir    string
	aiClient   HookAIClient
	httpClient *http.Client
	obs        *observabilityState // SNAPSHOT：可观测性状态（nil=禁用，零开销）
	// SNAPSHOT：沙箱管理器（可选注入，nil=禁用，向后兼容）。
	// 通过 SetSandboxManager 注入；executeCommandHook 调用 applySandbox（Prepare + Restrict）实现 OS 级隔离。
	// 降级策略：sandboxMgr=nil 或 Prepare/Restrict 失败时 fail-open（降级执行 + 记录日志）。
	// 官方源： @2026-07-20（OSSandbox 接口）
	sandboxMgr          sandbox.Manager
	defaultSandboxLevel sandbox.SandboxLevel
	// PR-A 信任门（Q1 拍板：对齐业内既有实现 startup_hooks_review，不信任则不运行）。
	// trustEnabled=false 为库默认（向后兼容，不打断既有调用方）；
	// 生产在 Executor.InitPipelineWithHooks 中调用 EnableTrustGate() 开启。
	trustEnabled bool
	// trustPath 信任库落盘位置，空则回落 ~/.whale/hooks.trust.json。
	trustPath string
	// trustRecords 信任库的内存缓存（惰性加载，避免每次 hook 执行都读盘）。
	trustRecords map[string]trustRecord
	// outcomeSink 是钩子结果里"结构化事实"的出口（D3：HookResult.Messages 生产了但零消费 ⇒
	// 跳过/超时/失败全静默）。所有执行路径汇聚到 ExecuteHookWithPayload，出口只在这里记一次；
	// 惰性初始化（nil 时首次记录时创建），故 zero-value Manager 也可用。
	outcomeSink *hookOutcomeSink
}

// safeEnvWhitelist 允许传递给 hook 子进程的系统环境变量白名单。
// H3 收口：委托 secret.DefaultAllowedEnv（守 02 §2.3.1 契约，env 脱敏唯一入口）。
// 保留此变量以兼容 hook_security_test.go 中的引用。
var safeEnvWhitelist = secret.DefaultAllowedEnv

// maxToolOutputEnvSize 环境变量 HOOK_TOOL_OUTPUT 的最大字节数
const maxToolOutputEnvSize = 32768

// maxToolInputEnvSize 环境变量 HOOK_TOOL_INPUT 的最大字节数
const maxToolInputEnvSize = 32768
