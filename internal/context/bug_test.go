package context

// 反例测试（Bug Reproduction Tests）
//
// 本文件是对标审查机制的核心组件。每个测试对应 docs/BENCHMARK_FACTS.md 中的一个事实条目。
//
// 测试特点：
// - 测试名以 TestBug_ 开头
// - 在当前（含 bug 的）代码上运行会 **失败**，证明 bug 存在
// - 修复 bug 后测试 **通过**，证明 bug 已修
//
// 使用方式：
// go test ./internal/context/ -run TestBug_ -v
//
// 预期结果：当前代码下所有 TestBug_ 测试应失败（证明 bug 存在）
// 修复后：所有 TestBug_ 测试应通过（证明 bug 已修）

import (
	"reflect"
	"strings"
	"sync"
	"testing"

	"github.com/anchorandfish/whale-code/internal/ai"
	"github.com/anchorandfish/whale-code/internal/session"
)

// hasMutexField 通过反射检查结构体是否包含 sync.Mutex 或 sync.RWMutex 字段
func hasMutexField(t reflect.Type) bool {
	for i := 0; i < t.NumField(); i++ {
		f := t.Field(i)
		if f.Type == reflect.TypeOf(sync.Mutex{}) || f.Type == reflect.TypeOf(sync.RWMutex{}) {
			return true
		}
	}
	return false
}

// TestBug_AutoCompactThreshold 对应 FACT-ERR-004
// auto-compact 阈值应为 contextWindow - 13000，而非固定百分比
// 压缩体系重构：自动压缩触发线由 Budget.ShouldAutoCompact 承担
// （window - 13000）；CheckState 的 StateShouldCompact 改为 0.80 提示线
// （用户决策窗口），两者职责分离。FACT-ERR-004 的"减法优于百分比"语义
// 通过 ShouldAutoCompact 断言。
func TestBug_AutoCompactThreshold(t *testing.T) {
	// FACT-ERR-004
	contextWindow := 200000
	budget := DefaultBudget(contextWindow)

	// 180000 tokens：未到主力线 187000 (200000 - 13000) → 不应触发自动压缩
	tokens := 180000
	if budget.ShouldAutoCompact(tokens, 0) {
		t.Errorf("BUG [FACT-ERR-004]: auto-compact threshold uses percentage, not contextWindow - 13000\n"+
			"  contextWindow: %d\n"+
			"  tokens: %d\n"+
			"  正确阈值: %d (contextWindow - 13000)\n"+
			"  期望: ShouldAutoCompact=false (tokens < 正确阈值)\n"+
			"  实际: true\n"+
			"  证据: 外部参考实现（留档见审查文档）",
			contextWindow, tokens, contextWindow-13000)
	}
	// 187000 tokens：到达主力线 → 应触发自动压缩
	if !budget.ShouldAutoCompact(187000, 0) {
		t.Errorf("BUG [FACT-ERR-004]: tokens=187000 (=window-13000) 应触发自动压缩，实际未触发")
	}
}

// TestBug_CircuitBreakerNoMutex 对应 FACT-ERR-005, FACT-CONC-001
// CircuitBreaker 共享状态修改无锁，并发调用会导致数据竞争，熔断失效
func TestBug_CircuitBreakerNoMutex(t *testing.T) {
	// FACT-ERR-005, FACT-CONC-001
	cb := NewCircuitBreaker(3)
	typ := reflect.TypeOf(cb).Elem()

	if !hasMutexField(typ) {
		t.Errorf("BUG [FACT-ERR-005, FACT-CONC-001]: CircuitBreaker has no mutex field\n" +
			"  期望: CircuitBreaker 有 sync.Mutex 或 sync.RWMutex 字段\n" +
			"  实际: CircuitBreaker 无锁字段\n" +
			"  原因: context.go:314-321 CircuitBreaker 结构体无 mu 字段\n" +
			"        context.go:332-339 RecordFailure 修改 failureCount/isOpen 无锁\n" +
			"        context.go:342-346 RecordSuccess 修改 failureCount/isOpen 无锁\n" +
			"        context.go:349-354 IsOpen 修改 halfOpen 无锁（读操作中修改状态）\n" +
			"        context.go:357-363 AllowProbe 修改 halfOpen 无锁\n" +
			"  风险: 并发调用 RecordFailure/IsOpen 数据竞争，熔断计数错误\n" +
			"  对比: ContextOverflowRecovery (recovery.go:73-78) 有 mu sync.Mutex\n" +
			"  证据: 外部参考实现（留档见审查文档）")
	}
}

