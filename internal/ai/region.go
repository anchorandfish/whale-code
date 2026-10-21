// Package ai — S3（M04 不足点 6）：router 层多 region 路由 RegionRouter
//
// 设计要点（吸纳审查优化 4/7）：
//   - 三态熔断（参考 Cloudflare LB）：
//     Healthy（健康）：正常放行
//     HalfOpen（半开）：放行 1 个请求探测，成功转 Healthy，失败转 Open
//     Open（熔断）：拒绝请求，直到 CooldownTTL 过后转 HalfOpen
//   - 被动探测触发（优化 7）：
//     所有 region 均 Open 时，选择最早进入 Open 的 region 放行 1 个请求
//   - ErrAllRegionsFailed 等同 529 错误（优化 4）：
//     所有 region 均 Open 且未过冷却时返回，由上层走 FallbackChain.Record529 切模型
//     不进入 Phase 1 重试（避免重试已熔断的 region）
//
// 业内对标：
//   - AWS region routing（多 region endpoint + 健康检查）
//     https://docs.aws.amazon.com/general/latest/gr/rande.html
//   - Cloudflare load balancing（region pool + health check + failover）
//     https://developers.cloudflare.com/load-balancing/
//   - LiteLLM router（model alias → region list + cooldown）
//     https://github.com/BerriAI/litellm
package ai

import (
	"errors"
	"sort"
	"sync"
	"time"
)

// ErrAllRegionsFailed 所有 region 熔断错误。
// 等同 529 错误，由上层走 FallbackChain.Record529() 切模型（吸纳审查优化 4）。
// 不进入 Phase 1 重试（避免重试已熔断的 region）。
var ErrAllRegionsFailed = errors.New("all regions are open (circuit broken)")

// RegionState region 熔断三态（参考 Cloudflare LB）
type RegionState int

const (
	// RegionHealthy 健康：正常放行
	RegionHealthy RegionState = iota
	// RegionHalfOpen 半开（探测中）：放行 1 个请求探测，成功转 Healthy，失败转 Open
	RegionHalfOpen
	// RegionOpen 熔断中：拒绝请求，直到 CooldownTTL 过后转 HalfOpen
	RegionOpen
)

// String 返回状态名字符串，用于日志和 metrics 标签
func (s RegionState) String() string {
	switch s {
	case RegionHealthy:
		return "healthy"
	case RegionHalfOpen:
		return "half_open"
	case RegionOpen:
		return "open"
	}
	return "unknown"
}

// RegionEntry region 配置项
type RegionEntry struct {
	// Name region 名，如 "us-east-1" / "eu-west-1"
	Name string
	// BaseURL region 对应的 baseURL，如 "https://api.anthropic.com"
	BaseURL string
	// Weight 优先级权重（10/5/1），数值越大优先级越高
	Weight int
}

// RegionConfig 多 region 路由配置
type RegionConfig struct {
	// Regions region 列表（按 Weight 降序使用）
	Regions []RegionEntry
	// HealthCheckTTL 健康检查间隔，默认 30s（对齐 RecoveryCoordinator.cooldown）
	HealthCheckTTL time.Duration
	// FailureThreshold 熔断阈值，默认 3（对齐 FallbackChain.degradationThreshold）
	FailureThreshold int
	// CooldownTTL 熔断后冷却，默认 60s（2 倍 HealthCheckTTL，防抖动）
	CooldownTTL time.Duration
}

// DefaultRegionConfig 提供默认配置
func DefaultRegionConfig() RegionConfig {
	return RegionConfig{
		Regions:          []RegionEntry{},
		HealthCheckTTL:   30 * time.Second,
		FailureThreshold: 3,
		CooldownTTL:      60 * time.Second,
	}
}

// RegionHealth region 健康状态（运行时维护）
type RegionHealth struct {
	// State 当前熔断状态
	State RegionState
	// FailureCount 连续失败次数（达到 FailureThreshold 后转 Open）
	FailureCount int
	// LastFailureTime 最后一次失败时间（用于被动探测：所有 region 均 Open 时选最早进入 Open 的）
	LastFailureTime time.Time
	// LastCheckTime 最后一次健康检查时间（用于冷却判断）
	LastCheckTime time.Time
}

