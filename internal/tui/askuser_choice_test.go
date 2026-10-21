// askuser_choice_test.go — T3 S5 AskUser 选择/渲染/仲裁单元测试。
// 覆盖 checkAskUserChoice 多结果/仲裁/频率/缓存/allow_free_text 跳过 + escapePrompt + categoryTags/renderAskQuestionLine。
// ★ 2026-09-22：原先此处的 3 个 renderPendingChoice 测试已删除 —— 该函数**生产零调用**
//
//	（调用点只有测试），整个函数被 linker 丢弃 ⇒ 那些测试在测一个线上不跑的渲染器。
//	选项框的渲染层断言现落在**唯一活线** renderChoiceUI 上（见 askuser_choiceui_test.go）。
//
// ★ 2026-10-13：renderCategoryLabel 改为 categoryTags + renderAskQuestionLine（英文主行 + 缩进中文辅行）。
package tui

import (
	"encoding/json"
	"testing"

	"github.com/anchorandfish/whale-code/internal/orchestrator"
	"github.com/anchorandfish/whale-code/internal/tools"
	"github.com/anchorandfish/whale-code/internal/util"
)

// makeAskUserResult 构造一个 AskUser 工具结果（JSON 编码后的 Output）。
// 用于 checkAskUserChoice 测试输入。
func makeAskUserResult(toolID string, ask tools.AskUserResult) tools.Result {
	raw, _ := json.Marshal(ask)
	return tools.Result{
		ID:     toolID,
		Name:   "AskUser",
		Output: string(raw),
	}
}

// newChoiceTestModel 构造仅含 askSettings/askCache 的最小 Model（避免 NewModel 全量初始化开销）。
// askCount 默认 0；pendingOrchAskReq 默认 nil。
func newChoiceTestModel(threshold int) Model {
	return Model{
		ToolModel: ToolModel{},
		askSettings: &AskSettings{
			AskThreshold: threshold,
		},
		askCache: NewAskCache(),
	}
}

// TestCheckAskUserChoice_NoAskUserResult 验证结果列表中无 AskUser 调用时返回空（REF-T3-S5-新增）。
func TestCheckAskUserChoice_NoAskUserResult(t *testing.T) {
	m := newChoiceTestModel(1)
	results := []tools.Result{
		{ID: "t1", Name: "bash", Output: "ok"},
		{ID: "t2", Name: "write_file", Output: "done"},
	}
	choices, ok := m.checkAskUserChoice(results)
	if ok {
		t.Errorf("should not have AskUser choices, got %d", len(choices))
	}
}

// TestCheckAskUserChoice_SingleResult 验证单个 AskUser 结果返回 1 个 PendingChoice（REF-T3-S5-新增）。
func TestCheckAskUserChoice_SingleResult(t *testing.T) {
	m := newChoiceTestModel(1)
	results := []tools.Result{
		makeAskUserResult("t1", tools.AskUserResult{
			Question:   "用 JWT 还是 Session？",
			Category:   "dialogue-clarify",
			DefaultIdx: 0,
			// 2026-09-23 · ：追加 Other 与 allow_free_text **无关**（有选项即总追加）。
			// 本行显式声明只是对齐真实入参形状，**不是**「自动追加 Other」的前提。
			AllowFreeText: true,
			Options: []tools.AskUserOption{
				{Label: "JWT", Value: "jwt", Description: "无状态 Token"},
				{Label: "Session", Value: "session"},
			},
		}),
	}
	choices, ok := m.checkAskUserChoice(results)
	if !ok || len(choices) != 1 {
		t.Fatalf("expected 1 choice, got %d (ok=%v)", len(choices), ok)
	}
	if choices[0].Prompt != "用 JWT 还是 Session？" {
		t.Errorf("Prompt mismatch: %q", choices[0].Prompt)
	}
	if choices[0].Category != "dialogue-clarify" {
		t.Errorf("Category mismatch: %q", choices[0].Category)
	}
	if choices[0].DefaultIdx != 0 {
		t.Errorf("DefaultIdx mismatch: %d", choices[0].DefaultIdx)
	}
	if len(choices[0].Options) != 3 {
		t.Errorf("Options count mismatch: %d (应含 2 个模型选项 + 1 个自动追加的 Other)", len(choices[0].Options))
	}
	// 2026-09-21 · AskUser 对齐 同类 CLI：末尾应自动追加 Other（None of the above）
	if last := choices[0].Options[len(choices[0].Options)-1]; last.Value != otherOptionValue {
		t.Errorf("最后一个选项应为 Other（Value=%s），得到: %+v", otherOptionValue, last)
	}
	if choices[0].Options[0].Description != "无状态 Token" {
		t.Errorf("Options[0].Description mismatch: %q", choices[0].Options[0].Description)
	}
	if m.askCount != 1 {
		t.Errorf("askCount should be 1 after hit, got %d", m.askCount)
	}
}

