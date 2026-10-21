package tools

// · 规则文件**落工作区**（可随仓库分发）⇒ 只能收紧，不能放宽
//
// ---------------------------------------------------------------------------
// 缺陷面（修改前，两半）
//
// 上半 —— `permission.ruleCheckLocked` 的 `RuleAllow` 分支把 `checkModeLocked`
// 返回的第二个值（`modeNeedConfirm`）**丢掉**（`modeAllowed, _ := ...`）⇒
// 一条写在 `<workDir>/.whale/settings.json` 里的 `allow` 规则足以把「模式本来
// 要求确认」的调用变成免问。这份文件**随仓库分发**（用户 clone 下来就有），
// 于是"他人提供的文件"替本机用户降了门槛。
//
// 下半 —— 规则前置门（`pipeline.go`）为让 `allow` 规则真正免问，会置
// `approved = true`，而 `DangerousGuardStage` / `DestructiveCheckStage` 读的是
// 同一个 `call.Approved` 并据此**跳过危险/破坏性确认** ⇒ 规则文件同样能关掉
// 危险档护栏。修法 = 把"规则推导的免问"与"用户显式批准"在**类型上**分开
// （`tools.Call.ApprovedByRule`）。
//
// ---------------------------------------------------------------------------
// 判别性（可判别性自检 · 反例注入必须能打红）
//
//	判据 ①②③（门槛） 反例 = 删掉 `ruleCheckLocked` 的
// `if rule.Source.RepoDistributed() && modeNeedConfirm { ... }`
// ⇒ 判据 ② 立刻红（needConfirm 由 true 变 false）。
//	判据 ④（ApprovedByRule）反例 = 把两个危险阶段的跳过条件改回裸 `call.Approved`
// ⇒ 判据 ④ 的"推导免问"那一格立刻红。
//
// ★ 判据 ③ 是**配对反证**（与 ② 成对）：证明本修法是"按来源收窄"，
// 不是"一刀切把所有 allow 都收紧" —— 后者会把 userSettings 的正当免问也砍掉。
// ★ 判据 ⑤ 是**端到端**（经 `ToolPipeline.Execute` 生产入口），
// 它证明两半在同一条路径上真的同时生效。

import (
	"context"
	"strings"
	"testing"

	"github.com/anchorandfish/whale-code/internal/hook"
	"github.com/anchorandfish/whale-code/internal/permission"
)

// ---------------------------------------------------------------------------
// 判据 ① · 「来源是否可随仓库分发」的 SSOT
// ---------------------------------------------------------------------------

func TestN053_RepoDistributedTruthTable(t *testing.T) {
	cases := []struct {
		src  permission.RuleSource
		want bool
		why  string
	}{
		{permission.SourceProjectSettings, true, "落 <workDir>/.whale/settings.json，随仓库分发"},
		{permission.SourceLocalSettings, true, "落 <workDir>/.whale/settings.local.json，同属工作区"},
		{permission.SourceUserSettings, false, "落 <homeDir>/.whale/settings.json，用户自己的机器"},
		{permission.SourceSession, false, "用户在本次运行中的显式批准（/allow、确认框记住）"},
		{permission.SourceCLIArg, false, "用户本次敲下的命令行参数"},
	}
	for _, c := range cases {
		if got := c.src.RepoDistributed(); got != c.want {
			t.Errorf(" RepoDistributed(%q) = %v，期望 %v（%s）", c.src, got, c.want, c.why)
		}
	}
}

// ---------------------------------------------------------------------------
// 判据 ② / ③ · 可分发来源的 allow **不得**降本机门槛；用户来源的 allow 仍免问（配对）
// ---------------------------------------------------------------------------

// n053PolicyWithAllow 构造一个 workspace-write 档 Policy，并注入一条 allow 规则。
func n053PolicyWithAllow(t *testing.T, rule string, src permission.RuleSource) *permission.Policy {
	t.Helper()
	p := permission.NewPolicy(permission.LevelWorkspaceWrite)
	parsed, err := permission.ParseRuleString(rule, permission.RuleAllow, src)
	if err != nil {
		t.Fatalf("ParseRuleString(%q): %v", rule, err)
	}
	p.RuleSet().Add(*parsed)
	return p
}

