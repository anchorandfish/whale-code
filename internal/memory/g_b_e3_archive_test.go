// g_b_e3_archive_test.go — G-B（O-B）E3 判别性测试：归档式 GC（不硬删）。
//
// 决策依据：集中拍板_决策表与并行修复分组_2026-09-14.md §2 E3 行（S18-G1）：
//
//	E3 | ApplyForgetting 只软标记不删 ⇒ 磁盘只增不减
//	   | **引入归档式 GC（不硬删）+ 持久化访问热度**
//
// 语义：「清理」= 从**活动目录**移出到 _archive/，字节永不丢；
// 且必须是**可判定的结构化事实**（verification==contradicted ∧ 失效时间超过宽限期），
// 不得靠「扫描到就删」的散文式判断。
package memory

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func scanFixtureStore(t *testing.T, dir string) *MemoryStore {
	t.Helper()
	store, err := NewMemoryStoreForScope(dir, "user", false)
	if err != nil {
		t.Fatal(err)
	}
	ResetCapabilityRegistryForTest()
	old := defaultMemoryStore
	SetDefaultMemoryStore(store)
	t.Cleanup(func() { SetDefaultMemoryStore(old) })
	return store
}

// seedRawEntry 直接落盘一条 frontmatter 记忆（绕过 Write 的时间戳改写，
// 用于构造「久远的失效记录」这一生产上真实存在的历史状态）。
func seedRawEntry(t *testing.T, dir, name string, entry *MemoryEntry) {
	t.Helper()
	data, err := serializeMemoryEntry(entry)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, name+".md"), data, 0o600); err != nil {
		t.Fatal(err)
	}
}

// TestG_B_E3_ScanArchivesContradictedBeyondGrace —— 归档主语义。
func TestG_B_E3_ScanArchivesContradictedBeyondGrace(t *testing.T) {
	dir := t.TempDir()
	store := scanFixtureStore(t, dir)

	past := time.Now().Add(-30 * 24 * time.Hour)
	expired := &MemoryEntry{
		Name:         "e3-arch-candidate",
		Description:  "expired entry",
		Type:         MemoryTypeUser,
		TrustLevel:   TrustEphemeral,
		Verification: VerificationContradicted,
		CreatedAt:    past,
		UpdatedAt:    past,
		ValidUntil:   &past,
		Content:      "obsolete body",
	}
	seedRawEntry(t, store.Dir(), expired.Name, expired)

	if err := scanOnce(store.Dir()); err != nil {
		t.Fatal(err)
	}

	// 1) 活动目录里不再有它（已被 GC 摘除，磁盘不再只增不减）
	if _, err := os.Stat(filepath.Join(store.Dir(), "e3-arch-candidate.md")); !os.IsNotExist(err) {
		t.Fatalf("E3：超过宽限期的 contradicted 记忆必须从活动目录移出（stat err=%v）", err)
	}
	// 2) 归档目录里有它，且内容完整 —— 这证明是「归档」而不是「硬删」
	archPath := filepath.Join(store.Dir(), memoryArchiveDirName, "e3-arch-candidate.md")
	body, err := os.ReadFile(archPath)
	if err != nil {
		t.Fatalf("E3：归档件不存在（=硬删，违反「不硬删」）：%v", err)
	}
	if !strings.Contains(string(body), "obsolete body") {
		t.Fatalf("E3：归档件内容不完整：%q", body)
	}
	// 3) 归档件不得被当成活动记忆
	list, err := store.List()
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range list {
		if e.Name == "e3-arch-candidate" {
			t.Fatal("E3：归档件不得出现在 List() 的活动记忆中")
		}
	}
}

// TestG_B_E3_ScanKeepsContradictedWithinGrace —— 宽限期内不得归档（不得误杀可恢复的记忆）。
func TestG_B_E3_ScanKeepsContradictedWithinGrace(t *testing.T) {
	dir := t.TempDir()
	store := scanFixtureStore(t, dir)

	now := time.Now()
	fresh := &MemoryEntry{
		Name:         "e3-arch-fresh",
		Description:  "just contradicted",
		Type:         MemoryTypeUser,
		TrustLevel:   TrustEphemeral,
		Verification: VerificationContradicted,
		CreatedAt:    now,
		UpdatedAt:    now,
		ValidUntil:   &now, // 刚失效
		Content:      "recent",
	}
	seedRawEntry(t, store.Dir(), fresh.Name, fresh)

	if err := scanOnce(store.Dir()); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(store.Dir(), "e3-arch-fresh.md")); err != nil {
		t.Fatalf("E3：宽限期内的 contradicted 记忆不得被归档：%v", err)
	}
}

