package cmdrules

import (
	"strings"
	"testing"
)

// 残余①「换行洗白」判别器。
//
// 现象：`\n` 在 shell 里是**命令分隔符**，而 `Normalize` 把它与空格同类折叠
// （`cmdrules.go` 的 `if ch == '\t' || ch == '\r' || ch == ' ' || ch == '\n'`）
// ⇒ 两条命令被并成**一段** ⇒ 所有「按命令位判定」的消费方都只看第一段的段首词。
//
// 两个方向的后果（同一根因，互为镜像）：
// - **只读面（免问）**：`cat a\nmkdir b` 被判「全段只读」⇒ 的免问出口**静默放行** mkdir；
// - **危险面（deny）**：`# 注释\nrm -rf /tmp/x` 的段首词是 `#` ⇒ 危险档**漏判**。
//
// 为什么 Normalize 该保留 `\n`：它的职责是抵御「**纯书写差异**」（`rm -rf /` 多空格），
// 而 `\n` 不是书写差异 —— 它改变命令的**语义**（一条 vs 两条）。把它当空白折掉，
// 等于把一个**结构性事实**降级成**书写噪声**（本批主题：拿代理顶替真值）。
//
// ★ 判别器纪律（本仓铁律）：本文件先在**修前**跑，必须**红**；不红 ⇒ 说明我读错了。

func TestW025_NewlineIsSeparator_NotWhitespace_FalseCases(t *testing.T) {
	cases := []struct{ name, cmd, why string }{
		{"read-then-mkdir", "cat a\nmkdir b", "换行后是两条命令，第二段命令位 mkdir 非白名单"},
		{"read-then-rm", "cat a\nrm -rf /tmp/x", "第二段命令位 rm 非白名单（且属危险/破坏性档）"},
		{"crlf-read-then-mkdir", "cat a\r\nmkdir b", "CRLF 同样是命令分隔"},
		{"mkdir-then-read", "mkdir b\ncat a", "第一段非白名单"},
		{"leading-blank-lines", "\n\nmkdir b", "前导空段不得使整体判只读"},
		{"abs-path-read-then-write", "/bin/cat a\ntee out.txt", "段2 tee 可写 ⇒ 非白名单"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if AllSegmentsReadOnly(tc.cmd) {
				t.Errorf("W-025①：%q 换行后是**两条命令**（%s），不得判「全段只读」", tc.cmd, tc.why)
			}
		})
	}
}

// 对照组（防「过度拦截」）：换行两侧**都**是只读白名单成员 ⇒ 仍须判 true。
// 没有这一组，一个「把带换行的命令一律判否」的退化实现也能让上面全绿 —— 那就是假绿。
func TestW025_NewlineSeparator_TrueCases(t *testing.T) {
	cases := []struct{ name, cmd string }{
		{"two-readonly-lines", "ls -la\npwd"},
		{"crlf-two-readonly", "cat a\r\nhead -3"},
		{"three-readonly-lines", "pwd\nls -la\nwhoami"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if !AllSegmentsReadOnly(tc.cmd) {
				t.Errorf("W-025①（对照）：%q 两段都是只读白名单成员，应判 true", tc.cmd)
			}
		})
	}
}

// 纯书写差异的防御**不得回退**：`Normalize` 仍须折叠行内空白。
// （否则换行修好、多空格被放进来 ⇒ 拆东墙补西墙。）
func TestW025_WhitespacePaddingDefense_StillHolds(t *testing.T) {
	if got := Normalize("rm  -rf\t\t/"); got != "rm -rf /" {
		t.Errorf("W-025①：行内空白仍须折叠为单空格，got %q", got)
	}
	if AllSegmentsReadOnly("ls  -la") != true {
		t.Errorf("W-025①：`ls  -la`（双空格）仍须判只读")
	}
	if _, ok := MatchDangerous("rm  -rf  /"); !ok {
		t.Errorf("W-025①：`rm  -rf  /`（多空格）仍须命中危险档")
	}
}

// 同源面：换行分隔必须在**所有**「按命令位判定」的入口上一致成立。
// 只修 AllSegmentsReadOnly 一处 = 半修（其余入口照样被洗白）。
func TestW025_NewlineSeparator_SharedByCommandPositionEntrypoints(t *testing.T) {
	// ① 命令段原语（CommandRegions 是导出的命令位原语，sandbox deny 面共用）
	regions := CommandRegions("cat a\nmkdir b")
	if len(regions) < 2 {
		t.Errorf("W-025①：CommandRegions(%q) 应切出 ≥2 段，got %d 段：%q",
			"cat a\\nmkdir b", len(regions), regions)
	}
	for _, r := range regions {
		if strings.Contains(r, "\n") {
			t.Errorf("W-025①：段内不得残留换行：%q", r)
		}
	}

	// ② 风险分级·破坏性命令位（chmod ∈ riskOnlyCommandNames）
	if !MatchRiskDestructive("echo hi\nchmod 777 x") {
		t.Errorf("W-025①：第二段命令位是 chmod，MatchRiskDestructive 须命中（修前因换行折叠而漏判）")
	}

	// ③ 危险档·短语规则的命令位门控（# 注释行曾把段首词顶成 `#` ⇒ 整串漏判）
	if _, ok := MatchDangerous("# clean up\nrm -rf /tmp/x"); !ok {
		t.Errorf("W-025①：第二段命令位是 rm，危险档须命中（修前段首词是 `#` ⇒ 漏判）")
	}

	// ④ 只读档（MatchReadOnly 是「任一段只读即只读」，与 AllSegmentsReadOnly 方向相反，
	// 但共用同一份段切分）——此处只钉「第二段确实成段」这一结构事实。
	if !MatchReadOnly("cat a\npwd") {
		t.Errorf("W-025①：MatchReadOnly 应能在任一段命中只读成员")
	}
}

// 反向自检（判据本身要有判别力）：把换行换成空格（= 修前的归一化语义），
// 下面这几条**必须**给出与被测相反的结果 —— 证明上面几条钉的正是「换行是不是分隔符」这一维。
func TestW025_DiscriminatorIsSensitiveToNewlineOnly(t *testing.T) {
	// 换行版：两条命令（非只读）
	if AllSegmentsReadOnly(strings.ReplaceAll("cat a\nmkdir b", "\n", " ")) {
		// 空格版被判只读（因为合并成一条命令）—— 这不是我们想要的结论，
		// 但它反证了「换行 vs 空格」这一维确实改变判定结果。
		t.Logf("对照：空格版 `cat a mkdir b` 判只读 = true（说明该维敏感 ✔）")
	}
	// 危险档同理：换行版命中、空格版段首词是 cat ⇒ 不命中
	if _, ok := MatchDangerous("cat a rm -rf /tmp/x"); ok {
		t.Errorf("W-025①：空格版 `cat a rm -rf /tmp/x` 段首词是 cat，不应命中危险档（对照失效）")
	}
}
