package tui

import (
	"testing"
	"time"

	"github.com/anchorandfish/whale-code/internal/ai"
	"github.com/anchorandfish/whale-code/internal/command"
	"github.com/anchorandfish/whale-code/internal/config"
	ctxpkg "github.com/anchorandfish/whale-code/internal/context"
	"github.com/anchorandfish/whale-code/internal/session"
)

// TestHandlePlan 测试 /plan 命令处理
func TestHandlePlan(t *testing.T) {
	m := createMinimalModel()

	tests := []struct {
		name        string
		args        string
		wantPlan    bool
		wantContain string
	}{
		{"开启计划模式", "on", true, "已开启"},
		{"关闭计划模式", "off", false, "已关闭"},
		{"无参数显示关闭状态", "", false, "关闭"},
		{"无参数显示开启态", "", true, "开启"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			m.planMode = tt.wantPlan
			result := m.handlePlan(tt.args)
			if tt.name == "无参数显示开启态" && !m.planMode {
				m.planMode = true
				result = m.handlePlan(tt.args)
			}
			if !containsStr(result, tt.wantContain) {
				t.Errorf("handlePlan(%q) = %q, want contain %q", tt.args, result, tt.wantContain)
			}
		})
	}
}

// TestHandlePlanToggle 测试 /plan 无参数时切换
func TestHandlePlanToggle(t *testing.T) {
	m := createMinimalModel()
	m.planMode = false
	result := m.handlePlan("toggle")
	if !m.planMode {
		t.Error("planMode should be toggled to true")
	}
	if !containsStr(result, "切换") {
		t.Errorf("handlePlan toggle should contain 切换, got: %s", result)
	}
}

// TestHandlePrivacy 测试 /privacy 命令处理
func TestHandlePrivacy(t *testing.T) {
	m := createMinimalModel()

	t.Run("显示默认设置", func(t *testing.T) {
		result := m.handlePrivacy("")
		if !containsStr(result, "隐私设置") {
			t.Errorf("privacy show should contain 隐私设置, got: %s", result)
		}
		if !containsStr(result, "关闭") {
			t.Error("default privacy should show 关闭")
		}
	})

	t.Run("开启遥测", func(t *testing.T) {
		result := m.handlePrivacy("analytics-on")
		if !containsStr(result, "遥测分析已开启") {
			t.Errorf("analytics-on should confirm, got: %s", result)
		}
		if !m.privacyConfig.AnalyticsEnabled {
			t.Error("AnalyticsEnabled should be true")
		}
	})

	t.Run("关闭遥测", func(t *testing.T) {
		m.privacyConfig.AnalyticsEnabled = true
		result := m.handlePrivacy("analytics-off")
		if !containsStr(result, "遥测分析已关闭") {
			t.Errorf("analytics-off should confirm, got: %s", result)
		}
		if m.privacyConfig.AnalyticsEnabled {
			t.Error("AnalyticsEnabled should be false")
		}
	})

	t.Run("未知子命令", func(t *testing.T) {
		result := m.handlePrivacy("unknown")
		if !containsStr(result, "未知子命令") {
			t.Errorf("unknown subcmd should error, got: %s", result)
		}
	})
}

// TestHandleStats 测试 /stats 命令处理
func TestHandleStats(t *testing.T) {
	m := createMinimalModel()

	t.Run("基本统计信息", func(t *testing.T) {
		m.inputTokens = 1234
		m.outputTokens = 5678
		m.compactCount = 2
		result := m.handleStats()
		if !containsStr(result, "统计信息") {
			t.Error("stats should contain 统计信息")
		}
		if !containsStr(result, "1,234") {
			t.Error("stats should show input tokens with comma formatting")
		}
		if !containsStr(result, "5,678") {
			t.Error("stats should show output tokens with comma formatting")
		}
		if !containsStr(result, "压缩次数: 2") {
			t.Error("stats should show compact count")
		}
	})

	t.Run("含用量追踪器数据", func(t *testing.T) {
		m.usageTracker = ai.NewUsageTracker("")
		m.usageTracker.RecordUsage("test-model", 1000, 2000, 100, 500)
		result := m.handleStats()
		if !containsStr(result, "累计用量") {
			t.Error("stats with tracker should contain 累计用量")
		}
		if !containsStr(result, "总输入") {
			t.Error("stats should show total input")
		}
	})

	t.Run("零值统计", func(t *testing.T) {
		m2 := createMinimalModel()
		result := m2.handleStats()
		if !containsStr(result, "统计信息") {
			t.Error("even zero stats should return header")
		}
	})
}

