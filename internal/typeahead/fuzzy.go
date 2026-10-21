// Package typeahead 提供 @ 文件路径输入补全（typeahead），对标同类 CLI useTypeahead.tsx + fileSuggestions.ts。
package typeahead

import (
	"strings"

	"github.com/sahilm/fuzzy"
)

// matchWithBonus 对标 fileSuggestions.ts:617 findMatchingFiles
// 调用 sahilm/fuzzy 进行模糊匹配，并对路径段边界（分隔符后首个字符）加分，提升目录/文件名首字符命中率
// query: 用户输入的 token（不含 @）；paths: 文件索引中的路径列表；max: 最多返回数量
// 返回按 Score 降序排列的 []Completion
//
// 修复 P1（break 截断算法错误）：
// 原实现在循环中 break 截取 max 条后再 sortByScoreDesc，导致加分后本该排在前面的项
// 被提前丢弃（fuzzy.Find 按原始 Score 降序，加分后顺序可能变化）。
// 正确做法：先对所有匹配计算加分，再排序，最后截取 max 条。
func matchWithBonus(query string, paths []string, max int) []Completion {
	if len(paths) == 0 {
		return nil
	}
	// sahilm/fuzzy 对空 query 返回零分全匹配，用于 showOnEmpty 场景
	matches := fuzzy.Find(query, paths)
	if len(matches) == 0 {
		return nil
	}

	// 1. 对所有匹配计算路径段加分（不提前截断，避免丢失高 bonus 项）
	results := make([]Completion, 0, len(matches))
	for _, m := range matches {
		bonus := pathSegmentBonus(m.Str, m.MatchedIndexes)
		results = append(results, Completion{
			DisplayText: m.Str,
			Score:       m.Score + bonus,
			IsDir:       isDirPath(m.Str),
		})
	}

	// 2. 按 Score 降序排列（路径段加分后需重排）
	sortByScoreDesc(results)

	// 3. 截取 max 条（排序后截取，确保最高分项不被误丢弃）
	if len(results) > max {
		results = results[:max]
	}
	return results
}

// pathSegmentBonus 对路径段边界字符的匹配给予加分
// 对标同类 CLI fileSuggestions.ts 的 path-segment 优先策略：
// 若匹配的字符恰好在路径分隔符（/ \ . _ -）之后，给予额外加分
func pathSegmentBonus(path string, matchedIndexes []int) int {
	bonus := 0
	for _, idx := range matchedIndexes {
		if idx == 0 {
			bonus += 2 // 首字符加分
			continue
		}
		prev := path[idx-1]
		if prev == '/' || prev == '\\' || prev == '.' || prev == '_' || prev == '-' {
			bonus += 2 // 路径段边界字符加分
		}
	}
	return bonus
}

// isDirPath 判断路径是否以路径分隔符结尾（目录标记）
func isDirPath(path string) bool {
	if path == "" {
		return false
	}
	last := path[len(path)-1]
	return last == '/' || last == '\\'
}

// sortByScoreDesc 按 Score 降序排列（简单插入排序，max ≤ 15）
func sortByScoreDesc(items []Completion) {
	for i := 1; i < len(items); i++ {
		for j := i; j > 0 && items[j].Score > items[j-1].Score; j-- {
			items[j], items[j-1] = items[j-1], items[j]
		}
	}
}

// longestCommonPrefix 计算字符串切片的最长公共前缀
// 对标 fileSuggestions.ts:587-598
func longestCommonPrefix(strs []string) string {
	if len(strs) == 0 {
		return ""
	}
	prefix := strs[0]
	for _, s := range strs[1:] {
		// 统一路径分隔符后比较
		prefixNorm := strings.ReplaceAll(prefix, "\\", "/")
		sNorm := strings.ReplaceAll(s, "\\", "/")
		j := 0
		for j < len(prefixNorm) && j < len(sNorm) && prefixNorm[j] == sNorm[j] {
			j++
		}
		prefix = prefixNorm[:j]
		if prefix == "" {
			break
		}
	}
	return prefix
}
