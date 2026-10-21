// forgetting_test.go — H11/N27 Phase A A5 Ebbinghaus 衰减 + 后台扫描测试（含反例）。
// ；落地：TestCalculateRetention_Normalized（致命反例，retention 不超过 1.0）
// +补扫描；落地：TestStartForgettingScanner_Compensate（反例，>24h 触发补扫描）
// 仅 ephemeral；落地：TestApplyForgetting_NonEphemeral_NoOp（反例）+ ContradictedNotOverwritten（反例）
// SNAPSHOT: tests={CalculateRetention×4, ApplyForgetting×5(含3反例), StartForgettingScanner×3(含1反例), shouldCompensateScan×2}
package memory

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// resetCapabilityStoreForTest 测试前重置全局 capabilityStore，避免跨测试状态污染。
// 不使用 t.Parallel()：capabilityStore 是全局变量，并行测试会引发 data race。
func resetCapabilityStoreForTest(t *testing.T) {
	t.Helper()
	ResetCapabilityRegistryForTest()
}

// TestCalculateRetention_Normal lastAccessedAt=now, accessCount=0, retention ≈ 1.0。
// 刚访问 + 0 次访问，decay=exp(0)=1.0, accessFactor=1+log(1)=1.0, retention=1.0。
func TestCalculateRetention_Normal(t *testing.T) {
	t.Parallel()
	retention := CalculateRetention(time.Now(), 0)
	if retention < 0.99 || retention > 1.0 {
		t.Errorf("retention = %f, 期望 ≈ 1.0 (now + 0 access, 归一化后 1.0)", retention)
	}
}

// TestCalculateRetention_Normalized 致命反例：accessCount=100 + Δt=1天, retention 不超过 1.0（归一化防护）。
// 审查报告优化 1 修复：未归一化时 exp(-1/7)×(1+log(101)) ≈ 0.867×5.615 ≈ 4.87 > 1.0，
// 归一化后强制为 1.0，防止高频访问+短间隔时 retention 越界。
func TestCalculateRetention_Normalized(t *testing.T) {
	t.Parallel()
	// 1 天前访问，accessCount=100
	yesterday := time.Now().Add(-24 * time.Hour)
	retention := CalculateRetention(yesterday, 100)
	// 未归一化: ≈ 4.87; 归一化后: 1.0
	if retention > 1.0 {
		t.Errorf("retention = %f, 期望 <= 1.0 (归一化上限防护失效)", retention)
	}
	// 1 天前 + 100 次访问，retention 应接近 1.0（被归一化）
	if retention < 0.5 {
		t.Errorf("retention = %f, 期望 >= 0.5 (1天前+100 access 归一化后应接近 1.0)", retention)
	}
}

// TestCalculateRetention_Decay Δt=7天, accessCount=0, retention ≈ exp(-1) ≈ 0.368。
// 7 天前访问 + 0 次访问，decay=exp(-1)≈0.368, accessFactor=1.0, retention≈0.368。
func TestCalculateRetention_Decay(t *testing.T) {
	t.Parallel()
	sevenDaysAgo := time.Now().Add(-7 * 24 * time.Hour)
	retention := CalculateRetention(sevenDaysAgo, 0)
	// exp(-1) ≈ 0.3678794, 容差 ±0.02（time.Since 实际值略大于 7 天）
	expected := 0.3678794
	if retention < expected-0.02 || retention > expected+0.02 {
		t.Errorf("retention = %f, 期望 ≈ %f (exp(-1), 容差 ±0.02)", retention, expected)
	}
}

// TestCalculateRetention_HighAccessCount accessCount=100, 验证 1+log(101) ≈ 5.615 系数生效。
// 间接验证：17 天前 + 100 access，无 accessFactor 时 retention ≈ exp(-17/7) ≈ 0.088 < 0.1，
// 有 accessFactor(5.615) 时 retention ≈ 0.088×5.615 ≈ 0.49（在 [0.1, 0.5) 区间）。
func TestCalculateRetention_HighAccessCount(t *testing.T) {
	t.Parallel()
	// 直接验证：now + 100 access → retention = 1.0×5.615 → 归一化为 1.0
	retention := CalculateRetention(time.Now(), 100)
	if retention != 1.0 {
		t.Errorf("retention = %f, 期望 1.0 (now + 100 access, accessFactor≈5.615 归一化为 1.0)", retention)
	}
	// 间接验证 accessFactor 生效：17 天前 + 100 access
	// 无 accessFactor: exp(-17/7) ≈ 0.088 (< 0.1)
	// 有 accessFactor: 0.088 × 5.615 ≈ 0.49 (在 [0.1, 0.5))
	seventeenDaysAgo := time.Now().Add(-17 * 24 * time.Hour)
	retention2 := CalculateRetention(seventeenDaysAgo, 100)
	if retention2 < 0.1 || retention2 >= 0.5 {
		t.Errorf("retention = %f, 期望在 [0.1, 0.5) 区间 (验证 accessFactor 5.615 生效, 否则应 < 0.1)", retention2)
	}
}

