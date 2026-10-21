package tui

import (
	"strings"
	"testing"

	"github.com/anchorandfish/whale-code/internal/session"
	tea "github.com/charmbracelet/bubbletea"
)

// isolatedHomeModel 在临时 HOME 下构造 Model，使 session store 落在临时目录。
//
// 为什么必须隔离：NewModel 用 os.UserHomeDir() 拼 ~/.whale/sessions，直接跑测试
// 会把会话文件写进**真实用户目录**（探针不得写真实用户文件）。
// Unix 上 os.UserHomeDir() 读 $HOME，因此 t.Setenv 是干净且最小的隔离手段。
//
// 为什么**不用 t.TempDir()**：NewModel 启动的后台 goroutine（filehistory persist /
// usage tracker / wiring）在用例返回后仍可能向 HOME 写文件，与 TempDir 注册的
// RemoveAll 清理断言竞态 ⇒ `TempDir RemoveAll cleanup: unlinkat .../.whale:
// directory not empty`（环境性失败类，曾被误读为逻辑回归）。
// 复用 resume_entry_test.go 的 newIsolatedTestHome：MkdirTemp + 忽略错误的清理。
//
// 为什么必须用返回 *Model 的构造器：`reassignServices` 把
// `sessionService`/`toolService` 指回**接收者自身**的嵌入结构体
// （`service.go:7`：`m.sessionService = &m.SessionModel`）。若构造器按值返回 Model，
// 接口字段指向的就是**被丢弃的那个局部变量**的嵌入结构体，与调用方手上的副本脱钩。
// 实测症状：用值构造器时 `handleSessionsPicker` 明明执行了
// SetPendingChoice/SetState，调用方读回来仍是零值/nil（见
// TestModelValueCopyBreaksServiceAliasing 固化的事实）。改用返回 *Model 的
// newTUIRegressionModel 后一致（这也是仓库既有做法：tui_regression_test.go 同款）。
func isolatedHomeModel(t *testing.T) *Model {
	t.Helper()
	home := newIsolatedTestHome(t)
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	return newTUIRegressionModel(t)
}

// TestModelValueCopyBreaksServiceAliasing 固化一个真实陷阱的**事实**（不解释成因）：
//
//	按值返回 Model 的构造器 ⇒ `m.sessionService` 与 `&m.SessionModel` **不是**同一对象；
//	返回 *Model 的构造器 ⇒ 是同一对象。
//
// 后果是：用值构造器写「调用 handler → 读回 State()/PendingChoice()」的测试，
// 读到的可能是另一个对象上的零值（本会话就踩过一次，误判成实现没生效）。
// 本用例让后来者一眼看到该用哪个构造器，而不必自己踩一遍。
func TestModelValueCopyBreaksServiceAliasing(t *testing.T) {
	home := newIsolatedTestHome(t) // 同上：避开 TempDir 清理竞态
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)

	byValue := newApprovalTestModel(t) // 返回 Model（值）
	valAliased := any(byValue.sessionService) == any(&byValue.SessionModel)
	t.Logf("值构造器：sessionService == &m.SessionModel ? %v", valAliased)

	byPtr := newTUIRegressionModel(t) // 返回 *Model
	ptrAliased := any(byPtr.sessionService) == any(&byPtr.SessionModel)
	t.Logf("指针构造器：sessionService == &m.SessionModel ? %v", ptrAliased)

	if !ptrAliased {
		t.Fatal("指针构造器的服务别名应成立（否则「写状态再读回」的测试全部不可信）")
	}
	if valAliased {
		t.Log("注：值构造器的别名也成立了 —— 说明 Model 结构或" +
			"reassignServices 的语义变了，本用例与依赖它的注释需一并复核")
	}
}

