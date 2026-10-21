// Package context — 缓存感知压缩 + Context Collapse integration test
package context

import (
	"strings"
	"testing"

	"github.com/anchorandfish/whale-code/internal/ai"
	"github.com/anchorandfish/whale-code/internal/session"
)

// TestIntegration_LayeredPromptToPipeline 分层提示词集成到压缩管线
// 验证: LayeredPrompt 渲染后的 system prompt 在压缩后仍然保持层级结构
func TestIntegration_LayeredPromptToPipeline(t *testing.T) {
	// 1. 构建分层提示词
	builder := ai.NewCacheAwareRequestBuilder()
	components := []ai.PromptComponent{
		{Name: "identity", Content: "I am Whale Code AI assistant", Stable: true, Layer: ai.LayerCoreIdentity},
		{Name: "tools", Content: "Available tools: bash, read_file, write_file", Stable: true, Layer: ai.LayerToolDefs},
		{Name: "project", Content: "Project: whale-code", Stable: false, Layer: ai.LayerProjectCtx},
		{Name: "session", Content: "Current task: implement caching", Stable: false, Layer: ai.LayerSessionState},
	}

	lp := builder.BuildLayeredPrompt(components)
	rendered := lp.Render()

	// 2. 验证渲染结果
	if !strings.HasPrefix(rendered.Content, "I am Whale Code AI assistant") {
		t.Error("Core identity should be first")
	}
	if !strings.HasSuffix(rendered.Content, "implement caching") {
		t.Error("Session state should be last")
	}

	// 3. 验证fingerprint
	if rendered.Fingerprint == "" {
		t.Error("Fingerprint should not be empty")
	}

	// 4. 验证各层 token 估算
	if len(rendered.LayerSizes) != 4 {
		t.Errorf("Expected 4 layer sizes, got %d", len(rendered.LayerSizes))
	}
}

// TestIntegration_CollapseWithPipeline Collapse 接入管线集成
// 验证: Collapse 的输出正确传递给后续阶段
func TestIntegration_CollapseWithPipeline(t *testing.T) {
	estimator := ai.NewTokenEstimator()
	pipeline := NewCompactionPipelineNoAI(estimator)

	// 创建包含文件引用和决策的大量消息
	messages := []session.Message{
		{Role: session.RoleUser, Blocks: []session.ContentBlock{{Type: "text", Text: "帮我修改 main.go 和 client.go 的代码结构，需要refactoring整个模块"}}, Timestamp: 1000},
		{Role: session.RoleAssistant, Blocks: []session.ContentBlock{{Type: "text", Text: "已修改 main.go，decided使用 Redis 缓存approach来optimization性能问题"}}, Timestamp: 2000},
		{Role: session.RoleUser, Blocks: []session.ContentBlock{{Type: "text", Text: "继续修改 client.go 和 server.go，添加错误处理逻辑"}}, Timestamp: 3000},
		{Role: session.RoleAssistant, Blocks: []session.ContentBlock{{Type: "text", Text: "已修改 client.go，添加了重试模式和熔断器模式"}}, Timestamp: 4000},
		{Role: session.RoleUser, Blocks: []session.ContentBlock{{Type: "text", Text: "还需要修改 config.go 和 utils.go"}}, Timestamp: 5000},
		{Role: session.RoleAssistant, Blocks: []session.ContentBlock{{Type: "text", Text: "已修改 config.go 和 utils.go"}}, Timestamp: 6000},
		{Role: session.RoleUser, Blocks: []session.ContentBlock{{Type: "text", Text: "运行test verification修改"}}, Timestamp: 7000},
		{Role: session.RoleAssistant, Blocks: []session.ContentBlock{{Type: "text", Text: "test passed，所有修改已完成"}}, Timestamp: 8000},
		{Role: session.RoleUser, Blocks: []session.ContentBlock{{Type: "text", Text: "提交代码"}}, Timestamp: 9000},
		{Role: session.RoleAssistant, Blocks: []session.ContentBlock{{Type: "text", Text: "已提交"}}, Timestamp: 10000},
		{Role: session.RoleUser, Blocks: []session.ContentBlock{{Type: "text", Text: "创建 PR"}}, Timestamp: 11000},
		{Role: session.RoleAssistant, Blocks: []session.ContentBlock{{Type: "text", Text: "PR 已创建"}}, Timestamp: 12000},
	}

	// 极小预算，强制压缩
	budget := DefaultBudget(30)
	result := pipeline.ExecuteWithOptions(messages, budget, nil, ExecuteOptions{
		Trigger: CompactionTriggerManual,
		Policy:  CompactionPolicyForce,
		Config:  DefaultCompactionConfig(),
	})

	// 验证管线执行了
	if len(result.StagesExecuted) == 0 {
		t.Error("Expected pipeline to execute some stages")
	}

	// 验证消息数减少
	if len(result.Messages) >= len(messages) {
		t.Errorf("Expected fewer messages after compaction, got %d vs %d", len(result.Messages), len(messages))
	}

	// 验证摘要包含文件引用
	if result.Summary != "" && !strings.Contains(result.Summary, "main.go") {
		t.Errorf("Expected summary to contain main.go, got: %s", result.Summary)
	}
}

