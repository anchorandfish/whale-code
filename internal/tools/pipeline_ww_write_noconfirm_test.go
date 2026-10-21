package tools

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/anchorandfish/whale-code/internal/permission"
)

// wwWriteInput 构造 write_file 的 JSON 入参（本文件自足，不依赖其它测试文件的 helper）。
func wwWriteInput(path, content string) string {
	b, err := json.Marshal(map[string]string{"path": path, "content": content})
	if err != nil {
		panic(err)
	}
	return string(b)
}

// S9/S10 手测缺陷· 判别性红测：workspace-write 档下「工作区内的写入」不应弹确认。
//
// 缺陷签名（用户手测）：档位已是 workspace-write，改两个工作区内的文件却各弹一次框。
// 根因：gate ①（permission.checkModeLocked）在 ww 档对写类工具**恒**返回 needConfirm=true，
// 而它**不看路径**（Policy 结构体里根本没有 workDir）；gate ②（approval OnRequest
// 「仅危险工具才问」）本可救回，但 IsDangerousToolFor 把所有「需要写权限」的工具都判为危险，
// 于是救回永不发生。⇒ 见 .workbuddy/memory/domains/权限与规则.md。
//
// 修法（本用例钉住的规格）：审批要拦的是「**逃逸**工作区」，不是「具备写能力」（对齐业内既有实现
// `core/src/safety.rs:32 assess_patch_safety` —— patch 落在 writable roots 内即 AutoApprove）。
// 判定复用**既有单一真相源**：写类工具表 isWriteTool + 工作区边界 checkWorkspaceBoundary。
//
// 生产入口：InitPipelineWithHooks → NewToolPipeline → ExecuteWithConfirm
// （即 internal/tui/tool_exec.go executeToolsDirect 走的入口）。
func TestFix_WW_WorkspaceContainedWriteNeedsNoConfirm(t *testing.T) {
	workDir := t.TempDir()
	sub := filepath.Join(workDir, "sub")
	if err := os.MkdirAll(sub, 0o755); err != nil {
		t.Fatalf("precondition: mkdir sub: %v", err)
	}

	r := NewRegistry()
	e := NewExecutor(r, workDir)
	policy := permission.NewPolicy(permission.LevelWorkspaceWrite)
	e.InitPipelineWithHooks(policy, workDir)
	if e.Pipeline == nil {
		t.Fatal("precondition: 生产装配下 Pipeline 必须非 nil")
	}

	target := filepath.Join(sub, "other.txt")
	pr := e.ExecuteWithConfirm(context.Background(), Call{
		ID:    "ww-1",
		Name:  "write_file",
		Input: wwWriteInput(target, "CHANGED2"),
	})

	if pr.NeedConfirm {
		t.Fatalf("工作区内的写入不应弹确认（ww 档审批拦的是「逃逸工作区」）: rejection=%q stageLogs=%+v",
			pr.Rejection, pr.StageLogs)
	}
	if !pr.Proceed {
		t.Fatalf("工作区内写入应放行: rejection=%q stageLogs=%+v", pr.Rejection, pr.StageLogs)
	}
	if pr.Result == nil || pr.Result.IsError {
		t.Fatalf("工作区内写入应真正执行（免问 ≠ 免执行）: result=%+v", pr.Result)
	}
	got, err := os.ReadFile(target)
	if err != nil {
		t.Fatalf("免问后文件应已落盘: %v", err)
	}
	if string(got) != "CHANGED2" {
		t.Fatalf("落盘内容 = %q, want %q", string(got), "CHANGED2")
	}
}

