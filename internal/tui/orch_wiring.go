// orch_wiring.go — H10 Phase 6 编排器 TUI 接线层（S4，）。
// 把 Orchestrator 实例和 OrchBridge 注入到 TUI Model，注册 /orchestrate 命令回调。
//
// 关键设计：
// - /orchestrate start/status/list/resume/abort 同步执行（快速文件 I/O）
// - /orchestrate clarify/plan/contract 异步执行（goroutine + tea.Msg 回传）
// 原因：clarify/contract 调 AskUser 同步阻塞等待用户输入，plan 调 LLM 可能慢；
// 若同步执行会阻塞 TUI 主循环，导致用户无法回答 AskUser 提问（死锁）。
// - 异步命令完成后通过 m.program.Send(orchCmdResultMsg) 回传结果到 TUI 主循环
//
// §4 S4 main.go 接线；落地：SetOrchestrator + SetOrchBridge + orchCmdResultMsg
// ；落地：异步 goroutine 中执行，失败通过 orchCmdResultMsg.err 回传
// SNAPSHOT: pkg=internal/tui; types={orchCmdResultMsg}; methods={SetOrchBridge,SetOrchestrator}; async_cmds={clarify,plan,contract}; sync_cmds={start,status,list,resume,abort}; comm=program.Send(orchCmdResultMsg)

package tui

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/anchorandfish/whale-code/internal/orchestrator"
)

// orchCmdResultMsg 异步编排命令完成后的回传消息（S4 接线）。
// clarify/plan/contract 在 goroutine 中执行，完成后通过 program.Send 发送到 TUI 主循环。
type orchCmdResultMsg struct {
	output string
	err    error
}

// orchAsyncSubcmds 需要异步执行的 /orchestrate 子命令集合（S4 接线）。
// 这些子命令涉及 LLM 调用或 AskUser 阻塞等待，必须在 goroutine 中执行。
var orchAsyncSubcmds = map[string]bool{
	orchestrator.OrchestrateSubClarify:  true,
	orchestrator.OrchestrateSubPlan:     true,
	orchestrator.OrchestrateSubContract: true,
}

// SetOrchBridge 注入编排器桥接器到 TUI Model（S4 接线）。
// 在 NewModel 之后、tea.NewProgram 之前调用。
// bridge 为 nil 时关闭编排器桥接（TUI 不监听 askCh/contractCh）。
func (m *Model) SetOrchBridge(bridge *OrchBridge) {
	m.orchBridge = bridge
}

// SetExperienceOrchestrator 注入经验编排器（B-P0-5 修复：实例化后 lifecycle.go:889 guard 自动放行 wireH12Evolution）。
// 在 NewModel 之后、tea.NewProgram 之前调用。orch 为 nil 时 H12 经验学习功能降级（wireH12Evolution 跳过）。
func (m *Model) SetExperienceOrchestrator(orch *orchestrator.ExperienceOrchestrator) {
	m.experienceOrch = orch
}

// SetExpStore 注入经验库存储（B-P0-5 修复：wireH12Evolution 依赖）。
func (m *Model) SetExpStore(store *orchestrator.ExperienceStore) {
	m.expStore = store
}

// SetQualityStore 注入质量存储（B-P0-5 修复：wireH12Evolution 依赖）。
func (m *Model) SetQualityStore(store *orchestrator.QualityStore) {
	m.qualityStore = store
}

// SetL1Scorer 注入 L1 评分器（B-P0-5 修复：wireH12Evolution 依赖）。
func (m *Model) SetL1Scorer(scorer *orchestrator.L1ScorerExt) {
	m.l1Scorer = scorer
}

// SetOrchestrator 注入编排器实例并注册 /orchestrate 命令回调（S4 接线，）。
// 在 NewModel 之后、tea.NewProgram 之前调用。
// orch 为 nil 时 /orchestrate 命令返回未初始化提示。
// programRef 用于异步命令完成后回传结果（SetProgram 之后调用）。
func (m *Model) SetOrchestrator(orch *orchestrator.Orchestrator) {
	if orch == nil {
		m.cmdRegistry.OnOrchestrate = func(args string) (string, error) {
			return "Orchestrator not initialized", nil
		}
		return
	}

	// 注册 OnOrchestrate 回调，区分同步/异步子命令
	m.cmdRegistry.OnOrchestrate = func(args string) (string, error) {
		parts := strings.Fields(args)
		if len(parts) == 0 {
			// 无子命令，走同步帮助路径
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			return orch.Handle(ctx, args)
		}

		subcmd := parts[0]

		// 异步子命令：clarify/plan/contract
		if orchAsyncSubcmds[subcmd] {
			// 检查 program 引用（异步回传必需）
			prog := m.program.get()
			if prog == nil {
				return "", fmt.Errorf("orchestrate %s: TUI program not initialized", subcmd)
			}

			// 启动 goroutine 异步执行，立即返回"任务已启动"提示
			go func() {
				// 异步命令不设超时（AskUser 可能等待用户 30 分钟）
				ctx := context.Background()
				output, err := orch.Handle(ctx, args)
				prog.Send(orchCmdResultMsg{output: output, err: err})
			}()

			return fmt.Sprintf("编排任务 %s 已启动，请等待完成...", subcmd), nil
		}

		// 同步子命令：start/status/list/resume/abort
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		return orch.Handle(ctx, args)
	}
}
