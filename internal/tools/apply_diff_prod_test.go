package tools

// apply_diff_prod_test.go — G5：`apply_unified_diff` 的**生产路径**表征。
//
// 依 `production-path-tdd`：
// - 全部从生产入口进入：`NewExecutor` → `InitPipelineWithHooks` → `ExecuteWithConfirm`
// （与 tui/lifecycle.go:425 / runner_core.go:532 / bridge.go:274 同一条链路），
// 不构造旁路、不 mock 守卫、不把 handler 直接调起来断言；
// - 每条守卫都配了**判别性反例**（见 TestApplyDiff_PipelineLayerIsLoadBearing），
// 注入反例能把断言打红 —— 否则"绿"只说明构造方式对了，不说明生产变了。
//
// 覆盖：
// P1 正向：已知 unified diff → 文件逐字节等于预期；
// P2 冲突：patch 与当前内容不匹配 → FailureKind=patch_conflict + 原文件未损坏；
// P3 越界：多文件补丁里有一个越界目标 → 管线前置拒绝（SandboxDenied）+ 工作区内那个文件也没动；
// P4 dry-run：只回报不落盘；
// P5 新建 + 删除：补丁语义完整；
// P6 终局性：patch_conflict 必须**不**终结本轮（与 sandbox/policy 拒绝区分）。

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/anchorandfish/whale-code/internal/permission"
)

// newApplyDiffProdExecutor 按生产方式装配 Executor：管线 + 权限策略 + 审批放行。
func newApplyDiffProdExecutor(t *testing.T, ws string) *Executor {
	t.Helper()
	e := NewExecutor(NewRegistry(), ws)

	policy := permission.NewPolicy(permission.LevelWorkspaceWrite)
	rule, err := permission.ParseRuleString(applyUnifiedDiffToolName, permission.RuleAllow, permission.SourceSession)
	if err != nil {
		t.Fatalf("ParseRuleString(%s allow): %v", applyUnifiedDiffToolName, err)
	}
	if err := policy.AddRule(*rule); err != nil {
		t.Fatalf("AddRule: %v", err)
	}
	e.InitPipelineWithHooks(policy, ws)
	if e.Pipeline == nil {
		t.Fatal("Pipeline 未初始化 —— 本用例前提失效，无法代表生产路径")
	}
	// 生产里由 TUI 应答确认；测试里显式自动接受，避免把"确认交互"混进本用例的判据。
	e.OnConfirm = func(tool, input string) bool { return true }
	return e
}

func applyDiffCall(id, patch string) Call {
	return Call{
		ID:    id,
		Name:  applyUnifiedDiffToolName,
		Input: fmt.Sprintf(`{"patch":%s}`, mustJSONString(patch)),
	}
}

// mustJSONString 把任意文本编码成 JSON 字符串字面量（含 \n 转义）。
func mustJSONString(s string) string {
	b, err := json.Marshal(s)
	if err != nil {
		panic(err)
	}
	return string(b)
}

// runApplyDiff 走生产入口执行一次补丁应用。
func runApplyDiff(t *testing.T, e *Executor, id, patch string) *PipelineResult {
	t.Helper()
	return e.ExecuteWithConfirm(context.Background(), applyDiffCall(id, patch))
}

func readFileOrFail(t *testing.T, path string) string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	return string(data)
}

// ------------------------------------------------------------------ P1 正向

