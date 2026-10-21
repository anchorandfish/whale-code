// Package sandbox — 资源限制公共类型与函数（M13-02 / P1-1）。
//
// 本文件不含 build tag，所有平台编译。
// 平台特定实现在 resource_enforce.go(!linux && !darwin) /
// resource_enforce_linux.go(linux) / resource_enforce_darwin.go(darwin)。

package sandbox

import (
	"context"
	"fmt"
	"os/exec"
	"sync"
	"time"
)

// ResourceLimits 是子进程资源上限配置（M13-02 SNAPSHOT）。
//
// 零值 = 不限制（与现状一致）。各字段独立，调用方按需启用。
type ResourceLimits struct {
	// CPUMaxSeconds CPU 时间上限（秒，0=不限制）。
	// 超出时子进程收到 SIGXCPU（Linux）或被 Job Object 终止（Windows）。
	CPUMaxSeconds uint64

	// MemoryMaxBytes 内存上限（字节，0=不限制）。
	//
	// ⚠️ 平台差异（SBX-021，2026-09-25）—— **只有 Windows 真正使用本字段**：
	// - Windows：Job Object 的 MemoryLimit（production.go:94），按**整个笼子**计量；
	// - Linux：**已退役**。原走 RLIMIT_AS（量虚拟地址空间，非实际占用），实测会
	// 误杀 `go build ./internal/...`（1GiB FAIL / 2GiB OK）⇒ 内存门改由
	// cgroup v2 `memory.max` 承担（SBX-019 路线 C，尚未落地）。退役事实经
	// linuxRlimitDegradationNotice → AuditLog 对用户可见（不许静默）；
	// - macOS：早已跳过 RLIMIT_AS（os_sandbox_darwin_rlimit.go:16-21），同理。
	//
	// ⇒ 在 Linux / macOS 上"本字段有值 ≠ 内存门存在"。`Validate` 仍要求它 > 0，
	// 是因为 Windows 需要它 —— **不要**据此在 Linux 上宣称内存受限。
	MemoryMaxBytes uint64

	// FileDescriptorMax 文件描述符上限（0=不限制）。
	// 防止子进程耗尽系统 fd 池（如打开大量 socket）。
	FileDescriptorMax uint64

	// ProcessMax 进程数上限（0=不限制，防 fork bomb）。
	// 与 validator.checkForkBomb 互补：checkForkBomb 静态检测 fork 炸弹 pattern；
	// ProcessMax 运行时硬限制即使绕过静态检测的实际 fork 数。
	ProcessMax uint64

	// FileSizeMaxBytes 单文件大小上限（字节，0=不限制）。
	// 防止子进程写入超大文件耗尽磁盘。
	FileSizeMaxBytes uint64
}

// Validate 验证 ResourceLimits 关键字段范围，返回明确的错误信息。
// 验证项：MemoryMaxBytes > 0（sandbox 内存限制）、ProcessMax > 0（sandbox 进程数限制）。
// 零值 ResourceLimits（全 0）视为"不限制"，跳过验证返回 nil。
func (r ResourceLimits) Validate() error {
	// 零值表示不限制，跳过验证
	if r == (ResourceLimits{}) {
		return nil
	}
	if r.MemoryMaxBytes == 0 {
		return fmt.Errorf("resource limits: MemoryMaxBytes must be > 0 when resource limits are set, got 0")
	}
	if r.ProcessMax == 0 {
		return fmt.Errorf("resource limits: ProcessMax must be > 0 when resource limits are set, got 0")
	}
	return nil
}

