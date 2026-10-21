// Package filehistory 提供统一的文件历史追踪、快照与回滚能力。
// 替代原有的 undo / snapshot / checkpoint 三套机制，基于内容寻址存储实现。
package filehistory

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io/fs"
	"log"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/anchorandfish/whale-code/internal/filehistory/diffutil"
	"github.com/anchorandfish/whale-code/internal/platform"
)

// 常量定义
const (
	// maxContentHashSize 大文件阈值（10MB）。
	// S10 / HG3 之后生产路径不再据此"跳过 SHA256"：writeContent 一律全量 SHA256
	// （见 content.go）。本常量保留，供测试构造">10MB 大文件"用例。
	maxContentHashSize = 10 * 1024 * 1024 // 10MB
	// gcThresholdBytes content/ 目录超过此大小触发紧急 GC
	gcThresholdBytes = 500 * 1024 * 1024 // 500MB
	// contextSize diff 上下文行数（与 snapshot.Diff 一致）
	contextSize = 3
)

// FileHistory 统一文件历史系统（替代 undo + snapshot + checkpoint）
// 通过内容寻址存储记录文件变更历史，支持工具级 Undo/Redo 和会话级 Rewind。
type FileHistory struct {
	workDir       string          // 工作目录绝对路径
	baseDir       string          // 存储根目录（~/.whale/filehistory/）
	contentDir    string          // 内容寻址存储目录（baseDir/content/）
	indexFile     string          // JSONL 索引文件路径（baseDir/index.jsonl，向后兼容老索引路径）
	lockFile      string          // 跨进程文件锁路径
	gcPendingFile string          // 待 GC 清理的 hash 清单文件路径
	mu            sync.Mutex      // 保护内存状态和文件操作
	indexCache    []IndexEntry    // 索引内存缓存（启动时加载当前活跃分片）
	trackedPaths  map[string]bool // 本会话已追踪的文件路径集合
	// undoRedoStack 用于工具级 Undo/Redo（仅文件，不涉及会话）
	undoStack []string // 可 undo 的 toolCallId 栈
	redoStack []string // 可 redo 的 toolCallId 栈
	// SNAPSHOT-2：B3 会话索引分片新增字段
	indexStore IndexStore // 索引存储抽象（替代直接操作 indexFile，C16 接口契约）
	sessionId  string     // 当前活跃会话 ID（用于分片路由）
	shardDir   string     // 分片目录路径（baseDir/index/）
	metaFile   string     // 元数据文件路径（baseDir/index/index_meta.json）
}

// IndexEntry 索引记录（JSONL 每行一条）
type IndexEntry struct {
	Type            string       `json:"type"`                      // "track" | "snapshot"
	MsgId           string       `json:"msgId"`                     // 关联的消息 ID（session.Message.UUID）
	ToolCallId      string       `json:"toolCallId,omitempty"`      // track 记录专有：工具调用 ID（tools.Call.ID）
	ToolName        string       `json:"toolName,omitempty"`        // track 记录专有：工具名称
	Label           string       `json:"label,omitempty"`           // snapshot 记录专有：快照标签
	Timestamp       time.Time    `json:"timestamp"`                 // 记录时间
	Files           []FileChange `json:"files"`                     // 文件变更列表
	SessionMsgCount int          `json:"sessionMsgCount,omitempty"` // snapshot 专有：会话消息数
	// SNAPSHOT-1：本轮新增字段
	// SessionId 所属会话 ID（用于分片路由，omitempty 向后兼容老索引）
	SessionId string `json:"sessionId,omitempty"`
}

// FileChange 单个文件的变更记录
type FileChange struct {
	Path       string  `json:"path"`                 // 绝对路径
	BeforeHash *string `json:"beforeHash,omitempty"` // track: 修改前内容 hash（nil=新建文件）
	AfterHash  *string `json:"afterHash,omitempty"`  // track: 修改后内容 hash（nil=删除文件）
	Hash       *string `json:"hash,omitempty"`       // snapshot: 当前内容 hash（nil=文件已删除）
	Mode       uint32  `json:"mode"`                 // 文件权限
	// Deleted（，2026-10-08）：**TrackAfter 确实跑过、且此刻文件已不存在**。
	//
	// 为什么必须显式记：`AfterHash == nil` 有**两种**成因 ——
	// ① 工具真的删除了这个文件（apply_unified_diff 的 IsDeleted 分支会 os.Remove）；
	// ② 工具中途失败，TrackAfter 从未被调用（条目停留在"只有 BeforeHash"）。
	// 二者在旧数据模型里**不可区分**，于是任何"本轮改了什么"的消费者只能两种都闭嘴 ——
	// 那等于把①谎报成"没变更"（轮末卡片的第一版就是如此，见 handoff 回执）。
	// 生产者是唯一知道答案的一方 ⇒ 由它在字段里落事实，消费者不做启发式猜测。
	Deleted bool `json:"deleted,omitempty"`
}

// Snapshot 快照视图（从 index 中提取）
type Snapshot struct {
	MsgId           string                // 快照关联的消息 ID
	Label           string                // 快照标签
	Timestamp       time.Time             // 快照时间
	Files           map[string]*FileState // path → 状态
	SessionMsgCount int                   // 会话消息数
}

// FileState 文件状态（快照中的单文件视图）
type FileState struct {
	Hash *string // nil = 文件不存在
	Mode uint32
}

// SnapshotSummary 快照摘要（ListSnapshots 返回）
type SnapshotSummary struct {
	MsgId           string
	Label           string
	Timestamp       time.Time
	SessionMsgCount int
	FileCount       int
}

