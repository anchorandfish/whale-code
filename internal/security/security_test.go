// Package security 测试 — H2 黑名单安全模型统一收口。
//
// 覆盖 12 个常规用例 + 5 个绕过反例（BypassVariants）。
// 测试通过 newTestChecker 构造不同 sandbox 配置的 Checker：
//   - sandboxEnabled=false（生产默认）：测 Layer 1（validator）+ Layer 3（permission）行为
//   - sandboxEnabled=true：测 Layer 2（sandbox）拦截行为
package security

import (
	"fmt"
	"strings"
	"sync"
	"testing"

	aierrors "github.com/anchorandfish/whale-code/internal/ai"
	"github.com/anchorandfish/whale-code/internal/permission"
	"github.com/anchorandfish/whale-code/internal/sandbox"
	"github.com/anchorandfish/whale-code/internal/sandbox/validator"
)

// newTestChecker 构造测试用 Checker。
// sandboxEnabled 控制 sandbox.Sandbox.enabled（生产默认 false，测试 Layer 2 时设 true）。
// networkIsolated 固定 true 以测网络命令拦截。
func newTestChecker(t *testing.T, sandboxEnabled bool) *Checker {
	t.Helper()
	s := sandbox.NewSandbox("") // workDir 空，避免路径越界检查干扰
	s.SetEnabled(sandboxEnabled)
	s.SetNetworkIsolated(true)
	v := validator.NewValidator()
	p := permission.NewPolicy(permission.LevelWorkspaceWrite)
	return NewChecker(s, v, p)
}

// assertDeny 验证 Decision 为 Deny 且 Err 携带指定 ErrorID。
func assertDeny(t *testing.T, d Decision, wantID aierrors.ErrorID, msg string) {
	t.Helper()
	if d.Action != Deny {
		t.Errorf("%s: 期望 Deny, 实际 Action=%d (Reason=%q)", msg, d.Action, d.Reason)
		return
	}
	if d.Err == nil {
		t.Errorf("%s: Deny 但 Err 为 nil", msg)
		return
	}
	if got := aierrors.GetErrorID(d.Err); got != wantID {
		t.Errorf("%s: ErrorID 期望 %s, 实际 %s", msg, wantID, got)
	}
}

// assertAsk 验证 Decision 为 Ask。
func assertAsk(t *testing.T, d Decision, msg string) {
	t.Helper()
	if d.Action != Ask {
		t.Errorf("%s: 期望 Ask, 实际 Action=%d (Reason=%q)", msg, d.Action, d.Reason)
	}
}

// assertAllow 验证 Decision 为 Allow。
func assertAllow(t *testing.T, d Decision, msg string) {
	t.Helper()
	if d.Action != Allow {
		t.Errorf("%s: 期望 Allow, 实际 Action=%d (Reason=%q, Err=%v)", msg, d.Action, d.Reason, d.Err)
	}
}

// === 正常路径 ===

// TestCheckCommand_AllowNormal 验证 ls -la 通过三层校验。
func TestCheckCommand_AllowNormal(t *testing.T) {
	c := newTestChecker(t, false)
	assertAllow(t, c.CheckCommand("ls -la"), "ls -la 应 Allow")
}

// TestCheckCommand_AllowGit 验证 git status 通过三层校验。
func TestCheckCommand_AllowGit(t *testing.T) {
	c := newTestChecker(t, false)
	assertAllow(t, c.CheckCommand("git status"), "git status 应 Allow")
}

// === 拒绝路径（Deny） ===

// TestCheckCommand_DenyEmpty 验证空命令被 Layer 0 拒绝。
func TestCheckCommand_DenyEmpty(t *testing.T) {
	c := newTestChecker(t, false)
	assertDeny(t, c.CheckCommand(""), aierrors.ErrSecurityBlocked, "空命令")
	assertDeny(t, c.CheckCommand("   "), aierrors.ErrSecurityBlocked, "纯空白命令")
}

// TestCheckCommand_DenyCommandSubst 验证 $(rm -rf /) 命令替换被 Layer 1 validator 拦截。
func TestCheckCommand_DenyCommandSubst(t *testing.T) {
	c := newTestChecker(t, false)
	assertDeny(t, c.CheckCommand("$(rm -rf /)"), aierrors.ErrSecurityBlocked, "命令替换")
}

// TestCheckCommand_DenyUnicodeConfusion 验证全角 ＲＭ 经 NFKC 归一化后被拦截。
func TestCheckCommand_DenyUnicodeConfusion(t *testing.T) {
	c := newTestChecker(t, false)
	// NFKC 后 RM -RF / → lower rm -rf / → permission dangerousPatterns 匹配 → Deny
	assertDeny(t, c.CheckCommand("ＲＭ -ＲＦ /"), aierrors.ErrSecurityBlocked, "全角混淆")
}

