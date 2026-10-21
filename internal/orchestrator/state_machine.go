// state_machine.go — 模块 1 状态机推进逻辑 + 校验函数注册表（H10 Phase 1 编排层 §2.2/§2.3）。
// REF-外部-LangGraph@官方文档+2026-07-15访问-State + checkpoint + human-in-loop；借鉴逻辑：状态序列化 + Restore；落地：StateMachine.Checkpoint/Restore 方法
// REF-外部-同类CLI@2026-03-31快照-Task tool 派发无状态机（H10 超越点）；落地：H10 在 subagent 之上加 StateMachine
// 主入口；落地：StateMachine 是 Orchestrator 的子组件
// 轮硬上限 + 停机判定权在外部；落地：每 Stage 失败修一次即进入下一 Stage
// SNAPSHOT: pkg=internal/orchestrator; type=StateMachine; methods={Advance()error,Checkpoint()State,Restore(State)error,CanTransition(from,to)bool}; flow=linear(非DAG); stages=7(init+6+done); fail_policy=修一次进下一Stage; max_retry_per_stage=1; user_callstop→interrupted

package orchestrator

import (
	"fmt"
	"sync"
	"time"
)

// StageDef Stage 定义（声明式，编排层 §2.3）。
type StageDef struct {
	ID           string `json:"id"`
	Type         string `json:"type"`
	UserConfirm  bool   `json:"user_confirm"`
	AdvanceGuard string `json:"advance_guard"`
	OnFail       string `json:"on_fail"`
}

// OnFail 枚举常量（编排层 §2.3）。
const (
	OnFailRetryOnce = "retry_once"
	OnFailInterrupt = "interrupt"
	OnFailAbort     = "abort"
)

// legalTransitions 合法转移表（编排层 §2.2）。
// key = from Stage, value = 允许转移到的 Stage 集合。
// 致命反例：done → 任何 Stage 必须拒绝（done 是终态）。
var legalTransitions = map[string]map[string]bool{
	StageInit: {
		StageClarify: true,
	},
	StageClarify: {
		StagePlan:        true,
		StageInterrupted: true,
	},
	StagePlan: {
		StageContract:    true,
		StageClarify:     true, // 回退重问
		StageInterrupted: true,
	},
	StageContract: {
		StageImplement:   true,
		StagePlan:        true, // 回退修一次
		StageInterrupted: true,
	},
	StageImplement: {
		StageReview:      true,
		StageContract:    true, // 回退修一次
		StageInterrupted: true,
	},
	StageReview: {
		StageTest:        true,
		StageImplement:   true, // 回退修一次
		StageInterrupted: true,
	},
	StageTest: {
		StageDone:        true,
		StageImplement:   true, // 回退修一次
		StageInterrupted: true,
	},
	StageDone: {}, // 终态：无任何转移允许（done→implement 必须拒绝，致命反例）
	StageInterrupted: {
		StageInit:      true, // 重做
		StageClarify:   true,
		StagePlan:      true,
		StageContract:  true,
		StageImplement: true,
		StageReview:    true,
		StageTest:      true,
		// done 不能从 interrupted 直接到（需经过 test Stage）
	},
}

// defaultStages 默认 Stage 序列（线性顺序，Phase 1 不涉及 DAG，编排层 §2.2）。
var defaultStages = []StageDef{
	{ID: StageClarify, Type: "interactive", UserConfirm: true, AdvanceGuard: "checkClarifyOutput", OnFail: OnFailRetryOnce},
	{ID: StagePlan, Type: "agent", UserConfirm: true, AdvanceGuard: "checkPlanOutput", OnFail: OnFailRetryOnce},
	{ID: StageContract, Type: "agent", UserConfirm: true, AdvanceGuard: "checkContractOutput", OnFail: OnFailRetryOnce},
	{ID: StageImplement, Type: "agent", UserConfirm: false, AdvanceGuard: "checkImplementOutput", OnFail: OnFailRetryOnce},
	{ID: StageReview, Type: "agent", UserConfirm: true, AdvanceGuard: "checkReviewOutput", OnFail: OnFailRetryOnce},
	{ID: StageTest, Type: "auto", UserConfirm: false, AdvanceGuard: "checkTestOutput", OnFail: OnFailRetryOnce},
}

