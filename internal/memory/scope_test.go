// scope_test.go — H11/N27 Phase B B1 测试。
// 含 4 个测试用例（含 D8 TOCTOU 致命反例）：
//   - TestNewMemoryStoreForScope_user：user scope 目录 _user/ 无 hash + per-file 锁路径 <name>.lock
//   - TestNewMemoryStoreForScope_project：project scope 返回 error
//   - TestNewMemoryStoreForProject：project scope 目录含 hash + 目录级锁 memory.lock
//   - TestWrite_user_no_enforceFileLimit（致命反例 D8 TOCTOU）：250 文件全部保留，enforceFileLimitLocked 未被调用
//   - TestWrite_user_capacity_warning：201 文件触发 stderr 容量告警
//   - TestWrite_project_enforceFileLimit：project scope 写 250 文件触发 LRU 删除（与 user 反例对照）
//   - TestWrite_zero_defaults：零值默认值处理（Importance=0→50 / Verification=""→unverified / ValidFrom=nil→&CreatedAt）
//   - TestSerializeRoundTrip：序列化-反序列化往返一致

package memory

import (
	"bytes"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// newTestBaseDir 创建临时测试目录（每测试隔离）。
func newTestBaseDir(t *testing.T) string {
	t.Helper()
	dir, err := os.MkdirTemp("", "whale-memory-scope-test-*")
	if err != nil {
		t.Fatalf("创建临时目录失败: %v", err)
	}
	t.Cleanup(func() {
		_ = os.RemoveAll(dir)
	})
	return dir
}

// captureStderr 重定向 os.Stderr 到 bytes.Buffer，返回恢复函数。
// 调用恢复函数后返回捕获的 stderr 字符串。
// 用法：
//
//	restore := captureStderr(t)
//	... 写入操作 ...
//	stderrOutput := restore()
func captureStderr(t *testing.T) func() string {
	t.Helper()
	oldStderr := os.Stderr
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatalf("os.Pipe 失败: %v", err)
	}
	os.Stderr = w

	var buf bytes.Buffer
	done := make(chan struct{})
	go func() {
		_, _ = io.Copy(&buf, r)
		close(done)
	}()

	// restore：恢复 stderr + 关闭写端 + 等待 goroutine 读完 + 返回捕获内容
	return func() string {
		os.Stderr = oldStderr
		_ = w.Close()
		<-done
		return buf.String()
	}
}

// TestNewMemoryStoreForScope_user 验证 user scope 创建后目录 _user/ 无 hash + per-file 锁路径 <name>.lock。
func TestNewMemoryStoreForScope_user(t *testing.T) {
	baseDir := newTestBaseDir(t)

	store, err := NewMemoryStoreForScope(baseDir, "user", false)
	if err != nil {
		t.Fatalf("NewMemoryStoreForScope(user) 失败: %v", err)
	}

	// 验证 scope
	if store.Scope() != "user" {
		t.Errorf("Scope() = %q, 期望 %q", store.Scope(), "user")
	}

	// 验证目录 = baseDir/memory/_user/（无 hash）
	expectedDir := filepath.Join(baseDir, "memory", "_user")
	if store.Dir() != expectedDir {
		t.Errorf("Dir() = %q, 期望 %q", store.Dir(), expectedDir)
	}

	// 验证目录存在
	if info, err := os.Stat(expectedDir); err != nil || !info.IsDir() {
		t.Errorf("目录 %q 不存在或不是目录 (err=%v)", expectedDir, err)
	}

	// 写入一个 entry，验证 per-file 锁路径为 <name>.lock（非 memory.lock）
	entry := &MemoryEntry{
		Name:        "test-pref",
		Description: "test preference",
		Type:        MemoryTypeUser,
		CreatedAt:   time.Now(),
		Content:     "test content",
	}
	if err := store.Write(entry); err != nil {
		t.Fatalf("Write 失败: %v", err)
	}

	// 验证文件存在
	path := store.FilePath("test-pref")
	if _, err := os.Stat(path); err != nil {
		t.Errorf("记忆文件 %q 未创建: %v", path, err)
	}

	// 验证 per-file 锁已被释放（Write 后 <name>.lock 应被删除）
	perFileLockPath := filepath.Join(store.Dir(), "test-pref.lock")
	if _, err := os.Stat(perFileLockPath); !os.IsNotExist(err) {
		t.Errorf("per-file 锁 %q 未释放 (期望 IsNotExist, 实际 err=%v)", perFileLockPath, err)
	}

	// 验证目录级锁 memory.lock 不存在（user scope 不应使用 memory.lock）
	dirLockPath := filepath.Join(store.Dir(), "memory.lock")
	if _, err := os.Stat(dirLockPath); !os.IsNotExist(err) {
		t.Errorf("user scope 不应使用目录级锁 memory.lock, 但文件存在 (err=%v)", err)
	}
}

