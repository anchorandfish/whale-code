// capability_test.go — H11/N27 Phase A A2 capability 接口测试（含 abstention 反例）。
package memory

import (
	"context"
	"errors"
	"testing"
)

// newTestCapabilityEntry 构造测试用 capability 记忆（含 verification 三态）。
func newTestCapabilityEntry(name, verification string) *MemoryEntry {
	return &MemoryEntry{
		Name:         name,
		Description:  "test capability",
		Type:         MemoryTypeProject,
		TrustLevel:   TrustCapability,
		Verification: verification,
		Content:      "test-value",
		Source:       SourceProbeFn,
		Importance:   50,
	}
}

// TestGetCapabilityWithMeta_Verified verification=verified 返回完整 entry，AccessCount 自增。
// 反例防护（致命）：防 verified 被错误 abstain。
func TestGetCapabilityWithMeta_Verified(t *testing.T) {
	ResetCapabilityRegistryForTest()
	entry := newTestCapabilityEntry("gcc-available", VerificationVerified)
	RegisterCapabilityForTest("gcc-available", entry)

	got, err := GetCapabilityWithMeta("gcc-available")
	if err != nil {
		t.Fatalf("verified capability should return entry, got error: %v", err)
	}
	if got == nil {
		t.Fatal("verified capability should return non-nil entry")
	}
	if got.Name != "gcc-available" {
		t.Errorf("Name = %q, expected gcc-available", got.Name)
	}
	if got.AccessCount != 1 {
		t.Errorf("AccessCount = %d, expected 1 (自增)", got.AccessCount)
	}
	if got.LastAccessedAt.IsZero() {
		t.Error("LastAccessedAt should be set (non-zero)")
	}
}

// TestGetCapabilityWithMeta_Unverified_ReturnsWithWarn verification=unverified 返回 entry + warn 日志。
// 反例防护：防 unverified 被错误拒绝。
func TestGetCapabilityWithMeta_Unverified_ReturnsWithWarn(t *testing.T) {
	ResetCapabilityRegistryForTest()
	entry := newTestCapabilityEntry("experimental-feature", VerificationUnverified)
	RegisterCapabilityForTest("experimental-feature", entry)

	got, err := GetCapabilityWithMeta("experimental-feature")
	if err != nil {
		t.Fatalf("unverified capability should return entry (not error), got: %v", err)
	}
	if got == nil {
		t.Fatal("unverified capability should return non-nil entry")
	}
	if got.AccessCount != 1 {
		t.Errorf("AccessCount = %d, expected 1 (unverified 仍需自增)", got.AccessCount)
	}
}

// TestGetCapabilityWithMeta_Contradicted_Abstention verification=contradicted 返回 ErrCapabilityNotFound。
// 致命反例（abstention 三态核心防护）：contradicted 必须 abstain，绝不可返回。
func TestGetCapabilityWithMeta_Contradicted_Abstention(t *testing.T) {
	ResetCapabilityRegistryForTest()
	entry := newTestCapabilityEntry("broken-dep", VerificationContradicted)
	RegisterCapabilityForTest("broken-dep", entry)

	got, err := GetCapabilityWithMeta("broken-dep")
	if err == nil {
		t.Fatal("contradicted capability must abstain (return ErrCapabilityNotFound), got nil error")
	}
	if !errors.Is(err, ErrCapabilityNotFound) {
		t.Errorf("contradicted should return ErrCapabilityNotFound, got: %v", err)
	}
	if got != nil {
		t.Errorf("contradicted should return nil entry, got: %v", got)
	}
}

