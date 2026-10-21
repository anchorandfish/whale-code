/*
Package fuzzy provides fuzzy string matching optimized
for filenames and code symbols in the style of Sublime Text,
VSCode, IntelliJ IDEA et al.

本地 replace 副本：因本机 Winsock 损坏无法 go get，源码取自
https://github.com/sahilm/fuzzy (master 分支)，通过 go.mod replace 指令引用。
*/
package fuzzy

import (
	"iter"
	"sort"
	"strings"
	"unicode"
	"unicode/utf8"
)

// Match 表示一个匹配结果。
type Match struct {
	// Str 被匹配的字符串。
	Str string
	// Index 被匹配字符串在原始切片中的索引。
	Index int
	// MatchedIndexes 匹配字符的索引位置，用于高亮显示。
	MatchedIndexes []int
	// Score 用于排序的匹配评分。
	Score int
}

const (
	firstCharMatchBonus            = 10
	matchFollowingSeparatorBonus   = 20
	camelCaseMatchBonus            = 20
	adjacentMatchBonus             = 5
	unmatchedLeadingCharPenalty    = -5
	maxUnmatchedLeadingCharPenalty = -15
)

var separators = []rune("/-_ .\\")

// Matches 是 Match 切片，按 Score 降序排列。
type Matches []Match

func (a Matches) Len() int           { return len(a) }
func (a Matches) Swap(i, j int)      { a[i], a[j] = a[j], a[i] }
func (a Matches) Less(i, j int) bool { return a[i].Score > a[j].Score }

// Source 表示字符串列表的抽象数据源。
type Source interface {
	// String 返回位置 i 处的待匹配字符串。
	String(i int) string
	// Len 返回数据源长度。
	Len() int
}

type stringSource []string

func (ss stringSource) String(i int) string {
	return ss[i]
}

func (ss stringSource) Len() int { return len(ss) }

func iterFromSource(s Source) iter.Seq[string] {
	return func(yield func(string) bool) {
		for i := 0; i < s.Len(); i++ {
			if !yield(s.String(i)) {
				return
			}
		}
	}
}

/*
Find 在 data 中查找 pattern，返回按匹配质量降序排列的结果。
匹配质量由一组加分和惩罚规则决定：
* 模式首字符匹配字符串首字符
* 匹配字符为驼峰大小写
* 匹配字符跟在分隔符（如下划线）之后
* 匹配字符与前一个匹配相邻
对搜索字符串中未匹配的字符和首个匹配前的所有字符施加惩罚。
*/
func Find(pattern string, data []string) Matches {
	return FindFrom(pattern, stringSource(data))
}

// FindNoSort 是 Find 的不排序版本。
func FindNoSort(pattern string, data []string) Matches {
	return FindFromNoSort(pattern, stringSource(data))
}

// FindFrom 使用 Source 接口替代字符串切片。
func FindFrom(pattern string, data Source) Matches {
	matches := FindFromNoSort(pattern, data)
	sort.Stable(matches)
	return matches
}

// FindFromNoSort 是 FindFrom 的不排序版本。
func FindFromNoSort(pattern string, data Source) Matches {
	return FindFromIterNoSort(pattern, iterFromSource(data))
}

// FindFromIter 使用迭代器替代 Source 接口。
func FindFromIter(pattern string, it iter.Seq[string]) Matches {
	matches := FindFromIterNoSort(pattern, it)
	sort.Stable(matches)
	return matches
}

// FindFromIterNoSort 是 FindFromIter 的不排序版本。
func FindFromIterNoSort(pattern string, it iter.Seq[string]) Matches {
	if len(pattern) == 0 {
		return nil
	}
	runes := []rune(pattern)
	var matches Matches
	var matchedIndexes []int
	var i int
	for matchStr := range it {
		var match Match
		match.Str = matchStr
		cleanMatchStr := matchStr
		if nullI := strings.IndexRune(matchStr, 0); nullI > -1 {
			cleanMatchStr = cleanMatchStr[:nullI]
		}
		match.Index = i
		i++
		if matchedIndexes != nil {
			match.MatchedIndexes = matchedIndexes
		} else {
			match.MatchedIndexes = make([]int, 0, len(runes))
		}
		var score int
		patternIndex := 0
		bestScore := -1
		matchedIndex := -1
		currAdjacentMatchBonus := 0
		var last rune
		var lastIndex int
		nextc, nextSize := utf8.DecodeRuneInString(cleanMatchStr)
		var candidate rune
		var candidateSize int
		for j := 0; j < len(cleanMatchStr); j += candidateSize {
			candidate, candidateSize = nextc, nextSize
			if equalFold(candidate, runes[patternIndex]) {
				score = 0
				if j == 0 {
					score += firstCharMatchBonus
				}
				if unicode.IsLower(last) && unicode.IsUpper(candidate) {
					score += camelCaseMatchBonus
				}
				if j != 0 && isSeparator(last) {
					score += matchFollowingSeparatorBonus
				}
				if len(match.MatchedIndexes) > 0 {
					lastMatch := match.MatchedIndexes[len(match.MatchedIndexes)-1]
					bonus := adjacentCharBonus(lastIndex, lastMatch, currAdjacentMatchBonus)
					score += bonus
					currAdjacentMatchBonus += bonus
				}
				if score > bestScore {
					bestScore = score
					matchedIndex = j
				}
			}
			var nextp rune
			if patternIndex < len(runes)-1 {
				nextp = runes[patternIndex+1]
			}
			if j+candidateSize < len(cleanMatchStr) {
				if cleanMatchStr[j+candidateSize] < utf8.RuneSelf {
					nextc, nextSize = rune(cleanMatchStr[j+candidateSize]), 1
				} else {
					nextc, nextSize = utf8.DecodeRuneInString(cleanMatchStr[j+candidateSize:])
				}
			} else {
				nextc, nextSize = 0, 0
			}
			if equalFold(nextp, nextc) || nextc == 0 {
				if matchedIndex > -1 {
					if len(match.MatchedIndexes) == 0 {
						penalty := matchedIndex * unmatchedLeadingCharPenalty
						bestScore += max(penalty, maxUnmatchedLeadingCharPenalty)
					}
					match.Score += bestScore
					match.MatchedIndexes = append(match.MatchedIndexes, matchedIndex)
					score = 0
					bestScore = -1
					patternIndex++
				}
			}
			lastIndex = j
			last = candidate
		}
		penalty := len(match.MatchedIndexes) - len(cleanMatchStr)
		match.Score += penalty
		if len(match.MatchedIndexes) == len(runes) {
			matches = append(matches, match)
			matchedIndexes = nil
		} else {
			matchedIndexes = match.MatchedIndexes[:0]
		}
	}
	return matches
}

// equalFold 判断两个 rune 是否大小写不敏感相等。
func equalFold(tr, sr rune) bool {
	if tr == sr {
		return true
	}
	if tr < sr {
		tr, sr = sr, tr
	}
	if tr < utf8.RuneSelf {
		if 'A' <= sr && sr <= 'Z' && tr == sr+'a'-'A' {
			return true
		}
		return false
	}
	r := unicode.SimpleFold(sr)
	for r != sr && r < tr {
		r = unicode.SimpleFold(r)
	}
	return r == tr
}

func adjacentCharBonus(i int, lastMatch int, currentBonus int) int {
	if lastMatch == i {
		return currentBonus*2 + adjacentMatchBonus
	}
	return 0
}

func isSeparator(s rune) bool {
	for _, sep := range separators {
		if s == sep {
			return true
		}
	}
	return false
}

func max(x int, y int) int {
	if x > y {
		return x
	}
	return y
}
