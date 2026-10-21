package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

// ===== B4-3A 新增：file.go LSP 接线测试 =====

// TestWriteFile_NotifiesLSP 验证 writeFileHandler 写文件成功后调用 DidChange
// 用 mockLSPClient 注入 lspClientCache，断言 didOpenCalls + didChangeCalls 非空
func TestWriteFile_NotifiesLSP(t *testing.T) {
	dir := t.TempDir()
	e := NewExecutor(NewRegistry(), dir)

	// 注入 mock LSP client
	mockClient := &mockLSPClient{ready: true}
	e.lspClientMu.Lock()
	e.lspClientCache["go"] = mockClient
	e.lspClientMu.Unlock()

	// 创建测试文件（与 Executor 同 dir，保证 sandbox 通过）
	testFile := filepath.Join(dir, "test.go")
	input := WriteFileInput{
		Path:    testFile,
		Content: "package main\n\nfunc main() {}\n",
	}
	inputBytes, _ := json.Marshal(input)

	handler := e.writeFileHandler()
	result, err := handler(context.Background(), inputBytes)
	if err != nil {
		t.Fatalf("writeFileHandler 失败: %v", err)
	}
	t.Logf("writeFileHandler result: %s", result)

	// 验证 mock 收到 DidChange（首次写文件会先 DidOpen 再 DidChange）
	totalCalls := len(mockClient.didOpenCalls) + len(mockClient.didChangeCalls)
	if totalCalls == 0 {
		t.Error("writeFileHandler 未通知 LSP（didOpenCalls + didChangeCalls 均为空）")
	}

	// 验证 DidChange 的 version 单调递增（若多次调用）
	for i := 1; i < len(mockClient.didChangeCalls); i++ {
		if mockClient.didChangeCalls[i].Version <= mockClient.didChangeCalls[i-1].Version {
			t.Errorf("DidChange version 非递增: calls[%d].Version=%d <= calls[%d].Version=%d",
				i, mockClient.didChangeCalls[i].Version, i-1, mockClient.didChangeCalls[i-1].Version)
		}
	}
}

// TestEditFile_NotifiesLSP 验证 editFileHandler 编辑文件成功后调用 DidChange
func TestEditFile_NotifiesLSP(t *testing.T) {
	dir := t.TempDir()
	e := NewExecutor(NewRegistry(), dir)

	// 注入 mock LSP client
	mockClient := &mockLSPClient{ready: true}
	e.lspClientMu.Lock()
	e.lspClientCache["go"] = mockClient
	e.lspClientMu.Unlock()

	// 创建初始文件
	testFile := filepath.Join(dir, "test.go")
	initialContent := "package main\n\nfunc oldName() {}\n"
	os.WriteFile(testFile, []byte(initialContent), 0644)

	input := EditFileInput{
		Path:    testFile,
		OldText: "oldName",
		NewText: "newName",
	}
	inputBytes, _ := json.Marshal(input)

	handler := e.editFileHandler()
	result, err := handler(context.Background(), inputBytes)
	if err != nil {
		t.Fatalf("editFileHandler 失败: %v", err)
	}
	t.Logf("editFileHandler result: %s", result)

	// 验证 mock 收到 DidChange
	if len(mockClient.didChangeCalls) == 0 {
		t.Fatal("editFileHandler 未通知 LSP（didChangeCalls 为空）")
	}

	// 验证 DidChange 传入的 newContent 包含 newName
	lastCall := mockClient.didChangeCalls[len(mockClient.didChangeCalls)-1]
	if !strings.Contains(lastCall.NewContent, "newName") {
		t.Errorf("DidChange newContent 不含 newName: %q", lastCall.NewContent)
	}
}

