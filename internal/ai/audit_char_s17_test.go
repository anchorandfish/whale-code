// Package ai — S17 (AI 推理运行时) 审计 characterization 测试
//
// 本文件只钉死 whale-code 当前行为（含难看行为），不改任何源码。
// 所有断言描述"当前"实现，不是"期望"实现。后续 Fix 改动若改变行为，
// 这些测试应随之翻转（先从生产真实入口进的红测）。
//
// 运行：go test ./internal/ai/ -run 'TestAuditS17' -count=1 -v
//
// 【Fix-F1（HG1=A）后注】本文件原先钉死的「coordinator 压制重试」(calls==1) 行为已被
// 本轮 Fix 翻转，对应测试更名为 CoordinatorNoLongerBlocksRetry 并断言 calls==3。
// 其余测试仍钉死未改动的行为。详见 docs/review0909/ai_HANDOFF.md。
package ai

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"
)

// ---------------------------------------------------------------------------
// A. loop_detector —— I4 终止保证：检测是" advisory "而非" enforced "
// ---------------------------------------------------------------------------

// TestAuditS17_LoopDetector_WarningAtThreshold 钉死：成功调用连续 threshold(=3) 次
// 相同工具+输入 → 第 3 次返回警告。
//
// ★ 本函数已于 2026-09-30 **有意翻转**（I-8.2 循环告警去噪）——**下一个人别当回归**：
// 翻转前钉的是"第 4 次仍应告警"（原实现 `>= threshold` ⇒ 3/4/5/6… 每多一次再刷一条）。
// 真机实测：同一条 bash 连发 4 次刷出 2 条几乎相同的告警，复读只占窗口不多给信息。
// 现行为 = 同一串连续重复**只在首次跨阈报一次**（`== threshold`）。
// 检测器仍无 halt/abort 字段（advisory）—— 强制停机归 agentguard P0-4 的重复调用判据。
func TestAuditS17_LoopDetector_WarningAtThreshold(t *testing.T) {
	d := NewLoopDetector(0, 0, 0) // 0 → 默认 3/5/5
	const input = `{"prompt":"read README first line"}`
	if w := d.CheckLoop("Agent", input, true); w != "" {
		t.Fatalf("第1次不应告警，got=%q", w)
	}
	if w := d.CheckLoop("Agent", input, true); w != "" {
		t.Fatalf("第2次不应告警，got=%q", w)
	}
	if w := d.CheckLoop("Agent", input, true); w == "" {
		t.Fatalf("第3次应告警（达到默认阈值3）")
	}
	// 第 4 次：**不再复读**（★ 已翻转，见函数头说明）。检测器仍无 "halt/abort"
	// 状态字段可供调用方读取 —— 这一点未变（强制面归 agentguard P0-4）。
	if w := d.CheckLoop("Agent", input, true); w != "" {
		t.Fatalf("第4次不应复读（首次跨阈已报过），got=%q", w)
	}
}

// TestAuditS17_LoopDetector_FailThresholdSeparate 钉死：失败重试与成功循环
// 计数互不干扰；默认 failThreshold=5，需连续 5 次失败才告警。
func TestAuditS17_LoopDetector_FailThresholdSeparate(t *testing.T) {
	d := NewLoopDetector(0, 0, 0)
	const input = `{"prompt":"x"}`
	for i := 0; i < 4; i++ {
		if w := d.CheckLoop("Bash", input, false); w != "" {
			t.Fatalf("第%d次失败不应告警（failThreshold=5）", i+1)
		}
	}
	if w := d.CheckLoop("Bash", input, false); w == "" {
		t.Fatalf("第5次失败应告警")
	}
	// 一次成功调用不应重置失败计数但也不混入成功计数：成功循环在自己的阈值(3)上告警。
	// ★ 已翻转（2026-09-30 同批）：原断言要求第 5 次仍告警（复读语义）；现为"首次跨阈
	// 报一次"，故改为在第 3 次（阈值）观察告警，并同时钉住"第 4 次不复读"。
	d2 := NewLoopDetector(0, 0, 0)
	if w := d2.CheckLoop("Bash", input, true); w != "" {
		t.Fatalf("成功循环第1次不应告警，got=%q", w)
	}
	if w := d2.CheckLoop("Bash", input, true); w != "" {
		t.Fatalf("成功循环第2次不应告警，got=%q", w)
	}
	if w := d2.CheckLoop("Bash", input, true); w == "" {
		t.Fatalf("成功循环应在第3次（阈值）告警")
	}
	if w := d2.CheckLoop("Bash", input, true); w != "" {
		t.Fatalf("第4次不应复读，got=%q", w)
	}
}

