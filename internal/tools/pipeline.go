package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/anchorandfish/whale-code/internal/approval"
	"github.com/anchorandfish/whale-code/internal/diff"
	"github.com/anchorandfish/whale-code/internal/hook"
	whalelog "github.com/anchorandfish/whale-code/internal/log"
	"github.com/anchorandfish/whale-code/internal/permission"
	"github.com/anchorandfish/whale-code/internal/platform"
	"github.com/anchorandfish/whale-code/internal/sandbox"
	"github.com/anchorandfish/whale-code/internal/sandbox/validator"
	"github.com/anchorandfish/whale-code/internal/security/cmdrules"
	"github.com/anchorandfish/whale-code/internal/util"
)

// PipelineResult represents a pipeline execution result
type PipelineResult struct {
	Proceed     bool
	Result      *Result
	Rejection   string
	NeedConfirm bool
	FailureKind FailureKind
	StageLogs   []StageLog

	// ConfirmSource 记录本条结果的**成因方**（· 2026-09-20）。
	//
	// 为什么放在这里：`Rejection` 是一句**本地化文案**，而"这条是谁拒的、
	// 用户到底有没有被问过"是可判定的**结构化事实** —— R4 明确禁止用散文关键词
	// 断言（`internal/ai/refusal_handler.go` 的判据收口同款教训）。修复前调用方
	// （`tui/runner_core.go:1436`、`tui/plain.go:1994`）只能自己拼
	// `"user cancelled tool execution"`，于是**管线自己拒的**调用在非交互入口上
	// 被显示成"用户取消了执行" —— 即 `tools/confirm.go:42` 硬纪律禁止的那句谎。
	//
	// 取值与语义（唯一判据是 `tools.ConfirmSource` 常量，**不得**由调用方自造字符串）：
	// - 零值 ⇒ 本条结果不涉及确认交互（例如成功出口）。
	// - SourceUser ⇒ 用户在确认框上按了 N（只有这一种可以说"用户拒绝"）。
	// - SourcePipeline ⇒ 管线在确认之前就拒了（规则门 / 边界 / 安全阶段 / 档位），
	// **用户从未被询问**。
	//
	// 单一填充点：本文件的各拒绝出口 + `ExecuteWithConfirm` 的用户拒绝分支
	// （executor.go）—— 调用方只**读**，不得自行推断。
	ConfirmSource ConfirmSource
}

// StageLog represents a stage log entry
type StageLog struct {
	Stage   string
	Status  string
	Message string
}

// PipelineStage is the pipeline stage interface
type PipelineStage interface {
	Execute(ctx context.Context, call Call, state *PipelineState) StageResult
	Name() string
}

// StageResult represents a stage execution result
type StageResult struct {
	Proceed     bool
	Rejection   string
	NeedConfirm bool
	Message     string
	FailureKind FailureKind
}

// PipelineState is the pipeline internal shared state
type PipelineState struct {
	Result   *Result
	FilePath string
	// TargetPaths 是本次调用将写入的**全部**目标路径（G5，2026-09-14）。
	//
	// 为什么需要它：`FilePath` 假设"一个工具调用 = 一个目标文件"，这对 write_file /
	// edit_file 成立，对 apply_unified_diff 不成立 —— 一段补丁里可以有 N 个文件。
	// 若只校验 FilePath，补丁里第 2 个越界路径会**绕过 CustomValidateStage** 直达 handler。
	//
	// 语义：**为空时退回 FilePath 单目标语义**（见 WriteTargets），因此对既有单文件
	// 工具是严格零行为变化；多文件工具由 pipelineWriteTargets 填充。
	TargetPaths []string
	// FileExistedBefore 目标文件在本次管道执行前是否已存在（2026-09-08 修复）。
	// cleanupTempFiles 用它区分"本次新产生的文件"与"执行前已存在的文件"，
	// 防止失败/暂停路径误删用户既有文件。
	FileExistedBefore bool
	NeedConfirm       bool
	UpdatedInput      string // Tool input modified by hook, passed to subsequent stages
	ExecutionError    string // Error message from tool execution, accessible by later stages
	// UserApproved 是**原始入口**的逐条批准事实（，2026-10-08）。
	//
	// 判据 = `call.Approved && !call.ApprovedByRule`，且必须在 Execute 头部
	// **规则门 / 会话记忆改写 approved 之前**取定 —— 否则 allow 规则与
	// `policy.Has` 推导出的 approved=true 会被放大成沙箱豁免（纪律：
	// 规则推导的免问不是"用户对这一条按下的 Yes"）。
	//
	// 消费点唯一：ExecuteStage（三入口 plain / TUI 重跑 / 子代理恢复 必经的
	// handler 调用点）据此把沙箱批准标记注入执行链 ctx。产生点收口的完整
	// 理由见 sandbox_sanction.go 头注。
	UserApproved bool
}

// WriteTargets 返回本次调用需要按「写类工具」校验收的全部目标路径。
//
// 单一入口：CustomValidateStage 只认这一个方法，不再自己决定"看 FilePath 还是看列表"。
// 空字符串被过滤（`--- /dev/null` 等在管线层不应产生空目标）。
func (s *PipelineState) WriteTargets() []string {
	if len(s.TargetPaths) > 0 {
		out := make([]string, 0, len(s.TargetPaths))
		for _, p := range s.TargetPaths {
			if strings.TrimSpace(p) != "" {
				out = append(out, p)
			}
		}
		if len(out) > 0 {
			return out
		}
	}
	if strings.TrimSpace(s.FilePath) != "" {
		return []string{s.FilePath}
	}
	return nil
}

// ToolPipeline is the tool execution pipeline
type ToolPipeline struct {
	Name     string
	stages   []PipelineStage
	hookMgr  *hook.Manager
	policy   *permission.Policy
	executor *Executor
	registry *Registry
	workDir  string
}

// NewToolPipeline creates a tool execution pipeline
func NewToolPipeline(registry *Registry, executor *Executor, hookMgr *hook.Manager, policy *permission.Policy, workDir string) *ToolPipeline {
	p := &ToolPipeline{
		hookMgr:  hookMgr,
		policy:   policy,
		executor: executor,
		registry: registry,
		workDir:  workDir,
	}

	// executor 可能为 nil（测试可传 nil）；Sandbox 由 NewExecutor 恒建（executor.go:245）。
	var sbx *sandbox.Sandbox
	if executor != nil {
		sbx = executor.Sandbox
	}

	p.stages = []PipelineStage{
		&SchemaValidateStage{registry: registry},
		&CustomValidateStage{workDir: workDir},
		&PreHookStage{hookMgr: hookMgr},
		// ★ 2026-10-04 新增：与执行链同源的沙箱预检（成因与接线见 SandboxPrecheckStage 注释）。
		//
		// 位置刻意在 PreHookStage **之后**：pre-hook 可改写命令（`state.UpdatedInput`
		// 经本文件 :380-381 覆盖 `normalizedCall.Input`），判定必须针对**最终会被执行的
		// 命令**；排在 PreHookStage 之前会拿"将被改写的命令"下结论，与执行链不一致。
		//
		// 又在 DangerousGuardStage **之前**：硬拒绝（能不能做）优先于任何"需确认"
		// （要不要问）。这与 -R1 同款纪律 —— 靠**顺序短路**，不靠"记得在每处判一遍"。
		&SandboxPrecheckStage{sandbox: sbx, policy: policy},
		// -R1：危险档（不可逆/灾难级）判定必须排在**所有免问出口之前** ——
		// PermissionStage 的 (a)/(b)/(c) 三个出口都在它内部，晚了就会被吃掉。
		&DangerousGuardStage{policy: policy},
		&DestructiveCheckStage{policy: policy}, // 桶 X6 P-4：破坏性操作确认（PreToolUse 阶段）
		&SafetyCheckStage{workDir: workDir, policy: policy},
		&PermissionStage{policy: policy, workDir: workDir},
		&ExecuteStage{executor: executor, registry: registry},
		&PostHookStage{hookMgr: hookMgr},
	}

	return p
}

// SetApprovalManager 注入 C19 第三层交互层 approval.Manager。
// 传入 nil 可清除已有管理器（恢复到无交互层时的暂停等待确认行为）。
// 未调用此方法时 PermissionStage.approvalMgr 为 nil，保持向后兼容。
func (p *ToolPipeline) SetApprovalManager(mgr *approval.Manager) {
	for _, stage := range p.stages {
		if ps, ok := stage.(*PermissionStage); ok {
			ps.approvalMgr = mgr
			return
		}
	}
}

// SetEnforcementStatus 注入「OS 级沙箱是否真的在强制」的判据。
//
// 传入 nil = 不可强制（fail-closed 默认）⇒ PermissionStage 的出口 (c) 不生效。
// 未调用此方法时 PermissionStage.enforcement 为 nil，行为与改动前逐字一致。
func (p *ToolPipeline) SetEnforcementStatus(es EnforcementStatus) {
	for _, stage := range p.stages {
		if ps, ok := stage.(*PermissionStage); ok {
			ps.enforcement = es
			return
		}
	}
}

// Policy 返回 pipeline 持有的权限策略引用，供 skillHandler 等调用方复用 SetAllowedTools/GetAllowedTools
// 未注入时返回 nil，调用方应自行判空降级
func (p *ToolPipeline) Policy() *permission.Policy {
	return p.policy
}

// runOnFailureApproval 在工具执行失败后查找 PermissionStage 并调用其 checkOnFailure 方法。
// 返回 nil 表示跳过 on-failure 审批（approvalMgr 为 nil 或非 OnFailure 策略）。
func (p *ToolPipeline) runOnFailureApproval(ctx context.Context, call Call, state *PipelineState) *StageResult {
	for _, stage := range p.stages {
		if ps, ok := stage.(*PermissionStage); ok {
			return ps.checkOnFailure(ctx, call, state)
		}
	}
	return nil
}

// logPipelineRejection 在**管线拒绝点**补一条「执行面」结构化留痕（G9-② · 分诊表）。
//
// 为什么需要它：`[audit] tool: execute done`（executor.go:657）写在 `executeDirect`
// **内部** ⇒ 只有**真的执行了**才留痕；而管线在任一阶段拒绝时是**短路返回**，
// 于是"被拒的调用"在 whale.log 里**零行**，I12（可复现性）不成立。
// 真机佐证（2026-09-16 02:03）：`~/.whale/logs/whale.log` 中被拒的 write_file **零行**。
//
// 为什么挂在**管线**而不是 TUI：`ToolPipeline.Execute` 是**唯一覆盖全部入口**的点
// （TUI 交互 / bridge / plain / 子 agent **都**经它）——挂在 TUI 侧只覆盖交互入口，
// 等于"只修了看得见的那一半"。
//
// 形态与 `execute done` 对齐（同一 logger、同一 key-value 风格），级别 Info
// —— 生产默认级别即 Info（internal/log/log.go:19），Debug 在默认配置下等价于零留痕（C9 教训）。
//
// 本函数**只留痕、零判定语义**：不改变任何返回结果，不新增拒绝面。
// 本函数**必须**带 `call_id`，与执行面留痕
// （`executor.go` 的 `[audit] tool: execute done`）共用同一关联键。
//
// 为什么参数改成整个 `Call` 而不是 `tool string`：修复前这里只有工具名，
// 于是一次调用的「决策」（问/放/拒）与「执行」落在两个通道、**没有共同键**
// —— 事后无法把某条拒绝归因到具体哪一次调用（同批多条同工具名时更是完全不可分）。
// 取 `Call` 而非多传一个 string，是为了让"新字段要透传"在**类型上**无法漏。
//
// 注意 `call.ID` 可能为空（未经 session 装配的内部调用）—— 此时留痕为空串，
// 调用方按"无关联键"处理，**不得**用工具名/时间戳凑一个假 id。
func logPipelineRejection(stage string, call Call, kind FailureKind, reason string) {
	whalelog.Info("[audit] tool: rejected",
		"phase", "tool_execute",
		"call_id", call.ID,
		"tool", call.Name,
		"stage", stage,
		"failure_kind", string(kind),
		"reason", reason,
	)
}

// logPipelineConfirmRequired 在**管线确认点**补一条结构化留痕（-R1）。
//
// 与 logPipelineRejection 同一 logger、同一 key-value 风格，级别 Info
// （生产默认级别即 Info，见 internal/log/log.go:19 —— Debug 在默认配置下等价于零留痕）。
//
// 为什么需要它：确认诉求目前**只存在于返回值里**（`Result.NeedConfirm`），落盘日志里
// 看不出"被哪一条规则问住"；而 `Execute` 会把 NeedConfirm 情形的 StageLogs.Message
// 统一改写成 "user confirmation required"（本文件 :292），规则 ID 因此在留痕里丢失。
// 这里按 R4 记**结构化字段**（rule_id），不靠散文关键词。
// 与 logPipelineRejection 同款 —— 决策面留痕必须带
// `call_id`，才能与执行面留痕（`[audit] tool: execute done`）对齐。
func logPipelineConfirmRequired(stage string, call Call, ruleID string) {
	whalelog.Info("[audit] tool: confirm required",
		"phase", "tool_execute",
		"call_id", call.ID,
		"tool", call.Name,
		"stage", stage,
		"rule_id", ruleID,
	)
}

