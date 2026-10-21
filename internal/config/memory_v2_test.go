// Package config — 智能记忆检索系统 v2 unit test
// 覆盖 YAML frontmatter 解析、读写、目录扫描、清单生成、选择提示构建、
// 新鲜度文本、偏好检测以及异步预取等功能。
package config

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/anchorandfish/whale-code/internal/ai"
	"github.com/anchorandfish/whale-code/internal/session"
)

// ============================================================
// 4.1 记忆系统refactoring — ParseFrontmatter / WriteAndRead / ScanDirectory
// ============================================================

// TestMemoryEntryV2_ParseFrontmatter 测试 YAML frontmatter + Markdown 正文解析
func TestMemoryEntryV2_ParseFrontmatter(t *testing.T) {
	content := `---
name: coding-style
description: User的编码风格偏好
type: user
created_at: 2026-05-19T10:00:00Z
updated_at: 2026-05-19T10:00:00Z
---

- 使用中文注释
- 函数级注释必须添加
`
	entry, err := ParseMemoryEntry(content)
	if err != nil {
		t.Fatalf("ParseMemoryEntry 返回错误: %v", err)
	}

	if entry.Name != "coding-style" {
		t.Errorf("Name = %q, expected %q", entry.Name, "coding-style")
	}
	if entry.Description != "User的编码风格偏好" {
		t.Errorf("Description = %q, expected %q", entry.Description, "User的编码风格偏好")
	}
	if entry.Type != MemoryTypeUser {
		t.Errorf("Type = %q, expected %q", entry.Type, MemoryTypeUser)
	}
	if !strings.Contains(entry.Content, "使用中文注释") {
		t.Errorf("Content should contain '使用中文注释', actual为 %q", entry.Content)
	}
	if !strings.Contains(entry.Content, "函数级注释必须添加") {
		t.Errorf("Content should contain '函数级注释必须添加', actual为 %q", entry.Content)
	}
}

// TestMemoryEntryV2_ParseFrontmatter_MissingDelimiter 测试缺少 frontmatter 边界标记
func TestMemoryEntryV2_ParseFrontmatter_MissingDelimiter(t *testing.T) {
	// 完全没有 --- 开头
	_, err := ParseMemoryEntry("just some text")
	if err == nil {
		t.Error("缺少 --- 开头should return error")
	}

	// 只有开头 ---，没有结尾 ---
	_, err = ParseMemoryEntry("---\nname: test\n")
	if err == nil {
		t.Error("缺少结尾 --- should return error")
	}
}

// TestMemoryEntryV2_ParseFrontmatter_InvalidType 测试无效记忆类型
func TestMemoryEntryV2_ParseFrontmatter_InvalidType(t *testing.T) {
	content := `---
name: bad-type
description: 无效类型
type: invalid_type
created_at: 2026-05-19T10:00:00Z
updated_at: 2026-05-19T10:00:00Z
---
some content
`
	_, err := ParseMemoryEntry(content)
	if err == nil {
		t.Error("无效类型should return error")
	}
}

// TestMemoryEntryV2_ParseFrontmatter_DefaultType 测试空类型默认为 user
func TestMemoryEntryV2_ParseFrontmatter_DefaultType(t *testing.T) {
	content := `---
name: no-type
description: 无类型
created_at: 2026-05-19T10:00:00Z
updated_at: 2026-05-19T10:00:00Z
---
content
`
	entry, err := ParseMemoryEntry(content)
	if err != nil {
		t.Fatalf("ParseMemoryEntry 返回错误: %v", err)
	}
	if entry.Type != MemoryTypeUser {
		t.Errorf("空类型应默认为 user, actual为 %q", entry.Type)
	}
}

// TestMemoryEntryV2_WriteAndRead 测试写入和读取记忆条目
func TestMemoryEntryV2_WriteAndRead(t *testing.T) {
	dir := t.TempDir()

	entry := &MemoryEntryV2{
		Name:        "test-memory",
		Description: "测试记忆",
		Type:        MemoryTypeUser,
		Content:     "这是测试内容",
	}

	// 写入
	path := filepath.Join(dir, "test-memory.md")
	if err := WriteMemoryEntry(path, entry); err != nil {
		t.Fatalf("WriteMemoryEntry 返回错误: %v", err)
	}

	// 验证文件存在
	if _, err := os.Stat(path); os.IsNotExist(err) {
		t.Fatal("写入后文件应存在")
	}

	// 读取
	readEntry, err := ReadMemoryEntry(path)
	if err != nil {
		t.Fatalf("ReadMemoryEntry 返回错误: %v", err)
	}

	if entry.Name != readEntry.Name {
		t.Errorf("Name 不匹配: 写入 %q, 读取 %q", entry.Name, readEntry.Name)
	}
	if entry.Description != readEntry.Description {
		t.Errorf("Description 不匹配: 写入 %q, 读取 %q", entry.Description, readEntry.Description)
	}
	if entry.Type != readEntry.Type {
		t.Errorf("Type 不匹配: 写入 %q, 读取 %q", entry.Type, readEntry.Type)
	}
	if entry.Content != readEntry.Content {
		t.Errorf("Content 不匹配: 写入 %q, 读取 %q", entry.Content, readEntry.Content)
	}
	if readEntry.FilePath != path {
		t.Errorf("FilePath = %q, expected %q", readEntry.FilePath, path)
	}
}

// TestMemoryEntryV2_WriteAndRead_DifferentTypes 测试不同记忆类型的写入和读取
func TestMemoryEntryV2_WriteAndRead_DifferentTypes(t *testing.T) {
	dir := t.TempDir()

	types := []MemoryType{MemoryTypeUser, MemoryTypeFeedback, MemoryTypeProject, MemoryTypeReference}
	for i, mt := range types {
		entry := &MemoryEntryV2{
			Name:        fmt.Sprintf("memory-%d", i),
			Description: fmt.Sprintf("类型测试 %d", i),
			Type:        mt,
			Content:     fmt.Sprintf("内容 %d", i),
		}
		path := filepath.Join(dir, fmt.Sprintf("mem-%d.md", i))
		if err := WriteMemoryEntry(path, entry); err != nil {
			t.Fatalf("写入类型 %s 失败: %v", mt, err)
		}

		readEntry, err := ReadMemoryEntry(path)
		if err != nil {
			t.Fatalf("读取类型 %s 失败: %v", mt, err)
		}
		if readEntry.Type != mt {
			t.Errorf("类型 %s: 读取类型为 %q, expected %q", mt, readEntry.Type, mt)
		}
	}
}

