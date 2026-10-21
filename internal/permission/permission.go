// Package permission — permission policy, security analysis, permission memory
package permission

import (
	"crypto/sha256"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"github.com/anchorandfish/whale-code/internal/security/cmdrules"
	"github.com/anchorandfish/whale-code/internal/util"
)

// Level 是权限模式（阶段 2 收模式：8 → 3，对齐业内既有实现 三档沙箱）。
//
// 三档是「能力（能不能做）」维度，与「审批（要不要问）」维度正交：
// - read-only：只读；写操作一律拒绝。
// - workspace-write：工作区可写，写操作默认需确认（默认 ask）。
// - danger-full-access：全放行，但危险命令（rm -rf / 等）仍保留二次确认护栏。
//
// 历史上曾有 5 个额外档位（accept-edits / dont-ask / plan / auto / bubble），
// 它们表达的是「审批策略」而非「能力」，与规则引擎职责重叠，故已收敛：
// - accept-edits → workspace-write + 编辑/安全命令的默认 allow 规则（见 SetModeByName）
// - dont-ask → danger-full-access（其危险命令护栏由 danger 的二次确认承接）
// - plan → read-only（行为本就相同）
// - auto → 移除（同类 CLI 权限决策是确定性的，AI 裁决不可审计）
// - bubble → 子代理委派参数（由 BubblePolicy 承担，不再是会话档）
//
// 旧字符串仍可被 ParseLevel 解析（向后兼容），但不再产生独立档位。
type Level int

const (
	LevelReadOnly Level = iota
	LevelWorkspaceWrite
	LevelDangerFullAccess
)

// String returns the string representation of permission level
func (l Level) String() string {
	switch l {
	case LevelReadOnly:
		return "read-only"
	case LevelWorkspaceWrite:
		return "workspace-write"
	case LevelDangerFullAccess:
		return "danger-full-access"
	default:
		return "unknown"
	}
}

// ParseLevel 是模式字符串 → Level 的**单一解析入口**（向后兼容）。
//
// 已删除的模式字符串不会报错，而是翻译到 3 档之一：
// - accept-edits → workspace-write（调用方如需「编辑免问」语义，用 SetModeByName）
// - dont-ask → danger-full-access
// - plan → read-only
// - auto → workspace-write（保守的确定性默认，不再有 AI 分类器档）
// - bubble → workspace-write（子代理上浮改由 BubblePolicy 承担）
//
// 未知字符串回退到 workspace-write（与历史行为一致）。
func ParseLevel(s string) Level {
	level, _ := LookupModeName(s)
	return level
}

// LookupModeName 解析模式名并返回「是否已知」。
//
// 这是模式名 → Level 的**唯一真相源**：ParseLevel、Policy.SetModeByName、TUI 的
// /permissions <mode> 都经由此处，避免多处解析实现漂移（影子实现）。
// 与 ParseLevel 的区别：这里能区分「已知的向后兼容别名」与「真正未知的输入」，
// 未知输入返回 (LevelWorkspaceWrite, false)，调用方可据此报错而非静默回退。
func LookupModeName(s string) (Level, bool) {
	switch strings.ToLower(strings.TrimSpace(strings.ReplaceAll(s, "_", "-"))) {
	case "read-only", "readonly":
		return LevelReadOnly, true
	case "workspace-write", "workspacewrite":
		return LevelWorkspaceWrite, true
	case "danger-full-access", "dangerfullaccess":
		return LevelDangerFullAccess, true
	// 已删除模式（向后兼容翻译）
	case "accept-edits", "acceptedits":
		return LevelWorkspaceWrite, true
	case "dont-ask", "dontask":
		return LevelDangerFullAccess, true
	case "plan":
		return LevelReadOnly, true
	case "auto":
		return LevelWorkspaceWrite, true
	case "bubble":
		return LevelWorkspaceWrite, true
	default:
		return LevelWorkspaceWrite, false
	}
}

// legacyAcceptEditsRules 返回复刻 accept-edits 语义所需的默认 allow 规则。
//
// accept-edits 的原始语义（checkModeLocked 旧分支）：
// - write_file / edit_file / NotebookEdit 免问
// - 安全 bash / PowerShell 免问
// - 危险 bash / PowerShell 仍需确认
// - 其他写操作（如 Agent）仍需确认
//
// 这些是「审批策略」，与 workspace-write 的能力叠加即可精确复刻：
// 把编辑工具与 shell 工具登记为 allow，allow 分支内部仍会先查沙箱能力（P1 硬边界），
// 并对危险命令保留确认。于是无需保留独立档位。
func legacyAcceptEditsRules() []PermissionRule {
	// ⚠️ P-002（2026-09-22 反缝补扫描）——本名单**刻意保持内容原样**（只加注释）：
	//
	// - `PowerShell` 是幽灵名（从未注册），但它在此只是一条"指向不存在工具的 allow
	// 规则"，**零效果**；删掉它会改动 `RuleSet().Count()` —— 该计数被
	// `permission_mode_collapse_test.go:264` 钉死为 5，且 TUI 人工测试 B-1 的
	// 「Rules: 5 active」以此为准 ⇒ 属**协调变更**，随 P-003（写类/进程类名单按桶派生）
	// 一起做，不在本轮顺手改计数。
	// - `Monitor` **不得**加进来：本函数产出的是 **allow 名单**，加成员 = accept-edits
	// 模式下 Monitor 免问 ⇒ 那是**放宽**，与本轮单向收窄的方向相反
	// （危险/破坏性命令另由 allow 分支内的危险档检查兜住，见本文件 checkModeLocked）。
	tools := []string{"write_file", "edit_file", "NotebookEdit", "bash", "PowerShell"}
	rules := make([]PermissionRule, 0, len(tools))
	for _, t := range tools {
		rules = append(rules, PermissionRule{
			ToolName: t,
			Behavior: RuleAllow,
			Source:   SourceSession,
		})
	}
	return rules
}

// Policy permission policy
//
// L1起**不再持有工具档位表**：工具档位的唯一真相源是工具注册表里的
// Spec.RequiredPermission，由 RequiredLevelOf 经注入的目录来源查询（见 level.go）。
// 此前那份 defaultToolRequirements() 手写表已删除 —— 它是 F5「级别双定义并漂移」的成因。
type Policy struct {
	mu              sync.RWMutex
	mode            Level
	memory          *Memory
	allowedTools    map[string]bool
	memoryStorePath string
	ruleSet         *RuleSet
	ruleStore       *RuleStore
	// M12-02/M12-03/M12-04 SNAPSHOT：权限体验增强字段
	dialog    PermissionDialog        // M12-02：专用对话框（nil 时退化为 needConfirm=true）
	hooks     []PermissionRequestHook // M12-03：权限请求监听器列表
	modeCycle *ModeCycle              // M12-04：模式循环切换器（Shift+Tab）
	// dangerousGuardrailDisabled 关闭「危险命令二次确认」护栏。
	// 零值 = 护栏生效（安全侧默认）；只有显式无人值守的装配点才置 true。
	// 见 SetDangerousCommandGuardrail。
	dangerousGuardrailDisabled bool
	// prefixMemory 是「允许该命令前缀」的**会话级**记忆（，2026-10-06 用户拍板）。
	//
	// 键 = 工具名 → 命令前缀头词（`cmdrules.CommandHead`，仅"单一简单命令"才有头词）→ true。
	//
	// ★ 与 memory 的**同一条纪律**（严格会话级）：只在进程内生效，不落盘、不跨重启；
	// 任何路径都不得把审计内容读回这里（留痕 ≠ 授权）。
	// ★ 与 memory 的**分工**：memory 记「这一条确切输入已批准」（逐字），
	// prefixMemory 记「这个命令头词已批准」（同头、且仍须是单一简单命令）。
	// 两者互不转化（见 prefix_memory_test.go 的 TestW022_PrefixIsNotFullSessionApproval）。
	//
	// 懒初始化：即使有装配点绕过 NewPolicy 造出零值 Policy 也安全（读 nil map 返回零值）。
	prefixMemory map[string]map[string]bool
}

