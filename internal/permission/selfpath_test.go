package permission

import (
	"os"
	"path/filepath"
	"testing"
)

// 「whale 自身源码树」识别声明的单元判据。
//
// 这一层只钉**声明本身**的语义（判据是什么、边界在哪、开关归谁）；
// 生产路径上的行为判别器在 `internal/tools/.go`。

func n077WriteGoMod(t *testing.T, dir, module string) {
	t.Helper()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir %s: %v", dir, err)
	}
	body := "// fixture\n\nmodule " + module + "\n\ngo 1.25\n"
	if err := os.WriteFile(filepath.Join(dir, "go.mod"), []byte(body), 0o644); err != nil {
		t.Fatalf("write go.mod: %v", err)
	}
}

// TestFix_N077_IsSelfSourceTree 主判据：判据是 go.mod 的 module 声明精确等于本仓模块路径。
func TestFix_N077_IsSelfSourceTree(t *testing.T) {
	root := t.TempDir()
	n077WriteGoMod(t, root, SelfModulePath)
	sub := filepath.Join(root, "internal", "permission")
	if err := os.MkdirAll(sub, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}

	// ① 模块根自身
	if !IsSelfSourceTree(root) {
		t.Fatalf("模块根应被识别为自身源码树: %s", root)
	}
	// ② 模块根的子目录（工作区可能是几层子路径）
	if !IsSelfSourceTree(sub) {
		t.Fatalf("模块根子目录应被识别为自身源码树: %s", sub)
	}
}

// TestGuard_N077_ExactMatchNotPrefix：精确相等，前缀相近的第三方模块不得被误保护。
func TestGuard_N077_ExactMatchNotPrefix(t *testing.T) {
	cases := []string{
		"example.com/some/other-project",
		"github.com/anchorandfish/whale-code-fork",
		"github.com/anchorandfish/whale-code-extra/deep",
		"github.com/anchorandfish/whale-code2",
	}
	for _, m := range cases {
		root := t.TempDir()
		n077WriteGoMod(t, root, m)
		if IsSelfSourceTree(root) {
			t.Errorf("模块 %q 不得被判为自身源码树（判据必须精确相等）", m)
		}
	}
}

// TestGuard_N077_NoGoModIsNotSelf：无 go.mod（普通项目目录）不得被判为自身。
func TestGuard_N077_NoGoModIsNotSelf(t *testing.T) {
	if IsSelfSourceTree(t.TempDir()) {
		t.Fatal("无 go.mod 的普通工作区不得被判为自身源码树")
	}
	if IsSelfSourceTree("") {
		t.Fatal("空 workDir 不得被判为自身源码树")
	}
}

// TestGuard_N077_BrokenGoModIsNotSelf：go.mod 存在但无 module 行（或读不了）⇒ 不判为自身。
func TestGuard_N077_BrokenGoModIsNotSelf(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "go.mod"), []byte("go 1.25\n"), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
	if IsSelfSourceTree(root) {
		t.Fatal("无 module 声明的 go.mod 不得被判为自身源码树")
	}
}

// TestGuard_N077_OptOutEnvIsUserControlled：启动时显式声明的免问开关生效，
// 且必须是**用户单向**通道（本进程环境变量，agent 的子进程改不到）。
func TestGuard_N077_OptOutEnvIsUserControlled(t *testing.T) {
	root := t.TempDir()
	n077WriteGoMod(t, root, SelfModulePath)

	if !IsSelfSourceTree(root) {
		t.Fatal("前置：该目录应被识别为自身源码树")
	}

	t.Setenv(selfModifyOptOutEnv, "1")
	if !SelfModifyOptOutActive() {
		t.Fatal("显式声明后 SelfModifyOptOutActive 应为 true")
	}
	if IsSelfSourceTree(root) {
		t.Fatal("用户启动时显式声明免问后，自指保护应关闭")
	}

	// 非真值不得开启
	t.Setenv(selfModifyOptOutEnv, "0")
	if SelfModifyOptOutActive() {
		t.Fatal(`"0" 不是真值，不得开启免问`)
	}
	t.Setenv(selfModifyOptOutEnv, "")
	if SelfModifyOptOutActive() {
		t.Fatal("空值不得开启免问")
	}
}

