// Package session — JSONL incremental persistence
package session

import (
	"bufio"
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/anchorandfish/whale-code/internal/bus"
	"github.com/anchorandfish/whale-code/internal/util"
)

// Store JSONL session store
type Store struct {
	dir          string
	mu           sync.Mutex
	lockMode     LockMode
	cache        []SessionSummary
	cacheKey     string // key for cached search results (e.g. "tag:foo")
	cacheTime    time.Time
	tagIndex     map[string][]string // tag -> sessionIDs, lazily loaded
	tagIndexTime time.Time           // mod time of the tag index file when last loaded
	// bus 用于发布生命周期事件（SessionForkEvent / SessionSwitchEvent），
	// 供 Context 模块的 SessionLifecycleHandler 订阅。可为 nil（向后兼容，
	// 未注入时静默跳过事件发布）。
	bus *bus.Bus
	// archiveEnabled 归档式压缩开关（settings.json compact_archive，默认 true）。
	// 开启时 Rewrite/Compact 物理删除消息前先把被删消息落入
	// <sessionID>.archive.jsonl sidecar 归档；关闭时保持旧行为（直接删除）。
	archiveEnabled bool

	// networkFS 标识会话目录是否落在网络共享卷（NFS/SMB/AFP/WebDAV）上。
	// flock 在部分网络卷上会「本地成功但跨主机无互斥」（silent success），
	// 那正是 L1 要修的 P1 换个位置复现且无信号 ⇒ 检测为网络卷时禁用 flock，
	// 回退 O_EXCL 锁（并在回退路径上同样修掉「存活即被夺锁」，见 §5.5.2）。
	// 在 NewStore 时检测一次并缓存（AcquireLock 每写必调，不能每写都 Statfs）。
	networkFS bool

	// heldMu / heldID / heldLock 是**会话级占用**状态：本进程当前
	// 「打开着」哪个会话，以及那把长期持有的占用锁。
	//
	// 与 s.mu 分开的理由：HoldSession 在持锁期间会调用 AcquireLock（可能阻塞到
	// 超时），不能把 s.mu 按住那么久 —— 写入路径要 s.mu 串行化进程内写入。
	heldMu   sync.Mutex
	heldID   string
	heldLock *SessionLock
}

type persistedSessionMetadata struct {
	Type            string `json:"type"`
	CompactionCount int    `json:"compaction_count,omitempty"`
	// Name is the human-readable session alias.
	//
	// 会话主键是文件名里的 UUID；Name 只是给人看的别名，可为空。
	// 之所以要**持久化**而不是每次从首条消息现推：别名是用户的身份标识，
	// 允许后续重命名，重推会把它改回去。旧式会话没有本字段时回退为文件名
	// （旧式文件名本身就是可读名），见 scanSessionMetadata。
	Name string `json:"name,omitempty"`
}

const sessionMetadataRecordType = "session_metadata"

// TranscriptVersion is a content-addressed CAS version for a JSONL file.
type TranscriptVersion struct {
	Exists bool
	SHA256 string
}

// ErrSessionRewriteConflict means the transcript changed after a compaction
// candidate was prepared. The caller must discard the candidate and retry
// from a fresh session snapshot.
var ErrSessionRewriteConflict = errors.New("session rewrite conflict")

func decodeSessionRecord(data []byte) (*Message, *persistedSessionMetadata, error) {
	var envelope struct {
		Type string `json:"type"`
	}
	if err := json.Unmarshal(data, &envelope); err != nil {
		return nil, nil, err
	}
	if envelope.Type == sessionMetadataRecordType {
		var metadata persistedSessionMetadata
		if err := json.Unmarshal(data, &metadata); err != nil {
			return nil, nil, err
		}
		if metadata.CompactionCount < 0 {
			return nil, nil, fmt.Errorf("negative compaction count")
		}
		return nil, &metadata, nil
	}
	if envelope.Type != "" {
		return nil, nil, fmt.Errorf("unknown session record type %q", envelope.Type)
	}
	var msg Message
	if err := json.Unmarshal(data, &msg); err != nil {
		return nil, nil, err
	}
	switch msg.Role {
	case RoleSystem, RoleUser, RoleAssistant, RoleTool:
	default:
		return nil, nil, fmt.Errorf("invalid or missing message role %q", msg.Role)
	}
	return &msg, nil, nil
}

// tagIndexEntry is the on-disk format for the tag index
type tagIndexEntry struct {
	SessionID string   `json:"session_id"`
	Tags      []string `json:"tags"`
}

// NewStore creates a session store.
//
// C24 breaking change: the signature now returns an error so that future
// SQLite-index initialization failures (and the directory creation step) can
// be reported to the caller instead of being silently swallowed. All call
// sites must handle the error.
//
// The directory is created if it does not exist; a permission or disk-space
// failure here is returned as an error rather than deferred to the first
// write.
func NewStore(dir string) (*Store, error) {
	// 权限取唯一声明 util.LockDirPerm（F11 L2，2026-09-20）：会话目录既放 transcript
	// 又放 .lock 文件，修复前此处是裸 0755，与 util/daemon 锁族不一致，且最终权限
	// 取决于「谁先 MkdirAll」（MkdirAll 对已存在目录是空操作）。
	if err := os.MkdirAll(dir, util.LockDirPerm); err != nil {
		return nil, fmt.Errorf("session: failed to create store directory %q: %w", dir, err)
	}
	// archiveEnabled 默认 true：归档式压缩是压缩路径的安全网，默认开启；
	// settings.json compact_archive=false 显式关闭（SetArchiveEnabled 接线）。
	s := &Store{dir: dir, archiveEnabled: true}
	// L1（方案 A）：检测会话目录是否落在网络共享卷上。fail-safe：检测失败（err != nil）
	// 或判为网络卷 ⇒ 禁用 flock、回退 O_EXCL，并告警（宁可回退，不可在「本地成功但跨主机
	// 无互斥」的网络卷上误用 flock）。
	if network, fstype, nfsErr := util.IsNetworkFS(dir); network || nfsErr != nil {
		s.networkFS = true
		if nfsErr != nil {
			log.Printf("[session] lock: cannot determine filesystem type of %q (%v); falling back to O_EXCL lock (flock disabled)", dir, nfsErr)
		} else {
			log.Printf("[session] lock: %q is on a network filesystem (%s); flock disabled, falling back to O_EXCL lock", dir, fstype)
		}
	}
	return s, nil
}

// Dir return storage directory path
func (s *Store) Dir() string {
	return s.dir
}

// SetLockMode set lock mode
func (s *Store) SetLockMode(mode LockMode) {
	s.lockMode = mode
}

// SetBus 注入事件总线，用于发布 SessionForkEvent / SessionSwitchEvent
// 生命周期事件（C20 冲突解决：Session 不直接调用 Context，而是通过 bus 解耦）。
// b 可为 nil，此时所有事件发布静默跳过，保持向后兼容。应在 NewStore 之后、
// 首次 ForkSession / 会话切换之前调用。
func (s *Store) SetBus(b *bus.Bus) {
	s.bus = b
}

// invalidateCache clears the session list cache and tag index
func (s *Store) invalidateCache() {
	s.cache = nil
	s.cacheTime = time.Time{}
	s.tagIndex = nil
	s.tagIndexTime = time.Time{}
}

// SessionSummary session summary (for list display)
type SessionSummary struct {
	ID        string `json:"id"`
	Name      string `json:"name"`
	TurnCount int    `json:"turn_count"`
	UpdatedAt int64  `json:"updated_at"`
}

// Append append a message to session file
// acquire file lock first (cross-process safe), then mutex (in-process safe)
//
// FACT-SESS-003 fix: even in best-effort mode, the write is rejected when
// the lock is actively held by another live process. Best-effort only
// tolerates non-lock-related failures (permission errors, etc.).
//
// E4（资源硬上限）：增量追加路径校验①单会话字节上限 ②会话文件总数上限（仅新建时）。
// 校验在**开文件之前**完成，故被拒的写入既不落盘也不留下空文件。
// 消息数上限不在此处校验（那需要 O(n) 读全文），由整会话写路径（Save/Import）承担；
// 磁盘增长由字节上限独立封顶，两条通道合起来保证单会话有界。
func (s *Store) Append(sessionID string, msg Message) error {
	// cross-process file lock
	lock, lockErr := s.acquireWriteLock(sessionID)
	if lockErr == nil {
		// lock == nil ⇒ 本进程已长期持有该会话的占用锁，本次写入无需重复加锁。
		if lock != nil {
			defer lock.Release()
		}
	} else {
		// Lock acquisition failed. In strict mode, always fail.
		// In best-effort mode, still fail if the lock is actively held
		// by another live process (FACT-SESS-003).
		if s.lockMode == LockModeStrict {
			return fmt.Errorf("unable to acquire session lock (another process may be writing): %w", lockErr)
		}
		if s.isLockHeldByActiveProcess(sessionID) {
			return fmt.Errorf("session %q is locked by another active process", sessionID)
		}
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	s.invalidateCache()

	if err := os.MkdirAll(s.dir, util.LockDirPerm); err != nil {
		return fmt.Errorf("failed to create session directory: %w", err)
	}

	path := s.sessionPath(sessionID)

	// E4：先序列化、先量尺寸，再决定是否开文件——保证被拒的写入不落盘。
	data, err := json.Marshal(msg)
	if err != nil {
		return fmt.Errorf("failed to serialize message: %w", err)
	}
	data = append(data, '\n')

	existing, statErr := os.Stat(path)
	if statErr != nil && !os.IsNotExist(statErr) {
		return fmt.Errorf("failed to stat session file: %w", statErr)
	}
	existed := statErr == nil
	if err := s.enforceNewSessionAllowed(sessionID, path, existed); err != nil {
		return err
	}
	var size int64
	if existed {
		size = existing.Size()
	}
	if err := checkByteLimit(sessionID, size, int64(len(data))); err != nil {
		return err
	}

	f, err := os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		return fmt.Errorf("failed to open session file: %w", err)
	}
	defer f.Close()

	if _, err := f.Write(data); err != nil {
		return fmt.Errorf("failed to write message: %w", err)
	}

	return nil
}

// AppendBatch 批量追加多条消息（append-only，不带别名）。
// 语义与锁行为见 appendBatch；AppendBatchNamed 是它的带别名变体。
func (s *Store) AppendBatch(sessionID string, msgs []Message) error {
	return s.appendBatch(sessionID, "", msgs)
}

// AppendBatchNamed 批量追加消息，并在**新建**会话文件时把可读别名 alias
// 写进首行的 session_metadata 记录。
//
// 会话主键是 UUID（文件名），可读别名不随文件名走，必须显式持久化，
// 否则 /session list 与 resume 只能显示一串 hex。别名只在文件首次创建时写入，
// 因此重复调用是幂等的（不会追加第二条 metadata 记录）。
func (s *Store) AppendBatchNamed(sessionID, alias string, msgs []Message) error {
	return s.appendBatch(sessionID, alias, msgs)
}