// NewPolicy creates a permission policy
func NewPolicy(mode Level) *Policy {
	return &Policy{
		mode:         mode,
		memory:       NewMemory(256),
		allowedTools: make(map[string]bool),
		ruleSet:      NewRuleSet(),
		prefixMemory: make(map[string]map[string]bool),
	}
}

// NewPolicyFromModeName 按模式名构造 Policy（启动装配入口）。
//
// 与 NewPolicy(configToPermissionLevel(...)) 的区别：这里经 SetModeByName，
// 会把已删除模式的审批语义一并迁移（如 accept-edits 注入编辑/安全命令的默认
// allow 规则），使配置里的旧模式在启动时得到与运行期切换一致的语义。
// 未知名称回退 workspace-write（与 ParseLevel 一致）。
func NewPolicyFromModeName(name string) *Policy {
	p := NewPolicy(LevelWorkspaceWrite)
	p.SetModeByName(name)
	return p
}

// Check 是唯一的权限判定入口：先过规则系统，未命中规则时再按当前模式判定。
// returns: allowed, needs confirmation
//
// 历史背景：此前 Check 不查规则，只有 CheckWithRules 查。而
// internal/tui/tool_exec.go 与 internal/remote/rpc/client.go 调用的是 Check，
// 导致用户配置的 deny 规则在这两条路径上被静默绕过。
// 现在两者行为完全一致，CheckWithRules 仅作为兼容别名保留。
func (p *Policy) Check(toolName, input string) (allowed bool, needConfirm bool) {
	p.mu.RLock()
	defer p.mu.RUnlock()

	// 规则系统优先。deny 是绝对的：不受模式、不受来源优先级、不受 allow 规则影响
	// （对齐业内既有实现：deny 规则在 bypassPermissions 下同样强制生效）。
	if ruleFound, ruleAllowed, ruleNeedConfirm := p.ruleCheckLocked(toolName, input); ruleFound {
		return ruleAllowed, ruleNeedConfirm
	}

	return p.checkModeLocked(toolName, input)
}

// checkModeLocked 按当前权限模式判定。调用方必须已持有 p.mu 的读锁。
//
// 三档语义（阶段 2 收模式后）：
// - read-only：只读工具放行，其余拒绝。
// - workspace-write：只读放行；写操作允许但默认需确认（allowedTools/memory 可免问；
// 危险命令即便免问也强制确认）。
// - danger-full-access：全部放行且免问；**唯一例外**是危险命令（rm -rf / 等）
// 仍保留二次确认护栏 —— 这条护栏承接了原 dont-ask 档的语义，使「dont-ask
// 并入 danger」不构成扩权。
func (p *Policy) checkModeLocked(toolName, input string) (allowed bool, needConfirm bool) {
	required := requiredForTool(toolName, input)

	switch p.mode {
	case LevelDangerFullAccess:
		// 全放行；危险命令保留二次确认（护栏不变量，承接原 dont-ask 语义）
		if p.guardrailFires(toolName, input) {
			return true, true
		}
		return true, false
	case LevelWorkspaceWrite:
		if required > LevelWorkspaceWrite {
			return false, false
		}
		if p.allowedTools[toolName] {
			// 免问，但危险命令仍强制确认
			if p.guardrailFires(toolName, input) {
				return true, true
			}
			return true, false
		}
		if p.memory.Has(toolName, input) {
			// 免问，但危险命令仍强制确认 —— 与上方 allowedTools 分支是**同一条不变量**
			// （「危险命令即便免问也强制确认」，见函数头三档语义）。
			//
			// ★ -R1：这条检查此前**漏了**。allowedTools 分支有、
			// memory 分支没有 ⇒ 危险命令一旦被记住就变成"此后永久免问"，护栏形同虚设。
			// 这是"同一不变量在两处实现、只改了一处"的典型形态：修法不是补 if，
			// 而是把判据收到唯一的 guardrailFires（本行），并让 Remember 不再记录危险命令
			// （双保险：前者管"存量记忆"，后者管"新增记忆"）。
			if p.guardrailFires(toolName, input) {
				return true, true
			}
			return true, false
		}
		// 未被免问：写操作允许但需确认（只读操作免问）
		return true, required > LevelReadOnly
	case LevelReadOnly:
		if required <= LevelReadOnly {
			return true, false
		}
		return false, false
	default:
		// 未知档位（不应出现）：fail-closed，拒绝且需确认。
		return false, true
	}
}

// ============================================================================
// 委派型工具（Agent）的输入感知定级 —— 阶段 3
// ============================================================================
//
// 第一性原理：权限判定的对象是「这次工具调用能造成多大能力影响」，不是「工具叫什么名字」。
// 对委派型工具 Agent，其能力上限由**被委派子代理的类型**决定，因此需求量级必须是 input
// 的函数，而非静态表里的一刀切（对齐业内既有实现：工具能力按 isReadOnly(input) 判定，
// 而非按工具名固定；见。
//
// 对照事实（改动前）：`defaultToolRequirements()["Agent"] = LevelWorkspaceWrite`，
// 于是一个只能读的 explore/plan 子代理在 read-only 档被直接拒绝、在 workspace-write 档
// 每次都要弹确认 —— 两层认知不一致（工具层 IsReadOnlyAgent 已禁止它写文件）。
//
// 按 agent_type 定级为何**不扩权**（关键安全论证）：
// 1. 子代理自身执行的每个工具调用都会独立经过本 Policy（agent.go executeToolCall →
// ExecuteWithPipeline → PermissionStage），写操作仍会被二次拦截；
// 2. plan/explore 的工具白名单在工具层已收敛为只读集（agent.go getAllowedToolSet）。
// 即：声明的 agent_type 是被**强制**的，不是被信任的 —— 定级只是在描述既已强制的能力。

// readOnlyAgentTypes 是「只读子代理类型」的**单一真相源**。
//
// 放在 permission 包的原因：权限判定必须知道「委派出去的能力上限」，而 permission
// 不能 import tools（tools 已 import permission，会形成环）。tools.AgentContext.IsReadOnlyAgent
// 委托本分类，避免两处各自维护 plan/explore 集合而漂移（影子实现）。
var readOnlyAgentTypes = map[string]bool{
	"plan":    true,
	"explore": true,
}

// DefaultAgentTypeName 是「调用方未声明 agent_type」时使用的默认子代理角色。
//
// L2（2026-09-14 用户拍板 §7⑤）：默认 **explore** —— 委派默认不扩权。
// 此前 tools 层的 inferAgentType 会在未声明时按 prompt 关键字猜，且猜不中一律落 coder
// （全量工具面，含 bash/write_file），这是 F2「声明不生效」的直因；现已删除该推断。
//
// 它必须与 tools 层的 AgentType 默认值同源：tools 侧经 permission.DefaultAgentTypeName
// 取值（tools 可 import permission），不另立常量。
const DefaultAgentTypeName = "explore"

// IsReadOnlyAgentType 判断子代理类型是否为只读（plan / explore）。
//
// 大小写与首尾空白不敏感。缺省/空/未知一律返回 false（归入写档，fail-closed）。
// 工具层（internal/tools/agent.go IsReadOnlyAgent）与本包定级共用此函数。
//
// 注意：**空串不映射为默认角色** —— 调用方应在进入判定前用 DefaultAgentTypeName 填默认值
// （canonicalAgentType 与 tools.Spawn 都这么做）。此处保守返回 false 是刻意的 fail-closed：
// 万一有路径绕过默认值填充，宁可判为写档（更严）也不误判为只读。
func IsReadOnlyAgentType(agentType string) bool {
	return readOnlyAgentTypes[strings.ToLower(strings.TrimSpace(agentType))]
}

