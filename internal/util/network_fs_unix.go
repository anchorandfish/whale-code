//go:build !windows

// Package util — 网络共享卷检测（Unix/Linux/macOS 实现）。
package util

import (
	"strings"
	"syscall"
)

// isNetworkFSOS 用 syscall.Statfs 读取路径所在文件系统类型，判断是否为网络卷。
//
// 平台字段（darwin 侧已用 go doc syscall.Statfs_t 核实）：
//   - darwin / *BSD：Fstypename [16]int8（转 string 去 NUL）
//   - linux：Type（魔数）
//
// 命中即判网络卷：nfs / smbfs / afpfs / webdav（darwin）· NFS_SUPER_MAGIC / SMB2_MAGIC（linux）。
func isNetworkFSOS(path string) (network bool, fstype string, err error) {
	var stat syscall.Statfs_t
	if err := syscall.Statfs(path, &stat); err != nil {
		return false, "", err
	}
	fs := fstypeOf(&stat)
	if fs == "" {
		// 拿不到类型名 ⇒ 不确定 ⇒ 不判本地（由调用方按 fail-safe 处理）。
		return false, "", nil
	}
	switch {
	case strings.Contains(fs, "nfs"):
		return true, fs, nil
	case strings.Contains(fs, "smb"):
		return true, fs, nil
	case strings.Contains(fs, "afp"):
		return true, fs, nil
	case strings.Contains(fs, "webdav"):
		return true, fs, nil
	case strings.Contains(fs, "cifs"):
		return true, fs, nil
	default:
		return false, fs, nil
	}
}
