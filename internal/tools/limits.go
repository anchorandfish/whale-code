// Package tools — N16 tool limits 系统。
// 本文件定义 ToolLimits 执行限制与 TurnLimiter 每轮调用限制器，
// 解决对标分析 G-04/G-05（无 per-tool 限制 / 无 per-turn 计数）。
// 默认 MaxCallsPerTurn=0 表示不启用 per-turn 限制，不触发 N6 仲裁。
package tools

import (
	"fmt"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"
)

// H13-G4 分级输出限制常量。
//
// 对标同类 CLI 的 BASH_MAX_OUTPUT_LENGTH / TASK_MAX_OUTPUT_LENGTH /
// CLAUDE_CODE_MAX_OUTPUT_TOKENS 三档环境变量（REF-外部: @2026-03-31快照）。
// 每档有默认值 + 上限，环境变量值会被 clamp 到上限，防止用户设置过高导致 OOM。
const (
	// DefaultBashMaxOutput bash 工具默认最大输出字符数（50000）。
	//
	// ★ 2026-09-20 批 D收敛说明：本常量原写 30000，但**无任何消费点**
	// （唯一读者 LoadFromEnv 本身零生产调用）；而实际生效的默认值来自
	// `bash.go` 的 `defaultBashMaxOutput = 50000`（bashHandler 硬编码）。
	// 「同一语义两处声明且取值不同」= isWriteTool 型真分歧 ⇒ 本次收敛为**唯一声明**，
	// 取值取**实际生效的那个**（50000），以保持零行为变更；bash.go 的原常量已删除。
	DefaultBashMaxOutput = 50000
	// MaxBashMaxOutput bash 工具最大输出上限（200000），env 值超过此值被 clamp
	MaxBashMaxOutput = 200000
	// DefaultTaskMaxOutput task/agent 工具默认最大输出字符数（30000）
	DefaultTaskMaxOutput = 30000
	// MaxTaskMaxOutput task/agent 工具最大输出上限（1000000），env 值超过此值被 clamp
	MaxTaskMaxOutput = 1000000
	// DefaultMaxOutputTokens 默认最大输出 token 数（0 表示按模型配置）
	DefaultMaxOutputTokens = 0
	// MaxMaxOutputTokens 最大输出 token 上限（128000，覆盖 GPT-4 Turbo 的 128K 上下文）
	MaxMaxOutputTokens = 128000

	// EnvBashMaxOutput bash 工具最大输出字符数环境变量名
	EnvBashMaxOutput = "WHALE_BASH_MAX_OUTPUT"
	// EnvTaskMaxOutput task/agent 工具最大输出字符数环境变量名
	EnvTaskMaxOutput = "WHALE_TASK_MAX_OUTPUT"
	// EnvMaxOutputTokens 最大输出 token 数环境变量名
	EnvMaxOutputTokens = "WHALE_MAX_OUTPUT_TOKENS"

	// EnvWebFetchMaxResultChars WebFetch 结果上限字符数环境变量名（S4）。
	//
	// 与上面 H13-G4 三档**同族**：同一套「env 覆盖 → clamp → 默认值兜底」语义，
	// 不新造机制（新造一套就是"同一件事两个实现"的入口）。
	// 用途：G11 真机手测时把 WebFetch 上限降到 8000，压低测试期的上下文/token 消耗；
	// 不设置时生产默认仍是 PerToolLimits 的定稿值，行为零变化。
	EnvWebFetchMaxResultChars = "WHALE_WEBFETCH_MAX_RESULT_CHARS"
	// MaxWebFetchMaxResultChars WebFetch 结果上限的 env clamp 上限（200000）
	MaxWebFetchMaxResultChars = 200000
)

