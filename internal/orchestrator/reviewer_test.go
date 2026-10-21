// reviewer_test.go — H10 Phase 3 模块 3 Reviewer 反例测试（TDD 先行）。
// 覆盖致命反例 3：SanitizeAuthorContext 屏蔽作者文件路径 / commit author / 方案文档引用。
// 覆盖：/020 MaxRound=1 + NoPlan=true 不喂方案 + N1 签名一致 + AST 接口变化检测。
// ；落地：Reviewer 实现 ReviewSubconversation 接口
// SNAPSHOT: tests={SanitizeAuthorContext_FilePathLeak, SanitizeAuthorContext_PlanDocRefLeak, SanitizeAuthorContext_CommitAuthorLeak, MaxRoundMustBeOne, NoPlanTrue_NoPlanDocPassed, ClassifyReview_InterfaceChange_FullDim, ClassifyReview_SecuritySensitive_FullDim, ClassifyReview_LineCount, N1SignatureNotChanged, Review_PassResult, DetectChangeType_InterfaceAST, Review_EndToEnd_AuthorFileLeak(Phase3修复v1致命漏洞1)}

package orchestrator

import (
	"context"
	"strings"
	"sync"
	"testing"

	"github.com/anchorandfish/whale-code/internal/tools"
)

// mockAgent 测试用 tools.Agent mock 实现，记录调用参数。
type mockAgent struct {
	mu         sync.Mutex
	called     bool
	lastInput  tools.AgentInput
	lastPrompt string
	executeFn  func(ctx context.Context, input tools.AgentInput) (tools.AgentOutput, error)
}

// Execute 实现 tools.Agent 接口（签名与 N1 一致：(ctx, AgentInput) → (AgentOutput, error)）。
func (m *mockAgent) Execute(ctx context.Context, input tools.AgentInput) (tools.AgentOutput, error) {
	m.mu.Lock()
	m.called = true
	m.lastInput = input
	m.lastPrompt = input.Prompt
	fn := m.executeFn
	m.mu.Unlock()

	if fn != nil {
		return fn(ctx, input)
	}
	// 默认返回 verdict=pass 的 JSON
	return tools.AgentOutput{
		Result: `{"verdict":"pass","fatal_flaws":[],"suggestions":[],"evidence_urls":[]}`,
		Status: tools.TaskCompleted,
	}, nil
}

// makeTestReviewer 构造测试用 Reviewer（默认返回 pass）。
func makeTestReviewer(t *testing.T) (*Reviewer, *mockAgent, *TraceStore) {
	t.Helper()
	agent := &mockAgent{}
	tracer, err := NewTraceStore(t.TempDir() + "/review_trace.jsonl")
	if err != nil {
		t.Fatalf("NewTraceStore: %v", err)
	}
	r := NewReviewer(agent, tracer, DefaultReviewerConfig())
	return r, agent, tracer
}

// TestReviewer_SanitizeAuthorContext_FilePathLeak 致命反例 3：diff 含作者文件路径 → 净化后路径被替换为 <author_file_redacted>。
func TestReviewer_SanitizeAuthorContext_FilePathLeak(t *testing.T) {
	t.Parallel()
	r, _, _ := makeTestReviewer(t)

	diff := `diff --git a/internal/auth/handler.go b/internal/auth/handler.go
index abc..def 100644
--- a/internal/auth/handler.go
+++ b/internal/auth/handler.go
@@ -10,5 +10,8 @@
+func HandleLogin() {
+    // 实现
+}
`
	authorFiles := []string{"internal/auth/handler.go"}

	sanitized, err := r.SanitizeAuthorContext(diff, authorFiles)
	if err != nil {
		t.Fatalf("SanitizeAuthorContext: %v", err)
	}

	// 致命反例 3 校验：作者文件路径应被替换
	if strings.Contains(sanitized, "internal/auth/handler.go") {
		t.Errorf("致命反例 3：作者文件路径未屏蔽，仍出现在净化后的 diff 中:\n%s", sanitized)
	}
	if !strings.Contains(sanitized, "<author_file_redacted>") {
		t.Errorf("作者文件路径应被替换为 <author_file_redacted>，got:\n%s", sanitized)
	}
}

