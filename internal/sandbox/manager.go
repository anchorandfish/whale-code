// Package sandbox — 沙箱管理器接口与实现
//
// 本文件是 C1 冲突解决的权威定义方。Manager 接口采用"Prepare + Validate"两段式设计：
// - Prepare 准备沙箱上下文（隔离环境生效），返回 SandboxContext + cleanup
// - ValidateCommand/ValidatePath 应用层验证（不执行命令）
//
// B8：ValidateCommand 返回 (*ValidationResult, error)，含 Blocked 字段（C13 分阈值策略）。
// B11：determineLevel 返回 error，不再 panic。
//
// Tools 模块的 Orchestrator 调用流程：
// 1. sandboxMgr.Prepare(ctx, level, workDir) → SandboxContext + cleanup
// 2. defer cleanup()
// 3. 在 SandboxContext 约束下执行命令（exec.Cmd 设置 SysProcAttr + Env）
//
// 沙箱不执行命令，只准备隔离环境；命令执行始终在 Tools 层。
package sandbox

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"

	"github.com/anchorandfish/whale-code/internal/sandbox/validator"
	"github.com/anchorandfish/whale-code/internal/security/failtier"
)

// Manager 是沙箱管理器接口。
//
// C1 冲突解决：sandbox 模块权威定义，tools/mcp 模块引用。
// 采用两段式接口（Prepare + Validate），避免双重执行。
//
// 并发安全：实现必须支持多个 goroutine 同时调用 Prepare/Validate。
type Manager interface {
	// Prepare 准备沙箱上下文（文件系统隔离、网络隔离、资源限制生效）。
	//
	// 前置条件：
	// - level != SandboxLevelDisabled（Disabled 时应跳过调用 Prepare）
	// - workDir 存在且可访问
	//
	// 后置条件：
	// - 返回的 SandboxContext 已完成所有隔离设置
	// - cleanup 函数用于释放沙箱资源（必须在命令执行完毕后调用）
	//
	// 错误语义：
	// - 平台不支持请求的 level：返回 error，调用方应 Decrement 后重试
	// - workDir 不存在：返回 error
	// - 资源不足：返回 error
	Prepare(ctx context.Context, level SandboxLevel, workDir string) (SandboxContext, func(), error)

	// ValidateCommand 应用层命令验证（不执行命令）。
	//
	// 返回 *ValidationResult（含 Blocked 字段，C13/B8）：
	// - Blocked=true：命令命中 Critical/High 级检查或黑名单，应阻止执行
	// - Blocked=false 但 Violations 非空：仅记录（Medium/Low 级）
	// 返回的 error 仅用于系统级错误（非命令违规）。
	ValidateCommand(ctx context.Context, command string, config *SandboxConfig) (*ValidationResult, error)

	// ValidatePath 应用层路径验证。
	//
	// 后置条件：返回 nil 表示路径在允许范围内。
	ValidatePath(ctx context.Context, path string, config *SandboxConfig) error

	// Level 返回管理器配置的默认沙箱级别。
	Level() SandboxLevel
}

// manager 是 Manager 接口的具体实现。
//
// P0 阶段不接入 OS 级隔离（Landlock/Seccomp/Cgroup/Job Object 留待 Phase C），
// Prepare 仅完成：工作目录校验、临时目录创建、环境变量隔离（C15）。
// 命令/路径校验由 validator 子包 + 内置黑名单完成。
//
// SNAPSHOT：新增 osSandbox 字段（OS 级沙箱实例）。
// Prepare 调用 OSSandbox.Enter 填充 PlatformHandle；cfg.OSLevel 零值时降级到 degradedSandbox。
type manager struct {
	mu          sync.RWMutex
	config      *SandboxConfig
	level       SandboxLevel
	failHard    bool // FailIfUnavailable：true 时无 OS 沙箱则 determineLevel 返回 error
	validator   *validator.Validator
	pathExtract *validator.PathExtractor
	audit       *AuditLog
	osSandbox   OSSandbox // SNAPSHOT：OS 级沙箱实例（NewManager 时创建，nil 不应发生）
}

