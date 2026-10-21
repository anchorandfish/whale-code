package command

// help_column_alignment_test.go —— 判据：`/help` 的中文辅行必须与**英文描述列对齐**。
//
// # 为什么要有这一条（用户直报，2026-10-14）
//
// 原话：「/help的命令，中文注释没有和英文对齐，而是直接在命令的下面，我希望，中文注释和英文注释对齐」。
// 现象（before）：
//
//	  /checkpoint delete      Delete a session checkpoint
//	   删除指定检查点                       ← 中文落在**行首**（3 空格），视觉上"在命令名下面"
//	                          /checkpoint delete <id>   ← usage 在描述列（25 空格）
//
// 期望（after）：中文辅行与主行内容**同一列起点** ⇒ 命令名 / 英文描述 / 中文描述 / usage 四者左边缘对齐。
//
// # 判据为什么这么写
//
// ★ 列起点**从渲染产物里反推**（`strings.Index(实际主行, EN)` = 英文实际起始列），不硬编码 25。
// 这样两个漂移源都盖住：① 中文辅行的缩进常量被改；② 命令名/键名超出 `%-Ns` 把英文列推右。
// 硬编码 25 只能在①上生效，且会在②发生时给出"看起来对"的假绿。
//
// ★ 判据逻辑（`alignedCopyViolations`）与实现**不同源**：它只做"在产物里找主行 → 数英文起始列 →
// 比对下一行"的文本关系，不调用生产的 `util.BiIndent`/`helpDescIndent`，
// 因此实现写错时判据不会跟着错（避免共享 bug 造成的假绿）。
//
// ★ 判别性自证：`TestHelp_ColumnAlignmentGuard_CatchesMisalignedCopy` 喂一段**错误对齐**的
// 合成文本，必须被抓；再喂对齐版，必须不误报。发现"喂进去也不红" ⇒ 守卫是假的。

import (
	"strconv"
	"strings"
	"testing"

	"github.com/anchorandfish/whale-code/internal/util"
)

// alignCase 一条「主行 → 中文辅行」的期望关系。
// locate 是**主行定位串**（命令名或键名，不含前导缩进），en 是主行里的英文内容。
type alignCase struct{ locate, en, zh string }

// alignedCopyViolations 在 out 里逐条检查 alignCase：
// 找到主行（TrimSpace 后以 locate 开头且含 en）⇒ 取 en 在**该实际行**里的起始列 col
// ⇒ 断言其下一行恰为 `strings.Repeat(" ", col) + zh`。
//
// 返回第一条违规的描述（空切片 = 全部对齐）。
func alignedCopyViolations(out string, cases []alignCase) []string {
	var bad []string
	lines := strings.Split(out, "\n")
	for _, c := range cases {
		idx := -1
		for i, l := range lines {
			t := strings.TrimSpace(l)
			if strings.HasPrefix(t, c.locate) && strings.Contains(l, c.en) {
				idx = i
				break
			}
		}
		if idx < 0 {
			bad = append(bad, "判据前置失真：输出里找不到主行（locate="+c.locate+"）")
			continue
		}
		main := lines[idx]
		col := strings.Index(main, c.en)
		if col <= 0 {
			bad = append(bad, "判据前置失真：主行里定位不到英文内容 "+strconv.Quote(c.en))
			continue
		}
		if idx+1 >= len(lines) {
			bad = append(bad, "主行后没有中文辅行："+strconv.Quote(main))
			continue
		}
		want := strings.Repeat(" ", col) + c.zh
		if got := lines[idx+1]; got != want {
			bad = append(bad, "中文辅行未与英文列对齐：\n  期望(缩进 "+strconv.Itoa(col)+" 列) "+strconv.Quote(want)+
				"\n  实际              "+strconv.Quote(got))
		}
	}
	return bad
}

