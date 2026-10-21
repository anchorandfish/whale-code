// trace.go — 模块 7 可观测性 TraceEntry + TraceWriter（H10 Phase 1 编排层 §3.5 / 总纲 §3.2）。
// REF-外部-Langfuse@官方文档+2026-07-15访问-trace/generation/score 三层分离；借鉴逻辑：trace 透明可观测；落地：trace.jsonl append-only
// 记录实际派发链深度；落地：TraceEntry.Depth 字段
// ≤3；落地：trace.jsonl 记录 Depth，超限直接拒绝派发
// SNAPSHOT: pkg=internal/orchestrator; type=TraceWriter; path=.whale/orchestrator/<task-id>/subtasks/<subtask-id>/trace.jsonl; format=append-only JSONL; fields={Timestamp,SubTaskID,ParentID,Depth,Event,AgentID,Stage,Model,ErrorType}; concurrency=sync.Mutex

package orchestrator

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/anchorandfish/whale-code/internal/orchrecord"
)

// TraceEvent 枚举常量（编排层 §3.5）。
const (
	TraceEventDispatch = orchrecord.TraceEventDispatch
	TraceEventStart    = orchrecord.TraceEventStart
	TraceEventEnd      = orchrecord.TraceEventEnd
	TraceEventFail     = orchrecord.TraceEventFail
)

// TraceEntry 执行轨迹条目（总纲 §3.2，编排层 §3.5）。
//
// ★ 类型权威已下沉到 internal/orchrecord：本处是类型别名，
//
//	保留 orchestrator 内部既有消费点的源码兼容，不另起一份定义。
type TraceEntry = orchrecord.TraceEntry

// TraceWriter 执行轨迹写入器（append-only JSONL，编排层 §2.4）。
type TraceWriter struct {
	path string
	mu   sync.Mutex
	file *os.File
}

// NewTraceWriter 构造 TraceWriter（以 O_APPEND|O_CREATE|O_WRONLY 打开文件，编排层 §3.5）。
// 路径穿越防护：path 含 ".." 时拒绝。
func NewTraceWriter(path string) (*TraceWriter, error) {
	if path == "" {
		return nil, fmt.Errorf("trace: path must not be empty")
	}
	// 路径穿越防护：在 Clean 前检查原始 path 是否含 ".."
	if strings.Contains(path, "..") {
		return nil, fmt.Errorf("trace: path contains '..': %s", path)
	}

	cleaned := filepath.Clean(path)

	// 确保目录存在
	if dir := filepath.Dir(cleaned); dir != "" && dir != "." {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return nil, fmt.Errorf("trace: mkdir %s: %w", dir, err)
		}
	}

	f, err := os.OpenFile(cleaned, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return nil, fmt.Errorf("trace: open %s: %w", cleaned, err)
	}

	return &TraceWriter{
		path: cleaned,
		file: f,
	}, nil
}

// Append 追加一条 TraceEntry 到 trace.jsonl（编排层 §3.5）。
func (w *TraceWriter) Append(entry TraceEntry) error {
	w.mu.Lock()
	defer w.mu.Unlock()

	if entry.Timestamp == 0 {
		entry.Timestamp = time.Now().Unix()
	}

	data, err := json.Marshal(entry)
	if err != nil {
		return fmt.Errorf("trace: marshal entry: %w", err)
	}
	data = append(data, '\n')

	if _, err := w.file.Write(data); err != nil {
		return fmt.Errorf("trace: write %s: %w", w.path, err)
	}
	return nil
}

// Close 关闭文件句柄。
func (w *TraceWriter) Close() error {
	w.mu.Lock()
	defer w.mu.Unlock()

	if w.file == nil {
		return nil
	}
	err := w.file.Close()
	w.file = nil
	return err
}

// ReadTrace 读取 trace.jsonl 所有条目（断点续传时用，编排层 §3.5）。
//
// ★ 薄转发到 internal/orchrecord.ReadTrace（读取契约唯一权威，）。
func ReadTrace(path string) ([]TraceEntry, error) {
	return orchrecord.ReadTrace(path)
}

// Path 返回 trace.jsonl 路径（仅用于诊断）。
func (w *TraceWriter) Path() string {
	return w.path
}
