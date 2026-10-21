// budget_token_only_test.go — 编排层预算：删「钱」这一维，判定只随 token 变。
//
// 背景（用户 2026-10-08 拍板选项 a =「保留节流、去掉钱语义」）：
// 编排器原先在派发前按"我们自己编的单价 × token = 美分"判断该不该继续派发，
// 而那单价没有真值来源（haiku=2¢/sonnet=8¢/opus=45¢ 是拍的），且默认预算 $1000 硬编码、
// `--budget` 旗标根本不存在 ⇒ 节流阀悬在一个假数上。违反「不算价钱」铁律（2026-10-05 拍板）。
//
// 本文件是**判别器**，三条判据（对应计划 §4 A-1 判据）：
// 1. 行为面：节流阀仍活着，且**只认 token**（派发路径 = ParallelScheduler.Dispatch）。
// 2. 判别器：token 用量改变 ⇒ 判定翻转；「成本」字段已被结构删除（编译期不可表达）⇒ 判定不受其影响。
// 3. 结构面（AST）：自定价符号在**非测试源码**里 0 命中；token 轴符号必须在盘。
//
// ★ 修前红：① 在旧口径下 CheckBeforeDispatch/CheckBudget 判的是 UsedCostCents（自编价），
// ③ 在旧源码里 staticCostByModel 等符号遍地 ⇒ 两条必红（见 handoff 回执的 before/after 记录）。
package orchestrator

import (
	"context"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/anchorandfish/whale-code/internal/tools"
)

// ---------------------------------------------------------------- 判据 ① 行为面

// TestW007_DispatchStillThrottlesOnTokens 节流阀仍活着，判据是 **token**。
// 生产入口 = ParallelScheduler.Dispatch（main.go 组装、编排派发走的那条）。
func TestW007_DispatchStillThrottlesOnTokens(t *testing.T) {
	t.Parallel()

	// (a) 大 token 上限：连派 2 个都放行
	t.Run("large-cap-allows-dispatch", func(t *testing.T) {
		workDir := t.TempDir()
		graph := NewDepGraph()
		addTestNodes(t, graph, []string{"W007-a1", "W007-a2"}, []string{BucketA, BucketB})
		budget := NewBudgetTracker(1_000_000) // 大上限
		scheduler := NewParallelScheduler(graph, makeTestWorktreePool(t, workDir, 3), budget, makeTestTraceStore(t), NewStateMachine("w007-large"), 3)
		agent := &mockAgent{}
		agent.executeFn = func(ctx context.Context, in tools.AgentInput) (tools.AgentOutput, error) {
			return tools.AgentOutput{Result: "ok", Status: tools.TaskCompleted,
				Usage: &tools.TokenUsage{InputTokens: 1000}}, nil
		}
		scheduler.SetAgent(agent)

		if err := scheduler.Dispatch(context.Background(), "W007-a1"); err != nil {
			t.Fatalf("大上限下第 1 次派发应成功：%v", err)
		}
		if err := scheduler.Dispatch(context.Background(), "W007-a2"); err != nil {
			t.Fatalf("大上限下第 2 次派发应成功：%v", err)
		}
		if budget.IsExceeded() {
			t.Error("大上限下不应超限")
		}
	})

	// (b) 小 token 上限：第 1 个记满用量后超限 ⇒ 第 2 个被预检拦下
	t.Run("small-cap-throttles", func(t *testing.T) {
		workDir := t.TempDir()
		graph := NewDepGraph()
		addTestNodes(t, graph, []string{"W007-b1", "W007-b2"}, []string{BucketA, BucketB})
		budget := NewBudgetTracker(5) // 上限 5 token
		scheduler := NewParallelScheduler(graph, makeTestWorktreePool(t, workDir, 3), budget, makeTestTraceStore(t), NewStateMachine("w007-small"), 3)
		agent := &mockAgent{}
		agent.executeFn = func(ctx context.Context, in tools.AgentInput) (tools.AgentOutput, error) {
			return tools.AgentOutput{Result: "ok", Status: tools.TaskCompleted,
				Usage: &tools.TokenUsage{InputTokens: 1000}}, nil // 1000 > 5
		}
		scheduler.SetAgent(agent)

		if err := scheduler.Dispatch(context.Background(), "W007-b1"); err != nil {
			t.Fatalf("第 1 次派发应成功（预检时未超限）：%v", err)
		}
		if !budget.IsExceeded() {
			t.Fatal("W-007①：记录 1000 token > 上限 5 后应标记 exceeded")
		}
		err := scheduler.Dispatch(context.Background(), "W007-b2")
		if err == nil {
			t.Fatal("W-007①：超限后第 2 次派发必须被拦（节流阀仍活着）")
		}
		if !strings.Contains(err.Error(), "budget already exceeded") {
			t.Errorf("拦截原因应指向预算超限，got: %v", err)
		}
		// 用量确实是 token（不是被折算过的"美分"）
		if got := budget.UsedTokens(); got != 1000 {
			t.Errorf("W-007①：已用量应为 provider 真报 token 1000，got=%d", got)
		}
	})
}

