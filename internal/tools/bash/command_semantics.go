// Package bash — 命令语义分析（T3，对标同类 CLI shouldUseSandbox）。
//
// 基于命令名/参数/重定向判断命令语义（只读/写工作目录/写外部/网络/破坏性/sudo），
// 供 Orchestrator.selectSandbox 决策沙箱级别。
package bash

import (
	"path/filepath"
	"strings"

	"github.com/anchorandfish/whale-code/internal/platform"
	"github.com/anchorandfish/whale-code/internal/security/cmdrules"
)

// SecurityContext 安全上下文（原方案 J 远程模式感知；2026-10-11 砍除
// /remote 后仅剩本地语义，字段随 RemoteMode 一并删除）。
type SecurityContext struct{}

// DefaultSecurityContext 默认安全上下文。
var DefaultSecurityContext = &SecurityContext{}

// CommandSemantics 命令语义。
type CommandSemantics struct {
	IsReadOnly           bool // 只读操作
	WritesToWorkDir      bool // 写工作目录
	WritesOutsideWorkDir bool // 写工作目录外
	AccessesNetwork      bool // 访问网络
	IsDestructive        bool // 破坏性
	UsesSudo             bool // 使用 sudo
}

// readOnlyCommands 只读命令集合。
var readOnlyCommands = map[string]bool{
	"cat": true, "ls": true, "grep": true, "find": true, "wc": true,
	"head": true, "tail": true, "less": true, "more": true, "sort": true,
	"uniq": true, "cut": true, "tr": true, "diff": true, "which": true,
	"env": true, "pwd": true, "date": true, "whoami": true, "uname": true,
	"stat": true, "du": true, "df": true, "file": true,
}

// destructiveCommands 破坏性命令集合。
//
// ★ -R1：名单**不在此处声明** —— 唯一声明在
// `cmdrules.confirmCommandNames`，这里只是它在 bash 语义层的只读视图。
// 此前这里是手抄的第二份（{rm,rmdir,mv,shred,mkfs}），与
// `tools/pipeline` 那份（多 dd/fdisk/parted）在集合上已经漂移。
var destructiveCommands = func() map[string]bool {
	m := make(map[string]bool)
	for _, name := range cmdrules.ConfirmCommandNames() {
		m[name] = true
	}
	return m
}()

// workDirWriteCommands 写工作目录命令集合。
var workDirWriteCommands = map[string]bool{
	"mkdir": true, "touch": true, "cp": true, "tee": true, "chmod": true,
	"chown": true, "ln": true, "tar": true, "zip": true, "unzip": true,
}

// networkCommands 网络命令集合。
var networkCommands = map[string]bool{
	"curl": true, "wget": true, "ssh": true, "scp": true, "rsync": true,
	"ftp": true, "nc": true, "telnet": true,
}

// AnalyzeSemantics 分析命令语义（基于 AST + 工作目录）。
// 使用默认安全上下文（本地模式）。远程模式请用 AnalyzeSemanticsWithContext。
func AnalyzeSemantics(ast *CommandAST, workDir string) *CommandSemantics {
	return AnalyzeSemanticsWithContext(ast, workDir, DefaultSecurityContext)
}

// AnalyzeSemanticsWithContext 分析命令语义（带安全上下文）。
func AnalyzeSemanticsWithContext(ast *CommandAST, workDir string, secCtx *SecurityContext) *CommandSemantics {
	s := &CommandSemantics{}
	if ast == nil {
		return s
	}
	if secCtx == nil {
		secCtx = DefaultSecurityContext
	}
	absWorkDir := absClean(workDir)

	for _, cmd := range ast.Commands {
		// sudo 前缀检测
		name := cmd.Name
		if name == "sudo" && len(cmd.Args) > 0 {
			s.UsesSudo = true
			name = cmd.Args[0]
		}

		switch {
		case readOnlyCommands[name]:
			s.IsReadOnly = true
		case destructiveCommands[name]:
			s.IsDestructive = true
			s.WritesToWorkDir = true
		case workDirWriteCommands[name]:
			s.WritesToWorkDir = true
		case networkCommands[name]:
			s.AccessesNetwork = true
		}

		// git 子命令语义
		if name == "git" && len(cmd.Args) > 0 {
			sub := cmd.Args[0]
			switch sub {
			case "status", "log", "diff", "show", "branch", "remote", "config", "rev-parse":
				s.IsReadOnly = true
			case "commit", "push", "merge", "rebase", "reset", "clean", "rm":
				s.WritesToWorkDir = true
			}
		}

		// 重定向语义
		for _, r := range cmd.Redirects {
			if r.Op == ">" || r.Op == ">>" || r.Op == "&>" || r.Op == "&>>" {
				if isPathInDir(r.Target, absWorkDir) {
					s.WritesToWorkDir = true
				} else {
					s.WritesOutsideWorkDir = true
				}
			}
		}

		// 绝对路径参数语义
		for _, arg := range cmd.Args {
			if strings.HasPrefix(arg, "-") {
				continue
			}
			if isAbsPath(arg) && !isPathInDir(arg, absWorkDir) {
				// 写命令操作工作目录外的绝对路径
				if !readOnlyCommands[name] {
					s.WritesOutsideWorkDir = true
				}
			}
		}
	}

	// 只读命令不应同时有写重定向
	if s.WritesToWorkDir || s.WritesOutsideWorkDir {
		s.IsReadOnly = false
	}
	return s
}

// isPathInDir 判断路径是否在目录内（**不做符号链接解析**，仅规范化后比较）。
//
// ★ 2026-09-19 收敛（同一语义多处实现）：边界判定改用 platform.IsWithinResolved（全仓唯一实现）。
// 原实现已自带分隔符边界检查（见下方"防止 /home/user 被 /home/u 误判"的注释），语义等价；
// 收敛后额外获得两点：
// - **dir 也被规范化**（原实现只 absClean 了 path，dir 原样使用 ⇒ 传入非规范的 dir 会漏判）；
// - 大小写按文件系统语义（macOS/Windows 上不再把同一目录的大小写变体误判为"在外"）。
//
// "不做符号链接解析"这一**策略保持不变**：IsWithinResolved 只比较规范化字符串，不解析 symlink。
func isPathInDir(path, dir string) bool {
	if path == "" || dir == "" {
		return false
	}
	return platform.IsWithinResolved(absClean(path), absClean(dir))
}

// absClean 返回清理后的绝对路径。
func absClean(p string) string {
	if p == "" {
		return ""
	}
	abs, err := filepath.Abs(p)
	if err != nil {
		return filepath.Clean(p)
	}
	return filepath.Clean(abs)
}
