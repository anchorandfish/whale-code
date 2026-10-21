package filehistory

import (
	"bufio"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// fullGC 全量垃圾回收：扫描索引构建引用集，删除无引用的内容块
// 在 FileHistory 初始化时调用
func (h *FileHistory) fullGC() (removedCount int, freedBytes int64, err error) {
	// 构建引用集
	referenced, err := h.buildReferenceSet()
	if err != nil {
		return 0, 0, fmt.Errorf("build reference set: %w", err)
	}

	// 扫描并清理
	removed, freed, err := h.sweepContent(referenced)
	if err != nil {
		return removed, freed, fmt.Errorf("sweep content: %w", err)
	}

	return removed, freed, nil
}

// buildReferenceSet 扫描索引，构建所有被引用的 hash 集合
// 包括 track 记录的 BeforeHash/AfterHash 和 snapshot 记录的 Hash
func (h *FileHistory) buildReferenceSet() (map[string]bool, error) {
	referenced := make(map[string]bool)

	for _, entry := range h.indexCache {
		for _, fc := range entry.Files {
			if fc.BeforeHash != nil {
				referenced[*fc.BeforeHash] = true
			}
			if fc.AfterHash != nil {
				referenced[*fc.AfterHash] = true
			}
			if fc.Hash != nil {
				referenced[*fc.Hash] = true
			}
		}
	}

	return referenced, nil
}

// sweepContent 扫描 content/ 目录，删除不在引用集中的内容块
func (h *FileHistory) sweepContent(referenced map[string]bool) (removedCount int, freedBytes int64, err error) {
	entries, err := os.ReadDir(h.contentDir)
	if err != nil {
		if os.IsNotExist(err) {
			return 0, 0, nil
		}
		return 0, 0, fmt.Errorf("read content dir: %w", err)
	}

	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}

		// 将文件名转为完整 hash
		hash := hashPrefix + entry.Name()
		if referenced[hash] {
			continue
		}

		// 获取文件大小（用于统计释放空间）
		info, err := entry.Info()
		if err != nil {
			continue
		}
		size := info.Size()

		// 删除无引用的内容块
		if err := os.Remove(filepath.Join(h.contentDir, entry.Name())); err != nil {
			log.Printf("[filehistory] GC: failed to remove %s: %v", entry.Name(), err)
			continue
		}

		removedCount++
		freedBytes += size
	}

	return removedCount, freedBytes, nil
}

// lazyGC 懒回收：处理 gc-pending 清单中的待清理 hash
func (h *FileHistory) lazyGC() (removedCount int, freedBytes int64, err error) {
	f, err := os.Open(h.gcPendingFile)
	if err != nil {
		if os.IsNotExist(err) {
			return 0, 0, nil // 无 gc-pending 文件，正常
		}
		return 0, 0, fmt.Errorf("open gc-pending: %w", err)
	}
	defer f.Close()

	// 重新构建引用集，避免删除仍被引用的块
	referenced, err := h.buildReferenceSet()
	if err != nil {
		return 0, 0, fmt.Errorf("build reference set: %w", err)
	}

	var remainingHashes []string
	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}

		// 如果 hash 仍被引用，跳过删除
		if referenced[line] {
			remainingHashes = append(remainingHashes, line)
			continue
		}

		// 获取文件大小
		size, _ := contentSize(h.contentDir, line)

		// 删除内容块
		if err := removeContent(h.contentDir, line); err != nil {
			log.Printf("[filehistory] lazyGC: failed to remove %s: %v", line, err)
			continue
		}

		removedCount++
		freedBytes += size
	}

	// 清理或重写 gc-pending 文件
	if len(remainingHashes) == 0 {
		os.Remove(h.gcPendingFile)
	} else {
		h.writeGcPending(remainingHashes)
	}

	return removedCount, freedBytes, nil
}

// markForGC 将 hash 加入 gc-pending 清单
// 传入 nil 或空列表时为 no-op
func (h *FileHistory) markForGC(hashes []string) error {
	if len(hashes) == 0 {
		return nil
	}
	return h.appendGcPending(hashes)
}