// RewindResult 回滚结果
type RewindResult struct {
	TargetMsgId     string   // 目标消息 ID
	FilesRestored   int      // 恢复的文件数
	FilesDeleted    int      // 删除的文件数
	SessionRestored bool     // 会话是否恢复成功
	BackupMsgId     string   // 自动备份的快照 msgId（用于失败回滚）
	Warnings        []string // 非致命警告（如外部修改的文件）

	// Conflicts 是 的结构化冲突面：外部修改过、因此**未被回滚**
	// 的文件。修复前这类文件只进 Warnings（散文）且**照样被覆写** —— 可靠判据
	// （内容 hash 不在本会话 track 记录中）没有接到不可逆动作上。现在：跳过 +
	// 结构化上报（R4：可判定的结构化事实不得由散文断言）。消费方按 Path 逐条
	// 处理；Reason 取 RewindConflictReason* 常量。
	Conflicts []RewindConflict
}

// RewindConflictReason 枚举冲突原因（独立命名空间，R8：不得与其它常量表互为子串）。
type RewindConflictReason string

const (
	// RewindConflictReasonExternalModified 盘上现值 hash 不在本会话 track 记录中
	// ⇒ 本会话之外的写入者改过它（另一会话 / 用户编辑器 / 其它工具）。回滚会盖掉
	// 不可逆的外部工作，故跳过。
	RewindConflictReasonExternalModified RewindConflictReason = "externally_modified"
)

// RewindConflict 描述一个被跳过的恢复目标。
type RewindConflict struct {
	Path        string               // 文件路径（快照 key，与 Track 记录一致）
	Reason      RewindConflictReason // 冲突原因
	CurrentHash *string              // 盘上现值 hash（nil = 文件当前不存在）
	TargetHash  *string              // 快照目标 hash（nil = 目标状态是「不存在」即删除）
}

func (c RewindConflict) String() string {
	cur, tgt := "<nil>", "<nil>"
	if c.CurrentHash != nil {
		cur = *c.CurrentHash
	}
	if c.TargetHash != nil {
		tgt = *c.TargetHash
	}
	return fmt.Sprintf("%s: reason=%s current=%s target=%s", c.Path, c.Reason, cur, tgt)
}

// DiffStats 差异统计结果（对齐业内既有实现 命名）
type DiffStats struct {
	Insertions   int // 新增行数
	Deletions    int // 删除行数
	FilesChanged int // 变更文件数
}

// ---------- 公共 API ----------

// NewFileHistory 创建文件历史系统实例
// baseDir 通常为 ~/.whale/filehistory/，内部按 workDir hash 隔离
// B3 改造：新增 IndexStore 接入 + 自动迁移老 index.jsonl 到分片格式
func NewFileHistory(workDir, baseDir string) (*FileHistory, error) {
	absWorkDir, err := filepath.Abs(workDir)
	if err != nil {
		return nil, fmt.Errorf("resolve work dir: %w", err)
	}

	resolved, err := filepath.EvalSymlinks(absWorkDir)
	if err == nil {
		absWorkDir = resolved
	}

	workDirHash := sha256.Sum256([]byte(absWorkDir))
	workDirHex := hex.EncodeToString(workDirHash[:])[:16]
	storageDir := filepath.Join(baseDir, workDirHex)

	oldBaseDir := filepath.Join(strings.Replace(baseDir, "filehistory", "file-history", 1), workDirHex)
	if err := migrateOldDir(oldBaseDir, storageDir); err != nil {
		log.Printf("[filehistory] migrate old dir failed (continuing with new dir): %v", err)
	}

	h := &FileHistory{
		workDir:       absWorkDir,
		baseDir:       storageDir,
		contentDir:    filepath.Join(storageDir, "content"),
		indexFile:     filepath.Join(storageDir, "index.jsonl"),
		lockFile:      filepath.Join(storageDir, "flock"),
		gcPendingFile: filepath.Join(storageDir, "gc-pending"),
		trackedPaths:  make(map[string]bool),
		// SNAPSHOT-2：B3 新增字段初始化
		shardDir:  filepath.Join(storageDir, "index"),
		metaFile:  filepath.Join(storageDir, "index", "index_meta.json"),
		sessionId: defaultSession, // 默认会话 ID（首次 SetCurrentSession 前的兜底）
	}

	if err := os.MkdirAll(h.contentDir, 0755); err != nil {
		return nil, fmt.Errorf("create content dir: %w", err)
	}

	// B3：初始化 ShardedIndexStore（优化 6 第 1 步）
	store, err := NewShardedIndexStore(h.shardDir, h.lockFile)
	if err != nil {
		return nil, fmt.Errorf("create index store: %w", err)
	}
	h.indexStore = store

	// B3：检测老 index.jsonl 是否存在 + 迁移（优化 5/6 第 2 步）
	if _, statErr := os.Stat(h.indexFile); statErr == nil {
		// index.jsonl 存在，迁移到 index_legacy.jsonl
		if migErr := h.indexStore.MigrateFromLegacy(h.indexFile); migErr != nil {
			log.Printf("[filehistory] migrate legacy index failed (continuing with new shard): %v", migErr)
		}
	}

	// B3：加载元数据 + 设置活跃会话（优化 6 第 3-4 步）
	if loadErr := store.LoadMeta(); loadErr != nil {
		log.Printf("[filehistory] load meta failed (degraded): %v", loadErr)
	}
	// 默认活跃会话：meta.ActiveSession 或 "default"
	activeSession := defaultSession
	if store.meta != nil && store.meta.ActiveSession != "" {
		activeSession = store.meta.ActiveSession
	}
	if setErr := h.indexStore.SetActiveSession(activeSession); setErr != nil {
		log.Printf("[filehistory] set active session failed: %v", setErr)
	}
	h.sessionId = activeSession

	// B3：加载当前活跃分片到 indexCache（优化 6 第 5 步）
	// S10 / HG1（决策 HG1=a，fail-closed）：损坏索引不再"降级为空"静默继续。
	// 返回错误 → 调用方（executor.go:263）将 FileHistory 置 nil → rewind/undo 不可用，
	// 且错误被上抛并可记录，提示人工恢复。禁止基于"部分加载的索引"执行回退。
	if err := h.loadIndex(); err != nil {
		return nil, fmt.Errorf("load index failed (fail-closed on corrupt index): %w", err)
	}

	if removed, freed, gcErr := h.fullGC(); gcErr != nil {
		log.Printf("[filehistory] startup GC failed (non-fatal): %v", gcErr)
	} else if removed > 0 {
		log.Printf("[filehistory] startup GC removed %d blocks, freed %s", removed, formatFileSize(freed))
	}

	return h, nil
}