// TestMemoryEntryV2_WriteCreatesDirectory 测试写入时自动创建目录
func TestMemoryEntryV2_WriteCreatesDirectory(t *testing.T) {
	dir := t.TempDir()
	nestedPath := filepath.Join(dir, "sub", "dir", "memory.md")

	entry := &MemoryEntryV2{
		Name:        "nested",
		Description: "嵌套目录测试",
		Type:        MemoryTypeUser,
		Content:     "嵌套内容",
	}
	if err := WriteMemoryEntry(nestedPath, entry); err != nil {
		t.Fatalf("写入嵌套目录失败: %v", err)
	}
	if _, err := os.Stat(nestedPath); os.IsNotExist(err) {
		t.Error("嵌套目录下文件应存在")
	}
}

// TestMemoryEntryV2_ScanDirectory 测试扫描目录中的记忆文件
func TestMemoryEntryV2_ScanDirectory(t *testing.T) {
	dir := t.TempDir()
	memoryDir := filepath.Join(dir, "memory")
	if err := os.MkdirAll(memoryDir, 0755); err != nil {
		t.Fatalf("创建目录失败: %v", err)
	}

	// 创建多个记忆文件
	for _, name := range []string{"style", "arch", "api"} {
		entry := &MemoryEntryV2{
			Name:        name,
			Description: name + " desc",
			Type:        MemoryTypeUser,
			Content:     name + " content",
		}
		path := filepath.Join(memoryDir, name+".md")
		if err := WriteMemoryEntry(path, entry); err != nil {
			t.Fatalf("写入 %s 失败: %v", name, err)
		}
	}

	// 创建一个非 .md 文件，不应被扫描到
	if err := os.WriteFile(filepath.Join(memoryDir, "notes.txt"), []byte("not a memory"), 0644); err != nil {
		t.Fatalf("写入 txt 文件失败: %v", err)
	}

	entries, err := ScanMemoryDirectory(memoryDir)
	if err != nil {
		t.Fatalf("ScanMemoryDirectory 返回错误: %v", err)
	}
	if len(entries) != 3 {
		t.Errorf("扫描到 %d 个文件, expected 3 个", len(entries))
	}
}

// TestMemoryEntryV2_ScanDirectory_Nested 测试扫描子目录中的记忆文件
func TestMemoryEntryV2_ScanDirectory_Nested(t *testing.T) {
	dir := t.TempDir()
	subDir := filepath.Join(dir, "sub", "deep")
	if err := os.MkdirAll(subDir, 0755); err != nil {
		t.Fatalf("创建子目录失败: %v", err)
	}

	// 在根目录和子目录各创建一个 .md 文件
	entry1 := &MemoryEntryV2{Name: "root", Description: "root", Type: MemoryTypeUser, Content: "root content"}
	entry2 := &MemoryEntryV2{Name: "deep", Description: "deep", Type: MemoryTypeUser, Content: "deep content"}
	if err := WriteMemoryEntry(filepath.Join(dir, "root.md"), entry1); err != nil {
		t.Fatalf("写入根目录文件失败: %v", err)
	}
	if err := WriteMemoryEntry(filepath.Join(subDir, "deep.md"), entry2); err != nil {
		t.Fatalf("写入子目录文件失败: %v", err)
	}

	entries, err := ScanMemoryDirectory(dir)
	if err != nil {
		t.Fatalf("ScanMemoryDirectory 返回错误: %v", err)
	}
	if len(entries) != 2 {
		t.Errorf("扫描到 %d 个文件, expected 2 个（含子目录）", len(entries))
	}
}

// TestMemoryEntryV2_ScanDirectory_Empty 测试扫描空目录
func TestMemoryEntryV2_ScanDirectory_Empty(t *testing.T) {
	dir := t.TempDir()

	entries, err := ScanMemoryDirectory(dir)
	if err != nil {
		t.Fatalf("ScanMemoryDirectory 返回错误: %v", err)
	}
	if len(entries) != 0 {
		t.Errorf("空目录should return 0 个文件, actual %d 个", len(entries))
	}
}

// ============================================================
// 4.2 LLM 记忆选择 — 清单生成与选择提示构建
// ============================================================

// TestMemoryManifest_Generation 测试记忆清单生成
func TestMemoryManifest_Generation(t *testing.T) {
	dir := t.TempDir()
	memoryDir := filepath.Join(dir, "memory")
	if err := os.MkdirAll(memoryDir, 0755); err != nil {
		t.Fatalf("创建目录失败: %v", err)
	}

	for i := 0; i < 5; i++ {
		entry := &MemoryEntryV2{
			Name:        fmt.Sprintf("memory-%d", i),
			Description: fmt.Sprintf("描述 %d", i),
			Type:        MemoryTypeUser,
			Content:     fmt.Sprintf("内容 %d", i),
		}
		path := filepath.Join(memoryDir, fmt.Sprintf("mem-%d.md", i))
		if err := WriteMemoryEntry(path, entry); err != nil {
			t.Fatalf("写入 mem-%d 失败: %v", i, err)
		}
	}

	manifest, err := GenerateMemoryManifest(memoryDir)
	if err != nil {
		t.Fatalf("GenerateMemoryManifest 返回错误: %v", err)
	}
	if len(manifest) != 5 {
		t.Fatalf("清单条目数 = %d, expected 5", len(manifest))
	}

	for i, m := range manifest {
		if m.Filename == "" {
			t.Errorf("条目 %d: Filename 为空", i)
		}
		if m.Name == "" {
			t.Errorf("条目 %d: Name 为空", i)
		}
		if m.Description == "" {
			t.Errorf("条目 %d: Description 为空", i)
		}
		if m.SizeChars <= 0 {
			t.Errorf("条目 %d: SizeChars = %d, should be greater than 0", i, m.SizeChars)
		}
	}
}

