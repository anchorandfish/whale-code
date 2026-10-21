package github

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"sort"
	"strings"
	"sync"
	"time"

	"golang.org/x/sync/errgroup"
)

// ============================================================
// 多 Agent 并行审查引擎（P0-1）
//
// 对标同类 CLI 的多 Agent 并行 + 交叉验证 + 严重度排序。
// 复用 ai.StreamWithRetry（通过 AIAdapter 间接调用），获得重试/降级/压缩能力，
// 不直接依赖 internal/ai 包，便于测试 mock。
// ============================================================

// EffortLevel 努力级别，控制多 Agent 并行审查的并发数。
// 级别越高，并发 Agent 数越多，审查越细，但 API 配额消耗越大。
// 避免低配额用户被限流：低配额用户使用 EffortLevelLow。
const (
	EffortLevelLow    = 1 // 低配额用户：并发 2 个 Agent
	EffortLevelMedium = 2 // 默认：并发 3 个 Agent
	EffortLevelHigh   = 3 // 深度审查：并发 5 个 Agent
)

// effortConcurrency 将努力级别映射为并发 Agent 数。
// low=2, medium=3, high=5；未知级别回退到 medium（3）。
func effortConcurrency(level int) int {
	switch level {
	case EffortLevelLow:
		return 2
	case EffortLevelHigh:
		return 5
	case EffortLevelMedium:
		return 3
	}
	return 3
}

// ReviewAgent 单个审查 Agent 的职责定义。
// 每个 Agent 聚焦一个领域，由专属 Focus 刻画（注入系统提示的 {{FOCUS_AREA}}）。
type ReviewAgent struct {
	Name         string // Agent 名称，如 "logic-bug"
	Focus        string // 专注领域描述
	SystemPrompt string // Agent 专属系统提示（为空时使用通用模板 + Focus）
}

// defaultAgents 默认 Agent 矩阵，对标同类 CLI 的 5 个审查维度。
var defaultAgents = []ReviewAgent{
	{Name: "logic-bug", Focus: "logic errors, off-by-one, null pointer, edge cases"},
	{Name: "security", Focus: "SQL injection, XSS, auth bypass, sensitive data leak"},
	{Name: "race-condition", Focus: "race conditions, deadlocks, concurrency safety"},
	{Name: "type-mismatch", Focus: "type mismatch, interface contract violation, nil assertion"},
	{Name: "regression", Focus: "behavior regression, API compatibility, breaking changes"},
}

// Finding 单个审查发现。结构化字段便于行级评论发布与严重度排序。
type Finding struct {
	Agent      string   `json:"agent"`      // 来源 Agent 名称
	Severity   Severity `json:"severity"`   // blocker / warning / suggestion
	File       string   `json:"file"`       // 文件路径
	Line       int      `json:"line"`       // 行号
	Column     int      `json:"column"`     // 列号（可选，0 表示未指定）
	Title      string   `json:"title"`      // 问题标题
	Body       string   `json:"body"`       // 详细说明
	Suggestion string   `json:"suggestion"` // 修复建议
	Verified   bool     `json:"verified"`   // 是否经交叉验证确认为真阳性
}

// ReviewMetrics 审查统计指标。
type ReviewMetrics struct {
	TotalFindings int   `json:"total_findings"` // 发现总数
	Blockers      int   `json:"blockers"`       // blocker 数量
	Warnings      int   `json:"warnings"`       // warning 数量
	Suggestions   int   `json:"suggestions"`    // suggestion 数量
	FilesReviewed int   `json:"files_reviewed"` // 审查涉及的文件数
	AgentsInvoked int   `json:"agents_invoked"` // 实际调用的 Agent 数
	DurationMs    int64 `json:"duration_ms"`    // 审查耗时（毫秒）
}

// ReviewResult 多 Agent 审查结果。
type ReviewResult struct {
	Summary  string        // 总结评论（Markdown）
	Findings []Finding     // 所有发现（已去重排序）
	Metrics  ReviewMetrics // 统计指标
}

// CrossVerifier 交叉验证器。
// 汇总所有 Agent 的发现，调用 AI 互查，标记真/假阳性以降低误报率（目标 < 5%）。
type CrossVerifier struct {
	aiClient  AIClient
	maxTokens int
}

