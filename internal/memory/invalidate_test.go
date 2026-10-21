// invalidate_test.go — H11/N27 Phase B B5 测试。
// 含致命反例：
//   - TestInvalidateMemory_to_contradicted（verification→contradicted + valid_until≈now）
//   - TestInvalidateMemory_persisted（文件持久化：重新 Read 验证字段已写入）
//   - TestInvalidateMemory_capability_store_cleaned（defer 清理生效，避免全局污染）
//
// 测试覆盖：
//   - TestInvalidateMemory_to_contradicted（致命反例）：verified → contradicted + valid_until
//   - TestInvalidateMemory_unverified_to_contradicted：unverified → contradicted
//   - TestInvalidateMemory_already_contradicted：终态再调返回 ErrInvalidTransition
//   - TestInvalidateMemory_not_found：name 不存在返回 ErrEntryNotFound
//   - TestInvalidateMemory_empty_name：空 name 返回 error
//   - TestInvalidateMemory_persisted（致命反例）：文件已写入
//   - TestInvalidateMemory_capability_store_cleaned（致命反例）：capabilityStore 已清理
//   - TestInvalidateMemory_cross_scope：project scope 也可失效

package memory

import (
	"strings"
	"testing"
	"time"
)

// setupInvalidateTest 创建测试用 MergeLayer + 清理 capabilityStore。
// 复用 setupTestMergeLayer（已注入 globalMergeLayer + t.Cleanup），
// 额外清理 capabilityStore 避免测试间状态污染（InvalidateMemory 会临时注入）。
func setupInvalidateTest(t *testing.T, workDir string) (baseDir string, ml *MergeLayer) {
	t.Helper()
	baseDir, ml = setupTestMergeLayer(t, workDir)
	// 清理 capabilityStore（InvalidateMemory 会临时注入 entry）
	t.Cleanup(ResetCapabilityRegistryForTest)
	return baseDir, ml
}

// writeVerifiedEntry 写入 verified 记忆（测试辅助）。
func writeVerifiedEntry(t *testing.T, store *MemoryStore, name string) {
	t.Helper()
	entry := &MemoryEntry{
		Name:         name,
		Description:  "verified entry for invalidate test",
		Type:         MemoryTypeUser,
		CreatedAt:    time.Now(),
		Content:      "verified content",
		TrustLevel:   TrustStable,
		Verification: VerificationVerified,
		Importance:   80,
		Source:       SourceUser,
	}
	if err := store.Write(entry); err != nil {
		t.Fatalf("Write verified entry %q 失败: %v", name, err)
	}
}

// writeUnverifiedEntry 写入 unverified 记忆（测试辅助）。
func writeUnverifiedEntry(t *testing.T, store *MemoryStore, name string) {
	t.Helper()
	entry := &MemoryEntry{
		Name:         name,
		Description:  "unverified entry for invalidate test",
		Type:         MemoryTypeFeedback,
		CreatedAt:    time.Now(),
		Content:      "unverified content",
		TrustLevel:   TrustProcedural,
		Verification: VerificationUnverified,
		Importance:   60,
		Source:       SourceReflection,
	}
	if err := store.Write(entry); err != nil {
		t.Fatalf("Write unverified entry %q 失败: %v", name, err)
	}
}

// writeContradictedEntry 写入 contradicted 记忆（测试辅助，终态）。
func writeContradictedEntry(t *testing.T, store *MemoryStore, name string) {
	t.Helper()
	now := time.Now()
	entry := &MemoryEntry{
		Name:         name,
		Description:  "contradicted entry for invalidate test",
		Type:         MemoryTypeProject,
		CreatedAt:    time.Now().Add(-1 * time.Hour),
		Content:      "contradicted content",
		TrustLevel:   TrustStable,
		Verification: VerificationContradicted,
		ValidUntil:   &now,
		Importance:   30,
		Source:       SourceUser,
	}
	if err := store.Write(entry); err != nil {
		t.Fatalf("Write contradicted entry %q 失败: %v", name, err)
	}
}

