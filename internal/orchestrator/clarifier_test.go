// clarifier_test.go — H10 Phase 6 模块 5 Clarifier 单元测试（TDD 先行，03-实施prompt.md §5.1）。
// 覆盖：Generate 成功（1-3 个问题）/ Generate LLM 失败返回 error / Generate 空问题（边界 case）/
// MergeAnswers 合并答案到 requirement / FormatQuestionsForDisplay 含 "[LLM 生成]" 标识。
// §5.1 S1 Clarifier 测试；落地：5 个测试用例覆盖 happy/降级/边界
// ；落地：Options ≤4 + Questions ≤3
// 7-Prompt injection 防御；落地：输出含 "[LLM 生成]" 标识
// SNAPSHOT: tests={Generate_HappyPath, Generate_LLMError, Generate_EmptyQuestions, MergeAnswers, PromptInjectionMark}

package orchestrator

import (
	"context"
	"errors"
	"strings"
	"testing"
)

// makeTestClarifierWithMock 构造测试用 Clarifier + mock AIClient（便于复用，S5）。
// completeFn 为 nil 时 mock 默认返回空字符串。
func makeTestClarifierWithMock(completeFn func(ctx context.Context, prompt string) (string, error)) (*Clarifier, *mockAIClient) {
	client := &mockAIClient{completeFn: completeFn}
	return NewClarifier(client, nil), client
}

// TestClarifier_Generate_HappyPath mock AIClient 返回 1-3 个问题，验证 Generate 解析正确（S5）。
// 覆盖 happy path：LLM 返回合法 JSON 含 2 个问题（其中 1 个含 4 个选项），验证数量/字段/选项数。
func TestClarifier_Generate_HappyPath(t *testing.T) {
	t.Parallel()
	// LLM 返回 2 个问题（Q1 含 4 个选项，Q2 无选项）
	mockOutput := `{"questions":[{"id":"Q1","question":"使用什么测试框架?","options":[{"label":"标准库","value":"testing"},{"label":"testify","value":"testify"},{"label":"goconvey","value":"goconvey"},{"label":"ginkgo","value":"ginkgo"}],"reason":"需要确定测试框架"},{"id":"Q2","question":"是否需要 mock?","reason":"mock 影响实现路径"}]}`
	c, _ := makeTestClarifierWithMock(func(ctx context.Context, prompt string) (string, error) {
		return mockOutput, nil
	})

	questions, err := c.Generate(context.Background(), "为 internal/foo 加单元测试")
	if err != nil {
		t.Fatalf("Generate happy path: %v", err)
	}
	if len(questions) != 2 {
		t.Fatalf("expected 2 questions, got %d", len(questions))
	}
	if questions[0].ID != "Q1" {
		t.Errorf("Q1.ID = %s, want Q1", questions[0].ID)
	}
	if len(questions[0].Options) != 4 {
		t.Errorf("Q1.Options count = %d, want 4", len(questions[0].Options))
	}
	if questions[0].Options[0].Value != "testing" {
		t.Errorf("Q1.Options[0].Value = %s, want testing", questions[0].Options[0].Value)
	}
	if questions[1].ID != "Q2" {
		t.Errorf("Q2.ID = %s, want Q2", questions[1].ID)
	}
	if len(questions[1].Options) != 0 {
		t.Errorf("Q2.Options count = %d, want 0", len(questions[1].Options))
	}
}

// TestClarifier_Generate_LLMError mock AIClient 返回 error，验证 Generate 返回 error（S5）。
// 覆盖降级路径：LLM 调用失败时 Generate 不应吞错。
func TestClarifier_Generate_LLMError(t *testing.T) {
	t.Parallel()
	llmErr := errors.New("llm: rate limit exceeded")
	c, _ := makeTestClarifierWithMock(func(ctx context.Context, prompt string) (string, error) {
		return "", llmErr
	})

	questions, err := c.Generate(context.Background(), "为 internal/foo 加单元测试")
	if err == nil {
		t.Fatal("Generate LLM error 应返回 error，got nil")
	}
	if !strings.Contains(err.Error(), "llm complete") {
		t.Errorf("error 应包含 'llm complete'，got: %v", err)
	}
	if questions != nil {
		t.Errorf("LLM 失败时 questions 应为 nil，got %v", questions)
	}
}

