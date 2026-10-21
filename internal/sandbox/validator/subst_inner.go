// subst_inner.go — ：执行型替换的「递归校验内层」。
//
// 本文件是**执行型替换判定的唯一实现**。判据口径（用户 2026-10-06 拍板）三句话：
//
//	维持 ：替换体**内层**危险（经同套检查判定）⇒ 仍 Critical 硬拒；
//	递归校验内层：内层良性 ⇒ 放行（不再「看到 `$(` 就一刀切拒」）；
//	fail-closed ：内层取不出配对 / 嵌套超深 ⇒ 拒（把「解析失败」当成「没问题」是 同族的错法）。
//
// 为什么这是修正而不是"放宽"：改前 `cat /etc/shadow`（直接执行）**放行**、
// `cat <(cat /etc/shadow)` 却因"有进程替换"被拒 —— 同一条命令，换个写法就换个结论。
// 递归把内外拉平：**替换体里的命令，按它直接执行的同一套标准判定**。
//
// 为什么放在 validator 而不是 permission：这是 **L1 静态校验**（与"批不批"无关的语法面判定），
// 一旦拒了"点允许也没用"；permission 层的危险档另有自己的载荷下探。
// 两层共用同一份**抽取**（`cmdrules.ExecutionPayloads`），只是**策略**不同：
//
//	validator —— 问"内层有没有触发任何静态检查"（更宽）
//	permission —— 问"内层是不是危险档命令"（更窄，但拒得更早）
//
// ★ 之前，本族在 validator 里是**两处各写一份**：AST 层扫 Words 找 `$(`、
// 字符串层用 8 条正则 —— 两处一致纯属巧合。本次收口到本文件 + 一个 CheckID（`CMD-SUBST`）。
package validator

import (
	"fmt"
	"strings"

	"github.com/anchorandfish/whale-code/internal/security/cmdrules"
)

// maxSubstDepth 替换体递归下探的深度上限。超过即 **fail-closed 拒**：
// 深层嵌套不是"没有危险"，而是"我们没下探到"。
const maxSubstDepth = 4

// substitutionFamilyFindings 是执行型替换判定的**唯一实现**。
//
// 返回：本条命令（含嵌套内层）里判为 blocking 的内层事实；空 = 替换族放行。
// 第二返回值 failClosed 是**结构化的** fail-closed 事实（R4：不得由 Message 散文
// 反推）—— "解析不出配对 / 嵌套超深"与"内层判出危险"是两种不同的拒：
// 前者连"按直接执行同一标准"都做不到，消费方必须硬拒，不可降级为可批准。
func substitutionFamilyFindings(cmd string, depth int) (findings []SecurityResult, fc bool) {
	payloads := cmdrules.ExecutionPayloads(cmd)
	if len(payloads) == 0 {
		// 没有载荷 ≠ 一定安全：也可能是"有替换但括号配不上" ⇒ fail-closed。
		if cmdrules.UnbalancedSubstitution(cmd) {
			return []SecurityResult{failClosed("unbalanced command substitution; refused")}, true
		}
		return nil, false
	}
	if depth <= 0 {
		// 确实有载荷要下探，但预算用尽 ⇒ 拒（而不是"下探不了就当没问题"）。
		return []SecurityResult{failClosed("command substitution nested too deeply; refused")}, true
	}

	var out []SecurityResult
	for _, p := range payloads {
		body := strings.TrimSpace(p)
		if body == "" {
			// `$()` / `$( )` 之类空替换：没有内容会被执行 ⇒ 放行。
			continue
		}
		inner, innerFC := innerFindings(body, depth-1)
		if innerFC {
			fc = true
		}
		for _, r := range inner {
			out = append(out, SecurityResult{
				Passed:  false,
				CheckID: "CMD-SUBST",
				// 保留内层的根因文案（如 `AST rm -rf dangerous deletion detected`），
				// 并在前面挂上载荷本身 —— 否则使用者只看到"命令替换被拒"，不知道是哪一段。
				Message:  fmt.Sprintf("in substitution payload %q: %s", body, r.Message),
				Severity: r.Severity,
			})
		}
	}
	return out, fc
}

