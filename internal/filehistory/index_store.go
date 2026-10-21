package filehistory

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/anchorandfish/whale-code/internal/util"
)

// IndexStore 抽象索引存储接口（C16 主导接口，B3 落地）
// 对上层 FileHistory 透明：单文件模式或分片模式由实现决定
// 对标：[VSCode Workspace Storage Sharding] https://code.visualstudio.com/docs/editor/workspaces @2026-07-21
type IndexStore interface {
	// AppendEntry 追加一条索引记录到当前活跃分片
	// 注意：entry.SessionId 由调用方填写；若为空，ShardedIndexStore 会填入当前 activeSession
	AppendEntry(entry IndexEntry) error

	// ScanEntries 扫描当前活跃分片的所有记录（启动加载用）
	// 注意：仅扫描活跃分片，不扫描其他分片（性能优化，启动 O(N_current)）
	ScanEntries() ([]IndexEntry, error)

	// FindEntry 在当前活跃分片中查找指定 msgId + type 的记录（优化 1）
	// 注意：仅扫描活跃分片；跨分片查找走 ScanAllShards() + 调用方过滤
	// 返回第一条匹配的记录，未找到时返回 nil
	FindEntry(msgId, entryType string) (*IndexEntry, error)

	// TruncateAfter 删除指定 msgId 之后的所有记录（仅当前活跃分片，优化 2）
	// 实现策略：读取 msgId 前的记录（含 msgId 本身）→ 原子写新文件 → rename 替换
	TruncateAfter(msgId string) error

	// RewriteIndex 全量重写当前活跃分片（用于 TrackAfter 更新已有记录）
	// 注意：参数 entries 必须全部属于当前 activeSession，否则报错（优化 2）
	RewriteIndex(entries []IndexEntry) error

	// ScanAllShards 扫描所有分片（M23-03 跨会话搜索用，C16 复用接口）
	// 实现策略：os.ReadDir(shardDir) + 串行 scanEntries 每个分片
	// 损坏分片跳过，记录 warning 日志
	ScanAllShards() ([]IndexEntry, error)

	// SetActiveSession 切换当前活跃会话分片（按 sessionId 路由）
	// sessionId 为空时路由到默认分片 "default"
	// sessionId 经 SHA256 取前 16 位作为文件名（防路径注入）
	SetActiveSession(sessionId string) error

	// ActiveShardPath 返回当前活跃分片文件路径（GC 等内部模块用）
	ActiveShardPath() string

	// MigrateFromLegacy 迁移老 index.jsonl 到分片格式（幂等，优化 5）
	// 策略：先写 index_meta.json.tmp 标记迁移中 → rename index.jsonl → index_legacy.jsonl → 写 index_meta.json
	// 中途失败回滚：恢复 index.jsonl + 删除 index_legacy.jsonl + 删除 .tmp
	MigrateFromLegacy(legacyFile string) error
}

// IndexMeta 分片元数据（index_meta.json）
// 记录活跃会话、所有分片信息、最后迁移时间
// 对标 [Leveldb MANIFEST] https://github.com/google/leveldb/blob/master/doc/impl.md @2026-07-21
type IndexMeta struct {
	Version        int         `json:"version"`        // 元数据版本（当前为 1）
	ActiveSession  string      `json:"activeSession"`  // 最近活跃会话 ID
	Shards         []ShardInfo `json:"shards"`         // 所有分片信息
	LastMigratedAt time.Time   `json:"lastMigratedAt"` // 最后迁移时间（零值表示迁移中）
}

// ShardInfo 单分片元信息
type ShardInfo struct {
	SessionId    string    `json:"sessionId"`    // 会话 ID
	Filename     string    `json:"filename"`     // 分片文件名（如 index_abc123.jsonl）
	EntryCount   int       `json:"entryCount"`   // 记录数
	LastModified time.Time `json:"lastModified"` // 最后修改时间
}

