// Package secret 是密钥脱敏的唯一入口（守 02 §2.3.1 契约）。
//
// 所有模块的密钥脱敏必须委托本包，不自实现。
// 本包完全自包含（仅依赖 stdlib），避免与 ai/log/security 产生循环导入。
// 错误包装通过回调模式（SetErrorWrapper）在启动阶段注入 ai.WithID。
package secret

import (
	"os"
	"strings"
)

// DefaultAllowedEnv 是子进程默认允许继承的最小环境变量白名单。
// 对标同类 CLI ShellEnvironmentPolicy 默认集 + whale-code hook.go safeEnvWhitelist。
// 调用方可通过 ScrubEnvWith 追加业务所需变量。
var DefaultAllowedEnv = []string{
	"PATH", "HOME", "USERPROFILE", "USER", "USERNAME",
	"LANG", "LC_ALL", "LC_CTYPE",
	"TERM", "SHELL", "SystemRoot", "TEMP", "TMP", "TMPDIR",
}

// ScrubEnv 按默认白名单过滤环境变量，剥离所有非白名单变量（含密钥）。
// 守 02 §2.3.1 契约：env 脱敏主接口。
// 入参 envs: os.Environ() 返回的 "KEY=VALUE" 切片。
// 返回: 仅含白名单变量的 "KEY=VALUE" 切片。
func ScrubEnv(envs []string) []string {
	return ScrubEnvWith(envs, nil)
}

// ScrubEnvWith 按默认白名单 + extra 允许集过滤环境变量。
// extra 用于工具声明所需 env（如 git 需要 GIT_* / github 需要 GITHUB_TOKEN）。
// extra 元素支持前缀匹配（如 "GIT_" 匹配 GIT_AUTHOR_NAME 等）和精确匹配（如 "GITHUB_TOKEN"）。
func ScrubEnvWith(envs []string, extra []string) []string {
	allowed := make(map[string]bool, len(DefaultAllowedEnv)+len(extra))
	for _, name := range DefaultAllowedEnv {
		allowed[name] = true
	}
	// extra 中的精确名直接加入白名单
	var extraPrefixes []string
	for _, e := range extra {
		if strings.HasSuffix(e, "_") {
			extraPrefixes = append(extraPrefixes, e)
		} else {
			allowed[e] = true
		}
	}

	result := make([]string, 0, len(envs))
	for _, env := range envs {
		idx := strings.IndexByte(env, '=')
		if idx <= 0 {
			continue
		}
		name := env[:idx]
		if allowed[name] {
			result = append(result, env)
			continue
		}
		for _, prefix := range extraPrefixes {
			if strings.HasPrefix(name, prefix) {
				result = append(result, env)
				break
			}
		}
	}
	return result
}

// SafeEnv 返回经白名单过滤后的环境变量，供 exec.Command 使用。
// 对标同类 CLI spawn.rs env_clear + envs。
// extra 用于追加业务所需变量（前缀或精确名）。
func SafeEnv(extra ...string) []string {
	return ScrubEnvWith(os.Environ(), extra)
}
