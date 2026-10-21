// Package search provides cross-session search functionality
// supports keyword search and regex matching across all historical sessions
package search

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/anchorandfish/whale-code/internal/session"
)

// SearchQuery represents search query parameters
type SearchQuery struct {
	Keywords   []string
	MatchCase  bool
	RegexMode  bool
	MaxResults int
	SessionIDs []string
}

// Match represents detailed info for a single match
type Match struct {
	MessageIndex int    `json:"message_index"`
	Role         string `json:"role"`
	Text         string `json:"text"`
	Timestamp    int64  `json:"timestamp"`
}

// SearchResult represents a single search result
type SearchResult struct {
	SessionID   string  `json:"session_id"`
	SessionName string  `json:"session_name"`
	MatchCount  int     `json:"match_count"`
	Matches     []Match `json:"matches"`
	UpdatedAt   int64   `json:"updated_at"`
}

// SearchResponse represents search response summary
type SearchResponse struct {
	Query         SearchQuery    `json:"query"`
	TotalSearched int            `json:"total_searched"`
	TotalMatches  int            `json:"total_matches"`
	Results       []SearchResult `json:"results"`
	SearchedAt    int64          `json:"searched_at"`
}

// Searcher is the cross-session search engine
type Searcher struct {
	store                *session.Store
	maxMatchesPerSession int
}

// NewSearcher 创建搜索器（向后兼容 wrapper，maxMatchesPerSession 用默认值 5）
// 旧测试代码可继续使用此签名，生产代码推荐用 NewSearcherWithConfig
func NewSearcher(store *session.Store) *Searcher {
	return NewSearcherWithConfig(store, 5)
}

// NewSearcherWithConfig 创建带配置的 Searcher（H8 新增，推荐生产代码使用）
// maxMatches 为每会话搜索匹配详情上限，<=0 时 fallback 到默认值 5
func NewSearcherWithConfig(store *session.Store, maxMatches int) *Searcher {
	if maxMatches <= 0 {
		maxMatches = 5
	}
	return &Searcher{
		store:                store,
		maxMatchesPerSession: maxMatches,
	}
}

// Search executes cross-session search
func (s *Searcher) Search(query SearchQuery) (*SearchResponse, error) {
	if len(query.Keywords) == 0 && !query.RegexMode {
		return nil, fmt.Errorf("search keywords required")
	}
	if query.MaxResults <= 0 {
		query.MaxResults = 20
	}

	// Compile search pattern
	matcher, err := buildMatcher(query)
	if err != nil {
		return nil, err
	}

	// Determine search scope
	var sessionIDs []string
	sessionNames := make(map[string]string)
	if len(query.SessionIDs) > 0 {
		sessionIDs = query.SessionIDs
	} else {
		summaries, err := s.store.List()
		if err != nil {
			return nil, fmt.Errorf("failed to list sessions: %w", err)
		}
		for _, sum := range summaries {
			sessionIDs = append(sessionIDs, sum.ID)
			sessionNames[sum.ID] = sum.Name
		}
	}

	resp := &SearchResponse{
		Query:         query,
		TotalSearched: len(sessionIDs),
		SearchedAt:    time.Now().UnixMilli(),
	}

	var results []SearchResult
	for _, sid := range sessionIDs {
		name := sessionNames[sid]
		result, err := s.searchInSession(sid, name, matcher, query.MaxResults)
		if err != nil {
			continue // Skip corrupted session
		}
		if result.MatchCount > 0 {
			results = append(results, *result)
		}
	}

	// Sort by match count descending
	sort.Slice(results, func(i, j int) bool {
		if results[i].MatchCount != results[j].MatchCount {
			return results[i].MatchCount > results[j].MatchCount
		}
		return results[i].UpdatedAt > results[j].UpdatedAt
	})

	// Limit total results
	if len(results) > query.MaxResults {
		results = results[:query.MaxResults]
	}

	resp.TotalMatches = len(results)
	resp.Results = results
	return resp, nil
}

// SearchInSession searches in a single session
func (s *Searcher) SearchInSession(sessionID string, query SearchQuery) (*SearchResult, error) {
	matcher, err := buildMatcher(query)
	if err != nil {
		return nil, err
	}
	return s.searchInSession(sessionID, sessionID, matcher, query.MaxResults)
}

// searchInSession searches in a single session (internal method)
func (s *Searcher) searchInSession(sessionID, sessionName string, matcher func(string) bool, maxPerSession int) (*SearchResult, error) {
	path := filepathJoin(s.store.Dir(), sessionID+".jsonl")

	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()

	result := &SearchResult{
		SessionID: sessionID,
	}

	scanner := bufio.NewScanner(f)
	buf := make([]byte, 0, 1024*1024)
	scanner.Buffer(buf, 10*1024*1024)

	msgIndex := 0
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}

		var msg session.Message
		if err := json.Unmarshal([]byte(line), &msg); err != nil {
			continue
		}

		text := extractSearchText(msg)
		if text == "" {
			msgIndex++
			continue
		}

		if matcher(text) {
			result.MatchCount++
			// Keep at most maxMatchesPerSession match details
			// H8：从 Searcher.maxMatchesPerSession 字段读取（原硬编码 const 5）
			if len(result.Matches) < s.maxMatchesPerSession {
				result.Matches = append(result.Matches, Match{
					MessageIndex: msgIndex,
					Role:         string(msg.Role),
					Text:         truncateText(text, 200),
					Timestamp:    msg.Timestamp,
				})
			}
			if result.UpdatedAt == 0 || msg.Timestamp > result.UpdatedAt {
				result.UpdatedAt = msg.Timestamp
			}
		}
		msgIndex++
	}

	// Try to get session name
	info, err := os.Stat(path)
	if err == nil {
		if result.UpdatedAt == 0 {
			result.UpdatedAt = info.ModTime().UnixMilli()
		}
	}

	result.SessionName = sessionName
	if result.SessionName == "" {
		result.SessionName = sessionID
	}

	return result, nil
}

