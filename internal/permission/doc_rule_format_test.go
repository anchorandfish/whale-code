package permission

import "testing"

// 本文件核对手测文档《子agent测试.md》给出的规则写法是否真能命中。
//
// 背景：文档 T-B 让用户写 `"deny": ["read_file(*:*)"]`，而用户实际配置文件里写的是
// `"deny": ["read_file(*)"]`。两者语义是否一致，直接决定 T-B 结论是否可信。
//
// 解析链：ParseRuleString → RuleContent；PermissionRule.Match → matchesContent →
// matchPattern(subCmd, content)，其中 matchPattern 先 Trim("*") 再按前后缀通配判定。
func TestDocRuleFormat_ReadFileVariants(t *testing.T) {
	cases := []struct {
		rule  string
		input string
		want  bool
		note  string
	}{
		{"read_file(*)", `{"path":"/Users/me/proj/README.md","limit":1}`, true, "用户配置的写法 —— 生效"},
		{"read_file(*:*)", `{"path":"/Users/me/proj/README.md","limit":1}`, false, "⚠️ 文档 T-B 推荐的写法 —— macOS/Linux 绝对路径无冒号，规则**不命中**"},
		{"read_file(*:*)", `{"path":"C:\\proj\\README.md","limit":1}`, true, "Windows 盘符路径含冒号才命中（所以文档写法在 Win 上像是对的）"},
		{"read_file(*)", `{"path":"C:\\proj\\README.md","limit":1}`, true, "用户写法跨平台一致"},
		{"read_file(*README*)", `{"path":"/Users/me/proj/README.md","limit":1}`, true, "包含匹配"},
		{"bash(rm:*)", `{"command":"rm -rf /tmp/abc"}`, true, "T-F 的 rm 规则 —— 按预期命中"},
		{"bash(rm:*)", `{"command":"rmdir /tmp/abc"}`, true, "⚠️ 前缀匹配过宽：rmdir 也算命中（方向是 fail-safe 过拦，但不够精确）"},
		{"bash(rm:*)", `{"command":"chmod +x rm"}`, false, "非前缀位置不命中"},
	}

	for _, c := range cases {
		rule, err := ParseRuleString(c.rule, RuleDeny, SourceProjectSettings)
		if err != nil {
			t.Fatalf("ParseRuleString(%q) 失败: %v", c.rule, err)
		}
		got := rule.Match("read_file", c.input)
		if c.rule[:4] == "bash" {
			got = rule.Match("bash", c.input)
		}
		if got != c.want {
			t.Errorf("规则 %-18s vs 输入 %-46s → 期望 %v，实际 %v（%s）",
				c.rule, c.input, c.want, got, c.note)
		} else {
			t.Logf("规则 %-18s → %v  (%s)", c.rule, got, c.note)
		}
	}
}
