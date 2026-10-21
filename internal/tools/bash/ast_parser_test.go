// Package bash — AST 解析器测试（T1 降级路径）。
package bash

import (
	"context"
	"testing"
)

// TestParseCommand_Simple 验证简单命令解析。
func TestParseCommand_Simple(t *testing.T) {
	ast, err := ParseCommand(context.Background(), `cat /etc/hosts`)
	if err != nil {
		t.Fatalf("ParseCommand error: %v", err)
	}
	if len(ast.Commands) != 1 {
		t.Fatalf("expected 1 command, got %d", len(ast.Commands))
	}
	cmd := ast.Commands[0]
	if cmd.Name != "cat" {
		t.Errorf("expected name cat, got %q", cmd.Name)
	}
	if len(cmd.Args) != 1 || cmd.Args[0] != "/etc/hosts" {
		t.Errorf("expected args [/etc/hosts], got %v", cmd.Args)
	}
}

// TestParseCommand_Compound 验证复合命令拆分（&&, ;, |, ||）。
func TestParseCommand_Compound(t *testing.T) {
	cases := []struct {
		input string
		count int
		names []string
	}{
		{`cd /etc && cat passwd`, 2, []string{"cd", "cat"}},
		{`ls -la; pwd`, 2, []string{"ls", "pwd"}},
		{`echo hi | grep h`, 2, []string{"echo", "grep"}},
		{`false || true`, 2, []string{"false", "true"}},
		{`a && b && c && d`, 4, []string{"a", "b", "c", "d"}},
	}
	for _, c := range cases {
		ast, err := ParseCommand(context.Background(), c.input)
		if err != nil {
			t.Fatalf("ParseCommand(%q) error: %v", c.input, err)
		}
		if len(ast.Commands) != c.count {
			t.Errorf("%q: expected %d commands, got %d (%+v)", c.input, c.count, len(ast.Commands), ast.Commands)
			continue
		}
		for i, want := range c.names {
			if ast.Commands[i].Name != want {
				t.Errorf("%q: cmd[%d] name=%q want %q", c.input, i, ast.Commands[i].Name, want)
			}
		}
	}
}

// TestParseCommand_Quoting 验证引号处理。
func TestParseCommand_Quoting(t *testing.T) {
	ast, err := ParseCommand(context.Background(), `echo "hello world" 'a b' c\ d`)
	if err != nil {
		t.Fatalf("ParseCommand error: %v", err)
	}
	cmd := ast.Commands[0]
	want := []string{"hello world", "a b", "c d"}
	if len(cmd.Args) != len(want) {
		t.Fatalf("expected %d args, got %d: %v", len(want), len(cmd.Args), cmd.Args)
	}
	for i := range want {
		if cmd.Args[i] != want[i] {
			t.Errorf("arg[%d]=%q want %q", i, cmd.Args[i], want[i])
		}
	}
}

// TestParseCommand_Redirect 验证重定向解析。
func TestParseCommand_Redirect(t *testing.T) {
	ast, err := ParseCommand(context.Background(), `echo hi > /tmp/out 2>&1`)
	if err != nil {
		t.Fatalf("ParseCommand error: %v", err)
	}
	cmd := ast.Commands[0]
	if len(cmd.Redirects) < 2 {
		t.Fatalf("expected at least 2 redirects, got %d", len(cmd.Redirects))
	}
	var hasOut, hasErrRedirect bool
	for _, r := range cmd.Redirects {
		if r.Op == ">" && r.Target == "/tmp/out" {
			hasOut = true
		}
		if r.Op == "2>&" {
			hasErrRedirect = true
		}
	}
	if !hasOut {
		t.Errorf("missing > /tmp/out redirect: %+v", cmd.Redirects)
	}
	if !hasErrRedirect {
		t.Errorf("missing 2>&1 redirect: %+v", cmd.Redirects)
	}
}

// TestParseCommand_AllCommandsSafe 验证安全命令白名单。
func TestParseCommand_AllCommandsSafe(t *testing.T) {
	ast, _ := ParseCommand(context.Background(), `cat foo && ls -la && pwd`)
	if !ast.AllCommandsSafe() {
		t.Error("expected safe commands to be safe")
	}
	ast2, _ := ParseCommand(context.Background(), `cat foo && rm -rf /`)
	if ast2.AllCommandsSafe() {
		t.Error("rm should not be safe")
	}
}

// TestParseCommand_HasCDCompound 验证 cd 复合检测。
func TestParseCommand_HasCDCompound(t *testing.T) {
	ast, _ := ParseCommand(context.Background(), `cd /etc && cat passwd`)
	if !ast.HasCDCompound() {
		t.Error("expected HasCDCompound true for 'cd /etc && cat passwd'")
	}
	ast2, _ := ParseCommand(context.Background(), `cd /tmp`)
	if ast2.HasCDCompound() {
		t.Error("expected HasCDCompound false for single cd")
	}
}
