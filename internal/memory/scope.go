// scope.go — H11/N27 Phase B B1 双层 scope 存储层。
// / scope 存储位置无 hash；落地：~/.whale/memory/_user/ 跨项目 user 偏好，per-file 锁 + 禁用 EnforceFileLimit + 容量告警 >200
// per-file 锁；落地：_user/ 用 <name>.lock per-file 锁，project 保留 memory.lock 目录级锁
// ；落地：O_CREATE|O_EXCL 跨进程锁（与 H5 等价的独立实现，零 CGO）
// 分支判断；落地：scope="user" 走 per-file 锁 + 容量告警 + 禁用 enforceFileLimitLocked；scope="project" 保留目录级锁 + enforceFileLimitLocked
// SNAPSHOT: NewMemoryStoreForScope={user:_user/(无hash)+per-file锁(<name>.lock)+禁用EnforceFileLimit+容量告警>200(stderr); project:NewMemoryStoreForProject(目录级锁 memory.lock + EnforceFileLimit); Write分支判断(候选4第四轮诚实细化: enforceLimit=false 须在 Write 内分支, 不能仅靠构造函数字段)}
//
// TD-12 已知架构债务（上线后 2 迭代内统一）：
// 本文件定义的 memory.MemoryStore 与 internal/config/memory_store.go 的 config.MemoryStore 并行存在，
// 两者各自实现了 acquireLock/isStaleLock/serializeMemoryEntry/parseMemoryEntry 等同质函数（独立方向）。
// 原因：H11/N27（memory 包）与 N12（config 包）是独立演进方向，memory.MemoryEntry 与 config.MemoryEntryV2
// 类型不同（虽字段对齐），强行统一会引入循环依赖或大规模重构。
// 计划：上线后 2 迭代内将 config.MemoryStore 标记为 deprecated，统一到 memory.MemoryStore + 桥接层，
// 届时 config 包仅保留 MemoryEntryV2 桥接类型，锁机制复用 memory.acquireLock。

package memory

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/anchorandfish/whale-code/internal/util"
	"golang.org/x/text/unicode/norm"
	"gopkg.in/yaml.v3"
)

// MaxMemoryFiles project scope 文件数上限（与 N12 config.MaxMemoryFiles 等价，02 §2.5.2 契约）。
// 超过此数量时按 UpdatedAt 升序删除最旧文件。
const MaxMemoryFiles = 200

// MemoryCapacityWarningThreshold _user/ scope 容量告警阈值（修复漏洞 G）。
// 超过此数量时仅告警（写 stderr），不删除，提示用户 /memory cleanup。
// 注：user scope 永不调用 EnforceFileLimit（D8 TOCTOU 修复，修复漏洞 D）。
const MemoryCapacityWarningThreshold = 200

// lockRetryAttempts 获取锁的最大重试次数（与 config.acquireLock 等价，H5）。
const lockRetryAttempts = 50

// lockRetryInterval 获取锁的重试间隔（与 config.acquireLock 等价）。
const lockRetryInterval = 100 * time.Millisecond

// lockStaleTimeout 锁文件超时阈值，超过则视为 stale 可抢占（与 config.isStaleLock 等价）。
const lockStaleTimeout = 5 * time.Minute

// sanitizeRe 匹配文件名中非字母数字/连字符/点的字符（与 config.sanitizeRe 等价，独立方向）。
// 注：\w 包含下划线，下划线保留不替换。
var sanitizeRe = regexp.MustCompile(`[^\w\-.]+`)

