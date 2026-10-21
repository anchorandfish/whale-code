// Package validator ast_tokenizer.go 命令安全 AST 解析框架 — 词法层（T1-P1-A4 /）。
//
// 本文件实现纯 Go shell 词法分析器（tokenizer），将命令字符串解析为 Token 列表，
// 供 ast_builder.go 构建最小 AST。
//
// 设计依据：
// - REF-外部-POSIX shell token 规范 https://pubs.opengroup.org/onlinepubs/9699919799/utilities/V3_chap02.html @2026-07-20
// - REF-外部-Go text/scanner https://pkg.go.dev/text/scanner @2026-07-20（状态机词法模式参考）
// - REF-外部-tree-sitter https://tree-sitter.github.io/tree-sitter/ @2026-07-20（AST 节点设计参考）
//
// 关键约束：
// - CGO_ENABLED=0：纯 Go 标准库实现，无 cgo 依赖
// - 零新依赖：仅用 strings / unicode 标准库
// - 跨平台：Windows/Linux/macOS 行为一致
// - 错误恢复：未识别字符跳过，不中断 tokenize
// - 缓冲区上限：64KB 防止超长命令耗尽内存
package validator

import (
	"strings"
)

// TokenKind Token 类型（POSIX shell 词法分类，聚焦安全检查必需的最小集）。
//
// REF-外部-POSIX shell token 规范 https://pubs.opengroup.org/onlinepubs/9699919799/utilities/V3_chap02.html @2026-07-20
type TokenKind int

const (
	// TokenWord 普通单词：rm / -rf / /tmp 等。
	TokenWord TokenKind = iota
	// TokenOperator 操作符：; / && / ||
	TokenOperator
	// TokenRedirect 重定向操作符：> / >> / < / 2> / &>
	TokenRedirect
	// TokenPipe 管道操作符：|
	TokenPipe
	// TokenBackground 后台执行操作符：&
	TokenBackground
	// TokenSubshellStart 子 shell 起始：(
	TokenSubshellStart
	// TokenSubshellEnd 子 shell 结束：）
	TokenSubshellEnd
	// TokenSubstitutionStart 命令替换/进程替换起始：$( / <( / >(
	TokenSubstitutionStart
	// TokenSubstitutionEnd 命令替换/进程替换结束：)（与 SubshellEnd 共用字符，由上下文区分）。
	TokenSubstitutionEnd
	// TokenVariable 变量引用：$VAR / ${VAR}
	TokenVariable
	// TokenQuote 引号字符串：'...' / "..."（Value 已去引号）。
	TokenQuote
	// TokenComment 行内注释：#...
	TokenComment
	// TokenEOF 输入结束。
	TokenEOF
)

// Token 单个词法单元。
type Token struct {
	// Kind Token 类型。
	Kind TokenKind
	// Value Token 值（TokenQuote 类型时为引号内文本，已去引号）。
	Value string
	// Pos 字节偏移（用于错误定位）。
	Pos int
}

// maxTokenizerLen 词法分析器输入长度上限（64KB，防止超长命令耗尽内存）。
const maxTokenizerLen = 64 * 1024

// Tokenizer 词法分析器（状态机，纯 Go，无 cgo）。
//
// REF-外部-Go text/scanner https://pkg.go.dev/text/scanner @2026-07-20
type Tokenizer struct {
	input  string
	pos    int
	tokens []Token
}

// NewTokenizer 创建词法分析器。
//
// 输入超过 maxTokenizerLen（64KB）时自动截断，防止超长命令耗尽内存。
func NewTokenizer(input string) *Tokenizer {
	if len(input) > maxTokenizerLen {
		input = input[:maxTokenizerLen]
	}
	return &Tokenizer{
		input:  input,
		pos:    0,
		tokens: make([]Token, 0, 32),
	}
}

