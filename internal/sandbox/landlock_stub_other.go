//go:build !linux

// Package sandbox — pre-exec 加固 helper 的非 Linux 平台 stub。
//
// main.go 在所有平台统一调用分发（os.Args[1] == SandboxExecHelperArg →
// RunPreExecHelper），Linux 之外的平台该参数不会被 sandbox.Restrict
// 写入（Landlock/seccomp 仅 Linux 有），此 stub 保证 main.go 跨平台编译。
//
// 注意：该文件不得 import golang.org/x/sys/unix——x/sys/unix 在 windows 上
// 不可编译，而本包必须为 windows 出构建。
package sandbox

// RunPreExecHelper 非 Linux 平台不可达（Restrict 不会注入 helper 参数）。
// 防御性输出错误并退出，避免误触发时静默执行未知命令。
func RunPreExecHelper() {
	panic("sandbox: pre-exec helper is linux-only (got unexpected invocation)")
}
