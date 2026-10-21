// Package permission — M12 权限体验增强。
//
// 设计目标：
// - M12-02 专用对话框：结构化 PermissionDialog API（多选项：允许一次/总是/拒绝/永久拒绝）
// - M12-03 PermissionRequest Hook：允许外部组件（TUI）注册监听并定制呈现
// - M12-04 Shift+Tab 切换：循环切换权限模式（read-only → workspace-write → accept-edits → auto）
// - M12-05 AI 分类器：复用现有 classifier.go 的 AIClassifier，集成到 Policy.Check 流程
//
// 设计原则：
// - 向后兼容：不修改现有 Policy.Check 签名，通过新接口 PermissionDialog 扩展
// - 依赖注入：Hook/Dialog 通过 SetPermissionDialog/SubscribePermissionRequest 注入
// - 跨平台：纯 Go，无 TUI 依赖（TUI 层通过 Hook 接入）
// - CGO_ENABLED=0：仅标准库
//
// 官方源对标：
// - 同类 CLI 的 permission prompts（官方文档，内部参考）
// - 同类 CLI 的 YOLO（放宽）模式（官方文档，内部参考）
// - GitHub 同类 CLI 的 approval（官方文档，内部参考）
package permission

import (
	"context"
	"fmt"
	"sync"
	"time"
)

// === M12-02: PermissionDialog ===

// DialogOption 是权限对话框中的可选选项（M12-02 SNAPSHOT）。
type DialogOption int

const (
	// DialogOptionAllowOnce 允许一次（不写入 memory）。
	DialogOptionAllowOnce DialogOption = iota
	// DialogOptionAllowAlways 总是允许（写入 memory，下次不再询问）。
	DialogOptionAllowAlways
	// DialogOptionDenyOnce 拒绝一次（不写入 denial）。
	DialogOptionDenyOnce
	// DialogOptionDenyAlways 永久拒绝（写入 denial tracker）。
	DialogOptionDenyAlways
	// DialogOptionSwitchMode 切换权限模式（Shift+Tab 触发）。
	DialogOptionSwitchMode
	// DialogOptionCancel 取消操作（等同 DenyOnce 但不记录）。
	DialogOptionCancel
)

// String 返回 DialogOption 的字符串表示。
func (o DialogOption) String() string {
	switch o {
	case DialogOptionAllowOnce:
		return "allow-once"
	case DialogOptionAllowAlways:
		return "allow-always"
	case DialogOptionDenyOnce:
		return "deny-once"
	case DialogOptionDenyAlways:
		return "deny-always"
	case DialogOptionSwitchMode:
		return "switch-mode"
	case DialogOptionCancel:
		return "cancel"
	default:
		return fmt.Sprintf("unknown(%d)", int(o))
	}
}

// DialogRequest 是权限对话框请求（M12-02 SNAPSHOT）。
type DialogRequest struct {
	// ToolName 工具名（如 "bash" / "write_file"）。
	ToolName string
	// Input 工具输入（JSON 字符串或命令文本）。
	Input string
	// RiskLevel 风险等级（"low" / "medium" / "high" / "critical"）。
	RiskLevel string
	// Reason 触发询问的原因（如 "command contains rm -rf" / "write to protected path"）。
	Reason string
	// AvailableOptions 该请求支持的选项（取决于风险等级与策略）。
	AvailableOptions []DialogOption
	// Context 上下文信息（前 N 轮对话摘要，用于 AI 分类）。
	Context []string
}

// DialogResponse 是权限对话框响应（M12-02 SNAPSHOT）。
type DialogResponse struct {
	// Option 用户选择的选项。
	Option DialogOption
	// NewMode 若 Option=SwitchMode，则新权限模式在此字段（否则忽略）。
	NewMode Level
	// Reason 用户提供的理由（可用于审计/学习）。
	Reason string
	// Duration 交互时长（用于性能统计）。
	Duration time.Duration
}

