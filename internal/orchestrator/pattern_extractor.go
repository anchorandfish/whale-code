// pattern_extractor.go — H12 自我进化系统 §2.1 模式提取器（v0.2 新增）。
// 设计说明 §2.1 + §3.2；落地：从 task trace 提取 Pattern 或更新已有 Pattern
// v0.2；落地：替代 v0.1 假设的 ReflectMemories 生成 Pattern
// SNAPSHOT: pkg=internal/orchestrator; type=PatternExtractor; flow={Match命中→Save更新LastUsedAt+L2AdoptionRate, 未命中→从trace提取新Pattern(VerifiedState=unverified,Scope=project,Version=1)}; degraded={Save失败/trace损坏→warn返回nil}
//
// 不复用声明（v0.2 修正致命漏洞 2/6）：
// - v0.1 曾假设直接复用 memory 包 ReflectMemories 生成 Pattern
// - v0.2 删除该假设：ReflectMemories 输入/输出均为 []string，处理 MemoryEntry 而非 Pattern
// - 本文件独立实现 Pattern 提取，全部在 orchestrator 包内，不跨包调用 memory 包接口
package orchestrator

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"strings"
	"time"
)

// PatternExtractor 模式提取器（v0.2 新增，替代 v0.1 假设的 ReflectMemories 生成 Pattern）。
// 从 task execution trace 提取 Pattern，或更新已有 Pattern 的 LastUsedAt + L2AdoptionRate。
//
// 关键约束：
// - 不复用 memory 包 ReflectMemories（跨包语义不匹配，致命漏洞 2/6 修复）
// - 不修改 Pattern struct 12 字段签名（BASE 保护）
// - VerifiedState 严格用 experience.go:22-27 四级枚举字符串常量
type PatternExtractor struct {
	experience *ExperienceStore
	nowFunc    func() time.Time
}

// NewPatternExtractor 构造模式提取器。
// experience 为 ExperienceStore 实例（必填，空将 panic-on-nil-call 由 Extract 校验）。
func NewPatternExtractor(experience *ExperienceStore) *PatternExtractor {
	return &PatternExtractor{
		experience: experience,
		nowFunc:    time.Now,
	}
}

// SetNowFunc 注入时间函数（测试用，便于固定时间）。
func (p *PatternExtractor) SetNowFunc(fn func() time.Time) {
	p.nowFunc = fn
}

