package tui

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/anchorandfish/whale-code/internal/ai"
	"github.com/anchorandfish/whale-code/internal/bridge"
	"github.com/anchorandfish/whale-code/internal/permission"
	"github.com/anchorandfish/whale-code/internal/session"
	"github.com/anchorandfish/whale-code/internal/tools"
	tea "github.com/charmbracelet/bubbletea"
)

// executeTools execute pending tool calls (with safety confirmation)
func (m *Model) executeTools() tea.Cmd {
	m.reassignServices() // 修复服务指针别名：指向当前实例自身子结构体
	// Capture the request generation before returning any asynchronous command.
	// Reading RequestID inside the closure can associate a cancelled tool result
	// with a newer user request that started before the closure runs.
	capturedID := m.aiService.RequestID()
	capturedPolicyMode := m.toolService.Policy().Mode()
	capturedTools := make([]session.ContentBlock, len(m.toolService.PendingTools()))
	copy(capturedTools, m.toolService.PendingTools())
	capturedPolicy := m.toolService.Policy()
	capturedExecutor := m.toolService.Executor()

	// The pipeline owns safety confirmation when it is initialized. Keeping a
	// second TUI pre-check here caused two prompts for the same write/command.
	//
	// ⚠️ 死代码警告：交互式 TUI 里 Executor.Pipeline 恒非 nil
	// （internal/tui/lifecycle.go:418 启动时无条件 InitPipelineWithHooks），
	// 因此本分支在生产路径上不执行，其中的规则门（RuleCheck）对真实运行无效。
	// 真正的规则裁决在 internal/tools/pipeline.go 的 Execute() 开头（规则前置门）。
	// 若将来要改权限/审批行为，先确认目标路径，别在这段里改 —— 历史教训见
	// PERMISSION_WORKFLOW.md 第 1 节。
	//
	// ★ P-002（2026-09-22 反缝补扫描）补充：本块内另有工具名对
	// `pt.Name == "bash" || pt.Name == "PowerShell"`（本文件 :68 与 :386 两处）。
	// 它们与「命令载体」SSOT（`permission.IsCommandBearingTool` = {bash, monitor}）
	// **已漂移**（多幽灵名、少 monitor），但因整块不可达 ⇒ 本轮**刻意不改**：
	// 改死码零效果、只增噪声。⚠️ 若这段哪天被复活，必须先把这两处换成 SSOT ——
	// 否则 Monitor 的拦截面会在这里和主管线分叉（正是 P-002 的成因）。
	if capturedExecutor.Pipeline == nil {
		for _, pt := range capturedTools {
			inputPreview := truncateRunes(pt.Input, 200)

			// 规则裁决必须发生在「是否弹确认框」之前。
			// 此前本函数只看风险等级与 Policy.Has()（会话记忆），规则系统完全没参与，
			// 导致显式 deny 规则命中时仍然弹框——只有用户先点 Y，才会在
			// executeToolsDirect 内部的 Check() 里被拦下（已晚了一步）。
			// 对齐业内既有实现： +，
			// Decision::Forbidden 直接拒绝，绝不进入审批流程。
			ruleFound, ruleAllowed, ruleNeedConfirm := capturedPolicy.RuleCheck(pt.Name, pt.Input)
			if ruleFound && !ruleAllowed {
				results := make([]tools.Result, 0, len(capturedTools))
				for _, p := range capturedTools {
					results = append(results, tools.Result{
						ID:      p.ID,
						Name:    p.Name,
						Output:  fmt.Sprintf("Blocked by permission rule / 已被权限规则拦截: %s\nUse /permissions list to review rules.", p.Name),
						IsError: true,
					})
				}
				return func() tea.Msg { return toolResultMsg{results: results, requestID: capturedID} }
			}
			// allow 规则命中且规则本身不要求确认 → 免问，直接放行到执行。
			// ask 规则、或 allow 命中危险命令（ruleCheckLocked 返回 needConfirm=true）
			// 仍走原有的确认逻辑，对齐业内既有实现「allow 只能免问、不能免掉危险确认」。
			ruleAllowsWithoutConfirm := ruleFound && ruleAllowed && !ruleNeedConfirm

			if pt.Name == "bash" || pt.Name == "PowerShell" {
				// 修复：用原始 pt.Input 提取纯命令（inputPreview 已被 truncateRunes 截断，解析截断 JSON 会失败）
				// permission.go:245 ExtractCommand / permission.go:126 Check 内部 extractCommand
				cmd := permission.ExtractCommand(pt.Input)
				risk := permission.AnalyzeBashCommand(cmd)
				if risk == permission.BashRiskDangerous {
					if capturedPolicyMode != permission.LevelDangerFullAccess {
						results := make([]tools.Result, 0, len(capturedTools))
						for _, p := range capturedTools {
							results = append(results, tools.Result{
								ID:      p.ID,
								Name:    p.Name,
								Output:  fmt.Sprintf("Dangerous command blocked; command was not run / 危险命令已拦截，命令未执行: %s\nRequires danger-full-access permission / 需要 danger-full-access 权限。", inputPreview),
								IsError: true,
							})
						}
						return func() tea.Msg { return toolResultMsg{results: results, requestID: capturedID} }
					}
				}

				if risk == permission.BashRiskDestructive || risk == permission.BashRiskDangerous {
					// allow 规则已明确放行且无需确认 → 不再打断用户
					if ruleAllowsWithoutConfirm {
						continue
					}
					// 修复：用 pt.Input 保持 memory key 与 executePipelineRemaining 一致
					if !capturedPolicy.Has(pt.Name, pt.Input) {
						m.toolService.SetPendingConfirm(&PendingConfirm{
							ToolName:  pt.Name,
							ToolInput: pt.Input,
							RiskLevel: string(risk),
							Kind:      ApprovalKindExec,
							Options:   buildApprovalOptions(ApprovalKindExec),
						})
						m.sessionService.SetState(StateWaitingConfirm)
						// The concrete operation is rendered in the confirmation overlay.
						// Keep it out of the transcript so the existing conversation does
						// not scroll away while the user is deciding.
						m.recalcViewportHeight()
						return nil
					}
				}
			}

			if pt.Name == "write_file" || pt.Name == "edit_file" {
				// allow 规则已明确放行且无需确认 → 不再打断用户
				if ruleAllowsWithoutConfirm {
					continue
				}
				// 修复：用 pt.Input 保持 memory key 一致（与 Remember key 对齐）
				if !capturedPolicy.Has(pt.Name, pt.Input) {
					m.toolService.SetPendingConfirm(&PendingConfirm{
						ToolName:  pt.Name,
						ToolInput: pt.Input,
						RiskLevel: "file-modify",
						Kind:      ApprovalKindFileModify,
						Options:   buildApprovalOptions(ApprovalKindFileModify),
					})
					m.sessionService.SetState(StateWaitingConfirm)
					// The confirmation overlay owns this transient prompt; do not add
					// it to the transcript and push older conversation off-screen.
					m.recalcViewportHeight()
					return nil
				}
			}
		}
	}

	return m.executeToolsDirect()
}

