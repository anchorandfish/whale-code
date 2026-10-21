// Package config — memory_store.go 统一记忆存储层（N12 /090）
//
// MemoryStore 封装跨进程文件锁 + 原子写入 + workDir hash 隔离，统一 V1/V2 记忆存储。
// 锁机制复用 internal/config/lockfile.go 的 acquireLock（H5）。
// 权限遵循 H5 ：文件 0600，目录 0700。
// 项目隔离遵循 H5 ：~/.whale/memory/{workDirSHA256[:16]}/
//
// TD-12 已知架构债务（上线后 2 迭代内统一）：
// 本文件定义的 config.MemoryStore 与 internal/memory/scope.go 的 memory.MemoryStore 并行存在，
// 两者各自实现了 acquireLock/isStaleLock/serializeMemoryEntry/parseMemoryEntry/sanitizeMemoryFilename
// 等同质函数（独立方向，常量与正则也重复：MaxMemoryFiles/sanitizeRe/lockRetryAttempts 等）。
// 原因：H11/N27（memory 包）与 N12（config 包）独立演进，config.MemoryEntryV2 与 memory.MemoryEntry
// 类型不同（虽 14 frontmatter 字段对齐），强行统一需迁移大量调用方且可能引入循环依赖。
// 计划：上线后 2 迭代内将本文件标记为 deprecated，统一到 memory.MemoryStore + 桥接层，
// 届时 config 包仅保留 MemoryEntryV2 桥接类型与 ParseMemoryEntry 公共入口，锁机制复用 memory.acquireLock。
package config

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/anchorandfish/whale-code/internal/ai"
	"github.com/anchorandfish/whale-code/internal/util"
	"golang.org/x/text/unicode/norm"
)

// MaxMemoryFiles 记忆文件数上限（对标同类 CLI MAX_MEMORY_FILES）。
// 超过此数量时按 updatedAt 升序删除最旧文件。
const MaxMemoryFiles = 200

// sanitizeRe 匹配文件名中非字母数字/连字符/点的字符（用于 sanitizeMemoryFilename）。
// 注：\w 包含下划线，下划线保留不替换（与 TestSanitizeMemoryFilename_SpecialChars 期望一致）。
var sanitizeRe = regexp.MustCompile(`[^\w\-.]+`)

// MemoryStore 统一记忆存储层，封装跨进程文件锁 + 原子写入 + workDir hash 隔离。
// 所有写操作通过 acquireLock 获取独占锁，防止多 whale 实例并发写损坏。
// 读操作无锁（AtomicWriteFile 保证读到完整文件，无需独占）。
type MemoryStore struct {
	baseDir string // ~/.whale/memory/
	workDir string // 当前工作目录（用于计算 hash）
	hash    string // workDir SHA256 hash 前 16 位
}

// NewMemoryStore 创建记忆存储实例，自动创建 {baseDir}/{workDirHash}/ 目录（0700 权限）。
// baseDir 通常为 ~/.whale/memory/，workDir 为当前工作目录绝对路径。
func NewMemoryStore(baseDir, workDir string) (*MemoryStore, error) {
	sum := sha256.Sum256([]byte(workDir))
	hash := hex.EncodeToString(sum[:])[:16]
	dir := filepath.Join(baseDir, hash)
	if err := os.MkdirAll(dir, 0700); err != nil {
		return nil, ai.WithID(fmt.Errorf("create memory dir: %w", err), ai.ErrMemoryStore)
	}
	return &MemoryStore{baseDir: baseDir, workDir: workDir, hash: hash}, nil
}

// Dir 返回当前 workDir 对应的记忆目录绝对路径（{baseDir}/{hash}/）。
func (s *MemoryStore) Dir() string {
	return filepath.Join(s.baseDir, s.hash)
}

// WorkDir 返回创建 store 时传入的工作目录。
func (s *MemoryStore) WorkDir() string {
	return s.workDir
}

// FilePath 返回指定 filename 在当前 workDir hash 目录下的完整路径。
// filename 可带或不带 .md 后缀，统一补全。
func (s *MemoryStore) FilePath(filename string) string {
	if !strings.HasSuffix(strings.ToLower(filename), ".md") {
		filename += ".md"
	}
	return filepath.Join(s.Dir(), filename)
}

// lockPath 返回锁文件基础路径（acquireLock 会在此基础上追加 ".lock"）。
// 锁文件位于记忆目录内，名为 memory.lock。
func (s *MemoryStore) lockPath() string {
	return filepath.Join(s.Dir(), "memory")
}

