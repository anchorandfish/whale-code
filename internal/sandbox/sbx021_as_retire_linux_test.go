//go:build linux

// Package sandbox — SBX-021 判别性测试：Linux 侧 RLIMIT_AS **退役**。
//
// # 来由（SBX-001 的副作用 R1，已实测坐实）
//
// SBX-001 把资源限制前移到 execve 之前后，限制**从此必定生效**；而生产默认
// `MemoryMaxBytes=1GiB` 走 `RLIMIT_AS`（**单进程虚拟地址空间**）⇒ 实测误杀
// `go build ./internal/...`（含本仓 `internal/tui`，失败点 `modernc.org/libc`
// 报 `fatal error: out of memory`），无限制时正常。
//
// # 为何是"退役"而不是"把值调大"
//
// `RLIMIT_AS` 量的是**虚拟地址空间**，而"内存炸弹"要防的是**实际占用**。
// Go / JVM / .NET 普遍预留远大于实际使用的虚拟空间 ⇒ **工具与目标语义不匹配**：
// 既容易误杀，又量不到真正关心的东西。调 1GiB→2GiB→4GiB 属于"在错的尺子上找阈值"。
// ⇒ 与 macOS 取舍一致（`os_sandbox_darwin_rlimit.go:16-21` 早已因同类理由跳过 AS），
// Linux 侧让该限制退役；内存门改由 cgroup v2 `memory.max` 承担
// （SBX-019 路线 C，非本编号范围）。
//
// # 判据设计（结构面 + 申报面，不打票面）
//
//	判据①  resourceLimitTargets 不得含 RLIMIT_AS          （结构面）
//	判据②  配置了资源限制时申报必须存在                    （申报面：不许静默）
//	判据③  Enter 集成 —— 申报真的落到 AuditLog            （集成面）
//	对照   未配置资源限制时不得申报                        （防判据②恒真）
//
// 每条都写明反例注入方式；before 必须红。
package sandbox

import (
	"strings"
	"testing"

	"golang.org/x/sys/unix"
)

// TestSBX021_ASNotInTargets 判据①（结构面）：Linux 侧不得再对 RLIMIT_AS 施加限制。
//
// 断言 `resourceLimitTargets`（两条施加路径的**单一维护点**）不含 RLIMIT_AS。
// 用**资源号**而非显示名比较：改名绕不过本判据。
//
//	before 红：AS 行仍在 resourceLimitTargets 中 ⇒ 命中 ⇒ Fail
//	反例注入：把 {unix.RLIMIT_AS, limits.MemoryMaxBytes, "AS"} 加回该表 ⇒ 必须变红
func TestSBX021_ASNotInTargets(t *testing.T) {
	limits := ResourceLimits{
		CPUMaxSeconds:     42,
		MemoryMaxBytes:    1 << 30, // 生产默认：正是会误杀 go build ./internal/... 的那个值
		FileDescriptorMax: 512,
		ProcessMax:        77,
		FileSizeMaxBytes:  10 << 20,
	}
	for _, target := range resourceLimitTargets(limits) {
		if target.resource == unix.RLIMIT_AS {
			t.Fatalf("Linux 侧仍在施加 RLIMIT_AS（name=%q value=%d）—— SBX-021 退役未生效；"+
				"该限制量的是虚拟地址空间而非实际占用，会误杀 go build ./internal/...",
				target.name, target.value)
		}
	}
}

// TestSBX021_RetirementIsAnnounced 判据②（申报面）：配置了资源限制时必须申报 AS 退役。
//
// 依据："无出口的降级不可见"（status.go 文件头，静默防护教训）。
// darwin 在同位置做了同形申报：`os_sandbox_darwin.go:362-367` 装配期
// LogDegradation + `os_sandbox_darwin_rlimit.go:152-157` helper 内打印。
//
//	before 红：骨架返回空串 ⇒ Fail
//	反例注入：让 linuxRlimitDegradationNotice 恒返回 "" ⇒ 必须变红
func TestSBX021_RetirementIsAnnounced(t *testing.T) {
	notice := linuxRlimitDegradationNotice(ResourceLimits{MemoryMaxBytes: 1 << 30, ProcessMax: 256})
	if notice == "" {
		t.Fatal("配置了资源限制却无降级申报 —— AS 退役成了**静默降级**（用户会以为内存门还在）")
	}
	if !strings.Contains(notice, "RLIMIT_AS") {
		t.Fatalf("申报未点明被退役的限制名（用户无法据此判断失去的是哪道门）：%q", notice)
	}
}

// TestSBX021_NoNoticeWhenNoLimits 对照：未配置资源限制时不得申报（防判据②恒真）。
//
// 这条在 before 与 after 都应通过 —— 它的作用是证明判据②**不是恒真**：
// 若某天有人把申报写成"无条件返回固定文案"，本对照会立刻变红。
func TestSBX021_NoNoticeWhenNoLimits(t *testing.T) {
	if notice := linuxRlimitDegradationNotice(ResourceLimits{}); notice != "" {
		t.Fatalf("未配置资源限制却申报了降级 —— 判据②恒真，失去判别力：%q", notice)
	}
}

// TestSBX021_EnterRecordsAnnouncement 判据③（集成）：申报必须真的落到 AuditLog。
//
// 走生产同一装配入口（newPlatformSandbox → Enter），断言 DegradeReasons 含申报。
// 这是"申报函数存在"与"申报真的发出去了"之间的差别 —— 只看判据②会漏掉
// "函数写好了但没人调用"这类断线（历史教训：接线端缺失）。
//
//	反例注入：删掉 Enter 中对 linuxRlimitDegradationNotice 的调用 ⇒ 必须变红
//
// 环境不满足（未走 linuxNamespaceSandbox / Enter 失败）时 skip，不制造假绿。
func TestSBX021_EnterRecordsAnnouncement(t *testing.T) {
	al := NewAuditLog()
	cfg := OSLevelConfig{
		Linux: LinuxSandboxConfig{
			UseNamespaces:  true,
			Namespaces:     []string{"pid"},
			ResourceLimits: ResourceLimits{MemoryMaxBytes: 1 << 30, ProcessMax: 256},
		},
	}
	sb := newPlatformSandbox(cfg, al)
	l, ok := sb.(*linuxNamespaceSandbox)
	if !ok {
		t.Skipf("环境未走 linuxNamespaceSandbox（Platform=%q），跳过集成断言", sb.Platform())
	}
	if _, _, err := l.Enter(); err != nil {
		t.Skipf("Enter 在当前环境不可用，跳过集成断言: %v", err)
	}
	for _, reason := range al.DegradeReasons() {
		if strings.Contains(reason, "RLIMIT_AS") {
			return
		}
	}
	t.Fatalf("Enter 后 AuditLog 无 AS 退役申报 —— 申报函数未被接线（降级静默不可见）：%v",
		al.DegradeReasons())
}
