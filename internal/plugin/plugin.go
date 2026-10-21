// Package plugin — plugin loading, lifecycle management
package plugin

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"github.com/anchorandfish/whale-code/internal/whalehome"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/anchorandfish/whale-code/internal/approval"
	"github.com/anchorandfish/whale-code/internal/log"
	"github.com/anchorandfish/whale-code/internal/secret"
	"github.com/anchorandfish/whale-code/internal/util"
)

// validHookEvents 是 plugin.json hooks[].event 字段允许的事件枚举集合。
// 仅暴露 injectPluginHooks 实际消费的 3 个事件（PreToolUse/PostToolUse/Stop），
// 其他 hook 事件（如 PreRead/PostWrite）当前未在插件路径接线，列入会误导用户。
var validHookEvents = map[string]bool{
	"PreToolUse":  true,
	"PostToolUse": true,
	"Stop":        true,
}

// semverPattern 是 semver 2.0.0 严格格式的正则表达式（MAJOR.MINOR.PATCH + 可选 prerelease/build）。
// 参考：https://semver.org/#is-there-a-suggested-regular-expression-regex-to-check-a-semver-string
var semverPattern = regexp.MustCompile(
	`^(0|[1-9]\d*)\.(0|[1-9]\d*)\.(0|[1-9]\d*)` +
		`(?:-((?:0|[1-9]\d*|\d*[a-zA-Z-][0-9a-zA-Z-]*)(?:\.(?:0|[1-9]\d*|\d*[a-zA-Z-][0-9a-zA-Z-]*))*))?` +
		`(?:\+([0-9a-zA-Z-]+(?:\.[0-9a-zA-Z-]+)*))?$`,
)

// pluginNameMaxWidth 是 plugin.json name 字段最大长度（含 ASCII 与多字节字符）。
const pluginNameMaxWidth = 64

// PluginStateFile is the plugin state persistence structure
//
// B4（S19-H-D · D2）：启用决定必须**绑定内容指纹**，不能只有 ID。
// v1 只有 `plugins{id:bool}` ⇒ 同 ID 换掉整份 manifest（甚至换成完全不同来源、不同 command）
// 后沿用同一个"已启用"决定（表征 TestCharS19_Plugin_EnableDecisionNotBoundToManifestContent 钉死了这个缺口）。
// v2 追加 `fingerprints{id:sha256:...}`；读侧对不上就不启用（fail-closed，见 loadStateLocked）。
// 旧文件（无 fingerprints）同样按"缺判据"处理 —— 不静默继承启用，需用户显式重启用一次。
type PluginStateFile struct {
	Version int             `json:"version"`
	Plugins map[string]bool `json:"plugins"`
	// Fingerprints 记录"用户据此做出启用决定"的那份内容指纹（manifest + main，见 pluginFingerprint）。
	Fingerprints map[string]string `json:"fingerprints,omitempty"`
}

// Plugin is the plugin definition
type Plugin struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	Version     string `json:"version"`
	Description string `json:"description"`
	Author      string `json:"author,omitempty"`
	Homepage    string `json:"homepage,omitempty"`
	Repository  string `json:"repository,omitempty"`
	License     string `json:"license,omitempty"`
	// Main 是插件主入口文件相对路径（相对插件目录），可选字段。
	// 指定时必须实际存在；为空时跳过校验（向后兼容旧插件无 main 字段）。
	// ( SNAPSHOT) M20-03：新增字段用于严格 manifest 校验。
	Main   string     `json:"main,omitempty"`
	Tools  []ToolDef  `json:"tools,omitempty"`
	Hooks  []HookDef  `json:"hooks,omitempty"`
	Skills []SkillDef `json:"skills,omitempty"`

	Enabled     bool      `json:"enabled"`
	SourcePath  string    `json:"-"`
	InstallTime time.Time `json:"-"`
	// Fingerprint 是加载时按 manifest 内容算出的指纹（B4：启用决定的绑定对象）。
	Fingerprint string `json:"-"`
	// EnableBlocked 非空表示"状态文件要求启用，但被判据拦下"（指纹缺失/不符），
	// 供 `/plugin list` 与 UI 解释"为什么它没起来"。为空表示没有这一状态。
	EnableBlocked string `json:"-"`
}

// ToolDef is the plugin tool definition
type ToolDef struct {
	Name               string                 `json:"name"`
	Description        string                 `json:"description"`
	InputSchema        map[string]interface{} `json:"input_schema"`
	RequiredPermission string                 `json:"required_permission"`
	Command            string                 `json:"command"` // Execute command
}

// HookDef is the plugin hook definition
type HookDef struct {
	Event    string `json:"event"`     // PreToolUse, PostToolUse, Stop
	ToolName string `json:"tool_name"` // glob pattern
	Command  string `json:"command"`
	// PluginID 是**注入侧**由 GetEnabledHooks 填上的来源身份（B5-E2，对标同类 CLI PluginHookSource.plugin_id）。
	//
	// json:"-" 是安全约束的一部分：身份必须来自插件自身（manifest 的 id / 目录名），
	// **不得**由 manifest 的 hooks[] 条目自带 —— 否则插件可以冒充另一个插件的身份，
	// 从而继承后者的信任记录（信任门按 id+内容寻址）。
	PluginID string `json:"-"`
}

// SkillDef is the plugin skill definition
type SkillDef struct {
	Name        string `json:"name"`
	Description string `json:"description"`
	Prompt      string `json:"prompt"`
}

