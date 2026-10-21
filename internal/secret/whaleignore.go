// Package secret — .whaleignore 敏感文件预扫描
package secret

import (
	"path/filepath"
	"strings"
)

// DefaultSensitivePatterns 是默认阻断的敏感文件 glob 模式（对标 .gitignore 语法）。
// 第一重防护：glob 模式匹配文件名/路径。
var DefaultSensitivePatterns = []string{
	".env", ".env.*", // 环境变量文件
	"*.pem", "*.key", // 私钥
	"id_rsa", "id_ed25519", // SSH 私钥
	"id_*", // SSH 私钥通配
	".aws/credentials", ".aws/*",
	".ssh/*",
	"*.keystore", "*.jks", // Java keystore
	"credentials.json", // 凭据文件
}

// ScanWhaleIgnore 将文件列表分为 safe / blocked 两组（双重防护，与 一致）。
// 第一重：blocked 命中 DefaultSensitivePatterns glob 模式。
// 第二重：对 glob 未命中的文件名，再用 ScanSecrets 脱敏正则兜底扫描，
//
//	命中密钥特征（如 sk-/ghp_/sk-ant- 前缀）的也进入 blocked。
func ScanWhaleIgnore(files []string) (safe, blocked []string) {
	safe = make([]string, 0, len(files))
	blocked = make([]string, 0)

	for _, file := range files {
		if isSensitiveFile(file) {
			blocked = append(blocked, file)
			continue
		}
		safe = append(safe, file)
	}
	return safe, blocked
}

// isSensitiveFile 双重防护检测单个文件是否敏感。
func isSensitiveFile(file string) bool {
	// 提取文件名和路径用于匹配
	base := filepath.Base(file)
	cleanPath := filepath.ToSlash(filepath.Clean(file))

	// 第一重：glob 模式匹配
	for _, pattern := range DefaultSensitivePatterns {
		if matchSensitivePattern(base, pattern) || matchSensitivePattern(cleanPath, pattern) {
			return true
		}
	}

	// 第二重：密钥特征正则兜底（如文件名本身含 sk-/ghp_ 前缀）
	if IsSecretLeak(base) {
		return true
	}

	return false
}

// matchSensitivePattern 用 filepath.Match 匹配，对路径模式做子路径匹配增强。
func matchSensitivePattern(name, pattern string) bool {
	// 精确匹配
	matched, err := filepath.Match(pattern, name)
	if err == nil && matched {
		return true
	}
	// 路径模式（含 /）尝试子路径匹配
	// 对 "project/.aws/credentials" 和 pattern ".aws/*"，
	// 需要尝试 ".aws/credentials" 子路径才能命中
	if strings.Contains(pattern, "/") {
		s := name
		for {
			if matched, err := filepath.Match(pattern, s); err == nil && matched {
				return true
			}
			idx := strings.Index(s, "/")
			if idx < 0 {
				break
			}
			s = s[idx+1:]
		}
	}
	// 通配模式尝试匹配路径各段
	parts := strings.Split(name, "/")
	for _, part := range parts {
		matched, err := filepath.Match(pattern, part)
		if err == nil && matched {
			return true
		}
	}
	return false
}
