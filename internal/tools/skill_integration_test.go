package tools

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/anchorandfish/whale-code/internal/ai"
	"github.com/anchorandfish/whale-code/internal/skill"
)

// skillMockAIClient 是 skill 集成测试用 mock client，跟踪 model 状态以验证 SetModel 与恢复
type skillMockAIClient struct {
	model       string
	setModelCnt int
}

// Stream 返回空流以满足 ai.Client 接口
func (m *skillMockAIClient) Stream(ctx context.Context, req ai.Request) (<-chan ai.Event, error) {
	ch := make(chan ai.Event, 1)
	ch <- ai.MessageStopEvent{}
	close(ch)
	return ch, nil
}

// Model 返回当前 model 名称
func (m *skillMockAIClient) Model() string { return m.model }

// SetModel 覆盖当前 model 并记录调用次数以供断言
func (m *skillMockAIClient) SetModel(model string) {
	m.model = model
	m.setModelCnt++
}

// Close 关闭客户端（mock 无资源需释放）
func (m *skillMockAIClient) Close() error { return nil }

// TestSkillHandler_InjectsModel 验证 skillHandler 调 SetModel 覆盖 + 执行后恢复原 model
func TestSkillHandler_InjectsModel(t *testing.T) {
	e := NewExecutor(NewRegistry(), t.TempDir())

	// 注入 mock AI client，初始 model 为 original-model
	mockAI := &skillMockAIClient{model: "original-model"}
	e.AIClient = mockAI

	// 注入 skill 到 SkillMgr（指定 forced-model）
	e.SkillMgr.Set("test", skill.Skill{
		Name:   "test",
		Prompt: "prompt",
		Model:  "forced-model",
	})

	input := struct {
		Name   string `json:"name"`
		Prompt string `json:"prompt"`
	}{Name: "test", Prompt: "args"}
	inputBytes, _ := json.Marshal(input)

	handler := e.skillHandler()
	_, err := handler(context.Background(), inputBytes)
	if err != nil {
		t.Fatalf("skillHandler 失败: %v", err)
	}

	// 执行后 model 应恢复为 original-model
	if mockAI.model != "original-model" {
		t.Errorf("执行后 model = %q, want original-model（应恢复）", mockAI.model)
	}
	// SetModel 应被调用 2 次（1 次覆盖 + 1 次恢复）
	if mockAI.setModelCnt != 2 {
		t.Errorf("SetModel 调用次数 = %d, want 2（覆盖+恢复）", mockAI.setModelCnt)
	}
}

// TestSkillHandler_NoModel_NoInject 验证 Model 为空时不调 SetModel
func TestSkillHandler_NoModel_NoInject(t *testing.T) {
	e := NewExecutor(NewRegistry(), t.TempDir())

	mockAI := &skillMockAIClient{model: "original-model"}
	e.AIClient = mockAI

	e.SkillMgr.Set("test", skill.Skill{
		Name:   "test",
		Prompt: "prompt",
		// Model 为空
	})

	input := struct {
		Name   string `json:"name"`
		Prompt string `json:"prompt"`
	}{Name: "test"}
	inputBytes, _ := json.Marshal(input)

	handler := e.skillHandler()
	_, err := handler(context.Background(), inputBytes)
	if err != nil {
		t.Fatalf("skillHandler 失败: %v", err)
	}

	// model 应未被改动
	if mockAI.model != "original-model" {
		t.Errorf("Model 为空时不应调 SetModel，model = %q", mockAI.model)
	}
	if mockAI.setModelCnt != 0 {
		t.Errorf("Model 为空时 SetModel 不应被调用，调用次数 = %d", mockAI.setModelCnt)
	}
}

// TestSkillHandler_NotFound 验证调用不存在的 skill 返回失败信息
func TestSkillHandler_NotFound(t *testing.T) {
	e := NewExecutor(NewRegistry(), t.TempDir())

	input := struct {
		Name   string `json:"name"`
		Prompt string `json:"prompt"`
	}{Name: "nonexistent"}
	inputBytes, _ := json.Marshal(input)

	handler := e.skillHandler()
	result, err := handler(context.Background(), inputBytes)
	// skillHandler 内部吞掉错误返回字符串，不向上传播
	if err != nil {
		t.Logf("返回 error（可接受）: %v", err)
	}
	if !strings.Contains(result, "failed") && !strings.Contains(result, "does not exist") {
		t.Errorf("应返回失败信息，got: %s", result)
	}
}
