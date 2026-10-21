// Package tools — N1 subagent / Task tool 编排系统实现（02 §2.1.1 契约）。
//
// 本文件实现 Agent 接口（定义于 agent.go）的具体类型 AgentTask 与编排器
// AgentTaskManager，支持递归子任务（max_depth=3）、errgroup+semaphore 并发控制、
// 5 态状态机、cancel 传播、cost 透传。
//
// 设计要点：
// - AgentTask 独立于旧 AgentContext/ForkManager，保留旧实现作内部细节
// - runMultiRound 复用旧 AgentContext.Run 的多轮对话循环逻辑（不自实现）
// - 错误用 N6 的 ai.WithID 包装（ErrAgentTimeout / ErrAgentRecursionLimit）
// - 并发模型选型见 （errgroup + semaphore.Weighted + atomic.Int64）
// - 状态机见 （5 态 + isValidTransition 转移表）
// - 递归策略见 （max_depth=3 硬上限 + ParentID 链防环）
//
// 命名说明：编排器命名为 AgentTaskManager 而非 TaskManager，避免与 task.go 中
// 已有的 TaskManager（bash 后台任务管理器）冲突；二者职责不同，独立维护。
package tools

import (
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"time"

	"github.com/anchorandfish/whale-code/internal/ai"
	"golang.org/x/sync/errgroup"
	"golang.org/x/sync/semaphore"
)

// N1 subagent 常量（02 §2.1.1 契约 + /020 决策）。
const (
	// maxAgentDepth 最大递归深度（03 §3.4 要求 ≤3，硬上限，避免误配置）。
	maxAgentDepth = 3
	// defaultMaxConcurrent 最大并发执行数（参考同类实现 max_threads）。
	defaultMaxConcurrent = 5
	// defaultMaxSpawn 最大 spawn 总数（防资源耗尽）。
	defaultMaxSpawn = 20
	// defaultAgentTimeout 单任务超时（继承 AgentContext 现状）。
	defaultAgentTimeout = 120 * time.Second
	// defaultAcquireTimeout SpawnParallel 中 execLimiter.Acquire 等待超时
	//（v2 P-08：防 slot 被占满时无限阻塞）。
	defaultAcquireTimeout = 30 * time.Second
)

// =====================================================================
// 辅助函数
// =====================================================================

// generateAgentUUID 生成 UUID v4 字符串，作为 AgentTask 的唯一 ID。
// 使用 crypto/rand 实现（参考 session.generateUUID），避免引入 github.com/google/uuid 依赖。
// 失败时 panic：CSPRNG 不可用时无法保证 ID 唯一性。
func generateAgentUUID() string {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		panic(fmt.Sprintf("agent uuid: crypto/rand.Read failed: %v", err))
	}
	// RFC 4122 §4.4：设置 version (4) 和 variant (10xx) 位。
	b[6] = (b[6] & 0x0f) | 0x40
	b[8] = (b[8] & 0x3f) | 0x80
	return fmt.Sprintf("%08x-%04x-%04x-%04x-%012x",
		uint32(b[0])<<24|uint32(b[1])<<16|uint32(b[2])<<8|uint32(b[3]),
		uint32(b[4])<<8|uint32(b[5]),
		uint32(b[6])<<8|uint32(b[7]),
		uint32(b[8])<<8|uint32(b[9]),
		b[10:16],
	)
}

// extractAgentID 从 ParentCtx 提取 AgentID（v2 P-07：nil 安全）。
// parentCtx 为 nil 或不携带 agentIDKey 时返回空字符串，不 panic。
// 用于 02 §2.1.2 cost 归属：子任务从父任务上下文继承 AgentID。
func extractAgentID(parentCtx *context.Context) string {
	if parentCtx == nil {
		return "" // 根任务或未注入 AgentID 时返回空
	}
	if v := (*parentCtx).Value(agentIDKey{}); v != nil {
		if id, ok := v.(string); ok {
			return id
		}
	}
	return ""
}

