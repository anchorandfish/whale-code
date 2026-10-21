// b6_command_bilingual_guard_test.go — 批 6 守卫（command 包 handler 输出双语化）。
//
// 覆盖面（批 6 文件）：
// - fast_handler.go / output_style_handler.go / migrate_handler.go：无注册描述的文件 ⇒ **强门**
// （全部 Han 字面量必须在 util.Bi/BiSame 实参子树内，或位于豁免函数体内）。
// - commands_config/core/git/github/notify/permission.go、command.go：含注册描述（受
// TestCommandDescriptions_AllChinese 约束、有意中文）⇒ **发射点门**（只盯 return 语句
// 结果里的 Han 字面量；注册描述在复合字面量里，天然不在管辖内）。
//
// 豁免函数：OutputStyleDescription —— 模型侧 system prompt（G14 拍板中文；：模型输入不改造）。
//
// 形态 SSOT = internal/util/bilingual.go。判别自证用内存合成源码（零磁盘写回，见双语技能 §7）。
// 门强度差异（strong / emit）按文件结构选择 —— 与 B2c/B2d 同理，强度差异必须在此写明。
package command

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"strings"
	"testing"

	"github.com/anchorandfish/whale-code/internal/util"
)

var b6FilesStrong = []string{
	"fast_handler.go",
	"output_style_handler.go",
	"migrate_handler.go",
}

var b6FilesEmit = []string{
	"commands_config.go",
	"commands_core.go",
	"commands_git.go",
	"commands_github.go",
	"commands_notify.go",
	"commands_permission.go",
	"command.go",
}

var b6ExemptFuncs = map[string]bool{
	"OutputStyleDescription": true,
}

// b6AllowedZHLhs 判断标识符是否 ZH 半边（zh 变量 / *ZH 后缀字段）。
func b6AllowedZHLhs(name string) bool {
	if name == "zh" {
		return true
	}
	return strings.HasSuffix(name, "ZH")
}

// b6CollectAllowed 收集「已包装」字面量位置：util.Bi/BiSame 实参子树 + ZH 名赋值右值。
func b6CollectAllowed(f *ast.File) map[token.Pos]bool {
	allowed := map[token.Pos]bool{}
	allowExpr := func(e ast.Expr) {
		ast.Inspect(e, func(m ast.Node) bool {
			if lit, ok := m.(*ast.BasicLit); ok {
				allowed[lit.Pos()] = true
			}
			return true
		})
	}
	ast.Inspect(f, func(n ast.Node) bool {
		switch v := n.(type) {
		case *ast.CallExpr:
			if sel, ok := v.Fun.(*ast.SelectorExpr); ok && (sel.Sel.Name == "Bi" || sel.Sel.Name == "BiSame") {
				for _, a := range v.Args {
					allowExpr(a)
				}
			}
		case *ast.AssignStmt:
			for i, lhs := range v.Lhs {
				if id, ok := lhs.(*ast.Ident); ok && b6AllowedZHLhs(id.Name) && i < len(v.Rhs) {
					allowExpr(v.Rhs[i])
				}
			}
		}
		return true
	})
	return allowed
}

// b6ExemptLines 收集豁免函数体覆盖的行号集合。
func b6ExemptLines(fset *token.FileSet, f *ast.File) map[int]bool {
	lines := map[int]bool{}
	ast.Inspect(f, func(n ast.Node) bool {
		if fd, ok := n.(*ast.FuncDecl); ok && b6ExemptFuncs[fd.Name.Name] && fd.Body != nil {
			for l := fset.Position(fd.Pos()).Line; l <= fset.Position(fd.End()).Line; l++ {
				lines[l] = true
			}
		}
		return true
	})
	return lines
}

// b6Scan 判据主体（纯函数，吃 src）：mode = "strong"（全部 Han 字面量）| "emit"（仅 return 结果）。
func b6Scan(src []byte, mode string) []string {
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, "snippet.go", src, 0)
	if err != nil {
		return []string{"parse error: " + err.Error()}
	}
	allowed := b6CollectAllowed(f)
	exempt := b6ExemptLines(fset, f)
	var violations []string
	check := func(lit *ast.BasicLit) {
		if lit.Kind != token.STRING || !util.HasHan(lit.Value) {
			return
		}
		if allowed[lit.Pos()] {
			return
		}
		if exempt[fset.Position(lit.Pos()).Line] {
			return
		}
		pos := fset.Position(lit.Pos())
		violations = append(violations, fmt.Sprintf("snippet.go:%d 未包装 Han 字面量 %s", pos.Line, lit.Value))
	}
	ast.Inspect(f, func(n ast.Node) bool {
		switch v := n.(type) {
		case *ast.BasicLit:
			if mode == "strong" {
				check(v)
			}
		case *ast.ReturnStmt:
			for _, r := range v.Results {
				ast.Inspect(r, func(m ast.Node) bool {
					// 边界：不下钻复合字面量。注册函数整体是 `return []Command{...}`，
					// 其中的 Description 字段受 TestCommandDescriptions_AllChinese 约束（有意中文），
					// 不属发射点门管辖；这些 handler 的运行时文本发射都是裸串 return / Sprintf，
					// 不经过复合字面量。
					if _, ok := m.(*ast.CompositeLit); ok {
						return false
					}
					if lit, ok := m.(*ast.BasicLit); ok {
						check(lit)
					}
					return true
				})
			}
		}
		return true
	})
	return violations
}

