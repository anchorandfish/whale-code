// Package tools — tool registry, executor, validation
package tools

import (
	"encoding/json"
	"fmt"
	"log"
	"strconv"
	"strings"
	"sync"

	"github.com/anchorandfish/whale-code/internal/permission"
	"github.com/anchorandfish/whale-code/internal/session"
)

// Level 是「工具所需权限档位」的类型。
//
// L1：它现在是 permission.Level 的**类型别名**，不再是 tools 包自有的
// 第二套枚举 —— 这正是 F5「同名不同类型的两个 Level、全仓零转换」的结构性修复。
// 保留 `tools.Level*` 这些名字只为兼容既有调用方（internal/github、包内测试），
// 它们与 permission.Level* 是同一个类型、同一组常量，**不可能再漂移**。
type Level = permission.Level

const (
	LevelReadOnly         = permission.LevelReadOnly
	LevelWorkspaceWrite   = permission.LevelWorkspaceWrite
	LevelDangerFullAccess = permission.LevelDangerFullAccess
)

// Spec tool specification
type Spec struct {
	Name               string                 `json:"name"`
	Description        string                 `json:"description"` // 保留，向后兼容（由 Desc.Render() 赋值）
	Desc               ToolDescription        `json:"-"`           // N16 新增：结构化描述
	InputSchema        map[string]interface{} `json:"input_schema"`
	Schema             ToolSchema             `json:"-"` // N16 新增：强类型 schema
	RequiredPermission Level                  `json:"required_permission"`
	ConcurrencySafe    bool                   `json:"concurrency_safe"`
	Limits             ToolLimits             `json:"-"` // N16 新增：执行限制
	// Exposure 控制工具 schema 的加载策略（对标同类 CLI defer_loading）。
	// ExposureDirect（零值）：模型直接可见，每次请求全量注入 tools 数组。
	// ExposureDeferred：延迟加载，不进入 tools 数组，需通过 ToolSearch 或自动加载兜底后注入。
	// ExposureHidden：隐藏，仅供内部分发，不进入 tools 数组。
	Exposure ToolExposure `json:"-"` // REF-结构体字段-
}

// Call tool call request
type Call struct {
	ID    string `json:"id"`
	Name  string `json:"name"`
	Input string `json:"input"`
	// Approved marks the current call after the user has explicitly approved
	// its safety prompt. It is internal execution state and is never serialized.
	Approved bool `json:"-"`

	// ReusedApproval 表示「本次调用与**同一次用户请求内已被用户明确批准过**的调用
	// 逐字相同（同 Name + 同 Input）」，因此复用那次批准、不再重复询问。
	// 由调用方（TUI）在跨批时置位；数值是内部执行状态，永不序列化。
	//
	// ★ 与 Approved 的区别**必须保持**（混用会突破危险面）：
	//
	//	Approved = 「用户对**这一条**调用说的 Yes」。
	// 被 DangerousGuardStage（pipeline.go:991）与
	// DestructiveCheckStage（pipeline.go:1080）读取 ⇒ 带它
	// 会**跳过危险档 / 破坏性确认**。这是批准的正常语义。
	//
	//	ReusedApproval = 「本次请求内同参数已获批过」这一**推断**，
	// **只**被 PermissionStage 的免问块读取（pipeline.go:1358 起）。
	//
	// 为什么推断不能也折成 Approved：危险档与破坏性检查都排在 PermissionStage
	// **之前**且带 NeedConfirm ⇒ ToolPipeline.Execute 在 pipeline.go:323 短路，
	// 它们**从来看不见** ReusedApproval。于是「危险面永不因复用而免问」是
	// **结构事实（靠 stage 顺序到不了）**，不是「靠每个新出口都记得加一个 if」——
	// 后者正是本仓反复复发的「多入口各写各的」病（同 DangerousGuardStage 的
	// 设计理由，见 pipeline.go:960-963）。
	// ⇒ **不得**把本字段改名为 Approved / 在危险阶段读它。
	ReusedApproval bool `json:"-"`

	// ApprovedByRule（· 2026-09-20）报告本 Call 的 `Approved` 是**由规则/
	// 记忆推导出来的免问**，而不是用户在确认框上对**这一条**调用按下的 Yes。
	//
	// 为什么必须与 Approved 分开：`Approved` 被 DangerousGuardStage 与
	// DestructiveCheckStage 读取并**跳过危险确认**（pipeline.go:1006 / :1095）。
	// 而规则前置门（pipeline.go:265-278）为让 `allow` 规则能真正免问，会把
	// `approved = true` —— 于是**一条可随仓库分发的 `allow` 规则**（写在
	// `<workDir>/.whale/settings.json` 里）就能让危险档 / 破坏性确认被跳过。
	// 这是 的第二半：与「规则只能收紧」是同一条不变式的两个方向。
	//
	// ⇒ 两个危险阶段的跳过条件因此收紧为 `call.Approved && !call.ApprovedByRule`：
	// 用户显式批准 ⇒ 仍跳过（否则 `ExecuteWithConfirm` 的"批准后重跑"会死循环）；
	// 规则推导 ⇒ **不跳过**（危险档 = 强制确认永不免问，-R1）。
	//
	// 数值是内部执行状态，永不序列化。
	ApprovedByRule bool `json:"-"`

	// SandboxEscalated（，2026-10-14）：本调用是「on-failure 沙箱升级」的批准重跑 ——
	// 命令在沙箱内以 EPERM 失败、用户对升级询问按了 Yes ⇒ 这一条在**沙箱外**重跑。
	//
	// 为什么不复用 Approved：`Approved` 的 sanctioned 传递（sandbox_sanction.go）
	// 只对 **TierConfirm** 命令生效（`sanctionedContext` 的既有判据）；而升级的对象
	// 恰恰是 **TierPass** 命令（规则引擎眼里无害、OS 沙箱才拒绝的）⇒ 复用会漏传。
	// 消费点唯一：`sanctionedContext` 的第二分支 ⇒ 与 同一条 sanctioned 通道
	// （HardDeny 恒拒、validator Critical 恒拦的纵深防御全部继承）。
	//
	// 唯一合法置位点 = `ExecuteWithConfirm` 在用户对升级询问按 Yes 后（与 Approved
	// 同生命周期：逐条、context 携带、不写盘、不记忆）。数值永不序列化。
	SandboxEscalated bool `json:"-"`
}

// Result tool execution result
type Result struct {
	ID          string      `json:"id"`
	Name        string      `json:"name"`
	Output      string      `json:"output"`
	IsError     bool        `json:"is_error"`
	FailureKind FailureKind `json:"failure_kind,omitempty"`

	// SandboxEPERM（，2026-10-14）：命令**已经跑过**且失败像 OS 沙箱造成
	// （沙箱内执行 + 非零退出 + 输出含 EPERM 文本）。与 FailureKindSandboxDenied
	// 组合 = 「沙箱内核拒绝（EPERM），命令已运行」；执行**前**的沙箱拒绝
	// （errSandboxDenied，命令一行没跑）不带本标记 —— 那不是升级对象（-R1）。
	// 唯一消费者 = ExecuteWithConfirm 的 on-failure 升级询问；内部分段状态，永不序列化。
	SandboxEPERM bool `json:"-"`
}

// FailureKind classifies terminal tool failures for orchestration decisions.
// Keeping this separate from the human-readable output avoids making callers
// parse localized text to decide whether a retry is safe.
type FailureKind string

const (
	FailureKindSandboxDenied FailureKind = "sandbox_denied"
	FailureKindPolicyDenied  FailureKind = "policy_denied"

	// FailureKindPatchConflict（G5，2026-09-14）：
	// 补丁无法应用到当前文件内容（hunk 上下文匹配不上 / 偏移超出 fuzz 上界 / 文件已被改动）。
	//
	// ★ 与上面两个 Kind 的**关键差异：它不是终局拒绝**。
	// sandbox_denied / policy_denied 是"这条路本来就不允许"，重试同一个调用毫无意义，
	// 所以 TUI 会直接终结本轮（tool_exec.go 的 terminalFailureKind 回路），
	// 防止 write_file→bash→edit_file 那种同规则下的噪声循环。
	// 而补丁冲突是**可恢复**的：模型读一遍当前文件、重新生成一份匹配的补丁即可 ——
	// 这属于 IK2 明说的"编译错误 / 命令非零退出"那一类普通失败，
	// 若也终结本轮，等于因为一次可以自愈的失配而掐掉整个回合。
	//
	// 因此本 Kind 的语义是「**带结构化标记的普通失败**」：IsError=true、FailureKind 非空，
	// 但 Terminal()=false。终局性由 FailureKind.Terminal() 单一真相源决定（见下），
	// 不再由"值是否非空"推断 —— 那正是本次要把两个正交维度解开的耦合。
	FailureKindPatchConflict FailureKind = "patch_conflict"

	// FailureKindCommandExit（(c)，2026-10-09）：
	// 被执行的命令**跑完了**，且它的答案是"失败"（非零退出码）——
	// 最典型的形态是 `go test` 断言失败返回 exit 1，或 `grep` 没找到（exit 1）。
	//
	// ★ 它和 patch_conflict 同档：**带结构化标记的普通失败**（IsError=true、
	// FailureKind 非空、Terminal()=false）。命令非零退出是**可恢复**的 ——
	// 模型读到失败原因、改一版再跑即可；设 Terminal=true 会因为一次可自愈的命令
	// 失败而**掐掉整个用户回合**（IK2 的教训，与 patch_conflict 同一条推理）。
	//
	// ★ 为什么需要它（根因）：修前 bash 的非零退出是一条裸 `fmt.Errorf`，
	// 于是 `executor.go` 的兜底包装把它与"真故障"合并成同一句
	// `tool execution failed: …` ⇒ **数据被当成故障**：用户/模型把"测试失败"
	// 误读成"whale 的工具坏了"（R4：本可用结构化种类表达的事实被压成了散文短语）。
	//
	// ★ 措辞分层（唯一声明见 `ToolErrorOutput`）：本 Kind 走**数据口吻**
	// （`<工具名>: 命令以退出码 N 结束 …`），`tool execution failed:` 从此
	// 只留给**未知错误 / 真故障**。
	FailureKindCommandExit FailureKind = "command_exit"
)

// failureKindSpec 描述一个 FailureKind 在**各自独立语义轴**上的归属。
//
// ★ 为什么是「结构体 + 显式布尔」而不是两个 map[FailureKind]bool：
//
//	map 查询的零值无法区分「显式声明为 false」与「**忘记登记**」——
//	而"忘记登记"正是本表要拦住的失败模式（遗漏者会静默继承默认语义）。
type failureKindSpec struct {
	Terminal       bool // 终局拒绝：重试同一调用无意义 ⇒ 允许终结用户回合（IK1）
	SecurityDenial bool // 安全拒绝：计入 R4 安全账本（TUI 的 Model.roundDeniedKind）
}

// failureKindSpecs 是 FailureKind **全部语义轴的唯一真相源**。
//
// 只有在这里登记过的 Kind 才允许让编排/TUI 终结本轮；未登记的（含零值）一律按
// "可恢复的普通失败"处理。新增 Kind 必须在此**显式登记每一个轴**，两条守卫：
//
// - failure_kind_registry_test.go —— 跑期遍历 AllFailureKinds() 查存在性（双向）
// - failure_kind_ast_gate_test.go —— 结构面扫本文件常量声明，抓"两侧都忘登记"
//
// ★ **两条轴必须分开**：今天 `Terminal` 与 `SecurityDenial` 两个集合恰好同集
// （`{sandbox_denied, policy_denied}`），但语义无关 —— 将来完全可能出现
// 「终局但非安全」（如预算耗尽）或「安全但非终局」的种类。
// 把安全账本的归属写成 TUI 侧那个枚举式 `switch { case SandboxDenied, PolicyDenied: }`
// （tool_exec.go 的 noteRoundSecurityDenials），会让新增种类**静默漏记**，
// 而 `tui/stream.go:1177` 的权威判据 `roundDeniedKind != ""` 对该种类恒 false
// —— 结构化拒绝事实在"账本归属判定"这一步被默认分支丢掉（= 对未枚举种类等价于不可得）。
var failureKindSpecs = map[FailureKind]failureKindSpec{
	FailureKindSandboxDenied: {Terminal: true, SecurityDenial: true},
	FailureKindPolicyDenied:  {Terminal: true, SecurityDenial: true},
	FailureKindPatchConflict: {Terminal: false, SecurityDenial: false},
	FailureKindCommandExit:   {Terminal: false, SecurityDenial: false},
}