// 执行时长上限（，2026-09-20 批 D）。
//
// 与上面 H13-G4 的「输出上限」同族：同一套「默认值兜底 → 上界 clamp」语义。
// **单一声明在此**；消费点只有两个 —— bashHandler（前台）与 TaskManager.Create（后台）。
//
// 修复前的事实：前台只有下界（`<=0 ⇒ 120s`）、**没有上界**
// ⇒ `input.Timeout = 36000`（10h）会原样生效；后台把原始入参透传、`<=0` 走 `WithCancel`
// ⇒ `run_in_background` 不填 timeout = **永不过期**。
const (
	// DefaultBashTimeoutSeconds 前台 bash 默认超时秒数（未指定时）。
	// 与原 bash.go:824 的硬编码 `120 * time.Second` 同值 ⇒ 零行为变更。
	DefaultBashTimeoutSeconds = 120
	// MaxBashTimeoutSeconds 前台 bash 超时上界秒数（2h）。请求超过此值被 clamp。
	MaxBashTimeoutSeconds = 2 * 60 * 60
	// DefaultBackgroundTimeoutSeconds 后台任务默认超时秒数（未指定时）。
	// 修复前 `timeout <= 0` ⇒ `WithCancel(Background())` = 永久；本值是「非永久」的默认。
	DefaultBackgroundTimeoutSeconds = 10 * 60
	// MaxBackgroundTimeoutSeconds 后台任务超时上界秒数（2h）。请求超过此值被 clamp。
	MaxBackgroundTimeoutSeconds = 2 * 60 * 60
)

// ClampTimeoutSeconds 把请求的执行超时秒数收敛到「默认值兜底 ＋ 上界 clamp」。
//
// 唯一实现：前台 bash 与后台任务**共用同一套语义**，不许各写一遍（「双实现」复发坑）。
//
// - requested <= 0 ⇒ def（未指定时用默认值，保证「非永久」）
// - requested > max ⇒ max（上界，防止单条命令无限期占用）
// - 否则原值
//
// def <= 0 或 max < def 时按 def 兜底，保证结果恒为正且在 [def, max] 内。
func ClampTimeoutSeconds(requested, def, max int) int {
	if def <= 0 {
		def = 1
	}
	if max < def {
		max = def
	}
	switch {
	case requested <= 0:
		return def
	case requested > max:
		return max
	default:
		return requested
	}
}

// OutputLimits 分级输出限制配置（H13-G4）。
//
// 对标同类 CLI 的三档环境变量
// （REF-外部: src/screens/Doctor.tsx:145-154 @2026-03-31快照）。
// 每档有默认值 + 上限，LoadFromEnv 读取环境变量并 clamp 到上限。
// TTL 规则（方案 4.2 节 G4）：环境变量在 LoadFromEnv 调用时一次性读取并 clamp，
// 运行时不动态变更（避免会话中途变更破坏正在进行的工具调用）。
type OutputLimits struct {
	// BashMaxOutput bash 工具最大输出字符数（默认 30000，上限 200000，env WHALE_BASH_MAX_OUTPUT）
	BashMaxOutput int
	// TaskMaxOutput task/agent 工具最大输出字符数（默认 30000，上限 1000000，env WHALE_TASK_MAX_OUTPUT）
	TaskMaxOutput int
	// MaxOutputTokens 最大输出 token 数（默认 0=按模型配置，env WHALE_MAX_OUTPUT_TOKENS）
	MaxOutputTokens int
}

// DefaultOutputLimits 返回默认分级输出限制配置（H13-G4）。
//
// 不读取环境变量，仅返回默认值。生产代码应使用 LoadFromEnv 读取环境变量覆盖。
func DefaultOutputLimits() OutputLimits {
	return OutputLimits{
		BashMaxOutput:   DefaultBashMaxOutput,
		TaskMaxOutput:   DefaultTaskMaxOutput,
		MaxOutputTokens: DefaultMaxOutputTokens,
	}
}

