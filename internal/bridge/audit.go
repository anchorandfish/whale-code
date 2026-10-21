// Package bridge — M21-05 审计日志完整 trail（SNAPSHOT）
//
// 本文件补全 SetToken/Verify/Refresh 三个关键操作的完整审计 trail。
// 对标同类 CLI 的事件日志（官方文档）。
//
// 设计要点（候选 A 选定）：
// - 接口签名：AuditLogger 接口抽象，支持多后端（文件/内存/远程）
// - 结构体字段：AuditEntry 含 Timestamp/Operation/TokenID/Result/Detail
// - 分层权责：本文件只做"记录"，鉴权由 jwt.go，重放检测由 jti_cache.go
// - 降级策略：审计写入失败仅记录日志，不阻断主流程
// - TTL规则：审计日志保留 30 天（auditRetentionDays），过期清理
package bridge

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/anchorandfish/whale-code/internal/util"
)

// auditRetentionDays 是审计日志保留天数（30 天，对标企业审计合规要求）。
const auditRetentionDays = 30

// AuditOperation 是审计操作类型（SNAPSHOT-1）。
type AuditOperation string

const (
	// AuditOpSetToken 是 SetToken 操作（设置/更新 token）。
	AuditOpSetToken AuditOperation = "set_token"
	// AuditOpVerify 是 Verify 操作（验证 JWT）。
	AuditOpVerify AuditOperation = "verify"
	// AuditOpRefresh 是 Refresh 操作（刷新 token）。
	AuditOpRefresh AuditOperation = "refresh"
	// AuditOpReconnect 是 Reconnect 操作（重连）。
	AuditOpReconnect AuditOperation = "reconnect"
	// AuditOpRevoke 是 Revoke 操作（吊销 token）。
	AuditOpRevoke AuditOperation = "revoke"

	// AuditOpToolApproval 是「工具审批决策留痕」操作（delta F，2026-09-14）。
	//
	// 与上面五个 token 操作的**关键区别**：本条目的字段由 ApprovalRecord 填充
	// （见 approval_audit.go），走的是 Decision/DecisionSource/Origin 三个新字段，
	// **不复用** TokenID/TokenMask 语义（避免"驴唇不对马嘴"的硬塞）。
	AuditOpToolApproval AuditOperation = "tool_approval"

	// AuditOpToolApprovalRequest 是「工具审批请求留痕」操作。
	//
	// 与 AuditOpToolApproval（决策侧）配对：同一次确认的两条经 ToolInputHash 关联，
	// decision.timestamp − request.timestamp = 该次确认的 pending 时长 ——
	// 用于判别「用户晚点批准」与「弹窗延迟浮现」（后者是真 UI 缺陷；2026-10-07
	// 真机 T-5/T-5b 两次 12~42 分钟空档无法归因，正是缺了 request 侧时刻）。
	// 条目语义：Decision="requested"、Actor="system"（此时还没有人的决定）。
	AuditOpToolApprovalRequest AuditOperation = "tool_approval_request"
)

// AuditResult 是审计操作结果（SNAPSHOT-2）。
type AuditResult string

const (
	// AuditResultSuccess 表示操作成功。
	AuditResultSuccess AuditResult = "success"
	// AuditResultFailure 表示操作失败。
	AuditResultFailure AuditResult = "failure"
	// AuditResultDenied 表示操作被拒绝（如重放检测）。
	AuditResultDenied AuditResult = "denied"
)

