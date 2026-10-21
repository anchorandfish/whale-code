//go:build darwin

// Package util — 网络共享卷检测（darwin 的 fstype 提取）。
package util

import (
	"syscall"
	"unsafe"
)

// fstypeOf 从 Statfs_t 提取文件系统类型名（darwin 侧用 Fstypename [16]int8）。
func fstypeOf(stat *syscall.Statfs_t) string {
	// [16]int8 → []byte（无拷贝），再截断到首个 NUL。
	raw := unsafe.Slice((*byte)(unsafe.Pointer(&stat.Fstypename[0])), len(stat.Fstypename))
	for i, c := range raw {
		if c == 0 {
			return string(raw[:i])
		}
	}
	return string(raw)
}
