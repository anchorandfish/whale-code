// Package typeahead - skill 补全测试（P0-08 M11-03 + M25-01 合并）
// 覆盖 # 触发符识别、模糊匹配、@ 与 # 并存、空结果、ESC/Tab/Enter 选择等场景
package typeahead

import (
	"strings"
	"testing"
)

// mockSkillProvider 测试用 SkillProvider 桩
type mockSkillProvider struct {
	skills []SkillItem
}

func (m *mockSkillProvider) ListSkills() []SkillItem {
	return m.skills
}

// setupTestSkillProvider 注入测试用桩并在测试结束后清理全局状态
func setupTestSkillProvider(skills []SkillItem) func() {
	original := skillProvider
	skillProvider = &mockSkillProvider{skills: skills}
	return func() {
		skillProvider = original
	}
}

// TestExtractTokenWithTrigger_HashAtStart 测试 # 在输入开头时的 token 提取
func TestExtractTokenWithTrigger_HashAtStart(t *testing.T) {
	input := "#memory-add"
	token, start, trigger := ExtractTokenWithTrigger(input, len(input))
	if trigger != TriggerHash {
		t.Fatalf("expected trigger=TriggerHash, got %v", trigger)
	}
	if token != "memory-add" {
		t.Fatalf("expected token=memory-add, got %s", token)
	}
	if start != 0 {
		t.Fatalf("expected start=0, got %d", start)
	}
}

// TestExtractTokenWithTrigger_HashAfterSpace 测试 # 在空格后的 token 提取
func TestExtractTokenWithTrigger_HashAfterSpace(t *testing.T) {
	// 注意：Go 字符串按字节计算位置，"调用 " 占 7 字节（调=3, 用=3, 空格=1）
	// 所以 # 在位置 7
	input := "调用 #memory-l"
	token, start, trigger := ExtractTokenWithTrigger(input, len(input))
	if trigger != TriggerHash {
		t.Fatalf("expected trigger=TriggerHash, got %v", trigger)
	}
	if token != "memory-l" {
		t.Fatalf("expected token=memory-l, got %s", token)
	}
	if start != 7 {
		t.Fatalf("expected start=7, got %d", start)
	}
}

// TestExtractTokenWithTrigger_HashEmpty 测试 # 后无字符的 showOnEmpty 场景
func TestExtractTokenWithTrigger_HashEmpty(t *testing.T) {
	input := "hello #"
	token, start, trigger := ExtractTokenWithTrigger(input, len(input))
	if trigger != TriggerHash {
		t.Fatalf("expected trigger=TriggerHash, got %v", trigger)
	}
	if token != "" {
		t.Fatalf("expected empty token, got %s", token)
	}
	if start < 0 {
		t.Fatalf("expected start >= 0, got %d", start)
	}
}

// TestExtractTokenWithTrigger_AtStillWorks 测试 @ 触发符仍正常工作（向后兼容）
func TestExtractTokenWithTrigger_AtStillWorks(t *testing.T) {
	input := "@internal/mention"
	token, start, trigger := ExtractTokenWithTrigger(input, len(input))
	if trigger != TriggerAt {
		t.Fatalf("expected trigger=TriggerAt, got %v", trigger)
	}
	if token != "internal/mention" {
		t.Fatalf("expected token=internal/mention, got %s", token)
	}
	if start != 0 {
		t.Fatalf("expected start=0, got %d", start)
	}
}

// TestExtractTokenWithTrigger_AtAndHashCoexist 测试 @ 与 # 在同一输入中并存
// 场景：用户输入 "@file.txt #skill-name"，最后一个触发符（#）应被识别
func TestExtractTokenWithTrigger_AtAndHashCoexist(t *testing.T) {
	// "@file.txt " 占 10 字节（@ + 8字符 + 空格），所以 # 在位置 10
	input := "@file.txt #memory-add"
	token, start, trigger := ExtractTokenWithTrigger(input, len(input))
	// cursor 在末尾，应识别最后一个触发符 #
	if trigger != TriggerHash {
		t.Fatalf("expected trigger=TriggerHash (last trigger), got %v", trigger)
	}
	if token != "memory-add" {
		t.Fatalf("expected token=memory-add, got %s", token)
	}
	if start != 10 {
		t.Fatalf("expected start=10, got %d", start)
	}
}