// AuditEntry 是审计日志条目（SNAPSHOT-3）。
//
// 字段说明（前 7 个为 既有 token 审计字段，语义保持不变）：
// - Timestamp: 操作时间（Unix 毫秒）
// - Operation: 操作类型（set_token/verify/refresh/reconnect/revoke/tool_approval）
// - TokenID: token 标识（jti 或 sub，不记录完整 token）
// - TokenMask: 脱敏后的 token（前4后4，中间 ***）
// - Result: 操作结果（success/failure/denied）
// - Detail: 详细信息（错误原因、IP 等，不记录敏感数据）
// - Source: 操作来源（local/remote/bridge）
//
// 后 10 个为 delta F（工具审批决策留痕，2026-09-14）新增，**token 审计不填**
// （全部 omitempty ⇒ 既有 JSONL 行不因新增字段而变化，jwt 侧零回归）：
// - SchemaVersion: 条目 schema 版本（审批条目=1，对齐业内既有实现 rollout-trace raw_event.rs）
// - Seq: 进程内单调序号（atomic 计数，**不声称**跨进程全局唯一 —— 多个 whale
// 进程 append 同一文件，声明全局唯一会变成谎言）
// - Actor: 决策执行者（"user"：留痕的是人的决定，不是系统的）
// - Origin: 交互路径（tui-subagent / tui-main / tui-elicitation / rpc）
// - Tool: 工具名（bash / write_file / …）
// - ToolInput: 脱敏后的入参预览（fail-closed，见 approval_audit.go redactToolInput）
// - ToolInputHash: 原始入参 sha256（"sha256:…"，不泄密、可用于关联同一次调用）
// - Decision: 决策类别（approved|approved_session|denied|cancelled|timed_out）
// - DecisionSource: 决策成因方（user|ctx-cancelled|timeout|slot-guard|no-channel）
// - SessionID: 会话标识（配合 Seq 定位"哪一次会话的第几个决策"）
// - RequestID: 产生该请求的 AI 回合标识 / MCP request id / pipeline requestID
// - McpServer: 仅 elicitation 填 —— 是**哪个 MCP server** 在要信息（I9 可还原性）
type AuditEntry struct {
	Timestamp int64          `json:"timestamp"`  // Unix 毫秒
	Operation AuditOperation `json:"operation"`  // 操作类型
	TokenID   string         `json:"token_id"`   // token 标识（jti/sub）
	TokenMask string         `json:"token_mask"` // 脱敏 token
	Result    AuditResult    `json:"result"`     // 操作结果
	Detail    string         `json:"detail"`     // 详细信息
	Source    string         `json:"source"`     // 操作来源

	// ↓↓↓ delta F 新增（token 审计留空）↓↓↓
	SchemaVersion  int    `json:"schema_version,omitempty"`
	Seq            int64  `json:"seq,omitempty"`
	Actor          string `json:"actor,omitempty"`
	Origin         string `json:"origin,omitempty"`
	Tool           string `json:"tool,omitempty"`
	ToolInput      string `json:"tool_input,omitempty"`
	ToolInputHash  string `json:"tool_input_hash,omitempty"`
	Decision       string `json:"decision,omitempty"`
	DecisionSource string `json:"decision_source,omitempty"`
	SessionID      string `json:"session_id,omitempty"`
	RequestID      string `json:"request_id,omitempty"`
	McpServer      string `json:"mcp_server,omitempty"`
}

// AuditLogger 是审计日志接口（SNAPSHOT-4）。
//
// 接口契约：
// - Log：记录审计条目
// - Query：按条件查询审计条目
// - Cleanup：清理过期审计条目
//
// 实现方：
// - fileAuditLogger：文件持久化实现（本文件实现）
// - memoryAuditLogger：内存实现（测试用）
type AuditLogger interface {
	// Log 记录审计条目。
	Log(entry *AuditEntry) error

	// Query 按 operation 查询审计条目。
	// limit <= 0 时返回全部。
	Query(operation AuditOperation, limit int) ([]*AuditEntry, error)

	// Cleanup 清理过期审计条目（超过 auditRetentionDays）。
	Cleanup() error
}

// fileAuditLogger 是文件持久化审计日志实现（候选 A）。
//
// 日志格式：JSONL（每行一条 JSON），便于流式读取与 grep。
// 文件路径：auditPath（如 ~/.whale/bridge/audit.jsonl）。
//
// S13-HG Q1=A 的落地（delta F，2026-09-14）—— 本结构体是 A 的**唯一落点**：
// - O_APPEND 追加（不覆盖历史）
// - 0600 文件 / 0700 目录
// - **写时 advisory lock**（跨进程，见 Log）；此前只有 sync.Mutex（仅进程内）
// - **Cleanup 不再 read→filter→rename 整文件重写**（那与 append-only 直接冲突）。
// 改为**整文件轮转**：写满/跨天时把当前文件改名归档，过期**整个归档文件**删除，
// 永不改写历史字节。
type fileAuditLogger struct {
	mu        sync.Mutex
	auditPath string

	// now 是时钟注入点（测试用）。nil ⇒ time.Now。
	// 轮转判定依赖"当前文件 mtime 是否还是今天"，注入时钟才能在测试里跨天。
	now func() time.Time
}