func TestN053_RepoDistributedAllowCannotLowerLocalThreshold(t *testing.T) {
	const (
		toolName = "bash"
		input    = `{"command":"echo hi"}`
		rule     = "bash(echo:*)"
	)

	// ---- 判据 ②：项目级（可分发）allow ⇒ 仍须确认 ----
	proj := n053PolicyWithAllow(t, rule, permission.SourceProjectSettings)
	found, allowed, needConfirm := proj.RuleCheck(toolName, input)
	if !found || !allowed {
		t.Fatalf("前提失效：规则未被命中/未放行（found=%v allowed=%v）——判据②会变成空测", found, allowed)
	}
	if !needConfirm {
		t.Errorf("N-053② 可随仓库分发的 allow 规则**降低了**本机门槛：needConfirm=false。\n"+
			"  期望 true —— 档位（workspace-write）说「要问」，<workDir>/.whale/settings.json 里的 allow\n"+
			"  只能收紧，不能把这次询问免掉。规则=%q 来源=%q", rule, permission.SourceProjectSettings)
	}

	// ---- 判据 ③（配对）：用户自己的 settings ⇒ 免问仍然生效 ----
	user := n053PolicyWithAllow(t, rule, permission.SourceUserSettings)
	found2, allowed2, needConfirm2 := user.RuleCheck(toolName, input)
	if !found2 || !allowed2 {
		t.Fatalf("前提失效：用户级规则未被命中/未放行（found=%v allowed=%v）", found2, allowed2)
	}
	if needConfirm2 {
		t.Errorf("N-053③ 配对失败：**用户自己的** allow 规则被一并收紧了（needConfirm=true）。\n" +
			"  本修法只应作用于可随仓库分发的来源；userSettings 是用户显式写下的免问，必须仍然生效。")
	}

	// ---- 判据 ③'（配对）：本地级 settings.local.json 也属可分发面 ----
	local := n053PolicyWithAllow(t, rule, permission.SourceLocalSettings)
	if _, _, nc := local.RuleCheck(toolName, input); !nc {
		t.Errorf("N-053③' settings.local.json 同样落在工作区内（可被提交进仓库）⇒ 必须与 projectSettings 同处理，实际 needConfirm=false")
	}
}

// ---------------------------------------------------------------------------
// 判据 ④ · 危险档 / 破坏性阶段：**规则推导的免问**不得跳过确认（配对）
// ---------------------------------------------------------------------------

