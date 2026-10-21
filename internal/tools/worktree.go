package tools

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/anchorandfish/whale-code/internal/util"
)

// WorktreeSession records the worktree state of the current session
type WorktreeSession struct {
	// OriginalCwd is the working directory before entering worktree (absolute path)
	OriginalCwd string `json:"original_cwd"`
	// WorktreePath is the filesystem path of the worktree (absolute path)
	WorktreePath string `json:"worktree_path"`
	// WorktreeName is the name of the worktree (slug format)
	WorktreeName string `json:"worktree_name"`
	// WorktreeBranch is the branch name associated with the worktree
	WorktreeBranch string `json:"worktree_branch,omitempty"`
	// OriginalBranch is the branch name before entering worktree
	OriginalBranch string `json:"original_branch,omitempty"`
	// OriginalHeadCommit is the HEAD commit SHA before entering worktree
	OriginalHeadCommit string `json:"original_head_commit,omitempty"`
	// SessionID is the associated session ID
	SessionID string `json:"session_id"`
}

// ChangeSummary is the change summary in a worktree
type ChangeSummary struct {
	ChangedFiles int `json:"changed_files"`
	Commits      int `json:"commits"`
}

// slugRegex is the regex for worktree name validity
var slugRegex = regexp.MustCompile(`^[a-zA-Z0-9._/-]+$`)

// ValidateWorktreeSlug validates the legality of a worktree name
// Name can only contain letters, digits, dots, underscores, hyphens, and slashes, max 64 characters
func ValidateWorktreeSlug(name string) error {
	if name == "" {
		return nil // empty name is valid, will auto-generate
	}
	if len(name) > 64 {
		return fmt.Errorf("worktree name too long (max 64 characters, got %d)", len(name))
	}
	if !slugRegex.MatchString(name) {
		return fmt.Errorf("worktree name contains illegal characters, only letters, digits, dots, underscores, hyphens, and slashes are allowed")
	}
	// Prevent path traversal
	if strings.Contains(name, "..") {
		return fmt.Errorf("worktree name cannot contain '..'")
	}
	// Prevent leading slash
	if strings.HasPrefix(name, "/") {
		return fmt.Errorf("worktree name cannot start with a slash")
	}
	return nil
}

// WorktreeBranchName generates a branch name from the worktree name
func WorktreeBranchName(name string) string {
	return "worktree-" + name
}

// GenerateRandomSlug generates a random worktree name
func GenerateRandomSlug() string {
	// Generate unique slug using nanosecond timestamp + crypto/rand random bytes
	ts := time.Now().UnixNano() % 100000000
	b := make([]byte, 4)
	_, _ = rand.Read(b)
	return fmt.Sprintf("wt-%08d-%x", ts, b)
}

// WorktreeManager manages the worktree lifecycle
type WorktreeManager struct {
	mu      sync.RWMutex
	session *WorktreeSession // current active worktree session (nil means not in worktree)
	workDir string           // original working directory
}

// NewWorktreeManager creates a WorktreeManager instance
func NewWorktreeManager(workDir string) *WorktreeManager {
	return &WorktreeManager{
		workDir: workDir,
	}
}

// GetCurrentSession returns the current worktree session
func (m *WorktreeManager) GetCurrentSession() *WorktreeSession {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.session
}

// SetSession sets the current worktree session
func (m *WorktreeManager) SetSession(session *WorktreeSession) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.session = session
}

// ClearSession clears the current worktree session
func (m *WorktreeManager) ClearSession() {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.session = nil
}

// IsInWorktree checks if currently in a worktree session
func (m *WorktreeManager) IsInWorktree() bool {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.session != nil
}

// GetWorkDir returns the current working directory (returns worktree path if in worktree)
func (m *WorktreeManager) GetWorkDir() string {
	m.mu.RLock()
	defer m.mu.RUnlock()
	if m.session != nil {
		return m.session.WorktreePath
	}
	return m.workDir
}

// GetOriginalWorkDir returns the original working directory
func (m *WorktreeManager) GetOriginalWorkDir() string {
	return m.workDir
}