// TestGuard_N077_CanModifyLocalStateFollowsCapabilitySSOT 钉住载体判据**来自能力表 SSOT**：
// 只有 CapEdit / CapExec 两类算「能改动本机」，不能退化成工具名单。
func TestGuard_N077_CanModifyLocalStateFollowsCapabilitySSOT(t *testing.T) {
	// 正向：写类与命令类
	for _, tool := range []string{"write_file", "edit_file", "apply_unified_diff", "bash", "monitor", "lsprename"} {
		if !CanModifyLocalState(tool) {
			t.Errorf("%q 应被判为可改动本机（CapEdit/CapExec）", tool)
		}
	}
	// 反向：读/网络/编排类不得被判为可改动本机
	for _, tool := range []string{"read_file", "grep_search", "webfetch", "websearch", "Task", "agent", "todowrite"} {
		if CanModifyLocalState(tool) {
			t.Errorf("%q 不该被判为可改动本机", tool)
		}
	}
	// 判据必须与能力表同源（不是另一份手抄名单）
	for _, tool := range []string{"write_file", "bash", "read_file", "webfetch"} {
		want := false
		for _, c := range CapabilitiesOf(tool) {
			if c == CapEdit || c == CapExec {
				want = true
			}
		}
		if got := CanModifyLocalState(tool); got != want {
			t.Errorf("%q: CanModifyLocalState=%v 与 CapabilitiesOf 派生值 %v 不一致", tool, got, want)
		}
	}
}

// ---------------------------------------------------------------------------
// 来源 2（`os.Executable()`）—— ★ 首次可判别的轴 ＋ P3 的两个谓词
// ---------------------------------------------------------------------------

// n077SelfTree 造一棵声明本仓模块路径的临时源码树，返回其根。
func n077SelfTree(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	n077WriteGoMod(t, root, SelfModulePath)
	return root
}

// n077ExeAt 注入「正在运行的二进制路径」（可判别的输入）。
func n077ExeAt(t *testing.T, exePath string) {
	t.Helper()
	old := RunningExePath
	RunningExePath = func() string { return exePath }
	t.Cleanup(func() { RunningExePath = old })
}

// TestFix_N077_P1ArbitraryReachPredicate 钉住 **P1 谓词**的语义：
// 「工作区**或**运行中的二进制落在自身树」即算自指。
//
// 为什么这是**期望行为**而不是偏差（口径 P3，用户 2026-09-20 05:2x 拍板）：
// P1 只服务「可达面无法由入参界定」的任意命令类（`bash`/`Monitor`）—— 对这类工具，
// 二进制住在自身树里就意味着它随时能改到自身树（`go build -o whale .` 只是其中一个形态）。
// 2026-09-20 实测（同一个 probe，只换 exe 位置）：
//
//	exe = <whale-code>/.n077probe/probe（树内）⇒ IsSelfSourceTree(<非自指工作区>) = true
//	exe = /private/tmp/n077probe_outside（树外）⇒ false
//
// ⇒ 目标受限类**不受**这条影响：它们走 P2 `PathInSelfSourceTree`（见下一个用例），
// 这正是 P3 用来消除"开发件跑任何工作区都吵"的手段。
func TestFix_N077_P1ArbitraryReachPredicate(t *testing.T) {
	tree := n077SelfTree(t)
	exeInTree := filepath.Join(tree, "whale") // 与生产同形：二进制住在自身源码树里
	unrelated := t.TempDir()                  // 与自身树无关的工作区

	n077WriteGoMod(t, unrelated, "example.com/some/other-project")

	// ① 二进制住在自身树里 ⇒ 非自指工作区也判自指（P1 有意从严，服务任意命令类）。
	if !isSelfSourceTreeFrom(unrelated, exeInTree) {
		t.Fatalf("P1 谓词语义变了：exe 在自身树内时非自指工作区应为 true（供 bash/Monitor 从严）。"+
			"若这是有意的口径变更，请改本用例并更新登记表；tree=%s workDir=%s", tree, unrelated)
	}
	// ② 该来源的本意：「工作区是源码树的父目录」形态必须被保护。
	parent := filepath.Dir(tree)
	if !isSelfSourceTreeFrom(parent, exeInTree) {
		t.Fatalf("来源 2 的本意失效：工作区是自身树的父目录时必须判为自指（parent=%s）", parent)
	}
	// ③ 反向：exe 不在树内时，非自指工作区不得被误判（防"凡 go.mod 皆自身"退化）。
	if isSelfSourceTreeFrom(unrelated, filepath.Join(t.TempDir(), "whale")) {
		t.Fatal("exe 不在自身树内时，非自指工作区不得被判为自指")
	}
	// ④ 空 exe（os.Executable 失败）⇒ 不得仅凭来源 2 判为自指。
	if isSelfSourceTreeFrom(unrelated, "") {
		t.Fatal("exe 路径为空时不得仅凭来源 2 判为自指")
	}
}

