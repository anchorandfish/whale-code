package powershell

import (
	"os"
	"path/filepath"
	"testing"
)

// resolveLink 解析 symlink（macOS 上 t.TempDir() 位于 /var -> /private/var）
func resolveLink(t *testing.T, p string) string {
	t.Helper()
	if r, err := filepath.EvalSymlinks(p); err == nil {
		return r
	}
	return p
}

// TestPathValidator_SiblingDirWithCommonPrefix 判别器：
// 「工作目录之外的**同前缀兄弟目录**」必须被 Validate 拒绝。
//
// 收敛前实现为 `strings.HasPrefix(strings.ToLower(absPath), strings.ToLower(workDir))`
// —— 裸前缀、无分隔符边界 ⇒ `…/proj-secret/…` 以 `…/proj` 为前缀而被**放行** ⇒ 本用例红。
// （判别力自证：用 -overlay 还原旧实现后，本用例必须变红，见同批回执。）
func TestPathValidator_SiblingDirWithCommonPrefix(t *testing.T) {
	base := resolveLink(t, t.TempDir())
	work := filepath.Join(base, "proj")
	sibling := filepath.Join(base, "proj-secret")

	for _, d := range []string{work, sibling} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatalf("mkdir %s: %v", d, err)
		}
	}
	inside := filepath.Join(work, "a.txt")
	outside := filepath.Join(sibling, "creds.txt")
	for _, f := range []string{inside, outside} {
		if err := os.WriteFile(f, []byte("x"), 0o600); err != nil {
			t.Fatalf("write %s: %v", f, err)
		}
	}

	v := NewPathValidator(work)

	// ① 工作目录内 ⇒ 放行
	if err := v.Validate(inside); err != nil {
		t.Errorf("工作目录内的路径被误拒：%s: %v", inside, err)
	}
	// ② 同前缀兄弟目录 ⇒ **必须拒绝**（本判别器的靶心）
	if err := v.Validate(outside); err == nil {
		t.Errorf("★ 同前缀兄弟目录被放行：%s（workDir=%s）—— 裸前缀比较的后果，必须拒绝", outside, work)
	}
	// ③ 工作目录本身 ⇒ 放行（边界：等于 root）
	if err := v.Validate(work); err != nil {
		t.Errorf("工作目录本身被误拒：%s: %v", work, err)
	}
}
