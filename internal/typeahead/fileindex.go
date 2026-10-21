package typeahead

import (
	"hash/fnv"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"time"
)

// fileIndex 文件索引单例（对标 fileSuggestions.ts:34-41）
type fileIndex struct {
	mu            sync.RWMutex
	paths         []string  // 已载入路径
	signature     string    // pathListSignature
	gitIndexMtime time.Time // .git/index mtime
	lastRefresh   time.Time // 上次刷新时间
	refreshing    bool      // 是否正在刷新
	generation    uint64    // 代际计数器（对标 cacheGeneration）
}

// fi 全局单例
var fi = &fileIndex{}

// cacheWorkDir 缓存的工作目录（由 tui 通过 InitWorkDir 设置，供 search 触发后台刷新使用）
var cacheWorkDir string

// InitWorkDir 设置缓存工作目录（由 tui 在初始化时调用）
// B-1 修复：search 在 paths 为空时需 workDir 触发 startBackgroundCacheRefresh
func InitWorkDir(dir string) {
	cacheWorkDir = dir
}

// refreshInterval 节流间隔（对标 fileSuggestions.ts:635 的 5s）
const refreshInterval = 5 * time.Second

// startBackgroundCacheRefresh 后台刷新（对标 fileSuggestions.ts:635）
// 节流：5s + .git/index mtime 变更即时
// cold start 必刷新
func (f *fileIndex) startBackgroundCacheRefresh(workDir string) {
	f.mu.RLock()
	shouldRefresh := !f.refreshing && time.Since(f.lastRefresh) >= refreshInterval
	f.mu.RUnlock()

	if !shouldRefresh {
		return
	}

	// 检查 .git/index mtime 是否变更
	gitIndex := filepath.Join(workDir, ".git", "index")
	if stat, err := os.Stat(gitIndex); err == nil {
		f.mu.RLock()
		oldMtime := f.gitIndexMtime
		f.mu.RUnlock()
		if !stat.ModTime().After(oldMtime) && f.signature != "" {
			// mtime 未变且已有缓存，跳过
			return
		}
	}

	f.mu.Lock()
	if f.refreshing {
		f.mu.Unlock()
		return
	}
	f.refreshing = true
	gen := atomic.AddUint64(&f.generation, 1)
	f.mu.Unlock()

	go func(gen uint64) {
		defer func() {
			f.mu.Lock()
			f.refreshing = false
			f.mu.Unlock()
		}()

		paths, err := listFiles(workDir)
		if err != nil || len(paths) == 0 {
			return
		}

		// 代际检查：防止 stale 覆盖
		f.mu.RLock()
		currentGen := atomic.LoadUint64(&f.generation)
		f.mu.RUnlock()
		if currentGen != gen {
			return // 有更新的刷新在进行，放弃本次结果
		}

		sig := pathListSignature(paths)

		f.mu.Lock()
		defer f.mu.Unlock()

		// 再次检查代际
		if atomic.LoadUint64(&f.generation) != gen {
			return
		}

		// signature 未变则跳过重建
		if sig == f.signature && len(f.paths) == len(paths) {
			f.lastRefresh = time.Now()
			return
		}

		f.paths = paths
		f.signature = sig
		f.lastRefresh = time.Now()

		// 更新 .git/index mtime
		if stat, err := os.Stat(gitIndex); err == nil {
			f.gitIndexMtime = stat.ModTime()
		}
	}(gen)
}

// pathListSignature 路径签名（对标 fileSuggestions.ts:111-131）
// stride=n/500 采样 FNV-1a 哈希，含首尾，跳过未变重建
func pathListSignature(paths []string) string {
	if len(paths) == 0 {
		return ""
	}

	h := fnv.New64a()

	// 首个
	h.Write([]byte(paths[0]))
	h.Write([]byte{0})

	// 末个
	if len(paths) > 1 {
		h.Write([]byte(paths[len(paths)-1]))
		h.Write([]byte{0})
	}

	// stride 采样
	stride := len(paths) / 500
	if stride < 1 {
		stride = 1
	}
	for i := stride; i < len(paths)-1; i += stride {
		h.Write([]byte(paths[i]))
		h.Write([]byte{0})
	}

	// 路径总数也参与签名
	countStr := []byte{byte(len(paths) >> 56), byte(len(paths) >> 48), byte(len(paths) >> 40),
		byte(len(paths) >> 32), byte(len(paths) >> 24), byte(len(paths) >> 16),
		byte(len(paths) >> 8), byte(len(paths))}
	h.Write(countStr)

	return string(h.Sum(nil))
}

// search 模糊搜索（对标 fileSuggestions.ts:617 findMatchingFiles）
// 用 sahilm/fuzzy.Match 与路径段边界加分包装
// B-1 修复：paths 为空时触发 startBackgroundCacheRefresh，返回 nil 供下次输入使用
func (f *fileIndex) search(query string, max int) []Completion {
	f.mu.RLock()
	paths := f.paths
	f.mu.RUnlock()

	if len(paths) == 0 {
		// B-1 修复：paths 为空时触发后台刷新
		workDir := cacheWorkDir
		if workDir == "" {
			workDir, _ = os.Getwd()
		}
		if workDir != "" {
			f.startBackgroundCacheRefresh(workDir)
		}
		return nil
	}

	return matchWithBonus(query, paths, max)
}

// ClearCache 清缓存（对标 fileSuggestions.ts:84 clearFileSuggestionCaches）
// resume session 时调用
func ClearCache() {
	fi.mu.Lock()
	defer fi.mu.Unlock()
	atomic.AddUint64(&fi.generation, 1)
	fi.paths = nil
	fi.signature = ""
	fi.gitIndexMtime = time.Time{}
	fi.lastRefresh = time.Time{}
	fi.refreshing = false
}