// extractParentInfo 从 ctx 提取父任务 ID 与深度（用于 TaskTool.Call）。
// 返回值：parentID（空表示根任务）、parentDepth（0 表示根任务）。
// 这些值由 TaskTool 在调用 Spawn 前注入 ctx，用于建立 ParentID 链。
func extractParentInfo(ctx context.Context) (parentID string, parentDepth int) {
	if ctx == nil {
		return "", 0
	}
	if v := ctx.Value(agentParentIDKey{}); v != nil {
		if id, ok := v.(string); ok {
			parentID = id
		}
	}
	if v := ctx.Value(agentDepthKey{}); v != nil {
		if d, ok := v.(int); ok {
			parentDepth = d
		}
	}
	return parentID, parentDepth
}

// =====================================================================
// AgentTask — Agent 接口的具体实现
// =====================================================================

// AgentTask 是 Agent 接口的具体实现，独立于旧 AgentContext/ForkManager。
// 每个 AgentTask 实例对应一次子任务执行，状态机保护其生命周期。
type AgentTask struct {
	id        string             // 任务 ID（uuid）
	parentID  string             // 父任务 ID（空表示根任务）
	depth     int                // 当前深度（0=根，max_depth=3）
	agentType AgentType          // 角色类型（coder/plan/explore/verification）
	status    TaskStatus         // 当前状态（受 mu 保护）
	mu        sync.RWMutex       // 保护 status 与 cancel 字段
	cancel    context.CancelFunc // cancel 函数（传播到后代）
	createdAt time.Time          // 创建时间
	startedAt time.Time          // 启动时间
	endedAt   time.Time          // 终态时间
	result    string             // 最终输出
	err       error              // 终态错误

	// parentToolSet 是父工具面（委派不扩权，L2/I2），由 Spawn 从 AgentInput 注入。
	// nil = 不限（信任根）。
	parentToolSet map[string]bool

	// manager 反向引用，用于资源释放（semaphore/spawnCounter）。
	// 由 AgentTaskManager.Spawn 在创建任务时注入。
	manager *AgentTaskManager

	// executor 是工具执行引擎，用于 runMultiRound 内的工具调用。
	// 由 AgentTaskManager.Spawn 注入。
	executor *Executor

	// usage 收集子任务 token 用量（供 N2 cost 归属）。
	usage TokenUsage

	// terminalFailure 是本轮**终局拒绝**（R3/L3.2，2026-09-14），由 runMultiRound
	// 从 AgentContext.TerminalFailure() 带出。它是"子 agent 被终局拒绝"这一事实
	// 从 AgentContext 走向 AgentOutput → ToolResult → Result 的**结构化通道**
	// （R1 契约 C2）；修复前该事实在 AgentContext 内部就蒸发了。
	// 受 mu 保护。
	terminalFailure *ToolExecError
}

