package filehistory

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"strconv"

	"github.com/anchorandfish/whale-code/internal/util"
)

// hashPrefix 是内容寻址存储中 hash 字符串的前缀
const hashPrefix = "sha256:"

// writeContent 将文件内容写入内容寻址存储
// 返回内容的 SHA256 hash（格式：sha256:hex64）
// 相同内容只存一份（自动去重）。
//
// S10 / HG3（决策 HG3=b，正确性优先）：不再对大文件走"前 4KB 前缀 + 大小"的
// 近似 hash。该近似会让前缀+大小相同的不同大文件共享同一 content-address，
// 还原时可能把 A 的内容写回 B 的文件（静默数据损坏）。内容寻址的正确性依赖
// hash 唯一标识内容，因此一律使用全量 SHA256。大文件的 hash 开销是可接受的
// 一次性成本，换取还原保真度。
func writeContent(contentDir string, content []byte) (hash string, err error) {
	hash = computeHash(content)

	// 去重：如果内容已存在则直接返回 hash
	if contentExists(contentDir, hash) {
		return hash, nil
	}

	// 确保内容目录存在
	if err := os.MkdirAll(contentDir, 0755); err != nil {
		return "", fmt.Errorf("create content dir: %w", err)
	}

	// 原子写入内容块
	contentPath := hashToPath(contentDir, hash)
	if err := util.AtomicWriteFile(contentPath, content, 0o600); err != nil {
		return "", fmt.Errorf("write content: %w", err)
	}

	return hash, nil
}

// readContent 从内容寻址存储读取指定 hash 的文件内容
// hash 格式：sha256:hex64
func readContent(contentDir string, hash string) ([]byte, error) {
	contentPath := hashToPath(contentDir, hash)
	data, err := os.ReadFile(contentPath)
	if err != nil {
		return nil, fmt.Errorf("read content %s: %w", hash, err)
	}
	return data, nil
}

// contentExists 检查指定 hash 的内容块是否存在
func contentExists(contentDir string, hash string) bool {
	contentPath := hashToPath(contentDir, hash)
	_, err := os.Stat(contentPath)
	return err == nil
}

// computeHash 计算文件内容的 SHA256 hash
// 返回格式：sha256:hex64
func computeHash(content []byte) string {
	sum := sha256.Sum256(content)
	return hashPrefix + hex.EncodeToString(sum[:])
}

// computeLargeFileHash 为大文件生成 hash。
//
// Deprecated: S10 / HG3 之后 writeContent 一律使用全量 SHA256（computeHash），
// 本函数不再被生产路径调用。保留仅为向后兼容旧的调用点/测试；其实现已改为
// 委托 computeHash，避免任何残留的"前缀近似 hash"碰撞隐患。
func computeLargeFileHash(content []byte) string {
	return computeHash(content)
}

// contentSize 返回内容块文件大小（用于 GC 统计）
func contentSize(contentDir string, hash string) (int64, error) {
	contentPath := hashToPath(contentDir, hash)
	info, err := os.Stat(contentPath)
	if err != nil {
		return 0, fmt.Errorf("stat content %s: %w", hash, err)
	}
	return info.Size(), nil
}

// removeContent 删除指定 hash 的内容块（仅 GC 调用）
func removeContent(contentDir string, hash string) error {
	contentPath := hashToPath(contentDir, hash)
	if err := os.Remove(contentPath); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("remove content %s: %w", hash, err)
	}
	return nil
}

// hashToPath 将 hash 转为内容存储路径
// hash 格式 "sha256:abc123..." → contentDir/abc123...
func hashToPath(contentDir string, hash string) string {
	// 去掉 "sha256:" 前缀，用剩余的 hex 字符串作为文件名
	hexPart := hash
	if len(hash) > len(hashPrefix) && hash[:len(hashPrefix)] == hashPrefix {
		hexPart = hash[len(hashPrefix):]
	}
	return filepath.Join(contentDir, hexPart)
}

// parseHashFromPath 从内容存储路径提取 hash 字符串（反向操作）
// TODO: reserved for future GC optimization（当前 GC 通过 os.ReadDir + 文件名直接处理）
func parseHashFromPath(contentDir string, fullPath string) string {
	rel, err := filepath.Rel(contentDir, fullPath)
	if err != nil {
		return ""
	}
	return hashPrefix + rel
}

// contentFileName 从 hash 中提取纯 hex 文件名（不含路径和前缀）
// TODO: reserved for future use（当前 hashToPath 内联处理了前缀剥离）
func contentFileName(hash string) string {
	if len(hash) > len(hashPrefix) && hash[:len(hashPrefix)] == hashPrefix {
		return hash[len(hashPrefix):]
	}
	return hash
}

// formatFileSize 将文件大小格式化为人类可读字符串（供日志和调试使用）
func formatFileSize(bytes int64) string {
	if bytes < 1024 {
		return strconv.FormatInt(bytes, 10) + "B"
	}
	if bytes < 1024*1024 {
		return fmt.Sprintf("%.1fKB", float64(bytes)/1024)
	}
	if bytes < 1024*1024*1024 {
		return fmt.Sprintf("%.1fMB", float64(bytes)/(1024*1024))
	}
	return fmt.Sprintf("%.1fGB", float64(bytes)/(1024*1024*1024))
}