// NewCrossVerifier 创建交叉验证器。maxTokens<=0 时使用默认 4096。
func NewCrossVerifier(aiClient AIClient, maxTokens int) *CrossVerifier {
	if maxTokens <= 0 {
		maxTokens = 4096
	}
	return &CrossVerifier{aiClient: aiClient, maxTokens: maxTokens}
}

// SeverityRanker 严重度排序器。
// 按 (file, line, title) 去重并按严重度降序排序。
type SeverityRanker struct{}

// NewSeverityRanker 创建严重度排序器。
func NewSeverityRanker() *SeverityRanker { return &SeverityRanker{} }

// MultiAgentOption 多 Agent 审查器的配置选项。
type MultiAgentOption func(*MultiAgentReviewer)

// WithAgents 自定义 Agent 列表（默认使用 defaultAgents）。
func WithAgents(agents []ReviewAgent) MultiAgentOption {
	return func(r *MultiAgentReviewer) {
		if len(agents) > 0 {
			r.agents = agents
		}
	}
}

// WithEffortLevel 设置努力级别（控制并发数）。
func WithEffortLevel(level int) MultiAgentOption {
	return func(r *MultiAgentReviewer) { r.effortLevel = level }
}

// WithMaxTokens 设置单次 AI 调用的最大输出 token 数。
func WithMaxTokens(n int) MultiAgentOption {
	return func(r *MultiAgentReviewer) {
		if n > 0 {
			r.maxTokens = n
		}
	}
}

// WithRules 设置项目级审查规则（注入系统提示的 {{REVIEW_RULES}}）。
func WithRules(rules string) MultiAgentOption {
	return func(r *MultiAgentReviewer) { r.rules = rules }
}

// WithVerifier 自定义交叉验证器（默认按 maxTokens 创建一个）。
func WithVerifier(v *CrossVerifier) MultiAgentOption {
	return func(r *MultiAgentReviewer) { r.verifier = v }
}

// MultiAgentReviewer 多 Agent 并行审查器。
// 通过多个聚焦不同领域的 Agent 并行审查同一 diff，
// 再经交叉验证过滤误报、严重度排序，产出结构化审查结果。
type MultiAgentReviewer struct {
	client      *Client
	aiClient    AIClient
	agents      []ReviewAgent
	verifier    *CrossVerifier
	ranker      *SeverityRanker
	effortLevel int
	maxTokens   int
	rules       string
}

// NewMultiAgentReviewer 创建多 Agent 并行审查器。
// 默认使用 5 个 Agent、medium 努力级别（并发 3）。
// client 可为 nil（仅使用 ReviewDiff 时不需要 GitHub 客户端，便于本地 /review 复用）。
func NewMultiAgentReviewer(client *Client, aiClient AIClient, opts ...MultiAgentOption) *MultiAgentReviewer {
	r := &MultiAgentReviewer{
		client:      client,
		aiClient:    aiClient,
		agents:      defaultAgents,
		ranker:      NewSeverityRanker(),
		effortLevel: EffortLevelMedium,
		maxTokens:   4096,
	}
	for _, opt := range opts {
		opt(r)
	}
	if r.verifier == nil {
		r.verifier = NewCrossVerifier(aiClient, r.maxTokens)
	}
	return r
}

// ReviewPR 对指定 PR 执行多 Agent 并行审查。
// 流程：获取 diff → 截断保护 → 并行 Agent 审查 → 交叉验证 → 去重排序。
// 仅读取 diff，不发布评论（发布由 Task 2 的 PostReview 负责）。
func (r *MultiAgentReviewer) ReviewPR(ctx context.Context, owner, repo string, prNumber int) (*ReviewResult, error) {
	if r.client == nil {
		return nil, fmt.Errorf("GitHub 客户端未初始化，无法获取 PR diff")
	}
	diff, err := r.client.GetPRDiff(ctx, owner, repo, prNumber)
	if err != nil {
		return nil, fmt.Errorf("获取 PR #%d diff 失败: %w", prNumber, err)
	}
	return r.ReviewDiff(ctx, diff)
}

