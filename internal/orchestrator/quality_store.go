// quality_store.go — 桶 1 QualityStore 实现（append-only JSONL + 内存索引覆盖语义，地基层 §3.4）。
// 并发控制；落地：sync.Mutex 保护 append，index map[TaskID]int64 记录最新行偏移
// SNAPSHOT: file_format=JSONL append-only; index=map[task_id]offset; overwrite=取最后行; concurrency=sync.Mutex; reload=全量扫描重建

package orchestrator

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/anchorandfish/whale-code/internal/orchrecord"
)

// QualityStore 质量旁路记账存储，append-only JSONL + 内存索引覆盖语义（地基层 §3.4）。
// 并发安全：sync.Mutex 保护 append + index 更新；QueryByTaskID 读文件不持锁（地基层 §3.4）。
// 幂等性：运行期纯追加不删行，内存索引指向最新行号（JSONL 无 UNIQUE 靠应用层保证）。
//
// === Phase 5 v0.2 BASE 变更（三段式，沉淀）===
// 新增 3 方法扩展：OnAppend（注册 append 回调）+ ScanFailedReverseQueries（扫描反查失败记录）。
// 原方案优势：地基层 §3.4 QualityStore 仅 3 方法（Append/QueryByTaskID/Reload）+ ScanAll（v0.2 已加），简单
// 本次牺牲：QualityStore 新增 2 方法，接口不再最小
// 不得不改原因：
// - OnAppend：ProfileUpdater 增量更新需 append 事件回调（避免每条记录都全量重扫）
// - ScanFailedReverseQueries：RetryReverseQuery 需扫描反查失败记录（§4.5.3 致命 3 修复）
//
// 不改 Append/QueryByTaskID/Reload/ScanAll 现有方法签名（BASE 不破坏）。
type QualityStore struct {
	path  string           // .whale/orchestrator/quality.jsonl 绝对路径
	mu    sync.Mutex       // 保护并发 append（02 §2.7.3 并发控制）
	index map[string]int64 // TaskID → 最新行字节偏移（启动时全量扫描构建，遇重复取最后行）
	// Phase 5 ：append 回调列表（OnAppend 注册，Append 时触发）
	onAppendCallbacks []func(QualityRecord)
}

// NewQualityStore 创建 QualityStore，path 为 quality.jsonl 绝对路径。
// 不立即创建文件，首次 Append 时由 O_CREATE 自动创建。调用方可选调 Reload() 加载已有数据。
func NewQualityStore(path string) (*QualityStore, error) {
	// 路径穿越防护（参考 N4 filehistory safePath 三步法）
	cleaned := filepath.Clean(path)
	if strings.Contains(cleaned, "..") {
		return nil, fmt.Errorf("quality_store: path contains '..': %s", path)
	}
	return &QualityStore{
		path:  cleaned,
		index: make(map[string]int64),
	}, nil
}

// Append 追加一条质量记录到 JSONL 文件，并更新内存索引（地基层 §3.4）。
// 加锁 → 校验 TaskID 非空 → JSON marshal → O_APPEND|O_CREATE|O_WRONLY 打开 → 写入一行 + \n → 记录字节偏移 → 解锁。
// Phase 5 ：Append 完成后锁外触发 OnAppend 回调（防回调内再调 QualityStore 致死锁）。
func (q *QualityStore) Append(rec QualityRecord) error {
	if rec.TaskID == "" {
		return fmt.Errorf("quality_store: TaskID must not be empty")
	}

	data, err := json.Marshal(rec)
	if err != nil {
		return fmt.Errorf("quality_store: marshal record: %w", err)
	}
	data = append(data, '\n')

	// 持锁段：写文件 + 更新索引 + 拷贝回调列表
	callbacks, err := q.appendLocked(rec, data)
	if err != nil {
		return err
	}

	// 锁外执行回调（防回调内再调 QualityStore 致死锁）
	for _, cb := range callbacks {
		if cb != nil {
			cb(rec)
		}
	}
	return nil
}