func TestApplyDiff_ProdPath_ApplyIsByteExact(t *testing.T) {
	ws := t.TempDir()
	target := filepath.Join(ws, "hello.txt")
	const before = "line one\nline two\nline three\nline four\n"
	const want = "line one\nline two changed\nline two and a half\nline three\nline four\n"
	if err := os.WriteFile(target, []byte(before), 0o644); err != nil {
		t.Fatalf("seed: %v", err)
	}

	e := newApplyDiffProdExecutor(t, ws)
	// 补丁里的路径是相对路径 —— 与 git 生成补丁的形态一致，也验证"补丁根 = 工作区"。
	patch := `diff --git a/hello.txt b/hello.txt
--- a/hello.txt
+++ b/hello.txt
@@ -1,4 +1,5 @@
 line one
-line two
+line two changed
+line two and a half
 line three
 line four
`
	res := runApplyDiff(t, e, "g5-p1", patch)
	if res.Result == nil {
		t.Fatalf("正向用例应产生结果，实际被拒：proceed=%v rejection=%q logs=%v", res.Proceed, res.Rejection, res.StageLogs)
	}
	if res.Result.IsError {
		t.Fatalf("正向应用不应失败：output=%s", res.Result.Output)
	}
	if got := readFileOrFail(t, target); got != want {
		t.Errorf("文件内容非逐字节相等：\n got=%q\nwant=%q", got, want)
	}
	if !strings.Contains(res.Result.Output, "1 file(s)") {
		t.Errorf("回报应含文件计数，实际 %q", res.Result.Output)
	}
}

// ---------------------------------------------------------- P2 冲突（可恢复失败）

func TestApplyDiff_ProdPath_ConflictRejectsAndLeavesFileIntact(t *testing.T) {
	ws := t.TempDir()
	target := filepath.Join(ws, "hello.txt")
	// 文件内容与补丁上下文完全不符（不是偏移，是内容不同）——fuzz 也无法救。
	const before = "completely\ndifferent\ncontent\nhere\n"
	if err := os.WriteFile(target, []byte(before), 0o644); err != nil {
		t.Fatalf("seed: %v", err)
	}

	e := newApplyDiffProdExecutor(t, ws)
	patch := `--- a/hello.txt
+++ b/hello.txt
@@ -1,4 +1,5 @@
 line one
-line two
+line two changed
+line two and a half
 line three
 line four
`
	res := runApplyDiff(t, e, "g5-p2", patch)
	if res.Result == nil {
		t.Fatalf("冲突应由 handler 产出结果（不是管线拒绝）：proceed=%v rejection=%q", res.Proceed, res.Rejection)
	}
	if !res.Result.IsError {
		t.Fatalf("冲突必须报错，实际被当成功：output=%s", res.Result.Output)
	}
	if res.Result.FailureKind != FailureKindPatchConflict {
		t.Fatalf("冲突 FailureKind = %q，期望 %q", res.Result.FailureKind, FailureKindPatchConflict)
	}
	if !strings.Contains(res.Result.Output, "patch conflict") {
		t.Errorf("冲突报文应可读，实际 %q", res.Result.Output)
	}
	// 原文件必须逐字节未动 —— 冲突时"零写入"是硬承诺。
	if got := readFileOrFail(t, target); got != before {
		t.Errorf("冲突时原文件被改动：\n got=%q\nwant=%q", got, before)
	}
}

// ------------------------------------------------ P3 越界（管线前置拒绝 + 零副作用）