// Terminal 报告该 Kind 是否为**终局拒绝**（IK1 的单一判据）。
//
// 为什么做成方法而不是让调用方写 `kind != ""`：非空 ≠ 终局。
// 把「是哪种失败」与「是否还能重试」当成同一个维度，会在新增 Kind 时静默踩雷
// （新 Kind 一出现就自动获得"终结用户回合"的权力）。这里把终局性显式登记出来，
// 并配一条守卫测试保证每个 Kind 都有明确归属、不会有人漏登记。
//
// 语义与替换前**逐字相同**（读 failureKindSpecs[k].Terminal，集合不变）。
func (k FailureKind) Terminal() bool { return failureKindSpecs[k].Terminal }

// IsSecurityDenial 报告该 Kind 是否属于**安全拒绝** —— R4 安全账本的单一判据。
//
// 消费方（第 2 轮 · internal/tui）：`noteRoundSecurityDenials` 的
// `switch r.FailureKind { case SandboxDenied, PolicyDenied: }` 改为调本谓词，
// 使"该入账的种类"由**声明点**决定，而不是由消费方各自枚举。
func (k FailureKind) IsSecurityDenial() bool { return failureKindSpecs[k].SecurityDenial }

// AllFailureKinds 返回全部已声明的 FailureKind（供守卫测试遍历，防止漏登记）。
//
// ★ 与 failureKindSpecs 交叉校验：本函数是「声明清单」、specs 是「语义表」，
// 二者由 failure_kind_registry_test.go 双向比对、由 failure_kind_ast_gate_test.go
// 对照结构面常量声明，任一侧漏项即打红。
func AllFailureKinds() []FailureKind {
	return []FailureKind{
		FailureKindSandboxDenied,
		FailureKindPolicyDenied,
		FailureKindPatchConflict,
		FailureKindCommandExit,
	}
}

// ToolErrorOutput 是「工具失败在用户/模型可见输出里的措辞」的**唯一声明**（(c)）。
//
// 分层规则（判据 = 该 Kind 是否**在 failureKindSpecs 里显式登记**为「带结构化标记的
// 普通失败」＝已登记且 !Terminal）：
//
//	┌────────────────────────┬──────────────────────────────────────────────┐
//	│ 种类 │ 措辞（本条是唯一产出点） │
//	├────────────────────────┼──────────────────────────────────────────────┤
//	│ command_exit │ `<工具名>: 命令以退出码 N 结束 …`（**数据口吻**）│
//	│ patch_conflict │ `<工具名>: patch conflict: …`（**数据口吻**） │
//	│ 未知错误 / 真故障 │ `tool execution failed: …`（故障口吻） │
//	│ **未登记的种类** │ `tool execution failed: …`（故障口吻，fail-closed）│
//	└────────────────────────┴──────────────────────────────────────────────┘
//
// 为什么写成函数而不是在 executor 里 `if kind == ...`：措辞与"哪一类失败"必须**同源**，
// 否则新增一个"普通失败"种类时，措辞会静默落回"工具坏了"（R4：类别走结构化字段）。
//
// ★ 为什么判据是 **comma-ok 的"已登记"**，而不是 `kind != "" && !kind.Terminal()`：
//
//	`Terminal()` 对**未登记**的 Kind 返回零值 `false` ⇒ 前者会把「忘记登记的新种类」
//	静默当成数据口吻 —— 这正是 `failureKindSpecs` 头部注释警告过的失败模式
//	（"map 查询的零值无法区分「显式声明为 false」与「忘记登记」"）。
//	措辞层是**最后一道**：一个忘了登记的**真故障**若在此被说成"这是命令的结果"，
//	模型会继续用坏掉的状态往下跑。故此处 fail-closed —— **只有显式登记为
//	非终局普通失败的种类**才走数据口吻，其余（含未登记 / 零值）一律走故障口吻。
//
// 为什么"数据口吻"用 `<工具名>: ` 前缀：显示层 `errOutputSelfLabels`（tools.go）认这个形态
// 并据此不再重复工具名 ⇒ 屏幕上是「❌ bash: 命令以退出码 1 结束」，而不是双重前缀。
func ToolErrorOutput(toolName string, kind FailureKind, err error) string {
	if spec, registered := failureKindSpecs[kind]; registered && !spec.Terminal {
		// 显式登记过的**普通失败**：命令/补丁把"失败"作为**结果**报出来，工具本身没坏。
		return fmt.Sprintf("%s: %v", toolName, err)
	}
	return fmt.Sprintf("tool execution failed: %v", err)
}

// Registry tool registry
type Registry struct {
	mu      sync.RWMutex
	builtin map[string]Spec
	mcp     map[string]Spec
	plugin  map[string]Spec
}

// NewRegistry creates a tool registry
func NewRegistry() *Registry {
	r := &Registry{
		builtin: make(map[string]Spec),
		mcp:     make(map[string]Spec),
		plugin:  make(map[string]Spec),
	}
	r.registerBuiltinTools()
	// L1：纳入工具档位目录的可见范围，使动态注册的插件/MCP 工具也能被权限层解析。
	registerLiveRegistry(r)
	return r
}

// Get returns a tool specification
func (r *Registry) Get(name string) (Spec, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	if spec, ok := r.builtin[name]; ok {
		return spec, true
	}
	if spec, ok := r.plugin[name]; ok {
		return spec, true
	}
	if spec, ok := r.mcp[name]; ok {
		return spec, true
	}
	return Spec{}, false
}

// ListMCPServers lists all registered MCP server names
func (r *Registry) ListMCPServers() []string {
	r.mu.RLock()
	defer r.mu.RUnlock()
	servers := make([]string, 0, len(r.mcp))
	for name := range r.mcp {
		servers = append(servers, name)
	}
	return servers
}

// All returns all tool specifications
func (r *Registry) All() []Spec {
	r.mu.RLock()
	defer r.mu.RUnlock()
	var all []Spec
	for _, s := range r.builtin {
		all = append(all, s)
	}
	for _, s := range r.plugin {
		all = append(all, s)
	}
	for _, s := range r.mcp {
		all = append(all, s)
	}
	return all
}

// AllDirect 返回 Exposure == ExposureDirect 的工具规格（不含延迟/隐藏工具）。
// buildToolDefs 使用此方法构造 tools 数组，避免全量加载 53 个工具 schema。
// 延迟工具需通过 ToolSearch 加载或自动加载兜底后，由 LoadedDeferredToolNames 合并。
// REF-分层权责-：Registry 负责 metadata 过滤，Executor 负责会话级加载状态。
func (r *Registry) AllDirect() []Spec {
	r.mu.RLock()
	defer r.mu.RUnlock()
	var all []Spec
	for _, s := range r.builtin {
		if s.Exposure == ExposureDirect {
			all = append(all, s)
		}
	}
	for _, s := range r.plugin {
		if s.Exposure == ExposureDirect {
			all = append(all, s)
		}
	}
	for _, s := range r.mcp {
		if s.Exposure == ExposureDirect {
			all = append(all, s)
		}
	}
	return all
}

// DeferredToolDirectory 生成延迟工具的紧凑目录文本，注入 system prompt 供模型发现。
// 格式：每行 "工具名 — 一句话描述"，按类别分组。约 400 token（30 个延迟工具）。
// 模型看到目录后可通过 ToolSearch({"load":"工具名"}) 加载完整 schema。
func (r *Registry) DeferredToolDirectory() string {
	r.mu.RLock()
	defer r.mu.RUnlock()
	var lines []string
	for _, s := range r.builtin {
		if s.Exposure == ExposureDeferred {
			desc := s.Description
			if len(desc) > 80 {
				desc = desc[:80] + "..."
			}
			lines = append(lines, fmt.Sprintf("- %s — %s", s.Name, desc))
		}
	}
	if len(lines) == 0 {
		return ""
	}
	return "## Deferred Tools (use ToolSearch to load schema)\n" +
		"The following tools are available but not loaded. " +
		"Call ToolSearch({\"load\": \"<tool_name>\"}) to load a tool's schema before using it.\n" +
		strings.Join(lines, "\n")
}

// List returns names of all registered tools
func (r *Registry) List() []string {
	r.mu.RLock()
	defer r.mu.RUnlock()
	var names []string
	for name := range r.builtin {
		names = append(names, name)
	}
	for name := range r.plugin {
		names = append(names, name)
	}
	for name := range r.mcp {
		names = append(names, name)
	}
	return names
}

// Definitions returns API tool definitions list
func (r *Registry) Definitions() []map[string]interface{} {
	specs := r.All()
	defs := make([]map[string]interface{}, 0, len(specs))
	for _, spec := range specs {
		defs = append(defs, map[string]interface{}{
			"name":         spec.Name,
			"description":  spec.Description,
			"input_schema": spec.InputSchema,
		})
	}
	return defs
}

// RegisterMCP register MCP tools
func (r *Registry) RegisterMCP(spec Spec) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, exists := r.builtin[spec.Name]; exists {
		return fmt.Errorf("MCP tool %q conflicts with builtin tool", spec.Name)
	}
	if _, exists := r.plugin[spec.Name]; exists {
		return fmt.Errorf("MCP tool %q conflicts with plugin tool", spec.Name)
	}
	r.mcp[spec.Name] = spec
	return nil
}

// RegisterPlugin register plugin tools
func (r *Registry) RegisterPlugin(spec Spec) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, exists := r.builtin[spec.Name]; exists {
		return fmt.Errorf("plugin tool %q conflicts with builtin tool", spec.Name)
	}
	if _, exists := r.mcp[spec.Name]; exists {
		return fmt.Errorf("plugin tool %q conflicts with MCP tool", spec.Name)
	}
	r.plugin[spec.Name] = spec
	return nil
}

// UnregisterMCP removes an MCP tool
func (r *Registry) UnregisterMCP(name string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	delete(r.mcp, name)
}

// UnregisterPlugin removes a plugin tool
func (r *Registry) UnregisterPlugin(name string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	delete(r.plugin, name)
}

// IsConcurrencySafe checks if a tool is concurrency-safe
func (r *Registry) IsConcurrencySafe(name string) bool {
	if spec, ok := r.Get(name); ok {
		return spec.ConcurrencySafe
	}
	return false
}

// toolRegistrationFailuresMu 保护全局工具注册失败累计器的并发访问。
// 设计为 process-global：所有 Executor 实例共享一个累计器，便于 main 路径
// 在启动完成后一次性 DrainToolRegistrationFailures 输出到 stderr/日志。
var toolRegistrationFailuresMu sync.Mutex

// toolRegistrationFailures 是工具注册失败项的累计列表（M20-04）。
// 由 RegisterSinglePluginTools / RegisterPluginTools 在失败时追加，
// 由 DrainToolRegistrationFailures 一次性取出清空。
var toolRegistrationFailures []ToolRegistrationFailure

// ToolRegistrationFailure 记录单个工具注册失败项（M20-04）。
// 字段含义：
// - PluginID：失败工具所属的插件 ID（用于提示用户定位插件）
// - ToolName：失败的工具名称
// - Reason：失败原因（如重名冲突、registry 内部错误等）
type ToolRegistrationFailure struct {
	PluginID string
	ToolName string
	Reason   error
}

