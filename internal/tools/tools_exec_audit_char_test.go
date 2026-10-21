package tools

// =============================================================================
// S3 工具执行 Tools（bash/file/edit）审计 — 表征测试（characterization）
//
// 日期：2026-09-09 域：S3（AUDIT_ROADMAP.md §2）
// 不变量焦点：I1（单一强制点） / I2（默认拒绝） / I3（边界输入校验） / I5（并发安全）
//
// 本文件目的：在「不动任何源码」的前提下，钉死 whale-code 工具执行链路的
// *当前* 真实行为。所有测试必须从「生产真正执行的入口」进入：
// - 带管线入口：Executor.InitPipelineWithHooks + ExecuteWithConfirm
// - 直连入口： Executor.Execute（ExecuteBatch 的 safe 路径与 Pipeline==nil 回退都走它）
// 规则（production-path-tdd）：这些测试当前必须全绿 = 已钉住当前行为；
// 后续 Fix 落地后，其中表征「缺陷」的用例会被刻意翻转并补足新断言。
//
// 【Fix 翻转版】T2/T4 已从「钉缺陷」翻转为「钉修复后行为」：
// - T2（F1）：管线初始化后，直连 Execute 也必须被 deny 规则拒绝、零副作用。
// - T4（F2）：无沙箱默认配置下，写工作区外绝对路径必须被拒绝；工作区内相对路径仍可写。
// 翻转后的红测先跑红（当前源码未改），修复后转绿 —— 证明「生产真正执行的那条路径」行为已变。
// =============================================================================

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/anchorandfish/whale-code/internal/permission"
)

// T1 — I2 默认拒绝（生产入口）：deny 规则命中时，bash 被拒绝执行且零副作用。
// 这是「fail-closed」在工具执行链路的钉死点：拒绝 ≠ 询问 ≠ 放行。
func TestChar_I2_BashDenyRuleBlocksExecution_NoSideEffect(t *testing.T) {
	tmp := t.TempDir()
	r := NewRegistry()
	e := NewExecutor(r, tmp)

	sentinel := filepath.Join(tmp, "SHOULD_NOT_EXIST")
	e.RegisterHandler("bash", func(ctx context.Context, input json.RawMessage) (string, error) {
		_ = os.WriteFile(sentinel, []byte("ran"), 0644)
		return "ran", nil
	})

	policy := permission.NewPolicy(permission.LevelWorkspaceWrite)
	rule, err := permission.ParseRuleString("bash(rm:*)", permission.RuleDeny, permission.SourceSession)
	if err != nil {
		t.Fatalf("ParseRuleString: %v", err)
	}
	if err := policy.AddRule(*rule); err != nil {
		t.Fatalf("AddRule: %v", err)
	}

	// 生产初始化方式（TUI/runner 真实路径）
	e.InitPipelineWithHooks(policy, tmp)
	if e.Pipeline == nil {
		t.Fatal("Pipeline 未初始化：本用例前提失效，无法代表生产路径")
	}

	res := e.ExecuteWithConfirm(context.Background(), Call{
		ID:    "c1",
		Name:  "bash",
		Input: `{"command":"rm /tmp/whale-ab/victim.txt"}`,
	})

	if res.NeedConfirm {
		t.Fatalf("deny 规则命中时不应请求确认（不得弹框），stageLogs=%+v", res.StageLogs)
	}
	if res.Proceed {
		t.Fatalf("deny 规则命中时应被拒绝，不应放行执行，stageLogs=%+v", res.StageLogs)
	}
	// 关键：handler 不得被调用（无副作用泄漏）
	if _, statErr := os.Stat(sentinel); !os.IsNotExist(statErr) {
		t.Fatalf("I2 失败：deny 路径下 handler 仍被执行（副作用泄漏）")
	}
}

