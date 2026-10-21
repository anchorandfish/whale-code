// compact_test.go — H11/N27 Phase C C2 CompactContext 测试（含 4 致命/反例）。
// 测试覆盖：
// - TestCompactContext_ReadTrace：mock 一个 trace.jsonl，验证 readTraceJSONL 返回正确 + schema 校验通过
// - TestCompactContext_SchemaDrift_Reject（致命反例）：schema 不匹配返回 error 不写记忆（原子性）
// - TestCompactContext_WriteEphemeral：验证写入 ephemeral + unverified + source=ai_self + importance=50
// - TestCompactContext_QualityPath_NotExist（反例）：quality 不存在时降级 trace-only + warn，仍写入记忆
// - TestCompactContext_AI_Fail_Atomicity（反例）：AI 失败返回 error 不写记忆（原子性）
// - TestCompactContext_LinkMemories：验证自动 LinkMemories 关联同主题 stable 记忆
// - TestReadTraceJSONL_EmbeddedSchema：首行 TraceEntry 内嵌 _schema_version 字段
// - TestReadQualityJSONL_NotExist：quality 不存在返回空切片（降级）
// - TestDetectSchemaDrift_Unit：detectSchemaDrift 单元测试（v1/v2/空 三种情况）
// - TestBuildCompactPrompt_Unit：buildCompactPrompt 单元测试（含 trace + quality）
package memory

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/anchorandfish/whale-code/internal/orchrecord"
)

// setupCompactTest 创建测试用 MergeLayer + 清理 aiCaller + 清理 linkRegistry + 清理 capabilityStore。
// 复用 setupReflectTest（已注入 globalMergeLayer + 清理 aiCaller/linkRegistry/capabilityStore）。
func setupCompactTest(t *testing.T, workDir string) (baseDir string, ml *MergeLayer) {
	t.Helper()
	return setupReflectTest(t, workDir)
}

// writeTraceJSONL 写入测试用 trace.jsonl 文件。
// schemaVersion="v1" → 首行嵌入 _schema_version 字段；schemaVersion="v2" → drift 测试。
// entries 是要写入的 TraceEntry 列表（不含 schema marker）。
// 返回文件路径。
func writeTraceJSONL(t *testing.T, path string, schemaVersion string, entries []orchrecord.TraceEntry) string {
	t.Helper()

	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("MkdirAll %s 失败: %v", filepath.Dir(path), err)
	}

	f, err := os.Create(path)
	if err != nil {
		t.Fatalf("Create %s 失败: %v", path, err)
	}
	defer f.Close()

	// 首行：嵌入 _schema_version 字段的 TraceEntry（或纯 schema marker）
	if schemaVersion != "" {
		// 构造首行：一个空的 TraceEntry + _schema_version 字段
		// 通过 map 序列化保证 _schema_version 字段存在
		firstLine := map[string]interface{}{
			"_schema_version": schemaVersion,
			"timestamp":       time.Now().Unix(),
			"subtask_id":      "schema-marker",
			"event":           "dispatch",
			"agent_id":        "schema-agent",
			"stage":           "init",
			"model":           "marker",
		}
		data, _ := json.Marshal(firstLine)
		if _, err := f.Write(append(data, '\n')); err != nil {
			t.Fatalf("Write schema marker 失败: %v", err)
		}
	}

	// 后续行：真实 TraceEntry
	for _, e := range entries {
		data, err := json.Marshal(e)
		if err != nil {
			t.Fatalf("Marshal TraceEntry 失败: %v", err)
		}
		if _, err := f.Write(append(data, '\n')); err != nil {
			t.Fatalf("Write TraceEntry 失败: %v", err)
		}
	}

	return path
}