// recordToolRegistrationFailure 追加一条工具注册失败记录到 process-global 列表（M20-04）。
// 并发安全：通过 toolRegistrationFailuresMu 保护，可被多个 Executor 实例并发调用。
func recordToolRegistrationFailure(pluginID, toolName string, reason error) {
	toolRegistrationFailuresMu.Lock()
	defer toolRegistrationFailuresMu.Unlock()
	toolRegistrationFailures = append(toolRegistrationFailures, ToolRegistrationFailure{
		PluginID: pluginID,
		ToolName: toolName,
		Reason:   reason,
	})
}

// DrainToolRegistrationFailures 取出并清空 process-global 工具注册失败列表（M20-04）。
// 调用方负责将返回的失败项格式化为用户可读的提示输出到 stderr/日志，
// 例如：`⚠️ 插件 foo 的工具 bar 注册失败：conflicts with builtin tool`。
// 多次调用安全：第二次调用返回空切片（已 drain）。
func DrainToolRegistrationFailures() []ToolRegistrationFailure {
	toolRegistrationFailuresMu.Lock()
	defer toolRegistrationFailuresMu.Unlock()
	failures := toolRegistrationFailures
	toolRegistrationFailures = nil
	return failures
}

// PeekToolRegistrationFailures 返回当前累计失败项的副本（不清空，M20-04）。
// 用于测试断言：测试可 Peek 后断言特定失败项存在。
func PeekToolRegistrationFailures() []ToolRegistrationFailure {
	toolRegistrationFailuresMu.Lock()
	defer toolRegistrationFailuresMu.Unlock()
	snapshot := make([]ToolRegistrationFailure, len(toolRegistrationFailures))
	copy(snapshot, toolRegistrationFailures)
	return snapshot
}

// ResetToolRegistrationFailures 清空 process-global 失败列表（M20-04，仅测试用）。
// 生产代码应使用 DrainToolRegistrationFailures 取出后处理，不直接 reset。
func ResetToolRegistrationFailures() {
	toolRegistrationFailuresMu.Lock()
	defer toolRegistrationFailuresMu.Unlock()
	toolRegistrationFailures = nil
}

// FormatToolRegistrationFailures 将失败项列表格式化为多行用户可读字符串（M20-04）。
// 每行格式：` - plugin=<pluginID> tool=<toolName>: <reason>`
// 空列表返回空字符串。调用方负责在前面加汇总行（如 `⚠️ 2 工具注册失败：`）。
func FormatToolRegistrationFailures(failures []ToolRegistrationFailure) string {
	if len(failures) == 0 {
		return ""
	}
	var sb strings.Builder
	for _, f := range failures {
		reason := ""
		if f.Reason != nil {
			reason = f.Reason.Error()
		}
		sb.WriteString(fmt.Sprintf("  - plugin=%s tool=%s: %s\n", f.PluginID, f.ToolName, reason))
	}
	return sb.String()
}

