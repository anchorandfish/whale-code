package agentguard

import (
	"sort"
	"strconv"
	"strings"
)

// TurnObservation 每轮结束的可观测信号（P0-4 进度检测器输入）。
// 字段由调用方从工具调用/结果中提取；不可得的字段用零值/nil 占位。
type TurnObservation struct {
	// FailingTests 本轮观测到的失败测试数。未知填 -1（不参与收敛判定）。
	FailingTests int
	// FilesChanged 本轮 Edit/Write 的文件路径。
	FilesChanged []string
	// FilesRead 本轮 Read 的文件路径。
	FilesRead []string
	// Output 本轮 AI 输出正文（用于相似度空转检测）。
	Output string
	// ToolCalls 本轮工具调用的 (工具名, 原始输入) 指纹，用于「同一工具 + 同一参数
	// 反复调用」空转检测（P0-4 第四判据）。
	//
	// 口径与 FilesRead/FilesChanged 一致：**只填成功执行**的调用（见
	// agent_guard.go turnObservation 的 G9-③ 约束）。
	// ★ 调用方**不要把只读工具**填进来：只读面的重复由 FilesRead/rereadExceeded
	// 按文件路径负责，同一语义不得有两个阈值（一个按路径、一个按输入）⇒
	// 过滤发生在 tui 的 turnObservation（那里才有 permission 能力分桶）。
	ToolCalls []ToolCallKey
}

// ToolCallKey 是一次工具调用的稳定指纹（工具名 + 原始输入 JSON）。
// 只用于**同一指纹是否重复出现**的计数，不参与任何语义解释。
type ToolCallKey struct {
	// Tool 工具名（如 bash）。
	Tool string
	// Input 工具原始输入（逐字比较；同一条命令 ⇒ 同一字符串）。
	Input string
}

// SpinDetail 空转详情（"卡在哪、试过什么"摘要的数据源）。
type SpinDetail struct {
	// StuckAt 空转卡点描述（如反复读取的文件、震荡的改动集合）。
	StuckAt string
	// Tried 已反复尝试的内容（重读文件列表 / 震荡文件列表）。
	Tried []string
}

const (
	// maxFileReads 同一文件读取次数阈值：超过即判定空转（churn 信号，
	// 事故实证：runner_core.go 被完整读取 24 次）。
	maxFileReads = 5
	// spinWindow 输出相似度检测窗口：连续 N 轮输出高度相似。
	spinWindow = 3
	// outputSimilarity 高相似阈值（0-1）。
	outputSimilarity = 0.9
	// stableWindow 改动文件数稳定判定窗口（最近 N 轮）。
	stableWindow = 3
	// minSignalTurns 判定所需的最少观测轮数。
	minSignalTurns = 3
	// maxSameToolCalls 同一 (工具, 输入) 的**累计**调用次数阈值：达到即判定空转
	// （P0-4 第四判据，见 repeatedCallsExceeded）。
	//
	// 取 5，与 maxFileReads 同量级但比较口径更严（`>=`，而重读是 `>`）：
	// 重读同一文件有时是合法地"回看上下文"，而**重复执行同一条命令/同一次写入**
	// 几乎没有正当理由。真机事故（2026-09-30 I-8.2）即 `echo hi > out/x.txt`
	// 连发 4 次，全靠模型自己停 —— 既有三条空转判据对非读写工具全不命中。
	maxSameToolCalls = 5
	// describeInputLimit 快照里展示"试过什么"时单条输入的截断长度，
	// 防止超长命令把停机报告刷屏。
	describeInputLimit = 80
)

// ProgressTracker P0-4 进度检测器：累积每轮观测，输出收敛/空转判定。
// 零值不可用，请用 NewProgressTracker。
type ProgressTracker struct {
	failingTests []int
	changedSets  [][]string
	readCounts   map[string]int
	callCounts   map[string]int // 同一 (工具,输入) 的累计调用次数（P0-4 重复调用信号）
	recentOutput []string
	changedFiles map[string]int // 历史累计（快照"已完成"用）
	oscillation  bool
	oscDetail    SpinDetail
}

// NewProgressTracker 创建进度检测器。
func NewProgressTracker() *ProgressTracker {
	return &ProgressTracker{
		readCounts:   make(map[string]int),
		callCounts:   make(map[string]int),
		changedFiles: make(map[string]int),
	}
}

