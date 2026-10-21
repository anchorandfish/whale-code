// Package security 提供命令安全校验的统一入口（H2 黑名单安全模型统一收口）。
//
// 本包是命令校验的唯一入口（02 §2.3.1 契约），hook/plugin/monitor/N10/N12
// 必须委托 security.CheckCommand 进行命令安全校验，不得自实现 strings.Contains 黑名单。
//
// 校验链路（三层 + 预处理）：
// - 预处理：Unicode NFKC 归一化 + 空格压缩，抵御全角同形字/多空格绕过
// - Layer 1：validator.Validator.Run 20 项检查（命令注入/混淆/危险语法）
// - Layer 2：sandbox.Sandbox.Validate 6 类拦截（黑名单/命令替换/IFS/网络/路径越界）
// - Layer 3：permission.AnalyzeBashCommand 风险分级（Dangerous Deny / Destructive Ask / ReadOnly Allow）
//
// 校验失败 wrap N6 的 ErrSecurityBlocked / ErrSecuritySandbox（02 §2.3.2）。
// 预留 Redactor 接口供 H3 注入密钥脱敏实现。
//
// 族A（G0 · 2026-09-14）：本门是**唯一门**。判定链上任一环的能力缺失
// （sandbox 未启用 / 依赖未注入）都不得静默放行（L0 禁止），必须作为
// 结构化 Deficiency 随 Decision 一并上抛（族A T1：降级到下一强制点 + 告警）。
// 机器判据是 Deficiency.Tier/Layer，**不得**由 Reason 散文反推（R4 收口同源）。
package security

import (
	"fmt"
	"log"
	"strings"
	"sync"

	aierrors "github.com/anchorandfish/whale-code/internal/ai"
	"github.com/anchorandfish/whale-code/internal/permission"
	"github.com/anchorandfish/whale-code/internal/sandbox"
	"github.com/anchorandfish/whale-code/internal/sandbox/validator"
	"github.com/anchorandfish/whale-code/internal/security/failtier"
	"github.com/anchorandfish/whale-code/internal/util"
	"golang.org/x/text/unicode/norm"
)

// Deficiency.Layer 的取值（唯一门的判定链环节名）。
// 供调用方/审计做机器判定；新增环节必须同时在此登记。
const (
	// DeficiencyLayerValidatorMissing Layer 1（静态检查）的能力缺失（A1 家族）。
	DeficiencyLayerValidatorMissing = "security.L1-validator"
	// DeficiencyLayerSandboxMissing Layer 2（沙箱应用层）的依赖未注入（A1 家族，原为 nil panic）。
	DeficiencyLayerSandboxMissing = "security.L2-sandbox-missing"
	// DeficiencyLayerSandboxDisabled Layer 2 的沙箱未启用（A1：生产默认姿态）。
	DeficiencyLayerSandboxDisabled = "security.L2-sandbox-disabled"
)

// sandboxDisabledWarnOnce 确保 sandbox 未启用警告仅输出一次（B4-P1-1 修复）。
// 非 TUI 路径（plain/bridge/CI/daemon）未调用 SetEnabled(true)，Layer 2 完全失效，
// 此处仅警告不阻断，避免影响已有行为；生产路径应通过 lifecycle.go 显式启用。
var sandboxDisabledWarnOnce sync.Once