// SetCurrentSession 切换当前活跃会话（优化 3）
// 内部：切换 indexStore 活跃分片 → 清空 indexCache/trackedPaths/undoStack/redoStack → 重新加载
// sessionId 为空时由 indexStore 路由到 "default" 分片
func (h *FileHistory) SetCurrentSession(sessionId string) error {
	h.mu.Lock()
	defer h.mu.Unlock()

	if sessionId == h.sessionId {
		return nil // 无需切换
	}

	// 1. 切换 indexStore 活跃分片
	if err := h.indexStore.SetActiveSession(sessionId); err != nil {
		return fmt.Errorf("set active session: %w", err)
	}
	h.sessionId = sessionId

	// 2. 清空内存状态（indexCache / trackedPaths / undoStack / redoStack）
	h.indexCache = nil
	h.trackedPaths = make(map[string]bool)
	h.undoStack = h.undoStack[:0]
	h.redoStack = h.redoStack[:0]

	// 3. 重新加载新会话分片到 indexCache
	if err := h.loadIndex(); err != nil {
		return fmt.Errorf("reload index after session switch: %w", err)
	}

	return nil
}

// GetCurrentSession 返回当前活跃会话 ID
func (h *FileHistory) GetCurrentSession() string {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.sessionId
}

// migrateOldDir 迁移旧目录 file-history 到新目录 filehistory
// 检测旧目录是否存在，若存在则复制内容到新目录，删除旧目录
func migrateOldDir(oldDir, newDir string) error {
	oldInfo, err := os.Stat(oldDir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return fmt.Errorf("stat old dir: %w", err)
	}
	if !oldInfo.IsDir() {
		return nil
	}

	newInfo, err := os.Stat(newDir)
	if err == nil && newInfo.IsDir() {
		return nil
	}

	if err := os.MkdirAll(newDir, 0755); err != nil {
		return fmt.Errorf("create new dir: %w", err)
	}

	files, err := os.ReadDir(oldDir)
	if err != nil {
		return fmt.Errorf("read old dir: %w", err)
	}

	for _, file := range files {
		oldPath := filepath.Join(oldDir, file.Name())
		newPath := filepath.Join(newDir, file.Name())

		if file.IsDir() {
			if err := migrateOldDir(oldPath, newPath); err != nil {
				return fmt.Errorf("migrate subdir %s: %w", file.Name(), err)
			}
		} else {
			content, err := os.ReadFile(oldPath)
			if err != nil {
				return fmt.Errorf("read old file %s: %w", file.Name(), err)
			}
			if err := os.WriteFile(newPath, content, 0o600); err != nil {
				return fmt.Errorf("write new file %s: %w", file.Name(), err)
			}
		}
	}

	if err := os.RemoveAll(oldDir); err != nil {
		log.Printf("[filehistory] remove old dir %s failed (continuing): %v", oldDir, err)
	}

	log.Printf("[filehistory] migrated old dir %s to %s", oldDir, newDir)
	return nil
}

// safePath 安全路径解析：断言路径位于工作目录内，返回规范化的绝对路径。
//
// ★ 2026-09-19 收敛（同一语义多处实现）：本函数原先自行实现"是否在 workDir 之内"
// （`strings.HasPrefix(ToLower(absPath), ToLower(workDir))` —— **裸前缀、无分隔符边界** + 无条件 ToLower），
// 与 platform.IsPathAllowed 分歧。实测反例：workDir=`…/proj` 时，`…/proj-secret/creds.txt`
// 被裸前缀判为"在内"而放行（正确实现拒绝）—— 见 path_containment_convergence_test.go。
//
// 现改为复用 platform.ResolveWithin（全仓唯一的包含判定 SSOT）：
// - **分隔符边界**正确 ⇒ 同前缀兄弟目录不再被误放行
// - **大小写跟随文件系统语义**（CaseInsensitiveFS）⇒ Linux 上不再把 `/WORK/FOO` 当作 `/work/foo`
// - 路径**尚不存在**仍可解析（Track 早于文件创建）⇒ 解析最深已存在祖先
// - **dangling symlink**（存在但解析失败）fail-closed ⇒ 不再被当作"尚未创建"而放行
func (h *FileHistory) safePath(p string) (string, error) {
	resolved, err := platform.ResolveWithin(p, []string{h.workDir})
	if err != nil {
		return "", fmt.Errorf("path traversal detected: %w", err)
	}
	return resolved, nil
}

