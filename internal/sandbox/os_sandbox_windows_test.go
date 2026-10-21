//go:build windows

// Windows Job Object 沙箱单元测试（执行断言 + 结果断言混合：T17-25 约束）。
package sandbox

import (
	"runtime"
	"testing"
	"unsafe"

	"golang.org/x/sys/windows"
)

// ===== 基础初始化测试 =====

func TestWindowsSandbox_Disabled(t *testing.T) {
	_ = runtime.GOOS
	w := NewWindowsSandbox(WindowsSandboxConfig{UseJobObject: false}, NewAuditLog(), SandboxLevelStrict)
	if w == nil {
		t.Fatal("NewWindowsSandbox nil")
	}
	_, _, err := w.Enter()
	if err != nil {
		t.Fatalf("Enter disabled err: %v", err)
	}
	defer w.Exit()
}

func TestWindowsSandbox_DefaultsAppliedWhenRequested(t *testing.T) {
	cfg := WindowsSandboxConfig{UseJobObject: true}
	w := NewWindowsSandbox(cfg, NewAuditLog(), SandboxLevelIsolated)
	_, cleanup, err := w.Enter()
	if err != nil {
		t.Logf("Enter returned err (CI env permission possible): %v", err)
		return
	}
	defer cleanup()
	if w.jobHandle == windows.Handle(0) {
		t.Log("Job handle zero (degraded path accepted, no panic)")
		return
	}
	var info windows.JOBOBJECT_EXTENDED_LIMIT_INFORMATION
	var returned uint32
	qerr := windows.QueryInformationJobObject(w.jobHandle, windows.JobObjectExtendedLimitInformation,
		uintptr(unsafe.Pointer(&info)), uint32(unsafe.Sizeof(info)), &returned)
	if qerr != nil {
		t.Skipf("QueryInformationJobObject not available in this env: %v", qerr)
	}
	flags := info.BasicLimitInformation.LimitFlags
	if flags&windows.JOB_OBJECT_LIMIT_KILL_ON_JOB_CLOSE == 0 {
		t.Errorf("KILL_ON_JOB_CLOSE not set; flags=0x%x", flags)
	}
	if flags&windows.JOB_OBJECT_LIMIT_PROCESS_MEMORY == 0 {
		t.Errorf("PROCESS_MEMORY limit not set; flags=0x%x (default memory expected)", flags)
	}
	if flags&windows.JOB_OBJECT_LIMIT_ACTIVE_PROCESS == 0 {
		t.Errorf("ACTIVE_PROCESS limit not set; flags=0x%x (default processes expected)", flags)
	}
	if info.ProcessMemoryLimit != uintptr(DefaultWindowsMemoryLimit) {
		t.Errorf("ProcessMemoryLimit=%d, want %d", info.ProcessMemoryLimit, DefaultWindowsMemoryLimit)
	}
	if info.BasicLimitInformation.ActiveProcessLimit != DefaultWindowsMaxProcesses {
		t.Errorf("ActiveProcessLimit=%d, want %d",
			info.BasicLimitInformation.ActiveProcessLimit, DefaultWindowsMaxProcesses)
	}
}

func TestWindowsSandbox_ExplicitMemoryAndProcessCount(t *testing.T) {
	mem := int64(128 * 1024 * 1024)
	var maxProc uint32 = 8
	cfg := WindowsSandboxConfig{UseJobObject: true, MemoryLimit: mem, MaxProcesses: maxProc}
	w := NewWindowsSandbox(cfg, NewAuditLog(), SandboxLevelIsolated)
	_, cleanup, err := w.Enter()
	if err != nil {
		t.Skipf("Enter failed: %v", err)
	}
	defer cleanup()
	if w.jobHandle == 0 {
		t.Skip("degraded path (no job), skip assertions")
	}
	var info windows.JOBOBJECT_EXTENDED_LIMIT_INFORMATION
	var returned uint32
	qerr := windows.QueryInformationJobObject(w.jobHandle, windows.JobObjectExtendedLimitInformation,
		uintptr(unsafe.Pointer(&info)), uint32(unsafe.Sizeof(info)), &returned)
	if qerr != nil {
		t.Skipf("QueryInformationJobObject failed: %v", qerr)
	}
	if info.ProcessMemoryLimit != uintptr(mem) {
		t.Errorf("ProcessMemoryLimit=%d, want %d", info.ProcessMemoryLimit, mem)
	}
	if info.BasicLimitInformation.ActiveProcessLimit != maxProc {
		t.Errorf("ActiveProcessLimit=%d, want %d",
			info.BasicLimitInformation.ActiveProcessLimit, maxProc)
	}
}

// ===== P2-1: CPU Rate Control — 结构契约 + 结果断言（B2-1 生产路径追踪）=====

// TestP21_CPURateConfigHasFields 验证配置结构暴露 CPUMaxPercent/IORateMax 字段。
// 与 production.go CPU 默认值映射对齐的结构契约断言（B2-1）。
func TestP21_CPURateConfigHasFields(t *testing.T) {
	cfg := WindowsSandboxConfig{
		UseJobObject:  true,
		MemoryLimit:   1024 * 1024,
		MaxProcesses:  32,
		CPUMaxPercent: 50,
		IORateMax:     0, // 0 = 不限制（字段存在以保证 production.go 可映射）
	}
	if cfg.CPUMaxPercent != 50 {
		t.Errorf("CPUMaxPercent=%d, want 50 (B2-1 结构契约)", cfg.CPUMaxPercent)
	}
	if cfg.IORateMax != 0 {
		t.Errorf("IORateMax=%d, want 0 (默认不限制)", cfg.IORateMax)
	}
}