// TestInvalidateMemory_to_contradicted 致命反例（contradicted + valid_until≈now）：
// verified 记忆调 InvalidateMemory → verification="contradicted" + valid_until≈now。
func TestInvalidateMemory_to_contradicted(t *testing.T) {
	_, ml := setupInvalidateTest(t, "/test/work/invalidate-verified")
	writeVerifiedEntry(t, ml.userStore, "verified-entry")

	before := time.Now()
	if err := InvalidateMemory("verified-entry", "user reported outdated"); err != nil {
		t.Fatalf("InvalidateMemory 失败: %v", err)
	}
	after := time.Now()

	// 验证 verification + valid_until
	readEntry, err := ml.userStore.Read("verified-entry")
	if err != nil {
		t.Fatalf("Read 失败: %v", err)
	}
	if readEntry.Verification != VerificationContradicted {
		t.Errorf("致命反例失效: verification = %q, 期望 %q", readEntry.Verification, VerificationContradicted)
	}
	if readEntry.ValidUntil == nil {
		t.Fatal("致命反例失效: ValidUntil 仍为 nil, 期望≈now")
	}
	if readEntry.ValidUntil.Before(before) || readEntry.ValidUntil.After(after) {
		t.Errorf("致命反例失效: ValidUntil = %v, 期望在 [%v, %v] 区间", readEntry.ValidUntil, before, after)
	}

	// UpdatedAt 也应刷新
	if readEntry.UpdatedAt.Before(before) || readEntry.UpdatedAt.After(after) {
		t.Errorf("UpdatedAt 未刷新: %v, 期望在 [%v, %v] 区间", readEntry.UpdatedAt, before, after)
	}

	// 其他字段不变（不应被污染）
	if readEntry.Name != "verified-entry" {
		t.Errorf("Name 被污染: %q, 期望 %q", readEntry.Name, "verified-entry")
	}
	if readEntry.Content != "verified content" {
		t.Errorf("Content 被污染: %q, 期望 %q", readEntry.Content, "verified content")
	}
	if readEntry.Importance != 80 {
		t.Errorf("Importance 被污染: %d, 期望 80", readEntry.Importance)
	}
}

// TestInvalidateMemory_unverified_to_contradicted 验证 unverified→contradicted 合法流转。
func TestInvalidateMemory_unverified_to_contradicted(t *testing.T) {
	_, ml := setupInvalidateTest(t, "/test/work/invalidate-unverified")
	writeUnverifiedEntry(t, ml.userStore, "unverified-entry")

	if err := InvalidateMemory("unverified-entry", "reflection rejected by user"); err != nil {
		t.Fatalf("InvalidateMemory 失败: %v", err)
	}

	readEntry, err := ml.userStore.Read("unverified-entry")
	if err != nil {
		t.Fatalf("Read 失败: %v", err)
	}
	if readEntry.Verification != VerificationContradicted {
		t.Errorf("verification = %q, 期望 %q", readEntry.Verification, VerificationContradicted)
	}
	if readEntry.ValidUntil == nil {
		t.Error("ValidUntil 仍为 nil, 期望≈now")
	}
}

// TestInvalidateMemory_already_contradicted 验证终态再调返回 ErrInvalidTransition。
func TestInvalidateMemory_already_contradicted(t *testing.T) {
	_, ml := setupInvalidateTest(t, "/test/work/invalidate-already")
	writeContradictedEntry(t, ml.userStore, "already-contradicted")

	err := InvalidateMemory("already-contradicted", "double invalidate")
	if err == nil {
		t.Fatal("InvalidateMemory(already-contradicted) 应返回 error, 实际 nil")
	}
	if !strings.Contains(err.Error(), ErrInvalidTransition.Error()) {
		t.Errorf("error 应含 ErrInvalidTransition: %v", err)
	}

	// 验证 ValidUntil 未被覆盖（保留原值）
	readEntry, err := ml.userStore.Read("already-contradicted")
	if err != nil {
		t.Fatalf("Read 失败: %v", err)
	}
	if readEntry.Verification != VerificationContradicted {
		t.Errorf("verification 被污染: %q, 期望 %q", readEntry.Verification, VerificationContradicted)
	}
}

// TestInvalidateMemory_not_found 验证 name 不存在返回 ErrEntryNotFound。
func TestInvalidateMemory_not_found(t *testing.T) {
	_, _ = setupInvalidateTest(t, "/test/work/invalidate-not-found")

	err := InvalidateMemory("nonexistent", "test")
	if err == nil {
		t.Fatal("InvalidateMemory(nonexistent) 应返回 error, 实际 nil")
	}
	if !strings.Contains(err.Error(), ErrEntryNotFound.Error()) {
		t.Errorf("error 应含 ErrEntryNotFound: %v", err)
	}
}

// TestInvalidateMemory_empty_name 验证空 name 返回 error。
func TestInvalidateMemory_empty_name(t *testing.T) {
	_, _ = setupInvalidateTest(t, "/test/work/invalidate-empty")

	err := InvalidateMemory("", "empty name")
	if err == nil {
		t.Fatal("InvalidateMemory(空 name) 应返回 error, 实际 nil")
	}
	if !strings.Contains(err.Error(), "must not be empty") {
		t.Errorf("error 信息不匹配: %v", err)
	}
}