// Manager is the plugin manager
type Manager struct {
	mu          sync.RWMutex
	plugins     map[string]*Plugin
	dirs        []string
	statePath   string
	approvalMgr *approval.Manager // B0 决策：approval 消费者，克隆前询问用户确认；nil 时跳过确认仅记录日志
	// onUninstall 卸载回调（P0-5/B6）：由 Executor 注入，
	// 调用 tools.UnregisterPluginTools + ReinjectPluginHooks 注销工具与清理钩子。
	// 为 nil 时 Uninstall 仅清理 Manager 内部状态（向后兼容）。
	onUninstall func(id string)
	// signatureVerifier 插件签名校验器（G2-P0-1 修复，）。
	// Install 路径强制调用 Verify 验证插件签名；nil 时 fail-closed 拒绝安装
	//（除非 allowUnsignedPlugins=true 显式 opt-in）。
	signatureVerifier SignatureVerifier
	// allowUnsignedPlugins 是否允许未签名插件安装（G2-P0-1 修复）。
	// 默认 false（fail-closed）；设为 true 时允许跳过验签但记审计警告。
	// 用于向后兼容旧插件市场——需显式 opt-in，不默认放行。
	allowUnsignedPlugins bool
}

// NewManager creates a plugin manager
func NewManager(dirs ...string) *Manager {
	statePath := filepath.Join(whalehome.Dir(), "plugin_state.json")
	return &Manager{
		plugins:   make(map[string]*Plugin),
		dirs:      dirs,
		statePath: statePath,
	}
}

// SetStatePath sets the state file path (for testing)
func (m *Manager) SetStatePath(path string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.statePath = path
}

// SetApprovalManager 注入 approval.Manager，用于 git clone 前的用户确认（B0 决策的 approval 消费者扩展）。
// 传入 nil 可清除已有管理器，此时克隆操作将跳过确认仅记录日志（向后兼容）。
func (m *Manager) SetApprovalManager(mgr *approval.Manager) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.approvalMgr = mgr
}

// SetOnUninstall 注入卸载回调（P0-5/B6）。
// Executor 注入的回调应执行 tools.UnregisterPluginTools(id) + ReinjectPluginHooks()，
// 确保卸载后插件工具不可调用、钩子不残留。传入 nil 可清除回调（向后兼容）。
func (m *Manager) SetOnUninstall(fn func(id string)) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.onUninstall = fn
}

// SetSignatureVerifier 注入插件签名校验器（G2-P0-1 修复，）。
//
// Install 路径在加载 plugin.json 后、持久化状态前调用 Verify 验证签名。
// 传入 nil 可清除校验器（此时 Install 走 fail-closed 降级逻辑，
// 需 SetAllowUnsignedPlugins(true) 方可放行未签名插件）。
func (m *Manager) SetSignatureVerifier(v SignatureVerifier) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.signatureVerifier = v
}

// SetAllowUnsignedPlugins 设置是否允许未签名插件安装（G2-P0-1 修复）。
//
// 默认 false（fail-closed）：signatureVerifier 为 nil 时拒绝安装。
// 设为 true 时允许跳过验签但记审计警告（向后兼容旧插件市场，需显式 opt-in）。
func (m *Manager) SetAllowUnsignedPlugins(allow bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.allowUnsignedPlugins = allow
}

// LoadAll loads all plugins
func (m *Manager) LoadAll() error {
	m.mu.Lock()
	defer m.mu.Unlock()

	for _, dir := range m.dirs {
		m.loadFromDir(dir)
	}

	// Load persisted state and apply
	m.loadStateLocked()
	return nil
}

// loadFromDir loads plugins from directory
func (m *Manager) loadFromDir(dir string) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return
	}

	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}

		manifestPath := filepath.Join(dir, entry.Name(), "plugin.json")
		if _, err := os.Stat(manifestPath); err != nil {
			continue
		}

		plugin, err := loadPlugin(manifestPath)
		if err != nil {
			log.Warn("Failed to load plugin", "path", manifestPath, "error", err)
			continue
		}

		plugin.SourcePath = filepath.Dir(manifestPath)

		// B6（S19-G5）：加载路径也必须验签（修复前只有 Install 验签 ⇒ 手工丢进
		// .whale/plugins/ 的插件**永不验签**，连"是否配了验签器"都不问）。
		// 语义完全交给验签器自己：Required=false 且无 .sig ⇒ 放行（不改既有姿态）；
		// Required=true 缺签名、或签名存在但验不过 ⇒ 拒绝加载（fail-closed，不进内存表）。
		if m.signatureVerifier != nil {
			if err := m.signatureVerifier.Verify(plugin.SourcePath); err != nil {
				log.Warn("Plugin signature verification failed — plugin NOT loaded",
					"id", plugin.ID, "path", plugin.SourcePath, "error", err)
				continue
			}
		}

		// B4：算出内容指纹，作为"启用决定"的绑定对象（loadStateLocked 会用它与状态文件比对）。
		plugin.Fingerprint = pluginFingerprint(plugin)

		// P0-7/B7: 插件默认禁用（高风险任意代码执行），需 --enable-plugins 显式启用。
		// loadStateLocked 会从状态文件恢复已启用插件的 Enabled=true。
		plugin.Enabled = false
		m.plugins[plugin.ID] = plugin
	}
}

