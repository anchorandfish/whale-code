//go:build windows

// Package sandbox — Windows Job Object 沙箱实现（/ T1-P1-B9）。
//
// 官方源对标：
// - Job Objects: https://learn.microsoft.com/en-us/windows/win32/procthread/job-objects @2026-07-21
// - CPU Rate Control: https://learn.microsoft.com/en-us/windows/win32/procthread/job-objects#cpu-rate-control @2026-07-21
package sandbox

import (
	"fmt"
	"os/exec"
	"unsafe"

	"golang.org/x/sys/windows"
)

// ====== Win32 API 常量/类型（golang.org/x/sys/windows 未内建）======
//
// JOBOBJECTINFOCLASS 值参考 winnt.h（无类型便于在 SetInformation/QueryInformation 不同
// 签名参数间互转；golang.org/x/sys/windows 对 SetInformation 用 uint32、
// QueryInformation 用 int32）。
//
//	JobObjectCpuRateControlInformation = 15 (Win8+/Server 2012+)
//	JobObjectIoRateControlInformation = 21 (Win10+/Server 2016+)
//
// JOB_OBJECT_CPU_RATE_CONTROL_* (winnt.h)：
//
//	ENABLE 0x1 — 启用速率控制
//	WEIGHTED 0x2 — 按权重（与 HARD_CAP 互斥）
//	HARD_CAP 0x4 — 超过即挂起（硬节流，而非通知）
//	NOTIFY 0x20 — 超阈值时注册通知（与 HARD_CAP 互斥）
//
// HARD_CAP + ENABLE 模式：CpuRate = 百分比 × 100，取值 [1, 10000]。
// 例：50% = 5000，10% = 1000。
const (
	jobObjectCpuRateInfoClass = 15 // int 无类型常量
	jobObjectIoRateInfoClass  = 21

	JOB_OBJECT_CPU_RATE_CONTROL_ENABLE   = uint32(0x00000001)
	JOB_OBJECT_CPU_RATE_CONTROL_WEIGHTED = uint32(0x00000002)
	JOB_OBJECT_CPU_RATE_CONTROL_HARD_CAP = uint32(0x00000004)
	JOB_OBJECT_CPU_RATE_CONTROL_NOTIFY   = uint32(0x00000020)
)

// jobObjectCPURateInfo 与 JOBOBJECT_CPU_RATE_CONTROL_INFORMATION (winnt.h) 内存布局一致。
// HARD_CAP 模式：CpuRate ∈ [1, 10000]（百分比 × 100）。
type jobObjectCPURateInfo struct {
	ControlFlags uint32
	CpuRate      uint32 // union: CpuRate / Weight / {MinRate, MaxRate}
}

// windowsSandbox 是 Windows Job Object 沙箱实现。
type windowsSandbox struct {
	cfg       WindowsSandboxConfig
	jobHandle windows.Handle
	audit     *AuditLog
	level     SandboxLevel
	closed    bool
}

// newPlatformSandbox 是 Windows 平台的 OS 沙箱工厂（覆盖 os_sandbox_other.go fallback）。
// Manager.Prepare 调用约定：newPlatformSandbox(cfg.OSLevel, m.audit)。
func newPlatformSandbox(cfg OSLevelConfig, audit *AuditLog) OSSandbox {
	// 零值配置 → degradedSandbox（Win10 真机 E2E 首跑教训：此前零值仍返回
	// windowsSandbox，Platform()="windows"，违反三平台统一契约
	// "Use*=false 时降级"，TestNewPlatformSandbox_DefaultDisabled FAIL）。
	if !cfg.Windows.UseJobObject {
		return newDegradedSandbox("windows job object disabled by config", audit)
	}
	return NewWindowsSandbox(cfg.Windows, audit, SandboxLevelStrict)
}

func NewWindowsSandbox(cfg WindowsSandboxConfig, audit *AuditLog, level SandboxLevel) *windowsSandbox {
	w := &windowsSandbox{cfg: cfg, audit: audit, level: level}
	if cfg.UseJobObject {
		if cfg.MemoryLimit == 0 {
			cfg.MemoryLimit = DefaultWindowsMemoryLimit
		}
		if cfg.MaxProcesses == 0 {
			cfg.MaxProcesses = DefaultWindowsMaxProcesses
		}
		w.cfg = cfg
	}
	return w
}

func (w *windowsSandbox) Platform() string { return "windows" }