// LoadFromEnv 从环境变量加载分级输出限制并 clamp 到上限（H13-G4）。
//
// 对标同类 CLI BASH_MAX_OUTPUT_LENGTH / TASK_MAX_OUTPUT_LENGTH /
// CLAUDE_CODE_MAX_OUTPUT_TOKENS 三档环境变量（REF-外部: @2026-03-31快照）。
//
// 读取顺序：环境变量值 → clamp 到上限 → 默认值兜底（env 未设置或解析失败时）。
// clamp 策略（方案 4.2 节 G4 TTL 规则）：
// - BashMaxOutput: clamp 到 [0, MaxBashMaxOutput]，env <= 0 时使用默认值
// - TaskMaxOutput: clamp 到 [0, MaxTaskMaxOutput]，env <= 0 时使用默认值
// - MaxOutputTokens: clamp 到 [0, MaxMaxOutputTokens]，env <= 0 时使用默认值（0=按模型）
//
// 返回：填充后的 OutputLimits。环境变量未设置时返回默认值（向后兼容）。
func LoadFromEnv() OutputLimits {
	limits := DefaultOutputLimits()
	if v := os.Getenv(EnvBashMaxOutput); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			if n <= 0 {
				// env 显式设为非正值时使用默认值
				limits.BashMaxOutput = DefaultBashMaxOutput
			} else if n > MaxBashMaxOutput {
				// clamp 到上限
				limits.BashMaxOutput = MaxBashMaxOutput
			} else {
				limits.BashMaxOutput = n
			}
		}
	}
	if v := os.Getenv(EnvTaskMaxOutput); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			if n <= 0 {
				limits.TaskMaxOutput = DefaultTaskMaxOutput
			} else if n > MaxTaskMaxOutput {
				limits.TaskMaxOutput = MaxTaskMaxOutput
			} else {
				limits.TaskMaxOutput = n
			}
		}
	}
	if v := os.Getenv(EnvMaxOutputTokens); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			if n < 0 {
				limits.MaxOutputTokens = DefaultMaxOutputTokens
			} else if n > MaxMaxOutputTokens {
				limits.MaxOutputTokens = MaxMaxOutputTokens
			} else {
				limits.MaxOutputTokens = n
			}
		}
	}
	return limits
}

// === 分级输出限制的运行时 SSOT（，2026-09-20 批 D）===
//
// 修复前：`LoadFromEnv()` 全仓**零调用** ⇒ 它读到的 3 个 env 写进一个没人读的结构体
// ⇒ 症状是「env 设了不生效」。本块把它变成**唯一读取出口**，并在启动早期一次性装载。

var (
	outputLimitsMu sync.RWMutex
	// outputLimits 是当前生效的分级输出限制。
	// 初始值 = DefaultOutputLimits()（env 未装载时的安全默认）。
	outputLimits = DefaultOutputLimits()
)

// LoadOutputLimitsFromEnv 在**启动早期**（env 加载完成后）调用一次，
// 把 env 派生的分级输出限制写入包级 SSOT，并返回装载结果。
//
// ★ 时机是语义的一部分：H13-G4 的 TTL 规则（见 LoadFromEnv 注释）要求
// 「一次性读取、运行时不动态变更」⇒ 必须在会话开始前调用，晚调用 = 语义错。
// 生产接线点 = main.go 的 `loadEnvFiles()`（本仓**唯一 env 咽喉点**，
// 主路径 / `whale doctor` / `whale --check-api` 三条入口都经它）。
func LoadOutputLimitsFromEnv() OutputLimits {
	ol := LoadFromEnv()
	outputLimitsMu.Lock()
	outputLimits = ol
	outputLimitsMu.Unlock()
	return ol
}

// GetOutputLimits 返回当前生效的分级输出限制（SSOT）。
//
// bash / task 输出上限的**默认值**从这里取（env 派生），不再各自硬编码常量。
func GetOutputLimits() OutputLimits {
	outputLimitsMu.RLock()
	defer outputLimitsMu.RUnlock()
	return outputLimits
}

// ToolLimits 定义工具的执行限制。
// MaxConcurrency=0 表示不限（受全局信号量 8 约束），=1 表示串行。
// MaxCallsPerTurn=0 表示不启用 per-turn 限制（向后兼容，不触发 N6）。
type ToolLimits struct {
	MaxCallsPerTurn int           // 每轮调用上限（0=不限）
	MaxConcurrency  int           // 并发上限（0=不限，1=串行）
	Timeout         time.Duration // 超时
	MaxResultChars  int           // 结果最大字符数（见 DefaultToolLimits 注释：web 系已接执行点，其余工具尚未）
}

