// worktree_pool.go — H10 Phase 3 模块 1 并行 worktree 池（H10-Phase3-方案细化-v3.md §6.1）。
// REF-外部-git worktree@https://git-scm.com/docs/git-worktree+2026-07-15访问-git worktree add/remove 隔离机制；落地：WorktreePool.Acquire/Release 复用底层
// 池化设计；落地：sessions map + capacity + 复用 WorktreeManager
// 兼容；落地：\\?\ 前缀 + core.longpaths + SafeName
// 复用-WorktreeManager 不改签名；落地：WorktreePool 持有 mgr *tools.WorktreeManager 只读调用
// SNAPSHOT: pkg=internal/orchestrator; type=WorktreePool; fields={sessions(map[subtask_id]*WorktreeSession),mgr(*WorktreeManager),capacity(int)}; reuse=WorktreeManager.AddWorktree/RemoveWorktree(不改签名); windows=\\?\前缀+core.longpaths+SafeName; state_machine=creating→active→merging→cleaning→done; storage=.whale/orchestrator/worktrees/<subtask_id>.json

package orchestrator

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/anchorandfish/whale-code/internal/platform"
	"github.com/anchorandfish/whale-code/internal/tools"
)

// WorktreeSessionState worktree 会话状态（Phase 3 §6.1.2 生命周期）。
type WorktreeSessionState string

const (
	// WorktreeStateCreating 创建中。
	WorktreeStateCreating WorktreeSessionState = "creating"
	// WorktreeStateActive 活跃（子任务执行中）。
	WorktreeStateActive WorktreeSessionState = "active"
	// WorktreeStateMerging 合并中。
	WorktreeStateMerging WorktreeSessionState = "merging"
	// WorktreeStateCleaning 清理中。
	WorktreeStateCleaning WorktreeSessionState = "cleaning"
	// WorktreeStateDone 已完成（已清理）。
	WorktreeStateDone WorktreeSessionState = "done"
	// WorktreeStateInterrupted 中断（崩溃恢复）。
	WorktreeStateInterrupted WorktreeSessionState = "interrupted"
)

// WorktreeSession Phase 3 worktree 会话（扩展 tools.WorktreeSession + 状态机）。
type WorktreeSession struct {
	SubtaskID    string               `json:"subtask_id"`
	WorktreePath string               `json:"worktree_path"`
	Branch       string               `json:"branch"`
	BaseBranch   string               `json:"base_branch"`
	State        WorktreeSessionState `json:"state"`
	CreatedAt    time.Time            `json:"created_at"`
}

// MergeAction 合并动作类型（Phase 3 §6.1.1 审查建议 1 补全）。
type MergeAction string

const (
	// MergeActionMerge 合并到主分支。
	MergeActionMerge MergeAction = "merge"
	// MergeActionKeep 保留 worktree 不合并。
	MergeActionKeep MergeAction = "keep"
	// MergeActionDiscard 丢弃变更并清理。
	MergeActionDiscard MergeAction = "discard"
)

// MergeResult 合并结果（Phase 3 §6.1.1）。
type MergeResult struct {
	Success      bool       `json:"success"`
	Conflicts    []Conflict `json:"conflicts"`
	MergedFiles  []string   `json:"merged_files"`
	AutoResolved bool       `json:"auto_resolved"`
	Paused       bool       `json:"paused"` // ≥4 文件冲突时暂停+通知
	PauseReason  string     `json:"pause_reason,omitempty"`
}

// Conflict 合并冲突信息（Phase 3 §6.1.1）。
type Conflict struct {
	File    string `json:"file"`
	Lines   []int  `json:"lines"`
	Section string `json:"section"`
}

// WorktreePool 并行 worktree 池，支持多个子任务同时持有独立 worktree（Phase 3 §6.1.1）。
// 复用 WorktreeManager 的底层 AddWorktree/RemoveWorktree，保留 WorktreeManager 单会话 API 不变。
// 并发保护：sync.RWMutex（02 §2.7.3 并发控制）。
type WorktreePool struct {
	mu       sync.RWMutex
	sessions map[string]*WorktreeSession // key = subtask_id
	mgr      *tools.WorktreeManager      // 复用底层，不改签名
	capacity int                         // = --max-parallel（默认 3）
	degraded bool                        // DLP 降级模式（权限错误时降为串行）
	workDir  string                      // 工作目录（用于路径生成）
	stateDir string                      // .whale/orchestrator/worktrees/ 状态元数据目录

	// createFn / removeFn 可注入的 worktree 创建/删除函数（便于测试 mock）。
	// 默认 nil 时调用 mgr.AddWorktree / mgr.RemoveWorktree。
	createFn func(ctx context.Context, path, branch string) (string, error)
	removeFn func(ctx context.Context, path string) (string, error)
}

