// links.go — H11/N27 Phase B B4 LinkMemories 双向链接。
// ；落地：A.Links 含 B.Name，B.Links 含 A.Name，reason 存内存字段 linkRegistry
// REF-外部-A-MEM@arxiv:2502.12110-双向链接；借鉴逻辑：A-MEM 提出记忆之间通过 link 字段建立双向关联，支持知识图谱式检索；落地：H11/N27 LinkMemories 写 A.Links+=B.Name 且 B.Links+=A.Name
// ；落地：ReflectMemories 调用 LinkMemories 时 warn 不 error（主流程不阻断）
// SNAPSHOT: LinkMemories={A.Links+=B.Name AND B.Links+=A.Name; reason存内存字段linkRegistry(不持久化); 去重(已存在不重复添加); 失败不阻断(ReflectMemories调用时warn不error); 加载走globalMergeLayer.ListMerged按Name查找+按FilePath推断scope写回}

package memory

import (
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"sync"
)

// linkRegistry 全局链接原因 map（内存字段，不持久化，重启归零）。
// key: entryName, value: map[linkTarget]reason
// 对齐 entry.go LinkReasons 概念性字段声明：实际存储在本 registry（避免 MemoryEntry struct 扩展）。
var (
	linkRegistry   = make(map[string]map[string]string)
	linkRegistryMu sync.RWMutex
)

// LinkMemories 在两条记忆之间建立双向链接（A.Links 含 B.Name，B.Links 含 A.Name）。
// 对齐 v0.4 §3.4 + A-MEM @arxiv:2502.12110 双向链接：记忆之间通过 Links 字段互相引用。
// reason 写入内存字段 linkRegistry（不持久化，重启归零）。
//
// 设计：
// - 通过 globalMergeLayer.ListMerged() 找到 A 和 B 的 entry（含 FilePath）
// - 调用 entry.AddLink 去重添加 link
// - 更新 linkRegistry[nameA][nameB] = reason + linkRegistry[nameB][nameA] = reason
// - 根据 entry.FilePath 推断 scope，用对应 store 写回
// - 任一写回失败返回 error（ReflectMemories 调用时 warn 不阻断主流程）
//
// 参数：
// - nameA: 记忆 A 的 Name
// - nameB: 记忆 B 的 Name
// - reason: 链接原因（可选，空字符串也接受，仅存于内存）
func LinkMemories(nameA, nameB, reason string) error {
	if nameA == "" || nameB == "" {
		return errors.New("LinkMemories: nameA and nameB must not be empty")
	}
	if nameA == nameB {
		return errors.New("LinkMemories: nameA and nameB must not be equal (self-link not allowed)")
	}

	ml := globalMergeLayer
	if ml == nil {
		return errors.New("LinkMemories: global MergeLayer not initialized, call SetGlobalMergeLayer first")
	}

	// 通过 ListMerged 找到 A 和 B（含 FilePath 信息，用于推断 scope 写回）
	merged, err := ml.ListMerged()
	if err != nil {
		return fmt.Errorf("LinkMemories: ListMerged: %w", err)
	}

	var entryA, entryB *MemoryEntry
	for _, e := range merged {
		if e.Name == nameA {
			entryA = e
		}
		if e.Name == nameB {
			entryB = e
		}
		if entryA != nil && entryB != nil {
			break
		}
	}

	if entryA == nil {
		return fmt.Errorf("LinkMemories: %w: name=%s", ErrEntryNotFound, nameA)
	}
	if entryB == nil {
		return fmt.Errorf("LinkMemories: %w: name=%s", ErrEntryNotFound, nameB)
	}

	// 去重添加双向 link（entry.AddLink 内部已做去重）
	entryA.AddLink(nameB)
	entryB.AddLink(nameA)

	// 更新 linkRegistry（reason 存内存字段，不持久化）
	linkRegistryMu.Lock()
	defer linkRegistryMu.Unlock()
	if linkRegistry[nameA] == nil {
		linkRegistry[nameA] = make(map[string]string)
	}
	linkRegistry[nameA][nameB] = reason
	if linkRegistry[nameB] == nil {
		linkRegistry[nameB] = make(map[string]string)
	}
	linkRegistry[nameB][nameA] = reason

	// 写回对应 scope 的 store（按 FilePath 推断 scope）
	if err := writeEntryBackToScope(entryA); err != nil {
		return fmt.Errorf("LinkMemories: write entryA %q: %w", nameA, err)
	}
	if err := writeEntryBackToScope(entryB); err != nil {
		return fmt.Errorf("LinkMemories: write entryB %q: %w", nameB, err)
	}

	return nil
}

// GetLinks 返回指定记忆的所有链接目标（读取 entry.Links 字段）。
// 通过 globalMergeLayer.ListMerged 加载，找不到返回 ErrEntryNotFound。
// 返回的切片是 entry.Links 的副本（避免外部修改污染内存）。
func GetLinks(name string) ([]string, error) {
	ml := globalMergeLayer
	if ml == nil {
		return nil, errors.New("GetLinks: global MergeLayer not initialized, call SetGlobalMergeLayer first")
	}

	merged, err := ml.ListMerged()
	if err != nil {
		return nil, fmt.Errorf("GetLinks: ListMerged: %w", err)
	}

	for _, e := range merged {
		if e.Name == name {
			// 返回副本，避免外部修改污染
			out := make([]string, len(e.Links))
			copy(out, e.Links)
			return out, nil
		}
	}
	return nil, fmt.Errorf("GetLinks: %w: name=%s", ErrEntryNotFound, name)
}

// GetLinkReason 返回指定链接的原因（从内存 linkRegistry 读取）。
// 找不到返回空字符串（不报错，reason 是可选的）。
func GetLinkReason(name, target string) string {
	linkRegistryMu.RLock()
	defer linkRegistryMu.RUnlock()
	if reasons, ok := linkRegistry[name]; ok {
		return reasons[target]
	}
	return ""
}

// writeEntryBackToScope 根据 entry.FilePath 推断 scope，用对应 store 写回。
// FilePath 含 "memory/_user" → userStore；否则 → projectStore。
// 调用方需确保 globalMergeLayer 已初始化且对应 store 非 nil。
func writeEntryBackToScope(entry *MemoryEntry) error {
	if entry == nil {
		return errors.New("writeEntryBackToScope: entry is nil")
	}
	ml := globalMergeLayer
	if ml == nil {
		return errors.New("writeEntryBackToScope: global MergeLayer not initialized")
	}

	// 通过 FilePath 推断 scope
	if strings.Contains(entry.FilePath, filepath.Join("memory", "_user")) {
		// user scope
		if ml.userStore == nil {
			return fmt.Errorf("writeEntryBackToScope: userStore is nil (entry %q)", entry.Name)
		}
		return ml.userStore.Write(entry)
	}
	// project scope（默认）
	if ml.projectStore == nil {
		return fmt.Errorf("writeEntryBackToScope: projectStore is nil (entry %q)", entry.Name)
	}
	return ml.projectStore.Write(entry)
}

// resetLinkRegistryForTest 清空 linkRegistry（仅供测试使用，避免测试间状态污染）。
// 测试通过 t.Cleanup 注册清理。
func resetLinkRegistryForTest() {
	linkRegistryMu.Lock()
	defer linkRegistryMu.Unlock()
	linkRegistry = make(map[string]map[string]string)
}