// appendLocked 持锁段：写文件 + 更新索引 + 拷贝回调列表（Phase 5 拆分）。
// 返回回调列表供锁外执行，避免回调内再调 QualityStore 致死锁。
func (q *QualityStore) appendLocked(rec QualityRecord, data []byte) ([]func(QualityRecord), error) {
	q.mu.Lock()
	defer q.mu.Unlock()

	// 确保目录存在
	if dir := filepath.Dir(q.path); dir != "" {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return nil, fmt.Errorf("quality_store: mkdir %s: %w", dir, err)
		}
	}

	// O_APPEND 模式打开：每次写入追加到文件末尾，多进程/多 goroutine 安全（POSIX 原子性）
	f, err := os.OpenFile(q.path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return nil, fmt.Errorf("quality_store: open %s: %w", q.path, err)
	}
	defer f.Close()

	// 获取写入前文件大小（即新行的起始偏移）
	info, err := f.Stat()
	if err != nil {
		return nil, fmt.Errorf("quality_store: stat %s: %w", q.path, err)
	}
	offset := info.Size()

	if _, err := f.Write(data); err != nil {
		return nil, fmt.Errorf("quality_store: write %s: %w", q.path, err)
	}

	// 更新内存索引：同 TaskID 多次追加，后写覆盖先写（取最后行）
	q.index[rec.TaskID] = offset

	// 拷贝回调列表（锁内拷贝，锁外执行）
	callbacks := make([]func(QualityRecord), len(q.onAppendCallbacks))
	copy(callbacks, q.onAppendCallbacks)
	return callbacks, nil
}

// QueryByTaskID 按 TaskID 查询最新一条记录（地基层 §3.4）。
// 读 index 偏移 → os.Open 只读（不持锁）→ Seek 到偏移 → bufio.Scanner 读一行 → json.Unmarshal。
func (q *QualityStore) QueryByTaskID(taskID string) (*QualityRecord, error) {
	if taskID == "" {
		return nil, fmt.Errorf("quality_store: TaskID must not be empty")
	}

	q.mu.Lock()
	offset, ok := q.index[taskID]
	q.mu.Unlock()
	if !ok {
		return nil, fmt.Errorf("quality_store: task_id %s not found", taskID)
	}

	f, err := os.Open(q.path)
	if err != nil {
		return nil, fmt.Errorf("quality_store: open %s: %w", q.path, err)
	}
	defer f.Close()

	if _, err := f.Seek(offset, 0); err != nil {
		return nil, fmt.Errorf("quality_store: seek to %d: %w", offset, err)
	}

	scanner := bufio.NewScanner(f)
	// 单行可能较大，扩容 buffer 防止 bufio.ErrTooLong
	scanner.Buffer(make([]byte, 64*1024), 4*1024*1024)
	if !scanner.Scan() {
		if err := scanner.Err(); err != nil {
			return nil, fmt.Errorf("quality_store: scan: %w", err)
		}
		return nil, fmt.Errorf("quality_store: no line at offset %d", offset)
	}

	var rec QualityRecord
	if err := json.Unmarshal(scanner.Bytes(), &rec); err != nil {
		return nil, fmt.Errorf("quality_store: unmarshal: %w", err)
	}
	return &rec, nil
}

// Reload 全量扫描 JSONL 文件重建内存索引（启动时调用，地基层 §3.4）。
// 遇同 TaskID 后写入覆盖先写入（取最后一行），保证 index 指向最新记录。
func (q *QualityStore) Reload() error {
	q.mu.Lock()
	defer q.mu.Unlock()

	// 清空索引
	q.index = make(map[string]int64)

	f, err := os.Open(q.path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil // 文件不存在视为空 store，不报错
		}
		return fmt.Errorf("quality_store: reload open %s: %w", q.path, err)
	}
	defer f.Close()

	// 按字节偏移逐行扫描
	reader := bufio.NewReader(f)
	var offset int64
	for {
		line, err := reader.ReadBytes('\n')
		if len(line) > 0 {
			// 提取 task_id 字段（轻量解析，避免每行完整 unmarshal）
			var probe struct {
				TaskID string `json:"task_id"`
			}
			if jerr := json.Unmarshal(line, &probe); jerr == nil && probe.TaskID != "" {
				q.index[probe.TaskID] = offset
			}
			offset += int64(len(line))
		}
		if err != nil {
			break // EOF 或错误，结束扫描
		}
	}
	return nil
}

