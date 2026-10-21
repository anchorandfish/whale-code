package permission

import (
	"sort"
	"strings"

	"github.com/anchorandfish/whale-code/internal/security/cmdrules"
)

// capability.go — 工具「能力分桶」的单一真相源（R2，对应 S4 方案 L3.1）。
//
// 为什么需要它：deny/allow 规则的匹配单位是**工具名字符串**，而"用户意图"是按
// **能力**表述的 —— 同一个能力被拆成多个工具名。此前只有 `Edit(*)` 一个家族特例，
// 读取侧没有对称实现 ⇒ 配 `read_file(*)` 之后 `grep_search` 仍能静默读到同一文件内容
// （2026-09-13 生产路径实测，见 internal/tools/deny_rule_bypass_test.go）。
//
// 本文件把"家族特例"改成"通用机制"：**能力 → 工具集合**只有一个来源，
// `Edit(*)` 从特例降为该机制的一个实例。
//
// 对标同类 CLI 的能力分桶
// agents/ToolSelector.tsx:42-74 的 ToolBuckets{READ_ONLY, EDIT, EXECUTION, MCP, OTHER}。
// 本项目用 6 类，且与 同类 CLI 有两处**有意分歧**：
// - `bash` 归 `Exec` 不归 `Read`（同类 CLI 的 BashTool 同样归 EXECUTION）——
// "只读"应由「不授予 Exec/Edit」表达，而不是「deny 掉读工具名」；
// - `Network`/`Mcp` 独立成桶（同类 CLI 把 WebFetch/WebSearch/ListMcpResources/
// ReadMcpResource 放进 READ_ONLY）—— 它们是**外部信任域**，与"本机文件"不同，
// `Read(*)` 不该连带禁掉联网取资料。
//
// ⚠️ 判据是「工具的**效果**」，不是名字读起来像什么。两个按名字会分错的例子：
// - `Monitor`：描述是"Start background process monitoring"，但入参是 `command`
// （`tail -f log.txt` / `npm run dev`）⇒ 它**在本机执行命令**，归 `Exec`。
// 若归 `Meta`，`Exec(*)` 会漏掉它。
// - `export_session`：名字像只读，实际**写 JSON 文件到指定路径** ⇒ 归 `Edit`。
// （其注册声明是 LevelReadOnly，与实践不符 —— 已记入协调文档 §8.4-①，交 L1 修。）

// Capability 是工具能力的分类标签。
type Capability string

const (
	// CapRead 向模型披露**本机既有**内容/结构（文件、仓库、代码符号）。
	CapRead Capability = "Read"
	// CapEdit 新建/修改/删除**持久化本机状态**（文件、仓库、配置、会话导出）。
	CapEdit Capability = "Edit"
	// CapExec 在本机跑任意命令/进程。只读角色不得持有。
	CapExec Capability = "Exec"
	// CapNetwork 访问外部网络（信任域在本机之外）。
	CapNetwork Capability = "Network"
	// CapMcp 调用外部 MCP 服务（信任域在本机之外，任意 RPC）。
	CapMcp Capability = "Mcp"
	// CapMeta 纯会话/控制面：无本机披露、无持久化落盘。
	CapMeta Capability = "Meta"
)

// capabilityNames 是全部能力的稳定枚举顺序（供守卫与文档遍历）。
var capabilityNames = []Capability{CapRead, CapEdit, CapExec, CapNetwork, CapMcp, CapMeta}

// CapabilityNames 返回全部能力（顺序稳定，便于守卫与快照比对）。
func CapabilityNames() []Capability {
	out := make([]Capability, len(capabilityNames))
	copy(out, capabilityNames)
	return out
}

