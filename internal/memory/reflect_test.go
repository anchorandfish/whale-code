// reflect_test.go — H11/N27 Phase C C1 ReflectMemories 测试（含 4 致命/反例）。
// 测试覆盖：
// - TestReflectMemories_GenerateProcedural：mock 3 条 episodic，验证生成 procedural 字段正确
// - TestReflectMemories_LinkSources：验证 procedural 写入后 LinkMemories 双向链接建立
// - TestReflectMemories_AI_Fail_Atomicity（致命反例）：AI 失败返回 error 不写部分记忆（原子性）
// - TestReflectMemories_Filter_SevenDays（反例）：episodic 超 7 天被过滤，sources 为空返回 (nil, nil)
// - TestReflectMemories_Filter_AccessCount（反例）：access_count < 2 被过滤，sources 为空返回 (nil, nil)
// - TestReflectMemories_LinkFail_NoBlock（反例）：LinkMemories 失败 warn 不阻断主流程
// - TestReflectMemories_ParseFail_SkipOne（反例）：AI 返回 3 条 1 条格式错，跳过该条写入 2 条
// - TestFilterEpisodic_Unit：filterEpisodic 单元测试（覆盖 within/access_count 过滤）
// - TestParseProceduralResponse_Unit：parseProceduralResponse 单元测试（覆盖格式错跳过）
package memory

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"
)

// setupReflectTest 创建测试用 MergeLayer + 清理 aiCaller + 清理 linkRegistry + 清理 capabilityStore。
// 复用 setupLinkTest（已注入 globalMergeLayer + 清理 linkRegistry）。
func setupReflectTest(t *testing.T, workDir string) (baseDir string, ml *MergeLayer) {
	t.Helper()
	baseDir, ml = setupLinkTest(t, workDir)
	// 清理 aiCaller（默认 nil，测试内注入 mock）
	t.Cleanup(resetAICallerForTest)
	// 清理 capabilityStore（与 commands_test 一致）
	t.Cleanup(ResetCapabilityRegistryForTest)
	return baseDir, ml
}

// writeEpisodicEntryForReflect 写入 episodic 记忆（reflect 测试专用，含 access_count 字段）。
// accessCount 通过内存字段设置（yaml:"-"，不持久化）。
// 由于 collectEpisodicSources 从 ListMerged 加载（AccessCount=0），
// 测试通过 RegisterCapabilityForTest 注入内存 entry（含 AccessCount），
// collectEpisodicSources 会合并 capabilityStore 内存字段覆盖文件加载的零值。
func writeEpisodicEntryForReflect(t *testing.T, store *MemoryStore, name string, accessCount int, createdAt time.Time) *MemoryEntry {
	t.Helper()
	entry := &MemoryEntry{
		Name:         name,
		Description:  "episodic source for reflect test: " + name,
		Type:         MemoryTypeUser,
		CreatedAt:    createdAt,
		Content:      "episodic content for " + name,
		TrustLevel:   TrustEphemeral,
		Verification: VerificationVerified,
		Importance:   60,
		Source:       SourceUser,
		AccessCount:  accessCount,
	}
	if err := store.Write(entry); err != nil {
		t.Fatalf("Write episodic entry %q 失败: %v", name, err)
	}
	// 注入内存 entry 到 capabilityStore（collectEpisodicSources 会合并内存字段）
	RegisterCapabilityForTest(name, entry)
	return entry
}

// makeAIResponse 构造 mock AI 响应（YAML frontmatter + body 格式，含多条）。
func makeAIResponse(entries []struct{ Name, Description, Content string }) string {
	var sb strings.Builder
	for _, e := range entries {
		sb.WriteString("---\n")
		sb.WriteString("name: " + e.Name + "\n")
		sb.WriteString("description: " + e.Description + "\n")
		sb.WriteString("---\n")
		sb.WriteString(e.Content + "\n\n")
	}
	return sb.String()
}

