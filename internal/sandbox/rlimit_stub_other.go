//go:build !darwin

// Package sandbox — rlimit re-exec helper 的非 macOS 平台 stub。
//
// main.go 在所有平台统一调用分发（os.Args[1] == RlimitHelperArg →
// RunRlimitHelper），darwin 之外的平台该参数不会被 sandbox.Restrict
// 写入（rlimit helper 仅 macOS 有），此 stub 保证 main.go 跨平台编译。
package sandbox

// RunRlimitHelper 非 darwin 平台不可达（Restrict 不会注入 helper 参数）。
// 防御性输出错误并退出，避免误触发时静默执行未知命令。
func RunRlimitHelper() {
	panic("sandbox: rlimit helper is darwin-only (got unexpected invocation)")
}
