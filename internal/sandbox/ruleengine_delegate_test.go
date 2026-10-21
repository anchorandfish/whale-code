package sandbox

// 残留修复（2026-10-08，I-13.10 真机复跑未过）—— 判据。
//
// 缺陷： 的递归校验只落在 validator 层；管线生产先跑的沙箱预检
// （SandboxPrecheckStage ⇒ Sandbox.Classify ⇒ evaluateAppLayer）仍是
// "见 $($ 就拦"的纯文本扫描 ⇒ 同一规则两处实现，判据绿但真机照弹框。
//
// 修法（第一性原理：事实只许问一次）：ruleengine 的 SUBSTITUTION 分支降级为
// **委托** validator.AssessSubstitution（唯一实现）：
//
//	内层良性 ⇒ 放行（不再问用户）
//	内层危险（Critical）⇒ TierHardDeny（按"直接执行"同一标准，且不可批准）
//	内层危险（非 Critical）⇒ TierConfirm（可批准）
//	不配对 / 超深 ⇒ TierHardDeny（fail-closed）
//	非执行型展开 ${ $[ $(( ⇒ 刻意不动，保持 TierConfirm（本文件钉死）
//
// 生产入口（R1）：Sandbox.Classify —— 与 SandboxPrecheckStage:1131 同一调用。
// A1/A4 在改前基线上必须**红**（tier 断言失败），否则不具判别性；
// A2/A3/A5/A6 是回归守卫（改前后都绿）。

import (
	"testing"
)

func newW021Sandbox(t *testing.T) *Sandbox {
	t.Helper()
	s := NewSandbox(t.TempDir())
	s.SetEnabled(true)
	return s
}

// TestW021A_BenignSubstitutionPasses 主判据：无害替换形态放行，不再弹框。
//
// 用户真机用例（I-13.10）：echo "now $(date)" 必须直接过预检。
func TestW021A_BenignSubstitutionPasses(t *testing.T) {
	s := newW021Sandbox(t)
	tier, rule, hit := s.Classify(`echo "now $(date)"`)
	if tier != TierAllow {
		t.Fatalf("无害命令替换应放行，实际 tier=%v rule=%s hit=%q（ 残留：预检仍在弹框）",
			tier, rule, hit)
	}
}

// TestW021A_DangerousInnerRootDelete_HardDeny 内层真危险 ⇒ 硬拒（不可批准）。
//
// 探针实测：validator 的 AST 检查对 `rm -rf /`（根目标）给
// Critical finding；委托后必须上浮为硬拒，而不是保留"弹框可批准"。
func TestW021A_DangerousInnerRootDelete_HardDeny(t *testing.T) {
	s := newW021Sandbox(t)
	tier, rule, hit := s.Classify(`echo $(rm -rf /)`)
	if tier != TierHardDeny {
		t.Fatalf("内层 rm -rf / 应硬拒（按直接执行同一标准），实际 tier=%v rule=%s hit=%q",
			tier, rule, hit)
	}
}

// TestW021A_NestedDangerousInner_HardDeny 嵌套危险逐层下探后仍硬拒。
func TestW021A_NestedDangerousInner_HardDeny(t *testing.T) {
	s := newW021Sandbox(t)
	tier, _, _ := s.Classify(`echo $(echo $(rm -rf /))`)
	if tier != TierHardDeny {
		t.Fatalf("嵌套内层 rm -rf / 应硬拒，实际 tier=%v", tier)
	}
}

// TestW021A_BacktickDangerousInner_Blocked 反引号形态同判据（同一实现）。
func TestW021A_BacktickDangerousInner_Blocked(t *testing.T) {
	s := newW021Sandbox(t)
	tier, _, _ := s.Classify("echo `rm -rf /`")
	if tier == TierAllow {
		t.Fatalf("反引号包裹的危险内层不得放行")
	}
}

// TestW021A_UnbalancedSubstitution_FailClosed 解析不出 ⇒ 硬拒（fail-closed）。
//
// 拍板第三句：把"解析失败"当成"没问题"是 同族的错法。
// 改前该形态落 TierConfirm（弹框可批准）；委托后必须 fail-closed 硬拒。
func TestW021A_UnbalancedSubstitution_FailClosed(t *testing.T) {
	s := newW021Sandbox(t)
	for _, cmd := range []string{`echo $(unclosed`, "echo `unclosed"} {
		tier, rule, _ := s.Classify(cmd)
		if tier != TierHardDeny {
			t.Fatalf("不配对替换应 fail-closed 硬拒，实际 tier=%v rule=%s cmd=%q", tier, rule, cmd)
		}
	}
}

// TestW021A_NonExecExpansions_Unchanged 非执行型展开： 刻意不动的一半。
//
// ${…} 参数展开 / $[…] 旧式算术 / $((…)) 算术 —— 保守照拦（TierConfirm），
// 本判据钉死"委托不越界"：改前后都应绿。
func TestW021A_NonExecExpansions_Unchanged(t *testing.T) {
	s := newW021Sandbox(t)
	for _, cmd := range []string{`echo ${HOME}`, `echo $((1+1))`, `echo $[1+1]`} {
		tier, rule, _ := s.Classify(cmd)
		if tier != TierConfirm {
			t.Fatalf("非执行型展开应保持可批准档（ 刻意不动），实际 tier=%v rule=%s cmd=%q",
				tier, rule, cmd)
		}
	}
}

// TestW021A_DenyWinsOverSubstitution deny-wins 不因委托而松动。
//
// 含 dd（硬拒名单； 后 rm 已降可批准档，硬拒样本换为 dd）的混合命令：
// deny 全量扫描在替换判定**之前**，必须照旧硬拒。
func TestW021A_DenyWinsOverSubstitution(t *testing.T) {
	s := newW021Sandbox(t)
	tier, _, _ := s.Classify(`dd if=/dev/zero of=/tmp/whale-w021-junk bs=1 count=1 && echo $(date)`)
	if tier != TierHardDeny {
		t.Fatalf("deny-wins：混合命令应硬拒，实际 tier=%v", tier)
	}
}