// DefaultResourceLimits 返回保守的默认资源上限（M13-02）。
//
// 默认值选择依据（对标 Linux 容器默认）：
// - CPU: 600s（10 分钟，覆盖典型工具调用，超时认为死循环）
// - Memory: 1GB（足够编译/测试，限制失控内存增长）
// - Fd: 1024（典型 ulimit -n 默认值，防止 fd 泄漏）
// - Process: 256（防 fork bomb，但允许合理并行子进程）
// - FileSize: 100MB（单文件大小，防超大日志/数据文件）
//
// 调用方按需覆盖（如编译任务可提高 MemoryMaxBytes 到 4GB）。
func DefaultResourceLimits() ResourceLimits {
	return ResourceLimits{
		CPUMaxSeconds:     600,
		MemoryMaxBytes:    1 * 1024 * 1024 * 1024, // 1GB
		FileDescriptorMax: 1024,
		ProcessMax:        256,
		FileSizeMaxBytes:  100 * 1024 * 1024, // 100MB
	}
}

// ApplyResourceLimits 在 exec.Cmd 启动后应用资源限制（P1-1 导出）。
//
// 平台行为：
// - Linux：通过 prlimit(pid, ...) 设置 RLIMIT_CPU/AS/NOFILE/NPROC/FSIZE
// - Windows：依赖 Job Object（已在 os_sandbox_windows.go 实现 MemoryLimit）
// - macOS/Darwin：降级为 no-op（Seatbelt profile limit 指令实现）
// - 其他平台：no-op + AuditLog 记录
//
// 注：本函数在 cmd.Start() 之后调用（此时子进程 PID 已知，可 prlimit 设置）。
//
// 并发安全：无内部状态，纯函数式调用。
func ApplyResourceLimits(cmd *exec.Cmd, limits ResourceLimits, audit *AuditLog) error {
	if limits == (ResourceLimits{}) {
		return nil // 全部为 0，跳过
	}
	return platformApplyResourceLimits(cmd, limits, audit)
}

// === M13-04: 审计日志索引 ===
//
// 设计目标：
// - 对 AuditLog 日志按 CheckID/Severity/Time 建立索引，支持快速查询
// - 用于安全运营：统计高频违规 CheckID、查询特定时间段的违规、按严重度过滤
// - 索引在内存构建（启动时扫描日志文件），不持久化（重建成本低）
// - 后台 goroutine 增量更新（避免阻塞主路径写入）

// AuditIndex 是审计日志索引（M13-04 SNAPSHOT）。
//
// 索引结构：
// - byCheckID: CheckID → 出现次数（统计高频违规）
// - bySeverity: Severity → 出现次数（统计严重度分布）
// - byDate: 日期（YYYYMMDD）→ 出现次数（按日统计）
// - totalEntries: 总条目数
type AuditIndex struct {
	mu           sync.RWMutex
	byCheckID    map[string]int
	bySeverity   map[string]int
	byDate       map[string]int
	totalEntries int
}

// NewAuditIndex 创建空索引。
func NewAuditIndex() *AuditIndex {
	return &AuditIndex{
		byCheckID:  make(map[string]int),
		bySeverity: make(map[string]int),
		byDate:     make(map[string]int),
	}
}

// IndexEntry 索引单条审计日志记录。
func (ai *AuditIndex) IndexEntry(checkID, severity, date string) {
	ai.mu.Lock()
	defer ai.mu.Unlock()
	if checkID != "" {
		ai.byCheckID[checkID]++
	}
	if severity != "" {
		ai.bySeverity[severity]++
	}
	if date != "" {
		ai.byDate[date]++
	}
	ai.totalEntries++
}

// TopCheckIDs 返回出现次数最多的 N 个 CheckID（降序）。
func (ai *AuditIndex) TopCheckIDs(n int) []CheckIDCount {
	ai.mu.RLock()
	defer ai.mu.RUnlock()
	if n <= 0 || n > len(ai.byCheckID) {
		n = len(ai.byCheckID)
	}
	out := make([]CheckIDCount, 0, len(ai.byCheckID))
	for id, count := range ai.byCheckID {
		out = append(out, CheckIDCount{CheckID: id, Count: count})
	}
	for i := 0; i < len(out); i++ {
		for j := i + 1; j < len(out); j++ {
			if out[j].Count > out[i].Count {
				out[i], out[j] = out[j], out[i]
			}
		}
	}
	if len(out) > n {
		out = out[:n]
	}
	return out
}

