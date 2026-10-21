// Package sandbox — 沙箱路径解析共享工具（P1-SEC）。
//
// 供 darwin Seatbelt 与 linux Landlock 共用：沙箱内核机制（SBPL subpath /
// Landlock path-beneath）都按内核眼中的真实路径匹配，必须先解析符号链接。
package sandbox

import (
	"fmt"
	"path/filepath"
)

// resolveSandboxPath 解析符号链接并返回绝对路径。
// 目标不存在时解析父目录（与 sandbox.go resolvePath 同语义）。
func resolveSandboxPath(p string) (string, error) {
	if p == "" {
		return "", fmt.Errorf("empty path")
	}
	abs, err := filepath.Abs(p)
	if err != nil {
		return "", err
	}
	if resolved, err := filepath.EvalSymlinks(abs); err == nil {
		return resolved, nil
	}
	parent := filepath.Dir(abs)
	resolvedParent, err := filepath.EvalSymlinks(parent)
	if err != nil {
		// 父目录也无法解析（极端环境），退回原始绝对路径
		return abs, nil
	}
	return filepath.Join(resolvedParent, filepath.Base(abs)), nil
}
