package tools

// ─────────────────────────────────────────────────────────────────────────────
// G6 守卫：钉「调用方给的 `tools` 清单**不参与**子 agent 的工具面」。
//
// 背景（G5 + G6 · 2026-09-15）：
// `AgentInput.Tools` 曾在兄弟任务上与 `Policy.AllowedTools()` 求交集、收窄工具面。
// 该机制已被 **L2移除**（并发不安全 / 语义错位 / 对只读零效果，
// 见 agent_task.go:291-299），但**四处声明面仍在宣称它生效**：
// ① 模型可见的 Task Spec（tools.go）+ task_tool.go 的 taskToolPrompt/Schema
// ② agent.go 的字段说明（称「附加收窄——取交集」）
// ③ goal/eval.go:68 的注释（把「只读」归因于该字段）
// ④ 无任何测试钉住「它不参与」
// ⇒ 模型/调用方**误信「收窄已生效」**；若据此放宽 AgentType 即**静默超授**
// （当前有效面仍 fail-closed，故是方向性隐患而非当前可利用缺陷）。
// G5 对齐了 ② 的一半，G6 对齐 ①②，③ 经 G-T-001 出单并在 2026-09-15 越界直改落盘
// （用户授权：internal/goal 不在三热包内）；
// G7进一步把 ① 的**载体**整体降级 —— task_tool.go 的 16 个 Tool 面方法
// （含 Prompt/Schema）成死码，已删除，P2 随之升级为「只暴露 Call」的结构断言。
//
// 本文件守卫各钉一个**可回归的面**：
// P1 真实 Registry 里 Task 的 Spec（= 模型真正看到的定义）不再广告 tools
// P2 TaskTool 已降级为执行体：只暴露 Call，不再是 tools.Tool 实现（G7 结构断言）
// P3 能力计算所依赖的 AgentContext **没有 Tools 槽位**（类型层面就不可能有）
// P4 internal/tools 生产码对 AgentInput.Tools **零读取**（扫描器自带判别性自检）
// P5 getAllowedToolSet() 的字面公式 = roleToolSet ∘ intersectToolSet，没有第三项
//
// 判别性：P1/P3 各自有 before 态（`-overlay` 反向重建）可打红 —— 见 G6_STATUS.md；
// P2/P4 是**读盘型**判别器（`-overlay` 只在编译期生效，对它们物理无效，见技能 R4-8）
// ⇒ 用**写回 + 无条件还原 + md5 校验**证明可判别；P4 另有合成片段自检；
// P5 由 P5b 反例 + 公式字面比对证明。
// ─────────────────────────────────────────────────────────────────────────────

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"testing"
)

// ── P1：模型真正看到的定义（Registry 的 Task Spec）不再广告 tools ──────────────

// TestG6_P1_LiveTaskSpecDoesNotAdvertiseTools 钉住**模型可见**的 Task 工具定义。
//
// 为什么扫 Registry 而不是 task_tool.go：实测模型可见定义来自 Registry 的 Spec 表
// （tools.go registerBuiltinTools → registry.Get → AgentContext.buildAgentTools,
// agent.go:1272；主会话侧 tui 走 registry.AllDirect()）。task_tool.go 的
// Prompt()/Schema() **零调用点** ⇒ 只改那边对模型无效，是假修复。
func TestG6_P1_LiveTaskSpecDoesNotAdvertiseTools(t *testing.T) {
	// 生产入口：与 tui / executor 构造 Registry 用的是同一个构造函数。
	reg := NewRegistry()
	spec, ok := reg.Get("Task")
	if !ok {
		t.Fatal("P1 前提失效：Registry 里没有 Task 工具（改名了？）—— 本守卫失去断言对象")
	}

	// P1-a：描述里不得再宣称「可以指定子 agent 能用哪些工具」
	for _, bad := range []string{"subset of tools", "allowed to use", "tool names"} {
		if strings.Contains(spec.Description, bad) {
			t.Errorf("P1 回归：Task 的 Description 仍在广告 tools 参数（命中 %q）\n  desc=%q",
				bad, spec.Description)
		}
	}

	// P1-b：InputSchema.properties 不得含 tools
	props, ok := spec.InputSchema["properties"].(map[string]interface{})
	if !ok || props == nil {
		t.Fatalf("P1 前提失效：Task 的 InputSchema.properties 不是 map（结构变了？）：%#v",
			spec.InputSchema)
	}
	if _, has := props["tools"]; has {
		t.Error("P1 回归：Task 的 InputSchema 又暴露了 tools 参数 —— " +
			"模型会以为能靠它收窄子 agent 工具面（机制早已被 L2 移除）⇒ 危险假信号")
	}

	// P1-c：反向守卫 —— 别把必需参数一起撤掉（防止「撤过了头」）
	if _, has := props["prompt"]; !has {
		t.Error("P1：Task 的 InputSchema 丢了 prompt 参数（撤 tools 时误删）")
	}
	reqOK := false
	reqList, _ := spec.InputSchema["required"].([]string)
	for _, r := range reqList {
		if r == "prompt" {
			reqOK = true
		}
	}
	if !reqOK {
		t.Errorf("P1：Task 的 required 不再含 prompt：%#v", spec.InputSchema["required"])
	}
}

