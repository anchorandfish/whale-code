// Package bash — 安全检查与路径提取测试（T2/T3，含 15 个攻击向量）。
package bash

import (
	"context"
	"sort"
	"testing"
)

// hasViolation 判断违规列表中是否含指定 CheckID。
func hasViolation(vs []*SecurityViolation, id string) bool {
	for _, v := range vs {
		if v.CheckID == id {
			return true
		}
	}
	return false
}

// hasCritical 判断是否存在 Critical 级违规。
func hasCritical(vs []*SecurityViolation) bool {
	return HasCriticalViolation(vs)
}

// TestSecurityChecks_Count 验证 27 项检查已注册。
func TestSecurityChecks_Count(t *testing.T) {
	if len(SecurityChecks) != 27 {
		t.Fatalf("expected 27 security checks, got %d", len(SecurityChecks))
	}
}

// TestSecurityChecks_AttackVectors 15 个 Bash 攻击向量测试。
func TestSecurityChecks_AttackVectors(t *testing.T) {
	cases := []struct {
		name    string
		cmd     string
		checkID string // 期望命中的检查 ID
		crit    bool   // 是否期望 Critical
	}{
		// 1. 命令替换 $()
		{"cmd_substitution", `echo $(whoami)`, "CMD_SUBST", true},
		// 2. 反引号替换
		{"backtick_subst", `echo ` + "`whoami`", "BACKTICK_SUBST", true},
		// 3. 进程替换 <()
		{"process_subst", `cat <(ls -la)`, "PROC_SUBST", true},
		// 4. 控制字符
		{"control_char", "echo \x01hi", "CTRL_CHAR", true},
		// 5. 回车注入
		{"cr_injection", "echo hi\rmalicious", "CR_INJECTION", true},
		// 6. 参数展开 ${}
		{"param_expansion", `echo ${HOME}`, "PARAM_EXPANSION", false},
		// 7. IFS 篡改
		{"ifs_injection", `IFS=' '; echo $1`, "IFS_INJECTION", false},
		// 8. heredoc 注入
		{"heredoc", `cat <<EOF\n$(rm -rf /)\nEOF`, "HEREDOC", false},
		// 9. 大括号展开
		{"brace_expansion", `echo {a,b,c}`, "BRACE_EXPANSION", false},
		// 10. 危险重定向到 /etc
		{"dangerous_redirect", `echo bad > /etc/passwd`, "DANGEROUS_REDIRECT", false},
		// 11. ANSI-C 引号
		{"ansic_quote", `echo $'\x41'`, "ANSIC_QUOTE", false},
		// 12. jq system() 函数
		{"jq_system", `echo '{}' | jq 'system("ls")'`, "JQ_SYSTEM", false},
		// 13. /proc/*/environ 读取
		{"proc_environ", `cat /proc/1/environ`, "PROC_ENVIRON", false},
		// 14. 路径遍历
		{"path_traversal", `cat ../../../etc/passwd`, "PATH_TRAVERSAL", false},
		// 15. Unicode 空格绕过
		{"unicode_space", "echo\u3000hi", "UNICODE_SPACE", false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			vs := RunSecurityChecks(c.cmd)
			if !hasViolation(vs, c.checkID) {
				t.Errorf("attack %q did not trigger check %s; got: %+v", c.cmd, c.checkID, vs)
			}
			if c.crit && !hasCritical(vs) {
				t.Errorf("attack %q expected Critical violation, got none critical", c.cmd)
			}
		})
	}
}

// TestSecurityChecks_ShortCircuit 验证短路逻辑。
func TestSecurityChecks_ShortCircuit(t *testing.T) {
	// 同时含 Critical 与 Medium：短路应在 Critical 处停止
	cmd := "echo \x01$(whoami) .."
	vs := RunSecurityChecksShortCircuit(cmd)
	if len(vs) == 0 {
		t.Fatal("expected violations")
	}
	// 短路版本：第一个必须是 Critical
	if vs[0].Severity != SeverityCritical {
		t.Errorf("expected first violation Critical, got %d", vs[0].Severity)
	}
}