// TestCheckAskUserChoice_MultipleResults 验证多个 AskUser 结果返回多个 PendingChoice（REF-T3-S5-新增）。
func TestCheckAskUserChoice_MultipleResults(t *testing.T) {
	m := newChoiceTestModel(3)
	results := []tools.Result{
		makeAskUserResult("t1", tools.AskUserResult{
			Question: "问题 1",
			Category: "clarify",
			Options:  []tools.AskUserOption{{Label: "A", Value: "a"}},
		}),
		makeAskUserResult("t2", tools.AskUserResult{
			Question: "问题 2",
			Category: "plan-confirm",
			Options:  []tools.AskUserOption{{Label: "B", Value: "b"}},
		}),
	}
	choices, ok := m.checkAskUserChoice(results)
	if !ok || len(choices) != 2 {
		t.Fatalf("expected 2 choices, got %d (ok=%v)", len(choices), ok)
	}
	if choices[0].Prompt != "问题 1" || choices[1].Prompt != "问题 2" {
		t.Errorf("Prompt order mismatch: %+v", choices)
	}
}

// TestCheckAskUserChoice_OrchestratorArbitration 验证编排器侧提问中时日常对话侧跳过（REF-降级策略-优化8，REF-T3-S5-新增）。
func TestCheckAskUserChoice_OrchestratorArbitration(t *testing.T) {
	m := newChoiceTestModel(1)
	// 模拟编排器侧正在等待用户回答
	m.pendingOrchAskReq = &orchestrator.AskRequest{}

	results := []tools.Result{
		makeAskUserResult("t1", tools.AskUserResult{
			Question: "日常对话问题",
			Category: "dialogue-clarify",
			Options:  []tools.AskUserOption{{Label: "A", Value: "a"}},
		}),
	}
	choices, ok := m.checkAskUserChoice(results)
	if ok {
		t.Errorf("orchestrator arbitration should suppress dialogue AskUser, got %d choices", len(choices))
	}
	if m.askCount != 0 {
		t.Errorf("askCount should not increment when suppressed, got %d", m.askCount)
	}
}

// TestCheckAskUserChoice_AllowFreeTextNoOptions 验证 allow_free_text=true 且 options 空时跳过 PendingChoice 渲染（REF-降级策略-优化2，REF-T3-S5-新增）。
func TestCheckAskUserChoice_AllowFreeTextNoOptions(t *testing.T) {
	m := newChoiceTestModel(1)
	results := []tools.Result{
		makeAskUserResult("t1", tools.AskUserResult{
			Question:      "请描述你的需求",
			Category:      "dialogue-clarify",
			AllowFreeText: true,
			// Options 为空
		}),
	}
	choices, ok := m.checkAskUserChoice(results)
	if ok {
		t.Errorf("allow_free_text=true with no options should skip, got %d choices", len(choices))
	}
}

