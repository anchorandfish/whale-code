// Package search 测试：SearchIndex 死代码删除后的反向回归
// 验证删除 SearchIndex/SessionIndexEntry/IndexFilePath/SaveIndex/LoadIndex 后：
// 1. 不再生成 ~/.whale/search_index.json 索引文件
// 2. Search() 行为不变（仍走全量扫描）
// 3. NewSearcher() 无副作用
// 4. SearchInSession 不依赖任何索引
package search

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/anchorandfish/whale-code/internal/session"
)

// TestDeadCodeRemoval_NoIndexFileCreated 验证删除 SaveIndex 后，
// 执行 Search 不会在用户家目录生成 search_index.json 文件。
// 这是死代码删除的关键反向断言：确认无任何残留的索引写入路径。
func TestDeadCodeRemoval_NoIndexFileCreated(t *testing.T) {
	// 构造测试 store（使用 t.TempDir 隔离，不污染真实家目录）
	dir := t.TempDir()
	store, err := session.NewStore(dir)
	if err != nil {
		t.Fatalf("session.NewStore failed: %v", err)
	}

	// 写入一个 session 供搜索
	if err := store.Append("test-session", session.UserText("hello keyword")); err != nil {
		t.Fatalf("Append failed: %v", err)
	}

	searcher := NewSearcher(store)

	// 执行搜索
	resp, err := searcher.Search(SearchQuery{
		Keywords: []string{"hello"},
	})
	if err != nil {
		t.Fatalf("Search failed: %v", err)
	}
	if resp.TotalMatches != 1 {
		t.Fatalf("Expected 1 match, got %d", resp.TotalMatches)
	}

	// 反向断言：家目录下不应存在 search_index.json
	home, err := os.UserHomeDir()
	if err != nil {
		t.Skip("无法获取家目录，跳过文件存在性检查")
	}
	indexPath := filepath.Join(home, ".whale", "search_index.json")
	if _, err := os.Stat(indexPath); err == nil {
		t.Errorf("死代码删除后仍存在 %s，可能 SaveIndex 被误保留或外部代码引入新调用方", indexPath)
	}
}

// TestDeadCodeRemoval_SearchStillFullScan 验证删除 LoadIndex 后，
// Search() 仍走全量扫描路径（store.List + searchInSession），
// 不依赖任何持久化索引。通过对比多 session 命中数确认扫描覆盖全部 session。
func TestDeadCodeRemoval_SearchStillFullScan(t *testing.T) {
	dir := t.TempDir()
	store, err := session.NewStore(dir)
	if err != nil {
		t.Fatalf("session.NewStore failed: %v", err)
	}

	// 写入 5 个 session，全部包含同一关键词
	for i := 0; i < 5; i++ {
		id := "session-" + string(rune('a'+i))
		if err := store.Append(id, session.UserText("common keyword")); err != nil {
			t.Fatalf("Append %s failed: %v", id, err)
		}
	}

	searcher := NewSearcher(store)
	resp, err := searcher.Search(SearchQuery{
		Keywords: []string{"common", "keyword"},
	})
	if err != nil {
		t.Fatalf("Search failed: %v", err)
	}

	// 全量扫描应覆盖全部 5 个 session（若误用索引可能漏扫）
	if resp.TotalSearched != 5 {
		t.Errorf("全量扫描应搜索 5 个 session，实际 %d", resp.TotalSearched)
	}
	if resp.TotalMatches != 5 {
		t.Errorf("全量扫描应命中 5 个 session，实际 %d", resp.TotalMatches)
	}
}

// TestDeadCodeRemoval_NewSearcherNoSideEffect 验证 NewSearcher 构造函数
// 不会触发任何索引文件写入或读取副作用。
// 删除 SaveIndex/LoadIndex 后，构造器应纯粹只赋值 store 字段。
func TestDeadCodeRemoval_NewSearcherNoSideEffect(t *testing.T) {
	dir := t.TempDir()
	store, err := session.NewStore(dir)
	if err != nil {
		t.Fatalf("session.NewStore failed: %v", err)
	}

	// 在 store 目录中预先写入若干 session
	if err := store.Append("s1", session.UserText("content")); err != nil {
		t.Fatalf("Append failed: %v", err)
	}

	// 构造 Searcher，不应产生任何文件 IO 副作用
	searcher := NewSearcher(store)
	if searcher == nil {
		t.Fatal("NewSearcher returned nil")
	}

	// 反向断言：store 目录下不应出现 search_index.json
	indexPath := filepath.Join(dir, "search_index.json")
	if _, err := os.Stat(indexPath); err == nil {
		t.Errorf("NewSearcher 不应生成 %s，但文件已存在", indexPath)
	}

	// 家目录下也不应出现
	if home, err := os.UserHomeDir(); err == nil {
		globalIndex := filepath.Join(home, ".whale", "search_index.json")
		if _, err := os.Stat(globalIndex); err == nil {
			t.Errorf("NewSearcher 不应在家目录生成 %s", globalIndex)
		}
	}
}

// TestDeadCodeRemoval_SearchInSessionIndependent 验证单 session 搜索
// SearchInSession 不依赖任何索引，直接扫描 JSONL 文件。
// 删除 LoadIndex 后此方法应仍正常工作。
func TestDeadCodeRemoval_SearchInSessionIndependent(t *testing.T) {
	dir := t.TempDir()
	store, err := session.NewStore(dir)
	if err != nil {
		t.Fatalf("session.NewStore failed: %v", err)
	}

	msgs := []session.Message{
		session.UserText("first message with target"),
		session.UserText("second message without"),
		session.AssistantText("third message with target", "model", nil),
	}
	for _, m := range msgs {
		if err := store.Append("solo", m); err != nil {
			t.Fatalf("Append failed: %v", err)
		}
	}

	searcher := NewSearcher(store)
	result, err := searcher.SearchInSession("solo", SearchQuery{
		Keywords: []string{"target"},
	})
	if err != nil {
		t.Fatalf("SearchInSession failed: %v", err)
	}
	if result.MatchCount != 2 {
		t.Errorf("Expected 2 matches (line 1 and 3), got %d", result.MatchCount)
	}
	if result.SessionID != "solo" {
		t.Errorf("Expected SessionID=solo, got %s", result.SessionID)
	}
}
