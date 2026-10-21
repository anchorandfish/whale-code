package platform

// 批 1 判据：跨平台路径锚定语义（单一声明 platform.AnchorsToWorkDir）。
//
// 缺陷背景（Windows 真机 CI 实证）：旧代码 `filepath.IsAbs(target) || target[1]==':'
// 在 Windows 上把 `/etc/passwd` 判成「锚定到工作目录」（filepath.IsAbs 在 Windows 要求
// 盘符）⇒ `echo x > /etc/passwd` 被拼进工作区放行（TestRedirect_AbsoluteOutsideStillBlocked）。
//
// 判别性取证（R4-7/R11）：本测试先在「新语义实现」上全绿；before 红 =
// `-overlay` 派生变体把 windows 分支的 anchorDriveRoot 判定剥掉（= 旧代码语义，可编译、
// 非编译失败红）⇒ Windows 组用例必须**精确**转红、POSIX 组保持绿（红集互斥）。
// 取证命令见回执。

import (
	"runtime"
	"testing"
)

func TestClassifyAnchor_WindowsFlavor(t *testing.T) {
	cases := []struct {
		name   string
		target string
		want   bool // AnchorsToWorkDirFlavor 期望值（true = 锚工作目录）
	}{
		// ★ 缺陷本体组：无盘符 rooted 路径锚到盘根，不是工作目录。
		{"etc_passwd", "/etc/passwd", false},
		{"backslash_rooted", `\etc\passwd`, false},
		{"posix_style_rooted_deep", "/etc/cron.d/evil", false},
		{"root_only", "/", false},

		// 带盘符 → 自身已锚定（不是工作目录相对）。
		{"volume_absolute", `C:\Windows\system32`, false},
		{"volume_relative", "C:foo", false},
		{"unc_path", `\\server\share\file`, false},
		{"verbatim_prefix", `\\?\C:\x`, false},

		// 真正的工作目录相对路径 → 锚工作目录（不回归）。
		{"plain_relative", "foo/bar.txt", true},
		{"dot_relative", "./local.txt", true},
		{"traversal_relative", "../escaped.txt", true},
		{"subdir_relative", `sub\dir\file`, true},
		{"empty_is_relative", "", true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := AnchorsToWorkDirFlavor(tc.target, OSFlavorWindows)
			if got != tc.want {
				t.Errorf("AnchorsToWorkDirFlavor(%q, windows) = %v, want %v", tc.target, got, tc.want)
			}
		})
	}
}

func TestClassifyAnchor_POSIXFlavor(t *testing.T) {
	cases := []struct {
		name   string
		target string
		want   bool
	}{
		{"absolute", "/etc/passwd", false},
		{"absolute_root", "/", false},
		// 旧代码在 POSIX 上也认 X: 形态（target[1]==':' 分支无平台守卫）——保真保留。
		{"volume_form_tolerated", "C:\\x", false},
		{"relative", "foo/bar.txt", true},
		{"dot_relative", "./local.txt", true},
		{"traversal", "../escaped.txt", true},
		{"empty", "", true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := AnchorsToWorkDirFlavor(tc.target, OSFlavorPOSIX)
			if got != tc.want {
				t.Errorf("AnchorsToWorkDirFlavor(%q, posix) = %v, want %v", tc.target, got, tc.want)
			}
		})
	}
}

// TestAnchorsToWorkDir_MatchesCurrentPlatform 钉住生产薄转发与纯函数一致（R6b 结构守卫）：
// 生产入口 AnchorsToWorkDir 必须等价于「按当前 GOOS 显式传入 flavor」的纯函数调用。
// 若有人把 flavor 参数删掉、内部直接读 runtime.GOOS 或写死某一侧，本用例立刻红。
func TestAnchorsToWorkDir_MatchesCurrentPlatform(t *testing.T) {
	flavor := OSFlavorPOSIX
	if runtime.GOOS == OSFlavorWindows {
		flavor = OSFlavorWindows
	}
	for _, tc := range []string{
		"foo.txt", "./a", "../a/b", "/abs/x", `/etc/passwd`, `C:\x`, "C:rel", `\\unc\share`, "",
	} {
		if got, want := AnchorsToWorkDir(tc), AnchorsToWorkDirFlavor(tc, flavor); got != want {
			t.Errorf("AnchorsToWorkDir(%q) = %v, flavor variant = %v —— 生产入口与纯函数分叉", tc, got, want)
		}
	}
}