// ---------------------------------------------------------------- 判据 ② 判别器

// TestW007_TokenDoublingFlipsDecision 判定只随 token 变（同一 cap 下，用量变化 → 判定翻转）。
// 「成本」字段已从结构里删除 ⇒ **在类型层不可表达**，因此不存在"设一个成本值看判定动不动"这条路；
// 这条与判据 ③（结构面）互为闭合。
func TestW007_TokenDoublingFlipsDecision(t *testing.T) {
	b := NewBudgetTracker(1000)

	if err := b.RecordUsage(400); err != nil {
		t.Fatal(err)
	}
	if _, over80, over100 := b.CheckBudget(); over80 || over100 {
		t.Fatalf("W-007②：400/1000 不应触发任何阈值，got over80=%v over100=%v", over80, over100)
	}

	if err := b.RecordUsage(400); err != nil { // 800 → 80%
		t.Fatal(err)
	}
	if _, over80, over100 := b.CheckBudget(); !over80 || over100 {
		t.Fatalf("W-007②：800/1000 应刚好触发 80%% 而未超限，got over80=%v over100=%v", over80, over100)
	}

	if err := b.RecordUsage(400); err != nil { // 1200 → 120%
		t.Fatal(err)
	}
	if _, _, over100 := b.CheckBudget(); !over100 {
		t.Fatal("W-007②：1200/1000 应超限（用量翻倍 ⇒ 判定翻转）")
	}
	if !b.IsExceeded() {
		t.Error("W-007②：超限后 IsExceeded 应为 true")
	}
}

// ---------------------------------------------------------------- 判据 ③ 结构面（AST）

