// Package validator ast_builder.go 命令安全 AST 解析框架 — 语法层（T1-P1-A4 /）。
//
// 本文件实现递归下降 AST 构建器，将 Token 列表解析为最小 AST。
// 聚焦安全检查必需的 5 类节点（SimpleCommand / Pipeline / AndOr / Subshell / FunctionDef），
// 不追求 bash 语法 100% 覆盖。
//
// 设计依据：
// - REF-外部-tree-sitter https://tree-sitter.github.io/tree-sitter/ @2026-07-20（AST 节点设计参考）
// - REF-外部-POSIX shell 语法规范 https://pubs.opengroup.org/onlinepubs/9699919799/utilities/V3_chap02.html @2026-07-20
//
// 关键约束：
// - CGO_ENABLED=0：纯 Go 标准库实现
// - 嵌套深度上限 32：防止恶意嵌套
// - 大括号不匹配：保守返回部分 AST（避免漏检）
package validator

import (
	"strings"
)

// CommandType AST 节点类型（最小集合，聚焦安全检查）。
type CommandType int

const (
	// NodeSimpleCommand 简单命令：rm -rf /
	NodeSimpleCommand CommandType = iota
	// NodePipeline 管道：cmd1 | cmd2
	NodePipeline
	// NodeAndOr 逻辑与/或：cmd1 && cmd2 || cmd3
	NodeAndOr
	// NodeSubshell 子 shell：( cmd1; cmd2 )
	NodeSubshell
	// NodeFunctionDef 函数定义：name() { body }
	NodeFunctionDef
)

// CommandNode AST 节点。
type CommandNode struct {
	// Type 节点类型。
	Type CommandType
	// Words 简单命令的单词（已去引号、去变量标记，保留 $VAR 字面）。
	Words []string
	// Raw 原始命令文本（用于错误信息）。
	Raw string
	// Redirects 重定向列表。
	Redirects []RedirectNode
	// Children 子命令（Pipeline/AndOr 的子节点）。
	Children []*CommandNode
	// Body 函数体（FunctionDef）/ 子 shell 内容（Subshell）。
	Body *CommandNode
	// FuncName 函数名（FunctionDef）。
	FuncName string
	// Background 是否后台执行（&）。
	Background bool
	// VarAssigns 变量赋值（VAR=value，前缀赋值）。
	VarAssigns []VarAssign
}

// RedirectNode 重定向节点。
type RedirectNode struct {
	// Op 操作符（> / >> / < / 2> / &> / 2>&1）。
	Op string
	// Target 目标文件；当 FDDup 为 true 时，Target 是**被复制的文件描述符**（如 "1"）。
	Target string
	// FD 文件描述符（0/1/2，默认 0 表示未指定）。
	FD int
	// FDDup 是否为「文件描述符复制」（`[n]>&m`，m 为数字）。
	//
	// 语义：把 fd m 复制到 fd n，**不打开、不创建、不写任何文件** ⇒ 不属于「写重定向」，
	// 不得进入危险写目标判定。由 isFDDupRedirectOp 在解析期一次性判定，
	// 消费点只读本字段，不各自重新嗅探 Op 字符串。
	FDDup bool
}

// VarAssign 变量赋值。
type VarAssign struct {
	// Name 变量名。
	Name string
	// Value 变量值。
	Value string
}

// isFDDupRedirectOp 判断重定向操作符是否为「文件描述符复制」（fd 复制）。
//
// 语义（POSIX "Duplicating File Descriptors" / bash 手册 "Redirection"）：
//
//	[n]>&m —— 把 fd m 复制到 fd n（m 必须是数字）。全过程不打开、不创建、不写任何文件。
//
// 因此这类重定向**没有路径写目标**，不得进入「危险写重定向」判定（根因）。
//
// ★ 关键边界（本机 bash 实测，2026-10-06）：
// - `2>&1` / `>&1` → fd 复制，不落盘；
// - `>&/tmp/f` → **会创建文件**（bash 把 `>&word` 视作 `&>word`）；
// - `2>& /tmp/f` → bash 报 "ambiguous redirect"，不落盘。
//
// ⇒ 只有 `>&` **后紧跟非空数字串**才算 fd 复制。`>&` 后为空（`>&` / `2>&`）或非数字
// （`>&file`）时返回 false —— 即使 bash 不落盘也按「写」处理，宁可多问不静默放行。
func isFDDupRedirectOp(op string) bool {
	i := strings.LastIndex(op, ">&")
	if i < 0 {
		return false
	}
	rest := op[i+2:]
	if rest == "" {
		return false
	}
	for j := 0; j < len(rest); j++ {
		if rest[j] < '0' || rest[j] > '9' {
			return false
		}
	}
	return true
}