// Observe 记录一轮观测。
func (t *ProgressTracker) Observe(obs TurnObservation) {
	t.failingTests = append(t.failingTests, obs.FailingTests)
	t.changedSets = append(t.changedSets, normalizePaths(obs.FilesChanged))
	t.recentOutput = append(t.recentOutput, strings.TrimSpace(obs.Output))
	for _, f := range obs.FilesChanged {
		t.changedFiles[f]++
	}
	for _, f := range obs.FilesRead {
		t.readCounts[f]++
	}
	// 批内去重：**同一轮**内同一条 assistant 消息里的多个并列 tool_use 是合法并发，
	// 不是跨轮失控（真机误报形态：并发派多个同参数 explore 子 agent）。
	// 每轮的每个指纹最多计一次，跨轮才累计 —— 与 loop_detector.CheckLoopBatch 的
	// 批内去重同一纪律（那条判据在同型问题上已有事故记录）。
	seenThisTurn := make(map[string]struct{}, len(obs.ToolCalls))
	for _, c := range obs.ToolCalls {
		k := toolCallKey(c)
		if _, dup := seenThisTurn[k]; dup {
			continue
		}
		seenThisTurn[k] = struct{}{}
		t.callCounts[k]++
	}
	t.detectOscillation()
}

// toolCallKey 把 (工具名, 输入) 拼成计数键；NUL 分隔避免 ("a","bc") 与 ("ab","c")
// 撞成同一个键。
func toolCallKey(c ToolCallKey) string {
	return c.Tool + "\x00" + c.Input
}

// Converging 报告收敛信号：满足以下任一且无空转信号。
// - 失败测试数已知且最近窗口单调不增、且整体下降
// - 改动文件数趋于稳定（最近窗口不再增长）
func (t *ProgressTracker) Converging() bool {
	if t.Spinning() {
		return false
	}
	if len(t.failingTests) < minSignalTurns {
		return false
	}
	if t.failingMonotonicDecrease() {
		return true
	}
	return t.changedCountStabilized()
}

// Spinning 报告空转信号：连续窗口输出高度相似、同文件重读超阈值、
// 同一 (工具,输入) 反复调用超阈值，或改动集合在两组间震荡。
func (t *ProgressTracker) Spinning() bool {
	return t.similarOutputs() || t.rereadExceeded() || t.repeatedCallsExceeded() || t.oscillation
}

// SpinningDetail 返回空转详情（卡点 + 反复尝试的内容）。
// 未空转时返回零值。
func (t *ProgressTracker) SpinningDetail() SpinDetail {
	if t.rereadExceeded() {
		var files []string
		for f, n := range t.readCounts {
			if n > maxFileReads {
				files = append(files, f)
			}
		}
		sort.Strings(files)
		return SpinDetail{
			StuckAt: "反复读取同一文件（" + itoa(len(files)) + " 个超阈值）",
			Tried:   files,
		}
	}
	if t.repeatedCallsExceeded() {
		var tried []string
		for k, n := range t.callCounts {
			if n >= maxSameToolCalls {
				tried = append(tried, describeToolCallKey(k, n))
			}
		}
		sort.Strings(tried)
		return SpinDetail{
			StuckAt: "反复执行同一工具 + 同一参数（" + itoa(len(tried)) + " 项超阈值）",
			Tried:   tried,
		}
	}
	if t.oscillation {
		return t.oscDetail
	}
	if t.similarOutputs() {
		return SpinDetail{
			StuckAt: "连续 " + itoa(spinWindow) + " 轮输出高度相似（无实质进展）",
			Tried:   nil,
		}
	}
	return SpinDetail{}
}

// failingMonotonicDecrease 失败测试数单调不增且整体下降。
func (t *ProgressTracker) failingMonotonicDecrease() bool {
	known := t.knownFailingWindow()
	if len(known) < minSignalTurns {
		return false
	}
	dropped := false
	for i := 1; i < len(known); i++ {
		if known[i] > known[i-1] {
			return false
		}
		if known[i] < known[i-1] {
			dropped = true
		}
	}
	return dropped
}

// knownFailingWindow 取最近 stableWindow 轮中已知（>=0）的失败测试数。
func (t *ProgressTracker) knownFailingWindow() []int {
	start := len(t.failingTests) - stableWindow
	if start < 0 {
		start = 0
	}
	var known []int
	for _, v := range t.failingTests[start:] {
		if v >= 0 {
			known = append(known, v)
		}
	}
	return known
}

// changedCountStabilized 改动文件数趋于稳定：最近窗口每轮改动数
// 都不超过首轮，且首轮存在改动活动（全程零改动是"无进展"而非"稳定"）。
func (t *ProgressTracker) changedCountStabilized() bool {
	if len(t.changedSets) < stableWindow {
		return false
	}
	window := t.changedSets[len(t.changedSets)-stableWindow:]
	first := len(window[0])
	if first == 0 {
		return false
	}
	for _, s := range window[1:] {
		if len(s) > first {
			return false
		}
	}
	return true
}

