// compact.go — H11/N27 Phase C C2 上下文压缩（CompactContext + /memory compact 命令）。
// REF-外部-同类CLI@2026-03-31快照-auto-compact；借鉴逻辑：长会话压缩；落地：CompactContext 改丢弃式为可检索式（写入 memory store，可被 SearchMemory 检索）
// REF-外部-MemGPT@https://arxiv.org/abs/2310.08560+2026-07-16访问-paging；借鉴逻辑：context 压缩为 memory；落地：CompactContext 写 ephemeral 记忆（可被 SearchMemory 检索）
// ；落地：5 接口之一，Phase 2+ 引入，H11/N27 主设计
// 跨模块依赖；落地：H10 → H11/N27 单向，H10 修改 schema 需通知；CompactSource.SchemaVersion 实现运行时 drift 检测
// ；落地：AI 失败返回 error 不写记忆；schema drift 拒绝解析不写记忆；LinkMemories 失败 warn 不阻断
// SNAPSHOT: CompactSource={TaskID, TracePath, QualityPath, SchemaVersion}; flow={1.读trace+quality(校验SchemaVersion), 2.小模型摘要, 3.写ephemeral记忆(verification=unverified,source=ai_self,importance=50), 4.LinkMemories关联stable/procedural, 5.返回Name}; cross_module_dep=H10→H11/N27单向(02§2.7.3通知声明); schema_drift=不匹配warn+拒绝解析(审查报告优化5); quality_path_not_exist=降级trace-only+warn; atomicity=AI失败/schema drift返回error不写记忆

package memory

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"os"
	"strings"
	"time"

	"github.com/anchorandfish/whale-code/internal/orchrecord"
)

// DefaultCompactSchemaVersion CompactContext 期望的 trace/quality schema 默认版本。
//
// ★ 从 internal/orchrecord 派生（根因修复）：消费者不再自持一份版本常量副本，
//
//	而是与生产者同源 —— 两层对「默认版本」的理解不会悄然分裂。
//
// 文件首行若无 _schema_version 字段，默认视为 "v1"（向后兼容 Phase 0/1 已有 trace/quality 文件）。
const DefaultCompactSchemaVersion = orchrecord.DefaultCompactSchemaVersion

// CompactSource 压缩源数据，含 schema 版本用于 drift 检测（审查报告优化 5）。
//
// 字段：
// - TaskID：任务 ID（用于命名 compact 记忆）
// - TracePath：H10 trace.jsonl 路径（跨模块依赖，02 §2.7.3 通知声明）
// - QualityPath：H10 quality.jsonl 路径（空字符串时降级为 trace-only + warn）
// - SchemaVersion：运行时 schema drift 检测（审查报告优化 5，空字符串默认 DefaultCompactSchemaVersion）
type CompactSource struct {
	TaskID        string // 任务 ID（用于命名 compact 记忆）
	TracePath     string // H10 trace.jsonl 路径（跨模块依赖，02 §2.7.3 通知声明）
	QualityPath   string // H10 quality.jsonl 路径（空字符串时降级为 trace-only + warn）
	SchemaVersion string // 运行时 schema drift 检测（审查报告优化 5）
}

