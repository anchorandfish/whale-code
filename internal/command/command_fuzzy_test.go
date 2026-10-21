// command_fuzzy_test.go — M11-04 sahilm/fuzzy 模糊匹配测试（收尾补全）。
//
// 覆盖 5 个测试：精确匹配 / 子串匹配 / 缩写匹配 / Levenshtein 降级 / 无匹配。
//
// 对标 fzf/fzy 智能模糊匹配 @https://github.com/junegunn/fzf @2026-07-21；
// 降级路径保留 levenshtein <= 2（向后兼容）。
package command

import (
	"testing"
)

// newTestFuzzyRegistry 构造仅含指定命令名的最小 Registry，用于 Suggest 测试。
// newTestFuzzyRegistry 跳过 registerBuiltinCommands，确保测试结果可重现。
func newTestFuzzyRegistry(names ...string) *Registry {
	r := &Registry{commands: make(map[string]Command)}
	for _, name := range names {
		r.commands[name] = Command{Name: name}
	}
	return r
}

// containsString checks if a string is in a slice（复用 command_test.go 既有 helper 风格）。
func containsStringFuzzy(s string, list []string) bool {
	for _, item := range list {
		if item == s {
			return true
		}
	}
	return false
}

// TestCommand_Fuzzy_Match_Exact 验证精确匹配优先：输入 "help" 应在结果中且排首位。
//
// sahilm/fuzzy 对精确匹配给出最高 Score，结果按 Score 降序，"help" 应为第一个。
func TestCommand_Fuzzy_Match_Exact(t *testing.T) {
	r := newTestFuzzyRegistry("help", "model", "compact")

	suggestions := Suggest("help", r)
	if len(suggestions) == 0 {
		t.Fatal("Suggest(help) returned empty, want at least 1 match")
	}
	if suggestions[0] != "help" {
		t.Errorf("Suggest(help)[0] = %q, want \"help\" (exact match highest score)", suggestions[0])
	}
	if !containsStringFuzzy("help", suggestions) {
		t.Errorf("Suggest(help) = %v, want contain \"help\"", suggestions)
	}
}

// TestCommand_Fuzzy_Match_Substring 验证子串匹配：输入 "export" 应匹配所有 export* 命令。
//
// sahilm/fuzzy 将 "export" 作为子序列匹配 "export"/"export-user"/"export-format"，
// 三个命令均应出现在结果中（不依赖 strings.Contains 降级路径）。
func TestCommand_Fuzzy_Match_Substring(t *testing.T) {
	r := newTestFuzzyRegistry("export", "export-user", "export-format", "help")

	suggestions := Suggest("export", r)
	if len(suggestions) < 3 {
		t.Errorf("Suggest(export) = %v, want at least 3 matches (export/export-user/export-format)", suggestions)
	}
	for _, want := range []string{"export", "export-user", "export-format"} {
		if !containsStringFuzzy(want, suggestions) {
			t.Errorf("Suggest(export) = %v, want contain %q", suggestions, want)
		}
	}
}

// TestCommand_Fuzzy_Match_Abbreviation 验证缩写匹配：输入 "cfg" 应匹配 "config"。
//
// sahilm/fuzzy 子序列匹配：c-f-g 是 c-o-n-f-i-g 的子序列（位置 0/3/5），
// 非 prefix/substring，levenshtein 距离 3 也会被旧逻辑过滤，仅 fuzzy 能匹配。
// 这是 M11-04 升级的核心价值点（输入 "md" 匹配 "model" 类似场景）。
func TestCommand_Fuzzy_Match_Abbreviation(t *testing.T) {
	r := newTestFuzzyRegistry("config", "help", "model")

	suggestions := Suggest("cfg", r)
	if !containsStringFuzzy("config", suggestions) {
		t.Errorf("Suggest(cfg) = %v, want contain \"config\" (abbreviation match via fuzzy subsequence)", suggestions)
	}
}

// TestCommand_Fuzzy_Match_Levenshtein 验证 Levenshtein <= 2 降级匹配。
//
// 输入 "xnfo"：sahilm/fuzzy 因 x 不在 "info" 中无法子序列匹配 → 0 结果；
// 降级到 levenshtein("info", "xnfo") = 1（替换 i→x）≤ 2 → 匹配 "info"。
// 此测试验证降级路径保留向后兼容（M11-04 关键约束）。
func TestCommand_Fuzzy_Match_Levenshtein(t *testing.T) {
	r := newTestFuzzyRegistry("info", "help", "model")

	suggestions := Suggest("xnfo", r)
	if !containsStringFuzzy("info", suggestions) {
		t.Errorf("Suggest(xnfo) = %v, want contain \"info\" (levenshtein fallback, distance=1)", suggestions)
	}
}

// TestCommand_Fuzzy_NoMatch 验证无匹配时返回空切片。
//
// 输入 "xyz123" 与所有命令名 fuzzy 不匹配且 levenshtein 距离 > 2，
// 应返回空切片（len == 0）。
func TestCommand_Fuzzy_NoMatch(t *testing.T) {
	r := newTestFuzzyRegistry("help", "model", "compact")

	suggestions := Suggest("xyz123", r)
	if len(suggestions) != 0 {
		t.Errorf("Suggest(xyz123) = %v, want empty (no match)", suggestions)
	}
}