// DecidedBy 的取值 —— **哪一层**给出了这次非 Allow 的裁决（结构化事实）。
//
// ★ 为什么要有它（P-014 · 2026-09-22）：此前 `Action` 把三层揉成一个值，
// 调用方**无法**区分「命令本身不可执行」（L1/L2）与「危险档须强制确认」（L3）。
// 而这两者的处置权**不同**：前者与"要不要问"无关，任何调用方都应拒绝；
// 后者的处置权归**管线**（`DangerousGuardStage` 强制确认，用户批准后 `Approved=true` 重跑即放行）。
// 揉在一起的结果就是 `monitor.Manager.Start` 把 L3 的危险档/破坏性档一并当成硬拒绝（本缺陷）。
//
// R4：本字段是**结构化出口**；调用方**不得**改由 `Reason` 散文反推是哪一层。
//
// ★ 命名空间（2026-09-22 复核时纠正）：取值一律用 `layer:*` 前缀，**不**复用
// `Deficiency.Layer` 的 `security.L1-*` 形状。理由不是审美 —— 两个词表此前**字面撞车**：
//
//	DeficiencyLayerValidatorMissing == "security.L1-validator" == DecidedByValidator （完全相同）
//	DeficiencyLayerSandboxMissing == "security.L2-sandbox-missing"
//
// ↑ 以 DecidedBySandbox "security.L2-sandbox" 为前缀
//
// 一个是「判定链哪一环能力缺失」（降级信号），一个是「哪一层给出的裁决」（裁决归属）——
// **语义不同却共用一个字符串**，任何 `HasPrefix`/`Contains` 式匹配都会跨词表误命中
// （本批的 `monitor` 判别器就一度写成 `strings.HasPrefix(d.DecidedBy, "security.L1")`，
// 正是这种被迫的前缀匹配）。⇒ 分开命名空间，让"混用"在**字面上不可能**。
const (
	// DecidedByInput Layer 0（预处理）：空命令。
	DecidedByInput = "layer:input"
	// DecidedByValidator Layer 1：命令注入/混淆/危险语法静态检查（validator 20 项）。
	DecidedByValidator = "layer:validator"
	// DecidedBySandbox Layer 2：沙箱应用层白/黑名单/命令替换/IFS/路径越界。
	DecidedBySandbox = "layer:sandbox"
	// DecidedByNetwork Layer 2.5：网络访问静态检测（P1-2）。
	DecidedByNetwork = "layer:network"
	// DecidedByPermission Layer 3：风险分级（危险档 Deny / 破坏性档 Ask）。
	// ★ 与上面几条不同：危险档是「**强制确认**（永不免问）」而非「命令本身不可执行」，
	// 其处置权归管线 ⇒ `Decision.HardBlock()` 对它返回 **false**。
	DecidedByPermission = "layer:permission"
	// DecidedByNotReady 判定链未就绪（`Checker` 未初始化）——fail-closed。
	DecidedByNotReady = "layer:not-ready"
)

// Action 校验决策动作 —— **门报出的事实**，不是"门替调用方做的处置"。
//
// ★ 语义固化（P-014 · 2026-09-22）：安全层只输出**风险事实**，「怎么处置」由**调用方**决定。
// 三条已落地的处置形态（互不冲突，各按自己的场景选；判据见各自实现处注释）：
// - **已在管线内的入口**（Monitor handler）：以**管线裁决**为准，不得重复裁决
// ——「要不要问」只有管线有权回答（它有用户交互、档位目录与沙箱豁免出口 (c)）；
// - **管线外的后台入口**（hook）：无交互能力 ⇒ fail-closed（Ask ⇒ block）；
// - **IDE 桥接**（bridge.CommandGuard）：只接受明确允许 ⇒ 非 Allow 即 block。
type Action int

const (
	// Allow 允许执行（三层全通过，命令为只读或安全）。
	Allow Action = iota
	// Deny 拒绝执行（空命令/命令注入/危险语法/黑名单/危险分级），Err 携带 N6 ErrorID。
	// 是否属"与询问无关的硬拒绝"由 `Decision.HardBlock()` 回答（见 `DecidedBy`）。
	Deny
	// Ask 需用户确认（破坏性命令）—— **事实**，不是指令。
	// 「是否因此拒绝/弹窗」由调用方按场景决定（已在管线内的入口以管线裁决为准）。
	Ask
)