// similarOutputs 最近 spinWindow 轮输出两两相似度均超阈值。
// 空输出视为"未知"（无证据），不参与判定——避免无文本输出的纯工具轮
// 被误判为空转。
func (t *ProgressTracker) similarOutputs() bool {
	if len(t.recentOutput) < spinWindow {
		return false
	}
	window := t.recentOutput[len(t.recentOutput)-spinWindow:]
	for _, o := range window {
		if strings.TrimSpace(o) == "" {
			return false
		}
	}
	for i := 1; i < len(window); i++ {
		if similarity(window[0], window[i]) < outputSimilarity {
			return false
		}
	}
	return true
}

// rereadExceeded 任一文件读取次数超过阈值。
func (t *ProgressTracker) rereadExceeded() bool {
	for _, n := range t.readCounts {
		if n > maxFileReads {
			return true
		}
	}
	return false
}

// repeatedCallsExceeded 任一 (工具, 输入) 的累计调用次数达到阈值。
//
// 与 rereadExceeded 的分工：后者按**文件路径**计（只读面），本函数按
// **工具 + 完整输入**计（非只读面，如 bash 的命令、write_file 的内容）
// ⇒ 两者红集不相交，同一次调用不会被两条判据各判一次。
//
// 为什么必须有这条（2026-09-30 真机 I-8.2）：既有的三条空转判据对
// `echo hi > out/x.txt` 这种"重复执行同一条命令、不读文件、输出为空"
// 的循环**全不命中**，强制面形同不存在。事故形态详见 repeated_calls_test.go。
func (t *ProgressTracker) repeatedCallsExceeded() bool {
	for _, n := range t.callCounts {
		if n >= maxSameToolCalls {
			return true
		}
	}
	return false
}

// describeToolCallKey 把计数键还原成停机报告里可读的一行："工具: 输入（N 次）"。
// 输入按 rune 截断，避免超长命令把报告刷屏（也避免截出半个多字节字符）。
func describeToolCallKey(key string, n int) string {
	tool, input, _ := strings.Cut(key, "\x00")
	if r := []rune(input); len(r) > describeInputLimit {
		input = string(r[:describeInputLimit]) + "…"
	}
	return tool + ": " + input + "（" + itoa(n) + " 次）"
}

// detectOscillation 检测改动集合在两组间震荡（A,B,A,B…）。
func (t *ProgressTracker) detectOscillation() {
	t.oscillation = false
	t.oscDetail = SpinDetail{}
	if len(t.changedSets) < 4 {
		return
	}
	window := t.changedSets[len(t.changedSets)-4:]
	a, b := window[0], window[1]
	if setEqual(a, b) || len(a) == 0 || len(b) == 0 {
		return
	}
	if setEqual(window[2], a) && setEqual(window[3], b) {
		t.oscillation = true
		all := append(append([]string{}, a...), b...)
		t.oscDetail = SpinDetail{
			StuckAt: "改动集合在两组文件间来回震荡",
			Tried:   uniqueSorted(all),
		}
	}
}

// similarity 计算两个输出的相似度（0-1）。基于行集合 Jaccard：agent
// 空转时的输出往往是同一段"检查/确认"文本的重复，行级 Jaccard 足够捕捉。
func similarity(a, b string) float64 {
	if a == "" && b == "" {
		return 1
	}
	la, lb := splitLines(a), splitLines(b)
	if len(la) == 0 || len(lb) == 0 {
		return 0
	}
	setA := make(map[string]bool, len(la))
	for _, l := range la {
		setA[strings.TrimSpace(l)] = true
	}
	inter := 0
	setB := make(map[string]bool, len(lb))
	for _, l := range lb {
		l = strings.TrimSpace(l)
		setB[l] = true
		if setA[l] {
			inter++
		}
	}
	union := len(setA) + len(setB) - inter
	if union == 0 {
		return 1
	}
	return float64(inter) / float64(union)
}

func splitLines(s string) []string {
	var out []string
	for _, l := range strings.Split(s, "\n") {
		if strings.TrimSpace(l) != "" {
			out = append(out, l)
		}
	}
	return out
}

func normalizePaths(paths []string) []string {
	out := make([]string, 0, len(paths))
	for _, p := range paths {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return out
}

func setEqual(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	sa := make(map[string]bool, len(a))
	for _, v := range a {
		sa[v] = true
	}
	for _, v := range b {
		if !sa[v] {
			return false
		}
	}
	return true
}

func uniqueSorted(in []string) []string {
	seen := make(map[string]bool, len(in))
	out := make([]string, 0, len(in))
	for _, v := range in {
		if !seen[v] {
			seen[v] = true
			out = append(out, v)
		}
	}
	sort.Strings(out)
	return out
}

func itoa(n int) string {
	return strconv.Itoa(n)
}
