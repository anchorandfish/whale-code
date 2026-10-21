package permission

import "testing"

// TestWriteToolSet_IsExactAndOnlyDeclaration 钉住「目标受限写类工具」集合的**准确成员**。
//
// ★ 判别性（本测试是 B1 第二半的**永久判别器**）：
//   - 往 `WriteToolSet` 加/删任一成员 ⇒ 本测试红（成员数 / 逐条核对）；
//   - 把任意命令类（bash/powershell/monitor）**并进**表 ⇒ 本测试红（安全回退）；
//   - 让 `internal/tools/pipeline.go` 重新长出本地名单 ⇒ tools 侧的
//     `TestFix_IsWriteTool_SingleDeclarationBothSidesAgree` 红（两侧逐工具答案会分叉）。
func TestWriteToolSet_IsExactAndOnlyDeclaration(t *testing.T) {
	want := map[string]bool{
		"write_file": true, "edit_file": true, "notebookedit": true,
		"lsprename": true, "git_commit": true, "git_add": true, "git_branch": true,
		"export_session": true, "apply_unified_diff": true,
	}
	if len(WriteToolSet) != len(want) {
		t.Fatalf("WriteToolSet 成员数 = %d，期望 %d；当前表 = %v", len(WriteToolSet), len(want), WriteToolSet)
	}
	for name := range want {
		if !WriteToolSet[name] {
			t.Errorf("WriteToolSet 缺 %q", name)
		}
	}

	// ★ 任意命令类**必须在表外**（并入 ⇒ 边界闸/出口 (a) 免问会对 bash 放行 = 安全回退）。
	for _, arbi := range []string{"bash", "powershell", "monitor", "Bash", "PowerShell", "Monitor"} {
		if IsWriteTool(arbi) {
			t.Errorf("%q 是任意命令类，不得进「目标受限写类工具」表（入参界定不了它的可达面）", arbi)
		}
	}

	// 大小写归一化：两种写法必须同答案（收敛前 pipeline 侧是精确匹配 ⇒ `notebookedit` 会漏判）。
	if IsWriteTool("NotebookEdit") != IsWriteTool("notebookedit") {
		t.Error("大小写应归一化：NotebookEdit / notebookedit 必须同答案")
	}
	if IsWriteTool("LspRename") != IsWriteTool("lsprename") {
		t.Error("大小写应归一化：LspRename / lsprename 必须同答案")
	}
}

// TestWriteToolSet_RemovedMembersAreInert 证明「删 bash / powershell」**不改变判定** ——
// 不是靠推理，而是实测其在本谓词**唯一生产调用点**（`SafetyCheckStage`）的输入提取结果。
//
// `SafetyCheckStage` 的链路 = `IsWriteTool` → `ExtractFilePath(input)` → `CheckPathSafety(path)`：
// 只有当 `ExtractFilePath` 能取到**非空**路径时，本谓词的取值才可能影响结果。
// 对 bash / powershell，任何命令串都取不到 `file_path` / `path`（`ExtractFilePath` 刻意不读
// `command`，理由见其文档）⇒ `CheckPathSafety("")` 直接返回 nil ⇒ 阶段照样 `Proceed`。
//
// ⇒ 若将来有人让 `ExtractFilePath` 开始读 `command`，**本测试会红**：那正是"删 bash 不再是
// 中性改动"的时刻，必须回来重新拍板「bash 到底算不算目标受限写类工具」。
func TestWriteToolSet_RemovedMembersAreInert(t *testing.T) {
	for _, in := range []string{
		`{"command":"rm -rf /tmp/x"}`,
		`{"command":"echo hi > /Users/x/.ssh/authorized_keys"}`,
		`{"command":"cat /etc/passwd"}`,
		`{"command":"powershell -c \"Remove-Item C:\\\\x -Recurse\""}`,
		`{}`,
	} {
		if got := ExtractFilePath(in); got != "" {
			t.Errorf("ExtractFilePath(%s) = %q，期望空串 —— 不为空则「删 bash」不再是中性改动", in, got)
		}
	}
	if CheckPathSafety("", "/tmp") != nil {
		t.Error(`CheckPathSafety("") 必须返回 nil：否则空路径会改变 SafetyCheckStage 的结果`)
	}
}
