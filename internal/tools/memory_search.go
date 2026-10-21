// memory_search.go — P4：`memory_search` 工具（模型**主动**检索长期记忆）。
//
// # 为什么需要它（P3-1b 收口后遗留的缺口）
//
// 注入路径是**被动**的：每轮最多注入 5 条记忆（`config.MemorySelector`）。
// 相关记忆没被注入时，对话中的模型没有任何手段自己去找 —— 正是 同类 CLI
// （对标证据件，`memories.search/read/list` 工具）已经解决、本仓登记为
// P4 的问题。对标结论见仓库根 `记忆主动检索_Codex对照取证_2026-10-08.md`：
// 关键差异不在检索技术（本仓打分器 bm25+RRF+trigram 比 同类 CLI 的纯子串强），
// 而在「把工具交给模型 + 文案教它何时搜」。
//
// # 架构（本仓纪律）
//
// - tools 不能 import memory（memory → command → config → tools 已成链，
// 反向必环）⇒ 检索能力经**函数字段** `Executor.MemorySearchFunc` 注入，
// 对齐 CapabilityLookup / ReflectTrigger 先例（executor.go:165-180）。
// 接线层（tui/memory_wiring.go）用 memorybridge（显式 merge 层）装配，
// 与注入路径（bridge.List/Rank）**同一数据源** —— 不重蹈「读路径两个数据源」
// 的 覆辙（P3-1b 回执 §4）。
// - 纯读：LevelReadOnly + CapRead；不写盘、不跑命令、不碰网络。
// - 零命中是正常情况（词法检索有语义上限，实测改写类 0.000）⇒ 非错误输出，
// 模型可据此继续正常对话或换关键词重试。
package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
)

// memorySearchToolName 工具名（注册表 / handler 表 / 能力分桶三处同名）。
const memorySearchToolName = "memory_search"

// memorySearchTopKDefaults topK 钳制边界。
// 缺省 5 与注入路径的 maxSelections(5) 对齐；上限 10 防模型一次拖全库。
const (
	memorySearchTopKDefault = 5
	memorySearchTopKMax     = 10
)

// MemorySearchInput 是 memory_search 工具的入参（BuildSchemaFromStruct 反射生成 schema）。
type MemorySearchInput struct {
	Query string `json:"query" desc:"Search keywords. Lexical match: include distinctive (>=3 char) substrings of the topic; paraphrases may not match"`
	TopK  int    `json:"top_k,omitempty" desc:"Max results 1-10 (default 5)"`
}

// MemorySearchHit 是单条命中（tools 包自有形状，不 import memory —— 解耦环）。
// 接线层适配器负责把 memory.MemoryEntry 映射成本结构。
type MemorySearchHit struct {
	Name        string `json:"name"`
	Description string `json:"description,omitempty"`
	Type        string `json:"type,omitempty"`
	Path        string `json:"path,omitempty"`
	Updated     string `json:"updated,omitempty"`
	Excerpt     string `json:"excerpt,omitempty"` // 内容首段摘录（description 为空时兜底）
}

// memorySearchHandler 返回 memory_search 的执行 handler。
//
// 降级语义（belt-and-braces 对齐 S3/ 精神）：
// - MemorySearchFunc == nil（未接线：测试/非 tui 直构）⇒ 明确报
// "not wired / not available" —— 不假成功、不 panic；生产 tui/runner 路径
// 必经 wireExecutorMemoryCallbacks 接线（与 CapabilityLookup 同一时序点）。
// - 检索层返回空/错误 ⇒ 区分：空 = 正常零命中输出；error = 工具错误。
func (e *Executor) memorySearchHandler() Handler {
	return func(ctx context.Context, raw json.RawMessage) (string, error) {
		var input MemorySearchInput
		if err := parseInput(raw, &input); err != nil {
			return "", fmt.Errorf("resolveinputfailed: %w", err)
		}
		query := strings.TrimSpace(input.Query)
		if query == "" {
			return "query cannot be empty", fmt.Errorf("query cannot be empty")
		}
		if e.MemorySearchFunc == nil {
			return "memory search is not wired in this context (MemorySearchFunc nil)",
				fmt.Errorf("memory search not wired")
		}
		topK := input.TopK
		if topK <= 0 {
			topK = memorySearchTopKDefault
		}
		if topK > memorySearchTopKMax {
			topK = memorySearchTopKMax
		}

		hits, err := e.MemorySearchFunc(ctx, query, topK)
		if err != nil {
			return "", fmt.Errorf("memory search failed: %w", err)
		}
		return formatMemorySearchOutput(query, hits), nil
	}
}

// formatMemorySearchOutput 渲染命中列表（模型可读文本面）。
// 零命中显式写明 "0 hits"（对齐 同类 CLI 模板的"If there are no relevant hits, stop
// memory lookup"语义：模型据此决定继续正常工作，而不是反复空搜）。
func formatMemorySearchOutput(query string, hits []MemorySearchHit) string {
	var sb strings.Builder
	fmt.Fprintf(&sb, "memory_search: %d hit(s) for %q\n", len(hits), query)
	if len(hits) == 0 {
		sb.WriteString("No relevant memories. Zero hits is normal for paraphrased queries — try different keywords or continue without memory.\n")
		return sb.String()
	}
	for i, h := range hits {
		fmt.Fprintf(&sb, "%d. [%s] %s", i+1, h.Type, h.Name)
		if h.Updated != "" {
			fmt.Fprintf(&sb, " (updated %s)", h.Updated)
		}
		sb.WriteString("\n")
		if h.Description != "" {
			fmt.Fprintf(&sb, "   %s\n", h.Description)
		}
		if h.Excerpt != "" {
			fmt.Fprintf(&sb, "   excerpt: %s\n", h.Excerpt)
		}
		if h.Path != "" {
			fmt.Fprintf(&sb, "   path: %s\n", h.Path)
		}
	}
	sb.WriteString("Use read_file on a path above for full content; cite memory-derived facts as possibly stale.\n")
	return sb.String()
}
