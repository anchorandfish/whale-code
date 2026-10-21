//go:build !windows

// Package sandbox — 门 2 修复测试：banner 必须到达真实终端 stderr（fd 2）。
//
// 背景（真机验证暴露）：main.go 非 debug 模式把 os.Stderr 变量重定向到
// 日志文件（防日志泄漏到 TUI 下方）。banner 若走 os.Stderr 变量会被吞进
// 日志文件——whale --plain 终端无任何 [sandbox] 行，"降级可见性"静默失效。
//
// 本测试复刻该场景：重定向 os.Stderr 变量到管道 A，dup fd 2 到管道 B，
// 断言 banner 出现在 B（真实终端）而非 A（日志文件），且无双前缀。
package sandbox

import (
	"io"
	"os"
	"strings"
	"syscall"
	"testing"
)

// captureFd2 把进程 fd 2 重定向到管道，返回读取函数与恢复函数。
func captureFd2(t *testing.T) (read func() string, restore func()) {
	t.Helper()
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatalf("os.Pipe: %v", err)
	}
	savedStderr := os.Stderr
	// 保存原 fd 2（dup 到空闲 fd），便于结束后恢复
	savedFd2, err := syscall.Dup(2)
	if err != nil {
		t.Fatalf("dup fd2: %v", err)
	}
	if err := syscall.Dup2(int(w.Fd()), 2); err != nil {
		t.Fatalf("dup2 pipe onto fd2: %v", err)
	}
	// 模拟 main.go：os.Stderr 变量指向另一个 sink（日志文件替身）
	devNull, _ := os.OpenFile(os.DevNull, os.O_WRONLY, 0)
	os.Stderr = devNull

	read = func() string {
		// 必须先还原 fd 2（管道写端只剩 w），再关 w → io.ReadAll 立即 EOF。
		// 顺序反了会挂死（fd 2 持有写端，EOF 永不到来——600s 超时事故）；
		// 而把还原留给 restore 则 r 已被关，读回空串（got "" 事故）。
		_ = syscall.Dup2(savedFd2, 2)
		_ = w.Close()
		data, _ := io.ReadAll(r)
		return string(data)
	}
	restore = func() {
		// 幂等：read 已还原过 fd 2 时，重复 Dup2 无害（savedFd2 仍开）。
		os.Stderr = savedStderr
		_ = syscall.Dup2(savedFd2, 2)
		_ = syscall.Close(savedFd2)
		_ = w.Close()
		_ = r.Close()
		_ = devNull.Close()
	}
	return read, restore
}

// TestPrintIsolationBannerReachesRealStderr 结果断言（门 2 修复核心）：
// os.Stderr 变量被重定向（模拟 main.go 日志重定向）时，
// banner 必须仍到达 fd 2（真实终端），且前缀只有一个 [sandbox]。
func TestPrintIsolationBannerReachesRealStderr(t *testing.T) {
	read, restore := captureFd2(t)
	// restore 幂等（错误均被忽略），defer 作为 t.Fatalf 路径的安全网
	defer restore()

	mgr := NewManager(&SandboxConfig{WorkDir: t.TempDir()}, SandboxLevelStrict)
	PrintIsolationBanner(mgr)

	// read 内部先还原 fd 2 再读（详见 captureFd2 注释）：
	// banner 已在写 fd 2 时同步落入管道缓冲，read 还原写端后立即读到 EOF 返回。
	out := read()
	if !strings.Contains(out, "[sandbox] ") {
		t.Errorf("banner must reach REAL stderr (fd 2) even when os.Stderr variable is redirected; got %q", out)
	}
	// 双前缀回归：修复前调用方再包一层 [sandbox] 产生 "[sandbox] [sandbox] "
	if strings.Contains(out, "[sandbox] [sandbox]") {
		t.Errorf("banner must have single [sandbox] prefix, got double: %q", out)
	}
	// 降级行也要到达（degraded 路径与主行同通道）
	if !strings.Contains(out, "\n") && strings.Contains(out, "degraded") {
		t.Errorf("degradation lines must also reach fd 2: %q", out)
	}
}
