// b2_toolmsg_bilingual_test.go — 工具结果面双语守卫。
//
// 守 的形态 SSOT（internal/util/bilingual.go：英文主行 + 3 空格缩进中文辅行，
// 含汉字的行必须以 CopyIndent 开头），覆盖本批改动的工具结果消息：
//
//	A. 展示层（行为面）：Monitor 启动回执必须过形态门，且英文主行含两个模型入口原文；
//	B. 源码层（结构面）：本批 5 个被改文件里，不得再有「行内并列」（Han 紧邻 '/'）的字符串字面量；
//	C. 判别性自证（不红 = 假绿）：把改造前的**真实原文**喂给判据，必须被抓到。
//
// 判据与实现分离：扫描循环由本测试自己写，只有汉字判定走 util.HasHan（字符集同源，技能 §4）。
package tools

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"strconv"
	"strings"
	"testing"

	"github.com/anchorandfish/whale-code/internal/util"
)

// TestW083B2a_MonitorStartedResultIsBilingual Monitor 启动回执：英文主行（首行）
// 无汉字且含 "Monitor started"；中文辅行缩进且含 "监控已启动"；两个模型入口原文保留。
func TestW083B2a_MonitorStartedResultIsBilingual(t *testing.T) {
	t.Parallel()
	out := monitorStartedResult("mon_1", "watch server", "node server.js")

	if v := util.AssertChineseOnlyOnIndentedLines(out); v != "" {
		t.Errorf("Monitor 回执形态违规行: %q\n全量:\n%s", v, out)
	}
	firstLine := strings.SplitN(out, "\n", 2)[0]
	if util.HasHan(firstLine) {
		t.Errorf("英文主行不得含汉字: %q", firstLine)
	}
	if !strings.Contains(firstLine, "Monitor started") {
		t.Errorf("英文主行应含 'Monitor started': %q", firstLine)
	}
	if !strings.Contains(out, util.CopyIndent+"📡 监控已启动") {
		t.Errorf("应有缩进的中文辅行（含「监控已启动」——monitor_test.go 钉该子串）:\n%s", out)
	}
	// 两个模型侧入口必须逐字保留（改文案不得弄丢模型能走的真实出口）
	for _, want := range []string{`Read output (model): MonitorTail {"monitor_id": "mon_1"}`, "Stop (user): /monitors stop mon_1"} {
		if !strings.Contains(out, want) {
			t.Errorf("模型/用户入口原文丢失: %q\n全量:\n%s", want, out)
		}
	}
}

// inlineParallelLiteral 报告一个字面量是否是「行内并列」形态 —— 即 否决的
// `English / 中文` 写法。判据 = '/' **两侧各紧邻一个空格**（形如 " / "），
// 且跨过空格后左右邻**至少一侧是汉字**。
//
// ★ 要求 " / "（两侧都要空格）是**实测收紧**的结果：只按「跳过空格后紧邻汉字」
// 会误报合法技术串 —— 实测两例（2026-10-12，见 回执 §4）：
//
//	"（权限/IO）" —— '限' 紧邻 '/'，但无空格（「词/词」技术写法，非行内并列）
//	"~/.whale/ 可能" —— 路径末尾 '/' 右侧空格只是断词
//
// 与 `cli_bilingual_test.go` 的判据同规则（各自独立实现，符合
// util/bilingual_test.go:18-20「字符集同源、判据逻辑独立」的分工）。
func inlineParallelLiteral(s string) bool {
	rs := []rune(s)
	for i := 1; i < len(rs)-1; i++ {
		if rs[i] != '/' || rs[i-1] != ' ' || rs[i+1] != ' ' {
			continue
		}
		if hanPastSpaces(rs, i, -1) || hanPastSpaces(rs, i, +1) {
			return true
		}
	}
	return false
}

// hanPastSpaces 自 from 沿 dir 跳过空格，报告首个非空格字符是否是汉字。
func hanPastSpaces(rs []rune, from, dir int) bool {
	for j := from + dir; j >= 0 && j < len(rs); j += dir {
		if rs[j] == ' ' {
			continue
		}
		return util.HasHan(string(rs[j]))
	}
	return false
}

