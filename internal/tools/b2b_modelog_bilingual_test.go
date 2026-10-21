package tools

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"strings"
	"testing"

	"github.com/anchorandfish/whale-code/internal/util"
)

// 双语文案守卫（`/bughunter` + `/ultraplan` 模式状态消息）。
//
// 形态 SSOT = internal/util/bilingual.go（英文主行 + 3 空格缩进中文辅行）。
// 分工原则（照 internal/util/bilingual_test.go:18-20）：**字符集同源**
// （复用 util.HasHan / util.AssertChineseOnlyOnIndentedLines）、**判据逻辑独立**
// （本文件自带 AST 遍历与扫描循环，不调用生产侧断言）。

var b2bFiles = []string{"bughunter.go", "ultraplan.go"}

// b2bHanOutsideBi 返回 src 中「含汉字的字符串字面量、但不在 util.Bi / util.BiSame 实参里」
// 的清单（= 会以中文为主原样输出的回归形态）。
//
// 抽成吃源码字节的纯函数，目的是让**判别性自证**可在测试内用合成源码完成
// （零磁盘写回，避开 `-overlay` 对「运行期自读磁盘」判别器无效的坑，见）。
func b2bHanOutsideBi(src []byte) []string {
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, "snippet.go", src, 0)
	if err != nil {
		return []string{"PARSE_ERROR: " + err.Error()}
	}

	// 1) 收集所有 util.Bi / util.BiSame 调用内的字面量位置（= 合法的中文辅行）。
	allowed := map[token.Pos]bool{}
	ast.Inspect(f, func(n ast.Node) bool {
		call, ok := n.(*ast.CallExpr)
		if !ok {
			return true
		}
		sel, ok := call.Fun.(*ast.SelectorExpr)
		if !ok {
			return true
		}
		pkg, ok := sel.X.(*ast.Ident)
		if !ok || pkg.Name != "util" || (sel.Sel.Name != "Bi" && sel.Sel.Name != "BiSame") {
			return true
		}
		ast.Inspect(call, func(m ast.Node) bool {
			if lit, ok := m.(*ast.BasicLit); ok && lit.Kind == token.STRING {
				allowed[lit.Pos()] = true
			}
			return true
		})
		return true
	})

	// 2) 找违规：含汉字的字面量不在 allowed 集合里。
	var out []string
	ast.Inspect(f, func(n ast.Node) bool {
		lit, ok := n.(*ast.BasicLit)
		if !ok || lit.Kind != token.STRING || !util.HasHan(lit.Value) {
			return true
		}
		if !allowed[lit.Pos()] {
			out = append(out, fmt.Sprintf("line %d: %s", fset.Position(lit.Pos()).Line, lit.Value))
		}
		return true
	})
	return out
}

// TestW083B2b_TouchedFilesExist 前置自检：被判文件必须真在，否则遍历零命中 = 假绿。
func TestW083B2b_TouchedFilesExist(t *testing.T) {
	for _, f := range b2bFiles {
		src, err := os.ReadFile(f)
		if err != nil {
			t.Fatalf("被守卫的源文件读不到（%s）: %v", f, err)
		}
		if len(src) == 0 {
			t.Fatalf("被守卫的源文件为空（%s）", f)
		}
	}
}

// TestW083B2b_NoHanLiteralOutsideBi 结构面：
// 两个文件里任何含汉字的字符串字面量都必须是 `util.Bi` / `util.BiSame` 的实参。
func TestW083B2b_NoHanLiteralOutsideBi(t *testing.T) {
	for _, fn := range b2bFiles {
		src, err := os.ReadFile(fn)
		if err != nil {
			t.Fatalf("读 %s: %v", fn, err)
		}
		for _, v := range b2bHanOutsideBi(src) {
			t.Errorf("%s: 含汉字的字面量不在 util.Bi/BiSame 实参里（会以中文为主原样输出）: %s", fn, v)
		}
	}
}

// TestW083B2b_AstGateCatchesInjection 判别性自证（结构门）：
// 注入一个「中文为主」的字面量必须被抓、合规写法不得被误判。
func TestW083B2b_AstGateCatchesInjection(t *testing.T) {
	bad := []byte("package p\n\nimport \"github.com/anchorandfish/whale-code/internal/util\"\n\n" +
		"func f() string { return \"Bug Hunter 状态：运行中\" }\n")
	if got := b2bHanOutsideBi(bad); len(got) == 0 {
		t.Error("AST 门漏判了「中文为主」的字面量（= 假绿，判据不承重）")
	}

	good := []byte("package p\n\nimport \"github.com/anchorandfish/whale-code/internal/util\"\n\n" +
		"func f() string { return util.Bi(\"Bug Hunter status: running\", \"Bug Hunter 状态：运行中\") }\n")
	if got := b2bHanOutsideBi(good); len(got) != 0 {
		t.Errorf("AST 门误判了合规写法（判据太宽）: %v", got)
	}
}

