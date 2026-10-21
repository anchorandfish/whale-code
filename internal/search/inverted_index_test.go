// Package search — M23-03 倒排索引测试（SNAPSHOT）
//
// 测试覆盖：
// - AddMessage / Search（单关键词、多关键词 AND）
// - RemoveSession
// - SessionLastUpdated / Size
// - IndexManager 增量更新
// - PersistToFile / LoadFromFile 持久化往返
// - 分词（中文 / 英文 / 混合）
package search

import (
	"path/filepath"
	"testing"
)

// TestTokenize_SplitsByNonAlnum 验证分词按非字母数字切分
func TestTokenize_SplitsByNonAlnum(t *testing.T) {
	tokens := tokenize("hello, world! foo_bar")
	// foo_bar 中下划线非 token 字符，应被切分为 foo / bar
	want := []string{"hello", "world", "foo", "bar"}
	if len(tokens) != len(want) {
		t.Fatalf("tokens = %v, want %v", tokens, want)
	}
	for i, tok := range tokens {
		if tok != want[i] {
			t.Errorf("tokens[%d] = %q, want %q", i, tok, want[i])
		}
	}
}

// TestTokenize_Lowercase 验证分词小写化
func TestTokenize_Lowercase(t *testing.T) {
	tokens := tokenize("Hello WORLD")
	for _, tok := range tokens {
		if tok != "hello" && tok != "world" {
			t.Errorf("unexpected token: %q", tok)
		}
	}
}

// TestTokenize_Chinese 验证中文连续序列保留
func TestTokenize_Chinese(t *testing.T) {
	tokens := tokenize("你好 world 世界")
	want := []string{"你好", "world", "世界"}
	if len(tokens) != len(want) {
		t.Fatalf("tokens = %v, want %v", tokens, want)
	}
	for i, tok := range tokens {
		if tok != want[i] {
			t.Errorf("tokens[%d] = %q, want %q", i, tok, want[i])
		}
	}
}

// TestMemoryIndex_AddAndSearchSingleKeyword 单关键词搜索
func TestMemoryIndex_AddAndSearchSingleKeyword(t *testing.T) {
	idx := NewMemoryIndex()
	idx.AddMessage("sess1", 0, 1000, "hello world")
	idx.AddMessage("sess2", 0, 2000, "hello foo")

	postings := idx.Search([]string{"hello"})
	if len(postings) != 2 {
		t.Fatalf("len(postings) = %d, want 2", len(postings))
	}
	// 按 Timestamp 降序，sess2 应在前
	if postings[0].SessionID != "sess2" {
		t.Errorf("postings[0].SessionID = %q, want sess2", postings[0].SessionID)
	}
	if postings[1].SessionID != "sess1" {
		t.Errorf("postings[1].SessionID = %q, want sess1", postings[1].SessionID)
	}
}

// TestMemoryIndex_SearchMultipleKeywordsAND 多关键词 AND 语义
func TestMemoryIndex_SearchMultipleKeywordsAND(t *testing.T) {
	idx := NewMemoryIndex()
	idx.AddMessage("sess1", 0, 1000, "hello world")
	idx.AddMessage("sess2", 0, 2000, "hello foo")
	idx.AddMessage("sess3", 0, 3000, "world foo bar")

	// hello + world 只有 sess1 同时包含
	postings := idx.Search([]string{"hello", "world"})
	if len(postings) != 1 {
		t.Fatalf("len(postings) = %d, want 1", len(postings))
	}
	if postings[0].SessionID != "sess1" {
		t.Errorf("postings[0].SessionID = %q, want sess1", postings[0].SessionID)
	}
}

// TestMemoryIndex_SearchMissingKeyword 缺失关键词返回 nil
func TestMemoryIndex_SearchMissingKeyword(t *testing.T) {
	idx := NewMemoryIndex()
	idx.AddMessage("sess1", 0, 1000, "hello world")

	postings := idx.Search([]string{"missing"})
	if postings != nil {
		t.Errorf("postings = %v, want nil", postings)
	}
}

// TestMemoryIndex_SearchEmptyKeywords 空关键词返回 nil
func TestMemoryIndex_SearchEmptyKeywords(t *testing.T) {
	idx := NewMemoryIndex()
	idx.AddMessage("sess1", 0, 1000, "hello")

	postings := idx.Search([]string{})
	if postings != nil {
		t.Errorf("postings = %v, want nil", postings)
	}
}

// TestMemoryIndex_RemoveSession 移除会话后搜索不到
func TestMemoryIndex_RemoveSession(t *testing.T) {
	idx := NewMemoryIndex()
	idx.AddMessage("sess1", 0, 1000, "hello")
	idx.AddMessage("sess2", 0, 2000, "hello")

	idx.RemoveSession("sess1")

	postings := idx.Search([]string{"hello"})
	if len(postings) != 1 {
		t.Fatalf("len(postings) = %d, want 1", len(postings))
	}
	if postings[0].SessionID != "sess2" {
		t.Errorf("postings[0].SessionID = %q, want sess2", postings[0].SessionID)
	}
}