// clock 返回注入的时钟，未注入时退回 time.Now。
func (l *fileAuditLogger) clock() time.Time {
	if l.now != nil {
		return l.now()
	}
	return time.Now()
}

// auditRotateMaxBytes 是单个审计文件的大小上限，超过即轮转到新的归档文件。
const auditRotateMaxBytes = 8 << 20 // 8 MiB

// auditArchiveSuffix 是归档文件的扩展名前缀：<base>.YYYYMMDD.jsonl
const auditArchiveSuffix = ".jsonl"

// NewFileAuditLogger 创建文件审计日志实例（SNAPSHOT-5）。
//
// auditPath 为空时返回 nil（降级：无审计）。
func NewFileAuditLogger(auditPath string) AuditLogger {
	if auditPath == "" {
		return &memoryAuditLogger{}
	}
	return &fileAuditLogger{auditPath: auditPath}
}

// Log 记录审计条目到文件（SNAPSHOT-6）。
//
// 行为：
// - JSONL 格式追加写入（每行一条 JSON，O_APPEND）
// - 目录自动创建（0700）
// - **持跨进程 advisory lock 写入**（S13-HG Q1=A）：
// sync.Mutex 只挡得住同进程的 goroutine；多个 whale 进程 append 同一文件时
// 仍需 OS 级锁。锁文件走 util.WithFileLock（unix=flock / windows=LockFileEx）。
// - 写入失败返回 error（调用方降级为仅日志）
//
// 为什么锁文件是 **sidecar**（auditPath+".lock"）而不是审计文件本身：
// 轮转会 os.Rename 掉审计文件。若锁在被 rename 掉的那个 inode 上，另一个进程
// 可能在 rename **之前**就已 open 该 inode —— 它拿到锁后写入的是**归档文件**。
// 锁 sidecar + "加锁后再打开数据文件" 消除了这个窗口。
func (l *fileAuditLogger) Log(entry *AuditEntry) error {
	if entry == nil {
		return fmt.Errorf("audit: nil entry")
	}
	if entry.Timestamp == 0 {
		entry.Timestamp = l.clock().UnixMilli()
	}

	l.mu.Lock()
	defer l.mu.Unlock()

	// 先按 0700 建目录（必须先于 util.WithFileLock —— 后者用 0755 MkdirAll，
	// 若由它先建目录，C2 断言的 0700 目录权限就会漂成 0755）。
	if err := os.MkdirAll(filepath.Dir(l.auditPath), 0700); err != nil {
		return fmt.Errorf("audit: mkdir: %w", err)
	}

	data, err := json.Marshal(entry)
	if err != nil {
		return fmt.Errorf("audit: marshal: %w", err)
	}
	data = append(data, '\n')

	return util.WithFileLock(l.auditPath+".lock", func(*os.File) error {
		if err := l.rotateLocked(); err != nil {
			return err
		}
		f, err := os.OpenFile(l.auditPath, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0600)
		if err != nil {
			return fmt.Errorf("audit: open file: %w", err)
		}
		defer f.Close()

		if _, err := f.Write(data); err != nil {
			return fmt.Errorf("audit: write: %w", err)
		}
		return nil
	})
}

// rotateLocked 在需要时把当前审计文件整文件改名归档（调用方须已持 advisory lock
// 与 l.mu）。触发条件：文件大小超过 auditRotateMaxBytes，或其 mtime 不在今天。
//
// 这是 A 的派生要求：**永不** read→filter→rename 改写历史字节，只做整文件改名。
func (l *fileAuditLogger) rotateLocked() error {
	fi, err := os.Stat(l.auditPath)
	if err != nil {
		if os.IsNotExist(err) {
			return nil // 尚未创建 → 无需轮转
		}
		return fmt.Errorf("audit: stat for rotate: %w", err)
	}
	if fi.Size() == 0 {
		return nil
	}
	mtime := fi.ModTime()
	if fi.Size() < auditRotateMaxBytes && mtime.Format("20060102") == l.clock().Format("20060102") {
		return nil
	}
	return l.archiveLocked(mtime)
}

