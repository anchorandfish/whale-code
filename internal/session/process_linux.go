//go:build linux

package session

import (
	"fmt"
	"os"
	"strconv"
	"strings"
)

// getProcessStartTimeOS returns the process start time on Linux by reading
// /proc/<pid>/stat. Field 22 (1-indexed: field 21 in 0-indexed split) is the
// starttime measured in clock ticks since boot. This value is stable for the
// lifetime of a process and differs across process instances that happen to
// share a PID, which is exactly what we need for PID-reuse detection.
func getProcessStartTimeOS(pid int) (int64, error) {
	data, err := os.ReadFile(fmt.Sprintf("/proc/%d/stat", pid))
	if err != nil {
		return 0, fmt.Errorf("read /proc/%d/stat: %w", pid, err)
	}

	// /proc/<pid>/stat fields are space-separated, but the comm field (field 2)
	// may contain spaces and is enclosed in parentheses. Split from the last
	// ')' to skip the comm field safely.
	s := string(data)
	lastParen := strings.LastIndexByte(s, ')')
	if lastParen < 0 || lastParen+2 > len(s) {
		return 0, fmt.Errorf("invalid /proc/%d/stat: no comm field", pid)
	}
	rest := s[lastParen+2:]
	fields := strings.Fields(rest)
	// After the comm field, field 3 starts at fields[0] — i.e. fields[i] is
	// overall field i+3. starttime is overall field 22 (man proc(5)), which is
	// fields[19]. ★ （2026-10-11 Linux VM 真机定谳）：这里曾差一位写成 20，
	// 实际取到第 23 字段 = vsize（虚拟内存字节数）——锁元数据因此写入 vsize，
	// 读侧「当前值==记录值」判活在活进程上必然偶发失配 ⇒ 会话双开互写
	// （数据损坏场景）。守卫测试 TestGetProcessStartTime_BoundedByBootElapsedTicks
	// 用「starttime ≤ 开机以来滴答数」的物理上界钉住本字段，取错必红。
	const starttimeFieldIndex = 19 // 0-indexed in the post-comm split
	if len(fields) <= starttimeFieldIndex {
		return 0, fmt.Errorf("invalid /proc/%d/stat: not enough fields", pid)
	}
	starttime, err := strconv.ParseInt(fields[starttimeFieldIndex], 10, 64)
	if err != nil {
		return 0, fmt.Errorf("parse starttime: %w", err)
	}
	// We return the raw clock-tick value. It only needs to be comparable, not
	// a wall-clock timestamp; two different process instances with the same
	// PID will have different starttime values.
	return starttime, nil
}
