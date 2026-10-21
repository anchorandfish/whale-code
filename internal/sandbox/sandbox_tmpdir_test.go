// Package sandbox — 判别器：沙箱自建临时目录必须注入子进程 env。
//
// 缺陷面（改前基线）：
//
//	Prepare 在工作区内建出 `<workDir>/sandbox-tmp-*` 并塞进 `SandboxContext.TempDir`，
//	但**没有任何消费者**；同时子进程 env 由 `BuildEnv(DefaultEnvConfig())` 按白名单构造，
//	TMPDIR 不在名单里 ⇒ 被剥掉。
//	后果：需要临时目录的命令（go build / go test / 多数构建工具）退回 `/tmp`，
//	而 `/tmp` 既不在 A4 应用层放行面、也不在 macOS/Linux 沙箱可写根内 ⇒ 直接失败：
//
// `go: creating work dir: mkdir /tmp/go-build…: operation not permitted`
//
// 判据面（为什么是它）：`SandboxContext.Env` 是子进程**真正**拿到的环境
//
//	（`internal/tools/bash.go:864` `cmd.Env = sbCtx.Env`；`executor.go:1371-1372`、
//	`task.go:199-200` 同源）⇒ 断言必须落在生产入口产出的 Env 上，而不是手工拼的 env。
package sandbox

import (
	"context"
	"os"
	"runtime"
	"strings"
	"testing"
)

// n098EnvValue 取 env 列表里 name 的值（平台感知大小写策略，与 BuildEnv 同源）。
func n098EnvValue(env []string, name string) (string, bool) {
	for _, kv := range env {
		k, v, ok := strings.Cut(kv, "=")
		if !ok {
			continue
		}
		if envNameMatches(k, name, envFoldCase) {
			return v, true
		}
	}
	return "", false
}

// TestFix_N098_ProductionPrepareInjectsTempDir 是 的主判别器。
//
// 走**生产入口** `NewProductionSandboxManager`（TUI lifecycle 与 runner_core 共用
// 的唯一工厂），断言 `Prepare` 返回的 Env 里，临时目录变量精确等于 `TempDir`。
// before（未修）⇒ 断言红（Env 里没有 TMPDIR）。
func TestFix_N098_ProductionPrepareInjectsTempDir(t *testing.T) {
	ws := t.TempDir() // 探针不碰真实用户文件：工作区用测试自建目录
	mgr := NewProductionSandboxManager(ws)

	ctx, cleanup, err := mgr.Prepare(context.Background(), SandboxLevelStrict, ws)
	if err != nil {
		t.Fatalf("Prepare 失败: %v", err)
	}
	defer cleanup()

	if ctx.TempDir == "" {
		t.Fatal("SandboxContext.TempDir 为空：Prepare 没有建出沙箱自建临时目录")
	}
	if !strings.HasPrefix(ctx.TempDir, ws) {
		t.Fatalf("TempDir 必须落在工作区内（否则会被沙箱判为越界）: TempDir=%s workDir=%s", ctx.TempDir, ws)
	}
	if fi, statErr := os.Stat(ctx.TempDir); statErr != nil || !fi.IsDir() {
		t.Fatalf("TempDir 不可用（子进程要往里写）: %v", statErr)
	}

	for _, name := range tempDirEnvNames() {
		got, ok := n098EnvValue(ctx.Env, name)
		if !ok {
			t.Fatalf("子进程 env 缺少 %s ⇒ 需要临时目录的命令会退回 /tmp 而失败\nenv=%v", name, ctx.Env)
		}
		if got != ctx.TempDir {
			t.Fatalf("%s = %q，期望沙箱自建 TempDir %q", name, got, ctx.TempDir)
		}
	}
}

