package skill

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestParseSkillFile_WithFrontmatter 测试带 frontmatter 的技能文件解析
func TestParseSkillFile_WithFrontmatter(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "review.md")
	content := "---\nname: code-review\ndescription: Code review skill\n---\nReview the following code:\n{{prompt}}"
	os.WriteFile(path, []byte(content), 0644)

	skill, err := parseSkillFile(path)
	if err != nil {
		t.Fatalf("parseSkillFile error: %v", err)
	}
	if skill.Name != "code-review" {
		t.Errorf("expected name 'code-review', got %q", skill.Name)
	}
	if skill.Description != "Code review skill" {
		t.Errorf("expected description 'Code review skill', got %q", skill.Description)
	}
	if skill.Prompt != "Review the following code:\n{{prompt}}" {
		t.Errorf("unexpected prompt: %q", skill.Prompt)
	}
}

// TestParseSkillFile_NoFrontmatter 测试无 frontmatter 的技能文件解析
func TestParseSkillFile_NoFrontmatter(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "debug.md")
	content := "Debug the following issue:\n{{prompt}}"
	os.WriteFile(path, []byte(content), 0644)

	skill, err := parseSkillFile(path)
	if err != nil {
		t.Fatalf("parseSkillFile error: %v", err)
	}
	if skill.Name != "debug" {
		t.Errorf("expected name 'debug', got %q", skill.Name)
	}
}

// TestManager_LoadAll 测试加载所有技能
func TestManager_LoadAll(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "review.md"), []byte("---\nname: review\ndescription: Review\n---\nReview: {{prompt}}"), 0644)
	os.WriteFile(filepath.Join(dir, "debug.md"), []byte("---\nname: debug\ndescription: Debug\n---\nDebug: {{prompt}}"), 0644)

	mgr := NewManager(SourceDir{Path: dir, Source: "project"})
	if err := mgr.LoadAll(); err != nil {
		t.Fatalf("LoadAll error: %v", err)
	}

	if len(mgr.List()) != 2 {
		t.Errorf("expected 2 skills, got %d", len(mgr.List()))
	}
}

// TestManager_Execute test execution技能
func TestManager_Execute(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "review.md"), []byte("---\nname: review\ndescription: Review\n---\nReview: {{prompt}}"), 0644)

	mgr := NewManager(SourceDir{Path: dir, Source: "project"})
	mgr.LoadAll()

	result, err := mgr.Execute("review", "my code")
	if err != nil {
		t.Fatalf("Execute error: %v", err)
	}
	if result != "Review: my code" {
		t.Errorf("expected 'Review: my code', got %q", result)
	}
}

// TestManager_Execute_NotFound test execution不存在的技能
func TestManager_Execute_NotFound(t *testing.T) {
	mgr := NewManager()
	_, err := mgr.Execute("nonexistent", "")
	if err == nil {
		t.Error("expected error for nonexistent skill")
	}
}

// TestManager_Execute_NoPlaceholder 测试无占位符时追加提示
func TestManager_Execute_NoPlaceholder(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "test.md"), []byte("---\nname: test\ndescription: Test\n---\nFixed prompt"), 0644)

	mgr := NewManager(SourceDir{Path: dir, Source: "project"})
	mgr.LoadAll()

	result, err := mgr.Execute("test", "user input")
	if err != nil {
		t.Fatalf("Execute error: %v", err)
	}
	expected := "Fixed prompt\n\nuser input"
	if result != expected {
		t.Errorf("expected %q, got %q", expected, result)
	}
}

// TestManager_FormatList 测试格式化技能列表
func TestManager_FormatList(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "review.md"), []byte("---\nname: review\ndescription: Review code\n---\nReview"), 0644)

	mgr := NewManager(SourceDir{Path: dir, Source: "project"})
	mgr.LoadAll()

	list := mgr.FormatList()
	if list == "" {
		t.Error("FormatList returned empty string")
	}
}

// TestManager_FormatList_Empty 测试空技能列表
func TestManager_FormatList_Empty(t *testing.T) {
	mgr := NewManager()
	list := mgr.FormatList()
	if list == "" {
		t.Error("FormatList should return message for empty skills")
	}
}

// TestParseSkillFile_YamlV3_MultilineDescription 验证 yaml.v3 解析多行 description（| 语法）
func TestParseSkillFile_YamlV3_MultilineDescription(t *testing.T) {
	content := "---\nname: test-skill\ndescription: |\n  This is a multi-line\n  description spanning\n  multiple lines.\nmin_version: \"1.0\"\n---\nSkill prompt body\n"
	tmpFile := filepath.Join(t.TempDir(), "test.md")
	os.WriteFile(tmpFile, []byte(content), 0644)

	s, err := parseSkillFile(tmpFile)
	if err != nil {
		t.Fatalf("parseSkillFile 失败: %v", err)
	}

	if s.Name != "test-skill" {
		t.Errorf("Name = %q, want test-skill", s.Name)
	}
	// 多行 description 应完整解析（含换行）
	if !strings.Contains(s.Description, "multi-line") {
		t.Errorf("Description 不含 multi-line: %q", s.Description)
	}
	if !strings.Contains(s.Description, "multiple lines") {
		t.Errorf("Description 不含 multiple lines: %q", s.Description)
	}
	if s.Prompt != "Skill prompt body" {
		t.Errorf("Prompt = %q, want 'Skill prompt body'", s.Prompt)
	}
}

