// capability.go — H11/N27 Phase A A2 capability 读取接口（GetCapability + GetCapabilitiesBatch + GetCapabilityWithMeta）。
// 只读接口；落地：GetCapability/GetCapabilitiesBatch/GetCapabilityWithMeta 三接口，H10 只读消费
// abstention；落地：verified 返回 entry / unverified 返回 entry+warn / contradicted 返回 ErrCapabilityNotFound
// Q4a-H10 可选增强；落地：GetCapabilityWithMeta 是 H10 可选调用接口（不强制 H10 升级）
// SNAPSHOT: GetCapability(key)=(CapabilityValue,error); GetCapabilitiesBatch(ctx,keys)=(map[string]CapabilityValue,error); GetCapabilityWithMeta(key)=(*MemoryEntry,error); abstention={verified:返回entry, unverified:返回entry+warn, contradicted:ErrCapabilityNotFound}; AccessCount+++LastAccessedAt更新(内存不持久化)

package memory

import (
	"context"
	"errors"
	"fmt"
	"log"
	"sync"
	"time"
)

// ErrCapabilityNotFound capability 未找到或被 abstain（verification=contradicted）。
// 对齐 02 §2.7.2 Q4a abstention 三态：contradicted 视同未找到。
// H10 调用方捕获此 error 后应保守降级（不使用该能力）。
var ErrCapabilityNotFound = errors.New("capability not found or contradicted")

// ErrCapabilityMustBeProjectScope capability 记忆必须写 project scope（Phase B PIT-001 防护）。
// Phase A 仅声明 error，Phase B scope.go 落地后实际抛出。
var ErrCapabilityMustBeProjectScope = errors.New("capability must be in project scope")

// capabilityStore 全局 capability 记忆存储（Phase A stub 实现，Phase B 由 scope.go 替换）。
// Phase A 使用内存 map 支持测试与最小集成；Phase B scope.go 落地后替换为按 scopeDir 加载实际 .md 文件。
// 并发安全：sync.RWMutex 保护 map 读写（02 §2.7.3 并发控制）。
var capabilityStore = struct {
	mu      sync.RWMutex
	entries map[string]*MemoryEntry // key → entry（key 即 MemoryEntry.Name）
}{
	entries: make(map[string]*MemoryEntry),
}

// RegisterCapabilityForTest 测试用注入 capability 记忆（Phase A stub，Phase B 由 scope.go 替换）。
// 生产环境由 Phase B scope.go 落地后注入实际存储。
// 注意：调用方应通过此接口注入 *副本* entry，避免外部修改影响内部状态。
func RegisterCapabilityForTest(key string, entry *MemoryEntry) {
	capabilityStore.mu.Lock()
	defer capabilityStore.mu.Unlock()
	capabilityStore.entries[key] = entry
}

// ResetCapabilityRegistryForTest 测试间重置注册表（避免测试间状态污染）。
func ResetCapabilityRegistryForTest() {
	capabilityStore.mu.Lock()
	defer capabilityStore.mu.Unlock()
	capabilityStore.entries = make(map[string]*MemoryEntry)
}

// loadEntryByKey 按 key 加载 capability 记忆（TD-11 修复：接入文件存储）。
//
// 加载策略（与 loadEntryByName 一致，hybrid cache）：
// 1. 先查内存 map（缓存），命中则返回缓存指针（保证 AccessCount++ 累积）
// 2. 未命中且 defaultMemoryStore 已注入：经 store.Read 从文件加载
// （内部即 os.ReadFile + parseMemoryEntry），缓存后返回
// 3. 未注入 store（测试场景）：返回 ErrCapabilityNotFound
//
// key 即 MemoryEntry.Name（capability 类型记忆以 Name 为唯一查找键）。
// 返回的是内存 map 中的指针，GetCapabilityWithMeta 通过内存字段自增是预期行为。
func loadEntryByKey(key string) (*MemoryEntry, error) {
	// 先查内存 map（缓存命中时直接返回，保证 AccessCount 等内存字段累积）
	capabilityStore.mu.RLock()
	if entry, ok := capabilityStore.entries[key]; ok {
		capabilityStore.mu.RUnlock()
		return entry, nil
	}
	capabilityStore.mu.RUnlock()

	// 未注入文件 store：回退到原 stub 行为（返回 not found）
	if defaultMemoryStore == nil {
		return nil, fmt.Errorf("%w: key=%s", ErrCapabilityNotFound, key)
	}

	// TD-11 修复：从文件加载（store.Read 内部调用 os.ReadFile + parseMemoryEntry）
	entry, err := defaultMemoryStore.Read(key)
	if err != nil {
		return nil, fmt.Errorf("%w: key=%s (file load: %v)", ErrCapabilityNotFound, key, err)
	}

	// 缓存到内存 map，后续访问返回同一指针（AccessCount++ 累积）
	capabilityStore.mu.Lock()
	capabilityStore.entries[key] = entry
	capabilityStore.mu.Unlock()

	return entry, nil
}