// Execute runs stages sequentially, short-circuits when any stage returns Proceed=false
// When a stage returns NeedConfirm=true, the pipeline pauses and waits for user confirmation
func (p *ToolPipeline) Execute(ctx context.Context, call Call) *PipelineResult {
	var stageLogs []StageLog
	var state PipelineState
	cleanedUp := false
	defer func() {
		if !cleanedUp {
			cleanupTempFiles(&state)
		}
	}()

	call = normalizeToolCallInput(call)
	name := NormalizeToolName(call.Name)
	approved := call.Approved
	// ★ ：原始入口的逐条批准事实必须在此取定 ——
	// 往下走，规则门与 `policy.Has` 会把 `approved` 改写成"规则/记忆推导的
	// 免问"，那不是"用户对这一条按下的 Yes"，不得据此豁免沙箱。
	// 消费点 = ExecuteStage（唯一），见 PipelineState.UserApproved 注释。
	state.UserApproved = call.Approved && !call.ApprovedByRule
	// approvedByRule：本 Call 的免问是**规则/记忆推导**出来的，
	// 而不是用户在确认框上对**这一条**调用按下的 Yes。两个危险阶段据此
	// **不**跳过确认（见 tools.Call.ApprovedByRule 的完整理由）。
	approvedByRule := false

	// 规则前置裁决：必须在任何可能请求用户确认的阶段之前完成。
	//
	// 背景（A/B 实测暴露）：DestructiveCheckStage / SafetyCheckStage 排在
	// PermissionStage 之前，而 Execute 循环一旦遇到 NeedConfirm 就立即短路返回
	// （见下方 "When user confirmation is needed" 分支）。于是持有规则的
	// PermissionStage 根本没机会参与决策 —— 表现为"配了 deny 规则，弹窗照弹"；
	// allow 规则同理，永远无法在真实路径上免问。
	//
	// 对齐业内既有实现：Decision::Forbidden 绝不进入审批流；
	// allow 只能免问、不能扩权（core/src/tools/sandboxing.rs:210-217）——
	// 这里只置 approved（等价于"用户已通过该安全提示"），沙箱仍由 ExecuteStage 独立执行。
	if p.policy != nil {
		// 用 `RuleDecision` 而不是 `RuleCheck` —— 前者额外返回**胜出规则的来源**，
		// 后者把来源丢了（三个 bool）。两者共用 permission 包内同一个
		// `ruleDecisionLocked` 决议，所以这里**没有**"管线自己再解析一遍规则"的第二份实现。
		if ruleFound, ruleAllowed, ruleNeedConfirm, ruleSource := p.policy.RuleDecision(name, call.Input); ruleFound {
			if !ruleAllowed {
				logPipelineRejection("rule_gate", Call{ID: call.ID, Name: name}, FailureKindPolicyDenied,
					fmt.Sprintf("tool %q blocked by explicit deny rule", name))
				return &PipelineResult{
					Proceed:       false,
					Rejection:     fmt.Sprintf("tool %q blocked by explicit deny rule", name),
					NeedConfirm:   false,
					FailureKind:   FailureKindPolicyDenied,
					ConfirmSource: SourcePipeline,
					StageLogs: []StageLog{{
						Stage:   "rule_gate",
						Status:  "reject",
						Message: "denied by permission rule (confirmation skipped)",
					}},
				}
			}
			// allow 规则命中且规则本身不要求确认（非 ask、非危险命令）→ 免问放行。
			// ask 规则或 allow 命中危险命令时不置位，仍走原有确认流程。
			if !ruleNeedConfirm {
				approved = true
				// ★ ：当这条 allow 来自**可随仓库分发**的来源时，把免问标成
				// "推导的"，使 DangerousGuardStage / DestructiveCheckStage
				// **不**因此跳过确认。
				//
				// ★ 口径只取 `RepoDistributed()`（`<workDir>/.whale/settings*.json`），
				// **不**取 `SourceSession`/`SourceCLIArg` —— 后两者是本机用户在
				// **本次运行**中的显式意图（`/allow`、"本会话记住"、命令行参数），
				// 让它们免问是本功能的**本意**。第一版实现把"凡规则推导"一律标记，
				// 结果把 `SourceSession` 的正当免问也砍掉了（`pipeline_rule_gate_test.go`
				// 的 `TestPipeline_AllowRuleSkipsConfirmation` 因此变红）——
				// 「按**来源**收窄」与「一刀切收紧」是两件事，本行是那个区别。
				//
				// ★ 而 `!call.Approved` 这个条件是**必需的**，不是保险：进入本行的
				// `call.Approved == true` 只可能来自 `ExecuteWithConfirm` 的
				// 「用户按下 Yes 后带 Approved 重跑整条管线」（executor.go）。
				// 若重跑时仍标 ApprovedByRule，两个危险阶段会**再次**拒绝跳过 ⇒
				// 确认框反复弹出 ⇒ 死循环。语义上也不对：那一次免问的决定性来源
				// 是**用户的显式批准**，规则只是同时命中而已。
				approvedByRule = ruleSource.RepoDistributed() && !call.Approved
			}
		}
	}

	// Session approvals suppress only interactive safety prompts. Permission
	// checks still run below, so changing to read-only or adding a deny rule
	// remains effective for remembered calls.
	if !approved && p.policy != nil && p.policy.Has(name, call.Input) {
		approved = true
	}
	normalizedCall := Call{
		ID:       call.ID,
		Name:     name,
		Input:    call.Input,
		Approved: approved,
		// ApprovedByRule 与 ReusedApproval 同款纪律：本处是**重建**一个 Call，
		// 任何新字段都必须显式列出，否则会在进入阶段之前被静默丢弃
		// （2026-09-20 已在 ReusedApproval 上实测踩到一次）。
		ApprovedByRule: approvedByRule,
		// ReusedApproval 必须**逐字透传**：它是调用方（TUI）置位的内部执行状态，
		// 语义是「本次请求内同参数已获批过」。
		//
		// ★ 漏掉这一行的后果（2026-09-20 实测踩到）：本处是**重建**一个 Call，
		// 只挑了几个字段拷 —— 于是 TUI 在 tool_exec.go 里置的 ReusedApproval
		// 在进入 PermissionStage 之前就被静默丢弃，表现为「改了但没生效」
		// （复用分支永不命中，重复确认照旧）。这正是本仓的经典陷阱：
		// 「多入口各写各的」——任何重建 Call 的地方都必须显式列出要透传的字段。
		//
		// 反向断言见 approval_reuse_test.go 的
		// TestFix_ApprovalReuse_IdenticalCallInSameRequestAskedOnce：
		// 若删掉本行，该用例立刻转红（已用 `-overlay` 在改前基线上实测）。
		ReusedApproval: call.ReusedApproval,
		// SandboxEscalated 同款纪律 —— 本处是**重建**一个 Call，
		// 漏列 = on-failure 升级重跑的 sanction 标记在进入阶段前被静默丢弃，
		// 重跑仍在沙箱内（w094 E2E 判别器抓到的实锤：链路直调 sandboxed=false，
		// 全管线重跑 EPERM 依旧）。
		SandboxEscalated: call.SandboxEscalated,
	}

	state.FilePath = pipelineExtractPath(call.Input)
	// G5：多目标工具（补丁类）的目标路径埋在补丁文本里，不在顶层字段 ——
	// 这里把它们一并登记进 TargetPaths，使 CustomValidateStage 能逐个校验。
	// 单文件工具的 TargetPaths 为 nil ⇒ WriteTargets() 退回 [FilePath]，行为不变。
	state.TargetPaths = pipelineWriteTargets(name, call.Input, p.workDir)
	// 2026-09-08 修复（G4 排查发现）：记录目标文件执行前是否存在，
	// 供 cleanupTempFiles 区分"本次新产生"与"执行前已存在"。
	if state.FilePath != "" {
		if _, err := os.Stat(state.FilePath); err == nil {
			state.FileExistedBefore = true
		}
	}

	for i, stage := range p.stages {
		sr := stage.Execute(ctx, normalizedCall, &state)

		// If pre-hook modified input, propagate to subsequent stages
		if state.UpdatedInput != "" && i > 0 {
			normalizedCall.Input = state.UpdatedInput
		}

		log := StageLog{
			Stage: stage.Name(),
		}

		if sr.Proceed {
			log.Status = "pass"
			log.Message = sr.Message
		} else if sr.NeedConfirm {
			log.Status = "pass"
			log.Message = "user confirmation required"
		} else {
			log.Status = "reject"
			log.Message = sr.Rejection
		}

		stageLogs = append(stageLogs, log)

		// When user confirmation is needed, pause pipeline execution, do not continue to subsequent stages
		if sr.NeedConfirm {
			return &PipelineResult{
				Proceed:     false,
				Result:      nil,
				Rejection:   sr.Message,
				NeedConfirm: true,
				StageLogs:   stageLogs,
			}
		}

		if !sr.Proceed {
			cleanupTempFiles(&state)
			cleanedUp = true
			logPipelineRejection(stage.Name(), normalizedCall, sr.FailureKind, sr.Rejection)
			return &PipelineResult{
				Proceed:     false,
				Result:      state.Result,
				Rejection:   sr.Rejection,
				NeedConfirm: sr.NeedConfirm,
				FailureKind: sr.FailureKind,
				StageLogs:   stageLogs,
				// 本出口是「管线在确认**之前**就拒了」⇒ 用户从未被询问。
				// 调用方（runnerCore / plain）必须据此说"被管线拒绝"，**不得**
				// 沿用修复前的 `"user cancelled tool execution"`。
				ConfirmSource: SourcePipeline,
			}
		}

		// B3: on-failure 审批接入 — ExecuteStage 执行失败后调用 CheckWithResult。
		// 仅当 stage 为 execute 且存在执行错误时触发，approvalMgr 为 nil 或非 OnFailure 策略时跳过（向后兼容）。
		if stage.Name() == "execute" && state.ExecutionError != "" &&
			(state.Result == nil || state.Result.FailureKind != FailureKindSandboxDenied) {
			if failSR := p.runOnFailureApproval(ctx, normalizedCall, &state); failSR != nil {
				failLog := StageLog{Stage: "on_failure_approval"}
				if failSR.Proceed && !failSR.NeedConfirm {
					failLog.Status = "pass"
					failLog.Message = failSR.Message
				} else if failSR.NeedConfirm {
					failLog.Status = "pass"
					failLog.Message = "on-failure user confirmation required"
				} else {
					failLog.Status = "reject"
					failLog.Message = failSR.Rejection
				}
				stageLogs = append(stageLogs, failLog)

				// NeedConfirm：暂停管道，等待用户确认（工具已执行，包含失败结果）
				if failSR.NeedConfirm {
					return &PipelineResult{
						Proceed:     false,
						Result:      state.Result,
						Rejection:   failSR.Message,
						NeedConfirm: true,
						StageLogs:   stageLogs,
					}
				}
				// Deny：终止管道，返回拒绝原因
				if !failSR.Proceed {
					cleanupTempFiles(&state)
					cleanedUp = true
					logPipelineRejection("on_failure_approval", normalizedCall, failSR.FailureKind, failSR.Rejection)
					return &PipelineResult{
						Proceed:     false,
						Result:      state.Result,
						Rejection:   failSR.Rejection,
						NeedConfirm: false,
						FailureKind: failSR.FailureKind,
						StageLogs:   stageLogs,
					}
				}
				// Allow：继续后续阶段（PostHookStage 等）
			}
		}
	}

	// 不变量守卫（设计偏差审查 C1 · 2026-09-16，移交单）：
	//
	// 不变量：**`PipelineResult.NeedConfirm == true` ⟹ `Proceed == false`**。
	//
	// 为什么今天恒成立、却仍要显式守卫：上面 `:287` 的短路是按 `sr.NeedConfirm`
	// （**stage 返回值**）判的，而不是按 `state.NeedConfirm`（**可变共享状态**）。
	// 两者今天同步（`DestructiveCheckStage` 在 `:873` 置 state 的同时也置了
	// StageResult），但同步关系**没有任何机制保证** —— 某个 stage 只要
	// 「置了 `state.NeedConfirm = true` 却不在 StageResult 里带 NeedConfirm」，
	// 短路就不会命中，管道会一路走到这里，并返回
	// `{Proceed:true, NeedConfirm:true}`。
	//
	// 这个形态为什么危险：`Executor.Execute`（executor.go:791）与
	// `ToolScheduler`（scheduler.go:283）**只看 `Proceed`** ⇒ 会**静默执行
	// 未经确认的调用**（`Executor.Execute` 还先看 `pr.Result != nil`，
	// 见 executor.go:795 → 连拒绝文案都不会出现）。
	// 而 `executor.go:857` 的 `OnConfirm == nil` 分支会把 NeedConfirm 原样
	// 透传给调用方（TUI 的确认弹窗依赖这条语义，见 `ExecuteWithConfirm` 契约注释）
	// ⇒ 该形态一旦出现，就是「既没问用户、也没被拒绝」的第三种状态。
	//
	// 处置：把「契约违反」变成 **fail-closed 拒绝**，而不是 fail-open 执行。
	// 形态对齐 `:288` 的确认暂停出口（`Result: nil` + `NeedConfirm: true`），
	// 因此这里**不置 `cleanedUp`**，由函数入口的 defer 统一清理临时文件。
	//
	// ⚠️ `Result` 必须取 `nil`，**不得**取 `state.Result`：`Executor.Execute`
	// 的返回顺序是「先 `if pr.Result != nil` 直接返回成功结果，再判 `!pr.Proceed`」
	// （executor.go:794-812）⇒ 带上 Result 会让本守卫在 `Execute` 这条入口上
	// **失效（fail-open）**，与不变量目的相反。
	//
	// R5 · 死代码风险声明：**本分支在当前代码上不可达**（上面那段「两者今天同步」
	// 即理由；穷举见 `d002_needconfirm_invariant_test.go` 头部注释）。它存在的意义是
	// 让上述契约违反在**未来**变成一次拒绝而非一次静默执行；判别性由该测试文件
	// 的探针用例保证（回退本分支必红）。
	if state.NeedConfirm {
		return &PipelineResult{
			Proceed:     false,
			Result:      nil,
			Rejection:   "pipeline state requires user confirmation but no stage surfaced it (fail-closed)",
			NeedConfirm: true,
			StageLogs:   stageLogs,
			// 本出口是**契约违反**触发的 fail-closed，用户从未被询问
			//（该分支今天不可达，见上方 R5 声明）⇒ 成因方是管线自己。
			ConfirmSource: SourcePipeline,
		}
	}

	// 2026-09-08 修复（G4 排查发现）：成功路径禁止清理目标文件。
	// 原实现在此调用 cleanupTempFiles 删除 os.TempDir() 前缀下的 FilePath，
	// 导致成功写入 /tmp 的文件被静默删除（write_file 返回成功但文件消失，
	// 随后 bash 引用该文件必然失败）。cleanedUp=true 同时关闭 defer 兜底。
	cleanedUp = true
	return &PipelineResult{
		Proceed: true,
		Result:  state.Result,
		// 成功出口**恒为 false**（`state.NeedConfirm == true` 已被上面挡住）。
		// 写常量而非 `state.NeedConfirm`，是为了让不变量在**字面上**成立，
		// 并且能被 d002_needconfirm_invariant_test.go 的 AST 门机械核验。
		NeedConfirm: false,
		StageLogs:   stageLogs,
	}
}

// cleanupTempFiles cleans up temporary files referenced in pipeline state.
// 2026-09-08 修复（G4 排查发现）：
// 原实现无条件删除 os.TempDir() 前缀下的 FilePath——（a）成功写入 /tmp 的文件
// 被静默删除（成功路径调用点已移除）；（b）失败/确认暂停路径会误删执行前已
// 存在的同名文件（edit_file 未过权限即删用户既有文件）。新守卫：仅当目标
// 文件是本次执行新产生（执行前不存在）时才允许清理。
func cleanupTempFiles(state *PipelineState) {
	if state.FilePath == "" || !strings.HasPrefix(state.FilePath, os.TempDir()) {
		return
	}
	if state.FileExistedBefore {
		return
	}
	os.Remove(state.FilePath)
}

// --- Stage 1: Schema Validation ---

// SchemaValidateStage is the Schema validation stage
type SchemaValidateStage struct {
	registry *Registry
}

// Name returns the stage name
func (s *SchemaValidateStage) Name() string {
	return "schema_validate"
}

// Execute performs Schema validation.
// T9 fail-closed: 未注册工具不跳过验证，而是直接拒绝（对标同类 CLI TOOL_DEFAULTS）。
func (s *SchemaValidateStage) Execute(ctx context.Context, call Call, state *PipelineState) StageResult {
	spec, ok := s.registry.Get(call.Name)
	if !ok {
		return StageResult{
			Proceed:   false,
			Rejection: fmt.Sprintf("tool %q not registered (fail-closed)", call.Name),
		}
	}

	schema := spec.InputSchema
	if schema == nil {
		return StageResult{Proceed: true, Message: "no InputSchema, skipping"}
	}

	var input map[string]interface{}
	rawInput := strings.TrimSpace(call.Input)
	if rawInput == "" {
		input = make(map[string]interface{})
	} else if err := json.Unmarshal([]byte(rawInput), &input); err != nil {
		return StageResult{
			Proceed:   false,
			Rejection: fmt.Sprintf("input is not a valid JSON object: %v", err),
		}
	}

	if required, ok := schema["required"].([]string); ok {
		for _, field := range required {
			if _, exists := input[field]; !exists {
				return StageResult{
					Proceed:   false,
					Rejection: util.Bi(fmt.Sprintf("missing required field: %s", field), fmt.Sprintf("缺少必填字段: %s", field)),
				}
			}
		}
	}

	if requiredRaw, ok := schema["required"].([]interface{}); ok {
		for _, fieldRaw := range requiredRaw {
			if field, ok := fieldRaw.(string); ok {
				if _, exists := input[field]; !exists {
					return StageResult{
						Proceed:   false,
						Rejection: util.Bi(fmt.Sprintf("missing required field: %s", field), fmt.Sprintf("缺少必填字段: %s", field)),
					}
				}
			}
		}
	}

	if properties, ok := schema["properties"].(map[string]interface{}); ok {
		for fieldName, propRaw := range properties {
			prop, ok := propRaw.(map[string]interface{})
			if !ok {
				continue
			}
			expectedType, _ := prop["type"].(string)
			if expectedType == "" {
				continue
			}

			val, exists := input[fieldName]
			if !exists {
				continue
			}

			if !checkType(val, expectedType) {
				return StageResult{
					Proceed:   false,
					Rejection: fmt.Sprintf("field %q type error: expected %s, got %s", fieldName, expectedType, actualType(val)),
				}
			}
		}
	}

	return StageResult{Proceed: true, Message: "Schema validation passed"}
}

// checkType checks if a value matches the expected JSON Schema type
func checkType(val interface{}, expected string) bool {
	switch expected {
	case "string":
		_, ok := val.(string)
		return ok
	case "integer":
		f, ok := val.(float64)
		return ok && f == float64(int(f))
	case "number":
		_, ok := val.(float64)
		return ok
	case "boolean":
		_, ok := val.(bool)
		return ok
	case "array":
		_, ok := val.([]interface{})
		return ok
	case "object":
		_, ok := val.(map[string]interface{})
		return ok
	default:
		return true
	}
}

// actualType returns the actual type name of a value
func actualType(val interface{}) string {
	switch val.(type) {
	case string:
		return "string"
	case float64:
		return "number"
	case bool:
		return "boolean"
	case []interface{}:
		return "array"
	case map[string]interface{}:
		return "object"
	case nil:
		return "null"
	default:
		return "unknown"
	}
}

// --- Stage 2: Custom Validation ---

// CustomValidateStage is the custom validation stage
type CustomValidateStage struct {
	workDir string
}

// Name returns the stage name
func (s *CustomValidateStage) Name() string {
	return "custom_validate"
}

