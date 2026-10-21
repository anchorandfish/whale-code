package sandbox

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

// TestWritableRoots_TracksTempDir 是 T1 的**行为面判别器**：可写根必须是
// 「现场算出的事实」，不是一份写死的名单。
//
// 判别性：改临时目录环境变量 ⇒ 可写根集合必须跟着变。反例（打红）＝
// 把 `ExtraWritableRoots` 改成返回硬编码的 `[]string{"/tmp", ...}` ⇒
// 改变量后集合不变 ⇒ 红。
func TestWritableRoots_TracksTempDir(t *testing.T) {
	dirA := t.TempDir()
	dirB := t.TempDir()

	// 平台差异：os.TempDir() 在 Unix 只读 TMPDIR；Windows 读 TMP（其次 TEMP）。
	// 判别器必须改对变量，否则 Windows 上集合不变是「环境变量没生效」，
	// 不是实现问题。
	tmpEnv := "TMPDIR"
	if runtime.GOOS == "windows" {
		tmpEnv = "TMP"
	}

	t.Setenv(tmpEnv, dirA)
	rootsA := WritableRoots("/some/workspace")
	if !containsRoot(rootsA, dirA) {
		t.Fatalf("%s=%s 时，可写根应含该目录；实际 = %v", tmpEnv, dirA, rootsA)
	}

	t.Setenv(tmpEnv, dirB)
	rootsB := WritableRoots("/some/workspace")
	if !containsRoot(rootsB, dirB) {
		t.Fatalf("%s=%s 时，可写根应含该目录（说明可写根与 os.TempDir 同源）；实际 = %v", tmpEnv, dirB, rootsB)
	}
	if containsRoot(rootsB, dirA) {
		t.Fatalf("改 TMPDIR 后可写根仍含旧临时目录 %s ⇒ 集合不是同源派生的；实际 = %v", dirA, rootsB)
	}
}

// TestWritableRoots_IncludesWorkspace 钉住「工作区本身是主根」。
func TestWritableRoots_IncludesWorkspace(t *testing.T) {
	ws := t.TempDir()
	roots := WritableRoots(ws)
	resolved, err := resolveSandboxPath(ws)
	if err != nil {
		t.Fatalf("resolve workspace: %v", err)
	}
	if !containsRoot(roots, resolved) {
		t.Fatalf("可写根应含工作区 %s；实际 = %v", resolved, roots)
	}
}

// TestWritableRoots_Dedupes 钉住去重（工作区恰好等于临时目录时不重复）。
func TestWritableRoots_Dedupes(t *testing.T) {
	ws := t.TempDir()
	t.Setenv("TMPDIR", ws)
	roots := WritableRoots(ws)
	seen := map[string]int{}
	for _, r := range roots {
		seen[r]++
		if seen[r] > 1 {
			t.Fatalf("可写根出现重复项 %q；实际 = %v", r, roots)
		}
	}
}

// TestRootsReason_EmptyIsUnknown 钉住空集合的措辞（不得渲染成"空列表"这种
// 看起来像"没有边界"的形态）。
func TestRootsReason_EmptyIsUnknown(t *testing.T) {
	if got := RootsReason(nil); got != "writable roots: unknown" {
		t.Fatalf("RootsReason(nil) = %q, want %q", got, "writable roots: unknown")
	}
}

func containsRoot(roots []string, want string) bool {
	wantResolved, err := filepath.EvalSymlinks(want)
	if err != nil {
		wantResolved = want
	}
	wantInfo, wantErr := os.Stat(want)
	for _, r := range roots {
		if r == want || r == wantResolved {
			return true
		}
		// symlink 解析粒度差异（/var/folders → /private/var/folders）下按 inode 兜底。
		if wantErr == nil {
			if ri, err := os.Stat(r); err == nil && os.SameFile(ri, wantInfo) {
				return true
			}
		}
	}
	return false
}