// Track 追踪文件变更前状态（工具执行前调用）
// 记录当前文件内容到内容寻址存储，关联到 messageId
// paths 为工具即将修改的文件路径列表
func (h *FileHistory) Track(msgId, toolCallId, toolName string, paths []string) error {
	h.mu.Lock()
	defer h.mu.Unlock()

	if msgId == "" {
		return nil
	}

	var changes []FileChange
	for _, p := range paths {
		absPath, err := h.safePath(p)
		if err != nil {
			return err
		}

		mode := recordFileMode(absPath)
		var beforeHash *string

		content, err := os.ReadFile(absPath)
		if err == nil {
			hash, wErr := writeContent(h.contentDir, content)
			if wErr != nil {
				return fmt.Errorf("write before content for %s: %w", absPath, wErr)
			}
			beforeHash = &hash
		}

		changes = append(changes, FileChange{
			Path:       absPath,
			BeforeHash: beforeHash,
			Mode:       mode,
		})

		h.trackedPaths[absPath] = true
	}

	entry := IndexEntry{
		Type:       "track",
		MsgId:      msgId,
		ToolCallId: toolCallId,
		ToolName:   toolName,
		Timestamp:  time.Now(),
		Files:      changes,
		// SNAPSHOT-3：B3 新增 - 填入当前会话 ID 用于分片路由
		SessionId: h.sessionId,
	}

	if err := h.appendIndexEntry(entry); err != nil {
		return fmt.Errorf("append track entry: %w", err)
	}

	// 推入 undo 栈，清空 redo 栈（标准 undo/redo 行为）
	h.undoStack = append(h.undoStack, toolCallId)
	h.redoStack = h.redoStack[:0]

	return nil
}

// TrackAfter 追踪文件变更后状态（工具执行后调用）
// 记录修改后的文件内容，与 Track 配对使用
func (h *FileHistory) TrackAfter(msgId, toolCallId string, paths []string) error {
	h.mu.Lock()
	defer h.mu.Unlock()

	if msgId == "" {
		return nil
	}

	// 先收集所有变更（不修改内存），确保内容写入成功
	// 记录需要更新的索引位置和新值
	type fileUpdate struct {
		entryIdx  int
		fileIdx   int
		afterHash *string
		mode      uint32
		deleted   bool
	}
	var updates []fileUpdate

	for i, entry := range h.indexCache {
		if entry.Type != "track" || entry.MsgId != msgId || entry.ToolCallId != toolCallId {
			continue
		}

		for _, p := range paths {
			absPath, err := h.safePath(p)
			if err != nil {
				return err
			}

			for j, fc := range entry.Files {
				if fc.Path != absPath {
					continue
				}

				content, rErr := os.ReadFile(absPath)
				var afterHash *string
				var mode uint32
				// 文件不存在 → AfterHash 为 nil（表示删除）
				// 保留原 Mode：TrackAfter 只更新 AfterHash/Mode，文件删除时
				// Mode 应保留 Track 时记录的值，否则会被覆盖为 0（0o000），
				// 导致 Undo 时 restoreFile 用 os.FileMode(0) 写文件，
				// 写出的文件无任何权限 → os.ReadFile 报 permission denied。
				// 用 fc.Mode（原 Track 记录的 mode）而非 0。
				// Bug: B6-P1-5 / 参考 ADR: filehistory-trackafter-mode-preservation
				//
				// 此处同时落 Deleted 事实（"读不到"≠"被删" —— 只认 NotExist）。
				deleted := errors.Is(rErr, fs.ErrNotExist)
				if rErr != nil {
					mode = fc.Mode
				} else {
					hash, wErr := writeContent(h.contentDir, content)
					if wErr != nil {
						return fmt.Errorf("write after content for %s: %w", absPath, wErr)
					}
					afterHash = &hash
					mode = recordFileMode(absPath)
				}
				updates = append(updates, fileUpdate{entryIdx: i, fileIdx: j, afterHash: afterHash, mode: mode, deleted: deleted})
			}
		}
	}

	if len(updates) == 0 {
		return fmt.Errorf("track entry not found for msgId=%s toolCallId=%s", msgId, toolCallId)
	}

	// 先修改内存，再写盘
	for _, u := range updates {
		h.indexCache[u.entryIdx].Files[u.fileIdx].AfterHash = u.afterHash
		h.indexCache[u.entryIdx].Files[u.fileIdx].Mode = u.mode
		h.indexCache[u.entryIdx].Files[u.fileIdx].Deleted = u.deleted
	}

	// 重写索引文件（因为 JSONL 是追加写的，更新需要全量重写）
	if err := h.rewriteIndex(); err != nil {
		// 写盘失败，回滚内存修改
		for _, u := range updates {
			h.indexCache[u.entryIdx].Files[u.fileIdx].AfterHash = nil
			h.indexCache[u.entryIdx].Files[u.fileIdx].Deleted = false
		}
		return fmt.Errorf("rewrite index: %w", err)
	}

	return nil
}

// Snapshot 创建快照（每轮对话结束调用）
// 记录当前所有 tracked 文件的内容 hash 列表，关联到 messageId
func (h *FileHistory) Snapshot(msgId, label string, sessionMsgCount int) (*Snapshot, error) {
	h.mu.Lock()
	defer h.mu.Unlock()

	if msgId == "" {
		return nil, fmt.Errorf("msgId must not be empty")
	}

	var changes []FileChange
	for path := range h.trackedPaths {
		state, err := h.getCurrentFileState(path)
		if err != nil {
			return nil, fmt.Errorf("get current state for %s: %w", path, err)
		}

		fc := FileChange{
			Path: path,
			Mode: state.Mode,
		}
		if state.Hash != nil {
			fc.Hash = state.Hash
		}
		changes = append(changes, fc)
	}

	entry := IndexEntry{
		Type:            "snapshot",
		MsgId:           msgId,
		Label:           label,
		Timestamp:       time.Now(),
		Files:           changes,
		SessionMsgCount: sessionMsgCount,
		// SNAPSHOT-3：B3 新增 - 填入当前会话 ID 用于分片路由
		SessionId: h.sessionId,
	}

	if err := h.appendIndexEntry(entry); err != nil {
		return nil, fmt.Errorf("append snapshot entry: %w", err)
	}

	// 构建并返回快照视图
	snap := &Snapshot{
		MsgId:           msgId,
		Label:           label,
		Timestamp:       entry.Timestamp,
		Files:           make(map[string]*FileState),
		SessionMsgCount: sessionMsgCount,
	}
	for _, fc := range changes {
		snap.Files[fc.Path] = &FileState{
			Hash: fc.Hash,
			Mode: fc.Mode,
		}
	}

	return snap, nil
}

