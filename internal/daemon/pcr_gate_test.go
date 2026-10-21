// Package daemon — PCR 域 Round2「Human gate 前提」characterization 测试。
//
// 用户在 Human gate 拍板：
//
//	Q1 = A（metrics 回环受信，对齐业内既有实现 is_unauthenticated_non_loopback_listener）
//	     前提：metrics 仅监听回环 —— 本文件钉住该前提的【真实形态】。
//	Q2 = A（同用户受信，对齐 OS/同类 CLI 模型）
//	     前提：auth.token 文件权限 0600 —— 本文件实测钉住。
//	Q3 = 同意鉴权单点收敛（I1）。
//
// 先钉后改（AUDIT_ROADMAP §5 规则 1）：这些断言必须在【改动前】对当前实现全绿，
// 证明"现状即如此"。改动后它们必须【仍然绿】（纯收敛不应改变行为）。
package daemon

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

// ---------------------------------------------------------------------------
// Q1 前提 · metrics 监听地址策略（钉住"真实形态"）
//
// 诚实结论：代码并非"metrics 永远只监听回环"。真实不变量是：
//   - 空 token  → 强制回环（非回环直接拒绝启动，fail-closed）
//   - 非空 token → 允许绑定任意地址（含 0.0.0.0），由 Bearer 鉴权保护
// 这正是同类 CLI is_unauthenticated_non_loopback_listener 的语义：
//   "未鉴权的非回环监听器" 被拒绝；"已鉴权" 的非回环监听器是允许的。
// ---------------------------------------------------------------------------

// 空 token + 非回环 → 拒绝启动（fail-closed）。
func TestPCR_MetricsBind_EmptyTokenRejectsNonLoopback(t *testing.T) {
	d := &Daemon{}
	srv, err := d.startMetricsServer("0.0.0.0:0", "")
	if err == nil {
		shutdownMetricsServer(srv)
		t.Fatal("空 token + 0.0.0.0 必须拒绝启动（fail-closed），实际放行")
	}
	if srv != nil {
		shutdownMetricsServer(srv)
		t.Fatal("fail-closed 时 startMetricsServer 应返回 nil server")
	}
}

// 空 token + 回环 → 允许（回环受信域）。
func TestPCR_MetricsBind_EmptyTokenAllowsLoopback(t *testing.T) {
	d := &Daemon{}
	srv, err := d.startMetricsServer("127.0.0.1:0", "")
	if err != nil {
		t.Fatalf("空 token + 127.0.0.1 应允许启动，实际 %v", err)
	}
	if srv == nil {
		t.Fatal("空 token + 127.0.0.1 应返回非 nil server")
	}
	shutdownMetricsServer(srv)
}

// 非空 token + 非回环 → 允许（有鉴权保护）。
// 这条钉住"metrics 仅监听回环"这一表述【并不字面成立】，避免误读 Q1 前提。
func TestPCR_MetricsBind_NonEmptyTokenAllowsNonLoopback(t *testing.T) {
	d := &Daemon{}
	srv, err := d.startMetricsServer("0.0.0.0:0", "secret-token")
	if err != nil {
		t.Fatalf("非空 token + 0.0.0.0 应允许启动（由 Bearer 鉴权保护），实际 %v", err)
	}
	if srv == nil {
		t.Fatal("非空 token + 0.0.0.0 应返回非 nil server")
	}
	shutdownMetricsServer(srv)
}

// ---------------------------------------------------------------------------
// Q2 前提 · auth.token 文件权限 0600（实测钉住）
//
// 走生产真实入口：IPCServer.writeAuthToken()（ipc.go:562）。
// 该路径经 util.AtomicWriteFile → atomicWrite 显式 os.Chmod(tmpPath, perm)，
// 因此不受进程 umask 影响。此处实测文件落盘后的 Perm()。
// ---------------------------------------------------------------------------

func TestPCR_AuthTokenFile_Perm0600(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Windows 无 POSIX 权限位（Go 建文件恒 0666），0600 断言仅适用 POSIX")
	}
	dir := t.TempDir()
	s := &IPCServer{socketPath: dir, authToken: "deadbeefdeadbeef"}
	if err := s.writeAuthToken(); err != nil {
		t.Fatalf("writeAuthToken 失败：%v", err)
	}
	fi, err := os.Stat(filepath.Join(dir, AuthTokenFile))
	if err != nil {
		t.Fatalf("auth.token 未落盘：%v", err)
	}
	if got := fi.Mode().Perm(); got != 0600 {
		t.Fatalf("auth.token 权限 = %o，期望 0600", got)
	}
	// 内容必须与内存中的 token 一致（客户端读同一文件即可鉴权）。
	b, err := os.ReadFile(filepath.Join(dir, AuthTokenFile))
	if err != nil {
		t.Fatalf("读取 auth.token 失败：%v", err)
	}
	if string(b) != "deadbeefdeadbeef" {
		t.Fatalf("auth.token 内容被意外改写：%q", string(b))
	}
}

// ---------------------------------------------------------------------------
// Q3 · 单一强制点 authorize 真值表（收敛后包内唯一的令牌比较原语）
//
// 三/四条传输路径（IPC / UDS-!windows / UDS-windows / metrics）都委托到这里，
// 因此这张真值表同时钉住了它们的共同语义。若未来有人在校验分支上做发散改动，
// 必须显式改动本表 —— 不再存在"某一条传输悄悄漂移"的盲区。
// ---------------------------------------------------------------------------

func TestPCR_Authorize_TruthTable(t *testing.T) {
	cases := []struct {
		name     string
		expected string
		provided string
		want     bool
	}{
		{"空期望令牌 → 拒绝（fail-closed）", "", "anything", false},
		{"空期望 + 空提供 → 拒绝", "", "", false},
		{"等长且匹配 → 放行", "abc", "abc", true},
		{"等长但内容不同 → 拒绝", "abc", "abd", false},
		{"提供更短 → 拒绝", "abc", "ab", false},
		{"提供更长 → 拒绝", "abc", "abcd", false},
		{"提供为空 vs 非空期望 → 拒绝", "abc", "", false},
	}
	for _, c := range cases {
		if got := authorize(c.expected, c.provided); got != c.want {
			t.Errorf("%s：authorize(%q,%q)=%v，期望 %v", c.name, c.expected, c.provided, got, c.want)
		}
	}
}