// ── P2：TaskTool 已降级为执行体（G7）────────────────────────────────────────

// TestG6_P2_TaskToolExposesOnlyCall 钉住 G7 的降级结果：
// task_tool.go 里的 TaskTool **只暴露 Call 一个方法**，不再是 tools.Tool 实现。
//
// 历史（升级说明）：
//
//	本组原为「TaskTool.Prompt()/Schema() 不含 tools」（G6）。G7把那 16 个
//	**零调用点**的 Tool 面方法整体删除后，原断言对象已不存在，故升级为**更强的结构断言**：
//	不是「没广告 tools」，而是「**根本没有 Tool 面**」。
//	若有人把 Name/Prompt/Schema/CheckPermissions/… 加回来（= 重建「双真相源」的第一步，
//	下一个读代码的人会再次以为模型看到的是 task_tool.go），本守卫立刻红。
//
// 判别性：本守卫自带**合成片段自检** —— 扫描器若认不出「TaskTool 有 Prompt() 方法」，
// 就判本守卫不可判别（恒绿），而不是静默通过。
func TestG6_P2_TaskToolExposesOnlyCall(t *testing.T) {
	// ── 自检（判别性）：扫描器必须能认出 Tool 面方法 ──
	synthetic := []byte("package tools\n\n" +
		"func (t *TaskTool) Prompt() string { return \"x\" }\n" +
		"func (t *TaskTool) Call(ctx context.Context, in json.RawMessage) (any, error) { return nil, nil }\n")
	if got := g6TaskToolMethods(synthetic); !got["Prompt"] {
		t.Fatal("P2 自检失败：扫描器认不出 TaskTool.Prompt ⇒ 本守卫不可判别（恒绿）")
	} else if !got["Call"] {
		t.Fatal("P2 自检失败：扫描器认不出 TaskTool.Call")
	}

	// ── 真值：磁盘上的 task_tool.go（读盘型判别器，故不能用 -overlay 造 before）──
	dir, err := os.Getwd()
	if err != nil {
		t.Fatalf("Getwd: %v", err)
	}
	src, err := os.ReadFile(filepath.Join(dir, "task_tool.go"))
	if err != nil {
		t.Fatalf("P2 前提失效：读不到 task_tool.go（%v）—— 文件改名了？", err)
	}
	got := g6TaskToolMethods(src)
	if len(got) == 0 {
		t.Fatal("P2 自检失败：一个方法都没扫到（解析失败 / 接收者类型改名）—— 结论无意义")
	}
	if !got["Call"] {
		t.Error("P2 撤过头：TaskTool.Call 不见了 —— 执行入口被误删，Task 工具将无法执行")
	}
	for name := range got {
		if name != "Call" {
			t.Errorf("P2 回归：TaskTool 又长回了 Tool 面方法 %s —— "+
				"G7 的降级被撤销，「读 task_tool.go 会以为它才是模型可见定义」的陷阱重新出现"+
				"（真正的定义在 tools.go 的 Task Spec，见 P1）", name)
		}
	}
}

// g6TaskToolMethods 返回源码里绑定到 `*TaskTool`（或 `TaskTool`）的方法名集合。
// 纯语法解析，故可用于合成片段自检。
func g6TaskToolMethods(src []byte) map[string]bool {
	out := map[string]bool{}
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, "task_tool.go", src, 0)
	if err != nil {
		return out
	}
	for _, d := range f.Decls {
		fn, ok := d.(*ast.FuncDecl)
		if !ok || fn.Recv == nil || len(fn.Recv.List) == 0 {
			continue
		}
		recv := fn.Recv.List[0].Type
		if star, ok := recv.(*ast.StarExpr); ok {
			recv = star.X
		}
		if id, ok := recv.(*ast.Ident); ok && id.Name == "TaskTool" {
			out[fn.Name.Name] = true
		}
	}
	return out
}