// Decision 命令校验决策（02 §2.3.1 契约预定义）。
//
// ★ 语义定位（P-014 · 2026-09-22）：本结构是**事实**的载体，不是"该怎么做"的指令。
// 消费方请用 `Blocked()` / `NeedsConfirmation()` / `HardBlock()` 读事实，再按自己的场景定策略。
type Decision struct {
	// Action 决策动作。
	Action Action
	// Reason 拒绝/需确认的原因（人类可读）。
	//
	// ★ ：本字段**给人看**（审批弹窗 / 降级告警）⇒ 走双语形态
	// （`util.Bi`：英文主行 + 缩进中文辅行）。而 `Err` 会随工具结果进**模型上下文**
	// ⇒ 保持**英文单侧**，不附中文（改了就等于改模型读到的内容）。
	Reason string
	// Err 仅 Action=Deny 时非 nil，已 wrap N6 ErrorID（ErrSecurityBlocked/ErrSecuritySandbox）。
	// ★ 英文单侧（见 Reason 字段注释）：它同时进「TUI 显示」与「模型上下文」。
	Err error
	// DecidedBy 给出这次非 Allow 裁决的**层**（取值见 DecidedBy* 常量；Allow 时为空）。
	// 结构化出口：调用方据此区分「命令本身不可执行」（L1/L2）与「危险档须强制确认」（L3），
	// **不得**由 Reason 散文反推（R4）。
	DecidedBy string
	// Severity validator 检测到的最高严重度（Allow/Ask 时为零值）。
	Severity validator.Severity
	// Risk permission 风险分级（仅 Action=Ask/Deny-by-permission 时有意义）。
	Risk permission.BashRisk
	// Deficiencies 本次判定的族A 降级事实（结构化出口）。
	//
	// 空 = 判定链完整生效。非空 = 链路某环能力缺失、已按族A 降级；
	// 调用方（hook/monitor/bridge/TUI）据此决定是否收紧姿态，**不得**改成
	// 解析 Reason 文案来推断（R4：结构化事实不得由散文关键词断言）。
	// T2/T3 会直接体现为 Deny，故此处主要是 T1（降级放行但必须可见）。
	Deficiencies []failtier.Deficiency
}

// Blocked 报告「判定链给出 Deny」这一事实（**任意**一层；含 L3 危险档）。
//
// 它回答的是"门怎么说"，不是"你该怎么办" —— 处置策略见类型 `Action` 的说明。
func (d Decision) Blocked() bool { return d.Action == Deny }

// NeedsConfirmation 报告「该命令属破坏性档、需用户确认」这一事实（L3 `Ask`）。
//
// ★ 它是**事实**：「要不要真的问」由调用方决定。已在管线内的入口**不得**据此再拒绝一次 ——
// 管线不认为需要问时，那次"确认"永远不会发生，重复裁决只会得到一个恒定失败
// （P-014 的缺陷本体：`Monitor` 对任何破坏性档命令 100% 失败）。
func (d Decision) NeedsConfirmation() bool { return d.Action == Ask }

// HardBlock 报告「命令**本身不可执行**」这一**与询问无关**的事实：
// L0（空命令）/ L1（注入、混淆、危险语法）/ L2（沙箱白黑名单、网络）/ 判定链未就绪。
//
// ★ **不含** L3 危险档（`DecidedByPermission`）：危险档的定义是「**强制确认**（永不免问）」，
// 不是硬拒绝（`-R1`）。管线会先强制确认，用户批准后带 `Approved=true` 重跑并放行 ⇒
// 下游 handler 若在此处再硬拒，就等于**否掉了用户的批准**（P-014 A1 要修掉的正是这一条）。
//
// 失败方向刻意取**严**：`DecidedBy` 为空（未来新增 Deny 而未登记层）时按硬拒绝处理
// （黑名单式判断 ⇒ 漏登记只会"更严"，不会"误放")。
func (d Decision) HardBlock() bool {
	return d.Action == Deny && d.DecidedBy != DecidedByPermission
}

// Redactor 密钥脱敏接口（H3 实施时注入具体实现）。
// 守 02 §2.3.1 契约：H3 是脱敏唯一入口，H2 不自实现。
type Redactor interface {
	// RedactSensitive 对字符串进行密钥脱敏，返回脱敏后的字符串。
	RedactSensitive(s string) string
}

