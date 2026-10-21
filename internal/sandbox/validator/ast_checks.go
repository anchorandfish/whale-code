// Package validator ast_checks.go 命令安全 AST 解析框架 — 检查层（T1-P1-A4 /）。
//
// 本文件实现 AST 检查器，遍历 AST 节点查找危险模式。
// 与现有 21 项字符串匹配共存（AST 在前 + 降级到字符串匹配）。
//
// 设计依据：
// - REF-外部-tree-sitter https://tree-sitter.github.io/tree-sitter/ @2026-07-20（AST 节点遍历参考）
// - :42 SecurityCheck 类型（向后兼容）
//
// 关键约束：
// - CGO_ENABLED=0：纯 Go 标准库实现
// - 三重降级：panic recover + 50ms 超时 + 大括号不匹配部分 AST
// - CheckID 加 -AST 后缀：与字符串匹配 CheckID 显式区分
package validator

import (
	"context"
	"fmt"
	"strings"
	"time"
)

// astTimeout AST 解析超时（50ms，超出则完全降级到字符串匹配）。
const astTimeout = 50 * time.Millisecond

// ASTCheckFunc AST 检查函数类型（基于 AST 根节点）。
type ASTCheckFunc func(root *CommandNode) *SecurityResult

// ASTChecker AST 检查器（纯 Go，无 cgo）。
type ASTChecker struct {
	checks []ASTCheckFunc
}

// NewASTChecker 创建带默认 AST 检查套件的检查器（5 项）。
func NewASTChecker() *ASTChecker {
	return &ASTChecker{checks: DefaultASTChecks()}
}

// DefaultASTChecks 返回默认 AST 检查套件（4 项）。
//
// 检查项：
// 1. FORK-BOMB-AST：AST fork 炸弹（Critical）
// 2. RM-RF-AST：AST rm -rf 危险删除（Critical）
// 3. VAR-INDIRECT-AST：AST 变量间接危险调用（High）
// 4. DANG-REDIR-AST：AST 危险重定向（High）
//
// ★ **移除**了原第 3 项 `CMD-SUBST-AST`（AST 扫 Words 找 `$(`）。
// 两条理由：
// 1. **判不了**：AST 的 Words 里只有标记、没有载荷（替换体被 `skipSubstitutionBody` 整段跳过，
// 不进 AST）⇒ 想"递归校验内层"就必须拿到载荷文本，而载荷在 AST 里根本不存在；
// 2. **重复实现**：字符串层 `checkCommandSubstitution` 同时自带一份 8 条正则的判定，
// 两处结论一致纯属巧合 —— 正是本仓最忌的"同语义两处实现"（已为此栽过一次）。
//
// ⇒ 本族收口到**字符串层单点**：`checkCommandSubstitution` + `subst_inner.go`，CheckID `CMD-SUBST`。
// 覆盖面**不缩**：原 AST 那份只认 `$(` / `<(` / `>(`，字符串层那份是它的超集（另含反引号 / `=(`）。
func DefaultASTChecks() []ASTCheckFunc {
	return []ASTCheckFunc{
		checkForkBombAST,
		checkRmRfAST,
		checkVarIndirectAST,
		checkDangerousRedirectAST,
	}
}

// Run 解析命令为 AST 并运行所有 AST 检查。
//
// 实现三重降级（§5.4）：
// - panic recover：任何 panic 都降级到字符串匹配
// - 50ms 超时：context.WithTimeout + select 超时降级
// - 大括号不匹配：ASTBuilder 保守返回部分 AST
//
// 解析失败时返回 nil（降级到字符串匹配）。
func (a *ASTChecker) Run(cmd string) (results []SecurityResult) {
	// 降级机制 1：panic recover
	defer func() {
		if r := recover(); r != nil {
			// panic 时返回 nil，触发降级到字符串匹配
			results = nil
		}
	}()

	// 降级机制 2：50ms 超时
	ctx, cancel := context.WithTimeout(context.Background(), astTimeout)
	defer cancel()

	done := make(chan []SecurityResult, 1)
	go func() {
		defer func() {
			if r := recover(); r != nil {
				done <- nil
			}
		}()
		done <- a.runInternal(cmd)
	}()

	select {
	case r := <-done:
		return r
	case <-ctx.Done():
		// 超时：返回 nil，降级到字符串匹配
		return nil
	}
}

