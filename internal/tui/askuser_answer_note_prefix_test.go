package tui

// askuser_answer_note_prefix_test.go — AskUser 回传形态守卫（2026-09-23 ·）
//
// 判据落点：**模型真正收到的那串字符** —— choiceResultMsg → session.ToolResult 的 Output。
// 不钉 choiceResultMsg 的字段（那只证「两个字段都在」，证不了「模型看到什么」）。
//
// 对齐对象：同类 CLI request_user_input
//
//	对标证据件
// let mut answer_list = selected_label.into_iter().collect::<Vec<_>>();
// if !notes.is_empty() { answer_list.push(format!("user_note: {notes}")); }
//
// ⇒ 选项与 note 是两个独立条目，note 带 `user_note: ` 前缀，模型不会把「补充」误读成「又选了一个」。
//
// 修前 whale（app.go 拼装处）：answerMsg = Answer + "\n[补充] " + FreeText
// ⇒ ① 模型收到「JWT / 但 Session 更符合…」这种自相矛盾的一句扁平串，只能自己猜哪个算数；
// ② `[补充]` 是汉字，混进英文主行，违反「英文主行不得含汉字」的形态契约（SSOT = bilingual.go）。

import (
	"strings"
	"testing"
)

// askUserNotePrefix 直接用生产常量（app.go），不再在测试里另写一份 —— 防双实现漂移。

// lastToolResultOutput 取会话末尾那条 tool_result 的 Output。
func lastToolResultOutput(t *testing.T, m *Model) string {
	t.Helper()
	cur := m.sessionService.CurrentSession()
	if cur == nil {
		t.Fatal("前置失败：无当前会话")
	}
	msgs := cur.GetMessages()
	for i := len(msgs) - 1; i >= 0; i-- {
		for _, b := range msgs[i].Blocks {
			if b.Type == "tool_result" {
				return b.Output
			}
		}
	}
	t.Fatal("会话里没有 tool_result —— AskUser 答案根本没回传（静默失败面）")
	return ""
}

// TestN131_AskUserNoteIsSeparateFromChoice 选了预设项 + 补充 note：两者都给模型，但 note 必须是
// 带 `user_note: ` 前缀的独立一条，不是拼进选项那句话里。
func TestN131_AskUserNoteIsSeparateFromChoice(t *testing.T) {
	m := newAskUserPendingModel(t)
	updatedModel, _ := m.Update(choiceResultMsg{
		ToolID:   "ask-1",
		Answer:   "jwt",
		FreeText: "但 Session 更符合我们现有架构",
	})
	updated := updatedModel.(Model)

	got := lastToolResultOutput(t, &updated)
	want := "jwt\n" + askUserNotePrefix + "但 Session 更符合我们现有架构"
	if got != want {
		t.Fatalf("模型收到的答案应为「选项 + 独立一条 user_note」：\n want %q\n got  %q", want, got)
	}
}

// TestN131_AskUserNoteOnlyWhenNoChoice 选中 Other（Answer 空）时只回 note —— 不带空前缀、不带空行。
func TestN131_AskUserNoteOnlyWhenNoChoice(t *testing.T) {
	m := newAskUserPendingModel(t)
	updatedModel, _ := m.Update(choiceResultMsg{
		ToolID:   "ask-1",
		Answer:   "",
		FreeText: "其实我们团队规范是用 OAuth2",
	})
	updated := updatedModel.(Model)

	got := lastToolResultOutput(t, &updated)
	if got != "其实我们团队规范是用 OAuth2" {
		t.Fatalf("纯补充（选 Other）应只回 note 本身，got %q", got)
	}
}

// TestN131_AskUserChoiceOnlyHasNoNoteLine 没补充时回传就是选项本身，不得多出空的 note 行。
func TestN131_AskUserChoiceOnlyHasNoNoteLine(t *testing.T) {
	m := newAskUserPendingModel(t)
	updatedModel, _ := m.Update(choiceResultMsg{
		ToolID: "ask-1",
		Answer: "session",
	})
	updated := updatedModel.(Model)

	got := lastToolResultOutput(t, &updated)
	if got != "session" {
		t.Fatalf("无补充时回传应为选项本身，got %q", got)
	}
	// 两个前缀都要核 —— 无补充时既不该有 note，也不该有 answer。
	for _, p := range []string{askUserNotePrefix, askUserAnswerPrefix} {
		if strings.Contains(got, p) {
			t.Fatalf("无补充时不得出现 %q 前缀，got %q", p, got)
		}
	}
}
