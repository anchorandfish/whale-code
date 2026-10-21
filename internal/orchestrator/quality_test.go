// quality_test.go — 桶 1 QualityStore + QualityRecord 反例测试（TDD 先行，地基层 §3.4）。
// 5 个反例测试覆盖：并发写竞态 / 重复 TaskID 覆盖 / Reload 重建索引 / 无 cost 字段 / 空 TaskID 拒绝。

package orchestrator

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
)

// makeTestStore 在 t.TempDir() 下创建 QualityStore，返回 store 与 path。
func makeTestStore(t *testing.T) (*QualityStore, string) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "quality.jsonl")
	q, err := NewQualityStore(path)
	if err != nil {
		t.Fatalf("NewQualityStore failed: %v", err)
	}
	return q, path
}

// TestQualityStore_ConcurrentAppend 致命反例：两个 goroutine 同时 Append 同一 TaskID 的不同记录，验证 sync.Mutex 防数据损坏。
// 验证目标（地基层 §3.4）：sync.Mutex 保护并发 append，文件不出现行交错损坏。
func TestQualityStore_ConcurrentAppend(t *testing.T) {
	q, path := makeTestStore(t)

	const N = 50 // 每个 goroutine 追加 50 次
	var wg sync.WaitGroup
	wg.Add(2)

	// goroutine 1：追加 task="shared" 的 L1Score=80 记录
	go func() {
		defer wg.Done()
		for i := 0; i < N; i++ {
			_ = q.Append(QualityRecord{
				TaskID:             "shared",
				OrchestrationID:    "orch-1",
				TaskType:           TaskTypeImplement,
				L1Score:            80,
				L2Verdict:          L2Adopted,
				Timestamp:          nowISO8601(),
				MetricsCorrelation: MetricsCorrelation{AgentID: "agent-A"},
			})
		}
	}()

	// goroutine 2：追加 task="shared" 的 L1Score=20 记录
	go func() {
		defer wg.Done()
		for i := 0; i < N; i++ {
			_ = q.Append(QualityRecord{
				TaskID:             "shared",
				OrchestrationID:    "orch-2",
				TaskType:           TaskTypeImplement,
				L1Score:            20,
				L2Verdict:          L2Discarded,
				Timestamp:          nowISO8601(),
				MetricsCorrelation: MetricsCorrelation{AgentID: "agent-B"},
			})
		}
	}()

	wg.Wait()

	// 验证 1：文件应有 100 行（每行完整，无交错损坏）
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read file: %v", err)
	}
	lines := strings.Split(strings.TrimRight(string(data), "\n"), "\n")
	if len(lines) != 2*N {
		t.Fatalf("expected %d lines, got %d (concurrent append corrupted file)", 2*N, len(lines))
	}

	// 验证 2：每行必须是合法 JSON（无半行写入）
	for i, line := range lines {
		var rec QualityRecord
		if err := json.Unmarshal([]byte(line), &rec); err != nil {
			t.Fatalf("line %d unmarshal failed (data corruption): %v\nline=%q", i, err, line)
		}
	}

	// 验证 3：QueryByTaskID 应返回最后一条记录（索引指向最新行）
	last, err := q.QueryByTaskID("shared")
	if err != nil {
		t.Fatalf("QueryByTaskID failed: %v", err)
	}
	if last.OrchestrationID == "" {
		t.Fatalf("last record OrchestrationID empty: data corruption")
	}
}

// TestQualityStore_DuplicateTaskID_Overwrite 同一 TaskID 追加两次，QueryByTaskID 返回最新行。
// 验证目标（地基层 §3.4）：JSONL 无 UNIQUE 约束，靠内存 index 覆盖语义保证读最新。
func TestQualityStore_DuplicateTaskID_Overwrite(t *testing.T) {
	q, _ := makeTestStore(t)

	// 第一次写入：L1Score=70
	if err := q.Append(QualityRecord{
		TaskID:    "dup-task",
		L1Score:   70,
		L2Verdict: L2Adopted,
		Timestamp: nowISO8601(),
	}); err != nil {
		t.Fatalf("first append: %v", err)
	}

	// 第二次写入：L1Score=30（同一 TaskID）
	if err := q.Append(QualityRecord{
		TaskID:    "dup-task",
		L1Score:   30,
		L2Verdict: L2Discarded,
		Timestamp: nowISO8601(),
	}); err != nil {
		t.Fatalf("second append: %v", err)
	}

	rec, err := q.QueryByTaskID("dup-task")
	if err != nil {
		t.Fatalf("QueryByTaskID: %v", err)
	}
	if rec.L1Score != 30 {
		t.Errorf("expected latest L1Score=30, got %d (overwrite semantics broken)", rec.L1Score)
	}
	if rec.L2Verdict != L2Discarded {
		t.Errorf("expected latest L2Verdict=%s, got %s", L2Discarded, rec.L2Verdict)
	}
}

