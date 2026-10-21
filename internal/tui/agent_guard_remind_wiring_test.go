package tui

import (
	"strings"
	"testing"

	"github.com/anchorandfish/whale-code/internal/agentguard"
)

// nudgeInSession 报告会话上下文里是否存在 P0-3 软提醒（Subtype=="review_nudge"）。
// 这是「提醒真的喂给了模型」的判别点 —— 只做 UI 提示而不进上下文等于假修复。
func nudgeInSession(m *Model) bool {
	cur := m.sessionService.CurrentSession()
	if cur == nil {
		return false
	}
	for _, msg := range cur.GetMessages() {
		if msg.Subtype == "review_nudge" {
			return true
		}
	}
	return false
}

// TestN131_ReviewNudgeIsInjectedNotStopped P0-3 降级为软提醒后的接线不变量：
//
//	① 不停机（VerdictRemind ≠ VerdictStop）
//	② 提醒进入模型上下文（session 里能查到 review_nudge）
//	③ 用户可见（窗口里有提示）
//
// 三者缺一即为假修复：只做 ① 等于把保护删了；只做 ③ 模型根本收不到提醒。
func TestN131_ReviewNudgeIsInjectedNotStopped(t *testing.T) {
	m := newGuardTestModel(t)

	// 用可控阈值重建 guard（默认 8 轮太长，测试不便于构造）
	cfg := agentguard.DefaultConfig()
	cfg.MaxReviewRounds = 2
	m.guard = agentguard.NewGuard(cfg)
	m.guard.BeginRequest()

	// 反复读**同一批已读过的**文件（无新文件 ⇒ 计入 P0-3），
	// 输出各不相同且读取次数不超过 maxFileReads=5（避免 P0-4 抢先停机）。
	steps := []struct {
		file   string
		output string
	}{
		{"/repo/a.go", "读取配置，确认连接串指向 localhost"},
		{"/repo/b.go", "检查中间件链，鉴权顺序在日志之后"},
		{"/repo/a.go", "复查配置，发现超时字段未设置"},
		{"/repo/b.go", "复查中间件，新增分支缺少覆盖"},
		{"/repo/a.go", "汇总以上发现，准备给出修改建议"},
	}
	for _, s := range steps {
		m.guard.BeforeModelCall()
		m.guard.AfterToolResults(false)
		m.guard.AfterTurn(agentguard.TurnObservation{
			FilesRead: []string{s.file},
			Output:    s.output,
		})
	}

	// ① 不停机
	if _, stopped := m.guardBeforeModelCall(); stopped {
		t.Fatal("P0-3 软提醒不得停机（已降级为提醒）")
	}
	// ② 进模型上下文
	if !nudgeInSession(m) {
		t.Fatal("★ 软提醒未注入会话上下文 —— 模型收不到提醒，等于把 P0-3 删了（假修复）")
	}
	// ③ 用户可见
	if out := renderedMessagesText(m); !strings.Contains(out, "无文件改动") {
		t.Fatalf("★ 软提醒应对用户可见（窗口需出现提示），实际窗口：\n%s", out)
	}
}
