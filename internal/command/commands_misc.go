package command

import (
	"fmt"
	"strings"
)

// handleUsage returns token usage details for the current session.
// 不展示本地估算费用：provider 不返回费用详情，本地价格表估算与真实账单不符。
func (r *Registry) handleUsage() string {
	if r.UsageTracker != nil {
		stats := r.UsageTracker.GetSessionStats(r.SessionName)
		return fmt.Sprintf("Token usage for session %s:\n"+
			"  Input tokens:  %d\n  Output tokens: %d\n  Cache read:    %d\n  Cache write:   %d\n",
			r.SessionName, stats.InputTokens, stats.OutputTokens,
			stats.CacheReadTokens, stats.CacheWriteTokens)
	}
	return "Usage tracking not available"
}

// handleStats returns aggregated session statistics（不含本地估算费用）
func (r *Registry) handleStats() string {
	var sb strings.Builder
	sb.WriteString("Session statistics:\n")
	if r.UsageTracker != nil {
		total := r.UsageTracker.GetTotalStats()
		sb.WriteString(fmt.Sprintf("  Total API calls: %d\n", total.Calls))
	}
	if r.Session != nil {
		msgs := r.Session.GetMessages()
		sb.WriteString(fmt.Sprintf("  Messages: %d\n  Turns: %d\n", len(msgs), countTurns(msgs)))
	}
	return sb.String()
}

// handleSummary 触发对话摘要生成。
// B7-P1-1 修复：原实现直接返回静态字符串 "Generating summary..."（假成功），
// 未调用 OnSummary 回调。修复后优先调用 OnSummary 回调返回真实摘要；
// 回调未接线时返回诚实降级提示，避免假成功。
func (r *Registry) handleSummary() string {
	if r.OnSummary != nil {
		return r.OnSummary()
	}
	return "Summary not available in this mode (OnSummary callback not wired)"
}

// handleOnboarding returns interactive onboarding guide text
func (r *Registry) handleOnboarding() string {
	return "Welcome to whale-code!\n" +
		"1. Type your question to start chatting with AI\n" +
		"2. Use /help to see available commands\n" +
		"3. Use /config to customize settings\n" +
		"4. Use /model to switch AI models"
}

// handleShare 导出并分享会话
func (r *Registry) handleShare(args string) string {
	if r.OnExport != nil {
		return r.OnExport("markdown")
	}
	return "Export the conversation first with /export, then share the file"
}

// countTurns counts the number of user turns in a message list
func countTurns(msgs []Message) int {
	count := 0
	for _, m := range msgs {
		if m.Role == "user" {
			count++
		}
	}
	return count
}