// Extract 从 task trace 提取或更新 Pattern（§2.1 + §3.2）。
// 流程：
// 1. 校验 experience 非空 + ctx 未超时
// 2. 推断 task_signature（从 trace 提取 Event + AgentID + Stage 聚合）
// 3. 调 ExperienceStore.Match 查找已有 Pattern
// 4. 命中：更新 LastUsedAt + L2AdoptionRate（通过 Save 追加新版本），不改 VerifiedState
// 5. 未命中：从 trace 提取新 Pattern（VerifiedState="unverified", Scope="project", Version=1）
// 6. 调 ExperienceStore.Save 持久化
//
// 降级（§2.1 降级策略）：
// - ExperienceStore 为 nil / Save 失败（IO 错误）→ warn 不阻断，返回 nil
// - trace 为空 / 损坏 → warn 不阻断，返回 nil（无 Pattern 可提取）
// - ctx 超时 → 返回 ctx.Err()
func (p *PatternExtractor) Extract(ctx context.Context, taskID string, trace []TraceEntry) error {
	// ① ctx 校验
	if err := ctx.Err(); err != nil {
		return err
	}
	// ② experience 校验
	if p == nil || p.experience == nil {
		return nil // 降级：无 experience store，不阻断闭环
	}
	// ③ taskID 非空校验（taskID 为空不阻断，返回 nil 避免破坏闭环）
	if taskID == "" {
		return nil
	}
	// ④ trace 空校验（trace 为空直接返回，无 Pattern 可提取）
	if len(trace) == 0 {
		return nil
	}

	// ⑤ 推断 task_signature
	signature := p.inferTaskSignatureFromTrace(taskID, trace)
	if signature == "" {
		return nil // 无法推断签名，跳过
	}

	// ⑥ ExperienceStore.Match 查找已有 Pattern
	// taskType/tags 从 trace 提取，空也允许 Match 走纯签名检索
	taskType := p.inferTaskTypeFromTrace(trace)
	tags := p.extractTagsFromTrace(trace)
	hits, matchErr := p.experience.Match(signature, taskType, tags)
	if matchErr == nil && len(hits) > 0 {
		// ⑦ 命中：更新 LastUsedAt + L2AdoptionRate（不改 VerifiedState）
		// 注：Match 返回 *Pattern（指向缓存对象），必须先值拷贝再修改，
		// 否则并发 Extract 调用会同时写同一 Pattern 指针的 LastUsedAt → data race。
		existing := hits[0]
		cloned := *existing // 值拷贝，避免并发写共享指针
		cloned.LastUsedAt = p.nowFunc().UTC().Format(time.RFC3339)
		// L2AdoptionRate 由 computeAdoptionRate 在 ExperienceStore 内部维护
		// 此处仅刷新 LastUsedAt 并通过 Save 追加新版本（versioned append）
		if saveErr := p.experience.Save(&cloned); saveErr != nil {
			// 降级：Save 失败 warn 不阻断，下次任务重试
			return nil
		}
		return nil
	}

	// ⑧ 未命中：从 trace 提取新 Pattern
	steps := p.traceToSteps(trace)
	files := p.extractFilesFromTrace(trace)
	models := p.extractModelsFromTrace(trace)
	now := p.nowFunc().UTC().Format(time.RFC3339)
	version := 1

	pattern := &Pattern{
		PatternID:      p.generatePatternID(taskID, signature, version),
		TaskSignature:  signature,
		Steps:          steps,
		FilesTouched:   files,
		VerifiedModels: models,
		VerifiedAt:     now,
		L2AdoptionRate: 0,                       // 新 Pattern 采纳率初始化为 0，由 PatternValidator 后续更新
		VerifiedState:  VerifiedStateUnverified, // 新 Pattern 默认 unverified
		Scope:          ScopeProject,            // 新提取 Pattern 默认 project 作用域
		Version:        version,
		LastUsedAt:     now,
	}

	// ⑨ 持久化新 Pattern（降级：Save 失败 warn 不阻断）
	if saveErr := p.experience.Save(pattern); saveErr != nil {
		return nil
	}
	return nil
}

// traceToSteps 将 TraceEntry[] 转为 Step[]（提取 Event + AgentID + Stage）。
// TraceEntry 字段：Timestamp/SubTaskID/ParentID/Depth/Event/AgentID/Stage/Model/ErrorType
// Step 字段：Action/Target/Description
// 映射：Action=Event, Target=Stage, Description=AgentID + SubTaskID
func (p *PatternExtractor) traceToSteps(trace []TraceEntry) []Step {
	if len(trace) == 0 {
		return []Step{}
	}
	steps := make([]Step, 0, len(trace))
	for _, e := range trace {
		desc := e.AgentID
		if e.SubTaskID != "" {
			if desc != "" {
				desc += " "
			}
			desc += e.SubTaskID
		}
		steps = append(steps, Step{
			Action:      e.Event,
			Target:      e.Stage,
			Description: desc,
		})
	}
	return steps
}

// extractFilesFromTrace 从 trace 提取涉及文件路径（从 Stage 字段解析）。
// Stage 字段约定：可能含文件路径（如 "edit:main.go" / "main.go" / "src/foo.go"）
// 启发式：含 "." 且不含空格的 token 视为文件路径
func (p *PatternExtractor) extractFilesFromTrace(trace []TraceEntry) []string {
	seen := make(map[string]struct{})
	for _, e := range trace {
		stage := strings.TrimSpace(e.Stage)
		if stage == "" {
			continue
		}
		// 切分空格 + 冒号取候选 token
		for _, sep := range []string{" ", ":", ","} {
			stage = strings.ReplaceAll(stage, sep, " ")
		}
		for _, tok := range strings.Fields(stage) {
			if isLikelyFilePath(tok) {
				seen[tok] = struct{}{}
			}
		}
	}
	out := make([]string, 0, len(seen))
	for f := range seen {
		out = append(out, f)
	}
	return out
}

// extractModelsFromTrace 从 trace 提取涉及的模型列表（去重）。
func (p *PatternExtractor) extractModelsFromTrace(trace []TraceEntry) []string {
	seen := make(map[string]struct{})
	for _, e := range trace {
		if e.Model == "" {
			continue
		}
		seen[e.Model] = struct{}{}
	}
	out := make([]string, 0, len(seen))
	for m := range seen {
		out = append(out, m)
	}
	return out
}

