// g_gg_a9_unreadable_test.go — G-G（O-G）A9 判别性测试：配置源「存在但不可读」= fail-closed T3。
//
// 决策依据：集中拍板_决策表与并行修复分组_2026-09-14.md
//
//	§2 A9 行（★已拍板）：
//
// A9 | S12-N3 | 配置源「存在但不可读」（EACCES）被**静默 continue**
// | 拒启 = T3（与 A8 同族；N3 本就与「损坏」同属 fail-open 家族）
//
//	§6.3 G-G 行：「A9 = 配置源『存在但不可读』**拒启（T3）**，
//
// 与 S12-PR#2 的『损坏拒启』**同档**」
//
//	§1 族A 口诀：「缺的是能力 → T1；缺的是判据 → T2；缺的是**保护器** → T3」
//
// —— 读不到配置 = 拿不到用户的安全相关设置（permission_mode 等）⇒ 判据缺失、
// 且没有更外层门可托底 ⇒ 拒启（T3）。与「损坏拒启」同族同档。
//
// 改码前实测（真二进制 `/tmp/gg/whale_before`，2026-09-15 01:5x）：
//
//	HOME=<tmp> 且 `~/.whale/settings.json` = chmod 000（内容为**合法** JSON + api_key）
//	⇒ **未拒启**，继续 onboarding/doctor，输出 `[FATAL] API Key: NOT SET`，rc=1。
//	即：不可读的配置源被当成「不存在」静默吞掉（fail-open）—— 这正是 A9 的缺陷面。
//
// 本文件的主用例断言的是 **Fix 后**语义 ⇒ 它在 before 态（把改动行回退成 `continue`
// 的 `-overlay` 副本）必须**精确打红**，这就是本项改动的判别性证据（见 内部状态清单 §A/B）。
// 用例名刻意不带 `Char` 前缀，避免 G-E 在 §1.17 记过的「用例名描述旧缺陷」命名债。
package config

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

