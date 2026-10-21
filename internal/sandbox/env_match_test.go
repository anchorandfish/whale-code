package sandbox

// Win10 真机 E2E 首跑（v1 门 4）暴露的生产 bug 回归测试：
// BuildEnv 在 Windows 上产出空 env（TestManager_Prepare "Env should not be
// empty" / TestBuildEnv_Filtering "PATH should be in env" FAIL）。
//
// 根因：Windows 环境变量名大小写不敏感（进程块中为 "Path" 而非 "PATH"），
// 白名单精确匹配 "PATH" 永不命中；且默认白名单全是 Unix 名（HOME/USER/LANG），
// Windows 必需变量（SystemRoot/TEMP/USERNAME...）全部被剔除 → 沙箱内命令
// 拿不到 PATH/SystemRoot，基本不可用。
//
// 本文件在所有平台编译运行（纯函数测试，foldCase 显式传参）；
// 真机 Windows 行为由 TestManager_Prepare / TestBuildEnv_Filtering 验证。
import (
	"runtime"
	"testing"
)

// TestEnvNameMatches_FoldCaseOnWindows 结果断言：Windows 命名形态命中白名单。
func TestEnvNameMatches_FoldCaseOnWindows(t *testing.T) {
	// Windows（大小写不敏感）："Path" 必须命中 "PATH" 白名单
	if !envNameMatches("Path", "PATH", true) {
		t.Error(`windows: "Path" should match allowlist "PATH" (case-insensitive)`)
	}
	if !envNameMatches("PATH", "path", true) {
		t.Error(`windows: "PATH" should match allowlist "path" (case-insensitive)`)
	}
	if !envNameMatches("SystemRoot", "SYSTEMROOT", true) {
		t.Error(`windows: "SystemRoot" should match allowlist "SYSTEMROOT"`)
	}
	// 大小写不敏感 ≠ 前缀/子串匹配
	if envNameMatches("PATH_EVIL", "PATH", true) {
		t.Error(`windows: "PATH_EVIL" must not match allowlist "PATH"`)
	}
	// Unix（大小写敏感）："path" 不得命中 "PATH"（fail-closed，剔除）
	if envNameMatches("path", "PATH", false) {
		t.Error(`unix: "path" must NOT match allowlist "PATH" (case-sensitive)`)
	}
	if !envNameMatches("PATH", "PATH", false) {
		t.Error(`unix: "PATH" should match allowlist "PATH"`)
	}
}

// TestDefaultEnvConfig_WindowsEssentials 结果断言：默认白名单包含 Windows
// 必需变量（缺失会导致沙箱内命令无 PATH/SystemRoot/TEMP 可用）。
func TestDefaultEnvConfig_WindowsEssentials(t *testing.T) {
	cfg := DefaultEnvConfig()
	// Windows 程序运行的最小集：路径/临时目录/系统根/用户名/解释器
	essentials := []string{"TEMP", "TMP", "SYSTEMROOT", "SYSTEMDRIVE", "USERNAME",
		"USERPROFILE", "COMSPEC", "PATHEXT", "WINDIR"}
	for _, want := range essentials {
		found := false
		for _, a := range cfg.AllowList {
			if envNameMatches(a, want, true) {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("DefaultEnvConfig.AllowList missing Windows essential %q (sandboxed commands would break on Windows)", want)
		}
	}
}

// TestBuildEnv_PreservesHostPathCase 结果断言：BuildEnv 不会因大小写剥离
// 宿主 PATH（在非 Windows 平台上构造 foldCase 分支的行为由上面纯函数测试
// 覆盖；此用例验证当前平台 PATH 存活，防止跨平台回归）。
func TestBuildEnv_PreservesHostPathCase(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("windows 真机行为由 TestBuildEnv_Filtering 验证")
	}
	env := BuildEnv(DefaultEnvConfig())
	found := false
	for _, kv := range env {
		if len(kv) >= 5 && kv[:5] == "PATH=" {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("PATH missing from BuildEnv output on %s", runtime.GOOS)
	}
}
