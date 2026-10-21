package tui

import (
	"context"

	"encoding/json"

	"errors"

	"fmt"

	"log/slog"

	"runtime"

	"strings"

	"sync"
	"sync/atomic"

	"time"
	"unicode/utf8"

	"github.com/charmbracelet/bubbles/textarea"

	"github.com/charmbracelet/bubbles/viewport"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/charmbracelet/lipgloss"

	"github.com/charmbracelet/x/ansi"

	"github.com/mattn/go-runewidth"

	"github.com/anchorandfish/whale-code/internal/ai"

	"github.com/anchorandfish/whale-code/internal/agentguard"

	"github.com/anchorandfish/whale-code/internal/bridge"

	"github.com/anchorandfish/whale-code/internal/bus"

	"github.com/anchorandfish/whale-code/internal/command"

	"github.com/anchorandfish/whale-code/internal/config"

	ctxpkg "github.com/anchorandfish/whale-code/internal/context"

	"github.com/anchorandfish/whale-code/internal/daemon"
	"github.com/anchorandfish/whale-code/internal/diff"

	"github.com/anchorandfish/whale-code/internal/github"

	"github.com/anchorandfish/whale-code/internal/goal"

	"github.com/anchorandfish/whale-code/internal/keybindings"

	"github.com/anchorandfish/whale-code/internal/mcp"

	"github.com/anchorandfish/whale-code/internal/monitor"

	"github.com/anchorandfish/whale-code/internal/notify"

	"github.com/anchorandfish/whale-code/internal/orchestrator"

	"github.com/anchorandfish/whale-code/internal/permission"

	"github.com/anchorandfish/whale-code/internal/search"

	whalelog "github.com/anchorandfish/whale-code/internal/log"
	"github.com/anchorandfish/whale-code/internal/session"

	"github.com/anchorandfish/whale-code/internal/tools"

	"github.com/anchorandfish/whale-code/internal/typeahead"
)

var (
	titleStyle = lipgloss.NewStyle().
			Bold(true).
			Foreground(lipgloss.Color("#00ADD8")).
			MarginBottom(1)

	userStyle = lipgloss.NewStyle().
			Foreground(lipgloss.Color("#7DC4E4"))

	assistantStyle = lipgloss.NewStyle().
			Foreground(lipgloss.Color("#A6DA95"))

	toolStyle = lipgloss.NewStyle().
			Foreground(lipgloss.Color("#8BE9FD"))

	errorStyle = lipgloss.NewStyle().
			Foreground(lipgloss.Color("#ED8796"))

	statusStyle = lipgloss.NewStyle().
			Foreground(lipgloss.Color("#8AADF4")).
			Border(lipgloss.RoundedBorder(), true).
			Padding(0, 1)

	// 视口未到底部时，"有新内容但你看不见"的角标样式。
	// 用高对比橙黄色 + 加粗，确保它在常驻状态栏里一眼可见（这是唯一的可见信号）。
	unreadStyle = lipgloss.NewStyle().
			Foreground(lipgloss.Color("#F5A97F")).
			Bold(true)

	thinkingStyle = lipgloss.NewStyle().
			Foreground(lipgloss.Color("#939AB7")).
			Italic(true)

	thinkingLabelStyle = lipgloss.NewStyle().
				Foreground(lipgloss.Color("#8BE9FD")).
				Bold(true)

	thinkingBorderStyle = lipgloss.NewStyle().
				Foreground(lipgloss.Color("#494D64")).
				Italic(true)

	cmdStyle = lipgloss.NewStyle().
			Foreground(lipgloss.Color("#FFD700")).Bold(true)

	separatorStyle = lipgloss.NewStyle().
			Foreground(lipgloss.Color("#363A4F"))

	// 时间戳样式，dim 灰色不突出显示，用于消息下方时间戳
	timestampStyle = lipgloss.NewStyle().
			Foreground(lipgloss.Color("#6C7086"))

	inputBoxStyle = lipgloss.NewStyle().
			Border(lipgloss.RoundedBorder()).
			BorderForeground(lipgloss.Color("#494D64")).
			PaddingLeft(1)

	sendBtnStyle = lipgloss.NewStyle().
			Border(lipgloss.RoundedBorder()).
			BorderForeground(lipgloss.Color("#494D64")).
			Padding(0, 2).
			Foreground(lipgloss.Color("#6C7086"))

	sendBtnActiveStyle = lipgloss.NewStyle().
				Border(lipgloss.RoundedBorder()).
				BorderForeground(lipgloss.Color("#8AADF4")).
				Padding(0, 2).
				Foreground(lipgloss.Color("#8AADF4")).
				Bold(true)

	stopBtnStyle = lipgloss.NewStyle().
			Border(lipgloss.RoundedBorder()).
			BorderForeground(lipgloss.Color("#494D64")).
			Padding(0, 2).
			Foreground(lipgloss.Color("#F38BA8"))

	stopBtnActiveStyle = lipgloss.NewStyle().
				Border(lipgloss.RoundedBorder()).
				BorderForeground(lipgloss.Color("#F38BA8")).
				Padding(0, 2).
				Foreground(lipgloss.Color("#F38BA8")).
				Bold(true)

	copyBtnStyle = lipgloss.NewStyle().
			Border(lipgloss.RoundedBorder()).
			BorderForeground(lipgloss.Color("#494D64")).
			Padding(0, 2).
			Foreground(lipgloss.Color("#8BE9FD"))

	// copyBtnActiveStyle 是 Copy all 的「engaged」态（：鼠标悬停/按下与 Tab
	// 焦点共用同一套高亮口径，与 send/stop 的 active 样式同构：亮色 + 粗体）。
	copyBtnActiveStyle = lipgloss.NewStyle().
				Border(lipgloss.RoundedBorder()).
				BorderForeground(lipgloss.Color("#8BE9FD")).
				Padding(0, 2).
				Foreground(lipgloss.Color("#8BE9FD")).
				Bold(true)

	monitorStyle = lipgloss.NewStyle().
			Foreground(lipgloss.Color("#BD93F9"))

	spinnerChars = []string{"⠋", "⠙", "⠹", "⠸", "⠼", "⠴", "⠦", "⠧", "⠇", "⠏"}
)

const (
	maxInputHeight  = 8
	maxInputRunes   = 50000
	spinnerInterval = 80 * time.Millisecond
)

// InteractionState interaction state
type InteractionState int

const (
	StateIdle InteractionState = iota
	StateStreaming
	StateToolExec
	StateWaitingChoice
	StateWaitingConfirm
	StateViewingDoc
)

// ChoiceOption represents an option
type ChoiceOption struct {
	Label       string
	Value       string
	Description string
	// DescriptionZH 是 Description 的中文辅助层。
	//
	// 渲染为**缩进辅助行**（形态 SSOT = bilingual.go），不与 Description 同行并列。
	// 为空时渲染方什么都不加 ⇒ 既有菜单（未填本字段）外观完全不变。
	DescriptionZH string
}

// PendingChoice 等待用户选择的提问（:158）。
// T3 S2 新增字段（SNAPSHOT）：DefaultIdx + Category。
// - DefaultIdx：默认选项索引（0-based，-1 无默认），TUI 高亮显示让用户按 Enter 直接确认。
// - Category：提问分类（clarify/plan-confirm/cost-warning/scope-confirm/dialogue-clarify），
// 用于差异化渲染（编排场景显示"编排提问"标签，对话场景显示"对话澄清"标签）。
type PendingChoice struct {
	Prompt     string
	Options    []ChoiceOption
	ToolID     string
	DefaultIdx int    // T3 S2 新增（SNAPSHOT）
	Category   string // T3 S2 新增（SNAPSHOT）
}

// choiceResultMsg is the user choice result message
type choiceResultMsg struct {
	ToolID    string
	Answer    string
	Cancelled bool
	// FreeText 是用户在「补充自由输入」子态里打的内容（2026-09-21 · AskUser 对齐 同类 CLI）。
	// 仅 AskUser 分支（落底 session.ToolResult）消费；其余本地 ToolID 分支（权限/会话/安全套餐/编排桥）
	// 不读它，零值空串向后兼容。
	FreeText string
	// AnswerIsUserOwn 为真表示「这次回答不是任何预设项」—— 用户高亮客户端追加的 Other 哨兵
	// （`None of the above`）后自己写了一段答案（2026-09-23 ·）。
	//
	// ★ 它决定 FreeText 在回传串里挂**哪个前缀**：真 ⇒ `user_answer:`（那是**答案**），
	// 假 ⇒ `user_note: `（那是对所选选项的**补充说明**）。
	// 判据由 resolveAskUserAnswer 按**哨兵值**给出（不是按展示文本反推），见该函数注释。
	// 非 AskUser 分支不读它，零值 false 向后兼容。
	AnswerIsUserOwn bool
}

// askUserNotePrefix 是「补充 note」在回传串里的前缀，与 同类 CLI 逐字一致
// （对标证据件 的 format!("user_note: {notes}")）。
//
// 为什么必须有前缀：不带前缀时，「选了 JWT」和「补充说 Session 更合适」会变成模型眼里
// 一句自相矛盾的话，只能靠模型猜。前缀把「这是补充、不是又一个选择」写进数据里。
const askUserNotePrefix = "user_note: "

// askUserAnswerPrefix 是「用户自己写的答案」在回传串里的前缀（2026-09-23 ·）。
//
// ★ 为什么不能沿用 `user_note: `（本仓 只在**界面**做了分工， 把它补到**数据面**）：
// 界面早就把两个入口分开了 —— `render_extras.go:188-189` 的注释与 `:200` 的活线文案写的是
// 「按 i = 给所选方案补充一句说明 · **选 None of the above = 自己作答**」，
// 但两者回传时都挂 `user_note: ` ⇒ 界面承诺「这是你的答案」、数据面标注「这是一条注」。
//
// 后果有真机实证（会话存档 01a0c506 L26-L29）：用户选 Other 并输入「看到后回复hello」，
// 回传 `None of the above\nuser_note: 看到后回复hello`（**逐字节送达**，len=39），
// 模型却把那段读成「注」、不执行其中的指令，只就 `None of the above` 作答。
// ⇒ 这不是送达问题，是**语义位置**问题：同一事实跨入口不同源（违反）。
//
// ⇒ 让「答案」待在**答案**的位置：Other 自填走本前缀，`i` 的补充说明仍走 user_note。
// 前缀自解释，不依赖模型记住工具描述。
//
// ★ 这是**有意偏离**对标实现（上游在 Other 场景同样用 `user_note: `）：只保留它的
// 「哨兵 ＋ 第二段」形态，修正第二段的前缀语义。且上游自身并不一致 —— 其 test client 的
// Other 分支只回 `user_note: {answer}`、**不带**哨兵
// （app-server-test-client/src/request_user_input.rs:108,129），而其 tui 用例带哨兵（mod.rs:3175）。
// 理由与取证见 `I-4回传送达_Noneoftheabove_取证回执_2026-09-23.md` §8。
const askUserAnswerPrefix = "user_answer: "

// formatAskUserAnswer 拼装 AskUser 回传给模型的答案串 —— **单一实现**，唯一调用点是下方
// choiceResultMsg 的 AskUser 落底分支（别在别处再拼一份，见 双实现漂移坑）。
//
// 对齐 同类 CLI：选项与 note 是两个可分辨的条目，note 带 `user_note: ` 前缀占独立一行。
//
//	同类 CLI 原文（mod.rs:902-905）：
//
// let mut answer_list = selected_label.into_iter().collect::<Vec<_>>();
// if !notes.is_empty() { answer_list.push(format!("user_note: {notes}")); }
//
// 修前（2026-09-23 ·）：本处是 `Answer + "\n[补充] " + FreeText` —— 一句扁平串：
//
//	① 语义面：「JWT + 但 Session 更符合…」，模型只能自己猜哪个算数；
//	② 形态面：`[补充]` 是汉字，混进英文主行，违反「英文主行不得含汉字」（SSOT = bilingual.go）。
//
// ★ ：`isUserOwnAnswer` 决定第二段挂哪个前缀 ——
// 选 Other 自填 ⇒ `user_answer:`（**答案**）；选预设项 + `i` ⇒ `user_note: `（**注**）。
// 参数来自 resolveAskUserAnswer 按哨兵值给出的结构化事实，**不**在本函数里拿展示文本反推。
func formatAskUserAnswer(answer, note string, isUserOwnAnswer bool) string {
	switch {
	case note == "":
		return answer
	case answer == "":
		// answer 为空 ⇒ 没有可回的选项，答案就是用户自己写的那句话。
		//
		// ★ 之后**选 Other 走不到这里**：那条路径回传的是 otherOptionLabel
		//（"None of the above"），见 resolveAskUserAnswer。本分支现在只是本函数的边界契约
		//（索引越界 / 未来引入「纯开放式提问直填」时才会落上），不再是「选 Other」的落点。
		return note
	default:
		if isUserOwnAnswer {
			// 用户拒绝了所有预设项、自己写了一段答案 ⇒ 它占**答案**的位置。
			return answer + "\n" + askUserAnswerPrefix + note
		}
		return answer + "\n" + askUserNotePrefix + note
	}
}

// askUserAnswerSegment 是「当前高亮项 → 回传给模型的答案」的解析结果（2026-09-23 · · 单一实现）。
type askUserAnswerSegment struct {
	// Answer 是回传给模型的答案段（模型要的是 Value，不是给人看的 Label）。
	Answer string
	// IsUserOwnAnswer 为真 ⇔ 高亮在客户端追加的 Other 哨兵 ⇒ 用户**拒绝了所有预设项**、
	// 子态里那段文字是**用户自己的答案**（不是对某个选项的补充说明）。
	IsUserOwnAnswer bool
}

// resolveAskUserAnswer 从当前高亮项解析**回传给模型**的答案段（2026-09-23 · · 单一实现）。
//
// - 高亮在预设项 ⇒ Answer = 该项的 Value（模型要的是值，不是给人看的 Label）
// - 高亮在 Other ⇒ Answer = otherOptionLabel（"None of the above"）—— **不是空串**，
// 且 IsUserOwnAnswer = true
// - 索引越界 ⇒ Answer = 空串（调用方据此退化成「只回 note」）
//
// ★ 为什么 Other 必须回标签而不是空串：对齐 同类 CLI —— 用户选 Other 时 answers[0] 就是
// OTHER_OPTION_LABEL 本身（request_user_input/mod.rs:58 定义，:3175 用例为
// `answers = ["None of the above", "user_note: Custom answer"]`）。
// whale 修前返回空串，模型只拿到一句**无来源标注的裸文本**，无从判断「这次回答不是任何预设项」，
// 只好退回自己入参里的 default_idx ⇒ 答成选项 0
// （真机 2026-09-23 · 会话存档 01a0c506：回传 "回复hello"，模型却答"你刚才选择的是 JWT"）。
//
// ★ ：本函数同时给出 IsUserOwnAnswer —— 判据是**哨兵值** `Value == otherOptionValue`，
// 不是拿展示文本（label）反推。这是本仓的纪律：语义事实由**拥有该事实的那一层**输出，
// 下游不许用 `answer == "None of the above"` 之类的字符串比较去猜（「用展示文本编码语义」是复发坑）。
//
// ★ 这是「选项 → 回传答案」的唯一解析点：`i` 子态提交路径走它，
//
//	避免与回显各写一份取值逻辑（双实现坑）。
func resolveAskUserAnswer(options []ChoiceOption, idx int) askUserAnswerSegment {
	if idx < 0 || idx >= len(options) {
		return askUserAnswerSegment{}
	}
	if options[idx].Value == otherOptionValue {
		return askUserAnswerSegment{Answer: otherOptionLabel, IsUserOwnAnswer: true}
	}
	return askUserAnswerSegment{Answer: options[idx].Value}
}

// askUserEchoSep 是**回显**里连接「选项段」与「补充段」的分隔符 —— 只用于显示，不进数据面。
const askUserEchoSep = " · "

// formatAskUserEcho 生成**用户可见的单行回显**，与 formatAskUserAnswer 同源：
// 同一对 (answer, note)，显示面把数据面的换行折成 ` · `。
//
// ★ 为什么要有它（· (c)）：修前回显由局部变量就地拼（`freeText`，或 `label + " · " + freeText`），
// 与回传是**两条独立代码路径**，可以各自漂移；而用户只看得到其中一边，于是会出现
// 「用户以为模型收到的是 A，模型收到的其实是 B」而无人察觉。
// 现在两者都从 (answer, note) 派生 ⇒ 回显里出现的选择段必然等于回传里的 answer 段。
func formatAskUserEcho(answer, note string) string {
	switch {
	case answer == "" && note == "":
		return ""
	case note == "":
		return answer
	case answer == "":
		return note
	default:
		return answer + askUserEchoSep + note
	}
}

// confirmResultMsg tool execution confirmation result
type confirmResultMsg struct {
	approved bool
	decision ApprovalDecision

	// delta F：随消息携带**审计上下文**。
	//
	// 为什么必须随消息带、而不是在消费点回头读 PendingConfirm：
	// 三个拒绝出口在**发射之前**就已 SetPendingConfirm(nil)（见 StateWaitingConfirm 的
	// enter / esc / 快捷键分支），消费点再读只能读到 nil ⇒ 拒绝一条都留不了痕。
	// 而发射点里 pc 仍在作用域内，上下文是**免费**的。
	source    tools.ConfirmSource
	toolName  string
	toolInput string
	kind      ApprovalKind
}

// newConfirmResultCmd 构造产出 confirmResultMsg 的 tea.Cmd，并把审计上下文一并带走。
// source 固定为 SourceUser —— 这条路径上的每个出口都来自一次真实按键（用户亲自决策）。
func newConfirmResultCmd(approved bool, decision ApprovalDecision, pc *PendingConfirm) tea.Cmd {
	msg := confirmResultMsg{approved: approved, decision: decision, source: tools.SourceUser}
	if pc != nil {
		msg.toolName = pc.ToolName
		msg.toolInput = pc.ToolInput
		msg.kind = pc.Kind
	}
	return func() tea.Msg { return msg }
}

// pipelineConfirmMsg is the pipeline confirmation request message
// When Pipeline returns NeedConfirm=true, notify Update to enter confirmation state via this message
//
// 【不变量 · resume context 完整性】确认暂停是「挂起-恢复」语义：暂停时**已发生的一切
// 副作用，其产出必须随恢复上下文完整保存**。缺了任何一项，恢复后都无法重现
// ——因为被 return 掉的闭包栈帧（含局部 results）在返回瞬间即销毁，而恢复路径
// 只会从 currentIndex 重新开始执行，下标 < currentIndex 的工具**不会被重跑**。
//
// 2026-09-14 S10-M2 实测缺陷：本结构原先只有 pendingTools(输入)+currentIndex(进度)，
// 没有 priorResults(产出) ⇒ 中途确认时已执行工具的结果被永久丢弃，表现为
// 「N 个 tool_use 却只有 M<N 个 tool_result」，模型在残缺上下文上作答。
// ⚠️ 今后向本结构增加「执行期产出」类状态时，必须一并携带，并同步
// TestFix_MultiToolConfirm_KeepsEarlierToolResults。
type pipelineConfirmMsg struct {
	toolName     string
	toolInput    string
	reason       string
	pendingTools []session.ContentBlock // List of tools to continue executing after confirmation
	currentIndex int                    // Index of current tool needing confirmation in pendingTools
	requestID    string
	// priorResults 暂停点之前**已执行完成**工具的结果（下标 < currentIndex，按原顺序）。
	// 恢复执行时必须原样前置回 results 切片，否则这批副作用无出口。
	priorResults []tools.Result
}

// PendingConfirm pending safety confirmation for risky operations
type PendingConfirm struct {
	ToolName  string
	ToolInput string
	RiskLevel string
	ToolIndex int
	SelectIdx int

	// Pipeline confirmation fields: remaining tools and current index after confirmation
	PipelinePendingTools []session.ContentBlock
	PipelineCurrentIndex int
	PipelineRequestID    string
	// PipelinePriorResults 暂停点之前已执行工具的结果（下标 < PipelineCurrentIndex，按原顺序）。
	// 不变量见 pipelineConfirmMsg 注释：恢复时必须原样前置回 results，否则这批副作用无出口。
	PipelinePriorResults []tools.Result

	// P0-6/P0-7 新增字段（第2轮修复：审批层多类型枚举 + MCP elicitation）
	// Kind 审批类型（零值 ApprovalKindExec 走旧 Y/N 路径，向后兼容）
	Kind ApprovalKind
	// Options 动态选项列表（替代硬编码 2 选项，对标同类 CLI build_options）
	Options []ApprovalOption
	// MCP elicitation 专属字段（对标同类 CLI McpElicitation 变体）
	// McpServerName MCP server 名称
	McpServerName string
	// McpRequestID 原始 JSON 字节的请求 ID（string/number 双类型，对标同类 CLI RequestId enum）
	// 第3轮修复（致命漏洞 #2）：类型从 string 改为 json.RawMessage，原样透传到 ElicitationResponse.RequestID
	McpRequestID json.RawMessage
	// McpMessage elicitation 请求消息内容
	McpMessage string
	// AgentReplyCh 子代理工具确认的回传通道（G4，2026-09-08 冒烟缺口）。
	// 非 nil 表示本确认来自子代理（Agent 工具内部调用），决策后必须经此通道
	// 直接回传，不走 pipeline 恢复/直接执行路径（主流仍在 Agent 工具 goroutine 中）。
	// R3：由 chan bool 改为 chan tools.ConfirmOutcome ——
	// 决策必须类型化（含成因），否则"槽位占用""ctx 取消""超时"都会被下游
	// 写成同一句"用户拒绝"。
	AgentReplyCh chan tools.ConfirmOutcome

	// SandboxEscalateCh on-failure 沙箱升级的回传通道（，2026-10-14）。
	// 非 nil 表示本确认来自沙箱升级桥（sandboxEscalateBridge），决策后经此通道
	// 回传 bool 并回到 StateToolExec（主流仍在工具执行 goroutine 中等待）。
	// 升级决策**不写 policy**（逐条、不记忆， 拍板）。
	SandboxEscalateCh chan bool
}

// ApprovalKind 标识审批请求的类型，对标同类 CLI ApprovalRequest 枚举的 4 个变体
// （REF-外部-同类 CLI-3c7f013f9735）
type ApprovalKind int

const (
	// ApprovalKindExec 命令执行审批（bash/PowerShell 危险命令）
	ApprovalKindExec ApprovalKind = iota
	// ApprovalKindFileModify 文件修改审批（write_file/edit_file）
	ApprovalKindFileModify
	// ApprovalKindPipeline Pipeline 中间工具确认
	ApprovalKindPipeline
	// ApprovalKindMcpElicitation MCP elicitation 请求审批（对标同类 CLI McpElicitation）
	ApprovalKindMcpElicitation
)

// ApprovalOption 描述审批 UI 中的一个可选项，对标同类 CLI ApprovalOption
// （REF-外部-同类 CLI-3c7f013f9735）
type ApprovalOption struct {
	// Label 选项显示文本（英文主行）
	Label string
	// LabelZH 选项的中文辅行（：缩进另起一行；为空则只有英文主行）
	LabelZH string
	// Decision 该选项对应的决策结果
	Decision ApprovalDecision
	// Shortcut 可选快捷键（单字符，空字符串表示无快捷键）
	Shortcut string
}

// ApprovalDecision 表示用户在审批 UI 中做出的决策
// 对标同类 CLI McpServerElicitationAction（Accept/Decline/Cancel）
// （REF-外部-同类 CLI-3c7f013f9735）
type ApprovalDecision int

const (
	// DecisionApproveOnce 本次允许执行
	DecisionApproveOnce ApprovalDecision = iota
	// DecisionApproveSession 会话内允许（写入 Policy 缓存）
	DecisionApproveSession
	// DecisionDeny 拒绝执行
	DecisionDeny
	// DecisionCancel 取消请求（elicitation 专用，对标同类 CLI McpServerElicitationAction::Cancel）
	DecisionCancel
	// DecisionApproveSessionPrefix 「允许该命令前缀」（，2026-10-06）——
	// 仅对**命令载体**的单一简单命令成立：本会话内同头命令免问。
	//
	// ★ 刻意**追加在末尾**：前面的取值保持原 iota 编号不变（审计里 int(d) 只用于告警）。
	// ★ 它属**放行类**决策，但粒度与 DecisionApproveSession 不同 ——
	// 判定"是不是放行"请统一走 `isApproveDecision`（SSOT），不要手写 `==` 比较。
	DecisionApproveSessionPrefix
)

// isApproveDecision 报告某个面板决策是否属于「放行」类。
//
// ★ 这是「放行类决策」的**唯一判据**：修复前 app.go 与 agent_confirm.go 里
// 散落着 4 处 `d == DecisionApproveOnce || d == DecisionApproveSession`
// （app.go:1792 / 1825、agent_confirm.go:101 / 124）。新增一档放行决策时，
// 那 4 处**必须同改** —— 漏一处就会出现"某个入口认、另一个入口不认"的分叉
// （正是本仓 「双实现」类复发坑）。收成单一函数后，新增档位只改这里。
func isApproveDecision(d ApprovalDecision) bool {
	switch d {
	case DecisionApproveOnce, DecisionApproveSession, DecisionApproveSessionPrefix:
		return true
	default:
		return false
	}
}

// elicitationRequestMsg MCP server 发来的 elicitation/create 请求
// 由 Client.dispatchRequest 通过 ElicitationHandler 转发到 TUI，Update 接收后构造 PendingConfirm
// 第3轮修复（致命漏洞 #2）：requestID 类型为 json.RawMessage（对标同类 CLI RequestId enum 双类型）
type elicitationRequestMsg struct {
	serverName string
	requestID  json.RawMessage // 原始 JSON 字节的请求 ID（string/number 双类型）
	message    string
}

// elicitationResponseMsg 用户在 TUI 决策后回传的 elicitation 响应
// 由 resolveElicitation 返回的 tea.Cmd 发出，Update 接收后调用 ResolveElicitation
// 第2轮修复（致命漏洞 #3）：原方案仅声明 elicitationRequestMsg，遗漏此类型，导致响应被丢弃
type elicitationResponseMsg struct {
	response mcp.ElicitationResponse
}

// tickMsg 是带世代号的活动状态渲染 tick，旧世代到达时直接丢弃。
type tickMsg struct {
	at  time.Time
	seq uint64
}

// viewportAdjustMsg triggers viewport height recalculation in Update()
type viewportAdjustMsg struct {
	height   int
	atBottom bool
}

