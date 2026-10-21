package tui

// askuser_other_semantics_test.go — AskUser「Other / 自由文本」语义守卫（2026-09-23 ·）
//
// # 起因（真机现象 + 最硬证据）
//
// 用户选 **Other（None of the above）** 并输入 `回复hello`，模型却答
// 「你好！不过你刚才选择的是 "JWT"」。
// 会话存档 `~/.whale/sessions/01a0c506-4b96-7f3c-8e18-9df4656f49eb.jsonl` 证明**回传是对的**：
//
//	L15 assistant tool_use input = {options:[JWT,Session], default_idx:0, allow_free_text:false}
//	L16 tool_result output = "回复hello" ← 回传里没有任何 JWT
//	L17 assistant text "你刚才选择的是 \"JWT\"" ← 模型自己猜的
//
// 模型为什么猜：它收到的是**无来源标注的裸文本**，无从判断这是「以上都不是」的自填内容，
// 只好退回自己入参里的 `default_idx: 0` ⇒ 选项 0 = JWT。
//
// # 修法（(a)(c) 保留 · (b) 已撤回）与各自的判别器
//
//	(a) 选 Other 时回传**标签本身**（对齐 同类 CLI answers[0] = OTHER_OPTION_LABEL）
// ⇒ TestN132_OtherKeepsSentinelLabel
//	(b) ~~尊重模型入参 `allow_free_text`：false ⇒ 不追加 Other~~ **2026-09-23 `` 已撤回**：
// 该字段并不是「模型可以关掉自填口」的开关 —— 同类 CLI 的 `is_other` 由服务端强制 true
// （`core/src/tools/handlers/request_user_input_spec.rs:120`），同类 CLI 亦无条件追加
// （`QuestionView.tsx:222`）。whale 的「模型可关」属三类独有，且该字段默认 false
// ⇒ 用户默认答不了「以上都不是」。现改为**有选项即总追加**；`allow_free_text` 只保留
// 「options 是否可为空（纯开放式提问）」一义，与它自己的 schema 文案 instead of selection 对齐。
// ⇒ TestN132_AllowFreeTextFalseStillAppendsOther / _AllowFreeTextTrueStillAppendsOther
//	(c) 用户可见回显与回传内容**同源**（同一个 (answer, note) 派生）
// ⇒ TestN132_EchoAndPayloadShareSource
//	(d) ****：`user_note: ` 只留给「对所选选项的补充说明」（`i` 入口）；
// 选 Other 自填改挂 `user_answer: `（**答案**）—— 把 只在**界面**做的分工补到**数据面**。
// ⇒ TestN132_OtherKeepsSentinelLabel（含反向半边：不得出现 note 前缀）
// TestN134_PresetChoiceNoteStillUsesNotePrefix
//
// ★ 判别性（production-path-tdd）：全部从**生产入口**进入 —— `checkAskUserChoice`（真拦截点）
// 装配 PendingChoice，再发**真实 tea.KeyMsg**；断言打在 `session.ToolResult` 的 Output
// （模型真正收到的那串字符）与会话里的用户可见回显行上，不钉中间字段。

import (
	"encoding/json"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/anchorandfish/whale-code/internal/tools"
)

// newAskUserPanelFromRealSession 复现 2026-09-23 真机会话存档 L15 的入参形状：
// 模型给了选项、指定 default_idx=0，并声明 `allow_free_text: false`（该次真机的原样入参）。
//
// ★ 后它的用途反了过来：不再是「证明不追加 Other」，而是**证明 false 也照样追加**
// —— 真机那次正是 false，而用户确实需要能选「以上都不是」。
func newAskUserPanelFromRealSession(t *testing.T) *Model {
	t.Helper()
	m := newTUIRegressionModel(t)

	raw, _ := json.Marshal(tools.AskUserResult{
		Question:      "登录认证方式选哪种？",
		Category:      "dialogue-clarify",
		DefaultIdx:    0,
		AllowFreeText: false,
		Options: []tools.AskUserOption{
			{Label: "JWT", Value: "jwt", Description: "无状态 Token，适合微服务"},
			{Label: "Session", Value: "session", Description: "服务端存储，传统 Web"},
		},
	})
	results := []tools.Result{{ID: "ask-1", Name: "AskUser", Output: string(raw)}}
	choices, ok := m.checkAskUserChoice(results)
	if !ok || len(choices) != 1 {
		t.Fatalf("前置失败：checkAskUserChoice 应返回 1 个 choice，got %d (ok=%v)", len(choices), ok)
	}
	m.toolService.SetPendingChoice(&choices[0])
	m.toolService.SetChoiceIdx(0)
	m.sessionService.SetState(StateWaitingChoice)
	m.reassignServices()
	return m
}