// TestParseSkillFile_YamlV3_ArrayAllowedTools 验证解析数组 allowed-tools
func TestParseSkillFile_YamlV3_ArrayAllowedTools(t *testing.T) {
	content := "---\nname: test-skill\ndescription: test\nallowed-tools:\n  - read_file\n  - write_file\n  - bash\nwhen_to_use: Use when writing files\nmodel: claude-3-5-sonnet\n---\nPrompt\n"
	tmpFile := filepath.Join(t.TempDir(), "test.md")
	os.WriteFile(tmpFile, []byte(content), 0644)

	s, err := parseSkillFile(tmpFile)
	if err != nil {
		t.Fatalf("parseSkillFile 失败: %v", err)
	}

	if len(s.AllowedTools) != 3 {
		t.Fatalf("AllowedTools 长度 = %d, want 3", len(s.AllowedTools))
	}
	expected := []string{"read_file", "write_file", "bash"}
	for i, want := range expected {
		if s.AllowedTools[i] != want {
			t.Errorf("AllowedTools[%d] = %q, want %q", i, s.AllowedTools[i], want)
		}
	}
	if s.WhenToUse != "Use when writing files" {
		t.Errorf("WhenToUse = %q", s.WhenToUse)
	}
	if s.Model != "claude-3-5-sonnet" {
		t.Errorf("Model = %q", s.Model)
	}
}

// TestParseSkillFile_BodyWithMarkdownHr_NotMisparsed 验证正文 --- 不被误识别为 frontmatter 边界
func TestParseSkillFile_BodyWithMarkdownHr_NotMisparsed(t *testing.T) {
	// 正文含 ---（markdown 水平分割线），应作为 prompt 一部分，不影响 frontmatter
	content := "---\nname: test\ndescription: test\n---\nPrompt section 1\n\n---\n\nPrompt section 2\n"
	tmpFile := filepath.Join(t.TempDir(), "test.md")
	os.WriteFile(tmpFile, []byte(content), 0644)

	s, err := parseSkillFile(tmpFile)
	if err != nil {
		t.Fatalf("parseSkillFile 失败: %v", err)
	}

	if s.Name != "test" {
		t.Errorf("Name = %q, want test", s.Name)
	}
	// prompt 应包含两个 section 和中间的 ---
	if !strings.Contains(s.Prompt, "Prompt section 1") {
		t.Errorf("Prompt 不含 section 1: %q", s.Prompt)
	}
	if !strings.Contains(s.Prompt, "Prompt section 2") {
		t.Errorf("Prompt 不含 section 2: %q", s.Prompt)
	}
	if !strings.Contains(s.Prompt, "---") {
		t.Errorf("Prompt 应保留正文中的 ---: %q", s.Prompt)
	}
}

// TestValidateSkill_InvalidName 验证 name 空校验失败
func TestValidateSkill_InvalidName(t *testing.T) {
	s := Skill{Name: "", Description: "valid desc"}
	if validateSkill(s, "1.0") {
		t.Error("空 name 应校验失败")
	}
}

// TestValidateSkill_DescriptionTooLong 验证 description > 500 字校验失败
func TestValidateSkill_DescriptionTooLong(t *testing.T) {
	longDesc := strings.Repeat("a", 501)
	s := Skill{Name: "test", Description: longDesc}
	if validateSkill(s, "1.0") {
		t.Error("description > 500 字应校验失败")
	}
}

// TestValidateSkill_DescriptionChineseByRune 验证中文 description 按 utf8 字符计长（非字节）
// 200 中文字符 = 600 字节，按字节 >500 会误拦；按 rune 计 200 ≤ 500 应通过
// 501 中文字符按 rune 计 > 500 应拦截
func TestValidateSkill_DescriptionChineseByRune(t *testing.T) {
	// 200 中文字符（600 字节）应通过
	chineseDesc := strings.Repeat("字", 200)
	s := Skill{Name: "test", Description: chineseDesc}
	if !validateSkill(s, "1.0") {
		t.Error("200 中文字符（600 字节）按 rune 计 200 ≤ 500 应通过，不应被字节长度误拦")
	}
	// 501 中文字符应拦截
	tooLong := strings.Repeat("字", 501)
	s2 := Skill{Name: "test", Description: tooLong}
	if validateSkill(s2, "1.0") {
		t.Error("501 中文字符应校验失败")
	}
}

// TestValidateSkill_MinVersionIncompatible 验证 min_version 不兼容校验失败
func TestValidateSkill_MinVersionIncompatible(t *testing.T) {
	s := Skill{Name: "test", Description: "desc", MinVersion: "2.0"}
	if validateSkill(s, "1.0") {
		t.Error("min_version=2.0 与 currentVersion=1.0 不兼容应校验失败")
	}
}

