package tui

// 97% Blocked 硬停 + 最小交接模板 的失败测试（压缩体系重构 2026-08-31）：
//   - Blocked 不再尝试压缩，只做硬停
//   - 硬停输出不依赖 AI 的固定结构交接模板（任务目标/当前状态/关键文件）
//     + /new、/copy 恢复指引（对齐既有死锁建议约定）
// 断言的是用户可见结果（文案内容），非"执行了"。

import (
	"strings"
	"testing"

	"github.com/anchorandfish/whale-code/internal/config"
	ctxpkg "github.com/anchorandfish/whale-code/internal/context"
	"github.com/anchorandfish/whale-code/internal/session"
)

func TestBlockedHandoffMessageContainsMinimalTemplate(t *testing.T) {
	messages := []session.Message{
		session.UserText("请修复 internal/tui 的流式渲染 bug"),
		{
			Role: session.RoleAssistant,
			Blocks: []session.ContentBlock{
				{Type: "tool_use", Name: "Read", Input: `{"file_path":"/repo/internal/tui/stream.go"}`},
			},
		},
		{
			Role: session.RoleAssistant,
			Blocks: []session.ContentBlock{
				{Type: "tool_use", Name: "Edit", Input: `{"file_path":"/repo/internal/tui/tool_exec.go"}`},
			},
		},
	}

	msg := blockedHandoffMessage(97.5, "请修复 internal/tui 的流式渲染 bug", messages)

	// 固定结构三要素
	for _, want := range []string{"任务目标", "当前状态", "关键文件"} {
		if !strings.Contains(msg, want) {
			t.Errorf("交接模板缺少 %q:\n%s", want, msg)
		}
	}
	// 任务目标来自 FirstUserInput（session metadata 填充，不依赖 AI）
	if !strings.Contains(msg, "请修复 internal/tui 的流式渲染 bug") {
		t.Errorf("交接模板未包含任务目标原文:\n%s", msg)
	}
	// 关键文件从工具调用提取
	for _, file := range []string{"stream.go", "tool_exec.go"} {
		if !strings.Contains(msg, file) {
			t.Errorf("交接模板未包含关键文件 %q:\n%s", file, msg)
		}
	}
	// 恢复指引 /clear /copy —— ★ 2026-10-11 同步：blocked_handoff.go 有意把 /new 改为
	// /clear（"对齐既有死锁建议约定：/clear / /copy"），测试未同步 ⇒ 假红。此处对齐生产文案。
	if !strings.Contains(msg, "/clear") || !strings.Contains(msg, "/copy") {
		t.Errorf("交接模板缺少 /clear //copy 恢复指引:\n%s", msg)
	}
	// 明确说明不再压缩（97% 不承担压缩职责）
	if !strings.Contains(msg, "97") {
		t.Errorf("硬停文案应说明 97%% 兜底线:\n%s", msg)
	}
}

func TestBlockedHandoffMessageEmptySessionStillRenders(t *testing.T) {
	msg := blockedHandoffMessage(97.0, "", nil)
	if !strings.Contains(msg, "任务目标") || !strings.Contains(msg, "当前状态") {
		t.Errorf("空会话也应渲染完整模板结构:\n%s", msg)
	}
}

func TestAutoCompactDueBlockedStopsInsteadOfCompacting(t *testing.T) {
	cfg := config.DefaultConfig()
	cfg.AutoCompact = true
	budget := ctxpkg.DefaultBudget(128000)

	// 90% 主力线（115000 = 128000-13000）：自动压缩
	if !autoCompactDue(&cfg, budget, 115000, 0) {
		t.Error("115000 (=window-13000 主力线) 应触发自动压缩")
	}
	// 80% 提示线：不触发
	if autoCompactDue(&cfg, budget, 102400, 0) {
		t.Error("102400 (80%% 提示线) 不应触发自动压缩")
	}
	// 97% Blocked：硬停，不压缩
	if autoCompactDue(&cfg, budget, 124200, 0) {
		t.Error("124200 (≥97%% Blocked) 不应再触发自动压缩（只做硬停+交接模板）")
	}
	// AutoCompact 关闭：任何档位都不压缩
	cfg.AutoCompact = false
	if autoCompactDue(&cfg, budget, 115000, 0) {
		t.Error("AutoCompact=false 不应触发自动压缩")
	}
}