// lastEchoLine 取渲染层里最后一条「 → …」用户可见回显（`app.go` 的 addMessage(" → %s")）。
//
// ★ 注意这里读的是 `sessionService.Messages()`（渲染层 messageBuffer），而回传内容读的是
// `session.ToolResult`（数据层 session.messages）—— **两个不同的存储**。判据刻意跨这两个面断言：
// 「用户看到的」与「模型收到的」一旦各自漂移，本判据就红。
func lastEchoLine(m *Model) string {
	msgs := m.sessionService.Messages()
	for i := len(msgs) - 1; i >= 0; i-- {
		if strings.Contains(msgs[i], " → ") {
			return strings.TrimSpace(msgs[i])
		}
	}
	return ""
}

// TestN132_OtherKeepsSentinelLabel (a)
// 选 Other 并自填 ⇒ 模型收到的 answer 段必须是 `otherOptionLabel` 本身（「以上都不是」），
// 而不是空串；自填内容作为独立的 `user_answer: ` 条跟在后面。
//
// ★ 改前缀断言：修前是 `user_note: `，等于把**用户的答案**标成了**注** ——
// 界面早就说「选 None of the above = 自己作答」（render_extras.go:200 活线文案），
// 数据面却告诉模型"这是一条注" ⇒ 模型按注处理、不执行其中的指令
// （真机 01a0c506 L27-L29：回传 `None of the above\nuser_note: 看到后回复hello` 逐字节送达；模型只就 `None of the above` 作答，没理会那句话）。
func TestN132_OtherKeepsSentinelLabel(t *testing.T) {
	m := newAskUserPendingModel(t)

	pending := m.toolService.PendingChoice()
	if pending == nil || !hasOtherOption(pending.Options) {
		t.Fatal("前置失败：allow_free_text=true 的 AskUser 面板应含 Other 哨兵项")
	}
	m.toolService.SetChoiceIdx(len(pending.Options) - 1) // 高亮到 Other

	// 选中 Other → enter 进补充子态（不立即回传）
	um, cmd := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	mm := um.(Model)
	mm.reassignServices()
	if cmd != nil {
		t.Fatalf("选中 Other 不应立即回传，got %#v", cmd)
	}
	if !mm.choiceFreeTextMode {
		t.Fatal("选中 Other 后应进入补充输入子态")
	}

	mm.uiService.Textarea().SetValue("我选 OAuth2")
	um2, cmd2 := mm.Update(tea.KeyMsg{Type: tea.KeyEnter})
	mm = um2.(Model)
	if cmd2 == nil {
		t.Fatal("enter 应返回选择结果命令")
	}
	res, ok := cmd2().(choiceResultMsg)
	if !ok {
		t.Fatalf("应返回 choiceResultMsg，got %#v", cmd2())
	}
	if res.Answer != otherOptionLabel {
		t.Fatalf("选 Other 时 choiceResultMsg.Answer 应为标签本身 %q，got %q"+
			"（修前为 \"\" ⇒ 回传裸文本，模型只能猜 default_idx）", otherOptionLabel, res.Answer)
	}

	um3, _ := mm.Update(res)
	final := um3.(Model)

	got := lastToolResultOutput(t, &final)
	want := otherOptionLabel + "\n" + askUserAnswerPrefix + "我选 OAuth2"
	if got != want {
		t.Fatalf("模型收到的答案必须带「以上都不是」标签 + 独立 user_answer 条：\n  want %q\n  got  %q", want, got)
	}
	// ★ 反向半边：选 Other 时**不得**出现 note 前缀 —— 这段文字是用户的**答案**。
	// 少了这一半，判据只证明"多了一条前缀"，证明不了"它不在注的位置上"（假绿缺口）。
	if strings.Contains(got, askUserNotePrefix) {
		t.Fatalf("选 Other 自填是「用户自己的答案」，不得挂在 %q（注）位置：got %q", askUserNotePrefix, got)
	}
}

