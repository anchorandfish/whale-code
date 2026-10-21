// Package secret — G-C 落盘验收：A7（规则表 fail-fast）。
//
// 本文件只用**既有**符号 + A7 新增的 `patternTableViolation`，用途有二：
//  1. 守卫：真实规则表在任何时候都必须通过校验（否则进程根本不该起来）；
//  2. 判别：证明该守卫**有牙** —— 注入坏表必须被判出，而不是恒真断言。
//
// A/B 的"before" = 把 `patternTableViolation` 的检查体摘掉（函数名仍在，故两侧都能编译），
// 此时 `TestA7_Probe_*` 会精确打红。
package secret

import (
	"regexp"
	"strings"
	"testing"
)

// TestA7_RealPatternTableIsValid 守卫：仓内真实规则表必须自洽。
//
// 失败含义：进程启动会 panic（fail-fast 生效）——但那是**运行时**才发现，
// 所以这条用例的作用是把它提前到 CI。
func TestA7_RealPatternTableIsValid(t *testing.T) {
	if err := patternTableViolation(sensitivePatterns, masterAnyPatternRegex); err != nil {
		t.Fatalf("真实规则表未通过 A7 校验（该状态下进程根本起不来）：%v", err)
	}
	// 显式钉住"表非空 + 每条规则可判定"，避免有人删规则后守卫被顺手改松。
	if len(sensitivePatterns) == 0 {
		t.Fatal("规则表为空：所有脱敏/检测会静默全失效")
	}
	for _, p := range sensitivePatterns {
		if p.id == "" || p.label == "" || p.re == nil || p.re.String() == "" {
			t.Errorf("规则不完整: id=%q label=%q re=%v", p.id, p.label, p.re)
		}
	}
}

// TestA7_Probe_BadPatternTablesAreRejected 判别性：每一类结构性坏表都必须被判出。
//
// 若把 `patternTableViolation` 改成 `return nil`（before），本用例逐条打红 ——
// 这就是"守卫可判别"的证据。
func TestA7_Probe_BadPatternTablesAreRejected(t *testing.T) {
	good := secretPattern{id: "k", label: "K", re: regexp.MustCompile(`k-[a-z]{4}`)}
	goodMaster := buildMasterRegexFrom([]secretPattern{good})

	cases := []struct {
		name     string
		patterns []secretPattern
		master   *regexp.Regexp
		wantSub  string // 期望错误信息里出现的关键词（证明报的是**这一类**问题）
	}{
		{
			name:     "空表（最隐蔽的 fail-open：连 recover 都不触发）",
			patterns: nil,
			master:   goodMaster,
			wantSub:  "empty",
		},
		{
			name:     "id 为空",
			patterns: []secretPattern{{id: "", label: "L", re: good.re}},
			master:   goodMaster,
			wantSub:  "empty id",
		},
		{
			name:     "id 重复",
			patterns: []secretPattern{good, good},
			master:   goodMaster,
			wantSub:  "duplicate",
		},
		{
			name:     "label 为空",
			patterns: []secretPattern{{id: "a", label: "", re: good.re}},
			master:   goodMaster,
			wantSub:  "label",
		},
		{
			name:     "正则为 nil（→ panic → recover → 静默放行）",
			patterns: []secretPattern{{id: "a", label: "A", re: nil}},
			master:   goodMaster,
			wantSub:  "nil",
		},
		{
			name:     "正则源为空（空正则匹配任意位置 ⇒ 恒命中）",
			patterns: []secretPattern{{id: "a", label: "A", re: regexp.MustCompile("")}},
			master:   goodMaster,
			wantSub:  "empty",
		},
		{
			name:     "master 为 nil",
			patterns: []secretPattern{good},
			master:   nil,
			wantSub:  "master",
		},
		{
			name:     "master 与规则表不一致（快/慢路径判定会分裂）",
			patterns: []secretPattern{good},
			master:   buildMasterRegexFrom([]secretPattern{{id: "other", label: "O", re: regexp.MustCompile(`zzz`)}}),
			wantSub:  "inconsistent",
		},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			err := patternTableViolation(c.patterns, c.master)
			if err == nil {
				t.Fatalf("A7 守卫失灵：%s 应当被判出，实际 nil（守卫无牙 = 假绿）", c.name)
			}
			if !strings.Contains(err.Error(), c.wantSub) {
				t.Errorf("错误信息未指向具体成因：got %q，期望含 %q", err.Error(), c.wantSub)
			}
		})
	}
}

// TestA7_Evidence_EmptyTableIsSilentNotLoud 给出"为什么必须 fail-fast"的现场证据：
// 规则表一旦为空，脱敏与检测**不会**报错、**不会** panic，只是静默判"没有密钥"；
// 更糟的是 `DetectLeakWithLabels` 会进入**自相矛盾**状态（快路径说命中、慢路径数不出规则 ⇒ 空标签）。
//
// 这正是 A7 选择的理由：真红线是"让失效不可达"，而不是"把运行期改成 fail-closed"。
func TestA7_Evidence_EmptyTableIsSilentNotLoud(t *testing.T) {
	const raw = "ANTHROPIC_API_KEY=sk-ant-api03-AAAABBBBCCCCDDDDEEEE"

	saved := sensitivePatterns
	sensitivePatterns = nil
	t.Cleanup(func() { sensitivePatterns = saved })

	// ① 脱敏静默失效：原样返回（没有任何错误信号）。
	if got := RedactSensitive(raw); got != raw {
		t.Fatalf("前置不成立：空表下 RedactSensitive 不应改动文本，got %q", got)
	}
	// ② 检测静默失效：判"无泄露"（没有任何错误信号）。
	if IsSecretLeak(raw) {
		t.Fatal("前置不成立：空表下 IsSecretLeak 不应命中")
	}
	// ③ 复合入口进入自相矛盾态：快路径（master 正则）仍命中，慢路径（逐规则）数不出标签
	//    ⇒ 调用方会拿到 `hit=true` 但 `labels==""` 的错误提示（无法归因）。
	labels, hit := DetectLeakWithLabels(raw)
	if !hit || labels != "" {
		t.Fatalf("前置不成立：期望 (labels=\"\", hit=true)，got (labels=%q, hit=%v)", labels, hit)
	}

	t.Log("已证：规则表为空 ⇒ 脱敏静默失效 + 检测自相矛盾（无 panic / 无错误）⇒ 必须 fail-fast 在启动阶段")
}
