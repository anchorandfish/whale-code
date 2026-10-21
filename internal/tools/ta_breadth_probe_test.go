package tools

import (
	"context"
	"strings"
	"testing"
)

// ============================================================================
// L2 守卫 —— agent_type 声明即真相（原 T-A 现场探针，R1 已把缺陷修掉并转成守卫）
// ============================================================================
//
// 现场（2026-09-13 TUI 手测 T-A）：主 agent 一轮发了 6 个 `Agent` 调用，其中 3 个
// 声明了 agent_type=explore；用户发现仍有子 agent 能绕过 read_file 的 deny 去读/写。
//
// 根因（F2，已修）：声明值只被用来算一份「免问」名单，真正决定子 agent 工具面的是
// `inferAgentType(prompt)` —— 按 prompt 关键字猜，猜不中落 coder（全量工具面，含
// bash/write_file）。中文「读取 README 第一行」不含任何关键字 ⇒ 一律 coder。
//
// 本守卫断言修复后的契约（三个都必须成立，缺一即回到缺陷态）：
// 1. **声明即真相**：工具面只由 AgentType 决定，prompt 文本不影响；
// 2. **未声明 → 默认只读角色**（explore，委派默认不扩权）；
// 3. **未知值 → fail-closed 硬报错**且零执行（不得静默降级为 coder）；
// 4. **委派不扩权**：子工具面 ⊆ 父工具面。
//
// 判别力：把 Spawn 的角色解析改回 `inferAgentType(input.Prompt)` → 第 3 条必红；
// 把角色工具面改回全量 → 第 1/2 条必红。
func TestGuard_S4_DeclaredAgentTypeIsEnforced(t *testing.T) {
	// 现场那条 prompt（中文、无关键字）—— 修复前它一定被猜成 coder。
	livePrompt := "读取项目里的 README.md 并返回第一行"

	writeish := []string{"bash", "write_file", "edit_file", "git_add", "config_set", "export_session"}

	// ── 1. 声明即真相：prompt 不同、声明相同 → 工具面相同（且写工具被挡）──
	declared := NewAgentContext(nil, livePrompt, "", ".", 1)
	declared.AgentType = AgentTypeExplore
	ds := declared.getAllowedToolSet()
	for _, tool := range writeish {
		if ds[tool] {
			t.Errorf("声明 explore（只读）却拿到写/执行工具 %s —— 声明未生效（F2 回归）", tool)
		}
	}
	// 同一角色 + 另一条 prompt（含 "explore"/"plan" 关键字）→ 面必须不变，证明 prompt 不参与。
	other := NewAgentContext(nil, "plan and explore the design, then verify", "", ".", 1)
	other.AgentType = AgentTypeExplore
	if !sameStringSet(ds, other.getAllowedToolSet()) {
		t.Error("同一 AgentType 在两条不同 prompt 下工具面不同 —— prompt 仍在参与决策（F2 回归）")
	}
	// 声明 coder + 只读措辞的 prompt → 仍是 coder 面（写工具在），证明声明压过措辞。
	coder := NewAgentContext(nil, "read the README first line", "", ".", 1)
	coder.AgentType = AgentTypeCoder
	cs := coder.getAllowedToolSet()
	if !cs["bash"] || !cs["write_file"] {
		t.Error("声明 coder 却拿不到 bash/write_file —— 工具面与声明不符")
	}

	// verification = 只读 ∪ {bash}：能跑命令，但不能写文件。
	ver := NewAgentContext(nil, livePrompt, "", ".", 1)
	ver.AgentType = AgentTypeVerification
	vs := ver.getAllowedToolSet()
	if !vs["bash"] {
		t.Error("verification 角色应能使用 bash（对抗性验证需要跑命令）")
	}
	if vs["write_file"] || vs["edit_file"] {
		t.Error("verification 角色不应能写文件")
	}

	// 委派型 Agent 只给 coder/fork，且不超过深度上限。
	//
	// 注意判据不是 getAllowedToolSet()：那一层是**白名单执行面**，且 "Agent" 的分发在
	// agent.go:520 就短路到 handleNestedAgentCall（永不经过白名单门）。委派能力由
	// mayDelegate() 表达，可见面由 buildAgentTools() 追加 —— 两者同源。
	if declared.mayDelegate() {
		t.Error("只读角色不应持有委派能力 Agent")
	}
	if !coder.mayDelegate() {
		t.Error("coder 角色（未达最大深度）应持有委派能力 Agent")
	}
	if cs["Agent"] {
		t.Error("getAllowedToolSet 不应含 Agent（委派能力由 mayDelegate 表达，不进白名单面）")
	}

	// ── 2. 未声明 → 默认角色（只读，委派默认不扩权）──
	def, ok := ParseAgentType("")
	if !ok {
		t.Fatal("空 agent_type 不应被判为非法")
	}
	if def != DefaultAgentType {
		t.Errorf("空 agent_type 应取默认角色 %s，得到 %s", DefaultAgentType, def)
	}
	defCtx := NewAgentContext(nil, livePrompt, "", ".", 1)
	defCtx.AgentType = def
	if defCtx.getAllowedToolSet()["bash"] || defCtx.getAllowedToolSet()["write_file"] {
		t.Error("未声明 agent_type 的子 agent 拿到了写/执行工具 —— 委派默认扩大了权限（违反 §7⑤）")
	}

	// ── 3. 未知值 → fail-closed，零执行 ──
	mgr := NewAgentTaskManager(1, 4, nil)
	out, err := mgr.Spawn(context.Background(), AgentInput{Prompt: "x", AgentType: "bogus_role"}, "", 0)
	if err == nil {
		t.Fatal("未知 agent_type 必须 fail-closed 报错，实际返回 nil error（静默降级 F2 回归）")
	}
	if !strings.Contains(err.Error(), "unknown agent_type") {
		t.Errorf("错误文案应点明 unknown agent_type，实际：%v", err)
	}
	if out.TaskID != "" {
		t.Errorf("未知 agent_type 不得创建任务（零执行），却拿到 TaskID=%s", out.TaskID)
	}

	// ── 4. 委派不扩权：子面 ⊆ 父面 ──
	parent := map[string]bool{"read_file": true, "grep_search": true}
	child := NewAgentContext(nil, livePrompt, "", ".", 1)
	child.AgentType = AgentTypeCoder
	child.ParentToolSet = parent
	for name := range child.getAllowedToolSet() {
		if !parent[name] {
			t.Errorf("子工具面含父面之外的 %s —— 委派扩权（I2 回归）", name)
		}
	}
	if !child.getAllowedToolSet()["read_file"] || !child.getAllowedToolSet()["grep_search"] {
		t.Error("父面内的 read_file/grep_search 应保留在子面里（收窄过度）")
	}
}