// MemoryStore H11/N27 Phase B 双层 scope 存储层。
// 独立于 N12 config.MemoryStore（H11/N27 是独立方向），复用相同的 frontmatter 格式 + 跨进程锁机制。
//
// scope 区分（对齐 02 §2.4.1 Q3 + 候选 4 红队档案四轮验证）：
// - user scope：dir=~/.whale/memory/_user/（无 hash）+ per-file 锁（<name>.lock）+ 禁用 EnforceFileLimit + 容量告警 >200
// - project scope：dir=~/.whale/memory/{workDirHash}/ + 目录级锁（memory.lock）+ EnforceFileLimit（与 N12 等价）
type MemoryStore struct {
	baseDir      string // 基础目录（如 ~/.whale）
	scope        string // "user" 或 "project"
	enforceLimit bool   // 是否启用 EnforceFileLimit（user=false 强制；project=true）
	dir          string // 实际存储目录（user → _user/；project → {workDirHash}/）
	workDir      string // 仅 project scope 用（用于计算 hash）
	hash         string // 仅 project scope 用（workDir SHA256 前 16 位）
}

// NewMemoryStoreForScope 创建指定 scope 的 MemoryStore。
// 对齐 02 §2.4.1 Q3 + 候选 4 红队档案四轮验证：
// - project scope：返回 error，提示使用 NewMemoryStoreForProject（B1 不重复实现 project 构造）
// - _user/ scope：per-file 锁 + 禁用 EnforceFileLimit + 容量告警（修复漏洞 A/D/G）
//
// enforceLimit 参数对 user scope 强制为 false（候选 4 第四轮诚实细化：不能仅靠构造函数字段，Write 内有分支判断兜底）。
func NewMemoryStoreForScope(baseDir, scope string, enforceLimit bool) (*MemoryStore, error) {
	switch scope {
	case "user":
		dir := filepath.Join(baseDir, "memory", "_user")
		if err := os.MkdirAll(dir, 0700); err != nil {
			return nil, fmt.Errorf("创建 _user scope 目录失败: %w", err)
		}
		// 候选 4 第四轮诚实细化：enforceLimit 对 user scope 强制为 false
		// 即使调用方传入 enforceLimit=true，Write 内分支判断也会跳过 enforceFileLimitLocked（兜底）
		return &MemoryStore{
			baseDir:      baseDir,
			scope:        "user",
			enforceLimit: false,
			dir:          dir,
		}, nil
	case "project":
		// project scope 需 workDir 计算 hash，应使用 NewMemoryStoreForProject
		return nil, errors.New("project scope use NewMemoryStoreForProject instead")
	default:
		return nil, fmt.Errorf("unknown scope: %s (must be user or project)", scope)
	}
}

// NewMemoryStoreForProject 创建 project scope 的 MemoryStore（目录级锁 + EnforceFileLimit）。
// 等价于 N12 config.NewMemoryStore（02 §2.5.2 契约），但独立于 config 包（独立方向）。
// workDir 为当前工作目录绝对路径，用于计算 SHA256 hash 隔离不同项目。
func NewMemoryStoreForProject(baseDir, workDir string) (*MemoryStore, error) {
	sum := sha256.Sum256([]byte(workDir))
	hash := hex.EncodeToString(sum[:])[:16]
	dir := filepath.Join(baseDir, "memory", hash)
	if err := os.MkdirAll(dir, 0700); err != nil {
		return nil, fmt.Errorf("创建 project scope 目录失败: %w", err)
	}
	return &MemoryStore{
		baseDir:      baseDir,
		scope:        "project",
		enforceLimit: true,
		dir:          dir,
		workDir:      workDir,
		hash:         hash,
	}, nil
}

// Scope 返回 MemoryStore 的 scope 类型（"user" 或 "project"）。
func (s *MemoryStore) Scope() string {
	return s.scope
}

// Dir 返回当前 scope 对应的记忆目录绝对路径。
// user scope: {baseDir}/memory/_user/
// project scope: {baseDir}/memory/{hash}/
func (s *MemoryStore) Dir() string {
	return s.dir
}

// WorkDir 返回创建 store 时传入的工作目录（仅 project scope 有效）。
func (s *MemoryStore) WorkDir() string {
	return s.workDir
}

// Hash 返回 workDir SHA256 hash 前 16 位（仅 project scope 有效）。
func (s *MemoryStore) Hash() string {
	return s.hash
}