// CheckIDCount 是 TopCheckIDs 的返回项。
type CheckIDCount struct {
	CheckID string
	Count   int
}

// SeverityCounts 返回各严重度的出现次数。
func (ai *AuditIndex) SeverityCounts() map[string]int {
	ai.mu.RLock()
	defer ai.mu.RUnlock()
	out := make(map[string]int, len(ai.bySeverity))
	for k, v := range ai.bySeverity {
		out[k] = v
	}
	return out
}

// DateCounts 返回各日期的日志条目数（用于容量规划）。
func (ai *AuditIndex) DateCounts() map[string]int {
	ai.mu.RLock()
	defer ai.mu.RUnlock()
	out := make(map[string]int, len(ai.byDate))
	for k, v := range ai.byDate {
		out[k] = v
	}
	return out
}

// TotalEntries 返回总索引条目数。
func (ai *AuditIndex) TotalEntries() int {
	ai.mu.RLock()
	defer ai.mu.RUnlock()
	return ai.totalEntries
}

// Reset 清空索引（用于热重载场景）。
func (ai *AuditIndex) Reset() {
	ai.mu.Lock()
	defer ai.mu.Unlock()
	ai.byCheckID = make(map[string]int)
	ai.bySeverity = make(map[string]int)
	ai.byDate = make(map[string]int)
	ai.totalEntries = 0
}

// === M13-06: 进程组超时 ===
//
// 设计目标：
// - 为进程组执行增加超时支持（cmd.Wait 永久阻塞 → 带超时等待）
// - 超时后强制 kill 整个进程组（避免孤儿进程）
// - 跨平台：Linux 用 Setpgid + kill(-pgid)，Windows 用 CREATE_NEW_PROCESS_GROUP + taskkill

// ProcessGroupTimeoutResult 是进程组执行结果（M13-06 SNAPSHOT）。
type ProcessGroupTimeoutResult struct {
	ExitCode int
	TimedOut bool
	Duration int64 // 纳秒
	Err      error
}

// RunWithTimeout 启动命令并在超时后强制终止整个进程组（M13-06）。
func RunWithTimeout(cmd *exec.Cmd, timeout time.Duration) ProcessGroupTimeoutResult {
	if err := cmd.Start(); err != nil {
		return ProcessGroupTimeoutResult{
			ExitCode: -1,
			Err:      fmt.Errorf("cmd.Start failed: %w", err),
		}
	}
	pid := cmd.Process.Pid

	if timeout <= 0 {
		err := cmd.Wait()
		exitCode := -1
		if cmd.ProcessState != nil {
			exitCode = cmd.ProcessState.ExitCode()
		}
		return ProcessGroupTimeoutResult{
			ExitCode: exitCode,
			Err:      err,
		}
	}

	// M13-06 的超时分支**委托到唯一核心**（process_wait.go 的 waitProcessGroup）——
	// 修复前本文件自有一份「goroutine Wait ＋ time.After ＋ 事后 KillProcessGroup」，
	// 与 bash/plugin/hook 三处的写法并存 ⇒ 仓内出现多份同语义实现（型复发坑）。
	// 委托后签名、返回结构、语义均不变（TimedOut / ExitCode / Duration / Err）。
	startTime := time.Now().UnixNano()
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	res, err := waitProcessGroup(ctx, cmd)

	exitCode := -1
	if cmd.ProcessState != nil {
		exitCode = cmd.ProcessState.ExitCode()
	}
	if res.TimedOut {
		return ProcessGroupTimeoutResult{
			ExitCode: -1,
			TimedOut: true,
			Duration: time.Now().UnixNano() - startTime,
			Err:      fmt.Errorf("process group %d timed out after %v", pid, timeout),
		}
	}
	return ProcessGroupTimeoutResult{
		ExitCode: exitCode,
		Duration: time.Now().UnixNano() - startTime,
		Err:      err,
	}
}
