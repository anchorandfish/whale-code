// Package tui - 贴图落盘存储（⑧）。
//
// 为什么要落盘：输入框里画不出缩略图（终端是文字世界），用户贴完图后**看不到**自己贴了什么。
// 同类 CLI 的解法是让占位符变成可点击的 OSC 8 超链接，指向磁盘上的图片文件
// （同类 CLI `ClickableImageRef.tsx` + `getStoredImagePath`）。whale 此前剪贴板图
// 只以 base64 存在内存里（临时文件 `defer os.Remove`）⇒ **磁盘上没有可点开的东西**，
// 所以 ⑧ 需要一个落盘载体。
package tui

import (
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"strings"

	"github.com/anchorandfish/whale-code/internal/util"
	"github.com/anchorandfish/whale-code/internal/whalehome"
)

// imageStoreSubdir 是贴图存储目录名（位于 whale 状态目录之下，与 sessions/logs 同级）。
const imageStoreSubdir = "images"

// imageStoreDir 返回贴图存储目录；whale 状态目录不可得时返回 ""。
func imageStoreDir() string {
	dir := whalehome.Dir()
	if dir == "" {
		return ""
	}
	return filepath.Join(dir, imageStoreSubdir)
}

// storePastedImage 把（压缩后的）贴图内容落盘，返回其绝对路径；任何失败返回 ""。
//
// 取名方式 = **内容寻址**（sha256 前 8 字节十六进制 + 扩展名）：
// - 同一张图重复粘贴 ⇒ 同一个文件（幂等），不需要额外的去重逻辑与清理；
// - 崩溃中途不会留下半成品（util.AtomicWriteFile 先写临时名再 rename）；
// - 路径稳定 ⇒ 已经渲染出去的超链接不会因为后续操作失效。
//
// 返回 "" 时调用方降级为「无超链接的成块占位符」—— 与「终端不支持超链接」
// 是同一条降级路径（对齐同类 CLI 的 fallback 语义：链接拿不到就退回普通文本）。
func storePastedImage(data []byte, mediaType string) string {
	dir := imageStoreDir()
	if dir == "" || len(data) == 0 {
		return ""
	}
	ext := imageExtForMediaType(mediaType)
	if ext == "" {
		return "" // 非已知图片格式：不落盘（没有可打开的图片文件）
	}
	sum := sha256.Sum256(data)
	path := filepath.Join(dir, hex.EncodeToString(sum[:8])+"."+ext)
	if _, err := os.Stat(path); err == nil {
		return path // 同内容已经在盘上（含并发粘贴的同一张图）
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return ""
	}
	if err := util.AtomicWriteFile(path, data, 0o600); err != nil {
		return ""
	}
	return path
}

// imageExtForMediaType 把图片 MIME 映射为文件扩展名；非已知图片类型返回 ""。
// 与内部实际支持的格式（image.Paste 产出的 MediaType）保持一致。
func imageExtForMediaType(mediaType string) string {
	switch strings.ToLower(strings.TrimSpace(mediaType)) {
	case "image/png":
		return "png"
	case "image/jpeg", "image/jpg":
		return "jpg"
	case "image/gif":
		return "gif"
	case "image/webp":
		return "webp"
	default:
		return ""
	}
}