// writeQualityJSONL 写入测试用 quality.jsonl 文件。
// schemaVersion="v1" → 首行嵌入 _schema_version 字段。
func writeQualityJSONL(t *testing.T, path string, schemaVersion string, records []orchrecord.QualityRecord) string {
	t.Helper()

	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("MkdirAll %s 失败: %v", filepath.Dir(path), err)
	}

	f, err := os.Create(path)
	if err != nil {
		t.Fatalf("Create %s 失败: %v", path, err)
	}
	defer f.Close()

	if schemaVersion != "" {
		firstLine := map[string]interface{}{
			"_schema_version": schemaVersion,
			"task_id":         "schema-marker",
		}
		data, _ := json.Marshal(firstLine)
		if _, err := f.Write(append(data, '\n')); err != nil {
			t.Fatalf("Write schema marker 失败: %v", err)
		}
	}

	for _, r := range records {
		data, err := json.Marshal(r)
		if err != nil {
			t.Fatalf("Marshal QualityRecord 失败: %v", err)
		}
		if _, err := f.Write(append(data, '\n')); err != nil {
			t.Fatalf("Write QualityRecord 失败: %v", err)
		}
	}

	return path
}

// makeTestTraceEntries 构造测试用 TraceEntry 列表（3 条 dispatch/start/end）。
func makeTestTraceEntries() []orchrecord.TraceEntry {
	now := time.Now().Unix()
	return []orchrecord.TraceEntry{
		{Timestamp: now, SubTaskID: "sub-1", Depth: 1, Event: "dispatch", AgentID: "agent-1", Stage: "plan", Model: "test-model"},
		{Timestamp: now + 1, SubTaskID: "sub-1", Depth: 1, Event: "start", AgentID: "agent-1", Stage: "plan", Model: "test-model"},
		{Timestamp: now + 2, SubTaskID: "sub-1", Depth: 1, Event: "end", AgentID: "agent-1", Stage: "plan", Model: "test-model"},
	}
}

// makeTestQualityRecords 构造测试用 QualityRecord 列表（1 条 adopted）。
func makeTestQualityRecords() []orchrecord.QualityRecord {
	return []orchrecord.QualityRecord{
		{
			TaskID:     "task-test-1",
			TaskType:   orchrecord.TaskTypeImplement,
			Complexity: orchrecord.ComplexityT2,
			Model:      "test-model",
			L0Pass:     true,
			L1Score:    85,
			L2Verdict:  orchrecord.L2Adopted,
			Timestamp:  time.Now().Format(time.RFC3339),
			PatternID:  "pattern-001",
		},
	}
}

// TestCompactContext_ReadTrace 验证 readTraceJSONL 读取 trace.jsonl + schema 版本校验通过。
func TestCompactContext_ReadTrace(t *testing.T) {
	_, _ = setupCompactTest(t, "/test/work/compact-read")

	tmpDir := t.TempDir()
	tracePath := filepath.Join(tmpDir, "trace.jsonl")

	entries := makeTestTraceEntries()
	writeTraceJSONL(t, tracePath, "v1", entries)

	// 调用 readTraceJSONL，校验 schema 通过 + 返回真实 entries（marker 被过滤）
	// 注：写入时首行是 schema marker 行（含 _schema_version 字段），readTraceJSONL 通过
	// filterRealTraceEntries 过滤 marker 行（审查报告优化 6），仅返回 3 条真实 entry
	result, err := readTraceJSONL(tracePath, "v1")
	if err != nil {
		t.Fatalf("readTraceJSONL 失败: %v", err)
	}
	// 3 条真实 entries（marker 已被 filterRealTraceEntries 过滤）
	if len(result) != len(entries) {
		t.Errorf("readTraceJSONL 返回 %d 条, 期望 %d (marker 已过滤)", len(result), len(entries))
	}

	// 验证 marker 行已被过滤，result[0] 直接是真实 entry（subtask_id="sub-1"）
	if len(result) > 0 && result[0].SubTaskID != "sub-1" {
		t.Errorf("result[0].SubTaskID = %q, 期望 sub-1 (marker 应被过滤)", result[0].SubTaskID)
	}
}

