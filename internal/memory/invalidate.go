// invalidate.go — H11/N27 Phase B B5 InvalidateMemory 标记记忆失效。
// ；落地：verification→contradicted + valid_until=now，复用 Phase A TransitionVerification 公共 API
// REF-外部-MemorySurvey@arxiv:2512.13564-contradicted 终态；借鉴逻辑：Memory Survey 提出 verification 三态（verified/unverified/contradicted），contradicted 是终态；落地：H11/N27 InvalidateMemory 标记 contradicted
// ；落地：/memory invalidate <name> <reason> 通过 command_list.go 注册（B6 实现）
// SNAPSHOT: InvalidateMemory={verification→contradicted(终态) + valid_until=now; 复用TransitionVerification公共API; 桥接=registerEntryForTransition临时注入capabilityStore+defer清理; 命令=/memory invalidate <name> <reason>}

package memory

import (
	"errors"
	"fmt"
)

// InvalidateMemory 标记记忆失效：verification→contradicted + valid_until=now。
// 对齐 v0.4 §7.5 + Memory Survey @arxiv:2512.13564：contradicted 是终态，需用户手动删除重建。
// 调用 TransitionVerification(name, "contradicted", reason) 复用 Phase A 公共 API。
//
// Phase B 桥接说明：
// - Phase A 的 TransitionVerification 用 capabilityStore（内存 map）加载 entry + saveEntry stub 不持久化
// - Phase B 的 entry 实际存在文件中
// - 本函数委托给 transitionVerificationOnFile 完成桥接（加载→注入→TransitionVerification→写回）
//
// 参数：
// - name: 记忆条目 Name
// - reason: 失效原因（用于日志，不影响逻辑）
//
// 返回 error 类型：
// - ErrEntryNotFound：name 不存在
// - ErrInvalidTransition：已 contradicted（终态不可流转）
func InvalidateMemory(name string, reason string) error {
	if name == "" {
		return errors.New("InvalidateMemory: name must not be empty")
	}
	if globalMergeLayer == nil {
		return errors.New("InvalidateMemory: global MergeLayer not initialized, call SetGlobalMergeLayer first")
	}
	return transitionVerificationOnFile(name, VerificationContradicted, reason)
}

// transitionVerificationOnFile 从文件加载 entry + 桥接 capabilityStore + 调 TransitionVerification + 写回文件。
// 供 InvalidateMemory（contradicted）和 handleConfirm（verified）复用，避免重复桥接代码。
//
// Phase B 桥接流程：
// 1. 从 globalMergeLayer 加载文件 entry（含 FilePath 信息）
// 2. 临时注册到 capabilityStore（让 Phase A TransitionVerification 公共 API 能找到）
// 3. 调用 TransitionVerification 公共 API（校验合法流转 + 修改 entry 字段）
// 4. defer 清理 capabilityStore（避免全局污染）
// 5. 写回文件（按 FilePath 推断 scope）
//
// 合法流转（isValidTransition）：
// - unverified→verified（ProbeFn 成功或用户 /memory confirm）
// - unverified→contradicted（ProbeFn 连续 3 次失败或用户 /memory invalidate）
// - verified→contradicted（用户 /memory invalidate）
// - contradicted→anything：终态，不可流转（返回 ErrInvalidTransition）
//
// 参数：
// - name: 记忆条目 Name
// - toState: 目标 verification 状态（verified/contradicted）
// - reason: 流转原因（用于日志，不影响逻辑）
func transitionVerificationOnFile(name, toState, reason string) error {
	ml := globalMergeLayer
	if ml == nil {
		return errors.New("transitionVerificationOnFile: global MergeLayer not initialized")
	}

	// 1. 从文件加载 entry
	merged, err := ml.ListMerged()
	if err != nil {
		return fmt.Errorf("transitionVerificationOnFile: ListMerged: %w", err)
	}
	var entry *MemoryEntry
	for _, e := range merged {
		if e.Name == name {
			entry = e
			break
		}
	}
	if entry == nil {
		return fmt.Errorf("transitionVerificationOnFile: %w: name=%s", ErrEntryNotFound, name)
	}

	// 2. 临时注册到 capabilityStore（让 Phase A TransitionVerification 公共 API 能找到）
	registerEntryForTransition(name, entry)
	defer unregisterEntryForTransition(name)

	// 3. 调用 Phase A 公共 API（校验合法流转 + 修改 entry 字段 + saveEntry stub）
	if err := TransitionVerification(name, toState, reason); err != nil {
		return fmt.Errorf("transitionVerificationOnFile: %w", err)
	}

	// 4. 写回文件（按 FilePath 推断 scope）
	if err := writeEntryBackToScope(entry); err != nil {
		return fmt.Errorf("transitionVerificationOnFile: write back %q: %w", name, err)
	}

	return nil
}

// registerEntryForTransition 临时把文件加载的 entry 注入 capabilityStore，
// 供 Phase A TransitionVerification 公共 API 使用（Phase A 用 capabilityStore 内存 map）。
// 调用方应在调 TransitionVerification 后调 unregisterEntryForTransition 清理。
//
// Phase B 桥接层设计：
// - Phase A 公共 API TransitionVerification 用 capabilityStore 加载 entry + saveEntry stub 不持久化
// - Phase B 的 entry 实际存在文件中，需通过 globalMergeLayer 加载
// - 本函数把文件 entry 临时注入 capabilityStore，让 TransitionVerification 能找到并修改
// - 修改后的 entry 由调用方写回文件（writeEntryBackToScope）
//
// 注意：同包内访问 capabilityStore（非导出），不暴露给外部。
func registerEntryForTransition(name string, entry *MemoryEntry) {
	capabilityStore.mu.Lock()
	defer capabilityStore.mu.Unlock()
	capabilityStore.entries[name] = entry
}

// unregisterEntryForTransition 清理临时注册的 entry（避免 capabilityStore 全局污染）。
// 调用方应通过 defer 调用，确保即使 TransitionVerification 失败也能清理。
func unregisterEntryForTransition(name string) {
	capabilityStore.mu.Lock()
	defer capabilityStore.mu.Unlock()
	delete(capabilityStore.entries, name)
}
