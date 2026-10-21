package tui

// bilingual_guard_test.go — 钉「英文为主 + 中文为辅」的**文案形态**（2026-09-18 用户拍板）。
//
// # 形态契约（形态 SSOT = bilingual.go）
//
//	English main line ← 主行：可独立读懂的英文，**不得含汉字**
// Chinese auxiliary line ← 辅助行：以 copyIndent 开头，**中文只允许出现在这里**
//
// 等价说法：**任何含汉字的行，都必须以 copyIndent 开头** —— 这就是"禁止行内并列"。
//
// # 判别性
//
// 把任一被钉的渲染改回 `English / 中文` 同行 ⇒ 含汉字的主行不以缩进开头 ⇒ 本组测试红。
// 反向保：只断言"含汉字的行必须缩进"，**不**要求"所有中文都必须保留" ——
// 纯英文输出（如 footer）同样是合规形态，不得被这组门误判。

import (
	"strings"
	"testing"

	"github.com/anchorandfish/whale-code/internal/permission"
)

// hasHan 由 `bilingual.go` 转发自形态 SSOT（`util.HasHan`）。
// ★ 这里**不再**另写一份本地实现：那会让"什么算汉字"出现两套标准 ——
// 形态门与实现可能判定不一致（如区间写窄），是最难查的一类假绿。
// （2026-09-19 `N-039b` 下沉 SSOT 时删除本地实现。）
//
// ⚠️ 但**扫描逻辑**（"含汉字的行必须以 copyIndent 开头"）仍由本文件**独立**写出
// （见下方 assertChineseOnlyOnIndentedLines），**不**调用生产侧的
// `util.AssertChineseOnlyOnIndentedLines` —— 判据逻辑若与实现同源，
// 实现写错时判据会跟着错（共享 bug ⇒ 假绿）。
// 字符集同源、判据独立，才是这里要的分工。

// assertChineseOnlyOnIndentedLines 是形态的机械门。
func assertChineseOnlyOnIndentedLines(t *testing.T, name, out string) {
	t.Helper()
	for _, line := range strings.Split(out, "\n") {
		if !hasHan(line) {
			continue
		}
		if !strings.HasPrefix(line, copyIndent) {
			t.Errorf("%s：中文出现在**非缩进行**（行内并列？）⇒ %q", name, line)
		}
	}
}

// firstHanIndex 返回首个汉字的字节下标（-1 表示不含汉字）。
// 字符集沿用 SSOT（`hasHan`），扫描逻辑本地独立 —— 与文件头「字符集同源、判据独立」的分工一致。
func firstHanIndex(s string) int {
	for i, r := range s {
		if hasHan(string(r)) {
			return i
		}
	}
	return -1
}

// assertChineseOnlyAfterIndentBoxed 是 assertChineseOnlyOnIndentedLines 的**盒式渲染**版本。
//
// 盒式渲染（lipgloss Border + Padding）会在每行内容前加边框与内边距（如 `│ `），
// 因此「整行必须以 copyIndent 开头」无法直接套用。等价判据：
// **每条含汉字的行里，中文之前那一段必须以 copyIndent 结尾**（中文只能跟在缩进之后，禁止与英文同行并列）。
//
// 判别性不变：把中文改回 `" — " + DescriptionZH`（同行）⇒ 中文前面不是 copyIndent ⇒ 红。
func assertChineseOnlyAfterIndentBoxed(t *testing.T, name, out string) {
	t.Helper()
	for _, line := range strings.Split(out, "\n") {
		idx := firstHanIndex(line)
		if idx < 0 {
			continue
		}
		if !strings.HasSuffix(line[:idx], copyIndent) {
			t.Errorf("%s：中文出现在**非缩进位置**（行内并列？）⇒ %q", name, line)
		}
	}
}

func TestBi_ShapeAndEmptyConvention(t *testing.T) {
	if got, want := bi("Main line", "辅助行"), "Main line\n"+copyIndent+"辅助行"; got != want {
		t.Fatalf("bi 形态错误：\n got %q\nwant %q", got, want)
	}
	if got := bi("only en", ""); got != "only en" {
		t.Fatalf("中文为空时应只返回英文，actual %q", got)
	}
	if got := bi("", "只有中文"); !strings.HasPrefix(got, copyIndent) {
		t.Fatalf("英文为空时中文仍必须缩进，actual %q", got)
	}
	if got := bi("English", "第一行\n第二行"); !strings.Contains(got, copyIndent+"第二行") {
		t.Fatalf("多行中文的每一行都要缩进，actual %q", got)
	}
}

func TestBiSame_SharesOneArgList(t *testing.T) {
	got := biSame("Value: %s", "取值：%s", "x")
	want := "Value: x\n" + copyIndent + "取值：x"
	if got != want {
		t.Fatalf("biSame 形态错误：\n got %q\nwant %q", got, want)
	}
}

// TestSecurityPresetCopy_ChineseOnlyOnIndentedLines 钉 /security-preset 的全部用户可见输出。
func TestSecurityPresetCopy_ChineseOnlyOnIndentedLines(t *testing.T) {
	policy := permission.NewPolicyFromModeName("workspace-write")

	cases := map[string]string{
		"securityPresetListing":       securityPresetListing(policy, nil),
		"securityPresetUnknownNotice": securityPresetUnknownNotice("bogus"),
	}
	for _, id := range []string{"safe", "standard", "unrestricted"} {
		p, ok := permission.LookupSecurityPreset(id)
		if !ok {
			t.Fatalf("前置失败：内置预设 %q 不存在", id)
		}
		cases["appliedNotice("+id+")"] = securityPresetAppliedNotice(p, nil)
	}
	for name, out := range cases {
		assertChineseOnlyOnIndentedLines(t, name, out)
	}
}