// TestNewMemoryStoreForScope_project 验证 project scope 返回 error（应使用 NewMemoryStoreForProject）。
func TestNewMemoryStoreForScope_project(t *testing.T) {
	baseDir := newTestBaseDir(t)

	_, err := NewMemoryStoreForScope(baseDir, "project", true)
	if err == nil {
		t.Fatal("NewMemoryStoreForScope(project) 应返回 error, 实际 nil")
	}

	if !strings.Contains(err.Error(), "project scope use NewMemoryStoreForProject instead") {
		t.Errorf("error 信息不匹配: %v", err)
	}
}

// TestNewMemoryStoreForScope_unknown 验证未知 scope 返回 error。
func TestNewMemoryStoreForScope_unknown(t *testing.T) {
	baseDir := newTestBaseDir(t)

	_, err := NewMemoryStoreForScope(baseDir, "unknown", true)
	if err == nil {
		t.Fatal("NewMemoryStoreForScope(unknown) 应返回 error, 实际 nil")
	}

	if !strings.Contains(err.Error(), "unknown scope") {
		t.Errorf("error 信息不匹配: %v", err)
	}
}

// TestNewMemoryStoreForProject 验证 project scope 创建后目录含 hash + 目录级锁 memory.lock。
func TestNewMemoryStoreForProject(t *testing.T) {
	baseDir := newTestBaseDir(t)
	workDir := "/test/work/dir"

	store, err := NewMemoryStoreForProject(baseDir, workDir)
	if err != nil {
		t.Fatalf("NewMemoryStoreForProject 失败: %v", err)
	}

	if store.Scope() != "project" {
		t.Errorf("Scope() = %q, 期望 %q", store.Scope(), "project")
	}

	// 验证目录含 hash（baseDir/memory/{hash}/）
	memoryDir := filepath.Join(baseDir, "memory")
	if !strings.HasPrefix(store.Dir(), memoryDir) {
		t.Errorf("Dir() = %q, 期望以 %q 开头", store.Dir(), memoryDir)
	}
	if store.Hash() == "" {
		t.Errorf("Hash() =空, 期望非空")
	}
	if !strings.HasSuffix(store.Dir(), store.Hash()) {
		t.Errorf("Dir() = %q, 期望以 hash %q 结尾", store.Dir(), store.Hash())
	}
	if store.WorkDir() != workDir {
		t.Errorf("WorkDir() = %q, 期望 %q", store.WorkDir(), workDir)
	}

	// 写入一个 entry，验证目录级锁 memory.lock（Write 后应被释放）
	entry := &MemoryEntry{
		Name:        "project-pref",
		Description: "project preference",
		Type:        MemoryTypeProject,
		CreatedAt:   time.Now(),
		Content:     "project content",
	}
	if err := store.Write(entry); err != nil {
		t.Fatalf("Write 失败: %v", err)
	}

	// 验证 memory.lock 已被释放（Write 后应被删除）
	dirLockPath := filepath.Join(store.Dir(), "memory.lock")
	if _, err := os.Stat(dirLockPath); !os.IsNotExist(err) {
		t.Errorf("目录级锁 %q 未释放 (期望 IsNotExist, 实际 err=%v)", dirLockPath, err)
	}

	// 验证 per-file 锁不存在（project scope 不应使用 per-file 锁）
	perFileLockPath := filepath.Join(store.Dir(), "project-pref.lock")
	if _, err := os.Stat(perFileLockPath); !os.IsNotExist(err) {
		t.Errorf("project scope 不应使用 per-file 锁 %q, 但文件存在 (err=%v)", perFileLockPath, err)
	}
}