// streamEventMsg AI stream event
type streamEventMsg struct {
	event     ai.Event
	events    <-chan ai.Event
	requestID string
	// ── ：请求构造移出主循环后的副作用回传 ──────────────
	//
	// `startAIStream` 现在在返回的 Cmd 里构造请求（主循环之外），而 `buildRequest`
	// 会把 `requestSnapshot` 写在那个**值快照**上 —— 不随消息回传，主循环就永远
	// 读不到新快照（`/context`、compact 等会退化成旧值）。
	//
	// snapshotKnown 区分两种来源：
	// - true = 发送路径首条消息（stream.go startAIStream）⇒ 携带真实快照，需应用；
	// - false = 流内续事件（readNextEvent）⇒ 不携带，**不得**覆盖已有快照。
	requestSnapshot contextRequestSnapshot
	snapshotKnown   bool
}

// streamDoneMsg stream done
type streamDoneMsg struct {
	blocks    []session.ContentBlock
	usage     *session.TokenUsage
	model     string
	thinking  string
	requestID string
	// stopReceived：true=收到 MessageStopEvent（模型侧正常结束），
	// false=events 通道关闭但无 MessageStopEvent（流截断，如网络中断/SSE 未发 [DONE]）。
	// 空响应排障关键信号（2026-09-08 A2 事故观测缺口）。
	stopReceived bool
	// finishReason：OpenAI 兼容流的 finish_reason（stop/length/content_filter 等）。
	finishReason string
}

// toolResultMsg tool execution result
type toolResultMsg struct {
	results   []tools.Result
	requestID string
}

// loopFireMsg loop task trigger message, sent by LoopScheduler's onFire callback
type loopFireMsg struct {
	taskID string
	prompt string
}

// aiErrorMsg AI error
type aiErrorMsg struct {
	err       error
	requestID string
}

// upgradeCheckResultMsg P0-3（D5-2）修复：异步 /upgrade check 结果消息。
// 由 OnUpgradeCheckResult 回调通过 program.Send 发送到 Update 循环，
// 避免 /upgrade check 同步阻塞 TUI 主线程。
type upgradeCheckResultMsg struct {
	result string
}

// autoCompactResultMsg P0-1（D4-1）修复：异步自动压缩结果消息。
// 由 startAsyncCompact 返回的 tea.Cmd 在 goroutine 中执行压缩后发出，
// Update 循环接收后应用到 model，避免压缩阻塞 TUI 主循环。
type autoCompactResultMsg struct {
	result           *ctxpkg.CompactResult
	config           ctxpkg.CompactionConfig
	pipeline         *ctxpkg.CompactionPipeline
	nothingToCompact bool
	requestID        string
	sessionEpoch     uint64
	modelEpoch       uint64
	cacheEpoch       uint64
	compactUsage     ai.UsageEvent
	hasCompactUsage  bool
	expectedVersion  session.TranscriptVersion
	expectedRevision uint64
	err              error
}

// monitorOutputMsg is the monitor process output message
type monitorOutputMsg struct {
	monitorID string
	line      string
}

// monitorDoneMsg is the monitor process completion message
type monitorDoneMsg struct {
	monitorID string
	status    monitor.MonitorStatus
	exitCode  int
	output    string
}

// VimModeState vim mode state
type VimModeState int

const (
	VimNormal VimModeState = iota // Normal mode (navigation)
	VimInsert                     // Insert mode (input)
)

// UIModel groups viewport, textarea, and style-related fields
type UIModel struct {
	viewport           viewport.Model
	textarea           textarea.Model
	spinnerIdx         int
	width              int
	height             int
	ready              bool
	dirty              bool
	xOffset            int
	inputFocusedOnSend bool
	inputFocusedOnStop bool
	vimMode            bool
	vimModeState       VimModeState
	vimPendingKey      string
	keybindings        *keybindings.Manager
	mdRenderer         *MarkdownRenderer
	// （M10-05）：主题管理器，支持运行时热切换。
	// mdRenderer 字段保留向后兼容（指向 themeMgr.Renderer() 快照）。
	themeMgr          *themeManager
	imagePasteHandler *ImagePasteHandler
	pendingImages     []PendingImage
	docLinks          []FileLink
	docLinkIdx        int
	suggestionState   *SuggestionState // N5 typeahead：@ / # / slash 补全弹窗状态
}

// SessionModel groups session and state-related fields
type SessionModel struct {
	session             *session.Session
	config              *config.Config
	store               *session.Store
	sessionName         string
	workDir             string
	homeDir             string // 缓存的家目录路径，避免重复调用 os.UserHomeDir()
	onboardingCompleted bool   // P3：缓存 onboarding 完成状态，避免每次渲染都 os.Stat
	savedMsgCount       int
	firstUserInput      string
	state               InteractionState
	messages            *messageBuffer // N13 G-04：消息缓冲区（FIFO 裁剪 + onEvict 失效 RenderCache）
	quitting            bool
	modelName           string
	modelEpoch          uint64
	availableModels     []string
	inputTokens         int
	outputTokens        int
	requestCount        int // 当前会话已完成的 provider 请求数（chat + compact AI，/context Requests 行数据源）
	lastInputTokens     int
	lastOutputTokens    int
	requestUsage        ai.RequestUsageAccumulator
	requestEpoch        uint64
	usageRequestID      string
	sessionEpoch        uint64
	requestSnapshot     contextRequestSnapshot
	usageTracker        *ai.UsageTracker
	inputHistory        []string
	historyIdx          int
	historyDraft        string
	currentAgent        string
	activeSkills        []string
	compactCount        int
	autoCompactCooldown bool
	planMode            bool
	// thinkingEnabled 是**三态**：nil=未指定 · 指向 true=显式开启 · 指向 false=显式关闭。
	// 与 config.ThinkingEnabled / ai.ThinkingConfig.Enabled 同类型 ⇒ 设置到请求零转换。
	thinkingEnabled      *bool
	thinkingBudgetTokens int
	cacheBroken          bool
	cacheBreakCount      int
	// C20: 事件总线与生命周期处理器，用于 Session ↔ Context 解耦。
	// Store 发布 SessionForkEvent/SessionSwitchEvent；LifecycleHandler
	// 订阅并清空 Context 缓存 + 从新 session 重载。
	eventBus         *bus.Bus
	lifecycleHandler *ctxpkg.SessionLifecycleHandler
	privacyConfig    *PrivacyConfig
	fastModel        string
	outputStyle      string
	marks            []sessionMark
	configLoader     *config.Loader
	currentText      string
	currentThinking  string
	interrupted      bool

	// P0-8/9 新增：排队输入队列（FIFO）。AI 流式输出 / 工具执行期间用户的输入入队，
	// 由 handleStreamDone 末尾通过 drainQueuedInputMsg 异步触发 drain。
	queuedInputs    []QueuedUserMessage
	queuedInputsMax int
}

// AIModel groups AI client, model, and compaction-related fields
type AIModel struct {
	aiClient      *ai.SimpleClient
	compactor     *ctxpkg.Compactor
	aiCompactor   *ctxpkg.AICompactor
	estimator     *ai.TokenEstimator
	budget        ctxpkg.Budget
	fallback      *ai.FallbackTracker
	modelRegistry *ai.ModelRegistry
	magicDocs     *config.MagicDocs
	memStore      *config.MemoryStore
	memIndex      *config.MemoryIndex
	memSelector   *config.MemorySelector
	memExtractor  *config.AutoMemoryExtractor
	streamCancel  context.CancelFunc
	requestID     string
	// RecoveryCoordinator coordinates retry/degrade/compress to prevent cascading failures
	recoveryCoordinator *ai.RecoveryCoordinator
	// retryConfig holds retry policy for StreamWithRetry
	retryConfig ai.RetryConfig
	// N11 修复 N1：持久 CompactionPipeline，断路器状态跨调用保留（同 runnerCore.Pipeline）。
	pipeline *ctxpkg.CompactionPipeline
	// N11 修复 N2：CachingIntegration 引用，供 handleCompact 获取 cacheInfo 传入 pipeline。
	caching *ai.CachingIntegration

	// V3-6-2/3 AI 安全检测器（循环/拒绝）。
	// loopDetector 在工具调用时检测重复参数循环和无进展。
	// refusalHandler 检测 AI 拒绝执行并提供强制执行机制。
	// 两个检测器均为 nil 安全（测试或未初始化时静默跳过），不阻塞主流程。
	// （原第三个 hallucinationDetector 已随 整体移除，2026-10-08。）
	loopDetector   *ai.LoopDetector
	refusalHandler *ai.RefusalHandler
	// R4（S17 P3 / V3-6-3）：本轮观察到的**结构化**安全拒绝
	// （tools.FailureKindSandboxDenied / FailureKindPolicyDenied），零值 = 本轮无拒绝事件。
	//
	// 它是「执行被安全策略拦截」这一断言的**唯一权威判据**；散文关键词
	// （ai.MentionsSecurityConstraint）降级为**辅助**合取项，不再单独决定分支。
	// 写入点：toolResultMsg 漏斗（app.go，单一写入点，同时覆盖 AskUser 主拦截路径）。
	// 清理点：resetAIDetectors（新用户回合开始），故语义是"本用户回合内"。
	//
	// 值字段：Update 返回的 Model 会被 Bubble Tea 存为下一轮 receiver，故跨 Update 携带成立
	// （与 confirmCount 的既有模式一致）。详见 domains/TUI按值复制陷阱.md。
	roundDeniedKind tools.FailureKind
}

// ToolModel groups tool registry, executor, and pipeline-related fields
type daemonController interface {
	IsRunning() bool
	Start(context.Context) error
	Stop(context.Context) error
	Restart(context.Context) error
	Status() (daemon.DaemonStatus, error)
	AddTask(string, string, daemon.TaskType, daemon.TaskPriority) (*daemon.Task, error)
	ListTasks() ([]daemon.Task, error)
	CancelTask(string) error
	Config() daemon.DaemonConfig
}

type ToolModel struct {
	registry       *tools.Registry
	executor       *tools.Executor
	cmdRegistry    *command.Registry
	policy         *permission.Policy
	pendingTools   []session.ContentBlock
	toolInputBuf   map[string]string
	currentToolID  string
	pendingChoice  *PendingChoice
	pendingConfirm *PendingConfirm
	// pendingClearConfirm 是 /clear 的二次确认态（C-4，2026-10-10）。
	// 缺陷：Usage 写 /clear --confirm 但实现不校验，敲下立即清空（不可逆）。
	// 现语义：无参数 /clear 置位本标记并提示；下一个 KeyMsg 为 y 才清空，其他键取消。
	// 带 --confirm 不置位、立即清。值字段：经 Update 值接收者返回流转（与 state 同型）。
	pendingClearConfirm bool
	// 「等，但出声」字段（2026-10-10 拍板）：等待用户批准期间出声 ——
	// 审批等待开始/持续写日志 + 面板计时。**不做**超时自决（等人是设计本意）。
	// 值字段：由 tick 驱动、经 Update 值接收者返回流转；全零值 = 不在等待期。
	approvalWaitStart   time.Time
	approvalWaitFor     *PendingConfirm // 本段等待归属的 confirm（指针同一性：区分「同一段等待」与「新一轮」）
	approvalWaitSecs    int             // 面板显示的已等秒数
	approvalWaitLastLog int             // 上次周期日志的已等秒数（间隔 approvalWaitLogIntervalSec）
	approvalWaitTool    string          // 本段等待的工具名（结束日志用）
	choiceIdx           int
	toolCancel          context.CancelFunc
	monitorMgr          *monitor.Manager
	monitorCh           chan monitorEvent
	// monitorEscape 是 monitorCh 阻塞发送的逃生口（A-5，见 monitorEscape 文档）。
	// 指针语义：所有 Model 副本共享同一个逃生口，退出时关一次即全局解开。
	monitorEscape *monitorEscape
	loopScheduler *tools.LoopScheduler
	loopCh        chan loopFireMsg
	daemon        *daemon.Daemon
	daemonControl daemonController
	githubTools   *github.GitHubTools
	searcher      *search.Searcher

	// approvalAudit 是审批决策留痕通道（delta F，2026-09-14）。
	//
	// ★ 必须是指针语义（接口内含指针 ⇒ Model 值复制后底层实例共享）。
	// **绝不能**换成值字段：Model 是按值复制的（bubbletea Update 每次返回新值），
	// 值字段会被写进"调用它的那份副本"，而真正被驱动的那份永远是零值 ⇒
	// 静默失效、完全不可见（与 programRef 的 P0 事故同形）。
	approvalAudit bridge.ApprovalAudit
}

// Model main TUI model
type Model struct {
	UIModel
	SessionModel
	AIModel
	ToolModel

	// Service interfaces for gradual migration from embedded structs.
	// New code should prefer using these interfaces over direct field access.
	// Once all methods are migrated, the embedded structs will be removed.
	uiService      UIService
	sessionService SessionService
	aiService      AIService
	toolService    ToolService

	// calibrationSource 是 provider usage 回流的 EMA 校准源（T1-B）。
	// SimpleClient.Stream 解析到 UsageEvent 时调用 RecordRemoteUsage 持续纠偏
	// 本地估算；estimateCurrentInputContextTokens 通过 ApplyCalibration 将
	// 偏移应用到完整口径估算，使状态栏/预警/自动压缩判定收敛到真实 usage。
	// nil 表示未注入（测试或纯本地模式），估算退化为裸启发式值。
	calibrationSource ai.TokenSource

	// N13 L0 引擎抽象层（02 §2.2.2 契约唯一主设计方）。
	// virtualScroll 消息级虚拟滚动（G-01）；renderCache 渲染缓存（G-02/G-03）；
	// textMeasurer 文本测量（G-05，替代 cellbuf.Wrap 的 rune 数切片）。
	// 接口类型内部含指针，Model 值复制时底层实例共享，无指针失效风险。
	virtualScroll VirtualScroll
	renderCache   RenderCache
	textMeasurer  TextMeasurer

	// program 承载 bubbletea Program 引用（共享持有者，见 programRef 文档）。
	// Model 按值复制（NewModel 按值返回、tea.NewProgram 内部再拷、Update 返回新值），
	// 若把 *tea.Program 直接存成值字段，SetProgram 只会写中其中一份副本，
	// 而 Program 运行期真正驱动的那份副本永远为 nil → 所有 program.Send 通道静默降级。
	// 因此经 programRef 间接持有：指针在副本间共享，任意副本都能看到已注入的 Program。
	// 在 NewModel 中创建一次（不变量）；nil 仅见于零值构造的测试 Model。
	program *programRef

	// N24 通知系统（反转裁剪→实施）。
	// notifier 管理通知队列（4 级优先级 + fold 合并 + invalidates 失效 + history 环形缓冲）。
	// toast 是 bubbletea toast 组件（顶部叠加 + tea.Tick 世代号 timeout）。
	notifier *notify.Notifier
	toast    *ToastModel

	// lastInputAreaTop 记录输入区顶部 Y 坐标（鼠标坐标空间），由 recalcViewportHeight
	// 在每次 Update 时测量并持久化（指针接收者写入随 Update 返回的 m 生效）。
	// 修复：替代原 app.go Y 路由处的硬编码公式
	// `Height - Textarea.Height - vFrame - 3`，该公式未计入 preview/popup/btnRow 行高，
	// 导致 preview/popup 存在时 inputAreaTop 偏大，输入区滚动被误判为对话区滚动。
	// 正确公式：toastHeight + Height - iaHeight - 2（iaHeight 由 renderInputArea 实测）。
	lastInputAreaTop int

	// lastPreviewHeight 记录排队输入预览（RenderPendingInputPreview）渲染高度，
	// 用于鼠标点击 Y 坐标路由时计算 Send/Stop 按钮行位置（btnRowY）。
	// 5 维度审查修复：原 btnRowY 公式 `inputAreaTop + Textarea.Height + vFrame`
	// 未计入 previewHeight，导致 preview 存在时按钮行点击落点偏移（差 previewHeight 行）。
	lastPreviewHeight int

	// lastContextState 记录上一轮 checkAutoCompact 观察到的上下文状态，
	// 用于检测状态"向上跨越"（Ok→Warning / Warning→ShouldCompact），
	// 每个阈值只在跨越时提示一次，避免每轮对话重复刷屏。
	// 压缩成功或开新会话后 token 回落，状态自然回到 Ok，跨越逻辑自动复位。
	lastContextState ctxpkg.State

	// ★ C（2026-10-13，手测卡② §19.5 选项 C）：后台完成事实的「待推给模型」队列。
	//
	// 为什么是队列、而不是"完成时直接写模型上下文"：进程完成的那一刻，**本轮的请求
	// 早已发出**（模型看不到这次请求之后追加的任何内容），所以事实只能在**下一次
	// 构建请求**时注入 —— buildMessages 是模型上下文的唯一构建入口
	// （生产调用点只有 stream.go:443）。队列由 injectMonitorFacts 消费并清空 ⇒ 恰好一次。
	//
	// 有界：封顶 maxPendingMonitorFacts；溢出计入 monitorFactsDropped（丢弃必须可见，
	// 与 A-5 的 dropped* 一族同口径），注入时以 "(+K earlier completions suppressed)"
	// 如实带出，不做静默丢弃。
	//
	// 注入的是**事实**（id / 终态 / 退码）而非**内容**：被禁的是把输出正文塞回对话流
	// （第二份无界副本，§19.3），不是"让模型知道完成了"。
	pendingMonitorFacts []string
	monitorFactsDropped int

	// N26: turnEndHooks turn 结束钩子列表（goal.Manager 通过 RegisterTurnEndHook 注册，）。
	// 每轮对话结束时（snapshot 之后）按注册顺序调用。
	// 钩子错误仅记录日志，不阻塞主流程（goal 续跑失败不阻塞用户输入）。
	turnEndHooks []TurnEndHook

	// N26: goalManager 长期目标管理器（，/goal 命令接线层）。
	// 在 NewModel 中通过 newGoalManager 构造，注册为 TurnEndHook 实现续跑评估。
	goalManager *goal.Manager

	// H10 Phase 6: orchBridge 编排器与 TUI 桥接器（S4 接线，）。
	// 由 main.go 在 NewModel 后通过 SetOrchBridge 注入；nil 表示未启用编排器。
	// Init() 启动 waitForOrchAskRequest/waitForOrchContractRequest 监听 channel。
	orchBridge *OrchBridge

	// H10 Phase 6: pendingOrchAskReq 当前等待用户回答的编排器提问请求（S4 接线）。
	// handleOrchAskRequest 设置，choiceResultMsg 处理分支清空并回传响应。
	pendingOrchAskReq *orchestrator.AskRequest

	// H10 Phase 6: pendingOrchContractReq 当前等待用户确认的编排器方案请求（S4 接线）。
	// handleOrchContractRequest 设置，choiceResultMsg 处理分支清空并回传响应。
	pendingOrchContractReq *orchestrator.ContractRequest

	// T3 S3 新增（SNAPSHOT）：日常对话 AskUser 频率控制 + 缓存。
	// askCount 当前用户消息的提问计数（每条消息重置，超 ask_threshold 时拦截）。
	// askCache 提问缓存（5 分钟 TTL 防同问题重复问）。
	// askSettings 提问配置（从 ~/.whale/settings.json 读取，nil 时降级默认值 1）。
	askCount    int
	askCache    *AskCache
	askSettings *AskSettings

	// choiceFreeTextMode 标记 StateWaitingChoice 内是否处于「补充自由输入」子态。
	// 用户按 i 或选中 Other（None of the above）后进入；enter 提交或 esc 取消后复位。
	// 复用主 textarea，不新建顶级状态（对齐 同类 CLI「typing jumps into notes」语义）。
	choiceFreeTextMode bool

	// 输入合并器（REF-方案-TUI输入与工具调用渲染修复 §4.1）：
	// 缓冲快速 KeyRunes（非 bracketed paste 终端的粘贴拆分），批量刷新避免逐字渲染。
	// 正常打字（单字符、间隔>24ms）不缓冲无延迟；粘贴批量出现。
	inputCoalesce inputCoalesceState

	// 输入框转义序列碎片闸门（，2026-10-07）：
	// 鼠标上报被 tty 拆成两次 read 时，bubbletea 会把 `ESC [` 解成 Alt+`[`、其余字节解成普通
	// KeyRunes ⇒ 终端协议碎片变成用户文字。本闸门是「输入框只接受文本」的唯一承载体。
	// 全文与代价说明见 composer_input_guard.go。
	composerInput composerInputGuard

	// clipboardNextSeq/clipboardApplySeq/clipboardPending 保证连续 Ctrl+V 的异步结果按触发顺序应用。
	// 外部剪贴板进程完成顺序不可预测，不能让后触发的结果先插入输入框。
	clipboardNextSeq  uint64
	clipboardApplySeq uint64
	clipboardPending  map[uint64]clipboardPasteMsg

	// ②：「系统剪贴板当前有图片」的探针状态。由 FocusMsg 探针置位（main.go
	// 开 WithReportFocus 后终端回聚焦即触发），贴图成功/明确读到无图时复位。
	// terminalFocused 跟踪终端窗口焦点（初始假定已聚焦——刚启动时用户就在终端里）。
	clipboardHasImage bool
	terminalFocused   bool

	// H11N27+H12 接线：wiringCancel 取消 wiring goroutine（ForgettingScanner 等）的 ctx CancelFunc。
	// wiringWait 在取消后等待 H11N27 扫描协程退出，避免退出阶段仍访问持久化目录。
	// 由 NewModel 创建，main.go 退出时通过 tuiShutdown.RegisterCleanup 联动调用。
	// SNAPSHOT(): Model 新增 wiringCancel 字段（context.CancelFunc，供退出时取消 wiring goroutine）
	wiringCancel context.CancelFunc
	wiringWait   func()

	// H12 接线预留字段（T2-A ExperienceOrchestrator 实例化后填充，当前默认 nil 走降级）。
	// experienceOrch 非 nil 时 wireH12Evolution 才会构造 EvolutionLoop 并注入。
	// 注：当前 T2-A setupOrchestrator 仅创建 Phase 1 Orchestrator（orchestrate_command.go），
	// 未创建 Phase 4 ExperienceOrchestrator（orchestrator.go），故这些字段默认 nil，
	// wireH12Evolution 调用被 m.experienceOrch != nil 守卫跳过（H12 降级模式）。
	experienceOrch *orchestrator.ExperienceOrchestrator
	expStore       *orchestrator.ExperienceStore
	qualityStore   *orchestrator.QualityStore
	l1Scorer       *orchestrator.L1ScorerExt

	// D4 修复：saveSession 异步化后的并发保护锁。
	// 指针字段（*sync.Mutex）确保 Model 值复制时仍指向同一锁实例，跨 Update 共享。
	// NewModel 中初始化为 &sync.Mutex{}，保护 saveSession 内 SavedMsgCount 读-改-写区间。
	saveMu *sync.Mutex

	// TUI inline scrollback 批次2：流式 active_cell + frame_limiter 集成。
	// activeCell 是流式活动内容真相源，
	// 用于记录待渲染的新内容 revision；frameLimiter 8.33ms 帧率限制器（对标同类 CLI MIN_FRAME_INTERVAL）。
	// 两者均在 NewModel 初始化，nil 守卫覆盖所有使用点（未初始化时退化为原行为）。
	// REF-外部: 同类 CLI frame_rate_limiter / chatwidget active_cell
	activeCell    *ActiveCell
	frameLimiter  *FrameLimiter
	lastSpinnerAt time.Time
	// waitingSince 记录本轮模型请求的发出时刻（现象②，2026-10-04）。
	// 用途：在「既无正文、也无思考内容」的等待窗口里渲染**已等待多久**，
	// 让用户能区分「请求已发出、在等网关回首 token」与「压根没发出去/卡死」——
	// 旧文案只说 "AI is thinking..."，两者在视觉上完全一样（spinner 会动但不进时间）。
	// 零值 = 未打点 ⇒ 退化为不带计时的等待行（见 stream.go waitingIndicator）。
	waitingSince time.Time
	tickSeq      uint64
	typeaheadSeq uint64

	// 流式渲染缓存（2026-08-01 输入响应优化）：消息数量不变时复用缓存，
	// 避免 renderStreamContent 每次 tick 全量调用 renderMessages()（O(n)→O(1)）。
	// WindowSizeMsg 时失效（宽度变化影响渲染）。
	// REF-外部: 同类 CLI code Ink throttle(leading:true) 只更新变化部分
	// REF-外部: 同类 CLI active_cell 可变尾部 历史消息不变
	streamCachedHistory  string
	streamCachedMsgCount int
	// streamCachedHistorySrcs 是 streamCachedHistory 的逐行 source map（批 C）：
	// 流式期间历史部分同样要能正确还原省略行，故与文本一起缓存、一起失效。
	streamCachedHistorySrcs []lineSource

	// guard P0 agent 防护（agent 防护体系 2026-08-31）：轮次上限/无进展熔断/
	// 审查子循环上限/进度检测。NewModel 初始化；sendInput 时 BeginRequest 归零；
	// startAIStream 顶部统一闸门判定。nil 时所有防线降级为放行（防御性）。
	guard *agentguard.Guard

	// confirmCount 安全确认循环断路器计数器（defense-in-depth，根因修复后正常路径不触发）。
	// key: requestID + ":" + toolIndex，value: 连续确认次数。超过 1 次拒绝执行。
	confirmCount map[string]int

	// reqApprovals 记录**本次用户请求内**已被用户明确批准、且逐字相同（同 Name + 同 Input）
	// 的调用，用于「同请求内相同调用复用刚批准的决定」，避免同一个副作用被问两次。
	// key = approvalReuseKey(name, input)；value = 批准发生时的 RequestID。
	//
	// ★ 为什么作用域边界编码在 map 的 **value** 里，而不是另开一个 `reqApprovalsID string`：
	// Model 在 bubbletea 下**按值复制**（见本文件 :636 的警告：值字段会被写进"调用它的
	// 那份副本"，静默失效且完全不可见）。map 是引用类型，对**内容**的修改（m[k]=v / delete）
	// 跨副本共享；而**值字段**不共享。⇒ 边界只能存在内容里。
	//
	// 生命周期：RequestID 由 handleSubmit 在**用户提交**时生成（stream.go:218/311），
	// 在此后整个 agent loop（多轮工具往返）内保持不变 —— 这正是 approval_audit.go:71 写进
	// 账本的 request_id 跨轮相同的原因（实测 seq5/seq6 同为 1789871930921108000）。
	// **过期条目天然失效**（reqID 不匹配即不复用），无需依赖显式清空正确。
	// 必须在 NewModel 里 make 一次（此后各副本共享同一 map）。
	reqApprovals map[string]string

	// pendingCommandCmd 保存斜杠命令同步处理阶段产生的异步 Bubble Tea 命令。
	// sendInput 会在 handleCommand 返回后取出并交给 Bubble Tea 调度，避免命令处理器自行启动
	// goroutine 后修改已经被值复制淘汰的 Model 副本。
	pendingCommandCmd tea.Cmd

	// mouseEnabled is the current terminal mouse-report state. On macOS it is
	// temporarily enabled while a response is running so Stop remains clickable.
	mouseEnabled bool
	// mouseIdlePreference is the user's preferred idle state (native selection
	// when false); it must not be confused with temporary active capture.
	mouseIdlePreference   bool
	mouseAutoDuringActive bool
	// mouseEpoch invalidates terminal mode commands superseded by a newer turn.
	mouseEpoch *atomic.Uint64

	// viewportContent 镜像最近一次传给 setViewportContent 的完整内容字符串，
	// 供鼠标拖选在全量内容上工作（包括已滚出屏幕的历史行），见 mouse_selection.go。
	viewportContent string

	// 以下三者与 viewportContent **同一次快照**产出（refreshTranscriptViewport），
	// 共同构成拖选复制的 source map（见 transcript_source.go）：
	// - viewportSources[k]：第 k 条**视觉行**的真值来源（省略行 → 完整原文）；
	// - viewportOwner[k] ：第 k 条视觉行所属的**逻辑行**号（硬折行的 N 条视觉行同号）；
	// - viewportLogical ：逻辑行文本（折行前），复制按它取数、只取一次。
	viewportSources []lineSource
	viewportOwner   []int
	viewportLogical []string

	// unreadBelow 统计"用户不在底部时到达、因而落在屏幕下方看不见"的消息条数。
	//
	// 修的是判定层错位：`addMessage` 原实现对「用户**主动触发**的回执」和「**被动到达**的
	// 流式输出」共用同一条 followTail 策略 —— 非底部时**保留原滚动位置且零提示**，
	// 于是敲了命令界面毫无反应（实测该帧仅发出 1255 字节 = 输入框被清空，输出行命中 0）。
	//
	// 语义（与 followTail 配套，不取代它）：
	// - 仍然**不回跳**（回跳会打断正在读历史的人，是倒退）；
	// - 但对**任意**新消息，只要落在屏幕下方，就在状态栏点亮角标 —— 这是唯一可见信号。
	// 清零条件只有一个（见 Update 顶部）：视口回到**底部**即视为已读。
	unreadBelow int

	// turn 是「本轮对话」的**结构承载体**（A-1/A-3，2026-09-30）。
	//
	// 起因（真机手测 I-7.1）：后台长跑进程（Monitor）的输出被逐行 addMessage
	// 直推进主对话流 ⇒ 1 行/秒刷屏、Turn complete 之后仍在写、用户无法停止。
	// 根因不在"输出没有界"（Monitor 本来就有有界权威副本：DefaultMaxLines=10000 +
	// GetOutput/GetLines/List/Get），而在**显示层绕过了权威副本另建了一份无界副本**，
	// 且那份副本（对话消息）**不带作者、不带回合** ⇒ 进程时钟（连续）直接覆盖了
	// 对话时钟（回合制）。对标 同类 CLI / 同类 CLI：两类共同的结构不变量是
	// **主对话流的写入权只在 turn 内**（同类 CLI：输出只经工具结果进；同类 CLI：输出只落盘）。
	//
	// 因此这里把「回合」变成可判别的事实，而不是流里的一句装饰文案。
	turn turnBoundary

	// monitorStats 是异步通道的丢弃/边界计数（A-1/A-5）。
	//
	// ★ 必须是指针（与 programRef 同形，不是风格选择）：见 monitorStats 类型的文档 ——
	// 递增发生在 NewModel 注册的**回调闭包**里，而闭包捕获的是 NewModel 的局部 Model，
	// 按值返回 ⇒ 值字段的递增写进死副本，界面永远读到 0。
	monitorStats *monitorStats

	// mouseSelection stores an application-owned drag selection for the
	// conversation viewport. 在对话区直接按住左键拖动即开始选择（无需 Shift），
	// 拖到上/下边缘自动滚动视口，可选中超出一屏的内容；松开自动复制。
	// Bubble Tea captures mouse reports in alt-screen mode, so relying on the
	// terminal's native selection is not consistent across terminals.
	mouseSelection mouseSelectionState

	// composerSelection 输入框（composer）文字选区（composer_selection.go）。
	// bubbles textarea v0.20.0 不支持选区，本字段叠加应用层选区状态：
	// Shift+方向键扩展、Backspace/Delete 删除、输入替换、Ctrl+C 复制、Esc 清除。
	// 值类型随 Model 复制；内容被历史导航等外部替换时经 composerSelectionRange
	// 越界检测自动失效，不会作用于错误文本。
	composerSelection composerSelectionState

	// composerMouseDrag 输入框鼠标拖选状态机（composer_mouse.go）：
	// 按住左键拖动扩展选区（锚点在按下处、另一端随光标），松开保留选区，
	// 复用 composerSelection 状态与渲染/复制/删除路径。
	composerMouseDrag composerMouseDragState

	// composerNavMem 纵向移动的列记忆：光标因占位符吸附偏离原生列后，
	// 记录「用户真正想要的列」，连续 ↑/↓ 期间恢复该列（吸附后 SetCursor 会清掉
	// bubbles 的 lastCharOffset 列记忆，必须自记）。cursorRow/Col 是吸附后光标的
	// 落点 —— 下一次纵向键到来时光标仍在该处才认为记忆有效（期间任何其他移动/
	// 编辑都会使记忆自然失效，无需逐路径清理）。
	composerNavMem composerNavMemState

	// pressedBtn 记录「鼠标按住中」的输入区按钮（档B）：
	// ""（无）/ "send" / "stop" / "copy"（取值 = 下面的按钮身份常量）。
	//
	// 语义（GUI 标准按钮）：按下（mousedown）即登记并以 active 样式高亮、**不开火**；
	// 松开（mouseup）才开火并清零。旧实现「按下即开火」对 send 而言按下态永远
	// 渲染不到（同一帧 state 翻成 Streaming、按钮换成 Stop）⇒「点下去有反馈」
	// 在 send 上结构性地不可见；改成松开开火后三个按钮都有按下反馈。
	//
	// 自愈：终端可能吞掉 release（指针移出终端窗口外松开）⇒ KeyMsg /
	// WindowSizeMsg 清零（见 Update 顶部），否则按钮会卡在高亮态。
	pressedBtn string
}

