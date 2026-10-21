package tools

// =============================================================================
// S11 diff / patch 应用 — 【手测探针】（真实真机路径可复现）
//
// 目的：把 diff_TEST_GUIDE.md §B 里「AI 跑不了、必须真机验证」的项，
//      尽可能收敛成**一条命令、确定性输出**的可复现探针，减少人工目检负担。
//
// 与 audit_diff_fix_test.go 的区别：
//   - 那个用 denySandboxMgr **桩**验证 handler 分支；
//   - 本文件用 sandbox.NewProductionSandboxManager / sandbox.NewManager
//     **真实管理器**（平台真实配置：darwin Seatbelt / linux namespaces+Landlock /
//     windows Job Object），验证拒绝链路在真沙箱配置下仍然成立。
//
// 运行（默认 Skip，避免污染常规 CI）：
//   cd whale-code
//   WHALE_S11_MANUAL=1 go test ./internal/tools/ -run TestManual_S11 -count=1 -v
//
// ⚠️ 本文件只在 `Pipeline == nil` 的回退入口才能触发 handler 层 sandbox 拒绝
//    （原因见 P2 注释）；生产主路径的越界拒绝由管线 CustomValidateStage 承担（P1）。
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

// s11ManualGate 默认 Skip；只有显式 WHALE_S11_MANUAL=1 才执行。
func s11ManualGate(t *testing.T) {
	t.Helper()
	if os.Getenv("WHALE_S11_MANUAL") != "1" {
		t.Skip("S11 手测探针：设 WHALE_S11_MANUAL=1 才运行（避免污染常规测试）")
	}
}

// s11Report 打印一条工具结果的全部关键字段（供人工核对）。
func s11Report(t *testing.T, tag string, res Result) {
	t.Helper()
	t.Logf("  [%s] name=%s is_error=%v failure_kind=%q", tag, res.Name, res.IsError, res.FailureKind)
	t.Logf("  [%s] output=%q", tag, strings.TrimSpace(res.Output))
}

// s11PolicyAllowWrite 构造 workspace-write 且显式放行 write_file + edit_file 的策略。
//
// 为什么必须显式放行这两个工具：走完整生产管线时 PermissionStage 会拦未放行的工具，
// 返回 `tool "edit_file" requires user confirmation`（NeedConfirm，无确认回调即视为失败），
// 那样就测不到本探针想测的「沙箱/边界」分支了。这是**探针前提**，不是产品缺陷。
func s11PolicyAllowWrite(t *testing.T) *permission.Policy {
	t.Helper()
	p := permission.NewPolicy(permission.LevelWorkspaceWrite)
	for _, tool := range []string{"write_file", "edit_file"} {
		rule, err := permission.ParseRuleString(tool, permission.RuleAllow, permission.SourceSession)
		if err != nil {
			t.Fatalf("ParseRuleString(%s): %v", tool, err)
		}
		if err := p.AddRule(*rule); err != nil {
			t.Fatalf("AddRule(%s): %v", tool, err)
		}
	}
	return p
}

// -----------------------------------------------------------------------------
// P1 — 生产主路径：真实生产沙箱 + 生产管线，越界写必须被拒
//
//	（CustomValidateStage workspace boundary → IsError=true + sandbox_denied）
//
// -----------------------------------------------------------------------------
func TestManual_S11_P1_RealSandbox_PipelineDeny_OutOfWorkspace(t *testing.T) {
	s11ManualGate(t)
	ws := t.TempDir()
	e := NewExecutor(NewRegistry(), ws)
	e.SetSandboxMgr(sandbox.NewProductionSandboxManager(ws)) // 真实生产管理器（Strict）
	e.InitPipelineWithHooks(s11PolicyAllowWrite(t), ws)
	if e.Pipeline == nil {
		t.Fatal("Pipeline 未初始化：本用例前提失效")
	}

	outside := filepath.Join(filepath.Dir(ws), "s11_p1_outside.txt")
	defer os.Remove(outside)

	res := e.Execute(context.Background(), Call{
		ID: "p1", Name: "write_file",
		Input: fmt.Sprintf(`{"path":%q,"content":"escaped"}`, outside),
	})
	s11Report(t, "P1", res)

	if _, err := os.Stat(outside); !os.IsNotExist(err) {
		t.Fatalf("❌ 越界文件竟被创建：%s", outside)
	}
	if !res.IsError {
		t.Errorf("❌ 生产管线越界拒绝应为 IsError=true，实际 false")
	}
	if res.FailureKind != FailureKindSandboxDenied {
		t.Errorf("❌ 期望 failure_kind=%q，实际 %q", FailureKindSandboxDenied, res.FailureKind)
	}
	if !strings.Contains(res.Output, "workspace boundary check") {
		t.Errorf("⚠️ 拒绝原因未含 'workspace boundary check'：%q", res.Output)
	}
}