// executePipelineRemaining continue executing remaining tools after pipeline confirmation
// After user confirms, record decision in Policy, then execute current and subsequent tools
// Use ExecuteWithConfirm instead of ExecuteWithPipeline; supports OnConfirm callback
func (m *Model) executePipelineRemaining(remember bool) tea.Cmd {
	if m.toolService.ToolCancel() != nil {
		m.toolService.ToolCancel()()
		m.toolService.SetToolCancel(nil)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 180*time.Second)
	m.toolService.SetToolCancel(cancel)

	// Restore pipeline confirmation context from pendingConfirm
	pc := m.toolService.PendingConfirm()
	capturedID := pc.PipelineRequestID
	capturedTools := make([]session.ContentBlock, len(pc.PipelinePendingTools))
	copy(capturedTools, pc.PipelinePendingTools)
	startIndex := pc.PipelineCurrentIndex
	// 恢复上下文完整性（不变量见 pipelineConfirmMsg 注释）：把暂停点之前已执行工具的结果
	// 一并取回。它们在上一段闭包返回时已被销毁，只存在于这里。
	capturedPriorResults := make([]tools.Result, len(pc.PipelinePriorResults))
	copy(capturedPriorResults, pc.PipelinePriorResults)
	capturedExecutor := m.toolService.Executor()
	capturedPolicy := m.toolService.Policy()

	// 请求内复用快照：**在 Update 线程**读 Model。下面的闭包在 tea.Cmd 的 goroutine 里
	// 执行，不得再碰 Model 字段 —— map 并发读写会 panic，且 Model 按值复制，
	// 闭包捕获的那份副本与真正被驱动的那份未必是同一个（见 app.go 内 programRef 的教训）。
	// 快照覆盖「同一用户请求的前几批/前几段里用户已批准过的逐字相同调用」
	// ⇒ 跨暂停点、跨批的复用都成立。
	reuseSnapshot := make([]bool, len(capturedTools))
	for i, pt := range capturedTools {
		reuseSnapshot[i] = m.requestsApprovalReuse(pt.Name, pt.Input, capturedID)
	}

	// Clear pipeline confirmation context
	m.toolService.SetPendingConfirm(nil)

	return func() tea.Msg {
		// 前置已完成的产出，再续跑剩余工具 ⇒ 顺序与 capturedTools 一致。
		results := make([]tools.Result, 0, len(capturedPriorResults)+len(capturedTools))
		results = append(results, capturedPriorResults...)
		// 段内累积：本段里刚被用户批准（i == startIndex）的那个输入，其**逐字相同**的
		// 后续调用直接复用该批准。局部 map ⇒ 闭包自洽，不需要碰 Model。
		// （跨段/跨批由 reuseSnapshot 承担：它每次恢复时重新从 Model 构造。）
		approvedInSegment := make(map[string]struct{}, 1)
		// Start from confirmed tool, execute one by one
		for i := startIndex; i < len(capturedTools); i++ {
			pt := capturedTools[i]
			reuseKey := approvalReuseKey(pt.Name, pt.Input)
			_, approvedInSameSegment := approvedInSegment[reuseKey]
			pr := capturedExecutor.ExecuteWithConfirm(ctx, tools.Call{
				ID:       pt.ID,
				Name:     pt.Name,
				Input:    pt.Input,
				Approved: i == startIndex,
				// 复用只在「该确切输入已被用户批准过」时成立：reuseSnapshot = 本请求
				// 早些时候批准过（跨消息/跨批）；approvedInSameSegment = 本段内刚批准过。
				// ⚠️ 危险面不在复用范围：本字段只被 PermissionStage 的免问块读取，
				// 危险档/破坏性检查排在它之前且带 NeedConfirm ⇒ 管线更早就短路了。
				// 详见 tools.Call.ReusedApproval 与 pipeline.go 的 (r) 出口注释。
				ReusedApproval: reuseSnapshot[i] || approvedInSameSegment,
			})
			if pr.NeedConfirm {
				// Subsequent tool also needs confirmation; pause again.
				// priorResults 必须带上：本轮已执行的（含从上次暂停恢复的）结果
				// 一旦随本闭包返回就销毁，下次恢复将无从重现。见 pipelineConfirmMsg 注释。
				cancel()
				return pipelineConfirmMsg{
					toolName:     pt.Name,
					toolInput:    pt.Input,
					reason:       pr.Rejection,
					pendingTools: capturedTools,
					currentIndex: i,
					requestID:    capturedID,
					priorResults: results,
				}
			}
			if pr.Proceed && pr.Result != nil {
				// Remember only after the policy and execution both succeed. A
				// read-only denial must not create a future allow-list entry.
				if remember && i == startIndex && capturedPolicy != nil {
					capturedPolicy.Remember(pt.Name, pt.Input, true)
				}
				// 该输入至此已被用户批准（i == startIndex 正是刚批准的那次）⇒ 记入段内集合，
				// 供本段内后续逐字相同的调用复用。刻意放在**成功分支内**：被拒绝 / 未执行的
				// 调用不得产生"已获批"这一事实 —— 与上面 Remember 是同一条纪律。
				approvedInSegment[reuseKey] = struct{}{}
				results = append(results, *pr.Result)
			} else if !pr.Proceed {
				// G9-④（S13）：管线校验阶段自动拒绝 ⇒ **决策面**留痕。
				//
				// 在此之前，「拒绝」只有**执行面**出口（G9-② 在 `tools/pipeline.go` 的拒绝点
				// 写 whale.log），而「这次调用的决策是 denied」在账本里**零条**（真机 2026-09-16
				// 02:03：越界写被拒，`~/.whale/audit/tool-approval.jsonl` 零条）⇒ `~/.whale/audit/`
				// 只是"批准账本"，不是"完整账本"。两轴**语义不同**，不是同一件事的两种写法：
				// · 执行面（whale.log）：这条调用**没被执行**；
				// · 决策面（tool-approval.jsonl）：这次调用的**决策是 denied**。
				//
				// ★ 成因方必须是 `tools.SourcePipeline`，**不得用 `tools.SourceUser` 凑**：
				// 用户**从未被询问**（CustomValidateStage 排在 PermissionStage 之前，直接短路
				// 返回，连确认框都没弹过）。用 SourceUser 等于在审计里**谎称"用户拒绝"**，
				// 违 `tools/confirm.go:42` 的硬纪律「只有 SourceUser 才是用户拒绝」。
				//
				// 复用既有**唯一落盘出口** `recordApprovalDecision`（→ `recordApproval` →
				// `~/.whale/audit/tool-approval.jsonl`），不另起通道（族C-1）；
				// `confirmDecisionOf(DecisionDeny)` 已含 `DecisionDenied` ⇒ 映射表无需改。
				m.recordApprovalDecision(bridge.OriginTUIMain, pt.Name, pt.Input,
					DecisionDeny, tools.SourcePipeline, "")
				results = append(results, pipelineRejectionResult(pt, pr))
				// ★ ：**批内短路** —— 本批到此终止，后续调用
				// 既不再询问也不再执行（占位结果见 abortRestOfBatch）。
				results = abortRestOfBatch(results, capturedTools, i+1, pt.ID)
				break
			} else {
				results = append(results, tools.Result{
					ID:      pt.ID,
					Name:    pt.Name,
					Output:  "⚠️ Pipeline execution returned no result",
					IsError: true,
				})
			}
		}
		cancel()
		return toolResultMsg{results: results, requestID: capturedID}
	}
}