// GetCapability 返回 capability 记忆的值（v0.3 接口，H10 主调用）。
// 对齐 02 §2.7.2 Q5：H10 只读调用，独立方向未实施时降级。
//
// abstention 行为规约（与 GetCapabilityWithMeta 一致，）：
// - verification=verified → 返回 CapabilityValue（Trust=TrustCapability）
// - verification=unverified → 返回 CapabilityValue（Trust=TrustCapability+":unverified"，让 H10 调用方决定 abstention）
// - verification=contradicted → 返回 ErrCapabilityNotFound（abstention）
//
// 注：本接口在 Phase A 仅提供 stub 实现（loadEntryByKey 走内存 map），
// 完整实现在 Phase B scope.go 落地后接入实际文件加载。
func GetCapability(key string) (CapabilityValue, error) {
	entry, err := loadEntryByKey(key)
	if err != nil {
		return CapabilityValue{}, err
	}

	// S18/G3（选项 B：加载时注入扫描）：capability 正文会被注入子代理 system prompt，
	// 若被篡改或由 LLM 反思污染可能携带注入/自提权指令。注入前先扫描并隔离可疑内容。
	safeContent, scan := SanitizeMemoryForInjection(entry.Content)
	if scan.Suspicious {
		log.Printf("WARN: capability %q content flagged by S18/G3 injection guard: %v", key, scan.Reasons)
	}

	// D4（来源标签 + 不参与安全决策）：注入 system prompt 的内容必须自述来源
	//（user / probe_fn / ai_self / reflection）与信任级别，并显式声明它是参考数据、
	// 不构成任何权限授予。隔离路径同样带标签（否则看不出隔离件来自记忆注入面）。
	injectable := FormatForInjection(safeContent, ProvenanceOf(entry, defaultStoreScope()))

	// 根据 verification 三态决定返回值
	switch entry.DefaultVerification() {
	case VerificationContradicted:
		// abstention：contradicted 视同未找到
		return CapabilityValue{}, fmt.Errorf("%w: key=%s is contradicted", ErrCapabilityNotFound, key)
	case VerificationUnverified:
		// 返回值但带 :unverified 后缀，让调用方决定保守降级
		return CapabilityValue{
			Value:     injectable,
			CheckedAt: entry.LastAccessedAt,
			Trust:     TrustCapability + ":unverified",
		}, nil
	default:
		// verified：正常返回
		return CapabilityValue{
			Value:     injectable,
			CheckedAt: entry.LastAccessedAt,
			Trust:     TrustCapability,
		}, nil
	}
}

// GetCapabilitiesBatch 批量查询 capability（Q5 仲裁通过，性能优化）。
// 单次调用返回多个 key 的 CapabilityValue，避免 H10 循环调用 GetCapability。
//
// 部分失败语义（Q5）：
// - 某个 key 未找到或 contradicted：跳过该 key（不写入返回 map），不阻塞其余
// - 返回 error 仅当所有 key 都失败（全部 abstain 或未找到）
// - 单个失败可通过返回 map 长度 < len(keys) 检测
func GetCapabilitiesBatch(ctx context.Context, keys []string) (map[string]CapabilityValue, error) {
	if len(keys) == 0 {
		return nil, nil
	}

	result := make(map[string]CapabilityValue, len(keys))
	var failedCount int

	for _, key := range keys {
		// 检查 ctx 是否取消（批量查询可能耗时长）
		if err := ctx.Err(); err != nil {
			return result, fmt.Errorf("batch capability query canceled: %w", err)
		}

		cv, err := GetCapability(key)
		if err != nil {
			// 单个 key 失败：跳过，不阻塞其余（Q5 部分失败语义）
			failedCount++
			continue
		}
		result[key] = cv
	}

	// 全部失败：返回 error（含失败计数）
	if failedCount == len(keys) {
		return result, fmt.Errorf("all %d capabilities failed (not found or contradicted)", failedCount)
	}

	return result, nil
}

// GetCapabilityWithMeta 返回完整元数据（含 verification 三态），H10 可选调用以获取 abstention 能力。
// 对齐 02 §2.7.2 Q4a：H10 可选增强接口。
//
// abstention 行为规约：
// - verification=verified → 信任，返回完整 entry（AccessCount 自增，更新 LastAccessedAt）
// - verification=unverified → 返回 entry + warn 日志（AccessCount 自增，调用方决定是否保守降级）
// - verification=contradicted → 返回 ErrCapabilityNotFound
//
// 内存字段更新（不持久化）：
// - AccessCount++：每次调用自增
// - LastAccessedAt = time.Now()：刷新访问时间（Ebbinghaus 衰减用）
//
// 注：本接口在 Phase A 仅提供 stub 实现（loadEntryByKey 走内存 map），
// 完整实现在 Phase B scope.go 落地后接入实际文件加载。
func GetCapabilityWithMeta(key string) (*MemoryEntry, error) {
	entry, err := loadEntryByKey(key)
	if err != nil {
		return nil, err
	}

	// 根据 verification 三态决定返回值
	switch entry.DefaultVerification() {
	case VerificationContradicted:
		// abstention：contradicted 视同未找到（防 H10 使用已失效能力）
		return nil, fmt.Errorf("%w: key=%s is contradicted", ErrCapabilityNotFound, key)
	case VerificationUnverified:
		// 返回 entry 但写 warn 日志（让 H10 调用方决定保守降级）
		log.Printf("WARN: capability %q is unverified, caller should consider conservative degradation", key)
		// 更新内存字段（不持久化）
		entry.AccessCount++
		entry.LastAccessedAt = time.Now()
		return entry, nil
	default:
		// verified：正常返回，更新内存字段
		entry.AccessCount++
		entry.LastAccessedAt = time.Now()
		return entry, nil
	}
}