// unreadableConfigSource 造一个「存在但不可读」的 user 级配置源。
//
// 返回 (userDir, sourcePath)。userDir 的用法与 main.go 的
// loadConfigWithOnboarding 一致（NewLoader(homeDir, cwd)）。
func unreadableConfigSource(t *testing.T) (string, string) {
	t.Helper()
	dir := t.TempDir()
	userDir := filepath.Join(dir, "user")
	whaleDir := filepath.Join(userDir, ".whale")
	if err := os.MkdirAll(whaleDir, 0o700); err != nil {
		t.Fatal(err)
	}
	src := filepath.Join(whaleDir, "settings.json")
	// 内容本身**合法**（含 api_key）—— 不可读的只是权限位。
	// 这样「修复前静默跳过」与「修复后拒启」的差异不可能来自 JSON 解析失败。
	payload := `{"permission_mode":"read-only","api_key":"sk-ant-must-not-be-silently-dropped"}`
	if err := os.WriteFile(src, []byte(payload), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(src, 0o000); err != nil {
		t.Fatal(err)
	}
	// 复原权限，避免 TempDir 清理期意外，并保证用例可重跑。
	t.Cleanup(func() { _ = os.Chmod(src, 0o600) })

	// 前置断言：确保在**当前进程身份**下确实是「不可读」。
	// 若以 root 运行，chmod 000 仍可读 ⇒ 用例会静默退化成「合法配置」，
	// A9 断言就失去意义（假绿），故显式跳过而不是让它假通过。
	if _, err := os.ReadFile(src); err == nil {
		t.Skipf("当前身份可读 chmod 000 文件（疑似 root），A9 不可判别，跳过")
	} else if os.IsNotExist(err) {
		t.Fatalf("前置条件失败：文件应存在但不可读，实际 os.IsNotExist=%v（err=%v）", os.IsNotExist(err), err)
	}
	return userDir, src
}

// TestGG_A9_UnreadableSourceIsFailClosed —— A9 T3 主语义（before 态必须红）。
//
// 期望（Fix 后）：两个生产入口都 fail-closed ——
// - Loader.CheckIntegrity() → ErrUnreadableConfig（启动前置只读预检，先于 onboarding 写盘）
// - Loader.Load() → (nil, ErrUnreadableConfig)
//
// before 态（characterization 实测）：两者都返回 nil（静默跳过），Load 还返回非 nil 默认配置。
func TestGG_A9_UnreadableSourceIsFailClosed(t *testing.T) {
	clearConfigEnv(t)
	userDir, src := unreadableConfigSource(t)
	t.Setenv("WHALE_HOME", filepath.Join(userDir, ".whale"))

	l := NewLoader(userDir, "")

	// 入口①：CheckIntegrity —— main.loadConfigWithOnboarding 的启动前置预检，
	// 先于任何写盘流程（onboarding 会 Save settings.json）⇒ 必须在这里就拦住。
	ciErr := l.CheckIntegrity()
	if ciErr == nil {
		t.Errorf("A9 T3：配置源 %s 存在但不可读，CheckIntegrity 必须 fail-closed，实际返回 nil", src)
	} else if !errors.Is(ciErr, ErrUnreadableConfig) {
		t.Errorf("A9：CheckIntegrity 应报 ErrUnreadableConfig，实际：%v", ciErr)
	}

	// 入口②：Load —— 配置真正装配点。
	cfg, err := l.Load()
	if err == nil {
		t.Fatalf("A9 T3：配置源存在但不可读，Load 必须返回 error（不得静默回退默认值），实际 nil；cfg=%+v", cfg)
	}
	if !errors.Is(err, ErrUnreadableConfig) {
		t.Errorf("A9：error 应为 ErrUnreadableConfig，实际：%v", err)
	}
	if cfg != nil {
		t.Errorf("A9：不可读时 Load 不应返回可用配置，实际非 nil：%+v", cfg)
	}
}

// TestGG_A9_MissingSourceIsStillLegal —— 反例守卫（判别性，防越界）。
//
// 「缺失」= 全新安装的合法状态（对齐业内既有实现：缺失不报错），**不得**被 A9 的
// fail-closed 扩大成「缺失也拒启」。若修复把 os.IsNotExist 一并纳入错误，本用例变红。
func TestGG_A9_MissingSourceIsStillLegal(t *testing.T) {
	clearConfigEnv(t)
	dir := t.TempDir()
	userDir := filepath.Join(dir, "user") // 无任何配置文件

	l := NewLoader(userDir, "")
	if err := l.CheckIntegrity(); err != nil {
		t.Errorf("A9 不得越界：文件缺失应合法（全新安装），CheckIntegrity 实际报错：%v", err)
	}
	cfg, err := l.Load()
	if err != nil {
		t.Fatalf("A9 不得越界：文件缺失时 Load 应成功，实际：%v", err)
	}
	if cfg == nil {
		t.Fatal("文件缺失时应返回默认配置")
	}
}

// TestGG_A9_CorruptSourceKeepsErrCorruptConfig —— 反例守卫（判别性，防语义吞并）。
//
// 两种失效必须**各归其类**：读不到 ⇒ ErrUnreadableConfig；读到了但不是 JSON ⇒ ErrCorruptConfig。
// 若修复把「不可读」直接复用 ErrCorruptConfig（或读错误分支漏判成损坏），本用例变红。
func TestGG_A9_CorruptSourceKeepsErrCorruptConfig(t *testing.T) {
	clearConfigEnv(t)
	dir := t.TempDir()
	userDir := filepath.Join(dir, "user")
	whaleDir := filepath.Join(userDir, ".whale")
	t.Setenv("WHALE_HOME", whaleDir)
	if err := os.MkdirAll(whaleDir, 0o700); err != nil {
		t.Fatal(err)
	}
	src := filepath.Join(whaleDir, "settings.json")
	if err := os.WriteFile(src, []byte("{ broken json ,,, "), 0o600); err != nil {
		t.Fatal(err)
	}

	l := NewLoader(userDir, "")
	err := l.CheckIntegrity()
	if !errors.Is(err, ErrCorruptConfig) {
		t.Fatalf("损坏源应报 ErrCorruptConfig，实际：%v", err)
	}
	if errors.Is(err, ErrUnreadableConfig) {
		t.Errorf("损坏源不得被归类为 ErrUnreadableConfig（两类失效必须可区分）：%v", err)
	}
}