// TestCheckAskUserChoice_FrequencyLimit 验证 askCount >= ask_threshold 时跳过（REF-降级策略-优化4，REF-T3-S5-新增）。
func TestCheckAskUserChoice_FrequencyLimit(t *testing.T) {
	m := newChoiceTestModel(2)
	// 模拟已达阈值
	m.askCount = 2

	results := []tools.Result{
		makeAskUserResult("t1", tools.AskUserResult{
			Question: "第三个问题",
			Category: "dialogue-clarify",
			Options:  []tools.AskUserOption{{Label: "A", Value: "a"}},
		}),
	}
	choices, ok := m.checkAskUserChoice(results)
	if ok {
		t.Errorf("frequency limit should suppress, got %d choices", len(choices))
	}
}

// TestCheckAskUserChoice_FrequencyBelowLimit 验证 askCount < ask_threshold 时正常返回（REF-T3-S5-新增）。
func TestCheckAskUserChoice_FrequencyBelowLimit(t *testing.T) {
	m := newChoiceTestModel(2)
	m.askCount = 1 // 1 < 2，未超阈值

	results := []tools.Result{
		makeAskUserResult("t1", tools.AskUserResult{
			Question: "第二个问题",
			Category: "dialogue-clarify",
			Options:  []tools.AskUserOption{{Label: "A", Value: "a"}},
		}),
	}
	choices, ok := m.checkAskUserChoice(results)
	if !ok || len(choices) != 1 {
		t.Fatalf("below threshold should pass, got %d (ok=%v)", len(choices), ok)
	}
	if m.askCount != 2 {
		t.Errorf("askCount should increment to 2, got %d", m.askCount)
	}
}

// TestCheckAskUserChoice_CacheHit 验证缓存命中时跳过对应 PendingChoice（REF-降级策略-优化3，REF-T3-S5-新增）。
func TestCheckAskUserChoice_CacheHit(t *testing.T) {
	m := newChoiceTestModel(3)
	// 预填充缓存：同 category + 同 question
	m.askCache.Set("dialogue-clarify", "已问过的问题", tools.ClarifyAnswer{QuestionID: "cached"})

	results := []tools.Result{
		makeAskUserResult("t1", tools.AskUserResult{
			Question: "已问过的问题",
			Category: "dialogue-clarify",
			Options:  []tools.AskUserOption{{Label: "A", Value: "a"}},
		}),
		makeAskUserResult("t2", tools.AskUserResult{
			Question: "新问题",
			Category: "dialogue-clarify",
			Options:  []tools.AskUserOption{{Label: "B", Value: "b"}},
		}),
	}
	choices, ok := m.checkAskUserChoice(results)
	if !ok || len(choices) != 1 {
		t.Fatalf("expected 1 choice (cache hit filters 1), got %d (ok=%v)", len(choices), ok)
	}
	if choices[0].Prompt != "新问题" {
		t.Errorf("cache should filter 已问过的问题, got %q", choices[0].Prompt)
	}
}

// TestCheckAskUserChoice_ErrorResultSkipped 验证 IsError=true 的 AskUser 结果被跳过（REF-T3-S5-新增）。
func TestCheckAskUserChoice_ErrorResultSkipped(t *testing.T) {
	m := newChoiceTestModel(1)
	results := []tools.Result{
		{
			ID:      "t1",
			Name:    "AskUser",
			Output:  `{"question":"q","options":[]}`,
			IsError: true,
		},
	}
	choices, ok := m.checkAskUserChoice(results)
	if ok {
		t.Errorf("error result should be skipped, got %d choices", len(choices))
	}
}

// TestEscapePrompt_ANSIRemoval 验证 escapePrompt 去除 ANSI 转义序列和控制字符（REF-安全-优化6，REF-T3-S5-新增）。
func TestEscapePrompt_ANSIRemoval(t *testing.T) {
	tests := []struct {
		name  string
		input string
		want  string
	}{
		{
			name:  "纯文本不变",
			input: "用 JWT 还是 Session？",
			want:  "用 JWT 还是 Session？",
		},
		{
			name:  "ANSI 颜色码被去除",
			input: "\x1b[31m红色问题\x1b[0m",
			want:  "红色问题",
		},
		{
			name:  "控制字符被去除（保留换行）",
			input: "第一行\n第二行\t制表符",
			want:  "第一行\n第二行制表符",
		},
		{
			name:  "空字符串",
			input: "",
			want:  "",
		},
		{
			name:  "多个 ANSI 序列",
			input: "\x1b[1;31mError\x1b[0m: \x1b[33mwarning\x1b[0m",
			want:  "Error: warning",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := escapePrompt(tt.input)
			if got != tt.want {
				t.Errorf("escapePrompt(%q) = %q, want %q", tt.input, got, tt.want)
			}
		})
	}
}

