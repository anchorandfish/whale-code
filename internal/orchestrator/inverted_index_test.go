// inverted_index_test.go — H10 Phase 4 模块 2 InvertedIndex 倒排索引反例测试（TDD 先行，Phase 4 §3.2.2）。
// 覆盖：tokenize 中英混排分词（CJK 按字切 / CamelCase 拆分 / 停用词去除）/ Search TopN / TaskTypeBoost / ConcurrentAdd / Rebuild checksum。
// 数据结构；落地：byTag/byToken/byType 三 map + RWMutex
// 零外部依赖；落地：Unicode 分类 + 驼峰拆分 + 去停用词
// SNAPSHOT: tests={Tokenize_CJK_SplitByChar, Tokenize_CamelCase_Split, Tokenize_StopWords_Removed, Search_TopN, Search_TaskTypeBoost, ConcurrentAdd_NoRace, Rebuild_ChecksumVerified, Rebuild_AtomicReplace, Search_EmptySignature_EmptyResult, Add_NilPattern_NoOp}

package orchestrator

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

// assertTokensEqual 比较实际 token 列表与期望（顺序敏感）。
func assertTokensEqual(t *testing.T, got, want []string) {
	t.Helper()
	if len(got) != len(want) {
		t.Errorf("token 数量不匹配: got %v (len=%d), want %v (len=%d)", got, len(got), want, len(want))
		return
	}
	for i := range got {
		if got[i] != want[i] {
			t.Errorf("token[%d] 不匹配: got %q, want %q", i, got[i], want[i])
		}
	}
}

// TestTokenize_CJK_SplitByChar CJK 字符按字切（"加斜杠命令" → ["加","斜","杠","命","令"]）。
func TestTokenize_CJK_SplitByChar(t *testing.T) {
	t.Parallel()
	got := tokenize("加斜杠命令")
	want := []string{"加", "斜", "杠", "命", "令"}
	assertTokensEqual(t, got, want)
}

// TestTokenize_CamelCase_Split 驼峰拆分（"AddSlashCommand" → ["add","slash","command"]）。
func TestTokenize_CamelCase_Split(t *testing.T) {
	t.Parallel()
	got := tokenize("AddSlashCommand")
	want := []string{"add", "slash", "command"}
	assertTokensEqual(t, got, want)
}

// TestTokenize_StopWords_Removed 停用词去除（"add the slash" → ["add","slash"]，the 被去）。
func TestTokenize_StopWords_Removed(t *testing.T) {
	t.Parallel()
	got := tokenize("add the slash")
	want := []string{"add", "slash"}
	assertTokensEqual(t, got, want)
}

// TestTokenize_CJKStopWord_Removed CJK 停用词去除（"的" 被去）。
func TestTokenize_CJKStopWord_Removed(t *testing.T) {
	t.Parallel()
	got := tokenize("修复的 bug")
	// "的" 是停用词应被去除，保留 "修"、"复"、"bug"
	for _, tok := range got {
		if tok == "的" {
			t.Errorf("CJK 停用词 '的' 应被去除，但仍出现在 tokens: %v", got)
		}
	}
}

// TestTokenize_EmptyString 空字符串返回 nil（反例）。
func TestTokenize_EmptyString(t *testing.T) {
	t.Parallel()
	got := tokenize("")
	if got != nil {
		t.Errorf("空字符串应返回 nil, got %v", got)
	}
}

// TestTokenize_MixedCNEn 中英混排（"修复 fix bug" → ["修","复","fix","bug"]）。
func TestTokenize_MixedCNEn(t *testing.T) {
	t.Parallel()
	got := tokenize("修复 fix bug")
	want := []string{"修", "复", "fix", "bug"}
	assertTokensEqual(t, got, want)
}

// TestTokenize_ABCToken 驼峰连续大写后接小写（"ABCToken" → ["abc","token"]）。
func TestTokenize_ABCToken(t *testing.T) {
	t.Parallel()
	got := tokenize("ABCToken")
	want := []string{"abc", "token"}
	assertTokensEqual(t, got, want)
}

// TestInvertedIndex_Search_TopN 20 条 pattern，Search 返回 top-5。
func TestInvertedIndex_Search_TopN(t *testing.T) {
	t.Parallel()
	idx := NewInvertedIndex()

	// 添加 20 条 pattern，task_signature 都含 "common" 关键词（确保都被命中）
	for i := 0; i < 20; i++ {
		p := makeTestPattern(
			"p-topn-"+string(rune('a'+i)),
			"common task signature "+string(rune('a'+i)),
			ScopeGlobal,
			VerifiedStateUnverified,
		)
		idx.Add(p)
	}

	// Search 返回 top-5
	results := idx.Search("common task signature", "", nil, 5)
	if len(results) > 5 {
		t.Errorf("Search 应返回最多 top-5, got %d", len(results))
	}
	if len(results) == 0 {
		t.Fatal("Search 应至少返回 1 条命中，got 0")
	}
}

