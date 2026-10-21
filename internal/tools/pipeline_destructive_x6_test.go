// Package tools — 桶 X6 P-4 破坏性操作确认反例测试。
//
// 验证 DestructiveCheckStage 能正确检测破坏性操作并请求用户确认。
// 覆盖场景：
//   - bash 破坏性命令（rm/rmdir/mv/shred/mkfs/dd）触发 NeedConfirm
//   - sudo 前缀的破坏性命令仍被检测
//   - 多行命令中的破坏性命令被检测
//   - git reset --hard / clean -f / push --force 被检测
//   - PowerShell 破坏性 cmdlet（Remove-Item 等）被检测
//   - exit_worktree action=remove 被检测
//   - 非破坏性命令（ls/cat/echo）不触发 NeedConfirm
//   - 空命令/注释行不触发 NeedConfirm
package tools

import (
	"context"
	"testing"
)

// TestX6_DestructiveCheck_BashRm 验证 rm 命令触发用户确认。
// 反例：若 rm 不被检测，AI 可直接删除文件而无需用户确认。
func TestX6_DestructiveCheck_BashRm(t *testing.T) {
	stage := &DestructiveCheckStage{}
	call := Call{
		ID:    "1",
		Name:  "bash",
		Input: `{"command": "rm -rf /tmp/test"}`,
	}
	state := &PipelineState{}
	sr := stage.Execute(context.Background(), call, state)

	if !sr.Proceed {
		t.Error("should not block pipeline (Proceed=true with NeedConfirm)")
	}
	if !sr.NeedConfirm {
		t.Error("rm command should trigger NeedConfirm")
	}
	if !state.NeedConfirm {
		t.Error("state.NeedConfirm should be set")
	}
}

// TestX6_DestructiveCheck_BashRmdir 验证 rmdir 命令触发用户确认。
func TestX6_DestructiveCheck_BashRmdir(t *testing.T) {
	stage := &DestructiveCheckStage{}
	call := Call{
		ID:    "1",
		Name:  "bash",
		Input: `{"command": "rmdir /tmp/empty"}`,
	}
	sr := stage.Execute(context.Background(), call, &PipelineState{})

	if !sr.NeedConfirm {
		t.Error("rmdir command should trigger NeedConfirm")
	}
}

// TestX6_DestructiveCheck_BashMv 验证 mv 命令触发用户确认。
func TestX6_DestructiveCheck_BashMv(t *testing.T) {
	stage := &DestructiveCheckStage{}
	call := Call{
		ID:    "1",
		Name:  "bash",
		Input: `{"command": "mv important.txt /dev/null"}`,
	}
	sr := stage.Execute(context.Background(), call, &PipelineState{})

	if !sr.NeedConfirm {
		t.Error("mv command should trigger NeedConfirm")
	}
}

// TestX6_DestructiveCheck_BashShred 验证 shred 命令触发用户确认。
func TestX6_DestructiveCheck_BashShred(t *testing.T) {
	stage := &DestructiveCheckStage{}
	call := Call{
		ID:    "1",
		Name:  "bash",
		Input: `{"command": "shred secret.key"}`,
	}
	sr := stage.Execute(context.Background(), call, &PipelineState{})

	if !sr.NeedConfirm {
		t.Error("shred command should trigger NeedConfirm")
	}
}

// TestX6_DestructiveCheck_BashMkfs 验证 mkfs 命令触发用户确认。
func TestX6_DestructiveCheck_BashMkfs(t *testing.T) {
	stage := &DestructiveCheckStage{}
	call := Call{
		ID:    "1",
		Name:  "bash",
		Input: `{"command": "mkfs.ext4 /dev/sda1"}`,
	}
	sr := stage.Execute(context.Background(), call, &PipelineState{})

	if !sr.NeedConfirm {
		t.Error("mkfs command should trigger NeedConfirm")
	}
}

// TestX6_DestructiveCheck_BashDd 验证 dd 命令触发用户确认。
func TestX6_DestructiveCheck_BashDd(t *testing.T) {
	stage := &DestructiveCheckStage{}
	call := Call{
		ID:    "1",
		Name:  "bash",
		Input: `{"command": "dd if=/dev/zero of=/dev/sda"}`,
	}
	sr := stage.Execute(context.Background(), call, &PipelineState{})

	if !sr.NeedConfirm {
		t.Error("dd command should trigger NeedConfirm")
	}
}