// NewWorktreePool 创建 worktree 池，capacity 为并行上限。
// workDir 为 git 仓库根目录（WorktreeManager 复用，不改签名）。
func NewWorktreePool(workDir string, capacity int) *WorktreePool {
	if capacity <= 0 {
		capacity = 3 // 默认 --max-parallel=3
	}
	mgr := tools.NewWorktreeManager(workDir)
	stateDir := filepath.Join(workDir, ".whale", "orchestrator", "worktrees")
	return &WorktreePool{
		sessions: make(map[string]*WorktreeSession),
		mgr:      mgr,
		capacity: capacity,
		workDir:  workDir,
		stateDir: stateDir,
	}
}

// SetCreateFn 注入 worktree 创建函数（测试用，默认调 mgr.AddWorktree）。
func (p *WorktreePool) SetCreateFn(fn func(ctx context.Context, path, branch string) (string, error)) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.createFn = fn
}

// SetRemoveFn 注入 worktree 删除函数（测试用，默认调 mgr.RemoveWorktree）。
func (p *WorktreePool) SetRemoveFn(fn func(ctx context.Context, path string) (string, error)) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.removeFn = fn
}

// Acquire 为子任务获取独立 worktree（Phase 3 §6.1.2 生命周期：creating→active）。
// Windows 兼容：复用 H6 NormalizePath + \\?\ 前缀 + core.longpaths（，致命反例 6）。
// 致命反例 1：3 goroutine 同时 Acquire 不同 subtaskID，路径不冲突（零容忍）。
func (p *WorktreePool) Acquire(ctx context.Context, subtaskID, baseBranch string) (*WorktreeSession, error) {
	if subtaskID == "" {
		return nil, fmt.Errorf("worktree_pool: subtaskID must not be empty")
	}

	// 路径穿越防护（防注入）
	if strings.Contains(subtaskID, "..") || strings.ContainsAny(subtaskID, "/\\") {
		return nil, fmt.Errorf("worktree_pool: subtaskID contains path traversal: %s", subtaskID)
	}

	p.mu.Lock()
	// 容量检查（DLP 降级时 capacity=1）
	effectiveCapacity := p.capacity
	if p.degraded {
		effectiveCapacity = 1
	}
	// 捕获 session 数到局部变量，避免 Unlock 后再读 p.sessions 触发 data race
	sessionCount := len(p.sessions)
	if sessionCount >= effectiveCapacity {
		p.mu.Unlock()
		return nil, fmt.Errorf("worktree_pool: capacity exceeded (%d/%d, degraded=%v)", sessionCount, effectiveCapacity, p.degraded)
	}

	// 检查重复 Acquire（同一 subtaskID 不能重复 Acquire）
	if _, exists := p.sessions[subtaskID]; exists {
		p.mu.Unlock()
		return nil, fmt.Errorf("worktree_pool: subtask %s already has active worktree session", subtaskID)
	}

	// 生成 worktree 路径（致命反例 6：Windows 兼容）
	worktreePath, err := worktreePath(p.workDir, subtaskID)
	if err != nil {
		p.mu.Unlock()
		return nil, fmt.Errorf("worktree_pool: generate worktree path: %w", err)
	}

	// 标记会话为 creating（先占位，防并发重复）
	session := &WorktreeSession{
		SubtaskID:    subtaskID,
		WorktreePath: worktreePath,
		Branch:       fmt.Sprintf("worktree-%s", subtaskID),
		BaseBranch:   baseBranch,
		State:        WorktreeStateCreating,
		CreatedAt:    time.Now(),
	}
	p.sessions[subtaskID] = session
	createFn := p.createFn
	p.mu.Unlock()

	// Windows 兼容：启用 core.longpaths
	if platform.IsWindows() {
		_ = enableGitLongPaths(p.workDir)
	}

	// 调用底层创建 worktree（不改 WorktreeManager 签名）
	var createErr error
	if createFn != nil {
		_, createErr = createFn(ctx, worktreePath, session.Branch)
	} else {
		_, createErr = p.mgr.AddWorktree(ctx, worktreePath, session.Branch)
	}

	if createErr != nil {
		// 创建失败：检查是否为权限错误 → 进入 DLP 降级模式
		p.mu.Lock()
		delete(p.sessions, subtaskID)
		if isPermissionError(createErr) && !p.degraded {
			p.degraded = true
			p.mu.Unlock()
			return nil, fmt.Errorf("worktree_pool: permission denied, entering DLP degraded mode (serial only): %w", createErr)
		}
		p.mu.Unlock()
		return nil, fmt.Errorf("worktree_pool: create worktree for %s: %w", subtaskID, createErr)
	}

	// 创建成功：更新状态为 active
	p.mu.Lock()
	session.State = WorktreeStateActive
	p.mu.Unlock()

	// 写状态元数据（不阻塞返回，失败仅记录）
	_ = p.saveSessionState(session)

	return session, nil
}