// 分片相关常量
const (
	// metaVersion 元数据版本号
	metaVersion = 1
	// defaultSession 默认会话 ID（空 sessionId 路由目标）
	defaultSession = "default"
	// legacySession 老索引迁移后会话 ID
	legacySession = "legacy"
	// legacyFilename 老索引迁移后的文件名
	legacyFilename = "index_legacy.jsonl"
	// defaultFilename 默认会话的分片文件名
	defaultFilename = "index_default.jsonl"
	// metaFilename 元数据文件名
	metaFilename = "index_meta.json"
	// metaTmpSuffix 元数据临时文件后缀（迁移中标记）
	metaTmpSuffix = ".tmp"
	// shardFilenamePrefix 分片文件名前缀
	shardFilenamePrefix = "index_"
	// shardFilenameSuffix 分片文件名后缀
	shardFilenameSuffix = ".jsonl"
)

// sessionToFilename 将 sessionId 转为分片文件名
// 策略：sessionId 经 SHA256 取前 16 位 hex → "index_<hex16>.jsonl"
// 空 sessionId 或 "default" 返回 "index_default.jsonl"（与 defaultSession 常量对齐）
// "legacy" 特殊值返回 "index_legacy.jsonl"（迁移目标固定）
// 防路径注入：sessionId 含 `../`、`:`、`\`、`*`、`?` 等特殊字符经 SHA256 后变为合法 hex
func sessionToFilename(sessionId string) string {
	if sessionId == "" || sessionId == defaultSession {
		return defaultFilename
	}
	if sessionId == legacySession {
		return legacyFilename
	}
	// SHA256 取前 16 位 hex（8 字节），保证文件名长度可控且跨平台合法
	sum := sha256.Sum256([]byte(sessionId))
	hex16 := hex.EncodeToString(sum[:])[:16]
	return shardFilenamePrefix + hex16 + shardFilenameSuffix
}

// filenameToSession 反推分片文件名对应的 sessionId（仅用于元数据重建）
// 注意：SHA256 不可逆，本函数仅在 meta.Shards 中查找；找不到返回空字符串
func filenameToSession(filename string, shards []ShardInfo) string {
	for _, s := range shards {
		if s.Filename == filename {
			return s.SessionId
		}
	}
	// 默认/老索引分片按文件名推断
	if filename == defaultFilename {
		return defaultSession
	}
	if filename == legacyFilename {
		return legacySession
	}
	return ""
}

// ---------- ShardedIndexStore 实现 ----------

// ShardedIndexStore 分片索引存储实现（按 sessionId 分片）
// 文件命名：index_<sessionId-hash16>.jsonl
// 元数据：index_meta.json（原子写）
type ShardedIndexStore struct {
	shardDir        string     // 分片目录路径（baseDir/index/）
	lockFile        string     // 跨进程文件锁路径（与 FileHistory 共享 storageDir/flock）
	metaFile        string     // 元数据文件路径（shardDir/index_meta.json）
	metaTmpFile     string     // 元数据临时文件（迁移中标记，优化 5）
	mu              sync.Mutex // 保护 activeSession / activeShardPath / meta
	activeSession   string     // 当前活跃会话 ID
	activeShardPath string     // 当前活跃分片文件绝对路径
	meta            *IndexMeta // 内存中的元数据缓存（nil 表示未加载）
}

// NewShardedIndexStore 创建分片索引存储
// shardDir 通常是 baseDir/index/
// lockFile 通常是 baseDir/flock（与 FileHistory 共享）
func NewShardedIndexStore(shardDir, lockFile string) (*ShardedIndexStore, error) {
	if err := os.MkdirAll(shardDir, 0755); err != nil {
		return nil, fmt.Errorf("create shard dir: %w", err)
	}
	s := &ShardedIndexStore{
		shardDir:    shardDir,
		lockFile:    lockFile,
		metaFile:    filepath.Join(shardDir, metaFilename),
		metaTmpFile: filepath.Join(shardDir, metaFilename+metaTmpSuffix),
		meta:        &IndexMeta{Version: metaVersion, Shards: []ShardInfo{}},
	}
	// 默认活跃会话为 "default"
	s.activeSession = defaultSession
	s.activeShardPath = filepath.Join(shardDir, defaultFilename)
	return s, nil
}