// TestHandleSummary 测试 /summary 命令处理
func TestHandleSummary(t *testing.T) {
	m := createMinimalModel()

	t.Run("对话不足", func(t *testing.T) {
		result := m.handleSummary("")
		if !containsStr(result, "对话内容不足") {
			t.Errorf("empty session should say insufficient, got: %s", result)
		}
	})

	t.Run("正常对话摘要", func(t *testing.T) {
		m.session.SetMessages([]session.Message{
			{Role: "user", Blocks: []session.ContentBlock{{Type: "text", Text: "请帮我fixed app.go 中的 bug"}}},
			{Role: "assistant", Blocks: []session.ContentBlock{{Type: "text", Text: "我来分析问题..."}}},
			{Role: "user", Blocks: []session.ContentBlock{{Type: "text", Text: "好的"}}},
			{Role: "assistant", Blocks: []session.ContentBlock{{Type: "text", Text: "fixed完成"}}},
		})
		m.firstUserInput = "请帮我fixed app.go 中的 bug"
		m.session.Metadata.TurnCount = 2
		m.session.Metadata.CreatedAt = time.Now().Add(-5 * time.Minute).UnixMilli()
		result := m.handleSummary("")
		if !containsStr(result, "对话摘要") {
			t.Error("summary should contain 对话摘要")
		}
		if !containsStr(result, "主题:") {
			t.Error("summary should contain 主题:")
		}
		if !containsStr(result, "轮次:") {
			t.Error("summary should contain 轮次:")
		}
		if !containsStr(result, "消息统计") {
			t.Error("summary should contain 消息统计")
		}
	})

	t.Run("提取文件名", func(t *testing.T) {
		m2 := createMinimalModel()
		m2.session.SetMessages([]session.Message{
			{Role: "user", Blocks: []session.ContentBlock{{Type: "text", Text: "查看 main.go 和 utils.ts 的代码"}}},
			{Role: "assistant", Blocks: []session.ContentBlock{{Type: "text", Text: "正在读取 main.go..."}}},
			{Role: "tool", Blocks: []session.ContentBlock{{Type: "tool_result", Text: "main.go content here"}}},
			{Role: "assistant", Blocks: []session.ContentBlock{{Type: "text", Text: "utils.ts 分析结果..."}}},
		})
		m2.session.Metadata.TurnCount = 2
		result := m2.handleSummary("")
		if !containsStr(result, "涉及文件") {
			t.Error("summary with files should contain 涉及文件")
		}
	})
}