// ── P3：能力计算的输入结构里根本没有 Tools 槽位 ──────────────────────────────

// TestG6_P3_AgentContextHasNoToolsSlot 用反射钉住结构性事实：
// 工具面唯一由 AgentContext 上的 AgentType ∩ ParentToolSet 决定，
// 而 AgentContext **没有** Tools 字段、NewAgentContext **不收**工具清单形参 ——
// 即 Spawn 路径上「把调用方清单喂进能力计算」在类型层面就不成立。
//
// 判别性：给 AgentContext 加一个 Tools 字段（= 重新引入该机制的第一步）本守卫立刻红。
func TestG6_P3_AgentContextHasNoToolsSlot(t *testing.T) {
	ctxType := reflect.TypeOf(AgentContext{})

	if f, ok := ctxType.FieldByName("Tools"); ok {
		t.Errorf("P3 回归：AgentContext 出现了 Tools 字段（%s %s）—— "+
			"能力计算多了个输入，getAllowedToolSet 很可能已把调用方清单算进去", f.Name, f.Type)
	}

	// 正面：唯一两个能力输入必须在（本守卫断言的是「只有它们」，不是「可以删输入」）
	for _, name := range []string{"AgentType", "ParentToolSet"} {
		if _, ok := ctxType.FieldByName(name); !ok {
			t.Errorf("P3 前提失效：AgentContext 丢了能力输入字段 %s", name)
		}
	}

	// 构造入口不得出现 []string 形参（工具清单就是 []string ⇒ 不该有这条通道）
	nt := reflect.TypeOf(NewAgentContext)
	for i := 0; i < nt.NumIn(); i++ {
		if nt.In(i).Kind() == reflect.Slice {
			t.Errorf("P3 回归：NewAgentContext 第 %d 个形参是 %s —— "+
				"出现了把工具清单喂进能力计算的通道", i, nt.In(i))
		}
	}

	// 兼容面必须仍在：AgentInput.Tools 被删会让旧调用方编译不过（orchestrator/goal）
	if _, ok := reflect.TypeOf(AgentInput{}).FieldByName("Tools"); !ok {
		t.Error("P3 回归：AgentInput.Tools 被删了 —— 兼容面破坏（旧调用方会编译失败）。" +
			"G5/G6 的处置是「保留但明示不参与判定」，不是删除")
	}
}

// ── P4：internal/tools 生产码对 AgentInput.Tools 零读取 ─────────────────────

// TestG6_P4_NoProductionReaderOfAgentInputTools 用 AST 扫本包生产码，
// 钉住「AgentInput.Tools 是只写不读的兼容字段」。
//
// 与 P3 的分工：P3 钉**结构**（没有槽位），P4 钉**代码**（即便留了字段也没人读）。
// 若有人绕过 AgentContext 直接在 Spawn 路径上读 input.Tools，只有 P4 会红。
func TestG6_P4_NoProductionReaderOfAgentInputTools(t *testing.T) {
	// ── 自检 1（判别性）：扫描器**必须能认出**一次读取，否则本守卫是恒绿的假守卫 ──
	syntheticRead := map[string][]byte{
		"synthetic_read.go": []byte(
			"package tools\n\nfunc g6Syn(input AgentInput) int { return len(input.Tools) }\n"),
	}
	if got := g6ScanAgentInputToolsReads(syntheticRead); len(got) == 0 {
		t.Fatal("P4 自检失败：扫描器认不出 `input.Tools` 的读取 ⇒ 本守卫不可判别（恒绿）")
	}

	// ── 自检 2（假阳性对照）：字段**搬运**不算读取，否则守卫会假红、进而被人放宽规则绕过 ──
	syntheticWrite := map[string][]byte{
		"synthetic_write.go": []byte(
			"package tools\n\nfunc g6Syn(p taskToolParams) AgentInput {\n" +
				"\treturn AgentInput{Prompt: p.Prompt, Tools: p.Tools}\n}\n"),
	}
	if got := g6ScanAgentInputToolsReads(syntheticWrite); len(got) != 0 {
		t.Errorf("P4 自检失败：把字段搬运误判成读取（%v）⇒ 会产生假红", got)
	}

	// ── 真值：本包生产码（排除 _test.go）零读取 ──
	dir, err := os.Getwd()
	if err != nil {
		t.Fatalf("Getwd: %v", err)
	}
	srcs, err := g6ReadPackageSources(dir)
	if err != nil {
		t.Fatalf("读 internal/tools 源码失败: %v", err)
	}
	// 守住守卫：确认真的扫到了目标文件，而不是扫了个空集（空集恒绿）
	for _, must := range []string{"agent.go", "agent_task.go", "task_tool.go", "tools.go"} {
		if len(srcs[must]) == 0 {
			t.Fatalf("P4 自检失败：未扫到 %s（dir=%s）—— 守卫扫了个空集，结论无意义", must, dir)
		}
	}
	if hits := g6ScanAgentInputToolsReads(srcs); len(hits) > 0 {
		t.Errorf("P4 回归：生产码开始**读取** AgentInput.Tools —— "+
			"工具面可能又把调用方清单算进去了：\n  %s", strings.Join(hits, "\n  "))
	}
}