// sameStringSet 比较两个集合（map[string]bool）是否相等。
func sameStringSet(a, b map[string]bool) bool {
	if len(a) != len(b) {
		return false
	}
	for k, v := range a {
		if b[k] != v {
			return false
		}
	}
	return true
}

// TestProbe_S4_TA_ToolCallLoopIsSequential 钉住 T-A 的另一半事实：
// TUI 生产路径（Pipeline != nil）里一轮内的多个工具调用是**串行**执行的，
// 因此「6 个子 agent 并行」在当前实现下不可能成立，maxConcurrent 也永远不会被观测到。
//
// 证据在代码层（无法用单测直接断言 goroutine 时序），此处只做可执行的形式化记录：
// - tool_exec.go:259-293 Pipeline != nil → for 循环逐个 ExecuteWithConfirm
// - executor.go:861-914 ExecuteBatch 仅 Pipeline==nil 回退路径可达
// - tools.go:491 Agent spec ConcurrencySafe=false → 即便走 ExecuteBatch 也进 unsafe 串行桶
// - agent_task.go:543 SpawnParallel 无任何生产调用方（仅测试）
func TestProbe_S4_TA_ToolCallLoopIsSequential(t *testing.T) {
	// 用真实注册表（与 Executor.registry 同一真相源，Executor.IsConcurrencySafe 只是委托它）
	reg := NewRegistry()
	if reg.IsConcurrencySafe("Agent") {
		t.Errorf("行为已变：Agent spec 现为 ConcurrencySafe=true —— TUI 回退路径会并发跑子 agent")
	}
	if !reg.IsConcurrencySafe("read_file") {
		t.Errorf("行为已变：read_file 不再是 ConcurrencySafe —— ExecuteBatch 分桶语义变了")
	}
	if !reg.IsConcurrencySafe("Task") {
		t.Logf("Task ConcurrencySafe=%v（与 Agent 相反：Task 在回退路径会并发）", reg.IsConcurrencySafe("Task"))
	}
	t.Logf("Agent  ConcurrencySafe=%v  → ExecuteBatch 中落入 unsafe 串行桶", reg.IsConcurrencySafe("Agent"))
	t.Logf("结论：6 个子 agent 是严格串行执行，非并行。")
}
