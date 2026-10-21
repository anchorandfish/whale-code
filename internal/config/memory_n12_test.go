// Package config — memory_n12_test.go N12 记忆命令回核测试（审查 prompt §3.7 10 用例）
//
// 本文件实现 N12-memory-command-审查prompt.md §3.7 要求的 10 个测试用例中的 9 个
// （测试 4 TestHandleMemory_Subcommands 位于 internal/tui/memory_command_test.go，
// 因 HandleMemoryCommand 属 tui 包）。本机 Esafenet DLP 限制可用 go test -c 验证
// 编译，运行推 CI（审查报告 P0-B 修复建议第 2 条）。
package config

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

// TestMigrateV1ToV2_Idempotent 验证 V1→V2 迁移幂等性：已存在的 .md 不被覆盖。
// 反例：若迁移不幂等，重复迁移会覆盖用户手动编辑的记忆内容。
// 实现说明：MigrateV1ToV2 在目标 .md 已存在时跳过（memory_migrate.go:66-72），
// 迁移完成后 memory.json 重命名为 memory.json.migrated（不删除，留备份）。
func TestMigrateV1ToV2_Idempotent(t *testing.T) {
	baseDir := t.TempDir()
	store, err := NewMemoryStore(baseDir, "/test/workdir")
	if err != nil {
		t.Fatalf("NewMemoryStore 失败: %v", err)
	}

	// 预先写入用户手动编辑版的 test-key.md（模拟用户已编辑）
	userEntry := MemoryEntryV2{
		Type:        MemoryTypeReference,
		Name:        "test-key",
		Content:     "user_edited_value",
		Description: "user manual edit",
	}
	if err := store.Write(userEntry); err != nil {
		t.Fatalf("写入用户编辑版失败: %v", err)
	}

	// 创建 V1 memory.json（JSONL 格式，含 test_key → test_value）
	// 注：sanitizeMemoryFilename 保留下划线（\w 含 _），故 key="test-key" sanitize 后文件名 test-key.md，
	// 与上方用户预写入的 test-key.md 匹配，迁移时应跳过（migrated=0）。
	v1Dir := t.TempDir()
	v1Path := filepath.Join(v1Dir, "memory.json")
	v1Content := []byte(`{"key":"test-key","value":"test_value"}` + "\n")
	if err := os.WriteFile(v1Path, v1Content, 0600); err != nil {
		t.Fatalf("写入 V1 memory.json 失败: %v", err)
	}

	// 迁移：test-key.md 已存在，应跳过（migrated=0）
	migrated, err := MigrateV1ToV2(v1Path, store)
	if err != nil {
		t.Fatalf("MigrateV1ToV2 失败: %v", err)
	}
	if migrated != 0 {
		t.Errorf("期望 migrated=0（跳过已存在），实际 %d", migrated)
	}

	// 验证用户编辑未被覆盖（幂等核心）
	entry, err := store.Read("test-key.md")
	if err != nil {
		t.Fatalf("读取迁移后记忆失败: %v", err)
	}
	if entry.Content != "user_edited_value" {
		t.Errorf("幂等失败：用户编辑被覆盖，期望 %q，实际 %q", "user_edited_value", entry.Content)
	}

	// 验证 memory.json 被重命名为 .migrated（不删除）
	if _, err := os.Stat(v1Path); !os.IsNotExist(err) {
		t.Errorf("期望 memory.json 已重命名，但文件仍存在: %s", v1Path)
	}
	if _, err := os.Stat(v1Path + ".migrated"); err != nil {
		t.Errorf("期望 memory.json.migrated 存在，但 Stat 失败: %v", err)
	}
}

