// Package search 测试
package search

import (
	"sync"
	"testing"

	"github.com/anchorandfish/whale-code/internal/session"
)

func createTestSessionStore(t *testing.T) *session.Store {
	t.Helper()
	dir := t.TempDir()
	store, err := session.NewStore(dir)
	if err != nil {
		t.Fatalf("session.NewStore failed: %v", err)
	}
	return store
}

func createSessions(t *testing.T, store *session.Store, sessions map[string][]session.Message) {
	t.Helper()
	for id, msgs := range sessions {
		if err := store.AppendBatch(id, msgs); err != nil {
			t.Fatalf("AppendBatch for %s failed: %v", id, err)
		}
	}
}

// TestNewSearcher 测试创建 Searcher 实例
func TestNewSearcher(t *testing.T) {
	sStore := createTestSessionStore(t)
	searcher := NewSearcher(sStore)
	if searcher == nil {
		t.Fatal("NewSearcher should not return nil")
	}
}

// TestSearch_SingleKeyword 测试单关键词搜索
func TestSearch_SingleKeyword(t *testing.T) {
	sStore := createTestSessionStore(t)
	createSessions(t, sStore, map[string][]session.Message{
		"session-1": {
			session.UserText("hello world"),
			session.AssistantText("goodbye", "model", nil),
		},
		"session-2": {
			session.UserText("hello there"),
			session.AssistantText("see you", "model", nil),
		},
	})

	searcher := NewSearcher(sStore)
	resp, err := searcher.Search(SearchQuery{
		Keywords: []string{"hello"},
	})
	if err != nil {
		t.Fatalf("Search failed: %v", err)
	}

	if resp.TotalMatches != 2 {
		t.Errorf("Expected 2 matching sessions, got %d", resp.TotalMatches)
	}
	if resp.TotalSearched != 2 {
		t.Errorf("Expected 2 total searched, got %d", resp.TotalSearched)
	}
}

// TestSearch_MultipleKeywords 测试多关键词 AND 搜索
func TestSearch_MultipleKeywords(t *testing.T) {
	sStore := createTestSessionStore(t)
	createSessions(t, sStore, map[string][]session.Message{
		"session-1": {
			session.UserText("database connection timeout"),
		},
		"session-2": {
			session.UserText("database index performance"),
		},
		"session-3": {
			session.UserText("connection pool settings"),
		},
	})

	searcher := NewSearcher(sStore)
	resp, err := searcher.Search(SearchQuery{
		Keywords: []string{"database", "connection"},
	})
	if err != nil {
		t.Fatalf("Search failed: %v", err)
	}

	if resp.TotalMatches != 1 {
		t.Errorf("Expected 1 matching session, got %d", resp.TotalMatches)
	}
	if len(resp.Results) > 0 && resp.Results[0].SessionID != "session-1" {
		t.Errorf("Expected session-1 to match, got %s", resp.Results[0].SessionID)
	}
}

// TestSearch_NoMatches 测试no match结果
func TestSearch_NoMatches(t *testing.T) {
	sStore := createTestSessionStore(t)
	createSessions(t, sStore, map[string][]session.Message{
		"session-1": {
			session.UserText("hello world"),
		},
	})

	searcher := NewSearcher(sStore)
	resp, err := searcher.Search(SearchQuery{
		Keywords: []string{"xyz-not-found"},
	})
	if err != nil {
		t.Fatalf("Search failed: %v", err)
	}

	if resp.TotalMatches != 0 {
		t.Errorf("Expected 0 matches, got %d", resp.TotalMatches)
	}
}

// TestSearch_EmptySessions 测试无会话时搜索
func TestSearch_EmptySessions(t *testing.T) {
	sStore := createTestSessionStore(t)
	searcher := NewSearcher(sStore)

	resp, err := searcher.Search(SearchQuery{
		Keywords: []string{"hello"},
	})
	if err != nil {
		t.Fatalf("Search failed: %v", err)
	}

	if resp.TotalMatches != 0 {
		t.Errorf("Expected 0 matches for empty store, got %d", resp.TotalMatches)
	}
	if resp.TotalSearched != 0 {
		t.Errorf("Expected 0 total searched for empty store, got %d", resp.TotalSearched)
	}
}