// PermissionDialog 是权限对话框接口（M12-02 SNAPSHOT）。
//
// 实现方约定：
// - TUI 层实现此接口，通过 Policy.SetPermissionDialog 注入
// - 非 TUI 场景（如 CI）可返回 DialogResponse{Option: DialogOptionDenyOnce}
// - 实现应阻塞直到用户响应（同步交互）
// - 实现应支持取消（context.Context）
type PermissionDialog interface {
	// Show 显示权限对话框并等待用户响应。
	Show(ctx context.Context, req DialogRequest) DialogResponse
}

// === M12-03: PermissionRequest Hook ===

// PermissionRequestEvent 是权限请求事件（M12-03 SNAPSHOT）。
//
// 在 Policy.Check 触发询问前广播此事件，外部组件可：
// - 记录审计日志
// - 显示前置提示（如「即将执行危险操作」）
// - 修改请求（如调整 RiskLevel 或 AvailableOptions）
type PermissionRequestEvent struct {
	// Request 原始对话框请求。
	Request DialogRequest
	// Source 触发源（"policy" / "rule" / "classifier"）。
	Source string
	// Timestamp 触发时间。
	Timestamp time.Time
}

// PermissionRequestHook 是权限请求监听器（M12-03 SNAPSHOT）。
//
// 实现：
// - 通过 Policy.SubscribePermissionRequest 注册
// - 多个监听器按注册顺序串行调用
// - 监听器不可修改请求（只读）；如需修改应实现 PermissionDialog
type PermissionRequestHook func(event PermissionRequestEvent)

// === M12-04: Shift+Tab 模式切换 ===

// ModeCycle 是权限模式循环切换器（M12-04 SNAPSHOT）。
//
// 切换顺序（Shift+Tab 循环，阶段 2 收模式后为 2 档）：
// 1. read-only（最严格，仅读）
// 2. workspace-write（默认，写需确认）
// 3. 回到 read-only
//
// 刻意跳过项：LevelDangerFullAccess —— 高风险档必须由用户显式指定
// （/permissions danger-full-access 或配置），不通过 Shift+Tab 进入。
// 原 accept-edits / dont-ask / auto 已收敛：它们的审批语义改由规则引擎承担，
// 不再是会话档，故不参与循环。
type ModeCycle struct {
	mu          sync.Mutex
	current     Level
	cycleLevels []Level
}

// NewModeCycle 创建模式循环切换器，初始模式为 workspace-write。
func NewModeCycle(initial Level) *ModeCycle {
	cycle := []Level{
		LevelReadOnly,
		LevelWorkspaceWrite,
	}
	// 验证初始模式在循环中（danger-full-access 不在循环 → 回退 workspace-write）
	found := false
	for _, l := range cycle {
		if l == initial {
			found = true
			break
		}
	}
	if !found {
		initial = LevelWorkspaceWrite
	}
	return &ModeCycle{
		current:     initial,
		cycleLevels: cycle,
	}
}

// Current 返回当前模式。
func (mc *ModeCycle) Current() Level {
	mc.mu.Lock()
	defer mc.mu.Unlock()
	return mc.current
}

// Next 切换到下一个模式（Shift+Tab），返回切换后的模式。
//
// 循环顺序：read-only → workspace-write → read-only
func (mc *ModeCycle) Next() Level {
	mc.mu.Lock()
	defer mc.mu.Unlock()
	for i, l := range mc.cycleLevels {
		if l == mc.current {
			next := (i + 1) % len(mc.cycleLevels)
			mc.current = mc.cycleLevels[next]
			return mc.current
		}
	}
	// 兜底：若当前模式不在循环中，回到 workspace-write
	mc.current = LevelWorkspaceWrite
	return mc.current
}

// Previous 切换到上一个模式（Shift+Tab 反向），返回切换后的模式。
func (mc *ModeCycle) Previous() Level {
	mc.mu.Lock()
	defer mc.mu.Unlock()
	for i, l := range mc.cycleLevels {
		if l == mc.current {
			prev := (i - 1 + len(mc.cycleLevels)) % len(mc.cycleLevels)
			mc.current = mc.cycleLevels[prev]
			return mc.current
		}
	}
	mc.current = LevelWorkspaceWrite
	return mc.current
}

