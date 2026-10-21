package filehistory

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/anchorandfish/whale-code/internal/permission"
	"github.com/anchorandfish/whale-code/internal/platform"
)

// resolveAll 解析 symlink（macOS 上 t.TempDir() 位于 /var，是 /private/var 的符号链接）
func resolveAll(t *testing.T, p string) string {
	t.Helper()
	r, err := filepath.EvalSymlinks(p)
	if err != nil {
		t.Fatalf("EvalSymlinks(%q): %v", p, err)
	}
	return r
}

// TestPathContainment_Convergence 是「同一语义多处实现」这类问题的**永久判别器（收敛门）**。
//
// 它把**同一批输入**同时喂给三个各自实现的「路径是否在工作目录之内」：
//
//	platform.IsPathAllowed        权威实现（前缀 + 分隔符边界，fail-closed）
//	filehistory.safePath          曾自行实现（裸前缀 + 无条件 ToLower）——  2026-09-19 已收敛
//	permission.CheckPathSafety    曾自行实现（filepath.Rel + HasPrefix(rel,"..")）
//
// 判别力：三者若语义一致 ⇒ 绿；任一格分歧 ⇒ 红，且日志直接指出是哪一实现、哪个输入不一致。
//
// 历史：2026-09-19 收敛前，本用例在 `proj-secret`（与 `proj` 同前缀的兄弟目录）上红 ——
// safePath 用裸前缀把它判为"在内"。收敛后三者共用 platform 的 SSOT（path_contain.go）。
// 若将来有人再写第四套实现且语义不同，本用例会再次变红。
func TestPathContainment_Convergence(t *testing.T) {
	base := resolveAll(t, t.TempDir())

	work := filepath.Join(base, "proj")
	sibling := filepath.Join(base, "proj-secret") // 与 work 同前缀的兄弟目录
	inside := filepath.Join(work, "src")
	dotted := filepath.Join(work, "..foo") // 合法目录名，仅以 ".." 开头（**不是**穿越）

	for _, d := range []string{work, sibling, inside, dotted} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatalf("mkdir %s: %v", d, err)
		}
	}
	write := func(p, body string) {
		t.Helper()
		if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
			t.Fatalf("write %s: %v", p, err)
		}
	}
	write(filepath.Join(sibling, "creds.txt"), "SECRET")
	write(filepath.Join(inside, "a.go"), "package main")
	write(filepath.Join(dotted, "b.go"), "package main")
	write(filepath.Join(base, "outer.txt"), "OUTSIDE")

	h := &FileHistory{workDir: work}

	// 只比较**已存在**的路径 —— 不存在路径上三者的策略本就不同（fail-closed vs 允许新文件），
	// 那属**有意的策略差异**，由 TestPathContainment_MissingPathPolicy 单独断言，不算分歧。
	cases := []struct {
		name   string
		path   string
		inside bool
	}{
		{"workDir 本身", work, true},
		{"workDir 内文件", filepath.Join(inside, "a.go"), true},
		{"内层以 .. 开头的合法目录", filepath.Join(dotted, "b.go"), true},
		{"同前缀兄弟目录的文件", filepath.Join(sibling, "creds.txt"), false},
		{"同前缀兄弟目录本身", sibling, false},
		{"上跳一层", filepath.Join(base, "outer.txt"), false},
	}

	t.Logf("workDir = %s", work)
	diverged := 0
	for _, c := range cases {
		authz := platform.IsPathAllowed(c.path, []string{work})
		_, err := h.safePath(c.path)
		safe := err == nil
		perm := permission.CheckPathSafety(c.path, work)
		permInside := perm == nil || !perm.IsProtected

		switch {
		case authz != safe || authz != permInside:
			diverged++
			t.Errorf("%s: ★分歧 —— IsPathAllowed=%v safePath=%v CheckPathSafety(在内)=%v  path=%s",
				c.name, authz, safe, permInside, c.path)
		case authz != c.inside:
			diverged++
			t.Errorf("%s: 三个实现一致但**结论本身错** —— 实际=%v 期望=%v  path=%s",
				c.name, authz, c.inside, c.path)
		default:
			t.Logf("%-22s 三者一致=%v  %s", c.name, authz, c.path)
		}
	}
	if diverged > 0 {
		t.Errorf("「路径是否在工作目录内」有 %d 处不一致 ⇒ 同一语义不得有多套逻辑，必须收敛到单一 SSOT", diverged)
	}
}

// TestPathContainment_MissingPathPolicy 断言"路径尚不存在"时各入口的**有意策略差异**：
// 这是设计选择（安全门宁拒勿放；新文件写入必须能过），不是分歧 —— 故单列，避免被上一条误判为收敛失败。
func TestPathContainment_MissingPathPolicy(t *testing.T) {
	base := resolveAll(t, t.TempDir())
	work := filepath.Join(base, "proj")
	if err := os.MkdirAll(work, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	newFile := filepath.Join(work, "not-created-yet.txt")

	// 安全门：不存在 ⇒ fail-closed
	if platform.IsPathAllowed(newFile, []string{work}) {
		t.Errorf("IsPathAllowed 对不存在的路径应为 false（fail-closed），实际 true")
	}

	// 新文件写入场景：必须能解析出来（否则新建文件被 workflow 拒绝）
	resolved, err := platform.ResolveWithin(newFile, []string{work})
	if err != nil {
		t.Errorf("ResolveWithin 对「尚未创建但父目录在内部」的路径应放行（新文件语义），实际报错: %v", err)
	} else if resolved != newFile {
		t.Errorf("ResolveWithin 解析结果 = %q，期望 %q", resolved, newFile)
	}

	// 新文件但父目录在外部 ⇒ 仍须拒绝（不能因"不存在"就放行任何路径）
	outsideNew := filepath.Join(base, "elsewhere", "new.txt")
	if _, err := platform.ResolveWithin(outsideNew, []string{work}); err == nil {
		t.Errorf("ResolveWithin 对「父目录在工作目录之外」的新路径应拒绝，实际放行")
	}
}
