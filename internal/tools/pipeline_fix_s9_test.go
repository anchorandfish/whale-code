package tools

import (
	"context"
	"testing"

	"github.com/anchorandfish/whale-code/internal/permission"
)

// S9 Fix 生产路径测试（HG-A2）：只读命令按作用域去重。
//
// 与 internal/permission 的单测互补：单测证明键归一化本身；本用例钉住
// **读取原语**（`Policy.Has` —— 管线在权限层读取记忆时调用的正是它，
// 其键为 `fingerprint → approvalKeyFor → commandScopeKey`）确实吃到了归一化后的键。
//
// ★ 2026-10-06 交互（如实登记，勿当噪音）：只读命令现在由**只读免问出口 (e)**
// （`cmdrules.AllSegmentsReadOnly`）直接放行 —— 连问都不问 —— 于是原先那条
// 「无记忆时仍需确认」的**基线前提不再成立**，靠它做的"归一化键在生产读取点生效"的
// **端到端**断言失去判别力（实测：本用例原样直接变红）。
// ⇒ 改为三层：
//
//	① 读取原语：语义相同、文本不同的只读命令必须命中同一条记忆（归一化键生效）；
//	② 反向：只读作用域授权**不得**外溢到非只读命令（fail-closed）；
//	③ 端到端：只读 ⇒ 免问（出口 (e)）；**写重定向 ⇒ 仍须确认**（豁免不得覆盖写面）。
func TestFix_S9_PipelineDedupesReadOnlyBashByScope(t *testing.T) {
	tmp := t.TempDir()
	r := NewRegistry()
	e := NewExecutor(r, tmp)

	policy := permission.NewPolicy(permission.LevelWorkspaceWrite)
	e.InitPipelineWithHooks(policy, tmp)
	if e.Pipeline == nil {
		t.Fatal("precondition: 生产装配下 Pipeline 必须非 nil")
	}

	// 记入「本会话」授权（等同 TUI 选 [A] Allow for this session）
	policy.Remember("bash", `{"command":"ls -la"}`, true)

	// ① 读取原语（管线读取点读的就是它）：语义相同、文本不同 ⇒ 必须命中同一条记忆
	if !policy.Has("bash", `{"command":"ls -la /tmp"}`) {
		t.Fatalf("HG-A2: 归一化作用域键未在读取原语上生效（`ls -la` 授权后 `ls -la /tmp` 应命中）")
	}
	// ② 反向：只读授权不得外溢到非只读命令
	if policy.Has("bash", `{"command":"mkdir -p /tmp/s9fix_should_not_exist"}`) {
		t.Fatalf("fail-closed: 只读作用域授权外溢到了非只读命令")
	}

	// ③-a 端到端：只读命令免问（出口 (e)；与是否有记忆无关）
	after := e.ExecuteWithConfirm(context.Background(), Call{ID: "b2", Name: "bash", Input: `{"command":"ls -la /tmp"}`})
	if after.NeedConfirm {
		t.Fatalf(": 只读命令应免确认; stageLogs=%+v", after.StageLogs)
	}

	// ③-b 端到端 fail-closed：写重定向仍必须确认（豁免不得覆盖写面）
	danger := e.ExecuteWithConfirm(context.Background(), Call{ID: "b3", Name: "bash", Input: `{"command":"echo s9 > /tmp/s9fix_should_not_exist"}`})
	if !danger.NeedConfirm {
		t.Fatalf("fail-closed: 写重定向不得被只读豁免/只读授权覆盖; stageLogs=%+v", danger.StageLogs)
	}
}
