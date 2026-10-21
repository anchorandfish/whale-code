//go:build linux

// Package util — 网络共享卷检测（linux 的 fstype 提取）。
package util

import "syscall"

// linux 上 Statfs_t.Type 是文件系统魔数。
const (
	nfsSuperMagic = 0x6969     // NFS_SUPER_MAGIC
	smb2Magic     = 0xFE534D42 // SMB2_MAGIC
	cifsMagic     = 0xFF534D42 // CIFS_MAGIC_NUMBER
)

// fstypeOf 从 Statfs_t 提取文件系统类型名（linux 侧用 Type 魔数）。
func fstypeOf(stat *syscall.Statfs_t) string {
	switch stat.Type {
	case nfsSuperMagic:
		return "nfs"
	case smb2Magic:
		return "smb2"
	case cifsMagic:
		return "cifs"
	default:
		return ""
	}
}