// toolCapabilities 是**唯一真相源**：工具名（小写）→ 它具备的能力。
//
// ★ 关键约束：**没有默认桶**。未登记的工具不在本表中，`CapabilitiesOf` 返回 nil，
// 由 `TestGuard_EveryRegisteredToolHasCapability`（internal/tools）抓出来。
// 若给"未知工具"一个兜底桶（例如 `Meta`），那条守卫将永远为绿、完全无判别力。
//
// 覆盖度与 `registerBuiltinTools`（internal/tools/tools.go）一一对应：57 个内置工具
// （2026-10-09 /059/060 新增 TaskOutput / MonitorTail / ForkStatus 三条，54 → 57）。
var toolCapabilities = map[string][]Capability{
	// ---- Read（16）披露本机既有内容/结构 ----
	"read_file":          {CapRead},
	"glob_search":        {CapRead},
	"grep_search":        {CapRead},
	"memory_search":      {CapRead}, // P4：检索 ~/.whale 记忆（纯读，LevelReadOnly 声明一致）
	"git_diff":           {CapRead},
	"git_status":         {CapRead},
	"git_log":            {CapRead},
	"lspdefinition":      {CapRead},
	"lspreferences":      {CapRead},
	"lspdiagnostics":     {CapRead},
	"lsphover":           {CapRead},
	"lspcompletion":      {CapRead},
	"lspimplementation":  {CapRead},
	"lsptypedefinition":  {CapRead},
	"lspdocumentsymbols": {CapRead},
	"lspcodeactions":     {CapRead}, // 声明 WW 但只返回建议、不落盘（协调文档 §9-Q3）

	// ---- Edit（12）新建/修改/删除持久化本机状态 ----
	"write_file": {CapEdit},
	"edit_file":  {CapEdit},
	// G5：把 unified diff 写回文件（含新建与删除）⇒ 与 write_file/edit_file 同桶，
	// 因此 `Edit(*)` deny 规则会一并覆盖它，只读角色面不会拿到它。
	"apply_unified_diff": {CapEdit},
	"notebookedit":       {CapEdit},
	"git_add":            {CapEdit},
	"git_commit":         {CapEdit},
	"git_branch":         {CapEdit},
	"lsprename":          {CapEdit},
	"export_session":     {CapEdit}, // ⚠️ 写 JSON 文件；注册声明 LevelReadOnly，见 §8.4-①
	"enter_worktree":     {CapEdit},
	"exit_worktree":      {CapEdit}, // action=remove 会删目录与分支
	"config_set":         {CapEdit}, // 可改 permission_level，属提权面

	// ---- Exec（2）在本机跑任意命令 ----
	"bash":    {CapExec},
	"monitor": {CapExec}, // 入参是 command，会真的执行

	// ---- Network（2）外部网络 ----
	"webfetch":  {CapNetwork},
	"websearch": {CapNetwork},

	// ---- Mcp（6）外部 MCP 服务 ----
	"mcplisttools":     {CapMcp},
	"mcpcalltool":      {CapMcp}, // 任意 RPC，能力上界最高
	"mcplistprompts":   {CapMcp},
	"mcpgetprompt":     {CapMcp},
	"mcplistresources": {CapMcp},
	"mcpreadresource":  {CapMcp},

	// ---- Meta（20）会话/控制面 ----
	"agent":      {CapMeta}, // 派生子代理（编排）；是否放行由 L2「委派不扩权」管
	"task":       {CapMeta},
	"taskcreate": {CapMeta},
	"tasklist":   {CapMeta},
	"taskstop":   {CapMeta},
	// ★ /059/060：三条**控制面读回**工具（拉）。
	//
	// 为什么是 Meta 而不是 Read（判据 = 本文件头部对 CapRead 的定义）：
	// CapRead = "披露**本机既有**内容/结构（文件、仓库、代码符号）"；
	// 这三条读的是**会话/控制面状态**（后台任务记录、fork 任务记录、监控记录），
	// 与既有 `tasklist` 同性质 ⇒ 同桶。
	// 反面后果（若误归 CapRead）：① `deny ["Read(*)"]` 会连带禁掉它们；
	// ② tui 的「原地打转」进展检测（agent_guard.go:156 readOnlyToolTarget，
	// 真相源就是 CapRead）会把"模型轮询同一个 task_id"误判成无进展 ⇒ 触发停轮干预。
	"taskoutput":        {CapMeta}, // 读后台任务已采集输出
	"monitortail":       {CapMeta}, // 读监控进程尾部输出
	"forkstatus":        {CapMeta}, // 读 fork 子 agent 的 status/result
	"todowrite":         {CapMeta}, // 写 agent 自身 todo 状态，非用户数据（§9-Q2）
	"askuser":           {CapMeta},
	"user_choice":       {CapMeta},
	"skill":             {CapMeta}, // ⚠️ 可注入 AllowedTools（权限面），风险由 L2 处理
	"sleep":             {CapMeta},
	"structured_output": {CapMeta},
	"toolsearch":        {CapMeta},
	"config_get":        {CapMeta},
	"sendmessage":       {CapMeta},
	"cronlist":          {CapMeta},
	"crondelete":        {CapMeta},
	"schedule_cron":     {CapMeta},
}

