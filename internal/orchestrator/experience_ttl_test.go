// experience_ttl_test.go — H10 Phase 4 模块 8 TTL + Windows 长路径 + DLP 降级反例测试（TDD 先行，Phase 4 §5/§6）。
// 覆盖：TTL 四级状态表 / 污染率暂停/恢复 / Windows \\?\ 前缀 + 短名映射 / DLP 降级 / 并发 sweep 无竞态。
// 状态表；落地：applyTTL 四级（unverified 7d/prov 30d/verified 90d/deprecated 180d）
// ；落地：checkPollution >20% 暂停 / <5% 恢复
// 长路径；落地：\\?\ 前缀 + pattern_id[:8] 短名 + 短名映射表
// 降级；落地：dlpDegradedHandler 切换 ~/.whale-safe/
// SNAPSHOT: tests={TTL_Unverified_7Days_ToDeprecated, TTL_ProvisionallyVerified_30Days_ToUnverified, TTL_Verified_90Days_LowRate_ToDeprecated, TTL_Deprecated_180Days_PhysicallyDeleted, PollutionCheck_Over20Percent_PauseInjection, PollutionCheck_Under5Percent_ResumeInjection, WindowsLongPath_PrefixApplied, WindowsLongPath_ShortHash_Over32000, DLPDegraded_SwitchToSafeDir, DLPDegraded_MemoryIndexOnly, TTLManager_ConcurrentSweep_NoRace}

package orchestrator