// g6ScanAgentInputToolsReads 扫描源码集合，返回**读取** AgentInput.Tools 的位置。
//
// 判定规则（类型感知，避免误伤同名不同类的字段，如 executor.go 的 plugin.Tools）：
// 1. 收集本文件里绑定到 AgentInput（含 *AgentInput / []AgentInput）的标识符名 ——
// 来源：形参（含接口方法形参）、`var x AgentInput`、`x := AgentInput{...}`；
// 2. 对 `X.Tools` 且 X 属于该名字集合的 SelectorExpr，
// 若它是**字段搬运**（AgentInput{Tools: X.Tools} 的取值位，或 `X.Tools = ...` 的赋值位）
// 则放行；否则判为读取。
func g6ScanAgentInputToolsReads(srcs map[string][]byte) []string {
	var hits []string
	for name, src := range srcs {
		fset := token.NewFileSet()
		f, err := parser.ParseFile(fset, name, src, 0)
		if err != nil {
			hits = append(hits, fmt.Sprintf("%s: PARSE ERROR: %v", name, err))
			continue
		}
		aiNames := g6AgentInputBoundNames(f)
		transfers := g6AgentInputTransferPositions(f)

		ast.Inspect(f, func(n ast.Node) bool {
			se, ok := n.(*ast.SelectorExpr)
			if !ok || se.Sel == nil || se.Sel.Name != "Tools" {
				return true
			}
			id, ok := se.X.(*ast.Ident)
			if !ok || !aiNames[id.Name] {
				return true // 接收者不是 AgentInput 值 ⇒ 不属本不变量
			}
			if transfers[se.Pos()] {
				return true // 字段搬运，放行
			}
			p := fset.Position(se.Pos())
			hits = append(hits, fmt.Sprintf("%s:%d:%d", p.Filename, p.Line, p.Column))
			return true
		})
	}
	sort.Strings(hits)
	return hits
}

// g6AgentInputBoundNames 返回文件内绑定到 AgentInput 类型的标识符名集合。
func g6AgentInputBoundNames(f *ast.File) map[string]bool {
	names := map[string]bool{}
	ast.Inspect(f, func(n ast.Node) bool {
		switch d := n.(type) {
		case *ast.FuncType: // 含函数/方法/接口方法形参
			if d.Params != nil {
				for _, fld := range d.Params.List {
					if !g6IsAgentInputType(fld.Type) {
						continue
					}
					for _, nm := range fld.Names {
						names[nm.Name] = true
					}
				}
			}
		case *ast.ValueSpec:
			if g6IsAgentInputType(d.Type) {
				for _, nm := range d.Names {
					names[nm.Name] = true
				}
			}
		case *ast.AssignStmt:
			if d.Tok != token.DEFINE {
				return true
			}
			for i, rhs := range d.Rhs {
				if i >= len(d.Lhs) || !g6IsAgentInputComposite(rhs) {
					continue
				}
				if id, ok := d.Lhs[i].(*ast.Ident); ok {
					names[id.Name] = true
				}
			}
		}
		return true
	})
	return names
}