// CapabilitiesOf 返回工具具备的能力。
//
// ★ 未登记的工具返回 **nil**，**不是** `[]Capability{CapMeta}` —— 见 toolCapabilities
// 的约束说明。调用方（deny 匹配）对 nil 的处理见 rules.go。
func CapabilitiesOf(tool string) []Capability {
	caps, ok := toolCapabilities[strings.ToLower(tool)]
	if !ok {
		return nil
	}
	out := make([]Capability, len(caps))
	copy(out, caps)
	return out
}

// capabilityFamily 是 toolCapabilities 的**派生视图**：能力小写名 → 工具名集合。
//
// 派生而非并列维护：两张表各自手写必然漂移（本仓库已有 4 份工具清单漂移的教训）。
func capabilityFamily() map[string]map[string]bool {
	fam := make(map[string]map[string]bool, len(capabilityNames))
	for _, c := range capabilityNames {
		fam[strings.ToLower(string(c))] = map[string]bool{}
	}
	for tool, caps := range toolCapabilities {
		for _, c := range caps {
			if set, ok := fam[strings.ToLower(string(c))]; ok {
				set[tool] = true
			}
		}
	}
	return fam
}

// capabilityFamilies 是 capabilityFamily() 的一次性缓存（只读，勿改）。
var capabilityFamilies = capabilityFamily()

// capabilityCovers 判断「规则名 ruleName」作为**能力别名**是否覆盖 toolName。
//
// ok=false 表示 ruleName 不是能力别名 —— 调用方须回落到工具名/内容语义，
// 不要把它当成"未命中"（那会让 `bash(rm:*)` 这类具体规则失效）。
// 入参须已小写（调用方 matchesTool 已做）。
func capabilityCovers(ruleName, toolName string) (covered bool, ok bool) {
	set, isCapability := capabilityFamilies[ruleName]
	if !isCapability {
		return false, false
	}
	return set[toolName], true
}

// capabilityCoversCall 是 capabilityCovers 的**调用级**版本：在静态分桶之上叠加
// 「效果视图」——回答「**这一次调用**是否落入该能力别名」。
//
// ★ （2026-10-06 用户拍板 ①）：为什么需要它 ——
// `Read(*)` 的成员判定此前只有「静态工具名 → 能力桶」一条路，而 `bash` 静态登记为
// `Exec` ⇒ 永不入读家族 ⇒ 即使用户写了 `deny ["Read(*)"]`，`bash cat SECRET.md`
// 仍能读（把"bash 一律弹框"这个**偶然闸门**拆掉后，该路径还从"有人工确认"
// 变成**静默**，缺口变宽）。
//
// 根因是**匹配范围**，不是优先级：deny 的裁决点（pipeline.go 的 rule_gate）本就在
// 所有免问出口之前，只要匹配得上就天然压过它们。故修复 = 让"效果是只读"的调用
// 进入读家族的匹配视图。
//
// ★ 唯一语义来源：只读效果由 `cmdrules.AllSegmentsReadOnly` 判定（**全段**口径）。
// 不复用 `MatchReadOnly`（任一段命中即只读）—— 那是**放宽**方向，用它做 deny 会漏拦
// （`weirdcmd && echo hi` 会被当只读）。也不剥 wrapper/赋值：`sudo cat X` 判否
// （fail-closed，已知残余见 回执）。
//
// ★ 护栏（**防提权**，见 read_effect_view_test.go 契约 3）：
// 效果视图**只作用于规则匹配面**，`toolCapabilities` 一字不改 ——
// 若图省事把 bash 塞进 `CapRead` 桶，只读子代理（tui/agent_guard.go 的
// `hasReadCapability`）就会**拿到 bash** ⇒ 提权，比原缺口更糟。
// 同理效果视图**只对 CapRead 生效**：`Edit(*)` / `Exec(*)` 等不做"按效果"扩张。
//
// 入参须已小写（调用方 matchesTool 已做）。ok 语义与 capabilityCovers 一致：
// false 表示 ruleName 不是能力别名，调用方须回落工具名/内容语义。
func capabilityCoversCall(ruleName, toolName, input string) (covered bool, ok bool) {
	covered, ok = capabilityCovers(ruleName, toolName)
	if !ok || covered {
		return covered, ok
	}
	if ruleName == strings.ToLower(string(CapRead)) && readEffectView(toolName, input) {
		return true, true
	}
	return false, true
}