// TestBug_AICompactorCircuitBreakerNoMutex 对应 FACT-ERR-005
// AICompactor 的 circuitBreaker 同样无锁
func TestBug_AICompactorCircuitBreakerNoMutex(t *testing.T) {
	// FACT-ERR-005
	typ := reflect.TypeOf(AICompactor{})

	if !hasMutexField(typ) {
		t.Errorf("BUG [FACT-ERR-005]: AICompactor has no mutex, circuitBreaker is not thread-safe\n" +
			"  期望: AICompactor 有 sync.Mutex 字段保护 circuitBreaker\n" +
			"  实际: AICompactor 无锁字段\n" +
			"  原因: ai_compact.go:15-23 AICompactor 结构体无 mu 字段\n" +
			"        ai_compact.go:71 c.circuitBreaker.IsOpen() 无锁调用\n" +
			"        ai_compact.go:83 c.circuitBreaker.RecordFailure() 无锁调用\n" +
			"        ai_compact.go:86 c.circuitBreaker.RecordSuccess() 无锁调用\n" +
			"  风险: 并发 compact 调用时 circuitBreaker 数据竞争，熔断失效\n" +
			"  证据: 外部参考实现（留档见审查文档）")
	}
}

// TestBug_CompactPTLRetryMissing 对应 FACT-ERR-006
// compact 自身 prompt-too-long 时应使用 truncateHeadForPTLRetry，当前完全未实现
func TestBug_CompactPTLRetryMissing(t *testing.T) {
	// FACT-ERR-006
	// 正确做法：compact 请求自身 prompt-too-long 时，调用 truncateHeadForPTLRetry
	// 按 token gap 丢弃最老 API-round group，无法解析 gap 时丢弃 20%，MAX_PTL_RETRIES=3
	// 当前实现：AICompactor.Compact 在 token 超限时只减少 preserve count，不处理 PTL 错误

	typ := reflect.TypeOf(AICompactor{})
	hasPTLRetry := false
	for i := 0; i < typ.NumField(); i++ {
		f := typ.Field(i)
		name := strings.ToLower(f.Name)
		if strings.Contains(name, "ptl") ||
			strings.Contains(name, "prompttoolong") ||
			strings.Contains(name, "truncatehead") ||
			strings.Contains(name, "ptlretry") {
			hasPTLRetry = true
			break
		}
	}

	if !hasPTLRetry {
		t.Errorf("BUG [FACT-ERR-006]: AICompactor has no PTL retry mechanism\n" +
			"  期望: AICompactor 有 PTL 重试字段（如 ptlRetryCount, maxPTLRetries）\n" +
			"  实际: AICompactor 无 PTL 相关字段\n" +
			"  正确做法: compact 请求自身 prompt-too-long 时，调用 truncateHeadForPTLRetry\n" +
			"            按 token gap 丢弃最老 API-round group，MAX_PTL_RETRIES=3\n" +
			"  当前实现: ai_compact.go:98-121 在 token 超限时只减少 preserve count\n" +
			"            不处理 compact 请求自身的 prompt-too-long 错误\n" +
			"  证据: 外部参考实现（留档见审查文档）")
	}
}

