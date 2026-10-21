// merge_layer_test.go — H11/N27 Phase B B2 测试。
// 含致命反例：TestMergeLayer_project_overrides_user（跨 scope 同名污染防护）。
//
// 测试覆盖：
//   - TestMergeLayer_project_overrides_user（致命反例）：project 整文件覆盖 user
//   - TestMergeLayer_no_conflict：两 scope 无同名
//   - TestMergeLayer_empty_user：user 空
//   - TestMergeLayer_empty_project：project 空
//   - TestMergeLayer_both_empty：两 scope 都空
//   - TestMergeLayer_user_nil：userStore 为 nil
//   - TestMergeLayer_project_nil：projectStore 为 nil

package memory

import (
	"strings"
	"testing"
	"time"
)

// writeEntryToStore 写入 entry 到 MemoryStore（测试辅助）。
func writeEntryToStore(t *testing.T, store *MemoryStore, name, description, content, verification string) {
	t.Helper()
	entry := &MemoryEntry{
		Name:         name,
		Description:  description,
		Type:         MemoryTypeUser,
		CreatedAt:    time.Now(),
		Content:      content,
		Verification: verification,
	}
	if err := store.Write(entry); err != nil {
		t.Fatalf("Write %q 失败: %v", name, err)
	}
}

// findEntryByName 从切片中查找指定 name 的 entry（测试辅助）。
func findEntryByName(entries []*MemoryEntry, name string) *MemoryEntry {
	for _, e := range entries {
		if e.Name == name {
			return e
		}
	}
	return nil
}

// TestMergeLayer_project_overrides_user 致命反例（跨 scope 同名污染防护）：
// user scope 有 {"name": "build-cmd", "description": "user version", "content": "user content"},
// project scope 有 {"name": "build-cmd", "description": "project version", "content": "project content"},
// ListMerged 返回 1 条且 description="project version" + content="project content"（整文件覆盖，非字段合并）。
//
// 关键反例验证：
//   - 不返回 2 条（去重生效）
//   - description 是 project 版本（整文件覆盖，非 user 的）
//   - content 是 project 版本（Markdown body 也整体覆盖，非 user 的）
//   - verification 是 project 版本（所有字段都覆盖）
func TestMergeLayer_project_overrides_user(t *testing.T) {
	baseDir := newTestBaseDir(t)

	userStore, err := NewMemoryStoreForScope(baseDir, "user", false)
	if err != nil {
		t.Fatalf("NewMemoryStoreForScope(user) 失败: %v", err)
	}
	projectStore, err := NewMemoryStoreForProject(baseDir, "/test/work/override")
	if err != nil {
		t.Fatalf("NewMemoryStoreForProject 失败: %v", err)
	}

	// 写入同名 entry，user 与 project 字段不同
	writeEntryToStore(t, userStore, "build-cmd", "user version", "user content", VerificationVerified)
	writeEntryToStore(t, projectStore, "build-cmd", "project version", "project content", VerificationUnverified)

	ml := NewMergeLayer(userStore, projectStore)
	merged, err := ml.ListMerged()
	if err != nil {
		t.Fatalf("ListMerged 失败: %v", err)
	}

	// 关键反例验证 1：去重生效，仅 1 条 build-cmd
	if len(merged) != 1 {
		t.Errorf("跨 scope 同名去重失效: ListMerged 返回 %d 条 (期望 1)", len(merged))
		for i, e := range merged {
			t.Logf("  merged[%d]: name=%q desc=%q", i, e.Name, e.Description)
		}
		return
	}

	// 关键反例验证 2：整文件覆盖（非字段合并）
	entry := merged[0]
	if entry.Description != "project version" {
		t.Errorf("Description 应为 project 版本 (整文件覆盖): 实际 %q, 期望 %q", entry.Description, "project version")
	}
	if entry.Content != "project content" {
		t.Errorf("Content 应为 project 版本 (Markdown body 整体覆盖): 实际 %q, 期望 %q", entry.Content, "project content")
	}
	if entry.Verification != VerificationUnverified {
		t.Errorf("Verification 应为 project 版本 (所有字段覆盖): 实际 %q, 期望 %q", entry.Verification, VerificationUnverified)
	}
}

