// Package tools — 生产沙箱接线测试（P1-SEC，修复非 TUI 模式沙箱缺失）。
//
// 修复前：SetSandboxMgr 仅在 TUI lifecycle.go 调用；
// plain/bridge（runner_core.go）路径 executor.Sandbox 默认 disabled、
// SandboxMgr 为 nil → prepareSandbox 走"无沙箱"路径，deny 命令直接执行。
//
// 结果断言：EnableProductionSandbox 后，deny 命令在 prepareSandbox 被拒
// （断言行为结果，而非"SandboxMgr 已注入"这类执行痕迹）。
package tools

import (
	"context"
	"testing"
)

// TestEnableProductionSandbox_BlocksDenyCommand 结果断言：
// 启用生产沙箱后，deny 列表命令（curl）在 prepareSandbox 即被拒绝。
func TestEnableProductionSandbox_BlocksDenyCommand(t *testing.T) {
	ws := t.TempDir()
	e := NewExecutor(nil, ws)

	// 修复前基线验证：默认状态下 curl 不被拦截（证明测试有效性）
	_, _, _, baselineErr := e.prepareSandbox(context.Background(), "curl http://example.com", ws)
	if baselineErr != nil {
		t.Fatalf("baseline assumption broken: curl should NOT be blocked before enabling production sandbox, got: %v", baselineErr)
	}

	e.EnableProductionSandbox(ws)

	_, _, sandboxed, err := e.prepareSandbox(context.Background(), "curl http://example.com", ws)
	if err == nil {
		t.Fatal("SECURITY: curl passed prepareSandbox after EnableProductionSandbox — deny-wins not enforced on non-TUI path")
	}
	if sandboxed {
		t.Error("denied command must not be marked sandboxed (no process should start)")
	}
}

// TestEnableProductionSandbox_NormalCommandNotBlocked 结果断言：
// 启用生产沙箱后，普通命令（echo）不被应用层误拦（可用性回归）。
func TestEnableProductionSandbox_NormalCommandNotBlocked(t *testing.T) {
	ws := t.TempDir()
	e := NewExecutor(nil, ws)
	e.EnableProductionSandbox(ws)

	_, _, _, err := e.prepareSandbox(context.Background(), "echo hello", ws)
	if err != nil {
		t.Fatalf("normal command 'echo hello' should not be blocked, got: %v", err)
	}
}
