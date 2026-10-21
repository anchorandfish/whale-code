// Package util — 二进制文件检测（canonical 实现）。
//
// 策略对齐 ripgrep：扩展名快速判定 + 内容 NUL 字节检测。
// 供 tools/binary_detect.go 薄包装引用，其他业务包可直接 import util 使用。
package util

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
)

// IsBinaryExt 按扩展名快速判定是否为二进制文件。
// 大小写不敏感；无扩展名返回 false。
func IsBinaryExt(path string) bool {
	ext := strings.ToLower(filepath.Ext(path))
	return BinaryExts[ext]
}

// IsBinary 检测给定路径是否为二进制文件（canonical 实现）。
// 策略：扩展名命中 → true；否则读首 8KB 检测 NUL 字节。
// 打开失败 → false（交由上层处理路径错误）；空文件 → false。
func IsBinary(path string) bool {
	if IsBinaryExt(path) {
		return true
	}
	f, err := os.Open(path)
	if err != nil {
		return false
	}
	defer f.Close()
	buf := make([]byte, 8192)
	n, err := f.Read(buf)
	if err != nil && n == 0 {
		return false
	}
	return bytes.Contains(buf[:n], []byte{0})
}