// Tokenize 执行词法分析，返回 Token 列表。
//
// 遇到未识别字符时跳过（错误恢复），不返回 error。
// 始终以 TokenEOF 结尾。
func (t *Tokenizer) Tokenize() []Token {
	for t.pos < len(t.input) {
		ch := t.input[t.pos]
		switch {
		case ch == ' ' || ch == '\t' || ch == '\n' || ch == '\r':
			// 空白字符跳过
			t.pos++
		case ch == '#':
			// 注释：从 # 到行尾
			t.tokenizeComment()
		case ch == '\'':
			// 单引号字符串（不展开变量）
			t.tokenizeSingleQuote()
		case ch == '"':
			// 双引号字符串（展开变量，但保留 $VAR 为字面供 AST 分析）
			t.tokenizeDoubleQuote()
		case ch == '|':
			// | 或 ||
			t.tokenizePipeOrOr()
		case ch == '&':
			// & 或 &&
			t.tokenizeAmpOrAnd()
		case ch == ';':
			// ; 操作符
			t.tokens = append(t.tokens, Token{Kind: TokenOperator, Value: ";", Pos: t.pos})
			t.pos++
		case ch == '(':
			// ( 子 shell 起始
			t.tokens = append(t.tokens, Token{Kind: TokenSubshellStart, Value: "(", Pos: t.pos})
			t.pos++
		case ch == ')':
			// ) 子 shell/命令替换结束
			t.tokens = append(t.tokens, Token{Kind: TokenSubshellEnd, Value: ")", Pos: t.pos})
			t.pos++
		case ch == '<':
			// < 或 <( 进程替换
			t.tokenizeLessOrProcSubst()
		case ch == '>':
			// > 或 >( 进程替换
			t.tokenizeGreaterOrProcSubst()
		case ch == '$':
			// $VAR / ${VAR} / $( 命令替换
			t.tokenizeDollar()
		default:
			// 普通单词
			t.tokenizeWord()
		}
	}
	t.tokens = append(t.tokens, Token{Kind: TokenEOF, Value: "", Pos: t.pos})
	return t.tokens
}

// tokenizeComment 解析注释（从 # 到行尾或字符串末尾）。
func (t *Tokenizer) tokenizeComment() {
	start := t.pos
	t.pos++ // 跳过 #
	for t.pos < len(t.input) {
		ch := t.input[t.pos]
		if ch == '\n' {
			break
		}
		t.pos++
	}
	t.tokens = append(t.tokens, Token{Kind: TokenComment, Value: t.input[start:t.pos], Pos: start})
}

// tokenizeSingleQuote 解析单引号字符串（不展开变量，原样保留）。
func (t *Tokenizer) tokenizeSingleQuote() {
	start := t.pos
	t.pos++ // 跳过起始 '
	var b strings.Builder
	for t.pos < len(t.input) {
		ch := t.input[t.pos]
		if ch == '\'' {
			// 闭合单引号
			t.pos++
			t.tokens = append(t.tokens, Token{Kind: TokenQuote, Value: b.String(), Pos: start})
			return
		}
		b.WriteByte(ch)
		t.pos++
	}
	// 未闭合：保守返回已扫描内容
	t.tokens = append(t.tokens, Token{Kind: TokenQuote, Value: b.String(), Pos: start})
}

// tokenizeDoubleQuote 解析双引号字符串（保留 $VAR 为字面供 AST 分析）。
//
// 注：双引号内的 $VAR 不展开为 TokenVariable，而是作为 TokenQuote 的字面内容保留。
// 这样 AST 检查能识别 "rm $var" 中的变量引用模式（与单引号一致处理）。
func (t *Tokenizer) tokenizeDoubleQuote() {
	start := t.pos
	t.pos++ // 跳过起始 "
	var b strings.Builder
	for t.pos < len(t.input) {
		ch := t.input[t.pos]
		if ch == '"' {
			// 闭合双引号
			t.pos++
			t.tokens = append(t.tokens, Token{Kind: TokenQuote, Value: b.String(), Pos: start})
			return
		}
		if ch == '\\' && t.pos+1 < len(t.input) {
			// 转义字符：保留转义后的字符
			next := t.input[t.pos+1]
			b.WriteByte(next)
			t.pos += 2
			continue
		}
		b.WriteByte(ch)
		t.pos++
	}
	// 未闭合：保守返回已扫描内容
	t.tokens = append(t.tokens, Token{Kind: TokenQuote, Value: b.String(), Pos: start})
}