// Write 写入单条记忆（获取独占锁 → 序列化 → 原子写入 0600 → 释放锁 → EnforceFileLimit）。
// 文件名由 entry.Name 经 sanitizeFilename 生成，自动补 .md 后缀。
// 若 entry.Name 为空则返回错误。CreatedAt 为零值时自动填充当前时间。
func (s *MemoryStore) Write(entry MemoryEntryV2) error {
	name := sanitizeMemoryFilename(entry.Name)
	if name == "" || (name == "untitled" && strings.TrimSpace(entry.Name) == "") {
		return ai.WithID(fmt.Errorf("memory name must not be empty"), ai.ErrMemoryStore)
	}
	// 统一 entry.Name 为 sanitized 形式，确保 frontmatter Name 与文件名一致
	// （List 从 frontmatter 读取 Name，FilePath 用 Name 构造路径，不一致会导致路径不匹配）
	entry.Name = name

	now := time.Now()
	if entry.CreatedAt.IsZero() {
		entry.CreatedAt = now
	}
	entry.UpdatedAt = now

	data, err := serializeMemoryEntry(&entry)
	if err != nil {
		return ai.WithID(fmt.Errorf("serialize memory entry: %w", err), ai.ErrMemoryStore)
	}

	path := s.FilePath(name)
	entry.FilePath = path

	// 独占锁防止跨进程并发写同一目录
	release, err := acquireLock(s.lockPath())
	if err != nil {
		return ai.WithID(fmt.Errorf("acquire memory write lock: %w", err), ai.ErrMemoryStore)
	}
	defer release()

	if err := util.AtomicWriteFile(path, data, 0600); err != nil {
		return ai.WithID(fmt.Errorf("write memory file: %w", err), ai.ErrMemoryStore)
	}

	// 写成功后执行文件数上限清理（Write 已持锁，直接调 locked 版本避免重入死锁）
	if err := s.enforceFileLimitLocked(); err != nil {
		// 清理失败不阻塞写入，仅记录
		fmt.Fprintf(os.Stderr, "[memory] EnforceFileLimit warning: %v\n", err)
	}

	return nil
}

// Read 读取单条记忆并解析 frontmatter + body。
// filename 可带或不带 .md 后缀。文件不存在或解析失败时返回错误。
func (s *MemoryStore) Read(filename string) (*MemoryEntryV2, error) {
	path := s.FilePath(filename)
	entry, err := ReadMemoryEntry(path)
	if err != nil {
		return nil, ai.WithID(fmt.Errorf("read memory %s: %w", filename, err), ai.ErrMemoryStore)
	}
	return entry, nil
}

// List 列出当前 workDir hash 目录下所有记忆（排除 MEMORY.md 索引与锁文件）。
// 返回结果按 UpdatedAt 降序（最新在前）。无法解析的文件被跳过。
func (s *MemoryStore) List() ([]MemoryEntryV2, error) {
	entries, err := os.ReadDir(s.Dir())
	if err != nil {
		return nil, ai.WithID(fmt.Errorf("read memory dir: %w", err), ai.ErrMemoryStore)
	}

	var result []MemoryEntryV2
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		name := e.Name()
		// 排除索引文件、锁文件、临时文件
		if name == "MEMORY.md" || strings.HasSuffix(name, ".lock") || strings.HasSuffix(name, ".tmp") || strings.HasSuffix(name, ".bak") {
			continue
		}
		if !strings.HasSuffix(strings.ToLower(name), ".md") {
			continue
		}
		entry, err := ReadMemoryEntry(filepath.Join(s.Dir(), name))
		if err != nil {
			continue // 跳过无法解析的文件
		}
		result = append(result, *entry)
	}

	// 按 UpdatedAt 降序排序（最新在前）
	sort.Slice(result, func(i, j int) bool {
		return result[i].UpdatedAt.After(result[j].UpdatedAt)
	})

	return result, nil
}

// Delete 删除单条记忆（获取独占锁 → os.Remove → 释放锁）。
// filename 可带或不带 .md 后缀。文件不存在时返回错误。
func (s *MemoryStore) Delete(filename string) error {
	path := s.FilePath(filename)

	release, err := acquireLock(s.lockPath())
	if err != nil {
		return ai.WithID(fmt.Errorf("acquire memory delete lock: %w", err), ai.ErrMemoryStore)
	}
	defer release()

	if err := os.Remove(path); err != nil {
		return ai.WithID(fmt.Errorf("delete memory %s: %w", filename, err), ai.ErrMemoryStore)
	}
	return nil
}

