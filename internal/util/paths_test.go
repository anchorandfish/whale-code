package util

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

// TestPathsEqual_SamePath 相同路径返回 true
func TestPathsEqual_SamePath(t *testing.T) {
	if !PathsEqual("/tmp/foo", "/tmp/foo") {
		t.Error("PathsEqual(相同路径) = false, want true")
	}
}

// TestPathsEqual_RelativeAndAbsolute 相对路径与绝对路径等价
func TestPathsEqual_RelativeAndAbsolute(t *testing.T) {
	wd, _ := os.Getwd()
	abs := filepath.Join(wd, "foo.txt")
	rel := "foo.txt"
	if !PathsEqual(abs, rel) {
		t.Errorf("PathsEqual(%q, %q) = false, want true", abs, rel)
	}
}

// TestPathsEqual_CaseInsensitive Windows/macOS 大小写不敏感
func TestPathsEqual_CaseInsensitive(t *testing.T) {
	if runtime.GOOS != "windows" && runtime.GOOS != "darwin" {
		t.Skip("仅 Windows/macOS 文件系统大小写不敏感")
	}
	p1 := filepath.Join(t.TempDir(), "Foo.txt")
	p2 := filepath.Join(filepath.Dir(p1), "foo.txt")
	// 注意：在 Windows/macOS 上 Abs+Clean+ToLower 后应等价
	if !PathsEqual(p1, p2) {
		t.Errorf("PathsEqual(%q, %q) = false, want true（大小写不敏感）", p1, p2)
	}
}

// TestPathsEqual_Symlink symlink 解析后等价返回 true
func TestPathsEqual_Symlink(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Windows symlink 创建需管理员权限，跳过")
	}
	dir := t.TempDir()
	realPath := filepath.Join(dir, "real.txt")
	if err := os.WriteFile(realPath, []byte("x"), 0644); err != nil {
		t.Fatal(err)
	}
	linkPath := filepath.Join(dir, "link.txt")
	if err := os.Symlink(realPath, linkPath); err != nil {
		t.Skipf("创建 symlink 失败（可能无权限）: %v", err)
	}
	if !PathsEqual(realPath, linkPath) {
		t.Errorf("PathsEqual(%q, %q) = false, want true（symlink 解析后等价）", realPath, linkPath)
	}
}

// TestPathsEqual_Different 不同路径返回 false
func TestPathsEqual_Different(t *testing.T) {
	if PathsEqual("/tmp/foo", "/tmp/bar") {
		t.Error("PathsEqual(不同路径) = true, want false")
	}
}

// TestPathsEqual_EvalSymlinksFailure 文件不存在时仍能比较 Abs 路径
func TestPathsEqual_EvalSymlinksFailure(t *testing.T) {
	dir := t.TempDir()
	p1 := filepath.Join(dir, "nonexistent.txt")
	p2 := filepath.Join(dir, "nonexistent.txt")
	// 文件不存在，EvalSymlinks 会失败，但 Abs+Clean 应仍等价
	if !PathsEqual(p1, p2) {
		t.Errorf("PathsEqual(同一路径，文件不存在) = false, want true")
	}
}

// TestPathsEqual_DotDot 路径含 .. 经 Clean 后等价
func TestPathsEqual_DotDot(t *testing.T) {
	dir := t.TempDir()
	p1 := filepath.Join(dir, "foo", "bar.txt")
	p2 := filepath.Join(dir, "foo", "sub", "..", "bar.txt")
	if !PathsEqual(p1, p2) {
		t.Errorf("PathsEqual(%q, %q) = false, want true（.. 经 Clean 后等价）", p1, p2)
	}
}