// TestWrite_user_no_enforceFileLimit 致命反例（D8 TOCTOU 防护）：
// 构造 _user/ scope 写入 250 个文件，验证 enforceFileLimitLocked 未被调用（无文件被 LRU 删除），
// 仅容量告警写入 stderr。
// 关键反例验证：user scope 写 250 > MaxMemoryFiles(200)，但不应触发 LRU 删除（D8 TOCTOU 修复）。
func TestWrite_user_no_enforceFileLimit(t *testing.T) {
	baseDir := newTestBaseDir(t)

	store, err := NewMemoryStoreForScope(baseDir, "user", false)
	if err != nil {
		t.Fatalf("NewMemoryStoreForScope(user) 失败: %v", err)
	}

	// 重定向 stderr 捕获告警
	restore := captureStderr(t)

	// 写入 250 个文件（> MaxMemoryFiles=200）
	for i := 0; i < 250; i++ {
		entry := &MemoryEntry{
			Name:        nameFromIndex("pref", i),
			Description: "user preference",
			Type:        MemoryTypeUser,
			CreatedAt:   time.Now(),
			Content:     "content",
		}
		if err := store.Write(entry); err != nil {
			t.Fatalf("Write 第 %d 个失败: %v", i, err)
		}
	}

	stderrOutput := restore()

	// 关键反例验证：250 个文件全部存在（enforceFileLimitLocked 未被调用，D8 TOCTOU 修复）
	files, _ := os.ReadDir(store.Dir())
	var mdCount int
	for _, f := range files {
		if !f.IsDir() && strings.HasSuffix(strings.ToLower(f.Name()), ".md") {
			mdCount++
		}
	}
	if mdCount != 250 {
		t.Errorf("D8 TOCTOU 修复失效: 写入 250 个文件, 仅剩 %d (期望 250, LRU 被错误触发)", mdCount)
	}

	// 验证容量告警写入 stderr
	if !strings.Contains(stderrOutput, "WARN: _user/ scope has") {
		t.Errorf("容量告警未写入 stderr, 实际输出: %q", stderrOutput)
	}
	if !strings.Contains(stderrOutput, "consider /memory cleanup") {
		t.Errorf("容量告警缺少 /memory cleanup 提示, 实际输出: %q", stderrOutput)
	}
}

// TestWrite_user_capacity_warning 验证写入 201 个文件时 stderr 含 "WARN: _user/ scope has" 告警。
func TestWrite_user_capacity_warning(t *testing.T) {
	baseDir := newTestBaseDir(t)

	store, err := NewMemoryStoreForScope(baseDir, "user", false)
	if err != nil {
		t.Fatalf("NewMemoryStoreForScope(user) 失败: %v", err)
	}

	restore := captureStderr(t)

	// 写入 201 个文件（= MemoryCapacityWarningThreshold + 1）
	for i := 0; i < 201; i++ {
		entry := &MemoryEntry{
			Name:        nameFromIndex("cap", i),
			Description: "user preference",
			Type:        MemoryTypeUser,
			CreatedAt:   time.Now(),
			Content:     "content",
		}
		if err := store.Write(entry); err != nil {
			t.Fatalf("Write 第 %d 个失败: %v", i, err)
		}
	}

	stderrOutput := restore()

	// 验证 stderr 含 "WARN: _user/ scope has" 告警
	if !strings.Contains(stderrOutput, "WARN: _user/ scope has") {
		t.Errorf("容量告警未写入 stderr, 实际输出: %q", stderrOutput)
	}
	if !strings.Contains(stderrOutput, "consider /memory cleanup") {
		t.Errorf("容量告警缺少 /memory cleanup 提示, 实际输出: %q", stderrOutput)
	}
}

// TestWrite_project_enforceFileLimit 与 user 反例对照：
// project scope 写 250 文件触发 LRU 删除（保留最新 200 个）。
func TestWrite_project_enforceFileLimit(t *testing.T) {
	baseDir := newTestBaseDir(t)

	store, err := NewMemoryStoreForProject(baseDir, "/test/project/dir")
	if err != nil {
		t.Fatalf("NewMemoryStoreForProject 失败: %v", err)
	}

	// 写入 250 个文件（> MaxMemoryFiles=200，触发 LRU 删除）
	for i := 0; i < 250; i++ {
		entry := &MemoryEntry{
			Name:        nameFromIndex("proj", i),
			Description: "project preference",
			Type:        MemoryTypeProject,
			CreatedAt:   time.Now(),
			Content:     "content",
		}
		if err := store.Write(entry); err != nil {
			t.Fatalf("Write 第 %d 个失败: %v", i, err)
		}
		// 每个文件 CreatedAt 不同（time.Now 精度 ms），保证 LRU 排序稳定
		time.Sleep(1 * time.Millisecond)
	}

	// 验证文件数 == 200（最旧 50 个被 LRU 删除）
	files, _ := os.ReadDir(store.Dir())
	var mdCount int
	for _, f := range files {
		if !f.IsDir() && strings.HasSuffix(strings.ToLower(f.Name()), ".md") {
			mdCount++
		}
	}
	if mdCount != MaxMemoryFiles {
		t.Errorf("project scope LRU 删除失效: 写入 250 文件后剩 %d (期望 %d)", mdCount, MaxMemoryFiles)
	}
}

