package approval

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"time"

	"github.com/anchorandfish/whale-code/internal/permission"
)

// AskFn 用户交互回调：在需要询问时由 Manager 调用，
// 返回审批决策与原因说明（原因可用于审计/日志）。
type AskFn func(ctx context.Context, toolName, input string) (ApprovalDecision, string)

// Manager 审批管理器：组合审批策略、缓存与用户交互回调，
// 提供 Check/CheckWithResult 入口供 Pipeline 调用。
type Manager struct {
	policy ApprovalPolicy
	cache  *Cache
	askFn  AskFn
}

// NewManager 创建审批管理器。默认缓存容量 128 条、TTL 5 分钟。
// askFn 可为 nil（此时需要询问的策略将 fail-closed 返回 DecisionDeny）。
func NewManager(policy ApprovalPolicy, askFn AskFn) *Manager {
	return &Manager{
		policy: policy,
		cache:  NewCache(128, 5*time.Minute),
		askFn:  askFn,
	}
}

// Check 在工具执行前检查审批。
//
// 流程：
// 1. 计算 inputHash=sha256(input)，查缓存，命中且未过期直接返回 DecisionAllow。
// 2. 按 policy 分支决策（Never/OnFailure/OnRequest/Untrusted）。
// 3. 若 askFn 返回 DecisionAllow，写入缓存实现 "AllowAlways" 免再问。
//
// 已知限制（P2，exploit not reachable）：缓存命中时绕过 policy 重新评估。
// 安全前提：policy 不在会话内动态变更（ClearCache 在会话切换时调用）。
// 若未来支持运行时 policy 切换，需在 Check 入口处先检查 policy 是否变更，
// 变更则 ClearCache 后重新评估。
//
// 返回值: (decision, reason, error)
func (m *Manager) Check(ctx context.Context, toolName, input string) (ApprovalDecision, string, error) {
	inputHash := hashInput(permission.ApprovalKeyFor(toolName, input))

	// 1. 缓存命中直接放行
	if decision, ok := m.cache.Get(toolName, inputHash); ok {
		return decision, fmt.Sprintf("cached approval (policy=%s)", m.policy), nil
	}

	// 2. 按策略决策
	switch m.policy {
	case PolicyNever:
		// 从不审批：仅靠沙箱限制
		return DecisionAllow, "policy=never, sandbox-only", nil

	case PolicyOnFailure:
		// 失败时审批：先放行执行，失败后由调用方再次调用 CheckWithResult
		return DecisionAllow, "policy=on-failure, deferred", nil

	case PolicyOnRequest:
		// 按需审批：仅危险工具询问
		if isDangerous(toolName, input) {
			decision, reason := m.ask(ctx, toolName, input)
			if decision == DecisionAllow {
				m.cache.Set(toolName, inputHash, DecisionAllow)
			}
			return decision, reason, nil
		}
		return DecisionAllow, "policy=on-request, safe tool", nil

	case PolicyUntrusted:
		// 高频审批：总是询问
		decision, reason := m.ask(ctx, toolName, input)
		if decision == DecisionAllow {
			m.cache.Set(toolName, inputHash, DecisionAllow)
		}
		return decision, reason, nil
	}

	return DecisionDeny, fmt.Sprintf("unknown approval policy: %d", m.policy), nil
}

// CheckWithResult 在工具执行后（通常 OnFailure 策略下）根据执行结果决定后续动作。
//
// - execErr == nil：执行成功，直接放行（DecisionAllow）。
// - execErr != nil：执行失败，调用 askFn 询问用户是否继续/重试；
// 若 askFn 返回 DecisionAllow 则写入缓存。
// - 非 OnFailure 策略：退化为 Check 语义。
func (m *Manager) CheckWithResult(ctx context.Context, toolName, input string, execErr error) (ApprovalDecision, string, error) {
	// 非 OnFailure 策略退化为 Check
	if m.policy != PolicyOnFailure {
		return m.Check(ctx, toolName, input)
	}

	inputHash := hashInput(permission.ApprovalKeyFor(toolName, input))

	if execErr == nil {
		return DecisionAllow, "policy=on-failure, execution succeeded", nil
	}

	// 执行失败：询问用户
	decision, reason := m.ask(ctx, toolName, input)
	if decision == DecisionAllow {
		m.cache.Set(toolName, inputHash, DecisionAllow)
	}
	return decision, reason, nil
}

// ClearCache 清空审批缓存（会话切换时调用，避免新会话复用旧会话的审批决策）。
func (m *Manager) ClearCache() {
	if m == nil || m.cache == nil {
		return
	}
	m.cache.Clear()
}

// Policy 返回当前审批策略，供调用方判断是否需要接入 on-failure 路径。
// nil Manager 返回 PolicyNever（不触发任何审批）。
func (m *Manager) Policy() ApprovalPolicy {
	if m == nil {
		return PolicyNever
	}
	return m.policy
}

// ask 调用用户交互回调。askFn 为 nil 时 fail-closed 返回 DecisionDeny。
func (m *Manager) ask(ctx context.Context, toolName, input string) (ApprovalDecision, string) {
	if m.askFn == nil {
		return DecisionDeny, "no ask callback configured (fail-closed)"
	}
	decision, reason := m.askFn(ctx, toolName, input)
	if reason == "" {
		reason = fmt.Sprintf("askFn returned %d", decision)
	}
	return decision, reason
}

// isDangerous 判断工具是否属于危险操作（需在 OnRequest 策略下询问用户）。
//
// B4-P1-3 修复：从 permission.IsDangerousTool 派生，替代硬编码的工具列表。
// 原实现硬编码 bash/powershell/write_file/edit_file + git_* 前缀，与权限档位脱钩。
//
// L1：删掉 `git_` 前缀兜底 —— 它是「工具档位表」的第 4 份手工副本，
// 且方向是**过严**（把 git_status/git_log/git_diff 这类只读操作也判为危险 → 多余弹窗）。
// 现在档位只有一个来源（工具注册表的 Spec.RequiredPermission）：
// - git_add/git_commit/git_branch → 声明 WW ⇒ 危险；
// - git_status/git_log/git_diff → 声明 RO ⇒ 不危险；
// - 未登记的工具（如插件传入的 git_clone）由 permission.RequiredLevelOf fail-closed
// 到最高档 ⇒ 仍判为危险（兜底行为与原先的前缀匹配一致）。
//
// 阶段 3：改用 IsDangerousToolFor（输入感知）——委派型工具 Agent 按入参 agent_type 判定，
// 只读子代理（plan/explore）不算危险工具；与 permission 的定级共用 requiredForTool，
// 避免「权限层说只读、审批层说危险」的两层认知不一致。
func isDangerous(toolName, input string) bool {
	return permission.IsDangerousToolFor(toolName, input)
}

// hashInput 使用 sha256 计算输入的十六进制摘要，作为缓存 inputHash。
func hashInput(input string) string {
	h := sha256.Sum256([]byte(input))
	return hex.EncodeToString(h[:])
}
