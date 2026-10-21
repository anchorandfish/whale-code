// Package validator — 从 bash 命令串里提取**写目标**（T2 的输入面）。
//
// 为什么需要它：审批面要回答的问题是"这一次调用的写入会不会全部落在可写根内"。
// 这个问题的输入是**写目标集合**，而写目标只存在于命令语法里 —— 靠正则找
// `> path` 会在管道、引号、heredoc、`2>&1` 等形态上漏检（本仓已有
// “ 四套 bash 危险判定各自实现、彼此不一致的教训）。
// ⇒ 复用**已存在的**词法＋语法层（tokenizer → ASTBuilder），只做一次提取。
//
// ★ 与 `checkDangerousRedirectAST` 的分工：那条是**安全过滤**（只看危险路径前缀，
// 命中即拒）；本函数是**事实提取**（把所有写目标报出来，结论由调用方按可写根判）。
// 两者共用 AST，不共用结论 —— 把安全判断塞进提取函数会让它无法被别的策略复用。
//
// ★ 刻意只提取**重定向写目标**（`>` / `>>` / `&>` / `N>`）：
// `touch x` / `cp a b` / `rm x` 这类"路径在参数里"的写操作，其语义依赖具体命令
// （`rm` 的第 3 个参数才是路径、`cp` 有两个……）⇒ 静态提取必然漏报。
// 漏报的后果是"判不出必拒" ⇒ 落到「未知」⇒ **仍然询问**（保守侧），
// 不会造成静默放行。这是与「宁多报不漏报」的补丁扫描（diff.ScanPatchTargets）
// 不同的取舍：那里的多报只是提前拒绝一次本就该拒的调用，这里的多报会把
// 合法命令误判成必拒。
package validator

import (
	"strings"
)

// devSinkPrefixes 是「写进去等于没写」的 /dev 目标前缀（与 security_checks.go 的
// devSafeWriteSinks 同一语义）。这些目标不构成"逃逸可写根"，必须从写目标集合里剔除，
// 否则 `2>/dev/null` 这种遍地都是的写法会被判成必拒。
func isDevSink(path string) bool {
	p := strings.TrimSpace(path)
	p = strings.Trim(p, `"'`)
	if !strings.HasPrefix(p, "/dev/") {
		return false
	}
	for _, s := range devSafeWriteSinks {
		if p == s || strings.HasPrefix(p, s) {
			return true
		}
	}
	return false
}

// BashWriteTargets 提取命令中所有**写重定向**的目标路径。
//
// 返回：
// - targets：写目标（保持原文，可能是相对路径；调用方按工作区解析）；
// - unresolved：是否存在**无法静态归约**的成分 —— 命令替换（`$(...)`/反引号）、
// 变量（`$VAR`、`${VAR}`）出现在写目标里。为 true 时调用方**不得**据此判定
// "必拒"（因为真实目标可能落在任何地方）⇒ 只能落到「未知」。
//
// 保守方向声明：本函数**只可能**多报 unresolved、少报 targets（解析失败也报
// unresolved）—— 两种偏差都会让调用方退到"询问"，不会造成静默放行。
func BashWriteTargets(command string) (targets []string, unresolved bool) {
	if strings.TrimSpace(command) == "" {
		// 空命令：没有写目标，也无可归约内容。
		return nil, false
	}

	builder := NewASTBuilder(NewTokenizer(command).Tokenize())
	root := builder.Build()
	if root == nil {
		// 解析失败（含大括号不匹配的保守部分 AST）⇒ 不可归约。
		return nil, true
	}

	seen := make(map[string]bool)
	WalkAST(root, func(node *CommandNode) bool {
		for _, r := range node.Redirects {
			// 仅「写文件」重定向（> / >> / N> / &>）；读方向与 fd 复制（2>&1 / >&2）不落盘。
			if !isFileWriteRedirect(r) {
				continue
			}
			t := strings.TrimSpace(r.Target)
			t = strings.Trim(t, `"'`)
			if t == "" {
				unresolved = true
				continue
			}
			if isDevSink(t) {
				continue // /dev/null 等无害 sink
			}
			if strings.ContainsAny(t, "$`") {
				// 变量 / 命令替换：真实目标不可静态确定。
				unresolved = true
				continue
			}
			if !seen[t] {
				seen[t] = true
				targets = append(targets, t)
			}
		}
		return true
	})

	// 写目标里出现解析不出的替换体时，ASTBuilder 只跳过不记名
	// （skipSubstitutionBody）⇒ 上面看不到。用命令串层面兜一层：
	// 只要出现命令替换，就把整条命令标成不可归约。
	if strings.Contains(command, "$(") || strings.Contains(command, "`") {
		unresolved = true
	}
	return targets, unresolved
}
