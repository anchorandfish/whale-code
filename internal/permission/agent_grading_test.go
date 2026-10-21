package permission

import "testing"

// ============================================================================
// 阶段 3 · Agent 定级 + 记忆键改型（先红后绿）
//
// 事实（改动前）：
// - defaultToolRequirements["Agent"] = LevelWorkspaceWrite（permission.go:653）
// → read-only 档下 Agent 一律被拒；workspace-write 档下一律需确认。
// （生产路径：pipeline.go:930 PermissionStage → Policy.CheckWithRules → checkModeLocked）
// - fingerprint(toolName, input) 对整段 input 做 sha256（permission.go:931）
// → Agent 入参含每次不同的 prompt，指纹每次不同，「允许本次」永不命中。
//
// 为什么按 agent_type 定级是安全的（不扩权）：
// - 子代理自身执行的每个工具调用都会独立经过本 Policy（agent.go executeToolCall →
// ExecuteWithPipeline → PermissionStage），写操作仍会被二次拦截；
// - plan/explore 的工具白名单在工具层已收敛为只读集（agent.go getAllowedToolSet）。
// 因此「按声明的 agent_type 定级」不会让写操作溜过去 —— 声明的类型是被**强制**的，不是被信任的。
//
// 对齐业内既有实现 / 同类 CLI：
// - 同类 CLI：能力按 isReadOnly(input) 判定，规则按 Tool(pattern) 匹配，而非整段输入
//, 584-597）
// - 同类 CLI：能力轴与审批轴正交；被委派的能力由子代理沙箱决定
//
// 定级规则：plan / explore → ReadOnly；coder / verification / fork / 缺省 / 未知 → WorkspaceWrite。
// ============================================================================

func agentInput(agentType, prompt string) string {
	if agentType == "" {
		return `{"prompt":"` + prompt + `"}`
	}
	return `{"prompt":"` + prompt + `","agent_type":"` + agentType + `"}`
}

// --- 1. 只读子代理不再被当成写操作拦截（核心目标） ---

func TestCheck_AgentReadOnlyTypeAllowedInReadOnlyMode(t *testing.T) {
	p := NewPolicy(LevelReadOnly)

	for _, at := range []string{"plan", "explore"} {
		allowed, needConfirm := p.Check("Agent", agentInput(at, "分析这个仓库"))
		if !allowed {
			t.Errorf("read-only 档下只读子代理 agent_type=%q 不应被拒（工具层已强制其只读）", at)
		}
		if needConfirm {
			t.Errorf("read-only 档下只读子代理 agent_type=%q 不应需确认，got needConfirm=true", at)
		}
	}
}

func TestCheck_AgentReadOnlyTypeNoConfirmInWorkspaceWrite(t *testing.T) {
	p := NewPolicy(LevelWorkspaceWrite)

	for _, at := range []string{"plan", "explore"} {
		allowed, needConfirm := p.Check("Agent", agentInput(at, "分析这个仓库"))
		if !allowed {
			t.Errorf("workspace-write 档下只读子代理 agent_type=%q 应允许", at)
		}
		if needConfirm {
			t.Errorf("workspace-write 档下只读子代理 agent_type=%q 不应弹确认（它无法写任何东西）", at)
		}
	}
}

// --- 2. 写能力子代理维持原语义（防过度放松） ---

func TestCheck_AgentWriteTypesStillConfirmInWorkspaceWrite(t *testing.T) {
	p := NewPolicy(LevelWorkspaceWrite)

	// coder / verification（持 bash）/ fork（继承父上下文全部工具）都是可写子代理。
	//
	// ⚠️ L2（2026-09-14 用户拍板 §7⑤）：**缺省（无 agent_type）不再等价 coder** ——
	// 默认角色改为 explore（只读，委派默认不扩权），见 TestCheck_AgentUnspecifiedTypeUsesDefaultRole。
	for _, at := range []string{"coder", "verification", "fork"} {
		allowed, needConfirm := p.Check("Agent", agentInput(at, "改点代码"))
		if !allowed {
			t.Errorf("workspace-write 档下可写子代理 agent_type=%q 应允许（带确认）", at)
		}
		if !needConfirm {
			t.Errorf("workspace-write 档下可写子代理 agent_type=%q 必须确认，got needConfirm=false", at)
		}
	}
}