// TestReviewer_SanitizeAuthorContext_PlanDocRefLeak 致命反例 3：diff 含方案文档引用 → 净化后被替换。
func TestReviewer_SanitizeAuthorContext_PlanDocRefLeak(t *testing.T) {
	t.Parallel()
	r, _, _ := makeTestReviewer(t)

	diff := `diff --git a/foo.go b/foo.go
+++ b/foo.go
@@ -1,3 +1,5 @@
+// 依据 H10-Phase3-方案细化-v3.md 设计
+// 参考 02-跨方向冲突预防与接口契约.md
+func Foo() {}
`

	sanitized, err := r.SanitizeAuthorContext(diff, nil)
	if err != nil {
		t.Fatalf("SanitizeAuthorContext: %v", err)
	}

	// 致命反例 3：方案文档引用应被替换
	if strings.Contains(sanitized, "依据 H10-Phase3-方案细化-v3.md") {
		t.Errorf("致命反例 3：方案文档引用未屏蔽，仍出现在净化后的 diff 中:\n%s", sanitized)
	}
	if !strings.Contains(sanitized, "<plan_ref_redacted>") {
		t.Errorf("方案文档引用应被替换为 <plan_ref_redacted>，got:\n%s", sanitized)
	}
}

// TestReviewer_SanitizeAuthorContext_CommitAuthorLeak 致命反例 3：diff 含 commit author → 净化后移除。
func TestReviewer_SanitizeAuthorContext_CommitAuthorLeak(t *testing.T) {
	t.Parallel()
	r, _, _ := makeTestReviewer(t)

	diff := `diff --git a/foo.go b/foo.go
Author: John Doe <john@example.com>
Commit: Jane Smith <jane@example.com>
+++ b/foo.go
+func Foo() {}
`

	sanitized, err := r.SanitizeAuthorContext(diff, nil)
	if err != nil {
		t.Fatalf("SanitizeAuthorContext: %v", err)
	}

	// 致命反例 3：commit author 信息应被移除
	if strings.Contains(sanitized, "John Doe") || strings.Contains(sanitized, "john@example.com") {
		t.Errorf("致命反例 3：commit author 未屏蔽（John Doe）:\n%s", sanitized)
	}
	if strings.Contains(sanitized, "Jane Smith") || strings.Contains(sanitized, "jane@example.com") {
		t.Errorf("致命反例 3：commit author 未屏蔽（Jane Smith）:\n%s", sanitized)
	}
	// Author: 与 Commit: 行应被移除
	for _, line := range strings.Split(sanitized, "\n") {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "Author:") || strings.HasPrefix(trimmed, "Commit:") {
			t.Errorf("致命反例 3：元数据行未移除: %s", line)
		}
	}
}

// TestReviewer_MaxRoundMustBeOne /020：MaxRound != 1 → Review 返回 error。
func TestReviewer_MaxRoundMustBeOne(t *testing.T) {
	t.Parallel()
	r, _, _ := makeTestReviewer(t)

	cases := []struct {
		name     string
		maxRound int
		wantErr  bool
	}{
		{"MaxRound=0 拒绝", 0, true},
		{"MaxRound=2 拒绝", 2, true},
		{"MaxRound=3 拒绝", 3, true},
		{"MaxRound=1 通过", 1, false},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := r.Review(context.Background(), ReviewRequest{
				TaskID:   "T-001",
				Stage:    StageReview,
				NoPlan:   true,
				MaxRound: tc.maxRound,
				Content:  "some code diff",
			})
			if tc.wantErr && err == nil {
				t.Errorf("MaxRound=%d 应返回 error（/020）", tc.maxRound)
			}
			if !tc.wantErr && err != nil {
				t.Errorf("MaxRound=1 不应返回 error，got: %v", err)
			}
		})
	}
}

// TestReviewer_NoPlan_True_NoPlanDocPassed ：NoPlan=true 时审查 prompt 不含方案文档。
func TestReviewer_NoPlan_True_NoPlanDocPassed(t *testing.T) {
	t.Parallel()
	r, agent, _ := makeTestReviewer(t)

	_, err := r.Review(context.Background(), ReviewRequest{
		TaskID:   "T-001",
		Stage:    StageReview,
		NoPlan:   true,
		MaxRound: 1,
		Content:  "diff content here",
	})
	if err != nil {
		t.Fatalf("Review: %v", err)
	}

	// 验证 agent 被调用
	agent.mu.Lock()
	defer agent.mu.Unlock()
	if !agent.called {
		t.Fatal("agent.Execute 应被调用")
	}

	// 校验：NoPlan=true 时 prompt 不应含 "方案文档" 关键词作为方案
	// 但 buildReviewPrompt 的标题是 "审查原则" 等，不应含 "方案设计推理"
	// 这里校验 prompt 不含 "依据方案" 等暗示作者设计推理的词
	if strings.Contains(agent.lastPrompt, "方案设计推理") {
		t.Errorf("：NoPlan=true 时 prompt 不应含方案设计推理:\n%s", agent.lastPrompt)
	}
	// 应含代码 diff
	if !strings.Contains(agent.lastPrompt, "diff content here") {
		t.Errorf("：prompt 应含代码 diff，got:\n%s", agent.lastPrompt)
	}
}