// appendBatch 批量追加多条消息（append-only，复用 O_APPEND|O_WRONLY 模式）
// 先获取跨进程文件锁（AcquireLock），再加进程内互斥锁（s.mu）。
//
// 与 Append 保持一致的 append-only 行为：每条消息以 JSON + 换行符追加到文件末尾，
// 文件标识符（inode）保持不变，崩溃时仅丢失最后一条未完成写入，不丢失历史。
//
// S4: 文件权限 0o600（与 FACT-SESS-002 对齐：transcript 含敏感会话内容）。
//
// 历史 bug（FACT-SESS-001）：原实现 os.ReadFile + AtomicWriteFile（temp + rename）
// 导致全量重写，inode 变化且崩溃时丢失全部历史；已改为 append-only 修复。
//
// FACT-SESS-003 fix: even in best-effort mode, the write is rejected when
// the lock is actively held by another live process.
func (s *Store) appendBatch(sessionID, alias string, msgs []Message) error {
	// acquire file lock to prevent concurrent writes
	lock, lockErr := s.acquireWriteLock(sessionID)
	if lockErr == nil {
		// lock == nil ⇒ 已由本进程长期持有，无需释放。
		if lock != nil {
			defer lock.Release()
		}
	} else {
		if s.lockMode == LockModeStrict {
			return fmt.Errorf("unable to acquire session lock (another process may be writing): %w", lockErr)
		}
		if s.isLockHeldByActiveProcess(sessionID) {
			return fmt.Errorf("session %q is locked by another active process", sessionID)
		}
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	s.invalidateCache()

	if err := os.MkdirAll(s.dir, util.LockDirPerm); err != nil {
		return fmt.Errorf("failed to create session directory: %w", err)
	}

	path := s.sessionPath(sessionID)

	// E4：批量与单条同规则——先全部序列化并量尺寸，再开文件。
	// 这同时兑现了接口注释中的 all-or-nothing 契约：任何一条序列化失败或被上限拒绝，
	// 都不会留下「前几条已写、后几条没写」的半截会话。
	encoded := make([][]byte, 0, len(msgs))
	var batchBytes int64
	for _, msg := range msgs {
		data, err := json.Marshal(msg)
		if err != nil {
			return fmt.Errorf("failed to serialize message: %w", err)
		}
		data = append(data, '\n')
		encoded = append(encoded, data)
		batchBytes += int64(len(data))
	}

	existing, statErr := os.Stat(path)
	if statErr != nil && !os.IsNotExist(statErr) {
		return fmt.Errorf("failed to stat session file: %w", statErr)
	}
	existed := statErr == nil
	if err := s.enforceNewSessionAllowed(sessionID, path, existed); err != nil {
		return err
	}

	// 新建会话时把可读别名写进 transcript 首行的 session_metadata 记录。
	//
	// 为什么放在这里而不是另开一个 sidecar：会话主键已是 UUID，文件名不再承载
	// 可读名，别名必须随消息一起持久化才不丢。写成**首行**而不是末尾，是为了
	// 与全量写路径（writeSessionLocked）的输出形态一致 —— 同一种记录只有一个
	// 位置约定，读取侧（Load / scanSessionMetadata）不必区分来源。
	// 头部与首批消息在同一个 O_APPEND 句柄里写出，不存在「头部写了、消息没写」
	// 的半截状态。
	if !existed && alias != "" {
		header, err := json.Marshal(persistedSessionMetadata{
			Type: sessionMetadataRecordType,
			Name: alias,
		})
		if err != nil {
			return fmt.Errorf("failed to serialize session metadata: %w", err)
		}
		header = append(header, '\n')
		encoded = append([][]byte{header}, encoded...)
		batchBytes += int64(len(header))
	}

	var size int64
	if existed {
		size = existing.Size()
	}
	if err := checkByteLimit(sessionID, size, batchBytes); err != nil {
		return err
	}

	if err := s.writeEncodedLocked(sessionID, encoded); err != nil {
		return err
	}

	s.updateTagIndex(sessionID, msgs)
	_ = s.saveTagIndex()

	return nil
}

// writeEncodedLocked 以 append-only 方式把**已序列化**的整行记录追加到会话文件。
//
// 调用方必须已持有跨进程锁与 s.mu，并已完成资源上限校验。
// 抽出来是为了让「写消息」与「写别名记录」共用同一段 IO：两者都是往同一文件
// 追加 JSON 行，规律（O_APPEND、0o600、不缓冲）必须一致，复制一份必然漂移。
func (s *Store) writeEncodedLocked(sessionID string, encoded [][]byte) error {
	// 以 append-only 模式打开文件：不读取旧内容，直接追加到文件末尾。
	// O_APPEND 保证并发安全（kernel 保证 write 原子追加到文件末尾），
	// 跨进程并发由 AcquireLock 兜底；O_CREATE 在文件不存在时创建。
	f, err := os.OpenFile(s.sessionPath(sessionID), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		return fmt.Errorf("failed to open session file: %w", err)
	}
	defer f.Close()

	// 逐条追加写入（每条记录一行 JSON）。
	// 不缓冲到内存再一次性写：保持 append-only 语义，崩溃时仅丢失最后一条未 fsync 的写入。
	for _, data := range encoded {
		if _, err := f.Write(data); err != nil {
			return fmt.Errorf("failed to write message: %w", err)
		}
	}
	return nil
}

// SetAlias 更新会话的可读别名，只追加一条 session_metadata 记录，**不改文件名**。
//
// 为什么是「改别名」而不是「重命名文件」：UUID 主键是会话的**地址**，别名是**标签**。
// 重命名文件会让所有已发出的引用同时失效 —— 别的终端里的 --session <id>、
// bookmarks、脚本里的 /resume <id> 全部指向一个不存在的文件；而用户说
// "改个名" 的诉求只是换个显示名。
//
// 采用 append-only 追加而非重写 transcript：重写会把 inode 换掉并动用 .bak 备份，
// 而改名是纯元数据操作，不值得冒丢历史的风险。读取侧（Load / scanSessionMetadata）
// 取**最后一条**非空 Name，因此追加即生效、且天然幂等可覆盖。
func (s *Store) SetAlias(sessionID, alias string) error {
	alias = strings.TrimSpace(alias)
	if alias == "" {
		return fmt.Errorf("empty session alias")
	}
	if !s.Exists(sessionID) {
		return fmt.Errorf("session %q not found", sessionID)
	}

	record, err := json.Marshal(persistedSessionMetadata{
		Type: sessionMetadataRecordType,
		Name: alias,
	})
	if err != nil {
		return fmt.Errorf("failed to serialize session metadata: %w", err)
	}
	record = append(record, '\n')

	lock, lockErr := s.acquireWriteLock(sessionID)
	if lockErr == nil {
		// lock == nil ⇒ 已由本进程长期持有，无需释放。
		if lock != nil {
			defer lock.Release()
		}
	} else if s.isLockHeldByActiveProcess(sessionID) {
		return fmt.Errorf("session %q is locked by another active process", sessionID)
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	s.invalidateCache()

	if err := os.MkdirAll(s.dir, util.LockDirPerm); err != nil {
		return fmt.Errorf("failed to create session directory: %w", err)
	}
	return s.writeEncodedLocked(sessionID, [][]byte{record})
}

// Load loads a complete session.
//
// In addition to parsing the JSONL file, Load applies two in-memory
// normalizations so callers always see a well-formed session regardless of
// file vintage:
//
// - loadMessagesWithChain (S6): backfills UUID/ParentUUID for legacy files
// that predate the UUID chain, so the in-memory session always has a
// consistent message chain for Compact/Fork.
// - normalizeMessagesBase64Data (S8): converts legacy Base64 image payloads
// (real base64, length > 64) to their SHA-256 hex digest form so the
// in-memory session always reflects the new hash semantics.
//
// A8（fail-closed T2，2026-09-14 改判）：会话文件含任何无法解析的行即视为
// **完整性受损**，整份会话拒绝加载（返回 nil, *SessionCorruptionError），
// 且损坏文件被**隔离**到 <storeDir>/quarantine/（移动而非删除，保留原始字节
// 供人工修复）。不再返回「跳过损坏行后仍可用」的 session。
//
// 两个附加项（不是替代品，见决策表 §2 A8 行）：
// - 显著告警：log ERROR + Error() 文案暴露隔离路径（上层 TUI 会原样展示）；
// - 审计：append-only 写 <storeDir>/audit/integrity.jsonl（含隔离路径）。
func (s *Store) Load(sessionID string) (*Session, error) {
	path := s.sessionPath(sessionID)
	f, err := os.Open(path)
	if err != nil {
		// A8 可视性附加项：文件已不存在但隔离件仍在 ⇒ 明确告知「该会话因损坏被隔离」，
		// 而不是抛一个让人以为「从来没这个会话」的 no such file。
		if os.IsNotExist(err) {
			if qPath := s.findQuarantinedSession(sessionID); qPath != "" {
				return nil, &SessionCorruptionError{SessionID: sessionID, QuarantinePath: qPath}
			}
		}
		return nil, fmt.Errorf("failed to open session file: %w", err)
	}
	defer f.Close()

	session := NewSession()
	// 别名三级回退与 scanSessionMetadata 保持一致：
	// 文件名（旧式会话可读名）→ transcript metadata 记录 → 首条 user 消息派生。
	// 文件名是 UUID 形状时它不是可读名，只能作为最后兜底。
	if !IsSessionID(sessionID) {
		session.Metadata.Name = sessionID
	}
	// Metadata was historically reconstructed from JSONL. Load the optional
	// sidecar first so counters such as compaction count survive resume while
	// old sessions remain fully compatible.
	metadataPath := s.metadataPath(sessionID)
	metadataBytes, metadataErr := os.ReadFile(metadataPath)
	if metadataErr != nil {
		metadataBytes, metadataErr = os.ReadFile(metadataPath + ".bak")
	}
	if metadataErr == nil {
		var metadata persistedSessionMetadata
		jsonErr := json.Unmarshal(metadataBytes, &metadata)
		if jsonErr != nil {
			if backupBytes, backupErr := os.ReadFile(metadataPath + ".bak"); backupErr == nil {
				jsonErr = json.Unmarshal(backupBytes, &metadata)
			}
		}
		if jsonErr == nil {
			session.Metadata.CompactionCount = metadata.CompactionCount
			if metadata.Name != "" {
				session.Metadata.Name = metadata.Name
			}
		}
	}

	scanner := bufio.NewScanner(f)
	buf := make([]byte, 0, 1024*1024)
	scanner.Buffer(buf, 10*1024*1024)

	lineNum := 0
	derivedAlias := ""
	var corruptLines []int
	for scanner.Scan() {
		lineNum++
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}

		msg, metadata, err := decodeSessionRecord([]byte(line))
		if err != nil {
			corruptLines = append(corruptLines, lineNum)
			continue
		}
		if metadata != nil {
			if metadata.CompactionCount > session.Metadata.CompactionCount {
				session.Metadata.CompactionCount = metadata.CompactionCount
			}
			// 可读别名持久化在 transcript 的 metadata 记录里（主键是文件名 UUID）。
			if metadata.Name != "" {
				session.Metadata.Name = metadata.Name
			}
			continue
		}

		session.messages = append(session.messages, *msg)
		if msg.Role == RoleUser {
			session.Metadata.TurnCount++
			if derivedAlias == "" {
				derivedAlias = DeriveAlias(msg.TextContent())
			}
		}
	}

	if len(corruptLines) > 0 {
		// ★ （2026-10-13 Windows 真机 CI）：**先关读句柄，再隔离**。
		// Windows 不允许改名仍被打开的文件（POSIX 容忍 ⇒ mac 上测不出）；
		// 旧代码 defer f.Close() 还挂着就去 rename 同一路径 ⇒ 隔离必失败、
		// 隔离路径为空（X6/A8/S18 九个用例在 Windows CI 上红）。对已关闭句柄
		// 再 Close 幂等返回 ErrInvalid，defer 无害。对齐业内既有实现的纪律：rename
		// 前句柄必须先关（Windows 语义下零例外）。
		_ = f.Close()
		// A8（fail-closed T2）：拒绝加载 + 隔离损坏文件 + 显著告警。
		// 「可见 + 审计」保留为附加项：
		// 1) 隔离：损坏字节移入 quarantine/，既不静默丢弃也不可再被当正常会话使用；
		// 2) 审计：append-only 事件（含隔离路径），留下不可否认的完整性痕迹；
		// 3) 告警：ERROR 级日志 + 类型化错误（文案含隔离路径，TUI 原样展示）。
		// 隔离失败不回退为 fail-open：加载仍然拒绝，隔离失败原因随错误一并上报。
		// 规范化（S6 UUID 链 / S8 Base64）只在可用会话上做，被拒会话一律丢弃半成品。
		qPath, qErr := s.quarantineCorruptSession(sessionID, path)
		s.appendIntegrityAudit(IntegrityAuditEvent{
			Event:          IntegrityAuditEventSessionCorrupt,
			SessionID:      sessionID,
			CorruptLines:   corruptLines,
			SkippedCount:   len(corruptLines),
			QuarantinePath: qPath,
			Detail:         "corrupt JSONL line(s) detected; load refused (fail-closed T2) and file quarantined",
		})
		log.Printf("[session] ERROR: session %q refused (fail-closed): %d corrupt line(s) %v; quarantined to %q (err=%v, integrity audit: %s)",
			sessionID, len(corruptLines), corruptLines, qPath, qErr, s.IntegrityAuditPath())
		return nil, &SessionCorruptionError{
			SessionID:      sessionID,
			CorruptLines:   corruptLines,
			QuarantinePath: qPath,
			QuarantineErr:  qErr,
		}
	}

	// 别名兜底：文件是 UUID 且 transcript 未写 metadata 头时，用首条 user 消息
	// 现推一个可读名，避免列表里出现一串无意义的 hex。
	if session.Metadata.Name == "" {
		session.Metadata.Name = derivedAlias
	}
	if session.Metadata.Name == "" {
		session.Metadata.Name = sessionID
	}

	// S6: backfill UUID/ParentUUID for legacy files.
	session.messages = loadMessagesWithChain(session.messages)
	// S8: convert legacy Base64 image payloads to hash form.
	normalizeMessagesBase64Data(session.messages)
	// Re-derive the chain tail so AddMessage links correctly after load.
	session.lastUUID = deriveLastUUID(session.messages)

	if len(session.messages) > 0 {
		first := session.messages[0]
		session.Metadata.CreatedAt = first.Timestamp
		last := session.messages[len(session.messages)-1]
		session.Metadata.UpdatedAt = last.Timestamp
	}

	return session, nil
}

// quarantineCorruptSession 把完整性受损的会话文件移入 <storeDir>/quarantine/。
//
// 为什么是「移动」而不是「删除」：拒绝加载已经让该会话不可用，删除则会永久
// 丢失用户对话（可能是被篡改的单行，其余内容完全可读）。移动 = 既从可加载
// 路径上摘除（fail-closed），又保留原始字节供人工修复。
//
// 隔离件命名 <sessionID>.jsonl.corrupt-<UTC 时间戳>；同秒重复损坏时追加序号，
// 避免互相覆盖。目录 0700、保留原文件权限（rename 不改 mode）。
func (s *Store) quarantineCorruptSession(sessionID, path string) (string, error) {
	dir := filepath.Join(s.dir, quarantineDirName)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", fmt.Errorf("create quarantine dir: %w", err)
	}
	base := SanitizeSessionID(sessionID) + ".jsonl.corrupt-" + time.Now().UTC().Format("20060102T150405Z")
	dst := filepath.Join(dir, base)
	for i := 1; ; i++ {
		if _, err := os.Stat(dst); os.IsNotExist(err) {
			break
		} else if err != nil && !os.IsNotExist(err) {
			return "", fmt.Errorf("stat quarantine target: %w", err)
		}
		dst = filepath.Join(dir, fmt.Sprintf("%s-%d", base, i))
	}
	if err := os.Rename(path, dst); err != nil {
		if os.IsNotExist(err) {
			// 原始文件已被并发加载者隔离：隔离语义已达成，不算失败。
			return dst, nil
		}
		return "", err
	}
	return dst, nil
}

