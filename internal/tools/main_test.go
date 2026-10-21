package tools

import (
	"os"
	"testing"

	"github.com/anchorandfish/whale-code/internal/permission"
	"github.com/anchorandfish/whale-code/internal/sandbox"
	"github.com/anchorandfish/whale-code/internal/sandbox/validator"
	"github.com/anchorandfish/whale-code/internal/security"
)

// TestMain 初始化 security.Checker，避免测试中 "Checker 未初始化" 错误。
//
// ★ rlimit re-exec helper 的分发（· 2026-09-19 补 · ** 已结构性收敛**）：
//
// 这里历史上是**逐包手写**的守卫 —— 原因见下（保留为可读性），但**不要再往新包里抄这个模式**：
// 分发现在由 `internal/sandbox` 的 init 自动完成（helper_dispatch.go），覆盖所有链接了
// sandbox 的二进制。此处保留一次显式调用，仅作为冗余保险（两处都委派同一函数，不会分歧）。
//
// 为什么当初必须补：macOS 上 `sandbox.Restrict` 会把命令重写成
//
//	<自己的可执行文件> __whale-rlimit -- /usr/bin/sandbox-exec -p <profile> -- <cmd>
//
// （os_sandbox_darwin_rlimit.go:70 —— 生产配置恒带 `rlimit(cpu,nofile,fsize)`）。
// **测试二进制没有 main.go** ⇒ 若不拦截，被 re-exec 的 `tools.test` 会从
// argv[1]=`__whale-rlimit` 开始**再跑一遍整个测试套件**（递归）—— 症状是测试"挂死"、
// load average 飙升，而**没有任何测试会红**，极难定位（首次实跑真的踩到：tools 包 10 分钟超时）。
//
// 非 darwin 平台该参数不会出现（Restrict 不注入 helper 参数），分支恒为 no-op。
func TestMain(m *testing.M) {
	// 必须先于任何初始化：命中 helper 参数时不返回（exec 或 exit）。
	sandbox.HandleSandboxHelperArg(os.Args)

	workDir, _ := os.Getwd()
	s := sandbox.NewSandbox(workDir)
	v := validator.NewValidator()
	p := permission.NewPolicy(permission.LevelDangerFullAccess)
	security.Init(s, v, p)
	os.Exit(m.Run())
}
