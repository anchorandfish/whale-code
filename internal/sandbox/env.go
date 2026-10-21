// Package sandbox — 环境变量隔离（C15/B9）。
package sandbox

import (
	"github.com/anchorandfish/whale-code/internal/whalehome"
	"os"
	"path/filepath"
	"runtime"
	"strings"
)

// DefaultSensitivePathPatterns 是默认的敏感目录过滤模式列表。
//
// PATH 中包含这些模式的条目会被 FilterPathEntries 移除，防止沙箱子进程
// 通过 PATH 中的可执行文件访问到敏感目录（如 ~/.ssh 下的密钥、
// ~/.aws 下的凭证、~/.gnupg 下的 GPG 密钥、~/.config 下的应用配置）。
//
// 跨平台：使用小写匹配，对路径分隔符不敏感。
// 可通过 EnvConfig.Set 显式覆盖 PATH 来绕过过滤。
var DefaultSensitivePathPatterns = []string{
	".ssh",
	".gnupg",
	".aws",
	".docker",
	".kube",
}

// FilterPathEntries 过滤 PATH 环境变量的条目，移除包含敏感目录模式的条目。
//
// 行为：
// 1. 按 os.PathListSeparator 分割 PATH（Windows 用 ';'，Linux/macOS 用 ':'）
// 2. 对每个条目，检查是否包含 sensitivePatterns 中的任一模式（小写不敏感）
// 3. 命中则跳过，否则保留
// 4. 重新拼接为过滤后的 PATH 字符串
//
// 入参：
// - pathValue: 原始 PATH 值（如 "/usr/bin:/home/user/.ssh/bin:/bin"）
// - sensitivePatterns: 敏感目录模式列表（如 [".ssh", ".aws"]）；为 nil 时使用 DefaultSensitivePathPatterns
//
// 返回：过滤后的 PATH（如 "/usr/bin:/bin"）
//
// 跨平台兼容：使用 os.PathListSeparator 自动适配 Windows/Unix。
func FilterPathEntries(pathValue string, sensitivePatterns []string) string {
	if pathValue == "" {
		return ""
	}
	if sensitivePatterns == nil {
		sensitivePatterns = DefaultSensitivePathPatterns
	}

	// 预计算小写模式，避免重复 ToLower
	lowerPatterns := make([]string, len(sensitivePatterns))
	for i, p := range sensitivePatterns {
		lowerPatterns[i] = strings.ToLower(p)
	}

	entries := filepath.SplitList(pathValue)
	filtered := make([]string, 0, len(entries))
	for _, entry := range entries {
		if entry == "" {
			continue
		}
		if containsSensitiveSegment(entry, lowerPatterns) {
			continue
		}
		filtered = append(filtered, entry)
	}

	return strings.Join(filtered, string(os.PathListSeparator))
}

// containsSensitiveSegment 检查路径中是否包含精确匹配敏感模式的目录段。
// 精确匹配路径分隔符分割的每一段，避免子串误杀合法路径。
// 例如 /home/user/.ssh/bin 包含 .ssh 段 → true
// 但 /home/user/.sshell-tools/bin 不含 .ssh 段 → false
func containsSensitiveSegment(path string, lowerPatterns []string) bool {
	cleaned := strings.ToLower(filepath.Clean(path))
	for _, seg := range strings.Split(cleaned, string(filepath.Separator)) {
		for _, pattern := range lowerPatterns {
			if seg == pattern {
				return true
			}
		}
	}
	return false
}

// BuildEnv 根据 EnvConfig 从宿主环境构造沙箱内安全的环境变量列表。
//
// 顺序：
// 1. 若 cfg 为 nil，使用 DefaultEnvConfig()。
// 2. 遍历宿主 os.Environ()，仅保留 AllowList 命中的变量。
// 3. 剔除命中 DenyList（glob）的变量（即使已在 AllowList 中）。
// 4. 对 PATH 变量值应用 FilterPathEntries，移除敏感目录条目。
// 5. 叠加 cfg.Set 中的覆盖/新增项（Set 中的 PATH 不再过滤，调用方全权控制）。
//
// 返回的列表可直接赋值给 exec.Cmd.Env。
// envNameMatches 报告环境变量名是否命中白名单项（v1 门 4，Win10 真机教训）。
//
// Windows 环境变量名大小写不敏感（进程块中为 "Path" 而非 "PATH"），
// foldCase=true 时用 EqualFold 精确匹配（非前缀/子串）；Unix 平台保持
// 大小写敏感精确匹配（"path" 不命中 "PATH"，fail-closed 剔除）。
func envNameMatches(name, allowed string, foldCase bool) bool {
	if foldCase {
		return strings.EqualFold(name, allowed)
	}
	return name == allowed
}

// envFoldCase 报告当前平台环境变量名匹配是否大小写不敏感。
var envFoldCase = runtime.GOOS == "windows"

// isPathEnvName 报告 name 是否为 PATH 变量（Windows 进程块中为 "Path"）。
func isPathEnvName(name string) bool {
	return envNameMatches(name, "PATH", envFoldCase)
}

// envNameAllowed 报告 name 是否命中任一白名单项（平台感知大小写策略）。
func envNameAllowed(name string, allowList []string) bool {
	for _, a := range allowList {
		if envNameMatches(name, a, envFoldCase) {
			return true
		}
	}
	return false
}