// ManagerOption 配置 Manager 的可选参数。
type ManagerOption func(*manager)

// WithFailHard 设置 FailIfUnavailable：无 OS 沙箱时 determineLevel 返回 error（而非降级）。
func WithFailHard() ManagerOption {
	return func(m *manager) { m.failHard = true }
}

// WithAuditLog 注入审计日志实例（默认自动创建）。
func WithAuditLog(a *AuditLog) ManagerOption {
	return func(m *manager) { m.audit = a }
}

// NewManager 创建沙箱管理器。
//
// cfg 为 nil 时使用零值配置；level 为管理器默认沙箱级别。
// 返回 Manager 接口，供 tools/mcp 模块持有。
//
// 初始化 osSandbox 字段（根据 cfg.OSLevel 选择平台实现）。
// cfg.OSLevel 零值时返回 degradedSandbox（与现状一致）。
//
// 门 3 网络开关归一化：AllowNetwork=true 时过滤 Linux "net" namespace。
// 在此单一入口归一化，cloneflags（Enter）/ status banner /
// describeIsolationFeatures 全部读归一化后的配置，无第二处开关逻辑。
// 注：会就地更新 cfg.OSLevel.Linux.Namespaces（生产工厂独占 cfg，无别名风险）。
func NewManager(cfg *SandboxConfig, level SandboxLevel, opts ...ManagerOption) Manager {
	if cfg == nil {
		cfg = &SandboxConfig{}
	}
	if cfg.OSLevel.Linux.AllowNetwork {
		cfg.OSLevel.Linux.Namespaces = filterNetNamespace(cfg.OSLevel.Linux.Namespaces)
	}
	m := &manager{
		config:      cfg,
		level:       level,
		validator:   validator.NewValidator(),
		pathExtract: validator.NewPathExtractor(),
		audit:       NewAuditLog(),
	}
	for _, opt := range opts {
		opt(m)
	}
	// 创建平台沙箱实例（根据 OSLevelConfig 选择实现）
	m.osSandbox = newPlatformSandbox(cfg.OSLevel, m.audit)
	return m
}

// Level 返回默认沙箱级别。
func (m *manager) Level() SandboxLevel {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.level
}

// determineLevel 确定实际可用的隔离级别（B11：返回 error，不 panic）。
//
// P0 阶段无 OS 级沙箱，恒降级到应用层验证。
// 若 failHard=true，则返回 error 表明"要求 OS 沙箱但不可用"。
func (m *manager) determineLevel(req SandboxLevel) (SandboxLevel, error) {
	if m.failHard {
		// P0 无 OS 沙箱可用；failHard 模式下视为不可用
		return SandboxLevelDisabled, errors.New("sandbox: OS-level sandbox required but unavailable (P0 app-only build)")
	}
	// 降级到应用层验证：保留请求级别用于配置意图，实际隔离由应用层完成
	return req, nil
}