// canonicalAgentType 从 Agent 工具入参 JSON 中提取并规范化 agent_type。
//
// 三条分支的语义刻意不同（**不要合并**）：
// - 入参不是合法 JSON（畸形/无法解析）→ **fail-closed 归入写档**（返回 "coder"）。
// 解析失败时我们无法知道调用方想声明什么，按最严格的一档处理才安全。
// - JSON 合法但未声明 / 声明为空 → **默认角色** DefaultAgentTypeName（explore）。
// 这是「调用方没说」的情形，对应用户拍板的「委派默认不扩权」（§7⑤）。
// - 声明了未知值 → 按原样返回（IsReadOnlyAgentType 判为写档，fail-closed）。
// 未知值会由 tools 层的 ParseAgentType 硬报错，此处只是权限层的保守兜底。
func canonicalAgentType(input string) string {
	var parsed struct {
		AgentType string `json:"agent_type"`
	}
	if err := json.Unmarshal([]byte(input), &parsed); err != nil {
		// 无法解析 ⇒ 不知道调用方想声明什么 ⇒ 按最严档（写角色）处理。
		// 注意这不是「默认角色」（默认是 explore），两者语义不同，别合并。
		return "coder"
	}
	t := strings.ToLower(strings.TrimSpace(parsed.AgentType))
	if t == "" {
		return DefaultAgentTypeName
	}
	return t
}

// requiredForTool 解析一次工具调用的能力需求量级（「能不能做」维度）。
//
// L1后：非委派型工具的档位一律来自**注入的工具目录**
// （= 注册表里的 Spec.RequiredPermission，唯一真相源），本包不再持有工具档位表。
//
// 委派型工具 Agent 是唯一特例：其需求量级由**入参 agent_type** 决定
// - plan / explore（只读子代理）→ LevelReadOnly
// - verification / coder / fork / 未知 → LevelWorkspaceWrite
//
// （verification 含 bash，故非只读。）
func requiredForTool(toolName, input string) Level {
	if toolName == "Agent" {
		if IsReadOnlyAgentType(canonicalAgentType(input)) {
			return LevelReadOnly
		}
		return LevelWorkspaceWrite
	}
	return RequiredLevelOf(toolName)
}

// isDangerousCommand 判断该工具调用是否属于「危险命令」（**命令载体**命中危险模式）。
// 工具面判据唯一来源 = `IsCommandBearingTool`（见本文件 mustConfirm 上方注释）。
// 供 danger 二次确认护栏与 workspace-write 免问护栏共用，避免两处判断漂移。
// SetDangerousCommandGuardrail 开关「危险命令二次确认」护栏。
//
// 这条护栏的成立前提是**有人在环能回答确认**。无人值守环境（whale --ci）里
// 确认请求无人应答：OnConfirm → promptConfirm 读 stdin 遇 EOF 返回 false，
// 被判为「用户取消」→ 命令静默失败。这比放行更糟——CI 里 `rm -rf dist`
// 这类清理步骤会从成功变成莫名失败。
//
// 因此无人值守的装配点显式关闭它：ci_permission 本就是用户显式 opt-in 的
// 无人值守授权，护栏在此没有应答通道，只会把「放行」扭曲成「拒绝」。
//
// 这是一次**窄豁免**，只关闭危险命令二次确认：
// - 不触碰三档的能力边界（sandbox）——read-only 下写操作依然被拒绝；
// - 不取消 workspace-write 的普通写确认。
//
// 默认开启（零值即生效，安全侧默认）。
func (p *Policy) SetDangerousCommandGuardrail(enabled bool) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.dangerousGuardrailDisabled = !enabled
}

// DangerousCommandGuardrail 返回护栏是否生效。
func (p *Policy) DangerousCommandGuardrail() bool {
	p.mu.RLock()
	defer p.mu.RUnlock()
	return !p.dangerousGuardrailDisabled
}

// guardrailFires 「必须取得用户确认」护栏是否触发。调用方必须已持有 p.mu 的读锁/写锁。
//
// ★ B2/A3语义扩大：此前这里只问「危险档吗」，于是破坏性档
// （rm/mv/shred/dd/fdisk/parted + PowerShell 破坏性 cmdlet）在 `danger-full-access`
// 下由本函数返回 false ⇒ `Policy.Check` 给出「放行且免问」，而本地管线会弹确认。
// 实测矩阵原存于 `internal/e2e/f2_verdict_parity_test.go`（该对照的远端侧随
// 2026-10-11 砍除 /remote 一并删除，本地侧结论并入 cmdrules.MatchConfirm）。
//
// 修法不是给远端补一个 if（那会再造一套语义），而是把「要不要问」收回 cmdrules 的
// 唯一声明处（`cmdrules.MatchConfirm` = 危险档 ∪ 破坏性档）—— 与
// `tools.pipeline.DestructiveCheckStage` 消费同一个结论，两侧不可能再分叉。
func (p *Policy) guardrailFires(toolName, input string) bool {
	if p.dangerousGuardrailDisabled {
		return false
	}
	return mustConfirm(toolName, input)
}

// mustConfirm 「这条命令是否必须取得用户确认」。判据**唯一来源** = cmdrules.MatchConfirm。
//
// 与 isDangerousCommand 的分工（别合并）：
// - mustConfirm（本函数）：回答「要不要问」——含破坏性档。消费方 = guardrailFires（档位判定）。
// - isDangerousCommand：回答「是不是危险档」——只含灾难级。消费方 = Remember
// （危险档不进审批记忆；破坏性档仍可被记住，因为 guardrailFires 已经是双保险）。
//
// ★ P-002（2026-09-22 反缝补扫描）：两个函数的**工具面**判据都改为
// `IsCommandBearingTool`（「命令载体」的唯一声明，由 CapExec 桶派生 = {bash, monitor}）。
//
// 修复前两处各手抄一份工具名对 `!= "bash" && != "PowerShell"`，与真实集合**双向不符**：
// - 多一个**幽灵名** `PowerShell` —— 它从未注册为工具（tools/tools.go 的 registerBuiltinTools
// 无此项、能力表也无此项；守卫见 s4_ssot_level_drift_probe_test.go:177）。
// 让一个不存在的名字参与判定，正是"改对真名"这件事被长期掩盖的原因。
// - 少一个**真身** `Monitor` —— 它的入参就是一条会被执行的命令串（capability.go:105 原文），
// 于是危险/破坏性档对 Monitor **形同不存在**。
//
// ⇒ 为什么用 IsCommandBearingTool 而不是 IsSandboxBoundedTool（两者今天同集合）：
// 本处问的是「**要不要拦**」（集合越宽越安全），而 IsSandboxBoundedTool 问的是
// 「**能不能免问**」（集合越窄越安全）。详见 capability.go 该函数的注释。
func mustConfirm(toolName, input string) bool {
	if !IsCommandBearingTool(toolName) {
		return false
	}
	return cmdrules.IsConfirmRequired(extractCommand(input))
}

func isDangerousCommand(toolName, input string) bool {
	if !IsCommandBearingTool(toolName) {
		return false
	}
	return AnalyzeBashCommand(extractCommand(input)) == BashRiskDangerous
}

// extractCommand extracts command string from tool input JSON
func extractCommand(input string) string {
	var parsed struct {
		Command string `json:"command"`
	}
	if err := json.Unmarshal([]byte(input), &parsed); err != nil {
		return input
	}
	return parsed.Command
}

// ExtractCommand 从工具输入 JSON 中提取 command 字段（导出版本，供 TUI 层调用）。
// 对标 Policy.Check 和 Pipeline.PermissionStage 内部的 extractCommand 提取逻辑，确保一致。
// permission.go:125 Check / pipeline.go:835 PermissionStage
func ExtractCommand(input string) string {
	return extractCommand(input)
}

