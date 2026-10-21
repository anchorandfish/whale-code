// Package validator ast_builder_test.go 语法分析器单元测试（T1-P1-A4 /）。
package validator

import (
	"testing"
)

// TestASTBuilder_SimpleCommand 验证简单命令解析。
func TestASTBuilder_SimpleCommand(t *testing.T) {
	tokens := NewTokenizer("rm -rf /").Tokenize()
	root := NewASTBuilder(tokens).Build()
	if root == nil {
		t.Fatal("expected non-nil root")
	}
	if root.Type != NodeSimpleCommand {
		t.Errorf("expected NodeSimpleCommand, got %d", root.Type)
	}
	if len(root.Words) != 3 {
		t.Fatalf("expected 3 words, got %d: %+v", len(root.Words), root.Words)
	}
	if root.Words[0] != "rm" || root.Words[1] != "-rf" || root.Words[2] != "/" {
		t.Errorf("unexpected words: %+v", root.Words)
	}
}

// TestASTBuilder_Pipeline 验证管道解析。
func TestASTBuilder_Pipeline(t *testing.T) {
	tokens := NewTokenizer("a | b").Tokenize()
	root := NewASTBuilder(tokens).Build()
	if root == nil {
		t.Fatal("expected non-nil root")
	}
	if root.Type != NodePipeline {
		t.Errorf("expected NodePipeline, got %d", root.Type)
	}
	if len(root.Children) != 2 {
		t.Fatalf("expected 2 children, got %d", len(root.Children))
	}
}

// TestASTBuilder_Background 验证后台执行标记。
func TestASTBuilder_Background(t *testing.T) {
	tokens := NewTokenizer("a &").Tokenize()
	root := NewASTBuilder(tokens).Build()
	if root == nil {
		t.Fatal("expected non-nil root")
	}
	if root.Type != NodeSimpleCommand {
		t.Errorf("expected NodeSimpleCommand, got %d", root.Type)
	}
	if !root.Background {
		t.Error("expected Background=true")
	}
}

// TestASTBuilder_AndOr 验证 && / || 逻辑与/或。
func TestASTBuilder_AndOr(t *testing.T) {
	tokens := NewTokenizer("a && b").Tokenize()
	root := NewASTBuilder(tokens).Build()
	if root == nil {
		t.Fatal("expected non-nil root")
	}
	if root.Type != NodeAndOr {
		t.Errorf("expected NodeAndOr, got %d", root.Type)
	}
	if len(root.Children) != 2 {
		t.Fatalf("expected 2 children, got %d", len(root.Children))
	}
}

// TestASTBuilder_Subshell 验证子 shell。
func TestASTBuilder_Subshell(t *testing.T) {
	tokens := NewTokenizer("( rm -rf / )").Tokenize()
	root := NewASTBuilder(tokens).Build()
	if root == nil {
		t.Fatal("expected non-nil root")
	}
	if root.Type != NodeSubshell {
		t.Errorf("expected NodeSubshell, got %d", root.Type)
	}
	if root.Body == nil {
		t.Fatal("expected non-nil body")
	}
	if root.Body.Type != NodeSimpleCommand {
		t.Errorf("expected body NodeSimpleCommand, got %d", root.Body.Type)
	}
}

// TestASTBuilder_FunctionDef 验证函数定义。
func TestASTBuilder_FunctionDef(t *testing.T) {
	tokens := NewTokenizer("f() { f|f& }; f").Tokenize()
	root := NewASTBuilder(tokens).Build()
	if root == nil {
		t.Fatal("expected non-nil root")
	}
	// 应该是 AndOr 节点（函数定义 + 调用）
	if root.Type != NodeAndOr {
		t.Errorf("expected NodeAndOr at root, got %d", root.Type)
	}
	if len(root.Children) < 1 {
		t.Fatal("expected at least 1 child")
	}
	funcNode := root.Children[0]
	if funcNode.Type != NodeFunctionDef {
		t.Errorf("expected NodeFunctionDef, got %d", funcNode.Type)
	}
	if funcNode.FuncName != "f" {
		t.Errorf("expected FuncName=f, got %q", funcNode.FuncName)
	}
	if funcNode.Body == nil {
		t.Fatal("expected non-nil function body")
	}
	if funcNode.Body.Type != NodePipeline {
		t.Errorf("expected body NodePipeline, got %d", funcNode.Body.Type)
	}
}