// archiveLocked 把 l.auditPath 改名成 <base>.<YYYYMMDD>.jsonl（同日重名时追加 -N）。
func (l *fileAuditLogger) archiveLocked(day time.Time) error {
	base := strings.TrimSuffix(l.auditPath, auditArchiveSuffix)
	stamp := day.Format("20060102")
	archive := fmt.Sprintf("%s.%s%s", base, stamp, auditArchiveSuffix)
	for i := 1; ; i++ {
		if _, err := os.Stat(archive); os.IsNotExist(err) {
			break
		}
		archive = fmt.Sprintf("%s.%s-%d%s", base, stamp, i, auditArchiveSuffix)
	}
	if err := os.Rename(l.auditPath, archive); err != nil {
		return fmt.Errorf("audit: rotate rename: %w", err)
	}
	return nil
}

// Query 按 operation 查询审计条目（SNAPSHOT-7）。
//
// behavior：
// - 全量读取文件，按 operation 过滤
// - limit <= 0 时返回全部匹配
// - 按时间倒序返回（最新在前）
func (l *fileAuditLogger) Query(operation AuditOperation, limit int) ([]*AuditEntry, error) {
	l.mu.Lock()
	defer l.mu.Unlock()

	data, err := os.ReadFile(l.auditPath)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("audit: read: %w", err)
	}

	var entries []*AuditEntry
	lines := splitAuditLines(string(data))
	for _, line := range lines {
		if line == "" {
			continue
		}
		var e AuditEntry
		if err := json.Unmarshal([]byte(line), &e); err != nil {
			continue
		}
		if operation != "" && e.Operation != operation {
			continue
		}
		entries = append(entries, &e)
	}

	// 按时间倒序（最新在前）
	for i, j := 0, len(entries)-1; i < j; i, j = i+1, j-1 {
		entries[i], entries[j] = entries[j], entries[i]
	}

	if limit > 0 && len(entries) > limit {
		entries = entries[:limit]
	}
	return entries, nil
}

// Cleanup 清理过期审计数据（SNAPSHOT-8 的 delta F 修订版）。
//
// ★ S13-HG Q1=A 的派生要求：**不得** read→filter→rename 整文件重写 —— 那会改写
// 历史字节，与 append-only 直接冲突。本实现改为**整文件粒度**的保留策略：
// - 当前文件若整体过期（mtime 早于 cutoff）→ 删除该文件
// - 归档文件 <base>.YYYYMMDD*.jsonl 若日期早于 cutoff → 整个文件删除
// - **永不**改写任何文件的字节内容
//
// ⚠️ 语义变化（有意，已在并行会话_协调_2026-09-14.md §1.11 记账）：
//
//	保留粒度由"逐条"变为"逐文件"。一条时间戳很旧、但今天才写入的记录会留在
//	今天的文件里 —— 这是 append-only 的必然代价（不读文件就无法按条目过滤），
//	且正是 S13 I9「不可变追加」要求的行为。
func (l *fileAuditLogger) Cleanup() error {
	l.mu.Lock()
	defer l.mu.Unlock()

	if err := os.MkdirAll(filepath.Dir(l.auditPath), 0700); err != nil {
		return fmt.Errorf("audit: mkdir for cleanup: %w", err)
	}
	cutoff := l.clock().Add(-auditRetentionDays * 24 * time.Hour)

	return util.WithFileLock(l.auditPath+".lock", func(*os.File) error {
		// 1) 当前文件整体过期 → 整个删除（不改写）
		if fi, err := os.Stat(l.auditPath); err == nil {
			if fi.ModTime().Before(cutoff) {
				if err := os.Remove(l.auditPath); err != nil {
					return fmt.Errorf("audit: remove expired active: %w", err)
				}
			}
		} else if !os.IsNotExist(err) {
			return fmt.Errorf("audit: stat for cleanup: %w", err)
		}

		// 2) 归档文件按日期整文件删除
		base := strings.TrimSuffix(l.auditPath, auditArchiveSuffix)
		matches, err := filepath.Glob(base + ".*" + auditArchiveSuffix)
		if err != nil {
			return fmt.Errorf("audit: glob archives: %w", err)
		}
		for _, arch := range matches {
			day, ok := auditArchiveDate(arch, base)
			if !ok {
				continue // 命名不符合本 logger 的归档规则 → 不碰（保守）
			}
			if day.Before(cutoff) {
				if err := os.Remove(arch); err != nil && !os.IsNotExist(err) {
					return fmt.Errorf("audit: remove expired archive %s: %w", arch, err)
				}
			}
		}
		return nil
	})
}