// TestQualityStore_Reload_RebuildIndex 写入 3 条（含 1 条重复 TaskID），Reload 后 index 指向最新行。
// 验证目标（地基层 §3.4）：全量扫描重建 index，遇同 TaskID 后写入覆盖先写入。
func TestQualityStore_Reload_RebuildIndex(t *testing.T) {
	q, _ := makeTestStore(t)

	// 写入 3 条，其中 task-A 出现两次
	records := []QualityRecord{
		{TaskID: "task-A", L1Score: 50, Timestamp: nowISO8601()},
		{TaskID: "task-B", L1Score: 60, Timestamp: nowISO8601()},
		{TaskID: "task-A", L1Score: 90, Timestamp: nowISO8601()}, // 覆盖 task-A
	}
	for _, r := range records {
		if err := q.Append(r); err != nil {
			t.Fatalf("append: %v", err)
		}
	}

	// 新建一个 store 实例模拟"重启"，调 Reload 重建索引
	q2, err := NewQualityStore(q.path)
	if err != nil {
		t.Fatalf("NewQualityStore: %v", err)
	}
	if err := q2.Reload(); err != nil {
		t.Fatalf("Reload: %v", err)
	}

	// 索引应有 2 个 TaskID（task-A 和 task-B）
	if got := q2.Count(); got != 2 {
		t.Errorf("expected index count=2, got %d", got)
	}

	// task-A 应返回最新行（L1Score=90，非 50）
	rec, err := q2.QueryByTaskID("task-A")
	if err != nil {
		t.Fatalf("QueryByTaskID task-A: %v", err)
	}
	if rec.L1Score != 90 {
		t.Errorf("Reload did not pick latest: expected L1Score=90, got %d", rec.L1Score)
	}
}

// TestQualityRecord_NoCostFields 致命反例：验证 QualityRecord 不含 InputTokens/OutputTokens/CostCents 等 cost 字段。
// 验证目标（PIT-002 防护）：违反「N2 是 cost 数据流唯一设计方」，零容忍。
func TestQualityRecord_NoCostFields(t *testing.T) {
	rec := QualityRecord{}
	rt := reflect.TypeOf(rec)

	// 禁止字段黑名单（PIT-002 防护）
	forbidden := []string{
		"InputTokens", "OutputTokens", "CostCents", "CacheCreation", "CacheRead",
		"InputToken", "OutputToken", "Cost", "TokensIn", "TokensOut",
	}
	for i := 0; i < rt.NumField(); i++ {
		name := rt.Field(i).Name
		for _, bad := range forbidden {
			if name == bad {
				t.Errorf("PIT-002 violation: QualityRecord must not contain cost field %q", name)
			}
		}
	}

	// 反向验证：MetricsCorrelation 只含 AgentID + Timestamp，不含 token/cost
	mcRt := reflect.TypeOf(MetricsCorrelation{})
	for i := 0; i < mcRt.NumField(); i++ {
		name := mcRt.Field(i).Name
		if name != "AgentID" && name != "Timestamp" {
			t.Errorf("MetricsCorrelation must only have AgentID/Timestamp, found %q", name)
		}
	}
}

// TestQualityStore_EmptyTaskID_Rejected 空 TaskID 应返回 error。
// 验证目标（地基层 §3.4）：TaskID 是主键，禁止空值写入。
func TestQualityStore_EmptyTaskID_Rejected(t *testing.T) {
	q, _ := makeTestStore(t)

	err := q.Append(QualityRecord{
		TaskID:    "", // 空 TaskID
		L1Score:   50,
		Timestamp: nowISO8601(),
	})
	if err == nil {
		t.Fatal("Append with empty TaskID must return error")
	}
	if !strings.Contains(err.Error(), "TaskID") {
		t.Errorf("error message should mention TaskID, got: %v", err)
	}

	// 验证 QueryByTaskID 也拒绝空 TaskID
	if _, err := q.QueryByTaskID(""); err == nil {
		t.Fatal("QueryByTaskID with empty TaskID must return error")
	}
}
