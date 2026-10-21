package github

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"testing"
)

// multiMockAI 多 Agent 审查用的 AIClient mock。
// 依据系统提示区分 Agent 调用与交叉验证调用，可选对特定 focus 强制失败。
type multiMockAI struct {
	mu          sync.Mutex
	agentResp   string // Agent 审查响应
	verifyResp  string // 交叉验证响应
	failOnFocus string // 系统提示含此子串时返回错误（用于失败测试）
	calls       int
}

func (m *multiMockAI) Stream(_ context.Context, req AIRequest) (<-chan AIEvent, error) {
	m.mu.Lock()
	m.calls++
	sys := strings.Join(req.SystemPrompt, " ")
	m.mu.Unlock()

	if m.failOnFocus != "" && strings.Contains(sys, m.failOnFocus) {
		return nil, fmt.Errorf("mock forced failure for focus: %s", m.failOnFocus)
	}
	resp := m.agentResp
	if strings.Contains(sys, "verification agent") {
		resp = m.verifyResp
	}

	ch := make(chan AIEvent, 2)
	ch <- AITextDeltaEvent{Content: resp}
	ch <- AIMessageStopEvent{}
	close(ch)
	return ch, nil
}

func (m *multiMockAI) CallCount() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.calls
}

// TestEffortConcurrency 验证努力级别到并发数的映射。
func TestEffortConcurrency(t *testing.T) {
	cases := map[int]int{
		EffortLevelLow:    2,
		EffortLevelMedium: 3,
		EffortLevelHigh:   5,
		0:                 3, // 未知回退 medium
		99:                3,
	}
	for level, want := range cases {
		if got := effortConcurrency(level); got != want {
			t.Errorf("effortConcurrency(%d) = %d, want %d", level, got, want)
		}
	}
}

// TestNormalizeSeverity 验证严重度归一化（含近义词与未知回退）。
func TestNormalizeSeverity(t *testing.T) {
	cases := map[string]Severity{
		"blocker":    SeverityBlocker,
		"CRITICAL":   SeverityBlocker,
		"error":      SeverityBlocker,
		"warning":    SeverityWarning,
		"warn":       SeverityWarning,
		"suggestion": SeveritySuggestion,
		"info":       SeveritySuggestion,
		"unknown":    SeveritySuggestion, // 未知回退
		"":           SeveritySuggestion,
	}
	for in, want := range cases {
		if got := NormalizeSeverity(in); got != want {
			t.Errorf("NormalizeSeverity(%q) = %v, want %v", in, got, want)
		}
	}
}

// TestSeverityRanker_Rank 验证去重与严重度降序排序。
func TestSeverityRanker_Rank(t *testing.T) {
	ranker := NewSeverityRanker()
	input := []Finding{
		{Severity: SeveritySuggestion, File: "a.go", Line: 10, Title: "T"},
		{Severity: SeverityBlocker, File: "a.go", Line: 10, Title: "T"}, // 同键，更高严重度 → 覆盖
		{Severity: SeverityWarning, File: "b.go", Line: 5, Title: "B"},
		{Severity: SeverityBlocker, File: "a.go", Line: 3, Title: "A"},
	}
	got := ranker.Rank(input)
	if len(got) != 3 {
		t.Fatalf("去重后应剩 3 条，实际 %d", len(got))
	}
	// 排序：blocker 优先，同 severity 按文件名/行号
	wantOrder := []struct {
		sev  Severity
		file string
		line int
	}{
		{SeverityBlocker, "a.go", 3},
		{SeverityBlocker, "a.go", 10},
		{SeverityWarning, "b.go", 5},
	}
	for i, w := range wantOrder {
		if got[i].Severity != w.sev || got[i].File != w.file || got[i].Line != w.line {
			t.Errorf("第 %d 条排序错误: got %+v, want {sev:%v file:%s line:%d}",
				i, got[i], w.sev, w.file, w.line)
		}
	}
}

// TestSeverityRanker_Empty 空输入返回 nil。
func TestSeverityRanker_Empty(t *testing.T) {
	ranker := NewSeverityRanker()
	if got := ranker.Rank(nil); got != nil {
		t.Errorf("空输入应返回 nil，实际 %v", got)
	}
}

