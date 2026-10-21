package tools

// capability_coverage_guard_test.go — 能力分桶的**覆盖守卫**（R2）。
//
// 为什么放在 internal/tools 而不是 internal/permission：
// 分桶的真相源在 permission 包，但**注册表**在 tools 包；permission 不能反向 import tools
// （import cycle），所以"每个已注册工具都被分类"这条只能在这里断言。
// 这与 A 会话的 `TestGuard_EveryRegisteredToolHasLevel` 成对（同一注册表、两个维度）。
//
// 三条不变量：
//  1. 每个**已注册**工具必须至少具备一个能力 —— 新增工具未分类即红（★ 这是防将来再漂移的关键门）；
//  2. 未登记的工具**不得**拿到任何能力（没有默认桶）—— 否则守卫 1 永远为绿、零判别力；
//  3. `deny <Cap>(*)` 必须覆盖该能力下的**全部**工具，且**不得**溢出到其它能力
//     （溢出会让 `Read(*)` 连带禁掉无关能力）。

import (
	"sort"
	"strings"
	"testing"

	"github.com/anchorandfish/whale-code/internal/permission"
)

// TestGuard_EveryRegisteredToolHasCapability 不变量 1。
func TestGuard_EveryRegisteredToolHasCapability(t *testing.T) {
	reg := NewRegistry()
	all := reg.All()
	if len(all) == 0 {
		t.Fatal("注册表为空 —— 守卫失去意义，先查 NewRegistry/registerBuiltinTools")
	}

	var missing []string
	for _, spec := range all {
		if len(permission.CapabilitiesOf(spec.Name)) == 0 {
			missing = append(missing, spec.Name)
		}
	}
	if len(missing) > 0 {
		sort.Strings(missing)
		t.Fatalf("有 %d 个已注册工具没有能力分类（新增工具必须显式分桶）：\n  %s\n"+
			"改哪里：internal/permission/capability.go 的 toolCapabilities（唯一真相源）",
			len(missing), strings.Join(missing, "\n  "))
	}
	t.Logf("已注册工具 %d 个，全部有显式能力分类", len(all))
}

// TestGuard_NoImplicitDefaultCapability 不变量 2（判别力自检）。
//
// 若有人图省事把 `CapabilitiesOf` 改成"未登记 → 返回 Meta"，
// 那么 TestGuard_EveryRegisteredToolHasCapability 会**永远绿**。
// 本用例把那个退化形态钉死：未登记的工具必须是 0 个能力。
func TestGuard_NoImplicitDefaultCapability(t *testing.T) {
	for _, bogus := range []string{
		"totally_unknown_tool",
		"read_file_v2",   // 看似是 read 家族的"新版本"
		"export_session", // 真实存在但故意用错误大小写以外的变体校验下面一条
		"tool_that_does_not_exist",
	} {
		if bogus == "export_session" {
			continue // 它是已登记工具，不能用作"未登记"样本
		}
		if caps := permission.CapabilitiesOf(bogus); len(caps) != 0 {
			t.Errorf("未登记工具 %q 不应有任何能力（禁止默认桶），实际得到 %v", bogus, caps)
		}
	}

	// 大小写归一：已登记工具无论怎么传都应拿到同样结果。
	lower := permission.CapabilitiesOf("read_file")
	upper := permission.CapabilitiesOf("READ_FILE")
	mixed := permission.CapabilitiesOf("Read_File")
	if len(lower) == 0 {
		t.Fatal("read_file 应有能力分类，实际为空")
	}
	if strings.Join(capStrings(upper), ",") != strings.Join(capStrings(lower), ",") ||
		strings.Join(capStrings(mixed), ",") != strings.Join(capStrings(lower), ",") {
		t.Errorf("CapabilitiesOf 应大小写不敏感：read_file=%v READ_FILE=%v Read_File=%v",
			lower, upper, mixed)
	}
}

// TestGuard_DenyCoversCapability 不变量 3（覆盖 + 不溢出）。
func TestGuard_DenyCoversCapability(t *testing.T) {
	reg := NewRegistry()

	// 按能力归组已注册工具。
	byCap := map[permission.Capability][]string{}
	for _, spec := range reg.All() {
		for _, c := range permission.CapabilitiesOf(spec.Name) {
			byCap[c] = append(byCap[c], spec.Name)
		}
	}

	caps := permission.CapabilityNames()
	if len(caps) == 0 {
		t.Fatal("CapabilityNames 为空")
	}

	for _, c := range caps {
		members := byCap[c]
		if len(members) == 0 {
			t.Errorf("能力 %s 下没有任何已注册工具 —— 要么分桶写错，要么该能力是多余的", c)
			continue
		}

		rule, err := permission.ParseRuleString(string(c)+"(*)", permission.RuleDeny, permission.SourceUserSettings)
		if err != nil {
			t.Fatalf("解析 deny 规则 %s(*) 失败: %v", c, err)
		}

		// (a) 覆盖：该能力下每个工具都必须被命中。
		notCovered := []string{}
		for _, tool := range members {
			if !rule.Match(tool, `{}`) {
				notCovered = append(notCovered, tool)
			}
		}
		if len(notCovered) > 0 {
			sort.Strings(notCovered)
			t.Errorf("deny %s(*) 未覆盖本能力下的工具：%v", c, notCovered)
		}

		// (b) 不溢出：**不属于**该能力的工具必须不被命中。
		overreach := []string{}
		for _, spec := range reg.All() {
			if hasCapability(spec.Name, c) {
				continue
			}
			if rule.Match(spec.Name, `{}`) {
				overreach = append(overreach, spec.Name)
			}
		}
		if len(overreach) > 0 {
			sort.Strings(overreach)
			t.Errorf("deny %s(*) 溢出到不属于该能力的工具（会产生与 %s 无关的副作用）：%v",
				c, c, overreach)
		}

		t.Logf("%-8s 覆盖 %2d 个工具：%v", c, len(members), members)
	}
}

func hasCapability(tool string, c permission.Capability) bool {
	for _, got := range permission.CapabilitiesOf(tool) {
		if got == c {
			return true
		}
	}
	return false
}

func capStrings(caps []permission.Capability) []string {
	out := make([]string, 0, len(caps))
	for _, c := range caps {
		out = append(out, string(c))
	}
	return out
}