// TestWriteFile_LSPNotifyFailure_NoBlock 验证 LSP 通知失败时写文件仍成功
// notifyLSPDidChange 内部应吞掉错误（_ = client.DidChange(...)）
func TestWriteFile_LSPNotifyFailure_NoBlock(t *testing.T) {
	dir := t.TempDir()
	e := NewExecutor(NewRegistry(), dir)

	// 注入会返回错误的 mock
	mockClient := &mockLSPClient{
		ready:        true,
		didChangeErr: fmt.Errorf("simulated LSP failure"),
	}
	e.lspClientMu.Lock()
	e.lspClientCache["go"] = mockClient
	e.lspClientMu.Unlock()

	testFile := filepath.Join(dir, "test.go")
	input := WriteFileInput{
		Path:    testFile,
		Content: "package main\n",
	}
	inputBytes, _ := json.Marshal(input)

	handler := e.writeFileHandler()
	result, err := handler(context.Background(), inputBytes)
	// 即使 LSP 通知失败，写文件应成功
	if err != nil {
		t.Errorf("writeFileHandler 因 LSP 错误而失败: %v", err)
	}
	if !strings.Contains(result, "File written") {
		t.Errorf("writeFileHandler 结果异常: %s", result)
	}

	// 验证文件确实写入了
	data, _ := os.ReadFile(testFile)
	if string(data) != "package main\n" {
		t.Errorf("文件内容不符: %q", string(data))
	}
}

// TestNextLSPVersion_Monotonic 验证 nextLSPVersion 返回的 version 单调递增
func TestNextLSPVersion_Monotonic(t *testing.T) {
	e := NewExecutor(NewRegistry(), t.TempDir())

	filePath := "/tmp/test.go"
	versions := make([]int, 0, 10)
	for i := 0; i < 10; i++ {
		v := e.nextLSPVersion(filePath)
		versions = append(versions, v)
	}

	// 第一次应返回 2（DidOpen 已占 version=1）
	if versions[0] != 2 {
		t.Errorf("首次 version = %d, want 2", versions[0])
	}
	// 后续单调递增
	for i := 1; i < len(versions); i++ {
		if versions[i] != versions[i-1]+1 {
			t.Errorf("version 非递增: versions[%d]=%d, versions[%d]=%d",
				i, versions[i], i-1, versions[i-1])
		}
	}
}

// TestNextLSPVersion_ConcurrentSafe 验证 nextLSPVersion 并发调用不产生重复 version
func TestNextLSPVersion_ConcurrentSafe(t *testing.T) {
	e := NewExecutor(NewRegistry(), t.TempDir())

	filePath := "/tmp/test.go"
	var wg sync.WaitGroup
	versions := make([]int, 100)
	var mu sync.Mutex

	for i := 0; i < 100; i++ {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			v := e.nextLSPVersion(filePath)
			mu.Lock()
			versions[idx] = v
			mu.Unlock()
		}(i)
	}
	wg.Wait()

	// 验证 100 个 version 互不相同（无重复）
	seen := make(map[int]bool)
	for _, v := range versions {
		if seen[v] {
			t.Errorf("version 重复: %d", v)
		}
		seen[v] = true
	}
	// 应有 100 个不同的 version
	if len(seen) != 100 {
		t.Errorf("version 去重后仅 %d 个，want 100", len(seen))
	}
}

// TestWriteFile_UnsupportedLanguage_NoNotify 验证写不支持语言的文件不触发 LSP 通知
// getLSPClient 对未知语言返回 error，notifyLSPDidChange 应静默跳过
func TestWriteFile_UnsupportedLanguage_NoNotify(t *testing.T) {
	dir := t.TempDir()
	e := NewExecutor(NewRegistry(), dir)

	// 不注入任何 mock（lspClientCache 为空）

	testFile := filepath.Join(dir, "test.xyz") // .xyz 不在 DetectLanguage 支持列表
	input := WriteFileInput{
		Path:    testFile,
		Content: "unknown content",
	}
	inputBytes, _ := json.Marshal(input)

	handler := e.writeFileHandler()
	_, err := handler(context.Background(), inputBytes)
	if err != nil {
		t.Fatalf("writeFileHandler 失败: %v", err)
	}
	// 不应 panic，不应报错（notifyLSPDidChange 静默跳过）
	// 文件应已写入
	data, _ := os.ReadFile(testFile)
	if string(data) != "unknown content" {
		t.Errorf("文件内容不符: %q", string(data))
	}
}
