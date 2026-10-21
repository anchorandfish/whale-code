// Package validator validator_ast_test.go Validator AST 集成测试（T1-P1-A4 /）。
package validator

import (
	"testing"
)

// TestValidator_ASTIntegrated 验证 NewValidator 默认启用 AST 检查。
func TestValidator_ASTIntegrated(t *testing.T) {
	v := NewValidator()
	if v.astChecker == nil {
		t.Fatal("expected astChecker non-nil by default")
	}
}

// TestValidator_WithoutAST 验证 NewValidatorWithoutAST 不启用 AST 检查。
func TestValidator_WithoutAST(t *testing.T) {
	v := NewValidatorWithoutAST()
	if v.astChecker != nil {
		t.Fatal("expected astChecker nil for NewValidatorWithoutAST")
	}
}

// TestValidator_ForkBombDoubleHit 验证 fork 炸弹被 AST 和字符串匹配双重命中。
//
// 吸纳优化 4：双重命中时两条记录都保留，严重度取较高值。
func TestValidator_ForkBombDoubleHit(t *testing.T) {
	v := NewValidator()
	results := v.Run(":(){ :|:& };:")
	if len(results) == 0 {
		t.Fatal("expected at least 1 violation for fork bomb")
	}
	hasAST := false
	hasString := false
	for _, r := range results {
		if r.CheckID == "FORK-BOMB-AST" {
			hasAST = true
		}
		if r.CheckID == "FORK-BOMB" {
			hasString = true
		}
	}
	if !hasAST {
		t.Error("expected FORK-BOMB-AST (AST check)")
	}
	if !hasString {
		t.Error("expected FORK-BOMB (string check)")
	}
	// 至少应该有阻断
	if !HasBlocking(results) {
		t.Error("expected HasBlocking=true for fork bomb")
	}
}

