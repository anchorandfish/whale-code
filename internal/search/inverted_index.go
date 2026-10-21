// Package search — M23-03 倒排索引（SNAPSHOT）
//
// 本文件实现跨会话搜索的倒排索引，对标 VSCode FTS5 倒排索引。
// 由于 CGO_ENABLED=0（不能用 sqlite3 FTS5），用纯 Go map 实现内存倒排索引。
//
// 对标：
// - VSCode FTS5：https://www.sqlite.org/fts5.html
// - 同类 CLI code 跨会话搜索
//
// 设计要点（候选 A 选定）：
// - 接口签名：InvertedIndex 接口抽象，支持多后端（内存/磁盘）
// - 结构体字段：posting 含 SessionID/MessageIndex/Timestamp
// - 分层权责：本文件只做"索引构建+查询"，会话遍历由 search.go
// - 降级策略：索引构建失败仅记录日志，回退到线性扫描
// - TTL规则：索引持久化到磁盘（可选），TTL=会话最后更新时间
package search

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
)

// maxIndexTokenLength 是单个 token 最大长度（防超长 token 耗内存）。
const maxIndexTokenLength = 200

// maxIndexTokensPerMessage 是单条消息最大索引 token 数（防超大消息耗内存）。
const maxIndexTokensPerMessage = 5000

// Posting 是倒排索引的 posting 条目（SNAPSHOT-1）。
type Posting struct {
	SessionID    string // 会话 ID
	MessageIndex int    // 消息索引（会话内）
	Timestamp    int64  // 消息时间戳（用于排序）
}

// memoryIndex 是内存倒排索引实现（SNAPSHOT-2）。
//
// 结构：
// - index: token -> []Posting（按 SessionID + MessageIndex 排序）
// - sessionTimes: SessionID -> 最后更新时间（用于增量更新判断）
type memoryIndex struct {
	mu           sync.RWMutex
	index        map[string][]Posting // token -> postings
	sessionTimes map[string]int64     // sessionID -> 最后更新时间
}

// NewMemoryIndex 创建内存倒排索引（SNAPSHOT-3）。
func NewMemoryIndex() *memoryIndex {
	return &memoryIndex{
		index:        make(map[string][]Posting),
		sessionTimes: make(map[string]int64),
	}
}

// AddMessage 将单条消息加入倒排索引（SNAPSHOT-4）。
//
// 行为：
// - 分词（小写化 + 按非字母数字切分）
// - 每个 token 加入倒排索引
// - 单条消息 token 数超过 maxIndexTokensPerMessage 时截断
// - 单 token 长度超过 maxIndexTokenLength 时跳过
func (idx *memoryIndex) AddMessage(sessionID string, msgIndex int, timestamp int64, text string) {
	tokens := tokenize(text)
	if len(tokens) > maxIndexTokensPerMessage {
		tokens = tokens[:maxIndexTokensPerMessage]
	}

	posting := Posting{
		SessionID:    sessionID,
		MessageIndex: msgIndex,
		Timestamp:    timestamp,
	}

	idx.mu.Lock()
	defer idx.mu.Unlock()

	for _, token := range tokens {
		if len(token) > maxIndexTokenLength {
			continue
		}
		idx.index[token] = append(idx.index[token], posting)
	}

	// 更新会话最后更新时间
	if timestamp > idx.sessionTimes[sessionID] {
		idx.sessionTimes[sessionID] = timestamp
	}
}

// Search 按关键词查询倒排索引（SNAPSHOT-5）。
//
// 行为：
// - 单关键词：返回该 token 的所有 postings
// - 多关键词：返回所有关键词都出现的 session（AND 语义）
// - 按 Timestamp 降序排序（最新在前）
func (idx *memoryIndex) Search(keywords []string) []Posting {
	if len(keywords) == 0 {
		return nil
	}

	idx.mu.RLock()
	defer idx.mu.RUnlock()

	// 收集每个关键词的 postings
	var postingSets [][]Posting
	for _, kw := range keywords {
		token := strings.ToLower(kw)
		postings, ok := idx.index[token]
		if !ok {
			return nil // 任一关键词不存在，AND 结果为空
		}
		postingSets = append(postingSets, postings)
	}

	// AND 语义：找同时在所有关键词 postings 中出现的 (SessionID, MessageIndex)
	if len(postingSets) == 1 {
		// 单关键词，直接返回
		result := make([]Posting, len(postingSets[0]))
		copy(result, postingSets[0])
		sortByTimestampDesc(result)
		return result
	}

	// 多关键词 AND
	counts := make(map[string]int)         // sessionID -> 出现次数
	postingMap := make(map[string]Posting) // sessionID:msgIndex -> Posting
	for _, set := range postingSets {
		seen := make(map[string]bool)
		for _, p := range set {
			key := fmt.Sprintf("%s:%d", p.SessionID, p.MessageIndex)
			if !seen[key] {
				seen[key] = true
				counts[key]++
				postingMap[key] = p
			}
		}
	}

	// 只保留在所有关键词中出现的
	var result []Posting
	expected := len(postingSets)
	for key, count := range counts {
		if count == expected {
			result = append(result, postingMap[key])
		}
	}
	sortByTimestampDesc(result)
	return result
}

// RemoveSession 从索引中移除指定会话的所有条目（SNAPSHOT-6）。
func (idx *memoryIndex) RemoveSession(sessionID string) {
	idx.mu.Lock()
	defer idx.mu.Unlock()
	for token, postings := range idx.index {
		var kept []Posting
		for _, p := range postings {
			if p.SessionID != sessionID {
				kept = append(kept, p)
			}
		}
		if len(kept) == 0 {
			delete(idx.index, token)
		} else {
			idx.index[token] = kept
		}
	}
	delete(idx.sessionTimes, sessionID)
}