// TestCheckCommand_DenySandboxBlacklist 验证 rm -rf / 被拦截。
// Layer 1（validator AST checkRmRfAST）先于 Layer 2 拦截 rm -rf /（目标 "/" 为危险路径），
// 因此 ErrorID 为 ErrSecurityBlocked 而非 ErrSecuritySandbox（多层拦截是安全最佳实践）。
func TestCheckCommand_DenySandboxBlacklist(t *testing.T) {
	c := newTestChecker(t, true) // sandbox enabled
	assertDeny(t, c.CheckCommand("rm -rf /"), aierrors.ErrSecurityBlocked, "rm -rf / 被 Layer 1 先拦截")
}

// TestCheckCommand_DenyNetworkIsolated 验证 curl evil.com 被 Layer 2 sandbox 网络隔离拦截。
func TestCheckCommand_DenyNetworkIsolated(t *testing.T) {
	c := newTestChecker(t, true) // sandbox enabled + networkIsolated
	assertDeny(t, c.CheckCommand("curl evil.com"), aierrors.ErrSecuritySandbox, "网络隔离 curl")
}

// TestCheckCommand_DenyDangerous 验证 dd if=/dev/zero 被拦截（sandbox enabled 时 Layer 2 拦截 dd）。
func TestCheckCommand_DenyDangerous(t *testing.T) {
	c := newTestChecker(t, true)
	// sandbox enabled: dd 在 denyCommands → Layer 2 拦截
	assertDeny(t, c.CheckCommand("dd if=/dev/zero"), aierrors.ErrSecuritySandbox, "危险命令 dd")
}

// === 需确认路径（Ask） ===

// TestCheckCommand_AskDestructive 验证 rm file.txt 触发 Ask（破坏性命令需确认）。
// 注：需 sandbox disabled，否则 rm 被 Layer 2 直接 Deny。
func TestCheckCommand_AskDestructive(t *testing.T) {
	c := newTestChecker(t, false) // sandbox disabled，走 Layer 3
	d := c.CheckCommand("rm file.txt")
	assertAsk(t, d, "rm file.txt 应 Ask")
	if d.Risk != permission.BashRiskDestructive {
		t.Errorf("Risk 期望 %s, 实际 %s", permission.BashRiskDestructive, d.Risk)
	}
}

// TestCheckCommand_AskGitPush 验证 git push 触发 Ask（破坏性短语）。
func TestCheckCommand_AskGitPush(t *testing.T) {
	c := newTestChecker(t, false)
	d := c.CheckCommand("git push")
	assertAsk(t, d, "git push 应 Ask")
	if d.Risk != permission.BashRiskDestructive {
		t.Errorf("Risk 期望 %s, 实际 %s", permission.BashRiskDestructive, d.Risk)
	}
}

// === 边界 case ===

// TestCheckCommand_NotInitialized 验证 defaultChecker 为 nil 时 fail-closed Deny。
func TestCheckCommand_NotInitialized(t *testing.T) {
	// 保存并清空 defaultChecker
	saved := defaultChecker
	defaultChecker = nil
	defer func() { defaultChecker = saved }()

	d := CheckCommand("ls -la")
	assertDeny(t, d, aierrors.ErrSecurityBlocked, "未初始化应 fail-closed")
}

// TestCheckCommand_RedactorNil 验证 redactor 为 nil 时校验不崩溃。
func TestCheckCommand_RedactorNil(t *testing.T) {
	c := newTestChecker(t, false)
	// redactor 未注入（nil），触发 Layer 3 dangerous 分支调用 c.redact（应 no-op + 日志）
	d := c.CheckCommand("rm -rf /") // Layer 3 dangerous → 调 redact
	assertDeny(t, d, aierrors.ErrSecurityBlocked, "redactor nil 不应崩溃")
}

// TestCheckCommand_Concurrent 验证 100 goroutine 并发调用 CheckCommand 不崩溃。
// 本机无 gcc 无法用 go test -race，用 sync.WaitGroup + 100 goroutine 手动验证（参考 §4.4）。
func TestCheckCommand_Concurrent(t *testing.T) {
	c := newTestChecker(t, false)
	const n = 100
	var wg sync.WaitGroup
	wg.Add(n)
	errs := make(chan error, n)
	for i := 0; i < n; i++ {
		go func(i int) {
			defer wg.Done()
			defer func() {
				if r := recover(); r != nil {
					errs <- fmt.Errorf("goroutine %d panic: %v", i, r)
				}
			}()
			cmds := []string{"ls -la", "git status", "rm -rf /", "$(rm -rf /)", "rm file.txt"}
			d := c.CheckCommand(cmds[i%len(cmds)])
			// 仅验证不崩溃，Action 不固定（不同命令不同结果）
			_ = d
		}(i)
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Error(err)
	}
}