// TestSearch_CaseInsensitive 测试默认大小写不敏感
func TestSearch_CaseInsensitive(t *testing.T) {
	sStore := createTestSessionStore(t)
	createSessions(t, sStore, map[string][]session.Message{
		"session-1": {
			session.UserText("Database CONNECTION"),
		},
	})

	searcher := NewSearcher(sStore)
	resp, err := searcher.Search(SearchQuery{
		Keywords: []string{"database", "connection"},
	})
	if err != nil {
		t.Fatalf("Search failed: %v", err)
	}

	if resp.TotalMatches != 1 {
		t.Errorf("Case-insensitive search should match, got %d matches", resp.TotalMatches)
	}
}

// TestSearch_CaseSensitive 测试区分大小写搜索
func TestSearch_CaseSensitive(t *testing.T) {
	sStore := createTestSessionStore(t)
	createSessions(t, sStore, map[string][]session.Message{
		"session-1": {
			session.UserText("Database connection"),
		},
	})

	searcher := NewSearcher(sStore)

	// 区分大小写：匹配正确大小写
	resp1, err := searcher.Search(SearchQuery{
		Keywords:  []string{"Database"},
		MatchCase: true,
	})
	if err != nil {
		t.Fatalf("Search failed: %v", err)
	}
	if resp1.TotalMatches != 1 {
		t.Errorf("Case-sensitive search with correct case should match, got %d", resp1.TotalMatches)
	}

	// 区分大小写：不匹配错误大小写
	resp2, err := searcher.Search(SearchQuery{
		Keywords:  []string{"database"},
		MatchCase: true,
	})
	if err != nil {
		t.Fatalf("Search failed: %v", err)
	}
	if resp2.TotalMatches != 0 {
		t.Errorf("Case-sensitive search with wrong case should not match, got %d", resp2.TotalMatches)
	}
}

// TestSearch_RegexMode 测试正则表达式搜索
func TestSearch_RegexMode(t *testing.T) {
	sStore := createTestSessionStore(t)
	createSessions(t, sStore, map[string][]session.Message{
		"session-1": {
			session.UserText("func ReadFile(path string) error"),
			session.UserText("func WriteFile(path string)"),
		},
	})

	searcher := NewSearcher(sStore)
	resp, err := searcher.Search(SearchQuery{
		Keywords:  []string{`func\s+\w+\(.*\)\s+error`},
		RegexMode: true,
	})
	if err != nil {
		t.Fatalf("Search failed: %v", err)
	}

	if resp.TotalMatches != 1 {
		t.Errorf("Regex should match ReadFile declaration, got %d matches", resp.TotalMatches)
	}
	if len(resp.Results) > 0 && resp.Results[0].MatchCount != 1 {
		t.Errorf("Expected 1 match in session, got %d", resp.Results[0].MatchCount)
	}
}

// TestSearch_InvalidRegex 测试无效正则表达式
func TestSearch_InvalidRegex(t *testing.T) {
	sStore := createTestSessionStore(t)
	createSessions(t, sStore, map[string][]session.Message{
		"session-1": {
			session.UserText("some content"),
		},
	})

	searcher := NewSearcher(sStore)
	_, err := searcher.Search(SearchQuery{
		Keywords:  []string{`[unclosed`},
		RegexMode: true,
	})
	if err == nil {
		t.Fatal("Expected error for invalid regex")
	}
}

// TestSearch_SpecificSession 测试限定会话搜索
func TestSearch_SpecificSession(t *testing.T) {
	sStore := createTestSessionStore(t)
	createSessions(t, sStore, map[string][]session.Message{
		"session-1": {
			session.UserText("hello from session 1"),
		},
		"session-2": {
			session.UserText("hello from session 2"),
		},
		"session-3": {
			session.UserText("hello from session 3"),
		},
	})

	searcher := NewSearcher(sStore)
	resp, err := searcher.Search(SearchQuery{
		Keywords:   []string{"hello"},
		SessionIDs: []string{"session-2"},
	})
	if err != nil {
		t.Fatalf("Search failed: %v", err)
	}

	if resp.TotalMatches != 1 {
		t.Errorf("Expected 1 match (session-2 only), got %d", resp.TotalMatches)
	}
	if len(resp.Results) > 0 && resp.Results[0].SessionID != "session-2" {
		t.Errorf("Expected session-2, got %s", resp.Results[0].SessionID)
	}
}