// TestW083B2b_ProducerOutputIsBilingual 行为面：
// 每个消息生产者真实输出的每一行都过形态门（含汉字的行必须缩进），且英文主行在
// （英文主行缺失 = 疑似退回「中文为主」）。
func TestW083B2b_ProducerOutputIsBilingual(t *testing.T) {
	probes := []struct {
		name string
		want string // 必须出现的英文主行
		out  func() string
	}{
		{"bughunter.start", "Bug Hunter mode started", func() string {
			return NewBugHunterManager().Start()
		}},
		{"bughunter.start.already", "Bug Hunter mode is already running", func() string {
			bh := NewBugHunterManager()
			bh.Start()
			return bh.Start()
		}},
		{"bughunter.stop", "Bug Hunter mode stopped", func() string {
			bh := NewBugHunterManager()
			bh.Start()
			bh.AddIssue(BugIssue{File: "a.go", Line: 1, Severity: "high", Description: "bug", Status: "open"})
			return bh.Stop()
		}},
		{"bughunter.stop.notrunning", "Bug Hunter mode is not running", func() string {
			return NewBugHunterManager().Stop()
		}},
		{"bughunter.status.running", "Bug Hunter status: running", func() string {
			bh := NewBugHunterManager()
			bh.Start()
			return bh.Status()
		}},
		{"bughunter.status.stopped", "Bug Hunter status: not running", func() string {
			return NewBugHunterManager().Status()
		}},
		{"ultraplan.toggle.on", "Ultraplan mode enabled", func() string {
			return NewUltraplanManager().Toggle()
		}},
		{"ultraplan.toggle.off", "Ultraplan mode disabled", func() string {
			up := NewUltraplanManager()
			up.Toggle()
			return up.Toggle()
		}},
		{"ultraplan.status.on", "Ultraplan mode: enabled", func() string {
			up := NewUltraplanManager()
			up.Toggle()
			return up.Status()
		}},
		{"ultraplan.status.off", "Ultraplan mode: not enabled", func() string {
			return NewUltraplanManager().Status()
		}},
	}
	for _, p := range probes {
		out := p.out()
		if v := util.AssertChineseOnlyOnIndentedLines(out); v != "" {
			t.Errorf("[%s] 输出有未缩进的中文行（形态门不过）: %q\n完整输出:\n%s", p.name, v, out)
		}
		if !strings.Contains(out, p.want) {
			t.Errorf("[%s] 英文主行缺失（%q）—— 疑似退回「中文为主」\n完整输出:\n%s", p.name, p.want, out)
		}
	}
}

// TestW083B2b_FormGateCatchesChinesePrimary 判别性自证（形态门）：
// 改造前的「中文为主」原文形态必须被形态门打红；合规新文不得被误判。
func TestW083B2b_FormGateCatchesChinesePrimary(t *testing.T) {
	old := []string{
		"Bug Hunter 状态：运行中\n启动时间: 2026-01-01 00:00:00\n运行时长: 1s", // 改前 formatRunningStatus 原文形态
		"Ultraplan 模式：已启用\n启动时间：2026-01-01 00:00:00\n运行时长：1s\n已执行步骤：1",
		"Bug Hunter 模式已停止\n本次扫描：发现 0 个问题，已fixed 0，待处理 0",
	}
	for _, o := range old {
		if v := util.AssertChineseOnlyOnIndentedLines(o); v == "" {
			t.Errorf("形态门漏判了「中文为主」的旧形态（= 假绿）: %q", o)
		}
	}

	good := []string{
		util.Bi("Bug Hunter status: running", "Bug Hunter 状态：运行中"),
		util.BiSame("Ultraplan mode: enabled\nStarted: %s", "Ultraplan 模式：已启用\n启动时间：%s", "2026-01-01"),
		util.Bi("Bug Hunter mode started", "Bug Hunter 模式已启动"),
	}
	for _, g := range good {
		if v := util.AssertChineseOnlyOnIndentedLines(g); v != "" {
			t.Errorf("合规新文被误判（判据太宽）: %q", v)
		}
	}
}