// TestMemoryIndex_SessionLastUpdated 最后更新时间
func TestMemoryIndex_SessionLastUpdated(t *testing.T) {
	idx := NewMemoryIndex()
	idx.AddMessage("sess1", 0, 1000, "first")
	idx.AddMessage("sess1", 1, 2000, "second")

	last := idx.SessionLastUpdated("sess1")
	if last != 2000 {
		t.Errorf("SessionLastUpdated = %d, want 2000", last)
	}
}

// TestMemoryIndex_Size 索引大小
func TestMemoryIndex_Size(t *testing.T) {
	idx := NewMemoryIndex()
	idx.AddMessage("sess1", 0, 1000, "hello world foo")

	// 三个不同 token
	if size := idx.Size(); size != 3 {
		t.Errorf("Size = %d, want 3", size)
	}
}

// TestIndexManager_IncrementalUpdate 增量更新：旧时间戳不重建
func TestIndexManager_IncrementalUpdate(t *testing.T) {
	mgr := NewIndexManager()

	msgs1 := []indexedMessage{
		{Index: 0, Text: "hello", Timestamp: 1000},
	}
	mgr.IndexSession("sess1", msgs1)
	size1 := mgr.IndexSize()

	// 同样时间戳再次索引：应跳过
	mgr.IndexSession("sess1", msgs1)
	size2 := mgr.IndexSize()

	if size1 != size2 {
		t.Errorf("incremental update failed: size1=%d size2=%d", size1, size2)
	}

	// 更新时间戳：应重建
	msgs2 := []indexedMessage{
		{Index: 0, Text: "hello world", Timestamp: 2000},
	}
	mgr.IndexSession("sess1", msgs2)
	size3 := mgr.IndexSize()
	if size3 != 2 {
		t.Errorf("after update Size = %d, want 2", size3)
	}
}

// TestIndexManager_SearchByIndex 通过索引管理器搜索
func TestIndexManager_SearchByIndex(t *testing.T) {
	mgr := NewIndexManager()
	mgr.IndexSession("sess1", []indexedMessage{
		{Index: 0, Text: "hello world", Timestamp: 1000},
	})

	postings := mgr.SearchByIndex([]string{"hello"})
	if len(postings) != 1 {
		t.Fatalf("len(postings) = %d, want 1", len(postings))
	}
	if postings[0].SessionID != "sess1" {
		t.Errorf("postings[0].SessionID = %q, want sess1", postings[0].SessionID)
	}
}

// TestIndexManager_PersistAndLoad 持久化往返测试
func TestIndexManager_PersistAndLoad(t *testing.T) {
	mgr1 := NewIndexManager()
	mgr1.IndexSession("sess1", []indexedMessage{
		{Index: 0, Text: "hello world", Timestamp: 1000},
	})
	mgr1.IndexSession("sess2", []indexedMessage{
		{Index: 0, Text: "foo bar", Timestamp: 2000},
	})

	path := filepath.Join(t.TempDir(), "index.json")
	if err := mgr1.PersistToFile(path); err != nil {
		t.Fatalf("PersistToFile err: %v", err)
	}

	mgr2 := NewIndexManager()
	if err := mgr2.LoadFromFile(path); err != nil {
		t.Fatalf("LoadFromFile err: %v", err)
	}

	// 验证加载后能搜索到
	postings := mgr2.SearchByIndex([]string{"hello"})
	if len(postings) != 1 {
		t.Fatalf("after load len(postings) = %d, want 1", len(postings))
	}
	if postings[0].SessionID != "sess1" {
		t.Errorf("postings[0].SessionID = %q, want sess1", postings[0].SessionID)
	}

	// 验证索引大小一致
	if mgr2.IndexSize() != mgr1.IndexSize() {
		t.Errorf("size mismatch: mgr1=%d mgr2=%d", mgr1.IndexSize(), mgr2.IndexSize())
	}
}

// TestIndexManager_LoadFromFileNotExist 文件不存在时返回 nil（首次启动）
func TestIndexManager_LoadFromFileNotExist(t *testing.T) {
	mgr := NewIndexManager()
	err := mgr.LoadFromFile(filepath.Join(t.TempDir(), "missing.json"))
	if err != nil {
		t.Errorf("LoadFromFile missing file err: %v, want nil", err)
	}
}

// TestMemoryIndex_AddMessage_TokenLimit 单条消息 token 数超限截断
func TestMemoryIndex_AddMessage_TokenLimit(t *testing.T) {
	idx := NewMemoryIndex()
	// 构造超过 maxIndexTokensPerMessage 的 token
	text := ""
	for i := 0; i < maxIndexTokensPerMessage+100; i++ {
		text += "word "
	}
	idx.AddMessage("sess1", 0, 1000, text)
	// 不验证具体大小，只验证不崩溃且 Size > 0
	if idx.Size() == 0 {
		t.Error("Size = 0, want > 0")
	}
}
