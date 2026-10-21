// 不变量守卫（设计偏差审查 C1 · 移交单 A3）
//
// 被守卫的不变量：
//
//	PipelineResult.NeedConfirm == true ⟹ PipelineResult.Proceed == false
//
// 为什么需要它（根因，不是"某行写错"）：
//
//	ToolPipeline.Execute 的循环是按 **sr.NeedConfirm（stage 返回值）** 短路的
//	（pipeline.go:287），而不是按 **state.NeedConfirm（可变共享状态）**。
//	今天两者恰好同步 ⇒ 100% fail-closed；但这条同步关系**没有任何机制保证**。
//	只要某个 stage「置了 state.NeedConfirm = true，却没在 StageResult 里带
//	NeedConfirm」，短路就不命中，管道一路走到成功出口并返回
//	{Proceed:true, NeedConfirm:true} ⇒ 只看 Proceed 的入口
//	（executor.go:791 Execute / scheduler.go:283 ToolScheduler）会
//	**静默执行未经确认的调用**。
//
// ⚠️ 诚实标注（本仓硬要求，不得含糊带过）：
//
// - 本文件**不是"缺陷复现"**，是**防回归不变量守卫**。上面那种 stage 在
// 当前代码里**不存在**（穷举：全仓 state.NeedConfirm = true 的写入点共
// 3 个函数 4 个赋值，全部同时带 StageResult.NeedConfirm: true ⇒ :287 已短路）。
// - 因此不能说"修完后漏洞消失"，只能说"不变量被机械守卫"。
// - 三个用例的**判别性**（谁能把它打红）各不相同，见各自的头注释；
// 不具判别性的用例已显式标注，**不得**被当作判别器读。
//
// 判别性取证（详细命令与实测结果见本轮回执）：
//
//	A3-1 TestD002_NeedConfirmImpliesNotProceed —— **可判别**
//
// `-overlay` 回退 pipeline.go 的 守卫 ⇒ 本用例必红；
// 回退前后 `md5` 不同；不改磁盘源码。
//
//	A3-2 TestD002_NoNeedConfirmLiteralInProceedLiteral —— **可判别**
//
// ⚠️ 本用例**运行期自读磁盘**（go/parser + nil src + 相对路径）
// ⇒ 按本仓铁律 `-overlay` 对它**物理无效**，判别性只能靠
// 「受控写回 + 无条件还原 + md5 校验」证明（实测已做，见回执）。
//
//	A3-3 TestD002_NilConfirmChannel_Contract —— **对 A 组不具判别性**
//
// （A 落与不落它都绿）。它的判别对象是**被本单打回的 B 方案**：
// 把 B 写回（`OnConfirm == nil` ⇒ 终局拒绝）时它与既有
// `TestExecuteWithConfirm_NoOnConfirm` **同时变红**，这正是打回 B 的实测证据。
package tools

import (
	"context"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/anchorandfish/whale-code/internal/permission"
	"github.com/anchorandfish/whale-code/internal/testutil"
)

// d002ProbeStage 模拟「契约违反型 stage」：只置 **state.NeedConfirm**，
// 刻意**不**在 StageResult 里带 NeedConfirm。
//
// 它**不是**生产 stage，只在本文件的用例里被追加到管线末尾。存在的意义：
// 把「未来某个 stage 只走 state 通道」这一假设场景变成可执行断言
// —— 这是本仓方法论要求的手法（票面判据不可判别 ⇒ 换结构面/注入面），
// 而不是"为了让被测代码跑到而手工放宽守卫"：生产守卫（:287 短路、
// PermissionStage、 成功出口守卫）在这里**全部原样生效、一个都没动**。
type d002ProbeStage struct{}

func (s *d002ProbeStage) Name() string { return "d002_probe_state_only_needconfirm" }

func (s *d002ProbeStage) Execute(ctx context.Context, call Call, state *PipelineState) StageResult {
	state.NeedConfirm = true
	return StageResult{
		Proceed: true, // 刻意：只置 state，不动 StageResult.NeedConfirm
		Message: "probe: state.NeedConfirm set without StageResult.NeedConfirm",
	}
}