// executeToolsDirect execute tools directly without confirmation
// When Pipeline is initialized, use ExecuteWithConfirm to execute tool calls one by one,
// Otherwise fallback to ExecuteBatch
func (m *Model) executeToolsDirect() tea.Cmd {
	if m.toolService.ToolCancel() != nil {
		m.toolService.ToolCancel()()
		m.toolService.SetToolCancel(nil)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 180*time.Second)
	m.toolService.SetToolCancel(cancel)

	capturedID := m.aiService.RequestID()
	capturedPolicyMode := m.toolService.Policy().Mode()
	capturedTools := make([]session.ContentBlock, len(m.toolService.PendingTools()))
	copy(capturedTools, m.toolService.PendingTools())
	capturedPolicy := m.toolService.Policy()
	capturedExecutor := m.toolService.Executor()

	// 请求内复用快照：Update 线程读 Model，闭包只读这份局部快照（理由见
	// executePipelineRemaining 内同名字段的注释）。这里**只**看 Model ——
	// 本函数是"新一批开始"，本批内若前面有工具已被批准，那是同一段内的累积，
	// 由 executePipelineRemaining 的 approvedInSegment 承担，不在本处。
	reuseSnapshot := make([]bool, len(capturedTools))
	for i, pt := range capturedTools {
		reuseSnapshot[i] = m.requestsApprovalReuse(pt.Name, pt.Input, capturedID)
	}

	return func() tea.Msg {
		// When Pipeline is initialized, use pipeline to execute tools one by one
		if capturedExecutor.Pipeline != nil {
			results := make([]tools.Result, 0, len(capturedTools))
			for i, pt := range capturedTools {
				pr := capturedExecutor.ExecuteWithConfirm(ctx, tools.Call{
					ID:    pt.ID,
					Name:  pt.Name,
					Input: pt.Input,
					// 同一用户请求内已批准过的逐字相同调用 ⇒ 复用那次批准，不再重复询问。
					// ⚠️ 危险面不在复用范围：本字段只被 PermissionStage 的免问块读取，
					// 危险档/破坏性检查排在它之前且带 NeedConfirm ⇒ 管线更早就短路了。
					ReusedApproval: reuseSnapshot[i],
				})
				if pr.NeedConfirm {
					// Pipeline requires user confirmation; return confirmation message and pause execution.
					// priorResults：首次进入时通常为空（第 0 个工具就要求确认），但当首个工具
					// 无需确认、后续才命中确认时，前面那些已执行的结果必须一并带过暂停点。
					cancel()
					return pipelineConfirmMsg{
						toolName:     pt.Name,
						toolInput:    pt.Input,
						reason:       pr.Rejection,
						pendingTools: capturedTools,
						currentIndex: i,
						requestID:    capturedID,
						priorResults: results,
					}
				}
				if pr.Proceed && pr.Result != nil {
					results = append(results, *pr.Result)
				} else if !pr.Proceed {
					// G9-④（S13）：与 executePipelineRemaining 同源的第二个拒绝点
					// —— 决策面留痕。成因方/不另起通道/为何不得用 SourceUser 的完整理由
					// 见 executePipelineRemaining 内同一分支的长注释（两处**必须同改**：
					// 只挂一处 ⇒ 另一条路径（免确认直跑 vs 确认后恢复）静默漏记）。
					m.recordApprovalDecision(bridge.OriginTUIMain, pt.Name, pt.Input,
						DecisionDeny, tools.SourcePipeline, "")
					results = append(results, pipelineRejectionResult(pt, pr))
					// ★ ：**批内短路**（与 executePipelineRemaining
					// 同一实现点 abortRestOfBatch；两处**必须同改**，见其文档注释）。
					results = abortRestOfBatch(results, capturedTools, i+1, pt.ID)
					break
				} else {
					results = append(results, tools.Result{
						ID:      pt.ID,
						Name:    pt.Name,
						Output:  "⚠️ Pipeline execution returned no result",
						IsError: true,
					})
				}
			}
			cancel()
			return toolResultMsg{results: results, requestID: capturedID}
		}

		// Fallback path: Pipeline not initialized; use original logic
		calls := make([]tools.Call, 0, len(capturedTools))
		for _, pt := range capturedTools {
			inputPreview := truncateRunes(pt.Input, 200)

			if pt.Name == "bash" || pt.Name == "PowerShell" {
				// 修复：用原始 pt.Input 提取纯命令（与 executeTools 保持一致）
				cmd := permission.ExtractCommand(pt.Input)
				risk := permission.AnalyzeBashCommand(cmd)
				if risk == permission.BashRiskDangerous {
					if capturedPolicyMode != permission.LevelDangerFullAccess {
						results := make([]tools.Result, 0, len(capturedTools))
						for _, p := range capturedTools {
							results = append(results, tools.Result{
								ID:      p.ID,
								Name:    p.Name,
								Output:  fmt.Sprintf("⚠️ Dangerous command blocked: %s\nRequires danger-full-access permission to execute\nUse /permissions danger-full-access to switch", inputPreview),
								IsError: true,
							})
						}
						cancel()
						return toolResultMsg{results: results, requestID: capturedID}
					}
				}
				if risk == permission.BashRiskDestructive {
					if capturedPolicyMode == permission.LevelReadOnly {
						results := make([]tools.Result, 0, len(capturedTools))
						for _, p := range capturedTools {
							results = append(results, tools.Result{
								ID:      p.ID,
								Name:    p.Name,
								Output:  fmt.Sprintf("⚠️ Destructive command inread-only mode: %s\nUse /permissions workspace-write to switch", inputPreview),
								IsError: true,
							})
						}
						cancel()
						return toolResultMsg{results: results, requestID: capturedID}
					}
				}
			}

			// 修复：用 pt.Input 保持 memory key 一致（与 executePipelineRemaining 的 Remember key 一致）
			allowed, _ := capturedPolicy.Check(pt.Name, pt.Input)
			if !allowed {
				results := make([]tools.Result, 0, len(capturedTools))
				for _, p := range capturedTools {
					results = append(results, tools.Result{
						ID:      p.ID,
						Name:    p.Name,
						Output:  fmt.Sprintf("⚠️ Insufficient permissions: tool %s requires higher permissions (current: %s)\nUse /permissions <mode> to switch", p.Name, capturedPolicyMode),
						IsError: true,
					})
				}
				cancel()
				return toolResultMsg{results: results, requestID: capturedID}
			}
			calls = append(calls, tools.Call{
				ID:    pt.ID,
				Name:  pt.Name,
				Input: pt.Input,
			})
		}

		results := capturedExecutor.ExecuteBatch(ctx, calls)
		cancel()
		return toolResultMsg{results: results, requestID: capturedID}
	}
}

