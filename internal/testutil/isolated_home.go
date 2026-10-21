// Package testutil 提供**跨包复用**的测试支撑。
//
// 纪律：本包只服务测试，**生产代码不得 import**（它是普通包而非 `_test.go`，
// 因为 Go 不允许跨包引用别人的测试文件）。
//
// 命名对标仓库既有的 `internal/mock`（同样是为测试而生的普通包）。
package testutil

import "os"

// TB 是本包所需的 `testing.TB` 子集。
//
// ★ 为什么用自定义接口而不是直接 `import "testing"`：本仓此前**没有任何**非 `_test.go`
// 文件 import `testing`（实测 grep）。引入它会让 `testing` 进入生产构建图
// （`testing` 会注册 flag、持有 os.Exit 路径），不值得为几行 helper 付这个代价。
// `*testing.T` / `*testing.B` 天然满足本接口，调用方无感。
type TB interface {
	Helper()
	Fatalf(format string, args ...any)
	Cleanup(func())
}

// IsolatedHomeDir 新建一个**宽松清理**的临时目录并返回其路径，用作用例专属 `$HOME`。
//
// 典型调用（一行，保持 `t.Setenv` 原有的还原语义与并行守卫）：
//
//	t.Setenv("HOME", testutil.IsolatedHomeDir(t))
//
// # 为什么不用 t.TempDir()
//
// `t.TempDir()` 注册的清理会**断言**目录已空（`RemoveAll` 失败即把用例判红），
// 而 `$HOME` 恰是**后台协程会持续写入**的目录 —— 本仓已知的写入者有：
//
//   - filehistory persist 协程
//   - usage tracker
//   - memory wiring 的 ForgettingScanner（启动时补扫描，写 `.whale/memory/.last_scan_at`）
//
// 二者叠加是 TOCTOU 竞态：`RemoveAll` 正在删 `.whale/` 的同时协程又往里面建条目 ⇒
//
//	TempDir RemoveAll cleanup: unlinkat .../.whale: directory not empty
//
// 该形态**非确定性**（同一批用例换个时序全绿）⇒「全量绿」不能作为"没问题"的证据。
// 本仓 2026-09-20 实测复现过一次（`internal/tui` 全量回归 2 条 picker 用例）。
//
// 机械门：仓库根 `tempdir_home_gate_test.go`（`TestGate_NoTempDirHomeIsolation`）
// 全仓禁止 `t.TempDir()` 作 HOME —— 新增即红；修法就是改调本函数。
//
// # 为什么本函数不自己设环境变量
//
// 若在包内直接 `os.Setenv`，就**丢掉了 `t.Setenv` 的两个语义**：① 用例结束自动还原
// （否则临时 HOME 会泄漏给同进程的后续用例）；② 并行用例下的保护性 panic。
// 因此本函数只负责"目录的清理语义"，环境变量仍由调用方的 `t.Setenv` 设定。
//
// # 与 CancelWiring 的关系
//
// 本函数只负责**不误判**；它**不**替代收尾。构造了 `NewModel` / `NewPlainRunner` /
// `NewBridgeRunner` 的用例**仍应**走生产退出路径（`Model.CancelWiring()`）停掉协程 ——
// 那是"该停的要停"的卫生要求（否则残留协程会污染后续用例的进程级全局量），
// 与此处"清理不得断言"是两件事，二者互补而非替代。
func IsolatedHomeDir(tb TB) string {
	tb.Helper()
	dir, err := os.MkdirTemp("", "whale-test-home-*")
	if err != nil {
		tb.Fatalf("testutil.IsolatedHomeDir: MkdirTemp failed: %v", err)
	}
	tb.Cleanup(func() { _ = os.RemoveAll(dir) }) // 忽略错误：关键（见上文 TOCTOU）
	return dir
}