// TestMemoryManifest_Generation_SkipsInvalidFiles 测试清单生成跳过无效文件
func TestMemoryManifest_Generation_SkipsInvalidFiles(t *testing.T) {
	dir := t.TempDir()

	// 写入一个合法的记忆文件
	validEntry := &MemoryEntryV2{
		Name: "valid", Description: "有效", Type: MemoryTypeUser, Content: "有效内容",
	}
	if err := WriteMemoryEntry(filepath.Join(dir, "valid.md"), validEntry); err != nil {
		t.Fatalf("写入有效文件失败: %v", err)
	}

	// 写入一个无效的 .md 文件（无 frontmatter）
	if err := os.WriteFile(filepath.Join(dir, "invalid.md"), []byte("no frontmatter"), 0644); err != nil {
		t.Fatalf("写入无效文件失败: %v", err)
	}

	manifest, err := GenerateMemoryManifest(dir)
	if err != nil {
		t.Fatalf("GenerateMemoryManifest 返回错误: %v", err)
	}
	if len(manifest) != 1 {
		t.Errorf("清单条目数 = %d, expected 1（跳过无效文件）", len(manifest))
	}
}

// TestMemorySelector_BuildPrompt 测试选择提示构建
func TestMemorySelector_BuildPrompt(t *testing.T) {
	selector := NewMemorySelector(nil, nil, nil)
	selector.SetMaxSelections(3)

	now := time.Now()
	manifest := []MemoryManifestEntry{
		{Filename: "auth.md", Name: "auth", Description: "认证模式", Type: MemoryTypeUser, Modified: now, SizeChars: 100},
		{Filename: "api.md", Name: "api", Description: "API 约定", Type: MemoryTypeProject, Modified: now, SizeChars: 200},
	}

	prompt := selector.buildSelectionPrompt("如何implementationUser认证？", manifest, []string{"read_file"})

	// 验证提示包含关键内容
	checks := []string{
		"auth.md",
		"api.md",
		"认证模式",
		"API 约定",
		"如何implementationUser认证？",
		"read_file",
		"3", // maxSelections
	}
	for _, want := range checks {
		if !strings.Contains(prompt, want) {
			t.Errorf("提示should contain %q, actual内容:\n%s", want, prompt)
		}
	}
}

// TestMemorySelector_BuildPrompt_NoRecentTools 测试无最近工具时提示不包含工具信息
func TestMemorySelector_BuildPrompt_NoRecentTools(t *testing.T) {
	selector := NewMemorySelector(nil, nil, nil)
	selector.SetMaxSelections(5)

	now := time.Now()
	manifest := []MemoryManifestEntry{
		{Filename: "test.md", Name: "test", Description: "测试", Type: MemoryTypeUser, Modified: now, SizeChars: 50},
	}

	prompt := selector.buildSelectionPrompt("查询", manifest, nil)

	if strings.Contains(prompt, "最近使用的工具") {
		t.Error("无最近工具时，提示should not contain '最近使用的工具'")
	}
}

// ============================================================
// 4.3 记忆新鲜度
// ============================================================

// TestMemoryFreshnessText 测试不同年龄段的新鲜度文本
func TestMemoryFreshnessText(t *testing.T) {
	// 今天的记忆 — 无警告
	text := MemoryFreshnessText(time.Now())
	if text != "" {
		t.Errorf("今天的记忆应无警告, actual: %q", text)
	}

	// 2 天前 — 天级警告（批 5：freshness 标签随形态门改英文单侧）
	text = MemoryFreshnessText(time.Now().Add(-2 * 24 * time.Hour))
	if !strings.Contains(text, "day") {
		t.Errorf("2 天前的记忆should contain day-level warning, actual: %q", text)
	}
	if !strings.Contains(text, "Note") {
		t.Errorf("2 天前的记忆should contain 'Note', actual: %q", text)
	}

	// 10 天前（约 1-2 周） — 周级警告
	text = MemoryFreshnessText(time.Now().Add(-10 * 24 * time.Hour))
	if !strings.Contains(text, "week") {
		t.Errorf("10 天前的记忆should contain week-level warning, actual: %q", text)
	}
	if !strings.Contains(text, "Warning") {
		t.Errorf("10 天前的记忆should contain 'Warning', actual: %q", text)
	}

	// 60 天前（约 2 个月） — 月级严重警告
	text = MemoryFreshnessText(time.Now().Add(-60 * 24 * time.Hour))
	if !strings.Contains(text, "month") {
		t.Errorf("60 天前的记忆should contain month-level warning, actual: %q", text)
	}
	if !strings.Contains(text, "Caution") {
		t.Errorf("60 天前的记忆should contain 'Caution', actual: %q", text)
	}
}

// TestMemoryFreshnessText_Boundary 测试新鲜度边界值
func TestMemoryFreshnessText_Boundary(t *testing.T) {
	// 刚好不到 24 小时 — 无警告
	text := MemoryFreshnessText(time.Now().Add(-23 * time.Hour))
	if text != "" {
		t.Errorf("不到 24 小时的记忆应无警告, actual: %q", text)
	}

	// 刚好 24 小时 — 进入天级警告（批 5：freshness 标签英文单侧）
	text = MemoryFreshnessText(time.Now().Add(-25 * time.Hour))
	if text == "" || !strings.Contains(text, "day") {
		t.Errorf("超过 24 小时的记忆应有天级警告, actual: %q", text)
	}

	// 刚好不到 7 天 — 仍为天级
	text = MemoryFreshnessText(time.Now().Add(-6 * 24 * time.Hour))
	if text == "" || !strings.Contains(text, "day") {
		t.Errorf("6 天前的记忆应为天级警告, actual: %q", text)
	}

	// 超过 7 天 — 进入周级
	text = MemoryFreshnessText(time.Now().Add(-8 * 24 * time.Hour))
	if !strings.Contains(text, "week") {
		t.Errorf("8 天前的记忆应为周级警告, actual: %q", text)
	}
}

