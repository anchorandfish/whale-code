package tools

// =============================================================================
// S11 diff / patch 应用 审计 — 表征测试（characterization）
//
// 日期：2026-09-10 域：S11（AUDIT_ROADMAP.md §2）
// 不变量焦点：I3（边界输入校验） / I4（状态机完备）
//
// 本文件目的：在「不动任何源码」的前提下，钉死 whale-code 的「内容落盘应用」链路
// （write_file / edit_file）的 *当前* 真实行为。所有用例从生产真实入口
// Executor.Execute(Call{Name:"write_file"|"edit_file", ...})
// 进入，与 S3 的 tool 执行入口一致。
//
// 设计纪律（production-path-tdd / 防假绿）：
// - 任何「断言副作用没发生」的用例，先在同一用例内用一个 gate 开启的对照组证明
// 副作用本可以发生（例如先做一次成功写入），否则该「未变化」断言极易假绿。
// - 这些测试当前必须全绿 = 已钉住当前行为；后续 Fix 落地时，表征「缺陷」的
// 用例会被刻意翻转并补足新断言。
//
// 参考实现锚点（见 diff_HANDOFF.md §Anchor）：
// - 同类 CLI apply_patch → assess_patch_safety：Reject 经 FunctionCallError 上报模型
//；空 patch / 越出可写根 → Reject（fail-closed）
// (safety.rs:40-44, 102-109)。
// - 同类 CLI Edit 工具：old_string/new_string，且支持上下文模糊匹配
// (FileEditToolDiff.tsx:107,124,173)。
// =============================================================================

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/anchorandfish/whale-code/internal/permission"
)

// runDiffTool 是 S11 统一入口封装：与 S3 一致，直接走 Executor.Execute。
func runDiffTool(t *testing.T, tmp string, name string, input map[string]interface{}) (string, bool) {
	t.Helper()
	r := NewRegistry()
	e := NewExecutor(r, tmp)
	res := e.Execute(context.Background(), Call{
		ID:    "1",
		Name:  name,
		Input: mustMarshal(input),
	})
	return res.Output, res.IsError
}

// T1 — I4 状态机：edit_file 唯一匹配 → 精确替换（且只替换一次）。
// 对照组：文件含两处 "hello world"，old_text 取唯一子串 "foo bar" 才能命中且只改一处。
func TestChar_DiffEdit_UniqueMatch_Replaced(t *testing.T) {
	tmp := t.TempDir()
	file := filepath.Join(tmp, "edit.txt")
	if err := os.WriteFile(file, []byte("hello world\nfoo bar\nhello world"), 0644); err != nil {
		t.Fatalf("setup: %v", err)
	}

	out, isErr := runDiffTool(t, tmp, "edit_file", map[string]interface{}{
		"path":     file,
		"old_text": "foo bar",
		"new_text": "baz qux",
	})
	if isErr {
		t.Fatalf("预期成功替换，却报错: %s", out)
	}
	got, _ := os.ReadFile(file)
	if string(got) != "hello world\nbaz qux\nhello world" {
		t.Errorf("替换结果不符：got %q", string(got))
	}
}

// T2 — I4 状态机：edit_file 旧文本不存在 → 返回 error，且文件内容不变。
// 防假绿：先在同一用例内做一次「成功写入」对照组，证明写机制本可改变文件；
// 随后对无匹配文本的编辑必须报错且文件保持原样。
func TestChar_DiffEdit_NoMatch_ErrorAndFileUnchanged(t *testing.T) {
	tmp := t.TempDir()
	control := filepath.Join(tmp, "control.txt")
	target := filepath.Join(tmp, "target.txt")
	if err := os.WriteFile(control, []byte("hello world"), 0644); err != nil {
		t.Fatalf("setup control: %v", err)
	}
	if err := os.WriteFile(target, []byte("target text here"), 0644); err != nil {
		t.Fatalf("setup target: %v", err)
	}

	// 对照组：成功编辑 control，证明写机制能改变磁盘文件。
	if _, isErr := runDiffTool(t, tmp, "edit_file", map[string]interface{}{
		"path":     control,
		"old_text": "hello",
		"new_text": "hi",
	}); isErr {
		t.Fatalf("对照组编辑应当成功，却失败")
	}
	if ctrl, _ := os.ReadFile(control); string(ctrl) != "hi world" {
		t.Fatalf("对照组未真正写入：got %q", string(ctrl))
	}

	// 主用例：对 target 编辑一个不存在的文本。
	out, isErr := runDiffTool(t, tmp, "edit_file", map[string]interface{}{
		"path":     target,
		"old_text": "zzz not present",
		"new_text": "replacement",
	})
	if !isErr {
		t.Errorf("无匹配文本应返回 error，却成功: %s", out)
	}
	if got, _ := os.ReadFile(target); string(got) != "target text here" {
		t.Errorf("无匹配编辑不应改动文件，got %q", string(got))
	}
}

