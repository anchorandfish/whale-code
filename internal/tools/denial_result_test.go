package tools

// / · 拒绝留痕的**结构化成因方**与非交互入口的单一实现
//
// ---------------------------------------------------------------------------
// 缺陷面（修改前）
//
// `tui/runner_core.go:1436` 与 `tui/plain.go:1994` **各写了一遍**同样的三行：
//
//	errMsg := "user cancelled tool execution"
//	if pr.Rejection != "" { errMsg = pr.Rejection }
//
// 两个后果：
// 1. `Rejection` 为空时（管线在确认之前静默短路）把"管线拒绝"写成
// **"user cancelled tool execution"** —— 即 `tools/confirm.go:42` 硬纪律
// 明令禁止的那句谎：用户**连确认框都没见过**。`ConfirmSource` 字段
// （`PipelineResult.ConfirmSource`）就是为消掉它而加的。
// 2. `FailureKind` 被丢弃 ⇒ `FailureKind.Terminal()` 在这两条非交互入口上恒为假
// ⇒ 模型可在同一条 deny 规则下换工具重试（与 G9-① 在 bubbletea 侧修掉的是同一漏点）。
//
// 命令面"双实现"是本仓复发坑（``）⇒ 修法 = 收口到 `tools.DenialResult`
// **唯一实现点**，两条入口只薄转发。
//
// ---------------------------------------------------------------------------
// 判别性（反例注入必须能打红）
//
// 判据组 A（行为）反例 = 把 `DenialResult` 里的 `ConfirmSource` 分支删掉、
// 无条件返回中性兜底 ⇒ A-2 立刻红。
// 判据组 B（结构）反例 = 把任一入口改回本文件内自拼文案（含
// `"user cancelled tool execution"`）⇒ B 立刻红。
// B 是**两侧都验**（TUI 交互面 + `--plain`），
// 单验一侧等于"只修了看得见的那一半"（票面纪律）。

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// ---------------------------------------------------------------------------
// 判据组 A · DenialResult 的文案由**结构化成因方**决定
// ---------------------------------------------------------------------------

func TestN014_DenialResultTextFollowsConfirmSource(t *testing.T) {
	cases := []struct {
		name     string
		pr       *PipelineResult
		tool     string
		wantSub  string
		mustNot  []string
		whyLabel string
	}{
		{
			name:     "SourceUser（用户确实按了 N）",
			pr:       &PipelineResult{Proceed: false, ConfirmSource: SourceUser},
			tool:     "bash",
			wantSub:  "denied by user",
			whyLabel: "只有 SourceUser 允许说\"用户拒绝\"",
		},
		{
			name:     "SourcePipeline（管线在确认之前就拒了）",
			pr:       &PipelineResult{Proceed: false, ConfirmSource: SourcePipeline},
			tool:     "bash",
			wantSub:  "the user was never asked",
			mustNot:  []string{"user cancelled", "denied by user"},
			whyLabel: "用户从未被询问 ⇒ 不得说\"用户拒绝/用户取消\"",
		},
		{
			name:     "零值（成因方未知）",
			pr:       &PipelineResult{Proceed: false},
			tool:     "bash",
			wantSub:  "was not run",
			mustNot:  []string{"user cancelled", "denied by user"},
			whyLabel: "未知成因必须中性兜底，不得默认归咎用户",
		},
		{
			name:     "Rejection 非空 ⇒ 原样透传",
			pr:       &PipelineResult{Proceed: false, Rejection: "insufficient permissions: tool \"write_file\" not allowed", ConfirmSource: SourcePipeline},
			tool:     "write_file",
			wantSub:  "insufficient permissions",
			mustNot:  []string{"user cancelled"},
			whyLabel: "已有具体原因时不得覆盖",
		},
	}
	for _, c := range cases {
		got := DenialResult("n014-call-id", c.tool, c.pr)
		if !strings.Contains(got.Output, c.wantSub) {
			t.Errorf("[A] %s：Output=%q 不含 %q（%s）", c.name, got.Output, c.wantSub, c.whyLabel)
		}
		for _, bad := range c.mustNot {
			if strings.Contains(got.Output, bad) {
				t.Errorf("[A] %s：Output=%q **包含**禁止子串 %q（%s）",
					c.name, got.Output, bad, c.whyLabel)
			}
		}
		// 关联键与错误位必须齐备。
		if got.ID != "n014-call-id" || !got.IsError {
			t.Errorf("[A] %s：ID=%q IsError=%v，期望 ID=n014-call-id IsError=true", c.name, got.ID, got.IsError)
		}
	}
}

