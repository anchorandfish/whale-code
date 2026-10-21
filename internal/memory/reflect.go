// reflect.go — H11/N27 Phase C C1 反思闭环（ReflectMemories + /memory reflect 命令）。
// REF-外部-GenerativeAgents@https://arxiv.org/abs/2304.03442+2026-07-16访问-reflection 机制；借鉴逻辑：episodic→procedural 合成；落地：ReflectMemories 收集 episodic + 小模型生成 procedural
// REF-外部-A-MEM@https://arxiv.org/abs/2502.12110+2026-07-16访问-双向链接；借鉴逻辑：合成记忆自动建 link；落地：ReflectMemories 写入后 LinkMemories 双向链接（失败不阻断 warn）
// ；落地：5 接口之一，H11/N27 主设计，H10 不直接调用
// ；落地：AI 失败返回 error 不写部分记忆；LinkMemories 失败 warn 不阻断；parse 部分失败跳过该条
// SNAPSHOT: filter={within=7天, access_count>=2}; model={Phase1=主模型, Phase2+=config.reflect_model}; output=1-3条procedural; write_fields={TrustLevel=procedural, Verification=unverified, Source=reflection, Importance=50}; link=LinkMemories双向(失败warn不阻断); atomicity=AI失败返回error不写部分记忆; parse_partial_fail=跳过格式错条不阻断整体

package memory

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"strings"
	"time"
)

// ReflectFilterWithin 反思过滤窗口：7 天内的 episodic 才参与反思（v0.4 §7.2）。
const ReflectFilterWithin = 7 * 24 * time.Hour

// ReflectFilterMinAccessCount 反思过滤最小访问次数：access_count >= 2 才参与反思（v0.4 §7.2）。
const ReflectFilterMinAccessCount = 2

// ReflectDefaultMaxProcedural 反思默认生成 procedural 记忆上限：3 条（v0.4 §7.2）。
const ReflectDefaultMaxProcedural = 3

// ReflectDefaultImportance 反思生成 procedural 记忆默认重要性：50（与 Write 零值默认值一致）。
const ReflectDefaultImportance = 50

// AICaller 可注入的 AI 调用函数（用于测试 mock，生产环境由 app.go 注入实际模型调用）。
// 输入：ctx + model + prompt；输出：AI 响应文本。
// ；落地：通过 aiCaller 全局变量注入，避免硬编码依赖 ai 包
var aiCaller func(ctx context.Context, model, prompt string) (string, error)

// SetAICaller 注入 AI 调用函数（app.go 接线时调用，测试时也可注入 mock）。
// 不注入时 ReflectMemories 返回 error（防误用）。
func SetAICaller(fn func(ctx context.Context, model, prompt string) (string, error)) {
	aiCaller = fn
}

// resetAICallerForTest 测试用清理 aiCaller（避免跨测试污染）。
func resetAICallerForTest() {
	aiCaller = nil
}

// ReflectOptions 反思选项。
type ReflectOptions struct {
	Model         string // AI 模型名（Phase 1 传主模型，Phase 2+ 传 config.reflect_model）
	MaxProcedural int    // 生成 procedural 记忆上限，默认 3（v0.4 §7.2）
}