func TestApplyDiff_ProdPath_OutOfWorkspaceRejectedByPipeline(t *testing.T) {
	ws := t.TempDir()
	inside := filepath.Join(ws, "inside.txt")
	const insideBefore = "old\n"
	if err := os.WriteFile(inside, []byte(insideBefore), 0o644); err != nil {
		t.Fatalf("seed: %v", err)
	}

	outsideDir := t.TempDir() // 独立 TempDir ⇒ 一定在工作区之外
	outside := filepath.Join(outsideDir, "outside.txt")
	const outsideBefore = "x\n"
	if err := os.WriteFile(outside, []byte(outsideBefore), 0o644); err != nil {
		t.Fatalf("seed: %v", err)
	}

	e := newApplyDiffProdExecutor(t, ws)
	// 第一个文件在工作区内（合法），第二个越界 —— 多文件补丁里任意一个越界都必须整体拒绝。
	patch := fmt.Sprintf(`--- %s
+++ %s
@@ -1,1 +1,1 @@
-old
+NEW
--- %s
+++ %s
@@ -1,1 +1,1 @@
-x
+HACKED
`, inside, inside, outside, outside)

	res := runApplyDiff(t, e, "g5-p3", patch)

	// 管线前置拒绝的形态：Proceed=false + FailureKind=sandbox_denied + **Result 为 nil**
	// （Result 非 nil 意味着已经跑到 ExecuteStage/handler —— 那就不是"前置拒绝"了）。
	if res.Proceed {
		t.Fatalf("越界写必须被拒，实际放行：result=%+v", res.Result)
	}
	if res.Result != nil {
		t.Fatalf("期望管线**前置**拒绝（handler 未被执行 ⇒ Result==nil），实际产生了结果：%s", res.Result.Output)
	}
	if res.FailureKind != FailureKindSandboxDenied {
		t.Fatalf("越界 FailureKind = %q，期望 %q", res.FailureKind, FailureKindSandboxDenied)
	}
	// 必须是**管线** CustomValidateStage 拒的：报文是 workspace boundary check，
	// 且 handler 的软拒绝文案（"Sandbox denied:"）不得出现。
	if !strings.Contains(res.Rejection, "workspace boundary check") {
		t.Errorf("期望管线 CustomValidateStage 的报文，实际 %q", res.Rejection)
	}
	if strings.Contains(res.Rejection, "Sandbox denied:") {
		t.Errorf("越界被 handler 兜底拦下了（说明管线前置校验没生效）：%q", res.Rejection)
	}
	// 零副作用：越界文件未改动，工作区内那个也**不能**被改（全有或全无）。
	if got := readFileOrFail(t, outside); got != outsideBefore {
		t.Errorf("越界文件被改动：%q", got)
	}
	if got := readFileOrFail(t, inside); got != insideBefore {
		t.Errorf("多文件补丁中工作区内文件被部分应用（违反全有或全无）：%q", got)
	}
}

// TestApplyDiff_PipelineLayerIsLoadBearing 是 P3 的**判别性反例**。
//
// 手法：临时把 `patchTextFields` 里的登记删掉（模拟"忘了给新工具接多目标前置校验"），
// 再跑同一段越界补丁，断言发生**可观测翻转**：
//
//	登记在 → 管线前置拒绝：Proceed=false、Result==nil、报文 "workspace boundary check"
//	登记摘掉 → 退到 handler：Result!=nil、报文来自 handler 的软拒绝路径
//
// 两态都不得让越界写生效（handler 侧有独立的 checkWorkspaceBoundary 兜底）。
// 若这条不翻转，说明 P3 的断言没打在新增的管线路径上（假绿）。
func TestApplyDiff_PipelineLayerIsLoadBearing(t *testing.T) {
	ws := t.TempDir()
	outside := filepath.Join(t.TempDir(), "outside.txt")
	if err := os.WriteFile(outside, []byte("x\n"), 0o644); err != nil {
		t.Fatalf("seed: %v", err)
	}
	patch := fmt.Sprintf("--- %s\n+++ %s\n@@ -1,1 +1,1 @@\n-x\n+HACKED\n", outside, outside)

	// 反例：摘掉补丁类工具的多目标登记
	saved, had := patchTextFields[applyUnifiedDiffToolName]
	if had {
		delete(patchTextFields, applyUnifiedDiffToolName)
	}
	t.Cleanup(func() {
		if had {
			patchTextFields[applyUnifiedDiffToolName] = saved
		}
	})

	e := newApplyDiffProdExecutor(t, ws)
	res := runApplyDiff(t, e, "g5-p3-flip", patch)

	// 翻转点 1（结构性）：从"管线前置拒绝"（handler 未执行 ⇒ Result==nil）
	// 变为"handler 拒绝"（Result!=nil）。
	if res.Result == nil {
		t.Fatalf("摘掉多目标登记后应由 handler 兜底拒绝（期望 Result!=nil），实际仍是管线拒绝：%q", res.Rejection)
	}
	// 翻转点 2（结构性）：硬拒绝（管线，IsError=true）→ 软拒绝（handler，IsError=false，
	// S11 G1-B 契约，与 write_file/edit_file 同形）。报文文案不作为判据 —— 那是表现层。
	if res.Result.IsError {
		t.Errorf("handler 兜底应是软拒绝（IsError=false），实际 IsError=true：output=%q", res.Result.Output)
	}
	if !strings.Contains(res.Result.Output, "outside the workspace") {
		t.Errorf("handler 兜底报文应说明越界，实际 %q", res.Result.Output)
	}
	if res.Result.FailureKind != FailureKindSandboxDenied {
		t.Errorf("handler 兜底仍应携带 FailureKind=sandbox_denied，实际 %q", res.Result.FailureKind)
	}
	// 无论走哪一层，越界写入都必须没发生。
	if got := readFileOrFail(t, outside); got != "x\n" {
		t.Errorf("兜底路径也绝不允许越界写，实际 %q", got)
	}
}

