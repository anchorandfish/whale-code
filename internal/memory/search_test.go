// search_test.go — H11/N27 Phase B B3 测试。
// 含 2 个致命反例：
//   - TestSearchMemory_unverified_procedural_downgrade（小模型错误流程降权，不改原 entry）
//   - TestSearchMemory_modernc_no_cgo（PIT-004 防护，纯 Go 无 CGO）
//
// 测试覆盖：
//   - TestSearchMemory_BM25：FTS5 返回 top-1
//   - TestSearchMemory_top10：返回 top-10
//   - TestSearchMemory_empty_query：空 query 返回 error
//   - TestSearchMemory_no_match：无匹配返回空切片
//   - TestEffectiveImportance：单元测试降权函数
//   - TestCalculateScore：单元测试三因子加权

package memory

import (
	"context"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"
)

// setupTestMergeLayer 创建测试用 MergeLayer（user + project scope）+ 注入全局。
// 返回 baseDir 供测试后续验证。
func setupTestMergeLayer(t *testing.T, workDir string) (baseDir string, ml *MergeLayer) {
	t.Helper()
	baseDir = newTestBaseDir(t)

	userStore, err := NewMemoryStoreForScope(baseDir, "user", false)
	if err != nil {
		t.Fatalf("NewMemoryStoreForScope(user) 失败: %v", err)
	}
	projectStore, err := NewMemoryStoreForProject(baseDir, workDir)
	if err != nil {
		t.Fatalf("NewMemoryStoreForProject 失败: %v", err)
	}

	ml = NewMergeLayer(userStore, projectStore)
	SetGlobalMergeLayer(ml)
	t.Cleanup(func() {
		SetGlobalMergeLayer(nil)
	})
	return baseDir, ml
}

// TestSearchMemory_BM25 验证 FTS5 返回 top-1 + 三因子排序正确。
func TestSearchMemory_BM25(t *testing.T) {
	_, ml := setupTestMergeLayer(t, "/test/work/bm25")

	// 写入 3 条记忆，仅 1 条含 "golang"
	writeEntryToStore(t, ml.userStore, "golang-pref", "golang preferences", "I prefer golang for backend", VerificationVerified)
	writeEntryToStore(t, ml.userStore, "rust-pref", "rust preferences", "I prefer rust for systems", VerificationVerified)
	writeEntryToStore(t, ml.userStore, "python-pref", "python preferences", "I use python for scripts", VerificationVerified)

	ctx := context.Background()
	results, err := SearchMemory(ctx, "golang", SearchOptions{})
	if err != nil {
		t.Fatalf("SearchMemory 失败: %v", err)
	}

	if len(results) == 0 {
		t.Fatal("SearchMemory 返回空结果，期望至少 1 条")
	}

	// top-1 应是 "golang-pref"
	if results[0].Name != "golang-pref" {
		t.Errorf("SearchMemory[0].Name = %q, 期望 %q", results[0].Name, "golang-pref")
	}
}