// Remember records a permission decision.
//
// S9 / HG-C1' / I6：本包是「审批记忆」的**单一存储 + 单一写入点**
// （生效中的那套是 Policy.memory；approval.Manager.cache 在生产路径惰性，见 HANDOFF）。
//
// ★ 严格会话级（2026-09-14 用户拍板，delta E）：
// 「允许本会话」的记忆**只在进程内生效** —— 不落盘、不跨重启。
// Round2 的 G1 曾在此调用 `p.SaveMemory()`，使授权跨重启存活（= 持久授权），
// 与选项文案「Allow for this session / 允许本会话」的字面语义不符 ⇒ **已撤销**。
//
// I6 仍被满足，但改由**留痕（audit，只写不读）**承担：授权本身不持久化，
// 而"谁在何时批了什么"另行落盘。⇒ **不变量：任何路径都不得把持久化的审批记录
// 回读进 Policy.memory**，否则「留痕」会悄悄退化回「持久授权」—— 正是本条要杜绝的形态。
//
// 说明：SaveMemory / LoadMemory / SetMemoryStorePath 保留为**休眠 API**
// （无生产调用点；供将来显式的"永久允许"档复用，当前产品无该档位）。
//
// ★ -R1——**危险档不进入记忆**：
// 「危险命令即便免问也强制确认」是本档的既定契约（见 checkModeLocked 的三档语义注释）。
// 但记忆是"下次别再问"的机制，一旦危险命令进了记忆，第二次就会**跳过确认** ——
// 那时护栏就不是护栏了。⇒ 本函数拒绝记录危险命令，与 `DangerousGuardStage`
// （管线侧强制确认）配对构成同一条语义的两半：管线保证"这次要问"，
// 这里保证"下次还要问"。
//
// 只对**命令载体**生效（`IsCommandBearingTool` = {bash, monitor}，判据唯一声明见其注释）。
//
// ★ P-002（2026-09-22 反缝补扫描）：原注释写「只对 bash / PowerShell 生效（其余工具
// 没有"危险命令"这一维）」—— 这个**前提不成立**：Monitor 的入参就是一条会被执行的
// 命令串（capability.go:105 原文）。⇒ 危险命令经 Monitor 曾被照常写进记忆，
// 第二次直接免问 —— 违反 -R1。管线侧那一半（DangerousGuardStage）当时也没拦住
// Monitor，两半同时漏 ⇒ 该命令**永不需要确认**。
func (p *Policy) Remember(toolName, input string, allowed bool) {
	if isDangerousCommand(toolName, input) {
		// 刻意不写记忆：危险命令每次都必须重新取得用户确认。
		// 留痕由调用方（TUI 审批确认路径）承担，不在此处重复。
		return
	}
	p.mu.Lock()
	p.memory.Set(toolName, input, allowed)
	p.mu.Unlock()
}

// Has check if permission decision exists in memory
func (p *Policy) Has(toolName, input string) bool {
	p.mu.RLock()
	defer p.mu.RUnlock()
	return p.memory.Has(toolName, input)
}

// ============================================================================
// （2026-10-06 用户拍板）· 「允许该命令前缀」的会话级记忆
// ============================================================================
//
// 现场动机：ww 档下**每一条** bash 调用都要确认（gate ① 按"工具是否只读"定级，
// 与具体命令无关）。模型连发 `sleep 60` / `sleep 70` 这类同头命令时，用户要
// 反复点同一件事 —— 而"同一条命令换个参数"并不提供新的决策信息。
//
// 与 memory（"允许本会话"）的区别：memory 的键是**整条输入**（逐字相同才免问），
// 前缀记忆的键是**命令头词**（同头、且仍须是"单一简单命令"才免问）。
//
// ★ 三条结构性收窄（缺一不可，见 prefix_memory_test.go / prefix_exempt_test.go）：
// ① **只认单一简单命令**：`cmdrules.CommandHead` 对复合/含替换/含重定向的命令返回 ""
// ⇒ `sleep 5 && rm -rf x` 根本进不了前缀范围（不靠扫描危险）；
// ② **危险/破坏性永不记、永不免**：`mustConfirm`（危险 ∪ 破坏性）在两处都挡；
// 且 HasCommandPrefix 的检查**不随** `SetDangerousCommandGuardrail(false)` 放宽
// （该开关只关"二次确认"，不得顺带放开前缀面）；
// ③ **严格会话级 + 按工具隔离**：不落盘、不跨重启；bash 的批准不外溢到 Monitor。
//
// ★ 消费点只有一个：`internal/tools/pipeline.go` 的 PermissionStage 免问出口 (d)。
// 刻意**不**在 `checkModeLocked` 里返回 needConfirm=false —— 那会让本豁免绕过
// PermissionStage 内部排在它前面的出口 (0)（自指保护）。顺序不变量由
// `prefix_exempt_test.go` 的 TestGuard_W022_PrefixExemptionSitsAfterSelfProtection 钉住。

// RememberPrefix 记录一次「允许该命令前缀」的用户授权。
//
// 仅对**命令载体**工具（`IsCommandBearingTool` = {bash, Monitor}）生效；命令必须是
// **单一简单命令**（否则没有可信的"前缀"可言）；危险/破坏性命令**不记**
// （与 `Remember` 拒绝危险命令是同一条不变量：护栏的"下次还要问"这一半）。
func (p *Policy) RememberPrefix(toolName, input string) {
	if !IsCommandBearingTool(toolName) {
		return
	}
	// 危险 ∪ 破坏性 ⇒ 不记（mustConfirm 严格强于 isDangerousCommand：还含破坏性档）。
	if mustConfirm(toolName, input) {
		return
	}
	head := cmdrules.CommandHead(extractCommand(input))
	if head == "" {
		// 复合/含替换/含重定向 ⇒ 不构成"前缀"（结构性收窄 ①）。
		return
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.prefixMemory == nil {
		p.prefixMemory = make(map[string]map[string]bool)
	}
	if p.prefixMemory[toolName] == nil {
		p.prefixMemory[toolName] = make(map[string]bool)
	}
	p.prefixMemory[toolName][head] = true
}

// HasCommandPrefix 报告该调用是否命中「允许该命令前缀」的会话级免问。
//
// 三条合取（任一不满足即不免问）：
// 1. 工具是命令载体；
// 2. 命令是单一简单命令，且其头词已被本会话显式批准（RememberPrefix）；
// 3. 该命令**不是**危险/破坏性（`mustConfirm`，与护栏开关**无关**的硬检查）。
//
// 调用方 = PermissionStage 免问出口 (d)；它只把"本来会问"改判成"不问"，
// 从不把"本来会拒"改成"问"（`!allowed` 的拒绝分支在本函数被调用之前）。
func (p *Policy) HasCommandPrefix(toolName, input string) bool {
	p.mu.RLock()
	defer p.mu.RUnlock()
	return p.hasCommandPrefixLocked(toolName, input)
}

// hasCommandPrefixLocked 是 HasCommandPrefix 的持锁版本。
func (p *Policy) hasCommandPrefixLocked(toolName, input string) bool {
	if !IsCommandBearingTool(toolName) {
		return false
	}
	// 危险/破坏性永不豁免 —— 用 mustConfirm（不是 guardrailFires），
	// 这样关闭危险护栏（无人值守装配）也不会顺带放开前缀面。
	if mustConfirm(toolName, input) {
		return false
	}
	head := cmdrules.CommandHead(extractCommand(input))
	if head == "" {
		return false
	}
	m := p.prefixMemory[toolName]
	return m != nil && m[head]
}

// Mode gets the current permission mode
func (p *Policy) Mode() Level {
	p.mu.RLock()
	defer p.mu.RUnlock()
	return p.mode
}

// SetMode sets the permission mode
func (p *Policy) SetMode(mode Level) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.mode = mode
}

// SetRuleSet sets the rule set
func (p *Policy) SetRuleSet(ruleSet *RuleSet) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if ruleSet != nil {
		p.ruleSet = ruleSet
	}
}

// SetRuleStore sets the rule store
func (p *Policy) SetRuleStore(store *RuleStore) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.ruleStore = store
}