// loadPlugin 解析 plugin.json 并执行严格 schema 校验（M20-03）。
//
// 校验项（任一失败即拒绝整个插件，不进入内存表）：
// - version 必须非空且符合 semver 2.0.0 格式（MAJOR.MINOR.PATCH + 可选 prerelease/build）
// - name 非空且长度 ≤ 64 字符（auto-fill 后再校验，确保默认值合规）
// - hooks[].event 必须在 {PreToolUse, PostToolUse, Stop} 枚举内
// - tools[].command / hooks[].command 非空（沿用 validateCommand）
// - main 字段若指定，相对插件目录的文件必须存在
//
// 失败时返回包裹原因的错误（不污染内存表），调用方按需记录日志或返回用户。
func loadPlugin(path string) (*Plugin, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read plugin: %w", err)
	}

	var plugin Plugin
	if err := json.Unmarshal(data, &plugin); err != nil {
		return nil, fmt.Errorf("parse plugin: %w", err)
	}

	// 默认值填充：缺 id 用目录名，缺 name 用 id
	if plugin.ID == "" {
		base := filepath.Dir(path)
		plugin.ID = filepath.Base(base)
	}
	if plugin.Name == "" {
		plugin.Name = plugin.ID
	}

	// 严格 schema 校验（M20-03）：在默认值填充后执行，确保 name 一定可校验。
	pluginDir := filepath.Dir(path)
	if err := validatePluginSchema(&plugin, pluginDir); err != nil {
		return nil, err
	}

	for _, tool := range plugin.Tools {
		if err := validateCommand(tool.Command); err != nil {
			return nil, fmt.Errorf("tool %q: %w", tool.Name, err)
		}
	}
	for _, hook := range plugin.Hooks {
		if err := validateCommand(hook.Command); err != nil {
			return nil, fmt.Errorf("hook %q: %w", hook.Event, err)
		}
	}

	return &plugin, nil
}

// validatePluginSchema 对插件 manifest 执行严格 schema 校验（M20-03）。
// pluginDir 为插件所在目录（用于校验 main 字段指向的文件是否存在）。
// 返回首个失败项的错误，nil 表示全部通过。
func validatePluginSchema(p *Plugin, pluginDir string) error {
	if err := validatePluginName(p.Name); err != nil {
		return fmt.Errorf("name: %w", err)
	}
	if err := validateSemver(p.Version); err != nil {
		return fmt.Errorf("version: %w", err)
	}
	for i, hook := range p.Hooks {
		if err := validateHookEvent(hook.Event); err != nil {
			return fmt.Errorf("hooks[%d]: %w", i, err)
		}
	}
	if p.Main != "" {
		if err := validateMainFile(p.Main, pluginDir); err != nil {
			return fmt.Errorf("main: %w", err)
		}
	}
	return nil
}

// validatePluginName 校验插件 name 字段非空且长度 ≤ 64 字符。
// 调用方在 auto-fill 默认值后再调用本函数，确保 name 已填充。
func validatePluginName(name string) error {
	if name == "" {
		return fmt.Errorf("name cannot be empty")
	}
	if len([]rune(name)) > pluginNameMaxWidth {
		return fmt.Errorf("name length %d exceeds max %d", len([]rune(name)), pluginNameMaxWidth)
	}
	return nil
}

// validateSemver 校验 version 字段非空且符合 semver 2.0.0 严格格式。
// 接受形如 1.0.0 / 0.2.1 / 1.0.0-alpha / 1.0.0-alpha.1+build.2 的版本号，
// 拒绝 v1 / 1.0 / abc / 1.0.0.0 等不合规字符串。
// 参考：https://semver.org/#is-there-a-suggested-regular-expression-regex-to-check-a-semver-string
func validateSemver(version string) error {
	if version == "" {
		return fmt.Errorf("version cannot be empty")
	}
	if !semverPattern.MatchString(version) {
		return fmt.Errorf("version %q is not valid semver (expected MAJOR.MINOR.PATCH, e.g. 1.0.0)", version)
	}
	return nil
}

// validateHookEvent 校验 hooks[].event 字段在预定义枚举内。
// 当前插件路径仅接线 PreToolUse/PostToolUse/Stop 三个事件，其他事件值会被
// injectPluginHooks 静默忽略导致用户困惑，故在加载阶段直接拒绝。
func validateHookEvent(event string) error {
	if event == "" {
		return fmt.Errorf("event cannot be empty")
	}
	if !validHookEvents[event] {
		return fmt.Errorf("event %q not in allowed enum %v", event, allowedHookEventsSorted())
	}
	return nil
}

// allowedHookEventsSorted 返回允许的 hook 事件枚举的排序后切片（用于错误提示）。
func allowedHookEventsSorted() []string {
	events := make([]string, 0, len(validHookEvents))
	for e := range validHookEvents {
		events = append(events, e)
	}
	// 简单排序保证错误消息稳定
	for i := 1; i < len(events); i++ {
		for j := i; j > 0 && events[j-1] > events[j]; j-- {
			events[j-1], events[j] = events[j], events[j-1]
		}
	}
	return events
}

// validateMainFile 校验 main 字段指向的文件实际存在于插件目录下。
// mainPath 为相对插件目录的路径，调用方须提供 pluginDir 绝对路径。
// 同时拒绝含 ".." 的相对路径，防路径穿越到插件目录之外。
func validateMainFile(mainPath, pluginDir string) error {
	if strings.Contains(mainPath, "..") {
		return fmt.Errorf("main path %q contains path traversal", mainPath)
	}
	absPath := filepath.Join(pluginDir, mainPath)
	if _, err := os.Stat(absPath); err != nil {
		return fmt.Errorf("main file %q not found: %w", mainPath, err)
	}
	return nil
}

// Get gets a plugin
func (m *Manager) Get(id string) (*Plugin, bool) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	p, ok := m.plugins[id]
	return p, ok
}

