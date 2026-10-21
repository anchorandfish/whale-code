package command

import (
	"strings"
	"testing"
)

// n105Case 是 的表驱动判据条目。
//
// want = IsCommandInput 的期望值
// oldWouldSay = **旧判据**（裸 `strings.HasPrefix(input, "/")`）在同一输入上的取值
//
// ★ oldWouldSay 不是为了"顺便测一下旧写法"，它是**判别性自证**：
// 本条目的修复对象是一个**新建函数**（修复前它不存在），所以无法跑"before 态"。
// 替代办法 = 把旧判据的取值显式写进表里，由 TestN105_IsCommandInput 逐条核对。
// 于是「want=false 且 oldWouldSay=true」的每一条，都是**本次修复真实改变了行为**的证据；
// 若整张表都满足 oldWouldSay == want，说明选的输入根本打不到差异（这条修复等于没修）。
type n105Case struct {
	name        string
	input       string
	want        bool
	oldWouldSay bool
}

var n105Cases = []n105Case{
	// ===== 正例：真命令必须仍判为命令（防"修好路径、顺手打死命令"）=====
	{"单段命令", "/permissions", true, true},
	{"多词命令（knownPrefixes）", "/session list", true, true},
	{"多词命令（第二组）", "/checkpoint save", true, true},
	{"带连字符", "/compact-strategy", true, true},
	{"大写字母（Parse 会 ToLower）", "/COMMIT-PUSH-PR", true, true},
	{"未注册的裸词 ⇒ 有意保留 Unknown 提示", "/does-not-exist", true, true},

	// ===== 反例：路径形状必须判为普通文本（本条主诉）=====
	{"★ 真机原文（无空格 · 中文逗号）", "/private/tmp/n077_t4/hello.txt，里面写hello", false, true},
	{"绝对路径", "/Users/example/project/src/main.rs", false, true},
	{"路径 + 中文追问", "/private/tmp/x.txt 帮我看看", false, true},
	{"含 `.` 的名字", "/foo.bar", false, true},
	{"含反斜杠", `/private\tmp\x.txt`, false, true},
	{"光一个斜杠", "/", false, true},
	{"双斜杠", "//", false, true},
	{"无扩展名的深路径", "/var/log/messages", false, true},

	// ===== 前导空白 = 正式逃逸语义（本函数刻意不 TrimSpace）=====
	{"前导空格 ⇒ 文本", " /permissions", false, false},
	{"前导 Tab ⇒ 文本", "\t/permissions", false, false},
	{"前导换行 ⇒ 文本", "\n/permissions", false, false},

	// ===== 普通文本 =====
	{"中文", "你好", false, false},
	{"英文", "hello", false, false},

	// ===== ★ 已知限制：不做磁盘兜底 ⇒ 单段绝对路径仍判为命令（**有意**，勿当失败）=====
	{"已知限制 · /tmp", "/tmp", true, true},
	{"已知限制 · /tmp + 追问", "/tmp 帮我看看", true, true},

	// ===== Parse 既有语义：`/` 之后的空白由 Parse 吃掉 =====
	{"斜杠 + 空格 + 命令", "/ permissions", true, true},
}

func TestN105_IsCommandInput(t *testing.T) {
	for _, c := range n105Cases {
		if got := IsCommandInput(c.input); got != c.want {
			t.Errorf("%s: IsCommandInput(%q) = %v, want %v", c.name, c.input, got, c.want)
		}
		// 表自身的一致性：oldWouldSay 必须等于旧判据的真实取值。
		// 它红了不是"实现错"，是"表写错了" —— 两者必须能区分开。
		if old := strings.HasPrefix(c.input, "/"); old != c.oldWouldSay {
			t.Errorf("%s: 旧判据 strings.HasPrefix(%q, \"/\") = %v，与表里的 oldWouldSay=%v 不符 ⇒ **表写错了**",
				c.name, c.input, old, c.oldWouldSay)
		}
	}
}

// TestN105_TableStillDiscriminates 守卫**判据表本身**。
//
// 若有人把"旧判据会判错"的用例删光，本表就退化成一堆 oldWouldSay == want 的条目 ——
// 此时测试仍会全绿，但它已经不再证明"本次修复改变了行为"。
// 本测试把这种"悄悄削弱判别力"变成红灯。
func TestN105_TableStillDiscriminates(t *testing.T) {
	const minDiscriminating = 8 // want=false 且 oldWouldSay=true 的条目数下限
	n := 0
	for _, c := range n105Cases {
		if !c.want && c.oldWouldSay {
			n++
		}
	}
	if n < minDiscriminating {
		t.Fatalf("判别性用例只剩 %d 条（下限 %d）⇒ 判据表被削弱，无法再证明修复改变了行为", n, minDiscriminating)
	}
	t.Logf("判别性用例 %d 条（旧判据在这些输入上都会误判为命令）", n)
}

