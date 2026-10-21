// Package bash — Bash 命令 AST 解析（T1）。
//
// C12 冲突解决：Bash AST 解析在 tools/bash 模块实现，sandbox 会复用。
//
// 解析策略（降级链）：
//   - 默认：纯 Go tokenizer/解析器（本文件），无需 cgo，即"regex 降级路径"。
//   - 可选：tree-sitter-bash cgo 绑定（当依赖可用且构建标签 bash_ts 启用时），
//     通过 ast_parser_cgo.go 提供，覆写 ParseCommand。当前 go.mod 未引入
//     tree-sitter 依赖，故默认走 tokenizer 路径。
//
// tokenizer 不追求完整 bash 语法（那需要 tree-sitter），而是为安全检查与
// 路径提取提供"足够准确"的命令结构：按 ; && || | 拆分复合命令，
// 识别命令名/参数/重定向，正确处理单双引号与反斜杠转义。
package bash

import (
	"context"
	"fmt"
	"strings"
)

// CommandAST 命令 AST（解析结果）。
type CommandAST struct {
	Source   string           // 原始命令文本
	Commands []*SingleCommand // 拆分后的命令列表
}

// SingleCommand 单条命令。
type SingleCommand struct {
	Name      string      // 命令名（已去引号/转义）
	Args      []string    // 参数列表（已去引号/转义）
	RawArgs   []string    // 参数原始文本（保留引号，用于安全检查）
	Redirects []*Redirect // 重定向
	RawText   string      // 该命令的原始文本片段
	Position  int         // 在源命令中的字节偏移
}

// Redirect 重定向。
type Redirect struct {
	Op     string // ">", ">>", "<", ">&", "2>", "2>>", "<&" 等
	Target string // 目标路径（已去引号）
}

// ParseCommand 解析命令为 AST。
//
// ctx 用于未来 tree-sitter 解析的可取消性（当前 tokenizer 路径同步执行）。
// 返回的 CommandAST 含拆分后的命令列表；解析失败返回 error。
func ParseCommand(ctx context.Context, command string) (*CommandAST, error) {
	if command == "" {
		return &CommandAST{Source: command}, nil
	}
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	default:
	}
	p := &parser{src: command}
	ast := p.parseProgram()
	ast.Source = command
	return ast, nil
}

// parser 手写递进 tokenizer + 命令拆分器。
type parser struct {
	src string
	pos int
}

// parseProgram 解析整个程序，按操作符拆分命令。
func (p *parser) parseProgram() *CommandAST {
	ast := &CommandAST{}
	for !p.atEnd() {
		// 跳过空白与换行
		p.skipWhitespaceAndNewlines()
		if p.atEnd() {
			break
		}
		// 跳过行间注释
		if p.peek() == '#' {
			p.skipToEOL()
			continue
		}
		cmd := p.parseCommand()
		if cmd != nil && (cmd.Name != "" || len(cmd.Args) > 0 || len(cmd.Redirects) > 0) {
			ast.Commands = append(ast.Commands, cmd)
		}
		// 跳过命令分隔符（; && || | 换行），由 parseCommand 返回后消费
		p.consumeSeparator()
	}
	return ast
}

// parseCommand 解析单条命令，直到遇到分隔符 (; && || | 换行) 或结尾。
func (p *parser) parseCommand() *SingleCommand {
	start := p.pos
	cmd := &SingleCommand{Position: start}
	first := true

	for !p.atEnd() {
		p.skipSpaces()
		if p.atEnd() {
			break
		}
		ch := p.peek()

		// 分隔符 → 命令结束
		if isSeparator(ch) {
			break
		}

		// 重定向
		if r := p.tryParseRedirect(); r != nil {
			cmd.Redirects = append(cmd.Redirects, r)
			continue
		}

		// 复合操作符（&& || 等）作为隐式分隔
		if op := p.peekOperator(); op != "" {
			break
		}

		// 读取一个 token（参数或命令名）
		raw, val := p.readToken()
		if raw == "" {
			// 遇到无法识别的字符（如裸括号），跳过避免死循环
			p.pos++
			continue
		}
		if first {
			cmd.Name = val
			cmd.RawArgs = append(cmd.RawArgs, raw)
			first = false
		} else {
			cmd.Args = append(cmd.Args, val)
			cmd.RawArgs = append(cmd.RawArgs, raw)
		}
	}

	cmd.RawText = p.src[start:p.pos]
	return cmd
}