// Set 直接设置当前模式（如用户通过命令行 /mode accept-edits 切换）。
func (mc *ModeCycle) Set(level Level) {
	mc.mu.Lock()
	defer mc.mu.Unlock()
	mc.current = level
}

// CycleLevels 返回参与循环的模式列表（只读副本）。
func (mc *ModeCycle) CycleLevels() []Level {
	mc.mu.Lock()
	defer mc.mu.Unlock()
	out := make([]Level, len(mc.cycleLevels))
	copy(out, mc.cycleLevels)
	return out
}

// === M12-05: AI 分类器集成（复用 classifier.go） ===

// ClassifyResult 是 AI 分类器集成结果（M12-05 SNAPSHOT）。
//
// 与 classifier.go 的 ClassifierResult 区别：
// - ClassifierResult 是底层 AI 调用结果
// - ClassifyResult 是 Policy 层集成结果（含 memory/denial 决策）
type ClassifyResult struct {
	// Decision 最终决策（Allow/Deny/Uncertain）。
	Decision ClassifierDecision
	// Source 决策源（"allowlist" / "memory" / "ai" / "fail-closed"）。
	Source string
	// Reason 决策理由（用于审计）。
	Reason string
	// DurationMs 分类耗时（毫秒）。
	DurationMs int64
}

// ClassifyActionWithMemory 是带 memory 与 denial 的 AI 分类集成入口（M12-05）。
//
// ⚠️ R5 死代码风险声明（阶段 2 收模式，2026-09-10）：本函数当前**没有生产调用方**
// （仅测试引用）。它服务的是已移除的 LevelAuto 档，不参与 Check/checkModeLocked 的
// 权限决策。保留作为未来「执行后验证 / 审计」的复用底座，详见 classifier.go 顶部声明。
//
// 调用流程：
// 1. 检查 memory：若 Policy.memory.Has(toolName, input) → 直接 Allow（source=memory）
// 2. 检查 denial tracker：若 ShouldFallback() 为 true → 走 fail-closed 返回 Uncertain
// 3. 调用 classifier.Classify：若 classifier 为 nil 则返回 Uncertain
// 4. 根据 classifier 返回的 Decision 做最终决策：
// - Allow → 返回 Allow（调用方可写入 memory）
// - Deny → 返回 Deny（调用方可写入 denial tracker）
// - Uncertain → 走 PermissionDialog 流程（M12-02）
//
// 参数：
// - ctx: 上下文（用于 AI 调用超时控制）
// - classifier: 已配置的 *Classifier（来自 classifier.go，可为 nil）
// - memory: Policy.memory（可为 nil）
// - tracker: Policy.tracker（DenialTracker，可为 nil）
// - action: 待分类的工具调用
//
// 返回：ClassifyResult 含决策/源/理由/耗时
//
// 注：本函数不直接调用 PermissionDialog，Uncertain 时返回 ClassifyResult{Decision: DecisionUncertain}，
// 由调用方（Policy.Check）决定是否弹出对话框。
//
// 兼容性：classifier.Classify 返回 ClassifierResult（非指针，无 error），
// 本函数将其包装为 ClassifyResult（含 memory/denial 决策）。
func ClassifyActionWithMemory(
	ctx context.Context,
	classifier *Classifier,
	memory *Memory,
	tracker *DenialTracker,
	action ClassifierAction,
) ClassifyResult {
	startTime := time.Now()

	// 1. memory 命中直接 Allow
	if memory != nil && memory.Has(action.ToolName, action.ToolInput) {
		return ClassifyResult{
			Decision:   DecisionAllow,
			Source:     "memory",
			Reason:     "previously allowed (memory hit)",
			DurationMs: time.Since(startTime).Milliseconds(),
		}
	}

	// 2. denial tracker 检查（连续失败阈值）
	// 注：DenialTracker 是通用计数器（无 per-tool key），用 ShouldFallback 判断是否进入降级
	if tracker != nil && tracker.ShouldFallback() {
		return ClassifyResult{
			Decision:   DecisionUncertain,
			Source:     "denial-fallback",
			Reason:     "denial tracker indicates consecutive failures",
			DurationMs: time.Since(startTime).Milliseconds(),
		}
	}

	// 3. classifier 调用
	if classifier == nil {
		// 无 classifier：fail-closed（默认 Uncertain，由调用方决定是否走 Dialog）
		return ClassifyResult{
			Decision:   DecisionUncertain,
			Source:     "no-classifier",
			Reason:     "classifier not configured",
			DurationMs: time.Since(startTime).Milliseconds(),
		}
	}

	// classifier.Classify 返回 ClassifierResult（值类型，无 error）
	result := classifier.Classify(ctx, action)

	// 4. 根据 classifier 返回决策
	switch result.Decision {
	case DecisionAllow:
		return ClassifyResult{
			Decision:   DecisionAllow,
			Source:     result.Source,
			Reason:     result.Reason,
			DurationMs: time.Since(startTime).Milliseconds(),
		}
	case DecisionDeny:
		return ClassifyResult{
			Decision:   DecisionDeny,
			Source:     result.Source,
			Reason:     result.Reason,
			DurationMs: time.Since(startTime).Milliseconds(),
		}
	default:
		return ClassifyResult{
			Decision:   DecisionUncertain,
			Source:     result.Source,
			Reason:     result.Reason,
			DurationMs: time.Since(startTime).Milliseconds(),
		}
	}
}

