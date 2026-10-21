package config //nolint:staticcheck // H4 本文件测试 V1 MagicDocs deprecated API，保留 V1 作为 fallback

import (
	"os"
	"path/filepath"
	"testing"
)

// TestMagicDocs_Load 测试从目录加载 WHALE.md 文档
// 创建临时目录并放置 WHALE.md 文件，验证 NewMagicDocs 能正确加载文档内容，
// 且通过 Get("project") 可获取到文档内容
func TestMagicDocs_Load(t *testing.T) {
	dir := t.TempDir()
	content := "# My Project\nThis is a test project."
	err := os.WriteFile(filepath.Join(dir, "WHALE.md"), []byte(content), 0644)
	if err != nil {
		t.Fatalf("写入 WHALE.md 失败: %v", err)
	}

	md := NewMagicDocs(dir)

	got, ok := md.Get("project")
	if !ok {
		t.Error("expected Get(\"project\") 返回 true，actual返回 false")
	}
	if got != content {
		t.Errorf("expected内容 %q，actual %q", content, got)
	}
}

// TestMagicDocs_LoadRecursive 测试从指定目录加载文档（V1 仅扫描 watchDir 本身）
// V1 的 MagicDocs 不递归向上遍历，只在 watchDir 中扫描。
// 本test verification在子目录创建 MagicDocs 时，不会加载父目录的文档
func TestMagicDocs_LoadRecursive(t *testing.T) {
	root := t.TempDir()

	// 在根目录放置 WHALE.md
	rootContent := "# Root Project\nRoot level docs."
	err := os.WriteFile(filepath.Join(root, "WHALE.md"), []byte(rootContent), 0644)
	if err != nil {
		t.Fatalf("写入根目录 WHALE.md 失败: %v", err)
	}

	// 创建子目录
	subDir := filepath.Join(root, "subdir")
	err = os.MkdirAll(subDir, 0755)
	if err != nil {
		t.Fatalf("创建子目录失败: %v", err)
	}

	// 在子目录放置不同的 WHALE.md
	subContent := "# Sub Project\nSub level docs."
	err = os.WriteFile(filepath.Join(subDir, "WHALE.md"), []byte(subContent), 0644)
	if err != nil {
		t.Fatalf("写入子目录 WHALE.md 失败: %v", err)
	}

	// 从子目录创建 MagicDocs，V1 只扫描子目录本身
	md := NewMagicDocs(subDir)

	got, ok := md.Get("project")
	if !ok {
		t.Error("expected Get(\"project\") 返回 true，actual返回 false")
	}
	if got != subContent {
		t.Errorf("expected加载子目录内容 %q，actual %q", subContent, got)
	}

	// 验证不会加载父目录的文档
	all := md.All()
	if len(all) != 1 {
		t.Errorf("V1 应只扫描 watchDir 本身，expected 1 个文档，actual %d 个", len(all))
	}
}

// TestMagicDocs_NotFound 测试目录中无标记文档时返回空结果
// 在空临时目录中创建 MagicDocs，验证 All() 返回空 map，
// Get() 返回空字符串和 false
func TestMagicDocs_NotFound(t *testing.T) {
	dir := t.TempDir()

	md := NewMagicDocs(dir)

	all := md.All()
	if len(all) != 0 {
		t.Errorf("空目录expected 0 个文档，actual %d 个", len(all))
	}

	_, ok := md.Get("project")
	if ok {
		t.Error("空目录expected Get(\"project\") 返回 false")
	}

	list := md.List()
	if len(list) != 0 {
		t.Errorf("空目录expected List 返回空切片，actual %d 项", len(list))
	}
}