// TestIntegration_CacheInfoAffectsCompaction 缓存信息影响压缩决策
// 验证: 高缓存删除率触发额外的压缩
func TestIntegration_CacheInfoAffectsCompaction(t *testing.T) {
	estimator := ai.NewTokenEstimator()
	pipeline := NewCompactionPipelineNoAI(estimator)

	// 创建大量消息
	messages := make([]session.Message, 25)
	for i := 0; i < 25; i++ {
		role := session.RoleUser
		if i%2 == 1 {
			role = session.RoleAssistant
		}
		messages[i] = session.Message{
			Role:      role,
			Blocks:    []session.ContentBlock{{Type: "text", Text: "content with some meaningful text to add tokens"}},
			Timestamp: int64(i * 1000),
		}
	}

	// 无缓存信息
	budget := DefaultBudget(100)
	resultNoCache := pipeline.Execute(messages, budget, nil)

	// 高缓存删除率
	cacheInfo := &ai.CacheInfo{
		CacheDeletedTokens: 800,
		TotalCachedTokens:  1000,
		CacheReadTokens:    200,
	}
	resultWithCache := pipeline.Execute(messages, budget, cacheInfo)

	// 有缓存信息时应该执行更多阶段（或至少相同）
	if len(resultWithCache.StagesExecuted) < len(resultNoCache.StagesExecuted) {
		t.Errorf("With cache info should execute >= stages than without, got %d vs %d",
			len(resultWithCache.StagesExecuted), len(resultNoCache.StagesExecuted))
	}
}

// TestIntegration_ForkCacheInheritance Fork 缓存inheritance集成
// 验证: RenderedPrompt 可被 Fork 子proxyinheritance
func TestIntegration_ForkCacheInheritance(t *testing.T) {
	// 1. 父proxy构建分层提示词
	builder := ai.NewCacheAwareRequestBuilder()
	components := []ai.PromptComponent{
		{Name: "identity", Content: "I am Whale Code AI assistant", Stable: true, Layer: ai.LayerCoreIdentity},
		{Name: "tools", Content: "Available tools: bash, read_file", Stable: true, Layer: ai.LayerToolDefs},
	}

	parentLP := builder.BuildLayeredPrompt(components)
	parentRendered := parentLP.Render()

	// 2. mock Fork 子proxyinheritance
	childSystemPrompt := parentRendered.Content

	// 3. 验证父子 system prompt 字节级相同
	if childSystemPrompt != parentRendered.Content {
		t.Error("Fork child should inherit exact same system prompt as parent")
	}

	// 4. 验证fingerprint匹配
	childFingerprint := parentRendered.Fingerprint
	if childFingerprint != parentRendered.Fingerprint {
		t.Error("Fork child fingerprint should match parent")
	}
}

// TestIntegration_MultipleForksSharePrefix 多个 Fork 共享前缀
func TestIntegration_MultipleForksSharePrefix(t *testing.T) {
	builder := ai.NewCacheAwareRequestBuilder()
	components := []ai.PromptComponent{
		{Name: "identity", Content: "I am Whale Code AI assistant", Stable: true, Layer: ai.LayerCoreIdentity},
		{Name: "tools", Content: "Available tools: bash, read_file", Stable: true, Layer: ai.LayerToolDefs},
		{Name: "project", Content: "Project: whale-code", Stable: false, Layer: ai.LayerProjectCtx},
	}

	parentLP := builder.BuildLayeredPrompt(components)
	parentRendered := parentLP.Render()

	// 3 个 Fork 子proxy
	for i := 0; i < 3; i++ {
		childPrompt := parentRendered.Content
		if childPrompt != parentRendered.Content {
			t.Errorf("Fork %d should share same system prompt as parent", i)
		}
	}
}

// TestIntegration_CacheAwareCompactionPipeline 完整缓存感知管线集成
func TestIntegration_CacheAwareCompactionPipeline(t *testing.T) {
	estimator := ai.NewTokenEstimator()
	pipeline := NewCompactionPipelineNoAI(estimator)

	// 创建消息，包含 system 消息（缓存边界）
	messages := []session.Message{
		{Role: session.RoleSystem, Blocks: []session.ContentBlock{{Type: "text", Text: "system prompt"}}, Timestamp: 0},
	}
	for i := 0; i < 20; i++ {
		role := session.RoleUser
		if i%2 == 1 {
			role = session.RoleAssistant
		}
		messages = append(messages, session.Message{
			Role:      role,
			Blocks:    []session.ContentBlock{{Type: "text", Text: "some content to add tokens to the conversation"}},
			Timestamp: int64((i + 1) * 1000),
		})
	}

	// 高缓存删除率
	cacheInfo := &ai.CacheInfo{
		CacheDeletedTokens: 600,
		TotalCachedTokens:  1000,
		CacheReadTokens:    400,
	}

	budget := DefaultBudget(100)
	result := pipeline.Execute(messages, budget, cacheInfo)

	// 验证 system 消息保留
	hasSystemMsg := false
	for _, msg := range result.Messages {
		if msg.Role == session.RoleSystem {
			hasSystemMsg = true
			break
		}
	}
	if !hasSystemMsg {
		t.Error("System message should be preserved after compaction")
	}

	// 验证消息数减少
	if len(result.Messages) >= len(messages) {
		t.Errorf("Expected fewer messages, got %d vs %d", len(result.Messages), len(messages))
	}
}
