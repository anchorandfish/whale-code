package platform

import (
	"fmt"
	"net/url"
	"strings"
)

// FilePathToURI 文件路径转 file:/// URI。
// 集中 lsp/client.go 13 处重复的 4 行模式（122-124 等）。
// 算法：ToSlash(p) → URL 编码（中文/空格/特殊字符）→ 拼 "file:///" 前缀。
// Windows C:\Users\foo → file:///C:/Users/foo
// Unix /home/user/foo → file:///home/user/foo
// UNC \\server\share → file://server/share（双斜杠保留 authority）
func FilePathToURI(p string) string {
	slash := ToSlash(p)

	// UNC 路径：\\server\share → file://server/share
	if IsUNCPath(p) {
		// 去掉开头 //，得到 server/share
		uncPath := strings.TrimLeft(slash, "/")
		return "file://" + encodeURIPath(uncPath)
	}

	encoded := encodeURIPath(slash)

	// Unix 绝对路径：/home/... → file:///home/...
	if strings.HasPrefix(slash, "/") {
		return "file://" + encoded
	}

	// Windows 绝对路径：C:/... → file:///C:/...
	return "file:///" + encoded
}

// encodeURIPath 逐段 URL 编码路径，保留 / 分隔符。
// url.PathEscape 会编码 /，因此需手动按段编码再用 / 拼接。
func encodeURIPath(p string) string {
	segments := strings.Split(p, "/")
	for i, seg := range segments {
		segments[i] = url.PathEscape(seg)
	}
	return strings.Join(segments, "/")
}

// URIToFilePath file:/// URI 转文件路径（含 URL 解码）。
// 当前 whale-code 缺失此函数（lsp 仅单向拼接，反向解析缺失导致中文路径断）。
// 对标同类 CLI uriToFilePath：去前缀 + Windows 驱动器 + decodeURIComponent。
// 算法：去 file:/// 或 file:// 前缀 → 识别 UNC（file://server/share）→ Windows 去 leading / → URL 解码。
// 容错：畸形 URI 不 panic，返回 error。
func URIToFilePath(uri string) (string, error) {
	if !strings.HasPrefix(uri, "file://") {
		return "", fmt.Errorf("invalid file URI: missing file:// prefix")
	}

	rest := strings.TrimPrefix(uri, "file://")
	// 现在 rest 可能是：
	// - /C:/Users/foo （Windows 盘符）
	// - /home/user/foo （Unix 绝对路径）
	// - server/share/path （UNC，无前导 /，因为 file:// 后直接是 server 名）

	// 先判断是否为 UNC 路径：
	// file://server/share 的 rest 是 server/share（无前导 /）
	// file:////server/share 的 rest 是 //server/share（有两个前导 /）
	var path string
	isUNC := false
	if strings.HasPrefix(rest, "//") {
		// file:////server/share → rest = //server/share
		isUNC = true
		path = rest
	} else if !strings.HasPrefix(rest, "/") {
		// file://server/share → rest = server/share
		// 这是 UNC 路径（authority = server，path = /share）
		isUNC = true
		path = "//" + rest
	} else {
		// 普通路径：/C:/... 或 /home/...
		path = rest
	}

	if isUNC {
		decoded, err := url.PathUnescape(path)
		if err != nil {
			return "", fmt.Errorf("unescape URI path: %w", err)
		}
		return FromSlash(decoded), nil
	}

	// Windows 盘符：/C:/... → C:/...
	if len(path) >= 3 && path[0] == '/' && path[2] == ':' {
		path = path[1:]
	}

	// URL 解码（处理中文等）
	decoded, err := url.PathUnescape(path)
	if err != nil {
		return "", fmt.Errorf("unescape URI path: %w", err)
	}

	// 转平台原生分隔符
	return FromSlash(decoded), nil
}
