// Package lsp — LSP 配置文件 schema 与加载器（T1-P1-A2 /）
//
// 配置文件位置：~/.whale/lsp.json（用户级，与 settings.json 同级）
// schema 字段：
// - version: 配置 schema 版本（当前 1）
// - servers: 服务器列表（name/command/args/filetypes/rootUri/env）
// - recovery: 崩溃恢复策略（maxRestarts/backoffDelayMs/healthCheckMs）
//
// 降级策略：
// - 配置文件不存在 → DefaultLSPConfig()（与现有硬编码一致）
// - 配置文件解析失败 → 日志告警 + DefaultLSPConfig()
// - 配置项缺字段 → 字段补默认值
//
// 对标 VS Code settings.json 声明式 schema：
// - 官方源：https://code.visualstudio.com/docs/configure/settings（访问日期 2026-07-21）
// - VS Code 通过扩展 manifest 间接配置，无独立 lsp.json；whale-code 改进为单文件独立配置
package lsp

import (
	"encoding/json"
	"fmt"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync/atomic"

	"github.com/anchorandfish/whale-code/internal/platform"
)

// CurrentLSPConfigVersion LSP 配置 schema 当前版本
// 升级 schema 时递增，旧版本配置由 migrateLSPConfig 迁移（吸纳优化 3）
const CurrentLSPConfigVersion = 1

// LSPConfig LSP 配置 schema（声明式）
// §3.1.2 SNAPSHOT-
type LSPConfig struct {
	Version  int            `json:"version"`  // 配置 schema 版本，默认 1
	Servers  []ServerConfig `json:"servers"`  // 服务器列表
	Recovery RecoveryConfig `json:"recovery"` // 崩溃恢复策略
}

// ServerConfig 单个语言服务器配置
type ServerConfig struct {
	Name      string   `json:"name"`              // 配置项名称（如 "go-gopls"）
	Command   string   `json:"command"`           // 可执行文件名或绝对路径
	Args      []string `json:"args,omitempty"`    // 启动参数（如 ["serve"]）
	Filetypes []string `json:"filetypes"`         // 关联文件扩展名（如 [".go"]）
	RootURI   string   `json:"rootUri,omitempty"` // 强制 rootUri（空则用 workspaceRoot）
	Env       []string `json:"env,omitempty"`     // 环境变量（"KEY=VALUE" 格式）
}

// RecoveryConfig 崩溃恢复策略
type RecoveryConfig struct {
	MaxRestarts    int `json:"maxRestarts"`             // 最大重启次数（默认 5，-1=无限）
	BackoffDelayMs int `json:"backoffDelayMs"`          // 退避基础延迟（默认 1000ms，指数退避）
	HealthCheckMs  int `json:"healthCheckMs,omitempty"` // 健康检查间隔（默认 0=禁用，仅 EOF 触发，吸纳优化 2）
}

// defaultLSPConfigOnce 缓存 DefaultLSPConfig 结果（编译期常量性质，进程级缓存）
var defaultLSPConfigOnce atomic.Pointer[LSPConfig]

// DefaultLSPConfig 返回内置硬编码配置（保持与现有 findLanguageServer 一致）
// 6 语言 + 7 个候选服务器映射（Python 含 pylsp + pyright 2 候选），recovery 默认 maxRestarts=5 + 1s 退避
// §3.1.1
func DefaultLSPConfig() *LSPConfig {
	if cached := defaultLSPConfigOnce.Load(); cached != nil {
		return cached
	}
	cfg := &LSPConfig{
		Version: CurrentLSPConfigVersion,
		Servers: []ServerConfig{
			{Name: "go-gopls", Command: "gopls", Args: []string{"serve"}, Filetypes: []string{".go"}},
			{Name: "python-pylsp", Command: "pylsp", Filetypes: []string{".py"}},
			{Name: "python-pyright", Command: "pyright-langserver", Args: []string{"--stdio"}, Filetypes: []string{".py"}},
			{Name: "typescript-tsserver", Command: "typescript-language-server", Args: []string{"--stdio"}, Filetypes: []string{".ts", ".tsx", ".js", ".jsx"}},
			{Name: "rust-analyzer", Command: "rust-analyzer", Filetypes: []string{".rs"}},
			{Name: "c-clangd", Command: "clangd", Filetypes: []string{".c", ".h"}},
			{Name: "cpp-clangd", Command: "clangd", Filetypes: []string{".cpp", ".cc", ".cxx", ".hpp"}},
		},
		Recovery: RecoveryConfig{
			MaxRestarts:    5,
			BackoffDelayMs: 1000,
			HealthCheckMs:  0, // 默认禁用，仅 EOF 触发（吸纳优化 2：Windows 兼容）
		},
	}
	defaultLSPConfigOnce.Store(cfg)
	return cfg
}

