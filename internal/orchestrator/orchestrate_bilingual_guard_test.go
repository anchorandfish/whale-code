package orchestrator

// b4_orchestrate_bilingual_guard_test.go — 批 4 守卫：
// 钉 `/orchestrate` 全家（start/status/list/resume/abort/clarify/plan/contract + usageHelp）
// 输出的**文案形态**（SSOT = internal/util/bilingual.go）。
//
// # 形态契约
//
//	English main line ← 主行：可独立读懂的英文，不得含汉字（调用方数据除外）
// Chinese auxiliary line ← 辅助行：以 util.CopyIndent 开头，汉字只允许出现在这里
//
// # 两层门（对齐双语技能 §4：别只写一层）
//
// ① **结构门（AST）**：orchestrate_command.go 里不得有「未包装的中文发射字面量」。
// 本文件的实现已**全量转入** `util.Bi` / `util.BiSame` / `(EN, ZH)` 字段对 /
// `(…EN, …ZH)` 变量对 ⇒ 合法残留只有三类，门按结构识别：
// a) 在 `util.Bi`/`util.BiSame` 实参子树内（批 2b/2c 同款判据）；
// b) 挂在名字以 `ZH` 结尾的字段/变量上（`DescriptionZH:` 复合字面量键、
// `…ZH` 赋值左值 —— 批 A `NoteZH` 同款「双语对生产者」形态）；
// c) 其余一律违规。
// ★ 判别力自证用**内存合成源码**喂纯函数（零磁盘写回，规避 ：
// overlay 对「运行期自读磁盘」判据物理无效；改真实文件违反探针纪律）。
// ★ 本文件是 tools 包 `b2cUnwrappedHanEmit` 的**跨包副本**（+ ZH 半边识别扩展）；
// 若第三个包也需要，应把判据**下沉到 util 叶子包**（：三处重复 = 双实现漂移温床）。
// ② **行为门**：经真实 `OrchestrateCmd.Handle` 入口驱动的**真实输出**，
// 含汉字的行必须以 util.CopyIndent 开头。数据用英文（"buy milk"），
// 把"我们自己的 copy"从调用方数据里隔离出来判（技能 §4：别把形态门施加到数据上）。

import (
	"context"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"strings"
	"testing"
	"unicode"

	"github.com/anchorandfish/whale-code/internal/util"
)

// === ① 结构门：纯函数（吃 src []byte，测试内存内可合成反例自证） ===

func b4HasHan(s string) bool {
	for _, r := range s {
		if unicode.Is(unicode.Han, r) {
			return true
		}
	}
	return false
}

// b4AllowedByZHName 判定一个 Han 字面量是否挂在「名字以 ZH 结尾」的字段/变量上
// （双语对的 zh 半边 —— 批 A NoteZH 先例）。返回合法字面量位置集合。
func b4AllowedByZHName(f *ast.File) map[token.Pos]bool {
	allowed := map[token.Pos]bool{}
	// 复合字面量键：DescriptionZH: "…" / NoteZH: "…"
	ast.Inspect(f, func(n ast.Node) bool {
		kv, ok := n.(*ast.KeyValueExpr)
		if !ok {
			return true
		}
		if id, ok := kv.Key.(*ast.Ident); ok && strings.HasSuffix(id.Name, "ZH") {
			ast.Inspect(kv.Value, func(m ast.Node) bool {
				if lit, ok := m.(*ast.BasicLit); ok {
					allowed[lit.Pos()] = true
				}
				return true
			})
		}
		return true
	})
	// 赋值左值：degradeReasonZH = "…" / x.NoteZH = "…" / degradeReasonEN, degradeReasonZH = "en", "zh"（平行赋值按位对应）
	ast.Inspect(f, func(n ast.Node) bool {
		assign, ok := n.(*ast.AssignStmt)
		if !ok {
			return true
		}
		for i, lhs := range assign.Lhs {
			name := ""
			switch e := lhs.(type) {
			case *ast.Ident:
				name = e.Name
			case *ast.SelectorExpr:
				name = e.Sel.Name
			}
			if !strings.HasSuffix(name, "ZH") || i >= len(assign.Rhs) {
				continue
			}
			ast.Inspect(assign.Rhs[i], func(m ast.Node) bool {
				if lit, ok := m.(*ast.BasicLit); ok {
					allowed[lit.Pos()] = true
				}
				return true
			})
		}
		return true
	})
	return allowed
}