// Execute 实现 Agent 接口，执行子任务并返回输出或错误。
//
// 流程（每步对应 8 道校验关卡）：
// 1. ParentCtx nil 检查（v2 P-07：子任务必须有父上下文）
// 2. 递归深度守卫（，关卡 4）
// 3. 状态机转移 pending → running（，关卡 5）
// 4. 创建子 context + 超时控制（关卡 6 defer cancel）
// 5. 从 ParentCtx 提取 AgentID 透传到 StreamWithRetry（02 §2.1.2，关卡 3）
// 6. 调用 runMultiRound（复用 AgentContext.Run 逻辑，关卡 7）
// 7. 状态机转移 running → completed/failed/cancelled
// 8. 错误分类（Canceled → Cancelled / DeadlineExceeded → Failed+ErrAgentTimeout）
func (t *AgentTask) Execute(ctx context.Context, input AgentInput) (AgentOutput, error) {
	// 0. ParentCtx nil 检查（v2 P-07：子任务必须有父上下文，根任务由 Spawn 包装可空）
	if t.depth > 0 && input.ParentCtx == nil {
		return AgentOutput{TaskID: t.id, Status: TaskFailed}, fmt.Errorf(
			"parentCtx is nil for child task (depth=%d)", t.depth)
	}

	// 1. 递归深度守卫（，关卡 4）
	if t.depth > maxAgentDepth {
		return AgentOutput{TaskID: t.id, Status: TaskFailed}, ai.WithID(
			fmt.Errorf("agent recursion depth %d exceeds limit %d", t.depth, maxAgentDepth),
			ai.ErrAgentRecursionLimit, // N6 ErrorID
		)
	}

	// 2. 状态机转移：pending → running（，关卡 5）
	if err := t.transition(TaskPending, TaskRunning); err != nil {
		return AgentOutput{TaskID: t.id, Status: t.getStatus()}, err
	}
	t.mu.Lock()
	t.startedAt = time.Now()
	t.mu.Unlock()

	// 3. 创建子 context（cancel 传播链，关卡 6 defer cancel）
	childCtx, cancel := context.WithCancel(ctx)
	t.mu.Lock()
	t.cancel = cancel
	t.mu.Unlock()

	// 资源清理（关卡 6：defer 释放 cancel / 槽位 / spawn 计数）
	defer func() {
		cancel() // 传播 cancel 到所有后代
		t.releaseSlot()
		t.releaseSpawnCounter()
		t.mu.Lock()
		t.endedAt = time.Now()
		t.mu.Unlock()
	}()

	// 4. 超时控制（N6 ErrAgentTimeout，关卡 2）
	timeoutCtx, timeoutCancel := context.WithTimeout(childCtx, defaultAgentTimeout)
	defer timeoutCancel()

	// 5. 从 ParentCtx 提取 AgentID，透传到 StreamWithRetry（02 §2.1.2，关卡 3）
	// extractAgentID 内部处理 ParentCtx 为 nil 的情况（返回空字符串，不 panic）
	agentID := extractAgentID(input.ParentCtx)
	// 将当前任务自身 ID 作为子任务的 AgentID 注入 streamCtx，构建归属链
	if agentID == "" {
		agentID = t.id // 根任务用自己的 ID 作为 AgentID
	}
	streamCtx := context.WithValue(timeoutCtx, agentIDKey{}, agentID)

	// 6. 调用 runMultiRound（复用 ai.StreamWithRetry，不自实现，关卡 7）
	result, err := t.runMultiRound(streamCtx, input, agentID)

	// 7. 状态机转移：running → completed/failed/cancelled
	if err != nil {
		t.mu.Lock()
		t.err = err
		t.result = result
		t.mu.Unlock()

		if errors.Is(err, context.Canceled) {
			// cancel 传播：转为 Cancelled 终态
			_ = t.transition(TaskRunning, TaskCancelled)
			return t.newAgentOutput(result), err
		}
		if errors.Is(err, context.DeadlineExceeded) {
			// 超时：转为 Failed 终态 + N6 ErrAgentTimeout
			_ = t.transition(TaskRunning, TaskFailed)
			wrapped := ai.WithID(err, ai.ErrAgentTimeout) // N6 ErrorID（关卡 2）
			return t.newAgentOutput(result), wrapped
		}
		// 其他错误：转为 Failed 终态
		_ = t.transition(TaskRunning, TaskFailed)
		return t.newAgentOutput(result), err
	}

	// 成功：转为 Completed 终态
	_ = t.transition(TaskRunning, TaskCompleted)
	t.mu.Lock()
	t.result = result
	t.mu.Unlock()
	return t.newAgentOutput(result), nil
}