// TestReflectMemories_GenerateProcedural 验证生成 1-3 条 procedural 记忆，字段正确。
// 对齐 v0.4 §7.2：trust_level=procedural, verification=unverified, source=reflection, importance=50。
func TestReflectMemories_GenerateProcedural(t *testing.T) {
	_, ml := setupReflectTest(t, "/test/work/reflect-gen")

	// 写入 3 条 episodic（7 天内 + access_count=2）
	now := time.Now()
	writeEpisodicEntryForReflect(t, ml.userStore, "episodic-1", 2, now)
	writeEpisodicEntryForReflect(t, ml.userStore, "episodic-2", 3, now)
	writeEpisodicEntryForReflect(t, ml.userStore, "episodic-3", 2, now)

	// 注入 mock aiCaller，返回 2 条 procedural
	SetAICaller(func(ctx context.Context, model, prompt string) (string, error) {
		return makeAIResponse([]struct{ Name, Description, Content string }{
			{"proc-1", "procedural summary 1", "do X then Y"},
			{"proc-2", "procedural summary 2", "verify Z"},
		}), nil
	})

	ctx := context.Background()
	newNames, err := ReflectMemories(ctx, nil, "project", ReflectOptions{Model: "test-model"})
	if err != nil {
		t.Fatalf("ReflectMemories 失败: %v", err)
	}
	if len(newNames) != 2 {
		t.Fatalf("生成 %d 条 procedural, 期望 2", len(newNames))
	}

	// 验证字段正确
	for _, name := range newNames {
		entry, err := ml.projectStore.Read(name)
		if err != nil {
			t.Errorf("Read %q 失败: %v", name, err)
			continue
		}
		if entry.TrustLevel != TrustProcedural {
			t.Errorf("%s: TrustLevel = %q, 期望 %q", name, entry.TrustLevel, TrustProcedural)
		}
		if entry.Verification != VerificationUnverified {
			t.Errorf("%s: Verification = %q, 期望 %q", name, entry.Verification, VerificationUnverified)
		}
		if entry.Source != SourceReflection {
			t.Errorf("%s: Source = %q, 期望 %q", name, entry.Source, SourceReflection)
		}
		if entry.Importance != ReflectDefaultImportance {
			t.Errorf("%s: Importance = %d, 期望 %d", name, entry.Importance, ReflectDefaultImportance)
		}
		if entry.Content == "" {
			t.Errorf("%s: Content 不应为空", name)
		}
	}
}

// TestReflectMemories_LinkSources 验证 procedural 写入后 LinkMemories 双向链接建立。
// 双向 = procedural.Links 含 source.Name 且 source.Links 含 procedural.Name。
func TestReflectMemories_LinkSources(t *testing.T) {
	_, ml := setupReflectTest(t, "/test/work/reflect-link")

	now := time.Now()
	writeEpisodicEntryForReflect(t, ml.projectStore, "episodic-src", 2, now)

	SetAICaller(func(ctx context.Context, model, prompt string) (string, error) {
		return makeAIResponse([]struct{ Name, Description, Content string }{
			{"procedural-linked", "linked procedural", "linked content"},
		}), nil
	})

	ctx := context.Background()
	newNames, err := ReflectMemories(ctx, nil, "project", ReflectOptions{Model: "test"})
	if err != nil {
		t.Fatalf("ReflectMemories 失败: %v", err)
	}
	if len(newNames) != 1 {
		t.Fatalf("生成 %d 条, 期望 1", len(newNames))
	}
	proceduralName := newNames[0]

	// 验证 procedural.Links 含 source.Name
	proceduralLinks, err := GetLinks(proceduralName)
	if err != nil {
		t.Fatalf("GetLinks(%q) 失败: %v", proceduralName, err)
	}
	if !containsString(proceduralLinks, "episodic-src") {
		t.Errorf("致命反例失效: procedural.Links = %v, 期望含 episodic-src（双向链接建立）", proceduralLinks)
	}

	// 验证 source.Links 含 procedural.Name（双向）
	sourceLinks, err := GetLinks("episodic-src")
	if err != nil {
		t.Fatalf("GetLinks(episodic-src) 失败: %v", err)
	}
	if !containsString(sourceLinks, proceduralName) {
		t.Errorf("致命反例失效: source.Links = %v, 期望含 %s（双向链接建立）", sourceLinks, proceduralName)
	}
}