// runInternal 内部执行 tokenize → build → check 三阶段。
func (a *ASTChecker) runInternal(cmd string) []SecurityResult {
	// S1: 词法分析
	tokenizer := NewTokenizer(cmd)
	tokens := tokenizer.Tokenize()

	// S2: 语法分析
	builder := NewASTBuilder(tokens)
	root := builder.Build()
	if root == nil {
		return nil
	}

	// S3: AST 检查
	var failed []SecurityResult
	for _, check := range a.checks {
		r := check(root)
		if r != nil && !r.Passed {
			failed = append(failed, *r)
		}
	}
	return failed
}

// === AST 检查 1：fork 炸弹 ===

// checkForkBombAST 检测 AST fork 炸弹（Critical）。
//
// 检测逻辑：
// 1. 遍历 AST 找 FunctionDef 节点（函数定义）
// 2. 函数体 Body 为 Pipeline
// 3. Pipeline.Children 含 2 个 SimpleCommand，Words[0] == FuncName
// 4. 至少 1 个子命令 Background=true（含 & 后台执行）
//
// 命中即判定为 fork 炸弹（自递归管道+后台）。
// 与字符串匹配 FORK-BOMB 共存（双重命中时两条记录都保留）。
func checkForkBombAST(root *CommandNode) *SecurityResult {
	detected := false
	WalkAST(root, func(node *CommandNode) bool {
		if node.Type != NodeFunctionDef || node.FuncName == "" || node.Body == nil {
			return true
		}
		if isForkBombBody(node.Body, node.FuncName) {
			detected = true
			return false // 停止遍历
		}
		return true
	})
	if detected {
		return &SecurityResult{
			Passed:   false,
			CheckID:  "FORK-BOMB-AST",
			Message:  "AST fork bomb detected (self-recursive function with pipe and background)",
			Severity: SeverityCritical,
		}
	}
	return &SecurityResult{Passed: true}
}

// isForkBombBody 判断函数体是否为 fork 炸弹模式（NAME | NAME &）。
func isForkBombBody(body *CommandNode, funcName string) bool {
	if body == nil || body.Type != NodePipeline {
		return false
	}
	if len(body.Children) < 2 {
		return false
	}
	// 检查所有子命令是否都是自递归调用 + 至少一个后台
	allRecursive := true
	hasBackground := false
	for _, child := range body.Children {
		if child.Type != NodeSimpleCommand {
			allRecursive = false
			break
		}
		if len(child.Words) == 0 || child.Words[0] != funcName {
			allRecursive = false
			break
		}
		if child.Background {
			hasBackground = true
		}
	}
	return allRecursive && hasBackground
}

// === AST 检查 2：rm -rf 危险删除 ===

// checkRmRfAST 检测 AST rm -rf 危险删除（Critical）。
//
// 检测逻辑：
// 1. 遍历 AST 找 SimpleCommand 节点
// 2. Words[0] (lower) == "rm"
// 3. Words 含 "-rf" / "-fr" / ("-r" + "-f" 组合) / "-r" 单独 + 危险目标
// 4. 目标（Words[1..]）含敏感路径："/" / "~" / "$home"（lower）/ "$" 开头的变量
//
// 与字符串匹配 dangerousPatterns 共存（双重命中时两条记录都保留）。
func checkRmRfAST(root *CommandNode) *SecurityResult {
	detected := false
	var detectedMsg string
	WalkAST(root, func(node *CommandNode) bool {
		if node.Type != NodeSimpleCommand || len(node.Words) == 0 {
			return true
		}
		if msg, hit := checkRmRfWords(node.Words); hit {
			detected = true
			detectedMsg = msg
			return false
		}
		return true
	})
	if detected {
		return &SecurityResult{
			Passed:   false,
			CheckID:  "RM-RF-AST",
			Message:  detectedMsg,
			Severity: SeverityCritical,
		}
	}
	return &SecurityResult{Passed: true}
}