// TestCheck_AgentUnspecifiedTypeUsesDefaultRole 钉住「未声明 agent_type」的语义（L2）。
//
// 拍板（§7⑤）：未声明 → DefaultAgentTypeName = explore ⇒ **只读** ⇒
// workspace-write 档下允许且免确认，read-only 档下也允许。
// 此前未声明等价 coder（写档）—— 这正是 F2「声明不生效」的另一半。
func TestCheck_AgentUnspecifiedTypeUsesDefaultRole(t *testing.T) {
	payload := agentInput("", "读取 README 第一行")

	ww := NewPolicy(LevelWorkspaceWrite)
	allowed, needConfirm := ww.Check("Agent", payload)
	if !allowed {
		t.Error("未声明 agent_type 应落到默认只读角色，workspace-write 档下应允许")
	}
	if needConfirm {
		t.Error("默认角色是只读（explore），不应弹确认")
	}

	ro := NewPolicy(LevelReadOnly)
	if allowedRO, _ := ro.Check("Agent", payload); !allowedRO {
		t.Error("未声明 agent_type 的默认角色是只读，read-only 档下应允许（委派默认不扩权）")
	}
}

func TestCheck_AgentWriteTypesDeniedInReadOnly(t *testing.T) {
	p := NewPolicy(LevelReadOnly)

	for _, at := range []string{"coder", "verification", "fork"} {
		if allowed, _ := p.Check("Agent", agentInput(at, "改点代码")); allowed {
			t.Errorf("read-only 档下可写子代理 agent_type=%q 必须被拒（能力超出沙箱）", at)
		}
	}
}

// --- 3. 未知类型 fail-closed：按「写」处理，绝不当成只读免问 ---

func TestCheck_AgentUnknownTypeTreatedAsWrite(t *testing.T) {
	p := NewPolicy(LevelWorkspaceWrite)

	allowed, needConfirm := p.Check("Agent", agentInput("totally-bogus", "x"))
	if !allowed {
		t.Errorf("未知 agent_type 不应被拒（工具层会报 invalid agent_type），但必须走写档确认")
	}
	if !needConfirm {
		t.Errorf("未知 agent_type 必须按写档确认（fail-closed），不得被当成只读免问")
	}

	// 非 JSON 入参同样 fail-closed 到写档
	allowed, needConfirm = p.Check("Agent", `not-json-at-all`)
	if !allowed || !needConfirm {
		t.Errorf("畸形入参应 fail-closed 到写档（allowed=true,needConfirm=true），got (%v,%v)", allowed, needConfirm)
	}
}

// --- 4. 记忆键：按 agent_type 而非整段 prompt ---

func TestMemory_AgentApprovalKeyedByAgentType(t *testing.T) {
	p := NewPolicy(LevelWorkspaceWrite)

	// 用户对一次 explore 子代理点了「允许本次/始终允许」
	p.Remember("Agent", agentInput("explore", "第一个任务"), true)

	// 同类型、不同 prompt 的下一次调用应命中同一次授权
	if !p.Has("Agent", agentInput("explore", "完全不同的第二个任务")) {
		t.Errorf("同为 explore 的不同 prompt 应命中同一次授权（记忆键按 agent_type，而非整段 prompt）")
	}
}

func TestMemory_AgentApprovalIsolatedByAgentType(t *testing.T) {
	p := NewPolicy(LevelWorkspaceWrite)

	p.Remember("Agent", agentInput("explore", "任务"), true)

	if p.Has("Agent", agentInput("coder", "任务")) {
		t.Errorf("不同 agent_type 之间不得共享授权（explore 的允许不能外溢到 coder）")
	}
	if p.Has("Agent", agentInput("fork", "任务")) {
		t.Errorf("不同 agent_type 之间不得共享授权（explore 的允许不能外溢到 fork）")
	}
}

// --- 5. 回归保护：非 Agent 工具的记忆键仍是「全量输入」，不得被本次改动放宽 ---

func TestMemory_NonAgentToolsStillKeyedByFullInput(t *testing.T) {
	p := NewPolicy(LevelWorkspaceWrite)

	p.Remember("write_file", `{"path":"a.go"}`, true)

	if p.Has("write_file", `{"path":"b.go"}`) {
		t.Errorf("write_file 的记忆键必须仍区分路径（不同 path 不得共享授权）")
	}
	if !p.Has("write_file", `{"path":"a.go"}`) {
		t.Errorf("write_file 相同入参应命中授权（本次改动不得影响非 Agent 工具）")
	}
}

// --- 6. 只读子代理分类是单一真相源 ---

func TestIsReadOnlyAgentType_CanonicalSet(t *testing.T) {
	readOnly := []string{"plan", "explore", "PLAN", " Explore "}
	for _, at := range readOnly {
		if !IsReadOnlyAgentType(at) {
			t.Errorf("IsReadOnlyAgentType(%q) = false, want true", at)
		}
	}
	writeTypes := []string{"coder", "verification", "fork", "", "bogus"}
	for _, at := range writeTypes {
		if IsReadOnlyAgentType(at) {
			t.Errorf("IsReadOnlyAgentType(%q) = true, want false（未知/缺省必须归写档）", at)
		}
	}
}