// TestApplyForgetting_EphemeralDecay_Contradicted retention<0.1 转 contradicted + ValidUntil 设置。
// 100 天前访问 + 0 access → retention ≈ exp(-100/7) ≈ 6.4e-7 < 0.1 → 转 contradicted。
func TestApplyForgetting_EphemeralDecay_Contradicted(t *testing.T) {
	resetCapabilityStoreForTest(t)

	oldTime := time.Now().Add(-100 * 24 * time.Hour)
	entry := &MemoryEntry{
		Name:           "test-ephemeral-contradicted",
		TrustLevel:     TrustEphemeral,
		Verification:   VerificationUnverified,
		LastAccessedAt: oldTime,
		AccessCount:    0,
	}
	RegisterCapabilityForTest(entry.Name, entry)

	err := ApplyForgetting(entry)
	if err != nil {
		t.Fatalf("ApplyForgetting failed: %v", err)
	}
	if entry.Verification != VerificationContradicted {
		t.Errorf("Verification = %q, 期望 %q (retention<0.1 转 contradicted)", entry.Verification, VerificationContradicted)
	}
	if entry.ValidUntil == nil {
		t.Errorf("ValidUntil 应被设置 (contradicted 时 ValidUntil=now)")
	}
}

// TestApplyForgetting_EphemeralDecay_Unverified 0.1<=retention<0.5 转 unverified (内部通道)。
// 10 天前访问 + 0 access → retention ≈ exp(-10/7) ≈ 0.24 (在 [0.1, 0.5)) → 转 unverified。
// 注：verified→unverified 仅 ApplyForgetting 内部触发，公共 API TransitionVerification 拒绝此流转。
func TestApplyForgetting_EphemeralDecay_Unverified(t *testing.T) {
	resetCapabilityStoreForTest(t)

	oldTime := time.Now().Add(-10 * 24 * time.Hour)
	entry := &MemoryEntry{
		Name:           "test-ephemeral-unverified",
		TrustLevel:     TrustEphemeral,
		Verification:   VerificationVerified, // 从 verified 流转到 unverified (内部通道)
		LastAccessedAt: oldTime,
		AccessCount:    0,
	}
	RegisterCapabilityForTest(entry.Name, entry)

	err := ApplyForgetting(entry)
	if err != nil {
		t.Fatalf("ApplyForgetting failed: %v", err)
	}
	if entry.Verification != VerificationUnverified {
		t.Errorf("Verification = %q, 期望 %q (0.1<=retention<0.5 转 unverified 内部通道)", entry.Verification, VerificationUnverified)
	}
}

// TestApplyForgetting_EphemeralDecay_Normal retention>=0.5 不流转。
// 刚访问 + 0 access → retention ≈ 1.0 >= 0.5 → 不流转，保持原 verification。
func TestApplyForgetting_EphemeralDecay_Normal(t *testing.T) {
	resetCapabilityStoreForTest(t)

	entry := &MemoryEntry{
		Name:           "test-ephemeral-normal",
		TrustLevel:     TrustEphemeral,
		Verification:   VerificationUnverified,
		LastAccessedAt: time.Now(),
		AccessCount:    0,
	}
	RegisterCapabilityForTest(entry.Name, entry)

	err := ApplyForgetting(entry)
	if err != nil {
		t.Fatalf("ApplyForgetting failed: %v", err)
	}
	if entry.Verification != VerificationUnverified {
		t.Errorf("Verification = %q, 期望保持 %q (retention>=0.5 不流转)", entry.Verification, VerificationUnverified)
	}
}