// TestReviewer_ClassifyReview_InterfaceChange_FullDim go/parser 检测到 FuncDecl 变化 → FullDim。
func TestReviewer_ClassifyReview_InterfaceChange_FullDim(t *testing.T) {
	t.Parallel()

	// 直接测试 ClassifyReview：接口变化 → FullDim
	tier := ClassifyReview(DiffStat{TotalLines: 10}, ChangeType{IsInterfaceOrContract: true})
	if tier != ReviewTierFullDim {
		t.Errorf("接口变化应触发 FullDim，got %s", tier)
	}

	// 测试 DetectChangeType 检测 FuncDecl
	diff := `diff --git a/foo.go b/foo.go
+++ b/foo.go
@@ -1,3 +1,5 @@
 package main

+func ExportedFunc() string {
+    return "new"
+}
`
	ct := DetectChangeType(diff)
	if !ct.IsInterfaceOrContract {
		t.Errorf("go/parser 应检测到 ExportedFunc 是接口变化（FuncDecl + IsExported）")
	}
}

// TestReviewer_ClassifyReview_SecuritySensitive_FullDim 路径含 crypto/auth/password → FullDim。
func TestReviewer_ClassifyReview_SecuritySensitive_FullDim(t *testing.T) {
	t.Parallel()

	// 直接测试 ClassifyReview：安全敏感 → FullDim
	tier := ClassifyReview(DiffStat{TotalLines: 5}, ChangeType{IsSecuritySensitive: true})
	if tier != ReviewTierFullDim {
		t.Errorf("安全敏感应触发 FullDim，got %s", tier)
	}

	// 测试 DetectChangeType 检测 crypto 路径
	diff := `diff --git a/internal/crypto/aes.go b/internal/crypto/aes.go
+++ b/internal/crypto/aes.go
+func Encrypt() {}
`
	ct := DetectChangeType(diff)
	if !ct.IsSecuritySensitive {
		t.Errorf("路径含 crypto 应触发 IsSecuritySensitive")
	}

	// auth 关键词
	diffAuth := `+++ b/auth/login.go
+func Login() {}
`
	ct = DetectChangeType(diffAuth)
	if !ct.IsSecuritySensitive {
		t.Errorf("路径含 auth 应触发 IsSecuritySensitive")
	}

	// password 关键词
	diffPass := `+++ b/password.go
+func Hash() {}
`
	ct = DetectChangeType(diffPass)
	if !ct.IsSecuritySensitive {
		t.Errorf("路径含 password 应触发 IsSecuritySensitive")
	}
}

// TestReviewer_ClassifyReview_LineCount diff 行数分级判定。
func TestReviewer_ClassifyReview_LineCount(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name     string
		stat     DiffStat
		change   ChangeType
		wantTier ReviewTier
	}{
		{"<50 行 → Checklist", DiffStat{TotalLines: 30}, ChangeType{}, ReviewTierChecklist},
		{"50 行 → MidDim", DiffStat{TotalLines: 50}, ChangeType{}, ReviewTierMidDim},
		{"200 行 → MidDim", DiffStat{TotalLines: 200}, ChangeType{}, ReviewTierMidDim},
		{">200 行 → FullDim", DiffStat{TotalLines: 201}, ChangeType{}, ReviewTierFullDim},
		{"接口变化 → FullDim（不论行数）", DiffStat{TotalLines: 5}, ChangeType{IsInterfaceOrContract: true}, ReviewTierFullDim},
		{"安全敏感 → FullDim（不论行数）", DiffStat{TotalLines: 5}, ChangeType{IsSecuritySensitive: true}, ReviewTierFullDim},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := ClassifyReview(tc.stat, tc.change)
			if got != tc.wantTier {
				t.Errorf("ClassifyReview(%+v, %+v) = %s, want %s", tc.stat, tc.change, got, tc.wantTier)
			}
		})
	}
}

