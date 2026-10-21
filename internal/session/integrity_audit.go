// integrity_audit.go — S18/G2 会话完整性审计事件（append-only JSONL）。
//
// 背景（I9 审计不可篡改）：
// A8 改判前（S18 早先「选项 C」）Load 遇损坏行保持 fail-open，用审计事件保证
// 「篡改中部一行」不被静默丢弃；**2026-09-14 19:45 拍板改判为 fail-closed T2**
// （见集中拍板_决策表 §2 A8 行）：拒绝加载 + 隔离损坏文件 + 显著告警。
// 「可见 + 审计」从主语义降为**附加项**——本文件因此继续存在，并新增
// QuarantinePath 字段记录隔离落点，使拒绝动作同样留下不可否认的痕迹。
//
// 落盘位置：<sessionDir>/audit/integrity.jsonl
//   - 放在 audit/ 子目录：List() 只扫描顶层 *.jsonl 且跳过目录，不会误把审计当日志。
//   - 权限：目录 0700、文件 0600（与全仓审计/会话落盘约定一致）。
//   - 写入：O_APPEND|O_CREATE|O_WRONLY（append-only，永不就地改写历史）。
//
// 降级：审计写入失败仅 log warn，绝不返回 error、绝不改变加载结论
// （审计是旁路可观测性，不是主流程门）。
package session

import (
	"encoding/json"
	"log"
	"os"
	"path/filepath"
	"sync"
	"time"
)

// integrityAuditFileName 审计日志文件名（位于 <sessionDir>/audit/ 下）。
const integrityAuditFileName = "integrity.jsonl"

// IntegrityAuditEventSessionCorrupt 是「会话文件存在损坏/不可解析行」的事件类型。
const IntegrityAuditEventSessionCorrupt = "session_corrupt_lines"

// IntegrityAuditEvent 一条会话完整性审计事件（JSONL 一行）。
type IntegrityAuditEvent struct {
	Timestamp    string `json:"timestamp"`     // RFC3339
	Event        string `json:"event"`         // 事件类型（见 IntegrityAuditEvent* 常量）
	SessionID    string `json:"session_id"`    // 受影响会话 ID
	CorruptLines []int  `json:"corrupt_lines"` // 损坏行号（1-based，按发现顺序）
	SkippedCount int    `json:"skipped_count"` // 被跳过的损坏行数
	// QuarantinePath 是 A8（fail-closed T2）改判后新增：损坏文件被隔离到的路径。
	// 非空即证明本次不是「跳过损坏行继续用」，而是「拒绝加载 + 隔离」。
	QuarantinePath string `json:"quarantine_path,omitempty"`
	Detail         string `json:"detail,omitempty"`
}

// storeAuditMu 串行化审计写入。
// 注意：Load 在 Store.mu 保护之外运行（Load 不加 s.mu），故审计写入需要独立互斥，
// 避免同进程内并发 Load 交错写（跨进程由 O_APPEND 原子追加兜底）。
var storeAuditMu sync.Mutex

// integrityAuditDir 返回审计目录路径（<dir>/audit）。
func (s *Store) integrityAuditDir() string {
	return filepath.Join(s.dir, "audit")
}

// IntegrityAuditPath 返回完整性审计日志的完整路径（供测试与运维读取）。
func (s *Store) IntegrityAuditPath() string {
	return filepath.Join(s.integrityAuditDir(), integrityAuditFileName)
}

// appendIntegrityAudit 以 append-only 方式追加一条审计事件。
// 最佳努力语义：失败仅 log warn，不返回 error（不阻断调用方）。
func (s *Store) appendIntegrityAudit(ev IntegrityAuditEvent) {
	if s == nil || s.dir == "" {
		return
	}
	if ev.Timestamp == "" {
		ev.Timestamp = time.Now().Format(time.RFC3339)
	}
	data, err := json.Marshal(ev)
	if err != nil {
		log.Printf("[session] integrity audit marshal failed: %v", err)
		return
	}
	data = append(data, '\n')

	storeAuditMu.Lock()
	defer storeAuditMu.Unlock()

	if err := os.MkdirAll(s.integrityAuditDir(), 0o700); err != nil {
		log.Printf("[session] integrity audit mkdir failed: %v", err)
		return
	}
	f, err := os.OpenFile(s.IntegrityAuditPath(), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		log.Printf("[session] integrity audit open failed: %v", err)
		return
	}
	defer f.Close()
	if _, err := f.Write(data); err != nil {
		log.Printf("[session] integrity audit write failed: %v", err)
	}
}