// DefaultToolLimits 默认工具限制，对齐业内既有实现 toolLimits.ts 默认值。
//
// ★ MaxResultChars 的落地状态（2026-09-18 S4 更新，原文见）：
// - **web 系（WebFetch / WebSearch）已有真实执行点**：截断在 web 层（web.go），
// 读 GetToolLimits(...).MaxResultChars；工具描述亦由同一值渲染 ⇒ 声明 = 执行 = 文档。
// - 其余工具（bash / read_file / Agent…）**仍无 per-tool 截断执行点**：本字段对它们仍只是声明。
// 在 executor 出口统一截断会改变**全部**工具的返回长度（当前无截断）⇒ 属跨域行为变更，
// 已单独立项挂 ，**不要并进检索轮次**（§11.2 范围警告）。
//
// 数值 50000 对齐业内既有实现 DEFAULT_MAX_RESULT_SIZE_CHARS（原 2000 过小）。
var DefaultToolLimits = ToolLimits{
	MaxCallsPerTurn: 0,                // 默认不限
	MaxConcurrency:  0,                // 默认不限（受全局信号量 8 约束）
	Timeout:         60 * time.Second, // 默认 60s
	MaxResultChars:  50000,            // 对齐业内既有实现（原 2000 过小）
}

// PerToolLimits per-tool 限制覆盖表，键为工具名（区分大小写，与 registerBuiltinTools 注册名一致）。
// bash 串行（沙箱约束），Agent 并发上限 3（子代理开销大）。
var PerToolLimits = map[string]ToolLimits{
	"bash": {
		MaxCallsPerTurn: 0,
		MaxConcurrency:  1, // 串行（沙箱约束）
		Timeout:         120 * time.Second,
		MaxResultChars:  50000,
	},
	"read_file": {
		MaxCallsPerTurn: 0,
		MaxConcurrency:  0,
		Timeout:         30 * time.Second,
		MaxResultChars:  50000,
	},
	"WebFetch": {
		MaxCallsPerTurn: 0,
		MaxConcurrency:  0,
		Timeout:         30 * time.Second,
		// ★ S4从 50000 改为 30000：**不是行为变更，是把声明对齐到执行**。
		// 改造前 webFetchHandler 自己硬编码 30000 截断（web.go:323-324），此处却声明 50000
		// ⇒ 声明≠执行（B2 第（2）条）。同源化后上限只有一个出口；
		// 生产运行时行为 = 30000 保持不变（v0.2 P0 统一定稿时**只改这一行**，描述自动跟随）。
		MaxResultChars: 30000,
	},
	"WebSearch": {
		MaxCallsPerTurn: 0,
		MaxConcurrency:  0,
		Timeout:         15 * time.Second, // 对齐 doWebSearch 的 client.Timeout（web.go:408）
		// ★ S4 新增条目：改造前**不存在** ⇒ 落 DefaultToolLimits(50000)，而实现是「≤5 条」，
		// 三处数字互不相同（B2）。8000 = 裁定 D=① 定稿值；它是**防御性上限**（B5：
		// 5 条摘要通常 <1.5K 字符，生产路径几乎不命中，判据须用构造的超长 snippet 且如实标注）。
		MaxResultChars: 8000,
	},
	"Agent": {
		MaxCallsPerTurn: 0,
		MaxConcurrency:  3,
		Timeout:         300 * time.Second,
		MaxResultChars:  50000,
	},
}

// GetToolLimits 获取工具的限制（优先 per-tool 覆盖，否则返回默认值）。
// 未知工具返回 DefaultToolLimits，保证调用方总能拿到有效限制。
//
// ★ S4：WebFetch 的 MaxResultChars 在此叠加 env 覆盖，本函数因此成为**上限的唯一出口** ——
// 截断点（web.go）、工具描述（webToolConstraints）、Spec.Limits（tools.go）全都读它，
// 于是「声明 = 执行 = 文档」由结构保证，而不是靠三处文案人工同步（B2 /）。
func GetToolLimits(toolName string) ToolLimits {
	l, ok := PerToolLimits[toolName]
	if !ok {
		l = DefaultToolLimits
	}
	if toolName == "WebFetch" {
		l.MaxResultChars = webFetchMaxResultChars()
	}
	return l
}