// findQuarantinedSession 返回该会话最近一次被隔离的路径（无则空串）。
// 仅在原文件不存在时调用，用于把「会话不见了」升级为「会话因损坏被隔离到此处」。
func (s *Store) findQuarantinedSession(sessionID string) string {
	prefix := SanitizeSessionID(sessionID) + ".jsonl.corrupt-"
	entries, err := os.ReadDir(filepath.Join(s.dir, quarantineDirName))
	if err != nil {
		return ""
	}
	newest := ""
	for _, e := range entries {
		if e.IsDir() || !strings.HasPrefix(e.Name(), prefix) {
			continue
		}
		if e.Name() > newest {
			newest = e.Name()
		}
	}
	if newest == "" {
		return ""
	}
	return filepath.Join(s.dir, quarantineDirName, newest)
}

// LoadReport summarizes the side effects of LoadWithReport: which crash
// recovery actions (if any) were taken before the session could be loaded.
type LoadReport struct {
	SessionID    string
	CorruptLines []int
	// QuarantinePath 非空表示该会话因损坏被拒绝加载（A8 fail-closed T2）
	// 且损坏文件已被隔离到此路径（供上层提示用户恢复位置）。
	QuarantinePath string
	RecoveredTmp   bool   // a crashed .tmp file was promoted to the main file
	TmpPath        string // the .tmp path that was recovered (if any)
	TmpAge         time.Duration
}

// LoadWithReport loads a session and, as part of loading, performs S3 crash
// recovery: if a `<sessionID>.jsonl.tmp` file is present and looks like a
// crash leftover (older than crashedTmpThreshold, i.e. the writer that
// created it is no longer running), it is promoted to replace the main file
// before parsing. This recovers data that was fully written to the temp file
// but never renamed because the process crashed mid-rename.
//
// A recent .tmp file (younger than the threshold) is left in place on the
// assumption that its writer is still active; LoadWithReport reports its
// presence via LoadReport but does not touch it.
//
// The returned LoadReport always describes what happened (corrupt lines,
// recovery actions) so callers (e.g. the TUI) can surface a message to the
// user. The returned *Session is never nil when err == nil.
//
// A8 改判（fail-closed T2）：损坏行**不再被降级为 report 后静默吞掉**。
// 会话完整性受损时 Load 拒绝加载，本函数把错误原样上抛（errors.As 可取到
// *SessionCorruptionError），并把损坏行号与隔离路径写进 LoadReport 供上层提示。
func (s *Store) LoadWithReport(sessionID string) (*Session, *LoadReport, error) {
	report := &LoadReport{SessionID: sessionID}

	// S3: crash recovery — check for a leftover .tmp file before parsing.
	if err := s.recoverCrashedTmp(sessionID, report); err != nil {
		// Recovery failure is non-fatal: fall through to normal Load so the
		// user still sees whatever is in the main file.
		_ = err
	}

	session, err := s.Load(sessionID)
	if err != nil {
		// A8：完整性受损 = 拒绝加载，错误必须上抛（不得降级为可用 session）。
		var sce *SessionCorruptionError
		if errors.As(err, &sce) {
			report.CorruptLines = sce.CorruptLines
			report.QuarantinePath = sce.QuarantinePath
		}
		return nil, report, err
	}
	return session, report, nil
}

// crashedTmpThreshold is how old a leftover .tmp file must be before
// LoadWithReport treats it as a crash leftover and promotes it. 5 minutes is
// long enough that any in-flight write will have either completed or timed
// out, while short enough that recovery happens promptly on the next load.
const crashedTmpThreshold = 5 * time.Minute

// recoverCrashedTmp checks for a `<sessionID>.jsonl.tmp` file and, if it is
// older than crashedTmpThreshold, promotes it to replace the main file. The
// promotion is atomic (rename) on Unix and a best-effort remove+rename on
// Windows.
//
// Rationale: AtomicWriteFile writes to a temp file then renames it over the
// target. If the process crashes after writing the temp file but before the
// rename, the temp file contains the complete new state but the main file
// still holds the old state. On the next load we detect this and finish the
// rename so the user does not lose the in-flight write.
//
// A .tmp file younger than the threshold is assumed to belong to a still-
// running writer and is left untouched.
func (s *Store) recoverCrashedTmp(sessionID string, report *LoadReport) error {
	path := s.sessionPath(sessionID)
	tmpPath := path + ".tmp"

	info, err := os.Stat(tmpPath)
	if err != nil {
		if os.IsNotExist(err) {
			return nil // no leftover, nothing to do
		}
		return err
	}

	age := time.Since(info.ModTime())
	report.TmpPath = tmpPath
	report.TmpAge = age

	// Recent .tmp — likely a still-running writer. Leave it alone.
	if age < crashedTmpThreshold {
		return nil
	}

	// Old .tmp — treat as crash leftover. Promote it to the main file.
	// On Windows, os.Rename fails if the destination exists, so remove first.
	if runtime.GOOS == "windows" {
		_ = os.Remove(path)
	}
	if err := os.Rename(tmpPath, path); err != nil {
		return fmt.Errorf("crash recovery: failed to promote %q: %w", tmpPath, err)
	}
	report.RecoveredTmp = true
	return nil
}

// quarantineDirName 隔离目录名（位于 session store 目录下）。
// 放在子目录：List()/countFiles 等只扫描顶层且跳过目录，不会把隔离件当会话。
const quarantineDirName = "quarantine"

// SessionCorruptionError 表示会话文件含损坏行、加载被**拒绝**（A8 fail-closed T2）。
//
// A8 改判（2026-09-14 19:45）背景：此前 Load 对损坏行 fail-open（跳过损坏行、
// 返回仍可用的 session + *LoadError）；改判后与之相反——拒绝加载该会话，并把
// 损坏文件**隔离**到 <storeDir>/quarantine/，使损坏/篡改既不能静默丢弃，
// 也不能继续被当成正常会话使用（与 S10-G2 损坏索引、S12-PR#2 损坏配置同档）。
//
// 两个附加项（不是替代品）：
// - 显著告警：Error() 文案带隔离路径，上层（TUI 会话切换/resume）据此提示用户；
// 同时 log 一条 ERROR 级痕迹。
// - 审计：append-only 写入 <storeDir>/audit/integrity.jsonl（含隔离路径）。
type SessionCorruptionError struct {
	SessionID      string
	CorruptLines   []int
	QuarantinePath string // 损坏文件被移动到的位置（空 = 隔离失败，见 QuarantineErr）
	QuarantineErr  error  // 隔离失败的原因（加载仍拒绝，仅隔离这一步未完成）
}

// Error implements the error interface.
func (e *SessionCorruptionError) Error() string {
	if e.QuarantinePath == "" {
		return fmt.Sprintf("session %q has %d corrupt line(s) %v; refusing to load (fail-closed)%s",
			e.SessionID, len(e.CorruptLines), e.CorruptLines, quarantineErrSuffix(e.QuarantineErr))
	}
	// CI 实证：%q 会把 Windows 路径的反斜杠转义成 \\，导致文案与
	// QuarantinePath 字段不再是同一字面量（上层按字段值检索文案失败）。
	// 路径按原样输出（手工加引号），POSIX 上行为零变化。
	return fmt.Sprintf("session %q has %d corrupt line(s) %v; refusing to load (fail-closed), file quarantined to \"%s\"%s",
		e.SessionID, len(e.CorruptLines), e.CorruptLines, e.QuarantinePath, quarantineErrSuffix(e.QuarantineErr))
}

func quarantineErrSuffix(err error) string {
	if err == nil {
		return ""
	}
	return fmt.Sprintf(" (quarantine failed: %v)", err)
}

// List lists all session summaries without loading full session content.
// Only scans a limited number of lines from each JSONL file to extract metadata.
func (s *Store) List() ([]SessionSummary, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.cache != nil && time.Since(s.cacheTime) < 5*time.Second {
		return s.cache, nil
	}

	if err := os.MkdirAll(s.dir, util.LockDirPerm); err != nil {
		return nil, err
	}

	entries, err := os.ReadDir(s.dir)
	if err != nil {
		return nil, err
	}

	var summaries []SessionSummary
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".jsonl") {
			continue
		}
		// 归档 sidecar（<sessionID>.archive.jsonl）不是会话文件，不得误列
		//（isArchiveFileName 见 archive.go；TestListExcludesArchiveFiles 锁定）。
		if isArchiveFileName(entry.Name()) {
			continue
		}

		sessionID := strings.TrimSuffix(entry.Name(), ".jsonl")
		info, err := entry.Info()
		if err != nil {
			continue
		}

		summary := SessionSummary{
			ID:        sessionID,
			Name:      sessionID,
			UpdatedAt: info.ModTime().UnixMilli(),
		}

		name, turnCount := s.scanSessionMetadata(sessionID)
		if name != "" {
			summary.Name = name
		}
		summary.TurnCount = turnCount

		summaries = append(summaries, summary)
	}

	sort.Slice(summaries, func(i, j int) bool {
		return summaries[i].UpdatedAt > summaries[j].UpdatedAt
	})

	s.cache = summaries
	s.cacheTime = time.Now()

	return summaries, nil
}

// scanSessionMetadata scans a session file to extract its alias and turn count.
//
// 修复 B2-P1-5：原实现限制扫描前 100 行，大文件用文件大小估算剩余行，
// 假设 user 消息均匀分布，偏差可达 ±30%。
// 现改为全量扫描，保证 turnCount 精确。scanner buffer 已设 1MB 上限，
// 单行 JSON 不会溢出；全量扫描的 O(n) 成本对会话文件（通常 <10K 行）可接受。
//
// 别名解析回退链（UUID 主键化后文件名不再是可读名，必须显式找回别名）：
// 1. transcript 里的 session_metadata 记录 Name —— 新式会话的权威来源；
// 2. 文件名本身 —— 旧式会话（文件名就是「可读名_时间戳」，不是 UUID 形状）；
// 3. 首条 user 消息派生 —— 新式会话但 metadata 头缺失（例如被外部工具截断）时兜底。
func (s *Store) scanSessionMetadata(sessionID string) (name string, turnCount int) {
	path := s.sessionPath(sessionID)
	f, err := os.Open(path)
	if err != nil {
		return sessionID, 0
	}
	defer f.Close()

	// 旧式文件名（非 UUID 形状）本身就是可读名，直接作为二级回退值。
	// 这样存量会话的展示名与改造前**逐字一致**，不因本次改动而变样。
	if !IsSessionID(sessionID) {
		name = sessionID
	}

	scanner := bufio.NewScanner(f)
	buf := make([]byte, 0, 64*1024)
	scanner.Buffer(buf, 1*1024*1024)

	derived := ""
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}

		msg, metadata, err := decodeSessionRecord([]byte(line))
		if err != nil {
			continue
		}
		if metadata != nil {
			// 持久化别名优先级最高，且不因后续行被覆盖。
			if metadata.Name != "" {
				name = metadata.Name
			}
			continue
		}
		if msg.Role == RoleUser {
			turnCount++
			if derived == "" {
				derived = DeriveAlias(msg.TextContent())
			}
		}
	}

	if name == "" {
		name = derived
	}
	if name == "" {
		name = sessionID
	}
	return name, turnCount
}

