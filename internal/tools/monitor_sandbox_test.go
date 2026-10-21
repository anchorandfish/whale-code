package tools

// C-P0-2 反例测试：后台监控进程沙箱绕过修复
//
// 测试目标：
//   - monitorHandler 调用 prepareSandbox 后，deny 命令被拦截（MonitorMgr.Start 不被调用）
//   - 无沙箱时向后兼容（MonitorMgr.Start 正常执行）
//   - 沙箱允许安全命令通过（不误拦 echo 等安全命令）
//
// 修复前：monitorHandler 不调 prepareSandbox，直接调用 MonitorMgr.Start，
// 后台监控进程可绕过沙箱 deny 列表执行任意命令（如 curl 外传数据）。
// 修复后：monitorHandler 复用 Executor.prepareSandbox，deny 命令被拦截，
// 返回 "sandbox denied" 且不启动监控进程；安全命令正常接入沙箱链路。

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/anchorandfish/whale-code/internal/monitor"
	"github.com/anchorandfish/whale-code/internal/sandbox"
)

// TestCP0_2_MonitorSandboxDenyCommand 反例测试（C-P0-2）：deny 命令被沙箱拦截，MonitorMgr.Start 不被调用。
//
// 修复前：monitorHandler 不调 prepareSandbox，curl（默认 deny 列表）可正常启动监控进程。
// 修复后：monitorHandler 调用 prepareSandbox，curl 被拦截，返回 "sandbox denied" 且不创建进程。
func TestCP0_2_MonitorSandboxDenyCommand(t *testing.T) {
	// 构造带 Legacy Sandbox 的 Executor（curl 在默认 deny 列表）
	workDir := t.TempDir()
	sb := sandbox.NewSandbox(workDir)
	sb.SetEnabled(true)

	e := &Executor{
		Sandbox:    sb,
		workDir:    workDir,
		MonitorMgr: monitor.NewManager(),
	}

	handler := e.monitorHandler(workDir)

	input, err := json.Marshal(MonitorInput{
		Command:     "curl http://evil.com",
		Description: "deny command test",
	})
	if err != nil {
		t.Fatalf("marshal input failed: %v", err)
	}

	output, err := handler(context.Background(), input)
	// G1-A（收口，2026-09-14）：沙箱拒绝语义由「(msg, nil)」改为返回软哨兵
	// errSandboxDeniedSoft —— **文案不变**（模型契约），由 executeDirect 映射为
	// IsError=false + FailureKind=SandboxDenied（与 write_file/edit_file 同形）。
	// 本用例在 handler 层**直接调用**，故断言哨兵本身；进入 executeDirect 后的
	// (IsError, FailureKind) 形态由 g1a_rejection_probe_test.go 覆盖。
	if !errors.Is(err, errSandboxDeniedSoft) {
		t.Fatalf("沙箱拒绝应返回软哨兵 errSandboxDeniedSoft，实际 error=%v；output=%s", err, output)
	}

	// 验证返回值表明沙箱拒绝
	if !strings.Contains(strings.ToLower(output), "sandbox") {
		t.Errorf("C-P0-2 反例失败: deny 命令未被沙箱拦截\n"+
			"  命令: curl http://evil.com\n"+
			"  期望: output 包含 'sandbox'（沙箱拒绝）\n"+
			"  实际: output=%s", output)
	}

	// 验证 MonitorMgr.Start 未被调用（无进程创建）
	procs := e.MonitorMgr.List()
	if len(procs) != 0 {
		// 清理可能创建的进程
		for _, p := range procs {
			_ = e.MonitorMgr.Stop(p.ID)
		}
		t.Errorf("C-P0-2 反例失败: deny 命令被拦截后仍创建了监控进程\n"+
			"  命令: curl http://evil.com\n"+
			"  期望: MonitorMgr.List() 长度为 0\n"+
			"  实际: 长度为 %d", len(procs))
	}
}