// RuleSet gets the current rule set
func (p *Policy) RuleSet() *RuleSet {
	p.mu.RLock()
	defer p.mu.RUnlock()
	return p.ruleSet
}

// RuleStore gets the rule store
func (p *Policy) RuleStore() *RuleStore {
	p.mu.RLock()
	defer p.mu.RUnlock()
	return p.ruleStore
}

// WireRuleStore builds a RuleStore from the standard settings.json paths,
// attaches it to this Policy, and immediately loads all persisted rules into
// the active rule set.
//
// This is the production wiring point that closes the P0 "RuleStore not wired"
// gap: before this call, NewRuleStore/SetRuleStore/SetPathForSource have zero
// non-test call sites, so settings.json allow/deny/ask are never loaded and
// AddRule silently skips persistence (ruleStore == nil).
//
// homeDir/ workDir follow the same layout as config.discoverSources:
//
//	user settings -> <homeDir>/.whale/settings.json
//	project settings -> <workDir>/.whale/settings.json
//	local settings -> <workDir>/.whale/settings.local.json
//
// A load error is returned but not fatal: the store stays attached so later
// /permissions reload and AddRule persistence still work.
func (p *Policy) WireRuleStore(homeDir, workDir string) error {
	store := NewRuleStoreFromPaths(homeDir, workDir)
	p.SetRuleStore(store)

	ruleSet, err := store.LoadAll()
	if err != nil {
		return err
	}
	p.SetRuleSet(ruleSet)
	return nil
}

// SetPriorityMode 设置规则集的优先级模式（透传，02 §4 仲裁契约变更）。
// 调用后 GetMatchingRules 将使用对应模式的 sourcePriority 映射。
func (p *Policy) SetPriorityMode(mode PriorityMode) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.ruleSet != nil {
		p.ruleSet.SetPriorityMode(mode)
	}
}

// PriorityMode 返回规则集的当前优先级模式。
// ruleSet 为 nil 时回填 PriorityPosix（与 NewRuleSet 默认值一致，行业惯例 user > project > default）。
func (p *Policy) PriorityMode() PriorityMode {
	p.mu.RLock()
	defer p.mu.RUnlock()
	if p.ruleSet != nil {
		return p.ruleSet.PriorityMode()
	}
	return PriorityPosix
}

// AddRule adds a rule
func (p *Policy) AddRule(rule PermissionRule) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.ruleSet.Add(rule)
	if p.ruleStore != nil {
		return p.ruleStore.AddRuleToSettings(rule.Source, rule)
	}
	return nil
}

// RemoveRule removes a rule
func (p *Policy) RemoveRule(toolName, ruleContent string, behavior RuleBehavior, source RuleSource) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.ruleSet.Remove(toolName, ruleContent, behavior, source)
	if p.ruleStore != nil {
		rule, _ := ParseRuleString(toolName, behavior, source)
		if ruleContent != "" {
			rule.RuleContent = ruleContent
		}
		return p.ruleStore.RemoveRuleFromSettings(source, *rule)
	}
	return nil
}

// RuleCheck checks rules and returns decision + whether the decision came from rules
// returns: (ruleFound bool, allowed bool, needConfirm bool)
// - ruleFound=false: no matching rule, fall through to normal Check
// - ruleFound=true, allowed=false: denied by rule (block)
// - ruleFound=true, allowed=true, needConfirm=false: allowed by rule (auto-allow)
// - ruleFound=true, allowed=true, needConfirm=true: ask behavior (needs confirmation)
func (p *Policy) RuleCheck(toolName, input string) (ruleFound bool, allowed bool, needConfirm bool) {
	p.mu.RLock()
	defer p.mu.RUnlock()

	return p.ruleCheckLocked(toolName, input)
}

// ruleCheckLocked 查询规则系统。调用方必须已持有 p.mu 的读锁。
//
// 单独抽出这个无锁版本，是为了让 Check 能在同一把读锁内完成「规则 + 模式」两次判定，
// 而不需要二次 RLock —— sync.RWMutex 的读锁重入在有写者等待时会死锁。
func (p *Policy) ruleCheckLocked(toolName, input string) (ruleFound bool, allowed bool, needConfirm bool) {
	found, allow, confirm, _ := p.ruleDecisionLocked(toolName, input)
	return found, allow, confirm
}

// RuleDecision 与 `RuleCheck` 同源同判，额外把**胜出规则的来源**返回给调用方。
//
// 为什么需要它（· 2026-09-20）：管线需要知道「这条 `allow` 是谁给的」——
// 可随仓库分发的来源（`<workDir>/.whale/settings*.json`）**不得**跳过危险档/
// 破坏性确认，而本机用户显式写下的来源（userSettings / session / cliArg）可以。
// `RuleCheck` 只返回三个 bool，来源信息在那里被丢弃 ⇒ 修复前管线只能"一刀切"
// （要么全信、要么全不信），两者都会错。
//
// ★ 与 `RuleCheck` 共用 `ruleDecisionLocked` 这**同一个**内部实现，因此
// 「谁被判定为胜出规则」在全仓只有一处决议 —— 不存在"管线自己再解析一遍规则"
// 的第二份实现（本仓 “ 家族的复发坑）。
func (p *Policy) RuleDecision(toolName, input string) (ruleFound bool, allowed bool, needConfirm bool, source RuleSource) {
	p.mu.RLock()
	defer p.mu.RUnlock()
	return p.ruleDecisionLocked(toolName, input)
}

// ruleDecisionLocked 是规则裁决的**唯一**实现（四元组版本，含来源）。
// 调用方必须已持有 p.mu 的读锁。
func (p *Policy) ruleDecisionLocked(toolName, input string) (ruleFound bool, allowed bool, needConfirm bool, source RuleSource) {
	if p.ruleSet == nil {
		return false, false, false, ""
	}

	rule := p.ruleSet.ResolveRuleDecision(toolName, input)
	if rule == nil {
		return false, false, false, ""
	}
	source = rule.Source

	switch rule.Behavior {
	case RuleDeny:
		return true, false, false, source
	case RuleAllow:
		// P1 决策：沙箱硬边界 —— allow 只能「免问」，不能「扩权」。
		// 对齐业内既有实现：审批策略决定「要不要问」，沙箱（模式）决定「能不能做」，两者正交；
		// 当模式本身不允许该工具（如 read-only 下的写操作）时，allow 规则不能越过硬边界放行。
		// 因此先按模式判定「能不能做」：模式拒绝 → 拒绝（不弹窗）；模式允许 → 由 allow 免问。
		modeAllowed, modeNeedConfirm := p.checkModeLocked(toolName, input)
		if !modeAllowed {
			return true, false, false, source
		}

		// **可随仓库分发的 allow 规则只能收紧，不能放宽。**
		//
		// 修复前 `modeNeedConfirm` 被**丢弃**（`modeAllowed, _ := ...`）⇒ 一条写在
		// `<workDir>/.whale/settings.json` 里的 `allow`（该文件随仓库分发，用户
		// clone 下来就有）足以把「模式本来要求确认」的调用变成免问 —— 即：
		// **他人提供的规则文件降了本机用户的门槛**。这正是 的形态。
		//
		// 判据不是新造的第二套名单，而是把上面**已经算出来**的 `modeNeedConfirm`
		// 用起来：档位说"要问"，可分发来源的 allow 就**不得**把它改成"不问"。
		//
		// 单向收窄（逐条核对，与文件内其它出口同一原则）：
		// - 只把「本来免问」改判成「问」，**从不**把「拒绝」改成「问」：
		// 拒绝已在上一行 `!modeAllowed` 返回，本块不可达；
		// - 不改变"能不能做"，`allowed` 仍为 true；
		// - 用户自己控制的来源（userSettings / session / cliArg）不受影响 ——
		// 那才是"本机用户显式写下的免问"。
		if source.RepoDistributed() && modeNeedConfirm {
			return true, true, true, source
		}
		// Allow rule but dangerous commands still need confirmation
		//
		// ★ P-002（2026-09-22 反缝补扫描）：工具面判据改为「命令载体」的唯一声明
		// （`IsCommandBearingTool` = {bash, monitor}）。修复前是手抄名对
		// `== "bash" || == "PowerShell"` ⇒ 一条 `allow Monitor` 规则 + 危险命令 = **免问**
		// （本行是 allow 分支里唯一的危险档护栏，它不认 Monitor 就等于没有）。
		if IsCommandBearingTool(toolName) {
			cmd := extractCommand(input)
			if AnalyzeBashCommand(cmd) == BashRiskDangerous {
				return true, true, true, source
			}
		}
		return true, true, false, source
	case RuleAsk:
		return true, true, true, source
	default:
		return false, false, false, ""
	}
}