// Rewind 回滚到指定快照（用户请求回退时调用）
// 同时回滚文件系统 + 会话状态到指定 messageId
// sessionStore 为 nil 时跳过会话恢复（仅恢复文件）
func (h *FileHistory) Rewind(msgId string, sessionStore SessionRestorer, currentSessionId string) (*RewindResult, error) {
	h.mu.Lock()
	defer h.mu.Unlock()

	// 安全检查：拒绝空 msgId（防止误删数据）
	if msgId == "" {
		return nil, fmt.Errorf("msgId must not be empty (safety check)")
	}

	// 优化 4：跨会话防御性检查 - msgId 必须在当前活跃分片
	// 跨会话 Rewind 不支持（数据隔离设计决策）
	found, err := h.indexStore.FindEntry(msgId, "snapshot")
	if err != nil {
		return nil, fmt.Errorf("find snapshot for msgId=%s: %w", msgId, err)
	}
	if found == nil {
		// 兼容老索引：snapshot 记录可能不存在，从 track 记录重建（与原逻辑一致）
		// 但仍需校验 msgId 在当前会话有 track 记录
		trackFound, _ := h.indexStore.FindEntry(msgId, "track")
		if trackFound == nil {
			return nil, fmt.Errorf("snapshot/track not found in current session %q for msgId=%s (cross-session rewind not supported)", h.sessionId, msgId)
		}
	}

	// 构建目标快照
	snap, err := h.buildSnapshot(msgId)
	if err != nil {
		return nil, fmt.Errorf("build target snapshot: %w", err)
	}
	if snap == nil {
		return nil, fmt.Errorf("snapshot not found for msgId=%s", msgId)
	}

	result := &RewindResult{
		TargetMsgId: msgId,
		Warnings:    []string{},
	}

	// Rewind 前创建备份快照。
	// S10 / HG2（决策 HG2=a）：best-effort 回滚仅在"确实存在可回退的 rewind-backup"时
	// 才可接受。若连备份都无法创建，说明没有安全网——此时必须在**改动任何文件之前**
	// 中止，而不是带着空备份继续（原实现只记 warning 后继续）。
	backupMsgId, err := h.createBackupSnapshot()
	if err != nil {
		return result, fmt.Errorf("refusing to rewind: backup snapshot could not be created, no rollback safety net: %w", err)
	}
	result.BackupMsgId = backupMsgId

	// 差分恢复文件
	restored, deleted, warnings, conflicts, err := h.diffAndRestore(snap)
	if err != nil {
		return result, fmt.Errorf("diff and restore: %w", err)
	}
	result.FilesRestored = restored
	result.FilesDeleted = deleted
	result.Warnings = append(result.Warnings, warnings...)
	result.Conflicts = append(result.Conflicts, conflicts...)

	// 截断索引（删除 msgId 之后的所有记录）
	// 截断失败视为致命错误：文件已恢复但索引未更新会导致数据不一致
	if err := h.truncateIndexAfter(msgId); err != nil {
		return result, fmt.Errorf("truncate index failed after file restore: %w", err)
	}

	// 恢复会话状态
	if sessionStore != nil {
		if err := h.restoreSession(msgId, sessionStore, currentSessionId); err != nil {
			result.Warnings = append(result.Warnings, fmt.Sprintf("session restore failed: %v", err))
		} else {
			result.SessionRestored = true
		}
	}

	// 清空 undo/redo 栈（Rewind 后历史已截断）
	h.undoStack = h.undoStack[:0]
	h.redoStack = h.redoStack[:0]

	// B6-P1-2: Rewind 截断索引后触发紧急 GC，清理截断产生的无引用 content 块。
	// 避免等待下次启动 fullGC，及时释放磁盘空间。GC 失败为非致命，仅记录日志。
	if removed, freed, gcErr := h.emergencyGC(gcThresholdBytes); gcErr != nil {
		log.Printf("[filehistory] Rewind emergencyGC failed (non-fatal): %v", gcErr)
	} else if removed > 0 {
		log.Printf("[filehistory] Rewind emergencyGC removed %d blocks, freed %s", removed, formatFileSize(freed))
	}

	// 截断的 content 块由 fullGC 在下次启动时清理

	return result, nil
}

// GetSnapshotFromSession 从指定源会话读取快照（候选 A，跨会话恢复）。
// 通过 ScanAllShards 全分片扫描 + 按 sessionId+msgId 过滤，构建 Snapshot。
// 落实审查建议优化 1：按 sessionId 过滤，避免遍历无关分片结果。
// 对标同类 CLI 跨会话 checkpoint 恢复（@2026-03-31快照）。
// 注意：仅查找 snapshot 类型记录（跨会话 track 重建过于复杂且非必要）。
func (h *FileHistory) GetSnapshotFromSession(sourceSessionId, msgId string) (*Snapshot, error) {
	h.mu.Lock()
	defer h.mu.Unlock()

	if sourceSessionId == "" || msgId == "" {
		return nil, fmt.Errorf("sourceSessionId and msgId must not be empty")
	}

	// ScanAllShards 扫描所有分片（跨会话读取）
	entries, err := h.indexStore.ScanAllShards()
	if err != nil {
		return nil, fmt.Errorf("scan all shards: %w", err)
	}

	// 按 sessionId + msgId 过滤 snapshot 记录（落实审查建议优化 1）
	var targetEntry *IndexEntry
	for i := range entries {
		e := &entries[i]
		if e.Type == "snapshot" && e.MsgId == msgId && e.SessionId == sourceSessionId {
			targetEntry = e
			break
		}
	}
	if targetEntry == nil {
		return nil, fmt.Errorf("snapshot not found: sessionId=%s msgId=%s", sourceSessionId, msgId)
	}

	// 从 IndexEntry 构建 Snapshot
	snap := &Snapshot{
		MsgId:           targetEntry.MsgId,
		Label:           targetEntry.Label,
		Timestamp:       targetEntry.Timestamp,
		Files:           make(map[string]*FileState),
		SessionMsgCount: targetEntry.SessionMsgCount,
	}
	for _, fc := range targetEntry.Files {
		snap.Files[fc.Path] = &FileState{
			Hash: fc.Hash,
			Mode: fc.Mode,
		}
	}
	return snap, nil
}

