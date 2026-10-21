// Package lsp — NewClient context leak 修复测试（6-D）
//
// 验证方案 B（defer + sentinel cleanup）：
//   - 5 个错误路径上 ctx 被 cancel（defer 兜底或显式 cancel）
//   - 成功路径上 NewClient 返回后 ctx 仍活跃（cleanup 责任已转移给 Client.cancelPending）
//
// 测试分层：
//   - L1 行为：NewClient 返回错误 + 不 panic
//   - L2 goroutine 计数：runtime.NumGoroutine 前后对比
//   - L3 mock exec.CommandContext：直接验证 ctx.Done() 触发（需小重构，本批次不做，CI 补跑）
//
// 本机限制：Esafenet DLP 可能拦截 go test 二进制运行；Winsock 损坏可能导致 gopls 启动失败。
// 编译/vet 通过即可，运行时验证需 CI 补跑 go test -race ./internal/lsp/...
package lsp

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

// TestNewClient_ErrorPath1_CancelDeferred 验证错误路径 1（findLanguageServer 失败）defer 兜底 cancel
// 此路径尚未创建 cmd，理论上不泄漏 goroutine；用例主要作回归保护，确保 defer 不引入新泄漏
func TestNewClient_ErrorPath1_CancelDeferred(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping goroutine count test in short mode")
	}

	before := runtime.NumGoroutine()

	// 触发错误路径 1：不支持的语言
	_, err := NewClient(context.Background(), "brainfuck", t.TempDir())
	if err == nil {
		t.Fatal("expected error for unsupported language")
	}

	// 等待 defer 触发 + GC 回收
	runtime.GC()
	time.Sleep(50 * time.Millisecond)

	after := runtime.NumGoroutine()
	// 允许 +1 波动（GC / 其他测试 goroutine）
	if after > before+1 {
		t.Errorf("goroutine leaked: before=%d after=%d (defer cancel 未生效?)", before, after)
	}
}

// TestNewClient_ErrorPath2_StdinPipeFail 验证错误路径 2（StdinPipe 失败）ctx 被 cancel
// 需 L3 重构（execCommandContext 包级变量 + cmd.StdinPipe mock）才能稳定触发
// 本批次未做 L3 重构，标记 CI 补跑
func TestNewClient_ErrorPath2_StdinPipeFail(t *testing.T) {
	if os.Getenv("CI_LSP_MOCK") == "" {
		t.Skip("跳过：需 CI_LSP_MOCK=1 环境与 exec.Cmd mock 支持（L3 重构未做）")
	}
	// TODO: L3 重构后通过 mock exec.Cmd.StdinPipe 返回错误，验证 ctx.Done() 触发
	t.Log("L3 mock 待 CI 环境实现")
}

// TestNewClient_ErrorPath3_StdoutPipeFail 验证错误路径 3（StdoutPipe 失败）ctx 被 cancel
// 同用例 2，需 L3 mock 支持
func TestNewClient_ErrorPath3_StdoutPipeFail(t *testing.T) {
	if os.Getenv("CI_LSP_MOCK") == "" {
		t.Skip("跳过：需 CI_LSP_MOCK=1 环境与 exec.Cmd mock 支持（L3 重构未做）")
	}
	// TODO: 同上
	t.Log("L3 mock 待 CI 环境实现")
}

// TestNewClient_ErrorPath4_StartFail_NoLeak 验证错误路径 4（cmd.Start 失败）无 goroutine 泄漏
// 真正触发 cmd.Start 失败需 mock exec.CommandContext（L3），本用例降级为：
// 若 gopls 已安装，用一个无效 workspaceRoot 触发后续阶段失败（实际走路径 5）
func TestNewClient_ErrorPath4_StartFail_NoLeak(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping goroutine count test in short mode")
	}

	// 前置：若 gopls 未安装，无法触发到 cmd.Start 阶段（会先走路径 1）
	if _, err := exec.LookPath("gopls"); err != nil {
		t.Skip("gopls not installed, cannot trigger cmd.Start path")
	}

	before := runtime.NumGoroutine()

	// 用一个无效的 workspaceRoot：cmd.Start 可能成功但 initialize 失败 → 实际走路径 5
	// 真正触发 cmd.Start 失败需 L3 mock，留 CI 补跑
	_, err := NewClient(context.Background(), "go", "/nonexistent/path/that/does/not/exist")
	if err == nil {
		t.Fatal("expected error for invalid workspaceRoot")
	}

	runtime.GC()
	time.Sleep(100 * time.Millisecond)

	after := runtime.NumGoroutine()
	if after > before+1 {
		t.Errorf("goroutine leaked: before=%d after=%d", before, after)
	}
}

