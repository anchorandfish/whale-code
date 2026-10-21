// Package daemon — M24-05 Notify history 持久化（SNAPSHOT）
//
// 本文件实现通知历史的持久化存储与查询，替代纯内存 EventBus 的事件丢失风险。
// 对标 systemd journal 持久化日志与 docker events 持久化。
//
// 设计要点（候选 A 选定）：
// - 接口签名：NotifyHistory 提供 Append/Query/Cleanup/Save/Load
// - 结构体字段：NotifyEntry 含 ID/Type/Payload/Timestamp
// - 分层权责：本文件只做"持久化+查询"，事件分发由 EventBus
// - 降级策略：Save 失败仅记录日志，不阻塞 Append
// - TTL规则：默认保留 7 天，超期由 Cleanup 删除
//
// SNAPSHOT-1：NotifyEntry 结构定义
// SNAPSHOT-2：NotifyHistory 内存索引 + 持久化
// SNAPSHOT-3：Append 追加通知
// SNAPSHOT-4：Query 按条件查询（类型/时间范围）
// SNAPSHOT-5：Cleanup 清理过期通知
// SNAPSHOT-6：Save/Load JSONL 持久化往返
package daemon

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"
)

// DefaultNotifyTTL 是默认通知保留时间（7 天）。
const DefaultNotifyTTL = 7 * 24 * time.Hour

// DefaultMaxNotifyEntries 是默认最大通知条目数（10000，防内存膨胀）。
const DefaultMaxNotifyEntries = 10000

// NotifyEntry 是单条通知记录（SNAPSHOT-1）。
type NotifyEntry struct {
	ID        string      `json:"id"`        // 唯一 ID（UUID 或自增）
	Type      EventType   `json:"type"`      // 事件类型
	Payload   interface{} `json:"payload"`   // 事件载荷
	Timestamp int64       `json:"timestamp"` // 毫秒时间戳
	Source    string      `json:"source"`    // 来源（如 "worker-1"）
}

// NotifyQuery 是通知查询条件（SNAPSHOT-2）。
type NotifyQuery struct {
	Type      EventType // 按类型过滤（空=所有）
	StartTime int64     // 起始时间戳（0=不限）
	EndTime   int64     // 结束时间戳（0=不限）
	Limit     int       // 最大返回条数（0=不限）
}

// NotifyHistory 是通知历史的持久化存储（SNAPSHOT-3）。
//
// 行为：
// - 内存索引 + JSONL 持久化
// - Append 时同步写文件（best-effort）
// - 超过 MaxEntries 时淘汰最旧条目
// - TTL 过期条目由 Cleanup 删除
//
// 线程安全：所有方法加锁保护。
type NotifyHistory struct {
	mu          sync.Mutex
	entries     []NotifyEntry
	persistPath string
	ttl         time.Duration
	maxEntries  int
	nextID      int
}

// NewNotifyHistory 创建通知历史存储（SNAPSHOT-4）。
//
// 参数：
// - persistPath: 持久化文件路径（空=纯内存，不持久化）
func NewNotifyHistory(persistPath string) *NotifyHistory {
	return &NotifyHistory{
		entries:     make([]NotifyEntry, 0, 256),
		persistPath: persistPath,
		ttl:         DefaultNotifyTTL,
		maxEntries:  DefaultMaxNotifyEntries,
		nextID:      1,
	}
}

// SetTTL 设置通知保留时间（SNAPSHOT-5）。
func (h *NotifyHistory) SetTTL(ttl time.Duration) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.ttl = ttl
}

// SetMaxEntries 设置最大条目数（SNAPSHOT-6）。
func (h *NotifyHistory) SetMaxEntries(max int) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.maxEntries = max
}

// Append 追加一条通知（SNAPSHOT-7）。
//
// 行为：
// - 自动生成 ID（如果为空）
// - 自动填充 Timestamp（如果为 0）
// - 超过 MaxEntries 时淘汰最旧条目
// - 同步写入持久化文件（best-effort，失败仅记录不影响内存）
func (h *NotifyHistory) Append(entry NotifyEntry) error {
	h.mu.Lock()
	defer h.mu.Unlock()

	if entry.ID == "" {
		entry.ID = fmt.Sprintf("notify-%d", h.nextID)
		h.nextID++
	}
	if entry.Timestamp == 0 {
		entry.Timestamp = time.Now().UnixMilli()
	}

	h.entries = append(h.entries, entry)

	// 超过最大条目数，淘汰最旧
	if len(h.entries) > h.maxEntries {
		excess := len(h.entries) - h.maxEntries
		h.entries = h.entries[excess:]
	}

	// 持久化（best-effort）
	if h.persistPath != "" {
		return h.appendToFileLocked(entry)
	}
	return nil
}

// Query 按条件查询通知（SNAPSHOT-8）。
//
// 返回符合条件的通知列表（按时间降序，最新在前）。
func (h *NotifyHistory) Query(q NotifyQuery) []NotifyEntry {
	h.mu.Lock()
	defer h.mu.Unlock()

	var result []NotifyEntry
	for i := len(h.entries) - 1; i >= 0; i-- {
		e := h.entries[i]
		if q.Type != "" && e.Type != q.Type {
			continue
		}
		if q.StartTime > 0 && e.Timestamp < q.StartTime {
			continue
		}
		if q.EndTime > 0 && e.Timestamp > q.EndTime {
			continue
		}
		result = append(result, e)
		if q.Limit > 0 && len(result) >= q.Limit {
			break
		}
	}
	return result
}

