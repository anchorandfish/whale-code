package permission

import "testing"

// 阶段 2 给 danger-full-access 加了「危险命令二次确认」护栏。
//
// 这条护栏的成立前提是**有人在环能回答确认**。在无人值守环境（whale --ci）
// 里，确认请求无人应答：OnConfirm → promptConfirm 读 stdin 遇 EOF 返回 false
// → 被判为「用户取消」→ 命令静默失败。这比放行更糟：CI 里 `rm -rf dist`
// 这类清理步骤会从成功变成莫名失败。
//
// 因此护栏可由装配点显式关闭（见 plain.go RunCI）。本文件钉住这是一次
// **窄豁免**：只关闭「危险命令二次确认」，不触碰三档的能力边界（沙箱），
// 也不取消普通写操作的确认。

// TestDangerousCommandGuardrail_DefaultEnabled 默认有人在环：护栏生效。
func TestDangerousCommandGuardrail_DefaultEnabled(t *testing.T) {
	p := NewPolicy(LevelDangerFullAccess)
	if !p.DangerousCommandGuardrail() {
		t.Fatal("护栏默认应开启（有人在环）")
	}
	allowed, needConfirm := p.Check("bash", `{"command":"rm -rf /"}`)
	if !allowed || !needConfirm {
		t.Fatalf("danger + rm -rf / = (allowed=%v, needConfirm=%v), want (true,true)", allowed, needConfirm)
	}
}

// TestDangerousCommandGuardrail_DisabledInUnattendedCI 无人值守：CI 装配点关闭护栏后放行。
func TestDangerousCommandGuardrail_DisabledInUnattendedCI(t *testing.T) {
	p := NewPolicy(LevelDangerFullAccess)
	p.SetDangerousCommandGuardrail(false)
	if p.DangerousCommandGuardrail() {
		t.Fatal("SetDangerousCommandGuardrail(false) 未生效")
	}
	allowed, needConfirm := p.Check("bash", `{"command":"rm -rf /"}`)
	if !allowed {
		t.Fatalf("无人值守 CI 下危险命令应放行，allowed=%v", allowed)
	}
	if needConfirm {
		t.Fatalf("无人值守 CI 下无人应答，不应要求确认：needConfirm=%v", needConfirm)
	}
}

// TestDangerousCommandGuardrail_DisabledDoesNotLoosenReadOnly 窄豁免边界 1：
// 关闭护栏不得放宽 read-only 的能力边界（沙箱不能被审批豁免）。
func TestDangerousCommandGuardrail_DisabledDoesNotLoosenReadOnly(t *testing.T) {
	p := NewPolicy(LevelReadOnly)
	p.SetDangerousCommandGuardrail(false)

	if allowed, _ := p.Check("write_file", `{"path":"a.go","content":"x"}`); allowed {
		t.Error("豁免护栏不应放宽 read-only：write_file 仍须拒绝")
	}
	if allowed, _ := p.Check("bash", `{"command":"rm -rf /"}`); allowed {
		t.Error("豁免护栏不应放宽 read-only：危险 bash 仍须拒绝")
	}
}

// TestDangerousCommandGuardrail_DisabledDoesNotLoosenWorkspaceWriteConfirm 窄豁免边界 2：
// 关闭护栏只针对危险命令，不取消 workspace-write 的普通写确认。
//
// ⚠️ 由此产生一个已知残留：ci_permission 若设为 workspace-write，CI 里普通
// 写操作仍会 NeedConfirm → 无人应答 → 静默拒绝（阶段 2 之前即是如此，非本次引入）。
// 是否一并豁免属独立决策，见 PERMISSION_STAGE2_HANDOFF.md §9。
func TestDangerousCommandGuardrail_DisabledDoesNotLoosenWorkspaceWriteConfirm(t *testing.T) {
	p := NewPolicy(LevelWorkspaceWrite)
	p.SetDangerousCommandGuardrail(false)

	if _, needConfirm := p.Check("write_file", `{"path":"a.go","content":"x"}`); !needConfirm {
		t.Error("豁免护栏不应取消 workspace-write 的普通写确认")
	}
}

// TestDangerousCommandGuardrail_WorkspaceWriteAllowedToolStillGuarded 窄豁免边界 3：
// workspace-write 下已被 allowedTools 免问的工具，危险命令仍受护栏约束（有人在环时）。
func TestDangerousCommandGuardrail_WorkspaceWriteAllowedToolStillGuarded(t *testing.T) {
	p := NewPolicy(LevelWorkspaceWrite)
	p.SetAllowedTools([]string{"bash"})

	// 有人在环：即便 bash 被免问，危险命令仍要确认
	if _, needConfirm := p.Check("bash", `{"command":"rm -rf /"}`); !needConfirm {
		t.Error("有人在环时，被免问的 bash 遇危险命令仍应确认")
	}

	// 无人值守：护栏关闭后放行
	p.SetDangerousCommandGuardrail(false)
	if allowed, needConfirm := p.Check("bash", `{"command":"rm -rf /"}`); !allowed || needConfirm {
		t.Errorf("无人值守时危险命令应放行，got (allowed=%v, needConfirm=%v)", allowed, needConfirm)
	}
}