// TestMagicDocs_TemplateVars 测试 ProjectContext 方法的上下文组装
// V1 的 MagicDocs 不支持 {{project}} 模板变量替换，
// 但 ProjectContext() 方法会按 "项目说明" 和 "规则" 分类组装上下文，
// 本test verification该组装逻辑的正确性
func TestMagicDocs_TemplateVars(t *testing.T) {
	dir := t.TempDir()

	// 创建 WHALE.md（映射到 "project" 键）
	projectContent := "# Project Guide\nUse Go 1.24 for this project."
	err := os.WriteFile(filepath.Join(dir, "WHALE.md"), []byte(projectContent), 0644)
	if err != nil {
		t.Fatalf("写入 WHALE.md 失败: %v", err)
	}

	// 创建 .cursorrules（映射到 "rules" 键）
	rulesContent := "Always use tabs for indentation."
	err = os.WriteFile(filepath.Join(dir, ".cursorrules"), []byte(rulesContent), 0644)
	if err != nil {
		t.Fatalf("写入 .cursorrules 失败: %v", err)
	}

	md := NewMagicDocs(dir)

	ctx := md.ProjectContext()

	// 验证上下文包含项目说明和规则标题
	if ctx == "" {
		t.Error("ProjectContext should not be empty")
	}

	// 验证包含项目说明内容
	if !containsString(ctx, projectContent) {
		t.Errorf("ProjectContext should contain项目说明 %q，actual: %q", projectContent, ctx)
	}

	// 验证包含规则内容
	if !containsString(ctx, rulesContent) {
		t.Errorf("ProjectContext should contain规则 %q，actual: %q", rulesContent, ctx)
	}

	// 验证包含分类标题
	if !containsString(ctx, "项目说明") {
		t.Error("ProjectContext should contain '项目说明' 标题")
	}
	if !containsString(ctx, "规则") {
		t.Error("ProjectContext should contain '规则' 标题")
	}
}

// TestMagicDocs_Cache 测试文档加载后的缓存行为
// 多次调用 Get() should return相同的结果，且 All() 返回的 map
// 是副本而非内部引用（修改返回值不影响缓存）
func TestMagicDocs_Cache(t *testing.T) {
	dir := t.TempDir()
	content := "# Cached Project\nThis content should be cached."
	err := os.WriteFile(filepath.Join(dir, "WHALE.md"), []byte(content), 0644)
	if err != nil {
		t.Fatalf("写入 WHALE.md 失败: %v", err)
	}

	md := NewMagicDocs(dir)

	// 第一次获取
	got1, ok1 := md.Get("project")
	if !ok1 {
		t.Error("第一次 Get(\"project\") should return true")
	}

	// 第二次获取，should return相同结果
	got2, ok2 := md.Get("project")
	if !ok2 {
		t.Error("第二次 Get(\"project\") should return true")
	}
	if got1 != got2 {
		t.Errorf("缓存should return一致结果，第一次 %q，第二次 %q", got1, got2)
	}

	// 修改磁盘文件不应影响缓存
	err = os.WriteFile(filepath.Join(dir, "WHALE.md"), []byte("# Modified\nNew content"), 0644)
	if err != nil {
		t.Fatalf("修改 WHALE.md 失败: %v", err)
	}

	got3, _ := md.Get("project")
	if got3 != got1 {
		t.Error("缓存未刷新前should return旧内容，但内容已变化")
	}

	// Refresh 后应加载新内容
	md.Refresh()
	got4, _ := md.Get("project")
	if got4 == got1 {
		t.Error("Refresh 后should return新内容，但仍返回旧内容")
	}

	// All() 返回的 map 是副本，修改不影响内部缓存
	allMap := md.All()
	allMap["project"] = "tampered"
	got5, _ := md.Get("project")
	if got5 == "tampered" {
		t.Error("All() 返回的 map 应为副本，修改不应影响内部缓存")
	}
}

