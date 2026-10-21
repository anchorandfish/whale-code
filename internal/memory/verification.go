// verification.go — H11/N27 Phase A A3 verification 三态流转（TransitionVerification + isValidTransition）。
// 三态流转；落地：unverified→{verified,contradicted} / verified→{contradicted} / contradicted→{}(终态)
// ；落地：ProbeFn成功→verified / ProbeFn连续3次失败→contradicted / 用户confirm→verified / 用户invalidate→contradicted
// →unverified 内部通道；落地：公共 API 拒绝，仅 ApplyForgetting 内部直接设字段绕过 isValidTransition
// SNAPSHOT: verification_transitions={unverified→{verified,contradicted}, verified→{contradicted}, contradicted→{}(终态)}; triggers={ProbeFn成功→verified, ProbeFn连续3次失败→contradicted, 用户/memory confirm→verified, 用户/memory invalidate→contradicted}; verified→unverified=ApplyForgetting内部通道(公共API拒绝)

package memory

import (
	"errors"
	"fmt"
	"time"
)

// ErrEntryNotFound 指定 name 的记忆未找到。
var ErrEntryNotFound = errors.New("memory entry not found")

// ErrInvalidTransition verification 流转不合法（如 contradicted 终态流转，或公共 API 试图 verified→unverified）。
var ErrInvalidTransition = errors.New("invalid verification transition")

// ErrCapabilityMustBeProjectScope 在 capability.go 定义（Phase A 声明，Phase B scope.go Write 抛出）。
// 为避免重复声明，此处不重新定义；使用时直接引用 capability.go 中的同名变量。

// validTransitions verification 流转合法性矩阵。
// key: from state, value: 合法的目标状态列表。
// 注意：verified→unverified 不在此矩阵中（仅 ApplyForgetting 内部通道触发，公共 API 拒绝）。
var validTransitions = map[string][]string{
	VerificationUnverified:   {VerificationVerified, VerificationContradicted},
	VerificationVerified:     {VerificationContradicted},
	VerificationContradicted: {}, // 终态，不可流转（需用户手动删除重建）
}

// isValidTransition 校验流转合法性（公共 API 入口，TransitionVerification 调用）。
// 对齐 v0.4 §2.3：
// - unverified→{verified, contradicted}：合法（公共 API 触发）
// - verified→{contradicted}：合法（公共 API 触发）
// - verified→unverified：仅 ApplyForgetting 内部触发（公共 API 拒绝）
// - contradicted→anything：终态，不可流转（需用户手动删除重建）
//
// ApplyForgetting 内部直接设 entry.Verification="unverified" 绕过本函数（独立内部通道）。
func isValidTransition(from, to string) bool {
	validTargets, ok := validTransitions[from]
	if !ok {
		// from 不在矩阵中（未知状态），拒绝流转
		return false
	}
	for _, t := range validTargets {
		if t == to {
			return true
		}
	}
	return false
}

// defaultMemoryStore 是 memory 包默认的文件存储实例（TD-11 修复）。
// 由 SetDefaultMemoryStore 注入（生产环境由 main.go 接线层调用）。
// 非 nil 时 loadEntryByName/loadEntryByKey/saveEntry 走文件存储（os.ReadFile + parseMemoryEntry），
// nil 时回退到 capabilityStore 内存 map（测试场景，保持向后兼容）。
var defaultMemoryStore *MemoryStore

// SetDefaultMemoryStore 注入默认文件存储实例（TD-11 修复）。
//
// 注入后 loadEntryByName/loadEntryByKey 将优先从 .md 文件加载（经 os.ReadFile + parseMemoryEntry），
// saveEntry 将通过 MemoryStore.Write 持久化到文件。
// 传入 nil 可清除注入，回退到内存 map 模式（测试用）。
//
// 安全：本函数非并发安全，应在初始化阶段（main.go 启动早期）调用，禁止运行时热切换。
func SetDefaultMemoryStore(store *MemoryStore) {
	defaultMemoryStore = store
}