// extractTagsFromTrace 从 trace 提取 tags（文件扩展名 + Stage tokens）。
func (p *PatternExtractor) extractTagsFromTrace(trace []TraceEntry) []string {
	tagSet := make(map[string]struct{})
	for _, e := range trace {
		stage := strings.TrimSpace(e.Stage)
		if stage == "" {
			continue
		}
		for _, tok := range strings.Fields(stage) {
			tagSet[tok] = struct{}{}
		}
	}
	// 文件扩展名也作为 tag
	for _, e := range trace {
		stage := strings.TrimSpace(e.Stage)
		for _, sep := range []string{" ", ":", ","} {
			stage = strings.ReplaceAll(stage, sep, " ")
		}
		for _, tok := range strings.Fields(stage) {
			if isLikelyFilePath(tok) {
				ext := fileExt(tok)
				if ext != "" {
					tagSet[ext] = struct{}{}
				}
			}
		}
	}
	out := make([]string, 0, len(tagSet))
	for tag := range tagSet {
		out = append(out, tag)
	}
	return out
}

// inferTaskSignatureFromTrace 推断 task_signature（自然语言描述 + 关键动词）。
// 启发式：取首个 trace 的 Stage 字段 + AgentID，组合为签名。
// 若 trace 全空返回空字符串。
func (p *PatternExtractor) inferTaskSignatureFromTrace(taskID string, trace []TraceEntry) string {
	if len(trace) == 0 {
		return ""
	}
	// 取首个非空 Stage 作为签名基础
	for _, e := range trace {
		stage := strings.TrimSpace(e.Stage)
		if stage != "" {
			// 截断到 80 字符（避免签名过长导致 Match 失败）
			if len(stage) > 80 {
				stage = stage[:80]
			}
			return stage
		}
	}
	// fallback：用 taskID
	return taskID
}

// inferTaskTypeFromTrace 从 trace 推断 task_type。
// 匹配 TaskTypeBugFix/TaskTypeDocGen/TaskTypePlan/TaskTypeReview/TaskTypeImplement。
func (p *PatternExtractor) inferTaskTypeFromTrace(trace []TraceEntry) string {
	// 合并所有 Stage / Event 字段
	var sb strings.Builder
	for _, e := range trace {
		sb.WriteString(e.Stage)
		sb.WriteString(" ")
		sb.WriteString(e.Event)
		sb.WriteString(" ")
	}
	combined := strings.ToLower(sb.String())
	for _, tt := range []string{TaskTypeBugFix, TaskTypeDocGen, TaskTypePlan, TaskTypeReview, TaskTypeImplement} {
		// 支持 kebab-case 与 snake_case 两种匹配
		if strings.Contains(combined, tt) || strings.Contains(combined, strings.ReplaceAll(tt, "-", "_")) {
			return tt
		}
	}
	return ""
}

// generatePatternID 生成 pattern_id（由 task_signature + version 哈希）。
// 格式：{signature的前16字符}-{sha256前8位}-v{version}
func (p *PatternExtractor) generatePatternID(taskID, signature string, version int) string {
	// signature safe ID：去非字母数字，截断到 16 字符
	sig := safeID(signature)
	if sig == "" {
		sig = safeID(taskID)
	}
	if sig == "" {
		sig = "unknown"
	}
	// sha256(taskID + signature + version) 前 8 位
	h := sha256.Sum256([]byte(fmt.Sprintf("%s|%s|%d", taskID, signature, version)))
	short := hex.EncodeToString(h[:])[:8]
	return fmt.Sprintf("%s-%s-v%d", sig, short, version)
}

// isLikelyFilePath 启发式判断 token 是否为文件路径（含 . 且不含空格）。
func isLikelyFilePath(tok string) bool {
	if tok == "" {
		return false
	}
	if !strings.Contains(tok, ".") {
		return false
	}
	// 排除 "1.0" 之类纯数字
	for _, r := range tok {
		if !(r >= '0' && r <= '9') && r != '.' {
			return true
		}
	}
	return false
}

// fileExt 提取文件扩展名（含 .，小写）。
func fileExt(path string) string {
	for i := len(path) - 1; i >= 0; i-- {
		if path[i] == '.' {
			return strings.ToLower(path[i:])
		}
		if path[i] == '/' || path[i] == '\\' {
			break
		}
	}
	return ""
}