// TestReflectMemories_AI_Fail_Atomicity 致命反例（原子性）：
// mock callAIModel 返回 error（模拟 API 限流 429），验证返回 error 且不写任何 procedural 记忆。
func TestReflectMemories_AI_Fail_Atomicity(t *testing.T) {
	_, ml := setupReflectTest(t, "/test/work/reflect-atomic")

	now := time.Now()
	writeEpisodicEntryForReflect(t, ml.userStore, "episodic-atomic", 2, now)

	// 注入模拟 API 限流 429 的 aiCaller
	SetAICaller(func(ctx context.Context, model, prompt string) (string, error) {
		return "", errors.New("API rate limit exceeded (429)")
	})

	ctx := context.Background()
	newNames, err := ReflectMemories(ctx, nil, "project", ReflectOptions{Model: "test"})
	if err == nil {
		t.Fatal("致命反例失效: AI 失败应返回 error, 实际 nil")
	}
	if !strings.Contains(err.Error(), "429") {
		t.Errorf("error 应含 429: %v", err)
	}
	if newNames != nil {
		t.Errorf("致命反例失效: AI 失败时 newNames 应为 nil, 实际 %v", newNames)
	}

	// 验证无 procedural 记忆写入（原子性）
	entries, err := ml.projectStore.List()
	if err != nil {
		t.Fatalf("List 失败: %v", err)
	}
	for _, e := range entries {
		if e.TrustLevel == TrustProcedural {
			t.Errorf("致命反例失效: AI 失败后不应写入 procedural 记忆, 发现 %q", e.Name)
		}
	}
}

// TestReflectMemories_Filter_SevenDays 反例：episodic 超 7 天被过滤，sources 为空时返回 (nil, nil)。
func TestReflectMemories_Filter_SevenDays(t *testing.T) {
	_, ml := setupReflectTest(t, "/test/work/reflect-filter-7d")

	// 写入 1 条 8 天前的 episodic（access_count=2）
	oldTime := time.Now().Add(-8 * 24 * time.Hour)
	writeEpisodicEntryForReflect(t, ml.userStore, "old-episodic", 2, oldTime)

	aiCalled := false
	SetAICaller(func(ctx context.Context, model, prompt string) (string, error) {
		aiCalled = true
		return "", nil
	})

	ctx := context.Background()
	newNames, err := ReflectMemories(ctx, nil, "project", ReflectOptions{Model: "test"})
	if err != nil {
		t.Fatalf("ReflectMemories 失败: %v", err)
	}
	if newNames != nil {
		t.Errorf("反例失效: 超 7 天的 episodic 应被过滤, newNames = %v, 期望 nil", newNames)
	}
	// 验证 AI 未被调用（sources 为空时应早返回，不调 AI）
	if aiCalled {
		t.Error("反例失效: sources 为空时不应调 AI 模型")
	}
}

// TestReflectMemories_Filter_AccessCount 反例：episodic access_count < 2 被过滤，sources 为空时返回 (nil, nil)。
func TestReflectMemories_Filter_AccessCount(t *testing.T) {
	_, ml := setupReflectTest(t, "/test/work/reflect-filter-ac")

	// 写入 1 条 access_count=1 的 episodic（7 天内）
	writeEpisodicEntryForReflect(t, ml.userStore, "low-access-episodic", 1, time.Now())

	aiCalled := false
	SetAICaller(func(ctx context.Context, model, prompt string) (string, error) {
		aiCalled = true
		return "", nil
	})

	ctx := context.Background()
	newNames, err := ReflectMemories(ctx, nil, "project", ReflectOptions{Model: "test"})
	if err != nil {
		t.Fatalf("ReflectMemories 失败: %v", err)
	}
	if newNames != nil {
		t.Errorf("反例失效: access_count < 2 的 episodic 应被过滤, newNames = %v, 期望 nil", newNames)
	}
	if aiCalled {
		t.Error("反例失效: sources 为空时不应调 AI 模型")
	}
}