// List lists all plugins
func (m *Manager) List() []*Plugin {
	m.mu.RLock()
	defer m.mu.RUnlock()
	var plugins []*Plugin
	for _, p := range m.plugins {
		plugins = append(plugins, p)
	}
	return plugins
}

// Enable enables a plugin
func (m *Manager) Enable(id string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	p, ok := m.plugins[id]
	if !ok {
		return fmt.Errorf("plugin %q does not exist", id)
	}
	p.Enabled = true
	// 用户显式启用 = 对**当前这份内容**的授权 ⇒ 清掉"被拦下"的说明；
	// 指纹由 saveStateLocked 重新锚定到当前内容（这就是 B4 的"review 事件"）。
	p.EnableBlocked = ""
	return m.saveStateLocked()
}

// Disable disables a plugin
func (m *Manager) Disable(id string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	p, ok := m.plugins[id]
	if !ok {
		return fmt.Errorf("plugin %q does not exist", id)
	}
	p.Enabled = false
	return m.saveStateLocked()
}

// EnableAll 启用所有已加载插件（P0-7/B7）。
// 由 --enable-plugins flag 触发：用户显式启用所有插件，绕过逐个确认。
// 返回启用的插件数量。状态持久化由调用方负责（避免频繁 IO）。
func (m *Manager) EnableAll() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	count := 0
	for _, p := range m.plugins {
		if !p.Enabled {
			p.Enabled = true
			count++
		}
	}
	return count
}

// Install 安装插件，强制调用 SignatureVerifier.Verify 验证签名（G2-P0-1 修复，）。
//
// 修复前：Install 全流程无 SignatureVerifier.Verify 调用，攻击者推送恶意插件可直接加载执行。
// 修复后：加载 plugin.json 后、持久化状态前强制验签。
//
// 流程：resolveInstallPath → loadPlugin → SignatureVerifier.Verify → saveStateLocked
//
// 降级策略（fail-closed）：
// - signatureVerifier 非 nil：调用 Verify，失败则拒绝安装
// - signatureVerifier 为 nil 且 allowUnsignedPlugins=false：拒绝安装（fail-closed）
// - signatureVerifier 为 nil 且 allowUnsignedPlugins=true：允许安装但记审计警告
func (m *Manager) Install(source string) (*Plugin, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	if len(m.dirs) == 0 {
		return nil, fmt.Errorf("no plugin directory configured")
	}

	targetDir := m.dirs[0]
	targetPath, err := m.resolveInstallPath(source, targetDir)
	if err != nil {
		return nil, err
	}

	manifestPath := filepath.Join(targetPath, "plugin.json")
	if _, err := os.Stat(manifestPath); err != nil {
		return nil, fmt.Errorf("Invalid plugin: plugin.json not found in %s", targetPath)
	}

	plugin, err := loadPlugin(manifestPath)
	if err != nil {
		return nil, fmt.Errorf("Invalid plugin: %w", err)
	}

	// G2-P0-1：签名验证（fail-closed）
	if m.signatureVerifier != nil {
		if err := m.signatureVerifier.Verify(targetPath); err != nil {
			return nil, fmt.Errorf("plugin signature verification failed: %w", err)
		}
	} else {
		// fail-closed：安全特性（签名验证）未配置时默认拒绝
		// 仅当显式 opt-in allow_unsigned_plugins 时放行并记审计警告
		if !m.allowUnsignedPlugins {
			return nil, fmt.Errorf("plugin signature verifier not configured (fail-closed); set allow_unsigned_plugins=true to opt-in unsigned installs")
		}
		log.Warn("Plugin install without signature verification (allow_unsigned_plugins=true)", "source", source)
	}

	if _, exists := m.plugins[plugin.ID]; exists {
		return nil, fmt.Errorf("Plugin already installed: %s", plugin.ID)
	}

	plugin.SourcePath = targetPath
	plugin.Enabled = true
	plugin.InstallTime = time.Now()
	m.plugins[plugin.ID] = plugin

	if err := m.saveStateLocked(); err != nil {
		return plugin, err
	}

	return plugin, nil
}

// resolveInstallPath resolves install path, supports Git URL and local path
func (m *Manager) resolveInstallPath(source, targetDir string) (string, error) {
	if isGitURL(source) {
		return m.cloneFromGit(source, targetDir)
	}

	if isLocalPath(source) {
		return source, nil
	}

	return "", fmt.Errorf("Invalid source: %s (must be a git URL or local path)", source)
}

// isGitURL checks if it is a Git URL
func isGitURL(source string) bool {
	return strings.HasPrefix(source, "https://") ||
		strings.HasPrefix(source, "http://") ||
		strings.HasPrefix(source, "git@") ||
		strings.HasPrefix(source, "ssh://")
}

// isLocalPath checks if it is a local path
func isLocalPath(source string) bool {
	if strings.HasPrefix(source, "/") || strings.HasPrefix(source, ".\\") || strings.HasPrefix(source, "./") {
		return true
	}
	if len(source) >= 2 && source[1] == ':' {
		return true
	}
	return false
}

// validateCommand 校验插件命令字符串非空（基础完整性检查）。
//
// P0-2：删除 dangerousShellChars 黑名单逻辑。命令注入防御改由三层兜底：
// - pluginToolHandler 使用 parseCommandToArgs 无 shell 执行（元字符不被解释）
// - sandbox.Manager.ValidateCommand 在执行前拦截危险命令模式
// - permission/approval 在工具调用前询问用户确认
//
// 保留空命令校验以拒绝无意义的 manifest 配置（与注入防御无关）。
//
// H2 合理偏离：本函数不委托 security.CheckCommand。
// 原因：validateCommand 在插件加载阶段（NewExecutor → LoadAll）调用，
// 此时 security.Init 尚未执行（defaultChecker 为 nil），fail-closed 会拒绝
// 所有插件命令导致插件无法加载。实际命令执行安全由 sandbox.Manager.ValidateCommand
// + approval 在执行时保障（此时 security 已初始化）。
func validateCommand(cmd string) error {
	if cmd == "" {
		return fmt.Errorf("command cannot be empty")
	}
	return nil
}