// TestExtractJSONArray 验证从 markdown 围栏及前后噪声中提取 JSON 数组。
func TestExtractJSONArray(t *testing.T) {
	cases := []struct {
		name  string
		input string
		want  string
	}{
		{"纯数组", `[{"a":1}]`, `[{"a":1}]`},
		{"json 围栏", "```json\n[{\"a\":1}]\n```", `[{"a":1}]`},
		{"普通围栏", "```\n[{\"a\":1}]\n```", `[{"a":1}]`},
		{"前后噪声", "Here is result:\n[{\"a\":1}]\ndone", `[{"a":1}]`},
		{"空数组", `[]`, `[]`},
		{"无数组", "no json here", ""},
		{"空串", "", ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := extractJSONArray(c.input); got != c.want {
				t.Errorf("extractJSONArray(%q) = %q, want %q", c.input, got, c.want)
			}
		})
	}
}

// TestParseFindingsJSON 验证发现 JSON 解析（含围栏与空数组）。
func TestParseFindingsJSON(t *testing.T) {
	t.Run("带围栏", func(t *testing.T) {
		raw := "```json\n[{\"severity\":\"blocker\",\"file\":\"a.go\",\"line\":10,\"title\":\"T\",\"body\":\"B\",\"suggestion\":\"S\"}]\n```"
		got, err := parseFindingsJSON(raw)
		if err != nil {
			t.Fatalf("解析失败: %v", err)
		}
		if len(got) != 1 {
			t.Fatalf("应解析 1 条，实际 %d", len(got))
		}
		f := got[0]
		if f.Severity != SeverityBlocker || f.File != "a.go" || f.Line != 10 || f.Title != "T" {
			t.Errorf("字段不匹配: %+v", f)
		}
	})
	t.Run("空数组", func(t *testing.T) {
		got, err := parseFindingsJSON("[]")
		if err != nil {
			t.Fatalf("空数组不应报错: %v", err)
		}
		if len(got) != 0 {
			t.Errorf("空数组应返回 0 条，实际 %d", len(got))
		}
	})
	t.Run("无效 JSON", func(t *testing.T) {
		if _, err := parseFindingsJSON("not json at all"); err == nil {
			t.Errorf("无效 JSON 应报错")
		}
	})
}

// TestMultiAgentReviewer_ReviewDiff 验证多 Agent 并行审查的聚合、去重、交叉验证与统计。
func TestMultiAgentReviewer_ReviewDiff(t *testing.T) {
	agentResp := `[{"severity":"blocker","file":"a.go","line":10,"title":"A","body":"ba","suggestion":"sa"},{"severity":"warning","file":"b.go","line":20,"title":"B","body":"bb","suggestion":"sb"}]`
	verifyResp := `[{"index":0,"verified":true,"reason":"ok"},{"index":1,"verified":true,"reason":"ok"}]`
	mock := &multiMockAI{agentResp: agentResp, verifyResp: verifyResp}

	// 用 2 个 Agent + high 努力级别，验证并发数被 agent 数量截断
	r := NewMultiAgentReviewer(nil, mock,
		WithAgents([]ReviewAgent{
			{Name: "logic-bug", Focus: "logic errors"},
			{Name: "security", Focus: "security"},
		}),
		WithEffortLevel(EffortLevelHigh),
	)

	result, err := r.ReviewDiff(context.Background(), "diff --git a/x b/x\n+hello")
	if err != nil {
		t.Fatalf("ReviewDiff 失败: %v", err)
	}
	// 2 个 agent 各返回 2 条相同发现 → 去重后 2 条
	if len(result.Findings) != 2 {
		t.Fatalf("去重后应剩 2 条，实际 %d", len(result.Findings))
	}
	// 排序：blocker 在前
	if result.Findings[0].Severity != SeverityBlocker {
		t.Errorf("首条应为 blocker，实际 %v", result.Findings[0].Severity)
	}
	// 交叉验证应标记 verified=true
	if !result.Findings[0].Verified || !result.Findings[1].Verified {
		t.Errorf("交叉验证后应标记 verified=true")
	}
	// 指标
	if result.Metrics.TotalFindings != 2 || result.Metrics.Blockers != 1 || result.Metrics.Warnings != 1 {
		t.Errorf("指标不匹配: %+v", result.Metrics)
	}
	if result.Metrics.AgentsInvoked != 2 {
		t.Errorf("AgentsInvoked 应为 2，实际 %d", result.Metrics.AgentsInvoked)
	}
	if result.Metrics.FilesReviewed != 2 {
		t.Errorf("FilesReviewed 应为 2，实际 %d", result.Metrics.FilesReviewed)
	}
	// 调用次数：2 个 agent + 1 次交叉验证（去重后 2 条 < batchSize 30，单批）
	if got := mock.CallCount(); got != 3 {
		t.Errorf("AI 调用次数应为 3（2 agent + 1 verify），实际 %d", got)
	}
	// 总结应含 emoji
	if !strings.Contains(result.Summary, "🔴") || !strings.Contains(result.Summary, "🟡") {
		t.Errorf("总结应含严重度 emoji: %s", result.Summary)
	}
}