// ReviewDiff 对给定 diff 执行多 Agent 并行审查。
// 不依赖 GitHub API，可供本地 /review 命令复用。
func (r *MultiAgentReviewer) ReviewDiff(ctx context.Context, diff string) (*ReviewResult, error) {
	start := time.Now()
	diff = strings.TrimSpace(diff)
	if diff == "" {
		return nil, fmt.Errorf("diff 为空，无法审查")
	}

	// 本批次保留 100KB 截断保护（DiffSplitter 属 P1，届时替代），并记录日志
	if len(diff) > maxDiffSize {
		log.Printf("[multi-review] diff 截断: 原始 %d 字节，限制 %d 字节", len(diff), maxDiffSize)
		diff = diff[:maxDiffSize] + fmt.Sprintf("\n\n... (diff truncated: original size %d bytes, limit %d bytes)", len(diff), maxDiffSize)
	}

	findings, err := r.runAgentsParallel(ctx, diff)
	if err != nil {
		// B5-P1-9: 所有 Agent 失败时返回 error，不再静默返回空结果
		return nil, fmt.Errorf("多 Agent 并行审查失败: %w", err)
	}

	// 交叉验证（降低误报）
	if r.verifier != nil && len(findings) > 0 {
		r.verifier.Verify(ctx, diff, findings)
	}

	// 去重 + 严重度排序
	ranked := r.ranker.Rank(findings)

	result := &ReviewResult{Findings: ranked}
	result.Metrics = buildMetrics(ranked, len(r.agents), time.Since(start))
	result.Summary = formatMultiReviewSummary(result)
	return result, nil
}

// runAgentsParallel 并行运行所有 Agent，单个 Agent 失败不阻塞其他。
// 并发数由努力级别控制（low=2, medium=3, high=5）。
// 使用 errgroup + SetLimit 控制并发；goroutine 内不返回错误，
// 而是记录日志并返回 nil，避免 errgroup 取消其他 Agent。
// B5-P1-9: 返回 ([]Finding, error)，若所有 Agent 均失败则返回 error，
// 避免静默返回空结果导致用户无法诊断审查失败原因。
func (r *MultiAgentReviewer) runAgentsParallel(ctx context.Context, diff string) ([]Finding, error) {
	concurrency := effortConcurrency(r.effortLevel)
	if concurrency > len(r.agents) {
		concurrency = len(r.agents)
	}

	g, gctx := errgroup.WithContext(ctx)
	g.SetLimit(concurrency)

	var (
		mu           sync.Mutex
		allFindings  []Finding
		successCount int
		failCount    int
	)

	for _, agent := range r.agents {
		agent := agent
		g.Go(func() error {
			findings, err := r.runAgent(gctx, agent, diff)
			mu.Lock()
			defer mu.Unlock()
			if err != nil {
				// 单个 Agent 失败不阻塞其他 Agent
				log.Printf("[multi-review] agent %s 失败: %v", agent.Name, err)
				failCount++
				return nil
			}
			successCount++
			allFindings = append(allFindings, findings...)
			return nil
		})
	}
	_ = g.Wait()

	// B5-P1-9: 所有 Agent 均失败时返回 error，避免静默返回空结果
	if successCount == 0 && len(r.agents) > 0 {
		return allFindings, fmt.Errorf("所有 %d 个审查 Agent 均失败（失败数=%d）", len(r.agents), failCount)
	}
	return allFindings, nil
}

// runAgent 运行单个 Agent，返回其审查发现。
func (r *MultiAgentReviewer) runAgent(ctx context.Context, agent ReviewAgent, diff string) ([]Finding, error) {
	system := agent.SystemPrompt
	if system == "" {
		system = buildAgentSystemPrompt(agent.Focus, r.rules)
	}
	user := buildAgentUserPrompt(diff)

	raw, err := r.callAIDelta(ctx, []string{system}, user)
	if err != nil {
		return nil, fmt.Errorf("agent %s AI 调用失败: %w", agent.Name, err)
	}

	findings, err := parseFindingsJSON(raw)
	if err != nil {
		return nil, fmt.Errorf("agent %s 解析发现失败: %w", agent.Name, err)
	}
	for i := range findings {
		findings[i].Agent = agent.Name
	}
	return findings, nil
}