// TestBug_TruncationRecoveryNoMutex 对应 FACT-CONC-001
// TruncationRecovery 的 ShouldRetry/Consume/Reset 无锁保护
func TestBug_TruncationRecoveryNoMutex(t *testing.T) {
	// FACT-CONC-001
	r := NewTruncationRecovery()
	typ := reflect.TypeOf(r).Elem()

	if !hasMutexField(typ) {
		t.Errorf("BUG [FACT-CONC-001]: TruncationRecovery has no mutex\n" +
			"  期望: TruncationRecovery 有 sync.Mutex 字段\n" +
			"  实际: TruncationRecovery 无锁字段\n" +
			"  原因: context.go:366-369 TruncationRecovery 结构体无 mu 字段\n" +
			"        context.go:380-382 ShouldRetry 读取 RemainingAttempts 无锁\n" +
			"        context.go:385-387 Consume 修改 RemainingAttempts 无锁\n" +
			"        context.go:389-392 Reset 修改 RemainingAttempts 无锁\n" +
			"  对比: ContextOverflowRecovery (recovery.go:73-78) 有 mu sync.Mutex\n" +
			"  风险: 并发调用 ShouldRetry/Consume 数据竞争，重试计数错误")
	}
}

// TestBug_BlockingSpawnBypassesLock 对应 FACT-CONC-001
// BlockingSpawn 直接访问 m.commitLog.nextID 和 m.commitLog.applied，绕过 CollapseCommitLog 的锁
func TestBug_BlockingSpawnBypassesLock(t *testing.T) {
	// FACT-CONC-001
	// context_collapse.go:215-217 直接访问 m.commitLog.nextID 和 m.commitLog.applied
	// 绕过 CollapseCommitLog.mu 锁，并发修改 applied slice 数据竞争
	//
	// 注：使用指针类型 reflect.TypeOf((*CollapseCommitLog)(nil)) 检查方法集，
	// 因为 CollapseCommitLog 含 sync.RWMutex，值接收者方法会触发 go vet copylocks 错误，
	// 所有方法均为指针接收者。指针类型的方法集包含值+指针接收者方法。

	typ := reflect.TypeOf((*CollapseCommitLog)(nil))
	hasAddMethod := false
	for i := 0; i < typ.NumMethod(); i++ {
		m := typ.Method(i)
		if strings.HasPrefix(m.Name, "Add") || strings.HasPrefix(m.Name, "Append") {
			hasAddMethod = true
			break
		}
	}

	if !hasAddMethod {
		t.Errorf("BUG [FACT-CONC-001]: CollapseCommitLog has no Add/Append method, BlockingSpawn bypasses lock\n" +
			"  期望: CollapseCommitLog 提供 AddApplied 方法，内部持锁修改 applied\n" +
			"  实际: CollapseCommitLog 无 Add/Append 方法\n" +
			"  原因: context_collapse.go:215-217 BlockingSpawn 直接访问：\n" +
			"        commit.ID = m.commitLog.nextID\n" +
			"        m.commitLog.nextID++\n" +
			"        m.commitLog.applied = append(m.commitLog.applied, commit)\n" +
			"  风险: 绕过 CollapseCommitLog.mu 锁，并发修改 applied slice 数据竞争\n" +
			"  正确做法: CollapseCommitLog 提供 AddApplied(commit) 方法，内部持锁")
	}
}

// TestBug_GetCommitLogLeaksReference 对应 FACT-CONC-003
// GetCommitLog 返回内部 commitLog 引用，调用方可绕过 manager 锁修改
func TestBug_GetCommitLogLeaksReference(t *testing.T) {
	// FACT-CONC-003
	cfg := DefaultCollapseConfig()
	mgr := NewContextCollapseManager(cfg, ai.NewTokenEstimator(), DefaultBudget(200000))

	log := mgr.GetCommitLog()
	if log == nil {
		t.Fatal("expected non-nil commit log")
	}

	// 调用方可以直接调用 Commit 修改内部状态，绕过 manager 的锁
	_, err := log.Commit(CollapseCommit{
		StartIndex: 0,
		EndIndex:   1,
		Summary:    "test bypass",
	}, 5)

	if err == nil {
		t.Errorf("BUG [FACT-CONC-003]: GetCommitLog leaks internal reference\n" +
			"  期望: GetCommitLog 不返回可变引用，或返回只读接口\n" +
			"  实际: 调用方可以直接 Commit 修改内部状态（err==nil 表示成功）\n" +
			"  原因: context_collapse.go:305-309 GetCommitLog 返回 m.commitLog 引用\n" +
			"  风险: 调用方在无 manager.mu 锁的情况下修改 commitLog，数据竞争\n" +
			"  正确做法: 返回只读接口或副本")
	}
}