// TestN132_AllowFreeTextFalseStillAppendsOther ( 修正 · 原 _NoOtherOption 反转)
// 模型声明 `allow_free_text: false` ⇒ 面板**仍然**追加 Other。
//
// 依据（逐字核源码，三类对照）：
// - 同类 CLI：`is_other` **不在模型可见的 schema 里**，服务端 normalize 无条件覆盖为 true
// （core/src/tools/handlers/request_user_input_spec.rs:120）；`options` 是必填（:64）。
// 工具说明还明写 "Do not include an \"Other\" option in this list; the client will add
// a free-form \"Other\" option automatically."（:31）
// - 同类 CLI：`t7 = [...textOptions, otherOption]` 无条件追加（QuestionView.tsx:222）。
//
// ⇒ 两类都不让模型关掉自填口；whale 原「模型可关」是三类独有，且该字段默认 false
// ⇒ 用户默认无法回答「以上都不是」（用户 2026-09-23 正是踩在这个形态上）。
func TestN132_AllowFreeTextFalseStillAppendsOther(t *testing.T) {
	m := newAskUserPanelFromRealSession(t)

	pc := m.toolService.PendingChoice()
	if pc == nil {
		t.Fatal("前置失败：无 PendingChoice")
	}
	if !hasOtherOption(pc.Options) {
		t.Fatalf("allow_free_text=false 也必须追加 Other（ 撤回 (b)）：Options=%+v", pc.Options)
	}
	if len(pc.Options) != 3 {
		t.Fatalf("Options 应为模型给的 2 项 + 客户端 Other = 3 项，got %d：%+v", len(pc.Options), pc.Options)
	}
	last := pc.Options[len(pc.Options)-1]
	if last.Label != otherOptionLabel || last.Value != otherOptionValue {
		t.Fatalf("末项应为客户端 Other（%q / %q），got %q / %q",
			otherOptionLabel, otherOptionValue, last.Label, last.Value)
	}
}

// TestN132_AllowFreeTextTrueStillAppendsOther (b 反向保)
// `allow_free_text: true` ⇒ 同样按 同类 CLI is_other 追加客户端 Other 项，且 Label = otherOptionLabel。
// 与上一条配对：**两条路径都追加** —— 追加与否不再取决于这个字段。
func TestN132_AllowFreeTextTrueStillAppendsOther(t *testing.T) {
	m := newAskUserPendingModel(t)

	pc := m.toolService.PendingChoice()
	if pc == nil {
		t.Fatal("前置失败：无 PendingChoice")
	}
	if !hasOtherOption(pc.Options) {
		t.Fatalf("allow_free_text=true 应追加 Other，Options=%+v", pc.Options)
	}
	last := pc.Options[len(pc.Options)-1]
	if last.Label != otherOptionLabel {
		t.Fatalf("Other 项 Label 应为 %q，got %q", otherOptionLabel, last.Label)
	}
}