// === Policy 集成：SetPermissionDialog + SubscribePermissionRequest ===

// SetPermissionDialog 注入权限对话框实现（M12-02 SNAPSHOT）。
//
// 注入后，Policy.Check 在需要询问时优先调用 dialog.Show；
// 未注入时退化为现有行为（返回 needConfirm=true 由调用方处理）。
//
// 并发安全：写入时持锁；读取时（Check 中）持读锁。
func (p *Policy) SetPermissionDialog(dialog PermissionDialog) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.dialog = dialog
}

// GetPermissionDialog 返回当前注入的权限对话框（可为 nil）。
func (p *Policy) GetPermissionDialog() PermissionDialog {
	p.mu.RLock()
	defer p.mu.RUnlock()
	return p.dialog
}

// SubscribePermissionRequest 注册权限请求监听器（M12-03 SNAPSHOT）。
//
// 监听器按注册顺序串行调用；事件为只读（不可修改请求）。
// 返回 unsubscribe 函数，调用后取消订阅。
func (p *Policy) SubscribePermissionRequest(hook PermissionRequestHook) func() {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.hooks = append(p.hooks, hook)
	idx := len(p.hooks) - 1
	return func() {
		p.mu.Lock()
		defer p.mu.Unlock()
		if idx < len(p.hooks) {
			p.hooks[idx] = nil // 标记为已取消订阅
		}
	}
}

// emitPermissionRequest 广播权限请求事件给所有监听器（M12-03）。
//
// 监听器调用失败不影响主流程（best-effort 通知）。
func (p *Policy) emitPermissionRequest(event PermissionRequestEvent) {
	p.mu.RLock()
	hooks := make([]PermissionRequestHook, len(p.hooks))
	copy(hooks, p.hooks)
	p.mu.RUnlock()

	for _, hook := range hooks {
		if hook == nil {
			continue
		}
		// best-effort：监听器 panic 不影响主流程
		func() {
			defer func() { _ = recover() }()
			hook(event)
		}()
	}
}

// SetModeCycle 注入模式循环切换器（M12-04 SNAPSHOT）。
//
// 注入后，PermissionDialog 的 DialogOptionSwitchMode 选项可通过 ModeCycle.Next() 切换。
func (p *Policy) SetModeCycle(mc *ModeCycle) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.modeCycle = mc
}