// CheckWithRules 已与 Check 等价，保留仅为兼容既有调用方。
//
// 此前它是唯一会查询规则系统的入口，而 pipeline 之外的调用方（TUI 直接执行、
// daemon/RPC）走的是不查规则的 Check，造成 deny 规则被绕过。
// 现在 Check 内部已包含规则判定，所有调用方路径一致。
// Rules take precedence: deny rules block, allow rules bypass, ask rules confirm
func (p *Policy) CheckWithRules(toolName, input string) (allowed bool, needConfirm bool) {
	return p.Check(toolName, input)
}

// SetAllowedTools sets the auto-approved tool list
func (p *Policy) SetAllowedTools(tools []string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.allowedTools = make(map[string]bool)
	for _, tool := range tools {
		p.allowedTools[tool] = true
	}
}

// GetNextPermissionMode 返回循环中的下一个模式（阶段 2 收模式后为 2 档循环）。
//
// 循环：read-only ⇄ workspace-write。
// danger-full-access 刻意**不参与**循环 —— 它是高风险档，必须由用户显式指定
// （/permissions danger-full-access 或配置），与同类 CLI 对 danger 的告警一致。
func GetNextPermissionMode(current Level) Level {
	switch current {
	case LevelReadOnly:
		return LevelWorkspaceWrite
	case LevelWorkspaceWrite:
		return LevelReadOnly
	default:
		// danger-full-access 或未知值：回到循环的默认档 workspace-write。
		return LevelWorkspaceWrite
	}
}

// SetModeByName 是「按名称切换模式」的单一生产入口（TUI /permissions <mode>）。
//
// 它把「解析模式名」与「迁移已删除模式的审批语义」收敛到一处，避免多处解析漂移：
// - 3 档名称：直接切换。
// - accept-edits：切换为 workspace-write，并注入编辑/安全命令的默认 allow 规则，
// 复刻「编辑免问 + 安全 bash 免问 + 危险命令确认」。
// - dont-ask / plan / auto / bubble：按 LookupModeName 的兼容翻译切换（不注入规则）。
//
// 返回 false 表示名称无法识别（不改变当前模式）。
func (p *Policy) SetModeByName(name string) bool {
	level, known := LookupModeName(name)
	if !known {
		return false
	}

	normalized := strings.ToLower(strings.TrimSpace(strings.ReplaceAll(name, "_", "-")))

	p.mu.Lock()
	defer p.mu.Unlock()
	p.mode = level

	// accept-edits：以默认 allow 规则复刻其审批语义。
	// 注意：规则只在 allow 分支生效，且 allow 会先查沙箱能力（P1 硬边界），
	// 因此在 read-only 下这些规则不会放行写操作。
	if normalized == "accept-edits" || normalized == "acceptedits" {
		if p.ruleSet == nil {
			p.ruleSet = NewRuleSet()
		}
		for _, r := range legacyAcceptEditsRules() {
			p.ruleSet.Add(r)
		}
	}
	return true
}

// AddAllowedTool adds an auto-approved tool
func (p *Policy) AddAllowedTool(tool string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.allowedTools[tool] = true
}

// RemoveAllowedTool removes an auto-approved tool
func (p *Policy) RemoveAllowedTool(tool string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	delete(p.allowedTools, tool)
}

// GetAllowedTools gets the auto-approved tool list
func (p *Policy) GetAllowedTools() []string {
	p.mu.RLock()
	defer p.mu.RUnlock()
	tools := make([]string, 0, len(p.allowedTools))
	for t := range p.allowedTools {
		tools = append(tools, t)
	}
	return tools
}

// IsAutoApproved checks if the tool is in the auto-approved list
func (p *Policy) IsAutoApproved(tool string) bool {
	p.mu.RLock()
	defer p.mu.RUnlock()
	return p.allowedTools[tool]
}

// SetMemoryStorePath sets the permission memory persistence path
func (p *Policy) SetMemoryStorePath(path string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.memoryStorePath = path
}

// SaveMemory saves permission memory to file.
//
// S9 delta E（严格会话级）：**休眠 + 不得接线**。
// Remember 已不再调用它（撤销 Round2 的 G1）——「允许本会话」不落盘。
// 任何把它接回启动/审批路径的改动，都会让授权重新跨重启存活，
// 并使"留痕"退化为"持久授权"。若要接线，必须先引入显式的"永久允许"档位并重新拍板。
func (p *Policy) SaveMemory() error {
	p.mu.RLock()
	storePath := p.memoryStorePath
	mem := p.memory
	p.mu.RUnlock()

	if storePath == "" {
		return nil
	}
	return mem.SaveToFile(storePath)
}

// LoadMemory loads permission memory from file.
//
// S9 delta E（严格会话级）：**休眠 API** —— 生产启动路径已不再调用它。
// 读路径的摘除是刻意的：不能只靠"没人写"来保证不持久化 ——
// 一个残留或被放置的 permissions.json 仍会被加载成有效授权（本地写面 = 提权向量）。
//
// S9 / I6：文件不存在（尚未产生任何持久授权）视为「空记忆」，不是错误——
// 否则每次冷启动都会打印 "[init] LoadMemory warning: ... no such file or directory"。
func (p *Policy) LoadMemory() error {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.memoryStorePath == "" {
		return nil
	}
	loaded, err := LoadMemoryFromFile(p.memoryStorePath, 256)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		return err
	}
	p.memory = loaded
	return nil
}

// IsDangerousToolFor 判断工具调用是否属于危险操作（B4-P1-3 修复）。
//
// 从**工具目录**派生：required >= LevelWorkspaceWrite 即 dangerous —— 危险分级与
// 权限档位共用同一真相源（注册表的 Spec.RequiredPermission），不再有第二张表。
// 未登记的工具由 RequiredLevelOf fail-closed 到最高档，故一律视为 dangerous。
//
// 阶段 3：入口为**输入感知** —— 委派型工具 Agent 按入参 agent_type 判定，
// 只读子代理（plan / explore）不算危险工具（与权限层定级共用 requiredForTool）。
// 审批层 approval.Manager.isDangerous 调用本函数。
//
// R5：本函数在生产路径上第一次被执行于 approval.Manager.Check（manager.go:69，
// PolicyOnRequest 策略分支，由 PermissionStage 在 needConfirm 时调用）。
// 旧的按名版本 IsDangerousTool(toolName) 已无任何调用点，按 R5 删除。
func IsDangerousToolFor(toolName, input string) bool {
	return requiredForTool(toolName, input) >= LevelWorkspaceWrite
}

// BashRisk represents Bash command risk level
type BashRisk string

const (
	BashRiskReadOnly    BashRisk = "read-only"
	BashRiskDestructive BashRisk = "destructive"
	BashRiskDangerous   BashRisk = "dangerous"
)