// TestMagicDocs_CompatibilityFiles test compatibility文件（CLAUDE.md, .cursorrules）的加载
// 验证 MagicDocs 能识别 CLAUDE.md 和 .cursorrules 文件，
// 并正确映射到对应的键名
func TestMagicDocs_CompatibilityFiles(t *testing.T) {
	dir := t.TempDir()

	claudeContent := "Claude-specific instructions."
	err := os.WriteFile(filepath.Join(dir, "CLAUDE.md"), []byte(claudeContent), 0644)
	if err != nil {
		t.Fatalf("写入 CLAUDE.md 失败: %v", err)
	}

	cursorContent := "Cursor rules for the project."
	err = os.WriteFile(filepath.Join(dir, ".cursorrules"), []byte(cursorContent), 0644)
	if err != nil {
		t.Fatalf("写入 .cursorrules 失败: %v", err)
	}

	md := NewMagicDocs(dir)

	// CLAUDE.md 也映射到 "project" 键
	got, ok := md.Get("project")
	if !ok {
		t.Error("expected Get(\"project\") 返回 true（CLAUDE.md）")
	}
	if got != claudeContent {
		t.Errorf("expected CLAUDE.md 内容 %q，actual %q", claudeContent, got)
	}

	// .cursorrules 映射到 "rules" 键
	got, ok = md.Get("rules")
	if !ok {
		t.Error("expected Get(\"rules\") 返回 true（.cursorrules）")
	}
	if got != cursorContent {
		t.Errorf("expected .cursorrules 内容 %q，actual %q", cursorContent, got)
	}
}

// TestMagicDocs_DocsDirectory 测试 docs/ 目录下 .md 文件的加载
// 验证 MagicDocs 能扫描 docs/ 目录下的 .md 文件，
// 并以 "docs/filename.md" 为键存储
func TestMagicDocs_DocsDirectory(t *testing.T) {
	dir := t.TempDir()

	docsDir := filepath.Join(dir, "docs")
	err := os.MkdirAll(docsDir, 0755)
	if err != nil {
		t.Fatalf("创建 docs 目录失败: %v", err)
	}

	apiContent := "# API Documentation\nREST API docs."
	err = os.WriteFile(filepath.Join(docsDir, "api.md"), []byte(apiContent), 0644)
	if err != nil {
		t.Fatalf("写入 api.md 失败: %v", err)
	}

	guideContent := "# User Guide\nHow to use this project."
	err = os.WriteFile(filepath.Join(docsDir, "guide.md"), []byte(guideContent), 0644)
	if err != nil {
		t.Fatalf("写入 guide.md 失败: %v", err)
	}

	md := NewMagicDocs(dir)

	got, ok := md.Get("docs/api.md")
	if !ok {
		t.Error("expected Get(\"docs/api.md\") 返回 true")
	}
	if got != apiContent {
		t.Errorf("expected docs/api.md 内容 %q，actual %q", apiContent, got)
	}

	got, ok = md.Get("docs/guide.md")
	if !ok {
		t.Error("expected Get(\"docs/guide.md\") 返回 true")
	}
	if got != guideContent {
		t.Errorf("expected docs/guide.md 内容 %q，actual %q", guideContent, got)
	}
}

// TestMagicDocs_EmptyWatchDir 测试 watchDir 为空字符串时不扫描
// 验证 NewMagicDocs("") 不会 panic，且返回空文档
func TestMagicDocs_EmptyWatchDir(t *testing.T) {
	md := NewMagicDocs("")

	all := md.All()
	if len(all) != 0 {
		t.Errorf("空 watchDir expected 0 个文档，actual %d 个", len(all))
	}

	list := md.List()
	if len(list) != 0 {
		t.Errorf("空 watchDir expected List 返回空，actual %d 项", len(list))
	}
}