// T2 — I1 单一强制点（F1 翻转版）：管线初始化后，直连 Executor.Execute 也必须
// 经过权限决策，deny 规则命中时被拒绝且零副作用。
//
// 修复前：直连 Execute 无视 deny 规则，命令被真实执行（双入口缺口，见 R1 接线断言 §1.2）。
// 修复后：Execute 在 Pipeline != nil 时收口到 Pipeline.Execute（含 PermissionStage）。
//
// 生产入口对齐：service_tool.go:24 / runner_core.go:2553（ExecuteTool）与
// runner_core.go:1470（ExecuteBatch fallback）都走 Execute；本用例用与生产一致的
// NewExecutor + InitPipelineWithHooks + Execute 初始化方式从真实入口进入。
func TestChar_I1_DirectExecuteBypassesPermissionPipeline(t *testing.T) {
	tmp := t.TempDir()
	r := NewRegistry()
	e := NewExecutor(r, tmp)

	// 与生产 T1 完全相同的 deny 规则 + bash/rm 输入
	policy := permission.NewPolicy(permission.LevelWorkspaceWrite)
	rule, err := permission.ParseRuleString("bash(rm:*)", permission.RuleDeny, permission.SourceSession)
	if err != nil {
		t.Fatalf("ParseRuleString: %v", err)
	}
	if err := policy.AddRule(*rule); err != nil {
		t.Fatalf("AddRule: %v", err)
	}

	// 生产初始化管线（与 T1 一致），然后走「直连 Execute」入口
	e.InitPipelineWithHooks(policy, tmp)
	if e.Pipeline == nil {
		t.Fatal("Pipeline 未初始化：本用例前提失效，无法代表生产路径")
	}

	sentinel := filepath.Join(tmp, "I1_SHOULD_NOT_EXIST")
	e.RegisterHandler("bash", func(ctx context.Context, input json.RawMessage) (string, error) {
		_ = os.WriteFile(sentinel, []byte("ran"), 0644)
		return "ran", nil
	})

	res := e.Execute(context.Background(), Call{
		ID:    "c2",
		Name:  "bash",
		Input: `{"command":"rm /tmp/whale-ab/victim.txt"}`,
	})

	// 修复后行为：deny 规则必须在直连 Execute 入口同样生效 → 拒绝执行
	if !res.IsError {
		t.Fatalf("F1 失败：deny 规则未在直连 Execute 入口拦截（仍放行），output=%s", res.Output)
	}
	if !strings.Contains(res.Output, "deny") && !strings.Contains(res.Output, "blocked") {
		t.Fatalf("F1 失败：拒绝原因应指向 deny 规则，实际 output=%s", res.Output)
	}
	// 关键：handler 不得被调用（零副作用）
	if _, statErr := os.Stat(sentinel); !os.IsNotExist(statErr) {
		t.Fatalf("F1 失败：deny 路径下 handler 仍被执行（副作用泄漏于 %s）", sentinel)
	}
}

// T3 — I3 边界校验（生产 handler 路径）：字面量 `..` 组件被拒绝且文件不被创建。
func TestChar_I3_WriteFilePathTraversalRejected(t *testing.T) {
	tmp := t.TempDir()
	r := NewRegistry()
	e := NewExecutor(r, tmp)

	// 手工拼接保留字面 "/../"（filepath.Join 会先清理掉 ".."，故此处不经由 Join）
	traversal := filepath.Join(tmp, "x") + "/../escaped_literal.txt"
	res := e.Execute(context.Background(), Call{
		ID:    "c3",
		Name:  "write_file",
		Input: fmt.Sprintf(`{"path":%q,"content":"x"}`, traversal),
	})

	if !res.IsError {
		t.Fatalf("I3 失败：字面 .. 遍历应被拒绝（期望 IsError）")
	}
	// 解析后的目标路径不应被创建
	resolved := filepath.Clean(traversal)
	if _, statErr := os.Stat(resolved); !os.IsNotExist(statErr) {
		t.Fatalf("I3 失败：遍历文件不应被创建于 %s", resolved)
	}
}

