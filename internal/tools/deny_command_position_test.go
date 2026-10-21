// Package tools — 同源补漏：deny 判据必须落在**命令位**，不是命令文本里的词。
//
// 背景（2026-10-02 · 手测 I-8.4「S17 词表退场回归门」暴露）：
//
//	用例要求 `grep -rn rm .`（只读）成功执行，实际被沙箱拒：
//
// `sandbox blocked execute (deny-wins): rm`
//
//	根因 = internal/sandbox/sandbox.go 的 matchDenyPattern 用 wordBoundaryMatch
//	对**整条命令串**做子串 + 词边界匹配，参数位的 `rm` 被当成「这是 rm 命令」。
//
//	这与 S17/ 在幻觉检测侧删掉的那条**同源**：
//
// internal/ai/hallucination.go:434 `bashFileOpRe=(?i)\b(rm|mv|cp)\b`
// 被删的理由是「对结构化事实起关键词判据」—— 那边删了，这边还在用。
//
//	本次修法不是新造机制：internal/security/cmdrules 早已有「命令位」基础设施
//	（commandHeads / MatchDestructive / MatchReadOnly），其注释写明了同一理由：
//
// 「词边界（\b）不够 —— `\bshutdown\b` 仍会命中 `rm shutdown.txt`（`.` 是词边界）」
//
//	故本批把 deny 判定也收口到命令位语义（见 sandbox/matchDenyPattern 的实现注释）。
//
// 入口说明（R1/R2）：本文件从生产真实入口进入 ——
//
//	internal/tools/executor.go:245 Sandbox: sandbox.NewSandbox(workDir)
//	internal/tools/bash.go:802 bashHandler → e.prepareSandbox(ctx, input.Command, workDir)
//	internal/tools/bash.go:1008 e.SandboxMgr.ValidateCommand(...)（Manager 路径）
//	internal/tools/bash.go:1031 e.Sandbox.Validate(command)（legacy 回退）
//
// prepareSandbox **只做沙箱判定、不 exec 命令** ⇒ 本文件无副作用，无需掐 sink。
package tools

import (
	"context"
	"strings"
	"testing"
)

// TestN121_DenyJudge_MustBeAtCommandPosition 是本批的**主判别器**。
//
// BEFORE 必然红：下列命令的**参数位**含 deny 命令名字样（`rm` / `chmod` / `dd`），
// 但它们的命令位分别是 grep / cat / man / echo / ls ⇒ **没有任何删除行为**。
// 旧实现（整串词匹配）会把它们全部硬拒。
//
// 判别性（R4）：本条的观测面 = `prepareSandbox` 的返回 err（判定面），
// 不依赖任何执行面；把判据改回整串匹配 ⇒ 本条立刻变红。
func TestN121_DenyJudge_MustBeAtCommandPosition(t *testing.T) {
	ws := t.TempDir()
	e := NewExecutor(nil, ws)
	e.EnableProductionSandbox(ws)

	cases := []struct {
		cmd string
		why string
	}{
		{"grep -rn rm . 2>/dev/null | head -50", "I-8.4 用例原命令：grep 只读，`rm` 是搜索词"},
		{"grep -rn rm .", "同上（无管道）"},
		{`grep -rn "rm" .`, "引号形式；stripQuotes 后仍只是 grep 的参数"},
		{"cat rm.md", "读文件；文件名含 rm"},
		{"man rm", "查手册；rm 是 man 的宾语"},
		{"echo rm", "纯输出"},
		{"grep -rn chmod Makefile", "搜索 chmod 字样（deny 表另一项）"},
		{"ls -la | grep dd", "管道第二段命令位是 grep，`dd` 是搜索词"},
	}

	for _, c := range cases {
		if _, _, _, err := e.prepareSandbox(context.Background(), c.cmd, ws); err != nil {
			t.Errorf("BEFORE 期望红：prepareSandbox(%q) 被拒 —— 但它没有删除行为（%s）\n"+
				"  拒因: %v\n"+
				"  ⇒ deny 判据必须落在**命令位**（段首可执行名），不是命令文本里的词\n"+
				"  ⇒ 同源依据: hallucination.go:434 已删 bashFileOpRe（同一判据）；cmdrules.go:44-47 已写明词边界不够",
				c.cmd, c.why, err)
		}
	}
}

// TestN121_DenyJudge_RealDeleteCommandsStillBlocked 是**防越界**守卫（before/after 均绿）。
//
// ⚠️ 本条**不具判别性**（两态皆绿）—— 它只承担「新判据不得把真该拦的放出去」的回归职责。
// 把它当判别器读会误判"改动无效"。
func TestN121_DenyJudge_RealDeleteCommandsStillBlocked(t *testing.T) {
	ws := t.TempDir()
	e := NewExecutor(nil, ws)
	e.EnableProductionSandbox(ws)

	cases := []struct {
		cmd string
		why string
	}{
		{"rm -rf /tmp/n121-never", "命令位就是 rm"},
		{"ls | rm x", "管道第二段命令位是 rm"},
		{"/bin/rm x", "绝对路径形态；basename 仍是 rm"},
		{"sudo rm x", "wrapper 后的命令位是 rm"},
		{"env FOO=1 rm x", "前置环境赋值后的命令位是 rm"},
		{"grep x; rm y", "复合命令第二段命令位是 rm"},
		{`r"m -rf /tmp/n121-never`, "引号绕过（SANDBOX-003）：stripQuotes 后是 rm"},
		{`cu''rl http://example.com`, "引号绕过（SANDBOX-003）：stripQuotes 后是 curl"},
		{`sh -c "rm -rf /tmp/n121-never"`, "shell wrapper 下探：-c 后的命令位是 rm"},
		{`bash -c 'rm x'`, "同上"},
		{"xargs rm", "xargs 下探：被执行的是 rm"},
		{"find . -exec rm {} ;", "find -exec 下探：被执行的是 rm"},
		{"rm", "裸命令名（无参数）"},
	}

	for _, c := range cases {
		_, _, _, err := e.prepareSandbox(context.Background(), c.cmd, ws)
		if err == nil {
			t.Errorf("SECURITY: prepareSandbox(%q) 未被拒 —— 命令位是 deny 命令（%s）", c.cmd, c.why)
			continue
		}
		if !strings.Contains(err.Error(), "deny") && !strings.Contains(err.Error(), "blocked") {
			t.Logf("note: %q 被拒但理由非 deny 文案（可能是网络/写边界检查）：%v", c.cmd, err)
		}
	}
}