func TestN053_DangerousStagesIgnoreRuleDerivedApproval(t *testing.T) {
	policy := permission.NewPolicy(permission.LevelWorkspaceWrite)
	ctx := context.Background()

	// (A) DangerousGuardStage —— 危险档（不可逆/灾难级）。
	dg := &DangerousGuardStage{policy: policy}
	dangerInput := `{"command":"shutdown -h now"}`

	// ④-A-1 前置：无 Approved ⇒ 必须确认（证明夹具真的命中危险档）。
	sr := dg.Execute(ctx, Call{Name: "bash", Input: dangerInput}, &PipelineState{})
	if !sr.NeedConfirm {
		t.Fatalf("前提失效：危险档夹具未命中（sr=%+v）—— 判据④会变成空测", sr)
	}

	// ④-A-2 规则推导的免问 ⇒ **仍须确认**。
	sr = dg.Execute(ctx, Call{Name: "bash", Input: dangerInput, Approved: true, ApprovedByRule: true}, &PipelineState{})
	if !sr.NeedConfirm {
		t.Errorf("N-053④ 规则推导的免问跳过了**危险档**确认（ApprovedByRule=true 时不再询问）。\n"+
			"  ⇒ 一条 <workDir>/.whale/settings.json 里的 allow 即可关掉危险档护栏。sr=%+v", sr)
	}

	// ④-A-3（配对）：用户**显式批准**（ApprovedByRule=false）⇒ 跳过，不得重复询问。
	// 这一格是必需的：若连它也不跳过，`ExecuteWithConfirm` 的"批准后重跑"会死循环。
	sr = dg.Execute(ctx, Call{Name: "bash", Input: dangerInput, Approved: true, ApprovedByRule: false}, &PipelineState{})
	if sr.NeedConfirm || !sr.Proceed {
		t.Errorf("N-053④-A3 配对失败：用户**显式批准**后仍被要求确认（sr=%+v）⇒ 批准后重跑将死循环", sr)
	}

	// (B) DestructiveCheckStage —— 破坏性档（有副作用、需确认，非灾难级）。
	dc := &DestructiveCheckStage{}
	destructiveInput := `{"command":"rm -rf ./n053_target"}`

	sr = dc.Execute(ctx, Call{Name: "bash", Input: destructiveInput}, &PipelineState{})
	if !sr.NeedConfirm {
		t.Fatalf("前提失效：破坏性夹具未命中（sr=%+v）—— 判据④会变成空测", sr)
	}

	sr = dc.Execute(ctx, Call{Name: "bash", Input: destructiveInput, Approved: true, ApprovedByRule: true}, &PipelineState{})
	if !sr.NeedConfirm {
		t.Errorf("N-053④ 规则推导的免问跳过了**破坏性档**确认。sr=%+v", sr)
	}

	sr = dc.Execute(ctx, Call{Name: "bash", Input: destructiveInput, Approved: true, ApprovedByRule: false}, &PipelineState{})
	if sr.NeedConfirm || !sr.Proceed {
		t.Errorf("N-053④-B3 配对失败：用户显式批准后仍被要求确认（sr=%+v）", sr)
	}
}

// ---------------------------------------------------------------------------
// 判据 ⑤ · 端到端：经生产入口 `ToolPipeline.Execute`
// ---------------------------------------------------------------------------

func TestN053_ProjectAllowRuleCannotSilenceDestructivePrompt(t *testing.T) {
	ws := t.TempDir()
	r := NewRegistry()
	e := NewExecutor(r, ws)

	policy := permission.NewPolicy(permission.LevelWorkspaceWrite)
	parsed, err := permission.ParseRuleString("bash(rm:*)", permission.RuleAllow, permission.SourceProjectSettings)
	if err != nil {
		t.Fatalf("ParseRuleString: %v", err)
	}
	policy.RuleSet().Add(*parsed)

	p := NewToolPipeline(r, e, hook.NewManager(ws), policy, ws)

	// 前置：规则确实被命中且放行（否则本用例会因"规则没生效"而假绿）。
	found, allowed, _ := policy.RuleCheck("bash", `{"command":"rm -rf ./n053_target"}`)
	if !found || !allowed {
		t.Fatalf("前提失效：项目级 allow 规则未命中/未放行（found=%v allowed=%v）", found, allowed)
	}

	pr := p.Execute(context.Background(), Call{
		ID:    "n053-e2e-001",
		Name:  "bash",
		Input: `{"command":"rm -rf ./n053_target"}`,
	})

	if !pr.NeedConfirm {
		t.Errorf("N-053⑤ 端到端失败：项目级（可随仓库分发）allow 规则使破坏性命令**不再确认**。\n"+
			"  Proceed=%v NeedConfirm=%v Rejection=%q\n  stageLogs=%+v",
			pr.Proceed, pr.NeedConfirm, pr.Rejection, pr.StageLogs)
	}
	if pr.Proceed {
		t.Errorf("N-053⑤ 破坏性命令不得在未确认的情况下放行：Proceed=true（rejection=%q）", pr.Rejection)
	}
	// 顺带核验：拒绝/确认理由里不得出现"用户拒绝"（用户从未被询问）。
	if strings.Contains(pr.Rejection, "denied by user") {
		t.Errorf("N-053⑤ 文案违规：用户从未被询问，Rejection 不得写\"用户拒绝\"：%q", pr.Rejection)
	}
}
