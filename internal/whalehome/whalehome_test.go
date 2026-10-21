package whalehome

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/anchorandfish/whale-code/internal/testutil"
)

// ===== 契约钉死 =====

// TestEnvVarContract 钉死对外契约字面量：用户在 shell 里 export 的就是 "WHALE_HOME"。
// （R4-12 往返自洽陷阱：环境变量名是对外契约，必须单独断言字面量，
// 不能只测"用 EnvVar 常量注入后生效"——那对改名免疫，恰恰是假绿。）
func TestEnvVarContract(t *testing.T) {
	if EnvVar != "WHALE_HOME" {
		t.Fatalf("EnvVar 对外契约被改动：got %q, want \"WHALE_HOME\"（改名需同步文档/回执/本测试）", EnvVar)
	}
}

// ===== Dir() =====

// TestDir_Default_RespectsHome 默认态：未设 WHALE_HOME ⇒ $HOME/.whale。
func TestDir_Default_RespectsHome(t *testing.T) {
	home := testutil.IsolatedHomeDir(t)
	t.Setenv("HOME", home)
	t.Setenv("WHALE_HOME", "")

	want := filepath.Join(home, ".whale")
	if got := Dir(); got != want {
		t.Fatalf("Dir() = %q, want %q（默认应为 $HOME/.whale）", got, want)
	}
}

// TestDir_Override_Wins 设了 WHALE_HOME ⇒ 状态目录整体搬家。
func TestDir_Override_Wins(t *testing.T) {
	home := testutil.IsolatedHomeDir(t)
	override := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("WHALE_HOME", override)

	if got := Dir(); got != filepath.Clean(override) {
		t.Fatalf("Dir() = %q, want override %q（WHALE_HOME 必须整体接管状态目录）", got, override)
	}
	// 反向半边（R9）：默认 home 不得混入
	if strings.HasPrefix(Dir(), home) {
		t.Fatalf("Dir() = %q 混入了默认 home %q —— override 未生效", Dir(), home)
	}
}

// TestDir_EmptyOverride_FallsBack 空串视同未设置（= 清除覆盖）。
func TestDir_EmptyOverride_FallsBack(t *testing.T) {
	home := testutil.IsolatedHomeDir(t)
	t.Setenv("HOME", home)
	t.Setenv("WHALE_HOME", "")

	want := filepath.Join(home, ".whale")
	if got := Dir(); got != want {
		t.Fatalf("Dir() = %q, want %q（空 WHALE_HOME 应视同未设置）", got, want)
	}
}

// ===== Validate() fail-fast =====

func TestValidate_Unset_OK(t *testing.T) {
	t.Setenv("WHALE_HOME", "")
	if err := Validate(); err != nil {
		t.Fatalf("未设置 WHALE_HOME 时 Validate() = %v, want nil（默认路径行为不变）", err)
	}
}

func TestValidate_ValidDir_OK(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("WHALE_HOME", dir)
	if err := Validate(); err != nil {
		t.Fatalf("合法目录 Validate() = %v, want nil", err)
	}
}

func TestValidate_NotExist_Refuses(t *testing.T) {
	t.Setenv("WHALE_HOME", filepath.Join(t.TempDir(), "does-not-exist"))
	err := Validate()
	if err == nil {
		t.Fatal("不存在的路径 Validate() = nil, want error（fail-fast：宁可拒启动也不静默写坏路径）")
	}
	if !strings.Contains(err.Error(), "WHALE_HOME") {
		t.Fatalf("错误信息必须点名 WHALE_HOME（用户要能看懂是谁的问题），got: %v", err)
	}
}

func TestValidate_FileNotDir_Refuses(t *testing.T) {
	file := filepath.Join(t.TempDir(), "afile")
	if err := os.WriteFile(file, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Setenv("WHALE_HOME", file)
	if err := Validate(); err == nil {
		t.Fatal("指向普通文件 Validate() = nil, want error")
	}
}

func TestValidate_Relative_Refuses(t *testing.T) {
	t.Setenv("WHALE_HOME", "relative/dir")
	if err := Validate(); err == nil {
		t.Fatal("相对路径 Validate() = nil, want error（相对路径会让状态目录随 cwd 漂移）")
	}
}