// auditArchiveDate 从归档文件名解析日期（<base>.<YYYYMMDD>[-N].jsonl）。
// 返回 (day, true) 表示解析成功；命名不符则 (zero, false)（调用方须跳过而非删除）。
func auditArchiveDate(archive, base string) (time.Time, bool) {
	rest := strings.TrimPrefix(archive, base+".")
	rest = strings.TrimSuffix(rest, auditArchiveSuffix)
	if rest == "" {
		return time.Time{}, false
	}
	if i := strings.IndexByte(rest, '-'); i >= 0 {
		rest = rest[:i]
	}
	if len(rest) != 8 {
		return time.Time{}, false
	}
	day, err := time.ParseInLocation("20060102", rest, time.Local)
	if err != nil {
		return time.Time{}, false
	}
	return day, true
}

// splitAuditLines 按换行符切分（内部工具函数）。
func splitAuditLines(s string) []string {
	var lines []string
	start := 0
	for i := 0; i < len(s); i++ {
		if s[i] == '\n' {
			line := s[start:i]
			if len(line) > 0 {
				lines = append(lines, line)
			}
			start = i + 1
		}
	}
	if start < len(s) {
		lines = append(lines, s[start:])
	}
	return lines
}

// memoryAuditLogger 是内存审计日志实现（测试用， SNAPSHOT-9）。
type memoryAuditLogger struct {
	mu      sync.Mutex
	entries []*AuditEntry
}

// Log 记录到内存。
func (m *memoryAuditLogger) Log(entry *AuditEntry) error {
	if entry == nil {
		return fmt.Errorf("audit: nil entry")
	}
	if entry.Timestamp == 0 {
		entry.Timestamp = time.Now().UnixMilli()
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	m.entries = append(m.entries, entry)
	return nil
}

// Query 从内存查询。
func (m *memoryAuditLogger) Query(operation AuditOperation, limit int) ([]*AuditEntry, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	var result []*AuditEntry
	for i := len(m.entries) - 1; i >= 0; i-- {
		e := m.entries[i]
		if operation != "" && e.Operation != operation {
			continue
		}
		result = append(result, e)
		if limit > 0 && len(result) >= limit {
			break
		}
	}
	return result, nil
}

// Cleanup 内存实现 no-op（测试数据不清理）。
func (m *memoryAuditLogger) Cleanup() error { return nil }

// LogAuditSuccess 是便捷函数：记录成功审计条目（SNAPSHOT-10）。
func LogAuditSuccess(logger AuditLogger, op AuditOperation, tokenID, tokenMask, source, detail string) {
	if logger == nil {
		return
	}
	_ = logger.Log(&AuditEntry{
		Operation: op,
		TokenID:   tokenID,
		TokenMask: tokenMask,
		Result:    AuditResultSuccess,
		Detail:    detail,
		Source:    source,
	})
}

// LogAuditFailure 是便捷函数：记录失败审计条目（SNAPSHOT-11）。
func LogAuditFailure(logger AuditLogger, op AuditOperation, tokenID, tokenMask, source, detail string) {
	if logger == nil {
		return
	}
	_ = logger.Log(&AuditEntry{
		Operation: op,
		TokenID:   tokenID,
		TokenMask: tokenMask,
		Result:    AuditResultFailure,
		Detail:    detail,
		Source:    source,
	})
}

// LogAuditDenied 是便捷函数：记录拒绝审计条目（SNAPSHOT-12）。
func LogAuditDenied(logger AuditLogger, op AuditOperation, tokenID, tokenMask, source, detail string) {
	if logger == nil {
		return
	}
	_ = logger.Log(&AuditEntry{
		Operation: op,
		TokenID:   tokenID,
		TokenMask: tokenMask,
		Result:    AuditResultDenied,
		Detail:    detail,
		Source:    source,
	})
}