// TestClarifier_Generate_EmptyQuestions mock AIClient 返回不含 JSON 的输出，验证 Generate 返回空 slice（S5 边界 case）。
// 覆盖边界 case：LLM 返回无 JSON 时视为 0 个问题，调用方直接进 plan Stage（§11.2）。
func TestClarifier_Generate_EmptyQuestions(t *testing.T) {
	t.Parallel()
	c, _ := makeTestClarifierWithMock(func(ctx context.Context, prompt string) (string, error) {
		return "抱歉，我无法理解此需求", nil
	})

	questions, err := c.Generate(context.Background(), "为 internal/foo 加单元测试")
	if err != nil {
		t.Fatalf("Generate 边界 case 不应返回 error（应返回空 slice），got: %v", err)
	}
	if questions != nil && len(questions) != 0 {
		t.Errorf("无 JSON 输出时 questions 应为空 slice，got %v", questions)
	}
}

// TestClarifier_Generate_EmptyQuestionsArray mock AIClient 返回空 questions 数组，验证 Generate 返回空 slice（S5 边界 case 2）。
// 覆盖边界 case：LLM 返回 {"questions":[]} 时也视为 0 个问题。
func TestClarifier_Generate_EmptyQuestionsArray(t *testing.T) {
	t.Parallel()
	c, _ := makeTestClarifierWithMock(func(ctx context.Context, prompt string) (string, error) {
		return `{"questions":[]}`, nil
	})

	questions, err := c.Generate(context.Background(), "为 internal/foo 加单元测试")
	if err != nil {
		t.Fatalf("Generate 空 questions 数组不应返回 error，got: %v", err)
	}
	if len(questions) != 0 {
		t.Errorf("空 questions 数组应返回 0 个问题，got %d", len(questions))
	}
}

// TestClarifier_Generate_QuestionsTruncatedToMax mock AIClient 返回 5 个问题，验证 Generate 截断为 3 个（红队约束，S5）。
// 覆盖红队约束：超过 max=3 的问题数应截断（防 LLM 列举攻击）。
func TestClarifier_Generate_QuestionsTruncatedToMax(t *testing.T) {
	t.Parallel()
	mockOutput := `{"questions":[{"id":"Q1","question":"q1"},{"id":"Q2","question":"q2"},{"id":"Q3","question":"q3"},{"id":"Q4","question":"q4"},{"id":"Q5","question":"q5"}]}`
	c, _ := makeTestClarifierWithMock(func(ctx context.Context, prompt string) (string, error) {
		return mockOutput, nil
	})

	questions, err := c.Generate(context.Background(), "为 internal/foo 加单元测试")
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}
	if len(questions) != ClarifyQuestionMaxNum {
		t.Errorf("超过 max 的问题数应截断为 %d，got %d", ClarifyQuestionMaxNum, len(questions))
	}
}

// TestClarifier_Generate_OptionsTruncatedToMax 单个问题选项超过 4 个应截断（红队约束，S5）。
func TestClarifier_Generate_OptionsTruncatedToMax(t *testing.T) {
	t.Parallel()
	mockOutput := `{"questions":[{"id":"Q1","question":"q1","options":[{"label":"a","value":"a"},{"label":"b","value":"b"},{"label":"c","value":"c"},{"label":"d","value":"d"},{"label":"e","value":"e"},{"label":"f","value":"f"}]}]}`
	c, _ := makeTestClarifierWithMock(func(ctx context.Context, prompt string) (string, error) {
		return mockOutput, nil
	})

	questions, err := c.Generate(context.Background(), "为 internal/foo 加单元测试")
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}
	if len(questions) != 1 {
		t.Fatalf("expected 1 question, got %d", len(questions))
	}
	if len(questions[0].Options) != ClarifyOptionMaxNum {
		t.Errorf("超过 max 的选项数应截断为 %d，got %d", ClarifyOptionMaxNum, len(questions[0].Options))
	}
}