// readEffectView 报告「这一次调用的效果是否为只读」——仅对**命令载体**有意义。
//
// fail-closed：非命令载体返回 false；认不出的命令（`sh -c 'cat X'` / `xargs cat` /
// `sudo cat`）由 AllSegmentsReadOnly 判否 ⇒ 落回 Exec 语义（不减免，方向为多问一次）。
func readEffectView(toolName, input string) bool {
	if !IsCommandBearingTool(toolName) {
		return false
	}
	return cmdrules.AllSegmentsReadOnly(ExtractCommand(input))
}

// CapabilityToolsOf 返回某能力下的全部工具名（升序），供守卫与文档生成。
func CapabilityToolsOf(c Capability) []string {
	set := capabilityFamilies[strings.ToLower(string(c))]
	out := make([]string, 0, len(set))
	for t := range set {
		out = append(out, t)
	}
	sort.Strings(out)
	return out
}

// ============================================================================
// 编排族（orchestration）—— 审批免问的委派/编排工具
// ============================================================================

// orchestrationTools 是「编排族」的**单一真相源**。
//
// 定义：这些工具**自身不产生任何本机副作用** —— 它们的可观察效果 100% 由被它们
// 触发的**下游调用**承担，而下游调用各自会独立经过工具管线（含权限阶段与工作区
// 边界校验）。因此"启动编排"这一动作本身没有可供审批的对象。
//
// 用途：审批层（internal/tools/pipeline.go 的 PermissionStage）据此把「启动编排」
// 从『必须确认』降为『不必确认』。
//
// 为什么需要它（第一性原理）：审批要拦的是**沙箱与边界管不住的副作用**，不是
// 「这个工具声明了多高的权限档位」。Task/Agent/Skill/TaskStop 的声明档位都是
// WorkspaceWrite（因为被委派出去的角色上限确实含写），但「需要写权限」是**能力
// 声明**，它与「这次调用是否危险」是正交的两件事。把前者当成后者，就会给每一个
// 编排动作弹一次**没有信息量**的确认框：用户在框里看不到子 agent 要做什么，
// 编排动作弹一次**没有信息量**的确认框：用户在框里看不到子 agent 要做什么，
// 而子 agent 真正做的每个敏感动作**还会各自再弹一次**。
//
// 对标业内既有实现（同类 CLI，本仓可读）：
// - `core/src/tools/handlers/multi_agents_spec.rs`：spawn_agent 的规格里
// `approval` **出现 0 次** —— 启动子 agent 本身不触发授权；
// - `core/src/codex_delegate.rs:72`：子 agent 的审批「handled via parent_session
// and are **not surfaced**」—— 不额外弹窗；
// - `core/src/tools/handlers/multi_agents.rs:5`：子 agent 继承父的
// `approval policy, sandbox` —— 委派不扩权；
// - `core/src/safety.rs:32 assess_patch_safety`：只对**逃逸可写根**才 `AskUser`。
//
// 入选判据（三条同时成立，缺一不可）：
// 1. 能力桶恰为 `CapMeta` —— 无本机披露、无持久化落盘（由 toolCapabilities 保证；
// 守卫 capability_orchestration_guard_test.go 钉住这一点，防止将来有人把
// bash / write_file 这类有副作用的工具混进来）；
// 2. **不**变更持久化控制面 ⇒ **排除** `crondelete` / `schedule_cron`：
// 它们改的是持久任务表，属控制面写入，保留审批；
// 3. **不**在本机执行命令 ⇒ **排除** `taskcreate`：其描述是
// "Executes a command in the background"，与 `Monitor` 同类 ——
// 又一个「按名字会分错」的工具（见本文件头部的同类警告）。
//
// ★ 免问**不扩权**（安全论证，逐条已核）：
// - Task / Agent → 子 agent 复用**同一个** Executor/Pipeline（agent_task.go:474），
// 其每次工具调用强制走完整 7 阶段（agent.go:992 executeToolCall →
// ExecuteWithPipeline），NeedConfirm 经 AgentConfirmFn 桥接到真实 TUI 确认框
// （tui/agent_confirm.go），无通道时 fail-closed 拒绝；
// - Skill → 信任门独立把关（executor.go:286 EnableTrustGate，project 来源直接拒绝），
// 且 B2 已删除其 AllowedTools 自提权注入（web.go:845）；
// - TaskStop → 只能终止我们自己经 TaskCreate 起的后台任务。
// ⇒ 子 agent 想逃逸工作区或跑危险命令，照样被拦下弹窗。
var orchestrationTools = map[string]bool{
	"task":     true, // 派生子 agent（经 AgentTaskMgr.Spawn）
	"agent":    true, // 派生子 agent（角色由 agent_type 声明）
	"taskstop": true, // 终止后台任务
	// tasklist 声明为 LevelReadOnly，在 ww 档本就免问；纳入是为保持「任务族」
	// 语义完整（同族的 create/stop 都被豁免，唯独 list 靠另一条路径免问会难以解释）。
	"tasklist": true,
	"skill":    true, // 加载技能（技能内的工具调用各自过管线）
}