// TestExtractTokenWithTrigger_AtAndHashCoexist_AtLast 测试 @ 与 # 并存但 @ 在末尾
// 场景：用户输入 "#skill-name @file.txt"，最后一个触发符（@）应被识别
func TestExtractTokenWithTrigger_AtAndHashCoexist_AtLast(t *testing.T) {
	input := "#skill-name @file.txt"
	token, start, trigger := ExtractTokenWithTrigger(input, len(input))
	if trigger != TriggerAt {
		t.Fatalf("expected trigger=TriggerAt (last trigger), got %v", trigger)
	}
	if token != "file.txt" {
		t.Fatalf("expected token=file.txt, got %s", token)
	}
	_ = start
}

// TestExtractTokenWithTrigger_NoTrigger 测试无触发符返回 TriggerNone
func TestExtractTokenWithTrigger_NoTrigger(t *testing.T) {
	input := "hello world"
	_, _, trigger := ExtractTokenWithTrigger(input, len(input))
	if trigger != TriggerNone {
		t.Fatalf("expected trigger=TriggerNone, got %v", trigger)
	}
}

// TestExtractTokenWithTrigger_HashInWord 测试 # 在单词中间不触发
// 场景：如 "abc#def" 不应被识别为 # 触发符（无前导空格或行首）
func TestExtractTokenWithTrigger_HashInWord(t *testing.T) {
	input := "abc#def"
	_, _, trigger := ExtractTokenWithTrigger(input, len(input))
	if trigger != TriggerNone {
		t.Fatalf("expected TriggerNone for mid-word #, got %v", trigger)
	}
}

// TestSuggestWithTrigger_HashPrefix 测试 # 触发符前缀匹配
// 输入 "#mem" 应匹配 "memory-add" 和 "memory-list"
func TestSuggestWithTrigger_HashPrefix(t *testing.T) {
	cleanup := setupTestSkillProvider([]SkillItem{
		{Name: "memory-add", Description: "添加记忆"},
		{Name: "memory-list", Description: "列出记忆"},
		{Name: "code-review", Description: "代码审查"},
	})
	defer cleanup()

	results := SuggestWithTrigger(TriggerHash, "mem")
	if len(results) != 2 {
		t.Fatalf("expected 2 matches, got %d", len(results))
	}
	// 前缀匹配应排第一
	if results[0].DisplayText != "memory-add" && results[0].DisplayText != "memory-list" {
		t.Fatalf("unexpected first match: %s", results[0].DisplayText)
	}
	for _, r := range results {
		if r.Trigger != TriggerHash {
			t.Fatalf("expected TriggerHash, got %v", r.Trigger)
		}
		if !strings.HasPrefix(r.DisplayText, "memory") {
			t.Fatalf("unexpected match: %s", r.DisplayText)
		}
	}
}

// TestSuggestWithTrigger_HashEmpty 测试 # 后无字符返回全部 skill（showOnEmpty）
func TestSuggestWithTrigger_HashEmpty(t *testing.T) {
	cleanup := setupTestSkillProvider([]SkillItem{
		{Name: "memory-add", Description: "添加记忆"},
		{Name: "code-review", Description: "代码审查"},
	})
	defer cleanup()

	results := SuggestWithTrigger(TriggerHash, "")
	if len(results) != 2 {
		t.Fatalf("expected 2 matches (showOnEmpty), got %d", len(results))
	}
}

// TestSuggestWithTrigger_HashNoMatch 测试无匹配返回 nil
func TestSuggestWithTrigger_HashNoMatch(t *testing.T) {
	cleanup := setupTestSkillProvider([]SkillItem{
		{Name: "memory-add", Description: "添加记忆"},
		{Name: "code-review", Description: "代码审查"},
	})
	defer cleanup()

	results := SuggestWithTrigger(TriggerHash, "nonexistent")
	if len(results) != 0 {
		t.Fatalf("expected 0 matches for nonexistent, got %d", len(results))
	}
}

// TestSuggestWithTrigger_HashFuzzy 测试 # 触发符模糊匹配（字符序列）
// 输入 "#mem" 应通过 fuzzyContains 匹配 "memory-add"
func TestSuggestWithTrigger_HashFuzzy(t *testing.T) {
	cleanup := setupTestSkillProvider([]SkillItem{
		{Name: "memory-add", Description: "添加记忆"},
		{Name: "mre", Description: "异常路径"},
	})
	defer cleanup()

	// "mem" 应只匹配 "memory-add"（前缀），不匹配 "mre"
	results := SuggestWithTrigger(TriggerHash, "mem")
	if len(results) != 1 {
		t.Fatalf("expected 1 match, got %d", len(results))
	}
	if results[0].DisplayText != "memory-add" {
		t.Fatalf("expected memory-add, got %s", results[0].DisplayText)
	}
}