// GuardFunc 流转条件校验函数类型（注册表查，编排层 §2.3）。
type GuardFunc func(state State) error

// StateMachine 状态机（编排层 §2.3，线性 Stage 推进）。
type StateMachine struct {
	mu      sync.Mutex
	current string
	stages  []StageDef
	taskID  string
	guards  map[string]GuardFunc
	// failCount 当前 Stage 失败次数（1 轮硬上限）
	failCount map[string]int
}

// NewStateMachine 创建状态机，初始 Stage=init（编排层 §2.3）。
func NewStateMachine(taskID string) *StateMachine {
	return &StateMachine{
		current:   StageInit,
		stages:    defaultStages,
		taskID:    taskID,
		guards:    make(map[string]GuardFunc),
		failCount: make(map[string]int),
	}
}

// RegisterGuard 注册流转条件校验函数（编排层 §2.3 advance_guard 注册表）。
func (sm *StateMachine) RegisterGuard(name string, fn GuardFunc) {
	sm.mu.Lock()
	defer sm.mu.Unlock()
	sm.guards[name] = fn
}

// CanTransition 校验 Stage 间流转是否合法（编排层 §2.2）。
// 致命反例：done→implement 必须返回 false（零容忍）。
func (sm *StateMachine) CanTransition(from, to string) bool {
	allowed, ok := legalTransitions[from]
	if !ok {
		return false
	}
	return allowed[to]
}

// Advance 推进到下一 Stage（执行 advance_guard 校验，通过则推进，失败按 OnFail 处理，编排层 §2.3）。
// 调用前需通过 RegisterGuard 注册当前 Stage 的 advance_guard 校验函数。
func (sm *StateMachine) Advance() error {
	sm.mu.Lock()
	defer sm.mu.Unlock()

	// 找到当前 Stage 在序列中的位置
	currentIdx := -1
	for i, s := range sm.stages {
		if s.ID == sm.current {
			currentIdx = i
			break
		}
	}
	if currentIdx == -1 {
		// 当前不在 Stage 序列中（init 或 interrupted 或 done）
		if sm.current == StageInit {
			// init → clarify
			sm.current = StageClarify
			sm.failCount = make(map[string]int)
			return nil
		}
		if sm.current == StageInterrupted {
			return fmt.Errorf("state_machine: cannot Advance from interrupted, use Restore first")
		}
		if sm.current == StageDone {
			return fmt.Errorf("state_machine: cannot Advance from done (terminal state)")
		}
		return fmt.Errorf("state_machine: unknown current stage %q", sm.current)
	}

	// 校验 advance_guard
	stage := sm.stages[currentIdx]
	if stage.AdvanceGuard != "" {
		if guard, ok := sm.guards[stage.AdvanceGuard]; ok && guard != nil {
			state := sm.checkpointLocked()
			if err := guard(state); err != nil {
				// 校验失败，按 OnFail 处理
				return sm.handleFailLocked(stage, err)
			}
		}
	}

	// 推进到下一 Stage
	if currentIdx+1 >= len(sm.stages) {
		// 最后一个 Stage（test）→ done
		sm.current = StageDone
	} else {
		sm.current = sm.stages[currentIdx+1].ID
	}
	sm.failCount = make(map[string]int)
	return nil
}

// handleFailLocked 处理 advance_guard 校验失败（持锁状态， 1 轮硬上限）。
func (sm *StateMachine) handleFailLocked(stage StageDef, guardErr error) error {
	switch stage.OnFail {
	case OnFailRetryOnce:
		sm.failCount[stage.ID]++
		if sm.failCount[stage.ID] > 1 {
			// 已修一次仍失败 → interrupt（1 轮硬上限）
			sm.current = StageInterrupted
			return fmt.Errorf("state_machine: stage %s failed after retry_once: %w", stage.ID, guardErr)
		}
		// 留在当前 Stage 重试（不推进）
		return fmt.Errorf("state_machine: stage %s guard failed (retry_once): %w", stage.ID, guardErr)
	case OnFailInterrupt:
		sm.current = StageInterrupted
		return fmt.Errorf("state_machine: stage %s interrupted: %w", stage.ID, guardErr)
	case OnFailAbort:
		sm.current = StageInterrupted
		return fmt.Errorf("state_machine: stage %s aborted: %w", stage.ID, guardErr)
	default:
		return fmt.Errorf("state_machine: stage %s unknown on_fail %q: %w", stage.ID, stage.OnFail, guardErr)
	}
}