// TestReviewer_N1SignatureNotChanged 验证 Reviewer 调用 Agent.Execute 签名与 N1 一致（零容忍）。
// N1 签名：Execute(ctx context.Context, input AgentInput) (AgentOutput, error)
// Reviewer 通过 r.agent.Execute(ctx, tools.AgentInput{...}) 调用，签名形状一致。
func TestReviewer_N1SignatureNotChanged(t *testing.T) {
	t.Parallel()
	r, agent, _ := makeTestReviewer(t)

	_, err := r.Review(context.Background(), ReviewRequest{
		TaskID:   "T-001",
		Stage:    StageReview,
		NoPlan:   true,
		MaxRound: 1,
		Content:  "diff",
	})
	if err != nil {
		t.Fatalf("Review: %v", err)
	}

	agent.mu.Lock()
	defer agent.mu.Unlock()

	// 校验 agent.Execute 被调用（签名一致则调用成功）
	if !agent.called {
		t.Fatal("agent.Execute 应被调用，签名与 N1 一致")
	}
	// 校验 lastInput 是 tools.AgentInput 类型（编译期保证，运行期仅校验 Prompt 非空）
	if agent.lastInput.Prompt == "" {
		t.Error("AgentInput.Prompt 不应为空")
	}
}

// TestReviewer_Review_PassResult Review 返回 pass verdict + Round=1。
func TestReviewer_Review_PassResult(t *testing.T) {
	t.Parallel()
	r, _, _ := makeTestReviewer(t)

	report, err := r.Review(context.Background(), ReviewRequest{
		TaskID:   "T-001",
		Stage:    StageReview,
		NoPlan:   true,
		MaxRound: 1,
		Content:  "diff content",
	})
	if err != nil {
		t.Fatalf("Review: %v", err)
	}
	if report == nil {
		t.Fatal("report 不应为 nil")
	}
	if report.Verdict != ReviewVerdictPass {
		t.Errorf("verdict 应为 pass，got %s", report.Verdict)
	}
	if report.Round != 1 {
		t.Errorf("Round 应为 1（/020），got %d", report.Round)
	}
}

// TestReviewer_Review_FailResult agent 返回 fail → report verdict=fail。
func TestReviewer_Review_FailResult(t *testing.T) {
	t.Parallel()
	agent := &mockAgent{
		executeFn: func(ctx context.Context, input tools.AgentInput) (tools.AgentOutput, error) {
			return tools.AgentOutput{
				Result: `{"verdict":"fail","fatal_flaws":["logic error"],"suggestions":[],"evidence_urls":["https://example.com"]}`,
				Status: tools.TaskCompleted,
			}, nil
		},
	}
	tracer, _ := NewTraceStore(t.TempDir() + "/trace.jsonl")
	r := NewReviewer(agent, tracer, DefaultReviewerConfig())

	report, err := r.Review(context.Background(), ReviewRequest{
		TaskID:   "T-001",
		Stage:    StageReview,
		NoPlan:   true,
		MaxRound: 1,
		Content:  "diff",
	})
	if err != nil {
		t.Fatalf("Review: %v", err)
	}
	if report.Verdict != ReviewVerdictFail {
		t.Errorf("verdict 应为 fail，got %s", report.Verdict)
	}
	if len(report.FatalFlaws) != 1 {
		t.Errorf("应有 1 个 fatal flaw，got %d", len(report.FatalFlaws))
	}
	if len(report.EvidenceURLs) != 1 {
		t.Errorf("应有 1 个 evidence URL，got %d", len(report.EvidenceURLs))
	}
}

// TestReviewer_Review_AgentError agent.Execute 返回 error → Review 返回 error。
func TestReviewer_Review_AgentError(t *testing.T) {
	t.Parallel()
	agent := &mockAgent{
		executeFn: func(ctx context.Context, input tools.AgentInput) (tools.AgentOutput, error) {
			return tools.AgentOutput{}, context.DeadlineExceeded
		},
	}
	tracer, _ := NewTraceStore(t.TempDir() + "/trace.jsonl")
	r := NewReviewer(agent, tracer, DefaultReviewerConfig())

	_, err := r.Review(context.Background(), ReviewRequest{
		TaskID:   "T-001",
		Stage:    StageReview,
		NoPlan:   true,
		MaxRound: 1,
		Content:  "diff",
	})
	if err == nil {
		t.Fatal("agent.Execute 返回 error 时 Review 应返回 error")
	}
}