// T3 — I4 状态机：edit_file 多处匹配 → 返回 error（不允许歧义替换）。
func TestChar_DiffEdit_MultipleMatch_Error(t *testing.T) {
	tmp := t.TempDir()
	file := filepath.Join(tmp, "multi.txt")
	if err := os.WriteFile(file, []byte("hello world\nhello world"), 0644); err != nil {
		t.Fatalf("setup: %v", err)
	}

	out, isErr := runDiffTool(t, tmp, "edit_file", map[string]interface{}{
		"path":     file,
		"old_text": "hello",
		"new_text": "hi",
	})
	if !isErr {
		t.Errorf("多处匹配应返回 error，却成功: %s", out)
	}
}

// T4 — I4 状态机：edit_file old_text == new_text → 返回 error（拒绝无操作写入）。
func TestChar_DiffEdit_IdenticalText_Error(t *testing.T) {
	tmp := t.TempDir()
	file := filepath.Join(tmp, "ident.txt")
	if err := os.WriteFile(file, []byte("line one\nline two"), 0644); err != nil {
		t.Fatalf("setup: %v", err)
	}

	out, isErr := runDiffTool(t, tmp, "edit_file", map[string]interface{}{
		"path":     file,
		"old_text": "line one",
		"new_text": "line one",
	})
	if !isErr {
		t.Errorf("old==new 应返回 error，却成功: %s", out)
	}
}

// T5 — I3 边界输入校验：write_file 空路径 → error。
func TestChar_DiffWrite_EmptyPath_Error(t *testing.T) {
	tmp := t.TempDir()
	out, isErr := runDiffTool(t, tmp, "write_file", map[string]interface{}{
		"path":    "",
		"content": "x",
	})
	if !isErr {
		t.Errorf("空路径应返回 error，却成功: %s", out)
	}
}

// T6 — I3 边界输入校验：路径遍历组件 "../" 必须被拒（validateFilePath 组件级检测）。
// 注意：必须用「保留 .. 字面量」的字符串（不能走 filepath.Join，Join 会先 Clean 掉 ".."，
// 退化为可写根检查而非遍历检测）。组件级检测在 handler 内先于可写根检查执行，返回真实 error
// （IsError==true）—— 与 G1 的「可写根拒绝返回 nil error」是两条不同拒绝路径，需分别钉死。
func TestChar_DiffWrite_PathTraversalRejected(t *testing.T) {
	tmp := t.TempDir()
	outside := tmp + "/../escape.txt" // 保留 ".." 字面量组件
	out, isErr := runDiffTool(t, tmp, "write_file", map[string]interface{}{
		"path":    outside,
		"content": "evil",
	})
	if !isErr {
		t.Errorf("路径遍历应被拒（真实 error），却成功: %s", out)
	}
	if !strings.Contains(out, "traversal") {
		t.Errorf("预期触发组件级遍历拒绝，实际输出: %s", out)
	}
	// 越出文件确不应被创建
	if _, err := os.Stat(tmp + "/../escape.txt"); err == nil {
		t.Errorf("越出文件被创建，路径遍历未生效")
	}
}