// 输入区按钮身份：pressedBtn 的取值域，也是「当前渲染的按钮与按下态
// 是否同一颗」的比对键。渲染面（render_extras.go）与事件面（Update 的
// press/release 分支）共用这组常量，避免两边各写字符串字面量而漂移。
const (
	btnIdentitySend = "send"
	btnIdentityStop = "stop"
	btnIdentityCopy = "copy"
)

// SetMouseEnabled records the initial mouse-report mode selected by main.
func (m *Model) SetMouseEnabled(enabled bool) {
	m.SetMouseMode(enabled, false)
}

// SetMouseMode records the initial terminal mode and whether macOS should
// temporarily capture mouse reports while an AI turn is active.
func (m *Model) SetMouseMode(enabled, autoDuringActive bool) {
	if m.mouseEpoch == nil {
		m.mouseEpoch = &atomic.Uint64{}
	}
	m.mouseEpoch.Add(1)
	m.mouseEnabled = enabled
	m.mouseIdlePreference = enabled
	m.mouseAutoDuringActive = autoDuringActive
}

func (m *Model) mouseTransition(msg tea.Msg) tea.Cmd {
	if m.mouseEpoch == nil {
		m.mouseEpoch = &atomic.Uint64{}
	}
	epoch := m.mouseEpoch.Add(1)
	epochRef := m.mouseEpoch
	return func() tea.Msg {
		if epochRef.Load() != epoch {
			return nil
		}
		return msg
	}
}

func (m *Model) enableActiveMouse() tea.Cmd {
	if !m.mouseAutoDuringActive || m.mouseEnabled {
		return nil
	}
	m.mouseEnabled = true
	return m.mouseTransition(tea.EnableMouseCellMotion())
}

func (m *Model) restoreIdleMouse() tea.Cmd {
	if !m.mouseAutoDuringActive || m.mouseIdlePreference || !m.mouseEnabled {
		return nil
	}
	m.mouseEnabled = false
	return m.mouseTransition(tea.DisableMouse())
}

// thinkingRecord captures a thinking process
type thinkingRecord struct {
	Timestamp time.Time
	Prompt    string
	Content   string
	Length    int
}

// doctorResultMsg D4 修复：/doctor 异步诊断完成后回传渲染结果。
// OnDoctor 回调同步返回"⏳ 诊断中..."占位，goroutine 内执行 doc.Run + doc.Render，
// 完成后通过 m.program.Send(doctorResultMsg{...}) 回传到 Update。
// Update 收到后通过 addMessage 显示结果，用户无需等待 30s 超时。
type doctorResultMsg struct {
	rendered string
}

// clipboardWriteResultMsg 将异步剪贴板写入失败回传到 TUI 主事件循环。
type clipboardWriteResultMsg struct {
	err error
}

// sessionMark represents a session mark
type sessionMark struct {
	Label     string
	MsgIndex  int
	CreatedAt time.Time
}

// PrivacyConfig holds privacy settings
type PrivacyConfig struct {
	AnalyticsEnabled bool `json:"analytics_enabled"`
	ErrorReporting   bool `json:"error_reporting"`
	DataSharing      bool `json:"data_sharing"`
}

// monitorEvent is a monitor event (passed to TUI via channel)
type monitorEvent struct {
	eventType string // "output" or "done"
	monitorID string
	line      string
	status    monitor.MonitorStatus
	exitCode  int
	output    string
}

// Init 初始化 TUI 的光标、后台事件和通知监听命令。
func (m Model) Init() tea.Cmd {
	cmds := []tea.Cmd{textarea.Blink, waitForMonitorEvent(m.toolService.MonitorCh()), waitForLoopFire(m.toolService.LoopCh())}
	// N24：toast 组件初始化（启动 pushCh 监听）
	if m.toast != nil {
		cmds = append(cmds, m.toast.Init())
	}
	// H10 Phase 6: 启动编排器桥接 channel 监听（S4 接线，）。
	// orchBridge 非 nil 时监听 askCh/contractCh，收到请求后进入 StateWaitingChoice。
	if m.orchBridge != nil {
		if askCmd := waitForOrchAskRequest(m.orchBridge.askCh); askCmd != nil {
			cmds = append(cmds, askCmd)
		}
		if contractCmd := waitForOrchContractRequest(m.orchBridge.contractCh); contractCmd != nil {
			cmds = append(cmds, contractCmd)
		}
	}
	return tea.Batch(cmds...)
}

func composerAtVisualStart(input *textarea.Model) bool {
	lineInfo := input.LineInfo()
	return input.Line() == 0 && lineInfo.RowOffset == 0
}

func composerAtVisualEnd(input *textarea.Model) bool {
	lineInfo := input.LineInfo()
	lastVisualRow := lineInfo.Height - 1
	if lastVisualRow < 0 {
		lastVisualRow = 0
	}
	return input.Line() == input.LineCount()-1 && lineInfo.RowOffset >= lastVisualRow
}

// navigateInputHistory handles Up/Down only at the visual boundaries of the
// composer. Inside multi-line or soft-wrapped input, the key is left for the
// textarea so the cursor and its internal viewport can move normally.
func (m *Model) navigateInputHistory(direction int) bool {
	input := m.uiService.Textarea()
	if !input.Focused() || len(m.sessionService.InputHistory()) == 0 {
		return false
	}
	if direction < 0 {
		if !composerAtVisualStart(input) || m.sessionService.HistoryIdx() <= 0 {
			return false
		}
		if m.sessionService.HistoryIdx() == len(m.sessionService.InputHistory()) {
			m.sessionService.SetHistoryDraft(input.Value())
		}
		m.sessionService.SetHistoryIdx(m.sessionService.HistoryIdx() - 1)
		input.SetValue(m.sessionService.InputHistory()[m.sessionService.HistoryIdx()])
	} else {
		if !composerAtVisualEnd(input) || m.sessionService.HistoryIdx() >= len(m.sessionService.InputHistory()) {
			return false
		}
		m.sessionService.SetHistoryIdx(m.sessionService.HistoryIdx() + 1)
		if m.sessionService.HistoryIdx() == len(m.sessionService.InputHistory()) {
			input.SetValue(m.sessionService.HistoryDraft())
		} else {
			input.SetValue(m.sessionService.InputHistory()[m.sessionService.HistoryIdx()])
		}
	}
	// 历史导航整体替换 textarea 内容，选区锚点随之失效，显式清除选区。
	m.clearComposerSelection()
	m.adjustInputHeightAfterCoalesce()
	return true
}

// scrollComposer moves the cursor by the same three-row increment used by the
// conversation viewport. textarea keeps the cursor visible, so this also
// scrolls its private viewport without exposing or duplicating its wrap logic.
func (m *Model) scrollComposer(direction int) tea.Cmd {
	input := m.uiService.Textarea()
	var cmds []tea.Cmd
	if !input.Focused() {
		m.uiService.SetInputFocusedOnSend(false)
		m.uiService.SetInputFocusedOnStop(false)
		cmds = append(cmds, input.Focus())
	}

	keyType := tea.KeyDown
	if direction < 0 {
		keyType = tea.KeyUp
	}
	for range 3 {
		var cmd tea.Cmd
		*input, cmd = input.Update(tea.KeyMsg{Type: keyType})
		cmds = append(cmds, cmd)
	}
	return tea.Batch(cmds...)
}

// waitForMonitorEvent waits for monitor events (Bubble Tea Cmd)
func waitForMonitorEvent(ch chan monitorEvent) tea.Cmd {
	return func() tea.Msg {
		ev, ok := <-ch
		if !ok {
			return nil
		}
		return ev
	}
}

// waitForLoopFire wait for loop task trigger event (Bubble Tea Cmd)
func waitForLoopFire(ch chan loopFireMsg) tea.Cmd {
	return func() tea.Msg {
		msg, ok := <-ch
		if !ok {
			return nil
		}
		return msg
	}
}

// SendPrompt send a prompt directly (for -p mode)
func (m *Model) SendPrompt(prompt string) {
	m.reassignServices() // 修复服务指针别名：指向当前实例自身子结构体
	m.uiService.Textarea().SetValue(prompt)
}

// recalcViewportHeight 计算 viewport 高度（v1.1 优化：高频路径消除渲染测高）。
// 对标同类 CLI Renderable::desired_height(width)（宽度感知计算，非渲染测高）。
// 高频状态（Idle/Streaming/ToolExec）用常量估算 inputArea 高度，消除 4 次完整渲染测高；
// 低频状态（WaitingChoice/WaitingConfirm/ViewingDoc）保留渲染测高（结构复杂且低频）。
// REF-外部: 同类 CLI desired_height
func (m *Model) recalcViewportHeight() {
	// The footer is multi-line on normal and narrow terminals; reserve its
	// actual rendered height so it remains fixed below the input area.
	sbHeight := 0
	if status := m.renderStatusBar(); status != "" {
		sbHeight = lipgloss.Height(status)
	}

	// inputArea 高度：高频 default 状态用常量估算，低频状态保留渲染测高
	var iaHeight int
	switch {
	case m.sessionService.State() == StateWaitingChoice && m.toolService.PendingChoice() != nil:
		// 低频状态：choice UI 结构复杂（box+选项+help），保留渲染测高
		iaHeight = lipgloss.Height(m.renderChoiceUI())
	case m.sessionService.State() == StateWaitingConfirm && m.toolService.PendingConfirm() != nil:
		// 低频状态：confirm UI 结构复杂，保留渲染测高
		iaHeight = lipgloss.Height(m.renderConfirmUI())
	case m.sessionService.State() == StateViewingDoc:
		// 低频状态：docNav UI 结构复杂，保留渲染测高
		iaHeight = lipgloss.Height(m.renderDocNavUI())
	default:
		// 高频状态（Idle/Streaming/ToolExec）：textarea 路径用常量估算（消除渲染测高）
		iaHeight = m.estimateInputAreaHeight()
	}

	// toast 高度：保留渲染测高（低频，toast 内容可能多行换行）
	toastHeight := 0
	if m.toast != nil {
		if tv := m.toast.View(); tv != "" {
			toastHeight = lipgloss.Height(tv)
		}
	}

	// N-MOUSE-ROW：Monitor 摘要区也是占屏区块，必须进高度预算。
	// 漏它一行 ⇒ View() 比终端多一行 ⇒ bubbletea 砍掉渲染缓冲顶部一行
	// ⇒ 鼠标拖选整体位移一行（用户报障「复制到上面的部分」）。
	// 预算与渲染同源：区块表见 layout.go buildView，新增区块必须两处同登记。
	monitorHeight := 0
	if strip := m.renderMonitorStrip(); strip != "" {
		monitorHeight = lipgloss.Height(strip)
	}

	// Reserve exactly the rendered footer/input/toast/monitor heights. View()'s
	// section joins do not produce additional terminal rows in the viewport
	// renderer, so subtracting a synthetic two-row gap leaves blank rows below
	// the footer and shifts mouse hit-testing upward.
	vpHeight := m.uiService.Height() - sbHeight - iaHeight - toastHeight - monitorHeight
	if vpHeight < 3 {
		vpHeight = 3
	}
	atBottom := m.uiService.Viewport().AtBottom()
	m.uiService.Viewport().Height = vpHeight
	if atBottom {
		m.uiService.Viewport().GotoBottom()
	}

	// 输入区顶部 Y 坐标（鼠标坐标空间），供 MouseMsg Y 路由使用。
	// v1.1 子审查须落实1修复：vpHeight 已减 toastHeight，inputArea 绝对位置不含 toastHeight
	// （推导：inputAreaTop = toastHeight + sbHeight + vpHeight = Height-sbHeight-iaHeight）
	m.lastInputAreaTop = m.uiService.Height() - sbHeight - iaHeight

	// v1.1 致命3修复：补设 lastPreviewHeight（鼠标 btnRowY 路由依赖，app.go:1794）
	// 用估算值（与 estimateInputAreaHeight 同源），保证鼠标路由与高度计算一致
	m.lastPreviewHeight = m.estimatePreviewLines()
}

// estimateInputAreaHeight 估算 default 状态 inputArea 高度（不渲染 UI，O(1) 常量计算）。
// 组成：textarea.Height + inputBoxStyle frame(2) + previewLines + btnRow(1) + popupLines。
// 估算误差：长消息换行会导致 preview 实际行数 > 估算值，但 viewport 滚动兼容 ±N 行误差。
// render_extras.go:16-67 renderInputArea（preview+textarea+btnRow+popup 拼接）
func (m *Model) estimateInputAreaHeight() int {
	taHeight := m.uiService.Textarea().Height()
	frameHeight := inputBoxStyle.GetVerticalFrameSize() // = 2（border only，无垂直 padding）
	// 排队输入预览：N 条消息 + section header(1) + Alt+Up hint(1) = N+2（空时0）
	previewLines := m.estimatePreviewLines()
	// Send/Stop uses a rounded border, so it occupies three terminal rows.
	// Measuring the actual compact button keeps mouse Y routing aligned with
	// the rendered label and both border rows.
	btnRow := lipgloss.Height(renderInputActionRow(m.uiService.Width()-4,
		m.sessionService.State() != StateIdle,
		m.uiService.InputFocusedOnStop() || m.uiService.InputFocusedOnSend(), m.pressedBtn))
	// typeahead 弹窗：min(N,7) 候选 + 滚动指示(N>7时+1) + popupBorderStyle 边框(2)
	popupLines := 0
	_, popupFits := completionPopupContentWidth(m.uiService.Width() - 4)
	if m.suggestionState != nil && m.suggestionState.Visible && popupFits {
		n := len(m.suggestionState.Completions)
		// v1.1 子审查建议1：renderCompletionPopup 在 Completions 为空时返回 ""（0行），此处同步
		if n == 0 {
			popupLines = 0
		} else {
			maxVisible := maxVisibleSuggestions // = 7（completion_popup.go:92）
			if n > maxVisible {
				popupLines = maxVisible + 1 + 2 // 封顶7 + 滚动指示1 + 边框2
			} else {
				popupLines = n + 2 // N + 边框2
			}
		}
	}
	return taHeight + frameHeight + previewLines + btnRow + popupLines
}

// estimatePreviewLines 估算排队输入预览行数（不渲染 UI，用 wrapTextSimple 估算每条消息换行行数）。
// 组成：空时0；非空时 section header(1) + Σ min(wrappedLines,3)+(超限+1) + Alt+Up hint(1)。
// v1.1 子审查须落实2修复：原 N+2 估算偏低（长消息换行实际行数 > N），改用 wrapTextSimple 精确估算。
// render_pending_preview.go:68-79 RenderPendingInputPreview + renderTruncatedMessage
func (m *Model) estimatePreviewLines() int {
	queued := m.QueuedInputsSnapshot()
	if len(queued) == 0 {
		return 0
	}
	width := m.uiService.Width() - 4 // RenderPendingInputPreview 的 width 参数
	if width < 20 {
		return 1 // renderNarrowPreview 返回1行
	}
	lines := 1             // section header
	wrapWidth := width - 4 // renderTruncatedMessage 用 width-4 做 wrap
	if wrapWidth <= 0 {
		wrapWidth = 1
	}
	for _, msg := range queued {
		wrapped := wrapTextSimple(msg.Text, wrapWidth)
		n := len(strings.Split(wrapped, "\n"))
		if n > pendingPreviewLineLimit {
			lines += pendingPreviewLineLimit + 1 // 3行内容 + 1行省略号
		} else {
			lines += n
		}
	}
	lines += 1 // Alt+Up hint
	return lines
}

// routeKeyToComposerTextarea 把消息送入输入框 textarea，并完成合并器 / 高度 / typeahead 后处理。
//
// ★ 这是「按键 → textarea」的**唯一实现**（收口，2026-09-22）。两个调用点共用：
// - Update 尾部（idle / streaming 等常规路径）
// - StateWaitingChoice 的「补充自由输入」子态（用户按 i 或选中 Other 之后）
//
// 分成两处各写一份 → 改了一处、另一处静默失效（记录过的双实现漂移形态），
// 而「子态下按键要进输入框」正是这类漂移的实际受害者。
//
// 语义与原先内联在 Update 尾部的代码逐条对齐：
// - 焦点在 Send/Stop 按钮，或 vim normal 态 ⇒ 不转发；
// - KeyRunes 突发（粘贴拆分）⇒ 走合并器缓冲，**跳过**高度重算与 typeahead debounce（由 flush 统一触发）；
// - MouseMsg ⇒ 不转发（：textarea 的 Update 不含 MouseMsg 分支，转发会导致意外滚动）；
// - 其余消息（如 flush tick）⇒ 只转发，不做高度重算。
func (m *Model) routeKeyToComposerTextarea(msg tea.Msg) tea.Cmd {
	if m.uiService.InputFocusedOnSend() {
		return nil
	}
	switch msg := msg.(type) {
	case tea.KeyMsg:
		if m.uiService.VimMode() && m.uiService.VimModeState() == VimNormal {
			m.composerInput.reset()
			return nil
		}
		// ★ 闸门（唯一实现，见 composer_input_guard.go）：
		// 「输入框只接受文本」——被拆散的 ESC 序列头与其载荷在这里被拦下，不进编辑器。
		// 必须放在 maybeCoalesceInput 之前：否则碎片会先被合并器缓冲、再由 flush 写进 textarea。
		if msg.Type == tea.KeyRunes {
			runes, ok := m.composerInput.review(msg)
			if !ok {
				return nil
			}
			if len(runes) == 0 {
				return nil
			}
			msg.Runes = runes
		} else {
			// 非文本键（Enter/方向键/Esc…）说明用户已经改按别的东西 ⇒ 结束重同步窗口，
			// 否则闸门会留下一个「接下来一直吞字」的状态。
			m.composerInput.reset()
		}
		if msg.Type == tea.KeyRunes && !msg.Paste && m.maybeCoalesceInput(msg.Runes) {
			// 输入合并器：快速 KeyRunes 突发（粘贴）→ 缓冲批量刷新，避免逐字渲染（REF-方案 §4.1）。
			// 仅在无待触发 flush 时调度（防重复调度）；突发期间跳过 typeahead debounce 与高度重算。
			if m.inputCoalesce.flushPending {
				return nil
			}
			m.inputCoalesce.flushPending = true
			return m.coalesceFlushCmd()
		}
		var taCmd tea.Cmd
		*m.uiService.Textarea(), taCmd = m.uiService.Textarea().Update(msg)
		// ⑥⑦：编辑后立刻把 [Image #N] 占位符与待发图队列对齐（删占位符 ⇒ 丢该图；编号重排）。
		// 必须排在高度重算**之前** —— 重排可能改变文本长度，高度依赖它。
		m.reconcilePendingImages()
		newHeight := m.desiredInputHeight()
		if m.uiService.Textarea().Height() != newHeight {
			m.uiService.Textarea().SetHeight(newHeight)
		}
		// 仅 @/#/slash 补全上下文触发 debounce，普通输入和后台消息不再创建 timer。
		if m.shouldDebounceTypeahead(string(msg.Runes)) {
			taCmd = tea.Batch(taCmd, m.debounceTypeaheadCmd())
		}
		m.recalcViewportHeight()
		return taCmd
	case tea.MouseMsg:
		// textarea v0.20.0 的 Update 不含 case tea.MouseMsg（仅处理 KeyMsg/paste），
		// 转发 MouseMsg 会落到 textarea 内部 viewport.Update 导致意外滚动。
		// 所有鼠标事件统一由下方 Y 坐标路由处理，不送 textarea。
		return nil
	default:
		var taCmd tea.Cmd
		*m.uiService.Textarea(), taCmd = m.uiService.Textarea().Update(msg)
		return taCmd
	}
}