// isFileWriteRedirect 是重定向节点「是否写文件」的**单一权威判定**，
// 供危险路径检查（checkDangerousRedirectAST）与写目标提取（BashWriteTargets）共用，
// 避免两处各自嗅探 Op 字符串而漂移（本仓 四套危险判定不一致的教训）。
//
// 排除两类「不写文件」的形态：
// - 读方向（`<` / `<<` / `<&`）：Op 不含 `>`；
// - fd 复制（`2>&1` / `>&2`）：读结构化字段 FDDup，不重新解析 Op。
func isFileWriteRedirect(r RedirectNode) bool {
	if r.FDDup {
		return false
	}
	return strings.Contains(r.Op, ">")
}

// maxASTDepth AST 节点深度上限（32，防止嵌套过深）。
const maxASTDepth = 32

// ASTBuilder 语法分析器（递归下降，纯 Go）。
type ASTBuilder struct {
	tokens []Token
	pos    int
	depth  int
}

// NewASTBuilder 创建语法分析器。
func NewASTBuilder(tokens []Token) *ASTBuilder {
	return &ASTBuilder{
		tokens: tokens,
		pos:    0,
		depth:  0,
	}
}

// Build 构建 AST。返回根节点（顶层命令）。
//
// 大括号不匹配时返回已构建的部分 AST（保守返回，避免漏检）。
// 节点深度超过 maxASTDepth（32）时停止递归（防止 fork 炸弹深度爆炸）。
func (b *ASTBuilder) Build() *CommandNode {
	if len(b.tokens) == 0 {
		return &CommandNode{Type: NodeSimpleCommand}
	}
	return b.parseAndOr()
}

// parseAndOr 解析 && / || / ; 顺序表达式（最低优先级）。
//
// 文法：andOr := pipeline ( (&& | || | ;) pipeline )*
// 注：; 视为顺序执行操作符，与 && / || 同优先级，统一构造 NodeAndOr。
func (b *ASTBuilder) parseAndOr() *CommandNode {
	b.depth++
	if b.depth > maxASTDepth {
		return &CommandNode{Type: NodeSimpleCommand}
	}
	defer func() { b.depth-- }()

	left := b.parsePipeline()
	if left == nil {
		return &CommandNode{Type: NodeSimpleCommand}
	}

	for b.pos < len(b.tokens) {
		tok := b.tokens[b.pos]
		if tok.Kind != TokenOperator {
			break
		}
		if tok.Value != "&&" && tok.Value != "||" && tok.Value != ";" {
			break
		}
		b.pos++ // 消耗 && / || / ;
		// 末尾 ; 不构造空子节点
		if b.pos >= len(b.tokens) || b.tokens[b.pos].Kind == TokenEOF {
			break
		}
		right := b.parsePipeline()
		if right == nil {
			break
		}
		left = &CommandNode{
			Type:     NodeAndOr,
			Children: []*CommandNode{left, right},
			Raw:      tok.Value,
		}
	}
	return left
}

// parsePipeline 解析管道表达式（中优先级）。
//
// 文法：pipeline := simpleOrSubshell ( | simpleOrSubshell )* [&]
func (b *ASTBuilder) parsePipeline() *CommandNode {
	left := b.parseSimpleOrSubshell()
	if left == nil {
		return nil
	}

	children := []*CommandNode{left}
	hasPipe := false
	for b.pos < len(b.tokens) {
		tok := b.tokens[b.pos]
		if tok.Kind != TokenPipe {
			break
		}
		b.pos++ // 消耗 |
		hasPipe = true
		right := b.parseSimpleOrSubshell()
		if right == nil {
			break
		}
		children = append(children, right)
	}

	// 检查后台执行标记 &
	if b.pos < len(b.tokens) && b.tokens[b.pos].Kind == TokenBackground {
		b.pos++ // 消耗 &
		// 标记最后一个子命令为后台执行
		if len(children) > 0 {
			children[len(children)-1].Background = true
		}
	}

	if !hasPipe {
		return left
	}
	return &CommandNode{
		Type:     NodePipeline,
		Children: children,
	}
}