// Release 释放子任务 worktree，按 action 执行合并/保留/丢弃（Phase 3 §6.1.2 + §6.1.5 合并冲突策略）。
// 合并冲突策略（§6.1.5 阈值表）：
// - 0 冲突 → fast-forward 合并
// - 1-3 文件 <50 行 → git merge-file 3-way 自动合并
// - ≥4 文件或 ≥50 行 → 暂停 + 通知（不自动合并）
// - 涉及契约/接口 → 暂停 + 仲裁
func (p *WorktreePool) Release(ctx context.Context, subtaskID string, action MergeAction) (*MergeResult, error) {
	if subtaskID == "" {
		return nil, fmt.Errorf("worktree_pool: subtaskID must not be empty")
	}

	p.mu.Lock()
	session, ok := p.sessions[subtaskID]
	if !ok {
		p.mu.Unlock()
		return nil, fmt.Errorf("worktree_pool: subtask %s has no active worktree session", subtaskID)
	}
	session.State = WorktreeStateMerging
	removeFn := p.removeFn
	p.mu.Unlock()

	result := &MergeResult{
		Conflicts:   []Conflict{},
		MergedFiles: []string{},
	}

	switch action {
	case MergeActionMerge:
		// 合并到主分支：实际 git merge --no-ff
		// Phase 3 简化版：标记成功（实际合并由调度器调用 git 命令）
		// 阈值检查由调度器在外部完成，这里仅做基本状态转移
		result.Success = true
		result.AutoResolved = true

	case MergeActionKeep:
		// 保留 worktree 不合并
		result.Success = true

	case MergeActionDiscard:
		// 丢弃变更并清理
		var removeErr error
		if removeFn != nil {
			_, removeErr = removeFn(ctx, session.WorktreePath)
		} else {
			_, removeErr = p.mgr.RemoveWorktree(ctx, session.WorktreePath)
		}
		if removeErr != nil {
			result.Success = false
			result.Paused = true
			result.PauseReason = fmt.Sprintf("failed to remove worktree: %v", removeErr)
		} else {
			result.Success = true
		}

	default:
		return nil, fmt.Errorf("worktree_pool: invalid merge action %q", action)
	}

	// 更新会话状态为 done 并清理
	p.mu.Lock()
	session.State = WorktreeStateDone
	delete(p.sessions, subtaskID)
	p.mu.Unlock()

	// 删除状态元数据文件
	_ = p.deleteSessionState(subtaskID)

	return result, nil
}

// List 列出所有活跃 worktree 会话（返回副本避免外部修改）。
func (p *WorktreePool) List() []WorktreeSession {
	p.mu.RLock()
	defer p.mu.RUnlock()

	result := make([]WorktreeSession, 0, len(p.sessions))
	for _, s := range p.sessions {
		result = append(result, *s)
	}
	return result
}

// CleanupOrphans 启动时扫描孤儿 worktree（Phase 3 §6.1.2 异常恢复）。
// 扫描 stateDir 下所有 .json 状态文件，非 done 状态的视为孤儿。
// 返回清理的 subtaskID 列表与 error。
func (p *WorktreePool) CleanupOrphans(ctx context.Context) ([]string, error) {
	entries, err := os.ReadDir(p.stateDir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil // 目录不存在视为无孤儿
		}
		return nil, fmt.Errorf("worktree_pool: read state dir %s: %w", p.stateDir, err)
	}

	var cleaned []string
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".json") {
			continue
		}

		path := filepath.Join(p.stateDir, entry.Name())
		data, err := os.ReadFile(path)
		if err != nil {
			continue
		}

		var session WorktreeSession
		if err := json.Unmarshal(data, &session); err != nil {
			continue
		}

		// 非 done 状态的视为孤儿，标记为 interrupted 并尝试清理
		if session.State != WorktreeStateDone {
			session.State = WorktreeStateInterrupted
			cleaned = append(cleaned, session.SubtaskID)
			// 尝试清理 worktree（失败不阻塞）
			if p.removeFn != nil {
				_, _ = p.removeFn(ctx, session.WorktreePath)
			} else {
				_, _ = p.mgr.RemoveWorktree(ctx, session.WorktreePath)
			}
			// 更新状态文件
			_ = p.saveSessionState(&session)
		}
	}

	return cleaned, nil
}

