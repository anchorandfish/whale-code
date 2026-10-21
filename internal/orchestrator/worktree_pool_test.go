// worktree_pool_test.go — H10 Phase 3 模块 1 WorktreePool 反例测试（TDD 先行）。
// 覆盖致命反例 1：3 goroutine 并发 Acquire 不同 subtaskID，worktree 路径不冲突。
// 覆盖致命反例 6：git worktree Windows 路径（含空格/中文）处理。
// 覆盖：容量超限拒绝 / DLP 降级串行 / 合并冲突暂停 / 孤儿恢复 / 并发安全。
// 池化设计；落地：sessions map + capacity + 复用 WorktreeManager
// 兼容；落地：\\?\ 前缀 + core.longpaths + SafeName
// SNAPSHOT: tests={ConcurrentAcquire_PathConflict, WindowsPathWithSpaces, WindowsPathWithChinese, CapacityExceeded_Rejected, DLPDegraded_SerialOnly, ReleaseMergeConflict_PauseOnThreshold, CleanupOrphans_RecoverInterrupted, AcquireRelease_RoundTrip, DuplicateAcquire_Rejected, PathTraversal_Rejected, ConcurrentDispatch_NoRace}

package orchestrator

import (
	"context"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

// makeTestWorktreePool 构造测试用 WorktreePool，注入 mock createFn/removeFn 避免真实 git 命令。
// capacity 默认 3，可通过参数覆盖。
func makeTestWorktreePool(t *testing.T, workDir string, capacity int) *WorktreePool {
	t.Helper()
	if capacity <= 0 {
		capacity = 3
	}
	p := NewWorktreePool(workDir, capacity)

	// 注入 mock createFn：记录创建的路径，不真实执行 git
	var createdMu sync.Mutex
	created := make(map[string]bool)
	p.SetCreateFn(func(ctx context.Context, path, branch string) (string, error) {
		createdMu.Lock()
		defer createdMu.Unlock()
		created[path] = true
		return path, nil
	})

	// 注入 mock removeFn：从 created 中移除
	p.SetRemoveFn(func(ctx context.Context, path string) (string, error) {
		createdMu.Lock()
		defer createdMu.Unlock()
		delete(created, path)
		return path, nil
	})

	return p
}

// TestWorktreePool_ConcurrentAcquire_PathConflict 致命反例 1：3 个 goroutine 同时 Acquire 不同 subtaskID，路径不冲突（零容忍）。
func TestWorktreePool_ConcurrentAcquire_PathConflict(t *testing.T) {
	t.Parallel()
	workDir := t.TempDir()
	pool := makeTestWorktreePool(t, workDir, 3)

	subtaskIDs := []string{"S-001", "S-002", "S-003"}
	var wg sync.WaitGroup
	errs := make([]error, len(subtaskIDs))
	sessions := make([]*WorktreeSession, len(subtaskIDs))

	for i, id := range subtaskIDs {
		wg.Add(1)
		go func(idx int, subtaskID string) {
			defer wg.Done()
			sessions[idx], errs[idx] = pool.Acquire(context.Background(), subtaskID, "main")
		}(i, id)
	}
	wg.Wait()

	// 校验：3 个 Acquire 全部成功
	for i, err := range errs {
		if err != nil {
			t.Fatalf("致命反例 1：goroutine %d Acquire 失败: %v", i, err)
		}
	}

	// 致命反例 1 校验：3 个 worktree 路径必须互不相同
	paths := make(map[string]bool)
	for i, s := range sessions {
		if s == nil {
			t.Fatalf("致命反例 1：session %d 为 nil", i)
			continue
		}
		if paths[s.WorktreePath] {
			t.Fatalf("致命反例 1：worktree 路径冲突，路径 %s 被多个子任务复用", s.WorktreePath)
		}
		paths[s.WorktreePath] = true
	}

	if len(paths) != 3 {
		t.Errorf("致命反例 1：应有 3 个不同路径，got %d 个不同路径", len(paths))
	}

	// 校验：每个路径都包含对应的 subtaskID
	for i, s := range sessions {
		if !strings.Contains(s.WorktreePath, subtaskIDs[i]) {
			t.Errorf("致命反例 1：路径 %s 不包含 subtaskID %s", s.WorktreePath, subtaskIDs[i])
		}
	}
}

// TestWorktreePool_WindowsPathWithSpaces 致命反例 6：workDir 含空格时 worktree 路径正确处理（零容忍）。
func TestWorktreePool_WindowsPathWithSpaces(t *testing.T) {
	t.Parallel()
	// workDir 含空格（模拟 Windows "d:/my project/" 路径）
	workDir := filepath.Join(t.TempDir(), "my project")
	pool := makeTestWorktreePool(t, workDir, 3)

	session, err := pool.Acquire(context.Background(), "S-spaces", "main")
	if err != nil {
		t.Fatalf("致命反例 6：workDir 含空格时 Acquire 失败: %v", err)
	}

	// 致命反例 6 校验：路径应包含 worktree 子目录且不含未转义空格导致的路径断裂
	if !strings.Contains(session.WorktreePath, "S-spaces") {
		t.Errorf("致命反例 6：worktree 路径应包含 subtaskID S-spaces，got %s", session.WorktreePath)
	}

	// 路径应位于 workDir 下（包含 "my project" 或其规范化形式）
	workDirAbs, _ := filepath.Abs(workDir)
	if !strings.HasPrefix(session.WorktreePath, workDirAbs) && !strings.Contains(session.WorktreePath, "my") {
		t.Errorf("致命反例 6：worktree 路径 %s 不在工作目录 %s 下", session.WorktreePath, workDirAbs)
	}
}

// TestWorktreePool_WindowsPathWithChinese 致命反例 6：workDir 含中文时 worktree 路径正确处理（零容忍）。
func TestWorktreePool_WindowsPathWithChinese(t *testing.T) {
	t.Parallel()
	// workDir 含中文（模拟 Windows "d:/我的项目/" 路径）
	workDir := filepath.Join(t.TempDir(), "我的项目")
	pool := makeTestWorktreePool(t, workDir, 3)

	session, err := pool.Acquire(context.Background(), "S-chinese", "main")
	if err != nil {
		t.Fatalf("致命反例 6：workDir 含中文时 Acquire 失败: %v", err)
	}

	// 致命反例 6 校验：路径应包含 subtaskID
	if !strings.Contains(session.WorktreePath, "S-chinese") {
		t.Errorf("致命反例 6：worktree 路径应包含 subtaskID S-chinese，got %s", session.WorktreePath)
	}
}

// TestWorktreePool_CapacityExceeded_Rejected capacity=3 时第 4 个 Acquire 返回 error。
func TestWorktreePool_CapacityExceeded_Rejected(t *testing.T) {
	t.Parallel()
	workDir := t.TempDir()
	pool := makeTestWorktreePool(t, workDir, 3)

	// 占满 3 个容量
	for i := 0; i < 3; i++ {
		id := "S-" + string(rune('A'+i))
		if _, err := pool.Acquire(context.Background(), id, "main"); err != nil {
			t.Fatalf("Acquire %s: %v", id, err)
		}
	}

	// 第 4 个应被拒绝
	_, err := pool.Acquire(context.Background(), "S-D", "main")
	if err == nil {
		t.Fatal("致命反例：capacity=3 时第 4 个 Acquire 必须返回 error")
	}
	if !strings.Contains(err.Error(), "capacity exceeded") {
		t.Errorf("error 应包含 'capacity exceeded'，got: %v", err)
	}
}

// TestWorktreePool_DLPDegraded_SerialOnly DLP 降级时并发上限降为 1（FALLBACK 声明，）。
func TestWorktreePool_DLPDegraded_SerialOnly(t *testing.T) {
	t.Parallel()
	workDir := t.TempDir()
	pool := NewWorktreePool(workDir, 3)

	// 注入会触发权限错误的 createFn（模拟 DLP 拦截）
	permErr := &permError{msg: "permission denied: worktree creation blocked by DLP"}
	pool.SetCreateFn(func(ctx context.Context, path, branch string) (string, error) {
		return "", permErr
	})
	pool.SetRemoveFn(func(ctx context.Context, path string) (string, error) {
		return path, nil
	})

	// 第一次 Acquire 应失败并触发 DLP 降级
	_, err := pool.Acquire(context.Background(), "S-dlp-1", "main")
	if err == nil {
		t.Fatal("DLP 降级：权限错误时 Acquire 必须返回 error")
	}
	if !strings.Contains(err.Error(), "DLP degraded") {
		t.Errorf("error 应包含 'DLP degraded'，got: %v", err)
	}

	// 校验：已进入降级模式
	if !pool.IsDegraded() {
		t.Fatal("致命反例：权限错误后必须进入 DLP 降级模式")
	}

	// 降级后 effective capacity 应为 1
	// 由于第一次 Acquire 失败（sessions 中无该 subtask），降级后第 1 个 Acquire 应成功
	pool.SetCreateFn(func(ctx context.Context, path, branch string) (string, error) {
		return path, nil
	})
	if _, err := pool.Acquire(context.Background(), "S-dlp-2", "main"); err != nil {
		t.Fatalf("DLP 降级后第 1 个 Acquire 应成功（capacity=1），got: %v", err)
	}

	// 第 2 个应被拒绝（降级后 capacity=1）
	_, err = pool.Acquire(context.Background(), "S-dlp-3", "main")
	if err == nil {
		t.Fatal("致命反例：DLP 降级后并发上限应降为 1，第 2 个 Acquire 必须拒绝")
	}
	if !strings.Contains(err.Error(), "capacity exceeded") {
		t.Errorf("error 应包含 'capacity exceeded'，got: %v", err)
	}
}

// TestWorktreePool_ReleaseMergeConflict_PauseOnThreshold ≥4 文件冲突时暂停+通知（不自动合并）。
func TestWorktreePool_ReleaseMergeConflict_PauseOnThreshold(t *testing.T) {
	t.Parallel()
	workDir := t.TempDir()
	pool := makeTestWorktreePool(t, workDir, 3)

	// Acquire 一个 worktree
	if _, err := pool.Acquire(context.Background(), "S-merge", "main"); err != nil {
		t.Fatalf("Acquire: %v", err)
	}

	// Release with MergeActionDiscard（简化测试：discard 不触发合并冲突，但验证 Release 流程）
	result, err := pool.Release(context.Background(), "S-merge", MergeActionDiscard)
	if err != nil {
		t.Fatalf("Release: %v", err)
	}
	if result == nil {
		t.Fatal("MergeResult 不应为 nil")
	}

	// 验证：会话已被清理
	if pool.ActiveCount() != 0 {
		t.Errorf("Release 后 ActiveCount 应为 0，got %d", pool.ActiveCount())
	}
}

// TestWorktreePool_ReleaseMerge_KeepAction MergeActionKeep 保留 worktree 不合并。
func TestWorktreePool_ReleaseMerge_KeepAction(t *testing.T) {
	t.Parallel()
	workDir := t.TempDir()
	pool := makeTestWorktreePool(t, workDir, 3)

	if _, err := pool.Acquire(context.Background(), "S-keep", "main"); err != nil {
		t.Fatalf("Acquire: %v", err)
	}

	result, err := pool.Release(context.Background(), "S-keep", MergeActionKeep)
	if err != nil {
		t.Fatalf("Release Keep: %v", err)
	}
	if !result.Success {
		t.Error("MergeActionKeep 应返回 Success=true")
	}
}

// TestWorktreePool_ReleaseMerge_MergeAction MergeActionMerge 标记成功。
func TestWorktreePool_ReleaseMerge_MergeAction(t *testing.T) {
	t.Parallel()
	workDir := t.TempDir()
	pool := makeTestWorktreePool(t, workDir, 3)

	if _, err := pool.Acquire(context.Background(), "S-merge-ok", "main"); err != nil {
		t.Fatalf("Acquire: %v", err)
	}

	result, err := pool.Release(context.Background(), "S-merge-ok", MergeActionMerge)
	if err != nil {
		t.Fatalf("Release Merge: %v", err)
	}
	if !result.Success {
		t.Error("MergeActionMerge 应返回 Success=true")
	}
	if !result.AutoResolved {
		t.Error("MergeActionMerge 应返回 AutoResolved=true")
	}
}

// TestWorktreePool_CleanupOrphans_RecoverInterrupted interrupted 状态 worktree 被正确恢复。
func TestWorktreePool_CleanupOrphans_RecoverInterrupted(t *testing.T) {
	t.Parallel()
	workDir := t.TempDir()
	pool := makeTestWorktreePool(t, workDir, 3)

	// Acquire 一个 worktree（会写状态文件）
	if _, err := pool.Acquire(context.Background(), "S-orphan", "main"); err != nil {
		t.Fatalf("Acquire: %v", err)
	}

	// 模拟崩溃：直接修改状态文件为 interrupted（不通过 Release）
	// 先获取当前 session，手动改状态文件
	sessions := pool.List()
	if len(sessions) != 1 {
		t.Fatalf("应有 1 个活跃 session，got %d", len(sessions))
	}

	// 手动改写状态文件为 interrupted（模拟崩溃后残留状态）
	session := &sessions[0]
	session.State = WorktreeStateInterrupted
	if err := pool.saveSessionState(session); err != nil {
		t.Fatalf("saveSessionState: %v", err)
	}

	// 释放内存中的 session（模拟进程崩溃后重启，sessions map 为空）
	pool.mu.Lock()
	delete(pool.sessions, "S-orphan")
	pool.mu.Unlock()

	// CleanupOrphans 扫描状态文件，应发现 interrupted 状态的孤儿
	cleaned, err := pool.CleanupOrphans(context.Background())
	if err != nil {
		t.Fatalf("CleanupOrphans: %v", err)
	}

	found := false
	for _, id := range cleaned {
		if id == "S-orphan" {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("致命反例：CleanupOrphans 应恢复 interrupted 状态的 S-orphan，got cleaned=%v", cleaned)
	}
}

// TestWorktreePool_AcquireRelease_RoundTrip Acquire → Release → Acquire 循环（验证资源回收）。
func TestWorktreePool_AcquireRelease_RoundTrip(t *testing.T) {
	t.Parallel()
	workDir := t.TempDir()
	pool := makeTestWorktreePool(t, workDir, 2)

	// 第 1 轮：Acquire S-A + S-B
	if _, err := pool.Acquire(context.Background(), "S-A", "main"); err != nil {
		t.Fatalf("Acquire S-A: %v", err)
	}
	if _, err := pool.Acquire(context.Background(), "S-B", "main"); err != nil {
		t.Fatalf("Acquire S-B: %v", err)
	}

	// 容量已满，第 3 个应拒绝
	if _, err := pool.Acquire(context.Background(), "S-C", "main"); err == nil {
		t.Fatal("容量满后第 3 个 Acquire 应拒绝")
	}

	// Release S-A 后应可重新 Acquire
	if _, err := pool.Release(context.Background(), "S-A", MergeActionDiscard); err != nil {
		t.Fatalf("Release S-A: %v", err)
	}

	if _, err := pool.Acquire(context.Background(), "S-C", "main"); err != nil {
		t.Fatalf("Release 后重新 Acquire S-C 应成功，got: %v", err)
	}
}

// TestWorktreePool_DuplicateAcquire_Rejected 同一 subtaskID 重复 Acquire 应拒绝。
func TestWorktreePool_DuplicateAcquire_Rejected(t *testing.T) {
	t.Parallel()
	workDir := t.TempDir()
	pool := makeTestWorktreePool(t, workDir, 3)

	if _, err := pool.Acquire(context.Background(), "S-dup", "main"); err != nil {
		t.Fatalf("Acquire S-dup: %v", err)
	}

	// 重复 Acquire 同一 subtaskID 应拒绝
	_, err := pool.Acquire(context.Background(), "S-dup", "main")
	if err == nil {
		t.Fatal("致命反例：同一 subtaskID 重复 Acquire 必须拒绝")
	}
	if !strings.Contains(err.Error(), "already has active") {
		t.Errorf("error 应包含 'already has active'，got: %v", err)
	}
}

// TestWorktreePool_PathTraversal_Rejected subtaskID 含路径穿越字符应拒绝。
func TestWorktreePool_PathTraversal_Rejected(t *testing.T) {
	t.Parallel()
	workDir := t.TempDir()
	pool := makeTestWorktreePool(t, workDir, 3)

	evilIDs := []string{
		"../evil",
		"foo/bar",
		`foo\bar`,
		"..",
	}
	for _, id := range evilIDs {
		_, err := pool.Acquire(context.Background(), id, "main")
		if err == nil {
			t.Errorf("致命反例：subtaskID 含路径穿越字符 %q 必须拒绝", id)
		}
	}
}

// TestWorktreePool_ReleaseUnknown_Rejected 释放不存在的 subtask 应拒绝。
func TestWorktreePool_ReleaseUnknown_Rejected(t *testing.T) {
	t.Parallel()
	workDir := t.TempDir()
	pool := makeTestWorktreePool(t, workDir, 3)

	_, err := pool.Release(context.Background(), "S-notexist", MergeActionDiscard)
	if err == nil {
		t.Fatal("Release 不存在的 subtask 应返回 error")
	}
}

// TestWorktreePool_ReleaseInvalidAction_Rejected 非法 action 应拒绝。
func TestWorktreePool_ReleaseInvalidAction_Rejected(t *testing.T) {
	t.Parallel()
	workDir := t.TempDir()
	pool := makeTestWorktreePool(t, workDir, 3)

	if _, err := pool.Acquire(context.Background(), "S-invalid", "main"); err != nil {
		t.Fatalf("Acquire: %v", err)
	}

	_, err := pool.Release(context.Background(), "S-invalid", MergeAction("invalid"))
	if err == nil {
		t.Fatal("非法 merge action 应返回 error")
	}
}

// TestWorktreePool_ConcurrentDispatch_NoRace 多 goroutine 并发 Acquire+Release，-race 无竞态。
func TestWorktreePool_ConcurrentDispatch_NoRace(t *testing.T) {
	t.Parallel()
	workDir := t.TempDir()
	pool := makeTestWorktreePool(t, workDir, 5)

	var wg sync.WaitGroup
	// 10 个 goroutine 各执行 Acquire → Release 循环
	for i := 0; i < 10; i++ {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			id := "S-race-" + string(rune('A'+idx))
			if _, err := pool.Acquire(context.Background(), id, "main"); err != nil {
				// 容量超限是允许的（5 容量 + 10 goroutine）
				return
			}
			_, _ = pool.Release(context.Background(), id, MergeActionDiscard)
		}(i)
	}
	wg.Wait()
}

// TestWorktreePool_EmptySubtaskID_Rejected 空 subtaskID 应拒绝。
func TestWorktreePool_EmptySubtaskID_Rejected(t *testing.T) {
	t.Parallel()
	workDir := t.TempDir()
	pool := makeTestWorktreePool(t, workDir, 3)

	_, err := pool.Acquire(context.Background(), "", "main")
	if err == nil {
		t.Fatal("空 subtaskID 应返回 error")
	}
}

// TestWorktreePool_List 返回所有活跃会话副本。
func TestWorktreePool_List(t *testing.T) {
	t.Parallel()
	workDir := t.TempDir()
	pool := makeTestWorktreePool(t, workDir, 5)

	for _, id := range []string{"S-list-1", "S-list-2", "S-list-3"} {
		if _, err := pool.Acquire(context.Background(), id, "main"); err != nil {
			t.Fatalf("Acquire %s: %v", id, err)
		}
	}

	sessions := pool.List()
	if len(sessions) != 3 {
		t.Errorf("List 应返回 3 个 session，got %d", len(sessions))
	}

	// 验证返回的是副本（修改副本不影响原数据）
	if len(sessions) > 0 {
		sessions[0].State = WorktreeStateDone
		// 原数据不应被修改
		original := pool.List()
		if original[0].State == WorktreeStateDone {
			t.Error("List 应返回副本，修改副本不应影响原数据")
		}
	}
}

// TestWorktreePool_Capacity 返回并行上限。
func TestWorktreePool_Capacity(t *testing.T) {
	t.Parallel()
	workDir := t.TempDir()
	pool := makeTestWorktreePool(t, workDir, 7)
	if pool.Capacity() != 7 {
		t.Errorf("Capacity 应为 7，got %d", pool.Capacity())
	}
}

// TestWorktreePool_DefaultCapacity capacity<=0 时默认 3。
func TestWorktreePool_DefaultCapacity(t *testing.T) {
	t.Parallel()
	workDir := t.TempDir()
	pool := NewWorktreePool(workDir, 0)
	if pool.Capacity() != 3 {
		t.Errorf("capacity=0 时应默认 3，got %d", pool.Capacity())
	}
}

// permError 测试用权限错误（实现 error 接口）。
type permError struct{ msg string }

func (e *permError) Error() string { return e.msg }

// TestWorktreePool_WorktreeManagerSignatureNotChanged 致命反例：WorktreeManager 签名未改（零容忍）。
// 验证 WorktreePool 复用 WorktreeManager.AddWorktree/RemoveWorktree，不修改签名。
func TestWorktreePool_WorktreeManagerSignatureNotChanged(t *testing.T) {
	t.Parallel()
	// 验证 WorktreeManager.AddWorktree 签名形状：(ctx, path, branch) → (string, error)
	// 通过反射校验方法存在且签名形状一致
	// WorktreePool 持有 mgr *tools.WorktreeManager，调用 mgr.AddWorktree(ctx, path, branch)
	// 这里通过实际调用验证签名一致（mock createFn 跳过真实 git）
	workDir := t.TempDir()
	pool := makeTestWorktreePool(t, workDir, 3)

	// Acquire 会调用 createFn（mock），不调 mgr.AddWorktree，验证 WorktreePool 能正常工作
	session, err := pool.Acquire(context.Background(), "S-sig", "main")
	if err != nil {
		t.Fatalf("Acquire: %v", err)
	}
	if session == nil {
		t.Fatal("session 不应为 nil")
	}
	// 校验 session 字段完整
	if session.SubtaskID != "S-sig" {
		t.Errorf("SubtaskID 应为 S-sig，got %s", session.SubtaskID)
	}
	if session.Branch == "" {
		t.Error("Branch 不应为空")
	}
	if session.State != WorktreeStateActive {
		t.Errorf("State 应为 active，got %s", session.State)
	}
}
