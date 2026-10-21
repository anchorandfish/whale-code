// hot_reload.go — 钩子配置热重载（M16-02）
// 使用 fsnotify 监听配置文件变更，防抖动后自动重载配置。
// 重载时加写锁（SetConfig 内部锁），hook 执行时加读锁（已实现），互斥保护配置替换。
// 重载失败时保留旧配置 + 输出错误日志。
//
// [C14 复用承诺] 引入 Reloadable 接口，HotReloader 不再硬绑定 *Manager，
// 支持任意实现 Reloadable 的对象（如 command.HistoryStore / command.AliasStore）复用 fsnotify + 防抖动机制。
package hook

import (
	"encoding/json"
	"fmt"
	"log"
	"path/filepath"
	"sync"
	"sync/atomic"
	"time"

	"github.com/fsnotify/fsnotify"
	"gopkg.in/yaml.v3"
)

// Reloadable 可热重载对象接口，HotReloader 监听到文件变更后调用（C14 复用承诺）
// 实现方：*hook.Manager（hook 配置热重载）/ *command.HistoryStore（命令历史热重载）/ *command.AliasStore（别名热重载）
// 实现方需保证：失败时保留旧内存状态 + 返回错误（由 HotReloader 记录 lastErr）
type Reloadable interface {
	// Reload 重新加载指定路径的文件内容到内存（全量替换，非追加合并）
	Reload(path string) error
}

// debounceInterval 防抖动间隔：500ms 内多次变更只触发一次重载
const debounceInterval = 500 * time.Millisecond

// HotReloader 钩子配置热重载器，监听配置文件变化并自动重载
// [SNAPSHOT-HOTRELOADER] ：manager 字段类型从 *Manager 改为 Reloadable 接口（C14 复用抽象）
type HotReloader struct {
	target      Reloadable        // [SNAPSHOT] 关联的可重载对象（原 manager *Manager 改为接口）
	path        string            // 监听的配置文件路径
	watcher     *fsnotify.Watcher // fsnotify 文件监听器
	debouncer   *time.Timer       // 防抖动定时器
	debounceMu  sync.Mutex        // 保护 debouncer
	stopCh      chan struct{}     // 停止信号通道
	doneCh      chan struct{}     // 监听循环完成信号通道
	reloadCount int64             // 重载次数（atomic，测试用）
	lastErr     error             // 最后一次重载错误（测试用）
	errMu       sync.RWMutex      // 保护 lastErr
}

// NewHotReloader 创建热重载器，传入关联的 Manager 和监听的配置文件路径
// 向后兼容：内部委托 NewHotReloaderForReloadable，*Manager 已实现 Reloadable 接口
func NewHotReloader(mgr *Manager, path string) (*HotReloader, error) {
	return NewHotReloaderForReloadable(mgr, path)
}

// NewHotReloaderForReloadable 为任意 Reloadable 对象创建热重载器（C14 复用扩展）
// 允许非 *hook.Manager 的对象（如 command.HistoryStore）复用 HotReloader 的 fsnotify + 防抖动机制
func NewHotReloaderForReloadable(target Reloadable, path string) (*HotReloader, error) {
	watcher, err := fsnotify.NewWatcher()
	if err != nil {
		return nil, fmt.Errorf("create fsnotify watcher: %w", err)
	}
	return &HotReloader{
		target:  target,
		path:    path,
		watcher: watcher,
		stopCh:  make(chan struct{}),
		doneCh:  make(chan struct{}),
	}, nil
}

// Start 启动热重载监听，监听配置文件所在目录（兼容编辑器原子替换）
func (h *HotReloader) Start() error {
	dir := filepath.Dir(h.path)
	if err := h.watcher.Add(dir); err != nil {
		return fmt.Errorf("watch directory %s: %w", dir, err)
	}
	go h.watchLoop()
	log.Printf("[hook] hot reload started for %s", h.path)
	return nil
}

// Stop 停止热重载监听，等待监听 goroutine 退出
func (h *HotReloader) Stop() {
	close(h.stopCh)
	<-h.doneCh
	_ = h.watcher.Close()
	// 停止可能挂起的防抖动定时器
	h.debounceMu.Lock()
	if h.debouncer != nil {
		h.debouncer.Stop()
		h.debouncer = nil
	}
	h.debounceMu.Unlock()
}

// ReloadCount 返回成功重载次数（测试用）
func (h *HotReloader) ReloadCount() int64 {
	return atomic.LoadInt64(&h.reloadCount)
}

// LastReloadError 返回最后一次重载错误，成功时返回 nil（测试用）
func (h *HotReloader) LastReloadError() error {
	h.errMu.RLock()
	defer h.errMu.RUnlock()
	return h.lastErr
}

// watchLoop 监听循环，处理 fsnotify 事件和错误
func (h *HotReloader) watchLoop() {
	defer close(h.doneCh)
	for {
		select {
		case <-h.stopCh:
			return
		case event, ok := <-h.watcher.Events:
			if !ok {
				return
			}
			h.handleEvent(event)
		case err, ok := <-h.watcher.Errors:
			if !ok {
				return
			}
			log.Printf("[hook] hot reload watcher error: %v", err)
		}
	}
}