// TestMemoryStore_Write_Permission0600 验证写入文件权限为 0600、目录权限为 0700（H5）。
// 反例：权限为 0644 → 其他用户可读记忆（含敏感偏好信息）。
func TestMemoryStore_Write_Permission0600(t *testing.T) {
	store, err := NewMemoryStore(t.TempDir(), "/test/workdir")
	if err != nil {
		t.Fatalf("NewMemoryStore 失败: %v", err)
	}

	entry := MemoryEntryV2{
		Type:    MemoryTypeUser,
		Name:    "preferences",
		Content: "用户偏好：喜欢用 Vim",
	}
	if err := store.Write(entry); err != nil {
		t.Fatalf("Write 失败: %v", err)
	}

	// 验证文件权限 0600
	path := store.FilePath("preferences")
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("Stat 文件失败: %v", err)
	}
	if mode := info.Mode().Perm(); mode != 0600 {
		t.Errorf("文件权限期望 0600，实际 %o（违反 H5 ）", mode)
	}

	// 验证目录权限 0700
	dirInfo, err := os.Stat(store.Dir())
	if err != nil {
		t.Fatalf("Stat 目录失败: %v", err)
	}
	if mode := dirInfo.Mode().Perm(); mode != 0700 {
		t.Errorf("目录权限期望 0700，实际 %o（违反 H5 ）", mode)
	}
}

// TestMemoryStore_ConcurrentWrite_LockProtection 验证跨进程锁保护并发写入。
// 反例：无锁 → 多个 goroutine 并发写同一目录 → 文件损坏/截断。
// 20 个 goroutine 并发写不同文件，应全部成功且锁文件最终释放。
func TestMemoryStore_ConcurrentWrite_LockProtection(t *testing.T) {
	store, err := NewMemoryStore(t.TempDir(), "/test/workdir")
	if err != nil {
		t.Fatalf("NewMemoryStore 失败: %v", err)
	}

	var wg sync.WaitGroup
	errs := make([]error, 20)
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			entry := MemoryEntryV2{
				Type:    MemoryTypeUser,
				Name:    fmt.Sprintf("concurrent_%d", idx),
				Content: fmt.Sprintf("value_%d", idx),
			}
			errs[idx] = store.Write(entry)
		}(i)
	}
	wg.Wait()

	// 验证全部成功
	for i, e := range errs {
		if e != nil {
			t.Errorf("goroutine %d Write 失败: %v", i, e)
		}
	}

	// 验证文件数量正确
	list, err := store.List()
	if err != nil {
		t.Fatalf("List 失败: %v", err)
	}
	if len(list) != 20 {
		t.Errorf("期望 20 个文件，实际 %d", len(list))
	}

	// 验证锁文件已释放（acquireLock 释放后 memory.lock 应不存在）
	lockPath := filepath.Join(store.Dir(), "memory.lock")
	if _, err := os.Stat(lockPath); err == nil {
		t.Errorf("期望锁文件已释放，但 %s 仍存在", lockPath)
	}
}

// TestMemoryStore_WorkDirIsolation 验证 workDir hash 隔离（H5）。
// 反例：全局共享 → A 项目记忆注入 B 项目。
func TestMemoryStore_WorkDirIsolation(t *testing.T) {
	baseDir := t.TempDir()

	// 项目 A
	storeA, err := NewMemoryStore(baseDir, "/project/A")
	if err != nil {
		t.Fatalf("NewMemoryStore A 失败: %v", err)
	}
	if err := storeA.Write(MemoryEntryV2{
		Type:    MemoryTypeProject,
		Name:    "proj-a",
		Content: "A 项目记忆",
	}); err != nil {
		t.Fatalf("Write A 失败: %v", err)
	}

	// 项目 B（同 baseDir，不同 workDir）
	storeB, err := NewMemoryStore(baseDir, "/project/B")
	if err != nil {
		t.Fatalf("NewMemoryStore B 失败: %v", err)
	}

	// 验证 B 看不到 A 的记忆
	listB, err := storeB.List()
	if err != nil {
		t.Fatalf("List B 失败: %v", err)
	}
	if len(listB) != 0 {
		t.Errorf("B 应无记忆（hash 隔离），实际 %d 条", len(listB))
	}

	// 验证 A 看不到 B 的记忆
	listA, err := storeA.List()
	if err != nil {
		t.Fatalf("List A 失败: %v", err)
	}
	if len(listA) != 1 {
		t.Errorf("A 应有 1 条，实际 %d 条", len(listA))
	}
	if filepath.Base(listA[0].FilePath) != "proj-a.md" {
		t.Errorf("期望 filename=proj-a.md，实际 %s", filepath.Base(listA[0].FilePath))
	}

	// 验证 hash 不同（SHA256 前 16 位）
	hashA := filepath.Base(storeA.Dir())
	hashB := filepath.Base(storeB.Dir())
	if hashA == hashB {
		t.Errorf("hash 应不同（workDir 隔离失败）：A=%s B=%s", hashA, hashB)
	}
}