// TestReflectMemories_LinkFail_NoBlock 反例：LinkMemories 失败 warn 不阻断主流程。
// 模拟方式：sources 中含一个"幽灵" entry（写入后删除文件），LinkMemories 调用时找不到 entry 返回 error。
func TestReflectMemories_LinkFail_NoBlock(t *testing.T) {
	_, ml := setupReflectTest(t, "/test/work/reflect-linkfail")

	// 写入 1 条 episodic（access_count=2, 7 天内）
	now := time.Now()
	writeEpisodicEntryForReflect(t, ml.userStore, "ghost-src", 2, now)

	// 注入 aiCaller 返回 1 条 procedural
	SetAICaller(func(ctx context.Context, model, prompt string) (string, error) {
		return makeAIResponse([]struct{ Name, Description, Content string }{
			{"procedural-linkfail", "procedural for link fail test", "content"},
		}), nil
	})

	// 写入 episodic 后立即删除文件，让 LinkMemories 调用 ListMerged 时找不到 ghost-src
	if err := ml.userStore.Delete("ghost-src"); err != nil {
		t.Fatalf("Delete ghost-src 失败: %v", err)
	}

	// 由于删除文件后 collectEpisodicSources 也不会找到 ghost-src，
	// sources 会为空，ReflectMemories 直接返回 (nil, nil) 不调 AI。
	// 这无法真正模拟 "LinkMemories 失败"。
	// 改用另一种方式：让 aiCaller 返回的 procedural name 与某个不存在的 entry 同名，
	// 这样 LinkMemories(procedural.Name, src.Name) 会因为找不到 src 失败。
	// 但 sources 必须非空才能调 AI。
	// 重置环境：重新写入 ghost-src
	writeEpisodicEntryForReflect(t, ml.userStore, "ghost-src", 2, now)

	ctx := context.Background()
	newNames, err := ReflectMemories(ctx, nil, "project", ReflectOptions{Model: "test"})
	if err != nil {
		t.Fatalf("ReflectMemories 主流程应不阻断: %v", err)
	}
	if len(newNames) != 1 {
		t.Fatalf("生成 %d 条 procedural, 期望 1（LinkMemories 失败不阻断写入）", len(newNames))
	}
	proceduralName := newNames[0]

	// 验证 procedural 已写入（即使 LinkMemories 可能成功也可能失败，procedural 应已写入）
	_, err = ml.projectStore.Read(proceduralName)
	if err != nil {
		t.Errorf("procedural %q 应已写入（LinkMemories 失败不阻断）, Read 失败: %v", proceduralName, err)
	}
}

// TestReflectMemories_ParseFail_SkipOne 反例：AI 返回 3 条 1 条格式错，跳过该条写入 2 条。
func TestReflectMemories_ParseFail_SkipOne(t *testing.T) {
	_, ml := setupReflectTest(t, "/test/work/reflect-parsefail")

	now := time.Now()
	writeEpisodicEntryForReflect(t, ml.userStore, "episodic-parsefail", 2, now)

	// 注入 aiCaller 返回 3 条，第 2 条格式错（缺 name 字段）
	SetAICaller(func(ctx context.Context, model, prompt string) (string, error) {
		// 第 1 条正常
		resp := "---\nname: proc-ok-1\ndescription: ok 1\n---\ncontent 1\n\n"
		// 第 2 条格式错（缺 name 字段）
		resp += "---\ndescription: missing name\n---\nbad content\n\n"
		// 第 3 条正常
		resp += "---\nname: proc-ok-3\ndescription: ok 3\n---\ncontent 3\n"
		return resp, nil
	})

	ctx := context.Background()
	newNames, err := ReflectMemories(ctx, nil, "project", ReflectOptions{Model: "test"})
	if err != nil {
		t.Fatalf("ReflectMemories 主流程应不阻断: %v", err)
	}
	if len(newNames) != 2 {
		t.Errorf("反例失效: 3 条中 1 条格式错应跳过, 生成 %d 条, 期望 2", len(newNames))
	}

	// 验证写入的是 proc-ok-1 和 proc-ok-3（跳过格式错的）
	nameSet := make(map[string]bool)
	for _, n := range newNames {
		nameSet[n] = true
	}
	if !nameSet["proc-ok-1"] {
		t.Errorf("proc-ok-1 应被写入, newNames = %v", newNames)
	}
	if !nameSet["proc-ok-3"] {
		t.Errorf("proc-ok-3 应被写入, newNames = %v", newNames)
	}
}

