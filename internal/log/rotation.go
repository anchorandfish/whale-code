// Package log — O-1 日志轮转与磁盘空间检查（桶 X6）
//
// 提供 lumberjack 之外的补充能力：
//  1. CheckDiskSpace：启动时检查目标目录所在分区剩余空间，<1GB 时发出警告
//  2. RotateJSONLByDate：对 quality.jsonl / trace.jsonl 等非 lumberjack 管理的
//     JSONL 文件按日期分片 + 清理过期分片
//
// 对标同类 CLI tracing + 文件轮转；同类 CLI winston + daily rotate
package log

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// MinFreeSpaceBytes 启动时建议的最小剩余磁盘空间（1GB）
// 低于此值时 CheckDiskSpace 会发出警告（不阻断启动）
const MinFreeSpaceBytes int64 = 1 * 1024 * 1024 * 1024

// CheckDiskSpace 检查目标路径所在分区的剩余磁盘空间。
// 当剩余空间 < MinFreeSpaceBytes（1GB）时，向 stderr 输出警告（不阻断启动）。
//
// O-1 修复（桶 X6）：启动时检查磁盘剩余空间，避免长会话因磁盘耗尽崩溃。
//
// 参数：
//   - path：任意路径（文件或目录），函数内部取其所在分区
//
// 返回：
//   - freeBytes：剩余字节数（获取失败时为 -1）
//   - warned：是否发出过低空间警告
//
// 跨平台：
//   - Unix（Linux/macOS）：syscall.Statfs
//   - Windows：syscall.GetDiskFreeSpaceEx（通过包内 windowsDiskFreeBytes 封装）
func CheckDiskSpace(path string) (freeBytes int64, warned bool) {
	freeBytes, err := diskFreeBytes(path)
	if err != nil {
		// 获取失败不阻断，仅记录
		fmt.Fprintf(os.Stderr, "[log] CheckDiskSpace: 无法获取 %q 的磁盘剩余空间: %v\n", path, err)
		return -1, false
	}
	if freeBytes < MinFreeSpaceBytes {
		warned = true
		// 同时向 stderr 和 slog 输出（slog 已被 InitFileLogger 切到 MultiWriter）
		fmt.Fprintf(os.Stderr, "[log] 警告: 磁盘剩余空间不足 1GB（当前 %d 字节，路径 %q），长会话可能因磁盘耗尽崩溃\n", freeBytes, path)
		Warn("磁盘剩余空间不足 1GB，长会话可能因磁盘耗尽崩溃",
			"free_bytes", freeBytes,
			"path", path)
	}
	return freeBytes, warned
}

// RotateJSONLByDate 对非 lumberjack 管理的 JSONL 文件按日期分片 + 清理过期分片。
//
// 适用场景：quality.jsonl / trace.jsonl 等持续追加的 JSONL 文件，
// 这些文件不走 lumberjack（lumberjack 按字节大小轮转，不按日期）。
//
// 行为：
//  1. 若主文件（path）存在且最后修改日期早于今天，将其重命名为
//     `<path>.YYYY-MM-DD`（按上次修改日期命名），实现按日分片
//  2. 清理 `<path>.*` 形式的旧分片中超过 maxAgeDays 天的文件
//  3. 重命名失败（如另一个进程正在写入）时返回 error，调用方可降级
//
// 参数：
//   - path：主 JSONL 文件路径（如 ~/.whale/orchestrator/quality.jsonl）
//   - maxAgeDays：旧分片保留天数（建议 30 天）
//
// 返回：
//   - rotated：是否执行了分片（true 表示主文件被重命名）
//   - cleaned：被清理的旧分片数量
//   - err：操作中的错误（非致命错误仍会返回，调用方按需降级）
//
// 并发安全：调用方应确保在写入 quality.jsonl 的 goroutine 之外调用
// （如启动时一次性调用，配合 QualityStore.Append 的 O_APPEND 模式，
// 重命名后下次 Append 会自动创建新文件）。
func RotateJSONLByDate(path string, maxAgeDays int) (rotated bool, cleaned int, err error) {
	if path == "" {
		return false, 0, fmt.Errorf("rotation: path must not be empty")
	}
	if maxAgeDays <= 0 {
		return false, 0, fmt.Errorf("rotation: maxAgeDays must be positive, got %d", maxAgeDays)
	}

	// 1. 检查主文件是否需要分片
	info, err := os.Stat(path)
	if err == nil {
		// 文件存在，检查最后修改日期是否早于今天
		fileDate := info.ModTime().Truncate(24 * time.Hour)
		today := time.Now().Truncate(24 * time.Hour)
		if fileDate.Before(today) {
			// 重命名为 <path>.YYYY-MM-DD
			dateSuffix := fileDate.Format("2006-01-02")
			rotatedPath := path + "." + dateSuffix
			// 若目标已存在（多次轮转竞态），追加 .1 / .2 后缀
			rotatedPath = uniqueRotatedPath(rotatedPath)
			if err := os.Rename(path, rotatedPath); err != nil {
				return false, 0, fmt.Errorf("rotation: rename %q -> %q: %w", path, rotatedPath, err)
			}
			rotated = true
		}
	} else if !os.IsNotExist(err) {
		// 非"文件不存在"的其他错误（如权限），返回
		return false, 0, fmt.Errorf("rotation: stat %q: %w", path, err)
	}

	// 2. 清理过期分片：扫描 <path>.* 文件，删除超过 maxAgeDays 天的
	dir := filepath.Dir(path)
	base := filepath.Base(path)
	entries, err := os.ReadDir(dir)
	if err != nil {
		// 读目录失败不致命（可能目录不存在），返回已完成的 rotated
		return rotated, 0, nil
	}

	cutoff := time.Now().Add(-time.Duration(maxAgeDays) * 24 * time.Hour)
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		name := entry.Name()
		// 匹配 <base>.YYYY-MM-DD 模式
		if !strings.HasPrefix(name, base+".") {
			continue
		}
		info, err := entry.Info()
		if err != nil {
			continue
		}
		if info.ModTime().Before(cutoff) {
			fullPath := filepath.Join(dir, name)
			if rmErr := os.Remove(fullPath); rmErr != nil {
				// 删除失败不致命（可能权限不足），记录后继续
				Warn("rotation: 清理过期分片失败",
					"file", fullPath,
					"error", rmErr)
				continue
			}
			cleaned++
		}
	}

	return rotated, cleaned, nil
}