// TestCompactContext_SchemaDrift_Reject 致命反例（原子性）：
// mock schema 不匹配（H10 修改 schema v1→v2），验证 CompactContext 返回 error 且不写任何 ephemeral 记忆。
// 关键：aiCaller 应未被调用（drift 在 AI 调用之前拒绝）。
func TestCompactContext_SchemaDrift_Reject(t *testing.T) {
	_, ml := setupCompactTest(t, "/test/work/compact-drift")

	tmpDir := t.TempDir()
	tracePath := filepath.Join(tmpDir, "trace.jsonl")

	// 写入 schemaVersion="v2" 的 trace 文件（模拟 H10 升级 schema）
	writeTraceJSONL(t, tracePath, "v2", makeTestTraceEntries())

	// 注入 aiCaller（drift 应在 AI 调用前拒绝，aiCaller 不应被调用）
	aiCalled := false
	SetAICaller(func(ctx context.Context, model, prompt string) (string, error) {
		aiCalled = true
		return "should not be called", nil
	})

	ctx := context.Background()
	source := CompactSource{
		TaskID:        "task-drift-test",
		TracePath:     tracePath,
		QualityPath:   "", // 不指定 quality，避免 quality 不存在影响测试
		SchemaVersion: "", // 默认 v1，与文件 v2 不匹配
	}
	newNames, err := CompactContext(ctx, source)
	if err == nil {
		t.Fatal("致命反例失效: schema drift 应返回 error, 实际 nil")
	}
	if !strings.Contains(err.Error(), "schema drift") {
		t.Errorf("error 应含 'schema drift': %v", err)
	}
	if newNames != nil {
		t.Errorf("致命反例失效: schema drift 时 newNames 应为 nil, 实际 %v", newNames)
	}
	if aiCalled {
		t.Error("致命反例失效: schema drift 时不应调用 aiCaller（drift 应在 AI 调用前拒绝）")
	}

	// 验证无 ephemeral 记忆写入（原子性）
	list, err := ml.projectStore.List()
	if err != nil {
		t.Fatalf("List 失败: %v", err)
	}
	for _, e := range list {
		if e.TrustLevel == TrustEphemeral && strings.HasPrefix(e.Name, "compact-") {
			t.Errorf("致命反例失效: schema drift 后不应写入 compact 记忆, 发现 %q", e.Name)
		}
	}
}

// TestCompactContext_WriteEphemeral 验证写入 ephemeral 记忆字段正确。
// 对齐 v0.4 §7.6：trust_level=ephemeral, verification=unverified, source=ai_self, importance=50。
func TestCompactContext_WriteEphemeral(t *testing.T) {
	_, ml := setupCompactTest(t, "/test/work/compact-write")

	tmpDir := t.TempDir()
	tracePath := filepath.Join(tmpDir, "trace.jsonl")
	writeTraceJSONL(t, tracePath, "v1", makeTestTraceEntries())

	// mock aiCaller 返回摘要文本
	SetAICaller(func(ctx context.Context, model, prompt string) (string, error) {
		return "# Task Compaction Summary\n\nKey decisions: ...\nFailure points: ...\nImprovements: ...", nil
	})

	ctx := context.Background()
	source := CompactSource{
		TaskID:        "task-write-test",
		TracePath:     tracePath,
		QualityPath:   "",
		SchemaVersion: "v1",
	}
	newNames, err := CompactContext(ctx, source)
	if err != nil {
		t.Fatalf("CompactContext 失败: %v", err)
	}
	if len(newNames) != 1 {
		t.Fatalf("CompactContext 返回 %d 个 name, 期望 1", len(newNames))
	}
	compactName := newNames[0]

	// 验证写入的字段正确
	entry, err := ml.projectStore.Read(compactName)
	if err != nil {
		t.Fatalf("Read %q 失败: %v", compactName, err)
	}
	if entry.TrustLevel != TrustEphemeral {
		t.Errorf("TrustLevel = %q, 期望 %q", entry.TrustLevel, TrustEphemeral)
	}
	if entry.Verification != VerificationUnverified {
		t.Errorf("Verification = %q, 期望 %q", entry.Verification, VerificationUnverified)
	}
	if entry.Source != SourceAISelf {
		t.Errorf("Source = %q, 期望 %q", entry.Source, SourceAISelf)
	}
	if entry.Importance != 50 {
		t.Errorf("Importance = %d, 期望 50", entry.Importance)
	}
	if !strings.HasPrefix(entry.Name, "compact-task-write-test-") {
		t.Errorf("Name = %q, 期望前缀 'compact-task-write-test-'", entry.Name)
	}
	if !strings.Contains(entry.Description, "task-write-test") {
		t.Errorf("Description = %q, 期望含 'task-write-test'", entry.Description)
	}
	if entry.Content == "" {
		t.Errorf("Content 不应为空")
	}
	if !strings.Contains(entry.Content, "Task Compaction Summary") {
		t.Errorf("Content 应含 AI 返回的摘要: %q", entry.Content)
	}
}