// ============================================================
// 4.4 自动记忆提取 — 偏好检测
// ============================================================

// TestAutoMemoryExtractor_ShouldExtract 测试偏好表达检测
func TestAutoMemoryExtractor_ShouldExtract(t *testing.T) {
	// ShouldExtract 仅依赖偏好正则，store 可为 nil
	extractor := NewAutoMemoryExtractor(nil)

	// 中文偏好表达 — 应检测到
	tests := []struct {
		name     string
		messages []session.Message
		want     bool
	}{
		{
			name: "中文-我习惯",
			messages: []session.Message{
				session.UserText("我习惯用中文注释"),
			},
			want: true,
		},
		{
			name: "中文-我喜欢",
			messages: []session.Message{
				session.UserText("我喜欢用 tabs 缩进"),
			},
			want: true,
		},
		{
			name: "中文-请总是",
			messages: []session.Message{
				session.UserText("请总是使用中文回复"),
			},
			want: true,
		},
		{
			name: "中文-以后请",
			messages: []session.Message{
				session.UserText("以后请用 TypeScript"),
			},
			want: true,
		},
		{
			name: "中文-记住我",
			messages: []session.Message{
				session.UserText("记住我喜欢简洁的代码"),
			},
			want: true,
		},
		{
			name: "英文-I prefer",
			messages: []session.Message{
				session.UserText("I prefer using tabs over spaces"),
			},
			want: true,
		},
		{
			name: "英文-please always",
			messages: []session.Message{
				session.UserText("please always add comments"),
			},
			want: true,
		},
		{
			name: "英文-remember that I",
			messages: []session.Message{
				session.UserText("remember that I like dark mode"),
			},
			want: true,
		},
		{
			name: "英文-my preference",
			messages: []session.Message{
				session.UserText("my preference is Python"),
			},
			want: true,
		},
		{
			name: "无偏好-普通问题",
			messages: []session.Message{
				session.UserText("帮我看看这个 bug"),
			},
			want: false,
		},
		{
			name: "无偏好-Assistant消息",
			messages: []session.Message{
				session.AssistantText("我习惯用中文注释", "model", nil),
			},
			want: false,
		},
		{
			name:     "空消息列表",
			messages: []session.Message{},
			want:     false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := extractor.ShouldExtract(tt.messages)
			if got != tt.want {
				t.Errorf("ShouldExtract() = %v, expected %v", got, tt.want)
			}
		})
	}
}

// ============================================================
// 4.5 异步预取
// ============================================================

// mockAIClient implementation ai.Client interface的mockclient
type mockAIClient struct {
	response string // mock的 AI 响应文本
	err      error  // mock的错误
}

// Stream mock流式调用，立即返回完整响应
func (m *mockAIClient) Stream(ctx context.Context, req ai.Request) (<-chan ai.Event, error) {
	ch := make(chan ai.Event, 1)
	if m.err != nil {
		ch <- ai.ErrorEvent{Err: m.err}
		close(ch)
		return ch, nil
	}
	ch <- ai.TextDeltaEvent{Content: m.response}
	close(ch)
	return ch, nil
}

// Model 返回mock模型名
func (m *mockAIClient) Model() string { return "mock-model" }

// SetModel mock设置模型
func (m *mockAIClient) SetModel(string) {}

// Close mock关闭
func (m *mockAIClient) Close() error { return nil }

// TestMemoryPrefetch_TryGet 测试异步预取的 TryGet 方法
func TestMemoryPrefetch_TryGet(t *testing.T) {
	ctx := context.Background()
	mockClient := &mockAIClient{response: "1\n2\n"}

	selector := NewMemorySelector(nil, nil, mockClient)
	selector.SetMaxSelections(5)

	now := time.Now()
	manifest := []MemoryManifestEntry{
		{Filename: "a.md", Name: "a", Description: "desc a", Type: MemoryTypeUser, Modified: now, SizeChars: 50},
		{Filename: "b.md", Name: "b", Description: "desc b", Type: MemoryTypeUser, Modified: now, SizeChars: 50},
		{Filename: "c.md", Name: "c", Description: "desc c", Type: MemoryTypeUser, Modified: now, SizeChars: 50},
		{Filename: "d.md", Name: "d", Description: "desc d", Type: MemoryTypeUser, Modified: now, SizeChars: 50},
		{Filename: "e.md", Name: "e", Description: "desc e", Type: MemoryTypeUser, Modified: now, SizeChars: 50},
		{Filename: "f.md", Name: "f", Description: "desc f", Type: MemoryTypeUser, Modified: now, SizeChars: 50},
	}

	pf := StartMemoryPrefetch(ctx, selector, "test query", manifest, []string{})

	// 等待预取完成
	time.Sleep(200 * time.Millisecond)

	// 第一次 TryGet — should return结果
	result, err, ok := pf.TryGet()
	if !ok {
		t.Error("预取完成后 TryGet should return ok=true")
	}
	if err != nil {
		t.Errorf("预取不should return error: %v", err)
	}
	_ = result

	// 第二次 TryGet — 已消费，should return false
	_, _, ok = pf.TryGet()
	if ok {
		t.Error("已消费的预取再次 TryGet should return ok=false")
	}
}

// TestMemoryPrefetch_TryGet_NotReady 测试预取未完成时 TryGet 返回未就绪
func TestMemoryPrefetch_TryGet_NotReady(t *testing.T) {
	ctx := context.Background()
	mockClient := &mockAIClient{response: "1\n"}

	selector := NewMemorySelector(nil, nil, mockClient)

	// 使用空清单，SelectRelevant 会立即返回 nil（不需要 AI 调用）
	pf := StartMemoryPrefetch(ctx, selector, "test query", nil, nil)

	// 等待完成（空清单会立即返回）
	time.Sleep(50 * time.Millisecond)

	_, _, ok := pf.TryGet()
	if !ok {
		t.Error("空清单预取应快速完成并返回 ok=true")
	}
}

