package typeahead

// fuzzy.go 反例测试（P1 修复验证）
//
// 验证 matchWithBonus 的核心修复：先对所有匹配计算加分并排序，最后截取 max 条。
// 旧代码（break 截断后排序）会丢失加分后排名上升的项；
// 新代码（全量加分→排序→截取）确保最高总分项不被误丢弃。

import (
	"testing"
)

// TestMatchWithBonus_SortBeforeTruncate 反例测试（P1 修复核心验证）。
// 验证加分后排名上升的项不被提前截断丢弃。
// 构造场景：query "a" 在多个等长路径中匹配同一位置，
// 含路径分隔符的路径获得 +2 bonus，应排在无 bonus 的路径之前。
// 旧代码（break 截断 max=1 后排序）会返回首个 fuzzy.Find 结果（无 bonus 项）；
// 新代码（全量加分→排序→截取）返回 bonus 项。
func TestMatchWithBonus_SortBeforeTruncate(t *testing.T) {
	// "xa" 和 "/a" 长度均为 2，query "a" 匹配位置均为 1
	// "xa": bonus 0（位置 1 前为 'x'，非分隔符）
	// "/a": bonus +2（位置 1 前为 '/'，路径分隔符）
	paths := []string{
		"xa", // 无 bonus
		"/a", // bonus +2（/ 后匹配）
		"ya", // 无 bonus
	}

	results := matchWithBonus("a", paths, 1)
	if len(results) != 1 {
		t.Fatalf("期望 1 条结果，得到 %d", len(results))
	}

	// /a 应排在最前（同原始分 + 路径段加分 2 > 0）
	if results[0].DisplayText != "/a" {
		t.Errorf("期望 /a 排首位（路径段加分后总分最高），得到 %s (score=%d)\n"+
			"  原因：旧代码 break 截断后排序，丢失加分后排名上升的项\n"+
			"  修复：先全量加分→排序→截取", results[0].DisplayText, results[0].Score)
	}
}

// TestMatchWithBonus_SortedByTotalScore 验证结果按总分（原始分+bonus）降序排列。
func TestMatchWithBonus_SortedByTotalScore(t *testing.T) {
	paths := []string{
		"src/main.go",
		"src/test.go",
		"main.go",
		"doc/main.md",
	}

	results := matchWithBonus("main", paths, 10)
	if len(results) == 0 {
		t.Fatal("期望至少 1 条结果")
	}

	// 验证降序
	for i := 1; i < len(results); i++ {
		if results[i-1].Score < results[i].Score {
			t.Errorf("结果未按 Score 降序排列：[%d]=%s(score=%d) < [%d]=%s(score=%d)",
				i-1, results[i-1].DisplayText, results[i-1].Score,
				i, results[i].DisplayText, results[i].Score)
		}
	}
}

// TestMatchWithBonus_RespectsMax 验证返回数量不超过 max。
func TestMatchWithBonus_RespectsMax(t *testing.T) {
	paths := []string{"abc", "bcd", "cde", "def", "efg"}
	results := matchWithBonus("", paths, 3)
	if len(results) > 3 {
		t.Errorf("返回 %d 条，超过 max=3", len(results))
	}
}

// TestMatchWithBonus_HighBonusNotTruncated 反例测试（P1 修复辅助验证）。
// 验证高 bonus 路径在小 max 下不被截断。
// 构造场景：多个路径匹配 query，其中含多个分隔符的路径获得更高 bonus。
func TestMatchWithBonus_HighBonusNotTruncated(t *testing.T) {
	// query "a" 匹配所有路径
	// "/a/b" 匹配位置 1 和 3，bonus = 2（/ 后）+ 2（/ 后）= 4
	// "xa" 匹配位置 1，bonus = 0
	// max=2 时，/a/b 应在结果中（总分最高）
	paths := []string{
		"xa",
		"ya",
		"/a/b",
	}

	results := matchWithBonus("a", paths, 2)
	if len(results) != 2 {
		t.Fatalf("期望 2 条结果，得到 %d", len(results))
	}

	// /a/b 应在结果中（多个路径段加分使总分最高）
	found := false
	for _, r := range results {
		if r.DisplayText == "/a/b" {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("期望 /a/b 在结果中（多路径段加分使总分最高），实际结果: %s, %s\n"+
			"  原因：旧代码 break 截断可能丢弃高 bonus 项",
			results[0].DisplayText, results[1].DisplayText)
	}
}

// TestMatchWithBonus_EmptyPaths 验证空路径列表返回 nil。
func TestMatchWithBonus_EmptyPaths(t *testing.T) {
	results := matchWithBonus("test", nil, 10)
	if results != nil {
		t.Errorf("空路径列表应返回 nil，得到 %d 条结果", len(results))
	}
}

// TestMatchWithBonus_NoMatch 验证无匹配返回 nil。
func TestMatchWithBonus_NoMatch(t *testing.T) {
	paths := []string{"hello", "world"}
	results := matchWithBonus("xyz", paths, 10)
	if results != nil {
		t.Errorf("无匹配应返回 nil，得到 %d 条结果", len(results))
	}
}