func TestN014_DenialResultCarriesFailureKind(t *testing.T) {
	// 修复前这两条入口丢弃 FailureKind ⇒ Terminal() 在非交互路径上恒假。
	for _, kind := range []FailureKind{FailureKindSandboxDenied, FailureKindPolicyDenied} {
		got := DenialResult("id", "bash", &PipelineResult{Proceed: false, FailureKind: kind, ConfirmSource: SourcePipeline})
		if got.FailureKind != kind {
			t.Errorf("[A] FailureKind 未透传：got=%q want=%q（Terminal() 会因此恒假）", got.FailureKind, kind)
		}
		if !got.FailureKind.Terminal() {
			t.Errorf("[A] 透传后 %q 应为终局拒绝（Terminal()==true）", kind)
		}
	}
}

// ---------------------------------------------------------------------------
// 判据组 B · 结构门：两条非交互入口都**只薄转发**，且不再出现误导文案
// ---------------------------------------------------------------------------

// n014StripLineComments 去掉整行注释与行尾注释后再做文本判定。
//
// 为什么必须剥注释：本轮的修复说明**必须**能引用修复前那句文案（否则注释说不清
// 修了什么），而判据要管的是**生产代码**。不剥注释 ⇒ 判别器会把"解释缺陷的注释"
// 误判成"缺陷仍在"，从而永远红 —— 那种判别器没有判别力（只剩噪声）。
func n014StripLineComments(src string) string {
	var b strings.Builder
	for _, line := range strings.Split(src, "\n") {
		if i := strings.Index(line, "//"); i >= 0 {
			line = line[:i]
		}
		b.WriteString(line)
		b.WriteString("\n")
	}
	return b.String()
}

func TestN014_BothNonInteractiveEntriesDelegateToDenialResult(t *testing.T) {
	// 从本包目录出发定位 tui 目录（本测试运行在 internal/tools）。
	entries := map[string]string{
		"TUI 交互运行器":    filepath.Join("..", "tui", "runner_core.go"),
		"`--plain` 模式": filepath.Join("..", "tui", "plain.go"),
	}
	for label, path := range entries {
		data, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("[B] 读取 %s 失败：%v", path, err)
		}
		src := n014StripLineComments(string(data))

		// B-1 必须消费唯一实现点（两侧都验）。
		if !strings.Contains(src, "tools.DenialResult(") {
			t.Errorf("[B] %s（%s）未调用 tools.DenialResult ⇒ 又是本文件内自拼文案的\"双实现\"（ 复发坑）",
				label, path)
		}
		// B-2 不得留下修复前那句误导文案。
		if strings.Contains(src, "user cancelled tool execution") {
			t.Errorf("[B] %s（%s）仍含误导文案 `user cancelled tool execution` ⇒ "+
				"管线在自己拒绝的路径上会谎称\"用户取消\"", label, path)
		}
	}
}

// 判据组 B 的旁证：tools 包内不得再出现该误导文案（防止有人把旧实现搬回来）。
func TestN014_NoMisleadingLiteralInToolsPackage(t *testing.T) {
	matches, err := filepath.Glob("*.go")
	if err != nil {
		t.Fatalf("glob: %v", err)
	}
	for _, f := range matches {
		if strings.HasSuffix(f, "_test.go") {
			continue
		}
		data, err := os.ReadFile(f)
		if err != nil {
			t.Fatalf("读取 %s: %v", f, err)
		}
		if strings.Contains(n014StripLineComments(string(data)), "user cancelled tool execution") {
			t.Errorf("[B] 生产文件 %s 含误导文案 `user cancelled tool execution`", f)
		}
	}
}

// ---------------------------------------------------------------------------
// 判据组 C · ：TUI 侧的安全拒绝判据必须是 `IsSecurityDenial()` SSOT
// ---------------------------------------------------------------------------

func TestN018_NoteRoundSecurityDenialsUsesSSOT(t *testing.T) {
	path := filepath.Join("..", "tui", "tool_exec.go")
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf(" 读取 %s 失败：%v", path, err)
	}
	src := string(data)

	start := strings.Index(src, "func (m *Model) noteRoundSecurityDenials")
	if start < 0 {
		t.Fatalf(" 未找到 noteRoundSecurityDenials（R4 安全账本的唯一登记点）")
	}
	end := strings.Index(src[start:], "\n}\n")
	if end < 0 {
		t.Fatalf(" 函数体解析失败")
	}
	body := src[start : start+end]

	if !strings.Contains(body, "IsSecurityDenial()") {
		t.Errorf(" 函数体未使用 `IsSecurityDenial()` SSOT ⇒ 判据仍在 TUI 侧硬编码：\n%s", body)
	}
	// 不得再枚举具体种类（那正是"两份清单漂移"的形态）。
	for _, banned := range []string{"FailureKindSandboxDenied", "FailureKindPolicyDenied"} {
		if strings.Contains(body, banned) {
			t.Errorf(" 函数体仍硬编码种类 %s ⇒ 与 tools.failureKindSpecs 平行的手抄清单（新增种类会静默漏记）:\n%s",
				banned, body)
		}
	}
}