// TestHandleThinkback 测试 /thinkback 命令处理
//
// ★ 2026-10-03 数据源收口：思考内容的**唯一真相源 = 会话存档**
// （assistant 消息里的 thinking 块，由 `handleStreamDone` 落盘）。
// 本用例原先往 TUI 侧内存存储（`m.currentThinking` / `m.thinkingHistory`）塞数据来驱动展示，
// 而那两个存储在生产**零写入点**、已随本轮删除 ⇒ 现改为把思考块写进 `session.Message`，
// 走与真机**同一条**读路径（R10：翻转用例语义，而不是削弱断言）。
//
// 原 `TestSaveThinkingRecord`（及其「上限 50 条」断言）随 `saveThinkingRecord` 一并删除：
// 「思考落盘」这一事实现由 `TestStreamDonePersistsThinkingIntoTranscript` 从生产入口
// `handleStreamDone` 判定；「不限条数、列全会话」由 `TestThinkbackAllListsEveryTurnFromTranscript`
// 判定。留旧断言只会把「已废的上限语义」固化成假绿。
func TestHandleThinkback(t *testing.T) {
	t.Run("无记录", func(t *testing.T) {
		m := createMinimalModel()
		result := m.handleThinkback("")
		if !containsStr(result, "暂无思考记录") {
			t.Errorf("no records should say so, got: %s", result)
		}
	})

	t.Run("有当前思考内容", func(t *testing.T) {
		m := createMinimalModel()
		m.sessionService.SetCurrentSession(transcriptWithThinking(
			"解释一下这个算法",
			"这是一个复杂的算法问题，需要考虑时间复杂度和空间复杂度的平衡...",
			"答案是 O(n log n)。"))
		result := m.handleThinkback("")
		if !containsStr(result, "最近一次思考过程") {
			t.Errorf("with thinking in transcript should show it, got: %s", result)
		}
		if !containsStr(result, "字符") {
			t.Errorf("should show character count, got: %s", result)
		}
		if !containsStr(result, "时间复杂度和空间复杂度") {
			t.Errorf("应展示存档里的思考原文, got: %s", result)
		}
	})

	t.Run("有历史记录-all", func(t *testing.T) {
		m := createMinimalModel()
		s := session.NewSession()
		s.SetMessages([]session.Message{
			session.UserText("如何optimization"),
			session.AssistantWithTools(session.WithThinkingBlock(nil, "需要从算法层面..."), "test-model", nil),
			session.UserText("refactoring建议"),
			session.AssistantWithTools(session.WithThinkingBlock(nil, "建议使用工厂模式..."), "test-model", nil),
		})
		m.sessionService.SetCurrentSession(s)

		result := m.handleThinkback("all")
		if !containsStr(result, "思考回顾") {
			t.Error("thinkback all should show header")
		}
		if !containsStr(result, "记录 #1") {
			t.Error("should show record #1")
		}
		if !containsStr(result, "记录 #2") {
			t.Error("should show record #2")
		}
		if !containsStr(result, "需要从算法层面") || !containsStr(result, "建议使用工厂模式") {
			t.Errorf("应展示两条存档思考原文, got: %s", result)
		}
	})

	t.Run("清空记录只清思考、保留对话", func(t *testing.T) {
		m := createMinimalModel()
		m.sessionService.SetCurrentSession(transcriptWithThinking("test", "test content", "对话正文"))

		result := m.handleThinkback("clear")
		if !containsStr(result, "思考记录已清空") {
			t.Errorf("clear should confirm, got: %s", result)
		}
		msgs := m.sessionService.CurrentSession().GetMessages()
		for i, msg := range msgs {
			if msg.HasThinking() {
				t.Errorf("clear 后第 %d 条消息仍带思考块：%+v", i, msg.Blocks)
			}
		}
		if len(msgs) != 2 || !containsStr(msgs[1].TextContent(), "对话正文") {
			t.Errorf("clear 只应清思考记录、不得删对话本体：%+v", msgs)
		}
	})
}

// TestHandleUsage 测试 /usage 命令处理
func TestHandleUsage(t *testing.T) {
	m := createMinimalModel()

	t.Run("无用量追踪器", func(t *testing.T) {
		m.usageTracker = nil
		result := m.handleUsage()
		if !containsStr(result, "未初始化") {
			t.Errorf("nil tracker should say not initialized, got: %s", result)
		}
	})

	t.Run("有数据", func(t *testing.T) {
		m.usageTracker = ai.NewUsageTracker("")
		m.usageTracker.RecordUsage("claude-sonnet-4-20250514", 1500, 3000, 200, 800)
		m.usageTracker.RecordUsage("claude-3-5-haiku-20241022", 800, 2000, 100, 400)
		result := m.handleUsage()
		if !containsStr(result, "Token Usage Report") {
			t.Error("usage with data should show report header")
		}
		if !containsStr(result, "Total") {
			t.Error("should show Total section")
		}
		// 本地估算费用已移除：provider 不返回费用详情，本地价格表估算与真实账单不符
		if containsStr(result, "Cost") {
			t.Error("should not show local cost estimate")
		}
	})

	t.Run("空追踪器", func(t *testing.T) {
		m.usageTracker = ai.NewUsageTracker("")
		result := m.handleUsage()
		if result == "" {
			t.Error("even empty tracker should return something")
		}
	})
}