// handleEvent 处理文件变更事件，按事件类型决定是否触发防抖动重载
func (h *HotReloader) handleEvent(event fsnotify.Event) {
	// 只处理目标文件的事件（监听的是目录，需过滤）
	if filepath.Clean(event.Name) != filepath.Clean(h.path) {
		return
	}
	// 文件删除：不重载，保留旧配置
	if event.Op&fsnotify.Remove == fsnotify.Remove {
		log.Printf("[hook] hot reload: config file removed, keeping old config: %s", h.path)
		h.setLastError(fmt.Errorf("config file removed"))
		return
	}
	// 文件创建/写入/重命名：触发防抖动重载
	if event.Op&(fsnotify.Write|fsnotify.Create|fsnotify.Rename) != 0 {
		h.scheduleReload()
	}
}

// scheduleReload 安排防抖动重载，500ms 内多次变更只触发一次
func (h *HotReloader) scheduleReload() {
	h.debounceMu.Lock()
	defer h.debounceMu.Unlock()
	if h.debouncer != nil {
		h.debouncer.Stop() // 重置定时器（防抖动核心逻辑）
	}
	h.debouncer = time.AfterFunc(debounceInterval, h.reloadNow)
}

// reloadNow 立即执行重载：委托 target.Reload（OPT-01 重构）
// [OPT-01] 原 h.manager.SetConfig(newCfg) 改为 h.target.Reload(h.path)
// 行为等价：*Manager.Reload 内部读文件 + parseConfigBytes + SetConfig（全量替换，非追加合并）
// 重载失败时保留旧状态 + 输出错误日志（由 Reload 实现方保证不破坏旧状态）
func (h *HotReloader) reloadNow() {
	if err := h.target.Reload(h.path); err != nil {
		log.Printf("[hook] hot reload: reload failed, keeping old state: %v", err)
		h.setLastError(err)
		return
	}
	atomic.AddInt64(&h.reloadCount, 1)
	h.setLastError(nil)
	log.Printf("[hook] hot reload: reloaded successfully from %s", h.path)
}

// setLastError 设置最后一次重载错误（线程安全）
func (h *HotReloader) setLastError(err error) {
	h.errMu.Lock()
	defer h.errMu.Unlock()
	h.lastErr = err
}

// parseConfigBytes 解析配置字节到新的 HookConfig（不修改 Manager 状态）
// 优先尝试 JSON，失败则尝试 yaml.v3（YAML 1.2 完整支持， SNAPSHOT）
// 对非空输入产生空配置的情况视为无效（防止垃圾输入被静默接受为空配置）
//
// 修复：空白输入短路返回空配置。
// yaml.v3 对含制表符的空白输入会报 "found character that cannot start any token"
// （YAML 禁止 Tab 缩进）。短路在 yaml.v3 之前处理，与旧 parseSimpleYAMLInto 行为一致。
func parseConfigBytes(data []byte) (HookConfig, error) {
	// 0. 空白输入短路（允许用户清空所有钩子，与旧解析器行为一致）
	if !hasNonWhitespaceContent(data) {
		return HookConfig{}, nil
	}
	var cfg HookConfig
	// 1. JSON 优先（向后兼容 hooks.json）
	if err := json.Unmarshal(data, &cfg); err == nil {
		return cfg, nil
	}
	// 2. JSON 失败，尝试 yaml.v3（YAML 1.2 完整支持）
	var yamlCfg HookConfig
	if err := yaml.Unmarshal(data, &yamlCfg); err != nil {
		return HookConfig{}, fmt.Errorf("failed to parse config (json+yaml.v3): %w", err)
	}
	// 验证：yaml.v3 对垃圾输入会产生空配置，需检测并拒绝
	// 仅当输入有实际内容但解析结果为空时才视为无效（允许用户写空配置清空钩子）
	if isConfigEmpty(yamlCfg) {
		return HookConfig{}, fmt.Errorf("invalid config: json parse failed and yaml.v3 produced empty config")
	}
	return yamlCfg, nil
}

// isConfigEmpty 检查配置是否所有钩子列表都为空
func isConfigEmpty(cfg HookConfig) bool {
	return len(cfg.PreToolUse) == 0 && len(cfg.PostToolUse) == 0 &&
		len(cfg.PreRead) == 0 && len(cfg.PostRead) == 0 &&
		len(cfg.PreWrite) == 0 && len(cfg.PostWrite) == 0 &&
		len(cfg.PreEdit) == 0 && len(cfg.PostEdit) == 0 &&
		len(cfg.PreBash) == 0 && len(cfg.PostBash) == 0 &&
		len(cfg.PreSearch) == 0 && len(cfg.PostSearch) == 0 &&
		len(cfg.PreGit) == 0 && len(cfg.PostGit) == 0 &&
		len(cfg.PreWeb) == 0 && len(cfg.PostWeb) == 0 &&
		len(cfg.PreAgent) == 0 && len(cfg.PostAgent) == 0 &&
		len(cfg.SessionStart) == 0 && len(cfg.SessionStop) == 0 &&
		len(cfg.Stop) == 0 && len(cfg.PostCompact) == 0 &&
		len(cfg.Notification) == 0
}

// hasNonWhitespaceContent 检查字节流是否包含非空白字符
func hasNonWhitespaceContent(data []byte) bool {
	for _, b := range data {
		if b != ' ' && b != '\t' && b != '\n' && b != '\r' {
			return true
		}
	}
	return false
}
