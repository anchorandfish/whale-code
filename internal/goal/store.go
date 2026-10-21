package goal

import (
	"encoding/json"
	"errors"
	"fmt"
	"github.com/anchorandfish/whale-code/internal/whalehome"
	"log"
	"os"
	"path/filepath"
	"strings"

	"github.com/anchorandfish/whale-code/internal/util"
)

// Store goal 持久化接口（JSON 文件实现，）。
//
// 接口契约：
// - Load 文件不存在返回 (nil, os.ErrNotExist)，不报错（调用方需处理）
// - Save 原子写（.tmp → os.Rename，WHALE.md §5）+ 文件锁（复用 config/lockfile.go 模式）
// - Delete 文件不存在不报错（幂等）
// - List 通过 filepath.Glob *.json 列出所有 goal
type Store interface {
	// Load 读取 goal（文件不存在返回 nil, os.ErrNotExist）。
	Load(threadID string) (*Goal, error)
	// Save 保存 goal（原子写 + 文件锁）。
	Save(threadID string, g *Goal) error
	// Delete 删除 goal（幂等，文件不存在不报错）。
	Delete(threadID string) error
	// List 列出所有 goal。
	List() ([]*Goal, error)
}

// fileStore JSON 文件实现。
type fileStore struct {
	rootDir string // 从 config 层传入（v4 修订：不硬编码 ~/.whale/goals/）
}

// NewFileStore 创建 JSON 文件 Store。
//
// rootDir 从 config 层读取（v4 修订：不硬编码 ~/.whale/goals/）。
// 若调用方传空，fallback 到 ~/.whale/goals/（H5 约定，待 H5 确认）。
// 目录不存在时自动创建（0755 权限，目标文件 0600，）。
func NewFileStore(rootDir string) Store {
	if rootDir == "" {
		// fallback：H5 ~\/.whale\/ 根约定，待 H5 确认后从 config 层传入
		home, err := os.UserHomeDir()
		if err != nil || home == "" {
			// 极端情况：无法获取 home，使用当前目录（仅供测试 fallback）
			home = "."
		}
		rootDir = filepath.Join(whalehome.Dir(), "goals")
	}
	// 确保目录存在（MkdirAll 0755，文件本身 0600）
	_ = os.MkdirAll(rootDir, 0o755)
	return &fileStore{rootDir: rootDir}
}