// TestHandleBreakCache 测试 /break-cache 命令处理
func TestHandleBreakCache(t *testing.T) {
	m := createMinimalModel()

	t.Run("正常打断", func(t *testing.T) {
		m.cacheBroken = false
		m.cacheBreakCount = 2
		result := m.handleBreakCache("")
		if !m.cacheBroken {
			t.Error("cacheBroken should be set to true")
		}
		if m.cacheBreakCount != 3 {
			t.Errorf("cacheBreakCount should be 3, got %d", m.cacheBreakCount)
		}
		if !containsStr(result, "缓存已打断") {
			t.Error("should confirm cache break")
		}
		if !containsStr(result, "第 3 次") {
			t.Error("should show break count")
		}
	})

	t.Run("查看状态-正常", func(t *testing.T) {
		m.cacheBroken = false
		m.cacheBreakCount = 0
		result := m.handleBreakCache("status")
		if !containsStr(result, "正常") {
			t.Errorf("status normal should say 正常, got: %s", result)
		}
	})

	t.Run("查看状态-已触发", func(t *testing.T) {
		m.cacheBroken = true
		m.cacheBreakCount = 5
		result := m.handleBreakCache("status")
		if !containsStr(result, "已触发") {
			t.Errorf("status triggered should say 已触发, got: %s", result)
		}
		if !containsStr(result, "第 5 次") {
			t.Error("should show count in status")
		}
	})
}

// TestHandleOnboarding 测试 /onboarding 命令处理
func TestHandleOnboarding(t *testing.T) {
	m := createMinimalModel()

	t.Run("完整引导", func(t *testing.T) {
		result := m.handleOnboarding("")
		if !containsStr(result, "欢迎使用 Whale Code") {
			t.Error("onboarding should welcome user")
		}
		if !containsStr(result, "核心命令") {
			t.Error("onboarding should have core commands section")
		}
		if !containsStr(result, "/help") {
			t.Error("should mention /help")
		}
		if !containsStr(result, "/model") {
			t.Error("should mention /model")
		}
		if !containsStr(result, "/compact") {
			t.Error("should mention /compact")
		}
		if !containsStr(result, "权限与安全") {
			t.Error("should have security section")
		}
		if !containsStr(result, "会话管理") {
			t.Error("should have session section")
		}
		if !containsStr(result, "Git 集成") {
			t.Error("should have git section")
		}
		if !containsStr(result, "进阶功能") {
			t.Error("should have advanced section")
		}
		if !containsStr(result, "快捷键") {
			t.Error("should have shortcuts section")
		}
		if len(result) < 500 {
			t.Errorf("onboarding should be substantial, got %d chars", len(result))
		}
	})

	t.Run("跳过引导", func(t *testing.T) {
		result := m.handleOnboarding("skip")
		if !containsStr(result, "跳过") {
			t.Errorf("skip should confirm, got: %s", result)
		}
	})
}

// TestHandleTasks 测试 /tasks 命令处理
func TestHandleTasks(t *testing.T) {
	m := createMinimalModel()

	t.Run("Daemon 未初始化", func(t *testing.T) {
		m.daemon = nil
		m.cmdRegistry.OnDaemon = nil
		result := m.handleTasks("")
		if !containsStr(result, "未启动") {
			t.Errorf("no daemon should say not started, got: %s", result)
		}
	})
}

