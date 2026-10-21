// 状态说明：本文件已集成到渲染循环（TUI inline scrollback 批次2流式集成）。
// 活动状态下 tick 8ms 检查 dirty + ShouldRender 7ms 节流，实现突发响应 ≤8ms；
// 无 delta 时不重绘，spinner 由主循环独立限制为 80ms 更新一次。
// 见 内部交付台账

package tui

import "time"

// FrameLimiter 帧率限制器，控制 TUI 渲染频率上限。
//
// 对标同类 CLI frame_rate_limiter.rs（REF-外部: 同类 CLI）：
// MIN_FRAME_INTERVAL = 8_333_334 纳秒（约 8.33ms，120FPS 上限）。
// 事件风暴时合并渲染请求，避免 CPU 空转和过度重绘。
//
// 设计：
// - ShouldRender() 判断距上次渲染是否已过 minInterval
// - MarkEmitted() 记录本次渲染时间
// - 节流只限制渲染频率，不丢 delta（active_cell.text 持续累积，渲染时取最新）
type FrameLimiter struct {
	minInterval time.Duration
	lastEmit    time.Time
}

// NewFrameLimiter 创建帧率限制器，默认 7ms。
// 7ms < tick 8ms，使每个 tick 的 ShouldRender=true（8ms >= 7ms），达成 ~120fps 渲染。
// 原 8.33ms 导致 8ms < 8.33ms，每 2 tick 跳过 1 次，实际 ~62fps。
// REF-外部: 同类 CLI MIN_FRAME_INTERVAL=8.33ms，但同类 CLI 用事件驱动非 tick 驱动，无此问题。
func NewFrameLimiter() *FrameLimiter {
	return &FrameLimiter{
		minInterval: 7 * time.Millisecond,
	}
}

// ShouldRender 返回是否应该渲染（距上次渲染已超过 minInterval，或首次渲染）。
func (f *FrameLimiter) ShouldRender() bool {
	if f.lastEmit.IsZero() {
		return true
	}
	return time.Since(f.lastEmit) >= f.minInterval
}

// MarkEmitted 记录本次渲染时间。REF-外部: 同类 CLI FrameRateLimiter mark_emitted。
func (f *FrameLimiter) MarkEmitted() {
	f.lastEmit = time.Now()
}

// Reset 重置帧率状态（流式结束或切换会话时调用）。
func (f *FrameLimiter) Reset() {
	f.lastEmit = time.Time{}
}