// Delete delete session
func (s *Store) Delete(sessionID string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.invalidateCache()
	path := s.sessionPath(sessionID)
	if err := os.Remove(path); err != nil {
		return err
	}
	_ = os.Remove(s.metadataPath(sessionID))
	_ = os.Remove(s.metadataPath(sessionID) + ".bak")
	// 归档 sidecar 连带清理：会话删除后归档无主，残留即孤儿文件
	//（清理失败不阻断主删除，留 canary 日志供排查）。
	if err := os.Remove(s.archivePath(sessionID)); err != nil && !os.IsNotExist(err) {
		log.Printf("[session] delete %s: archive sidecar remove failed: %v", sessionID, err)
	}
	return nil
}

// Rename renames a session
func (s *Store) Rename(oldID, newID string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.invalidateCache()
	oldPath := s.sessionPath(oldID)
	newPath := s.sessionPath(newID)
	if err := os.Rename(oldPath, newPath); err != nil {
		return err
	}
	if _, err := os.Stat(s.metadataPath(oldID)); err == nil {
		if err := os.Rename(s.metadataPath(oldID), s.metadataPath(newID)); err != nil {
			_ = os.Rename(newPath, oldPath)
			return err
		}
	}
	if _, err := os.Stat(s.metadataPath(oldID) + ".bak"); err == nil {
		_ = os.Rename(s.metadataPath(oldID)+".bak", s.metadataPath(newID)+".bak")
	}
	// 归档 sidecar 跟随重命名（失败不回滚主文件：归档丢失可接受、
	// 主会话不可丢，留 canary 日志供排查）。
	if _, err := os.Stat(s.archivePath(oldID)); err == nil {
		if err := os.Rename(s.archivePath(oldID), s.archivePath(newID)); err != nil {
			log.Printf("[session] rename %s->%s: archive sidecar rename failed: %v", oldID, newID, err)
		}
	}
	return nil
}

// Exists check if session exists
func (s *Store) Exists(sessionID string) bool {
	path := s.sessionPath(sessionID)
	_, err := os.Stat(path)
	return err == nil
}

// GenerateID generate session ID
//
// 实现已改为 UUID v7（NewSessionID），保留函数名以兼容既有调用点。
// 旧实现为 "session-<毫秒>-<rand(1e6)>"，随机位仅约 20 bit：高频生成会撞号，
// 而撞号的后果是两份会话写进同一文件（静默覆盖），见
// TestProbeSessionIDUniqueness。改为 74 bit 随机的 UUID v7 后该风险消除。
func GenerateID() string {
	return NewSessionID()
}

// MaxAliasRunes 是会话别名的最大长度（超出则截断并加省略号）。
// 10 个字符足以辨识一个对话，又不至于让 /session list 的行宽失控。
const MaxAliasRunes = 10

// DeriveAlias 从用户首条输入派生会话别名（人类可读名）。
//
// 这是别名派生的**唯一实现**：TUI 的命名路径与存量会话的别名回填都走这里，
// 避免两处各写一份截断/净化规则后逐渐漂移（历史上 TUI 就存在
// commands_session.go 与 runner_core.go 两份同名 autoNameSession 实现，
// 且各自内联了截断规则）。
//
// 与 TUI 旧实现的两点差异：
// 1. 净化改用 session 包自己的 SanitizeSessionID，不再在 TUI 侧重复一份
// sanitizeFilename —— 别名既可能被当作文件名（旧式会话），也会出现在
// 列表里，两种用途本就要求同一套净化规则；
// 2. 截断按 rune 计数（中文按字计），与旧的 []rune 行为一致。
func DeriveAlias(firstUserInput string) string {
	name := strings.TrimSpace(firstUserInput)
	// 只取首行：多行输入里第一行最有信息量，也避免别名里混入换行。
	if idx := strings.IndexAny(name, "\r\n"); idx >= 0 {
		name = name[:idx]
	}
	name = SanitizeSessionID(strings.TrimSpace(name))
	if name == "" {
		return ""
	}
	runes := []rune(name)
	if len(runes) > MaxAliasRunes {
		name = string(runes[:MaxAliasRunes]) + "..."
	}
	return name
}

// Resolve 把用户给出的「会话标识」解析为真实的会话 ID（文件名去后缀）。
//
// 接受四种写法，按优先级命中：
// 1. 完整 UUID（新会话的主键）；
// 2. hex 句柄（≥8 位 hex）：**展示句柄**（session.Handles 给的值，即 /session list
// 里 `[xxxx]` 那个，可原样回填；组内撞车时会被加长到 >8 位）或 UUID 前缀（手打开头几位）；
// 3. 可读名（会话 Metadata.Name，即用户首条输入派生出的别名）；
// 4. 旧式文件名（存量会话的文件名本身就是可读名_时间戳，无 UUID）。
//
// 歧义（多个会话匹配同一前缀/可读名）时不猜：返回错误并列出候选，
// 让调用方提示用户补全 —— 猜错会把对话接到错误的会话上，代价远高于多打几位。
func (s *Store) Resolve(idOrName string) (string, error) {
	candidate := strings.TrimSpace(idOrName)
	if candidate == "" {
		return "", fmt.Errorf("empty session identifier")
	}
	candidate = strings.TrimSuffix(candidate, ".jsonl")

	// 1) 精确命中文件名（UUID 会话与旧式会话都走这条）。
	if s.Exists(candidate) {
		return candidate, nil
	}

	summaries, err := s.List()
	if err != nil {
		return "", fmt.Errorf("failed to list sessions: %w", err)
	}

	// 2) hex 句柄匹配：展示句柄（随机尾段， 起；撞车时由 Handles 按需加长）
	// 优先，UUID 前缀兜底。
	//
	// 两段而非并集：句柄是列表印给用户看的**指认物**，必须能唯一点回它自己 ——
	// 若把「别的会话主键恰好以该句柄开头」也并进来，用户照抄列表就会撞歧义。
	// 前缀匹配只是「手打开头几位」的便利，命中不到句柄时才启用。
	if isUUIDPrefix(candidate) {
		lower := strings.ToLower(candidate)
		var byShortCode, byPrefix []string
		for _, s := range summaries {
			if !IsSessionID(s.ID) {
				continue // 旧式可读名不参与 hex 匹配（走第 3/4 条）
			}
			if matchHexTail(s.ID, lower) {
				byShortCode = append(byShortCode, s.ID)
				continue
			}
			if strings.HasPrefix(strings.ToLower(s.ID), lower) {
				byPrefix = append(byPrefix, s.ID)
			}
		}
		if len(byShortCode) > 0 {
			return pickOne(byShortCode, candidate)
		}
		return pickOne(byPrefix, candidate)
	}

	// 3) 可读名精确匹配。
	var nameHits []string
	for _, s := range summaries {
		if s.Name != "" && s.Name == candidate {
			nameHits = append(nameHits, s.ID)
		}
	}
	if len(nameHits) > 0 {
		return pickOne(nameHits, candidate)
	}

	// 4) 可读名/文件名子串匹配（兜底，容忍用户只记得片段）。
	var fuzzy []string
	for _, s := range summaries {
		if strings.Contains(strings.ToLower(s.Name), strings.ToLower(candidate)) ||
			strings.Contains(strings.ToLower(s.ID), strings.ToLower(candidate)) {
			fuzzy = append(fuzzy, s.ID)
		}
	}
	return pickOne(fuzzy, candidate)
}

// pickOne 在候选中挑选唯一会话；多命中时报错并列出候选（不替用户猜）。
func pickOne(hits []string, candidate string) (string, error) {
	switch len(hits) {
	case 0:
		return "", fmt.Errorf("no session matches %q", candidate)
	case 1:
		return hits[0], nil
	default:
		if len(hits) > 5 {
			hits = hits[:5]
		}
		return "", fmt.Errorf("ambiguous session %q, matches: %s (please give more characters)",
			candidate, strings.Join(hits, ", "))
	}
}

// isUUIDPrefix 报告该字符串是否可作为 UUID 前缀使用（≥8 个 hex/连字符）。
func isUUIDPrefix(s string) bool {
	hexish := 0
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch {
		case c == '-':
		case (c >= '0' && c <= '9') || (c >= 'a' && c <= 'f') || (c >= 'A' && c <= 'F'):
			hexish++
		default:
			return false
		}
	}
	return hexish >= 8
}

// CurrentTimeMillis current time milliseconds
func CurrentTimeMillis() int64 {
	return MillisFromTime(nil)
}

// MillisFromTime get milliseconds from time.Time
func MillisFromTime(t *time.Time) int64 {
	if t == nil {
		return time.Now().UnixMilli()
	}
	return t.UnixMilli()
}

// LockMode represents lock mode
type LockMode int

const (
	// LockModeBestEffort tries to acquire lock, does not block on failure
	LockModeBestEffort LockMode = iota
	// LockModeStrict strict mode, returns error if lock acquisition fails
	LockModeStrict
)

// lockStaleThreshold is how long a lock may be held before it is considered
// expired (the holder likely crashed). 30s matches a comparable CLI's
// proper-lockfile stale window and is long enough for any normal write while
// short enough to recover quickly from a crashed writer.
const lockStaleThreshold = 30 * time.Second

// isLockHeldByActiveProcess reports whether a valid (non-stale) lock file
// exists for the given session. This is used by the best-effort lock path
// (FACT-SESS-003): even in best-effort mode, a write must be rejected when
// the lock is actively held by another live process, because proceeding
// would corrupt the session file. Best-effort only skips the lock for
// non-lock-related failures (permission errors, missing directory, etc.).
//
// If the lock is held by the SAME process (same PID AND same ProcessStart),
// it is another goroutine that already acquired the lock — the in-process
// mutex will serialize access, so the write is allowed. A lock with
// ProcessStart == 0 (manually created / legacy) is treated as foreign
// because we cannot confirm it belongs to this process instance.
//
// ★ F11 L4—— 本函数**刻意**与 `storeLockVerdict` 不同，且必须保持不同：
//
//	它回答的**不是**同一个问题。`storeLockVerdict` 答「这个锁能不能被**夺走**」
//	（跨进程事实）；本函数答「best-effort 模式下这次写入**是否可以继续**」，
//	其判据含一条**进程内事实**：同进程锁由 `s.mu` 串行化 ⇒ 可以继续。
//
// ⇒ 锁文件损坏时这里仍是 `false`（放行），与 `config`/`AcquireLock` 的「未知不夺」
// 并不矛盾 —— 一个是"能不能删别人的锁"，一个是"我自己能不能写"。同时它**也不能**
// 改成 fail-closed：`AcquireLock` 用 `O_EXCL` 建文件后再 `Write`，两者之间存在
// **0 字节窗口**，同进程并发 `Append` 会读到半截文件；改成 fail-closed 会直接
// 拒绝本进程自己的并发写（实测打红：`TestStore_ConcurrentAppend` 10 个 goroutine
// 里 8 个报 "locked by another active process"）。
// ⇒ 迁移规格 §5 行 6 要求的"不得留两套"针对的是**同一问题**的两个答案；
// 此处是**两个不同问题**，已按"读到 return 语句再判断"的方法论逐条核过。
//
// If the lock is held by the SAME process (same PID AND same ProcessStart),
// it is another goroutine that already acquired the lock — the in-process
// mutex will serialize access, so the write is allowed. A lock with
// ProcessStart == 0 (manually created / legacy) is treated as foreign
// because we cannot confirm it belongs to this process instance.
func (s *Store) isLockHeldByActiveProcess(sessionID string) bool {
	lockPath := filepath.Join(s.dir, SanitizeSessionID(sessionID)+".lock")
	data, err := os.ReadFile(lockPath)
	if err != nil {
		if os.IsNotExist(err) {
			// 锁文件真的不存在 ⇒ 无人持有（best-effort 语义：允许写入）
			return false
		}
		// ★ CI 实证（Windows）：LockFileEx 独占锁覆盖整个文件 ⇒ 其他进程
		// 读锁文件报 sharing violation（"another process has locked a portion"）。
		// POSIX 的 flock 不阻止普通读，所以这条只在 Windows 触发。
		// 「文件在但读不到」= 有活跃持有者钉着它 ⇒ fail-closed 视为被持有，
		// 绝不能按「读不到 ⇒ 无人持有」放行写入（那正是三个多开写串红测试的根因）。
		return true
	}
	var lockInfo SessionLock
	if err := json.Unmarshal(data, &lockInfo); err != nil {
		// 半截 / 损坏 ⇒ 无法归因 ⇒ 按"进程内 mutex 会串行化"放行（见上方说明）
		return false
	}
	if lockInfo.PID == 0 {
		return false
	}
	// Same-process check: if the lock was acquired by another goroutine in
	// this process, the mutex will serialize — allow the write.
	if lockInfo.PID == os.Getpid() && lockInfo.ProcessStart != 0 {
		currentStart := currentProcessStart()
		if currentStart != 0 && currentStart == lockInfo.ProcessStart {
			return false
		}
	}
	return isProcessStillOwner(lockInfo)
}

