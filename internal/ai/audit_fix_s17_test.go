// Package ai — S17 (AI 推理运行时) Fix 目标测试（HG1=A：解耦重试计数）
//
// 与 audit_char_s17_test.go 的区别：那里钉死"修复前"的难看行为；这里断言"修复后"
// 的目标不变量。从生产真实入口 ai.StreamWithRetry 进入（非手工构造内部前提）。
//
// 硬规则：本文件先写、先红（当前 calls 被压扁），实现 HG1=A 后转绿。
//
// 运行：go test ./internal/ai/ -run 'TestFixS17' -count=1 -v
package ai

import (
	"context"
	"testing"
	"time"
)

// TestFixS17_HG1_Phase1RetriesBoundedByMaxRetries
//
// 目标不变量（HG1=A / 对齐业内既有实现「retries >= max_retries 才停」）：
// 同模型重试次数严格由 RetryConfig.MaxRetries 驱动 —— coordinator 非 nil 时也必须发起
// MaxRetries+1 次尝试，绝不能因 RecoveryCoordinator 的状态推进而中途 break。
//
// 修复前：calls==1（首个 500 把状态推到 Retrying，下一轮 BeforeRecovery("retry") 返 false → break）。
// 修复后：calls==3。
func TestFixS17_HG1_Phase1RetriesBoundedByMaxRetries(t *testing.T) {
	client := &auditCountingClient{err: &APIError{StatusCode: 500, ErrorID: ErrAIApiServer, Message: "internal server error"}}
	cfg := RetryConfig{MaxRetries: 2, BaseDelay: time.Millisecond, MaxDelay: 5 * time.Millisecond, JitterRatio: 0}
	coordinator := NewRecoveryCoordinatorWithCooldown(0)

	_, err := StreamWithRetry(context.Background(), client, Request{Model: "m"}, cfg, nil, coordinator)
	if err == nil {
		t.Fatal("持续 500 应返回错误")
	}
	if got := client.calls.Load(); got != 3 {
		t.Fatalf("Phase1 同模型重试应严格 = MaxRetries+1 = 3 次（与 coordinator 无关）；got=%d（MaxRetries=%d）", got, cfg.MaxRetries)
	}
}

// TestFixS17_HG1_PostExhaustionAdvanceWiring
//
// 目标不变量：Phase1 耗尽后，StreamWithRetry 必须**单次**推进 coordinator 一个阶段
// （Normal→Retrying），把"同模型重试已耗尽"交接给 degrade/compress 升级门；且 Phase1
// 期间每次尝试只做记账（consecutiveErrors 累加）而不推进状态机。
//
// 本测试是"接线守卫"：若摘掉循环后的那次 coordinator.RecordFailure()，状态将停留
// Normal，本测试转红 —— 即"摘掉接线即红，防门变死代码"。
//
// 观测：calls==3（MaxRetries+1）、ConsecutiveErrors==4（3 次尝试记账 + 1 次阶段推进）、
// State==Retrying（恰好一个阶段步进）。修复前这三个值分别为 1 / 1 / Retrying。
//
// 已知边界（G1b，另立 human gate，不在本轮 Fix 范围）：即便状态推进到 Retrying，
// 默认 30s 冷却、以及 Phase2 内层 coordinator.RecordFailure()（嵌在"模型确实切换"的
// if 内）仍会使 Phase3 压缩门返 false。故本测试只钉"升级接线存在"，**不**断言 Phase3
// 实际发起调用。
func TestFixS17_HG1_PostExhaustionAdvanceWiring(t *testing.T) {
	client := &auditCountingClient{err: &APIError{StatusCode: 500, ErrorID: ErrAIApiServer, Message: "internal server error"}}
	cfg := RetryConfig{MaxRetries: 2, BaseDelay: time.Millisecond, MaxDelay: 5 * time.Millisecond, JitterRatio: 0}
	coordinator := NewRecoveryCoordinatorWithCooldown(0)

	_, err := StreamWithRetry(context.Background(), client, Request{Model: "m"}, cfg, nil, coordinator)
	if err == nil {
		t.Fatal("持续 500 应返回错误")
	}
	if got := client.calls.Load(); got != 3 {
		t.Fatalf("Phase1 = MaxRetries+1 = 3 次；got=%d", got)
	}
	if got := coordinator.ConsecutiveErrors(); got != 4 {
		t.Fatalf("应为 3 次同模型尝试记账 + 1 次阶段推进 = 4；got=%d（说明 Phase1 记账或升级接线缺失）", got)
	}
	if coordinator.State() != StateRetrying {
		t.Fatalf("Phase1 耗尽后应恰好推进一个阶段到 Retrying（升级接线存在）；got=%v（Normal 说明接线被摘）", coordinator.State())
	}
}

// TestFixS17_HG1_RetryAttemptDoesNotAdvancePhase 单元级钉子：RecordRetryAttempt
// 只累加 consecutiveErrors，不推进状态机、不武装升级冷却。
func TestFixS17_HG1_RetryAttemptDoesNotAdvancePhase(t *testing.T) {
	rc := NewRecoveryCoordinatorWithCooldown(0)
	for i := 0; i < 5; i++ {
		rc.RecordRetryAttempt()
	}
	if rc.State() != StateNormal {
		t.Fatalf("RecordRetryAttempt 不应推进状态机，got=%v", rc.State())
	}
	if rc.ConsecutiveErrors() != 5 {
		t.Fatalf("RecordRetryAttempt 应累加 consecutiveErrors=5，got=%d", rc.ConsecutiveErrors())
	}
	// 未武装冷却 → 升级门可被咨询（state 仍 Normal，故 degrade 门仍应 false）
	if rc.BeforeRecovery("degrade") {
		t.Fatal("Normal 状态不应允许 degrade")
	}
	if !rc.BeforeRecovery("retry") {
		t.Fatal("Normal 状态应允许 retry（状态未被 retry 记账污染）")
	}
}
