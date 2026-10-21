// Package session — cross-platform process start-time detection.
//
// This is used by the file-lock (SessionLock.ProcessStart) to guard against
// PID reuse: a lock holder that died and whose PID was later assigned to a
// new process would otherwise be misidentified as "still alive". Comparing
// the recorded process start-time against the live process's start-time
// makes the check robust.
//
// Platform implementations:
// - Linux: /proc/<pid>/stat field 22 (starttime in clock ticks since boot)
// - macOS: sysctl(KERN_PROC_PID) -> kinfo_proc.kp_proc.p_starttime
// - Windows: GetProcessTimes syscall (primary); wmic fallback for systems
// where the syscall path is unavailable. Note wmic is deprecated
// on Windows 11 24H2+; GetProcessTimes is always available.
package session

import (
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/anchorandfish/whale-code/internal/platform"
)

// getProcessStartTime returns the start time of the process identified by pid
// as a comparable integer (not necessarily a Unix timestamp — it only needs to
// be stable and unique per process instance on a given host). Returns an error
// if the process does not exist or the start time cannot be determined.
//
// Callers compare the returned value against the value recorded in the lock
// file; a mismatch indicates the original holder has exited and the PID has
// been recycled.
func getProcessStartTime(pid int) (int64, error) {
	if pid <= 0 {
		return 0, fmt.Errorf("invalid pid %d", pid)
	}
	return getProcessStartTimeOS(pid)
}

// isProcessRunning reports whether the process with the given pid is currently
// alive. Combine it with getProcessStartTime for PID-reuse protection.
//
// ★ 判据唯一来源：internal/platform.ProcessAlive（收敛，2026-09-20）。
//
// 修复前本函数自带一份实现：`os.FindProcess(pid)` + `proc.Signal(syscall.Signal(0)) == nil`。
// `os.FindProcess` 在 Unix 上**永不失败**（它不查进程是否存在，只构造句柄），
// 于是判定完全落在 `Signal(0)` 上 —— 而 `Signal(0)` 走 `syscall.Kill`，
// **EPERM 会作为 error 返回**。EPERM 的语义是「进程存在，只是本进程无权给它发信号」，
// 所以修复前的写法把「存在但无权发信号」判成了**已退出**。
//
// 这不是边角情形：与 `pid=1`（launchd/init）比较时**任何**用户都会拿到 EPERM。
// 后果与 F11 L1（误夺锁 ⇒ 数据丢失）同向：store.go 的锁有效性判定读到这里时，
// 「持有者还活着」会被判成「持有者已死」⇒ 夺锁 ⇒ 两个会话同时写同一份会话存档。
//
// Windows 的 tasklist 分支也一并删除：platform 的 Windows 实现用 OpenProcess，
// 不依赖解析 tasklist 文本（原实现有「PID 前缀误命中」风险）。
func isProcessRunning(pid int) bool {
	return platform.ProcessAlive(pid)
}

// parseWmicTime parses a WMIC CreationDate value into a comparable integer.
// WMIC format: "20260624123045.123456+480" (YYYYMMDDHHMMSS.ffffff+TZ).
// We convert it to a Unix millisecond timestamp so it is stable across calls
// and comparable. This is the fallback path for Windows when GetProcessTimes
// is unavailable; the primary path uses the syscall directly.
//
// Returns 0 if the string cannot be parsed (callers should treat 0 as
// "unknown" and fall back to weaker liveness checking rather than failing
// the whole lock acquisition).
func parseWmicTime(s string) int64 {
	s = strings.TrimSpace(s)
	if len(s) < 14 {
		return 0
	}
	// Layout: 20260624123045.123456+480
	// 0 1
	// 0123456789012345...
	year, _ := strconv.Atoi(s[0:4])
	month, _ := strconv.Atoi(s[4:6])
	day, _ := strconv.Atoi(s[6:8])
	hour, _ := strconv.Atoi(s[8:10])
	minute, _ := strconv.Atoi(s[10:12])
	second, _ := strconv.Atoi(s[12:14])
	if year < 1970 || month < 1 || month > 12 || day < 1 || day > 31 {
		return 0
	}
	if hour < 0 || hour > 23 || minute < 0 || minute > 59 || second < 0 || second > 60 {
		return 0
	}

	// Build a time.Time in the local zone (WMIC reports local time with a
	// timezone offset, but for PID-reuse detection we only need a stable
	// comparable value; the absolute instant does not matter).
	t := time.Date(year, time.Month(month), day, hour, minute, second, 0, time.Local)
	return t.UnixMilli()
}