// uniqueRotatedPath 在 path 已存在时追加 .1 .2 .3... 后缀，避免覆盖。
// 例如 quality.jsonl.2026-07-23 已存在时返回 quality.jsonl.2026-07-23.1。
func uniqueRotatedPath(path string) string {
	if _, err := os.Stat(path); os.IsNotExist(err) {
		return path
	}
	for i := 1; i < 1000; i++ {
		candidate := fmt.Sprintf("%s.%d", path, i)
		if _, err := os.Stat(candidate); os.IsNotExist(err) {
			return candidate
		}
	}
	// 极端情况：1000 个同名分片，返回带纳秒时间戳的路径
	return fmt.Sprintf("%s.%d", path, time.Now().UnixNano())
}

// diskFreeBytes 跨平台封装：返回指定路径所在分区的剩余字节数。
// Unix 走 syscall.Statfs，Windows 走 windowsDiskFreeBytes（编译时分支）。
func diskFreeBytes(path string) (int64, error) {
	// 确保路径所在目录存在（Statfs 对不存在的路径会失败）
	dir := filepath.Dir(path)
	if dir == "" {
		dir = "."
	}
	return platformDiskFreeBytes(dir)
}

// platformDiskFreeBytes 平台特定实现，由 disk_unix.go / disk_windows.go 提供。
// 在 unix 系统使用 syscall.Statfs，在 windows 系统使用 GetDiskFreeSpaceEx。
//
// 参数：
//   - dir：已存在的目录路径
//
// 返回：
//   - 剩余字节数
//   - 错误（如目录不存在、权限不足）
//
// 注意：本函数签名在 rotation_unix.go 和 rotation_windows.go 中实现，
// 通过编译时分支选择平台实现。在非 Unix/非 Windows 平台（如 plan9）返回错误。
var platformDiskFreeBytes func(dir string) (int64, error)

// sortedRotatedFiles 返回按修改时间排序的过期分片列表（最旧在前）。
// 辅助函数，供测试与外部诊断调用。
//
// 参数：
//   - path：主 JSONL 文件路径
//
// 返回：
//   - 分片文件完整路径列表（按 ModTime 升序）
//   - 错误
func sortedRotatedFiles(path string) ([]string, error) {
	dir := filepath.Dir(path)
	base := filepath.Base(path)
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, err
	}
	type fileWithTime struct {
		path    string
		modTime time.Time
	}
	var files []fileWithTime
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		name := entry.Name()
		if !strings.HasPrefix(name, base+".") {
			continue
		}
		info, err := entry.Info()
		if err != nil {
			continue
		}
		files = append(files, fileWithTime{
			path:    filepath.Join(dir, name),
			modTime: info.ModTime(),
		})
	}
	sort.Slice(files, func(i, j int) bool {
		return files[i].modTime.Before(files[j].modTime)
	})
	result := make([]string, len(files))
	for i, f := range files {
		result[i] = f.path
	}
	return result, nil
}
