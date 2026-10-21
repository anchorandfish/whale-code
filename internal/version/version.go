// Package version — 版本号的唯一真源（single source of truth）。
//
// 设计说明（：同一事实跨入口必须同源）：
// - `Version` 是本仓**唯一**的版本声明。`--version`、启动横幅、doctor、
// onboarding、TUI 全部从这里读取，不再各自去读 `debug.ReadBuildInfo()`。
// - 发布构建用 `-ldflags "-X github.com/anchorandfish/whale-code/internal/version.Version=x.y.z"`
// 覆盖默认值（所以它必须是 `var` 而不是 `const`）。
// - 走 `go install ...@vX.Y.Z` 时没有 ldflags，此时默认值就是发布版本号，仍然正确。
//
// 为什么不能读 `debug.ReadBuildInfo()`：从源码目录直接 `go build` 时
// `info.Main.Version` 恒为 `(devel)`，会把发布件显示成 `dev`，与 `--version`
// 不一致（v0.1.0 发布前的实测缺陷）。
package version

// Version 当前发布版本号。发布构建由 -ldflags 注入，默认值与最近一次发布一致。
//
// 版本演进规则（SemVer）：
// - 主版本号：不兼容 API 变更
// - 次版本号：向后兼容的功能新增
// - 修订号：向后兼容的 bug 修复
var Version = "0.1.0"

// BuildDate 构建日期（可选，由 -ldflags 注入，默认空）。
var BuildDate = ""

// Commit 构建对应的 git commit（可选，由 -ldflags 注入，默认空）。
var Commit = ""