// validateGitURL 校验 git URL 协议白名单（https/http/git@/ssh://）。
//
// P0-2：删除 dangerousShellChars 字符黑名单。URL 注入防御由以下措施保障：
// - cloneFromGit 使用 exec.Command 参数化传递 URL（不经 shell）
// - core.hooksPath=/dev/null + protocol.file.allow=never + --no-local 三道防线
// - approval.Manager 在克隆前询问用户确认
//
// 保留协议白名单以拒绝非 Git 协议（file:// 等已被 protocol.file.allow=never 拦截，
// 此处为深度防御，避免无效协议进入 clone 流程）。
func validateGitURL(rawurl string) error {
	if !isGitURL(rawurl) {
		return fmt.Errorf("invalid git URL: %s", rawurl)
	}
	return nil
}

// cloneFromGit 克隆插件仓库并在克隆前执行 approval 确认、克隆后校验完整性。
//
// 安全措施（缺一不可，必须在 clone 命令本身下功夫，verifyCloneIntegrity 为时已晚）：
// - core.hooksPath=/dev/null：禁用 hooks，防止 .git/hooks/post-checkout 等在克隆后立即触发 RCE
// - protocol.file.allow=never：禁止 file:// 协议，防止路径遍历与本地文件泄露
// - --no-local：强制走 git 协议，避免本地硬链接绕过隔离边界
// - clone 后清理 .git/hooks/ 目录（防御纵深）：物理删除恶意钩子，
// 防止后续 git 操作未携带 core.hooksPath=/dev/null 时触发 RCE
//
// approval 接入（B0 决策的消费者扩展）：克隆前调用 approval.Manager.Check 询问用户确认；
// approvalMgr 为 nil 时跳过确认仅记录日志（向后兼容）。
func (m *Manager) cloneFromGit(url, targetDir string) (string, error) {
	if err := validateGitURL(url); err != nil {
		return "", err
	}

	name := url[strings.LastIndex(url, "/")+1:]
	if !strings.HasSuffix(name, ".git") {
		name += ".git"
	}
	dirName := strings.TrimSuffix(name, ".git")
	if strings.Contains(dirName, "..") {
		return "", fmt.Errorf("Invalid git URL: directory name contains path traversal")
	}
	targetPath := filepath.Join(targetDir, dirName)

	// Verify the resolved target path stays within targetDir to prevent
	// path traversal attacks where URL components include ".." segments
	absTargetPath, err := filepath.Abs(targetPath)
	if err != nil {
		return "", fmt.Errorf("Failed to resolve target path: %w", err)
	}
	absTargetDir, err := filepath.Abs(targetDir)
	if err != nil {
		return "", fmt.Errorf("Failed to resolve target directory: %w", err)
	}
	relPath, err := filepath.Rel(absTargetDir, absTargetPath)
	if err != nil || strings.HasPrefix(relPath, "..") {
		return "", fmt.Errorf("Invalid git URL: resolves outside plugin directory")
	}

	if _, err := os.Stat(targetPath); err == nil {
		return targetPath, nil
	}

	// 克隆前通过 approval.Manager 询问用户确认（B0 决策的 approval 消费者扩展）。
	// approvalMgr 为 nil 时跳过确认，仅记录日志（向后兼容，B3 config 接入前为常态）。
	if m.approvalMgr != nil {
		decision, reason, checkErr := m.approvalMgr.Check(context.Background(), "git_clone", url)
		if checkErr != nil {
			return "", fmt.Errorf("git clone approval check failed: %w", checkErr)
		}
		if decision != approval.DecisionAllow {
			return "", fmt.Errorf("git clone denied by approval: %s", reason)
		}
	} else {
		// P-3 修复（全模块审查）：approvalMgr 为 nil 时改 fail-closed，
		// 拒绝执行 git clone，避免未审批的远程代码下载。
		return "", fmt.Errorf("git clone rejected: approval manager not configured (fail-closed), url=%s", url)
	}

	if err := os.MkdirAll(targetDir, 0755); err != nil {
		return "", fmt.Errorf("Install failed: %w", err)
	}

	// 安全加固：三道防线必须在 clone 命令本身生效，verifyCloneIntegrity 在 clone 之后运行为时已晚。
	cmd := exec.Command("git",
		"-c", "core.hooksPath=/dev/null", // 禁用 hooks，防止 post-checkout 等 hook 触发 RCE
		"-c", "protocol.file.allow=never", // 禁止 file:// 协议，防路径遍历与本地文件泄露
		"clone", "--depth", "1",
		"--no-local", // 强制走 git 协议，避免本地硬链接绕过隔离边界
		url, targetPath,
	)
	// H3：子进程 env 走白名单，剥离密钥变量（守 02 §2.3.1）。
	// git clone 需要 GIT_* 配置变量（如 GIT_SSH），追加前缀匹配。
	cmd.Env = secret.SafeEnv("GIT_")
	output, err := cmd.CombinedOutput()
	if err != nil {
		return "", fmt.Errorf("Install failed: %v\n%s", err, string(output))
	}

	// 安全加固：clone 后立即清理 .git/hooks/ 目录。
	// core.hooksPath=/dev/null 仅在本次 clone 命令期间禁用 hooks，
	// 后续任何 git 操作（如 commit/checkout）若未携带该配置，
	// 仓库内 .git/hooks/post-checkout 等恶意钩子可能被触发执行 RCE。
	// 物理删除 hooks 目录可彻底消除该风险，作为防御纵深补充。
	hooksDir := filepath.Join(targetPath, ".git", "hooks")
	if rmErr := os.RemoveAll(hooksDir); rmErr != nil {
		// 删除失败不阻断安装，仅记录日志（hooks 已被 core.hooksPath 禁用，非致命）
		log.Warn("Plugin clone: failed to remove .git/hooks directory",
			"path", hooksDir, "error", rmErr)
	} else {
		log.Info("Plugin clone: removed .git/hooks directory to prevent hook injection",
			"path", hooksDir)
	}

	// Verify the cloned repository has a valid HEAD commit
	if err := verifyCloneIntegrity(targetPath); err != nil {
		log.Warn("Plugin clone integrity check failed", "path", targetPath, "error", err)
		// Clean up the cloned directory on integrity failure
		os.RemoveAll(targetPath)
		return "", fmt.Errorf("Plugin clone integrity verification failed: %w", err)
	}

	return targetPath, nil
}