// ReflectMemories 将近期 episodic 记忆合成为 procedural 记忆。
// 对齐 v0.4 §7.2：Generative Agents reflection 简化版 + A-MEM 双向链接。
//
// 流程：
// 1. 收集 episodic 记忆（sourceNames 指定时按名加载；nil 时自动从 globalMergeLayer 收集所有符合条件的）
// 2. 过滤：7 天内 + access_count >= 2（filterEpisodic）
// 3. 小模型生成 1-3 条 procedural 记忆草稿（callAIModel → parseProceduralResponse）
// 4. 写入指定 scope（trust_level=procedural, verification=unverified, source=reflection, importance=50）
// 5. LinkMemories 双向链接（失败不阻断，warn）
//
// 原子性：AI 调用失败时返回 error，不写部分记忆。
//
// scope 参数："user" 或 "project"（默认 project）。
// 返回：生成的 procedural 记忆 Name 列表 + error。
//
// REF-外部-GenerativeAgents@https://arxiv.org/abs/2304.03442+2026-07-16访问-reflection；借鉴逻辑：episodic→procedural 合成
// REF-外部-A-MEM@https://arxiv.org/abs/2502.12110+2026-07-16访问-双向链接；借鉴逻辑：合成记忆建 link
// ；落地：5 接口之一
func ReflectMemories(ctx context.Context, sourceNames []string, scope string, opts ReflectOptions) ([]string, error) {
	// 1. 注入校验：aiCaller 未注入时返回 error（防误用）
	if aiCaller == nil {
		return nil, errors.New("ReflectMemories: aiCaller not injected, call SetAICaller first")
	}

	// 2. scope 默认值 + 校验
	if scope == "" {
		scope = "project"
	}
	if scope != "user" && scope != "project" {
		return nil, fmt.Errorf("ReflectMemories: invalid scope %q (must be user or project)", scope)
	}

	// 3. MaxProcedural 默认值
	if opts.MaxProcedural <= 0 {
		opts.MaxProcedural = ReflectDefaultMaxProcedural
	}

	// 4. 收集 episodic 记忆源
	sources, err := collectEpisodicSources(sourceNames)
	if err != nil {
		return nil, fmt.Errorf("ReflectMemories: collect sources: %w", err)
	}

	// 5. 过滤：7 天内 + access_count >= 2
	sources = filterEpisodic(sources, ReflectFilterWithin, ReflectFilterMinAccessCount)
	if len(sources) == 0 {
		// 无可反思源，返回空列表（不报错，降级策略）
		return nil, nil
	}

	// 6. 构造反思 prompt
	prompt := buildReflectPrompt(sources)

	// 7. 调用 AI 模型（原子性：失败返回 error，不写部分记忆）
	resp, err := aiCaller(ctx, opts.Model, prompt)
	if err != nil {
		return nil, fmt.Errorf("ReflectMemories: call AI model: %w", err)
	}

	// 8. 解析 AI 响应，提取 1-3 条 procedural 记忆草稿
	procedurals := parseProceduralResponse(resp)
	if len(procedurals) == 0 {
		// AI 返回无法解析，返回空列表（不报错，但记日志）
		log.Printf("WARN: ReflectMemories: AI response parsed 0 procedural entries")
		return nil, nil
	}

	// 9. 限制 MaxProcedural
	if len(procedurals) > opts.MaxProcedural {
		procedurals = procedurals[:opts.MaxProcedural]
	}

	// 10. 写入 procedural 记忆 + 建立双向链接
	var newNames []string
	for _, p := range procedurals {
		// 自动填字段（trust_level/verification/source/importance）
		p.TrustLevel = TrustProcedural
		p.Verification = VerificationUnverified
		p.Source = SourceReflection
		if p.Importance == 0 {
			p.Importance = ReflectDefaultImportance
		}
		p.Type = MemoryTypeFeedback

		// 写入指定 scope
		if err := writeEntryToScope(p, scope); err != nil {
			// 写入失败不阻断整体（已写的 procedural 保留），记 warn 继续
			log.Printf("WARN: ReflectMemories: writeEntryToScope %q failed: %v", p.Name, err)
			continue
		}
		newNames = append(newNames, p.Name)

		// LinkMemories 双向链接（失败不阻断，warn）
		for _, src := range sources {
			if err := LinkMemories(p.Name, src.Name, "reflection source"); err != nil {
				log.Printf("WARN: ReflectMemories: LinkMemories %s↔%s failed: %v", p.Name, src.Name, err)
				// 不阻断主流程，继续下一条 link
			}
		}
	}

	return newNames, nil
}