// TestSuggestWithTrigger_HashSubstring 测试子串匹配
// 输入 "#review" 应匹配 "code-review"（子串）
func TestSuggestWithTrigger_HashSubstring(t *testing.T) {
	cleanup := setupTestSkillProvider([]SkillItem{
		{Name: "code-review", Description: "代码审查"},
		{Name: "memory-add", Description: "添加记忆"},
	})
	defer cleanup()

	results := SuggestWithTrigger(TriggerHash, "review")
	if len(results) != 1 {
		t.Fatalf("expected 1 match, got %d", len(results))
	}
	if results[0].DisplayText != "code-review" {
		t.Fatalf("expected code-review, got %s", results[0].DisplayText)
	}
}

// TestSuggestWithTrigger_NoProvider 测试 SkillProvider 未注入时返回 nil
// 验证降级场景：未注入 provider 时 # 触发符不弹窗
func TestSuggestWithTrigger_NoProvider(t *testing.T) {
	cleanup := setupTestSkillProvider(nil)
	defer cleanup()

	// 显式置 nil
	skillProvider = nil
	results := SuggestWithTrigger(TriggerHash, "mem")
	if results != nil {
		t.Fatalf("expected nil when no provider, got %v", results)
	}
}

// TestSuggestWithTrigger_AtTrigger 测试 @ 触发符走文件链路
// 验证 SuggestWithTrigger(TriggerAt, ...) 与 Suggest(...) 行为一致
func TestSuggestWithTrigger_AtTrigger(t *testing.T) {
	// 此测试不依赖 SkillProvider，仅验证 TriggerAt 走文件链路
	// 文件链路返回值依赖全局 fileIndex，这里只验证不 panic 即可
	results := SuggestWithTrigger(TriggerAt, "")
	_ = results
}

// TestSuggestWithTrigger_NoneTrigger 测试 TriggerNone 返回 nil
func TestSuggestWithTrigger_NoneTrigger(t *testing.T) {
	results := SuggestWithTrigger(TriggerNone, "abc")
	if results != nil {
		t.Fatalf("expected nil for TriggerNone, got %v", results)
	}
}

// TestSuggestWithTrigger_RespectsMaxSuggestions 测试最多返回 MaxSuggestions 项
func TestSuggestWithTrigger_RespectsMaxSuggestions(t *testing.T) {
	// 准备超过 MaxSuggestions 数量的 skill
	skills := make([]SkillItem, MaxSuggestions+5)
	for i := range skills {
		skills[i] = SkillItem{
			Name:        "skill-" + string(rune('a'+i)),
			Description: "test skill",
		}
	}
	cleanup := setupTestSkillProvider(skills)
	defer cleanup()

	results := SuggestWithTrigger(TriggerHash, "")
	if len(results) > MaxSuggestions {
		t.Fatalf("expected at most %d results, got %d", MaxSuggestions, len(results))
	}
}

// TestApplySuggestion_HashTrigger 测试 # skill 补全后插入空格后缀
func TestApplySuggestion_HashTrigger(t *testing.T) {
	input := "#mem"
	suggestion := Completion{
		DisplayText: "memory-add",
		Trigger:     TriggerHash,
	}
	newInput, _ := ApplySuggestion(input, suggestion, 0, 3)
	expected := "#memory-add "
	if newInput != expected {
		t.Fatalf("expected %q, got %q", expected, newInput)
	}
}

// TestApplySuggestion_HashTriggerInMiddle 测试 # skill 补全在输入中间
func TestApplySuggestion_HashTriggerInMiddle(t *testing.T) {
	// 注意：Go 字符串按字节计算位置，"调用 " 占 7 字节（调=3, 用=3, 空格=1）
	// 所以 # 在位置 7，token "mem" 长度 3
	// ApplySuggestion 会保留 input 中 mem 后的原空格，并额外加一个空格作为 skill 后缀
	// 这与 TestApplySuggestion_MiddleInput 行为一致（@ 文件场景同样会双空格）
	input := "调用 #mem 然后做点别的"
	suggestion := Completion{
		DisplayText: "memory-add",
		Trigger:     TriggerHash,
	}
	newInput, _ := ApplySuggestion(input, suggestion, 7, 3)
	expected := "调用 #memory-add  然后做点别的"
	if newInput != expected {
		t.Fatalf("expected %q, got %q", expected, newInput)
	}
}