// FilePath 返回指定 filename 在当前 scope 目录下的完整路径。
// filename 可带或不带 .md 后缀，统一补全。
func (s *MemoryStore) FilePath(filename string) string {
	if !strings.HasSuffix(strings.ToLower(filename), ".md") {
		filename += ".md"
	}
	return filepath.Join(s.dir, filename)
}

// Write 写入单条记忆（候选 4 第四轮诚实细化核心，B1 关键修复）。
//
// scope="user"：per-file 锁（<name>.lock）+ 容量告警 >200 + 不调 enforceFileLimitLocked（修复漏洞 A/D）
// scope="project"：目录级锁（memory.lock）+ enforceFileLimitLocked（与 N12 等价）
//
// 候选 4 第四轮诚实细化：enforceLimit=false 不能仅靠构造函数字段，必须在 Write 内加分支判断。
// 这样即使外部传入错误的 enforceLimit=true 给 user scope，Write 仍会按 scope 走正确分支（兜底）。
//
// 零值默认值处理（与 config.ParseMemoryEntry 对称）：
// - Importance == 0 → 默认 50
// - ValidFrom == nil → 回退到 &CreatedAt
// - Verification == "" → 默认 "unverified"（NEVER 默认 verified，防 PIT-001）
//
// PIT-001 防护（致命反例，B6 §5.2 第 10 用例）：trust_level=capability 的记忆写入 user scope 直接拒绝。
// 理由：capability 记录项目特定能力探测（gcc/git 可用性），换机器/换项目后会失效被当真（PIT-001 重现）。
// 必须写入 project scope，让 workDir hash 隔离不同项目。
func (s *MemoryStore) Write(entry *MemoryEntry) error {
	if entry == nil {
		return errors.New("Write: entry is nil")
	}
	name := sanitizeMemoryFilename(entry.Name)
	if name == "" || (name == "untitled" && strings.TrimSpace(entry.Name) == "") {
		return errors.New("memory Name cannot be empty")
	}
	// 统一 entry.Name 为 sanitized 形式，确保 frontmatter Name 与文件名一致
	entry.Name = name

	// PIT-001 防护（致命反例 TestCapability_force_project_scope）：
	// trust_level=capability 必须写入 project scope，写入 user scope 直接拒绝。
	// 理由：capability 是项目特定能力探测，换机器/换项目后失效被当真（PIT-001 重现）。
	if s.scope == "user" && entry.TrustLevel == TrustCapability {
		return fmt.Errorf("Write %q: %w (capability 记录项目特定能力, 必须写入 project scope)",
			name, ErrCapabilityMustBeProjectScope)
	}

	now := time.Now()
	if entry.CreatedAt.IsZero() {
		entry.CreatedAt = now
	}
	entry.UpdatedAt = now

	// 零值默认值处理（与 config.ParseMemoryEntry 对称）
	if entry.Importance == 0 {
		entry.Importance = 50
	}
	if entry.Verification == "" {
		entry.Verification = VerificationUnverified
	}
	if entry.ValidFrom == nil {
		createdAt := entry.CreatedAt
		entry.ValidFrom = &createdAt
	}

	data, err := serializeMemoryEntry(entry)
	if err != nil {
		return fmt.Errorf("serialize memory entry: %w", err)
	}

	path := s.FilePath(name)
	entry.FilePath = path

	if s.scope == "user" {
		// user scope: per-file 锁（<name>.lock）+ 容量告警 + 禁用 EnforceFileLimit
		// acquireLock 自动追加 .lock → <name>.lock（修复漏洞 A：不同文件并发写）
		lockPath := filepath.Join(s.dir, name)
		release, err := acquireLock(lockPath)
		if err != nil {
			return fmt.Errorf("acquire per-file lock: %w", err)
		}
		defer release()

		if err := util.AtomicWriteFile(path, data, 0600); err != nil {
			return fmt.Errorf("write memory file: %w", err)
		}

		// 容量告警（仅警告，不删除，修复漏洞 G）
		if count := s.countFiles(); count > MemoryCapacityWarningThreshold {
			fmt.Fprintf(os.Stderr, "WARN: _user/ scope has %d files, consider /memory cleanup\n", count)
		}

		// 不调 enforceFileLimitLocked（修复漏洞 D：消除 TOCTOU 窗口）
		return nil
	}

	// project scope: 目录级锁（memory.lock）+ enforceFileLimitLocked
	lockPath := filepath.Join(s.dir, "memory") // acquireLock 自动追加 .lock → memory.lock
	release, err := acquireLock(lockPath)
	if err != nil {
		return fmt.Errorf("acquire dir lock: %w", err)
	}
	defer release()

	if err := util.AtomicWriteFile(path, data, 0600); err != nil {
		return fmt.Errorf("write memory file: %w", err)
	}

	// 写成功后执行文件数上限清理（Write 已持锁，直接调 locked 版本避免重入死锁）
	if s.enforceLimit {
		if err := s.enforceFileLimitLocked(); err != nil {
			// 清理失败不阻塞写入，仅记录
			fmt.Fprintf(os.Stderr, "[memory] EnforceFileLimit warning: %v\n", err)
		}
	}
	return nil
}