// Checker 命令校验器（H2 唯一入口的实现）。
// 字段在 Init 后只读，并发安全（参考 §4.4）。
type Checker struct {
	sandbox   *sandbox.Sandbox
	validator *validator.Validator
	policy    *permission.Policy
	redactor  Redactor // H3 注入，nil 时 no-op + 警告日志
}

// NewChecker 构造校验器，注入 sandbox/validator/policy 三套能力。
func NewChecker(s *sandbox.Sandbox, v *validator.Validator, p *permission.Policy) *Checker {
	return &Checker{sandbox: s, validator: v, policy: p}
}

// SetRedactor 注入 H3 脱敏器（H3 实施时调用）。
// 仅在启动阶段调用一次，非运行时热更新（参考 §4.4）。
func (c *Checker) SetRedactor(r Redactor) {
	c.redactor = r
}

// CheckCommand 命令校验唯一入口（02 §2.3.1 契约）。
// 三层校验：validator 20 项检查 → sandbox 6 类拦截 → permission 风险分级。
// 预处理阶段对命令做 NFKC 归一化 + 空格压缩，抵御全角同形字/多空格绕过。
// 返回 Decision 指导调用方决策。
//
// 族A（G0 · A1）：判定链上任一环节的能力缺失都不静默放行 ——
// validator/sandbox 缺失或 sandbox 未启用 ⇒ 记 T1 Deficiency（降级到下一强制点 + 告警），
// sandbox == nil 不再 panic（原为判定链断裂）。
// 决策结果（Action/Reason/Err/Severity/Risk）与加固前**逐字一致**，
// Deficiency 是纯增量出口（新增字段，调用方零改动）。
func (c *Checker) CheckCommand(cmd string) Decision {
	// Layer 0：空命令直接拒绝（fail-closed）
	if strings.TrimSpace(cmd) == "" {
		return Decision{
			Action:    Deny,
			DecidedBy: DecidedByInput,
			// ★ ：`Err` 保持**英文单侧** —— 它会随工具结果进
			// **模型上下文**，给它加中文辅行 = 改变模型读到的内容（用户拍板「Err 先不中文」）。
			Err: aierrors.WithID(fmt.Errorf("empty command"), aierrors.ErrSecurityBlocked),
			// `Reason` 是**给人看**的（审批弹窗 / 降级告警）⇒ 走双语形态 SSOT。
			Reason: util.Bi("empty command", "空命令"),
		}
	}

	// 预处理：NFKC 归一化 + 空格压缩。
	// 归一化仅用于安全检查，不修改调用方传入的原命令。
	// 目的：抵御全角同形字（ｒｍ → rm）与多空格/tab 绕过（复用 hook 旧 validateHookCommandSafety 能力）。
	normalized := normalizeForCheck(cmd)

	// deficiencies 累计本次判定的族A 降级事实（结构化出口）。
	// 声明为 nil 切片：无降级时 Decision.Deficiencies == nil（判据是 len()==0）。
	var deficiencies []failtier.Deficiency

	// Layer 1：validator 20 项检查（命令注入/混淆/危险命令/语法）。
	// 注：实际方法名是 Run（非 Validate），见 validator/security_checks.go:92。
	if c.validator == nil {
		// 能力缺失 ⇒ T1：降级到 Layer 2/3，但必须可见（不得静默）。
		d := failtier.Deficiency{
			Tier:       failtier.T1,
			Layer:      DeficiencyLayerValidatorMissing,
			Reason:     util.Bi("validator not injected; command-injection/obfuscation static checks were skipped", "validator 未注入，命令注入/混淆静态检查被跳过"),
			FallbackTo: "sandbox(L2)+permission(L3)",
		}
		deficiencies = append(deficiencies, d)
		sandbox.WarnDeficiency(d)
	} else {
		results := c.validator.Run(normalized)
		if validator.HasBlocking(results) {
			highest := highestSeverity(results)
			reason := formatViolations(results)
			return Decision{
				Action:    Deny,
				DecidedBy: DecidedByValidator,
				// Err 英文单侧（进模型上下文）；Reason 由 validator 产出（结构化 CheckID），原样保留。
				Err:          aierrors.WithID(fmt.Errorf("command injection or dangerous syntax: %s", reason), aierrors.ErrSecurityBlocked),
				Reason:       reason,
				Severity:     highest,
				Deficiencies: deficiencies,
			}
		}
	}

	// Layer 2：sandbox 6 类拦截（白名单/黑名单/命令替换/IFS/网络/路径越界）。
	// 注：sandbox 默认 enabled=false 时该层不生效（生产非 TUI 姿态）；族A 要求
	// 这件事对调用方**结构化可见**（Deficiency），而不只是一条日志。
	if c.sandbox == nil {
		// 依赖未注入（原实现直接解引用 ⇒ panic，判定链断裂）：落 T1 而非崩溃。
		d := failtier.Deficiency{
			Tier:       failtier.T1,
			Layer:      DeficiencyLayerSandboxMissing,
			Reason:     util.Bi("sandbox not injected; application-layer allow/deny lists and command-substitution checks were skipped", "sandbox 未注入，应用层白/黑名单与命令替换检查被跳过"),
			FallbackTo: "permission(L3)",
		}
		deficiencies = append(deficiencies, d)
		sandbox.WarnDeficiency(d)
	} else {
		if !c.sandbox.Enabled() {
			sandboxDisabledWarnOnce.Do(func() {
				log.Printf("WARN: sandbox Layer 2 未启用（enabled=false），命令白名单/黑名单/网络隔离均失效。" +
					" TUI 路径由 lifecycle.go 自动启用；非 TUI 路径（plain/bridge/CI/daemon）需显式调用 SetEnabled(true)")
			})
			d := failtier.Deficiency{
				Tier:       failtier.T1,
				Layer:      DeficiencyLayerSandboxDisabled,
				Reason:     util.Bi("sandbox disabled (enabled=false); application-layer allow/deny lists and command-substitution checks were skipped", "sandbox 未启用(enabled=false)，应用层白/黑名单与命令替换检查被跳过"),
				FallbackTo: "permission(L3)",
			}
			deficiencies = append(deficiencies, d)
			sandbox.WarnDeficiency(d)
		} else {
			allowed, reason := c.sandbox.Validate(normalized)
			if !allowed {
				return Decision{
					Action:    Deny,
					DecidedBy: DecidedBySandbox,
					// Err 英文单侧（进模型上下文）；Reason 由 sandbox 产出，原样保留。
					Err:          aierrors.WithID(fmt.Errorf("sandbox blocked: %s", reason), aierrors.ErrSecuritySandbox),
					Reason:       reason,
					Deficiencies: deficiencies,
				}
			}
		}

		// Layer 2.5：网络静态检测（P1-2）。
		// 接入 DetectNetworkAccess，使网络检测参与拦截决策（此前为死代码）。
		// 仅在 networkIsolated=true 时启用，避免影响非隔离模式。
		// 注：姿态读取不依赖 enabled（保持既有行为：disabled 且 networkIsolated=true 时仍拦截入站监听）。
		if c.sandbox.IsNetworkIsolated() {
			result, blocked := sandbox.DetectNetworkAccess(normalized)
			if blocked {
				return Decision{
					Action:    Deny,
					DecidedBy: DecidedByNetwork,
					// Err 英文单侧（进模型上下文）；Reason 是命中的模式串，原样保留。
					Err:          aierrors.WithID(fmt.Errorf("network access blocked by sandbox: %s", result.Pattern), aierrors.ErrSecuritySandbox),
					Reason:       result.Pattern,
					Deficiencies: deficiencies,
				}
			}
		}
	}

	// Layer 3：permission 风险分级（ReadOnly/Destructive/Dangerous）。
	// 注：AnalyzeBashCommand 是包级函数（非 Policy 方法），见 permission.go:521。
	// 注：常量名是 BashRisk*（非 Risk*），见 permission.go:515-517。
	// 注：policy 字段当前为预留，未直接使用（AnalyzeBashCommand 不依赖 Policy 实例）。
	risk := permission.AnalyzeBashCommand(normalized)
	if risk == permission.BashRiskDangerous {
		return Decision{
			Action:    Deny,
			DecidedBy: DecidedByPermission,
			// Err 英文单侧（进模型上下文）。
			Err:          aierrors.WithID(fmt.Errorf("dangerous command: %s", c.redact(cmd)), aierrors.ErrSecurityBlocked),
			Reason:       util.Bi("dangerous command tier", "危险命令分级"),
			Risk:         risk,
			Deficiencies: deficiencies,
		}
	}
	if risk == permission.BashRiskDestructive {
		return Decision{
			Action:       Ask,
			DecidedBy:    DecidedByPermission,
			Reason:       util.Bi("destructive command requires user confirmation", "破坏性命令需用户确认"),
			Risk:         risk,
			Deficiencies: deficiencies,
		}
	}

	// 全部通过（BashRiskReadOnly 或默认放行）
	return Decision{Action: Allow, Deficiencies: deficiencies}
}

