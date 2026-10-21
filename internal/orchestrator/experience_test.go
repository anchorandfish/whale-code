// experience_test.go — H10 Phase 4 模块 1 ExperienceStore 反例测试（TDD 先行，Phase 4 §3.1）。
// 覆盖致命反例 4：并发 Save 不损坏 jsonl（sync.RWMutex 防竞态，-race 无报告）。
// 覆盖：缓存命中/未命中 / 作用域合并 project 优先 / 索引同步 / Windows 长路径 / 内存字段不落盘。
// 核心；落地：Save/Get/Match/ListByScope
// 长路径；落地：windowsLongPathHandler \\?\ 前缀
// SNAPSHOT: tests={ConcurrentSave_NoCorruption(致命反例4), Get_CacheHit, Get_CacheMiss_ReadFromJsonl, ScopeMerge_ProjectPriority, Save_IndexSynced, WindowsLongPath_PrefixApplied, NoMemoryFields_Persisted, Save_NilPattern_Rejected, Save_InvalidScope_Rejected}

package orchestrator

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

// newTestExperienceStore 创建测试用 ExperienceStore（temp dir 双 scope）。
func newTestExperienceStore(t *testing.T) *ExperienceStore {
	t.Helper()
	globalPath := filepath.Join(t.TempDir(), "global")
	projectPath := filepath.Join(t.TempDir(), "project")
	s, err := NewExperienceStore(globalPath, projectPath)
	if err != nil {
		t.Fatalf("NewExperienceStore: %v", err)
	}
	return s
}

// makeTestPattern 构造测试用 Pattern（可指定 id/signature/scope/state）。
func makeTestPattern(id, signature, scope, state string) *Pattern {
	return &Pattern{
		PatternID:      id,
		TaskSignature:  signature,
		Steps:          []Step{{Action: "edit_file", Target: "main.go", Description: "测试步骤"}},
		FilesTouched:   []string{"main.go"},
		VerifiedModels: []string{ModelSonnet},
		VerifiedAt:     time.Now().UTC().Format(time.RFC3339),
		L2AdoptionRate: 0.8,
		VerifiedState:  state,
		Scope:          scope,
		Version:        1,
		LastUsedAt:     time.Now().UTC().Format(time.RFC3339),
	}
}

// TestExperienceStore_ConcurrentSave_NoCorruption 致命反例 4：3 goroutine 同时 Save 不同 pattern，-race 无竞态，jsonl 不损坏（零容忍）。
func TestExperienceStore_ConcurrentSave_NoCorruption(t *testing.T) {
	t.Parallel()
	s := newTestExperienceStore(t)
	patterns := []*Pattern{
		makeTestPattern("p-concurrent-1", "fix bug in auth module", ScopeGlobal, VerifiedStateUnverified),
		makeTestPattern("p-concurrent-2", "add slash command feature", ScopeGlobal, VerifiedStateUnverified),
		makeTestPattern("p-concurrent-3", "update doc generation flow", ScopeGlobal, VerifiedStateUnverified),
	}

	var wg sync.WaitGroup
	errs := make([]error, len(patterns))
	wg.Add(len(patterns))
	for i, p := range patterns {
		go func(idx int, pat *Pattern) {
			defer wg.Done()
			errs[idx] = s.Save(pat)
		}(i, p)
	}
	wg.Wait()

	for i, err := range errs {
		if err != nil {
			t.Fatalf("goroutine %d Save failed: %v", i, err)
		}
	}

	// 验证 jsonl 不损坏：每条都能 Get 到
	s.ClearCache()
	for _, p := range patterns {
		got, err := s.Get(p.PatternID)
		if err != nil {
			t.Fatalf("Get %s after concurrent Save: %v", p.PatternID, err)
		}
		if got.PatternID != p.PatternID {
			t.Errorf("pattern_id mismatch: got %s, want %s", got.PatternID, p.PatternID)
		}
	}

	// 验证 jsonl 文件每行都是合法 JSON（无损坏）
	data, err := os.ReadFile(s.PatternsFilePath(ScopeGlobal))
	if err != nil {
		t.Fatalf("read patterns.jsonl: %v", err)
	}
	lines := splitJSONLines(data)
	if len(lines) != len(patterns) {
		t.Fatalf("expected %d lines in jsonl, got %d (corrupted)", len(patterns), len(lines))
	}
	for i, line := range lines {
		var p Pattern
		if err := json.Unmarshal(line, &p); err != nil {
			t.Fatalf("line %d unmarshal failed (jsonl corrupted): %v", i, err)
		}
	}
}