// TestSecurityChecks_SafeCommand 安全命令不命中 Critical。
func TestSecurityChecks_SafeCommand(t *testing.T) {
	vs := RunSecurityChecks("cat /etc/hosts && ls -la /tmp")
	if hasCritical(vs) {
		t.Errorf("safe command triggered Critical: %+v", vs)
	}
}

// TestPathExtractors 各命令路径提取。
func TestPathExtractors(t *testing.T) {
	cases := []struct {
		cmd  string
		want []string
	}{
		{`cat /etc/hosts`, []string{"/etc/hosts"}},
		{`rm -rf /tmp/a /tmp/b`, []string{"/tmp/a", "/tmp/b"}},
		{`cp src.txt dst.txt`, []string{"dst.txt"}}, // 目标路径
		{`mv a b`, []string{"b"}},
		{`grep pattern file.txt`, []string{"file.txt"}},
		{`dd if=/dev/zero of=/tmp/img`, []string{"/tmp/img"}},
		{`curl -o /tmp/out http://x`, []string{"/tmp/out"}},
		{`wget -O /tmp/w http://x`, []string{"/tmp/w"}},
		{`tar -cf /tmp/a.tar .`, []string{"/tmp/a.tar"}},
		{`echo hi > /tmp/out`, []string{"/tmp/out"}}, // 重定向目标
	}
	for _, c := range cases {
		t.Run(c.cmd, func(t *testing.T) {
			ast, err := ParseCommand(context.Background(), c.cmd)
			if err != nil {
				t.Fatalf("ParseCommand error: %v", err)
			}
			got := ExtractAllPathsFromAST(ast)
			sort.Strings(got)
			sort.Strings(c.want)
			if !equalStrings(got, c.want) {
				t.Errorf("paths=%v want %v", got, c.want)
			}
		})
	}
}

// TestCheckDangerousRemoval 危险删除路径检测。
func TestCheckDangerousRemoval(t *testing.T) {
	hit := CheckDangerousRemoval([]string{"/", "/home/user", "/usr"}, "/home/user/proj")
	if len(hit) != 2 {
		t.Errorf("expected 2 dangerous hits, got %d: %v", len(hit), hit)
	}
	safe := CheckDangerousRemoval([]string{"/home/user/proj/sub"}, "/home/user/proj")
	if len(safe) != 0 {
		t.Errorf("expected no dangerous hit, got %v", safe)
	}
}

// TestCommandSemantics 命令语义分析。
func TestCommandSemantics(t *testing.T) {
	// 只读命令
	ast, _ := ParseCommand(context.Background(), `cat /tmp/a && ls -la`)
	s := AnalyzeSemantics(ast, "/home/user/proj")
	if !s.IsReadOnly {
		t.Errorf("expected IsReadOnly true for read-only cmd, got %+v", s)
	}
	// 破坏性命令
	ast2, _ := ParseCommand(context.Background(), `rm -rf /tmp/b`)
	s2 := AnalyzeSemantics(ast2, "/home/user/proj")
	if !s2.IsDestructive {
		t.Errorf("expected IsDestructive true for rm, got %+v", s2)
	}
	// 网络命令
	ast3, _ := ParseCommand(context.Background(), `curl http://x`)
	s3 := AnalyzeSemantics(ast3, "/home/user/proj")
	if !s3.AccessesNetwork {
		t.Errorf("expected AccessesNetwork true for curl, got %+v", s3)
	}
	// 复合命令含写操作 → 不应 IsReadOnly
	ast4, _ := ParseCommand(context.Background(), `cat /tmp/a && rm -rf /tmp/b`)
	s4 := AnalyzeSemantics(ast4, "/home/user/proj")
	if s4.IsReadOnly {
		t.Errorf("compound with rm should not be IsReadOnly, got %+v", s4)
	}
	if !s4.IsDestructive {
		t.Errorf("expected IsDestructive true for compound with rm, got %+v", s4)
	}
}

func equalStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
