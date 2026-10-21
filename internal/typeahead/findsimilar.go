package typeahead

import (
	"os"
	"path/filepath"
	"strings"
)

// FindSimilarFile 提交后兜底建议
// 对标同类 CLI
// 先试同名不同扩展名（foo.ts → foo.tsx），无果再 levenshtein ≤2（fop.ts → foo.ts）
// 返回建议文件名（仅文件名，非完整路径），无建议返回 ""
func FindSimilarFile(filePath, workDir string) string {
	dir := filepath.Dir(filePath)
	if dir == "" || dir == "." {
		dir = workDir
	}
	if !filepath.IsAbs(dir) {
		dir = filepath.Join(workDir, dir)
	}

	base := filepath.Base(filePath)
	name := strings.TrimSuffix(base, filepath.Ext(base))

	entries, err := os.ReadDir(dir)
	if err != nil {
		return ""
	}

	// 1. 同名不同扩展名（对标 utils/file.ts:178-207）
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		entryName := entry.Name()
		entryBase := strings.TrimSuffix(entryName, filepath.Ext(entryName))
		if entryBase == name && entryName != base {
			return entryName
		}
	}

	// 2. levenshtein ≤2 兜底（对标 utils/file.ts:201-204）
	bestMatch := ""
	bestDist := 3 // >2 的不返回
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		entryName := entry.Name()
		dist := levenshtein(base, entryName)
		if dist < bestDist {
			bestDist = dist
			bestMatch = entryName
		}
	}

	return bestMatch
}

// levenshtein 计算两个字符串的编辑距离（独立实现，不依赖 command 包，避免循环依赖）
// 对标 internal/command/command.go:219-231 的 levenshtein 算法
func levenshtein(a, b string) int {
	la, lb := len(a), len(b)
	if la == 0 {
		return lb
	}
	if lb == 0 {
		return la
	}

	// 滚动数组优化空间
	prev := make([]int, lb+1)
	curr := make([]int, lb+1)
	for j := 0; j <= lb; j++ {
		prev[j] = j
	}

	for i := 1; i <= la; i++ {
		curr[0] = i
		for j := 1; j <= lb; j++ {
			cost := 1
			if a[i-1] == b[j-1] {
				cost = 0
			}
			curr[j] = min3(prev[j]+1, curr[j-1]+1, prev[j-1]+cost)
		}
		prev, curr = curr, prev
	}
	return prev[lb]
}

// min3 返回三个整数中的最小值
func min3(a, b, c int) int {
	if b < a {
		a = b
	}
	if c < a {
		a = c
	}
	return a
}