// TestExperienceStore_Get_CacheHit Save 后 Get 命中缓存（不读磁盘）。
func TestExperienceStore_Get_CacheHit(t *testing.T) {
	t.Parallel()
	s := newTestExperienceStore(t)
	p := makeTestPattern("p-cache-hit", "fix cache hit bug", ScopeGlobal, VerifiedStateUnverified)
	if err := s.Save(p); err != nil {
		t.Fatalf("Save: %v", err)
	}

	// Save 后应命中缓存（CacheSize >= 1）
	if s.CacheSize() == 0 {
		t.Fatal("Save 后缓存应为非空（CacheHit 前提）")
	}

	got, err := s.Get(p.PatternID)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got.PatternID != p.PatternID {
		t.Errorf("Get 命中缓存返回错误 pattern: got %s, want %s", got.PatternID, p.PatternID)
	}
}

// TestExperienceStore_Get_CacheMiss_ReadFromJsonl 清空缓存后 Get 从 jsonl 读取。
func TestExperienceStore_Get_CacheMiss_ReadFromJsonl(t *testing.T) {
	t.Parallel()
	s := newTestExperienceStore(t)
	p := makeTestPattern("p-cache-miss", "fix cache miss bug", ScopeGlobal, VerifiedStateUnverified)
	if err := s.Save(p); err != nil {
		t.Fatalf("Save: %v", err)
	}

	// 清空缓存，强制从 jsonl 读
	s.ClearCache()
	if s.CacheSize() != 0 {
		t.Fatalf("ClearCache 后缓存应为空, got %d", s.CacheSize())
	}

	got, err := s.Get(p.PatternID)
	if err != nil {
		t.Fatalf("Get after ClearCache: %v", err)
	}
	if got.PatternID != p.PatternID {
		t.Errorf("从 jsonl 读取 pattern_id 不匹配: got %s, want %s", got.PatternID, p.PatternID)
	}
	// Get 后应回填缓存
	if s.CacheSize() == 0 {
		t.Error("Get 后缓存应回填")
	}
}

// TestExperienceStore_ScopeMerge_ProjectPriority 同 task_signature 在 global 和 project 各一条，Match 返回 project 的。
func TestExperienceStore_ScopeMerge_ProjectPriority(t *testing.T) {
	t.Parallel()
	s := newTestExperienceStore(t)

	globalP := makeTestPattern("p-merge-global", "fix scope merge bug", ScopeGlobal, VerifiedStateVerified)
	projectP := makeTestPattern("p-merge-project", "fix scope merge bug", ScopeProject, VerifiedStateVerified)

	if err := s.Save(globalP); err != nil {
		t.Fatalf("Save global: %v", err)
	}
	if err := s.Save(projectP); err != nil {
		t.Fatalf("Save project: %v", err)
	}

	hits, err := s.Match("fix scope merge bug", TaskTypeBugFix, nil)
	if err != nil {
		t.Fatalf("Match: %v", err)
	}
	if len(hits) == 0 {
		t.Fatal("expected at least 1 hit, got 0")
	}
	if hits[0].PatternID != "p-merge-project" {
		t.Errorf("ScopeMerge 应返回 project pattern, got %s (scope=%s)", hits[0].PatternID, hits[0].Scope)
	}
}