// TestValidator_RmRfAST 验证 rm -rf / 通过 AST 检出。
func TestValidator_RmRfAST(t *testing.T) {
	v := NewValidator()
	results := v.Run("rm -rf /")
	if len(results) == 0 {
		t.Fatal("expected violations for rm -rf /")
	}
	found := false
	for _, r := range results {
		if r.CheckID == "RM-RF-AST" {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("expected RM-RF-AST in results: %+v", results)
	}
}

// TestValidator_CmdSubstInnerVerdict 验证替换族在**生产入口**的判据。
//
// 改前本用例叫 `TestValidator_CmdSubstDoubleHit`：断言 `echo $(whoami)` 被 AST 层与
// 字符串层**双重命中**（两个 CheckID）。 把本族收口到单点（`CMD-SUBST`），
// 并改判据 —— 判的**不是"有没有替换"**，而是"**替换体里那条命令**危险不危险"。
// ⇒ 用例改名 + 改断言：同一条命令，内层良莠决定结论。
func TestValidator_CmdSubstInnerVerdict(t *testing.T) {
	v := NewValidator()

	// 内层良性 ⇒ 替换族不得给出任何结论
	for _, cmd := range []string{"echo $(whoami)", "cat <(echo hi)"} {
		for _, r := range v.Run(cmd) {
			if r.CheckID == "CMD-SUBST" || r.CheckID == "CMD-SUBST-AST" {
				t.Errorf("内层良性不得命中替换族：%q ⇒ %s", cmd, r.Message)
			}
		}
	}

	// 内层危险 ⇒ 由 CMD-SUBST 拦下（且不再有第二个同义 CheckID）
	hit := false
	for _, r := range v.Run("echo $(rm -rf /)") {
		if r.CheckID == "CMD-SUBST" {
			hit = true
		}
		if r.CheckID == "CMD-SUBST-AST" {
			t.Errorf("替换族应只有一个 owner，出现重复 CheckID：%s", r.CheckID)
		}
	}
	if !hit {
		t.Error("内层危险必须由 CMD-SUBST 拦下")
	}
}

// TestValidator_WithoutASTOnlyString 验证 NewValidatorWithoutAST 仅字符串匹配。
func TestValidator_WithoutASTOnlyString(t *testing.T) {
	v := NewValidatorWithoutAST()
	results := v.Run(":(){ :|:& };:")
	if len(results) == 0 {
		t.Fatal("expected string-match violations for fork bomb")
	}
	// 不应有 AST 检查结果
	for _, r := range results {
		if r.CheckID == "FORK-BOMB-AST" {
			t.Errorf("expected no AST check in NewValidatorWithoutAST, got %s", r.CheckID)
		}
	}
	// 应有字符串匹配结果
	hasString := false
	for _, r := range results {
		if r.CheckID == "FORK-BOMB" {
			hasString = true
			break
		}
	}
	if !hasString {
		t.Error("expected FORK-BOMB string match")
	}
}

// TestValidator_SafeCommandNoViolations 验证安全命令无违规。
func TestValidator_SafeCommandNoViolations(t *testing.T) {
	v := NewValidator()
	results := v.Run("ls -la")
	if len(results) != 0 {
		t.Errorf("expected no violations for ls -la, got: %+v", results)
	}
}

// TestValidator_DangerousRedirectDoubleHit 验证危险重定向双命中。
func TestValidator_DangerousRedirectDoubleHit(t *testing.T) {
	v := NewValidator()
	results := v.Run("echo x > /etc/passwd")
	hasAST := false
	hasString := false
	for _, r := range results {
		if r.CheckID == "DANG-REDIR-AST" {
			hasAST = true
		}
		if r.CheckID == "DANG-REDIR" {
			hasString = true
		}
	}
	if !hasAST {
		t.Error("expected DANG-REDIR-AST (AST check)")
	}
	if !hasString {
		t.Error("expected DANG-REDIR (string check)")
	}
}

// TestValidator_VarIndirectASTOnly 验证变量间接仅 AST 命中（字符串匹配无此检查）。
func TestValidator_VarIndirectASTOnly(t *testing.T) {
	v := NewValidator()
	results := v.Run("x=rm; $x -rf /")
	found := false
	for _, r := range results {
		if r.CheckID == "VAR-INDIRECT-AST" {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("expected VAR-INDIRECT-AST in results: %+v", results)
	}
}

// TestRunSecurityChecks_DefaultEnabledAST 验证 RunSecurityChecks 包级函数默认启用 AST。
func TestRunSecurityChecks_DefaultEnabledAST(t *testing.T) {
	results := RunSecurityChecks("rm -rf /")
	if len(results) == 0 {
		t.Fatal("expected violations for rm -rf /")
	}
	found := false
	for _, r := range results {
		if r.CheckID == "RM-RF-AST" {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("expected RM-RF-AST in RunSecurityChecks results: %+v", results)
	}
}

// TestValidator_BackwardCompat 验证向后兼容：原 21 项字符串匹配不受影响。
func TestValidator_BackwardCompat(t *testing.T) {
	v := NewValidator()
	// 控制字符应被字符串匹配检出
	results := v.Run("echo\x00bad")
	found := false
	for _, r := range results {
		if r.CheckID == "CTRL-CHAR" {
			found = true
			break
		}
	}
	if !found {
		t.Error("expected CTRL-CHAR from string match (backward compat)")
	}
}

// TestValidator_IFSInjection 验证 IFS 注入仍被字符串匹配检出。
func TestValidator_IFSInjection(t *testing.T) {
	v := NewValidator()
	results := v.Run("IFS=' '")
	found := false
	for _, r := range results {
		if r.CheckID == "IFS-INJECT" {
			found = true
			break
		}
	}
	if !found {
		t.Error("expected IFS-INJECT from string match")
	}
}

// TestDevNullRedirectNotDangerous 回归：丢弃 stderr 的标准写法 >/dev/null 不应被
// DANG-REDIR / DANG-REDIR-AST 误伤（字符/虚拟设备 sink，非磁盘块设备写）。
// 由生产真实入口 NewValidator().Run 进入。
func TestDevNullRedirectNotDangerous(t *testing.T) {
	v := NewValidator()
	results := v.Run("ls -la README.md 2>/dev/null || echo not found")
	for _, r := range results {
		if r.CheckID == "DANG-REDIR" || r.CheckID == "DANG-REDIR-AST" {
			t.Errorf("2>/dev/null should NOT be flagged as dangerous redirect, got %s: %s", r.CheckID, r.Message)
		}
	}
}

// TestDevBlockDeviceRedirectStillDangerous 守卫：写块设备 /dev/sda（直接写盘）仍必须拦。
func TestDevBlockDeviceRedirectStillDangerous(t *testing.T) {
	v := NewValidator()
	results := v.Run("echo x > /dev/sda")
	found := false
	for _, r := range results {
		if r.CheckID == "DANG-REDIR" || r.CheckID == "DANG-REDIR-AST" {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("expected dangerous redirect for > /dev/sda, got: %+v", results)
	}
}