// TestMagicDocs_EmptyFileSkipped 测试空内容文件被跳过
// 验证只有空白字符的文件不会被加载到文档缓存中
func TestMagicDocs_EmptyFileSkipped(t *testing.T) {
	dir := t.TempDir()

	// 创建只有空白的 WHALE.md
	err := os.WriteFile(filepath.Join(dir, "WHALE.md"), []byte("   \n\n  \t  "), 0644)
	if err != nil {
		t.Fatalf("写入 WHALE.md 失败: %v", err)
	}

	md := NewMagicDocs(dir)

	_, ok := md.Get("project")
	if ok {
		t.Error("空白内容的文件不应被加载到缓存中")
	}
}

// containsString helper functions：检查字符串是否包含子串
func containsString(s, substr string) bool {
	return len(s) >= len(substr) && (s == substr || len(substr) == 0 ||
		(len(s) > 0 && len(substr) > 0 && findSubstring(s, substr)))
}

// findSubstring helper functions：查找子串
func findSubstring(s, substr string) bool {
	for i := 0; i <= len(s)-len(substr); i++ {
		if s[i:i+len(substr)] == substr {
			return true
		}
	}
	return false
}

// TestMagicDocs_ScanFixedFiles 测试扫描 WHALE.md、CLAUDE.md、.cursorrules 固定文件
// 验证 NewMagicDocs 能同时识别多种标记文件并正确映射到对应键名
func TestMagicDocs_ScanFixedFiles(t *testing.T) {
	dir := t.TempDir()

	// 创建三种固定文件
	whaleContent := "# Whale Project\nWhale instructions."
	if err := os.WriteFile(filepath.Join(dir, "WHALE.md"), []byte(whaleContent), 0644); err != nil {
		t.Fatalf("写入 WHALE.md 失败: %v", err)
	}
	claudeContent := "# Claude Project\nClaude instructions."
	if err := os.WriteFile(filepath.Join(dir, "CLAUDE.md"), []byte(claudeContent), 0644); err != nil {
		t.Fatalf("写入 CLAUDE.md 失败: %v", err)
	}
	cursorContent := "Cursor rules content."
	if err := os.WriteFile(filepath.Join(dir, ".cursorrules"), []byte(cursorContent), 0644); err != nil {
		t.Fatalf("写入 .cursorrules 失败: %v", err)
	}

	md := NewMagicDocs(dir)

	// WHALE.md 和 CLAUDE.md 都映射到 "project" 键（后扫描的覆盖先扫描的）
	gotProject, okProject := md.Get("project")
	if !okProject {
		t.Error("应找到 project 键")
	}
	_ = gotProject

	// .cursorrules 映射到 "rules" 键
	gotRules, okRules := md.Get("rules")
	if !okRules {
		t.Error("应找到 rules 键")
	}
	if gotRules != cursorContent {
		t.Errorf("rules 内容 = %q, expected %q", gotRules, cursorContent)
	}

	// 应至少有 2 个文档
	all := md.All()
	if len(all) < 2 {
		t.Errorf("应至少有 2 个文档，actual %d 个", len(all))
	}
}

// TestMagicDocs_Refresh 测试 Refresh() 重新扫描并更新文档映射
// 验证 Refresh 能清除旧缓存并加载新增文件
func TestMagicDocs_Refresh(t *testing.T) {
	dir := t.TempDir()

	// 初始创建 WHALE.md
	initialContent := "# Initial\nInitial content."
	if err := os.WriteFile(filepath.Join(dir, "WHALE.md"), []byte(initialContent), 0644); err != nil {
		t.Fatalf("写入 WHALE.md 失败: %v", err)
	}

	md := NewMagicDocs(dir)

	got, ok := md.Get("project")
	if !ok || got != initialContent {
		t.Errorf("初始加载: project = %q, ok = %v, expected %q", got, ok, initialContent)
	}

	// 修改 WHALE.md 内容
	updatedContent := "# Updated\nUpdated content."
	if err := os.WriteFile(filepath.Join(dir, "WHALE.md"), []byte(updatedContent), 0644); err != nil {
		t.Fatalf("更新 WHALE.md 失败: %v", err)
	}

	// 刷新前仍返回旧内容
	got, _ = md.Get("project")
	if got == updatedContent {
		t.Error("刷新前不should return新内容")
	}

	// 执行刷新
	md.Refresh()

	// 刷新后should return新内容
	got, ok = md.Get("project")
	if !ok {
		t.Error("刷新后应找到 project 键")
	}
	if got != updatedContent {
		t.Errorf("刷新后: project = %q, expected %q", got, updatedContent)
	}
}