// ------------------------------------------------------------------ P4 dry-run

func TestApplyDiff_ProdPath_DryRunWritesNothing(t *testing.T) {
	ws := t.TempDir()
	target := filepath.Join(ws, "hello.txt")
	const before = "alpha\nbeta\ngamma\n"
	if err := os.WriteFile(target, []byte(before), 0o644); err != nil {
		t.Fatalf("seed: %v", err)
	}

	e := newApplyDiffProdExecutor(t, ws)
	patch := `--- a/hello.txt
+++ b/hello.txt
@@ -1,3 +1,3 @@
 alpha
-beta
+BETA
 gamma
`
	res := e.ExecuteWithConfirm(context.Background(), Call{
		ID:   "g5-p4",
		Name: applyUnifiedDiffToolName,
		Input: fmt.Sprintf(`{"patch":%s,"dry_run":true}`,
			mustJSONString(patch)),
	})
	if res.Result == nil || res.Result.IsError {
		t.Fatalf("dry-run 不应失败：result=%+v rejection=%q", res.Result, res.Rejection)
	}
	if !strings.Contains(res.Result.Output, "Dry run") {
		t.Errorf("dry-run 回报应显式说明未落盘，实际 %q", res.Result.Output)
	}
	if !strings.Contains(res.Result.Output, "hello.txt") {
		t.Errorf("dry-run 应列出将被改动的文件，实际 %q", res.Result.Output)
	}
	if got := readFileOrFail(t, target); got != before {
		t.Errorf("dry-run 必须零写入，实际文件变成了 %q", got)
	}
}

// ------------------------------------------------------------ P5 新建 + 删除

func TestApplyDiff_ProdPath_CreateAndDelete(t *testing.T) {
	ws := t.TempDir()
	created := filepath.Join(ws, "created.txt")
	deleted := filepath.Join(ws, "deleted.txt")
	keeper := filepath.Join(ws, "keeper.txt")
	if err := os.WriteFile(deleted, []byte("gone\nalso gone\n"), 0o644); err != nil {
		t.Fatalf("seed: %v", err)
	}
	if err := os.WriteFile(keeper, []byte("keep\n"), 0o644); err != nil {
		t.Fatalf("seed: %v", err)
	}

	e := newApplyDiffProdExecutor(t, ws)
	patch := fmt.Sprintf(`--- /dev/null
+++ b/created.txt
@@ -0,0 +1,2 @@
+first
+second
--- %s
+++ /dev/null
@@ -1,2 +0,0 @@
-gone
-also gone
`, deleted)
	res := runApplyDiff(t, e, "g5-p5", patch)
	if res.Result == nil || res.Result.IsError {
		t.Fatalf("新建+删除补丁应成功：result=%+v rejection=%q", res.Result, res.Rejection)
	}
	if got := readFileOrFail(t, created); got != "first\nsecond\n" {
		t.Errorf("新建文件内容错误：%q", got)
	}
	if _, err := os.Stat(deleted); !os.IsNotExist(err) {
		t.Errorf("被删文件仍存在（err=%v）", err)
	}
	if got := readFileOrFail(t, keeper); got != "keep\n" {
		t.Errorf("未涉及的文件被改动：%q", got)
	}
}