// TestFix_N077_P2PathPredicate 钉住 **P2 谓词** `PathInSelfSourceTree` 的语义：
// 只看**目标路径**自己落在哪 —— 不看二进制在哪，也不看工作区
// （P3 里供「效果只经由声明目标发生」的写类工具使用）。
func TestFix_N077_P2PathPredicate(t *testing.T) {
	tree := n077SelfTree(t)
	deep := filepath.Join(tree, "internal", "permission")
	if err := os.MkdirAll(deep, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	// 「已存在的文件」先真造出来 —— 否则它会因为"路径不存在"而恰好走另一条分支（假绿）。
	existing := filepath.Join(deep, "n077_fixture.txt")
	if err := os.WriteFile(existing, []byte("x"), 0o644); err != nil {
		t.Fatalf("write fixture: %v", err)
	}

	cases := []struct {
		name   string
		target string
		want   bool
	}{
		{"模块根", tree, true},
		{"模块根子目录", deep, true},
		{"已存在的文件", existing, true},
		{"模块根的 go.mod", filepath.Join(tree, "go.mod"), true},
		{"尚不存在的路径（按目录链判定）", filepath.Join(deep, "not-yet.go"), true},
		{"无关目录", t.TempDir(), false},
		{"无关目录下的文件", filepath.Join(t.TempDir(), "a.go"), false},
		{"空路径", "", false},
	}
	for _, c := range cases {
		if got := PathInSelfSourceTree(c.target); got != c.want {
			t.Errorf("%s: PathInSelfSourceTree(%q)=%v want %v", c.name, c.target, got, c.want)
		}
	}
}

// TestGuard_N077_P2PredicateIgnoresRunningExe 是 P3 分流的**结构面**守卫：
// P2 谓词只认目标路径 —— 把「运行中的二进制」放进自身树**不得**影响它。
// 这条若变红，说明 P2 被 P1 污染 ⇒ 「目标受限类不因二进制位置而吵」当场失效（P3 归零）。
func TestGuard_N077_P2PredicateIgnoresRunningExe(t *testing.T) {
	tree := n077SelfTree(t)
	unrelatedFile := filepath.Join(t.TempDir(), "a.go")

	if PathInSelfSourceTree(unrelatedFile) {
		t.Fatal("前置不成立：无关文件不该被判在自身树内")
	}

	n077ExeAt(t, filepath.Join(tree, "whale")) // 生产形态：二进制住在自身树里

	if PathInSelfSourceTree(unrelatedFile) {
		t.Fatal("二进制住在自身树里不得改变 P2 谓词的结果（P3：目标受限类只看目标）")
	}
	// 对照：同一注入下 P1 谓词**必须**受影响 —— 否则两个谓词是同一件事，P3 的分流是空的。
	if !IsSelfSourceTree(filepath.Dir(unrelatedFile)) {
		t.Fatal("同一注入下 P1 谓词应为 true —— 否则两条谓词无从区分，P3 的分流是空的")
	}
}

// TestGuard_N077_OptOutEnvCoversBothPredicates：用户启动时显式声明的免问开关
// 必须**同时**覆盖两个谓词（只覆盖其一只会让另一条变成"关不掉的保护"，用户无从退出）。
func TestGuard_N077_OptOutEnvCoversBothPredicates(t *testing.T) {
	tree := n077SelfTree(t)
	if !IsSelfSourceTree(tree) || !PathInSelfSourceTree(filepath.Join(tree, "a.go")) {
		t.Fatal("前置：未开免问时两个谓词都应为 true")
	}
	t.Setenv(selfModifyOptOutEnv, "1")
	if IsSelfSourceTree(tree) {
		t.Fatal("免问开关未覆盖 P1 谓词（IsSelfSourceTree）")
	}
	if PathInSelfSourceTree(filepath.Join(tree, "a.go")) {
		t.Fatal("免问开关未覆盖 P2 谓词（PathInSelfSourceTree）")
	}
}