// TestGetCapabilityWithMeta_AccessCount_Increment 连续 2 次调用，AccessCount 从 0→1→2。
// 反例防护：防内存字段未更新。
func TestGetCapabilityWithMeta_AccessCount_Increment(t *testing.T) {
	ResetCapabilityRegistryForTest()
	entry := newTestCapabilityEntry("counter-test", VerificationVerified)
	RegisterCapabilityForTest("counter-test", entry)

	if entry.AccessCount != 0 {
		t.Fatalf("初始 AccessCount = %d, expected 0", entry.AccessCount)
	}

	// 第一次调用
	if _, err := GetCapabilityWithMeta("counter-test"); err != nil {
		t.Fatalf("第一次调用失败: %v", err)
	}
	if entry.AccessCount != 1 {
		t.Errorf("第一次调用后 AccessCount = %d, expected 1", entry.AccessCount)
	}

	// 第二次调用
	if _, err := GetCapabilityWithMeta("counter-test"); err != nil {
		t.Fatalf("第二次调用失败: %v", err)
	}
	if entry.AccessCount != 2 {
		t.Errorf("第二次调用后 AccessCount = %d, expected 2", entry.AccessCount)
	}
}

// TestGetCapabilityWithMeta_NotFound key 不存在返回 ErrCapabilityNotFound。
func TestGetCapabilityWithMeta_NotFound(t *testing.T) {
	ResetCapabilityRegistryForTest()

	got, err := GetCapabilityWithMeta("nonexistent-key")
	if err == nil {
		t.Fatal("未找到的 key 应返回 error")
	}
	if !errors.Is(err, ErrCapabilityNotFound) {
		t.Errorf("未找到应返回 ErrCapabilityNotFound, got: %v", err)
	}
	if got != nil {
		t.Errorf("未找到应返回 nil entry, got: %v", got)
	}
}

// TestGetCapability_Contradicted_Abstention GetCapability 也对 contradicted abstain（与 GetCapabilityWithMeta 一致）。
func TestGetCapability_Contradicted_Abstention(t *testing.T) {
	ResetCapabilityRegistryForTest()
	entry := newTestCapabilityEntry("broken-cap", VerificationContradicted)
	RegisterCapabilityForTest("broken-cap", entry)

	cv, err := GetCapability("broken-cap")
	if err == nil {
		t.Fatal("contradicted capability must abstain")
	}
	if !errors.Is(err, ErrCapabilityNotFound) {
		t.Errorf("contradicted should return ErrCapabilityNotFound, got: %v", err)
	}
	if cv.Value != nil {
		t.Errorf("contradicted should return zero-value CapabilityValue, got Value: %v", cv.Value)
	}
}

// TestGetCapability_Unverified_TrustSuffix unverified 时 CapabilityValue.Trust 含 ":unverified" 后缀。
func TestGetCapability_Unverified_TrustSuffix(t *testing.T) {
	ResetCapabilityRegistryForTest()
	entry := newTestCapabilityEntry("unverified-cap", VerificationUnverified)
	RegisterCapabilityForTest("unverified-cap", entry)

	cv, err := GetCapability("unverified-cap")
	if err != nil {
		t.Fatalf("unverified should not return error, got: %v", err)
	}
	if cv.Trust != TrustCapability+":unverified" {
		t.Errorf("unverified Trust = %q, expected %q", cv.Trust, TrustCapability+":unverified")
	}
}

// TestGetCapability_Verified_TrustNoSuffix verified 时 CapabilityValue.Trust 无后缀。
func TestGetCapability_Verified_TrustNoSuffix(t *testing.T) {
	ResetCapabilityRegistryForTest()
	entry := newTestCapabilityEntry("verified-cap", VerificationVerified)
	RegisterCapabilityForTest("verified-cap", entry)

	cv, err := GetCapability("verified-cap")
	if err != nil {
		t.Fatalf("verified should not return error, got: %v", err)
	}
	if cv.Trust != TrustCapability {
		t.Errorf("verified Trust = %q, expected %q (无后缀)", cv.Trust, TrustCapability)
	}
}

