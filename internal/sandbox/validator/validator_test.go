package validator

import (
	"testing"
)

// TestSecurityChecks_ExpansionFamily 验证**非执行型展开**族的保守处理（语义未变）。
//
// `${…}` / `$[…]` / `$((…))` 是**展开**（expand）不是执行（execute）——
// 刻意不动它们，理由见 checkExpansionConservative 的三条。
func TestSecurityChecks_ExpansionFamily(t *testing.T) {
	blocked := []string{
		"echo ${HOME}",
		"echo $[1+1]",
		"echo $((1+1))",
	}
	for _, cmd := range blocked {
		if r := checkExpansionConservative(cmd); r == nil || r.Passed {
			t.Errorf("expected conservative block for %q", cmd)
		}
	}
	safe := []string{
		"echo hello",
		"ls -la",
		`echo "a(b)c"`,
	}
	for _, cmd := range safe {
		if r := checkExpansionConservative(cmd); r != nil && !r.Passed {
			t.Errorf("expected pass for %q, got: %s", cmd, r.Message)
		}
	}
}

// TestSecurityChecks_CommandSubstitutionInner 验证**执行型替换**族的递归校验口径。
//
// 改前本用例（`TestSecurityChecks_CommandSubstitution`）断言 8 种形态"出现即拦"。
// 改变了判据：判的**不是"有没有替换"**，而是"**替换体里那条命令**危险不危险"。
// ⇒ 拆成两族分别断言：展开族（上面那条，语义未变）+ 替换族（本用例，判据已变）。
//
// 覆盖面不缩：原 8 条里 `$()` / 反引号 / `<()` / `>()` / `=()` 五条落在本族，
// `${}` / `$[]` / `$(())` 三条落在展开族。
func TestSecurityChecks_CommandSubstitutionInner(t *testing.T) {
	benign := []string{
		"echo $(whoami)",
		"echo `whoami`",
		"cat <(echo hi)",
		"echo > >(tee log)",
		"echo =(cmd)",
	}
	for _, cmd := range benign {
		if r := checkCommandSubstitution(cmd); r != nil && !r.Passed {
			t.Errorf("内层良性应放行：%q ⇒ %s", cmd, r.Message)
		}
	}

	dangerous := []string{
		"echo $(rm -rf /)",
		"echo `rm -rf /`",
		"cat <(rm -rf /)",
		"echo =(rm -rf /)",
	}
	for _, cmd := range dangerous {
		if r := checkCommandSubstitution(cmd); r == nil || r.Passed {
			t.Errorf("内层危险必须拒：%q", cmd)
		}
	}
}

// TestSecurityChecks_ControlCharacters 验证控制字符检测。
func TestSecurityChecks_ControlCharacters(t *testing.T) {
	blocked := []string{
		"echo\x00malicious",
		"echo\x01bad",
		"echo\x1ftest",
		"echo\x7fdel",
	}
	safe := []string{
		"echo hello",
		"ls -la\t/tmp",
		"echo line1\nline2",
	}

	for _, cmd := range blocked {
		result := checkControlCharacters(cmd)
		if result == nil || result.Passed {
			t.Errorf("expected control char block for %q", cmd)
		}
	}
	for _, cmd := range safe {
		result := checkControlCharacters(cmd)
		if result != nil && !result.Passed {
			t.Errorf("expected pass for %q, got: %s", cmd, result.Message)
		}
	}
}

// TestSecurityChecks_IFSInjection 验证 IFS 注入检测。
func TestSecurityChecks_IFSInjection(t *testing.T) {
	blocked := []string{
		"IFS=' '",
		"IFS='';cat /etc/passwd",
		"export IFS=:",
	}
	safe := []string{
		"echo hello",
		"cat file.txt",
	}

	for _, cmd := range blocked {
		result := checkIFSInjection(cmd)
		if result == nil || result.Passed {
			t.Errorf("expected IFS injection block for %q", cmd)
		}
	}
	for _, cmd := range safe {
		result := checkIFSInjection(cmd)
		if result != nil && !result.Passed {
			t.Errorf("expected pass for %q, got: %s", cmd, result.Message)
		}
	}
}

