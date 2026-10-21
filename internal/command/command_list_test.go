// Package command — N16 命令描述中文化测试（02 §2.5.1 契约）。
// 覆盖反例 14：所有命令 Description 必须中文 + ≤50 字符。
// 覆盖反例 15：命令描述应以中文动词开头。
package command

import (
	"strings"
	"testing"
	"unicode/utf8"
)

// TestCostCommandRemoved /cost 命令全链路移除回归：
// provider 不返回费用详情，本地价格表估算与真实账单不符。
// /help（FormatHelp）与指令补全（registry.All）都从注册表渲染，
// 注册表不得再注册 cost 命令，否则 /help 与补全会同步复活。
func TestCostCommandRemoved(t *testing.T) {
	r := NewRegistry()
	for _, cmd := range r.All() {
		if cmd.Name == "cost" || strings.HasPrefix(cmd.Name, "cost ") {
			t.Errorf("命令 %q 仍注册在注册表（/cost 已移除）", cmd.Name)
		}
	}
	for _, args := range []string{"", "--all"} {
		if out := FormatHelp(r, args); strings.Contains(out, "/cost") {
			t.Errorf("FormatHelp(%q) 仍显示 /cost:\n%.300s", args, out)
		}
	}
}

// isEnglish 简单判断字符串是否纯英文（无中文字符）。
// 含中文字符（0x4e00-0x9fff）即视为中文描述。
func isEnglish(s string) bool {
	for _, r := range s {
		if r >= 0x4e00 && r <= 0x9fff {
			return false // 含中文字符
		}
	}
	return true
}

// TestCommandDescriptions_AllChinese 反例 14：所有命令 Description 必须中文 + ≤50 字符。
// 校验 02 §2.5.1 契约：命令 description 必须中文，≤50 字符。
func TestCommandDescriptions_AllChinese(t *testing.T) {
	r := NewRegistry()
	cmds := r.All()
	if len(cmds) == 0 {
		t.Fatal("注册命令数为 0，无法校验")
	}
	for _, cmd := range cmds {
		if cmd.Description == "" {
			t.Errorf("命令 %s Description 为空", cmd.Name)
			continue
		}
		if isEnglish(cmd.Description) {
			t.Errorf("命令 %s 描述仍为英文: %q（违反 02 §2.5.1 契约）", cmd.Name, cmd.Description)
		}
		runeCount := utf8.RuneCountInString(cmd.Description)
		if runeCount > 50 {
			t.Errorf("命令 %s 描述超 50 字符（%d）: %q", cmd.Name, runeCount, cmd.Description)
		}
	}
}

// TestCommandDescriptions_StartsWithVerb 反例 15：命令描述应以中文动词开头。
// 常见动词首字：显/压/诊/查/切/创/删/编/运/导/配/重/清/退/列/添/移/启/停/查/安/禁/启/生/回/强/提/恢/合/分/自/三/记/大/调/检/取/快/复/管/从/初/超/在/撤。
// 注意：部分命令描述以英文术语开头（如 "SSH"、"AI"），属于可接受范畴。
func TestCommandDescriptions_StartsWithVerb(t *testing.T) {
	verbs := []string{
		"显", "压", "诊", "查", "切", "创", "删", "编", "运", "导",
		"配", "重", "清", "退", "列", "添", "移", "启", "停", "安",
		"禁", "生", "回", "强", "提", "恢", "合", "分", "一", "A", "S",
		"自", "三", "记", "大", "调", "检", "取", "快", "复", "管",
		"从", "初", "超", "在", "撤", "命", "指", "切", "刷", "保",
		"暂", "登", "文", "本", "触", "沙", "验", "跨", "监", "关",
	}
	r := NewRegistry()
	cmds := r.All()
	for _, cmd := range cmds {
		if cmd.Description == "" {
			continue // 空描述在反例 14 已报错
		}
		runes := []rune(cmd.Description)
		if len(runes) == 0 {
			continue
		}
		firstChar := string(runes[0])
		found := false
		for _, v := range verbs {
			if firstChar == string(v) {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("命令 %s 描述应以动词开头: %q（首字 %q）", cmd.Name, cmd.Description, firstChar)
		}
	}
}
