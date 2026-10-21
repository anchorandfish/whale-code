// Package ai — T1-C M04 多 region 路由单元测试
//
// 测试用例（按 03-实施prompt.md §6.2.3）：
//   - TC-RG-001: 所有 region Healthy → 返回 Weight 最高的
//   - TC-RG-002: 主 region 失败 FailureThreshold 次 → 转 Open；切到次 region
//   - TC-RG-003: Open 过 CooldownTTL → 转 HalfOpen 放行 1 个请求
//   - TC-RG-004: HalfOpen 探测成功/失败 → 转 Healthy / 转 Open
//   - TC-RG-005: 所有 region 均 Open → 返回 ErrAllRegionsFailed
//   - TC-RG-006: 并发 SelectRegion/MarkFailed → go test -race 无 race
package ai

import (
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// 测试用 region 配置（us > eu > ap 优先级）
func newTestRegionConfig() RegionConfig {
	return RegionConfig{
		Regions: []RegionEntry{
			{Name: "us-east-1", BaseURL: "https://api-us.example.com", Weight: 10},
			{Name: "eu-west-1", BaseURL: "https://api-eu.example.com", Weight: 5},
			{Name: "ap-southeast-1", BaseURL: "https://api-ap.example.com", Weight: 1},
		},
		HealthCheckTTL:   30 * time.Second,
		FailureThreshold: 3,
		CooldownTTL:      60 * time.Second,
	}
}

// TestTC_RG_001_AllHealthySelectHighestWeight 验证所有 region Healthy 时返回 Weight 最高的
func TestTC_RG_001_AllHealthySelectHighestWeight(t *testing.T) {
	router := NewDefaultRegionRouter(newTestRegionConfig())

	baseURL, region, err := router.SelectRegion("claude-3", nil)
	if err != nil {
		t.Fatalf("SelectRegion 返回错误: %v", err)
	}
	if region != "us-east-1" {
		t.Errorf("region = %q, 期望 us-east-1 (Weight=10)", region)
	}
	if baseURL != "https://api-us.example.com" {
		t.Errorf("baseURL = %q, 期望 https://api-us.example.com", baseURL)
	}

	// 状态应为 Healthy
	if state := router.GetState("us-east-1"); state != RegionHealthy {
		t.Errorf("us-east-1 状态 = %s, 期望 healthy", state)
	}
}

// TestTC_RG_002_PrimaryFailureTriggersOpen 验证主 region 失败 FailureThreshold 次后转 Open，下次切到次 region
func TestTC_RG_002_PrimaryFailureTriggersOpen(t *testing.T) {
	router := NewDefaultRegionRouter(newTestRegionConfig())

	// 失败 3 次（达到 FailureThreshold）
	for i := 0; i < 3; i++ {
		router.MarkRegionFailed("us-east-1")
	}

	// us-east-1 应转 Open
	if state := router.GetState("us-east-1"); state != RegionOpen {
		t.Errorf("us-east-1 失败 3 次后状态 = %s, 期望 open", state)
	}

	// 下次 SelectRegion 应切到次 region（eu-west-1，Weight=5）
	baseURL, region, err := router.SelectRegion("claude-3", nil)
	if err != nil {
		t.Fatalf("SelectRegion 返回错误: %v", err)
	}
	if region != "eu-west-1" {
		t.Errorf("region = %q, 期望 eu-west-1（us 已 Open）", region)
	}
	if baseURL != "https://api-eu.example.com" {
		t.Errorf("baseURL = %q, 期望 https://api-eu.example.com", baseURL)
	}
}

// TestTC_RG_003_OpenAfterCooldownBecomesHalfOpen 验证 Open 过 CooldownTTL 后转 HalfOpen 放行
func TestTC_RG_003_OpenAfterCooldownBecomesHalfOpen(t *testing.T) {
	cfg := newTestRegionConfig()
	cfg.CooldownTTL = 50 * time.Millisecond // 短 TTL 便于测试
	router := NewDefaultRegionRouter(cfg)

	// 让 us-east-1 进入 Open
	for i := 0; i < 3; i++ {
		router.MarkRegionFailed("us-east-1")
	}
	if state := router.GetState("us-east-1"); state != RegionOpen {
		t.Fatalf("前置失败：us-east-1 状态 = %s, 期望 open", state)
	}

	// 等待 CooldownTTL 过期
	time.Sleep(70 * time.Millisecond)

	// 再次 SelectRegion，us-east-1 应转 HalfOpen 并放行
	_, region, err := router.SelectRegion("claude-3", nil)
	if err != nil {
		t.Fatalf("SelectRegion 返回错误: %v", err)
	}
	if region != "us-east-1" {
		t.Errorf("Cooldown 后应选 us-east-1（HalfOpen 放行），实际 region = %q", region)
	}
	if state := router.GetState("us-east-1"); state != RegionHalfOpen {
		t.Errorf("us-east-1 状态 = %s, 期望 half_open", state)
	}
}

// TestTC_RG_004_HalfOpenProbeOutcome 验证 HalfOpen 探测成功转 Healthy，失败转 Open
func TestTC_RG_004_HalfOpenProbeOutcome(t *testing.T) {
	t.Run("ProbeSuccess_TransToHealthy", func(t *testing.T) {
		cfg := newTestRegionConfig()
		cfg.CooldownTTL = 50 * time.Millisecond
		router := NewDefaultRegionRouter(cfg)

		// 让 us-east-1 进入 Open → 等冷却 → 转 HalfOpen
		for i := 0; i < 3; i++ {
			router.MarkRegionFailed("us-east-1")
		}
		time.Sleep(70 * time.Millisecond)
		router.SelectRegion("claude-3", nil) // 触发 HalfOpen
		if state := router.GetState("us-east-1"); state != RegionHalfOpen {
			t.Fatalf("前置失败：us-east-1 状态 = %s, 期望 half_open", state)
		}

		// 探测成功
		router.MarkRegionHealthy("us-east-1")
		if state := router.GetState("us-east-1"); state != RegionHealthy {
			t.Errorf("探测成功后状态 = %s, 期望 healthy", state)
		}
	})

	t.Run("ProbeFailed_TransToOpen", func(t *testing.T) {
		cfg := newTestRegionConfig()
		cfg.CooldownTTL = 50 * time.Millisecond
		router := NewDefaultRegionRouter(cfg)

		// 让 us-east-1 进入 Open → 等冷却 → 转 HalfOpen
		for i := 0; i < 3; i++ {
			router.MarkRegionFailed("us-east-1")
		}
		time.Sleep(70 * time.Millisecond)
		router.SelectRegion("claude-3", nil) // 触发 HalfOpen
		if state := router.GetState("us-east-1"); state != RegionHalfOpen {
			t.Fatalf("前置失败：us-east-1 状态 = %s, 期望 half_open", state)
		}

		// 探测失败
		router.MarkRegionFailed("us-east-1")
		if state := router.GetState("us-east-1"); state != RegionOpen {
			t.Errorf("探测失败后状态 = %s, 期望 open", state)
		}
	})
}

// TestTC_RG_005_AllOpenReturnsErrAllRegionsFailed 验证所有 region 均被 excluded 时返回 ErrAllRegionsFailed
//
// 注：region.go 设计（吸纳审查优化 7）是「所有 region 均 Open 但未 excluded 时，
// 选择最早进入 Open 的 region 放行 1 个请求（被动探测）」，
// 不会立即返回 ErrAllRegionsFailed。
// ErrAllRegionsFailed 仅在所有 region 都被 excluded（在 retry.go 中累计失败后加入排除列表）时触发。
func TestTC_RG_005_AllOpenReturnsErrAllRegionsFailed(t *testing.T) {
	cfg := newTestRegionConfig()
	cfg.CooldownTTL = 1 * time.Hour // 长 TTL 避免被动的 HalfOpen 探测
	router := NewDefaultRegionRouter(cfg)

	// 让所有 region 进入 Open
	for _, r := range []string{"us-east-1", "eu-west-1", "ap-southeast-1"} {
		for i := 0; i < 3; i++ {
			router.MarkRegionFailed(r)
		}
	}

	// 验证三个 region 都是 Open
	for _, r := range []string{"us-east-1", "eu-west-1", "ap-southeast-1"} {
		if state := router.GetState(r); state != RegionOpen {
			t.Errorf("%s 状态 = %s, 期望 open", r, state)
		}
	}

	// 场景 A：所有 region 都 Open 但未 excluded → 触发被动探测（返回最早 Open 的 region）
	_, regionA, errA := router.SelectRegion("claude-3", nil)
	if errA != nil {
		t.Errorf("场景 A：SelectRegion 错误 = %v, 期望 nil（被动探测应放行）", errA)
	}
	t.Logf("场景 A：所有 Open 未 excluded → 被动探测 region=%q", regionA)

	// 场景 B：所有 region 都被 excluded → 返回 ErrAllRegionsFailed
	_, _, errB := router.SelectRegion("claude-3", []string{"us-east-1", "eu-west-1", "ap-southeast-1"})
	if !errors.Is(errB, ErrAllRegionsFailed) {
		t.Errorf("场景 B：SelectRegion 错误 = %v, 期望 ErrAllRegionsFailed", errB)
	}
}

// TestTC_RG_006_ConcurrentSelectAndMarkFailedRace 验证并发 SelectRegion/MarkFailed 时无 race
func TestTC_RG_006_ConcurrentSelectAndMarkFailedRace(t *testing.T) {
	router := NewDefaultRegionRouter(newTestRegionConfig())

	// 并发 10 个 goroutine 同时调 SelectRegion + MarkRegionFailed + MarkRegionHealthy
	var wg sync.WaitGroup
	var errCount atomic.Int32
	for i := 0; i < 10; i++ {
		wg.Add(1)
		go func(seed int) {
			defer wg.Done()
			for j := 0; j < 100; j++ {
				_, region, err := router.SelectRegion("claude-3", nil)
				if err != nil {
					errCount.Add(1)
					continue
				}
				// 随机标记成功或失败
				if (seed+j)%2 == 0 {
					router.MarkRegionFailed(region)
				} else {
					router.MarkRegionHealthy(region)
				}
			}
		}(i)
	}
	wg.Wait()

	// 主要验证：go test -race 不报 race（其他错误是业务逻辑问题，此处只关心并发安全）
	t.Logf("并发测试完成，错误数=%d（ErrAllRegionsFailed 是预期之一）", errCount.Load())
}

// TestRegionState_String 验证 RegionState.String 方法
func TestRegionState_String(t *testing.T) {
	cases := []struct {
		s    RegionState
		want string
	}{
		{RegionHealthy, "healthy"},
		{RegionHalfOpen, "half_open"},
		{RegionOpen, "open"},
		{RegionState(99), "unknown"},
	}
	for _, c := range cases {
		got := c.s.String()
		if got != c.want {
			t.Errorf("RegionState(%d).String() = %q, 期望 %q", c.s, got, c.want)
		}
	}
}

// TestDefaultRegionConfig 验证默认配置
func TestDefaultRegionConfig(t *testing.T) {
	cfg := DefaultRegionConfig()
	if cfg.HealthCheckTTL != 30*time.Second {
		t.Errorf("HealthCheckTTL = %v, 期望 30s", cfg.HealthCheckTTL)
	}
	if cfg.FailureThreshold != 3 {
		t.Errorf("FailureThreshold = %d, 期望 3", cfg.FailureThreshold)
	}
	if cfg.CooldownTTL != 60*time.Second {
		t.Errorf("CooldownTTL = %v, 期望 60s", cfg.CooldownTTL)
	}
	if cfg.Regions == nil {
		t.Error("Regions 应为非 nil（空切片）")
	}
}

// TestNewDefaultRegionRouter_Defaults 验证 NewDefaultRegionRouter 默认值填充
func TestNewDefaultRegionRouter_Defaults(t *testing.T) {
	// 传零值 RegionConfig
	router := NewDefaultRegionRouter(RegionConfig{})
	if router.cfg.HealthCheckTTL != 30*time.Second {
		t.Errorf("HealthCheckTTL = %v, 期望 30s (默认)", router.cfg.HealthCheckTTL)
	}
	if router.cfg.FailureThreshold != 3 {
		t.Errorf("FailureThreshold = %d, 期望 3 (默认)", router.cfg.FailureThreshold)
	}
	if router.cfg.CooldownTTL != 60*time.Second {
		t.Errorf("CooldownTTL = %v, 期望 60s (默认)", router.cfg.CooldownTTL)
	}
}

// TestNewDefaultRegionRouter_SortByWeight 验证 regions 按 Weight 降序排序
func TestNewDefaultRegionRouter_SortByWeight(t *testing.T) {
	// 乱序输入
	cfg := RegionConfig{
		Regions: []RegionEntry{
			{Name: "low", BaseURL: "https://low.example.com", Weight: 1},
			{Name: "high", BaseURL: "https://high.example.com", Weight: 100},
			{Name: "mid", BaseURL: "https://mid.example.com", Weight: 50},
		},
	}
	router := NewDefaultRegionRouter(cfg)
	entries := router.RegionEntries()
	if len(entries) != 3 {
		t.Fatalf("entries len = %d, 期望 3", len(entries))
	}
	// 验证降序：high(100) → mid(50) → low(1)
	if entries[0].Name != "high" {
		t.Errorf("entries[0].Name = %q, 期望 high", entries[0].Name)
	}
	if entries[1].Name != "mid" {
		t.Errorf("entries[1].Name = %q, 期望 mid", entries[1].Name)
	}
	if entries[2].Name != "low" {
		t.Errorf("entries[2].Name = %q, 期望 low", entries[2].Name)
	}
}

// TestRegionRouter_ExcludedRegions 验证 excluded 参数排除指定 region
func TestRegionRouter_ExcludedRegions(t *testing.T) {
	router := NewDefaultRegionRouter(newTestRegionConfig())

	// 排除 us-east-1 和 eu-west-1，应返回 ap-southeast-1
	baseURL, region, err := router.SelectRegion("claude-3", []string{"us-east-1", "eu-west-1"})
	if err != nil {
		t.Fatalf("SelectRegion 返回错误: %v", err)
	}
	if region != "ap-southeast-1" {
		t.Errorf("region = %q, 期望 ap-southeast-1（excluded us+eu）", region)
	}
	if baseURL != "https://api-ap.example.com" {
		t.Errorf("baseURL = %q, 期望 https://api-ap.example.com", baseURL)
	}
}

// TestRegionRouter_MarkUnknownRegionIgnored 验证 MarkRegionFailed/MarkRegionHealthy 对未知 region 静默忽略
func TestRegionRouter_MarkUnknownRegionIgnored(t *testing.T) {
	router := NewDefaultRegionRouter(newTestRegionConfig())

	// 未知 region 不应 panic
	router.MarkRegionFailed("unknown-region")
	router.MarkRegionHealthy("unknown-region")

	// 已知 region 状态保持 Healthy
	if state := router.GetState("us-east-1"); state != RegionHealthy {
		t.Errorf("us-east-1 状态 = %s, 期望 healthy（Mark unknown 不应影响）", state)
	}
}

// TestRegionRouter_GetStateUnknownRegion 验证未知 region 返回 RegionHealthy（保守策略）
func TestRegionRouter_GetStateUnknownRegion(t *testing.T) {
	router := NewDefaultRegionRouter(newTestRegionConfig())

	if state := router.GetState("unknown-region"); state != RegionHealthy {
		t.Errorf("未知 region 状态 = %s, 期望 healthy (保守策略)", state)
	}
}

// TestRegionRouter_HealthCheckTTL 验证 HealthCheckTTL 方法返回配置值
func TestRegionRouter_HealthCheckTTL(t *testing.T) {
	cfg := newTestRegionConfig()
	cfg.HealthCheckTTL = 15 * time.Second
	router := NewDefaultRegionRouter(cfg)

	if ttl := router.HealthCheckTTL(); ttl != 15*time.Second {
		t.Errorf("HealthCheckTTL = %v, 期望 15s", ttl)
	}
}

// TestNoopRegionRouter 验证 noopRegionRouter 行为（OnRegionRouter=nil 时的默认行为）
func TestNoopRegionRouter(t *testing.T) {
	var r RegionRouter = noopRegionRouter{}

	// SelectRegion 返回空 baseURL 和 nil 错误
	baseURL, region, err := r.SelectRegion("claude-3", nil)
	if err != nil {
		t.Errorf("noopRegionRouter.SelectRegion err = %v, 期望 nil", err)
	}
	if baseURL != "" {
		t.Errorf("noopRegionRouter.SelectRegion baseURL = %q, 期望空", baseURL)
	}
	if region != "" {
		t.Errorf("noopRegionRouter.SelectRegion region = %q, 期望空", region)
	}

	// 其他方法不应 panic
	r.MarkRegionFailed("any")
	r.MarkRegionHealthy("any")
	if ttl := r.HealthCheckTTL(); ttl != 0 {
		t.Errorf("noopRegionRouter.HealthCheckTTL = %v, 期望 0", ttl)
	}
	if state := r.GetState("any"); state != RegionHealthy {
		t.Errorf("noopRegionRouter.GetState = %s, 期望 healthy", state)
	}
}

// TestRegionRouter_RegionEntries 验证 RegionEntries 返回副本（不暴露内部切片）
func TestRegionRouter_RegionEntries(t *testing.T) {
	router := NewDefaultRegionRouter(newTestRegionConfig())
	entries := router.RegionEntries()

	// 修改返回的切片不应影响 router 内部状态
	entries[0].Name = "modified"
	entries2 := router.RegionEntries()
	if entries2[0].Name == "modified" {
		t.Error("RegionEntries 返回了内部切片的引用（应返回副本）")
	}
}
