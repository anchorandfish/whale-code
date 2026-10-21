// .go — 的**结构门（AST）**：禁止把散文关键词拒绝判据加回来。
//
// 为什么需要它（不是"顺手加个测试"）：这套散文判据**已经复发了两次**。
//
// - R4（S17 P3，2026-09-14）：先把「执行被安全策略拦截」从纯散文改成结构化合取，**特意保留**了
// "AI 自身拒绝"这一层的关键词判据（当时假定它没有结构化源）。
// - ：真机 T-1 证明那个假定不成立 —— 关键词把 T-1 的**期望输出**
// （「我没有这个工具，所以我无法调用」）判成了"AI 拒绝"，且弹出机制上无效的 force 提示，
// 把模型推向 T-1 票面明列的失败态②（幻觉）。
//
// ⇒ 真正的根治不是"把表调准"（那只是把误报往后推，见 `stream.go checkAISafety` 的反例纪律），
// 而是**换通道 + 装一道门**：本文件就是那道门。
//
// 为什么是 AST 门而不是"读文本 grep"：直接 `os.ReadFile` + `strings.Contains` 会被**文档注释**
// 误伤，而"记录这次删除的原因"恰恰是本单最该留下的东西（复发过两次的坑必须写明）。
// ⇒ 本门用 `go/parser` **只检查语法元素**：
//
// - `*ast.Ident` 的**标识符名** —— 挡住"把同一个表/函数原样加回来"。
// - `*ast.BasicLit` 的**字符串字面量** —— 挡住"换个名字但把同一批关键词/同一条可见文案加回来"。
//
// 注释不在检查范围内（分析类工具默认不收集注释）⇒ 允许"考古式"说明，禁止**可执行**的判据。
//
// ⚠️ **判别力自证的正确通道（照 的教训）**：本门用 `go/parser` 读**源码文件**，
// 属"运行期自读磁盘"型判据 ⇒ **`-overlay` 对它物理无效**（overlay 只替换编译期输入，
// 改不动它读到的字节）。⇒ 自证必须走**真实写回 + 无条件还原 + md5 校验**，
// 否则会得出"注入后仍全绿 ⇒ 这门是假的"的错误结论。
//
// 本门的边界（如实登记）：它挡的是"标识符 + 字面量"这一层。有人若改用**拼装**
// （如 `"我"+"无法"`）仍可绕过 —— 那一层靠 `domains/结构化判据与散文关键词.md`
// 与 `stream.go` 的反例纪律说明。门不是万能的，但"原样加回来"这条最常见的回退路径被物理堵死。
package ai

import (
	"go/ast"
	"go/parser"
	"go/token"
	"strings"
	"testing"
)

// bannedProseRefusalIdents 被 删除的散文关键词判据的标识符。
// 任何一个作为**标识符**重新出现，都意味着判据通道回退。
var bannedProseRefusalIdents = []string{
	"zhRefusalPatterns",  // 中文拒绝关键词表
	"enRefusalRe",        // 英文拒绝关键词正则
	"DetectRefusal",      // 散文判据入口
	"ForceExecutePrompt", // 旧：自动弹出的强制覆盖提示（机制上无效）
}

// bannedProseRefusalLiterals 被 删除的关键词 / 可见文案的**字面量**。
// 这一层挡"换个函数名、但把同一批词加回来"。
var bannedProseRefusalLiterals = []string{
	// 原 zhRefusalPatterns 的全部条目
	"我无法", "我不能", "我拒绝",
	"这会破坏", "这很危险",
	"不建议", "我不建议",
	"这可能导致",
	// 原 enRefusalRe 的捕获组
	"I refuse", "this will break", "this is dangerous", "not recommended",
	// 原 TUI 自动断言的可见文案
	"⚠️ AI refusal / AI 拒绝执行: refusal language detected / 检测到拒绝措辞: ",
	"To override an AI-only refusal",
}

// guardedSources 必须保持"无散文拒绝判据"的源码文件（相对本包目录）。
var guardedSources = []struct {
	path string
	why  string
}{
	{"refusal_handler.go", "散文关键词表与判据入口的原属地（ 已删）"},
	{"../tui/stream.go", "checkAISafety 是判据收口点（ 已删 AI-refusal 自动分支）"},
}

func TestN039_NoProseRefusalKeywordJudgement(t *testing.T) {
	fset := token.NewFileSet()
	for _, src := range guardedSources {
		// 不传 parser.ParseComments ⇒ 注释不进 AST，文档说明不算违规。
		f, err := parser.ParseFile(fset, src.path, nil, 0)
		if err != nil {
			// 解析失败就红：装作通过会让本门静默失效（判据不许 fail-open）。
			t.Fatalf("解析不了被守的源文件 %s（%s）: %v", src.path, src.why, err)
		}

		ast.Inspect(f, func(n ast.Node) bool {
			switch node := n.(type) {
			case *ast.Ident:
				for _, banned := range bannedProseRefusalIdents {
					if node.Name == banned {
						t.Errorf("%s 出现被  删除的散文拒绝判据标识符 %q（%s）——"+
							"散文关键词不得再作为拒绝判据：要断言「AI 拒绝」必须先有结构化事实源。"+
							"理由见 refusal_handler.go 包注释与 internal/tui/stream.go checkAISafety。",
							src.path, banned, src.why)
					}
				}
			case *ast.BasicLit:
				if node.Kind != token.STRING {
					return true
				}
				// node.Value 是**源码原文**（含引号）；直接小写包含匹配即可。
				// 这些关键词表原本就是大小写不敏感匹配，所以这里也做大小写不敏感。
				lit := strings.ToLower(node.Value)
				for _, banned := range bannedProseRefusalLiterals {
					if strings.Contains(lit, strings.ToLower(banned)) {
						t.Errorf("%s 出现被  删除的拒绝关键词/文案字面量 %q（%s）——"+
							"换名字绕过不算修好：判据的通道错了就得换通道，"+
							"加合取项只会把误报往后推（T-1 本身就是更宽关键词判据的反例）。",
							src.path, banned, src.why)
					}
				}
			}
			return true
		})
	}
}