// Update 处理 TUI 主事件循环中的状态、输入、流式输出和后台消息。
func (m Model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	// 主循环耗时总闸：Update 的**每一条** return 路径都被它覆盖
	// （用 defer 而非逐处打点 —— 本函数有几十个提前 return，逐处补必然漏）。
	// 代价：快路径只多一次 time.Since；只有慢路径才格式化 msg 类型。
	perfStart := time.Now()
	defer func() {
		if d := time.Since(perfStart); perfProbeThreshold <= 0 || d >= perfProbeThreshold {
			perfEmitter("update", d, fmt.Sprintf("msg=%T", msg))
		}
	}()
	m.reassignServices() // 修复服务指针别名：指向当前实例自身子结构体
	// ⑥⑦ 兜底：改动输入框文本但**不走按键路径**的入口（输入合并器 flush、rewind、
	// 未来新增的置文本点）统一在这里对齐。键盘编辑已在 routeKeyToComposerTextarea 里即时处理，
	// 故这里的迟到一轮只影响非交互写入，用户无感。无待发图时该调用零成本直接返回。
	m.reconcilePendingImages()
	// 档B：按下态自愈。终端可能吞掉 release（指针移出终端窗口外松开），
	// 没有这个兜底按钮会卡在高亮态直到下一次点击。键盘活动与 resize 都取消
	// 视觉按下态（两者都意味着「这次点击已经过去了」）。
	if m.pressedBtn != "" {
		switch msg.(type) {
		case tea.KeyMsg, tea.WindowSizeMsg:
			m.pressedBtn = ""
		}
	}
	// 未读角标的**唯一**清零点。视口只要在底部（无论是用户按 End/GotoBottom、
	// 滚轮滚回底部、还是内容变短自然到底），未读即视为已读。
	// 放在 Update 顶部而非各按键分支：滚动入口有 PgUp/PgDn/Ctrl+↑↓/Home/End/滚轮/拖选
	// 多处（且多数分支 `return m, nil` 提前返回），逐处补清零必然会漏掉新入口。
	if m.unreadBelow > 0 && m.uiService.Viewport().AtBottom() {
		m.unreadBelow = 0
	}
	// C-4：/clear 二次确认态拦截 —— 必须在一切 KeyMsg 处理之前，
	// 否则 y/其他键会落进输入框或触发别的分支。非 KeyMsg（tick/mouse 等）照常放行，
	// 用户确认前仍可滚动查看即将清掉的内容。
	// ★ 逻辑必须内联在 Update 自己的副本上（R1b-2）：若抽成值接收者方法再返回，
	// 其 sessionService 仍指向本层副本的内嵌 SessionModel，消息写入与本层返回值
	// 错位 —— 实测 flag 被重置而消息原地不动。
	if m.pendingClearConfirm {
		if key, ok := msg.(tea.KeyMsg); ok {
			m.pendingClearConfirm = false
			if key.Type == tea.KeyRunes && len(key.Runes) == 1 && (key.Runes[0] == 'y' || key.Runes[0] == 'Y') {
				m.performClear()
			} else {
				m.addMessage(bi("Cancelled — conversation unchanged", "已取消，对话保持不变"))
			}
			return m, nil
		}
	}
	var cmds []tea.Cmd

	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		viewport := m.uiService.Viewport()
		wasAtBottom := viewport.AtBottom()
		previousMaxOffset := viewport.TotalLineCount() - viewport.Height
		if previousMaxOffset < 0 {
			previousMaxOffset = 0
		}
		bottomGap := previousMaxOffset - viewport.YOffset
		if bottomGap < 0 {
			bottomGap = 0
		}
		// v1.2：完全同步 resize（去掉 75ms debounce），拖拽过程中实时更新。
		// 超越同类 CLI：同类 CLI 常规 resize 用 75ms trailing debounce，
		// 仅流合并后用 schedule_immediate（transcript_reflow.rs:91-94）；whale-code 完全同步。
		// 单次重渲染 2-5ms < 16ms（60fps），完全同步可行。
		// REF-外部: 同类 CLI schedule_debounced/schedule_immediate

		// 极窄屏兜底（子审查建议2）：msg.Width < 4 时 msg.Width-4 为负数
		if msg.Width < 4 {
			return m, nil
		}

		// 立即更新宽度/高度（轻量操作）
		m.uiService.SetWidth(msg.Width)
		m.uiService.SetHeight(msg.Height)
		m.uiService.Viewport().Width = msg.Width - 4
		m.uiService.Textarea().SetWidth(msg.Width - 4 - inputBoxStyle.GetHorizontalFrameSize())
		if m.toast != nil {
			m.toast.SetWidth(msg.Width)
		}

		// 须落实4：首次 resize（Ready=false）立即渲染，避免启动空屏
		if !m.uiService.Ready() {
			m.uiService.SetReady(true)
			m.recalcViewportHeight()
			if m.renderCache != nil {
				m.renderCache.InvalidateAll()
			}
			if m.virtualScroll != nil {
				m.virtualScroll.SetViewport(m.uiService.Viewport().Height)
				m.virtualScroll.InvalidateAll()
			}
			m.refreshTranscriptViewport()
			m.uiService.Viewport().GotoBottom()
			return m, nil
		}

		// 后续 resize 完全同步（无 debounce）
		m.recalcViewportHeight()
		if m.renderCache != nil {
			m.renderCache.InvalidateAll()
		}
		if m.virtualScroll != nil {
			m.virtualScroll.SetViewport(m.uiService.Viewport().Height)
			m.virtualScroll.InvalidateAll()
		}

		// 须落实1：按 State 分流渲染（避免流式闪烁）
		// 流式/工具执行中：调用 renderStreamContent（保留流式尾部），否则流式尾部会消失一帧
		// 须落实2：虚拟滚动路径失效 streamCachedHistory（依赖 Height/YOffset，不能复用）
		state := m.sessionService.State()
		if state == StateStreaming || state == StateToolExec {
			// 流式状态：失效缓存 + renderStreamContent（内部会重建缓存）
			m.invalidateStreamHistoryCache()
			m.renderStreamContent()
		} else {
			// 非流式状态（Idle/WaitingChoice/WaitingConfirm/ViewingDoc）：虚拟滚动路径失效缓存，短会话路径保留缓存
			if len(m.sessionService.Messages()) > streamRenderThreshold {
				m.invalidateStreamHistoryCache()
			}
			m.refreshTranscriptViewport()
		}
		if wasAtBottom {
			m.uiService.Viewport().GotoBottom()
		} else {
			newMaxOffset := m.uiService.Viewport().TotalLineCount() - m.uiService.Viewport().Height
			if newMaxOffset < 0 {
				newMaxOffset = 0
			}
			m.uiService.Viewport().SetYOffset(newMaxOffset - bottomGap)
		}
		return m, nil

	case viewportAdjustMsg:
		if m.uiService.Viewport().Height != msg.height {
			m.uiService.Viewport().Height = msg.height
			if msg.atBottom {
				m.uiService.Viewport().GotoBottom()
			}
		}

	case typeaheadTickMsg:
		// 修复：50ms debounce timer 触发后检测 @ token
		if msg.seq != m.typeaheadSeq {
			return m, nil
		}
		m.updateTypeaheadSuggestions()

	case tickMsg:
		if msg.seq != m.tickSeq {
			return m, nil
		}
		state := m.sessionService.State()
		// 审批等待期也续订 tick —— 此前 tick 只在 Streaming/ToolExec
		// 续订，弹确认框后 tick 停摆（真机实测 151.9s 日志零行 + 无计时）。等待期改为
		// 由 tick 驱动：开始/周期性写日志 + 面板计时；仍不做超时自决。
		waitingConfirm := m.toolService.PendingConfirm()
		if state == StateWaitingConfirm && waitingConfirm != nil {
			return m, m.approvalWaitTick(msg.at, msg.seq, waitingConfirm)
		}
		// 等待结束检测（收口在 tick 而非各决策出口）：state 已离开等待态、或 confirm
		// 已换人 ⇒ 把上一段等待落一条结束日志并复位。
		if !m.approvalWaitStart.IsZero() {
			m.approvalWaitEnd(msg.at)
		}
		if state != StateStreaming && state != StateToolExec {
			return m, nil
		}
		now := msg.at
		if m.lastSpinnerAt.IsZero() || now.Sub(m.lastSpinnerAt) >= spinnerInterval {
			m.uiService.SetSpinnerIdx((m.uiService.SpinnerIdx() + 1) % len(spinnerChars))
			m.lastSpinnerAt = now
			m.uiService.SetDirty(true)
		}
		rendered := false
		if m.uiService.Dirty() {
			// frame_limiter 节流（2026-08-01 批次2集成）：距上次渲染 < 8.33ms 则跳过，
			// Dirty 保持 true，下次 tick 再渲染（不丢 delta，activeCell.text/CurrentText 持续累积）。
			// nil 守卫：frameLimiter 未初始化时退化为原行为（始终渲染）。
			// REF-外部: 同类 CLI FrameRateLimiter clamp_deadline
			if m.frameLimiter == nil || m.frameLimiter.ShouldRender() {
				m.renderStreamContent()
				if m.frameLimiter != nil {
					m.frameLimiter.MarkEmitted()
				}
				m.uiService.SetDirty(false)
				rendered = true
			}
		}
		// T1-P1-B7 ：tick 时触发异步预渲染 buffer 范围内的消息，
		// 不阻塞 Update 循环；tea.Cmd 由 Bubbletea 调度执行，完成后产生 chunkRenderedMsg。
		if rendered && m.virtualScroll != nil {
			if cmd := m.virtualScroll.PrefetchBuffer(m.uiService.Viewport().YOffset); cmd != nil {
				cmds = append(cmds, cmd)
			}
		}
		if m.sessionService.State() == StateStreaming || m.sessionService.State() == StateToolExec {
			cmds = append(cmds, tickCmd(msg.seq))
		}
		return m, tea.Batch(cmds...)

	// T1-P1-B7 ：异步渲染完成消息处理。
	// chunkRenderedMsg 由 PrefetchBuffer 返回的 tea.Cmd 产生，
	// 此处回写 chunkedVirtualScroll.msgLines 并标记 dirty 触发 View 刷新。
	case chunkRenderedMsg:
		if cvs, ok := m.virtualScroll.(*chunkedVirtualScroll); ok && cvs != nil {
			cvs.ApplyChunkRendered(msg)
			m.uiService.SetDirty(true)
		}

	// D4 修复：/doctor 异步诊断结果回传。
	// OnDoctor goroutine 完成后通过 m.program.Send 发送，此处接收并显示。
	case doctorResultMsg:
		m.addMessage(msg.rendered)

	// N24：toast 消息委托（toastPushMsg / toastTimeoutMsg / 窗口大小同步）
	case toastPushMsg:
		if m.toast != nil {
			tm, cmd := m.toast.Update(msg)
			if t, ok := tm.(*ToastModel); ok {
				m.toast = t
			}
			cmds = append(cmds, cmd)
			m.recalcViewportHeight()
		}
	case toastTimeoutMsg:
		if m.toast != nil {
			tm, cmd := m.toast.Update(msg)
			if t, ok := tm.(*ToastModel); ok {
				m.toast = t
			}
			cmds = append(cmds, cmd)
			m.recalcViewportHeight()
		}

	case clipboardPasteMsg:
		return m, m.handleClipboardPasteResult(msg)

	// ②：终端窗口回聚焦（⌘Tab 切回、截图后点回终端）⇒ 探测剪贴板是否躺了图片，
	// 有则提示「按 Ctrl+V」。对齐 同类 CLI（终端聚焦 + 剪贴板有图才提示按键）。
	// 无定时轮询：进程外读剪贴板只在焦点变化时发生，零常驻开销。
	case tea.FocusMsg:
		m.terminalFocused = true
		return m, clipboardImageProbeCmd()

	case tea.BlurMsg:
		m.terminalFocused = false
		return m, nil

	case clipboardImageProbeMsg:
		m.clipboardHasImage = msg.hasImage
		return m, nil

	case clipboardWriteResultMsg:
		if msg.err != nil {
			m.addMessage(errorStyle.Render(fmt.Sprintf("Clipboard write failed: %v", msg.err)))
		}
		return m, nil

	case tea.KeyMsg:
		// Immediate 通知显式抢占 Esc；普通 toast 不抢键盘，避免影响中断和导航语义。
		if msg.String() == "esc" && m.toast != nil && m.toast.visible {
			if current := m.toast.notifier.Current(); current != nil && current.Priority == notify.PriorityImmediate {
				tm, cmd := m.toast.Update(msg)
				if toast, ok := tm.(*ToastModel); ok {
					m.toast = toast
				}
				m.recalcViewportHeight()
				return m, cmd
			}
		}
		// 非 bracketed 多行粘贴中的 Enter 必须先转换为换行；若先同步 flush，
		// active 状态会被清空，Enter 将误触发送或确认。
		if msg.Type == tea.KeyEnter && m.canAcceptComposerText() && m.shouldCoalesceEnter() {
			return m, m.bufferCoalescedNewline()
		}
		// 输入合并器：突发期间按下非文本键（Enter/Esc/方向键/ctrl 等）时，
		// 先同步刷新缓冲，确保该键看到完整输入（REF-方案 §4.1 运行时冲突-合并器 flush）。
		// KeyRunes（含 Paste）不触发同步刷新——它们走合并器缓冲或 paste 专用路径。
		if m.inputCoalesce.active && len(m.inputCoalesce.buf) > 0 && !(msg.Type == tea.KeyRunes && !msg.Paste) {
			m.flushCoalescedInputSync()
		}
		if msg.Paste {
			// bracketed paste 已携带终端实际粘贴内容，禁止重新读取系统剪贴板。
			if m.canAcceptComposerText() {
				if truncated := m.insertComposerText(string(msg.Runes)); truncated {
					m.showInputLimitWarning()
				}
				if m.shouldDebounceTypeahead(string(msg.Runes)) {
					return m, m.debounceTypeaheadCmd()
				}
				return m, nil
			}
			return m, nil
		}

		if isImagePasteKey(msg) && m.canAcceptComposerText() {
			m.clipboardNextSeq++
			return m, readClipboardPasteCmd(m.uiService.ImagePasteHandler() != nil, m.clipboardNextSeq)
		}

		if m.sessionService.State() == StateWaitingConfirm {
			// 2026-10-06（P1）：确认面板期间放行**视口导航键**（pgup/pgdown/ctrl+↑↓/home/end）。
			//
			// 修前本块以裸 `return m, nil` 收尾 ⇒ 这些键在面板期间被静默丢弃，
			// 而滚轮走 MouseMsg 不受影响 ⇒ 「滚得上去、键盘回不来」。用户现象：
			// 批准后新输出落在屏幕外，界面停在更早一屏，且无从回到最新。
			//
			// ★ 放在块入口，而不是三个 handler 内部：三种确认框（主会话 / 子代理 /
			// elicitation）共用一处，用户不区分"这是哪种确认框"，行为必须一致。
			// ★ 且必须写在 Update **自己的接收者**上 —— handleElicitationKeyMsg 是值接收者
			// 并返回自身副本，视口经 uiService 指针写进的是**另一个副本**的存储，
			// 下一次 Update 顶部的 reassignServices() 会把它复位（该形态已由
			// panel_scroll_keys_test.go 的 elicitation 格实测抓到，见 R1b"绑到死副本"）。
			//
			// 只放行**视图键**：面板对决策键仍是模态（up/down 选选项、y/n/a 决策），
			// 字符键也不放行 —— 面板期间输入框不渲染，让字符落进 textarea 会变成
			// "看不见的输入"。
			if m.viewportNavKey(msg.String()) {
				return m, nil
			}
			// G4：子代理确认走专用分支——决策直接回传 AgentReplyCh，
			// 不做 resetStreamState/StateIdle（主流仍在 Agent 工具 goroutine 中）。
			if pc := m.toolService.PendingConfirm(); pc != nil && pc.AgentReplyCh != nil {
				return m.handleAgentConfirmKeyMsg(msg, pc)
			}
			// 沙箱升级面板走专用分支——决策回传 SandboxEscalateCh，
			// 不写 policy（逐条、不记忆），回 StateToolExec（同 agent confirm 理由）。
			if pc := m.toolService.PendingConfirm(); pc != nil && pc.SandboxEscalateCh != nil {
				return m.handleSandboxEscalateKeyMsg(msg, pc)
			}
			// P0-7：elicitation 类型走专用按键处理（Esc 硬编码 Cancel + N 选项导航）
			if pc := m.toolService.PendingConfirm(); pc != nil && pc.Kind == ApprovalKindMcpElicitation {
				return m.handleElicitationKeyMsg(msg)
			}
			pc := m.toolService.PendingConfirm()
			options := []ApprovalOption(nil)
			if pc != nil {
				options = pc.Options
				if len(options) == 0 {
					options = approvalOptionsFor(pc.Kind, pc.ToolName, pc.ToolInput)
				}
			}
			switch msg.String() {
			case "up":
				if pc != nil && pc.SelectIdx > 0 {
					pc.SelectIdx--
				}
				return m, nil
			case "down":
				if pc != nil && pc.SelectIdx < len(options)-1 {
					pc.SelectIdx++
				}
				return m, nil
			case "enter":
				if pc != nil && pc.SelectIdx >= 0 && pc.SelectIdx < len(options) &&
					isApproveDecision(options[pc.SelectIdx].Decision) {
					m.sessionService.SetState(StateToolExec)
					// 修复：不清空 PendingConfirm，由 confirmResultMsg 处理时根据 PipelinePendingTools
					// 分发到 executePipelineRemaining（内部清空）或 executeToolsDirect（末尾清空）
					m.recalcViewportHeight()
					decision := options[pc.SelectIdx].Decision
					return m, newConfirmResultCmd(true, decision, pc)
				}
				m.addMessage(errorStyle.Render("Denied; operation was not run / 已拒绝，操作未执行"))
				m.sessionService.SetState(StateIdle)
				m.toolService.SetPendingConfirm(nil)
				m.resetStreamState()
				m.recalcViewportHeight()
				return m, newConfirmResultCmd(false, DecisionDeny, pc)
			case "esc", "ctrl+c":
				// delta F（Esc 语义修正，2026-09-14）：Esc = interrupt（取消），**不是**拒绝。
				// 对齐 R3/D1 在子 agent 路径已确立的语义（agent_confirm.go：ESC 是 interrupt，
				// 不是"拒绝了这次工具调用"），也与 elicitation 的 DecisionCancel 一致。
				// 修复前这里发 DecisionDeny ⇒ **同一个物理 Esc 键**在 exec 与 elicitation
				// 两条路径上落出两种语义，审计无论采信哪一边都会失真。
				m.addMessage(errorStyle.Render("Cancelled; operation was not run / 已取消，操作未执行"))
				m.sessionService.SetState(StateIdle)
				m.toolService.SetPendingConfirm(nil)
				m.resetStreamState()
				m.recalcViewportHeight()
				return m, newConfirmResultCmd(false, DecisionCancel, pc)
			}
			if pc != nil {
				for i, option := range options {
					if option.Shortcut == "" || !strings.EqualFold(msg.String(), option.Shortcut) {
						continue
					}
					pc.SelectIdx = i
					if isApproveDecision(option.Decision) {
						m.sessionService.SetState(StateToolExec)
						decision := option.Decision
						return m, newConfirmResultCmd(true, decision, pc)
					}
					m.addMessage(errorStyle.Render("Denied; operation was not run / 已拒绝，操作未执行"))
					m.sessionService.SetState(StateIdle)
					m.toolService.SetPendingConfirm(nil)
					m.resetStreamState()
					m.recalcViewportHeight()
					return m, newConfirmResultCmd(false, DecisionDeny, pc)
				}
			}
			return m, nil
		}

		if m.sessionService.State() == StateWaitingChoice {
			// 补充输入子态下，除显式控制键外的**所有**按键一律转发给 textarea。
			//
			// 本分支末尾是无条件 `return m, nil` ⇒ 除下列导航键外的按键在此被静默丢弃，
			// 永远到不了函数尾部的 textarea 转发点。缺的就是这一步：用户按 i / 选中 Other 后
			// 能进子态、光标也聚焦了，**但敲任何字符都进不了输入框**（手测现象「按 i 打不进字」）。
			//
			// ★ 必须放行**全部**字符键（含 j / k）：只放行部分字母会让 `json` 的 j、`Key` 的 k 继续被吞。
			// ★ 转发复用 routeKeyToComposerTextarea（唯一实现），不在本处再写一份 —— 见 双实现坑。
			if m.choiceFreeTextMode {
				switch msg.String() {
				case "enter", "esc", "ctrl+c":
					// 落到下方常规分流：提交补充内容 / 退回选择态 / 取消
				default:
					return m, m.routeKeyToComposerTextarea(msg)
				}
			}
			switch msg.String() {
			case "up", "k":
				if m.toolService.ChoiceIdx() > 0 {
					m.toolService.SetChoiceIdx(m.toolService.ChoiceIdx() - 1)
				}
				return m, nil
			case "down", "j":
				if m.toolService.PendingChoice() != nil && m.toolService.ChoiceIdx() < len(m.toolService.PendingChoice().Options)-1 {
					m.toolService.SetChoiceIdx(m.toolService.ChoiceIdx() + 1)
				}
				return m, nil
			case "i":
				// 2026-09-21 · AskUser 对齐 同类 CLI：i 键进入「补充自由输入」子态（对齐「typing jumps into notes」）。
				// 2026-09-23 · ：进入条件收进 `isNoteCapableChoice`（唯一判定）—— 与
				// renderChoiceUI 的 `[i]` 提示行**同源**。修前两处各写一份条件（提示看 hasOtherOption、
				// 按键看 ToolID 白名单），一旦面板形态变化就分叉出「界面不提示、按键却有反应」的隐性功能。
				if isNoteCapableChoice(m.toolService.PendingChoice()) {
					m.choiceFreeTextMode = true
					m.uiService.Textarea().SetValue("")
					m.uiService.SetInputFocusedOnStop(false)
					m.uiService.Textarea().Focus()
					m.recalcViewportHeight()
				}
				return m, nil
			case "enter":
				// 补充输入子态：enter 提交自由文本（而非选选项）
				if m.choiceFreeTextMode {
					if m.toolService.PendingChoice() != nil {
						pending := m.toolService.PendingChoice()
						freeText := m.uiService.Textarea().Value()
						// 补：空答案守卫 —— 什么都不填就 enter，会把
						// `Answer == "" && FreeText == ""` 回传给模型（`choiceResultMsg` →
						// 工具结果 `output` 为空）⇒ 模型收到「无内容的答案」白跑一轮，
						// 用户还以为自己答过了（正是 09-22 会话存档 L25 无 output 的成因）。
						// 处置：留在子态，等用户输入内容，或按 Esc 退回选择态。
						if strings.TrimSpace(freeText) == "" {
							return m, nil
						}
						// 2026-09-23 · (a)：选 Other 时回传**标签本身**（不再置空）——
						// 对齐 同类 CLI answers[0] = OTHER_OPTION_LABEL。置空会让模型收到一句
						// 无来源标注的裸文本，只能退回自己入参里的 default_idx 猜
						//（真机 2026-09-23 · 会话存档 01a0c506：output="回复hello" 却答"你选择的是 JWT"）。
						seg := resolveAskUserAnswer(pending.Options, m.toolService.ChoiceIdx())
						// （2026-09-22 · 手测真机现象修）：回显已提交的答案 ——
						// 与选项路径（下方 `case "enter"` 的 ` → <label>`）同口径。
						//
						// 修前本路径**只回传、不回显，也不清空输入框** ⇒ 用户 enter 之后只见选择框
						// 消失、屏幕上什么也没多出来、敲的字还留在输入框里 ⇒ 判断为「没送出去 / 没回复」
						//（会话存档 01a0c516 L31 证明答案其实已作为 tool_result 送达模型）。
						//
						// 2026-09-23 · (c)：回显改为与回传**同源**（由同一对 (answer, note) 派生，
						// 见 formatAskUserEcho）—— 修前 echo 由局部变量单独拼，两条路径可各自漂移，
						// 用户只会看到其中一边。
						echo := formatAskUserEcho(seg.Answer, freeText)
						m.addMessage(fmt.Sprintf(" → %s", echo))
						result := choiceResultMsg{
							ToolID: pending.ToolID,
							Answer: seg.Answer,
							// 把「这是用户自己的答案，不是补充说明」这一结构化事实
							// 一路带到拼装层 —— 由它决定第二段挂 `user_answer:` 还是 `user_note: `。
							AnswerIsUserOwn: seg.IsUserOwnAnswer,
							Cancelled:       false,
							FreeText:        freeText,
						}
						m.choiceFreeTextMode = false
						m.uiService.Textarea().Reset()
						m.uiService.Textarea().SetHeight(3)
						m.uiService.Textarea().Blur()
						m.uiService.SetInputFocusedOnStop(false)
						m.toolService.SetPendingChoice(nil)
						m.recalcViewportHeight()
						return m, func() tea.Msg { return result }
					}
				}
				if m.toolService.PendingChoice() != nil && m.toolService.ChoiceIdx() < len(m.toolService.PendingChoice().Options) {
					pending := m.toolService.PendingChoice()
					selected := pending.Options[m.toolService.ChoiceIdx()]
					// 选中 Other（None of the above）→ 进入补充输入子态，不立即回传
					if selected.Value == otherOptionValue {
						m.choiceFreeTextMode = true
						m.uiService.Textarea().SetValue("")
						m.uiService.SetInputFocusedOnStop(false)
						m.uiService.Textarea().Focus()
						m.recalcViewportHeight()
						return m, nil
					}
					if pending.ToolID == permissionModeChoiceToolID {
						m.addMessage(fmt.Sprintf("Selected: %s", selected.Label))
					} else {
						m.addMessage(fmt.Sprintf(" → %s", selected.Label))
					}
					result := choiceResultMsg{
						ToolID:    pending.ToolID,
						Answer:    selected.Value,
						Cancelled: false,
					}
					m.toolService.SetPendingChoice(nil)
					m.recalcViewportHeight()
					return m, func() tea.Msg { return result }
				}
			case "esc", "ctrl+c":
				if m.toolService.PendingChoice() != nil {
					// 补充输入子态下 esc：先退出子态回到选择态（对齐 同类 CLI freeform esc 返回选项）
					if m.choiceFreeTextMode {
						// 退回选择态即**放弃这份草稿** —— 再次进入子态（i / 选中 Other）
						// 本就会 SetValue("")，留着它只会在选择框消失后残留成「没送出去的输入」。
						m.choiceFreeTextMode = false
						m.uiService.Textarea().Reset()
						m.uiService.Textarea().SetHeight(3)
						m.uiService.Textarea().Blur()
						m.uiService.SetInputFocusedOnStop(false)
						m.recalcViewportHeight()
						return m, nil
					}
					pending := m.toolService.PendingChoice()
					switch pending.ToolID {
					case permissionModeChoiceToolID:
						m.addMessage("Cancelled — permission unchanged")
					case securityPresetChoiceToolID:
						m.addMessage("Cancelled — security preset unchanged")
					default:
						m.addMessage(" → Cancelled")
					}
					result := choiceResultMsg{
						ToolID:    pending.ToolID,
						Answer:    "",
						Cancelled: true,
					}
					m.toolService.SetPendingChoice(nil)
					m.recalcViewportHeight()
					return m, func() tea.Msg { return result }
				}
			case "pgup", "pgdown", "ctrl+up", "ctrl+down", "home", "end":
				// 2026-10-06（P1）：选择面板期间同样放行视口导航键 —— 与确认面板同因同解
				// （修前本分支末尾的裸 `return m, nil` 吞掉这些键，见 viewportNavKey 注释）。
				//
				// ⚠️ 边界：补充输入子态（choiceFreeTextMode）下**不**走这里 —— 那种子态把
				// 键盘交给 textarea（见上方 分支），home/end 在那里是文本光标操作，
				// 抢过来会破坏打字；子态里要滚动请先按 Esc 退回选择态。
				m.viewportNavKey(msg.String())
				return m, nil
			}
			return m, nil
		}

		if m.sessionService.State() == StateViewingDoc {
			switch msg.String() {
			case "up", "k":
				if m.uiService.DocLinkIdx() > 0 {
					m.uiService.SetDocLinkIdx(m.uiService.DocLinkIdx() - 1)
				}
				return m, nil
			case "down", "j":
				if m.uiService.DocLinkIdx() < len(m.uiService.DocLinks())-1 {
					m.uiService.SetDocLinkIdx(m.uiService.DocLinkIdx() + 1)
				}
				return m, nil
			case "enter":
				if m.uiService.DocLinkIdx() < len(m.uiService.DocLinks()) {
					link := m.uiService.DocLinks()[m.uiService.DocLinkIdx()]
					if err := OpenInEditor(link.Path, link.Line); err != nil {
						m.addMessage(fmt.Sprintf("⚠️ Failed to open editor: %v", err))
					} else if m.sessionService.State() == StateIdle {
						m.addMessage(fmt.Sprintf("📂 Opened: %s", FormatFileLinkChoice(link)))
					}
				}
				m.sessionService.SetState(StateIdle)
				m.uiService.SetDocLinks(nil)
				m.uiService.SetDocLinkIdx(0)
				m.recalcViewportHeight()
				return m, nil
			case "esc", "ctrl+c":
				m.sessionService.SetState(StateIdle)
				m.uiService.SetDocLinks(nil)
				m.uiService.SetDocLinkIdx(0)
				m.recalcViewportHeight()
				return m, nil
			}
			// P1-1 修复：删除 switch 之后的兜底 return，让非导航键跌落到主 switch
		}

		if m.sessionService.State() != StateIdle && m.sessionService.State() != StateViewingDoc {
			switch msg.String() {
			case "esc":
				return m.stopExecution()
			case "tab":
				// Tab to focus Stop button when not idle
				if m.uiService.Textarea().Focused() {
					m.uiService.SetInputFocusedOnStop(true)
					m.uiService.Textarea().Blur()
					return m, nil
				}
			case "shift+tab":
				// Shift+Tab to return focus from Stop button to textarea
				if m.uiService.InputFocusedOnStop() {
					m.uiService.SetInputFocusedOnStop(false)
					m.uiService.Textarea().Focus()
					return m, nil
				}
			case "enter":
				// Enter on Stop button triggers stop
				if m.uiService.InputFocusedOnStop() {
					return m.stopExecution()
				}
			case "pgup", "pgdown", "ctrl+up", "ctrl+down", "home", "end":
				// 实现收进 viewportNavKey（唯一实现），此处只做薄转发 ——
				// 修前这里另有一份 `case "pgup": HalfViewUp()` 链，与主键位表各写一份。
				m.viewportNavKey(msg.String())
				return m, nil
			case "up":
				if m.navigateInputHistory(-1) {
					return m, nil
				}
			case "down":
				if m.navigateInputHistory(1) {
					return m, nil
				}
			}
		}

		// N5 typeahead：@ / # / slash 补全弹窗按键拦截（仅 idle + 弹窗可见时生效）
		// P0-08 扩展：三种弹窗复用同一套键盘交互
		if m.suggestionState != nil && m.suggestionState.Visible {
			// Enter/Tab may arrive before the 50ms debounce tick. Refresh synchronously
			// so the selected range and candidates always match the current input.
			if msg.String() == "enter" || msg.String() == "tab" {
				m.updateTypeaheadSuggestions()
			}
			switch msg.String() {
			case "up":
				m.suggestionState.MoveUp()
				return m, nil
			case "down":
				m.suggestionState.MoveDown()
				return m, nil
			case "enter":
				if sel := m.suggestionState.CurrentSelection(); sel != nil {
					// 已完整输入选中补全项时，再应用补全只会追加一个分隔空格
					// （用户感知为“第一次回车变成空格”）。此时关闭弹窗，
					// 放行 Enter 走下方正常发送路径。
					if m.typeaheadSelectionAlreadyTyped(*sel) {
						// 父命令（registry 注册了 "name ..." 子命令）裸回车
						// 不执行静态 usage 死文本，而是展开为 "/name " 并弹出子命令候选
						// （提示常驻，用户可直接接着敲 show/list/...）。
						// 叶子命令保持既有语义：关弹窗、放行执行。
						if sel.Trigger == typeahead.TriggerSlash &&
							typeahead.HasSubcommands(sel.DisplayText, m.slashCommandItems()) {
							m.applyTypeaheadCompletion(*sel)
							m.updateTypeaheadSuggestions()
							return m, nil
						}
						m.suggestionState.Clear()
						m.recalcViewportHeight()
						break
					}
					m.applyTypeaheadCompletion(*sel)
					return m, nil
				}
			case "tab":
				// Tab：最长公共前缀补全（对标 fileSuggestions.ts:587）
				// P0-08：保留 Trigger 字段以确保 ApplySuggestion 选择正确的后缀逻辑
				prefix := typeahead.FindLongestCommonPrefix(m.suggestionState.Completions)
				input := m.uiService.Textarea().Value()
				currentToken := ""
				if m.suggestionState.TokenStart >= 0 && m.suggestionState.TokenStart+1 <= len(input) {
					end := m.suggestionState.TokenStart + 1 + m.suggestionState.TokenLen
					if end > len(input) {
						end = len(input)
					}
					currentToken = input[m.suggestionState.TokenStart+1 : end]
				}
				// A common prefix equal to the typed token is not a completion. In
				// particular, `/s` must remain `/s`, rather than becoming invalid `/s `.
				if prefix != "" && len(prefix) > len(currentToken) &&
					strings.HasPrefix(strings.ToLower(prefix), strings.ToLower(currentToken)) {
					completeCommand := false
					for _, completion := range m.suggestionState.Completions {
						if strings.EqualFold(strings.TrimSpace(completion.DisplayText), strings.TrimSpace(prefix)) {
							completeCommand = true
							break
						}
					}
					if completeCommand {
						m.applyTypeaheadCompletion(typeahead.Completion{
							DisplayText: prefix,
							IsDir:       false,
							Trigger:     m.suggestionState.CurrentTrigger,
						})
					} else {
						m.applySlashPrefix(prefix)
					}
				}
				return m, nil
			case "esc":
				m.suggestionState.Clear()
				m.recalcViewportHeight()
				return m, nil
			}
		}

		// 输入框选区按键（composer_selection.go）：Shift+方向键/Home/End 扩展选区、
		// Backspace/Delete 删除选区、Esc 清除、普通方向键折叠到选区边缘。
		// 返回 true 表示已消费（不再进入全局键位与 textarea 原生路径）；
		// 返回 false 表示透传（输入字符路径已先删除选区再落到 textarea，
		// 实现整体替换）。Ctrl+C 不在此拦截，走下方 idle 复制分支。
		if m.handleComposerSelectionKey(msg) {
			if newHeight := m.desiredInputHeight(); m.uiService.Textarea().Height() != newHeight {
				m.uiService.Textarea().SetHeight(newHeight)
			}
			m.recalcViewportHeight()
			return m, nil
		}

		switch msg.String() {
		case "ctrl+d":
			m.cleanupOnExit()
			m.sessionService.SetQuitting(true)
			return m, tea.Quit

		case "ctrl+c":
			// Ctrl+C 仅用于取消当前交互或中断正在执行的 AI/工具调用。
			// 空闲时：输入框有活动选区 → 复制选区（终端惯例，复制不清除选区）；
			// 无选区时保持原"无副作用"行为。对话复制仍使用 Ctrl+Y 或 /copy。
			if m.sessionService.State() == StateIdle {
				if text := m.composerSelectionText(); text != "" {
					return m, m.copyComposerSelection(text)
				}
				return m, nil
			}
			return m.stopExecution()

		case "ctrl+y":
			if m.sessionService.State() == StateIdle {
				return m, m.copyConversation("last")
			}
			return m, nil

		case "alt+y":
			if m.sessionService.State() == StateIdle {
				return m, m.copyConversation("all")
			}
			return m, nil

		case "ctrl+o":
			if m.sessionService.State() == StateIdle {
				return m.handleOpenDocNav()
			}
			return m, nil

		case "pgup", "pgdown", "ctrl+up", "ctrl+down", "home", "end":
			// 实现收进 viewportNavKey（唯一实现），此处只做薄转发。
			m.viewportNavKey(msg.String())
			return m, nil

		case "up":
			if m.navigateInputHistory(-1) {
				return m, nil
			}

		case "down":
			if m.navigateInputHistory(1) {
				return m, nil
			}

		default:
			if m.uiService.Keybindings() != nil {
				// mode 与绑定表一致：normal/insert（非 vim_normal/vim_insert）
				mode := "insert"
				if m.uiService.VimMode() && m.uiService.VimModeState() == VimNormal {
					mode = "normal"
				}
				key := keybindings.FromKeyMsg(msg)
				action := m.uiService.Keybindings().Parse(key, mode)
				switch action.Type {
				case keybindings.ActionVimMotion:
					switch action.Motion {
					case "cursor_left":
						*m.uiService.Textarea(), _ = m.uiService.Textarea().Update(tea.KeyMsg{Type: tea.KeyLeft})
					case "cursor_right":
						*m.uiService.Textarea(), _ = m.uiService.Textarea().Update(tea.KeyMsg{Type: tea.KeyRight})
					case "cursor_down":
						m.uiService.Viewport().LineDown(3)
					case "cursor_up":
						m.uiService.Viewport().LineUp(3)
					case "line_start":
						m.uiService.Textarea().CursorStart()
					case "line_end":
						m.uiService.Textarea().CursorEnd()
					case "goto_top":
						m.uiService.Viewport().GotoTop()
					case "goto_bottom":
						m.uiService.Viewport().GotoBottom()
					}
					return m, nil
				case keybindings.ActionVimModeSwitch:
					switch action.Motion {
					case "mode_insert":
						m.uiService.SetVimModeState(VimInsert)
					case "mode_normal":
						m.uiService.SetVimModeState(VimNormal)
					}
					return m, nil
				case keybindings.ActionVimDelete:
					switch action.Motion {
					case "delete_char":
						*m.uiService.Textarea(), _ = m.uiService.Textarea().Update(tea.KeyMsg{Type: tea.KeyDelete})
					case "delete_line":
						m.vimDeleteCurrentLine()
					}
					return m, nil
				case keybindings.ActionSequencePending:
					return m, nil
				case keybindings.ActionReserved:
					return m, nil
				case keybindings.ActionPassthrough:
					// F5 修复后补全：keybindings.json 不存在（Mode=""）时无 vim 键绑定，
					// 此处 hardcoded fallback 确保 vimMode=true 时 'i'/'a'/'o'/Escape 能切换模式。
					// 当用户配置了 keybindings.json 时，Parse 返回 ActionVimModeSwitch 而非 Passthrough，
					// 此 fallback 不触发（无冲突）。
					if m.uiService.VimMode() {
						if m.uiService.VimModeState() == VimNormal {
							// Normal 模式：i/a/o 切换到 Insert
							if len(msg.Runes) == 1 && (msg.Runes[0] == 'i' || msg.Runes[0] == 'a' || msg.Runes[0] == 'o') {
								m.uiService.SetVimModeState(VimInsert)
								return m, nil
							}
						} else {
							// Insert 模式：Escape 切换到 Normal
							if msg.Type == tea.KeyEsc {
								m.uiService.SetVimModeState(VimNormal)
								return m, nil
							}
						}
					}
				}
			}

		case "tab":
			if m.sessionService.State() == StateIdle && m.uiService.Textarea().Focused() {
				m.uiService.SetInputFocusedOnSend(true)
				m.uiService.Textarea().Blur()
				return m, nil
			}

		case "shift+tab":
			if m.sessionService.State() == StateIdle && m.uiService.InputFocusedOnSend() {
				m.uiService.SetInputFocusedOnSend(false)
				m.uiService.Textarea().Focus()
				return m, nil
			}

		case "enter":
			if m.uiService.InputFocusedOnSend() {
				return m.sendInput()
			}
			if m.sessionService.State() == StateIdle && m.uiService.Textarea().Focused() {
				return m.sendInput()
			}
			if m.uiService.VimMode() && m.uiService.VimModeState() == VimNormal {
				return m.sendInput()
			}
			// P0-8/9 新增：非 idle 状态下 Enter 将 textarea 内容入队（不丢弃用户输入）。
			// 仅当 textarea 聚焦且无 typeahead 弹窗可见时触发，避免与补全路径冲突。
			if m.sessionService.State() != StateIdle && m.uiService.Textarea().Focused() && !m.isTypeaheadVisible() {
				rawInput := m.uiService.Textarea().Value()
				if utf8.RuneCountInString(rawInput) > maxInputRunes {
					m.showInputLimitWarning()
					return m, nil
				}
				input := strings.TrimSpace(rawInput)
				if input != "" {
					if !m.EnqueueInput(QueuedUserMessage{Text: input, Action: QueuedInputPlain}) {
						m.addMessage(errorStyle.Render("⚠️ Queued inputs full. Please process queued messages first."))
						return m, nil
					}
					m.uiService.Textarea().Reset()
					m.uiService.Textarea().SetHeight(3)
					m.uiService.Textarea().Focus()
					m.recalcViewportHeight()
				}
				return m, nil
			}

		case "alt+up":
			// P0-8/9 新增：Alt+Up 从队尾 pop 一条消息回 composer 编辑（LIFO）。
			// 仅在非 idle 状态且有排队消息时触发；idle 状态下保持原 Up 键导航语义。
			if m.sessionService.State() != StateIdle && m.HasQueuedInputs() {
				msg, ok := m.PopLatestQueuedForEdit()
				if ok {
					m.uiService.Textarea().SetValue(msg.Text)
					m.uiService.Textarea().Focus()
					m.recalcViewportHeight()
				}
				return m, nil
			}
		}
		// 文档导航只接受导航键和全局快捷键；其余按键不得写入被隐藏的 composer。
		if m.sessionService.State() == StateViewingDoc {
			return m, nil
		}

	case streamEventMsg:
		if m.isStaleStreamRequest(msg.requestID) {
			return m, nil
		}
		// 应用异步请求构造回传的 requestSnapshot（仅携带时覆盖）。
		if msg.snapshotKnown {
			m.SessionModel.requestSnapshot = msg.requestSnapshot
		}
		cmds = append(cmds, m.handleStreamEvent(msg.event, msg.events))

	case streamDoneMsg:
		if m.isStaleStreamRequest(msg.requestID) {
			return m, nil
		}
		cmds = append(cmds, m.handleStreamDone(msg))

	case drainQueuedInputMsg:
		// P0-8/9 新增：drain 钩子异步触发。
		// 由 handleStreamDone 在所有 turn 结束处理（IncrementTurn/saveSession/
		// FileHistory Snapshot/runTurnEndHooks/checkAutoCompact/auto memory extraction）
		// 完成后通过返回 drainQueuedInputMsg{} 触发，避免跳过 turn 结束钩子。
		// 此处调用 sendInput 复用原路径提交排队消息并启动新一轮 AI stream。
		//
		// P0-8/9 致命漏洞 #1 修复：drain 前必须检查 State == StateIdle。
		// 竞态场景：用户在 drainQueuedInputMsg 到达前抢先按 Enter 提交新消息（State=Streaming），
		// 若此时仍 pop 消息，sendInput 会因 State != Idle 提前返回（stream.go:60-64），
		// 但消息已从队列 pop，导致排队消息永久丢失。
		// 对标同类 CLI maybe_send_next_queued_inputpop 前检查 is_input_allowed。
		// 修复策略：State != Idle 时不 pop，延迟 200ms 重新排队 drainQueuedInputMsg，
		// 当前 turn 结束后 handleStreamDone 也会再次触发 drain。
		if m.HasQueuedInputs() {
			if m.sessionService.State() != StateIdle {
				// State != Idle：排队消息保留在队列中，延迟重新触发 drain 避免忙等。
				return m, tea.Tick(200*time.Millisecond, func(time.Time) tea.Msg {
					return drainQueuedInputMsg{}
				})
			}
			next, ok := m.DrainNextQueuedInput()
			if ok {
				m.uiService.Textarea().SetValue(next.Text)
				return m.sendInput()
			}
		}

	case toolResultMsg:
		if msg.requestID != m.aiService.RequestID() {
			return m, nil
		}
		// R4：结构化拒绝入账必须在**分流之前**——AskUser 主拦截路径
		// （下面 checkAskUserChoice 命中时）不经 handleToolResults，
		// 若记在 handleToolResults 里，同批到达的沙箱/权限拒绝会被整批忽略。
		m.noteRoundSecurityDenials(msg.results)
		// 清理断路器计数（requestID 已变化，旧计数无意义）
		m.confirmCount = nil
		// T3 S2: checkAskUserChoice 改为返回 []PendingChoice（BC-1 破坏性变更）。
		// 日常对话场景一般只有 1 个 AskUser 命中，取第一个渲染（与原 *PendingChoice 行为对齐）。
		askUserChoices, hasAskUser := m.checkAskUserChoice(msg.results)
		if hasAskUser && len(askUserChoices) > 0 {
			// P0 防护记账：AskUser 主拦截路径不走 handleToolResults，
			// 必须在此记轮次，否则审查子循环上限可被 AskUser 交错规避。
			m.guardRecordAskUserTurn(msg.results)
			askUserResult := askUserChoices[0]
			m.toolService.SetPendingChoice(&askUserResult)
			m.toolService.SetChoiceIdx(0)
			m.sessionService.SetState(StateWaitingChoice)
			m.addMessage(toolStyle.Render(renderAskQuestionLine(askUserResult.Category, askUserResult.Prompt)))
			m.recalcViewportHeight()
		} else {
			cmds = append(cmds, m.handleToolResults(msg.results))
		}

	case aiErrorMsg:
		if m.isStaleStreamRequest(msg.requestID) {
			return m, nil
		}
		// 保存原 State 用于判断是否为用户主动取消（2026-07-31 修正第1轮审查致命漏洞#3）。
		// context.Canceled 在 StateStreaming/StateToolExec 时为用户主动取消（stop键/Ctrl+C），静默处理；
		// 其他 State 下的 context.Canceled 可能是网络超时等真实错误，仍显示。
		prevState := m.sessionService.State()
		m.sessionService.SetState(StateIdle)
		m.uiService.SetInputFocusedOnStop(false)
		m.recalcViewportHeight()
		if errors.Is(msg.err, context.Canceled) && (prevState == StateStreaming || prevState == StateToolExec || m.sessionService.Interrupted()) {
			// 用户主动取消（stop键/Ctrl+C），静默处理，仅显示中断提示。
			// 补充 Interrupted() 检查：stopExecution/Ctrl+C 已将 State 重置为 Idle，
			// 仅靠 prevState 检查会失效；Interrupted() 为 true 说明是主动取消。
			// 同时清理中断状态，避免残留干扰下一轮。
			m.resetStreamState()
			m.addMessage(toolStyle.Render("[已中断]"))
			return m, m.restoreIdleMouse()
		}
		errMsg := msg.err.Error()
		if ai.IsContextOverflowError(errMsg) {
			current := m.sessionService.CurrentSession()
			beforeRevision := sessionRevision(current)
			compactResult := m.handleCompactWithTrigger("", ctxpkg.CompactionTriggerReactive, ctxpkg.CompactionPolicyForce)
			if compactCommittedSince(current, beforeRevision) {
				m.addMessage(toolStyle.Render("Context overflow, auto-compacting and retrying..."))
				m.sessionService.SetState(StateStreaming)
				m.resetStreamState()
				cmds = append(cmds, m.startAIStream(), m.restartTickCmd())
			} else {
				m.addMessage(errorStyle.Render("Context overflow recovery stopped: " + compactResult + ". Use /rewind or /clear to free space."))
			}
		} else if ai.GetErrorID(msg.err) == ai.ErrAIApiOverload {
			m.addMessage(toolStyle.Render("AI 服务过载，已自动降级到备用模型"))
			m.addMessage(errorStyle.Render(formatFriendlyError(msg.err)))
		} else {
			friendlyErr := formatFriendlyError(msg.err)
			m.addMessage(errorStyle.Render(friendlyErr))
		}
		if m.sessionService.State() == StateIdle {
			cmds = append(cmds, m.restoreIdleMouse())
		}

	case upgradeCheckResultMsg:
		// P0-3（D5-2）：异步 /upgrade check 结果到达，显示到 TUI
		m.addMessage(msg.result)

	case autoCompactResultMsg:
		// P0-1（D4-1）：异步自动压缩结果到达，应用到 model（在主循环中执行，无数据竞态）
		// 压缩和新请求可以并发完成；只允许当前请求代次修改会话。候选捕获
		// 错误发生在压缩开始前，因此在 revision 检查前展示，避免被静默丢弃。
		m.applyCompactUsage(msg.compactUsage, msg.hasCompactUsage, msg.sessionEpoch, msg.modelEpoch, msg.cacheEpoch)
		if msg.requestID != "" && msg.requestID != m.aiService.RequestID() ||
			msg.sessionEpoch != m.SessionModel.sessionEpoch ||
			msg.modelEpoch != m.SessionModel.modelEpoch ||
			msg.cacheEpoch != cacheCapabilityEpoch(m.aiService.Caching()) {
			return m, nil
		}
		if msg.err != nil {
			m.addMessage(errorStyle.Render(fmt.Sprintf("Compaction failed: %v", msg.err)))
			break
		}
		current := m.sessionService.CurrentSession()
		var currentRevision uint64
		if current != nil {
			_, _, currentRevision, _ = current.Snapshot()
		}
		if current == nil || currentRevision != msg.expectedRevision {
			return m, nil
		}
		if msg.nothingToCompact {
			m.addMessage(toolStyle.Render("Nothing to compact"))
		} else {
			if err := m.commitCompactResult(msg.result, msg.config, msg.pipeline, msg.expectedVersion, msg.expectedRevision); err != nil {
				m.addMessage(errorStyle.Render(fmt.Sprintf("Compaction failed: %v", err)))
				break
			}
			if !msg.result.Changed {
				m.addMessage(toolStyle.Render(formatCompactResult(msg.result, msg.config)))
				break
			}
			if m.notifier != nil {
				m.notifier.Push(notify.NewNotification("compact", "上下文压缩", fmt.Sprintf("消息数 %d → %d", msg.result.PreMessageCount, msg.result.PostMessageCount)))
			}
			// 切口③：压缩成功后插入显式分界（UI-only），标记「以上历史已压缩为摘要，
			// 模型上下文不再包含原始内容」。只进显示侧，永不写模型侧（不变式 I5）。
			m.addMessage(formatCompactBoundary())
			m.addMessage(toolStyle.Render(fmt.Sprintf("Compacted: messages %d -> %d", msg.result.PreMessageCount, msg.result.PostMessageCount)))
			// 问题②：压缩完成后检测固定开销是否已超窗口（此时压缩无法缓解，需提示用户增大窗口）
			if fixed, window, exceeded := m.fixedOverheadExceedsWindow(); exceeded {
				m.addMessage(toolStyle.Render(formatFixedOverheadWarning(fixed, window)))
			}
		}
		// P0-1: compact 完成后，如果有排队输入，触发 drain（对标 handleStreamDone 的 drain 逻辑）
		if m.HasQueuedInputs() {
			cmds = append(cmds, func() tea.Msg { return drainQueuedInputMsg{} })
		}

	case choiceResultMsg:
		// 权限菜单是 TUI 本地选择，不应进入 AskUser 工具结果链路。
		if msg.ToolID == permissionModeChoiceToolID {
			m.sessionService.SetState(StateIdle)
			if !msg.Cancelled {
				// 走 SetModeByName：统一处理已删除模式的审批语义迁移。
				if m.toolService.Policy().SetModeByName(msg.Answer) {
					m.addMessage(fmt.Sprintf("Permission mode: %s", m.toolService.Policy().Mode()))
				} else {
					m.addMessage(errorStyle.Render("Invalid permission mode; nothing changed"))
				}
			}
			m.recalcViewportHeight()
			return m, tea.Batch(cmds...)
		}

		// 会话选择器：同样是 TUI 本地选择，不进 AskUser 链路。
		// 载入走 handleSessionSwitch —— 与 /session switch <id> 同一条路径，
		// 菜单只负责把「用户点的是哪一个会话」翻译成会话 ID。
		if msg.ToolID == sessionPickerChoiceToolID {
			m.sessionService.SetState(StateIdle)
			if !msg.Cancelled && msg.Answer != "" {
				if out := m.handleSessionSwitch(msg.Answer); out != "" {
					m.addMessage(errorStyle.Render(out))
				}
			}
			m.recalcViewportHeight()
			return m, tea.Batch(cmds...)
		}

		// 快照回退选择器（，2026-10-14）：同样是 TUI 本地选择，不进 AskUser 链路。
		// 回退走 doRewind —— 与 `/rewind <msgId>` / `/rewind #N` 同一条路径，
		// 选择器只把「用户点的是哪一条快照」翻译成锚点，不重复实现回退逻辑
		// （：菜单自己再实现一遍是漂移的典型来源）。
		if msg.ToolID == rewindPickerChoiceToolID {
			m.sessionService.SetState(StateIdle)
			if !msg.Cancelled && msg.Answer != "" {
				m.addMessage(m.doRewind(msg.Answer))
			}
			m.recalcViewportHeight()
			return m, tea.Batch(cmds...)
		}

		// 安全套餐菜单：同样是 TUI 本地选择，不进 AskUser 链路。
		// 应用语义与命令路径**同源**（applySecurityPreset + securityPresetAppliedNotice），
		// 不是"菜单自己再实现一遍" —— 那正是 记录的双实现漂移形态。
		if msg.ToolID == securityPresetChoiceToolID {
			m.sessionService.SetState(StateIdle)
			if !msg.Cancelled {
				if preset, ok := permission.LookupSecurityPreset(msg.Answer); ok {
					exec := m.toolService.Executor()
					applySecurityPreset(m.toolService.Policy(), exec, preset)
					m.addMessage(securityPresetAppliedNotice(preset, exec))
				} else {
					// 菜单只可能产出内置 id ⇒ 走到这里说明两侧数据源不一致，
					// 必须显式报错，不得静默什么都不做。
					m.addMessage(errorStyle.Render(fmt.Sprintf(
						"Unknown security preset: %s; nothing changed", msg.Answer)))
				}
			}
			m.recalcViewportHeight()
			return m, tea.Batch(cmds...)
		}

		// H10 Phase 6: 编排器桥接的提问/确认请求走专用回传路径（S4 接线，）。
		// ToolID 前缀 "orch-ask-" 表示编排器提问，"orch-contract" 表示方案确认。
		// 这两种请求不进入 AI 工具结果链路，直接通过 RespCh 回传给编排器 goroutine。
		if strings.HasPrefix(msg.ToolID, "orch-ask-") && m.pendingOrchAskReq != nil {
			req := m.pendingOrchAskReq
			m.pendingOrchAskReq = nil
			if msg.Cancelled {
				sendOrchAskResponse(*req, orchestrator.ClarifyAnswer{}, true)
			} else {
				// 提取问题 ID（ToolID 格式 "orch-ask-<QID>"）
				qID := strings.TrimPrefix(msg.ToolID, "orch-ask-")
				sendOrchAskResponse(*req, orchestrator.ClarifyAnswer{
					QuestionID: qID,
					Value:      msg.Answer,
				}, false)
			}
			// 恢复空闲状态，等待编排器 goroutine 推进
			m.sessionService.SetState(StateIdle)
			m.recalcViewportHeight()
			return m, tea.Batch(cmds...)
		}
		if msg.ToolID == "orch-contract" && m.pendingOrchContractReq != nil {
			req := m.pendingOrchContractReq
			m.pendingOrchContractReq = nil
			if msg.Cancelled {
				sendOrchContractResponse(*req, orchestrator.ConfirmResult{}, true)
			} else {
				// 解析用户选择：accept/reject/modify
				result := orchestrator.ParseContractAnswer(orchestrator.ClarifyAnswer{Value: msg.Answer})
				sendOrchContractResponse(*req, result, false)
			}
			m.sessionService.SetState(StateIdle)
			m.recalcViewportHeight()
			return m, tea.Batch(cmds...)
		}

		answerMsg := "User cancelled selection"
		if !msg.Cancelled {
			// 2026-09-23 · ：拼装收进 formatAskUserAnswer（单一实现），不再就地拼 "\n[补充] "。
			// `msg.AnswerIsUserOwn` 决定第二段前缀 —— 选 Other 自填 ⇒ `user_answer:`（答案），
			// 选预设项 + `i` ⇒ `user_note: `（注）。无补充时只回选项；纯开放式提问只回裸文本。
			answerMsg = formatAskUserAnswer(msg.Answer, msg.FreeText, msg.AnswerIsUserOwn)
		}
		toolMsg := session.ToolResult(msg.ToolID, "AskUser", answerMsg, msg.Cancelled)
		m.sessionService.CurrentSession().SetMessages(append(m.sessionService.CurrentSession().GetMessages(), toolMsg))
		m.sessionService.SetState(StateStreaming)
		m.resetStreamState()
		cmds = append(cmds, m.startAIStream(), m.restartTickCmd())

	case OrchAskRequestMsg:
		// H10 Phase 6: 收到编排器提问请求，设置 PendingChoice 进入 StateWaitingChoice（S4 接线）。
		cmds = append(cmds, m.handleOrchAskRequest(msg))
		// 持续监听后续提问请求（编排器可能多轮提问）
		if m.orchBridge != nil {
			if askCmd := waitForOrchAskRequest(m.orchBridge.askCh); askCmd != nil {
				cmds = append(cmds, askCmd)
			}
		}

	case OrchContractRequestMsg:
		// H10 Phase 6: 收到编排器方案确认请求，设置 PendingChoice 进入 StateWaitingChoice（S4 接线）。
		cmds = append(cmds, m.handleOrchContractRequest(msg))
		if m.orchBridge != nil {
			if contractCmd := waitForOrchContractRequest(m.orchBridge.contractCh); contractCmd != nil {
				cmds = append(cmds, contractCmd)
			}
		}

	case orchCmdResultMsg:
		// H10 Phase 6: 异步编排命令完成，显示结果到 TUI（S4 接线，）。
		// clarify/plan/contract 在 goroutine 中执行，完成后通过 program.Send 回传结果。
		if msg.err != nil {
			m.addMessage(errorStyle.Render(fmt.Sprintf("编排命令失败: %v", msg.err)))
		} else if msg.output != "" {
			m.addMessage(toolStyle.Render(msg.output))
		}
		m.sessionService.SetState(StateIdle)
		m.recalcViewportHeight()

	case agentConfirmRequestMsg:
		// G4（2026-09-08 冒烟缺口）：子代理工具确认请求——复用现有确认 overlay。
		// 设置 AgentReplyCh 标记，按键由 handleAgentConfirmKeyMsg 专用分支处理，
		// 决策经通道回传给阻塞中的 AgentConfirmFn。
		//
		// 单槽占用守卫（对抗式审查 P1）：确认槽同一时刻只允许一个请求。
		// 已被占用（主会话 pipeline 确认/另一个子代理确认）时禁止覆盖——
		// 覆盖会导致被顶掉的请求方（replyCh 无人应答）悬挂至超时，主会话
		// 流程永不恢复（TUI 卡死）。守卫将死锁降级为可观测的拒绝。
		if existing := m.toolService.PendingConfirm(); existing != nil {
			slog.Warn("agent confirm slot occupied, denying sub-agent confirmation",
				"tool", msg.toolName, "occupiedBy", existing.ToolName)
			// D3（R3，2026-09-14）：槽位占用时用户**根本没被问过**，
			// 必须归因为 slot-guard，不得让下游渲染成"用户拒绝"。
			// 修复前这里是裸 false，而下游把它写成 "rejected by user"。
			outcome := tools.ConfirmOutcome{
				Decision: tools.DecisionDenied,
				Source:   tools.SourceSlotGuard,
			}
			// D2（R3）：**必须留痕**。修复前该分支只发 replyCh + 一条 slog，
			// 子 agent 的这次工具请求在界面上**凭空消失** —— 与另一个确认弹层
			// 同时发生时，用户完全无从得知"还有个请求被顶掉了"。
			// 文案由 outcome.Explain 生成（slot-guard 不会说"用户拒绝"）。
			m.addMessage(errorStyle.Render(outcome.Explain(msg.toolName)))
			// delta F（H1b）：槽位占用**也是**审批层的一次终局决策（用户从未被询问），
			// 必须留痕。它绕过 finishAgentConfirm 直接经 replyCh 返回 —— 是子 agent
			// 路径上的**第二个**漏斗，只挂 finishAgentConfirm 会整类漏掉。
			m.recordConfirmOutcome(bridge.OriginTUISubagent, msg.toolName, msg.toolInput, outcome)
			if msg.replyCh != nil {
				msg.replyCh <- outcome
			}
			return m, nil
		}
		kind := ApprovalKindExec
		if msg.toolName == "write_file" || msg.toolName == "edit_file" || msg.toolName == "NotebookEdit" {
			kind = ApprovalKindFileModify
		}
		m.toolService.SetPendingConfirm(&PendingConfirm{
			ToolName:     msg.toolName,
			ToolInput:    msg.toolInput,
			RiskLevel:    "agent-tool",
			Kind:         kind,
			Options:      approvalOptionsFor(kind, msg.toolName, msg.toolInput),
			AgentReplyCh: msg.replyCh,
		})
		m.sessionService.SetState(StateWaitingConfirm)
		m.recalcViewportHeight()

	case sandboxEscalateRequestMsg:
		// on-failure 沙箱升级请求——机制镜像 agentConfirmRequestMsg。
		// 单槽占用守卫（同一理由）：槽被占时禁止覆盖，否则被顶掉的请求方悬挂到超时。
		if existing := m.toolService.PendingConfirm(); existing != nil {
			slog.Warn("sandbox escalate slot occupied, denying escalation",
				"tool", msg.toolName, "occupiedBy", existing.ToolName)
			// 槽位占用 = 用户根本没被问过 ⇒ 回传 false（fail-closed）且留痕、可见。
			m.addMessage(errorStyle.Render("[sandbox-escalate] escalation request dropped: confirm slot occupied (user was not asked)"))
			m.recordConfirmOutcome(bridge.OriginTUISandboxEscalate, msg.toolName, msg.command,
				tools.ConfirmOutcome{Decision: tools.DecisionDenied, Source: tools.SourceSlotGuard})
			if msg.replyCh != nil {
				msg.replyCh <- false
			}
			return m, nil
		}
		m.toolService.SetPendingConfirm(&PendingConfirm{
			ToolName:          msg.toolName,
			ToolInput:         msg.command,
			RiskLevel:         "sandbox-escalate (EPERM)",
			Kind:              ApprovalKindExec,
			Options:           sandboxEscalateOptions(),
			SandboxEscalateCh: msg.replyCh,
		})
		m.sessionService.SetState(StateWaitingConfirm)
		m.recalcViewportHeight()

	case confirmResultMsg:
		// delta F（H2）：审批决策留痕。挂在**消费点**而不是 5 个发射点 ——
		// 挂发射点 = 5 处重复插入、漏一处就漏一类决策；消费点只有 1 处。
		m.recordApprovalDecision(bridge.OriginTUIMain, msg.toolName, msg.toolInput, msg.decision, msg.source, "")
		if msg.approved {
			// 请求内审批复用：登记「本次请求内 (toolName, toolInput) 已被用户批准」
			// ⇒ 同一请求里模型再发**逐字相同**的调用时不再重复询问（避免同一个副作用
			// 被问两次）。刻意与上面那条留痕挂**同一个消费点**，理由相同：
			// 消费点只有 1 处，发射点有 5 处，挂发射点必漏。
			//
			// requestID 取 m.aiService.RequestID() —— 与 newApprovalRecord 的取法**同源**，
			// 否则会出现"账本里的 request_id 与复用判定的边界不是同一个"这种漂移。
			// 作用域 = 本次用户请求（该 ID 由 handleSubmit 生成、整个 agent loop 内不变）；
			// 危险面不在复用范围（结构性排除，见 tools.Call.ReusedApproval 的字段注释）。
			m.noteRequestApproval(msg.toolName, msg.toolInput, m.aiService.RequestID())

			// 「允许该命令前缀」是**独立于**本会话全量批准的一档。
			//
			// 刻意放在两条分支（pipeline / 非 pipeline）**之上**：两条路径都要建立前缀记忆，
			// 挂分支里必漏一条（与上方"挂消费点而非发射点"同一条理由）。
			// 且必须在任何 SetPendingConfirm(nil) 之前读取 PendingConfirm（ToolName/ToolInput 的来源）。
			//
			// ★ 全量批准（DecisionApproveSession）**不**由此建立前缀记忆 —— 二者互不转化
			// （见 permission 包的 TestW022_PrefixIsNotFullSessionApproval）。
			if msg.decision == DecisionApproveSessionPrefix {
				if pc := m.toolService.PendingConfirm(); pc != nil && m.toolService.Policy() != nil {
					m.toolService.Policy().RememberPrefix(pc.ToolName, pc.ToolInput)
				}
			}
			// Check if pipeline confirmation; need to execute remaining tools
			if m.toolService.PendingConfirm() != nil && len(m.toolService.PendingConfirm().PipelinePendingTools) > 0 {
				// 断路器：同一 requestID+toolIndex 连续确认超过 1 次时拒绝（defense-in-depth）
				pc := m.toolService.PendingConfirm()
				key := fmt.Sprintf("%s:%d", pc.PipelineRequestID, pc.PipelineCurrentIndex)
				if m.confirmCount == nil {
					m.confirmCount = make(map[string]int)
				}
				m.confirmCount[key]++
				if m.confirmCount[key] > 1 {
					slog.Warn("circuit breaker: repeated confirmation detected, rejecting",
						"requestID", pc.PipelineRequestID, "toolIndex", pc.PipelineCurrentIndex)
					m.toolService.SetPendingConfirm(nil)
					m.sessionService.SetState(StateIdle)
					m.confirmCount = nil
					// 建议优化6：拒绝时补 resetStreamState 清理 PendingTools 残留状态（与 stopExecution 对齐）
					m.resetStreamState()
					m.addMessage(errorStyle.Render("Repeated confirmation detected; execution stopped for safety / 检测到重复确认，为安全起见已停止执行"))
					m.recalcViewportHeight()
					return m, nil
				}
				cmds = append(cmds, m.executePipelineRemaining(msg.decision == DecisionApproveSession), m.restartTickCmd())
			} else {
				// 修复：非 Pipeline 路径清空 PendingConfirm 后执行（approve 分支不再清空）
				if msg.decision == DecisionApproveSession {
					if pc := m.toolService.PendingConfirm(); pc != nil && m.toolService.Policy() != nil {
						m.toolService.Policy().Remember(pc.ToolName, pc.ToolInput, true)
					}
				}
				m.toolService.SetPendingConfirm(nil)
				cmds = append(cmds, m.executeToolsDirect(), m.restartTickCmd())
			}
		} else {
			// 建议优化1：取消确认时清理断路器计数（防内存泄漏，cancel/esc 已清空 PendingConfirm）
			m.confirmCount = nil
		}

	case pipelineConfirmMsg:
		// Pipeline requests user confirmation, entering confirmation wait state
		if msg.requestID != m.aiService.RequestID() {
			return m, nil
		}
		m.toolService.SetPendingConfirm(&PendingConfirm{
			ToolName:             msg.toolName,
			ToolInput:            msg.toolInput,
			RiskLevel:            msg.reason,
			PipelinePendingTools: msg.pendingTools,
			PipelineCurrentIndex: msg.currentIndex,
			PipelineRequestID:    msg.requestID,
			PipelinePriorResults: msg.priorResults,
			Kind:                 ApprovalKindPipeline,
			Options:              approvalOptionsFor(ApprovalKindPipeline, msg.toolName, msg.toolInput),
		})
		m.sessionService.SetState(StateWaitingConfirm)
		// The confirmation overlay renders the concrete operation and reason.
		// It is transient and must not be appended to the transcript, otherwise
		// the prompt can scroll the existing conversation out of view.
		cmds = append(cmds, m.restartTickCmd()) // 等待期要有 tick（计时+日志）
		m.recalcViewportHeight()

	case elicitationRequestMsg:
		// P0-7：MCP server 发来 elicitation/create 请求，构造 PendingConfirm 并切换到等待确认状态
		// 对标同类 CLI elicitation_options：3 选项 [Accept, Decline, Cancel]，Esc 硬编码 Cancel
		m.toolService.SetPendingConfirm(&PendingConfirm{
			Kind:          ApprovalKindMcpElicitation,
			Options:       buildApprovalOptions(ApprovalKindMcpElicitation),
			McpServerName: msg.serverName,
			McpRequestID:  msg.requestID,
			McpMessage:    msg.message,
			SelectIdx:     0,
		})
		m.sessionService.SetState(StateWaitingConfirm)
		// The elicitation UI owns this transient prompt; keep the transcript
		// stable while the user is choosing a response.
		m.recalcViewportHeight()

	case elicitationResponseMsg:
		// P0-7（第2轮修复致命漏洞 #3）：用户决策后回传响应给 MCP client
		// 由 resolveElicitation 返回的 tea.Cmd 发出，调用 ResolveElicitation 路由到对应 reqID
		// 通过 tools.GetCachedMCPClient 按服务器名查找 client，类型断言到 mcp.MCPClient
		if client, ok := tools.GetCachedMCPClient(msg.response.ServerName); ok {
			if mc, ok := client.(mcp.MCPClient); ok {
				if err := mc.ResolveElicitation(msg.response); err != nil {
					// fail-safe：reqID 已超时被清理，仅记录日志，不阻塞 TUI
					m.addMessage(errorStyle.Render(fmt.Sprintf("Elicitation resolve failed: %v", err)))
				}
			}
		}

	case monitorEvent:
		cmds = append(cmds, m.handleMonitorEvent(msg))
	case loopFireMsg:
		cmds = append(cmds, m.handleLoopFire(msg))
	case coalesceFlushMsg:
		// 输入合并器 flush：批量写入缓冲的 rune（REF-方案 §4.1）。
		// handleCoalesceFlush 返回非 nil 表示已刷新且需重调度（处理 flush 期间新到达的 rune）。
		if cmd := m.handleCoalesceFlush(msg.seq); cmd != nil {
			cmds = append(cmds, cmd)
		}
		return m, tea.Batch(cmds...)
	case mouseSelectionAutoScrollMsg:
		// 拖选边缘自动滚动 tick：拖动到对话区上/下边缘并停住时持续滚动视口，
		// 把选区延伸到可视范围之外（见 mouse_selection.go）。
		if cmd := m.handleMouseSelectionAutoScroll(); cmd != nil {
			cmds = append(cmds, cmd)
		}
		return m, tea.Batch(cmds...)
	}

	// 「按键 → textarea」的唯一实现；StateWaitingChoice 的补充输入子态调用同一个函数（见）。
	cmds = append(cmds, m.routeKeyToComposerTextarea(msg))

	var vpCmd tea.Cmd
	if mouseMsg, ok := msg.(tea.MouseMsg); ok {
		// N-MOUSE-ROW：坐标优先取渲染实测（layout.go mouseGeometry），
		// 只有 loading 帧/未渲染夹具才回退 recalcViewportHeight 的估算值。
		// 旧代码固定用 `lastInputAreaTop - vp.Height` 反推聊天区顶行：一旦某个占屏
		// 区块没进高度预算（Monitor 摘要区），渲染行数就会超过终端高度，bubbletea
		// 从顶部砍行 ⇒ 屏幕 Y 与模型 Y 差 N 行 ⇒ 「选这里、复制到上面」。
		//
		// 批 C / C-2：账本（l）连同区块表一起返回，拖选命中改为**按区块判定**
		// （五区共用一套屏幕坐标），不再是 chatTop/inputTop 两个 Y 阈值。
		l, inputAreaTop, chatAreaTop := m.mouseGeometry(mouseMsg)

		// 拖选进行中（含边缘自动滚动）：motion/release 只喂给选择状态机，
		// 不得转发给 viewport/textarea，否则拖动会滚动对话或移动光标。
		// 坐标口径取自拖选快照（sel.geom），与 press 同源，不读本次布局。
		if m.mouseSelection.dragging {
			switch {
			case mouseMsg.Action == tea.MouseActionMotion:
				m.updateMouseSelection(mouseMsg.X, mouseMsg.Y)
				// 指针拖到聊天区上/下边缘时启动自动滚动 tick 链，
				// 停住不动也能持续滚动，选区延伸到可视范围之外。
				if cmd := m.updateMouseSelectionAutoScroll(mouseMsg.X, mouseMsg.Y); cmd != nil {
					return m, cmd
				}
				return m, nil
			case mouseMsg.Action == tea.MouseActionRelease:
				// Bubble Tea represents a terminal button release with
				// MouseButtonNone; use the release coordinates as the final end.
				m.updateMouseSelection(mouseMsg.X, mouseMsg.Y)
				cmd := m.finishMouseSelection()
				return m, cmd
			}
		}

		// 输入框拖选（composer_mouse.go）：motion 扩展选区（光标随指针），
		// release 以最终坐标收尾并保留选区。事件不转发 textarea/viewport。
		if m.composerMouseDrag.dragging {
			switch {
			case mouseMsg.Action == tea.MouseActionMotion:
				m.extendComposerMouseDrag(mouseMsg.X, mouseMsg.Y)
				return m, nil
			case mouseMsg.Action == tea.MouseActionRelease:
				m.extendComposerMouseDrag(mouseMsg.X, mouseMsg.Y)
				m.endComposerMouseDrag()
				return m, nil
			}
		}

		// 整屏拖选（无需 Shift）：按住左键拖动即选择，松开自动复制；Shift+拖动同样
		// 走此路径（兼容旧习惯）。批 C / C-2 后，toast/对话区/Monitor 摘要/状态栏
		// 以及**面板态**（确认框/选择面板/文档导航）都在同一套屏幕坐标里可选；
		// 普通输入态的 textarea/按钮/补全弹窗仍走原路由（点击行为完全不变）。
		if mouseMsg.Action == tea.MouseActionPress && mouseMsg.Button == tea.MouseButtonLeft {
			if m.beginMouseSelection(mouseMsg.X, mouseMsg.Y, l, chatAreaTop, inputAreaTop) {
				// 焦点切换：点可选文本区时 blur textarea，让用户清楚焦点已切走。
				// 仅当焦点不在 Send/Stop 按钮时才 blur，避免破坏 Tab 焦点态。
				if !m.uiService.InputFocusedOnSend() && !m.uiService.InputFocusedOnStop() && m.uiService.Textarea().Focused() {
					m.uiService.Textarea().Blur()
				}
				return m, nil
			}
			m.clearMouseSelection()
			if mouseMsg.Y >= inputAreaTop {
				// 5 维度审查修复：btnRowY 计入 lastPreviewHeight。
				// 输入区布局（render_extras.go:55-65）：preview(上方) → textarea → btnRow → popup(下方)。
				// 按钮行 Y = 输入区顶部 + previewHeight + textarea.Height + inputBoxStyle 垂直边框。
				// 原公式漏 previewHeight，导致排队输入预览显示时按钮点击落点偏移。
				btnRowY := inputAreaTop + m.lastPreviewHeight + m.uiService.Textarea().Height() + inputBoxStyle.GetVerticalFrameSize()
				btnRowHeight := lipgloss.Height(renderInputActionRow(m.uiService.Width()-4,
					m.sessionService.State() != StateIdle,
					m.uiService.InputFocusedOnStop() || m.uiService.InputFocusedOnSend(), m.pressedBtn))
				if mouseMsg.Y >= btnRowY && mouseMsg.Y < btnRowY+btnRowHeight {
					// 档B：按下**只**登记按下态（按钮立即以 active 样式高亮），
					// 开火推迟到 release 分支 —— 旧实现「按下即开火」对 send 而言
					// 按下态永远渲染不到（同一帧 state 翻成 Streaming、按钮换成 Stop），
					// 「点下去有反馈」在 send 上结构性地不可见。
					if m.sessionService.State() == StateStreaming || m.sessionService.State() == StateToolExec {
						// Stop button press area
						btnView := renderActionButton(m.uiService.Width()-4, true, false, m.pressedBtn)
						btnWidth := lipgloss.Width(btnView)
						btnX := m.uiService.Width() - 4 - btnWidth
						if mouseMsg.X >= btnX && mouseMsg.X < btnX+btnWidth {
							m.pressedBtn = btnIdentityStop
							return m, nil
						}
					} else if m.sessionService.State() == StateIdle {
						// Send button press area
						btnView := renderActionButton(m.uiService.Width()-4, false, false, m.pressedBtn)
						btnWidth := lipgloss.Width(btnView)
						btnX := m.uiService.Width() - 4 - btnWidth
						if mouseMsg.X >= btnX && mouseMsg.X < btnX+btnWidth {
							m.pressedBtn = btnIdentitySend
							return m, nil
						}
						copyWidth := lipgloss.Width(renderCopyAllButton(btnX-1, m.pressedBtn == btnIdentityCopy))
						if copyWidth > 0 && mouseMsg.X >= 0 && mouseMsg.X < copyWidth {
							m.pressedBtn = btnIdentityCopy
							return m, nil
						}
					}
				} else {
					// 焦点切换 + 光标定位：点击输入框区域（非按钮行）时 focus textarea
					// 并把光标定位到点击的字符位置。仅当焦点不在 Send/Stop 按钮时处理，避免覆盖 Tab 焦点态。
					// 修复"点击输入框无光标反应"：textarea v0.20.0 不处理 MouseMsg，需手动 SetCursor；
					// 且 Focus() 返回的 blink Cmd 必须回收以重启光标闪烁动画。
					if !m.uiService.InputFocusedOnSend() && !m.uiService.InputFocusedOnStop() {
						if cmd := m.uiService.Textarea().Focus(); cmd != nil {
							cmds = append(cmds, cmd)
						}
						m.setCursorByMouseX(mouseMsg.X)
						// 输入框鼠标拖选（composer_mouse.go）：精确定位光标（含多行）
						// 并记录锚点；映射失败时保持上方 setCursorByMouseX 的降级定位。
						m.beginComposerMouseDrag(mouseMsg.X, mouseMsg.Y)
					}
				}
			} else {
				// 状态栏区域（对话区上方的点击）：维持 blur + viewport 转发。
				// bubbles viewport 只响应滚轮 press，此处转发无副作用。
				if !m.uiService.InputFocusedOnSend() && !m.uiService.InputFocusedOnStop() && m.uiService.Textarea().Focused() {
					m.uiService.Textarea().Blur()
				}
				*m.uiService.Viewport(), vpCmd = m.uiService.Viewport().Update(msg)
				cmds = append(cmds, vpCmd)
			}
		} else if mouseMsg.Action == tea.MouseActionRelease {
			// 档B：松开才开火（按下时已高亮，见上方 press 分支）。
			// 按「按下时登记的按钮」开火，**不**按 release 坐标重新命中 —— 终端上报的
			// release 常带 MouseButtonNone、坐标也可能被 clamp 出窗口，重新命中会把
			// 「按下后抖一像素」误判成取消（GUI 也不这么干：进入按钮即锁定本次点击）。
			switch btn := m.pressedBtn; btn {
			case btnIdentityCopy:
				m.pressedBtn = ""
				return m, m.copyConversation("all")
			case btnIdentitySend:
				m.pressedBtn = ""
				return m.sendInput()
			case btnIdentityStop:
				m.pressedBtn = ""
				return m.stopExecution()
			}
			// 无登记按下的裸 release（拖选的松开已被上方两个状态机消费）⇒ 落到底部丢弃。
		} else if wheelDirection(mouseMsg) != 0 {
			m.clearMouseSelection()
			// Route wheel events by their actual visual bounds. The preview,
			// action button, and completion popup must not scroll either pane.
			if mouseMsg.Y < inputAreaTop {
				// 2026-10-06（P2）：**面板期间对话区不响应滚轮** —— 确认/选择面板是模态，
				// 视口只接受显式导航键（见 viewportNavKey）。
				//
				// 修前滚轮不经任何 state 分流：等待确认时照样能滚，而键盘却被面板分支末尾的
				// 裸 return 吞掉 ⇒ 「能滚离底部、回不来」。用户在等待期间（哪怕无意地）一碰
				// 触控板，视口就离开底部；批准后的新内容按 口径（非底部不回跳）落在
				// 屏幕外，界面看起来"退回了上一屏"—— 这正是手测报障的那一格。
				//
				// ★ 不是"把能力关掉"：键盘侧已由 P1 放行（PgUp/PgDn/Ctrl+↑↓/Home/End 面板期间
				// 全部可用），面板期间仍可自由浏览历史，只是入口从"随手滚"收窄为"显式导航"。
				if !m.decisionPanelActive() {
					*m.uiService.Viewport(), vpCmd = m.uiService.Viewport().Update(msg)
					cmds = append(cmds, vpCmd)
				}
			} else {
				textareaTop := inputAreaTop + m.lastPreviewHeight
				textareaBottom := textareaTop + m.uiService.Textarea().Height() + inputBoxStyle.GetVerticalFrameSize()
				if mouseMsg.Y >= textareaTop && mouseMsg.Y < textareaBottom {
					cmds = append(cmds, m.scrollComposer(wheelDirection(mouseMsg)))
				}
			}
		}
		// 删除原 `else if mouseMsg.Button != tea.MouseButtonRight` 兜底分支。
		// 该分支无条件转发 motion/release 等事件给 viewport，导致输入区移动鼠标时对话区误滚动。
		// 未识别的鼠标事件（motion/release 等）一律丢弃。
	} else if _, ok := msg.(tea.KeyMsg); !ok {
		*m.uiService.Viewport(), vpCmd = m.uiService.Viewport().Update(msg)
		cmds = append(cmds, vpCmd)
	}

	return m, tea.Batch(cmds...)
}