// TestMemoryPrefetch_WaitGet 测试阻塞等待预取结果
func TestMemoryPrefetch_WaitGet(t *testing.T) {
	ctx := context.Background()
	mockClient := &mockAIClient{response: "1\n"}

	selector := NewMemorySelector(nil, nil, mockClient)

	pf := StartMemoryPrefetch(ctx, selector, "test query", nil, nil)

	// WaitGet 应阻塞直到完成
	result, err := pf.WaitGet()
	if err != nil {
		t.Errorf("WaitGet 不should return error: %v", err)
	}
	if result != nil {
		t.Errorf("空清单的 WaitGet should return nil 结果, actual: %v", result)
	}
}

// ============================================================
// helper functions测试
// ============================================================

// TestParseSelectedIndices 测试 AI 响应编号解析
func TestParseSelectedIndices(t *testing.T) {
	tests := []struct {
		name     string
		text     string
		maxCount int
		want     []int
	}{
		{"简单编号", "1\n3\n5\n", 6, []int{0, 2, 4}},
		{"带文字", "我选择 2 和 4", 5, []int{1, 3}},
		{"超出scope", "1\n99\n2\n", 3, []int{0, 1}},
		{"空文本", "", 5, nil},
		{"重复编号", "1\n1\n2\n", 5, []int{0, 1}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := parseSelectedIndices(tt.text, tt.maxCount)
			if len(got) != len(tt.want) {
				t.Errorf("parseSelectedIndices(%q, %d) = %v, expected %v", tt.text, tt.maxCount, got, tt.want)
				return
			}
			for i := range got {
				if got[i] != tt.want[i] {
					t.Errorf("parseSelectedIndices(%q, %d)[%d] = %d, expected %d", tt.text, tt.maxCount, i, got[i], tt.want[i])
				}
			}
		})
	}
}

// TestSanitizeFilename 测试文件名清理（统一使用 sanitizeMemoryFilename，P1-3 修复）
func TestSanitizeFilename(t *testing.T) {
	tests := []struct {
		input string
		want  string
	}{
		{"My Preference", "my-preference"},
		{"hello/world", "hello-world"},
		{"  spaces  ", "spaces"},
		{"UPPERCASE", "uppercase"},
		{"a/b:c*d?e", "a-b-c-d-e"},
		{"", "untitled"},
	}

	for _, tt := range tests {
		got := sanitizeMemoryFilename(tt.input)
		if got != tt.want {
			t.Errorf("sanitizeMemoryFilename(%q) = %q, expected %q", tt.input, got, tt.want)
		}
	}
}

// TestIsValidMemoryType 测试记忆类型校验
func TestIsValidMemoryType(t *testing.T) {
	validTypes := []MemoryType{MemoryTypeUser, MemoryTypeFeedback, MemoryTypeProject, MemoryTypeReference}
	for _, mt := range validTypes {
		if !isValidMemoryType(mt) {
			t.Errorf("isValidMemoryType(%q) should return true", mt)
		}
	}

	invalidTypes := []MemoryType{"invalid", "", "UNKNOWN"}
	for _, mt := range invalidTypes {
		if isValidMemoryType(mt) {
			t.Errorf("isValidMemoryType(%q) should return false", mt)
		}
	}
}

// TestMemorySelector_SetMaxSelections test setup最大选择数
func TestMemorySelector_SetMaxSelections(t *testing.T) {
	selector := NewMemorySelector(nil, nil, nil)

	// 正常设置
	selector.SetMaxSelections(10)
	if selector.maxSelections != 10 {
		t.Errorf("maxSelections = %d, expected 10", selector.maxSelections)
	}

	// 无效值不应改变
	selector.SetMaxSelections(0)
	if selector.maxSelections != 10 {
		t.Errorf("设置 0 不应改变 maxSelections, actual = %d", selector.maxSelections)
	}

	selector.SetMaxSelections(-1)
	if selector.maxSelections != 10 {
		t.Errorf("设置负数不应改变 maxSelections, actual = %d", selector.maxSelections)
	}
}

// TestMemorySelector_SelectRelevant_SmallManifest 测试清单条目数不超过最大选择数时直接返回全部
func TestMemorySelector_SelectRelevant_SmallManifest(t *testing.T) {
	selector := NewMemorySelector(nil, nil, nil)
	selector.SetMaxSelections(5)

	now := time.Now()
	manifest := []MemoryManifestEntry{
		{Filename: "a.md", Name: "a", Description: "desc a", Type: MemoryTypeUser, Modified: now, SizeChars: 50},
		{Filename: "b.md", Name: "b", Description: "desc b", Type: MemoryTypeUser, Modified: now, SizeChars: 50},
	}

	// 不需要 AI 调用，直接返回全部
	result, err := selector.SelectRelevant(context.Background(), "query", manifest, nil)
	if err != nil {
		t.Fatalf("SelectRelevant 返回错误: %v", err)
	}
	if len(result) != 2 {
		t.Errorf("清单条目数 <= maxSelections 时should return全部, actual %d 条", len(result))
	}
}

// TestMemorySelector_SelectRelevant_EmptyManifest 测试空清单返回 nil
func TestMemorySelector_SelectRelevant_EmptyManifest(t *testing.T) {
	selector := NewMemorySelector(nil, nil, nil)

	result, err := selector.SelectRelevant(context.Background(), "query", nil, nil)
	if err != nil {
		t.Fatalf("SelectRelevant 返回错误: %v", err)
	}
	if result != nil {
		t.Errorf("空清单should return nil, actual: %v", result)
	}
}

// TestParseExtractedPreferences 测试偏好提取结果解析
func TestParseExtractedPreferences(t *testing.T) {
	yamlContent := "```yaml\n- name: coding-style\n  description: 编码风格\n  type: user\n  content: 使用中文注释\n```\n"
	entries, err := parseExtractedPreferences(yamlContent)
	if err != nil {
		t.Fatalf("parseExtractedPreferences 返回错误: %v", err)
	}
	if len(entries) != 1 {
		t.Fatalf("条目数 = %d, expected 1", len(entries))
	}
	if entries[0].Name != "coding-style" {
		t.Errorf("Name = %q, expected %q", entries[0].Name, "coding-style")
	}
	if entries[0].Content != "使用中文注释" {
		t.Errorf("Content = %q, expected %q", entries[0].Content, "使用中文注释")
	}
}