// describeIsolationFeatures 返回 Windows 实际生效的隔离特性（门 2 降级可见性）。
// 仅资源限制级（Job Object）；访问控制缺口（GAP-WIN-P2/P3）不在特性中宣称。
func describeIsolationFeatures(cfg OSLevelConfig) []string {
	f := fmt.Sprintf("job-object(mem=%dM,procs=%d", cfg.Windows.MemoryLimit>>20, cfg.Windows.MaxProcesses)
	if cfg.Windows.CPUMaxPercent > 0 {
		f += fmt.Sprintf(",cpu=%d%%", cfg.Windows.CPUMaxPercent)
	}
	f += ")"
	return []string{f}
}

// isLinuxLandlockBuild 报告当前构建是否含 Landlock 路径（仅 linux）。
func isLinuxLandlockBuild() bool { return false }

func (w *windowsSandbox) Enter() (handle interface{}, cleanup func(), err error) {
	if !w.cfg.UseJobObject {
		if w.audit != nil {
			w.audit.LogDegradation(w.level, SandboxLevelRelaxed, "Windows Job Object disabled in config")
		}
		return nil, func() {}, nil
	}
	h, cerr := windows.CreateJobObject(nil, nil)
	if cerr != nil {
		if w.audit != nil {
			w.audit.LogDegradation(w.level, SandboxLevelRelaxed,
				fmt.Sprintf("CreateJobObject failed: %v", cerr))
		}
		return nil, func() {}, nil
	}
	w.jobHandle = h

	if slerr := w.setExtendedLimits(); slerr != nil {
		windows.CloseHandle(w.jobHandle)
		w.jobHandle = 0
		if w.audit != nil {
			w.audit.LogDegradation(w.level, SandboxLevelRelaxed,
				fmt.Sprintf("SetInformationJobObject(Extended) failed: %v", slerr))
		}
		return nil, func() {}, nil
	}

	// P2-1 CPU Rate：设置失败仅打 canary（不中断 Job 主路径，fail-open + 可观测）。
	if cpuErr := w.setCPURate(); cpuErr != nil && w.audit != nil {
		w.audit.LogDegradation(w.level, w.level,
			fmt.Sprintf("GAP-WIN-P2-1 CPU rate control skipped (SetInformationJobObject CpuRate: %v)", cpuErr))
	}
	// P2-1 IO Rate：预留，设置失败仅 canary。
	if ioErr := w.setIORate(); ioErr != nil && w.audit != nil {
		w.audit.LogDegradation(w.level, w.level,
			fmt.Sprintf("GAP-WIN-P2-1 IO rate control skipped (SetInformationJobObject IoRate: %v)", ioErr))
	}

	cleanup = func() {
		if w.jobHandle != 0 {
			_ = windows.TerminateJobObject(w.jobHandle, 1) // 退出码 1：被 Job 终结的进程不得报告 exit 0（Win10 真机教训：exit 0 伪装成功，父进程无法区分正常退出与被杀）
			windows.CloseHandle(w.jobHandle)
			w.jobHandle = 0
		}
	}
	return w.jobHandle, cleanup, nil
}

func (w *windowsSandbox) Exit() error {
	if w.closed {
		return nil
	}
	w.closed = true
	if w.jobHandle != 0 {
		_ = windows.TerminateJobObject(w.jobHandle, 1) // 退出码 1：被 Job 终结的进程不得报告 exit 0（Win10 真机教训：exit 0 伪装成功，父进程无法区分正常退出与被杀）
		windows.CloseHandle(w.jobHandle)
		w.jobHandle = 0
	}
	return nil
}

func (w *windowsSandbox) Restrict(cmd *exec.Cmd) error { _ = cmd; return nil }

// PostStartHook 在进程启动后将其 Assign 至 Job Object（含子进程继承）。
// 由于 exec.Cmd.Process 不直接暴露 Handle 字段，使用 OpenProcess 拿
// PROCESS_SET_QUOTA 访问句柄后 Assign（与同类 CLI code Windows 沙箱路径一致）。
func (w *windowsSandbox) PostStartHook() func(*exec.Cmd) error {
	if w.jobHandle == 0 {
		return nil
	}
	return func(cmd *exec.Cmd) error {
		if cmd == nil || cmd.Process == nil {
			return nil
		}
		const access = windows.PROCESS_SET_QUOTA | windows.PROCESS_TERMINATE | windows.PROCESS_DUP_HANDLE
		proc, oerr := windows.OpenProcess(access, false, uint32(cmd.Process.Pid))
		if oerr != nil {
			return fmt.Errorf("OpenProcess for job assignment: %w", oerr)
		}
		defer windows.CloseHandle(proc)
		return windows.AssignProcessToJobObject(w.jobHandle, proc)
	}
}

