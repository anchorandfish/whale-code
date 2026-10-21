// Package tools — 二进制文件检测（薄包装，委托 internal/util canonical 实现）。
//
// 历史问题：早期版本 file_cache.go 与 search_engine.go 存在两份不一致的
// isBinaryFile 实现。现已统一为 internal/util 包的 canonical 实现，
// 本文件保留对外 API（IsBinaryFile / IsBinaryExt / BinaryExts）及包内别名
// （isBinaryFile / isBinaryExt / isBinaryFileCached），内部委托 util，
// 调用方（file_cache / pipeline / search_engine）零改动。
//
// 规范策略（对齐 ripgrep，实现在 internal/util/binary.go）：
//  1. 扩展名快速判定（BinaryExts）：覆盖常见二进制格式，O(1) 命中即返回 true。
//  2. 内容检测：读取首 8KB，若包含 NUL 字节则判定为二进制（ripgrep 策略）。
package tools

import (
	"sync"

	"github.com/anchorandfish/whale-code/internal/util"
)

// BinaryExts 引用 util 包 canonical 来源（map 引用语义）。
// 运行期只读，不允许修改。
var BinaryExts = util.BinaryExts

// binaryExts 是 BinaryExts 的包内别名，保持既有调用方（search_engine 等）
// 无需改动即可工作。
var binaryExts = BinaryExts

// IsBinaryExt 委托 util.IsBinaryExt 按扩展名快速判定是否为二进制文件。
// 大小写不敏感；无扩展名返回 false。
func IsBinaryExt(path string) bool {
	return util.IsBinaryExt(path)
}

// isBinaryExt 是 IsBinaryExt 的包内别名，兼容既有调用方与测试。
func isBinaryExt(path string) bool {
	return IsBinaryExt(path)
}

// IsBinaryFile 委托 util.IsBinary 检测给定路径是否为二进制文件。
//
// 策略：
//  1. 扩展名命中 BinaryExts → true（覆盖常见格式，避免读盘）
//  2. 读取首 8KB，若包含 NUL 字节 → true（ripgrep 内容检测策略）
//  3. 打开失败 → false（视为非二进制，交由上层处理路径错误）
//  4. 空文件 → false
//
// 注：本函数不缓存结果；需要缓存请使用 isBinaryFileCached。
func IsBinaryFile(path string) bool {
	return util.IsBinary(path)
}

// isBinaryFile 是 IsBinaryFile 的包内别名，兼容既有调用方与测试。
func isBinaryFile(path string) bool {
	return IsBinaryFile(path)
}

// ============================================================================
// 二进制检测缓存（search_engine 历史使用的 LRU 式缓存）
// 缓存是业务层关注点，util 不提供；本缓存内部调 util.IsBinary。
// ============================================================================

var (
	binaryCache   map[string]bool
	binaryCacheMu sync.Mutex
)

const maxBinaryCacheSize = 5000

func init() {
	binaryCache = make(map[string]bool)
}

// isBinaryFileCached 是带 LRU 式缓存的二进制检测，供 search_engine 在
// 大规模文件遍历时使用，避免对同一文件重复读盘。
//
// 缓存达到 maxBinaryCacheSize 时整体重置（简单 LRU 近似，足够本场景）。
func isBinaryFileCached(path string) bool {
	binaryCacheMu.Lock()
	if result, ok := binaryCache[path]; ok {
		binaryCacheMu.Unlock()
		return result
	}
	binaryCacheMu.Unlock()

	result := isBinaryFile(path) // 委托 util.IsBinary

	binaryCacheMu.Lock()
	if len(binaryCache) >= maxBinaryCacheSize {
		binaryCache = make(map[string]bool)
	}
	binaryCache[path] = result
	binaryCacheMu.Unlock()

	return result
}