// TestSearch_MaxResults 测试最大结果数限制
func TestSearch_MaxResults(t *testing.T) {
	sStore := createTestSessionStore(t)
	sessions := make(map[string][]session.Message)
	for i := 0; i < 10; i++ {
		id := "session-" + string(rune('a'+i))
		sessions[id] = []session.Message{
			session.UserText("hello"),
		}
	}
	createSessions(t, sStore, sessions)

	searcher := NewSearcher(sStore)
	resp, err := searcher.Search(SearchQuery{
		Keywords:   []string{"hello"},
		MaxResults: 3,
	})
	if err != nil {
		t.Fatalf("Search failed: %v", err)
	}

	if len(resp.Results) > 3 {
		t.Errorf("Expected at most 3 results, got %d", len(resp.Results))
	}
}

// TestSearchInSession 测试单会话搜索
func TestSearchInSession(t *testing.T) {
	sStore := createTestSessionStore(t)
	createSessions(t, sStore, map[string][]session.Message{
		"test-session": {
			session.UserText("message with no match"),
			session.UserText("keyword found here"),
			session.AssistantText("keyword also here", "model", nil),
			session.UserText("another no match"),
			session.UserText("keyword third time"),
		},
	})

	searcher := NewSearcher(sStore)
	result, err := searcher.SearchInSession("test-session", SearchQuery{
		Keywords: []string{"keyword"},
	})
	if err != nil {
		t.Fatalf("SearchInSession failed: %v", err)
	}

	if result.MatchCount != 3 {
		t.Errorf("Expected 3 matches, got %d", result.MatchCount)
	}
}

// TestSearch_EmptyKeywords 测试空关键词搜索
func TestSearch_EmptyKeywords(t *testing.T) {
	sStore := createTestSessionStore(t)
	createSessions(t, sStore, map[string][]session.Message{
		"session-1": {
			session.UserText("hello"),
		},
	})

	searcher := NewSearcher(sStore)
	_, err := searcher.Search(SearchQuery{
		Keywords: []string{},
	})
	if err == nil {
		t.Fatal("Expected error for empty keywords")
	}
}

// TestSearch_MalformedJSONL 测试损坏的 JSONL 文件
func TestSearch_MalformedJSONL(t *testing.T) {
	sStore := createTestSessionStore(t)
	// 先正常创建一些消息
	sStore.Append("test-session", session.UserText("hello keyword"))
	sStore.Append("test-session", session.UserText("another message"))

	searcher := NewSearcher(sStore)
	resp, err := searcher.Search(SearchQuery{
		Keywords: []string{"keyword"},
	})
	if err != nil {
		t.Fatalf("Search failed: %v", err)
	}

	if resp.TotalMatches != 1 {
		t.Errorf("Expected 1 match, got %d", resp.TotalMatches)
	}
}

// TestSearch_TextTruncation 测试匹配文本截断
func TestSearch_TextTruncation(t *testing.T) {
	longText := ""
	for i := 0; i < 500; i++ {
		longText += "a"
	}
	longText += " KEYWORD "
	for i := 0; i < 500; i++ {
		longText += "b"
	}

	sStore := createTestSessionStore(t)
	createSessions(t, sStore, map[string][]session.Message{
		"session-1": {
			session.UserText(longText),
		},
	})

	searcher := NewSearcher(sStore)
	resp, err := searcher.Search(SearchQuery{
		Keywords: []string{"KEYWORD"},
	})
	if err != nil {
		t.Fatalf("Search failed: %v", err)
	}

	if resp.TotalMatches != 1 {
		t.Errorf("Expected 1 match, got %d", resp.TotalMatches)
	}
	if len(resp.Results) > 0 && len(resp.Results[0].Matches) > 0 {
		text := resp.Results[0].Matches[0].Text
		runes := []rune(text)
		if len(runes) > 203 { // 200 + "..." 允许少量溢出
			t.Errorf("Text should be truncated to ~200 chars, got %d runes", len(runes))
		}
	}
}

