package tools

// B2（S19-H-B）· 「skill 的 allowed-tools 不得写共享 Policy」的守卫。
//
// 移交单：内部工作回执
// 决策项：B2（§2.2 族B · I8 外部代码 ⊥ 权限 / I10 不自提权）
//
// ============================================================================
// ⚠️ 为什么这里**没有**一个"行为面判别器"（实测结论，不是偷懒）
// ============================================================================
// 被删的机制是「覆盖 + defer 恢复」：
//
//	origTools := pol.GetAllowedTools() // 用户原值，如 ["bash"]
//	pol.SetAllowedTools(result.AllowedTools) // skill 声明的清单
//	defer pol.SetAllowedTools(origTools) // 函数返回时恢复
//
// defer 在 skillHandler **返回前**执行 ⇒ 调用方拿到控制权时 Policy **必然已是原值**。
// 因此票面验收判据①（"Execute 后断言 GetAllowedTools 仍是 ['read']"）**在 before 与 after
// 两态下都是绿的** —— 它不构成判别性，实测见 GT-B2-p1 的文件头与本轮回执。
//
// 而且窗口内**没有任何工具执行**（B2 拍板依据④：该注入"defer 窗口内无工具执行 ⇒ 实际无效"）
// ⇒ 单线程下窗口内不存在任何可观测点；唯一可能的观测者是"共享同一 Policy 的其它 goroutine"，
// 那是时序相关的非确定观测，不能作为守卫。**故判别手段只能是结构面**（GT-B2-p2）。
// ============================================================================

import (
	"context"
	"go/ast"
	"go/parser"
	"go/token"
	"path/filepath"
	"strings"
	"testing"

	"github.com/anchorandfish/whale-code/internal/permission"
	"github.com/anchorandfish/whale-code/internal/skill"
	"github.com/anchorandfish/whale-code/internal/testutil"
)

// gtB2PolicyAfterSkillCall 走生产链路执行一个声明了 allowed-tools 的 skill，
// 返回调用结束后共享 Policy 的免问表。
//
// 夹具必须用 **user 来源**：B1 接线后 project 来源会在 SkillMgr.ExecuteWithMeta 就早退
// （信任门），根本到不了 B2 那段代码 —— 那样 p1 会变成"测了个空"。
func gtB2PolicyAfterSkillCall(t *testing.T, allowedTools []string) []string {
	t.Helper()
	home := testutil.IsolatedHomeDir(t)
	t.Setenv("HOME", home)
	ws := t.TempDir()

	s19WriteFile(t,
		filepath.Join(home, ".whale", "skills", "gtwriter.md"),
		s19SkillMarkdown("gtwriter", "do the thing", allowedTools))

	e := gtExecutorAtProductionEntry(t, ws)
	if s, ok := e.SkillMgr.Get("gtwriter"); !ok || s.Source != "user" {
		t.Fatalf("前提失效：夹具 skill 未按 user 来源加载（ok=%v）", ok)
	}

	pol := permission.NewPolicy(permission.LevelWorkspaceWrite)
	pol.SetAllowedTools([]string{"bash"}) // 模拟用户配置里的允许清单（免问表）
	e.InitPipelineWithHooks(pol, ws)

	// 直接调生产 handler（skillHandler 就是 B2 的落点）。
	h := e.skillHandler()
	out, err := h(context.Background(), []byte(`{"name":"gtwriter"}`))
	if err != nil {
		t.Fatalf("skillHandler 返回 error: %v", err)
	}
	if !strings.Contains(out, "do the thing") {
		t.Fatalf("前提失效：skill 正文未返回（%q）—— 说明没走到 B2 那段代码，本条会变成空测", out)
	}
	return pol.GetAllowedTools()
}

// GT-B2-p1 · 调用结束后，共享 Policy 的免问表**不得**被 skill 声明改写。
//
// ⚠️ **本条不具判别性**（before 与 after 均绿，原因见文件头）—— 保留它是为了防住
// 「有人把注入加回来且**不带** defer 恢复」这种更糟的形态；对本次删除动作它不提供判别力。
// 真正的判别器是 GT-B2-p2。
func TestGT_B2_P1_SharedPolicyUntouchedAfterSkillCall(t *testing.T) {
	got := gtB2PolicyAfterSkillCall(t, []string{"write_file"})
	if len(got) != 1 || got[0] != "bash" {
		t.Fatalf("共享 Policy 的免问表被 skill 声明改写：want [bash]，got %v。"+
			"（注意：本条两态均绿 ⇒ 单凭它不能证明注入已被删除）", got)
	}
}

// GT-B2-p2 · **判别器（结构面）**：生产文件 web.go 的 skillHandler 函数体内不得存在
// SetAllowedTools 调用 —— 即"外部内容不再能写权限层"。
//
// 判别性：把 web.go 里那段注入块放回去 ⇒ 本条打红。用 AST 而非正则，避免注释/字符串误命中。
// 防恒绿：先断言 skillHandler 确实被解析到（否则改名/删函数会让守卫静默失真）。
func TestGT_B2_P2_NoSkillDrivenAllowedToolsOverwriteInProduction(t *testing.T) {
	const srcFile = "web.go"

	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, srcFile, nil, 0)
	if err != nil {
		t.Fatalf("解析 %s 失败: %v", srcFile, err)
	}

	var (
		found bool
		hits  []string
	)
	for _, decl := range f.Decls {
		fd, ok := decl.(*ast.FuncDecl)
		if !ok || fd.Name == nil || fd.Name.Name != "skillHandler" || fd.Body == nil {
			continue
		}
		found = true
		ast.Inspect(fd.Body, func(n ast.Node) bool {
			call, ok := n.(*ast.CallExpr)
			if !ok {
				return true
			}
			sel, ok := call.Fun.(*ast.SelectorExpr)
			if !ok {
				return true
			}
			if sel.Sel != nil && sel.Sel.Name == "SetAllowedTools" {
				hits = append(hits, fset.Position(call.Pos()).String())
			}
			return true
		})
	}
	if !found {
		t.Fatalf("守卫失真：%s 里找不到 skillHandler —— 函数被改名/删除，本守卫必须同步更新", srcFile)
	}
	if len(hits) > 0 {
		t.Fatalf("B2 回归：skillHandler 内又出现 SetAllowedTools 调用（外部内容写入权限层）: %v\n"+
			"B2 拍板：SetAllowedTools 是「免问表」（permission.go:609 auto-approve），skill 正文与"+
			"该元数据同源同文件 ⇒ 用它承载「能力收窄」是错层，且外部内容不得驱动权限变化（I8/I10）。",
			hits)
	}
}

// GT-B2-p3 · 范围守卫：B2 只删「注入行为」，**不删** SkillResult.AllowedTools 字段。
//
// 该字段是公开结构、tui 的 OnSkillExecuteWithMeta 在用；删字段会波及界外（O-U 边界）。
// 本条为编译期断言（引用即证明字段仍存在），防止实现时"顺手删字段"越界。
func TestGT_B2_P3_SkillResultAllowedToolsFieldStillDeclared(t *testing.T) {
	r := skill.SkillResult{AllowedTools: []string{"read_file"}}
	if strings.Join(r.AllowedTools, ",") != "read_file" {
		t.Fatalf("SkillResult.AllowedTools 字段语义变了：%v", r.AllowedTools)
	}
}