// Checkpoint 生成当前状态快照（用于持久化到 state.json，编排层 §2.4）。
func (sm *StateMachine) Checkpoint() State {
	sm.mu.Lock()
	defer sm.mu.Unlock()
	return sm.checkpointLocked()
}

// checkpointLocked 持锁状态下的快照生成（内部调用）。
func (sm *StateMachine) checkpointLocked() State {
	return State{
		TaskID:    sm.taskID,
		Stage:     sm.current,
		SubTasks:  nil, // SubTasks 由编排器外部维护，Checkpoint 仅记录 Stage
		UpdatedAt: time.Now().Unix(),
	}
}

// Restore 从快照恢复状态机（断点续传，编排层 §2.5）。
func (sm *StateMachine) Restore(state State) error {
	sm.mu.Lock()
	defer sm.mu.Unlock()

	// 校验 Stage 合法性
	validStages := map[string]bool{
		StageInit: true, StageClarify: true, StagePlan: true, StageContract: true,
		StageImplement: true, StageReview: true, StageTest: true,
		StageDone: true, StageInterrupted: true,
	}
	if !validStages[state.Stage] {
		return fmt.Errorf("state_machine: invalid stage %q in restore state", state.Stage)
	}

	sm.current = state.Stage
	sm.taskID = state.TaskID
	sm.failCount = make(map[string]int)
	return nil
}

// Current 返回当前 Stage ID（仅用于诊断）。
func (sm *StateMachine) Current() string {
	sm.mu.Lock()
	defer sm.mu.Unlock()
	return sm.current
}

// degradeTransitions 是 SetCurrent 允许的降级目标白名单（H1-3 修复，）。
//
// SetCurrent 用于降级场景（LLM 失败跳过 clarify 等），原实现不校验 target 合法性，
// 存在跳转到非法状态的风险（如直接跳到 done 跳过 implement/review/test）。
// 白名单仅允许降级到"可恢复"状态（init/clarify/plan/contract/implement/review/test/interrupted），
// 不允许直接跳到 done（必须经过 test Stage 才能完成，避免跳过测试）。
var degradeTransitions = map[string]bool{
	StageInit:        true,
	StageClarify:     true,
	StagePlan:        true,
	StageContract:    true,
	StageImplement:   true,
	StageReview:      true,
	StageTest:        true,
	StageInterrupted: true,
	// StageDone 不在白名单：禁止通过 SetCurrent 直接跳到 done（必须经 test Stage）
}

// SetCurrent 直接设置当前 Stage（Phase 6 S1 新增，用于 advanceToStage 跳过中间 Stage）。
// 仅用于降级场景（LLM 失败跳过 clarify 等），正常流程应通过 Advance 推进。
//
// H1-3 修复（全模块审查）：原实现不校验 legalTransitions，
// 存在跳转到 done（跳过测试）或未知状态的风险。
// 修复：维护 degradeTransitions 白名单，仅允许降级到"可恢复"状态，
// 禁止直接跳到 done（必须经过 test Stage 才能完成）。
// target 不在白名单时返回 error（调用方应处理，通常走 interrupted）。
func (sm *StateMachine) SetCurrent(target string) error {
	sm.mu.Lock()
	defer sm.mu.Unlock()
	if !degradeTransitions[target] {
		// 非法降级目标：拒绝并记录当前状态，调用方应走 interrupted 兜底
		return fmt.Errorf("state_machine: SetCurrent rejected target %q (not in degrade whitelist), current=%q (use interrupted for recovery)", target, sm.current)
	}
	sm.current = target
	sm.failCount = make(map[string]int)
	return nil
}

// TaskID 返回状态机关联的 TaskID。
func (sm *StateMachine) TaskID() string {
	sm.mu.Lock()
	defer sm.mu.Unlock()
	return sm.taskID
}
