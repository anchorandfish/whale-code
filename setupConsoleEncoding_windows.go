//go:build windows

// P1-2 修复：Windows 控制台编码设置（build tag 拆分）
// 启用 UTF-8 代码页 + VT 处理，解决旧版 cmd.exe 下 Unicode 边框/emoji 乱码问题。
//
// 2026-07-31 第4轮乱码根治：根因是 tea.WithInputTTY() 导致 bubbletea 跳过 coninput
// 读取器，回退到 go-localereader（硬编码 CP_ACP=936/GBK 解码），与 SetConsoleCP(65001)
// 冲突导致输入乱码。修复方式：main.go 中 Windows 下不使用 tea.WithInputTTY()。
// 本文件仅需设置代码页 + VT 处理，无需 consoleUTF16Writer。

package main

import (
	"fmt"
	"os"

	"golang.org/x/sys/windows"

	"github.com/anchorandfish/whale-code/internal/log"
)

// CP_UTF8 是 Win32 代码页常量 65001（golang.org/x/sys/windows 未导出，需本地定义）
const CP_UTF8 = 65001

// setupConsoleEncoding 设置 Windows 控制台为 UTF-8 + VT 处理模式。
// 代码页设置后，coninput 读取器（ReadConsoleInputW）正确处理 Unicode 输入，
// VT 处理让 ANSI 转义序列（颜色/光标/边框）被正确解析。
func setupConsoleEncoding() {
	// 1. 设置输出代码页为 UTF-8 (65001)
	if err := windows.SetConsoleOutputCP(CP_UTF8); err != nil {
		fmt.Fprintf(os.Stderr, "[console] SetConsoleOutputCP failed: %v\n", err)
		log.Warn("[console] SetConsoleOutputCP failed", "err", err)
	}

	// 2. 设置输入代码页为 UTF-8
	if err := windows.SetConsoleCP(CP_UTF8); err != nil {
		fmt.Fprintf(os.Stderr, "[console] SetConsoleCP failed: %v\n", err)
		log.Warn("[console] SetConsoleCP failed", "err", err)
	}

	// 3. 启用输出 VT 处理（ANSI 转义序列支持：颜色/光标/边框）
	outHandle := windows.Handle(os.Stdout.Fd())
	var outMode uint32
	if err := windows.GetConsoleMode(outHandle, &outMode); err == nil {
		outMode |= windows.ENABLE_VIRTUAL_TERMINAL_PROCESSING
		outMode |= windows.ENABLE_PROCESSED_OUTPUT
		if err := windows.SetConsoleMode(outHandle, outMode); err != nil {
			fmt.Fprintf(os.Stderr, "[console] SetConsoleMode(output) failed: %v\n", err)
		}
	}

	// 4. 启用输入 VT 处理（鼠标事件/方向键转义序列）
	inHandle := windows.Handle(os.Stdin.Fd())
	var inMode uint32
	if err := windows.GetConsoleMode(inHandle, &inMode); err == nil {
		inMode |= windows.ENABLE_VIRTUAL_TERMINAL_INPUT
		// 保留 ENABLE_PROCESSED_INPUT 以支持 Ctrl+C 信号
		inMode |= windows.ENABLE_PROCESSED_INPUT
		if err := windows.SetConsoleMode(inHandle, inMode); err != nil {
			fmt.Fprintf(os.Stderr, "[console] SetConsoleMode(input) failed: %v\n", err)
		}
	}
}