// TestG_B_E3_ScanNeverArchivesNonContradicted —— 反例守卫：正常记忆永不归档。
func TestG_B_E3_ScanNeverArchivesNonContradicted(t *testing.T) {
	dir := t.TempDir()
	store := scanFixtureStore(t, dir)

	old := time.Now().Add(-365 * 24 * time.Hour)
	entries := []*MemoryEntry{
		{Name: "e3-keep-stable", Description: "stable", Type: MemoryTypeProject,
			TrustLevel: TrustStable, Verification: VerificationVerified,
			CreatedAt: old, UpdatedAt: old, Content: "stable body"},
		{Name: "e3-keep-ephemeral", Description: "unverified ephemeral", Type: MemoryTypeUser,
			TrustLevel: TrustEphemeral, Verification: VerificationUnverified,
			CreatedAt: old, UpdatedAt: old, Content: "ephemeral body"},
	}
	for _, e := range entries {
		seedRawEntry(t, store.Dir(), e.Name, e)
	}
	if err := scanOnce(store.Dir()); err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		if _, err := os.Stat(filepath.Join(store.Dir(), e.Name+".md")); err != nil {
			t.Fatalf("E3：非 contradicted 的记忆被误归档（%s）：%v", e.Name, err)
		}
	}
}

// TestG_B_E3_FileLimitArchivesInsteadOfDeleting —— 条目数上限路径也不得硬删。
// project scope 触发 MaxMemoryFiles 淘汰时，被淘汰的条目必须可在 _archive/ 找回。
//
// 触达方式：先直接铺满 fixture（= 生产上「目录里已有超过上限的历史条目」这一真实状态，
// 避免用 210 次 Write+fsync 拖慢单测），再用**一次真实 Write** 触发生产清理路径
// enforceFileLimitLocked（由 MemoryStore.Write 内部调用）。
func TestG_B_E3_FileLimitArchivesInsteadOfDeleting(t *testing.T) {
	baseDir := t.TempDir()
	store, err := NewMemoryStoreForProject(baseDir, "/test/e3/limit")
	if err != nil {
		t.Fatal(err)
	}

	base := time.Now().Add(-time.Duration(MaxMemoryFiles+20) * time.Minute)
	total := MaxMemoryFiles + 10
	for i := 0; i < total; i++ {
		seedRawEntry(t, store.Dir(), nameFromIndex("e3lim", i), &MemoryEntry{
			Name:         nameFromIndex("e3lim", i),
			Description:  "limit victim",
			Type:         MemoryTypeProject,
			TrustLevel:   TrustStable,
			Verification: VerificationVerified,
			CreatedAt:    base.Add(time.Duration(i) * time.Minute),
			UpdatedAt:    base.Add(time.Duration(i) * time.Minute),
			Content:      "victim body",
		})
	}

	// 生产入口：一次 Write 触发上限清理（超出上限的条目应被归档而非删除）
	if err := store.Write(&MemoryEntry{
		Name:        "e3lim-trigger",
		Description: "trigger",
		Type:        MemoryTypeProject,
		Content:     "trigger body",
	}); err != nil {
		t.Fatalf("Write 触发上限清理失败: %v", err)
	}

	// 活动目录被压到上限
	files, _ := os.ReadDir(store.Dir())
	var mdCount int
	for _, f := range files {
		if !f.IsDir() && strings.HasSuffix(strings.ToLower(f.Name()), ".md") {
			mdCount++
		}
	}
	if mdCount != MaxMemoryFiles {
		t.Fatalf("E3：上限未生效：活动目录 %d 个 .md（期望 %d）", mdCount, MaxMemoryFiles)
	}

	// 被淘汰的条目必须在 _archive/ 里（不硬删）
	wantArchived := total + 1 - MaxMemoryFiles
	archEntries, err := os.ReadDir(filepath.Join(store.Dir(), memoryArchiveDirName))
	if err != nil {
		t.Fatalf("E3：归档目录不存在（被淘汰条目 = 硬删，数据丢失）：%v", err)
	}
	if len(archEntries) != wantArchived {
		t.Fatalf("E3：归档件数量 %d（期望 %d）", len(archEntries), wantArchived)
	}
	for _, e := range archEntries {
		body, err := os.ReadFile(filepath.Join(store.Dir(), memoryArchiveDirName, e.Name()))
		if err != nil {
			t.Fatalf("E3：归档件不可读：%v", err)
		}
		if !strings.Contains(string(body), "victim body") {
			t.Fatalf("E3：归档件内容不完整：%q", body)
		}
	}
	// 归档件不得被当成活动记忆
	list, err := store.List()
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != MaxMemoryFiles {
		t.Fatalf("E3：List() 返回 %d 条（期望 %d，归档件不得混入）", len(list), MaxMemoryFiles)
	}
}