// EditInEditor 使用 $VISUAL/$EDITOR 打开记忆文件编辑。
// 编辑前备份 .bak，编辑后重新解析 frontmatter 验证格式，失败则回滚备份。
func (s *MemoryStore) EditInEditor(filename string) error {
	path := s.FilePath(filename)
	if _, err := os.Stat(path); err != nil {
		return ai.WithID(fmt.Errorf("memory file not found: %s", filename), ai.ErrMemoryStore)
	}

	// 编辑前备份
	bakPath := path + ".bak"
	if err := copyFile(path, bakPath); err != nil {
		return ai.WithID(fmt.Errorf("create edit backup: %w", err), ai.ErrMemoryStore)
	}

	if err := editInEditor(path); err != nil {
		// 编辑器启动失败，清理备份
		os.Remove(bakPath)
		return ai.WithID(err, ai.ErrMemoryEditor)
	}

	// 编辑后验证 frontmatter 格式
	if _, err := ReadMemoryEntry(path); err != nil {
		// 格式错误，回滚备份
		if rbErr := copyFile(bakPath, path); rbErr != nil {
			return ai.WithID(fmt.Errorf("revalidate after edit failed and rollback failed (original backed up at %s): %w", bakPath, err), ai.ErrMemoryStore)
		}
		os.Remove(bakPath)
		return ai.WithID(fmt.Errorf("invalid frontmatter after edit, rolled back to previous state: %w", err), ai.ErrMemoryParse)
	}

	os.Remove(bakPath)
	return nil
}

// EnforceFileLimit 检查记忆文件数，超过 MaxMemoryFiles 时按 UpdatedAt 升序删除最旧。
// MEMORY.md 索引与锁文件不计入上限。
// 独立调用时自行获取独占锁，使 check-and-delete 原子化（D8 TOCTOU 修复）。
// Write 内部应调 enforceFileLimitLocked 以复用已持有的锁，避免重入死锁。
func (s *MemoryStore) EnforceFileLimit() error {
	release, err := acquireLock(s.lockPath())
	if err != nil {
		return ai.WithID(fmt.Errorf("acquire memory cleanup lock: %w", err), ai.ErrMemoryStore)
	}
	defer release()
	return s.enforceFileLimitLocked()
}

// enforceFileLimitLocked 执行文件数上限清理，假设调用方已持有独占锁（D8 TOCTOU 修复）。
// 在锁保护下 List 与 Delete 原子执行，消除竞态窗口。
// 删除时若文件已被其他进程删除（os.IsNotExist），视为成功而非错误。
func (s *MemoryStore) enforceFileLimitLocked() error {
	list, err := s.List()
	if err != nil {
		return err
	}
	if len(list) <= MaxMemoryFiles {
		return nil
	}

	// List 已按 UpdatedAt 降序，最旧在尾部
	excess := len(list) - MaxMemoryFiles
	oldest := list[len(list)-excess:]
	for _, e := range oldest {
		path := s.FilePath(e.Name)
		if err := os.Remove(path); err != nil {
			// 文件已被其他进程删除不算错误，继续清理其余
			if os.IsNotExist(err) {
				continue
			}
			// 其他错误不阻塞，继续尝试其余
			continue
		}
	}
	return nil
}

// sanitizeMemoryFilename 清理记忆文件名（对标同类 CLI paths.ts validateMemoryPath）。
// 规则：NFC 规范化 → 去空字节 → 非字母数字/连字符/点替换为 - → 去首尾 - → 小写 → 限长 64。
// 拒绝 UNC/相对路径/空字节，空结果返回 "untitled"。
func sanitizeMemoryFilename(name string) string {
	// NFC 规范化（对标同类 CLI paths.ts NFC normalize）
	name = norm.NFC.String(name)
	// 去除空字节（拒绝 null-byte injection）
	name = strings.ReplaceAll(name, "\x00", "")
	// 拒绝 UNC/绝对路径前缀：去掉路径分隔符
	name = strings.ReplaceAll(name, "\\", "-")
	name = strings.ReplaceAll(name, "/", "-")
	// 非字母数字/连字符/点替换为 -
	re := sanitizeRe
	name = re.ReplaceAllString(name, "-")
	// 去首尾 -
	name = strings.Trim(name, "-")
	// 小写
	name = strings.ToLower(name)
	// 限长 64
	if len(name) > 64 {
		name = name[:64]
	}
	if name == "" {
		return "untitled"
	}
	return name
}

// copyFile 复制 src 到 dst（用于编辑前备份）。
func copyFile(src, dst string) error {
	data, err := os.ReadFile(src)
	if err != nil {
		return err
	}
	return util.AtomicWriteFile(dst, data, 0600)
}