// transition 状态机转移（，拒绝非法转移）。
// 受 mu 保护，并发安全。
func (t *AgentTask) transition(from, to TaskStatus) error {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.status != from {
		return fmt.Errorf("invalid transition: %s → %s (current %s)",
			from.String(), to.String(), t.status.String())
	}
	if !isValidTransition(from, to) {
		return fmt.Errorf("forbidden transition: %s → %s", from.String(), to.String())
	}
	t.status = to
	return nil
}

// isValidTransition 状态转移表。
// pending → running/cancelled；running → completed/failed/cancelled；终态不可转移。
func isValidTransition(from, to TaskStatus) bool {
	switch from {
	case TaskPending:
		return to == TaskRunning || to == TaskCancelled
	case TaskRunning:
		return to == TaskCompleted || to == TaskFailed || to == TaskCancelled
	default:
		return false // 终态不可转移
	}
}

// getStatus 返回当前状态（并发安全，受 mu.RLock 保护）。
func (t *AgentTask) getStatus() TaskStatus {
	t.mu.RLock()
	defer t.mu.RUnlock()
	return t.status
}

// runMultiRound 多轮对话循环，复用现有 AgentContext.Run 逻辑（关卡 7：不自实现）。
//
// 设计：构造一个 AgentContext 实例，注入 prompt/角色/工具面/Model，
// 调用其 Run 方法完成多轮对话。AgentContext.Run 内部已使用 ai.StreamWithRetry。
// streamCtx 携带 agentIDKey，AgentContext.Run → StreamWithRetry 自然透传 AgentID。
//
// 若 input.Model 非空，通过 AIAdapter.SetModel 临时注入（参考 B4-3B 改进2，defer 恢复）。
//
// ⚠️ L2移除了一处**共享状态就地改写**：此前这里会
// `Pipeline.Policy().SetAllowedTools(intersect(input.Tools, allowedTools))`，
// 把子 agent 的名单写进**父会话共享的 Policy** 再 defer 恢复。三个问题：
// - 并发不安全：一旦子 agent 并行（SpawnParallel / ExecuteBatch 回退路径）会互扰；
// - 语义错位：AllowedTools 是「免问表」（审批 UX），用它承载「能力收窄」是错层；
// - 对「只读」零效果：读工具本就免问，收窄免问表拦不住写工具（F2/F4 的同源问题）。
//
// 现在能力收窄由 agentCtx.AgentType（角色工具面）∩ agentCtx.ParentToolSet（委派不扩权）
// 承担 —— 判定发生在**子 agent 自己的作用域**内，不碰父 Policy。
func (t *AgentTask) runMultiRound(streamCtx context.Context, input AgentInput, agentID string) (string, error) {
	if t.executor == nil || t.executor.AIClient == nil {
		return "", fmt.Errorf("executor or AIClient not configured for agent task %s", t.id)
	}

	// 临时注入 Model（B4-3B 改进2：defer 恢复）
	if input.Model != "" {
		originalModel := t.executor.AIClient.Model()
		t.executor.AIClient.SetModel(input.Model)
		defer func() {
			t.executor.AIClient.SetModel(originalModel)
		}()
	}

	// 构造 AgentContext（复用旧实现，关卡 7）
	agentCtx := NewAgentContext(t.executor, input.Prompt, "", t.executor.WorkDir(), t.depth)
	agentCtx.AgentType = t.agentType
	agentCtx.MaxDepth = maxAgentDepth
	agentCtx.Timeout = defaultAgentTimeout

	// 工具面收窄（L2）：只由 AgentType（角色工具面）∩ parentToolSet（委派不扩权）决定。
	// AgentInput.Tools 已不参与能力判定（见其字段说明），故此处不再读取。
	agentCtx.ParentToolSet = t.parentToolSet

	// 调用 AgentContext.Run（内部使用 StreamWithRetry，streamCtx 携带 agentIDKey）
	result, err := agentCtx.Run(streamCtx)

	// 收集 token 用量（供 N2 cost 归属）
	// R3/L3.2：同时把"本轮是否被终局拒绝"这一事实带出 AgentContext ——
	// 它必须落到 AgentOutput.FailureKind → ToolResult → Result.FailureKind
	// （R1 契约 C2）；否则上层无法区分"终局拒绝"与"普通失败"。
	t.mu.Lock()
	t.usage.CacheCreationTokens += int64(agentCtx.CacheStats.CacheCreationTokens)
	t.usage.CacheReadTokens += int64(agentCtx.CacheStats.CacheReadTokens)
	t.terminalFailure = agentCtx.TerminalFailure()
	t.mu.Unlock()

	return result, err
}