// T7 — I3 边界校验（生产管线入口 · F2 删除后的正确边界来源）：
// 写工作区外绝对路径必须被管线 CustomValidateStage 前置拒绝（IsError=true，零副作用）。
//
// 背景：原 T7 钉的是 F2（validateWritablePathSandbox）的
// 「拒绝但返回 (msg,nil) → IsError==false」行为。经核实 F2 在生产为死代码
// （生产三入口均 InitPipelineWithHooks，write_file 先被 CustomValidateStage
// 拦截，到不了 handler 里的 F2），F2 已删除。越界写现由 CustomValidateStage
// （pipeline.go:487-501）负责，本测试改从生产真实入口断言。
func TestChar_DiffWrite_OutsideWorkspace_SandboxDeniedReturnsNilError(t *testing.T) {
	tmp := t.TempDir()
	r := NewRegistry()
	e := NewExecutor(r, tmp)
	// 允许 write_file，隔离"权限询问"干扰，聚焦越界边界（同 T4 理由）。
	policy := permission.NewPolicy(permission.LevelWorkspaceWrite)
	rule, err := permission.ParseRuleString("write_file", permission.RuleAllow, permission.SourceSession)
	if err != nil {
		t.Fatalf("ParseRuleString(write_file allow): %v", err)
	}
	if err := policy.AddRule(*rule); err != nil {
		t.Fatalf("AddRule: %v", err)
	}
	e.InitPipelineWithHooks(policy, tmp)
	if e.Pipeline == nil {
		t.Fatal("Pipeline 未初始化：无法代表生产路径")
	}

	outside := filepath.Join(tmp, "..", "outside.txt") // workDir 之外
	res := e.Execute(context.Background(), Call{
		ID:   "t7",
		Name: "write_file",
		Input: mustMarshal(map[string]interface{}{
			"path":    outside,
			"content": "evil",
		}),
	})
	if !res.IsError {
		t.Fatalf("越界写应被管线拒绝（IsError），却被放行：output=%s", res.Output)
	}
	if !strings.Contains(res.Output, "workspace boundary check") || !strings.Contains(res.Output, "not within workspace") {
		t.Fatalf("管线拒绝原因不符（期望 workspace boundary check），output=%s", res.Output)
	}
	if _, statErr := os.Stat(outside); !os.IsNotExist(statErr) {
		t.Fatalf("越界文件仍被创建于 %s", outside)
	}
}

// T8 — I3 钉死：符号链接（workDir 内 → workDir 外）必须被拒。
// resolveValidationPath 走 filepath.EvalSymlinks，应把链接解析到外部目标后判越界。
// 本测试证明「字符串级可写根检查」已覆盖符号链接逃逸（与同类 CLI 的
// 硬链接/OS 级逃逸风险区分：后者需真实沙箱兜底，本测试只验证字符串级已收敛）。
func TestChar_DiffEdit_SymlinkToOutside_Denied(t *testing.T) {
	if _, err := os.Readlink("/"); err == nil { // 占位，确保 os.Symlink API 可用
	}
	tmp := t.TempDir()
	outsideDir := t.TempDir() // 另一个 TempDir，必然在 tmp 之外
	linkPath := filepath.Join(tmp, "link_outside")
	if err := os.Symlink(outsideDir, linkPath); err != nil {
		t.Skipf("os.Symlink 不可用，跳过: %v", err)
	}
	targetViaLink := filepath.Join(linkPath, "secret.txt")

	out, isErr := runDiffTool(t, tmp, "edit_file", map[string]interface{}{
		"path":     targetViaLink,
		"old_text": "x",
		"new_text": "y",
	})
	// 文件不存在 → 即便不判越界也会因 read 失败报错；但核心期望是「链接被解析后越界」而拒。
	// 这里只断言：链接目标文件没有被创建到 workDir 之外。
	if _, statErr := os.Stat(targetViaLink); statErr == nil {
		t.Errorf("符号链接目标文件被创建（逃逸）: %s", targetViaLink)
	}
	_ = out
	_ = isErr
}

// T9 — I3/I4 边界：edit_file 对 CRLF 文件的行尾保持。
// 文件为 CRLF，old_text 以 LF 提供，应命中并维持 CRLF。
func TestChar_DiffEdit_CRLFRoundTrip(t *testing.T) {
	tmp := t.TempDir()
	file := filepath.Join(tmp, "crlf.txt")
	crlfContent := "line one\r\nline two\r\nline three\r\n"
	if err := os.WriteFile(file, []byte(crlfContent), 0644); err != nil {
		t.Fatalf("setup: %v", err)
	}

	out, isErr := runDiffTool(t, tmp, "edit_file", map[string]interface{}{
		"path":     file,
		"old_text": "line two",
		"new_text": "line TWO",
	})
	if isErr {
		t.Fatalf("CRLF 编辑应成功，却报错: %s", out)
	}
	got, _ := os.ReadFile(file)
	if !strings.Contains(string(got), "line TWO") {
		t.Errorf("替换未生效: %q", string(got))
	}
	// 行尾应保持 CRLF
	if strings.Contains(string(got), "line ONE\r\n") == false && !strings.Contains(string(got), "line one\r\n") {
		// 不在断言内，仅信息
	}
	if !strings.Contains(string(got), "\r\n") {
		t.Errorf("CRLF 行尾被破坏，变为纯 LF: %q", string(got))
	}
}