// GetModeCycle 返回当前注入的模式循环切换器（可为 nil）。
func (p *Policy) GetModeCycle() *ModeCycle {
	p.mu.RLock()
	defer p.mu.RUnlock()
	return p.modeCycle
}

// SwitchModeNext 切换到下一个权限模式（M12-04）。
//
// 调用前提：已通过 SetModeCycle 注入切换器。
// 返回切换后的模式；未注入切换器时返回当前模式不变。
func (p *Policy) SwitchModeNext() Level {
	p.mu.RLock()
	mc := p.modeCycle
	mode := p.mode
	p.mu.RUnlock()
	if mc == nil {
		return mode
	}
	newMode := mc.Next()
	p.mu.Lock()
	p.mode = newMode
	p.mu.Unlock()
	return newMode
}

// === RiskLevel 评估（供 M12-02 DialogRequest.RiskLevel 使用） ===

// AssessRiskLevel 评估工具调用的风险等级（M12-02 SNAPSHOT）。
//
// 评估逻辑：
// - **命令载体**（`permission.IsCommandBearingTool` = {bash, monitor}，唯一声明）：
// 调用 AnalyzeBashCommand → Dangerous=high, Destructive=high, ReadOnly=low
// - write_file/edit_file：检查路径是否受保护 → high（受保护）/ medium（普通写）
// - read 操作：low
// - 其他：medium
//
// ★ P-002（2026-09-22 反缝补扫描）：命令载体那一支修复前是手抄名对
// `case "bash", "PowerShell":` ⇒ `Monitor` 落 default，风险等级显示 **medium 而非 high**
// （用户看到的风险比实际低一档 → 会影响 AvailableOptionsForRisk 给出的选项集），
// 而 `PowerShell` 是个从未注册的工具。判据现由 SSOT 派生。
//
// 返回字符串："low" / "medium" / "high" / "critical"
func AssessRiskLevel(toolName, input string) string {
	if IsCommandBearingTool(toolName) {
		cmd := extractCommand(input)
		risk := AnalyzeBashCommand(cmd)
		switch risk {
		case BashRiskDangerous, BashRiskDestructive:
			return "high"
		case BashRiskReadOnly:
			return "low"
		default:
			return "medium"
		}
	}
	switch toolName {
	case "write_file", "edit_file", "NotebookEdit":
		filePath := ExtractFilePath(input)
		if filePath != "" {
			result := CheckPathSafety(filePath, "")
			if result != nil && result.IsProtected {
				return "high"
			}
		}
		return "medium"
	case "read_file", "grep_search", "glob_search", "list_files", "memory_search":
		return "low"
	default:
		return "medium"
	}
}

// AvailableOptionsForRisk 根据风险等级返回可用的对话框选项（M12-02）。
//
// 策略：
// - low: AllowOnce / AllowAlways / DenyOnce
// - medium: AllowOnce / AllowAlways / DenyOnce / DenyAlways / SwitchMode
// - high: AllowOnce / DenyOnce / DenyAlways / SwitchMode（不允许 AllowAlways）
// - critical: DenyOnce / DenyAlways / SwitchMode（强制拒绝，仅可切换模式）
func AvailableOptionsForRisk(riskLevel string) []DialogOption {
	switch riskLevel {
	case "low":
		return []DialogOption{DialogOptionAllowOnce, DialogOptionAllowAlways, DialogOptionDenyOnce}
	case "medium":
		return []DialogOption{
			DialogOptionAllowOnce, DialogOptionAllowAlways,
			DialogOptionDenyOnce, DialogOptionDenyAlways,
			DialogOptionSwitchMode,
		}
	case "high":
		return []DialogOption{
			DialogOptionAllowOnce,
			DialogOptionDenyOnce, DialogOptionDenyAlways,
			DialogOptionSwitchMode,
		}
	case "critical":
		return []DialogOption{
			DialogOptionDenyOnce, DialogOptionDenyAlways,
			DialogOptionSwitchMode,
		}
	default:
		return []DialogOption{DialogOptionAllowOnce, DialogOptionDenyOnce}
	}
}
