// pattern_extractor_test.go — H12 模式提取器反例测试（TDD 先行，§2.1）。
// 覆盖：
// - 命中已有 Pattern：更新 LastUsedAt
// - 未命中：从 trace 提取新 Pattern（VerifiedState=unverified, Scope=project, Version=1）
// - trace 损坏：warn 不阻断，返回 nil
// - ExperienceStore.Save 失败：warn 不阻断，返回 nil
//
// 设计说明 §2.1；落地：命中/未命中/降级
// SNAPSHOT: tests={Extract_NewPattern_FromTrace, Extract_Hit_UpdateLastUsedAt, Extract_EmptyTrace_NoOp, Extract_EmptyTaskID_NoOp, Extract_NilExperience_Degrade, Extract_CtxCancelled_ReturnsErr, TraceToSteps_BasicMapping, ExtractFilesFromTrace_Heuristic}
package orchestrator

import (
	"context"
	"testing"
	"time"
)

// makeTraceEntries 构造测试用 TraceEntry 列表。
func makeTraceEntries() []TraceEntry {
	return []TraceEntry{
		{
			Timestamp: time.Now().Unix(),
			SubTaskID: "sub-1",
			ParentID:  "parent",
			Depth:     1,
			Event:     TraceEventStart,
			AgentID:   "agent-A",
			Stage:     "edit main.go",
			Model:     ModelSonnet,
		},
		{
			Timestamp: time.Now().Unix(),
			SubTaskID: "sub-2",
			ParentID:  "parent",
			Depth:     1,
			Event:     TraceEventEnd,
			AgentID:   "agent-B",
			Stage:     "verify main.go",
			Model:     ModelSonnet,
		},
	}
}

// TestPatternExtractor_Extract_NewPattern_FromTrace 未命中：从 trace 提取新 Pattern（§2.1）。
func TestPatternExtractor_Extract_NewPattern_FromTrace(t *testing.T) {
	s := newTestExperienceStore(t)
	p := NewPatternExtractor(s)
	p.SetNowFunc(fixedNow)

	trace := makeTraceEntries()
	if err := p.Extract(context.Background(), "task-1", trace); err != nil {
		t.Fatalf("Extract: %v", err)
	}

	// 验证：从 ExperienceStore 检索刚写入的 Pattern
	patterns, err := s.ListByScope(ScopeProject)
	if err != nil {
		t.Fatalf("ListByScope: %v", err)
	}
	if len(patterns) != 1 {
		t.Fatalf("expected 1 pattern, got %d", len(patterns))
	}
	got := patterns[0]
	if got.VerifiedState != VerifiedStateUnverified {
		t.Errorf("new pattern VerifiedState = %q, want %q", got.VerifiedState, VerifiedStateUnverified)
	}
	if got.Scope != ScopeProject {
		t.Errorf("new pattern Scope = %q, want %q", got.Scope, ScopeProject)
	}
	if got.Version != 1 {
		t.Errorf("new pattern Version = %d, want 1", got.Version)
	}
	if got.TaskSignature == "" {
		t.Errorf("new pattern TaskSignature should not be empty")
	}
	if len(got.Steps) != 2 {
		t.Errorf("new pattern Steps len = %d, want 2", len(got.Steps))
	}
}

// TestPatternExtractor_Extract_Hit_UpdateLastUsedAt 命中已有 Pattern：更新 LastUsedAt（§2.1）。
func TestPatternExtractor_Extract_Hit_UpdateLastUsedAt(t *testing.T) {
	s := newTestExperienceStore(t)
	p := NewPatternExtractor(s)

	// 先注入一个已存在的 Pattern
	existing := makeTestPattern("test-pid-1", "edit main.go", ScopeProject, VerifiedStateUnverified)
	existing.LastUsedAt = "2025-01-01T00:00:00Z" // 老 LastUsedAt
	if err := s.Save(existing); err != nil {
		t.Fatalf("Save existing pattern: %v", err)
	}

	// 注入倒排索引（让 Match 能命中）
	// 注：ExperienceStore.Save 已自动加倒排索引

	// 调 Extract
	fixedTime := time.Date(2026, 7, 19, 12, 0, 0, 0, time.UTC)
	p.SetNowFunc(func() time.Time { return fixedTime })

	trace := []TraceEntry{
		{
			Event:   TraceEventStart,
			AgentID: "agent-A",
			Stage:   "edit main.go",
			Model:   ModelSonnet,
		},
	}
	if err := p.Extract(context.Background(), "task-2", trace); err != nil {
		t.Fatalf("Extract: %v", err)
	}

	// 验证：LastUsedAt 应被更新（但不能精确断言哪个 pattern_id，因 Match 内部去重）
	// 此处仅断言 ExperienceStore 内存在以 "edit main.go" 为签名的 Pattern，且 LastUsedAt 已更新
	patterns, err := s.ListByScope(ScopeProject)
	if err != nil {
		t.Fatalf("ListByScope: %v", err)
	}
	if len(patterns) == 0 {
		t.Fatalf("no patterns in store")
	}
	// 查找包含 "edit main.go" 的 pattern
	var found *Pattern
	for _, p := range patterns {
		if p.TaskSignature == "edit main.go" {
			found = p
			break
		}
	}
	if found == nil {
		t.Fatalf("expected pattern with signature 'edit main.go' not found")
	}
	if found.LastUsedAt == "2025-01-01T00:00:00Z" {
		t.Errorf("LastUsedAt should be updated, got %s", found.LastUsedAt)
	}
}

