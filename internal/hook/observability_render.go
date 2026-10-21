// observability_render.go — 钩子可观测性渲染（/ T1-P2-G5-M16Hook可观测性）。
//
// 提供 RenderTrace 字符串渲染（对标 VSCode Task 输出层级格式）和 Stats() 快照查询。
//
// 渲染格式（对标 VSCode Task 输出）：
//
//	[hook trace] 3 hooks, total=15ms
//	├─ PreToolUse hook_a (command, 2ms) → allow
//	├─ PreToolUse hook_b (http, 8ms ⚠️slow) → allow
//	└─ PreToolUse hook_c (command, 5ms) → block
//
// 官方源：
// - VSCode Task 输出: https://code.visualstudio.com/docs/editor/tasks @2026-07-21
package hook

import (
	"fmt"
	"strings"
	"time"
)

// RenderTrace 渲染当前会话的 trace 链路为字符串（对标 VSCode Task 输出）。
//
// 返回值：
// - obs=nil 或 EnableTrace=false 时返回禁用提示
// - 无 span 时返回空提示
// - 有 span 时按时间顺序渲染层级树
//
// 性能：单 span output 截断到 defaultOutputTruncateLen (256 字节)，防止日志爆炸。
// 慢钩子判定使用 obs.cfg.SlowHookThreshold（非全局默认值），确保与统计判定一致。
func (m *Manager) RenderTrace() string {
	if m.obs == nil || !m.obs.cfg.EnableTrace {
		return "[hook trace] observability disabled"
	}
	spans := m.obs.traceBuf.All()
	if len(spans) == 0 {
		return "[hook trace] no spans recorded"
	}
	// 计算总耗时
	var total time.Duration
	for _, s := range spans {
		total += s.Duration
	}
	// 慢钩子阈值：使用配置值（已修正零值），与 stats 判定一致
	threshold := m.obs.cfg.SlowHookThreshold
	var b strings.Builder
	// 头部：hooks 数 + 总耗时
	fmt.Fprintf(&b, "[hook trace] %d hooks, total=%s\n", len(spans), total)
	// 渲染每个 span（顶层 span 不嵌套显示）
	for i, s := range spans {
		last := i == len(spans)-1
		renderSpan(&b, s, "", last, threshold)
	}
	return b.String()
}

// renderSpan 递归渲染单个 span 及其子 span。
//
// 参数：
// - b: 输出 builder
// - s: 待渲染 span
// - prefix: 前缀（用于子 span 缩进）
// - last: 是否为同级最后一个 span（决定用 ├─ 还是 └─）
// - threshold: 慢钩子阈值（与统计判定一致）
func renderSpan(b *strings.Builder, s HookSpan, prefix string, last bool, threshold time.Duration) {
	marker := "├─ "
	if last {
		marker = "└─ "
	}
	// 慢钩子标记（用传入的 threshold，与统计判定一致）
	slow := ""
	if s.Duration >= threshold {
		slow = " ⚠️slow"
	}
	// 错误标记
	errStr := ""
	if s.Error != nil {
		errStr = fmt.Sprintf(" err=%v", s.Error)
	}
	// output 截断（子审查优化 5 吸纳）
	out := s.Output
	if len(out) > defaultOutputTruncateLen {
		out = out[:defaultOutputTruncateLen] + "... (truncated)"
	}
	// 主行：eventType hookID (hookType, duration, slow, err) → action
	fmt.Fprintf(b, "%s%s %s %s (%s, %s%s%s) → %s",
		prefix, marker, s.EventType, s.HookID, s.HookType, s.Duration, slow, errStr, s.Action)
	if out != "" {
		fmt.Fprintf(b, " out=%q", out)
	}
	b.WriteString("\n")
	// 递归渲染子 span
	childPrefix := prefix
	if last {
		childPrefix += "   "
	} else {
		childPrefix += "│  "
	}
	for j, c := range s.Children {
		renderSpan(b, c, childPrefix, j == len(s.Children)-1, threshold)
	}
}

// Stats 返回当前统计快照（按 hook ID 维度）。
//
// 返回 nil 表示统计未启用或 obs=nil；
// 返回的切片为副本，调用方可安全修改。
func (m *Manager) Stats() []HookStatsSnapshot {
	if m.obs == nil || !m.obs.cfg.EnableStats || m.obs.stats == nil {
		return nil
	}
	return m.obs.stats.Snapshot()
}
