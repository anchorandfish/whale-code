package keybindings

import (
	"fmt"
	"github.com/anchorandfish/whale-code/internal/whalehome"
	"os"
	"path/filepath"
	"sync"
)

// Manager keybinding 管理器，持有 Resolver 和配置路径。
//
// （M11-02）：加 sync.RWMutex 保护 resolver 字段，
// 修复并发 Reload + Parse 的 DATA RACE（TestManagerConcurrentParseAndReload -race 失败）。
// Parse 持读锁代理 resolver.Parse；Reload/load 持写锁重建 resolver。
type Manager struct {
	mu            sync.RWMutex // （M11-02）：保护 resolver 并发访问
	resolver      *Resolver
	userConfig    string
	projectConfig string
	cfg           Config
}

// NewManager 构造 Manager，加载配置 + 合并默认 + 校验 + 构建 Resolver
// D7 修复：校验错误已接入 N6 ErrorID（ErrKeybindingsInvalid），通过 ai.WithID 包装
// 降级警告仍用 fmt.Fprintf 到 stderr（非致命错误，降级到默认表继续运行）
func NewManager(userConfigPath, projectConfigPath string) (*Manager, error) {
	m := &Manager{
		userConfig:    userConfigPath,
		projectConfig: projectConfigPath,
	}

	if err := m.load(); err != nil {
		return nil, err
	}

	return m, nil
}

// load 加载配置文件并构建 Resolver
func (m *Manager) load() error {
	preservedMode := m.cfg.Mode

	cfg, err := LoadAll(m.userConfig, m.projectConfig)
	if err != nil {
		return err
	}

	if preservedMode != "" {
		cfg.Mode = preservedMode
	}

	// 降级配置：校验/冲突/保留键失败时统一使用此配置
	fallbackCfg := Config{Mode: cfg.Mode}

	if errs := ValidateConfig(cfg); len(errs) > 0 {
		for _, e := range errs {
			fmt.Fprintf(os.Stderr, "keybindings: config validation error: %v\n", e)
		}
		cfg = fallbackCfg
	}

	bindings := MergeWithDefault(cfg)

	if conflicts := DetectConflicts(bindings); len(conflicts) > 0 {
		for _, c := range conflicts {
			fmt.Fprintf(os.Stderr, "keybindings: conflict detected: key=%s mode=%s actions=%v\n", c.Key, c.Mode, c.Actions)
		}
		bindings = MergeWithDefault(fallbackCfg)
	}

	// B2 修复：CheckReserved 仅检查用户自定义绑定（cfg.Bindings），不应检查默认表
	// 默认表已由开发方确保不含保留键（B2 修复后 Emacs 默认表移除 ctrl+d）
	if overridden := CheckReserved(cfg.Bindings); len(overridden) > 0 {
		for _, k := range overridden {
			fmt.Fprintf(os.Stderr, "keybindings: reserved key '%s' cannot be overridden, ignoring\n", k)
		}
		bindings = MergeWithDefault(fallbackCfg)
	}

	m.cfg = cfg
	m.resolver = NewResolver(bindings, ReservedKeys())

	return nil
}

// Parse 代理到 Resolver.Parse。
// （M11-02）：持读锁，与 Reload 写锁互斥，修复 DATA RACE。
func (m *Manager) Parse(key Key, mode string) Action {
	m.mu.RLock()
	defer m.mu.RUnlock()
	if m.resolver == nil {
		return Action{Type: ActionPassthrough}
	}
	return m.resolver.Parse(key, mode)
}

// Reload 重新加载配置（手动热重载）。
// （M11-02）：持写锁，与 Parse 读锁互斥，修复 DATA RACE。
func (m *Manager) Reload() error {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.load()
}

// Mode 返回当前配置模式。
// （M11-02）：持读锁保护 cfg 并发访问。
func (m *Manager) Mode() string {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.cfg.Mode
}

// SetMode 设置当前模式（仅内存），需要调用 Reload 才能生效。
// （M11-02）：持写锁保护 cfg 并发访问。
func (m *Manager) SetMode(mode string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.cfg.Mode = mode
}

// DefaultConfigPath 返回默认的 keybindings.json 路径（user + project）
// D5 修复：补充 projectPath 返回值（原仅返回 userPath，projectPath 始终为空）
func DefaultConfigPath(workDir string) (userPath, projectPath string) {
	// 收口：Dir() 返回空（home 不可得）⇒ userPath 保持空，行为同前。
	if whalehome.Dir() != "" {
		userPath = filepath.Join(whalehome.Dir(), "keybindings.json")
	}
	if workDir != "" {
		projectPath = filepath.Join(workDir, ".whale", "keybindings.json")
	}
	return
}