// CompactContext 将长会话 trace+quality 压缩为 ephemeral 记忆。
// 对齐 v0.4 §7.6：同类 CLI auto-compact + MemGPT paging 简化版。
//
// 流程：
// 1. 读取 trace + quality（校验 SchemaVersion，drift 则 warn + 拒绝解析）
// 2. 小模型生成摘要
// 3. 写入 ephemeral 记忆（verification=unverified, source=ai_self, importance=50）
// 4. LinkMemories 关联同主题 stable/procedural（失败不阻断，warn）
//
// 原子性：AI 调用失败或 schema drift 时返回 error，不写记忆。
// 降级策略：quality 路径为空或文件不存在时，降级为 trace-only 压缩 + warn 日志（不报错）。
//
// 跨模块依赖：H10 → H11/N27 单向（02 §2.7.3 通知声明），
// H10 修改 QualityRecord/TraceEntry schema 时需通知本模块更新 DefaultCompactSchemaVersion。
//
// REF-外部-同类CLI@2026-03-31快照-auto-compact；借鉴逻辑：长会话压缩改丢弃式为可检索式
// REF-外部-MemGPT@https://arxiv.org/abs/2310.08560+2026-07-16访问-paging；借鉴逻辑：context 压缩为 memory
// ；落地：5 接口之一，Phase 2+ 引入
// ；落地：AI 失败/schema drift 返回 error 不写记忆
func CompactContext(ctx context.Context, source CompactSource) ([]string, error) {
	// 1. 注入校验：aiCaller 未注入时返回 error（防误用）
	if aiCaller == nil {
		return nil, errors.New("CompactContext: aiCaller not injected, call SetAICaller first")
	}

	// 2. TaskID 必填校验
	if source.TaskID == "" {
		return nil, errors.New("CompactContext: TaskID must not be empty")
	}

	// 3. TracePath 必填校验（quality 可降级，trace 不可降级）
	if source.TracePath == "" {
		return nil, errors.New("CompactContext: TracePath must not be empty")
	}

	// 4. 默认 schema 版本
	if source.SchemaVersion == "" {
		source.SchemaVersion = DefaultCompactSchemaVersion
	}

	// 5. 读 trace（schema drift 则拒绝解析，不写记忆）
	trace, err := readTraceJSONL(source.TracePath, source.SchemaVersion)
	if err != nil {
		return nil, fmt.Errorf("CompactContext: read trace: %w", err)
	}

	// 6. 读 quality（schema drift 则拒绝；quality 不存在时降级为空切片 + warn）
	quality, err := readQualityJSONL(source.QualityPath, source.SchemaVersion)
	if err != nil {
		return nil, fmt.Errorf("CompactContext: read quality: %w", err)
	}

	// 7. trace 和 quality 都为空时无法压缩，返回 error（无法生成有意义的摘要）
	if len(trace) == 0 && len(quality) == 0 {
		return nil, errors.New("CompactContext: trace and quality both empty, nothing to compact")
	}

	// 8. 构造压缩 prompt
	prompt := buildCompactPrompt(trace, quality)

	// 9. 调 AI 模型（原子性：失败返回 error 不写记忆）
	resp, err := aiCaller(ctx, "", prompt)
	if err != nil {
		return nil, fmt.Errorf("CompactContext: call AI model: %w", err)
	}
	if strings.TrimSpace(resp) == "" {
		// AI 返回空，记日志但不报错（降级策略）
		log.Printf("WARN: CompactContext: AI response empty, no memory written for task %q", source.TaskID)
		return nil, nil
	}

	// 10. 构造 ephemeral entry（写入 project scope，走 Ebbinghaus 衰减通道）
	now := time.Now()
	entry := &MemoryEntry{
		Name:           fmt.Sprintf("compact-%s-%d", source.TaskID, now.Unix()),
		Description:    "compact of task " + source.TaskID,
		Type:           MemoryTypeFeedback,
		TrustLevel:     TrustEphemeral, // 短期压缩记忆走 Ebbinghaus 衰减
		Verification:   VerificationUnverified,
		Source:         SourceAISelf,
		Importance:     50,
		CreatedAt:      now,
		UpdatedAt:      now,
		LastAccessedAt: now, // 审查报告优化 3：设置 LastAccessedAt 避免 ApplyForgetting 立即衰减（零值会导致 deltaTime≈56年→retention≈0→立即 contradicted）
	}
	entry.SetContent(resp) // Markdown body

	// 11. 写入 project scope
	if globalMergeLayer == nil {
		return nil, errors.New("CompactContext: global MergeLayer not initialized, call SetGlobalMergeLayer first")
	}
	if globalMergeLayer.projectStore == nil {
		return nil, errors.New("CompactContext: projectStore is nil")
	}
	if err := globalMergeLayer.projectStore.Write(entry); err != nil {
		return nil, fmt.Errorf("CompactContext: write ephemeral entry: %w", err)
	}

	// 12. 自动 LinkMemories 关联同主题 stable/procedural（失败不阻断主流程，warn）
	// 通过 SearchMemory 检索同主题记忆（taskID 作为 query），找到 stable/procedural 类型记忆建双向链接。
	// 失败时仅 warn，不阻断主流程（ephemeral 已写入不应回滚）。
	if err := linkCompactToRelatedMemories(ctx, entry.Name, source.TaskID); err != nil {
		log.Printf("WARN: CompactContext: link compact %q to related memories failed: %v", entry.Name, err)
		// 不阻断主流程
	}

	return []string{entry.Name}, nil
}