// Count 返回当前索引中不同 TaskID 的数量（仅用于测试与诊断）。
func (q *QualityStore) Count() int {
	q.mu.Lock()
	defer q.mu.Unlock()
	return len(q.index)
}

// ScanAll 全量扫描 JSONL 文件返回所有最新记录（去重，每 TaskID 仅返回最后一行）。
// ProfileStore.Reload 用此方法全量重建画像表（地基层 §3.5）。
// 注意：返回顺序按 TaskID 首次出现顺序，不保证时间序；调用方需自行排序。
//
// ★ 薄转发到 internal/orchrecord.ScanAllQuality（读取契约唯一权威，）。
func (q *QualityStore) ScanAll() ([]QualityRecord, error) {
	return orchrecord.ScanAllQuality(q.path)
}

// Path 返回 quality.jsonl 路径（仅用于诊断与 ProfileStore.Reload 间接访问）。
func (q *QualityStore) Path() string {
	return q.path
}

// nowISO8601 返回当前时间的 ISO 8601 字符串（便于上层填入 QualityRecord.Timestamp）。
func nowISO8601() string {
	return time.Now().UTC().Format(time.RFC3339Nano)
}

// OnAppend 注册 append 事件回调（Phase 5 BASE 变更）。
// 落地 §4.5.1 ProfileUpdater 增量更新：每次 Append 后回调通知 ProfileUpdater 增量更新画像表。
// 回调在锁外执行（防回调内再调 QualityStore 致死锁），回调失败不阻塞 Append。
// 返回 error 仅为接口一致，当前永远返回 nil（注册不会失败）。
func (q *QualityStore) OnAppend(fn func(QualityRecord)) error {
	if fn == nil {
		return fmt.Errorf("quality_store: OnAppend callback must not be nil")
	}
	q.mu.Lock()
	defer q.mu.Unlock()
	q.onAppendCallbacks = append(q.onAppendCallbacks, fn)
	return nil
}

// ScanFailedReverseQueries 扫描反查失败的记录（Phase 5 BASE 变更）。
// 落地 §4.5.3 RetryReverseQuery：找出所有 QualityRecord 中 metrics_correlation 反查 CostRecord 失败的记录。
// 由于 quality.jsonl 不直接存储"反查失败"标志（旁路记账），本方法采用启发式：
// - threshold > 0：返回 SampleCount <= threshold 的记录（旧记录可能反查失败，需重试）
// - threshold == 0：返回所有记录（全量重试，慎用）
// - threshold < 0：返回空（禁用扫描）
//
// 实际反查失败的判定由调用方（ProfileUpdater.RetryReverseQuery）执行 ReverseQueryCost 后判定。
func (q *QualityStore) ScanFailedReverseQueries(threshold int) ([]QualityRecord, error) {
	records, err := q.ScanAll()
	if err != nil {
		return nil, fmt.Errorf("quality_store: ScanFailedReverseQueries: %w", err)
	}
	if threshold < 0 {
		return nil, nil
	}
	var failed []QualityRecord
	for _, rec := range records {
		// 启发式：metrics_correlation.agent_id 为空的记录视为反查失败（无关联 CostRecord）
		// 或 timestamp 为空（旧记录无关联键）
		if rec.MetricsCorrelation.AgentID == "" || rec.MetricsCorrelation.Timestamp == "" {
			failed = append(failed, rec)
			continue
		}
		// threshold > 0：旧记录（age 超过 threshold 天）视为可能反查失败
		if threshold > 0 && rec.Timestamp != "" {
			ts, perr := time.Parse(time.RFC3339, rec.Timestamp)
			if perr == nil {
				age := time.Since(ts)
				if age > time.Duration(threshold)*24*time.Hour {
					failed = append(failed, rec)
				}
			}
		}
	}
	return failed, nil
}