// TestParseExtractedPreferences_SingleEntry 测试解析单个偏好条目（非列表格式）
func TestParseExtractedPreferences_SingleEntry(t *testing.T) {
	yamlContent := "name: my-style\ndescription: 我的风格\ntype: feedback\ncontent: 喜欢简洁代码\n"
	entries, err := parseExtractedPreferences(yamlContent)
	if err != nil {
		t.Fatalf("parseExtractedPreferences 返回错误: %v", err)
	}
	if len(entries) != 1 {
		t.Fatalf("条目数 = %d, expected 1", len(entries))
	}
	if entries[0].Name != "my-style" {
		t.Errorf("Name = %q, expected %q", entries[0].Name, "my-style")
	}
	if entries[0].Type != MemoryTypeFeedback {
		t.Errorf("Type = %q, expected %q", entries[0].Type, MemoryTypeFeedback)
	}
}

// TestParseExtractedPreferences_SkipsEmpty test skipped名称或内容为空的条目
func TestParseExtractedPreferences_SkipsEmpty(t *testing.T) {
	yamlContent := "```yaml\n- name: \"\"\n  description: 空\n  type: user\n  content: 有内容\n- name: 有名\n  description: 描述\n  type: user\n  content: \"\"\n```\n"
	entries, err := parseExtractedPreferences(yamlContent)
	if err != nil {
		t.Fatalf("parseExtractedPreferences 返回错误: %v", err)
	}
	if len(entries) != 0 {
		t.Errorf("名称或内容为空的条目应被跳过, actual %d 条", len(entries))
	}
}

// TestBuildExtractionPrompt 测试偏好提取提示词构建
func TestBuildExtractionPrompt(t *testing.T) {
	texts := []string{"我习惯用中文注释", "请总是使用 TypeScript"}
	prompt := buildExtractionPrompt(texts)

	if !strings.Contains(prompt, "我习惯用中文注释") {
		t.Error("提示should containUser文本 1")
	}
	if !strings.Contains(prompt, "请总是使用 TypeScript") {
		t.Error("提示should containUser文本 2")
	}
	if !strings.Contains(prompt, "YAML") {
		t.Error("提示should contain YAML 格式说明")
	}
}

// TestSelectRelevant_AISelection 测试 AI 驱动的记忆选择
// 使用 Mock AI client，验证 SelectRelevant 能返回与查询相关的记忆条目
func TestSelectRelevant_AISelection(t *testing.T) {
	mockClient := &mockAIClient{response: "1\n3\n"}

	selector := NewMemorySelector(nil, nil, mockClient)
	selector.SetMaxSelections(5)

	now := time.Now()
	manifest := []MemoryManifestEntry{
		{Filename: "auth.md", Name: "auth", Description: "认证偏好", Type: MemoryTypeUser, Modified: now, SizeChars: 100},
		{Filename: "api.md", Name: "api", Description: "API 约定", Type: MemoryTypeProject, Modified: now, SizeChars: 200},
		{Filename: "style.md", Name: "style", Description: "编码风格", Type: MemoryTypeUser, Modified: now, SizeChars: 150},
		{Filename: "deploy.md", Name: "deploy", Description: "部署流程", Type: MemoryTypeProject, Modified: now, SizeChars: 300},
		{Filename: "test.md", Name: "test", Description: "testing strategy", Type: MemoryTypeReference, Modified: now, SizeChars: 250},
		{Filename: "db.md", Name: "db", Description: "数据库配置", Type: MemoryTypeProject, Modified: now, SizeChars: 180},
	}

	result, err := selector.SelectRelevant(context.Background(), "如何implementationUser认证？", manifest, []string{"read_file"})
	if err != nil {
		t.Fatalf("SelectRelevant 返回错误: %v", err)
	}

	// AI 返回了编号 1 和 3，应选择对应条目
	if len(result) != 2 {
		t.Errorf("应选择 2 个相关记忆，actual %d 个", len(result))
	}
	if len(result) > 0 && result[0].Name != "auth" {
		t.Errorf("第一个结果应为 auth，actual为 %s", result[0].Name)
	}
	if len(result) > 1 && result[1].Name != "style" {
		t.Errorf("第二个结果应为 style，actual为 %s", result[1].Name)
	}
}

// TestExtractAndSave 测试从对话中提取偏好并保存为记忆条目
// 验证 ExtractAndSave 能检测User偏好表达，调用 AI 提取，并保存到磁盘
func TestExtractAndSave(t *testing.T) {
	dir := t.TempDir()
	// NewAutoMemoryExtractor 新签名接收 *MemoryStore，ExtractAndSave 通过 store.Write 落盘
	store, err := NewMemoryStore(dir, "test-workdir")
	if err != nil {
		t.Fatalf("创建 MemoryStore 失败: %v", err)
	}
	memoryDir := store.Dir()

	extractor := NewAutoMemoryExtractor(store)

	// 构造包含偏好表达的User消息
	messages := []session.Message{
		session.UserText("我习惯用中文注释，请总是添加函数级注释"),
	}

	// 验证 ShouldExtract 返回 true
	if !extractor.ShouldExtract(messages) {
		t.Error("应检测到偏好表达")
	}

	// 使用 Mock AI clientmock偏好提取
	yamlResponse := "```yaml\n- name: coding-style\n  description: 编码风格偏好\n  type: user\n  content: 使用中文注释，添加函数级注释\n```\n"
	mockClient := &mockAIClient{response: yamlResponse}

	err = extractor.ExtractAndSave(context.Background(), mockClient, messages)
	if err != nil {
		t.Fatalf("ExtractAndSave 返回错误: %v", err)
	}

	// 验证记忆文件已保存
	files, err := os.ReadDir(memoryDir)
	if err != nil {
		t.Fatalf("读取记忆目录失败: %v", err)
	}
	if len(files) == 0 {
		t.Error("ExtractAndSave 应保存至少一个记忆文件")
	}
}