// viewportNavHint 是「某个视口导航动作该怎么告诉用户」的**唯一声明**：把 bubbletea 的键名
// （= `msg.String()`，生产按键链实际匹配的那个值）与**本平台物理键盘上真实能按出的写法**
// 绑在同一处。
//
// 为什么必须绑在一起（P3，2026-10-06）：状态栏那句 `⏬ Unread: N below (End)` 是全应用
// **唯一**教你回到底部的提示，但 `(End)` 是**第二处**手写「回底用哪个键」—— 与
// `viewportNavKey` 的 `case "end"` 各写各的（「同一语义两处实现」同一族）。
// 后果已经真实发生：mac 笔记本**没有独立的 End 键**（End = Fn+→），于是这条唯一的提示
// 在用户手上的这台机器上**按不出来** —— 用户反馈"批准后回不到最新"。
//
// 绑在一处后：`Key` 由判据驱动**生产入口**（`Update`）验证「这个键真的能跳到底」，
// 文案与行为任何一侧漂移都会红，提示不可能再对着一个失效的键说话。
type viewportNavHint struct {
	Key   string // bubbletea msg.String() 值 = 生产按键链实际匹配的键名
	Label string // 本平台物理键盘上给用户看的写法（不含空格，便于状态栏单行排布）
}