// LoadLSPConfig 加载 ~/.whale/lsp.json，缺字段降级到 DefaultLSPConfig
// 降级路径：
// - 路径为空 → DefaultLSPConfig + nil
// - 路径展开失败 → DefaultLSPConfig + error
// - 文件不存在 → DefaultLSPConfig + nil（正常降级，不告警）
// - 文件解析失败 → DefaultLSPConfig + 日志告警 + nil（不阻塞启动）
// - 配置版本过期 → migrateLSPConfig 迁移
// - Validate 失败 → DefaultLSPConfig + 日志告警 + nil
//
// §3.1.1
func LoadLSPConfig(path string) (*LSPConfig, error) {
	if path == "" {
		return DefaultLSPConfig(), nil
	}

	expanded, err := platform.ExpandTilde(path)
	if err != nil {
		log.Printf("[lsp] expand tilde failed for %s: %v, fallback to default", path, err)
		return DefaultLSPConfig(), fmt.Errorf("expand tilde: %w", err)
	}

	absPath, err := filepath.Abs(expanded)
	if err != nil {
		log.Printf("[lsp] abs path failed for %s: %v, fallback to default", expanded, err)
		return DefaultLSPConfig(), fmt.Errorf("abs path: %w", err)
	}

	data, err := os.ReadFile(absPath)
	if err != nil {
		if os.IsNotExist(err) {
			// 文件不存在是正常降级路径，不输出告警
			return DefaultLSPConfig(), nil
		}
		log.Printf("[lsp] read config failed %s: %v, fallback to default", absPath, err)
		return DefaultLSPConfig(), nil
	}

	var cfg LSPConfig
	if err := json.Unmarshal(data, &cfg); err != nil {
		log.Printf("[lsp] parse config failed %s: %v, fallback to default", absPath, err)
		return DefaultLSPConfig(), nil
	}

	// 版本迁移
	if cfg.Version < CurrentLSPConfigVersion {
		if err := migrateLSPConfig(&cfg); err != nil {
			log.Printf("[lsp] migrate config failed: %v, fallback to default", err)
			return DefaultLSPConfig(), nil
		}
	}

	// 字段补默认值
	applyRecoveryDefaults(&cfg.Recovery)

	// Validate
	if errs := cfg.Validate(); len(errs) > 0 {
		log.Printf("[lsp] config validation errors: %v, fallback to default", errs)
		return DefaultLSPConfig(), nil
	}

	return &cfg, nil
}

// applyRecoveryDefaults 为 RecoveryConfig 缺省字段补默认值
// BackoffDelayMs=0 → 1000；MaxRestarts=0 → 5；HealthCheckMs 负数 → 0
// 零值（未设置 recovery 段）使用 DefaultLSPConfig().Recovery 整段
func applyRecoveryDefaults(r *RecoveryConfig) {
	def := DefaultLSPConfig().Recovery
	if r.MaxRestarts == 0 && r.BackoffDelayMs == 0 && r.HealthCheckMs == 0 {
		// 整段缺省，用默认值
		*r = def
		return
	}
	if r.BackoffDelayMs == 0 {
		r.BackoffDelayMs = def.BackoffDelayMs
	}
	if r.MaxRestarts == 0 {
		r.MaxRestarts = def.MaxRestarts
	}
	if r.HealthCheckMs < 0 {
		r.HealthCheckMs = 0
	}
}

// Validate 校验 LSPConfig 字段合法性
// 返回错误列表（空表示通过）
// 检查项：name 非空且不重复、command 非空、filetypes 非空、recovery 数值合法
// §3.1.1
func (c *LSPConfig) Validate() []error {
	var errs []error
	seen := make(map[string]bool)
	for i, s := range c.Servers {
		if s.Name == "" {
			errs = append(errs, fmt.Errorf("servers[%d].name is empty", i))
		} else if seen[s.Name] {
			errs = append(errs, fmt.Errorf("servers[%d].name %s duplicated", i, s.Name))
		} else {
			seen[s.Name] = true
		}
		if s.Command == "" {
			errs = append(errs, fmt.Errorf("servers[%d].command is empty", i))
		}
		if len(s.Filetypes) == 0 {
			errs = append(errs, fmt.Errorf("servers[%d].filetypes is empty", i))
		}
	}
	if c.Recovery.MaxRestarts < -1 {
		errs = append(errs, fmt.Errorf("recovery.maxRestarts invalid: %d (must be -1 or >=0)", c.Recovery.MaxRestarts))
	}
	if c.Recovery.BackoffDelayMs < 0 {
		errs = append(errs, fmt.Errorf("recovery.backoffDelayMs invalid: %d (must be >=0)", c.Recovery.BackoffDelayMs))
	}
	if c.Recovery.HealthCheckMs < 0 {
		errs = append(errs, fmt.Errorf("recovery.healthCheckMs invalid: %d (must be >=0)", c.Recovery.HealthCheckMs))
	}
	return errs
}