// EnterWorktree 创建一个 worktree 并切换当前会话到该 worktree
func (m *WorktreeManager) EnterWorktree(ctx context.Context, name, sessionID string) (*WorktreeSession, error) {
	// Check if already in a worktree
	if m.IsInWorktree() {
		return nil, fmt.Errorf("%s", util.Bi("Already in a worktree session; exit the current worktree first", "已在 worktree 会话中，请先退出当前 worktree"))
	}

	// Verify Git repository
	helper := NewGitHelper(m.workDir)
	if !helper.IsGitRepo(ctx) {
		return nil, fmt.Errorf("%s", util.Bi("Not a Git repository: the current directory is not inside a Git work tree", "当前目录不是 Git 仓库"))
	}

	// Process name
	if name == "" {
		name = GenerateRandomSlug()
	}
	if err := ValidateWorktreeSlug(name); err != nil {
		return nil, fmt.Errorf("%s: %w", util.Bi("Worktree name validation failed", "worktree 名称验证失败"), err)
	}
	if strings.ContainsAny(name, "/\\") {
		return nil, fmt.Errorf("%s", util.Bi("Worktree name cannot contain path separators", "worktree 名称不能包含路径分隔符"))
	}

	// Get current branch and HEAD commit
	originalBranch, _ := helper.CurrentBranch(ctx)
	originalHeadCommit, _ := m.getHeadCommit(ctx, m.workDir)

	// Create .whale/worktrees directory
	worktreesDir := filepath.Join(m.workDir, ".whale", "worktrees")
	if err := os.MkdirAll(worktreesDir, 0755); err != nil {
		return nil, fmt.Errorf("failed to create worktrees directory: %w", err)
	}

	// worktree path
	worktreePath := filepath.Join(worktreesDir, name)
	branchName := WorktreeBranchName(name)

	// Check if target path already exists
	if _, err := os.Stat(worktreePath); err == nil {
		return nil, fmt.Errorf("worktree path already exists: %s", worktreePath)
	}

	// Create git worktree
	output, err := helper.Run(ctx, "worktree", "add", worktreePath, "-b", branchName, "HEAD")
	if err != nil {
		return nil, fmt.Errorf("failed to create worktree: %w\n%s", err, output)
	}

	// Build session
	session := &WorktreeSession{
		OriginalCwd:        m.workDir,
		WorktreePath:       worktreePath,
		WorktreeName:       name,
		WorktreeBranch:     branchName,
		OriginalBranch:     originalBranch,
		OriginalHeadCommit: originalHeadCommit,
		SessionID:          sessionID,
	}

	m.SetSession(session)
	return session, nil
}

// ExitWorktree 退出当前 worktree 会话
// action: "keep" 保留 worktree，"remove" 删除 worktree
// discardChanges: 当 action=remove 且存在未提交变更时，必须为 true
func (m *WorktreeManager) ExitWorktree(ctx context.Context, action string, discardChanges bool) (*WorktreeSession, *ChangeSummary, error) {
	session := m.GetCurrentSession()
	if session == nil {
		return nil, nil, fmt.Errorf("%s", util.Bi("Not in a worktree session; nothing to exit", "不在 worktree 会话中，无需退出"))
	}

	// Count changes
	summary, err := m.CountWorktreeChanges(ctx)
	if err != nil {
		// Cannot determine status, disallow deletion for safety
		if action == "remove" && !discardChanges {
			return nil, nil, fmt.Errorf("%s", util.BiSame("Cannot determine worktree status (%v); set discard_changes=true to force removal", "无法确认 worktree 状态（%v），设置 discard_changes=true 以强制删除", err))
		}
		summary = &ChangeSummary{}
	}

	// Safety check: must confirm when there are changes in remove mode
	if action == "remove" && !discardChanges {
		if summary.ChangedFiles > 0 || summary.Commits > 0 {
			return session, summary, fmt.Errorf("%s", util.BiSame(
				"worktree has %d uncommitted file(s) and %d unmerged commit(s); removing it will permanently discard this work. Set discard_changes=true to confirm, or use action=\"keep\" to keep the worktree",
				"worktree 有 %d 个未提交文件和 %d 个未合并提交，删除将永久丢弃此工作。设置 discard_changes=true 以确认，或使用 action=\"keep\" 保留 worktree",
				summary.ChangedFiles, summary.Commits,
			))
		}
	}

	// Execute action
	if action == "remove" {
		helper := NewGitHelper(m.workDir)
		output, err := helper.Run(ctx, "worktree", "remove", session.WorktreePath, "--force")
		if err != nil {
			return nil, nil, fmt.Errorf("failed to delete worktree: %w\n%s", err, output)
		}
	}

	// Clear session
	m.ClearSession()

	return session, summary, nil
}