// T4 — I3 边界校验（生产管线入口 · F2 删除后的正确边界来源）：
// 写工作区外绝对路径必须被管线 CustomValidateStage 拒绝；工作区内路径仍可写。
//
// 背景：原 T4 曾经 `e.Execute`（Pipeline==nil → executeDirect → handler）
// 直连 writeFileHandler 触发 F2 的 writable-root 收敛，属「绕开管线的构造场景」。
// 但生产三入口（lifecycle/runner/bridge）都 InitPipelineWithHooks，write_file 永远先经
// CustomValidateStage（pipeline.go:487-501）拦截，F2 在管线内不可达 → 已判定为死代码并删除。
// 本测试改从真实生产入口进入：InitPipelineWithHooks → Execute，断言 CustomValidateStage
// 负责拦截（消息 workspace boundary check，与用户实测一致）。
func TestChar_I3_WriteFileAbsolutePathOutsideWorkspaceAllowed(t *testing.T) {
	ws := t.TempDir() // 伪工作区
	r := NewRegistry()
	e := NewExecutor(r, ws)

	// 生产初始化方式（与 TUI/runner 真实路径一致）
	policy := permission.NewPolicy(permission.LevelWorkspaceWrite)
	// 允许 write_file（模拟用户已允许该工具 / TUI 已审批），使"工作区内写"在
	// PermissionStage 放行（不弹确认）；"工作区外写"仍被 CustomValidateStage 前置拒绝。
	rule, err := permission.ParseRuleString("write_file", permission.RuleAllow, permission.SourceSession)
	if err != nil {
		t.Fatalf("ParseRuleString(write_file allow): %v", err)
	}
	if err := policy.AddRule(*rule); err != nil {
		t.Fatalf("AddRule: %v", err)
	}
	e.InitPipelineWithHooks(policy, ws)
	if e.Pipeline == nil {
		t.Fatal("Pipeline 未初始化：本用例前提失效，无法代表生产路径")
	}

	// 工作区之外的兄弟目录（仍位于 /tmp，便于清理）
	outside := filepath.Join(filepath.Dir(ws), "whale_i3_outside.txt")
	defer os.Remove(outside)

	res := e.Execute(context.Background(), Call{
		ID:    "c4",
		Name:  "write_file",
		Input: fmt.Sprintf(`{"path":%q,"content":"hi"}`, outside),
	})

	// 生产管线行为：CustomValidateStage 前置拒绝，IsError=true，Output 指向越界原因。
	// 消息与用户 2026-09-10 实测一致（workspace boundary check: ... is not within workspace）。
	if !res.IsError {
		t.Fatalf("越界写应被管线拒绝（IsError），却被放行：output=%s", res.Output)
	}
	if !strings.Contains(res.Output, "workspace boundary check") || !strings.Contains(res.Output, "not within workspace") {
		t.Fatalf("管线拒绝原因不符（期望 workspace boundary check），output=%s", res.Output)
	}
	if _, statErr := os.Stat(outside); !os.IsNotExist(statErr) {
		t.Fatalf("越界文件仍被创建于 %s", outside)
	}

	// 补充断言：工作区内路径仍可写（管线不误伤正常写入）。
	inside := filepath.Join(ws, "whale_i3_inside.txt")
	resIn := e.Execute(context.Background(), Call{
		ID:    "c4-in",
		Name:  "write_file",
		Input: fmt.Sprintf(`{"path":%q,"content":"ok"}`, inside),
	})
	if resIn.IsError {
		t.Fatalf("工作区内路径写应放行，却被拒绝：%s", resIn.Output)
	}
	if _, statErr := os.Stat(inside); os.IsNotExist(statErr) {
		t.Fatalf("工作区内文件应已写入 %s", inside)
	}
}

// T5 — I5 并发安全（冒烟）：ExecuteBatch 的 safe 路径并发跑 read_file。
func TestChar_I5_BatchSafeToolConcurrentNoPanic(t *testing.T) {
	tmp := t.TempDir()
	r := NewRegistry()
	e := NewExecutor(r, tmp)

	const n = 24
	calls := make([]Call, 0, n)
	for i := 0; i < n; i++ {
		p := filepath.Join(tmp, fmt.Sprintf("f%d.txt", i))
		if err := os.WriteFile(p, []byte("data"), 0644); err != nil {
			t.Fatalf("setup write: %v", err)
		}
		calls = append(calls, Call{
			ID:    fmt.Sprintf("r%d", i),
			Name:  "read_file",
			Input: fmt.Sprintf(`{"path":%q}`, p),
		})
	}

	results := e.ExecuteBatch(context.Background(), calls)
	if len(results) != n {
		t.Fatalf("并发批处理返回数量不符：got %d want %d", len(results), n)
	}
	for _, res := range results {
		if res.IsError {
			t.Fatalf("并发读失败：%s", res.Output)
		}
	}
}
