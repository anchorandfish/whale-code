// Package validator ast_checks_test.go AST 检查单元测试（T1-P1-A4 /）。
package validator

import (
	"testing"
)

// TestASTChecks_ForkBombClassic 验证经典 :() 形式 fork 炸弹的 AST 检测。
func TestASTChecks_ForkBombClassic(t *testing.T) {
	checker := NewASTChecker()
	results := checker.Run(":(){ :|:& };:")
	if len(results) == 0 {
		t.Fatal("expected at least 1 violation for classic fork bomb")
	}
	found := false
	for _, r := range results {
		if r.CheckID == "FORK-BOMB-AST" {
			found = true
			if r.Severity != SeverityCritical {
				t.Errorf("expected Critical severity, got %d", r.Severity)
			}
			break
		}
	}
	if !found {
		t.Errorf("expected FORK-BOMB-AST checkID, got: %+v", results)
	}
}

// TestASTChecks_ForkBombNamed 验证命名函数形式 fork 炸弹的 AST 检测。
func TestASTChecks_ForkBombNamed(t *testing.T) {
	checker := NewASTChecker()
	results := checker.Run("bomb(){ bomb|bomb& }; bomb")
	found := false
	for _, r := range results {
		if r.CheckID == "FORK-BOMB-AST" {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("expected FORK-BOMB-AST for named fork bomb, got: %+v", results)
	}
}

// TestASTChecks_RmRfRoot 验证 rm -rf / 的 AST 检测。
func TestASTChecks_RmRfRoot(t *testing.T) {
	checker := NewASTChecker()
	results := checker.Run("rm -rf /")
	found := false
	for _, r := range results {
		if r.CheckID == "RM-RF-AST" {
			found = true
			if r.Severity != SeverityCritical {
				t.Errorf("expected Critical severity, got %d", r.Severity)
			}
			break
		}
	}
	if !found {
		t.Errorf("expected RM-RF-AST for rm -rf /, got: %+v", results)
	}
}

// TestASTChecks_RmRfTilde 验证 rm -rf ~ 的 AST 检测。
func TestASTChecks_RmRfTilde(t *testing.T) {
	checker := NewASTChecker()
	results := checker.Run("rm -rf ~")
	found := false
	for _, r := range results {
		if r.CheckID == "RM-RF-AST" {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("expected RM-RF-AST for rm -rf ~, got: %+v", results)
	}
}

// TestASTChecks_RmFrRoot 验证 rm -fr / 顺序变体的 AST 检测。
func TestASTChecks_RmFrRoot(t *testing.T) {
	checker := NewASTChecker()
	results := checker.Run("rm -fr /")
	found := false
	for _, r := range results {
		if r.CheckID == "RM-RF-AST" {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("expected RM-RF-AST for rm -fr /, got: %+v", results)
	}
}

// TestASTChecks_RmRfVariable 验证 rm -rf $VAR 的 AST 检测。
func TestASTChecks_RmRfVariable(t *testing.T) {
	checker := NewASTChecker()
	results := checker.Run("rm -rf $TARGET")
	found := false
	for _, r := range results {
		if r.CheckID == "RM-RF-AST" {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("expected RM-RF-AST for rm -rf $TARGET, got: %+v", results)
	}
}

// TestASTChecks_RmRfHome 验证 rm -rf $HOME 的 AST 检测。
func TestASTChecks_RmRfHome(t *testing.T) {
	checker := NewASTChecker()
	results := checker.Run("rm -rf $HOME")
	found := false
	for _, r := range results {
		if r.CheckID == "RM-RF-AST" {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("expected RM-RF-AST for rm -rf $HOME, got: %+v", results)
	}
}

// TestASTChecks_SubstitutionFamilyNotDuplicated 是 的**归属守卫**。
//
// 背景：原 `TestASTChecks_CmdSubst` / `TestASTChecks_ProcSubst` 断言的是
// 「`echo $(whoami)` / `cat <(cat /etc/shadow)` 会被 AST 层以 `CMD-SUBST-AST` 命中」。
// 把**执行型替换族**收口到字符串层单点（`CMD-SUBST`）后，
// 这两条断言的**对象已不存在**，其覆盖面由 `subst_inner_test.go` 以更强的形式接走：
//
//	旧：echo $(whoami) → 命中（"有替换就拦"）
//	旧：cat <(cat /etc/shadow) → 命中（"有进程替换就拦"）
//	新：echo $(date) → 放行（内层良性） ← 本体
//	新：cat <(rm -rf /) → 拒 （内层危险） ← 同一覆盖面的正确形态
//
// 本用例存在的意义：**防回潮**。若有人把 `CMD-SUBST-AST` 加回 AST 套件，
// 那会重新制造"同一语义两处各写一份实现"——本仓已经栽过（的 3 处分歧）。
func TestASTChecks_SubstitutionFamilyNotDuplicated(t *testing.T) {
	for _, cmd := range []string{"echo $(whoami)", "cat <(cat /etc/shadow)"} {
		for _, r := range NewASTChecker().Run(cmd) {
			if r.CheckID == "CMD-SUBST-AST" {
				t.Errorf("AST 层不得再自带替换族判定（ 已收口到 CMD-SUBST 单点）：%q", cmd)
			}
		}
	}
}

// TestASTChecks_VarIndirect 验证变量间接危险调用 x=rm; $x 的 AST 检测。
func TestASTChecks_VarIndirect(t *testing.T) {
	checker := NewASTChecker()
	results := checker.Run("x=rm; $x -rf /")
	found := false
	for _, r := range results {
		if r.CheckID == "VAR-INDIRECT-AST" {
			found = true
			if r.Severity != SeverityHigh {
				t.Errorf("expected High severity, got %d", r.Severity)
			}
			break
		}
	}
	if !found {
		t.Errorf("expected VAR-INDIRECT-AST for x=rm; $x -rf /, got: %+v", results)
	}
}

// TestASTChecks_DangerousRedirect 验证危险重定向的 AST 检测。
func TestASTChecks_DangerousRedirect(t *testing.T) {
	checker := NewASTChecker()
	results := checker.Run("echo x > /etc/passwd")
	found := false
	for _, r := range results {
		if r.CheckID == "DANG-REDIR-AST" {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("expected DANG-REDIR-AST for echo x > /etc/passwd, got: %+v", results)
	}
}

// TestASTChecks_SafeCommand 验证安全命令不触发 AST 检查。
func TestASTChecks_SafeCommand(t *testing.T) {
	checker := NewASTChecker()
	results := checker.Run("ls -la")
	if len(results) != 0 {
		t.Errorf("expected no violations for ls -la, got: %+v", results)
	}
}

// TestASTChecks_SafeEcho 验证安全 echo 命令不触发 AST 检查。
func TestASTChecks_SafeEcho(t *testing.T) {
	checker := NewASTChecker()
	results := checker.Run("echo hello")
	if len(results) != 0 {
		t.Errorf("expected no violations for echo hello, got: %+v", results)
	}
}

// TestASTChecks_RmWithoutForce 验证 rm 不带 -rf 不触发 RM-RF-AST。
func TestASTChecks_RmWithoutForce(t *testing.T) {
	checker := NewASTChecker()
	results := checker.Run("rm /tmp/safe-file")
	found := false
	for _, r := range results {
		if r.CheckID == "RM-RF-AST" {
			found = true
			break
		}
	}
	if found {
		t.Errorf("expected no RM-RF-AST for rm /tmp/safe-file, got: %+v", results)
	}
}

// TestASTChecks_RmRfSafeTarget 验证 rm -rf 安全目标不触发 RM-RF-AST。
func TestASTChecks_RmRfSafeTarget(t *testing.T) {
	checker := NewASTChecker()
	// rm -rf /tmp/safe-dir：目标 /tmp/safe-dir 不是危险路径
	results := checker.Run("rm -rf /tmp/safe-dir")
	found := false
	for _, r := range results {
		if r.CheckID == "RM-RF-AST" {
			found = true
			break
		}
	}
	if found {
		t.Errorf("expected no RM-RF-AST for rm -rf /tmp/safe-dir, got: %+v", results)
	}
}

// TestASTChecks_PanicRecovery 验证 panic 降级机制。
func TestASTChecks_PanicRecovery(t *testing.T) {
	checker := &ASTChecker{
		checks: []ASTCheckFunc{
			func(root *CommandNode) *SecurityResult {
				panic("test panic")
			},
		},
	}
	// 应该 recover 并返回 nil（降级）
	results := checker.Run("ls")
	if results != nil {
		t.Errorf("expected nil on panic, got: %+v", results)
	}
}

// TestASTChecks_EmptyInput 验证空输入不触发检查。
func TestASTChecks_EmptyInput(t *testing.T) {
	checker := NewASTChecker()
	results := checker.Run("")
	if len(results) != 0 {
		t.Errorf("expected no violations for empty input, got: %+v", results)
	}
}

// TestDefaultASTChecks 验证默认 AST 检查套件含 4 项（起：原 5 项减去已迁出的替换族）。
func TestDefaultASTChecks(t *testing.T) {
	checks := DefaultASTChecks()
	if len(checks) != 4 {
		t.Errorf("expected 4 default AST checks, got %d", len(checks))
	}
}