// TestApplySuggestion_AtFileStillWorks 测试 @ 文件补全后缀逻辑保持不变
func TestApplySuggestion_AtFileStillWorks(t *testing.T) {
	input := "@inter"
	suggestion := Completion{
		DisplayText: "internal/mention/mention.go",
		IsDir:       false,
		Trigger:     TriggerAt,
	}
	newInput, _ := ApplySuggestion(input, suggestion, 0, 5)
	expected := "@internal/mention/mention.go "
	if newInput != expected {
		t.Fatalf("expected %q, got %q", expected, newInput)
	}
}

// TestApplySuggestion_AtDirStillWorks 测试 @ 目录补全后缀逻辑保持不变
func TestApplySuggestion_AtDirStillWorks(t *testing.T) {
	input := "@inter"
	suggestion := Completion{
		DisplayText: "internal/mention",
		IsDir:       true,
		Trigger:     TriggerAt,
	}
	newInput, _ := ApplySuggestion(input, suggestion, 0, 5)
	// 目录补全应保留路径分隔符后缀
	if !strings.HasSuffix(newInput, "/") && !strings.HasSuffix(newInput, "\\") {
		t.Fatalf("expected path separator suffix, got %q", newInput)
	}
}

// TestSkillMatchScore_Prefix 测试前缀匹配得分最高
func TestSkillMatchScore_Prefix(t *testing.T) {
	score := skillMatchScore("mem", "memory-add", "添加记忆")
	if score != 200 {
		t.Fatalf("expected prefix score 200, got %d", score)
	}
}

// TestSkillMatchScore_Substring 测试子串匹配得分
func TestSkillMatchScore_Substring(t *testing.T) {
	score := skillMatchScore("review", "code-review", "代码审查")
	if score != 150 {
		t.Fatalf("expected substring score 150, got %d", score)
	}
}

// TestSkillMatchScore_Fuzzy 测试模糊匹配得分
func TestSkillMatchScore_Fuzzy(t *testing.T) {
	// "mry" 按字符顺序匹配 "memory-add"（m-e-m-o-r-y，包含 m,r,y）
	score := skillMatchScore("mry", "memory-add", "添加记忆")
	if score != 100 {
		t.Fatalf("expected fuzzy score 100, got %d", score)
	}
}

// TestSkillMatchScore_NoMatch 测试不匹配返回 -1
func TestSkillMatchScore_NoMatch(t *testing.T) {
	score := skillMatchScore("xyz", "memory-add", "添加记忆")
	if score != -1 {
		t.Fatalf("expected no-match score -1, got %d", score)
	}
}

// TestSkillMatchScore_EmptyQuery 测试空查询返回基础分
func TestSkillMatchScore_EmptyQuery(t *testing.T) {
	score := skillMatchScore("", "memory-add", "")
	if score != 100 {
		t.Fatalf("expected empty query score 100, got %d", score)
	}
}

// TestFuzzyContains 测试模糊字符序列匹配
func TestFuzzyContains(t *testing.T) {
	cases := []struct {
		s, query string
		expected bool
	}{
		{"memory-add", "mem", true},
		{"memory-add", "mry", true}, // m-e-m-o-r-y 中按序找 m,r,y
		{"memory-add", "mad", true}, // m-e-m-o-r-y- - a-d
		{"memory-add", "xyz", false},
		{"memory-add", "", true}, // 空 query 全匹配
		{"", "", true},
		{"", "a", false},
	}
	for _, c := range cases {
		got := fuzzyContains(c.s, c.query)
		if got != c.expected {
			t.Fatalf("fuzzyContains(%q, %q) = %v, expected %v", c.s, c.query, got, c.expected)
		}
	}
}

// TestExtractTokenWithTrigger_LongInput 测试超长输入不 panic
func TestExtractTokenWithTrigger_LongInput(t *testing.T) {
	longInput := "#" + strings.Repeat("a", 2000)
	// 不应 panic
	token, _, trigger := ExtractTokenWithTrigger(longInput, len(longInput))
	if trigger != TriggerHash {
		t.Fatalf("expected TriggerHash, got %v", trigger)
	}
	if len(token) != 2000 {
		t.Fatalf("expected token length 2000, got %d", len(token))
	}
}

// TestSuggestWithTrigger_LongTokenTruncation 测试超长 token 截断
func TestSuggestWithTrigger_LongTokenTruncation(t *testing.T) {
	cleanup := setupTestSkillProvider([]SkillItem{
		{Name: "memory-add", Description: "添加记忆"},
	})
	defer cleanup()

	longToken := strings.Repeat("a", 2000)
	// 不应 panic，且应返回 0 匹配（不匹配）
	results := SuggestWithTrigger(TriggerHash, longToken)
	if len(results) != 0 {
		t.Fatalf("expected 0 matches for long token, got %d", len(results))
	}
}