// CountWorktreeChanges counts changes in the worktree
func (m *WorktreeManager) CountWorktreeChanges(ctx context.Context) (*ChangeSummary, error) {
	session := m.GetCurrentSession()
	if session == nil {
		return nil, fmt.Errorf("not in a worktree session")
	}

	summary := &ChangeSummary{}

	// Count uncommitted files
	helper := NewGitHelper(session.WorktreePath)
	status, err := helper.Status(ctx)
	if err != nil {
		return nil, fmt.Errorf("failed to get worktree status: %w", err)
	}
	if status != "" {
		lines := strings.Split(status, "\n")
		for _, line := range lines {
			if strings.TrimSpace(line) != "" {
				summary.ChangedFiles++
			}
		}
	}

	// Count unmerged commits
	if session.OriginalHeadCommit != "" {
		output, err := helper.Run(ctx, "rev-list", "--count", session.OriginalHeadCommit+"..HEAD")
		if err == nil {
			if n := strings.TrimSpace(output); n != "" {
				var count int
				fmt.Sscanf(n, "%d", &count)
				summary.Commits = count
			}
		}
	}

	return summary, nil
}

// getHeadCommit 获取指定目录的 HEAD 提交 SHA。
//
// C-P1 修复：改用 GitHelper.Run 统一接入 S-2（env 白名单过滤）+ S-3（hooks 禁用）安全加固，
// 不再直接 exec.CommandContext 绕过环境变量过滤。
//
// 参数：
//   - ctx: 上下文
//   - dir: git 仓库目录（可能与 m.workDir 不同，如 worktree 子目录）
//
// 返回：trim 后的 HEAD commit SHA，以及执行错误
func (m *WorktreeManager) getHeadCommit(ctx context.Context, dir string) (string, error) {
	helper := NewGitHelper(dir)
	return helper.Run(ctx, "rev-parse", "HEAD")
}

// ListWorktrees lists all worktrees
func (m *WorktreeManager) ListWorktrees(ctx context.Context) (string, error) {
	helper := NewGitHelper(m.workDir)
	if !helper.IsGitRepo(ctx) {
		return "", fmt.Errorf("current directory is not a Git repository")
	}
	return helper.Run(ctx, "worktree", "list")
}

// AddWorktree manually adds a worktree (for slash commands)
func (m *WorktreeManager) AddWorktree(ctx context.Context, path, branch string) (string, error) {
	helper := NewGitHelper(m.workDir)
	if !helper.IsGitRepo(ctx) {
		return "", fmt.Errorf("current directory is not a Git repository")
	}

	args := []string{"worktree", "add", path}
	if branch != "" {
		args = append(args, "-b", branch)
	}
	args = append(args, "HEAD")

	return helper.Run(ctx, args...)
}

// RemoveWorktree manually removes a worktree (for slash commands)
func (m *WorktreeManager) RemoveWorktree(ctx context.Context, path string) (string, error) {
	helper := NewGitHelper(m.workDir)
	if !helper.IsGitRepo(ctx) {
		return "", fmt.Errorf("current directory is not a Git repository")
	}
	return helper.Run(ctx, "worktree", "remove", path)
}

// PruneWorktrees prunes stale worktree references and cleans up orphaned directories
func (m *WorktreeManager) PruneWorktrees(ctx context.Context) (string, error) {
	helper := NewGitHelper(m.workDir)
	if !helper.IsGitRepo(ctx) {
		return "", fmt.Errorf("current directory is not a Git repository")
	}

	result, err := helper.Run(ctx, "worktree", "prune")
	if err != nil {
		return "", fmt.Errorf("failed to prune worktrees: %w", err)
	}

	worktreesDir := filepath.Join(m.workDir, ".whale", "worktrees")
	info, err := os.Stat(worktreesDir)
	if err != nil || !info.IsDir() {
		return result, nil
	}

	entries, err := os.ReadDir(worktreesDir)
	if err != nil {
		return result, nil
	}

	activeWorktrees, _ := helper.Run(ctx, "worktree", "list", "--porcelain")
	cleaned := 0
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		dirPath := filepath.Join(worktreesDir, entry.Name())
		if !strings.Contains(activeWorktrees, dirPath) {
			os.RemoveAll(dirPath)
			cleaned++
		}
	}

	if cleaned > 0 {
		return fmt.Sprintf("%s\nCleaned %d orphaned worktree directories", result, cleaned), nil
	}
	return result, nil
}

// --- Tool Handlers ---

// getWorktreeManager returns the per-instance worktree manager, lazily initialized
func (e *Executor) getWorktreeManager(workDir string) *WorktreeManager {
	e.worktreeManagerOnce.Do(func() {
		e.worktreeManager = NewWorktreeManager(workDir)
	})
	return e.worktreeManager
}