// TestSessionPicker_OpensChoiceWithAliases 裸 /session 必须打开交互式选择器，
// 且每个选项的可读别名与主键都能指认到真实会话。
func TestSessionPicker_OpensChoiceWithAliases(t *testing.T) {
	m := isolatedHomeModel(t)
	store := m.sessionService.Store()
	if store == nil {
		t.Fatal("前置条件：Model 应持有 session store")
	}

	type seed struct {
		id    string
		alias string
	}
	seeds := []seed{
		{session.GenerateID(), "登录重构"},
		{session.GenerateID(), "接口联调"},
	}
	for _, s := range seeds {
		if err := store.AppendBatchNamed(s.id, s.alias, []session.Message{
			session.UserText("首条输入 " + s.alias),
		}); err != nil {
			t.Fatalf("seed %s: %v", s.alias, err)
		}
	}

	out := m.handleSessionsPicker()
	if m.sessionService.State() != StateWaitingChoice {
		t.Fatalf("选择器应进入 StateWaitingChoice，实际 %v（out=%q）", m.sessionService.State(), out)
	}
	pc := m.toolService.PendingChoice()
	if pc == nil {
		t.Fatal("PendingChoice 未设置")
	}
	if pc.ToolID != sessionPickerChoiceToolID {
		t.Errorf("ToolID = %q, want %q", pc.ToolID, sessionPickerChoiceToolID)
	}
	if len(pc.Options) != len(seeds) {
		t.Fatalf("选项数 = %d, want %d", len(pc.Options), len(seeds))
	}

	// 每个选项的 Value 必须是**主键**（可用于 Resume/switch），Label 必须含别名。
	byValue := make(map[string]ChoiceOption, len(pc.Options))
	for _, o := range pc.Options {
		byValue[o.Value] = o
	}
	for _, s := range seeds {
		o, ok := byValue[s.id]
		if !ok {
			t.Errorf("选项 Value 未使用会话主键 %q；实际选项：%+v", s.id, pc.Options)
			continue
		}
		if !strings.Contains(o.Label, s.alias) {
			t.Errorf("选项 Label 未包含别名 %q：%q", s.alias, o.Label)
		}
	}
}

// TestSessionPicker_SelectedValueLoadsTargetSession 选中项的 Value 必须真的能载入那个会话。
//
// 这是端到端判别器：把 Value 从「主键」改成「别名」（很容易犯的错）本用例即转红，
// 因为别名不是文件名，Load 会失败。
func TestSessionPicker_SelectedValueLoadsTargetSession(t *testing.T) {
	m := isolatedHomeModel(t)
	store := m.sessionService.Store()
	if store == nil {
		t.Fatal("前置条件：Model 应持有 session store")
	}

	target := session.GenerateID()
	if err := store.AppendBatchNamed(target, "目标会话", []session.Message{
		session.UserText("目标会话"),
	}); err != nil {
		t.Fatalf("seed: %v", err)
	}

	m.handleSessionsPicker()
	pc := m.toolService.PendingChoice()
	if pc == nil || len(pc.Options) == 0 {
		t.Fatal("选择器未产生选项")
	}

	// 直接走生产载入路径（与用户按 Enter 后 choiceResultMsg 分支调的是同一个方法）。
	if out := m.handleSessionSwitch(pc.Options[0].Value); out != "" {
		t.Fatalf("handleSessionSwitch 失败: %s", out)
	}
	if got := m.sessionService.SessionName(); got != target {
		t.Errorf("载入后 SessionName = %q, want %q", got, target)
	}
	if got := len(m.sessionService.CurrentSession().GetMessages()); got != 1 {
		t.Errorf("载入后消息数 = %d, want 1", got)
	}
}

// TestSessionPickerResolvesByAliasAndPrefix 选择器之外，三条入口都要能按别名/短码命中。
//
// /session switch、/resume 共用 Store.Resolve，本用例锁定它们确实用了同一个解析器。
func TestSessionPickerResolvesByAliasAndPrefix(t *testing.T) {
	m := isolatedHomeModel(t)
	store := m.sessionService.Store()
	if store == nil {
		t.Fatal("前置条件：Model 应持有 session store")
	}

	target := session.GenerateID()
	if err := store.AppendBatchNamed(target, "部署排障", []session.Message{
		session.UserText("部署排障"),
	}); err != nil {
		t.Fatalf("seed: %v", err)
	}

	for _, in := range []string{"部署排障", target[:8], target} {
		m.sessionService.SetSessionName("")
		if out := m.handleSessionSwitch(in); out != "" {
			t.Errorf("/session switch %q 失败: %s", in, out)
			continue
		}
		if got := m.sessionService.SessionName(); got != target {
			t.Errorf("/session switch %q 载入了 %q, want %q", in, got, target)
		}
	}
}

