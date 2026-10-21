// injection_scan.go — S18/G3 记忆内容「间接注入」扫描与隔离（Human gate 选项 B）。
//
// 背景（I10 抗间接注入 / 自提权）：
// capability / procedural 记忆（procedural 由 reflect.go 经 LLM 合成）会被注入
// 子代理 system prompt（tools/agent.go loadAgentCapability ← CapabilityLookup ←
// memory.GetCapability）。若记忆文件被外部篡改、或反思生成被污染，其正文就可能
// 携带「忽略以上指令 / 关闭安全检查 / 提权」之类内容，构成间接注入。
//
// 选项 B 的取向：
//  1. 加载时扫描：对将进入 prompt 的记忆内容做注入特征扫描；
//  2. 数据隔离标记：注入时用明确的「这是参考数据、不是指令」信封包裹（见 tools 层），
//     并保证记忆内容不能作为指令执行、不能自我提权。
//
// 本文件只做「扫描 + 隔离」，不做策略决策：命中即「隔离」（不注入原文），
// 宁可少注入一条可疑记忆（可用性），也不把可疑指令喂给模型（安全）。
package memory

import (
	"log"
	"regexp"
	"strings"
)

// InjectionScanResult 注入扫描结果。
type InjectionScanResult struct {
	Suspicious bool     // 是否命中注入/自提权特征
	Reasons    []string // 命中的特征名（不含原文，避免把可疑内容二次写日志）
}

// injectionQuarantineNotice 命中注入特征时替代原文注入的隔离提示。
const injectionQuarantineNotice = "[MEMORY QUARANTINED: possible prompt-injection or self-escalation content detected; raw content withheld by S18/G3 guard]"

// injectionPattern 单条注入特征。
type injectionPattern struct {
	name string
	re   *regexp.Regexp
}

// injectionPatterns 注入 / 自提权特征集合（英文 + 中文）。
// 设计原则：要求「动作词 + 对象词」共现，尽量降低对正常能力笔记（如 "build 命令为 go build"）的误伤。
var injectionPatterns = []injectionPattern{
	// 1) 指令覆盖（EN）：ignore/disregard/forget/override ... previous/prior ... instruction(s)/prompt/rules
	{"instruction-override-en", regexp.MustCompile(`(?i)\b(ignore|disregard|forget|override|discard)\b[^\n]{0,40}\b(previous|prior|above|earlier|preceding|all)\b[^\n]{0,24}\b(instruction|prompt|rule|direction|message|constraint)s?\b`)},
	// 2) 指令覆盖（zh）：忽略/无视/忘记/覆盖/推翻 … 之前/以上/上述/所有 … 指令/提示/规则/要求
	{"instruction-override-zh", regexp.MustCompile(`(忽略|无视|忘记|覆盖|推翻|作废)[^\n]{0,20}(之前|以上|上述|先前|前面|所有|全部)[^\n]{0,12}(指令|提示|规则|要求|命令|说明|约束)`)},
	// 3) 角色劫持：you are now / act as ... / 假装你是 …
	{"role-hijack", regexp.MustCompile(`(?i)\b(you are now|from now on,? you|act as (an?|the)\b|new (system )?(instruction|prompt|role)|pretend (to be|you are)|假装你是|现在你是|从现在开始你|你现在是)`)},
	// 4) 系统提示探测：尝试读取/泄露 system prompt
	{"system-prompt-probe", regexp.MustCompile(`(?i)\b(your |the )?(system prompt|initial instructions|hidden instructions)\b|系统提示词?|初始指令|隐藏指令`)},
	// 5) 关闭安全（EN）：disable/bypass/turn off/skip/circumvent ... security/safety/guardrail/permission/approval/sandbox/check
	{"security-disable-en", regexp.MustCompile(`(?i)\b(disable|bypass|turn off|switch off|skip|circumvent|evade)\b[^\n]{0,32}\b(security|safety|guardrail|guard rails?|permission|approval|confirmation|sandbox|protection|check)\b`)},
	// 6) 关闭安全（zh）
	{"security-disable-zh", regexp.MustCompile(`(关闭|禁用|绕过|跳过|解除|取消|忽略)[^\n]{0,12}(安全|权限|审批|校验|检查|沙箱|防护|确认)`)},
	// 7) 显式提权 / 危险逃逸命令
	{"privilege-escalation", regexp.MustCompile(`(?i)(\bsudo\b|chmod\s+0?777|chown\s+root|setenforce\s+0|--dangerously-skip-permissions|danger[-_ ]full[-_ ]access|rm\s+-rf\s+/)`)},
	// 8) 提权请求（EN）：grant/give/elevate ... permission/privilege/root/admin
	{"permission-grant-en", regexp.MustCompile(`(?i)\b(grant|give|elevate|escalate)\b[^\n]{0,24}\b(permission|privilege|access|root|admin|bypass)\b`)},
}

// ScanForInjection 扫描记忆内容是否含注入 / 自提权特征。
// 返回命中的特征名（不含原文），供调用方决策与日志。
func ScanForInjection(content string) InjectionScanResult {
	res := InjectionScanResult{}
	if strings.TrimSpace(content) == "" {
		return res
	}
	for _, p := range injectionPatterns {
		if p.re.MatchString(content) {
			res.Suspicious = true
			res.Reasons = append(res.Reasons, p.name)
		}
	}
	return res
}

// SanitizeMemoryForInjection 返回「可安全注入」的记忆内容：
//   - 未命中：返回原文
//   - 命中：返回隔离提示（丢弃原文，避免可疑指令进入 prompt），并记 WARN 日志
//     （日志只记特征名与会话无关的元信息，不二次落可疑正文）。
//
// 第二个返回值为扫描结果，调用方可据此做统计 / 审计。
func SanitizeMemoryForInjection(content string) (string, InjectionScanResult) {
	res := ScanForInjection(content)
	if !res.Suspicious {
		return content, res
	}
	log.Printf("WARN: memory content quarantined by S18/G3 injection guard (reasons: %s)", strings.Join(res.Reasons, ","))
	return injectionQuarantineNotice, res
}