// TestCompactContext_QualityPath_NotExist 反例（降级策略）：
// quality.jsonl 不存在时，验证降级为 trace-only 压缩 + warn，仍写入记忆。
func TestCompactContext_QualityPath_NotExist(t *testing.T) {
	_, ml := setupCompactTest(t, "/test/work/compact-quality-missing")

	tmpDir := t.TempDir()
	tracePath := filepath.Join(tmpDir, "trace.jsonl")
	writeTraceJSONL(t, tracePath, "v1", makeTestTraceEntries())

	// quality 路径指向不存在的文件
	qualityPath := filepath.Join(tmpDir, "nonexistent", "quality.jsonl")

	SetAICaller(func(ctx context.Context, model, prompt string) (string, error) {
		return "trace-only compaction summary", nil
	})

	ctx := context.Background()
	source := CompactSource{
		TaskID:        "task-quality-missing",
		TracePath:     tracePath,
		QualityPath:   qualityPath,
		SchemaVersion: "v1",
	}
	newNames, err := CompactContext(ctx, source)
	if err != nil {
		t.Fatalf("反例失效: quality 不存在应降级不报错, 实际 error: %v", err)
	}
	if len(newNames) != 1 {
		t.Fatalf("反例失效: 降级后应仍写入 1 条 compact 记忆, 实际 %d", len(newNames))
	}

	// 验证 ephemeral 记忆已写入
	entry, err := ml.projectStore.Read(newNames[0])
	if err != nil {
		t.Fatalf("Read %q 失败: %v", newNames[0], err)
	}
	if entry.TrustLevel != TrustEphemeral {
		t.Errorf("TrustLevel = %q, 期望 %q", entry.TrustLevel, TrustEphemeral)
	}
}

// TestCompactContext_AI_Fail_Atomicity 反例（原子性）：
// mock aiCaller 返回 error，验证 CompactContext 返回 error 且不写记忆。
func TestCompactContext_AI_Fail_Atomicity(t *testing.T) {
	_, ml := setupCompactTest(t, "/test/work/compact-ai-fail")

	tmpDir := t.TempDir()
	tracePath := filepath.Join(tmpDir, "trace.jsonl")
	writeTraceJSONL(t, tracePath, "v1", makeTestTraceEntries())

	SetAICaller(func(ctx context.Context, model, prompt string) (string, error) {
		return "", errors.New("AI model timeout (503)")
	})

	ctx := context.Background()
	source := CompactSource{
		TaskID:        "task-ai-fail",
		TracePath:     tracePath,
		QualityPath:   "",
		SchemaVersion: "v1",
	}
	newNames, err := CompactContext(ctx, source)
	if err == nil {
		t.Fatal("反例失效: AI 失败应返回 error, 实际 nil")
	}
	if !strings.Contains(err.Error(), "503") {
		t.Errorf("error 应含 503: %v", err)
	}
	if newNames != nil {
		t.Errorf("反例失效: AI 失败时 newNames 应为 nil, 实际 %v", newNames)
	}

	// 验证无 ephemeral 记忆写入（原子性）
	list, err := ml.projectStore.List()
	if err != nil {
		t.Fatalf("List 失败: %v", err)
	}
	for _, e := range list {
		if e.TrustLevel == TrustEphemeral && strings.HasPrefix(e.Name, "compact-") {
			t.Errorf("反例失效: AI 失败后不应写入 compact 记忆, 发现 %q", e.Name)
		}
	}
}