// AssessRmRfDangerousTarget 判定命令是否含「rm 递归删除灾难目标」形态（SSOT，）。
//
// 背景：rm 已从预检硬拒名单降为可批准档（，2026-10-14 拍板），但灾难形态
// （`rm -rf /` / `~` / `$HOME` / `$变量`）必须继续硬拒 —— 否则用户批准后仍会被
// 执行链的 RM-RF-AST Critical 拦下（"批准了还拒" = 病在窄集合复发）。
//
// ★ 唯一实现：本函数直接复用 checkRmRfAST 的同一套 AST 判定（执行链 Critical
// 的来源），不另写第二份正则 —— 同一语义两处实现是本仓明令禁止的。
//
// 返回 true ⇒ 预检必须升级为硬拒（连批准框都不弹）。
func AssessRmRfDangerousTarget(cmd string) bool {
	checker := &ASTChecker{checks: []ASTCheckFunc{checkRmRfAST}}
	for _, r := range checker.Run(cmd) {
		if r.CheckID == "RM-RF-AST" && !r.Passed {
			return true
		}
	}
	return false
}

// checkRmRfWords 检查单词列表是否构成 rm -rf 危险删除。
func checkRmRfWords(words []string) (string, bool) {
	if len(words) == 0 {
		return "", false
	}
	if strings.ToLower(words[0]) != "rm" {
		return "", false
	}
	// 收集 flags 和目标
	var flags []string
	var targets []string
	for _, w := range words[1:] {
		if strings.HasPrefix(w, "-") {
			flags = append(flags, w)
		} else {
			targets = append(targets, w)
		}
	}
	// 检查 -rf / -fr 组合
	hasRf := false
	for _, f := range flags {
		lower := strings.ToLower(f)
		if lower == "-rf" || lower == "-fr" {
			hasRf = true
			break
		}
		// -r 和 -f 分开
		if lower == "-r" {
			for _, f2 := range flags {
				if strings.ToLower(f2) == "-f" {
					hasRf = true
					break
				}
			}
		}
	}
	if !hasRf {
		return "", false
	}
	// 检查目标是否含敏感路径
	for _, t := range targets {
		lower := strings.ToLower(t)
		if isDangerousRmTarget(t, lower) {
			return fmt.Sprintf("AST rm -rf dangerous target: %s", t), true
		}
	}
	return "", false
}

// isDangerousRmTarget 判断 rm 目标是否为危险路径。
func isDangerousRmTarget(target, lower string) bool {
	// 根目录 /
	if target == "/" || target == "/*" {
		return true
	}
	// 家目录 ~
	if strings.HasPrefix(target, "~") {
		return true
	}
	// $HOME / $home 变量
	if lower == "$home" || strings.HasPrefix(lower, "$home/") {
		return true
	}
	// 任意 $ 变量（无法静态求值，保守判定为危险）
	if strings.HasPrefix(target, "$") {
		return true
	}
	return false
}

// === AST 检查 3（原）：命令替换/进程替换 —— 已迁出本层（，2026-10-06）===
//
// 原 `checkCmdSubstAST` 在此实现：遍历 AST 找 Words 里的 `$(` / `<(` / `>(` 标记，命中即 Critical。
//
// 它有两个结构性缺陷，故 整族迁出：
// 1. **判不了**：AST 的 Words 里只有标记字符串，替换体被 `skipSubstitutionBody` 跳过 ⇒
// 想"递归校验内层"就必须拿到载荷文本，而载荷在 AST 里根本不存在；
// 2. **重复实现**：字符串层 `checkCommandSubstitution` 同时自带一份 8 条正则的判定，
// 两处结论一致纯属巧合（本仓最忌的"同语义两处实现"）。
//
// 现由 `checkCommandSubstitution`（字符串层）+ `subst_inner.go`（递归下探）**单点**负责，
// CheckID 统一为 `CMD-SUBST`。本包 AST 检查减为 4 项，见 DefaultASTChecks。

// === AST 检查 4：变量间接危险调用 ===