// collectEpisodicSources 收集 episodic 记忆源。
// sourceNames 指定时按名加载；nil 时自动从 globalMergeLayer 收集所有记忆。
//
// 内存字段合并（关键）：
// - ListMerged 从文件加载 entry，AccessCount/LastAccessedAt/ProbeFailCount 是 yaml:"-" 内存字段，文件中为 0
// - capabilityStore 保存运行时 entry（含内存字段），用于 ReflectMemories 过滤 access_count >= 2
// - 本函数合并：ListMerged 加载所有 entry（含 frontmatter 字段），再用 capabilityStore 的内存字段覆盖
//
// 这样 filterEpisodic 能正确按 AccessCount 过滤（AccessCount 是进程内状态，重启归零，符合 v0.4 §7.2 语义）。
func collectEpisodicSources(sourceNames []string) ([]*MemoryEntry, error) {
	if globalMergeLayer == nil {
		return nil, errors.New("globalMergeLayer not initialized, call SetGlobalMergeLayer first")
	}

	merged, err := globalMergeLayer.ListMerged()
	if err != nil {
		return nil, fmt.Errorf("ListMerged: %w", err)
	}

	// 合并 capabilityStore 内存字段（AccessCount/LastAccessedAt/ProbeFailCount）
	capabilityStore.mu.RLock()
	defer capabilityStore.mu.RUnlock()
	for _, e := range merged {
		if memEntry, ok := capabilityStore.entries[e.Name]; ok {
			// 用内存字段覆盖文件加载的零值
			e.AccessCount = memEntry.AccessCount
			e.LastAccessedAt = memEntry.LastAccessedAt
			e.ProbeFailCount = memEntry.ProbeFailCount
		}
	}

	if sourceNames == nil {
		// 全量返回，由 filterEpisodic 过滤
		return merged, nil
	}

	// 按名过滤
	nameSet := make(map[string]bool, len(sourceNames))
	for _, n := range sourceNames {
		nameSet[n] = true
	}
	var result []*MemoryEntry
	for _, e := range merged {
		if nameSet[e.Name] {
			result = append(result, e)
		}
	}
	return result, nil
}

// filterEpisodic 过滤 episodic 记忆：7 天内 + access_count >= 2（v0.4 §7.2）。
// within = 时间窗口（默认 7 天）；minAccessCount = 最小访问次数（默认 2）。
// CreatedAt 超过 within 的被剔除；AccessCount < minAccessCount 的被剔除。
func filterEpisodic(sources []*MemoryEntry, within time.Duration, minAccessCount int) []*MemoryEntry {
	if len(sources) == 0 {
		return nil
	}
	now := time.Now()
	cutoff := now.Add(-within)

	var result []*MemoryEntry
	for _, e := range sources {
		if e == nil {
			continue
		}
		// 时间过滤：CreatedAt 在 cutoff 之后（7 天内）
		if e.CreatedAt.Before(cutoff) {
			continue
		}
		// 访问次数过滤：AccessCount >= 2
		if e.AccessCount < minAccessCount {
			continue
		}
		result = append(result, e)
	}
	return result
}

// buildReflectPrompt 构造反思 prompt，喂给小模型生成 1-3 条 procedural 记忆。
// 参考 Generative Agents @arxiv:2304.03442 reflection 机制。
// system 指令要求生成 YAML frontmatter + Markdown body 格式，便于 parseProceduralResponse 解析。
func buildReflectPrompt(sources []*MemoryEntry) string {
	var sb strings.Builder
	sb.WriteString("你是反思 AI，基于近期 episodic 记忆合成 procedural 操作经验。\n")
	sb.WriteString("要求：\n")
	sb.WriteString("1. 生成 1-3 条 procedural 记忆（高频访问的 episodic 合成）\n")
	sb.WriteString("2. 每条记忆格式：YAML frontmatter + Markdown body，用 --- 分隔\n")
	sb.WriteString("3. frontmatter 必含 name / description 字段，body 是操作经验总结\n")
	sb.WriteString("4. name 用英文短横线分隔（如 gcc-build-options）\n\n")
	sb.WriteString("示例格式：\n")
	sb.WriteString("---\nname: example-procedural\ndescription: 示例 procedural 记忆\n---\n这是操作经验总结正文\n\n")
	sb.WriteString("===\n近期 episodic 记忆：\n")

	for i, e := range sources {
		sb.WriteString(fmt.Sprintf("\n[记忆 %d]\n", i+1))
		sb.WriteString(fmt.Sprintf("name: %s\n", e.Name))
		sb.WriteString(fmt.Sprintf("description: %s\n", e.Description))
		sb.WriteString(fmt.Sprintf("content: %s\n", e.Content))
	}

	sb.WriteString("\n===\n请输出 1-3 条 procedural 记忆，每条用 --- 分隔。")
	return sb.String()
}