// verifyCloneIntegrity performs basic integrity checks on a cloned repository
// to detect truncated or corrupted clones
func verifyCloneIntegrity(path string) error {
	// Verify HEAD exists and references a valid commit
	headCmd := exec.Command("git", "rev-parse", "HEAD")
	headCmd.Dir = path
	headCmd.Env = secret.SafeEnv("GIT_") // H3：子进程 env 白名单
	headOutput, err := headCmd.Output()
	if err != nil {
		return fmt.Errorf("cannot resolve HEAD: %w", err)
	}
	commitHash := strings.TrimSpace(string(headOutput))
	if len(commitHash) < 7 {
		return fmt.Errorf("invalid commit hash: %s", commitHash)
	}

	// Verify the commit object exists in the repository
	catCmd := exec.Command("git", "cat-file", "-t", commitHash)
	catCmd.Dir = path
	catCmd.Env = secret.SafeEnv("GIT_") // H3：子进程 env 白名单
	catOutput, err := catCmd.Output()
	if err != nil || strings.TrimSpace(string(catOutput)) != "commit" {
		return fmt.Errorf("commit %s not found in repository", commitHash[:7])
	}

	// Verify plugin.json exists and is readable
	manifestPath := filepath.Join(path, "plugin.json")
	if _, err := os.Stat(manifestPath); err != nil {
		return fmt.Errorf("plugin.json not found in cloned repository")
	}

	return nil
}

// Uninstall 卸载插件：先触发注销回调（清理工具与钩子），再从内存移除并持久化状态。
//
// 注销回调（onUninstall）在 delete(m.plugins, id) 之前调用，此时插件仍存在于 map，
// 回调内 tools.UnregisterPluginTools 可通过 Get(id) 读取插件工具列表执行注销。
//
// 死锁规避：回调必须在释放写锁后调用——UnregisterPluginTools 内部调用
// Manager.Get(id) 获取 RLock，若 Uninstall 持有写锁会死锁。
// 因此采用「锁内检查存在 → 释放锁 → 调用回调 → 重新获锁 delete」两段式。
// onUninstall 为 nil 时仅清理 Manager 内部状态（向后兼容）。
func (m *Manager) Uninstall(id string) error {
	m.mu.Lock()
	if _, ok := m.plugins[id]; !ok {
		m.mu.Unlock()
		return fmt.Errorf("plugin %q does not exist", id)
	}
	m.mu.Unlock()

	// 释放锁后调用回调：回调内 Get(id) 可成功（插件尚未 delete）
	if m.onUninstall != nil {
		m.onUninstall(id)
	}

	m.mu.Lock()
	defer m.mu.Unlock()
	// 双检查：回调期间可能被并发 Uninstall 卸载同一 id
	if _, ok := m.plugins[id]; !ok {
		return nil
	}
	delete(m.plugins, id)
	return m.saveStateLocked()
}

// ReloadAll rescans and loads all plugin directories
func (m *Manager) ReloadAll() ([]*Plugin, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	// Save current state snapshot（B4：同时记住指纹 —— 见下方恢复条件）
	oldState := make(map[string]bool)
	oldFingerprint := make(map[string]string)
	for id, p := range m.plugins {
		oldState[id] = p.Enabled
		oldFingerprint[id] = p.Fingerprint
	}

	// Clear current list
	m.plugins = make(map[string]*Plugin)

	// Rescan
	for _, dir := range m.dirs {
		m.loadFromDir(dir)
	}

	// Restore known plugin states
	// B4：与 loadStateLocked 同一条判据 —— 内容变了就不继承启用决定（否则 ReloadAll 会成为
	// "换掉 manifest 后自动重新启用"的旁路，与 loadStateLocked 的 fail-closed 自相矛盾）。
	for id, enabled := range oldState {
		p, ok := m.plugins[id]
		if !ok {
			continue
		}
		switch {
		case !enabled:
			p.Enabled = false
		case oldFingerprint[id] == "" || p.Fingerprint == "":
			p.Enabled = false
			p.EnableBlocked = "enable decision not inherited across reload (content fingerprint unavailable); re-enable explicitly"
		case oldFingerprint[id] != p.Fingerprint:
			p.Enabled = false
			p.EnableBlocked = fmt.Sprintf("plugin content changed on reload (recorded %s, current %s); re-enable to review the new content",
				shortFingerprint(oldFingerprint[id]), shortFingerprint(p.Fingerprint))
		default:
			p.Enabled = true
		}
	}

	// P0-7/B7: 新发现的插件默认禁用（loadFromDir 设置 Enabled=false），需 --enable-plugins 显式启用。
	if err := m.saveStateLocked(); err != nil {
		return nil, err
	}

	return m.listLocked(), nil
}