// registerBuiltinTools registers builtin tools
func (r *Registry) registerBuiltinTools() {
	tools := []Spec{
		{
			Name: "bash",
			Desc: ToolDescription{
				Purpose: "Execute Bash commands. Supports timeout and sandbox isolation. Dangerous commands require higher permissions.",
				Parameters: []ParamDesc{
					{Name: "command", Type: "string", Required: true, Desc: "Bash command to execute"},
					{Name: "timeout", Type: "integer", Required: false, Desc: "Timeout in seconds, default 120"},
					{Name: "run_in_background", Type: "boolean", Required: false, Desc: "Whether to run in the background (recommended for long commands, auto-notify on completion)"},
				},
				Behavior:    "Runs command in sandbox with process group isolation. For long-running commands, use run_in_background; for streaming monitoring (e.g., viewing logs), use the Monitor tool.",
				Constraints: "Max 120s timeout; serial execution (MaxConcurrency=1)",
			},
			Schema: *BuildSchemaFromStruct(BashInput{}),
			Limits: GetToolLimits("bash"),
			// L1（2026-09-14 用户拍板）：bash 的能力需求是「在工作区跑命令」= WorkspaceWrite。
			// 此前声明为 LevelDangerFullAccess 是把**风险**维度误当**能力**档 —— 一旦声明成为
			// 运行时唯一真相源，bash 会在默认档（workspace-write）被直接拒绝，产品不可用。
			// 「危险命令」（rm -rf / 等）的额外把关不属于档位，由既有二次确认护栏承担
			// （permission.Policy.guardrailFires）。
			RequiredPermission: LevelWorkspaceWrite,
			ConcurrencySafe:    false,
		},
		{
			Name: "read_file",
			Desc: ToolDescription{
				Purpose: "Read file contents. Supports line number ranges.",
				Parameters: []ParamDesc{
					{Name: "path", Type: "string", Required: true, Desc: "File path"},
					{Name: "offset", Type: "integer", Required: false, Desc: "Starting line number (1-based)"},
					{Name: "limit", Type: "integer", Required: false, Desc: "Number of lines to read"},
				},
				Behavior: "Reads file contents from the specified path, with optional line range support.",
			},
			Schema:             *BuildSchemaFromStruct(ReadFileInput{}),
			Limits:             GetToolLimits("read_file"),
			RequiredPermission: LevelReadOnly,
			ConcurrencySafe:    true,
		},
		{
			Name: "write_file",
			Desc: ToolDescription{
				Purpose: "Write file. Overwrites if file exists.",
				Parameters: []ParamDesc{
					{Name: "path", Type: "string", Required: true, Desc: "File path"},
					{Name: "content", Type: "string", Required: true, Desc: "File content"},
				},
				Behavior: "Writes content to the specified path, overwriting any existing file.",
			},
			Schema:             *BuildSchemaFromStruct(WriteFileInput{}),
			Limits:             GetToolLimits("write_file"),
			RequiredPermission: LevelWorkspaceWrite,
			ConcurrencySafe:    false,
		},
		{
			Name: "edit_file",
			Desc: ToolDescription{
				Purpose: "Edit file. Uses Search/Replace mode for precise replacement.",
				Parameters: []ParamDesc{
					{Name: "path", Type: "string", Required: true, Desc: "File path"},
					{Name: "old_text", Type: "string", Required: true, Desc: "Text to replace"},
					{Name: "new_text", Type: "string", Required: true, Desc: "Replacement text"},
				},
				Behavior: "Performs precise text replacement in the specified file using search/replace mode.",
			},
			Schema:             *BuildSchemaFromStruct(EditFileInput{}),
			Limits:             GetToolLimits("edit_file"),
			RequiredPermission: LevelWorkspaceWrite,
			ConcurrencySafe:    false,
		},
		{
			// G5：apply_unified_diff —— 把一段 unified diff **写回**文件。
			// 此前本仓库的 diff 能力只渲染不应用（internal/diff 无任何 Apply 函数），
			// 本工具补上后半条链路。语义对齐 `git apply`：
			// - 支持 git 的 a/ b/ 前缀与无前缀两种、/dev/null 新建与删除；
			// - 逐文件应用，上下文允许有限偏移（fuzz 默认 2，可调），超限即冲突；
			// - **全有或全无**：先把所有文件的新内容在内存里算完，任一冲突或越界
			// ⇒ 一个字节都不写（仓库无 git，事后回滚不可靠，所以让回滚不必存在）；
			// - dry_run=true 只回报将改哪些文件/hunk，不落盘。
			Name: applyUnifiedDiffToolName,
			Desc: ToolDescription{
				Purpose: "Apply a unified diff (patch) to the workspace. Creates, modifies and deletes files as the patch describes. Nothing is written if any hunk conflicts or any target escapes the workspace.",
				Parameters: []ParamDesc{
					{Name: "patch", Type: "string", Required: false, Desc: "Unified diff text (git-style a/ b/ prefixes optional)"},
					{Name: "patch_path", Type: "string", Required: false, Desc: "Path to a .patch/.diff file to read the patch from (alternative to patch)"},
					{Name: "dry_run", Type: "boolean", Required: false, Desc: "Report what would change without writing anything"},
					{Name: "max_fuzz", Type: "integer", Required: false, Desc: "Max leading/trailing context lines to ignore when locating hunks (default 2, max 10)"},
				},
				Behavior:    "Parses the unified diff, applies every hunk to the current file content (allowing limited context drift), then writes the results atomically. All-or-nothing: a conflict anywhere aborts before any file is touched. Use dry_run to preview first.",
				Constraints: "Target paths must stay inside the workspace; at least one of `patch`/`patch_path` is required",
			},
			Schema:             *BuildSchemaFromStruct(ApplyUnifiedDiffInput{}),
			Limits:             GetToolLimits(applyUnifiedDiffToolName),
			RequiredPermission: LevelWorkspaceWrite,
			ConcurrencySafe:    false,
		},
		{
			Name: "glob_search",
			Desc: ToolDescription{
				Purpose: "Find files by name pattern. Supports ** for recursive matching. Automatically respects .gitignore.",
				Parameters: []ParamDesc{
					{Name: "pattern", Type: "string", Required: true, Desc: "Glob pattern (e.g. '**/*.go', 'src/**/*.ts')"},
					{Name: "path", Type: "string", Required: false, Desc: "Directory to search in (default: cwd)"},
				},
				Behavior: "Searches for files matching the glob pattern, respecting .gitignore rules.",
			},
			Schema:             *BuildSchemaFromStruct(GlobInput{}),
			Limits:             GetToolLimits("glob_search"),
			RequiredPermission: LevelReadOnly,
			ConcurrencySafe:    true,
		},
		{
			Name: "grep_search",
			Desc: ToolDescription{
				Purpose: "Search file contents using regex. Automatically respects .gitignore. Start with output_mode=files_with_matches to find candidate files, then use output_mode=content to inspect matches.",
				Parameters: []ParamDesc{
					{Name: "pattern", Type: "string", Required: true, Desc: "Regex pattern to search for (required)"},
					{Name: "path", Type: "string", Required: false, Desc: "Directory to search in (default: cwd)"},
					{Name: "glob", Type: "string", Required: false, Desc: "File filter glob (e.g. '*.go', 'src/**/*.ts')"},
					{Name: "output_mode", Type: "string", Required: false, Desc: "Output mode (default: content)"},
					{Name: "case_insensitive", Type: "boolean", Required: false, Desc: "Case insensitive search (-i)"},
					{Name: "context_lines", Type: "integer", Required: false, Desc: "Context lines around match (-C)"},
					{Name: "before_context", Type: "integer", Required: false, Desc: "Lines before match (-B)"},
					{Name: "after_context", Type: "integer", Required: false, Desc: "Lines after match (-A)"},
					{Name: "head_limit", Type: "integer", Required: false, Desc: "Max results (default: 100)"},
					{Name: "offset", Type: "integer", Required: false, Desc: "Skip first N results"},
					{Name: "multiline", Type: "boolean", Required: false, Desc: "Enable multiline mode where . matches newlines"},
				},
				Behavior: "Searches file contents using the specified regex pattern, respecting .gitignore rules.",
			},
			Schema:             *BuildSchemaFromStruct(GrepInput{}),
			Limits:             GetToolLimits("grep_search"),
			RequiredPermission: LevelReadOnly,
			ConcurrencySafe:    true,
		},
		{
			// P4：模型主动检索记忆。对标 同类 CLI 对标证据件
			// （memories.search 工具 + 摘要注入 + 决策边界提示词）。本仓检索底座
			// bm25+RRF+trigram（SearchMemoryWithLayer）强于 同类 CLI 的纯子串；
			// 能力经 Executor.MemorySearchFunc 注入（tui 接线层装配）。
			// 文案把"词法命中的语义边界"直接教给模型（含 ≥3 字关键词、改写可能零命中），
			// 对齐 同类 CLI 模板把决策边界写进提示词的做法（实测教训：改写类召回 0.000）。
			Name: memorySearchToolName,
			Desc: ToolDescription{
				Purpose: "Search long-term memory for prior sessions' decisions, conventions, user preferences, and past findings. Use when the user references prior context, asks about previous decisions, or when the injected memory section seems to miss something relevant.",
				Parameters: []ParamDesc{
					{Name: "query", Type: "string", Required: true, Desc: "Search keywords. Lexical match: include distinctive (>=3 char) substrings of the topic; a paraphrase may not match"},
					{Name: "top_k", Type: "integer", Required: false, Desc: "Max results 1-10 (default 5)"},
				},
				Behavior: "Runs a lexical BM25 search over user and project memory scopes and returns matching entries with name, type, description, and file path. Zero hits is normal for paraphrased queries — retry with different keywords or continue without memory. Read-only.",
			},
			Schema:             *BuildSchemaFromStruct(MemorySearchInput{}),
			Limits:             GetToolLimits(memorySearchToolName),
			RequiredPermission: LevelReadOnly,
			ConcurrencySafe:    true,
		},
		{
			Name: "WebFetch",
			Desc: ToolDescription{
				// ★ S4（B2 / D5：描述与实现一致）：改造前 Purpose/Behavior 写的是
				// "convert to Markdown"，而实现是 stripHTMLTags ⇒ **纯文本，不是 Markdown**。
				// 描述说了没有的能力，与"工具面 = 能力面"同源问题，故一并对齐（§4.1）。
				Purpose: "Fetch a web page and return its text content.",
				Parameters: []ParamDesc{
					{Name: "url", Type: "string", Required: true, Desc: "Web page URL"},
				},
				Behavior: "Fetches the web page at the specified URL and returns its text with HTML tags removed (plain text, not Markdown).",
				// Constraints 段由上限常量渲染（不手写数字）：见 limits.go webToolConstraints()。
				Constraints: webToolConstraints("WebFetch"),
			},
			Schema:             *BuildSchemaFromStruct(WebFetchInput{}),
			Limits:             GetToolLimits("WebFetch"),
			RequiredPermission: LevelReadOnly,
			ConcurrencySafe:    true,
		},
		{
			Name: "WebSearch",
			Desc: ToolDescription{
				Purpose: "Search the internet using keywords. Returns a list of search results with titles, URLs, and snippets.",
				Parameters: []ParamDesc{
					{Name: "query", Type: "string", Required: true, Desc: "Search keywords"},
				},
				Behavior: "Performs an internet search using the specified query and returns matching results.",
				// ★ S4：上限与条数都由常量/契约渲染 —— 描述里不出现手写数字（B2 / T15）。
				Constraints: webToolConstraints("WebSearch"),
			},
			Schema:             *BuildSchemaFromStruct(WebSearchInput{}),
			Limits:             GetToolLimits("WebSearch"),
			RequiredPermission: LevelReadOnly,
			ConcurrencySafe:    true,
		},
		{
			Name: "Agent",
			Desc: ToolDescription{
				Purpose: "Start a sub-agent to execute an independent task. Supports multiple types: coder (general coding), plan (read-only planning), explore (code exploration), verification (adversarial testing).",
				Parameters: []ParamDesc{
					{Name: "prompt", Type: "string", Required: true, Desc: "Task description for the sub-agent"},
					{Name: "description", Type: "string", Required: false, Desc: "Brief task description (3-5 words)"},
					{Name: "agent_type", Type: "string", Required: false, Desc: "Sub-agent type: coder|plan|explore|verification|fork"},
				},
				Behavior: "Resolves agent_type parameter and passes to AgentContext, implementing typed sub-agent scheduling.",
			},
			Schema:             *BuildSchemaFromStruct(legacyAgentInput{}),
			Limits:             GetToolLimits("Agent"),
			RequiredPermission: LevelWorkspaceWrite,
			ConcurrencySafe:    false,
		},
		// ForkStatus — read a background fork sub-agent's status/result (, 2026-10-09)
		//
		// fire-and-forget 的另一半：竞品的 async 子 agent 必配「task_id → 读回」通道
		// （同类 CLI `wait_agent`："Completed statuses may include the agent's final message"，
		// core/src/tools/handlers/multi_agents_spec.rs:241-243），whale 此前只抄了"发"。
		//
		// 三态可读：completed / failed / timed_out（与 agent.go:890-903 的三条写入路径一一对应）。
		// 权限面（LevelReadOnly）：fork 启动时已过审批（本条目 Agent 声明 LevelWorkspaceWrite）；
		// 能力分桶 = CapMeta（控制面读回），不进 Read(*) 家族。
		{
			Name:        "ForkStatus",
			Description: "Read the status and result of a background fork sub-agent started via Agent with agent_type=fork. Covers completed, failed and timed_out; an agent that finished carries its final message as the result.",
			InputSchema: map[string]interface{}{
				"type":     "object",
				"required": []string{"task_id"},
				"properties": map[string]interface{}{
					"task_id": map[string]interface{}{"type": "string", "description": "Fork task ID returned by Agent with agent_type=fork (e.g. fork_1757...)"},
				},
			},
			RequiredPermission: LevelReadOnly,
			ConcurrencySafe:    true,
		},
		{
			Name: "TodoWrite",
			Desc: ToolDescription{
				Purpose: "Manage a todo list for tracking task progress. Supports pending, in_progress, and completed statuses with priority levels.",
				Parameters: []ParamDesc{
					{Name: "todos", Type: "array", Required: true, Desc: "Todo list items"},
				},
				Behavior: "Writes todo items to persistent storage, merging with existing items and removing completed ones.",
			},
			Schema:             *BuildSchemaFromStruct(TodoWriteInput{}),
			Limits:             GetToolLimits("TodoWrite"),
			RequiredPermission: LevelReadOnly,
			ConcurrencySafe:    false,
		},
		{
			Name: "AskUser",
			Description: "Ask the user a question. Supports default option, category-based rendering, and free-text input. Use this when user input is ambiguous (confidence<0.6) and clarification is needed. " +
				"The client always appends an 'Other' choice labelled \"None of the above\" when options are present. " +
				"Result shapes: (a) the chosen option's value, optionally followed on the next line by `user_note: <text>` when the user added notes to that choice; " +
				"or (b) `None of the above` followed on the next line by `user_answer: <text>` when the user rejected every preset choice — " +
				"in case (b) the text after `user_answer: ` IS the user's own answer: treat it as their reply and act on it, do not dismiss it as a side note. " +
				"For orchestration scenarios (clarify/plan-confirm), the orchestrator handles asking via AskUserGateway, do not call this tool.",
			InputSchema: map[string]interface{}{
				"type":     "object",
				"required": []string{"question", "category"},
				"properties": map[string]interface{}{
					"question": map[string]interface{}{
						"type":        "string",
						"description": "Question content (max 500 chars)",
					},
					"options": map[string]interface{}{
						"type":        "array",
						"description": "Provide 2-3 mutually exclusive choices. Put the recommended option first and mark it via default_idx. Do NOT include an 'Other' option; the client adds a free-form 'Other' choice automatically.",
						"items": map[string]interface{}{
							"type": "object",
							"properties": map[string]interface{}{
								"label":       map[string]interface{}{"type": "string", "description": "Option display text (max 20 chars)"},
								"value":       map[string]interface{}{"type": "string", "description": "Option value"},
								"description": map[string]interface{}{"type": "string", "description": "Option detail (max 80 chars)"},
							},
						},
					},
					"default_idx": map[string]interface{}{ // Phase 6 S2 新增（SNAPSHOT）
						"type":        "integer",
						"description": "Index of the recommended option (0-based, -1 means no default). Will be clamped to valid range.",
						"default":     -1,
					},
					"category": map[string]interface{}{ // Phase 6 S2 新增（SNAPSHOT）
						"type":        "string",
						"enum":        []string{"clarify", "plan-confirm", "cost-warning", "scope-confirm", "dialogue-clarify"},
						"description": "Question category. dialogue-clarify is for daily conversations; others are orchestrator-only.",
					},
					// T3 S1 新增（SNAPSHOT）：allow_free_text + context 两字段
					// ★ 2026-09-23 · ：语义**收窄**为单一含义 —— 只管「options 是否可为空」。
					// 它**不是**「模型能关掉自填口」的开关：客户端追加的 None of the above
					// 在有 options 时**总是**存在（对齐 同类 CLI 服务端强制 is_other=true 与
					// 同类 CLI 的无条件追加）。这样写也与原句 instead of selection 一致。
					"allow_free_text": map[string]interface{}{
						"type": "boolean",
						"description": "Free text *instead of* selection: true lets you ask a pure open-ended " +
							"question (options may be omitted); false requires 2-3 options. This does NOT " +
							"gate the client-added 'None of the above' choice, which is always available " +
							"when options are present. Default false.",
						"default": false,
					},
					"context": map[string]interface{}{
						"type":        "object",
						"description": "Optional context metadata (trace_id, task_id, etc.)",
					},
				},
			},
			RequiredPermission: LevelReadOnly,
			ConcurrencySafe:    false,
		},
		{
			Name: "Skill",
			Desc: ToolDescription{
				Purpose: "Load and execute a named skill. Skills provide specialized capabilities and domain knowledge for specific tasks. Each skill may declare a when_to_use hint guiding when it should be selected; consult the skill list (when_to_use field) to decide the most appropriate skill for the current task.",
				Parameters: []ParamDesc{
					{Name: "name", Type: "string", Required: true, Desc: "Skill name"},
					{Name: "prompt", Type: "string", Required: false, Desc: "Skill prompt"},
				},
				Behavior: "Calls ExecuteWithMeta to get metadata, injects Model/AllowedTools if non-empty, then executes the skill.",
			},
			Schema:             *BuildSchemaFromStruct(SkillInput{}),
			Limits:             GetToolLimits("Skill"),
			RequiredPermission: LevelWorkspaceWrite,
			ConcurrencySafe:    false,
		},
		{
			Name:        "McpListTools",
			Description: "List tools provided by an MCP server.",
			InputSchema: map[string]interface{}{
				"type":     "object",
				"required": []string{"server"},
				"properties": map[string]interface{}{
					"server": map[string]interface{}{"type": "string", "description": "MCP server name"},
				},
			},
			RequiredPermission: LevelReadOnly,
			ConcurrencySafe:    true,
		},
		{
			Name:        "McpCallTool",
			Description: "Call a tool on an MCP server.",
			InputSchema: map[string]interface{}{
				"type":     "object",
				"required": []string{"server", "tool_name"},
				"properties": map[string]interface{}{
					"server":    map[string]interface{}{"type": "string", "description": "MCP server name"},
					"tool_name": map[string]interface{}{"type": "string", "description": "Tool name to call"},
					"arguments": map[string]interface{}{"type": "object", "description": "Tool arguments"},
				},
			},
			RequiredPermission: LevelWorkspaceWrite,
			ConcurrencySafe:    false,
		},
		{
			Name:        "McpListPrompts",
			Description: "List prompt templates provided by an MCP server.",
			InputSchema: map[string]interface{}{
				"type":     "object",
				"required": []string{"server"},
				"properties": map[string]interface{}{
					"server": map[string]interface{}{"type": "string", "description": "MCP server name"},
				},
			},
			RequiredPermission: LevelReadOnly,
			ConcurrencySafe:    true,
		},
		{
			Name:        "McpGetPrompt",
			Description: "Get a prompt template from an MCP server.",
			InputSchema: map[string]interface{}{
				"type":     "object",
				"required": []string{"server", "prompt_name"},
				"properties": map[string]interface{}{
					"server":      map[string]interface{}{"type": "string", "description": "MCP server name"},
					"prompt_name": map[string]interface{}{"type": "string", "description": "Prompt template name"},
					"arguments":   map[string]interface{}{"type": "object", "description": "Prompt arguments"},
				},
			},
			RequiredPermission: LevelReadOnly,
			ConcurrencySafe:    true,
		},
		{
			Name:        "McpListResources",
			Description: "List resources provided by an MCP server.",
			InputSchema: map[string]interface{}{
				"type":     "object",
				"required": []string{"server"},
				"properties": map[string]interface{}{
					"server": map[string]interface{}{"type": "string", "description": "MCP server name"},
				},
			},
			RequiredPermission: LevelReadOnly,
			ConcurrencySafe:    true,
		},
		{
			Name:        "McpReadResource",
			Description: "Read a resource from an MCP server.",
			InputSchema: map[string]interface{}{
				"type":     "object",
				"required": []string{"server", "uri"},
				"properties": map[string]interface{}{
					"server": map[string]interface{}{"type": "string", "description": "MCP server name"},
					"uri":    map[string]interface{}{"type": "string", "description": "Resource URI"},
				},
			},
			RequiredPermission: LevelReadOnly,
			ConcurrencySafe:    true,
		},
		{
			Name:        "LspDefinition",
			Description: "Go to definition. Uses LSP server to find the definition location of a symbol.",
			InputSchema: map[string]interface{}{
				"type":     "object",
				"required": []string{"file_path", "line", "character"},
				"properties": map[string]interface{}{
					"file_path": map[string]interface{}{"type": "string", "description": "File path"},
					"line":      map[string]interface{}{"type": "integer", "description": "Line number (1-based)"},
					"character": map[string]interface{}{"type": "integer", "description": "Column number (1-based)"},
				},
			},
			RequiredPermission: LevelReadOnly,
			ConcurrencySafe:    true,
		},
		{
			Name:        "LspReferences",
			Description: "Find references. Uses LSP server to find all reference locations of a symbol.",
			InputSchema: map[string]interface{}{
				"type":     "object",
				"required": []string{"file_path", "line", "character"},
				"properties": map[string]interface{}{
					"file_path": map[string]interface{}{"type": "string", "description": "File path"},
					"line":      map[string]interface{}{"type": "integer", "description": "Line number (1-based)"},
					"character": map[string]interface{}{"type": "integer", "description": "Column number (1-based)"},
				},
			},
			RequiredPermission: LevelReadOnly,
			ConcurrencySafe:    true,
		},
		{
			Name:        "LspDiagnostics",
			Description: "Get diagnostics. Uses LSP server to get errors and warnings for a file.",
			InputSchema: map[string]interface{}{
				"type":     "object",
				"required": []string{"file_path"},
				"properties": map[string]interface{}{
					"file_path": map[string]interface{}{"type": "string", "description": "File path"},
				},
			},
			RequiredPermission: LevelReadOnly,
			ConcurrencySafe:    true,
		},
		{
			Name:        "LspHover",
			Description: "Get hover information. Uses LSP server to get type signature and documentation of a symbol.",
			InputSchema: map[string]interface{}{
				"type":     "object",
				"required": []string{"file_path", "line", "character"},
				"properties": map[string]interface{}{
					"file_path": map[string]interface{}{"type": "string", "description": "File path"},
					"line":      map[string]interface{}{"type": "integer", "description": "Line number (1-based)"},
					"character": map[string]interface{}{"type": "integer", "description": "Column number (1-based)"},
				},
			},
			RequiredPermission: LevelReadOnly,
			ConcurrencySafe:    true,
		},
		{
			Name:        "LspCompletion",
			Description: "Get code completion suggestions. Uses LSP server to get completion items at a position.",
			InputSchema: map[string]interface{}{
				"type":     "object",
				"required": []string{"file_path", "line", "character"},
				"properties": map[string]interface{}{
					"file_path": map[string]interface{}{"type": "string", "description": "File path"},
					"line":      map[string]interface{}{"type": "integer", "description": "Line number (1-based)"},
					"character": map[string]interface{}{"type": "integer", "description": "Column number (1-based)"},
				},
			},
			RequiredPermission: LevelReadOnly,
			ConcurrencySafe:    true,
		},
		{
			Name:        "LspRename",
			Description: "Rename a symbol. Uses LSP server to rename a symbol and return all locations that need modification.",
			InputSchema: map[string]interface{}{
				"type":     "object",
				"required": []string{"file_path", "line", "character", "new_name"},
				"properties": map[string]interface{}{
					"file_path": map[string]interface{}{"type": "string", "description": "File path"},
					"line":      map[string]interface{}{"type": "integer", "description": "Line number (1-based)"},
					"character": map[string]interface{}{"type": "integer", "description": "Column number (1-based)"},
					"new_name":  map[string]interface{}{"type": "string", "description": "New name"},
				},
			},
			RequiredPermission: LevelWorkspaceWrite,
			ConcurrencySafe:    false,
		},
		{
			Name:        "NotebookEdit",
			Description: "Edit a Jupyter Notebook cell. Supports replace, insert, and delete operations.",
			InputSchema: map[string]interface{}{
				"type":     "object",
				"required": []string{"path", "edit_kind"},
				"properties": map[string]interface{}{
					"path":       map[string]interface{}{"type": "string", "description": "Notebook file path"},
					"edit_kind":  map[string]interface{}{"type": "string", "enum": []string{"replace", "insert", "delete"}, "description": "Edit type"},
					"cell_index": map[string]interface{}{"type": "integer", "description": "Cell index (0-based)"},
					"new_source": map[string]interface{}{"type": "string", "description": "New cell source content (required for replace/insert)"},
					"cell_type":  map[string]interface{}{"type": "string", "enum": []string{"code", "markdown"}, "description": "Cell type (used for insert, default code)"},
				},
			},
			RequiredPermission: LevelWorkspaceWrite,
			ConcurrencySafe:    false,
		},
		{
			Name: "Monitor",
			// ★ 目标态描述（2026-09-30 B-1）。旧的尾句 "Each stdout line is displayed
			// in real-time, and a completion notification is sent when the process exits."
			// 是**机制承诺**：模型会把它写进给用户的回答里，与工具卡片重复
			// （真机 I-7.1 实证），且它描述的"逐行实时显示"正是 A 会话要废掉的行为。
			//
			// ★ 2026-10-13（手测卡② §19.4 选项 A）：上一次改写**没改干净** ——
			// 只把动词从 "is sent" 换成 "is reported"，仍然承诺「进程结束会汇报」，
			// 而 done 只进**显示缓冲**、**不进模型上下文**（卡② §18.2 已证）⇒ 模型会等一个
			// 永不到来的通知；且仍让模型去用 `/monitors`（人类斜杠命令，模型调不到 ——
			// 与 同类缺陷：只修了 tool_result、没修 Spec）。
			// A 会话已合入（输出不再进对话流），故删掉"顺序依赖"的旧注记。
			// 现在描述只写**模型真能走**的入口：MonitorTail（可带 wait_ms）。
			Description: "Start a background process to watch. Use it instead of sleep polling for log tails, API polling, dev servers, and other long-running commands. Its output is kept in a bounded buffer and is NOT streamed into the conversation, and no completion event is delivered to you: read it on demand with MonitorTail (optionally pass wait_ms to block until the process exits or the budget elapses, then you get its final status and exit code).",
			InputSchema: map[string]interface{}{
				"type":     "object",
				"required": []string{"command"},
				"properties": map[string]interface{}{
					"command":     map[string]interface{}{"type": "string", "description": "Command to monitor (e.g., tail -f log.txt, kubectl logs -f pod, npm run dev)"},
					"description": map[string]interface{}{"type": "string", "description": "Brief description of the monitoring task (e.g., 'watching build logs')"},
				},
			},
			RequiredPermission: LevelWorkspaceWrite,
			ConcurrencySafe:    false,
		},
		// MonitorTail — read a monitor's bounded tail output (, 2026-10-09)
		//
		// 与人类入口 `/monitors tail <id>` 同构：只读权威副本（Process.GetOutput），
		// 返回最后 N 行 + 总行数。修复前 Monitor 的输出对模型**全程不可读**
		// （回执只指人类斜杠命令），而 sleep 门恰恰主动推荐模型走 Monitor 这条路。
		//
		// 权限面（LevelReadOnly）：监控在启动时已过审批（Monitor 声明 LevelWorkspaceWrite）；
		// 能力分桶 = CapMeta（控制面读回），不进 Read(*) 家族。
		{
			Name:        "MonitorTail",
			Description: "Read the recent output of a monitored process, by monitor ID. Returns the last lines plus the total line count; monitor output is not streamed into the conversation, so this is how you inspect it. If the process is still running and you pass wait_ms, this blocks up to that many milliseconds for it to exit, then reports its final status and exit code — use it to learn that a background process finished instead of polling.",
			InputSchema: map[string]interface{}{
				"type":     "object",
				"required": []string{"monitor_id"},
				"properties": map[string]interface{}{
					"monitor_id": map[string]interface{}{"type": "string", "description": "Monitor ID returned by the Monitor tool (e.g. mon_1)"},
					"wait_ms": map[string]interface{}{
						"type":        "integer",
						"description": "Optional. If the process is still running, block up to this many milliseconds waiting for it to exit before returning (so the result carries its final status and exit code). 0 or omitted returns immediately.",
					},
				},
			},
			RequiredPermission: LevelReadOnly,
			ConcurrencySafe:    true,
		},
		{
			Name:        "git_diff",
			Description: "Get Git diff output. Supports viewing staged or working directory changes.",
			InputSchema: map[string]interface{}{
				"type":     "object",
				"required": []string{},
				"properties": map[string]interface{}{
					"staged": map[string]interface{}{"type": "boolean", "description": "Whether to view staged diff (git diff --cached), default false for working directory diff"},
				},
			},
			RequiredPermission: LevelReadOnly,
			ConcurrencySafe:    true,
		},
		{
			Name:        "git_status",
			Description: "Get Git working directory status. Shows modified, staged, and untracked files.",
			InputSchema: map[string]interface{}{
				"type":     "object",
				"required": []string{},
				"properties": map[string]interface{}{
					"short": map[string]interface{}{"type": "boolean", "description": "Use short format (default true)"},
				},
			},
			RequiredPermission: LevelReadOnly,
			ConcurrencySafe:    true,
		},
		{
			Name:        "git_log",
			Description: "Get Git commit log.",
			InputSchema: map[string]interface{}{
				"type":     "object",
				"required": []string{},
				"properties": map[string]interface{}{
					"n":       map[string]interface{}{"type": "integer", "description": "Show the last N commits (default 10)"},
					"oneline": map[string]interface{}{"type": "boolean", "description": "Use one-line format (default true)"},
					"path":    map[string]interface{}{"type": "string", "description": "Show commits for the specified path only"},
				},
			},
			RequiredPermission: LevelReadOnly,
			ConcurrencySafe:    true,
		},
		{
			Name:        "git_add",
			Description: "Add files to the Git staging area.",
			InputSchema: map[string]interface{}{
				"type":     "object",
				"required": []string{"files"},
				"properties": map[string]interface{}{
					"files": map[string]interface{}{"type": "array", "items": map[string]interface{}{"type": "string"}, "description": "List of file paths to add"},
				},
			},
			RequiredPermission: LevelWorkspaceWrite,
			ConcurrencySafe:    false,
		},
		{
			Name:        "git_commit",
			Description: "Commit changes in the Git staging area.",
			InputSchema: map[string]interface{}{
				"type":     "object",
				"required": []string{"message"},
				"properties": map[string]interface{}{
					"message": map[string]interface{}{"type": "string", "description": "Commit message"},
				},
			},
			RequiredPermission: LevelWorkspaceWrite,
			ConcurrencySafe:    false,
		},
		{
			Name:        "git_branch",
			Description: "List, create, or checkout Git branches.",
			InputSchema: map[string]interface{}{
				"type":     "object",
				"required": []string{},
				"properties": map[string]interface{}{
					"action": map[string]interface{}{"type": "string", "enum": []string{"list", "create", "checkout", "current"}, "description": "Action type (default list)"},
					"name":   map[string]interface{}{"type": "string", "description": "Branch name (required for create/checkout)"},
					"all":    map[string]interface{}{"type": "boolean", "description": "List all branches including remote (used with list)"},
				},
			},
			RequiredPermission: LevelWorkspaceWrite,
			ConcurrencySafe:    false,
		},
		// ScheduleCron — cron job scheduling
		{
			Name:        "schedule_cron",
			Description: "Schedule a cron job to run a command at specified intervals",
			InputSchema: map[string]interface{}{
				"type": "object",
				"properties": map[string]interface{}{
					"schedule":    map[string]interface{}{"type": "string", "description": "Cron expression (e.g., '0 * * * *' for hourly)"},
					"command":     map[string]interface{}{"type": "string", "description": "Command to execute"},
					"description": map[string]interface{}{"type": "string", "description": "Human-readable description of the job"},
				},
				"required": []string{"schedule", "command"},
			},
			RequiredPermission: LevelWorkspaceWrite,
			ConcurrencySafe:    false,
		},
		// UserChoice — multiple choice user selection
		{
			Name:        "user_choice",
			Description: "Present multiple choices to the user and get their selection",
			InputSchema: map[string]interface{}{
				"type": "object",
				"properties": map[string]interface{}{
					"question": map[string]interface{}{"type": "string", "description": "Question to ask the user"},
					"options":  map[string]interface{}{"type": "array", "items": map[string]interface{}{"type": "string"}, "description": "List of options to choose from"},
				},
				"required": []string{"question", "options"},
			},
			RequiredPermission: LevelReadOnly,
			ConcurrencySafe:    false,
		},
		// ConfigGet — read configuration
		{
			Name:        "config_get",
			Description: "Get a configuration value by key",
			InputSchema: map[string]interface{}{
				"type": "object",
				"properties": map[string]interface{}{
					"key": map[string]interface{}{"type": "string", "description": "Configuration key (e.g., 'model', 'permission_level')"},
				},
				"required": []string{"key"},
			},
			RequiredPermission: LevelReadOnly,
			ConcurrencySafe:    true,
		},
		// ConfigSet — write configuration
		{
			Name:        "config_set",
			Description: "Set a configuration value",
			InputSchema: map[string]interface{}{
				"type": "object",
				"properties": map[string]interface{}{
					"key":   map[string]interface{}{"type": "string", "description": "Configuration key"},
					"value": map[string]interface{}{"type": "string", "description": "Configuration value"},
				},
				"required": []string{"key", "value"},
			},
			RequiredPermission: LevelWorkspaceWrite,
			ConcurrencySafe:    false,
		},
		// ExportSession — export session
		{
			Name:        "export_session",
			Description: "Export the current session to a JSON file",
			InputSchema: map[string]interface{}{
				"type": "object",
				"properties": map[string]interface{}{
					"path": map[string]interface{}{"type": "string", "description": "File path to export the session to"},
				},
				"required": []string{"path"},
			},
			// Q1（2026-09-14 用户拍板）：它**确实写 JSON 文件到磁盘**，能力上是 Edit。
			// 此前声明 LevelReadOnly 与行为不符 —— L1 让声明成为唯一真相源后，若不正名，
			// 它会进入只读工具面（只读角色可写盘），R2 守卫 TestGuard_NoReadCapabilityViaExec 必红。
			RequiredPermission: LevelWorkspaceWrite,
			ConcurrencySafe:    false,
		},
		// StructuredOutput — structured output
		{
			Name:        "structured_output",
			Description: "Force the output to conform to a JSON schema",
			InputSchema: map[string]interface{}{
				"type": "object",
				"properties": map[string]interface{}{
					"schema": map[string]interface{}{"type": "object", "description": "JSON Schema that the output must conform to"},
					"data":   map[string]interface{}{"description": "The data to validate and format according to the schema"},
				},
				"required": []string{"schema"},
			},
			RequiredPermission: LevelReadOnly,
			ConcurrencySafe:    true,
		},
		// EnterWorktree — enter worktree
		{
			Name:        "enter_worktree",
			Description: "Create an isolated Git worktree and switch the session to it. Only call when the user explicitly requests a worktree.",
			InputSchema: map[string]interface{}{
				"type":     "object",
				"required": []string{},
				"properties": map[string]interface{}{
					"name": map[string]interface{}{
						"type":        "string",
						"description": "Optional worktree name. Can only contain letters, digits, dots, underscores, and hyphens, max 64 characters. Auto-generates a random name if not provided.",
					},
				},
			},
			RequiredPermission: LevelWorkspaceWrite,
			ConcurrencySafe:    false,
		},
		// ExitWorktree — exit worktree
		{
			Name:        "exit_worktree",
			Description: "Exit the worktree session and restore the original working directory. Only call when the user explicitly requests to exit the worktree.",
			InputSchema: map[string]interface{}{
				"type":     "object",
				"required": []string{"action"},
				"properties": map[string]interface{}{
					"action": map[string]interface{}{
						"type":        "string",
						"enum":        []string{"keep", "remove"},
						"description": "\"keep\" preserves the worktree directory and branch; \"remove\" deletes both.",
					},
					"discard_changes": map[string]interface{}{
						"type":        "boolean",
						"description": "When action is \"remove\" and the worktree has uncommitted files or unmerged commits, must be set to true to delete. Otherwise the tool will refuse and list changes.",
					},
				},
			},
			RequiredPermission: LevelWorkspaceWrite,
			ConcurrencySafe:    false,
		},
		// ToolSearch — search/discover available tools
		{
			Name:        "ToolSearch",
			Description: "Search and load available tools. Two modes: (1) search by keywords with {\"query\": \"git\"} to discover tools; (2) load a tool's schema with {\"load\": \"git_status\"} to make it available in subsequent requests. Use this when you need a tool that is not currently in your tool set (e.g., LSP, MCP, Cron, Worktree tools are deferred and must be loaded first).",
			InputSchema: map[string]interface{}{
				"type":     "object",
				"required": []string{},
				"properties": map[string]interface{}{
					"query":    map[string]interface{}{"type": "string", "description": "Search keywords to find tools by name or description"},
					"category": map[string]interface{}{"type": "string", "enum": []string{"builtin", "mcp", "plugin", "all"}, "description": "Tool category filter (default all, used with query)"},
					"load":     map[string]interface{}{"type": "string", "description": "Tool name to load. Loads the tool's schema so it becomes available in subsequent requests. Use after finding a tool via search."},
				},
			},
			RequiredPermission: LevelReadOnly,
			ConcurrencySafe:    true,
		},
		// LspImplementation — go to implementation
		{
			Name:        "LspImplementation",
			Description: "Go to implementation. Uses LSP server to find implementation locations of an interface or abstract symbol.",
			InputSchema: map[string]interface{}{
				"type":     "object",
				"required": []string{"file_path", "line", "character"},
				"properties": map[string]interface{}{
					"file_path": map[string]interface{}{"type": "string", "description": "File path"},
					"line":      map[string]interface{}{"type": "integer", "description": "Line number (1-based)"},
					"character": map[string]interface{}{"type": "integer", "description": "Column number (1-based)"},
				},
			},
			RequiredPermission: LevelReadOnly,
			ConcurrencySafe:    true,
		},
		// LspTypeDefinition — go to type definition
		{
			Name:        "LspTypeDefinition",
			Description: "Go to type definition. Uses LSP server to find the type definition location of a symbol.",
			InputSchema: map[string]interface{}{
				"type":     "object",
				"required": []string{"file_path", "line", "character"},
				"properties": map[string]interface{}{
					"file_path": map[string]interface{}{"type": "string", "description": "File path"},
					"line":      map[string]interface{}{"type": "integer", "description": "Line number (1-based)"},
					"character": map[string]interface{}{"type": "integer", "description": "Column number (1-based)"},
				},
			},
			RequiredPermission: LevelReadOnly,
			ConcurrencySafe:    true,
		},
		// LspCodeActions — get code action suggestions
		{
			Name:        "LspCodeActions",
			Description: "Get code action suggestions. Uses LSP server to get code actions at a position (quick fixes, refactoring, etc).",
			InputSchema: map[string]interface{}{
				"type":     "object",
				"required": []string{"file_path", "line", "character"},
				"properties": map[string]interface{}{
					"file_path":   map[string]interface{}{"type": "string", "description": "File path"},
					"line":        map[string]interface{}{"type": "integer", "description": "Line number (1-based)"},
					"character":   map[string]interface{}{"type": "integer", "description": "Column number (1-based)"},
					"action_kind": map[string]interface{}{"type": "string", "description": "Code action kind filter (e.g., quickfix, refactor, source.organizeImports)"},
				},
			},
			RequiredPermission: LevelWorkspaceWrite,
			ConcurrencySafe:    false,
		},
		// LspDocumentSymbols — get document symbol outline
		{
			Name:        "LspDocumentSymbols",
			Description: "Get document symbol outline. Uses LSP server to get the symbol outline of a file (functions, classes, variables, etc).",
			InputSchema: map[string]interface{}{
				"type":     "object",
				"required": []string{"file_path"},
				"properties": map[string]interface{}{
					"file_path": map[string]interface{}{"type": "string", "description": "File path"},
				},
			},
			RequiredPermission: LevelReadOnly,
			ConcurrencySafe:    true,
		},
		// SendMessage — send message to other agents
		{
			Name:        "SendMessage",
			Description: "Send a message to another agent. Used for inter-agent communication, supports sending text messages to a specified agent.",
			InputSchema: map[string]interface{}{
				"type":     "object",
				"required": []string{"agent_id", "message"},
				"properties": map[string]interface{}{
					"agent_id": map[string]interface{}{"type": "string", "description": "Target agent ID"},
					"message":  map[string]interface{}{"type": "string", "description": "Message content"},
				},
			},
			RequiredPermission: LevelReadOnly,
			ConcurrencySafe:    false,
		},
		// TaskCreate — create background task
		{
			Name:        "TaskCreate",
			Description: "Create a background task. Executes a command in the background and returns a task ID; read its output with TaskOutput (use TaskList for status, TaskStop to stop it).",
			InputSchema: map[string]interface{}{
				"type":     "object",
				"required": []string{"command"},
				"properties": map[string]interface{}{
					"command":     map[string]interface{}{"type": "string", "description": "Command to execute"},
					"description": map[string]interface{}{"type": "string", "description": "Task description"},
					// ★ 同族小项：旧文案 "0 means no timeout" 与 的
					// 实际行为矛盾（0 ⇒ clamp 成默认 600s / 上界 7200s，task.go:104）——
					// 会诱导模型对用户承诺"不会超时"。文案必须与实际生效值一致。
					"timeout": map[string]interface{}{"type": "integer", "description": "Timeout in seconds. 0 uses the default (600s); values above 7200s are clamped."},
				},
			},
			RequiredPermission: LevelWorkspaceWrite,
			ConcurrencySafe:    false,
		},
		// TaskList — list background tasks
		{
			Name:        "TaskList",
			Description: "List background tasks. Shows all background tasks and their status.",
			InputSchema: map[string]interface{}{
				"type":     "object",
				"required": []string{},
				"properties": map[string]interface{}{
					"status": map[string]interface{}{"type": "string", "enum": []string{"running", "completed", "failed", "all"}, "description": "Filter by status (default all)"},
				},
			},
			RequiredPermission: LevelReadOnly,
			ConcurrencySafe:    true,
		},
		// TaskStop — stop background task
		{
			Name:        "TaskStop",
			Description: "Stop a background task. Stops the background task with the specified ID.",
			InputSchema: map[string]interface{}{
				"type":     "object",
				"required": []string{"task_id"},
				"properties": map[string]interface{}{
					"task_id": map[string]interface{}{"type": "string", "description": "Task ID to stop"},
				},
			},
			RequiredPermission: LevelWorkspaceWrite,
			ConcurrencySafe:    false,
		},
		// TaskOutput — read background task output (, 2026-10-09)
		//
		// 模型可见描述必须点明「TaskList 只有状态、内容要用本工具取」——
		// 修复前正是这句话缺失 + 工具不存在，才逼出「模型凭空断言输出为 hi」。
		//
		// 权限面（LevelReadOnly）论据：任务创建时已过审批，读其已采集输出不新增越权；
		// 能力分桶 = CapMeta（与 TaskList 同桶），不进 Read(*) 家族（§0′ 第 2 条）。
		{
			Name:        "TaskOutput",
			Description: "Read what a background task actually printed, by task ID. Use it after TaskCreate or bash with run_in_background — TaskList only reports status, not output. Output is bounded; when truncated, a total-size note is included.",
			InputSchema: map[string]interface{}{
				"type":     "object",
				"required": []string{"task_id"},
				"properties": map[string]interface{}{
					"task_id": map[string]interface{}{"type": "string", "description": "Task ID returned by TaskCreate or by bash with run_in_background"},
				},
			},
			RequiredPermission: LevelReadOnly,
			ConcurrencySafe:    true,
		},
		// CronList — list cron jobs
		{
			Name:        "CronList",
			Description: "List cron jobs. Shows all scheduled cron jobs.",
			InputSchema: map[string]interface{}{
				"type":       "object",
				"required":   []string{},
				"properties": map[string]interface{}{},
			},
			RequiredPermission: LevelReadOnly,
			ConcurrencySafe:    true,
		},
		// CronDelete — delete cron job
		{
			Name:        "CronDelete",
			Description: "Delete a cron job. Deletes the cron job with the specified ID.",
			InputSchema: map[string]interface{}{
				"type":     "object",
				"required": []string{"job_id"},
				"properties": map[string]interface{}{
					"job_id": map[string]interface{}{"type": "string", "description": "Cron job ID to delete"},
				},
			},
			RequiredPermission: LevelWorkspaceWrite,
			ConcurrencySafe:    false,
		},
		// Sleep — wait for specified duration
		{
			Name:        "Sleep",
			Description: "Wait for a specified duration. Used when waiting is needed (e.g., waiting for build completion, service startup). Maximum wait is 60 seconds.",
			InputSchema: map[string]interface{}{
				"type":     "object",
				"required": []string{"seconds"},
				"properties": map[string]interface{}{
					"seconds": map[string]interface{}{"type": "number", "description": "Wait time in seconds, minimum 0.1, maximum 60", "minimum": 0.1, "maximum": 60},
				},
			},
			RequiredPermission: LevelReadOnly,
			ConcurrencySafe:    true,
		},
		// N1 subagent / Task tool：让 AI 派生子任务，支持递归与并发。
		// ReadOnly + ConcurrencySafe：Task tool 本身仅编排，实际写操作由子任务内工具各自声明。
		//
		// ⚠️ G6：**本处才是模型可见的 Task 工具定义**。它经
		// Registry.Get(name) → AgentContext.buildAgentTools()（agent.go:1272）与
		// tui 的 registry.AllDirect()（stream.go:620 / runner_core.go:947）注入 tools 数组。
		// 同包 `task_tool.go` 曾有一份**同名 Task 的 AI 面**（taskToolPrompt / Schema()），
		// 全仓零调用点（executor.go:1720 的 taskHandler 只调 TaskTool.Call）⇒
		// **G7已把那份成死码的 16 个 Tool 接口方法整体删除**，
		// `task_tool.go` 现在只剩执行入口 `Call`（不再是 tools.Tool 实现）。
		// ⇒ Task 的模型可见定义**从此只有本表一处**，改这里即改模型所见（不必再"两处同改"）。
		// 守卫见 g6_tools_not_in_toolface_test.go（P1 钉本表、P2 钉 task_tool.go 只剩 Call）。
		//
		// G6-① 撤下 `tools` 参数（与 B2 删除 skills 的 allowed-tools 注入同一处置逻辑：
		// 不留假机制）：子 agent 的工具面由 AgentType（角色面）∩ ParentToolSet（委派不扩权）
		// 唯一决定，唯一计算点 getAllowedToolSet()（agent.go:1235-1237），**不含本参数**——
		// 该「与 input.Tools 求交集」的机制已于 L2移除。
		// 继续向模型广告它 = 承诺一个不存在的收窄能力 ⇒ 模型/调用方误信「收窄已生效」，
		// 若据此放宽 AgentType 即**静默超授**。故连描述里的 "can use a subset of tools" 一并撤掉。
		{
			Name:        "Task",
			Description: "Launch a new sub-agent to execute an independent task. The sub-agent runs with its own conversation context; its tool set is determined by its declared agent role (not by this call). Supports recursive subtasks (max_depth=3) and concurrent execution.",
			InputSchema: map[string]interface{}{
				"type":     "object",
				"required": []string{"prompt"},
				"properties": map[string]interface{}{
					"prompt": map[string]interface{}{
						"type":        "string",
						"description": "Task description / instruction for the sub-agent (required)",
					},
					"model": map[string]interface{}{
						"type":        "string",
						"description": "Model name to force for the sub-agent. If empty, inherits parent's model.",
					},
				},
			},
			// L1/L2：Task 与 Agent 同为**委派型工具**（经 AgentTaskMgr.Spawn
			// 派生子代理），故与 Agent 对齐定为 WorkspaceWrite。
			//
			// 此前声明 LevelReadOnly，但生产生效值是 WorkspaceWrite（不在旧手写表里 →
			// 走 fail-safe 默认）—— 声明与行为不符（§8.4-②）。定 WW 是**保持既有生效行为**：
			// · workspace-write 档：需确认（旧表 fallback 的同一行为）；
			// · read-only 档：拒绝（同上，只读会话不得开子代理）。
			RequiredPermission: LevelWorkspaceWrite,
			// Task 子代理的角色由 task_tool.go 显式声明（declaration is truth）。
			ConcurrencySafe: true,
		},
	}

	// 延迟工具集合：低频/高级功能工具，不进入默认 tools 数组。
	// 对标同类 CLI defer_loading：核心开发工作流（文件操作/搜索/git/web/agent）保持 Direct，
	// 高级功能（LSP/MCP/Cron/Worktree/Notebook/后台任务/配置）标记 Deferred。
	// 模型通过 ToolSearch({"load":"工具名"}) 或自动加载兜底使用。
	// REF-降级策略-：延迟工具 handler 仍注册（executor.go），仅 schema 不注入 tools 数组。
	deferredToolNames := map[string]bool{
		// Notebook/Monitor（3个）
		"NotebookEdit": true, "Monitor": true, "MonitorTail": true,
		// Git（6个）：中频但可延迟，模型通过 ToolSearch 加载后使用
		"git_diff": true, "git_status": true, "git_log": true,
		"git_add": true, "git_commit": true, "git_branch": true,
		// Cron（3个）
		"schedule_cron": true, "CronList": true, "CronDelete": true,
		// Worktree（2个）
		"enter_worktree": true, "exit_worktree": true,
		// 后台任务（5个）
		"TaskCreate": true, "TaskList": true, "TaskStop": true, "TaskOutput": true, "Sleep": true,
		// 子代理读回（1个）：fork 是 fire-and-forget，回执让模型按 task_id 拉结论
		"ForkStatus": true,
		// 配置（3个）
		"config_get": true, "config_set": true, "export_session": true,
		// 其他中频工具（3个）：可延迟加载
		"user_choice": true, "structured_output": true, "SendMessage": true,
	}

	// MCP（6个）/ LSP（10个）延迟标记从**唯一名单** mcpMetaToolNames / lspToolNames 派生
	// （能力面装配同源），避免在 deferredToolNames 里再抄一份工具名清单造成漂移。
	for _, name := range mcpMetaToolNames {
		deferredToolNames[name] = true
	}
	for _, name := range lspToolNames {
		deferredToolNames[name] = true
	}

	// N16 桥接：Desc.Render() → Description，Schema.ToMap() → InputSchema（向后兼容）
	// 优化 A：参数描述已被 schema property description 覆盖时用 RenderLean 消除双份传输，
	// 未覆盖（schema 缺参数或描述为空）时回退 Render() 保证参数说明不丢失。
	for _, tool := range tools {
		// ★ S3（裁定 C=②「彻底不注册」）：此刻无能力的工具**不建 spec**。
		// 为什么落在这一层：AllDirect() / List() / DeferredToolDirectory() 全都遍历 r.builtin，
		// 不注册即自动不在任何"按注册表派生"的面里，无需逐面打补丁（§11.1 落点①的第①②面）。
		// 档位目录（toolLevelsSnapshot）不在这一步覆盖范围内 —— 它是 init 期静态快照且合并"只增不减"，
		// 已由 toollevel.go 用自己的**同一谓词**过滤。
		// ★ 注意（实测修正）：本步**不**使 handler 不可达 —— handler 由 executor.go 独立注册，
		// 故执行门另有闸门（见 registerBuiltinHandlers 里的 WebSearch）。
		if !toolAvailable(tool.Name) {
			continue
		}
		if tool.Desc.Purpose != "" {
			if tool.Desc.ParametersCoveredBySchema(tool.Schema) {
				tool.Description = tool.Desc.RenderLean()
			} else {
				tool.Description = tool.Desc.Render()
			}
		}
		if tool.Schema.Type != "" {
			tool.InputSchema = tool.Schema.ToMap()
		}
		if tool.Limits.MaxResultChars == 0 {
			tool.Limits = GetToolLimits(tool.Name)
		}
		// 标记延迟工具 Exposure（零值为 ExposureDirect，仅需设置 Deferred）
		if deferredToolNames[tool.Name] {
			tool.Exposure = ExposureDeferred
		}
		// N16 Parameters 空值校验（v2 补充，回应审查报告建议改5）
		if len(tool.Schema.Properties) > 0 && len(tool.Desc.Parameters) == 0 {
			log.Printf("[N16] tool %s: schema has %d properties but Parameters desc is empty, AI will not see parameter descriptions",
				tool.Name, len(tool.Schema.Properties))
		}
		r.builtin[tool.Name] = tool
	}
}