// Size 返回索引中的 token 数量（SNAPSHOT-7）。
func (idx *memoryIndex) Size() int {
	idx.mu.RLock()
	defer idx.mu.RUnlock()
	return len(idx.index)
}

// SessionLastUpdated 返回会话最后更新时间（SNAPSHOT-8）。
func (idx *memoryIndex) SessionLastUpdated(sessionID string) int64 {
	idx.mu.RLock()
	defer idx.mu.RUnlock()
	return idx.sessionTimes[sessionID]
}

// tokenize 分词（SNAPSHOT-9）。
//
// 简化分词：
// - 转小写
// - 按非字母数字字符切分
// - 过滤空 token
// - 保留中文连续序列（按 unicode 字母切分时中文会聚合）
func tokenize(text string) []string {
	text = strings.ToLower(text)
	// 按非字母数字切分（中文字符在 unicode 中视为字母）
	var tokens []string
	var current strings.Builder
	for _, r := range text {
		if isTokenRune(r) {
			current.WriteRune(r)
		} else {
			if current.Len() > 0 {
				tokens = append(tokens, current.String())
				current.Reset()
			}
		}
	}
	if current.Len() > 0 {
		tokens = append(tokens, current.String())
	}
	return tokens
}

// isTokenRune 判断 rune 是否为 token 字符（字母/数字/中文）。
func isTokenRune(r rune) bool {
	if r >= 'a' && r <= 'z' {
		return true
	}
	if r >= '0' && r <= '9' {
		return true
	}
	if r >= 0x4e00 && r <= 0x9fff { // CJK Unified Ideographs
		return true
	}
	return false
}

// sortByTimestampDesc 按 Timestamp 降序排序（最新在前）。
func sortByTimestampDesc(postings []Posting) {
	sort.Slice(postings, func(i, j int) bool {
		return postings[i].Timestamp > postings[j].Timestamp
	})
}

// IndexManager 是倒排索引管理器（SNAPSHOT-10）。
//
// 封装索引构建、查询、持久化能力。
type IndexManager struct {
	index *memoryIndex
	mu    sync.Mutex
}

// NewIndexManager 创建索引管理器（SNAPSHOT-11）。
func NewIndexManager() *IndexManager {
	return &IndexManager{
		index: NewMemoryIndex(),
	}
}

// IndexSession 索引单个会话（SNAPSHOT-12）。
//
// 增量更新：仅当会话最后更新时间 > 索引中的时间时重新索引。
func (m *IndexManager) IndexSession(sessionID string, messages []indexedMessage) {
	m.mu.Lock()
	defer m.mu.Unlock()

	var lastUpdate int64
	for _, msg := range messages {
		if msg.Timestamp > lastUpdate {
			lastUpdate = msg.Timestamp
		}
	}
	if lastUpdate <= m.index.SessionLastUpdated(sessionID) {
		return // 无需更新
	}

	// 先移除旧索引，再重建
	m.index.RemoveSession(sessionID)
	for _, msg := range messages {
		m.index.AddMessage(sessionID, msg.Index, msg.Timestamp, msg.Text)
	}
}

// indexedMessage 是索引消息结构（内部用）。
type indexedMessage struct {
	Index     int
	Text      string
	Timestamp int64
}

// SearchByIndex 用倒排索引加速搜索（SNAPSHOT-13）。
//
// 返回匹配的 postings（按时间降序）。
func (m *IndexManager) SearchByIndex(keywords []string) []Posting {
	return m.index.Search(keywords)
}

// PersistToFile 持久化索引到文件（SNAPSHOT-14）。
//
// 简化实现：序列化为 JSON。
func (m *IndexManager) PersistToFile(path string) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	// 确保目录存在
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return fmt.Errorf("index: mkdir: %w", err)
	}

	// 序列化
	m.index.mu.RLock()
	defer m.index.mu.RUnlock()

	type serialIndex struct {
		Index        map[string][]Posting `json:"index"`
		SessionTimes map[string]int64     `json:"session_times"`
		SavedAt      int64                `json:"saved_at"`
	}
	si := serialIndex{
		Index:        m.index.index,
		SessionTimes: m.index.sessionTimes,
		SavedAt:      time.Now().Unix(),
	}

	data, err := json.Marshal(si)
	if err != nil {
		return fmt.Errorf("index: marshal: %w", err)
	}

	// 原子写入
	tmpPath := path + ".tmp"
	if err := os.WriteFile(tmpPath, data, 0600); err != nil {
		return fmt.Errorf("index: write tmp: %w", err)
	}
	if err := os.Rename(tmpPath, path); err != nil {
		return fmt.Errorf("index: rename: %w", err)
	}
	return nil
}

// LoadFromFile 从文件加载索引（SNAPSHOT-15）。
func (m *IndexManager) LoadFromFile(path string) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil // 首次启动
		}
		return fmt.Errorf("index: read: %w", err)
	}

	type serialIndex struct {
		Index        map[string][]Posting `json:"index"`
		SessionTimes map[string]int64     `json:"session_times"`
	}
	var si serialIndex
	if err := json.Unmarshal(data, &si); err != nil {
		return fmt.Errorf("index: unmarshal: %w", err)
	}

	m.index.index = si.Index
	m.index.sessionTimes = si.SessionTimes
	return nil
}

// IndexSize 返回索引大小（SNAPSHOT-16）。
func (m *IndexManager) IndexSize() int {
	return m.index.Size()
}