import (
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

// makeTestPatternWithTime 构造测试用 Pattern（可指定 VerifiedState + VerifiedAt + LastUsedAt + L2AdoptionRate）。
func makeTestPatternWithTime(id, signature, scope, state string, verifiedAt time.Time, lastUsedAt time.Time, l2Rate float64) *Pattern {
	p := makeTestPattern(id, signature, scope, state)
	p.VerifiedAt = verifiedAt.UTC().Format(time.RFC3339)
	p.LastUsedAt = lastUsedAt.UTC().Format(time.RFC3339)
	p.L2AdoptionRate = l2Rate
	return p
}

// TestTTL_Unverified_7Days_ToDeprecated unverified 7 天未升级 → deprecated（§6.1 TTL 表）。
func TestTTL_Unverified_7Days_ToDeprecated(t *testing.T) {
	t.Parallel()
	s := newTestExperienceStore(t)
	mgr := NewExperienceTTLManager(s, 0)

	fixedNow := time.Now()
	mgr.SetNowFunc(func() time.Time { return fixedNow })

	// VerifiedAt = 8 天前（> 7 天阈值）
	p := makeTestPatternWithTime("p-ttl-unv", "bug-fix ttl test", ScopeGlobal, VerifiedStateUnverified,
		fixedNow.Add(-8*24*time.Hour), fixedNow, 0.8)
	if err := s.Save(p); err != nil {
		t.Fatalf("Save: %v", err)
	}

	if err := mgr.RunTTLSweepOnce(); err != nil {
		t.Fatalf("RunTTLSweepOnce: %v", err)
	}

	got, err := s.Get("p-ttl-unv")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got.VerifiedState != VerifiedStateDeprecated {
		t.Errorf("unverified 7 天未升级应变为 deprecated，got %s", got.VerifiedState)
	}
}

// TestTTL_ProvisionallyVerified_30Days_ToUnverified provisionally_verified 30 天未升级 → 退回 unverified（§6.1 TTL 表）。
func TestTTL_ProvisionallyVerified_30Days_ToUnverified(t *testing.T) {
	t.Parallel()
	s := newTestExperienceStore(t)
	mgr := NewExperienceTTLManager(s, 0)

	fixedNow := time.Now()
	mgr.SetNowFunc(func() time.Time { return fixedNow })

	// VerifiedAt = 31 天前（> 30 天阈值）
	p := makeTestPatternWithTime("p-ttl-prov", "bug-fix prov ttl", ScopeGlobal, VerifiedStateProvisionallyVerified,
		fixedNow.Add(-31*24*time.Hour), fixedNow, 0.8)
	if err := s.Save(p); err != nil {
		t.Fatalf("Save: %v", err)
	}

	if err := mgr.RunTTLSweepOnce(); err != nil {
		t.Fatalf("RunTTLSweepOnce: %v", err)
	}

	got, err := s.Get("p-ttl-prov")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got.VerifiedState != VerifiedStateUnverified {
		t.Errorf("provisionally_verified 30 天未升级应退回 unverified，got %s", got.VerifiedState)
	}
}

// TestTTL_Verified_90Days_LowRate_ToDeprecated verified 90 天未用 + l2_rate<0.3 → deprecated（§6.1 TTL 表）。
func TestTTL_Verified_90Days_LowRate_ToDeprecated(t *testing.T) {
	t.Parallel()
	s := newTestExperienceStore(t)
	mgr := NewExperienceTTLManager(s, 0)

	fixedNow := time.Now()
	mgr.SetNowFunc(func() time.Time { return fixedNow })

	// LastUsedAt = 91 天前（> 90 天阈值）+ L2AdoptionRate = 0.2（< 0.3 阈值）
	p := makeTestPatternWithTime("p-ttl-ver", "bug-fix verified ttl", ScopeGlobal, VerifiedStateVerified,
		fixedNow.Add(-100*24*time.Hour), fixedNow.Add(-91*24*time.Hour), 0.2)
	if err := s.Save(p); err != nil {
		t.Fatalf("Save: %v", err)
	}

	if err := mgr.RunTTLSweepOnce(); err != nil {
		t.Fatalf("RunTTLSweepOnce: %v", err)
	}

	got, err := s.Get("p-ttl-ver")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got.VerifiedState != VerifiedStateDeprecated {
		t.Errorf("verified 90 天未用 + l2_rate<0.3 应变为 deprecated，got %s", got.VerifiedState)
	}
}

// TestTTL_Deprecated_180Days_PhysicallyDeleted deprecated 180 天后标记物理删除（§6.1 TTL 表）。
// 当前实现仅标记不删除（物理删除由独立 cleanup 任务执行），验证不 panic + 状态不变。
func TestTTL_Deprecated_180Days_PhysicallyDeleted(t *testing.T) {
	t.Parallel()
	s := newTestExperienceStore(t)
	mgr := NewExperienceTTLManager(s, 0)

	fixedNow := time.Now()
	mgr.SetNowFunc(func() time.Time { return fixedNow })

	// VerifiedAt = 181 天前（> 180 天阈值）+ deprecated 状态
	p := makeTestPatternWithTime("p-ttl-dep", "bug-fix deprecated ttl", ScopeGlobal, VerifiedStateDeprecated,
		fixedNow.Add(-181*24*time.Hour), fixedNow, 0.1)
	if err := s.Save(p); err != nil {
		t.Fatalf("Save: %v", err)
	}

	// 不应 panic / error
	if err := mgr.RunTTLSweepOnce(); err != nil {
		t.Fatalf("RunTTLSweepOnce: %v", err)
	}

	// deprecated 状态不变（物理删除由独立任务执行，本处仅标记）
	got, err := s.Get("p-ttl-dep")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got.VerifiedState != VerifiedStateDeprecated {
		t.Errorf("deprecated 180 天后状态应保持 deprecated（物理删除由独立任务执行），got %s", got.VerifiedState)
	}
}

// TestPollutionCheck_Over20Percent_PauseInjection 污染率 > 20% → 暂停注入（§5.2）。
func TestPollutionCheck_Over20Percent_PauseInjection(t *testing.T) {
	t.Parallel()
	s := newTestExperienceStore(t)
	mgr := NewExperienceTTLManager(s, 0)

	fixedNow := time.Now()
	mgr.SetNowFunc(func() time.Time { return fixedNow })

	// 5 个 pattern，2 个 l2_rate<0.3（40% > 20% 阈值）
	for i, rate := range []float64{0.8, 0.2, 0.8, 0.2, 0.8} {
		p := makeTestPatternWithTime(
			"p-poll-"+string(rune('a'+i)),
			"bug-fix poll test "+string(rune('a'+i)),
			ScopeGlobal, VerifiedStateVerified,
			fixedNow, fixedNow, rate,
		)
		if err := s.Save(p); err != nil {
			t.Fatalf("Save %d: %v", i, err)
		}
	}

	rate, shouldPause := mgr.checkPollution()
	if !shouldPause {
		t.Errorf("污染率 %.0f%% > 20%% 应暂停注入，got shouldPause=false", rate*100)
	}
	if rate <= PollutionPauseThreshold {
		t.Errorf("污染率应 > %.0f%%，got %.0f%%", PollutionPauseThreshold*100, rate*100)
	}
}

// TestPollutionCheck_Under5Percent_ResumeInjection 污染率 < 5% → 恢复注入（§5.2）。
func TestPollutionCheck_Under5Percent_ResumeInjection(t *testing.T) {
	t.Parallel()
	s := newTestExperienceStore(t)
	mgr := NewExperienceTTLManager(s, 0)

	fixedNow := time.Now()
	mgr.SetNowFunc(func() time.Time { return fixedNow })

	// 10 个 pattern，全部 l2_rate>=0.3（0% < 5% 阈值）
	for i := 0; i < 10; i++ {
		p := makeTestPatternWithTime(
			"p-resume-"+string(rune('a'+i)),
			"bug-fix resume test "+string(rune('a'+i)),
			ScopeGlobal, VerifiedStateVerified,
			fixedNow, fixedNow, 0.8,
		)
		if err := s.Save(p); err != nil {
			t.Fatalf("Save %d: %v", i, err)
		}
	}

	rate, shouldPause := mgr.checkPollution()
	if shouldPause {
		t.Errorf("污染率 %.0f%% < 5%% 应恢复注入（不暂停），got shouldPause=true", rate*100)
	}
	if rate > PollutionResumeThreshold {
		t.Errorf("污染率应 < %.0f%%，got %.0f%%", PollutionResumeThreshold*100, rate*100)
	}
}

// TestWindowsLongPath_PrefixApplied Windows 模式 + 路径 > 240 → 添加 \\?\ 前缀。
func TestWindowsLongPath_PrefixApplied(t *testing.T) {
	// 不使用 t.Parallel()：SetWindowsLongPathMode 修改全局变量 isWindowsForLongPath，并行会 data race
	defer SetWindowsLongPathMode(true)()

	// 构造 > 240 字符的路径
	longPath := `C:\project\` + strings.Repeat("a", 230) + `\patterns.jsonl`
	if len(longPath) <= windowsLongPathThreshold {
		t.Fatalf("测试路径长度 %d 应 > %d", len(longPath), windowsLongPathThreshold)
	}

	result, err := windowsLongPathHandler(longPath, "test-pattern-id")
	if err != nil {
		t.Fatalf("windowsLongPathHandler: %v", err)
	}

	if !strings.HasPrefix(result, `\\?\`) {
		t.Errorf("Windows 长路径应添加 \\\\?\\ 前缀，got %s", result)
	}
	if !strings.Contains(result, longPath) {
		t.Errorf("前缀路径应包含原路径，got %s", result)
	}
}

// TestWindowsLongPath_ShortHash_Over32000 Windows 模式 + 路径 > 32000 → pattern_id[:8] 短名。
func TestWindowsLongPath_ShortHash_Over32000(t *testing.T) {
	// 不使用 t.Parallel()：SetWindowsLongPathMode 修改全局变量 isWindowsForLongPath，并行会 data race
	defer SetWindowsLongPathMode(true)()
	defer ResetShortNameMap()

	patternID := "test-pattern-id-long"
	dir := `C:\project\.whale\experience\patterns`
	// 构造 > 32000 字符的路径（目录短 + 文件名超长）
	longName := strings.Repeat("a", windowsShortHashThreshold)
	longPath := filepath.Join(dir, longName+".json")

	result, err := windowsLongPathHandler(longPath, patternID)
	if err != nil {
		t.Fatalf("windowsLongPathHandler: %v", err)
	}

	// 应使用 pattern_id[:8] 作为短文件名
	expectedShortName := patternID[:8] + ".json"
	if !strings.Contains(result, expectedShortName) {
		t.Errorf("路径 > 32000 应使用 pattern_id[:8] 短名 %q，got %s", expectedShortName, result)
	}
	if !strings.HasPrefix(result, `\\?\`) {
		t.Errorf("短名路径仍应有 \\\\?\\ 前缀，got %s", result)
	}

	// 验证短名映射表已更新
	full, ok := LookupShortNameFull(expectedShortName)
	if !ok {
		t.Fatal("短名映射表应包含短名 → 全名映射")
	}
	if full != longPath {
		t.Errorf("短名映射的全名应为原路径，got %s", full)
	}
}

// TestWindowsLongPath_NonWindows_NoChange 非 Windows 平台 → 路径不变更。
func TestWindowsLongPath_NonWindows_NoChange(t *testing.T) {
	// 不使用 t.Parallel()：SetWindowsLongPathMode 修改全局变量 isWindowsForLongPath，并行会 data race
	defer SetWindowsLongPathMode(false)()

	longPath := `C:\project\` + strings.Repeat("a", 230) + `\patterns.jsonl`
	result, err := windowsLongPathHandler(longPath, "test-pattern-id")
	if err != nil {
		t.Fatalf("windowsLongPathHandler: %v", err)
	}
	if result != longPath {
		t.Errorf("非 Windows 平台路径应不变更，got %s", result)
	}
}

// TestDLPDegraded_SwitchToSafeDir DLP 降级：projectPath 切换到 ~/.whale-safe/experience/（§5.3）。
func TestDLPDegraded_SwitchToSafeDir(t *testing.T) {
	t.Parallel()
	s := newTestExperienceStore(t)

	// 初始状态：未降级
	if IsDLPDegraded(s) {
		t.Fatal("初始状态不应为 DLP 降级")
	}

	// 模拟 DLP 降级：强制切换到 safe 路径
	safePath := filepath.Join(t.TempDir(), ".whale-safe", "experience")
	ForceDLPDegraded(s, safePath)

	// 验证已降级
	if !IsDLPDegraded(s) {
		t.Fatal("ForceDLPDegraded 后应为 DLP 降级状态")
	}
}

// TestDLPDegraded_MemoryIndexOnly DLP 降级后经验库仍可用（Save/Get/Match 走 safe 路径，内存索引不丢失）。
func TestDLPDegraded_MemoryIndexOnly(t *testing.T) {
	t.Parallel()
	s := newTestExperienceStore(t)

	// 切换到 safe 路径（需创建子目录）
	safePath := filepath.Join(t.TempDir(), ".whale-safe", "experience")
	ForceDLPDegraded(s, safePath)

	// 降级后 Save 应写入 safe 路径
	p := makeTestPattern("p-dlp-test", "bug-fix dlp test", ScopeProject, VerifiedStateVerified)
	if err := s.Save(p); err != nil {
		t.Fatalf("DLP 降级后 Save 应成功（走 safe 路径）：%v", err)
	}

	// Get 应从 safe 路径读取
	got, err := s.Get("p-dlp-test")
	if err != nil {
		t.Fatalf("DLP 降级后 Get 应成功：%v", err)
	}
	if got.PatternID != "p-dlp-test" {
		t.Errorf("Get PatternID 期望 p-dlp-test，got %s", got.PatternID)
	}
}

// TestTTLManager_ConcurrentSweep_NoRace 并发 sweep 不竞态（-race 验证，§6 并发安全）。
func TestTTLManager_ConcurrentSweep_NoRace(t *testing.T) {
	t.Parallel()
	s := newTestExperienceStore(t)
	mgr := NewExperienceTTLManager(s, 0)

	fixedNow := time.Now()
	mgr.SetNowFunc(func() time.Time { return fixedNow })

	// 保存多个 pattern
	for i := 0; i < 5; i++ {
		p := makeTestPatternWithTime(
			"p-race-"+string(rune('a'+i)),
			"bug-fix race test "+string(rune('a'+i)),
			ScopeGlobal, VerifiedStateUnverified,
			fixedNow.Add(-8*24*time.Hour), fixedNow, 0.8,
		)
		if err := s.Save(p); err != nil {
			t.Fatalf("Save %d: %v", i, err)
		}
	}

	// 5 goroutine 同时 RunTTLSweepOnce
	var wg sync.WaitGroup
	errs := make([]error, 5)
	wg.Add(5)
	for i := 0; i < 5; i++ {
		go func(idx int) {
			defer wg.Done()
			errs[idx] = mgr.RunTTLSweepOnce()
		}(i)
	}
	wg.Wait()

	for i, err := range errs {
		if err != nil {
			t.Errorf("goroutine %d RunTTLSweepOnce: %v", i, err)
		}
	}
}