// IsOrchestrationTool 报告该工具是否属于「编排族」（判据见 orchestrationTools）。
//
// 大小写与首尾空白不敏感（与 CapabilitiesOf 的规范化一致）。
//
// **未登记的工具一律返回 false**（fail-closed）：不认识的工具保持原有确认行为，
// 绝不因为"名字读起来像编排"就被免问。
func IsOrchestrationTool(tool string) bool {
	return orchestrationTools[strings.ToLower(strings.TrimSpace(tool))]
}

// OrchestrationTools 返回编排族的全部工具名（升序），供守卫与文档遍历。
func OrchestrationTools() []string {
	out := make([]string, 0, len(orchestrationTools))
	for t := range orchestrationTools {
		out = append(out, t)
	}
	sort.Strings(out)
	return out
}

// ============================================================================
// 沙箱边界内的工具（sandbox-bounded）—— 审批免问的第二类
// ============================================================================

// sandboxBoundedTools 是「可观察效果**全部**落在 OS 沙箱边界内」的工具集合（单一真相源）。
//
// 判据（两条都满足才算）：
// 1. 本机效果**只有「跑一个进程」这一条通道** —— 该进程由 OS 沙箱
// （darwin seatbelt / linux namespaces+Landlock）包住；
// 2. **没有**第二条绕过沙箱的副作用通道（进程内的持久化写、直连网络等）。
//
// ★ 为什么**不复用** `CapabilitiesOf()` / `CapExec`（尽管今天恰好同集合）：
// 两者回答的是**不同的问题**。
// - `CapExec` 回答「这个工具**有没有**在本机执行的能力」—— 用于 deny 规则匹配
// （`Exec(*)` 该覆盖谁）；
// - 本表回答「**这一次调用**是否已经被沙箱兜住」—— 用于审批免问。
//
// 反例说明判据确实不同：`TaskCreate` 会在本机跑后台命令（像 Exec），
// **同时**又把任务状态写进 `~/.whale/`（进程内、沙箱管不到）⇒ 它**不得**进本表。
// 若把两者合并，将来有人为修 `Exec(*)` 的覆盖面把 `taskcreate` 补进 `CapExec` 时，
// 会**顺带静默放宽审批** —— 正是本仓反复出现的「影子判据漂移」。
// ⇒ 因此：独立命名判据 + **包含关系守卫**（本表必须 ⊆ `CapExec`，见
// `capability_sandbox_bounded_guard_test.go`）。
//
// 用途：审批层（internal/tools/pipeline.go 的 PermissionStage）据此在
// **OS 沙箱真的在强制**时把进程类命令从『必须确认』降为『不必确认』。
// 该豁免**必须**与「沙箱是否真在强制」合取使用（见 PermissionStage 出口 (c)）——
// 单凭本表免问会在装不上沙箱的机器上变成"既无边界也不问"。
var sandboxBoundedTools = map[string]bool{
	"bash":    true, // 效果 = 一个被沙箱包住的 shell 进程
	"monitor": true, // 同上：入参是 command，真的执行（见 toolCapabilities 注）
}