// TestSearchMemory_unverified_procedural_downgrade 致命反例（小模型错误流程降权）：
// 2 条同 importance=80 的记忆，A 是 verification=unverified + trust_level=procedural，
// B 是 verification=verified + trust_level=stable，query 同时命中。
// 验证：
//  1. A 的 effectiveImportance=40（降权 ×0.5）
//  2. A 排序在 B 之后（B 分数更高）
//  3. 关键反例验证：排序后再次读取 A 的 entry.Importance 仍为 80（未被永久降权）
func TestSearchMemory_unverified_procedural_downgrade(t *testing.T) {
	_, ml := setupTestMergeLayer(t, "/test/work/downgrade")

	// 写入 A：unverified + procedural + importance=80
	entryA := &MemoryEntry{
		Name:         "procedural-flow",
		Description:  "procedural reflection flow",
		Type:         MemoryTypeFeedback,
		CreatedAt:    time.Now(),
		Content:      "golang build procedure",
		TrustLevel:   TrustProcedural,
		Verification: VerificationUnverified,
		Importance:   80,
		Source:       SourceReflection,
	}
	if err := ml.userStore.Write(entryA); err != nil {
		t.Fatalf("Write entryA 失败: %v", err)
	}

	// 写入 B：verified + stable + importance=80
	entryB := &MemoryEntry{
		Name:         "stable-fact",
		Description:  "stable fact about golang",
		Type:         MemoryTypeProject,
		CreatedAt:    time.Now(),
		Content:      "golang stable fact",
		TrustLevel:   TrustStable,
		Verification: VerificationVerified,
		Importance:   80,
		Source:       SourceUser,
	}
	if err := ml.userStore.Write(entryB); err != nil {
		t.Fatalf("Write entryB 失败: %v", err)
	}

	// 验证 1：effectiveImportance(A) = 40（降权 ×0.5）
	effA := effectiveImportance(entryA)
	if effA != 40 {
		t.Errorf("effectiveImportance(A) = %d, 期望 40 (降权 ×0.5)", effA)
	}
	effB := effectiveImportance(entryB)
	if effB != 80 {
		t.Errorf("effectiveImportance(B) = %d, 期望 80 (不降权)", effB)
	}

	// 验证 2：SearchMemory 排序后 B 在 A 之前
	ctx := context.Background()
	results, err := SearchMemory(ctx, "golang", SearchOptions{})
	if err != nil {
		t.Fatalf("SearchMemory 失败: %v", err)
	}
	if len(results) < 2 {
		t.Fatalf("SearchMemory 返回 %d 条, 期望至少 2 条", len(results))
	}

	// 找到 A 和 B 的位置
	var idxA, idxB = -1, -1
	for i, r := range results {
		if r.Name == "procedural-flow" {
			idxA = i
		}
		if r.Name == "stable-fact" {
			idxB = i
		}
	}
	if idxA < 0 || idxB < 0 {
		t.Fatalf("SearchMemory 未返回 A 或 B: idxA=%d, idxB=%d", idxA, idxB)
	}
	if idxA <= idxB {
		t.Errorf("降权反例失效: A(procedural,unverified) 应排序在 B(stable,verified) 之后, 实际 A index=%d, B index=%d", idxA, idxB)
	}

	// 验证 3：关键反例——A 的 entry.Importance 仍为 80（未被永久降权）
	// 从 results 中找 A
	var resultA *MemoryEntry
	for _, r := range results {
		if r.Name == "procedural-flow" {
			resultA = r
			break
		}
	}
	if resultA == nil {
		t.Fatal("SearchMemory 结果中未找到 procedural-flow")
	}
	if resultA.Importance != 80 {
		t.Errorf("关键反例失效: A 的 entry.Importance 被永久降权为 %d, 期望 80 (降权仅在排序时生效)", resultA.Importance)
	}

	// 从 store 重新读取 A，验证持久化数据未被修改
	readA, err := ml.userStore.Read("procedural-flow")
	if err != nil {
		t.Fatalf("Read entryA 失败: %v", err)
	}
	if readA.Importance != 80 {
		t.Errorf("关键反例失效: 持久化的 A.Importance 被修改为 %d, 期望 80", readA.Importance)
	}
}

// TestSearchMemory_top10 验证写入 15 条记忆返回 top-10。
func TestSearchMemory_top10(t *testing.T) {
	_, ml := setupTestMergeLayer(t, "/test/work/top10")

	// 写入 15 条记忆，全部含 "common-keyword"
	for i := 0; i < 15; i++ {
		entry := &MemoryEntry{
			Name:         "mem-" + nameFromIndex("m", i),
			Description:  "test memory " + nameFromIndex("m", i),
			Type:         MemoryTypeUser,
			CreatedAt:    time.Now(),
			Content:      "common-keyword content " + nameFromIndex("m", i),
			TrustLevel:   TrustStable,
			Verification: VerificationVerified,
			Importance:   50,
			Source:       SourceUser,
		}
		if err := ml.userStore.Write(entry); err != nil {
			t.Fatalf("Write 第 %d 个失败: %v", i, err)
		}
		time.Sleep(1 * time.Millisecond) // 保证 CreatedAt 不同
	}

	ctx := context.Background()
	results, err := SearchMemory(ctx, "common-keyword", SearchOptions{})
	if err != nil {
		t.Fatalf("SearchMemory 失败: %v", err)
	}

	if len(results) != SearchMemoryTopK {
		t.Errorf("SearchMemory 返回 %d 条, 期望 %d (top-10)", len(results), SearchMemoryTopK)
	}
}