// LoadMeta 加载或重建元数据
// 策略：先尝试读 metaFile；损坏或不存在则扫描 shardDir 重建；重建后立即持久化
//
// V3-5-1 接入：使用 util.LoadWithRecovery 替换 os.ReadFile + json.Unmarshal。
// 文件损坏时隔离到 ~/.whale/corrupt/ 并从 shardDir 重建（不 panic，不阻塞启动）。
func (s *ShardedIndexStore) LoadMeta() error {
	s.mu.Lock()
	defer s.mu.Unlock()

	// 检测迁移中断：metaTmpFile 残留表示上次迁移未完成（优化 5）
	if _, err := os.Stat(s.metaTmpFile); err == nil {
		log.Printf("[filehistory] detected interrupted migration (metaTmp exists), rebuilding meta from shard dir")
		// 删除残留的 metaTmp，重建 meta
		os.Remove(s.metaTmpFile)
		return s.rebuildMetaLocked()
	}

	// V3-5-1：使用 LoadWithRecovery 替换 os.ReadFile + json.Unmarshal，损坏时隔离文件。
	// corruptDir 推导：shardDir 为 ~/.whale/filehistory/{hash}/index/，向上 3 级到 ~/.whale/
	corruptDir := filepath.Join(filepath.Dir(filepath.Dir(filepath.Dir(s.shardDir))), "corrupt")
	var meta IndexMeta
	if err := util.LoadWithRecovery(s.metaFile, &meta, corruptDir); err != nil {
		if errors.Is(err, util.ErrFileCorrupted) {
			// meta 文件损坏已被隔离，从 shardDir 重建
			log.Printf("[filehistory] meta file corrupted and quarantined, rebuilding from shard dir: %s", s.metaFile)
			return s.rebuildMetaLocked()
		}
		if os.IsNotExist(err) {
			// meta 文件不存在，从 shardDir 重建
			return s.rebuildMetaLocked()
		}
		return fmt.Errorf("read meta file: %w", err)
	}

	s.meta = &meta

	// 同步活跃会话到 meta.ActiveSession
	if meta.ActiveSession != "" {
		s.activeSession = meta.ActiveSession
	} else {
		s.activeSession = defaultSession
	}
	s.activeShardPath = filepath.Join(s.shardDir, sessionToFilename(s.activeSession))
	return nil
}

// rebuildMetaLocked 从 shardDir 重建元数据（调用方持锁）
// 策略：os.ReadDir(shardDir) + 收集 index_*.jsonl + 统计记录数 + 立即持久化
func (s *ShardedIndexStore) rebuildMetaLocked() error {
	entries, err := os.ReadDir(s.shardDir)
	if err != nil {
		if os.IsNotExist(err) {
			// shardDir 不存在，初始化空 meta
			s.meta = &IndexMeta{Version: metaVersion, Shards: []ShardInfo{}}
			return s.saveMetaLocked()
		}
		return fmt.Errorf("read shard dir: %w", err)
	}

	meta := &IndexMeta{
		Version:       metaVersion,
		ActiveSession: defaultSession,
		Shards:        []ShardInfo{},
	}

	var lastMod time.Time
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		name := entry.Name()
		// 仅处理 index_*.jsonl 文件
		if !strings.HasPrefix(name, shardFilenamePrefix) || !strings.HasSuffix(name, shardFilenameSuffix) {
			continue
		}
		info, err := entry.Info()
		if err != nil {
			continue
		}
		// 反查 sessionId（无法反查则按文件名推断）
		sid := filenameToSession(name, nil)
		if sid == "" {
			// 未知分片，sessionId 用文件名（去掉前缀后缀）
			sid = strings.TrimSuffix(strings.TrimPrefix(name, shardFilenamePrefix), shardFilenameSuffix)
		}
		// 统计记录数
		shardPath := filepath.Join(s.shardDir, name)
		entries, _ := scanEntries(shardPath)
		entryCount := len(entries)

		meta.Shards = append(meta.Shards, ShardInfo{
			SessionId:    sid,
			Filename:     name,
			EntryCount:   entryCount,
			LastModified: info.ModTime(),
		})

		// 选最近修改的分片作为活跃会话
		if info.ModTime().After(lastMod) {
			lastMod = info.ModTime()
			meta.ActiveSession = sid
		}
	}

	s.meta = meta
	s.activeSession = meta.ActiveSession
	s.activeShardPath = filepath.Join(s.shardDir, sessionToFilename(s.activeSession))
	return s.saveMetaLocked()
}