// 反例组（守卫）：证明「工作区内写免问」**没有**把其它情形一并放过。
// 这些用例在改动前后都应绿 —— 绿了才说明免问的边界是收紧的。
func TestGuard_WW_WorkspaceContainedWriteExclusions(t *testing.T) {
	ctx := context.Background()
	newWWPipeline := func(t *testing.T, workDir string) *Executor {
		t.Helper()
		e := NewExecutor(NewRegistry(), workDir)
		e.InitPipelineWithHooks(permission.NewPolicy(permission.LevelWorkspaceWrite), workDir)
		return e
	}

	t.Run("工作区外的写入不得静默放行", func(t *testing.T) {
		workDir := t.TempDir()
		outside := t.TempDir() // 另一个独立目录 = 工作区外
		e := newWWPipeline(t, workDir)

		target := filepath.Join(outside, "escape.txt")
		pr := e.ExecuteWithConfirm(ctx, Call{
			ID: "ww-out", Name: "write_file", Input: wwWriteInput(target, "ESCAPED"),
		})
		if pr.Proceed {
			t.Fatalf("工作区外写入必须被拦: rejection=%q stageLogs=%+v", pr.Rejection, pr.StageLogs)
		}
		if _, err := os.Stat(target); err == nil {
			t.Fatal("工作区外文件不得被创建（前置拒绝、零副作用）")
		}
	})

	t.Run("非写类工具（bash）仍需确认", func(t *testing.T) {
		e := newWWPipeline(t, t.TempDir())
		pr := e.ExecuteWithConfirm(ctx, Call{ID: "ww-bash", Name: "bash", Input: `{"command":"mkdir -p ww_probe"}`})
		if !pr.NeedConfirm {
			t.Fatalf("bash 不在写类工具表内，不得被本次免问覆盖: stageLogs=%+v", pr.StageLogs)
		}
	})

	t.Run("写类但无可提取路径的工具（git_commit）仍需确认", func(t *testing.T) {
		e := newWWPipeline(t, t.TempDir())
		pr := e.ExecuteWithConfirm(ctx, Call{ID: "ww-git", Name: "git_commit", Input: `{"message":"x"}`})
		if !pr.NeedConfirm {
			t.Fatalf("写类工具若无路径目标，则「目标全在工作区内」不可判定 ⇒ 必须仍问: stageLogs=%+v", pr.StageLogs)
		}
	})

	t.Run("workDir 为空时不得判定为工作区内", func(t *testing.T) {
		// checkWorkspaceBoundary 在 workDir=="" 时**不做判定**（返回 ok）——
		// 必须显式要求 workDir != ""，否则空值会静默变成「处处都算工作区内」。
		dir := t.TempDir()
		e := NewExecutor(NewRegistry(), "")
		e.InitPipelineWithHooks(permission.NewPolicy(permission.LevelWorkspaceWrite), "")

		pr := e.ExecuteWithConfirm(ctx, Call{
			ID: "ww-nowork", Name: "write_file",
			Input: wwWriteInput(filepath.Join(dir, "x.txt"), "X"),
		})
		if !pr.NeedConfirm {
			t.Fatalf("workDir 为空 ⇒ 边界不可判定 ⇒ 必须仍问（fail-closed）: stageLogs=%+v", pr.StageLogs)
		}
	})

	t.Run("read-only 档下工作区内写入仍被拒（能力硬边界）", func(t *testing.T) {
		workDir := t.TempDir()
		e := NewExecutor(NewRegistry(), workDir)
		e.InitPipelineWithHooks(permission.NewPolicy(permission.LevelReadOnly), workDir)

		pr := e.ExecuteWithConfirm(ctx, Call{
			ID: "ro-in", Name: "write_file",
			Input: wwWriteInput(filepath.Join(workDir, "y.txt"), "Y"),
		})
		if pr.Proceed || pr.NeedConfirm {
			t.Fatalf("read-only 必须直接拒（不弹框、不执行）: proceed=%v needConfirm=%v", pr.Proceed, pr.NeedConfirm)
		}
		if pr.FailureKind != FailureKindPolicyDenied {
			t.Fatalf("read-only 拒绝形态 = %q, want %q", pr.FailureKind, FailureKindPolicyDenied)
		}
	})
}