// TestPatternExtractor_Extract_EmptyTrace_NoOp 空 trace 不创建 Pattern（降级）。
func TestPatternExtractor_Extract_EmptyTrace_NoOp(t *testing.T) {
	s := newTestExperienceStore(t)
	p := NewPatternExtractor(s)

	if err := p.Extract(context.Background(), "task-3", nil); err != nil {
		t.Fatalf("Extract empty trace: %v", err)
	}
	patterns, err := s.ListByScope(ScopeProject)
	if err != nil {
		t.Fatalf("ListByScope: %v", err)
	}
	if len(patterns) != 0 {
		t.Errorf("empty trace should not create pattern, got %d", len(patterns))
	}
}

// TestPatternExtractor_Extract_EmptyTaskID_NoOp taskID 空不阻断闭环。
func TestPatternExtractor_Extract_EmptyTaskID_NoOp(t *testing.T) {
	s := newTestExperienceStore(t)
	p := NewPatternExtractor(s)

	if err := p.Extract(context.Background(), "", makeTraceEntries()); err != nil {
		t.Errorf("empty taskID should return nil, got %v", err)
	}
}

// TestPatternExtractor_Extract_NilExperience_Degrade experience 为 nil 降级不报错。
func TestPatternExtractor_Extract_NilExperience_Degrade(t *testing.T) {
	// 构造 experience=nil 的 extractor
	p := &PatternExtractor{
		experience: nil,
		nowFunc:    time.Now,
	}
	if err := p.Extract(context.Background(), "task-4", makeTraceEntries()); err != nil {
		t.Errorf("nil experience should degrade silently, got %v", err)
	}
}

// TestPatternExtractor_Extract_CtxCancelled_ReturnsErr ctx 取消返回 ctx.Err()。
func TestPatternExtractor_Extract_CtxCancelled_ReturnsErr(t *testing.T) {
	s := newTestExperienceStore(t)
	p := NewPatternExtractor(s)

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	err := p.Extract(ctx, "task-5", makeTraceEntries())
	if err == nil {
		t.Errorf("cancelled ctx should return ctx.Err()")
	}
}

// TestTraceToSteps_BasicMapping trace → Step 映射正确性。
func TestTraceToSteps_BasicMapping(t *testing.T) {
	p := NewPatternExtractor(nil)
	trace := makeTraceEntries()
	steps := p.traceToSteps(trace)
	if len(steps) != 2 {
		t.Fatalf("steps len = %d, want 2", len(steps))
	}
	if steps[0].Action != TraceEventStart {
		t.Errorf("step0 Action = %q, want %q", steps[0].Action, TraceEventStart)
	}
	if steps[0].Target != "edit main.go" {
		t.Errorf("step0 Target = %q, want %q", steps[0].Target, "edit main.go")
	}
}

// TestExtractFilesFromTrace_Heuristic 文件提取启发式正确。
func TestExtractFilesFromTrace_Heuristic(t *testing.T) {
	p := NewPatternExtractor(nil)
	trace := []TraceEntry{
		{Stage: "edit main.go"},
		{Stage: "verify src/foo.go"},
		{Stage: "review docs"}, // 不含 . 不应被识别
	}
	files := p.extractFilesFromTrace(trace)
	// 应包含 main.go 与 src/foo.go
	found := map[string]bool{}
	for _, f := range files {
		found[f] = true
	}
	if !found["main.go"] {
		t.Errorf("expected main.go in files, got %v", files)
	}
	if !found["src/foo.go"] {
		t.Errorf("expected src/foo.go in files, got %v", files)
	}
}

// TestGeneratePatternID_Stable 相同输入生成相同 pattern_id。
func TestGeneratePatternID_Stable(t *testing.T) {
	p := NewPatternExtractor(nil)
	id1 := p.generatePatternID("task-x", "fix bug", 1)
	id2 := p.generatePatternID("task-x", "fix bug", 1)
	if id1 != id2 {
		t.Errorf("same input should produce same pattern_id: %s vs %s", id1, id2)
	}
	// 不同输入生成不同 pattern_id
	id3 := p.generatePatternID("task-y", "fix bug", 1)
	if id1 == id3 {
		t.Errorf("different taskID should produce different pattern_id")
	}
}