// SessionLock represents a session file lock
// Aligned with a comparable CLI lock-file mechanism: prevents concurrent processes from writing to the same session file causing data corruption
//
// S1 enhancement: ProcessStart + Hostname enable triple verification
// (PID liveness + start-time match + staleness) to guard against PID reuse.
// Legacy lock files without ProcessStart fall back to PID liveness + staleness.
type SessionLock struct {
	path         string
	sessionID    string
	PID          int       `json:"pid"`
	AcquiredAt   time.Time `json:"acquired_at"`
	Version      int64     `json:"version"`
	ProcessStart int64     `json:"process_start,omitempty"` // comparable start-time of PID (S1)
	Hostname     string    `json:"hostname,omitempty"`      // host that holds the lock (S1)

	// held 是 flock 路径下持有的内核锁句柄。进程退出时内核自动释放，Release 只解锁
	// 不删 backing file（filelock.go 注释：持锁期不得删 backing file，否则 unlink+flock
	// 会破坏互斥）。回退路径（网络卷）为 nil，此时 Release 走 os.Remove 语义。
	held *util.HeldFileLock
}

// isProcessStillOwner performs the S1 triple verification.
//
// ★ L1 修复（2026-09-22，§5.5.2）：年龄检查**降级为兜底**，不得推翻已确证的存活。
// 修复前「锁龄」判定先于「存活」判定并短路，导致持有者存活、仅锁龄 > 30s 就被判
// 陈旧 ⇒ 从活人手里夺锁（P1 本体）。修复后顺序重划为：
//
// 1. hostname 守卫（共享卷跨主机，保留）
// 2. 存活是最高优先级信息：确认存活 ⇒ 锁有效，年龄不得推翻
// 3. PID 复用（仅当记录了 ProcessStart）：确证归属 ⇒ 直接有效，不再看年龄
// 4. 兜底：legacy 锁（ProcessStart==0）无法识别 PID 复用 ⇒ 年龄是唯一可用信号
//
// 不变量（一次性成立）：锁的有效性判定不得丢弃已确证的存活信息。
// lockStaleThreshold 退化为仅 legacy 分支的信号。
func isProcessStillOwner(lockInfo SessionLock) bool {
	// Hostname guard: a lock left by a process on another host is not ours
	// even if a local PID happens to match. Empty hostname = legacy lock, skip.
	if lockInfo.Hostname != "" {
		if h, err := os.Hostname(); err == nil && h != lockInfo.Hostname {
			return false
		}
	}
	// Check #1: is the PID alive at all?（存活是最高优先级，先于年龄）
	if !isProcessRunning(lockInfo.PID) {
		return false
	}
	// Check #2: PID reuse — only when ProcessStart was recorded.
	if lockInfo.ProcessStart != 0 {
		currentStart, err := getProcessStartTime(lockInfo.PID)
		if err != nil || currentStart != lockInfo.ProcessStart {
			return false
		}
		return true // ★ 已能确证归属 ⇒ 不再看年龄
	}
	// 兜底：legacy 锁文件（ProcessStart==0）无法识别 PID 复用 ⇒ 年龄是唯一可用信号。
	return time.Since(lockInfo.AcquiredAt) <= lockStaleThreshold
}

// AcquireLock acquires a session file lock.
//
// ★ L1 修复（方案 A，2026-09-22）：主路径迁至**内核 flock**（复用 util.HeldFileLock），
// 持有者进程退出由内核自动释放锁，不再存在「陈旧窗口夺活人锁」问题。
// API 采用有界超时（util.TryAcquireFileLockTimeout，默认 2s），杜绝无限挂起。
//
// 锁文件**不再「创建即锁」**：flock 路径下 backing file 在持有期不得被删除
// （filelock.go:15-16），故 Release 只解锁不删文件。诊断信息（pid/host/acquired_at）
// 通过 HeldFileLock.WriteMetadata 写入锁文件内容，供 IsLocked 读侧展示。
//
// 网络共享卷（s.networkFS）回退到 O_EXCL 锁：flock 在部分网络卷会「本地成功但
// 跨主机无互斥」，回退路径保留跨主机语义（Hostname 字段），并已通过 §5.5.2 修掉
// 「存活即被夺锁」（isProcessStillOwner 年龄降级为兜底）。
//
// On success the returned SessionLock is also registered with the package
// cleanup handler (cleanup.go) so that a SIGINT/SIGTERM during a write will
// still release the lock instead of leaving it dangling.
func (s *Store) AcquireLock(sessionID string) (*SessionLock, error) {
	if err := os.MkdirAll(s.dir, util.LockDirPerm); err != nil {
		return nil, fmt.Errorf("failed to create session directory: %w", err)
	}

	lockPath := filepath.Join(s.dir, SanitizeSessionID(sessionID)+".lock")

	if !s.networkFS {
		return s.acquireFlockLock(sessionID, lockPath)
	}
	return s.acquireOExclLock(sessionID, lockPath)
}

// acquireFlockLock 是 flock 主路径：内核锁 + 诊断元数据，不删 backing file。
func (s *Store) acquireFlockLock(sessionID, lockPath string) (*SessionLock, error) {
	timeout := util.EffectiveSessionLockTimeout()
	held, err := util.TryAcquireFileLockTimeout(lockPath, timeout)
	if err != nil {
		if errors.Is(err, util.ErrFileLocked) || isLockTimeout(err) {
			return nil, fmt.Errorf("session %q is locked by another process", sessionID)
		}
		return nil, fmt.Errorf("acquire session lock: %w", err)
	}

	// 写入诊断元数据（pid/host/acquired_at），供 IsLocked 读侧展示「被 PID x 锁住」。
	// 失败不致命：锁已持有，元数据只是诊断信息（flock 读不出锁龄，只能靠这里写）。
	lockInfo := SessionLock{
		PID:          os.Getpid(),
		AcquiredAt:   time.Now(),
		Version:      time.Now().UnixNano(),
		ProcessStart: currentProcessStart(),
		Hostname:     currentHostname(),
	}
	if data, merr := json.Marshal(lockInfo); merr == nil {
		_ = held.WriteMetadata(data)
	}

	lock := &SessionLock{
		path:         lockPath,
		sessionID:    sessionID,
		PID:          lockInfo.PID,
		AcquiredAt:   lockInfo.AcquiredAt,
		Version:      lockInfo.Version,
		ProcessStart: lockInfo.ProcessStart,
		Hostname:     lockInfo.Hostname,
		held:         held,
	}
	registerLockForCleanup(lock)
	return lock, nil
}

// isLockTimeout 报告 err 是否为锁超时错误（errors.Is 判 LockTimeoutError）。
func isLockTimeout(err error) bool {
	var t *util.LockTimeoutError
	return errors.As(err, &t)
}

