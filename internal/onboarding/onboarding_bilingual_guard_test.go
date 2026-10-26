package onboarding

// 批 5 守卫：onboarding 向导输出双语形态（英文主行 + 缩进中文辅行）。
//
// 覆盖面：
// - renderDifferentiatedWelcome（首屏 box）：box 行结构完整 + 含汉字行必以 CopyIndent 开头
// + 英文主行显示宽度不超 box 内容区（防 padToWidth「超宽原样返回」爆 box）
// - renderStepProgress：双语两行，英文主行无汉字、中文辅行缩进
//
// 形态判定 SSOT = util.AssertChineseOnlyOnIndentedLines（不各写一套 hasHan）。

import (
	"strings"
	"testing"

	"github.com/anchorandfish/whale-code/internal/util"
)

// TestW083B5_WelcomeBox_BilingualForm 钉首屏 box 的双语形态与行宽。
func TestW083B5_WelcomeBox_BilingualForm(t *testing.T) {
	o := &Onboarding{} // 渲染函数不依赖字段（可在零值 receiver 上调用，见 fail-open 注释）
	out := o.renderDifferentiatedWelcome()
	if out == "" {
		t.Fatal("welcome box 渲染为空（panic 被 fail-open 吞了？看 render.go recover）")
	}

	lines := strings.Split(out, "\n")
	if len(lines) < 10 {
		t.Fatalf("box 行数异常（双语化后应比单语更多），实得 %d 行", len(lines))
	}
	for i, line := range lines {
		// box 边框行（╔═╗ ╠═╣ ╚═╝）跳过内容判定。
		if strings.HasPrefix(line, "╔") || strings.HasPrefix(line, "╠") || strings.HasPrefix(line, "╚") {
			continue
		}
		// ① box 结构：内容行必须以 ║ 开头并以 ║ 结尾。
		if !strings.HasPrefix(line, "║") || !strings.HasSuffix(line, "║") {
			t.Errorf("box 第 %d 行破坏了 ║…║ 结构：%q", i+1, line)
			continue
		}
		// 判定单位 = 内容区（剥掉 ║ 前后缀）。box 是容器语法，
		// 形态铁律施加在容器内的内容上（同「表头行」先例的建模方式）。
		content := strings.TrimSuffix(strings.TrimPrefix(line, "║"), "║")
		// ② 形态铁律：内容区含汉字的行必须以 CopyIndent 开头（box 内辅行同样缩进）。
		if v := util.AssertChineseOnlyOnIndentedLines(content); v != "" {
			t.Errorf("box 第 %d 行中文出现在非缩进行：%q", i+1, line)
		}
		// ③ 宽度：内容区显示宽不得超 welcomeBoxWidth（否则 padToWidth 超宽
		// 原样返回，box 右边 ║ 被挤出对齐）。
		if w := displayWidth(content); w > welcomeBoxWidth {
			t.Errorf("box 第 %d 行超宽（%d > %d）：%q", i+1, w, welcomeBoxWidth, line)
		}
	}

	// ④ 关键内容仍在（原 render_test 的 Contains 断言依赖中文辅行保留）。
	// W-121：沙箱卖点按平台分支，期望值取自同一 SSOT，不写死某一平台
	// （否则门会把"Linux 上说 Windows-native"这个错误主张钉成合规）。
	_, sandboxZH := sandboxClaims()
	for _, want := range []string{"本地优先", "H12 自我进化", sandboxZH} {
		if !strings.Contains(out, want) {
			t.Errorf("box 应保留中文辅行内容 %q（render_test.go 的 Contains 断言依赖它）", want)
		}
	}
	// ⑤ 英文主行也在（双语化不是删中文，是加英文主行）。
	if !strings.Contains(out, "Local-first") {
		t.Error("box 应含英文主行 Local-first（双语形态）")
	}
}

// TestW083B5_StepProgress_BilingualForm 钉步骤进度行的双语形态。
func TestW083B5_StepProgress_BilingualForm(t *testing.T) {
	out := renderStepProgress(1, "Environment Check", "环境检测")
	if v := util.AssertChineseOnlyOnIndentedLines(out); v != "" {
		t.Fatalf("步骤进度行中文出现在非缩进行：%q", v)
	}
	if !strings.Contains(out, "Step 1/4: Environment Check") {
		t.Errorf("应含英文主行，实际：\n%s", out)
	}
	if !strings.Contains(out, util.CopyIndent+"━━━ 步骤 1/4：环境检测 ━━━") {
		t.Errorf("应含缩进中文辅行，实际：\n%s", out)
	}
}

// TestW083B5_FormGateIsDiscriminating 证明门的判别力：旧形态必红。
func TestW083B5_FormGateIsDiscriminating(t *testing.T) {
	// 旧形态（行内并列）必红。
	if util.AssertChineseOnlyOnIndentedLines("Step 1/4: 环境检测") == "" {
		t.Fatal("形态门失效：改造前的行内并列形态未被判红（判别力为零 ⇒ 门是假的）")
	}
	// 纯英文输出必须绿（反向保：不得要求所有行都有中文）。
	if v := util.AssertChineseOnlyOnIndentedLines("Step 1/4: Environment Check"); v != "" {
		t.Fatalf("形态门误红纯英文输出：%q", v)
	}
}
