// Package sandbox — 沙箱 re-exec helper 的**结构性分发**。
//
// 为什么分发点必须放在**库**里而不是 main.go：
//
//	restrict 把命令最外层改写为 `<自身可执行文件> <helperArg> -- <cmd>...`
//	（见 os_sandbox_darwin_rlimit.go / os_sandbox_linux_preexec.go）。被 re-exec 的
//	"自身可执行文件"在测试里就是**测试二进制**（如 tui.test / hook.test），而测试二进制
//	**没有 main.go** ⇒ 若只在 main.go 分发，helper 分支永远不会命中，被 re-exec 的
//	测试二进制会从 argv[1]=helperArg 继续跑 → **递归重跑整个测试套件并挂死**。
//
// 为什么 init 比"每个包自己写一个 TestMain 守卫"更强：
//
//	TestMain 是**逐包**的、需要人记得加。本仓已经因此复发过一次（internal/tools 在
//	2026-09-19 才补上，见 tools/main_test.go 头部注释）。而任何能触发 re-exec 的进程
//	必然链接了本包（是它自己调 restrict 才会被改写）⇒ 把分发放进本包 init，
//	**所有**二进制（生产 binary ＋ 任何包的测试二进制）自动覆盖，无需逐包登记。
//
// 为什么必须早于一切初始化：helper 分支要 setrlimit 之后 execve 目标命令；
// 若中途有别的包的 init 先跑（读配置/建日志/TUI 初始化），那些副作用会被带进沙箱内。
// 本包 init 在依赖图上早于任何 importer，因此是最早可达的时点。
package sandbox

import "os"

func init() {
	// HandleSandboxHelperArg 命中时**不返回**（execve 或 exit），未命中时立即返回。
	HandleSandboxHelperArg(os.Args)
}