// TestReviewer_DetectChangeType_NoGoCode diff 无 Go 代码 → 无接口变化。
func TestReviewer_DetectChangeType_NoGoCode(t *testing.T) {
	t.Parallel()
	diff := `diff --git a/README.md b/README.md
+++ b/README.md
+文档更新
`
	ct := DetectChangeType(diff)
	if ct.IsInterfaceOrContract {
		t.Error("非 Go 代码 diff 不应触发 IsInterfaceOrContract")
	}
}

// TestReviewer_SanitizeAuthorContext_EmptyDiff 空 diff 返回空字符串。
func TestReviewer_SanitizeAuthorContext_EmptyDiff(t *testing.T) {
	t.Parallel()
	r, _, _ := makeTestReviewer(t)

	sanitized, err := r.SanitizeAuthorContext("", []string{"foo.go"})
	if err != nil {
		t.Fatalf("SanitizeAuthorContext: %v", err)
	}
	if sanitized != "" {
		t.Errorf("空 diff 应返回空字符串，got %q", sanitized)
	}
}

// TestReviewer_ImplementsReviewSubconversation 编译期校验：Reviewer 实现 ReviewSubconversation 接口。
func TestReviewer_ImplementsReviewSubconversation(t *testing.T) {
	t.Parallel()
	var _ ReviewSubconversation = (*Reviewer)(nil)
	// 编译通过即接口实现
}

// TestReviewer_TraceWritten Review 成功后写 review_started + review_completed trace。
func TestReviewer_TraceWritten(t *testing.T) {
	t.Parallel()
	r, _, tracer := makeTestReviewer(t)

	_, err := r.Review(context.Background(), ReviewRequest{
		TaskID:   "T-trace",
		Stage:    StageReview,
		NoPlan:   true,
		MaxRound: 1,
		Content:  "diff",
	})
	if err != nil {
		t.Fatalf("Review: %v", err)
	}

	// 查询 review 事件
	events, err := tracer.QueryReviewEvents("T-trace")
	if err != nil {
		t.Fatalf("QueryReviewEvents: %v", err)
	}
	if len(events) != 2 {
		t.Errorf("应写入 2 条审查事件（started + completed），got %d", len(events))
	}
	if len(events) >= 1 && events[0].EventType != Phase3EvtReviewStarted {
		t.Errorf("第一条应为 review_started，got %s", events[0].EventType)
	}
}