// TestMergeLayer_no_conflict 两 scope 无同名，ListMerged 返回全部条目。
func TestMergeLayer_no_conflict(t *testing.T) {
	baseDir := newTestBaseDir(t)

	userStore, _ := NewMemoryStoreForScope(baseDir, "user", false)
	projectStore, _ := NewMemoryStoreForProject(baseDir, "/test/work/no-conflict")

	writeEntryToStore(t, userStore, "user-pref-1", "user pref 1", "user content 1", VerificationVerified)
	writeEntryToStore(t, userStore, "user-pref-2", "user pref 2", "user content 2", VerificationVerified)
	writeEntryToStore(t, projectStore, "project-pref-1", "project pref 1", "project content 1", VerificationUnverified)

	ml := NewMergeLayer(userStore, projectStore)
	merged, err := ml.ListMerged()
	if err != nil {
		t.Fatalf("ListMerged 失败: %v", err)
	}

	if len(merged) != 3 {
		t.Errorf("无冲突场景 ListMerged 返回 %d 条 (期望 3)", len(merged))
	}

	// 验证全部 entry 存在
	for _, name := range []string{"user-pref-1", "user-pref-2", "project-pref-1"} {
		if findEntryByName(merged, name) == nil {
			t.Errorf("ListMerged 缺少 entry: %s", name)
		}
	}
}

// TestMergeLayer_empty_user user scope 空，ListMerged 返回 project 全部。
func TestMergeLayer_empty_user(t *testing.T) {
	baseDir := newTestBaseDir(t)

	userStore, _ := NewMemoryStoreForScope(baseDir, "user", false)
	projectStore, _ := NewMemoryStoreForProject(baseDir, "/test/work/empty-user")

	// user 不写入任何 entry
	writeEntryToStore(t, projectStore, "project-only", "project only", "project content", VerificationVerified)

	ml := NewMergeLayer(userStore, projectStore)
	merged, err := ml.ListMerged()
	if err != nil {
		t.Fatalf("ListMerged 失败: %v", err)
	}

	if len(merged) != 1 {
		t.Errorf("user 空场景 ListMerged 返回 %d 条 (期望 1)", len(merged))
	}
	if merged[0].Name != "project-only" {
		t.Errorf("merged[0].Name = %q, 期望 %q", merged[0].Name, "project-only")
	}
}

// TestMergeLayer_empty_project project scope 空，ListMerged 返回 user 全部。
func TestMergeLayer_empty_project(t *testing.T) {
	baseDir := newTestBaseDir(t)

	userStore, _ := NewMemoryStoreForScope(baseDir, "user", false)
	projectStore, _ := NewMemoryStoreForProject(baseDir, "/test/work/empty-project")

	writeEntryToStore(t, userStore, "user-only", "user only", "user content", VerificationVerified)

	ml := NewMergeLayer(userStore, projectStore)
	merged, err := ml.ListMerged()
	if err != nil {
		t.Fatalf("ListMerged 失败: %v", err)
	}

	if len(merged) != 1 {
		t.Errorf("project 空场景 ListMerged 返回 %d 条 (期望 1)", len(merged))
	}
	if merged[0].Name != "user-only" {
		t.Errorf("merged[0].Name = %q, 期望 %q", merged[0].Name, "user-only")
	}
}

// TestMergeLayer_both_empty 两 scope 都空，ListMerged 返回空切片。
func TestMergeLayer_both_empty(t *testing.T) {
	baseDir := newTestBaseDir(t)

	userStore, _ := NewMemoryStoreForScope(baseDir, "user", false)
	projectStore, _ := NewMemoryStoreForProject(baseDir, "/test/work/both-empty")

	ml := NewMergeLayer(userStore, projectStore)
	merged, err := ml.ListMerged()
	if err != nil {
		t.Fatalf("ListMerged 失败: %v", err)
	}

	if len(merged) != 0 {
		t.Errorf("两 scope 都空场景 ListMerged 返回 %d 条 (期望 0)", len(merged))
	}
}

// TestMergeLayer_user_nil userStore 为 nil，仅返回 project（不 panic）。
func TestMergeLayer_user_nil(t *testing.T) {
	baseDir := newTestBaseDir(t)

	projectStore, _ := NewMemoryStoreForProject(baseDir, "/test/work/user-nil")
	writeEntryToStore(t, projectStore, "only-project", "only project", "project content", VerificationVerified)

	ml := NewMergeLayer(nil, projectStore)
	merged, err := ml.ListMerged()
	if err != nil {
		t.Fatalf("ListMerged(user=nil) 失败: %v", err)
	}

	if len(merged) != 1 {
		t.Errorf("userStore=nil 场景 ListMerged 返回 %d 条 (期望 1)", len(merged))
	}
}