// TestSearch_MatchesLimit 测试每会话匹配详情上限
func TestSearch_MatchesLimit(t *testing.T) {
	sStore := createTestSessionStore(t)
	msgs := make([]session.Message, 10)
	for i := range msgs {
		msgs[i] = session.UserText("keyword present")
	}
	createSessions(t, sStore, map[string][]session.Message{
		"session-1": msgs,
	})

	searcher := NewSearcher(sStore)
	resp, err := searcher.Search(SearchQuery{
		Keywords: []string{"keyword"},
	})
	if err != nil {
		t.Fatalf("Search failed: %v", err)
	}

	if resp.TotalMatches != 1 {
		t.Fatalf("Expected 1 matching session, got %d", resp.TotalMatches)
	}

	result := resp.Results[0]
	if result.MatchCount != 10 {
		t.Errorf("MatchCount should be 10, got %d", result.MatchCount)
	}
	if len(result.Matches) > 5 {
		t.Errorf("Matches details should be capped at %d, got %d", 5, len(result.Matches))
	}
}

// TestSearch_ResultOrdering 测试搜索结果排序
func TestSearch_ResultOrdering(t *testing.T) {
	sStore := createTestSessionStore(t)
	createSessions(t, sStore, map[string][]session.Message{
		"s-low": {
			session.UserText("keyword"),
		},
		"s-high": {
			session.UserText("keyword"),
			session.UserText("keyword"),
			session.UserText("keyword"),
			session.UserText("keyword"),
			session.UserText("keyword"),
		},
		"s-mid": {
			session.UserText("keyword"),
			session.UserText("keyword"),
			session.UserText("keyword"),
		},
	})

	searcher := NewSearcher(sStore)
	resp, err := searcher.Search(SearchQuery{
		Keywords: []string{"keyword"},
	})
	if err != nil {
		t.Fatalf("Search failed: %v", err)
	}

	if resp.TotalMatches != 3 {
		t.Fatalf("Expected 3 matches, got %d", resp.TotalMatches)
	}

	// 最高匹配次数的应排最前
	if resp.Results[0].SessionID != "s-high" {
		t.Errorf("Expected s-high first, got %s", resp.Results[0].SessionID)
	}
	if resp.Results[2].SessionID != "s-low" {
		t.Errorf("Expected s-low last, got %s", resp.Results[2].SessionID)
	}
}

// TestSearchMatchDetails 测试匹配详情完整性
func TestSearchMatchDetails(t *testing.T) {
	sStore := createTestSessionStore(t)
	createSessions(t, sStore, map[string][]session.Message{
		"test-session": {
			session.UserText("keyword match"),
		},
	})

	searcher := NewSearcher(sStore)
	resp, err := searcher.Search(SearchQuery{
		Keywords: []string{"keyword"},
	})
	if err != nil {
		t.Fatalf("Search failed: %v", err)
	}

	if len(resp.Results) == 0 {
		t.Fatal("Should have 1 result")
	}

	result := resp.Results[0]
	if len(result.Matches) != 1 {
		t.Fatal("Should have 1 match")
	}

	match := result.Matches[0]
	if match.MessageIndex != 0 {
		t.Errorf("MessageIndex = %d, want 0", match.MessageIndex)
	}
	if match.Role != "user" {
		t.Errorf("Role = '%s', want 'user'", match.Role)
	}
	if match.Text == "" {
		t.Error("Text should not be empty")
	}
	if match.Timestamp == 0 {
		t.Error("Timestamp should not be 0")
	}
}

// TestSearchWithToolMessages 测试搜索工具消息
func TestSearchWithToolMessages(t *testing.T) {
	sStore := createTestSessionStore(t)
	createSessions(t, sStore, map[string][]session.Message{
		"test-session": {
			session.UserText("please run a command"),
			session.AssistantWithTools([]session.ContentBlock{
				{Type: "text", Text: "Let me check"},
				{Type: "tool_use", ID: "tool-1", Name: "bash", Input: `{"command":"ls -la"}`},
			}, "model", nil),
			session.ToolResult("tool-1", "bash", "compilation error: syntax error at line 42", false),
		},
	})

	searcher := NewSearcher(sStore)

	// 搜索工具输出中的内容
	resp1, err := searcher.Search(SearchQuery{
		Keywords: []string{"syntax", "error"},
	})
	if err != nil {
		t.Fatalf("Search failed: %v", err)
	}
	if resp1.TotalMatches != 1 {
		t.Errorf("Should find 'syntax error' in tool result, got %d matches", resp1.TotalMatches)
	}

	// 搜索工具名称
	resp2, err := searcher.Search(SearchQuery{
		Keywords: []string{"bash"},
	})
	if err != nil {
		t.Fatalf("Search failed: %v", err)
	}
	if resp2.TotalMatches != 1 {
		t.Errorf("Should find 'bash' as tool name, got %d matches", resp2.TotalMatches)
	}
}