// TestReviewer_Review_EndToEnd_AuthorFileLeak 致命反例 3 端到端测试（Phase 3 修复审查 v1 致命漏洞 1）。
// 验证 ReviewRequest.AuthorFiles 在 NoPlan=true 与 NoPlan=false 两个分支均被屏蔽：
// - NoPlan=true：走 buildReviewPrompt 路径，prompt 中不应出现作者文件路径
// - NoPlan=false：走 fmt.Sprintf 覆盖路径，prompt 中不应出现作者文件路径
//
// 零容忍：作者文件路径泄露到审查子对话即视为致命漏洞。
func TestReviewer_Review_EndToEnd_AuthorFileLeak(t *testing.T) {
	t.Parallel()

	// 构造含作者文件路径 + commit author + 方案文档引用的 diff（命中所有屏蔽分支）
	const authorFile = "internal/auth/handler.go"
	diff := "diff --git a/" + authorFile + " b/" + authorFile + "\n" +
		"Author: Secret Author <secret@example.com>\n" +
		"+++ b/" + authorFile + "\n" +
		"@@ -1,3 +1,5 @@\n" +
		" // 依据 H10-Phase3-方案细化-v3.md 设计\n" +
		"+func HandleLogin() string {\n" +
		"+    return \"token\"\n" +
		"+}\n"

	t.Run("NoPlan_true_屏蔽作者文件路径", func(t *testing.T) {
		t.Parallel()
		r, agent, _ := makeTestReviewer(t)

		_, err := r.Review(context.Background(), ReviewRequest{
			TaskID:      "T-leak-noplan-true",
			Stage:       StageReview,
			NoPlan:      true, // 走 buildReviewPrompt 路径
			MaxRound:    1,
			Content:     diff,
			AuthorFiles: []string{authorFile},
		})
		if err != nil {
			t.Fatalf("Review: %v", err)
		}

		agent.mu.Lock()
		defer agent.mu.Unlock()
		if !agent.called {
			t.Fatal("agent.Execute 应被调用")
		}

		// 致命反例 3：作者文件路径不应出现在 prompt 中
		if strings.Contains(agent.lastPrompt, authorFile) {
			t.Errorf("致命反例 3 (NoPlan=true)：作者文件路径泄露到审查 prompt:\n%s", agent.lastPrompt)
		}
		// commit author 不应泄露
		if strings.Contains(agent.lastPrompt, "Secret Author") || strings.Contains(agent.lastPrompt, "secret@example.com") {
			t.Errorf("致命反例 3 (NoPlan=true)：commit author 泄露到审查 prompt:\n%s", agent.lastPrompt)
		}
		// 方案文档引用不应泄露
		if strings.Contains(agent.lastPrompt, "依据 H10-Phase3-方案细化-v3.md") {
			t.Errorf("致命反例 3 (NoPlan=true)：方案文档引用泄露到审查 prompt:\n%s", agent.lastPrompt)
		}
		// 应包含屏蔽占位符
		if !strings.Contains(agent.lastPrompt, "<author_file_redacted>") {
			t.Errorf("NoPlan=true：prompt 应含 <author_file_redacted> 占位符，got:\n%s", agent.lastPrompt)
		}
	})

	t.Run("NoPlan_false_屏蔽作者文件路径", func(t *testing.T) {
		t.Parallel()
		r, agent, _ := makeTestReviewer(t)

		_, err := r.Review(context.Background(), ReviewRequest{
			TaskID:      "T-leak-noplan-false",
			Stage:       StageReview,
			NoPlan:      false, // 走 fmt.Sprintf 覆盖路径（修复前此分支用 req.Content 未屏蔽）
			MaxRound:    1,
			Content:     diff,
			PlanDoc:     "审查方案文档：xxx", // noPlan=false 时必须非空
			AuthorFiles: []string{authorFile},
		})
		if err != nil {
			t.Fatalf("Review: %v", err)
		}

		agent.mu.Lock()
		defer agent.mu.Unlock()
		if !agent.called {
			t.Fatal("agent.Execute 应被调用")
		}

		// 致命反例 3：作者文件路径不应出现在 prompt 中（修复前 noPlan=false 分支用 req.Content 泄露）
		if strings.Contains(agent.lastPrompt, authorFile) {
			t.Errorf("致命反例 3 (NoPlan=false)：作者文件路径泄露到审查 prompt（修复前漏洞）:\n%s", agent.lastPrompt)
		}
		// commit author 不应泄露
		if strings.Contains(agent.lastPrompt, "Secret Author") || strings.Contains(agent.lastPrompt, "secret@example.com") {
			t.Errorf("致命反例 3 (NoPlan=false)：commit author 泄露到审查 prompt:\n%s", agent.lastPrompt)
		}
		// 应包含屏蔽占位符
		if !strings.Contains(agent.lastPrompt, "<author_file_redacted>") {
			t.Errorf("NoPlan=false：prompt 应含 <author_file_redacted> 占位符，got:\n%s", agent.lastPrompt)
		}
		// PlanDoc 内容应保留（喂方案审查方案本身）
		if !strings.Contains(agent.lastPrompt, "审查方案文档：xxx") {
			t.Errorf("NoPlan=false：prompt 应含 PlanDoc 内容，got:\n%s", agent.lastPrompt)
		}
	})

	t.Run("AuthorFiles_为空_仅清理元数据", func(t *testing.T) {
		t.Parallel()
		r, agent, _ := makeTestReviewer(t)

		// AuthorFiles 为空时仍应清理 commit author / 方案文档引用
		_, err := r.Review(context.Background(), ReviewRequest{
			TaskID:   "T-leak-empty-authorfiles",
			Stage:    StageReview,
			NoPlan:   true,
			MaxRound: 1,
			Content:  diff,
			// AuthorFiles 不设置（向后兼容 Phase 2 调用方）
		})
		if err != nil {
			t.Fatalf("Review: %v", err)
		}

		agent.mu.Lock()
		defer agent.mu.Unlock()

		// commit author 应被清理（与作者文件无关，由 removeMetadataLines 处理）
		if strings.Contains(agent.lastPrompt, "Secret Author") || strings.Contains(agent.lastPrompt, "secret@example.com") {
			t.Errorf("AuthorFiles 为空时 commit author 仍应被清理:\n%s", agent.lastPrompt)
		}
	})
}