// TestSessionCommandHandledLocally 裸 /session 与 /session pick 必须在本地处理名单里，
// 否则会先跑 Registry handler（返回 __SESSION_PICK__ 魔法串）再被本地分支覆盖。
func TestSessionCommandHandledLocally(t *testing.T) {
	for _, name := range []string{"session", "session pick", "session list", "session switch"} {
		if !isTUICommandHandledLocally(name) {
			t.Errorf("%q 应由 TUI 本地处理（否则魔法字符串会泄露给用户）", name)
		}
	}
}

// TestSessionCommandDispatchOpensPicker 走**生产分发入口** handleCommand：
// 用户敲 `/session` 必须打开选择器，而不是打印 help / 报未知命令 / 泄露魔法串。
//
// 这是接线判别器：把 app.go 的 `case "session"` 摘掉（或让
// isTUICommandHandledLocally 漏掉它）本用例即转红。
func TestSessionCommandDispatchOpensPicker(t *testing.T) {
	m := isolatedHomeModel(t)
	store := m.sessionService.Store()
	if store == nil {
		t.Fatal("前置条件：Model 应持有 session store")
	}
	if err := store.AppendBatchNamed(session.GenerateID(), "分发测试", []session.Message{
		session.UserText("分发测试"),
	}); err != nil {
		t.Fatalf("seed: %v", err)
	}

	out := m.handleCommand("/session")
	if m.sessionService.State() != StateWaitingChoice {
		t.Fatalf("/session 应进入选择状态，实际 %v（out=%q）", m.sessionService.State(), out)
	}
	pc := m.toolService.PendingChoice()
	if pc == nil || pc.ToolID != sessionPickerChoiceToolID {
		t.Fatalf("/session 应打开会话选择器，实际 PendingChoice=%+v（out=%q）", pc, out)
	}
	if strings.Contains(out, "__SESSION_PICK__") {
		t.Fatalf("魔法字符串泄露给用户：%q", out)
	}
}

// TestTabCompletedSessionCommandIsExecutable 「Tab 补出来的字符串必须是活输入」。
//
// 背景：可见的已注册父命令（如 /checkpoint）`/chec` + Tab 会补成 `"/checkpoint "`（带分隔空格）。
// 若命令分发不去掉尾随空白，这个补全结果就变成**死输入**：用户 Tab 补全后回车 → 报未知命令。
// 本用例不手写命令串，而是把**补全的真实产物**喂进生产分发入口，断言它真的被执行。
// ★ ：载体从 `/session` 换成 `/checkpoint` —— session 根已收回隐藏
//
//	（v1 无承诺，Tab 不再补出它）；「补全产物可执行」的判别面不变，只换仍可见的载体。
//
// 判别性：让 command.Parse 不再 TrimSpace（或补全产物带空格后不规范化）即转红。
func TestTabCompletedSessionCommandIsExecutable(t *testing.T) {
	m := isolatedHomeModel(t)

	m.textarea.SetValue("/chec")
	m.updateTypeaheadSuggestions()
	// ⚠️ `Model.Update` 是**值接收者**（app.go:1207）⇒ 只改副本并返回副本，
	// 补全结果要从**副本**上读（读 `m` 只会看到 "/chec"）。
	updatedModel, _ := m.Update(tea.KeyMsg{Type: tea.KeyTab})
	completed := updatedModel.(Model).textarea.Value()
	if completed != "/checkpoint " {
		t.Fatalf("前置条件：Tab 应补出带分隔空格的 \"/checkpoint \"，实际 %q", completed)
	}

	// ⚠️ 但**分发必须回到存活的 `m` 上做**，不能在 `updated` 上做：`handleCommand` 内部
	// 会再次 `reassignServices()`，把服务接口指回**它自己那个副本**的子结构，
	// 于是写在 `updated` 上的状态会留在被丢弃的对象里（实测：out 正常但 State()==0、
	// PendingChoice()==nil）。这与 TestSessionCommandDispatchOpensPicker 同款取法。
	out := m.handleCommand(completed)
	if strings.Contains(out, "Unknown") {
		t.Fatalf("Tab 补全产物必须可执行（不得报未知命令），out=%q", out)
	}
	if strings.Contains(out, "__") && strings.Contains(out, "__") {
		t.Fatalf("魔法字符串泄露给用户：%q", out)
	}
}