// TestASTBuilder_VarAssign 验证变量赋值。
func TestASTBuilder_VarAssign(t *testing.T) {
	tokens := NewTokenizer("x=rm $x -rf /").Tokenize()
	root := NewASTBuilder(tokens).Build()
	if root == nil {
		t.Fatal("expected non-nil root")
	}
	if root.Type != NodeSimpleCommand {
		t.Errorf("expected NodeSimpleCommand, got %d", root.Type)
	}
	if len(root.VarAssigns) != 1 {
		t.Fatalf("expected 1 VarAssign, got %d", len(root.VarAssigns))
	}
	if root.VarAssigns[0].Name != "x" || root.VarAssigns[0].Value != "rm" {
		t.Errorf("unexpected VarAssign: %+v", root.VarAssigns[0])
	}
	// Words 应包含 $x -rf /
	if len(root.Words) != 3 {
		t.Fatalf("expected 3 words, got %d: %+v", len(root.Words), root.Words)
	}
	if root.Words[0] != "$x" {
		t.Errorf("expected first word $x, got %q", root.Words[0])
	}
}

// TestASTBuilder_Redirect 验证重定向。
func TestASTBuilder_Redirect(t *testing.T) {
	tokens := NewTokenizer("echo x > /tmp/y").Tokenize()
	root := NewASTBuilder(tokens).Build()
	if root == nil {
		t.Fatal("expected non-nil root")
	}
	if len(root.Redirects) != 1 {
		t.Fatalf("expected 1 redirect, got %d", len(root.Redirects))
	}
	r := root.Redirects[0]
	if r.Op != ">" || r.Target != "/tmp/y" {
		t.Errorf("unexpected redirect: %+v", r)
	}
}

// TestASTBuilder_Empty 验证空输入。
func TestASTBuilder_Empty(t *testing.T) {
	tokens := NewTokenizer("").Tokenize()
	root := NewASTBuilder(tokens).Build()
	if root == nil {
		t.Fatal("expected non-nil root for empty input")
	}
	if root.Type != NodeSimpleCommand {
		t.Errorf("expected NodeSimpleCommand for empty, got %d", root.Type)
	}
}

// TestASTBuilder_ForkBombClassic 验证经典 fork 炸弹 :(){ :|:& };: 解析。
func TestASTBuilder_ForkBombClassic(t *testing.T) {
	tokens := NewTokenizer(":(){ :|:& };:").Tokenize()
	root := NewASTBuilder(tokens).Build()
	if root == nil {
		t.Fatal("expected non-nil root")
	}
	// 应该能识别函数定义（无论 AndOr 包装与否）
	foundFuncDef := false
	WalkAST(root, func(node *CommandNode) bool {
		if node.Type == NodeFunctionDef && node.FuncName == ":" {
			foundFuncDef = true
			return false
		}
		return true
	})
	if !foundFuncDef {
		t.Error("expected to find FunctionDef with FuncName=:")
	}
}

// TestWalkAST 验证 WalkAST 遍历。
func TestWalkAST(t *testing.T) {
	tokens := NewTokenizer("a && b | c").Tokenize()
	root := NewASTBuilder(tokens).Build()
	visited := 0
	WalkAST(root, func(node *CommandNode) bool {
		visited++
		return true
	})
	if visited == 0 {
		t.Error("expected at least 1 node visited")
	}
}

// TestParseVarAssign 验证变量赋值解析。
func TestParseVarAssign(t *testing.T) {
	tests := []struct {
		input    string
		isAssign bool
		name     string
		value    string
	}{
		{"x=rm", true, "x", "rm"},
		{"VAR=value", true, "VAR", "value"},
		{"_var=123", true, "_var", "123"},
		{"x=", true, "x", ""},
		{"rm", false, "", ""},
		{"=rm", false, "", ""},
		{"1x=rm", false, "", ""},
	}
	for _, tc := range tests {
		assign, ok := parseVarAssign(tc.input)
		if ok != tc.isAssign {
			t.Errorf("input %q: expected isAssign=%v, got %v", tc.input, tc.isAssign, ok)
			continue
		}
		if ok && (assign.Name != tc.name || assign.Value != tc.value) {
			t.Errorf("input %q: expected name=%q value=%q, got name=%q value=%q",
				tc.input, tc.name, tc.value, assign.Name, assign.Value)
		}
	}
}