// TestW007_NoSelfPricedSymbolsInPackageSource 结构面门：
// 自定价符号在**非测试源码**里 0 命中；token 轴符号必须在盘。
//
// 用 go/parser 扫真实标识符与字符串字面量（**注释天然被忽略**）—— 比文本 grep 精确，
// 不会因为注释里提到旧名字而误报（本仓 shell grep 已有 8 次漏检/误判教训）。
func TestW007_NoSelfPricedSymbolsInPackageSource(t *testing.T) {
	// 必须消失的「钱」面符号（标识符）
	forbiddenIdents := []string{
		"staticCostByModel", "staticCostByModelName",
		"CostPer1kTokensCents", "computeCostFromUsage",
		"AvgCostCents", "CostCapCents",
		"TotalCostCents", "UsedCostCents", "ActualCostCents", "EstimatedCost",
		"ExceedReasonCost", "ExceedReasonBoth",
		"QueryAvgCost", "ReverseQueryCost", "aggregateCostFromMetrics",
		"profileEstimate", "staticEstimate",
		"RecordActualCost", "UsedCents", "TotalBudgetCents",
		"totalBudgetCents", "usedCents",
	}
	// 必须消失的「钱」面字符串字面量（JSON tag / 枚举值）
	forbiddenLits := []string{
		"cost_cap_cents", "total_cost_cents", "used_cost_cents",
		"actual_cost_cents", "estimated_cost", "cost_exceeded",
		"avg_cost_cents",
	}
	// token 轴符号必须在盘（防"两边都删干净了也算过"）
	requiredIdents := []string{
		"DefaultTotalTokensCap", "totalTokensCap", "usedTokens",
		"QueryAvgTokens", "ReverseQueryUsage", "totalTokensFromUsage",
		"staticTokensByModel", "staticTokensByModelName", "ExceedReasonTokens",
	}

	forbidden := make(map[string]bool, len(forbiddenIdents))
	for _, s := range forbiddenIdents {
		forbidden[s] = true
	}
	seen := make(map[string]bool)

	entries, err := os.ReadDir(".")
	if err != nil {
		t.Fatalf("ReadDir: %v", err)
	}
	scanned := 0
	for _, e := range entries {
		name := e.Name()
		if !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			continue
		}
		scanned++
		fset := token.NewFileSet()
		f, perr := parser.ParseFile(fset, filepath.Join(".", name), nil, 0)
		if perr != nil {
			t.Fatalf("parse %s: %v", name, perr)
		}
		ast.Inspect(f, func(n ast.Node) bool {
			switch x := n.(type) {
			case *ast.Ident:
				seen[x.Name] = true
				if forbidden[x.Name] {
					t.Errorf("W-007③：自定价符号 %q 仍在非测试源码 %s（应已删除）", x.Name, name)
				}
			case *ast.BasicLit:
				if x.Kind == token.STRING {
					// 用子串匹配：真实落点是 struct tag（如 `json:"total_cost_cents"`），
					// 其 x.Value 含内外引号，等值比较抓不到 ⇒ 必须 Contains。
					for _, l := range forbiddenLits {
						if strings.Contains(x.Value, l) {
							t.Errorf("W-007③：自定价字面量 %q 仍在非测试源码 %s（应已删除）", l, name)
						}
					}
				}
			}
			return true
		})
	}
	if scanned < 5 {
		t.Fatalf("W-007③：仅扫到 %d 个源文件，门输入不完整 ⇒ 拒绝出结论", scanned)
	}
	for _, r := range requiredIdents {
		if !seen[r] {
			t.Errorf("W-007③：token 轴符号 %q 不在盘（应为唯一权威）", r)
		}
	}
}

// TestW007_NoMoneyFieldsInStructs 结构面（reflect）：
// 预算/结果/契约结构体不得含任何「钱」字段（cost/cents 类）。
func TestW007_NoMoneyFieldsInStructs(t *testing.T) {
	types := map[string]reflect.Type{
		"BudgetTracker":   reflect.TypeOf((*BudgetTracker)(nil)).Elem(),
		"BudgetStateExt":  reflect.TypeOf((*BudgetStateExt)(nil)).Elem(),
		"BudgetHint":      reflect.TypeOf((*BudgetHint)(nil)).Elem(),
		"Estimate":        reflect.TypeOf((*Estimate)(nil)).Elem(),
		"BudgetResultExt": reflect.TypeOf((*BudgetResultExt)(nil)).Elem(),
		"TaskResult":      reflect.TypeOf((*TaskResult)(nil)).Elem(),
		"Pattern":         reflect.TypeOf((*Pattern)(nil)).Elem(),
	}
	for typeName, rt := range types {
		for i := 0; i < rt.NumField(); i++ {
			fname := strings.ToLower(rt.Field(i).Name)
			if strings.Contains(fname, "cost") || strings.Contains(fname, "cents") {
				t.Errorf("W-007④：%s.%s 仍是「钱」字段（应已删除）", typeName, rt.Field(i).Name)
			}
		}
	}
	// 契约守卫同向：BudgetHint 只能有 token 轴
	if !AssertBudgetHintTokensCapOnly() {
		t.Error("W-007④：BudgetHint 必须只含 TokensCap，不得含 cost/cents 字段")
	}
}