// ValidateInput validates tool input
func ValidateInput(toolName, input string) *ValidationError {
	if input == "" {
		return &ValidationError{
			Message: fmt.Sprintf("tool `%s` input is empty, please provide valid JSON parameters", toolName),
		}
	}

	var parsed interface{}
	if err := json.Unmarshal([]byte(input), &parsed); err != nil {
		return &ValidationError{
			Message: fmt.Sprintf("tool `%s` input is not valid JSON: %v", toolName, err),
		}
	}

	if _, ok := parsed.(map[string]interface{}); !ok {
		typeName := "unknown"
		switch parsed.(type) {
		case []interface{}:
			typeName = "array"
		case string:
			typeName = "string"
		case float64:
			typeName = "number"
		case bool:
			typeName = "boolean"
		}
		return &ValidationError{
			Message: fmt.Sprintf("tool `%s` input must be a JSON object, actual type: %s", toolName, typeName),
		}
	}

	return nil
}

// ValidationError represents a validation error
type ValidationError struct {
	Message string
}

// Error implements the error interface
func (e *ValidationError) Error() string {
	return e.Message
}

// NormalizeToolName normalizes tool name (supports aliases)
func NormalizeToolName(name string) string {
	aliases := map[string]string{
		"read":   "read_file",
		"write":  "write_file",
		"edit":   "edit_file",
		"glob":   "glob_search",
		"grep":   "grep_search",
		"search": "WebSearch",
		"fetch":  "WebFetch",
	}
	if canonical, ok := aliases[name]; ok {
		return canonical
	}
	return name
}

