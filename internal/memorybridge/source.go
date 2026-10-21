// Package memorybridge — 把 internal/memory 的读侧能力适配成 config.MemorySource。
//
// ## 为什么需要这个中间包（依赖倒置的落点）
//
// config 包不能 import memory 包：memory → command → config 已构成链，
// 再加 config → memory 就成环（Go 编译拒绝，详见 config/memory_source.go 的说明）。
// 而读路径需要 memory 的两样东西：
// 1. 双 scope 合并视图（_user/ 跨项目偏好 + {hash}/ 项目事实，project 覆盖 user）
// 2. 有效性判定（作废/过期记忆不得注入）—— 唯一权威是 memory 包的 LiveAt
//
// 本包是唯一同时 import config 与 memory 的地方，因此由它承担适配：
// 把 memory 的数据形状翻译成 config 的读路径契约（MemorySource）。
//
// ## 行为变化（相对接线前）
//
// - 可以看到 _user/ scope 的记忆（接线前只能看到 project scope）
// - contradicted / 已过期记忆不再进入候选（接线前会进入，见 P1 判别器 I3）
// - 同名时 project 覆盖 user（沿用 memory 合并层语义）
//
// 排序不变：仍按 UpdatedAt 降序（与原 config.MemoryStore.List 一致）。
package memorybridge

import (
	"context"
	"fmt"
	"github.com/anchorandfish/whale-code/internal/whalehome"
	"sort"
	"strings"
	"time"

	"github.com/anchorandfish/whale-code/internal/config"
	"github.com/anchorandfish/whale-code/internal/memory"
)

// Source 实现 config.MemorySource，数据来自 memory 包的双 scope 合并层。
type Source struct {
	dir     string              // MEMORY.md 索引目录（= project scope 目录）
	user    *memory.MemoryStore // _user/ scope（跨项目用户偏好）
	project *memory.MemoryStore // {workDirHash}/ scope（项目事实）
	merge   *memory.MergeLayer  // 合并视图（project 覆盖 user）
}

// NewSource 构造读路径数据源。
// homeDir 为用户主目录，workDir 为当前工作目录绝对路径。
// baseDir 约定为 {homeDir}/.whale（与 memory 包 scope 约定一致）。
func NewSource(homeDir, workDir string) (*Source, error) {
	baseDir := whalehome.Dir()

	userStore, err := memory.NewMemoryStoreForScope(baseDir, "user", false)
	if err != nil {
		return nil, fmt.Errorf("memorybridge: init user scope: %w", err)
	}
	projectStore, err := memory.NewMemoryStoreForProject(baseDir, workDir)
	if err != nil {
		return nil, fmt.Errorf("memorybridge: init project scope: %w", err)
	}

	return &Source{
		dir:     projectStore.Dir(),
		user:    userStore,
		project: projectStore,
		merge:   memory.NewMergeLayer(userStore, projectStore),
	}, nil
}

// Dir 返回 project scope 目录（MEMORY.md 所在），与接线前的 config store 目录相同。
func (s *Source) Dir() string { return s.dir }

// List 返回读路径可见的记忆，三步：
// 1. 合并 user + project（project 覆盖 user，整文件覆盖）
// 2. 有效性过滤（调用 memory.LiveAt —— 不在本包复制判定条件）
// 3. 按 UpdatedAt 降序（对齐原 config.MemoryStore.List 的顺序契约）
func (s *Source) List() ([]config.MemoryEntryV2, error) {
	merged, err := s.merge.ListMerged()
	if err != nil {
		return nil, fmt.Errorf("memorybridge: list merged: %w", err)
	}

	now := time.Now()
	out := make([]config.MemoryEntryV2, 0, len(merged))
	for _, e := range merged {
		// 有效性判定的唯一权威在 memory 包（liveness.go）；此处只调用，不复制条件。
		if !e.LiveAt(now) {
			continue
		}
		out = append(out, toConfigEntry(e))
	}

	// ListMerged 返回的是 map 遍历结果（顺序不确定）。读路径依赖 UpdatedAt 降序
	// —— 的本地兜底 = 取前 N 条「最近更新」，顺序错则兜底语义静默改变。
	sort.Slice(out, func(i, j int) bool {
		return out[i].UpdatedAt.After(out[j].UpdatedAt)
	})
	return out, nil
}

// Read 按 name 读取单条记忆（name 可带或不带 .md 后缀）。
// 遵守合并层的覆盖语义：project 优先，user 兜底。
func (s *Source) Read(name string) (*config.MemoryEntryV2, error) {
	if e, err := s.project.Read(name); err == nil {
		return toConfigEntryPtr(e), nil
	}
	if e, err := s.user.Read(name); err == nil {
		return toConfigEntryPtr(e), nil
	}
	return nil, fmt.Errorf("memorybridge: read %q: not found in project or user scope", name)
}