// ---------------------------------------------------------------------------
// B. recovery_coordinator —— I4 状态机：单向、fail 后 BeforeRecovery("retry") 失效
// ---------------------------------------------------------------------------

// TestAuditS17_RecoveryCoordinator_StateMachine 钉死：Normal→Retrying→Degrading→
// Compressing→Failed 单向；进入 Retrying 后 BeforeRecovery("retry") 返回 false
// （仅 "degrade" 被允许），这是与 StreamWithRetry 不兼容的根因。
func TestAuditS17_RecoveryCoordinator_StateMachine(t *testing.T) {
	rc := NewRecoveryCoordinatorWithCooldown(0) // cooldown=0 避免冷却干扰
	if !rc.BeforeRecovery("retry") {
		t.Fatal("Normal 状态应允许 retry")
	}
	rc.RecordFailure() // Normal → Retrying
	if rc.State() != StateRetrying {
		t.Fatalf("应为 Retrying，got %v", rc.State())
	}
	if rc.BeforeRecovery("retry") {
		t.Fatal("Retrying 状态不应再允许 retry（仅 degrade 允许）—— 这是与 StreamWithRetry 的关键不兼容点")
	}
	if !rc.BeforeRecovery("degrade") {
		t.Fatal("Retrying 应允许 degrade")
	}
	rc.RecordFailure() // Retrying → Degrading
	if rc.State() != StateDegrading {
		t.Fatalf("应为 Degrading，got %v", rc.State())
	}
	if !rc.BeforeRecovery("compress") {
		t.Fatal("Degrading 应允许 compress")
	}
	rc.RecordFailure() // Degrading → Compressing
	if rc.State() != StateCompressing {
		t.Fatalf("应为 Compressing，got %v", rc.State())
	}
	if rc.BeforeRecovery("retry") || rc.BeforeRecovery("degrade") || rc.BeforeRecovery("compress") {
		t.Fatal("Compressing 不应允许任何恢复")
	}
	rc.RecordFailure() // Compressing → Failed
	if rc.State() != StateFailed {
		t.Fatalf("应为 Failed，got %v", rc.State())
	}
	if rc.BeforeRecovery("retry") {
		t.Fatal("Failed 不应允许 retry")
	}
	rc.RecordSuccess() // 重置回 Normal
	if rc.State() != StateNormal {
		t.Fatalf("RecordSuccess 后应回 Normal，got %v", rc.State())
	}
}

// ---------------------------------------------------------------------------
// C. StreamWithRetry + RecoveryCoordinator —— I4/I11 关键发现：coordinator 阻断重试
// ---------------------------------------------------------------------------

type auditCountingClient struct {
	calls atomic.Int32
	err   error
}

func (c *auditCountingClient) Stream(ctx context.Context, req Request) (<-chan Event, error) {
	c.calls.Add(1)
	return nil, c.err
}
func (c *auditCountingClient) Model() string     { return "test-model" }
func (c *auditCountingClient) SetModel(m string) {}
func (c *auditCountingClient) Close() error      { return nil }

// TestAuditS17_StreamWithRetry_CoordinatorNoLongerBlocksRetry —— 【Fix-F1（HG1=A）已翻转】
//
// 原行为（本轮 Fix 前的钉死基线）：coordinator 非 nil 时，一次 500 就把状态推进到
// Retrying，使下一轮 BeforeRecovery("retry") 返 false → 重试循环 break；MaxRetries=2
// 本应允许 3 次尝试，实际只调 1 次。
// Fix-F1（HG1=A）后：同模型重试预算改由 RetryConfig.MaxRetries 独立驱动（Phase1 不再
// 咨询 coordinator），coordinator 非 nil 也发起 MaxRetries+1 次。
// 本测试由「calls==1」翻转为「calls==3」，作为该修复的回归守卫。
func TestAuditS17_StreamWithRetry_CoordinatorNoLongerBlocksRetry(t *testing.T) {
	client := &auditCountingClient{err: &APIError{StatusCode: 500, ErrorID: ErrAIApiServer, Message: "internal server error"}}
	cfg := RetryConfig{MaxRetries: 2, BaseDelay: time.Millisecond, MaxDelay: 10 * time.Millisecond, JitterRatio: 0}
	coordinator := NewRecoveryCoordinator()

	_, err := StreamWithRetry(context.Background(), client, Request{Model: "m"}, cfg, nil, coordinator)
	if err == nil {
		t.Fatal("应返回错误")
	}
	if got := client.calls.Load(); got != 3 {
		t.Fatalf("Fix-F1 后：coordinator 非 nil 也应发起 MaxRetries+1=3 次 Stream 调用，got=%d", got)
	}
	if coordinator.State() != StateRetrying {
		t.Fatalf("Phase1 耗尽后 coordinator 应推进到 Retrying，got %v", coordinator.State())
	}
}