// d002Setup 以**生产装配**建管线（InitPipelineWithHooks，与 tui/lifecycle.go、
// tui/runner_core.go、tui/bridge.go 三条生产入口同一个函数），随后追加探针 stage。
//
// HOME 隔离到临时目录：InitPipelineWithHooks 会读 $HOME/.whale/hooks.json
// 与 hooks.trust.json（S7 信任门），若用户真机存在配置会让用例结果依赖环境。
// 返回的 readPath 是工作区内一个**真实存在**的文件，供 read_file 成功执行
// —— 这样"结果非 nil"才是真的执行产物，用例的 fail-open 断言才有意义。
func d002Setup(t *testing.T) (e *Executor, readPath string) {
	t.Helper()
	t.Setenv("HOME", testutil.IsolatedHomeDir(t))

	ws := t.TempDir()
	readPath = filepath.Join(ws, "d002_probe_target.txt")
	if err := os.WriteFile(readPath, []byte("d002-probe\n"), 0o644); err != nil {
		t.Fatalf("准备读取目标失败: %v", err)
	}

	e = NewExecutor(NewRegistry(), ws)
	// read_file 在 danger-full-access 下不需要权限层确认 ⇒ 用例里唯一的
	// NeedConfirm 来源就是探针 stage（否则:287 短路会让用例"因错误的原因变绿"）。
	e.InitPipelineWithHooks(permission.NewPolicy(permission.LevelDangerFullAccess), ws)
	if e.Pipeline == nil {
		t.Fatal("InitPipelineWithHooks 之后 Pipeline 仍为 nil ⇒ 用例无效")
	}
	e.Pipeline.stages = append(e.Pipeline.stages, &d002ProbeStage{})
	return e, readPath
}

// ---------------------------------------------------------------------------
// A3-1 不变量守卫（可判别：overlay 回退 守卫 ⇒ 必红）
// ---------------------------------------------------------------------------

// TestD002_NeedConfirmImpliesNotProceed 从**生产入口**驱动：
//
//	NewExecutor → InitPipelineWithHooks → ExecuteWithConfirm(OnConfirm == nil)
//
// 断言管线在「成功出口携带 NeedConfirm」时必须 fail-closed。
// 这里刻意用 OnConfirm == nil 的形态，是因为它同时覆盖两类消费方：
// executor.go:791 `Execute`（只看 Proceed）与 tui/tool_exec.go:296（看 NeedConfirm）。
func TestD002_NeedConfirmImpliesNotProceed(t *testing.T) {
	e, readPath := d002Setup(t)

	call := Call{ID: "d002-a31", Name: "read_file", Input: `{"path":` + jsonStringForD002(readPath) + `}`}
	got := e.ExecuteWithConfirm(context.Background(), call)
	if got == nil {
		t.Fatal("ExecuteWithConfirm 返回 nil")
	}

	// 夹具自检：探针必须真的被执行过（= 前面的阶段没有提前短路）。
	// 少了这一条，用例可能在"管线早就因别的原因暂停"时变绿 —— 那是假绿。
	if !d002StageRun(got.StageLogs, "d002_probe_state_only_needconfirm") {
		t.Fatalf("探针 stage 未进入 StageLogs ⇒ 用例无效（管线在更早的阶段就返回了）: %+v", got.StageLogs)
	}

	if got.NeedConfirm && got.Proceed {
		t.Errorf("违反  不变量：NeedConfirm=true 且 Proceed=true ⇒ 只看 Proceed 的调用方会静默执行未经确认的调用。stageLogs=%+v", got.StageLogs)
	}
	if got.Proceed {
		t.Errorf("state.NeedConfirm 为真时管线必须 fail-closed（Proceed=false），实际 Proceed=true；rejection=%q", got.Rejection)
	}
	// Result 必须为 nil：executor.go:795 是先 `if pr.Result != nil` 再判 !Proceed，
	// 带 Result 会让守卫在 Execute 这条入口上失效（fail-open）。这是有意设计。
	if got.Result != nil {
		t.Errorf("fail-closed 出口不得携带 Result（否则 Execute 的 pr.Result!=nil 短路会把未确认的调用当成功返回）")
	}
	if got.Rejection == "" {
		t.Error("fail-closed 出口必须给出 Rejection，否则调用方拿不到拒绝理由")
	}
	t.Logf(" A3-1：Proceed=%v NeedConfirm=%v Rejection=%q stageLogs=%d 条",
		got.Proceed, got.NeedConfirm, got.Rejection, len(got.StageLogs))
}