// RegionRouter 多 region 路由接口。
// 实现方应在 StreamWithRetry 调用 client.Stream 之前调用 SelectRegion 选择 baseURL，
// 失败时调用 MarkRegionFailed，成功时调用 MarkRegionHealthy。
type RegionRouter interface {
	// SelectRegion 根据模型选择 baseURL，excluded 为已失败的 region 列表。
	// 吸纳审查优化 7：所有 region 均 Open 时，选择最早进入 Open 的 region 放行 1 个请求（半开探测）
	// 返回 ErrAllRegionsFailed 表示所有 region 熔断且未过冷却。
	SelectRegion(model string, excluded []string) (baseURL, region string, err error)
	// MarkRegionFailed 标记 region 失败（用于熔断）
	MarkRegionFailed(region string)
	// MarkRegionHealthy 标记 region 健康（半开探测成功后转 Healthy）
	MarkRegionHealthy(region string)
	// HealthCheckTTL 返回健康检查间隔
	HealthCheckTTL() time.Duration
	// GetState 返回指定 region 当前状态（用于 metrics / 日志）
	GetState(region string) RegionState
}

// DefaultRegionRouter 默认实现（线程安全，sync.RWMutex 保护 health map）
type DefaultRegionRouter struct {
	mu      sync.RWMutex
	regions []RegionEntry
	health  map[string]*RegionHealth
	cfg     RegionConfig
}

// NewDefaultRegionRouter 创建默认路由器。
// regions 按 Weight 降序排序后存储，SelectRegion 优先返回 Weight 最高的可用 region。
func NewDefaultRegionRouter(cfg RegionConfig) *DefaultRegionRouter {
	if cfg.HealthCheckTTL <= 0 {
		cfg.HealthCheckTTL = 30 * time.Second
	}
	if cfg.FailureThreshold <= 0 {
		cfg.FailureThreshold = 3
	}
	if cfg.CooldownTTL <= 0 {
		cfg.CooldownTTL = 60 * time.Second
	}

	// 复制 regions 切片，避免外部修改
	regions := make([]RegionEntry, len(cfg.Regions))
	copy(regions, cfg.Regions)
	// 按 Weight 降序排序（高权重优先）
	sort.SliceStable(regions, func(i, j int) bool {
		return regions[i].Weight > regions[j].Weight
	})

	health := make(map[string]*RegionHealth, len(regions))
	for _, r := range regions {
		health[r.Name] = &RegionHealth{State: RegionHealthy}
	}

	return &DefaultRegionRouter{
		regions: regions,
		health:  health,
		cfg:     cfg,
	}
}

// SelectRegion 根据模型选择 baseURL，excluded 为已失败的 region 列表。
// 吸纳审查优化 7：所有 region 均 Open 时，选择最早进入 Open 的 region 放行 1 个请求（半开探测）
//
// 选择顺序：
//  1. 遍历 regions（按 Weight 降序），跳过 excluded 中的 region：
//     - Healthy → 直接返回
//     - HalfOpen → 放行（探测中，仅放行 1 个请求）
//     - Open 且 LastFailureTime + CooldownTTL < now → 转 HalfOpen，放行（被动探测）
//     - Open 且未过冷却 → 跳过
//  2. 所有 region 均 Open 且未过冷却 → 选择最早进入 Open 的 region 放行 1 个请求（被动探测）
//  3. 仍无可用 region → 返回 ErrAllRegionsFailed
func (r *DefaultRegionRouter) SelectRegion(model string, excluded []string) (baseURL, region string, err error) {
	r.mu.Lock()
	defer r.mu.Unlock()

	excludedSet := make(map[string]bool, len(excluded))
	for _, name := range excluded {
		excludedSet[name] = true
	}

	now := time.Now()
	var earliestOpenRegion *RegionEntry
	var earliestOpenTime time.Time

	// 第一遍：找 Healthy / HalfOpen / 已过冷却的 Open（转 HalfOpen）
	for i := range r.regions {
		entry := &r.regions[i]
		if excludedSet[entry.Name] {
			continue
		}
		h, ok := r.health[entry.Name]
		if !ok {
			// 未在 health map 中的 region 视为 Healthy
			h = &RegionHealth{State: RegionHealthy}
			r.health[entry.Name] = h
		}

		switch h.State {
		case RegionHealthy:
			return entry.BaseURL, entry.Name, nil
		case RegionHalfOpen:
			// 半开状态：放行（探测中）
			return entry.BaseURL, entry.Name, nil
		case RegionOpen:
			// 已过冷却：转 HalfOpen 放行（被动探测）
			if !h.LastFailureTime.IsZero() && now.Sub(h.LastFailureTime) >= r.cfg.CooldownTTL {
				h.State = RegionHalfOpen
				h.LastCheckTime = now
				return entry.BaseURL, entry.Name, nil
			}
			// 未过冷却：记录最早进入 Open 的 region（用于第二遍被动探测）
			if earliestOpenRegion == nil ||
				(h.LastFailureTime.Before(earliestOpenTime) && !h.LastFailureTime.IsZero()) {
				earliestOpenRegion = entry
				if !h.LastFailureTime.IsZero() {
					earliestOpenTime = h.LastFailureTime
				} else {
					earliestOpenTime = now
				}
			}
		}
	}

	// 第二遍：所有 region 均 Open 且未过冷却 → 选择最早进入 Open 的 region 放行 1 个请求（被动探测）
	// 吸纳审查优化 7：被动探测触发条件
	if earliestOpenRegion != nil {
		h := r.health[earliestOpenRegion.Name]
		if h != nil {
			h.State = RegionHalfOpen
			h.LastCheckTime = now
			return earliestOpenRegion.BaseURL, earliestOpenRegion.Name, nil
		}
	}

	return "", "", ErrAllRegionsFailed
}