// TestCompactContext_LinkMemories 验证写入后自动 LinkMemories 关联同主题 stable 记忆。
// 预写一条 stable 记忆（Content 含 taskID 关键词），验证 compact 写入后双向链接建立。
func TestCompactContext_LinkMemories(t *testing.T) {
	_, ml := setupCompactTest(t, "/test/work/compact-link")

	// 预写一条 stable 记忆（Content 含 "link-feature" 关键词，便于 SearchMemory 检索）
	stableEntry := &MemoryEntry{
		Name:         "stable-design-link-feature",
		Description:  "stable design about link-feature",
		Type:         MemoryTypeProject,
		CreatedAt:    time.Now(),
		Content:      "stable design notes for link-feature implementation",
		TrustLevel:   TrustStable,
		Verification: VerificationVerified,
		Importance:   80,
		Source:       SourceUser,
	}
	if err := ml.projectStore.Write(stableEntry); err != nil {
		t.Fatalf("Write stable entry 失败: %v", err)
	}

	tmpDir := t.TempDir()
	tracePath := filepath.Join(tmpDir, "trace.jsonl")
	writeTraceJSONL(t, tracePath, "v1", makeTestTraceEntries())

	SetAICaller(func(ctx context.Context, model, prompt string) (string, error) {
		return "compaction summary for link-feature task", nil
	})

	ctx := context.Background()
	source := CompactSource{
		TaskID:        "link-feature", // 与 stable 记忆 Content 关键词匹配
		TracePath:     tracePath,
		QualityPath:   "",
		SchemaVersion: "v1",
	}
	newNames, err := CompactContext(ctx, source)
	if err != nil {
		t.Fatalf("CompactContext 失败: %v", err)
	}
	if len(newNames) != 1 {
		t.Fatalf("CompactContext 返回 %d 个 name, 期望 1", len(newNames))
	}
	compactName := newNames[0]

	// 验证 stable 记忆的 Links 含 compact entry name（双向链接建立）
	stableLinks, err := GetLinks("stable-design-link-feature")
	if err != nil {
		t.Fatalf("GetLinks(stable-design-link-feature) 失败: %v", err)
	}
	if !containsString(stableLinks, compactName) {
		t.Errorf("LinkMemories 反例失效: stable.Links = %v, 期望含 %s（双向链接建立）",
			stableLinks, compactName)
	}

	// 验证 compact entry 的 Links 含 stable name（反向链接建立）
	compactLinks, err := GetLinks(compactName)
	if err != nil {
		t.Fatalf("GetLinks(%q) 失败: %v", compactName, err)
	}
	if !containsString(compactLinks, "stable-design-link-feature") {
		t.Errorf("LinkMemories 反例失效: compact.Links = %v, 期望含 stable-design-link-feature（双向链接建立）",
			compactLinks)
	}
}

// TestReadTraceJSONL_EmbeddedSchema 验证首行 TraceEntry 内嵌 _schema_version 字段的读取。
// 首行是合法 TraceEntry + _schema_version="v1"，readSchemaVersion 应提取 "v1"。
func TestReadTraceJSONL_EmbeddedSchema(t *testing.T) {
	tmpDir := t.TempDir()
	tracePath := filepath.Join(tmpDir, "trace.jsonl")

	// 写入 trace.jsonl：首行是真实 TraceEntry + _schema_version 字段
	f, err := os.Create(tracePath)
	if err != nil {
		t.Fatalf("Create 失败: %v", err)
	}
	// 首行：TraceEntry + _schema_version
	firstLine := orchrecord.TraceEntry{
		Timestamp: time.Now().Unix(),
		SubTaskID: "sub-embedded",
		Depth:     1,
		Event:     "dispatch",
		AgentID:   "agent-1",
		Stage:     "plan",
		Model:     "test",
	}
	data, _ := json.Marshal(firstLine)
	// 在 JSON 末尾添加 _schema_version 字段（hack：手工拼接）
	lineStr := string(data)
	lineStr = strings.TrimSuffix(lineStr, "}")
	lineStr += ",\"_schema_version\":\"v1\"}"
	if _, err := f.WriteString(lineStr + "\n"); err != nil {
		t.Fatalf("Write first line 失败: %v", err)
	}
	// 第 2 行：真实 TraceEntry（无 schema_version）
	secondEntry := orchrecord.TraceEntry{
		Timestamp: time.Now().Unix(),
		SubTaskID: "sub-2",
		Depth:     1,
		Event:     "end",
		AgentID:   "agent-1",
		Stage:     "plan",
		Model:     "test",
	}
	data2, _ := json.Marshal(secondEntry)
	if _, err := f.Write(append(data2, '\n')); err != nil {
		t.Fatalf("Write second line 失败: %v", err)
	}
	f.Close()

	// 验证 readSchemaVersion 提取 "v1"
	schema, err := readSchemaVersion(tracePath)
	if err != nil {
		t.Fatalf("readSchemaVersion 失败: %v", err)
	}
	if schema != "v1" {
		t.Errorf("readSchemaVersion = %q, 期望 v1 (从嵌入字段提取)", schema)
	}

	// 验证 readTraceJSONL 通过 drift 校验 + 返回 2 条 entries
	result, err := readTraceJSONL(tracePath, "v1")
	if err != nil {
		t.Fatalf("readTraceJSONL 失败: %v", err)
	}
	if len(result) != 2 {
		t.Errorf("readTraceJSONL 返回 %d 条, 期望 2", len(result))
	}
}