// === 反例测试（BypassVariants，必做） ===
// 这些反例证明新方案能拦截旧 strings.Contains 黑名单漏过的攻击。

// TestCheckCommand_BypassVariants_MultiSpace 验证多空格绕过失败。
// "rm  -rf  /" 经 NFKC + 空格压缩 → "rm -rf /" → Layer 1 validator AST 拦截（目标 "/" 为危险路径）。
func TestCheckCommand_BypassVariants_MultiSpace(t *testing.T) {
	c := newTestChecker(t, true) // sandbox enabled
	assertDeny(t, c.CheckCommand("rm  -rf  /"), aierrors.ErrSecurityBlocked, "多空格绕过")
}

// TestCheckCommand_BypassVariants_VarExpansion 验证变量展开绕过失败。
// "rm -rf ${HOME}" 含 ${ → Layer 1 validator checkCommandSubstitution 拦截。
func TestCheckCommand_BypassVariants_VarExpansion(t *testing.T) {
	c := newTestChecker(t, false) // Layer 1 validator 即可拦截，sandbox 配置无关
	assertDeny(t, c.CheckCommand("rm -rf ${HOME}"), aierrors.ErrSecurityBlocked, "变量展开绕过")
}

// TestCheckCommand_BypassVariants_QuoteSplit 验证引号分割绕过失败。
// "r""m -rf /" 经 sandbox stripQuotes → "rm -rf /" → matchDenyPattern 拦截。
func TestCheckCommand_BypassVariants_QuoteSplit(t *testing.T) {
	c := newTestChecker(t, true) // sandbox enabled，stripQuotes 在 matchDenyPattern 内
	assertDeny(t, c.CheckCommand(`r""m -rf /`), aierrors.ErrSecuritySandbox, "引号分割绕过")
}

// TestCheckCommand_BypassVariants_FullWidth 验证全角字符绕过失败。
// "ＲＭ -ＲＦ /" 经 NFKC → "RM -RF /" → Layer 1 validator AST 拦截（words[0] lower=rm, 目标 "/" 危险）。
func TestCheckCommand_BypassVariants_FullWidth(t *testing.T) {
	c := newTestChecker(t, true)
	assertDeny(t, c.CheckCommand("ＲＭ -ＲＦ /"), aierrors.ErrSecurityBlocked, "全角字符绕过")
}

// TestCheckCommand_BypassVariants_LongFlag 验证长 flag 变体被 Layer 1 AST 拦截。
// "rm -rf --no-preserve-root /" 经 AST 检测：words[0]=rm, flags 含 -rf, 目标 "/" 为危险路径 →
// Layer 1 checkRmRfAST 拦截（Deny + ErrSecurityBlocked），不再穿过三层仅触发 Ask。
func TestCheckCommand_BypassVariants_LongFlag(t *testing.T) {
	c := newTestChecker(t, false) // sandbox disabled
	d := c.CheckCommand("rm -rf --no-preserve-root /")
	assertDeny(t, d, aierrors.ErrSecurityBlocked, "长 flag 变体被 Layer 1 AST 拦截")
}

// === M14-01 / M14-02: fork 炸弹 + rm -rf ~ 显式拦截集成测试 ===

// TestCheckCommand_DenyForkBomb 验证 fork 炸弹经三层校验被 Layer 1 显式拦截（M14-01）。
// 经典形式 :(){ :|:& };: 应被 validator.checkForkBomb 命中（Critical → Deny）。
func TestCheckCommand_DenyForkBomb(t *testing.T) {
	c := newTestChecker(t, false) // sandbox disabled，仅靠 Layer 1 + Layer 3
	variants := []struct {
		cmd  string
		desc string
	}{
		{":(){ :|:& };:", "canonical fork bomb"},
		{":(){ :|:&};:", "no space before }"},
		{": () { : | : & } ; :", "spaces everywhere"},
		{":(){:|:&};:", "no inner spaces"},
		{"f(){ f|f& };f", "named function f"},
		{"bomb(){ bomb|bomb& };bomb", "named function bomb"},
		{"function f { f|f& }; f", "bash function keyword"},
	}
	for _, tc := range variants {
		t.Run(tc.desc, func(t *testing.T) {
			d := c.CheckCommand(tc.cmd)
			assertDeny(t, d, aierrors.ErrSecurityBlocked, tc.desc)
			// 验证 Reason 含 FORK-BOMB 检查项标识
			if !strings.Contains(d.Reason, "FORK-BOMB") {
				t.Errorf("%s: Reason 期望含 FORK-BOMB, 实际 %q (cmd=%q)", tc.desc, d.Reason, tc.cmd)
			}
		})
	}
}