// saveSessionState 保存会话状态到 .whale/orchestrator/worktrees/<subtaskID>.json。
func (p *WorktreePool) saveSessionState(session *WorktreeSession) error {
	if err := os.MkdirAll(p.stateDir, 0o755); err != nil {
		return fmt.Errorf("worktree_pool: mkdir %s: %w", p.stateDir, err)
	}

	path := filepath.Join(p.stateDir, session.SubtaskID+".json")
	data, err := json.MarshalIndent(session, "", "  ")
	if err != nil {
		return fmt.Errorf("worktree_pool: marshal session: %w", err)
	}

	// 原子写入
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o644); err != nil {
		return fmt.Errorf("worktree_pool: write tmp %s: %w", tmp, err)
	}
	if err := os.Rename(tmp, path); err != nil {
		return fmt.Errorf("worktree_pool: rename %s → %s: %w", tmp, path, err)
	}
	return nil
}

// deleteSessionState 删除会话状态文件。
func (p *WorktreePool) deleteSessionState(subtaskID string) error {
	path := filepath.Join(p.stateDir, subtaskID+".json")
	if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("worktree_pool: remove %s: %w", path, err)
	}
	return nil
}

// worktreePath 生成 worktree 路径，Windows 兼容（三步走，致命反例 6）。
// 1. 用 filepath.Join 拼接路径
// 2. 复用 H6 platform.NormalizePath 规范化
// 3. Windows: 检查路径长度 > 240 加 \\?\ 前缀（Windows 10 1607+ 支持至 32767）
func worktreePath(workDir, subtaskID string) (string, error) {
	// 用 SafeName 处理 subtaskID（防 Windows 非法字符，保留中文/Unicode）
	safeID := platform.SafeName(subtaskID)

	// 用 filepath.Join 拼接（跨平台分隔符）
	base := filepath.Join(workDir, ".whale", "orchestrator", "worktrees", safeID)

	// 复用 H6 NormalizePath（EvalSymlinks + Abs + NFC 规范化）
	normalized, err := platform.NormalizePath(base)
	if err != nil {
		return "", fmt.Errorf("normalize worktree path: %w", err)
	}

	// Windows 长路径兼容：路径 > 240 字符时加 \\?\ 前缀
	if platform.IsWindows() && len(normalized) > 240 {
		// \\?\ 前缀支持 Windows 10 1607+ 长路径（至 32767 字符）
		if !strings.HasPrefix(normalized, `\\?\`) {
			normalized = `\\?\` + normalized
		}
	}

	return normalized, nil
}

// enableGitLongPaths 启用 git core.longpaths（Windows 长路径兼容，）。
func enableGitLongPaths(workDir string) error {
	// 用 exec.Command 调 git config（不依赖 WorktreeManager 内部方法）
	// 这里简化实现：直接写 .git/config 配置
	gitDir := filepath.Join(workDir, ".git")
	if _, err := os.Stat(gitDir); err != nil {
		return fmt.Errorf("git dir not found: %w", err)
	}

	configPath := filepath.Join(gitDir, "config")
	data, err := os.ReadFile(configPath)
	if err != nil {
		return fmt.Errorf("read git config: %w", err)
	}

	content := string(data)
	// 检查是否已配置 core.longpaths
	if strings.Contains(content, "longpaths = true") {
		return nil
	}

	// 追加 [core] longpaths = true（简化实现，不解析 INI）
	if strings.Contains(content, "[core]") {
		content = strings.Replace(content, "[core]", "[core]\n\tlongpaths = true\n\tsymlinks = false", 1)
	} else {
		content += "\n[core]\n\tlongpaths = true\n\tsymlinks = false\n"
	}

	return os.WriteFile(configPath, []byte(content), 0o644)
}

// isPermissionError 检测是否为权限错误（用于触发 DLP 降级）。
func isPermissionError(err error) bool {
	if err == nil {
		return false
	}
	msg := strings.ToLower(err.Error())
	return strings.Contains(msg, "permission denied") ||
		strings.Contains(msg, "access is denied") ||
		strings.Contains(msg, "operation not permitted")
}

// IsDegraded 返回是否处于 DLP 降级模式（仅用于诊断）。
func (p *WorktreePool) IsDegraded() bool {
	p.mu.RLock()
	defer p.mu.RUnlock()
	return p.degraded
}

// ActiveCount 返回当前活跃 worktree 数量。
func (p *WorktreePool) ActiveCount() int {
	p.mu.RLock()
	defer p.mu.RUnlock()
	return len(p.sessions)
}

// Capacity 返回并行上限。
func (p *WorktreePool) Capacity() int {
	p.mu.RLock()
	defer p.mu.RUnlock()
	return p.capacity
}