// loadEntryByName 按 name 加载记忆（TD-11 修复：接入文件存储）。
//
// 加载策略（hybrid cache）：
// 1. 若 defaultMemoryStore 已注入：先查内存 map（缓存），未命中则经 store.Read 从文件加载
// （内部即 os.ReadFile + parseMemoryEntry），加载后缓存到内存 map 并返回缓存指针
// 2. 若 defaultMemoryStore 未注入（测试场景）：直接查内存 map
//
// 返回内存 map 中的指针（非文件副本），确保 GetCapabilityWithMeta 的 AccessCount++ 能累积。
// 文件加载失败（文件不存在/解析错误）时返回 ErrEntryNotFound。
func loadEntryByName(name string) (*MemoryEntry, error) {
	// 先查内存 map（缓存命中时直接返回，保证 AccessCount 等内存字段累积）
	capabilityStore.mu.RLock()
	if entry, ok := capabilityStore.entries[name]; ok {
		capabilityStore.mu.RUnlock()
		return entry, nil
	}
	capabilityStore.mu.RUnlock()

	// 未注入文件 store：回退到原 stub 行为（返回 not found）
	if defaultMemoryStore == nil {
		return nil, fmt.Errorf("%w: name=%s", ErrEntryNotFound, name)
	}

	// TD-11 修复：从文件加载（store.Read 内部调用 os.ReadFile + parseMemoryEntry）
	entry, err := defaultMemoryStore.Read(name)
	if err != nil {
		return nil, fmt.Errorf("%w: name=%s (file load: %v)", ErrEntryNotFound, name, err)
	}

	// 缓存到内存 map，后续访问返回同一指针（AccessCount++ 累积）
	capabilityStore.mu.Lock()
	capabilityStore.entries[name] = entry
	capabilityStore.mu.Unlock()

	return entry, nil
}

// saveEntry 保存记忆（TD-11 修复：接入 MemoryStore.Write 文件持久化）。
//
// 持久化策略：
// 1. 若 defaultMemoryStore 已注入：调用 store.Write 序列化为 YAML frontmatter + Markdown body，
// 原子写入 .md 文件（跨进程锁 + 0600 权限），同时更新内存 map 缓存
// 2. 若 defaultMemoryStore 未注入（测试场景）：仅更新内存 map（entry 指针已被调用方修改，无需额外操作）
//
// 注意：内存字段（AccessCount/LastAccessedAt/ProbeFailCount）不持久化到文件（yaml:"-"），
// 但保留在内存 map 中供后续访问累积。
func saveEntry(entry *MemoryEntry) error {
	if entry == nil {
		return errors.New("saveEntry: entry is nil")
	}

	// 更新内存 map 缓存（无论是否走文件，都需保证内存 map 持有最新指针）
	capabilityStore.mu.Lock()
	capabilityStore.entries[entry.Name] = entry
	capabilityStore.mu.Unlock()

	// 未注入文件 store：仅内存 map（测试场景），entry 指针已被调用方修改，直接返回
	if defaultMemoryStore == nil {
		return nil
	}

	// TD-11 修复：通过 MemoryStore.Write 持久化到文件
	if err := defaultMemoryStore.Write(entry); err != nil {
		return fmt.Errorf("saveEntry: file write failed for %s: %w", entry.Name, err)
	}

	return nil
}

// TransitionVerification 流转 verification 状态（公共 API 入口）。
// 对齐 v0.4 §7.5：
// - unverified→verified（ProbeFn 成功或用户 /memory confirm）
// - unverified→contradicted（ProbeFn 连续 3 次失败或用户 /memory invalidate）
// - verified→contradicted（用户 /memory invalidate）
// - verified→unverified 仅 ApplyForgetting 内部触发（ephemeral 衰减），公共 API 拒绝
// - contradicted 是终态，不可流转（需用户手动删除重建）
//
// 副作用：
// - toState=contradicted 时，设置 ValidUntil=now（标记失效时间）
// - toState=verified 且 ValidFrom==nil 时，设置 ValidFrom=now（首次验证成功记录起始时间）
// - UpdatedAt=now（更新修改时间）
//
// 参数：
// - name: 记忆条目 Name
// - toState: 目标 verification 状态（verified/unverified/contradicted）
// - reason: 流转原因（用于日志，不影响逻辑）
func TransitionVerification(name string, toState string, reason string) error {
	entry, err := loadEntryByName(name)
	if err != nil {
		return err
	}

	fromState := entry.DefaultVerification()

	// 校验合法流转（公共 API 入口）
	if !isValidTransition(fromState, toState) {
		return fmt.Errorf("%w: %s→%s (reason: %s)", ErrInvalidTransition, fromState, toState, reason)
	}

	// 应用流转
	entry.Verification = toState
	entry.UpdatedAt = time.Now()

	// toState=contradicted 时，设置 ValidUntil=now（标记失效时间）
	if toState == VerificationContradicted {
		now := time.Now()
		entry.ValidUntil = &now
	}

	// toState=verified 且 ValidFrom==nil 时，设置 ValidFrom=now（首次验证成功记录起始时间）
	// 注意：已存在的 ValidFrom 不被覆盖（防重复流转时丢失原始起始时间）
	if toState == VerificationVerified && entry.ValidFrom == nil {
		now := time.Now()
		entry.ValidFrom = &now
	}

	// 持久化（Phase A stub：内存 map 持有指针，修改已生效）
	if err := saveEntry(entry); err != nil {
		return fmt.Errorf("saveEntry after transition: %w", err)
	}

	return nil
}
