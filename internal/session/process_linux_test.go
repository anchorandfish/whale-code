//go:build linux

package session

import (
	"os"
	"strconv"
	"strings"
	"testing"
)

// 判别性守卫：getProcessStartTime 必须返回 /proc/<pid>/stat 第 22 字段
// （starttime，开机以来的时钟滴答），而不是相邻字段。
//
// 历史缺陷（2026-10-11 Linux VM 真机定谳）：post-comm 切分索引差一位，
// 实际取到第 23 字段 = vsize（虚拟内存字节数）。后果链：锁元数据写入 vsize，
// 读侧用「当前 vsize == 记录 vsize」判活——活进程 vsize 随分配漂移 ⇒ 偶发
// 误判「holder 已死」⇒ best-effort 放行写入 ⇒ 会话双开互写（数据损坏
// 场景在 Linux 真实可发生）。mac 走 sysctl 路径无此缺陷，故 mac 全绿掩盖。
//
// 判别器不复制解析逻辑，用物理上界： starttime ≤ 开机以来的滴答总数
// （/proc/uptime × CLK_TCK）。Go 进程 vsize ≈ 1e9 字节，滴答 ≈ 1e6 量级，
// 差 3 个数量级 ⇒ 取错字段必红。
func TestGetProcessStartTime_BoundedByBootElapsedTicks(t *testing.T) {
	uptimeData, err := os.ReadFile("/proc/uptime")
	if err != nil {
		t.Skipf("/proc/uptime 不可读: %v", err)
	}
	uptimeSec, err := strconv.ParseFloat(strings.Fields(string(uptimeData))[0], 64)
	if err != nil {
		t.Skipf("/proc/uptime 解析失败: %v", err)
	}
	// CLK_TCK：Linux 用户态惯例恒为 100（getconf CLK_TCK 实测）。判别器只依赖
	// 「滴答与字节数差 3 个数量级」，即使 HZ 取值有出入也不影响红绿判别。
	const clkTck = 100
	maxTicks := int64(uptimeSec * clkTck)

	pid := os.Getpid()
	got, err := getProcessStartTime(pid)
	if err != nil {
		t.Fatalf("getProcessStartTime(%d): %v", pid, err)
	}
	if got <= 0 {
		t.Fatalf("getProcessStartTime(%d) = %d，非正值不可能", pid, got)
	}
	if got > maxTicks {
		t.Fatalf("getProcessStartTime(%d) = %d 超过开机以来滴答上界 %d "+
			"(uptime=%.1fs × %dHz) ⇒ 取到了非 starttime 字段（历史缺陷：vsize，）",
			pid, got, maxTicks, uptimeSec, clkTck)
	}
	// 稳定性：同一进程两次调用必须相等（锁元数据「写入侧/读取侧」两处调用的契约）。
	got2, err := getProcessStartTime(pid)
	if err != nil || got2 != got {
		t.Fatalf("getProcessStartTime(%d) 不稳定: %d vs %d (err=%v) ⇒ 元数据比对必偶发误判", pid, got, got2, err)
	}
}
