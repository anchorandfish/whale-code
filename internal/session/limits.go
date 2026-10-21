// limits.go — G-B（O-B）E4：会话资源硬上限（I11 资源有界）。
//
// 决策依据：集中拍板_决策表与并行修复分组_2026-09-14.md §2 E4 行（S18-G5/G6）
// 「session 无消息数/大小/总数上限 ⇒ **补硬上限**（与 E1/E2 同族）」。
//
// 语义（与 E1 单轮输出上限 / E2 总轮次预算同族 = 「有界，触顶即止」）：
//   - 上限是**安全阀**，不是日常阈值：正常使用远不会触及；
//   - 触顶时**明确失败**（*SessionLimitError，且不落盘半条消息），
//     绝不静默截断对话历史——对话历史不可再生，静默截断比拒绝更危险；
//   - 三道上限各自独立，覆盖资源增长的三条通道：
//     ① 单会话消息数（内存/解析成本）② 单会话字节数（磁盘/上下文成本）
//     ③ 会话文件总数（目录项/磁盘总量）
//
// 关于「无年龄 GC」：E4 的拍板答案只要求**补硬上限**（未含按龄淘汰），
// 故本文件不实现年龄 GC；该 Gap 作为残余项登记在回执中，不在本轮擅自扩大范围。
package session

import (
	"fmt"
	"os"
	"strings"
)

// 会话资源上限默认值（安全阀量级；<=0 表示「使用默认」，不支持静默关闭）。
//
// 为什么是变量而非常量：它们是**策略数值**（受限环境/嵌入方可收紧），
// 且必须可被判别性测试在真实规模下触发（256MiB/10 万条无法在单测里构造）。
var (
	// MaxSessionMessages 单会话消息数上限（整会话写路径上生效）。
	MaxSessionMessages = 100_000
	// MaxSessionBytes 单会话 JSONL 字节上限（增量追加与整会话写路径上均生效）。
	MaxSessionBytes = 256 << 20
	// MaxSessions 会话文件总数上限（仅在「新建会话文件」时校验）。
	MaxSessions = 1000
)

// SessionLimitError 三道上限共用的类型化错误：调用方据此区分「资源触顶」
// 与其他 IO 失败（例如可提示用户删除旧会话后重试）。
type SessionLimitError struct {
	Kind      string // 见 SessionLimit* 常量
	SessionID string
	Limit     int64 // 触顶的上限值
	Actual    int64 // 触发时的实际值（或「预计将达」的值）
}

// 上限种类。
const (
	SessionLimitMessages = "messages" // 单会话消息数
	SessionLimitBytes    = "bytes"    // 单会话字节数
	SessionLimitCount    = "sessions" // 会话文件总数
)

// Error implements the error interface.
func (e *SessionLimitError) Error() string {
	switch e.Kind {
	case SessionLimitCount:
		return fmt.Sprintf("session store limit reached: %d session files (limit %d); delete old sessions before creating %q",
			e.Actual, e.Limit, e.SessionID)
	case SessionLimitMessages:
		return fmt.Sprintf("session %q would exceed the message limit (%d >= %d); start a new session or compact it",
			e.SessionID, e.Actual, e.Limit)
	default:
		return fmt.Sprintf("session %q would exceed the size limit (%d >= %d bytes); start a new session or compact it",
			e.SessionID, e.Actual, e.Limit)
	}
}

// effectiveLimit 归一化上限：<=0 视作「使用默认」，杜绝「配置成 0 即静默关闭上限」。
func effectiveLimit(v, def int) int {
	if v <= 0 {
		return def
	}
	return v
}

// checkByteLimit 校验「已有 size 字节 + 追加 delta 字节」是否越过单会话字节上限。
func checkByteLimit(sessionID string, size, delta int64) error {
	limit := int64(effectiveLimit(MaxSessionBytes, 256<<20))
	if size+delta > limit {
		return &SessionLimitError{
			Kind:      SessionLimitBytes,
			SessionID: sessionID,
			Limit:     limit,
			Actual:    size + delta,
		}
	}
	return nil
}

// checkMessageLimit 校验整会话消息数是否越过上限（在已持有全部消息的写路径上调用）。
func checkMessageLimit(sessionID string, count int) error {
	limit := int64(effectiveLimit(MaxSessionMessages, 100_000))
	if int64(count) > limit {
		return &SessionLimitError{
			Kind:      SessionLimitMessages,
			SessionID: sessionID,
			Limit:     limit,
			Actual:    int64(count),
		}
	}
	return nil
}

// enforceNewSessionAllowed 在「即将新建会话文件」时校验会话总数上限。
//
// 只在新建路径校验：上限不得把已有会话变成只读（用户必须仍能继续现有会话）。
// 若目标文件已存在则直接放行（增量追加不受总数上限影响）。
func (s *Store) enforceNewSessionAllowed(sessionID, path string, existed bool) error {
	if existed {
		return nil
	}
	limit := int64(effectiveLimit(MaxSessions, 1000))
	count := s.countSessionFiles()
	if int64(count) >= limit {
		return &SessionLimitError{
			Kind:      SessionLimitCount,
			SessionID: sessionID,
			Limit:     limit,
			Actual:    int64(count),
		}
	}
	return nil
}

// countSessionFiles 统计 store 目录下的会话文件数。
// 排除目录、归档 sidecar（<id>.archive.jsonl）、锁/临时/备份/元数据文件，
// 只计真正的会话主文件，避免把附属文件算成「一个会话」。
func (s *Store) countSessionFiles() int {
	entries, err := os.ReadDir(s.dir)
	if err != nil {
		return 0
	}
	var count int
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		name := e.Name()
		if !strings.HasSuffix(name, ".jsonl") || isArchiveFileName(name) {
			continue
		}
		if strings.HasSuffix(name, ".tmp") || strings.HasSuffix(name, ".bak") {
			continue
		}
		count++
	}
	return count
}
