//go:build !windows

// P1-2 修复：非 Windows 平台控制台编码设置（build tag 拆分）
// 非 Windows 平台终端默认 UTF-8 + VT 支持，无需设置控制台编码。

package main

// setupConsoleEncoding 非 Windows 平台空实现
func setupConsoleEncoding() {
	// 非 Windows 平台无需设置控制台编码
}