// jumpToBottomHintFor 是「跳到最新」提示的**纯函数**形态（按 goos 参数化，便于跨平台判据）。
//
// 与本包既有约定同形：`scrollbackSupportFor(..., runtime.GOOS)` + 薄包装 `DetectScrollbackSupport`
// —— 平台逻辑收进纯函数，包装层只读 `runtime.GOOS`，判据才能在一台机器上验证全部平台分支。
//
// Key 两端都是 "end"：`Fn+→` 与 `End` 送去的是**同一个** ESC 序列，bubbletea 都解成 "end"，
// 因此平台差异只在**键盘标注**上，行为面零分叉。非 darwin 平台保留 End（Windows / Linux
// 终端键盘上都有独立 End 键）。
func jumpToBottomHintFor(goos string) viewportNavHint {
	if goos == "darwin" {
		return viewportNavHint{Key: "end", Label: "Fn+→"}
	}
	return viewportNavHint{Key: "end", Label: "End"}
}

// jumpToBottomHint 读真实平台，供渲染层使用。
func jumpToBottomHint() viewportNavHint { return jumpToBottomHintFor(runtime.GOOS) }

// viewportNavKey 执行**唯一的**对话区视口导航实现（纯滚动键；不含 up/down —— 那两个键在
// 不同状态下语义不同：确认/选择面板里选选项、idle 里翻输入历史）。
//
// 为什么必须收进共享函数（本仓 「双实现」复发坑）：这些键要在**四条**路径上都能按 ——
//
//	① 主键位表（idle 态，主 switch）；
//	② 运行态（Streaming/ToolExec 的非 idle 通用分流）；
//	③ 确认面板（StateWaitingConfirm：主会话 / 子代理 / elicitation）；
//	④ 选择面板（StateWaitingChoice）。
//
// 修前 ①②③④ 各写一份 `case "pgup": HalfViewUp()` 链，任何一处按键改动都会漏其余，
// 于是同一个物理键在不同状态下行为分叉。收进本函数后，各调用点只做**薄转发**。
//
// 返回 true 表示该键已消费（调用方直接 `return m, nil`）。
func (m *Model) viewportNavKey(key string) bool {
	switch key {
	case "pgup":
		m.uiService.Viewport().HalfViewUp()
	case "pgdown":
		m.uiService.Viewport().HalfViewDown()
	case "ctrl+up":
		m.uiService.Viewport().LineUp(3)
	case "ctrl+down":
		m.uiService.Viewport().LineDown(3)
	case "home":
		m.uiService.Viewport().GotoTop()
	case "end":
		m.uiService.Viewport().GotoBottom()
	default:
		return false
	}
	return true
}