// TestFix_N098_ExplicitSetWins 钉住优先级契约：`EnvConfig.Set` 里显式给出的值
// 优先级最高（BuildEnv 文档：「Set 中的 PATH 不再过滤，调用方全权控制」），
// 自动注入**不得**覆盖调用方的显式选择。
func TestFix_N098_ExplicitSetWins(t *testing.T) {
	ws := t.TempDir()
	custom := t.TempDir() // 调用方自己指定的临时目录

	cfg := &SandboxConfig{
		WorkDir:   ws,
		OSLevel:   DefaultOSLevelConfig(),
		EnvConfig: DefaultEnvConfig(),
	}
	cfg.EnvConfig.Set = map[string]string{"TMPDIR": custom}
	mgr := NewManager(cfg, SandboxLevelStrict)

	ctx, cleanup, err := mgr.Prepare(context.Background(), SandboxLevelStrict, ws)
	if err != nil {
		t.Fatalf("Prepare 失败: %v", err)
	}
	defer cleanup()

	got, ok := n098EnvValue(ctx.Env, "TMPDIR")
	if !ok {
		t.Fatal("env 缺少 TMPDIR（显式 Set 的值应保留）")
	}
	if got != custom {
		t.Fatalf("调用方显式 Set[\"TMPDIR\"]=%q 被自动注入覆盖为 %q", custom, got)
	}
}

// TestFix_N098_InjectDoesNotDuplicate 钉住 upsert 语义：
// 反复注入不得产出重复键（重复键在 exec.Cmd.Env 里行为依赖平台解析顺序，
// 会让"最后一个生效"变成隐式约定）。
func TestFix_N098_InjectDoesNotDuplicate(t *testing.T) {
	base := []string{"PATH=/usr/bin", "TMPDIR=/tmp", "HOME=/root"}
	out := injectTempDir(base, DefaultEnvConfig(), "/ws/sandbox-tmp-1")

	count := 0
	for _, kv := range out {
		if k, _, ok := strings.Cut(kv, "="); ok && envNameMatches(k, "TMPDIR", envFoldCase) {
			count++
		}
	}
	if count != 1 {
		t.Fatalf("TMPDIR 出现 %d 次，期望恰好 1 次: %v", count, out)
	}
	got, _ := n098EnvValue(out, "TMPDIR")
	if got != "/ws/sandbox-tmp-1" {
		t.Fatalf("TMPDIR 未被替换为沙箱自建根: %q", got)
	}
	if v, _ := n098EnvValue(out, "PATH"); v != "/usr/bin" {
		t.Fatalf("注入不应影响其它变量: PATH=%q", v)
	}
}

// TestFix_N098_EmptyTempDirIsNoop 钉住 fail-safe：tempDir 为空时不得凭空
// 写入一个空值 TMPDIR=（会让子进程把临时目录解析为当前目录）。
func TestFix_N098_EmptyTempDirIsNoop(t *testing.T) {
	base := []string{"PATH=/usr/bin"}
	out := injectTempDir(base, DefaultEnvConfig(), "")
	if len(out) != 1 {
		t.Fatalf("tempDir 为空时不应改动 env: %v", out)
	}
	if _, ok := n098EnvValue(out, "TMPDIR"); ok {
		t.Fatal("tempDir 为空时不应写入 TMPDIR")
	}
}

// TestFix_N098_TempDirNamesMatchPlatform 钉住平台差异（Windows 走 TEMP/TMP，
// 与 DefaultEnvConfig 的 Windows 组一致）。
func TestFix_N098_TempDirNamesMatchPlatform(t *testing.T) {
	names := tempDirEnvNames()
	if runtime.GOOS == "windows" {
		for _, want := range []string{"TEMP", "TMP"} {
			found := false
			for _, n := range names {
				if n == want {
					found = true
				}
			}
			if !found {
				t.Fatalf("Windows 必须注入 %s，实际 %v", want, names)
			}
		}
		return
	}
	if len(names) != 1 || names[0] != "TMPDIR" {
		t.Fatalf("Unix 只应注入 TMPDIR，实际 %v", names)
	}
}