// Execute performs custom validation (path traversal detection, binary file check, workspace boundary check)
//
// G5：改为遍历 `state.WriteTargets()`（多目标工具如 apply_unified_diff
// 一段补丁含 N 个文件）。**对单文件工具行为严格不变** —— WriteTargets 在 TargetPaths
// 为空时返回 [FilePath]，与旧实现的唯一目标完全一致；旧实现里"没有 filePath 就跳过"
// 也由 WriteTargets 返回 nil 保留。任一目标不过即整体拒绝（前置、零副作用）。
func (s *CustomValidateStage) Execute(ctx context.Context, call Call, state *PipelineState) StageResult {
	targets := state.WriteTargets()
	if len(targets) == 0 {
		return StageResult{Proceed: true, Message: "no file path, skipping custom validation"}
	}

	for _, filePath := range targets {
		if strings.Contains(filePath, "..") {
			cleaned := filepath.Clean(filePath)
			if strings.Contains(cleaned, "..") {
				return StageResult{
					Proceed:     false,
					Rejection:   fmt.Sprintf("path traversal detection: path %q contains ..", filePath),
					FailureKind: FailureKindSandboxDenied,
				}
			}
		}
	}

	if isWriteTool(call.Name) {
		for _, filePath := range targets {
			// 这里是**拒绝闸**：只有"已证明越界"才拒。
			// "未知"（工作区未配置 / 路径无法解析）**不当作越界** ⇒ 不在前置阶段拒绝，
			// 而是继续往下走，最终由 PermissionStage 的免问闸要求确认（见 BoundaryVerdict：
			// 未知既不豁免也不拒绝，收敛到"问用户"）。
			if v, reason := checkWorkspaceBoundary(filePath, s.workDir); v.ProvablyOutside() {
				return StageResult{
					Proceed:     false,
					Rejection:   reason,
					FailureKind: FailureKindSandboxDenied,
				}
			}
		}

		for _, filePath := range targets {
			if isBinaryFile(filePath) {
				return StageResult{
					Proceed:   false,
					Rejection: fmt.Sprintf("binary file check: path %q may be a binary file", filePath),
				}
			}
		}
	}

	return StageResult{Proceed: true, Message: "custom validation passed"}
}

// BoundaryVerdict 是「目标路径相对工作区」的**三值**判定（· 2026-09-19）。
//
// 为什么不是 bool：改前该判据返回裸 `bool`，并把两件性质不同的事压进了同一个 `true` ——
// - 「**已证明**落在工作区内」（可以据此免问）
// - 「**根本没有判定**」（workDir 为空 / 路径无法解析）
//
// 于是 `if ok { ...免问... }` 这种写法在"没有判定"时会**静默得到"在工作区内"**。
// 这正是本仓库反复出现的失效形状：fail-open 的读法长在调用点上，而没有任何类型约束
// 阻止它被写出来（同类形状见 `validator.isDangerousWriteTarget` 的教训）。
//
// ⇒ 把「不可判定」提升为**一等状态**，并让它做**零值**。这样任何未显式处理的 verdict
//
//	都落在 `BoundaryUnknown`，即安全侧。判据仍然只有一个，但「未知该往哪边倒」
//	由**调用点显式声明**（见 ProvablyInside / ProvablyOutside）—— 这就是本类型存在的全部意义。
type BoundaryVerdict int

const (
	// BoundaryUnknown 表示**无法判定**（工作区未配置、或路径无法解析成绝对路径）。
	// ★ 刻意取零值：让"忘记处理"默认落在安全侧，而不是默许。
	BoundaryUnknown BoundaryVerdict = iota
	// BoundaryInside 表示**已证明**目标落在工作区内。
	// 只有这一种状态可以作为「免问」的依据（见 workspaceContainedWrite）。
	BoundaryInside
	// BoundaryOutside 表示**已证明**目标逃逸工作区。
	// 只有这一种状态可以作为「拒绝」的依据（见 CustomValidateStage / apply_unified_diff）。
	BoundaryOutside
)

// ProvablyInside 报告是否**已证明**在边界内 —— 唯一可作为「免问」依据的状态。
func (v BoundaryVerdict) ProvablyInside() bool { return v == BoundaryInside }

// ProvablyOutside 报告是否**已证明**逃逸 —— 唯一可作为「拒绝」依据的状态。
//
// 与 ProvablyInside 的**不对称性是有意的**，别把它"修统一"：
// - 免问需要**证明在内部**（举证责任在豁免方 —— 放宽一寸就是静默放行）；
// - 拒绝需要**证明在外部**（不拿"不知道"当"越界"去拒用户 —— 那会在工作区未配置时
// 让所有写操作直接失败，把一个配置问题变成功能不可用）。
//
// ⇒ 「未知」既不豁免也不拒绝，顺着管线落到 PermissionStage，在那里**问用户**。
// 三条出口全部收敛到「问」这一侧：既没有静默放行，也没有把未知当越界。
func (v BoundaryVerdict) ProvablyOutside() bool { return v == BoundaryOutside }

// checkWorkspaceBoundary 判定 path 相对 workDir 的位置（**全仓唯一的路径包含判据**）。
//
// 为什么提取出来而不是让两侧各写一遍：写类工具的目标必须留在工作区内，这条不变量
// 现在需要三处把关 —— 管线 CustomValidateStage（前置、零副作用）、
// apply_unified_diff handler（底线，覆盖 Pipeline==nil 回退和"管线没发现的路径"）、
// 以及 PermissionStage 的免问闸。三处各写一份 rel/前缀判断必然漂移
// （本仓库已有 4 份工具清单漂移的教训），且漂移方向通常是"漏检"。
//
// ★ 变更：返回值由裸 `bool` 改为 `BoundaryVerdict`。
// 「workDir 为空」与「路径无法解析」不再伪装成"在工作区内"，而是显式报 `BoundaryUnknown`
// —— 于是**三种调用点必须各自声明**对"未知"的处置，无法再靠 `if ok` 蒙过去。
// 各调用点的处置及其理由见 BoundaryVerdict 的文档。
//
// ★ 第 7 批变更（2026-09-19 · 交叉领域收敛）：**机制**改为委托 `platform`
// （`resolveValidationPath` → `platform.ResolveExistingParent`，
// 包含判定 → `platform.IsWithinResolved`），本函数只保留**策略**（三值映射）。
// 收敛前的实现是本仓最后一份「自建 resolver + filepath.Rel/HasPrefix」，
// 与 filehistory / permission / powershell / bash 各写一份——正是"这里一套、那里一套"。
//
// 新文件本身通常不存在，因此解析最近的已存在祖先目录 ——
// 这同时处理 macOS /var -> /private/var 别名和祖先 symlink 逃逸。
func checkWorkspaceBoundary(path, workDir string) (BoundaryVerdict, string) {
	if workDir == "" {
		// 改前此处返回 (true, "")，即"处处都算工作区内"。
		return BoundaryUnknown, "workspace boundary check: workspace root is unknown"
	}
	absPath, pathErr := resolveValidationPath(path)
	absWorkDir, workDirErr := resolveValidationPath(workDir)
	if pathErr != nil || workDirErr != nil {
		// 改前此处同样返回 (true, "")。
		return BoundaryUnknown, "workspace boundary check: path could not be resolved"
	}
	// 唯一的包含判定（分隔符边界 + 仅在大小写不敏感文件系统上折叠大小写）。
	if !platform.IsWithinResolved(absPath, absWorkDir) {
		return BoundaryOutside, fmt.Sprintf("workspace boundary check: path %q is not within workspace %q", path, workDir)
	}
	return BoundaryInside, ""
}

// resolveValidationPath 解析路径及其最近的已存在祖先目录。
// 目标不存在时仍保留未解析的尾部路径，确保新文件写入和 symlink 边界判断一致。
//
// ★ 算法已收敛到 `platform.ResolveExistingParent`（全仓唯一实现）；
// 本函数只负责 pipeline 侧的**入参约定**：先 Abs 成绝对路径（包含判定需要绝对路径），
// 并在解析失败时保留收敛前的容错行为（退回 Clean(abs)），使本次改动是
// **纯机制收敛、零策略变化**。
func resolveValidationPath(path string) (string, error) {
	abs, err := filepath.Abs(path)
	if err != nil {
		return "", err
	}
	resolved, rerr := platform.ResolveExistingParent(abs)
	if rerr != nil {
		return filepath.Clean(abs), nil
	}
	return resolved, nil
}

// isWriteTool 报告 name 是否为「目标受限写类工具」。
//
// ★ 2026-09-20 批 C：本函数由**本地第二张名单**改为 `permission.WriteToolSet` 的**薄转发**。
// 收敛前本包与 `permission` 各有一份、且对 `bash` 的答案**相反**（两份各自的测试还各自钉死了
// 相反答案）—— 详见 `permission/safety.go` 里 `WriteToolSet` 的长注释。
// ⇒ 判定本体只剩一处；本包装只保留，是为了让包内 4 个调用点（边界闸 / 出口 (a) 免问 /
// 两个自指分流点）读起来语义不变。
// ★ 不得在此重新长出第二张名单 —— 那正是本次要修的 B1 第二半。
func isWriteTool(name string) bool {
	return permission.IsWriteTool(name)
}

// --- Stage 3: Pre-Hook ---

// PreHookStage is the pre-hook stage
type PreHookStage struct {
	hookMgr *hook.Manager
}

// Name returns the stage name
func (s *PreHookStage) Name() string {
	return "pre_hook"
}

// Execute runs pre-hooks
func (s *PreHookStage) Execute(ctx context.Context, call Call, state *PipelineState) StageResult {
	if s.hookMgr == nil {
		return StageResult{Proceed: true, Message: "no hook manager, skipping"}
	}

	result := s.hookMgr.RunPreHooks(call.Name, call.Input)

	// If hook provided modified input, pass it to pipeline state
	if result.UpdatedInput != nil {
		state.UpdatedInput = *result.UpdatedInput
	}

	// 桶 X3 病灶2：细分生命周期 hook 触发（修复生产零调用）
	// 修复前：PreToolUse 是唯一触发点，PreBash/PreRead/PreWrite/PreEdit/PreGit/PreWeb 等细分 hook 从未在生产路径触发。
	// 修复后：在 PreToolUse 通过后，根据工具类型额外触发对应细分 hook（block 时整体阻断）。
	// 细分 hook 与 PreToolUse 是不同事件类型，用户在不同 hook 列表注册，不会重复执行同一 hook。
	if result.Action != hook.ActionBlock {
		if blockReason := s.runPreToolSpecificHooks(call); blockReason != "" {
			return StageResult{
				Proceed:   false,
				Rejection: blockReason,
			}
		}
	}

	switch result.Action {
	case hook.ActionBlock:
		reason := "pre-hook blocked"
		if result.Reason != "" {
			reason = result.Reason
		}
		return StageResult{
			Proceed:   false,
			Rejection: reason,
		}
	case hook.ActionAsk:
		return StageResult{
			Proceed:     true,
			NeedConfirm: true,
			Message:     result.Reason,
		}
	default:
		msg := "pre-hook passed"
		if result.Output != "" {
			msg = fmt.Sprintf("pre-hook passed, command output: %s", truncateStr(result.Output, 200))
		}
		if result.UpdatedInput != nil {
			msg = fmt.Sprintf("pre-hook passed (input modified), command output: %s", truncateStr(result.Output, 200))
		}
		if result.Error != nil {
			msg = fmt.Sprintf("pre-hook command execution error (non-blocking): %v", result.Error)
		}
		return StageResult{Proceed: true, Message: msg}
	}
}

// runPreToolSpecificHooks 根据工具类型触发对应细分 Pre hook（桶 X3 病灶2 接入点）。
//
// 细分 hook 与 PreToolUse 互补：PreToolUse 是总入口，细分 hook 提供按工具类型的事件语义。
// 例如用户可在 PreBash 注册专属 matcher="bash" 的 hook（如禁止特定命令模式）。
// 任一细分 hook 返回 block 则整体阻断（fail-closed 语义）。
//
// 返回非空字符串表示 block 原因；空字符串表示通过。
func (s *PreHookStage) runPreToolSpecificHooks(call Call) string {
	if s.hookMgr == nil {
		return ""
	}
	name := call.Name
	switch name {
	case "bash":
		// bash 工具：触发 PreBash hook，传入 command 字段
		command := pipelineExtractField(call.Input, "command")
		result := s.hookMgr.RunPreBashHooks(command)
		if result.Action == hook.ActionBlock {
			reason := "pre-bash hook blocked"
			if result.Reason != "" {
				reason = result.Reason
			}
			return reason
		}
	case "read_file":
		filePath := pipelineExtractField(call.Input, "path")
		if filePath == "" {
			filePath = pipelineExtractField(call.Input, "file_path")
		}
		result := s.hookMgr.RunPreReadHooks(filePath)
		if result.Action == hook.ActionBlock {
			reason := "pre-read hook blocked"
			if result.Reason != "" {
				reason = result.Reason
			}
			return reason
		}
	case "write_file":
		filePath := pipelineExtractField(call.Input, "path")
		if filePath == "" {
			filePath = pipelineExtractField(call.Input, "file_path")
		}
		result := s.hookMgr.RunPreWriteHooks(filePath)
		if result.Action == hook.ActionBlock {
			reason := "pre-write hook blocked"
			if result.Reason != "" {
				reason = result.Reason
			}
			return reason
		}
	case "edit_file":
		filePath := pipelineExtractField(call.Input, "path")
		if filePath == "" {
			filePath = pipelineExtractField(call.Input, "file_path")
		}
		result := s.hookMgr.RunPreEditHooks(name, filePath)
		if result.Action == hook.ActionBlock {
			reason := "pre-edit hook blocked"
			if result.Reason != "" {
				reason = result.Reason
			}
			return reason
		}
	case "git_diff", "git_status", "git_log", "git_add", "git_commit", "git_branch":
		operation := strings.TrimPrefix(name, "git_")
		result := s.hookMgr.RunPreGitHooks(operation)
		if result.Action == hook.ActionBlock {
			reason := "pre-git hook blocked"
			if result.Reason != "" {
				reason = result.Reason
			}
			return reason
		}
	case "WebFetch", "WebSearch":
		url := pipelineExtractField(call.Input, "url")
		if url == "" {
			url = pipelineExtractField(call.Input, "query")
		}
		result := s.hookMgr.RunPreWebHooks(url)
		if result.Action == hook.ActionBlock {
			reason := "pre-web hook blocked"
			if result.Reason != "" {
				reason = result.Reason
			}
			return reason
		}
	}
	return ""
}

// --- Stage 3.3: Dangerous Command Guard（-R1 · 2026-09-20）---

// SandboxPrecheckStage 是「命令是否被沙箱结构性拒绝」在管线上的**唯一承载体**。
//
// 为什么必须是一个显式阶段（真机取证 2026-10-04）：决策链与执行链对同一条命令给出
// **相反**结论 ——
//
//	决策链（本文件各阶段旁路）：只查 `cmdrules`（危险/破坏性/需确认），
//
// **不含**命令替换 / IFS 这类沙箱结构性规则；
//
//	执行链（`bash.go:996` prepareSandbox → 经 Sandbox 门做 legacy 复检）：
//
// 经 `evaluateAppLayer` ⇒ 命中 `RuleSubstitution` 等。
//
// 后果（用户可见）：`Monitor({"command":"… $(date) …"})` 弹出确认框、用户点了「允许」，
// 执行时却被 `sandbox denied: command substitution not allowed` 否决 ——
// **用户白批准一次**，且批准账本记 `decision=approved result:success`。
//
// ★ —— 本阶段的档位语义**有意收窄**，请勿按旧版理解：
//
//	2026-10-04 的处置是「一律 `Proceed:false`，不可批准」。那次修对的是
//	**"真不可逆的命令不得被一次批准绕过"**（`rm -rf /` 与 `chmod +x` 当时共用一条通道）。
//	但它把两件不同的事一起硬拒了 ⇒ 用户真机反馈：
//	「我不想关沙箱，但 AI 改个代码还不行，允许了还会被拒绝。」
//
//	现在按**后果档**分流（档位唯一判据源 = `sandbox.VerdictTier` / `tierForRule`）：
//
// TierHardDeny（rm/dd/mkfs/format · 反向 shell · 裸 \r · 白名单未命中）
// ⇒ `Proceed:false` + `FailureKind=sandbox_denied`（与 10-04 逐字一致，**不弹框**）
// TierConfirm（命令替换 · IFS · curl/wget/chmod/chown · 网络命令 · 越界写）
// ⇒ `NeedConfirm`：用户按 Yes 后本阶段放行，且执行链**不再复检**，
// 该条命令在**沙箱外**执行 —— 与 同类 CLI `BypassSandboxFirstAttempt`
// （`core/src/tools/sandboxing.rs:274`）同义
//
//	业内对照（全文见 内部工作回执 的「沙箱拒绝分档」对标证据件 2026-10-05）：
//	同类 CLI 的固定危险名单只有 `rm -f/rm -rf/sudo`，且命中是 `Decision::Prompt`
//	（**强制询问**、批准后在沙箱外执行）；同类 CLI 有 `Allow unsandboxed fallback`。
//	两类都不存在"日常命令硬拒且不可批准"这一档。
//
// 为什么"批准"必须同时在两个阶段生效：执行链的 `prepareSandbox` 会**再查一次**
// 沙箱（`bash.go:996`），而 handler 签名看不到 `Call.Approved`
// ⇒ 批准事实经 **context**（`sandbox_sanction.go`）传给执行链。
//
// ⚠️ 归一化口径：本阶段用**原始命令**（`call.Input` 的 command 字段）调 Classify/Validate，
// 与执行链 `bash.go:996` 逐字一致 —— 这样才能**准确预测**执行结局。
// （审批侧 `security.CheckCommand` 走 `normalizeForCheck` 是另一条链，不在本阶段内。）
//
// ⚠️ 单侧收窄纪律保持：本阶段只回答"要不要问 / 能不能做"，权限层本来就会拒的调用
// 一律让路（见下方 `deferring to it` 出口）。
//
// R5（死代码声明）：`sandbox == nil || !Enabled()` 分支在生产**不执行** ——
// `executor.go:245` 恒建 Sandbox，`lifecycle.go:435` 在 TUI 路径恒 `SetEnabled(true)`。
// 该分支只为与执行链口径对齐（执行链同样在未启用时放行）与测试可构造性而保留。
type SandboxPrecheckStage struct {
	sandbox *sandbox.Sandbox
	policy  *permission.Policy
}

