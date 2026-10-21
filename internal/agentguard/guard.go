package agentguard

import (
	"fmt"
	"log/slog"
	"sort"
	"strings"
)

// Verdict 是防线判定结果。
type Verdict int

const (
	// VerdictProceed 继续当前预算段（未触任何防线）。
	VerdictProceed Verdict = iota
	// VerdictExtend 收敛续跑：静默进入下一预算段（仅自主模式开启时）。
	VerdictExtend
	// VerdictStop 停机交回控制权（到限/空转/熔断），附进度快照。
	VerdictStop
	// VerdictRemind 软提醒：**不停机**，仅注入一条提示给模型 + 给用户可见提示。
	// P0-3 专用：其信号（「无外部效果」）只是空转的有偏代理，不可靠信号不应
	// 拥有停机这种不可逆权力；误报代价高（打断合法只读研究），故降级为提醒。
	VerdictRemind
)

// Decision 一次防线判定的结果。
type Decision struct {
	Verdict Verdict
	// Turns 当前预算段已消耗轮次。
	Turns int
	// Segment 当前预算段序号（1 起，续跑递增）。
	Segment int
	// Reason 判定原因（日志/提示用）。
	Reason string
	// Report VerdictStop 时非 nil（结构化进度快照）。
	Report *StopReport
	// Reminder VerdictRemind 时非空：注入模型的软提醒文本（双语：英文主行 + 中文辅助）。
	Reminder string
}

// StopReport 停机时输出的结构化进度快照（P4 职责分离：暂停点只做
// 进度快照，零 AI 生成成本；交接文档是独立的 /handoff 导出命令）。
type StopReport struct {
	// Cause 停机原因（轮次到限/空转/熔断/审查超限）。
	Cause string
	// TotalTurns 本次用户请求累计模型调用轮次。
	TotalTurns int
	// Segment 预算段序号。
	Segment int
	// Completed 已完成（本轮请求内改动过的文件，去重）。
	Completed []string
	// InProgress 进行中（空转时反复读取的文件 / 最近读取的文件）。
	InProgress []string
	// NextSteps 建议下一步。
	NextSteps []string
	// StuckAt 空转停机：卡在哪。
	StuckAt string
	// Tried 空转停机：试过什么。
	Tried []string
}

// Render 输出用户可见的快照文本。固定模板、纯本地拼装。
func (r *StopReport) Render() string {
	var sb strings.Builder
	sb.WriteString(fmt.Sprintf("⏸ 已暂停并交回控制权（%s；本轮已用 %d 轮，预算段 %d）\n\n", r.Cause, r.TotalTurns, r.Segment))
	sb.WriteString("[任务进度快照]\n")
	sb.WriteString(fmt.Sprintf("已完成: %s\n", joinOrDefault(r.Completed, "(本请求内无文件改动)")))
	sb.WriteString(fmt.Sprintf("进行中: %s\n", joinOrDefault(r.InProgress, "(无)")))
	if r.StuckAt != "" {
		tried := joinOrDefault(r.Tried, "(未记录)")
		sb.WriteString(fmt.Sprintf("卡在: %s\n试过: %s\n", r.StuckAt, tried))
	}
	sb.WriteString(fmt.Sprintf("下一步: %s\n", joinOrDefault(r.NextSteps, defaultNextSteps)))
	sb.WriteString("\n恢复指引: 直接回复「继续」接着跑（新预算段），或补充指令调整方向。\n")
	return sb.String()
}

const defaultNextSteps = "检查以上快照，决定继续、调整指令或换路径"

// Guard 聚合 P0 全部防线。非并发安全：交互模式 agent loop 单线程推进，
// 每个用户请求独占；跨请求用 BeginRequest 重置。
type Guard struct {
	cfg           Config
	turns         int  // 当前预算段已消耗轮次
	segment       int  // 预算段序号（0 = 首段未开始）
	total         int  // 本请求累计轮次（跨段累计，供快照展示）
	failureRounds int  // 连续整轮工具全失败计数（P0-2）
	reviewRounds  int  // 连续纯读轮计数（P0-3）
	stopped       bool // 已停机（停机后拒绝继续，直到 BeginRequest）
	tracker       *ProgressTracker
}

// NewGuard 创建防护器。
func NewGuard(cfg Config) *Guard {
	return &Guard{
		cfg:     cfg,
		tracker: NewProgressTracker(),
	}
}