// ------------------------------------------------------------ P7 patch_path 入口

func TestApplyDiff_ProdPath_PatchReadFromFile(t *testing.T) {
	ws := t.TempDir()
	target := filepath.Join(ws, "hello.txt")
	if err := os.WriteFile(target, []byte("alpha\nbeta\n"), 0o644); err != nil {
		t.Fatalf("seed: %v", err)
	}
	patchFile := filepath.Join(ws, "change.patch")
	patch := `--- a/hello.txt
+++ b/hello.txt
@@ -1,2 +1,2 @@
 alpha
-beta
+BETA
`
	if err := os.WriteFile(patchFile, []byte(patch), 0o644); err != nil {
		t.Fatalf("seed patch: %v", err)
	}

	e := newApplyDiffProdExecutor(t, ws)
	res := e.ExecuteWithConfirm(context.Background(), Call{
		ID:    "g5-p7",
		Name:  applyUnifiedDiffToolName,
		Input: fmt.Sprintf(`{"patch_path":%s}`, mustJSONString(patchFile)),
	})
	if res.Result == nil || res.Result.IsError {
		t.Fatalf("patch_path 入口应可用：result=%+v rejection=%q", res.Result, res.Rejection)
	}
	if got := readFileOrFail(t, target); got != "alpha\nBETA\n" {
		t.Errorf("内容错误：%q", got)
	}
}

// ------------------------------------------------- P8 写文件面注册（装配契约）

// TestApplyDiff_RegisteredAsWriteFace 钉住"本工具必须命中写文件面"这条装配契约。
//
// 为什么值得单测：`isWriteTool` 有两份实现（tools 包按管线用，permission 包按
// 受保护路径检查用）。任一份漏登记，本工具就会**绕过**工作区边界/受保护路径检查 ——
// 而且不会有任何编译错误或现有测试变红。所以这里把两份都点名断言。
func TestApplyDiff_RegisteredAsWriteFace(t *testing.T) {
	if !isWriteTool(applyUnifiedDiffToolName) {
		t.Error("tools.isWriteTool 未把 apply_unified_diff 当作写类工具 ⇒ CustomValidateStage 跳过工作区边界检查")
	}
	if !permission.IsWriteTool(applyUnifiedDiffToolName) {
		t.Error("permission.IsWriteTool 未把 apply_unified_diff 当作写类工具 ⇒ SafetyCheckStage 跳过受保护路径检查")
	}

	// 注册表：必须有 Spec、声明 WW 档、且能力分桶为 Edit（⇒ read-only 角色面拿不到它、
	// Edit(*) deny 规则覆盖它）。
	spec, ok := NewRegistry().Get(applyUnifiedDiffToolName)
	if !ok {
		t.Fatalf("注册表里没有 %s 的 Spec", applyUnifiedDiffToolName)
	}
	if spec.RequiredPermission != LevelWorkspaceWrite {
		t.Errorf("RequiredPermission = %v，期望 %v", spec.RequiredPermission, LevelWorkspaceWrite)
	}
	if spec.ConcurrencySafe {
		t.Error("补丁应用会并发写同一批文件，不得声明为并发安全")
	}
	var hasEdit bool
	for _, c := range permission.CapabilitiesOf(applyUnifiedDiffToolName) {
		if c == permission.CapEdit {
			hasEdit = true
		}
	}
	if !hasEdit {
		t.Error("能力分桶缺少 Edit ⇒ deny Edit(*) 覆盖不到它、只读角色面会拿到它")
	}
	if len(spec.InputSchema) == 0 {
		t.Error("InputSchema 为空 —— 模型看不到参数定义，工具不可用")
	}
}

