package command

import (
	"strings"
	"testing"

	"github.com/anchorandfish/whale-code/internal/util"
)

// v1_surface_test.go —— （2026-10-12 用户直报「/help 相关还是有问题」）判据。
//
// 用户口径（原话）：
// 1. 「英文为主中文为辅的意思是，英语是主要的，中文有的英文也要有」
// ⇒ /help 输出里凡含汉字的行必须是**缩进的中文辅行**（英文主行可独立读懂）。
// 旧行为：命令条目主行直接用中文 Description（英文只在补全弹窗），快捷键块纯中文。
// 2. 「1版有的所有命令都要有，其他无承诺的都不要有，当然你可以做个登记，方便以后改版」
// ⇒ v1 承诺面 = 09-08 验收文档 48 条 + 后续用户拍板增量
// （/monitors 家族 09-30 回执、/security-preset 10-10 判据册、/skill 10-11 O-2 拍板、
// /memory 10-12 14:50 拍板恢复、/sandbox status 10-14 拍板登记）
// = **55 条**（10-14 起）；10-03 登记批其余 14 条（/fast /daemon×8 /task×3 /session /session pick）
// **不在承诺清单** ⇒ 收回隐藏（handler 保留、手输仍可执行）。/memory 于 10-12 14:50
// 经用户拍板单独恢复可见（README 主打能力不可发现）—— 见回执 §2。
//
// 判别性：before（可见 68 / hidden 72 / 主行中文不缩进）下本文件全部红；
// 任何一条被错误重新取消隐藏、或新文案把中文写进不缩进的主行，也会各自点名打红。

// w082V1SurfaceReclaimed 是本轮收回隐藏的命令（家族写根名，精确计数由 TestW082_V1SurfaceExactCounts 盖）。
var w082V1SurfaceReclaimed = []string{
	"fast",
	"daemon start", "daemon stop", "daemon restart", "daemon status",
	"daemon add", "daemon list", "daemon cancel", "daemon log",
	"task list", "task show", "task cancel",
	"session", "session pick",
}

// TestW082_V1SurfaceExactCounts 结构面：注册表可见/隐藏/桩三计数精确钉住。
// ★ 本函数已于 2026-10-14 **有意翻转**（54→55，/sandbox status 登记）——**下一个人别当回归**：
//
//	翻转前钉的是「可见 54」，因为 v1 承诺面此前 = 09-08 基线 48 + monitors×3 +
//	security-preset + skill + memory[10-12 拍板恢复]。本轮用户直报「/sandbox status
//	显示里只有 /sandbox」⇒ 按用户拍板（方案 A）把 status 子命令注册为全名（弹窗可提示 +
//	/help 可见），它是 /sandbox 家族的**既有能力**（handleSandbox default 分支），登记即补承诺
//	—— 判例同构 /monitors 家族（09-30）。隐藏 86 / 桩 8 不变。
func TestW082_V1SurfaceExactCounts(t *testing.T) {
	r := NewRegistry()
	vis, hid, stub := 0, 0, 0
	for _, c := range r.All() {
		if c.Stub {
			stub++
		} else if c.Hidden {
			hid++
		} else {
			vis++
		}
	}
	if vis != 55 {
		t.Errorf("默认可见命令 = %d，应为 55（v1 承诺面：09-08 基线 48 + monitors×3 + security-preset + skill + memory[10-12 拍板恢复] + sandbox status[10-14 拍板登记]）", vis)
	}
	if hid != 86 {
		t.Errorf("隐藏命令 = %d，应为 86（10-03 登记批 14 条按  口径收回；/memory 已拍板恢复）", hid)
	}
	if stub != 8 {
		t.Errorf("桩命令 = %d，应为 8", stub)
	}
}

// TestW082_ReclaimedAreHidden 结构面：收回的 16 条必须 Hidden 且 handler 保留。
func TestW082_ReclaimedAreHidden(t *testing.T) {
	r := NewRegistry()
	for _, name := range w082V1SurfaceReclaimed {
		cmd, ok := r.Get(name)
		if !ok {
			t.Errorf("/%s 应在注册表中存在（收回 ≠ 删除：handler 保留，手输仍可执行）", name)
			continue
		}
		if !cmd.Hidden {
			t.Errorf("/%s 应标记 Hidden: true（v1 无承诺， 口径收回；登记见 内部工作回执）", name)
		}
		if cmd.Handler == nil {
			t.Errorf("/%s 的 handler 不应为 nil（收回隐藏只影响可见性）", name)
		}
	}
}

// TestW082_ReclaimedAbsentFromDefaultHelpAndCompletion 结果面 + 声明面：
// 收回命令不得出现在默认 /help 与 `/` 补全（前缀级）。
func TestW082_ReclaimedAbsentFromDefaultHelpAndCompletion(t *testing.T) {
	r := NewRegistry()
	help := FormatHelp(r, "")
	// ★ 判定方法（monitors 判据 §4.3 的同款经验）：不能用行首子串（" /session " 是
	// " /session save…" 的前缀，会假阳），也不能按空格切 token（多词命令名被腰斩）。
	// 用「前缀 + 该命令自己的 DescriptionEN 结尾」整行匹配 —— EN 互不相同 ⇒ 彼此不可顶替。
	for _, name := range w082V1SurfaceReclaimed {
		cmd, ok := r.Get(name)
		if !ok {
			continue
		}
		for _, line := range strings.Split(help, "\n") {
			trimmed := strings.TrimSpace(line)
			if strings.HasPrefix(trimmed, "/"+name+" ") && strings.HasSuffix(trimmed, cmd.DescriptionEN) {
				t.Errorf("默认 /help 不应列出无承诺命令 /%s（：收回隐藏）", name)
				break
			}
		}
	}
	for _, prefix := range []string{"", "me", "fas", "da", "task", "session"} {
		for _, got := range r.Completion(prefix) {
			for _, name := range w082V1SurfaceReclaimed {
				if got == name {
					t.Errorf("补全 %q 不应返回收回命令 %q", prefix, name)
				}
			}
		}
	}
	// 家族二级前缀也要盖住（收回 ≠ 部分收回）
	for _, prefix := range []string{"daemon ", "daemon s", "task l", "task c", "session p"} {
		if got := r.Completion(prefix); containsSlice(got, "daemon start") || containsSlice(got, "task list") || containsSlice(got, "session pick") {
			t.Errorf("补全 %q 不应返回收回命令，实际 %v", prefix, got)
		}
	}
}

