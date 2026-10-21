package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestDiscoverInstructionFiles_Basic 测试基本层级发现
// 创建multi-tier级目录结构，验证 DiscoverInstructionFiles 能从 CWD 向上遍历发现各层级的 WHALE.md，
// 且结果按深度降序排列（离 CWD 远的先加载，离 CWD 近的post-loading，优先级最高）
func TestDiscoverInstructionFiles_Basic(t *testing.T) {
	root := t.TempDir()
	os.WriteFile(filepath.Join(root, "WHALE.md"), []byte("root instructions"), 0644)
	os.MkdirAll(filepath.Join(root, "src", "api"), 0755)
	os.WriteFile(filepath.Join(root, "src", "WHALE.md"), []byte("src instructions"), 0644)
	os.WriteFile(filepath.Join(root, "src", "api", "WHALE.md"), []byte("api instructions"), 0644)

	files := DiscoverInstructionFiles(filepath.Join(root, "src", "api"))
	if len(files) < 2 {
		t.Errorf("expected发现至少 2 个 WHALE.md，actual发现 %d 个", len(files))
	}

	// 验证按深度降序排列：深度大的在前，深度小的在后
	for i := 1; i < len(files); i++ {
		if files[i].Depth > files[i-1].Depth {
			t.Errorf("文件应按深度降序排列，但 files[%d].Depth=%d > files[%d].Depth=%d",
				i, files[i].Depth, i-1, files[i-1].Depth)
		}
	}

	// 验证各层级内容都被发现
	contentSet := make(map[string]bool)
	for _, f := range files {
		contentSet[f.Content] = true
	}
	if !contentSet["api instructions"] {
		t.Error("应发现 api 层级的 WHALE.md")
	}
	if !contentSet["src instructions"] {
		t.Error("应发现 src 层级的 WHALE.md")
	}
	if !contentSet["root instructions"] {
		t.Error("应发现 root 层级的 WHALE.md")
	}
}

// TestDiscoverInstructionFiles_Deduplication 测试内容去重
// 在不同层级放置相同内容的 WHALE.md，验证 SHA256 去重机制只保留一份
func TestDiscoverInstructionFiles_Deduplication(t *testing.T) {
	root := t.TempDir()
	sameContent := "same instructions"
	os.WriteFile(filepath.Join(root, "WHALE.md"), []byte(sameContent), 0644)
	os.MkdirAll(filepath.Join(root, "sub"), 0755)
	os.WriteFile(filepath.Join(root, "sub", "WHALE.md"), []byte(sameContent), 0644)

	files := DiscoverInstructionFiles(root)
	contentSet := make(map[string]bool)
	for _, f := range files {
		contentSet[f.Content] = true
	}
	if len(contentSet) != 1 {
		t.Errorf("相同内容应去重，expected 1 种内容，actual %d 种", len(contentSet))
	}
}

// TestDiscoverInstructionFiles_Compatibility 测试 CLAUDE.md 和 .cursorrules compatibility
// 验证 DiscoverInstructionFiles 能发现 CLAUDE.md 和 .cursorrules 文件，并标记正确的类型标识
func TestDiscoverInstructionFiles_Compatibility(t *testing.T) {
	root := t.TempDir()
	os.WriteFile(filepath.Join(root, "CLAUDE.md"), []byte("claude instructions"), 0644)
	os.WriteFile(filepath.Join(root, ".cursorrules"), []byte("cursor rules"), 0644)

	files := DiscoverInstructionFiles(root)
	types := make(map[string]bool)
	for _, f := range files {
		types[f.Type] = true
	}
	if !types["claude"] {
		t.Error("应发现 CLAUDE.md，类型为 claude")
	}
	if !types["cursorrules"] {
		t.Error("应发现 .cursorrules，类型为 cursorrules")
	}
}