// Prepare 准备沙箱上下文。
//
// 增强：调用 OSSandbox.Enter 填充 PlatformHandle + 设置 ctx._osSandbox。
// Enter 失败时不返回错误（降级到 nil 句柄，与现状一致），仅记 LogDegradation。
func (m *manager) Prepare(ctx context.Context, level SandboxLevel, workDir string) (SandboxContext, func(), error) {
	if level == SandboxLevelDisabled {
		return SandboxContext{}, nil, errors.New("sandbox: Prepare called with SandboxLevelDisabled (should skip Prepare)")
	}
	if workDir == "" {
		return SandboxContext{}, nil, errors.New("sandbox: workDir is empty")
	}
	absWorkDir, err := filepath.Abs(workDir)
	if err != nil {
		return SandboxContext{}, nil, fmt.Errorf("sandbox: resolve workDir: %w", err)
	}
	if info, err := os.Stat(absWorkDir); err != nil || !info.IsDir() {
		return SandboxContext{}, nil, fmt.Errorf("sandbox: workDir not accessible: %s", absWorkDir)
	}

	actualLevel, err := m.determineLevel(level)
	if err != nil {
		return SandboxContext{}, nil, err
	}

	// 临时目录：优先在工作目录下创建 .sandbox-tmp，便于清理与白名单覆盖
	tempDir, err := os.MkdirTemp(absWorkDir, "sandbox-tmp-*")
	if err != nil {
		// 降级到系统临时目录 —— 但必须落在**沙箱自建 tmp 根**下（A4）：
		// 否则该临时目录自身会被 ValidatePath 判为"工作区之外"（A4 收窄了 /tmp 放行）。
		if ensureSandboxTempRoot() {
			tempDir, err = os.MkdirTemp(SandboxTempRoot(), "sandbox-tmp-*")
		}
		if err != nil {
			return SandboxContext{}, nil, fmt.Errorf("sandbox: create tempDir: %w", err)
		}
	}

	// 环境变量隔离（C15/B9）
	envCfg := m.config.EnvConfig
	if envCfg == nil {
		envCfg = DefaultEnvConfig()
	}
	env := BuildEnv(envCfg)
	// 把沙箱自建 tempDir 注入子进程 env。
	// 白名单（AllowList）不含 TMPDIR ⇒ 不注入则子进程丢失临时目录指向、
	// 退回 /tmp —— 而 /tmp 不在沙箱可写根内，任何需要临时目录的命令都会失败
	// （实测 go build：mkdir /tmp/go-build…: operation not permitted）。
	// 详见 env.go injectTempDir（含优先级与"为什么不用父进程 TMPDIR"）。
	env = injectTempDir(env, envCfg, tempDir)

	// CancelFunc：P0 无进程组句柄，使用 context 取消（调用方应配合 KillProcessGroup）
	subCtx, cancel := context.WithCancel(ctx)

	// 调用 OSSandbox.Enter 填充 PlatformHandle
	// Enter 失败时不阻塞主流程（降级到 nil 句柄，记 LogDegradation）
	var platformHandle interface{}
	var osCleanup func()
	var deficiencies []failtier.Deficiency
	osSandbox := m.osSandbox
	if osSandbox != nil {
		handle, cleanup, enterErr := osSandbox.Enter()
		if enterErr != nil {
			// P0-4 修复：Isolated 级别 fail-closed，Strict 以下 fail-open。
			// Isolated 是最高隔离级别，用户请求此级别表示"我需要最强隔离"，
			// 此时 fail-open 违背用户意图。Strict/Relaxed 仍可降级保证可用性。
			if level >= SandboxLevelIsolated {
				cancel()
				_ = os.RemoveAll(tempDir) // O-1 修复：清理临时目录避免泄漏
				return SandboxContext{}, nil, fmt.Errorf("sandbox: OS enter failed at isolated level (fail-closed): %w", enterErr)
			}
			// A5（2026-09-14 · 族A T1）：Strict/Relaxed 仍是"不硬 deny"（Opt-A 的 T1 档：
			// OS 隔离不可用 ⇒ 该命令降级为需权限层显式放行），但**不得静默** ——
			// "OS 隔离能力缺失"必须作为结构化事实随 SandboxContext 上抛，
			// 而不是只留一条调用方拿不到的审计日志（改前：只有 LogDegradation）。
			d := failtier.Deficiency{
				Tier:       failtier.T1,
				Layer:      DeficiencyLayerOSEnter,
				Reason:     fmt.Sprintf("OS sandbox enter failed (%s level): %v", level.String(), enterErr),
				FallbackTo: "permission(L3)+app-layer",
			}
			deficiencies = append(deficiencies, d)
			WarnDeficiency(d)
			if m.audit != nil {
				m.audit.LogDegradation(level, Decrement(level), fmt.Sprintf("OS sandbox enter failed: %v", enterErr))
			}
		} else {
			platformHandle = handle
			osCleanup = cleanup
		}
	}

	cleanup := func() {
		cancel()
		// 清理临时目录（best-effort）
		_ = os.RemoveAll(tempDir)
		// 释放 OS 级句柄
		if osCleanup != nil {
			osCleanup()
		}
		if osSandbox != nil {
			_ = osSandbox.Exit()
		}
	}

	// P0-3：从 OSSandbox 获取 PostStartHook（Windows: AssignProcess；其他: nil）
	var postStartHook func(*exec.Cmd) error
	if osSandbox != nil {
		postStartHook = osSandbox.PostStartHook()
	}

	// P1-1：从 SandboxConfig 填充 ResourceLimits
	var resLimits ResourceLimits
	if m.config != nil {
		resLimits = m.config.ResourceLimits
	}

	return SandboxContext{
		WorkDir:        absWorkDir,
		TempDir:        tempDir,
		Env:            env,
		CancelFunc:     cancel,
		PlatformHandle: platformHandle, // 填充 OS 级句柄（nil 时表示降级或未启用）
		PostStartHook:  postStartHook,  // P0-3：Windows AssignProcess 钩子
		ResourceLimits: resLimits,      // P1-1：资源限制配置
		Deficiencies:   deficiencies,   // A5：OS 级 Enter 失败等降级事实（结构化出口）
		_level:         actualLevel,
		_ctx:           subCtx,
		_osSandbox:     osSandbox, // 暴露 OSSandbox 实例给工具层调用 Restrict
	}, cleanup, nil
}