// ===== internal helpers =====

func (w *windowsSandbox) setExtendedLimits() error {
	var info windows.JOBOBJECT_EXTENDED_LIMIT_INFORMATION
	info.BasicLimitInformation.LimitFlags = windows.JOB_OBJECT_LIMIT_KILL_ON_JOB_CLOSE
	if w.cfg.MemoryLimit > 0 {
		info.BasicLimitInformation.LimitFlags |= windows.JOB_OBJECT_LIMIT_PROCESS_MEMORY
		info.ProcessMemoryLimit = uintptr(w.cfg.MemoryLimit)
	}
	if w.cfg.MaxProcesses > 0 {
		info.BasicLimitInformation.LimitFlags |= windows.JOB_OBJECT_LIMIT_ACTIVE_PROCESS
		info.BasicLimitInformation.ActiveProcessLimit = w.cfg.MaxProcesses
	}
	// windows.SetInformationJobObject 签名：(Handle, uint32, uintptr, uint32) → (int, error)
	_, err := windows.SetInformationJobObject(w.jobHandle,
		windows.JobObjectExtendedLimitInformation,
		uintptr(unsafe.Pointer(&info)), uint32(unsafe.Sizeof(info)))
	return err
}

// setCPURate 写入 JOBOBJECT_CPU_RATE_CONTROL_INFORMATION（HARD_CAP 模式）。
// CPUMaxPercent ∈ [1,100]；0/负 → 不调用。
// 错误返回用于 audit canary（调用方 fail-open，不中断主流程）。
func (w *windowsSandbox) setCPURate() error {
	if w.cfg.CPUMaxPercent == 0 {
		return nil
	}
	pct := w.cfg.CPUMaxPercent
	if pct > 100 {
		pct = 100
	}
	cpuRate := pct * 100
	if cpuRate < 1 {
		cpuRate = 1
	}
	if cpuRate > 10000 {
		cpuRate = 10000
	}
	info := jobObjectCPURateInfo{
		ControlFlags: JOB_OBJECT_CPU_RATE_CONTROL_ENABLE | JOB_OBJECT_CPU_RATE_CONTROL_HARD_CAP,
		CpuRate:      cpuRate,
	}
	// 2nd param uint32
	_, err := windows.SetInformationJobObject(w.jobHandle, uint32(jobObjectCpuRateInfoClass),
		uintptr(unsafe.Pointer(&info)), uint32(unsafe.Sizeof(info)))
	return err
}

// setIORate 预留实现：Win10+ JOBOBJECT_IO_RATE_CONTROL_INFORMATION_NATIVE_V2。
// 因 x/sys/windows 未内建结构/常量，在 cfg.IORateMax > 0 时返回描述性错误
// （由上层写入 canary 降级日志）。
// TODO(P2): 导入 Native_V2 结构 + SetInformationJobObject 写入。
func (w *windowsSandbox) setIORate() error {
	if w.cfg.IORateMax == 0 {
		return nil
	}
	return fmt.Errorf("IO rate control struct (Native_V2) not yet bound to x/sys; "+
		"max bytes/sec intent: %d (canary only)", w.cfg.IORateMax)
}

// ReadCPURate 读取当前 Job 的 CPU rate 配置（测试结果断言用）。
// 返回：(controlFlags, cpuRatePercent, error)；未启用时 pct=0。
func (w *windowsSandbox) ReadCPURate() (uint32, uint32, error) {
	if w.jobHandle == 0 {
		return 0, 0, fmt.Errorf("no job handle")
	}
	var info jobObjectCPURateInfo
	var returned uint32
	// windows.QueryInformationJobObject 签名：
	// func(job Handle, infoClass int32, infoPtr uintptr, size uint32, returned *uint32) error
	err := windows.QueryInformationJobObject(w.jobHandle, int32(jobObjectCpuRateInfoClass),
		uintptr(unsafe.Pointer(&info)), uint32(unsafe.Sizeof(info)), &returned)
	if err != nil {
		return 0, 0, err
	}
	pct := uint32(0)
	enabled := info.ControlFlags&JOB_OBJECT_CPU_RATE_CONTROL_ENABLE != 0
	hard := info.ControlFlags&JOB_OBJECT_CPU_RATE_CONTROL_HARD_CAP != 0
	if enabled && hard && info.CpuRate >= 100 {
		pct = info.CpuRate / 100
	}
	return info.ControlFlags, pct, nil
}
