// Package sandbox — A4：沙箱自建 tmp 的**单一**放行策略。
//
// 依据：集中拍板 §2 A4 行 —— Opt-B「只放行沙箱自建 tmp」。
//
// 缺陷面（改前基线）：
//
//	legacy `Sandbox.ValidatePath`（sandbox.go）与 `manager.ValidatePath`（manager.go）
//	各自 `isPathWithin(absPath, resolvePath(os.TempDir()))` ⇒ **整个系统临时目录无条件放行**。
//	系统临时目录是所有本机进程都可写的共享区 ⇒ containment 被削掉一大块
//	（同类 CLI 的 workspace_roots 无 /tmp 例外；Opt-A「保留」逆行业做法）。
//
// 收编方式（不是删除放行，而是**收窄放行的根**）：
//
//	两处都改为 `allowedSandboxTemp(absPath)` —— 只有**沙箱自建**的临时根
//	`SandboxTempRoot()`（及其子目录）被放行。放行的根由本文件唯一决定，
//	两栈不可能再各自漂移（与 A3 的 ruleengine.go 同构）。
//
// 为什么不是"什么都不放行"：沙箱自身在 Prepare 里会建临时目录（构建缓存/中间产物），
// 那个目录必须可写；但它应落在**沙箱自己拥有的根**下，而不是与全世界共享的 /tmp 顶层。
package sandbox

import (
	"os"
	"path/filepath"
)

// sandboxTempRootName 是沙箱自建临时根的目录名（置于系统临时目录之下）。
const sandboxTempRootName = "whale-sandbox"

// SandboxTempRoot 返回沙箱自建临时根的**字面**路径（未解析符号链接）。
//
// 它是 A4 后唯一被额外放行的目录：沙箱自己的中间产物放这里，
// 其余系统临时目录内容一律按"工作区之外"处理。
func SandboxTempRoot() string {
	return filepath.Join(os.TempDir(), sandboxTempRootName)
}

// ensureSandboxTempRoot 幂等地创建沙箱临时根（权限 0700），返回其可用性。
//
// 为什么 0700：根由沙箱独占，避免同机其他用户借此目录读写沙箱中间产物。
// 为什么每次 Stat 而不是 Once：进程内多个 Sandbox/Manager 实例可能在任意时刻启用，
// 且目录可能被外部清理；轻量 Stat 比全局 Once 更贴合"每次启用都要保证根存在"。
// 失败不 panic（返回 false）：放行策略会 fail-closed（根不可用 ⇒ 不放行），
// 由 A1 家族的 Deficiency 机制保证这件事对外可见。
func ensureSandboxTempRoot() bool {
	root := SandboxTempRoot()
	if fi, err := os.Lstat(root); err == nil {
		// 已存在：必须是真目录（符号链接不算 —— 见 allowedSandboxTemp）。
		return fi.IsDir()
	}
	if err := os.MkdirAll(root, 0o700); err != nil {
		// 并发下另一执行体可能刚建成：再判定一次，避免把竞态误报为失败。
		fi, err2 := os.Lstat(root)
		return err2 == nil && fi.IsDir()
	}
	return true
}

// allowedSandboxTemp 判定 **已 resolve 的绝对路径** absPath 是否落在沙箱自建临时根内（A4）。
//
// 三道 fail-closed 前置（任一不满足即不放行）：
//
//  1. 根必须**真实存在**（不存在 ⇒ 沙箱从未建过自建 tmp ⇒ 没有可放行的根）；
//  2. 根必须是**目录且本身不是符号链接**（Lstat）—— 否则 `$TMPDIR/whale-sandbox`
//     若被同机进程预建为指向 /etc 的符号链接，resolvePath 会让任意 /etc 路径"落在根内"；
//  3. 解析后仍在根内（isPathWithin，含分隔符边界）。
//
// 注意 absPath 必须已由调用方 resolvePath（两处调用点均如此），本函数不再解析它。
func allowedSandboxTemp(absPath string) bool {
	root := SandboxTempRoot()
	fi, err := os.Lstat(root)
	if err != nil || !fi.IsDir() || fi.Mode()&os.ModeSymlink != 0 {
		return false
	}
	return isPathWithin(absPath, resolvePath(root))
}
