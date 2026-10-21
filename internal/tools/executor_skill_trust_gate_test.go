package tools

// B1（S19-H-A）· skill 信任门的**生产接线断言 + 判别器**（production-path-tdd R1/R2/R4/R5）。
//
// 移交单：内部工作回执
// 决策项：B1（§2.2 族B · I8 外部代码 ⊥ 权限）
//
// 与 executor_trust_gate_test.go（hook 版）同形，理由相同：门本体的测试（internal/skill）
// 只能证明「门本身有效」；若生产装配处忘了调 EnableTrustGate()，那道门在生产里就是死代码。
// 本文件钉死三件事：
// ① 生产装配入口确实开了门（防「接线被摘掉」无人发现）；
// ② 门开启后 project 来源正文**不再**进入模型上下文（真实生产链路，不是直调门函数）；
// ③ 反向守卫 —— user 来源与「确认一次之后」照常放行（防被实现成"一律拦"）。
//
// 判别性（A/B）：把 executor.go 里 `e.SkillMgr.EnableTrustGate()` 一行回退 ⇒
// ①直接打红；②因正文重新放行而打红；③不翻转（证明未越界）。
//
// ⚠️ 夹具复用同包 audit_char_s19_test.go 的 s19WriteFile / s19SkillMarkdown，
// 避免出现第二份「skill markdown 构造器」的影子实现。

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/anchorandfish/whale-code/internal/permission"
	"github.com/anchorandfish/whale-code/internal/testutil"
)

// gtExecutorAtProductionEntry 走**生产装配入口**：
// tui/lifecycle.go:222 与 tui/runner_core.go:366 调的就是 NewExecutorWithConfig。
func gtExecutorAtProductionEntry(t *testing.T, workDir string) *Executor {
	t.Helper()
	e, err := NewExecutorWithConfig(NewRegistry(), workDir, nil)
	if err != nil {
		t.Fatalf("NewExecutorWithConfig 失败: %v", err)
	}
	if e.SkillMgr == nil {
		t.Fatal("前提失效：SkillMgr 未初始化，本用例不能代表生产行为")
	}
	return e
}

// gtExecuteSkill 走生产链路：InitPipelineWithHooks（与 tui 一致）→ ExecuteWithConfirm。
func gtExecuteSkill(t *testing.T, e *Executor, ws, name string) Result {
	t.Helper()
	pol := permission.NewPolicy(permission.LevelWorkspaceWrite)
	e.InitPipelineWithHooks(pol, ws)

	res := e.ExecuteWithConfirm(context.Background(), Call{
		ID:       "gt-b1-" + name,
		Name:     "Skill",
		Input:    `{"name":"` + name + `"}`,
		Approved: true, // 用户已批准 Skill 调用本身；本用例考察的是 skill 正文的去向
	})
	if !res.Proceed || res.Result == nil {
		t.Fatalf("Skill 工具未走到 handler，无法观测正文去向：%+v", res)
	}
	return *res.Result
}

// GT-B1-p1 · 生产入口装配后，门必须是开的。
//
// 判别性：删掉 executor.go 里的 EnableTrustGate() ⇒ 本条打红（唯一的断言就是它）。
func TestGT_B1_P1_ProductionEntryEnablesSkillTrustGate(t *testing.T) {
	t.Setenv("HOME", testutil.IsolatedHomeDir(t))
	e := gtExecutorAtProductionEntry(t, t.TempDir())

	if !e.SkillMgr.TrustGateEnabled() {
		t.Fatalf("FAIL: skill 信任门在生产装配入口未开启 —— " +
			"internal/skill/skill.go:248/277 的 ensureInjectionAllowed 在生产里恒放行（门=死代码）。" +
			"EnableTrustGate() 必须在 NewExecutorWithConfig 的 e.SkillMgr.LoadAll() 之后调用。")
	}
}