// TestX6_DestructiveCheck_SudoPrefix 验证 sudo 前缀的破坏性命令仍被检测。
// 反例：若 sudo rm 不被检测，AI 可通过 sudo 绕过确认。
func TestX6_DestructiveCheck_SudoPrefix(t *testing.T) {
	stage := &DestructiveCheckStage{}
	call := Call{
		ID:    "1",
		Name:  "bash",
		Input: `{"command": "sudo rm -rf /etc"}`,
	}
	sr := stage.Execute(context.Background(), call, &PipelineState{})

	if !sr.NeedConfirm {
		t.Error("sudo rm should trigger NeedConfirm")
	}
}

// TestX6_DestructiveCheck_MultilineCommand 验证多行命令中的破坏性命令被检测。
// 反例：若仅检查首行，多行命令中的 rm 会被遗漏。
func TestX6_DestructiveCheck_MultilineCommand(t *testing.T) {
	stage := &DestructiveCheckStage{}
	call := Call{
		ID:    "1",
		Name:  "bash",
		Input: `{"command": "echo hello\nls -la\nrm -rf /tmp/data"}`,
	}
	sr := stage.Execute(context.Background(), call, &PipelineState{})

	if !sr.NeedConfirm {
		t.Error("rm in multiline command should trigger NeedConfirm")
	}
}

// TestX6_DestructiveCheck_GitResetHard 验证 git reset --hard 触发用户确认。
// 反例：若 git reset --hard 不被检测，AI 可直接丢弃工作区改动。
func TestX6_DestructiveCheck_GitResetHard(t *testing.T) {
	stage := &DestructiveCheckStage{}
	call := Call{
		ID:    "1",
		Name:  "bash",
		Input: `{"command": "git reset --hard HEAD~3"}`,
	}
	sr := stage.Execute(context.Background(), call, &PipelineState{})

	if !sr.NeedConfirm {
		t.Error("git reset --hard should trigger NeedConfirm")
	}
}

// TestX6_DestructiveCheck_GitCleanForce 验证 git clean -f 触发用户确认。
func TestX6_DestructiveCheck_GitCleanForce(t *testing.T) {
	stage := &DestructiveCheckStage{}
	call := Call{
		ID:    "1",
		Name:  "bash",
		Input: `{"command": "git clean -fd"}`,
	}
	sr := stage.Execute(context.Background(), call, &PipelineState{})

	if !sr.NeedConfirm {
		t.Error("git clean -fd should trigger NeedConfirm")
	}
}

// TestX6_DestructiveCheck_GitPushForce 验证 git push --force 触发用户确认。
func TestX6_DestructiveCheck_GitPushForce(t *testing.T) {
	stage := &DestructiveCheckStage{}
	call := Call{
		ID:    "1",
		Name:  "bash",
		Input: `{"command": "git push --force origin main"}`,
	}
	sr := stage.Execute(context.Background(), call, &PipelineState{})

	if !sr.NeedConfirm {
		t.Error("git push --force should trigger NeedConfirm")
	}
}

// TestX6_DestructiveCheck_PowerShellCmdlet 验证 PowerShell 破坏性 cmdlet 被检测。
// 反例：若 Remove-Item 不被检测，Windows 上 AI 可直接删除文件。
func TestX6_DestructiveCheck_PowerShellCmdlet(t *testing.T) {
	stage := &DestructiveCheckStage{}
	call := Call{
		ID:    "1",
		Name:  "bash",
		Input: `{"command": "Remove-Item -Recurse -Force C:\\temp"}`,
	}
	sr := stage.Execute(context.Background(), call, &PipelineState{})

	if !sr.NeedConfirm {
		t.Error("Remove-Item cmdlet should trigger NeedConfirm")
	}
}

// TestX6_DestructiveCheck_ExitWorktreeRemove 验证 exit_worktree action=remove 触发用户确认。
func TestX6_DestructiveCheck_ExitWorktreeRemove(t *testing.T) {
	stage := &DestructiveCheckStage{}
	call := Call{
		ID:    "1",
		Name:  "exit_worktree",
		Input: `{"action": "remove", "discard_changes": true}`,
	}
	sr := stage.Execute(context.Background(), call, &PipelineState{})

	if !sr.NeedConfirm {
		t.Error("exit_worktree with action=remove should trigger NeedConfirm")
	}
}