// TestP21_SetResourceLimits_AppliesCPURate 验证 P2-1 CPU HARD_CAP 设置结果。
// 生产路径：NewWindowsSandbox → Enter → setExtendedLimits → setCPURate
// 结果断言：QueryInformationJobObject 读回，flags=ENABLE|HARD_CAP、
// CpuRate = 2500（25% × 100）；或降级路径必须能观测到 canary。
func TestP21_SetResourceLimits_AppliesCPURate(t *testing.T) {
	audit := NewAuditLog()
	cfg := WindowsSandboxConfig{
		UseJobObject:  true,
		MemoryLimit:   512 * 1024 * 1024,
		MaxProcesses:  16,
		CPUMaxPercent: 25,
	}
	w := NewWindowsSandbox(cfg, audit, SandboxLevelStrict)
	_, cleanup, enterErr := w.Enter()
	if enterErr != nil {
		t.Skipf("Enter failed: %v", enterErr)
	}
	defer cleanup()
	if w.jobHandle == 0 {
		// 降级路径：必须可观测（B4-2 不可静默）
		if !audit.ContainsDegradation() {
			t.Error("degraded path but audit has NO DEGRADE canary (B4-2 静默防护违例)")
		}
		t.Skip("degraded path, skip CPU assertions")
	}

	var info jobObjectCPURateInfo
	var returned uint32
	// jobObjectCpuRateInfoClass 是无类型常量；QueryInformationJobObject 参数 2 为 int32。
	qerr := windows.QueryInformationJobObject(w.jobHandle, int32(jobObjectCpuRateInfoClass),
		uintptr(unsafe.Pointer(&info)), uint32(unsafe.Sizeof(info)), &returned)
	if qerr != nil {
		// 老系统不支持 CPU rate info class（如 Win7）→ 降级 canary 必须存在
		if !audit.ContainsDegradation() {
			t.Errorf("Query CPU rate failed but audit has NO DEGRADE canary: %v (B4-2)", qerr)
		}
		t.Skipf("QueryInformationJobObject(CpuRate) unsupported on this Windows: %v", qerr)
	}

	wantFlags := JOB_OBJECT_CPU_RATE_CONTROL_ENABLE | JOB_OBJECT_CPU_RATE_CONTROL_HARD_CAP
	if info.ControlFlags&wantFlags != wantFlags {
		t.Errorf("ControlFlags=0x%x, want bits ENABLE(0x1)|HARD_CAP(0x4)=0x%x",
			info.ControlFlags, wantFlags)
	}
	wantRate := uint32(25 * 100) // 2500 (25% × 100)
	if info.CpuRate != wantRate {
		t.Errorf("CpuRate=%d, want %d (P2-1 结果断言，25%% HARD_CAP)", info.CpuRate, wantRate)
	}

	// ReadCPURate 便捷方法一致性断言
	_, pct, rerr := w.ReadCPURate()
	if rerr != nil {
		t.Skipf("ReadCPURate failed: %v", rerr)
	}
	if pct != 25 {
		t.Errorf("ReadCPURate pct=%d, want 25", pct)
	}
}

// TestP21_FDLimit_IndirectConstraintCanary 验证 Windows 无原生 fd/文件大小限制时，
// platformApplyResourceLimits 会打降级 canary 日志（B4-2 静默防护清单）。
//
// 文档缺口（见 os_sandbox.go WindowsSandboxConfig 注释）：
//
//	[GAP-WIN-P2 fd 限制]      → Job 内存限制兜底 + canary 可观测
//	[GAP-WIN-P2 文件大小限制]  → I/O 速率限制/内存限制兜底 + canary 可观测
func TestP21_FDLimit_IndirectConstraintCanary(t *testing.T) {
	audit := NewAuditLog()
	limits := ResourceLimits{
		MemoryMaxBytes:    128 * 1024 * 1024,
		ProcessMax:        16,
		FileDescriptorMax: 512,              // GAP-WIN-P2: indirect only
		FileSizeMaxBytes:  10 * 1024 * 1024, // GAP-WIN-P2: indirect only
	}
	// resource_enforce_windows.go (//go:build windows) 的实现
	err := platformApplyResourceLimits(nil, limits, audit)
	if err != nil {
		t.Fatalf("platformApplyResourceLimits err=%v "+
			"(B5-1 降级应 fail-open 返回 nil，不可向上报错)", err)
	}
	if !audit.ContainsDegradation() {
		t.Error("FileDescriptorMax/FileSizeMaxBytes > 0 on Windows, " +
			"but audit has NO DEGRADE canary (B4-2 静默防护违例)")
	}
	dc := audit.DegradationCount()
	if dc < 2 {
		t.Errorf("DegradationCount=%d, want >= 2 (fd + file-size each produce canary)", dc)
	}
}