// TestSecurityChecks_DangerousRedirections 验证危险重定向检测。
func TestSecurityChecks_DangerousRedirections(t *testing.T) {
	blocked := []string{
		"echo bad > /etc/passwd",
		"cat x >> /usr/bin/foo",
		"echo y 2> /boot/grub.cfg",
	}
	safe := []string{
		"echo hello > /tmp/file",
		"cat x >> ./log.txt",
	}

	for _, cmd := range blocked {
		result := checkDangerousRedirections(cmd)
		if result == nil || result.Passed {
			t.Errorf("expected dangerous redirect block for %q", cmd)
		}
	}
	for _, cmd := range safe {
		result := checkDangerousRedirections(cmd)
		if result != nil && !result.Passed {
			t.Errorf("expected pass for %q, got: %s", cmd, result.Message)
		}
	}
}

// TestSecurityChecks_BraceExpansion 验证花括号展开检测。
func TestSecurityChecks_BraceExpansion(t *testing.T) {
	blocked := []string{
		"echo {a,b,c}",
		"rm /tmp/{file1,file2}",
	}
	safe := []string{
		"echo hello",
		"echo {single}",
	}

	for _, cmd := range blocked {
		result := checkBraceExpansion(cmd)
		if result == nil || result.Passed {
			t.Errorf("expected brace expansion block for %q", cmd)
		}
	}
	for _, cmd := range safe {
		result := checkBraceExpansion(cmd)
		if result != nil && !result.Passed {
			t.Errorf("expected pass for %q, got: %s", cmd, result.Message)
		}
	}
}

// TestValidator_HasBlocking 验证 HasBlocking 辅助函数。
func TestValidator_HasBlocking(t *testing.T) {
	results := []SecurityResult{
		{Passed: true, Severity: SeverityLow},
		{Passed: false, Severity: SeverityMedium, CheckID: "TEST"},
	}
	if HasBlocking(results) {
		t.Error("should not have blocking with only Low/Medium")
	}

	results = append(results, SecurityResult{Passed: false, Severity: SeverityHigh, CheckID: "HIGH"})
	if !HasBlocking(results) {
		t.Error("should have blocking with High")
	}

	results = append(results, SecurityResult{Passed: false, Severity: SeverityCritical, CheckID: "CRIT"})
	if !HasBlocking(results) {
		t.Error("should have blocking with Critical")
	}
}

// TestValidator_Run 验证 Validator.Run 对多条违规的聚合。
//
// ★ ：内层危险才算"命令替换违规" ⇒ 样例的内层必须是**危险命令**。
// 改前用 `$(cat /etc/passwd)`（内层良性）也能凑出第 2 条，改后不会 —— 这是有意变更。
func TestValidator_Run(t *testing.T) {
	v := NewValidator()
	// 同时触发命令替换（内层危险）+ IFS 注入
	results := v.Run("IFS=''; echo $(rm -rf /)")
	if len(results) < 2 {
		t.Errorf("expected at least 2 violations, got %d: %v", len(results), results)
	}
}

// TestPathExtractor_CompositeCommand 验证复合命令路径提取（B6 核心修复）。
// 原 tokenizeCommand 遇 |;& 截断，导致 `echo ok; rm -rf /` 只检查 echo。
func TestPathExtractor_CompositeCommand(t *testing.T) {
	pe := NewPathExtractor()

	tests := []struct {
		cmd     string
		hasPath string // 期望提取结果中包含此路径
		desc    string
	}{
		{
			cmd:     "echo ok; rm /etc/passwd",
			hasPath: "/etc/passwd",
			desc:    "semicolon-separated rm path",
		},
		{
			cmd:     "echo ok | cat /etc/shadow",
			hasPath: "/etc/shadow",
			desc:    "pipe-separated cat path",
		},
		{
			cmd:     "echo ok && rm /tmp/danger",
			hasPath: "/tmp/danger",
			desc:    "&& separated rm path",
		},
		{
			cmd:     "cat /etc/hosts; echo done",
			hasPath: "/etc/hosts",
			desc:    "first segment cat path",
		},
	}

	for _, tc := range tests {
		t.Run(tc.desc, func(t *testing.T) {
			paths := pe.Extract(tc.cmd)
			found := false
			for _, p := range paths {
				if p == tc.hasPath {
					found = true
					break
				}
			}
			if !found {
				t.Errorf("expected path %q in extraction of %q, got: %v", tc.hasPath, tc.cmd, paths)
			}
		})
	}
}

