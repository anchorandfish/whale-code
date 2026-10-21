//go:build !windows

// Package tools — 门 2 banner 接线断言（Unix；Windows 无 syscall.Dup2，
// banner 生成逻辑由 sandbox 包跨平台单测 + 真机 E2E 覆盖）。
package tools

import (
	"io"
	"os"
	"strings"
	"syscall"
	"testing"
)

// TestEnableProductionSandbox_PrintsIsolationBanner 门 2 接线断言（结果断言）：
// EnableProductionSandbox 必须把实际隔离等级打到**真实终端 stderr（fd 2）**。
//
// 门 2 修复（真机验证）：banner 原走 os.Stderr 变量，被 main.go 的日志
// 重定向吞进 ~/.whale/logs/whale.log（终端不可见）。修复后 fd 2 直写，
// 本测试同步镜像生产行为：dup fd 2 捕获（而非 os.Stderr 变量——那正是
// bug 通道，捕获它会假通过）。
func TestEnableProductionSandbox_PrintsIsolationBanner(t *testing.T) {
	ws := t.TempDir()
	e := NewExecutor(nil, ws)

	// 捕获 fd 2（生产写入点；os.Stderr 变量在真实进程里指向日志文件）
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatalf("os.Pipe: %v", err)
	}
	savedFd2, err := syscall.Dup(2)
	if err != nil {
		t.Fatalf("dup fd2: %v", err)
	}
	if err := syscall.Dup2(int(w.Fd()), 2); err != nil {
		t.Fatalf("dup2 pipe onto fd2: %v", err)
	}
	e.EnableProductionSandbox(ws)
	_ = syscall.Dup2(savedFd2, 2)
	_ = syscall.Close(savedFd2)
	_ = w.Close()
	out, _ := io.ReadAll(r)

	if !strings.Contains(string(out), "[sandbox] ") {
		t.Errorf("fd2 missing sandbox banner, got: %q", string(out))
	}
	// 双前缀回归：调用方不得再包一层 [sandbox]
	if strings.Contains(string(out), "[sandbox] [sandbox]") {
		t.Errorf("banner must have single [sandbox] prefix, got: %q", string(out))
	}
	// banner 必须含级别行（生产全平台启用 OS 沙箱，非 degraded 空白）
	if !strings.Contains(string(out), "strict:") {
		t.Errorf("banner missing level 'strict:', got: %q", string(out))
	}
}
