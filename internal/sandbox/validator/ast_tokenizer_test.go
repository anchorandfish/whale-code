// Package validator ast_tokenizer_test.go 词法分析器单元测试（T1-P1-A4 /）。
package validator

import (
	"strings"
	"testing"
)

// TestTokenizer_Empty 验证空字符串输入。
func TestTokenizer_Empty(t *testing.T) {
	tok := NewTokenizer("")
	tokens := tok.Tokenize()
	if len(tokens) != 1 || tokens[0].Kind != TokenEOF {
		t.Errorf("empty input should return [TokenEOF], got %v", tokens)
	}
}

// TestTokenizer_SingleWord 验证单个单词。
func TestTokenizer_SingleWord(t *testing.T) {
	tok := NewTokenizer("rm")
	tokens := tok.Tokenize()
	if len(tokens) != 2 {
		t.Fatalf("expected 2 tokens, got %d", len(tokens))
	}
	if tokens[0].Kind != TokenWord || tokens[0].Value != "rm" {
		t.Errorf("expected TokenWord(rm), got %+v", tokens[0])
	}
	if tokens[1].Kind != TokenEOF {
		t.Errorf("expected TokenEOF at end, got %+v", tokens[1])
	}
}

// TestTokenizer_SimpleCommand 验证简单命令 rm -rf /。
func TestTokenizer_SimpleCommand(t *testing.T) {
	tok := NewTokenizer("rm -rf /")
	tokens := tok.Tokenize()
	if len(tokens) != 4 {
		t.Fatalf("expected 4 tokens, got %d: %+v", len(tokens), tokens)
	}
	expect := []struct {
		kind  TokenKind
		value string
	}{
		{TokenWord, "rm"},
		{TokenWord, "-rf"},
		{TokenWord, "/"},
		{TokenEOF, ""},
	}
	for i, e := range expect {
		if tokens[i].Kind != e.kind || tokens[i].Value != e.value {
			t.Errorf("token %d: expected %+v, got %+v", i, e, tokens[i])
		}
	}
}

// TestTokenizer_Pipe 验证管道 |。
func TestTokenizer_Pipe(t *testing.T) {
	tok := NewTokenizer("a | b")
	tokens := tok.Tokenize()
	if len(tokens) != 4 {
		t.Fatalf("expected 4 tokens, got %d", len(tokens))
	}
	if tokens[1].Kind != TokenPipe || tokens[1].Value != "|" {
		t.Errorf("expected TokenPipe, got %+v", tokens[1])
	}
}

// TestTokenizer_Background 验证后台执行 &。
func TestTokenizer_Background(t *testing.T) {
	tok := NewTokenizer("a &")
	tokens := tok.Tokenize()
	if len(tokens) != 3 {
		t.Fatalf("expected 3 tokens, got %d", len(tokens))
	}
	if tokens[1].Kind != TokenBackground || tokens[1].Value != "&" {
		t.Errorf("expected TokenBackground, got %+v", tokens[1])
	}
}

// TestTokenizer_And 验证逻辑与 &&。
func TestTokenizer_And(t *testing.T) {
	tok := NewTokenizer("a && b")
	tokens := tok.Tokenize()
	if len(tokens) != 4 {
		t.Fatalf("expected 4 tokens, got %d", len(tokens))
	}
	if tokens[1].Kind != TokenOperator || tokens[1].Value != "&&" {
		t.Errorf("expected TokenOperator(&&), got %+v", tokens[1])
	}
}

// TestTokenizer_Or 验证逻辑或 ||。
func TestTokenizer_Or(t *testing.T) {
	tok := NewTokenizer("a || b")
	tokens := tok.Tokenize()
	if len(tokens) != 4 {
		t.Fatalf("expected 4 tokens, got %d", len(tokens))
	}
	if tokens[1].Kind != TokenOperator || tokens[1].Value != "||" {
		t.Errorf("expected TokenOperator(||), got %+v", tokens[1])
	}
}

// TestTokenizer_Redirect 验证重定向 >。
func TestTokenizer_Redirect(t *testing.T) {
	tok := NewTokenizer("a > /tmp/x")
	tokens := tok.Tokenize()
	if len(tokens) != 4 {
		t.Fatalf("expected 4 tokens, got %d", len(tokens))
	}
	if tokens[1].Kind != TokenRedirect || tokens[1].Value != ">" {
		t.Errorf("expected TokenRedirect(>), got %+v", tokens[1])
	}
	if tokens[2].Kind != TokenWord || tokens[2].Value != "/tmp/x" {
		t.Errorf("expected TokenWord(/tmp/x), got %+v", tokens[2])
	}
}

