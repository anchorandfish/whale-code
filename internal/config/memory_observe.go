// Package config — memory_observe.go
//
// P0：记忆选择的**只写不读**观测落盘。
//
// 目的：在**零行为变化**的前提下，把「每次记忆选择到底发生了什么」落盘成 JSONL，
// 作为后续所有记忆改造（本地打分层 / 检索工具 / 常驻卡片）的**判据数据源**。
// 没有它，后面每一次调参都是盲的 —— 这是整个改造序列的第一步。
//
// 落盘位置 = {store.Dir()}/selection_trace.jsonl：
//
//	· 与记忆同域 ⇒ 天然按 workDir hash 隔离（A 仓库的选择不会记进 B 仓库）
//	· 目录 0700 / 文件 0600（与 ~/.whale/memory 同权限；刻意不放 0755 的 ~/.whale/logs）
//	· 后缀 .jsonl ≠ .md ⇒ **不会被 MemoryStore.List() 扫成一条"记忆"**（memory_store.go:160-165）
//
// ★ 只写不读：本包**不提供**任何回读该事件的 API。观测不得反向影响选择逻辑 ——
//
//	这点与 internal/bridge/approval_audit.go 的 delta F 同款纪律。
package config

import (
	"encoding/json"
	"os"
	"path/filepath"
	"time"
)

const (
	// memoryTraceSchema 观测条目的 schema 版本（对齐 bridge approvalSchemaVersion 的版本化条目设计）。
	memoryTraceSchema = 1
	// memoryTraceFile 观测文件名。
	memoryTraceFile = "selection_trace.jsonl"
	// maxTraceDescRunes 落盘时每条记忆描述的最大 rune 数（防单行过长）。
	maxTraceDescRunes = 160
	// traceKeyPrefixLen 落盘时缓存键保留的前缀长度（完整 sha256 无必要，够定位同一会话即可）。
	traceKeyPrefixLen = 16
	// maxTracePickRunes ：pick_raw 落盘的最大 rune 数（模型原始响应截断保护）。
	maxTracePickRunes = 400
)

// 分支常量：一次记忆选择最终走了哪条路（离线统计的第一步就是按它分组）。
const (
	traceBranchCacheHit   = "cache_hit"       // 会话级缓存命中
	traceBranchSyncAll    = "sync_all"        // 条目 ≤ maxSelections，本地全量同步
	traceBranchFallback   = "fallback_warmup" // 需要 AI：先给本地兜底，后台预热
	traceBranchWarmBackup = "warmup_backfill" // 后台预热完成，把 AI 结果写回缓存
	traceBranchAIPick     = "ai_pick"         // AI 选择调用的原始响应观测（每次调用一条）
)

// MemoryTraceEntry 是落盘时 manifest 里的一条记忆（只取离线分析必需的字段）。
type MemoryTraceEntry struct {
	Name     string `json:"name"`
	Type     string `json:"type"`
	Desc     string `json:"desc"`
	Modified string `json:"modified"`
	Chars    int    `json:"chars"`
}