// TestAuditS17_StreamWithRetry_NoCoordinatorRetriesFully 钉死对照：coordinator 为 nil
// 时，MaxRetries=2 确实发起 3 次 Stream 调用。证明"重试被压扁"是 coordinator 引入的。
func TestAuditS17_StreamWithRetry_NoCoordinatorRetriesFully(t *testing.T) {
	client := &auditCountingClient{err: &APIError{StatusCode: 500, ErrorID: ErrAIApiServer, Message: "internal server error"}}
	cfg := RetryConfig{MaxRetries: 2, BaseDelay: time.Millisecond, MaxDelay: 10 * time.Millisecond, JitterRatio: 0}

	_, _ = StreamWithRetry(context.Background(), client, Request{Model: "m"}, cfg, nil, nil)
	if got := client.calls.Load(); got != 3 {
		t.Fatalf("coordinator 为 nil 时应发起 3 次 Stream 调用，got=%d", got)
	}
}

// ---------------------------------------------------------------------------
// D. reconnect —— I4/I11：ShouldReconnect 分类 + 退避有界
// ---------------------------------------------------------------------------

func TestAuditS17_Reconnect_ShouldReconnect(t *testing.T) {
	rw := NewReconnectWrapper(nil, ReconnectConfig{}, NewResumeTracker())
	cases := []struct {
		name string
		err  error
		want bool
	}{
		{"400", &APIError{StatusCode: 400}, false},
		{"401", &APIError{StatusCode: 401}, false},
		{"403", &APIError{StatusCode: 403}, false},
		{"404", &APIError{StatusCode: 404}, false},
		{"413", &APIError{StatusCode: 413}, false},
		{"408", &APIError{StatusCode: 408}, true},
		{"500", &APIError{StatusCode: 500}, true},
		{"502", &APIError{StatusCode: 502}, true},
		{"503", &APIError{StatusCode: 503}, true},
		{"504", &APIError{StatusCode: 504}, true},
		{"429", &APIError{StatusCode: 429}, true},
		{"529", &APIError{StatusCode: 529}, true},
		{"ctx-cancel", context.Canceled, false},
		{"network", errors.New("connection refused"), true},
		{"unknown", errors.New("weird thing"), false},
	}
	for _, c := range cases {
		if got := rw.ShouldReconnect(c.err); got != c.want {
			t.Errorf("ShouldReconnect(%s)=%v want %v", c.name, got, c.want)
		}
	}
}

func TestAuditS17_Reconnect_BackoffBounded(t *testing.T) {
	rw := NewReconnectWrapper(nil, DefaultReconnectConfig(), NewResumeTracker())
	// 超大 attempt 退避必须封顶 MaxDelay(30s)，绝不溢出/无限
	if d := rw.computeBackoff(1000); d > rw.cfg.MaxDelay {
		t.Fatalf("退避超过 MaxDelay：%v", d)
	}
	if d := rw.computeBackoff(1); d != rw.cfg.BaseDelay {
		t.Fatalf("attempt=1 应等于 BaseDelay(1s)，got %v", d)
	}
}

// ---------------------------------------------------------------------------
// E. backoff —— I11：指数退避封顶 + 确定性（jitter=0）
// ---------------------------------------------------------------------------

func TestAuditS17_Backoff_CapAndDeterminism(t *testing.T) {
	b := NewExponentialBackoff(time.Second, 30*time.Second, 2.0, 0, nil)
	if d := b.NextDelay(1); d != time.Second {
		t.Fatalf("attempt=1 应为 1s，got %v", d)
	}
	if d := b.NextDelay(0); d != time.Second {
		t.Fatalf("attempt<=0 应为 base 1s，got %v", d)
	}
	// 超大 attempt 必须封顶 MaxDelay，且无溢出
	if d := b.NextDelay(100); d != 30*time.Second {
		t.Fatalf("超大 attempt 应封顶 30s，got %v", d)
	}
	// jitter=0 → 确定性：两次相同调用结果一致
	if b.NextDelay(3) != b.NextDelay(3) {
		t.Fatal("jitter=0 应确定性")
	}
}

// ---------------------------------------------------------------------------
// F. refusal_handler —— I10 姿态：安全策略拒绝被排除在 force 流外；force 仅用户确认
// ---------------------------------------------------------------------------

