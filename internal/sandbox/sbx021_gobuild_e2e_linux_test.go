//go:build linux

// Package sandbox — SBX-021 端到端（**生产真实路径**）：
// 生产默认资源限制下，重负载编译**不得**被误杀。
//
// # 为什么需要这条（与同编号其余判据的分工）
//
// `sbx021_as_retire_linux_test.go` 的三条判据是**结构面 / 申报面**：
// 证明"AS 没被施加""退役有申报""申报被接线"。它们都**不回答**用户真正会踩到的那件事——
// **在沙箱里跑 `go build` 会不会失败**。
//
// SBX-001 的 R1 正是这样暴露的：改动全绿，却在 `go build ./internal/...` 上炸。
// ⇒ 本测试经**真实 helper**（`wrapWithPreExecHelper`）以**生产默认限制**
// （`DefaultResourceLimits()`）包装 `go build`，断言其成功。
//
// 选择 `./internal/tui/` 作默认目标：它是 SBX-001 实测中 1GiB AS 下**必失败**的目标
// （失败点 `modernc.org/libc`，报 `fatal error: out of memory`），
// 因此对"AS 是否被施加"有判别力，而小包（如 `./internal/sandbox/`）没有。
//
// 反例注入：把 `{unix.RLIMIT_AS, limits.MemoryMaxBytes, "AS"}` 加回
// `resourceLimitTargets` ⇒ 本测试必须变红。
//
// # 默认 skip 的理由
//
// 需要 Go 工具链 + 模块缓存，且编译 tui 较慢 ⇒ 不适合放进每次 `go test` 的默认路径
// （否则引入 flaky 与时长）。开启方式：
//
//	SBX021_E2E_BUILD=1 go test ./internal/sandbox/ -run TestSBX021_GoBuild -v
//
// 可选环境变量：SBX021_BUILD_TARGET（目标包）、SBX021_BUILD_WORKDIR（工作目录）。
package sandbox

import (
	"os"
	"os/exec"
	"testing"
)

// TestSBX021_GoBuildSurvivesProductionLimits 端到端：生产默认限制下 go build 必须成功。
func TestSBX021_GoBuildSurvivesProductionLimits(t *testing.T) {
	if os.Getenv("SBX021_E2E_BUILD") == "" {
		t.Skip("需 SBX021_E2E_BUILD=1 开启（依赖 Go 工具链与模块缓存；" +
			"可用 SBX021_BUILD_TARGET / SBX021_BUILD_WORKDIR 覆盖）")
	}
	if _, err := exec.LookPath("go"); err != nil {
		t.Skipf("go 工具链不可用，跳过: %v", err)
	}

	workdir := os.Getenv("SBX021_BUILD_WORKDIR")
	if workdir == "" {
		workdir = "/work"
	}
	target := os.Getenv("SBX021_BUILD_TARGET")
	if target == "" {
		target = "./internal/tui/"
	}

	limits := DefaultResourceLimits()
	t.Logf("生产默认资源限制: CPU=%ds Mem=%dB Proc=%d NOFILE=%d FSIZE=%dB",
		limits.CPUMaxSeconds, limits.MemoryMaxBytes, limits.ProcessMax,
		limits.FileDescriptorMax, limits.FileSizeMaxBytes)

	cmd := exec.Command("go", "build", target)
	cmd.Dir = workdir
	// ★ 必须给子进程一个**干净 GOCACHE**：否则 `go build` 命中构建缓存后直接返回，
	// 不会真正编译，也就不会申请大块虚拟地址空间 ⇒ 本测试对"RLIMIT_AS 是否被施加"
	// **失去判别力**（表现为 5 秒内"通过"的假绿；上轮实测之所以能测出误杀，
	// 正是靠 `GOCACHE=$(mktemp -d)` 强制真编译）。
	cmd.Env = append(os.Environ(), "GOFLAGS=-mod=mod", "GOCACHE="+t.TempDir())

	if err := wrapWithPreExecHelper(cmd, LinuxSandboxConfig{ResourceLimits: limits}); err != nil {
		t.Fatalf("wrapWithPreExecHelper: %v", err)
	}
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("生产默认资源限制下 `go build %s` 失败 —— 这正是用户会踩到的误杀；"+
			"且报错指向代码而非沙箱，用户归因方向必然是错的: %v\n--- 输出 ---\n%s",
			target, err, out)
	}
	t.Logf("`go build %s` 在生产默认资源限制下成功（AS 退役生效）", target)
}