// innerFindings 对**替换体内层**按「完整检查集 − 替换族自身」判定，再往下递归一层。
//
// 「− 替换族自身」是刻意的：内层的嵌套替换由 ③ 递归接管，若这里也跑替换族，
// 同一次下探会被判两遍，且深度不再收敛。
// 第二返回值：更深层出现的 fail-closed 事实向上传递（消费方仍须硬拒）。
func innerFindings(body string, depth int) (failed []SecurityResult, fc bool) {
	// ① AST 检查（完整 4 项）。单独兜 panic：AST 崩了只丢这一半、字符串那半照跑 ——
	// 与 ASTChecker.Run 的「降级到字符串匹配」口径保持一致。
	func() {
		defer func() { _ = recover() }()
		root := buildASTOrNil(body)
		if root == nil {
			return
		}
		for _, check := range DefaultASTChecks() {
			if r := check(root); r != nil && !r.Passed {
				failed = append(failed, *r)
			}
		}
	}()

	// ② 字符串检查 = 完整套件 − 替换族（见 DefaultInnerSecurityChecks 注释）
	for _, check := range DefaultInnerSecurityChecks() {
		if r := check(body); r != nil && !r.Passed {
			failed = append(failed, *r)
		}
	}

	// ③ 更深一层（depth 用尽时由 substitutionFamilyFindings 统一 fail-closed）
	deep, deepFC := substitutionFamilyFindings(body, depth)
	if deepFC {
		fc = true
	}
	failed = append(failed, deep...)
	return failed, fc
}

// buildASTOrNil 词法 + 语法分析；解析不出根节点时返回 nil（与 ASTChecker.runInternal 同路径）。
func buildASTOrNil(cmd string) *CommandNode {
	tokens := NewTokenizer(cmd).Tokenize()
	return NewASTBuilder(tokens).Build()
}

// failClosed 构造一条 fail-closed 的 Critical 结论。
func failClosed(msg string) SecurityResult {
	return SecurityResult{
		Passed:   false,
		CheckID:  "CMD-SUBST",
		Message:  msg,
		Severity: SeverityCritical,
	}
}

// SubstitutionAssessment 是执行型替换判定的**三态结论**（残留修复，2026-10-08）。
//
// 为什么要有这个类型：ruleengine（internal/sandbox）的 SUBSTITUTION 分支原先
// 自带一份"见 $($ 就拦"的纯文本扫描 —— 同一规则两处实现， 只修了
// validator 那一份 ⇒ 真机预检照弹框（I-13.10 复跑未过，取证回执 2026-10-08）。
// 收口方式 = ruleengine **降级为委托**，把"这条命令的替换体危险吗"只问本包
// 这一个入口。三态映射见 VerdictAssessment 各字段注释与 ruleengine 消费点。
type SubstitutionAssessment struct {
	// FailClosed 解析不出（括号不配对 / 嵌套超深）⇒ 消费方必须硬拒。
	// 它与"内层判出危险"是两种拒：前者连判定都做不了，不可降级为可批准。
	FailClosed bool
	// Findings 内层危险事实（经"完整检查集 − 替换族"递归判定）。
	// 空 + FailClosed=false ⇒ 内层良性，消费方应放行。
	Findings []SecurityResult
}

// AssessSubstitution 判定一条命令的执行型替换（$(`、反引号、`<(…)`、`>(…)`、`=(…)`）
// —— **三态唯一入口**（ruleengine 预检与本包完整套件共用，同一实现）。
//
// ⚠️ 调用方应先自行确认命令里**存在执行型替换形态**再调用（presence 判据是
// 消费方的责任）：本函数对"没有替换的普通命令"返回良性（Findings 空），
// 那不代表"可以跳过其他规则"。这也把 `sh -c "…"` 等执行型包装排除在
// ruleengine 的 SUBSTITUTION 分支之外（无替换形态）—— 它们由本包完整套件
// 在既有路径上判定，职责不变。
func AssessSubstitution(cmd string) SubstitutionAssessment {
	findings, fc := substitutionFamilyFindings(cmd, maxSubstDepth)
	return SubstitutionAssessment{
		FailClosed: fc,
		Findings:   findings,
	}
}