// BeginRequest 用户发起新请求：全部状态归零。
func (g *Guard) BeginRequest() {
	g.turns = 0
	g.segment = 0
	g.total = 0
	g.failureRounds = 0
	g.reviewRounds = 0
	g.stopped = false
	g.tracker = NewProgressTracker()
}

// TotalTurns 返回本请求累计轮次。
func (g *Guard) TotalTurns() int {
	return g.total
}

// BeforeModelCall 在每次模型调用前判定（P0-1 轮次上限 + P0-4 到限测进度
// + P0-3 审查超限 + 空转前置停机）。返回 VerdictStop 时调用方必须停止
// 后续模型调用并渲染 Report。
func (g *Guard) BeforeModelCall() Decision {
	g.segment = max(g.segment, 1)
	if g.stopped {
		return g.stopDecision("已停机，等待用户输入（BeginRequest 重置）")
	}
	// P0-3 审查子循环超限 → **软提醒，不停机**。
	// 判据「无外部效果」是空转的有偏代理（合法只读研究会被误判），
	// 故只注入提醒把决定权交回模型；真正的强制停机由 P0-1（轮次）与
	// P0-4（重复检测：输出相似 / 重读 / diff 震荡）负责。
	if g.ReviewOverLimit() {
		return g.remindDecision()
	}
	// 空转前置停机（任何模式都生效：前置防线，压缩体系表第一行）
	if g.tracker.Spinning() {
		return g.spinningStopDecision()
	}
	if g.turns < g.cfg.MaxTurns {
		return Decision{Verdict: VerdictProceed, Turns: g.turns, Segment: g.segment}
	}
	// 到限：自主模式 + 收敛 → 静默续跑下一预算段（P0-4/P0-5）
	if g.cfg.AutonomyFull() && g.tracker.Converging() && g.segment < g.cfg.MaxSegments {
		g.segment++
		g.turns = 0
		return Decision{
			Verdict: VerdictExtend,
			Segment: g.segment,
			Reason:  fmt.Sprintf("轮次到限且检测到收敛信号，静默续跑第 %d 预算段", g.segment),
		}
	}
	// 默认（自主关闭）或空转/超段：停机交回控制权
	return g.stopDecision(fmt.Sprintf("模型调用轮次达到上限 %d", g.cfg.MaxTurns))
}

// AfterToolResults 在每轮工具执行完毕后判定（P0-2 无进展熔断）。
// allFailed = 本轮存在工具调用且全部失败。返回 VerdictStop 时调用方
// 必须停止后续模型调用。
func (g *Guard) AfterToolResults(allFailed bool) Decision {
	if allFailed {
		g.failureRounds++
		if g.failureRounds >= g.cfg.MaxConsecutiveToolFailures {
			return g.stopDecision(fmt.Sprintf(
				"连续 %d 轮工具调用全部失败（无进展熔断）", g.cfg.MaxConsecutiveToolFailures))
		}
	} else {
		g.failureRounds = 0
	}
	return Decision{Verdict: VerdictProceed, Turns: g.turns, Segment: max(g.segment, 1)}
}

// AfterTurn 在每轮（模型输出 + 工具结果落定）后记录观测：
// 计入轮次（P0-1）、喂进度检测器（P0-4）、更新审查计数（P0-3）。
func (g *Guard) AfterTurn(obs TurnObservation) {
	g.turns++
	g.total++
	// 先判「本轮是否读了新文件」（Observe 之前，readCounts 尚未计入本轮），
	// 再喂检测器。
	readNew := g.readsAnyNewFile(obs.FilesRead)
	g.tracker.Observe(obs)
	// P0-3：纯读轮且未读新文件（原地打转）计为审查轮；有文件改动、或
	// 读了从未读过的新文件（实质前进）即重置。仅用「无文件改动」作为空转
	// 代理会误杀合法的只读研究任务（如「依次读多个新文件」）。
	if len(normalizePaths(obs.FilesChanged)) == 0 && !readNew {
		g.reviewRounds++
	} else {
		g.reviewRounds = 0
	}
	// 可观测性（canary）：每轮落一行防护状态日志，人工验证失败时可复盘轨迹。
	slog.Info("agentguard: turn",
		"turns", g.turns,
		"total", g.total,
		"segment", max(g.segment, 1),
		"failing", obs.FailingTests,
		"changed", len(normalizePaths(obs.FilesChanged)),
		"read", len(normalizePaths(obs.FilesRead)),
		"review_rounds", g.reviewRounds,
	)
}