// TestSearchMemory_empty_query 验证空 query 返回 error。
func TestSearchMemory_empty_query(t *testing.T) {
	_, _ = setupTestMergeLayer(t, "/test/work/empty-query")

	ctx := context.Background()
	_, err := SearchMemory(ctx, "", SearchOptions{})
	if err == nil {
		t.Fatal("SearchMemory(空 query) 应返回 error, 实际 nil")
	}
	if !strings.Contains(err.Error(), "query cannot be empty") {
		t.Errorf("error 信息不匹配: %v", err)
	}
}

// TestSearchMemory_no_match 验证无匹配返回空切片（非 error）。
func TestSearchMemory_no_match(t *testing.T) {
	_, ml := setupTestMergeLayer(t, "/test/work/no-match")

	writeEntryToStore(t, ml.userStore, "golang-pref", "golang preferences", "I prefer golang", VerificationVerified)

	ctx := context.Background()
	results, err := SearchMemory(ctx, "nonexistent-keyword", SearchOptions{})
	if err != nil {
		t.Fatalf("SearchMemory 无匹配不应返回 error: %v", err)
	}
	// 无匹配时 FTS5 返回 0 条，三因子排序后也是 0 条
	if len(results) != 0 {
		t.Errorf("SearchMemory 无匹配返回 %d 条, 期望 0", len(results))
	}
}

// TestSearchMemory_scope_filter 验证 scope 过滤器。
func TestSearchMemory_scope_filter(t *testing.T) {
	_, ml := setupTestMergeLayer(t, "/test/work/scope-filter")

	// user scope 写入
	writeEntryToStore(t, ml.userStore, "user-golang", "user golang pref", "I prefer golang", VerificationVerified)
	// project scope 写入
	writeEntryToStore(t, ml.projectStore, "project-golang", "project golang config", "use golang 1.25", VerificationVerified)

	ctx := context.Background()

	// 仅 user scope
	results, err := SearchMemory(ctx, "golang", SearchOptions{ScopeFilter: []string{"user"}})
	if err != nil {
		t.Fatalf("SearchMemory 失败: %v", err)
	}
	for _, r := range results {
		if r.Name == "project-golang" {
			t.Errorf("ScopeFilter(user) 失效: 返回了 project-golang")
		}
	}

	// 仅 project scope
	results, err = SearchMemory(ctx, "golang", SearchOptions{ScopeFilter: []string{"project"}})
	if err != nil {
		t.Fatalf("SearchMemory 失败: %v", err)
	}
	for _, r := range results {
		if r.Name == "user-golang" {
			t.Errorf("ScopeFilter(project) 失效: 返回了 user-golang")
		}
	}
}

// TestSearchMemory_type_filter 验证 type 过滤器。
func TestSearchMemory_type_filter(t *testing.T) {
	_, ml := setupTestMergeLayer(t, "/test/work/type-filter")

	// user 类型
	entry1 := &MemoryEntry{
		Name:         "user-type-golang",
		Description:  "user golang pref",
		Type:         MemoryTypeUser,
		CreatedAt:    time.Now(),
		Content:      "I prefer golang",
		Verification: VerificationVerified,
	}
	if err := ml.userStore.Write(entry1); err != nil {
		t.Fatalf("Write 失败: %v", err)
	}
	// feedback 类型
	entry2 := &MemoryEntry{
		Name:         "feedback-golang",
		Description:  "user feedback about golang",
		Type:         MemoryTypeFeedback,
		CreatedAt:    time.Now(),
		Content:      "golang feedback here",
		Verification: VerificationVerified,
	}
	if err := ml.userStore.Write(entry2); err != nil {
		t.Fatalf("Write 失败: %v", err)
	}

	ctx := context.Background()
	results, err := SearchMemory(ctx, "golang", SearchOptions{TypeFilter: []string{"user"}})
	if err != nil {
		t.Fatalf("SearchMemory 失败: %v", err)
	}
	for _, r := range results {
		if r.Type == MemoryTypeFeedback {
			t.Errorf("TypeFilter(user) 失效: 返回了 feedback 类型")
		}
	}
}