// otherOptionValue 是「None of the above / Other」选项的哨兵值（2026-09-21 · AskUser 对齐 同类 CLI）。
// TUI 在 options 末尾自动追加一个 Other 选项（对齐 同类 CLI request_user_input 的 is_other：客户端补，模型不提供）。
// 用户选中它（Answer == otherOptionValue）时进入「补充自由输入」子态。
const otherOptionValue = "__other__"

// otherOptionLabel 是 Other 哨兵项的显示标签，同时也是**回传给模型的 answer 段**（2026-09-23 ·）。
//
// ★ 为什么「标签本身」必须回传：同类 CLI 在用户选 Other 时，answers[0] 就是 OTHER_OPTION_LABEL 本身
// （request_user_input/mod.rs:58 定义，:3175-3176 的用例为 answers = ["None of the above", "user_note: Custom answer"]）。
// whale 修前把 answer 置成空串 ⇒ 模型只收到一句**无来源标注的裸文本**，无从判断它是「以上都不是」的自填内容，
// 只好退回自己入参里的 default_idx ⇒ 答成选项 0（真机 2026-09-23 · 会话存档 01a0c506：
// 回传 output="回复hello"，模型却答"你刚才选择的是 JWT"）。
// ⇒ 标签是「这次回答不是任何预设项」的唯一结构信号，丢了它模型就只能猜。
const otherOptionLabel = "None of the above"

// appendOtherOption 把 Other 选项追加到 ChoiceOption 切片末尾（单一实现点，两处构造都走这里）。
// 对齐 同类 CLI：Other 文案英文主行 + 中文辅助层（形态 SSOT = bilingual.go）。
func appendOtherOption(options []ChoiceOption) []ChoiceOption {
	return append(options, ChoiceOption{
		Label:         otherOptionLabel,
		Value:         otherOptionValue,
		Description:   "Provide your own answer",
		DescriptionZH: "自定义答案",
	})
}

// buildAskUserChoiceOptions 把 AskUser 的选项映射成 ChoiceOption，并**总是**追加客户端 Other。
//
// ★ 「选项列表 + Other 逃生项」的**唯一实现** —— `checkAskUserChoice`（主路径）与
// `routeAskUserFallback`（兜底路径）**都必须走它**，别各写一份。2026-09-23 · 就是在这
// 两处各写了一份条件（`if askResult.AllowFreeText`）， 撤回时若只改一处，两条回传路径
// 会分叉成「一条有 Other、一条没有」——这类「同一语义两处实现」是本仓的复发坑。
//
// 为什么 Other 无条件是必须的（，逐字核源码）：
// - 同类 CLI：`is_other` **不在模型可见的 schema 里**，由服务端 normalize 强制 true
// （`对标证据件`），且 `options` 必填（:64）。
// 工具说明还明写 "Do not include an \"Other\" option in this list; the client will add a
// free-form \"Other\" option automatically."（:31）
// - 同类 CLI：`t7 = [...textOptions, otherOption]` 无条件追加（`QuestionView.tsx:222`）。
//
// ⇒ 两类都不让模型关掉自填口；`allow_free_text` **不**参与本判定（它只管 options 可否为空）。
func buildAskUserChoiceOptions(askResult tools.AskUserResult) []ChoiceOption {
	options := make([]ChoiceOption, len(askResult.Options))
	for i, opt := range askResult.Options {
		options[i] = ChoiceOption{
			Label:       opt.Label,
			Value:       opt.Value,
			Description: opt.Description,
		}
	}
	return appendOtherOption(options)
}