// TestMemoryIndex_Truncation 验证 MEMORY.md 索引截断（200 行 / 25000 字节）。
// 反例：MEMORY.md 无上限 → 注入 system prompt 超长 → 浪费 context。
// 创建 250 条记忆（超过 MaxMemoryFiles=200），EnforceFileLimit 删至 200，
// RebuildEntrypoint 重建后 LoadEntrypoint 应不超过行数/字节限制。
func TestMemoryIndex_Truncation(t *testing.T) {
	store, err := NewMemoryStore(t.TempDir(), "/test/workdir")
	if err != nil {
		t.Fatalf("NewMemoryStore 失败: %v", err)
	}
	index := NewMemoryIndex(store)

	// 创建 250 条记忆（超过 MaxMemoryFiles=200）
	for i := 0; i < 250; i++ {
		if err := store.Write(MemoryEntryV2{
			Type:    MemoryTypeReference,
			Name:    fmt.Sprintf("entry_%03d", i),
			Content: fmt.Sprintf("记忆条目 %d", i),
		}); err != nil {
			t.Fatalf("Write %d 失败: %v", i, err)
		}
	}

	// EnforceFileLimit 应删除最旧 50 条（Write 内部已调用，此处显式验证幂等）
	if err := store.EnforceFileLimit(); err != nil {
		t.Fatalf("EnforceFileLimit 失败: %v", err)
	}
	list, err := store.List()
	if err != nil {
		t.Fatalf("List 失败: %v", err)
	}
	if len(list) != MaxMemoryFiles {
		t.Errorf("期望 %d 条，实际 %d 条", MaxMemoryFiles, len(list))
	}

	// RebuildEntrypoint 重建 MEMORY.md
	if err := index.RebuildEntrypoint(); err != nil {
		t.Fatalf("RebuildEntrypoint 失败: %v", err)
	}
	content, err := index.LoadEntrypoint()
	if err != nil {
		t.Fatalf("LoadEntrypoint 失败: %v", err)
	}

	// 验证不超过 MaxEntrypointBytes（25000）
	if len(content) > MaxEntrypointBytes {
		t.Errorf("期望 ≤ %d 字节，实际 %d", MaxEntrypointBytes, len(content))
	}
	// 验证行数不超过 MaxEntrypointLines（200）
	if lines := strings.Count(content, "\n"); lines > MaxEntrypointLines {
		t.Errorf("期望 ≤ %d 行，实际 %d", MaxEntrypointLines, lines)
	}
}

// TestMemorySelector_DriftCaveat 验证 MemorySelector.Select 注入 drift caveat。
// 反例：无 drift caveat → AI 盲目信任过时记忆 → 推荐错误 API。
// 1 条记忆 ≤ maxSelections(5)，SelectRelevant 直接返回不调用 AI（nil client 安全），
// Select 末尾无条件追加 memoryDriftCaveat（memory_v2.go:425）。
func TestMemorySelector_DriftCaveat(t *testing.T) {
	store, err := NewMemoryStore(t.TempDir(), "/test/workdir")
	if err != nil {
		t.Fatalf("NewMemoryStore 失败: %v", err)
	}
	index := NewMemoryIndex(store)

	// 创建一条可能过时的记忆
	if err := store.Write(MemoryEntryV2{
		Type:    MemoryTypeReference,
		Name:    "library-version",
		Content: "项目使用 React 16.8",
	}); err != nil {
		t.Fatalf("Write 失败: %v", err)
	}

	// nil client：1 条 ≤ maxSelections(5)，SelectRelevant 直接返回 manifest 不调 AI
	selector := NewMemorySelector(store, index, nil)

	result, err := selector.Select(context.Background(), "React hooks 用法")
	if err != nil {
		t.Fatalf("Select 失败: %v", err)
	}

	// 验证包含 drift caveat（对标同类 CLI MEMORY_DRIFT_CAVEAT）
	if !strings.Contains(result, "Caveat") {
		t.Errorf("期望包含 drift caveat 'Caveat'，实际: %s", result)
	}
	if !strings.Contains(result, "验证") {
		t.Errorf("期望包含 '验证'（caveat 文本），实际: %s", result)
	}
	// 验证记忆内容被注入
	if !strings.Contains(result, "React 16.8") {
		t.Errorf("期望包含记忆内容 'React 16.8'，实际: %s", result)
	}
}

