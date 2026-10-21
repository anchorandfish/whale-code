package util

import (
	"path/filepath"
	"testing"

	"github.com/anchorandfish/whale-code/internal/platform"
)

// TestPathEquality_Convergence 是「路径等价」轴的**永久门**。
//
// 收敛前这条轴上有**三份各自实现**的等价逻辑：
//
//	util.PathsEqual Abs + EvalSymlinks + Clean + 按 runtime.GOOS 折叠大小写
//	platform.PathEqual ToSlash + Clean + 按 CaseInsensitiveFS() 折叠
//	platform.CaseInsensitiveCompare ToSlash + Clean + **一律**折叠
//
// 本门断言两条不变量：
//
//	A. **机制同源**：util.PathsEqual 的结论 == platform.PathEqual(规范化(a), 规范化(b))。
//	B. **折叠策略跟随文件系统语义**（而不是硬编码 GOOS）：显式声明
//
// WHALE_CASE_SENSITIVE=1 时必须**不**折叠 —— 收敛前 util.PathsEqual 忽略该覆盖。
//
// ★ 判别力自证（R4）：不变量 B 在收敛前的 util/paths.go 上为**红**
// （macOS/Windows 上旧实现硬编码 `runtime.GOOS == "darwin"`，无视环境覆盖）。
func TestPathEquality_Convergence(t *testing.T) {
	tmp := t.TempDir()
	a := filepath.Join(tmp, "a.txt")
	b := filepath.Join(tmp, "b.txt")

	cases := [][2]string{
		{a, a},                                // 同一路径
		{a, filepath.Join(tmp, ".", "a.txt")}, // Clean 后等价
		{a, filepath.Join(tmp, "sub", "..") + "/a.txt"}, // .. 经 Clean 后等价
		{a, b},   // 不同路径
		{"", ""}, // 两个空路径
	}

	for _, c := range cases {
		got := PathsEqual(c[0], c[1])

		n1, err1 := platform.NormalizePath(c[0])
		if err1 != nil {
			n1 = c[0]
		}
		n2, err2 := platform.NormalizePath(c[1])
		if err2 != nil {
			n2 = c[1]
		}
		want := platform.PathEqual(n1, n2)

		if got != want {
			t.Errorf("机制未同源：PathsEqual(%q,%q)=%v，而 platform 组合=%v", c[0], c[1], got, want)
		}
	}
}

// TestPathEquality_HonorsCaseSensitivityOverride 锁住「折叠策略跟随文件系统语义」。
//
// WHALE_CASE_SENSITIVE=1 是 platform.CaseInsensitiveFS() 的显式覆盖开关。
// 收敛前 util.PathsEqual 直接读 runtime.GOOS，**无视**该开关：
// 在 macOS 上即使声明了"本卷大小写敏感"，仍会把 /tmp/Foo 与 /tmp/foo 判为等价。
func TestPathEquality_HonorsCaseSensitivityOverride(t *testing.T) {
	if !platform.CaseInsensitiveFS() {
		t.Skip("本环境默认大小写敏感：折叠本来就不会发生，本用例无判别力")
	}

	t.Setenv("WHALE_CASE_SENSITIVE", "1")

	tmp := t.TempDir()
	upper := filepath.Join(tmp, "Foo", "x.txt")
	lower := filepath.Join(tmp, "foo", "x.txt")

	if PathsEqual(upper, lower) {
		t.Errorf("声明 WHALE_CASE_SENSITIVE=1 后不应折叠大小写，但 PathsEqual(%q,%q)=true", upper, lower)
	}
	if platform.PathEqual(upper, lower) {
		t.Errorf("platform.PathEqual 也应在该覆盖下不折叠")
	}
}

// TestPathEquality_ForcedFoldIsADistinctPolicy 把「强制折叠」记为**显式策略**而非第二实现。
//
// CaseInsensitiveCompare 与 PathEqual 共用同一机制，差别只在 foldCase 参数。
// 本门把这条差别**钉在断言上**：在大小写敏感的文件系统上两者必须给出不同结论 ——
// 若将来有人把 CaseInsensitiveCompare 改成"跟随文件系统"，本门会红，
// 从而迫使改动者显式面对 的策略选择。
func TestPathEquality_ForcedFoldIsADistinctPolicy(t *testing.T) {
	if platform.CaseInsensitiveFS() {
		t.Skip("本环境默认大小写不敏感：两者结论本就相同，本用例无判别力")
	}

	upper := "/home/Foo/bar"
	lower := "/home/foo/bar"

	if platform.PathEqual(upper, lower) {
		t.Errorf("大小写敏感文件系统上 PathEqual 不应折叠")
	}
	if !platform.CaseInsensitiveCompare(upper, lower) {
		t.Errorf("CaseInsensitiveCompare 按  应**强制**折叠（策略与 PathEqual 不同）")
	}
}