// TestClarifier_MergeAnswers 验证 MergeAnswers 把答案列表合并到 requirement 末尾（S5）。
// 覆盖：空 answers 返回原 requirement / 非空 answers 追加 [澄清问答] 段。
func TestClarifier_MergeAnswers(t *testing.T) {
	t.Parallel()
	c, _ := makeTestClarifierWithMock(nil)

	t.Run("EmptyAnswers_ReturnsOriginal", func(t *testing.T) {
		original := "为 internal/foo 加单元测试"
		got := c.MergeAnswers(original, nil)
		if got != original {
			t.Errorf("空 answers 应返回原 requirement，got: %s", got)
		}
	})

	t.Run("NonEmptyAnswers_Appended", func(t *testing.T) {
		original := "为 internal/foo 加单元测试"
		answers := []ClarifyAnswer{
			{QuestionID: "Q1", Value: "testing"},
			{QuestionID: "Q2", Value: "需要 mock"},
		}
		got := c.MergeAnswers(original, answers)
		if !strings.Contains(got, original) {
			t.Errorf("合并后应包含原 requirement，got: %s", got)
		}
		if !strings.Contains(got, "[澄清问答]") {
			t.Errorf("合并后应包含 [澄清问答] 段，got: %s", got)
		}
		if !strings.Contains(got, "Q1: testing") {
			t.Errorf("合并后应包含 Q1 答案，got: %s", got)
		}
		if !strings.Contains(got, "Q2: 需要 mock") {
			t.Errorf("合并后应包含 Q2 答案，got: %s", got)
		}
	})
}

// TestClarifier_PromptInjectionMark 验证 FormatQuestionsForDisplay 输出含 "[LLM 生成]" 标识（审查优化 7，S5）。
// 覆盖 prompt injection 防御：展示给用户的文本前必须加 "[LLM 生成]" 标识行。
func TestClarifier_PromptInjectionMark(t *testing.T) {
	t.Parallel()
	questions := []ClarifyQuestion{
		{ID: "Q1", Question: "使用什么测试框架?", Reason: "需要确定框架"},
		{ID: "Q2", Question: "是否需要 mock?", Options: []ClarifyOption{{Label: "是", Value: "yes"}, {Label: "否", Value: "no"}}},
	}

	output := FormatQuestionsForDisplay(questions)
	if !strings.Contains(output, ClarifyPromptInjectionMark) {
		t.Errorf("输出应包含 prompt injection 防御标识 %q，got: %s", ClarifyPromptInjectionMark, output)
	}
	if !strings.Contains(output, "[LLM 生成]") {
		t.Errorf("输出应包含 '[LLM 生成]' 子串，got: %s", output)
	}
	if !strings.Contains(output, "使用什么测试框架?") {
		t.Errorf("输出应包含 Q1 问题文本，got: %s", output)
	}
	if !strings.Contains(output, "是否需要 mock?") {
		t.Errorf("输出应包含 Q2 问题文本，got: %s", output)
	}
}

// TestClarifier_PromptInjectionMark_EmptyQuestions 空问题列表应返回空字符串（边界 case，S5）。
func TestClarifier_PromptInjectionMark_EmptyQuestions(t *testing.T) {
	t.Parallel()
	output := FormatQuestionsForDisplay(nil)
	if output != "" {
		t.Errorf("空问题列表应返回空字符串，got: %s", output)
	}
}

// TestClarifier_Generate_NilReceiver nil receiver 应返回 error 不 panic（S5 健壮性）。
func TestClarifier_Generate_NilReceiver(t *testing.T) {
	t.Parallel()
	var c *Clarifier
	_, err := c.Generate(context.Background(), "test")
	if err == nil {
		t.Fatal("nil receiver Generate 应返回 error")
	}
}

// TestClarifier_Generate_EmptyRequirement 空需求应返回 error（S5 健壮性）。
func TestClarifier_Generate_EmptyRequirement(t *testing.T) {
	t.Parallel()
	c, _ := makeTestClarifierWithMock(nil)
	_, err := c.Generate(context.Background(), "   ")
	if err == nil {
		t.Fatal("空需求应返回 error")
	}
	if !strings.Contains(err.Error(), "requirement must not be empty") {
		t.Errorf("error 应包含 'requirement must not be empty'，got: %v", err)
	}
}