// parseSimpleOrSubshell 解析简单命令或子 shell 或函数定义。
func (b *ASTBuilder) parseSimpleOrSubshell() *CommandNode {
	if b.pos >= len(b.tokens) {
		return nil
	}
	tok := b.tokens[b.pos]
	switch tok.Kind {
	case TokenSubshellStart:
		return b.parseSubshell()
	case TokenSubstitutionStart:
		// 命令替换/进程替换：作为简单命令的单词处理
		return b.parseSimpleCommand()
	case TokenWord:
		// 检查是否为函数定义（name() { ... }）
		if b.isFunctionDef() {
			return b.parseFunctionDef()
		}
		return b.parseSimpleCommand()
	case TokenVariable, TokenQuote:
		// 变量引用 / 引号字符串开头的命令（如 $x -rf /）
		return b.parseSimpleCommand()
	default:
		return nil
	}
}

// isFunctionDef 判断当前 Token 序列是否为函数定义（name() { ... }）。
//
// 模式：TokenWord + TokenSubshellStart + TokenSubshellEnd + TokenWord("{")
// 注：shell 中 name() { ... } 的 () 和 { 都是字面 Token。
func (b *ASTBuilder) isFunctionDef() bool {
	if b.pos+3 >= len(b.tokens) {
		return false
	}
	if b.tokens[b.pos].Kind != TokenWord {
		return false
	}
	if b.tokens[b.pos+1].Kind != TokenSubshellStart {
		return false
	}
	if b.tokens[b.pos+2].Kind != TokenSubshellEnd {
		return false
	}
	// { 可以是 TokenWord("{") 或单独的 TokenWord
	if b.tokens[b.pos+3].Kind != TokenWord {
		return false
	}
	return b.tokens[b.pos+3].Value == "{" || b.tokens[b.pos+3].Value == "{}"
}

// parseFunctionDef 解析函数定义。
//
// 文法：functionDef := name () { body }
// body 为递归 parseAndOr，直到遇到匹配的 }
func (b *ASTBuilder) parseFunctionDef() *CommandNode {
	funcName := b.tokens[b.pos].Value
	b.pos += 3 // 消耗 name ( )
	// 跳过 {
	if b.pos < len(b.tokens) && b.tokens[b.pos].Kind == TokenWord && b.tokens[b.pos].Value == "{" {
		b.pos++
	}

	// 递归解析函数体
	body := b.parseAndOr()

	// 跳过 }（如果存在）
	for b.pos < len(b.tokens) {
		tok := b.tokens[b.pos]
		if tok.Kind == TokenWord && tok.Value == "}" {
			b.pos++
			break
		}
		if tok.Kind == TokenOperator && tok.Value == ";" {
			b.pos++
			continue
		}
		break
	}

	return &CommandNode{
		Type:     NodeFunctionDef,
		FuncName: funcName,
		Body:     body,
	}
}

// parseSubshell 解析子 shell ( ... )。
func (b *ASTBuilder) parseSubshell() *CommandNode {
	b.pos++ // 消耗 (
	// 递归解析子 shell 内容
	body := b.parseAndOr()
	// 跳过 )（如果存在）
	if b.pos < len(b.tokens) && b.tokens[b.pos].Kind == TokenSubshellEnd {
		b.pos++
	}
	return &CommandNode{
		Type: NodeSubshell,
		Body: body,
	}
}

// parseSimpleCommand 解析简单命令（含变量赋值前缀 + 单词 + 重定向）。
func (b *ASTBuilder) parseSimpleCommand() *CommandNode {
	node := &CommandNode{Type: NodeSimpleCommand}
	var words []string

	for b.pos < len(b.tokens) {
		tok := b.tokens[b.pos]
		switch tok.Kind {
		case TokenWord:
			// 检查是否为变量赋值（VAR=value 形式）
			if assign, ok := parseVarAssign(tok.Value); ok && len(words) == 0 {
				node.VarAssigns = append(node.VarAssigns, assign)
				b.pos++
				continue
			}
			words = append(words, tok.Value)
			b.pos++
		case TokenQuote:
			// 引号字符串作为单词
			words = append(words, tok.Value)
			b.pos++
		case TokenVariable:
			// 变量引用作为单词（保留 $VAR 字面，供 AST 检查识别）
			words = append(words, tok.Value)
			b.pos++
		case TokenSubstitutionStart:
			// 命令替换/进程替换作为单词（记录原始标记）
			words = append(words, tok.Value)
			b.pos++
			// 递归消耗替换体（直到匹配的 SubstitutionEnd）
			b.skipSubstitutionBody()
		case TokenRedirect:
			b.parseRedirect(node)
		case TokenSubshellEnd:
			// 命令替换结束，交由上层处理
			return node.withWords(words)
		default:
			// 操作符 / 管道 / 后台 / EOF 等：结束简单命令
			return node.withWords(words)
		}
	}
	return node.withWords(words)
}