// TestN132_EchoAndPayloadShareSource (c)
// 「用户看到的回显」与「模型收到的答案」必须同源：回显里的选项段 == 回传的 answer 段。
//
// 为什么要有这条：修前 `echo` 由局部变量单独拼（`freeText`，或 `label + " · " + freeText`），
// 与回传是两条独立代码路径 ⇒ 两边可以各自漂移，而用户只会看到其中一边。
// 本次真机困惑正源于此：用户以为模型收到的是 hello，无从知道模型拿它没辙。
func TestN132_EchoAndPayloadShareSource(t *testing.T) {
	m := newAskUserPendingModel(t)

	pending := m.toolService.PendingChoice()
	m.toolService.SetChoiceIdx(len(pending.Options) - 1) // Other

	um, _ := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	mm := um.(Model)
	mm.reassignServices()
	mm.uiService.Textarea().SetValue("我选 OAuth2")

	um2, cmd := mm.Update(tea.KeyMsg{Type: tea.KeyEnter})
	mm = um2.(Model)
	res, ok := cmd().(choiceResultMsg)
	if !ok {
		t.Fatalf("应返回 choiceResultMsg，got %#v", cmd())
	}

	um3, _ := mm.Update(res)
	final := um3.(Model)

	payload := lastToolResultOutput(t, &final)
	answerSeg := strings.SplitN(payload, "\n", 2)[0]

	echo := lastEchoLine(&final)
	if echo == "" {
		t.Fatal("找不到面板回显行（→ …）—— 提交后必须回显，否则用户以为没送出去")
	}
	if answerSeg != otherOptionLabel {
		t.Fatalf("回传 answer 段应为 %q，got %q", otherOptionLabel, answerSeg)
	}
	if !strings.Contains(echo, answerSeg) {
		t.Fatalf("回显必须与回传同源：回显 %q 里看不到回传的 answer 段 %q "+
			"—— 用户会以为模型收到的是别的东西", echo, answerSeg)
	}
	if !strings.Contains(echo, "我选 OAuth2") {
		t.Fatalf("回显必须包含用户自填内容，got %q", echo)
	}
}

// TestN134_PresetChoiceNoteStillUsesNotePrefix 走 `i` 入口（选预设项 + 补充说明）——
// 那是**真·注**，必须继续挂 `user_note: `，且**不得**挂 `user_answer: `。
//
// ★ 为什么这一半不可省（防"一刀切"式假修复）： 只该改「选 Other」那一支。
// 若实现者图省事把两个入口一起换成 `user_answer:`，本判据转红。
// 两个入口在数据面分开，正是 “「同一事实跨入口必须同源」的可机测面。
//
// 从**生产入口**进入：真实 tea.KeyMsg "i" → 子态 → 敲字 → Enter，断言打在
// `session.ToolResult` 的 Output（模型真正收到的那串字符），不钉中间字段。
func TestN134_PresetChoiceNoteStillUsesNotePrefix(t *testing.T) {
	m := newAskUserPendingModel(t)

	pending := m.toolService.PendingChoice()
	if pending == nil {
		t.Fatal("前置失败：应存在 PendingChoice")
	}
	m.toolService.SetChoiceIdx(0) // 高亮预设项 0（不是 Other）

	um, cmd := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("i")})
	mm := um.(Model)
	mm.reassignServices()
	if cmd != nil {
		t.Fatalf("按 i 不应立即回传，got %#v", cmd)
	}
	if !mm.choiceFreeTextMode {
		t.Fatal("按 i 应进入补充输入子态")
	}

	mm.uiService.Textarea().SetValue("但 Session 更符合我们现有架构")
	um2, cmd2 := mm.Update(tea.KeyMsg{Type: tea.KeyEnter})
	mm = um2.(Model)
	if cmd2 == nil {
		t.Fatal("enter 应返回选择结果命令")
	}
	res, ok := cmd2().(choiceResultMsg)
	if !ok {
		t.Fatalf("应返回 choiceResultMsg，got %#v", cmd2())
	}
	if res.AnswerIsUserOwn {
		t.Fatalf("选预设项 + i 是「补充说明」，AnswerIsUserOwn 必须为 false，got true（Answer=%q）", res.Answer)
	}

	um3, _ := mm.Update(res)
	final := um3.(Model)
	got := lastToolResultOutput(t, &final)

	want := res.Answer + "\n" + askUserNotePrefix + "但 Session 更符合我们现有架构"
	if got != want {
		t.Fatalf("选预设项 + i 的补充说明必须挂 note 前缀（保持既有字面）：\n  want %q\n  got  %q", want, got)
	}
	if strings.Contains(got, askUserAnswerPrefix) {
		t.Fatalf("选预设项 + i 不是「用户自己作答」，不得出现 %q：got %q", askUserAnswerPrefix, got)
	}
}