// callAIDelta 调用 AI 并收集完整的流式文本响应。
// 通过 AIClient（AIAdapter）间接复用 ai.StreamWithRetry 的重试/降级/压缩能力，
// 不自实现重试逻辑。
func (r *MultiAgentReviewer) callAIDelta(ctx context.Context, systemPrompt []string, userPrompt string) (string, error) {
	req := AIRequest{
		SystemPrompt: systemPrompt,
		Messages:     []AIMessage{{Role: "user", Content: userPrompt}},
		MaxTokens:    r.maxTokens,
	}
	events, err := r.aiClient.Stream(ctx, req)
	if err != nil {
		return "", fmt.Errorf("AI 调用失败: %w", err)
	}

	var builder strings.Builder
	for event := range events {
		switch e := event.(type) {
		case AITextDeltaEvent:
			builder.WriteString(e.Content)
		case AIErrorEvent:
			return "", fmt.Errorf("AI 返回错误: %w", e.Err)
		case AIMessageStopEvent:
			// 正常结束
		}
	}
	result := strings.TrimSpace(builder.String())
	if result == "" {
		return "", fmt.Errorf("AI 返回空内容")
	}
	return result, nil
}

// Verify 执行交叉验证，原地标记每个 Finding 的 Verified 字段。
// 将发现分批发送给验证 AI（每批 batchSize 条），逐条判定真/假阳性。
func (v *CrossVerifier) Verify(ctx context.Context, diff string, findings []Finding) {
	const batchSize = 30
	for start := 0; start < len(findings); start += batchSize {
		end := start + batchSize
		if end > len(findings) {
			end = len(findings)
		}
		batch := findings[start:end]
		v.verifyBatch(ctx, diff, batch)
	}
}

// verifyBatch 验证单批发现，原地更新 Verified 字段。
// 验证调用失败时不修改原发现（保留 Agent 的原始判定），仅记录日志。
func (v *CrossVerifier) verifyBatch(ctx context.Context, diff string, batch []Finding) {
	prompt := buildCrossVerifyPrompt(batch, diff)
	raw, err := v.callVerify(ctx, prompt)
	if err != nil {
		log.Printf("[multi-review] 交叉验证调用失败: %v", err)
		return
	}
	results := parseVerifyJSON(raw)
	for _, res := range results {
		if res.Index < 0 || res.Index >= len(batch) {
			continue
		}
		batch[res.Index].Verified = res.Verified
	}
}

// callVerify 调用 AI 执行交叉验证并收集响应。
func (v *CrossVerifier) callVerify(ctx context.Context, userPrompt string) (string, error) {
	req := AIRequest{
		SystemPrompt: []string{crossVerifySystemPrompt},
		Messages:     []AIMessage{{Role: "user", Content: userPrompt}},
		MaxTokens:    v.maxTokens,
	}
	events, err := v.aiClient.Stream(ctx, req)
	if err != nil {
		return "", err
	}
	var builder strings.Builder
	for event := range events {
		switch e := event.(type) {
		case AITextDeltaEvent:
			builder.WriteString(e.Content)
		case AIErrorEvent:
			return "", e.Err
		case AIMessageStopEvent:
		}
	}
	return strings.TrimSpace(builder.String()), nil
}

// Rank 对发现按 (file, line, title) 去重，并按严重度降序排序。
// 相同键的发现保留严重度最高者；排序优先级：严重度 > 文件名 > 行号。
func (SeverityRanker) Rank(findings []Finding) []Finding {
	if len(findings) == 0 {
		return nil
	}
	keyOf := func(f Finding) string {
		return fmt.Sprintf("%s:%d|%s", f.File, f.Line, strings.ToLower(f.Title))
	}
	seen := make(map[string]int) // key → 在 result 中的索引
	result := make([]Finding, 0, len(findings))
	for _, f := range findings {
		k := keyOf(f)
		if idx, ok := seen[k]; ok {
			if f.Severity.Rank() > result[idx].Severity.Rank() {
				result[idx] = f
			}
			continue
		}
		seen[k] = len(result)
		result = append(result, f)
	}
	sort.SliceStable(result, func(i, j int) bool {
		ri, rj := result[i].Severity.Rank(), result[j].Severity.Rank()
		if ri != rj {
			return ri > rj
		}
		if result[i].File != result[j].File {
			return result[i].File < result[j].File
		}
		return result[i].Line < result[j].Line
	})
	return result
}