// TestGetCapabilitiesBatch_PartialFailure 3 个 key 其中 1 个 contradicted，返回 map 含 2 个成功 + 1 个跳过。
// 反例防护：单个失败不阻塞其余。
func TestGetCapabilitiesBatch_PartialFailure(t *testing.T) {
	ResetCapabilityRegistryForTest()

	RegisterCapabilityForTest("key-1-ok", newTestCapabilityEntry("key-1-ok", VerificationVerified))
	RegisterCapabilityForTest("key-2-ok", newTestCapabilityEntry("key-2-ok", VerificationVerified))
	RegisterCapabilityForTest("key-3-broken", newTestCapabilityEntry("key-3-broken", VerificationContradicted))

	keys := []string{"key-1-ok", "key-2-ok", "key-3-broken", "key-4-missing"}
	result, err := GetCapabilitiesBatch(context.Background(), keys)

	// 部分成功不应返回 error
	if err != nil {
		t.Errorf("部分失败不应返回 error, got: %v", err)
	}
	if len(result) != 2 {
		t.Errorf("部分失败应返回 2 个成功结果, got %d", len(result))
	}
	if _, ok := result["key-1-ok"]; !ok {
		t.Error("result 缺失 key-1-ok")
	}
	if _, ok := result["key-2-ok"]; !ok {
		t.Error("result 缺失 key-2-ok")
	}
	if _, ok := result["key-3-broken"]; ok {
		t.Error("contradicted key 不应在结果中")
	}
	if _, ok := result["key-4-missing"]; ok {
		t.Error("missing key 不应在结果中")
	}
}

// TestGetCapabilitiesBatch_AllFail 全部失败返回 error。
func TestGetCapabilitiesBatch_AllFail(t *testing.T) {
	ResetCapabilityRegistryForTest()

	RegisterCapabilityForTest("broken-1", newTestCapabilityEntry("broken-1", VerificationContradicted))
	RegisterCapabilityForTest("broken-2", newTestCapabilityEntry("broken-2", VerificationContradicted))

	keys := []string{"broken-1", "broken-2", "missing"}
	result, err := GetCapabilitiesBatch(context.Background(), keys)

	if err == nil {
		t.Error("全部失败应返回 error")
	}
	if len(result) != 0 {
		t.Errorf("全部失败应返回空 map, got %d 个结果", len(result))
	}
}

// TestGetCapabilitiesBatch_EmptyKeys 空 keys 返回 nil, nil。
func TestGetCapabilitiesBatch_EmptyKeys(t *testing.T) {

	result, err := GetCapabilitiesBatch(context.Background(), nil)
	if err != nil {
		t.Errorf("空 keys 不应返回 error, got: %v", err)
	}
	if result != nil {
		t.Errorf("空 keys 应返回 nil, got: %v", result)
	}
}

// TestGetCapabilitiesBatch_ContextCancel ctx 取消时返回 context error。
func TestGetCapabilitiesBatch_ContextCancel(t *testing.T) {
	ResetCapabilityRegistryForTest()

	// 注册一些 entries 让循环至少跑一次
	for i := 0; i < 5; i++ {
		key := string(rune('a' + i))
		RegisterCapabilityForTest(key, newTestCapabilityEntry(key, VerificationVerified))
	}

	ctx, cancel := context.WithCancel(context.Background())
	cancel() // 立即取消

	keys := []string{"a", "b", "c", "d", "e"}
	_, err := GetCapabilitiesBatch(ctx, keys)
	if err == nil {
		t.Error("ctx 取消时应返回 error")
	}
}

// TestRegisterCapabilityForTest_Overwrite 测试注入覆盖语义（同名 key 后写覆盖先写）。
func TestRegisterCapabilityForTest_Overwrite(t *testing.T) {
	ResetCapabilityRegistryForTest()

	entry1 := newTestCapabilityEntry("overwrite-test", VerificationVerified)
	RegisterCapabilityForTest("overwrite-test", entry1)

	entry2 := newTestCapabilityEntry("overwrite-test", VerificationUnverified)
	RegisterCapabilityForTest("overwrite-test", entry2)

	got, err := GetCapabilityWithMeta("overwrite-test")
	if err != nil {
		t.Fatalf("读取失败: %v", err)
	}
	// 应返回后写入的 entry（unverified）
	if got.DefaultVerification() != VerificationUnverified {
		t.Errorf("覆盖后 verification = %q, expected unverified", got.DefaultVerification())
	}
}
