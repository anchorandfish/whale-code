package tui

// security_preset_picker_test.go — `/security-preset` 上下键选择器的**生产路径**守卫。
//
// 与 `/permissions` 的三条同型测试**成对**（`app_test.go`：TestPermissionsCommandOpensModePicker /
// TestPermissionsModePickerEnterAppliesMode / TestPermissionsModePickerEscKeepsMode）：
// 两者共用同一套 PendingChoice 机制，任一条将来脱离该机制（例如自己另造一套菜单），
// 这里或那里就会红。
//
// 判别性（production-path-tdd）：本组**从用户真实入口进入** ——
// `m.handleCommand("/security-preset")` + 真实按键消息 `tea.KeyMsg`，
// 不手工构造 PendingChoice、不 mock 守卫。

import (
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/anchorandfish/whale-code/internal/permission"
)

func TestSecurityPresetCommandOpensPicker(t *testing.T) {
	m := newTUIRegressionModel(t)
	m.toolService.Policy().SetMode(permission.LevelWorkspaceWrite)

	output := m.handleCommand("/security-preset")

	// 回执主行**复用 securityPresetCurrentLine**（与 listing / plain 同源）。
	level := m.toolService.Policy().Mode()
	sandboxOn := sandboxCapabilityOn(m.toolService.Executor())
	if cur, ok := permission.MatchSecurityPreset(level, sandboxOn); ok {
		if !strings.Contains(output, "Current: "+cur.ID) {
			t.Fatalf("回执应报当前预设 %q，actual %q", cur.ID, output)
		}
	} else if !strings.Contains(output, "Current: Custom") {
		t.Fatalf("两轴无匹配时应报 Custom，actual %q", output)
	}

	if m.sessionService.State() != StateWaitingChoice {
		t.Fatalf("/security-preset 应进入选择状态，actual %v", m.sessionService.State())
	}
	pending := m.toolService.PendingChoice()
	if pending == nil {
		t.Fatal("/security-preset 应创建 PendingChoice")
	}
	if pending.ToolID != securityPresetChoiceToolID {
		t.Fatalf("应为专用选择（不是 AskUser 选择），actual ToolID=%q", pending.ToolID)
	}
	if len(pending.Options) != 3 {
		t.Fatalf("应有 3 个内置预设，actual %d", len(pending.Options))
	}

	// 默认高亮必须与 permission.MatchSecurityPreset 的判定一致（I4：两轴取合取）。
	gotID := pending.Options[m.toolService.ChoiceIdx()].Value
	if cur, ok := permission.MatchSecurityPreset(level, sandboxOn); ok {
		if gotID != cur.ID {
			t.Fatalf("当前状态匹配预设 %q，默认高亮应为它，actual %q", cur.ID, gotID)
		}
	} else if gotID == "" {
		t.Fatal("无匹配（Custom）时也必须高亮某项，不得为空")
	}
}

func TestSecurityPresetPickerEnterAppliesBothAxes(t *testing.T) {
	m := newTUIRegressionModel(t)
	m.toolService.Policy().SetMode(permission.LevelWorkspaceWrite)
	m.handleCommand("/security-preset")

	pending := m.toolService.PendingChoice()
	if pending == nil {
		t.Fatal("前置失败：选择器未打开")
	}
	// 显式选中 unrestricted，避免依赖默认高亮位置。
	idx := -1
	for i, o := range pending.Options {
		if o.Value == "unrestricted" {
			idx = i
		}
	}
	if idx < 0 {
		t.Fatal("前置失败：选项里没有 unrestricted")
	}
	m.toolService.SetChoiceIdx(idx)

	updatedModel, cmd := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	updated := updatedModel.(Model)
	updated.reassignServices()
	if cmd == nil {
		t.Fatal("Enter 应返回选择结果命令")
	}
	result, ok := cmd().(choiceResultMsg)
	if !ok || result.ToolID != securityPresetChoiceToolID || result.Cancelled || result.Answer != "unrestricted" {
		t.Fatalf("Enter 结果错误：%#v", result)
	}

	updatedModel, _ = updated.Update(result)
	updated = updatedModel.(Model)
	updated.reassignServices()

	// ★ 两轴**都必须**被写入 —— 这是 I2 对"双轴写入面"的核心声明。
	if got := updated.toolService.Policy().Mode(); got != permission.LevelDangerFullAccess {
		t.Fatalf("审批轴应为 danger-full-access，actual %s", got)
	}
	if sandboxCapabilityOn(updated.toolService.Executor()) {
		t.Fatal("能力轴应被关闭（unrestricted ⇒ sandbox off）")
	}
	if updated.sessionService.State() != StateIdle {
		t.Fatalf("应用后应回到 Idle，actual %v", updated.sessionService.State())
	}
}

