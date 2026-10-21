// baseline_test.go — 桶 6 baseline checklist 反例测试（地基层 §2.3）。
// 2 个反例测试覆盖：规则数 ≤10 + 格式有效（# <name> checklist + numbered list）。

package orchestrator

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestChecklist_MaxTenRules 每个 .checklist.md 文件规则数 ≤ 10（解析 numbered list）。
// 验证目标（地基层 §2.3）：≤10 条硬规则，AI 不读大段散文，精炼 checklist 更有效。
func TestChecklist_MaxTenRules(t *testing.T) {
	lib := NewBaselineLibrary("")
	all := lib.LoadAll()

	if len(all) != 5 {
		t.Errorf("expected 5 builtin checklists, got %d", len(all))
	}

	for name, content := range all {
		count := CountRules(content)
		if count > 10 {
			t.Errorf("checklist %s has %d rules (must be ≤10)", name, count)
		}
		if count == 0 {
			t.Errorf("checklist %s has 0 rules (must have at least 1)", name)
		}
	}
}

// TestChecklist_FormatValid 每个文件以 # <name> checklist 开头 + numbered list。
// 验证目标（地基层 §2.3）：格式可机检，子任务注入时能正确解析。
func TestChecklist_FormatValid(t *testing.T) {
	lib := NewBaselineLibrary("")
	all := lib.LoadAll()

	for name, content := range all {
		if err := ValidateFormat(content); err != nil {
			t.Errorf("checklist %s format invalid: %v", name, err)
		}
		// 额外验证：第一行含 "checklist"
		lines := strings.Split(content, "\n")
		if len(lines) == 0 {
			t.Errorf("checklist %s is empty", name)
			continue
		}
		if !strings.Contains(lines[0], "checklist") {
			t.Errorf("checklist %s first line must contain 'checklist', got %q", name, lines[0])
		}
	}
}

// TestBaselineLibrary_LoadByTaskType 按任务类型加载 checklist（补充测试）。
func TestBaselineLibrary_LoadByTaskType(t *testing.T) {
	lib := NewBaselineLibrary("")

	cases := []struct {
		taskType     string
		expectSubstr string
	}{
		{TaskTypeBugFix, "error"},
		{TaskTypeImplement, "concurrency"},
		{TaskTypeDocGen, "api-design"},
		{TaskTypeReview, "test-quality"},
		{TaskTypePlan, "cli-design"},
	}

	for _, c := range cases {
		content, err := lib.Load(c.taskType)
		if err != nil {
			t.Errorf("Load(%q) failed: %v", c.taskType, err)
			continue
		}
		if !strings.Contains(content, c.expectSubstr) {
			t.Errorf("Load(%q) should contain %q, got first line: %q",
				c.taskType, c.expectSubstr, strings.Split(content, "\n")[0])
		}
	}

	// 未知任务类型应返回 error
	if _, err := lib.Load("unknown-type"); err == nil {
		t.Error("Load(unknown-type) must return error")
	}
}

// TestBaselineLibrary_ProjectLevelOverride 项目级 .whale/baselines/ 覆盖内置（补充测试）。
func TestBaselineLibrary_ProjectLevelOverride(t *testing.T) {
	dir := t.TempDir()

	// 写入项目级 checklist（覆盖内置）
	customContent := "# error-handling checklist\n1. custom rule from project level\n"
	customPath := filepath.Join(dir, "error-handling.checklist.md")
	if err := os.WriteFile(customPath, []byte(customContent), 0o644); err != nil {
		t.Fatalf("write custom checklist: %v", err)
	}

	lib := NewBaselineLibrary(dir)
	content, err := lib.Load(TaskTypeBugFix)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if content != customContent {
		t.Errorf("expected project-level custom content, got builtin")
	}
}