// RestoreFromSnapshot 应用外部快照到当前工作区 + 恢复目标会话消息（候选 A，跨会话恢复）。
// 复用 diffAndRestore（文件恢复 + rollback）+ restoreSession（会话消息截断）。
// 落实审查建议优化 2：恢复前 log warning 提示跨会话恢复。
// 注意：不截断当前会话索引（跨会话恢复不修改当前会话历史，仅恢复文件 + 会话消息）。
// 对标同类 CLI 跨会话 checkpoint restore（@2026-03-31快照）。
func (h *FileHistory) RestoreFromSnapshot(snap *Snapshot, sessionStore SessionRestorer, targetSessionId string) (*RewindResult, error) {
	h.mu.Lock()
	defer h.mu.Unlock()

	if snap == nil {
		return nil, fmt.Errorf("snapshot must not be nil")
	}

	log.Printf("[filehistory] RestoreFromSnapshot: cross-session restore from snapshot msgId=%s to session=%s", snap.MsgId, targetSessionId)

	result := &RewindResult{
		TargetMsgId: snap.MsgId,
		Warnings:    []string{},
	}

	// 创建备份快照（当前工作区状态，用于失败回滚）。
	// S10 / HG2（决策 HG2=a）：与 Rewind 一致——备份创建失败则拒绝恢复，不留"无安全网"的中间态。
	backupMsgId, err := h.createBackupSnapshot()
	if err != nil {
		return result, fmt.Errorf("refusing to restore: backup snapshot could not be created, no rollback safety net: %w", err)
	}
	result.BackupMsgId = backupMsgId

	// 差分恢复文件（复用 diffAndRestore，含 rollback 机制）
	restored, deleted, warnings, conflicts, err := h.diffAndRestore(snap)
	if err != nil {
		return result, fmt.Errorf("diff and restore: %w", err)
	}
	result.FilesRestored = restored
	result.FilesDeleted = deleted
	result.Warnings = append(result.Warnings, warnings...)
	result.Conflicts = append(result.Conflicts, conflicts...)

	// 恢复会话消息（复用 restoreSession）
	// 注意：restoreSession 内部通过 indexCache 查找 msgId 的 SessionMsgCount，
	// 跨会话场景下 indexCache 可能不含该 msgId → restoreSession 返回 error，
	// 此时仅记录 warning，文件恢复仍成功。
	if sessionStore != nil {
		if err := h.restoreSession(snap.MsgId, sessionStore, targetSessionId); err != nil {
			result.Warnings = append(result.Warnings, fmt.Sprintf("session restore failed (cross-session): %v", err))
		} else {
			result.SessionRestored = true
		}
	}

	return result, nil
}

// Undo 撤销最近一次工具调用的文件变更（工具级 undo）
// 仅回退文件，不回退会话消息
func (h *FileHistory) Undo(msgId string) (*IndexEntry, error) {
	h.mu.Lock()
	defer h.mu.Unlock()

	// 从 undoStack 末尾向前查找属于 msgId 的 toolCallId
	for i := len(h.undoStack) - 1; i >= 0; i-- {
		tcId := h.undoStack[i]
		entry := h.findTrackEntry(msgId, tcId)
		if entry == nil {
			continue
		}

		// 恢复每个文件到 BeforeHash 状态
		for _, fc := range entry.Files {
			if fc.BeforeHash != nil {
				if err := h.restoreFile(fc.Path, fc.BeforeHash, fc.Mode); err != nil {
					return nil, fmt.Errorf("undo restore %s: %w", fc.Path, err)
				}
			} else if fc.AfterHash != nil {
				// BeforeHash 为 nil 表示文件是新建的，undo 时删除
				if err := h.deleteFile(fc.Path); err != nil {
					return nil, fmt.Errorf("undo delete %s: %w", fc.Path, err)
				}
			}
		}

		// 从 undoStack 移除，推入 redoStack
		h.undoStack = append(h.undoStack[:i], h.undoStack[i+1:]...)
		h.redoStack = append(h.redoStack, tcId)

		return entry, nil
	}

	return nil, fmt.Errorf("no undoable tool call for msgId=%s", msgId)
}

// Redo 重做最近一次撤销的工具调用
func (h *FileHistory) Redo(msgId string) (*IndexEntry, error) {
	h.mu.Lock()
	defer h.mu.Unlock()

	// 从 redoStack 末尾向前查找属于 msgId 的 toolCallId
	for i := len(h.redoStack) - 1; i >= 0; i-- {
		tcId := h.redoStack[i]
		entry := h.findTrackEntry(msgId, tcId)
		if entry == nil {
			continue
		}

		// 恢复每个文件到 AfterHash 状态
		for _, fc := range entry.Files {
			if fc.AfterHash != nil {
				if err := h.restoreFile(fc.Path, fc.AfterHash, fc.Mode); err != nil {
					return nil, fmt.Errorf("redo restore %s: %w", fc.Path, err)
				}
			} else if fc.BeforeHash != nil {
				// AfterHash 为 nil 表示文件被删除，redo 时删除
				if err := h.deleteFile(fc.Path); err != nil {
					return nil, fmt.Errorf("redo delete %s: %w", fc.Path, err)
				}
			}
		}

		// 从 redoStack 移除，推入 undoStack
		h.redoStack = append(h.redoStack[:i], h.redoStack[i+1:]...)
		h.undoStack = append(h.undoStack, tcId)

		return entry, nil
	}

	return nil, fmt.Errorf("no redoable tool call for msgId=%s", msgId)
}