// TestPathExtractor_SingleCommand 验证单命令路径提取。
func TestPathExtractor_SingleCommand(t *testing.T) {
	pe := NewPathExtractor()

	tests := []struct {
		cmd       string
		expectLen int
		desc      string
	}{
		{"cat /etc/passwd", 1, "cat single file"},
		{"rm /tmp/a /tmp/b", 2, "rm multiple files"},
		{"cp src dst", 1, "cp target path"},
		{"mv old new", 1, "mv target path"},
		{"ln -s target link", 1, "ln target (last)"},
		{"chmod 755 /tmp/script", 1, "chmod target"},
		{"dd if=/dev/zero of=/tmp/img", 1, "dd output"},
		{"tar -f /tmp/archive.tar -c .", 1, "tar output file"},
		{"grep pattern /var/log/syslog", 1, "grep file"},
		{"find /usr -name '*.go'", 1, "find root"},
	}

	for _, tc := range tests {
		t.Run(tc.desc, func(t *testing.T) {
			paths := pe.Extract(tc.cmd)
			if len(paths) != tc.expectLen {
				t.Errorf("expected %d paths for %q, got %d: %v", tc.expectLen, tc.cmd, len(paths), paths)
			}
		})
	}
}

// TestSplitSegments 验证复合命令分段（引号感知）。
func TestSplitSegments(t *testing.T) {
	tests := []struct {
		cmd      string
		expected int
		desc     string
	}{
		{"echo hello", 1, "single command"},
		{"echo a; echo b", 2, "semicolon split"},
		{"echo a | grep b", 2, "pipe split"},
		{"echo a && echo b", 3, "&& split (each & is a separator, middle segment is empty)"},
		{"echo 'a;b' ; echo c", 2, "quote-aware semicolon"},
		{"echo 'a|b' | grep c", 2, "quote-aware pipe"},
	}

	for _, tc := range tests {
		t.Run(tc.desc, func(t *testing.T) {
			segments := splitSegments(tc.cmd)
			if len(segments) != tc.expected {
				t.Errorf("expected %d segments for %q, got %d: %v", tc.expected, tc.cmd, len(segments), segments)
			}
		})
	}
}

// TestTokenizeCommand 验证单段分词（引号处理）。
func TestTokenizeCommand(t *testing.T) {
	tests := []struct {
		cmd      string
		expected []string
		desc     string
	}{
		{"echo hello", []string{"echo", "hello"}, "simple"},
		{"echo 'hello world'", []string{"echo", "hello world"}, "single quoted"},
		{`echo "hello world"`, []string{"echo", "hello world"}, "double quoted"},
		{"cat /etc/passwd", []string{"cat", "/etc/passwd"}, "absolute path"},
	}

	for _, tc := range tests {
		t.Run(tc.desc, func(t *testing.T) {
			tokens := tokenizeCommand(tc.cmd)
			if len(tokens) != len(tc.expected) {
				t.Errorf("expected %d tokens, got %d: %v", len(tc.expected), len(tokens), tokens)
				return
			}
			for i, tok := range tokens {
				if tok != tc.expected[i] {
					t.Errorf("token %d: expected %q, got %q", i, tc.expected[i], tok)
				}
			}
		})
	}
}

// === M14-01: fork 炸弹显式检测测试 ===

// TestSecurityChecks_ForkBomb_Classic 验证经典 : 形式 fork 炸弹的检测（happy path + 空格变体）。
func TestSecurityChecks_ForkBomb_Classic(t *testing.T) {
	blocked := []struct {
		cmd  string
		desc string
	}{
		{":(){ :|:& };:", "canonical fork bomb"},
		{":(){ :|:&};:", "no space before }"},
		{":(){ :|:& } ; :", "space before } and ;"},
		{": () { : | : & } ; :", "spaces everywhere"},
		{": (  ) {  :  |  :  &  }  ;  :", "multiple spaces"},
		{":(){:|:&};:", "no inner spaces at all"},
		{":(){ : | : & };:", "spaces inside body"},
		// 仅函数定义（无尾部调用）也应被拦截，因为定义本身已构成危险
		{":(){ :|:& }", "definition only without invocation"},
		// 命令前缀场景（嵌入更大命令中）
		{"bash -c ':(){ :|:& };:'", "nested in bash -c"},
	}

	for _, tc := range blocked {
		t.Run(tc.desc, func(t *testing.T) {
			result := checkForkBomb(tc.cmd)
			if result == nil || result.Passed {
				t.Errorf("expected fork bomb block for %q (%s)", tc.cmd, tc.desc)
				return
			}
			if result.CheckID != "FORK-BOMB" {
				t.Errorf("CheckID 期望 FORK-BOMB, 实际 %s (cmd=%q)", result.CheckID, tc.cmd)
			}
			if result.Severity != SeverityCritical {
				t.Errorf("Severity 期望 Critical, 实际 %d (cmd=%q)", result.Severity, tc.cmd)
			}
		})
	}
}

