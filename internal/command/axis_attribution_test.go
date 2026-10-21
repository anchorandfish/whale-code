package command

// axis_attribution_test.go — G12/G13：**三个安全写入面的轴归属**必须写进帮助/提示。
//
// 用户明确要求：
//
//	「permisions 很容易被认为是常规的权限，在 help 和代码提示中要显示，
// 具体是啥，是单沙箱还是单表面的不包含沙箱」
//
// 本仓有三个安全写入面，写的轴**不同**：
//
//	/permissions <mode> → 审批轴 only（"问不问"） —— **不含沙箱**
//	/sandbox on|off → 能力轴 only（OS 隔离） —— **不含审批档**
//	/security-preset <id> → 两轴一起（预设）
//
// 三者若在帮助里只写"权限/沙箱"这类笼统措辞，用户无法判断某个命令到底动不动沙箱。
// 因此本文件把"描述里必须出现轴归属限定词"变成机器可判的守卫。
//
// # 判别性
//
// 把任一条描述改回笼统措辞（如把 permissions 改回 "View or change what Whale Code
// can do"）⇒ 必红。

import (
	"strings"
	"testing"
)

// axisAttributionCommands 是必须带轴归属声明的三个命令名。
var axisAttributionCommands = []string{"permissions", "sandbox", "security-preset"}

// TestRegistryDescriptionsDeclareAxisOwnership 判据：注册表（help / 补全的上游）里
// 三个安全命令的描述必须写明各自写哪些轴。
func TestRegistryDescriptionsDeclareAxisOwnership(t *testing.T) {
	reg := NewRegistry()
	byName := map[string]Command{}
	for _, c := range reg.All() {
		byName[c.Name] = c
	}

	// 前置断言：三者都必须存在且描述非空 —— 否则下面的断言会静默恒绿。
	for _, name := range axisAttributionCommands {
		c, ok := byName[name]
		if !ok {
			t.Fatalf("前置失败：命令 %q 未注册（help 与补全都看不到它）", name)
		}
		if strings.TrimSpace(c.Description) == "" {
			t.Fatalf("前置失败：%q 的描述为空 —— 轴归属断言会静默恒绿", name)
		}
	}

	// 单轴面必须**显式否定**另一轴（只说"查看/切换权限"是不够的）。
	if d := byName["permissions"].Description; !hasAxisWords(d, "no sandbox", "不含沙箱") {
		t.Errorf("/permissions 的描述必须显式说明它**不含沙箱**（否则用户会以为设了它就安全了）；got %q", d)
	}
	if d := byName["sandbox"].Description; !hasAxisWords(d, "no approval", "不含审批") {
		t.Errorf("/sandbox 的描述必须显式说明它**不含审批档**；got %q", d)
	}
	// 双轴面必须点明"两轴"。
	if d := byName["security-preset"].Description; !hasAxisWords(d, "Both axes", "两轴") {
		t.Errorf("/security-preset 的描述必须说明它**同时写两轴**；got %q", d)
	}
}

// TestSuggestionMetadataDeclaresAxisOwnership 判据：补全弹窗用的英文描述同样要带轴归属。
//
// 这是**第二条独立出口**：注册表用于 help，`englishCommandDescriptions` 用于
// slash 补全弹窗。只改一处 = 半修（同源点穷举，R3⑤）。
func TestSuggestionMetadataDeclaresAxisOwnership(t *testing.T) {
	// 前置断言：三个键都必须在表里，否则断言恒绿。
	for _, name := range axisAttributionCommands {
		d, ok := englishCommandDescriptions[name]
		if !ok || strings.TrimSpace(d) == "" {
			t.Fatalf("前置失败：englishCommandDescriptions[%q] 缺失或为空 —— 断言会静默恒绿", name)
		}
	}

	if d := englishCommandDescriptions["permissions"]; !strings.Contains(d, "sandbox") {
		t.Errorf("补全描述里 /permissions 必须点明它与沙箱的关系（不含沙箱）；got %q", d)
	}
	if d := englishCommandDescriptions["sandbox"]; !strings.Contains(d, "approval") {
		t.Errorf("补全描述里 /sandbox 必须点明它与审批轴的关系（不含审批档）；got %q", d)
	}
	if d := englishCommandDescriptions["security-preset"]; !strings.Contains(d, "BOTH") {
		t.Errorf("补全描述里 /security-preset 必须点明它写两轴；got %q", d)
	}
}

// hasAxisWords 判断描述里是否出现任一限定词。
func hasAxisWords(desc string, alternatives ...string) bool {
	for _, alt := range alternatives {
		if strings.Contains(desc, alt) {
			return true
		}
	}
	return false
}