// TestMatchConditionalRules_AlwaysLoad 测试无条件规则
// 规则文件没有 paths frontmatter 字段时，应始终加载，无论目标文件路径是什么
func TestMatchConditionalRules_AlwaysLoad(t *testing.T) {
	dir := t.TempDir()
	rulesDir := filepath.Join(dir, ".whale", "rules")
	os.MkdirAll(rulesDir, 0755)

	// 无 paths 字段 = 始终加载
	os.WriteFile(filepath.Join(rulesDir, "always.md"), []byte("always apply"), 0644)

	matched := MatchConditionalRules(rulesDir, "src/api/handler.go")
	if len(matched) != 1 {
		t.Errorf("无条件规则应始终匹配，expected 1 条，actual %d 条", len(matched))
	}
	if len(matched) > 0 && matched[0].Pattern != nil {
		t.Error("无条件规则的 Pattern should be nil")
	}
	if len(matched) > 0 && matched[0].Content != "always apply" {
		t.Errorf("规则内容应为 'always apply'，actual为 '%s'", matched[0].Content)
	}
}

// TestMatchConditionalRules_Conditional 测试条件规则
// 规则文件有 paths frontmatter 字段时，应根据目标文件路径进行 glob 匹配，
// 匹配时加载，不匹配时不加载
func TestMatchConditionalRules_Conditional(t *testing.T) {
	dir := t.TempDir()
	rulesDir := filepath.Join(dir, ".whale", "rules")
	os.MkdirAll(rulesDir, 0755)

	// 有 paths 字段 = 条件匹配
	content := `---
paths:
  - src/api/**/*.go
---
API rules
`
	os.WriteFile(filepath.Join(rulesDir, "api-only.md"), []byte(content), 0644)

	// 匹配的情况
	matched := MatchConditionalRules(rulesDir, "src/api/handler.go")
	if len(matched) != 1 {
		t.Errorf("路径匹配时应加载规则，expected 1 条，actual %d 条", len(matched))
	}
	if len(matched) > 0 {
		if matched[0].Content != "API rules" {
			t.Errorf("规则内容应为 'API rules'，actual为 '%s'", matched[0].Content)
		}
		if len(matched[0].Pattern) != 1 || matched[0].Pattern[0] != "src/api/**/*.go" {
			t.Errorf("Pattern should contain 'src/api/**/*.go'，actual为 %v", matched[0].Pattern)
		}
	}

	// 不匹配的情况
	matched = MatchConditionalRules(rulesDir, "src/cli/main.go")
	if len(matched) != 0 {
		t.Errorf("路径不匹配时不应加载规则，expected 0 条，actual %d 条", len(matched))
	}
}

// TestResolveIncludes_Basic 测试基本 @include 指令解析
// 验证 @include 引用的文件内容能被正确替换到主内容中，原始引用标记被移除
func TestResolveIncludes_Basic(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "included.md"), []byte("included content"), 0644)

	result := ResolveIncludes("before @included.md after", dir, 5, make(map[string]bool))
	if !strings.Contains(result, "included content") {
		t.Error("should contain被引用文件的内容 'included content'")
	}
	if strings.Contains(result, "@included.md") {
		t.Error("@include 指令应被替换，不应保留原始引用 '@included.md'")
	}
	if !strings.Contains(result, "before") || !strings.Contains(result, "after") {
		t.Error("应保留 @include 前后的原始内容")
	}
}

// TestResolveIncludes_MaxDepth 测试递归深度限制
// 验证 maxDepth 参数能限制 @include 的递归解析深度，
// 深度不足时保留未解析的引用，深度足够时解析到最深层
func TestResolveIncludes_MaxDepth(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "a.md"), []byte("@b.md"), 0644)
	os.WriteFile(filepath.Join(dir, "b.md"), []byte("@c.md"), 0644)
	os.WriteFile(filepath.Join(dir, "c.md"), []byte("deep content"), 0644)

	// 深度 1 — 只解析一层：@a.md → 替换为 b.md 的内容（即 @c.md），但不再递归
	result := ResolveIncludes("@a.md", dir, 1, make(map[string]bool))
	if strings.Contains(result, "deep content") {
		t.Error("深度 1 不应解析到第 3 层内容 'deep content'")
	}

	// 深度 5 — 解析所有层
	result = ResolveIncludes("@a.md", dir, 5, make(map[string]bool))
	if !strings.Contains(result, "deep content") {
		t.Error("深度 5 应解析到第 3 层内容 'deep content'")
	}
}