// IsSandboxBoundedTool 报告该工具的可观察效果是否**全部**落在 OS 沙箱边界内。
//
// 大小写与首尾空白不敏感（与 CapabilitiesOf 的规范化一致）。
//
// **未登记的工具一律返回 false**（fail-closed）：不认识的工具保持原有确认行为。
func IsSandboxBoundedTool(tool string) bool {
	return sandboxBoundedTools[strings.ToLower(strings.TrimSpace(tool))]
}

// SandboxBoundedTools 返回本集合的全部工具名（升序），供守卫与文档遍历。
func SandboxBoundedTools() []string {
	out := make([]string, 0, len(sandboxBoundedTools))
	for t := range sandboxBoundedTools {
		out = append(out, t)
	}
	sort.Strings(out)
	return out
}

// ============================================================================
// 命令载体（command-bearing）—— 「这次调用的入参里有没有一条会被执行的命令」
// ============================================================================

// IsCommandBearingTool 报告该工具是否是「命令载体」：入参含一条**会真的在本机执行**的
// 命令串（`command` 字段），因而「这条命令危不危险 / 破不破坏」这一维对它是有意义的。
//
// 判据由 toolCapabilities 的 `CapExec` 桶**派生**（不另立名单）—— 与 capabilityFamily
// 同一理由：本仓已有 4 份工具清单互相漂移的教训，凡能派生的一律不手抄。
//
// ★ 为什么**不**复用 `IsSandboxBoundedTool`（尽管今天两者恰好同集合）：
// 两者回答的是**不同的问题**，而且**漂移方向相反**（与 sandboxBoundedTools 的注释同一理由）：
// - `IsSandboxBoundedTool` 回答「**能不能免问**」——集合**越窄越安全**；
// - 本判据回答「**要不要按危险/破坏性命令去拦**」——集合**越宽越安全**。
//
// 合并的后果是单向的：将来若出现一个「是命令载体、但效果不只在沙箱内」的工具
// （`taskcreate` 就是这种候选：既跑命令、又把任务状态写进 `~/.whale/`），合并后它的
// 危险命令会因为「不在 sandbox-bounded 表里」而被**漏拦** —— 那正是 P-002/★S-1
// （Monitor 绕过危险档）的同形复发。⇒ 需要"豁免"语义处用 `IsSandboxBoundedTool`，
// 需要"拦"语义处用本函数。
//
// ★ 两条派生关系由守卫钉住（capability_command_bearing_guard_test.go）：
//
//	① 本判据 == 具备 CapExec 的工具集（防手抄回潮）；
//	② `IsSandboxBoundedTool(x) ⇒ IsCommandBearingTool(x)` —— **被豁免的必被拦**：
//
// 免问面永远不大于拦截面。这条不变量就是 ★S-1 的结构性预防（原缺陷正是
// 「豁免出口认得 monitor，而危险档判定不认得」）。
//
// 大小写与首尾空白不敏感（与 CapabilitiesOf / IsSandboxBoundedTool 的规范化一致）。
// **未登记的工具一律返回 false**（fail-closed）：不认识的工具按各档原有行为处理。
func IsCommandBearingTool(tool string) bool {
	for _, c := range CapabilitiesOf(strings.TrimSpace(tool)) {
		if c == CapExec {
			return true
		}
	}
	return false
}

// CommandBearingTools 返回命令载体集合的全部工具名（升序），供守卫与文档遍历。
// 派生自 `CapabilityToolsOf(CapExec)`：Exec 桶增删成员时本函数自动跟随。
func CommandBearingTools() []string {
	return CapabilityToolsOf(CapExec)
}
