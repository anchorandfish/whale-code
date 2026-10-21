package sandbox

// 相对路径重定向越界写测试（R4 修复）。
//
// 漏洞背景：legacy Sandbox.Validate 的写重定向边界检查（sandbox.go ~line 205）
// 只对"以 / 或 X: 开头"的目标路径做边界检查（HasPrefix("/") 守卫）。
// 相对路径目标如 `../../escaped.txt` 不满足该前缀 → 整个边界检查被跳过 → 越界写绕过。
//
// 生产可达：bash 工具 → prepareSandbox(bash.go:975) → e.Sandbox.Validate(command)
// （executor.go:232 Sandbox 恒非 nil；lifecycle.go:435 SetEnabled(true) 生产启用）
//
// 修复前这些用例 RED（相对路径越界写被放行）；修复后 GREEN。
//
// 注：使用 t.TempDir() 作为真实 workdir 基准，避免 macOS 下 /tmp -> /private/tmp
// 软链接导致 resolvePath 把绝对/相对路径解析出不同基准的歧义。

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"
)

// newWorkDirSandbox 创建一个以真实临时子目录为 workDir 的 legacy sandbox，
// 并返回 workDir 与越界目标目录（workDir 的父目录）。
func newWorkDirSandbox(t *testing.T) (*Sandbox, string, string) {
	t.Helper()
	base := t.TempDir() // e.g. /var/folders/.../TestX/001
	workDir := filepath.Join(base, "whale-ws")
	// 生产里工作目录必然存在；创建它使 resolvePath 的符号链接展开一致，
	// 避免 macOS /var->/private/var 软链接在不存在的父目录上产生解析歧义。
	if err := os.MkdirAll(workDir, 0o755); err != nil {
		t.Fatalf("mkdir workdir: %v", err)
	}
	outside := filepath.Dir(workDir) // workDir 的父 = base
	s := NewSandbox(workDir)
	s.SetEnabled(true) // 与生产 lifecycle.go:435 一致，启用 legacy 沙箱
	return s, workDir, outside
}

// relUp 构造从 workDir（shell cwd）相对上跳 n 层的逃逸目标路径。
func relUp(n int, name string) string {
	rel := ""
	for i := 0; i < n; i++ {
		rel += "../"
	}
	return rel + name
}

// TestRedirect_RelativeTraversalOutsideWorkDir
// 相对路径穿越（../）写目标逃出 workDir，应被拒绝。这是本漏洞的核心。
func TestRedirect_RelativeTraversalOutsideWorkDir(t *testing.T) {
	s, _, _ := newWorkDirSandbox(t)

	attacks := []struct {
		name    string
		command string
	}{
		{"single_up_outside", fmt.Sprintf("echo x > %s", relUp(1, "escaped.txt"))},
		{"double_up_outside", fmt.Sprintf("echo x > %s", relUp(2, "escaped.txt"))},
		{"deep_traversal", fmt.Sprintf("echo x > %s", relUp(4, "etc/evil"))},
		{"append_traversal", fmt.Sprintf("echo x >> %s", relUp(1, "escaped.log"))},
		{"fd2_traversal", fmt.Sprintf("echo x 2> %s", relUp(1, "err.log"))},
	}

	for _, atk := range attacks {
		t.Run(atk.name, func(t *testing.T) {
			allowed, reason := s.Validate(atk.command)
			if allowed {
				t.Errorf("relative traversal redirect was NOT blocked (sandbox bypass)\n"+
					"  command: %s\n  expected: blocked", atk.command)
			}
			if reason == "" {
				t.Errorf("relative traversal redirect blocked but no reason given: %s", atk.command)
			}
		})
	}
}

// TestRedirect_InsideWorkDirStillAllowed
// 工作目录内写必须继续放行（相对与绝对路径都不误伤）。
func TestRedirect_InsideWorkDirStillAllowed(t *testing.T) {
	s, workDir, _ := newWorkDirSandbox(t)

	allowed := []string{
		"echo x > local.txt",        // 裸相对，落在 workDir
		"echo x > ./local.txt",      // ./ 相对
		"echo x > sub/dir/file.txt", // 子目录相对
		fmt.Sprintf("echo x > %s", filepath.Join(workDir, "out.txt")),  // workDir 内绝对
		fmt.Sprintf("echo x >> %s", filepath.Join(workDir, "log.txt")), // workDir 内绝对追加
	}

	for _, cmd := range allowed {
		ok, reason := s.Validate(cmd)
		if !ok {
			t.Errorf("in-workdir write %q should be allowed, got blocked: %s", cmd, reason)
		}
	}
}

// TestRedirect_AbsoluteOutsideStillBlocked
// 绝对路径越界写仍被拒绝（回归保护，确认修复未破坏原行为）。
func TestRedirect_AbsoluteOutsideStillBlocked(t *testing.T) {
	s, _, _ := newWorkDirSandbox(t)

	blocked := []string{
		"echo x > /etc/passwd",
		"cat f > /root/.ssh/authorized_keys",
		"echo x > /etc/cron.d/evil 2>&1",
	}

	for _, cmd := range blocked {
		ok, _ := s.Validate(cmd)
		if ok {
			t.Errorf("absolute outside write %q should be blocked", cmd)
		}
	}
}

// TestRedirect_WorkDirJoinedRelative
// 防止实现把相对路径错误地以进程 cwd（而非 shell workDir）解析导致误判。
func TestRedirect_WorkDirJoinedRelative(t *testing.T) {
	s, _, _ := newWorkDirSandbox(t)

	if ok, _ := s.Validate("echo x > inner.txt"); !ok {
		t.Error("inner relative write should be allowed")
	}
	if ok, _ := s.Validate("echo x > ../../../pwned"); ok {
		t.Error("deep traversal beyond workdir should be blocked")
	}
}