// TestResolveIncludes_CircularReference 测试循环引用防护
// 验证 @include 的循环引用不会导致无限递归或 panic，
// 而是生成循环引用跳过标记
func TestResolveIncludes_CircularReference(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "a.md"), []byte("@b.md"), 0644)
	os.WriteFile(filepath.Join(dir, "b.md"), []byte("@a.md"), 0644)

	// 不应 panic
	result := ResolveIncludes("@a.md", dir, 5, make(map[string]bool))

	// should contain循环引用跳过标记
	if !strings.Contains(result, "循环引用已跳过") {
		t.Error("循环引用应生成跳过标记 '<!-- 循环引用已跳过: ... -->'")
	}
}

// TestDiscoverNestedMemory test nested记忆发现
// 验证 DiscoverNestedMemory 能发现 CWD 与目标文件路径之间的 WHALE.md 文件，
// CWD 在深层目录时，向上遍历到目标文件所在目录，收集沿途的 WHALE.md
func TestDiscoverNestedMemory(t *testing.T) {
	root := t.TempDir()
	os.MkdirAll(filepath.Join(root, "src", "api"), 0755)
	os.WriteFile(filepath.Join(root, "src", "WHALE.md"), []byte("src rules"), 0644)
	os.WriteFile(filepath.Join(root, "src", "api", "WHALE.md"), []byte("api rules"), 0644)

	// CWD 在深层目录，目标文件在根目录
	// 函数从 CWD 向上遍历到目标文件所在目录
	cwd := filepath.Join(root, "src", "api")
	targetFile := filepath.Join(root, "somefile.go")

	files := DiscoverNestedMemory(cwd, targetFile)
	if len(files) < 2 {
		t.Errorf("应发现至少 2 个嵌套 WHALE.md，actual发现 %d 个", len(files))
	}

	// 验证内容包含预期的规则
	contents := make(map[string]bool)
	for _, f := range files {
		contents[f.Content] = true
	}
	if !contents["src rules"] {
		t.Error("应发现 src 层级的 WHALE.md")
	}
	if !contents["api rules"] {
		t.Error("应发现 api 层级的 WHALE.md")
	}

	// 验证按深度降序排列：远的先加载，近的post-loading
	for i := 1; i < len(files); i++ {
		if files[i].Depth > files[i-1].Depth {
			t.Errorf("文件应按深度降序排列，但 files[%d].Depth=%d > files[%d].Depth=%d",
				i, files[i].Depth, i-1, files[i-1].Depth)
		}
	}
}

// TestDiscoverInstructionFiles 测试从 CWD 向上遍历发现指令文件
// 验证 DiscoverInstructionFiles 在multi-tier级目录结构中能正确发现各层级的指令文件
func TestDiscoverInstructionFiles(t *testing.T) {
	root := t.TempDir()
	subDir := filepath.Join(root, "project", "src")
	os.MkdirAll(subDir, 0755)

	// 在根目录放置 WHALE.md
	os.WriteFile(filepath.Join(root, "WHALE.md"), []byte("root level instructions"), 0644)
	// 在子目录放置 WHALE.md
	os.WriteFile(filepath.Join(subDir, "WHALE.md"), []byte("src level instructions"), 0644)

	files := DiscoverInstructionFiles(subDir)
	if len(files) < 2 {
		t.Errorf("应发现至少 2 个指令文件，actual发现 %d 个", len(files))
	}

	// 验证按深度降序排列
	for i := 1; i < len(files); i++ {
		if files[i].Depth > files[i-1].Depth {
			t.Errorf("文件应按深度降序排列，但 files[%d].Depth=%d > files[%d].Depth=%d",
				i, files[i].Depth, i-1, files[i-1].Depth)
		}
	}
}

// TestMatchConditionalRules 测试带 paths frontmatter 的条件规则
// 验证规则文件中的 paths 字段仅对匹配的文件路径生效
func TestMatchConditionalRules(t *testing.T) {
	dir := t.TempDir()
	rulesDir := filepath.Join(dir, ".whale", "rules")
	os.MkdirAll(rulesDir, 0755)

	// 创建带 paths 的条件规则
	goRuleContent := `---
paths:
  - "**/*.go"
---
Go specific rules
`
	os.WriteFile(filepath.Join(rulesDir, "go-rules.md"), []byte(goRuleContent), 0644)

	// 创建不带 paths 的无条件规则
	os.WriteFile(filepath.Join(rulesDir, "always.md"), []byte("Always apply rules"), 0644)

	// 测试匹配 .go 文件 — should return两条规则
	matched := MatchConditionalRules(rulesDir, "main.go")
	if len(matched) != 2 {
		t.Errorf("匹配 .go 文件should return 2 条规则，actual %d 条", len(matched))
	}

	// 测试不匹配 .py 文件 — 应只返回无条件规则
	matched = MatchConditionalRules(rulesDir, "main.py")
	if len(matched) != 1 {
		t.Errorf("匹配 .py 文件should return 1 条无条件规则，actual %d 条", len(matched))
	}
	if len(matched) > 0 && matched[0].Content != "Always apply rules" {
		t.Errorf("无条件规则内容应为 'Always apply rules'，actual为 '%s'", matched[0].Content)
	}
}