// TestW083B6_StrongFilesNoUnwrappedHan 强门：批 6 无注册描述文件的全部 Han 字面量必须已包装。
func TestW083B6_StrongFilesNoUnwrappedHan(t *testing.T) {
	for _, name := range b6FilesStrong {
		src, err := os.ReadFile(name)
		if err != nil {
			t.Fatalf("读取 %s 失败（本守卫以包目录为 CWD 运行）：%v", name, err)
		}
		if vs := b6Scan(src, "strong"); len(vs) > 0 {
			t.Errorf("%s 存在未包装的中文发射点（应走 util.Bi/BiSame，模型侧/system prompt 面以豁免函数登记）：\n%s",
				name, strings.Join(vs, "\n"))
		}
	}
}

// TestW083B6_EmitFilesReturnsWrapped 发射点门：含注册描述文件的 return 结果必须已包装。
func TestW083B6_EmitFilesReturnsWrapped(t *testing.T) {
	for _, name := range b6FilesEmit {
		src, err := os.ReadFile(name)
		if err != nil {
			t.Fatalf("读取 %s 失败：%v", name, err)
		}
		if vs := b6Scan(src, "emit"); len(vs) > 0 {
			t.Errorf("%s 的 return 结果存在未包装的中文（应走 util.Bi；注册 Description 字段不在管辖内）：\n%s",
				name, strings.Join(vs, "\n"))
		}
	}
}

// TestW083B6_GateDiscriminating 判别自证（内存合成源码，零磁盘写回）：
// 违规必被抓、合规与豁免不误判。不红 = 门是假的。
func TestW083B6_GateDiscriminating(t *testing.T) {
	cases := []struct {
		name    string
		mode    string
		src     string
		wantRed bool
	}{
		{
			name:    "strong：裸 return 中文必抓",
			mode:    "strong",
			src:     "package p\nfunc f() string { return \"中文未包装\" }\n",
			wantRed: true,
		},
		{
			name:    "strong：Bi 包装的实参不误判",
			mode:    "strong",
			src:     "package p\nfunc f() string { return wrap.Bi(\"en main\", \"zh 辅行\") }\n",
			wantRed: false,
		},
		{
			name:    "strong：豁免函数体内的中文不误判",
			mode:    "strong",
			src:     "package p\nfunc OutputStyleDescription(s string) string { return \"模型侧中文\" }\n",
			wantRed: false,
		},
		{
			name:    "emit：return 里的裸中文必抓",
			mode:    "emit",
			src:     "package p\nfunc f() string { return \"中文未包装\" }\n",
			wantRed: true,
		},
		{
			name:    "emit：复合字面量里的注册描述（非 return）不误判",
			mode:    "emit",
			src:     "package p\nvar cs = []C{{Name: \"x\", Description: \"中文描述\"}}\n",
			wantRed: false,
		},
		{
			name:    "emit：return 整张注册表（复合字面量）不下钻，注册描述不误判",
			mode:    "emit",
			src:     "package p\nfunc cs() []C { return []C{{Name: \"x\", Description: \"中文描述\"}} }\n",
			wantRed: false,
		},
		{
			name:    "emit：return 复合字面量之外的裸中文仍必抓",
			mode:    "emit",
			src:     "package p\nfunc cs() []C { return []C{{Name: \"x\", Description: \"中文描述\"}} }\nfunc f() string { return \"中文未包装\" }\n",
			wantRed: true,
		},
		{
			name:    "emit：豁免函数的 return 中文不误判",
			mode:    "emit",
			src:     "package p\nfunc OutputStyleDescription(s string) string { return \"模型侧中文\" }\n",
			wantRed: false,
		},
		{
			name:    "emit：Bi 包装的 return 不误判",
			mode:    "emit",
			src:     "package p\nfunc f() string { return wrap.Bi(\"en\", \"zh\") }\n",
			wantRed: false,
		},
	}
	for _, tc := range cases {
		vs := b6Scan([]byte(tc.src), tc.mode)
		if tc.wantRed && len(vs) == 0 {
			t.Errorf("%s：判据没牙（违规未被识别）", tc.name)
		}
		if !tc.wantRed && len(vs) > 0 {
			t.Errorf("%s：误判合规源码：%v", tc.name, vs)
		}
	}
}