// Name 返回阶段名称。
func (s *SandboxPrecheckStage) Name() string { return "sandbox_precheck" }

// Execute 在**询问用户之前**用与执行链同源的裁决者检查命令。
//
// 只对「命令载体工具」（`permission.IsCommandBearingTool`，唯一声明在 `capability.go`；
// 集合由 `CapExec` 桶派生 = {bash, monitor}）生效；按后果档分流（见类型注释）：
// - 硬拒档 ⇒ 终局拒绝（`FailureKind=sandbox_denied`），标 `ConfirmSource=SourcePipeline`
// （用户在语义上从未被询问）；
// - 可批准档 ⇒ `NeedConfirm`；用户在确认框按 Yes 后带 `Approved=true` 重跑本阶段，
// 此时放行（批准是**逐条**的，不写盘、不记忆）。
func (s *SandboxPrecheckStage) Execute(ctx context.Context, call Call, state *PipelineState) StageResult {
	if s.sandbox == nil || !s.sandbox.Enabled() {
		return StageResult{Proceed: true, Message: "sandbox not enabled, skipping precheck"}
	}
	if !permission.IsCommandBearingTool(call.Name) {
		return StageResult{Proceed: true, Message: "not a command-bearing tool"}
	}
	// 单向收窄（与 DangerousGuardStage / SafetyCheckStage 同款纪律）：
	// 权限层本来就会拒的调用，不在此改写成另一种失败原因 —— 让路给它。
	if s.policy != nil {
		if allowed, _ := s.policy.Check(call.Name, call.Input); !allowed {
			return StageResult{Proceed: true, Message: "permission layer denies this call; deferring to it"}
		}
	}
	cmd := pipelineExtractField(call.Input, "command")
	if strings.TrimSpace(cmd) == "" {
		return StageResult{Proceed: true, Message: "empty command"}
	}

	// 档位与"能不能做"来自**同一引擎**（同一 Sandbox 实例、同一次判定语义），
	// 不在这里重列名单、也不从 Reason 文本反推档位（R4）。
	tier, rule, hit := s.sandbox.Classify(cmd)
	switch tier {
	case sandbox.TierHardDeny:
		reason := sandboxReason(s.sandbox, cmd)
		logPipelineRejection("sandbox_precheck", call, FailureKindSandboxDenied, reason)
		return StageResult{
			Proceed:     false,
			Rejection:   reason,
			FailureKind: FailureKindSandboxDenied,
		}

	case sandbox.TierConfirm:
		// ★ 用户的逐条批准对**可批准档**生效。
		// `!call.ApprovedByRule` 是必需的（与 DangerousGuardStage:1184 同款纪律）：
		// ApprovedByRule 表示"免问来自随仓库分发的规则"，不是用户按下 Yes
		// ⇒ 不得据此把命令放到沙箱外执行。
		if call.Approved && !call.ApprovedByRule {
			return StageResult{
				Proceed: true,
				Message: fmt.Sprintf("sandbox rule %s sanctioned by user approval (hit=%q)", rule, hit),
			}
		}
		state.NeedConfirm = true
		logPipelineConfirmRequired("sandbox_precheck", call, rule)
		return StageResult{
			Proceed:     true,
			NeedConfirm: true,
			Message: util.Bi(
				fmt.Sprintf("sandbox rule %s requires confirmation (hit=%q): approving runs this one command outside the sandbox", rule, hit),
				fmt.Sprintf("命中沙箱规则 %s（触发字符 %q）：批准后本条命令将在沙箱外执行", rule, hit),
			),
		}
	}
	return StageResult{Proceed: true, Message: "command passes sandbox precheck"}
}

// sandboxReason 取"拒绝原因"文案（保持与执行链 `Sandbox.Validate` 逐字同源）。
//
// Classify 只给档位与命中 token，不给完整 Reason；这里再取一次 Reason 是**同一个
// 单一实现**的第二次调用（不是第二套判据）。
func sandboxReason(sbx *sandbox.Sandbox, cmd string) string {
	if _, reason := sbx.Validate(cmd); reason != "" {
		return reason
	}
	// 不可达（Classify 说硬拒 ⇒ Validate 必然拒绝）；保留兜底以免文案为空。
	return "sandbox denied"
}

// DangerousGuardStage 是「危险档」在管线上的**唯一承载体**。
//
// 为什么必须是一个显式阶段（而不是让权限层的护栏兼职）：
// `PermissionStage` 的三个免问出口 (a)/(b)/(c) 都在它**内部**，只要危险档的判定
// 晚于它们，就会被吃掉 —— 这正是本轮实测到的 P0：
//
//	`shutdown -h now` × 沙箱真在强制
//
// · DestructiveCheckStage 用自己那份名单（**不含** shutdown/reboot/halt 族）⇒ 不命中
// · PermissionStage 出口 (c)「进程类 × 沙箱在强制 ⇒ 免问」⇒ 免问
// · 结果：**[execute] pass** —— 系统关机命令被静默执行
//
// 所以本阶段的定位是**结构性**的，不是加一条 if：它排在 `DestructiveCheckStage`
// **之前**并带 `NeedConfirm` ⇒ `ToolPipeline.Execute` 在 `sr.NeedConfirm` 处短路
// （见本文件 :301），后续阶段（含全部免问出口）**根本不会被调用**。
// 「危险档免疫免问」由此从"靠判断记得写"变成"靠顺序到不了"。
//
// 与权限层护栏**同一个开关**：`policy.DangerousCommandGuardrail()` 为 false
// （CI 无人值守装配）时本阶段不介入 —— 语义是"无人在环应答"，此时新增确认点
// 会让 CI 静默失败（既有约定见 ci_permission_stage2_test.go）。
type DangerousGuardStage struct {
	policy *permission.Policy
}

// Name 返回阶段名称。
func (s *DangerousGuardStage) Name() string { return "dangerous_guard" }

// Execute 判定本次调用是否命中危险档；命中则强制请求用户确认。
//
// 判据是 `cmdrules.MatchDangerous`（危险档唯一真相源）；命中后 `Rejection` 里
// 引用的是**规则 ID**，不是散文（R4：可判定的结构化事实不得由散文关键词断言）。
//
// 尊重 `call.Approved`：用户在确认框上批准后，`ExecuteWithConfirm` 会带
// `Approved=true` **重跑整条管线**（executor.go:967）——此时不再重复提问。
// 「不会因为记住而长期免问」由 `permission.Policy.Remember` 拒绝记录危险命令保证
// （见该函数注释），二者是本档语义的两半。
func (s *DangerousGuardStage) Execute(ctx context.Context, call Call, state *PipelineState) StageResult {
	if s.policy != nil && !s.policy.DangerousCommandGuardrail() {
		return StageResult{Proceed: true, Message: "dangerous command guardrail disabled by policy"}
	}
	// ★ P-002（2026-09-22 反缝补扫描）：工具面判据改为「命令载体」的**唯一声明**
	// `permission.IsCommandBearingTool`（由 CapExec 桶派生 = {bash, monitor}）。
	//
	// 修复前这里是手抄名对 `call.Name != "bash" && call.Name != "PowerShell"`：
	// 多一个幽灵名（PowerShell 从未注册）、少一个真身（monitor）⇒ **本阶段对 Monitor
	// 形同不存在**：`Monitor({"command":"shutdown -h now"}) × 沙箱在强制` 时，
	// 本阶段直接 Proceed（连 cmdrules 都不查）→ DestructiveCheckStage 也只有 `case "bash"`
	// → PermissionStage 出口 (c) 用**真 SSOT（含 monitor）**免问 ⇒ **静默执行**。
	// 即本文件 :1036-1039 记录并已修掉的 P0 换了一条路径复现。
	//
	// 为什么用 IsCommandBearingTool 而不是 IsSandboxBoundedTool（两者今天同集合）：
	// 本阶段问的是「**要不要拦**」（集合越宽越安全）；出口 (c) 问的是「**能不能免问**」
	// （集合越窄越安全）。合并会让"是命令载体但效果不只在沙箱内"的工具被漏拦
	// （taskcreate 就是这种候选）⇒ 详见 capability.go 该函数注释。
	if !permission.IsCommandBearingTool(call.Name) {
		return StageResult{Proceed: true, Message: "not a command-bearing tool"}
	}
	// ★ ：跳过条件加上 `!call.ApprovedByRule`。
	//
	// 修复前只看 `call.Approved`，而规则前置门为让 `allow` 规则生效也会置
	// `Approved` —— 于是一条**写在 `<workDir>/.whale/settings.json` 里、随仓库
	// 分发**的 `allow` 规则即可让危险档确认被跳过。那不是"用户批准"，是
	// "别人提供的文件替用户做了决定"。
	//
	// 用户显式批准（`ExecuteWithConfirm` 带 Approved 重跑）仍然跳过 —— 否则
	// 批准后重跑会再次落到本分支并再次要求确认，形成死循环。
	if call.Approved && !call.ApprovedByRule {
		return StageResult{Proceed: true, Message: "dangerous command previously approved"}
	}
	// ★ 单向收窄：本阶段只**增加**「必须问」，绝不改变「能不能做」。
	//
	// 若权限层本来就会拒绝（典型：`read-only` 档下的写类命令），**不要**在此把它提前
	// 变成一次"可批准的询问" —— 那会把一个硬边界软化成弹窗，方向与豁免相反、
	// 是**放宽**。让拒绝在 PermissionStage 照常发生（本阶段让路）。
	// 这与文件内其它出口共用的原则一致：任何新判定都只能是单向收窄。
	if s.policy != nil {
		if allowed, _ := s.policy.Check(call.Name, call.Input); !allowed {
			return StageResult{Proceed: true, Message: "permission layer denies this call; deferring to it"}
		}
	}
	cmd := pipelineExtractField(call.Input, "command")
	if strings.TrimSpace(cmd) == "" {
		return StageResult{Proceed: true, Message: "empty command"}
	}
	pat, hit := cmdrules.MatchDangerous(cmd)
	if !hit {
		return StageResult{Proceed: true, Message: "no dangerous command detected"}
	}
	state.NeedConfirm = true
	logPipelineConfirmRequired("dangerous_guard", call, pat.ID)
	// ★ ：确认弹窗的 Risk 行按双语形态 SSOT 拼接 ——
	// 英文主行可独立读懂（rule ID + 英文理由），中文辅行缩进另起一行。
	// cmdrules 是 stdlib-only 叶子包（不 import util）⇒ 只出 Note/NoteZH 数据，
	// 形态在本层（展示侧）拼，避免把 util 依赖灌进数据层。
	return StageResult{
		Proceed:     true,
		NeedConfirm: true,
		Message: util.Bi(
			fmt.Sprintf("dangerous command requires confirmation: rule=%s (%s)", pat.ID, pat.Note),
			fmt.Sprintf("危险命令需要确认：规则=%s（%s）", pat.ID, pat.NoteZH),
		),
	}
}

// --- Stage 3.4: Destructive Command Check (桶 X6 P-4) ---

// destructiveGitOps 是需要用户确认的破坏性 git 操作集合。
// 仅检查可能造成数据丢失的操作（reset --hard, clean -f, push --force 等）。
var destructiveGitOps = map[string]bool{
	"reset --hard":            true,
	"clean -f":                true,
	"clean -fd":               true,
	"clean -xfd":              true,
	"push --force":            true,
	"push -f":                 true,
	"push --force-with-lease": true,
}

// DestructiveCheckStage 是破坏性操作检测阶段（桶 X6 P-4）。
// 在 PreHook 之后、SafetyCheck 之前执行，检测 bash/git/powershell 等工具
// 的破坏性操作，检测到则设置 NeedConfirm=true 请求用户确认。
//
// 契约（A2 · 2026-09-16 更正，原注释写「不直接拒绝（Proceed=true +
// NeedConfirm=true），由 PermissionStage/上层决定是否放行」——**与实现相反**）：
// 本 stage 的 `Proceed=true` 只表示「本 stage 不独自拒绝」，
// **不表示「管道放行」**。`ToolPipeline.Execute` 的循环按 `sr.NeedConfirm`
// 短路（pipeline.go:287）⇒ 只要本 stage 带 NeedConfirm，管道**立即停止**，
// 统一返回 `{Proceed:false, NeedConfirm:true}`，PermissionStage **不会**再被调用。
// 真正的「是否放行」由用户在确认框上做。
type DestructiveCheckStage struct {
	// policy 仅用于「单向收窄」判定（见 Execute 末尾）：权限层本来就会拒的调用，
	// 本阶段不得把它提前变成一次"可批准的询问"。为 nil 时该判定不生效
	// （直接构造阶段对象的既有测试因此保持原行为；生产装配由 NewToolPipeline 注入）。
	policy *permission.Policy
}

// Name 返回阶段名称。
func (s *DestructiveCheckStage) Name() string {
	return "destructive_check"
}

// Execute 执行破坏性操作检测。
// 检测策略：
// - bash 工具：提取 command 字段，交给 cmdrules.MatchDestructive 走命令位判定
// （按 shell 分隔符逐段取命令位；含 PowerShell 破坏性 cmdlet 子串匹配）
// - git_* 工具：检查操作参数是否匹配 destructiveGitOps
// - exit_worktree 工具：action=remove 时标记为破坏性
// - powershell 命令（bash 内）：匹配 cmdrules.DestructiveCmdlets
//
// ⚠️ 与 DangerousGuardStage 的分工（-R1）：危险档（不可逆/灾难级，如 rm -rf /、
// shutdown）由**前一个阶段**判定并短路，根本到不了这里；本阶段负责的是
// **破坏性档**（有副作用、需确认，但不属灾难级）。两档的名单都来自 `cmdrules`。
//
// 检测到破坏性操作时返回 `{Proceed:true, NeedConfirm:true}`。
// ⚠️ **这会阻断管道**（A2 · 2026-09-16：原注释写「不阻断管道」，与
// pipeline.go:287 的实际行为相反）：`Execute` 循环一旦看到 `sr.NeedConfirm`
// 就立即返回 `{Proceed:false, NeedConfirm:true}`，后续阶段不再执行。
// 本 stage 同置 `state.NeedConfirm`，那是给 PermissionStage 读的可变通道；
// 但因为本函数总是同步带上 `StageResult.NeedConfirm`，PermissionStage 在
// 破坏性命令上**实际不会被执行**（该通道今天的唯一实际读点是「无人在此写」）。
func (s *DestructiveCheckStage) Execute(ctx context.Context, call Call, state *PipelineState) StageResult {
	// ★ ：与 DangerousGuardStage 同一条修订 —— 规则推导的
	// 免问（`ApprovedByRule`）**不得**跳过破坏性确认。理由与逐条论证见
	// `tools.Call.ApprovedByRule`；两处的判据必须逐字一致（同一不变量的两个承载体）。
	if call.Approved && !call.ApprovedByRule {
		return StageResult{Proceed: true, Message: "destructive operation previously approved"}
	}
	reasonEN, reasonZH := checkDestructive(call.Name, call.Input)
	if reasonEN == "" {
		return StageResult{Proceed: true, Message: "destructive check passed"}
	}
	// ★ 单向收窄（-R1 · 与 DangerousGuardStage 同一条原则）：
	// 权限层本来就会拒绝的调用，本阶段**不得**把它提前变成一次"可批准的询问"。
	//
	// 为什么这不是"顺手加一层"：`read-only` 档的**契约**就是「只读放行，其余**拒绝**」
	// （见 permission.checkModeLocked 的三档注释）。本阶段排在 PermissionStage 之前
	// 且带 NeedConfirm 会短路管道（:301），于是 `read-only` 档下的 `rm -rf x`
	// 一直是**询问**而非拒绝 —— 用户可以在只读档下批准一次写操作，硬边界被软化成了弹窗。
	// 修法不是改判定，而是**让路**：把"能不能做"还给权限层，本阶段只负责"要不要问"。
	if s.policy != nil {
		if allowed, _ := s.policy.Check(call.Name, call.Input); !allowed {
			return StageResult{Proceed: true, Message: "permission layer denies this call; deferring to it"}
		}
	}
	state.NeedConfirm = true
	// ★ ：确认弹窗的 Risk 行按双语形态 SSOT 拼接 ——
	// 英文主行可独立读懂（检测结论 + 英文理由），中文辅行缩进另起一行。
	// checkDestructive 出 (en, zh) 两侧而非预拼串：本层还要在两侧外面各包一层
	// 框架文案，预拼的串无法再拆（拆字符串 = 把形态事实降级成书写噪声）。
	return StageResult{
		Proceed:     true,
		NeedConfirm: true,
		Message: util.Bi(
			fmt.Sprintf("destructive operation detected: %s (user confirmation required)", reasonEN),
			fmt.Sprintf("检测到破坏性操作：%s（需要用户确认）", reasonZH),
		),
	}
}

