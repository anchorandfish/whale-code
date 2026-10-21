package tools

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// 残余①「换行洗白」的 **E2E 判别器**（走生产入口：
// `InitPipelineWithHooks` → `ExecuteWithConfirm`，与 TUI 的 executeToolsDirect 同一条链）。
//
// 单元判据在 `internal/security/cmdrules/newline_segment_test.go`（钉判定本体）；
// 本文件钉**用户可见后果** —— 同一根因在管线里的两种表现：
//
//	① **免问面（静默放行）**：`cat x\nmkdir -p d` 被 的只读免问出口 (e) 吞掉
// ⇒ 命令**零确认执行**（用户粘贴一段两行脚本，第二行写盘，界面上不会问）；
//	② **deny 面（过度拦截）**：`Read(*)` 把「含写」的复合命令当成"读"拒绝
// ⇒ 用户拿到"读被禁"的拒绝理由，而实际被拦下的是一次**写**。
//
// 两个方向都源自 `cmdrules.Normalize` 曾把命令分隔符 `\n` 当书写空白折叠。
// 判别力：用 `-overlay` 把 `Normalize` 退回折叠版 ⇒ 本文件必须**精确打红**。

// ① 免问面：换行后的**第二行不是只读**，就不得被只读免问吞掉。
//
// 断言形态 = 「不得静默执行」（`Proceed && !NeedConfirm`）——
// 无论它落到"询问"还是"拒绝"都算通过；只有零确认执行才算失败。
func TestW025_E2E_NewlineSeparatedWriteMustNotBeSilentlyExempted(t *testing.T) {
	ctx := context.Background()
	workDir := t.TempDir()
	e, _ := w023Executor(t, workDir)

	cases := []struct{ name, cmd, why string }{
		{"read-then-mkdir", "cat go.mod\nmkdir -p " + filepath.Join(workDir, "d1"),
			"第二行是写（mkdir）"},
		{"comment-then-mkdir", "# tidy up\nmkdir -p " + filepath.Join(workDir, "d2"),
			"`#` 注释行把段首词顶成 `#`，掩盖了第二行的 mkdir"},
		{"crlf-read-then-mkdir", "cat go.mod\r\nmkdir -p " + filepath.Join(workDir, "d3"),
			"CRLF 同样是命令分隔"},
		{"read-then-rm", "cat go.mod\nrm -f " + filepath.Join(workDir, "nonexistent.txt"),
			"第二行是删（rm）"},
		{"read-then-tee", "cat go.mod\ntee " + filepath.Join(workDir, "copy.txt"),
			"第二行 tee 可写 ⇒ 不在只读白名单"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			pr := e.ExecuteWithConfirm(ctx, Call{ID: "w025-" + tc.name, Name: "bash", Input: bashInput(tc.cmd)})
			if pr.Proceed && !pr.NeedConfirm {
				t.Fatalf("W-025① 换行洗白（免问面）：%q 的第二行不是只读（%s），"+
					"却被只读免问出口静默放行 ⇒ 用户看到命令**零确认执行**。stageLogs=%+v",
					tc.cmd, tc.why, pr.StageLogs)
			}
			t.Logf("%-22s -> Proceed=%v NeedConfirm=%v Rejection=%q",
				tc.name, pr.Proceed, pr.NeedConfirm, pr.Rejection)
		})
	}
}

// 对照组（防「把带换行的命令一律拦下」的退化实现）：
// 换行两侧**都是**只读白名单成员 ⇒ 仍须免问，且**真的执行**（免问 ≠ 免执行）。
//
// 没有这一组，「一律不豁免」的假修复也能让 ① 全绿 —— 那是把 的功能删掉充数。
func TestW025_E2E_NewlineSeparatedAllReadOnlyStillExempted(t *testing.T) {
	ctx := context.Background()
	workDir := t.TempDir()
	e, _ := w023Executor(t, workDir)

	const marker = "w025_marker_file"
	if err := os.WriteFile(filepath.Join(workDir, marker), []byte("x"), 0o644); err != nil {
		t.Fatalf("precondition: 造标记文件失败: %v", err)
	}

	pr := e.ExecuteWithConfirm(ctx, Call{
		ID: "w025-ctrl", Name: "bash",
		Input: bashInput("pwd\nls " + workDir + "\nwhoami"),
	})
	if pr.NeedConfirm {
		t.Fatalf("W-025① 对照组：三行都是只读白名单成员（pwd/ls/whoami），应免确认；"+
			"stageLogs=%+v", pr.StageLogs)
	}
	if !pr.Proceed || pr.Result == nil {
		t.Fatalf("W-025① 对照组：免问 ≠ 免执行，命令必须真的跑；proceed=%v result=%+v",
			pr.Proceed, pr.Result)
	}
	if !strings.Contains(fmt.Sprintf("%v", pr.Result), marker) {
		t.Fatalf("W-025① 对照组：多行只读命令应真正执行并产出 `ls` 输出（应含 %q）；result=%v",
			marker, pr.Result)
	}
}

// ② deny 面：`Read(*)` **不得**把「含写」的复合命令当作读拒绝。
//
// 方向与 ① 相反：这里不是"漏拦"，是"**错拦**" ——
// 用户写 `Read(*)` 表达的是"别读文件"，而 `cat x\nmkdir d` 里那次调用**包含写**，
// 它应当落回 Exec 语义（照常询问），而不是被贴上"读被禁"的拒绝理由。
func TestW025_E2E_ReadDenyMustNotOverblockWriteContainingCompound(t *testing.T) {
	ctx := context.Background()
	workDir := t.TempDir()
	e := newProductionExecutor(t, workDir,
		`{"permissions":{"deny":["Read(*)"]},"priorityMode":"posix"}`)

	// 反例锚（必须仍被 Read(*) 拒）：**全只读**的换行复合命令 = 真的在读。
	pureRead := e.ExecuteWithConfirm(ctx, Call{
		ID: "w025-pure", Name: "bash", Input: bashInput("cat go.mod\nls -la"),
	})
	if pureRead.Proceed {
		t.Fatalf("W-025① 反例锚：`cat go.mod\\nls -la` 全段只读 ⇒ 应被 Read(*) 拒绝，" +
			"实际被放行（否则本用例不构成判别）")
	}
	if !strings.Contains(pureRead.Rejection, "deny rule") {
		t.Fatalf("W-025① 反例锚：拒绝理由应来自 deny 规则，实际 %q", pureRead.Rejection)
	}

	// 被测：含写 ⇒ 不是读 ⇒ 不得被"读被禁"拒绝
	mixed := e.ExecuteWithConfirm(ctx, Call{
		ID: "w025-mixed", Name: "bash",
		Input: bashInput("cat go.mod\nmkdir -p " + filepath.Join(workDir, "d")),
	})
	if strings.Contains(mixed.Rejection, "deny rule") {
		t.Fatalf("W-025① 换行洗白（deny 面）：`cat x\\nmkdir -p d` 含写，不是读，"+
			"不得被 Read(*) 当读拒绝；实际 Rejection=%q", mixed.Rejection)
	}
	t.Logf("mixed -> Proceed=%v NeedConfirm=%v Rejection=%q",
		mixed.Proceed, mixed.NeedConfirm, mixed.Rejection)
}