// b4UnwrappedHanEmit 返回 src 中「未包装的中文发射字面量」清单（空 = 合规）。
// 结构门：在 util.Bi/BiSame 实参子树内、或挂在 *ZH 名字上 = 合法；其余违规。
func b4UnwrappedHanEmit(src []byte) []string {
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, "snippet.go", src, 0)
	if err != nil {
		return []string{fmt.Sprintf("parse error: %v", err)}
	}
	allowed := map[token.Pos]bool{}
	ast.Inspect(f, func(n ast.Node) bool {
		call, ok := n.(*ast.CallExpr)
		if !ok {
			return true
		}
		sel, ok := call.Fun.(*ast.SelectorExpr)
		if !ok || (sel.Sel.Name != "Bi" && sel.Sel.Name != "BiSame") {
			return true
		}
		for _, arg := range call.Args {
			ast.Inspect(arg, func(m ast.Node) bool {
				if lit, ok := m.(*ast.BasicLit); ok {
					allowed[lit.Pos()] = true
				}
				return true
			})
		}
		return true
	})
	for pos := range b4AllowedByZHName(f) {
		allowed[pos] = true
	}
	var violations []string
	ast.Inspect(f, func(n ast.Node) bool {
		lit, ok := n.(*ast.BasicLit)
		if !ok || lit.Kind != token.STRING || !b4HasHan(lit.Value) {
			return true
		}
		if allowed[lit.Pos()] {
			return true
		}
		violations = append(violations, lit.Value)
		return true
	})
	return violations
}

// TestW083B4_StructGateIsDiscriminating 结构门判别性自证（内存合成源码，零磁盘写回）：
// 违规发射必被抓；Bi 包装 / ZH 半边（字段对、变量对）不误判；EN 半边挂中文仍必被抓。
// 不红 = 门是假的。
func TestW083B4_StructGateIsDiscriminating(t *testing.T) {
	header := "package orchestrator\n\nimport \"github.com/anchorandfish/whale-code/internal/util\"\n"

	// ① 违规：裸发射中文（Errorf 与普通赋值两个形态）必被抓全。
	bad := header + "\nfunc f() error { return fmt.Errorf(\"未包装的中文\") }\n\nvar _ = \"另一处裸中文\"\n"
	if got := b4UnwrappedHanEmit([]byte(bad)); len(got) != 2 {
		t.Fatalf("结构门失效：裸中文发射未被抓全（got %d，want 2）：%v", len(got), got)
	}
	// ② 合法：util.Bi 实参内的中文不误判。
	good := header + "\nvar _ = util.Bi(\"ok\", \"中文辅行\")\n"
	if got := b4UnwrappedHanEmit([]byte(good)); len(got) != 0 {
		t.Fatalf("结构门误判 util.Bi 实参：%v", got)
	}
	// ③ 合法：ZH 字段半边（DescriptionZH 同款）不误判。
	pair := header + "\ntype s struct{ Description, DescriptionZH string }\n\nvar _ = s{Description: \"en\", DescriptionZH: \"中文\"}\n"
	if got := b4UnwrappedHanEmit([]byte(pair)); len(got) != 0 {
		t.Fatalf("结构门误判 ZH 字段半边：%v", got)
	}
	// ④ 合法：ZH 变量半边（degradeReasonZH 同款，仓内约定后缀为大写 ZH）不误判。
	varPair := header + "\nfunc g() {\n\tdegradeReasonZH := \"中文\"\n\t_ = degradeReasonZH\n}\n"
	if got := b4UnwrappedHanEmit([]byte(varPair)); len(got) != 0 {
		t.Fatalf("结构门误判 ZH 变量半边：%v", got)
	}
	// ⑤ 严格性：en 半边变量挂中文仍必被抓（否则识别过松）。
	enSide := header + "\nfunc h() {\n\ten := \"中文在英文半边\"\n\t_ = en\n}\n"
	if got := b4UnwrappedHanEmit([]byte(enSide)); len(got) != 1 {
		t.Fatalf("结构门过松：EN 半边挂中文未被抓：%v", got)
	}
	// ⑥ 合法：平行赋值（degradeReasonEN, degradeReasonZH = "en", "zh" 同款）按位豁免 zh 半边。
	para := header + "\nfunc p() {\n\tdegradeReasonEN, degradeReasonZH := \"english\", \"中文\"\n\t_, _ = degradeReasonEN, degradeReasonZH\n}\n"
	if got := b4UnwrappedHanEmit([]byte(para)); len(got) != 0 {
		t.Fatalf("结构门误判平行赋值 ZH 半边：%v", got)
	}
	// ⑦ 严格性：平行赋值的 en 半边挂中文仍必被抓（按位对应不能放过 en 位）。
	paraBad := header + "\nfunc q() {\n\tdegradeReasonEN, degradeReasonZH := \"中文占 en 位\", \"中文\"\n\t_, _ = degradeReasonEN, degradeReasonZH\n}\n"
	if got := b4UnwrappedHanEmit([]byte(paraBad)); len(got) != 1 {
		t.Fatalf("结构门过松：平行赋值 EN 半边挂中文未被抓：%v", got)
	}
}