// ValidateCommand 应用层命令验证（B8：返回 *ValidationResult）。
//
// A3 收编：规则判定与 legacy `Sandbox.Validate` 共用同一引擎
// `evaluateAppLayer` —— 此前本方法**缺**应用层网络拦截与写重定向越界检查（G3/G6），
// 与 legacy 门语义分歧（审计 characterization C-S2-I1-5 / G0 的
// `TestG0Char_ManagerValidate_RuleRange` 已把该分歧钉死）。收编后：
//
//	① validator 安全检查套件（20 项，Critical/High ⇒ Blocked）—— 本方法独有，保持不变
//	② 应用层规则（共享引擎）：deny-wins / allow 白名单 / 命令替换 / IFS / 网络 / 写重定向
func (m *manager) ValidateCommand(ctx context.Context, command string, config *SandboxConfig) (*ValidationResult, error) {
	if strings.TrimSpace(command) == "" {
		return &ValidationResult{Blocked: false}, nil
	}
	cfg := config
	if cfg == nil {
		cfg = m.config
	}
	if cfg == nil {
		cfg = &SandboxConfig{}
	}
	level := m.Level()

	result := &ValidationResult{Level: level}

	// 1. 安全检查套件（B5）
	for _, r := range m.validator.Run(command) {
		result.Violations = append(result.Violations, SecurityViolation{
			CheckID:  r.CheckID,
			Message:  r.Message,
			Severity: int(r.Severity),
		})
	}

	// 2. 应用层规则（A3 收编：与 legacy 门同一实现，消除语义分歧）
	// deny-wins：deny 全量扫描先于 allow（R9 P0-12 修复保持）。
	verdict := evaluateAppLayer(command, appLayerRuleSet{
		DenyCommands:    cfg.DenyCommands,
		AllowCommands:   cfg.AllowCommands,
		NetworkIsolated: networkIsolatedForConfig(cfg.NetworkMode),
		WorkDir:         cfg.WorkDir,
	})
	if verdict.Blocked {
		// ★ ：Severity 携带**后果档**语义（分档唯一实现仍是
		// `tierForRule`，这里只是把档位翻译成 Severity 载体，不新增第二份名单）：
		// - TierHardDeny（ALLOWLIST 未命中 / CONTROL_CHAR / NETWORK_CRITICAL /
		// fail-closed 未知规则）⇒ Critical —— 策略边界与逃逸载体，消费方不得放行；
		// - TierConfirm（curl/chmod/命令替换/IFS/越界写等日常可逆）⇒ High。
		// 此前只有 NETWORK_CRITICAL 映射 Critical，其余硬拒档被压平成 High ⇒
		// bash.go 执行链（起 Critical-only）会把「策略边界硬拒」误当
		// 「可放行的疑似项」（TestG1A_Probe 07 对照组因此抓过红）。
		sev := SeverityHigh
		if verdict.Tier == TierHardDeny {
			sev = SeverityCritical
		}
		result.Violations = append(result.Violations, SecurityViolation{
			CheckID:  verdict.Rule,
			Message:  verdict.Reason,
			Severity: sev,
		})
	}

	// 3. 阻止决策：Critical/High 级违规 → Blocked
	for _, v := range result.Violations {
		if v.Severity >= SeverityHigh {
			result.Blocked = true
			break
		}
	}

	// 4. 审计记录
	if len(result.Violations) > 0 && m.audit != nil {
		m.audit.LogViolations(command, result.Violations, result.Blocked)
	}

	return result, nil
}

