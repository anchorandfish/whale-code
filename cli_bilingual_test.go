package main

// cli_bilingual_test.go — **CLI 启动诊断面**的形态守卫。
//
// # 本批改了什么
//
// `main.go` 两处启动诊断从「中文为主」改为双语 SSOT（`internal/util/bilingual.go`：
// 英文主行 + 3 空格缩进中文辅行）：
// - 多实例并发写警告（`main()` 的 instanceLock 分支）；
// - 配置不可用拒绝启动（`refuseToStartOnUnusableConfig`，含「不可读」/「损坏」两支）。
//
// # 判定面（结构面）
//
// `main.go` 的**字符串字面量**里不得再出现「行内并列」形态（Han 紧邻 `/`）——
// 那是 明确否决的 `English / 中文` 写法（中文行无缩进、宽度翻倍）。
//
// # 为什么用「字面量级」而不是行级形态门
//
// `util.Bi(en, zh)` 的 `zh` 参数**天然不含缩进**（缩进由运行期 `IndentCopyLines` 加）。
// 行级门（`AssertChineseOnlyOnIndentedLines`）会把合法的 `zh` 参数误判为违规 ⇒
// 源码扫描必须用「Han 是否与 `/` 相邻」这一**字面量级**判据。
//
// # 判据独立（照 internal/util/bilingual_test.go:18-20 的分工原则）
//
// **字符集定义同源** ⇒ 走 `util.HasHan`；**判据逻辑独立** ⇒ 本测试自己写扫描循环，
// 不调用任何生产侧断言函数（否则"实现与判据共享 bug"会双双失明）。此处与
// `internal/tools/b2_toolmsg_bilingual_test.go` 各自独立成判，是有意为之，不是重复实现。
//
// # 判别力自证
//
// `TestW083B2a_CLIDetectorCatchesCounterExamples` 把改造前的**真实原文**喂给判据，
// 必须被抓到；改造后的真实新文必须判为合规（否则是假绿/假红）。

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

// cliInlineParallel 报告一个字面量是否是「行内并列」形态 —— 即被 否决的
// `English / 中文` 写法。判据 = `/` **两侧各紧邻一个空格**（形如 " / "），
// 且跨过空格后左右邻**至少一侧是汉字**。
//
// ★ 为什么必须要求 " / "（两侧都要空格）：只按「跳过空格后紧邻汉字」会大量误报
// 合法技术串 —— 实测踩中两处：
//
//	"（权限/IO）" —— '限' 紧邻 '/'，但无空格（是「词/词」技术写法，非行内并列）
//	"~/.whale/ 可能" —— 路径末尾 '/' 右侧是空格 + 汉字（空格只是断词）
//
// 判据随反例收紧（见 TestW083B2a_CLIDetectorCatchesCounterExamples 的负例）。
func cliInlineParallel(s string) bool {
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

// cliMainGoLiterals 解析 main.go，返回全部字符串字面量（去引号展开后的值）及其位置。
func cliMainGoLiterals(t *testing.T) []struct {
	val string
	pos string
} {
	t.Helper()
	const file = "main.go"
	if _, err := os.Stat(file); err != nil {
		t.Fatalf("main.go 不存在（测试 CWD 应为仓库根）: %v", err)
	}
	fset := token.NewFileSet()
	af, err := parser.ParseFile(fset, file, nil, 0)
	if err != nil {
		t.Fatalf("解析 main.go 失败: %v", err)
	}
	var out []struct {
		val string
		pos string
	}
	ast.Inspect(af, func(n ast.Node) bool {
		bl, ok := n.(*ast.BasicLit)
		if !ok || bl.Kind != token.STRING {
			return true
		}
		if v, err := strconv.Unquote(bl.Value); err == nil {
			out = append(out, struct {
				val string
				pos string
			}{v, fset.Position(bl.Pos()).String()})
		}
		return true
	})
	if len(out) == 0 {
		t.Fatal("main.go 未解析出任何字符串字面量（静默零命中 = 假绿）")
	}
	return out
}

// TestW083B2a_CLINoInlineParallelLiterals 源码结构门：main.go 不得残留「行内并列」字面量。
func TestW083B2a_CLINoInlineParallelLiterals(t *testing.T) {
	for _, lit := range cliMainGoLiterals(t) {
		if cliInlineParallel(lit.val) {
			t.Errorf("main.go: 行内并列文案未清除（Han 紧邻 '/'）: %q  @ %s", lit.val, lit.pos)
		}
	}
}

// TestW083B2a_CLIBilingualFormsInPlace 正向：两处启动诊断的**英文主行**必须作为字面量存在。
// 若有人把文案退回「中文为主」，这两串会消失 ⇒ 本测试红（补上一条结构门挡回归）。
func TestW083B2a_CLIBilingualFormsInPlace(t *testing.T) {
	lits := cliMainGoLiterals(t)
	joined := make([]string, 0, len(lits))
	for _, l := range lits {
		joined = append(joined, l.val)
	}
	all := strings.Join(joined, "\x00")
	for _, want := range []string{
		"⚠️ Another whale instance is already running (PID: %d)",
		"❌ Failed to load config: %v",
	} {
		if !strings.Contains(all, want) {
			t.Errorf("main.go 应含英文主行字面量 %q（双语 SSOT 形态）", want)
		}
	}
}

// TestW083B2a_CLIDetectorCatchesCounterExamples 判别性自证：行内并列必被抓、合规新文不得误判。
//
// ⚠️ 注意本门的**作用域**：`main.go` 改造前的形态是「中文为主」（纯中文、无 '/”），
// 那不是行内并列 ⇒ 本门的字面量判据**不覆盖**「退回中文为主」这类回归；后者由
// `TestW083B2a_CLIBilingualFormsInPlace`（英文主行字面量必须在）挡。两门互补，各挡一种回归。
func TestW083B2a_CLIDetectorCatchesCounterExamples(t *testing.T) {
	// 正例（必须抓）：行内并列 = 汉字紧邻 '/'
	bad := []string{
		"⚠️ 检测到另一个 whale 实例 / Another instance is running",
		"English / 中文",
		"Command / 命令: ls",
	}
	for _, b := range bad {
		if !cliInlineParallel(b) {
			t.Errorf("行内并列（Han 紧邻 '/'）必须被抓，否则判据不承重: %q", b)
		}
	}
	// 负例（不得误判）：改造后的真实新文 + 改造前的纯中文原文 + 两处**实测误报源**
	good := []string{
		"⚠️ Another whale instance is already running (PID: %d)\n  Concurrent writes to ~/.whale/ may cause data conflicts",
		"❌ Failed to load config: %v",
		"❌ 配置加载失败：%v", // 改造前原文：纯中文，无 '/' ⇒ 不该被行内并列门抓（它属于另一门）
		"配置文件存在但**无法读取**（权限/IO）。请修复后重试：",            // 误报源①：'限' 紧邻 '/' 但无空格
		"⚠️ ...数据冲突\n如确需多开...\n（路径 ~/.whale/ 可能冲突）", // 误报源②：路径 '/' 右侧是空格+汉字
		"已落盘 %d/%d 个文件", // 语言中性：'/' 两侧是数字
	}
	for _, g := range good {
		if cliInlineParallel(g) {
			t.Errorf("合规新文/纯中文被误判为行内并列（判据太宽 = 假红）: %q", g)
		}
	}
}
