package tui

import (
	"unicode/utf8"

	"golang.org/x/text/encoding/simplifiedchinese"
)

// decodeStdinBytes 对 stdin 读取的原始字节做编码兜底解码。
//
// Windows 中文环境下，若 SetConsoleCP(65001) 未生效（旧版 cmd.exe / 某些终端降级 /
// IME 直接输出 GBK），stdin 会发送 GBK(936) 字节。直接当 UTF-8 解码会乱码
// （典型表现：UTF-8 字节被按 GBK 解码，如"你好"→"浣犲ソ"）。
//
// 策略：
// 1. 先 utf8.Valid 校验，合法 UTF-8 直接返回（零成本快路径）
// 2. 非法则尝试 GBK→UTF-8 解码（golang.org/x/text/encoding/simplifiedchinese）
// 3. GBK 也失败，返回原始字节（Go string 按 UTF-8 替换符渲染）
//
// 对标同类 CLI 均无此兜底（依赖终端 UTF-8），whale-code Windows 优先需更稳。
func decodeStdinBytes(b []byte) string {
	// 快路径：合法 UTF-8 直接返回
	if utf8.Valid(b) {
		return string(b)
	}
	// GBK 兜底解码
	dec := simplifiedchinese.GBK.NewDecoder()
	if s, err := dec.Bytes(b); err == nil {
		return string(s)
	}
	// GBK 解码也失败，返回原始字节（UTF-8 替换符兜底）
	return string(b)
}