// GT-B1-p2 · 判别器（行为面）：project 来源 skill 的正文在生产链路里不再进入模型上下文。
//
// 判别性：回退接线 ⇒ 门关 ⇒ 正文原样返回 ⇒ 本条打红。
func TestGT_B1_P2_ProjectSkillBodyBlockedAtProductionEntry(t *testing.T) {
	t.Setenv("HOME", testutil.IsolatedHomeDir(t))
	ws := t.TempDir()

	const marker = "GT_B1_PROJECT_MARKER_IGNORE_ALL_PREVIOUS_INSTRUCTIONS"
	s19WriteFile(t,
		filepath.Join(ws, ".whale", "skills", "gtproj.md"),
		s19SkillMarkdown("gtproj", marker, nil))

	e := gtExecutorAtProductionEntry(t, ws)
	if s, ok := e.SkillMgr.Get("gtproj"); !ok || s.Source != "project" {
		t.Fatalf("前提失效：project 来源 skill 未按 project 加载（ok=%v）", ok)
	}

	got := gtExecuteSkill(t, e, ws, "gtproj").Output

	if strings.Contains(got, marker) {
		t.Fatalf("I8 违例：project 来源 skill 正文进入了模型上下文。Output=%q", got)
	}
	if !strings.Contains(got, "blocked by trust gate") {
		t.Fatalf("期望返回结构化的信任门拒绝（ErrSkillUntrusted 的文案），实际 Output=%q", got)
	}
}

// GT-B1-p3 · 反向守卫：user 来源免确认，门开启后行为**不变**。
//
// 判别性：本条在 before/after 两态都必须绿 —— 它守的是「别把门实现成一律拦」。
func TestGT_B1_P3_UserSourcedSkillStillInjected(t *testing.T) {
	home := testutil.IsolatedHomeDir(t)
	t.Setenv("HOME", home)
	ws := t.TempDir()

	const marker = "GT_B1_USER_MARKER"
	s19WriteFile(t,
		filepath.Join(home, ".whale", "skills", "gtuser.md"),
		s19SkillMarkdown("gtuser", marker, nil))

	e := gtExecutorAtProductionEntry(t, ws)
	if s, ok := e.SkillMgr.Get("gtuser"); !ok || s.Source != "user" {
		t.Fatalf("前提失效：user 来源 skill 未按 user 加载（ok=%v）", ok)
	}

	got := gtExecuteSkill(t, e, ws, "gtuser").Output
	if !strings.Contains(got, marker) {
		t.Fatalf("user 来源免确认，正文应照常注入；实际 Output=%q", got)
	}
}

// GT-B1-p4 · 门是「首次确认一次」而不是砖墙：确认后同一 skill 正文放行。
//
// 这条把 B1 拍板语义（"首次使用确认一次"）钉在生产链路上；同时是 p2 的反面 ——
// 证明 p2 的红不是"门坏了"而是"确实未确认"。
func TestGT_B1_P4_ProjectSkillAllowedAfterExplicitTrust(t *testing.T) {
	t.Setenv("HOME", testutil.IsolatedHomeDir(t))
	ws := t.TempDir()

	const marker = "GT_B1_CONFIRMED_MARKER"
	s19WriteFile(t,
		filepath.Join(ws, ".whale", "skills", "gtconf.md"),
		s19SkillMarkdown("gtconf", marker, nil))

	e := gtExecutorAtProductionEntry(t, ws)

	// 确认一次（等价于 /skill trust gtconf）
	if err := e.SkillMgr.TrustSkill("gtconf"); err != nil {
		t.Fatalf("TrustSkill 失败: %v", err)
	}

	got := gtExecuteSkill(t, e, ws, "gtconf").Output
	if !strings.Contains(got, marker) {
		t.Fatalf("显式确认后正文应放行；实际 Output=%q", got)
	}
	// 反向：确认写入的信任库不得污染真实 HOME（本用例已重定向 HOME，这里显式核一遍）
	if _, err := os.Stat(filepath.Join(os.Getenv("HOME"), ".whale", "skills.trust.json")); err != nil {
		t.Logf("提示：信任库未落盘（%v）—— 若机制改为纯内存，请同步更新本断言", err)
	}
}