// g6AgentInputTransferPositions 返回「字段搬运」表达式的位置集合。
func g6AgentInputTransferPositions(f *ast.File) map[token.Pos]bool {
	out := map[token.Pos]bool{}
	ast.Inspect(f, func(n ast.Node) bool {
		switch v := n.(type) {
		case *ast.CompositeLit:
			if !g6IsAgentInputType(v.Type) {
				return true
			}
			for _, elt := range v.Elts {
				kv, ok := elt.(*ast.KeyValueExpr)
				if !ok {
					continue
				}
				if id, ok := kv.Key.(*ast.Ident); ok && id.Name == "Tools" {
					out[kv.Value.Pos()] = true
				}
			}
		case *ast.AssignStmt:
			for _, lhs := range v.Lhs {
				if se, ok := lhs.(*ast.SelectorExpr); ok && se.Sel != nil && se.Sel.Name == "Tools" {
					out[se.Pos()] = true
				}
			}
		}
		return true
	})
	return out
}

// g6IsAgentInputType 报告类型表达式是否为 AgentInput / *AgentInput / []AgentInput(/别名限定名)。
func g6IsAgentInputType(t ast.Expr) bool {
	switch v := t.(type) {
	case *ast.Ident:
		return v.Name == "AgentInput"
	case *ast.StarExpr:
		return g6IsAgentInputType(v.X)
	case *ast.ArrayType:
		return g6IsAgentInputType(v.Elt)
	case *ast.SelectorExpr:
		return v.Sel != nil && v.Sel.Name == "AgentInput"
	}
	return false
}

// g6IsAgentInputComposite 报告表达式是否为 AgentInput{...} 复合字面量。
func g6IsAgentInputComposite(e ast.Expr) bool {
	cl, ok := e.(*ast.CompositeLit)
	if !ok {
		return false
	}
	return g6IsAgentInputType(cl.Type)
}

// g6ReadPackageSources 读包目录下的生产 .go（排除 _test.go）。
func g6ReadPackageSources(dir string) (map[string][]byte, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, err
	}
	out := map[string][]byte{}
	for _, e := range entries {
		n := e.Name()
		if e.IsDir() || !strings.HasSuffix(n, ".go") || strings.HasSuffix(n, "_test.go") {
			continue
		}
		b, err := os.ReadFile(filepath.Join(dir, n))
		if err != nil {
			return nil, err
		}
		out[n] = b
	}
	return out, nil
}

// ── P5：工具面公式字面钉死（没有第三项）───────────────────────────────────

// TestG6_P5_ToolFaceIsRoleIntersectParent 钉住 getAllowedToolSet() 的**字面公式**：
// 结果必须恒等于 intersectToolSet(roleToolSet(AgentType), ParentToolSet)。
// 任何「多加一项」（例如把调用方清单算进去）都会让某些组合不再相等。
func TestG6_P5_ToolFaceIsRoleIntersectParent(t *testing.T) {
	cases := []struct {
		role   AgentType
		parent map[string]bool
	}{
		{AgentTypeCoder, nil},
		{AgentTypeExplore, nil},
		{AgentTypeVerification, nil},
		{AgentTypeCoder, map[string]bool{"bash": true, "read_file": true}},
		{AgentTypeExplore, map[string]bool{"bash": true, "write_file": true}},
		{AgentTypeVerification, map[string]bool{"write_file": true}},
		{AgentTypeVerification, map[string]bool{}},
		{AgentTypeFork, map[string]bool{"bash": true}},
	}
	for _, c := range cases {
		a := NewAgentContext(nil, "probe", "", ".", 1)
		a.AgentType = c.role
		a.ParentToolSet = c.parent

		got := a.getAllowedToolSet()
		want := intersectToolSet(roleToolSet(c.role), c.parent)
		if !sameStringSet(got, want) {
			t.Errorf("P5 回归：getAllowedToolSet() ≠ roleToolSet(%s) ∩ ParentToolSet —— 公式里多了第三项？\n"+
				"   got =%v\n  want =%v", c.role, g6SortedKeys(got), g6SortedKeys(want))
		}
	}

	// P5b 反例：只读角色即便父面含写工具，也不得拿到写工具（角色面必须生效）
	ro := NewAgentContext(nil, "probe", "", ".", 1)
	ro.AgentType = AgentTypeExplore
	ro.ParentToolSet = map[string]bool{"write_file": true, "bash": true}
	if s := ro.getAllowedToolSet(); s["write_file"] {
		t.Error("P5b：只读角色 explore 在父面含 write_file 时拿到了 write_file —— 角色面未生效")
	}
}

func g6SortedKeys(m map[string]bool) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}