// -----------------------------------------------------------------------------
// P1b — 直接问真实沙箱管理器要判定（不经过管线），暴露「沙箱与管线判定范围不同」的事实：
//
//	A4 之后：沙箱**不再**放行整个 $TMPDIR，只放行沙箱自建的 tmp 根
//	（`internal/sandbox/sandbox_tmp.go` 的 SandboxTempRoot() = <TMPDIR>/whale-sandbox；
//	根不存在或是符号链接 ⇒ 不放行，fail-closed）。$TMPDIR 顶层不再放行。
//	管线侧则始终只允许工作区。
//
// -----------------------------------------------------------------------------
func TestManual_S11_P1b_RealSandbox_ValidatePath_Decisions(t *testing.T) {
	s11ManualGate(t)
	ws := t.TempDir()
	mgr := sandbox.NewProductionSandboxManager(ws)
	t.Logf("  真实沙箱 level=%v workDir=%s", mgr.Level(), ws)

	cases := []string{
		filepath.Join(ws, "inside.txt"),            // 工作区内 → 应放行
		"/etc/whale_s11_probe.txt",                 // 系统目录 → 应拒绝
		filepath.Join(os.TempDir(), "s11_tmp.txt"), // A4 后 $TMPDIR 顶层不再放行（仅沙箱自建 tmp 根）
	}
	for _, p := range cases {
		err := mgr.ValidatePath(context.Background(), p, nil)
		t.Logf("  ValidatePath(%q) → err=%v", p, err)
	}
	// 硬断言：系统目录必须被拒（否则真沙箱显然失效）
	if err := mgr.ValidatePath(context.Background(), "/etc/whale_s11_probe.txt", nil); err == nil {
		t.Errorf("❌ 真实沙箱竟放行 /etc —— 沙箱配置或平台后端异常")
	}
}

// -----------------------------------------------------------------------------
// P2 — handler 层 sandbox 拒绝（真实管理器，回退入口 Pipeline==nil）
//
//	B 语义：IsError 保持 false + Output 含 "Sandbox denied" + FailureKind=sandbox_denied
//
// ⚠️ 为什么必须 Pipeline==nil：生产主路径管线先跑 CustomValidateStage，
//
//	其工作区边界与沙箱 WorkDir 同源，故「管线会先拒」；handler 的 sandbox
//	分支只在管线未装配（回退）时可达 —— 属纵深防御，非主拒绝点。
//
// -----------------------------------------------------------------------------
func TestManual_S11_P2_RealSandbox_HandlerDeny_Fallback(t *testing.T) {
	s11ManualGate(t)
	ws := t.TempDir()
	e := NewExecutor(NewRegistry(), ws)
	e.SetSandboxMgr(sandbox.NewProductionSandboxManager(ws)) // 真实管理器
	// 故意不 InitPipeline → Execute 走 executeDirect → handler.validatePathSandbox

	target := "/etc/whale_s11_probe_write.txt"
	res := e.Execute(context.Background(), Call{
		ID: "p2", Name: "write_file",
		Input: fmt.Sprintf(`{"path":%q,"content":"x"}`, target),
	})
	s11Report(t, "P2", res)

	if _, err := os.Stat(target); !os.IsNotExist(err) {
		t.Fatalf("❌ 被拒路径竟被写入：%s", target)
	}
	if res.IsError {
		t.Errorf("❌ B 语义应保持 IsError=false，实际 true")
	}
	if !strings.Contains(res.Output, "Sandbox denied") {
		t.Errorf("❌ Output 应保留 'Sandbox denied' 文本，实际 %q", res.Output)
	}
	if res.FailureKind != FailureKindSandboxDenied {
		t.Errorf("❌ 期望 failure_kind=%q，实际 %q", FailureKindSandboxDenied, res.FailureKind)
	}
}