// saveMeta 持久化元数据（原子写：tmp + rename，加锁）
func (s *ShardedIndexStore) saveMeta() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.saveMetaLocked()
}

// saveMetaLocked 持锁版本持久化元数据
// 对标 [util.AtomicWriteFile] tmp + fsync + rename 模式
func (s *ShardedIndexStore) saveMetaLocked() error {
	if s.meta == nil {
		return fmt.Errorf("meta is nil, cannot save")
	}
	data, err := json.MarshalIndent(s.meta, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal meta: %w", err)
	}
	if err := util.AtomicWriteFile(s.metaFile, data, 0o600); err != nil {
		return fmt.Errorf("write meta file: %w", err)
	}
	return nil
}

// AppendEntry 实现 IndexStore.AppendEntry
// 策略：调 appendEntry(activeShardPath, entry)；entry.SessionId 为空时填入 activeSession
func (s *ShardedIndexStore) AppendEntry(entry IndexEntry) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	// entry.SessionId 为空时填入当前 activeSession
	if entry.SessionId == "" {
		entry.SessionId = s.activeSession
	}

	// 调用包级 appendEntry（已实现跨进程锁 + O_APPEND）
	if err := appendEntry(s.activeShardPath, entry); err != nil {
		return err
	}

	// 更新 meta 中的分片信息（best-effort，失败仅记日志）
	s.updateShardInfoLocked(s.activeSession, s.activeShardPath, 1)
	if saveErr := s.saveMetaLocked(); saveErr != nil {
		log.Printf("[filehistory] saveMeta after AppendEntry failed (non-fatal): %v", saveErr)
	}

	return nil
}

// updateShardInfoLocked 更新或追加分片信息（调用方持锁）
// sessionId: 分片对应的会话 ID
// shardPath: 分片文件路径
// addedCount: 新增的记录数（用于 EntryCount 累加）
func (s *ShardedIndexStore) updateShardInfoLocked(sessionId, shardPath string, addedCount int) {
	if s.meta == nil {
		s.meta = &IndexMeta{Version: metaVersion, Shards: []ShardInfo{}}
	}
	filename := filepath.Base(shardPath)
	now := time.Now()

	// 查找是否已有该分片信息
	for i := range s.meta.Shards {
		if s.meta.Shards[i].Filename == filename {
			s.meta.Shards[i].EntryCount += addedCount
			s.meta.Shards[i].LastModified = now
			s.meta.Shards[i].SessionId = sessionId
			return
		}
	}

	// 新增分片信息
	// 统计当前记录数
	entries, _ := scanEntries(shardPath)
	s.meta.Shards = append(s.meta.Shards, ShardInfo{
		SessionId:    sessionId,
		Filename:     filename,
		EntryCount:   len(entries),
		LastModified: now,
	})
}

// ScanEntries 实现 IndexStore.ScanEntries
// 策略：调 scanEntries(activeShardPath)
func (s *ShardedIndexStore) ScanEntries() ([]IndexEntry, error) {
	s.mu.Lock()
	shardPath := s.activeShardPath
	s.mu.Unlock()
	return scanEntries(shardPath)
}

// FindEntry 实现 IndexStore.FindEntry（优化 1）
// 策略：调 findEntry(activeShardPath, msgId, entryType)
// 仅扫描活跃分片；跨分片查找走 ScanAllShards()
func (s *ShardedIndexStore) FindEntry(msgId, entryType string) (*IndexEntry, error) {
	s.mu.Lock()
	shardPath := s.activeShardPath
	s.mu.Unlock()
	return findEntry(shardPath, msgId, entryType)
}