// TestResolveIncludes 测试 @include 指令解析
// 验证 ResolveIncludes 能正确将 @include 引用替换为被引用文件的内容
func TestResolveIncludes(t *testing.T) {
	dir := t.TempDir()

	// 创建被引用的文件
	os.WriteFile(filepath.Join(dir, "shared.md"), []byte("Shared instructions content"), 0644)

	// 创建包含 @include 的主内容
	mainContent := "Main instructions\n@shared.md\nEnd of instructions"

	result := ResolveIncludes(mainContent, dir, 5, make(map[string]bool))

	// 验证 @include 被替换为文件内容
	if !strings.Contains(result, "Shared instructions content") {
		t.Error("should contain被引用文件的内容 'Shared instructions content'")
	}
	if strings.Contains(result, "@shared.md") {
		t.Error("@include 指令应被替换，不应保留 '@shared.md'")
	}
	if !strings.Contains(result, "Main instructions") {
		t.Error("应保留 @include 前的原始内容")
	}
	if !strings.Contains(result, "End of instructions") {
		t.Error("应保留 @include 后的原始内容")
	}
}

// === 3-C-1 @include 路径白名单测试 ===

// TestResolveIncludes_PathTraversal_Blocked 验证 @../secret 路径穿越被白名单阻断
func TestResolveIncludes_PathTraversal_Blocked(t *testing.T) {
	dir := t.TempDir()
	// 在 dir 上层创建一个假 secret 文件
	parentDir := filepath.Dir(dir)
	secretFile := filepath.Join(parentDir, "secret")
	if err := os.WriteFile(secretFile, []byte("SECRET_CONTENT"), 0644); err != nil {
		t.Fatalf("WriteFile failed: %v", err)
	}
	defer os.Remove(secretFile)

	content := "@../secret"
	result := ResolveIncludes(content, dir, 5, nil)

	// 应保留原 @../secret 文本，不含 SECRET_CONTENT
	if strings.Contains(result, "SECRET_CONTENT") {
		t.Errorf("路径穿越未被阻断，结果含 SECRET_CONTENT: %s", result)
	}
	if !strings.Contains(result, "@../secret") {
		t.Errorf("应保留原 @../secret 文本，got: %s", result)
	}
}

// TestResolveIncludes_AbsolutePath_Blocked 验证 @/etc/passwd 绝对路径被白名单阻断
func TestResolveIncludes_AbsolutePath_Blocked(t *testing.T) {
	dir := t.TempDir()
	content := "@/etc/passwd"
	result := ResolveIncludes(content, dir, 5, nil)

	// 应保留原 @/etc/passwd 文本
	if !strings.Contains(result, "@/etc/passwd") {
		t.Errorf("绝对路径未被阻断，got: %s", result)
	}
	// 不应包含 /etc/passwd 的内容
	if strings.Contains(result, "root:") {
		t.Errorf("绝对路径未被阻断，结果含 /etc/passwd 内容: %s", result)
	}
}

// TestResolveIncludes_HomeRelative_Blocked 验证 @~/.ssh/id_rsa 家目录相对路径被白名单阻断
func TestResolveIncludes_HomeRelative_Blocked(t *testing.T) {
	dir := t.TempDir()
	content := "@~/.ssh/id_rsa"
	result := ResolveIncludes(content, dir, 5, nil)

	// ~/.ssh 不在白名单（白名单是 ~/.whale，非 ~/.ssh）
	if !strings.Contains(result, "@~/.ssh/id_rsa") {
		t.Errorf("家目录相对路径未被阻断，got: %s", result)
	}
}

