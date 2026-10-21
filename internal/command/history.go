// history.go — 命令历史持久化（M09-01 改造，）
// 持久化命令历史到 ~/.whale/command_history.jsonl，支持跨会话回溯。
// 实现 hook.Reloadable 接口，可被 HotReloader 监听外部修改（C14 复用承诺）。
// 对标同类 CLI ~/.<同类 CLI 配置目录>/history.jsonl @2026-03-31 快照。
package command

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// HistoryEntry 单条命令历史记录（M09-01 改造，）
type HistoryEntry struct {
	Command   string    `json:"command"`    // 完整命令（含 / 前缀）
	Timestamp time.Time `json:"timestamp"`  // 执行时间
	SessionID string    `json:"session_id"` // 会话 ID（可空）
}

// defaultMaxHistoryEntries 默认最大历史条目数
const defaultMaxHistoryEntries = 1000

// HistoryStore 命令历史存储器，jsonl 持久化 + 内存缓存（M09-01 改造，）
// 实现 hook.Reloadable 接口，可被 HotReloader 监听外部修改（C14 复用承诺）
// 对标同类 CLI ~/.<同类 CLI 配置目录>/history.jsonl @2026-03-31 快照，超越点：热重载 + 前缀查询 API + cross-mode 复用
type HistoryStore struct {
	mu         sync.RWMutex
	entries    []HistoryEntry // 内存缓存（最近 N 条）
	filePath   string         // 持久化文件路径（~/.whale/command_history.jsonl）
	maxEntries int            // 最大条目数
}

// NewHistoryStore 创建历史存储器，filePath 为持久化文件路径（M09-01 改造，）
// filePath 为空时不持久化（仅内存）；maxEntries<=0 时用默认值 1000
// 启动期自动加载已有历史文件（失败降级为空历史）
func NewHistoryStore(filePath string, maxEntries int) *HistoryStore {
	if maxEntries <= 0 {
		maxEntries = defaultMaxHistoryEntries
	}
	h := &HistoryStore{
		entries:    make([]HistoryEntry, 0, maxEntries),
		filePath:   filePath,
		maxEntries: maxEntries,
	}
	if filePath != "" {
		_ = h.loadFromFile() // 加载失败降级为空历史
	}
	return h
}

// Record 记录一条命令到历史（同步写入内存 + 追加写入文件）
// OPT-04：采用同步 append（O_APPEND），简单可靠，性能 < 1ms
// OPT-05：文件权限 0600（含敏感命令如 /env set TOKEN=xxx），目录权限 0700
func (h *HistoryStore) Record(cmd string, sessionID string) error {
	if cmd == "" {
		return nil
	}
	entry := HistoryEntry{
		Command:   cmd,
		Timestamp: time.Now(),
		SessionID: sessionID,
	}
	h.mu.Lock()
	// 内存追加 + FIFO 淘汰
	h.entries = append(h.entries, entry)
	if len(h.entries) > h.maxEntries {
		h.entries = h.entries[len(h.entries)-h.maxEntries:]
	}
	h.mu.Unlock()
	// 文件追加（同步）
	if h.filePath == "" {
		return nil
	}
	return h.appendToFile(entry)
}

// Query 查询历史，支持前缀过滤与数量限制（M09-01 改造，）
// prefix 为空时返回全部；limit<=0 时用默认值 100
// 从最近往前扫描（最近优先）
func (h *HistoryStore) Query(prefix string, limit int) []HistoryEntry {
	if limit <= 0 {
		limit = 100
	}
	h.mu.RLock()
	defer h.mu.RUnlock()
	var result []HistoryEntry
	// 从后往前扫描（最近优先）
	for i := len(h.entries) - 1; i >= 0 && len(result) < limit; i-- {
		if prefix == "" || hasCommandPrefix(h.entries[i].Command, prefix) {
			result = append(result, h.entries[i])
		}
	}
	return result
}

// Reload 实现 hook.Reloadable 接口，被 HotReloader 调用重新加载文件（C14 复用承诺）
// 失败时保留旧内存缓存 + 返回错误
func (h *HistoryStore) Reload(path string) error {
	if path == "" {
		return fmt.Errorf("reload: path is empty")
	}
	newEntries, err := readHistoryFile(path, h.maxEntries)
	if err != nil {
		return fmt.Errorf("reload history from %s: %w", path, err)
	}
	h.mu.Lock()
	h.entries = newEntries
	h.filePath = path
	h.mu.Unlock()
	return nil
}

// loadFromFile 从文件加载历史到内存（启动期调用，失败返回 error 但构造函数忽略）
func (h *HistoryStore) loadFromFile() error {
	entries, err := readHistoryFile(h.filePath, h.maxEntries)
	if err != nil {
		return err
	}
	h.mu.Lock()
	h.entries = entries
	h.mu.Unlock()
	return nil
}

// appendToFile 追加一条记录到 jsonl 文件（O_APPEND，权限 0600）
func (h *HistoryStore) appendToFile(entry HistoryEntry) error {
	data, err := json.Marshal(entry)
	if err != nil {
		return fmt.Errorf("marshal history entry: %w", err)
	}
	data = append(data, '\n')
	if err := os.MkdirAll(filepath.Dir(h.filePath), 0700); err != nil {
		return fmt.Errorf("mkdir for history file: %w", err)
	}
	// OPT-05：文件权限 0600（命令历史可能含敏感信息如 /env set TOKEN=xxx）
	f, err := os.OpenFile(h.filePath, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0600)
	if err != nil {
		return fmt.Errorf("open history file: %w", err)
	}
	defer f.Close()
	if _, err := f.Write(data); err != nil {
		return fmt.Errorf("write history file: %w", err)
	}
	return nil
}

// readHistoryFile 读取 jsonl 文件到切片（保留最近 maxEntries 条）
// 文件不存在视为空历史（返回空切片 + nil error）
// 损坏行跳过（降级，不中断整体加载）
func readHistoryFile(path string, maxEntries int) ([]HistoryEntry, error) {
	f, err := os.Open(path)
	if err != nil {
		if os.IsNotExist(err) {
			return []HistoryEntry{}, nil // 文件不存在视为空历史
		}
		return nil, err
	}
	defer f.Close()
	var entries []HistoryEntry
	scanner := bufio.NewScanner(f)
	scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024) // 1MB buffer
	for scanner.Scan() {
		line := scanner.Bytes()
		if len(line) == 0 {
			continue
		}
		var entry HistoryEntry
		if err := json.Unmarshal(line, &entry); err != nil {
			continue // 跳过损坏行（降级，不中断）
		}
		entries = append(entries, entry)
	}
	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("scan history file: %w", err)
	}
	// 保留最近 maxEntries 条
	if len(entries) > maxEntries {
		entries = entries[len(entries)-maxEntries:]
	}
	return entries, nil
}

// hasCommandPrefix 检查命令是否以指定前缀开头（不区分大小写）
// 用于 Query 前缀过滤，如 prefix="/co" 匹配 "/commit" "/config" 等
func hasCommandPrefix(cmd, prefix string) bool {
	// 去掉 / 前缀后比较（用户可能输入 /co 或 co）
	cmdClean := strings.TrimPrefix(cmd, "/")
	prefixClean := strings.TrimPrefix(prefix, "/")
	if len(cmdClean) < len(prefixClean) {
		return false
	}
	return strings.EqualFold(cmdClean[:len(prefixClean)], prefixClean)
}