// TestReadQualityJSONL_NotExist 验证 quality 文件不存在时降级返回空切片。
func TestReadQualityJSONL_NotExist(t *testing.T) {
	// 不存在的路径
	qualityPath := "/nonexistent/path/quality.jsonl"

	records, err := readQualityJSONL(qualityPath, "v1")
	if err != nil {
		t.Fatalf("反例失效: quality 不存在应降级返回 nil, 实际 error: %v", err)
	}
	if records != nil {
		t.Errorf("反例失效: quality 不存在应返回 nil 切片, 实际 %d 条", len(records))
	}
}

// TestReadQualityJSONL_EmptyPath 验证 quality 路径为空时降级返回空切片。
func TestReadQualityJSONL_EmptyPath(t *testing.T) {
	records, err := readQualityJSONL("", "v1")
	if err != nil {
		t.Fatalf("反例失效: 空路径应降级返回 nil, 实际 error: %v", err)
	}
	if records != nil {
		t.Errorf("反例失效: 空路径应返回 nil 切片, 实际 %d 条", len(records))
	}
}

// TestDetectSchemaDrift_Unit detectSchemaDrift 单元测试。
// 覆盖：v1==v1 通过 / v2!=v1 拒绝 / 空默认 v1 通过 / 空默认 v2 拒绝。
func TestDetectSchemaDrift_Unit(t *testing.T) {
	tests := []struct {
		name           string
		actualSchema   string
		expectedSchema string
		wantErr        bool
		errContains    string
	}{
		{"v1_matches_v1", "v1", "v1", false, ""},
		{"v2_drift_v1", "v2", "v1", true, "schema drift"},
		{"empty_actual_defaults_to_v1_matches_v1", "", "v1", false, ""},
		{"empty_actual_defaults_to_v1_drift_v2", "", "v2", true, "schema drift"},
		{"empty_expected_defaults_to_v1_matches_v1", "v1", "", false, ""},
		{"empty_both_defaults_to_v1_matches", "", "", false, ""},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := detectSchemaDrift(tt.actualSchema, tt.expectedSchema)
			if tt.wantErr {
				if err == nil {
					t.Errorf("detectSchemaDrift(%q, %q) 应返回 error, 实际 nil",
						tt.actualSchema, tt.expectedSchema)
				} else if tt.errContains != "" && !strings.Contains(err.Error(), tt.errContains) {
					t.Errorf("detectSchemaDrift(%q, %q) error 应含 %q, 实际: %v",
						tt.actualSchema, tt.expectedSchema, tt.errContains, err)
				}
			} else {
				if err != nil {
					t.Errorf("detectSchemaDrift(%q, %q) 不应返回 error, 实际: %v",
						tt.actualSchema, tt.expectedSchema, err)
				}
			}
		})
	}
}