// -----------------------------------------------------------------------------
// P3 — 回归：真实生产沙箱下，工作区内 write + edit 仍应成功
// -----------------------------------------------------------------------------
func TestManual_S11_P3_InWorkspace_WriteEdit_Happy(t *testing.T) {
	s11ManualGate(t)
	ws := t.TempDir()
	e := NewExecutor(NewRegistry(), ws)
	e.SetSandboxMgr(sandbox.NewProductionSandboxManager(ws))
	e.InitPipelineWithHooks(s11PolicyAllowWrite(t), ws)

	f := filepath.Join(ws, "happy.txt")
	res := e.Execute(context.Background(), Call{
		ID: "p3-w", Name: "write_file",
		Input: fmt.Sprintf(`{"path":%q,"content":"alpha\nbeta\n"}`, f),
	})
	s11Report(t, "P3-write", res)
	if res.IsError {
		t.Fatalf("❌ 工作区内写入被拒（回归）：%s", res.Output)
	}

	res2 := e.Execute(context.Background(), Call{
		ID: "p3-e", Name: "edit_file",
		Input: fmt.Sprintf(`{"path":%q,"old_text":"beta","new_text":"BETA"}`, f),
	})
	s11Report(t, "P3-edit", res2)
	if res2.IsError {
		t.Fatalf("❌ 工作区内编辑被拒（回归）：%s", res2.Output)
	}
	got, _ := os.ReadFile(f)
	if !strings.Contains(string(got), "BETA") {
		t.Errorf("❌ 内容未更新：%q", string(got))
	}
}

// -----------------------------------------------------------------------------
// P4 — G4：混合行尾文件，未触及行必须逐字节不变
// -----------------------------------------------------------------------------
func TestManual_S11_P4_MixedEOL_ByteExact(t *testing.T) {
	s11ManualGate(t)
	ws := t.TempDir()
	e := NewExecutor(NewRegistry(), ws)
	e.SetSandboxMgr(sandbox.NewProductionSandboxManager(ws))
	e.InitPipelineWithHooks(s11PolicyAllowWrite(t), ws)

	orig := "line one\r\nline two\nline three\r\n"
	f := filepath.Join(ws, "mixed.txt")
	if err := os.WriteFile(f, []byte(orig), 0644); err != nil {
		t.Fatal(err)
	}
	res := e.Execute(context.Background(), Call{
		ID: "p4", Name: "edit_file",
		Input: fmt.Sprintf(`{"path":%q,"old_text":"line two","new_text":"line TWO"}`, f),
	})
	s11Report(t, "P4", res)
	got, _ := os.ReadFile(f)

	want := "line one\r\nline TWO\nline three\r\n"
	t.Logf("  before=%q", orig)
	t.Logf("  after =%q", string(got))
	t.Logf("  want  =%q", want)
	if string(got) != want {
		t.Errorf("❌ 混合行尾未被逐字节保留")
	}
}