// TestHelp_CommandCopyAlignedToEnglishColumn 结果面：每条可见命令的中文描述，
// 必须与主行里的英文描述**同一列起点**（用户 2026-10-14 口径）。
//
// 判别性：before（中文辅行走 util.CopyIndent=3 空格）下本用例对**每一条**命令打红。
func TestHelp_CommandCopyAlignedToEnglishColumn(t *testing.T) {
	r := NewRegistry()
	help := FormatHelp(r, "")

	var cases []alignCase
	for _, cmd := range r.All() {
		if cmd.Hidden || cmd.Stub {
			continue
		}
		if cmd.Description == "" || cmd.DescriptionEN == "" {
			continue // 无中文可对齐（BiIndent 空串约定：只出主行）
		}
		cases = append(cases, alignCase{locate: "/" + cmd.Name, en: cmd.DescriptionEN, zh: cmd.Description})
	}
	if len(cases) < 40 {
		t.Fatalf("只收集到 %d 条命令配对（预期 ≥40）—— 判据前置失真", len(cases))
	}
	if bad := alignedCopyViolations(help, cases); len(bad) > 0 {
		t.Errorf("/help 有 %d 条命令的中文辅行未与英文描述列对齐：\n%s",
			len(bad), strings.Join(bad, "\n"))
	}
}

// TestHelp_ShortcutCopyAlignedToEnglishColumn 结果面：快捷键块同理。
func TestHelp_ShortcutCopyAlignedToEnglishColumn(t *testing.T) {
	r := NewRegistry()
	help := FormatHelp(r, "")

	var cases []alignCase
	for _, sc := range helpShortcuts {
		cases = append(cases, alignCase{locate: sc.key, en: sc.en, zh: sc.zh})
	}
	if len(cases) < 15 {
		t.Fatalf("快捷键表只有 %d 条（预期 ≥15）—— 判据前置失真", len(cases))
	}
	if bad := alignedCopyViolations(help, cases); len(bad) > 0 {
		t.Errorf("/help 有 %d 条快捷键的中文辅行未与英文说明列对齐：\n%s",
			len(bad), strings.Join(bad, "\n"))
	}
}

// TestHelp_ShortcutTableFitsColumn 结构门：按键名显示宽度必须 ≤ helpShortcutCol-3（=20），
// 否则 `%-20s` 不再生效、列起点被推右 ⇒ 中文列静默错位（数据表的价值就在这里：
// 加一条超宽键名会被**点名**，而不是让对齐悄悄坏掉）。
func TestHelp_ShortcutTableFitsColumn(t *testing.T) {
	const keyBudget = helpShortcutCol - 3 // 2 缩进 + 1 分隔空格
	for _, sc := range helpShortcuts {
		w := util.DisplayWidth(sc.key)
		if w > keyBudget {
			t.Errorf("快捷键 %q 显示宽度 %d 列 > 预算 %d 列：会把英文说明列推右、中文列随之错位。"+
				"请缩短键名，或同步调大 helpShortcutCol。", sc.key, w, keyBudget)
		}
	}
}

// TestHelp_ColumnAlignmentGuard_CatchesMisalignedCopy 判别性自证：
// ① 错误对齐（中文落在行首）**必须**被抓；② 对齐版**不得**误报。
// 不红 ⇒ 这个守卫是假的（技能 §4「判别性自检」）。
func TestHelp_ColumnAlignmentGuard_CatchesMisalignedCopy(t *testing.T) {
	const (
		mainLine = "  /checkpoint delete      Delete a session checkpoint"
		en       = "Delete a session checkpoint"
		zh       = "删除指定检查点"
	)
	c := alignCase{locate: "/checkpoint delete", en: en, zh: zh}

	// ① 反例：中文落在行首（= 用户直报的 before 形态）
	misaligned := mainLine + "\n" + util.CopyIndent + zh + "\n"
	if bad := alignedCopyViolations(misaligned, []alignCase{c}); len(bad) == 0 {
		t.Fatal("反例未被抓住：中文辅行落在行首（3 空格）时本判据应打红 —— 守卫失效")
	}

	// ② 正例：中文与英文同列（列起点从主行里量出来，不写死 25）
	aligned := mainLine + "\n" + strings.Repeat(" ", strings.Index(mainLine, en)) + zh + "\n"
	if bad := alignedCopyViolations(aligned, []alignCase{c}); len(bad) > 0 {
		t.Fatalf("正例被误报（对齐版不应违规）：%v", bad)
	}
}
