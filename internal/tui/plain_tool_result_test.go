package tui

// 本文件是 `--plain`（CI / 无人值守）工具结果出口的判据（2026-09-30 · 真机 I-8.2）。
//
// 回归背景：`echo hi > out/i8_created.txt` **执行成功**，但 stdout 本就为空 ⇒
// `--plain` 把 `[Result: ]` 印成一行空壳，看日志的人以为结果丢了。
//
// 上一轮把三个出口收敛到 session.NormalizeToolOutput 时，本出口只做到"代码同源"、
// **没有机器判据**（回执 §5 已登记为未做项）。本文件补上：判据从生产入口
// emitToolResults 进入 —— 它是 RunCI 的工具结果出口，**无旁路**（RunCI 里只有
// 这一个调用点），所以判据覆盖的就是生产路径本身。

import (
	"bytes"
	"regexp"
	"strings"
	"testing"

	"github.com/anchorandfish/whale-code/internal/tools"
)

var plainResultLine = regexp.MustCompile(`\[Result: (.*?)\]`)

// TestPlainToolResultIsNeverBlank 主判据：成功但无输出 ⇒ `[Result: …]` 不得为空壳，
// 且回传模型的 tool_result 内容同样不得为空。
func TestPlainToolResultIsNeverBlank(t *testing.T) {
	var stdout, stderr bytes.Buffer

	msgs, failed := emitToolResults(&stdout, &stderr, []tools.Result{
		{ID: "call_1", Name: "bash", Output: "", IsError: false},
	})

	if failed {
		t.Fatal("执行成功但无输出，不得被当成失败（否则 CI 会误判为工具出错）")
	}
	if stderr.Len() != 0 {
		t.Fatalf("成功结果不得写 stderr；实际 %q", stderr.String())
	}

	out := stdout.String()
	m := plainResultLine.FindStringSubmatch(out)
	if m == nil {
		t.Fatalf("成功结果必须印出一行 `[Result: …]`；实际 %q", out)
	}
	if strings.TrimSpace(m[1]) == "" {
		t.Fatalf("`[Result: ]` 不得为空壳 —— 看日志的人会以为结果丢了；实际整行 %q", out)
	}

	if len(msgs) != 1 {
		t.Fatalf("应产出 1 条 tool 消息；实际 %d", len(msgs))
	}
	if got := msgs[0].Blocks[0].Output; strings.TrimSpace(got) == "" {
		t.Fatalf("回传模型的 tool_result 内容同样不得为空（模型会当成「没跑」而重发）；实际 %q", got)
	}
}

// TestPlainToolResultPassesThroughRealOutput 反例守卫（before/after 都必须绿）：
// ① 真实输出必须原样印出、② 失败结果走 stderr 并计入 failed、
// ③ 空壳 `[Result: ]` 在任何分支都不得出现。
func TestPlainToolResultPassesThroughRealOutput(t *testing.T) {
	var stdout, stderr bytes.Buffer

	msgs, failed := emitToolResults(&stdout, &stderr, []tools.Result{
		{ID: "a", Name: "bash", Output: "hello\n", IsError: false},
		{ID: "b", Name: "bash", Output: "boom", IsError: true},
	})

	if !failed {
		t.Fatal("含 IsError=true 的结果必须报失败（CI 依赖它做连续失败熔断）")
	}
	if !strings.Contains(stdout.String(), "hello") {
		t.Fatalf("成功结果必须原样印到 stdout；实际 %q", stdout.String())
	}
	if !strings.Contains(stderr.String(), "boom") {
		t.Fatalf("失败结果必须印到 stderr；实际 %q", stderr.String())
	}
	if strings.Contains(stdout.String(), "[Result: ]") {
		t.Fatalf("空壳 `[Result: ]` 不得出现；实际 %q", stdout.String())
	}
	if len(msgs) != 2 {
		t.Fatalf("两条结果应产出 2 条 tool 消息；实际 %d", len(msgs))
	}
}