// TestHandleContext 测试 /context 命令处理
func TestHandleContext(t *testing.T) {
	m := createMinimalModel()
	m.config = &config.Config{ContextWindow: 200000}

	t.Run("正常上下文", func(t *testing.T) {
		m.inputTokens = 45000
		m.outputTokens = 12000
		m.compactCount = 2
		result := m.handleContext()
		if !containsStr(result, "上下文信息") {
			t.Error("context should show header")
		}
		if !containsStr(result, "总容量") {
			t.Error("should show total capacity")
		}
		if !containsStr(result, "200,000") {
			t.Error("should show context window size")
		}
		if !containsStr(result, "已使用") {
			t.Error("should show usage")
		}
		if !containsStr(result, "输入: 45,000 | Output 输出: 12,000") {
			t.Error("should show cumulative provider usage separately")
		}
		if !containsStr(result, "压缩:") {
			t.Error("should show compression info")
		}
		if !containsStr(result, "已压缩: 2 times 次") {
			t.Error("should show compact count")
		}
	})

	t.Run("高占用警告", func(t *testing.T) {
		m2 := createMinimalModel()
		m2.config = &config.Config{ContextWindow: 200000}
		m2.requestSnapshot = contextRequestSnapshot{SystemPrompt: 170000, Valid: true}
		result := m2.handleContext()
		if !containsStr(result, "⚠️") {
			t.Error("high usage should show warning")
		}
		if !containsStr(result, "较高") {
			t.Error("should suggest compact")
		}
	})
}

// TestFormatIntWithComma 测试千分位格式化
func TestFormatIntWithComma(t *testing.T) {
	tests := []struct {
		input    int
		expected string
	}{
		{0, "0"},
		{5, "5"},
		{100, "100"},
		{999, "999"},
		{1000, "1,000"},
		{1234, "1,234"},
		{10000, "10,000"},
		{123456, "123,456"},
		{1000000, "1,000,000"},
		{1234567, "1,234,567"},
	}
	for _, tt := range tests {
		got := formatIntWithComma(tt.input)
		if got != tt.expected {
			t.Errorf("formatIntWithComma(%d) = %q, want %q", tt.input, got, tt.expected)
		}
	}
}

// TestFormatTokensForCount 测试 token 数量格式化
func TestFormatTokensForCount(t *testing.T) {
	tests := []struct {
		input   int
		contain string
	}{
		{0, "0"},
		{500, "500"},
		{999, "999"},
		{1000, "k"},
		{5000, "k"},
		{999999, "999.9k"},
		{1000000, "1.0M"},
		{2500000, "2.5M"},
	}
	for _, tt := range tests {
		got := formatTokensForCount(tt.input)
		if !containsStr(got, tt.contain) {
			t.Errorf("formatTokensForCount(%d) = %q, want contain %q", tt.input, got, tt.contain)
		}
	}
}

// createMinimalModel creates a minimal Model instance for testing
func createMinimalModel() *Model {
	cfg := &config.Config{
		ContextWindow:  200000,
		PermissionMode: "default",
		Model:          "test-model",
		FallbackModel:  "",
		VimMode:        false,
		AutoCompact:    true,
	}
	m := &Model{
		SessionModel: SessionModel{
			session:         session.NewSession(),
			config:          cfg,
			store:           nil,
			usageTracker:    nil,
			modelName:       "test-model",
			availableModels: []string{"test-model"},
		},
		AIModel: AIModel{
			compactor:     nil,
			aiCompactor:   nil,
			estimator:     ai.NewTokenEstimator(),
			budget:        ctxpkg.DefaultBudget(200000),
			magicDocs:     nil,
			memStore:      nil,
			memIndex:      nil,
			memSelector:   nil,
			memExtractor:  nil,
			modelRegistry: ai.NewModelRegistry(),
		},
		ToolModel: ToolModel{
			cmdRegistry:   new(command.Registry),
			policy:        nil,
			monitorMgr:    nil,
			loopScheduler: nil,
			searcher:      nil,
		},
	}
	m.reassignServices() // 初始化服务指针，指向自身子结构体，避免 nil pointer
	return m
}

// containsStr 检查字符串是否包含子串
func containsStr(s, substr string) bool {
	for i := 0; i+len(substr) <= len(s); i++ {
		if s[i:i+len(substr)] == substr {
			return true
		}
	}
	return false
}
