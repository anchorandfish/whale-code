//go:build windows

// Package util — 网络共享卷检测（Windows 实现）。
package util

import (
	"golang.org/x/sys/windows"
)

// isNetworkFSOS 用 GetDriveType 判断路径所在盘是否为网络盘（DRIVE_REMOTE = 4）。
func isNetworkFSOS(path string) (network bool, fstype string, err error) {
	root, err := windows.UTF16PtrFromString(path)
	if err != nil {
		return false, "", err
	}
	dt := windows.GetDriveType(root)
	switch dt {
	case windows.DRIVE_REMOTE:
		return true, "remote", nil
	default:
		return false, "", nil
	}
}