// MaxToolResultLength is the default max length of tool result output before truncation.
// N16: 从 2000 提升到 50000，对齐业内既有实现 DEFAULT_MAX_RESULT_SIZE_CHARS。
var MaxToolResultLength = 50000

// FormatToolResult formats a tool result string with configurable truncation length
// 收口：砍尾走唯一权威 TruncateTail（真实丢弃量 + 上游标记合并）。
// 注意：maxLen 语义为 **rune 数**（与 foldToolResult 同一口径；旧实现按字节切，
// 会切断 UTF-8 多字节字符）。
func FormatToolResult(result string, maxLen int) string {
	return TruncateTail(result, maxLen)
}

// ToolResultHeadLines 工具结果折叠时显示的 head 行数（对标同类 CLI TOOL_CALL_MAX_LINES=5）
// REF-外部: 同类 CLI
var ToolResultHeadLines = 5

// ToolResultTailLines 工具结果折叠时显示的 tail 行数（对标同类 CLI head+tail 对半）
var ToolResultTailLines = 5

// splitResultLines 是工具结果「行」的**唯一定义**：内容行数，不含结尾换行符产生的空元素。
//
// 折叠不变量 **head 5 / tail 5 / omitted = N−10** 依赖这个唯一定义。
// 旧实现直接 `strings.Split(output, "\n")` 计数，而 read_file 等生产输出**以 "\n" 结尾**
// ⇒ Split 多出一个空元素：省略数多算 1（显示 `+11` 而非 `+10`），且该空元素还占掉 tail 的
// 一个名额 ⇒ 尾块只显示 **4 行真内容**，与设计"头5+尾5"不符。
//
// ★ 既有测试全部用 `strings.Join(lines, "\n")`（**无尾换行**）构造输入 ⇒ 这条生产形态
// 此前**零判据**，所以这个 off-by-one 长期未被发现。守卫见
// TestFormatToolResultStruct_TrailingNewline_HeadTailOmittedInvariant（带尾换行）。
//
// 定义：去掉结尾换行符后按 "\n" 切分；空输出 = 0 行。
func splitResultLines(output string) []string {
	trimmed := strings.TrimRight(output, "\n")
	if trimmed == "" {
		return nil
	}
	return strings.Split(trimmed, "\n")
}