func TestAuditS17_Refusal_AuxiliarySignalIsNotAJudgement(t *testing.T) {
	h := NewRefusalHandler(true)
	// ★ （2026-09-19 · 用户放行方案 A）**有意改写**本用例：
	// 原名 `TestAuditS17_Refusal_TwoDetectorsIndependent`，钉的是"DetectRefusal 只匹配拒绝措辞、
	// 不匹配泛化安全策略文本"。那个检测器（及其关键词表）**已整体删除** ——
	// 真机 T-1 证明它必然误报（模型如实说「我没有这个工具…所以我无法调用」= 票面期望行为，
	// 却命中「我无法」被判成 AI 拒绝）。⇒ 本用例现在只钉"留下的那一半"的性质。
	//
	// MentionsSecurityConstraint 是**辅助**信号（读散文、不是事实），**不能**单独构成判据：
	if !h.MentionsSecurityConstraint("sandbox policy blocked this operation: permission denied") {
		t.Fatal("应标记为'文本在谈安全约束' → 作为合取的第二项参与判定")
	}
	// 它的**天性**（也是它永远不能单独当判据的原因）：任何"提到"约束的**正常**说明都会命中。
	if !h.MentionsSecurityConstraint("当前沙箱是 workspace-write，所以 curl 会失败") {
		t.Fatal("辅助信号命中的是措辞、不是事实；正常解释约束的答复同样会命中")
	}
	// 调用方（tui/stream.go checkAISafety）在 R4 后的契约：
	// ⛔ = 本轮有**结构化**拒绝事件（Model.roundDeniedKind 非空，来源 tools.Result.FailureKind）
	// **∧** MentionsSecurityConstraint(text) 命中；
	// 仅散文命中而本轮无结构化事件 → 走普通拒绝分支并**保留** AI-only 强制确认路径。
	// 两检测器仍然独立，但辅助信号不再能单独决定分支（R4 有意翻转了旧契约）。
	//
	// ★ （2026-09-19 · 方案 A）**再次翻转**了上面第 3 句：
	// "普通拒绝分支"与"AI-only 强制确认路径**自动**出现"这两件事**都已删除** ——
	// 散文命中而本轮无结构化事件时，现在**什么都不输出**（静默）。
	// 理由：真机 T-1 证明关键词判据把"如实说没有这个工具"判成了拒绝，
	// 而 T-1 的期望输出必然含「我无法」⇒ 该判据与票面期望自相矛盾；
	// 且自动弹出的 force 提示在此场景机制上无效，会把模型推向编造结果。
	// ⇒ 现在的契约是：**散文不得单独产生任何断言**；"AI 拒绝"这个事实在本协议下
	// 没有结构化源（FinishReason 只表达 content_filter），故按"证据不足时应静默"不自动断言。
	// ★ 强制覆盖**机制本身保留**，但只由用户**主动**输入「我确认强制执行」触发（不再自动弹），
	// 可发现性走 /help（internal/command）。
	// 结构门见 `.go`。
	// 下面这两条断言（MentionsSecurityConstraint 命中/不命中）**未受影响**，仍然有效。
}

func TestAuditS17_Refusal_ForceModeRequiresExplicitConfirm(t *testing.T) {
	h := NewRefusalHandler(true)
	if msg := h.GetForceExecuteSystemMessage(); msg != "" {
		t.Fatal("未进入 force 模式时不应注入 system message")
	}
	// 模型可能输出 "我确认强制执行" 这类文本——但只有用户输入走 IsForceExecuteConfirm
	if h.IsForceExecuteConfirm("模型说：我确认强制执行") {
		t.Fatal("子串不应匹配：必须是精确确认短语（防模型输出触发 force 模式）")
	}
	if !h.IsForceExecuteConfirm("我确认强制执行") {
		t.Fatal("精确短语应匹配")
	}
	h.SetForceExecuteMode(true)
	if msg := h.GetForceExecuteSystemMessage(); msg == "" {
		t.Fatal("进入 force 模式后应返回注入提示")
	}
}

// ---------------------------------------------------------------------------
// G. cache_reference —— I5/I11：LRU 容量有界，内存不随会话无限增长
// ---------------------------------------------------------------------------

func TestAuditS17_CacheReference_LRUCapacity(t *testing.T) {
	r := NewCacheReferenceRegistryWithCapacity(3)
	for i := 0; i < 5; i++ {
		r.Register(string(rune('A'+i)), i, 0)
	}
	if r.Size() != 3 {
		t.Fatalf("容量应封顶 3，got %d", r.Size())
	}
	// 最早注册的 'A'/0 应被淘汰（真 LRU）
	if r.Lookup("A") != nil {
		t.Fatal("最久未用的 A 应已被淘汰")
	}
	if r.Lookup("E") == nil {
		t.Fatal("最近使用的 E 应仍在")
	}
}