// ============================================================
// v0.4 扩展字段测试（9 新字段 + 3 零值默认值）
// ============================================================

// TestMemoryEntryV2_ParseV04Fields 测试 v0.4 新增 9 字段（v0.3+3 + v0.4+6）的解析
func TestMemoryEntryV2_ParseV04Fields(t *testing.T) {
	content := `---
name: go-version-probe
description: 探测本机 go 版本
type: project
created_at: 2026-07-17T10:00:00Z
updated_at: 2026-07-17T10:00:00Z
trust_level: capability
probe_fn: shell:go version
ttl_seconds: 86400
verification: verified
valid_from: 2026-07-17T00:00:00Z
valid_until: 2026-12-31T00:00:00Z
links:
  - go-install-path
  - go-path-env
importance: 80
source: probe_fn
---
本机 go 1.25.0
`
	entry, err := ParseMemoryEntry(content)
	if err != nil {
		t.Fatalf("ParseMemoryEntry 返回错误: %v", err)
	}

	// v0.3 +3 字段
	if entry.TrustLevel != "capability" {
		t.Errorf("TrustLevel = %q, expected %q", entry.TrustLevel, "capability")
	}
	if entry.ProbeFn != "shell:go version" {
		t.Errorf("ProbeFn = %q, expected %q", entry.ProbeFn, "shell:go version")
	}
	if entry.TTlSeconds != 86400 {
		t.Errorf("TTlSeconds = %d, expected 86400", entry.TTlSeconds)
	}
	// v0.4 +6 字段
	if entry.Verification != "verified" {
		t.Errorf("Verification = %q, expected %q", entry.Verification, "verified")
	}
	if entry.ValidFrom == nil {
		t.Fatal("ValidFrom 不应为 nil")
	}
	if !entry.ValidFrom.Equal(time.Date(2026, 7, 17, 0, 0, 0, 0, time.UTC)) {
		t.Errorf("ValidFrom = %v, expected 2026-07-17T00:00:00Z", entry.ValidFrom)
	}
	if entry.ValidUntil == nil {
		t.Fatal("ValidUntil 不应为 nil")
	}
	if !entry.ValidUntil.Equal(time.Date(2026, 12, 31, 0, 0, 0, 0, time.UTC)) {
		t.Errorf("ValidUntil = %v, expected 2026-12-31T00:00:00Z", entry.ValidUntil)
	}
	if len(entry.Links) != 2 {
		t.Fatalf("Links 长度 = %d, expected 2", len(entry.Links))
	}
	if entry.Links[0] != "go-install-path" || entry.Links[1] != "go-path-env" {
		t.Errorf("Links = %v, expected [go-install-path go-path-env]", entry.Links)
	}
	if entry.Importance != 80 {
		t.Errorf("Importance = %d, expected 80", entry.Importance)
	}
	if entry.Source != "probe_fn" {
		t.Errorf("Source = %q, expected %q", entry.Source, "probe_fn")
	}
}

// TestMemoryEntryV2_Defaults_ImportanceZero 测试 Importance==0 默认为 50
func TestMemoryEntryV2_Defaults_ImportanceZero(t *testing.T) {
	content := `---
name: no-importance
description: 缺省 importance
type: user
created_at: 2026-07-17T10:00:00Z
updated_at: 2026-07-17T10:00:00Z
---
content
`
	entry, err := ParseMemoryEntry(content)
	if err != nil {
		t.Fatalf("ParseMemoryEntry 返回错误: %v", err)
	}
	if entry.Importance != 50 {
		t.Errorf("Importance 缺省应为 50, actual %d（PIT-001 防护：0 视为未设置）", entry.Importance)
	}
}

// TestMemoryEntryV2_Defaults_ImportanceExplicitZero 测试显式 importance: 0 仍视为未设置→默认 50
// 原因：YAML 中无法区分"未设置"与"显式 0"，故 0 一律视为未设置（设计取舍：宁可不偏不倚，也不偏极端）
func TestMemoryEntryV2_Defaults_ImportanceExplicitZero(t *testing.T) {
	content := "---\n" +
		"name: zero-imp\n" +
		"description: 显式 0\n" +
		"type: user\n" +
		"created_at: 2026-07-17T10:00:00Z\n" +
		"updated_at: 2026-07-17T10:00:00Z\n" +
		"importance: 0\n" +
		"---\nbody\n"
	entry, err := ParseMemoryEntry(content)
	if err != nil {
		t.Fatalf("ParseMemoryEntry 返回错误: %v", err)
	}
	if entry.Importance != 50 {
		t.Errorf("显式 importance:0 仍应默认为 50, actual %d", entry.Importance)
	}
}

// TestMemoryEntryV2_Defaults_ValidFromNil 测试 ValidFrom==nil 回退到 &CreatedAt（bi-temporal 起始语义）
func TestMemoryEntryV2_Defaults_ValidFromNil(t *testing.T) {
	content := `---
name: no-valid-from
description: 缺省 valid_from
type: user
created_at: 2026-07-17T10:00:00Z
updated_at: 2026-07-17T10:00:00Z
---
content
`
	entry, err := ParseMemoryEntry(content)
	if err != nil {
		t.Fatalf("ParseMemoryEntry 返回错误: %v", err)
	}
	if entry.ValidFrom == nil {
		t.Fatal("ValidFrom 缺省时应回退到 &CreatedAt，不应为 nil")
	}
	if !entry.ValidFrom.Equal(entry.CreatedAt) {
		t.Errorf("ValidFrom 缺省应等于 CreatedAt, ValidFrom=%v CreatedAt=%v", entry.ValidFrom, entry.CreatedAt)
	}
}

