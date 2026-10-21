// completion_extended_test.go — 多层子命令补全测试（M09-03 改造，）
package command

import (
	"testing"
)

// TestCompletion_2Level_Legacy 测试旧 2 级补全兼容（无 Children）
func TestCompletion_2Level_Legacy(t *testing.T) {
	r := newTestRegistry()

	// /plugin in → install（旧 2 级补全，无 Children）
	matches := r.Completion("/plugin in")
	if len(matches) != 1 || matches[0] != "install" {
		t.Errorf("expected ['install'], got %v", matches)
	}
}

// TestCompletion_3Level_Children 测试 3 级补全 /pr create draft
func TestCompletion_3Level_Children(t *testing.T) {
	r := newTestRegistry()

	// /pr create d → draft（Children 多层补全）
	matches := r.Completion("/pr create d")
	if len(matches) != 1 {
		t.Fatalf("expected 1 match for '/pr create d', got %v", matches)
	}
	if matches[0] != "draft" {
		t.Errorf("expected 'draft', got %s", matches[0])
	}
}

// TestCompletion_3Level_Children_All 测试 3 级补全返回所有子命令（通过空前缀）
func TestCompletion_3Level_Children_All(t *testing.T) {
	r := newTestRegistry()

	// /pr create d → draft
	matches := r.Completion("/pr create d")
	if len(matches) != 1 || matches[0] != "draft" {
		t.Errorf("expected ['draft'] for '/pr create d', got %v", matches)
	}
	// /pr create r → ready
	matches = r.Completion("/pr create r")
	if len(matches) != 1 || matches[0] != "ready" {
		t.Errorf("expected ['ready'] for '/pr create r', got %v", matches)
	}
}

// TestCompletion_NoChildren_Fallback 测试无 Children 走 SubCommands → ArgCompleter（OPT-06）
func TestCompletion_NoChildren_Fallback(t *testing.T) {
	r := newTestRegistry()

	// /plugin i → install（SubCommands 过滤）
	matches := r.Completion("/plugin i")
	if len(matches) != 1 || matches[0] != "install" {
		t.Errorf("expected ['install'] for '/plugin i', got %v", matches)
	}
	// /plugin e → enable（5维度审查修复：SubCommands 改为 install/enable/disable/list）
	matches = r.Completion("/plugin e")
	if len(matches) != 1 || matches[0] != "enable" {
		t.Errorf("expected ['enable'] for '/plugin e', got %v", matches)
	}
	// /plugin d → disable
	matches = r.Completion("/plugin d")
	if len(matches) != 1 || matches[0] != "disable" {
		t.Errorf("expected ['disable'] for '/plugin d', got %v", matches)
	}
}

// TestCompletion_SubCommandsPriority 测试 SubCommands 优先于 Children keys（OPT-06）
func TestCompletion_SubCommandsPriority(t *testing.T) {
	r := newTestRegistry()

	// pr 有 SubCommands [create review list merge] + Children {create: ...}
	// /pr c → 应匹配 SubCommands 中的 create（而非 Children keys）
	matches := r.Completion("/pr c")
	if len(matches) != 1 {
		t.Fatalf("expected 1 match for '/pr c', got %v", matches)
	}
	if matches[0] != "create" {
		t.Errorf("expected 'create' from SubCommands (priority), got %s", matches[0])
	}
}

// TestCompletion_Deep_NoMatch 测试深层无匹配返回 nil
func TestCompletion_Deep_NoMatch(t *testing.T) {
	r := newTestRegistry()

	// /pr nonexistent x → Children 中找不到 nonexistent → 返回 nil
	matches := r.Completion("/pr nonexistent x")
	if matches != nil {
		t.Errorf("expected nil for deep no-match, got %v", matches)
	}
}

// TestCompletion_Deep_ChildrenNoMatch 测试 Children 中无匹配的子命令
func TestCompletion_Deep_ChildrenNoMatch(t *testing.T) {
	r := newTestRegistry()

	// /pr create x → Children create 的 SubCommands [draft ready]，x 无匹配 → 返回 nil
	matches := r.Completion("/pr create x")
	if matches != nil {
		t.Errorf("expected nil for '/pr create x', got %v", matches)
	}
}

// TestCompletion_NoCommand 测试不存在的命令
func TestCompletion_NoCommand(t *testing.T) {
	r := newTestRegistry()

	matches := r.Completion("/nonexistent sub")
	if matches != nil {
		t.Errorf("expected nil for nonexistent command, got %v", matches)
	}
}

// TestCompletion_EmptyInput 测试空输入
func TestCompletion_EmptyInput(t *testing.T) {
	r := newTestRegistry()

	matches := r.Completion("")
	// 空输入应返回所有命令
	if len(matches) == 0 {
		t.Error("expected non-empty matches for empty input, got empty")
	}
}

// TestCompletion_SingleSlash 测试仅 / 输入
func TestCompletion_SingleSlash(t *testing.T) {
	r := newTestRegistry()

	matches := r.Completion("/")
	// 仅 / 应返回所有命令
	if len(matches) == 0 {
		t.Error("expected non-empty matches for '/' input, got empty")
	}
}

// TestCompletion_Children_NilSafe 测试旧条目无 Children 字段不会 panic
func TestCompletion_Children_NilSafe(t *testing.T) {
	r := newTestRegistry()

	// /plugin install sub → plugin 有 SubCommands 但无 Children
	// "install" 是有效 SubCommand，subCommandConfirmed=true，跳过 SubCommands
	// plugin 有 ArgCompleter，返回 ArgCompleter 结果（不会 panic）
	matches := r.Completion("/plugin install sub")
	// 不应 panic，且应返回 ArgCompleter 的结果（plugin 有 ArgCompleter）
	if matches == nil {
		t.Error("expected non-nil matches (ArgCompleter fallback), got nil")
	}
}

// TestCompletion_Children_NilSafe_NoArgCompleter 测试无 Children 无 ArgCompleter 时不 panic
func TestCompletion_Children_NilSafe_NoArgCompleter(t *testing.T) {
	r := newTestRegistry()

	// /daemon start sub → daemon 有 SubCommands 但无 Children 无 ArgCompleter
	// "start" 是有效 SubCommand，subCommandConfirmed=true，跳过 SubCommands
	// 无 ArgCompleter → 返回 nil（不会 panic）
	matches := r.Completion("/daemon start sub")
	if matches != nil {
		t.Errorf("expected nil for '/daemon start sub' (no ArgCompleter), got %v", matches)
	}
}