// checkAskUserChoice 检查 tool results 中是否有 AskUser 调用需要交互选择。
// 352
// SNAPSHOT: 原 fn 返回 (*PendingChoice, bool) 仅第一个命中；T3 S2 改为 ([]PendingChoice, bool) 支持批量。
// 同时加 4 项前置判断：
// 1. 仲裁逻辑（REF-降级策略-优化8）：若编排器侧提问中（pendingOrchAskReq != nil），日常对话侧 AskUser 直接跳过。
// 2. 频率拦截（REF-降级策略-优化4）：askCount >= ask_threshold 时跳过（T3 S3）。
// 3. allow_free_text=true 且 options 为空时跳过 PendingChoice 渲染（REF-降级策略-优化2）。
// 4. 缓存命中时跳过（REF-降级策略-优化3，T3 S3）。
// 命中时 Prompt 文本经 escapePrompt 过滤防 prompt injection（REF-安全-优化6）。
func (m *Model) checkAskUserChoice(results []tools.Result) ([]PendingChoice, bool) {
	// 仲裁逻辑（REF-降级策略-优化8：编排器侧优先，复用 T2-A 已有 pendingOrchAskReq 字段）
	if m.pendingOrchAskReq != nil {
		return nil, false
	}
	// 频率拦截（REF-降级策略-优化4：ask_threshold 控制）
	if m.askSettings != nil && m.askCount >= m.askSettings.AskThreshold {
		return nil, false
	}

	var choices []PendingChoice
	for _, r := range results {
		if r.Name == "AskUser" && !r.IsError {
			var askResult tools.AskUserResult
			if json.Unmarshal([]byte(r.Output), &askResult) == nil {
				// allow_free_text=true 且 options 为空时跳过 PendingChoice 渲染（REF-降级策略-优化2）
				if askResult.AllowFreeText && len(askResult.Options) == 0 {
					continue
				}
				if len(askResult.Options) > 0 {
					options := buildAskUserChoiceOptions(askResult)
					choices = append(choices, PendingChoice{
						Prompt:     escapePrompt(askResult.Question), // REF-安全-prompt injection 防御-优化6
						Options:    options,
						ToolID:     r.ID,
						DefaultIdx: askResult.DefaultIdx,
						Category:   askResult.Category,
					})
				}
			}
		}
	}

	// 缓存命中检查（REF-降级策略-优化3，T3 S3）
	var filtered []PendingChoice
	for _, ch := range choices {
		if m.askCache != nil {
			if _, hit := m.askCache.Get(ch.Category, ch.Prompt); hit {
				continue // 缓存命中，跳过
			}
		}
		filtered = append(filtered, ch)
	}
	if len(filtered) > 0 {
		m.askCount++
	}
	return filtered, len(filtered) > 0
}

// escapePrompt 转义 Prompt 文本防止终端渲染异常（REF-安全-prompt injection 防御-优化6）。
// 去除 ANSI 转义序列（\x1b[...m）和控制字符（< 0x20 除 \n），保留可打印字符。
func escapePrompt(s string) string {
	var buf strings.Builder
	inEscape := false
	for _, r := range s {
		if r == 0x1b {
			inEscape = true
			continue
		}
		if inEscape {
			// ANSI 转义序列以 'm' 或字母（a-zA-Z）结束
			if r == 'm' || (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') {
				inEscape = false
			}
			continue
		}
		// 保留可打印字符 + 换行符
		if r >= 0x20 || r == '\n' {
			buf.WriteRune(r)
		}
	}
	return buf.String()
}

// abortRestOfBatch 是 的**单一实现点**：本批在前一条调用被**终局拒绝**后，
// 为剩余调用补"未执行"占位结果。
//
// 缺陷本体：两处批循环在 `!pr.Proceed` 时只是**记一条拒绝然后继续下一条** ⇒
// 后续调用会被**照常询问、照常执行**。用户的"拒绝"只对当前那一条生效，
// 与"我拒绝了这一批"的直觉不符（一次拒绝几乎不产生约束力）。
//
// 修法：**短路**。剩余调用**不再进入管线**（既不询问也不执行），只补占位结果。
//
// 为什么要补占位而不是干脆不产出：一条 assistant 消息里的每个 tool_use 都必须有
// 配对的 tool_result（否则下一次请求的工具配对不成立，网关侧直接报错）。
// 占位结果**显式声明"没跑"**，而不是留空或伪造成功。
//
// ★ 两处批循环（`executePipelineRemaining` / `executeToolsDirect`）**必须共用本函数**
// （本仓 教训："双实现 ⇒ 一侧改了另一侧没改"）。
func abortRestOfBatch(results []tools.Result, calls []session.ContentBlock, from int, causeID string) []tools.Result {
	for j := from; j < len(calls); j++ {
		results = append(results, tools.Result{
			ID:   calls[j].ID,
			Name: calls[j].Name,
			// ★ ：双语 SSOT 形态（biSame）—— 修前是
			// "Not run / 未执行\n…" 行内并列（中文行无缩进，过不了形态门）。
			Output: biSame(
				"Not run\nThis call was terminated after an earlier call (%s) in this batch was denied; it was neither confirmed nor executed.",
				"未执行\n这一批已在前一条调用（%s）被拒绝后终止；本调用既未被询问，也未被执行。",
				causeID),
			IsError: true,
		})
	}
	return results
}

