// Package sandbox — 门 3 网络开关测试（TDD：失败测试先行）。
//
// 粗粒度开关（默认断网姿态不变）：
// - Linux：AllowNetwork=true 时 NewManager 归一化过滤 "net" namespace
// （单一事实源：cloneflags / status banner / 审计状态同源）
// - macOS：AllowNetwork=true 时 Seatbelt profile 省略 (deny network*)
// - 生产入口：env WHALE_SANDBOX_ALLOW_NETWORK=1/true 开启
//
// 分层验证链：
// 1. 存在：AllowNetwork 字段被配置接受
// 2. 已接线：NewManager 归一化 + 生产工厂读 env
// 3. 防护真的发生：net namespace 不在 Cloneflags / profile 无 deny network*
// （平台特定测试见 os_sandbox_linux_test.go / os_sandbox_darwin_profile_test.go）
// 4. 不影响正常功能：默认（无 env）时网络隔离保持
package sandbox

import (
	"runtime"
	"strings"
	"testing"
)

// TestProductionNetworkSwitchFromEnv 生产工厂 env 接线（结果断言）：
// env 开启时 banner 可见 network=on；默认时不可见（断网默认姿态）。
// Windows 例外：Job Object 不隔离网络（GAP-WIN-P3），网络本就通，
// 开关无操作也不宣称 network=on（宣称即说谎）。
func TestProductionNetworkSwitchFromEnv(t *testing.T) {
	// 默认：无 env → 不出现 network=on
	//（显式置空，防开发者 shell 里已 export 该 env 造成假失败——审查 #7）
	t.Setenv("WHALE_SANDBOX_ALLOW_NETWORK", "")
	mgr := NewProductionSandboxManager(t.TempDir())
	st := IsolationStatusOf(mgr)
	for _, f := range st.Features {
		if strings.Contains(f, "network=on") {
			t.Errorf("default (no env) must keep network isolated, got feature %q", f)
		}
	}

	// 开启：env → banner 可见 network=on（安全降级必须用户可见）
	t.Setenv("WHALE_SANDBOX_ALLOW_NETWORK", "1")
	mgr = NewProductionSandboxManager(t.TempDir())
	st = IsolationStatusOf(mgr)
	found := false
	for _, f := range st.Features {
		if f == "network=on" {
			found = true
		}
	}
	if runtime.GOOS == "windows" {
		if found {
			t.Errorf("windows must NOT claim network=on (job object has no network isolation, GAP-WIN-P3): %v", st.Features)
		}
	} else if !found {
		// ★ 2026-10-09（接线）：沙箱**降级**时没有任何特性可宣称（`IsolationStatusOf`
		// 对 degraded 返回空 Features）⇒ `network=on` 也不该出现（出现即说谎）。
		// 此时本用例**观察不到**开/关差异（没有 profile 被生成）——那是环境限制，
		// 不是接线错误；同时把"降级不得谎报 network=on"这条负向断言留下。
		if st.Platform == "degraded" {
			t.Skipf("本机 OS 沙箱降级（platform=%q）⇒ 无从观察 network=on 的开/关差异（环境限制，非判据错误）",
				st.Platform)
		}
		t.Errorf("env WHALE_SANDBOX_ALLOW_NETWORK=1 → Features %v must contain network=on", st.Features)
	}
}

// TestProductionNetworkSwitchEnvValues env 解析值域：
// 1/true/yes/on（大小写不敏感）开启；其余（含 0/false/垃圾值）保持断网。
func TestProductionNetworkSwitchEnvValues(t *testing.T) {
	for _, v := range []string{"1", "true", "TRUE", "yes", "on"} {
		t.Setenv("WHALE_SANDBOX_ALLOW_NETWORK", v)
		if !networkAllowedByEnv() {
			t.Errorf("env value %q should enable network", v)
		}
	}
	for _, v := range []string{"", "0", "false", "no", "off", "garbage"} {
		t.Setenv("WHALE_SANDBOX_ALLOW_NETWORK", v)
		if networkAllowedByEnv() {
			t.Errorf("env value %q must NOT enable network", v)
		}
	}
}

// TestNewManagerAllowNetworkNormalizesLinuxNet Linux 归一化（跨平台可编译的配置级断言：
// 归一化发生在 NewManager，与平台无关）。
func TestNewManagerAllowNetworkNormalizesLinuxNet(t *testing.T) {
	cfg := &SandboxConfig{
		OSLevel: OSLevelConfig{
			Linux: LinuxSandboxConfig{
				UseNamespaces: true,
				Namespaces:    []string{"pid", "net", "uts", "mount"},
				AllowNetwork:  true,
			},
		},
	}
	m := NewManager(cfg, SandboxLevelStrict)
	mgr, ok := m.(*manager)
	if !ok {
		t.Fatal("NewManager must return *manager")
	}
	for _, ns := range mgr.config.OSLevel.Linux.Namespaces {
		if ns == "net" {
			t.Errorf("AllowNetwork=true must filter 'net' namespace (single source of truth), got %v",
				mgr.config.OSLevel.Linux.Namespaces)
		}
	}
	// 关闭时不误伤：AllowNetwork=false 保留 net
	cfg2 := &SandboxConfig{
		OSLevel: OSLevelConfig{
			Linux: LinuxSandboxConfig{
				UseNamespaces: true,
				Namespaces:    []string{"pid", "net", "uts", "mount"},
			},
		},
	}
	m2 := NewManager(cfg2, SandboxLevelStrict).(*manager)
	hasNet := false
	for _, ns := range m2.config.OSLevel.Linux.Namespaces {
		if ns == "net" {
			hasNet = true
		}
	}
	if !hasNet {
		t.Error("AllowNetwork=false (default) must keep 'net' namespace")
	}
}

// TestNewSandboxNetworkSwitchCoherent 门 3 口径一致性（对抗式审查 #1）：
// Layer-2 应用层与 OS 级开关消费同一 env——banner 宣称 network=on 时
// curl 必须真的可用（networkIsolated=false + deny 列表移除 curl/wget），
// 否则三层断网机制各自为政（OS 放行、应用层拒绝）。
// 默认姿态不变：网络阻断 + curl 拒绝 + 破坏性命令（rm）不受开关影响。
func TestNewSandboxNetworkSwitchCoherent(t *testing.T) {
	// 默认：networkIsolated=true + curl 被 deny 列表拦截
	t.Setenv("WHALE_SANDBOX_ALLOW_NETWORK", "")
	s := NewSandbox(t.TempDir())
	s.SetEnabled(true)
	if !s.IsNetworkIsolated() {
		t.Error("default must keep networkIsolated=true")
	}
	if ok, _ := s.Validate("curl http://example.com"); ok {
		t.Error("default must block curl (deny list)")
	}

	// env 开启：三层一致——networkIsolated=false、curl 放行、rm 仍拒
	t.Setenv("WHALE_SANDBOX_ALLOW_NETWORK", "1")
	s = NewSandbox(t.TempDir())
	s.SetEnabled(true)
	if s.IsNetworkIsolated() {
		t.Error("env on must set networkIsolated=false (same source as OS switch)")
	}
	if ok, reason := s.Validate("curl http://example.com"); !ok {
		t.Errorf("env on must allow curl (banner says network=on), got block: %s", reason)
	}
	if ok, _ := s.Validate("rm -rf /tmp/nonexistent-probe"); ok {
		t.Error("env on must still block rm (destructive deny list unaffected by network switch)")
	}
}
