// Package bash — 安全命令白名单（T1/T3，对标同类 CLI is_known_safe_command）。
//
// 仅判定"命令名 + 参数"是否属于已知安全集合，供 IsSafeToCallWithExec 使用。
// 安全 ≠ 无需沙箱，而是"可安全用 exec 直接调用（不走 shell）"的判断。
package bash

import "strings"

// safeCommandChecker 单条命令的安全检查器。
type safeCommandChecker func(name string, args []string) bool

// safeCommands 安全命令白名单。
var safeCommands = map[string]safeCommandChecker{
	"cat":    allowWithFileArgs,
	"cd":     allowWithoutArgs,
	"echo":   allowAnyArgs,
	"grep":   allowAnyArgs,
	"ls":     allowWithFileArgs,
	"pwd":    allowWithoutArgs,
	"wc":     allowWithFileArgs,
	"head":   allowWithFileArgs,
	"tail":   allowWithFileArgs,
	"less":   allowWithFileArgs,
	"more":   allowWithFileArgs,
	"sort":   allowWithFileArgs,
	"uniq":   allowWithFileArgs,
	"cut":    allowWithFileArgs,
	"tr":     allowAnyArgs,
	"diff":   allowWithFileArgs,
	"which":  allowWithCommandName,
	"env":    allowWithoutArgs,
	"true":   allowWithoutArgs,
	"false":  allowWithoutArgs,
	"date":   allowAnyArgs,
	"whoami": allowWithoutArgs,
	"uname":  allowAnyArgs,
}

// safeGitSubcommands 安全 git 子命令（只读）。
var safeGitSubcommands = map[string]bool{
	"status":    true,
	"log":       true,
	"diff":      true,
	"show":      true,
	"branch":    true,
	"remote":    true,
	"config":    true, // 仅查询，-l 模式
	"stash":     true, // list
	"rev-parse": true,
}

// dangerousFindOptions 危险 find 选项（禁止 -exec/-delete）。
var dangerousFindOptions = map[string]bool{
	"-exec":    true,
	"-execdir": true,
	"-delete":  true,
	"-ok":      true,
	"-okdir":   true,
}

// isKnownSafeCommand 判断命令是否为已知安全命令。
func isKnownSafeCommand(name string, args []string) bool {
	if name == "" {
		return false
	}
	// git 特殊处理：仅允许只读子命令
	if name == "git" && len(args) > 0 {
		if safeGitSubcommands[args[0]] {
			return checkGitSubcommandArgs(args[0], args[1:])
		}
		return false
	}
	// find 特殊处理：检查危险选项
	if name == "find" {
		for _, arg := range args {
			if dangerousFindOptions[arg] {
				return false
			}
		}
		return true
	}
	// 通用白名单
	checker, ok := safeCommands[name]
	if !ok {
		return false
	}
	return checker(name, args)
}

// checkGitSubcommandArgs 检查 git 子命令参数是否安全。
func checkGitSubcommandArgs(sub string, args []string) bool {
	switch sub {
	case "config":
		// git config 仅在 -l/--list 或查询（无写操作参数）时安全
		for _, a := range args {
			if a == "--global" || a == "--system" || a == "--local" || a == "--unset" || a == "--unset-all" {
				return false
			}
			if strings.Contains(a, "=") {
				return false // 写入配置
			}
		}
		return true
	default:
		return true
	}
}

// allowAnyArgs 允许任意参数。
func allowAnyArgs(name string, args []string) bool { return true }

// allowWithoutArgs 仅允许无参数。
func allowWithoutArgs(name string, args []string) bool { return len(args) == 0 }

// allowWithFileArgs 允许带文件参数（含选项）。
func allowWithFileArgs(name string, args []string) bool { return true }

// allowWithCommandName 允许带命令名参数（which）。
func allowWithCommandName(name string, args []string) bool { return len(args) <= 1 }
