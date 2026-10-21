// Package platform — 进程存活判定的**唯一声明**（/ ，2026-09-20）。
//
// # 为什么收敛到这里
//
// 修复前「pid 对应的进程还活着吗」这个问题在全仓有 **7 处**各写各的实现
// （`util` / `config` / `daemon` / `session` 四域 × 平台分支 ＋ 一处测试内影子实现）。
// 其中**至少两处方向相反**：
//
// - `util` / `config` / `daemon`：`syscall.Kill(pid,0)`，**EPERM 视为存活**
// （EPERM 的语义是"进程存在但你没权限给它发信号"）；
// - `session.isProcessRunning`：`os.FindProcess` + `Signal(0) == nil`
// ⇒ **EPERM 落到"信号失败"分支 ⇒ 判死**。
//
// 后果不是"风格不一致"，而是**安全性方向相反**：`session` 域用这个判定回答
// "锁是否还有效"，判死 ⇒ 夺锁 ⇒ 与 `F11 L1`（误夺锁 ⇒ 数据丢失）同向。
// 而与 `pid=1`（launchd）比较时，**任何用户**（含 root）都会拿到 EPERM ⇒
// 这不是"多用户才触发"的边角情形。
//
// ⇒ 收敛到 `internal/platform`（**已存在的零 repo 依赖叶子**，勿新建包；
// `F12` 的 `path_contain.go`、`F14` 的 `path.go` 已在此，同类事实同包）。
//
// # 语义契约（唯一，所有调用方共享）
//
// ProcessAlive(pid):
// pid <= 0 ⇒ false（**必须**先拒：kill(0,0) 指"调用者所在进程组"、
// kill(-pid,0) 指"进程组 |pid|"，sig=0 下都返回 nil
// ⇒ 无守卫会把它们判成"存活"）
// 进程存在（含无权发信号） ⇒ true
// 进程不存在（ESRCH / 句柄无效）⇒ false
//
// ★ **EPERM ⇒ true** 是契约的一部分，不是实现细节：把它判成 false 会让
// "存在但不可发信号"的进程被当成已退出 —— 那正是 。
//
// # 判别器纪律（沿用 教训）
//
// 本族的判别器**必须在每一份原实现所在的包上都跑**（本仓"判据两侧都验"）。
// 带 `//go:build` 的分支只覆盖单平台 ⇒ 只能标 **PLATFORM-PARTIAL**，不得标 CLOSED。
package platform

// ProcessGroupsAreAddressable 报告本平台的"进程组存活"判定是否可用
// （`ProcessGroupAlive` 是否有真实实现）。
//
// 为什么需要它：Windows 没有 POSIX 进程组，`killProcessTree` 走 `taskkill /T`。
// 调用方（`daemon`）据本常量选择路径，而不是靠 `runtime.GOOS` 再分一次支
// —— 那会造出第二份"平台判定"。
const ProcessGroupsAreAddressable = processGroupsAreAddressable