// ValidatePath 应用层路径验证（含 SANDBOX-006 TOCTOU 缓解）。
func (m *manager) ValidatePath(ctx context.Context, path string, config *SandboxConfig) error {
	if path == "" {
		return errors.New("sandbox: path is empty")
	}
	cfg := config
	if cfg == nil {
		cfg = m.config
	}
	absWorkDir, _ := filepath.Abs(cfg.WorkDir)
	absWorkDir = resolvePath(absWorkDir)

	absPath, err := filepath.Abs(path)
	if err != nil {
		return fmt.Errorf("sandbox: resolve path: %w", err)
	}
	absPath = resolvePath(absPath)

	// 工作目录内放行
	if isPathWithin(absPath, absWorkDir) {
		return m.verifyNoTOCTOU(absPath)
	}
	// 额外读白名单
	for _, dir := range cfg.AllowRead {
		absDir, _ := filepath.Abs(dir)
		absDir = resolvePath(absDir)
		if isPathWithin(absPath, absDir) {
			return m.verifyNoTOCTOU(absPath)
		}
	}
	// 额外写白名单
	for _, dir := range cfg.AllowWrite {
		absDir, _ := filepath.Abs(dir)
		absDir = resolvePath(absDir)
		if isPathWithin(absPath, absDir) {
			return m.verifyNoTOCTOU(absPath)
		}
	}
	// A4：放行根从"整个 os.TempDir()"收窄为"沙箱自建 tmp"
	// （allowedSandboxTemp，单一实现在 sandbox_tmp.go；legacy 门同一实现）。
	if allowedSandboxTemp(absPath) {
		return m.verifyNoTOCTOU(absPath)
	}
	// whale-code 数据目录（C17）
	for _, dir := range SandboxDirs() {
		if isPathWithin(absPath, resolvePath(dir)) {
			return m.verifyNoTOCTOU(absPath)
		}
	}

	if m.audit != nil {
		m.audit.LogPathViolation(path, absPath)
	}
	return fmt.Errorf("sandbox: path outside allowed range: %s", absPath)
}

// verifyNoTOCTOU 缓解 SANDBOX-006 TOCTOU 竞态：
// 验证后立即对最终路径组件做符号链接检查，拒绝指向外部的边界符号链接。
// 对于已存在文件，通过打开后 fstat 比对路径一致性（best-effort）。
func (m *manager) verifyNoTOCTOU(absPath string) error {
	// 最终组件若是符号链接，且解析后越出已校验范围，则拒绝。
	// 此处 absPath 已经 resolvePath 处理（解析了符号链接），
	// 再次解析以检测验证与使用之间的竞态窗口。
	reResolved := resolvePath(absPath)
	if reResolved != absPath {
		// 路径在校验窗口内被替换为符号链接目标，拒绝
		return fmt.Errorf("sandbox: TOCTOU detected, path changed during validation: %s", absPath)
	}
	return nil
}

// extractCommandNameQuoteAware 提取命令名并剥离引号（SANDBOX-003 修复：cu”rl → curl）。
func extractCommandNameQuoteAware(command string) string {
	normalized := normalizeWhitespace(strings.TrimSpace(command))
	// 取第一个 token
	parts := strings.Fields(normalized)
	if len(parts) == 0 {
		return ""
	}
	cmd := stripQuotes(parts[0])
	cmd = filepath.Base(cmd)
	return cmd
}

// stripQuotes 剥离命令名中的单/双引号（cu”rl → curl，cu""rl → curl）。
func stripQuotes(s string) string {
	var b strings.Builder
	for _, r := range s {
		if r == '\'' || r == '"' {
			continue
		}
		b.WriteRune(r)
	}
	return b.String()
}