// maxSearchTextLength limits the maximum length of text matched against regex
// to prevent excessive processing time on large inputs
const maxSearchTextLength = 100 * 1024 // 100KB per message

func filepathJoin(elem ...string) string {
	return filepath.Join(elem...)
}

func buildMatcher(query SearchQuery) (func(string) bool, error) {
	if query.RegexMode {
		if len(query.Keywords) == 0 {
			return nil, fmt.Errorf("regex search requires a regex pattern")
		}
		pattern := query.Keywords[0]
		if len(pattern) > 500 {
			return nil, fmt.Errorf("regex pattern too long (max 500 characters)")
		}
		re, err := regexp.Compile(pattern)
		if err != nil {
			return nil, fmt.Errorf("invalid regex: %w", err)
		}
		// Limit regex complexity: patterns longer than 200 runes may still cause
		// performance degradation even with RE2 engine
		if len([]rune(pattern)) > 200 {
			return nil, fmt.Errorf("regex pattern too complex (max 200 characters)")
		}
		return func(text string) bool {
			// Truncate text to prevent excessive processing on large inputs
			searchText := text
			if len(searchText) > maxSearchTextLength {
				searchText = searchText[:maxSearchTextLength]
			}
			return re.MatchString(searchText)
		}, nil
	}

	// Normal keyword matching
	keywords := make([]string, len(query.Keywords))
	for i, kw := range query.Keywords {
		if query.MatchCase {
			keywords[i] = kw
		} else {
			keywords[i] = strings.ToLower(kw)
		}
	}

	return func(text string) bool {
		// Truncate text to prevent excessive processing on large inputs
		searchText := text
		if !query.MatchCase {
			searchText = strings.ToLower(searchText)
		}
		if len(searchText) > maxSearchTextLength {
			searchText = searchText[:maxSearchTextLength]
		}
		for _, kw := range keywords {
			if !strings.Contains(searchText, kw) {
				return false
			}
		}
		return true
	}, nil
}

// extractSearchText extracts searchable text from message
func extractSearchText(msg session.Message) string {
	var parts []string
	for _, b := range msg.Blocks {
		switch b.Type {
		case "text":
			if b.Text != "" {
				parts = append(parts, b.Text)
			}
		case "tool_use":
			if b.Name != "" {
				parts = append(parts, b.Name)
			}
			if b.Input != "" {
				parts = append(parts, b.Input)
			}
		case "tool_result":
			if b.ToolName != "" {
				parts = append(parts, b.ToolName)
			}
			if b.Output != "" {
				parts = append(parts, b.Output)
			}
		case "thinking":
			if b.Thinking != "" {
				parts = append(parts, b.Thinking)
			}
		}
	}
	return strings.Join(parts, " ")
}

// truncateText truncates text to specified character count
func truncateText(text string, maxChars int) string {
	runes := []rune(text)
	if len(runes) <= maxChars {
		return text
	}
	return string(runes[:maxChars]) + "..."
}

// FormatResults formats search results as displayable text
func FormatResults(resp *SearchResponse) string {
	var sb strings.Builder

	if len(resp.Results) == 0 {
		sb.WriteString("No matching sessions found.\n")
		if len(resp.Query.Keywords) > 0 {
			sb.WriteString(fmt.Sprintf("Search terms: %s\n", strings.Join(resp.Query.Keywords, " ")))
		}
		return sb.String()
	}

	sb.WriteString(fmt.Sprintf("Search \"%s\", searched %d sessions, found %d matches\n\n",
		formatKeywords(resp.Query.Keywords), resp.TotalSearched, resp.TotalMatches))

	for i, result := range resp.Results {
		sb.WriteString(fmt.Sprintf("%d. Session %s  (%d matches)\n", i+1, result.SessionName, result.MatchCount))
		for _, match := range result.Matches {
			sb.WriteString(fmt.Sprintf("   [Turn %d] %s: %s\n", match.MessageIndex, match.Role, match.Text))
		}
		if result.MatchCount > len(result.Matches) {
			sb.WriteString(fmt.Sprintf("   ... ... %d more matches\n", result.MatchCount-len(result.Matches)))
		}
		sb.WriteString("\n")
	}

	return sb.String()
}

// formatKeywords formats keyword display
func formatKeywords(keywords []string) string {
	return strings.Join(keywords, " ")
}