// tokenizePipeOrOr 解析 | 或 ||。
func (t *Tokenizer) tokenizePipeOrOr() {
	start := t.pos
	if t.pos+1 < len(t.input) && t.input[t.pos+1] == '|' {
		// || 逻辑或
		t.tokens = append(t.tokens, Token{Kind: TokenOperator, Value: "||", Pos: start})
		t.pos += 2
	} else {
		// | 管道
		t.tokens = append(t.tokens, Token{Kind: TokenPipe, Value: "|", Pos: start})
		t.pos++
	}
}

// tokenizeAmpOrAnd 解析 & 或 &&。
func (t *Tokenizer) tokenizeAmpOrAnd() {
	start := t.pos
	if t.pos+1 < len(t.input) && t.input[t.pos+1] == '&' {
		// && 逻辑与
		t.tokens = append(t.tokens, Token{Kind: TokenOperator, Value: "&&", Pos: start})
		t.pos += 2
	} else {
		// & 后台执行
		t.tokens = append(t.tokens, Token{Kind: TokenBackground, Value: "&", Pos: start})
		t.pos++
	}
}

// tokenizeLessOrProcSubst 解析 < / </ << / <( 进程替换。
func (t *Tokenizer) tokenizeLessOrProcSubst() {
	start := t.pos
	if t.pos+1 < len(t.input) && t.input[t.pos+1] == '(' {
		// <( 进程替换
		t.tokens = append(t.tokens, Token{Kind: TokenSubstitutionStart, Value: "<(", Pos: start})
		t.pos += 2
		return
	}
	// < 或 << 重定向
	value := "<"
	if t.pos+1 < len(t.input) && t.input[t.pos+1] == '<' {
		value = "<<"
		t.pos += 2
	} else {
		t.pos++
	}
	t.tokens = append(t.tokens, Token{Kind: TokenRedirect, Value: value, Pos: start})
}

// tokenizeGreaterOrProcSubst 解析 > / >> / >( 进程替换 / >&N（fd 复制）。
func (t *Tokenizer) tokenizeGreaterOrProcSubst() {
	start := t.pos
	if t.pos+1 < len(t.input) && t.input[t.pos+1] == '(' {
		// >( 进程替换
		t.tokens = append(t.tokens, Token{Kind: TokenSubstitutionStart, Value: ">(", Pos: start})
		t.pos += 2
		return
	}
	var op strings.Builder
	op.WriteByte('>')
	t.pos++
	if t.pos < len(t.input) && t.input[t.pos] == '>' {
		op.WriteByte('>')
		t.pos++
	}
	// 并入 `>&` 文件描述符复制（>&2 / >&1），与 tokenizeFDRedirect 对 `2>&1` 的处理一致，
	// 使 fd 复制在「有/无数字前缀」两种形态下都成为**自包含** Redirect token
	// （目标是被复制的 fd，不是路径）。★ 只并入 `&` + 连续数字：
	// `>&file`（bash 视作 &>file，**会写文件**）不并入路径，仍按写重定向解析，不误放行。
	if t.pos < len(t.input) && t.input[t.pos] == '&' {
		op.WriteByte('&')
		t.pos++
		for t.pos < len(t.input) && t.input[t.pos] >= '0' && t.input[t.pos] <= '9' {
			op.WriteByte(t.input[t.pos])
			t.pos++
		}
	}
	t.tokens = append(t.tokens, Token{Kind: TokenRedirect, Value: op.String(), Pos: start})
}

// tokenizeDollar 解析 $VAR / ${VAR} / $( 命令替换。
func (t *Tokenizer) tokenizeDollar() {
	start := t.pos
	if t.pos+1 >= len(t.input) {
		// $ 单独出现：作为单词
		t.tokens = append(t.tokens, Token{Kind: TokenWord, Value: "$", Pos: start})
		t.pos++
		return
	}
	next := t.input[t.pos+1]
	switch {
	case next == '(':
		// $( 命令替换
		t.tokens = append(t.tokens, Token{Kind: TokenSubstitutionStart, Value: "$(", Pos: start})
		t.pos += 2
	case next == '{':
		// ${VAR} 变量展开
		t.tokenizeBraceVar(start)
	default:
		// $VAR 简单变量
		t.tokenizeSimpleVar(start)
	}
}