// TestTokenizer_FDRedirect 验证文件描述符重定向 2>。
func TestTokenizer_FDRedirect(t *testing.T) {
	tok := NewTokenizer("cmd 2> /tmp/err")
	tokens := tok.Tokenize()
	// 期望：[TokenWord(cmd), TokenRedirect(2>), TokenWord(/tmp/err), TokenEOF]
	if len(tokens) != 4 {
		t.Fatalf("expected 4 tokens, got %d: %+v", len(tokens), tokens)
	}
	if tokens[1].Kind != TokenRedirect || tokens[1].Value != "2>" {
		t.Errorf("expected tokens[1]=TokenRedirect(2>), got %+v", tokens[1])
	}
	if tokens[2].Kind != TokenWord || tokens[2].Value != "/tmp/err" {
		t.Errorf("expected tokens[2]=TokenWord(/tmp/err), got %+v", tokens[2])
	}
}

// TestTokenizer_SingleQuote 验证单引号字符串。
func TestTokenizer_SingleQuote(t *testing.T) {
	tok := NewTokenizer("'rm -rf'")
	tokens := tok.Tokenize()
	if len(tokens) != 2 {
		t.Fatalf("expected 2 tokens, got %d", len(tokens))
	}
	if tokens[0].Kind != TokenQuote || tokens[0].Value != "rm -rf" {
		t.Errorf("expected TokenQuote(rm -rf), got %+v", tokens[0])
	}
}

// TestTokenizer_DoubleQuote 验证双引号字符串。
func TestTokenizer_DoubleQuote(t *testing.T) {
	tok := NewTokenizer(`"rm $var"`)
	tokens := tok.Tokenize()
	if len(tokens) != 2 {
		t.Fatalf("expected 2 tokens, got %d", len(tokens))
	}
	if tokens[0].Kind != TokenQuote || tokens[0].Value != "rm $var" {
		t.Errorf("expected TokenQuote(rm $var), got %+v", tokens[0])
	}
}

// TestTokenizer_DoubleQuoteEscape 验证双引号转义。
func TestTokenizer_DoubleQuoteEscape(t *testing.T) {
	tok := NewTokenizer(`"a\"b"`)
	tokens := tok.Tokenize()
	if len(tokens) != 2 {
		t.Fatalf("expected 2 tokens, got %d", len(tokens))
	}
	if tokens[0].Kind != TokenQuote || tokens[0].Value != `a"b` {
		t.Errorf("expected TokenQuote(a\"b), got %+v", tokens[0])
	}
}

// TestTokenizer_Variable 验证 $VAR 变量。
func TestTokenizer_Variable(t *testing.T) {
	tok := NewTokenizer("$HOME")
	tokens := tok.Tokenize()
	if len(tokens) != 2 {
		t.Fatalf("expected 2 tokens, got %d", len(tokens))
	}
	if tokens[0].Kind != TokenVariable || tokens[0].Value != "$HOME" {
		t.Errorf("expected TokenVariable($HOME), got %+v", tokens[0])
	}
}

// TestTokenizer_BraceVariable 验证 ${VAR} 变量。
func TestTokenizer_BraceVariable(t *testing.T) {
	tok := NewTokenizer("${HOME}")
	tokens := tok.Tokenize()
	if len(tokens) != 2 {
		t.Fatalf("expected 2 tokens, got %d", len(tokens))
	}
	if tokens[0].Kind != TokenVariable || tokens[0].Value != "${HOME}" {
		t.Errorf("expected TokenVariable(${HOME}), got %+v", tokens[0])
	}
}

// TestTokenizer_CommandSubstitution 验证 $(...) 命令替换。
func TestTokenizer_CommandSubstitution(t *testing.T) {
	tok := NewTokenizer("$(whoami)")
	tokens := tok.Tokenize()
	if len(tokens) != 4 {
		t.Fatalf("expected 4 tokens, got %d: %+v", len(tokens), tokens)
	}
	if tokens[0].Kind != TokenSubstitutionStart || tokens[0].Value != "$(" {
		t.Errorf("expected TokenSubstitutionStart($(), got %+v", tokens[0])
	}
	if tokens[1].Kind != TokenWord || tokens[1].Value != "whoami" {
		t.Errorf("expected TokenWord(whoami), got %+v", tokens[1])
	}
	// 注：) 同时用于子 shell 和命令替换的关闭，统一为 TokenSubshellEnd
	if tokens[2].Kind != TokenSubshellEnd {
		t.Errorf("expected TokenSubshellEnd (closes substitution), got %+v", tokens[2])
	}
}

