// Package sandbox — 沙箱共享类型定义
package sandbox

import (
	"context"
	"fmt"
	"os/exec"
	"strings"

	"github.com/anchorandfish/whale-code/internal/security/failtier"
)

type SandboxLevel int

const (
	SandboxLevelDisabled SandboxLevel = iota
	SandboxLevelRelaxed
	SandboxLevelStrict
	SandboxLevelIsolated
)

func (l SandboxLevel) String() string {
	switch l {
	case SandboxLevelDisabled:
		return "disabled"
	case SandboxLevelRelaxed:
		return "relaxed"
	case SandboxLevelStrict:
		return "strict"
	case SandboxLevelIsolated:
		return "isolated"
	default:
		return fmt.Sprintf("unknown(%d)", int(l))
	}
}

func ParseSandboxMode(s string) (SandboxLevel, error) {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "disabled", "off", "none", "":
		return SandboxLevelDisabled, nil
	case "relaxed":
		return SandboxLevelRelaxed, nil
	case "strict":
		return SandboxLevelStrict, nil
	case "isolated":
		return SandboxLevelIsolated, nil
	default:
		return SandboxLevelDisabled, fmt.Errorf("unknown sandbox level: %q (expected disabled/relaxed/strict/isolated)", s)
	}
}

func Decrement(level SandboxLevel) SandboxLevel {
	if level <= SandboxLevelDisabled {
		return SandboxLevelDisabled
	}
	return level - 1
}

type SandboxConfig struct {
	WorkDir        string
	AllowRead      []string
	AllowWrite     []string
	AllowCommands  []string
	DenyCommands   []string
	NetworkMode    string
	EnvConfig      *EnvConfig
	OSLevel        OSLevelConfig
	ResourceLimits ResourceLimits
}

type EnvConfig struct {
	AllowList []string
	DenyList  []string
	Set       map[string]string
}

func DefaultEnvConfig() *EnvConfig {
	return &EnvConfig{
		AllowList: []string{
			"PATH", "HOME", "USER", "LANG", "LC_ALL", "TERM",
			// Windows 必需变量（Win10 真机 E2E 首跑教训：缺失导致沙箱内
			// 命令无 PATH/SystemRoot/TEMP 可用，BuildEnv 产出空 env）。
			// Windows 环境变量名大小写不敏感（进程块中为 "Path"），
			// 匹配经 envNameMatches 的 EqualFold 处理，此处统一大写。
			"TEMP", "TMP", "SYSTEMROOT", "SYSTEMDRIVE", "WINDIR",
			"USERNAME", "USERPROFILE", "HOMEDRIVE", "HOMEPATH",
			"COMSPEC", "PATHEXT", "NUMBER_OF_PROCESSORS", "PROCESSOR_ARCHITECTURE",
			"LOCALAPPDATA", "APPDATA", "PROGRAMFILES", "PROGRAMDATA",
		},
		DenyList: []string{
			"*_API_KEY", "*_TOKEN", "*_SECRET",
			"AWS_*", "OPENAI_*", "ANTHROPIC_*",
		},
		Set: map[string]string{},
	}
}

// UnsandboxedEnvConfig 返回"去沙箱"场景的环境变量配置（，2026-10-10）。
//
// 与 DefaultEnvConfig 的**唯一**区别：AllowList 为空 —— 不按白名单收窄，
// 保留宿主全部变量（HTTPS_PROXY / NODE_PATH / GOPATH / SSH_AUTH_SOCK / 语言运行时
// 路径…），只按 DenyList 剔除密钥类变量。
//
// 为什么需要它：MCP server 去沙箱后仍要守住"最小安全底线"——不让宿主密钥泄漏给
// 第三方 server —— 但**不能**复用 DefaultEnvConfig 的窄白名单，否则会把联网/构建
// 所必需的环境变量一并掐掉，变成"去沙箱了还是跑不起来"（这正是 的痛点）。
//
// DenyList 单一事实源：直接取 DefaultEnvConfig() 的 DenyList（同一个切片），
// 保证"剥密钥"规则永远与沙箱路径一致，杜绝两处各抄一份后漂移。
//
// 注：经 BuildEnv 构造时，PATH 仍会走 FilterPathEntries（移除含 .ssh/.aws/… 段的条目），
// 这是同一入口的既有行为，对去沙箱场景属于可接受的纵深防御。
func UnsandboxedEnvConfig() *EnvConfig {
	def := DefaultEnvConfig()
	return &EnvConfig{
		AllowList: nil, // nil/空 = 不按白名单收窄，保留宿主全部变量
		DenyList:  def.DenyList,
		Set:       map[string]string{},
	}
}

type ValidationResult struct {
	Blocked    bool
	Violations []SecurityViolation
	Level      SandboxLevel
}

type SecurityViolation struct {
	CheckID  string
	Message  string
	Severity int
}

const (
	SeverityLow      = 0
	SeverityMedium   = 1
	SeverityHigh     = 2
	SeverityCritical = 3
)

type SandboxContext struct {
	WorkDir        string
	TempDir        string
	Env            []string
	CancelFunc     func()
	PlatformHandle interface{}
	PostStartHook  func(*exec.Cmd) error
	ResourceLimits ResourceLimits

	// Deficiencies 本次 Prepare 的族A 降级事实（结构化出口，A5）。
	//
	// 空 = 保护链完整生效。非空 = 某环节能力缺失、已按族A 降级（当前唯一来源：
	// Strict/Relaxed 级 OSSandbox.Enter 失败 ⇒ T1，命令在无 OS 隔离下继续，
	// 调用方据此收紧姿态）。**不得**改成读审计日志/散文关键词反推（R4 收口同源）。
	Deficiencies []failtier.Deficiency

	_level     SandboxLevel
	_ctx       context.Context
	_osSandbox OSSandbox
}

func (sc SandboxContext) Context() context.Context {
	if sc._ctx != nil {
		return sc._ctx
	}
	return context.Background()
}

func (sc SandboxContext) EffectiveLevel() SandboxLevel {
	return sc._level
}

func (sc SandboxContext) OSSandbox() OSSandbox {
	return sc._osSandbox
}

func NewSandboxContextForTesting(workDir string, env []string, os OSSandbox) SandboxContext {
	return SandboxContext{
		WorkDir:    workDir,
		Env:        env,
		_osSandbox: os,
	}
}