func TestSecurityPresetPickerEscKeepsBothAxes(t *testing.T) {
	m := newTUIRegressionModel(t)
	m.toolService.Policy().SetMode(permission.LevelReadOnly)
	m.handleCommand("/security-preset")

	beforeMode := m.toolService.Policy().Mode()
	beforeSandbox := sandboxCapabilityOn(m.toolService.Executor())

	updatedModel, cmd := m.Update(tea.KeyMsg{Type: tea.KeyEsc})
	updated := updatedModel.(Model)
	updated.reassignServices()
	if cmd == nil {
		t.Fatal("Esc 应返回取消结果命令")
	}
	result, ok := cmd().(choiceResultMsg)
	if !ok || result.ToolID != securityPresetChoiceToolID || !result.Cancelled {
		t.Fatalf("Esc 结果错误：%#v", result)
	}

	updatedModel, _ = updated.Update(result)
	updated = updatedModel.(Model)
	updated.reassignServices()

	if got := updated.toolService.Policy().Mode(); got != beforeMode {
		t.Fatalf("Esc 不应改审批轴，actual %s（before %s）", got, beforeMode)
	}
	if got := sandboxCapabilityOn(updated.toolService.Executor()); got != beforeSandbox {
		t.Fatalf("Esc 不应改能力轴，actual %v（before %v）", got, beforeSandbox)
	}
	if updated.sessionService.State() != StateIdle {
		t.Fatalf("取消后应回到 Idle，actual %v", updated.sessionService.State())
	}
}

// TestSecurityPresetPickerArrowKeysMove 上下键必须能移动高亮（用户点名的诉求）。
func TestSecurityPresetPickerArrowKeysMove(t *testing.T) {
	m := newTUIRegressionModel(t)
	m.toolService.Policy().SetMode(permission.LevelWorkspaceWrite)
	m.handleCommand("/security-preset")

	start := m.toolService.ChoiceIdx()

	// ↓ 下移一项。
	next, _ := m.Update(tea.KeyMsg{Type: tea.KeyDown})
	cur := next.(Model)
	cur.reassignServices()
	if got := cur.toolService.ChoiceIdx(); got != start+1 {
		t.Fatalf("↓ 应下移一项：want %d, got %d", start+1, got)
	}

	// ↑ 回到原位。
	next, _ = cur.Update(tea.KeyMsg{Type: tea.KeyUp})
	cur = next.(Model)
	cur.reassignServices()
	if got := cur.toolService.ChoiceIdx(); got != start {
		t.Fatalf("↑ 应回到原位：want %d, got %d", start, got)
	}

	// 边界：到顶后继续按 ↑ 不得越界（也不得 panic）。
	for i := 0; i < 3; i++ {
		next, _ = cur.Update(tea.KeyMsg{Type: tea.KeyUp})
		cur = next.(Model)
		cur.reassignServices()
	}
	if got := cur.toolService.ChoiceIdx(); got != 0 {
		t.Fatalf("已在顶部时 ↑ 应停在 0，actual %d", got)
	}
}