// ToolFoldHint 折叠省略行里「如何看完整输出」的提示口径。
//
// 为什么是结构而不是字符串：提示里的**命令名**与**序号**都由调用方所处的入口决定 ——
// TUI 有 /tool（`internal/tui/format.go` case "tool"），`--plain` 没有（`plain.go` 零命中
// `case "tool"`）。让每个调用方自己拼字符串 ⇒ 拼接口径分散，又变成「同一提示多入口各写各的」
// （本仓 同族）。故这里只留**一个**渲染点 foldHintText，调用方只声明
// "我这个入口有什么命令、序号是多少"。
//
// 取值口径：
// - Command 为空 ⇒ **不提示**：该入口没有这个命令时不许说谎（plain 即此形态）。
// - Seq > 0 ⇒ "<Command> <Seq>"：与 /tool 的 [N/M] **同源**（序号来源见 tui 侧 toolResultSeq）。
// - Seq <= 0 ⇒ "<Command>"：无序号上下文的兼容形态。
type ToolFoldHint struct {
	Command string
	Seq     int
}

// foldHintText 是折叠提示文案的**唯一渲染点**。
func foldHintText(h ToolFoldHint) string {
	if h.Command == "" {
		return ""
	}
	if h.Seq > 0 {
		return "输入 " + h.Command + " " + strconv.Itoa(h.Seq) + " 查看完整输出"
	}
	return "输入 " + h.Command + " 查看完整输出"
}