// checkDestructive 检测工具调用是否包含破坏性操作。
// 返回 (英文主行, 中文辅行)；两侧均为空表示未检测到。
// 按工具类型分支检测，避免导入 bash/powershell 包造成循环依赖。
func checkDestructive(toolName, input string) (string, string) {
	// ★ P-002（2026-09-22 反缝补扫描）：`case "bash":` → 「命令载体」的**唯一声明**
	// `permission.IsCommandBearingTool`（由 CapExec 桶派生 = {bash, monitor}）。
	//
	// 这是 ★S-1 的**第二半**：危险档（DangerousGuardStage）只管"灾难级"（shutdown / rm -rf /…），
	// `rm -rf build` 这类属**破坏性档**、由本函数负责。修复前 Monitor 落 default ⇒ 不命中
	// → 随后 PermissionStage 出口 (c) 按**真 SSOT** 免问 ⇒ 破坏性命令**零确认执行**。
	// 与下方 checkBashDestructive 注释里记的 bash 旧缺口**同源**（豁免前提在"另一条路径"上失效）。
	if permission.IsCommandBearingTool(toolName) {
		command := pipelineExtractField(input, "command")
		if command == "" {
			return "", ""
		}
		return checkBashDestructive(command)
	}
	switch toolName {
	case "git_commit":
		// git_commit 本身不破坏性（仅提交暂存区），跳过
		return "", ""
	case "git_branch":
		// git_branch 的 checkout 操作可能丢弃工作区改动，但不直接删除文件
		return "", ""
	default:
		// 检查 exit_worktree action=remove
		if toolName == "exit_worktree" {
			action := pipelineExtractField(input, "action")
			if action == "remove" {
				return "exit_worktree with action=remove (deletes worktree directory and branch)",
					"exit_worktree action=remove（删除 worktree 目录与分支）"
			}
		}
		return "", ""
	}
}

// checkBashDestructive 检测 bash 命令字符串中的破坏性操作。
//
// ★ B2 收尾：命令位与 PowerShell cmdlet 的判定**整段交给**
// `cmdrules.MatchDestructive`（该判定的唯一声明处），本函数不再自带第二套
// "首 token 精确匹配 + 本层只读名单视图"。
//
// 为什么必须换（不是风格问题，是一处**真实缺口**，方向为漏判）：
// 旧实现只按 `\n` 切行、只取首个 token ⇒
// - `echo hi ; rm -rf build`：首 token 是 `echo` ⇒ **判不出破坏性** ⇒ 本阶段放行。
// 随后权限层按 AnalyzeBashCommand 判 destructive（needConfirm=true），
// 但在「沙箱真在强制的 WW 档」下被出口 (c) 免问 ⇒ **整条命令零确认执行**。
// 形态与 同源：豁免的前提在"组合命令"上失效。
// - `/bin/rm -rf x`：旧实现不剥路径前缀 ⇒ 漏判；cmdrules 会剥。
// - `sudo -u root rm -rf x`：旧实现只跳 `sudo` 不跳其选项 ⇒ 漏判；cmdrules 会跳包装命令与选项。
//
// cmdrules.commandHeads 按 shell 分隔符逐段取命令位，并处理前置环境赋值、包装命令、路径前缀
// ⇒ 同一条判据在**更强的解析**上执行，方向恒为"更严"（不含任何放宽）。
//
// 保留在本函数内的唯一判定：git 子命令的破坏性参数（reset --hard / clean -f / push --force…）。
// cmdrules 无对应声明，且它属"命令名不破坏、参数破坏"的另一类 ⇒ 未收口项（见回执"未闭合"）。
// ★ ：返回 (英文主行, 中文辅行) 两侧，**不**返回预拼的双语串 ——
// 调用方（DestructiveCheckStage）还要在两侧外面各包一层框架文案
// （"destructive operation detected: …"），预拼的串无法再拆。
// 形态拼接统一走 util.Bi（全仓 SSOT），本函数不自己拼 "\n" + 缩进。
func checkBashDestructive(command string) (string, string) {
	// 按行分割（多行命令逐行检查）
	lines := strings.Split(command, "\n")
	for _, line := range lines {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		// (1) 命令位 / cmdlet 判定 —— 唯一声明在 cmdrules.MatchDestructive。
		if pat, ok := cmdrules.MatchDestructive(line); ok {
			return fmt.Sprintf("bash %s (%s)", pat.Value, pat.Note),
				fmt.Sprintf("bash %s（%s）", pat.Value, pat.NoteZH)
		}
		// (2) git 子命令 + 破坏性参数（cmdrules 无对应声明，保留在 tools 层）
		if reason := destructiveGitSubcommand(line); reason != "" {
			// git 理由本身是语言中性的命令文本（如 "git reset --hard"）⇒ 两侧相同
			return reason, reason
		}
	}
	return "", ""
}

// destructiveGitSubcommand 检测 `git <子命令> <破坏性参数>` 形态（如 git reset --hard）。
// 返回非空字符串表示命中；空字符串表示未命中。
//
// 与 checkBashDestructive 的关系：这是同一函数的私有拆分，不是第二份实现 ——
// git 破坏性参数的名单仍只有 `destructiveGitOps` 一处声明。
func destructiveGitSubcommand(line string) string {
	tokens := strings.Fields(line)
	idx := 0
	// 跳过 sudo 前缀（与 cmdrules 的包装命令处理保持同向；此处只处理 sudo，够用且不改行为）
	if idx < len(tokens) && tokens[idx] == "sudo" {
		idx++
	}
	if idx >= len(tokens) || tokens[idx] != "git" || idx+1 >= len(tokens) {
		return ""
	}
	subCmd := strings.Join(tokens[idx+1:], " ")
	for op := range destructiveGitOps {
		if strings.Contains(subCmd, op) {
			return fmt.Sprintf("git %s", op)
		}
	}
	return ""
}

// --- Stage 3.5: Safety Path Check ---

// SafetyCheckStage is the safety path check stage
// Validates protected paths before permission check
type SafetyCheckStage struct {
	workDir string
	// policy 仅用于「单向收窄」判定（见 Execute 末尾），理由与 DestructiveCheckStage 同：
	// 本阶段排在 PermissionStage 之前且带 NeedConfirm 会短路管道，
	// 于是 `read-only` 档下"往受保护路径写"一直是**询问**而非拒绝。
	// 为 nil 时该判定不生效（直接构造阶段对象的既有测试保持原行为）。
	policy *permission.Policy
}

// Name returns the stage name
func (s *SafetyCheckStage) Name() string {
	return "safety_check"
}

// Execute checks whether the target path is protected
func (s *SafetyCheckStage) Execute(ctx context.Context, call Call, state *PipelineState) StageResult {
	if !permission.IsWriteTool(call.Name) {
		return StageResult{Proceed: true, Message: "non-write tool, skipping safety path check"}
	}
	if call.Approved {
		return StageResult{Proceed: true, Message: "path safety warning previously approved"}
	}

	targetPath := permission.ExtractFilePath(call.Input)

	result := permission.CheckPathSafety(targetPath, s.workDir)
	if result == nil || !result.IsProtected {
		return StageResult{Proceed: true, Message: "path safety check passed"}
	}

	// ★ 单向收窄（-R1）：权限层本来就会拒绝的调用，不得在此被软化成一次询问。
	if s.policy != nil {
		if allowed, _ := s.policy.Check(call.Name, call.Input); !allowed {
			return StageResult{Proceed: true, Message: "permission layer denies this call; deferring to it"}
		}
	}

	return StageResult{
		Proceed:     true,
		NeedConfirm: true,
		Message:     fmt.Sprintf("security warning: %s", result.Reason),
	}
}

// --- Stage 4: Permission Decision ---

// EnforcementStatus 报告「OS 级边界强制**是否真的在生效**」（· 2026-09-19）。
//
// 为什么需要这一问：审批判据从「工具声明了多高的能力档位」改成「**这一次调用**
// 是否能被兜住」之后，三问里的第三问 ——「强制机制真的生效吗」—— 必须有事实来源。
// 该事实的持有者是 `internal/sandbox`（`IsolationStatusOf` 早已算出平台/特性），
// 但历史上**只喂给启动 banner**，审批层拿不到 ⇒ 本接口就是那条断线的接线端。
//
// fail-closed 契约：未注入（nil）或返回 false 一律视为**不可强制**，
// 此时 PermissionStage 退回「问」的行为 —— 与 同类 CLI `assess_patch_safety`
// 同语义（core/src/safety.rs:78-98：只有真能强制才 AutoApprove，否则 AskUser）。
//
// 生产实现：`sandbox.EnforcementActive`（唯一判据）经 `Executor.SetSandboxMgr`
// 派生注入 —— 谁来兜住进程，谁才决定"沙箱内免问"这条豁免能不能开。
type EnforcementStatus interface {
	// OSEnforcementActive 报告 OS 级沙箱当前是否真的能把进程关在边界内。
	OSEnforcementActive() bool
}

// enforcementActive 是豁免路径上的 fail-closed 读取：nil ⇒ 不可强制。
func enforcementActive(es EnforcementStatus) bool {
	return es != nil && es.OSEnforcementActive()
}

// PermissionStage is the permission decision stage
type PermissionStage struct {
	policy      *permission.Policy
	approvalMgr *approval.Manager // 可为 nil，向后兼容；非 nil 时在 needConfirm 路径接管决策
	// enforcement 报告「OS 级沙箱是否真的在强制」（可为 nil = 不可强制）。
	//
	// 为什么这一层需要它：出口 (c)（进程类 × 沙箱真在强制 ⇒ 免问）的前提事实
	// 不在权限层 —— `permission.Policy` 既没有 workDir 也没有沙箱状态；而沙箱的
	// 真实生效状态由 `internal/sandbox` 持有。⇒ 由装配层注入，本层只做合取判定。
	enforcement EnforcementStatus
	// workDir 是工作区根，用于判定「写类调用的目标是否全部落在工作区内」。
	//
	// 为什么这一层需要它：权限档位（permission.Policy）**只按工具名定级、不看路径**
	// （Policy 结构体里没有 workDir），所以 ww 档下写类工具恒返回 needConfirm=true ——
	// 与「写入是否逃逸工作区」无关。而「目标有没有逃逸」这一事实的**唯一持有者**是本管线
	// （CustomValidateStage 已按 checkWorkspaceBoundary 逐个认证）。因此免问判定必须落在
	// 持有事实的这一层，而不是在权限层再造一套路径判定（那会形成第二套可写根定义）。
	workDir string
}

// Name returns the stage name
func (s *PermissionStage) Name() string {
	return "permission"
}

