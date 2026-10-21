// Package tools — AskUser 类型单元测试（T3 S1 落地，）。
// 覆盖 JSON 序列化/反序列化字段一致性 + 边界 case。
package tools

import (
	"encoding/json"
	"strings"
	"testing"
)

// TestAskUserInputJSONRoundTrip 验证 AskUserInput JSON 序列化/反序列化字段一致。
func TestAskUserInputJSONRoundTrip(t *testing.T) {
	original := AskUserInput{
		Question:      "认证方式选哪种？",
		Options:       []AskOption{{Label: "JWT", Description: "无状态 Token", Value: "jwt"}},
		DefaultIdx:    0,
		AllowFreeText: false,
		Category:      "dialogue-clarify",
		Context:       map[string]any{"trace_id": "abc123"},
	}
	data, err := json.Marshal(original)
	if err != nil {
		t.Fatalf("Marshal failed: %v", err)
	}
	var restored AskUserInput
	if err := json.Unmarshal(data, &restored); err != nil {
		t.Fatalf("Unmarshal failed: %v", err)
	}
	if restored.Question != original.Question {
		t.Errorf("Question mismatch: got %q want %q", restored.Question, original.Question)
	}
	if restored.Category != original.Category {
		t.Errorf("Category mismatch: got %q want %q", restored.Category, original.Category)
	}
	if restored.DefaultIdx != original.DefaultIdx {
		t.Errorf("DefaultIdx mismatch: got %d want %d", restored.DefaultIdx, original.DefaultIdx)
	}
	if restored.AllowFreeText != original.AllowFreeText {
		t.Errorf("AllowFreeText mismatch: got %v want %v", restored.AllowFreeText, original.AllowFreeText)
	}
	if len(restored.Options) != 1 || restored.Options[0].Value != "jwt" {
		t.Errorf("Options mismatch: %+v", restored.Options)
	}
	if restored.Context["trace_id"] != "abc123" {
		t.Errorf("Context mismatch: %+v", restored.Context)
	}
}

// TestClarifyQuestionJSONRoundTrip 验证 ClarifyQuestion JSON 字段一致。
func TestClarifyQuestionJSONRoundTrip(t *testing.T) {
	original := ClarifyQuestion{
		ID:            "q1",
		Question:      "用什么数据库？",
		Options:       []AskUserOption{{Label: "MySQL", Value: "mysql"}},
		DefaultIdx:    -1,
		Category:      "clarify",
		AllowFreeText: true,
		Context:       map[string]any{"task_id": "t1"},
	}
	data, err := json.Marshal(original)
	if err != nil {
		t.Fatalf("Marshal failed: %v", err)
	}
	var restored ClarifyQuestion
	if err := json.Unmarshal(data, &restored); err != nil {
		t.Fatalf("Unmarshal failed: %v", err)
	}
	if restored.ID != original.ID || restored.Question != original.Question {
		t.Errorf("ID/Question mismatch: %+v", restored)
	}
	if restored.DefaultIdx != original.DefaultIdx {
		t.Errorf("DefaultIdx mismatch: got %d want %d", restored.DefaultIdx, original.DefaultIdx)
	}
	if restored.Category != original.Category {
		t.Errorf("Category mismatch: got %q want %q", restored.Category, original.Category)
	}
	if !restored.AllowFreeText {
		t.Errorf("AllowFreeText should be true")
	}
}

// TestClarifyAnswerCancelled 验证 Cancelled=true 时 JSON 序列化字段一致（REF-T3-S5-修复）。
// 修复说明：原测试断言 "selected_idx 应被 omitempty 省略"，但 SelectedIdx=-1 不是 int 零值（0），
// omitempty 不会省略 -1。struct 文档明确 SelectedIdx=-1 表示取消/自由输入，是合法值应保留。
// 修复后断言：cancelled:true 存在 + selected_idx:-1 存在 + free_text 省略（空字符串零值）。
func TestClarifyAnswerCancelled(t *testing.T) {
	ans := ClarifyAnswer{
		QuestionID:  "q1",
		SelectedIdx: -1,
		FreeText:    "",
		Cancelled:   true,
	}
	data, err := json.Marshal(ans)
	if err != nil {
		t.Fatalf("Marshal failed: %v", err)
	}
	jsonStr := string(data)
	if !strings.Contains(jsonStr, `"cancelled":true`) {
		t.Errorf("JSON should contain cancelled:true, got: %s", jsonStr)
	}
	// SelectedIdx=-1 是有意义的值（表示取消/自由输入），omitempty 不会省略非零值，应保留
	if !strings.Contains(jsonStr, `"selected_idx":-1`) {
		t.Errorf("JSON should contain selected_idx:-1 (cancelled semantic), got: %s", jsonStr)
	}
	// FreeText="" 是 string 零值，omitempty 应省略
	if strings.Contains(jsonStr, `"free_text"`) {
		t.Errorf("Empty FreeText should be omitted, got: %s", jsonStr)
	}
}

// TestAskOptionFields 验证 AskOption 三字段（Label/Description/Value）对齐 T0 草案。
func TestAskOptionFields(t *testing.T) {
	opt := AskOption{Label: "JWT", Description: "无状态 Token", Value: "jwt"}
	data, _ := json.Marshal(opt)
	var m map[string]any
	_ = json.Unmarshal(data, &m)
	if m["label"] != "JWT" || m["value"] != "jwt" || m["description"] != "无状态 Token" {
		t.Errorf("AskOption fields mismatch: %+v", m)
	}
}
