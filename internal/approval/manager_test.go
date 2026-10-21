package approval

import (
	"context"
	"testing"

	"github.com/anchorandfish/whale-code/internal/permission"
)

// approvalCacheKey 复刻生产侧（manager.go）的缓存键推导：hashInput(permission.ApprovalKeyFor(...))。
// 测试不再手算 hashInput(input)，否则与生产的键推导漂移会产生假阴性/假阳性。
func approvalCacheKey(toolName, input string) string {
	return hashInput(permission.ApprovalKeyFor(toolName, input))
}

// TestManager_ClearCache_HitBeforeClear 验证 ClearCache 调用前缓存命中（Get 返回 true）。
func TestManager_ClearCache_HitBeforeClear(t *testing.T) {
	askFn := func(ctx context.Context, toolName, input string) (ApprovalDecision, string) {
		return DecisionAllow, "user allowed"
	}
	m := NewManager(PolicyOnRequest, askFn)

	// 触发一次审批写入缓存（bash 为危险工具，OnRequest 策略下会询问）
	decision, _, err := m.Check(context.Background(), "bash", "ls -la")
	if err != nil {
		t.Fatalf("Check 返回错误: %v", err)
	}
	if decision != DecisionAllow {
		t.Fatalf("期望 DecisionAllow，得到 %d", decision)
	}

	// 验证缓存命中：相同输入应命中缓存
	inputHash := approvalCacheKey("bash", "ls -la")
	cached, ok := m.cache.Get("bash", inputHash)
	if !ok {
		t.Fatal("ClearCache 调用前缓存应命中")
	}
	if cached != DecisionAllow {
		t.Errorf("缓存决策 = %d，期望 %d", cached, DecisionAllow)
	}
}

// TestManager_ClearCache_MissAfterClear 验证 ClearCache 调用后缓存失效（Get 返回 false）。
func TestManager_ClearCache_MissAfterClear(t *testing.T) {
	askFn := func(ctx context.Context, toolName, input string) (ApprovalDecision, string) {
		return DecisionAllow, "user allowed"
	}
	m := NewManager(PolicyOnRequest, askFn)

	// 写入缓存
	_, _, _ = m.Check(context.Background(), "bash", "ls -la")
	inputHash := approvalCacheKey("bash", "ls -la")
	if _, ok := m.cache.Get("bash", inputHash); !ok {
		t.Fatal("前置条件：缓存应命中")
	}

	// 清空缓存
	m.ClearCache()

	// 验证缓存已失效
	if _, ok := m.cache.Get("bash", inputHash); ok {
		t.Error("ClearCache 调用后缓存应失效（Get 返回 false）")
	}
}

// TestManager_ClearCache_NilManager 验证 nil Manager 调用 ClearCache 不 panic。
func TestManager_ClearCache_NilManager(t *testing.T) {
	var m *Manager
	// 不应 panic
	m.ClearCache()
}

// TestManager_ClearCache_NilCacheField 验证 cache 字段为 nil 时不 panic（防御性）。
func TestManager_ClearCache_NilCacheField(t *testing.T) {
	m := &Manager{cache: nil}
	// 不应 panic
	m.ClearCache()
}

// TestManager_ClearCache_MultipleEntries 验证清空多条缓存后全部失效。
func TestManager_ClearCache_MultipleEntries(t *testing.T) {
	askFn := func(ctx context.Context, toolName, input string) (ApprovalDecision, string) {
		return DecisionAllow, "allowed"
	}
	m := NewManager(PolicyOnRequest, askFn)

	// 写入多条缓存
	_, _, _ = m.Check(context.Background(), "bash", "cmd1")
	_, _, _ = m.Check(context.Background(), "bash", "cmd2")
	_, _, _ = m.Check(context.Background(), "write_file", "content")

	// 验证缓存命中
	if _, ok := m.cache.Get("bash", approvalCacheKey("bash", "cmd1")); !ok {
		t.Fatal("cmd1 缓存应命中")
	}
	if _, ok := m.cache.Get("bash", approvalCacheKey("bash", "cmd2")); !ok {
		t.Fatal("cmd2 缓存应命中")
	}

	// 清空
	m.ClearCache()

	// 验证全部失效
	if _, ok := m.cache.Get("bash", approvalCacheKey("bash", "cmd1")); ok {
		t.Error("cmd1 缓存应已失效")
	}
	if _, ok := m.cache.Get("bash", approvalCacheKey("bash", "cmd2")); ok {
		t.Error("cmd2 缓存应已失效")
	}
	if _, ok := m.cache.Get("write_file", approvalCacheKey("write_file", "content")); ok {
		t.Error("write_file 缓存应已失效")
	}
}

// TestManager_Policy 验证 Policy 方法返回正确的策略。
func TestManager_Policy(t *testing.T) {
	tests := []struct {
		name   string
		policy ApprovalPolicy
	}{
		{"on-failure", PolicyOnFailure},
		{"on-request", PolicyOnRequest},
		{"never", PolicyNever},
		{"untrusted", PolicyUntrusted},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			m := NewManager(tt.policy, nil)
			if got := m.Policy(); got != tt.policy {
				t.Errorf("Policy() = %d，期望 %d", got, tt.policy)
			}
		})
	}
}

// TestManager_Policy_NilManager 验证 nil Manager 的 Policy 返回 PolicyNever。
func TestManager_Policy_NilManager(t *testing.T) {
	var m *Manager
	if got := m.Policy(); got != PolicyNever {
		t.Errorf("nil Manager Policy() = %d，期望 %d (PolicyNever)", got, PolicyNever)
	}
}