// Execute checks tool execution permission.
// C19 fail-closed: 无权限策略时默认拒绝（对标同类 CLI TOOL_DEFAULTS），而非放行。
// 若先前阶段（如 SafetyCheckStage）已请求确认，则跳过。
func (s *PermissionStage) Execute(ctx context.Context, call Call, state *PipelineState) StageResult {
	if s.policy == nil {
		return StageResult{
			Proceed:     false,
			Rejection:   fmt.Sprintf("no permission policy configured for tool %q (fail-closed)", call.Name),
			FailureKind: FailureKindPolicyDenied,
		}
	}

	allowed, needConfirm := s.policy.CheckWithRules(call.Name, call.Input)
	if !allowed {
		if ruleFound, ruleAllowed, _ := s.policy.RuleCheck(call.Name, call.Input); ruleFound && !ruleAllowed {
			return StageResult{
				Proceed:     false,
				Rejection:   fmt.Sprintf("tool %q blocked by explicit deny rule", call.Name),
				FailureKind: FailureKindPolicyDenied,
			}
		}
		return StageResult{
			Proceed:     false,
			Rejection:   fmt.Sprintf("insufficient permissions: tool %q not allowed under current permission mode", call.Name),
			FailureKind: FailureKindPolicyDenied,
		}
	}

	// 审批免问出口：(r) 请求内复用、(0) 自指保护闸（，只改判"问"，不免问）、
	// (a) 工作区内的写类调用、(b) 编排/委派族、(c) 进程类 × 沙箱真在强制、
	// (d) 命令前缀已批准、(e) 只读命令—— 见下方各自分支。
	// ★ 顺序不变量：(r) 与 (0) 必须排在 (a)–(e) **之前**（(r) 是"同一次请求内已批准过"，
	// (0) 是"可达面指向 whale 自身 ⇒ 一律问"）⇒ (a)–(e) 全部**顺序短路**地继承 (0)，
	// 将来新增任何免问出口都必须插在本清单**末尾**（即 (0) 之后），并补一条顺序守卫。
	//
	// 第一性原理（对齐业内既有实现）：审批要拦的是**沙箱与边界管不住的副作用**，
	// 不是「这个工具声明了多高的权限档位」—— 后者是**能力声明**，与「这次调用
	// 是否危险」是正交的两件事。把前者当成后者，就会给每一个"需要写能力"的动作
	// 弹一次框。
	//
	// 两个出口都是**单向收窄**：不改变任何拒绝判定，也不放大任何能力。
	//
	// 约束：仅抑制 gate ①（权限档位）因「需要写能力」而提出的确认。
	// 前置阶段自己的确认诉求（经 `state.NeedConfirm` 传入）不受影响。
	//
	// A2（2026-09-16 更正，原注释举例「如 SafetyCheckStage 的敏感路径告警」）：
	// **该举例与事实不符** —— `SafetyCheckStage.Execute` 只返
	// `StageResult.NeedConfirm`，**从不写 `state.NeedConfirm`**。全仓
	// `state.NeedConfirm = true` 的写入点共三处函数、四个赋值：
	// DestructiveCheckStage.Execute、PermissionStage.Execute（DecisionAsk / 兜底）、
	// PermissionStage.checkOnFailure，而它们**都同时带上**
	// `StageResult.NeedConfirm: true` ⇒ `:287` 已短路，本函数在那些情形下
	// 根本不会被调用。
	//
	// ⇒ 因此下面这两处对 `state.NeedConfirm` 的读取**今天不可达**（恒为 false）。
	// 保留它们是纵深防御，但必须写清**契约**，否则下一个人会照着「SafetyCheck 走
	// state 通道」这句错注释去写新 stage：
	//
	//	stage 若用 `state.NeedConfirm` 表达确认诉求，**必须**在 StageResult 里
	//	**同步**置 `NeedConfirm: true`（并且像 DestructiveCheckStage 那样遵守
	//	`call.Approved`）。只置 state 不置 StageResult ⇒ `:287` 不短路 ⇒
	//	请求会被吞掉；此时由 pipeline.go 成功出口的 守卫 fail-closed 兜底
	//	（变成一次拒绝，而不是一次静默执行）。
	if needConfirm && !state.NeedConfirm {
		// (r) 请求内复用：本次调用与**同一次用户请求内已被用户明确批准过**的
		// 调用逐字相同（同 Name + 同 Input）⇒ 复用那次批准，不再重复询问。
		//
		// 为什么这是「同一个副作用被问两次」的正解：确认层是**逐调用**裁决的，
		// 而模型常常在一条 assistant 消息里（或同一请求的相邻两轮里）发出**逐字相同**的
		// 调用 —— 2026-09-20 实测两次：① 同一条消息里两个 `mkdir -p X`；
		// ② 「跑一下 ls -la」里模型答完又发一次同样的 `ls -la`。用户对**同一个确切输入**
		// 已经说过 Yes，第二次再问不提供任何新的决策信息。
		//
		// ★ 为什么落在本块（PermissionStage）而不是折成 `call.Approved`：本块之上还有
		// DangerousGuardStage(:123) 与 DestructiveCheckStage(:124)，二者都读
		// `call.Approved` 并因此**跳过危险确认**（:991 / :1080）。若把复用折成 Approved，
		// 危险命令会随复用完**免问** —— 突破「危险档 = 强制确认永不免问」(-R1)。
		// 而两个危险阶段排在 PermissionStage **之前**且带 `NeedConfirm` ⇒
		// ToolPipeline.Execute 在 :323 短路，本函数**根本不会被调用** ⇒
		// 危险面自动、结构性地排除在本判定之外（不依赖任何"是否是危险命令"的第二套判据）。
		//
		// 单向收窄（与 (a)/(b)/(c) 三条同款，逐条核对）：
		// - 不改任何拒绝判定：`!allowed` 的两个拒绝分支在本行**上方**；
		// - 不放宽任何上限：只把「本来要问」改判成「不问」，从不改变"能不能做"；
		// - 不覆盖危险面：见上，靠 stage 顺序到不了。
		//
		// ⚠️ 已知边界：`danger-full-access` 档下 `checkModeLocked` 直接返回 (true,false)
		// ⇒ `needConfirm` 为 false ⇒ 本块整体不进入 ⇒ 该档行为与本改动无关（本就免问）。
		if call.ReusedApproval {
			return StageResult{
				Proceed: true,
				Message: "request-scoped approval reused: identical call already approved in this request",
			}
		}

		// (0) 自指保护：本次调用的**可达面**指向 whale 自身时一律确认 ——
		// 排在本块**首位**，故下面的免问出口对「实际可达」的调用全被短路
		// （起收窄为**实际可达**：物理上改不到树的调用让路，见下方判定处）。
		//
		// 口径 = **P3（按工具可达面分流；用户 2026-09-20 05:2x 拍板）**，实现见 `selfTargetedCall`：
		// · 任意命令类（`bash`/`Monitor`）⇒ P1 `IsSelfSourceTree`（工作区**或**二进制落在自身树）；
		// · 目标受限类（效果只经由声明路径发生的写类工具）⇒ P2 `PathInSelfSourceTree`（只看目标）。
		// 判据与分流理由集中在 `permission/selfpath.go` 的头部注释（单一真相源），此处只做载体。
		//
		// 为什么不是只在 (a)/(c) 各加一个自指例外（方案 B）：B 只堵两个出口，
		// 新出口不会自动继承 —— 正是本仓库反复出现的「多入口各写各的」病。用户拍板方案 A：
		// **单一声明** + **一个载体** ⇒ 所有入口继承。
		//
		// 第一性原理：出口 (a)/(c) 都以「工作区/沙箱边界」为信任边界，而自指场景下
		// **该边界与被保护对象（whale 自己）重合** ⇒ 前提字面成立、结论不成立。
		// "沙箱已经兜住了 ⇒ 没有可供用户决策的信息"这一论断此时失效：
		// 边界内被保护的东西，正是用户需要决策的对象。
		//
		// 单向收窄（三条，与 -R1 同款）：
		// - 不改任何拒绝判定：`!allowed` 的两个拒绝分支在本行**上方** ⇒ read-only 档下
		// 写自身源码仍是 policy_denied，不会被软化成一次弹窗；
		// - 不放宽任何上限：本闸**只**把「本来免问」改判成「问」，**从不**返回免问
		// （免问仍只由下面三个出口与用户显式批准给出）；
		// - 尊重用户**显式**给出的免问：`call.Approved`（本次已批准）与 policy 层的
		// allowedTools / 审批记忆（二者让 needConfirm=false ⇒ 本块整体不进入）均不受影响。
		// ⇒「免问」只能由用户单向给出，不能由"我们以为它无害"给出。
		//
		// ⚠️ 已知边界（登记为待拍板项，本批刻意不动）：`danger-full-access` 档下
		// `checkModeLocked` 直接返回 (true,false) ⇒ 不进入本块 ⇒ 自指写/重建仍免问。
		// 该档语义是用户显式选择的"全放行且免问"；是否给它开第二条例外
		// （首例是危险命令护栏）属另一个决策，不在用户拍板范围内。
		if !call.Approved &&
			permission.CanModifyLocalState(call.Name) &&
			selfTargetedCall(call.Name, call.Input, s.workDir) {
			// ★ T3（2026-09-20 · 用户拍板）：本闸命中时**只放行「必拒」穿透**。
			//
			// 「必拒」= 已证明这次调用的**全部**写目标落在本次生效的可写根之外
			// ⇒ 沙箱必然拒掉这次写入 ⇒ 这次调用**根本跑不成** ⇒ 弹框只是白问一次
			// （用户无论答什么，结果都一样，不提供任何决策信息）。
			//
			// 「必允 / 未知」⇒ **仍然询问**：不因"目标证明在工作区内"放开自指保护。
			// 这是刻意保留的从严面 —— 自指场景下"边界内被保护的东西正是用户要决策的对象"
			// （见上文第一性原理），"已在可写根内"因此不构成免问理由。
			//
			// ★ T4（同批）：走本分支时返回的是**硬拒绝**（Proceed=false），
			// NeedConfirm 取零值 false ⇒ **不得**把硬拒绝软化成一个弹窗
			// （`-R1` 红线：`PermissionStage` 之前的确认阶段不得让路 `deferring to it`）。
			// 判别器：`.go` 断言必拒分支 NeedConfirm==false
			// 且 Rejection 非空 —— 改成弹窗必须让该断言红。
			if classifyWriteVerdict(call.Name, call.Input, s.workDir).MustDeny() {
				roots := sandbox.WritableRoots(s.workDir)
				return StageResult{
					Proceed: false,
					Rejection: fmt.Sprintf(
						"denied: target outside writable roots; self-source protection rejects without asking (%s)",
						sandbox.RootsReason(roots)),
					FailureKind: FailureKindSandboxDenied,
				}
			}
			// ★ （2026-10-08 · 用户拍板「乙」）：收窄为**实际可达** ——
			// 本次调用**物理上改不到**自身源树时，(0) 没有任何决策信息可提供
			// （三问 ①③），让路给下面的免问出口 (a)–(e)。
			// 两条让路判据与 fail-closed 方向见 `selfSourceBeyondActualReach`；
			// 判别器：`selfsource_actual_reach_test.go`（只读/沙箱兜住两个改前红判别格
			// ＋ 两个防越界守卫）。
			//
			// ⚠️ 上面的硬拒绝（MustDeny）**不让路** —— 本收窄只影响「问 vs 不问」，
			// 绝不把「拒」软化成「问」（`-R1` 红线仍然成立）。
			if !selfSourceBeyondActualReach(call.Name, call.Input, s.workDir, s.enforcement) {
				return StageResult{
					Proceed:     true,
					NeedConfirm: true,
					Message: "self-source protection: this call can reach whale's own source tree; " +
						"confirmation required before modifying local state",
				}
			}
			// 实际不可达 ⇒ 不在此拦截，落入 (a) 工作区内写 / (b) 编排族 / (c) 沙箱在强制 /
			// (d) 前缀 / (e) 只读 各出口按既有语义裁决。
		}

		// (a) 工作区内的写类调用免问。
		//
		// 对齐业内既有实现 ` assess_patch_safety`：patch 落在 writable roots 内
		// 即 `SafetyCheck::AutoApprove`（不问），只有**逃逸可写根**才 `AskUser` ——
		// 审批要拦的是「逃逸工作区」，不是「具备写能力」。
		//
		// 为什么落在这一层而不是改 permission.checkModeLocked：权限层**没有** workDir
		// （见 PermissionStage.workDir 注释），结构上无法区分「工作区内写」与「逃逸写」；
		// 而该事实的唯一持有者正是本管线（CustomValidateStage 已用同一个 checkWorkspaceBoundary
		// 逐个认证过目标）。在权限层实现必然要**再写一套路径提取 + 边界判定**，那正是本仓库
		// 反复出现的「影子判据漂移」成因。⇒ 在持有事实的那一层收口。
		if workspaceContainedWrite(call.Name, call.Input, s.workDir) {
			return StageResult{
				Proceed: true,
				Message: "workspace-contained write: confirmation not required",
			}
		}

		// (b) 编排/委派族免问（Task / Agent / Skill / TaskStop / TaskList）。
		//
		// 同一条第一性原理的另一面：审批要拦的是**副作用**，不是**能力声明**。
		// 这些工具的声明档位是 WorkspaceWrite（被委派角色的能力上限确实含写），
		// 但它们**自身不产生任何本机副作用** —— 效果 100% 由下游调用承担，而下游
		// 调用各自会独立经过本管线。⇒ 「启动编排」这一步没有可供审批的对象。
		//
		// 判据与安全论证（含"为何不扩权"的逐条依据）在
		// `permission.IsOrchestrationTool` 的文档注释里，此处不重复（单一真相源）。
		// 对标 同类 CLI：启动子 agent 不触发授权（multi_agents_spec.rs 零 approval 标记），
		// 子 agent 内部的审批走父会话且**不额外弹窗**（codex_delegate.rs:72）。
		//
		// 与 (a) 一样是**单向收窄**：只抑制 gate ①（权限档位）因「需要写能力」提出的
		// 确认诉求，不改变任何拒绝判定 —— read-only 档下这些工具仍被拒绝
		// （required=WW > RO ⇒ checkModeLocked 返回 false），deny 规则仍在更早的
		// rule_gate 短路。豁免不能绕过任何拒绝面。
		if permission.IsOrchestrationTool(call.Name) {
			return StageResult{
				Proceed: true,
				Message: "orchestration exemption: side effects are handled by downstream calls",
			}
		}

		// (c) 进程类工具免问（bash / Monitor）× OS 沙箱**真的在强制**（，2026-09-19）。
		//
		// 同一条第一性原理的第三面：审批要拦的是**沙箱与边界管不住的副作用**。
		// 进程类工具确实声明了 WorkspaceWrite 档位，但那只是**能力声明**（"这个工具能写"）；
		// 而它们产生的副作用**全部**属于可被 OS 级边界强制的东西（文件系统 + 网络 + 子进程）。
		// ⇒ 只要强制机制**确实在生效**，"你允许吗"这一问在沙箱已经替你兜住的范围内
		// 没有可供用户决策的信息 —— 弹框只是把成本转嫁给用户。
		//
		// ★ ③「强制机制真的生效吗」是**硬前提**，不是锦上添花：
		// `degradedSandbox.Restrict()` 是 **`return nil`**（零强制，os_sandbox.go:184）——
		// 配置里关掉沙箱、或机器上没有 `/usr/bin/sandbox-exec` 时走的就是这个降级实现。
		// 若略去③，在"沙箱装不上的机器"上就变成「也不问，也没人兜」= **静默放行任意命令**。
		// 对标 同类 CLI `core/src/safety.rs:78-98`：只有**真能强制**才 AutoApprove，
		// 否则一律退回 AskUser —— 同一判据，同一方向。
		//
		// 为什么网络也必须被排除在③之外：macOS seatbelt 默认 `(deny network*)`，
		// 一旦配置成 network=on，进程就能外联 ⇒ "副作用全部落在边界内"不再成立。
		// （该排除的实现唯一在 `sandbox.EnforcementActive`；本层只消费事实，不重复判定。）
		//
		// ★ 与前两个出口一致，是**单向收窄**：
		// - 不改任何拒绝判定：read-only 档仍在上方 `!allowed` 处被拒；deny 规则更早在
		// rule_gate 短路 ⇒ 豁免不能绕过任何拒绝面。
		// - 不改"不可逆仍需确认"：`DestructiveCheckStage` 排在本阶段**之前**且短路
		// （`pipeline.go:119`），`rm -rf` 这类根本到不了这里。
		// - 只抑制 gate ①（权限档位）因"需要写能力"提出的确认诉求。
		//
		// ⚠️ 与审批层的先后关系（刻意与 (a)/(b) 保持一致，写清以免被当成漏判）：
		// 本出口在 `s.approvalMgr.Check` **之前**返回 ⇒「沙箱内免问」优先于交互审批
		// 策略（含 `approval.PolicyUntrusted` 的"总是询问"）。这样是一致的 ——
		// 审批层只处理"确实存在的确认请求"，而本出口的含义正是"gate ① 没有请求要提"。
		// 用户的**显式否决权**不在审批层，而在更早的 deny 规则（rule_gate），
		// 豁免绕不过它（见 TestGuard_N040_DenyRuleBeatsSandboxExemption）。
		// ★ 遗留待确认项：是否让 PolicyUntrusted 退出全部豁免 —— 这会同时动 (a)/(b)
		// 的语义，属另一个决策，已登记为 后续项而非本轮顺手改。
		if permission.IsSandboxBoundedTool(call.Name) && enforcementActive(s.enforcement) {
			return StageResult{
				Proceed: true,
				Message: "sandbox-bounded: OS enforcement active, confirmation not required",
			}
		}

		// (d) 「允许该命令前缀」会话级免问（，2026-10-06 用户拍板）。
		//
		// 现场动机：ww 档下 gate ① 对**每一条** bash 调用都提确认（按"工具是否只读"定级，
		// 与具体命令无关）。模型连发 `sleep 60` / `sleep 70` 这类**同头**命令时，
		// 用户要反复回答同一件事，而"同一条命令换个参数"不提供任何新的决策信息。
		//
		// ★ 为什么落在这里（而不是让 `checkModeLocked` 返回 needConfirm=false）：
		// 本块**之上**是出口 (0) 自指保护 —— 它把"可达面指向 whale 自身"的调用
		// 改成"必须问"。若前缀免问在权限层就把 needConfirm 抹成 false，本块整体不进入
		// ⇒ 自指保护被**结构性绕过**（用户批准过 `seq` 前缀，就能顺带免问"动 whale
		// 自身源码"的调用）。⇒ 与 (a)/(b)/(c) 同处，**顺序短路**地继承 (0)。
		// 判别器：`prefix_exempt_test.go` 的
		// TestGuard_W022_PrefixExemptionSitsAfterSelfProtection（同一命令同一前缀，
		// 普通工作区免问、自身源码树内仍须确认）。
		//
		// 单向收窄（与 (a)/(b)/(c) 逐条同款）：
		// - 不改任何拒绝判定：`!allowed` 的两个拒绝分支在本块**上方**；
		// - 不放宽任何上限：只把"本来要问"改判成"不问"；
		// - 不覆盖危险/破坏性面：`HasCommandPrefix` 内部用 `mustConfirm` 硬挡
		// （且与 `SetDangerousCommandGuardrail(false)` 无关）；`DestructiveCheckStage` /
		// `DangerousGuardStage` 更排在本阶段**之前**并短路。
		// - 只对**单一简单命令**成立：复合/含替换/含重定向的命令根本没有头词
		// （`cmdrules.CommandHead` 返回 ""）⇒ 结构性地不在前缀范围内。
		if s.policy.HasCommandPrefix(call.Name, call.Input) {
			return StageResult{
				Proceed: true,
				Message: "command-prefix approval: the same command head was already approved for this session",
			}
		}

		// (e) 只读命令免问（，2026-10-06 用户拍板）。
		//
		// 现场动机（用户报「sleep 70 && echo final_check 一直被弹窗」）：ww 档下 gate ①
		// 对**每一条** bash 调用都提确认 —— 按「**工具**是否只读」定级（`bash` 能写 ⇒
		// 条条问），与**具体命令**无关。实测（`Policy.Check` 三档）：`ls -la` / `seq 1 9` /
		// `sleep 70 && echo final_check` 在该档**全部** `needConfirm=true`；而分类器
		// `AnalyzeBashCommand` 早已把它们判成 **`read-only`** —— 这份"只读"没接到 bash 的
		// 免问路径上（只喂风险分级 / Monitor）。
		//
		// 本出口就是把那个**已经算出来的事实**接回来：命令的**每一段**都在
		// 「任意参数下都只读」白名单内（`cmdrules.AllSegmentsReadOnly`）⇒ 免问。
		//
		// ★ 为什么是「全段」而不是复用 `MatchReadOnly`：后者是**任一段**命中即只读
		// （`cmdrules.go:304-317`）⇒ `weirdcmd && echo hi` 会被判只读 ⇒ 拿它免问就是
		// **静默放行** `weirdcmd`。免问是**放宽**方向，必须全段成立。
		// 业内同口径：同类 CLI `is_safe_command.rs:12-50` 对 `bash -lc "…"` 也是拆段后要求
		// **all** commands 都 safe 才整体判定 safe。
		//
		// ★ 为什么落在这里（与 (a)/(b)/(c)/(d) 同处、排在自指保护 (0) 的**判定处之后**）：
		// 若图省事让 `checkModeLocked` 直接返回 `needConfirm=false`，`if needConfirm {…}`
		// 整块不进入 ⇒ **出口 (0) 被结构性绕过**。后，(0) 对
		// 「物理上改不到树」的调用（全段只读 ⇒ 正是本出口的判据）已**让路**，
		// 只读命令因此能走到这里免问；能写且没兜底的调用仍被 (0) 拦在前面。
		// 判别器：`selfsource_actual_reach_test.go`
		// （TestW048_ReadOnlyInSelfTreeNotConfirmed 判别格 ＋ TestW048_WriteCommands… 防越界守卫）。
		//
		// 单向收窄（与 (a)/(b)/(c)/(d) 逐条同款）：
		// - 不改任何拒绝判定：`!allowed` 的两个拒绝分支在本块**上方**；显式 deny 规则
		// 更早在 rule_gate 短路（判别器 TestGuard_W023_DenyRuleBeatsReadOnlyExemption）；
		// - 不放宽任何上限：只把"本来要问"改判成"不问"，从不改变"能不能做"；
		// - 不覆盖危险/破坏性面：`DestructiveCheckStage` / `DangerousGuardStage` 排在本阶段
		// **之前**并短路；且白名单本身只收「任意参数下都只读」的命令名
		// （find/less/sed/git/tee/xargs 这些"参数可写"的一律不在）；
		// - 结构上排除复合危险：`AllSegmentsReadOnly` 要求**每一段**都在白名单内，
		// `ls && rm -rf x` / `echo $(rm -rf /)` / `ls > /tmp/x` 全部不成立。
		//
		// ⚠️ 只对**命令载体**工具取命令（`IsCommandBearingTool` = bash / Monitor）：
		// 其它工具的 input 里也可能出现 `"command"` 字段（`extractCommand` 只按 JSON key 取），
		// 不过滤就会把 `write_file` 的确认也吞掉。判别器：
		// TestGuard_W023_NonCommandToolsUnaffected。
		if permission.IsCommandBearingTool(call.Name) {
			if cmdrules.AllSegmentsReadOnly(permission.ExtractCommand(call.Input)) {
				return StageResult{
					Proceed: true,
					Message: "read-only command: no filesystem side effect, confirmation not required",
				}
			}
		}
	}

	// Approval only bypasses the interactive prompt. The policy/rule decision
	// above still runs so read-only and explicit deny rules cannot be bypassed.
	if needConfirm || state.NeedConfirm {
		if call.Approved {
			state.NeedConfirm = false
			return StageResult{Proceed: true, Message: "permission check passed with user approval"}
		}
		// C19 第三层（交互层）：approval.Manager 接管 needConfirm 决策。
		// approvalMgr 为 nil 时保持原有暂停等待确认的行为（向后兼容）。
		if s.approvalMgr != nil {
			decision, reason, err := s.approvalMgr.Check(ctx, call.Name, call.Input)
			if err != nil {
				return StageResult{
					Proceed:   false,
					Rejection: fmt.Sprintf("approval check failed for tool %q: %v", call.Name, err),
				}
			}
			switch decision {
			case approval.DecisionAllow:
				// 交互层允许：清除 NeedConfirm，继续执行后续阶段
				state.NeedConfirm = false
				return StageResult{Proceed: true, Message: fmt.Sprintf("tool %q approved by approval manager: %s", call.Name, reason)}
			case approval.DecisionDeny:
				// 交互层拒绝：终止管道
				return StageResult{
					Proceed:     false,
					Rejection:   fmt.Sprintf("tool %q denied by approval manager: %s", call.Name, reason),
					FailureKind: FailureKindPolicyDenied,
				}
			case approval.DecisionAsk:
				// 交互层要求询问：保持 NeedConfirm=true，由上层暂停等待用户确认
				state.NeedConfirm = true
				return StageResult{
					Proceed:     true,
					NeedConfirm: true,
					Message:     fmt.Sprintf("tool %q requires user confirmation (approval ask): %s", call.Name, reason),
				}
			}
		}

		state.NeedConfirm = true
		return StageResult{
			Proceed:     true,
			NeedConfirm: true,
			Message:     fmt.Sprintf("tool %q requires user confirmation", call.Name),
		}
	}

	return StageResult{Proceed: true, Message: "permission check passed"}
}