// TestWrite_zero_defaults 验证零值默认值处理。
// Importance=0 → 50, Verification="" → unverified, ValidFrom=nil → &CreatedAt。
func TestWrite_zero_defaults(t *testing.T) {
	baseDir := newTestBaseDir(t)

	store, err := NewMemoryStoreForScope(baseDir, "user", false)
	if err != nil {
		t.Fatalf("NewMemoryStoreForScope(user) 失败: %v", err)
	}

	createdAt := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	entry := &MemoryEntry{
		Name:        "zero-defaults",
		Description: "test zero defaults",
		Type:        MemoryTypeUser,
		CreatedAt:   createdAt,
		Content:     "content",
		// Importance = 0
		// Verification = ""
		// ValidFrom = nil
	}
	if err := store.Write(entry); err != nil {
		t.Fatalf("Write 失败: %v", err)
	}

	// 重新读取验证默认值已应用
	read, err := store.Read("zero-defaults")
	if err != nil {
		t.Fatalf("Read 失败: %v", err)
	}

	if read.Importance != 50 {
		t.Errorf("Importance 零值默认值失效: 实际 %d, 期望 50", read.Importance)
	}
	if read.Verification != VerificationUnverified {
		t.Errorf("Verification 零值默认值失效: 实际 %q, 期望 %q", read.Verification, VerificationUnverified)
	}
	if read.ValidFrom == nil {
		t.Errorf("ValidFrom 零值默认值失效: 实际 nil, 期望非 nil（回退到 CreatedAt）")
	} else if !read.ValidFrom.Equal(createdAt) {
		t.Errorf("ValidFrom 零值默认值失效: 实际 %v, 期望 %v (CreatedAt)", *read.ValidFrom, createdAt)
	}
}

// TestSerializeRoundTrip 验证序列化-反序列化往返一致。
func TestSerializeRoundTrip(t *testing.T) {
	now := time.Now()
	validFrom := now.Add(-1 * time.Hour)
	validUntil := now.Add(1 * time.Hour)
	entry := &MemoryEntry{
		Name:         "round-trip",
		Description:  "test round trip",
		Type:         MemoryTypeUser,
		CreatedAt:    now,
		UpdatedAt:    now,
		TrustLevel:   TrustStable,
		ProbeFn:      "test-probe",
		TTlSeconds:   86400,
		Verification: VerificationVerified,
		ValidFrom:    &validFrom,
		ValidUntil:   &validUntil,
		Links:        []string{"link1", "link2"},
		Importance:   75,
		Source:       SourceUser,
		Content:      "test content body",
	}

	data, err := serializeMemoryEntry(entry)
	if err != nil {
		t.Fatalf("serializeMemoryEntry 失败: %v", err)
	}

	parsed, err := parseMemoryEntry(string(data))
	if err != nil {
		t.Fatalf("parseMemoryEntry 失败: %v", err)
	}

	// 验证字段一致
	if parsed.Name != entry.Name {
		t.Errorf("Name: 实际 %q, 期望 %q", parsed.Name, entry.Name)
	}
	if parsed.Description != entry.Description {
		t.Errorf("Description: 实际 %q, 期望 %q", parsed.Description, entry.Description)
	}
	if parsed.Type != entry.Type {
		t.Errorf("Type: 实际 %q, 期望 %q", parsed.Type, entry.Type)
	}
	if parsed.TrustLevel != entry.TrustLevel {
		t.Errorf("TrustLevel: 实际 %q, 期望 %q", parsed.TrustLevel, entry.TrustLevel)
	}
	if parsed.ProbeFn != entry.ProbeFn {
		t.Errorf("ProbeFn: 实际 %q, 期望 %q", parsed.ProbeFn, entry.ProbeFn)
	}
	if parsed.TTlSeconds != entry.TTlSeconds {
		t.Errorf("TTlSeconds: 实际 %d, 期望 %d", parsed.TTlSeconds, entry.TTlSeconds)
	}
	if parsed.Verification != entry.Verification {
		t.Errorf("Verification: 实际 %q, 期望 %q", parsed.Verification, entry.Verification)
	}
	if parsed.Importance != entry.Importance {
		t.Errorf("Importance: 实际 %d, 期望 %d", parsed.Importance, entry.Importance)
	}
	if parsed.Source != entry.Source {
		t.Errorf("Source: 实际 %q, 期望 %q", parsed.Source, entry.Source)
	}
	if parsed.Content != entry.Content {
		t.Errorf("Content: 实际 %q, 期望 %q", parsed.Content, entry.Content)
	}
	if len(parsed.Links) != len(entry.Links) {
		t.Errorf("Links 长度: 实际 %d, 期望 %d", len(parsed.Links), len(entry.Links))
	} else {
		for i, l := range entry.Links {
			if parsed.Links[i] != l {
				t.Errorf("Links[%d]: 实际 %q, 期望 %q", i, parsed.Links[i], l)
			}
		}
	}
	// ValidFrom/ValidUntil 用 RFC3339 字符串比较（避免亚秒精度+单调时钟差异，A5 修复经验）
	if parsed.ValidFrom == nil {
		t.Errorf("ValidFrom: 实际 nil, 期望非 nil")
	} else if parsed.ValidFrom.Format(time.RFC3339Nano) != validFrom.Format(time.RFC3339Nano) {
		t.Errorf("ValidFrom: 实际 %v, 期望 %v", *parsed.ValidFrom, validFrom)
	}
	if parsed.ValidUntil == nil {
		t.Errorf("ValidUntil: 实际 nil, 期望非 nil")
	} else if parsed.ValidUntil.Format(time.RFC3339Nano) != validUntil.Format(time.RFC3339Nano) {
		t.Errorf("ValidUntil: 实际 %v, 期望 %v", *parsed.ValidUntil, validUntil)
	}
}