// Cleanup 清理过期通知（SNAPSHOT-9）。
//
// 删除 Timestamp 早于 (now - ttl) 的条目。
// 返回清理的条目数。
func (h *NotifyHistory) Cleanup() int {
	h.mu.Lock()
	defer h.mu.Unlock()

	if h.ttl <= 0 {
		return 0
	}

	cutoff := time.Now().Add(-h.ttl).UnixMilli()
	var kept []NotifyEntry
	removed := 0
	for _, e := range h.entries {
		if e.Timestamp < cutoff {
			removed++
			continue
		}
		kept = append(kept, e)
	}
	h.entries = kept

	// 如果有清理，重写持久化文件
	if removed > 0 && h.persistPath != "" {
		_ = h.rewriteFileLocked()
	}

	return removed
}

// Save 将全部历史保存到文件（SNAPSHOT-10）。
//
// 全量保存（非追加），用于启动时加载后的定期快照。
func (h *NotifyHistory) Save() error {
	h.mu.Lock()
	defer h.mu.Unlock()

	if h.persistPath == "" {
		return nil
	}
	return h.rewriteFileLocked()
}

// rewriteFileLocked 全量重写持久化文件（调用方持锁）。
func (h *NotifyHistory) rewriteFileLocked() error {
	if err := os.MkdirAll(filepath.Dir(h.persistPath), 0700); err != nil {
		return fmt.Errorf("notify: mkdir: %w", err)
	}

	tmpPath := h.persistPath + ".tmp"
	f, err := os.OpenFile(tmpPath, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0600)
	if err != nil {
		return fmt.Errorf("notify: create tmp: %w", err)
	}

	writer := bufio.NewWriter(f)
	enc := json.NewEncoder(writer)
	for _, e := range h.entries {
		if err := enc.Encode(e); err != nil {
			f.Close()
			os.Remove(tmpPath)
			return fmt.Errorf("notify: encode: %w", err)
		}
	}
	if err := writer.Flush(); err != nil {
		f.Close()
		os.Remove(tmpPath)
		return fmt.Errorf("notify: flush: %w", err)
	}
	if err := f.Close(); err != nil {
		return fmt.Errorf("notify: close: %w", err)
	}

	if err := os.Rename(tmpPath, h.persistPath); err != nil {
		return fmt.Errorf("notify: rename: %w", err)
	}
	return nil
}

// appendToFileLocked 追加单条到持久化文件（调用方持锁， SNAPSHOT-11）。
func (h *NotifyHistory) appendToFileLocked(entry NotifyEntry) error {
	if err := os.MkdirAll(filepath.Dir(h.persistPath), 0700); err != nil {
		return fmt.Errorf("notify: mkdir: %w", err)
	}

	f, err := os.OpenFile(h.persistPath, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0600)
	if err != nil {
		return fmt.Errorf("notify: open append: %w", err)
	}
	defer f.Close()

	data, err := json.Marshal(entry)
	if err != nil {
		return fmt.Errorf("notify: marshal: %w", err)
	}
	data = append(data, '\n')

	if _, err := f.Write(data); err != nil {
		return fmt.Errorf("notify: write: %w", err)
	}
	return nil
}

// Load 从文件加载历史（SNAPSHOT-12）。
//
// 启动时调用，加载持久化的通知历史。
// 文件不存在时返回 nil（首次启动）。
func (h *NotifyHistory) Load() error {
	h.mu.Lock()
	defer h.mu.Unlock()

	if h.persistPath == "" {
		return nil
	}

	f, err := os.Open(h.persistPath)
	if err != nil {
		if os.IsNotExist(err) {
			return nil // 首次启动
		}
		return fmt.Errorf("notify: open: %w", err)
	}
	defer f.Close()

	var loaded []NotifyEntry
	scanner := bufio.NewScanner(f)
	scanner.Buffer(make([]byte, 1024*1024), 1024*1024) // 1MB buffer
	for scanner.Scan() {
		line := scanner.Bytes()
		if len(line) == 0 {
			continue
		}
		var entry NotifyEntry
		if err := json.Unmarshal(line, &entry); err != nil {
			continue // 跳过损坏行
		}
		loaded = append(loaded, entry)
	}
	if err := scanner.Err(); err != nil {
		return fmt.Errorf("notify: scan: %w", err)
	}

	h.entries = loaded
	// 更新 nextID 避免冲突
	maxID := 0
	for _, e := range loaded {
		var id int
		fmt.Sscanf(e.ID, "notify-%d", &id)
		if id > maxID {
			maxID = id
		}
	}
	h.nextID = maxID + 1

	return nil
}

// Size 返回当前历史条目数（SNAPSHOT-13）。
func (h *NotifyHistory) Size() int {
	h.mu.Lock()
	defer h.mu.Unlock()
	return len(h.entries)
}
