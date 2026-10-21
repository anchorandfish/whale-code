// help_search_test.go — 帮助搜索与过滤测试（M09-04 改造，）
package command

import (
	"strings"
	"testing"
)

// TestFormatHelp_Search_Hit 测试 --search 命中
func TestFormatHelp_Search_Hit(t *testing.T) {
	r := NewRegistry()
	output := FormatHelp(r, "--search commit")

	// 应包含 /commit 命令（在 Git 分类下）
	if !strings.Contains(output, "commit") {
		t.Errorf("expected output to contain 'commit' for --search commit, got:\n%s", output)
	}
}

// TestFormatHelp_Search_Miss 测试 --search 无命中
func TestFormatHelp_Search_Miss(t *testing.T) {
	r := NewRegistry()
	output := FormatHelp(r, "--search zznonexistent")

	// 应包含无结果提示
	if !strings.Contains(output, "未找到匹配") {
		t.Errorf("expected '未找到匹配' for --search zznonexistent, got:\n%s", output)
	}
}

// TestFormatHelp_Search_CaseInsensitive 测试 --search 大小写不敏感（OPT-02）
func TestFormatHelp_Search_CaseInsensitive(t *testing.T) {
	r := NewRegistry()
	// 用大写 COMMIT 搜索，应命中小写 commit
	output := FormatHelp(r, "--search COMMIT")

	if !strings.Contains(strings.ToLower(output), "commit") {
		t.Errorf("expected output to contain 'commit' (case insensitive) for --search COMMIT, got:\n%s", output)
	}
}

// TestFormatHelp_Filter_Category 测试 --filter 分类过滤
func TestFormatHelp_Filter_Category(t *testing.T) {
	r := NewRegistry()
	// V0.1-HIDDEN：Git 分类命令已全部隐藏，改用可见的 Core 分类验证 --filter
	output := FormatHelp(r, "--filter Core")

	// 应包含 Core 分类标题
	if !strings.Contains(output, "━━━ Core ━━━") {
		t.Errorf("expected '━━━ Core ━━━' category for --filter Core, got:\n%s", output)
	}
	// 不应包含 Session 分类
	if strings.Contains(output, "━━━ Session ━━━") {
		t.Errorf("expected no Session category for --filter Core, got:\n%s", output)
	}
}

// TestFormatHelp_Filter_CaseInsensitive 测试 --filter 分类大小写不敏感
func TestFormatHelp_Filter_CaseInsensitive(t *testing.T) {
	r := NewRegistry()
	// 用小写 core 过滤，应匹配 Core 分类
	output := FormatHelp(r, "--filter core")

	if !strings.Contains(output, "━━━ Core ━━━") {
		t.Errorf("expected '━━━ Core ━━━' for --filter core (case insensitive), got:\n%s", output)
	}
}

// TestFormatHelp_Search_And_Filter 测试 --search + --filter 组合
func TestFormatHelp_Search_And_Filter(t *testing.T) {
	r := NewRegistry()
	output := FormatHelp(r, "--filter Git --search commit")

	// 应同时满足 Git 分类 + commit 关键词
	if !strings.Contains(output, "commit") {
		t.Errorf("expected output to contain 'commit' for --filter Git --search commit, got:\n%s", output)
	}
	if strings.Contains(output, "━━━ Core ━━━") {
		t.Errorf("expected no Core category for --filter Git, got:\n%s", output)
	}
}

// TestFormatHelp_NoArgs_Legacy 测试无参数兼容（向后兼容）
func TestFormatHelp_NoArgs_Legacy(t *testing.T) {
	r := NewRegistry()
	output := FormatHelp(r, "")

	// 无参数时应显示多个分类
	if !strings.Contains(output, "━━━ Core ━━━") {
		t.Errorf("expected '━━━ Core ━━━' category for no args, got:\n%s", output)
	}
	// 应包含键盘快捷键（中文化）
	if !strings.Contains(output, "快捷键") {
		t.Errorf("expected '快捷键' for no args, got:\n%s", output)
	}
}

// TestFormatHelp_All_ShowsStub 测试 --all 显示桩命令
func TestFormatHelp_All_ShowsStub(t *testing.T) {
	r := NewRegistry()
	// 注入一个桩命令用于测试
	r.commands["test-stub-cmd"] = Command{
		Name:        "test-stub-cmd",
		Description: "test stub command",
		Category:    "Other",
		Stub:        true,
	}

	// 无 --all 时不应显示桩命令
	outputNoAll := FormatHelp(r, "")
	if strings.Contains(outputNoAll, "test-stub-cmd") {
		t.Errorf("expected stub command hidden without --all, got:\n%s", outputNoAll)
	}

	// --all 时应显示桩命令并带 [未实现] 后缀
	outputAll := FormatHelp(r, "--all")
	if !strings.Contains(outputAll, "test-stub-cmd") {
		t.Errorf("expected stub command shown with --all, got:\n%s", outputAll)
	}
	// ★ C-6有意翻转：双语化 → [stub 未实现]，勿当回归
	if !strings.Contains(outputAll, "[stub 未实现]") {
		t.Errorf("expected [stub 未实现] suffix with --all, got:\n%s", outputAll)
	}
}

// TestParseFlag 测试 flag 解析
func TestParseFlag(t *testing.T) {
	tests := []struct {
		name     string
		args     string
		flag     string
		expected string
	}{
		{"simple", "--search commit", "--search", "commit"},
		{"filter", "--filter Git", "--filter", "Git"},
		{"combined", "--search commit --all", "--search", "commit"},
		{"no_value_followed_by_flag", "--search --all", "--search", ""},
		{"no_value_at_end", "--search", "--search", ""},
		{"not_present", "--all", "--search", ""},
		{"empty_args", "", "--search", ""},
		{"multi_word_value_first", "--filter Git --search commit", "--filter", "Git"},
		{"multi_word_value_second", "--filter Git --search commit", "--search", "commit"},
		// 5 维度审查修复：单横杠 flag 也应被识别为 flag token
		{"single_dash_flag_after", "--search -all", "--search", ""},
		{"single_dash_flag_value", "--filter -Git", "--filter", ""},
		// 子审查建议：边界 case 锁定
		{"stdin_placeholder_as_value", "--search -", "--search", "-"}, // 单独 "-" 是 stdin 占位符，可作值
		{"double_dash_only", "--search --", "--search", ""},           // "--" 视为 flag token
		{"negative_number_as_flag", "--search -5", "--search", ""},    // 负数视为 flag token
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := parseFlag(tt.args, tt.flag)
			if result != tt.expected {
				t.Errorf("parseFlag(%q, %q) = %q, want %q", tt.args, tt.flag, result, tt.expected)
			}
		})
	}
}

// TestParseFlag_FollowedByAnotherFlag 测试 flag 后紧跟另一个 flag
func TestParseFlag_FollowedByAnotherFlag(t *testing.T) {
	result := parseFlag("--search --filter Git", "--search")
	if result != "" {
		t.Errorf("expected empty string when flag followed by another flag, got %q", result)
	}
}