// listLocked gets plugin list (caller must hold lock)
func (m *Manager) listLocked() []*Plugin {
	var plugins []*Plugin
	for _, p := range m.plugins {
		plugins = append(plugins, p)
	}
	return plugins
}

// SaveState persists plugin state to file
func (m *Manager) SaveState() error {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.saveStateLocked()
}

// saveStateLocked 原子持久化插件状态（调用方须持写锁）。
// 使用 util.AtomicWriteFile（tempfile + fsync + rename）替代 os.WriteFile，
// 防止并发写或断电导致状态文件损坏：原文件在 rename 成功前保持完整。
//
// B4：同时记录**当前内容指纹** —— 这个动作就是"把启用决定锚定到某一版内容"。
func (m *Manager) saveStateLocked() error {
	state := PluginStateFile{
		Version:      2,
		Plugins:      make(map[string]bool),
		Fingerprints: make(map[string]string),
	}
	for id, p := range m.plugins {
		state.Plugins[id] = p.Enabled
		// 指纹以"当前磁盘内容"重算（p.Fingerprint 由 loadFromDir 填过，但 Uninstall/Install/
		// ReloadAll 之后可能过期 ⇒ 重算是唯一真相，避免把旧指纹当成新内容的授权）。
		if fp := pluginFingerprint(p); fp != "" {
			state.Fingerprints[id] = fp
			p.Fingerprint = fp
		}
	}

	data, err := json.MarshalIndent(state, "", "  ")
	if err != nil {
		return fmt.Errorf("save plugin state: %w", err)
	}

	// AtomicWriteFile 内部已处理 MkdirAll + tempfile + fsync + rename，
	// 保证并发写不损坏、断电恢复后状态文件完整。
	if err := util.AtomicWriteFile(m.statePath, data, 0600); err != nil {
		return fmt.Errorf("save plugin state: %w", err)
	}

	return nil
}

// LoadState loads plugin state from file
func (m *Manager) LoadState() error {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.loadStateLocked()
}

// loadStateLocked loads and applies plugin state from file (caller must hold write lock)
//
// B4（S19-H-D · D2 = 内容指纹绑定）：启用决定只在**指纹对得上**时才被继承。
//
//	① 状态文件要求启用 + 记录了指纹 + 与当前内容一致 ⇒ 启用（老行为，正常路径）；
//	② 状态文件要求启用 + 无指纹记录（v1 旧文件）或指纹不符 ⇒ **不启用**（fail-closed，T2：
//
// 判定所需的判据不可得/已失效）+ 记 EnableBlocked 说明 + 告警，用户显式 `/plugin enable <id>`
// 或 `--enable-plugins` 即可对**当前**内容重新授权。
//
//	③ 状态文件要求禁用 ⇒ 维持禁用（无需判据）。
//
// 修复前：只按 ID 赋值 ⇒ 同 ID 换掉整份 manifest（不同来源、不同 command）后沿用同一启用决定。
func (m *Manager) loadStateLocked() error {
	data, err := os.ReadFile(m.statePath)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return fmt.Errorf("load plugin state: %w", err)
	}

	var state PluginStateFile
	if err := json.Unmarshal(data, &state); err != nil {
		return fmt.Errorf("load plugin state: %w", err)
	}

	for id, enabled := range state.Plugins {
		p, ok := m.plugins[id]
		if !ok {
			continue
		}
		if !enabled {
			p.Enabled = false
			continue
		}
		recorded := state.Fingerprints[id]
		switch {
		case recorded == "":
			p.Enabled = false
			p.EnableBlocked = "enable decision in state file has no content fingerprint (legacy state); re-enable explicitly to bind it to the current content"
		case p.Fingerprint == "":
			// 判据不可得（算不出当前内容指纹）⇒ 同样不放行（族 A：缺判据 = T2，不得 fail-open）。
			p.Enabled = false
			p.EnableBlocked = "current content fingerprint unavailable; re-enable explicitly to bind it to the current content"
		case recorded != p.Fingerprint:
			p.Enabled = false
			p.EnableBlocked = fmt.Sprintf("plugin content changed since it was enabled (recorded %s, current %s); re-enable to review the new content",
				shortFingerprint(recorded), shortFingerprint(p.Fingerprint))
		default:
			p.Enabled = true
		}
		if p.EnableBlocked != "" {
			log.Warn("Plugin enable decision NOT inherited (content fingerprint mismatch) — plugin stays disabled",
				"id", id, "reason", p.EnableBlocked)
		}
	}

	return nil
}

// shortFingerprint 截断指纹到 "sha256:abcd1234…" 形态，供人类可读告警/列表使用。
func shortFingerprint(fp string) string {
	const keep = 8
	if len(fp) > len("sha256:")+keep {
		return fp[:len("sha256:")+keep] + "…"
	}
	return fp
}

// GetEnabledTools gets tool definitions of all enabled plugins
func (m *Manager) GetEnabledTools() []ToolDef {
	m.mu.RLock()
	defer m.mu.RUnlock()
	var tools []ToolDef
	for _, p := range m.plugins {
		if !p.Enabled {
			continue
		}
		tools = append(tools, p.Tools...)
	}
	return tools
}