// TestInvalidateMemory_persisted 致命反例（文件持久化）：
// InvalidateMemory 后重新 Read 验证文件已写入（verification=contradicted + valid_until）。
func TestInvalidateMemory_persisted(t *testing.T) {
	_, ml := setupInvalidateTest(t, "/test/work/invalidate-persisted")
	writeVerifiedEntry(t, ml.userStore, "persist-entry")

	before := time.Now()
	if err := InvalidateMemory("persist-entry", "persisted test"); err != nil {
		t.Fatalf("InvalidateMemory 失败: %v", err)
	}

	// 重新 Read 验证文件已写入
	readEntry, err := ml.userStore.Read("persist-entry")
	if err != nil {
		t.Fatalf("Read 失败: %v", err)
	}
	if readEntry.Verification != VerificationContradicted {
		t.Errorf("致命反例失效: 文件中 verification = %q, 期望 %q", readEntry.Verification, VerificationContradicted)
	}
	if readEntry.ValidUntil == nil {
		t.Fatal("致命反例失效: 文件中 ValidUntil 仍为 nil, 期望≈now")
	}
	if readEntry.ValidUntil.Before(before) {
		t.Errorf("致命反例失效: ValidUntil = %v, 期望晚于 %v", readEntry.ValidUntil, before)
	}

	// 验证 MergeLayer.ListMerged 也能读到新状态（避免内存缓存与文件不一致）
	merged, err := ml.ListMerged()
	if err != nil {
		t.Fatalf("ListMerged 失败: %v", err)
	}
	var found *MemoryEntry
	for _, e := range merged {
		if e.Name == "persist-entry" {
			found = e
			break
		}
	}
	if found == nil {
		t.Fatal("ListMerged 未找到 persist-entry")
	}
	if found.Verification != VerificationContradicted {
		t.Errorf("致命反例失效: ListMerged 中 verification = %q, 期望 %q", found.Verification, VerificationContradicted)
	}
}

// TestInvalidateMemory_capability_store_cleaned 致命反例（capabilityStore 已清理）：
// InvalidateMemory 用 defer unregisterEntryForTransition 清理，
// 验证调用后 capabilityStore 不残留 entry（避免全局污染）。
func TestInvalidateMemory_capability_store_cleaned(t *testing.T) {
	_, ml := setupInvalidateTest(t, "/test/work/invalidate-cleanup")
	writeVerifiedEntry(t, ml.userStore, "cleanup-entry")

	if err := InvalidateMemory("cleanup-entry", "cleanup test"); err != nil {
		t.Fatalf("InvalidateMemory 失败: %v", err)
	}

	// 验证 capabilityStore 已清理（defer unregisterEntryForTransition 生效）
	capabilityStore.mu.RLock()
	_, exists := capabilityStore.entries["cleanup-entry"]
	capabilityStore.mu.RUnlock()
	if exists {
		t.Errorf("致命反例失效: capabilityStore 残留 cleanup-entry, 期望已清理（defer unregister 未生效）")
	}

	// 验证再次调 InvalidateMemory 仍能找到 entry（从文件重新加载）
	if err := InvalidateMemory("cleanup-entry", "second call should fail"); err == nil {
		t.Error("第二次 InvalidateMemory 应返回 ErrInvalidTransition（已 contradicted）, 实际 nil")
	} else if !strings.Contains(err.Error(), ErrInvalidTransition.Error()) {
		t.Errorf("第二次 error 应含 ErrInvalidTransition: %v", err)
	}
}

// TestInvalidateMemory_cross_scope 验证 project scope 也可失效。
func TestInvalidateMemory_cross_scope(t *testing.T) {
	_, ml := setupInvalidateTest(t, "/test/work/invalidate-cross")
	// 写入 project scope
	writeVerifiedEntry(t, ml.projectStore, "project-entry")

	if err := InvalidateMemory("project-entry", "project scope invalidate"); err != nil {
		t.Fatalf("InvalidateMemory 失败: %v", err)
	}

	// 验证 project store 中 entry 已失效
	readEntry, err := ml.projectStore.Read("project-entry")
	if err != nil {
		t.Fatalf("projectStore.Read 失败: %v", err)
	}
	if readEntry.Verification != VerificationContradicted {
		t.Errorf("cross scope 失效失败: verification = %q, 期望 %q", readEntry.Verification, VerificationContradicted)
	}
	if readEntry.ValidUntil == nil {
		t.Error("cross scope 失效失败: ValidUntil 仍为 nil")
	}
}
