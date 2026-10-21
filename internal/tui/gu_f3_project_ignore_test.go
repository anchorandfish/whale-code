package tui

// gu_f3_project_ignore_test.go — F3（移交单 G-C-002）：
// `secret.SetGlobalProjectIgnore` 必须由**两条启动路径**各自注入 cwd。
//
// 缺陷（G-C 定位）：该函数全仓此前**只有定义 + 测试**（零生产调用）
// ⇒ `<proj>/.whaleignore` 与 `~/.whale/whaleignore` 运行时完全不生效，
// 敏感判定只剩内置默认表。方向 = fail-safe（拍板 A：启动注入 cwd）。
//
// 判别性：项目级 pattern 用一个**内置默认表必然不含**的文件名，
// 因此"命中"只可能来自本次启动注入 ⇒ 单点摘掉那一行即回落为未命中（见回执 A/B）。

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/anchorandfish/whale-code/internal/config"
	"github.com/anchorandfish/whale-code/internal/secret"
)

// guF3Project 造一个含项目级 .whaleignore 的工程目录，返回 (项目根, 应被判敏感的文件)。
func guF3Project(t *testing.T, pattern string) (string, string) {
	t.Helper()
	proj := t.TempDir()
	if err := os.WriteFile(filepath.Join(proj, ".whaleignore"), []byte(pattern+"\n"), 0644); err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(proj, pattern)
	if err := os.WriteFile(target, []byte("x"), 0644); err != nil {
		t.Fatal(err)
	}
	return proj, target
}

// guF3ResetGlobal 把全局单例指到一个**不匹配**的根，确保"命中"只可能来自被测启动路径。
func guF3ResetGlobal(t *testing.T, target string) {
	t.Helper()
	secret.SetGlobalProjectIgnore(t.TempDir())
	if secret.IsSensitiveFile(target) {
		t.Fatalf("前置失败：重置全局 ignore 后 %s 不应命中（否则判据不可判别）", target)
	}
	t.Cleanup(func() { secret.SetGlobalProjectIgnore("") })
}

// TestGU_F3_PlainPathStartupInjectsProjectIgnore plain/bridge 路径（NewRunnerCore）。
func TestGU_F3_PlainPathStartupInjectsProjectIgnore(t *testing.T) {
	_ = guIsolatedHome(t)
	// 内置默认表不含这个名字 ⇒ 命中只能来自项目级 .whaleignore。
	const pattern = "gu-f3-plain-secret-notes.txt"
	proj, target := guF3Project(t, pattern)
	t.Chdir(proj)
	guF3ResetGlobal(t, target)

	cfg := config.DefaultConfig()
	cfg.Model = "test-model"
	_ = NewRunnerCore(&cfg, newTestClient("test-model"), newTestRegistry(),
		proj, t.TempDir(), filepath.Join(t.TempDir(), "sessions"), "Chinese")

	if !secret.IsSensitiveFile(target) {
		t.Fatalf("F3：NewRunnerCore（plain/bridge 启动路径）未注入项目级 ignore —— "+
			"%s/.whaleignore 不生效", proj)
	}
}

// TestGU_F3_TuiPathStartupInjectsProjectIgnore TUI 路径（NewModel）。
//
// ⚠️ 构造次序不可换：必须先 `guF3ResetGlobal` 再 `NewModel`。
// 因为 `NewModel` 会按当前 cwd 注入项目根（正是被测行为），一旦先构造 Model，
// 全局单例就已指向本工程 ⇒ 前置断言（"重置后不命中"）无法成立、判据不可判别。
func TestGU_F3_TuiPathStartupInjectsProjectIgnore(t *testing.T) {
	_ = guIsolatedHome(t)
	const pattern = "gu-f3-tui-secret-notes.txt"
	proj, target := guF3Project(t, pattern)
	t.Chdir(proj)
	guF3ResetGlobal(t, target)

	m := NewModel(newTestConfig(), newTestClient("test-model"), newTestRegistry())
	guTrackModelTeardown(t, &m)
	if m.toolService.Executor() == nil {
		t.Fatal("前置失败：NewModel 未装配 executor")
	}

	if !secret.IsSensitiveFile(target) {
		t.Fatalf("F3：NewModel（TUI 启动路径）未注入项目级 ignore —— %s/.whaleignore 不生效", proj)
	}
	// 反向：注入的根必须是当前工程，而不是别处（否则只是"碰巧命中"）。
	if got := secret.GetGlobalProjectIgnore(); got == nil || !got.IsSensitive(target) {
		t.Fatalf("F3：全局 ignore 实例未指向当前工程根 %s", proj)
	}
}

// TestGU_F3_UserGlobalWhaleignoreAlsoApplies 顺带钉住 ~/.whale/whaleignore（用户全局层）：
// 同一注入点必须让全局层也生效（否则 F3 只修了一半）。
//
// ⚠️ 时序不变量（勿调换）：用户全局文件必须**在 guF3ResetGlobal 之后**才落盘。
// 因为 NewProjectIgnore 会同时加载 项目级<root>/.whaleignore **与** 用户全局
// $HOME/.whale/whaleignore；若先写用户全局，guF3ResetGlobal 的重置实例就会
// 命中该 pattern，前置断言（"重置后不应命中"）本身失败 ⇒ 判据不可判别。
// 正确次序：先 reset（HOME 下尚无 whaleignore，前置必然不命中）→ 再落用户全局
// → NewModel 注入时加载用户全局 ⇒ 命中只可能来自本次注入。
func TestGU_F3_UserGlobalWhaleignoreAlsoApplies(t *testing.T) {
	home := guIsolatedHome(t)
	const pattern = "gu-f3-user-global-notes.txt"
	proj := t.TempDir()
	target := filepath.Join(proj, pattern)
	if err := os.WriteFile(target, []byte("x"), 0644); err != nil {
		t.Fatal(err)
	}
	t.Chdir(proj)

	// 步骤 1：重置全局单例。此刻 HOME 下无 ~/.whale/whaleignore，前置必须不命中。
	guF3ResetGlobal(t, target)

	// 步骤 2：现在才写用户全局层。项目级 .whaleignore 不存在 ⇒ 后续命中
	// 只可能来自注入点加载的用户全局文件（内置默认表不含该 pattern）。
	if err := os.MkdirAll(filepath.Join(home, ".whale"), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(home, ".whale", "whaleignore"), []byte(pattern+"\n"), 0644); err != nil {
		t.Fatal(err)
	}

	// 步骤 3：构造 Model（注入点在此加载 $HOME/.whale/whaleignore）。
	m := NewModel(newTestConfig(), newTestClient("test-model"), newTestRegistry())
	guTrackModelTeardown(t, &m)

	if !secret.IsSensitiveFile(target) {
		t.Fatalf("F3：用户全局 ~/.whale/whaleignore 未生效（注入点只覆盖项目级层）")
	}
}
