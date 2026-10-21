package tui

// 97% Blocked 兜底线（压缩体系重构 2026-08-31）：
//   - Blocked 不再承担压缩职责：窗口已满时 AI 压缩请求本身可能撑爆/截断
//     （教训：8000 token 窗口压缩失败死锁），只做硬停保可恢复
//   - 硬停输出"最小交接模板"：不依赖 AI 的固定结构（任务目标/当前状态/关键文件），
//     从 session 数据（FirstUserInput + 消息统计 + 工具调用中的文件路径）填充，
//     零额外生成成本（P4 职责分离：暂停点只做进度快照，交接文档是独立导出命令）
//   - 恢复指引对齐既有死锁建议约定：/clear / /copy

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/anchorandfish/whale-code/internal/config"
	ctxpkg "github.com/anchorandfish/whale-code/internal/context"
	"github.com/anchorandfish/whale-code/internal/session"
)

// autoCompactDue 报告是否应触发主力自动压缩（AI 压缩）。
// 触发条件：AutoCompact 开启 且 越过主力线（contextWindow - AutoCompactBufferTokens，
// 由 Budget.ShouldAutoCompact 判定，BodyAfterPrefix 同口径） 且 未达 97% Blocked
// （Blocked 只做硬停，不再尝试压缩）。
func autoCompactDue(cfg *config.Config, budget ctxpkg.Budget, estimatedTokens, cachedInputTokens int) bool {
	if cfg == nil || !cfg.AutoCompact {
		return false
	}
	if budget.CheckState(estimatedTokens, cachedInputTokens) == ctxpkg.StateBlocked {
		return false
	}
	return budget.ShouldAutoCompact(estimatedTokens, cachedInputTokens)
}

// blockedHandoffMessage 渲染 97% 硬停的最小交接模板（纯本地拼装，不调用 AI）。
// percent 为当前上下文使用率（0-100）。
func blockedHandoffMessage(percent float64, firstUserInput string, messages []session.Message) string {
	var sb strings.Builder
	sb.WriteString(fmt.Sprintf("⛔ Context at %.0f%%（97%% 兜底线）已硬停，不再尝试压缩：窗口已满时压缩请求本身可能失败。\n\n", percent))

	sb.WriteString("[最小交接模板]\n")
	goal := strings.TrimSpace(firstUserInput)
	if goal == "" {
		goal = "(无——本会话首条用户输入为空)"
	} else if runes := []rune(goal); len(runes) > 120 {
		goal = string(runes[:120]) + "..."
	}
	sb.WriteString(fmt.Sprintf("任务目标: %s\n", goal))
	sb.WriteString(fmt.Sprintf("当前状态: 会话消息 %d 条 / 上下文已用 %.0f%%\n", len(messages), percent))

	files := extractKeyFilesFromMessages(messages)
	if len(files) == 0 {
		files = []string{"(本会话工具调用中未提取到文件路径)"}
	}
	sb.WriteString(fmt.Sprintf("关键文件: %s\n", strings.Join(files, ", ")))

	sb.WriteString("\n恢复指引:\n")
	sb.WriteString("  /copy  导出当前对话（手动接力上下文）\n")
	sb.WriteString("  /clear 开新会话（本会话已保存，可随时用 /sessions 切回）\n")
	return sb.String()
}

// extractKeyFilesFromMessages 从会话工具调用块中提取文件路径（去重，最多 10 个）。
// Input 为工具调用的 JSON 参数串，兼容常见键名（file_path/path/filename/file/dir）。
func extractKeyFilesFromMessages(messages []session.Message) []string {
	const maxFiles = 10
	seen := make(map[string]bool)
	var files []string
	for _, msg := range messages {
		for _, block := range msg.Blocks {
			if block.Type != "tool_use" || block.Input == "" {
				continue
			}
			var raw map[string]interface{}
			if err := json.Unmarshal([]byte(block.Input), &raw); err != nil {
				continue
			}
			for _, key := range []string{"file_path", "path", "filename", "file", "dir"} {
				if v, ok := raw[key].(string); ok && v != "" {
					if !seen[v] {
						seen[v] = true
						files = append(files, v)
						if len(files) >= maxFiles {
							return files
						}
					}
					break
				}
			}
		}
	}
	return files
}