// TestW083B6_FastOutputsBilingualForm 行为门：/fast 三种输出的形态。
// 注意：/fast 在 TUI 被 isTUICommandHandledLocally 短路 ⇒ 本断言钉的是 plain/bridge/CLI 入口可见形态。
func TestW083B6_FastOutputsBilingualForm(t *testing.T) {
	r := NewRegistry()
	cmd, ok := r.Get("fast")
	if !ok {
		t.Fatal("fast command not found")
	}
	for _, args := range []string{"status", "on", "off"} {
		out, err := cmd.Handler(args)
		if err != nil {
			t.Fatalf("/fast %s returned error: %v", args, err)
		}
		if v := util.AssertChineseOnlyOnIndentedLines(out); v != "" {
			t.Errorf("/fast %s 输出违反「英文主行 + 缩进中文辅行」形态，违规行：%q\n输出：\n%s", args, v, out)
		}
		if !strings.Contains(out, "Fast mode") {
			t.Errorf("/fast %s 主行必须是可独立读懂的英文（含 Fast mode），got:\n%s", args, out)
		}
		if !strings.Contains(out, "快速模式") {
			t.Errorf("/fast %s 中文辅助行缺失（不是删文案，是改形态），got:\n%s", args, out)
		}
	}
}

// TestW083B6_OutputStyleFormAndErrorID 行为门：/output-style handler 输出形态 + [config_invalid] 前缀必须保持在串首
// （WrapOutputStyleError 用 HasPrefix 解析 ErrorID，前缀被中文辅行挤走 = 静默破坏 §3.9 错误通道）。
func TestW083B6_OutputStyleFormAndErrorID(t *testing.T) {
	h := NewOutputStyleCommandHandler(nil)

	status := h("")
	if v := util.AssertChineseOnlyOnIndentedLines(status); v != "" {
		t.Errorf("status 输出形态违规：%q\n输出：\n%s", v, status)
	}
	if !strings.Contains(status, "Current output style") || !strings.Contains(status, "当前输出风格") {
		t.Errorf("status 输出缺英文主行或中文辅行：\n%s", status)
	}

	invalid := h("bogus")
	if !strings.HasPrefix(invalid, "[config_invalid]") {
		t.Errorf("[config_invalid] 前缀必须保持在串首（§3.9 ErrorID 通道），got:\n%s", invalid)
	}
	if v := util.AssertChineseOnlyOnIndentedLines(invalid); v != "" {
		t.Errorf("invalid 输出形态违规：%q\n输出：\n%s", v, invalid)
	}
	if _, err := WrapOutputStyleError(invalid); err == nil {
		t.Error("invalid 输出应被 WrapOutputStyleError 识别为错误")
	}

	switched := h("concise")
	if v := util.AssertChineseOnlyOnIndentedLines(switched); v != "" {
		t.Errorf("switched 输出形态违规：%q\n输出：\n%s", v, switched)
	}
	if !strings.Contains(switched, "Output style switched") || !strings.Contains(switched, "输出风格已切换为") {
		t.Errorf("switched 输出缺英文主行或中文辅行：\n%s", switched)
	}
}

// TestW083B6_UnwiredFallbackNoticesBilingual 行为门：未接线 fallback 提示的形态
// （：未接线提示只陈述未接线；双语形态照守）。TUI 短路面见各命令注释。
func TestW083B6_UnwiredFallbackNoticesBilingual(t *testing.T) {
	r := NewRegistry()
	cases := []struct {
		name   string
		enFrag string
		zhFrag string
	}{
		{"pr list", "GitHub integration is not initialized", "GitHub 集成未初始化"},
		{"notify list", "Notification system is not initialized", "通知系统未初始化"},
		{"worktree add", "/worktree add is only available", "/worktree add 仅在交互式"},
		{"permissions", "/permissions is only available in interactive TUI mode", "/permissions 仅在交互式 TUI 模式下可用"},
	}
	for _, tc := range cases {
		cmd, ok := r.Get(tc.name)
		if !ok {
			t.Fatalf("/%s not found", tc.name)
		}
		out, err := cmd.Handler("")
		if err != nil {
			t.Fatalf("/%s returned error: %v", tc.name, err)
		}
		if v := util.AssertChineseOnlyOnIndentedLines(out); v != "" {
			t.Errorf("/%s 输出形态违规：%q\n输出：\n%s", tc.name, v, out)
		}
		if !strings.Contains(out, tc.enFrag) {
			t.Errorf("/%s 缺英文主行（%q）：\n%s", tc.name, tc.enFrag, out)
		}
		if !strings.Contains(out, tc.zhFrag) {
			t.Errorf("/%s 缺中文辅行（%q）：\n%s", tc.name, tc.zhFrag, out)
		}
	}
}