// TestExperienceStore_Save_IndexSynced Save 后 _index.json 含新 pattern_id 条目。
func TestExperienceStore_Save_IndexSynced(t *testing.T) {
	t.Parallel()
	s := newTestExperienceStore(t)
	p := makeTestPattern("p-index-sync", "fix index sync bug", ScopeGlobal, VerifiedStateUnverified)
	if err := s.Save(p); err != nil {
		t.Fatalf("Save: %v", err)
	}

	// 直接读 _index.json 验证含 pattern_id 条目
	entry, err := s.lookupPatternIndex(ScopeGlobal, "p-index-sync")
	if err != nil {
		t.Fatalf("lookupPatternIndex: %v", err)
	}
	if entry.Scope != ScopeGlobal {
		t.Errorf("index entry scope mismatch: got %s, want %s", entry.Scope, ScopeGlobal)
	}
	if entry.TaskSignature != p.TaskSignature {
		t.Errorf("index entry signature mismatch: got %s, want %s", entry.TaskSignature, p.TaskSignature)
	}
}

// TestExperienceStore_WindowsLongPath_PrefixApplied 路径 > 240 加 \\?\ 前缀。
// 直接测试 windowsLongPathHandler（ExperienceStore.Save 内部调用此函数）。
func TestExperienceStore_WindowsLongPath_PrefixApplied(t *testing.T) {
	// 不使用 t.Parallel()：SetWindowsLongPathMode 修改全局变量 isWindowsForLongPath，并行会 data race
	restore := SetWindowsLongPathMode(true)
	defer restore()

	// 构造 > 240 字符的路径
	longPath := "/" + strings.Repeat("a", 250) + "/patterns/patterns.jsonl"
	result, err := windowsLongPathHandler(longPath, "test-pattern-id-1234")
	if err != nil {
		t.Fatalf("windowsLongPathHandler: %v", err)
	}
	if !strings.HasPrefix(result, `\\?\`) {
		t.Errorf("：路径 > 240 应加 \\\\?\\ 前缀, got %s", result)
	}
}

// TestExperienceStore_WindowsLongPath_ShortPath_NoPrefix 路径 <= 240 不加前缀（反例）。
func TestExperienceStore_WindowsLongPath_ShortPath_NoPrefix(t *testing.T) {
	// 不使用 t.Parallel()：SetWindowsLongPathMode 修改全局变量 isWindowsForLongPath，并行会 data race
	restore := SetWindowsLongPathMode(true)
	defer restore()

	shortPath := "/tmp/short/patterns.jsonl"
	result, err := windowsLongPathHandler(shortPath, "test-pattern")
	if err != nil {
		t.Fatalf("windowsLongPathHandler: %v", err)
	}
	if strings.HasPrefix(result, `\\?\`) {
		t.Errorf("：路径 <= 240 不应加 \\\\?\\ 前缀, got %s", result)
	}
}

// TestExperienceStore_NoMemoryFields_Persisted provisionalAdoptions 等内存字段不落 jsonl（仅运行时）。
func TestExperienceStore_NoMemoryFields_Persisted(t *testing.T) {
	t.Parallel()
	s := newTestExperienceStore(t)
	p := makeTestPattern("p-no-mem", "fix no memory fields bug", ScopeGlobal, VerifiedStateUnverified)
	if err := s.Save(p); err != nil {
		t.Fatalf("Save: %v", err)
	}

	data, err := os.ReadFile(s.PatternsFilePath(ScopeGlobal))
	if err != nil {
		t.Fatalf("read patterns.jsonl: %v", err)
	}
	jsonStr := string(data)
	if strings.Contains(jsonStr, "provisional_adoptions") {
		t.Errorf("provisionalAdoptions 不应持久化到 jsonl（仅运行时内存字段）")
	}
	if strings.Contains(jsonStr, "cache_order") {
		t.Errorf("cacheOrder 不应持久化到 jsonl（仅运行时内存字段）")
	}
	if strings.Contains(jsonStr, "cache") {
		t.Errorf("cache 不应持久化到 jsonl（仅运行时内存字段）")
	}
}

// TestExperienceStore_Save_NilPattern_Rejected 反例：Save(nil) 返回 error。
func TestExperienceStore_Save_NilPattern_Rejected(t *testing.T) {
	t.Parallel()
	s := newTestExperienceStore(t)
	if err := s.Save(nil); err == nil {
		t.Error("Save(nil) 必须返回 error")
	}
}

// TestExperienceStore_Save_InvalidScope_Rejected 反例：Scope 非法返回 error。
func TestExperienceStore_Save_InvalidScope_Rejected(t *testing.T) {
	t.Parallel()
	s := newTestExperienceStore(t)
	p := makeTestPattern("p-bad-scope", "fix bad scope", "invalid-scope", VerifiedStateUnverified)
	if err := s.Save(p); err == nil {
		t.Error("Save with invalid scope 必须返回 error")
	}
}

// TestExperienceStore_Save_EmptyPatternID_Rejected 反例：PatternID 空返回 error。
func TestExperienceStore_Save_EmptyPatternID_Rejected(t *testing.T) {
	t.Parallel()
	s := newTestExperienceStore(t)
	p := makeTestPattern("", "fix empty id", ScopeGlobal, VerifiedStateUnverified)
	if err := s.Save(p); err == nil {
		t.Error("Save with empty PatternID 必须返回 error")
	}
}

// TestExperienceStore_Get_NotFound 反例：Get 不存在的 pattern_id 返回 error。
func TestExperienceStore_Get_NotFound(t *testing.T) {
	t.Parallel()
	s := newTestExperienceStore(t)
	_, err := s.Get("nonexistent-pattern-id")
	if err == nil {
		t.Error("Get 不存在的 pattern_id 必须返回 error")
	}
}

// TestExperienceStore_SavePitfall_NewPitfall 保存新 pitfall 并验证可读回。
func TestExperienceStore_SavePitfall_NewPitfall(t *testing.T) {
	t.Parallel()
	s := newTestExperienceStore(t)
	pitfall := &Pitfall{
		PitfallID:     "pitfall-test-1",
		TaskSignature: "fix pitfall save bug",
		Symptom:       "compile error",
		RootCause:     "missing import",
		Mitigation:    "add import",
		Occurrences:   1,
		Scope:         ScopeProject,
	}
	if err := s.SavePitfall(pitfall); err != nil {
		t.Fatalf("SavePitfall: %v", err)
	}

	// 通过 ListPitfallsByScope 读回
	pitfalls, err := s.ListPitfallsByScope(ScopeProject)
	if err != nil {
		t.Fatalf("ListPitfallsByScope: %v", err)
	}
	if len(pitfalls) != 1 {
		t.Fatalf("expected 1 pitfall, got %d", len(pitfalls))
	}
	if pitfalls[0].PitfallID != "pitfall-test-1" {
		t.Errorf("pitfall_id mismatch: got %s", pitfalls[0].PitfallID)
	}
}

// TestExperienceStore_ListByScope 返回指定 scope 的所有 pattern。
func TestExperienceStore_ListByScope(t *testing.T) {
	t.Parallel()
	s := newTestExperienceStore(t)
	p1 := makeTestPattern("p-list-1", "fix list bug 1", ScopeGlobal, VerifiedStateUnverified)
	p2 := makeTestPattern("p-list-2", "fix list bug 2", ScopeGlobal, VerifiedStateUnverified)
	p3 := makeTestPattern("p-list-3", "fix list bug 3", ScopeProject, VerifiedStateUnverified)

	for _, p := range []*Pattern{p1, p2, p3} {
		if err := s.Save(p); err != nil {
			t.Fatalf("Save: %v", err)
		}
	}

	globalPatterns, err := s.ListByScope(ScopeGlobal)
	if err != nil {
		t.Fatalf("ListByScope global: %v", err)
	}
	if len(globalPatterns) != 2 {
		t.Errorf("expected 2 global patterns, got %d", len(globalPatterns))
	}

	projectPatterns, err := s.ListByScope(ScopeProject)
	if err != nil {
		t.Fatalf("ListByScope project: %v", err)
	}
	if len(projectPatterns) != 1 {
		t.Errorf("expected 1 project pattern, got %d", len(projectPatterns))
	}
}
