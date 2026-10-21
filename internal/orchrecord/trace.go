// Package orchrecord 是「编排 trace/quality 落盘记录格式契约」的唯一权威承载者。
//
// 为什么它必须独立成零依赖叶子包（/ / CP-1 的根因修复）：
//
//	`internal/memory/compact.go` 需要读取编排层（orchestrator）落盘的
//	trace.jsonl / quality.jsonl，于是它 import 了 `internal/orchestrator`。
//	但 memory（H11/N27，低层）反向 import orchestrator（H10，高层）——
//	这是依赖方向倒置，且消费者自持了记录格式的「版本常量 + 字段名」副本，
//	生产者（orchestrator）从不写 schema marker ⇒ 设计好的 drift 探测器在生产中恒不响。
//
// 修法（第一性原理，对齐 同类 CLI `protocol` crate 做法）：
//
//	把「记录类型（json tag 即落盘格式权威）+ 读取契约 + schema 版本声明」
//	整体下沉到本叶子包。orchestrator 与 memory 都只 import 本包 ⇒
//	反向依赖消失，格式权威唯一，且本包零内部依赖（只 import stdlib），
//	不可能与任何业务层形成环。
//
// ⚠️ 边界（必须写下来，否则下一个人会高估它）：
// - 本包只承载「记录类型 + 读取 + 版本声明」这一层格式契约，
// 不承载 orchestrator 的写入端（QualityStore.Append / OnAppend / Reload /
// QueryByTaskID / ScanFailedReverseQueries 等）——那些是生产者的行为，
// 留在 orchestrator 包内，与本契约无涉。
// - 本包不得 import 任何 `github.com/anchorandfish/whale-code/internal/...` 包。
// 违反此不变量会让「下沉消除反向依赖」的目标落空（见 memory 包的 import 方向门）。
package orchrecord

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// TraceEvent 枚举常量（编排层 §3.5）。
const (
	TraceEventDispatch = "dispatch"
	TraceEventStart    = "start"
	TraceEventEnd      = "end"
	TraceEventFail     = "fail"
)

// TraceEntry 执行轨迹条目（总纲 §3.2，编排层 §3.5）。
//
// json tag 即 trace.jsonl 的落盘格式权威：任何对字段/语义的修改，
// 都只允许在本包发生（这正是本包存在的意义）。
type TraceEntry struct {
	Timestamp int64  `json:"timestamp"`
	SubTaskID string `json:"subtask_id"`
	ParentID  string `json:"parent_id,omitempty"`
	Depth     int    `json:"depth"`
	Event     string `json:"event"`
	AgentID   string `json:"agent_id"`
	Stage     string `json:"stage"`
	Model     string `json:"model"`
	ErrorType string `json:"error_type,omitempty"`
	// PatternID 命中的经验库 Pattern ID（B-P0-3 修复：EvolutionLoop.Run 从 trace 提取命中 Pattern 的 ID 用于 Validate）。
	// 空表示该 trace 条目未命中经验库 Pattern。omitempty 向后兼容。
	PatternID string `json:"pattern_id,omitempty"`
}

// ReadTrace 读取 trace.jsonl 所有条目（断点续传时用，编排层 §3.5）。
// 这是「读取契约」的权威实现：memory 侧复用它与 orchestrator 侧保持严格一致，
// 不再自持一套解析逻辑。
func ReadTrace(path string) ([]TraceEntry, error) {
	if strings.Contains(path, "..") {
		return nil, fmt.Errorf("trace: path contains '..': %s", path)
	}

	cleaned := filepath.Clean(path)
	f, err := os.Open(cleaned)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("trace: open %s: %w", cleaned, err)
	}
	defer f.Close()

	var entries []TraceEntry
	scanner := bufio.NewScanner(f)
	scanner.Buffer(make([]byte, 64*1024), 4*1024*1024)
	for scanner.Scan() {
		line := scanner.Bytes()
		if len(line) == 0 {
			continue
		}
		var entry TraceEntry
		if err := json.Unmarshal(line, &entry); err != nil {
			return nil, fmt.Errorf("trace: unmarshal: %w", err)
		}
		entries = append(entries, entry)
	}
	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("trace: scan: %w", err)
	}
	return entries, nil
}