// CanUndo 检查是否有可撤销的工具调用
func (h *FileHistory) CanUndo(msgId string) bool {
	h.mu.Lock()
	defer h.mu.Unlock()

	for i := len(h.undoStack) - 1; i >= 0; i-- {
		tcId := h.undoStack[i]
		if entry := h.findTrackEntry(msgId, tcId); entry != nil {
			return true
		}
	}
	return false
}

// CanRedo 检查是否有可重做的工具调用
func (h *FileHistory) CanRedo(msgId string) bool {
	h.mu.Lock()
	defer h.mu.Unlock()

	for i := len(h.redoStack) - 1; i >= 0; i-- {
		tcId := h.redoStack[i]
		if entry := h.findTrackEntry(msgId, tcId); entry != nil {
			return true
		}
	}
	return false
}

// DiffStats 返回两个快照间的文件变更统计
// 对齐业内既有实现 命名：Insertions/Deletions 为行级统计，FilesChanged 为文件数
func (h *FileHistory) DiffStats(fromMsgId, toMsgId string) (*DiffStats, error) {
	h.mu.Lock()
	defer h.mu.Unlock()

	fromSnap, err := h.buildSnapshot(fromMsgId)
	if err != nil {
		return nil, fmt.Errorf("build from snapshot: %w", err)
	}
	toSnap, err := h.buildSnapshot(toMsgId)
	if err != nil {
		return nil, fmt.Errorf("build to snapshot: %w", err)
	}

	fromFiles := make(map[string]*FileState)
	if fromSnap != nil {
		for k, v := range fromSnap.Files {
			fromFiles[k] = v
		}
	}

	toFiles := make(map[string]*FileState)
	if toSnap != nil {
		for k, v := range toSnap.Files {
			toFiles[k] = v
		}
	}

	var filesChanged int
	var totalInsertions, totalDeletions int

	// 遍历 to 快照中的文件：新增 + 修改
	for path, toState := range toFiles {
		fromState, exists := fromFiles[path]

		if !exists || fromState.Hash == nil {
			// 新增文件：整文件行数计入 Insertions
			if toState.Hash != nil {
				filesChanged++
				insertions, _ := h.countLinesForHash(toState.Hash)
				totalInsertions += insertions
			}
			continue
		}

		// 修改文件：行级 diff 统计
		if fromState.Hash != nil && toState.Hash != nil && *fromState.Hash != *toState.Hash {
			filesChanged++
			ins, del := h.lineDiffForHashes(fromState.Hash, toState.Hash)
			totalInsertions += ins
			totalDeletions += del
		}
	}

	// 遍历 from 快照中的文件：删除
	for path, fromState := range fromFiles {
		if fromState.Hash == nil {
			continue
		}
		toState, exists := toFiles[path]
		if !exists || toState.Hash == nil {
			// 删除文件：整文件行数计入 Deletions
			filesChanged++
			deletions, _ := h.countLinesForHash(fromState.Hash)
			totalDeletions += deletions
		}
	}

	return &DiffStats{
		Insertions:   totalInsertions,
		Deletions:    totalDeletions,
		FilesChanged: filesChanged,
	}, nil
}

// countLinesForHash 从内容存储读取指定 hash 的内容并统计行数
func (h *FileHistory) countLinesForHash(hash *string) (lineCount int, err error) {
	if hash == nil {
		return 0, nil
	}
	content, err := readContent(h.contentDir, *hash)
	if err != nil {
		return 0, err
	}
	if len(content) == 0 {
		return 0, nil
	}
	return strings.Count(string(content), "\n") + 1, nil
}

// lineDiffForHashes 对比两个 hash 的内容，返回行级 Insertions/Deletions
// 复用 diffutil.DiffStats 算法
func (h *FileHistory) lineDiffForHashes(fromHash, toHash *string) (insertions, deletions int) {
	if fromHash == nil || toHash == nil {
		return 0, 0
	}

	fromContent, fromErr := readContent(h.contentDir, *fromHash)
	if fromErr != nil {
		return 0, 0
	}
	toContent, toErr := readContent(h.contentDir, *toHash)
	if toErr != nil {
		return 0, 0
	}

	return diffutil.DiffStats(string(fromContent), string(toContent))
}

// ListSnapshots 列出所有快照摘要（供 /checkpoint list 等命令展示）
func (h *FileHistory) ListSnapshots() ([]SnapshotSummary, error) {
	h.mu.Lock()
	defer h.mu.Unlock()

	var summaries []SnapshotSummary
	for _, entry := range h.indexCache {
		if entry.Type != "snapshot" {
			continue
		}
		summaries = append(summaries, SnapshotSummary{
			MsgId:           entry.MsgId,
			Label:           entry.Label,
			Timestamp:       entry.Timestamp,
			SessionMsgCount: entry.SessionMsgCount,
			FileCount:       len(entry.Files),
		})
	}
	return summaries, nil
}

// GetSnapshot 获取指定 messageId 的快照详情
func (h *FileHistory) GetSnapshot(msgId string) (*Snapshot, error) {
	h.mu.Lock()
	defer h.mu.Unlock()

	return h.buildSnapshot(msgId)
}

// GC 清理无引用的内容块（启动时调用）
func (h *FileHistory) GC() (removedCount int, freedBytes int64, err error) {
	h.mu.Lock()
	defer h.mu.Unlock()

	// 先执行懒回收（处理 gc-pending 清单）
	lazyRemoved, lazyFreed, _ := h.lazyGC()

	// 再执行全量 GC
	fullRemoved, fullFreed, err := h.fullGC()

	return lazyRemoved + fullRemoved, lazyFreed + fullFreed, err
}

