// decision_reason_bilingual_guard_test.go — 双语文案守卫。
//
// 钉两件事（用户 2026-10-13 拍板）：
//
//	① 用户可见的 `Decision.Reason` 走**双语形态**（英文主行 + 3 空格缩进中文辅行）。
//
// 它只进「给人看」的出口：审批弹窗 / 降级告警。
//
//	② `Decision.Err` **保持英文单侧**（不含汉字）—— 它会随工具结果进**模型上下文**，
//
// 给它加中文辅行 = 改变模型读到的内容。
//
// # 判别性（本文件必须能被打红）
//
// - 把 `Reason` 改回纯中文单侧（如 `"危险命令分级"`）⇒ 用例①红（缺英文主行 + 形式违规）；
// - 给任一 `Err` 追加中文辅行 ⇒ 用例②红。
//
// # 判据独立性（沿用 `internal/tui/bilingual_guard_test.go` 的分工）
//
// 「什么算汉字」用形态 SSOT（`util.HasHan`，字符集同源）；
// 「含汉字的行必须缩进」的**扫描逻辑本地独立写出**（不调 `util.AssertChineseOnlyOnIndentedLines`）——
// 判据若与实现同源，实现写错时判据会跟着错（共享 bug ⇒ 假绿）。
// 期望值用**字面量**（`reasonIndent`）而非 `util.CopyIndent`：守卫不该由被守卫的常量拼出来。
package security

import (
	"strings"
	"testing"

	"github.com/anchorandfish/whale-code/internal/permission"
	"github.com/anchorandfish/whale-code/internal/util"
)

// reasonIndent 是中文辅助行的字面缩进（= 形态 SSOT `util.CopyIndent`）。
// ★ 故意写字面量：用 `util.CopyIndent` 拼期望值会让"缩进被改坏"时守卫跟着一起变（同源假绿）。
const reasonIndent = "   "

// firstHanIdx 返回首个汉字的字节下标（-1 = 不含汉字）。
func firstHanIdx(s string) int {
	for i, r := range s {
		if r >= 0x4e00 && r <= 0x9fff {
			return i
		}
	}
	return -1
}

// reasonFormViolation 扫描 out：**含汉字的行必须以 `reasonIndent` 开头**（禁止行内并列）。
// 返回第一条违规行（合规则空串）。扫描逻辑本地独立（见文件头「判据独立性」）。
func reasonFormViolation(out string) string {
	for _, line := range strings.Split(out, "\n") {
		if !util.HasHan(line) {
			continue
		}
		if !strings.HasPrefix(line, reasonIndent) {
			return line
		}
	}
	return ""
}

// TestW088_ReasonFormScanDiscriminates 是本文件的**判别性自证**：
// 扫描器必须能区分「行内并列 / 纯中文单侧」（违规）与「英文主行 + 缩进中文」（合规）。
// 若这条绿但上面两个用例红，说明扫描器太松（判据失效）。
func TestW088_ReasonFormScanDiscriminates(t *testing.T) {
	bad := []string{
		"危险命令分级",                        // 纯中文单侧：没有英文主行
		"dangerous command tier 危险命令分级", // 行内并列
	}
	for _, b := range bad {
		if reasonFormViolation(b) == "" {
			t.Errorf("应有违规但扫描器放行（判据太松）：%q", b)
		}
	}
	good := []string{
		"dangerous command tier\n" + reasonIndent + "危险命令分级",
		"pure english only", // 纯英文也是合规形态（反向保：不得要求中文必须存在）
	}
	for _, g := range good {
		if v := reasonFormViolation(g); v != "" {
			t.Errorf("不应有违规但扫描器误判：%q ⇒ 违规行 %q", g, v)
		}
	}
}

// TestW088_DecisionReasonBilingual_ErrEnglishOnly 用生产构造函数 `NewChecker` 走
// **真实的 CheckCommand**（不经任何 mock），验证 Reason 双语 / Err 英文单侧。
func TestW088_DecisionReasonBilingual_ErrEnglishOnly(t *testing.T) {
	// 前置自检（判别性依赖）：夹具命令必须真的落在我改过 Reason 的那三条分支上。
	if got := permissionAnalyze("chmod"); got != "destructive" {
		t.Fatalf("precondition 失效：`chmod` 应判破坏性档，实测 %q", got)
	}
	if got := permissionAnalyze("mkfs.notreal"); got != "dangerous" {
		t.Fatalf("precondition 失效：`mkfs.notreal` 应判危险档，实测 %q", got)
	}

	c := newTestChecker(t, false) // sandbox 关：让判定落到 permission(L3)（与 P-014 夹具同口径）

	cases := []struct {
		name       string
		cmd        string
		wantReason string
		wantErrSub string // Err 必须含有的英文主行子串；空 = 期望 Err 为 nil
	}{
		{
			name:       "空命令",
			cmd:        "",
			wantReason: "empty command\n" + reasonIndent + "空命令",
			wantErrSub: "empty command",
		},
		{
			name:       "危险档",
			cmd:        "mkfs.notreal",
			wantReason: "dangerous command tier\n" + reasonIndent + "危险命令分级",
			wantErrSub: "dangerous command:",
		},
		{
			name:       "破坏性档（Ask）",
			cmd:        "chmod",
			wantReason: "destructive command requires user confirmation\n" + reasonIndent + "破坏性命令需用户确认",
			wantErrSub: "", // Ask 不带 Err
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			d := c.CheckCommand(tc.cmd)

			// ① Reason 必须是**精确**的双语形态（英文主行 + 字面量缩进 + 中文辅行）。
			if d.Reason != tc.wantReason {
				t.Errorf("Reason 形态不符：\n got %q\nwant %q", d.Reason, tc.wantReason)
			}
			// 形式门：含汉字的行必须以缩进开头（防"英文主行里混了汉字"或"中文没缩进"）。
			if v := reasonFormViolation(d.Reason); v != "" {
				t.Errorf("Reason 含非缩进中文字（行内并列？）：%q", v)
			}

			// ② Err 英文单侧：不得含任何汉字，且保留英文主行。
			if tc.wantErrSub == "" {
				if d.Err != nil {
					t.Errorf("期望 Err 为 nil，实测 %v", d.Err)
				}
				return
			}
			if d.Err == nil {
				t.Fatalf("期望 Err 非 nil（含英文主行 %q）", tc.wantErrSub)
			}
			if util.HasHan(d.Err.Error()) {
				t.Errorf("Err 是**模型上下文**出口，不得含汉字：%q", d.Err.Error())
			}
			if !strings.Contains(d.Err.Error(), tc.wantErrSub) {
				t.Errorf("Err 缺英文主行 %q：%q", tc.wantErrSub, d.Err.Error())
			}
		})
	}
}

// permissionAnalyze 把权限档归一成短名，供前置自检比较
// （用生产函数 `permission.AnalyzeBashCommand`，不依赖散文字面量 —— R4）。
func permissionAnalyze(cmd string) string {
	switch permission.AnalyzeBashCommand(cmd) {
	case permission.BashRiskDangerous:
		return "dangerous"
	case permission.BashRiskDestructive:
		return "destructive"
	default:
		return "other"
	}
}