// TestApplyForgetting_NonEphemeral_NoOp 反例：stable/capability/procedural 不衰减, 直接返回 nil。
// 即使 LastAccessedAt 极老（100 天前），非 ephemeral 类型不应用 Ebbinghaus 衰减。
func TestApplyForgetting_NonEphemeral_NoOp(t *testing.T) {
	resetCapabilityStoreForTest(t)

	nonEphemeralTypes := []string{TrustStable, TrustCapability, TrustProcedural}
	for _, trust := range nonEphemeralTypes {
		oldTime := time.Now().Add(-100 * 24 * time.Hour) // 极老时间
		entry := &MemoryEntry{
			Name:           "test-non-ephemeral-" + trust,
			TrustLevel:     trust,
			Verification:   VerificationVerified,
			LastAccessedAt: oldTime,
			AccessCount:    0,
		}
		RegisterCapabilityForTest(entry.Name, entry)

		err := ApplyForgetting(entry)
		if err != nil {
			t.Errorf("ApplyForgetting(%s) 返回 error: %v, 期望 nil (非 ephemeral 不衰减)", trust, err)
		}
		if entry.Verification != VerificationVerified {
			t.Errorf("ApplyForgetting(%s) Verification = %q, 期望保持 %q (非 ephemeral 不流转)", trust, entry.Verification, VerificationVerified)
		}
	}
}

// TestApplyForgetting_ContradictedNotOverwritten 反例：已 contradicted 的 ephemeral 不被降级为 unverified。
// retention 在 [0.1, 0.5) 区间（10 天前访问），但 entry 已是 contradicted 终态，
// 防护逻辑应保持 contradicted 不被降级为 unverified（防止终态被覆盖）。
func TestApplyForgetting_ContradictedNotOverwritten(t *testing.T) {
	resetCapabilityStoreForTest(t)

	// retention 在 [0.1, 0.5) 区间（10 天前访问, retention ≈ 0.24）
	// 正常情况下会转 unverified, 但已 contradicted 的应保持
	oldTime := time.Now().Add(-10 * 24 * time.Hour)
	entry := &MemoryEntry{
		Name:           "test-contradicted-keep",
		TrustLevel:     TrustEphemeral,
		Verification:   VerificationContradicted, // 已 contradicted 终态
		LastAccessedAt: oldTime,
		AccessCount:    0,
	}
	RegisterCapabilityForTest(entry.Name, entry)

	err := ApplyForgetting(entry)
	if err != nil {
		t.Fatalf("ApplyForgetting failed: %v", err)
	}
	if entry.Verification != VerificationContradicted {
		t.Errorf("Verification = %q, 期望保持 %q (contradicted 终态不被降级为 unverified)", entry.Verification, VerificationContradicted)
	}
}

// TestStartForgettingScanner_Compensate 反例：.last_scan_at 距今 > 24h, 启动时补扫描触发。
// 写入 25 小时前的 .last_scan_at，启动 scanner 后应触发补扫描并更新 .last_scan_at 为当前时间。
func TestStartForgettingScanner_Compensate(t *testing.T) {
	resetCapabilityStoreForTest(t)

	scopeDir := t.TempDir()

	// 写入 25 小时前的 .last_scan_at
	oldTime := time.Now().Add(-25 * time.Hour)
	oldTimestamp := oldTime.Format(time.RFC3339)
	if err := os.WriteFile(filepath.Join(scopeDir, lastScanAtFileName), []byte(oldTimestamp), 0o644); err != nil {
		t.Fatalf("write .last_scan_at: %v", err)
	}

	// 启动 scanner
	ctx, cancel := context.WithCancel(context.Background())
	go StartForgettingScanner(ctx, scopeDir)

	// 等待补扫描完成（shouldCompensateScan + scanOnce + updateLastScanAt 同步执行）
	time.Sleep(200 * time.Millisecond)
	cancel()
	time.Sleep(50 * time.Millisecond) // 等 goroutine 退出

	// 验证 .last_scan_at 已更新为最近时间
	data, err := os.ReadFile(filepath.Join(scopeDir, lastScanAtFileName))
	if err != nil {
		t.Fatalf("read .last_scan_at: %v", err)
	}
	newTime, err := time.Parse(time.RFC3339, string(data))
	if err != nil {
		t.Fatalf("parse .last_scan_at %q: %v", string(data), err)
	}
	if !newTime.After(oldTime) {
		t.Errorf(".last_scan_at 未更新, 旧=%v, 新=%v (期望补扫描触发后更新为最近时间)", oldTime, newTime)
	}
}