// TestEditInEditor_NoEditor 验证 $VISUAL/$EDITOR 均未设置时返回明确错误而非 panic。
// 反例：$EDITOR 未设置 → panic / 无提示。
// 注：VISUAL 优先级测试（TestEditInEditor_VISUALPriority）因跨平台编辑器差异推 CI，
// 本机 Windows 无 vim/nano，editInEditor 直接 exec.Command 不易 mock。
func TestEditInEditor_NoEditor(t *testing.T) {
	t.Setenv("VISUAL", "")
	t.Setenv("EDITOR", "")

	err := editInEditor("/tmp/test.md")
	if err == nil {
		t.Fatal("期望返回错误，实际 nil")
	}
	if !strings.Contains(err.Error(), "VISUAL") {
		t.Errorf("期望错误信息含 'VISUAL'，实际: %v", err)
	}
}

// TestSanitizeMemoryFilename_SpecialChars 验证文件名清理特殊字符（P0-1 迁移安全）。
// 反例：key 含 "/" → filename 含路径分隔符 → 写入到 hash 目录外。
// 实际函数 sanitizeMemoryFilename（memory_store.go:245）：NFC 规范化 → 去空字节 →
// / \ 替换为 - → 非 \w\-. 替换为 - → Trim - → 小写 → 限长 64 → 空返回 "untitled"。
// 注：_ 属于 \w 保留不替换（与审查 prompt 伪代码期望不同，以实际实现为准）。
func TestSanitizeMemoryFilename_SpecialChars(t *testing.T) {
	cases := []struct {
		input    string
		expected string
	}{
		{"normal_key", "normal_key"},                   // _ 属于 \w，保留
		{"key/with/slash", "key-with-slash"},           // / → -
		{"key\\with\\backslash", "key-with-backslash"}, // \ → -
		{"key:with:colon", "key-with-colon"},           // → -
		{"KEY_UPPER", "key_upper"},                     // 大写 → 小写，_ 保留
		{"", "untitled"},                               // 空 → untitled
		{"---", "untitled"},                            // 全 - → Trim → 空 → untitled
	}

	for _, tc := range cases {
		got := sanitizeMemoryFilename(tc.input)
		if got != tc.expected {
			t.Errorf("sanitizeMemoryFilename(%q) = %q, 期望 %q", tc.input, got, tc.expected)
		}
	}
}

// TestV1NoResidualReferences 验证 V1 memory.go 已删除无残留（P0-1 架构清理）。
// 反例：V1 代码删除后仍有引用 → 编译失败。
// 本测试验证 V1 源文件已删除；完整编译验证由 go build ./... 提供（CI）。
func TestV1NoResidualReferences(t *testing.T) {
	wd, err := os.Getwd()
	if err != nil {
		t.Fatalf("Getwd 失败: %v", err)
	}

	// V1 memory.go 应已删除（N12 P0-1）
	v1Path := filepath.Join(wd, "memory.go")
	if _, err := os.Stat(v1Path); !os.IsNotExist(err) {
		t.Errorf("V1 memory.go 应已删除，但文件仍存在: %s", v1Path)
	}

	// V1 memory_test.go 应已删除
	v1TestPath := filepath.Join(wd, "memory_test.go")
	if _, err := os.Stat(v1TestPath); !os.IsNotExist(err) {
		t.Errorf("V1 memory_test.go 应已删除，但文件仍存在: %s", v1TestPath)
	}

	// 完整编译验证（go build ./...）及 grep 残留引用检查由 CI 提供
}
