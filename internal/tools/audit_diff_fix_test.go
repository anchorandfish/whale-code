package tools

// =============================================================================
// S11 diff / patch 应用 审计 — Fix 验证测试（TDD 红→绿）
//
// 日期：2026-09-10 域：S11（AUDIT_ROADMAP.md §2）
// 本轮 Fix 决策（用户拍板）：
// - G1 走【B 标记】：拒绝保留模型非-error 契约（Output 文本 + IsError=false），
// 但打上结构化 FailureKind=FailureKindSandboxDenied，供编排/TUI 识别为终局拒绝
// （阻断 TUI tool_exec.go:559-566 所述 write_file→bash→edit_file 噪声循环）。
// （A 大修「拒绝上报统一」另立轮次。）
// - G2：edit_file 复用 util.AtomicWriteFile（与 write_file 一致）。
// - G4：混合行尾文件编辑时，未触及行必须逐字节保留。
// - G3/G5 本轮不做（记入未来可选增强）。
//
// ⚠️ 基线更正：并行会话已于 2026-09-10T04:24 删除 handler 层
// validateWritablePathSandbox（判定 F2 死代码）。故 G1 的**生产拒绝点**现为
// 管线 CustomValidateStage（pipeline.go:487-501，workspace boundary check），
// handler 层剩余拒绝点为 sandbox 层 validatePathSandbox（file.go:242/375）。
// 本文件两个 G1 用例分别覆盖这两点。
//
// 纪律：红测从生产真实入口进。修复前应【红】，修复后【绿】。
// =============================================================================

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/anchorandfish/whale-code/internal/permission"
	"github.com/anchorandfish/whale-code/internal/sandbox"
)

// denySandboxMgr 是 headless 测试桩：所有路径校验一律拒绝，用于触发 handler 层
// validatePathSandbox 的拒绝分支（file.go:242/375）而无需真实 OS 沙箱。
type denySandboxMgr struct{}

func (denySandboxMgr) Prepare(context.Context, sandbox.SandboxLevel, string) (sandbox.SandboxContext, func(), error) {
	return sandbox.SandboxContext{}, func() {}, nil
}
func (denySandboxMgr) ValidateCommand(context.Context, string, *sandbox.SandboxConfig) (*sandbox.ValidationResult, error) {
	return &sandbox.ValidationResult{}, nil
}
func (denySandboxMgr) ValidatePath(context.Context, string, *sandbox.SandboxConfig) error {
	return fmt.Errorf("stub sandbox: path denied for test")
}
func (denySandboxMgr) Level() sandbox.SandboxLevel { return sandbox.SandboxLevelStrict }

// G1-Prod — 生产管线入口：越界写被 CustomValidateStage 拒绝，结果必须携带
// FailureKind=FailureKindSandboxDenied（当前缺失 → 红）。
func TestFix_S11_G1_FileDenyCarriesSandboxFailureKind_Production(t *testing.T) {
	ws := t.TempDir()
	r := NewRegistry()
	e := NewExecutor(r, ws)

	policy := permission.NewPolicy(permission.LevelWorkspaceWrite)
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

	outside := filepath.Join(filepath.Dir(ws), "whale_s11_g1_outside.txt")
	defer os.Remove(outside)

	res := e.Execute(context.Background(), Call{
		ID:    "s11-g1-prod",
		Name:  "write_file",
		Input: fmt.Sprintf(`{"path":%q,"content":"x"}`, outside),
	})

	if _, statErr := os.Stat(outside); !os.IsNotExist(statErr) {
		t.Fatalf("越界文件被创建于 %s", outside)
	}
	if res.FailureKind != FailureKindSandboxDenied {
		t.Fatalf("生产路径越界拒绝应携带 FailureKind=%q，实际=%q；output=%s",
			FailureKindSandboxDenied, res.FailureKind, res.Output)
	}
}

// G1-Handler — handler 层 sandbox 拒绝分支（file.go:242/375）：B 语义 =
// IsError 保持 false + 输出保留 "Sandbox denied" 文本 + 新增 FailureKind 标记。
func TestFix_S11_G1_HandlerSandboxDenyMarker_Soft(t *testing.T) {
	ws := t.TempDir()
	r := NewRegistry()
	e := NewExecutor(r, ws)
	e.SandboxMgr = denySandboxMgr{} // 触发 handler 层 validatePathSandbox 拒绝

	target := filepath.Join(ws, "denied.txt")
	res := e.Execute(context.Background(), Call{
		ID:    "s11-g1-handler",
		Name:  "write_file",
		Input: fmt.Sprintf(`{"path":%q,"content":"x"}`, target),
	})

	if _, statErr := os.Stat(target); !os.IsNotExist(statErr) {
		t.Fatalf("被拒写入不应创建文件: %s", target)
	}
	if res.IsError {
		t.Fatalf("B 语义应保持 IsError=false，实际 true；output=%s", res.Output)
	}
	if !strings.Contains(res.Output, "Sandbox denied") {
		t.Fatalf("B 语义应保留 'Sandbox denied' 文本，实际 output=%s", res.Output)
	}
	if res.FailureKind != FailureKindSandboxDenied {
		t.Fatalf("handler 拒绝应携带 FailureKind=%q，实际=%q",
			FailureKindSandboxDenied, res.FailureKind)
	}
}

// G4 — 混合行尾文件编辑：未触及行必须逐字节保留（当前全量 \n→\r\n → 红）。
func TestFix_S11_G4_MixedEOL_PreservesUntouchedLines(t *testing.T) {
	tmp := t.TempDir()
	file := filepath.Join(tmp, "mixed.txt")
	if err := os.WriteFile(file, []byte("line one\r\nline two\nline three\r\n"), 0644); err != nil {
		t.Fatalf("setup: %v", err)
	}

	out, isErr := runDiffTool(t, tmp, "edit_file", map[string]interface{}{
		"path":     file,
		"old_text": "line two",
		"new_text": "line TWO",
	})
	if isErr {
		t.Fatalf("edit 失败: %s", out)
	}

	got, _ := os.ReadFile(file)
	want := "line one\r\nline TWO\nline three\r\n"
	if string(got) != want {
		t.Fatalf("混合行尾未被逐字节保留：\n got=%q\nwant=%q", string(got), want)
	}
}

// G2 — edit_file 成功后不得残留临时文件（守卫；原子性一致性主要由复用 util.AtomicWriteFile 保证）。
func TestFix_S11_G2_EditNoTempResidue(t *testing.T) {
	tmp := t.TempDir()
	file := filepath.Join(tmp, "g2.txt")
	if err := os.WriteFile(file, []byte("alpha\nbeta\n"), 0644); err != nil {
		t.Fatalf("setup: %v", err)
	}

	out, isErr := runDiffTool(t, tmp, "edit_file", map[string]interface{}{
		"path":     file,
		"old_text": "beta",
		"new_text": "BETA",
	})
	if isErr {
		t.Fatalf("edit 失败: %s", out)
	}

	entries, _ := os.ReadDir(tmp)
	for _, en := range entries {
		if strings.HasSuffix(en.Name(), ".tmp") || strings.HasSuffix(en.Name(), ".whale-tmp") {
			t.Fatalf("编辑后残留临时文件: %s", en.Name())
		}
	}
	got, _ := os.ReadFile(file)
	if !strings.Contains(string(got), "BETA") {
		t.Fatalf("内容未更新: %q", string(got))
	}
}