// TestSanitizeMemoryFilename 验证 sanitize 规则（与 config.sanitizeMemoryFilename 等价）。
func TestSanitizeMemoryFilename(t *testing.T) {
	// 注：Go regex \w 不含中文/unicode 字母（除非开启 re2.Unicode flag），
	// 中文会被 sanitizeRe 替换为 "-"，最终 Trim 后空 → "untitled"
	tests := []struct {
		input    string
		expected string
	}{
		{"simple-name", "simple-name"},
		{"Name With Spaces", "name-with-spaces"},
		{"中文记忆", "untitled"},             // 中文被 sanitizeRe 替换为 - → Trim 后空 → untitled
		{"path/to/file", "path-to-file"}, // / 替换为 - → Trim 后 path-to-file
		{"path\\to\\file", "path-to-file"},
		{"UPPER", "upper"},
		{"", "untitled"},
		{"   ", "untitled"},
		{strings.Repeat("a", 100), strings.Repeat("a", 64)}, // 限长 64
		{"name_123", "name_123"},                            // \w 含下划线
	}
	for _, tt := range tests {
		got := sanitizeMemoryFilename(tt.input)
		if got != tt.expected {
			t.Errorf("sanitizeMemoryFilename(%q) = %q, 期望 %q", tt.input, got, tt.expected)
		}
	}
}

// TestReadNotFound 验证 Read 不存在的文件返回 error。
func TestReadNotFound(t *testing.T) {
	baseDir := newTestBaseDir(t)

	store, err := NewMemoryStoreForScope(baseDir, "user", false)
	if err != nil {
		t.Fatalf("NewMemoryStoreForScope(user) 失败: %v", err)
	}

	_, err = store.Read("non-existent")
	if err == nil {
		t.Fatal("Read 不存在的文件应返回 error, 实际 nil")
	}
}

// TestListEmpty 验证空目录 List 返回空切片。
func TestListEmpty(t *testing.T) {
	baseDir := newTestBaseDir(t)

	store, err := NewMemoryStoreForScope(baseDir, "user", false)
	if err != nil {
		t.Fatalf("NewMemoryStoreForScope(user) 失败: %v", err)
	}

	list, err := store.List()
	if err != nil {
		t.Fatalf("List 失败: %v", err)
	}
	if len(list) != 0 {
		t.Errorf("空目录 List 返回 %d 条, 期望 0", len(list))
	}
}