// acquireOExclLock 是网络共享卷的回退路径：保留 O_EXCL + JSON 三元组，跨主机语义
// 由 Hostname 字段承接。isProcessStillOwner 已按 §5.5.2 修复「存活即被夺锁」。
func (s *Store) acquireOExclLock(sessionID, lockPath string) (*SessionLock, error) {
	f, err := os.OpenFile(lockPath, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		if !os.IsExist(err) {
			return nil, fmt.Errorf("failed to create lock file: %w", err)
		}
		// Lock file exists — decide whether it is stale via triple verification.
		stale, err := s.isLockStale(lockPath)
		if err != nil || !stale {
			return nil, fmt.Errorf("session %q is locked by another process", sessionID)
		}
		// Stale: remove and retry once.
		if err := os.Remove(lockPath); err != nil {
			return nil, fmt.Errorf("session %q is locked and stale lock could not be removed: %w", sessionID, err)
		}
		f, err = os.OpenFile(lockPath, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
		if err != nil {
			return nil, fmt.Errorf("session %q is locked by another process", sessionID)
		}
	}

	// We own the lock file. Write our identity into it.
	version := time.Now().UnixNano()
	lockInfo := SessionLock{
		PID:          os.Getpid(),
		AcquiredAt:   time.Now(),
		Version:      version,
		ProcessStart: currentProcessStart(),
		Hostname:     currentHostname(),
	}
	data, _ := json.Marshal(lockInfo)
	if _, err := f.Write(data); err != nil {
		f.Close()
		os.Remove(lockPath)
		return nil, fmt.Errorf("failed to write lock file: %w", err)
	}
	if err := f.Close(); err != nil {
		os.Remove(lockPath)
		return nil, fmt.Errorf("failed to close lock file: %w", err)
	}

	// Race guard: re-read to confirm our lock was not overwritten by a
	// concurrent process that also thought the lock was stale.
	if data2, readErr := os.ReadFile(lockPath); readErr == nil {
		var verifyLock SessionLock
		if json.Unmarshal(data2, &verifyLock) == nil && verifyLock.Version == version {
			lock := &SessionLock{
				path:         lockPath,
				sessionID:    sessionID,
				PID:          lockInfo.PID,
				AcquiredAt:   lockInfo.AcquiredAt,
				Version:      version,
				ProcessStart: lockInfo.ProcessStart,
				Hostname:     lockInfo.Hostname,
			}
			registerLockForCleanup(lock)
			return lock, nil
		}
	}
	os.Remove(lockPath)
	return nil, fmt.Errorf("session %q is locked by another process (race detected)", sessionID)
}

// storeLockVerdict 对会话锁文件做**三值**判定（F11 L4，2026-09-20）。
//
// 判据类型是唯一声明 `util.LockVerdict`（与 `config.lockVerdict` 共用同一份语义）；
// 两侧的差异只在"什么算陈旧"这个**事实**上（此处 S1 三重校验，config 是 5 分钟超时），
// 不再是两套各写各的判据。
//
// ★ 修复点（L4 本体）：内容损坏 / PID 为 0 原先返回 **Stale**（fail-open，立即夺锁），
// 与 `config.isStaleLock` 对同一失败产物判"被持有"**方向相反**。现按上游批复
// （I-3.4）统一为 `Unknown ⇒ 不夺`（fail-closed，与本仓 I2 口径一致）。
//
// ⚠️ 已知代价：损坏的锁文件不再被自动清理 ⇒ 该会话写入会被拒绝，需人工删除
// `.lock` 文件。这是「未知不夺」的必然结果（对应 L1 的同一取向：宁可拒绝，
// 不可误夺 ⇒ 误夺会与另一写者同时写同一份 transcript）。
func (s *Store) storeLockVerdict(lockPath string) util.LockVerdict {
	data, err := os.ReadFile(lockPath)
	if err != nil {
		if os.IsNotExist(err) {
			// 文件在 stat 与 read 之间消失 ⇒ 锁已不存在 ⇒ 可夺（调用方随即重建）
			return util.LockVerdictStale
		}
		// 其他读取失败（权限 / IO）：读不到 ≠ 锁没了 ⇒ 未知，不夺
		return util.LockVerdictUnknown
	}
	var lockInfo SessionLock
	if err := json.Unmarshal(data, &lockInfo); err != nil {
		// 损坏的锁文件 ⇒ 未知 ⇒ 不夺（修复前：视为陈旧并立即夺锁）
		return util.LockVerdictUnknown
	}
	// PID 为 0 视为半截锁（写入中途崩溃）⇒ 未知 ⇒ 不夺（修复前：视为陈旧）
	if lockInfo.PID == 0 {
		return util.LockVerdictUnknown
	}
	if isProcessStillOwner(lockInfo) {
		return util.LockVerdictHeld
	}
	return util.LockVerdictStale
}

// isLockStale 读锁文件并判定其是否**可被抢占**。
//
// ★ 现为 `storeLockVerdict` 的薄转发：唯一放行分支是 `util.LockVerdictStale`
// （`MaySteal()`），损坏/不可判定一律 **false（不夺）**。
func (s *Store) isLockStale(lockPath string) (bool, error) {
	return s.storeLockVerdict(lockPath).MaySteal(), nil
}

// currentProcessStart returns the start time of the current process for
// recording into SessionLock.ProcessStart. On error (unsupported platform or
// permission denied) it returns 0, which causes isProcessStillOwner to fall
// back to PID-liveness-only verification — degrading gracefully rather than
// failing lock acquisition.
func currentProcessStart() int64 {
	start, err := getProcessStartTime(os.Getpid())
	if err != nil {
		return 0
	}
	return start
}

// currentHostname returns the local hostname, or "" if it cannot be
// determined. An empty hostname disables the hostname guard in
// isProcessStillOwner (treated as legacy lock).
func currentHostname() string {
	h, err := os.Hostname()
	if err != nil {
		return ""
	}
	return h
}

// Release releases the session file lock and unregisters it from the cleanup
// handler so a later signal does not try to release an already-released lock.
//
// ★ L1 修复：flock 路径（l.held != nil）只解锁、不删 backing file —— filelock.go:15-16
// 明写持锁期不得删 backing file，否则 unlink+flock 会破坏互斥（A 持旧 inode，B 删后
// create 新 inode 加锁成功）。回退路径（网络卷，l.held == nil）才走 os.Remove。
func (l *SessionLock) Release() {
	unregisterLock(l)
	if l.held != nil {
		_ = l.held.Release()
		return
	}
	os.Remove(l.path)
}

// SessionInUseError 报告目标会话已被**另一个进程占用** —— 即会话级占用锁拿不到。
//
// 与「写锁获取失败」的区别：这是**长期持有**的占用锁，语义是「有人正开着这个会话」，
// 而不是「有人正在写这一行」。调用方应把它当**可向用户明示的终态**（拒绝进入 + 给出
// 出路），而不是重试信号。
//
// 背景：修复前那把 .lock 只在单次写入期间被持有（毫秒级），于是「占用」这件事
// 在系统里根本没有承载体 ⇒ 第二个窗口既得不到提示、也拦不住，两段对话按写入时刻
// 交错进同一份 transcript。
type SessionInUseError struct {
	SessionID string
	PID       int    // 持有者 PID（读不到锁元数据时为 0）
	Hostname  string // 持有者主机名（跨主机共享卷时用于指名）
}

func (e *SessionInUseError) Error() string {
	switch {
	case e.PID > 0 && e.Hostname != "":
		return fmt.Sprintf("session %s is already in use by PID %d on %s", e.SessionID, e.PID, e.Hostname)
	case e.PID > 0:
		return fmt.Sprintf("session %s is already in use by PID %d", e.SessionID, e.PID)
	default:
		return fmt.Sprintf("session %s is already in use by another process", e.SessionID)
	}
}

// UserMessage 是**面向用户**的完整提示（英文主行 + 缩进中文辅行）。
//
// ★ 唯一实现：`-c` / `--session` 的启动路径（package main）与 TUI 内
// `/resume` · `/session switch` 都调本方法 —— 同一事实跨入口必须同源
// ，四处各写一份文案正是本仓反复吃亏的「双实现」坑。
//
// 内容必须含两件事：为什么被拒（两个窗口写同一份记录会把两段对话交错在一起）
// 与两条出路（--fork-session 复制一份 / 直接开新会话）。只回一句"失败"，
// 用户下一步只能靠猜。
func (e *SessionInUseError) UserMessage() string {
	holder := ""
	if e.PID > 0 {
		holder = fmt.Sprintf(" (PID %d)", e.PID)
	}
	return util.BiSame(
		"⚠️ Session %s is already open in another whale window%s.\n"+
			"  Two windows on one session would interleave their messages.\n"+
			"  Use --fork-session %s for an independent copy, or start a new session.",
		"⚠️ 会话 %s 已在另一个 whale 窗口打开%s\n"+
			"  两个窗口同时写同一份记录会把两段对话交错在一起\n"+
			"  改用 --fork-session %s 复制一份，或直接开一个新会话",
		e.SessionID, holder, e.SessionID)
}

// HoldSession 把会话标记为「本进程正在使用」，并**长期持有**该锁直到
// ReleaseHeldSession 或进程退出。
//
// ★ 结构性修复：占用必须由「持有」表达，而不是「看一眼」。
// 修复前 AcquireLock 只被各写路径调用（写完即放，毫秒级），全仓没有任何代码在
// 「会话打开期间」持有它 ⇒ 启动期的 CheckSessionLock 几乎永远什么也看不到，
// 两个窗口都能进入同一会话各自 append。
//
// 与写锁是**同一把锁**（同一个 .lock）：持有时本进程的写入不再重复加锁
// （见 acquireWriteLock），别的进程则会被 AcquireLock / isLockHeldByActiveProcess
// 拒绝 —— 这正好把 multi_instance_probe_test.go 声明的不变量「占用期间他人写入被拒」
// 落到生产路径上，而不再依赖那个人造的 hold 子进程。
//
// 语义：
// - 幂等：同一会话重复调用直接成功。
// - 切换：**先拿到新锁、再放旧锁**；拿不到则保持原状（不半途换会话）。
// - 崩溃安全：flock 随进程退出由内核释放 ⇒ 不会留下死锁（与写锁同一保证）。
// - 争用判定不依赖「持有者是否存活」探测（Windows 上那条探测不可靠，
// 见总账 / 家族）：flock 拿不到本身就是权威信号。
func (s *Store) HoldSession(sessionID string) error {
	if sessionID == "" {
		return nil
	}

	s.heldMu.Lock()
	if s.heldID == sessionID && s.heldLock != nil {
		s.heldMu.Unlock()
		return nil // 幂等
	}
	s.heldMu.Unlock()

	// 不持 heldMu 去拿锁：AcquireLock 可能阻塞到超时（默认 2s），
	// 而 holdsSession 在**每次写入**都会被调用，不能让它等这么久。
	lock, err := s.AcquireLock(sessionID)

	s.heldMu.Lock()
	defer s.heldMu.Unlock()
	if err != nil {
		// 并发竞态：另一个 goroutine 可能已为同一会话拿到锁。
		if s.heldID == sessionID && s.heldLock != nil {
			return nil
		}
		locked, pid, host, lerr := s.LockHolder(sessionID)
		if lerr != nil || !locked {
			// 不是「被占」而是真实故障（权限 / IO）⇒ 原样上报，不要误报成占用。
			return fmt.Errorf("hold session %q: %w", sessionID, err)
		}
		return &SessionInUseError{SessionID: sessionID, PID: pid, Hostname: host}
	}
	if s.heldLock != nil {
		s.heldLock.Release()
	}
	s.heldID, s.heldLock = sessionID, lock
	return nil
}

// ReleaseHeldSession 释放本进程长期持有的会话占用锁（切到新会话 / 开新会话时调用）。
func (s *Store) ReleaseHeldSession() {
	s.heldMu.Lock()
	defer s.heldMu.Unlock()
	if s.heldLock != nil {
		s.heldLock.Release()
	}
	s.heldID, s.heldLock = "", nil
}

// HeldSession 返回本进程当前长期持有的会话主键（未持有时为空串）。
func (s *Store) HeldSession() string {
	s.heldMu.Lock()
	defer s.heldMu.Unlock()
	return s.heldID
}

// holdsSession 报告本进程是否已长期持有该会话的占用锁。
func (s *Store) holdsSession(sessionID string) bool {
	if sessionID == "" {
		return false
	}
	s.heldMu.Lock()
	defer s.heldMu.Unlock()
	return s.heldID == sessionID && s.heldLock != nil
}

// acquireWriteLock 获取**本次写入**要用的跨进程锁。
//
// ★ 已由本进程长期持有时返回 (nil, nil)，调用方按「无需释放」处理。
//
// 为什么必须跳过：flock 是 open-file-description 级，同进程另开一个 fd 去 flock
// **会与自己的持有冲突** —— 既白等满 EffectiveSessionLockTimeout()（默认 2s，
// 每次写入都卡），在 LockModeStrict 下还会直接失败。持锁期间本进程已独占该会话，
// 进程内并发由 s.mu 串行化，重复加锁没有任何收益。
func (s *Store) acquireWriteLock(sessionID string) (*SessionLock, error) {
	if s.holdsSession(sessionID) {
		return nil, nil
	}
	return s.AcquireLock(sessionID)
}

// ForkSession forks a session: creates new session ID and copies all messages
// Aligned with a comparable CLI --fork-session: new ID + copy history, two sessions develop independently
//
// C20: fork 成功后发布 bus.SessionForkEvent，供 Context 模块清空缓存。
// 若未注入 bus，静默跳过。
func (s *Store) ForkSession(origID string) (string, error) {
	// Load original session
	session, err := s.Load(origID)
	if err != nil {
		return "", fmt.Errorf("failed to load original session: %w", err)
	}

	// Generate new session ID
	newID := GenerateID()

	// A fork inherits the parent's compaction lineage and then evolves
	// independently.
	forked := NewSession()
	forked.Metadata.CompactionCount = session.Metadata.CompactionCount
	// 别名随 fork 继承并加后缀：列表里两行同名会让人分不清接的是哪一个，
	// 而主键（UUID）本身不具可读性。
	forked.Metadata.Name = forkAlias(session.Metadata.Name)
	forked.SetMessages(copyMessages(session.messages))
	if err := s.Save(newID, forked); err != nil {
		return "", fmt.Errorf("failed to copy messages to new session: %w", err)
	}
	// fork 继承归档谱系：压缩历史随 fork 复制（checkpoint 语义），
	// 两会话之后独立演化。失败不阻断 fork（归档是安全网，可缺失）。
	if err := copyArchiveIfExists(s.archivePath(origID), s.archivePath(newID)); err != nil {
		log.Printf("[session] fork %s->%s: archive inheritance failed: %v", origID, newID, err)
	}

	// 发布 fork 生命周期事件（C20）
	s.publishFork(origID, newID)

	return newID, nil
}

// ForkSessionByID forks a session by specified ID.
// Uses GenerateID() for collision-resistant ID generation.
//
// C20: fork 成功后发布 bus.SessionForkEvent，供 Context 模块清空缓存。
// 若未注入 bus，静默跳过。
func (s *Store) ForkSessionByID(sessionID string) (string, error) {
	// 接受别名 / UUID 前缀 / 完整 UUID / 旧式文件名（与 --session 同一套解析）。
	resolved, err := s.Resolve(sessionID)
	if err != nil {
		return "", err
	}
	msgs, err := s.Load(resolved)
	if err != nil {
		return "", fmt.Errorf("failed to load session %s: %w", resolved, err)
	}
	newID := GenerateID()
	forked := NewSession()
	forked.Metadata.CompactionCount = msgs.Metadata.CompactionCount
	forked.Metadata.Name = forkAlias(msgs.Metadata.Name)
	forked.SetMessages(copyMessages(msgs.messages))
	if err := s.Save(newID, forked); err != nil {
		return "", fmt.Errorf("failed to write forked session: %w", err)
	}
	// fork 继承归档谱系（同 ForkSession）。
	if err := copyArchiveIfExists(s.archivePath(resolved), s.archivePath(newID)); err != nil {
		log.Printf("[session] fork %s->%s: archive inheritance failed: %v", resolved, newID, err)
	}

	// 发布 fork 生命周期事件（C20）
	s.publishFork(resolved, newID)

	return newID, nil
}

// forkAlias 派生 fork 会话的别名：继承父别名并标注来源。
// 为空时返回空串（读取侧会回退到文件名/首条消息派生），不造假名。
func forkAlias(parentAlias string) string {
	if parentAlias == "" {
		return ""
	}
	return parentAlias + " (fork)"
}

// publishFork 发布 SessionForkEvent。bus 为 nil 时静默跳过。
// 抽取为内部方法以复用 ForkSession / ForkSessionByID 的事件发布逻辑。
func (s *Store) publishFork(parentID, forkID string) {
	if s.bus == nil {
		return
	}
	s.bus.Publish(bus.SessionForkEvent{
		ParentSessionID: parentID,
		ForkSessionID:   forkID,
		OccurredAt:      time.Now(),
	})
}

// NotifySessionSwitch 发布 SessionSwitchEvent。由 TUI 层在会话切换成功后
// 调用，oldID 为切换前的会话 ID，newID 为切换后的会话 ID。若未注入 bus，
// 静默跳过（向后兼容）。接收者可能为 nil（测试场景 Store() 返回 nil），此时
// 同样静默跳过。
//
// C20 冲突解决：Session 不直接调用 Context，而是通过 bus 事件解耦；
// TUI 层不持有 bus 引用，故通过 Store 间接触发。
func (s *Store) NotifySessionSwitch(oldID, newID string) {
	if s == nil || s.bus == nil {
		return
	}
	s.bus.Publish(bus.SessionSwitchEvent{
		OldSessionID: oldID,
		NewSessionID: newID,
		OccurredAt:   time.Now(),
	})
}

// Compact rewrites a session file so that only the active message chain
// survives. Dead branches (messages reachable from a tombstoned message but
// not from the chain tail) are dropped, and tombstoned messages themselves
// are removed. The result is a smaller JSONL file that loads faster and
// carries no orphaned history.
//
// C8 contract: this is the Session-side operation the Context module calls
// after inserting a compact_boundary message. The Context module owns the
// decision of *when* to compact and *what* to tombstone; Session.Compact only
// executes the file rewrite once the tombstones are in place.
//
// Compact loads the session, drops every message whose UUID is not reachable
// from the current chain tail (last non-tombstoned message), and rewrites the
// file via Save. It does not change any UUID/ParentUUID — the surviving
// chain's links are already correct.
//
// Returns the number of messages removed (for logging / UI feedback).
func (s *Store) Compact(sessionID string) (removed int, err error) {
	session, err := s.Load(sessionID)
	if err != nil {
		return 0, fmt.Errorf("compact: failed to load session %s: %w", sessionID, err)
	}
	msgs := session.GetMessages()
	if len(msgs) == 0 {
		return 0, nil
	}

	// Build the set of UUIDs reachable from the chain tail by walking
	// ParentUUID backwards. Anything not in this set is a dead branch.
	tail := session.LastUUID()
	reachable := make(map[string]bool, len(msgs))
	for uuid := tail; uuid != ""; {
		reachable[uuid] = true
		parent := ""
		for _, m := range msgs {
			if m.UUID == uuid {
				parent = m.ParentUUID
				break
			}
		}
		if parent == "" || reachable[parent] {
			break
		}
		uuid = parent
	}

	// Keep only reachable, non-tombstoned messages.
	survivors := make([]Message, 0, len(msgs))
	for _, m := range msgs {
		if m.Tombstoned {
			removed++
			continue
		}
		if reachable[m.UUID] {
			survivors = append(survivors, m)
		} else {
			removed++
		}
	}

	session.SetMessages(survivors)
	// 归档式压缩：重写前先把被删消息（tombstoned + 死分支）落入 sidecar
	// 归档；失败则中止（主文件原样保留）。
	// 锁契约（对抗式审查 1.3 修复）：archiveDroppedLocked 要求调用方持有
	// s.mu（archiveEnabled 读 + 归档追加均在锁内）；Save 内部自取 s.mu，
	// 因此归档段单独持锁、Save 前释放，避免双重加锁死锁。
	if s.archiveEnabled {
		s.mu.Lock()
		archiveErr := s.archiveDroppedLocked(sessionID, session.GetMessages(), session.Metadata.CompactionCount)
		s.mu.Unlock()
		if archiveErr != nil {
			log.Printf("[session] compact %s: archive failed, rewrite aborted: %v", sessionID, archiveErr)
			return 0, fmt.Errorf("compact: archive dropped messages: %w", archiveErr)
		}
	}
	if err := s.Save(sessionID, session); err != nil {
		return 0, fmt.Errorf("compact: failed to rewrite session %s: %w", sessionID, err)
	}
	return removed, nil
}

// sessionIDReplacer 替换不能出现在文件名里的字符。
var sessionIDReplacer = strings.NewReplacer(
	"/", "_", "\\", "_", ":", "_", "*", "_",
	"?", "_", "\"", "_", "<", "_", ">", "_", "|", "_",
)

// SanitizeSessionID 把会话 ID 中不能出现在文件名里的字符替换为下划线。
//
// 背景（2026-09-09 实测）：会话名直接取自用户首条输入，例如
// "请执行 rm /tmp/whale-ab/victim.txt" → 会话名 "请执行 rm /tm..._2026-09-09_18-33-26"。
// 其中的 '/' 会被 filepath.Join 当成路径分隔符，"请执行 rm " 被当成子目录名，
// 于是保存时报错 "no such file or directory"（目录当然不存在）。
// 任何包含 '/' 的用户输入都会触发 —— 让用户操作文件路径是极常见的场景。
//
// 在落盘层净化而不是只在命名处净化：sessionID 还有 /session switch、
// resume、fork 等来源，落盘层兜底可以覆盖全部入口。
func SanitizeSessionID(sessionID string) string {
	return sessionIDReplacer.Replace(sessionID)
}

// sessionPath returns the session file path
func (s *Store) sessionPath(sessionID string) string {
	return filepath.Join(s.dir, SanitizeSessionID(sessionID)+".jsonl")
}

func (s *Store) metadataPath(sessionID string) string {
	return filepath.Join(s.dir, SanitizeSessionID(sessionID)+".meta.json")
}

// IsLocked checks if the session is locked by another (still-alive) process.
//
// S1: uses isProcessStillOwner (triple verification) so that a lock left by a
// crashed process, a recycled PID, or an expired holder is correctly reported
// as unlocked.
//
// ★ F11 L4：本函数是「这个锁还能不能夺」的**查询入口**，判定
// 统一交给 `storeLockVerdict`（与 `AcquireLock` / `isLockHeldByActiveProcess` 同源）。
// 修复前此处对损坏的锁文件 `return false, 0, nil`（"未锁"），而 `AcquireLock`
// 对同一文件判"陈旧可夺" ⇒ 恰好同向；若只改 `AcquireLock` 而不改此处，就会**新造**
// 一个「同一事实两个相反答案」（铁律：同一事实跨入口必须同源）。
// pid 返回 0 表示「持有者无法指名（锁文件损坏/半截）」，调用方据此调整措辞。
// LockHolder 返回会话占用者信息：是否被占、持有者 PID、持有者主机名。
//
// ★ 唯一实现：IsLocked 与 HoldSession 的占用上报都委托本方法，避免两处各解析
// 一份锁元数据（历史 bug 模式：一处改了另一处没改）。判定仍走 storeLockVerdict
// （内核锁为权威；未知按 fail-closed 视为被占，与写入侧一致）。
func (s *Store) LockHolder(sessionName string) (locked bool, pid int, hostname string, err error) {
	lockPath := filepath.Join(s.dir, SanitizeSessionID(sessionName)+".lock")
	data, err := os.ReadFile(lockPath)
	if err != nil {
		if os.IsNotExist(err) {
			return false, 0, "", nil
		}
		return false, 0, "", err
	}

	var lockInfo SessionLock
	// 解析失败时 lockInfo 保持零值（PID=0）⇒ 调用方看到「无法指名的持有者」。
	_ = json.Unmarshal(data, &lockInfo)

	if s.storeLockVerdict(lockPath).MaySteal() {
		// Stale ⇒ 锁已失效，视为未锁（PID 仍回报，便于诊断「谁的残骸」）
		return false, lockInfo.PID, lockInfo.Hostname, nil
	}
	// Held / Unknown ⇒ 视为**被占**（Unknown 出于 fail-closed，与写入侧一致）
	return true, lockInfo.PID, lockInfo.Hostname, nil
}

// IsLocked 报告会话是否被占用，以及占用者 PID（无占用者为 0）。
//
// 实现委托 LockHolder（单一实现）；保留本签名是因为它是 SessionLocker 接口的
// 既有成员，调用方（tui.CheckSessionLock 等）无需改动。
func (s *Store) IsLocked(sessionName string) (bool, int, error) {
	locked, pid, _, err := s.LockHolder(sessionName)
	return locked, pid, err
}

// Save saves the session using atomic write (util.AtomicWriteFile: temp file +
// fsync + rename). The fsync guarantees the transcript is durable on disk
// before the function returns, which matters because session files hold user
// conversation history that cannot be regenerated.
//
// FACT-SESS-005 fix: before overwriting, the existing file is copied to a
// timestamped backup (<sessionID>.jsonl.bak) so that a crash during the write
// (or a logic bug that corrupts the new content) can be rolled back.
func (s *Store) Save(sessionID string, session *Session) error {
	lock, err := s.acquireBestEffortRewriteLock(sessionID)
	if err != nil {
		return err
	}
	if lock != nil {
		defer lock.Release()
	}

	messages, metadata, _, _ := session.Snapshot()
	s.mu.Lock()
	defer s.mu.Unlock()
	// E4（资源硬上限）：整会话写路径校验消息数 + 字节 + 会话总数（仅新建）。
	// 这里也校验，是为了堵住「不经 Append 直接 Save 一个超大会话」的旁路
	//（fork / merge / import 都走 Save）。
	// 注意：Compact/Rewrite 故意**不**校验——它同时是「已有超限会话」的收缩出口，
	// 若在此处拦下，用户将永远无法把超限会话压回上限以内。
	if err := s.enforceSaveLimitsLocked(sessionID, messages); err != nil {
		return err
	}
	return s.writeSessionLocked(sessionID, messages, metadata.CompactionCount, metadata.Name)
}

// enforceSaveLimitsLocked 在整会话写入前校验 E4 三道上限。调用方必须持有 s.mu。
func (s *Store) enforceSaveLimitsLocked(sessionID string, messages []Message) error {
	path := s.sessionPath(sessionID)
	existed := false
	if _, statErr := os.Stat(path); statErr == nil {
		existed = true
	}
	if err := s.enforceNewSessionAllowed(sessionID, path, existed); err != nil {
		return err
	}
	if err := checkMessageLimit(sessionID, len(messages)); err != nil {
		return err
	}
	var total int64
	for i := range messages {
		data, err := json.Marshal(messages[i])
		if err != nil {
			return fmt.Errorf("failed to serialize message: %w", err)
		}
		total += int64(len(data)) + 1
	}
	return checkByteLimit(sessionID, 0, total)
}

// TranscriptVersion returns a full-file content version for compaction CAS.
func (s *Store) TranscriptVersion(sessionID string) (TranscriptVersion, error) {
	return readTranscriptVersion(s.sessionPath(sessionID))
}

// Rewrite atomically verifies the full transcript hash, checks the live
// in-memory revision, writes the candidate, and installs it in memory. Lock
// order is cross-process lock -> Store mutex -> Session mutex.
func (s *Store) Rewrite(sessionID string, expected TranscriptVersion, live *Session, expectedRevision uint64, candidate *Session) error {
	candidateMessages, candidateMetadata, _, _ := candidate.Snapshot()
	lock, err := s.acquireRewriteLock(sessionID)
	if err != nil {
		return err
	}
	if lock != nil {
		defer lock.Release()
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	current, err := readTranscriptVersion(s.sessionPath(sessionID))
	if err != nil {
		return fmt.Errorf("rewrite: read transcript version: %w", err)
	}
	if current != expected {
		return fmt.Errorf("%w: transcript hash changed", ErrSessionRewriteConflict)
	}

	live.mu.Lock()
	defer live.mu.Unlock()
	if live.revision != expectedRevision {
		return fmt.Errorf("%w: in-memory revision changed", ErrSessionRewriteConflict)
	}
	// 归档式压缩：主文件重写前，被删消息先落入 sidecar 归档。
	// 写入顺序是安全关键：归档失败 → 整体中止，主文件原样保留，
	// 杜绝"主文件已删、归档没写"的永久丢失窗口（canary 日志可观测）。
	if s.archiveEnabled {
		if err := s.archiveDroppedLocked(sessionID, candidateMessages, candidateMetadata.CompactionCount); err != nil {
			log.Printf("[session] rewrite %s: archive failed, rewrite aborted: %v", sessionID, err)
			return fmt.Errorf("rewrite: archive dropped messages: %w", err)
		}
	}
	if err := s.writeSessionLocked(sessionID, candidateMessages, candidateMetadata.CompactionCount, candidateMetadata.Name); err != nil {
		return err
	}
	live.messages = CloneMessages(candidateMessages)
	live.Metadata = candidateMetadata
	live.lastUUID = deriveLastUUID(live.messages)
	live.revision++
	return nil
}

func (s *Store) acquireRewriteLock(sessionID string) (*SessionLock, error) {
	lock, err := s.acquireWriteLock(sessionID)
	if err != nil {
		return nil, fmt.Errorf("unable to acquire required session rewrite lock: %w", err)
	}
	// lock 可能为 nil：本进程已长期持有该会话，调用方按 nil 处理。
	return lock, nil
}

// acquireBestEffortRewriteLock preserves the historical Save behavior. The
// compaction Rewrite path intentionally does not use this fallback because a
// full transcript replacement must never proceed without cross-process CAS
// serialization.
func (s *Store) acquireBestEffortRewriteLock(sessionID string) (*SessionLock, error) {
	lock, lockErr := s.acquireWriteLock(sessionID)
	if lockErr == nil {
		// lock 可能为 nil：本进程已长期持有该会话，调用方按 nil 处理。
		return lock, nil
	}
	if s.lockMode == LockModeStrict {
		return nil, fmt.Errorf("unable to acquire session lock (another process may be writing): %w", lockErr)
	}
	if s.isLockHeldByActiveProcess(sessionID) {
		return nil, fmt.Errorf("session %q is locked by another active process", sessionID)
	}
	return nil, nil
}

// writeSessionLocked 全量重写会话文件（调用方必须持有 s.mu 与跨进程锁）。
//
// alias 是会话的可读别名，随 compaction 计数一起写进 transcript 首行的
// session_metadata 记录；空别名不写该字段（omitempty），此时读取侧回退到
// 文件名/首条消息派生。
func (s *Store) writeSessionLocked(sessionID string, messages []Message, compactionCount int, alias string) error {

	s.invalidateCache()

	path := s.sessionPath(sessionID)

	if err := os.MkdirAll(s.dir, util.LockDirPerm); err != nil {
		return fmt.Errorf("failed to create session directory: %w", err)
	}

	// FACT-SESS-005: create a backup of the existing file before overwriting.
	if existing, err := os.ReadFile(path); err == nil {
		backupPath := path + ".bak"
		_ = os.WriteFile(backupPath, existing, 0o600)
	}

	// Serialize all messages into a buffer first so a marshal error does not
	// leave a half-written temp file on disk.
	var buf bytes.Buffer
	writer := bufio.NewWriter(&buf)
	metadataRecord := persistedSessionMetadata{Type: sessionMetadataRecordType, CompactionCount: compactionCount, Name: alias}
	metadataData, err := json.Marshal(metadataRecord)
	if err != nil {
		return fmt.Errorf("failed to serialize session metadata: %w", err)
	}
	if _, err := writer.Write(append(metadataData, '\n')); err != nil {
		return fmt.Errorf("failed to write session metadata: %w", err)
	}
	for _, msg := range messages {
		data, err := json.Marshal(msg)
		if err != nil {
			return fmt.Errorf("failed to serialize message: %w", err)
		}
		if _, err := writer.Write(append(data, '\n')); err != nil {
			return fmt.Errorf("failed to write message: %w", err)
		}
	}
	if err := writer.Flush(); err != nil {
		return fmt.Errorf("failed to flush messages: %w", err)
	}

	// AtomicWriteFile handles temp file creation, fsync, and rename; on any
	// failure path it cleans up the temp file so LoadWithReport will not
	// mistake it for a crash-recovery candidate.
	if err := util.AtomicWriteFile(path, buf.Bytes(), 0o600); err != nil {
		return fmt.Errorf("failed to atomically write session file: %w", err)
	}

	s.updateTagIndex(sessionID, messages)
	_ = s.saveTagIndex()

	return nil
}

func readTranscriptVersion(path string) (TranscriptVersion, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return TranscriptVersion{}, nil
		}
		return TranscriptVersion{}, err
	}
	for i, line := range bytes.Split(data, []byte{'\n'}) {
		line = bytes.TrimSpace(line)
		if len(line) == 0 {
			continue
		}
		if _, _, err := decodeSessionRecord(line); err != nil {
			return TranscriptVersion{}, fmt.Errorf("invalid transcript record at line %d: %w", i+1, err)
		}
	}
	sum := sha256.Sum256(data)
	return TranscriptVersion{Exists: true, SHA256: hex.EncodeToString(sum[:])}, nil
}