func pipelineRejectionResult(toolCall session.ContentBlock, result *tools.PipelineResult) tools.Result {
	if result.FailureKind == tools.FailureKindPolicyDenied {
		return tools.Result{
			ID:   toolCall.ID,
			Name: toolCall.Name,
			// ★ ：双语 SSOT 形态（biSame）—— 修前是
			// "Permission denied / 权限拒绝\nTool / 工具: %s\n…" 行内并列。
			Output: biSame(
				"Permission denied\nTool: %s\nReason: %s\nNo operation was run.",
				"权限拒绝\n工具：%s\n原因：%s\n操作未执行。",
				toolCall.Name, result.Rejection),
			IsError:     true,
			FailureKind: tools.FailureKindPolicyDenied,
		}
	}
	// G9-①（S13）：default 分支**原样透传** FailureKind，**不得枚举具体取值**。
	//
	// 修复前这里不赋值 ⇒ 除 PolicyDenied 外的**所有**拒绝种类在 TUI 侧塌成零值。
	// 当前种类有 3 个（tools.AllFailureKinds：sandbox_denied / policy_denied /
	// patch_conflict），将来还会新增。零值的两个后果：
	// · noteRoundSecurityDenials（本文件 :521 附近）只认 SandboxDenied/PolicyDenied
	// ⇒ R4 的权威判据 `roundDeniedKind != ""` 对**越界写恒为空**（真机 2026-09-16 02:03 实证）；
	// · handleToolResults 的 terminalFailureKind 回路据 FailureKind.Terminal() 判定
	// ⇒ 越界写不再终结本轮，模型可换工具在**同一条 deny 规则**下重试。
	//
	// 写成 `else if result.FailureKind == tools.FailureKindSandboxDenied` 是错解：
	// 下一个新增种类会继续漏 —— 本仓"两份清单漂移"的经典形态。透传后任何新增种类自动不再漏。
	return tools.Result{
		ID:          toolCall.ID,
		Name:        toolCall.Name,
		Output:      fmt.Sprintf("⚠️ Pipeline rejected: %s", result.Rejection),
		IsError:     true,
		FailureKind: result.FailureKind, // G9-①：原样透传，不枚举
	}
}

// noteRoundSecurityDenials 把本轮工具结果里的**结构化**安全拒绝登记进账本
// （Model.roundDeniedKind）。R4（S17 P3 / V3-6-3）的权威判据就是它。
//
// 只认 tools.Result.FailureKind，**绝不**从 Output 文本反推——"拿散文猜拒绝"
// 正是本 bug 的错设计（任何提到沙箱的正常回答都会被判成执行被拦截）。
// 同批出现多种拒绝时后者覆盖前者（与 handleToolResults 的 terminalFailureKind 同口径）。
// 判据改为 `FailureKind.IsSecurityDenial()`（tools.go:160 的
// SSOT），**不得**在本函数里再枚举具体种类。
//
// 修复前这里是 `switch r.FailureKind { case SandboxDenied, PolicyDenied: }` ——
// 一份**与 `failureKindSpecs` 平行的手抄清单**。两者今天恰好同集，但语义无关：
// 将来新增一个 `SecurityDenial: true` 的种类（如传闻中的 `rule_hard_deny`），
// tools 侧的 `failureKindSpecs` 会登记它，而本函数会**静默漏记** ⇒ R4 安全账本
// 的权威判据 `roundDeniedKind != ""` 对该种类恒为空。
// ★ 这正是本仓"两份清单漂移"的经典形态：修法不是补 case，而是**删掉清单**、
//
//	把判定收到唯一声明上。
func (m *Model) noteRoundSecurityDenials(results []tools.Result) {
	for _, r := range results {
		if r.FailureKind.IsSecurityDenial() {
			m.roundDeniedKind = r.FailureKind
		}
	}
}