// parseProceduralResponse 解析 AI 返回，提取 1-3 条 procedural 记忆草稿。
// 期望格式（buildReflectPrompt 指定的）：
//
//	---
//	name: <Name>
//	description: <Description>
//	---
//	<Content>
//
// 多条之间用空行分隔。解析失败时跳过该条（不阻断整体），log warn。
// 返回 []*MemoryEntry（仅含 Name/Description/Content 三字段，其他字段由 ReflectMemories 填充）。
func parseProceduralResponse(resp string) []*MemoryEntry {
	if strings.TrimSpace(resp) == "" {
		return nil
	}

	var result []*MemoryEntry

	// 按 "---" 分割为多个块
	// 每个记忆格式：---\nname: x\ndescription: y\n---\nbody
	// 拆分策略：找到所有 "---" 行的位置，相邻两个 --- 之间是 frontmatter，之后到下一个 --- 之前是 body
	lines := strings.Split(resp, "\n")

	type pendingEntry struct {
		fmLines  []string
		bodyText string
	}
	var pending *pendingEntry
	var bodyBuilder strings.Builder
	inBody := false

	flushPending := func() {
		if pending == nil {
			return
		}
		// 解析 frontmatter
		entry, err := parseProceduralFrontmatter(pending.fmLines, pending.bodyText)
		if err != nil {
			log.Printf("WARN: parseProceduralResponse: skip invalid entry: %v", err)
		} else if entry != nil && entry.Name != "" {
			result = append(result, entry)
		}
		pending = nil
	}

	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		if trimmed == "---" {
			if pending == nil {
				// 开始新条目的 frontmatter
				pending = &pendingEntry{}
				inBody = false
			} else if !inBody {
				// frontmatter 结束，进入 body
				inBody = true
				bodyBuilder.Reset()
			} else {
				// body 中遇到 --- 视为新条目开始，flush 当前
				pending.bodyText = strings.TrimSpace(bodyBuilder.String())
				flushPending()
				pending = &pendingEntry{}
				inBody = false
			}
		} else if pending != nil {
			if inBody {
				bodyBuilder.WriteString(line)
				bodyBuilder.WriteString("\n")
			} else {
				pending.fmLines = append(pending.fmLines, line)
			}
		}
	}

	// flush 最后一个 pending
	if pending != nil {
		pending.bodyText = strings.TrimSpace(bodyBuilder.String())
		flushPending()
	}

	return result
}

// parseProceduralFrontmatter 解析单条 procedural 记忆的 frontmatter + body。
// 输入：fmLines = frontmatter 行列表（不含 --- 分隔符）；bodyText = Markdown body。
// 仅支持 name / description 两字段（其他字段由 ReflectMemories 填充）。
func parseProceduralFrontmatter(fmLines []string, bodyText string) (*MemoryEntry, error) {
	// 简单 YAML 解析（仅 name/description 两行）
	entry := &MemoryEntry{}
	for _, line := range fmLines {
		// 去除前后空白
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		// 解析 "key: value"
		idx := strings.Index(line, ":")
		if idx < 0 {
			continue
		}
		key := strings.TrimSpace(line[:idx])
		value := strings.TrimSpace(line[idx+1:])
		// 去除可能的引号
		value = strings.Trim(value, "\"'")

		switch key {
		case "name":
			entry.Name = value
		case "description":
			entry.Description = value
		}
	}

	if entry.Name == "" {
		return nil, errors.New("frontmatter missing name field")
	}

	// 尝试 JSON 解析（如果 frontmatter 是 JSON 格式，便于测试 mock）
	if len(fmLines) == 1 && strings.HasPrefix(fmLines[0], "{") {
		var jsonEntry struct {
			Name        string `json:"name"`
			Description string `json:"description"`
		}
		if err := json.Unmarshal([]byte(fmLines[0]), &jsonEntry); err == nil {
			entry.Name = jsonEntry.Name
			entry.Description = jsonEntry.Description
		}
	}

	entry.Content = bodyText
	return entry, nil
}

// writeEntryToScope 写入 procedural 记忆到指定 scope，自动填字段。
// scope="user" → globalMergeLayer.userStore；scope="project" → globalMergeLayer.projectStore。
// 注：procedural trust_level 不会被 PIT-001 防护拦截（仅 capability 才强制 project scope）。
func writeEntryToScope(entry *MemoryEntry, scope string) error {
	if globalMergeLayer == nil {
		return errors.New("writeEntryToScope: globalMergeLayer not initialized")
	}

	var store *MemoryStore
	if scope == "user" {
		store = globalMergeLayer.userStore
	} else {
		store = globalMergeLayer.projectStore
	}
	if store == nil {
		return fmt.Errorf("writeEntryToScope: store for scope %q is nil", scope)
	}

	return store.Write(entry)
}