// MarkRegionFailed 标记 region 失败（用于熔断）。
// 累计 FailureCount，达到 FailureThreshold 后转 Open。
// 若原 HalfOpen（探测中）失败，直接转 Open 并刷新 LastFailureTime。
func (r *DefaultRegionRouter) MarkRegionFailed(region string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	h, ok := r.health[region]
	if !ok {
		// 未知 region 忽略
		return
	}
	h.FailureCount++
	h.LastFailureTime = time.Now()
	if h.State == RegionHalfOpen {
		// 半开探测失败：直接转 Open
		h.State = RegionOpen
		return
	}
	if h.FailureCount >= r.cfg.FailureThreshold {
		h.State = RegionOpen
	}
}

// MarkRegionHealthy 标记 region 健康（半开探测成功后转 Healthy）。
// 重置 FailureCount 和 LastFailureTime。
func (r *DefaultRegionRouter) MarkRegionHealthy(region string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	h, ok := r.health[region]
	if !ok {
		return
	}
	h.State = RegionHealthy
	h.FailureCount = 0
	h.LastFailureTime = time.Time{}
	h.LastCheckTime = time.Now()
}

// HealthCheckTTL 返回健康检查间隔
func (r *DefaultRegionRouter) HealthCheckTTL() time.Duration {
	return r.cfg.HealthCheckTTL
}

// GetState 返回指定 region 当前状态（用于 metrics / 日志）。
// 未知 region 返回 RegionHealthy（保守策略，不阻塞流量）。
func (r *DefaultRegionRouter) GetState(region string) RegionState {
	r.mu.RLock()
	defer r.mu.RUnlock()
	h, ok := r.health[region]
	if !ok {
		return RegionHealthy
	}
	return h.State
}

// RegionEntries 返回路由器配置的 region 列表（按 Weight 降序）。
// 用于上层初始化时获取所有 region 名以构建 excluded 列表。
func (r *DefaultRegionRouter) RegionEntries() []RegionEntry {
	r.mu.RLock()
	defer r.mu.RUnlock()
	result := make([]RegionEntry, len(r.regions))
	copy(result, r.regions)
	return result
}

// noopRegionRouter 是一个空实现的 RegionRouter，用于 OnRegionRouter=nil 时的默认行为。
// 所有方法返回零值，SelectRegion 返回空 baseURL 和 nil 错误（表示不切换 region）。
// 该类型不导出，仅供 retry.go 内部使用（C3 兼容性：OnRegionRouter=nil 时跳过 region 路由）。
type noopRegionRouter struct{}

// SelectRegion 返回空 baseURL 和 nil 错误（表示不切换 region）
func (noopRegionRouter) SelectRegion(model string, excluded []string) (baseURL, region string, err error) {
	return "", "", nil
}

// MarkRegionFailed 空操作
func (noopRegionRouter) MarkRegionFailed(region string) {}

// MarkRegionHealthy 空操作
func (noopRegionRouter) MarkRegionHealthy(region string) {}

// HealthCheckTTL 返回 0（不进行健康检查）
func (noopRegionRouter) HealthCheckTTL() time.Duration { return 0 }

// GetState 返回 RegionHealthy（保守策略）
func (noopRegionRouter) GetState(region string) RegionState { return RegionHealthy }