// releaseSlot 释放执行槽（semaphore.Release）。
// 在 Execute defer 中调用，确保槽位不泄漏（关卡 6）。
// manager 为 nil 时（直接构造的 AgentTask，如测试）跳过释放。
func (t *AgentTask) releaseSlot() {
	if t.manager == nil {
		return
	}
	t.manager.execLimiter.Release(1)
}

// releaseSpawnCounter 释放 spawn 计数（atomic.Add -1）。
// 在 Execute defer 中调用，确保计数不累积（关卡 6）。
// manager 为 nil 时跳过释放。
func (t *AgentTask) releaseSpawnCounter() {
	if t.manager == nil {
		return
	}
	t.manager.spawnCounter.Add(-1)
}

// collectUsage 收集 token 用量（并发安全，受 mu 保护）。
// 返回 Usage 副本，避免外部修改影响内部状态。
func (t *AgentTask) collectUsage() *TokenUsage {
	t.mu.RLock()
	defer t.mu.RUnlock()
	u := t.usage
	return &u
}

// newAgentOutput 汇总子任务的终态输出。
//
// R3/L3.2：把本轮**终局拒绝**（若发生）带到 AgentOutput 上 ——
// 这是"子 agent 被拒"从 AgentContext 走向上层 Result.FailureKind 的唯一通道
// （R1 契约 C2）。IK2：普通失败不设 Kind，故 here 只在 terminalFailure 非空时落字段。
//
// 锁序：先取 terminalFailure 再释放，随后独立获取 status/usage 的锁 ——
// 避免在同一把 RWMutex 上嵌套读锁（Go 的 RWMutex 不可重入）。
func (t *AgentTask) newAgentOutput(result string) AgentOutput {
	t.mu.RLock()
	tf := t.terminalFailure
	t.mu.RUnlock()

	out := AgentOutput{
		Result: result,
		TaskID: t.id,
		Status: t.getStatus(),
		Usage:  t.collectUsage(),
	}
	if tf != nil {
		out.FailureKind = tf.Kind
		out.Confirm = tf.Confirm
	}
	return out
}

// =====================================================================
// AgentTaskManager — 子任务编排器
// =====================================================================

// AgentTaskManager 管理所有 AgentTask 的生命周期（并发模型）。
// 命名为 AgentTaskManager 而非 TaskManager，避免与 task.go 的 TaskManager（bash 后台任务）冲突。
type AgentTaskManager struct {
	tasks         map[string]*AgentTask // 任务表（受 mu 保护）
	mu            sync.RWMutex
	execLimiter   *semaphore.Weighted // 执行容量限流
	spawnCounter  atomic.Int64        // 总 spawn 数
	maxConcurrent int64               // 最大并发执行数（默认 5）
	maxSpawn      int64               // 最大 spawn 总数（默认 20）

	// executor 是工具执行引擎，注入到每个 AgentTask 供 runMultiRound 使用。
	executor *Executor
}