// Read 读取单条记忆并解析 frontmatter + body。
// filename 可带或不带 .md 后缀。文件不存在或解析失败时返回错误。
// 读操作无锁（AtomicWriteFile 保证读到完整文件，无需独占）。
func (s *MemoryStore) Read(filename string) (*MemoryEntry, error) {
	path := s.FilePath(filename)
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read memory file %s: %w", filename, err)
	}
	entry, err := parseMemoryEntry(string(data))
	if err != nil {
		return nil, fmt.Errorf("parse memory entry %s: %w", filename, err)
	}
	entry.FilePath = path
	return entry, nil
}

// List 列出当前 scope 目录下所有记忆（排除 MEMORY.md 索引与锁文件）。
// 返回结果按 UpdatedAt 降序（最新在前）。无法解析的文件被跳过。
func (s *MemoryStore) List() ([]*MemoryEntry, error) {
	entries, err := os.ReadDir(s.dir)
	if err != nil {
		return nil, fmt.Errorf("read memory directory: %w", err)
	}

	var result []*MemoryEntry
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		name := e.Name()
		// 排除索引文件、锁文件、临时文件、备份文件
		if name == "MEMORY.md" || strings.HasSuffix(name, ".lock") || strings.HasSuffix(name, ".tmp") || strings.HasSuffix(name, ".bak") {
			continue
		}
		if !strings.HasSuffix(strings.ToLower(name), ".md") {
			continue
		}
		entry, err := s.Read(name)
		if err != nil {
			continue // 跳过无法解析的文件
		}
		result = append(result, entry)
	}

	// 按 UpdatedAt 降序排序（最新在前）
	sort.Slice(result, func(i, j int) bool {
		return result[i].UpdatedAt.After(result[j].UpdatedAt)
	})

	return result, nil
}

// Delete 删除单条记忆。
// scope="user"：per-file 锁（<name>.lock）
// scope="project"：目录级锁（memory.lock）
//
// filename 可带或不带 .md 后缀。文件不存在时返回错误。
func (s *MemoryStore) Delete(filename string) error {
	path := s.FilePath(filename)

	var lockPath string
	if s.scope == "user" {
		// user scope: per-file 锁
		name := strings.TrimSuffix(filename, ".md")
		name = strings.TrimSuffix(name, ".MD")
		lockPath = filepath.Join(s.dir, sanitizeMemoryFilename(name))
	} else {
		// project scope: 目录级锁
		lockPath = filepath.Join(s.dir, "memory")
	}
	release, err := acquireLock(lockPath)
	if err != nil {
		return fmt.Errorf("acquire delete lock: %w", err)
	}
	defer release()

	if err := os.Remove(path); err != nil {
		return fmt.Errorf("delete memory file %s: %w", filename, err)
	}
	return nil
}