func persistedTailUUID(path string) (string, bool, error) {
	f, err := os.Open(path)
	if err != nil {
		if os.IsNotExist(err) {
			return "", false, nil
		}
		return "", false, err
	}
	defer f.Close()

	var messages []Message
	scanner := bufio.NewScanner(f)
	scanner.Buffer(make([]byte, 0, 1024*1024), 10*1024*1024)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}
		msg, metadata, err := decodeSessionRecord([]byte(line))
		if err != nil {
			return "", true, err
		}
		if metadata != nil {
			continue
		}
		messages = append(messages, *msg)
	}
	if err := scanner.Err(); err != nil {
		return "", true, err
	}
	messages = loadMessagesWithChain(messages)
	return deriveLastUUID(messages), true, nil
}

// tagIndexPath returns the path to the tag index file
func (s *Store) tagIndexPath() string {
	return filepath.Join(s.dir, "_tags_index.json")
}

// loadTagIndex loads the tag index from disk, returns empty map if not found.
//
// FACT-CONC-003 fix: previously the in-memory cache was never invalidated,
// so external modifications to the tag index file were never observed.
// The cache now tracks the file's modification time and reloads from disk
// when the file is newer than the last load.
//
// V3-5-1 接入：使用 util.LoadWithRecovery 替换 os.ReadFile + json.Unmarshal。
// 文件损坏时隔离到 ~/.whale/corrupt/ 并返回空 map（不 panic，不阻塞启动）。
func (s *Store) loadTagIndex() map[string][]string {
	path := s.tagIndexPath()
	info, err := os.Stat(path)
	if err != nil {
		// File doesn't exist — clear cache and return empty map.
		s.tagIndex = nil
		s.tagIndexTime = time.Time{}
		return make(map[string][]string)
	}
	// Return cached result only if the file hasn't changed since last load.
	if s.tagIndex != nil && !info.ModTime().After(s.tagIndexTime) {
		return s.tagIndex
	}
	// File is newer or cache is empty — reload from disk.
	// V3-5-1：使用 LoadWithRecovery 替换 os.ReadFile + json.Unmarshal，损坏时隔离文件。
	var entries []tagIndexEntry
	corruptDir := filepath.Join(filepath.Dir(s.dir), "corrupt")
	if err := util.LoadWithRecovery(path, &entries, corruptDir); err != nil {
		if errors.Is(err, util.ErrFileCorrupted) {
			log.Printf("[session] tag index file corrupted and quarantined: %s", path)
		}
		return make(map[string][]string)
	}
	index := make(map[string][]string)
	for _, e := range entries {
		for _, tag := range e.Tags {
			index[tag] = append(index[tag], e.SessionID)
		}
	}
	s.tagIndex = index
	s.tagIndexTime = info.ModTime()
	return index
}