// TestInvertedIndex_Search_TaskTypeBoost task_type 命中加分（×3）影响排序。
// 构造：2 条 pattern 命中相同 token，但只有 1 条 task_type 匹配 → task_type 匹配的应排第一。
func TestInvertedIndex_Search_TaskTypeBoost(t *testing.T) {
	t.Parallel()
	idx := NewInvertedIndex()

	// p1: task_signature 含 "auth"，task_type="bug-fix"（被 extractTaskType 提取）
	p1 := makeTestPattern("p-boost-1", "fix auth bug", ScopeGlobal, VerifiedStateUnverified)
	p1.FilesTouched = []string{"auth.go"} // "auth" 也会作为 tag
	idx.Add(p1)

	// p2: task_signature 含 "auth"，但 task_type 不匹配（不会加 ×3 分）
	p2 := makeTestPattern("p-boost-2", "auth module update", ScopeGlobal, VerifiedStateUnverified)
	idx.Add(p2)

	// Search 时 taskType="bug-fix"，p1 应因 task_type 命中加 3 分而排第一
	results := idx.Search("auth", TaskTypeBugFix, nil, 5)
	if len(results) < 2 {
		t.Fatalf("应至少返回 2 条命中, got %d", len(results))
	}
	if results[0] != "p-boost-1" {
		t.Errorf("task_type 命中加 ×3 分应使 p-boost-1 排第一, got first=%s", results[0])
	}
}

// TestInvertedIndex_ConcurrentAdd_NoRace 多 goroutine 同时 Add，-race 无竞态（并发安全）。
func TestInvertedIndex_ConcurrentAdd_NoRace(t *testing.T) {
	t.Parallel()
	idx := NewInvertedIndex()

	var wg sync.WaitGroup
	wg.Add(10)
	for i := 0; i < 10; i++ {
		go func(n int) {
			defer wg.Done()
			p := makeTestPattern(
				"p-concurrent-"+string(rune('a'+n)),
				"concurrent add test "+string(rune('a'+n)),
				ScopeGlobal,
				VerifiedStateUnverified,
			)
			idx.Add(p)
		}(i)
	}
	wg.Wait()

	// 验证索引中有 10 个 pattern
	if idx.Size() != 10 {
		t.Errorf("并发 Add 后索引应有 10 个 pattern, got %d", idx.Size())
	}
}

// TestInvertedIndex_Rebuild_ChecksumVerified Rebuild 后 _index.checksum 文件存在且含 sha256。
func TestInvertedIndex_Rebuild_ChecksumVerified(t *testing.T) {
	t.Parallel()
	tmpDir := t.TempDir()
	patternsDir := filepath.Join(tmpDir, "patterns")
	if err := os.MkdirAll(patternsDir, 0o755); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}

	// 写 patterns.jsonl（2 条 pattern）
	p1 := makeTestPattern("p-rebuild-1", "rebuild test one", ScopeGlobal, VerifiedStateUnverified)
	p2 := makeTestPattern("p-rebuild-2", "rebuild test two", ScopeGlobal, VerifiedStateUnverified)
	jsonlPath := filepath.Join(patternsDir, patternsFileName)
	f, err := os.OpenFile(jsonlPath, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		t.Fatalf("OpenFile: %v", err)
	}
	for _, p := range []*Pattern{p1, p2} {
		data, _ := json.Marshal(p)
		data = append(data, '\n')
		if _, err := f.Write(data); err != nil {
			t.Fatalf("Write: %v", err)
		}
	}
	f.Close()

	// Rebuild
	idx := NewInvertedIndex()
	if err := idx.Rebuild(patternsDir); err != nil {
		t.Fatalf("Rebuild: %v", err)
	}

	// 验证 _index.json 存在
	indexPath := filepath.Join(patternsDir, indexFileName)
	if _, err := os.Stat(indexPath); err != nil {
		t.Errorf("Rebuild 后 _index.json 应存在: %v", err)
	}

	// 验证 _index.checksum 存在且含 sha256
	checksumPath := filepath.Join(patternsDir, "_index.checksum")
	checksumData, err := os.ReadFile(checksumPath)
	if err != nil {
		t.Fatalf("Rebuild 后 _index.checksum 应存在: %v", err)
	}
	if !strings.Contains(string(checksumData), "sha256:") {
		t.Errorf("_index.checksum 应含 'sha256:' 前缀, got: %s", string(checksumData))
	}
}