func BuildEnv(cfg *EnvConfig) []string {
	if cfg == nil {
		cfg = DefaultEnvConfig()
	}
	denyGlobs := cfg.DenyList

	var env []string
	for _, kv := range os.Environ() {
		name, value, ok := strings.Cut(kv, "=")
		if !ok {
			continue
		}
		// 白名单过滤：AllowList 非空时必须命中（Windows 大小写不敏感）
		if len(cfg.AllowList) > 0 && !envNameAllowed(name, cfg.AllowList) {
			continue
		}
		// 黑名单过滤：命中 DenyList glob 则剔除
		if matchAnyGlob(name, denyGlobs) {
			continue
		}
		// PATH 值过滤：移除包含敏感目录的条目（如 ~/.ssh/bin）
		if isPathEnvName(name) {
			value = FilterPathEntries(value, nil)
		}
		env = append(env, name+"="+value)
	}

	// 叠加 Set（调用方显式覆盖，不再过滤）
	for k, v := range cfg.Set {
		env = append(env, k+"="+v)
	}
	return env
}

// tempDirEnvNames 返回"临时目录"变量名（平台差异：Windows 用 TEMP/TMP）。
//
// 与 DefaultEnvConfig 的 Windows 组对齐：Windows 进程块里 TEMP/TMP 是标准
// 临时目录变量，Unix 侧只认 TMPDIR。
func tempDirEnvNames() []string {
	if runtime.GOOS == "windows" {
		return []string{"TMPDIR", "TEMP", "TMP"}
	}
	return []string{"TMPDIR"}
}

// injectTempDir 把**沙箱自建**临时目录注入子进程 env。
//
// 为什么必须注入：子进程 env 由 BuildEnv 按 AllowList 白名单构造，而 TMPDIR
// 不在默认名单里 ⇒ 被剥掉 ⇒ 需要临时目录的命令（go build / go test / 多数构建
// 工具）退回 `/tmp`；而 `/tmp` 既不在 A4 的应用层放行面、也不在 macOS seatbelt
// 可写根内 ⇒ 命令直接失败（实测 `mkdir /tmp/go-build…: operation not permitted`）。
//
// 为什么指向 tempDir 而不是继承父进程 TMPDIR：
// 1. tempDir 由 Prepare 在**工作区内**建（`os.MkdirTemp(absWorkDir, "sandbox-tmp-*")`）
// ⇒ 已被 seatbelt/landlock profile 放行，不会撞 A4 的越界判定；
// 2. 生命周期与子进程一致（Prepare 返回的 cleanup() 负责 RemoveAll）；
// 3. 不依赖父进程是否设置 TMPDIR（CI / daemon 下常见为空）。
//
// 优先级：`cfg.Set` 中显式给出的同名键最高（延续 BuildEnv 的既有契约
// 「Set 中的值调用方全权控制」）—— 显式设置时本函数不覆盖。
//
// 幂等：同名键已存在则**原地替换**，绝不追加重复键（重复键在 exec.Cmd.Env
// 里的生效顺序依赖平台解析，会把"哪个生效"变成隐式约定）。
func injectTempDir(env []string, cfg *EnvConfig, tempDir string) []string {
	if tempDir == "" {
		return env
	}
	for _, name := range tempDirEnvNames() {
		if cfgSetsEnvName(cfg, name) {
			continue
		}
		env = upsertEnv(env, name, tempDir)
	}
	return env
}

// cfgSetsEnvName 报告 cfg.Set 是否显式设置了 name（平台感知大小写策略）。
func cfgSetsEnvName(cfg *EnvConfig, name string) bool {
	if cfg == nil {
		return false
	}
	for k := range cfg.Set {
		if envNameMatches(k, name, envFoldCase) {
			return true
		}
	}
	return false
}

// upsertEnv 在 env 中设置 name=value：已存在则替换，否则追加。
func upsertEnv(env []string, name, value string) []string {
	for i, kv := range env {
		k, _, ok := strings.Cut(kv, "=")
		if !ok {
			continue
		}
		if envNameMatches(k, name, envFoldCase) {
			env[i] = name + "=" + value
			return env
		}
	}
	return append(env, name+"="+value)
}

// SandboxDirs 返回沙箱默认白名单应包含的 whale-code 数据目录（C17）。
// 调用方可将这些路径加入 SandboxConfig.AllowRead/AllowWrite。
func SandboxDirs() []string {
	// 收口：状态目录唯一走 whalehome.Dir()；home 不可得时 Dir() 返回空 ⇒ 返回 nil（行为同前）。
	base := whalehome.Dir()
	if base == "" {
		return nil
	}
	return []string{
		base,
		filepath.Join(base, "sessions"),
		filepath.Join(base, "cache"),
		filepath.Join(base, "logs"),
	}
}

// matchAnyGlob 用 shell glob 匹配变量名是否命中任一模式（* 匹配任意序列）。
func matchAnyGlob(name string, patterns []string) bool {
	for _, p := range patterns {
		if globMatch(p, name) {
			return true
		}
	}
	return false
}

// globMatch 简单 shell glob：仅支持 *（任意序列）。大小写敏感。
func globMatch(pattern, name string) bool {
	pi, ni := 0, 0
	star := -1
	starNi := 0
	for ni < len(name) {
		if pi < len(pattern) && (pattern[pi] == name[ni]) {
			pi++
			ni++
		} else if pi < len(pattern) && pattern[pi] == '*' {
			star = pi
			starNi = ni
			pi++
		} else if star != -1 {
			pi = star + 1
			starNi++
			ni = starNi
		} else {
			return false
		}
	}
	for pi < len(pattern) && pattern[pi] == '*' {
		pi++
	}
	return pi == len(pattern)
}