// ------------------------------------------------------------ P6 终局性语义

// TestFailureKind_TerminalityIsExplicit 钉住 G5 引入的**维度拆分**：
// 「是哪种失败」与「是否终结本轮」是两个正交属性，终局性必须显式登记。
//
// 判别性：若有人把 FailureKind.Terminal() 改回 `k != ""`，本用例立刻变红
// （patch_conflict 会被误判为终局 ⇒ 一次可自愈的补丁失配掐掉整个用户回合）。
func TestFailureKind_TerminalityIsExplicit(t *testing.T) {
	if !FailureKindSandboxDenied.Terminal() {
		t.Error("sandbox_denied 必须是终局拒绝（重试同一调用无意义）")
	}
	if !FailureKindPolicyDenied.Terminal() {
		t.Error("policy_denied 必须是终局拒绝")
	}
	if FailureKindPatchConflict.Terminal() {
		t.Error("patch_conflict 不得是终局拒绝 —— 它是可恢复的普通失败（模型重读文件后可再提交新补丁）")
	}
	if FailureKind("").Terminal() {
		t.Error("零值必须是「可重试」，不得是终局")
	}
	if FailureKind("nonexistent_kind").Terminal() {
		t.Error("未登记的新 Kind 不得默认获得终局权（fail-safe：默认不终结用户回合）")
	}

	// 每个已声明的 Kind 都必须有明确归属（防止新增 Kind 时漏登记）。
	for _, k := range AllFailureKinds() {
		if k == "" {
			t.Errorf("AllFailureKinds 里出现了零值：%q", k)
		}
	}
	// ★ 本计数已于 2026-10-09 **有意翻转** 3 → 4（(c) 新增 `command_exit`）
	// —— **下一个人别当回归**：翻转前钉的是「FailureKind 取值集合恰为 3 个」
	// （sandbox_denied / policy_denied / patch_conflict）。新增第四种
	// `FailureKindCommandExit`（命令非零退出＝带标记的普通失败，Terminal=false）后，
	// 计数必须随之 +1；若要**再**加种类，请连同上面那条"每个 Kind 都要有明确归属"
	// 与 R1 契约文档一起更新（本条只锁"数量变了要有人知道"，不锁语义）。
	if len(AllFailureKinds()) != 4 {
		t.Errorf("FailureKind 取值集合大小变了（%d，当前应为 4：sandbox_denied / policy_denied / "+
			"patch_conflict / command_exit）—— 新增 Kind 时请同步本文用例、failureKindSpecs "+
			"的语义表与 R1 契约文档",
			len(AllFailureKinds()))
	}
	// 新增种类（(c)）的终局性：命令非零退出是**可恢复**的 ⇒ 不得掐掉用户回合。
	if FailureKindCommandExit.Terminal() {
		t.Error("command_exit 不得是终局拒绝 —— 命令返回非零是正常结果，模型可以改一版再跑")
	}
	if FailureKindCommandExit.IsSecurityDenial() {
		t.Error("command_exit 不得被算作安全拒绝 —— 否则它会被记进 R4 安全账本，使判据失真")
	}

	// ToolExecError.Terminal() 必须与 FailureKind.Terminal() 同源（不得各判各的）。
	if (&ToolExecError{Tool: "t", Kind: FailureKindPatchConflict}).Terminal() {
		t.Error("ToolExecError.Terminal 未跟随 FailureKind.Terminal —— 两个判据已漂移")
	}
	if !(&ToolExecError{Tool: "t", Kind: FailureKindSandboxDenied}).Terminal() {
		t.Error("ToolExecError.Terminal 未跟随 FailureKind.Terminal —— 两个判据已漂移")
	}
}