// MemorySelectEvent 是一次记忆选择的完整观测事实。
//
// 它是**事实记录**而非"模型自报"：记录的是"我们实际注入了哪几条"，
// 模型无法伪造 —— 这正是后续度量"记忆到底有没有被用上"所需的最硬信号。
type MemorySelectEvent struct {
	Schema          int                `json:"schema"`
	TS              string             `json:"ts"`
	Key             string             `json:"key"`              // 缓存键前缀（同一会话可关联）
	EntrypointChars int                `json:"entrypoint_chars"` // MEMORY.md 索引长度（每次注入的固定开销）
	EntrypointOK    bool               `json:"entrypoint_present"`
	ManifestN       int                `json:"manifest_n"`
	Manifest        []MemoryTraceEntry `json:"manifest"`
	Branch          string             `json:"branch"`
	Selected        []string           `json:"selected"`
	SelectedN       int                `json:"selected_n"`
	OutputChars     int                `json:"output_chars"`
	WarmupStarted   bool               `json:"warmup_started,omitempty"`
	WarmupOK        bool               `json:"warmup_ok,omitempty"`
	WarmupElapsedMS int64              `json:"warmup_elapsed_ms,omitempty"`

	// ── P3-wire：本地排序的落地情况 ────────────────────────
	//
	// 这两个字段是"接线到底有没有生效"的**唯一直接证据**：manifest 超过
	// maxSelections 时，若 RankMode=rank 且 RankHits>0，即本地打分器真的参与了
	// 首轮注入的选择（而不是退回"按最近更新"）。
	RankMode string `json:"rank_mode,omitempty"` // rank / recency_only / no_hint / empty_query / under_limit
	RankHits int    `json:"rank_hits,omitempty"` // 最终注入里来自本地排序的条数

	// ── ：AI 选择的原始响应观测 ────────────────────────
	//
	// warmup_backfill 两次真机 `selected=None`（warmup_ok=true）无法定因：
	// 「返空串」与「返无数字散文」在 parseSelectedIndices 里同样得到 0 条。
	// pick_raw 把模型原始响应（截断保护）落盘，下一次复现即可定因。
	// 只写不读、零行为变化 —— 与 observe() 同一纪律。
	PickRaw     string `json:"pick_raw,omitempty"`     // 模型原始响应（≤400 rune）
	PickIndices []int  `json:"pick_indices,omitempty"` // parseSelectedIndices 解析结果（1-based）
}

// tracePath 返回观测文件路径；无 store（测试/降级装配）时返回空串 ⇒ 观测静默跳过。
//
// ★ Dir() 为空的实现也必须返回空串：否则 filepath.Join("", f) 会得到**相对路径**
// `selection_trace.jsonl`，把观测写进进程当前目录 —— 观测是"只写不读"的旁路，
// 但它不该污染用户的工作目录（测试替身常返回空 Dir）。
func (ms *MemorySelector) tracePath() string {
	if ms.store == nil {
		return ""
	}
	dir := ms.store.Dir()
	if dir == "" {
		return ""
	}
	return filepath.Join(dir, memoryTraceFile)
}

// observe 把一次选择事件追加到观测文件。
//
// ★ 零行为变化保证：本函数**只追加写**，不读回、不改任何决策；
// 任何失败（目录不可写、序列化失败、无 store）都静默返回 —— 观测绝不阻塞或中断请求路径。
func (ms *MemorySelector) observe(ev MemorySelectEvent) {
	path := ms.tracePath()
	if path == "" {
		return
	}
	ev.Schema = memoryTraceSchema
	if ev.TS == "" {
		ev.TS = time.Now().Format(time.RFC3339Nano)
	}
	data, err := json.Marshal(ev)
	if err != nil {
		return
	}
	data = append(data, '\n')
	// O_APPEND：追加写在单次 write 下由内核保证原子（配合 ≤PIPE_BUF 的条目大小）。
	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0600)
	if err != nil {
		return
	}
	defer f.Close()
	_, _ = f.Write(data)
}

// traceManifest 把 manifest 转成落盘用的精简明细。
func traceManifest(manifest []MemoryManifestEntry) []MemoryTraceEntry {
	if len(manifest) == 0 {
		return nil
	}
	out := make([]MemoryTraceEntry, 0, len(manifest))
	for _, e := range manifest {
		out = append(out, MemoryTraceEntry{
			Name:     e.Name,
			Type:     string(e.Type),
			Desc:     traceTruncateRunes(e.Description, maxTraceDescRunes),
			Modified: e.Modified.Format(time.RFC3339),
			Chars:    e.SizeChars,
		})
	}
	return out
}

// traceNames 取一组成员的 Name（落盘"最终注入了哪几条"）。
func traceNames(sel []MemoryManifestEntry) []string {
	if len(sel) == 0 {
		return nil
	}
	out := make([]string, 0, len(sel))
	for _, e := range sel {
		out = append(out, e.Name)
	}
	return out
}

// traceKey 截短缓存键（完整 sha256 落盘无必要，前缀够关联同一会话）。
func traceKey(key string) string {
	if len(key) <= traceKeyPrefixLen {
		return key
	}
	return key[:traceKeyPrefixLen]
}

// traceTruncateRunes 按 rune 截断（避免把中文截成半个字节）。
func traceTruncateRunes(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n]) + "…"
}