// TestBuildCompactPrompt_Unit buildCompactPrompt 单元测试。
// 验证 prompt 含 trace 事件 + quality 评分 + 系统指令。
func TestBuildCompactPrompt_Unit(t *testing.T) {
	trace := []orchrecord.TraceEntry{
		{Timestamp: 1, SubTaskID: "sub-1", Depth: 1, Event: "dispatch", AgentID: "a-1", Stage: "plan", Model: "m-1"},
		{Timestamp: 2, SubTaskID: "sub-1", Depth: 1, Event: "fail", AgentID: "a-1", Stage: "plan", Model: "m-1", ErrorType: "timeout"},
	}
	quality := []orchrecord.QualityRecord{
		{TaskID: "task-1", TaskType: "implement", Complexity: "T2", Model: "m-1",
			L0Pass: false, L1Score: 60, L2Verdict: "modified", PatternID: "p-1", BudgetExceeded: true},
	}

	prompt := buildCompactPrompt(trace, quality)

	// 验证系统指令
	if !strings.Contains(prompt, "上下文压缩 AI") {
		t.Errorf("prompt 应含系统指令 '上下文压缩 AI', 实际:\n%s", prompt)
	}

	// 验证 trace 事件
	if !strings.Contains(prompt, "dispatch") {
		t.Errorf("prompt 应含 trace 事件 'dispatch'")
	}
	if !strings.Contains(prompt, "fail") {
		t.Errorf("prompt 应含 trace 事件 'fail'")
	}
	if !strings.Contains(prompt, "timeout") {
		t.Errorf("prompt 应含 trace ErrorType 'timeout'")
	}

	// 验证 quality 评分
	if !strings.Contains(prompt, "task-1") {
		t.Errorf("prompt 应含 quality TaskID 'task-1'")
	}
	if !strings.Contains(prompt, "modified") {
		t.Errorf("prompt 应含 quality L2Verdict 'modified'")
	}
	if !strings.Contains(prompt, "pattern=p-1") {
		t.Errorf("prompt 应含 quality PatternID 'p-1'")
	}
	if !strings.Contains(prompt, "budget_exceeded=true") {
		t.Errorf("prompt 应含 budget_exceeded=true 标记")
	}

	// 验证空输入处理
	emptyPrompt := buildCompactPrompt(nil, nil)
	if !strings.Contains(emptyPrompt, "(trace 为空)") {
		t.Errorf("空 trace 时 prompt 应含 '(trace 为空)'")
	}
	if !strings.Contains(emptyPrompt, "(quality 为空)") {
		t.Errorf("空 quality 时 prompt 应含 '(quality 为空)'")
	}
}

// TestCompactContext_NoAICaller 注入校验：aiCaller 未注入时返回 error。
func TestCompactContext_NoAICaller(t *testing.T) {
	_, _ = setupCompactTest(t, "/test/work/compact-no-ai")
	// 不注入 aiCaller

	ctx := context.Background()
	source := CompactSource{
		TaskID:    "task-no-ai",
		TracePath: "/tmp/trace.jsonl",
	}
	_, err := CompactContext(ctx, source)
	if err == nil {
		t.Fatal("aiCaller 未注入时应返回 error, 实际 nil")
	}
	if !strings.Contains(err.Error(), "aiCaller") {
		t.Errorf("error 应含 aiCaller: %v", err)
	}
}

// TestCompactContext_EmptyTaskID TaskID 必填校验。
func TestCompactContext_EmptyTaskID(t *testing.T) {
	_, _ = setupCompactTest(t, "/test/work/compact-empty-task")
	SetAICaller(func(ctx context.Context, model, prompt string) (string, error) {
		return "should not be called", nil
	})

	ctx := context.Background()
	source := CompactSource{
		TaskID:    "",
		TracePath: "/tmp/trace.jsonl",
	}
	_, err := CompactContext(ctx, source)
	if err == nil {
		t.Fatal("TaskID 为空时应返回 error, 实际 nil")
	}
	if !strings.Contains(err.Error(), "TaskID") {
		t.Errorf("error 应含 TaskID: %v", err)
	}
}

// TestCompactContext_EmptyTracePath TracePath 必填校验。
func TestCompactContext_EmptyTracePath(t *testing.T) {
	_, _ = setupCompactTest(t, "/test/work/compact-empty-trace")
	SetAICaller(func(ctx context.Context, model, prompt string) (string, error) {
		return "should not be called", nil
	})

	ctx := context.Background()
	source := CompactSource{
		TaskID:    "task-empty-trace",
		TracePath: "",
	}
	_, err := CompactContext(ctx, source)
	if err == nil {
		t.Fatal("TracePath 为空时应返回 error, 实际 nil")
	}
	if !strings.Contains(err.Error(), "TracePath") {
		t.Errorf("error 应含 TracePath: %v", err)
	}
}
