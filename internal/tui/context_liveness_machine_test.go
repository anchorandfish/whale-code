package tui

// /context 数字活性机测。
//
// 背景面：/context 的 Used/Messages 数字存在「陈旧读死角」嫌疑 ——
// 实现里 system/tools 部分来自最近一次请求的 requestSnapshot（有意设计，
// 文案已声明 "last request"），但 **消息维** 必须是活的：改了会话消息而未发请求，
// /context 的 Messages 条数与 token 估算必须立即跟着变。
// 若有人把消息维也改成读快照/缓存（陈旧读），本门转红。
//
// 生产路径（R1 断言）：
// m.handleCommand("/context") (app.go:3962 case "context")
// → m.handleContext() (commands_task.go:292)
// → m.handleContextInfo() (commands_task.go:16)
// → formatContextStatus() (context_status.go:93)
// → estimateCurrentInputContext() ← 消息维从 sess.GetMessages() 现算（context_status.go:68-69）
//
// 判别性自证（R4 通道 A，-overlay 编译期 mutant）：
// 把 formatContextStatus 的 messageTokens 钉成 0、msgs 计数钉成 0 ⇒ 两条断言必须精确转红。
// 本轮已实测：mutant 下 token 断言与条数断言同时红；原码下绿（见 handoff 回执）。

import (
	"regexp"
	"strconv"
	"strings"
	"testing"

	"github.com/anchorandfish/whale-code/internal/session"
)

var ctxMsgLineRe = regexp.MustCompile(
	`Messages 对话消息: ~([0-9,]+)（local estimate, (\d+) msgs`)

func parseCtxMsgLine(t *testing.T, out string) (tokens, count int) {
	t.Helper()
	m := ctxMsgLineRe.FindStringSubmatch(out)
	if m == nil {
		t.Fatalf("前置失败：/context 输出里找不到 Messages 行（文案漂移也会打到这条）：%q", out)
	}
	tokens, _ = strconv.Atoi(strings.ReplaceAll(m[1], ",", ""))
	count, _ = strconv.Atoi(m[2])
	return tokens, count
}

func TestContextSlashCommandNumbersAreLive(t *testing.T) {
	m := newTUIRegressionModel(t)

	out1 := m.handleCommand("/context")
	beforeTokens, beforeCount := parseCtxMsgLine(t, out1)

	sess := m.sessionService.CurrentSession()
	if sess == nil {
		t.Fatal("前置失败：回归模型应有当前会话")
	}
	// 关键：只改会话消息，**不发请求** ⇒ requestSnapshot 不更新。
	// 这正是陈旧读死角最容易被掩盖的形态 —— 快照维不变，消息维必须活。
	probe := strings.Repeat("liveness probe filler token ", 400)
	sess.SetMessages(append(sess.GetMessages(), session.UserText(probe)))

	out2 := m.handleCommand("/context")
	afterTokens, afterCount := parseCtxMsgLine(t, out2)

	if afterCount != beforeCount+1 {
		t.Fatalf("/context 消息条数是陈旧读：会话新增 1 条后 count before=%d after=%d（应恰好 +1）",
			beforeCount, afterCount)
	}
	if afterTokens <= beforeTokens {
		t.Fatalf("/context 消息 token 估算不随会话状态更新（数字活性死角）：before=%d after=%d（探针约 4000+ token）",
			beforeTokens, afterTokens)
	}
	t.Logf("[ctx-liveness] count %d→%d, tokens %d→%d", beforeCount, afterCount, beforeTokens, afterTokens)
}