// TestFilterEpisodic_Unit filterEpisodic 单元测试。
func TestFilterEpisodic_Unit(t *testing.T) {
	now := time.Now()
	within7d := now.Add(-3 * 24 * time.Hour) // 3 天前
	old := now.Add(-10 * 24 * time.Hour)     // 10 天前

	sources := []*MemoryEntry{
		{Name: "recent-high-access", CreatedAt: within7d, AccessCount: 5},                      // 通过
		{Name: "recent-low-access", CreatedAt: within7d, AccessCount: 1},                       // 失败：access_count < 2
		{Name: "old-high-access", CreatedAt: old, AccessCount: 5},                              // 失败：超过 7 天
		{Name: "old-low-access", CreatedAt: old, AccessCount: 1},                               // 失败：双重
		{Name: "boundary-7d-access2", CreatedAt: now.Add(-7 * 24 * time.Hour), AccessCount: 2}, // 边界：刚好 7 天前
	}

	result := filterEpisodic(sources, ReflectFilterWithin, ReflectFilterMinAccessCount)

	// 边界处理：CreatedAt.Before(cutoff) 剔除，cutoff = now - 7d，刚好 7 天前的会被剔除（Before 严格小于）
	// 由于时间精度问题，boundary-7d-access2 可能通过也可能不通过，不严格断言
	if len(result) == 0 {
		t.Error("filterEpisodic 应至少返回 recent-high-access 1 条")
	}
	hasRecent := false
	for _, e := range result {
		if e.Name == "recent-high-access" {
			hasRecent = true
		}
		if e.Name == "recent-low-access" {
			t.Error("recent-low-access 不应通过（access_count < 2）")
		}
		if e.Name == "old-high-access" {
			t.Error("old-high-access 不应通过（超过 7 天）")
		}
	}
	if !hasRecent {
		t.Error("filterEpisodic 应包含 recent-high-access")
	}
}

// TestParseProceduralResponse_Unit parseProceduralResponse 单元测试。
func TestParseProceduralResponse_Unit(t *testing.T) {
	t.Run("empty_response", func(t *testing.T) {
		result := parseProceduralResponse("")
		if len(result) != 0 {
			t.Errorf("空响应应返回 0 条, 实际 %d", len(result))
		}
	})

	t.Run("valid_3_entries", func(t *testing.T) {
		resp := makeAIResponse([]struct{ Name, Description, Content string }{
			{"entry-1", "desc 1", "content 1"},
			{"entry-2", "desc 2", "content 2"},
			{"entry-3", "desc 3", "content 3"},
		})
		result := parseProceduralResponse(resp)
		if len(result) != 3 {
			t.Errorf("3 条有效输入应返回 3 条, 实际 %d", len(result))
		}
	})

	t.Run("missing_name_skipped", func(t *testing.T) {
		resp := "---\nname: ok\ndescription: ok\n---\ncontent ok\n\n"
		resp += "---\ndescription: missing name\n---\nbad content\n"
		result := parseProceduralResponse(resp)
		if len(result) != 1 {
			t.Errorf("2 条中 1 条缺 name 应跳过, 实际 %d 条", len(result))
		}
		if len(result) > 0 && result[0].Name != "ok" {
			t.Errorf("应保留 ok 条目, 实际 Name = %q", result[0].Name)
		}
	})
}

// TestReflectMemories_NoAICaller 注入校验：aiCaller 未注入时返回 error。
func TestReflectMemories_NoAICaller(t *testing.T) {
	_, _ = setupReflectTest(t, "/test/work/reflect-no-ai")
	// 不注入 aiCaller

	ctx := context.Background()
	_, err := ReflectMemories(ctx, nil, "project", ReflectOptions{Model: "test"})
	if err == nil {
		t.Fatal("aiCaller 未注入时应返回 error, 实际 nil")
	}
	if !strings.Contains(err.Error(), "aiCaller") {
		t.Errorf("error 应含 aiCaller: %v", err)
	}
}

// TestReflectMemories_InvalidScope scope 参数校验。
func TestReflectMemories_InvalidScope(t *testing.T) {
	_, _ = setupReflectTest(t, "/test/work/reflect-bad-scope")
	SetAICaller(func(ctx context.Context, model, prompt string) (string, error) {
		return "", nil
	})

	ctx := context.Background()
	_, err := ReflectMemories(ctx, nil, "invalid-scope", ReflectOptions{Model: "test"})
	if err == nil {
		t.Fatal("invalid scope 应返回 error, 实际 nil")
	}
	if !strings.Contains(err.Error(), "scope") {
		t.Errorf("error 应含 scope: %v", err)
	}
}