// migrateLSPConfig 迁移旧版本配置到当前版本
// 当前 v1 → v1 是 noop，预留扩展点（吸纳优化 3）
// 未来 v2 迁移逻辑写在这里
func migrateLSPConfig(cfg *LSPConfig) error {
	if cfg.Version < 1 {
		cfg.Version = 1
	}
	return nil
}

// FindServersByFiletype 根据文件扩展名查找匹配的服务器配置
// 返回所有匹配的 ServerConfig（多个候选时调用方按顺序 LookPath）
// 扩展名规范化为小写带点前缀（如 ".go"）
// §3.1.1
func (c *LSPConfig) FindServersByFiletype(ext string) []ServerConfig {
	var result []ServerConfig
	normExt := normalizeExtension(ext)
	if normExt == "" {
		return nil
	}
	for _, s := range c.Servers {
		for _, ft := range s.Filetypes {
			if normalizeExtension(ft) == normExt {
				result = append(result, s)
				break
			}
		}
	}
	return result
}

// normalizeExtension 规范化扩展名为小写带点前缀（如 ".go"）
// 输入 "go" / ".go" / ".GO" / "GO" 均返回 ".go"
func normalizeExtension(ext string) string {
	if ext == "" {
		return ""
	}
	if ext[0] != '.' {
		ext = "." + ext
	}
	return strings.ToLower(ext)
}

// languageToExtension 语言名到主扩展名（用于配置查找）
// 与 DetectLanguage 反向映射，仅返回主扩展名
// 不在表中的语言返回空字符串
func languageToExtension(language string) string {
	switch strings.ToLower(language) {
	case "go":
		return ".go"
	case "python":
		return ".py"
	case "typescript":
		return ".ts"
	case "rust":
		return ".rs"
	case "c":
		return ".c"
	case "cpp":
		return ".cpp"
	default:
		return ""
	}
}

// AnyLanguageServerAvailable 能力探针（· W5）——判断「是否至少有一个语言服务器在 PATH 上」。
//
// 为什么判据是"任一可用"而不是"全部可用"：LSP 能力位是**运行期 + per-language** 的
// （同一会话里 .go 可用、.py 不可用是常态），启动期**全局**谓词会两向都错 ——
// 装了 gopls 没装 pyright 时，按"全部可用"隐藏会误伤 Go，按"按语言逐判"则要改成运行期判据。
// 所以本探针只回答一个保守、且与"零 server 一定 100% 不可用"这一事实严格对齐的问题：
// **一个都没有**才返回 false（⇒ 工具面整体隐藏，无误伤）；**有任一个**就返回 true
// （⇒ 保留工具，per-language 可用性仍由运行期 getLSPClient 判定，无假承诺）。
//
// 判据与 findServerFromConfig 同源：都是 exec.LookPath(cmd) 是否命中，只是前者按语言、
// 本探针跨全部 server。cfg 为 nil 时降级 DefaultLSPConfig（与 NewClientWithConfig 一致）。
func AnyLanguageServerAvailable(cfg *LSPConfig) bool {
	return anyLanguageServerAvailableFrom(cfg, exec.LookPath)
}

// anyLanguageServerAvailableFrom 是可注入形态：判据用固定的 lookPath 驱动、不依赖进程真实 PATH
// （否则判据会随开发机是否装了 gopls 而飘，属"环境相关假绿"—— 见 webSearchChannelFrom 同款理由）。
func anyLanguageServerAvailableFrom(cfg *LSPConfig, lookPath func(string) (string, error)) bool {
	if cfg == nil {
		cfg = DefaultLSPConfig()
	}
	for _, s := range cfg.Servers {
		if s.Command == "" {
			continue
		}
		if _, err := lookPath(s.Command); err == nil {
			return true
		}
	}
	return false
}

// findServerFromConfig 从 LSPConfig 查找语言对应的服务器
// 多个候选时按顺序 LookPath，找到第一个可用的返回
// 找不到时返回 error（调用方决定降级策略）
// 与现有 findLanguageServer 不同：返回 (cmd, args, error) 三元组，支持自定义 args
// 错误消息保持与 findLanguageServer 一致（"no language server found for X"）以兼容现有文档与工具
// §3.1.1
func findServerFromConfig(cfg *LSPConfig, language string) (cmd string, args []string, err error) {
	if cfg == nil {
		cfg = DefaultLSPConfig()
	}
	lang := strings.ToLower(language)
	ext := languageToExtension(lang)
	if ext == "" {
		return "", nil, fmt.Errorf("no language server found for %s", language)
	}

	candidates := cfg.FindServersByFiletype(ext)
	if len(candidates) == 0 {
		return "", nil, fmt.Errorf("no language server found for %s", language)
	}

	for _, s := range candidates {
		if p, lookErr := exec.LookPath(s.Command); lookErr == nil {
			return p, s.Args, nil
		}
	}
	return "", nil, fmt.Errorf("no language server found for %s", language)
}