// handleToolResults 处理工具执行结果，并衔接等待选择或下一轮 AI 流。
func (m *Model) handleToolResults(results []tools.Result) tea.Cmd {
	if m.sessionService.Interrupted() {
		m.sessionService.SetState(StateIdle)
		m.uiService.SetInputFocusedOnStop(false)
		m.resetStreamState()
		m.recalcViewportHeight()
		return nil
	}

	toolMessages := make([]session.Message, 0, len(results))
	hasPendingChoice := false
	var terminalFailureKind tools.FailureKind
	for _, r := range results {
		// 纵深防御（REF-方案-TUI输入与工具调用渲染修复 §4.3）：AskUser 结果绝不显示原始 JSON、
		// 绝不回传原始 JSON 给模型。checkAskUserChoice 是首选拦截；此处兜底，确保任何漏网 AskUser
		// 都走交互选择 UI，而非把 question/options JSON 回传模型导致"自顾自续写"。
		if r.Name == "AskUser" && !r.IsError {
			if m.routeAskUserFallback(r) {
				hasPendingChoice = true
				continue
			}
			// 无法渲染交互 UI（编排器竞争/提问禁用/解析失败/无 options）：
			// 回传中性文本引导模型不再调用 AskUser，不显示原始 JSON。
			toolMessages = append(toolMessages, session.ToolResult(r.ID, "AskUser", "[AskUser: skipped, do not call AskUser again, proceed with best judgment]", false))
			continue
		}
		// 实时路径：本条结果**尚未**写进 session ⇒ 序号 = 现有总数 + 1。
		// 与 /tool 的 [N/M]、折叠提示的 "/tool <N>" 共用 SSOT toolResultSeq。
		display := tools.FormatToolResultStructWithHint(r, tools.MaxToolResultLength,
			tools.ToolFoldHint{Command: "/tool",
				Seq: newToolResultSeq(m.sessionService.CurrentSession().GetMessages()).next()})
		if r.FailureKind.Terminal() {
			terminalFailureKind = r.FailureKind
		}
		m.addMessage(display)

		toolMessages = append(toolMessages, session.ToolResult(r.ID, r.Name, r.Output, r.IsError))
	}

	// V3-6-2：把工具结果喂给循环检测器（原幻觉记录已随 移除，2026-10-08）。
	m.checkLoopBehavior(results)

	// Hard sandbox and permission denials are terminal for this turn. Returning
	// them to the model invited alternate tools that are governed by the same
	// deny rule, producing noisy write_file -> bash -> edit_file loops.
	if terminalFailureKind != "" {
		m.sessionService.CurrentSession().SetMessages(append(m.sessionService.CurrentSession().GetMessages(), toolMessages...))
		m.completeBlockedTurn(terminalFailureKind)
		return m.restoreIdleMouse()
	}

	// 有 AskUser 待选择：保存普通工具结果到 session，进入 StateWaitingChoice 等用户选择。
	// choiceResultMsg 处理时（app.go:1641 分支）追加 AskUser tool_result 并 startAIStream。
	// 不在此处重启 AI 流，避免模型把 question JSON 当答案续写。
	if hasPendingChoice {
		if len(toolMessages) > 0 {
			m.sessionService.CurrentSession().SetMessages(append(m.sessionService.CurrentSession().GetMessages(), toolMessages...))
		}
		// P0 记账：AskUser 轮也计入观测（轮次/纯读计数），否则审查子循环上限
		// 可被 AskUser 交错规避；不跑熔断判定（等待用户输入不是工具失败）。
		if m.guard != nil {
			m.guard.AfterTurn(turnObservation(
				m.toolService.PendingTools(), results,
				lastAssistantText(m.sessionService.CurrentSession().GetMessages())))
		}
		m.sessionService.SetState(StateWaitingChoice)
		m.recalcViewportHeight()
		return nil
	}

	m.sessionService.CurrentSession().SetMessages(append(m.sessionService.CurrentSession().GetMessages(), toolMessages...))

	// P0 agent 防护：每轮工具落定后记录观测 + 无进展熔断判定。
	// 必须在 resetStreamState 前调用（观测器从 PendingTools 读取本轮工具调用入参）。
	// Stop 时已渲染进度快照并交回控制权；透传停机收尾命令（drain 排队输入/
	// 异步自动压缩）与鼠标恢复，不再发起下一轮模型调用。
	if cmd, stopped := m.guardAfterToolRound(results); stopped {
		return tea.Batch(cmd, m.restoreIdleMouse())
	}

	m.sessionService.SetState(StateStreaming)
	m.resetStreamState()

	return tea.Batch(m.startAIStream(), m.restartTickCmd())
}

// completeBlockedTurn closes a turn after a hard sandbox or permission denial.
// The explanation is local so the model cannot retry through an equivalent
// tool or add a second refusal banner.
func (m *Model) completeBlockedTurn(kind tools.FailureKind) {
	// ★ ：双语 SSOT 形态（bi）—— 修前是「英文段\n\n中文段」，
	// 中文行无缩进过不了形态门（util.AssertChineseOnlyOnIndentedLines）。
	// ★ ：改用 AssistantNotice —— 这条是**程序写的说明**，
	// 必须走纯文本通道；否则 bi 的「英文行\n 中文行」被 Markdown 折成一行
	// （真机实测：终端越宽越必然并成 "English 中文"）。
	explanation := bi(
		"The command was blocked by the active sandbox. No command was run, and no alternate command or interpreter will be attempted. Change the sandbox setting explicitly before submitting the request again.",
		"命令已被当前沙箱拦截，未执行，也不会改用其他命令或解释器重试该操作。请先明确修改沙箱设置，再重新提交请求。")
	if kind == tools.FailureKindPolicyDenied {
		explanation = bi(
			"Permission denied. No operation was run, and no alternate tool will be attempted.",
			"权限拒绝。操作未执行，也不会改用其他工具重试。")
	}
	assistantMsg := session.AssistantNotice(explanation, m.sessionService.ModelName())
	m.sessionService.CurrentSession().AddMessage(assistantMsg)
	display, srcs := formatAssistantMessageWithThinkingSource(
		assistantMsg.Blocks,
		"",
		assistantMsg.Timestamp,
		m.contentWrapWidth(),
	)
	m.addMessageWithSources(display, srcs)
	m.resetStreamState()
	m.sessionService.SetState(StateIdle)
	m.uiService.SetInputFocusedOnSend(false)
	m.uiService.SetInputFocusedOnStop(false)
	m.uiService.Textarea().Focus()
	if m.refusalHandler != nil {
		m.refusalHandler.SetForceExecuteMode(false)
	}
	m.finishTurn(true)
	m.recalcViewportHeight()
}

// routeAskUserFallback handleToolResults 的 AskUser 兜底渲染：解析结果并渲染交互选择 UI。
// 成功渲染返回 true（已设 StateWaitingChoice 等用户选择）；失败返回 false（调用方回传中性文本）。
// 复用 app.go:1571-1577 的 PendingChoice 设置逻辑；guard 编排器竞争与提问禁用场景。
func (m *Model) routeAskUserFallback(r tools.Result) bool {
	// 编排器侧提问中：不竞争渲染 UI（避免双重提问）。
	if m.pendingOrchAskReq != nil {
		return false
	}
	// 提问已禁用（AskThreshold=0）或超阈值：尊重配置，不渲染 UI。
	if m.askSettings != nil && m.askCount >= m.askSettings.AskThreshold {
		return false
	}
	var askResult tools.AskUserResult
	if json.Unmarshal([]byte(r.Output), &askResult) != nil {
		return false
	}
	if len(askResult.Options) == 0 {
		return false
	}
	options := buildAskUserChoiceOptions(askResult)
	choiceIdx := 0
	if askResult.DefaultIdx >= 0 && askResult.DefaultIdx < len(options) {
		choiceIdx = askResult.DefaultIdx
	}
	m.toolService.SetPendingChoice(&PendingChoice{
		Prompt:     escapePrompt(askResult.Question), // REF-安全-prompt injection 防御
		Options:    options,
		ToolID:     r.ID,
		DefaultIdx: askResult.DefaultIdx,
		Category:   askResult.Category,
	})
	m.toolService.SetChoiceIdx(choiceIdx)
	m.addMessage(toolStyle.Render(renderAskQuestionLine(askResult.Category, escapePrompt(askResult.Question))))
	m.askCount++ // 与 checkAskUserChoice 一致：兜底渲染 UI 也递增频率计数（REC-4 修复）
	// recalcViewportHeight 由调用方 handleToolResults 的 hasPendingChoice 分支统一调用（REC-5 修复，避免重复）
	return true
}

