// Package tools — Sandbox integration test
// 验证 Sandbox 在 Executor 和 Bash 工具中的正确集成
package tools

import (
	"context"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// TestExecutor_SandboxInitialized 测试创建 Executor 后 Sandbox 不为 nil 且默认禁用
func TestExecutor_SandboxInitialized(t *testing.T) {
	r := NewRegistry()
	tmpDir := t.TempDir()
	e := NewExecutor(r, tmpDir)

	// Sandbox 应已被初始化，不为 nil
	if e.Sandbox == nil {
		t.Fatal("Executor.Sandbox 不should be nil，应在 NewExecutor 中初始化")
	}

	// Sandbox 默认应为禁用状态
	if e.Sandbox.Enabled() {
		t.Error("Sandbox 默认应为禁用状态")
	}
}

// TestSandbox_EnableDisable 测试启用沙箱后危险命令rejected，禁用后所有命令允许
func TestSandbox_EnableDisable(t *testing.T) {
	r := NewRegistry()
	tmpDir := t.TempDir()
	e := NewExecutor(r, tmpDir)

	// 禁用状态下，危险命令应允许
	e.Sandbox.SetEnabled(false)
	allowed, _ := e.Sandbox.Validate("rm -rf /")
	if !allowed {
		t.Error("禁用状态下，所有命令应被允许")
	}

	// 启用状态下，危险命令should be rejected
	e.Sandbox.SetEnabled(true)
	allowed, reason := e.Sandbox.Validate("rm -rf /")
	if allowed {
		t.Error("启用状态下，危险命令should be rejected")
	}
	if reason == "" {
		t.Error("拒绝命令时should return原因")
	}

	// 启用状态下，安全命令应允许
	allowed, _ = e.Sandbox.Validate("echo hello")
	if !allowed {
		t.Error("启用状态下，安全命令应被允许")
	}

	// 再次禁用，危险命令应再次允许
	e.Sandbox.SetEnabled(false)
	allowed, _ = e.Sandbox.Validate("rm -rf /")
	if !allowed {
		t.Error("重新禁用后，所有命令应被允许")
	}
}

// TestSandbox_ValidatePath 测试路径验证：工作目录内允许，工作目录外拒绝
func TestSandbox_ValidatePath(t *testing.T) {
	r := NewRegistry()
	tmpDir := t.TempDir()
	e := NewExecutor(r, tmpDir)

	// 启用沙箱
	e.Sandbox.SetEnabled(true)

	// 工作目录内路径应允许
	insidePath := filepath.Join(tmpDir, "test.txt")
	allowed, _ := e.Sandbox.ValidatePath(insidePath)
	if !allowed {
		t.Errorf("工作目录内路径 %q 应被允许", insidePath)
	}

	// 工作目录外路径应拒绝
	outsidePath := "/etc/passwd"
	if runtime.GOOS == "windows" {
		outsidePath = "C:\\Windows\\System32\\config\\SAM"
	}
	allowed, reason := e.Sandbox.ValidatePath(outsidePath)
	if allowed {
		t.Errorf("工作目录外路径 %q should be rejected", outsidePath)
	}
	if reason == "" {
		t.Error("拒绝路径时should return原因")
	}

	// 禁用状态下，所有路径应允许
	e.Sandbox.SetEnabled(false)
	allowed, _ = e.Sandbox.ValidatePath(outsidePath)
	if !allowed {
		t.Error("禁用状态下，所有路径应被允许")
	}
}

// TestBashHandler_SandboxBlock 测试启用沙箱时，危险命令通过 bash 工具被阻止
func TestBashHandler_SandboxBlock(t *testing.T) {
	r := NewRegistry()
	tmpDir := t.TempDir()
	e := NewExecutor(r, tmpDir)

	// 启用沙箱
	e.Sandbox.SetEnabled(true)

	// 执行危险命令
	result := e.Execute(context.Background(), Call{
		ID:    "1",
		Name:  "bash",
		Input: mustMarshal(map[string]interface{}{"command": "rm -rf /"}),
	})

	// 危险命令应被沙箱阻止。本路径是 **bash 硬拒绝**（bash.go 的 errSandboxDenied）
	// ⇒ executeDirect 映射为 IsError=true + FailureKind=SandboxDenied（G1-B 语义）。
	// （G1-A 收口后，写/搜索/插件工具的 handler 层拒绝走**软**哨兵 IsError=false；
	//   与这里刻意不同 —— 两者由同一个 executeDirect 分支区分，勿混。）
	if !strings.Contains(result.Output, "sandbox") {
		t.Errorf("危险命令应被沙箱拒绝，actual输出: %s", result.Output)
	}
}

// TestBashHandler_SandboxAllow 测试启用沙箱时，安全命令正常执行
func TestBashHandler_SandboxAllow(t *testing.T) {
	r := NewRegistry()
	tmpDir := t.TempDir()
	e := NewExecutor(r, tmpDir)

	// 启用沙箱
	e.Sandbox.SetEnabled(true)

	// 安全命令应正常执行
	var echoCmd string
	if runtime.GOOS == "windows" {
		echoCmd = "Write-Output hello"
	} else {
		echoCmd = "echo hello"
	}

	result := e.Execute(context.Background(), Call{
		ID:    "1",
		Name:  "bash",
		Input: mustMarshal(map[string]interface{}{"command": echoCmd}),
	})

	if result.IsError {
		t.Fatalf("安全命令应正常执行，但得到错误: %s", result.Output)
	}
	if !strings.Contains(result.Output, "hello") {
		t.Errorf("安全命令输出should contain 'hello'，actual: %s", result.Output)
	}
}