// TestSecurityChecks_ForkBomb_Named 验证命名函数形式 fork 炸弹的检测。
func TestSecurityChecks_ForkBomb_Named(t *testing.T) {
	blocked := []struct {
		cmd  string
		desc string
	}{
		{"f(){ f|f& };f", "single-letter name f"},
		{"bomb(){ bomb|bomb& };bomb", "multi-letter name bomb"},
		{"fork(){ fork|fork& };fork", "name fork"},
		{"f() { f | f & } ; f", "named with spaces"},
		{"function f { f|f& }; f", "bash function keyword form"},
		{"function bomb { bomb|bomb& }; bomb", "bash function keyword multi-letter"},
		// 函数体内含其他语句但仍是 fork 炸弹
		{"f(){ echo hi; f|f& };f", "named with prefix statement"},
	}

	for _, tc := range blocked {
		t.Run(tc.desc, func(t *testing.T) {
			result := checkForkBomb(tc.cmd)
			if result == nil || result.Passed {
				t.Errorf("expected fork bomb block for %q (%s)", tc.cmd, tc.desc)
				return
			}
			if result.CheckID != "FORK-BOMB" {
				t.Errorf("CheckID 期望 FORK-BOMB, 实际 %s (cmd=%q)", result.CheckID, tc.cmd)
			}
		})
	}
}

// TestSecurityChecks_ForkBomb_FalsePositives 验证合法命令不被误报为 fork 炸弹。
func TestSecurityChecks_ForkBomb_FalsePositives(t *testing.T) {
	safe := []struct {
		cmd  string
		desc string
	}{
		{"ls -la", "normal ls"},
		{"echo hello", "normal echo"},
		{"git status", "normal git"},
		{"cat /etc/passwd", "normal cat"},
		// 函数定义但无自递归管道
		{"f() { echo hello; }", "function without recursion"},
		// 函数自调用但无管道+后台（不构成 fork 炸弹）
		{"f() { f; }", "function calls self without pipe"},
		// 函数含管道但非自递归
		{"f() { echo hi | grep h; }", "function with pipe but not self-recursive"},
		// 名函数但无自递归
		{":(){ echo hello; }", "colon-name function without recursion"},
		// 普通管道命令
		{"ls | grep foo", "normal pipe command"},
		// 含 : 但非 fork 炸弹（如 shell 条件判断）
		{"if [ -f x ]; then echo y; fi", "shell if statement"},
		// 函数定义后调用其他命令
		{"greet() { echo hi; }; greet", "function definition and normal call"},
	}

	for _, tc := range safe {
		t.Run(tc.desc, func(t *testing.T) {
			result := checkForkBomb(tc.cmd)
			if result != nil && !result.Passed {
				t.Errorf("expected pass for %q (%s), got: [%s] %s", tc.cmd, tc.desc, result.CheckID, result.Message)
			}
		})
	}
}

// TestSecurityChecks_ForkBomb_Integration 验证 fork 炸弹在 Validator.Run 完整检查套件中被拦截。
func TestSecurityChecks_ForkBomb_Integration(t *testing.T) {
	v := NewValidator()
	results := v.Run(":(){ :|:& };:")
	if !HasBlocking(results) {
		t.Fatalf("expected blocking result for fork bomb, got: %v", results)
	}
	found := false
	for _, r := range results {
		if r.CheckID == "FORK-BOMB" {
			found = true
			if r.Severity != SeverityCritical {
				t.Errorf("FORK-BOMB Severity 期望 Critical, 实际 %d", r.Severity)
			}
		}
	}
	if !found {
		t.Errorf("expected FORK-BOMB check ID in results, got: %v", results)
	}
}