// NewAgentTaskManager 创建 Agent 任务管理器。
// maxConcurrent：最大并发执行数（建议 defaultMaxConcurrent=5）。
// maxSpawn：最大 spawn 总数（建议 defaultMaxSpawn=20）。
// executor：工具执行引擎，可为 nil（测试场景，runMultiRound 会返回错误）。
func NewAgentTaskManager(maxConcurrent, maxSpawn int64, executor *Executor) *AgentTaskManager {
	if maxConcurrent <= 0 {
		maxConcurrent = defaultMaxConcurrent
	}
	if maxSpawn <= 0 {
		maxSpawn = defaultMaxSpawn
	}
	return &AgentTaskManager{
		tasks:         make(map[string]*AgentTask),
		execLimiter:   semaphore.NewWeighted(maxConcurrent),
		maxConcurrent: maxConcurrent,
		maxSpawn:      maxSpawn,
		executor:      executor,
	}
}

// Spawn 创建并启动子任务（递归入口）。
//
// 流程：
// 1. 总数限流（CAS，超限返回 ErrAgentRecursionLimit，关卡 2/4）
// 2. 创建 AgentTask（depth = parentDepth + 1）
// 3. 注册到 tasks map（关卡 5 RLock 保护）
// 4. 执行容量限流（semaphore.Acquire，关卡 5）
// 5. 调用 task.Execute（关卡 6 defer Release）
//
// 注意：spawnCounter 在此 Add(1)，在 task.Execute 的 defer 中 Add(-1)。
// 若 Acquire 失败或 task 创建后未执行，需手动释放 spawnCounter。
func (m *AgentTaskManager) Spawn(ctx context.Context, input AgentInput, parentID string, parentDepth int) (AgentOutput, error) {
	// 0. 角色解析（L2：声明即真相，未知值 fail-closed）
	//
	// 此前这里是 `inferAgentType(input.Prompt)` —— 按 prompt 关键字猜角色，猜不中落 coder
	// （全量工具面，含 bash/write_file）。声明值只被用来算一份「免问」白名单，
	// 于是「声明 explore」在中文 prompt 下等于没声明。这是现场 T-A 的直因（F2）。
	agentType, ok := ParseAgentType(input.AgentType)
	if !ok {
		return AgentOutput{}, invalidAgentTypeError(input.AgentType)
	}

	// 1. 总数限流（CAS，超限返回 ErrAgentRecursionLimit）
	if m.spawnCounter.Add(1) > m.maxSpawn {
		m.spawnCounter.Add(-1)
		return AgentOutput{}, ai.WithID(
			fmt.Errorf("agent spawn count exceeds limit %d", m.maxSpawn),
			ai.ErrAgentRecursionLimit, // N6 ErrorID（总数超限归入递归限制）
		)
	}

	// 2. 创建任务
	task := &AgentTask{
		id:            generateAgentUUID(),
		parentID:      parentID,
		depth:         parentDepth + 1,
		agentType:     agentType,
		parentToolSet: input.ParentToolSet,
		status:        TaskPending,
		createdAt:     time.Now(),
		manager:       m,
		executor:      m.executor,
	}

	// 3. 注册到任务表
	m.mu.Lock()
	m.tasks[task.id] = task
	m.mu.Unlock()

	// 4. 执行容量限流（semaphore.Acquire，关卡 5）
	// Acquire 失败时需手动释放 spawnCounter（task.Execute 未运行，defer 不会触发）
	if err := m.execLimiter.Acquire(ctx, 1); err != nil {
		m.spawnCounter.Add(-1)
		// 从任务表中移除未执行的任务
		m.mu.Lock()
		delete(m.tasks, task.id)
		m.mu.Unlock()
		// Acquire 失败通常是 ctx 取消，返回原 err（携带 ctx.Canceled）
		return AgentOutput{TaskID: task.id, Status: TaskCancelled}, err
	}

	// 5. 执行任务（task.Execute 内部 defer 会释放槽位与 spawn 计数）
	return task.Execute(ctx, input)
}