// TruncateAfter 实现 IndexStore.TruncateAfter
// 策略：调 truncateAfter(activeShardPath, msgId)
func (s *ShardedIndexStore) TruncateAfter(msgId string) error {
	s.mu.Lock()
	shardPath := s.activeShardPath
	s.mu.Unlock()

	if err := truncateAfter(shardPath, msgId); err != nil {
		return err
	}

	// 更新 meta 中分片的记录数
	s.mu.Lock()
	defer s.mu.Unlock()
	entries, _ := scanEntries(shardPath)
	for i := range s.meta.Shards {
		if filepath.Base(shardPath) == s.meta.Shards[i].Filename {
			s.meta.Shards[i].EntryCount = len(entries)
			s.meta.Shards[i].LastModified = time.Now()
			break
		}
	}
	if saveErr := s.saveMetaLocked(); saveErr != nil {
		log.Printf("[filehistory] saveMeta after TruncateAfter failed (non-fatal): %v", saveErr)
	}
	return nil
}

// RewriteIndex 实现 IndexStore.RewriteIndex（优化 2）
// 策略：验证 entries 全部属于 activeSession；通过后原子写活跃分片
func (s *ShardedIndexStore) RewriteIndex(entries []IndexEntry) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	// 验证所有 entries 都属于当前 activeSession
	for i, e := range entries {
		if e.SessionId != "" && e.SessionId != s.activeSession {
			return fmt.Errorf("RewriteIndex: entry[%d] sessionId=%q does not match active session %q", i, e.SessionId, s.activeSession)
		}
	}

	// 加锁写文件
	return util.WithFileLock(s.lockFile, func(_ *os.File) error {
		var buf strings.Builder
		for _, entry := range entries {
			data, err := marshalEntry(entry)
			if err != nil {
				return fmt.Errorf("marshal entry: %w", err)
			}
			buf.Write(data)
		}
		// 原子写入（tmp + fsync + rename + 0600）
		if err := util.AtomicWriteFile(s.activeShardPath, []byte(buf.String()), 0o600); err != nil {
			return fmt.Errorf("write index: %w", err)
		}
		return nil
	})
}

// ScanAllShards 实现 IndexStore.ScanAllShards
// 策略：os.ReadDir(shardDir) + 串行 scanEntries 每个分片；损坏分片跳过 + log.Printf warning
// 串行扫描避免 -race 复杂度；并发优化留给后续 P2 改进
func (s *ShardedIndexStore) ScanAllShards() ([]IndexEntry, error) {
	s.mu.Lock()
	shardDir := s.shardDir
	s.mu.Unlock()

	entries, err := os.ReadDir(shardDir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("read shard dir: %w", err)
	}

	var result []IndexEntry
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		name := entry.Name()
		// 仅扫描 index_*.jsonl 文件
		if !strings.HasPrefix(name, shardFilenamePrefix) || !strings.HasSuffix(name, shardFilenameSuffix) {
			continue
		}
		// 跳过 meta 文件
		if name == metaFilename {
			continue
		}

		shardPath := filepath.Join(shardDir, name)
		shardEntries, err := scanEntries(shardPath)
		if err != nil {
			// 损坏分片跳过，记录 warning
			log.Printf("[filehistory] ScanAllShards: skip corrupt shard %s: %v", name, err)
			continue
		}
		// 反查 sessionId 并填入 entries（向后兼容老索引无 sessionId 字段）
		sid := filenameToSession(name, nil)
		if sid == "" {
			sid = strings.TrimSuffix(strings.TrimPrefix(name, shardFilenamePrefix), shardFilenameSuffix)
		}
		for i := range shardEntries {
			if shardEntries[i].SessionId == "" {
				shardEntries[i].SessionId = sid
			}
		}
		result = append(result, shardEntries...)
	}
	return result, nil
}