// TestInvertedIndex_Rebuild_AtomicReplace 重建过程用临时文件 + rename（原子替换）。
// 验证：Rebuild 后 _index.json.tmp 不残留（原子替换成功）。
func TestInvertedIndex_Rebuild_AtomicReplace(t *testing.T) {
	t.Parallel()
	tmpDir := t.TempDir()
	patternsDir := filepath.Join(tmpDir, "patterns")
	if err := os.MkdirAll(patternsDir, 0o755); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}

	// 写 patterns.jsonl
	p := makeTestPattern("p-atomic-1", "atomic replace test", ScopeGlobal, VerifiedStateUnverified)
	jsonlPath := filepath.Join(patternsDir, patternsFileName)
	data, _ := json.Marshal(p)
	data = append(data, '\n')
	if err := os.WriteFile(jsonlPath, data, 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	idx := NewInvertedIndex()
	if err := idx.Rebuild(patternsDir); err != nil {
		t.Fatalf("Rebuild: %v", err)
	}

	// 验证 _index.json.tmp 不残留（原子替换成功）
	tmpPath := filepath.Join(patternsDir, indexFileName+".tmp")
	if _, err := os.Stat(tmpPath); !os.IsNotExist(err) {
		t.Errorf("Rebuild 后 _index.json.tmp 不应残留（原子替换应成功）, err=%v", err)
	}

	// 验证 _index.json 内容正确（含 pattern_id）
	idxData, err := os.ReadFile(filepath.Join(patternsDir, indexFileName))
	if err != nil {
		t.Fatalf("ReadFile _index.json: %v", err)
	}
	var idxFile patternIndex
	if err := json.Unmarshal(idxData, &idxFile); err != nil {
		t.Fatalf("Unmarshal _index.json: %v", err)
	}
	if _, ok := idxFile.Entries["p-atomic-1"]; !ok {
		t.Errorf("_index.json 应含 pattern_id 'p-atomic-1', got entries: %v", idxFile.Entries)
	}
}

// TestInvertedIndex_Search_EmptySignature_SearchAll 空签名 + 非空 taskType 仍可命中（反例边界）。
func TestInvertedIndex_Search_EmptySignature_SearchAll(t *testing.T) {
	t.Parallel()
	idx := NewInvertedIndex()
	p := makeTestPattern("p-empty-sig", "bug-fix in auth module", ScopeGlobal, VerifiedStateUnverified)
	idx.Add(p)

	// 空 signature 但 taskType 命中（×3 加分）
	results := idx.Search("", TaskTypeBugFix, nil, 5)
	if len(results) == 0 {
		t.Error("空 signature + taskType 命中应返回结果")
	}
}

// TestInvertedIndex_Add_NilPattern_NoOp Add(nil) 不 panic 不报错（反例边界）。
func TestInvertedIndex_Add_NilPattern_NoOp(t *testing.T) {
	t.Parallel()
	idx := NewInvertedIndex()
	// 不应 panic
	idx.Add(nil)
	idx.Add(&Pattern{PatternID: ""}) // 空 PatternID 也应跳过
	if idx.Size() != 0 {
		t.Errorf("Add(nil) 和 Add(emptyID) 后索引应仍为空, got %d", idx.Size())
	}
}

// TestInvertedIndex_Search_TopNZero_DefaultApplied topN=0 时使用默认值 20。
func TestInvertedIndex_Search_TopNZero_DefaultApplied(t *testing.T) {
	t.Parallel()
	idx := NewInvertedIndex()
	// 添加 25 条 pattern
	for i := 0; i < 25; i++ {
		p := makeTestPattern(
			"p-default-topn-"+string(rune('a'+(i%26))),
			"common keyword "+string(rune('a'+(i%26))),
			ScopeGlobal,
			VerifiedStateUnverified,
		)
		idx.Add(p)
	}

	// topN=0 应使用默认值 20
	results := idx.Search("common keyword", "", nil, 0)
	if len(results) > 20 {
		t.Errorf("topN=0 应使用默认值 20, got %d", len(results))
	}
}

// TestInvertedIndex_LoadFromIndexFile 从 _index.json 加载到倒排索引。
func TestInvertedIndex_LoadFromIndexFile(t *testing.T) {
	t.Parallel()
	tmpDir := t.TempDir()

	// 构造 _index.json
	idxFile := patternIndex{
		Entries: map[string]patternIndexEntry{
			"p-load-1": {
				Scope:         ScopeGlobal,
				TaskSignature: "load test signature",
				TaskType:      TaskTypeBugFix,
				Tags:          []string{".go", "load"},
			},
		},
	}
	data, _ := json.MarshalIndent(idxFile, "", "  ")
	indexPath := filepath.Join(tmpDir, indexFileName)
	if err := os.WriteFile(indexPath, data, 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	idx := NewInvertedIndex()
	if err := idx.LoadFromIndexFile(indexPath); err != nil {
		t.Fatalf("LoadFromIndexFile: %v", err)
	}

	// 验证加载后 Search 能命中
	results := idx.Search("load test signature", TaskTypeBugFix, []string{".go"}, 5)
	found := false
	for _, id := range results {
		if id == "p-load-1" {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("LoadFromIndexFile 后 Search 应命中 p-load-1, got %v", results)
	}
}

// TestInvertedIndex_LoadFromIndexFile_NotExist_NoError 文件不存在视为空索引，不报错。
func TestInvertedIndex_LoadFromIndexFile_NotExist_NoError(t *testing.T) {
	t.Parallel()
	idx := NewInvertedIndex()
	err := idx.LoadFromIndexFile(filepath.Join(t.TempDir(), "nonexistent.json"))
	if err != nil {
		t.Errorf("文件不存在应返回 nil（空索引），got: %v", err)
	}
}