// webFetchMaxResultChars 解析 WebFetch 的结果上限：env 覆盖（clamp 后）> PerToolLimits 定稿值。
//
// ⚠️ 这里**刻意每次读 env、不做一次性缓存**：本函数会在 **init()** 期被调用
// （toollevel.go 的档位快照会跑一遍 registerBuiltinTools），而那早于 main.go 的 loadEnvFiles()。
// 一次性缓存 = 在 env 尚未加载时把值钉死 ⇒ 正是 §11.1 记录的**同一个时序坑**。
// （H13-G4 的三档 env 可以安全地"一次性读取"，是因为它有 main 侧显式调用的 LoadFromEnv 时机
// ——★ 2026-09-20 批 D起该时机**真实存在**：`main.loadEnvFiles()` 调
// `LoadOutputLimitsFromEnv()`；此前该句只是设计意图，LoadFromEnv 零调用，注释与事实不符。
// 本函数的调用方全在 init / 构造路径上，没有那个时机。env 在进程启动后固定，重复读是稳定的。）
func webFetchMaxResultChars() int {
	base := DefaultToolLimits.MaxResultChars
	if l, ok := PerToolLimits["WebFetch"]; ok {
		base = l.MaxResultChars
	}
	raw := strings.TrimSpace(os.Getenv(EnvWebFetchMaxResultChars))
	if raw == "" {
		return base
	}
	n, err := strconv.Atoi(raw)
	if err != nil || n <= 0 {
		return base // 解析失败 / 非正值 ⇒ 默认值兜底（与 LoadFromEnv 同语义）
	}
	if n > MaxWebFetchMaxResultChars {
		return MaxWebFetchMaxResultChars // clamp（与 LoadFromEnv 同语义）
	}
	return n
}

// webToolConstraints 把上限渲染成 web 工具的 Constraints 段（★ B2：同一事实单一 SSOT）。
//
// 为什么由函数渲染、而不是把数字写进 Spec 字面量：v0.2 P0 把上限统一定稿时**只改 limits.go 一行**，
// 描述自动跟随 ⇒ 不会出现"改了执行忘了改文案"（v0.2 §5.1 的担心）。
// 反过来，描述里手写数字会让判据 T15（描述与常量同源；注入不等常量必须红）失去判别对象。
func webToolConstraints(toolName string) string {
	maxChars := GetToolLimits(toolName).MaxResultChars
	switch toolName {
	case "WebSearch":
		return fmt.Sprintf("Returns titles, URLs and short snippets only — never page bodies. "+
			"Total output is capped at %d characters; truncation is explicitly marked. "+
			"Use WebFetch when you need a specific page's full text.", maxChars)
	case "WebFetch":
		return fmt.Sprintf("Returns the page's plain text (HTML tags removed), not Markdown. "+
			"Output is capped at %d characters; truncation is explicitly marked.", maxChars)
	}
	return ""
}

// TurnLimiter 每轮工具调用限制器（可选启用）。
// Executor.turnLimiter 为 nil 时完全不介入执行路径（向后兼容）。
// 非 nil 时在 Execute 入口检查限制，出口记录调用，每轮 AI 响应后 Reset。
type TurnLimiter struct {
	mu     sync.Mutex
	counts map[string]int
}

// NewTurnLimiter 创建每轮工具调用限制器，初始化计数表。
func NewTurnLimiter() *TurnLimiter {
	return &TurnLimiter{counts: make(map[string]int)}
}

// CheckLimit 检查工具是否超过 per-turn 限制，超限返回 error。
// MaxCallsPerTurn=0 表示不限，直接返回 nil（默认行为）。
func (l *TurnLimiter) CheckLimit(toolName string) error {
	l.mu.Lock()
	defer l.mu.Unlock()
	limits := GetToolLimits(toolName)
	if limits.MaxCallsPerTurn > 0 && l.counts[toolName] >= limits.MaxCallsPerTurn {
		return fmt.Errorf("tool %s exceeded per-turn limit %d", toolName, limits.MaxCallsPerTurn)
	}
	return nil
}

// Reset 每轮 AI 响应后重置计数，开启新一轮统计。
func (l *TurnLimiter) Reset() {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.counts = make(map[string]int)
}

// Record 记录一次工具调用，递增 per-tool 计数。
func (l *TurnLimiter) Record(toolName string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.counts[toolName]++
}