// SetActiveSession 实现 IndexStore.SetActiveSession
// 策略：sessionId 为空时设为 "default"；更新 activeSession + activeShardPath
// 若分片文件不存在不主动创建（首次 AppendEntry 时自动创建）
func (s *ShardedIndexStore) SetActiveSession(sessionId string) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	if sessionId == "" {
		sessionId = defaultSession
	}
	s.activeSession = sessionId
	s.activeShardPath = filepath.Join(s.shardDir, sessionToFilename(sessionId))

	// 更新 meta.ActiveSession
	if s.meta == nil {
		s.meta = &IndexMeta{Version: metaVersion, Shards: []ShardInfo{}}
	}
	if s.meta.ActiveSession != sessionId {
		s.meta.ActiveSession = sessionId
		if saveErr := s.saveMetaLocked(); saveErr != nil {
			log.Printf("[filehistory] saveMeta after SetActiveSession failed (non-fatal): %v", saveErr)
		}
	}
	return nil
}

// ActiveShardPath 实现 IndexStore.ActiveShardPath
func (s *ShardedIndexStore) ActiveShardPath() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.activeShardPath
}

// MigrateFromLegacy 实现 IndexStore.MigrateFromLegacy（优化 5）
// 策略：
//  1. 检测 legacyFile 是否存在，不存在则返回 nil（已迁移）
//  2. 写 metaTmpFile 标记迁移中（version=1, activeSession="legacy", lastMigratedAt=零值）
//  3. os.Rename(legacyFile, shardDir/index_legacy.jsonl)
//  4. 更新 meta：activeSession="legacy", shards=[{legacy}], lastMigratedAt=now
//  5. os.Rename(metaTmpFile, metaFile) 提交迁移
//  6. 设置 activeSession="legacy" + activeShardPath=index_legacy.jsonl
//
// 中途失败：删除 index_legacy.jsonl + 恢复 legacyFile（rename 回去）+ 删除 metaTmpFile
func (s *ShardedIndexStore) MigrateFromLegacy(legacyFile string) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	// 1. 检测 legacyFile 是否存在
	if _, err := os.Stat(legacyFile); err != nil {
		if os.IsNotExist(err) {
			// 老索引不存在，已迁移完成
			return nil
		}
		return fmt.Errorf("stat legacy file: %w", err)
	}

	legacyShardPath := filepath.Join(s.shardDir, legacyFilename)

	// 2. 写 metaTmpFile 标记迁移中（lastMigratedAt 为零值表示迁移中）
	migratingMeta := &IndexMeta{
		Version:        metaVersion,
		ActiveSession:  legacySession,
		Shards:         []ShardInfo{},
		LastMigratedAt: time.Time{}, // 零值表示迁移中
	}
	metaData, err := json.MarshalIndent(migratingMeta, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal migrating meta: %w", err)
	}
	if err := os.WriteFile(s.metaTmpFile, metaData, 0o600); err != nil {
		return fmt.Errorf("write meta tmp: %w", err)
	}

	// 3. 检测 index_legacy.jsonl 是否已存在（部分迁移残留）
	if _, err := os.Stat(legacyShardPath); err == nil {
		// 残留的 index_legacy.jsonl 存在，先删除避免 rename 冲突
		os.Remove(legacyShardPath)
	}

	// 4. rename legacyFile → index_legacy.jsonl
	if err := os.Rename(legacyFile, legacyShardPath); err != nil {
		// rename 失败，清理 metaTmp
		os.Remove(s.metaTmpFile)
		return fmt.Errorf("rename legacy file: %w", err)
	}

	// 5. 统计记录数 + 构建最终 meta
	entries, _ := scanEntries(legacyShardPath)
	info, _ := os.Stat(legacyShardPath)
	var lastMod time.Time
	if info != nil {
		lastMod = info.ModTime()
	}
	finalMeta := &IndexMeta{
		Version:       metaVersion,
		ActiveSession: legacySession,
		Shards: []ShardInfo{
			{
				SessionId:    legacySession,
				Filename:     legacyFilename,
				EntryCount:   len(entries),
				LastModified: lastMod,
			},
		},
		LastMigratedAt: time.Now(),
	}
	finalData, err := json.MarshalIndent(finalMeta, "", "  ")
	if err != nil {
		// 回滚：恢复 legacyFile
		os.Rename(legacyShardPath, legacyFile)
		os.Remove(s.metaTmpFile)
		return fmt.Errorf("marshal final meta: %w", err)
	}

	// 6. 写最终 metaFile + 删除 metaTmp
	if err := util.AtomicWriteFile(s.metaFile, finalData, 0o600); err != nil {
		// 回滚：恢复 legacyFile
		os.Rename(legacyShardPath, legacyFile)
		os.Remove(s.metaTmpFile)
		return fmt.Errorf("write final meta: %w", err)
	}
	os.Remove(s.metaTmpFile)

	// 7. 更新内存状态
	s.meta = finalMeta
	s.activeSession = legacySession
	s.activeShardPath = legacyShardPath

	log.Printf("[filehistory] migrated legacy index %s → %s (%d entries)", legacyFile, legacyShardPath, len(entries))
	return nil
}

