// Package util — 网络共享卷（NFS/SMB 等）检测与锁迁移的超时策略。
//
// 本文件是 L1（会话文件锁迁移至内核 flock）的两个新增能力落点：
//
//  1. IsNetworkFS —— 存储类型检测。flock 在部分网络卷上会「本地成功但跨主机
//     无互斥」（silent success）：不报错、不阻塞，两个 host 同时认为「我持有锁」。
//     那正是要修的 P1 换个位置复现，且没有任何信号。因此检测必须是 fail-safe：
//     判定不确定 ⇒ 视为网络卷（不启用 flock + 告警），而不是「默认启用、出错再说」。
//
//  2. SessionLockTimeout —— session 锁的超时策略变量（见 filelock_timeout.go
//     的 WithFileLockTimeout）。≤0 归一化为默认值，杜绝把 0 传给库（0 在库侧
//     语义是「无限阻塞」，与「≤0 用默认」只是看起来像，不归一化就是静默埋雷）。
package util

import "time"

// defaultSessionLockTimeout 是 session 锁的默认超时。2s 对真实持有时间
// （微秒–毫秒级追加、≤256MiB 全量重写通常 ≪100ms）有约 3 个数量级余量，
// 同时把最坏 UI 停顿封顶在 2s（而非 WithFileLock 的无限挂起）。
const defaultSessionLockTimeout = 2 * time.Second

// SessionLockTimeout 是 session 锁获取的超时策略变量（策略数值，非 const，
// 沿 limits.go 的惯例）。≤0 表示「使用默认」（归一化见 effectiveSessionLockTimeout）。
//
// 可选接线 env WHALE_SESSION_LOCK_TIMEOUT（WHALE_* 前缀是本仓既有惯例）。
var SessionLockTimeout = defaultSessionLockTimeout

// EffectiveSessionLockTimeout 归一化超时值：≤0 视作「使用默认」，杜绝把 0
// 传给 WithFileLockTimeout / TryAcquireFileLockTimeout（那里 0 = 无限阻塞）。
func EffectiveSessionLockTimeout() time.Duration {
	return effectiveSessionLockTimeout(SessionLockTimeout)
}

// effectiveSessionLockTimeout 归一化超时值：≤0 视作「使用默认」。
func effectiveSessionLockTimeout(v time.Duration) time.Duration {
	if v <= 0 {
		return defaultSessionLockTimeout
	}
	return v
}

// IsNetworkFS 报告 path 所在文件系统是否为网络共享卷（NFS / SMB / AFP / WebDAV 等）。
//
// 返回值：
//   - network: 是否为网络卷
//   - fstype:  文件系统类型名（供告警文案指名，非网络卷时为空）
//   - err:     检测失败（平台不支持 / 权限不足）
//
// ★ fail-safe 约定：err != nil 时调用方必须按「不确定 ⇒ 不启用 flock」处理，
// 不得把检测失败当成「本地卷」。
//
// 判定函数做成包级变量 networkFSProbe，仅为了 §6.4 降级测试能注入假结果——
// 生产路径一律走真实系统调用（见 network_fs_unix.go / network_fs_windows.go）。
var networkFSProbe = isNetworkFSOS

// IsNetworkFS 是导出入口（内部委托给可注入的 networkFSProbe）。
func IsNetworkFS(path string) (network bool, fstype string, err error) {
	return networkFSProbe(path)
}