// foldEllipsis 渲染省略行（省略行数恒定可见，提示可选）。
func foldEllipsis(omitted int, h ToolFoldHint) string {
	if hint := foldHintText(h); hint != "" {
		return fmt.Sprintf("\n… +%d lines (%s)\n", omitted, hint)
	}
	return fmt.Sprintf("\n… +%d lines\n", omitted)
}

// FormatToolResultStruct formats a tool Result struct for display
func FormatToolResultStruct(result Result) string {
	return FormatToolResultStructWithHint(result, MaxToolResultLength, ToolFoldHint{Command: "/tool"})
}

// FormatToolResultStructWithMaxLen 格式化工具结果用于显示（无序号提示的兼容入口）。
// REF-外部: 同类 CLI output_lines
func FormatToolResultStructWithMaxLen(result Result, maxLen int) string {
	return FormatToolResultStructWithHint(result, maxLen, ToolFoldHint{Command: "/tool"})
}

// FormatToolResultStructWithHint 是折叠显示的**唯一实现**，提示口径由 hint 决定。
//
// C1 切口②：折叠提示曾是写死的「输入 /tool 查看完整输出」，而无参 /tool
// 恒返回**最近一条** ⇒ 用户照第 3 个折叠块的提示输入，拿到的是第 11 条 —— 提示指东、返回打西。
// 现在提示可带 Seq，序号由调用方注入、与 /tool 同源（见 internal/tui/tool_result_seq.go）。
func FormatToolResultStructWithHint(result Result, maxLen int, hint ToolFoldHint) string {
	text, _, _ := foldToolResult(result, maxLen, hint)
	return text
}

// ToolFoldMarkerFor 返回 result 在给定 maxLen/hint 下产生的**折叠省略行**：
// - marker = 该省略行在屏幕上的精确字面（不含首尾换行）；未发生折叠 ⇒ ""。
// - hidden = 被它吞掉的中段原文行（原样，不裁剪）；未发生折叠 ⇒ nil。
//
// 为什么必须有（2026-10-05 · 真机报障「中间有一段我复制不了」）：
// 工具结果在显示层被折成 head+tail，中段原文**根本不在屏幕上**；而鼠标拖选复制的
// 正是渲染产物 ⇒ 用户拖过折叠块，中段永远拿不到。另一边 /copy all 与 /tool N 走
// session 权威源，本来就完整 ⇒ 同一段内容两个入口给出不同结果（与 同族）。
//
// 本函数让**复制层**能按「渲染层自己产出的省略行字面」把中段还原成原文。
// ★ 关键：marker 与 hidden 由同一个 foldToolResult 一次算出，与
// FormatToolResultStructWithHint 共用这次计算 —— 所以「渲染怎么折的」与
// 「复制怎么还原」不可能各写一份而脱钩（改了也只会一起改）。
func ToolFoldMarkerFor(result Result, maxLen int, hint ToolFoldHint) (marker string, hidden []string) {
	_, hidden, marker = foldToolResult(result, maxLen, hint)
	return marker, hidden
}

// errOutputSelfLabels 报告错误输出是否**已自带工具名前缀**（供显示层去重， 第 4 件）。
//
// 认两种自报形态（都允许 executor 的 `tool execution failed: ` 包装在更外层）：
// - "<name>: …" —— 工具自身报告的形态；
// - "tool execution failed: <name>…" —— executor 包装后的形态。
//
// 判据用 `<name>:`（带冒号）而不是裸 `<name>`：避免 "bashful error" 这类前缀被误判成
// 已自报 "bash"，从而把渲染头该补的名字也省掉。
func errOutputSelfLabels(output, name string) bool {
	if name == "" {
		return false
	}
	s := strings.TrimPrefix(output, "tool execution failed: ")
	return strings.HasPrefix(s, name+":")
}

// foldToolResult 是工具结果折叠的**唯一计算点**：一次算出三个同源产物 ——
// 「显示文本」/「被省略的中段原文」/「省略行字面」。
//
// 拆出它的理由：显示侧只消费 text，复制侧只消费 marker+hidden，两者若各算一遍，
// 就是同一折叠语义的第二份实现（本仓 同族）；共用一次计算才使"同源"成为
// 结构保证而不是注释约定。
//
// 未发生折叠（短输出 / 错误输出 / 空输出占位）⇒ marker == ""、hidden == nil。
//
// ⚠️ hidden 取自**截断后**的 output（与 head/tail 同一份文本），而不是 session 里的
// 原始 Output：拖选选中的是屏幕上那一段，"屏幕上被省略掉的是什么"才是它该还原的东西。
// 超长输出（> maxLen 字符）下 /tool 会给到更长的原文，这是既有的另一条口径，不在本函数。
func foldToolResult(result Result, maxLen int, hint ToolFoldHint) (text string, hidden []string, marker string) {
	if maxLen <= 0 {
		maxLen = MaxToolResultLength
	}
	// 空输出占位：与**回传面**共用同一实现（session.NormalizeToolOutput）——
	// `echo hi > out/x.txt` 这类命令成功但 stdout 本就为空，不留 `📋 bash:` 后一片空白
	// （2026-09-30 · I-8.2 真机手测：该空白让用户以为结果丢了；同一事实回传面也必须非空，
	// 否则模型会把同一条命令重发 4 次）。放在 IsError 分支之前 ⇒ 错误态一并受力。
	normalized := session.NormalizeToolOutput(result.Output, result.IsError)

	if result.IsError {
		// 第 4 件（2026-10-07 真机 T-14b）：错误 payload 常**自带**结构化前缀
		// （executor.go 包装成 `tool execution failed: <工具名>: <原因>`）⇒ 若渲染头再
		// 补一次工具名，屏幕就出现「❌ 名: tool execution failed: 名: …」的双重前缀。
		// 此处只去**显示冗余**：payload 已自报工具名时，渲染头不再重复；
		// 措辞/包装分层（要不要保留 `tool execution failed:`）属 (c)，本处不动 payload。
		if errOutputSelfLabels(normalized, result.Name) {
			return "❌ " + normalized, nil, ""
		}
		return fmt.Sprintf("❌ %s: %s", result.Name, normalized), nil, ""
	}

	// Windows \r\n 归一化（子审查建议1：防止行尾残留 \r 导致显示异常）
	output := strings.ReplaceAll(normalized, "\r\n", "\n")

	// 字符级兜底截断（收口：唯一权威 TruncateTail —— 按 rune 切避免切断
	// UTF-8 多字节字符；上游（如 bash）已砍过一刀时自动合并计数，单一标记，
	// 不再出现「第二刀把第一刀的标记吃掉、报 23」的自我覆盖形态）。
	output = TruncateTail(output, maxLen)

	// 行级 head+tail 折叠（对标同类 CLI output_lines，阈值对齐业内既有实现：> Head+Tail 才折叠）
	// 行数一律走 splitResultLines（唯一定义），不得再用裸 Split。
	lines := splitResultLines(output)
	if len(lines) <= ToolResultHeadLines+ToolResultTailLines {
		return fmt.Sprintf("📋 %s:\n%s", result.Name, strings.Join(lines, "\n")), nil, ""
	}

	omitted := len(lines) - ToolResultHeadLines - ToolResultTailLines
	ellipsis := foldEllipsis(omitted, hint)
	folded := strings.Join(lines[:ToolResultHeadLines], "\n") +
		ellipsis +
		strings.Join(lines[len(lines)-ToolResultTailLines:], "\n")
	return fmt.Sprintf("📋 %s:\n%s", result.Name, folded),
		lines[ToolResultHeadLines : len(lines)-ToolResultTailLines],
		strings.Trim(ellipsis, "\n")
}

// FormatToolCall formats a tool call for display
func FormatToolCall(call Call) string {
	var inputPreview string
	if len(call.Input) > 100 {
		inputPreview = call.Input[:100] + "..."
	} else {
		inputPreview = call.Input
	}
	return fmt.Sprintf("🔧 %s(%s)", call.Name, strings.TrimSpace(inputPreview))
}