// redact 对字符串进行脱敏（redactor 为 nil 时 no-op + 警告日志）。
// 用于错误消息中可能包含的命令文本，避免密钥泄漏到日志。
func (c *Checker) redact(s string) string {
	if c.redactor == nil {
		log.Printf("WARN: 密钥脱敏未启用，H3 未实施，命令可能含敏感信息")
		return s
	}
	return c.redactor.RedactSensitive(s)
}

// defaultChecker 全局校验器单例，由 Init 设置。
// 字段在 Init 后只读，并发安全（参考 §4.4）。
var defaultChecker *Checker

// Init 初始化全局校验器（main.go / tui/app.go 启动时调用）。
// 在 sandbox/validator/permission 三者初始化之后调用。
func Init(s *sandbox.Sandbox, v *validator.Validator, p *permission.Policy) {
	defaultChecker = NewChecker(s, v, p)
}

// CheckCommand 命令校验唯一入口（包级函数，委托给 defaultChecker）。
// hook/plugin/monitor/N10/N12 调用此函数。
// 未初始化时 fail-closed 返回 Deny + ErrSecurityBlocked。
func CheckCommand(cmd string) Decision {
	if defaultChecker == nil {
		// 未初始化时 fail-closed
		return Decision{
			Action:    Deny,
			DecidedBy: DecidedByNotReady,
			// Err 英文单侧（进模型上下文）。
			Err:    aierrors.WithID(fmt.Errorf("security.Checker not initialized"), aierrors.ErrSecurityBlocked),
			Reason: util.Bi("security.Checker not initialized", "Checker 未初始化"),
		}
	}
	return defaultChecker.CheckCommand(cmd)
}

