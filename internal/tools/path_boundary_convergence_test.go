package tools

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/anchorandfish/whale-code/internal/platform"
)

// TestWorkspaceBoundary_Convergence 是「路径包含判定」交叉领域在 **tools 侧**的永久门。
//
// 收敛前 tools/pipeline.go 是本仓最后一处「自建 resolver + filepath.Rel/HasPrefix」
// 实现（第 6 套）。本门锁住两件事：
//
//  1. **边界正确性**：同前缀兄弟目录必须判 Outside；目录内合法名 `..foo` 必须判 Inside。
//  2. **机制同源**：`checkWorkspaceBoundary` 的结论必须与 `platform.IsWithinResolved`
//     在同样的规范化输入上**逐条一致** —— 若将来有人把它改回自建比较，本门变红。
//
// ★ 判别力自证（R4）：本门针对收敛前的 pipeline.go 为**红**
// （`-overlay` 还原旧实现可复现，见回执）。判据是**行为**判据（调函数），
// 故 `-overlay` 只改编译期输入即有效。
func TestWorkspaceBoundary_Convergence(t *testing.T) {
	tmp := t.TempDir()
	proj := filepath.Join(tmp, "proj")
	projSecret := filepath.Join(tmp, "proj-secret")

	if err := os.MkdirAll(filepath.Join(proj, "..foo"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(projSecret, 0o755); err != nil {
		t.Fatal(err)
	}

	cases := []struct {
		name    string
		path    string
		workDir string
		want    BoundaryVerdict
	}{
		// —— 基本：内部 / 外部 ——
		{"workDir 本身", proj, proj, BoundaryInside},
		{"内部已有文件", filepath.Join(proj, "a.txt"), proj, BoundaryInside},
		{"内部不存在的新文件", filepath.Join(proj, "new", "deep", "b.go"), proj, BoundaryInside},
		{"同级无关目录", filepath.Join(tmp, "other", "a.txt"), proj, BoundaryOutside},

		// —— ★ 同前缀兄弟目录（裸前缀实现会误放行）——
		{"同前缀兄弟目录", filepath.Join(projSecret, "creds.txt"), proj, BoundaryOutside},
		{"同前缀兄弟目录（无子路径）", projSecret, proj, BoundaryOutside},

		// —— ★ 目录内合法名以 .. 开头（Rel 实现会误拒）——
		{"内部 ..foo 目录", filepath.Join(proj, "..foo", "b.go"), proj, BoundaryInside},

		// —— 穿越 ——
		{"显式 .. 穿越", filepath.Join(proj, "..", "outside", "x.txt"), proj, BoundaryOutside},

		// —— 未知 ——
		{"workDir 为空", filepath.Join(proj, "a.txt"), "", BoundaryUnknown},
	}

	for _, c := range cases {
		got, reason := checkWorkspaceBoundary(c.path, c.workDir)
		if got != c.want {
			t.Errorf("checkWorkspaceBoundary(%q, %q) = %v（%s），期望 %v",
				c.path, c.workDir, got, reason, c.want)
			continue
		}

		// 机制同源断言：与 platform 的唯一包含判定在同样的规范化输入上必须一致。
		if c.workDir == "" {
			continue
		}
		absPath, err := resolveValidationPath(c.path)
		if err != nil {
			t.Fatalf("resolveValidationPath(%q): %v", c.path, err)
		}
		absWork, err := resolveValidationPath(c.workDir)
		if err != nil {
			t.Fatalf("resolveValidationPath(%q): %v", c.workDir, err)
		}
		inPlatform := platform.IsWithinResolved(absPath, absWork)
		fromVerdict := got == BoundaryInside
		if inPlatform != fromVerdict {
			t.Errorf("机制未同源：%q vs %q\n  platform.IsWithinResolved=%v\n  checkWorkspaceBoundary==Inside=%v",
				c.path, c.workDir, inPlatform, fromVerdict)
		}
	}
}

// TestWorkspaceBoundary_CaseFoldingMatchesFilesystem 锁住「大小写折叠跟随文件系统语义」。
//
// 这是本域在 **tools 侧**的**唯一**真实分歧点（其余边界行为收敛前后一致）：
//
//	收敛前用 `filepath.Rel(absWorkDir, absPath)` 比较 —— Rel 是**纯字符串**运算，
//	不感知文件系统大小写。于是在 macOS / Windows 上，`<tmp>/PROJ/a.txt` 相对
//	工作区 `<tmp>/proj` 会被算成 `../PROJ/a.txt` ⇒ 判 Outside ⇒ **误拒**同一目录下的写入。
//
//	收敛后走 `platform.IsWithinResolved`：**仅在 CaseInsensitiveFS() 时折叠大小写**，
//	Linux（大小写敏感）行为保持不变。
//
// ★ 判别力自证：本用例在收敛前的 pipeline.go 上为**红**（overlay 可复现）；
// 在 Linux 上自动跳过 —— 那里两个路径确实是不同目录，判 Outside 才是对的。
func TestWorkspaceBoundary_CaseFoldingMatchesFilesystem(t *testing.T) {
	if !platform.CaseInsensitiveFS() {
		t.Skip("本文件系统大小写敏感（Linux 等）：大小写不同的路径是不同目录，无需折叠")
	}

	tmp := t.TempDir()
	proj := filepath.Join(tmp, "proj")
	if err := os.MkdirAll(proj, 0o755); err != nil {
		t.Fatal(err)
	}

	upper := filepath.Join(tmp, "PROJ", "a.txt")
	if _, err := os.Stat(filepath.Join(tmp, "PROJ")); err != nil {
		t.Skipf("本卷不接受仅大小写不同的目录名，无法构造用例：%v", err)
	}

	got, reason := checkWorkspaceBoundary(upper, proj)
	if got != BoundaryInside {
		t.Errorf("大小写不敏感卷上，仅大小写不同的同一目录应判 BoundaryInside，实际 %v（%s）", got, reason)
	}
}

// TestWorkspaceBoundary_SymlinkAncestorEscape 锁住「祖先 symlink 逃逸」：
// 工作区里的软链接指向外部时，经由该链接写入的目标必须判 Outside。
//
// 这条正是「解析最深已存在祖先」而非「只 Clean」的理由 —— 若将来有人把
// ResolveExistingParent 换成纯 Clean，本用例会变红。
func TestWorkspaceBoundary_SymlinkAncestorEscape(t *testing.T) {
	tmp := t.TempDir()
	proj := filepath.Join(tmp, "proj")
	outside := filepath.Join(tmp, "outside")
	if err := os.MkdirAll(proj, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(outside, 0o755); err != nil {
		t.Fatal(err)
	}

	link := filepath.Join(proj, "escape")
	if err := os.Symlink(outside, link); err != nil {
		t.Skipf("本环境不支持创建 symlink，跳过：%v", err)
	}

	got, reason := checkWorkspaceBoundary(filepath.Join(link, "x.txt"), proj)
	if got != BoundaryOutside {
		t.Errorf("经 symlink 逃逸应判 BoundaryOutside，实际 %v（%s）", got, reason)
	}
}