// TestD002_ExecuteEntry_FailClosed 同一条不当行为的**第二个出口**：
// executor.go:791 `Execute`（TUI/daemon/--ci 的共用入口，也走 ExecuteBatch 的
// safe / unsafe 两条分支）。它只看 Proceed，且**先看 Result**
// ⇒ 这里必须断言「拿到的是错误结果，而不是工具的成功输出」。
//
// 与 A3-1 同属 overlay 可判别（回退 守卫 ⇒ 本用例必红，且红在
// "IsError=false、工具输出被当成成功" 这一 fail-open 事实上）。
func TestD002_ExecuteEntry_FailClosed(t *testing.T) {
	e, readPath := d002Setup(t)

	res := e.Execute(context.Background(), Call{
		ID:    "d002-a31-exec",
		Name:  "read_file",
		Input: `{"path":` + jsonStringForD002(readPath) + `}`,
	})

	if !res.IsError {
		t.Errorf("存在未经确认的调用却被当作成功返回（fail-open）：IsError=%v output=%q", res.IsError, res.Output)
	}
	if strings.Contains(res.Output, "d002-probe") {
		t.Errorf("未确认调用的**执行产物**泄漏给了调用方：output=%q", res.Output)
	}
	t.Logf(" A3-1b：Execute 入口 IsError=%v output=%q", res.IsError, res.Output)
}

// ---------------------------------------------------------------------------
// A3-2 AST 机械门（可判别：受控写回 + 还原 + md5，不能用 -overlay）
// ---------------------------------------------------------------------------

// d002MinPipelineResultLiterals 是本门「没有变瞎」的下界。
// 2026-09-16 实测本包非测试文件共 10 个 PipelineResult 复合字面量
// （pipeline.go 7 + executor.go 3）。取 8 作下界：既容忍正常增删，
// 又能在文件被改名/移动/整体重构时把门本身打红（否则门会静默恒绿）。
const d002MinPipelineResultLiterals = 8

// TestD002_NoNeedConfirmLiteralInProceedLiteral 机械核验：
// **本包非测试文件里不存在 `PipelineResult{... Proceed: true ... NeedConfirm: true ...}` 字面量。**
//
// 覆盖面：`filepath.Glob("*.go")` 排除 `_test.go` —— 即整个 internal/tools 包的生产码
// （不只是 pipeline.go：executor.go 的 ExecuteWithPipeline/ExecuteWithConfirm 也构造
// PipelineResult，是同一不变量的同族出口，见 R3⑤ 同源点穷举）。
//
// ⚠️ 已知边界（必须写下来，否则下一个人会高估它）：
// - 只看**复合字面量**。`Proceed: someVar` / `NeedConfirm: sr.NeedConfirm`
// 这类**变量形态**本门看不见 ⇒ 这正是 A3-1/A3-1b 行为守卫存在的理由。
// - 只看**本包**。
// - 本用例**运行期自读磁盘**（parser.ParseFile 相对路径 + nil src）⇒
// `-overlay` 对它**物理无效**；判别性只能用「受控写回 + 无条件还原 + md5」证明。
func TestD002_NoNeedConfirmLiteralInProceedLiteral(t *testing.T) {
	fset := token.NewFileSet()
	files, err := filepath.Glob("*.go")
	if err != nil {
		t.Fatalf("glob 失败: %v", err)
	}

	scanned, literals := 0, 0
	var violations []string

	for _, f := range files {
		if strings.HasSuffix(f, "_test.go") {
			continue
		}
		file, err := parser.ParseFile(fset, f, nil, 0)
		if err != nil {
			t.Fatalf("解析 %s 失败: %v", f, err)
		}
		scanned++

		ast.Inspect(file, func(n ast.Node) bool {
			cl, ok := n.(*ast.CompositeLit)
			if !ok || !d002IsPipelineResultType(cl.Type) {
				return true
			}
			literals++

			proceedTrue, needConfirmTrue := false, false
			for _, elt := range cl.Elts {
				kv, ok := elt.(*ast.KeyValueExpr)
				if !ok {
					continue
				}
				key, ok := kv.Key.(*ast.Ident)
				if !ok {
					continue
				}
				switch key.Name {
				case "Proceed":
					proceedTrue = d002IsTrueLit(kv.Value)
				case "NeedConfirm":
					needConfirmTrue = d002IsTrueLit(kv.Value)
				}
			}
			if proceedTrue && needConfirmTrue {
				pos := fset.Position(cl.Pos())
				violations = append(violations,
					pos.Filename+":"+strconv.Itoa(pos.Line)+" PipelineResult{Proceed: true, NeedConfirm: true}")
			}
			return true
		})
	}

	// 门的自检：没扫到文件 / 没扫到目标字面量 ⇒ 门已失真（恒绿），必须打红自己。
	if scanned == 0 {
		t.Fatal("没有扫到任何非测试 .go 文件 ⇒ 本门已失真（工作目录或文件名规则变了）")
	}
	if literals < d002MinPipelineResultLiterals {
		t.Fatalf("只找到 %d 个 PipelineResult 复合字面量（预期 ≥ %d）⇒ 本门可能已扫不到目标，判别力已失效",
			literals, d002MinPipelineResultLiterals)
	}

	if len(violations) > 0 {
		t.Errorf("发现 %d 处  违例（PipelineResult 同时 Proceed:true 与 NeedConfirm:true ⇒ 只看 Proceed 的调用方会静默执行未经确认的调用）:\n  %s",
			len(violations), strings.Join(violations, "\n  "))
	}
	t.Logf(" A3-2：扫描 %d 个非测试文件 / %d 个 PipelineResult 字面量，违例 0", scanned, literals)
}

