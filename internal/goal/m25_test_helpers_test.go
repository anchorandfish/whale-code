// m25_test_helpers_test.go — M25-02/M25-04 测试辅助（mock Store + 构造测试用 Manager）。
//
// 提供 mockStore 实现 goal.Store 接口，用于测试不依赖文件系统。
// 提供 newTestManager 构造可测试的 Manager（evalAgent=nil 走降级路径）。

package goal

import (
	"errors"
	"os"
	"sync"
)

// mockStore 内存 mock 实现 goal.Store 接口（测试专用）。
type mockStore struct {
	mu      sync.Mutex
	goals   map[string]*Goal
	listErr error // 模拟 List 错误
}

// newMockStore 创建 mock store。
func newMockStore() *mockStore {
	return &mockStore{goals: make(map[string]*Goal)}
}

// Load 读取 goal（文件不存在返回 nil + os.ErrNotExist，与 fileStore 行为一致）。
// 修复 H1-P0-1（mock 契约忠实度）：mockStore 不再用 errNotFoundSentinel 掩盖生产 bug，
// 改为返回 os.ErrNotExist 与 fileStore.Load 行为一致，确保测试路径与生产路径一致。
func (s *mockStore) Load(threadID string) (*Goal, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	g, ok := s.goals[threadID]
	if !ok {
		return nil, os.ErrNotExist
	}
	// 返回副本避免测试间污染
	cp := *g
	return &cp, nil
}

// Save 保存 goal。
func (s *mockStore) Save(threadID string, g *Goal) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	cp := *g
	s.goals[threadID] = &cp
	return nil
}

// Delete 删除 goal（幂等）。
func (s *mockStore) Delete(threadID string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.goals, threadID)
	return nil
}

// List 列出所有 goal。
func (s *mockStore) List() ([]*Goal, error) {
	if s.listErr != nil {
		return nil, s.listErr
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	result := make([]*Goal, 0, len(s.goals))
	for _, g := range s.goals {
		cp := *g
		result = append(result, &cp)
	}
	return result, nil
}

// newTestManager 统一在 manager_test.go 定义（两参数 + testOpt 选项模式）。
// newTestActiveGoal 创建测试用的 active goal。
func newTestActiveGoal(threadID string) *Goal {
	return &Goal{
		GoalID:      "test-goal-" + threadID,
		ThreadID:    threadID,
		Objective:   "test objective for " + threadID,
		Status:      StatusActive,
		TokenBudget: 100000,
		CreatedAt:   1000000,
		UpdatedAt:   1000000,
	}
}

// errTestSentinel 测试用哨兵错误。
var errTestSentinel = errors.New("test sentinel error")