// SpawnParallel 并发启动多个子任务（errgroup，）。
//
// v2 P-08 修订：为整体 Acquire 等待设置超时兜底（defaultAcquireTimeout），
// 避免所有 slot 被长时间任务占满时新任务无限阻塞。
// 超时后该子任务返回 ctx.Err()，不阻塞 errgroup 其他任务（g.Go 内 return nil）。
//
// 单失败不阻塞其他：g.Go 内 return nil，不返回 err，errgroup 不会 cancel gctx。
func (m *AgentTaskManager) SpawnParallel(ctx context.Context, inputs []AgentInput, parentID string, parentDepth int) ([]AgentOutput, error) {
	g, gctx := errgroup.WithContext(ctx)
	results := make([]AgentOutput, len(inputs))

	for i, input := range inputs {
		i, input := i, input
		g.Go(func() error {
			// 为每个子任务的 Acquire 等待设置超时兜底（防 slot 被占满时无限阻塞）
			acquireCtx, acquireCancel := context.WithTimeout(gctx, defaultAcquireTimeout)
			defer acquireCancel()
			out, err := m.Spawn(acquireCtx, input, parentID, parentDepth)
			if err != nil {
				// 单个失败不阻塞其他（g.Go 内 return nil，不返回 err）
				results[i] = AgentOutput{TaskID: out.TaskID, Status: TaskFailed}
				return nil
			}
			results[i] = out
			return nil
		})
	}

	_ = g.Wait()
	return results, nil
}

// Cancel 取消任务（传播到所有后代）。
// 通过 task.cancel() 触发 context 链，子任务在 Execute 内的 childCtx 自动收到 cancel 信号。
// 任务不存在时返回错误。
func (m *AgentTaskManager) Cancel(taskID string) error {
	m.mu.RLock()
	task, ok := m.tasks[taskID]
	m.mu.RUnlock()
	if !ok {
		return fmt.Errorf("task %s not found", taskID)
	}
	task.mu.RLock()
	cancel := task.cancel
	task.mu.RUnlock()
	if cancel != nil {
		cancel() // context 链自动传播到所有后代
	}
	return nil
}

// List 列出所有任务（供 Task UI）。
// 返回 AgentTaskInfo 切片副本，避免外部修改影响内部状态。
func (m *AgentTaskManager) List() []AgentTaskInfo {
	m.mu.RLock()
	defer m.mu.RUnlock()
	infos := make([]AgentTaskInfo, 0, len(m.tasks))
	for _, t := range m.tasks {
		t.mu.RLock()
		infos = append(infos, AgentTaskInfo{
			ID:        t.id,
			ParentID:  t.parentID,
			Depth:     t.depth,
			Status:    t.status,
			CreatedAt: t.createdAt,
			StartedAt: t.startedAt,
			EndedAt:   t.endedAt,
		})
		t.mu.RUnlock()
	}
	return infos
}

// CleanCompleted 清理终态任务，返回清理数量。
// 终态：completed / failed / cancelled（IsFinal 返回 true）。
func (m *AgentTaskManager) CleanCompleted() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	count := 0
	for id, t := range m.tasks {
		t.mu.RLock()
		final := t.status.IsFinal()
		t.mu.RUnlock()
		if final {
			delete(m.tasks, id)
			count++
		}
	}
	return count
}

// Get 获取单个任务（供 /task show <id> 命令使用）。
// 返回 AgentTaskInfo 副本与可能的错误（任务不存在）。
func (m *AgentTaskManager) Get(taskID string) (AgentTaskInfo, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	t, ok := m.tasks[taskID]
	if !ok {
		return AgentTaskInfo{}, fmt.Errorf("task %s not found", taskID)
	}
	t.mu.RLock()
	defer t.mu.RUnlock()
	return AgentTaskInfo{
		ID:        t.id,
		ParentID:  t.parentID,
		Depth:     t.depth,
		Status:    t.status,
		CreatedAt: t.createdAt,
		StartedAt: t.startedAt,
		EndedAt:   t.endedAt,
	}, nil
}

// Executor 返回管理器持有的 Executor 引用，供外部接线使用。
func (m *AgentTaskManager) Executor() *Executor {
	return m.executor
}