// readTraceJSONL 读取 H10 trace.jsonl，校验 schema 版本（drift 则 warn + 拒绝解析）。
// 复用 orchrecord.ReadTrace(path)（读取契约唯一权威）但前置 schema 检查。
//
// 文件不存在返回 error（trace 不可降级，与 quality 不同）。
// 首行 _schema_version 缺失时默认 DefaultCompactSchemaVersion（向后兼容 Phase 0/1）。
// 首行 _schema_version 与 expectedSchema 不匹配时返回 schema drift error。
//
// schema marker 行过滤（审查报告优化 6）：若文件首行是 schema marker（含 _schema_version 字段），
// H10 ReadTrace 会将其解析为零值 TraceEntry 混入正常列表，buildCompactPrompt 会输出噪声。
// 本函数在 ReadTrace 返回后过滤掉 SubTaskID="schema-marker" 的零值条目。
//
// 跨模块依赖；落地：复用 H10 ReadTrace 前置 schema 检查
func readTraceJSONL(path string, expectedSchema string) ([]orchrecord.TraceEntry, error) {
	if path == "" {
		return nil, errors.New("readTraceJSONL: path is empty")
	}

	// 前置 schema drift 检查
	actualSchema, err := readSchemaVersion(path)
	if err != nil {
		return nil, fmt.Errorf("read schema version: %w", err)
	}
	if err := detectSchemaDrift(actualSchema, expectedSchema); err != nil {
		return nil, err
	}

	// 复用 H10 ReadTrace（全量读取所有行）
	entries, err := orchrecord.ReadTrace(path)
	if err != nil {
		return nil, fmt.Errorf("orchrecord.ReadTrace: %w", err)
	}

	// 过滤 schema marker 行（审查报告优化 6）：
	// marker 行通常 SubTaskID="schema-marker" 或 Event="" 且 Timestamp=0
	return filterRealTraceEntries(entries), nil
}

// filterRealTraceEntries 过滤掉 schema marker 行，仅保留真实 TraceEntry。
// 判定：SubTaskID == "schema-marker" 或 (Event == "" && Timestamp == 0) 视为 marker。
//
// ⚠️ D4 遗留（附带必做项 ii 未在本轮处理，见回执）：R4 抽验已证明本过滤在
//
//	生产中是**死代码**——生产者（orchestrator）从不产生 `schema-marker` 行，且
//	`Append` 保证 Timestamp 非零，两条判据对生产者输出永不命中。这是「消费者单方
//	发明的协议」的语义残留。是否删除（或让生产者真正承担 marker 协议）属 D4 独立
//	决策，需单独判别器证明"行为中性"，本轮（反向 import）不越界处理。
func filterRealTraceEntries(entries []orchrecord.TraceEntry) []orchrecord.TraceEntry {
	if len(entries) == 0 {
		return entries
	}
	result := make([]orchrecord.TraceEntry, 0, len(entries))
	for _, e := range entries {
		if e.SubTaskID == "schema-marker" {
			continue
		}
		if e.Event == "" && e.Timestamp == 0 {
			continue
		}
		result = append(result, e)
	}
	return result
}

