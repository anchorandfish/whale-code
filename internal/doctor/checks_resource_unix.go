//go:build !windows

package doctor

import (
	"fmt"
	"os"
	"strconv"
	"strings"
	"syscall"
)

// getMemoryInfo 获取系统内存信息（Unix/Linux 平台）。
// 读取 /proc/meminfo 获取总内存和可用内存。
// 返回：总内存(MB) + 可用内存(MB) + 错误。
func getMemoryInfo() (totalMB, availMB int, err error) {
	data, err := os.ReadFile("/proc/meminfo")
	if err != nil {
		return 0, 0, fmt.Errorf("read /proc/meminfo: %w", err)
	}

	totalKB, availKB := 0, 0
	for _, line := range strings.Split(string(data), "\n") {
		if strings.HasPrefix(line, "MemTotal:") {
			totalKB = parseMemInfoLine(line)
		} else if strings.HasPrefix(line, "MemAvailable:") {
			availKB = parseMemInfoLine(line)
		}
	}

	if totalKB == 0 {
		return 0, 0, fmt.Errorf("MemTotal not found in /proc/meminfo")
	}

	// MemAvailable 在旧内核（<3.14）可能不存在，回退到 MemFree + Buffers + Cached
	if availKB == 0 {
		availKB = totalKB // 回退：保守用总内存（不触发低可用内存警告）
	}

	return totalKB / 1024, availKB / 1024, nil
}

// parseMemInfoLine 解析 /proc/meminfo 中的一行，返回数值（KB）。
// 行格式："MemTotal:       16384000 kB"
func parseMemInfoLine(line string) int {
	fields := strings.Fields(line)
	if len(fields) < 2 {
		return 0
	}
	val, err := strconv.Atoi(fields[1])
	if err != nil {
		return 0
	}
	return val
}

// getDiskInfo 获取指定路径所在磁盘的剩余空间（Unix/Linux 平台）。
// 使用 syscall.Statfs 获取文件系统统计信息。
// 返回：剩余空间(MB) + 错误。
func getDiskInfo(path string) (freeMB int, err error) {
	var stat syscall.Statfs_t
	if err := syscall.Statfs(path, &stat); err != nil {
		return 0, fmt.Errorf("statfs %s: %w", path, err)
	}
	// 剩余空间 = 空闲块数 × 块大小
	freeBytes := stat.Bfree * uint64(stat.Bsize)
	freeMB = int(freeBytes / 1024 / 1024)
	return freeMB, nil
}