// buildMetrics 根据排序后的发现构建统计指标。
func buildMetrics(findings []Finding, agentsInvoked int, duration time.Duration) ReviewMetrics {
	m := ReviewMetrics{
		TotalFindings: len(findings),
		AgentsInvoked: agentsInvoked,
		DurationMs:    duration.Milliseconds(),
	}
	files := make(map[string]struct{})
	for _, f := range findings {
		switch f.Severity {
		case SeverityBlocker:
			m.Blockers++
		case SeverityWarning:
			m.Warnings++
		case SeveritySuggestion:
			m.Suggestions++
		}
		if f.File != "" {
			files[f.File] = struct{}{}
		}
	}
	m.FilesReviewed = len(files)
	return m
}

// formatMultiReviewSummary 生成多 Agent 审查的总结评论（Markdown）。
func formatMultiReviewSummary(result *ReviewResult) string {
	var b strings.Builder
	b.WriteString("## 🤖 Multi-Agent Code Review\n\n")
	m := result.Metrics
	b.WriteString(fmt.Sprintf("**审查概览**：%d 个 Agent 并行，发现 %d 个问题", m.AgentsInvoked, m.TotalFindings))
	b.WriteString(fmt.Sprintf("（🔴 %d / 🟡 %d / 🟢 %d）", m.Blockers, m.Warnings, m.Suggestions))
	b.WriteString(fmt.Sprintf("，涉及 %d 个文件，耗时 %dms\n\n", m.FilesReviewed, m.DurationMs))

	if len(result.Findings) == 0 {
		b.WriteString("未发现明显问题，代码质量良好。\n")
	} else {
		b.WriteString("| 严重度 | 文件 | 行 | 标题 | Agent |\n")
		b.WriteString("|--------|------|----|------|-------|\n")
		for _, f := range result.Findings {
			verified := ""
			if f.Verified {
				verified = " ✅"
			}
			b.WriteString(fmt.Sprintf("| %s%s | `%s` | %d | %s | %s |\n",
				f.Severity.Emoji(), verified, f.File, f.Line, f.Title, f.Agent))
		}
	}
	b.WriteString("\n---\n*由 Whale Code 多 Agent 引擎生成*\n")
	return b.String()
}

// ============================================================
// Prompt 模板（对标 01 文档附录 B）
// ============================================================

// agentSystemPromptTemplate Agent 通用系统提示模板。
// 占位符 {{FOCUS_AREA}} / {{REVIEW_RULES}} 由 buildAgentSystemPrompt 替换。
// 强制输出结构化 JSON 数组（P0-3 严重度分级的核心约束）。
const agentSystemPromptTemplate = `You are a senior code reviewer focusing on {{FOCUS_AREA}}.
Analyze the following diff and report findings as JSON array.
Each finding MUST have: severity, file, line, title, body, suggestion.

Severity levels:
- blocker: must fix (bugs, security, data loss)
- warning: should fix (risk, edge case)
- suggestion: nice to have (quality, readability)

Project rules:
{{REVIEW_RULES}}

Output ONLY the JSON array, no other text. If no issues found, output [].
Schema per finding:
{"severity":"blocker|warning|suggestion","file":"path","line":0,"column":0,"title":"short","body":"detail","suggestion":"fix"}`

// crossVerifySystemPrompt 交叉验证 Agent 的系统提示（对标附录 B.3）。
const crossVerifySystemPrompt = `You are a verification agent.
Below are findings from multiple review agents.
For each finding, verify if it is a TRUE positive or FALSE positive.
Consider:
1. Does the code actually have the described issue?
2. Is the severity appropriate?
3. Is the suggestion correct?

Output ONLY a JSON array, no other text. Schema per item:
{"index":0,"verified":true,"reason":"short explanation"}`

// buildAgentSystemPrompt 构造 Agent 系统提示，替换占位符。
func buildAgentSystemPrompt(focus, rules string) string {
	s := agentSystemPromptTemplate
	s = strings.ReplaceAll(s, "{{FOCUS_AREA}}", focus)
	if strings.TrimSpace(rules) == "" {
		rules = "(none)"
	}
	s = strings.ReplaceAll(s, "{{REVIEW_RULES}}", rules)
	return s
}

// buildAgentUserPrompt 构造 Agent 用户提示，嵌入 diff。
func buildAgentUserPrompt(diff string) string {
	return "Code changes diff:\n```diff\n" + diff + "\n```\n\nOutput the JSON array of findings."
}