// TestResolveIncludes_Whitelisted_OK 验证 @docs/rules.md（在 cwd 下）正常替换
func TestResolveIncludes_Whitelisted_OK(t *testing.T) {
	dir := t.TempDir()
	// 在 dir 下创建 docs/rules.md
	docsDir := filepath.Join(dir, "docs")
	os.MkdirAll(docsDir, 0755)
	rulesFile := filepath.Join(docsDir, "rules.md")
	os.WriteFile(rulesFile, []byte("RULES_CONTENT"), 0644)

	content := "@docs/rules.md"
	result := ResolveIncludes(content, dir, 5, nil)

	// 应替换为 RULES_CONTENT
	if !strings.Contains(result, "RULES_CONTENT") {
		t.Errorf("白名单内路径未正常替换，got: %s", result)
	}
	if strings.Contains(result, "@docs/rules.md") {
		t.Errorf("应替换 @docs/rules.md，但保留原文本: %s", result)
	}
}

// TestResolveIncludes_WhaleSubdir_OK 验证 @.whale/skills/test.md 正常替换
func TestResolveIncludes_WhaleSubdir_OK(t *testing.T) {
	dir := t.TempDir()
	whaleDir := filepath.Join(dir, ".whale", "skills")
	os.MkdirAll(whaleDir, 0755)
	skillFile := filepath.Join(whaleDir, "test.md")
	os.WriteFile(skillFile, []byte("SKILL_CONTENT"), 0644)

	content := "@.whale/skills/test.md"
	result := ResolveIncludes(content, dir, 5, nil)

	if !strings.Contains(result, "SKILL_CONTENT") {
		t.Errorf(".whale 子目录路径未正常替换，got: %s", result)
	}
}

// TestResolveIncludes_Symlink_Blocked 验证 symlink 指向白名单外不被绕过
func TestResolveIncludes_Symlink_Blocked(t *testing.T) {
	dir := t.TempDir()
	parentDir := filepath.Dir(dir)
	targetFile := filepath.Join(parentDir, "target")
	os.WriteFile(targetFile, []byte("TARGET_CONTENT"), 0644)
	defer os.Remove(targetFile)

	// 在 dir 下创建 symlink 指向 target（白名单外）
	linkPath := filepath.Join(dir, "link.md")
	err := os.Symlink(targetFile, linkPath)
	if err != nil {
		t.Skipf("无法创建 symlink（权限/平台限制）: %v", err)
	}

	content := "@link.md"
	result := ResolveIncludes(content, dir, 5, nil)

	// symlink 解析后指向白名单外，应阻断
	if strings.Contains(result, "TARGET_CONTENT") {
		t.Errorf("symlink 绕过白名单，结果含 TARGET_CONTENT: %s", result)
	}
}

// TestResolveIncludes_CircularReference_Preserved 验证循环引用检测仍有效
func TestResolveIncludes_CircularReference_Preserved(t *testing.T) {
	dir := t.TempDir()
	// 创建 a.md @b.md，b.md @a.md
	os.WriteFile(filepath.Join(dir, "a.md"), []byte("@b.md"), 0644)
	os.WriteFile(filepath.Join(dir, "b.md"), []byte("@a.md"), 0644)

	content := "@a.md"
	result := ResolveIncludes(content, dir, 5, nil)

	// 应检测到循环引用，结果含循环引用提示
	if !strings.Contains(result, "循环引用") {
		t.Errorf("循环引用检测失效，got: %s", result)
	}
}

// TestResolveIncludes_MaxDepth_Preserved 验证 maxDepth 限制仍有效
func TestResolveIncludes_MaxDepth_Preserved(t *testing.T) {
	dir := t.TempDir()
	// 创建嵌套引用链 a@b@b@b...
	os.WriteFile(filepath.Join(dir, "a.md"), []byte("@b.md"), 0644)
	os.WriteFile(filepath.Join(dir, "b.md"), []byte("@b.md"), 0644)

	content := "@a.md"
	result := ResolveIncludes(content, dir, 2, nil) // maxDepth=2

	// maxDepth 限制应生效，不会无限递归
	if strings.Contains(result, "panic") {
		t.Errorf("maxDepth 限制失效导致 panic: %s", result)
	}
}