// saveTagIndex saves the tag index to disk atomically.
//
// S5: uses util.AtomicWriteFileNoFsync because the tag index is a rebuildable
// cache (reconstructed on demand from the JSONL files) — the fsync throughput
// cost is not justified for a non-critical path. The temp+rename pattern still
// guards against process crashes.
func (s *Store) saveTagIndex() error {
	if s.tagIndex == nil {
		return nil
	}
	var entries []tagIndexEntry
	seen := make(map[string]map[string]bool) // sessionID -> set of tags
	for tag, sessionIDs := range s.tagIndex {
		for _, sid := range sessionIDs {
			if seen[sid] == nil {
				seen[sid] = make(map[string]bool)
			}
			seen[sid][tag] = true
		}
	}
	for sid, tags := range seen {
		tagList := make([]string, 0, len(tags))
		for t := range tags {
			tagList = append(tagList, t)
		}
		entries = append(entries, tagIndexEntry{SessionID: sid, Tags: tagList})
	}
	data, err := json.Marshal(entries)
	if err != nil {
		return err
	}
	if err := util.AtomicWriteFileNoFsync(s.tagIndexPath(), data, 0o600); err != nil {
		return err
	}
	// Refresh tagIndexTime so the next loadTagIndex doesn't needlessly reload
	// the file we just wrote.
	if info, err := os.Stat(s.tagIndexPath()); err == nil {
		s.tagIndexTime = info.ModTime()
	}
	return nil
}

// updateTagIndex scans messages and updates the tag index for a session
func (s *Store) updateTagIndex(sessionID string, messages []Message) {
	index := s.loadTagIndex()
	// Collect all tags from messages
	tagSet := make(map[string]bool)
	for _, msg := range messages {
		for _, tag := range msg.Tags {
			tagSet[strings.ToLower(tag)] = true
		}
	}
	// Remove old entries for this session
	for tag := range index {
		filtered := make([]string, 0, len(index[tag]))
		for _, sid := range index[tag] {
			if sid != sessionID {
				filtered = append(filtered, sid)
			}
		}
		if len(filtered) > 0 {
			index[tag] = filtered
		} else {
			delete(index, tag)
		}
	}
	// Add new entries
	for tag := range tagSet {
		index[tag] = append(index[tag], sessionID)
	}
	s.tagIndex = index
}

// SearchByTag searches sessions by tag.
// Uses the tag index for O(1) lookup; falls back to streaming scan if index is stale.
// Results are cached per tag and invalidated on any write operation.
func (s *Store) SearchByTag(tag string) ([]SessionSummary, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	// use cached results if available (5 second TTL)
	cacheKey := "tag:" + tag
	if s.cache != nil && s.cacheKey == cacheKey && time.Since(s.cacheTime) < 5*time.Second {
		return s.cache, nil
	}

	// Try tag index first for fast lookup
	index := s.loadTagIndex()
	tagLower := strings.ToLower(tag)

	var results []SessionSummary
	if sessionIDs, ok := index[tagLower]; ok && len(sessionIDs) > 0 {
		for _, sessionID := range sessionIDs {
			path := s.sessionPath(sessionID)
			info, err := os.Stat(path)
			if err != nil {
				continue
			}
			// Quick turn count from scanSessionMetadata (first 100 lines only)
			_, turnCount := s.scanSessionMetadata(sessionID)
			results = append(results, SessionSummary{
				ID:        sessionID,
				Name:      sessionID,
				TurnCount: turnCount,
				UpdatedAt: info.ModTime().UnixMilli(),
			})
		}
	} else {
		// Fallback: full scan for backward compatibility
		entries, err := os.ReadDir(s.dir)
		if err != nil {
			return nil, fmt.Errorf("failed to read session directory: %w", err)
		}

		for _, entry := range entries {
			if entry.IsDir() || filepath.Ext(entry.Name()) != ".jsonl" {
				continue
			}
			// 归档 sidecar 不是会话（同 List() 排除规则）。
			if isArchiveFileName(entry.Name()) {
				continue
			}
			sessionID := strings.TrimSuffix(entry.Name(), ".jsonl")

			info, err := entry.Info()
			if err != nil {
				continue
			}

			found, turnCount := s.scanSessionForTag(sessionID, tag)
			if found {
				results = append(results, SessionSummary{
					ID:        sessionID,
					Name:      sessionID,
					TurnCount: turnCount,
					UpdatedAt: info.ModTime().UnixMilli(),
				})
			}
		}
	}

	// cache results
	s.cache = results
	s.cacheKey = cacheKey
	s.cacheTime = time.Now()

	return results, nil
}

// scanSessionForTag scans a session file for a specific tag without loading
// the full session into memory. Returns (found, turnCount).
func (s *Store) scanSessionForTag(sessionID, tag string) (bool, int) {
	path := s.sessionPath(sessionID)
	f, err := os.Open(path)
	if err != nil {
		return false, 0
	}
	defer f.Close()

	scanner := bufio.NewScanner(f)
	buf := make([]byte, 0, 64*1024)
	scanner.Buffer(buf, 1*1024*1024)

	turnCount := 0
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}
		msg, metadata, err := decodeSessionRecord([]byte(line))
		if err != nil || metadata != nil {
			continue
		}
		if msg.Role == RoleUser {
			turnCount++
		}
		for _, t := range msg.Tags {
			if strings.EqualFold(t, tag) {
				return true, turnCount
			}
		}
	}
	return false, turnCount
}

// Import imports a session (batch write messages)
func (s *Store) Import(sessionID string, messages []Message) error {
	s.invalidateCache()
	session := NewSession()
	session.messages = messages
	return s.Save(sessionID, session)
}