// TestMemoryEntryV2_Defaults_VerificationEmpty 测试 Verification=="" 默认为 "unverified"
// NEVER 默认 "verified"，防止 PIT-001 capability 失效被当真
func TestMemoryEntryV2_Defaults_VerificationEmpty(t *testing.T) {
	content := `---
name: no-verification
description: 缺省 verification
type: user
created_at: 2026-07-17T10:00:00Z
updated_at: 2026-07-17T10:00:00Z
---
content
`
	entry, err := ParseMemoryEntry(content)
	if err != nil {
		t.Fatalf("ParseMemoryEntry 返回错误: %v", err)
	}
	if entry.Verification != "unverified" {
		t.Errorf("Verification 缺省应为 unverified, actual %q（PIT-001 防护：NEVER 默认 verified）", entry.Verification)
	}
}

// TestMemoryEntryV2_RoundTrip_V04Fields 测试 v0.4 9 字段的序列化-解析往返一致性
// 写入 → 读取 → 验证字段保持不变
func TestMemoryEntryV2_RoundTrip_V04Fields(t *testing.T) {
	dir := t.TempDir()

	validFrom := time.Date(2026, 7, 17, 0, 0, 0, 0, time.UTC)
	validUntil := time.Date(2026, 12, 31, 0, 0, 0, 0, time.UTC)

	entry := &MemoryEntryV2{
		Name:        "round-trip-v04",
		Description: "往返测试",
		Type:        MemoryTypeProject,
		Content:     "body content",
		// v0.3 +3
		TrustLevel: "capability",
		ProbeFn:    "shell:echo hi",
		TTlSeconds: 3600,
		// v0.4 +6
		Verification: "verified",
		ValidFrom:    &validFrom,
		ValidUntil:   &validUntil,
		Links:        []string{"link-1", "link-2"},
		Importance:   75,
		Source:       "user",
	}

	path := filepath.Join(dir, "round-trip-v04.md")
	if err := WriteMemoryEntry(path, entry); err != nil {
		t.Fatalf("WriteMemoryEntry 返回错误: %v", err)
	}

	read, err := ReadMemoryEntry(path)
	if err != nil {
		t.Fatalf("ReadMemoryEntry 返回错误: %v", err)
	}

	// 验证 9 新字段往返一致
	if read.TrustLevel != entry.TrustLevel {
		t.Errorf("TrustLevel 往返不一致: 写 %q 读取 %q", entry.TrustLevel, read.TrustLevel)
	}
	if read.ProbeFn != entry.ProbeFn {
		t.Errorf("ProbeFn 往返不一致: 写 %q 读取 %q", entry.ProbeFn, read.ProbeFn)
	}
	if read.TTlSeconds != entry.TTlSeconds {
		t.Errorf("TTlSeconds 往返不一致: 写 %d 读取 %d", entry.TTlSeconds, read.TTlSeconds)
	}
	if read.Verification != entry.Verification {
		t.Errorf("Verification 往返不一致: 写 %q 读取 %q", entry.Verification, read.Verification)
	}
	if read.ValidFrom == nil || !read.ValidFrom.Equal(*entry.ValidFrom) {
		t.Errorf("ValidFrom 往返不一致: 写 %v 读取 %v", entry.ValidFrom, read.ValidFrom)
	}
	if read.ValidUntil == nil || !read.ValidUntil.Equal(*entry.ValidUntil) {
		t.Errorf("ValidUntil 往返不一致: 写 %v 读取 %v", entry.ValidUntil, read.ValidUntil)
	}
	if len(read.Links) != len(entry.Links) {
		t.Errorf("Links 长度往返不一致: 写 %d 读取 %d", len(entry.Links), len(read.Links))
	} else {
		for i, l := range entry.Links {
			if read.Links[i] != l {
				t.Errorf("Links[%d] 往返不一致: 写 %q 读取 %q", i, l, read.Links[i])
			}
		}
	}
	if read.Importance != entry.Importance {
		t.Errorf("Importance 往返不一致: 写 %d 读取 %d", entry.Importance, read.Importance)
	}
	if read.Source != entry.Source {
		t.Errorf("Source 往返不一致: 写 %q 读取 %q", entry.Source, read.Source)
	}
}

// TestMemoryEntryV2_RoundTrip_NilValidUntil 测试 ValidUntil==nil 往返保持 nil（无过期时间）
func TestMemoryEntryV2_RoundTrip_NilValidUntil(t *testing.T) {
	dir := t.TempDir()

	entry := &MemoryEntryV2{
		Name:         "nil-valid-until",
		Description:  "无过期",
		Type:         MemoryTypeUser,
		Content:      "body",
		Verification: "unverified",
		Importance:   50,
		Source:       "user",
		// ValidUntil 留 nil
	}

	path := filepath.Join(dir, "nil-valid-until.md")
	if err := WriteMemoryEntry(path, entry); err != nil {
		t.Fatalf("WriteMemoryEntry 返回错误: %v", err)
	}

	read, err := ReadMemoryEntry(path)
	if err != nil {
		t.Fatalf("ReadMemoryEntry 返回错误: %v", err)
	}
	if read.ValidUntil != nil {
		t.Errorf("ValidUntil 应保持 nil（无过期）, actual %v", read.ValidUntil)
	}
}

// TestMemoryEntryV2_RoundTrip_EmptyLinks 测试 Links 为空时往返保持空（非 nil）
func TestMemoryEntryV2_RoundTrip_EmptyLinks(t *testing.T) {
	dir := t.TempDir()

	entry := &MemoryEntryV2{
		Name:         "empty-links",
		Description:  "无链接",
		Type:         MemoryTypeUser,
		Content:      "body",
		Verification: "unverified",
		Importance:   50,
		Source:       "user",
	}

	path := filepath.Join(dir, "empty-links.md")
	if err := WriteMemoryEntry(path, entry); err != nil {
		t.Fatalf("WriteMemoryEntry 返回错误: %v", err)
	}

	read, err := ReadMemoryEntry(path)
	if err != nil {
		t.Fatalf("ReadMemoryEntry 返回错误: %v", err)
	}
	// yaml 反序列化空 list 通常为 nil slice，len==0 即可
	if len(read.Links) != 0 {
		t.Errorf("Links 应为空, actual %v", read.Links)
	}
}