// GetEnabledHooks gets hook definitions of all enabled plugins
//
// B5-E2（S19-H-E）：注入**来源身份** —— 每个 HookDef 带上其所属插件的 ID。
// 修复前注入形态是 `ID=""`（无 provenance），导致插件 hook 在信任库里没有身份，
// 只能靠 SourcePlugin 一刀切"安装即信任"，且改写授权只能走 `all`（无法按插件粒度授权）。
// ID 只可能来自插件自身（HookDef.PluginID 是 json:"-"，manifest 无法冒充）。
func (m *Manager) GetEnabledHooks() []HookDef {
	m.mu.RLock()
	defer m.mu.RUnlock()
	var hooks []HookDef
	for _, p := range m.plugins {
		if !p.Enabled {
			continue
		}
		for _, hd := range p.Hooks {
			hd.PluginID = p.ID
			hooks = append(hooks, hd)
		}
	}
	return hooks
}

// pluginFingerprint 计算插件内容的指纹（B4：启用决定绑定的对象），形态对齐业内既有实现
// `config/src/fingerprint.rs:37`（canonical JSON → sha256 → "sha256:<hex>"）。
//
// 覆盖"会被执行的一切"：id/name/version/main（含 main 文件内容）+ tools（含 command 与 schema）
// + hooks（event/tool_name/command）+ skills（name/prompt）。
// 不含运行期字段（Enabled/SourcePath/InstallTime/Fingerprint）。
//
// 字段顺序由结构体字面量固定、map（InputSchema）由 encoding/json 按键排序 ⇒ 同内容必得同指纹。
// 任一必需文件读取失败或序列化失败 ⇒ 返回空串（调用方按"判据不可得" fail-closed，不得当成"无变化"）。
func pluginFingerprint(p *Plugin) string {
	if p == nil {
		return ""
	}
	type toolFP struct {
		Name         string                 `json:"name"`
		Description  string                 `json:"description"`
		Command      string                 `json:"command"`
		RequiredPerm string                 `json:"required_permission"`
		Schema       map[string]interface{} `json:"input_schema,omitempty"`
	}
	type hookFP struct {
		Event    string `json:"event"`
		ToolName string `json:"tool_name"`
		Command  string `json:"command"`
	}
	type skillFP struct {
		Name        string `json:"name"`
		Description string `json:"description"`
		Prompt      string `json:"prompt"`
	}
	type manifestFP struct {
		ID      string    `json:"id"`
		Name    string    `json:"name"`
		Version string    `json:"version"`
		Main    string    `json:"main"`
		Tools   []toolFP  `json:"tools"`
		Hooks   []hookFP  `json:"hooks"`
		Skills  []skillFP `json:"skills"`
		// MainDigest 是 main 入口文件的内容摘要（空表示未声明 main）。
		MainDigest string `json:"main_digest,omitempty"`
	}

	fp := manifestFP{ID: p.ID, Name: p.Name, Version: p.Version, Main: p.Main}
	for _, t := range p.Tools {
		fp.Tools = append(fp.Tools, toolFP{
			Name: t.Name, Description: t.Description, Command: t.Command,
			RequiredPerm: t.RequiredPermission, Schema: t.InputSchema,
		})
	}
	for _, h := range p.Hooks {
		fp.Hooks = append(fp.Hooks, hookFP{Event: h.Event, ToolName: h.ToolName, Command: h.Command})
	}
	for _, s := range p.Skills {
		fp.Skills = append(fp.Skills, skillFP{Name: s.Name, Description: s.Description, Prompt: s.Prompt})
	}
	if p.Main != "" && p.SourcePath != "" {
		data, err := os.ReadFile(filepath.Join(p.SourcePath, p.Main))
		if err != nil {
			log.Warn("Plugin fingerprint: cannot read main file — fingerprint unavailable",
				"id", p.ID, "main", p.Main, "error", err)
			return ""
		}
		sum := sha256.Sum256(data)
		fp.MainDigest = hex.EncodeToString(sum[:])
	}

	data, err := json.Marshal(fp)
	if err != nil {
		log.Warn("Plugin fingerprint: marshal failed — fingerprint unavailable", "id", p.ID, "error", err)
		return ""
	}
	sum := sha256.Sum256(data)
	return "sha256:" + hex.EncodeToString(sum[:])
}

// FormatList formats the plugin list
func (m *Manager) FormatList() string {
	plugins := m.List()
	if len(plugins) == 0 {
		return "No plugins installed. Create a plugin directory and plugin.json under .whale/plugins/ to add plugins."
	}

	var sb strings.Builder
	sb.WriteString(fmt.Sprintf("Installed %d plugins:\n", len(plugins)))
	for _, p := range plugins {
		status := "Enabled"
		if !p.Enabled {
			status = "Disabled"
			// B4：把"为什么它没起来"说清楚，否则 fail-closed 会变成用户眼里的静默失效。
			if p.EnableBlocked != "" {
				status = "Disabled — " + p.EnableBlocked
			}
		}
		sb.WriteString(fmt.Sprintf("  - %s (%s) v%s [%s] %s\n", p.Name, p.ID, p.Version, status, p.Description))
		if len(p.Tools) > 0 {
			sb.WriteString(fmt.Sprintf("    Tools: %d\n", len(p.Tools)))
		}
		if len(p.Hooks) > 0 {
			sb.WriteString(fmt.Sprintf("    Hooks: %d\n", len(p.Hooks)))
		}
	}
	return sb.String()
}