// TestW083B4_OrchestrateCommandNoUnwrappedHanEmit 结构门：生产文件本体必合规。
// ★ 注意 CWD：本门运行期自读磁盘，必须经 `go test ./internal/orchestrator/` 执行
// （它把 CWD 设为包目录）；`go test -c` 后在仓库根裸跑会扫错文件（探针伪影，技能 §7）。
func TestW083B4_OrchestrateCommandNoUnwrappedHanEmit(t *testing.T) {
	src, err := os.ReadFile("orchestrate_command.go")
	if err != nil {
		t.Fatalf("读取失败：%v", err)
	}
	if got := b4UnwrappedHanEmit(src); len(got) != 0 {
		t.Fatalf("orchestrate_command.go 存在未包装的中文发射字面量（应改 util.Bi / (EN,ZH) 对）：%v", got)
	}
}

// === ② 行为门：真实入口的真实输出，汉字只许出现在缩进行 ===

func firstB4FormViolation(out string) string {
	for _, line := range strings.Split(out, "\n") {
		if !util.HasHan(line) {
			continue
		}
		if !strings.HasPrefix(line, util.CopyIndent) {
			return line
		}
	}
	return ""
}

// b4assert 断言输出形态合规。
func b4assert(t *testing.T, name, out string) {
	t.Helper()
	if v := firstB4FormViolation(out); v != "" {
		t.Errorf("%s：中文出现在非缩进行（行内并列？）⇒ %q\n输出：\n%s", name, v, out)
	}
}