// workspaceContainedWrite 判断本次调用是否为「目标**全部**落在工作区内的写类调用」。
//
// 语义（对齐业内既有实现 assess_patch_safety）：这是「写类调用是否会逃逸工作区」的判定，
// 用于把这种情形从「必须确认」降为「不必确认」。
//
// 复用**既有单一真相源**，不新增第二套路径判定：
// - 写类工具表：isWriteTool（同一张表被 CustomValidateStage 使用）
// - 目标提取：pipelineWriteTargets（顶层 path 字段 + 补丁文本内路径）
// - 边界判定：checkWorkspaceBoundary（CustomValidateStage 的同一判据）
//
// 所有提前返回都取 **false（= 不可判定 ⇒ 保持询问）**，即 fail-closed。
//
// ★ 变更：workDir 为空不再需要单独守卫 —— `checkWorkspaceBoundary`
// 现在对「工作区未知」返回 `BoundaryUnknown`（而非从前的 `true`），本函数只认
// `ProvablyInside()`，未知自然落在 false。原先那句 `workDir == ""` 已**结构性地冗余**，
// 故删除：留着会让人误以为安全靠的是这个守卫，而真正的保证是 verdict 的三值语义
// （零值即「未知」）。判别测试：`TestGuard_N040_VerdictZeroValueIsUnknown`
// （结构性，锁零值语义）+ `pipeline_ww_write_noconfirm_test.go` 的
// 「workDir 为空时不得判定为工作区内」（行为面，断言仍须确认）。
//
// 其余提前返回（都取 false）：
// - 非写类工具：超出本免问的适用范围（如 bash 的破坏性仍需确认）。
// - 提取不到任何目标（如 git_commit / git_add 这类无 path 字段的写类工具）：
// 无法证明「没逃逸」，保持询问。
// - 任一目标**未证明**在工作区内（越界、或未知）：保持询问。
func workspaceContainedWrite(name, input, workDir string) bool {
	if !isWriteTool(name) {
		return false
	}
	targets := pipelineWriteTargets(name, input, workDir)
	if len(targets) == 0 {
		return false
	}
	for _, target := range targets {
		// 免问闸的读法与拒绝闸**相反**：必须「已证明在内部」才放行免问；
		// 「未知」（含工作区未配置）一律落到 false = 保持询问。
		if v, _ := checkWorkspaceBoundary(target, workDir); !v.ProvablyInside() {
			return false
		}
	}
	return true
}

// selfTargetedCall 判定「这一次**能改动本机**的调用」是否指向 whale 自身
// —— 载体，口径 **P3（按工具可达面分流；用户 2026-09-20 05:2x 拍板）**。
//
// 分流与判据（判据本身在 `permission/selfpath.go`，此处只按可达面选用哪一个）：
//
//	任意命令类 —— **不在**写类工具表里：
//
// · `bash` / `Monitor` 入参只有一个命令串，里面可以 `cd`、可以 `$(...)`、
// 可以再调别的程序 ⇒ **入参界定不了它的可达面**；
// · `config_set` 改的是 whale 自身的配置（`~/.whale/`），目标根本不在入参里。
// ⇒ 用 **P1** `permission.IsSelfSourceTree`：工作区**或**运行中的二进制落在自身树即算自指。
// 这是 P3 保留的从严面：开发件（住在自身树里的二进制）跑任何工作区都算自指，
// 因为"从二进制所在处改到自身树"随时可能。
//
//	目标受限类 —— **在**写类工具表（`isWriteTool`，同一张表驱动 CustomValidateStage 的
//
// 逐个边界认证与出口 (a) 的免问）里：效果只经由声明的目标路径发生，且该目标已被
// 逐个认证过 ⇒ 可达面**可静态判定**
// ⇒ 用 **P2** `permission.PathInSelfSourceTree`：**任一目标**落在自身树即算自指。
// 相对路径以 `workDir` 为基准解析（与 `pipelineWriteTargets` 对补丁路径的处理一致）。
//
// ⇒ 结果：开发件跑**别人的项目**时，写文件不再弹框（这是 P3 修掉的"吵"），
// 但从别处写进自身树、以及任何 `bash`，仍然照旧从严。
//
// 目标提取不到时（`git_commit` / `git_add` / `git_branch` 这类无 path 字段的仓库级操作）
// 退回 **P1** —— 把「目标不可静态判定 ⇒ 从严」这条语义**显式化**，而不是让"零目标"
// 静默地落到"非自指"。（今天两条分支对这些工具的行为等价：它们既不在出口 (a) 的免问
// 范围内 —— `workspaceContainedWrite` 对零目标返回 false —— 也不在 (b)/(c) 里 ⇒ 无论如何
// 都保留询问。此处写明是防止将来有人给出口 (a) 放宽"零目标"语义时静默放开。）
//
// ★ 分流**不新增工具名单**：两个类的边界分别是**既有 SSOT** —— 写类工具表 `isWriteTool`
// （本仓已有 4 份工具清单漂移的教训）与能力表 `permission.CapabilitiesOf`
// （经 `CanModifyLocalState`，见其文档）。本函数只是把两个既有事实组合起来。
// 结构面守卫：`TestGuard_N077_P3_CommandClassNeverUsesTargetReach`（钉住"具备 CapExec 的
// 工具永不落入 P2"—— 这正是 P3 保留的从严面）。
func selfTargetedCall(name, input, workDir string) bool {
	if !isWriteTool(name) {
		return permission.IsSelfSourceTree(workDir)
	}
	targets := pipelineWriteTargets(name, input, workDir)
	if len(targets) == 0 {
		return permission.IsSelfSourceTree(workDir)
	}
	for _, target := range targets {
		if workDir != "" && platform.AnchorsToWorkDir(target) {
			target = filepath.Join(workDir, target)
		}
		if permission.PathInSelfSourceTree(target) {
			return true
		}
	}
	return false
}

// selfSourceBeyondActualReach 报告本次调用**物理上改不到**自身源码树。
//
// 用户拍板「乙」：自指保护 (0) 原口径是「命令类可达」（任意 bash 都能
// `cd` 进自身树）⇒ 开发件（二进制住在自身树里）下**每一条** bash 都弹，[P] 前缀免问 (d)
// 与只读免问 (e) 全被短路（I-13.11 真机取证）。把已拍板的 三问落到这道闸上：
// 只有下面两类形态「没有决策信息可提供」；其余一律仍须确认（fail-closed）：
//
//	(i) 命令**全段只读** —— 判据**唯一**复用出口 (e) 的 `cmdrules.AllSegmentsReadOnly`
//
// （不造第二套只读判据）⇒ 没有任何写副作用，跑在哪都改不到树（①：无越界）；
//
//	(ii) OS 沙箱**真在强制**（nil = 不可强制 ⇒ 不让路）且自身树不在任何可写根内
//
// （可写根落在树内，或树落在可写根内）⇒ 进程被内核关在树外（③：兜底在场）。
//
// 单向收窄：本函数只让 (0) 的「询问」让路；`classifyWriteVerdict().MustDeny()` 的硬拒绝、
// deny 规则、危险/破坏性档（更早短路）一概不受影响 ——「拒」永不被软化成「问」。
//
// ⚠️ 「树落在可写根内」用**二进制路径**作代理：模块根必是 exe 的祖先
// （`moduleRootFrom` 的定义），故「根在树内」（`PathInSelfSourceTree(root)`）与
// 「exe 在根内」两个方向的比较合取起来恰好覆盖「树与可写根相交」，无需导出树根路径。
// exe 取不到（空串）⇒ 只剩第一个方向 ⇒ 判不出「树不在根内」⇒ fail-closed 仍问。
//
// ⚠️ 路径形态必须一致：`WritableRoots` 返回的是**解析过符号链接**的路径
// （`/private/var/...`），而 `os.Executable`/测试注入可能给**未解析**形态
// （`/var/...`）—— 不先 `EvalSymlinks` 归一，`filepath.Rel` 会跨链接形态误判
// 「exe 不在根内」⇒ 把 dogfooding 树误判成不可达（TestFix_N077_P3 守恒格当场抓到）。
// 解析失败时退回原值（fail-closed：宁可仍问，不可误放）。
func selfSourceBeyondActualReach(name, input, workDir string, enforcement EnforcementStatus) bool {
	// (i) 全段只读 ⇒ 零写副作用。
	if permission.IsCommandBearingTool(name) &&
		cmdrules.AllSegmentsReadOnly(permission.ExtractCommand(input)) {
		return true
	}
	// (ii) 沙箱真在强制 ＋ 树不在任何可写根内。
	if !enforcementActive(enforcement) {
		return false
	}
	exeForms := selfSourceExePathForms(permission.RunningExePath())
	for _, root := range sandbox.WritableRoots(workDir) {
		if permission.PathInSelfSourceTree(root) {
			return false // 可写根落在树内 ⇒ 树可写
		}
		for _, exe := range exeForms {
			if rel, err := filepath.Rel(root, exe); err == nil &&
				rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
				return false // 树（exe 的祖先）落在可写根内 ⇒ 树可写
			}
		}
	}
	return true
}

// selfSourceExePathForms 给出 exe 路径的**全部形态**（原始 ＋ 解析符号链接后），
// 供「exe 是否落在可写根内」逐形态比对 —— 任一形态命中即算命中（fail-closed）。
//
// 为什么不能只解析一次：注入的 exe 路径**文件可能不存在**（测试注入的是路径字符串、
// 生产中 exe 也可能经符号链接启动）⇒ 对整条路径 `EvalSymlinks` 会失败；
// 退而对**目录部分**解析再拼回文件名。取不到任何解析形态就只留原始形态。
func selfSourceExePathForms(exe string) []string {
	exe = strings.TrimSpace(exe)
	if exe == "" {
		return nil
	}
	forms := []string{exe}
	if resolved, err := filepath.EvalSymlinks(exe); err == nil && resolved != "" && resolved != exe {
		forms = append(forms, resolved)
	} else if dir, err := filepath.EvalSymlinks(filepath.Dir(exe)); err == nil && dir != "" {
		if joined := filepath.Join(dir, filepath.Base(exe)); joined != exe {
			forms = append(forms, joined)
		}
	}
	return forms
}

// WriteTargetVerdict 是「本次调用的写目标能否被静态证明落在**本次生效的可写根**内」
// 的**三值**判定（T2 · 2026-09-20）。
//
// 为什么不是 bool：与 BoundaryVerdict 同构 —— 两件性质不同的事不能被压进一个 `true`：
// - 「**已证明**全部写目标在可写根内」（沙箱会放行）
// - 「**已证明**全部写目标在可写根之外」（沙箱必然拒绝）
//
// 而「判不出来」（命令不可解析、目标含变量/命令替换、一个目标都没有）必须是一等状态，
// 且做**零值** —— 这样任何未显式处理的 verdict 都落在安全侧（保持询问）。
//
// 可写根的**唯一权威**是 `sandbox.WritableRoots`（见 internal/sandbox/writable_roots.go，
// 收敛）。本函数只做「提取目标 ⇒ 与那一份集合比对」，不另算边界。
type WriteTargetVerdict int

const (
	// WriteVerdictUnknown 表示**无法判定**：命令不可解析、写目标含不可归约成分
	// （变量 `$VAR` / 命令替换 `$(...)`）、一个写目标都没提取到、或目标有内有外。
	// ★ 刻意取零值：忘记处理时落到安全侧（保持询问），而不是默许或误拒。
	WriteVerdictUnknown WriteTargetVerdict = iota
	// WriteVerdictMustAllow 表示**已证明**全部写目标落在本次生效的可写根内。
	WriteVerdictMustAllow
	// WriteVerdictMustDeny 表示**已证明**全部写目标落在可写根之外
	// ⇒ 沙箱必然拒绝这次写入，调用根本跑不成。
	WriteVerdictMustDeny
)