// TestEffectiveImportance 验证 effectiveImportance 函数。
func TestEffectiveImportance(t *testing.T) {
	tests := []struct {
		name     string
		entry    *MemoryEntry
		expected int
	}{
		{
			name: "unverified+procedural→降权×0.5",
			entry: &MemoryEntry{
				Importance:   80,
				Verification: VerificationUnverified,
				TrustLevel:   TrustProcedural,
			},
			expected: 40,
		},
		{
			name: "verified+procedural→不降权",
			entry: &MemoryEntry{
				Importance:   80,
				Verification: VerificationVerified,
				TrustLevel:   TrustProcedural,
			},
			expected: 80,
		},
		{
			name: "unverified+stable→不降权",
			entry: &MemoryEntry{
				Importance:   80,
				Verification: VerificationUnverified,
				TrustLevel:   TrustStable,
			},
			expected: 80,
		},
		{
			name: "verified+stable→不降权",
			entry: &MemoryEntry{
				Importance:   80,
				Verification: VerificationVerified,
				TrustLevel:   TrustStable,
			},
			expected: 80,
		},
		{
			name:     "nil entry→0",
			entry:    nil,
			expected: 0,
		},
		{
			name: "importance=0→0（即使降权也是 0）",
			entry: &MemoryEntry{
				Importance:   0,
				Verification: VerificationUnverified,
				TrustLevel:   TrustProcedural,
			},
			expected: 0,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := effectiveImportance(tt.entry)
			if got != tt.expected {
				t.Errorf("effectiveImportance() = %d, 期望 %d", got, tt.expected)
			}
		})
	}
}

// TestCalculateScore 验证 calculateScore 三因子加权。
func TestCalculateScore(t *testing.T) {
	// 高 importance + 高 recency + 高 relevance → 高分
	entry := &MemoryEntry{
		Name:           "high-score",
		Importance:     100,
		Verification:   VerificationVerified,
		TrustLevel:     TrustStable,
		LastAccessedAt: time.Now(),
		CreatedAt:      time.Now(),
	}
	highScore := calculateScore(entry, "test", 100, 1.0) // relevance=1.0（高）

	// 低 importance + 低 recency + 低 relevance → 低分
	oldTime := time.Now().Add(-30 * 24 * time.Hour) // 30 天前
	entry2 := &MemoryEntry{
		Name:           "low-score",
		Importance:     10,
		Verification:   VerificationVerified,
		TrustLevel:     TrustStable,
		LastAccessedAt: oldTime,
		CreatedAt:      oldTime,
	}
	lowScore := calculateScore(entry2, "test", 10, 0.0) // relevance=0.0（低）

	if highScore <= lowScore {
		t.Errorf("calculateScore 失效: high=%v, low=%v (期望 high > low)", highScore, lowScore)
	}

	// 验证 recency 衰减：相同 importance + relevance，30 天前的 entry 分数应低于刚访问的
	if calculateScore(entry, "test", 100, 0.5) <= calculateScore(entry2, "test", 100, 0.5) {
		t.Errorf("calculateScore recency 衰减失效: 现在=%v, 30天前=%v (期望现在 > 30天前)",
			calculateScore(entry, "test", 100, 0.5), calculateScore(entry2, "test", 100, 0.5))
	}
}

// TestSearchMemory_modernc_no_cgo 致命反例（PIT-004 防护）：
// 验证 internal/memory/search.go 编译产物无 CGO 符号。
// 用 GOOS=windows go build 验证 Windows 交叉编译通过（Windows 下 CGO 需 gcc，本机无 gcc）。
//
// 注：本测试在 WSL Linux 下跑，验证 GOOS=windows 交叉编译通过即可（即不依赖 CGO）。
// 若 search.go 用了 mattn/go-sqlite3（CGO），交叉编译会因缺 mingw 失败。
func TestSearchMemory_modernc_no_cgo(t *testing.T) {
	// 跳过短测试模式（交叉编译耗时较长）
	if testing.Short() {
		t.Skip("skipping cross-compile check in short mode")
	}

	// 本测试在 internal/memory/ 包目录下运行，go build . 编译当前包
	// 设置 GOOS=windows + CGO_ENABLED=0 验证 Windows 交叉编译通过（PIT-004 防护）
	cmd := exec.Command("go", "build", "-o", os.DevNull, ".")
	cmd.Env = append([]string{"GOOS=windows", "GOARCH=amd64", "CGO_ENABLED=0"}, os.Environ()...)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("GOOS=windows go build 失败（PIT-004 防护失效，可能引入了 CGO）: %v\n输出: %s", err, out)
	}
}