// readsAnyNewFile 报告 files 里是否存在「本轮之前从未读过」的文件（readCounts
// 中计数为 0）。它区分「原地打转（反复读已读文件）」与「前进（读新文件）」，
// 是 P0-3 审查计数是否累计的关键判据。
func (g *Guard) readsAnyNewFile(files []string) bool {
	for _, f := range normalizePaths(files) {
		if g.tracker.readCounts[f] == 0 {
			return true
		}
	}
	return false
}

// ReviewOverLimit 报告是否该发 P0-3 软提醒（连续纯读轮达阈值）。
// 注意：这是**提醒**判据而非停机判据 —— 调用方收到 VerdictRemind 后
// 应继续模型调用，只把 Reminder 注入上下文。
func (g *Guard) ReviewOverLimit() bool {
	return g.reviewRounds > g.cfg.MaxReviewRounds
}

// stopDecision 构造停机判定（附进度快照）。
func (g *Guard) stopDecision(cause string) Decision {
	g.stopped = true
	report := g.buildReport(cause)
	return Decision{
		Verdict: VerdictStop,
		Turns:   g.turns,
		Segment: max(g.segment, 1),
		Reason:  cause,
		Report:  report,
	}
}

// remindDecision 构造 P0-3 软提醒（不停机），并把计数归零重新开始
// —— 提醒是周期性 nudge（对齐 同类 CLI reminder_interval_tokens），不是一次性闸门。
func (g *Guard) remindDecision() Decision {
	n := g.reviewRounds
	g.reviewRounds = 0
	return Decision{
		Verdict:  VerdictRemind,
		Turns:    g.turns,
		Segment:  max(g.segment, 1),
		Reason:   fmt.Sprintf("已连续 %d 轮无文件改动", n),
		Reminder: reviewNudgeText(n),
	}
}

// reviewNudgeText 生成注入模型的提醒文本（英文主行 + 中文缩进辅助，仓库文案惯例）。
func reviewNudgeText(n int) string {
	return fmt.Sprintf(
		"[Progress nudge] %d consecutive turns with no file changes. "+
			"If you are still gathering information, continue; otherwise report your findings to the user or converge on a decision.\n"+
			"  提示：已连续 %d 轮未改动文件。若仍在收集信息可继续；否则建议向用户汇报当前发现或收敛结论。",
		n, n)
}

// spinningStopDecision 空转停机（附"卡在哪、试过什么"摘要）。
func (g *Guard) spinningStopDecision() Decision {
	d := g.stopDecision("检测到空转信号（无实质进展）")
	detail := g.tracker.SpinningDetail()
	d.Report.StuckAt = detail.StuckAt
	d.Report.Tried = detail.Tried
	if len(detail.Tried) > 0 {
		d.Report.InProgress = detail.Tried
	}
	return d
}

// buildReport 从累积观测中填充快照（零 AI 成本）。
func (g *Guard) buildReport(cause string) *StopReport {
	r := &StopReport{
		Cause:      cause,
		TotalTurns: g.total,
		Segment:    max(g.segment, 1),
	}
	// 已完成：本请求内改动过的文件（按首次改动顺序）
	var completed []string
	seen := make(map[string]bool)
	for _, set := range g.tracker.changedSets {
		for _, f := range set {
			if !seen[f] {
				seen[f] = true
				completed = append(completed, f)
			}
		}
	}
	r.Completed = completed
	// 进行中：按读取热度取前 5 个文件（空转详情在 spinningStopDecision 中覆盖）
	r.InProgress = g.tracker.recentReadFiles()
	return r
}

// recentReadFiles 返回最近一轮读取的文件（进度快照"进行中"数据源）。
func (t *ProgressTracker) recentReadFiles() []string {
	var files []string
	seen := make(map[string]bool)
	// readCounts 无序；按读取次数降序取前 5 作为"最近关注"
	type kv struct {
		file string
		n    int
	}
	var kvs []kv
	for f, n := range t.readCounts {
		if !seen[f] {
			seen[f] = true
			kvs = append(kvs, kv{f, n})
		}
	}
	sort.Slice(kvs, func(i, j int) bool { return kvs[i].n > kvs[j].n })
	for i, v := range kvs {
		if i >= 5 {
			break
		}
		files = append(files, v.file)
	}
	return files
}

func joinOrDefault(items []string, def string) string {
	if len(items) == 0 {
		return def
	}
	return strings.Join(items, ", ")
}

func max(a, b int) int {
	if a > b {
		return a
	}
	return b
}