// TestW083B2a_NoInlineParallelLiteralsInTouchedFiles 源码结构门：本批被改的
// 5 个 tools 源文件里，不得再残留「行内并列」的字符串字面量。
// 用 go/ast 解析真实源码（不是行扫描，注释与多行拼接都能正确还原）。
func TestW083B2a_NoInlineParallelLiteralsInTouchedFiles(t *testing.T) {
	t.Parallel()
	files := []string{"bash.go", "executor.go", "file.go", "apply_diff.go", "monitor.go"}
	fset := token.NewFileSet()
	for _, f := range files {
		af, err := parser.ParseFile(fset, f, nil, 0)
		if err != nil {
			t.Fatalf("解析 %s 失败（测试 CWD 应为包目录）: %v", f, err)
		}
		ast.Inspect(af, func(n ast.Node) bool {
			bl, ok := n.(*ast.BasicLit)
			if !ok || bl.Kind != token.STRING {
				return true
			}
			val, err := strconv.Unquote(bl.Value)
			if err != nil {
				return true
			}
			if inlineParallelLiteral(val) {
				t.Errorf("%s: 行内并列文案未清除（Han 紧邻 '/'）: %q  @ %s",
					f, val, fset.Position(bl.Pos()))
			}
			return true
		})
	}
}

// TestW083B2a_InlineParallelDetectorCatchesCounterExamples 判别性自证：
// 改造前的真实原文必须被判据抓到；改造后的真实新文必须判为合规。
// 反例取自 `git show HEAD:` 版（见 回执 §3），不是臆造。
func TestW083B2a_InlineParallelDetectorCatchesCounterExamples(t *testing.T) {
	t.Parallel()
	olds := []string{
		// bash.go:808 改前
		"sandbox denied / 沙箱拒绝\nCommand / 命令: %s\nReason / 原因: %s\nNo command was run / 命令未执行",
		// executor.go:1002 改前
		"user denied tool execution / 用户拒绝工具执行",
		// 通用行内并列
		"[Y] Allow once / 允许本次",
	}
	for _, old := range olds {
		if !inlineParallelLiteral(old) {
			t.Errorf("改前行内并列文案必须被判违规: %q", old)
		}
	}
	news := []string{
		// bash.go:808 改后（英文主行）
		"sandbox denied\nCommand: %s\nReason: %s\nNo command was run.",
		// bash.go:808 改后（中文辅行 —— 含 Han 但无 '/'）
		"沙箱拒绝\n命令：%s\n原因：%s\n命令未执行。",
		// 语言中性数据（%d/%d 的斜杠两侧是拉丁字母/数字，不是 Han）
		"（已落盘 %d/%d 个文件；本次未做回滚 —— 仓库无 git）",
		// 实测误报源①：'限' 紧邻 '/' 但**无空格** —— 技术串「词/词」，非行内并列
		"配置文件存在但**无法读取**（权限/IO）。请修复后重试：",
		// 实测误报源②：路径末尾 '/' 右侧空格+汉字只是**断词**，非 " / " 形态
		"多实例并发写 ~/.whale/ 可能导致数据冲突",
	}
	for _, nw := range news {
		if inlineParallelLiteral(nw) {
			t.Errorf("改造后的新文被误判为行内并列（判据太宽 = 假红）: %q", nw)
		}
	}
	// 形态门自证：改前原文过不了形态门（含汉字行未缩进）
	if v := util.AssertChineseOnlyOnIndentedLines(olds[0]); v == "" {
		t.Error("改前行内并列文案必须过不了形态门")
	}
}

// TestW083B2a_TouchedFilesExist 前置自检：源码门扫的文件必须真的在（否则静默零命中 = 假绿）。
func TestW083B2a_TouchedFilesExist(t *testing.T) {
	t.Parallel()
	for _, f := range []string{"bash.go", "executor.go", "file.go", "apply_diff.go", "monitor.go"} {
		if _, err := os.Stat(f); err != nil {
			t.Errorf("源码门的目标文件不存在（CWD 不对？静默零命中 = 假绿）: %s: %v", f, err)
		}
	}
}