// TestMagicDocs_ProjectContext 测试 ProjectContext() 返回格式化的项目文档文本
// 验证 ProjectContext 方法能正确组装项目说明和规则内容
func TestMagicDocs_ProjectContext(t *testing.T) {
	dir := t.TempDir()

	// 创建项目说明文件
	projectContent := "# My Project\nThis is a test project with Go."
	if err := os.WriteFile(filepath.Join(dir, "WHALE.md"), []byte(projectContent), 0644); err != nil {
		t.Fatalf("写入 WHALE.md 失败: %v", err)
	}

	// 创建规则文件
	rulesContent := "Always use tabs for indentation."
	if err := os.WriteFile(filepath.Join(dir, ".cursorrules"), []byte(rulesContent), 0644); err != nil {
		t.Fatalf("写入 .cursorrules 失败: %v", err)
	}

	md := NewMagicDocs(dir)
	ctx := md.ProjectContext()

	// 验证上下文不为空
	if ctx == "" {
		t.Error("ProjectContext should not be empty")
	}

	// 验证包含项目说明标题和内容
	if !containsString(ctx, "项目说明") {
		t.Error("ProjectContext should contain '项目说明' 标题")
	}
	if !containsString(ctx, projectContent) {
		t.Errorf("ProjectContext should contain项目说明内容 %q", projectContent)
	}

	// 验证包含规则标题和内容
	if !containsString(ctx, "规则") {
		t.Error("ProjectContext should contain '规则' 标题")
	}
	if !containsString(ctx, rulesContent) {
		t.Errorf("ProjectContext should contain规则内容 %q", rulesContent)
	}
}

// TestMagicDocs_HTMLMarker 测试 HTML 注释标记检测
// 包含 <!-- MAGIC DOC --> 的文件内容应被识别为 magic doc
func TestMagicDocs_HTMLMarker(t *testing.T) {
	content := "<!-- MAGIC DOC -->\n# My Project\nThis is a project doc."
	if !HasMagicMarker(content) {
		t.Error("包含 <!-- MAGIC DOC --> 标记的内容应被识别为 magic doc")
	}
}

// TestMagicDocs_MarkdownMarker 测试 Markdown 标题标记检测
// 包含 # MAGIC DOC: title 的文件内容应被识别为 magic doc
func TestMagicDocs_MarkdownMarker(t *testing.T) {
	content := "# MAGIC DOC: project-guide\nThis is a project guide."
	if !HasMagicMarker(content) {
		t.Error("包含 # MAGIC DOC: title 标记的内容应被识别为 magic doc")
	}
}

// TestMagicDocs_OnlyFirst5Lines 测试仅检查前 5 行的标记
// 标记出现在第 6 行时不应被检测到
func TestMagicDocs_OnlyFirst5Lines(t *testing.T) {
	// 前 5 行无标记，第 6 行有标记
	content := "line1\nline2\nline3\nline4\nline5\n<!-- MAGIC DOC -->\nmore content"
	if HasMagicMarker(content) {
		t.Error("第 6 行的标记不应被检测到，仅检查前 5 行")
	}

	// Markdown 标记在第 6 行也不应被检测
	content2 := "line1\nline2\nline3\nline4\nline5\n# MAGIC DOC: late-title\ncontent"
	if HasMagicMarker(content2) {
		t.Error("第 6 行的 Markdown 标记不应被检测到，仅检查前 5 行")
	}
}