// ---------- 内部方法 ----------

// loadIndex 启动时加载索引文件到内存缓存
// 设计说明：undo/redo 栈状态不持久化到索引（避免每次 Undo/Redo 都全量重写索引）
// 重启后所有 track 记录重新加入 undoStack，已 Undo 的操作会重新可 Undo。
// 这是有意的设计决策：重启后用户通常不会立即 Undo 历史操作，且持久化 undo 状态
// 需在 IndexEntry 增加 Undone 字段并在每次 Undo/Redo 时 rewriteIndex，性能开销过大。
// B3 改造：通过 indexStore.ScanEntries() 加载当前活跃分片（替代直接 scanEntries(h.indexFile)）
func (h *FileHistory) loadIndex() error {
	entries, err := h.indexStore.ScanEntries()
	if err != nil {
		return err
	}
	h.indexCache = entries

	// 从索引中重建 trackedPaths 和 undoStack
	// 注意：此处将所有 track 记录加入 undoStack，不区分运行时是否已被 Undo
	for _, entry := range entries {
		if entry.Type == "track" {
			h.undoStack = append(h.undoStack, entry.ToolCallId)
			for _, fc := range entry.Files {
				h.trackedPaths[fc.Path] = true
			}
		}
	}

	return nil
}

// appendIndexEntry 追加一条索引记录到 JSONL 文件
// B3 改造：通过 indexStore.AppendEntry() 路由到当前活跃分片
func (h *FileHistory) appendIndexEntry(entry IndexEntry) error {
	// SNAPSHOT-3：若 entry.SessionId 为空，填入当前 sessionId（用于分片路由）
	if entry.SessionId == "" {
		entry.SessionId = h.sessionId
	}
	if err := h.indexStore.AppendEntry(entry); err != nil {
		return err
	}
	// 同步更新内存缓存
	h.indexCache = append(h.indexCache, entry)
	return nil
}

// truncateIndexAfter 删除指定 messageId 之后的所有索引记录
// B3 改造：通过 indexStore.TruncateAfter() 截断当前活跃分片
func (h *FileHistory) truncateIndexAfter(msgId string) error {
	if err := h.indexStore.TruncateAfter(msgId); err != nil {
		return err
	}
	// 同步更新内存缓存：保留 msgId 及之前的记录
	cutIdx := -1
	for i, entry := range h.indexCache {
		if entry.MsgId == msgId {
			cutIdx = i
		}
	}
	if cutIdx >= 0 {
		h.indexCache = h.indexCache[:cutIdx+1]
	}
	return nil
}

// rewriteIndex 全量重写索引文件（用于 TrackAfter 更新已有记录）
// 使用 util.WithFileLock 保护跨进程并发（与 appendEntry/truncateAfter 一致）
// B3 改造：通过 indexStore.RewriteIndex() 重写当前活跃分片（优化 2 校验 sessionId 一致性）
func (h *FileHistory) rewriteIndex() error {
	return h.indexStore.RewriteIndex(h.indexCache)
}

// buildSnapshot 从索引中提取指定 messageId 的快照视图
func (h *FileHistory) buildSnapshot(msgId string) (*Snapshot, error) {
	// 优先查找 snapshot 类型的记录
	for _, entry := range h.indexCache {
		if entry.Type == "snapshot" && entry.MsgId == msgId {
			snap := &Snapshot{
				MsgId:           entry.MsgId,
				Label:           entry.Label,
				Timestamp:       entry.Timestamp,
				Files:           make(map[string]*FileState),
				SessionMsgCount: entry.SessionMsgCount,
			}
			for _, fc := range entry.Files {
				snap.Files[fc.Path] = &FileState{
					Hash: fc.Hash,
					Mode: fc.Mode,
				}
			}
			return snap, nil
		}
	}

	// 没有 snapshot 记录时，从 track 记录重建
	fileStates := make(map[string]*FileState)
	found := false
	for _, entry := range h.indexCache {
		if entry.MsgId != msgId {
			continue
		}
		if entry.Type != "track" {
			continue
		}
		found = true
		for _, fc := range entry.Files {
			// 使用 AfterHash（如果有），否则用 BeforeHash
			var hash *string
			if fc.AfterHash != nil {
				hash = fc.AfterHash
			} else if fc.BeforeHash != nil {
				// Track 记录了 before 但没有 after（工具执行失败）
				// 文件状态保持不变（before 状态）
				continue
			}
			fileStates[fc.Path] = &FileState{
				Hash: hash,
				Mode: fc.Mode,
			}
		}
	}

	if !found {
		return nil, nil
	}

	return &Snapshot{
		MsgId: msgId,
		Files: fileStates,
	}, nil
}

// getCurrentFileState 获取指定文件当前的内容 hash
func (h *FileHistory) getCurrentFileState(path string) (*FileState, error) {
	mode := recordFileMode(path)
	content, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return &FileState{Hash: nil, Mode: 0}, nil
		}
		return nil, err
	}

	hash, err := writeContent(h.contentDir, content)
	if err != nil {
		return nil, fmt.Errorf("write content: %w", err)
	}

	return &FileState{Hash: &hash, Mode: mode}, nil
}

// findTrackEntry 在索引缓存中查找指定 msgId + toolCallId 的 track 记录
func (h *FileHistory) findTrackEntry(msgId, toolCallId string) *IndexEntry {
	for i := range h.indexCache {
		entry := &h.indexCache[i]
		if entry.Type == "track" && entry.MsgId == msgId && entry.ToolCallId == toolCallId {
			return entry
		}
	}
	return nil
}

// recordFileMode 读取文件权限位，文件不存在时返回默认权限 0644
func recordFileMode(path string) uint32 {
	info, err := os.Stat(path)
	if err != nil {
		return 0644
	}
	return uint32(info.Mode().Perm())
}