// withWords 返回带单词列表的节点（避免重复赋值）。
func (n *CommandNode) withWords(words []string) *CommandNode {
	n.Words = words
	return n
}

// parseRedirect 解析重定向（如 > /tmp/x 或 2> /tmp/err 或 2>&1）。
func (b *ASTBuilder) parseRedirect(node *CommandNode) {
	op := b.tokens[b.pos].Value
	b.pos++
	fd := 0
	// 从操作符提取文件描述符（如 2> 提取 2）
	if len(op) > 1 && op[0] >= '0' && op[0] <= '9' {
		fd = int(op[0] - '0')
	}
	// fd 复制（`2>&1` / `>&2`）：目标是一个**文件描述符**，不是路径。tokenizer 已把整个
	// 形态并入 Op（如 "2>&1"），它是**自包含**重定向 —— 不得把后续 token 当作 Target。
	//
	// 原缺陷：`2>&1` 写在末尾时，这里会取到 EOF ⇒ Target="" ⇒ isDangerousWriteTarget("")
	// 的空值兜底把它误判成「危险写重定向」而硬拒；且 `2>&1 > /etc/passwd` 会把真正的 `>`
	// 当成 Target 吞掉，导致真写重定向漏检。
	if isFDDupRedirectOp(op) {
		node.Redirects = append(node.Redirects, RedirectNode{
			Op:     op,
			Target: op[strings.LastIndex(op, ">&")+2:], // 被复制的 fd（如 "1"）
			FD:     fd,
			FDDup:  true,
		})
		return
	}
	if b.pos >= len(b.tokens) {
		return
	}
	target := b.tokens[b.pos].Value
	node.Redirects = append(node.Redirects, RedirectNode{
		Op:     op,
		Target: target,
		FD:     fd,
	})
	b.pos++
}

// skipSubstitutionBody 跳过命令替换体（直到匹配的 SubstitutionEnd）。
//
// 用于简单命令中的 $(...) / <(...) / >(...) 替换体，
// 不深度解析（避免过度复杂），仅跳过对应 Token。
func (b *ASTBuilder) skipSubstitutionBody() {
	depth := 1
	for b.pos < len(b.tokens) && depth > 0 {
		tok := b.tokens[b.pos]
		switch tok.Kind {
		case TokenSubstitutionStart, TokenSubshellStart:
			depth++
		case TokenSubstitutionEnd, TokenSubshellEnd:
			depth--
		}
		b.pos++
	}
}

// parseVarAssign 解析变量赋值（VAR=value 形式）。
//
// 返回 (assign, true) 表示是变量赋值；(VarAssign{}, false) 表示不是。
// VAR 必须是合法变量名（字母/下划线开头，后跟字母/数字/下划线）。
func parseVarAssign(s string) (VarAssign, bool) {
	idx := strings.IndexByte(s, '=')
	if idx <= 0 {
		return VarAssign{}, false
	}
	name := s[:idx]
	if !isValidVarName(name) {
		return VarAssign{}, false
	}
	value := s[idx+1:]
	return VarAssign{Name: name, Value: value}, true
}

// isValidVarName 判断字符串是否为合法变量名。
func isValidVarName(s string) bool {
	if len(s) == 0 {
		return false
	}
	// 首字符：字母或下划线
	first := s[0]
	if !((first >= 'a' && first <= 'z') || (first >= 'A' && first <= 'Z') || first == '_') {
		return false
	}
	// 后续字符：字母/数字/下划线
	for i := 1; i < len(s); i++ {
		ch := s[i]
		if !((ch >= 'a' && ch <= 'z') || (ch >= 'A' && ch <= 'Z') || (ch >= '0' && ch <= '9') || ch == '_') {
			return false
		}
	}
	return true
}

// WalkAST 深度优先遍历 AST，对每个节点调用 fn。
//
// 用于 AST 检查层（ast_checks.go）遍历所有节点查找危险模式。
// 若 fn 返回 false，停止遍历该分支。
func WalkAST(root *CommandNode, fn func(*CommandNode) bool) {
	if root == nil {
		return
	}
	if !fn(root) {
		return
	}
	for _, child := range root.Children {
		WalkAST(child, fn)
	}
	if root.Body != nil {
		WalkAST(root.Body, fn)
	}
}