// checkVarIndirectAST 检测 AST 变量间接危险调用（High）。
//
// 检测逻辑（两阶段扫描）：
// 1. 第一阶段：遍历 AST 收集所有 VarAssign，记录 Value 为危险命令名的变量
// 危险命令名集合：{"rm", "mkfs", "dd", "format", "chmod", "chown"}
// 2. 第二阶段：遍历 AST 找 SimpleCommand，若 Words[0] 是 $VAR 形式且 VAR 在第一阶段危险变量集合中，命中
//
// 示例：x=rm; $x -rf / → 命中（x=rm 在第一阶段的命令中，$x -rf / 在第二阶段的命令中）
// 示例：a=rm; $a -rf /tmp → 命中
func checkVarIndirectAST(root *CommandNode) *SecurityResult {
	dangerousNames := map[string]bool{
		"rm": true, "mkfs": true, "dd": true,
		"format": true, "chmod": true, "chown": true,
	}

	// 第一阶段：收集所有危险变量赋值（VAR=危险命令名）
	dangerousVars := make(map[string]string) // var name → dangerous value
	WalkAST(root, func(node *CommandNode) bool {
		for _, assign := range node.VarAssigns {
			if dangerousNames[strings.ToLower(assign.Value)] {
				dangerousVars[assign.Name] = assign.Value
			}
		}
		return true
	})

	if len(dangerousVars) == 0 {
		return &SecurityResult{Passed: true}
	}

	// 第二阶段：查找使用 $VAR 形式调用危险变量的 SimpleCommand
	detected := false
	var detectedMsg string
	WalkAST(root, func(node *CommandNode) bool {
		if node.Type != NodeSimpleCommand || len(node.Words) == 0 {
			return true
		}
		first := node.Words[0]
		// 提取 $VAR 中的 VAR 名
		varName := extractVarName(first)
		if varName == "" {
			return true
		}
		if dangerousValue, ok := dangerousVars[varName]; ok {
			detected = true
			detectedMsg = fmt.Sprintf("AST variable indirect dangerous call: %s=%s, %s", varName, dangerousValue, first)
			return false
		}
		return true
	})
	if detected {
		return &SecurityResult{
			Passed:   false,
			CheckID:  "VAR-INDIRECT-AST",
			Message:  detectedMsg,
			Severity: SeverityHigh,
		}
	}
	return &SecurityResult{Passed: true}
}

// extractVarName 从 $VAR / ${VAR} 字符串中提取变量名。
//
// 返回空字符串表示不是变量引用形式。
func extractVarName(s string) string {
	if len(s) < 2 || s[0] != '$' {
		return ""
	}
	if s[1] == '{' {
		// ${VAR} 形式
		if len(s) < 4 || s[len(s)-1] != '}' {
			return ""
		}
		return s[2 : len(s)-1]
	}
	// $VAR 形式
	var name strings.Builder
	for i := 1; i < len(s); i++ {
		ch := s[i]
		if (ch >= 'a' && ch <= 'z') || (ch >= 'A' && ch <= 'Z') || (ch >= '0' && ch <= '9') || ch == '_' {
			name.WriteByte(ch)
		} else {
			break
		}
	}
	return name.String()
}

// === AST 检查 5：危险重定向 ===

// checkDangerousRedirectAST 检测 AST 危险重定向（High）。
//
// 检测逻辑：
// 1. 遍历 AST 找 RedirectNode
// 2. Op 含 ">" / ">>"（写操作）
// 3. Target 含敏感路径：/etc/ /usr/ /bin/ /sbin/ /boot/ /sys/ /proc/ /dev/ /root/
//
// 与字符串匹配 DANG-REDIR 共存（双重命中时两条记录都保留）。
func checkDangerousRedirectAST(root *CommandNode) *SecurityResult {
	detected := false
	var detectedMsg string
	WalkAST(root, func(node *CommandNode) bool {
		for _, r := range node.Redirects {
			// 仅「写文件」重定向（> / >> / N> / &>）；读方向与 fd 复制（2>&1 / >&2）不算，
			// 判定统一走 isFileWriteRedirect（SSOT）。
			if !isFileWriteRedirect(r) {
				continue
			}
			if isDangerousWriteTarget(r.Target) {
				detected = true
				if strings.TrimSpace(r.Target) == "" {
					// ★ ：空/无法解析目标（典型：载荷里悬空的 `>`，
					// 如 `sh -c 'echo R-<CODE>'`）不是"敏感路径"问题 —— 真病因是命令
					// 本身写残（真 shell 对悬空 `>` 报语法错）。文案必须说真话，否则
					// 模型会朝"避开敏感路径"方向改写、永远改不对。判定面不动：
					// isDangerousWriteTarget("") 仍 fail-closed（边界，S2 SSOT）。
					detectedMsg = fmt.Sprintf("AST write redirect with missing/unparseable target (conservative deny): %s", r.Op)
				} else {
					detectedMsg = fmt.Sprintf("AST dangerous redirect to sensitive path: %s %s", r.Op, r.Target)
				}
				return false
			}
		}
		return true
	})
	if detected {
		return &SecurityResult{
			Passed:   false,
			CheckID:  "DANG-REDIR-AST",
			Message:  detectedMsg,
			Severity: SeverityHigh,
		}
	}
	return &SecurityResult{Passed: true}
}