// TestStartForgettingScanner_NoCompensate .last_scan_at 距今 < 24h, 启动时不补扫描。
// 写入 1 小时前的 .last_scan_at，启动 scanner 后不应触发补扫描，.last_scan_at 保持不变。
func TestStartForgettingScanner_NoCompensate(t *testing.T) {
	resetCapabilityStoreForTest(t)

	scopeDir := t.TempDir()

	// 写入 1 小时前的 .last_scan_at（近期扫描）
	recentTime := time.Now().Add(-1 * time.Hour)
	recentTimestamp := recentTime.Format(time.RFC3339)
	if err := os.WriteFile(filepath.Join(scopeDir, lastScanAtFileName), []byte(recentTimestamp), 0o644); err != nil {
		t.Fatalf("write .last_scan_at: %v", err)
	}

	// 启动 scanner
	ctx, cancel := context.WithCancel(context.Background())
	go StartForgettingScanner(ctx, scopeDir)

	// 等待一小段时间（确保 scanner 已进入 for 循环，未触发补扫描）
	time.Sleep(200 * time.Millisecond)
	cancel()
	time.Sleep(50 * time.Millisecond) // 等 goroutine 退出

	// 验证 .last_scan_at 仍是近期时间（未被更新）
	// 注：用 RFC3339 字符串比较而非 time.Equal，避免亚秒精度+单调时钟差异导致误判
	data, err := os.ReadFile(filepath.Join(scopeDir, lastScanAtFileName))
	if err != nil {
		t.Fatalf("read .last_scan_at: %v", err)
	}
	currentStr := string(data)
	expectedStr := recentTime.Format(time.RFC3339)
	if currentStr != expectedStr {
		t.Errorf(".last_scan_at 被更新了, 旧=%s, 新=%s (期望近期扫描不补扫描)", expectedStr, currentStr)
	}
}

// TestStartForgettingScanner_ContextCancel ctx.Done() 时 goroutine 退出 (防泄漏)。
// 写入近期 .last_scan_at（避免补扫描阻塞），启动 scanner 后立即取消 ctx，
// goroutine 应在 1 秒内退出，防止 goroutine 泄漏。
func TestStartForgettingScanner_ContextCancel(t *testing.T) {
	resetCapabilityStoreForTest(t)

	scopeDir := t.TempDir()

	// 写入近期 .last_scan_at（避免补扫描阻塞）
	recentTime := time.Now().Add(-1 * time.Hour)
	recentTimestamp := recentTime.Format(time.RFC3339)
	if err := os.WriteFile(filepath.Join(scopeDir, lastScanAtFileName), []byte(recentTimestamp), 0o644); err != nil {
		t.Fatalf("write .last_scan_at: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		StartForgettingScanner(ctx, scopeDir)
		close(done)
	}()

	// 立即取消 ctx
	cancel()

	// 验证 goroutine 在 1 秒内退出（防泄漏）
	select {
	case <-done:
		// 成功: goroutine 已退出
	case <-time.After(1 * time.Second):
		t.Fatal("StartForgettingScanner 未在 1 秒内退出 (ctx 取消后 goroutine 泄漏)")
	}
}

// TestShouldCompensateScan_FileNotExist .last_scan_at 不存在, 返回 true。
// 首次启动场景：无 .last_scan_at 文件，应触发补扫描。
func TestShouldCompensateScan_FileNotExist(t *testing.T) {
	t.Parallel()
	scopeDir := t.TempDir()

	// 不创建 .last_scan_at 文件
	result := shouldCompensateScan(scopeDir)
	if !result {
		t.Errorf("shouldCompensateScan() = false, 期望 true (.last_scan_at 不存在应补扫描)")
	}
}

// TestShouldCompensateScan_RecentScan .last_scan_at 距今 < 24h, 返回 false。
// 近期已扫描场景：1 小时前的 .last_scan_at，不应补扫描。
func TestShouldCompensateScan_RecentScan(t *testing.T) {
	t.Parallel()
	scopeDir := t.TempDir()

	// 写入 1 小时前的 .last_scan_at
	recentTime := time.Now().Add(-1 * time.Hour)
	recentTimestamp := recentTime.Format(time.RFC3339)
	if err := os.WriteFile(filepath.Join(scopeDir, lastScanAtFileName), []byte(recentTimestamp), 0o644); err != nil {
		t.Fatalf("write .last_scan_at: %v", err)
	}

	result := shouldCompensateScan(scopeDir)
	if result {
		t.Errorf("shouldCompensateScan() = true, 期望 false (近期扫描 < 24h 不补扫描)")
	}
}