// readQualityJSONL 读取 H10 quality.jsonl，校验 schema 版本（drift 则 warn + 拒绝解析）。
// 复用 H10 QualityStore.ScanAll() 但前置 schema 检查。
//
// 降级策略：quality 路径为空或文件不存在时，返回空切片 + warn 日志（不报错）。
// 调用方应据此降级为 trace-only 压缩。
//
// 跨模块依赖；落地：复用 H10 QualityStore.ScanAll 前置 schema 检查
func readQualityJSONL(path string, expectedSchema string) ([]orchrecord.QualityRecord, error) {
	// 降级策略：路径为空，返回空切片 + warn
	if path == "" {
		log.Printf("WARN: readQualityJSONL: quality path is empty, degrading to trace-only compaction")
		return nil, nil
	}

	// 文件不存在，降级为空切片 + warn（不报错）
	if _, err := os.Stat(path); err != nil {
		if os.IsNotExist(err) {
			log.Printf("WARN: readQualityJSONL: quality file not exist %q, degrading to trace-only compaction", path)
			return nil, nil
		}
		return nil, fmt.Errorf("readQualityJSONL: stat %s: %w", path, err)
	}

	// 前置 schema drift 检查
	actualSchema, err := readSchemaVersion(path)
	if err != nil {
		return nil, fmt.Errorf("read schema version: %w", err)
	}
	if err := detectSchemaDrift(actualSchema, expectedSchema); err != nil {
		return nil, err
	}

	// 复用读取契约权威 ScanAllQuality()（：不再反向 import orchestrator 的写入端）
	records, err := orchrecord.ScanAllQuality(path)
	if err != nil {
		return nil, fmt.Errorf("orchrecord.ScanAllQuality: %w", err)
	}
	return records, nil
}

// readSchemaVersion 读 JSONL 首行提取 _schema_version 字段。
// 首行不是合法 JSON 或不含 _schema_version 字段时返回空字符串（调用方按默认处理）。
// 文件不存在或为空时返回空字符串。
//
// 路径穿越防护（审查报告优化 2，defense in depth）：与 H10 ReadTrace/QualityStore 一致，
// path 含 ".." 时拒绝，避免恶意 taskID 拼接出敏感路径被读取首行。
func readSchemaVersion(path string) (string, error) {
	// 路径穿越防护（审查报告优化 2，defense in depth）
	if strings.Contains(path, "..") {
		return "", fmt.Errorf("readSchemaVersion: path contains '..': %s", path)
	}

	f, err := os.Open(path)
	if err != nil {
		if os.IsNotExist(err) {
			return "", nil
		}
		return "", fmt.Errorf("open %s: %w", path, err)
	}
	defer f.Close()

	scanner := bufio.NewScanner(f)
	scanner.Buffer(make([]byte, 64*1024), 4*1024*1024)
	if !scanner.Scan() {
		// 空文件
		return "", nil
	}

	var probe struct {
		// ★ marker 字段名的唯一权威 = orchrecord.SchemaVersionMarker。
		// Go 的 struct tag 必须是字符串字面量、无法引用常量，故此处硬编码；
		// 由「import 方向门 + orchrecord 侧 SchemaVersionMarker 常量」共同守护一致性。
		SchemaVersion string `json:"_schema_version"`
	}
	if err := json.Unmarshal(scanner.Bytes(), &probe); err != nil {
		// 首行不是合法 JSON（可能是普通 TraceEntry 行），视为无 schema marker
		return "", nil
	}
	return probe.SchemaVersion, nil
}

// detectSchemaDrift 校验文件 schema 版本是否匹配 expectedSchema（审查报告优化 5）。
// actualSchema 空字符串时默认 DefaultCompactSchemaVersion（向后兼容 Phase 0/1 已有文件）。
// 不匹配返回 error（含 warn 信息：当前版本 / 期望版本 / 建议 H10 通知 H11/N27）。
//
// 跨模块依赖；落地：H10 修改 schema 需通知 H11/N27，drift 拒绝解析
func detectSchemaDrift(actualSchema, expectedSchema string) error {
	if actualSchema == "" {
		actualSchema = DefaultCompactSchemaVersion
	}
	if expectedSchema == "" {
		expectedSchema = DefaultCompactSchemaVersion
	}
	if actualSchema != expectedSchema {
		return fmt.Errorf("schema drift: expected=%s, actual=%s, H10 修改 schema 需通知 H11/N27 CompactContext 模块",
			expectedSchema, actualSchema)
	}
	return nil
}