// TestX6_DestructiveCheck_ExitWorktreeKeep 验证 exit_worktree action=keep 不触发确认。
func TestX6_DestructiveCheck_ExitWorktreeKeep(t *testing.T) {
	stage := &DestructiveCheckStage{}
	call := Call{
		ID:    "1",
		Name:  "exit_worktree",
		Input: `{"action": "keep"}`,
	}
	sr := stage.Execute(context.Background(), call, &PipelineState{})

	if sr.NeedConfirm {
		t.Error("exit_worktree with action=keep should NOT trigger NeedConfirm")
	}
}

// TestX6_DestructiveCheck_SafeCommand 验证非破坏性命令不触发用户确认。
// 反例：若 ls/cat 等只读命令也触发确认，会过度打扰用户。
func TestX6_DestructiveCheck_SafeCommand(t *testing.T) {
	stage := &DestructiveCheckStage{}
	safeCommands := []string{
		`{"command": "ls -la"}`,
		`{"command": "cat file.txt"}`,
		`{"command": "echo hello"}`,
		`{"command": "git status"}`,
		`{"command": "git log --oneline"}`,
		`{"command": "grep pattern file"}`,
	}
	for _, input := range safeCommands {
		call := Call{ID: "1", Name: "bash", Input: input}
		state := &PipelineState{}
		sr := stage.Execute(context.Background(), call, state)
		if sr.NeedConfirm {
			t.Errorf("safe command should NOT trigger NeedConfirm: %s", input)
		}
		if state.NeedConfirm {
			t.Errorf("state.NeedConfirm should not be set for safe command: %s", input)
		}
	}
}

// TestX6_DestructiveCheck_EmptyCommand 验证空命令不触发确认也不报错。
func TestX6_DestructiveCheck_EmptyCommand(t *testing.T) {
	stage := &DestructiveCheckStage{}
	call := Call{
		ID:    "1",
		Name:  "bash",
		Input: `{"command": ""}`,
	}
	state := &PipelineState{}
	sr := stage.Execute(context.Background(), call, state)

	if !sr.Proceed {
		t.Error("empty command should proceed")
	}
	if sr.NeedConfirm {
		t.Error("empty command should NOT trigger NeedConfirm")
	}
}

// TestX6_DestructiveCheck_CommentOnly 验证仅含注释的命令不触发确认。
func TestX6_DestructiveCheck_CommentOnly(t *testing.T) {
	stage := &DestructiveCheckStage{}
	call := Call{
		ID:    "1",
		Name:  "bash",
		Input: `{"command": "# this is a comment\n# another comment"}`,
	}
	sr := stage.Execute(context.Background(), call, &PipelineState{})

	if sr.NeedConfirm {
		t.Error("comment-only command should NOT trigger NeedConfirm")
	}
}

// TestX6_DestructiveCheck_StageName 验证阶段名称正确。
func TestX6_DestructiveCheck_StageName(t *testing.T) {
	stage := &DestructiveCheckStage{}
	if stage.Name() != "destructive_check" {
		t.Errorf("expected stage name 'destructive_check', got %q", stage.Name())
	}
}

// TestX6_DestructiveCheck_CheckBashDestructive_Unit 单元测试 checkBashDestructive 函数。
func TestX6_DestructiveCheck_CheckBashDestructive_Unit(t *testing.T) {
	tests := []struct {
		name    string
		command string
		wantHit bool
	}{
		{"rm", "rm file.txt", true},
		{"sudo rm", "sudo rm -rf /", true},
		{"ls", "ls -la", false},
		{"cat", "cat file.txt", false},
		{"multiline with rm", "echo hi\nrm file\n", true},
		{"multiline safe", "echo hi\nls -la\n", false},
		{"git reset --hard", "git reset --hard HEAD", true},
		{"git status", "git status", false},
		{"Remove-Item", "Remove-Item file", true},
		{"empty", "", false},
		{"comment only", "# comment", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result, _ := checkBashDestructive(tt.command)
			if tt.wantHit && result == "" {
				t.Errorf("expected destructive detection for %q, got empty", tt.command)
			}
			if !tt.wantHit && result != "" {
				t.Errorf("expected no detection for %q, got %q", tt.command, result)
			}
		})
	}
}