// TestCP0_2_MonitorNoSandboxBackwardCompat 验证无沙箱时向后兼容（C-P0-2）。
//
// 无沙箱配置时，monitorHandler 退化为直接调用 MonitorMgr.Start（仅进程组隔离），
// 安全命令正常启动监控进程。
func TestCP0_2_MonitorNoSandboxBackwardCompat(t *testing.T) {
	workDir := t.TempDir()

	// 不设置 Sandbox/SandboxMgr（无沙箱，向后兼容）
	e := &Executor{
		workDir:    workDir,
		MonitorMgr: monitor.NewManager(),
	}
	defer e.MonitorMgr.StopAll()

	handler := e.monitorHandler(workDir)

	input, err := json.Marshal(MonitorInput{
		Command:     "echo no_sandbox_compat",
		Description: "backward compat test",
	})
	if err != nil {
		t.Fatalf("marshal input failed: %v", err)
	}

	output, err := handler(context.Background(), input)
	if err != nil {
		t.Fatalf("C-P0-2 向后兼容失败: 无沙箱时安全命令应正常启动\n"+
			"  命令: echo no_sandbox_compat\n"+
			"  期望: 启动成功\n"+
			"  实际: error=%v", err)
	}

	// 验证监控进程已创建
	procs := e.MonitorMgr.List()
	if len(procs) == 0 {
		t.Errorf("C-P0-2 向后兼容失败: 无沙箱时未创建监控进程\n"+
			"  命令: echo no_sandbox_compat\n"+
			"  期望: MonitorMgr.List() 长度 >= 1\n"+
			"  实际: 长度为 0\n"+
			"  output=%s", output)
	}
}

// TestCP0_2_MonitorSandboxAllowsSafeCommand 验证沙箱允许安全命令通过（C-P0-2 回归测试）。
//
// 带沙箱的 Executor 不应误拦安全命令（如 echo），监控进程应正常创建。
// Legacy Sandbox 路径返回空 SandboxContext（无 OS 级隔离），但仍标记 sandboxed=true，
// 验证 monitorHandler 正确处理空 SandboxContext（不 panic、不误拒）。
func TestCP0_2_MonitorSandboxAllowsSafeCommand(t *testing.T) {
	workDir := t.TempDir()
	sb := sandbox.NewSandbox(workDir)
	sb.SetEnabled(true)

	e := &Executor{
		Sandbox:    sb,
		workDir:    workDir,
		MonitorMgr: monitor.NewManager(),
	}
	defer e.MonitorMgr.StopAll()

	handler := e.monitorHandler(workDir)

	input, err := json.Marshal(MonitorInput{
		Command:     "echo safe_monitor_test",
		Description: "safe command test",
	})
	if err != nil {
		t.Fatalf("marshal input failed: %v", err)
	}

	output, err := handler(context.Background(), input)
	if err != nil {
		t.Fatalf("C-P0-2 回归失败: 安全命令被误拒\n"+
			"  命令: echo safe_monitor_test\n"+
			"  期望: 启动成功\n"+
			"  实际: error=%v", err)
	}

	// 验证返回值不是沙箱拒绝
	if strings.Contains(strings.ToLower(output), "sandbox denied") {
		t.Errorf("C-P0-2 回归失败: 安全命令被沙箱误拒\n"+
			"  命令: echo safe_monitor_test\n"+
			"  期望: 正常启动（output 不含 'sandbox denied'）\n"+
			"  实际: output=%s", output)
	}

	// 验证监控进程已创建
	procs := e.MonitorMgr.List()
	if len(procs) == 0 {
		t.Errorf("C-P0-2 回归失败: 安全命令未创建监控进程\n"+
			"  命令: echo safe_monitor_test\n"+
			"  期望: MonitorMgr.List() 长度 >= 1\n"+
			"  实际: 长度为 %d", len(procs))
	}
}