// tryParseRedirect 尝试解析重定向（>, >>, <, 2>, &> 等）。
// 返回 nil 表示当前不是重定向。
func (p *parser) tryParseRedirect() *Redirect {
	save := p.pos
	// 可选 fd 前缀：数字或 &
	fdPrefix := ""
	if p.peek() == '&' {
		// &> 或 &>> 全部重定向
		fdPrefix = "&"
		p.pos++
		if p.peek() == '>' {
			p.pos++
			op := "&>"
			if p.peek() == '>' {
				p.pos++
				op = "&>>"
			}
			p.skipSpaces()
			_, target := p.readToken()
			return &Redirect{Op: op, Target: target}
		}
		// 不是重定向，回退
		p.pos = save
		return nil
	}
	if isDigit(p.peek()) {
		j := p.pos
		for isDigit(p.src[j]) {
			j++
			if j >= len(p.src) {
				break
			}
		}
		if j < len(p.src) && (p.src[j] == '>' || p.src[j] == '<') {
			fdPrefix = p.src[p.pos:j]
			p.pos = j
		}
	}

	ch := p.peek()
	if ch != '>' && ch != '<' {
		if fdPrefix != "" {
			p.pos = save
		}
		return nil
	}

	op := fdPrefix + string(ch)
	p.pos++
	if p.peek() == ch {
		// >> 或 <<
		op += string(ch)
		p.pos++
	}
	if p.peek() == '&' {
		// >& 或 <&
		op += "&"
		p.pos++
	}
	p.skipSpaces()
	if isSeparator(p.peek()) || p.atEnd() {
		// 重定向无目标
		return &Redirect{Op: op, Target: ""}
	}
	_, target := p.readToken()
	return &Redirect{Op: op, Target: target}
}

// peekOperator 查看复合操作符（&& ||），不消费。
func (p *parser) peekOperator() string {
	if p.pos+1 < len(p.src) {
		two := p.src[p.pos : p.pos+2]
		switch two {
		case "&&", "||":
			return two
		}
	}
	return ""
}

// consumeSeparator 消费命令分隔符（; && || | 换行）。
func (p *parser) consumeSeparator() {
	p.skipSpaces()
	if p.atEnd() {
		return
	}
	if op := p.peekOperator(); op != "" {
		p.pos += 2
		return
	}
	ch := p.peek()
	if ch == ';' || ch == '|' || ch == '\n' {
		p.pos++
	}
}

// readToken 读取一个 token，返回 (原始文本, 去引号值)。
// 处理单引号、双引号、反引号、反斜杠转义、命令替换 $(...) 与 ${...}（原样保留标记）。
// 引号 span 在 switch 内被完整消费，故循环顶部始终处于"引号外"，
// 空白/分隔符/操作符在顶部即构成词边界。
func (p *parser) readToken() (string, string) {
	var raw, val strings.Builder
	start := p.pos

	for !p.atEnd() {
		ch := p.peek()
		// 词边界：空白/分隔符/操作符/重定向起始（引号内已在 switch 消费，不会到达此处）
		if isSeparator(ch) {
			break
		}
		if ch == ' ' || ch == '\t' {
			break
		}
		if ch == ';' || ch == '|' {
			break
		}
		if op := p.peekOperator(); op != "" {
			break
		}
		if ch == '>' || ch == '<' {
			break
		}

		switch ch {
		case '\'':
			// 单引号：原样保留内容（不解析转义）
			p.pos++
			for !p.atEnd() && p.peek() != '\'' {
				val.WriteByte(p.peek())
				p.pos++
			}
			if !p.atEnd() {
				p.pos++ // 闭合引号
			}
		case '"':
			// 双引号：保留 $ 与 \ 转义语义，但去除引号本身
			p.pos++
			for !p.atEnd() && p.peek() != '"' {
				c := p.peek()
				if c == '\\' && p.pos+1 < len(p.src) {
					// 双引号内仅 \$ ` " \ 换行 被转义
					nxt := p.src[p.pos+1]
					if nxt == '$' || nxt == '`' || nxt == '"' || nxt == '\\' || nxt == '\n' {
						val.WriteByte(nxt)
						p.pos += 2
						continue
					}
				}
				val.WriteByte(c)
				p.pos++
			}
			if !p.atEnd() {
				p.pos++ // 闭合引号
			}
		case '\\':
			// 反斜杠转义下一个字符
			if p.pos+1 < len(p.src) {
				val.WriteByte(p.src[p.pos+1])
				p.pos += 2
			} else {
				p.pos++
			}
		default:
			val.WriteByte(ch)
			p.pos++
		}
	}
	raw.WriteString(p.src[start:p.pos])
	return raw.String(), val.String()
}