// AnalyzeBashCommand analyzes Bash command risk.
//
// ★ 危险档（Dangerous）的**模式表已收敛到 cmdrules**（唯一真相源）——
// 本函数只保留「命中之后怎么分级」这一策略，不再自带模式表。
// 这样 memory 的降级门（basicDangerCheck）与安全链的 Layer 3 共用同一份数据，
// 不会再出现「同一命令两处结论不同」。
//
// 分级顺序（与收敛前逐条保持）：
// 1. 空命令 → 只读
// 2. 危险档（cmdrules.MatchDangerous）→ Dangerous（Deny）
// 3. 破坏性命令名（词边界）→ Destructive（Ask）
// 4. 破坏性短语 → Destructive
// 5. 只读短语 → ReadOnly
// 6. 兜底 → Destructive（保守：未知即需确认）
func AnalyzeBashCommand(command string) BashRisk {
	trimmed := strings.TrimSpace(command)
	if trimmed == "" {
		return BashRiskReadOnly
	}

	// 危险档：委托 cmdrules（唯一真相源）。命中即 Deny。
	if _, ok := cmdrules.MatchDangerous(trimmed); ok {
		return BashRiskDangerous
	}

	normalized := cmdrules.Normalize(trimmed)
	lower := strings.ToLower(normalized)

	// 破坏性命令名：按**命令位**判定（同源补漏，2026-10-02）。
	//
	// 修复前是本包私有的 `wordBoundaryMatch`（`\b` 整串词边界）遍历
	// `cmdrules.DestructiveCommandNames()` —— 判的是「命令文本里有没有这个词」。
	// 于是 `grep -rn rm .` / `grep -rn chmod Makefile` / `ls -la | grep dd` 被判
	// 「破坏性 ⇒ 需确认」，而它们的命令位是 grep（只读档表成员）；且本分级顺序里
	// 破坏性档（第 3 步）**先于**只读档（第 5 步）⇒ 只读命令被强行拉进确认闸。
	//
	// 与 sandbox 的 `matchDenyPattern`（deny 判据）是同一判据的两处实现，
	// 也与 S17/ 在幻觉检测侧删掉的 `bashFileOpRe=(?i)\b(rm|mv|cp)\b` 同源。
	// 判据收口到 `cmdrules.MatchRiskDestructive`（与 MatchDestructive / MatchReadOnly
	// 共用 commandHeads 命令位），理由见 cmdrules 第 44-47 行：词边界 `\b` 不够。
	if cmdrules.MatchRiskDestructive(trimmed) {
		return BashRiskDestructive
	}

	for _, c := range cmdrules.DestructivePhrases() {
		if strings.Contains(lower, c) {
			return BashRiskDestructive
		}
	}

	// ★ P-015：只读判定改走 `cmdrules.MatchReadOnly` —— **命令位/段首**匹配。
	//
	// 修复前是本包自写的**裸子串**匹配（`strings.Contains(lower, c)` 遍历一张含裸 `"ls"` 的表）：
	// 任何含 `ls` 两个字母的命令都被判只读。实测命中（修前）：
	// `go test ./internal/tools/`（路径含 tools）、`make -C tools`、`docker build -t tools .`、
	// `false`、`lsblk` —— 而只读档 = 免确认 ＋ 沙箱在强制档下命中免问出口 (c) ⇒ 误报即**放宽**。
	//
	// `sleep` / `seq` 的只读分类理由见 `cmdrules.readOnlyCommands` 注释
	// （不加会导致 `monitor.Start("sleep 60")` 被默认判为 destructive → 后台场景 fail-closed 拒绝）。
	if cmdrules.MatchReadOnly(trimmed) {
		return BashRiskReadOnly
	}

	return BashRiskDestructive
}

// normalizeCommandWhitespace normalizes whitespace in command strings.
//
// ★ 实现已收敛到 cmdrules.Normalize（唯一真相源）；此处保留薄转发仅为
// 本包内既有调用点的可读性，不再自带第二份空白归一化实现。
func normalizeCommandWhitespace(cmd string) string {
	return cmdrules.Normalize(cmd)
}

// originalValue stores the original toolName and input for the fingerprint key
type originalValue struct {
	ToolName string
	Input    string
}

// Memory represents permission memory (LRU + fingerprint deduplication)
type Memory struct {
	mu             sync.Mutex
	entries        map[string]bool
	keys           []string
	keyIndex       map[string]int
	maxSize        int
	originalValues map[string]originalValue
}

// NewMemory creates permission memory
func NewMemory(maxSize int) *Memory {
	return &Memory{
		entries:        make(map[string]bool),
		keys:           make([]string, 0),
		keyIndex:       make(map[string]int),
		maxSize:        maxSize,
		originalValues: make(map[string]originalValue),
	}
}

// Has checks if there is a memory, moves key to end on hit for LRU eviction
// Uses constant-time comparison for the fingerprint key to mitigate timing attacks
func (m *Memory) Has(toolName, input string) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	key := fingerprint(toolName, input)
	// Use constant-time comparison to find the matching key
	for _, existingKey := range m.keys {
		if subtle.ConstantTimeCompare([]byte(key), []byte(existingKey)) == 1 {
			allowed := m.entries[existingKey]
			if allowed {
				if idx, ok := m.keyIndex[existingKey]; ok {
					m.keys = append(m.keys[:idx], m.keys[idx+1:]...)
					for i := idx; i < len(m.keys); i++ {
						m.keyIndex[m.keys[i]] = i
					}
					m.keys = append(m.keys, existingKey)
					m.keyIndex[existingKey] = len(m.keys) - 1
				}
				return true
			}
			return false
		}
	}
	return false
}

// Set sets memory, also stores original toolName and input
func (m *Memory) Set(toolName, input string, allowed bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	key := fingerprint(toolName, input)
	if _, exists := m.entries[key]; !exists {
		if len(m.keys) >= m.maxSize {
			evictKey := m.keys[0]
			m.keys = m.keys[1:]
			delete(m.entries, evictKey)
			delete(m.originalValues, evictKey)
			delete(m.keyIndex, evictKey)
			for i := 0; i < len(m.keys); i++ {
				m.keyIndex[m.keys[i]] = i
			}
		}
		m.keys = append(m.keys, key)
		m.keyIndex[key] = len(m.keys) - 1
	}
	m.entries[key] = allowed
	m.originalValues[key] = originalValue{ToolName: toolName, Input: input}
}

// memoryEntry is a persistence entry
type memoryEntry struct {
	ToolName string `json:"tool_name"`
	Input    string `json:"input"`
	Allowed  bool   `json:"allowed"`
}

// SaveToFile saves permission memory to file, using original toolName and input instead of fingerprint
func (m *Memory) SaveToFile(path string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0755); err != nil {
		return fmt.Errorf("create directory: %w", err)
	}

	var entries []memoryEntry
	for _, key := range m.keys {
		if allowed, ok := m.entries[key]; ok && allowed {
			orig := m.originalValues[key]
			entries = append(entries, memoryEntry{
				ToolName: orig.ToolName,
				Input:    orig.Input,
				Allowed:  allowed,
			})
		}
	}

	data, err := json.MarshalIndent(entries, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal: %w", err)
	}

	return util.AtomicWriteFile(path, data, 0o600)
}

// LoadMemoryFromFile loads permission memory from file, also restores original value mapping
func LoadMemoryFromFile(path string, maxSize int) (*Memory, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read file: %w", err)
	}

	var entries []memoryEntry
	if err := json.Unmarshal(data, &entries); err != nil {
		return nil, fmt.Errorf("unmarshal: %w", err)
	}

	mem := NewMemory(maxSize)
	for _, entry := range entries {
		if entry.Allowed {
			key := fingerprint(entry.ToolName, entry.Input)
			mem.entries[key] = true
			mem.keys = append(mem.keys, key)
			mem.keyIndex[key] = len(mem.keys) - 1
			mem.originalValues[key] = originalValue{ToolName: entry.ToolName, Input: entry.Input}
		}
	}

	return mem, nil
}

