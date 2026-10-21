package tui

// gu_testinfra_test.go — G-U 轮新增用例的**测试基建**（只服务本轮新增文件，不改既有夹具）。
//
// 解决的问题（本轮实测踩到、非猜想的 flaky）：
//
//	`NewModel` 的生产装配会启动 H11N27 的 ForgettingScanner 后台协程
//	（lifecycle.go:1113-1115 → memory_wiring.go:154-166 → memory.StartForgettingScanner），
//	该协程在**启动时**先做一次补扫描，把 `.whale/memory/.last_scan_at` 写进 `$HOME`。
//	既有夹具 `newApprovalTestModel` 不替换 HOME，故扫描器落在 TestMain 的共享临时 HOME 里，
//	只在进程退出时整体删除 ⇒ 看不到问题。
//	而本轮 A1 / A8 / F3 的用例为了隔离真实用户目录，做了 `t.Setenv("HOME", t.TempDir())`：
//	扫描器于是写进**本测**临时 HOME，与 `t.TempDir()` 的 RemoveAll 竞争 ⇒ 随机报
//	`TempDir RemoveAll cleanup: ... directory not empty`（时序相关、非确定性）。
//
// 修法 = 走**产品自己的退出路径**：`Model.CancelWiring()`（main.go 的 tuiShutdown 也调它）。
// 它在 cancel 之后 wait 扫描协程退出，且幂等（lifecycle.go:1175-1190）。
// 顺序保证：`t.Cleanup` 为 LIFO，本 helper 注册的收尾**晚于** `t.TempDir()`，
// 因此先停协程、后删目录 —— 顺序反了就等于没修。
//
// 纪律：新增用例若构造了进程级后台协程（NewModel / NewPlainRunner / NewBridgeRunner），
// 必须配一条收尾。否则它今天不红，也会在负载变化时变成"偶发红"。
//
// 2026-09-20 补：隔离目录本身也已收口到 `internal/testutil.IsolatedHomeDir`（宽松清理），
// 并加了全仓机械门（仓库根 `tempdir_home_gate_test.go`）禁止 `t.TempDir()` 作 HOME。
// 两条纪律**并存、互补**：① 清理不得断言（testutil）② 协程该停要停（本文件 teardown）。

import "testing"

// guIsolatedHome 把 HOME 隔离到本测专属临时目录，返回该目录路径。
// 只做隔离，不构造 Model（供需要在构造 Model **之前**写 $HOME 下文件的用例使用）。
//
// ★ 目录创建改走 `newIsolatedTestHome`（= `internal/testutil.IsolatedHomeDir`，宽松清理），
// **不再用 `t.TempDir()`**：后台协程会在用例结束后仍写 $HOME，与其 RemoveAll 清理断言竞态
// （`unlinkat .../.whale: directory not empty`，非确定性）。机械门见仓库根
// `tempdir_home_gate_test.go`。
//
// ⇒ 上面那段"靠 `t.Cleanup` 的 LIFO 顺序抢在删目录之前收尾"的前提**已不再是绿的必需条件**；
// 但 `guTrackModelTeardown` 仍必须保留：它的真正职责是**停掉协程**，防止残留协程把
// 本测临时目录上的**进程级全局量**泄漏给后续用例 —— 那是清理语义之外的另一件事。
func guIsolatedHome(t *testing.T) string {
	t.Helper()
	home := newIsolatedTestHome(t)
	t.Setenv("HOME", home)
	return home
}

// guTrackModelTeardown 注册 Model 的生产退出收尾。
//   - `CancelWiring`：停 wiring 协程（ForgettingScanner 等）并等其退出；
//   - `resetMemoryWiringGlobalsForTest`：复位 Model 接线写入的**进程级全局量**
//     （`globalMergeLayer` / `defaultMemoryStore` / `aiCaller`），否则这些单例指向本测
//     临时目录，目录删除后会污染后续用例（见 memory_wiring_test.go 中该函数的注释）。
//
// 必须在 `t.TempDir()` 之后调用（LIFO ⇒ 先收尾、后删目录）。
func guTrackModelTeardown(t *testing.T, m *Model) {
	t.Helper()
	t.Cleanup(func() {
		m.CancelWiring()
		resetMemoryWiringGlobalsForTest()
	})
}

// guModelIsolatedHome 常用路径：隔离 HOME → 经生产入口构造 Model → 注册退出收尾。
// 返回 Model 与本测 HOME 路径。
func guModelIsolatedHome(t *testing.T) (Model, string) {
	t.Helper()
	home := guIsolatedHome(t)
	m := newApprovalTestModel(t)
	guTrackModelTeardown(t, &m)
	return m, home
}