// TestW082_HelpChineseOnlyOnIndentedLines 形态门：默认 /help 输出中，
// **含汉字的行必须以 CopyIndent（3 空格）开头** —— 即中文只允许出现在缩进辅行上。
//
// 这是对技能 `whale-code-bilingual-copy` §4-③ 全局形态铁律的直接落地（双语化之后
// /help 输出整体满足，不再需要锚行切片例外）。判别力：before 的命令主行
// ` /checkpoint save 创建带标签的会话检查点`（2 空格开头 + 汉字）即被打红。
// ⚠️ 若未来某条 Usage/主行重新混入汉字，本门会点名 —— 这是设计意图，别关门。
func TestW082_HelpChineseOnlyOnIndentedLines(t *testing.T) {
	r := NewRegistry()
	help := FormatHelp(r, "")
	for _, line := range strings.Split(help, "\n") {
		if util.HasHan(line) && !strings.HasPrefix(line, util.CopyIndent) {
			t.Errorf("/help 输出中含汉字的行必须以 %q 缩进开头（英文主行 + 中文辅行），违规行：%q",
				util.CopyIndent, line)
		}
	}
}

// TestW082_HelpEntryIsEnglishPrimary 结果面抽查：命令条目主行必须是英文描述、
// 中文描述在紧随其后的缩进辅行（正向对照 —— 形态门管「不许」，本条管「必须」）。
func TestW082_HelpEntryIsEnglishPrimary(t *testing.T) {
	r := NewRegistry()
	help := FormatHelp(r, "")
	cases := []struct{ name, enFragment, zh string }{
		{"checkpoint save", "Save", "创建带标签的会话检查点"},
		{"doctor", "Check environment", "诊断检查"},
		{"monitors", "List", "列出/停止/查看监控进程"},
	}
	for _, tc := range cases {
		cmd, ok := r.Get(tc.name)
		if !ok {
			t.Fatalf("/%s 未注册（判据前置）", tc.name)
		}
		en := cmd.DescriptionEN
		if !strings.Contains(en, tc.enFragment) {
			t.Fatalf("/%s 的 DescriptionEN=%q 未含 %q（判据前置失真）", tc.name, en, tc.enFragment)
		}
		// 主行：/name + EN 描述
		foundMain := false
		// 辅行：缩进 + 中文
		foundZH := false
		lines := strings.Split(help, "\n")
		for i, line := range lines {
			trimmed := strings.TrimSpace(line)
			if strings.HasPrefix(trimmed, "/"+tc.name+" ") && strings.Contains(line, en) {
				foundMain = true
				// 辅行 = 主行的下一非空行（Usage 行可能插在中间）
				for j := i + 1; j < len(lines) && j <= i+3; j++ {
					if strings.HasPrefix(lines[j], util.CopyIndent) && strings.Contains(lines[j], tc.zh) {
						foundZH = true
						break
					}
				}
				break
			}
		}
		if !foundMain {
			t.Errorf("/help 中 /%s 的主行应包含英文描述 %q", tc.name, en)
		}
		if !foundZH {
			t.Errorf("/help 中 /%s 的中文辅行（%q）缺失或未缩进", tc.name, tc.zh)
		}
	}
}

// TestW082_HelpLineWidthBudget 排版门：默认 /help 输出任何一行显示宽度 ≤ 80 列
// （汉字/全角/制表符按 2 列，SSOT = util.DisplayWidth）。
//
// 背景（2026-10-12 用户直报「排版没有对齐」）：/archive 主行 82 列、/review usage 101 列、
// 快捷键块 105 列 —— 超宽行被终端硬折行，折下来的尾巴与下一行黏连，视觉上「全乱了」。
// **中文不是对齐问题的原因**（中文在独立缩进辅行，主行是纯 ASCII 定宽），
// 元凶是超宽行本身。预算 80 列：常见终端 80–120 列，80 保底不折。
// 判别力：before 实测 8 行超宽，本门各点一名；新增长文案同样会被点名。
func TestW082_HelpLineWidthBudget(t *testing.T) {
	r := NewRegistry()
	help := FormatHelp(r, "")
	for i, line := range strings.Split(help, "\n") {
		if w := util.DisplayWidth(line); w > 80 {
			t.Errorf("/help 第 %d 行显示宽度 %d 列，超过 80 列预算（超宽会被终端硬折行、破坏对齐）：%q",
				i+1, w, line)
		}
	}
}

// TestW082_HelpShortcutsBilingual 快捷键块抽查：快捷键主行英文、操作说明在缩进辅行。
func TestW082_HelpShortcutsBilingual(t *testing.T) {
	r := NewRegistry()
	help := FormatHelp(r, "")
	if !strings.Contains(help, "Send message") {
		t.Errorf("/help 快捷键块应含英文主行（如 Send message）")
	}
	if !containsString(help, "发送消息") {
		t.Errorf("/help 快捷键块应保留中文辅行（发送消息）")
	}
}
