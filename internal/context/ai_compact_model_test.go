package context

import (
	"context"
	"fmt"
	"sync"
	"testing"

	"github.com/anchorandfish/whale-code/internal/ai"
)

// compactModelProbeClient 按 req.Model 决定成功/失败，并记录每次请求用到的模型。
//
// 为什么需要它：既有 mockAIClient 的 streamErr 是全局的，无法表达
// "便宜模型挂、主模型好" 这个**降级路径的判别场景**。
type compactModelProbeClient struct {
	ai.Client
	mainModel string
	failModel string // 该模型返回错误；空串表示都不失败

	mu     sync.Mutex
	models []string
}

func (c *compactModelProbeClient) Model() string   { return c.mainModel }
func (c *compactModelProbeClient) SetModel(string) {}
func (c *compactModelProbeClient) Close() error    { return nil }

func (c *compactModelProbeClient) Stream(_ context.Context, req ai.Request) (<-chan ai.Event, error) {
	c.mu.Lock()
	c.models = append(c.models, req.Model)
	c.mu.Unlock()
	if c.failModel != "" && req.Model == c.failModel {
		return nil, fmt.Errorf("probe: model %q unavailable", req.Model)
	}
	return makeMockStream("summary from probe"), nil
}

func (c *compactModelProbeClient) requestModels() []string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]string(nil), c.models...)
}

func compactOnce(c *AICompactor) *CompactResult {
	return c.Compact(makeMessages(20), CompactionConfig{PreserveRecentMessages: 5, MaxEstimatedTokens: 10000})
}

// TestAICompactor_UsesConfiguredCompactModel 钉「压缩模型可配」的**接线**：
// SetCompactModel 之后，摘要请求必须真的带着那个模型名出去，而不是主模型。
func TestAICompactor_UsesConfiguredCompactModel(t *testing.T) {
	client := &compactModelProbeClient{mainModel: "main-model"}
	c := NewAICompactor(client, ai.NewTokenEstimator())
	c.SetCompactModel("cheap-model")

	if got := c.CompactModel(); got != "cheap-model" {
		t.Fatalf("CompactModel()=%q, want cheap-model", got)
	}
	res := compactOnce(c)
	if res.Summary == "" {
		t.Fatal("摘要为空")
	}
	if got := client.requestModels(); len(got) != 1 || got[0] != "cheap-model" {
		t.Fatalf("摘要请求模型 = %v, want [cheap-model]（配置未生效）", got)
	}
}

// TestAICompactor_DefaultCompactModelIsMain 默认（未配置）必须沿用主模型 —— 行为不变。
func TestAICompactor_DefaultCompactModelIsMain(t *testing.T) {
	client := &compactModelProbeClient{mainModel: "main-model"}
	c := NewAICompactor(client, ai.NewTokenEstimator())
	_ = compactOnce(c)

	if got := client.requestModels(); len(got) != 1 || got[0] != "main-model" {
		t.Fatalf("默认摘要请求模型 = %v, want [main-model]", got)
	}
}

// TestAICompactor_FallsBackToMainModelOnCompactModelFailure 降级路径：
// 配了便宜模型但它失败 ⇒ 自动用主模型**重算一次**。
//
// ★ 判据钉的是**请求序列** [cheap, main] —— 不能钉"摘要非空"：Compact 有 L2/L3
// 本地兜底，摘要恒非空，那种断言会被兜底蒙对（假绿）。同时要求最终 fallback 级别
// 是 L1（AI 摘要）而非 L2/L3，证明真的走完了 AI 降级链、拿到了摘要。
func TestAICompactor_FallsBackToMainModelOnCompactModelFailure(t *testing.T) {
	client := &compactModelProbeClient{mainModel: "main-model", failModel: "cheap-model"}
	c := NewAICompactor(client, ai.NewTokenEstimator())
	c.SetCompactModel("cheap-model")

	res := compactOnce(c)
	if res.Summary == "" {
		t.Fatalf("降级后仍应产出摘要，得 %#v", res)
	}
	got := client.requestModels()
	if len(got) != 2 || got[0] != "cheap-model" || got[1] != "main-model" {
		t.Fatalf("请求模型序列 = %v, want [cheap-model main-model]（未回退主模型）", got)
	}
	if lvl := c.LastFallbackLevel(); lvl != FallbackLevelAISummary {
		t.Fatalf("最终 fallback 级别 = %v, want ai_summary（说明回退后仍拿不到 AI 摘要）", lvl)
	}
}

// TestAICompactor_NoFallbackLoopWhenNoCompactModel 未配压缩模型时，失败**不得**多打一次
// （否则是"无配置也降级"的隐式行为变更 + 白花一次调用）。
func TestAICompactor_NoFallbackLoopWhenNoCompactModel(t *testing.T) {
	client := &compactModelProbeClient{mainModel: "main-model", failModel: "main-model"}
	c := NewAICompactor(client, ai.NewTokenEstimator())

	_ = compactOnce(c)
	if got := client.requestModels(); len(got) != 1 {
		t.Fatalf("未配压缩模型时不应重试：请求模型序列 = %v, want 长度 1", got)
	}
}