// TestMergeLayer_project_nil projectStore 为 nil，仅返回 user（不 panic）。
func TestMergeLayer_project_nil(t *testing.T) {
	baseDir := newTestBaseDir(t)

	userStore, _ := NewMemoryStoreForScope(baseDir, "user", false)
	writeEntryToStore(t, userStore, "only-user", "only user", "user content", VerificationVerified)

	ml := NewMergeLayer(userStore, nil)
	merged, err := ml.ListMerged()
	if err != nil {
		t.Fatalf("ListMerged(project=nil) 失败: %v", err)
	}

	if len(merged) != 1 {
		t.Errorf("projectStore=nil 场景 ListMerged 返回 %d 条 (期望 1)", len(merged))
	}
}

// TestMergeLayer_both_nil 两 store 都 nil，返回空切片无 error。
func TestMergeLayer_both_nil(t *testing.T) {
	ml := NewMergeLayer(nil, nil)
	merged, err := ml.ListMerged()
	if err != nil {
		t.Fatalf("ListMerged(both nil) 不应返回 error: %v", err)
	}
	if len(merged) != 0 {
		t.Errorf("两 store 都 nil 场景 ListMerged 返回 %d 条 (期望 0)", len(merged))
	}
}

// TestMergeError_Error 验证 MergeError.Error() 输出格式。
func TestMergeError_Error(t *testing.T) {
	me := &MergeError{
		UserErr:    nil,
		ProjectErr: nil,
	}
	if !strings.Contains(me.Error(), "merge layer error") {
		t.Errorf("MergeError.Error() 不含 'merge layer error': %q", me.Error())
	}
}

// TestMergeLayer_multiple_overrides 多个同名 entry 全部由 project 覆盖。
func TestMergeLayer_multiple_overrides(t *testing.T) {
	baseDir := newTestBaseDir(t)

	userStore, _ := NewMemoryStoreForScope(baseDir, "user", false)
	projectStore, _ := NewMemoryStoreForProject(baseDir, "/test/work/multi-override")

	// 写入 3 个同名 entry，user + project 各 1 个 + 1 个仅 user + 1 个仅 project
	writeEntryToStore(t, userStore, "shared-1", "user shared 1", "user content 1", VerificationVerified)
	writeEntryToStore(t, userStore, "user-only", "user only", "user content", VerificationVerified)
	writeEntryToStore(t, projectStore, "shared-1", "project shared 1", "project content 1", VerificationUnverified)
	writeEntryToStore(t, projectStore, "project-only", "project only", "project content", VerificationVerified)

	ml := NewMergeLayer(userStore, projectStore)
	merged, err := ml.ListMerged()
	if err != nil {
		t.Fatalf("ListMerged 失败: %v", err)
	}

	// 期望 3 条（shared-1 被覆盖 + user-only + project-only）
	if len(merged) != 3 {
		t.Errorf("多覆盖场景 ListMerged 返回 %d 条 (期望 3)", len(merged))
	}

	// 验证 shared-1 是 project 版本
	shared := findEntryByName(merged, "shared-1")
	if shared == nil {
		t.Fatal("shared-1 未找到")
	}
	if shared.Description != "project shared 1" {
		t.Errorf("shared-1 应为 project 版本 (整文件覆盖): 实际 %q", shared.Description)
	}

	// 验证 user-only 仍是 user 版本
	userOnly := findEntryByName(merged, "user-only")
	if userOnly == nil {
		t.Fatal("user-only 未找到")
	}
	if userOnly.Description != "user only" {
		t.Errorf("user-only 应为 user 版本: 实际 %q", userOnly.Description)
	}

	// 验证 project-only 是 project 版本
	projectOnly := findEntryByName(merged, "project-only")
	if projectOnly == nil {
		t.Fatal("project-only 未找到")
	}
	if projectOnly.Description != "project only" {
		t.Errorf("project-only 应为 project 版本: 实际 %q", projectOnly.Description)
	}
}