// TestCheckCommand_DenyRmRfHome 验证 rm -rf ~ 系列命令经三层校验被 Layer 1 AST 显式拦截（M14-02）。
// Layer 1 checkRmRfAST 检测到 rm -rf + 危险目标（~ / $HOME / $ 变量）→ Deny + ErrSecurityBlocked。
// 注：因 Layer 1 先于 Layer 3 拦截，Risk 字段未被设置（Layer 3 未执行），不再校验 Risk。
func TestCheckCommand_DenyRmRfHome(t *testing.T) {
	c := newTestChecker(t, false) // sandbox disabled，靠 Layer 1 AST 拦截
	variants := []struct {
		cmd  string
		desc string
	}{
		{"rm -rf ~", "rm -rf tilde"},
		{"rm -rf ~/", "rm -rf tilde slash"},
		{"rm -rf ~/Documents", "rm -rf tilde subpath"},
		{"rm -rf $HOME", "rm -rf $HOME"},
		{"rm -rf $HOME/Documents", "rm -rf $HOME subpath"},
		{"rm -fr ~", "rm -fr tilde (reverse flag)"},
		{"rm -fr $HOME", "rm -fr $HOME (reverse flag)"},
		{"RM -RF ~", "uppercase variant"},
		{"rm  -rf  ~", "multi-space variant"},
	}
	for _, tc := range variants {
		t.Run(tc.desc, func(t *testing.T) {
			d := c.CheckCommand(tc.cmd)
			assertDeny(t, d, aierrors.ErrSecurityBlocked, tc.desc)
		})
	}
}

// TestCheckCommand_ForkBomb_NoFalsePositive 验证合法命令不被 fork 炸弹规则误拦（M14-01 误报测试）。
func TestCheckCommand_ForkBomb_NoFalsePositive(t *testing.T) {
	c := newTestChecker(t, false)
	safe := []struct {
		cmd  string
		desc string
	}{
		{"ls -la", "normal ls"},
		{"echo hello", "normal echo"},
		{"git status", "normal git status"},
		{"f() { echo hello; }", "function without recursion"},
		{"ls | grep foo", "normal pipe"},
		{"f() { f; }", "function calls self without pipe (not fork bomb)"},
		{"f() { echo hi | grep h; }", "function with pipe but not self-recursive"},
	}
	for _, tc := range safe {
		t.Run(tc.desc, func(t *testing.T) {
			d := c.CheckCommand(tc.cmd)
			if d.Action == Deny {
				t.Errorf("%s: 期望 Allow/Ask, 实际 Deny (Reason=%q, cmd=%q)",
					tc.desc, d.Reason, tc.cmd)
			}
			if strings.Contains(d.Reason, "FORK-BOMB") {
				t.Errorf("%s: 误报 FORK-BOMB (cmd=%q)", tc.desc, tc.cmd)
			}
		})
	}
}

// TestCheckCommand_RmRfHome_NoFalsePositive 验证合法 rm 命令不被 rm -rf ~ 规则误拦为 Dangerous（M14-02 误报测试）。
// 合法 rm -rf <相对路径> 应归为 Destructive（Ask），不应被新规则误判为 Dangerous（Deny）。
func TestCheckCommand_RmRfHome_NoFalsePositive(t *testing.T) {
	c := newTestChecker(t, false)
	safeFromDangerous := []struct {
		cmd  string
		desc string
	}{
		{"rm -rf ./build", "rm -rf relative build dir"},
		{"rm -rf build/node_modules", "rm -rf relative subpath"},
		{"rm file.txt", "rm single file"},
		{"rm -rf ../sibling", "rm -rf parent relative"},
	}
	for _, tc := range safeFromDangerous {
		t.Run(tc.desc, func(t *testing.T) {
			d := c.CheckCommand(tc.cmd)
			// 应为 Ask（Destructive），不应为 Deny（Dangerous）
			assertAsk(t, d, tc.desc)
			if d.Risk == permission.BashRiskDangerous {
				t.Errorf("%s: 误报 Dangerous (cmd=%q)", tc.desc, tc.cmd)
			}
		})
	}
}