// ---------------------------------------------------------------------------
// A3-3 契约守卫（判别对象 = 本单**打回**的 B 方案，对 A 组不具判别性）
// ---------------------------------------------------------------------------

// TestD002_NilConfirmChannel_Contract 钉住 `ExecuteWithConfirm` 在
// **OnConfirm == nil** 时的**两条**语义（完整理由见 executor.go 该函数头部
// 的 契约注释）：
//
//	① 安全轴：Proceed == false 且 Result == nil —— 绝不执行、绝不把结果漏给调用方；
//	② 接线契约：NeedConfirm 原样透传 —— tui/tool_exec.go:296 / :174 的
//
// pendingConfirm 弹窗、tui/bridge.go:938（传 nil onConfirm）依赖它。
//
// ⚠️ 与 A3-1/A3-2 不同，**本用例对 A 组不具判别性**（A 落与不落它都绿）。
// 它的判别对象是移交单里被**打回**的 B 方案：「OnConfirm == nil ⇒ 返回终局拒绝、
// NeedConfirm=false」。把 B 写回时本用例**必红**（tui 确认弹窗失效），
// 这是打回 B 的实测证据，不是推理。
func TestD002_NilConfirmChannel_Contract(t *testing.T) {
	t.Setenv("HOME", testutil.IsolatedHomeDir(t))

	ws := t.TempDir()
	e := NewExecutor(NewRegistry(), ws)
	e.InitPipelineWithHooks(permission.NewPolicy(permission.LevelWorkspaceWrite), ws)
	if e.Pipeline == nil {
		t.Fatal("Pipeline 未初始化 ⇒ 用例无效")
	}
	// 形态 2：故意不接 OnConfirm（与 tui/tool_exec.go、tui/bridge.go 一致）
	e.OnConfirm = nil

	// workspace-write 下 bash 需确认 ⇒ 管线在 PermissionStage 暂停
	got := e.ExecuteWithConfirm(context.Background(), Call{
		ID:    "d002-a33",
		Name:  "bash",
		Input: `{"command":"mkdir -p d002_probe"}`,
	})
	if got == nil {
		t.Fatal("ExecuteWithConfirm 返回 nil")
	}

	if got.Proceed {
		t.Errorf("无确认通道时不得放行：Proceed=true；rejection=%q", got.Rejection)
	}
	if got.Result != nil {
		t.Errorf("无确认通道时不得返回执行结果（Execute 会先看 pr.Result != nil）")
	}
	if !got.NeedConfirm {
		t.Error("OnConfirm == nil 时必须把 NeedConfirm **原样透传**给调用方：" +
			"tui/tool_exec.go:296 / :174 的「允许/拒绝」弹窗与 tui/bridge.go:938 路径依赖它。" +
			"把这里改成 false（移交单 B 方案）会让 TUI 确认弹窗**彻底失效**，" +
			"所有需确认的调用被静默拒绝。")
	}
	t.Logf(" A3-3：Proceed=%v NeedConfirm=%v Result==nil=%v rejection=%q",
		got.Proceed, got.NeedConfirm, got.Result == nil, got.Rejection)
}

// --------------------------- 小工具 ---------------------------

func d002IsPipelineResultType(expr ast.Expr) bool {
	switch t := expr.(type) {
	case *ast.Ident:
		return t.Name == "PipelineResult"
	case *ast.SelectorExpr:
		return t.Sel != nil && t.Sel.Name == "PipelineResult"
	}
	return false
}

func d002IsTrueLit(expr ast.Expr) bool {
	switch v := expr.(type) {
	case *ast.Ident:
		return v.Name == "true"
	case *ast.ParenExpr:
		return d002IsTrueLit(v.X)
	}
	return false
}

func d002StageRun(logs []StageLog, stage string) bool {
	for _, l := range logs {
		if l.Stage == stage {
			return true
		}
	}
	return false
}

func jsonStringForD002(s string) string {
	// strconv.Quote 产出的 Go 字符串字面量对路径（含 \ 与 "）与 JSON 字符串同构。
	return strconv.Quote(s)
}