// -----------------------------------------------------------------------------
// P5 — G2：原子写，编辑后无 *.tmp 残留 + 原文件权限位保持不变
// -----------------------------------------------------------------------------
func TestManual_S11_P5_AtomicWrite_NoResidue_Perms(t *testing.T) {
	s11ManualGate(t)
	ws := t.TempDir()
	e := NewExecutor(NewRegistry(), ws)
	e.SetSandboxMgr(sandbox.NewProductionSandboxManager(ws))
	e.InitPipelineWithHooks(s11PolicyAllowWrite(t), ws)

	f := filepath.Join(ws, "perm.txt")
	if err := os.WriteFile(f, []byte("secret\n"), 0600); err != nil {
		t.Fatal(err)
	}
	before, _ := os.Stat(f)
	t.Logf("  before mode=%v", before.Mode().Perm())

	res := e.Execute(context.Background(), Call{
		ID: "p5", Name: "edit_file",
		Input: fmt.Sprintf(`{"path":%q,"old_text":"secret","new_text":"SECRET"}`, f),
	})
	s11Report(t, "P5", res)
	if res.IsError {
		t.Fatalf("edit 失败：%s", res.Output)
	}

	after, _ := os.Stat(f)
	t.Logf("  after  mode=%v", after.Mode().Perm())
	if after.Mode().Perm() != before.Mode().Perm() {
		t.Errorf("❌ 权限位被改写：%v → %v", before.Mode().Perm(), after.Mode().Perm())
	}

	entries, _ := os.ReadDir(ws)
	var residue []string
	for _, en := range entries {
		if strings.HasSuffix(en.Name(), ".tmp") || strings.Contains(en.Name(), ".whale-tmp") {
			residue = append(residue, en.Name())
		}
	}
	t.Logf("  目录条目=%v", func() []string {
		var names []string
		for _, en := range entries {
			names = append(names, en.Name())
		}
		return names
	}())
	if len(residue) > 0 {
		t.Errorf("❌ 残留临时文件：%v", residue)
	}
}

// -----------------------------------------------------------------------------
// P6 — 符号链接逃逸：工作区内 symlink 指向工作区外 → 写操作必须被拒
// -----------------------------------------------------------------------------
func TestManual_S11_P6_SymlinkEscape_Denied(t *testing.T) {
	s11ManualGate(t)
	ws := t.TempDir()
	outside := t.TempDir()

	target := filepath.Join(outside, "escape.txt")
	if err := os.WriteFile(target, []byte("ORIGINAL"), 0644); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(ws, "link.txt")
	if err := os.Symlink(target, link); err != nil {
		t.Skipf("平台不支持 symlink，跳过：%v", err)
	}

	e := NewExecutor(NewRegistry(), ws)
	e.SetSandboxMgr(sandbox.NewProductionSandboxManager(ws))
	e.InitPipelineWithHooks(s11PolicyAllowWrite(t), ws)

	res := e.Execute(context.Background(), Call{
		ID: "p6", Name: "write_file",
		Input: fmt.Sprintf(`{"path":%q,"content":"HACKED"}`, link),
	})
	s11Report(t, "P6", res)

	got, _ := os.ReadFile(target)
	t.Logf("  链接目标内容=%q", string(got))
	if string(got) != "ORIGINAL" {
		t.Errorf("❌ 符号链接逃逸成功：工作区外文件被改写")
	}
	if res.FailureKind != FailureKindSandboxDenied {
		t.Errorf("❌ 期望 failure_kind=%q，实际 %q", FailureKindSandboxDenied, res.FailureKind)
	}
}

// -----------------------------------------------------------------------------
// P7 — 只读模式：工作区内写也必须被拒（policy_denied，与 sandbox_denied 区分）
// -----------------------------------------------------------------------------
func TestManual_S11_P7_ReadOnly_DeniesWrite(t *testing.T) {
	s11ManualGate(t)
	ws := t.TempDir()
	e := NewExecutor(NewRegistry(), ws)
	e.SetSandboxMgr(sandbox.NewProductionSandboxManager(ws))
	e.InitPipelineWithHooks(permission.NewPolicy(permission.LevelReadOnly), ws)

	f := filepath.Join(ws, "ro.txt")
	res := e.Execute(context.Background(), Call{
		ID: "p7", Name: "write_file",
		Input: fmt.Sprintf(`{"path":%q,"content":"nope"}`, f),
	})
	s11Report(t, "P7", res)

	if _, err := os.Stat(f); !os.IsNotExist(err) {
		t.Errorf("❌ 只读模式竟写入成功：%s", f)
	}
	if !res.IsError {
		t.Errorf("❌ 只读拒绝应为 IsError=true")
	}
	if res.FailureKind != FailureKindPolicyDenied {
		t.Errorf("❌ 期望 failure_kind=%q，实际 %q", FailureKindPolicyDenied, res.FailureKind)
	}
}