// MustDeny 报告是否**已证明**必被沙箱拒绝 —— 唯一可作为「不弹、直接拒」依据的状态。
//
// 与 BoundaryVerdict.ProvablyOutside 的不对称性同理：只有**证明在外部**才敢省掉询问；
// 「未知」绝不当作「必拒」（那会把"判不出来"变成一次硬拒绝，把功能不可用伪装成安全）。
func (v WriteTargetVerdict) MustDeny() bool { return v == WriteVerdictMustDeny }

// MustAllow 报告是否**已证明**全部写目标在可写根内 —— 唯一可作为「免问」依据的状态。
//
// ★ 注意与 `workspaceContainedWrite` 的区别（别把两者合并）：
// - 本判据的比对集合是**沙箱可写根**（工作区 ＋ 临时目录 ＋ 缓存），回答"沙箱会不会拒"；
// - workspaceContainedWrite 的比对集合是**工作区**，回答"是否逃逸工作区"。
//
// 免问（出口 (a)）仍然只认后者 —— 用前者会把 `/tmp` 变成免问根（放大豁免）。
func (v WriteTargetVerdict) MustAllow() bool { return v == WriteVerdictMustAllow }

// classifyWriteVerdict 计算三值判据（T2）。
//
// 输入：工具名 ＋ 入参 ＋ 工作区。仅对**进程类**工具（`permission.IsSandboxBoundedTool`：
// bash / monitor）有意义 —— 其余工具的写目标不在命令串里，一律返回 Unknown。
//
// 步骤：
// 1. 取命令串（字段 `command`）；
// 2. 用 AST 提取**写重定向**目标（`validator.BashWriteTargets`）；
// 3. 与 `sandbox.WritableRoots(workDir)` 逐个比对，全部在内 ⇒ MustAllow、
// 全部在外 ⇒ MustDeny、内有外有 ⇒ Unknown。
//
// 所有提前返回都取 **Unknown（= 保持询问）**，即 fail-closed。
//
// ★ 为什么"一个写目标都没有"是 Unknown 而不是 MustDeny：空集与可写根的交集确实是空集，
// 但 `ls` / `git status` 这类命令**没有任何写入**，"必被沙箱拒"这个结论不成立；
// 把它判成 MustDeny 会让所有只读命令在自指场景下被硬拒（严重回归）。
// 同理相对路径以 workDir 解析：`cd /etc && echo x > y` 这类会被判成"工作区内的 y"
// ⇒ 落到 Unknown ⇒ 仍然询问（保守侧）。漏报只损失"省一次询问"，不会静默放行。
func classifyWriteVerdict(name, input, workDir string) WriteTargetVerdict {
	if !permission.IsSandboxBoundedTool(name) {
		return WriteVerdictUnknown
	}
	command := pipelineExtractField(input, "command")
	if strings.TrimSpace(command) == "" {
		return WriteVerdictUnknown
	}
	targets, unresolved := validator.BashWriteTargets(command)
	if unresolved || len(targets) == 0 {
		return WriteVerdictUnknown
	}
	roots := sandbox.WritableRoots(workDir)
	if len(roots) == 0 {
		return WriteVerdictUnknown
	}

	anyInside, anyOutside := false, false
	for _, t := range targets {
		abs := t
		if !filepath.IsAbs(abs) {
			if workDir == "" {
				return WriteVerdictUnknown
			}
			abs = filepath.Join(workDir, abs)
		}
		inSomeRoot := false
		for _, root := range roots {
			// 复用全仓唯一的路径包含判据（checkWorkspaceBoundary 的第二个参数
			// 是"根"，此处传入的是沙箱可写根之一，不是工作区）。
			if v, _ := checkWorkspaceBoundary(abs, root); v.ProvablyInside() {
				inSomeRoot = true
				break
			}
		}
		if inSomeRoot {
			anyInside = true
		} else {
			anyOutside = true
		}
	}
	switch {
	case anyInside && anyOutside:
		return WriteVerdictUnknown
	case anyOutside:
		return WriteVerdictMustDeny
	default:
		return WriteVerdictMustAllow
	}
}

// checkOnFailure 在工具执行失败后调用 approvalMgr.CheckWithResult 进行 on-failure 审批。
//
// 触发语义说明：state.ExecutionError 由 ExecuteStage 在工具返回错误结果
// （result.IsError=true）时写入，表示"工具执行结果为错误"而非 Go 层 error/panic。
// ExecuteStage 始终返回 Proceed=true（即使 IsError=true），确保本方法可达。
//
// 接入条件（全部满足才调用 CheckWithResult）：
// - approvalMgr 非 nil（向后兼容：nil 时返回 nil StageResult，表示跳过）
// - 策略为 PolicyOnFailure（其他策略已在执行前由 Check 决策，无需 on-failure 复查）
// - state.ExecutionError != ""（工具执行返回错误结果）
//
// 返回 *StageResult（nil 表示跳过）：
// - DecisionAllow → Proceed=true，允许重试或继续后续阶段
// - DecisionDeny → Proceed=false，返回拒绝原因
// - DecisionAsk → NeedConfirm=true，按现有 needConfirm 暂停逻辑处理
func (s *PermissionStage) checkOnFailure(ctx context.Context, call Call, state *PipelineState) *StageResult {
	// approvalMgr 为 nil 时跳过（向后兼容，不接入 on-failure 审批）
	if s.approvalMgr == nil {
		return nil
	}
	// 非 OnFailure 策略跳过：其他策略已在执行前由 Check 决策
	if s.approvalMgr.Policy() != approval.PolicyOnFailure {
		return nil
	}

	// 将 ExecutionError 字符串转为 error 传入 CheckWithResult
	execErr := fmt.Errorf("%s", state.ExecutionError)
	decision, reason, err := s.approvalMgr.CheckWithResult(ctx, call.Name, call.Input, execErr)
	if err != nil {
		return &StageResult{
			Proceed:   false,
			Rejection: fmt.Sprintf("on-failure approval check failed for tool %q: %v", call.Name, err),
		}
	}

	switch decision {
	case approval.DecisionAllow:
		// 允许重试或继续：按现有重试逻辑处理（此处放行，由后续阶段决定）
		return &StageResult{
			Proceed: true,
			Message: fmt.Sprintf("tool %q on-failure approved by approval manager: %s", call.Name, reason),
		}
	case approval.DecisionDeny:
		// 拒绝：终止管道，返回拒绝原因
		return &StageResult{
			Proceed:     false,
			Rejection:   fmt.Sprintf("tool %q denied by on-failure approval manager: %s", call.Name, reason),
			FailureKind: FailureKindPolicyDenied,
		}
	case approval.DecisionAsk:
		// 需要询问用户：设置 NeedConfirm，按现有暂停等待确认逻辑处理
		state.NeedConfirm = true
		return &StageResult{
			Proceed:     true,
			NeedConfirm: true,
			Message:     fmt.Sprintf("tool %q requires user confirmation (on-failure ask): %s", call.Name, reason),
		}
	}

	return &StageResult{Proceed: true, Message: "on-failure approval completed"}
}

// --- Stage 5: Tool Execution ---

// ExecuteStage is the tool execution stage
type ExecuteStage struct {
	executor *Executor
	registry *Registry
}

// Name returns the stage name
func (s *ExecuteStage) Name() string {
	return "execute"
}

// Execute executes the tool and captures the result
func (s *ExecuteStage) Execute(ctx context.Context, call Call, state *PipelineState) StageResult {
	if s.executor == nil {
		return StageResult{
			Proceed:   false,
			Rejection: "executor not initialized",
		}
	}

	// F1（I1 单一强制点）：管线内部必须调用裸执行引擎 executeDirect，
	// 而非 Execute —— 否则 Execute 的门控分支会再次进入 Pipeline.Execute，造成无限递归。

	// ★ ：沙箱批准标记的**唯一产生点**（收口自
	// executor.go 的 plain 分支 —— 旧点只覆盖 ExecuteWithConfirm 的
	// OnConfirm 重跑，TUI 面板批准重跑（tool_exec.go）与子代理批准恢复
	// （agent.go）重入 Pipeline.Execute 时不经过它 ⇒ "批了白批"）。
	//
	// 为什么放这里而不是 Pipeline.Execute 头部：本阶段拿到的是 **pre-hook
	// 改写之后的最终命令**（与 SandboxPrecheckStage:150 的位置纪律同款 ——
	// 判定必须针对真正会被执行的命令）；而批准事实（state.UserApproved）
	// 在头部从原始 Call 取定，两条信息在此汇合。档位判断
	// （仅 TierConfirm 才注入）在 sanctionedContext 内部完成。
	if state.UserApproved {
		ctx = sanctionedContext(ctx, s.executor.Sandbox, call)
	}

	result := s.executor.executeDirect(ctx, call)
	state.Result = &result

	if result.IsError {
		state.ExecutionError = result.Output
		return StageResult{
			Proceed: true,
			Message: fmt.Sprintf("tool execution completed (with error): %s", truncateStr(result.Output, 200)),
		}
	}

	return StageResult{Proceed: true, Message: "tool execution succeeded"}
}

// --- Stage 6: Post-Hook ---

// PostHookStage is the post-hook stage
type PostHookStage struct {
	hookMgr *hook.Manager
}

// Name returns the stage name
func (s *PostHookStage) Name() string {
	return "post_hook"
}

// Execute runs post-hooks
func (s *PostHookStage) Execute(ctx context.Context, call Call, state *PipelineState) StageResult {
	if s.hookMgr == nil {
		return StageResult{Proceed: true, Message: "no hook manager, skipping"}
	}

	output := ""
	if state.Result != nil {
		output = state.Result.Output
	}

	result := s.hookMgr.RunPostHooks(call.Name, call.Input, output)

	// 桶 X3 病灶2：细分生命周期 hook 触发（修复生产零调用）
	// 修复前：PostToolUse 是唯一触发点，PostBash/PostRead/PostWrite/PostEdit/PostGit/PostWeb 等细分 hook 从未在生产路径触发。
	// 修复后：在 PostToolUse 之后，根据工具类型额外触发对应细分 hook（block 时整体阻断）。
	if result.Action != hook.ActionBlock {
		if blockReason := s.runPostToolSpecificHooks(call, output); blockReason != "" {
			return StageResult{
				Proceed:   false,
				Rejection: blockReason,
			}
		}
	}

	switch result.Action {
	case hook.ActionBlock:
		reason := "post-hook blocked"
		if result.Reason != "" {
			reason = result.Reason
		}
		return StageResult{
			Proceed:   false,
			Rejection: reason,
		}
	default:
		if result.Modified != nil && state.Result != nil {
			state.Result.Output = *result.Modified
		}
		msg := "post-hook passed"
		if result.Output != "" {
			msg = fmt.Sprintf("post-hook passed, command output: %s", truncateStr(result.Output, 200))
		}
		if result.Error != nil {
			msg = fmt.Sprintf("post-hook command execution error (non-blocking): %v", result.Error)
		}
		return StageResult{Proceed: true, Message: msg}
	}
}

// runPostToolSpecificHooks 根据工具类型触发对应细分 Post hook（桶 X3 病灶2 接入点）。
//
// 与 PreHookStage.runPreToolSpecificHooks 对称：PostToolUse 之外，按工具类型触发细分 post hook。
// 用于工具执行后的细分事件通知（如 PostBash 可记录命令历史、PostRead 可更新文件访问缓存）。
// 任一细分 hook 返回 block 则整体阻断（fail-closed 语义）。
//
// 返回非空字符串表示 block 原因；空字符串表示通过。
func (s *PostHookStage) runPostToolSpecificHooks(call Call, output string) string {
	if s.hookMgr == nil {
		return ""
	}
	name := call.Name
	switch name {
	case "bash":
		command := pipelineExtractField(call.Input, "command")
		result := s.hookMgr.RunPostBashHooks(command, output)
		if result.Action == hook.ActionBlock {
			reason := "post-bash hook blocked"
			if result.Reason != "" {
				reason = result.Reason
			}
			return reason
		}
	case "read_file":
		filePath := pipelineExtractField(call.Input, "path")
		if filePath == "" {
			filePath = pipelineExtractField(call.Input, "file_path")
		}
		result := s.hookMgr.RunPostReadHooks(filePath, output)
		if result.Action == hook.ActionBlock {
			reason := "post-read hook blocked"
			if result.Reason != "" {
				reason = result.Reason
			}
			return reason
		}
	case "write_file":
		filePath := pipelineExtractField(call.Input, "path")
		if filePath == "" {
			filePath = pipelineExtractField(call.Input, "file_path")
		}
		result := s.hookMgr.RunPostWriteHooks(filePath)
		if result.Action == hook.ActionBlock {
			reason := "post-write hook blocked"
			if result.Reason != "" {
				reason = result.Reason
			}
			return reason
		}
	case "edit_file":
		filePath := pipelineExtractField(call.Input, "path")
		if filePath == "" {
			filePath = pipelineExtractField(call.Input, "file_path")
		}
		result := s.hookMgr.RunPostEditHooks(name, filePath, "", output)
		if result.Action == hook.ActionBlock {
			reason := "post-edit hook blocked"
			if result.Reason != "" {
				reason = result.Reason
			}
			return reason
		}
	case "git_diff", "git_status", "git_log", "git_add", "git_commit", "git_branch":
		operation := strings.TrimPrefix(name, "git_")
		result := s.hookMgr.RunPostGitHooks(operation, output)
		if result.Action == hook.ActionBlock {
			reason := "post-git hook blocked"
			if result.Reason != "" {
				reason = result.Reason
			}
			return reason
		}
	case "WebFetch", "WebSearch":
		url := pipelineExtractField(call.Input, "url")
		if url == "" {
			url = pipelineExtractField(call.Input, "query")
		}
		result := s.hookMgr.RunPostWebHooks(url, output)
		if result.Action == hook.ActionBlock {
			reason := "post-web hook blocked"
			if result.Reason != "" {
				reason = result.Reason
			}
			return reason
		}
	}
	return ""
}

// pipelineExtractPath extracts the path field from tool input JSON (pipeline-specific, avoids conflict with extractFilePath in scheduler.go)
func pipelineExtractPath(input string) string {
	var parsed map[string]interface{}
	if err := json.Unmarshal([]byte(input), &parsed); err != nil {
		return ""
	}
	for _, key := range []string{"path", "file_path", "file"} {
		if p, ok := parsed[key].(string); ok {
			return p
		}
	}
	return ""
}

// patchTextFields 登记「补丁文本装在入参哪个字段」的工具（G5）。
//
// 为什么要一张表而不是在 pipelineWriteTargets 里写 if name == "..."：
// 目标是让"新增一个补丁类工具"只需要在这里加一行 —— 多目标前置校验、工作区边界、
// 二进制拦截就自动生效。散落的 if 迟早会漏掉新工具（本仓库已有 4 份工具清单漂移的教训）。
var patchTextFields = map[string]string{
	applyUnifiedDiffToolName: "patch",
}

// pipelineWriteTargets 返回本次调用需要按写类工具校验的**全部**目标路径。
//
// - 单文件工具：等价于 pipelineExtractPath（返回 0 或 1 个），语义与顺序不变；
// - 补丁类工具：目标路径不在顶层字段里，而是埋在补丁文本内 —— 用
// diff.ScanPatchTargets **宽松扫描**补丁头（宁多报不漏报），把每个候选路径
// 都交给 CustomValidateStage。多报一个最多让一次本就该拒的调用提前被拒；
// 漏报则会让越界写穿到 handler（那正是本次要堵的洞）。
//
// 补丁内的相对路径以 `workDir` 为基准解析（补丁路径的语义就是"相对补丁根"，
// 而补丁根 = 工作区）；顶层 path 字段保持既有的 CWD 语义不动。
//
// 注意：`patch_path`（补丁文件自身的路径）**不**进这里 —— 它是读取面，与 read_file
// 同级，不受"写类工具必须留在工作区内"的约束；handler 对它只做 validateFilePath。
func pipelineWriteTargets(name, input, workDir string) []string {
	var out []string
	if base := pipelineExtractPath(input); base != "" {
		out = append(out, base)
	}
	if field, ok := patchTextFields[name]; ok {
		for _, p := range diff.ScanPatchTargets(pipelineExtractField(input, field)) {
			if workDir != "" && !filepath.IsAbs(p) {
				p = filepath.Join(workDir, p)
			}
			out = append(out, p)
		}
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

// pipelineExtractField 从工具 input JSON 中提取指定字段值（桶 X3 病灶2 辅助函数）。
// 用于细分 hook 触发时提取工具特定字段（如 bash 的 command、WebFetch 的 url）。
// 字段不存在或类型非 string 时返回空字符串。
func pipelineExtractField(input, field string) string {
	var parsed map[string]interface{}
	if err := json.Unmarshal([]byte(input), &parsed); err != nil {
		return ""
	}
	if v, ok := parsed[field].(string); ok {
		return v
	}
	return ""
}
