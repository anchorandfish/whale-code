package validator

import (
	"strings"
	"testing"
)

// === 判别器：空/无法解析的写重定向目标，拒绝理由不得谎称「敏感路径」 ===
//
// 背景（2026-10-11 16:11 真机实录，P5 手测）：
// `sh -c 'sleep 3; echo R-<CODE>'`（测试暗号 `<CODE>` 未替换）⇒ 载荷里悬空的
// `>` 被 AST tokenizer 当写重定向算子、后面没有目标词 ⇒
// isDangerousWriteTarget("") 保守拒绝（security_checks.go:458-459 空值兜底），
// 但理由串打印成 "AST dangerous redirect to sensitive path: >"
// （ast_checks.go:422 格式 `Op Target`，Target 为空只剩 Op）。
//
// 危害：模型读到「敏感路径」会朝"换写法避开敏感路径"方向自我纠正，
// 而真实病因是命令本身写残了（真 shell 对悬空 `>` 同样报语法错）⇒ 永远改不对。
//
// 修法：空目标单独成句。isDangerousWriteTarget 的**返回值不动**
// （S2 单一权威判定； 的 fail-closed 边界原样保留，
// 见 dangerous_redirect_fddup_test.go 的 TestW016_EmptyTargetStillFailClosed）。
//
// 全部断言从生产真实入口 NewValidator().Run() 进入（不构造 AST、不绕守卫）。

// w071DeniedMessages 返回一次 Run 命中的全部拒绝理由（仅 Passed=false 的项）。
func w071DeniedMessages(cmd string) string {
	var msgs []string
	for _, r := range NewValidator().Run(cmd) {
		if !r.Passed {
			msgs = append(msgs, r.Message)
		}
	}
	return strings.Join(msgs, " || ")
}

// TestW071_SubstEmptyTargetMessageNotSensitivePath：替换载荷里的空目标
// （真机原始实例）理由必须说「无法解析」，不得冒充「敏感路径」。修复前此用例红。
func TestW071_SubstEmptyTargetMessageNotSensitivePath(t *testing.T) {
	msgs := w071DeniedMessages("sh -c 'sleep 3; echo R-<CODE>'")
	// 正向留痕（R3-③）：必须真的走到"替换载荷递归校验"，否则断言不可信。
	if !strings.Contains(msgs, "substitution payload") {
		t.Fatalf("未观察到替换载荷校验留痕 ⇒ 可能提前拒绝/路径不对，全部理由: %s", msgs)
	}
	if strings.Contains(msgs, "sensitive path") {
		t.Errorf("空目标拒绝理由冒充敏感路径（ 未修复），理由: %s", msgs)
	}
	if !strings.Contains(msgs, "unparseable") {
		t.Errorf("空目标拒绝理由应含 unparseable（ 新文案），理由: %s", msgs)
	}
}

// TestW071_TopLevelEmptyTargetMessage：非替换形态的悬空 `>`（echo x >）
// 走同一个检查函数的另一入口，文案同理。修复前此用例红。
func TestW071_TopLevelEmptyTargetMessage(t *testing.T) {
	msgs := w071DeniedMessages("echo x >")
	if strings.Contains(msgs, "sensitive path") {
		t.Errorf("空目标拒绝理由冒充敏感路径（ 未修复），理由: %s", msgs)
	}
	if !strings.Contains(msgs, "unparseable") {
		t.Errorf("空目标拒绝理由应含 unparseable（ 新文案），理由: %s", msgs)
	}
}

// TestW071_RealSensitivePathMessageUnchanged：反向对照 —— 真写敏感路径的理由
// **必须**仍报 sensitive path（不得把真敏感路径的文案一并改掉，安全语义不动）。
func TestW071_RealSensitivePathMessageUnchanged(t *testing.T) {
	msgs := w071DeniedMessages("echo x > /etc/passwd")
	if !strings.Contains(msgs, "sensitive path") {
		t.Errorf("真写敏感路径的理由应仍含 sensitive path（反向对照被改坏！），理由: %s", msgs)
	}
}