// countFiles 统计 scope 目录下 .md 记忆文件数（排除 MEMORY.md/锁/临时/备份）。
// 不获取锁（仅用于告警，容忍轻度竞态）。
func (s *MemoryStore) countFiles() int {
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
		if name == "MEMORY.md" || strings.HasSuffix(name, ".lock") || strings.HasSuffix(name, ".tmp") || strings.HasSuffix(name, ".bak") {
			continue
		}
		if strings.HasSuffix(strings.ToLower(name), ".md") {
			count++
		}
	}
	return count
}

// enforceFileLimitLocked 执行文件数上限清理，假设调用方已持有独占锁（D8 TOCTOU 修复）。
// 在锁保护下 List 与清理原子执行，消除竞态窗口。
// 仅 project scope 调用（user scope 在 Write 中跳过此函数）。
//
// E3（归档式 GC，不硬删）：超出上限的条目**归档**到 <dir>/_archive/ 而不是
// os.Remove。理由是记忆条目不可再生，而上限只是「活动集」的容量约束——
// 移出活动集即可达成本轮目标，删除是不可逆的额外损失。
// 被并发移走（os.IsNotExist）视为成功，继续清理其余。
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
		if _, archErr := archiveFile(s.dir, path); archErr != nil {
			// 归档失败不阻塞其余条目；调用方通过 List() 计数（仍超限）与日志可见。
			log.Printf("WARN: memory limit eviction: archive %s failed, entry left in place: %v", path, archErr)
			continue
		}
	}
	return nil
}

// ============================================================
// 跨进程锁（与 config.acquireLock 等价，独立实现）
// ============================================================