// TestN105_WhitelistCoversEveryRegisteredCommand 是**白名单的安全不变量**。
//
// ④ 的字符集白名单是本条修复里唯一"可能误伤"的一环：命令名只要含白名单外的字符，
// 就会被 IsCommandInput 判成普通文本 —— **静默地**不再执行。
// 所以这里正向断言：**每一个已注册命令名都必须满足白名单**。
// 将来若有人注册 `/foo.bar`，本测试红；而不是它在生产里被静默吞掉、事后无从归因。
func TestN105_WhitelistCoversEveryRegisteredCommand(t *testing.T) {
	all := NewRegistry().All()
	if len(all) == 0 {
		t.Fatal("注册表为空 ⇒ 本守卫会假绿（必须先证明它能被喂到数据）")
	}
	for _, cmd := range all {
		if !isCommandNameShaped(strings.ToLower(cmd.Name)) {
			t.Errorf("已注册命令 %q 不满足命令名白名单 ⇒ IsCommandInput 会把它当普通文本。\n"+
				"二选一：把该字符加入白名单（须同时复核路径误判面），或改这个命令名。", cmd.Name)
		}
	}
	t.Logf("已核 %d 个已注册命令名，全部满足白名单", len(all))
}

// TestN105_EveryRegisteredCommandIsRecognized 是「判定 ↔ 注册表」的一致性守卫：
// 从注册表反推输入串，逐个喂给 IsCommandInput，**每一个都必须判为命令**。
//
// 与上一条互补：上一条查"字符集"，这一条查"整串"（覆盖 Parse 的切名逻辑，
// 例如多词命令 `session list` 是否被正确切成一个名字而不是 `session`）。
func TestN105_EveryRegisteredCommandIsRecognized(t *testing.T) {
	all := NewRegistry().All()
	if len(all) == 0 {
		t.Fatal("注册表为空 ⇒ 本守卫会假绿")
	}
	for _, cmd := range all {
		in := "/" + cmd.Name
		if !IsCommandInput(in) {
			t.Errorf("%q 是已注册命令，其输入串 %q 却被判为普通文本", cmd.Name, in)
		}
	}
	t.Logf("已核 %d 个已注册命令，全部被 IsCommandInput 认作命令", len(all))
}

// TestN105_CompletionPredicateDivergesExactlyOnEmptyName 钉死**两条谓词的关系**。
//
// 背景（2026-09-20 批 C 实测发现）：typeahead 的补全触发与"提交是不是命令"**不是同一个问题**。
// 把补全侧直接换成 IsCommandInput 会让「输入 `/` 看全部命令」这个基本 UX 消失
// （实测：`TestExtractTokenWithTriggerSlash/empty_command` 立刻变红）。
// ⇒ 正确做法 = 同一实现 + 一处取值差异，而不是两条各写一份，也不是强行合并。
//
// 本测试断言的不变量：**两条谓词当且仅当 `Parse` 切出的 name 为空时分叉**；
// name 非空时（含路径、前导空白、多词命令、未知名字）两边**必须逐输入相同**。
// 这既防"补全侧偷偷放宽成 HasPrefix"，也防"提交侧被顺手改成空名放行"。
func TestN105_CompletionPredicateDivergesExactlyOnEmptyName(t *testing.T) {
	inputs := []string{
		"/", "/   ", "/permissions", "/session list", "/private/tmp/x.txt",
		" /permissions", "   /permissions", "hello /per", "//", "/tmp", "", "/PER",
		"/review @mai", "/private/tmp/n077_t4/hello.txt，里面写hello",
	}

	diverged := 0
	for _, in := range inputs {
		name, _ := Parse(in)
		emptyName := strings.HasPrefix(in, "/") && name == ""
		submit, complete := IsCommandInput(in), IsCommandCompletionInput(in)

		if submit == complete {
			if emptyName {
				t.Errorf("%q：Parse 的 name 为空 ⇒ 两条谓词本应分叉，实测都 = %v", in, submit)
			}
			continue
		}
		diverged++
		if !emptyName {
			t.Errorf("%q：只允许在 name 为空时分叉，实测 IsCommandInput=%v 而 IsCommandCompletionInput=%v",
				in, submit, complete)
		}
	}

	// 点名两条关键取值的**方向**（光"分叉了"不够，方向反了同样错）。
	if IsCommandInput("/") {
		t.Error("IsCommandInput(\"/\") 必须为 false：裸 / 没有可执行的命令名")
	}
	if !IsCommandCompletionInput("/") {
		t.Error("IsCommandCompletionInput(\"/\") 必须为 true：裸 / 正是「看全部命令」的入口")
	}
	if diverged == 0 {
		t.Fatal("没有任何输入让两条谓词分叉 ⇒ 本测试打不到差异点（判别器失效）")
	}
	t.Logf("已核 %d 条输入，其中 %d 条在 name 为空处分叉（预期 ≥1）", len(inputs), diverged)
}