// decisionPanelActive 报告当前是否有「等待用户决策的面板」在位（确认 / 选择 / elicitation）。
//
// 唯一判据 = 状态与面板数据**同时在位** —— 与渲染分流（layout.go renderInputAreaForState）
// 及高度预算（recalcViewportHeight 的 vpHeight 扣减）的进入条件**同源**，不另立第二权威。
//
// 用途只有一处：面板期间**滚轮不改动对话区视口**（面板是模态，视口只接受显式导航键）。
// 修前滚轮走 MouseMsg，不经任何 state 分流 ⇒ 滚轮能滚、键盘却被面板分支的裸 return 吞掉，
// 形成「能滚离底部、回不来」的不对称 —— 用户在等待确认期间（哪怕无意）让视口离开底部，
// 批准后的新内容按 口径（非底部不回跳）落在屏幕外，界面看起来"退回了上一屏"。
func (m *Model) decisionPanelActive() bool {
	switch m.sessionService.State() {
	case StateWaitingConfirm:
		return m.toolService.PendingConfirm() != nil
	case StateWaitingChoice:
		return m.toolService.PendingChoice() != nil
	default:
		return false
	}
}

// wheelDirection normalizes Bubble Tea's current Button-based wheel events and
// its deprecated Type field, which is still populated by some terminals.
// It returns -1 for up, 1 for down, and 0 for non-wheel events.
func wheelDirection(msg tea.MouseMsg) int {
	switch msg.Button {
	case tea.MouseButtonWheelUp:
		return -1
	case tea.MouseButtonWheelDown:
		return 1
	}
	switch msg.Type {
	case tea.MouseWheelUp:
		return -1
	case tea.MouseWheelDown:
		return 1
	default:
		return 0
	}
}

// View renders the conversation and composer, with the status footer fixed
// at the bottom so it remains visible while history scrolls.
func (m Model) View() string {
	// N-MOUSE-ROW：视图与屏幕几何账本同源（layout.go buildView）。
	// 手工拼区块 + 另处估算坐标的做法会让两者漂移 —— 漂移量正好是 bubbletea
	// 从顶部砍掉的行数，表现为「鼠标选这里、复制到上面」。
	view, _ := m.buildView()
	return view
}

// setViewportContent 更新对话视口内容，并镜像一份快照到 viewportContent，
// 供鼠标拖选复制全量内容（含滚出屏幕的历史行）。
func (m *Model) setViewportContent(content string) {
	m.uiService.Viewport().SetContent(content)
	m.viewportContent = content
	// 无 source map 的裸写入（流式尾部 / 测试夹具）：清空上一次快照，
	// 拖选退回"按可见字面复制"（fail-soft，不产生错位归属）。
	m.viewportSources = nil
	m.viewportOwner = nil
	m.viewportLogical = nil
}

// refreshTranscriptViewport 是「重渲染转录 → 折行 → 写入视口 + 登记 source map」的
// **唯一入口**。
//
// 它把原先散落各处的 `setViewportContent(wrapViewportContent(renderMessages()))`
// 三连收敛成一句，并保证 **文本 / 折行 / source map 三者同源同快照** ——
// 这是批 C 的核心：复制的真值不再由屏幕（有损投影）定义，而由渲染层吐出的 source map
// （行 → 真值）定义（见 transcript_source.go）。
func (m *Model) refreshTranscriptViewport() {
	text, logicalSrcs := m.renderTranscript()
	wrapped, visualSrcs, owner := m.wrapTranscriptWithMap(text, logicalSrcs)
	m.setViewportContentSourced(wrapped, visualSrcs, owner, strippedLines(text))
}

// strippedLines 把多行文本按行剥 ANSI 并去掉行尾空白。
//
// 逻辑行文本要供**按显示列切片**用（displaySlice 逐 rune 计宽），必须与
// mouse_selection.go 的 screenDoc 可见行同口径（已剥 ANSI、忽略行尾空白），
// 否则转义序列会被当成可打印字符参与列宽计算，切片错位。
func strippedLines(text string) []string {
	lines := strings.Split(text, "\n")
	for i, l := range lines {
		lines[i] = strings.TrimRight(ansi.Strip(l), " ")
	}
	return lines
}

// setViewportContentSourced 同 setViewportContent，但一次性写入整张 source map。
func (m *Model) setViewportContentSourced(wrapped string, visualSrcs []lineSource, owner []int, logical []string) {
	m.uiService.Viewport().SetContent(wrapped)
	m.viewportContent = wrapped
	m.viewportSources = visualSrcs
	m.viewportOwner = owner
	m.viewportLogical = logical
}

// invalidateStreamHistoryCache 失效流式历史渲染缓存。
//
// ★ 文本与其 source map 必须**一起**失效：只清文本不清来源，会出现"文本是新的、
// 来源是旧的"的错位归属（比不做更糟）。故收口成一个函数，杜绝三处各清一半。
func (m *Model) invalidateStreamHistoryCache() {
	m.streamCachedHistory = ""
	m.streamCachedMsgCount = 0
	m.streamCachedHistorySrcs = nil
}

// refreshStreamingViewport 写入**流式帧**的视口内容 + source map。
//
// 历史部分的来源取自 streamCachedHistorySrcs（与实时登记同源，随历史缓存一起失效）；
// **流式尾部按可见字面**（尾部是活动轮、尚未定稿，其省略行由定稿路径登记）。
// 这样拖选在流式期间也能正确还原历史里的省略行，不至于"流式一开始就退化"。
func (m *Model) refreshStreamingViewport(history string, historySrcs []lineSource, tail string) {
	content := history + "\n" + tail
	logicalSrcs := make([]lineSource, 0, len(historySrcs)+lineCountOf(tail))
	logicalSrcs = append(logicalSrcs, historySrcs...)
	for i := 0; i < lineCountOf(tail); i++ {
		logicalSrcs = append(logicalSrcs, lineSource{})
	}
	wrapped, visualSrcs, owner := m.wrapTranscriptWithMap(content, logicalSrcs)
	m.setViewportContentSourced(wrapped, visualSrcs, owner, strings.Split(content, "\n"))
}

// addMessage 追加显示消息；仅当用户原本位于底部时跟随新消息，否则保留浏览位置。
//
// **非底部时必须留下可见痕迹**。原实现在非底部时只是把新内容追加到屏幕之外
// （`SetYOffset(offset)` 保持原位置）且**一句提示都不给** ⇒ 用户敲完命令看到界面毫无变化，
// 会误判成"命令没生效 / 历史丢了"（2026-09-23 实测复现：该帧只发出 1255 字节）。
//
// 分层口径（单点，不逐调用点枚举 —— addMessage 是全部 ~60 个写入点的唯一收口）：
// - 一律**不**强制回跳（`GotoBottom()` 会打断正在读历史的人，是倒退）；
// - 凡落在屏幕下方的新消息，一律计入 unreadBelow，由状态栏角标显示（见 renderStatusBar）。
//
// 清零不在这里：只有一个条件 —— 视口回到**底部**（Update 顶部集中判定）。
// - 作者一律按 **AuthorTurn** 记：本函数的调用点全是「本轮产出」。
// 回合之外的异步产物必须显式走 addMessageFrom(AuthorAsync, …)，不得借道本函数。
func (m *Model) addMessage(msg string) {
	m.addMessageFrom(AuthorTurn, msg)
}

// Author 标识一条显示消息的**作者归属**（A-1，2026-09-30）。
//
// 为什么必须进签名：收口 `addMessage` 原来的签名只有 `string` ⇒
// 「谁写的」「属于哪一轮」在**类型上不可表达**，于是作者身份无从校验，
// 任何异步事件都自动获得与「模型这一轮说了什么」同样的地位。
// 本仓既有判据（审批三问 / R4 结构化事实优先）同一条道理：
// 可判别的事实应由**结构**承担，而不是由措辞约定承担。
type Author int

const (
	// AuthorTurn 是本轮对话的作者：用户输入回显、模型输出、工具结果、命令回执。
	// 它只在 turn 内产生。
	AuthorTurn Author = iota

	// AuthorAsync 是**回合之外**的异步产物：后台 Monitor 的 output/done、
	// 定时循环触发等。
	//
	// 它与 turn 的关系是"不同步"的：进程时钟连续、对话时钟回合制，两者不可能对齐
	// （对标报告 §2 约束 C2）。因此 Async 作者**不得**在 turn 已结束时取得
	// 主对话流的写入权 —— 这正是 同类 CLI「输出只能经工具结果进」与
	// 同类 CLI「输出只落盘、主界面零输出」共同守住的那条不变量。
	AuthorAsync
)

// String 给日志/判据失败信息一个可读名字。
func (a Author) String() string {
	switch a {
	case AuthorAsync:
		return "async"
	default:
		return "turn"
	}
}

// turnBoundary 是「本轮是否已结束」的唯一结构承载体（A-3，2026-09-30）。
//
// 改造前：`finishTurn` 通过 `addMessage(formatTurnCompleteBoundary())` 追加**又一条消息**
// 来表达"回合结束" ⇒ 它与 Monitor 输出**走同一函数、同一条流**，物理上管不住随后到达的
// 任何写入。whale 有「回合结束」的**字**，没有「回合结束」的**结构**。
//
// 改造后：回合结束是一次**状态迁移**，只在 closedAt/seq 留痕，**不占消息身份**；
// 轮末分隔线由渲染侧从本结构的 marks 逐条投影出来（见 displayMessages）。
// 判据：删除 seq/closedAt 的赋值 ⇒ 轮末标记消失且测试转红；删除字段 ⇒ 编译失败。
type turnBoundary struct {
	// seq 是本会话已**落地**的轮次数（finishTurn 递增）。
	// 它同时是"是否有过回合"的判据：seq==0 表示还没有任何一轮结束过。
	seq int

	// closedAt 是最近一次回合结束的时刻；零值表示尚未有回合结束。
	// 轮末标记的 HH:MM:SS 取自它 —— 而不是渲染时刻的 time.Now()，
	// 否则标记会随重绘"跳动"，也不可被测试固定。
	closedAt time.Time

	// marks 是**每轮结束固化一条**的历史边界（2026-10-06 新增）。
	//
	// 为什么必须是列表而不是单个时刻：单个 closedAt 只能描述**最后一轮**，
	// 于是历史里每一轮的边界都无处安放 —— 用户回看长会话时“每轮没有分界”
	// （A-3 的副作用，2026-10-06 用户报障）。列表让每一轮都留下自己的锚点。
	//
	// 它**只进显示侧**（由 displayMessages 投影），永不进 sessionService.Messages()
	// 或模型侧 session（不变式 I5；判据 TestTurnBoundary_NotPersistedAsMessageOrModelContent）。
	marks []turnMark
}

// turnMark 是一条**已落地**的轮末边界：于 at 结束（normal / interrupted），
// 结束位置是显示缓冲第 after 条之后。
//
// after 的语义 = closeTurn 被调用时 len(sessionService.Messages())。
// 该前提在生产上成立：正常路径 handleStreamDone 先 addMessage(本轮 assistant 正文)、
// 末尾才 finishTurn → closeTurn；中断路径在补完部分输出显示消息后才 closeTurn
// （stream.go 的 stopExecution），故此刻本轮消息均已进入显示缓冲。
// displayMessages 用它把每条边界**插回原位**，而不是把所有边界堆在末尾 ——
// 后者在回看时定不了位，等于没做（判据 …EachTurnLeavesPersistentMark 的位置断言）。
//
// ⚠️ 已知边界（如实标注）：messageBuffer 是 FIFO（defaultMaxMessages=10000，
// 2026-10-08 由 1000 提高 —— 1000 时长会话数百轮即触顶，「历史对话消失」，用户真机报障拍板）。
// 一旦会话超过上限、最旧的消息被裁剪，**被裁掉那段之前**的边界 after 锚点会整体前移而无法校正
// （displayMessages 只能防御“after 超出当前长度”，无法感知“缓冲整体前移”）。
// 真实影响面：单会话 >10000 条显示消息之后，最早期的边界位置可能漂移。
// 修正需给 messageBuffer 记累计淘汰数（绝对序号），本轮未做 —— 超出本次“每轮留锚点”的范围。
type turnMark struct {
	at          time.Time
	after       int
	interrupted bool // true = 被用户中断（Stop / Ctrl+C），渲染为 ⏹；false = 正常结束，渲染为 ✓

	// changes 是**本轮真实提交的文件变更**（，2026-10-08 用户拍板）。
	//
	// 为什么挂在边界上、而不是又一条消息：与 A-3 同一条道理 —— 它是**回合的属性**，
	// 由渲染侧从 marks 投影成"结束标志正上方的一行摘要"。落成消息会同时污染
	// 显示缓冲与模型侧 session（不变式 I5），且用户在会话里"看到"一条自己没说过的内容。
	//
	// 取值语义（★ 两种"空"不同，别混）：
	// - nil = 取不到事实（filehistory 降级 / 锚点为空）⇒ 不显示卡片（不声称"无变更"）；
	// - 非 nil 且 FilesChanged==0 = 本轮确实没有文件变更 ⇒ 同样不显示（零噪音）。
	changes *diff.TurnDiff
}

// turnWriteOpen 报告主对话流**当前是否处于轮内**（A-1 的唯一授权判据）。
//
// 唯一权威 = 会话交互状态机（SessionModel.State）：只有 StateIdle 表示本轮已收口。
// ★ 不另立平行布尔字段：那会产生"两份状态各说各话"的第二权威（本仓 同形）。
// ★ 这不是新造的语义 —— handleLoopFire 早已用同一判据守护它自己的动作；
// 差别在于：那个入口**只**考虑了自己的到达时机，写入收口**从来没有**判据。
func (m *Model) turnWriteOpen() bool {
	return m.sessionService.State() != StateIdle
}

// turnHasEnded 报告是否存在"已结束的回合"（A-3 的可判别事实）。
//
// ★ 2026-10-06 起**仅判据消费**（零生产调用点，别再当死码删）：渲染侧已不再走它 ——
// 历史边界改由 m.turn.marks 逐条投影（见 displayMessages），"当前是否已收口"不再是渲染的开关。
// 保留它是为了 (1) 判据仍能钉住"回合确有结构承载体"（删掉 closeTurn 的赋值即转假），
// (2) turnWriteOpen() 之外的第二个可读事实 —— 它回答的是"有没有结束过的轮次"，与会话状态机互补。
func (m *Model) turnHasEnded() bool {
	return m.turn.seq > 0 && !m.turn.closedAt.IsZero()
}

// closeTurn 记录一次回合结束。**这是"回合结束"留痕的唯一承载体**。
// 它不写消息：调用方不得再用 addMessage 表达边界。
//
// interrupted 决定结束标志的符号（✓ 正常 / ⏹ 中断，见 formatTurnCompleteBoundary）。
// 正常收尾由 finishTurn(runHooks==true) 调用；中断轮由 stopExecution 直接调用 ——
// 这样"每一轮恰好一个结束标志"对两种收尾形态都成立（2026-10-07 用户拍板）。
//
// 因为标记改由状态投影（displayMessages），这里必须**主动刷新一次视口内容** ——
// 否则标记要等到下一次 addMessage 才现身，用户会看到"回合结束了但界面毫无反应"。
// 刷新口径与 addMessage 一致（不回跳、保持阅读位置），但**不计 unread**：
// 轮末标记不是一条消息，把它算进"你看不见的内容"会污染 的未读语义。
func (m *Model) closeTurn(at time.Time, interrupted bool) {
	m.turn.seq++
	m.turn.closedAt = at
	// 固化本轮的历史边界：after = 本轮消息在显示缓冲中的结束位置。
	// 必须在下面 renderMessages() **之前** append —— 否则本轮自己的边界要等下一次刷新才现身。
	//
	// 同一次 append 里带上**本轮的净变更**（卡片数据源）。
	// 只在这里采集 ⇒ 正常收尾与中断收尾（stopExecution）两条路径**都**拿到卡片，
	// 不需要各自实现一遍（那会变成"同一事实两处采集"， 同族）。
	m.turn.marks = append(m.turn.marks, turnMark{
		at:          at,
		after:       len(m.sessionService.Messages()),
		interrupted: interrupted,
		changes:     m.currentTurnChanges(),
	})

	followTail := m.uiService.Viewport().AtBottom()
	offset := m.uiService.Viewport().YOffset
	m.refreshTranscriptViewport()
	if followTail {
		m.uiService.Viewport().GotoBottom()
	} else {
		m.uiService.Viewport().SetYOffset(offset)
	}
}

// monitorEscape 是「done 事件不丢」那次阻塞发送的**逃生口**（A-5，2026-09-30）。
//
// 背景：`monitorCh` 是 256 缓冲，满了原本走 `default:` 静默丢弃。丢 output
// （采样）可辩护；丢 done（进程结束 = 状态迁移）**不可接受** —— 界面会永久停在
// running，而进程其实已退出，且没有任何后续事件能纠正它。
// 因此 done 改为"阻塞发送 + 逃生口"，写法对齐 internal/bridge/ws_transport.go:206-210
// （`select { case sendCh <- msg: … case <-closeCh: … }`，无 default）。
//
// 必须是指针语义：Model 按值复制（bubbletea Update 每次返回新值），
// 值字段会让"关闭"只作用于其中一份副本，真正被驱动的那份永远闭不了（与 programRef 同形）。
// 刻意**不放进 ToolModel 的值字段**，而是经指针持有，保证所有副本看到同一个逃生口。
type monitorEscape struct {
	once sync.Once
	ch   chan struct{}
}

func newMonitorEscape() *monitorEscape {
	return &monitorEscape{ch: make(chan struct{})}
}

// done 返回逃生通道；退出流程里被 close ⇒ 阻塞发送立即解开。
func (e *monitorEscape) done() <-chan struct{} {
	if e == nil {
		// 零值/未注入（旧测试构造的 Model）⇒ 返回永不关闭的通道。
		// 语义：不给逃生口，宁可让发送保持阻塞语义，也不假装"能逃"。
		return nil
	}
	return e.ch
}

// close 幂等关闭逃生口（多次退出路径都会走到，重复 close 会 panic）。
func (e *monitorEscape) close() {
	if e == nil {
		return
	}
	e.once.Do(func() { close(e.ch) })
}

// monitorStats 是异步通道的丢弃/边界计数（A-1/A-5，2026-09-30）。
//
// 改造前的形态（lifecycle.go 三处回调）是 `default:` **空分支** ⇒ 事件静默蒸发：
// 用户既看不到"少了什么"，也无从判断界面是否还跟得上。本仓对丢弃的一贯口径是
// 「可以丢，但必须可计数、可见」。这里按**严重性**分开计，而不是合成一个总数：
//
//	丢「采样」（output 刷新通知）：可辩护 —— 行本身仍在 Monitor 的有界权威副本里
//
// （DefaultMaxLines=10000），丢的只是"催重绘"的通知。
//
//	丢「状态迁移」（done = 进程结束）：**不可接受** —— 界面会永久停在 running，
//
// 而进程其实已退出，且没有后续事件能纠正它。
// 已改为不丢（阻塞发送 + 逃生口），故本计数正常恒为 0；
// 一旦非 0 即为真异常，必须暴露而不是咽掉。
//
//	丢「触发」（loopFire）：可辩护 —— 与 handleLoopFire 的既有取舍一致。
//
// ★★ 必须是指针语义（与 programRef / approvalAudit 同形，**不是**风格选择）：
// 三处递增都发生在 **NewModel 里注册的回调闭包**内，而闭包捕获的是 NewModel 的
// **局部 Model**；NewModel 又**按值返回** ⇒ 若这些计数是 Model 的值字段，
// 闭包里的 `m.x++` 会写进"调用它的那份局部副本"，真正被 tea.Program 驱动的那份
// **永远读到 0** —— 静默失效、界面上永远看不到任何丢弃（与 programRef 的 P0 事故完全同形）。
//
// 用 atomic：递增在 monitor 读进程 goroutine，读取在 tea 主循环（渲染 / /monitors 回执），
// 两者并发 ⇒ 普通 int 是数据竞争（-race 会报）。
type monitorStats struct {
	outputDropped atomic.Int64 // 丢采样：output 刷新通知
	doneDropped   atomic.Int64 // 丢状态迁移：done（正常恒 0）
	loopDropped   atomic.Int64 // 丢触发：loopFire
	asyncBlocked  atomic.Int64 // A-1 边界（turn 已结束）拦下的 Async 写入数
	outputSeen    atomic.Int64 // 被消费的 output 通知数
}

// stats 返回本 Model 的计数容器。
//
// 零值 Model（旧测试构造形态）未走 NewModel ⇒ 无容器。此时返回**临时**实例：
// 读恒为 0、写不保留。这是刻意的 —— 没有装配就没有计数，不假装有（R5 死代码声明）。
func (m *Model) stats() *monitorStats {
	if m.monitorStats == nil {
		return &monitorStats{}
	}
	return m.monitorStats
}

// droppedOutput 返回"被丢弃的输出刷新通知"数（丢采样）。
func (m *Model) droppedOutput() int { return int(m.stats().outputDropped.Load()) }

// droppedDone 返回"被丢弃的完成事件"数（丢状态迁移，正常为 0）。
func (m *Model) droppedDone() int { return int(m.stats().doneDropped.Load()) }

// droppedLoop 返回"被丢弃的循环触发"数。
func (m *Model) droppedLoop() int { return int(m.stats().loopDropped.Load()) }

// suppressedAsync 返回被 A-1 边界拦下的异步写入数。
func (m *Model) suppressedAsync() int { return int(m.stats().asyncBlocked.Load()) }

// consumedOutput 返回被消费的 output 通知数。
func (m *Model) consumedOutput() int { return int(m.stats().outputSeen.Load()) }