// fingerprint generates fingerprint from tool name + input
// approvalKeyFor 返回「记住本次授权」所用的规范化身份（明文，未哈希）。
//
// 默认为 (toolName, 全量 input)；但对委派型工具 Agent，其入参含**每次不同**的 prompt，
// 整段 input 作键会让「允许本次 / 始终允许」永不命中（每次 prompt 变 → 键变）。
// Agent 的可授权主体是它委派出去的**子代理类型**，故按 (toolName, agent_type) 记
// —— 与定级同源（对齐业内既有实现：授权规则按 Tool(pattern) 匹配，而非整段输入；
// 见。
//
// 这是一次**窄归一化**：只影响 Agent，其余工具仍按全量输入记忆（逐条授权语义不变）。
func approvalKeyFor(toolName, input string) string {
	if toolName == "Agent" {
		return toolName + "\x00agent_type=" + canonicalAgentType(input)
	}
	if isShellTool(toolName) {
		return toolName + "\x00" + commandScopeKey(extractCommand(input))
	}
	return toolName + "\x00" + input
}

// isShellTool 判断是否为「命令类」工具（其 input 的命令语义需按作用域归一化）。
//
// ⚠️ P-002（2026-09-22 反缝补扫描）——本函数**刻意不用** IsCommandBearingTool /
// IsSandboxBoundedTool，也**不是漏改**：这里问的是**审批记忆作用域**（见 commandScopeKey），
// 与"是不是命令载体/要不要拦"是两件事。把它扩到 `monitor` 会把记忆键从「整段输入」
// 归一化为「命令前缀」⇒ 批准一次 `tail -f a.log` 即长期免问同前缀命令 —— 那是**放宽**，
// 与本轮单向收窄的方向相反（这条决策有判据钉住：
// p002_command_bearing_test.go 的 TestGuard_P002_ApprovalScopeDeliberatelyUnchangedForMonitor）。
//
// `PowerShell`（幽灵名）已移除：它从未注册，留着只会让下一处手抄有样学样。
func isShellTool(toolName string) bool {
	return toolName == "bash"
}

// commandScopeKey 把一条 shell 命令归一化为「审批记忆作用域」（S9 / HG-A2）。
//
// 对齐业内既有实现 execpolicy 的 prefix_rule 模型：授权绑定到「命令作用域/前缀」而非整段文本
// （rule.rs:46 PrefixPattern::matches_prefix、policy.rs:67 get_allowed_prefixes；
//
//	可执行名归一化对齐 executable_name.rs:6 executable_lookup_key —— 去路径、去 .exe、小写）。
//
// 归一化按**危险语义等价分组**，复用本包既有的 AnalyzeBashCommand 分级作单一真相源，
// 并且**只对可证明同危险度的调用合并**，避免「不同危险度命令被归为一类」：
//
// - 只读类（BashRiskReadOnly）**且**是「简单单命令」（无组合/重定向/命令替换、无参数级
// 破坏性开关）**且**可执行名在 scopeCollapsibleExecutables 白名单内 → 键 = 可执行名
// （丢弃参数）。同语义只读调用（`ls -la` vs `ls -la /tmp`）互相去重 —— 正是
// 「整段 input 作键」造成去重失效的那个缺陷（G5）。
// - 其余一切情况（destructive / dangerous / 组合命令 / 无法解析）→ 键 = 规范化后的
// **完整命令**：绝不跨目标、跨参数合并（fail-closed）。
//
// 安全论证（为何不扩权）：只读类命令在任何参数下都不改文件系统、无副作用；一旦出现命令
// 组合、重定向、破坏性开关，或分级非只读，即退回整段键。故「授权一条只读命令 → 自动放行
// 另一条只读命令」不会外溢到任何写/危险命令，也不会把 rm/chmod/curl|sh 等并入。
// 「可安全按可执行名合并授权」的白名单**唯一声明**已收口到
// `cmdrules.alwaysReadOnlyExecutables`（★ 2026-10-06 ： 的只读**免问**出口
// 需要同一份"任意参数下都只读"的知识，故移到命令规则的叶子包，本包改为消费
// `cmdrules.IsAlwaysReadOnlyExecutable`）。成员判据与显式排除项见那边的注释。
//
// ⚠️ 合并需**同时**满足该白名单 + `AnalyzeBashCommand==只读类`，故有效集合是二者交集 ——
// 这也是 `isSimpleReadOnlyCommand` 仍保留 `AnalyzeBashCommand` 检查的原因
// （白名单是"命令名维度"的保证，分级是"整条命令维度"的兜底，二者不同源、都要）。
func commandScopeKey(command string) string {
	norm := normalizeCommandWhitespace(strings.TrimSpace(command))
	if norm == "" {
		return "cmd:"
	}
	exe := executableKey(norm)
	if exe != "" && cmdrules.IsAlwaysReadOnlyExecutable(exe) && isSimpleReadOnlyCommand(norm) {
		return "ro:" + exe
	}
	return "cmd:" + norm
}

// isSimpleReadOnlyCommand 判断命令是否属于「简单只读单命令」——可安全按可执行名合并。
func isSimpleReadOnlyCommand(norm string) bool {
	// 命令组合 / 重定向 / 命令替换 / 变量展开：危险可能藏在组合里，一律不合并。
	for _, meta := range []string{";", "&&", "||", "|", ">", "<", "`", "$(", "${"} {
		if strings.Contains(norm, meta) {
			return false
		}
	}
	// 参数级破坏性开关：命令名只读，但参数会写盘/执行。
	for _, danger := range []string{"-delete", "-exec", "-execdir", "-ok", "tee ", " -i ", "--in-place", "-o "} {
		if strings.Contains(norm, danger) {
			return false
		}
	}
	return AnalyzeBashCommand(norm) == BashRiskReadOnly
}

// executableKey 取命令的「可执行名」作为作用域身份：
// 首 token → 去目录 → 去引号 → 小写 → 去 Windows 扩展名（对齐业内既有实现 executable_lookup_key）。
func executableKey(norm string) string {
	fields := strings.Fields(norm)
	if len(fields) == 0 {
		return ""
	}
	exe := fields[0]
	if i := strings.LastIndexAny(exe, `/\`); i >= 0 {
		exe = exe[i+1:]
	}
	exe = strings.Trim(exe, `"'`)
	exe = strings.ToLower(exe)
	for _, suffix := range []string{".exe", ".cmd", ".bat", ".com"} {
		exe = strings.TrimSuffix(exe, suffix)
	}
	return exe
}

// ApprovalKeyFor 是 approvalKeyFor 的导出版本。
//
// 供 approval.Manager 的「记住授权」缓存复用同一套规范化规则（其键原为 sha256(整段 input)，
// 与 permission.Memory 是同一语义的第二处实现）—— 避免两处记忆键语义漂移（R3⑤ 同源点穷举）。
// 调用方负责对返回值做哈希。
func ApprovalKeyFor(toolName, input string) string {
	return approvalKeyFor(toolName, input)
}

// fingerprint generates fingerprint from tool name + input
func fingerprint(toolName, input string) string {
	h := sha256.New()
	h.Write([]byte(approvalKeyFor(toolName, input)))
	return fmt.Sprintf("%x", h.Sum(nil))[:32]
}

// BubblePolicy is the bubble permission policy
// Aligned with a comparable CLI bubble permission mode: sub-agent permission requests bubble up to parent agent
// Sub-agent does not make independent permission decisions, forwards all confirmation-needed requests to parent agent
type BubblePolicy struct {
	parent *Policy
}

// NewBubblePolicy creates a bubble permission policy
func NewBubblePolicy(parent *Policy) *BubblePolicy {
	return &BubblePolicy{parent: parent}
}

// Check checks permission (bubbles up to parent agent)
//
// 父策略的 Check 内部已包含规则判定，这里无需再单独查一次。
// 原先的预检会在不持锁的情况下读取 b.parent.ruleSet（数据竞争），
// 且该预检在「规则未命中」时同样回退到 parent.Check，去掉后行为完全等价。
func (b *BubblePolicy) Check(toolName, input string) (bool, bool) {
	return b.parent.Check(toolName, input)
}