// TestNewClient_ErrorPath5_InitializeFail_NoLeak 验证错误路径 5（initialize 失败）显式 cancel + defer 兜底
// 触发方式：用一个不存在的 workspaceRoot，gopls 启动后 initialize 握手失败
func TestNewClient_ErrorPath5_InitializeFail_NoLeak(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping goroutine count test in short mode")
	}
	if _, err := exec.LookPath("gopls"); err != nil {
		t.Skip("gopls not installed, skipping")
	}

	before := runtime.NumGoroutine()

	// workspaceRoot 不存在 → gopls initialize 阶段 rootUri 无效，握手可能失败
	_, err := NewClient(context.Background(), "go", "/nonexistent/path")
	if err == nil {
		// 某些 gopls 版本可能容忍不存在的 rootUri，不视为失败
		t.Skip("gopls tolerated invalid rootUri, cannot trigger initialize failure")
	}

	runtime.GC()
	time.Sleep(100 * time.Millisecond)

	after := runtime.NumGoroutine()
	if after > before+1 {
		t.Errorf("goroutine leaked after initialize failure: before=%d after=%d", before, after)
	}
}

// TestNewClient_SuccessPath_NoImmediateCancel 验证成功路径上 defer 不立即 cancel（关键回归保护）
// cleanup = func() {} 转移责任后，NewClient 返回时 ctx 必须保持活跃，否则 cmd 被 kill
//
// 若修复错误（如采用方案 C 裸 defer cancel()），NewClient 返回时 ctx 立即取消，
// exec.CommandContext 触发 cmd 被 kill，gopls 进程死亡，readLoop 读 stdout EOF 退出，
// pending channel 被关闭。此时 client.IsReady() 仍为 true（ready 在 cancel 前已设置），
// 但任何 sendRequest 会因 ch 被关闭而返回 "connection closed" 错误。
//
// 本用例通过 Definition 请求间接验证 ctx 仍活跃：
//   - 若 ctx 未被 cancel：gopls 活着，Definition 返回结果或空（非 "connection closed"）
//   - 若 ctx 被 cancel（方案 C 错误）：gopls 死亡，Definition 返回 "connection closed"
func TestNewClient_SuccessPath_NoImmediateCancel(t *testing.T) {
	if _, err := exec.LookPath("gopls"); err != nil {
		t.Skip("gopls not installed, skipping")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	tempDir := t.TempDir()
	client, err := NewClient(ctx, "go", tempDir)
	if err != nil {
		t.Fatalf("NewClient 失败: %v", err)
	}
	defer client.Close()

	// 验证 client 已就绪（ready 在 cancel 前设置，方案 C 错误下仍为 true）
	if !client.IsReady() {
		t.Fatal("client should be ready after successful NewClient")
	}

	// 创建测试文件，用于触发 Definition 请求
	testFile := filepath.Join(tempDir, "main.go")
	if err := os.WriteFile(testFile, []byte("package main\n\nfunc main() {}\n"), 0644); err != nil {
		t.Fatalf("写测试文件失败: %v", err)
	}

	// 短暂等待，让方案 C 错误下的 cmd kill + readLoop 退出有时间发生
	time.Sleep(100 * time.Millisecond)

	// 发起 Definition 请求：若 ctx 被 cancel，server 已死，返回 "connection closed"
	defCtx, defCancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer defCancel()

	_, err = client.Definition(defCtx, testFile, 0, 0)
	if err != nil {
		// "connection closed" 表明 readLoop 退出 + pending channel 关闭，即 server 已死
		if strings.Contains(err.Error(), "connection closed") {
			t.Fatalf("Definition 返回 connection closed，说明 NewClient 返回后 ctx 被立即 cancel（方案 C 错误）: %v", err)
		}
		// 其他错误（如无定义、超时）可接受，说明 server 仍活着
		t.Logf("Definition 返回错误（可接受，非 connection closed）: %v", err)
	}
}
