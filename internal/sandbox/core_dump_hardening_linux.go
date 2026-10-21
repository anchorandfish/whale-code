//go:build linux

// Package sandbox — Linux 防 core dump 加固（SBX-020）。
//
// # 要防什么
//
// core dump 会把进程的**整个内存镜像**写到磁盘（沿用 core_pattern，常见落在
// 工作区或 /var/crash）。沙箱内跑的是 AI 生成的命令，其内存里可能含本次读入的
// 密钥/令牌/源码；dump 文件由内核以目标进程权限写出，可能正好落在工作区里被
// 后续命令或用户读到 —— 等于把沙箱内看到的秘密复制到沙箱外。
//
// # ★ 本文件只做一件事，且是**实测过有效**的那一件
//
//	RLIMIT_CORE = 0（软/硬上限都置 0）
//
// 为什么只做这一件、为什么**刻意不做** `prctl(PR_SET_DUMPABLE, 0)`：
//
// 票面（2026-09-24_001 §3 SBX-020）写的是"同类 CLI 做 CORE=0 + PR_SET_DUMPABLE,0"，
// 但核实源码后必须修正它的**作用域**，否则会把无效动作当成加固交付：
//
// 1. 同类 CLI 的 `prctl(PR_SET_DUMPABLE, 0)` 打在**它自己的进程**上 ——
// `pre_main_hardening()` 由 `#[ctor::ctor]` 在 pre-main 调用
// （对标证据件、:44-53），
// 调用点见 responses-api-proxy/src/main.rs:4-6；另有 `disable_process_dumping()`
// 只用于内部 bridge 进程（linux-sandbox/src/proxy_routing.rs:675-678）。
// **没有任何一处把它施加到"被沙箱的命令"上**。
// 2. 为什么不能施加到被沙箱的命令上：**execve 会把 dumpable 复位为 1**。
// 内核在 `begin_new_exec()` 里重算 dumpability，普通（非 setuid/setgid、
// 非特权变更）exec 一律回到 SUID_DUMP_USER=1。本仓实测（容器 golang:1.25 /
// 内核 5.10.76-linuxkit）：
//
//	BEFORE-SET: dumpable=1
//	AFTER-SET : dumpable=0 ← prctl 生效了
//	AFTER-EXEC: dumpable=1 ← execve 又把它复位了
//
// 复现脚本见 内部工作回执 回执 §"决定性实验"。
// 3. 而 pre-exec helper 的全部动作都发生在 **execve 之前**（这正是它的价值），
// 所以在此处 prctl(PR_SET_DUMPABLE,0) 对目标命令**物理无效**。留着它 =
// 制造"以为加了 ptrace 防护"的虚假保护感 —— 正是 SBX-014 谎报教训。
// ⇒ 由 `TestSBX020_PreExecDumpableDoesNotSurviveExecve` 装一道门，
// 防止后来者再把它加回来。
//
// # 未做但已知有效的方向（需单独拍板，不在本次范围）
//
// 把 `PR_SET_DUMPABLE=0` + `RLIMIT_CORE=0` 施加到 **whale 自身进程**（启动期，
// 等价 同类 CLI 的 pre-main ctor）。收益有二：① whale 内存里的密钥/会话不被同 uid
// 进程 ptrace 读取；② RLIMIT_CORE=0 会被**全部后代**继承，连不走 helper 的
// 命令路径也一并覆盖。代价：它改变 CLI 本体行为（非 root 调试器无法 attach），
// 且属"全局行为变更"，须用户拍板后再做。
//
// 官方源：
// - man getrlimit(2) RLIMIT_CORE: https://man7.org/linux/man-pages/man2/getrlimit.2.html
// - man prctl(2) PR_SET_DUMPABLE + "reset to 1 on execve": https://man7.org/linux/man-pages/man2/prctl.2.html

package sandbox

import (
	"fmt"

	"golang.org/x/sys/unix"
)

// applyCoreDumpHardeningToSelf 对**调用进程自身**施加防 core dump 加固。
//
// 只做一件事：RLIMIT_CORE 的软/硬上限都置 0。目标命令及其全部后代从此写不出
// core —— 因为 rlimit 随 execve 与 fork 继承，且**不会**被 execve 复位
// （与 dumpable 不同，见文件头第 2 点）。
//
// 硬上限置 0 对非特权进程不可逆，这正是意图：目标命令不得自行把 core dump
// 能力恢复回来（`setrlimit` 无法抬高 hard limit）。helper 进程随即 execve，
// 故该不可逆性只作用于目标命令，不波及 whale 本体。
//
// 失败即返回 error（由 helper fail-closed 退出 126）：与主路径资源限制同一
// 姿态 —— 装不上必须出声，不得静默降级成"以为加固了"（SBX-014 谎报教训）。
func applyCoreDumpHardeningToSelf() error {
	core := unix.Rlimit{Cur: 0, Max: 0}
	if err := unix.Setrlimit(unix.RLIMIT_CORE, &core); err != nil {
		return fmt.Errorf("core dump hardening: setrlimit(RLIMIT_CORE, 0): %w", err)
	}
	return nil
}