// buildCompactPrompt 构造压缩 prompt，喂给小模型生成摘要。
// 参考 MemGPT @arxiv:2310.08560 paging 机制。
// system 指令要求生成 1 条 Markdown 摘要（含关键决策/失败点/改进建议），不包含技术细节（时间戳/ID）。
//
// user 部分列出 trace 的 dispatch/start/end/fail 事件 + quality 的 L0/L1/L2 评分。
// REF-外部-MemGPT@https://arxiv.org/abs/2310.08560+2026-07-16访问-paging；借鉴逻辑：context 压缩为 memory
func buildCompactPrompt(trace []orchrecord.TraceEntry, quality []orchrecord.QualityRecord) string {
	var sb strings.Builder
	sb.WriteString("你是上下文压缩 AI，将长会话 trace+quality 压缩为可检索摘要。\n")
	sb.WriteString("要求：\n")
	sb.WriteString("1. 生成 1 条 Markdown 摘要\n")
	sb.WriteString("2. 摘要应包含：关键决策、失败点、改进建议\n")
	sb.WriteString("3. 不要包含时间戳、ID 等技术细节，聚焦操作经验\n")
	sb.WriteString("4. 摘要长度建议 200-500 字\n\n")
	sb.WriteString("===\ntrace 事件流：\n")

	if len(trace) == 0 {
		sb.WriteString("(trace 为空)\n")
	}
	for _, e := range trace {
		sb.WriteString(fmt.Sprintf("- [%s] subtask=%s depth=%d agent=%s stage=%s model=%s",
			e.Event, e.SubTaskID, e.Depth, e.AgentID, e.Stage, e.Model))
		if e.ErrorType != "" {
			sb.WriteString(" error=" + e.ErrorType)
		}
		sb.WriteString("\n")
	}

	sb.WriteString("\n===\nquality 评分：\n")
	if len(quality) == 0 {
		sb.WriteString("(quality 为空)\n")
	}
	for _, q := range quality {
		sb.WriteString(fmt.Sprintf("- task=%s type=%s complexity=%s model=%s L0=%v L1=%d L2=%s",
			q.TaskID, q.TaskType, q.Complexity, q.Model, q.L0Pass, q.L1Score, q.L2Verdict))
		if q.PatternID != "" {
			sb.WriteString(" pattern=" + q.PatternID)
		}
		if q.BudgetExceeded {
			sb.WriteString(" budget_exceeded=true")
		}
		sb.WriteString("\n")
	}

	sb.WriteString("\n===\n请输出 1 条 Markdown 摘要。")
	return sb.String()
}

// linkCompactToRelatedMemories 通过 SearchMemory 检索同主题 stable/procedural 记忆并建双向链接。
// query 用 source.TaskID 作为关键词检索，找到同主题记忆后调用 LinkMemories 建双向链接。
// 失败时仅 warn，不阻断主流程（compact 已写入不应回滚）。
//
// 设计：仅链接 stable/procedural 类型，不链接 ephemeral（避免 ephemeral 之间互相链接形成噪声）。
func linkCompactToRelatedMemories(ctx context.Context, compactName, taskID string) error {
	// 通过 SearchMemory 检索同主题记忆（taskID 作为 query，TopK=5）
	entries, err := SearchMemory(ctx, taskID, SearchOptions{TopK: 5})
	if err != nil {
		return fmt.Errorf("SearchMemory %q: %w", taskID, err)
	}

	linked := 0
	for _, e := range entries {
		// 跳过自身
		if e.Name == compactName {
			continue
		}
		// 仅链接 stable/procedural 类型（避免 ephemeral 互相链接噪声）
		if e.TrustLevel != TrustStable && e.TrustLevel != TrustProcedural {
			continue
		}
		// 建双向链接（失败不阻断整体，继续下一条）
		if err := LinkMemories(compactName, e.Name, "compact related"); err != nil {
			log.Printf("WARN: linkCompactToRelatedMemories: LinkMemories %s↔%s failed: %v",
				compactName, e.Name, err)
			continue
		}
		linked++
	}

	if linked == 0 {
		// 未找到同主题记忆不算错误（首次 compact 时正常）
		return nil
	}
	return nil
}