// tokenizeBraceVar 解析 ${VAR} 形式变量。
func (t *Tokenizer) tokenizeBraceVar(start int) {
	t.pos += 2 // 跳过 ${
	var b strings.Builder
	b.WriteString("${")
	for t.pos < len(t.input) {
		ch := t.input[t.pos]
		if ch == '}' {
			b.WriteByte('}')
			t.pos++
			t.tokens = append(t.tokens, Token{Kind: TokenVariable, Value: b.String(), Pos: start})
			return
		}
		b.WriteByte(ch)
		t.pos++
	}
	// 未闭合：保守返回
	t.tokens = append(t.tokens, Token{Kind: TokenVariable, Value: b.String(), Pos: start})
}

// tokenizeSimpleVar 解析 $VAR 形式变量（VAR 为字母/数字/下划线）。
func (t *Tokenizer) tokenizeSimpleVar(start int) {
	t.pos++ // 跳过 $
	var b strings.Builder
	b.WriteByte('$')
	for t.pos < len(t.input) {
		ch := t.input[t.pos]
		if isVarNameChar(ch) {
			b.WriteByte(ch)
			t.pos++
		} else {
			break
		}
	}
	t.tokens = append(t.tokens, Token{Kind: TokenVariable, Value: b.String(), Pos: start})
}

// isVarNameChar 判断字符是否为变量名合法字符（字母/数字/下划线）。
func isVarNameChar(ch byte) bool {
	return (ch >= 'a' && ch <= 'z') || (ch >= 'A' && ch <= 'Z') || (ch >= '0' && ch <= '9') || ch == '_'
}

// tokenizeWord 解析普通单词（含数字开头的文件描述符重定向如 2>）。
func (t *Tokenizer) tokenizeWord() {
	start := t.pos
	// 先检查是否为数字开头 + > / < 形式（如 2> / 1>>）
	if t.pos+1 < len(t.input) && t.input[t.pos] >= '0' && t.input[t.pos] <= '9' {
		next := t.input[t.pos+1]
		if next == '>' || next == '<' {
			// 数字文件描述符重定向（如 2>）
			t.tokenizeFDRedirect(start)
			return
		}
	}
	var b strings.Builder
	for t.pos < len(t.input) {
		ch := t.input[t.pos]
		if isWordTerminator(ch) {
			break
		}
		if ch == '\\' && t.pos+1 < len(t.input) {
			// 转义字符：保留转义后的字符
			b.WriteByte(t.input[t.pos+1])
			t.pos += 2
			continue
		}
		b.WriteByte(ch)
		t.pos++
	}
	if b.Len() > 0 {
		t.tokens = append(t.tokens, Token{Kind: TokenWord, Value: b.String(), Pos: start})
	}
}

// tokenizeFDRedirect 解析数字文件描述符重定向（如 2> / 1>> / 2>&1）。
func (t *Tokenizer) tokenizeFDRedirect(start int) {
	// 读取数字部分
	fdStart := t.pos
	for t.pos < len(t.input) && t.input[t.pos] >= '0' && t.input[t.pos] <= '9' {
		t.pos++
	}
	fd := t.input[fdStart:t.pos]
	// 读取 > / < 部分
	if t.pos >= len(t.input) {
		return
	}
	var op strings.Builder
	op.WriteString(fd)
	if t.input[t.pos] == '>' {
		op.WriteByte('>')
		t.pos++
		if t.pos < len(t.input) && t.input[t.pos] == '>' {
			op.WriteByte('>')
			t.pos++
		}
	} else if t.input[t.pos] == '<' {
		op.WriteByte('<')
		t.pos++
	}
	// 处理 2>&1 形式
	if t.pos < len(t.input) && t.input[t.pos] == '&' {
		op.WriteByte('&')
		t.pos++
		for t.pos < len(t.input) && t.input[t.pos] >= '0' && t.input[t.pos] <= '9' {
			op.WriteByte(t.input[t.pos])
			t.pos++
		}
	}
	t.tokens = append(t.tokens, Token{Kind: TokenRedirect, Value: op.String(), Pos: start})
}

// isWordTerminator 判断字符是否为单词终止符。
func isWordTerminator(ch byte) bool {
	switch ch {
	case ' ', '\t', '\n', '\r':
		return true
	case '|', '&', ';', '(', ')', '<', '>', '#':
		return true
	case '\'', '"':
		return true
	case '$':
		return true
	}
	return false
}