// Rank 实现 config.MemoryRanker：按查询给出相关度降序的记忆 name（最多 topK 条）。
// 这是 P3-wire把 `internal/memory` 的本地打分器接进注入路径的**接点**。
//
// ★ 用 SearchMemoryWithLayer 而不是 SearchMemory：
// 后者读的是 memory 包的**包级全局** merge 层（靠装配时序注入，`SetGlobalMergeLayer`）。
// 本适配器自己持有两份 store，`List` 走的是 `s.merge` —— 若这里改读全局层，
// 同一条读路径就有两个数据来源，两者指向不同 workDir 时会**静默读到不同记忆**。
// 显式传 s.merge 保证 List 与 Rank 同源（本仓 G5–G7 / 同类教训）。
//
// 语义边界（如实记录）：底层是**词法**检索（trigram 子串 + 词项 OR），不是语义检索。
// 实测（internal/memory/p3_recall_bench_test.go，42 条语料）：
// - 字面重合的整句/关键词/短词：Recall@5 = 1.000
// - 换个说法的改写查询：Recall@5 = 0.000 ← 已知上限，需向量检索才可能跨过
//
// 因此调用方必须把"零命中"当正常情况处理（config 侧会退回按最近更新）。
func (s *Source) Rank(ctx context.Context, query string, topK int) ([]string, error) {
	if topK <= 0 || strings.TrimSpace(query) == "" {
		return nil, nil
	}
	entries, err := memory.SearchMemoryWithLayer(ctx, s.merge, query, memory.SearchOptions{TopK: topK})
	if err != nil {
		return nil, fmt.Errorf("memorybridge: rank: %w", err)
	}
	out := make([]string, 0, len(entries))
	for _, e := range entries {
		if e == nil {
			continue
		}
		out = append(out, e.Name)
	}
	return out, nil
}

// Search 实现 P4 模型主动检索（tools.memory_search 的数据面）：按查询返回**完整条目**
// （Name/Description/Type/FilePath/UpdatedAt/Content 摘录），供工具结果渲染。
//
// ★ 与 Rank 同走 `s.merge`（显式层）—— 这是 P3-1b 定下的"同一条读路径一个数据源"纪律：
// 注入路径（List/Rank）与新工具（Search）读同一份合并视图，不重蹈"两个数据源静默读不同
// workDir"的 覆辙。零命中返回空切片（不是 error），与 SearchMemory 契约一致。
func (s *Source) Search(ctx context.Context, query string, topK int) ([]*memory.MemoryEntry, error) {
	if topK <= 0 || strings.TrimSpace(query) == "" {
		return nil, nil
	}
	entries, err := memory.SearchMemoryWithLayer(ctx, s.merge, query, memory.SearchOptions{TopK: topK})
	if err != nil {
		return nil, fmt.Errorf("memorybridge: search: %w", err)
	}
	return entries, nil
}

// toConfigEntry 把 memory.MemoryEntry 搬成 config.MemoryEntryV2。
// 两类型字段已对齐（14 frontmatter + 内存字段），此处只做搬运，不做语义解释。
func toConfigEntry(e *memory.MemoryEntry) config.MemoryEntryV2 {
	if e == nil {
		return config.MemoryEntryV2{}
	}
	return config.MemoryEntryV2{
		Name:           e.Name,
		Description:    e.Description,
		Type:           config.MemoryType(e.Type),
		CreatedAt:      e.CreatedAt,
		UpdatedAt:      e.UpdatedAt,
		TrustLevel:     e.TrustLevel,
		ProbeFn:        e.ProbeFn,
		TTlSeconds:     e.TTlSeconds,
		Verification:   e.Verification,
		ValidFrom:      e.ValidFrom,
		ValidUntil:     e.ValidUntil,
		Links:          e.Links,
		Importance:     e.Importance,
		Source:         e.Source,
		AccessCount:    e.AccessCount,
		LastAccessedAt: e.LastAccessedAt,
		ProbeFailCount: e.ProbeFailCount,
		Content:        e.Content,
		FilePath:       e.FilePath,
	}
}

// toConfigEntryPtr 返回 toConfigEntry 结果的指针（Read 的返回形状）。
func toConfigEntryPtr(e *memory.MemoryEntry) *config.MemoryEntryV2 {
	c := toConfigEntry(e)
	return &c
}