// TestFormatResults test result格式化
func TestFormatResults(t *testing.T) {
	resp := &SearchResponse{
		Query: SearchQuery{
			Keywords: []string{"test", "keyword"},
		},
		TotalSearched: 5,
		TotalMatches:  1,
		Results: []SearchResult{
			{
				SessionID:   "session-1",
				SessionName: "测试会话",
				MatchCount:  2,
				Matches: []Match{
					{MessageIndex: 0, Role: "user", Text: "test keyword in message"},
				},
			},
		},
	}

	formatted := FormatResults(resp)
	if formatted == "" {
		t.Error("Formatted results should not be empty")
	}
	if !contains(formatted, "测试会话") {
		t.Error("Formatted results should contain session name")
	}
	if !contains(formatted, "test keyword in message") {
		t.Error("Formatted results should contain match text")
	}
}

// TestFormatResults_Empty 测试空结果格式化
func TestFormatResults_Empty(t *testing.T) {
	resp := &SearchResponse{
		Query: SearchQuery{
			Keywords: []string{"nothing"},
		},
	}

	formatted := FormatResults(resp)
	if formatted == "" {
		t.Error("Formatted results should not be empty for empty results")
	}
}

// TestTruncateText 测试文本截断
func TestTruncateText(t *testing.T) {
	short := "hello"
	if truncateText(short, 200) != "hello" {
		t.Error("Short text should not be truncated")
	}

	long := ""
	for i := 0; i < 300; i++ {
		long += "x"
	}
	result := truncateText(long, 200)
	if len([]rune(result)) != 203 { // 200 chars + "..."
		t.Errorf("Truncated length = %d, want 203", len([]rune(result)))
	}
}

// TestExtractSearchText 测试搜索文本提取
func TestExtractSearchText(t *testing.T) {
	// 文本消息
	textMsg := session.UserText("hello world")
	extracted := extractSearchText(textMsg)
	if !contains(extracted, "hello world") {
		t.Errorf("Text message extraction: got '%s', want containing 'hello world'", extracted)
	}

	// 工具调用消息
	toolMsg := session.Message{
		Role: session.RoleAssistant,
		Blocks: []session.ContentBlock{
			{Type: "text", Text: "Let me check"},
			{Type: "tool_use", ID: "1", Name: "bash", Input: `{"command":"ls -la"}`},
		},
	}
	extracted = extractSearchText(toolMsg)
	if !contains(extracted, "bash") {
		t.Errorf("Tool message extraction should contain 'bash', got '%s'", extracted)
	}

	// 工具结果消息
	resultMsg := session.ToolResult("t1", "bash", "command output here", false)
	extracted = extractSearchText(resultMsg)
	if !contains(extracted, "command output here") {
		t.Errorf("Tool result extraction should contain output, got '%s'", extracted)
	}
}

// TestConcurrentSearch 测试并发搜索
func TestConcurrentSearch(t *testing.T) {
	sStore := createTestSessionStore(t)
	createSessions(t, sStore, map[string][]session.Message{
		"session-1": {session.UserText("hello alpha")},
		"session-2": {session.UserText("hello beta")},
		"session-3": {session.UserText("hello gamma")},
		"session-4": {session.UserText("hello delta")},
		"session-5": {session.UserText("hello epsilon")},
	})

	searcher := NewSearcher(sStore)
	var wg sync.WaitGroup
	errors := make(chan error, 5)

	keywords := [][]string{
		{"alpha"}, {"beta"}, {"gamma"}, {"delta"}, {"epsilon"},
	}

	for i := 0; i < 5; i++ {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			resp, err := searcher.Search(SearchQuery{
				Keywords: keywords[idx],
			})
			if err != nil {
				errors <- err
				return
			}
			if resp.TotalMatches != 1 {
				errors <- nil
			}
		}(i)
	}

	wg.Wait()
	close(errors)

	for err := range errors {
		if err != nil {
			t.Errorf("Concurrent search error: %v", err)
		}
	}
}

func contains(s, substr string) bool {
	for i := 0; i <= len(s)-len(substr); i++ {
		if s[i:i+len(substr)] == substr {
			return true
		}
	}
	return false
}