// buildCrossVerifyPrompt 构造交叉验证用户提示。
// 为每条发现标注 index，便于回填 Verified 字段。
func buildCrossVerifyPrompt(batch []Finding, diff string) string {
	type indexed struct {
		Index      int      `json:"index"`
		Severity   Severity `json:"severity"`
		File       string   `json:"file"`
		Line       int      `json:"line"`
		Title      string   `json:"title"`
		Body       string   `json:"body"`
		Suggestion string   `json:"suggestion"`
	}
	items := make([]indexed, len(batch))
	for i, f := range batch {
		items[i] = indexed{i, f.Severity, f.File, f.Line, f.Title, f.Body, f.Suggestion}
	}
	data, _ := json.Marshal(items)
	var b strings.Builder
	b.WriteString("Findings to verify:\n```json\n")
	b.Write(data)
	b.WriteString("\n```\n\n")
	b.WriteString("Diff context:\n```diff\n")
	b.WriteString(diff)
	b.WriteString("\n```\n\nOutput the JSON array with verified flags.")
	return b.String()
}

// ============================================================
// JSON 解析工具
// ============================================================

// findingJSON AI 输出的单条发现 JSON 结构。
type findingJSON struct {
	Severity   string `json:"severity"`
	File       string `json:"file"`
	Line       int    `json:"line"`
	Column     int    `json:"column"`
	Title      string `json:"title"`
	Body       string `json:"body"`
	Suggestion string `json:"suggestion"`
}

// verifyResultJSON 交叉验证输出的单条结果 JSON 结构。
type verifyResultJSON struct {
	Index    int    `json:"index"`
	Verified bool   `json:"verified"`
	Reason   string `json:"reason"`
}

// parseFindingsJSON 解析 AI 输出的发现 JSON 数组。
// 容忍 markdown 代码围栏 ```json ... ``` 及前后多余文本。
func parseFindingsJSON(raw string) ([]Finding, error) {
	body := extractJSONArray(raw)
	if body == "" {
		return nil, fmt.Errorf("未找到 JSON 数组")
	}
	var items []findingJSON
	if err := json.Unmarshal([]byte(body), &items); err != nil {
		return nil, fmt.Errorf("解析 JSON 数组失败: %w", err)
	}
	findings := make([]Finding, 0, len(items))
	for _, it := range items {
		if it.File == "" && it.Line == 0 && it.Title == "" {
			continue // 跳过空发现
		}
		findings = append(findings, Finding{
			Severity:   NormalizeSeverity(it.Severity),
			File:       it.File,
			Line:       it.Line,
			Column:     it.Column,
			Title:      it.Title,
			Body:       it.Body,
			Suggestion: it.Suggestion,
		})
	}
	return findings, nil
}

// parseVerifyJSON 解析交叉验证 AI 输出的 JSON 数组。
// 解析失败时返回空切片（不阻塞流程）。
func parseVerifyJSON(raw string) []verifyResultJSON {
	body := extractJSONArray(raw)
	if body == "" {
		return nil
	}
	var items []verifyResultJSON
	if err := json.Unmarshal([]byte(body), &items); err != nil {
		log.Printf("[multi-review] 解析交叉验证 JSON 失败: %v", err)
		return nil
	}
	return items
}

// extractJSONArray 从可能包含 markdown 围栏或前后文本的字符串中提取首个 JSON 数组。
func extractJSONArray(raw string) string {
	s := strings.TrimSpace(raw)
	if s == "" {
		return ""
	}
	s = stripCodeFence(s)
	start := strings.Index(s, "[")
	end := strings.LastIndex(s, "]")
	if start < 0 || end < 0 || end <= start {
		return ""
	}
	return s[start : end+1]
}

// stripCodeFence 去除 ```json ... ``` 或 ``` ... ``` 围栏。
func stripCodeFence(s string) string {
	if !strings.HasPrefix(s, "```") {
		return s
	}
	if nl := strings.Index(s, "\n"); nl >= 0 {
		s = s[nl+1:]
	}
	if idx := strings.LastIndex(s, "```"); idx >= 0 {
		s = s[:idx]
	}
	return strings.TrimSpace(s)
}
