package whalehome

import (
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"testing"
)

// 收口守卫（结构性，读盘型判据）：
//
// 生产码里"whale 状态目录"的唯一合法写法是 whalehome.Dir()。
// 本守卫扫描全仓非测试 .go 文件，凡出现 `filepath.Join(x, ".whale"...)` 形态：
// - x 含 work/project/current 等 ⇒ 工作区侧 .whale（项目配置，语义不同）⇒ 放行；
// - x 含 home/userDir ⇒ 状态目录残留旧写法 ⇒ 红（应改走 whalehome.Dir()）；
// - 其它未知变量 ⇒ fail-closed 红（必须显式登记为项目侧或改为 Dir()）。
//
// 判别性（R6 通道 B′ 正向注入反例）：本测试运行期自读磁盘，overlay 无效；
// 判别性由 zz_probe 文件注入证明——见 TestResidualJoinGuard_IsDiscriminatory。
// 注入反例后必须立即删除（收工纪律：rm + ls 自证不在盘）。

var residualJoinRe = regexp.MustCompile(`filepath\.Join\(\s*([\w\.\(\)\[\]]+?),\s*"\.whale"`)

// allowResidualVar 返回 true 表示该变量是"工作区侧 .whale"（项目配置语义，非状态目录）。
// ★ 登记制：新增放行变量必须在此写明理由。
func allowResidualVar(v string) bool {
	lv := strings.ToLower(v)
	switch {
	case strings.Contains(lv, "work"), strings.Contains(lv, "project"):
		return true // 工作区/项目目录侧 .whale（hooks.json、rules、worktrees 等项目配置）
	case lv == "current":
		return true // magic_docs_v2 allowedIncludeRoots：向上走查的项目根（非状态目录）
	case lv == "basedir":
		return true // magic_docs_v2 allowedIncludeRoots(baseDir)：项目基目录（非状态目录）
	}
	return false
}

func repoRoot(t *testing.T) string {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("posix path assumptions")
	}
	// 测试二进制工作目录 = internal/whalehome ⇒ 仓库根在上两级
	return filepath.Join("..", "..")
}

func scanResidualJoins(t *testing.T, root string) []string {
	t.Helper()
	var violations []string
	err := filepath.Walk(root, func(path string, fi os.FileInfo, err error) error {
		if err != nil {
			return nil // 不可达目录跳过（vendor 权限等）
		}
		name := fi.Name()
		if fi.IsDir() {
			switch name {
			case "vendor", "docs", "handoff", "scripts", ".git", "testdata", "competitor_snapshots":
				return filepath.SkipDir
			}
			// whalehome 包自身是 SSOT 实现（Dir() 里就是 $HOME/.whale 的定义处），豁免
			if path == filepath.Join(root, "internal", "whalehome") {
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			return nil
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return nil
		}
		for _, m := range residualJoinRe.FindAllStringSubmatch(string(data), -1) {
			v := m[1]
			if strings.Contains(v, "whalehome") {
				continue // Join(whalehome.Dir(), ".whale", ...) 形态不存在，防御
			}
			if allowResidualVar(v) {
				continue
			}
			violations = append(violations, path+": Join("+v+", \".whale\"...)")
		}
		return nil
	})
	if err != nil {
		t.Fatalf("walk failed: %v", err)
	}
	return violations
}

func TestResidualJoinGuard_NoHomeDerivedWhaleJoins(t *testing.T) {
	violations := scanResidualJoins(t, repoRoot(t))
	if len(violations) > 0 {
		t.Fatalf(" 收口被破坏：生产码仍存在 home 派生的 .whale 拼接（应改走 whalehome.Dir()）:\n%s",
			strings.Join(violations, "\n"))
	}
}

// TestResidualJoinGuard_IsDiscriminatory 判别性自证（R6 通道 B′）：
// 注入一个含 home 派生拼接的私有探针文件 ⇒ 守卫必须红；删掉后必须绿。
func TestResidualJoinGuard_IsDiscriminatory(t *testing.T) {
	if os.Getenv("W103_GUARD_PROBE") != "1" {
		t.Skip("仅以 W103_GUARD_PROBE=1 单独运行：本用例会临时写入探针文件")
	}
	root := repoRoot(t)
	probe := filepath.Join(root, "internal", "whalehome", "zz_w103_probe.go")
	probeSrc := "package whalehome\n\nimport \"path/filepath\"\n\nvar zzProbe = filepath.Join(homeDirForProbe, \".whale\", \"x\")\n"
	// 探针需可编译吗？不需要——守卫是文本扫描（ReadFile+regex），不编译探针。
	if err := os.WriteFile(probe, []byte(probeSrc), 0o644); err != nil {
		t.Fatal(err)
	}
	violations := scanResidualJoins(t, root)
	// 收工纪律：无论如何先删探针，再断言
	os.Remove(probe)
	if _, err := os.Stat(probe); !os.IsNotExist(err) {
		t.Fatalf("探针未删除成功，必须人工确认 %s 不在盘", probe)
	}
	found := false
	for _, v := range violations {
		if strings.Contains(v, "zz_w103_probe.go") {
			found = true
		}
	}
	if !found {
		t.Fatalf("守卫未抓到注入的 home 派生拼接反例 ⇒ 守卫不可判别（恒绿假门）。violations=%v", violations)
	}
}