// acquireLock 获取指定路径的排他锁文件。
// 用 os.OpenFile(O_CREATE|O_EXCL) 创建锁文件（路径 + ".lock"），写入 PID + 创建时间戳。
// 若锁文件已存在且为有效锁（未超时），阻塞重试直到获取或超时。
// 若锁文件已存在但为 stale（超时 5 分钟），抢占锁。
// 返回 release 函数，调用后删除锁文件释放锁；返回 error 表示获取失败。
//
// 阻塞策略：最多重试 50 次，每次间隔 100ms，总阻塞上限 5 秒。
// 与 config.acquireLock 等价（H5 跨平台实现），独立于 config 包。
func acquireLock(path string) (func(), error) {
	lockPath := path + ".lock"

	for attempt := 0; attempt < lockRetryAttempts; attempt++ {
		fd, err := os.OpenFile(lockPath, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
		if err == nil {
			// 创建成功，写入 PID + 时间戳
			content := fmt.Sprintf("%d\n%d", os.Getpid(), time.Now().Unix())
			if _, wErr := fd.WriteString(content); wErr != nil {
				fd.Close()
				os.Remove(lockPath)
				return nil, fmt.Errorf("write lockfile: %w", wErr)
			}
			fd.Close()

			release := func() {
				os.Remove(lockPath)
			}
			return release, nil
		}

		// 锁文件已存在，检查是否 stale
		if isStaleLock(lockPath) {
			// 抢占 stale lock：删除后重试
			os.Remove(lockPath)
			continue
		}

		// 锁被持有，等待后重试
		time.Sleep(lockRetryInterval)
	}

	return nil, fmt.Errorf("acquire lock timeout (%.0fs): %s", lockRetryAttempts*lockRetryInterval.Seconds(), lockPath)
}

// isStaleLock 检查锁文件是否过期（可被抢占）。
//
// ★ F11 局部收敛：判据唯一来源是 `util.LockVerdict`（见 lockVerdict），
// 本函数不再自带二值分支，只做薄转发 `MaySteal()` —— 与 config.isStaleLock / goal.isStaleLock
// 收敛为同一份声明。判定规则（与 config 等价，独立方向）：
// - 读取失败 → Stale（抢占）
// - 内容无法解析（行数 < 2 / PID 非数字 / 时间戳非数字）→ Unknown（保守，不夺）
// - 超时 5 分钟 → Stale
// - 本包不检查 PID 存活（跨平台降级）→ Held
func isStaleLock(lockPath string) bool {
	return lockVerdict(lockPath).MaySteal()
}

// lockVerdict 对锁文件做**三值**判定（F11 L4 的唯一判定点，与 config.lockVerdict 同源）。
//
// 规则（与 session 侧 Store.lockVerdict 同一套语义，都走 util.LockVerdict）：
//
//	读不到文件 ⇒ Stale （锁不存在 ⇒ 可夺）
//	内容无法解析 ⇒ Unknown （不夺；修复前本处 return false 是「保守持有」）
//	超时 5 分钟 ⇒ Stale
//	否则 ⇒ Held
func lockVerdict(lockPath string) util.LockVerdict {
	data, err := os.ReadFile(lockPath)
	if err != nil {
		return util.LockVerdictStale // 读不到视为 stale
	}

	lines := strings.Split(string(data), "\n")
	if len(lines) < 2 {
		return util.LockVerdictUnknown // 无法解析 → 未知 → 不夺
	}

	timestamp, err := parseUnixTimestamp(lines[1])
	if err != nil {
		return util.LockVerdictUnknown // 时间戳非数字 → 未知 → 不夺
	}

	// 超时 5 分钟视为 stale
	if time.Since(time.Unix(timestamp, 0)) > lockStaleTimeout {
		return util.LockVerdictStale
	}

	return util.LockVerdictHeld
}

// parseUnixTimestamp 解析 Unix 时间戳字符串。
func parseUnixTimestamp(s string) (int64, error) {
	var ts int64
	_, err := fmt.Sscanf(strings.TrimSpace(s), "%d", &ts)
	return ts, err
}

// ============================================================
// frontmatter 序列化（与 config.serializeMemoryEntry/ParseMemoryEntry 等价，基于 memory.MemoryEntry 独立实现）
// ============================================================

// memoryFrontMatter YAML frontmatter 中间结构（与 config.memoryFrontMatter 等价，14 字段）。
// 独立于 config 包，常量重复可接受（桥接层降级）。
type memoryFrontMatter struct {
	Name         string     `yaml:"name"`
	Description  string     `yaml:"description"`
	Type         MemoryType `yaml:"type"`
	CreatedAt    time.Time  `yaml:"created_at"`
	UpdatedAt    time.Time  `yaml:"updated_at"`
	TrustLevel   string     `yaml:"trust_level"`
	ProbeFn      string     `yaml:"probe_fn"`
	TTlSeconds   int64      `yaml:"ttl_seconds"`
	Verification string     `yaml:"verification"`
	ValidFrom    *time.Time `yaml:"valid_from"`
	ValidUntil   *time.Time `yaml:"valid_until"`
	Links        []string   `yaml:"links"`
	Importance   int        `yaml:"importance"`
	Source       string     `yaml:"source"`
	// S18/G1 修复：持久化访问热度（修复前为 yaml:"-" 内存字段）。
	// 冷加载后若为零，ApplyForgetting 会回退到 UpdatedAt/ValidFrom 作时间锚，
	// 避免 zero time 导致 retention≈0 误判 contradicted。
	AccessCount    int       `yaml:"access_count"`
	LastAccessedAt time.Time `yaml:"last_accessed_at"`
}

// serializeMemoryEntry 序列化 MemoryEntry 为 YAML frontmatter + Markdown body 字节。
// 与 config.serializeMemoryEntry 对称（独立实现）。
// 内存字段（AccessCount/LastAccessedAt/ProbeFailCount/Content/FilePath）中：
// - Content 是 Markdown body，写入 --- 之后的正文部分
// - AccessCount/LastAccessedAt/ProbeFailCount/FilePath 不序列化（yaml:"-"）
func serializeMemoryEntry(entry *MemoryEntry) ([]byte, error) {
	fm := memoryFrontMatter{
		Name:         entry.Name,
		Description:  entry.Description,
		Type:         entry.Type,
		CreatedAt:    entry.CreatedAt,
		UpdatedAt:    entry.UpdatedAt,
		TrustLevel:   entry.TrustLevel,
		ProbeFn:      entry.ProbeFn,
		TTlSeconds:   entry.TTlSeconds,
		Verification: entry.Verification,
		ValidFrom:    entry.ValidFrom,
		ValidUntil:   entry.ValidUntil,
		Links:        entry.Links,
		Importance:   entry.Importance,
		Source:       entry.Source,
		// S18/G1：持久化访问热度（使 Ebbinghaus 衰减基于真实持久化数据）。
		AccessCount:    entry.AccessCount,
		LastAccessedAt: entry.LastAccessedAt,
	}

	fmBytes, err := yaml.Marshal(&fm)
	if err != nil {
		return nil, fmt.Errorf("serialize frontmatter: %w", err)
	}

	var sb strings.Builder
	sb.WriteString("---\n")
	sb.Write(fmBytes)
	sb.WriteString("---\n")
	if entry.Content != "" {
		sb.WriteString(entry.Content)
		sb.WriteString("\n")
	}
	return []byte(sb.String()), nil
}

// parseMemoryEntry 解析 YAML frontmatter + Markdown body。
// 与 config.ParseMemoryEntry 对称（独立实现），零值默认值处理一致。
// 零值默认值规则：
// - Importance == 0 → 默认 50
// - ValidFrom == nil → 回退到 &CreatedAt
// - Verification == "" → 默认 "unverified"（NEVER 默认 verified，防 PIT-001）
func parseMemoryEntry(content string) (*MemoryEntry, error) {
	content = strings.TrimSpace(content)
	if !strings.HasPrefix(content, "---") {
		return nil, errors.New("memory entry missing YAML frontmatter (---)")
	}

	endIdx := strings.Index(content[3:], "---")
	if endIdx < 0 {
		return nil, errors.New("memory entry YAML frontmatter not properly closed")
	}

	fmContent := content[3 : endIdx+3]
	bodyContent := content[endIdx+6:] // Skip second ---

	var fm memoryFrontMatter
	if err := yaml.Unmarshal([]byte(fmContent), &fm); err != nil {
		return nil, fmt.Errorf("parse YAML frontmatter: %w", err)
	}

	// 零值默认值处理（与 config.ParseMemoryEntry 一致）
	if fm.Importance == 0 {
		fm.Importance = 50
	}
	if fm.ValidFrom == nil {
		createdAt := fm.CreatedAt
		fm.ValidFrom = &createdAt
	}
	if fm.Verification == "" {
		fm.Verification = VerificationUnverified
	}

	return &MemoryEntry{
		Name:         fm.Name,
		Description:  fm.Description,
		Type:         fm.Type,
		CreatedAt:    fm.CreatedAt,
		UpdatedAt:    fm.UpdatedAt,
		TrustLevel:   fm.TrustLevel,
		ProbeFn:      fm.ProbeFn,
		TTlSeconds:   fm.TTlSeconds,
		Verification: fm.Verification,
		ValidFrom:    fm.ValidFrom,
		ValidUntil:   fm.ValidUntil,
		Links:        fm.Links,
		Importance:   fm.Importance,
		Source:       fm.Source,
		// S18/G1：还原持久化的访问热度（旧文件缺这两字段 → 自动零值，
		// 由 ApplyForgetting 的时间锚回退逻辑兜底，不误判 contradicted）。
		AccessCount:    fm.AccessCount,
		LastAccessedAt: fm.LastAccessedAt,
		Content:        strings.TrimSpace(bodyContent),
	}, nil
}

// sanitizeMemoryFilename 清理记忆文件名（与 config.sanitizeMemoryFilename 等价）。
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
	name = sanitizeRe.ReplaceAllString(name, "-")
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