// ---------- SingleFileIndexStore 实现（向后兼容包装） ----------

// SingleFileIndexStore 单文件索引存储（向后兼容包装老 indexFile）
// 仅用于测试注入或老格式兼容场景，生产环境使用 ShardedIndexStore
type SingleFileIndexStore struct {
	indexFile string
	lockFile  string
}

// NewSingleFileIndexStore 创建单文件索引存储
func NewSingleFileIndexStore(indexFile, lockFile string) *SingleFileIndexStore {
	return &SingleFileIndexStore{
		indexFile: indexFile,
		lockFile:  lockFile,
	}
}

// AppendEntry 实现 IndexStore.AppendEntry
func (s *SingleFileIndexStore) AppendEntry(entry IndexEntry) error {
	return appendEntry(s.indexFile, entry)
}

// ScanEntries 实现 IndexStore.ScanEntries
func (s *SingleFileIndexStore) ScanEntries() ([]IndexEntry, error) {
	return scanEntries(s.indexFile)
}

// FindEntry 实现 IndexStore.FindEntry
func (s *SingleFileIndexStore) FindEntry(msgId, entryType string) (*IndexEntry, error) {
	return findEntry(s.indexFile, msgId, entryType)
}

// TruncateAfter 实现 IndexStore.TruncateAfter
func (s *SingleFileIndexStore) TruncateAfter(msgId string) error {
	return truncateAfter(s.indexFile, msgId)
}

// RewriteIndex 实现 IndexStore.RewriteIndex
// 单文件模式下重写整个 indexFile（与现有 rewriteIndex 一致）
func (s *SingleFileIndexStore) RewriteIndex(entries []IndexEntry) error {
	return util.WithFileLock(s.lockFile, func(_ *os.File) error {
		var buf strings.Builder
		for _, entry := range entries {
			data, err := marshalEntry(entry)
			if err != nil {
				return fmt.Errorf("marshal entry: %w", err)
			}
			buf.Write(data)
		}
		if err := util.AtomicWriteFile(s.indexFile, []byte(buf.String()), 0o600); err != nil {
			return fmt.Errorf("write index: %w", err)
		}
		return nil
	})
}

// ScanAllShards 实现 IndexStore.ScanAllShards
// 单文件模式下等同于 ScanEntries
func (s *SingleFileIndexStore) ScanAllShards() ([]IndexEntry, error) {
	return scanEntries(s.indexFile)
}

// SetActiveSession 实现 IndexStore.SetActiveSession
// 单文件模式无分片概念，为 noop
func (s *SingleFileIndexStore) SetActiveSession(sessionId string) error {
	return nil
}

// ActiveShardPath 实现 IndexStore.ActiveShardPath
func (s *SingleFileIndexStore) ActiveShardPath() string {
	return s.indexFile
}

// MigrateFromLegacy 实现 IndexStore.MigrateFromLegacy
// 单文件模式无迁移需求，返回 nil
func (s *SingleFileIndexStore) MigrateFromLegacy(legacyFile string) error {
	return nil
}
