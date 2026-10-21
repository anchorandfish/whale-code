// Package util — 锁族的**两条唯一声明**（F11 L2 / L4，2026-09-20）。
//
// # 为什么放在这里
//
// `internal/util` 是锁族（`InstanceLock` / `filelock` / `filelock_timeout`）的实现包，
// 且它**只依赖 `internal/platform`**（零反向依赖）⇒ 与 `config` / `session` / `daemon`
// 之间无环，可作为共享声明的落点。
// 依赖方向实测：`config` 已引用 `util`（`config/config.go` 等 4 个文件），
// `session` 已引用 `util`（`session/store.go:22`）⇒ 两侧都无需新增包依赖。
package util

import "os"

// ═══════════════════════════════════════════════════════════════════════════
// F11 L2 —— 锁目录权限的**唯一声明**
// ═══════════════════════════════════════════════════════════════════════════
//
// 修复前同族各写各的，实际权限取决于「**谁先 `MkdirAll` 谁定**」：
//
//	TryAcquireFileLock / bridge audit / daemon 全族 = 0700
//	WithFileLock / WithFileLockTimeout / InstanceLock = 0755   ← 不一致
//	实测 ~/.whale/audit = 0700，而 sessions / filehistory / logs / goals = 0755
//
// 这不是"风格问题"：`MkdirAll` 对**已存在**的目录是空操作 ⇒ 同一目录的最终权限
// 由第一个创建者决定，第二个调用方的 0755/0700 完全不生效 ⇒ 单看某一个调用点
// 无法推断磁盘上的真实权限。收敛为显式常量后，「目录权限」成为可声明的常量，
// 而不是调用顺序的副产物。
//
// 取值 0o700：锁目录/私有状态目录只应属于持有者（同包 `filelock.go:25`、
// `daemon` 全族、`bridge/audit.go` 已是 0700，此处只是把多数派写进单一声明）。
const LockDirPerm os.FileMode = 0o700

// ═══════════════════════════════════════════════════════════════════════════
// F11 L4 —— 锁有效性判定的**唯一三值声明**
// ═══════════════════════════════════════════════════════════════════════════
//
// 修复前同一个「损坏 / 0 字节锁文件」，两套实现给出**相反**决策：
//
//	config.isStaleLock   内容无法解析 ⇒ 视为"被持有"（fail-closed，但提前 return
//	                     ⇒ 5 分钟超时永远取不到 ⇒ 永久阻塞）
//	session.isLockStale  json.Unmarshal 失败 ⇒ 视为"陈旧"（fail-open，立即夺锁）
//
// 两边注释都自称深思熟虑 ⇒ 不是"谁写错了"，而是**同一失败产物没有单一权威决策**。
// 现收敛为三值，且**零值 = 未知 = 不夺**（fail-closed）：
// Go 的零值语义使「忘记赋值」这种错误也落在安全侧，与 `platform.BoundaryUnknown`
// 同一手法（该处注释："零值即 Unknown，`if ok` 读法在类型上不可表达"）。
//
// 本仓 I2 口径 = fail-closed ⇒ 未知态**不得**被当成可夺。
type LockVerdict int

const (
	// LockVerdictUnknown：**零值**。无法判定锁是否仍被持有（内容损坏 / 不可解析 /
	// 文件读不到但存在等）。⇒ `MaySteal()` 为 false，**默认不夺**。
	LockVerdictUnknown LockVerdict = iota

	// LockVerdictHeld：判定为**仍被有效持有**（持有者存活 / 未超时）⇒ 不夺。
	LockVerdictHeld

	// LockVerdictStale：判定为**陈旧可夺**（持有者已退出 / 已超时 / 文件已消失）。
	LockVerdictStale
)

// MaySteal 报告该判定是否允许调用方**夺锁**（删除并重建锁文件）。
//
// ★ 唯一放行分支是 `LockVerdictStale`：Held 与 Unknown（含未初始化的零值）
// 一律 false。任何"把 Unknown 并入可夺"的改动都会让本族判别器变红
// （见 `lock_verdict_test.go` 与 `session/lock_verdict_test.go`）。
func (v LockVerdict) MaySteal() bool {
	return v == LockVerdictStale
}

// String 供日志/断言可读（避免只打印数字导致归因困难）。
func (v LockVerdict) String() string {
	switch v {
	case LockVerdictUnknown:
		return "unknown"
	case LockVerdictHeld:
		return "held"
	case LockVerdictStale:
		return "stale"
	default:
		return "invalid"
	}
}