// TestW083B4_OrchestrateOutputs_ChineseOnlyOnIndentedLines 钉 /orchestrate 全子命令
// （TUI 与 plain 共用本实现，无双实现 ⇒ 钉一处覆盖两路）。
// 需求/任务 ID 等数据用英文，隔离出我们自己的 copy。
// 空参分支会同时返回业务 error（调用方据 error 决定展示方式）—— 这里只判 string 形态。
func TestW083B4_OrchestrateOutputs_ChineseOnlyOnIndentedLines(t *testing.T) {
	orch, _ := makeTestOrchestrator(t)
	cmd := NewOrchestrateCmd(orch)
	ctx := context.Background()

	// 用法帮助（空参 / 未知子命令）——含 DescriptionZH 辅行渲染。
	out, _ := cmd.Handle(ctx, "")
	b4assert(t, "usage(empty)", out)
	out, _ = cmd.Handle(ctx, "bogus")
	b4assert(t, "usage(bogus)", out)

	// start：空参用法分支 + 成功创建（英文数据）。
	out, _ = cmd.HandleStart(ctx, "")
	b4assert(t, "start(empty)", out)
	out, err := cmd.HandleStart(ctx, "buy milk")
	if err != nil {
		t.Fatalf("start 应成功：%v", err)
	}
	b4assert(t, "start(ok)", out)

	// start 之后：status / list 非空分支。
	out, err = cmd.HandleStatus(ctx, "")
	if err != nil {
		t.Fatalf("status 应成功：%v", err)
	}
	b4assert(t, "status(all)", out)
	out, err = cmd.HandleList(ctx)
	if err != nil {
		t.Fatalf("list 应成功：%v", err)
	}
	b4assert(t, "list", out)

	// 不存在任务 / 空参 / 各 stage 不匹配分支。
	out, _ = cmd.HandleStatus(ctx, "T-nope")
	b4assert(t, "status(missing)", out)
	out, _ = cmd.HandleResume(ctx, "T-nope")
	b4assert(t, "resume(missing)", out)
	out, _ = cmd.HandleResume(ctx, "")
	b4assert(t, "resume(empty)", out)
	out, _ = cmd.HandleAbort(ctx, "T-nope")
	b4assert(t, "abort(missing)", out)
	out, _ = cmd.HandleAbort(ctx, "")
	b4assert(t, "abort(empty)", out)
	out, _ = cmd.HandleClarify(ctx, "T-nope")
	b4assert(t, "clarify(missing)", out)
	out, _ = cmd.HandlePlan(ctx, "T-nope")
	b4assert(t, "plan(missing)", out)
	out, _ = cmd.HandleContract(ctx, "T-nope")
	b4assert(t, "contract(missing)", out)
}

// TestW083B4_UsageHelpRendersZHFieldAsIndentedAux 钉 usageHelp 的 DescriptionZH 渲染形态：
// 英文主行 + 缩进中文辅行 + Usage 行双语；旧行为（纯中文 Description 直接当主行）不得复现。
func TestW083B4_UsageHelpRendersZHFieldAsIndentedAux(t *testing.T) {
	orch, _ := makeTestOrchestrator(t)
	cmd := NewOrchestrateCmd(orch)
	out := cmd.usageHelp()

	if !strings.Contains(out, "Subcommands:\n") {
		t.Errorf("usageHelp 应含英文主行 Subcommands:，实际：\n%s", out)
	}
	// 每个 ZH 描述都必须以 CopyIndent 开头出现在辅行上（辅行形态 = 前有 \n + CopyIndent）。
	for _, spec := range cmd.Commands() {
		if spec.DescriptionZH == "" {
			t.Errorf("子命令 %s 缺 DescriptionZH（批 4 应成对填写）", spec.Name)
			continue
		}
		want := "\n" + util.CopyIndent + spec.DescriptionZH + "\n"
		if !strings.Contains(out, want) {
			t.Errorf("usageHelp 未按辅行形态渲染 %s 的中文描述（want %q）：\n%s", spec.Name, want, out)
		}
	}
	// 英文主行可独立读懂（弱代理：主题词存在即可，不钉词形 —— 技能 §7「判据钉词形」坑）。
	for _, topic := range []string{"Start an orchestration task", "Show orchestration task status", "List all orchestration tasks"} {
		if !strings.Contains(out, topic) {
			t.Errorf("usageHelp 主行缺英文描述 %q：\n%s", topic, out)
		}
	}
}
