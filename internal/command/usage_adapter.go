package command

import (
	"github.com/anchorandfish/whale-code/internal/ai"
)

// usageTrackerAdapter 把 ai.UsageTracker 适配到 command.UsageTrackerInterface。
// N2 新增：激活 /usage /stats 死代码（5-E 留下的 r.UsageTracker 从未注入）。
// ai.UsageTracker 已有 GetSnapshot/GetTotalAPICalls，适配器做字段映射。
type usageTrackerAdapter struct {
	tracker *ai.UsageTracker
}

// NewUsageTrackerAdapter 创建适配器，t 为 nil 时返回携带 nil tracker 的非 nil 适配器。
// 设计决策（审查 prompt §2.7 nil 安全）：构造函数不返回 nil 接口，而是返回
// 非 nil 指针 + nil tracker，使 GetSessionStats/GetTotalStats 的 nil 检查生效，
// 调用方无需在每次调用前判空（符合 /usage /stats 死代码激活后的调用路径）。
// 返回 UsageTrackerInterface 接口类型，便于 command.Registry 直接赋值。
func NewUsageTrackerAdapter(t *ai.UsageTracker) UsageTrackerInterface {
	return &usageTrackerAdapter{tracker: t}
}

// GetSessionStats 实现 UsageTrackerInterface，返回 session 级用量统计。
// 注：ai.UsageTracker 当前是全局累计（5-E 留 P1），session 级隔离推到 P1。
// 当前实现返回全局累计数据，sessionName 参数预留待 P1 启用。
// nil 安全：a 或 a.tracker 为 nil 时返回零值 SessionUsageStats。
// 字段映射决策（改进方案 §3.3）：
// - CacheWriteTokens ← cacheCreation（a comparable CLI cache write = Anthropic cache creation）
// - CacheReadTokens ← cacheRead
func (a *usageTrackerAdapter) GetSessionStats(sessionName string) SessionUsageStats {
	if a == nil || a.tracker == nil {
		return SessionUsageStats{}
	}
	in, out, cacheCreate, cacheRead := a.tracker.GetSnapshot()
	return SessionUsageStats{
		InputTokens:      int64(in),
		OutputTokens:     int64(out),
		CacheReadTokens:  int64(cacheRead),
		CacheWriteTokens: int64(cacheCreate),
	}
}

// GetTotalStats 实现 UsageTrackerInterface，返回全局聚合统计。
// nil 安全：a 或 a.tracker 为 nil 时返回零值 TotalUsageStats。
// 字段映射决策（改进方案 §3.3）：
// - Calls ← GetTotalAPICalls()（走 getter 持 RLock，并发安全）
func (a *usageTrackerAdapter) GetTotalStats() TotalUsageStats {
	if a == nil || a.tracker == nil {
		return TotalUsageStats{}
	}
	return TotalUsageStats{
		Calls: int64(a.tracker.GetTotalAPICalls()),
	}
}
