// Package log — O-1 Unix 平台磁盘空间检查实现（桶 X6）
//
// 在 Unix（Linux/macOS/BSD）系统上通过 syscall.Statfs 获取分区剩余空间。
// 本文件通过 build tag //go:build unix 限制仅在 Unix 平台编译。

//go:build unix || linux || darwin || freebsd || netbsd || openbsd

package log

import (
	"fmt"
	"syscall"
)

// init 注册 Unix 平台的 diskFreeBytes 实现。
// 通过 syscall.Statfs 获取指定目录所在分区的剩余字节数。
func init() {
	platformDiskFreeBytes = unixDiskFreeBytes
}

// unixDiskFreeBytes 返回指定目录所在分区的剩余字节数（Unix 实现）。
//
// 实现：
//   - 调用 syscall.Statfs 获取分区统计信息
//   - Bavail * Bsize = 普通用户可用剩余字节数
//
// 参数：
//   - dir：已存在的目录路径
//
// 返回：
//   - 剩余字节数
//   - 错误（目录不存在、权限不足等）
func unixDiskFreeBytes(dir string) (int64, error) {
	var stat syscall.Statfs_t
	if err := syscall.Statfs(dir, &stat); err != nil {
		return 0, fmt.Errorf("statfs %q: %w", dir, err)
	}
	// Bavail = 普通用户可用块数（不含 root 保留空间）
	// Bsize = 块大小（字节）
	return int64(stat.Bavail) * int64(stat.Bsize), nil
}