// enterWorktreeHandler 是 enter_worktree 工具的处理器，负责创建 worktree 并切换会话
func (e *Executor) enterWorktreeHandler(workDir string) Handler {
	return func(ctx context.Context, raw json.RawMessage) (string, error) {
		var input struct {
			Name string `json:"name"`
		}
		// name is optional, empty input is valid
		if len(raw) > 0 {
			_ = json.Unmarshal(raw, &input)
		}

		wm := e.getWorktreeManager(workDir)

		session, err := wm.EnterWorktree(ctx, input.Name, "")
		if err != nil {
			return "", err
		}

		branchInfoEN, branchInfoZH := "", ""
		if session.WorktreeBranch != "" {
			branchInfoEN = fmt.Sprintf(" (branch %s)", session.WorktreeBranch)
			branchInfoZH = fmt.Sprintf("(分支 %s)", session.WorktreeBranch)
		}

		return util.Bi(
			fmt.Sprintf("Created worktree at %s%s. Session switched to the worktree. Use exit_worktree to exit.", session.WorktreePath, branchInfoEN),
			fmt.Sprintf("已在 %s%s 创建 worktree。会话已切换到 worktree。使用 exit_worktree 退出。", session.WorktreePath, branchInfoZH),
		), nil
	}
}

// exitWorktreeHandler 是 exit_worktree 工具的处理器，负责退出 worktree 会话并按需保留或删除 worktree
func (e *Executor) exitWorktreeHandler(workDir string) Handler {
	return func(ctx context.Context, raw json.RawMessage) (string, error) {
		var input struct {
			Action         string `json:"action"`
			DiscardChanges bool   `json:"discard_changes"`
		}
		// action is required, but compatible with empty input
		if len(raw) > 0 {
			_ = json.Unmarshal(raw, &input)
		}

		if input.Action == "" {
			return "", fmt.Errorf("%s", util.Bi("action cannot be empty", "action 参数不能为空"))
		}
		if input.Action != "keep" && input.Action != "remove" {
			return "", fmt.Errorf("%s", util.BiSame("action must be \"keep\" or \"remove\", got: %q", "action 必须为 \"keep\" 或 \"remove\"，得到：%q", input.Action))
		}

		wm := e.getWorktreeManager(workDir)

		session, summary, err := wm.ExitWorktree(ctx, input.Action, input.DiscardChanges)
		if err != nil {
			// If safety rejection (has changes but not confirmed), return detailed error
			if summary != nil && (summary.ChangedFiles > 0 || summary.Commits > 0) {
				return "", err
			}
			return "", err
		}

		if input.Action == "keep" {
			branchInfoEN, branchInfoZH := "", ""
			if session.WorktreeBranch != "" {
				branchInfoEN = fmt.Sprintf(" (branch %s)", session.WorktreeBranch)
				branchInfoZH = fmt.Sprintf("(分支 %s)", session.WorktreeBranch)
			}
			return util.Bi(
				fmt.Sprintf("Exited worktree. Work kept at %s%s. Session returned to %s.", session.WorktreePath, branchInfoEN, session.OriginalCwd),
				fmt.Sprintf("已退出 worktree。工作保留在 %s%s。会话返回到 %s。", session.WorktreePath, branchInfoZH, session.OriginalCwd),
			), nil
		}

		// action == "remove"
		enParts, zhParts := []string{}, []string{}
		if summary != nil {
			if summary.Commits > 0 {
				enParts = append(enParts, fmt.Sprintf("%d commit(s)", summary.Commits))
				zhParts = append(zhParts, fmt.Sprintf("%d 个提交", summary.Commits))
			}
			if summary.ChangedFiles > 0 {
				enParts = append(enParts, fmt.Sprintf("%d uncommitted file(s)", summary.ChangedFiles))
				zhParts = append(zhParts, fmt.Sprintf("%d 个未提交文件", summary.ChangedFiles))
			}
		}
		en := fmt.Sprintf("Exited and removed worktree (%s).", session.WorktreePath)
		zh := fmt.Sprintf("已退出并删除 worktree (%s)。", session.WorktreePath)
		if len(enParts) > 0 {
			en += fmt.Sprintf(" Discarded %s.", strings.Join(enParts, " and "))
			zh += fmt.Sprintf(" 已丢弃 %s。", strings.Join(zhParts, " 和 "))
		}
		en += fmt.Sprintf(" Session returned to %s.", session.OriginalCwd)
		zh += fmt.Sprintf(" 会话返回到 %s。", session.OriginalCwd)
		return util.Bi(en, zh), nil
	}
}
