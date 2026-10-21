// Package platform — 跨平台路径锚定语义（单一声明）。
//
// （2026-10-13 Windows 真机 CI）实证的缺陷面：旧代码用 `filepath.IsAbs(target) ||
// (len(target) >= 2 && target[1] == ':')` 判定「重定向/写入目标是否锚定到工作目录」，
// 但 **Windows 上 `filepath.IsAbs("/etc/passwd") == false`**（Go 在 Windows 要求盘符才算
// 绝对路径）⇒ `/etc/passwd` 被当成相对路径 `filepath.Join(workDir, …)` 拼进工作区放行；
// 而真正的 OS 语义是：无盘符的 `/x` 锚到**当前盘根**（`C:\etc\passwd`），shell 与
// `os.Create` 都这么解析。`echo x > /etc/passwd` 在 Windows 上因此逃出工作区边界
// （CI `TestRedirect_AbsoluteOutsideStillBlocked` 红，macOS 绿 —— 平台语义差异，非回归）。
//
// 同一缺陷形状在本仓共四处调用点（R3⑤ 同源穷举）：
// - internal/sandbox/sandbox.go redirectOutsideWorkDir（重定向边界）
// - internal/tools/apply_diff.go resolvePatchTargetPath（补丁目标解析）
// - internal/tools/pipeline.go selfTargetedCall（自指保护的目标归位）
// - internal/tools/powershell/path_validation.go（PowerShell 工具的工作目录约束）
//
// 第一性原理（C2 约束）：「目标落在盘上哪里」由 OS/shell 的解析语义裁决，
// 不能用 POSIX 直觉回答。本文件把「锚定分类」收口为唯一声明，并把平台语义
// 参数化（osFlavor），使 **Windows 分支在任意主机上都可表驱动测试**（判别器结构，
// 参照 -R1：把平台差异从编译期常量变成可注入输入）。
package platform

import (
	"runtime"
	"strings"
)

// OSFlavorWindows / OSFlavorPOSIX 是 osFlavor 参数的两个合法取值。
const (
	OSFlavorWindows = "windows"
	OSFlavorPOSIX   = "posix"
)

// anchorDecision 描述重定向/写入目标应锚定到哪个基准。
type anchorDecision int

const (
	// anchorWorkDir：shell 意义上的相对路径 → 锚到工作目录（两平台一致）。
	anchorWorkDir anchorDecision = iota
	// anchorDriveRoot：盘根相对（无盘符的 /x 或 \x，仅 Windows 语义）→ 锚到当前盘根。
	// POSIX 上 `/x` 就是绝对路径，永远不会落到这一档。
	anchorDriveRoot
	// anchorAbsolute：自身已锚定（POSIX 绝对 / 带盘符或 UNC 的 Windows 路径）→ 原样解析。
	anchorAbsolute
)

// classifyAnchor 是**纯函数**（不碰文件系统、不依赖编译期 GOOS），
// 按 osFlavor 显式模拟两平台的路径解析语义。
//
// 忠实保真说明：旧代码在 POSIX 上也认 `X:` 形态（`target[1] == ':'` 分支无平台守卫），
// posix 分支保留该行为，保证 macOS 上行为零变化（A/B 单变量）。
func classifyAnchor(target, osFlavor string) anchorDecision {
	t := target
	if osFlavor == OSFlavorWindows {
		// 带卷标（C:\x 绝对 / C:foo 卷标相对）或 UNC（\\server\share）→ 自身已锚定。
		// 卷标相对路径语义上锚到「该盘的 cwd」，不是工作目录 —— 保守按已锚定处理，
		// 交调用方用 filepath.Abs / 原样判定（fail-closed 方向：绝不误拼进工作区）。
		if len(t) >= 2 && t[1] == ':' {
			return anchorAbsolute
		}
		if strings.HasPrefix(t, `\\`) {
			return anchorAbsolute
		}
		// 无盘符的 /x 或 \x → 盘根相对（缺陷本体：旧代码此处误判为 workDir 锚定）。
		if strings.HasPrefix(t, "/") || strings.HasPrefix(t, `\`) {
			return anchorDriveRoot
		}
		return anchorWorkDir
	}
	// POSIX：/x 即绝对路径；保留旧代码对 X: 形态的容忍（行为零变化）。
	if strings.HasPrefix(t, "/") {
		return anchorAbsolute
	}
	if len(t) >= 2 && t[1] == ':' {
		return anchorAbsolute
	}
	return anchorWorkDir
}

// AnchorsToWorkDirFlavor 报告 target 是否应锚定到工作目录（按 osFlavor 模拟平台语义）。
// 导出仅供测试在任意主机上覆盖两种平台语义；生产调用方走 AnchorsToWorkDir。
func AnchorsToWorkDirFlavor(target, osFlavor string) bool {
	return classifyAnchor(target, osFlavor) == anchorWorkDir
}

// AnchorsToWorkDir 生产入口：按 runtime.GOOS 判定 target 是否锚定到工作目录。
//
// 返回 true ⇒ 调用方应 filepath.Join(workDir, target)；
// 返回 false ⇒ 调用方应把 target 当已锚定路径原样处理（或 filepath.Abs）。
func AnchorsToWorkDir(target string) bool {
	flavor := OSFlavorPOSIX
	if runtime.GOOS == OSFlavorWindows {
		flavor = OSFlavorWindows
	}
	return AnchorsToWorkDirFlavor(target, flavor)
}