// TestBug_FindCacheBoundaryWrongLogic 对应 FACT-AI-002
// findCacheBoundary 只找 RoleSystem 消息作为 cache 边界，不基于实际 cache_control 标记
func TestBug_FindCacheBoundaryWrongLogic(t *testing.T) {
	// FACT-AI-002
	// 正确做法：cache boundary 应基于实际 cache_control 标记位置或 API 返回的 cache 信息
	// 当前实现：只找 RoleSystem 消息作为边界

	compactor := NewCacheAwareCompactor(ai.NewTokenEstimator())

	// 构造消息：有 user 消息但无 system 消息
	messages := []session.Message{
		{Role: session.RoleUser, Blocks: []session.ContentBlock{{Type: "text", Text: "msg1"}}},
		{Role: session.RoleAssistant, Blocks: []session.ContentBlock{{Type: "text", Text: "msg2"}}},
		{Role: session.RoleUser, Blocks: []session.ContentBlock{{Type: "text", Text: "msg3"}}},
	}

	boundary := compactor.findCacheBoundary(messages)

	// 当前实现：无 system 消息，boundary=0
	// 正确做法：应基于 cache_control 标记或 cache info
	if boundary == 0 {
		t.Errorf("BUG [FACT-AI-002]: findCacheBoundary only looks for RoleSystem messages\n" +
			"  期望: 基于 cache_control 标记位置或 API 返回的 cache 信息\n" +
			"  实际: 只找 RoleSystem 消息，无 system 消息时 boundary=0\n" +
			"  原因: cache_aware.go:90-98 findCacheBoundary 遍历找 msg.Role == session.RoleSystem\n" +
			"  风险: 无 system 消息时无法识别 cache 边界，cache-aware 压缩失效\n" +
			"  证据: 外部参考实现（留档见审查文档）")
	}
}

// TestBug_CacheBreakDetectionSingleCondition 对应 FACT-AI-003
// cache break 检测应使用双条件（>5% 且 >2000 tokens），当前只用单条件（>5%）
func TestBug_CacheBreakDetectionSingleCondition(t *testing.T) {
	// FACT-AI-003
	// 正确做法：cache miss token 下降 >5% 且 超过 MIN_CACHE_MISS_TOKENS=2000
	// 当前实现：只用 drop > 5%，无 MIN_CACHE_MISS_TOKENS 检查

	detector := ai.NewCacheBreakDetector()
	detector.RecordPromptState("test", nil, "model", "cc")

	statePtr := detector.GetLastPromptState()
	if statePtr == nil {
		t.Fatal("expected non-nil prompt state")
	}
	currentState := *statePtr

	// 第一次调用：建立 baseline，prevCacheRead=100
	detector.CheckForCacheBreak(100, currentState)

	// 第二次调用：cacheRead 从 100 降到 90，drop=10% > 5%
	// 但绝对值下降只有 10 tokens，远小于 MIN_CACHE_MISS_TOKENS=2000
	// 正确做法：不应判定为 cache break（绝对值不足）
	// 当前实现：判定为 cache break（只看百分比）
	result := detector.CheckForCacheBreak(90, currentState)

	if result != nil && result.IsBreak {
		t.Errorf("BUG [FACT-AI-003]: cache break detection uses single condition (drop > 5%%)\n" +
			"  期望: 双条件 - drop > 5%% 且 绝对值下降 > MIN_CACHE_MISS_TOKENS=2000\n" +
			"  实际: 单条件 - drop > 5%% 即判定 break\n" +
			"  场景: prevCacheRead=100, currentCacheRead=90, drop=10%% > 5%%, 但绝对值下降=10 < 2000\n" +
			"  原因: cache_break_detection.go:166 只检查 drop > breakThreshold\n" +
			"  风险: 小请求误报 cache break，触发不必要的压缩\n" +
			"  证据: 外部参考实现（留档见审查文档）")
	}
}
