package plugin

// G2-P0-1 反例测试：plugin Install 路径不调 SignatureVerifier 修复
//
// 测试目标：
//   - 配置 verifier 且验签失败时，Install 拒绝安装
//   - 未配置 verifier 且 allowUnsignedPlugins=false 时，Install fail-closed 拒绝
//   - 未配置 verifier 且 allowUnsignedPlugins=true 时，Install 允许但记审计
//   - 配置 verifier 且验签通过时，Install 成功
//
// 修复前：Install 全流程无 SignatureVerifier.Verify 调用，恶意插件可直接加载。
// 修复后：Install 强制调用 Verify，fail-closed 降级。

import (
	"os"
	"path/filepath"
	"testing"
)

// createTestPluginDir 创建一个测试插件目录（含 plugin.json）。
//
// 参数：
//   - t: 测试实例
//   - id: 插件 ID
//
// 返回：插件目录路径。
func createTestPluginDir(t *testing.T, id string) string {
	t.Helper()
	dir := t.TempDir()
	manifest := `{
		"id": "` + id + `",
		"name": "Test Plugin",
		"version": "1.0.0",
		"description": "test plugin"
	}`
	if err := os.WriteFile(filepath.Join(dir, "plugin.json"), []byte(manifest), 0644); err != nil {
		t.Fatalf("write plugin.json failed: %v", err)
	}
	return dir
}

// fakeVerifier 是测试用的签名校验器，可控制验签结果。
type fakeVerifier struct {
	shouldPass bool
	called     bool
}

// Verify 测试用验签实现，根据 shouldPass 返回 nil 或 error。
func (f *fakeVerifier) Verify(_ string) error {
	f.called = true
	if !f.shouldPass {
		return errFakeVerifyFailed
	}
	return nil
}

// VerifyBytes 测试用验签实现，始终返回 nil（本测试不覆盖此路径）。
func (f *fakeVerifier) VerifyBytes(_ []byte, _ string) error {
	return nil
}

// errFakeVerifyFailed 是 fakeVerifier 验签失败时返回的错误。
var errFakeVerifyFailed = errFake("signature verification failed")

// errFake 是测试用错误类型。
type errFake string

func (e errFake) Error() string { return string(e) }

// TestG2P0_1_InstallRejectsFailedVerification 反例测试（G2-P0-1）：验签失败时 Install 拒绝。
//
// 修复前：Install 不调 Verify，验签失败仍可安装。
// 修复后：Install 调 Verify，验签失败返回 "plugin signature verification failed"。
func TestG2P0_1_InstallRejectsFailedVerification(t *testing.T) {
	pluginDir := createTestPluginDir(t, "test-fail-verify")

	m := NewManager(t.TempDir())
	m.SetStatePath(filepath.Join(t.TempDir(), "state.json"))
	// 配置一个验签失败的 verifier
	m.SetSignatureVerifier(&fakeVerifier{shouldPass: false})

	_, err := m.Install(pluginDir)
	if err == nil {
		t.Fatalf("G2-P0-1 反例失败: 验签失败时 Install 应拒绝\n" +
			"  期望: error 非 nil（验签失败）\n" +
			"  实际: error=nil（未调 Verify 或忽略失败）")
	}
}

// TestG2P0_1_InstallFailClosedNoVerifier 反例测试（G2-P0-1）：未配置 verifier 时 fail-closed 拒绝。
//
// 修复前：Install 不调 Verify，未配置 verifier 时直接安装（fail-open）。
// 修复后：未配置 verifier 且 allowUnsignedPlugins=false 时拒绝安装（fail-closed）。
func TestG2P0_1_InstallFailClosedNoVerifier(t *testing.T) {
	pluginDir := createTestPluginDir(t, "test-no-verifier")

	m := NewManager(t.TempDir())
	m.SetStatePath(filepath.Join(t.TempDir(), "state.json"))
	// 不配置 verifier，allowUnsignedPlugins 默认 false（fail-closed）

	_, err := m.Install(pluginDir)
	if err == nil {
		t.Fatalf("G2-P0-1 反例失败: 未配置 verifier 时应 fail-closed 拒绝\n" +
			"  期望: error 非 nil（fail-closed）\n" +
			"  实际: error=nil（fail-open，恶意插件可直接安装）")
	}
}

// TestG2P0_1_InstallAllowsUnsignedWithOptIn 验证显式 opt-in 时允许未签名插件（G2-P0-1 降级测试）。
//
// allowUnsignedPlugins=true 时，未配置 verifier 仍可安装（记审计警告）。
// 用于向后兼容旧插件市场——需显式 opt-in。
func TestG2P0_1_InstallAllowsUnsignedWithOptIn(t *testing.T) {
	pluginDir := createTestPluginDir(t, "test-unsigned-optin")

	m := NewManager(t.TempDir())
	m.SetStatePath(filepath.Join(t.TempDir(), "state.json"))
	// 不配置 verifier，但显式 opt-in 允许未签名插件
	m.SetAllowUnsignedPlugins(true)

	plugin, err := m.Install(pluginDir)
	if err != nil {
		t.Fatalf("G2-P0-1 降级失败: allowUnsignedPlugins=true 时应允许安装\n"+
			"  期望: error=nil\n"+
			"  实际: error=%v", err)
	}
	if plugin == nil || plugin.ID != "test-unsigned-optin" {
		t.Errorf("G2-P0-1 降级失败: 插件未正确安装\n"+
			"  期望: plugin.ID=test-unsigned-optin\n"+
			"  实际: plugin=%v", plugin)
	}
}

// TestG2P0_1_InstallSucceedsWithValidVerification 验证验签通过时 Install 成功（G2-P0-1 回归测试）。
//
// 配置验签通过的 verifier，Install 应成功安装插件。
func TestG2P0_1_InstallSucceedsWithValidVerification(t *testing.T) {
	pluginDir := createTestPluginDir(t, "test-valid-verify")

	m := NewManager(t.TempDir())
	m.SetStatePath(filepath.Join(t.TempDir(), "state.json"))
	// 配置一个验签通过的 verifier
	verifier := &fakeVerifier{shouldPass: true}
	m.SetSignatureVerifier(verifier)

	plugin, err := m.Install(pluginDir)
	if err != nil {
		t.Fatalf("G2-P0-1 回归失败: 验签通过时 Install 应成功\n"+
			"  期望: error=nil\n"+
			"  实际: error=%v", err)
	}
	if plugin == nil || plugin.ID != "test-valid-verify" {
		t.Errorf("G2-P0-1 回归失败: 插件未正确安装\n"+
			"  期望: plugin.ID=test-valid-verify\n"+
			"  实际: plugin=%v", plugin)
	}
	if !verifier.called {
		t.Errorf("G2-P0-1 回归失败: SignatureVerifier.Verify 未被调用\n" +
			"  期望: verifier.called=true\n" +
			"  实际: verifier.called=false")
	}
}