// checkLoopBehavior 把本批工具结果喂给循环检测器。
// 从 pendingTools 提取工具输入参数，供循环指纹比对。
// 循环检测：同一工具+同一参数跨轮连续调用超过阈值时发出警告。
// （原 hallucinationDetector 记录已随 整体移除，2026-10-08。）
// nil 安全（未初始化时静默跳过）。
func (m *Model) checkLoopBehavior(results []tools.Result) {
	if m.loopDetector == nil {
		return
	}

	// 从 pendingTools 构建 toolID → input 映射（resetStreamState 前读取）
	pendingTools := m.toolService.PendingTools()
	inputByID := make(map[string]string, len(pendingTools))
	for _, pt := range pendingTools {
		inputByID[pt.ID] = pt.Input
	}

	// V3-6-2：检测循环行为（同一工具+同一参数跨轮连续调用超过阈值）。
	// 修复（TUI 实测误报）：改用 CheckLoopBatch 一次喂"本轮（同一条 assistant 消息）"
	// 的全部工具结果，批内并列同指纹只计一次 → 并发发多个同参数子 agent 不再误报
	// "循环 3/4/5/6"；跨批失控（模型多轮反复做同一操作）仍会被 CheckLoopBatch 累计检测。
	calls := make([]ai.Call, 0, len(results))
	for _, r := range results {
		calls = append(calls, ai.Call{ToolName: r.Name, Input: inputByID[r.ID], Success: !r.IsError})
	}
	for _, warn := range m.loopDetector.CheckLoopBatch(calls) {
		m.addMessage(errorStyle.Render(warn))
	}
}

// --- 请求内审批复用（「同一个副作用不被问两次」） ---
//
// 背景（2026-09-20 实测）：确认层是**逐调用**裁决的，而模型会发出**逐字相同**的调用 ——
// 实测两种形态：① 同一条 assistant 消息里两条逐字相同的 `mkdir -p X`；
// ② 「跑一下 ls -la」里模型答完（还反问"需要我帮你做什么"）之后，在**同一条消息里**
// 又挂了一次同样的 `ls -la`（跨两条消息、两个 agent turn，但**同一个用户请求**）。
// 用户对同一个确切输入已经说过 Yes，第二次再问不提供任何新的决策信息。
//
// 作用域 = **一次用户请求**（不是一批，也不是整个会话）：
// - 比"仅同一条消息"宽 —— 否则修不了形态 ②（跨消息）；
// - 比"整个会话免问"（面板 `[A]`）窄 —— 退出本次请求即失效。
//
// 危险面**不在**复用范围内，但**不是**靠这里判定的：`ReusedApproval` 只被
// PermissionStage 的免问块读取，而危险档/破坏性检查排在它之前且带 NeedConfirm
// ⇒ 管线更早就短路了（详见 tools.Call.ReusedApproval 的字段注释与
// pipeline.go 的 (r) 出口注释）。⇒ 这里**不得**引入"是不是危险命令"的第二套判据。

// approvalReuseKey 是请求内审批复用的键：工具名 + 输入，逐字相同才复用。
// 用 NUL 分隔，避免 ("a","bc") 与 ("ab","c") 拼出同一个键。
func approvalReuseKey(name, input string) string {
	return name + "\x00" + input
}

// requestsApprovalReuse 回答「本次调用能否复用**同一请求内**已获批的相同调用」。
//
// reqID 取 m.aiService.RequestID()：由 handleSubmit 在**用户提交**时生成
// （stream.go:218/311），在此后整个 agent loop（多轮工具往返）内保持不变 ——
// 这正是 approval_audit.go:71 写进账本的 request_id 跨轮相同的原因。
//
// reqID 为空时**恒不复用**：Plain/Bridge 适配器的 RequestID() 恒返回 ""
// （service_adapter_ai.go:102），那种入口没有"用户请求"这个边界
// ⇒ 宁多问，不可跨请求免问。
func (m *Model) requestsApprovalReuse(name, input, reqID string) bool {
	if reqID == "" || m.reqApprovals == nil {
		return false
	}
	id, ok := m.reqApprovals[approvalReuseKey(name, input)]
	return ok && id == reqID
}

// noteRequestApproval 记录「本次请求内，用户已对 (name, input) 这个确切调用说过 Yes」。
//
// ★ 唯一写入点是审批决策的**消费点**（app.go `case confirmResultMsg` 的 approved 分支），
// 与 recordApprovalDecision 挂同一处。理由同该处注释：挂发射点 = 多处重复插入、
// 漏一处就漏一类决策；消费点只有 1 处。
//
// 顺带清掉属于别的请求的条目（内存卫生）。**判定本身不依赖清理** ——
// requestsApprovalReuse 按 reqID 比对，过期条目天然不复用。
//
// 线程：只在 Update 线程调用（Model 字段不跨 goroutine 共享；批循环闭包在
// tea.Cmd 的 goroutine 里跑，因此那里**只读**构造期快照，见 executePipelineRemaining）。
func (m *Model) noteRequestApproval(name, input, reqID string) {
	if m.reqApprovals == nil {
		// 仅非常规构造路径（如测试直接拼 Model）会走到；生产由 lifecycle.go 的
		// NewModel 保证已初始化。此处 make 只写本副本的字段，见 Model.reqApprovals 注释。
		m.reqApprovals = make(map[string]string)
	}
	for k, v := range m.reqApprovals {
		if v != reqID {
			delete(m.reqApprovals, k)
		}
	}
	m.reqApprovals[approvalReuseKey(name, input)] = reqID
}