// safePath 校验 threadID 并拼接路径（T9 反例：路径穿越防护）。
//
// 校验规则：
// - 非空
// - 不含路径分隔符（/ \）
// - 不含 ".."（防止路径穿越）
// - 不含空字节
//
// 返回完整 JSON 文件路径 + 错误（若有）。
func (s *fileStore) safePath(threadID string) (string, error) {
	if threadID == "" {
		return "", fmt.Errorf("goal: threadID is empty")
	}
	if strings.ContainsAny(threadID, `/\`) {
		return "", fmt.Errorf("goal: threadID contains path separator: %q", threadID)
	}
	if strings.Contains(threadID, "..") {
		return "", fmt.Errorf("goal: threadID contains path traversal: %q", threadID)
	}
	if strings.ContainsRune(threadID, 0) {
		return "", fmt.Errorf("goal: threadID contains null byte")
	}
	return filepath.Join(s.rootDir, threadID+".json"), nil
}

// lockPath 返回 threadID 对应的锁文件路径（复用 config/lockfile.go 模式：path+".lock"）。
func (s *fileStore) lockPath(threadID string) (string, error) {
	dataPath, err := s.safePath(threadID)
	if err != nil {
		return "", err
	}
	return dataPath + ".lock", nil
}

// acquireFileLock 获取跨进程文件锁（复用 config/lockfile.go 模式：O_CREATE|O_EXCL + stale 检测）。
//
// 实现说明：config.acquireLock 未导出，goal 包内重实现等价逻辑（参考 lockfile.go:23-57）。
// 阻塞策略：最多 50 次 × 100ms = 5 秒。
// 返回 release 函数（删除锁文件）；返回 error 表示获取失败。
func (s *fileStore) acquireFileLock(threadID string) (func(), error) {
	lockPath, err := s.lockPath(threadID)
	if err != nil {
		return nil, err
	}
	for attempt := 0; attempt < 50; attempt++ {
		fd, err := os.OpenFile(lockPath, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
		if err == nil {
			// 创建成功，写入 PID + 时间戳
			if _, wErr := fmt.Fprintf(fd, "%d\n%d", os.Getpid(), nowUnix()); wErr != nil {
				fd.Close()
				_ = os.Remove(lockPath)
				return nil, fmt.Errorf("goal: write lockfile: %w", wErr)
			}
			fd.Close()
			release := func() { _ = os.Remove(lockPath) }
			return release, nil
		}
		// 锁文件已存在，检查是否 stale（PID 不存活或超时 5 分钟）
		if isStaleLock(lockPath) {
			_ = os.Remove(lockPath)
			continue
		}
		sleepFn(100 * 1000 * 1000) // 100ms
	}
	return nil, fmt.Errorf("goal: acquire lock timeout (5s): %s", lockPath)
}

// Load 读取 goal（文件不存在返回 nil, os.ErrNotExist，不报错）。
func (s *fileStore) Load(threadID string) (*Goal, error) {
	path, err := s.safePath(threadID)
	if err != nil {
		return nil, err
	}
	data, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, os.ErrNotExist
		}
		return nil, fmt.Errorf("goal: read file %s: %w", path, err)
	}
	var g Goal
	if err := json.Unmarshal(data, &g); err != nil {
		return nil, fmt.Errorf("goal: unmarshal %s: %w", path, err)
	}
	return &g, nil
}

// Save 保存 goal（原子写 + 文件锁，WHALE.md §5）。
func (s *fileStore) Save(threadID string, g *Goal) error {
	path, err := s.safePath(threadID)
	if err != nil {
		return err
	}
	// 获取跨进程文件锁
	release, err := s.acquireFileLock(threadID)
	if err != nil {
		return err
	}
	defer release()
	// 原子写：marshal → .tmp → fsync → os.Rename
	data, err := json.MarshalIndent(g, "", "  ")
	if err != nil {
		return fmt.Errorf("goal: marshal: %w", err)
	}
	if err := util.AtomicWriteFile(path, data, 0o600); err != nil {
		return fmt.Errorf("goal: atomic write %s: %w", path, err)
	}
	return nil
}

// Delete 删除 goal（幂等，文件不存在不报错）。
func (s *fileStore) Delete(threadID string) error {
	path, err := s.safePath(threadID)
	if err != nil {
		return err
	}
	release, err := s.acquireFileLock(threadID)
	if err != nil {
		return err
	}
	defer release()
	err = os.Remove(path)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("goal: delete %s: %w", path, err)
	}
	return nil
}

// List 列出所有 goal（filepath.Glob *.json，排除 .lock 文件）。
// 单个文件读取或反序列化失败时记录 log.Warn 并跳过，不阻塞整个列表。
func (s *fileStore) List() ([]*Goal, error) {
	pattern := filepath.Join(s.rootDir, "*.json")
	paths, err := filepath.Glob(pattern)
	if err != nil {
		return nil, fmt.Errorf("goal: glob %s: %w", pattern, err)
	}
	var goals []*Goal
	for _, p := range paths {
		// 跳过锁文件（*.json.lock 不会被 *.json 匹配，但防御性检查）
		if strings.HasSuffix(p, ".lock") {
			continue
		}
		data, err := os.ReadFile(p)
		if err != nil {
			log.Printf("[goal] List: 跳过文件 %s，读取失败: %v", p, err)
			continue // 单个文件失败不阻塞列表
		}
		var g Goal
		if err := json.Unmarshal(data, &g); err != nil {
			log.Printf("[goal] List: 跳过文件 %s，反序列化失败: %v", p, err)
			continue
		}
		goals = append(goals, &g)
	}
	return goals, nil
}