// TestAxisNotices_ChineseOnlyOnIndentedLines 钉轴归属提示（G12/G13 主题的三条写入面）。
func TestAxisNotices_ChineseOnlyOnIndentedLines(t *testing.T) {
	cases := map[string]string{
		"sandboxAxisNotice":                 sandboxAxisNotice(permission.LevelWorkspaceWrite),
		"approvalAxisNotice":                approvalAxisNotice(permission.LevelWorkspaceWrite, nil),
		"sandboxDisableNotice":              sandboxDisableNotice,
		"unknownApprovalModeNotice(mode)":   unknownApprovalModeNotice("read-only"),
		"unknownApprovalModeNotice(preset)": unknownApprovalModeNotice("safe"),
	}
	for name, out := range cases {
		assertChineseOnlyOnIndentedLines(t, name, out)
	}
}

// TestSecurityPresetMenuChoices_EnglishMainChineseAux 钉菜单数据的语言分工。
func TestSecurityPresetMenuChoices_EnglishMainChineseAux(t *testing.T) {
	opts, sel := securityPresetMenuChoices(permission.LevelWorkspaceWrite, true)
	if len(opts) != 3 {
		t.Fatalf("应有 3 项，actual %d", len(opts))
	}
	if sel != 1 {
		t.Fatalf("workspace-write + 沙箱开 应高亮 standard（下标 1），actual %d", sel)
	}
	for _, o := range opts {
		if hasHan(o.Label) {
			t.Errorf("Label 是英文主文案，不得含汉字：%q", o.Label)
		}
		if hasHan(o.Description) {
			t.Errorf("Description 是英文主文案，不得含汉字：%q", o.Description)
		}
		if o.DescriptionZH == "" {
			t.Errorf("预设 %q 缺中文辅助文案（DescriptionZH）", o.Value)
		}
	}
}

// TestPermissionModeMenuChoices_EnglishMainChineseAux 同型钉 /permissions 的档位菜单。
func TestPermissionModeMenuChoices_EnglishMainChineseAux(t *testing.T) {
	opts, sel := permissionModeMenuChoices(permission.LevelWorkspaceWrite)
	if len(opts) != 3 {
		t.Fatalf("应有 3 项，actual %d", len(opts))
	}
	if sel != 1 {
		t.Fatalf("workspace-write 应高亮下标 1，actual %d", sel)
	}
	for _, o := range opts {
		if hasHan(o.Label) || hasHan(o.Description) {
			t.Errorf("Label/Description 应为英文主文案：%q / %q", o.Label, o.Description)
		}
		if o.DescriptionZH == "" {
			t.Errorf("档位 %q 缺中文辅助文案（DescriptionZH）", o.Value)
		}
	}
}

// TestRenderChoiceUI_ChineseAuxLineIndented 钉**唯一活线渲染器**（选项框）：
// 菜单的 DescriptionZH 必须渲染成缩进辅助行，不得与 Description 同行。
//
// ★ 2026-09-22：本测试原断言落在 `renderPendingChoice` 上 —— 而该函数**生产零调用**
// （调用点只有测试），整个函数被 linker 丢弃（`go tool nm whale | grep -c renderPendingChoice` = 0）
// ⇒ 这条门一直是绿的，**却从未覆盖线上真正渲染的那个函数**。
// 现改钉 `renderChoiceUI`（真实渲染点：app.go 的 StateWaitingChoice 分支）。
//
// 判别性：把 render_extras.go 的中文分支改回 `" — " + DescriptionZH`（同行）⇒ 红。
func TestRenderChoiceUI_ChineseAuxLineIndented(t *testing.T) {
	m := newTUIRegressionModel(t)
	m.toolService.SetPendingChoice(&PendingChoice{
		Prompt: "Pick an option",
		Options: []ChoiceOption{
			{Label: "read-only", Value: "read-only", Description: "Read only", DescriptionZH: "只读"},
		},
		DefaultIdx: 0,
	})
	m.toolService.SetChoiceIdx(0)

	out := m.renderChoiceUI()
	assertChineseOnlyAfterIndentBoxed(t, "renderChoiceUI", out)
	if !strings.Contains(out, " — Read only") {
		t.Fatalf("应保留英文主文案（同行追加）：%q", out)
	}
	if !strings.Contains(out, copyIndent+"只读") {
		t.Fatalf("中文辅助行必须以 copyIndent 缩进另起一行：%q", out)
	}
}

// TestRenderChoiceUI_NoChineseWhenNoDescriptionZH 反向保：
// 未填 DescriptionZH 时输出 **不得** 出现任何中文辅助行 —— 既有英文菜单外观零变化。
//
// ★ 同时把「无 Other 哨兵 ⇒ 不显示 [i] 提示」钉在这里：本地菜单（权限/会话/安全套餐）
// 走的就是这一形态， 的提示只对 AskUser 类提问出现。
func TestRenderChoiceUI_NoChineseWhenNoDescriptionZH(t *testing.T) {
	m := newTUIRegressionModel(t)
	m.toolService.SetPendingChoice(&PendingChoice{
		Prompt:     "Pick",
		Options:    []ChoiceOption{{Label: "standard", Value: "standard", Description: "desc"}},
		DefaultIdx: 0,
	})
	m.toolService.SetChoiceIdx(0)

	out := m.renderChoiceUI()
	if hasHan(out) {
		t.Fatalf("未填 DescriptionZH 时不应出现任何中文：%q", out)
	}
	if !strings.Contains(out, " — desc") {
		t.Fatalf("英文 Description 应同行渲染：%q", out)
	}
}