// TestValidateSkill_Valid 验证合法 skill 校验通过
func TestValidateSkill_Valid(t *testing.T) {
	s := Skill{Name: "test", Description: "valid", MinVersion: "1.0"}
	if !validateSkill(s, "1.5") {
		t.Error("合法 skill 应校验通过")
	}
}

// TestLoadAll_SourcePriority_UserProject 验证 user/project 同名 skill project 胜出
func TestLoadAll_SourcePriority_UserProject(t *testing.T) {
	userDir := t.TempDir()
	projectDir := t.TempDir()

	// 两个目录各放一个同名 skill，description 不同
	userSkill := "---\nname: shared\ndescription: from-user\n---\nuser prompt\n"
	projectSkill := "---\nname: shared\ndescription: from-project\n---\nproject prompt\n"
	os.WriteFile(filepath.Join(userDir, "shared.md"), []byte(userSkill), 0644)
	os.WriteFile(filepath.Join(projectDir, "shared.md"), []byte(projectSkill), 0644)

	// dirs 顺序：user → project（project 后加载，覆盖 user）
	m := NewManager(
		SourceDir{Path: userDir, Source: "user"},
		SourceDir{Path: projectDir, Source: "project"},
	)
	if err := m.LoadAll(); err != nil {
		t.Fatalf("LoadAll 失败: %v", err)
	}

	s, ok := m.Get("shared")
	if !ok {
		t.Fatal("skill shared 未加载")
	}
	if s.Description != "from-project" {
		t.Errorf("Description = %q, want from-project（project 应覆盖 user）", s.Description)
	}
	if s.Source != "project" {
		t.Errorf("Source = %q, want project", s.Source)
	}
	if s.FilePath != filepath.Join(projectDir, "shared.md") {
		t.Errorf("FilePath = %q", s.FilePath)
	}
}

// TestExecuteWithMeta_ReturnsMeta 验证 ExecuteWithMeta 返回 AllowedTools/Model/WhenToUse/Source
func TestExecuteWithMeta_ReturnsMeta(t *testing.T) {
	m := NewManager(SourceDir{Path: t.TempDir(), Source: "project"})
	// 直接注入 skill（绕过文件加载）
	m.mu.Lock()
	m.skills["test"] = Skill{
		Name:         "test",
		Description:  "test",
		Prompt:       "Hello {{prompt}}",
		Source:       "project",
		AllowedTools: []string{"read_file"},
		Model:        "claude-3-5-sonnet",
		WhenToUse:    "Use when testing",
	}
	m.mu.Unlock()

	result, err := m.ExecuteWithMeta("test", "world")
	if err != nil {
		t.Fatalf("ExecuteWithMeta 失败: %v", err)
	}

	if result.Prompt != "Hello world" {
		t.Errorf("Prompt = %q, want 'Hello world'", result.Prompt)
	}
	if len(result.AllowedTools) != 1 || result.AllowedTools[0] != "read_file" {
		t.Errorf("AllowedTools = %v, want [read_file]", result.AllowedTools)
	}
	if result.Model != "claude-3-5-sonnet" {
		t.Errorf("Model = %q", result.Model)
	}
	if result.WhenToUse != "Use when testing" {
		t.Errorf("WhenToUse = %q", result.WhenToUse)
	}
	if result.Source != "project" {
		t.Errorf("Source = %q", result.Source)
	}
}

// TestExecute_BackwardCompat 验证旧 Execute 仍返回 prompt 字符串
func TestExecute_BackwardCompat(t *testing.T) {
	m := NewManager(SourceDir{Path: t.TempDir(), Source: "project"})
	m.mu.Lock()
	m.skills["test"] = Skill{
		Name:   "test",
		Prompt: "Hello {{prompt}}",
		Source: "project",
	}
	m.mu.Unlock()

	result, err := m.Execute("test", "world")
	if err != nil {
		t.Fatalf("Execute 失败: %v", err)
	}
	if result != "Hello world" {
		t.Errorf("Execute 返回 = %q, want 'Hello world'", result)
	}
}

// TestParseSkillFile_LegacySingleFile 验证现有 .whale/skills/*.md 单文件格式仍可加载
func TestParseSkillFile_LegacySingleFile(t *testing.T) {
	// 旧格式：可能有 frontmatter 也可能没有
	content := "---\nname: legacy\ndescription: legacy skill\n---\nLegacy prompt\n"
	tmpFile := filepath.Join(t.TempDir(), "legacy.md")
	os.WriteFile(tmpFile, []byte(content), 0644)

	s, err := parseSkillFile(tmpFile)
	if err != nil {
		t.Fatalf("parseSkillFile 失败: %v", err)
	}
	if s.Name != "legacy" {
		t.Errorf("Name = %q, want legacy", s.Name)
	}
	if s.Description != "legacy skill" {
		t.Errorf("Description = %q", s.Description)
	}
	// 新字段应为空（旧格式无这些字段）
	if len(s.AllowedTools) != 0 {
		t.Errorf("旧格式 AllowedTools 应为空，got %v", s.AllowedTools)
	}
	if s.Model != "" {
		t.Errorf("旧格式 Model 应为空，got %q", s.Model)
	}
}