// emergencyGC 紧急 GC：content/ 目录超过容量上限时触发
// 按 LRU 删除最旧的无引用块，直到目录大小降至 maxBytes 以下
func (h *FileHistory) emergencyGC(maxBytes int64) (removedCount int, freedBytes int64, err error) {
	currentSize, err := contentDirSize(h.contentDir)
	if err != nil {
		return 0, 0, fmt.Errorf("get content dir size: %w", err)
	}

	if currentSize <= maxBytes {
		return 0, 0, nil
	}

	// 构建引用集
	referenced, err := h.buildReferenceSet()
	if err != nil {
		return 0, 0, fmt.Errorf("build reference set: %w", err)
	}

	// 收集所有无引用的内容块及其修改时间
	type contentEntry struct {
		name    string
		size    int64
		modTime time.Time
	}

	var candidates []contentEntry
	entries, err := os.ReadDir(h.contentDir)
	if err != nil {
		return 0, 0, fmt.Errorf("read content dir: %w", err)
	}

	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		hash := hashPrefix + entry.Name()
		if referenced[hash] {
			continue
		}
		info, err := entry.Info()
		if err != nil {
			continue
		}
		candidates = append(candidates, contentEntry{
			name:    entry.Name(),
			size:    info.Size(),
			modTime: info.ModTime(),
		})
	}

	// 按修改时间排序（最旧的排前面，先删除）
	sort.Slice(candidates, func(i, j int) bool {
		return candidates[i].modTime.Before(candidates[j].modTime)
	})

	// 删除最旧的块，直到大小降到阈值以下
	for _, c := range candidates {
		if currentSize <= maxBytes {
			break
		}

		if err := os.Remove(filepath.Join(h.contentDir, c.name)); err != nil {
			log.Printf("[filehistory] emergencyGC: failed to remove %s: %v", c.name, err)
			continue
		}

		removedCount++
		freedBytes += c.size
		currentSize -= c.size
	}

	return removedCount, freedBytes, nil
}

// contentDirSize 计算 content/ 目录总大小
func contentDirSize(contentDir string) (int64, error) {
	var totalSize int64

	entries, err := os.ReadDir(contentDir)
	if err != nil {
		if os.IsNotExist(err) {
			return 0, nil
		}
		return 0, fmt.Errorf("read content dir: %w", err)
	}

	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		info, err := entry.Info()
		if err != nil {
			continue
		}
		totalSize += info.Size()
	}

	return totalSize, nil
}

// ---------- GC 辅助方法 ----------

// appendGcPending 追加 hash 到 gc-pending 清单文件
// gc-pending 文件用于记录待延迟删除的内容块，由 lazyGC 在适当时机清理
func (h *FileHistory) appendGcPending(hashes []string) error {
	if err := os.MkdirAll(filepath.Dir(h.gcPendingFile), 0755); err != nil {
		return fmt.Errorf("create gc dir: %w", err)
	}

	f, err := os.OpenFile(h.gcPendingFile, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		return fmt.Errorf("open gc-pending: %w", err)
	}
	defer f.Close()

	for _, hash := range hashes {
		if _, err := f.WriteString(hash + "\n"); err != nil {
			return fmt.Errorf("write gc-pending: %w", err)
		}
	}
	return nil
}

// writeGcPending 全量重写 gc-pending 清单文件
// 使用原子写（tmp + rename）保证文件完整性，用于更新待清理的 hash 列表
func (h *FileHistory) writeGcPending(hashes []string) error {
	var buf strings.Builder
	for _, hash := range hashes {
		buf.WriteString(hash)
		buf.WriteString("\n")
	}

	tmpFile := h.gcPendingFile + ".tmp"
	if err := os.WriteFile(tmpFile, []byte(buf.String()), 0o600); err != nil {
		return fmt.Errorf("write tmp gc-pending: %w", err)
	}
	if err := os.Rename(tmpFile, h.gcPendingFile); err != nil {
		os.Remove(tmpFile)
		return fmt.Errorf("rename tmp gc-pending: %w", err)
	}
	return nil
}