// TestCategoryTags 验证 5 类 category 的（英文主行, 中文辅行）标签 + 未知值返回空（更新）。
// ★ ：本用例原断言 renderCategoryLabel 的**纯中文**标签（如 "[编排提问-澄清] "）。
// 该函数已改为 categoryTags + renderAskQuestionLine（英文主行 + 缩进中文辅行），
// 断言随之更新；**意图不变**（5 类标签 + 未知值不产生标签）。
func TestCategoryTags(t *testing.T) {
	tests := []struct {
		category string
		wantEN   string
		wantZH   string
	}{
		{"clarify", "[Orchestrator · Clarify]", "[编排提问-澄清]"},
		{"plan-confirm", "[Orchestrator · Plan confirm]", "[编排提问-方案确认]"},
		{"cost-warning", "[Orchestrator · Cost warning]", "[编排提问-成本警告]"},
		{"scope-confirm", "[Orchestrator · Scope confirm]", "[编排提问-范围确认]"},
		{"dialogue-clarify", "[Dialogue · Clarify]", "[对话澄清]"},
		{"", "", ""},
		{"unknown", "", ""},
	}
	for _, tt := range tests {
		t.Run(tt.category, func(t *testing.T) {
			en, zh := categoryTags(tt.category)
			if en != tt.wantEN || zh != tt.wantZH {
				t.Errorf("categoryTags(%q) = (%q, %q), want (%q, %q)", tt.category, en, zh, tt.wantEN, tt.wantZH)
			}
		})
	}
}

// TestRenderAskQuestionLine 验证「❓ 提问行」的双语形态（新增）：
// 英文主行 = 标签 + 提问；中文辅行 = 缩进的中文标签；未知 category 不得产生孤立空白行。
func TestRenderAskQuestionLine(t *testing.T) {
	got := renderAskQuestionLine("clarify", "Which approach?")
	want := "❓ [Orchestrator · Clarify] Which approach?\n" + copyIndent + "[编排提问-澄清]"
	if got != want {
		t.Errorf("renderAskQuestionLine = %q, want %q", got, want)
	}
	// 未知 category：只保留提问行（无孤立中文辅行 / 前导空白）。
	if got := renderAskQuestionLine("unknown", "Q"); got != "❓ Q" {
		t.Errorf("renderAskQuestionLine(unknown) = %q, want %q", got, "❓ Q")
	}
	// 形态门：含汉字的行必须以 copyIndent 开头（对渲染产物施加，不施加到模型写的提问上）。
	if v := util.AssertChineseOnlyOnIndentedLines(renderAskQuestionLine("clarify", "Q")); v != "" {
		t.Errorf("形态违规：%q", v)
	}
}

// TestCheckAskUserChoice_AskCountNotIncrementedOnNoHit 验证无命中时 askCount 不增加（REF-T3-S5-新增）。
func TestCheckAskUserChoice_AskCountNotIncrementedOnNoHit(t *testing.T) {
	m := newChoiceTestModel(3)
	// 全部 allow_free_text=true + 空 options → 全跳过
	results := []tools.Result{
		makeAskUserResult("t1", tools.AskUserResult{
			Question:      "q1",
			Category:      "dialogue-clarify",
			AllowFreeText: true,
		}),
	}
	_, ok := m.checkAskUserChoice(results)
	if ok {
		t.Errorf("should not have choices when all skipped")
	}
	if m.askCount != 0 {
		t.Errorf("askCount should not increment when no hit, got %d", m.askCount)
	}
}