// skipSpaces 跳过空格与制表符（不跳换行，换行是分隔符）。
func (p *parser) skipSpaces() {
	for !p.atEnd() {
		ch := p.peek()
		if ch == ' ' || ch == '\t' {
			p.pos++
			continue
		}
		break
	}
}

// skipWhitespaceAndNewlines 跳过空白与换行。
func (p *parser) skipWhitespaceAndNewlines() {
	for !p.atEnd() {
		ch := p.peek()
		if ch == ' ' || ch == '\t' || ch == '\n' || ch == '\r' {
			p.pos++
			continue
		}
		break
	}
}

// skipToEOL 跳到行尾。
func (p *parser) skipToEOL() {
	for !p.atEnd() && p.peek() != '\n' {
		p.pos++
	}
}

func (p *parser) atEnd() bool { return p.pos >= len(p.src) }
func (p *parser) peek() byte {
	if p.pos >= len(p.src) {
		return 0
	}
	return p.src[p.pos]
}

// isSeparator 判断字符是否为命令分隔符（空白除外，单独处理）。
func isSeparator(ch byte) bool {
	switch ch {
	case 0, ';', '|', '\n', '\r':
		return true
	}
	return false
}

func isDigit(ch byte) bool { return ch >= '0' && ch <= '9' }

// AllCommandsSafe 复合命令逐条校验（对标同类 CLI all_commands.iter.all）。
// 每条命令都需通过 IsSafeToCallWithExec。
func (a *CommandAST) AllCommandsSafe() bool {
	for _, cmd := range a.Commands {
		if !a.IsSafeToCallWithExec(cmd) {
			return false
		}
	}
	return true
}

// IsSafeToCallWithExec 检查单条命令是否安全（对标同类 CLI）。
// 安全 = 命令名在白名单 + 重定向安全。
func (a *CommandAST) IsSafeToCallWithExec(cmd *SingleCommand) bool {
	if cmd == nil {
		return false
	}
	if !isKnownSafeCommand(cmd.Name, cmd.Args) {
		return false
	}
	for _, r := range cmd.Redirects {
		if !isSafeRedirect(r) {
			return false
		}
	}
	return true
}

// HasCDCompound 检测复合命令是否包含 cd（对标同类 CLI）。
// cd /etc && cat passwd 中的 cd 改变后续命令工作目录，需整体走 shell。
func (a *CommandAST) HasCDCompound() bool {
	for i, cmd := range a.Commands {
		if cmd.Name == "cd" && i < len(a.Commands)-1 {
			return true // cd 后还有命令
		}
	}
	return false
}

// isSafeRedirect 判断重定向是否安全（不指向系统敏感路径）。
func isSafeRedirect(r *Redirect) bool {
	if r == nil {
		return true
	}
	if r.Op != ">" && r.Op != ">>" && r.Op != "&>" && r.Op != "&>>" {
		return true // 非写重定向，视为安全
	}
	if r.Target == "" {
		return false
	}
	return !isSystemSensitivePath(r.Target)
}

// isSystemSensitivePath 判断是否为系统敏感路径（写重定向禁止）。
func isSystemSensitivePath(path string) bool {
	// 去除前导空格与引号
	t := strings.TrimSpace(path)
	t = strings.Trim(t, `"'`)
	lower := strings.ToLower(t)
	for _, p := range []string{"/etc/", "/proc/", "/sys/", "/dev/", "/boot/", "/usr/", "/bin/", "/sbin/", "/var/"} {
		if strings.HasPrefix(lower, p) || lower == strings.TrimRight(p, "/") {
			return true
		}
	}
	return false
}

// String 返回 AST 的可读表示（调试用）。
func (a *CommandAST) String() string {
	if a == nil || len(a.Commands) == 0 {
		return fmt.Sprintf("CommandAST(empty, source=%q)", a.Source)
	}
	var b strings.Builder
	fmt.Fprintf(&b, "CommandAST(%d commands):\n", len(a.Commands))
	for i, c := range a.Commands {
		fmt.Fprintf(&b, "  [%d] name=%q args=%v redirects=%d raw=%q\n", i, c.Name, c.Args, len(c.Redirects), c.RawText)
	}
	return b.String()
}