// TestTokenizer_ProcessSubstitutionIn 验证 <(...) 进程替换。
func TestTokenizer_ProcessSubstitutionIn(t *testing.T) {
	tok := NewTokenizer("<(cat /etc/shadow)")
	tokens := tok.Tokenize()
	if len(tokens) < 2 {
		t.Fatalf("expected >=2 tokens, got %d", len(tokens))
	}
	if tokens[0].Kind != TokenSubstitutionStart || tokens[0].Value != "<(" {
		t.Errorf("expected TokenSubstitutionStart(<(), got %+v", tokens[0])
	}
}

// TestTokenizer_ProcessSubstitutionOut 验证 >(...) 进程替换。
func TestTokenizer_ProcessSubstitutionOut(t *testing.T) {
	tok := NewTokenizer(">(tee log)")
	tokens := tok.Tokenize()
	if len(tokens) < 2 {
		t.Fatalf("expected >=2 tokens, got %d", len(tokens))
	}
	if tokens[0].Kind != TokenSubstitutionStart || tokens[0].Value != ">(" {
		t.Errorf("expected TokenSubstitutionStart(>(), got %+v", tokens[0])
	}
}

// TestTokenizer_Comment 验证注释。
func TestTokenizer_Comment(t *testing.T) {
	tok := NewTokenizer("rm # this is a comment")
	tokens := tok.Tokenize()
	if len(tokens) != 3 {
		t.Fatalf("expected 3 tokens, got %d: %+v", len(tokens), tokens)
	}
	if tokens[1].Kind != TokenComment {
		t.Errorf("expected TokenComment, got %+v", tokens[1])
	}
	if !strings.HasPrefix(tokens[1].Value, "#") {
		t.Errorf("comment should start with #, got %q", tokens[1].Value)
	}
}

// TestTokenizer_Subshell 验证子 shell 括号。
func TestTokenizer_Subshell(t *testing.T) {
	tok := NewTokenizer("( rm -rf / )")
	tokens := tok.Tokenize()
	if len(tokens) != 6 {
		t.Fatalf("expected 6 tokens, got %d: %+v", len(tokens), tokens)
	}
	if tokens[0].Kind != TokenSubshellStart {
		t.Errorf("expected TokenSubshellStart, got %+v", tokens[0])
	}
	if tokens[4].Kind != TokenSubshellEnd {
		t.Errorf("expected TokenSubshellEnd, got %+v", tokens[4])
	}
}

// TestTokenizer_Semicolon 验证分号。
func TestTokenizer_Semicolon(t *testing.T) {
	tok := NewTokenizer("a; b")
	tokens := tok.Tokenize()
	if len(tokens) != 4 {
		t.Fatalf("expected 4 tokens, got %d", len(tokens))
	}
	if tokens[1].Kind != TokenOperator || tokens[1].Value != ";" {
		t.Errorf("expected TokenOperator(;), got %+v", tokens[1])
	}
}

// TestTokenizer_Overlong 验证超长输入截断。
func TestTokenizer_Overlong(t *testing.T) {
	long := strings.Repeat("a", maxTokenizerLen+100)
	tok := NewTokenizer(long)
	// 截断后输入长度应为 maxTokenizerLen
	if len(tok.input) != maxTokenizerLen {
		t.Errorf("expected input truncated to %d, got %d", maxTokenizerLen, len(tok.input))
	}
}

// TestTokenizer_ForkBombClassic 验证经典 fork 炸弹 :(){ :|:& };:。
func TestTokenizer_ForkBombClassic(t *testing.T) {
	tok := NewTokenizer(":(){ :|:& };:")
	tokens := tok.Tokenize()
	// 应能正确 tokenize（包含函数定义结构）
	if len(tokens) < 5 {
		t.Fatalf("expected >=5 tokens for fork bomb, got %d: %+v", len(tokens), tokens)
	}
	// 第一个 token 应是 TokenWord(":")
	if tokens[0].Kind != TokenWord || tokens[0].Value != ":" {
		t.Errorf("expected TokenWord(:), got %+v", tokens[0])
	}
}

// TestTokenizer_NestedSubstitution 验证嵌套命令替换 $(echo $(date))。
func TestTokenizer_NestedSubstitution(t *testing.T) {
	tok := NewTokenizer("$(echo $(date))")
	tokens := tok.Tokenize()
	// 应能识别嵌套结构
	if len(tokens) < 6 {
		t.Fatalf("expected >=6 tokens for nested substitution, got %d: %+v", len(tokens), tokens)
	}
}