// TestListSortedByUpdatedAt 验证 List 按 UpdatedAt 降序排序。
func TestListSortedByUpdatedAt(t *testing.T) {
	baseDir := newTestBaseDir(t)

	store, err := NewMemoryStoreForScope(baseDir, "user", false)
	if err != nil {
		t.Fatalf("NewMemoryStoreForScope(user) 失败: %v", err)
	}

	// 写入 3 个 entry，CreatedAt 故意不同（最新 CreatedAt 应排前）
	entries := []*MemoryEntry{
		{Name: "old", Description: "old", Type: MemoryTypeUser, CreatedAt: time.Now(), Content: "old"},
		{Name: "newer", Description: "newer", Type: MemoryTypeUser, CreatedAt: time.Now(), Content: "newer"},
		{Name: "newest", Description: "newest", Type: MemoryTypeUser, CreatedAt: time.Now(), Content: "newest"},
	}
	for _, e := range entries {
		if err := store.Write(e); err != nil {
			t.Fatalf("Write 失败: %v", err)
		}
		time.Sleep(2 * time.Millisecond) // 保证 UpdatedAt 不同
	}

	list, err := store.List()
	if err != nil {
		t.Fatalf("List 失败: %v", err)
	}
	if len(list) != 3 {
		t.Fatalf("List 返回 %d 条, 期望 3", len(list))
	}

	// 按 UpdatedAt 降序排序（最新在前）
	for i := 0; i < len(list)-1; i++ {
		if !list[i].UpdatedAt.After(list[i+1].UpdatedAt) {
			t.Errorf("List 排序错误: index %d (%v) 应晚于 index %d (%v)",
				i, list[i].UpdatedAt, i+1, list[i+1].UpdatedAt)
		}
	}
	// 最新应排第一
	if list[0].Name != "newest" {
		t.Errorf("List[0].Name = %q, 期望 %q (最新)", list[0].Name, "newest")
	}
}

// TestDelete 验证删除文件。
func TestDelete(t *testing.T) {
	baseDir := newTestBaseDir(t)

	store, err := NewMemoryStoreForScope(baseDir, "user", false)
	if err != nil {
		t.Fatalf("NewMemoryStoreForScope(user) 失败: %v", err)
	}

	entry := &MemoryEntry{
		Name:        "delete-me",
		Description: "to be deleted",
		Type:        MemoryTypeUser,
		CreatedAt:   time.Now(),
		Content:     "content",
	}
	if err := store.Write(entry); err != nil {
		t.Fatalf("Write 失败: %v", err)
	}

	// 验证文件存在
	if _, err := os.Stat(store.FilePath("delete-me")); err != nil {
		t.Fatalf("文件未创建: %v", err)
	}

	// 删除
	if err := store.Delete("delete-me"); err != nil {
		t.Fatalf("Delete 失败: %v", err)
	}

	// 验证文件不存在
	if _, err := os.Stat(store.FilePath("delete-me")); !os.IsNotExist(err) {
		t.Errorf("删除后文件仍存在 (err=%v)", err)
	}
}

// TestDeleteProjectScope 验证 project scope 删除走目录级锁。
func TestDeleteProjectScope(t *testing.T) {
	baseDir := newTestBaseDir(t)

	store, err := NewMemoryStoreForProject(baseDir, "/test/project/delete")
	if err != nil {
		t.Fatalf("NewMemoryStoreForProject 失败: %v", err)
	}

	entry := &MemoryEntry{
		Name:        "delete-proj",
		Description: "to be deleted",
		Type:        MemoryTypeProject,
		CreatedAt:   time.Now(),
		Content:     "content",
	}
	if err := store.Write(entry); err != nil {
		t.Fatalf("Write 失败: %v", err)
	}

	if err := store.Delete("delete-proj"); err != nil {
		t.Fatalf("Delete 失败: %v", err)
	}

	if _, err := os.Stat(store.FilePath("delete-proj")); !os.IsNotExist(err) {
		t.Errorf("删除后文件仍存在 (err=%v)", err)
	}
}

// nameFromIndex 生成基于序号的唯一 name（前缀 + 编号 + 字母后缀，避免 sanitize 冲突）。
// 用于 250/201 文件批量写入测试。
func nameFromIndex(prefix string, i int) string {
	// 编码：前缀-000-aaa 形式，保证 250+ 不同 name
	return prefix + "-" + zeroPad(i, 3) + "-" + string(rune('a'+i%26)) + string(rune('a'+i/26%26))
}

// zeroPad 左侧补零到指定宽度。
func zeroPad(n, width int) string {
	s := ""
	for i := 0; i < width; i++ {
		d := (n / pow10(width-1-i)) % 10
		s += string(rune('0' + d))
	}
	return s
}

// pow10 计算 10^n（仅小数 n，避免引入 math）。
func pow10(n int) int {
	result := 1
	for i := 0; i < n; i++ {
		result *= 10
	}
	return result
}