// TestMultiAgentReviewer_SingleAgentFailureDoesNotBlock 验证单个 Agent 失败不阻塞其他 Agent。
func TestMultiAgentReviewer_SingleAgentFailureDoesNotBlock(t *testing.T) {
	agentResp := `[{"severity":"warning","file":"a.go","line":1,"title":"T","body":"b","suggestion":"s"}]`
	// logic-bug 的 focus 含 "logic errors"，强制失败
	mock := &multiMockAI{
		agentResp:   agentResp,
		verifyResp:  `[{"index":0,"verified":true,"reason":"ok"}]`,
		failOnFocus: "logic errors",
	}
	r := NewMultiAgentReviewer(nil, mock,
		WithAgents([]ReviewAgent{
			{Name: "logic-bug", Focus: "logic errors"},
			{Name: "security", Focus: "security"},
		}),
		WithEffortLevel(EffortLevelLow),
	)
	result, err := r.ReviewDiff(context.Background(), "diff --git a/x b/x\n+hi")
	if err != nil {
		t.Fatalf("单个 Agent 失败不应导致整体失败: %v", err)
	}
	// security 成功返回 1 条
	if len(result.Findings) != 1 {
		t.Fatalf("应剩余 1 条发现，实际 %d", len(result.Findings))
	}
	if result.Findings[0].Agent != "security" {
		t.Errorf("剩余发现应来自 security，实际 %s", result.Findings[0].Agent)
	}
}

// TestMultiAgentReviewer_EmptyDiff 空 diff 应报错。
func TestMultiAgentReviewer_EmptyDiff(t *testing.T) {
	mock := &multiMockAI{agentResp: "[]"}
	r := NewMultiAgentReviewer(nil, mock)
	if _, err := r.ReviewDiff(context.Background(), "   "); err == nil {
		t.Errorf("空 diff 应报错")
	}
}

// TestCrossVerifier_Verify 验证交叉验证按 index 回填 Verified。
func TestCrossVerifier_Verify(t *testing.T) {
	verifyResp := `[{"index":0,"verified":true,"reason":"ok"},{"index":1,"verified":false,"reason":"fp"}]`
	mock := &multiMockAI{verifyResp: verifyResp}
	v := NewCrossVerifier(mock, 1024)
	findings := []Finding{
		{File: "a.go", Line: 1, Title: "T1"},
		{File: "b.go", Line: 2, Title: "T2"},
	}
	v.Verify(context.Background(), "diff", findings)
	if !findings[0].Verified {
		t.Errorf("第 0 条应 verified=true")
	}
	if findings[1].Verified {
		t.Errorf("第 1 条应 verified=false")
	}
}

// TestCrossVerifier_VerifyFailure 验证调用失败时不修改原发现（保留 Agent 原始判定）。
func TestCrossVerifier_VerifyFailure(t *testing.T) {
	mock := &multiMockAI{failOnFocus: "verification agent"}
	v := NewCrossVerifier(mock, 1024)
	findings := []Finding{{File: "a.go", Line: 1, Title: "T", Verified: false}}
	v.Verify(context.Background(), "diff", findings)
	if findings[0].Verified {
		t.Errorf("验证失败时不应修改 Verified 为 true")
	}
}

// TestBuildAgentSystemPrompt 验证占位符替换。
func TestBuildAgentSystemPrompt(t *testing.T) {
	got := buildAgentSystemPrompt("my-focus", "my-rules")
	if !strings.Contains(got, "my-focus") {
		t.Errorf("应替换 {{FOCUS_AREA}}")
	}
	if !strings.Contains(got, "my-rules") {
		t.Errorf("应替换 {{REVIEW_RULES}}")
	}
	// 空 rules 应替换为 (none)
	got2 := buildAgentSystemPrompt("f", "")
	if !strings.Contains(got2, "(none)") {
		t.Errorf("空 rules 应替换为 (none)")
	}
}
