package tools

// 相对路径重定向越界写的生产入口回归测试（R4 修复）。
//
// 生产路径：bash 工具 → e.Execute → bash 执行 → prepareSandbox(bash.go:975)
//   → e.Sandbox.Validate(command)（executor.go:232 Sandbox 恒非 nil；lifecycle.go:435 SetEnabled(true)）
//   → sandbox.go checkWriteRedirectBoundary（相对路径也归一化到 workDir 基准判边界）。
//
// 修复前：`echo x > ../../outside.txt` 从 workDir 逃逸写被放行（沙箱绕过）。
// 修复后：命令被拒（FailureKindSandboxDenied），且外部文件确实未被创建。
//
// 断言的是真实用户可见行为：命令未执行 + 越界文件不存在。

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestBash_RelativeTraversalRedirect_Denied(t *testing.T) {
	if testing.Short() {
		t.Skip("short mode")
	}
	base := t.TempDir()                          // 越界目标所在：base/outside.txt
	workDir := filepath.Join(base, "proj", "ws") // bash 的 cwd 更深一层，../../ 可逃逸到 base
	if err := os.MkdirAll(workDir, 0o755); err != nil {
		t.Fatalf("mkdir workdir: %v", err)
	}
	escapedFile := filepath.Join(base, "escaped.txt")

	r := NewRegistry()
	e := NewExecutor(r, workDir)
	if e.Sandbox == nil {
		t.Fatal("NewExecutor should initialize the legacy sandbox")
	}
	e.Sandbox.SetEnabled(true) // 与生产 lifecycle.go:435 一致

	// workDir/proj/ws，../../escaped.txt → base/escaped.txt（越界）
	command := "echo pwnd > ../../escaped.txt"
	result := e.Execute(context.Background(), Call{
		ID:    "r4-redir",
		Name:  "bash",
		Input: mustMarshal(map[string]interface{}{"command": command}),
	})

	if !result.IsError {
		t.Fatalf("relative traversal redirect should be denied, got success (bypass!)\n  output: %s", result.Output)
	}
	if result.FailureKind != FailureKindSandboxDenied {
		t.Errorf("expected FailureKindSandboxDenied, got %q", result.FailureKind)
	}
	if !strings.Contains(result.Output, "sandbox denied") {
		t.Errorf("output should mention sandbox denial, got %q", result.Output)
	}
	if _, err := os.Stat(escapedFile); !os.IsNotExist(err) {
		t.Fatalf("denied command must NOT create escaped file %s (stat err=%v)", escapedFile, err)
	}
}

// TestBash_InWorkDirRedirect_Allowed 工作目录内（绝对路径）写重定向仍允许（不误伤）。
// 用绝对 workDir 内目标，避免 legacy-only（无 manager）下 shell cwd 未切到 workDir 的干扰。
func TestBash_InWorkDirRedirect_Allowed(t *testing.T) {
	if testing.Short() {
		t.Skip("short mode")
	}
	workDir := t.TempDir()
	r := NewRegistry()
	e := NewExecutor(r, workDir)
	if e.Sandbox == nil {
		t.Fatal("NewExecutor should initialize the legacy sandbox")
	}
	e.Sandbox.SetEnabled(true)

	outFile := filepath.Join(workDir, "ok.txt")
	command := "echo hi > " + outFile
	result := e.Execute(context.Background(), Call{
		ID:    "r4-inside",
		Name:  "bash",
		Input: mustMarshal(map[string]interface{}{"command": command}),
	})

	if result.IsError {
		t.Fatalf("in-workdir write should be allowed, got error: %s", result.Output)
	}
	data, err := os.ReadFile(outFile)
	if err != nil {
		t.Fatalf("expected in-workdir file created: %v", err)
	}
	if !strings.Contains(string(data), "hi") {
		t.Errorf("file content mismatch: %q", string(data))
	}
}
