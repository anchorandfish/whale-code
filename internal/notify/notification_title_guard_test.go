package notify

// 批 5 守卫：OS 通知标题英文单侧。
//
// 为什么标题不能双语：Title 进单行容器（OS 通知栏 / `/notify list` 的
// `[label] title: message` 行），容不下缩进辅行 —— 与 tui 的 priorityLabel
// 同一先例（批 3 拍板）。⇒ 标题必须无汉字。

import (
	"testing"

	"github.com/anchorandfish/whale-code/internal/util"
)

// TestW083B5_NotificationTitles_EnglishOnly 钉通知标题英文单侧。
func TestW083B5_NotificationTitles_EnglishOnly(t *testing.T) {
	// ① 错误通知。
	if got := NewErrorNotification("E_TEST", "msg").Title; util.HasHan(got) {
		t.Errorf("错误通知标题含汉字 %q：单行容器容不下辅行 ⇒ 必须英文单侧", got)
	}
	// ② 任务通知全分支。
	for _, status := range []string{"completed", "failed", "cancelled", "bogus"} {
		got := NewTaskNotification("T_TEST", status).Title
		if util.HasHan(got) {
			t.Errorf("任务通知(status=%s)标题含汉字 %q：必须英文单侧", status, got)
		}
	}
}

// TestW083B5_NotificationTitleIsDiscriminating 证明门的判别力。
func TestW083B5_NotificationTitleIsDiscriminating(t *testing.T) {
	if !util.HasHan("任务完成") {
		t.Fatal("HasHan 对汉字串返回 false —— 判定器失效（门是假的）")
	}
	if util.HasHan("Task Completed") {
		t.Fatal("HasHan 对英文串返回 true —— 判定器失效（门是假的）")
	}
}