// SetRedactor 注入 H3 脱敏器（H3 实施时调用）。
func SetRedactor(r Redactor) {
	if defaultChecker != nil {
		defaultChecker.SetRedactor(r)
	}
}

// normalizeForCheck 对命令做安全检查前的归一化。
// 1. Unicode NFKC 归一化：全角字符（ｒｍ）转为半角（rm），抵御同形字绕过。
// 2. 空格压缩：连续空格/tab/CR 压缩为单个空格，抵御多空格绕过。
// 归一化仅用于安全检查，不修改调用方传入的原命令。
func normalizeForCheck(cmd string) string {
	normalized := norm.NFKC.String(cmd)
	// strings.Fields 按空白分割，Join 重组为单空格分隔
	return strings.Join(strings.Fields(normalized), " ")
}

// highestSeverity 返回 results 中的最高严重度。
func highestSeverity(results []validator.SecurityResult) validator.Severity {
	var highest validator.Severity
	for _, r := range results {
		if r.Severity > highest {
			highest = r.Severity
		}
	}
	return highest
}

// formatViolations 将违规结果格式化为人类可读的字符串（CheckID: Message 分号分隔）。
func formatViolations(results []validator.SecurityResult) string {
	if len(results) == 0 {
		return ""
	}
	parts := make([]string, 0, len(results))
	for _, r := range results {
		parts = append(parts, fmt.Sprintf("[%s] %s", r.CheckID, r.Message))
	}
	return strings.Join(parts, "; ")
}