// addMessageFrom 是全部显示写入点的**唯一收口**，且**作者是签名的一部分**（A-1）。
//
// 硬规则：AuthorAsync 在 turn 已结束时**不得**写入主消息流。
// 被拦下的写入会被计数（asyncSuppressed），不让它变成"静默丢弃"——
// 本仓对丢弃的一贯要求是"可计数、可见"，见 A-5 的 dropped* 计数。
//
// ★ 这不是节流。节流只是把"每秒 1 条"变成"每 5 秒 1 条"，作者问题原封不动；
// 本函数判的是**有没有作者身份/在不在轮内**，与被拦下多少条无关。
func (m *Model) addMessageFrom(author Author, msg string) {
	m.addMessageFromWithSources(author, msg, nil)
}

// addMessageWithSources 追加一条**带 source map**的显示消息（批 C）。
//
// 凡渲染产地做过省略（思考预览 / 命令截断 / 工具结果折叠）的消息都走本入口，
// 把"这一行代表哪段原文"随内容一起存下来（见 transcript_source.go）。
func (m *Model) addMessageWithSources(msg string, sources []lineSource) {
	m.addMessageFromWithSources(AuthorTurn, msg, sources)
}

func (m *Model) addMessageFromWithSources(author Author, msg string, sources []lineSource) {
	if author == AuthorAsync && !m.turnWriteOpen() {
		m.stats().asyncBlocked.Add(1)
		return
	}
	followTail := m.uiService.Viewport().AtBottom()
	offset := m.uiService.Viewport().YOffset
	m.sessionService.AddMessageWithSources(msg, sources)
	// 这一行是**全量重排** —— renderMessages() 全量字符串拼接（O(会话长度)）
	// + wrapViewportContent() 对整篇做一次硬折行。它是「主循环里做 O(会话) 工作」
	// 的头号实例，必须有测量，否则"加一条消息要多久"无法回答。
	tRender := time.Now()
	m.refreshTranscriptViewport()
	perfReport("addMessageFrom.render", tRender, "")
	if followTail {
		m.uiService.Viewport().GotoBottom()
		// 在底部：新内容本来就看得见，角标保持清零。
		m.unreadBelow = 0
	} else {
		m.uiService.Viewport().SetYOffset(offset)
		// 非底部：内容落在屏幕下方 ⇒ 点亮角标（不回跳、不打断阅读）。
		m.unreadBelow++
	}
}

// setCursorByMouseX 根据鼠标 X 坐标定位 textarea 光标列。
// 修复"点击输入框无光标反应"——bubbles textarea v0.20.0 的 Update 不含
// case tea.MouseMsg，点击不会自动定位光标，需手动调用 SetCursor。
// X 偏移扣除 inputBoxStyle 左边框(RoundedBorder 1) + PaddingLeft(1) = 2 列。
// CJK 宽字符用 runewidth.RuneWidth 精确换算 cell→rune 索引。
// 已知限制：textarea v0.20.0 未导出当前行号 API（row 字段未导出），无法精确定位到
// 点击的多行中的具体行，这里按第一行计算光标列。单行输入场景准确；多行场景光标
// 会定位到第一行对应列（比"点击无任何反应"已显著改善）。
func (m *Model) setCursorByMouseX(mouseX int) {
	ta := m.uiService.Textarea()
	// 扣除输入框左边框 + padding
	xCell := mouseX - 2
	if xCell < 0 {
		xCell = 0
	}
	value := ta.Value()
	// 取第一行内容计算光标列（多行场景已知限制：光标定位到第一行）
	line := value
	if idx := strings.Index(value, "\n"); idx >= 0 {
		line = value[:idx]
	}
	col := 0
	accumWidth := 0
	for _, r := range line {
		if accumWidth >= xCell {
			break
		}
		accumWidth += runewidth.RuneWidth(r)
		col++
	}
	ta.SetCursor(col)
}

// isTUICommandHandledLocally 判断命令是否由当前 TUI Model 直接处理。
// 这些命令不得先执行 Registry Handler 再执行本地分支，否则会产生双重撤销、重复安装等副作用。
func isTUICommandHandledLocally(name string) bool {
	switch name {
	case "model", "models", "fast", "permissions", "status", "compact", "compact-strategy", "archive", "clear",
		"session", "session list", "session save", "session pick", "resume", "sessions", "export", "export-user", "export-format", "share",
		"think", "memory", "config", "tool", "name", "rewind", "monitors", "copy", "mouse",
		// 2026-09-30：/monitors 家族子命令登记为注册全名（供 `/` 弹窗提示）后，必须同步进本表。
		//
		// ★ 本表的语义是「**命令是否由本地实现处理**」，作用在 app.go 的
		// `if !isTUICommandHandledLocally(name) { cmd.Handler(args) }` 那道闸上：
		// 漏登记 ⇒ 注册侧 Handler 被**多执行一次**（副作用重复，同该函数文档所述）；
		// 注意其**输出不会**被用户看到 —— 紧随其后的 `switch name` 会无条件覆盖 output
		// （用 overlay 变体 M 实测确认过，别把"会看到未接线提示"写进判据）。
		"monitors stop", "monitors tail",
		"session switch", "session merge", "branch list", "commit", "worktree list", "worktree add",
		"worktree remove", "worktree prune", "diff", "plugin list", "plugin install", "plugin enable",
		"plugin disable", "reload-plugins", "sandbox", "sandbox status", "security-preset", "undo", "redo", "theme", "agents",
		"skills", "cron add", "cron list", "cron remove", "loop", "loop list", "loop cancel",
		"skillify", "simplify", "verify", "batch", "plan", "privacy", "stats", "summary", "thinkback",
		"usage", "break-cache", "tasks", "context", "commit-push-pr", "session-split",
		// mark/unmark/tag 已从本表移除（C-5，2026-10-10）：注册侧已补 Stub，走通用 cmd.Handler。
		"rename", "security-review", "env", "output-style", "terminal-setup",
		"mcp", "migrate", "checkpoint", "checkpoint save", "checkpoint list", "checkpoint restore",
		"checkpoint delete", "checkpoint show", "search", "task list", "task cancel", "task show",
		"notify list", "notify clear", "notify dismiss", "goal", "import", "workflow":
		return true
	default:
		return false
	}
}

func (m *Model) handleMouseCommand(args string) string {
	switch strings.ToLower(strings.TrimSpace(args)) {
	case "", "status":
		if m.mouseIdlePreference {
			return "Mouse capture: on (TUI clicks/wheel enabled)"
		}
		if m.mouseAutoDuringActive {
			return "Mouse capture: off while idle; temporarily on during AI output so Stop is clickable"
		}
		return "Mouse capture: off (terminal native selection enabled)"
	case "on":
		m.mouseEnabled = true
		m.mouseIdlePreference = true
		m.mouseAutoDuringActive = false
		m.pendingCommandCmd = m.mouseTransition(tea.EnableMouseCellMotion())
		return "Mouse capture enabled. Use /mouse off for terminal native selection."
	case "off":
		m.mouseEnabled = false
		m.mouseIdlePreference = false
		m.mouseAutoDuringActive = false
		m.pendingCommandCmd = m.mouseTransition(tea.DisableMouse())
		return "Mouse capture disabled. Select with the terminal, then copy with its native shortcut."
	default:
		return "Usage: /mouse on|off|status"
	}
}

// performClear 执行 /clear 的实际清空动作（C-4 抽出：无参数确认态按 y 与
// /clear --confirm 两条路径共用同一实现，防双实现漂移）。
func (m *Model) performClear() {
	m.sessionService.SetCurrentSession(session.NewSession())
	m.sessionService.SetMessages([]string{titleStyle.Render("Chat cleared")})
	m.sessionService.SetInputTokens(0)
	m.sessionService.SetOutputTokens(0)
	m.sessionService.SetSavedMsgCount(0)
	m.sessionService.SetSessionName("")
	m.sessionService.SetFirstUserInput("")
	m.refreshTranscriptViewport()
}

// handleClearConfirmKey 处理 /clear 二次确认态下的按键（C-4）：y 清空，其他键取消。
// 返回值型态与 Update 一致（值接收者流转）。
func (m Model) handleClearConfirmKey(key tea.KeyMsg) (Model, tea.Cmd) {
	m.pendingClearConfirm = false
	if key.Type == tea.KeyRunes && len(key.Runes) == 1 && (key.Runes[0] == 'y' || key.Runes[0] == 'Y') {
		m.performClear()
		return m, nil
	}
	m.addMessage(bi("Cancelled — conversation unchanged", "已取消，对话保持不变"))
	return m, nil
}

// handleCommand 解析并执行斜杠命令，确保每条命令只经过一个生产处理路径。
func (m *Model) handleCommand(input string) string {
	name, args := command.Parse(input)
	cmd, ok := m.cmdRegistry.Get(name)
	if !ok {
		suggestions := command.Suggest(name, m.cmdRegistry)
		if len(suggestions) > 0 {
			return fmt.Sprintf("Unknown: /%s\nDid you mean: /%s?", name, suggestions[0])
		}
		return fmt.Sprintf("Unknown: /%s\nType /help for all commands", name)
	}

	var output string
	if !isTUICommandHandledLocally(name) {
		var err error
		output, err = cmd.Handler(args)
		if err != nil {
			return errorStyle.Render(fmt.Sprintf("Command error: %v", err))
		}
	}

	switch name {
	case "model":
		output = m.handleModelCommand(args)
	case "models":
		output = ai.FormatModelList(m.sessionService.ModelName(), m.sessionService.AvailableModels(), m.aiService.ModelRegistry())
	case "fast":
		m.bindFastModeCallbacks()
		fastOutput, fastErr := cmd.Handler(args)
		if fastErr != nil {
			return errorStyle.Render(fmt.Sprintf("Command error: %v", fastErr))
		}
		output = fastOutput
	case "permissions":
		output = m.handlePermissionsCommand(args)
	case "status":
		output = m.formatStatus()
	case "mouse":
		output = m.handleMouseCommand(args)
	case "compact":
		output = m.handleCompact(args)
	case "archive":
		output = m.handleArchive(args)
	case "compact-strategy":
		output = m.handleCompactStrategy(args)
	case "clear":
		// C-4：Usage 写 /clear --confirm，实现此前不校验 —— 敲下立即清空
		// 不可逆上下文，行为与帮助文案相悖（假确认）。现补真闸：
		// 无参数 → 进入二次确认态（pendingClearConfirm），按 y 清空、其他键取消；
		// --confirm → 保持原语义立即清。
		if hasConfirmFlag := func() bool {
			for _, f := range strings.Fields(args) {
				if f == "--confirm" {
					return true
				}
			}
			return false
		}(); hasConfirmFlag {
			m.performClear()
			output = ""
		} else {
			m.pendingClearConfirm = true
			output = bi(
				"⚠️ This clears the entire conversation. Press y to confirm, any other key to cancel (or /clear --confirm to skip this prompt)",
				"⚠️ 这将清空整个对话上下文。按 y 确认清空，其他键取消（或直接 /clear --confirm 跳过确认）",
			)
		}
	case "session", "session pick":
		// 裸 /session = 交互式选择器（↑↓/Enter）。要文本列表用 /session list，
		// 要按名/ID 载入用 /session switch <id> 或 /resume <id>。
		output = m.handleSessionsPicker()
	case "session list":
		output = m.handleSessions()
	case "session save":
		output = m.handleSessionSave()
	case "resume":
		output = m.handleResume(args)
	case "sessions":
		output = m.handleSessions()
	case "export":
		output = m.handleExportCommand(args)
	case "export-user":
		output = m.handleExportUserCommand(args)
	case "export-format":
		output = m.handleExportFormat(args)
	case "share":
		output = m.handleShare(args)
	case "think":
		output = m.handleThink(args)
	case "memory":
		memOut, memErr := m.handleMemory(args)
		if memErr != nil {
			return errorStyle.Render(fmt.Sprintf("Memory error: %v", memErr))
		}
		output = memOut
	case "config":
		output = m.handleConfig(args)
	case "tool":
		// /tool [N] 从 session 权威源现查工具结果完整输出（切口②：不再依赖 lastToolResult 副本）。
		output = m.handleToolCommand(args)
	case "name":
		// /name 与 /rename 收敛到同一实现：历史上这里另写了一份「别名_时间戳」
		// 改名逻辑，与 handleRename 逐渐分叉（本仓已多次因双实现漂移吃过亏）。
		output = m.handleRename(args)
	case "rewind":
		output = m.handleRewind(args)
	case "checkpoint":
		output = m.handleCheckpoint("save " + args)
	case "checkpoint save":
		output = m.handleCheckpoint("save " + args)
	case "checkpoint list":
		output = m.handleCheckpoint("list")
	case "checkpoint restore":
		output = m.handleCheckpoint("restore " + args)
	case "checkpoint delete":
		output = m.handleCheckpoint("delete " + args)
	case "checkpoint show":
		output = m.handleCheckpoint("show " + args)
	case "search":
		output = m.handleSearch(args)
	case "monitors":
		output = m.handleMonitors(args)
	// 2026-09-30：家族子命令把**动词**补回给同一个本地实现（同构先例：`task cancel` → "cancel "+args）。
	// 注册全名让 `command.Parse` 切出 (name="monitors stop", args="<id>")，而 handleMonitors
	// 只认 `stop <id>` 形态 ⇒ 这里是两者之间唯一的重拼点。
	case "monitors stop":
		output = m.handleMonitors("stop " + args)
	case "monitors tail":
		output = m.handleMonitors("tail " + args)
	case "copy":
		output = m.handleCopy(args)
	case "session switch":
		output = m.handleSessionSwitch(args)
	case "session merge":
		output = m.handleSessionMerge(args)
	case "branch list":
		output = m.handleBranchList()
	case "commit":
		output = m.handleCommit()
	case "worktree list":
		output = m.handleWorktreeList()
	case "worktree add":
		output = m.handleWorktreeAdd(args)
	case "worktree remove":
		output = m.handleWorktreeRemove(args)
	case "worktree prune":
		output = m.handleWorktreePrune()
	case "diff":
		output = m.handleDiff(args)
	case "plugin list":
		output = m.handlePluginList()
	case "plugin install":
		output = m.handlePluginInstall(args)
	case "plugin enable":
		output = m.handlePluginEnable(args)
	case "plugin disable":
		output = m.handlePluginDisable(args)
	case "reload-plugins":
		output = m.handleReloadPlugins()
	case "sandbox":
		output = m.handleSandbox(args)
	// 2026-10-14：家族子命令把动词补回给同一个本地实现（同构先例：`monitors stop` → "stop "+args）。
	// 注册全名让 `command.Parse` 切出 (name="sandbox status") ⇒ 这里是两者之间唯一的重拼点。
	case "sandbox status":
		output = m.handleSandbox("status")
	case "security-preset":
		output = m.handleSecurityPreset(args)
	case "undo":
		output = m.handleUndoWithStack()
	case "redo":
		output = m.handleRedoWithStack()
	case "theme":
		output = m.handleTheme(args)
	case "agents":
		if args != "" {
			output = m.handleAgentSwitch(args)
		} else {
			output = m.handleAgents()
		}
	case "skills":
		if args != "" {
			output = m.handleSkillToggle(args)
		} else {
			output = m.handleSkills()
		}
	case "cron add":
		output = m.handleCronAdd(args)
	case "cron list":
		output = m.handleCronList()
	case "cron remove":
		output = m.handleCronRemove(args)
	case "loop":
		output = m.handleLoop(args)
	case "loop list":
		output = m.handleLoopList()
	case "loop cancel":
		output = m.handleLoopCancel(args)
	case "skillify", "simplify", "verify", "batch":
		// Prompt Skill command: inject rendered prompt as user message and trigger AI stream
		output = m.handleSkillPrompt(name, args)
	case "plan":
		output = m.handlePlan(args)
	case "privacy":
		output = m.handlePrivacy(args)
	case "stats":
		output = m.handleStats()
	case "summary":
		output = m.handleSummary(args)
	case "thinkback":
		output = m.handleThinkback(args)
	case "usage":
		output = m.handleUsage()
	case "break-cache":
		output = m.handleBreakCache(args)
	case "tasks":
		output = m.handleTasks(args)
	case "task list":
		output = m.handleTaskCommand("list")
	case "task cancel":
		output = m.handleTaskCommand("cancel " + args)
	case "task show":
		output = m.handleTaskCommand("show " + args)
	case "context":
		output = m.handleContext()
	case "commit-push-pr":
		output = m.handleCommitPushPR()
	case "session-split":
		output = m.handleSessionSplit(args)
	// P0-2 修复：删除 12 个桩 case（export-format/share/voice/ultraplan/bughunter×3/
	// version/upgrade/login/logout），让命令走 cmd.Handler（command 包已注册，
	// 回调在 lifecycle.go/runner_core.go 接线）。
	// version 桩删除同时解决 P0-3：统一使用 r.Version 单一来源（commands_core.go:15）。
	// mark/unmark/tag 桩 case 已删（C-5 守卫门修复，2026-10-10）：command 包从未注册 ⇒
	// Get() 先失败回 Unknown，case 永远走不到（死代码）。已改为注册侧补 Stub +
	// 删除本处 case 与 isTUICommandHandledLocally 名单项，诚实降级文案由
	// commands_core.go stubPlannedFeature 唯一提供。
	// 保留 rename 调用 m.handleRename（command 包 rename Handler 是桩，P0-4 待根治）。
	case "rename":
		output = m.handleRename(args)
	case "security-review":
		output = m.handleSecurityReview(args)
	case "env":
		output = m.handleEnvCmd(args)
	case "output-style":
		output = m.handleOutputStyle(args)
	case "terminal-setup":
		output = m.handleTerminalSetup()
	case "mcp":
		output = m.handleMcpCommand(args)
	case "migrate":
		output = m.handleMigrateCmd(args)
	case "notify list":
		output = m.handleNotifyList()
	case "notify clear":
		output = m.handleNotifyClear()
	case "notify dismiss":
		output = m.handleNotifyDismiss()
	case "goal":
		output = m.handleGoalCmd(args)
	case "import":
		output = m.handleImportSession(args)
	case "workflow":
		workflowOutput, workflowErr := m.handleWorkflow(args)
		if workflowErr != nil {
			return errorStyle.Render(fmt.Sprintf("Command error: %v", workflowErr))
		}
		output = workflowOutput
	}

	return output
}

// handleElicitationKeyMsg 处理 elicitation 审批状态下的按键事件
// 对标同类 CLI try_handle_shortcut + elicitation_options 的 Esc 硬编码 Cancel 契约
// （REF-外部-同类 CLI-3c7f013f9735, 1092-1131）
//
// 硬契约：Esc 键无论用户是否自定义键位，都强制映射为 DecisionCancel。
// 这是 elicitation 场景的安全 abort 路径，避免 "误以为是取消实际继续" 风险。
//
// 参数：
// - msg 按键消息
//
// 返回：处理后的 Model 与可能的 tea.Cmd。若返回 non-nil cmd 则表示已决策。
func (m Model) handleElicitationKeyMsg(msg tea.KeyMsg) (Model, tea.Cmd) {
	pc := m.toolService.PendingConfirm()
	if pc == nil {
		return m, nil
	}
	switch msg.String() {
	case "esc", "ctrl+c":
		// 硬编码：elicitation Esc = Cancel（对标同类 CLI 硬契约）
		return m.resolveElicitation(DecisionCancel)
	case "up", "k":
		if pc.SelectIdx > 0 {
			pc.SelectIdx--
		}
		return m, nil
	case "down", "j":
		if pc.SelectIdx < len(pc.Options)-1 {
			pc.SelectIdx++
		}
		return m, nil
	case "enter":
		if pc.SelectIdx < len(pc.Options) {
			return m.resolveElicitation(pc.Options[pc.SelectIdx].Decision)
		}
		return m, nil
	default:
		// 快捷键匹配（y/n 等）
		for i, opt := range pc.Options {
			if opt.Shortcut != "" && msg.String() == opt.Shortcut {
				_ = i // 索引仅用于调试，决策由 opt.Decision 决定
				return m.resolveElicitation(opt.Decision)
			}
		}
	}
	return m, nil
}

// resolveElicitation 将 elicitation 决策结果回传给 MCP client 并清理审批状态
// 对标同类 CLI handle_elicitation_decision
// （REF-外部-同类 CLI-3c7f013f9735）
//
// 第3轮修复（致命漏洞 #2）：pc.McpRequestID 类型为 json.RawMessage
// 原样透传到 ElicitationResponse.RequestID，由 ResolveElicitation 转换 string(RawID) 作为 router map key
//
// 参数：
// - decision 用户决策（Accept/Decline/Cancel）
//
// 返回：清理后的 Model 与发送响应的 tea.Cmd。
func (m Model) resolveElicitation(decision ApprovalDecision) (Model, tea.Cmd) {
	pc := m.toolService.PendingConfirm()
	if pc == nil {
		return m, nil
	}
	// delta F（H3）：MCP elicitation 的出口**不经过** confirmResultMsg（它发的是
	// elicitationResponseMsg，见本函数末尾）⇒ 必须在这里单独挂，否则 MCP 三态零留痕。
	// Tool 用固定的 "mcp_elicitation"；是哪个 server 在问由 McpServer 字段带出。
	m.recordApprovalDecision(bridge.OriginTUIElicitation, "mcp_elicitation", pc.McpMessage,
		decision, tools.SourceUser, pc.McpServerName)
	m.sessionService.SetState(StateIdle)
	m.toolService.SetPendingConfirm(nil)
	m.recalcViewportHeight()

	// 构造 MCP elicitation 响应消息
	// 第3轮修复：McpRequestID 是 json.RawMessage，原样透传到 ElicitationResponse.RequestID
	resp := mcp.ElicitationResponse{
		ServerName: pc.McpServerName,
		RequestID:  pc.McpRequestID,
		Action:     decisionToElicitationAction(decision),
	}
	return m, func() tea.Msg {
		return elicitationResponseMsg{response: resp}
	}
}

// decisionToElicitationAction 将内部 ApprovalDecision 映射为 MCP elicitation 动作
// 对标同类 CLI McpServerElicitationAction（Accept/Decline/Cancel）
// （REF-外部-同类 CLI-3c7f013f9735）
func decisionToElicitationAction(d ApprovalDecision) mcp.ElicitationAction {
	switch d {
	case DecisionApproveOnce:
		return mcp.ElicitationActionAccept
	case DecisionDeny:
		return mcp.ElicitationActionDecline
	default:
		return mcp.ElicitationActionCancel
	}
}

// approvalWaitLogIntervalSec 是 等待期周期日志的间隔（秒）。
// 拍板判据要求「等待 10s+ 日志能还原等待时长近似值」⇒ 10s 间隔下 10s+ 的等待
// 至少产生一条周期行，配合 start/end 行可还原近似时长。
const approvalWaitLogIntervalSec = 10

// approvalWaitTick 是 「等，但出声」的等待期 tick（每次 tickMsg 到达时调用）。
//
// 职责（只出声，零判定语义）：① 首个 tick 记等待开始 + 起始日志；
// ② 每过 approvalWaitLogIntervalSec 补一条带 elapsed_s 的周期日志；
// ③ 刷新面板显示秒数；④ 续订 tick。**不做**超时自决（拍板：等人是设计本意，Esc 随时取消）。
//
// 落点收口在 tick 而非各 SetPendingConfirm 出口的理由：确认框的创建点有 3 处
// （agent 工具 / pipeline / elicitation），逐处补开始日志必漏；tick 是等待期唯一
// 持续到达的事件，且本分支同时负责续订 —— 懒启动一处覆盖全部入口。
//
// ★ 期望 tick 真有航班：三个确认框创建点已各补一次 restartTickCmd（同批）。
//
// 值接收者安全（R1b-2 反面自查）：本方法只写 Model 的**值字段**与
// reassignServices 后指向本副本内嵌结构的 service 指针，且调用方原样返回同一副本。
func (m *Model) approvalWaitTick(now time.Time, seq uint64, pc *PendingConfirm) tea.Cmd {
	if m.approvalWaitStart.IsZero() || m.approvalWaitFor != pc {
		// 新一段等待（首帧，或上一段等待没等到 tick 收尾就换了 confirm ⇒ 先补收尾）
		if !m.approvalWaitStart.IsZero() {
			m.approvalWaitEnd(now)
		}
		m.approvalWaitStart = now
		m.approvalWaitFor = pc
		m.approvalWaitLastLog = 0
		m.approvalWaitSecs = 0
		m.approvalWaitTool = pc.ToolName
		whalelog.Info("[audit] approval: awaiting user approval",
			"phase", "approval_wait",
			"tool", pc.ToolName,
			"event", "start",
		)
	}
	elapsed := int(now.Sub(m.approvalWaitStart).Seconds())
	if elapsed != m.approvalWaitSecs {
		m.approvalWaitSecs = elapsed
		m.uiService.SetDirty(true) // 面板计时逐秒刷新
	}
	if elapsed-m.approvalWaitLastLog >= approvalWaitLogIntervalSec {
		m.approvalWaitLastLog = elapsed
		whalelog.Info("[audit] approval: awaiting user approval",
			"phase", "approval_wait",
			"tool", m.approvalWaitTool,
			"event", "waiting",
			"elapsed_s", elapsed,
		)
	}
	return tickCmd(seq)
}

// approvalWaitEnd 是 的等待收尾：落一条带总时长的结束日志并复位字段。
// 触发点 = tick 检测到「已不在等待态」或「confirm 已换人」（见 tickMsg 分支）。
func (m *Model) approvalWaitEnd(now time.Time) {
	total := int(now.Sub(m.approvalWaitStart).Seconds())
	if total < 0 {
		total = 0
	}
	whalelog.Info("[audit] approval: wait ended",
		"phase", "approval_wait",
		"tool", m.approvalWaitTool,
		"total_s", total,
	)
	m.approvalWaitStart = time.Time{}
	m.approvalWaitFor = nil
	m.approvalWaitSecs = 0
	m.approvalWaitLastLog = 0
	m.approvalWaitTool = ""
}

// restartTickCmd 启动新的活动状态 tick 世代，并使此前尚未到达的 tick 全部失效。
func (m *Model) restartTickCmd() tea.Cmd {
	m.tickSeq++
	return tickCmd(m.tickSeq)
}

// tickCmd 在流式或工具执行状态下调度指定世代的下一次 8ms 渲染检查。
//
// tick 仅在活动状态续订；内容 dirty 时最多等待 8ms，spinner 独立限制为 80ms 更新一次。
// REF-外部: 同类 CLI MIN_FRAME_INTERVAL=8_333_334ns
func tickCmd(seq uint64) tea.Cmd {
	return tea.Tick(8*time.Millisecond, func(t time.Time) tea.Msg {
		return tickMsg{at: t, seq: seq}
	})
}
